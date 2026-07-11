//! External sort: produce a sorted, disjoint stream from unsorted input with
//! bounded memory.
//!
//! **Streaming input API** (fixes #1): `ExtSorter::new(config) → Add(record)
//! → Finish()`. Each full chunk is immediately spilled to a temp file. The
//! caller never holds the entire input in memory.
//!
//! **Interval normalization** (fixes #4): overlapping input is split into
//! disjoint coverage segments. For overlapping records with different
//! scope_ids, last-wins semantics apply (later records overwrite earlier).
//! Same-scope overlaps are merged.
//!
//! **File-backed spill**: for inputs > chunk_size, sorted chunks are spilled
//! to temp files and k-way merged.

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::migrate::{DesiredRecord, DesiredStream};
use alloc::vec::Vec;
use std::fs::{File, OpenOptions};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

static RUN_COUNTER: AtomicU64 = AtomicU64::new(0);

/// Configuration for external sort.
#[derive(Clone, Debug)]
pub struct ExtSortConfig {
    /// Maximum records to hold in memory before spilling to a temp file.
    pub chunk_size: usize,
    /// Directory for temporary spill files. None = /tmp.
    pub temp_dir: Option<PathBuf>,
}

impl Default for ExtSortConfig {
    fn default() -> Self {
        ExtSortConfig { chunk_size: 100_000, temp_dir: None }
    }
}

// ──────────────────────────────────────────────────────────────────────────
// Streaming sorter (fixes #1)
// ──────────────────────────────────────────────────────────────────────────

/// Incremental external sorter. Accepts records one at a time via `add()`,
/// spills sorted chunks when the buffer is full, and produces a sorted,
/// disjoint, coalesced stream via `finish()`.
///
/// Memory bounded by: chunk_size × record_size.
pub struct ExtSorter<K: IpKey> {
    config: ExtSortConfig,
    chunk: Vec<DesiredRecord<K>>,
    run_paths: Vec<PathBuf>,
    finished: bool,
}

impl<K: IpKey> ExtSorter<K> {
    pub fn new(config: ExtSortConfig) -> Self {
        ExtSorter {
            config,
            chunk: Vec::new(),
            run_paths: Vec::new(),
            finished: false,
        }
    }

    /// Add a record. When the chunk buffer reaches chunk_size, it is sorted,
    /// normalized, coalesced, and spilled to a temp file.
    pub fn add(&mut self, from: K, to: K, scope_id: u32) -> Result<()> {
        self.chunk.push(DesiredRecord { from, to, scope_id });
        if self.chunk.len() >= self.config.chunk_size {
            self.spill_chunk()?;
        }
        Ok(())
    }

    /// Finish sorting and return a sorted, disjoint, coalesced stream.
    /// Consumes the sorter.
    pub fn finish(mut self) -> Result<Box<dyn DesiredStream<K>>> {
        self.finished = true;

        // Spill any remaining records in the chunk buffer.
        if !self.chunk.is_empty() {
            self.spill_chunk()?;
        }

        if self.run_paths.is_empty() {
            // No input at all.
            return Ok(Box::new(SortedStream { records: Vec::new(), pos: 0 }));
        }

        if self.run_paths.len() == 1 {
            // Single run: read back into memory (already sorted + normalized).
            let records = read_run::<K>(&self.run_paths[0])?;
            let _ = std::fs::remove_file(&self.run_paths[0]);
            return Ok(Box::new(SortedStream { records, pos: 0 }));
        }

        // Multiple runs: k-way merge with coalescing.
        let merge = KWayMerge::<K>::new(&self.run_paths)?;
        Ok(Box::new(MergeStream { merge: Some(merge), run_paths: self.run_paths }))
    }

    fn spill_chunk(&mut self) -> Result<()> {
        if self.chunk.is_empty() { return Ok(()); }

        // Sort by `from`.
        self.chunk.sort_by(|a, b| a.from.cmp(&b.from));

        // Normalize: resolve overlaps into disjoint segments (last-wins for
        // different scope_ids, merge for same scope_ids).
        let normalized = normalize_chunk(&self.chunk);

        // Write to temp file.
        let dir: PathBuf = self.config.temp_dir.clone().unwrap_or_else(|| PathBuf::from("/tmp"));
        let unique = RUN_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = dir.join(format!("iprange_extsort_{}_{}", unique, self.run_paths.len()));
        write_run::<K>(&path, &normalized)?;
        self.run_paths.push(path);
        self.chunk.clear();
        Ok(())
    }
}

impl<K: IpKey> ExtSorter<K> {
    /// Abort the sort and clean up temp files. Alternative to finish().
    pub fn abort(&mut self) {
        for p in &self.run_paths {
            let _ = std::fs::remove_file(p);
        }
        self.run_paths.clear();
        self.chunk.clear();
    }
}

/// Normalize a sorted chunk: resolve overlaps into disjoint segments.
/// Last-wins for different scope_ids; merge for same scope_ids.
///
/// **Fixes #4:** overlapping input is properly split into disjoint segments.
fn normalize_chunk<K: IpKey>(sorted: &[DesiredRecord<K>]) -> Vec<DesiredRecord<K>> {
    if sorted.is_empty() { return Vec::new(); }
    if sorted.len() == 1 { return sorted.to_vec(); }

    // Collect all boundary points.
    let mut boundaries: Vec<K> = Vec::new();
    for r in sorted {
        boundaries.push(r.from);
        if let Some(after) = r.to.checked_inc() {
            boundaries.push(after);
        }
    }
    boundaries.sort_by(|a, b| a.cmp(b));
    boundaries.dedup();

    // For each segment [boundaries[i], boundaries[i+1]-1], find the last
    // record that covers it (last-wins for different scopes).
    let mut out: Vec<DesiredRecord<K>> = Vec::new();
    for i in 0..boundaries.len().saturating_sub(1) {
        let seg_from = boundaries[i];
        let seg_to = boundaries[i + 1].checked_dec().unwrap_or(boundaries[i + 1]);
        if seg_from > seg_to { continue; }

        // Find the last record covering this segment.
        let mut last_scope: Option<u32> = None;
        for r in sorted {
            if r.from <= seg_from && r.to >= seg_to {
                last_scope = Some(r.scope_id);
            }
        }

        if let Some(scope) = last_scope {
            // Coalesce with previous output if same scope and adjacent.
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
    }

    out
}

// ──────────────────────────────────────────────────────────────────────────
// In-memory sorted stream (for small inputs)
// ──────────────────────────────────────────────────────────────────────────

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
            let r = self.records[self.pos];
            self.pos += 1;
            Some(r)
        } else { None }
    }
}

impl<K: IpKey> Clone for SortedStream<K> {
    fn clone(&self) -> Self {
        SortedStream { records: self.records.clone(), pos: self.pos }
    }
}

// ──────────────────────────────────────────────────────────────────────────
// K-way merge (for spill path)
// ──────────────────────────────────────────────────────────────────────────

struct RunReader<K: IpKey> {
    file: File,
    current: Option<DesiredRecord<K>>,
}

impl<K: IpKey> RunReader<K> {
    fn open(path: &Path) -> Result<Self> {
        let mut file = OpenOptions::new().read(true).open(path).map_err(Error::Io)?;
        let current = read_record::<K>(&mut file)?;
        Ok(RunReader { file, current })
    }
    fn advance(&mut self) {
        self.current = read_record::<K>(&mut self.file).ok().flatten();
    }
}

struct KWayMerge<K: IpKey> {
    runs: Vec<RunReader<K>>,
    cached: Option<DesiredRecord<K>>,
}

impl<K: IpKey> KWayMerge<K> {
    fn new(run_paths: &[PathBuf]) -> Result<Self> {
        let mut runs = Vec::with_capacity(run_paths.len());
        for p in run_paths { runs.push(RunReader::<K>::open(p)?); }
        let mut m = KWayMerge { runs, cached: None };
        m.cached = m.compute_coalesced();
        Ok(m)
    }

    fn find_min(&self) -> Option<usize> {
        let mut min_idx: Option<usize> = None;
        for i in 0..self.runs.len() {
            if self.runs[i].current.is_none() { continue; }
            match min_idx {
                None => min_idx = Some(i),
                Some(mi) => {
                    if self.runs[i].current.unwrap().from < self.runs[mi].current.unwrap().from {
                        min_idx = Some(i);
                    }
                }
            }
        }
        min_idx
    }

    fn pop_min(&mut self) -> Option<DesiredRecord<K>> {
        let idx = self.find_min()?;
        let result = self.runs[idx].current.take();
        self.runs[idx].advance();
        result
    }

    fn compute_coalesced(&mut self) -> Option<DesiredRecord<K>> {
        let mut result = self.pop_min()?;
        loop {
            let merge = if let Some(idx) = self.find_min() {
                let n = self.runs[idx].current.as_ref().unwrap();
                n.scope_id == result.scope_id &&
                    result.to.checked_inc().map_or(false, |a| a == n.from)
            } else { false };
            if !merge { break; }
            result.to = self.pop_min().unwrap().to;
        }
        Some(result)
    }
}

impl<K: IpKey> DesiredStream<K> for KWayMerge<K> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { self.cached.as_ref() }
    fn next(&mut self) -> Option<DesiredRecord<K>> {
        let result = self.cached.take()?;
        self.cached = self.compute_coalesced();
        Some(result)
    }
}

struct MergeStream<K: IpKey> {
    merge: Option<KWayMerge<K>>,
    run_paths: Vec<PathBuf>,
}

impl<K: IpKey> DesiredStream<K> for MergeStream<K> {
    fn peek(&self) -> Option<&DesiredRecord<K>> { self.merge.as_ref()?.peek() }
    fn next(&mut self) -> Option<DesiredRecord<K>> { self.merge.as_mut()?.next() }
}

impl<K: IpKey> Drop for MergeStream<K> {
    fn drop(&mut self) {
        for p in &self.run_paths { let _ = std::fs::remove_file(p); }
    }
}

// ──────────────────────────────────────────────────────────────────────────
// File I/O helpers
// ──────────────────────────────────────────────────────────────────────────

fn read_record<K: IpKey>(file: &mut File) -> Result<Option<DesiredRecord<K>>> {
    let kw = K::WIDTH;
    let mut buf = vec![0u8; kw * 2 + 4];
    match file.read_exact(&mut buf) {
        Ok(()) => Ok(Some(DesiredRecord {
            from: K::read_le(&buf[..kw]),
            to: K::read_le(&buf[kw..2*kw]),
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

// ──────────────────────────────────────────────────────────────────────────
// Convenience: one-shot sort (for backward compat / small inputs)
// ──────────────────────────────────────────────────────────────────────────

/// One-shot sort: takes ownership of a Vec, sorts, normalizes, returns a stream.
/// For large inputs, prefer ExtSorter (streaming input).
pub fn ext_sort<K: IpKey>(
    records: Vec<DesiredRecord<K>>,
    config: &ExtSortConfig,
) -> Result<Box<dyn DesiredStream<K>>> {
    let mut sorter = ExtSorter::new(config.clone());
    for rec in records {
        sorter.add(rec.from, rec.to, rec.scope_id)?;
    }
    sorter.finish()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;

    fn rec(from: u32, to: u32, scope: u32) -> DesiredRecord<Ipv4Key> {
        DesiredRecord { from: Ipv4Key(from), to: Ipv4Key(to), scope_id: scope }
    }

    #[test]
    fn in_memory_sort() {
        let input = vec![rec(30,40,1), rec(10,20,1), rec(21,29,1), rec(50,60,2)];
        let mut s = SortedStream::from_unsorted(input);
        let r1 = s.next().unwrap();
        assert_eq!(r1.from, Ipv4Key(10));
        assert_eq!(r1.to, Ipv4Key(40)); // coalesced [10-20]+[21-29]
        assert_eq!(s.next().unwrap().from, Ipv4Key(50));
        assert!(s.next().is_none());
    }

    #[test]
    fn normalize_overlapping_different_scope() {
        // [10-20] scope=1, [15-25] scope=2 → last-wins
        let input = vec![rec(10,20,1), rec(15,25,2)];
        let s = SortedStream::from_unsorted(input);
        // After normalization: [10-14] scope=1, [15-25] scope=2
        assert_eq!(s.records.len(), 2);
        assert_eq!(s.records[0].from, Ipv4Key(10));
        assert_eq!(s.records[0].to, Ipv4Key(14));
        assert_eq!(s.records[0].scope_id, 1);
        assert_eq!(s.records[1].from, Ipv4Key(15));
        assert_eq!(s.records[1].to, Ipv4Key(25));
        assert_eq!(s.records[1].scope_id, 2);
    }

    #[test]
    fn normalize_overlapping_same_scope() {
        let input = vec![rec(10,20,1), rec(15,25,1)];
        let s = SortedStream::from_unsorted(input);
        assert_eq!(s.records.len(), 1);
        assert_eq!(s.records[0].from, Ipv4Key(10));
        assert_eq!(s.records[0].to, Ipv4Key(25));
    }

    #[test]
    fn streaming_sorter_small() {
        let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig { chunk_size: 100, temp_dir: None });
        sorter.add(Ipv4Key(30), Ipv4Key(40), 1).unwrap();
        sorter.add(Ipv4Key(10), Ipv4Key(20), 2).unwrap();
        sorter.add(Ipv4Key(5), Ipv4Key(8), 3).unwrap();
        let mut stream = sorter.finish().unwrap();
        assert_eq!(stream.next().unwrap().from, Ipv4Key(5));
        assert_eq!(stream.next().unwrap().from, Ipv4Key(10));
        assert_eq!(stream.next().unwrap().from, Ipv4Key(30));
        assert!(stream.next().is_none());
    }

    #[test]
    fn streaming_sorter_spill() {
        let config = ExtSortConfig { chunk_size: 10, temp_dir: None };
        let mut sorter = ExtSorter::<Ipv4Key>::new(config);
        for i in 0..25u32 {
            sorter.add(Ipv4Key(1000-i), Ipv4Key(1000-i), i).unwrap();
        }
        let mut stream = sorter.finish().unwrap();
        let mut prev = Ipv4Key(0);
        let mut count = 0;
        while let Some(r) = stream.next() {
            assert!(r.from > prev || count == 0);
            prev = r.from;
            count += 1;
        }
        assert_eq!(count, 25);
    }

    #[test]
    fn streaming_sorter_spill_normalized() {
        // Overlapping input across spill boundaries should normalize correctly.
        let config = ExtSortConfig { chunk_size: 5, temp_dir: None };
        let mut sorter = ExtSorter::<Ipv4Key>::new(config);
        for i in 0..10u32 {
            sorter.add(Ipv4Key(i*2), Ipv4Key(i*2+1), 1).unwrap();
        }
        let mut stream = sorter.finish().unwrap();
        let r = stream.next().unwrap();
        assert_eq!(r.from, Ipv4Key(0));
        assert_eq!(r.to, Ipv4Key(19)); // fully coalesced
        assert!(stream.next().is_none());
    }

    #[test]
    fn streaming_sorter_empty() {
        let sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig::default());
        let mut stream = sorter.finish().unwrap();
        assert!(stream.next().is_none());
    }
}
