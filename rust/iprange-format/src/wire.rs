//! Field-by-field (de)serialization of the fixed on-disk structures.
//!
//! This is the byte contract. Encoding produces exact-size arrays; decoding parses a
//! byte slice the caller has already bounds-checked, verifying only the structural
//! self-consistency that belongs with the bytes (magic, version_major, reserved/pad
//! zero, header_size sanity). Cross-field policy (§15: `file_size == real`, offsets,
//! ordering, alignment, the record walk, integrity) lives in the reader.
//!
//! Per §3 the layout is **packed, little-endian, field-by-field** — never a struct
//! cast — so it is identical on every host and trivially mirrored in pure Go.

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::spec::{self, IpVersion};

#[inline]
fn u16_le(b: &[u8], at: usize) -> u16 {
    u16::from_le_bytes([b[at], b[at + 1]])
}
#[inline]
fn u32_le(b: &[u8], at: usize) -> u32 {
    u32::from_le_bytes([b[at], b[at + 1], b[at + 2], b[at + 3]])
}
#[inline]
fn u64_le(b: &[u8], at: usize) -> u64 {
    let mut x = [0u8; 8];
    x.copy_from_slice(&b[at..at + 8]);
    u64::from_le_bytes(x)
}
#[inline]
fn put_u16(b: &mut [u8], at: usize, v: u16) {
    b[at..at + 2].copy_from_slice(&v.to_le_bytes());
}
#[inline]
fn put_u32(b: &mut [u8], at: usize, v: u32) {
    b[at..at + 4].copy_from_slice(&v.to_le_bytes());
}
#[inline]
fn put_u64(b: &mut [u8], at: usize, v: u64) {
    b[at..at + 8].copy_from_slice(&v.to_le_bytes());
}

/// The fixed 72-byte file header (§5). `magic` and `version_major` are implied
/// constants; `entry_count`/`unique_ip_count_*` are computed (backpatched) fields.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Header {
    /// `version_minor` (0 for the v3.0 contract).
    pub version_minor: u16,
    /// `header_size` (72 in v3.0).
    pub header_size: u16,
    /// `flags`: bit0 ip_version; bits 1–15 reserved=0.
    pub flags: u16,
    /// Total file size; MUST equal the real size (reader-checked).
    pub file_size: u64,
    /// Directory offset (== `header_size`).
    pub directory_offset: u64,
    /// Number of directory entries.
    pub directory_count: u32,
    /// `license_flags`: bit0 dont_redistribute; bits 1–31 reserved=0.
    pub license_flags: u32,
    /// Number of index records (computed).
    pub entry_count: u64,
    /// Generation time, POSIX seconds; externally supplied.
    pub generation_unixtime: u64,
    /// Low 64 bits of the unique-IP count (computed).
    pub unique_ip_count_lo: u64,
    /// High 64 bits of the unique-IP count (computed); 0 for IPv4.
    pub unique_ip_count_hi: u64,
}

impl Header {
    /// Encode to the canonical 72 bytes. `magic`/`version_major` are written from the
    /// spec constants; reserved bytes are zero by construction.
    pub fn encode(&self) -> [u8; 72] {
        let mut b = [0u8; 72];
        b[0..8].copy_from_slice(&spec::MAGIC);
        put_u16(&mut b, 8, spec::VERSION_MAJOR);
        put_u16(&mut b, 10, self.version_minor);
        put_u16(&mut b, 12, self.header_size);
        put_u16(&mut b, 14, self.flags);
        put_u64(&mut b, 16, self.file_size);
        put_u64(&mut b, 24, self.directory_offset);
        put_u32(&mut b, 32, self.directory_count);
        put_u32(&mut b, 36, self.license_flags);
        put_u64(&mut b, 40, self.entry_count);
        put_u64(&mut b, 48, self.generation_unixtime);
        put_u64(&mut b, 56, self.unique_ip_count_lo);
        put_u64(&mut b, 64, self.unique_ip_count_hi);
        b
    }

    /// Parse a header from at least 72 bytes, applying the bytes-level gate: magic,
    /// `version_major == 3`, `header_size` sanity (§5/§15 steps 1–2), and the header
    /// `flags`/`license_flags` reserved bits (§3). Cross-field policy is the reader's.
    pub fn decode(b: &[u8]) -> Result<Header> {
        if b.len() < spec::HEADER_SIZE as usize {
            return Err(Error::FileTooShort {
                need: spec::HEADER_SIZE as u64,
                have: b.len() as u64,
            });
        }
        if b[0..8] != spec::MAGIC {
            return Err(Error::BadMagic);
        }
        let version_major = u16_le(b, 8);
        if version_major != spec::VERSION_MAJOR {
            return Err(Error::UnsupportedMajor(version_major));
        }
        let version_minor = u16_le(b, 10);
        let header_size = u16_le(b, 12);
        if header_size < spec::HEADER_SIZE || header_size % 8 != 0 {
            return Err(Error::BadHeaderSize(header_size));
        }
        if version_minor == 0 && header_size != spec::HEADER_SIZE {
            return Err(Error::BadHeaderSize(header_size));
        }
        let flags = u16_le(b, 14);
        if flags & !spec::FLAG_IP_VERSION != 0 {
            return Err(Error::NonZeroReserved("header.flags bits 1-15"));
        }
        let license_flags = u32_le(b, 36);
        if license_flags & !spec::LICENSE_FLAG_DONT_REDISTRIBUTE != 0 {
            return Err(Error::NonZeroReserved("header.license_flags bits 1-31"));
        }
        Ok(Header {
            version_minor,
            header_size,
            flags,
            file_size: u64_le(b, 16),
            directory_offset: u64_le(b, 24),
            directory_count: u32_le(b, 32),
            license_flags,
            entry_count: u64_le(b, 40),
            generation_unixtime: u64_le(b, 48),
            unique_ip_count_lo: u64_le(b, 56),
            unique_ip_count_hi: u64_le(b, 64),
        })
    }

    /// The IP family from `flags` bit 0.
    pub fn ip_version(&self) -> IpVersion {
        if self.flags & spec::FLAG_IP_VERSION != 0 {
            IpVersion::V6
        } else {
            IpVersion::V4
        }
    }
}

/// A 72-byte directory entry (§6). `hash` is the full 32-byte SHA-256 of the section
/// bytes, stored in standard digest order (never byte-swapped).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DirEntry {
    /// Section kind id (§8).
    pub kind: u32,
    /// `flags`: bit0 must_understand; bits 1–31 reserved=0.
    pub flags: u32,
    /// Absolute file offset of the section.
    pub offset: u64,
    /// Section length in bytes.
    pub length: u64,
    /// Required alignment of `offset`.
    pub align: u64,
    /// Full SHA-256 of the section bytes.
    pub hash: [u8; 32],
}

impl DirEntry {
    /// Encode to the canonical 72 bytes (the `reserved` u64 at offset 32 is zero).
    pub fn encode(&self) -> [u8; 72] {
        let mut b = [0u8; 72];
        put_u32(&mut b, 0, self.kind);
        put_u32(&mut b, 4, self.flags);
        put_u64(&mut b, 8, self.offset);
        put_u64(&mut b, 16, self.length);
        put_u64(&mut b, 24, self.align);
        // bytes 32..40 = reserved (zero)
        b[40..72].copy_from_slice(&self.hash);
        b
    }

    /// Parse one directory entry from at least 72 bytes, checking `flags` reserved
    /// bits and the `reserved` u64 are zero (§3). Other rules (kind!=0, align set,
    /// offset/length bounds, sort, non-overlap) are cross-entry policy in the reader.
    pub fn decode(b: &[u8]) -> Result<DirEntry> {
        if b.len() < spec::DIR_ENTRY_SIZE {
            return Err(Error::FileTooShort {
                need: spec::DIR_ENTRY_SIZE as u64,
                have: b.len() as u64,
            });
        }
        let flags = u32_le(b, 4);
        if flags & !spec::DIR_FLAG_MUST_UNDERSTAND != 0 {
            return Err(Error::NonZeroReserved("dir_entry.flags bits 1-31"));
        }
        if u64_le(b, 32) != 0 {
            return Err(Error::NonZeroReserved("dir_entry.reserved"));
        }
        let mut hash = [0u8; 32];
        hash.copy_from_slice(&b[40..72]);
        Ok(DirEntry {
            kind: u32_le(b, 0),
            flags,
            offset: u64_le(b, 8),
            length: u64_le(b, 16),
            align: u64_le(b, 24),
            hash,
        })
    }
}

/// The 32-byte index sub-header (§9).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct IndexSubHeader {
    /// 12 (v4) or 40 (v6).
    pub record_size: u32,
    /// 4 (v4) or 16 (v6).
    pub key_width: u32,
    /// Number of records; MUST equal header `entry_count` (reader-checked).
    pub record_count: u64,
}

impl IndexSubHeader {
    /// Encode to the canonical 32 bytes (the trailing 16 reserved bytes are zero).
    pub fn encode(&self) -> [u8; 32] {
        let mut b = [0u8; 32];
        put_u32(&mut b, 0, self.record_size);
        put_u32(&mut b, 4, self.key_width);
        put_u64(&mut b, 8, self.record_count);
        // bytes 16..32 = reserved (zero)
        b
    }

    /// Parse from at least 32 bytes, checking the 16 reserved bytes are zero and
    /// `(record_size, key_width)` is a valid pair (12↔4 or 40↔16, §9).
    pub fn decode(b: &[u8]) -> Result<IndexSubHeader> {
        if b.len() < spec::INDEX_SUBHEADER_SIZE {
            return Err(Error::FileTooShort {
                need: spec::INDEX_SUBHEADER_SIZE as u64,
                have: b.len() as u64,
            });
        }
        if b[16..32].iter().any(|&x| x != 0) {
            return Err(Error::NonZeroReserved("index_subheader.reserved"));
        }
        let record_size = u32_le(b, 0);
        let key_width = u32_le(b, 4);
        match (key_width, record_size) {
            (4, 12) | (16, 40) => {}
            _ => return Err(Error::Structural("index sub-header record_size/key_width mismatch")),
        }
        Ok(IndexSubHeader {
            record_size,
            key_width,
            record_count: u64_le(b, 8),
        })
    }
}

/// One interval-map record: an inclusive `[start, end]` range and its `value_id`
/// (`0xFFFF_FFFF` = "present, no value"). Generic over the key width.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Record<K: IpKey> {
    /// Range start (inclusive).
    pub start: K,
    /// Range end (inclusive).
    pub end: K,
    /// Index into the values table, or `VALUE_ID_NONE`.
    pub value_id: u32,
}

impl<K: IpKey> Record<K> {
    /// Encode this record into the first `K::RECORD_SIZE` bytes of `out`. The v6 pad
    /// is zero by construction. Panics if `out` is shorter than `RECORD_SIZE`.
    pub fn encode_into(&self, out: &mut [u8]) {
        // start | end | value_id, then (v6 only) a 4-byte zero pad.
        self.start.write_le(&mut out[0..K::WIDTH]);
        self.end.write_le(&mut out[K::WIDTH..2 * K::WIDTH]);
        put_u32(out, 2 * K::WIDTH, self.value_id);
        // out[2*WIDTH+4 .. RECORD_SIZE] is the pad; left zero by the caller's buffer.
        debug_assert_eq!(2 * K::WIDTH + 4 + pad_len::<K>(), K::RECORD_SIZE);
    }

    /// Parse a record from the first `K::RECORD_SIZE` bytes of `src`, checking the v6
    /// `pad` is zero (§3/§9). Panics if `src` is shorter than `RECORD_SIZE`.
    pub fn decode(src: &[u8]) -> Result<Record<K>> {
        let start = K::read_le(&src[0..K::WIDTH]);
        let end = K::read_le(&src[K::WIDTH..2 * K::WIDTH]);
        let value_id = u32_le(src, 2 * K::WIDTH);
        // v6 has a 4-byte pad that MUST be zero (it is hashed, so determinism needs it).
        let pad_off = 2 * K::WIDTH + 4;
        if src[pad_off..K::RECORD_SIZE].iter().any(|&x| x != 0) {
            return Err(Error::NonZeroReserved("v6 record pad"));
        }
        Ok(Record { start, end, value_id })
    }
}

/// Bytes of trailing pad in a record of key `K` (0 for v4, 4 for v6).
const fn pad_len<K: IpKey>() -> usize {
    K::RECORD_SIZE - (2 * K::WIDTH + 4)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::{Ipv4Key, Ipv6Key};

    #[test]
    fn struct_sizes_match_spec() {
        assert_eq!(Header { ..zero_header() }.encode().len(), 72);
        assert_eq!(core::mem::size_of::<[u8; spec::DIR_ENTRY_SIZE]>(), 72);
        assert_eq!(spec::INDEX_SUBHEADER_SIZE, 32);
        assert_eq!(Ipv4Key::RECORD_SIZE, 12);
        assert_eq!(Ipv6Key::RECORD_SIZE, 40);
        assert_eq!(pad_len::<Ipv4Key>(), 0);
        assert_eq!(pad_len::<Ipv6Key>(), 4);
    }

    fn zero_header() -> Header {
        Header {
            version_minor: 0,
            header_size: 72,
            flags: 0,
            file_size: 0,
            directory_offset: 72,
            directory_count: 0,
            license_flags: 0,
            entry_count: 0,
            generation_unixtime: 0,
            unique_ip_count_lo: 0,
            unique_ip_count_hi: 0,
        }
    }

    #[test]
    fn header_round_trip_and_magic() {
        let h = Header {
            version_minor: 0,
            header_size: 72,
            flags: spec::FLAG_IP_VERSION, // IPv6
            file_size: 0x1122_3344_5566_7788,
            directory_offset: 72,
            directory_count: 4,
            license_flags: spec::LICENSE_FLAG_DONT_REDISTRIBUTE,
            entry_count: 7,
            generation_unixtime: 1_700_000_000,
            unique_ip_count_lo: 42,
            unique_ip_count_hi: 1,
        };
        let bytes = h.encode();
        assert_eq!(&bytes[0..8], b"IPRANGE3");
        assert_eq!(u16_le(&bytes, 8), 3); // version_major
        assert_eq!(Header::decode(&bytes).unwrap(), h);
        assert_eq!(h.ip_version(), IpVersion::V6);
    }

    #[test]
    fn header_rejects_bad_magic_and_major() {
        let mut b = zero_header().encode();
        b[0] = b'X';
        assert!(matches!(Header::decode(&b), Err(Error::BadMagic)));
        let mut b = zero_header().encode();
        put_u16(&mut b, 8, 4); // version_major 4
        assert!(matches!(Header::decode(&b), Err(Error::UnsupportedMajor(4))));
    }

    #[test]
    fn header_rejects_reserved_flag_bits() {
        let mut b = zero_header().encode();
        put_u16(&mut b, 14, 0b10); // reserved flag bit set
        assert!(matches!(Header::decode(&b), Err(Error::NonZeroReserved(_))));
    }

    #[test]
    fn dir_entry_round_trip() {
        let e = DirEntry {
            kind: spec::SectionKind::Index.id(),
            flags: spec::DIR_FLAG_MUST_UNDERSTAND,
            offset: 144,
            length: 32,
            align: 16,
            hash: spec::SHA256_EMPTY,
        };
        let b = e.encode();
        assert_eq!(u64_le(&b, 32), 0, "reserved is zero");
        assert_eq!(DirEntry::decode(&b).unwrap(), e);
    }

    #[test]
    fn index_subheader_round_trip_and_pairing() {
        let s = IndexSubHeader {
            record_size: 40,
            key_width: 16,
            record_count: 3,
        };
        assert_eq!(IndexSubHeader::decode(&s.encode()).unwrap(), s);
        // mismatched pairing rejected.
        let bad = IndexSubHeader {
            record_size: 12,
            key_width: 16,
            record_count: 0,
        };
        assert!(matches!(
            IndexSubHeader::decode(&bad.encode()),
            Err(Error::Structural(_))
        ));
    }

    #[test]
    fn record_round_trip_v6_pad_zero() {
        let r = Record::<Ipv6Key> {
            start: Ipv6Key { hi: 0x2001_0db8_0000_0000, lo: 0 },
            end: Ipv6Key { hi: 0x2001_0db8_0000_0000, lo: 0xffff },
            value_id: spec::VALUE_ID_NONE,
        };
        let mut buf = [0u8; 40];
        r.encode_into(&mut buf);
        assert_eq!(&buf[36..40], &[0, 0, 0, 0], "v6 pad is zero");
        assert_eq!(Record::<Ipv6Key>::decode(&buf).unwrap(), r);
        // non-zero pad is rejected.
        buf[36] = 1;
        assert!(matches!(
            Record::<Ipv6Key>::decode(&buf),
            Err(Error::NonZeroReserved(_))
        ));
    }

    #[test]
    fn record_round_trip_v4() {
        let r = Record::<Ipv4Key> {
            start: Ipv4Key(0x0a00_0000),
            end: Ipv4Key(0x0a00_00ff),
            value_id: 5,
        };
        let mut buf = [0u8; 12];
        r.encode_into(&mut buf);
        assert_eq!(Record::<Ipv4Key>::decode(&buf).unwrap(), r);
    }
}
