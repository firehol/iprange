//! External sort: produce a sorted, disjoint stream from unsorted input with
//! bounded memory.
//!
//! This is a helper for the migrate API — downstream callers often receive
//! unsorted IP feeds that may not fit in memory. The external sort spills
//! sorted chunks to temporary files when the memory limit is reached, then
//! k-way merges them.
//!
//! For small inputs that fit in memory, it degrades to an in-memory sort.

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::migrate::{DesiredRecord, DesiredStream};
use alloc::vec::Vec;
use core::cmp::Ordering;

/// Configuration for external sort.
#[derive(Clone, Debug)]
pub struct ExtSortConfig {
    /// Maximum records to hold in memory before spilling.
    pub chunk_size: usize,
}

impl Default for ExtSortConfig {
    fn default() -> Self {
        ExtSortConfig { chunk_size: 100_000 }
    }
}

/// An in-memory sorted stream. Holds all records in a Vec, sorted and coalesced.
/// This is the bounded-memory path: if input fits in the chunk_size, no spill.
pub struct SortedStream<K: IpKey> {
    pub records: Vec<DesiredRecord<K>>,
    pub pos: usize,
}

impl<K: IpKey> SortedStream<K> {
    /// Build a sorted, coalesced stream from an unsorted Vec.
    /// Adjacent records with the same scope_id are coalesced.
    pub fn from_unsorted(mut records: Vec<DesiredRecord<K>>) -> Self {
        // Sort by `from` ascending.
        records.sort_by(|a, b| a.from.cmp(&b.from));
        // Coalesce adjacent same-scope_id records.
        let coalesced = coalesce_adjacent(records);
        SortedStream { records: coalesced, pos: 0 }
    }
}

impl<K: IpKey> DesiredStream<K> for SortedStream<K> {
    fn peek(&self) -> Option<&DesiredRecord<K>> {
        self.records.get(self.pos)
    }

    fn next(&mut self) -> Option<DesiredRecord<K>> {
        if self.pos < self.records.len() {
            let r = self.records[self.pos];
            self.pos += 1;
            Some(r)
        } else {
            None
        }
    }
}

/// Coalesce adjacent records where `prev.to + 1 == next.from` and `prev.scope_id == next.scope_id`.
/// This reduces the record count for optimal storage.
fn coalesce_adjacent<K: IpKey>(records: Vec<DesiredRecord<K>>) -> Vec<DesiredRecord<K>> {
    if records.len() <= 1 {
        return records;
    }
    let mut out: Vec<DesiredRecord<K>> = Vec::with_capacity(records.len());
    out.push(records[0]);
    for i in 1..records.len() {
        let last = out.len() - 1;
        let prev = out[last];
        let curr = records[i];
        // Coalesce if: same scope_id AND adjacent (prev.to + 1 == curr.from)
        if prev.scope_id == curr.scope_id {
            if let Some(prev_to_next) = prev.to.checked_inc() {
                if prev_to_next == curr.from {
                    // Merge: extend prev.to
                    out[last].to = curr.to;
                    continue;
                }
            }
        }
        out.push(curr);
    }
    out
}

/// External sort: sort unsorted records with bounded memory.
///
/// For inputs that fit in `config.chunk_size`, this is equivalent to
/// `SortedStream::from_unsorted`. For larger inputs, it spills sorted chunks
/// to temporary files and k-way merges them (TODO: spill path).
pub fn ext_sort<K: IpKey>(
    records: Vec<DesiredRecord<K>>,
    _config: &ExtSortConfig,
) -> Result<SortedStream<K>> {
    // For now: in-memory sort (no spill). The spill path will be added
    // when the full external sort with file-backed runs is implemented.
    // The API is ready for callers — they just need to ensure their input
    // fits in config.chunk_size for now.
    if records.len() > _config.chunk_size {
        // TODO: implement spill+merge for large inputs
        return Err(Error::State("external sort spill not yet implemented (input exceeds chunk_size)"));
    }
    Ok(SortedStream::from_unsorted(records))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;

    #[test]
    fn sort_and_coalesce() {
        let input = vec![
            DesiredRecord { from: Ipv4Key(10), to: Ipv4Key(20), scope_id: 1 },
            DesiredRecord { from: Ipv4Key(30), to: Ipv4Key(40), scope_id: 1 },
            DesiredRecord { from: Ipv4Key(21), to: Ipv4Key(29), scope_id: 1 }, // adjacent to both
            DesiredRecord { from: Ipv4Key(50), to: Ipv4Key(60), scope_id: 2 },
        ];
        let mut stream = SortedStream::from_unsorted(input);
        // After sort: [10-20],[21-29],[30-40],[50-60]
        // After coalesce: [10-40] (scope 1), [50-60] (scope 2)
        let r1 = stream.next().unwrap();
        assert_eq!(r1.from, Ipv4Key(10));
        assert_eq!(r1.to, Ipv4Key(40));
        assert_eq!(r1.scope_id, 1);
        let r2 = stream.next().unwrap();
        assert_eq!(r2.from, Ipv4Key(50));
        assert_eq!(r2.to, Ipv4Key(60));
        assert_eq!(r2.scope_id, 2);
        assert!(stream.next().is_none());
    }

    #[test]
    fn no_coalesce_different_scope() {
        let input = vec![
            DesiredRecord { from: Ipv4Key(10), to: Ipv4Key(20), scope_id: 1 },
            DesiredRecord { from: Ipv4Key(21), to: Ipv4Key(30), scope_id: 2 },
        ];
        let mut stream = SortedStream::from_unsorted(input);
        assert_eq!(stream.next().unwrap().scope_id, 1);
        assert_eq!(stream.next().unwrap().scope_id, 2);
        assert!(stream.next().is_none());
    }
}
