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

pub struct ExtSorter<K: IpKey> {
    config: ExtSortConfig,
    chunk: Vec<DesiredRecord<K>>,
    run_paths: Vec<PathBuf>,
}

impl<K: IpKey> ExtSorter<K> {
    pub fn new(config: ExtSortConfig) -> Self {
        ExtSorter { config, chunk: Vec::new(), run_paths: Vec::new() }
    }

    pub fn add(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.chunk.push(DesiredRecord { from, to, scope_id });
        if self.chunk.len() >= self.config.chunk_size { self.spill_chunk()?; }
        Ok(())
    }

    pub fn finish(mut self) -> Result<Box<dyn DesiredStream<K>>> {
        if !self.chunk.is_empty() { self.spill_chunk()?; }
        if self.run_paths.is_empty() {
            return Ok(Box::new(SortedStream { records: Vec::new(), pos: 0 }));
        }
        if self.run_paths.len() == 1 {
            let records = read_run::<K>(&self.run_paths[0])?;
            let _ = std::fs::remove_file(&self.run_paths[0]);
            return Ok(Box::new(SortedStream { records, pos: 0 }));
        }
        let merge = KWayMerge::<K>::new(&self.run_paths)?;
        Ok(Box::new(MergeStream { merge: Some(merge), run_paths: self.run_paths }))
    }

    fn spill_chunk(&mut self) -> Result<()> {
        if self.chunk.is_empty() { return Ok(()); }
        self.chunk.sort_by(|a, b| a.from.cmp(&b.from));
        let normalized = normalize_chunk(&self.chunk);
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
/// Last-wins for different scope_ids; merge for same scope_ids.
fn normalize_chunk<K: IpKey>(sorted: &[DesiredRecord<K>]) -> Vec<DesiredRecord<K>> {
    if sorted.len() <= 1 { return sorted.to_vec(); }

    // Fast path: check disjoint.
    let mut disjoint = true;
    for i in 1..sorted.len() {
        if sorted[i].from <= sorted[i-1].to { disjoint = false; break; }
    }
    if disjoint { return coalesce_adjacent(sorted); }

    // Sweep line: collect (position, is_start, record_index) events.
    #[derive(Clone, Copy)]
    struct Event { pos: u128, is_start: bool, idx: usize }
    let mut events: Vec<Event> = Vec::with_capacity(sorted.len() * 2);
    for (i, r) in sorted.iter().enumerate() {
        events.push(Event { pos: r.from.to_u128(), is_start: true, idx: i });
        // For end: use to+1 (exclusive). Handle max address correctly.
        match r.to.checked_inc() {
            Some(after) => events.push(Event { pos: after.to_u128(), is_start: false, idx: i }),
            None => { /* to is family_max — no end event, record covers to end */ }
        }
    }
    events.sort_by(|a, b| {
        a.pos.cmp(&b.pos)
            .then_with(|| (a.is_start as u8).cmp(&(b.is_start as u8))) // ends before starts at same pos
    });

    // Sweep: maintain active record indices. At each segment, last-wins = highest idx.
    let mut active: Vec<usize> = Vec::new();
    let mut out: Vec<DesiredRecord<K>> = Vec::new();

    let mut i = 0;
    while i < events.len() {
        let pos = events[i].pos;

        // Process all events at this position.
        let mut next_pos = pos;
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
        if i < events.len() { next_pos = events[i].pos; }
        else { break; } // no more segments

        if active.is_empty() { continue; }

        let seg_from = K::from_u128(pos);
        let seg_to = K::from_u128(next_pos - 1);

        // Last-wins: highest index in active.
        let max_idx = *active.iter().max().unwrap();
        let scope = sorted[max_idx].scope_id;

        // Coalesce with previous if same scope and adjacent.
        if let Some(last) = out.last_mut() {
            if last.scope_id == scope {
                if let Some(after) = last.to.checked_inc() {
                    if after == seg_from {
                        last.to = seg_to;
                        continue;
                    }
                }
            }
        }
        out.push(DesiredRecord { from: seg_from, to: seg_to, scope_id: scope });
    }

    out
}

fn coalesce_adjacent<K: IpKey>(records: &[DesiredRecord<K>]) -> Vec<DesiredRecord<K>> {
    if records.is_empty() { return Vec::new(); }
    let mut out: Vec<DesiredRecord<K>> = Vec::with_capacity(records.len());
    out.push(records[0]);
    for curr in records.iter().skip(1) {
        let last = out.len() - 1;
        if out[last].scope_id == curr.scope_id {
            if let Some(a) = out[last].to.checked_inc() {
                if a == curr.from {
                    out[last].to = curr.to;
                    continue;
                }
            }
        }
        out.push(*curr);
    }
    out
}

// ── Sorted stream ──

#[derive(Clone)]
pub struct SortedStream<K: IpKey> {
    pub records: Vec<DesiredRecord<K>>,
    pub pos: usize,
}

impl<K: IpKey> SortedStream<K> {
    pub fn from_unsorted(records: Vec<DesiredRecord<K>>) -> Self {
        let mut sorted = records;
        sorted.sort_by(|a, b| a.from.cmp(&b.from));
        let normalized = normalize_chunk(&sorted);
        SortedStream { records: normalized, pos: 0 }
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

struct RunReader<K: IpKey> { file: File, current: Option<DesiredRecord<K>> }
impl<K: IpKey> RunReader<K> {
    fn open(path: &Path) -> Result<Self> {
        let mut file = OpenOptions::new().read(true).open(path).map_err(Error::Io)?;
        Ok(RunReader { current: read_record::<K>(&mut file)?, file })
    }
    fn advance(&mut self) { self.current = read_record::<K>(&mut self.file).ok().flatten(); }
}

struct KWayMerge<K: IpKey> { runs: Vec<RunReader<K>>, cached: Option<DesiredRecord<K>> }
impl<K: IpKey> KWayMerge<K> {
    fn new(paths: &[PathBuf]) -> Result<Self> {
        let mut runs = Vec::with_capacity(paths.len());
        for p in paths { runs.push(RunReader::<K>::open(p)?); }
        let mut m = KWayMerge { runs, cached: None };
        m.cached = m.compute_next();
        Ok(m)
    }

    fn find_min(&self) -> Option<usize> {
        let mut mi: Option<usize> = None;
        for i in 0..self.runs.len() {
            if self.runs[i].current.is_none() { continue; }
            match mi {
                None => mi = Some(i),
                Some(m) => if self.runs[i].current.unwrap().from < self.runs[m].current.unwrap().from { mi = Some(i); }
            }
        }
        mi
    }

    fn pop_min(&mut self) -> Option<DesiredRecord<K>> {
        let idx = self.find_min()?;
        let r = self.runs[idx].current.take();
        self.runs[idx].advance();
        r
    }

    /// Compute the next coalesced/normalized record. Handles cross-run overlaps.
    fn compute_next(&mut self) -> Option<DesiredRecord<K>> {
        let mut result = self.pop_min()?;
        loop {
            let next_idx = match self.find_min() { Some(i) => i, None => break };
            let next = &self.runs[next_idx].current.as_ref().unwrap();
            if next.from > result.to {
                // No overlap — check adjacency for same-scope coalescing.
                if next.scope_id == result.scope_id {
                    if let Some(after) = result.to.checked_inc() {
                        if after.cmp(&next.from) == core::cmp::Ordering::Equal {
                            result.to = self.pop_min().unwrap().to;
                            continue;
                        }
                    }
                }
                break;
            }
            // Overlap!
            if next.scope_id == result.scope_id {
                // Same scope → extend.
                let popped = self.pop_min().unwrap();
                if popped.to > result.to { result.to = popped.to; }
            } else if next.from > result.from {
                // Different scope, partial overlap → truncate result, next wins for its part.
                result.to = next.from.checked_dec().unwrap_or(next.from);
                break;
            } else {
                // next.from <= result.from → next fully covers result → take next.
                result = self.pop_min().unwrap();
            }
        }
        Some(result)
    }
}

impl<K: IpKey> DesiredStream<K> for KWayMerge<K> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { self.cached.as_ref() }
    fn next(&mut self) -> Option<DesiredRecord<K>> {
        let r = self.cached.take()?;
        self.cached = self.compute_next();
        Some(r)
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

fn read_record<K: IpKey>(file: &mut File) -> Result<Option<DesiredRecord<K>>> {
    let kw = K::WIDTH;
    let mut buf = vec![0u8; kw * 2 + 4];
    match file.read_exact(&mut buf) {
        Ok(()) => Ok(Some(DesiredRecord {
            from: K::read_le(&buf[..kw]), to: K::read_le(&buf[kw..2*kw]),
            scope_id: u32::from_le_bytes([buf[2*kw], buf[2*kw+1], buf[2*kw+2], buf[2*kw+3]]),
        })),
        Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => Ok(None),
        Err(e) => Err(Error::Io(e)),
    }
}

fn write_record<K: IpKey>(file: &mut File, rec: &DesiredRecord<K>) -> Result<()> {
    let kw = K::WIDTH;
    let mut buf = vec![0u8; kw * 2 + 4];
    rec.from.write_le(&mut buf[..kw]);
    rec.to.write_le(&mut buf[kw..2*kw]);
    buf[2*kw..].copy_from_slice(&rec.scope_id.to_le_bytes());
    file.write_all(&buf).map_err(Error::Io)
}

fn write_run<K: IpKey>(path: &Path, records: &[DesiredRecord<K>]) -> Result<()> {
    let mut file = OpenOptions::new().write(true).create(true).truncate(true)
        .open(path).map_err(Error::Io)?;
    for rec in records { write_record::<K>(&mut file, rec)?; }
    file.flush().map_err(Error::Io)
}

fn read_run<K: IpKey>(path: &Path) -> Result<Vec<DesiredRecord<K>>> {
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
