//! A value-taking option as the last command-line argument.
//!
//! Every option branch of the C scan is guarded by `i + 1 < argc`
//! (`src/iprange.c:515-706`, `src/iprange6_main.c:112-170`), so an
//! option with no following argument is **not** taken as an option at
//! all: the token falls through to the input branch of the scan. In
//! IPv4 mode that branch opens it as a file, so `iprange --min-prefix`
//! prints the two load lines and exits 1. In IPv6 mode the IPv4 main
//! skips loading (`src/iprange.c:722-724`) and the `iprange6_run()`
//! re-scan discards the dash-prefixed token as a flag
//! (`src/iprange6_main.c:176`), so `-6 --min-prefix` runs on stdin and
//! exits 0.
//!
//! The defect pinned here is the loop the old Rust scan entered instead:
//! `take_value` returned `None` without advancing the cursor, and every
//! caller arm `continue`d, so the same token was pushed as an input
//! source forever. That never exits and allocates without bound
//! (measured: 12.7 GB in 5 s). Both bounds are therefore asserted here:
//! the run must finish inside the wall-clock bound *and* inside a 2 GiB
//! address-space cap, so an allocation bomb fails fast rather than
//! waiting out the timeout.
//!
//! The list of options is derived from the code, not from memory: it is
//! every token whose C branch reads `argv[++i]`, together with the
//! aliases the Rust parser matches. `--at` and `--file-list` are *not*
//! in it because neither the C tool nor the Rust parser has such an
//! option.

#![cfg(unix)]

use std::path::PathBuf;

mod parity_support;
use parity_support::{assert_parity, raw, run_bounded, PROGRAM};

/// Every value-taking option and alias, with a value that is valid for
/// it in both families (used for the present-value control cases).
const VALUE_OPTIONS: &[(&str, &str)] = &[
    ("--min-prefix", "8"),
    ("--prefixes", "8,16"),
    ("--default-prefix", "24"),
    ("-p", "24"),
    ("--ipset-reduce", "10"),
    ("--reduce-factor", "10"),
    ("--ipset-reduce-entries", "10"),
    ("--reduce-entries", "10"),
    ("--print-prefix", "P"),
    ("--print-prefix-ips", "P"),
    ("--print-prefix-nets", "P"),
    ("--print-suffix", "S"),
    ("--print-suffix-ips", "S"),
    ("--print-suffix-nets", "S"),
    ("--dns-threads", "2"),
];

/// The two C load lines for a name that cannot be opened.
fn load_failure(token: &str) -> Vec<u8> {
    format!("iprange: {token} - No such file or directory\niprange: Cannot load ipset: {token}\n")
        .into_bytes()
}

#[test]
fn value_less_trailing_option_is_an_input_in_ipv4_and_exits_1() {
    for (option, _) in VALUE_OPTIONS {
        assert_parity(
            &format!("trailing {option}"),
            &[std::ffi::OsStr::new(option)],
            b"",
            (1, b"".as_slice(), load_failure(option).as_slice()),
        );
    }
    // `as` is not dash-prefixed, so the trailing keyword is an input
    // path in both families (C's branch needs a next argument).
    assert_parity(
        "trailing as",
        &[std::ffi::OsStr::new("as")],
        b"",
        (1, b"".as_slice(), load_failure("as").as_slice()),
    );
}

#[test]
fn value_less_trailing_option_is_a_discarded_flag_in_ipv6() {
    for (option, _) in VALUE_OPTIONS {
        // stdin carries the only input; the option token is skipped.
        assert_parity(
            &format!("-6 trailing {option}"),
            &[std::ffi::OsStr::new("-6"), std::ffi::OsStr::new(option)],
            b"::/0\n",
            (0, b"::/0\n".as_slice(), b"".as_slice()),
        );
    }
    assert_parity(
        "-6 trailing as",
        &[std::ffi::OsStr::new("-6"), std::ffi::OsStr::new("as")],
        b"",
        (1, b"".as_slice(), load_failure("as").as_slice()),
    );
}

#[test]
fn value_less_trailing_option_stays_inside_the_memory_cap() {
    // The regression is an allocation loop, so the memory bound is the
    // assertion that matters: rc must be 1 well before 2 GiB is touched.
    for (option, _) in VALUE_OPTIONS {
        let run = run_bounded(
            &PathBuf::from(PROGRAM),
            &[std::ffi::OsStr::new(option)],
            b"",
        );
        assert!(
            !run.timed_out,
            "{option}: the run did not finish inside the bound ({})",
            run.describe()
        );
        assert_eq!(
            (run.code, run.signal),
            (Some(1), None),
            "{option}: expected a clean rc 1, got {}",
            run.describe()
        );
    }
}

#[test]
fn present_value_options_keep_working() {
    // Control cases: the same options with their value must behave
    // exactly as they did before the missing-value fix, and two of them
    // must visibly take effect, so "reject every option value" is not
    // accepted as a fix.
    use std::ffi::OsStr;

    let dir = parity_support::Scratch::new("d1-control");
    let adjacent = dir.file(b"adjacent.txt", b"1.2.3.4\n1.2.3.5\n");
    let single = dir.file(b"single.txt", b"1.2.3.4\n");
    let v6 = dir.file(b"in6.txt", b"::1\n");
    let (adjacent, single, v6) = (adjacent.as_os_str(), single.as_os_str(), v6.as_os_str());

    assert_parity(
        "--min-prefix 8",
        &[OsStr::new("--min-prefix"), OsStr::new("8"), adjacent],
        b"",
        (0, b"1.2.3.4/31\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        // Only /32 may be printed, so the adjacent pair stays two IPs
        // (without the option the merge prints 1.2.3.4/31).
        "--prefixes 32",
        &[OsStr::new("--prefixes"), OsStr::new("32"), adjacent],
        b"",
        (0, b"1.2.3.4\n1.2.3.5\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "--print-prefix P",
        &[OsStr::new("--print-prefix"), OsStr::new("P"), adjacent],
        b"",
        (0, b"P1.2.3.4/31\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "--dns-threads 2",
        &[OsStr::new("--dns-threads"), OsStr::new("2"), adjacent],
        b"",
        (0, b"1.2.3.4/31\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        // A bare address takes the default prefix: proves the value was
        // consumed by the option and applied (src/iprange.c:559).
        "--default-prefix 24",
        &[OsStr::new("--default-prefix"), OsStr::new("24"), single],
        b"",
        (0, b"1.2.3.0/24\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "-p 24",
        &[OsStr::new("-p"), OsStr::new("24"), single],
        b"",
        (0, b"1.2.3.0/24\n".as_slice(), b"".as_slice()),
    );
    // In IPv6 mode C consumes the value without validating it (the IPv6
    // parser always uses /128), so `24` must not be re-read as an input
    // file (src/iprange.c:564, src/iprange6_main.c:155).
    assert_parity(
        "-6 -p 24",
        &[OsStr::new("-6"), OsStr::new("-p"), OsStr::new("24"), v6],
        b"",
        (0, b"::1\n".as_slice(), b"".as_slice()),
    );
    assert_parity(
        "-6 --default-prefix 24",
        &[
            OsStr::new("-6"),
            OsStr::new("--default-prefix"),
            OsStr::new("24"),
            v6,
        ],
        b"",
        (0, b"::1\n".as_slice(), b"".as_slice()),
    );
}

#[test]
fn invalid_value_for_a_present_option_still_reports_the_argv_bytes() {
    // Guards against "fixing" the missing-value case by rejecting the
    // option: a valid option with an out-of-range value keeps the C
    // usage diagnostic (bytes, including a non-UTF-8 value).
    let bad = raw(b"a\xffb");
    let expected =
        b"iprange: Invalid value 'a\xffb' for --min-prefix. It must be between 1 and 32.\n"
            .to_vec();
    assert_parity(
        "--min-prefix with a non-numeric value",
        &[std::ffi::OsStr::new("--min-prefix"), bad.as_os_str()],
        b"",
        (1, b"".as_slice(), expected.as_slice()),
    );
}

#[test]
fn the_harness_bounds_really_fire() {
    // A harness that cannot detect a hang would pass every case above by
    // accident, so both bounds are proven here.
    use std::ffi::OsStr;

    // The wall-clock bound fires on a run that never finishes.
    let sleep = PathBuf::from("/bin/sleep");
    let forever = raw(b"30");
    let run = parity_support::run_bounded_with_limit(
        &sleep,
        &[forever.as_os_str()],
        b"",
        parity_support::MEM_LIMIT_KIB,
        std::time::Duration::from_millis(300),
    );
    assert!(run.timed_out, "the wall-clock bound did not fire");

    // The address-space cap is in force in the child: a shell asked for
    // its own limit reports the cap (in KiB), so the same cap that kills
    // an allocation bomb is verified to be applied.
    let sh = PathBuf::from("/bin/sh");
    let probe = raw(b"-c");
    let command = raw(b"ulimit -v");
    let run = run_bounded(&sh, &[probe.as_os_str(), command.as_os_str()], b"");
    let reported: u64 = String::from_utf8_lossy(&run.stdout)
        .trim()
        .parse()
        .unwrap_or_else(|e| panic!("ulimit -v reported {:?}: {e}", run.stdout));
    assert_eq!(
        reported,
        parity_support::MEM_LIMIT_KIB,
        "the run did not inherit the address-space cap ({}: {})",
        OsStr::new("-c").to_string_lossy(),
        run.describe()
    );
}
