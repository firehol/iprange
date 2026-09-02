//! Legacy binary save/load (v1.0 IPv4, v2.0 IPv6) with the exact C
//! validation rules and diagnostics.
//!
//! Ported 1:1 from `src/ipset_binary.c` (v1.0) and
//! `src/ipset6_binary.c` (v2.0). The v4 payload is:
//!
//! ```text
//! iprange binary format v1.0\n
//! optimized\n | non-optimized\n
//! record size 8\n
//! records N\n
//! bytes N*8+4\n
//! lines L\n
//! unique ips U\n
//! <u32 marker 0x1A2B3C4D, native endian>
//! <N records: u32 lo, u32 hi, native endian>
//! ```
//!
//! The v2 payload adds an `ipv6\n` family line after the header and
//! uses 32-byte records; each u128 is written as the native
//! `uint128_t` layout of the C (lo64 then hi64 on little-endian
//! hosts), and `unique ips` is decimal u128. The header `unique ips`
//! is validated against a recomputation over the records before it
//! is trusted, exactly like the C loader.

use std::io;

use super::range::{IpNum, IpSet, Range};

const BINARY_HEADER_V10: &[u8] = b"iprange binary format v1.0\n";
const BINARY_HEADER_V20: &[u8] = b"iprange binary format v2.0\n";
const ENDIAN_MARKER: u32 = 0x1A2B3C4D;

/// fgets()-style line reader over an in-memory payload: each line is
/// the bytes up to and including `\n`, or the remaining bytes when
/// the payload ends without a newline (the C buffer would hold that
/// final chunk at EOF).
struct LineReader<'a> {
    data: &'a [u8],
    pos: usize,
}

impl<'a> LineReader<'a> {
    fn new(data: &'a [u8]) -> Self {
        LineReader { data, pos: 0 }
    }

    fn next(&mut self) -> Option<&'a [u8]> {
        if self.pos >= self.data.len() {
            return None;
        }
        let rest = &self.data[self.pos..];
        let end = rest
            .iter()
            .position(|&b| b == b'\n')
            .map(|i| i + 1)
            .unwrap_or(rest.len());
        let line = &rest[..end];
        self.pos += end;
        Some(line)
    }

    fn remaining(&self) -> usize {
        self.data.len() - self.pos
    }

    fn take(&mut self, n: usize) -> &'a [u8] {
        let out = &self.data[self.pos..self.pos + n];
        self.pos += n;
        out
    }
}

/// C `s ? s : ""` in the "found '%s'" diagnostics: EOF renders as
/// an empty quoted string.
fn line_text(line: Option<&[u8]>) -> String {
    line.map(|l| String::from_utf8_lossy(l).into_owned())
        .unwrap_or_default()
}

/// C `parse_binary_size_field` / `parse_binary_u64_field`: the value
/// must start with an ASCII digit, parse as an unsigned decimal, and
/// end at the line end (`\n` or end-of-buffer). The C prints the raw
/// rest-of-line (newline included) inside the quotes.
fn parse_u64_field(source: &str, field: &str, value: &[u8]) -> Result<u64, String> {
    let text = String::from_utf8_lossy(value).into_owned();
    if value.first().is_none_or(|b| !b.is_ascii_digit()) {
        return Err(format!("iprange: {source}: invalid {field} value '{text}'"));
    }
    let mut parsed: u64 = 0;
    let mut rest = value;
    while let Some((&b, tail)) = rest.split_first() {
        if !b.is_ascii_digit() {
            break;
        }
        parsed = parsed
            .checked_mul(10)
            .and_then(|v| v.checked_add((b - b'0') as u64))
            .ok_or_else(|| format!("iprange: {source}: invalid {field} value '{text}'"))?;
        rest = tail;
    }
    if !(rest.is_empty() || rest == b"\n") {
        return Err(format!("iprange: {source}: invalid {field} value '{text}'"));
    }
    Ok(parsed)
}

/// C `parse_binary6_u128_field`: decimal u128 with wrap detection
/// ("value overflow") and the same line-end rule.
fn parse_u128_field(source: &str, field: &str, value: &[u8]) -> Result<u128, String> {
    let text = String::from_utf8_lossy(value).into_owned();
    if value.first().is_none_or(|b| !b.is_ascii_digit()) {
        return Err(format!("iprange: {source}: invalid {field} value '{text}'"));
    }
    let mut parsed: u128 = 0;
    let mut rest = value;
    while let Some((&b, tail)) = rest.split_first() {
        if !b.is_ascii_digit() {
            break;
        }
        let next = parsed.wrapping_mul(10).wrapping_add((b - b'0') as u128);
        if next < parsed {
            return Err(format!("iprange: {source}: {field} value overflow"));
        }
        parsed = next;
        rest = tail;
    }
    if !(rest.is_empty() || rest == b"\n") {
        return Err(format!("iprange: {source}: invalid {field} value '{text}'"));
    }
    Ok(parsed)
}

/// C `binary_validate_payload` (src/ipset_binary.c): verifies record
/// order/adjacency, recomputes the unique count (direct sum for an
/// optimized payload, sort-and-merge sweep otherwise), and rejects a
/// header that claims optimized over a non-optimized payload.
fn validate_payload_v1(
    source: &str,
    header_optimized: bool,
    entries: usize,
    expected: u128,
    ranges: &[Range<u32>],
) -> Result<bool, String> {
    let mut payload_optimized = true;
    if entries == 0 {
        if expected != 0 {
            return Err(format!(
                "iprange: {source}: unique IPs ({expected}) do not match the binary payload (0)"
            ));
        }
        return Ok(true);
    }

    for (i, r) in ranges.iter().enumerate() {
        if r.lo > r.hi {
            return Err(format!(
                "iprange: {source}: invalid binary record {} has addr > broadcast",
                i + 1
            ));
        }
    }

    for i in 1..entries {
        let prev = ranges[i - 1];
        let curr = ranges[i];
        if curr.lo < prev.lo
            || curr.lo <= prev.hi
            || (prev.hi != u32::MAX && curr.lo == prev.hi + 1)
        {
            payload_optimized = false;
            break;
        }
    }

    let actual: u128 = if payload_optimized {
        ranges.iter().map(|r| r.hi as u128 - r.lo as u128 + 1).sum()
    } else {
        // C sorts (addr asc, broadcast desc) and sweep-merges to
        // count unique IPs of a non-optimized payload.
        let mut tmp = ranges.to_vec();
        tmp.sort_by(|a, b| a.lo.cmp(&b.lo).then_with(|| b.hi.cmp(&a.hi)));
        let mut lo = tmp[0].lo;
        let mut hi = tmp[0].hi;
        let mut total: u128 = 0;
        for r in &tmp[1..] {
            if r.hi <= hi {
                continue;
            }
            if r.lo <= hi || (hi != u32::MAX && r.lo == hi + 1) {
                hi = r.hi;
                continue;
            }
            total += hi as u128 - lo as u128 + 1;
            lo = r.lo;
            hi = r.hi;
        }
        total + (hi as u128 - lo as u128 + 1)
    };

    if expected != actual {
        return Err(format!(
            "iprange: {source}: unique IPs ({expected}) do not match the binary payload ({actual})"
        ));
    }

    if header_optimized && !payload_optimized {
        return Err(format!(
            "iprange: {source}: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records"
        ));
    }

    Ok(payload_optimized)
}

/// C `binary6_validate_payload` (src/ipset6_binary.c): same walk as
/// v1, but non-optimized payloads trust the header count (the set is
/// re-optimized after loading) and the unique total wraps like the
/// C u128 arithmetic.
fn validate_payload_v2(
    source: &str,
    header_optimized: bool,
    entries: usize,
    expected: u128,
    ranges: &[Range<u128>],
) -> Result<bool, String> {
    let mut payload_optimized = true;
    if entries == 0 {
        if expected != 0 {
            return Err(format!(
                "iprange: {source}: unique IPs do not match the binary payload"
            ));
        }
        return Ok(true);
    }

    for (i, r) in ranges.iter().enumerate() {
        if r.lo > r.hi {
            return Err(format!(
                "iprange: {source}: invalid binary record {} has addr > broadcast",
                i + 1
            ));
        }
    }

    for i in 1..entries {
        let prev = ranges[i - 1];
        let curr = ranges[i];
        if curr.lo < prev.lo
            || curr.lo <= prev.hi
            || (prev.hi != u128::MAX && curr.lo == prev.hi.wrapping_add(1))
        {
            payload_optimized = false;
            break;
        }
    }

    if !payload_optimized {
        if header_optimized {
            return Err(format!(
                "iprange: {source}: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records"
            ));
        }
        return Ok(false);
    }

    // C u128 sum wraps (a full-universe single record is 2^128 and
    // wraps to zero); replicate with wrapping arithmetic.
    let mut actual: u128 = 0;
    for r in ranges {
        actual = actual.wrapping_add(r.hi.wrapping_sub(r.lo).wrapping_add(1));
    }
    if expected != actual {
        return Err(format!(
            "iprange: {source}: unique IPs do not match the binary payload"
        ));
    }

    Ok(true)
}

/// Seven/eight-line header parse starting at the top of `r`.
/// Returns `(header_optimized, entries, bytes, lines, unique_ips)`.
/// `is_v2` selects the v2 grammar, family line, and error texts;
/// `record_bytes` is the record size the caller expects (8 or 32).
fn parse_header<'a>(
    r: &mut LineReader<'a>,
    source: &str,
    is_v2: bool,
    record_bytes: u64,
) -> Result<(bool, u64, u64, u64, u128), String> {
    let expected_header = if is_v2 {
        BINARY_HEADER_V20
    } else {
        BINARY_HEADER_V10
    };
    let v1 = !is_v2;

    let line = r.next();
    if line != Some(expected_header) {
        let what = if is_v2 {
            "expecting binary v2 header"
        } else {
            "expecting binary header"
        };
        return Err(format!(
            "iprange: {source} {what} but found '{}'.",
            line_text(line)
        ));
    }

    if is_v2 {
        let line = r.next();
        if line != Some(b"ipv6\n") {
            return Err(format!(
                "iprange: {source} expected family 'ipv6' but found '{}'.",
                line_text(line)
            ));
        }
    }

    let line = r.next();
    let header_optimized = match line {
        Some(b"optimized\n") => true,
        Some(b"non-optimized\n") => false,
        _ => {
            let what = if v1 {
                "2nd line should be the optimized flag,"
            } else {
                "expected optimized flag"
            };
            return Err(format!(
                "iprange: {source} {what} but found '{}'.",
                line_text(line)
            ));
        }
    };

    // The remaining five header lines carry a fixed key prefix; a
    // missing line or a wrong key uses the C per-line diagnostic.
    let mut field = |key: &[u8], what_v1: &str, what_v2: &str| -> Result<&'a [u8], String> {
        let line = r.next();
        match line {
            Some(line) if line.starts_with(key) => Ok(&line[key.len()..]),
            _ => {
                let what = if v1 { what_v1 } else { what_v2 };
                Err(format!(
                    "iprange: {source} {what} but found '{}'.",
                    line_text(line)
                ))
            }
        }
    };

    let value = field(
        b"record size ",
        "3rd line should be the record size,",
        "expected record size",
    )?;
    let size = parse_u64_field(source, "record size", value)?;
    if size != record_bytes {
        return Err(format!(
            "iprange: {source}: invalid record size {size} (expected {record_bytes})"
        ));
    }

    let value = field(
        b"records ",
        "4th line should be the number of records,",
        "expected records count",
    )?;
    let entries = parse_u64_field(source, "records", value)?;

    let value = field(
        b"bytes ",
        "5th line should be the number of bytes,",
        "expected bytes count",
    )?;
    let bytes = parse_u64_field(source, "bytes", value)?;

    let value = field(
        b"lines ",
        "6th line should be the number of lines read,",
        "expected lines count",
    )?;
    let lines = parse_u64_field(source, "lines", value)?;

    let value = field(
        b"unique ips ",
        "7th line should be the number of unique IPs,",
        "expected unique ips",
    )?;
    let unique = if v1 {
        parse_u64_field(source, "unique ips", value)? as u128
    } else {
        parse_u128_field(source, "unique ips", value)?
    };

    Ok((header_optimized, entries, bytes, lines, unique))
}

/// Read `entries` records of `record_bytes` bytes from `r`.
/// `read_one` decodes a single record.
fn read_records<'a, T: IpNum>(
    r: &mut LineReader<'a>,
    source: &str,
    entries: usize,
    record_bytes: usize,
    read_one: impl Fn(&[u8]) -> Range<T>,
) -> Result<Vec<Range<T>>, String> {
    let need = entries
        .checked_mul(record_bytes)
        .ok_or_else(|| format!("iprange: {source}: invalid number of records ({entries})"))?;
    if r.remaining() < need {
        let loaded = r.remaining() / record_bytes;
        return Err(format!(
            "iprange: {source}: expected to load {entries} entries, loaded {loaded}"
        ));
    }
    let mut ranges = Vec::with_capacity(entries);
    for _ in 0..entries {
        let raw = r.take(record_bytes);
        ranges.push(read_one(raw));
    }
    Ok(ranges)
}

/// C allocation-overflow guard (`invalid number of records`).
fn check_entries_overflow(source: &str, entries: u64, record_bytes: u64) -> Result<usize, String> {
    let max = ((usize::MAX as u64).saturating_sub(4)) / record_bytes;
    if entries > max {
        return Err(format!(
            "iprange: {source}: invalid number of records ({entries})"
        ));
    }
    Ok(entries as usize)
}

/// Read the u32 marker and verify it against the native endianness
/// marker (C `fread` + `endian != endianness` checks).
fn check_marker(r: &mut LineReader, source: &str) -> Result<(), String> {
    if r.remaining() < 4 {
        return Err(format!("iprange: {source}: cannot load ipset header"));
    }
    let raw: [u8; 4] = r.take(4).try_into().unwrap();
    if u32::from_ne_bytes(raw) != ENDIAN_MARKER {
        return Err(format!("iprange: {source}: incompatible endianness"));
    }
    Ok(())
}

/// Parse the released IPv4 binary payload (v1.0), validating the
/// exact header lines and native-endian record layout. Errors carry
/// the exact C diagnostic text of `src/ipset_binary.c` (the parse
/// layer adds the outer "Cannot fast load {name}" wrapper).
pub fn load_v1(data: &[u8], source: &str) -> Result<IpSet<u32>, String> {
    let mut r = LineReader::new(data);
    let (header_optimized, entries, bytes, lines, unique) = parse_header(&mut r, source, false, 8)?;

    // C: `entries > ((SIZE_MAX - 4) / sizeof(network_addr_t))`.
    let entries = check_entries_overflow(source, entries, 8)?;

    let expected_bytes = entries as u128 * 8 + 4;
    if bytes as u128 != expected_bytes {
        return Err(format!(
            "iprange: {source} invalid number of bytes, found {bytes}, expected {expected_bytes}."
        ));
    }

    check_marker(&mut r, source)?;

    if unique < entries as u128 {
        return Err(format!(
            "iprange: {source}: unique IPs ({unique}) cannot be less than entries ({entries})"
        ));
    }
    if lines < entries as u64 {
        return Err(format!(
            "iprange: {source}: lines ({lines}) cannot be less than entries ({entries})"
        ));
    }

    let ranges = read_records(&mut r, source, entries, 8, |raw: &[u8]| {
        let lo: [u8; 4] = raw[0..4].try_into().unwrap();
        let hi: [u8; 4] = raw[4..8].try_into().unwrap();
        Range {
            lo: u32::from_ne_bytes(lo),
            hi: u32::from_ne_bytes(hi),
        }
    })?;

    if r.remaining() != 0 {
        return Err(format!(
            "iprange: {source}: trailing data found after binary payload"
        ));
    }

    let payload_optimized =
        validate_payload_v1(source, header_optimized, entries, unique, &ranges)?;

    Ok(IpSet {
        ranges,
        entries,
        lines: lines as usize,
        unique,
        optimized: header_optimized && payload_optimized,
    })
}

/// Parse the released IPv6 binary payload (v2.0). Errors carry the
/// exact C diagnostic text of `src/ipset6_binary.c` (the parse layer
/// adds the outer "Cannot load binary v2 {name}" wrapper).
pub fn load_v2(data: &[u8], source: &str) -> Result<IpSet<u128>, String> {
    let mut r = LineReader::new(data);
    let (header_optimized, entries, bytes, lines, unique) = parse_header(&mut r, source, true, 32)?;

    // C: `entries > ((SIZE_MAX - 4) / sizeof(network_addr6_t))`.
    let entries = check_entries_overflow(source, entries, 32)?;

    let expected_bytes = entries as u128 * 32 + 4;
    if bytes as u128 != expected_bytes {
        return Err(format!(
            "iprange: {source} invalid number of bytes, found {bytes}, expected {expected_bytes}."
        ));
    }

    check_marker(&mut r, source)?;

    // C: `unique < entries && unique != 0`.
    if unique < entries as u128 && unique != 0 {
        return Err(format!(
            "iprange: {source}: unique IPs cannot be less than entries ({entries})"
        ));
    }
    if lines < entries as u64 {
        return Err(format!(
            "iprange: {source}: lines ({lines}) cannot be less than entries ({entries})"
        ));
    }

    let ranges = read_records(&mut r, source, entries, 32, |raw: &[u8]| {
        let lo: [u8; 16] = raw[0..16].try_into().unwrap();
        let hi: [u8; 16] = raw[16..32].try_into().unwrap();
        Range {
            lo: u128::from_ne_bytes(lo),
            hi: u128::from_ne_bytes(hi),
        }
    })?;

    if r.remaining() != 0 {
        return Err(format!(
            "iprange: {source}: trailing data found after binary payload"
        ));
    }

    let payload_optimized =
        validate_payload_v2(source, header_optimized, entries, unique, &ranges)?;

    Ok(IpSet {
        ranges,
        entries,
        lines: lines as usize,
        unique,
        optimized: header_optimized && payload_optimized,
    })
}

/// Serialize an optimized IPv4 set as the released v1.0 binary
/// payload (header lines, marker, native-endian records). An empty
/// set writes nothing (`test -s file` semantics of the C saver).
/// Generic over the family so the printer passes its set directly;
/// only the 32-bit family is meaningful (asserted).
pub fn write_v1<W: io::Write, F: IpNum>(w: &mut W, set: &IpSet<F>) -> io::Result<()> {
    debug_assert_eq!(F::BITS, 32);
    if set.entries == 0 {
        return Ok(());
    }
    w.write_all(BINARY_HEADER_V10)?;
    w.write_all(if set.optimized {
        b"optimized\n"
    } else {
        b"non-optimized\n"
    })?;
    write!(w, "record size 8\n")?;
    write!(w, "records {}\n", set.entries)?;
    write!(w, "bytes {}\n", set.entries * 8 + 4)?;
    write!(w, "lines {}\n", set.lines)?;
    write!(w, "unique ips {}\n", set.unique)?;
    w.write_all(&ENDIAN_MARKER.to_ne_bytes())?;
    for range in &set.ranges {
        w.write_all(&(range.lo.as_u128() as u32).to_ne_bytes())?;
        w.write_all(&(range.hi.as_u128() as u32).to_ne_bytes())?;
    }
    Ok(())
}

/// Serialize an optimized IPv6 set as the released v2.0 binary
/// payload. An empty set writes nothing. Generic over the family
/// (only the 128-bit family is meaningful, asserted).
pub fn write_v2<W: io::Write, F: IpNum>(w: &mut W, set: &IpSet<F>) -> io::Result<()> {
    debug_assert_eq!(F::BITS, 128);
    if set.entries == 0 {
        return Ok(());
    }
    w.write_all(BINARY_HEADER_V20)?;
    w.write_all(b"ipv6\n")?;
    w.write_all(if set.optimized {
        b"optimized\n"
    } else {
        b"non-optimized\n"
    })?;
    write!(w, "record size 32\n")?;
    write!(w, "records {}\n", set.entries)?;
    write!(w, "bytes {}\n", set.entries * 32 + 4)?;
    write!(w, "lines {}\n", set.lines)?;
    write!(w, "unique ips {}\n", set.unique)?;
    w.write_all(&ENDIAN_MARKER.to_ne_bytes())?;
    for range in &set.ranges {
        w.write_all(&range.lo.as_u128().to_ne_bytes())?;
        w.write_all(&range.hi.as_u128().to_ne_bytes())?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::legacy::range::Range;

    fn set4(ranges: &[(u32, u32)]) -> IpSet<u32> {
        let mut s = IpSet::default();
        for &(lo, hi) in ranges {
            s.add_range(Range { lo, hi });
        }
        s.optimize();
        s
    }

    fn set6(ranges: &[(u128, u128)]) -> IpSet<u128> {
        let mut s = IpSet::default();
        for &(lo, hi) in ranges {
            s.add_range(Range { lo, hi });
        }
        s.optimize();
        s
    }

    /// Assemble a v1 payload with the given header values and raw
    /// record bytes (marker is appended automatically).
    fn payload_v1(
        optimized: &str,
        record_size: &str,
        records: &str,
        bytes: &str,
        lines: &str,
        unique: &str,
        marker: &[u8],
        records_bytes: &[u8],
    ) -> Vec<u8> {
        let mut v = Vec::new();
        v.extend(b"iprange binary format v1.0\n");
        v.extend(optimized.as_bytes());
        v.push(b'\n');
        v.extend(format!("record size {record_size}\n").as_bytes());
        v.extend(format!("records {records}\n").as_bytes());
        v.extend(format!("bytes {bytes}\n").as_bytes());
        v.extend(format!("lines {lines}\n").as_bytes());
        v.extend(format!("unique ips {unique}\n").as_bytes());
        v.extend(marker);
        v.extend(records_bytes);
        v
    }

    /// Assemble a v2 payload (family line included).
    fn payload_v2(
        family: &str,
        optimized: &str,
        record_size: &str,
        records: &str,
        bytes: &str,
        lines: &str,
        unique: &str,
        records_bytes: &[u8],
    ) -> Vec<u8> {
        let mut v = Vec::new();
        v.extend(b"iprange binary format v2.0\n");
        v.extend(family.as_bytes());
        v.push(b'\n');
        v.extend(optimized.as_bytes());
        v.push(b'\n');
        v.extend(format!("record size {record_size}\n").as_bytes());
        v.extend(format!("records {records}\n").as_bytes());
        v.extend(format!("bytes {bytes}\n").as_bytes());
        v.extend(format!("lines {lines}\n").as_bytes());
        v.extend(format!("unique ips {unique}\n").as_bytes());
        v.extend(ENDIAN_MARKER.to_ne_bytes());
        v.extend(records_bytes);
        v
    }

    fn u32_le(v: u32) -> Vec<u8> {
        v.to_ne_bytes().to_vec()
    }

    fn u128_le(v: u128) -> Vec<u8> {
        v.to_ne_bytes().to_vec()
    }

    #[test]
    fn v1_write_matches_oracle_bytes() {
        let mut set = set4(&[(0xac10_6301, 0xac10_6301)]);
        set.lines = 1;
        let mut buf = Vec::new();
        write_v1(&mut buf, &set).unwrap();
        let mut expected = Vec::new();
        expected.extend(b"iprange binary format v1.0\noptimized\nrecord size 8\n");
        expected.extend(b"records 1\nbytes 12\nlines 1\nunique ips 1\n");
        expected.extend(0x1A2B3C4Du32.to_ne_bytes());
        expected.extend(0xac10_6301u32.to_ne_bytes());
        expected.extend(0xac10_6301u32.to_ne_bytes());
        assert_eq!(buf, expected);
    }

    #[test]
    fn v2_write_matches_oracle_bytes() {
        let mut set = set6(&[(
            0x2001_0db8_0000_0000_0000_0000_0000_0001,
            0x2001_0db8_0000_0000_0000_0000_0000_0001,
        )]);
        set.lines = 1;
        let mut buf = Vec::new();
        write_v2(&mut buf, &set).unwrap();
        let mut expected = Vec::new();
        expected.extend(b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\n");
        expected.extend(b"records 1\nbytes 36\nlines 1\nunique ips 1\n");
        expected.extend(0x1A2B3C4Du32.to_ne_bytes());
        expected.extend(0x2001_0db8_0000_0000_0000_0000_0000_0001u128.to_ne_bytes());
        expected.extend(0x2001_0db8_0000_0000_0000_0000_0000_0001u128.to_ne_bytes());
        assert_eq!(buf, expected);
    }

    #[test]
    fn v1_write_load_roundtrip() {
        let mut set = set4(&[(0x0a00_0001, 0x0a00_0001), (0x0a00_0008, 0x0a00_000b)]);
        set.lines = 2;
        assert_eq!(set.unique, 5);
        let mut buf = Vec::new();
        write_v1(&mut buf, &set).unwrap();
        let loaded = load_v1(&buf, "rt.bin").unwrap();
        assert_eq!(loaded.ranges, set.ranges);
        assert_eq!(loaded.entries, 2);
        assert_eq!(loaded.lines, 2);
        assert_eq!(loaded.unique, 5);
        assert!(loaded.optimized);
    }

    #[test]
    fn v2_write_load_roundtrip_with_mapped_v6() {
        // Mapped IPv4 1.2.3.4 plus a 2^96 /32 block.
        let mut set = set6(&[
            (
                0x0000_0000_0000_0000_0000_ffff_0102_0304,
                0x0000_0000_0000_0000_0000_ffff_0102_0304,
            ),
            (
                0x2001_0db8_0000_0000_0000_0000_0000_0000,
                0x2001_0db8_ffff_ffff_ffff_ffff_ffff_ffff,
            ),
        ]);
        set.lines = 2;
        let big = 1u128 << 96;
        assert_eq!(set.unique, 1 + big);
        let mut buf = Vec::new();
        write_v2(&mut buf, &set).unwrap();
        let loaded = load_v2(&buf, "rt6.bin").unwrap();
        assert_eq!(loaded.ranges, set.ranges);
        assert_eq!(loaded.entries, 2);
        assert_eq!(loaded.lines, 2);
        assert_eq!(loaded.unique, 1 + big);
        assert!(loaded.optimized);
        // The decimal u128 header round-trips byte-exact: the bytes
        // up to the marker are pure text ending with the unique line.
        let expected_text = format!(
            "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 2\nbytes 68\nlines 2\nunique ips {}\n",
            1 + big
        );
        assert!(buf.starts_with(expected_text.as_bytes()));
    }

    #[test]
    fn v1_empty_set_writes_nothing_and_loads() {
        let set = IpSet::<u32>::default();
        let mut buf = Vec::new();
        write_v1(&mut buf, &set).unwrap();
        assert!(buf.is_empty());

        let data = payload_v1(
            "optimized",
            "8",
            "0",
            "4",
            "0",
            "0",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[],
        );
        let loaded = load_v1(&data, "empty.bin").unwrap();
        assert!(loaded.ranges.is_empty());
        assert_eq!(loaded.entries, 0);
        assert_eq!(loaded.unique, 0);
        assert!(loaded.optimized);

        // A claimed non-zero unique over zero records is rejected.
        let bad = payload_v1(
            "optimized",
            "8",
            "0",
            "4",
            "0",
            "5",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[],
        );
        assert_eq!(
            load_v1(&bad, "bad.bin").unwrap_err(),
            "iprange: bad.bin: unique IPs (5) do not match the binary payload (0)"
        );
    }

    #[test]
    fn v1_load_rejects_wrong_header_text() {
        let data = b"iprange binary format v9.9\noptimized\nrecord size 8\nrecords 0\nbytes 4\nlines 0\nunique ips 0\n";
        assert_eq!(
            load_v1(data, "h.bin").unwrap_err(),
            "iprange: h.bin expecting binary header but found 'iprange binary format v9.9\n'."
        );
        // EOF at the header (no bytes at all).
        assert_eq!(
            load_v1(b"", "e.bin").unwrap_err(),
            "iprange: e.bin expecting binary header but found ''."
        );
        // Truncated header without newline.
        assert_eq!(
            load_v1(b"iprange binary format v1.0", "t.bin").unwrap_err(),
            "iprange: t.bin expecting binary header but found 'iprange binary format v1.0'."
        );
    }

    #[test]
    fn v1_load_rejects_bad_optimized_line() {
        let data = b"iprange binary format v1.0\nmaybe-optimized\n";
        assert_eq!(
            load_v1(data, "o.bin").unwrap_err(),
            "iprange: o.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'."
        );
    }

    #[test]
    fn v1_load_rejects_bad_record_size() {
        let data = payload_v1(
            "optimized",
            "4",
            "0",
            "4",
            "0",
            "0",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[],
        );
        assert_eq!(
            load_v1(&data, "s.bin").unwrap_err(),
            "iprange: s.bin: invalid record size 4 (expected 8)"
        );
    }

    #[test]
    fn v1_load_rejects_hexlike_records_field() {
        // test 57: "records 0x10" must fail like the C strtoull tail.
        let data = b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 0x10\nbytes 4\nlines 0\nunique ips 0\n";
        assert_eq!(
            load_v1(data, "x.bin").unwrap_err(),
            "iprange: x.bin: invalid records value '0x10\n'"
        );
    }

    #[test]
    fn v1_load_rejects_wrong_bytes() {
        let data = payload_v1(
            "optimized",
            "8",
            "1",
            "4",
            "1",
            "1",
            &ENDIAN_MARKER.to_ne_bytes(),
            &u32_le(1),
        );
        assert_eq!(
            load_v1(&data, "b.bin").unwrap_err(),
            "iprange: b.bin invalid number of bytes, found 4, expected 12."
        );
    }

    #[test]
    fn v1_load_rejects_marker_issues() {
        // No marker bytes at all.
        let data = payload_v1("optimized", "8", "0", "4", "0", "0", &[], &[]);
        assert_eq!(
            load_v1(&data, "m.bin").unwrap_err(),
            "iprange: m.bin: cannot load ipset header"
        );
        // Wrong marker value.
        let data = payload_v1(
            "optimized",
            "8",
            "0",
            "4",
            "0",
            "0",
            &0xDEADBEEFu32.to_ne_bytes(),
            &[],
        );
        assert_eq!(
            load_v1(&data, "m.bin").unwrap_err(),
            "iprange: m.bin: incompatible endianness"
        );
    }

    #[test]
    fn v1_load_rejects_unique_below_entries() {
        let data = payload_v1(
            "optimized",
            "8",
            "2",
            "20",
            "2",
            "1",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[u32_le(1), u32_le(1), u32_le(2), u32_le(2)].concat(),
        );
        assert_eq!(
            load_v1(&data, "u.bin").unwrap_err(),
            "iprange: u.bin: unique IPs (1) cannot be less than entries (2)"
        );
    }

    #[test]
    fn v1_load_rejects_lines_below_entries() {
        let data = payload_v1(
            "optimized",
            "8",
            "2",
            "20",
            "1",
            "2",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[u32_le(1), u32_le(1), u32_le(2), u32_le(2)].concat(),
        );
        assert_eq!(
            load_v1(&data, "l.bin").unwrap_err(),
            "iprange: l.bin: lines (1) cannot be less than entries (2)"
        );
    }

    #[test]
    fn v1_load_rejects_short_records() {
        let data = payload_v1(
            "optimized",
            "8",
            "2",
            "20",
            "2",
            "2",
            &ENDIAN_MARKER.to_ne_bytes(),
            &u32_le(1), // only one record's worth of bytes
        );
        assert_eq!(
            load_v1(&data, "short.bin").unwrap_err(),
            "iprange: short.bin: expected to load 2 entries, loaded 0"
        );
    }

    #[test]
    fn v1_load_rejects_trailing_garbage() {
        let data = payload_v1(
            "optimized",
            "8",
            "1",
            "12",
            "1",
            "1",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[u32_le(0x0102_0304), u32_le(0x0102_0304), b"JUNK".to_vec()].concat(),
        );
        assert_eq!(
            load_v1(&data, "trail.bin").unwrap_err(),
            "iprange: trail.bin: trailing data found after binary payload"
        );
    }

    #[test]
    fn v1_load_rejects_unique_mismatch() {
        // test 59 fake-counts case: unique 999 over one 4.3.2.1 record.
        let data = payload_v1(
            "optimized",
            "8",
            "1",
            "12",
            "999",
            "999",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[u32_le(0x0403_0201), u32_le(0x0403_0201)].concat(),
        );
        assert_eq!(
            load_v1(&data, "fake.bin").unwrap_err(),
            "iprange: fake.bin: unique IPs (999) do not match the binary payload (1)"
        );
    }

    #[test]
    fn v1_load_rejects_optimized_claim_over_overlapping() {
        // test 59 duplicate-records case: header optimized, records
        // overlap (second addr <= first broadcast).
        let data = payload_v1(
            "optimized",
            "8",
            "2",
            "20",
            "2",
            "2",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[
                u32_le(0x0403_0201),
                u32_le(0x0403_0202),
                u32_le(0x0403_0202),
                u32_le(0x0403_0202),
            ]
            .concat(),
        );
        assert_eq!(
            load_v1(&data, "dup.bin").unwrap_err(),
            "iprange: dup.bin: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records"
        );
    }

    #[test]
    fn v1_non_optimized_payload_recomputes_unique() {
        // Overlapping records with a truthful non-optimized header:
        // loads with the flag clear; a later optimize merges them.
        let data = payload_v1(
            "non-optimized",
            "8",
            "2",
            "20",
            "2",
            "2",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[
                u32_le(0x0403_0201),
                u32_le(0x0403_0202),
                u32_le(0x0403_0202),
                u32_le(0x0403_0202),
            ]
            .concat(),
        );
        let mut loaded = load_v1(&data, "raw.bin").unwrap();
        assert!(!loaded.optimized);
        assert_eq!(loaded.entries, 2);
        assert_eq!(loaded.unique, 2);
        loaded.optimize();
        assert_eq!(
            loaded.ranges,
            vec![Range {
                lo: 0x0403_0201,
                hi: 0x0403_0202
            }]
        );
        assert_eq!(loaded.unique, 2);

        // A lying unique count is still caught for non-optimized v1
        // (unique >= entries so the earlier entries check passes).
        let bad = payload_v1(
            "non-optimized",
            "8",
            "2",
            "20",
            "2",
            "3",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[
                u32_le(0x0403_0201),
                u32_le(0x0403_0202),
                u32_le(0x0403_0202),
                u32_le(0x0403_0202),
            ]
            .concat(),
        );
        assert_eq!(
            load_v1(&bad, "raw.bin").unwrap_err(),
            "iprange: raw.bin: unique IPs (3) do not match the binary payload (2)"
        );
    }

    #[test]
    fn v1_adjacent_records_with_optimized_header_load() {
        // Adjacency is allowed only with the guarded form: prev hi
        // below MAX and curr lo == prev hi + 1 is NOT optimized
        // disorder (test the detection loop edge).
        let data = payload_v1(
            "optimized",
            "8",
            "2",
            "20",
            "2",
            "3",
            &ENDIAN_MARKER.to_ne_bytes(),
            &[u32_le(1), u32_le(1), u32_le(2), u32_le(2)].concat(),
        );
        assert_eq!(
            load_v1(&data, "adj.bin").unwrap_err(),
            "iprange: adj.bin: unique IPs (3) do not match the binary payload (2)"
        );
    }

    #[test]
    fn v2_load_rejects_family_and_flag_lines() {
        let data = b"iprange binary format v2.0\nipv4\n";
        assert_eq!(
            load_v2(data, "f.bin").unwrap_err(),
            "iprange: f.bin expected family 'ipv6' but found 'ipv4\n'."
        );
        let data = b"iprange binary format v2.0\nipv6\noptimize\n";
        assert_eq!(
            load_v2(data, "f.bin").unwrap_err(),
            "iprange: f.bin expected optimized flag but found 'optimize\n'."
        );
    }

    #[test]
    fn v2_load_rejects_unique_mismatch_without_values() {
        let mut recs = Vec::new();
        recs.extend(u128_le(1));
        recs.extend(u128_le(1));
        let data = payload_v2("ipv6", "optimized", "32", "1", "36", "1", "7", &recs);
        assert_eq!(
            load_v2(&data, "u6.bin").unwrap_err(),
            "iprange: u6.bin: unique IPs do not match the binary payload"
        );
    }

    #[test]
    fn v2_load_rejects_unique_overflow() {
        // 2^128 cannot be represented in the u128 header field.
        let data = b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 0\nbytes 4\nlines 0\nunique ips 340282366920938463463374607431768211456\n";
        assert_eq!(
            load_v2(data, "o6.bin").unwrap_err(),
            "iprange: o6.bin: unique ips value overflow"
        );
    }

    #[test]
    fn v2_unique_zero_below_entries_is_allowed() {
        // C: `unique < entries && unique != 0` — zero is exempt.
        let mut recs = Vec::new();
        recs.extend(u128_le(1));
        recs.extend(u128_le(1));
        recs.extend(u128_le(2));
        recs.extend(u128_le(2));
        let data = payload_v2("ipv6", "non-optimized", "32", "2", "68", "2", "0", &recs);
        let loaded = load_v2(&data, "z6.bin").unwrap();
        assert_eq!(loaded.entries, 2);
        assert_eq!(loaded.unique, 0);
        assert!(!loaded.optimized);
    }

    #[test]
    fn v2_load_rejects_trailing_and_short() {
        let mut recs = Vec::new();
        recs.extend(u128_le(1));
        recs.extend(u128_le(1));
        recs.extend(b"JUNK");
        let data = payload_v2("ipv6", "optimized", "32", "1", "36", "1", "1", &recs);
        assert_eq!(
            load_v2(&data, "t6.bin").unwrap_err(),
            "iprange: t6.bin: trailing data found after binary payload"
        );
        let data = payload_v2("ipv6", "optimized", "32", "2", "68", "2", "2", &[]);
        assert_eq!(
            load_v2(&data, "s6.bin").unwrap_err(),
            "iprange: s6.bin: expected to load 2 entries, loaded 0"
        );
    }

    #[test]
    fn v2_write_load_roundtrip_large_u128_count() {
        // 2001:db8::/32 covers 2^96 addresses: the decimal u128
        // header line round-trips through both writers/readers.
        let mut set = set6(&[(
            0x2001_0db8_0000_0000_0000_0000_0000_0000,
            0x2001_0db8_ffff_ffff_ffff_ffff_ffff_ffff,
        )]);
        set.lines = 1;
        let big = 1u128 << 96;
        assert_eq!(set.unique, big);
        let mut buf = Vec::new();
        write_v2(&mut buf, &set).unwrap();
        let header_line = "unique ips 79228162514264337593543950336\n";
        assert!(buf
            .windows(header_line.len())
            .any(|w| w == header_line.as_bytes()));
        let loaded = load_v2(&buf, "big.bin").unwrap();
        assert_eq!(loaded.unique, big);
        assert_eq!(loaded.entries, 1);
        assert!(loaded.optimized);
    }

    #[test]
    fn write_v1_non_optimized_flag_roundtrip() {
        let mut set = set4(&[(1, 2), (5, 6)]);
        set.optimized = false;
        set.lines = 2;
        let mut buf = Vec::new();
        write_v1(&mut buf, &set).unwrap();
        assert!(buf.starts_with(b"iprange binary format v1.0\nnon-optimized\n"));
        let loaded = load_v1(&buf, "raw.bin").unwrap();
        assert!(!loaded.optimized);
        assert_eq!(loaded.entries, 2);
        assert_eq!(loaded.unique, set.unique);
    }
}
