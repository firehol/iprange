//! Reader: validate a v3 file and look up keys.
//!
//! [`Reader`] borrows `&[u8]` (the file bytes, or an mmap'd region) and runs the
//! §15 validation pipeline before exposing anything. Lookups are a zero-allocation
//! numeric binary search directly over the index bytes — the mmap read-only hot path.
//! [`Reader::open_metadata_only`] does only steps 1–7 (header + directory + feed-meta)
//! for the cheap metadata path. With the `mmap` feature, [`mmap::MmapFile`] maps a
//! path (with the §15 open/`fstat`/probe safety) and yields the bytes to [`Reader`].

use sha2::{Digest, Sha256};

use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::spec::{self, IpVersion, SectionKind};
use crate::wire::{u32_le, DirEntry, Header, IndexSubHeader};

/// Located byte ranges of the sections that matter for reads.
#[derive(Clone, Copy, Debug, Default)]
struct Sections {
    feed_meta: Option<(u64, u64)>,
    index: Option<(u64, u64)>,
    values: Option<(u64, u64)>,
    signature: Option<(u64, u64)>,
}

/// A validated, read-only view over a v3 file's bytes.
#[derive(Debug)]
pub struct Reader<'a> {
    bytes: &'a [u8],
    header: Header,
    ip_version: IpVersion,
    feed_meta: (usize, usize), // (offset, len)
    index_records: (usize, usize),
    record_count: u64,
    values: Option<(usize, usize)>,
    values_count: u32,
}

/// Result of a lookup: the IP is present, and its associated value (if any).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Hit {
    /// The matched record's `value_id` (`0xFFFF_FFFF` = present, no value).
    pub value_id: u32,
}

/// A borrowed value-table entry.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ValueRef<'a> {
    /// Value type id (§10).
    pub type_id: u32,
    /// Opaque value bytes.
    pub bytes: &'a [u8],
}

/// The six feed-meta fields, validated as UTF-8 (§7 reader MUST reject invalid).
#[derive(Clone, Copy, Debug)]
pub struct FeedMetaView<'a> {
    /// Human-readable feed name.
    pub name: &'a str,
    /// Category token.
    pub category: &'a str,
    /// Maintainer identity.
    pub maintainer: &'a str,
    /// Maintainer URL.
    pub maintainer_url: &'a str,
    /// Original threat-intel source URL.
    pub source_url: &'a str,
    /// Short license token.
    pub license: &'a str,
}

impl<'a> Reader<'a> {
    /// Open and **fully validate** an untrusted file (§15 steps 1–13): structure,
    /// the per-record safety walk, and section-hash verification. On success, lookups
    /// are safe. On any violation, returns a typed [`Error`] and exposes nothing.
    pub fn open(bytes: &'a [u8]) -> Result<Reader<'a>> {
        let r = Self::parse_structure(bytes)?;
        r.walk_records()?; // step 11 (safety walk)
        r.verify_hashes()?; // step 12 (integrity; SHOULD-at-load, we always do it here)
        Ok(r)
    }

    /// Open for **metadata only** (§16): header + directory + feed-meta (steps 1–7).
    /// Does not walk the index or verify record-level invariants/hashes, so lookups
    /// MUST NOT be performed on the result.
    pub fn open_metadata_only(bytes: &'a [u8]) -> Result<Reader<'a>> {
        Self::parse_structure(bytes)
    }

    /// The parsed header.
    pub fn header(&self) -> &Header {
        &self.header
    }

    /// The file's IP family.
    pub fn ip_version(&self) -> IpVersion {
        self.ip_version
    }

    /// The number of index records.
    pub fn record_count(&self) -> u64 {
        self.record_count
    }

    /// The feed-meta fields, validated as UTF-8.
    pub fn feed_meta(&self) -> Result<FeedMetaView<'a>> {
        let (off, len) = self.feed_meta;
        let b = &self.bytes[off..off + len];
        // The section must hold at least the u32 count — guard before reading it, so a
        // crafted short feed-meta section is rejected, not an out-of-bounds panic.
        if b.len() < 4 {
            return Err(Error::Structural("feed-meta section shorter than its count field"));
        }
        let count = u32_le(b, 0);
        if count < spec::FEED_META_FIELD_COUNT {
            return Err(Error::Structural("feed-meta field_count < 6"));
        }
        if self.header.version_minor == 0 && count != spec::FEED_META_FIELD_COUNT {
            return Err(Error::Structural("feed-meta field_count != 6 for v3.0"));
        }
        // Read all `count` declared fields, keeping the 6 this version knows and
        // skipping any a future minor version added (additive forward-compat, §7).
        let mut pos = 4usize;
        let mut fields: [&str; 6] = [""; 6];
        for i in 0..count {
            if pos + 4 > len {
                return Err(Error::Structural("feed-meta field length runs past section"));
            }
            let flen = u32_le(b, pos) as usize;
            pos += 4;
            if pos + flen > len {
                return Err(Error::Structural("feed-meta field bytes run past section"));
            }
            if (i as usize) < 6 {
                // only the known fields are exposed (and UTF-8-validated); extra
                // future fields are skipped without interpreting their bytes.
                fields[i as usize] = core::str::from_utf8(&b[pos..pos + flen])
                    .map_err(|_| Error::Invariant("feed-meta field is not valid UTF-8"))?;
            }
            pos += flen;
        }
        // exact-length: no trailing garbage after the declared fields (like
        // index/values). This still rejects junk while allowing future extra fields.
        if pos != len {
            return Err(Error::Structural("feed-meta section length not exact"));
        }
        Ok(FeedMetaView {
            name: fields[0],
            category: fields[1],
            maintainer: fields[2],
            maintainer_url: fields[3],
            source_url: fields[4],
            license: fields[5],
        })
    }

    /// Look up an IPv4 address; `Err` if the file is not IPv4.
    pub fn lookup_v4(&self, key: Ipv4Key) -> Result<Option<Hit>> {
        if self.ip_version != IpVersion::V4 {
            return Err(Error::Structural("lookup_v4 on a non-IPv4 file"));
        }
        Ok(self.search::<Ipv4Key>(key))
    }

    /// Look up an IPv6 address; `Err` if the file is not IPv6.
    pub fn lookup_v6(&self, key: Ipv6Key) -> Result<Option<Hit>> {
        if self.ip_version != IpVersion::V6 {
            return Err(Error::Structural("lookup_v6 on a non-IPv6 file"));
        }
        Ok(self.search::<Ipv6Key>(key))
    }

    /// Resolve a `value_id` to its value-table entry (`None` for the sentinel or an
    /// out-of-range id — though a validated file never has an out-of-range id).
    ///
    /// Entries are variable-length, so this walks them sequentially to the requested
    /// positional index. A consumer doing many lookups can build its own side index.
    pub fn value(&self, value_id: u32) -> Option<ValueRef<'a>> {
        if value_id == spec::VALUE_ID_NONE || value_id >= self.values_count {
            return None;
        }
        let (off, len) = self.values?;
        let b = &self.bytes[off..off + len];
        let mut pos = 4usize; // skip the u32 count
        for idx in 0..self.values_count {
            let type_id = u32_le(b, pos);
            let blen = u32_le(b, pos + 4) as usize;
            let start = pos + 8;
            if idx == value_id {
                return Some(ValueRef {
                    type_id,
                    bytes: &b[start..start + blen],
                });
            }
            pos = start + blen;
        }
        None
    }

    // ---- internal ----

    fn parse_structure(bytes: &'a [u8]) -> Result<Reader<'a>> {
        let real_size = bytes.len() as u64;
        // step 1
        if real_size < u64::from(spec::HEADER_SIZE) {
            return Err(Error::FileTooShort {
                need: u64::from(spec::HEADER_SIZE),
                have: real_size,
            });
        }
        // step 2
        let header = Header::decode(bytes)?;
        if real_size < u64::from(header.header_size) {
            return Err(Error::FileTooShort {
                need: u64::from(header.header_size),
                have: real_size,
            });
        }
        // step 3
        if header.file_size != real_size {
            return Err(Error::FileSizeMismatch {
                header: header.file_size,
                real: real_size,
            });
        }
        let ip_version = header.ip_version();
        if ip_version == IpVersion::V4
            && (header.unique_ip_count_hi != 0 || header.unique_ip_count_lo > (1u64 << 32))
        {
            return Err(Error::Structural("IPv4 unique_ip_count out of range"));
        }
        // step 4
        if header.directory_offset != u64::from(header.header_size) {
            return Err(Error::Structural("directory_offset != header_size"));
        }
        let dir_count = u64::from(header.directory_count);
        if dir_count < 3 {
            return Err(Error::Structural("directory_count < 3"));
        }
        let dir_bytes = dir_count
            .checked_mul(spec::DIR_ENTRY_SIZE as u64)
            .ok_or(Error::Overflow("directory size"))?;
        let dir_end = header
            .directory_offset
            .checked_add(dir_bytes)
            .ok_or(Error::Overflow("directory end"))?;
        if dir_end > real_size {
            return Err(Error::Structural("directory extends past file"));
        }

        // steps 5–7: walk directory entries in order.
        let mut sections = Sections::default();
        let mut prev_end = dir_end;
        let mut prev_rank: u64 = 0;
        for i in 0..dir_count {
            let at = (header.directory_offset + i * spec::DIR_ENTRY_SIZE as u64) as usize;
            let e = DirEntry::decode(&bytes[at..])?;
            if e.kind == 0 {
                return Err(Error::Structural("directory entry kind 0"));
            }
            // align: valid set; canonical for a known kind; offset multiple.
            if !spec::is_valid_align(e.align) {
                return Err(Error::Structural("align not in the valid set"));
            }
            if let Some(known) = SectionKind::from_id(e.kind) {
                if e.align != known.align() {
                    return Err(Error::Structural("align != canonical value for known kind"));
                }
                if e.flags != known.flags() {
                    return Err(Error::Structural("flags != canonical value for known kind"));
                }
            } else if e.flags & spec::DIR_FLAG_MUST_UNDERSTAND != 0 {
                // unknown/reserved kind with must_understand=1 → reject (§6).
                return Err(Error::Structural("unknown must_understand=1 section"));
            }
            // expected offset = align_up(prev_end, align): pins padding + non-overlap.
            let expected = spec::align_up(prev_end, e.align).ok_or(Error::Overflow("offset"))?;
            if e.offset != expected {
                return Err(Error::Structural("section offset != align_up(prev_end, align)"));
            }
            // inter-region padding (prev_end..offset) MUST be zero.
            if bytes[prev_end as usize..e.offset as usize].iter().any(|&x| x != 0) {
                return Err(Error::NonZeroReserved("inter-region padding"));
            }
            let end = e.offset.checked_add(e.length).ok_or(Error::Overflow("section end"))?;
            if end > real_size {
                return Err(Error::Structural("section extends past file"));
            }
            // canonical band order (non-decreasing rank; signature(5) ranks last).
            let rank = canon_rank(e.kind);
            if rank < prev_rank {
                return Err(Error::Structural("sections not in canonical order"));
            }
            prev_rank = rank;
            // record section + reject duplicate mandatory kinds.
            record_section(&mut sections, e.kind, e.offset, e.length)?;
            prev_end = end;
        }
        // step 6: file ends exactly at the last section.
        if prev_end != header.file_size {
            return Err(Error::Structural("trailing bytes after last section"));
        }
        // required sections present.
        let feed_meta = sections.feed_meta.ok_or(Error::Structural("missing feed-meta"))?;
        let index = sections.index.ok_or(Error::Structural("missing index"))?;
        let signature = sections.signature.ok_or(Error::Structural("missing signature"))?;
        // signature length 0 in v3.0 (step 10).
        if header.version_minor == 0 && signature.1 != 0 {
            return Err(Error::Structural("signature length != 0 in v3.0"));
        }

        // step 7: feed-meta is validated lazily by feed_meta(); validate it now once.
        // step 8: index sub-header.
        let (idx_off, idx_len) = index;
        if idx_len < spec::INDEX_SUBHEADER_SIZE as u64 {
            return Err(Error::Structural("index shorter than its sub-header"));
        }
        let sub = IndexSubHeader::decode(&bytes[idx_off as usize..])?;
        if sub.key_width != ip_version.key_width() {
            return Err(Error::Structural("key_width != header ip_version"));
        }
        if sub.record_count != header.entry_count {
            return Err(Error::Structural("record_count != header.entry_count"));
        }
        let rsz = u64::from(sub.record_size);
        if sub.record_count > (spec::MAX_U64 - spec::INDEX_SUBHEADER_SIZE as u64) / rsz.max(1) {
            return Err(Error::Overflow("record_count * record_size"));
        }
        if spec::INDEX_SUBHEADER_SIZE as u64 + sub.record_count * rsz != idx_len {
            return Err(Error::Structural("index length != 32 + record_count*record_size"));
        }
        let index_records = (
            (idx_off + spec::INDEX_SUBHEADER_SIZE as u64) as usize,
            (sub.record_count * rsz) as usize,
        );

        // step 9: values section structure (if present).
        let mut values_count = 0u32;
        if let Some((voff, vlen)) = sections.values {
            values_count = validate_values(&bytes[voff as usize..(voff + vlen) as usize])?;
        }

        let r = Reader {
            bytes,
            header,
            ip_version,
            feed_meta: (feed_meta.0 as usize, feed_meta.1 as usize),
            index_records,
            record_count: sub.record_count,
            values: sections.values.map(|(o, l)| (o as usize, l as usize)),
            values_count,
        };
        // step 7: validate feed-meta content eagerly, so both open() and
        // open_metadata_only() are a complete structural gate (not deferred to the
        // feed_meta() accessor).
        r.feed_meta()?;
        Ok(r)
    }

    /// Step 11 — per-record safety walk in a single forward pass.
    fn walk_records(&self) -> Result<()> {
        match self.ip_version {
            IpVersion::V4 => self.walk::<Ipv4Key>(),
            IpVersion::V6 => self.walk::<Ipv6Key>(),
        }
    }

    fn walk<K: IpKey>(&self) -> Result<()> {
        let (off, len) = self.index_records;
        let recs = &self.bytes[off..off + len];
        let mut walked = 0u64;
        let mut prev_end: Option<K> = None;
        let mut any_value = false;
        let mut unique: u128 = 0; // recomputed unique-IP count (§5 SHOULD)
        let mut i = 0usize;
        while i < recs.len() {
            let r = crate::wire::Record::<K>::decode(&recs[i..])?; // checks v6 pad==0
            if r.start > r.end {
                return Err(Error::Invariant("record start > end"));
            }
            if let Some(pe) = prev_end {
                if r.start <= pe {
                    return Err(Error::Invariant("index not sorted/disjoint"));
                }
            }
            // value_id bounds (sentinel or < values_count).
            if r.value_id != spec::VALUE_ID_NONE {
                if r.value_id >= self.values_count {
                    return Err(Error::Invariant("value_id out of range"));
                }
                any_value = true;
            }
            // accumulate the unique-IP count; a full-IPv6-space range is
            // unrepresentable in the 128-bit header, so reject it.
            let size = K::range_size(r.start, r.end)
                .ok_or(Error::Invariant("range covers the entire IPv6 space"))?;
            unique = unique
                .checked_add(size)
                .ok_or(Error::Overflow("unique_ip_count recompute"))?;
            prev_end = Some(r.end);
            walked += 1;
            i += K::RECORD_SIZE;
        }
        if walked != self.record_count {
            return Err(Error::Invariant("walked record count != entry_count"));
        }
        // recomputed unique-IP count MUST match the header (catches header-field
        // corruption that section hashes — which do not cover the header — cannot).
        let header_unique =
            (u128::from(self.header.unique_ip_count_hi) << 64) | u128::from(self.header.unique_ip_count_lo);
        if unique != header_unique {
            return Err(Error::Invariant("unique_ip_count != recomputed sum"));
        }
        // "used ⇒ present" is covered above (value_id < values_count, which is 0 when
        // the section is absent). Reject a non-sentinel use with no values section.
        if any_value && self.values.is_none() {
            return Err(Error::Structural("value_id used but no values section"));
        }
        Ok(())
    }

    /// Step 12 — verify each present section's SHA-256 against its directory hash.
    fn verify_hashes(&self) -> Result<()> {
        // Re-walk the directory to get hashes; cheap (few entries).
        let dir_off = self.header.directory_offset as usize;
        for i in 0..self.header.directory_count as u64 {
            let at = dir_off + (i as usize) * spec::DIR_ENTRY_SIZE;
            let e = DirEntry::decode(&self.bytes[at..])?;
            let body = &self.bytes[e.offset as usize..(e.offset + e.length) as usize];
            let got: [u8; 32] = Sha256::digest(body).into();
            if got != e.hash {
                return Err(Error::IntegrityFailed("section hash mismatch"));
            }
        }
        Ok(())
    }

    /// Numeric binary search (lower-bound) over the index records (zero-alloc).
    fn search<K: IpKey>(&self, key: K) -> Option<Hit> {
        let (off, _len) = self.index_records;
        let recs = &self.bytes[off..];
        let n = self.record_count;
        let mut lo = 0u64;
        let mut hi = n;
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            let at = (mid as usize) * K::RECORD_SIZE;
            let start = K::read_le(&recs[at..]);
            if key < start {
                hi = mid;
            } else {
                lo = mid + 1;
            }
        }
        if lo == 0 {
            return None;
        }
        let at = ((lo - 1) as usize) * K::RECORD_SIZE;
        let start = K::read_le(&recs[at..]);
        let end = K::read_le(&recs[at + K::WIDTH..]);
        if start <= key && key <= end {
            let value_id = u32_le(recs, at + 2 * K::WIDTH);
            Some(Hit { value_id })
        } else {
            None
        }
    }
}

/// Owned-mutable mode: load a validated file back into an editable [`Writer`] so a
/// caller can add/remove ranges and re-serialize. Because the file's records are
/// already sorted+coalesced, loading and immediately rebuilding is byte-idempotent.
#[cfg(feature = "alloc")]
impl Reader<'_> {
    /// Reconstruct an editable IPv4 [`Writer`] from this file (`Err` if not IPv4).
    pub fn to_writer_v4(&self) -> Result<crate::writer::Writer<Ipv4Key>> {
        if self.ip_version != IpVersion::V4 {
            return Err(Error::Structural("to_writer_v4 on a non-IPv4 file"));
        }
        self.to_writer::<Ipv4Key>()
    }

    /// Reconstruct an editable IPv6 [`Writer`] from this file (`Err` if not IPv6).
    pub fn to_writer_v6(&self) -> Result<crate::writer::Writer<Ipv6Key>> {
        if self.ip_version != IpVersion::V6 {
            return Err(Error::Structural("to_writer_v6 on a non-IPv6 file"));
        }
        self.to_writer::<Ipv6Key>()
    }

    fn to_writer<K: IpKey>(&self) -> Result<crate::writer::Writer<K>> {
        use crate::writer::{FeedMeta, Value, Writer};
        let fm = self.feed_meta()?;
        let meta = FeedMeta {
            name: fm.name.into(),
            category: fm.category.into(),
            maintainer: fm.maintainer.into(),
            maintainer_url: fm.maintainer_url.into(),
            source_url: fm.source_url.into(),
            license: fm.license.into(),
        };
        let mut w = Writer::<K>::new(meta, self.header.license_flags, self.header.generation_unixtime);
        let (off, len) = self.index_records;
        let recs = &self.bytes[off..off + len];
        let mut i = 0usize;
        while i < recs.len() {
            let r = crate::wire::Record::<K>::decode(&recs[i..])?;
            let value = if r.value_id == spec::VALUE_ID_NONE {
                None
            } else {
                let v = self.value(r.value_id).ok_or(Error::Invariant("dangling value_id"))?;
                Some(Value { type_id: v.type_id, bytes: v.bytes.to_vec() })
            };
            w.add_range(r.start, r.end, value)?;
            i += K::RECORD_SIZE;
        }
        Ok(w)
    }
}

/// Canonical-position rank: signature (kind 5) sorts last; every other kind ranks by
/// its numeric value (which is exactly the §4/§8 band order, with reserved 4/6
/// between 3 and 7).
fn canon_rank(kind: u32) -> u64 {
    if kind == SectionKind::Signature.id() {
        u64::MAX
    } else {
        u64::from(kind)
    }
}

fn record_section(s: &mut Sections, kind: u32, offset: u64, length: u64) -> Result<()> {
    let slot = match SectionKind::from_id(kind) {
        Some(SectionKind::FeedMeta) => &mut s.feed_meta,
        Some(SectionKind::Index) => &mut s.index,
        Some(SectionKind::Values) => &mut s.values,
        Some(SectionKind::Signature) => &mut s.signature,
        None => return Ok(()), // unknown/optional kind: not tracked
    };
    if slot.is_some() {
        return Err(Error::Structural("duplicate mandatory section"));
    }
    *slot = Some((offset, length));
    Ok(())
}

/// Validate a values section's structure and return its `count` (§10).
fn validate_values(b: &[u8]) -> Result<u32> {
    if b.len() < 4 {
        return Err(Error::Structural("values section shorter than count"));
    }
    let count = u32_le(b, 0);
    if count == 0 {
        return Err(Error::Structural("values section present with count 0"));
    }
    let mut pos = 4usize;
    for _ in 0..count {
        if pos + 8 > b.len() {
            return Err(Error::Structural("values entry header past section"));
        }
        let type_id = u32_le(b, pos);
        if type_id == 0 {
            return Err(Error::Structural("values entry type_id 0"));
        }
        let blen = u32_le(b, pos + 4) as usize;
        pos += 8;
        if pos + blen > b.len() {
            return Err(Error::Structural("values entry bytes past section"));
        }
        if type_id == 1 {
            validate_membership_set(&b[pos..pos + blen])?;
        }
        pos += blen;
    }
    if pos != b.len() {
        return Err(Error::Structural("values section length not exact"));
    }
    Ok(count)
}

/// Validate a `type_id == 1` membership set (§10): non-empty, a multiple of 4 bytes,
/// and strictly-ascending little-endian `u32` feed-ids. A reader MUST enforce this on
/// untrusted files — the writer guarantees it, but a hand-crafted file might not.
fn validate_membership_set(b: &[u8]) -> Result<()> {
    if b.is_empty() || b.len() % 4 != 0 {
        return Err(Error::Invariant("type_id 1 membership set malformed"));
    }
    let mut prev: Option<u32> = None;
    for chunk in b.chunks_exact(4) {
        let id = u32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
        if let Some(p) = prev {
            if id <= p {
                return Err(Error::Invariant("type_id 1 feed-ids not strictly ascending"));
            }
        }
        prev = Some(id);
    }
    Ok(())
}

/// Memory-mapped file reader (Unix). Implements the §15 mmap-safety steps:
/// `O_NOFOLLOW` open, `fstat` the open fd, refuse non-regular/sparse files, re-`fstat`
/// after mapping to catch replacement, and probe the last byte to surface truncation.
///
/// Gated on `unix`: the safety steps use Unix-only syscalls, so the `mmap` feature is
/// a no-op on non-Unix targets (the crate still builds; use [`Reader::open`] on bytes).
#[cfg(all(feature = "mmap", unix))]
pub mod mmap {
    use super::*;
    use std::fs::File;
    use std::os::fd::AsRawFd;
    use std::os::unix::fs::{FileExt, MetadataExt, OpenOptionsExt};
    use std::path::Path;

    /// An mmap'd file whose bytes can be handed to [`Reader::open`].
    #[derive(Debug)]
    pub struct MmapFile {
        map: memmap2::Mmap,
    }

    impl MmapFile {
        /// Open and map `path` read-only with the §15 mmap-safety steps. On any
        /// safety failure (symlink, sparse hole, truncation, or replacement during
        /// open) it returns an error; the caller can fall back to reading the file
        /// into a `Vec<u8>` and using [`Reader::open`]. The caller remains responsible
        /// for a trusted parent directory (hardlinks / intermediate symlinks).
        pub fn open(path: &Path) -> Result<MmapFile> {
            // O_NOFOLLOW refuses an attacker-controlled symlink at the final component.
            let file = File::options()
                .read(true)
                .custom_flags(libc::O_NOFOLLOW)
                .open(path)?;
            let fd = file.as_raw_fd();
            // fstat the OPEN fd (never re-stat by path) — TOCTOU-safe.
            let meta = file.metadata()?;
            if !meta.file_type().is_file() {
                return Err(Error::Structural("not a regular file"));
            }
            let len = meta.len();
            if len < u64::from(spec::HEADER_SIZE) {
                return Err(Error::FileTooShort { need: u64::from(spec::HEADER_SIZE), have: len });
            }
            // Hole detection: a hole inside a mapped section SIGBUSes despite bounds
            // checks. SEEK_HOLE from 0 must report the first hole at EOF (== len).
            // SAFETY: fd is a valid open descriptor for the lifetime of `file`.
            let hole = unsafe { libc::lseek(fd, 0, libc::SEEK_HOLE) };
            if hole < 0 {
                // SEEK_HOLE unsupported on this filesystem (e.g. EINVAL/ENXIO). Per
                // §15, a reader without hole detection MUST not mmap — the caller
                // should read the file into a Vec and use Reader::open instead.
                return Err(Error::Structural(
                    "hole detection unavailable on this filesystem — use Reader::open on bytes, not mmap",
                ));
            }
            if hole as u64 != len {
                return Err(Error::Structural("sparse file (hole detected) — use Reader::open on bytes, not mmap"));
            }
            // SAFETY: read-only mapping of a file we treat as immutable; never written.
            let map = unsafe { memmap2::Mmap::map(&file)? };
            // Re-fstat after mapping: reject if size/inode/device changed (the file was
            // replaced between the first fstat and the mmap).
            let meta2 = file.metadata()?;
            if meta2.len() != len || meta2.ino() != meta.ino() || meta2.dev() != meta.dev() {
                return Err(Error::Structural("file changed during mmap (TOCTOU)"));
            }
            // Probe the last mapped byte: a residual truncation faults here (catchable),
            // not on a hot lookup. pread past EOF returns 0, not SIGBUS — check the count.
            let mut b = [0u8; 1];
            match file.read_at(&mut b, len - 1) {
                Ok(1) => {}
                _ => return Err(Error::Structural("file truncated after fstat (probe failed)")),
            }
            Ok(MmapFile { map })
        }

        /// The mapped bytes.
        pub fn bytes(&self) -> &[u8] {
            &self.map
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::writer::{FeedMeta, Value, Writer};

    fn meta() -> FeedMeta {
        FeedMeta {
            name: "firehol_level1".into(),
            category: "attacks".into(),
            maintainer: "FireHOL".into(),
            license: "GPL-2.0".into(),
            ..Default::default()
        }
    }

    fn build_v4() -> Vec<u8> {
        let mut w = Writer::<Ipv4Key>::new(meta(), spec::LICENSE_FLAG_DONT_REDISTRIBUTE, 1700);
        w.add_range(Ipv4Key(0x0a00_0000), Ipv4Key(0x0a00_00ff), None).unwrap();
        w.add_range(
            Ipv4Key(0x0b00_0000),
            Ipv4Key(0x0b00_000f),
            Some(Value { type_id: 2, bytes: vec![7] }),
        )
        .unwrap();
        w.build().unwrap()
    }

    #[test]
    fn round_trip_open_and_feed_meta() {
        let bytes = build_v4();
        let r = Reader::open(&bytes).unwrap();
        assert_eq!(r.ip_version(), IpVersion::V4);
        assert_eq!(r.record_count(), 2);
        let fm = r.feed_meta().unwrap();
        assert_eq!(fm.name, "firehol_level1");
        assert_eq!(fm.category, "attacks");
        assert_eq!(fm.license, "GPL-2.0");
    }

    #[test]
    fn owned_mutable_reload_is_byte_idempotent() {
        let bytes = build_v4();
        let r = Reader::open(&bytes).unwrap();
        // Load -> rebuild with no edits must reproduce the exact same file.
        let rebuilt = r.to_writer_v4().unwrap().build().unwrap();
        assert_eq!(rebuilt, bytes, "reload+rebuild must be byte-idempotent");
    }

    #[test]
    fn owned_mutable_edit_then_rebuild() {
        let bytes = build_v4();
        let r = Reader::open(&bytes).unwrap();
        let mut w = r.to_writer_v4().unwrap();
        // Add a new disjoint range, rebuild, and confirm it reads back with the edit.
        w.add_range(Ipv4Key(0x0c00_0000), Ipv4Key(0x0c00_0000), None).unwrap();
        let edited = w.build().unwrap();
        let r2 = Reader::open(&edited).unwrap();
        assert_eq!(r2.record_count(), 3);
        assert!(r2.lookup_v4(Ipv4Key(0x0c00_0000)).unwrap().is_some());
        // original ranges still present.
        assert!(r2.lookup_v4(Ipv4Key(0x0a00_0080)).unwrap().is_some());
    }

    #[test]
    fn lookups_hit_and_miss() {
        let bytes = build_v4();
        let r = Reader::open(&bytes).unwrap();
        // inside the first range -> present, no value.
        let h = r.lookup_v4(Ipv4Key(0x0a00_0080)).unwrap().unwrap();
        assert_eq!(h.value_id, spec::VALUE_ID_NONE);
        // inside the second range -> value present.
        let h = r.lookup_v4(Ipv4Key(0x0b00_0005)).unwrap().unwrap();
        assert_ne!(h.value_id, spec::VALUE_ID_NONE);
        let v = r.value(h.value_id).unwrap();
        assert_eq!(v.type_id, 2);
        assert_eq!(v.bytes, &[7]);
        // a gap -> not found.
        assert!(r.lookup_v4(Ipv4Key(0x0c00_0000)).unwrap().is_none());
        // boundaries.
        assert!(r.lookup_v4(Ipv4Key(0x0a00_0000)).unwrap().is_some());
        assert!(r.lookup_v4(Ipv4Key(0x0a00_00ff)).unwrap().is_some());
        assert!(r.lookup_v4(Ipv4Key(0x0a00_0100)).unwrap().is_none());
    }

    #[test]
    fn wrong_family_lookup_errors() {
        let bytes = build_v4();
        let r = Reader::open(&bytes).unwrap();
        assert!(r.lookup_v6(Ipv6Key::MIN).is_err());
    }

    #[test]
    fn tamper_file_size_rejected() {
        let mut bytes = build_v4();
        let n = bytes.len();
        bytes.truncate(n - 1); // now file_size header != real size
        assert!(matches!(Reader::open(&bytes), Err(Error::FileSizeMismatch { .. })));
    }

    #[test]
    fn tamper_index_byte_rejected() {
        let bytes = build_v4();
        let h = Reader::open(&bytes).unwrap();
        let idx_off = h.index_records.0;
        let mut bytes2 = bytes.clone();
        bytes2[idx_off] ^= 0xff; // flip a record's start byte
        // The safety walk (sortedness / recomputed unique-IP count) catches this
        // before the hash step; either way the file MUST be rejected.
        assert!(Reader::open(&bytes2).is_err());
    }

    #[test]
    fn tamper_value_byte_fails_hash() {
        // A flipped byte inside an opaque (type_id 2) value passes the structural
        // walk (which doesn't inspect value content) and is caught by §15 step 12.
        let bytes = build_v4(); // second range carries a type_id 2 value {7}
        let r = Reader::open(&bytes).unwrap();
        let (voff, _vlen) = r.values.expect("values section present");
        let mut bytes2 = bytes.clone();
        // values layout: count(4) | type_id(4) | byte_length(4) | bytes... — so the
        // first entry's payload byte is at voff+12.
        bytes2[voff + 12] ^= 0xff;
        assert!(matches!(Reader::open(&bytes2), Err(Error::IntegrityFailed(_))));
    }

    #[test]
    fn reader_rejects_non_ascending_membership_set() {
        let mut w = Writer::<Ipv4Key>::new(meta(), 0, 0);
        let body = [1u32.to_le_bytes(), 5u32.to_le_bytes()].concat(); // ascending ids
        w.add_range(Ipv4Key(10), Ipv4Key(20), Some(Value { type_id: 1, bytes: body })).unwrap();
        let bytes = w.build().unwrap();
        let r = Reader::open(&bytes).unwrap();
        let (voff, _) = r.values.expect("values present");
        // payload (count4|type4|len4|bytes) ids at voff+12 (id0) and voff+16 (id1);
        // rewrite to [5, 1] (non-ascending) — validate_values (pre-hash) must reject.
        let mut b2 = bytes.clone();
        b2[voff + 12] = 5;
        b2[voff + 16] = 1;
        assert!(matches!(Reader::open(&b2), Err(Error::Invariant(_))));
    }

    #[test]
    fn reader_rejects_corrupted_unique_count() {
        let bytes = build_v4();
        let mut b2 = bytes.clone();
        b2[56] ^= 0xff; // unique_ip_count_lo (header offset 56) — header is not hashed
        assert!(matches!(Reader::open(&b2), Err(Error::Invariant(_))));
    }

    #[test]
    fn writer_rejects_reserved_license_flags() {
        let mut w = Writer::<Ipv4Key>::new(meta(), 0b10, 0); // reserved bit set
        w.add_range(Ipv4Key(1), Ipv4Key(2), None).unwrap();
        assert!(matches!(w.build(), Err(Error::InvalidInput(_))));
    }

    // Hand-build a structurally-consistent empty v4 file whose feed-meta section is
    // exactly `fm` bytes (offsets, lengths, and section hashes all correct).
    fn craft_v4_with_feed_meta(fm: &[u8]) -> Vec<u8> {
        craft_v4_with_feed_meta_ver(fm, 0)
    }

    fn craft_v4_with_feed_meta_ver(fm: &[u8], version_minor: u16) -> Vec<u8> {
        use crate::wire::{DirEntry, Header, IndexSubHeader};
        let hash = |b: &[u8]| -> [u8; 32] { Sha256::digest(b).into() };
        let index_bytes = IndexSubHeader { record_size: 12, key_width: 4, record_count: 0 }.encode();
        let dir_end = 72 + 3 * spec::DIR_ENTRY_SIZE as u64;
        let fm_off = spec::align_up(dir_end, 8).unwrap();
        let fm_len = fm.len() as u64;
        let idx_off = spec::align_up(fm_off + fm_len, 16).unwrap();
        let idx_len = index_bytes.len() as u64;
        let sig_off = spec::align_up(idx_off + idx_len, 8).unwrap();
        let header = Header {
            version_minor, header_size: 72, flags: 0, file_size: sig_off,
            directory_offset: 72, directory_count: 3, license_flags: 0, entry_count: 0,
            generation_unixtime: 0, unique_ip_count_lo: 0, unique_ip_count_hi: 0,
        };
        let entries = [
            DirEntry { kind: 1, flags: 1, offset: fm_off, length: fm_len, align: 8, hash: hash(fm) },
            DirEntry { kind: 2, flags: 1, offset: idx_off, length: idx_len, align: 16, hash: hash(&index_bytes) },
            DirEntry { kind: 5, flags: 0, offset: sig_off, length: 0, align: 8, hash: hash(&[]) },
        ];
        let mut out = header.encode().to_vec();
        for e in &entries {
            out.extend_from_slice(&e.encode());
        }
        out.resize(fm_off as usize, 0);
        out.extend_from_slice(fm);
        out.resize(idx_off as usize, 0);
        out.extend_from_slice(&index_bytes);
        out.resize(sig_off as usize, 0);
        out
    }

    #[test]
    fn reader_rejects_short_feed_meta_without_panic() {
        // feed-meta length 0 (< 4) — must be REJECTED, not an out-of-bounds panic
        // (regression for the round-2 P1). The file is otherwise fully consistent.
        let bytes = craft_v4_with_feed_meta(&[]);
        assert!(matches!(Reader::open(&bytes), Err(Error::Structural(_))));
        // a well-formed hand-built feed-meta (count 6 + 6 empty fields) opens fine.
        let mut fm = 6u32.to_le_bytes().to_vec();
        for _ in 0..6 {
            fm.extend_from_slice(&0u32.to_le_bytes());
        }
        assert!(Reader::open(&craft_v4_with_feed_meta(&fm)).is_ok());
        // trailing bytes after the 6 fields violate the exact-length rule.
        fm.push(0);
        assert!(matches!(Reader::open(&craft_v4_with_feed_meta(&fm)), Err(Error::Structural(_))));
    }

    #[test]
    fn reader_accepts_future_extra_feed_meta_fields() {
        // A future v3.1 file may declare >6 feed-meta fields; a v3.0 reader reads the
        // 6 it knows and skips the extras (additive forward-compat, §7).
        let mut fm = 8u32.to_le_bytes().to_vec(); // 8 fields
        for i in 0..8u32 {
            fm.extend_from_slice(&1u32.to_le_bytes()); // each field: 1 byte
            fm.push(b'a' + i as u8);
        }
        let bytes = craft_v4_with_feed_meta_ver(&fm, 1); // version_minor = 1
        let r = Reader::open(&bytes).unwrap();
        let v = r.feed_meta().unwrap();
        assert_eq!(v.name, "a"); // field 0
        assert_eq!(v.license, "f"); // field 5; fields 6,7 skipped
        // but a v3.0 file (version_minor 0) with >6 fields is still rejected.
        let v0 = craft_v4_with_feed_meta(&fm);
        assert!(Reader::open(&v0).is_err());
    }

    #[test]
    fn metadata_only_skips_record_validation() {
        let bytes = build_v4();
        let r = Reader::open_metadata_only(&bytes).unwrap();
        assert_eq!(r.feed_meta().unwrap().name, "firehol_level1");
    }

    #[test]
    fn round_trip_v6() {
        let mut w = Writer::<Ipv6Key>::new(meta(), 0, 1);
        let a = Ipv6Key { hi: 0x2001_0db8_0000_0000, lo: 0 };
        let b = Ipv6Key { hi: 0x2001_0db8_0000_0000, lo: 0xffff };
        w.add_range(a, b, None).unwrap();
        let bytes = w.build().unwrap();
        let r = Reader::open(&bytes).unwrap();
        assert_eq!(r.ip_version(), IpVersion::V6);
        assert!(r.lookup_v6(Ipv6Key { hi: 0x2001_0db8_0000_0000, lo: 0x100 }).unwrap().is_some());
        assert!(r.lookup_v6(Ipv6Key { hi: 0x2001_0db8_0000_0001, lo: 0 }).unwrap().is_none());
    }
}
