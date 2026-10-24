//! C-compatible record splitting, classification, and address parsing.
//! Split raw input with [`Records`], then truncate each record with [`cstr`]
//! before classifying. Classification does not decide caller error policy.

use crate::{FamilyImpl, Range};

/// C `MAX_LINE` (fgets buffer): one line record is at most 1023
/// bytes plus the trailing newline slot.
pub const MAX_LINE: usize = 1024;

/// C `MAX_INPUT_ELEMENT` (IPv4 token buffer).
const MAX_TOKEN: usize = 255;
/// C `MAX_INPUT_ELEMENT6` (IPv6 token buffer).
const MAX_TOKEN6: usize = 256;

/// Parse one address token (`ADDR`, `ADDR/PREFIX`) with the family
/// parse policy. `prefix` is the family default applied when the
/// token carries no prefix (v4 `--default-prefix`, v6 always 128);
/// `fix_network` mirrors C `cidr_use_network` (--dont-fix-network).
///
/// The caller must validate `default_prefix` is at most 32 for IPv4.
/// IPv6 ignores this argument and uses 128. An invalid IPv4 default may panic.
pub fn parse_token<F: FamilyImpl>(
    token: &str,
    default_prefix: u32,
    fix_network: bool,
) -> Result<Range<F>, String> {
    let prefix = F::default_prefix(default_prefix);
    F::parse_cidr(token, prefix, fix_network, prefix)
}

/// Count `:` bytes (C `strchr` loop detects a second colon).
pub fn colons(rec: &[u8]) -> usize {
    rec.iter().filter(|&&b| b == b':').count()
}

// ---------------------------------------------------------------------------
// Line grammar (exact C parse_line / parse_line6 ports)
// ---------------------------------------------------------------------------

/// A parsed line. `WarnedRange` carries the C classification-time
/// warning text; the first IP of the broken range is still added.
#[derive(Debug, PartialEq)]
pub enum LineOutcome {
    Empty,
    OneIp(String),
    TwoIps(String, String),
    Hostname(String),
    WarnedRange { first: String, warning: String },
    Invalid,
}

fn is_hostname_char(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_' || b == b'-' || b == b'.'
}

fn is_v4_token_char(b: u8) -> bool {
    b.is_ascii_digit() || b == b'.' || b == b'/'
}

fn is_v6_token_char(b: u8) -> bool {
    b.is_ascii_hexdigit() || b == b':' || b == b'.' || b == b'/'
}

/// Skip only the spaces and tabs accepted by the C grammar.
pub fn skip_ws(mut s: &[u8]) -> &[u8] {
    loop {
        match s.split_first() {
            Some((b' ', rest)) | Some((b'\t', rest)) => s = rest,
            _ => return s,
        }
    }
}

/// Scan with the C token-buffer cap (ipstr[MAX_INPUT_ELEMENT]).
fn scan<const N: usize>(s: &[u8], mut pred: impl FnMut(u8) -> bool) -> (&[u8], &[u8]) {
    let n = s.len().min(N);
    let mut i = 0usize;
    while i < n && pred(s[i]) {
        i += 1;
    }
    (&s[..i], &s[i..])
}

fn ascii_lossy(b: &[u8]) -> String {
    String::from_utf8_lossy(b).into_owned()
}

/// C token_looks_ip_like(): has a dot or a slash.
fn token_looks_ip_like(t: &[u8]) -> bool {
    t.contains(&b'.') || t.contains(&b'/')
}

/// C token_is_complete_ipv4_candidate().
fn token_is_complete_v4(t: &[u8]) -> bool {
    if t.contains(&b'/') {
        return true;
    }
    let mut dots = 0usize;
    let mut digits = 0usize;
    for &b in t {
        if b.is_ascii_digit() {
            digits += 1;
            continue;
        }
        if b == b'.' && digits > 0 {
            dots += 1;
            digits = 0;
            continue;
        }
        return false;
    }
    dots == 3 && digits > 0
}

/// C line_is_hostname_candidate(): whole line is hostname chars plus
/// trailing whitespace and a comment or end of line.
fn line_is_hostname_candidate(line: &[u8]) -> bool {
    let mut s = skip_ws(line);
    let mut has = false;
    while let Some(&b) = s.first() {
        if is_hostname_char(b) {
            has = true;
            s = &s[1..];
        } else {
            break;
        }
    }
    if !has {
        return false;
    }
    matches!(
        skip_ws(s).first(),
        None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
    )
}

/// C classify_address() used by the IPv6 loader: 6 = has ':',
/// 4 = dotted/slashed/all-digits, 0 = hostname-like.
pub fn classify_token(t: &[u8]) -> u8 {
    if t.contains(&b':') {
        return 6;
    }
    if t.contains(&b'.') || t.contains(&b'/') {
        return 4;
    }
    if !t.is_empty() && t.iter().all(u8::is_ascii_digit) {
        return 4;
    }
    0
}

/// C parse_line() (ipset_load.c); lineid feeds the two warnings that
/// the parser prints while classifying.
pub fn classify_v4(rec: &[u8], lineid: usize) -> LineOutcome {
    let s = skip_ws(rec);
    match s.first() {
        Some(b'#') | Some(b';') => return LineOutcome::Empty,
        Some(b'\r') | Some(b'\n') => return LineOutcome::Empty,
        None => return LineOutcome::Empty,
        _ => {}
    }

    let (tok, rest0) = scan::<MAX_TOKEN>(s, is_v4_token_char);
    if tok.is_empty() {
        return hostname_v4(rec);
    }

    let hostname_candidate = line_is_hostname_candidate(rec);
    let rest = skip_ws(rest0);
    match rest.first() {
        Some(b'#') | Some(b';') => return LineOutcome::OneIp(ascii_lossy(tok)),
        None | Some(b'\r') | Some(b'\n') => return LineOutcome::OneIp(ascii_lossy(tok)),
        _ => {}
    }

    if rest[0] != b'-' {
        if tok.contains(&b'/') {
            return LineOutcome::Invalid;
        }
        if token_looks_ip_like(tok) && token_is_complete_v4(tok) {
            return LineOutcome::Invalid;
        }
        if hostname_candidate {
            return hostname_v4(rec);
        }
        return LineOutcome::Invalid;
    }

    let after = skip_ws(&rest[1..]);
    match after.first() {
        Some(b'#') | Some(b';') => {
            let found = ascii_lossy(after);
            return LineOutcome::WarnedRange {
                first: ascii_lossy(tok),
                warning: fmt_ignore_text(lineid, &found),
            };
        }
        None | Some(b'\r') | Some(b'\n') => {
            return LineOutcome::WarnedRange {
                first: ascii_lossy(tok),
                warning: fmt_incomplete_v4(lineid),
            };
        }
        _ => {}
    }

    let (tok2, rest2) = scan::<MAX_TOKEN>(after, is_v4_token_char);
    if tok2.is_empty() {
        if !tok.contains(&b'/') && !token_is_complete_v4(tok) && hostname_candidate {
            return hostname_v4(rec);
        }
        return LineOutcome::Invalid;
    }
    let rest2 = skip_ws(rest2);
    if matches!(
        rest2.first(),
        None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
    ) {
        return LineOutcome::TwoIps(ascii_lossy(tok), ascii_lossy(tok2));
    }
    if !tok.contains(&b'/') && !token_is_complete_v4(tok) && hostname_candidate {
        return hostname_v4(rec);
    }
    LineOutcome::Invalid
}

/// C parse_hostname() (IPv4 form): hostname chars, then whitespace,
/// then a comment or end of line.
fn hostname_v4(rec: &[u8]) -> LineOutcome {
    let (h, rest) = scan::<MAX_TOKEN>(skip_ws(rec), is_hostname_char);
    if h.is_empty() {
        return LineOutcome::Invalid;
    }
    if matches!(
        skip_ws(rest).first(),
        None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
    ) {
        LineOutcome::Hostname(ascii_lossy(h))
    } else {
        LineOutcome::Invalid
    }
}

/// C parse_line6() (ipset6_load.c). The "incomplete range" warning
/// carries no line number and has a single text for comments and
/// end-of-line alike.
pub fn classify_v6(rec: &[u8]) -> LineOutcome {
    let s = skip_ws(rec);
    match s.first() {
        Some(b'#') | Some(b';') => return LineOutcome::Empty,
        Some(b'\r') | Some(b'\n') => return LineOutcome::Empty,
        None => return LineOutcome::Empty,
        _ => {}
    }

    let (tok, rest0) = scan::<MAX_TOKEN6>(s, is_v6_token_char);
    if tok.is_empty() {
        // Direct hostname path (C scans from the line start).
        let (h, rest) = scan::<MAX_TOKEN6>(skip_ws(rec), is_hostname_char);
        if h.is_empty() {
            return LineOutcome::Invalid;
        }
        if matches!(
            skip_ws(rest).first(),
            None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
        ) {
            return LineOutcome::Hostname(ascii_lossy(h));
        }
        return LineOutcome::Invalid;
    }

    let has_colon = tok.contains(&b':');
    let rest = skip_ws(rest0);
    if matches!(
        rest.first(),
        None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
    ) {
        return LineOutcome::OneIp(ascii_lossy(tok));
    }

    if rest[0] != b'-' {
        // The token is not an address of any family: retry as a
        // hostname from the start of the line (C behavior).
        if !has_colon && classify_token(tok) == 0 {
            let (h, rest) = scan::<MAX_TOKEN6>(skip_ws(rec), is_hostname_char);
            if !h.is_empty()
                && matches!(
                    skip_ws(rest).first(),
                    None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
                )
            {
                return LineOutcome::Hostname(ascii_lossy(h));
            }
        }
        return LineOutcome::Invalid;
    }

    let after = skip_ws(&rest[1..]);
    if matches!(
        after.first(),
        None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
    ) {
        return LineOutcome::WarnedRange {
            first: ascii_lossy(tok),
            warning: fmt_incomplete_v6(),
        };
    }

    let (tok2, rest2) = scan::<MAX_TOKEN6>(after, is_v6_token_char);
    if tok2.is_empty() {
        return LineOutcome::Invalid;
    }
    if matches!(
        skip_ws(rest2).first(),
        None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
    ) {
        return LineOutcome::TwoIps(ascii_lossy(tok), ascii_lossy(tok2));
    }
    LineOutcome::Invalid
}

/// C fgets(line, MAX_LINE, fp) record iteration: each record is at
/// most 1023 bytes and includes the newline when one appears within
/// that window; long physical lines continue as further records.
pub struct Records<'a> {
    data: &'a [u8],
    pos: usize,
}

impl<'a> Records<'a> {
    /// Split raw bytes before applying per-record C-string truncation.
    pub fn new(data: &'a [u8]) -> Self {
        Records { data, pos: 0 }
    }
}

impl<'a> Iterator for Records<'a> {
    type Item = &'a [u8];

    fn next(&mut self) -> Option<&'a [u8]> {
        if self.pos >= self.data.len() {
            return None;
        }
        let start = self.pos;
        let end = (start + MAX_LINE - 1).min(self.data.len());
        match self.data[start..end].iter().position(|&b| b == b'\n') {
            Some(k) => {
                self.pos = start + k + 1;
                Some(&self.data[start..start + k + 1])
            }
            None => {
                self.pos = end;
                Some(&self.data[start..end])
            }
        }
    }
}

fn fmt_ignore_text(lineid: usize, found: &str) -> String {
    format!(
        "iprange: Ignoring text on line {lineid}, expected an ip address after -, but found '{found}'"
    )
}

fn fmt_incomplete_v4(lineid: usize) -> String {
    format!(
        "iprange: Incomplete range on line {lineid}, expected an ip address after -, but line ended"
    )
}

fn fmt_incomplete_v6() -> String {
    "iprange: Incomplete range on line, expected an address after -".to_owned()
}

/// The bytes C `%s` would print for one buffer: everything up to the
/// first NUL byte. A header line or a file name may hold NUL bytes,
/// and `fprintf` stops at the first one.
pub fn cstr(value: &[u8]) -> &[u8] {
    match value.iter().position(|&b| b == 0) {
        Some(i) => &value[..i],
        None => value,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    // ------------------------------------------------------------------
    // fgets record iteration (C MAX_LINE = 1024)
    // ------------------------------------------------------------------

    #[test]
    fn nul_truncation_preserves_raw_record_boundaries() {
        let mut input = b"1.2.3.4\0".to_vec();
        input.resize(MAX_LINE - 1, b'x');
        input.extend_from_slice(b"5.6.7.8\n");
        let records: Vec<_> = Records::new(&input).collect();
        assert_eq!(records.len(), 2);
        assert_eq!(classify_v4(cstr(records[0]), 1), one_ip("1.2.3.4"));
        assert_eq!(classify_v4(cstr(records[1]), 2), one_ip("5.6.7.8"));
    }

    #[test]
    fn caller_prefix_is_used_only_for_ipv4() {
        assert_eq!(
            parse_token::<u32>("1.2.3.4", 24, true).unwrap(),
            Range {
                lo: 0x01020300,
                hi: 0x010203ff
            }
        );
        assert_eq!(
            parse_token::<u128>("::1", 24, true).unwrap(),
            Range { lo: 1, hi: 1 }
        );
    }

    #[test]
    fn records_split_like_fgets() {
        let data = b"a\nbb\n";
        let recs: Vec<&[u8]> = Records::new(data).collect();
        assert_eq!(recs, vec![&b"a\n"[..], &b"bb\n"[..]]);
    }

    #[test]
    fn records_truncate_at_1023_bytes() {
        let long = vec![b'x'; 3000];
        let recs: Vec<&[u8]> = Records::new(&long).collect();
        assert_eq!(recs.len(), 3);
        assert_eq!(recs[0].len(), 1023);
        assert_eq!(recs[1].len(), 1023);
        assert_eq!(recs[2].len(), 3000 - 2046);
        // A newline inside the window terminates the record early.
        let mut data = long;
        data[500] = b'\n';
        let recs: Vec<&[u8]> = Records::new(&data).collect();
        assert_eq!(recs[0].len(), 501);
        assert_eq!(recs[0][500], b'\n');
    }

    #[test]
    fn records_trailing_line_without_newline() {
        let recs: Vec<&[u8]> = Records::new(b"a\nb").collect();
        assert_eq!(recs, vec![&b"a\n"[..], &b"b"[..]]);
    }

    // ------------------------------------------------------------------
    // IPv4 line grammar (C parse_line)
    // ------------------------------------------------------------------

    fn v4(rec: &[u8]) -> LineOutcome {
        classify_v4(rec, 7)
    }

    fn one_ip(s: &str) -> LineOutcome {
        LineOutcome::OneIp(s.to_owned())
    }

    #[test]
    fn v4_comments_and_blank_lines() {
        assert!(matches!(v4(b"# comment\n"), LineOutcome::Empty));
        assert!(matches!(v4(b"; comment\n"), LineOutcome::Empty));
        assert!(matches!(v4(b""), LineOutcome::Empty));
        assert!(matches!(v4(b"   \n"), LineOutcome::Empty));
        assert!(matches!(v4(b"\t\r\n"), LineOutcome::Empty));
        assert!(matches!(v4(b"\n"), LineOutcome::Empty));
    }

    #[test]
    fn v4_single_ips() {
        assert_eq!(v4(b"1.2.3.4\n"), one_ip("1.2.3.4"));
        assert_eq!(v4(b"  1.2.3.4  \n"), one_ip("1.2.3.4"));
        assert_eq!(v4(b"1.2.3.4#c\n"), one_ip("1.2.3.4"));
        assert_eq!(v4(b"1.2.3.4 ; c\n"), one_ip("1.2.3.4"));
        assert_eq!(v4(b"123\n"), one_ip("123"));
        assert_eq!(v4(b"1.2.3\n"), one_ip("1.2.3"));
        assert_eq!(v4(b"1.2.3.4\r\n"), one_ip("1.2.3.4"));
    }

    #[test]
    fn v4_cidr_lines() {
        assert_eq!(v4(b"1.2.3.0/24\n"), one_ip("1.2.3.0/24"));
        assert_eq!(
            v4(b"1.2.3.4/255.255.255.0\n"),
            one_ip("1.2.3.4/255.255.255.0")
        );
    }

    #[test]
    fn v4_range_lines() {
        assert_eq!(
            v4(b"1.2.3.4-5.6.7.8\n"),
            LineOutcome::TwoIps("1.2.3.4".into(), "5.6.7.8".into())
        );
        assert_eq!(
            v4(b"1.2.3.4 - 5.6.7.8\n"),
            LineOutcome::TwoIps("1.2.3.4".into(), "5.6.7.8".into())
        );
        assert_eq!(
            v4(b"1.2.3.0/24 - 5.6.7.0/24\n"),
            LineOutcome::TwoIps("1.2.3.0/24".into(), "5.6.7.0/24".into())
        );
        assert_eq!(
            v4(b"123 - 456\n"),
            LineOutcome::TwoIps("123".into(), "456".into())
        );
    }

    #[test]
    fn v4_hostname_lines() {
        assert!(
            matches!(v4(b"www.example.com\n"), LineOutcome::Hostname(h) if h == "www.example.com")
        );
        assert!(
            matches!(v4(b" www.example.com \n"), LineOutcome::Hostname(h) if h == "www.example.com")
        );
        assert!(
            matches!(v4(b"www.example.com # c\n"), LineOutcome::Hostname(h) if h == "www.example.com")
        );
        assert!(matches!(v4(b"abcd\n"), LineOutcome::Hostname(h) if h == "abcd"));
    }

    #[test]
    fn v4_broken_ranges_warn_and_keep_first_ip() {
        match v4(b"1.2.3.4 -\n") {
            LineOutcome::WarnedRange { first, warning } => {
                assert_eq!(first, "1.2.3.4");
                assert_eq!(
                    warning,
                    "iprange: Incomplete range on line 7, expected an ip address after -, but line ended"
                );
            }
            other => panic!("expected WarnedRange, got {other:?}"),
        }
        match v4(b"1.2.3.4 - # c\n") {
            LineOutcome::WarnedRange { first, warning } => {
                assert_eq!(first, "1.2.3.4");
                // C prints the rest of the buffer, trailing newline
                // included, so the quote closes on the next line.
                assert_eq!(
                    warning,
                    "iprange: Ignoring text on line 7, expected an ip address after -, but found '# c\n'"
                );
            }
            other => panic!("expected WarnedRange, got {other:?}"),
        }
    }

    #[test]
    fn v4_invalid_lines() {
        assert!(matches!(v4(b"1.2.3.4 5.6.7.8\n"), LineOutcome::Invalid));
        assert!(matches!(v4(b"1.2.3.4 x\n"), LineOutcome::Invalid));
        assert!(matches!(v4(b"1.2.3.4 - 5.6.7.8 z\n"), LineOutcome::Invalid));
        assert!(matches!(v4(b"garbage stuff\n"), LineOutcome::Invalid));
        assert!(matches!(v4(b"1.2.3.4--5.6.7.8\n"), LineOutcome::Invalid));
        assert!(matches!(v4(b"1.2.3.4/24 x\n"), LineOutcome::Invalid));
    }

    // ------------------------------------------------------------------
    // IPv6 line grammar (C parse_line6)
    // ------------------------------------------------------------------

    fn v6(rec: &[u8]) -> LineOutcome {
        classify_v6(rec)
    }

    #[test]
    fn v6_comments_and_blank_lines() {
        assert!(matches!(v6(b"# c\n"), LineOutcome::Empty));
        assert!(matches!(v6(b"; c\n"), LineOutcome::Empty));
        assert!(matches!(v6(b"  \n"), LineOutcome::Empty));
    }

    #[test]
    fn v6_single_and_range_lines() {
        assert_eq!(v6(b"::1\n"), one_ip("::1"));
        assert_eq!(v6(b"2001:db8::1/64\n"), one_ip("2001:db8::1/64"));
        assert_eq!(v6(b"1.2.3.4\n"), one_ip("1.2.3.4"));
        // Bare hex runs parse as IPv6 addresses before hostnames.
        assert_eq!(v6(b"abcd\n"), one_ip("abcd"));
        assert_eq!(
            v6(b"::1 - ::2\n"),
            LineOutcome::TwoIps("::1".into(), "::2".into())
        );
    }

    #[test]
    fn v6_hostname_lines() {
        assert!(matches!(v6(b"garbage\n"), LineOutcome::Hostname(h) if h == "garbage"));
        // Non-hex letter retried as hostname from line start.
        assert!(matches!(v6(b"abcdG\n"), LineOutcome::Hostname(h) if h == "abcdG"));
        assert!(
            matches!(v6(b"www.example.com\n"), LineOutcome::Hostname(h) if h == "www.example.com")
        );
    }

    #[test]
    fn v6_incomplete_range_warning_has_no_line_number() {
        match v6(b"::1 -\n") {
            LineOutcome::WarnedRange { first, warning } => {
                assert_eq!(first, "::1");
                assert_eq!(
                    warning,
                    "iprange: Incomplete range on line, expected an address after -"
                );
            }
            other => panic!("expected WarnedRange, got {other:?}"),
        }
        // The v6 loader prints the same warning for a comment.
        assert!(matches!(
            v6(b"::1 - # c\n"),
            LineOutcome::WarnedRange { .. }
        ));
        // Scope identifiers are not IPv6 tokens (C is_ipv6_char).
        assert!(matches!(v6(b"fe80::1%eth0\n"), LineOutcome::Invalid));
    }

    // ------------------------------------------------------------------
    // Address token parsing through the family hook
    // ------------------------------------------------------------------

    #[test]
    fn parse_token_uses_family_semantics() {
        assert_eq!(
            parse_token::<u32>("1.2.3.4", 32, true).unwrap(),
            Range {
                lo: 0x01020304,
                hi: 0x01020304
            }
        );
        // fix-network default: host bits masked, broadcast filled.
        assert_eq!(
            parse_token::<u32>("1.2.3.99/24", 32, true).unwrap(),
            Range {
                lo: 0x01020300,
                hi: 0x010203ff
            }
        );
        assert_eq!(
            parse_token::<u32>("1.2.3.99/24", 32, false).unwrap(),
            Range {
                lo: 0x01020363,
                hi: 0x010203ff
            }
        );
    }

    #[test]
    fn warning_texts_are_c_exact() {
        assert_eq!(
            fmt_ignore_text(9, "# c\n"),
            "iprange: Ignoring text on line 9, expected an ip address after -, but found '# c\n'"
        );
        assert_eq!(
            fmt_incomplete_v4(4),
            "iprange: Incomplete range on line 4, expected an ip address after -, but line ended"
        );
        assert_eq!(
            fmt_incomplete_v6(),
            "iprange: Incomplete range on line, expected an address after -"
        );
    }
}
