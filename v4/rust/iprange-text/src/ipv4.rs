//! IPv4 family implementation for the legacy surface: inet_aton
//! numeric forms, netmask prefixes, mapped-IPv6 conversion, and
//! dotted-quad formatting.
//!
//! Ported 1:1 from the C oracle semantics:
//! - `src/iprange.h` `a_to_hl()` / `str2netaddr()` (inet_aton forms,
//!   octal/hex components, 8.8.16 and 8.24 shortened forms, netmask
//!   prefix fallback, `cidr_use_network` policy);
//! - `src/ipset_load.c` mapped-IPv6 extraction (`::ffff:a.b.c.d`);
//! - `src/ipset_print.c` `ip2str_r()` / `print_addr()` formatting.
//!
//! Verified against a freshly rebuilt oracle binary: every parse
//! form, error text, and range here was probed and matches.

use super::family::{Family, FamilyImpl};
use super::range::Range;

/// The C `a_to_hl()` failure text: `iprange: Invalid address %s.`
/// (the address token, without any `/prefix` suffix, and with the
/// trailing period).
fn invalid_address(token: &str) -> String {
    format!("iprange: Invalid address {token}.")
}

/// inet_aton component grammar, exactly as the oracle libc behaves:
/// one component, base chosen like C literals (0x hex, 0 octal,
/// otherwise decimal), component overflow rejected, hex requires at
/// least one digit after `0x`. The component is consumed at `*pos`
/// and the caller must ensure `*pos < bytes.len()` on entry.
fn parse_component(bytes: &[u8], pos: &mut usize) -> Result<u64, ()> {
    let start = *pos;
    let base = match bytes[start] {
        b'0' => {
            if start + 1 < bytes.len() && (bytes[start + 1] == b'x' || bytes[start + 1] == b'X') {
                16
            } else {
                8
            }
        }
        b'1'..=b'9' => 10,
        _ => return Err(()),
    };
    // Decimal and octal accumulate the leading digit itself (the `0`
    // of an octal is a zero-valued digit); hex skips the `0x` marker.
    let mut i = if base == 16 { start + 2 } else { start };
    let mut value: u64 = 0;
    let mut hex_digits = 0u32;
    while i < bytes.len() {
        let digit = match base {
            16 => match bytes[i] {
                b'0'..=b'9' => (bytes[i] - b'0') as u64,
                b'a'..=b'f' => (bytes[i] - b'a' + 10) as u64,
                b'A'..=b'F' => (bytes[i] - b'A' + 10) as u64,
                _ => break,
            },
            8 => match bytes[i] {
                b'0'..=b'7' => (bytes[i] - b'0') as u64,
                _ => break,
            },
            _ => match bytes[i] {
                b'0'..=b'9' => (bytes[i] - b'0') as u64,
                _ => break,
            },
        };
        value = match value.checked_mul(base).and_then(|v| v.checked_add(digit)) {
            Some(v) => v,
            // Accumulation overflow: the oracle rejects any component
            // magnitude above u32 (and all final field bounds are
            // below u32::MAX except the single-part form, which is
            // u32::MAX), so a u64 overflow is invalid regardless.
            None => return Err(()),
        };
        if base == 16 {
            hex_digits += 1;
        }
        i += 1;
    }
    if base == 16 && hex_digits == 0 {
        // The oracle rejects a bare `0x`/`0X` (no hex digits).
        return Err(());
    }
    *pos = i;
    Ok(value)
}

/// inet_aton semantics, exactly as observed from the oracle libc:
///
/// - 1..=4 dot-separated components, no empty components, no leading
///   or trailing dot;
/// - component base: `0x`/`0X` hex (>= 1 digit), leading `0` octal,
///   otherwise decimal; component magnitudes above `u32::MAX` are
///   rejected;
/// - shortened forms keep the BSD widths: `a.b` is 8.24 (`a.b.0.0`
///   is NOT how the oracle reads it) and `a.b.c` is 8.8.16;
/// - field bounds: 8-bit leading components, last component 32/24/16/8
///   bits for 1/2/3/4 parts.
///
/// The C line grammar strips whitespace before this parser runs, so
/// whitespace (which raw inet_aton partly tolerates) is rejected
/// here: tokens reaching `parse_addr` never contain it.
fn inet_aton(text: &str) -> Result<u32, ()> {
    let bytes = text.as_bytes();
    if bytes.is_empty() || !bytes[0].is_ascii_digit() {
        return Err(());
    }

    let mut parts = [0u64; 3];
    let mut parts_count = 0usize;
    let mut pos = 0usize;
    let value = loop {
        let part = parse_component(bytes, &mut pos)?;
        if pos < bytes.len() && bytes[pos] == b'.' {
            if parts_count >= 3 {
                // A fifth component (`a.b.c.d.e`) is invalid.
                return Err(());
            }
            parts[parts_count] = part;
            parts_count += 1;
            pos += 1;
            if pos == bytes.len() {
                // A dot must be followed by another component
                // (`1.2.3.` is invalid).
                return Err(());
            }
            continue;
        }
        if pos != bytes.len() {
            // Trailing junk after the last component.
            return Err(());
        }
        break part;
    };

    let fits = |v: u64, bits: u32| v <= ((1u64 << bits) - 1);
    let value = match parts_count + 1 {
        1 => {
            if value > u32::MAX as u64 {
                return Err(());
            }
            value as u32
        }
        2 => {
            if !fits(parts[0], 8) || !fits(value, 24) {
                return Err(());
            }
            ((parts[0] as u32) << 24) | value as u32
        }
        3 => {
            if !fits(parts[0], 8) || !fits(parts[1], 8) || !fits(value, 16) {
                return Err(());
            }
            ((parts[0] as u32) << 24) | ((parts[1] as u32) << 16) | value as u32
        }
        _ => {
            if !fits(parts[0], 8) || !fits(parts[1], 8) || !fits(parts[2], 8) || !fits(value, 8) {
                return Err(());
            }
            ((parts[0] as u32) << 24)
                | ((parts[1] as u32) << 16)
                | ((parts[2] as u32) << 8)
                | value as u32
        }
    };
    Ok(value)
}

/// C `netmask()`/`network()`/`broadcast()` in one step: the closed
/// range of `addr/prefix` under the `cidr_use_network` policy.
///
/// With fix-network the start is the network address and the end its
/// broadcast (host bits are silently masked away, never an error);
/// with `--dont-fix-network` the start stays the raw host address and
/// the end is still the broadcast of that raw start. `prefix == 0`
/// with fix-network is the full universe `[0, MAX]`; without
/// fix-network it starts at the raw address (oracle-verified:
/// `5.6.7.8/0 --dont-fix-network` prints `5.6.7.8-255.255.255.255`).
fn prefix_range(addr: u32, prefix: u32, fix_network: bool) -> Range<u32> {
    debug_assert!(prefix <= 32);
    let mask = if prefix == 0 {
        0
    } else {
        u32::MAX << (32 - prefix)
    };
    let lo = if fix_network { addr & mask } else { addr };
    Range { lo, hi: lo | !mask }
}

impl FamilyImpl for u32 {
    const FAMILY: Family = Family::V4;

    fn parse_addr(token: &str) -> Result<Self, String> {
        inet_aton(token).map_err(|()| invalid_address(token))
    }

    fn parse_cidr(
        token: &str,
        prefix: u32,
        fix_network: bool,
        _default_prefix: u32,
    ) -> Result<Range<Self>, String> {
        // C `str2netaddr()` parses the address (truncated at the
        // first '/') and the prefix text after it; when the token
        // carries no '/', the caller supplies the family default.
        let (address, prefix) = match token.find('/') {
            Some(slash) => {
                let prefix = Self::parse_prefix(&token[slash + 1..])?;
                (&token[..slash], prefix)
            }
            None => (token, prefix),
        };
        let addr = Self::parse_addr(address)?;
        Ok(prefix_range(addr, prefix, fix_network))
    }

    fn parse_prefix(text: &str) -> Result<u32, String> {
        // C `str2netaddr()` prefix branch: `strtol(text, 10)` first
        // (sign allowed, full-string consumption, long overflow
        // rejected), value 0..=32 wins; anything else falls back to
        // the netmask text form.
        let bytes = text.as_bytes();
        let mut i = 0usize;
        let negative = i < bytes.len() && bytes[i] == b'-';
        if i < bytes.len() && (bytes[i] == b'-' || bytes[i] == b'+') {
            i += 1;
        }
        let start = i;
        let mut value: i64 = 0;
        let mut overflow = false;
        while i < bytes.len() && bytes[i].is_ascii_digit() {
            let digit = (bytes[i] - b'0') as i64;
            match value.checked_mul(10).and_then(|v| v.checked_add(digit)) {
                Some(v) => value = v,
                None => overflow = true,
            }
            i += 1;
        }
        if !overflow && i == bytes.len() && i > start {
            let parsed = if negative { -value } else { value };
            if (0..=32).contains(&parsed) {
                return Ok(parsed as u32);
            }
        }
        // Netmask fallback: contiguous 1-bits of `!inet_aton(text)`
        // count the prefix; the C loop starts at 32 and shifts out
        // trailing 1-bits, then rejects any remaining bits. An
        // unparseable netmask reports the plain-address error first
        // (C `a_to_hl()` runs before the contiguity check).
        let address = inet_aton(text).map_err(|()| invalid_address(text))?;
        let mut mask = !address;
        let mut prefix = 32i32;
        while mask & 1 == 1 {
            mask >>= 1;
            prefix -= 1;
        }
        if mask != 0 {
            return Err(format!("iprange: Invalid netmask {text}"));
        }
        Ok(prefix as u32)
    }

    fn fmt_addr(addr: Self) -> String {
        // C `ip2str_r()`: dotted-quad decimal without leading zeros.
        format!(
            "{}.{}.{}.{}",
            (addr >> 24) & 0xff,
            (addr >> 16) & 0xff,
            (addr >> 8) & 0xff,
            addr & 0xff
        )
    }

    fn fmt_cidr(addr: Self, prefix: u32) -> String {
        // C `print_addr()`: any prefix below the family width prints
        // `addr/prefix` (including `/0`, whose address part the
        // pipeline has already masked to 0.0.0.0); the full width
        // (and anything above it) prints the bare address.
        if prefix < Self::BITS {
            format!("{}/{}", Self::fmt_addr(addr), prefix)
        } else {
            Self::fmt_addr(addr)
        }
    }

    fn convert_foreign(token: &str) -> Option<Range<Self>> {
        // C `ipset_load.c` mapped-IPv6 handling: exactly `::ffff:`
        // (the four f's case-insensitively, colon at offset 6)
        // followed by a non-empty `[0-9./]+` tail, then end of input.
        // Everything else is not this family's business (the parse
        // worker counts it as dropped IPv6). `::ffff:0102:0304` is
        // NOT converted (the tail scan stops at the second colon and
        // the C drops the line).
        let bytes = token.as_bytes();
        if bytes.len() < 8
            || bytes[0] != b':'
            || bytes[1] != b':'
            || bytes[6] != b':'
            || !bytes[2..6].iter().all(|c| *c == b'f' || *c == b'F')
        {
            return None;
        }
        // C scans `[0-9./]+`, then skips spaces/tabs and accepts at
        // end of record, newline, CR, '#' or ';' (the record keeps
        // its trailing newline here). Any other tail is dropped.
        let tail = &token[7..];
        let end = tail
            .bytes()
            .position(|c| !(c.is_ascii_digit() || c == b'.' || c == b'/'))
            .unwrap_or(tail.len());
        if end == 0 {
            return None;
        }
        let rest = tail[end..].trim_start_matches([' ', '\t']);
        if !rest.is_empty()
            && !rest.starts_with('\n')
            && !rest.starts_with('\r')
            && !rest.starts_with('#')
            && !rest.starts_with(';')
        {
            return None;
        }
        let v4 = &tail[..end];
        // C resolves the tail with `str2netaddr()` under the C
        // defaults (fix-network on, default prefix 32). A failed
        // tail parse drops the line (the C prints the underlying
        // parse error as a side effect; the drop warning itself is
        // the parse worker's).
        let (address, prefix) = match v4.find('/') {
            Some(p) => (
                Self::parse_addr(&v4[..p]).ok()?,
                Self::parse_prefix(&v4[p + 1..]).ok()?,
            ),
            None => (Self::parse_addr(v4).ok()?, 32),
        };
        Some(prefix_range(address, prefix, true))
    }
}

#[cfg(test)]
mod tests {
    use super::{FamilyImpl, Range};

    #[test]
    fn parse_addr_inet_aton_forms() {
        // Bare 32-bit integer (decimal).
        assert_eq!(u32::parse_addr("1"), Ok(0x0000_0001));
        assert_eq!(u32::parse_addr("123456"), Ok(0x0001_e240));
        assert_eq!(u32::parse_addr("4294967295"), Ok(0xffff_ffff));
        // Shortened forms keep the BSD widths (oracle: 1.2.3 -> 1.2.0.3).
        assert_eq!(u32::parse_addr("1.2.3"), Ok(0x0102_0003));
        assert_eq!(u32::parse_addr("1.2"), Ok(0x0100_0002));
        assert_eq!(u32::parse_addr("127.1"), Ok(0x7f00_0001));
        // Dotted quad.
        assert_eq!(u32::parse_addr("1.2.3.4"), Ok(0x0102_0304));
        assert_eq!(u32::parse_addr("0.0.0.0"), Ok(0));
        assert_eq!(u32::parse_addr("255.255.255.255"), Ok(u32::MAX));
        // Octal components.
        assert_eq!(u32::parse_addr("0177.0.0.1"), Ok(0x7f00_0001));
        assert_eq!(u32::parse_addr("00"), Ok(0));
        assert_eq!(u32::parse_addr("010.020.030.040"), Ok(0x0810_1820));
        // Hex components, 0x and 0X.
        assert_eq!(u32::parse_addr("0x7f000001"), Ok(0x7f00_0001));
        assert_eq!(u32::parse_addr("0X7F000001"), Ok(0x7f00_0001));
        assert_eq!(u32::parse_addr("0x7f.1"), Ok(0x7f00_0001));
        assert_eq!(u32::parse_addr("0x7f.0x7f"), Ok(0x7f00_007f));
    }

    #[test]
    fn parse_addr_rejects() {
        let bad = [
            "",
            "abc",
            "-1",
            "1.2.3.4.5",
            ".1.2.3",
            "1.2.3.",
            "1..2",
            "256.1.1.1",
            "256.1",
            "1.256.3",
            "1.2.3.256",
            "65535.65535",
            "1.2.65536",
            "0x",
            "0X",
            "0xg",
            "08",
            "0b101",
            "00x1",
            "4294967296",
            "0x100000000",
            "1.2.3.4x",
            " 1.2.3.4",
            "1.2.3.4 ",
        ];
        for t in bad {
            assert_eq!(
                u32::parse_addr(t),
                Err(format!("iprange: Invalid address {t}.")),
                "token {t:?}"
            );
        }
    }

    #[test]
    fn parse_cidr_masks() {
        // /24 with fix-network: host bits silently masked away.
        assert_eq!(
            u32::parse_cidr("1.2.3.4/24", 24, true, 32),
            Ok(Range {
                lo: 0x0102_0300,
                hi: 0x0102_03ff
            })
        );
        // --dont-fix-network: raw host start, prefix broadcast end.
        assert_eq!(
            u32::parse_cidr("1.2.3.4/24", 24, false, 32),
            Ok(Range {
                lo: 0x0102_0304,
                hi: 0x0102_03ff
            })
        );
        // /0 fix-network: full universe [0, MAX].
        assert_eq!(
            u32::parse_cidr("0.0.0.0/0", 0, true, 32),
            Ok(Range {
                lo: 0,
                hi: u32::MAX
            })
        );
        // /0 --dont-fix-network: oracle starts at the raw address.
        assert_eq!(
            u32::parse_cidr("5.6.7.8/0", 0, false, 32),
            Ok(Range {
                lo: 0x0506_0708,
                hi: u32::MAX
            })
        );
        // /32: single host. Token with and without the suffix text.
        assert_eq!(
            u32::parse_cidr("1.2.3.4/32", 32, true, 32),
            Ok(Range {
                lo: 0x0102_0304,
                hi: 0x0102_0304
            })
        );
        assert_eq!(
            u32::parse_cidr("1.2.3.4", 32, true, 32),
            Ok(Range {
                lo: 0x0102_0304,
                hi: 0x0102_0304
            })
        );
        // /0 via bare address with --default-prefix 0 (caller-owned prefix).
        assert_eq!(
            u32::parse_cidr("1.2.3.4", 0, true, 0),
            Ok(Range {
                lo: 0,
                hi: u32::MAX
            })
        );
        // Bad address part reports the address-only token, C text.
        assert_eq!(
            u32::parse_cidr("1.2.3.999/24", 24, true, 32),
            Err("iprange: Invalid address 1.2.3.999.".to_owned())
        );
    }

    #[test]
    fn parse_prefix_forms() {
        // Strict decimal 0..=32 (strtol base 10, octal-looking text
        // stays decimal: "010" is 10, oracle-verified).
        assert_eq!(u32::parse_prefix("0"), Ok(0));
        assert_eq!(u32::parse_prefix("00"), Ok(0));
        assert_eq!(u32::parse_prefix("010"), Ok(10));
        assert_eq!(u32::parse_prefix("24"), Ok(24));
        assert_eq!(u32::parse_prefix("32"), Ok(32));
        // Netmask text forms.
        assert_eq!(u32::parse_prefix("255.255.255.0"), Ok(24));
        assert_eq!(u32::parse_prefix("255.255.0.0"), Ok(16));
        assert_eq!(u32::parse_prefix("255.255.255.255"), Ok(32));
        assert_eq!(u32::parse_prefix("0.0.0.0"), Ok(0));
        // Rejections, exact C texts.
        assert_eq!(
            u32::parse_prefix("33"),
            Err("iprange: Invalid netmask 33".to_owned())
        );
        assert_eq!(
            u32::parse_prefix("999"),
            Err("iprange: Invalid netmask 999".to_owned())
        );
        assert_eq!(
            u32::parse_prefix("255.0.255.0"),
            Err("iprange: Invalid netmask 255.0.255.0".to_owned())
        );
        assert_eq!(
            u32::parse_prefix("256.255.255.0"),
            Err("iprange: Invalid address 256.255.255.0.".to_owned())
        );
        assert_eq!(
            u32::parse_prefix(""),
            Err("iprange: Invalid address .".to_owned())
        );
        assert_eq!(
            u32::parse_prefix("abc"),
            Err("iprange: Invalid address abc.".to_owned())
        );
    }

    #[test]
    fn fmt_addr_forms() {
        assert_eq!(u32::fmt_addr(0x0102_0304), "1.2.3.4");
        assert_eq!(u32::fmt_addr(0x0a00_0001), "10.0.0.1");
        assert_eq!(u32::fmt_addr(0), "0.0.0.0");
        assert_eq!(u32::fmt_addr(u32::MAX), "255.255.255.255");
        assert_eq!(u32::fmt_addr(0x7f00_0001), "127.0.0.1");
    }

    #[test]
    fn fmt_cidr_forms() {
        assert_eq!(u32::fmt_cidr(0x0102_0300, 24), "1.2.3.0/24");
        // Full-width prefix prints as a bare address (C print_addr rule).
        assert_eq!(u32::fmt_cidr(0x0102_0304, 32), "1.2.3.4");
        // /0 prints with the masked network address (0.0.0.0).
        assert_eq!(u32::fmt_cidr(0, 0), "0.0.0.0/0");
    }

    #[test]
    fn convert_foreign_mapped_forms() {
        // Mapped IPv6 with dotted quad, mixed-case f's, and the
        // shortened/octal spellings the C tail scan accepts.
        assert_eq!(
            u32::convert_foreign("::ffff:1.2.3.4"),
            Some(Range {
                lo: 0x0102_0304,
                hi: 0x0102_0304
            })
        );
        assert_eq!(
            u32::convert_foreign("::FFFF:1.2.3.4"),
            Some(Range {
                lo: 0x0102_0304,
                hi: 0x0102_0304
            })
        );
        assert_eq!(
            u32::convert_foreign("::ffff:1.2.3"),
            Some(Range {
                lo: 0x0102_0003,
                hi: 0x0102_0003
            })
        );
        assert_eq!(
            u32::convert_foreign("::ffff:1.2.3.4/24"),
            Some(Range {
                lo: 0x0102_0300,
                hi: 0x0102_03ff
            })
        );
        // Not converted (oracle drops these as IPv6).
        assert_eq!(u32::convert_foreign("::ffff:0102:0304"), None);
        assert_eq!(u32::convert_foreign("::ffff:1.2.3.4/33"), None);
        assert_eq!(u32::convert_foreign("::ffff:1.2.3.999"), None);
        assert_eq!(u32::convert_foreign("2001:db8::1"), None);
        assert_eq!(u32::convert_foreign("1.2.3.4"), None);
        assert_eq!(u32::convert_foreign("::ffff:"), None);
    }
}
