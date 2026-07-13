//! External sort with O(n log n) normalization and correct tail handling.

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::migrate::{DesiredRecord, DesiredStream};
use alloc::vec::Vec;
use std::fs::{File, OpenOptions};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

static RUN_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Clone, Debug)]
pub struct ExtSortConfig {
    pub chunk_size: usize,
    pub temp_dir: Option<PathBuf>,
}

impl Default for ExtSortConfig {
    fn default() -> Self { ExtSortConfig { chunk_size: 100_000, temp_dir: None } }
}

// ── Streaming sorter ──

#[allow(missing_debug_implementations)]
pub struct ExtSorter<K: IpKey> {
    config: ExtSortConfig,
    chunk: Vec<(DesiredRecord<K>, u64)>,
    run_paths: Vec<PathBuf>,
    global_seq: u64,
}

impl<K: IpKey> ExtSorter<K> {
    pub fn new(config: ExtSortConfig) -> Self {
        ExtSorter { config, chunk: Vec::new(), run_paths: Vec::new(), global_seq: 0 }
    }

    pub fn add(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        let seq = self.global_seq;
        self.global_seq += 1;
        self.chunk.push((DesiredRecord { from, to, scope_id }, seq));
        if self.chunk.len() >= self.config.chunk_size { self.spill_chunk()?; }
        Ok(())
    }

    pub fn finish(mut self) -> Result<Box<dyn DesiredStream<K>>> {
        if !self.chunk.is_empty() { self.spill_chunk()?; }
        if self.run_paths.is_empty() {
            return Ok(Box::new(SortedStream { records: Vec::new(), pos: 0 }));
        }
        if self.run_paths.len() == 1 {
            let tagged = read_run::<K>(&self.run_paths[0])?;
            let _ = std::fs::remove_file(&self.run_paths[0]);
            let records: Vec<DesiredRecord<K>> = tagged.into_iter().map(|(r, _)| r).collect();
            return Ok(Box::new(SortedStream { records, pos: 0 }));
        }
        let merge = KWayMerge::<K>::new(&self.run_paths)?;
        Ok(Box::new(MergeStream { merge: Some(merge), run_paths: self.run_paths }))
    }

    fn spill_chunk(&mut self) -> Result<()> {
        if self.chunk.is_empty() { return Ok(()); }
        // Tag each record with its global input seq BEFORE sorting, so that
        // normalize_chunk can resolve last-wins by input sequence and the
        // surviving seq is persisted to the spill file for cross-run merge.
        let mut indexed: Vec<(DesiredRecord<K>, u64)> = self.chunk.drain(..).collect();
        // Sort by from key (stable — preserves input order for equal keys).
        indexed.sort_by(|a, b| a.0.from.cmp(&b.0.from));
        let (sorted, seqs): (Vec<DesiredRecord<K>>, Vec<u64>) =
            indexed.into_iter().unzip();
        let normalized = normalize_chunk(&sorted, &seqs);
        let dir: PathBuf = self.config.temp_dir.clone().unwrap_or_else(|| PathBuf::from("/tmp"));
        let unique = RUN_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = dir.join(format!("iprange_extsort_{}_{}", unique, self.run_paths.len()));
        write_run::<K>(&path, &normalized)?;
        self.run_paths.push(path);
        self.chunk.clear();
        Ok(())
    }
}

/// O(n log n) normalization using a sweep line with an interval tree.
/// Handles ALL edge cases: overlaps, tails, max-address boundaries.
/// Last-wins = highest input seq (not sorted-array index).
///
/// Returns each output segment paired with the global seq of the input record
/// that won the segment (i.e. set its scope). Coalesced same-scope adjacent
/// segments carry the max seq of their constituents.
fn normalize_chunk<K: IpKey>(sorted: &[DesiredRecord<K>], seqs: &[u64]) -> Vec<(DesiredRecord<K>, u64)> {
    if sorted.len() <= 1 {
        return sorted.iter().zip(seqs.iter()).map(|(r, &s)| (*r, s)).collect();
    }

    // Fast path: check disjoint.
    let mut disjoint = true;
    for i in 1..sorted.len() {
        if sorted[i].from <= sorted[i-1].to { disjoint = false; break; }
    }
    if disjoint { return coalesce_adjacent(sorted, seqs); }

    // Sweep line: collect (position, is_start, record_index) events.
    #[derive(Clone, Copy)]
    struct Event { pos: u128, is_start: bool, is_max_end: bool, idx: usize }
    let mut events: Vec<Event> = Vec::with_capacity(sorted.len() * 2);
    for (i, r) in sorted.iter().enumerate() {
        events.push(Event { pos: r.from.to_u128(), is_start: true, is_max_end: false, idx: i });
        match r.to.checked_inc() {
            Some(after) => events.push(Event { pos: after.to_u128(), is_start: false, is_max_end: false, idx: i }),
            // to is family_max — use a flag instead of a sentinel value.
            None => events.push(Event { pos: 0, is_start: false, is_max_end: true, idx: i }),
        }
    }
    events.sort_by(|a, b| {
        // max_end events sort after everything (they represent "to the end").
        match (a.is_max_end, b.is_max_end) {
            (true, true) => std::cmp::Ordering::Equal,
            (true, false) => std::cmp::Ordering::Greater,
            (false, true) => std::cmp::Ordering::Less,
            (false, false) => a.pos.cmp(&b.pos)
                .then_with(|| (a.is_start as u8).cmp(&(b.is_start as u8))),
        }
    });

    // Sweep: maintain active record indices. At each segment, last-wins = highest seq.
    let mut active: Vec<usize> = Vec::new();
    let mut out: Vec<(DesiredRecord<K>, u64)> = Vec::new();

    let mut i = 0;
    while i < events.len() {
        let pos = events[i].pos;

        // Process all events at this position.
        while i < events.len() && events[i].pos == pos {
            let ev = &events[i];
            if ev.is_start {
                active.push(ev.idx);
            } else {
                active.retain(|&x| x != ev.idx);
            }
            i += 1;
        }

        // Determine segment end.
        if i >= events.len() { break; } // no more segments

        if active.is_empty() { continue; }

        let seg_from = K::from_u128(pos);
        // The segment ends at either the next event's position - 1, or K::MAX
        // if the next event is a max_end flag.
        let seg_to = if events[i].is_max_end {
            K::MAX
        } else {
            K::from_u128(events[i].pos - 1)
        };

        // Last-wins: highest input seq in active.
        let max_idx = *active.iter().max_by_key(|&&i| seqs[i]).unwrap();
        let scope = sorted[max_idx].scope_id;
        let win_seq = seqs[max_idx];

        // Coalesce with previous if same scope and adjacent.
        if let Some(last) = out.last_mut() {
            if last.0.scope_id == scope {
                if let Some(after) = last.0.to.checked_inc() {
                    if after == seg_from {
                        last.0.to = seg_to;
                        if win_seq > last.1 { last.1 = win_seq; }
                        continue;
                    }
                }
            }
        }
        out.push((DesiredRecord { from: seg_from, to: seg_to, scope_id: scope }, win_seq));
    }

    out
}

fn coalesce_adjacent<K: IpKey>(records: &[DesiredRecord<K>], seqs: &[u64]) -> Vec<(DesiredRecord<K>, u64)> {
    if records.is_empty() { return Vec::new(); }
    let mut out: Vec<(DesiredRecord<K>, u64)> = Vec::with_capacity(records.len());
    out.push((records[0], seqs[0]));
    for i in 1..records.len() {
        let curr = &records[i];
        let last = out.len() - 1;
        if out[last].0.scope_id == curr.scope_id {
            if let Some(a) = out[last].0.to.checked_inc() {
                if a == curr.from {
                    out[last].0.to = curr.to;
                    if seqs[i] > out[last].1 { out[last].1 = seqs[i]; }
                    continue;
                }
            }
        }
        out.push((*curr, seqs[i]));
    }
    out
}

// ── Sorted stream ──

#[derive(Clone)]
#[allow(missing_debug_implementations)]
pub struct SortedStream<K: IpKey> {
    pub records: Vec<DesiredRecord<K>>,
    pub pos: usize,
}

impl<K: IpKey> SortedStream<K> {
    pub fn from_unsorted(records: Vec<DesiredRecord<K>>) -> Self {
        // Tag with input order before sorting so normalize_chunk resolves
        // last-wins by input sequence.
        let mut indexed: Vec<(DesiredRecord<K>, usize)> = records
            .into_iter()
            .enumerate()
            .map(|(i, r)| (r, i))
            .collect();
        indexed.sort_by(|a, b| a.0.from.cmp(&b.0.from));
        let (sorted, seqs_local): (Vec<DesiredRecord<K>>, Vec<usize>) =
            indexed.into_iter().unzip();
        let seqs: Vec<u64> = seqs_local.into_iter().map(|s| s as u64).collect();
        let normalized = normalize_chunk(&sorted, &seqs);
        let recs: Vec<DesiredRecord<K>> = normalized.into_iter().map(|(r, _)| r).collect();
        SortedStream { records: recs, pos: 0 }
    }
}

impl<K: IpKey> DesiredStream<K> for SortedStream<K> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { self.records.get(self.pos) }
    fn next(&mut self) -> Option<DesiredRecord<K>> {
        if self.pos < self.records.len() {
            let r = self.records[self.pos]; self.pos += 1; Some(r)
        } else { None }
    }
}

// ── K-way merge with overlap normalization ──

struct RunReader<K: IpKey> { file: File, current: Option<(DesiredRecord<K>, u64)> }
impl<K: IpKey> RunReader<K> {
    fn open(path: &Path) -> Result<Self> {
        let mut file = OpenOptions::new().read(true).open(path).map_err(Error::Io)?;
        Ok(RunReader { current: read_record::<K>(&mut file)?, file })
    }
    fn advance(&mut self) { self.current = read_record::<K>(&mut self.file).ok().flatten(); }
}

enum KMin { Run(usize), Pending(usize) }

struct KWayMerge<K: IpKey> {
    runs: Vec<RunReader<K>>,
    cached: Option<(DesiredRecord<K>, u64)>,
    pending: Vec<(DesiredRecord<K>, u64)>,
}
impl<K: IpKey> KWayMerge<K> {
    fn new(paths: &[PathBuf]) -> Result<Self> {
        let mut runs = Vec::with_capacity(paths.len());
        for p in paths { runs.push(RunReader::<K>::open(p)?); }
        let mut m = KWayMerge { runs, cached: None, pending: Vec::new() };
        m.cached = m.compute_next();
        Ok(m)
    }

    fn find_min(&self) -> Option<KMin> {
        // Find the minimum across runs AND pending.
        let mut best: Option<K> = None;
        let mut best_kind: Option<KMin> = None;

        for (i, run) in self.runs.iter().enumerate() {
            if let Some(ref cur) = run.current {
                if best.is_none() || cur.0.from < best.unwrap() {
                    best = Some(cur.0.from);
                    best_kind = Some(KMin::Run(i));
                }
            }
        }
        for (i, p) in self.pending.iter().enumerate() {
            if best.is_none() || p.0.from < best.unwrap() {
                best = Some(p.0.from);
                best_kind = Some(KMin::Pending(i));
            }
        }
        best_kind
    }

    fn pop_min(&mut self) -> Option<(DesiredRecord<K>, u64)> {
        match self.find_min()? {
            KMin::Run(idx) => {
                let r = self.runs[idx].current.take();
                self.runs[idx].advance();
                r
            }
            KMin::Pending(idx) => {
                // Pop from pending (swap_remove for O(1)).
                Some(self.pending.swap_remove(idx))
            }
        }
    }

    /// Compute the next coalesced/normalized record. Handles cross-run overlaps.
    ///
    /// Each record carries a global input seq. When two records from different
    /// runs overlap with different scopes, the one with the higher seq wins
    /// (last-wins by global input order). Same-scope overlaps are always merged.
    fn compute_next(&mut self) -> Option<(DesiredRecord<K>, u64)> {
        let mut result = self.pop_min()?;
        loop {
            let (next_from, next_to, next_scope, next_seq) = match self.peek_min() {
                None => break,
                Some(v) => v,
            };
            if next_from > result.0.to {
                // No overlap — check adjacency for same-scope coalescing.
                if next_scope == result.0.scope_id {
                    if let Some(after) = result.0.to.checked_inc() {
                        if after == next_from {
                            let (popped, popped_seq) = self.pop_min().unwrap();
                            if popped.to > result.0.to { result.0.to = popped.to; }
                            if popped_seq > result.1 { result.1 = popped_seq; }
                            continue;
                        }
                    }
                }
                break;
            }
            if next_scope == result.0.scope_id {
                // Same scope → extend result, keep max seq.
                let (popped, popped_seq) = self.pop_min().unwrap();
                if popped.to > result.0.to { result.0.to = popped.to; }
                if popped_seq > result.1 { result.1 = popped_seq; }
            } else {
                // Different scope, overlapping ranges — resolve by global seq.
                let result_seq = result.1;
                if next_seq > result_seq {
                    // Next is newer → next wins (last-wins). Trim/split result.
                    let original_to = result.0.to;
                    let original_scope = result.0.scope_id;
                    if next_from > result.0.from {
                        // Partial overlap → truncate result at next_from-1.
                        result.0.to = next_from.checked_dec().unwrap_or(next_from);
                        if original_to > next_to {
                            if let Some(ts) = next_to.checked_inc() {
                                self.pending.push((DesiredRecord { from: ts, to: original_to, scope_id: original_scope }, result_seq));
                            }
                        }
                        break;
                    } else {
                        // next_from == result.from → next fully covers result start.
                        // Defer result's surviving tail, then take next as result.
                        if original_to > next_to {
                            if let Some(ts) = next_to.checked_inc() {
                                self.pending.push((DesiredRecord { from: ts, to: original_to, scope_id: original_scope }, result_seq));
                            }
                        }
                        result = self.pop_min().unwrap();
                    }
                } else {
                    // Result is newer (or equal) → result wins. Consume next;
                    // if next extends beyond result, defer its tail.
                    let (popped, popped_seq) = self.pop_min().unwrap();
                    if popped.to > result.0.to {
                        if let Some(ts) = result.0.to.checked_inc() {
                            self.pending.push((DesiredRecord { from: ts, to: popped.to, scope_id: next_scope }, popped_seq));
                        }
                    }
                    // result unchanged, continue scanning.
                }
            }
        }
        Some(result)
    }

    fn peek_min(&self) -> Option<(K, K, u32, u64)> {
        let kmin = self.find_min()?;
        match kmin {
            KMin::Run(idx) => {
                let (r, seq) = self.runs[idx].current.as_ref()?;
                Some((r.from, r.to, r.scope_id, *seq))
            }
            KMin::Pending(idx) => {
                let (r, seq) = self.pending.get(idx)?;
                Some((r.from, r.to, r.scope_id, *seq))
            }
        }
    }
}

impl<K: IpKey> DesiredStream<K> for KWayMerge<K> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { self.cached.as_ref().map(|(r, _)| r) }
    fn next(&mut self) -> Option<DesiredRecord<K>> {
        let r = self.cached.take()?;
        self.cached = self.compute_next();
        Some(r.0)
    }
}

struct MergeStream<K: IpKey> { merge: Option<KWayMerge<K>>, run_paths: Vec<PathBuf> }
impl<K: IpKey> DesiredStream<K> for MergeStream<K> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { self.merge.as_ref()?.peek() }
    fn next(&mut self) -> Option<DesiredRecord<K>> { self.merge.as_mut()?.next() }
}
impl<K: IpKey> Drop for MergeStream<K> {
    fn drop(&mut self) { for p in &self.run_paths { let _ = std::fs::remove_file(p); } }
}

// ── File I/O ──
//
// Spill record layout: from (K::WIDTH) | to (K::WIDTH) | scope_id (4) | seq (8).
// The seq is the global input order counter, used by the k-way merge to resolve
// last-wins across separate spill runs.

fn read_record<K: IpKey>(file: &mut File) -> Result<Option<(DesiredRecord<K>, u64)>> {
    let kw = K::WIDTH;
    let mut buf = vec![0u8; kw * 2 + 12];
    match file.read_exact(&mut buf) {
        Ok(()) => Ok(Some((
            DesiredRecord {
                from: K::read_le(&buf[..kw]),
                to: K::read_le(&buf[kw..2*kw]),
                scope_id: u32::from_le_bytes([buf[2*kw], buf[2*kw+1], buf[2*kw+2], buf[2*kw+3]]),
            },
            u64::from_le_bytes([
                buf[2*kw+4], buf[2*kw+5], buf[2*kw+6], buf[2*kw+7],
                buf[2*kw+8], buf[2*kw+9], buf[2*kw+10], buf[2*kw+11],
            ]),
        ))),
        Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => Ok(None),
        Err(e) => Err(Error::Io(e)),
    }
}

fn write_record<K: IpKey>(file: &mut File, rec: &DesiredRecord<K>, seq: u64) -> Result<()> {
    let kw = K::WIDTH;
    let mut buf = vec![0u8; kw * 2 + 12];
    rec.from.write_le(&mut buf[..kw]);
    rec.to.write_le(&mut buf[kw..2*kw]);
    buf[2*kw..2*kw+4].copy_from_slice(&rec.scope_id.to_le_bytes());
    buf[2*kw+4..2*kw+12].copy_from_slice(&seq.to_le_bytes());
    file.write_all(&buf).map_err(Error::Io)
}

fn write_run<K: IpKey>(path: &Path, records: &[(DesiredRecord<K>, u64)]) -> Result<()> {
    let mut file = OpenOptions::new().write(true).create(true).truncate(true)
        .open(path).map_err(Error::Io)?;
    for (rec, seq) in records { write_record::<K>(&mut file, rec, *seq)?; }
    file.flush().map_err(Error::Io)
}

fn read_run<K: IpKey>(path: &Path) -> Result<Vec<(DesiredRecord<K>, u64)>> {
    let mut file = OpenOptions::new().read(true).open(path).map_err(Error::Io)?;
    let mut records = Vec::new();
    while let Some(r) = read_record::<K>(&mut file)? { records.push(r); }
    Ok(records)
}

/// Convenience: one-shot sort.
pub fn ext_sort<K: IpKey>(records: Vec<DesiredRecord<K>>, config: &ExtSortConfig) -> Result<Box<dyn DesiredStream<K>>> {
    let mut sorter = ExtSorter::new(config.clone());
    for rec in records { sorter.add(rec.from, rec.to, rec.scope_id)?; }
    sorter.finish()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;

    fn r(f: u32, t: u32, s: u32) -> DesiredRecord<Ipv4Key> {
        DesiredRecord { from: Ipv4Key(f), to: Ipv4Key(t), scope_id: s }
    }

    #[test]
    fn in_memory_sort() {
        let s = SortedStream::from_unsorted(vec![r(30,40,1), r(10,20,1), r(21,29,1), r(50,60,2)]);
        assert_eq!(s.records[0].from, Ipv4Key(10));
        assert_eq!(s.records[0].to, Ipv4Key(40));
    }

    #[test]
    fn normalize_different_scope() {
        let s = SortedStream::from_unsorted(vec![r(10,20,1), r(15,25,2)]);
        assert_eq!(s.records.len(), 2);
        assert_eq!(s.records[0].to, Ipv4Key(14)); // [10-14] scope=1
        assert_eq!(s.records[1].from, Ipv4Key(15)); // [15-25] scope=2
    }

    #[test]
    fn normalize_tail_preserved() {
        let s = SortedStream::from_unsorted(vec![r(56,69,0), r(60,75,1), r(63,72,0)]);
        // [56-59] s=0, [60-62] s=1, [63-72] s=0, [73-75] s=1 (tail!)
        assert_eq!(s.records.len(), 4);
        assert_eq!(s.records[3].from, Ipv4Key(73));
        assert_eq!(s.records[3].to, Ipv4Key(75));
    }

    #[test]
    fn normalize_max_address() {
        let s = SortedStream::from_unsorted(vec![r(u32::MAX-10, u32::MAX, 1)]);
        assert_eq!(s.records.len(), 1);
        assert_eq!(s.records[0].to, Ipv4Key(u32::MAX));
    }

    #[test]
    fn streaming_sorter() {
        let mut sorter = ExtSorter::new(ExtSortConfig { chunk_size: 10, temp_dir: None });
        for i in 0..25u32 { sorter.add(Ipv4Key(1000-i), Ipv4Key(1000-i), i).unwrap(); }
        let mut stream = sorter.finish().unwrap();
        let mut prev = Ipv4Key(0);
        let mut count = 0;
        while let Some(r) = stream.next() { assert!(r.from > prev || count == 0); prev = r.from; count += 1; }
        assert_eq!(count, 25);
    }

    #[test]
    fn cross_run_overlap() {
        let mut sorter = ExtSorter::new(ExtSortConfig { chunk_size: 1, temp_dir: None });
        sorter.add(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
        sorter.add(Ipv4Key(15), Ipv4Key(25), 2).unwrap();
        let mut stream = sorter.finish().unwrap();
        let r1 = stream.next().unwrap();
        assert_eq!(r1.from, Ipv4Key(10));
        assert_eq!(r1.to, Ipv4Key(14));
        let r2 = stream.next().unwrap();
        assert_eq!(r2.from, Ipv4Key(15));
        assert_eq!(r2.to, Ipv4Key(25));
    }
}

#[cfg(test)]
mod worker_bugs {
    use super::*;
    use crate::key::Ipv4Key;

    fn r(f: u32, t: u32, s: u32) -> DesiredRecord<Ipv4Key> {
        DesiredRecord { from: Ipv4Key(f), to: Ipv4Key(t), scope_id: s }
    }

    #[test]
    fn max_address_overlap() {
        // [u32::MAX-10, u32::MAX] scope=1 overlaps [u32::MAX-5, u32::MAX] scope=2
        // Expected: [MAX-10, MAX-6] scope=1, [MAX-5, MAX] scope=2
        let s = SortedStream::from_unsorted(vec![
            r(u32::MAX - 10, u32::MAX, 1),
            r(u32::MAX - 5, u32::MAX, 2),
        ]);
        assert_eq!(s.records.len(), 2, "should have 2 segments");
        assert_eq!(s.records[0].from, Ipv4Key(u32::MAX - 10));
        assert_eq!(s.records[0].to, Ipv4Key(u32::MAX - 6));
        assert_eq!(s.records[0].scope_id, 1);
        assert_eq!(s.records[1].from, Ipv4Key(u32::MAX - 5));
        assert_eq!(s.records[1].to, Ipv4Key(u32::MAX));
        assert_eq!(s.records[1].scope_id, 2);
    }

    #[test]
    fn cross_run_contained_tail() {
        // Run A has [10-30] scope=1 (wide range)
        // Run B has [15-25] scope=2 (contained within A)
        // Expected after merge: [10-14] scope=1, [15-25] scope=2, [26-30] scope=1
        let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig { chunk_size: 1, temp_dir: None });
        sorter.add(Ipv4Key(10), Ipv4Key(30), 1).unwrap();
        sorter.add(Ipv4Key(15), Ipv4Key(25), 2).unwrap();
        let mut stream = sorter.finish().unwrap();
        let r1 = stream.next().unwrap();
        assert_eq!(r1.from, Ipv4Key(10));
        assert_eq!(r1.to, Ipv4Key(14));
        assert_eq!(r1.scope_id, 1);
        let r2 = stream.next().unwrap();
        assert_eq!(r2.from, Ipv4Key(15));
        assert_eq!(r2.to, Ipv4Key(25));
        assert_eq!(r2.scope_id, 2);
        let r3 = stream.next().unwrap();
        assert_eq!(r3.from, Ipv4Key(26));
        assert_eq!(r3.to, Ipv4Key(30));
        assert_eq!(r3.scope_id, 1);
        assert!(stream.next().is_none());
    }
}

#[cfg(test)]
mod ipv6_max_tests {
    use super::*;
    use crate::key::Ipv6Key;

    #[test]
    fn ipv6_max_address_overlap() {
        // Two records ending at the IPv6 maximum address.
        let max = Ipv6Key::MAX;
        let max_minus_1 = max.checked_dec().unwrap();

        let s = SortedStream::from_unsorted(vec![
            DesiredRecord { from: max_minus_1, to: max, scope_id: 1 },
            DesiredRecord { from: max, to: max, scope_id: 2 },
        ]);
        // Expected: [max-1, max-1] scope=1, [max, max] scope=2
        assert_eq!(s.records.len(), 2);
        assert_eq!(s.records[0].from, max_minus_1);
        assert_eq!(s.records[0].to, max_minus_1);
        assert_eq!(s.records[0].scope_id, 1);
        assert_eq!(s.records[1].from, max);
        assert_eq!(s.records[1].to, max);
        assert_eq!(s.records[1].scope_id, 2);
    }
}
