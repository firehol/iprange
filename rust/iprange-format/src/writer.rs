//! Writer: build a byte-identical v3 file from an input set of ranges + values.
//!
//! The writer follows the §9 production order exactly: (1) sort by `start`,
//! (2) coalesce adjacent same-**content** neighbours, (3) assign `value_id`s by the
//! §10 sweep — then computes `entry_count`/`unique_ip_count`, lays the sections out
//! at `align_up` offsets (zero inter-section padding), hashes each section, and
//! backpatches the header. The build is in-memory (the spec permits buffering the
//! header until all sections are complete instead of streaming + seek-back).
//!
//! Requires the `alloc` feature.

extern crate alloc;
use alloc::collections::BTreeMap;
use alloc::vec::Vec;

use sha2::{Digest, Sha256};

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::spec::{self, SectionKind};
use crate::wire::{DirEntry, Header, IndexSubHeader, Record};

/// An opaque per-range value. The format treats `bytes` as caller-supplied content
/// (§0/§10); only `type_id == 1` (membership set) carries a structural rule the
/// writer enforces. `None` means the range is present with no value (the sentinel).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Value {
    /// Value type (registry §10). `0` is invalid.
    pub type_id: u32,
    /// Opaque value bytes (e.g. for `type_id == 1`, ascending LE `u32` feed-ids).
    pub bytes: Vec<u8>,
}

impl Value {
    /// Validate this value's structural rules (§10). `type_id == 0` is invalid; for
    /// `type_id == 1` the bytes must be a non-empty, `% 4 == 0`, strictly-ascending
    /// list of LE `u32`s. Unknown `type_id`s are accepted verbatim (bounds only).
    fn validate(&self) -> Result<()> {
        if self.type_id == 0 {
            return Err(Error::InvalidInput("value type_id 0 is reserved/invalid"));
        }
        if self.type_id == 1 {
            if self.bytes.is_empty() || self.bytes.len() % 4 != 0 {
                return Err(Error::InvalidInput(
                    "type_id 1 membership set must be a non-empty multiple of 4 bytes",
                ));
            }
            let mut prev: Option<u32> = None;
            for chunk in self.bytes.chunks_exact(4) {
                let id = u32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
                if let Some(p) = prev {
                    if id <= p {
                        return Err(Error::InvalidInput(
                            "type_id 1 feed-ids must be strictly ascending",
                        ));
                    }
                }
                prev = Some(id);
            }
        }
        Ok(())
    }
}

/// The six feed-meta fields (§7), in order. Strings are `String`, so UTF-8 validity
/// is guaranteed by construction; the writer emits them verbatim (no normalization).
#[derive(Clone, Debug, Default)]
pub struct FeedMeta {
    /// Human-readable feed name.
    pub name: alloc::string::String,
    /// Category token.
    pub category: alloc::string::String,
    /// Maintainer identity.
    pub maintainer: alloc::string::String,
    /// Maintainer URL.
    pub maintainer_url: alloc::string::String,
    /// Original threat-intel source URL.
    pub source_url: alloc::string::String,
    /// Short license token (SPDX preferred).
    pub license: alloc::string::String,
}

impl FeedMeta {
    fn encode(&self) -> Vec<u8> {
        let fields = [
            &self.name,
            &self.category,
            &self.maintainer,
            &self.maintainer_url,
            &self.source_url,
            &self.license,
        ];
        let mut out = Vec::new();
        out.extend_from_slice(&spec::FEED_META_FIELD_COUNT.to_le_bytes());
        for f in fields {
            let b = f.as_bytes();
            out.extend_from_slice(&(b.len() as u32).to_le_bytes());
            out.extend_from_slice(b);
        }
        out
    }
}

/// Builds a v3 file for key width `K`.
#[derive(Debug)]
pub struct Writer<K: IpKey> {
    feed_meta: FeedMeta,
    license_flags: u32,
    generation_unixtime: u64,
    ranges: Vec<(K, K, Option<Value>)>,
}

impl<K: IpKey> Writer<K> {
    /// Start a new writer with the given identity metadata.
    pub fn new(feed_meta: FeedMeta, license_flags: u32, generation_unixtime: u64) -> Self {
        Writer {
            feed_meta,
            license_flags,
            generation_unixtime,
            ranges: Vec::new(),
        }
    }

    /// Add an inclusive range `[start, end]` with an optional value. `start <= end`
    /// is required; overlap with other ranges is rejected at [`build`](Self::build).
    pub fn add_range(&mut self, start: K, end: K, value: Option<Value>) -> Result<()> {
        if start > end {
            return Err(Error::InvalidInput("range start > end"));
        }
        if let Some(v) = &value {
            v.validate()?;
        }
        self.ranges.push((start, end, value));
        Ok(())
    }

    /// Produce the complete v3 file bytes, or an error if the input is not encodable.
    pub fn build(mut self) -> Result<Vec<u8>> {
        // license_flags MUST NOT set reserved bits (§7) — fail at write time rather
        // than emit a file the reader rejects. (Feed-meta is UTF-8 by construction:
        // the fields are `String`.)
        if self.license_flags & !spec::LICENSE_FLAG_DONT_REDISTRIBUTE != 0 {
            return Err(Error::InvalidInput("license_flags sets reserved bits"));
        }

        // (1) sort by start.
        self.ranges.sort_by(|a, b| a.0.cmp(&b.0));

        // Validate disjointness (a.end < b.start) and coalesce same-content neighbours
        // in a single forward pass (§9).
        let mut coalesced: Vec<(K, K, Option<Value>)> = Vec::with_capacity(self.ranges.len());
        for (start, end, value) in self.ranges.into_iter() {
            if let Some((_, last_end, last_val)) = coalesced.last_mut() {
                if start <= *last_end {
                    return Err(Error::InvalidInput("overlapping input ranges (not disjoint)"));
                }
                // contiguous + same content => extend the current run.
                if *last_val == value && last_end.checked_inc() == Some(start) {
                    *last_end = end;
                    continue;
                }
            }
            coalesced.push((start, end, value));
        }

        let entry_count = coalesced.len() as u64;

        // (3) assign value_ids by sweeping the coalesced records; sentinels skipped.
        let mut dedup: BTreeMap<(u32, Vec<u8>), u32> = BTreeMap::new();
        let mut values_order: Vec<&Value> = Vec::new();
        let mut records: Vec<Record<K>> = Vec::with_capacity(coalesced.len());
        let mut unique: u128 = 0;
        for (start, end, value) in coalesced.iter() {
            // unique-IP count (checked 128-bit; None = full IPv6 space, unrepresentable).
            let size = K::range_size(*start, *end)
                .ok_or(Error::InvalidInput("range covers the entire IPv6 space"))?;
            unique = unique
                .checked_add(size)
                .ok_or(Error::Overflow("unique_ip_count sum exceeds 2^128-1"))?;

            let value_id = match value {
                None => spec::VALUE_ID_NONE,
                Some(v) => {
                    let key = (v.type_id, v.bytes.clone());
                    if let Some(&id) = dedup.get(&key) {
                        id
                    } else {
                        let id = u32::try_from(values_order.len())
                            .map_err(|_| Error::InvalidInput("more than 2^32-1 distinct values"))?;
                        if id == spec::VALUE_ID_NONE {
                            return Err(Error::InvalidInput("more than 2^32-1 distinct values"));
                        }
                        dedup.insert(key, id);
                        values_order.push(v);
                        id
                    }
                }
            };
            records.push(Record {
                start: *start,
                end: *end,
                value_id,
            });
        }

        // Encode sections.
        let feed_meta_bytes = self.feed_meta.encode();
        let index_bytes = encode_index::<K>(&records);
        let values_bytes = if values_order.is_empty() {
            None
        } else {
            Some(encode_values(&values_order))
        };

        // Lay out sections in canonical order at align_up offsets; build directory.
        let mut sections: Vec<(SectionKind, Vec<u8>)> = Vec::with_capacity(4);
        sections.push((SectionKind::FeedMeta, feed_meta_bytes));
        sections.push((SectionKind::Index, index_bytes));
        if let Some(v) = values_bytes {
            sections.push((SectionKind::Values, v));
        }
        sections.push((SectionKind::Signature, Vec::new())); // empty, last

        let directory_count = sections.len() as u32;
        let header_size = u64::from(spec::HEADER_SIZE);
        let dir_bytes_len = u64::from(directory_count)
            .checked_mul(spec::DIR_ENTRY_SIZE as u64)
            .ok_or(Error::Overflow("directory size"))?;
        let mut cursor = header_size
            .checked_add(dir_bytes_len)
            .ok_or(Error::Overflow("directory end"))?;

        // First pass: assign each section its offset; collect dir entries.
        let mut entries: Vec<DirEntry> = Vec::with_capacity(sections.len());
        let mut placed: Vec<(u64, &[u8])> = Vec::with_capacity(sections.len());
        for (kind, bytes) in &sections {
            let align = kind.align();
            let offset = spec::align_up(cursor, align).ok_or(Error::Overflow("section offset"))?;
            let length = bytes.len() as u64;
            let mut hasher = Sha256::new();
            hasher.update(bytes);
            let hash: [u8; 32] = hasher.finalize().into();
            entries.push(DirEntry {
                kind: kind.id(),
                flags: kind.flags(),
                offset,
                length,
                align,
                hash,
            });
            placed.push((offset, bytes));
            cursor = offset.checked_add(length).ok_or(Error::Overflow("section end"))?;
        }
        let file_size = cursor;

        // unique-IP count split.
        let unique_lo = unique as u64;
        let unique_hi = (unique >> 64) as u64;

        let header = Header {
            version_minor: spec::VERSION_MINOR,
            header_size: spec::HEADER_SIZE,
            flags: K::VERSION.flag_bit(),
            file_size,
            directory_offset: header_size,
            directory_count,
            license_flags: self.license_flags,
            entry_count,
            generation_unixtime: self.generation_unixtime,
            unique_ip_count_lo: unique_lo,
            unique_ip_count_hi: unique_hi,
        };

        // Assemble: header || directory || (padding + section)*  — zero padding fills
        // each align_up gap; file ends exactly at the last section.
        let mut out = Vec::with_capacity(file_size as usize);
        out.extend_from_slice(&header.encode());
        for e in &entries {
            out.extend_from_slice(&e.encode());
        }
        for (offset, bytes) in &placed {
            debug_assert!(*offset >= out.len() as u64);
            out.resize(*offset as usize, 0u8); // inter-section padding (zeros)
            out.extend_from_slice(bytes);
        }
        debug_assert_eq!(out.len() as u64, file_size);
        Ok(out)
    }
}

fn encode_index<K: IpKey>(records: &[Record<K>]) -> Vec<u8> {
    let sub = IndexSubHeader {
        record_size: K::RECORD_SIZE as u32,
        key_width: K::WIDTH as u32,
        record_count: records.len() as u64,
    };
    let mut out = Vec::with_capacity(spec::INDEX_SUBHEADER_SIZE + records.len() * K::RECORD_SIZE);
    out.extend_from_slice(&sub.encode());
    let mut rec = [0u8; 40]; // max record size
    for r in records {
        let buf = &mut rec[..K::RECORD_SIZE];
        buf.iter_mut().for_each(|b| *b = 0); // clear (zeroes the v6 pad)
        r.encode_into(buf);
        out.extend_from_slice(buf);
    }
    out
}

fn encode_values(values: &[&Value]) -> Vec<u8> {
    let mut out = Vec::new();
    out.extend_from_slice(&(values.len() as u32).to_le_bytes());
    for v in values {
        out.extend_from_slice(&v.type_id.to_le_bytes());
        out.extend_from_slice(&(v.bytes.len() as u32).to_le_bytes());
        out.extend_from_slice(&v.bytes);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::spec::IpVersion;
    use crate::wire::Header;

    fn meta() -> FeedMeta {
        FeedMeta {
            name: "test".into(),
            category: "attacks".into(),
            ..Default::default()
        }
    }

    #[test]
    fn build_empty_v4_file_structure() {
        let w = Writer::<Ipv4Key>::new(meta(), 0, 1_700_000_000);
        let bytes = w.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.ip_version(), IpVersion::V4);
        assert_eq!(h.entry_count, 0);
        assert_eq!(h.unique_ip_count_lo, 0);
        assert_eq!(h.unique_ip_count_hi, 0);
        assert_eq!(h.directory_offset, 72);
        assert_eq!(h.directory_count, 3); // feed-meta, index, signature (no values)
        assert_eq!(h.file_size, bytes.len() as u64);
        // index align 16 => offset is 16-aligned.
    }

    #[test]
    fn coalesce_adjacent_same_value() {
        let mut w = Writer::<Ipv4Key>::new(meta(), 0, 0);
        w.add_range(Ipv4Key(10), Ipv4Key(20), None).unwrap();
        w.add_range(Ipv4Key(21), Ipv4Key(30), None).unwrap(); // contiguous, same (None)
        let bytes = w.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.entry_count, 1, "two contiguous same-value ranges coalesce to 1");
        assert_eq!(h.unique_ip_count_lo, 21); // 10..=30
    }

    #[test]
    fn no_coalesce_different_value() {
        let mut w = Writer::<Ipv4Key>::new(meta(), 0, 0);
        let v = |b: u32| Some(Value { type_id: 2, bytes: b.to_le_bytes().to_vec() });
        w.add_range(Ipv4Key(10), Ipv4Key(20), v(1)).unwrap();
        w.add_range(Ipv4Key(21), Ipv4Key(30), v(2)).unwrap(); // contiguous, different value
        let bytes = w.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.entry_count, 2);
        assert_eq!(h.directory_count, 4); // values section present
    }

    #[test]
    fn reject_overlapping_input() {
        let mut w = Writer::<Ipv4Key>::new(meta(), 0, 0);
        w.add_range(Ipv4Key(10), Ipv4Key(20), None).unwrap();
        w.add_range(Ipv4Key(15), Ipv4Key(25), None).unwrap();
        assert!(matches!(w.build(), Err(Error::InvalidInput(_))));
    }

    #[test]
    fn value_dedup_assigns_same_id() {
        let mut w = Writer::<Ipv4Key>::new(meta(), 0, 0);
        let v = Some(Value { type_id: 2, bytes: vec![1, 2, 3, 4] });
        w.add_range(Ipv4Key(10), Ipv4Key(20), v.clone()).unwrap();
        w.add_range(Ipv4Key(100), Ipv4Key(200), v).unwrap(); // same content, not contiguous
        let bytes = w.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.entry_count, 2);
        assert_eq!(h.directory_count, 4);
        // both records reference value_id 0 (one interned value) — verified via reader later.
    }

    #[test]
    fn reject_full_ipv6_space() {
        let mut w = Writer::<Ipv6Key>::new(meta(), 0, 0);
        w.add_range(Ipv6Key::MIN, Ipv6Key::MAX, None).unwrap();
        assert!(matches!(w.build(), Err(Error::InvalidInput(_))));
    }

    #[test]
    fn deterministic_same_input_same_bytes() {
        let mk = || {
            let mut w = Writer::<Ipv4Key>::new(meta(), 0, 1234);
            w.add_range(Ipv4Key(100), Ipv4Key(200), Some(Value { type_id: 2, bytes: vec![9] }))
                .unwrap();
            w.add_range(Ipv4Key(10), Ipv4Key(20), None).unwrap(); // added out of order
            w.build().unwrap()
        };
        assert_eq!(mk(), mk(), "same logical input -> byte-identical output");
    }
}
