//! Byte-exact `-v` diagnostics, byte-exact names, and the binary-format
//! validation messages of the legacy CLI, against the released C tool.
//!
//! Three classes of divergence from `iprange 2.1.2_master` are pinned here.
//! Every expectation is the C's own rc/stdout/stderr, measured over a
//! fixture directory that the test recreates byte for byte, so the case
//! names are relative and the pinned text does not depend on the scratch
//! path.
//!
//! 1. **Verbose content.** The C load path and the operation walkers own
//!    specific lines: `Optimizing combined ipset` when the printed set is
//!    not yet optimized (`src/ipset_print.c:140-142` calls `ipset_optimize`,
//!    which logs at `src/ipset_optimize.c:43,47`), `Printing <group-A name>
//!    with ...` because `ipset_exclude()` names its result after its first
//!    operand (`src/ipset_exclude.c:23`), one `Finding common IPs in A and
//!    B` for the compare modes because the released build defines
//!    `COMPARE_WITH_COMMON` (`CMakeLists.txt:76`, `configure.ac:99`,
//!    `src/iprange.c:1053,1096,1138`), `NON-OPTIMIZED ...` at the ordering
//!    transition (`src/ipset.h:107-121`, `src/ipset6.h:100-106`), and
//!    `Enabling prefix N` per `--prefixes` token (`src/iprange.c:549`, with
//!    no `iprange: ` prefix). The IPv6 twin prints none of the load
//!    bookkeeping lines: `src/iprange6_main.c` has no `debug` statement and
//!    `src/ipset6_load.c` only logs `Loading from ... (IPv6 mode)`.
//!
//! 2. **Name bytes.** A POSIX file name is any sequence of non-NUL,
//!    non-slash bytes, and the C echoes it with `%s`, so a name holding 0xFF
//!    must reach stderr as those bytes rather than as U+FFFD.
//!
//! 3. **Binary input.** The v1/v2 header validators echo the source label
//!    and the offending header value (`src/ipset_binary.c`,
//!    `src/ipset6_binary.c`), and every `ipset_load()` failure is followed
//!    by the caller's context line `Cannot load ipset: <name>`
//!    (`src/iprange.c:911`, `src/iprange6_main.c:320`). A payload read as
//!    text echoes its record through `src/ipset_load.c:343`,
//!    `src/ipset6_load.c:250`, which prints the buffer with `%s`: the echo
//!    stops at the first NUL byte.
//!
//! The only comparison exception is the C's exit-time timing line, which
//! reports this process's own wall clock and cannot be reproduced. It is
//! handled per line, not by normalization: a line matches only if it is
//! exactly `completed in <d>.<5 digits> seconds (read <d>.<5 digits> +
//! think <d>.<5 digits> + speak <d>.<5 digits>)`, and the pinned expectation
//! states where that single line belongs (the IPv6 twin prints no timing
//! line at all). A missing, duplicated, misplaced, or differently shaped
//! timing line fails the case.

#![cfg(unix)]

use std::ffi::OsStr;

mod parity_support;
use parity_support::{assert_verbose_parity, raw, Scratch};

/// One measured C contract: argv bytes, stdin, and the expected exit code,
/// stdout bytes, and stderr bytes (`<WALLCLOCK>` marks the timing line).
struct Case {
    label: &'static str,
    argv: &'static [&'static [u8]],
    stdin: &'static [u8],
    rc: i32,
    stdout: &'static [u8],
    stderr: &'static [u8],
}

/// Recreate the measured fixture directory: every input file by its exact
/// name bytes (some hold 0xFF) and the empty directories the `@dir` cases
/// need.
fn fixtures() -> Scratch {
    let dir = Scratch::new("verbose-diagnostics");
    for (name, payload) in FIXTURES {
        let path = dir.path().join(raw(name));
        if let Some(parent) = path.parent() {
            if parent != dir.path() {
                std::fs::create_dir_all(parent).expect("create the fixture directory");
            }
        }
        std::fs::write(&path, payload).expect("write the fixture");
    }
    for name in EMPTY_DIRS {
        std::fs::create_dir(dir.path().join(raw(name))).expect("create the empty directory");
    }
    dir
}

/// Run every case with the working directory pinned to the fixture
/// directory, so each name in the pinned text is the relative name the C
/// measured.
fn run_cases(cases: &[Case]) {
    let dir = fixtures();
    for case in cases {
        let argv: Vec<_> = case.argv.iter().map(|a| raw(a)).collect();
        let argv: Vec<&OsStr> = argv.iter().map(|a| a.as_os_str()).collect();
        assert_verbose_parity(
            case.label,
            dir.path(),
            &argv,
            case.stdin,
            (case.rc, case.stdout, case.stderr),
        );
    }
}

#[test]
fn verbose_diagnostics_ipv4_match_c() {
    run_cases(VERBOSE_IPV4);
}

#[test]
fn verbose_diagnostics_ipv6_match_c() {
    run_cases(VERBOSE_IPV6);
}

#[test]
fn operation_diagnostics_carry_name_bytes() {
    run_cases(NAMES_WITH_INVALID_BYTES);
}

#[test]
fn binary_input_diagnostics_match_c() {
    run_cases(BINARY_VALIDATION);
}

/// The pinned cases only mean something if the fixture directory they run
/// in is the one that was measured, including the names that are not valid
/// UTF-8 and the directories that hold no files.
#[test]
fn fixture_set_is_complete() {
    let dir = fixtures();
    for (name, _) in FIXTURES {
        assert!(
            dir.path().join(raw(name)).exists(),
            "fixture {} is missing",
            String::from_utf8_lossy(name)
        );
    }
    assert!(dir.path().join(raw(b"dir/z\xffy.txt")).is_file());
    assert!(dir.path().join(raw(b"v1\xff.bin")).is_file());
    for name in EMPTY_DIRS {
        let path = dir.path().join(raw(name));
        assert!(
            path.is_dir(),
            "{} is not a directory",
            String::from_utf8_lossy(name)
        );
        assert_eq!(std::fs::read_dir(&path).unwrap().count(), 0);
    }
}
// Generated by measurement against the C oracle; see the
// module documentation of this file.

const VERBOSE_IPV4: &[Case] = &[
    Case { label: "merge two sets", argv: &[b"-v", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "merge three sets", argv: &[b"-v", b"one.iprange", b"two.iprange", b"three.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/29\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Merging two.iprange to combined ipset\niprange: Merging three.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /29 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "merge single set", argv: &[b"-v", b"one.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "union two sets", argv: &[b"-v", b"--union", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "reduce merged sets", argv: &[b"-v", b"--ipset-reduce", b"20", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n\nCounting prefixes in combined ipset\nBreak down by prefix:\n\t- prefix /30 counts 1 entries\nTotal 1 entries generated\nAcceptable is to reach 16384 entries by reducing prefixes\n\tNothing more to reduce\n\nEliminated 0 out of 1 prefixes (1 remain in the final set).\n\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "except names result by group A", argv: &[b"-v", b"one.iprange", b"--except", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/31\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Removing IPs in two.iprange from one.iprange\niprange: Printing one.iprange with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "except three group A sets", argv: &[b"-v", b"one.iprange", b"two.iprange", b"--except", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/31\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to one.iprange\niprange: Optimizing one.iprange\niprange: Removing IPs in two.iprange from one.iprange\niprange: Printing one.iprange with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "compare finds common ips", argv: &[b"-v", b"--compare", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"one.iprange,two.iprange,1,1,4,2,4,2\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\n<WALLCLOCK>\n" },
    Case { label: "compare three sets", argv: &[b"-v", b"--compare", b"one.iprange", b"two.iprange", b"three.iprange"], stdin: b"", rc: 0,
        stdout: b"one.iprange,two.iprange,1,1,4,2,4,2\none.iprange,three.iprange,1,1,4,4,8,0\ntwo.iprange,three.iprange,1,1,2,4,6,0\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Is already optimized three.iprange\niprange: Finding common IPs in one.iprange and two.iprange\niprange: Finding common IPs in one.iprange and three.iprange\niprange: Finding common IPs in two.iprange and three.iprange\n<WALLCLOCK>\n" },
    Case { label: "compare first", argv: &[b"-v", b"--compare-first", b"one.iprange", b"two.iprange", b"three.iprange"], stdin: b"", rc: 0,
        stdout: b"two.iprange,1,2,2\nthree.iprange,1,4,0\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Is already optimized three.iprange\niprange: Finding common IPs in two.iprange and one.iprange\niprange: Finding common IPs in three.iprange and one.iprange\n<WALLCLOCK>\n" },
    Case { label: "compare next", argv: &[b"-v", b"one.iprange", b"--compare-next", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"one.iprange,two.iprange,1,1,4,2,4,2\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\n<WALLCLOCK>\n" },
    Case { label: "common of two sets", argv: &[b"-v", b"--common", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.2/31\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\niprange: Printing common with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "diff of two sets", argv: &[b"-v", b"one.iprange", b"--diff", b"two.iprange"], stdin: b"", rc: 1,
        stdout: b"10.0.0.0/31\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding diff IPs in one.iprange and two.iprange\niprange: Printing diff with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "count unique merged", argv: &[b"-v", b"--count-unique", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"1,4\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n<WALLCLOCK>\n" },
    Case { label: "count unique single set", argv: &[b"-v", b"--count-unique", b"one.iprange"], stdin: b"", rc: 0,
        stdout: b"1,4\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\n<WALLCLOCK>\n" },
    Case { label: "count unique all", argv: &[b"-v", b"--count-unique-all", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"one.iprange,1,4\ntwo.iprange,1,2\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\n<WALLCLOCK>\n" },
    Case { label: "print binary merged", argv: &[b"-v", b"--print-binary", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 2\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n<WALLCLOCK>\n" },
    Case { label: "print ranges merged", argv: &[b"-v", b"--print-ranges", b"one.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0-10.0.0.3\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\ntotals: 2 lines read, 1 distinct IP ranges found, 0 CIDR prefixes, 1 ranges printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "enabling prefix announcements", argv: &[b"-v", b"--prefixes", b"24,32", b"one.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n", stderr: b"Enabling prefix 24\nEnabling prefix 32\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n4 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 4 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "enabling prefix duplicates", argv: &[b"-v", b"--prefixes", b"24,24,8", b"one.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n", stderr: b"Enabling prefix 24\nEnabling prefix 24\nEnabling prefix 8\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n4 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 4 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "dir expansion", argv: &[b"-v", b"@dir"], stdin: b"", rc: 0,
        stdout: b"8.8.8.8\n9.9.9.9\n", stderr: b"iprange: Loading files from directory dir\niprange: Loading file dir/a.txt from directory dir\niprange: Loading from dir/a.txt\niprange: Loaded optimized dir/a.txt\niprange: Loading file dir/z\xffy.txt from directory dir\niprange: Loading from dir/z\xffy.txt\niprange: Loaded optimized dir/z\xffy.txt\niprange: Merging dir/z\xffy.txt to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "list expansion", argv: &[b"-v", b"@list.txt"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"iprange: Loading files from list list.txt\niprange: Loading file one.iprange from list (line 1)\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading file two.iprange from list (line 2)\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "empty file reports", argv: &[b"-v", b"empty.iprange"], stdin: b"", rc: 0,
        stdout: b"", stderr: b"iprange: Loading from empty.iprange\niprange: empty.iprange is empty\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "stdin", argv: &[b"-v", b"-"], stdin: b"", rc: 0,
        stdout: b"", stderr: b"iprange: Loading from stdin\niprange: stdin is empty\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "unsorted reports non-optimized", argv: &[b"-v", b"unsorted.iprange"], stdin: b"", rc: 0,
        stdout: b"1.1.1.1\n10.0.0.0/30\n10.0.0.8/30\n", stderr: b"iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 3 ranges, 9 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 2 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 3 lines read, 3 distinct IP ranges found, 2 CIDR prefixes, 3 CIDRs printed, 9 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "unsorted merge", argv: &[b"-v", b"unsorted.iprange", b"one.iprange"], stdin: b"", rc: 0,
        stdout: b"1.1.1.1\n10.0.0.0/30\n10.0.0.8/30\n", stderr: b"iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Merging one.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 3 ranges, 9 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 2 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 4 lines read, 3 distinct IP ranges found, 2 CIDR prefixes, 3 CIDRs printed, 9 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "unsorted except names result", argv: &[b"-v", b"unsorted.iprange", b"--except", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"1.1.1.1\n10.0.0.0/31\n10.0.0.8/30\n", stderr: b"iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Optimizing unsorted.iprange\niprange: Removing IPs in two.iprange from unsorted.iprange\niprange: Printing unsorted.iprange with 3 ranges, 7 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 4 lines read, 3 distinct IP ranges found, 3 CIDR prefixes, 3 CIDRs printed, 7 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "unsorted compare", argv: &[b"-v", b"--compare", b"unsorted.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"unsorted.iprange,two.iprange,3,1,9,2,9,2\n", stderr: b"iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Optimizing unsorted.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in unsorted.iprange and two.iprange\n<WALLCLOCK>\n" },
    Case { label: "single ips", argv: &[b"-v", b"-1", b"one.iprange"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n", stderr: b"iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 IPs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "ranges mode", argv: &[b"-v", b"-r", b"one.iprange"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: -r - No such file or directory\niprange: Cannot load ipset: -r\n" },
];

const VERBOSE_IPV6: &[Case] = &[
    Case { label: "v6 merge", argv: &[b"-6", b"-v", b"one6.iprange", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n" },
    Case { label: "v6 merge three", argv: &[b"-6", b"-v", b"one6.iprange", b"two6.iprange", b"three6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n2001:db8:0:1::/64\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Loading from three6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Merging three6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 2 ranges, 18446744073709551624 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /64 counts 1 entries\n\t- prefix /125 counts 1 entries\n\ntotals: 3 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 18446744073709551624 unique IPs\n" },
    Case { label: "v6 single set", argv: &[b"-6", b"-v", b"one6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n" },
    Case { label: "v6 except names result", argv: &[b"-6", b"-v", b"one6.iprange", b"--except", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::4/126\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Removing IPs in two6.iprange from one6.iprange (IPv6)\niprange: Printing one6.iprange (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n" },
    Case { label: "v6 compare keeps combining", argv: &[b"-6", b"-v", b"--compare", b"one6.iprange", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"one6.iprange,two6.iprange,1,1,8,4,8,4\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Combining one6.iprange and two6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\n" },
    Case { label: "v6 compare first", argv: &[b"-6", b"-v", b"--compare-first", b"one6.iprange", b"two6.iprange", b"three6.iprange"], stdin: b"", rc: 0,
        stdout: b"two6.iprange,1,4,4\nthree6.iprange,1,18446744073709551616,0\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Loading from three6.iprange (IPv6 mode)\niprange: Combining two6.iprange and one6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\niprange: Combining three6.iprange and one6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\n" },
    Case { label: "v6 common", argv: &[b"-6", b"-v", b"--common", b"one6.iprange", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/126\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Finding common IPs in one6.iprange and two6.iprange (IPv6)\niprange: Printing common (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n" },
    Case { label: "v6 diff", argv: &[b"-6", b"-v", b"one6.iprange", b"--diff", b"two6.iprange"], stdin: b"", rc: 1,
        stdout: b"2001:db8::4/126\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Finding diff IPs in one6.iprange and two6.iprange (IPv6)\niprange: Printing diff (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n" },
    Case { label: "v6 count unique merged", argv: &[b"-6", b"-v", b"--count-unique", b"one6.iprange", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"1,8\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\n" },
    Case { label: "v6 count unique single", argv: &[b"-6", b"-v", b"--count-unique", b"one6.iprange"], stdin: b"", rc: 0,
        stdout: b"1,8\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\n" },
    Case { label: "v6 count unique all", argv: &[b"-6", b"-v", b"--count-unique-all", b"one6.iprange", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"one6.iprange,1,8\ntwo6.iprange,1,4\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\n" },
    Case { label: "v6 print binary merged", argv: &[b"-6", b"-v", b"--print-binary", b"one6.iprange", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 2\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 ", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\n" },
    Case { label: "v6 prefixes silent", argv: &[b"-6", b"-v", b"--prefixes", b"24,128", b"one6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::\n2001:db8::1\n2001:db8::2\n2001:db8::3\n2001:db8::4\n2001:db8::5\n2001:db8::6\n2001:db8::7\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n8 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 8 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 8 CIDRs printed, 8 unique IPs\n" },
    Case { label: "v6 dir expansion silent", argv: &[b"-6", b"-v", b"@dir6"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/126\n", stderr: b"iprange: Loading from dir6/a.txt (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n" },
    Case { label: "v6 list expansion silent", argv: &[b"-6", b"-v", b"@list6.txt"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n" },
    Case { label: "v6 empty dir", argv: &[b"-6", b"-v", b"@emptydir6"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: No valid files found in directory: emptydir6\n" },
    Case { label: "v6 empty list", argv: &[b"-6", b"-v", b"@emptylist.txt"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: No valid files found in file list: emptylist.txt\n" },
    Case { label: "v6 empty file silent", argv: &[b"-6", b"-v", b"empty6.iprange"], stdin: b"", rc: 0,
        stdout: b"", stderr: b"iprange: Loading from empty6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n" },
    Case { label: "v6 stdin silent", argv: &[b"-6", b"-v", b"-"], stdin: b"", rc: 0,
        stdout: b"", stderr: b"iprange: Loading from stdin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n" },
    Case { label: "v6 binary load silent", argv: &[b"-6", b"-v", b"v2.bin"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"iprange: Loading from v2.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n" },
    Case { label: "v6 unsorted non-optimized", argv: &[b"-6", b"-v", b"unsorted6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/120\n", stderr: b"iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 256 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /120 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 256 unique IPs\n" },
    Case { label: "v6 unsorted merge", argv: &[b"-6", b"-v", b"unsorted6.iprange", b"one6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/120\n", stderr: b"iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Loading from one6.iprange (IPv6 mode)\niprange: Merging one6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 256 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /120 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 256 unique IPs\n" },
    Case { label: "v6 unsorted except", argv: &[b"-6", b"-v", b"unsorted6.iprange", b"--except", b"two6.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::4/126\n2001:db8::8/125\n2001:db8::10/124\n2001:db8::20/123\n2001:db8::40/122\n2001:db8::80/121\n", stderr: b"iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Optimizing unsorted6.iprange (IPv6)\niprange: Removing IPs in two6.iprange from unsorted6.iprange (IPv6)\niprange: Printing unsorted6.iprange (IPv6) with 1 ranges, 252 unique IPs\n\n6 printed CIDRs, break down by prefix:\n\t- prefix /121 counts 1 entries\n\t- prefix /122 counts 1 entries\n\t- prefix /123 counts 1 entries\n\t- prefix /124 counts 1 entries\n\t- prefix /125 counts 1 entries\n\t- prefix /126 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 6 CIDR prefixes, 6 CIDRs printed, 252 unique IPs\n" },
];

const NAMES_WITH_INVALID_BYTES: &[Case] = &[
    Case { label: "merge names the bytes", argv: &[b"-v", b"bad\xffname.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"1.2.3.4\n10.0.0.2/31\n", stderr: b"iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 3 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 3 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "as label with 0xFF", argv: &[b"-v", b"bad\xffname.iprange", b"as", b"lab\xffel"], stdin: b"", rc: 0,
        stdout: b"1.2.3.4\n", stderr: b"iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "except names the bytes", argv: &[b"-v", b"bad\xffname.iprange", b"--except", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"1.2.3.4\n", stderr: b"iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Removing IPs in two.iprange from bad\xffname.iprange\niprange: Printing bad\xffname.iprange with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "compare names the bytes", argv: &[b"-v", b"--compare", b"bad\xffname.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"bad\xffname.iprange,two.iprange,1,1,1,2,3,0\n", stderr: b"iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\xffname.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in bad\xffname.iprange and two.iprange\n<WALLCLOCK>\n" },
    Case { label: "compare next names the bytes", argv: &[b"-v", b"bad\xffname.iprange", b"--compare-next", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"bad\xffname.iprange,two.iprange,1,1,1,2,3,0\n", stderr: b"iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\xffname.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in bad\xffname.iprange and two.iprange\n<WALLCLOCK>\n" },
    Case { label: "diff names the bytes", argv: &[b"-v", b"bad\xffname.iprange", b"--diff", b"two.iprange"], stdin: b"", rc: 1,
        stdout: b"1.2.3.4\n10.0.0.2/31\n", stderr: b"iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding diff IPs in bad\xffname.iprange and two.iprange\niprange: Printing diff with 2 ranges, 3 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 3 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "count unique all bytes", argv: &[b"-v", b"--count-unique-all", b"bad\xffname.iprange", b"two.iprange"], stdin: b"", rc: 0,
        stdout: b"bad\xffname.iprange,1,1\ntwo.iprange,1,2\n", stderr: b"iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\xffname.iprange\niprange: Is already optimized two.iprange\n<WALLCLOCK>\n" },
    Case { label: "binary bytes in v4", argv: &[b"-v", b"v1\xff.bin"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"iprange: Loading from v1\xff.bin\niprange: Binary loaded optimized v1\xff.bin\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "binary bytes in v6", argv: &[b"-6", b"-v", b"v2\xff.bin"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"iprange: Loading from v2\xff.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n" },
    Case { label: "v6 name bytes", argv: &[b"-6", b"-v", b"bad6\xffname.iprange"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"iprange: Loading from bad6\xffname.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n" },
];

const BINARY_VALIDATION: &[Case] = &[
    Case { label: "damaged flag line", argv: &[b"d_badflag.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_badflag.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'.\niprange: Cannot fast load d_badflag.bin\niprange: Cannot load ipset: d_badflag.bin\n" },
    Case { label: "damaged flag line verbose", argv: &[b"-v", b"d_badflag.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: Loading from d_badflag.bin\niprange: d_badflag.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'.\niprange: Cannot fast load d_badflag.bin\niprange: Cannot load ipset: d_badflag.bin\n" },
    Case { label: "damaged record size", argv: &[b"d_badrecsize.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_badrecsize.bin: invalid record size 9 (expected 8)\niprange: Cannot fast load d_badrecsize.bin\niprange: Cannot load ipset: d_badrecsize.bin\n" },
    Case { label: "damaged record size verbose", argv: &[b"-v", b"d_badrecsize.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: Loading from d_badrecsize.bin\niprange: d_badrecsize.bin: invalid record size 9 (expected 8)\niprange: Cannot fast load d_badrecsize.bin\niprange: Cannot load ipset: d_badrecsize.bin\n" },
    Case { label: "records not a number", argv: &[b"d_badrecords.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_badrecords.bin: invalid records value 'abc\n'\niprange: Cannot fast load d_badrecords.bin\niprange: Cannot load ipset: d_badrecords.bin\n" },
    Case { label: "records overflow bound", argv: &[b"d_hugerecords.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_hugerecords.bin: invalid number of records (18446744073709551615)\niprange: Cannot fast load d_hugerecords.bin\niprange: Cannot load ipset: d_hugerecords.bin\n" },
    Case { label: "bytes mismatch", argv: &[b"d_badbytes.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_badbytes.bin invalid number of bytes, found 999, expected 12.\niprange: Cannot fast load d_badbytes.bin\niprange: Cannot load ipset: d_badbytes.bin\n" },
    Case { label: "bytes mismatch verbose", argv: &[b"-v", b"d_badbytes.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: Loading from d_badbytes.bin\niprange: d_badbytes.bin invalid number of bytes, found 999, expected 12.\niprange: Cannot fast load d_badbytes.bin\niprange: Cannot load ipset: d_badbytes.bin\n" },
    Case { label: "lines below entries", argv: &[b"d_badlines.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_badlines.bin: lines (0) cannot be less than entries (1)\niprange: Cannot fast load d_badlines.bin\niprange: Cannot load ipset: d_badlines.bin\n" },
    Case { label: "unique ips not a number", argv: &[b"d_badunique.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_badunique.bin: invalid unique ips value 'qq\n'\niprange: Cannot fast load d_badunique.bin\niprange: Cannot load ipset: d_badunique.bin\n" },
    Case { label: "unique ips below entries", argv: &[b"d_lowunique.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_lowunique.bin: unique IPs (0) cannot be less than entries (1)\niprange: Cannot fast load d_lowunique.bin\niprange: Cannot load ipset: d_lowunique.bin\n" },
    Case { label: "truncated payload", argv: &[b"d_truncated.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_truncated.bin: expected to load 1 entries, loaded 0\niprange: Cannot fast load d_truncated.bin\niprange: Cannot load ipset: d_truncated.bin\n" },
    Case { label: "truncated payload verbose", argv: &[b"-v", b"d_truncated.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: Loading from d_truncated.bin\niprange: d_truncated.bin: expected to load 1 entries, loaded 0\niprange: Cannot fast load d_truncated.bin\niprange: Cannot load ipset: d_truncated.bin\n" },
    Case { label: "trailing data", argv: &[b"d_trailing.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d_trailing.bin: trailing data found after binary payload\niprange: Cannot fast load d_trailing.bin\niprange: Cannot load ipset: d_trailing.bin\n" },
    Case { label: "trailing data verbose", argv: &[b"-v", b"d_trailing.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: Loading from d_trailing.bin\niprange: d_trailing.bin: trailing data found after binary payload\niprange: Cannot fast load d_trailing.bin\niprange: Cannot load ipset: d_trailing.bin\n" },
    Case { label: "bad endianness marker", argv: &[b"d_nomarker.bin"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"" },
    Case { label: "v2 in v4 mode", argv: &[b"v2.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: v2.bin: IPv6 binary file cannot be loaded in IPv4 mode (use -6)\niprange: Cannot load ipset: v2.bin\n" },
    Case { label: "v1 in v6 mode", argv: &[b"-6", b"v1.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: v1.bin: IPv4 binary file cannot be loaded in IPv6 mode\niprange: Cannot load ipset: v1.bin\n" },
    Case { label: "v6 damaged record size", argv: &[b"-6", b"d6_badrecsize.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d6_badrecsize.bin expected optimized flag but found 'record size 16\n'.\niprange: Cannot load binary v2 d6_badrecsize.bin\niprange: Cannot load ipset: d6_badrecsize.bin\n" },
    Case { label: "v6 damaged bytes", argv: &[b"-6", b"d6_badbytes.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d6_badbytes.bin expected records count but found 'bytes 5\n'.\niprange: Cannot load binary v2 d6_badbytes.bin\niprange: Cannot load ipset: d6_badbytes.bin\n" },
    Case { label: "v6 damaged bytes verbose", argv: &[b"-6", b"-v", b"d6_badbytes.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: Loading from d6_badbytes.bin (IPv6 mode)\niprange: d6_badbytes.bin expected records count but found 'bytes 5\n'.\niprange: Cannot load binary v2 d6_badbytes.bin\niprange: Cannot load ipset: d6_badbytes.bin\n" },
    Case { label: "v6 damaged unique ips", argv: &[b"-6", b"d6_badunique.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d6_badunique.bin expected lines count but found 'unique ips zz\n'.\niprange: Cannot load binary v2 d6_badunique.bin\niprange: Cannot load ipset: d6_badunique.bin\n" },
    Case { label: "v6 damaged family line", argv: &[b"-6", b"d6_badfamily.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d6_badfamily.bin expected family 'ipv6' but found 'family ipv4\n'.\niprange: Cannot load binary v2 d6_badfamily.bin\niprange: Cannot load ipset: d6_badfamily.bin\n" },
    Case { label: "v6 unique below entries", argv: &[b"-6", b"d6_lowunique.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d6_lowunique.bin expected lines count but found 'unique ips 0\n'.\niprange: Cannot load binary v2 d6_lowunique.bin\niprange: Cannot load ipset: d6_lowunique.bin\n" },
    Case { label: "damaged flag line bytes", argv: &[b"bad\xffflag.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: bad\xffflag.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'.\niprange: Cannot fast load bad\xffflag.bin\niprange: Cannot load ipset: bad\xffflag.bin\n" },
    Case { label: "damaged records bytes", argv: &[b"bad\xffrecs.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: bad\xffrecs.bin: invalid records value 'abc\n'\niprange: Cannot fast load bad\xffrecs.bin\niprange: Cannot load ipset: bad\xffrecs.bin\n" },
    Case { label: "v6 damaged bytes name bytes", argv: &[b"-6", b"d6bad\xffbytes.bin"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: d6bad\xffbytes.bin expected records count but found 'bytes 5\n'.\niprange: Cannot load binary v2 d6bad\xffbytes.bin\niprange: Cannot load ipset: d6bad\xffbytes.bin\n" },
    Case { label: "bad marker name bytes", argv: &[b"bad\xffnomark.bin"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"" },
    Case { label: "valid v1 binary", argv: &[b"v1.bin"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"" },
    Case { label: "valid v1 binary verbose", argv: &[b"-v", b"v1.bin"], stdin: b"", rc: 0,
        stdout: b"10.0.0.0/30\n", stderr: b"iprange: Loading from v1.bin\niprange: Binary loaded optimized v1.bin\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "valid v2 binary", argv: &[b"-6", b"v2.bin"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"" },
    Case { label: "valid v2 binary verbose", argv: &[b"-6", b"-v", b"v2.bin"], stdin: b"", rc: 0,
        stdout: b"2001:db8::/125\n", stderr: b"iprange: Loading from v2.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n" },

    // A record with an embedded NUL is echoed by C `fprintf("%s")` only
    // up to that NUL, so nothing after it may appear in the diagnostic.
    Case { label: "record echo stops at NUL", argv: &[b"nulsp.iprange"], stdin: b"", rc: 1,
        stdout: b"", stderr: b"iprange: Cannot understand line No 1 from nulsp.iprange: x y\niprange: Cannot load ipset: nulsp.iprange\n" },
];

/// Every input the cases read, as the exact bytes the C
/// measurement used. The binary payloads are the C's own
/// `--print-binary` output, so a validation case tests the
/// released format rather than a reconstruction of it.
const FIXTURES: &[(&[u8], &[u8])] = &[
    (b"one.iprange", b"10.0.0.0/30\n"),
    (b"two.iprange", b"10.0.0.2/31\n"),
    (b"three.iprange", b"10.0.0.4/30\n"),
    (b"one6.iprange", b"2001:db8::/125\n"),
    (b"two6.iprange", b"2001:db8::/126\n"),
    (b"three6.iprange", b"2001:db8:0:1::/64\n"),
    (b"empty.iprange", b""),
    (b"empty6.iprange", b""),
    (b"unsorted.iprange", b"10.0.0.8/30\n10.0.0.0/30\n1.1.1.1\n"),
    (b"unsorted6.iprange", b"2001:db8::/120\n2001:db8::/126\n"),
    (b"dir/a.txt", b"8.8.8.8\n"),
    (b"dir/z\xffy.txt", b"9.9.9.9\n"),
    (b"dir6/a.txt", b"2001:db8::/126\n"),
    (b"list.txt", b"one.iprange\ntwo.iprange\n"),
    (b"list6.txt", b"one6.iprange\ntwo6.iprange\n"),
    (b"bad\xffname.iprange", b"1.2.3.4\n"),
    (b"bad6\xffname.iprange", b"2001:db8::/125\n"),
    (b"emptylist.txt", b"# nothing\n"),
    (b"v1.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"v2.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"v1\xff.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"v2\xff.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"bad\xffflag.bin", b"iprange binary format v1.0\nmaybe-optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"bad\xffnomark.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"bad\xffrecs.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords abc\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d6_badbytes.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nbytes 5\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"d6_badfamily.bin", b"iprange binary format v2.0\nfamily ipv4\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"d6_badrecsize.bin", b"iprange binary format v2.0\nipv6\nrecord size 16\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"d6_badunique.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nunique ips zz\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"d6_lowunique.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nunique ips 0\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"d6_wrongheader.bin", b"BINARY-BROKEN\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"d6bad\xffbytes.bin", b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nbytes 5\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\r\x01 "),
    (b"d_badbytes.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 999\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_badflag.bin", b"iprange binary format v1.0\nmaybe-optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_badlines.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 0\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_badrecords.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords abc\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_badrecsize.bin", b"iprange binary format v1.0\noptimized\nrecord size 9\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_badunique.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips qq\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_hugerecords.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 18446744073709551615\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_lowunique.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 0\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_nomarker.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"d_trailing.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\njunk"),
    (b"d_truncated.bin", b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00"),
    (b"d_wrongheader.bin", b"BINARY-BROKEN\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n"),
    (b"nulsp.iprange", b"x y\0z\n"),
];

/// Directories the C measurement found empty (an `@dir` case
/// needs one that no fixture file can create).
const EMPTY_DIRS: &[&[u8]] = &[b"emptydir", b"emptydir6"];
