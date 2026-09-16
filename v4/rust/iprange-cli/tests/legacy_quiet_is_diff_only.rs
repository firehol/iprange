//! `--quiet` suppresses diff output only.
//!
//! The released tool reads the quiet flag at exactly one place per
//! family: `if(!quiet) ipset_print(ips, print)` in the diff branch
//! (`src/iprange.c:1026`, `src/iprange6_main.c:414`). Its own help text
//! says so (`--quiet ... for diff mode`). Everything else — merge,
//! common, except, compare, count-unique — prints normally with
//! `--quiet` set, and the exit code keeps reporting the diff result
//! (1 when the difference is non-empty, 0 when the sets are equal).
//!
//! The Rust print path used to return early for every mode when quiet
//! was set, which silenced merges and comparisons that the C tool still
//! prints. These cases pin the C contract for both families and for the
//! equal-sets case, so a blanket suppression cannot come back.

#![cfg(unix)]

use std::ffi::OsStr;
mod parity_support;
use parity_support::assert_parity;

fn fixtures(tag: &str) -> parity_support::Scratch {
    let dir = parity_support::Scratch::new(tag);
    dir.file(b"a.txt", b"1.2.3.4\n5.6.7.8\n");
    dir.file(b"b.txt", b"1.2.3.4\n9.9.9.9\n");
    dir.file(b"aagain.txt", b"1.2.3.4\n5.6.7.8\n");
    dir.file(b"a6.txt", b"::1\n::2\n");
    dir.file(b"b6.txt", b"::1\n::3\n");
    dir
}

/// The fixture path `name` inside `dir`, as the argument to pass.
fn p(dir: &parity_support::Scratch, name: &str) -> Box<OsStr> {
    dir.path().join(name).into_os_string().into_boxed_os_str()
}

#[test]
fn quiet_keeps_every_non_diff_output() {
    let dir = fixtures("quiet-merge");
    let a = p(&dir, "a.txt");
    let b = p(&dir, "b.txt");

    // Merge: identical output with and without --quiet.
    let merged: &[u8] = b"1.2.3.4\n5.6.7.8\n9.9.9.9\n";
    assert_parity(
        "merge without --quiet",
        &[a.as_ref(), b.as_ref()],
        b"",
        (0, merged, b"".as_slice()),
    );
    assert_parity(
        "merge with --quiet",
        &[OsStr::new("--quiet"), a.as_ref(), b.as_ref()],
        b"",
        (0, merged, b"".as_slice()),
    );
    assert_parity(
        "common with --quiet",
        &[
            OsStr::new("--quiet"),
            a.as_ref(),
            OsStr::new("--common"),
            b.as_ref(),
        ],
        b"",
        (0, b"1.2.3.4\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "except with --quiet",
        &[
            OsStr::new("--quiet"),
            a.as_ref(),
            OsStr::new("--except"),
            b.as_ref(),
        ],
        b"",
        (0, b"5.6.7.8\n".as_slice(), b"".as_slice()),
    );
    // The CSV name column is the load path, so the expected rows are
    // built from the fixture paths.
    let (a_name, b_name) = (
        a.to_string_lossy().into_owned(),
        b.to_string_lossy().into_owned(),
    );
    let counted = format!("{a_name},2,2\n{b_name},2,2\n").into_bytes();
    assert_parity(
        "count-unique-all with --quiet",
        &[
            OsStr::new("--quiet"),
            a.as_ref(),
            b.as_ref(),
            OsStr::new("--count-unique-all"),
        ],
        b"",
        (0, &counted, b"".as_slice()),
    );
    let compare = format!("{a_name},{b_name},2,2,2,2,3,1\n").into_bytes();
    assert_parity(
        "compare with --quiet",
        &[
            OsStr::new("--quiet"),
            a.as_ref(),
            b.as_ref(),
            OsStr::new("--compare"),
        ],
        b"",
        (0, &compare, b"".as_slice()),
    );
}

#[test]
fn quiet_silences_diff_but_keeps_its_exit_code() {
    let dir = fixtures("quiet-diff");
    let a = p(&dir, "a.txt");
    let b = p(&dir, "b.txt");
    let aagain = p(&dir, "aagain.txt");

    // Without --quiet the difference prints and rc 1 reports it.
    assert_parity(
        "diff without --quiet",
        &[a.as_ref(), OsStr::new("--diff"), b.as_ref()],
        b"",
        (1, b"5.6.7.8\n9.9.9.9\n".as_slice(), b"".as_slice()),
    );
    // With --quiet nothing is printed, and rc still reports the diff.
    assert_parity(
        "diff with --quiet",
        &[
            OsStr::new("--quiet"),
            a.as_ref(),
            OsStr::new("--diff"),
            b.as_ref(),
        ],
        b"",
        (1, b"".as_slice(), b"".as_slice()),
    );
    // Equal sets: silent and rc 0 either way.
    assert_parity(
        "equal diff with --quiet",
        &[
            OsStr::new("--quiet"),
            a.as_ref(),
            OsStr::new("--diff"),
            aagain.as_ref(),
        ],
        b"",
        (0, b"".as_slice(), b"".as_slice()),
    );
}

#[test]
fn quiet_is_diff_only_in_ipv6_too() {
    let dir = fixtures("quiet-v6");
    let a = p(&dir, "a6.txt");
    let b = p(&dir, "b6.txt");

    assert_parity(
        "-6 merge with --quiet",
        &[
            OsStr::new("-6"),
            OsStr::new("--quiet"),
            a.as_ref(),
            b.as_ref(),
        ],
        b"",
        // ::2 and ::3 are adjacent, so the merge prints one /127.
        (0, b"::1\n::2/127\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "-6 diff without --quiet",
        &[
            OsStr::new("-6"),
            a.as_ref(),
            OsStr::new("--diff"),
            b.as_ref(),
        ],
        b"",
        (1, b"::2\n::3\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "-6 diff with --quiet",
        &[
            OsStr::new("-6"),
            OsStr::new("--quiet"),
            a.as_ref(),
            OsStr::new("--diff"),
            b.as_ref(),
        ],
        b"",
        (1, b"".as_slice(), b"".as_slice()),
    );
}
