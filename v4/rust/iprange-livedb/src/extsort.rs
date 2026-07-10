//! External sort: produce a sorted, disjoint stream from unsorted input with
//! bounded memory.
//!
//! For inputs that fit in chunk_size, sorts in memory. For larger inputs,
//! spills sorted chunks (runs) to temporary files, then k-way merges them.

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
    pub chunk_size: usize,
    pub temp_dir: Option<PathBuf>,
}

impl Default for ExtSortConfig {
    fn default() -> Self {
        ExtSortConfig { chunk_size: 100_000, temp_dir: None }
    }
}

// --- in-memory sorted stream ---

pub struct SortedStream<K: IpKey> {
    pub records: Vec<DesiredRecord<K>>,
    pub pos: usize,
}

impl<K: IpKey> SortedStream<K> {
    pub fn from_unsorted(mut records: Vec<DesiredRecord<K>>) -> Self {
        records.sort_by(|a, b| a.from.cmp(&b.from));
        SortedStream { records: coalesce_adjacent(records), pos: 0 }
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

// --- file-backed run reader ---

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

// --- k-way merge ---

struct KWayMerge<K: IpKey> {
    runs: Vec<RunReader<K>>,
    cached: Option<DesiredRecord<K>>, // coalesced peek result
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
    fn peek(&self) -> Option<&DesiredRecord<K>> {
        self.cached.as_ref()
    }
    fn next(&mut self) -> Option<DesiredRecord<K>> {
        let result = self.cached.take()?;
        self.cached = self.compute_coalesced();
        Some(result)
    }
}

// --- file I/O helpers ---

fn read_record<K: IpKey>(file: &mut File) -> Result<Option<DesiredRecord<K>>> {
    let kw = K::WIDTH;
    let mut buf = vec![0u8; kw * 2 + 4];
    match file.read_exact(&mut buf) {
        Ok(()) => {
            Ok(Some(DesiredRecord {
                from: K::read_le(&buf[..kw]),
                to: K::read_le(&buf[kw..2*kw]),
                scope_id: u32::from_le_bytes([buf[2*kw], buf[2*kw+1], buf[2*kw+2], buf[2*kw+3]]),
            }))
        }
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

fn spill_run<K: IpKey>(records: Vec<DesiredRecord<K>>, dir: &Path, idx: usize) -> Result<PathBuf> {
    let mut sorted = records;
    sorted.sort_by(|a, b| a.from.cmp(&b.from));
    let coalesced = coalesce_adjacent(sorted);
    let unique = RUN_COUNTER.fetch_add(1, Ordering::SeqCst);
    let path = dir.join(format!("iprange_extsort_{}_{}", unique, idx));
    let mut file = OpenOptions::new().write(true).create(true).truncate(true)
        .open(&path).map_err(Error::Io)?;
    for rec in &coalesced { write_record::<K>(&mut file, rec)?; }
    file.flush().map_err(Error::Io)?;
    Ok(path)
}

fn coalesce_adjacent<K: IpKey>(records: Vec<DesiredRecord<K>>) -> Vec<DesiredRecord<K>> {
    if records.len() <= 1 { return records; }
    let mut out: Vec<DesiredRecord<K>> = Vec::with_capacity(records.len());
    out.push(records[0]);
    for curr in records.into_iter().skip(1) {
        let last = out.len() - 1;
        if out[last].scope_id == curr.scope_id {
            if let Some(a) = out[last].to.checked_inc() {
                if a == curr.from {
                    out[last].to = curr.to;
                    continue;
                }
            }
        }
        out.push(curr);
    }
    out
}

// --- entry point ---

pub fn ext_sort<K: IpKey>(
    records: Vec<DesiredRecord<K>>,
    config: &ExtSortConfig,
) -> Result<Box<dyn DesiredStream<K>>> {
    if records.len() <= config.chunk_size {
        return Ok(Box::new(SortedStream::from_unsorted(records)));
    }

    // Spill path.
    let dir: PathBuf = config.temp_dir.clone().unwrap_or_else(|| PathBuf::from("/tmp"));
    let mut run_paths = Vec::new();
    let mut chunk = Vec::with_capacity(config.chunk_size);
    let mut run_idx = 0;

    for rec in records {
        chunk.push(rec);
        if chunk.len() >= config.chunk_size {
            let path = spill_run::<K>(core::mem::take(&mut chunk), &dir, run_idx)?;
            run_paths.push(path);
            run_idx += 1;
        }
    }
    if !chunk.is_empty() {
        run_paths.push(spill_run::<K>(chunk, &dir, run_idx)?);
    }

    if run_paths.len() == 1 {
        let mut file = OpenOptions::new().read(true).open(&run_paths[0]).map_err(Error::Io)?;
        let mut recs = Vec::new();
        while let Some(r) = read_record::<K>(&mut file)? { recs.push(r); }
        let _ = std::fs::remove_file(&run_paths[0]);
        return Ok(Box::new(SortedStream { records: recs, pos: 0 }));
    }

    let merge = KWayMerge::<K>::new(&run_paths)?;
    Ok(Box::new(MergeStream { merge: Some(merge), run_paths }))
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
        assert_eq!(r1.to, Ipv4Key(40));
        assert_eq!(s.next().unwrap().from, Ipv4Key(50));
        assert!(s.next().is_none());
    }

    #[test]
    fn spill_sort() {
        let config = ExtSortConfig { chunk_size: 10, temp_dir: None };
        let mut input: Vec<DesiredRecord<Ipv4Key>> = Vec::new();
        for i in 0..25u32 { input.push(rec(1000-i, 1000-i, i)); }
        let mut s = ext_sort(input, &config).unwrap();
        let mut prev = Ipv4Key(0);
        let mut count = 0;
        while let Some(r) = s.next() {
            assert!(r.from > prev || count == 0);
            prev = r.from;
            count += 1;
        }
        assert_eq!(count, 25);
    }

    #[test]
    fn spill_coalesce() {
        let config = ExtSortConfig { chunk_size: 5, temp_dir: None };
        let mut input: Vec<DesiredRecord<Ipv4Key>> = Vec::new();
        for i in 0..10u32 { input.push(rec(i*2, i*2+1, 1)); }
        let mut s = ext_sort(input, &config).unwrap();
        let r = s.next().unwrap();
        assert_eq!(r.from, Ipv4Key(0));
        assert_eq!(r.to, Ipv4Key(19));
        assert!(s.next().is_none());
    }
}
