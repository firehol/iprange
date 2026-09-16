//! Unrecognized dash-prefixed arguments: the two scanners disagree in
//! C, and the Rust legacy parser must disagree in the same way.
//!
//! The released tool has two argv scanners. `main()`
//! (`src/iprange.c:515-720`) matches its option table and sends every
//! remaining token to the input branch, so an unknown option such as
//! `-c` or `--bogus` is an ordinary **file name** there: the run fails
//! to open it and exits 1 with the two load lines. When the family has
//! already been switched to IPv6, `main()` skips IPv4 loading
//! (`src/iprange.c:722-724`) and `iprange6_run()` re-scans argv, where
//! every token that starts with `-`, is longer than one byte and is not
//! exactly `-` is discarded as a flag (`src/iprange6_main.c:176`) -
//! silently, with no diagnostic, and without being counted as input.
//!
//! Consequences pinned here (all measured on the C binary):
//! - `-6 -c` with `::/0` on stdin succeeds and prints `::/0`; the token
//!   is not an input and not an error.
//! - `iprange -6 -c ::/0` fails: after the skip, `::/0` is a *file name*
//!   (`iprange: Cannot load ipset: ::/0`) because no such file exists.
//! - position decides the classifier: `--bogus -6 file6` is classified
//!   by `main()` while the family is still IPv4, so it is a file and the
//!   run exits 1; `-6 --bogus file6` exits 0.
//! - the skip is byte-based, so `--` is skipped as well, and a bare `-`
//!   is never skipped (it is stdin in both families).
//!
//! The lowercase `-c` is not the counting option (`-C` is); the point of
//! these cases is the classifier, not that spelling.

#![cfg(unix)]

use std::ffi::OsStr;

mod parity_support;
use parity_support::{assert_parity, Scratch};

/// The two C load lines for a name that cannot be opened.
fn load_failure(token: &str) -> Vec<u8> {
    format!("iprange: {token} - No such file or directory\niprange: Cannot load ipset: {token}\n")
        .into_bytes()
}

#[test]
fn unknown_dash_argument_is_a_file_in_ipv4() {
    let dir = Scratch::new("dash-v4");
    let input = dir.file(b"in.txt", b"1.2.3.4\n");

    for token in ["-c", "-Z", "--bogus", "--", "-x"] {
        let expected = load_failure(token);
        // Before the inputs.
        assert_parity(
            &format!("v4 unknown {token} before a file"),
            &[OsStr::new(token), input.as_os_str()],
            b"",
            (1, b"".as_slice(), expected.as_slice()),
        );
        // After the inputs: still classified as an input.
        assert_parity(
            &format!("v4 unknown {token} after a file"),
            &[input.as_os_str(), OsStr::new(token)],
            b"",
            (1, b"".as_slice(), expected.as_slice()),
        );
    }
}

#[test]
fn unknown_dash_argument_is_skipped_in_ipv6() {
    let dir = Scratch::new("dash-v6");
    let v6 = dir.file(b"in6.txt", b"::1\n");
    let other = dir.file(b"other6.txt", b"::5\n");

    for token in ["-c", "-Z", "--bogus", "--", "-x"] {
        // The token is discarded: no diagnostic, and the file still
        // loads, so the run succeeds.
        assert_parity(
            &format!("v6 unknown {token} before a file"),
            &[OsStr::new("-6"), OsStr::new(token), v6.as_os_str()],
            b"",
            (0, b"::1\n".as_slice(), b"".as_slice()),
        );
        assert_parity(
            &format!("v6 unknown {token} after a file"),
            &[OsStr::new("-6"), v6.as_os_str(), OsStr::new(token)],
            b"",
            (0, b"::1\n".as_slice(), b"".as_slice()),
        );
        assert_parity(
            &format!("v6 unknown {token} between two files"),
            &[
                OsStr::new("-6"),
                v6.as_os_str(),
                OsStr::new(token),
                other.as_os_str(),
            ],
            b"",
            (0, b"::1\n::5\n".as_slice(), b"".as_slice()),
        );
    }

    // With nothing but the skipped token, stdin is the input (this is
    // the reproducer the review recorded as "`-6 -c ::/0`": the `::/0`
    // arrives on stdin, it is not a file name).
    assert_parity(
        "v6 skipped token leaves stdin as the input",
        &[OsStr::new("-6"), OsStr::new("-c")],
        b"::/0\n",
        (0, b"::/0\n".as_slice(), b"".as_slice()),
    );
    // And a literal `::/0` after the skipped token is a file name.
    assert_parity(
        "v6 literal address after a skipped token is a path",
        &[OsStr::new("-6"), OsStr::new("-c"), OsStr::new("::/0")],
        b"",
        (1, b"".as_slice(), load_failure("::/0").as_slice()),
    );
}

#[test]
fn the_family_active_at_the_token_position_decides_the_class() {
    let dir = Scratch::new("dash-order");
    let v6 = dir.file(b"in6.txt", b"::1\n");

    // `--bogus` is seen by main() while the family is still IPv4: it is
    // a file name, so the run fails even though -6 follows it.
    assert_parity(
        "unknown token before -6 is a path",
        &[OsStr::new("--bogus"), OsStr::new("-6"), v6.as_os_str()],
        b"",
        (1, b"".as_slice(), load_failure("--bogus").as_slice()),
    );
    // The same token after -6 is skipped by the IPv6 re-scan.
    assert_parity(
        "unknown token after -6 is skipped",
        &[OsStr::new("-6"), OsStr::new("--bogus"), v6.as_os_str()],
        b"",
        (0, b"::1\n".as_slice(), b"".as_slice()),
    );
    // A skipped token does not disturb the group split either: the
    // positional operator after it still selects group B.
    assert_parity(
        "unknown token beside a positional operator",
        &[
            OsStr::new("-6"),
            v6.as_os_str(),
            OsStr::new("-c"),
            OsStr::new("--except"),
            dir.file(b"other6.txt", b"::1\n").as_os_str(),
        ],
        b"",
        (0, b"".as_slice(), b"".as_slice()),
    );
}

#[test]
fn bare_dash_is_stdin_in_both_families() {
    // The IPv6 skip rule excludes exactly one token: a single `-`.
    assert_parity(
        "v4 - reads stdin",
        &[OsStr::new("-")],
        b"1.2.3.4\n",
        (0, b"1.2.3.4\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "v6 - reads stdin",
        &[OsStr::new("-6"), OsStr::new("-")],
        b"::9\n",
        (0, b"::9\n".as_slice(), b"".as_slice()),
    );
}

#[test]
fn unknown_dash_argument_beside_an_at_source_keeps_the_source() {
    // The skip rule is about the token's leading byte, so an `@` source
    // (byte `@`) is never skipped while a flag is: the list still loads
    // and the stray flag after it costs nothing.
    let dir = Scratch::new("dash-at");
    let data = dir.file(b"a6.txt", b"::7\n");
    let list = dir.path().join("list.txt");
    std::fs::write(&list, format!("{}\n", data.display()).as_bytes()).unwrap();

    assert_parity(
        "v6 unknown token beside @list",
        &[
            OsStr::new("-6"),
            OsStr::new(format!("@{}", list.display()).as_str()),
            OsStr::new("-c"),
        ],
        b"",
        (0, b"::7\n".as_slice(), b"".as_slice()),
    );
}
