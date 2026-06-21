//! Read-only parser for the **legacy** iprange binary format (v1.0 IPv4 / v2.0 IPv6)
//! produced by `iprange --print-binary`, for migration to v3.
//!
//! The byte layout is specified in `.agents/sow/specs/legacy-binary-format.md`
//! (verified against real `iprange --print-binary` artifacts). The one subtlety: a
//! legacy IPv6 address is stored `lo`-then-`hi` on a little-endian writer — the
//! opposite of v3's `hi`-then-`lo` key — so migration transposes the halves.
//!
//! Requires the `alloc` feature.

extern crate alloc;
use alloc::vec::Vec;

use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};

const MAGIC_V4: &str = "iprange binary format v1.0";
const MAGIC_V6: &str = "iprange binary format v2.0";
const MARKER_LE: [u8; 4] = [0x4D, 0x3C, 0x2B, 0x1A]; // 0x1A2B3C4D written little-endian

/// A parsed legacy file: the IP family with its inclusive `[start, end]` ranges plus
/// the header metadata. Ranges are in file order (sorted+disjoint when `optimized`).
#[derive(Clone, Debug)]
pub enum Legacy {
    /// IPv4 (v1.0).
    V4 {
        /// Whether the file declared itself optimized (sorted + disjoint).
        optimized: bool,
        /// Header `unique ips` (total addresses covered).
        unique_ips: u64,
        /// Header `lines` (source input lines).
        lines: u64,
        /// The ranges.
        ranges: Vec<(Ipv4Key, Ipv4Key)>,
    },
    /// IPv6 (v2.0).
    V6 {
        /// Whether the file declared itself optimized.
        optimized: bool,
        /// Header `unique ips` as a 128-bit count.
        unique_ips: u128,
        /// Header `lines`.
        lines: u64,
        /// The ranges (keys already transposed to v3's hi-then-lo form).
        ranges: Vec<(Ipv6Key, Ipv6Key)>,
    },
}

/// Parse a legacy binary file. Returns an error if it is not a recognized legacy
/// file or violates the legacy loader's structural rules.
pub fn parse(bytes: &[u8]) -> Result<Legacy> {
    let mut pos = 0usize;
    let magic = read_line(bytes, &mut pos)?;
    let is_v6 = if magic == MAGIC_V4 {
        false
    } else if magic == MAGIC_V6 {
        true
    } else {
        return Err(Error::InvalidInput("not a legacy iprange binary file"));
    };

    if is_v6 {
        let fam = read_line(bytes, &mut pos)?;
        if fam != "ipv6" {
            return Err(Error::InvalidInput("legacy v2.0 missing ipv6 line"));
        }
    }

    let opt_line = read_line(bytes, &mut pos)?;
    let optimized = match opt_line {
        "optimized" => true,
        "non-optimized" => false,
        _ => return Err(Error::InvalidInput("legacy optimized flag line malformed")),
    };

    let record_size = parse_prefixed(read_line(bytes, &mut pos)?, "record size ")?;
    let expect_rs: u64 = if is_v6 { 32 } else { 8 };
    if record_size != expect_rs {
        return Err(Error::InvalidInput("legacy record size mismatch for family"));
    }
    let records = parse_prefixed(read_line(bytes, &mut pos)?, "records ")?;
    let bytes_field = parse_prefixed(read_line(bytes, &mut pos)?, "bytes ")?;
    let lines = parse_prefixed(read_line(bytes, &mut pos)?, "lines ")?;
    let unique_line = read_line(bytes, &mut pos)?;
    let unique_str = strip_prefix(unique_line, "unique ips ")?;

    // payload bytes = records*record_size + 4 (the marker).
    let payload = records
        .checked_mul(record_size)
        .and_then(|p| p.checked_add(4))
        .ok_or(Error::Overflow("legacy bytes field"))?;
    if bytes_field != payload {
        return Err(Error::InvalidInput("legacy bytes field inconsistent"));
    }
    if lines < records {
        return Err(Error::InvalidInput("legacy lines < records"));
    }

    // endianness marker. Only little-endian is accepted: the legacy C tool refuses
    // cross-endian files, and §14 of the v3 spec rejects a big-endian marker. Real
    // legacy files come from x86-64 (little-endian); we never need the BE path.
    let marker = bytes.get(pos..pos + 4).ok_or(Error::InvalidInput("legacy truncated before marker"))?;
    if marker != MARKER_LE {
        return Err(Error::InvalidInput(
            "legacy file is not little-endian (big-endian rejected, matching the C tool and §14)",
        ));
    }
    pos += 4;

    // records must be exactly the remaining bytes (no trailing data). Compute in u64
    // so `records` is never truncated by a 32-bit `usize` before the multiply.
    let body = &bytes[pos..];
    let need = records
        .checked_mul(record_size)
        .ok_or(Error::Overflow("legacy payload size"))?;
    if body.len() as u64 != need {
        return Err(Error::InvalidInput("legacy payload length mismatch / trailing data"));
    }

    if is_v6 {
        let unique_ips: u128 = unique_str.parse().map_err(|_| Error::InvalidInput("legacy unique ips not a u128"))?;
        let ranges = parse_v6_records(body, records as usize)?;
        validate_v6(&ranges, optimized, unique_ips)?;
        Ok(Legacy::V6 { optimized, unique_ips, lines, ranges })
    } else {
        let unique_ips: u64 = unique_str.parse().map_err(|_| Error::InvalidInput("legacy unique ips not a u64"))?;
        let ranges = parse_v4_records(body, records as usize)?;
        validate_v4(&ranges, optimized, unique_ips)?;
        Ok(Legacy::V4 { optimized, unique_ips, lines, ranges })
    }
}

fn parse_v4_records(body: &[u8], n: usize) -> Result<Vec<(Ipv4Key, Ipv4Key)>> {
    let mut out = Vec::with_capacity(n);
    for i in 0..n {
        let b = &body[i * 8..i * 8 + 8];
        let addr = u32::from_le_bytes([b[0], b[1], b[2], b[3]]);
        let bcast = u32::from_le_bytes([b[4], b[5], b[6], b[7]]);
        if addr > bcast {
            return Err(Error::Invariant("legacy record addr > broadcast"));
        }
        out.push((Ipv4Key(addr), Ipv4Key(bcast)));
    }
    Ok(out)
}

fn parse_v6_records(body: &[u8], n: usize) -> Result<Vec<(Ipv6Key, Ipv6Key)>> {
    let mut out = Vec::with_capacity(n);
    for i in 0..n {
        let b = &body[i * 32..i * 32 + 32];
        let addr = rd_v6(&b[0..16]);
        let bcast = rd_v6(&b[16..32]);
        if addr > bcast {
            return Err(Error::Invariant("legacy record addr > broadcast"));
        }
        // a full-IPv6-space range (size 2^128) is unrepresentable in v3 — reject it
        // here so both languages fail at the legacy layer, not at migration.
        if addr.hi == 0 && addr.lo == 0 && bcast.hi == u64::MAX && bcast.lo == u64::MAX {
            return Err(Error::InvalidInput("legacy range covers the entire IPv6 space"));
        }
        out.push((addr, bcast));
    }
    Ok(out)
}

/// Read a legacy 16-byte little-endian IPv6 address into a v3 `hi`-then-`lo` key. The
/// legacy little-endian layout stores `{lo, hi}` (bytes 0–7 = `lo`, 8–15 = `hi`), the
/// opposite of v3's key, so this transposes the halves.
fn rd_v6(b: &[u8]) -> Ipv6Key {
    Ipv6Key { hi: u64_le(&b[8..16]), lo: u64_le(&b[0..8]) }
}

fn u64_le(b: &[u8]) -> u64 {
    let mut a = [0u8; 8];
    a.copy_from_slice(&b[0..8]);
    u64::from_le_bytes(a)
}

fn validate_v4(ranges: &[(Ipv4Key, Ipv4Key)], optimized: bool, unique_ips: u64) -> Result<()> {
    if (unique_ips as u128) < ranges.len() as u128 {
        return Err(Error::InvalidInput("legacy unique ips < records"));
    }
    if optimized {
        let mut sum: u128 = 0;
        for w in 0..ranges.len() {
            let (s, e) = ranges[w];
            if w > 0 && s.0 <= ranges[w - 1].1 .0 {
                return Err(Error::Invariant("legacy optimized records not sorted/disjoint"));
            }
            sum += u128::from(e.0 - s.0) + 1;
        }
        if sum != u128::from(unique_ips) {
            return Err(Error::InvalidInput("legacy unique ips != sum of ranges"));
        }
    }
    Ok(())
}

fn validate_v6(ranges: &[(Ipv6Key, Ipv6Key)], optimized: bool, unique_ips: u128) -> Result<()> {
    if unique_ips < ranges.len() as u128 {
        return Err(Error::InvalidInput("legacy unique ips < records"));
    }
    if optimized {
        let mut sum: u128 = 0;
        for w in 0..ranges.len() {
            let (s, e) = ranges[w];
            if w > 0 && s <= ranges[w - 1].1 {
                return Err(Error::Invariant("legacy optimized records not sorted/disjoint"));
            }
            // size = e - s + 1, checked. e >= s holds (per-record check), so the
            // subtraction cannot underflow; only the full IPv6 space overflows +1.
            let size = (e.to_u128() - s.to_u128())
                .checked_add(1)
                .ok_or(Error::InvalidInput("legacy range covers the entire IPv6 space"))?;
            sum = sum.checked_add(size).ok_or(Error::Overflow("legacy unique ips sum"))?;
        }
        if sum != unique_ips {
            return Err(Error::InvalidInput("legacy unique ips != sum of ranges"));
        }
    }
    Ok(())
}

/// Read one `\n`-terminated ASCII line (returned without the `\n`), advancing `pos`.
fn read_line<'a>(bytes: &'a [u8], pos: &mut usize) -> Result<&'a str> {
    let start = *pos;
    let rel = bytes[start..]
        .iter()
        .position(|&b| b == b'\n')
        .ok_or(Error::InvalidInput("legacy header line missing newline"))?;
    let line = &bytes[start..start + rel];
    *pos = start + rel + 1;
    core::str::from_utf8(line).map_err(|_| Error::InvalidInput("legacy header line not UTF-8"))
}

fn strip_prefix<'a>(line: &'a str, prefix: &str) -> Result<&'a str> {
    line.strip_prefix(prefix)
        .ok_or(Error::InvalidInput("legacy header line prefix mismatch"))
}

fn parse_prefixed(line: &str, prefix: &str) -> Result<u64> {
    strip_prefix(line, prefix)?
        .parse()
        .map_err(|_| Error::InvalidInput("legacy header numeric field malformed"))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Build a minimal legacy v2.0 file with a single record [addr16 || bcast16].
    fn legacy_v6(unique: &str, addr: [u8; 16], bcast: [u8; 16]) -> Vec<u8> {
        let mut b = Vec::new();
        b.extend_from_slice(b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips ");
        b.extend_from_slice(unique.as_bytes());
        b.push(b'\n');
        b.extend_from_slice(&MARKER_LE);
        b.extend_from_slice(&addr);
        b.extend_from_slice(&bcast);
        b
    }

    #[test]
    fn rejects_full_ipv6_space() {
        // full space [::, ffff:..:ffff]: legacy saturates the count to 2^128-1.
        let bytes = legacy_v6(
            "340282366920938463463374607431768211455",
            [0u8; 16],
            [0xFFu8; 16],
        );
        assert!(matches!(parse(&bytes), Err(Error::InvalidInput(_))));
    }

    #[test]
    fn rejects_big_endian_marker() {
        let mut bytes = legacy_v6("256", [0u8; 16], {
            let mut e = [0u8; 16];
            e[0] = 0xff; // lo low byte -> a tiny non-zero range end
            e
        });
        let marker_off = bytes.len() - 32 - 4;
        bytes[marker_off..marker_off + 4].copy_from_slice(&[0x1A, 0x2B, 0x3C, 0x4D]); // BE marker
        assert!(matches!(parse(&bytes), Err(Error::InvalidInput(_))));
    }
}
