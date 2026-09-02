//! IPv6 family implementation of the legacy `iprange` surface.
//!
//! This port matches the released C oracle byte-for-byte (SOW-0028, legacy
//! contract: language-local adapter). Authority: `src/iprange6.h`
//! (`str2netaddr6`, `netmask6`, `broadcast6`, `is_ipv4_mapped`),
//! `src/ipset6_load.c` (`parse_address6` + `classify_address`),
//! `src/ipset6_print.c` (`ip6str_r` = glibc `inet_ntop`), and the glibc
//! implementations of `inet_pton`/`inet_ntop`/`inet_aton` that the C
//! binary links against (verified empirically against the oracle binary
//! and the installed libc, glibc 2.44).
//!
//! # Behavior pinned from the C oracle
//!
//! * `parse_addr` classifies the token exactly like C `classify_address`:
//!   a token containing `:` is parsed as IPv6 (`inet_pton`-equivalent);
//!   a token containing `.`, `/`, or only decimal digits is parsed as
//!   IPv4 with glibc `inet_aton` semantics and normalized to the mapped
//!   address `::ffff:A.B.C.D`; anything else is a hostname-class token
//!   and fails with C's `Cannot parse address: %s`. C's own loader
//!   routes hostname-class tokens to DNS before address parsing; the
//!   parse worker must do the same (see [`super::parse`]).
//! * Errors are exactly the C stderr texts:
//!   - `iprange: Invalid IPv6 address %s` (v6 parse failure)
//!   - `iprange: Invalid address %s.` (v4 parse failure, trailing dot)
//!   - `iprange: Cannot parse address: %s` (hostname-class token)
//!   - `iprange: Invalid IPv6 prefix /%s` (bad v6 prefix text)
//!   - `iprange: Invalid netmask %s` / `iprange: Invalid address %s.`
//!     (v4 prefix text that is not a decimal 0..32 falls back to the C
//!     netmask interpretation, exactly like `str2netaddr`)
//! * `fmt_addr` reproduces the glibc `inet_ntop` IPv6 algorithm,
//!   including its quirks: the first longest zero run (>= 2 words) is
//!   compressed, groups print lowercase hex without leading zeros, and a
//!   `::`-leading zero run of length 6 (or 5 with word 5 == `0xffff`)
//!   prints the remaining 32 bits as a decimal dotted quad. Hence mapped
//!   IPv4 prints `::ffff:A.B.C.D`, `::ffff:0:1` prints `::ffff:0.0.0.1`,
//!   and `::1234:5678` prints `::18.52.86.120`.
//! * `parse_prefix` replicates the full-string `strtol` semantics of
//!   `str2netaddr6`: leading ASCII whitespace and `+`/`-` are accepted,
//!   trailing junk and overflow are rejected, range is 0..=128; the
//!   `--default-prefix` option is a no-op in v6 mode (the C v6 main
//!   never reads it; probes confirm it does not affect v6 input).
//!
//! # Mixed-family routing rules for the parse worker
//!
//! In v6 mode C accepts every token; nothing is dropped, and bare IPv4
//! input is normalized to mapped IPv6. `convert_foreign` therefore
//! always returns `None`. The parse worker must classify tokens with
//! the same rule as `classify_address` and then:
//!
//! * v6-class tokens (contain `:`): use `parse_addr`/`parse_cidr` here.
//! * v4-class tokens *without* a prefix: `parse_addr` here normalizes
//!   them to the mapped single address; the result equals the C
//!   `::ffff:A.B.C.D` mapping (the C v4 default prefix 32 is a no-op on
//!   a single address).
//! * v4-class tokens *with* a prefix (`A.B.C.D/N`, netmask text, or a
//!   bare-digit prefix): C applies the prefix to the 32-bit value and
//!   maps the resulting range (`1.2.3.4/24` -> `::ffff:1.2.3.0/120`).
//!   The parse worker may either pass the full token (with the `/`) to
//!   `parse_cidr` here, which ports the whole C `str2netaddr` path
//!   including netmask-form prefixes, or route the token to the IPv4
//!   family implementation and map the range (OR the 32-bit range
//!   into `0xffff_u128 << 32`). Both produce identical, oracle-exact
//!   results; do not combine a stripped address part with a v6-wide
//!   prefix mask, because masking the mapped address at < 96 bits does
//!   not match C.
//! * mixed-family range endpoints (`1.2.3.4 - ::1`): C rejects the line
//!   with `iprange: Mixed-family range on line %d: %s - %s` when both
//!   endpoints are IP-class tokens of different classes; the parse
//!   worker must implement that check (`c1 != c2 && c1 != 0 && c2 != 0`).
//! * the loader token grammar only ever delivers prefix texts of pure
//!   `[0-9]` characters (`is_ipv6_char` stops at any other byte), so
//!   the `strtol` leniencies for whitespace/sign text are unreachable
//!   through files/stdin; they are still ported for fidelity.
//!
//! `parse_cidr` takes either the full token (then the embedded prefix
//! text wins, exactly like C) or the already-split address part (then
//! the `prefix` argument is used). The `default_prefix` argument is
//! accepted for trait uniformity but not consulted on the v6 side: C
//! hardcodes 128 for IPv6 text and 32 for IPv4 text in v6 mode, and
//! the `prefix` argument already carries the split value.

use super::family::{Family, FamilyImpl};
use super::range::Range;

/// The IPv4-mapped prefix `::ffff:0:0/96` (bits 32..48 set), as in
/// `IPV6_MAPPED_PREFIX` in `src/iprange6.h`.
const MAPPED_PREFIX: u128 = 0xffff_u128 << 32;

/// Token classes of the C `classify_address()` helper in
/// `src/ipset6_load.c`: `6` = IPv6 text, `4` = IPv4 text, `0` = DNS.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Class {
    V6,
    V4,
    Other,
}

/// C `classify_address`: `:` marks IPv6; `.`, `/` or all-digits marks
/// IPv4; everything else is a hostname-class token.
fn classify(token: &str) -> Class {
    if token.contains(':') {
        return Class::V6;
    }
    if token.contains('.') || token.contains('/') {
        return Class::V4;
    }
    let all_digits = !token.is_empty() && token.bytes().all(|b| b.is_ascii_digit());
    if all_digits {
        return Class::V4;
    }
    Class::Other
}

/// True when `addr` is an IPv4-mapped IPv6 address (`::ffff:0:0/96`):
/// Build the mapped IPv6 form of an IPv4 value (C `ipv4_to_mapped6`).
fn mapped_addr(v4: u32) -> u128 {
    MAPPED_PREFIX | v4 as u128
}

/// Split `token` at its first `/`, returning the address part and the
/// prefix text. Matches the `strchr(ipstr, '/')` split of the C
/// parsers; a token without `/` yields `None` for the prefix text.
fn split_token(token: &str) -> (&str, Option<&str>) {
    match token.find('/') {
        Some(pos) => (&token[..pos], Some(&token[pos + 1..])),
        None => (token, None),
    }
}

/// glibc `strtol(s, &end, 10)` full-string port: optional leading
/// C-locale whitespace, optional sign, base-10 digits, nothing else.
/// Returns `None` for empty, trailing junk, or overflow (the C
/// `errno || end == nptr || *end != '\0'` failure set).
fn strtol_decimal(text: &str) -> Option<i64> {
    let b = text.as_bytes();
    let mut i = 0;
    while i < b.len() && is_ascii_space(b[i]) {
        i += 1;
    }
    let mut negative = false;
    if i < b.len() && (b[i] == b'+' || b[i] == b'-') {
        negative = b[i] == b'-';
        i += 1;
    }
    let start = i;
    let mut value: i64 = 0;
    while i < b.len() && b[i].is_ascii_digit() {
        let digit = (b[i] - b'0') as i64;
        value = value.checked_mul(10).and_then(|v| v.checked_add(digit))?;
        i += 1;
    }
    if i == start {
        return None;
    }
    if i != b.len() {
        return None;
    }
    Some(if negative { -value } else { value })
}

/// C-locale `isspace` for the bytes glibc accepts (all ASCII).
fn is_ascii_space(b: u8) -> bool {
    matches!(b, b' ' | b'\t' | b'\n' | 0x0b | 0x0c | b'\r')
}

/// `hex_digit_value` of glibc's `inet_pton6`: 0-9, a-f, A-F, else -1.
fn hex_digit_value(b: u8) -> Option<u32> {
    match b {
        b'0'..=b'9' => Some((b - b'0') as u32),
        b'a'..=b'f' => Some((b - b'a' + 10) as u32),
        b'A'..=b'F' => Some((b - b'A' + 10) as u32),
        _ => None,
    }
}

/// glibc `inet_pton(AF_INET6, s, ...)` port (`resolv/inet_pton_length.c`
/// in glibc 2.44): strict RFC 4291 text with a single `::`, at most
/// 4 hex digits per group, optional strict dotted-quad tail, nothing
/// else. Returns the address as a big-endian numeric value.
fn inet_pton6(text: &str) -> Option<u128> {
    let b = text.as_bytes();
    let end = b.len();
    if end == 0 {
        return None;
    }
    let mut src = 0;
    // Leading single ':' is only valid as the start of '::'.
    if b[src] == b':' {
        src += 1;
        if src >= end || b[src] != b':' {
            return None;
        }
    }
    // glibc leaves src at the second ':'; the loop consumes it as the
    // '::' marker (colonp) below. curtok tracks the text after the last
    // ':' for a possible dotted-quad tail.
    let mut curtok = src;
    let mut out = [0u8; 16];
    let mut tp = 0usize; // bytes written so far
    let mut colonp: Option<usize> = None;
    let mut xdigits: u32 = 0; // hex digits in the current group
    let mut val: u32 = 0;
    while src < end {
        let ch = b[src];
        src += 1;
        if let Some(digit) = hex_digit_value(ch) {
            if xdigits == 4 {
                return None;
            }
            val = (val << 4) | digit;
            if val > 0xffff {
                return None;
            }
            xdigits += 1;
            continue;
        }
        if ch == b':' {
            curtok = src;
            if xdigits == 0 {
                // '::' marker: only one allowed.
                if colonp.is_some() {
                    return None;
                }
                colonp = Some(tp);
                continue;
            }
            // A trailing single ':' is invalid.
            if src >= end {
                return None;
            }
            if tp + 2 > 16 {
                return None;
            }
            out[tp] = (val >> 8) as u8;
            out[tp + 1] = val as u8;
            tp += 2;
            xdigits = 0;
            val = 0;
            continue;
        }
        if ch == b'.' && tp + 4 <= 16 {
            if let Some(v4) = inet_pton4(&b[curtok..end]) {
                out[tp..tp + 4].copy_from_slice(&v4);
                tp += 4;
                xdigits = 0;
                break; // the dotted quad consumed the rest of the string
            }
        }
        return None;
    }
    if xdigits > 0 {
        if tp + 2 > 16 {
            return None;
        }
        out[tp] = (val >> 8) as u8;
        out[tp + 1] = val as u8;
        tp += 2;
    }
    if let Some(cp) = colonp {
        // '::' would expand to a zero-width field.
        if tp == 16 {
            return None;
        }
        let n = tp - cp;
        out.copy_within(cp..cp + n, 16 - n);
        out[cp..16 - n].fill(0);
        tp = 16;
    }
    if tp != 16 {
        return None;
    }
    Some(u128::from_be_bytes(out))
}

/// glibc `inet_pton(AF_INET, ...)` port (strict dotted quad, no leading
/// zeros, exactly four octets <= 255, full-string consumption).
fn inet_pton4(b: &[u8]) -> Option<[u8; 4]> {
    let mut out = [0u8; 4];
    let mut idx = 0usize;
    let mut saw_digit = false;
    let mut octets = 0usize;
    for &ch in b {
        if ch.is_ascii_digit() {
            let cur = out[idx] as u32;
            let new = cur * 10 + (ch - b'0') as u32;
            if saw_digit && cur == 0 {
                return None;
            }
            if new > 255 {
                return None;
            }
            out[idx] = new as u8;
            if !saw_digit {
                octets += 1;
                if octets > 4 {
                    return None;
                }
                saw_digit = true;
            }
        } else if ch == b'.' && saw_digit {
            if octets == 4 {
                return None;
            }
            idx += 1;
            out[idx] = 0;
            saw_digit = false;
        } else {
            return None;
        }
    }
    if octets < 4 {
        return None;
    }
    Some(out)
}

/// glibc `inet_aton` port (`resolv/inet_addr.c`): C-style per-part
/// numbers (`0x` hex, leading `0` octal, else decimal), at most four
/// parts, non-final parts <= 255, final part bounded by part count
/// (0xffffffff / 0xffffff / 0xffff / 0xff), trailing ASCII whitespace
/// accepted. Returns the address in host order (A.B.C.D as
/// `A<<24 | B<<16 | C<<8 | D`), like `a_to_hl` after `ntohl`.
fn inet_aton(text: &str) -> Option<u32> {
    let b = text.as_bytes();
    let mut i = 0usize;
    // Bounds for the final part, indexed by the number of parts already
    // stored (glibc `max[4]`).
    const MAX_PART: [u32; 4] = [0xffff_ffff, 0x00ff_ffff, 0x0000_ffff, 0x0000_00ff];
    let mut parts = [0u32; 4];
    let mut nparts = 0usize;
    let mut val: u32;
    loop {
        // The glibc `digit` flag (`cp != endp`) is always true here:
        // the first character of every part must be a digit.
        if i >= b.len() || !b[i].is_ascii_digit() {
            return None;
        }
        let (v, next) = strtoul_auto(b, i)?;
        val = v;
        i = next;
        if i < b.len() && b[i] == b'.' {
            if nparts > 2 || val > 0xff {
                return None;
            }
            parts[nparts] = val;
            nparts += 1;
            i += 1;
        } else {
            break;
        }
    }
    // Trailing characters: only C-locale ASCII whitespace is allowed.
    if i < b.len() && !is_ascii_space(b[i]) {
        return None;
    }
    if val > MAX_PART[nparts] {
        return None;
    }
    let mut addr = 0u32;
    for (k, &p) in parts[..nparts].iter().enumerate() {
        addr |= p << (24 - 8 * k as u32);
    }
    addr |= val;
    Some(addr)
}

/// glibc `strtoul(s, &end, 0)` on one `inet_aton` part: `0x`/`0X` hex,
/// leading `0` octal, else decimal; parses as many digits as the base
/// allows and stops at the first invalid character. Fails when the
/// value does not fit 32 bits, which is exactly glibc's
/// `ul > 0xffffffff` (and ERANGE) rejection.
fn strtoul_auto(b: &[u8], i: usize) -> Option<(u32, usize)> {
    let (base, mut start) =
        if b[i] == b'0' && i + 1 < b.len() && (b[i + 1] == b'x' || b[i + 1] == b'X') {
            (16, i + 2)
        } else if b[i] == b'0' {
            (8, i + 1)
        } else {
            (10, i)
        };
    let mut val: u32 = 0;
    let mut consumed = 0usize;
    while start < b.len() {
        let d = match base {
            16 => hex_digit_value(b[start]),
            8 => {
                if (b'0'..=b'7').contains(&b[start]) {
                    Some((b[start] - b'0') as u32)
                } else {
                    None
                }
            }
            _ => {
                if b[start].is_ascii_digit() {
                    Some((b[start] - b'0') as u32)
                } else {
                    None
                }
            }
        };
        match d {
            Some(d) => {
                if base == 16 {
                    val = val.checked_mul(16).and_then(|v| v.checked_add(d))?;
                } else {
                    val = val.checked_mul(base).and_then(|v| v.checked_add(d))?;
                }
                start += 1;
                consumed += 1;
            }
            None => break,
        }
    }
    if consumed == 0 {
        // glibc consumed only the leading '0' (e.g. "0x" or "08").
        // Callers reject the remaining junk later; the digit flag is
        // set by the caller because the first byte was a digit.
        return Some((0, i + 1));
    }
    Some((val, start))
}

/// 128-bit netmask of `prefix` (C `netmask6` in `src/iprange6.h`).
fn netmask6(prefix: u32) -> u128 {
    match prefix {
        0 => 0,
        128 => u128::MAX,
        p => u128::MAX << (128 - p),
    }
}

/// The C `str2netaddr6` range: `[network addr, broadcast]` over the
/// full 128-bit space; `fix_network == false` (C `--dont-fix-network`)
/// keeps the raw host start with the prefix broadcast end.
fn range_v6(addr: u128, prefix: u32, fix_network: bool) -> Range<u128> {
    let mask = netmask6(prefix);
    let lo = if fix_network { addr & mask } else { addr };
    Range { lo, hi: lo | !mask }
}

/// The IPv4 prefix of the v4-in-v6 path: C `str2netaddr` semantics.
/// A decimal text in 0..=32 wins; anything else is interpreted as a
/// netmask (inverted dotted quad whose trailing-one count becomes the
/// prefix), and fails with the exact C messages.
fn v4_prefix_from_text(text: &str) -> Result<u32, String> {
    if let Some(v) = strtol_decimal(text) {
        if (0..=32).contains(&v) {
            return Ok(v as u32);
        }
    }
    // Netmask form: ~inet_aton(text), count trailing ones from 32.
    let mask_value =
        inet_aton(text).ok_or_else(|| format!("iprange: Invalid address {}.", text))?;
    let mut mask = !mask_value;
    let mut prefix = 32i32;
    while mask & 1 == 1 {
        mask >>= 1;
        prefix -= 1;
    }
    if mask != 0 {
        return Err(format!("iprange: Invalid netmask {}", text));
    }
    Ok(prefix as u32)
}

impl FamilyImpl for u128 {
    const FAMILY: Family = Family::V6;

    fn parse_addr(token: &str) -> Result<Self, String> {
        // C classify_address runs on the full token before the '/'
        // split; split only afterwards for the address parse.
        let (addr_text, prefix_text) = split_token(token);
        let class = classify(token);
        let _ = prefix_text; // prefixes belong to parse_cidr
        match class {
            Class::V6 => inet_pton6(addr_text)
                .ok_or_else(|| format!("iprange: Invalid IPv6 address {}", addr_text)),
            Class::V4 => inet_aton(addr_text)
                .map(mapped_addr)
                .ok_or_else(|| format!("iprange: Invalid address {}.", addr_text)),
            Class::Other => Err(format!("iprange: Cannot parse address: {}", token)),
        }
    }

    fn parse_cidr(
        token: &str,
        prefix: u32,
        fix_network: bool,
        _default_prefix: u32,
    ) -> Result<Range<Self>, String> {
        // C classifies the whole token, then splits at the first '/'.
        let (addr_text, prefix_text) = split_token(token);
        let class = classify(token);
        match class {
            Class::V6 => {
                // str2netaddr6: strict strtol prefix, no netmask form.
                let prefix = match prefix_text {
                    Some(text) => match strtol_decimal(text) {
                        Some(v) if (0..=128).contains(&v) => v as u32,
                        _ => return Err(format!("iprange: Invalid IPv6 prefix /{}", text)),
                    },
                    // The parameterized contract channel: the parse
                    // worker normally splits the token first.
                    None => {
                        if prefix > 128 {
                            return Err(format!("iprange: Invalid IPv6 prefix /{}", prefix));
                        }
                        prefix
                    }
                };
                let addr = inet_pton6(addr_text)
                    .ok_or_else(|| format!("iprange: Invalid IPv6 address {}", addr_text))?;
                Ok(range_v6(addr, prefix, fix_network))
            }
            Class::V4 => {
                // str2netaddr: decimal 0..=32 or netmask-form prefix.
                let prefix = match prefix_text {
                    Some(text) => v4_prefix_from_text(text)?,
                    // A bare v4 token in v6 mode uses C's fixed IPv4
                    // default (32): --default-prefix does not apply in
                    // v6 mode. Tolerate the v6 family default
                    // (128) as "no prefix" for callers that pass the
                    // trait default for a prefix-less token.
                    None => {
                        if prefix == 128 || prefix == 32 {
                            32
                        } else if prefix <= 32 {
                            prefix
                        } else {
                            return Err(format!("iprange: Invalid netmask {}", prefix));
                        }
                    }
                };
                let v4 = inet_aton(addr_text)
                    .ok_or_else(|| format!("iprange: Invalid address {}.", addr_text))?;
                let mask = if prefix == 0 {
                    0
                } else if prefix >= 32 {
                    u32::MAX
                } else {
                    u32::MAX << (32 - prefix)
                };
                let lo = if fix_network { v4 & mask } else { v4 };
                Ok(Range {
                    lo: mapped_addr(lo),
                    hi: mapped_addr(lo | !mask),
                })
            }
            Class::Other => Err(format!("iprange: Cannot parse address: {}", token)),
        }
    }

    fn parse_prefix(text: &str) -> Result<u32, String> {
        // str2netaddr6 prefix validation, exactly: strtol base 10 over
        // the full string, range 0..=128; the error text keeps the
        // original prefix text after the slash.
        match strtol_decimal(text) {
            Some(v) if (0..=128).contains(&v) => Ok(v as u32),
            _ => Err(format!("iprange: Invalid IPv6 prefix /{}", text)),
        }
    }

    fn fmt_addr(addr: Self) -> String {
        inet_ntop6(addr)
    }

    fn fmt_cidr(addr: Self, prefix: u32) -> String {
        if prefix >= 128 {
            inet_ntop6(addr)
        } else {
            format!("{}/{}", inet_ntop6(addr), prefix)
        }
    }

    fn convert_foreign(_token: &str) -> Option<Range<Self>> {
        // In v6 mode the C oracle accepts every token class: IPv6 text,
        // IPv4 text (normalized to mapped), and hostnames (DNS). There
        // is no foreign-family conversion, so this is always None. The
        // IPv4 family implementation owns the reverse direction
        // (mapped IPv6 back to IPv4 in v4 mode).
        None
    }
}

/// glibc `inet_ntop(AF_INET6, ...)` port (`resolv/inet_ntop.c`,
/// `inet_ntop6_format`): first longest zero run of >= 2 words is
/// compressed (ties: the first), groups are lowercase hex without
/// leading zeros, and a `::`-leading zero run of length 6, or of
/// length 5 with word 5 == 0xffff, prints the low 32 bits as a decimal
/// dotted quad.
fn inet_ntop6(addr: u128) -> String {
    let bytes = addr.to_be_bytes();
    let mut words = [0u16; 8];
    for (i, w) in words.iter_mut().enumerate() {
        *w = u16::from_be_bytes([bytes[2 * i], bytes[2 * i + 1]]);
    }

    // Find the first longest run of zero words (length >= 2).
    let mut best_base: i32 = -1;
    let mut best_len = 0u32;
    let mut cur_base: i32 = -1;
    let mut cur_len = 0u32;
    for (i, &w) in words.iter().enumerate() {
        if w == 0 {
            if cur_base == -1 {
                cur_base = i as i32;
                cur_len = 1;
            } else {
                cur_len += 1;
            }
        } else if cur_base != -1 {
            if best_len == 0 || cur_len > best_len {
                best_base = cur_base;
                best_len = cur_len;
            }
            cur_base = -1;
        }
    }
    if cur_base != -1 && (best_len == 0 || cur_len > best_len) {
        best_base = cur_base;
        best_len = cur_len;
    }
    if best_len < 2 {
        best_base = -1;
        best_len = 0;
    }

    let mut out = String::with_capacity(46);
    for (i, &w) in words.iter().enumerate() {
        if best_base != -1 && i as i32 >= best_base && (i as i32) < (best_base + best_len as i32) {
            if i as i32 == best_base {
                out.push(':');
            }
            continue;
        }
        if i != 0 {
            out.push(':');
        }
        if i == 6 && best_base == 0 && (best_len == 6 || (best_len == 5 && words[5] == 0xffff)) {
            out.push_str(&format!(
                "{}.{}.{}.{}",
                bytes[12], bytes[13], bytes[14], bytes[15]
            ));
            break;
        }
        out.push_str(&format!("{:x}", w));
    }
    if best_base != -1 && (best_base as u32 + best_len) == 8 {
        out.push(':');
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn range(lo: u128, hi: u128) -> Range<u128> {
        Range { lo, hi }
    }

    #[test]
    fn parse_addr_full_and_compressed() {
        assert_eq!(u128::parse_addr("::"), Ok(0));
        assert_eq!(u128::parse_addr("::1"), Ok(1));
        assert_eq!(u128::parse_addr("0:0:0:0:0:0:0:1"), Ok(1));
        assert_eq!(
            u128::parse_addr("2001:db8::1"),
            Ok(0x2001_0db8_0000_0000_0000_0000_0000_0001)
        );
        assert_eq!(
            u128::parse_addr("2001:db8:0:0:0:0:0:1"),
            Ok(0x2001_0db8_0000_0000_0000_0000_0000_0001)
        );
        assert_eq!(
            u128::parse_addr("ABCD::1"),
            Ok(0xabcd_0000_0000_0000_0000_0000_0000_0001)
        );
        assert_eq!(
            u128::parse_addr("1::"),
            Ok(0x0001_0000_0000_0000_0000_0000_0000_0000)
        );
        assert_eq!(
            u128::parse_addr("1:2:3:4:5:6:7:8"),
            Ok(0x0001_0002_0003_0004_0005_0006_0007_0008)
        );
    }

    #[test]
    fn parse_addr_embedded_v4_tail() {
        // ::ffff:1.2.3.4
        assert_eq!(
            u128::parse_addr("::ffff:1.2.3.4"),
            Ok(0x0000_0000_0000_0000_0000_ffff_0102_0304)
        );
        // Words for the mapped 1.2.3.4 = 0xffff00000000 | 0x01020304.
        assert_eq!(
            u128::parse_addr("::ffff:1.2.3.4"),
            Ok(0xffff_0000_0000u128 | 0x0102_0304)
        );
        // IPv4-compatible tail is accepted too.
        assert_eq!(
            u128::parse_addr("::1.2.3.4"),
            Ok(0x0000_0000_0000_0000_0000_0000_0102_0304)
        );
        // Hex groups parse the same as the dotted tail.
        assert_eq!(
            u128::parse_addr("::ffff:0102:0304"),
            u128::parse_addr("::ffff:1.2.3.4")
        );
        // 7 groups + tail is a valid full address.
        assert!(u128::parse_addr("1:2:3:4:5:6:1.2.3.4").is_ok());
        // Five-octet tail is rejected.
        assert_eq!(
            u128::parse_addr("::ffff:1.2.3.4.5"),
            Err("iprange: Invalid IPv6 address ::ffff:1.2.3.4.5".to_owned())
        );
    }

    #[test]
    fn parse_addr_bare_v4_normalizes_to_mapped() {
        assert_eq!(
            u128::parse_addr("1.2.3.4"),
            u128::parse_addr("::ffff:1.2.3.4")
        );
        // inet_aton partial forms (a.b.c -> a.b.0.c etc.).
        assert_eq!(
            u128::parse_addr("1.2.3"),
            u128::parse_addr("::ffff:1.2.0.3")
        );
        assert_eq!(u128::parse_addr("1.2"), u128::parse_addr("::ffff:1.0.0.2"));
        assert_eq!(u128::parse_addr("1"), u128::parse_addr("::ffff:0.0.0.1"));
        assert_eq!(
            u128::parse_addr("127.1"),
            u128::parse_addr("::ffff:127.0.0.1")
        );
        // Octal leading zero and large single part.
        assert_eq!(
            u128::parse_addr("010.1.1.1"),
            u128::parse_addr("::ffff:8.1.1.1")
        );
        assert_eq!(
            u128::parse_addr("4294967295"),
            u128::parse_addr("::ffff:255.255.255.255")
        );
        // Trailing whitespace is accepted by inet_aton (C behavior).
        assert_eq!(
            u128::parse_addr("1.2.3.4 "),
            u128::parse_addr("::ffff:1.2.3.4")
        );
    }

    #[test]
    fn parse_addr_errors_match_c() {
        assert_eq!(
            u128::parse_addr("::garbage"),
            Err("iprange: Invalid IPv6 address ::garbage".to_owned())
        );
        assert_eq!(
            u128::parse_addr("102:304"),
            Err("iprange: Invalid IPv6 address 102:304".to_owned())
        );
        assert_eq!(
            u128::parse_addr("1.2.3.99999"),
            Err("iprange: Invalid address 1.2.3.99999.".to_owned())
        );
        assert_eq!(
            u128::parse_addr("256.1.1.1"),
            Err("iprange: Invalid address 256.1.1.1.".to_owned())
        );
        assert_eq!(
            u128::parse_addr("4294967296"),
            Err("iprange: Invalid address 4294967296.".to_owned())
        );
        assert_eq!(
            u128::parse_addr("abcdef"),
            Err("iprange: Cannot parse address: abcdef".to_owned())
        );
        assert_eq!(
            u128::parse_addr(""),
            Err("iprange: Cannot parse address: ".to_owned())
        );
    }

    #[test]
    fn parse_cidr_ranges_match_c() {
        // /128 keeps a single address.
        assert_eq!(u128::parse_cidr("::1", 128, true, 128), Ok(range(1, 1)));
        // /0 with fix-network is the full universe.
        assert_eq!(
            u128::parse_cidr("::1", 0, true, 128),
            Ok(range(0, u128::MAX))
        );
        // /0 without fix-network keeps the raw start.
        assert_eq!(
            u128::parse_cidr("::1", 0, false, 128),
            Ok(range(1, u128::MAX))
        );
        // /64 fix vs dont-fix (C probe: 2001:db8::7/64).
        assert_eq!(
            u128::parse_cidr("2001:db8::7", 64, true, 128),
            Ok(range(
                0x2001_0db8_0000_0000_0000_0000_0000_0000,
                0x2001_0db8_0000_0000_ffff_ffff_ffff_ffff
            ))
        );
        assert_eq!(
            u128::parse_cidr("2001:db8::7", 64, false, 128),
            Ok(range(
                0x2001_0db8_0000_0000_0000_0000_0000_0007,
                0x2001_0db8_0000_0000_ffff_ffff_ffff_ffff
            ))
        );
        // Mapped input under a v6-class token masks the FULL 128 bits
        // (C probe: ::ffff:1.2.3.7/24 fix -> ::/24).
        assert_eq!(
            u128::parse_cidr("::ffff:1.2.3.7", 24, true, 128),
            Ok(range(0, u128::MAX >> 24))
        );
        assert_eq!(
            u128::parse_cidr("::ffff:1.2.3.7", 24, false, 128),
            Ok(range(
                0x0000_0000_0000_0000_0000_ffff_0102_0307,
                u128::MAX >> 24
            ))
        );
        // Full-token form with an embedded prefix (C str2netaddr6 split).
        assert_eq!(
            u128::parse_cidr("2001:db8::7/64", 128, true, 128),
            u128::parse_cidr("2001:db8::7", 64, true, 128)
        );
    }

    #[test]
    fn parse_cidr_v4_class_uses_32bit_prefix() {
        // C probe: 1.2.3.4/24 -> ::ffff:1.2.3.0-::ffff:1.2.3.255.
        assert_eq!(
            u128::parse_cidr("1.2.3.4", 24, true, 128),
            Ok(range(
                0x0000_0000_0000_0000_0000_ffff_0102_0300,
                0x0000_0000_0000_0000_0000_ffff_0102_03ff
            ))
        );
        // dont-fix keeps the raw host start (C probe: 1.2.3.7/24).
        assert_eq!(
            u128::parse_cidr("1.2.3.7", 24, false, 128),
            Ok(range(
                0x0000_0000_0000_0000_0000_ffff_0102_0307,
                0x0000_0000_0000_0000_0000_ffff_0102_03ff
            ))
        );
        // /0 over the v4-class token is the mapped IPv4 space.
        assert_eq!(
            u128::parse_cidr("1.2.3.4", 0, true, 128),
            Ok(range(
                0x0000_0000_0000_0000_0000_ffff_0000_0000,
                0x0000_0000_0000_0000_0000_ffff_ffff_ffff
            ))
        );
        // Full-token netmask form (C probe: 1.2.3.4/255.255.255.0).
        assert_eq!(
            u128::parse_cidr("1.2.3.4/255.255.255.0", 24, true, 128),
            u128::parse_cidr("1.2.3.4", 24, true, 128)
        );
        // Out-of-range numeric prefix falls to the C netmask error.
        assert_eq!(
            u128::parse_cidr("1.2.3.4", 64, true, 128),
            Err("iprange: Invalid netmask 64".to_owned())
        );
        assert_eq!(
            u128::parse_cidr("1.2.3.4/255.255.255.1", 24, true, 128),
            Err("iprange: Invalid netmask 255.255.255.1".to_owned())
        );
    }

    #[test]
    fn parse_cidr_errors_match_c() {
        assert_eq!(
            u128::parse_cidr("::1", 129, true, 128),
            Err("iprange: Invalid IPv6 prefix /129".to_owned())
        );
        assert_eq!(
            u128::parse_cidr("::1/129", 129, true, 128),
            Err("iprange: Invalid IPv6 prefix /129".to_owned())
        );
        assert_eq!(
            u128::parse_cidr("::1/xyz", 129, true, 128),
            Err("iprange: Invalid IPv6 prefix /xyz".to_owned())
        );
        assert_eq!(
            u128::parse_cidr("1.2.3.4/64", 64, true, 128),
            Err("iprange: Invalid netmask 64".to_owned())
        );
        assert_eq!(
            u128::parse_addr("::1/24"),
            Ok(1),
            "parse_addr ignores an embedded prefix (address part only)"
        );
    }

    #[test]
    fn parse_prefix_matches_strtol() {
        assert_eq!(u128::parse_prefix("128"), Ok(128));
        assert_eq!(u128::parse_prefix("0"), Ok(0));
        assert_eq!(u128::parse_prefix("24"), Ok(24));
        assert_eq!(u128::parse_prefix("012"), Ok(12), "base 10, not octal");
        assert_eq!(u128::parse_prefix(" 24"), Ok(24), "strtol skips spaces");
        assert_eq!(u128::parse_prefix("+24"), Ok(24), "strtol accepts +");
        assert_eq!(u128::parse_prefix("-0"), Ok(0), "strtol accepts -0");
        for bad in [
            "",
            "129",
            "-1",
            "24x",
            "24 ",
            "0x10",
            "abc",
            "9999999999999999999999",
        ] {
            assert_eq!(
                u128::parse_prefix(bad),
                Err(format!("iprange: Invalid IPv6 prefix /{}", bad)),
                "prefix {:?}",
                bad
            );
        }
    }

    #[test]
    fn fmt_addr_matches_glibc_inet_ntop() {
        assert_eq!(u128::fmt_addr(0), "::");
        assert_eq!(u128::fmt_addr(1), "::1");
        assert_eq!(
            u128::fmt_addr(0x2001_0db8_0000_0000_0000_0000_0000_0001),
            "2001:db8::1"
        );
        // Mapped dotted-quad form (probed oracle outputs).
        assert_eq!(
            u128::fmt_addr(0x0000_0000_0000_0000_0000_ffff_0102_0304),
            "::ffff:1.2.3.4"
        );
        assert_eq!(
            u128::fmt_addr(0x0000_0000_0000_0000_0000_ffff_0000_0001),
            "::ffff:0.0.0.1"
        );
        assert_eq!(
            u128::fmt_addr(0x0000_0000_0000_0000_0000_ffff_ffff_ffff),
            "::ffff:255.255.255.255"
        );
        // glibc quirks: 6-word zero run prints the tail as dotted quad.
        assert_eq!(
            u128::fmt_addr(0x0000_0000_0000_0000_0000_0000_1234_5678),
            "::18.52.86.120"
        );
        // First longest zero run wins ties.
        assert_eq!(
            u128::fmt_addr(0x0001_0000_0000_0002_0000_0000_0003_0004),
            "1::2:0:0:3:4"
        );
        assert_eq!(
            u128::fmt_addr(0x2001_0db8_0000_0000_0001_0000_0000_0001),
            "2001:db8::1:0:0:1"
        );
        // Trailing run compresses with a trailing colon.
        assert_eq!(
            u128::fmt_addr(0x0001_0000_0000_0000_0000_0000_0000_0000),
            "1::"
        );
        assert_eq!(
            u128::fmt_addr(0x2001_0db8_0000_0000_0000_0000_0000_0000),
            "2001:db8::"
        );
        assert_eq!(
            u128::fmt_addr(0x0001_0002_0003_0004_0005_0006_0007_0000),
            "1:2:3:4:5:6:7:0"
        );
        // No zero run: no compression.
        assert_eq!(
            u128::fmt_addr(0x0001_0002_0003_0004_0005_0006_0007_0008),
            "1:2:3:4:5:6:7:8"
        );
        // Lowercase hex.
        assert_eq!(
            u128::fmt_addr(0xabcd_0000_0000_0000_0000_0000_0000_0001),
            "abcd::1"
        );
    }

    #[test]
    fn fmt_cidr_matches_c_print_addr6() {
        assert_eq!(u128::fmt_cidr(0, 0), "::/0");
        assert_eq!(u128::fmt_cidr(1, 128), "::1");
        assert_eq!(
            u128::fmt_cidr(0x2001_0db8_0000_0000_0000_0000_0000_0000, 64),
            "2001:db8::/64"
        );
        assert_eq!(
            u128::fmt_cidr(0x0000_0000_0000_0000_0000_ffff_0102_0300, 120),
            "::ffff:1.2.3.0/120"
        );
    }

    #[test]
    fn convert_foreign_is_always_none_in_v6_mode() {
        for token in ["1.2.3.4", "::1", "::ffff:1.2.3.4", "hostname.example"] {
            assert_eq!(u128::convert_foreign(token), None, "token {:?}", token);
        }
    }
}
