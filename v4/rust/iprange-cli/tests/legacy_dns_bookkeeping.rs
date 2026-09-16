//! DNS `-v` bookkeeping for resolved hostnames, against the released C tool.
//!
//! The C resolver adds one ipset entry per reply ADDRESS, and that single
//! fact drives every line pinned here. One hostname that answers with N
//! addresses therefore produces N per-address debug lines, N in `IPs got`,
//! and N in `totals: %zu lines read`, and the order those N entries are
//! added in decides whether the set is reported as optimized:
//!
//!   * the worker stacks one reply node per address (`src/ipset_dns.c:253-255`,
//!     `src/ipset6_dns.c:210-211`) and the loader adds one range per node
//!     with no deduplication (`src/ipset_dns.c:275-281`,
//!     `src/ipset6_dns.c:223-233`), so `ips->lines` (`src/ipset.h:89`,
//!     `src/ipset6.h:72`) and `totals: %zu lines read`
//!     (`src/ipset_print.c:223`, `src/ipset6_print.c:210`) count entries,
//!     not input records;
//!   * the reply list is a stack (`p->next = dns_replies; dns_replies = p;`),
//!     so entries are added in the reverse of the order the resolver
//!     answered in; that order decides whether an out-of-order add clears
//!     the optimized flag and prints `NON-OPTIMIZED ...`
//!     (`src/ipset.h:100-121`, `src/ipset6.h:96-105`), which in turn drives
//!     `Loaded non-optimized NAME` (`src/ipset_load.c:418`) and
//!     `Optimizing ...` (`src/ipset_optimize.c:47`,
//!     `src/ipset6_optimize.c:24`);
//!   * `DNS: '%s' = %s` is printed once per address while the worker walks
//!     the answer list, IPv4 only (`src/ipset_dns.c:246-249`); the IPv6 pool
//!     prints no per-address line, no `Creating new DNS thread`
//!     (`src/ipset_dns.c:92`) and no summary at all (`dns6_done()` in
//!     `src/ipset6_dns.c`);
//!   * `IPs got %lu` is the raw address count, duplicates included
//!     (`dns_request_done()` in `src/ipset_dns.c:118-128`);
//!   * `dns_done()` prints its summary after the last `dns_process_replies()`
//!     call (`src/ipset_dns.c:363-375`), so a `NON-OPTIMIZED` line produced
//!     by those additions is printed before the summary.
//!
//! Every hostname here is answered from `/etc/hosts` or by the numeric short
//! circuit inside `getaddrinfo(3)`, so no case needs resolver traffic. A
//! numeric form such as `0x7f000001` is a hostname, not a literal: the C
//! treats any first token that is not pure `[0-9./]` as a name, and the
//! numeric form resolves to exactly one address, which makes the reply count
//! reproducible on any host. `localhost` answers with two IPv4 addresses on
//! some hosts and one on others, so the cases that use it are compared as
//! multisets of lines with exact counts plus the entry-count invariants,
//! never as fixed bytes.
//!
//! rc and stdout are always byte for byte. Stderr is byte for byte except
//! for the two lines the C derives from its own clock, each removed
//! identically on both sides and then shape-checked: the exit-time timing
//! line (`src/iprange.c:1216-1222`) and
//! `DNS: waiting %lu DNS resolutions to finish...` (`src/ipset_dns.c:348`).
//! The waiting line is printed once per iteration of the loader's
//! `while(pending) { ...; sleep(1); }` loop, so both its in-flight count and
//! how many times it appears (down to zero) depend on how long the resolver
//! took; measured on this host, `-v h2.txt` printed one waiting line in 11 of
//! 12 consecutive C runs and none in the twelfth. Pinning it would encode
//! wall clock as expected output.

#![cfg(unix)]

use std::collections::BTreeMap;
use std::ffi::OsStr;
use std::path::PathBuf;

mod parity_support;
use parity_support::{
    assert_parity_masked, mask_wallclock, raw, reference, run_bounded_in, Scratch, PROGRAM,
};

/// One measured C contract: argv bytes, the expected exit code, stdout
/// bytes, and stderr bytes (`<WALLCLOCK>` marks the timing line; the DNS
/// waiting line is recorded as `<N>` and is removed before comparison).
struct Case {
    label: &'static str,
    argv: &'static [&'static [u8]],
    rc: i32,
    stdout: &'static [u8],
    stderr: &'static [u8],
}

/// The fixture directory the C was measured over. A numeric-form hostname
/// holds exactly one address; `localhost` is used where the answer set is
/// compared as a multiset instead of being pinned.
const FIXTURES: &[(&[u8], &[u8])] = &[
    (b"h1.txt", b"0x7f000001\n"),
    (b"h2.txt", b"0x7f000001\n0x7f000001\n"),
    (b"hA.txt", b"0x7f000001\n"),
    (b"hB.txt", b"0x0A000001\n"),
    (b"hl.txt", b"0x7f000001\n10.0.0.1\n"),
    (b"l1.txt", b"localhost\n"),
    (b"l2.txt", b"localhost\nlocalhost\n"),
    (b"lo4.txt", b"localhost\n"),
    (b"n1.txt", b"ip6-allnodes\n"),
    (b"hn.txt", b"0x7f000001\n0x0A000001\n"),
];

fn fixtures() -> Scratch {
    let dir = Scratch::new("dns-bookkeeping");
    for (name, payload) in FIXTURES {
        dir.file(name, payload);
    }
    dir
}

/// Run the engine with the working directory pinned to the fixture
/// directory, so every name in the pinned text is the relative name the C
/// measured, and mask the timing line.
fn run(dir: &Scratch, argv: &[&[u8]]) -> (i32, Vec<u8>, Vec<u8>) {
    let args: Vec<_> = argv.iter().map(|a| raw(a)).collect();
    let args: Vec<&OsStr> = args.iter().map(|a| a.as_os_str()).collect();
    let run = run_bounded_in(&PathBuf::from(PROGRAM), dir.path(), &args, b"");
    assert!(
        !run.timed_out,
        "the run exceeded the time bound: argv {argv:?}"
    );
    (
        run.code.unwrap_or(-1),
        run.stdout.clone(),
        mask_wallclock(&run.stderr),
    )
}

/// The exact C per-worker notification (`src/ipset_dns.c:91-92`).
const THREAD_CREATION_LINE: &[u8] = b"iprange: Creating new DNS thread";

/// Remove every per-worker notification, from the engine output and from the pinned
/// C text alike. `dns_threads` is incremented outside the requests lock and only ever
/// grows (`src/ipset_dns.c:88-110`), and a worker is started only while more requests
/// are pending than there are workers, so how many workers a batch reaches depends on
/// whether an earlier request had already finished when the next one was queued.
/// Measured on this host: 1 of 29 consecutive C runs of the two-request case reached
/// one worker instead of two.
fn strip_dns_worker_trace(bytes: &[u8]) -> Vec<u8> {
    bytes
        .split(|b| *b == b'\n')
        .filter(|line| *line != THREAD_CREATION_LINE)
        .collect::<Vec<_>>()
        .join(&[b'\n'][..])
}

/// Rewrite only the number of workers the summary reports using, keeping the
/// configured maximum, so the rest of that line - the request count, the failures,
/// the retries and the address count `IPs got` - stays pinned byte for byte.
fn mask_dns_threads_used(bytes: &[u8]) -> Vec<u8> {
    const TAIL: &[u8] = b", threads used ";
    bytes
        .split(|b| *b == b'\n')
        .map(|line| {
            let Some(at) = line.windows(TAIL.len()).position(|w| w == TAIL) else {
                return line.to_vec();
            };
            let after = &line[at + TAIL.len()..];
            let digits = after.iter().take_while(|b| b.is_ascii_digit()).count();
            match after.get(digits..digits + 4) {
                Some(b" of ") if digits > 0 => {
                    let mut out = line[..at + TAIL.len()].to_vec();
                    out.extend_from_slice(b"<T> of ");
                    out.extend_from_slice(&after[digits + 4..]);
                    out
                }
                _ => line.to_vec(),
            }
        })
        .collect::<Vec<_>>()
        .join(&[b'\n'][..])
}

/// The stderr shapes this file retires because the C derives them from the worker
/// pool and the clock rather than from the data: the exit-time timing line, the DNS
/// waiting line, the per-worker notification, and the worker count of the summary.
/// Everything else stays pinned, so this is a whitelist of four lines, not a
/// normalization of the output.
fn canonical_dns_stderr(bytes: &[u8]) -> Vec<u8> {
    mask_dns_threads_used(&strip_dns_worker_trace(&strip_dns_waiting(
        &mask_wallclock(bytes),
    )))
}

/// Parse the C summary `iprange: DNS: made M DNS requests, failed F, retries: R, IPs
/// got G, threads used U of K` (`src/ipset_dns.c:375`) into
/// `[made, failed, retries, got, used, max]`, or `None` when the line is not one.
fn parse_dns_summary(line: &str) -> Option<[u64; 6]> {
    let mut rest = line.strip_prefix("iprange: DNS: made ")?;
    let mut out = [0u64; 5];
    for (i, sep) in [
        " DNS requests, failed ",
        ", retries: ",
        ", IPs got ",
        ", threads used ",
        " of ",
    ]
    .iter()
    .enumerate()
    {
        let (num, tail) = rest.split_once(sep)?;
        out[i] = num.parse().ok()?;
        rest = tail;
    }
    Some([out[0], out[1], out[2], out[3], out[4], rest.parse().ok()?])
}

/// What IS determined about the worker pool, checked on the unmasked text.
///
/// `dns_done()` runs once per input file and `dns_reset_stats()` clears the counters
/// per file (`src/ipset_load.c:386`, `src/ipset_dns.c:42-56`), so a multi-file run
/// prints one summary per file with that file's own request and address counts.
/// `dns_threads` is not among the reset counters, so the worker count is monotonic
/// across the whole run and a later file may report more workers than it requested.
/// The invariants are therefore: every thread-creation line carries the exact C text;
/// every line that reports workers is a well-formed summary; each summary reports at
/// least one worker and no more than the configured maximum; the count never falls
/// between consecutive summaries; it never exceeds the total requests of the run; and
/// the number of thread-creation lines equals the last summary's worker count, because
/// the C increments `dns_threads` exactly where it prints that line.
fn assert_dns_thread_accounting(label: &str, bytes: &[u8]) {
    let text = String::from_utf8_lossy(bytes).into_owned();
    let mut created = 0usize;
    let mut total_requests = 0u64;
    let mut previous_used = 0u64;
    let mut last_used: Option<u64> = None;
    for line in text.lines() {
        if line.as_bytes() == THREAD_CREATION_LINE {
            created += 1;
            continue;
        }
        if line.starts_with("iprange: Creating new DNS thread") {
            panic!("{label}: a DNS thread-creation line is not the C text: {line}");
        }
        if !line.contains(", threads used ") {
            continue;
        }
        let [made, _failed, _retries, _got, used, max] = parse_dns_summary(line)
            .unwrap_or_else(|| panic!("{label}: a DNS summary line is not the C text: {line}"));
        total_requests += made;
        assert!(
            (1..=max).contains(&used),
            "{label}: {used} workers with a maximum of {max}\n{text}"
        );
        assert!(
            used >= previous_used,
            "{label}: the worker count fell from {previous_used} to {used}, but dns_threads only grows\n{text}"
        );
        previous_used = used;
        last_used = Some(used);
    }
    let Some(used) = last_used else {
        assert_eq!(
            created, 0,
            "{label}: thread-creation lines with no DNS summary\n{text}"
        );
        return;
    };
    assert!(
        used <= total_requests,
        "{label}: {used} workers for {total_requests} requests in total\n{text}"
    );
    assert_eq!(
        created, used as usize,
        "{label}: {created} thread-creation lines but the summary reports {used} workers\n{text}"
    );
}

/// The prefix of the C waiting line (`src/ipset_dns.c:348`).
const WAITING_PREFIX: &[u8] = b"iprange: DNS: waiting ";

/// Drop every waiting line, on the engine output and on the pinned C text
/// alike. Presence, count and in-flight value all follow the resolver's
/// elapsed time, so no part of this line is a pinned contract.
fn strip_dns_waiting(bytes: &[u8]) -> Vec<u8> {
    bytes
        .split(|b| *b == b'\n')
        .filter(|line| !line.starts_with(WAITING_PREFIX))
        .collect::<Vec<_>>()
        .join(&[b'\n'][..])
}

/// Verify that every waiting line present in raw stderr has the exact C
/// shape, so removing the line cannot hide a broken one. The pinned C text
/// carries `<N>` in place of the measured count, which is the only accepted
/// deviation.
fn assert_dns_waiting_shape(label: &str, bytes: &[u8]) {
    const TAIL: &[u8] = b" DNS resolutions to finish...";
    for line in bytes.split(|b| *b == b'\n') {
        let Some(rest) = line.strip_prefix(WAITING_PREFIX) else {
            continue;
        };
        let digits = rest.iter().take_while(|b| b.is_ascii_digit()).count();
        let well_formed = match rest.get(digits..) {
            Some(tail) if tail == TAIL => digits > 0,
            Some(b"<N>") => rest == b"<N> DNS resolutions to finish...",
            _ => false,
        };
        assert!(
            well_formed,
            "{label}: a DNS waiting line does not have the C shape: {:?}",
            String::from_utf8_lossy(line)
        );
    }
}

/// Every stderr line with its exact count, so a case can state that each
/// expected line appears exactly once without pinning an order the C itself
/// cannot pin.
fn line_counts(bytes: &[u8]) -> BTreeMap<Vec<u8>, usize> {
    let mut counts = BTreeMap::new();
    for line in bytes.split(|b| *b == b'\n') {
        *counts.entry(line.to_vec()).or_insert(0) += 1;
    }
    counts
}

/// The unsigned integer that follows `key` in the first line holding it, or
/// -1 when no line does.
fn number_after(text: &str, key: &str) -> i64 {
    for line in text.lines() {
        let Some(at) = line.find(key) else { continue };
        let digits: String = line[at + key.len()..]
            .chars()
            .take_while(|c| c.is_ascii_digit())
            .collect();
        if let Ok(value) = digits.parse() {
            return value;
        }
    }
    -1
}

/// The debug line the C prints once per reply address (`src/ipset_dns.c:248`).
fn is_per_address_line(line: &str) -> bool {
    line.starts_with("iprange: DNS: '") && line.contains("' = ")
}

/// C `dns_done()` prints the summary after the last `dns_process_replies()`
/// (`src/ipset_dns.c:363-375`), and the worker prints the per-address lines
/// while it walks the answer list (`src/ipset_dns.c:246-249`) before the
/// loader drains them. The additions of that drain are what can print
/// `NON-OPTIMIZED ...`, so in a correct run the summary line comes after the
/// last `NON-OPTIMIZED` line and after the last per-address line.
fn assert_dns_line_order(label: &str, stderr: &[u8]) {
    let text = String::from_utf8_lossy(stderr).into_owned();
    let lines: Vec<&str> = text.lines().collect();
    let index_of = |pred: fn(&str) -> bool| lines.iter().rposition(|l| pred(l));
    let summary = index_of(|l| l.starts_with("iprange: DNS: made "));
    let non_optimized = index_of(|l| l.starts_with("iprange: NON-OPTIMIZED "));
    let per_address = index_of(is_per_address_line);
    if let (Some(summary), Some(last_add)) = (summary, non_optimized) {
        assert!(
            summary > last_add,
            "{label}: the DNS summary must follow the additions that printed NON-OPTIMIZED\n{text}"
        );
    }
    if let (Some(summary), Some(last_reply)) = (summary, per_address) {
        assert!(
            summary > last_reply,
            "{label}: the DNS summary must follow the per-address reply lines\n{text}"
        );
    }
}

/// The bookkeeping family, byte for byte: the per-address debug line, the
/// entry-count `lines read` totals, and the `Loaded`/`Optimizing` pair that
/// follows from the insertion order. Every expectation is the C's own bytes.
const DNS_BOOKKEEPING: &[Case] = &[
    Case { label: "dns one host v4", argv: &[b"-v", b"h1.txt"], rc: 0,
        stdout: b"127.0.0.1\n", stderr: b"iprange: Loading from h1.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting 1 DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n" }, // engine agrees: True
    Case { label: "dns two files one host each", argv: &[b"-v", b"hA.txt", b"hB.txt"], rc: 0,
        stdout: b"10.0.0.1\n127.0.0.1\n", stderr: b"iprange: Loading from hA.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file hA.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting 1 DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hA.txt\niprange: Loading from hB.txt\niprange: DNS resolution for hostname '0x0A000001' from line 1 of file hB.txt.\niprange: DNS: waiting 1 DNS resolutions to finish...\niprange: DNS: '0x0A000001' = 10.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hB.txt\niprange: Merging hB.txt to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" }, // engine agrees: True
    Case { label: "dns one host v6 localhost", argv: &[b"-6", b"-v", b"l1.txt"], rc: 0,
        stdout: b"::1\n::ffff:127.0.0.1\n", stderr: b"iprange: Loading from l1.txt (IPv6 mode)\niprange: DNS resolution for hostname 'localhost' from line 1 of file l1.txt (IPv6 mode).\niprange: Printing combined ipset (IPv6) with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n" }, // engine agrees: True
    Case { label: "dns one host v6 allnodes", argv: &[b"-6", b"-v", b"n1.txt"], rc: 0,
        stdout: b"ff02::1\n", stderr: b"iprange: Loading from n1.txt (IPv6 mode)\niprange: DNS resolution for hostname 'ip6-allnodes' from line 1 of file n1.txt (IPv6 mode).\niprange: Printing combined ipset (IPv6) with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n" }, // engine agrees: True
    Case { label: "dns host then literal v4", argv: &[b"-v", b"hl.txt"], rc: 0,
        stdout: b"10.0.0.1\n127.0.0.1\n", stderr: b"iprange: Loading from hl.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file hl.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting 1 DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hl.txt\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" }, // engine agrees: True
    Case { label: "dns one host v4 threads 1", argv: &[b"-v", b"--dns-threads", b"1", b"h1.txt"], rc: 0,
        stdout: b"127.0.0.1\n", stderr: b"iprange: Loading from h1.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting 1 DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 1\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n" }, // engine agrees: True
    Case { label: "dns one host v4 binary", argv: &[b"-v", b"--print-binary", b"h1.txt"], rc: 0,
        stdout: b"iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 1\nM<+\x1a\x01\x00\x00\x7f\x01\x00\x00\x7f", stderr: b"iprange: Loading from h1.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting 1 DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\n<WALLCLOCK>\n" }, // engine agrees: True
    Case { label: "dns one host v6 binary header", argv: &[b"-6", b"-v", b"--print-binary", b"l1.txt"], rc: 0,
        stdout: b"iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 2\nbytes 68\nlines 2\nunique ips 2\nM<+\x1a\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x7f\xff\xff\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x7f\xff\xff\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00", stderr: b"iprange: Loading from l1.txt (IPv6 mode)\niprange: DNS resolution for hostname 'localhost' from line 1 of file l1.txt (IPv6 mode).\n" }, // engine agrees: True
];

/// Cases whose reply count is fixed by the numeric short circuit inside
/// `getaddrinfo(3)`: each `0x...` name answers exactly one address per family,
/// so two names answer twice and every line count is pinned.
const DNS_REPLY_COUNTS: &[Case] = &[
    Case { label: "dns same host twice v4", argv: &[b"-v", b"h2.txt"], rc: 0,
        stdout: b"127.0.0.1\n", stderr: b"iprange: Loading from h2.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file h2.txt.\niprange: Creating new DNS thread\niprange: DNS resolution for hostname '0x7f000001' from line 2 of file h2.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: NON-OPTIMIZED h2.txt at line 2, entry 1, last was 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433), new is 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433)\niprange: DNS: made 2 DNS requests, failed 0, retries: 0, IPs got 2, threads used 2 of 5\niprange: Loaded non-optimized h2.txt\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n" }, // engine agrees: True
];

/// Cases whose file holds more than one hostname, so more than one worker pushes
/// onto the single shared reply stack (`src/ipset_dns.c:253-255`,
/// `src/ipset6_dns.c:210-211`) that the adder then drains head-first
/// (`src/ipset_dns.c:275-281`, `src/ipset6_dns.c:223-233`). The interleave decides
/// how many `NON-OPTIMIZED` lines appear and what their `at line N, entry E`
/// fields say, and with it whether the printed set needs optimizing at all, so
/// those two line families are compared only by shape and by their relation to
/// each other. Everything else - including the `totals:` entry count these cases
/// exist to pin - is still a multiset with exact counts against the pinned C text
/// and against the installed C.
const DNS_INTERLEAVE_DEPENDENT: &[Case] = &[
    Case { label: "dns two names one thread", argv: &[b"-v", b"--dns-threads", b"1", b"hn.txt"], rc: 0,
        stdout: b"10.0.0.1\n127.0.0.1\n", stderr: b"iprange: Loading from hn.txt\niprange: DNS resolution for hostname '0x7f000001' from line 1 of file hn.txt.\niprange: Creating new DNS thread\niprange: DNS resolution for hostname '0x0A000001' from line 2 of file hn.txt.\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: '0x0A000001' = 10.0.0.1\niprange: DNS: '0x7f000001' = 127.0.0.1\niprange: NON-OPTIMIZED hn.txt at line 2, entry 1, last was 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433), new is 10.0.0.1 (167772161) - 10.0.0.1 (167772161)\niprange: DNS: made 2 DNS requests, failed 0, retries: 0, IPs got 2, threads used 1 of 1\niprange: Loaded non-optimized hn.txt\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n" },
    Case { label: "dns same host twice v6", argv: &[b"-6", b"-v", b"l2.txt"], rc: 0,
        stdout: b"::1\n::ffff:127.0.0.1\n", stderr: b"iprange: Loading from l2.txt (IPv6 mode)\niprange: DNS resolution for hostname 'localhost' from line 1 of file l2.txt (IPv6 mode).\niprange: DNS resolution for hostname 'localhost' from line 2 of file l2.txt (IPv6 mode).\niprange: NON-OPTIMIZED l2.txt at line 3, entry 2, last was ::ffff:127.0.0.1 - ::ffff:127.0.0.1, new is ::1 - ::1\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 2 entries\n\ntotals: 4 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n" }, // engine agrees: True
];

/// Cases whose answer set the host decides. A second `localhost` IPv4 address
/// comes from the `files`/`myhostname` chain on some hosts only (this host
/// answers two), so pinning the address count would encode one machine's NSS
/// configuration into the contract. For these cases the engine is compared
/// against the installed C as a multiset with exact counts, and its own counts
/// are cross-checked against each other, with no fixed expectation.
const DNS_HOST_DEPENDENT_ANSWERS: &[Case] = &[
    Case { label: "dns localhost v4 duplicate answers", argv: &[b"-v", b"lo4.txt"], rc: 0,
        stdout: b"127.0.0.1\n", stderr: b"iprange: Loading from lo4.txt\niprange: DNS resolution for hostname 'localhost' from line 1 of file lo4.txt.\niprange: Creating new DNS thread\niprange: DNS: waiting <N> DNS resolutions to finish...\niprange: DNS: 'localhost' = 127.0.0.1\niprange: DNS: 'localhost' = 127.0.0.1\niprange: NON-OPTIMIZED lo4.txt at line 2, entry 1, last was 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433), new is 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433)\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 2, threads used 1 of 5\niprange: Loaded non-optimized lo4.txt\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n" }, // engine agrees: True
];

#[test]
fn dns_bookkeeping_matches_c_byte_for_byte() {
    let dir = fixtures();
    for case in DNS_BOOKKEEPING {
        let argv: Vec<_> = case.argv.iter().map(|a| raw(a)).collect();
        let argv: Vec<&OsStr> = argv.iter().map(|a| a.as_os_str()).collect();
        assert_parity_masked(
            case.label,
            dir.path(),
            &argv,
            b"",
            (case.rc, case.stdout, case.stderr),
            &canonical_dns_stderr,
        );
    }
}

/// A hostname that answers with N addresses is added N times, so for a file
/// that holds nothing but hostnames the per-address debug lines, `IPs got`
/// and `totals: N lines read` must all agree, every stderr line the C prints
/// must be present exactly once, and `Loaded non-optimized` plus
/// `Optimizing combined ipset` must appear exactly when a `NON-OPTIMIZED`
/// add did (`src/ipset_load.c:418`, `src/ipset_optimize.c:47`). Deduplicating
/// the reply addresses breaks all three at once.
#[test]
fn dns_reply_addresses_are_counted_once_each() {
    let dir = fixtures();
    for case in DNS_REPLY_COUNTS.iter().chain(DNS_HOST_DEPENDENT_ANSWERS) {
        // The host-decided family records this machine's resolver answer
        // as documentation; its address count is not a contract.
        let pinned = !DNS_HOST_DEPENDENT_ANSWERS
            .iter()
            .any(|other| other.label == case.label);
        let (rc, stdout, stderr) = run(&dir, case.argv);
        assert_eq!(rc, case.rc, "{}: wrong exit code", case.label);
        assert_eq!(stdout, case.stdout, "{}: wrong stdout bytes", case.label);
        assert_dns_waiting_shape("engine", &stderr);
        assert_dns_thread_accounting("engine", &stderr);
        let masked = canonical_dns_stderr(&stderr);
        if pinned {
            assert_eq!(
                masked,
                canonical_dns_stderr(case.stderr),
                "{}: wrong stderr bytes (raw {})",
                case.label,
                String::from_utf8_lossy(&stderr)
            );
        }

        let text = String::from_utf8_lossy(&masked).into_owned();
        let per_address = text.lines().filter(|l| is_per_address_line(l)).count() as i64;
        let got = number_after(&text, "IPs got ");
        let read = number_after(&text, "totals: ");
        assert_eq!(
            (per_address, got, read),
            (got, read, read),
            "{}: one line per reply address, and `IPs got` and `lines read` must equal it\n{text}",
            case.label
        );

        let non_optimized = text.matches("iprange: NON-OPTIMIZED ").count();
        assert_eq!(
            non_optimized,
            text.matches("iprange: Loaded non-optimized ").count(),
            "{}: `Loaded non-optimized` must appear exactly when an add was out of order\n{text}",
            case.label
        );
        assert_eq!(
            non_optimized.min(1),
            text.matches("iprange: Optimizing combined ipset").count(),
            "{}: the printed set must be optimized once when an add was out of order\n{text}",
            case.label
        );
        assert_dns_line_order(case.label, &masked);

        let Some(reference) = reference() else {
            continue;
        };
        let args: Vec<_> = case.argv.iter().map(|a| raw(a)).collect();
        let args: Vec<&OsStr> = args.iter().map(|a| a.as_os_str()).collect();
        let oracle = run_bounded_in(&reference, dir.path(), &args, b"");
        assert!(
            !oracle.timed_out,
            "{}: the C exceeded the time bound",
            case.label
        );
        assert_eq!(
            (oracle.code, oracle.stdout.clone()),
            (Some(case.rc), case.stdout.to_vec()),
            "{}: the C disagrees on rc/stdout",
            case.label
        );
        assert_dns_waiting_shape("the C reference", &oracle.stderr);
        assert_dns_thread_accounting("the C reference", &oracle.stderr);
        let c_masked = canonical_dns_stderr(&oracle.stderr);
        assert_eq!(
            line_counts(&c_masked),
            line_counts(&masked),
            "{}: stderr lines differ from {}\nengine: {}\nC: {}",
            case.label,
            reference.display(),
            String::from_utf8_lossy(&masked),
            String::from_utf8_lossy(&c_masked)
        );
        assert_dns_line_order("the C reference", &c_masked);
    }
}

/// The waiting line of a multi-request batch is the one stderr line whose
/// value the C cannot reproduce: it prints the number of requests still in
/// flight at the instant of the poll (`src/ipset_dns.c:337-348`). Every other
/// line of the reply family is pinned exactly by the tests above. What this
/// test owns is the line's shape: the number of waiting lines may be zero (the
/// resolver can finish before the loader first polls) and may not exceed the
/// number of requests, and every waiting line that does appear must have the
/// exact C text.
#[test]
fn dns_waiting_line_is_the_only_free_shape() {
    let dir = fixtures();
    for case in DNS_REPLY_COUNTS.iter().chain(DNS_HOST_DEPENDENT_ANSWERS) {
        let (_, _, stderr) = run(&dir, case.argv);
        let lines: Vec<Vec<u8>> = stderr.split(|b| *b == b'\n').map(|l| l.to_vec()).collect();
        let waiting: Vec<&Vec<u8>> = lines
            .iter()
            .filter(|l| l.starts_with(WAITING_PREFIX))
            .collect();
        assert_dns_waiting_shape(case.label, &stderr);
        assert_dns_thread_accounting(case.label, &stderr);
        let requests = number_after(&String::from_utf8_lossy(&stderr), "iprange: DNS: made ");
        assert!(
            (waiting.len() as i64) <= requests.max(1),
            "{}: {} waiting lines for {} requests\n{:?}",
            case.label,
            waiting.len(),
            requests,
            lines
        );
    }
}

/// The `NON-OPTIMIZED` text of `src/ipset.h:117` and `src/ipset6.h:102`, with
/// the numeric fields the interleave decides left free.
fn is_non_optimized_shape(line: &str) -> bool {
    let Some(rest) = line.strip_prefix("iprange: NON-OPTIMIZED ") else {
        return false;
    };
    let Some(at) = rest.find(" at line ") else {
        return false;
    };
    let digits = |t: &str| !t.is_empty() && t.chars().all(|c| c.is_ascii_digit());
    let after = &rest[at + " at line ".len()..];
    let Some(n_line) = after.split(", ").next() else {
        return false;
    };
    let Some(rest2) = after.split_once(", entry ").map(|(_, r)| r) else {
        return false;
    };
    let n_entry = rest2.split(", ").next().unwrap_or("");
    let Some(tails) = rest2.split_once(", last was ") else {
        return false;
    };
    let Some((last, new)) = tails.1.split_once(", new is ") else {
        return false;
    };
    let range_pair = |t: &str| t.split_once(" - ").is_some();
    rest[..at].contains('.')
        && digits(n_line)
        && digits(n_entry)
        && range_pair(last.trim_end_matches(','))
        && range_pair(new.trim_end_matches(','))
}

/// Drop the line families whose presence and text the worker interleave decides,
/// so what remains is a stable multiset: the out-of-order-add report, the
/// optimize request it triggers, and the `Loaded` line that reports the flag
/// those adds left behind (`src/ipset_load.c:418`).
fn strip_interleave_trace(bytes: &[u8]) -> Vec<u8> {
    bytes
        .split(|b| *b == b'\n')
        .filter(|line| {
            !line.starts_with(b"iprange: NON-OPTIMIZED ")
                && !line.starts_with(b"iprange: Optimizing combined ipset")
                && !line.starts_with(b"iprange: Loaded optimized ")
                && !line.starts_with(b"iprange: Loaded non-optimized ")
        })
        .collect::<Vec<_>>()
        .join(&[b'\n'][..])
}

/// `Loaded <adj> <file>` (`src/ipset_load.c:418`) reports the optimized flag of the
/// file that was just loaded, and that flag is cleared only by an out-of-order add
/// (`src/ipset.h:107`, `src/ipset6.h:98`), so for a run that loads one file the line
/// must read `non-optimized` exactly when at least one add was out of order. The gate
/// is the one thing here that is not derivable from the adds: the line belongs to the
/// IPv4 loader, and `src/ipset6_load.c` prints no `Loaded` line at all, so a v6 run
/// legitimately shows out-of-order adds with no `Loaded` line to agree with them.
///
/// Returns why the output is inconsistent, or `None` when it is not.
fn loaded_relation_violation(raw: &str, non_optimized: usize) -> Option<String> {
    let loaded: Vec<&str> = raw
        .lines()
        .filter(|l| l.starts_with("iprange: Loaded "))
        .collect();
    if loaded.is_empty() {
        return None;
    }
    let want = usize::from(non_optimized > 0);
    let got = loaded
        .iter()
        .filter(|l| l.starts_with("iprange: Loaded non-optimized "))
        .count();
    if got == want {
        return None;
    }
    Some(format!(
        "`Loaded non-optimized` appeared {got} time(s) for {non_optimized} out-of-order adds (want {want})"
    ))
}

/// The C's other optimization branch for the same input, built from the three sites
/// that decide it: with the adds in order `ipset_added_entry()` returns early and
/// never clears the flag (`src/ipset.h:100-121`), so `src/ipset_load.c:418` prints
/// `optimized` and `ipset_print.c:142-143` does not call `ipset_optimize()` and so
/// prints no `Optimizing` line. Every entry count is unchanged, because `ips->lines`
/// is incremented before the order test (`src/ipset.h:89`).
fn flip_optimization_trace(pin: &[u8]) -> Vec<u8> {
    let mut dropped_non = 0;
    let mut dropped_opt = 0;
    let mut rewrote_loaded = 0;
    let out: Vec<Vec<u8>> = pin
        .split(|b| *b == b'\n')
        .filter_map(|line| {
            if line.starts_with(b"iprange: NON-OPTIMIZED ") {
                dropped_non += 1;
                return None;
            }
            if line.starts_with(b"iprange: Optimizing combined ipset") {
                dropped_opt += 1;
                return None;
            }
            if line.starts_with(b"iprange: Loaded non-optimized ") {
                rewrote_loaded += 1;
                let mut copy = line.to_vec();
                copy.splice(
                    "iprange: Loaded ".len()..("iprange: Loaded ".len() + "non-optimized".len()),
                    "optimized".bytes(),
                );
                return Some(copy);
            }
            Some(line.to_vec())
        })
        .collect();
    // A stale pin must be loud, not silently turned into a different shape.
    assert_eq!(
        dropped_non, 1,
        "the pin no longer holds exactly one out-of-order add"
    );
    assert_eq!(
        dropped_opt, 1,
        "the pin no longer holds exactly one optimize request"
    );
    assert_eq!(
        rewrote_loaded, 1,
        "the pin no longer holds exactly one Loaded line"
    );
    out.join(&[b'\n'][..])
}

/// Why the two-hostname cases cannot be compared line-for-line: the released C
/// produces both optimization branches for the same input, and the two branches are
/// different sets of stderr lines. A comparison that keeps every line therefore
/// rejects a run that is correct, which is what the interleave family exists to stop.
/// Stripping the three trace lines makes the branches identical again while leaving
/// the entry count - the thing these cases pin - fully intact.
#[test]
fn dns_optimization_trace_flip_is_why_the_interleave_family_exists() {
    let case = DNS_INTERLEAVE_DEPENDENT
        .iter()
        .find(|c| c.label == "dns two names one thread")
        .expect("`dns two names one thread` must live in the interleave family");
    let flipped = flip_optimization_trace(case.stderr);

    assert_ne!(
        line_counts(&canonical_dns_stderr(case.stderr)),
        line_counts(&canonical_dns_stderr(&flipped)),
        "if the C's two branches were multiset-identical the case needed no special family"
    );
    assert_eq!(
        line_counts(&strip_interleave_trace(&canonical_dns_stderr(case.stderr))),
        line_counts(&strip_interleave_trace(&canonical_dns_stderr(&flipped))),
        "stripping the trace must make the C's two branches agree"
    );
    let stable =
        String::from_utf8_lossy(&strip_interleave_trace(&canonical_dns_stderr(case.stderr)))
            .into_owned();
    assert!(
        stable
            .lines()
            .any(|l| l.starts_with("totals: 2 lines read")),
        "the entry count the case exists to pin must survive the strip\n{stable}"
    );
    assert!(
        !stable.lines().any(|l| l.starts_with("iprange: Loaded ")),
        "the strip must retire the `Loaded` line, whose text the same branch rewrites\n{stable}"
    );
}

/// The `Loaded` relation must be able to fail, and must stay silent for the IPv6
/// loader that prints no `Loaded` line at all.
#[test]
fn dns_loaded_relation_detects_every_mismatch() {
    let out_of_order = "iprange: NON-OPTIMIZED f at line 2, entry 1, last was 1 - 1, new is 0 - 0\niprange: Loaded non-optimized f\n";
    let in_order = "iprange: Loaded optimized f\n";
    let v6_out_of_order =
        "iprange: NON-OPTIMIZED f at line 3, entry 2, last was ::1 - ::1, new is ::2 - ::2\n";
    assert_eq!(loaded_relation_violation(out_of_order, 1), None);
    assert_eq!(loaded_relation_violation(in_order, 0), None);
    assert_eq!(
        loaded_relation_violation(v6_out_of_order, 1),
        None,
        "v6 prints no Loaded line"
    );
    assert!(
        loaded_relation_violation(out_of_order, 0).is_some(),
        "non-optimized with no out-of-order add"
    );
    assert!(
        loaded_relation_violation(in_order, 1).is_some(),
        "optimized despite an out-of-order add"
    );
    assert!(
        loaded_relation_violation(
            "iprange: Loaded non-optimized f\niprange: Loaded non-optimized f\n",
            1
        )
        .is_some(),
        "two files reported non-optimized for one out-of-order add"
    );
}

/// One hostname per file is resolved by one worker, so the optimization trace is
/// fully determined and stays byte-pinned above. A file with more than one
/// hostname is not, so these cases drop `NON-OPTIMIZED` and `Optimizing` from the
/// comparison and assert instead that every `NON-OPTIMIZED` line that does appear
/// carries the exact C shape, that `Optimizing` appears exactly when at least one
/// add was out of order, and that every other stderr line - the entry count in
/// particular - matches the pinned C text and the installed C as a multiset with
/// exact counts.
#[test]
fn dns_interleaved_batch_pins_counts_not_the_optimization_trace() {
    let dir = fixtures();
    for case in DNS_INTERLEAVE_DEPENDENT {
        let (rc, stdout, stderr) = run(&dir, case.argv);
        assert_eq!(rc, case.rc, "{}: wrong exit code", case.label);
        assert_eq!(stdout, case.stdout, "{}: wrong stdout bytes", case.label);
        assert_dns_waiting_shape("engine", &stderr);
        assert_dns_thread_accounting("engine", &stderr);
        let stable = strip_interleave_trace(&canonical_dns_stderr(&stderr));
        let text = String::from_utf8_lossy(&stable).into_owned();

        for line in text.lines() {
            if line.starts_with("iprange: NON-OPTIMIZED ") {
                panic!(
                    "{}: a NON-OPTIMIZED line survived the strip: {line}",
                    case.label
                );
            }
        }
        let raw_text = String::from_utf8_lossy(&stderr).into_owned();
        let non_optimized: Vec<&str> = raw_text
            .lines()
            .filter(|l| l.starts_with("iprange: NON-OPTIMIZED "))
            .collect();
        for line in &non_optimized {
            assert!(
                is_non_optimized_shape(line),
                "{}: a NON-OPTIMIZED line does not have the C shape: {line}",
                case.label
            );
        }
        let optimizing = raw_text
            .matches("iprange: Optimizing combined ipset")
            .count();
        assert_eq!(
            optimizing,
            usize::from(!non_optimized.is_empty()),
            "{}: `Optimizing` must appear exactly when an add was out of order\n{raw_text}",
            case.label
        );
        if let Some(why) = loaded_relation_violation(&raw_text, non_optimized.len()) {
            panic!("{}: {why}\n{raw_text}", case.label);
        }
        assert!(
            number_after(&text, "totals: ") > 0,
            "{}: the entry-count totals line is missing\n{text}",
            case.label
        );
        assert_eq!(
            line_counts(&stable),
            line_counts(&strip_interleave_trace(&canonical_dns_stderr(case.stderr))),
            "{}: stable stderr lines differ from the measured C multiset\nengine: {text}",
            case.label
        );

        let Some(reference) = reference() else {
            continue;
        };
        let args: Vec<_> = case.argv.iter().map(|a| raw(a)).collect();
        let args: Vec<&OsStr> = args.iter().map(|a| a.as_os_str()).collect();
        let oracle = run_bounded_in(&reference, dir.path(), &args, b"");
        assert!(
            !oracle.timed_out,
            "{}: the C exceeded the time bound",
            case.label
        );
        assert_eq!(
            (oracle.code, oracle.stdout.clone()),
            (Some(case.rc), case.stdout.to_vec()),
            "{}: the C disagrees on rc/stdout",
            case.label
        );
        assert_dns_waiting_shape("the C reference", &oracle.stderr);
        assert_dns_thread_accounting("the C reference", &oracle.stderr);
        let c_stable = strip_interleave_trace(&canonical_dns_stderr(&oracle.stderr));
        assert_eq!(
            line_counts(&c_stable),
            line_counts(&stable),
            "{}: stable stderr lines differ from {}\nC: {}",
            case.label,
            reference.display(),
            String::from_utf8_lossy(&c_stable)
        );
    }
}

/// The pinned cases only mean something if the fixture directory they run in
/// holds the measured bytes.
#[test]
fn dns_bookkeeping_fixture_set_is_complete() {
    let dir = fixtures();
    for (name, payload) in FIXTURES {
        let path = dir.path().join(raw(name));
        assert!(
            path.is_file(),
            "fixture {} is missing",
            String::from_utf8_lossy(name)
        );
        assert_eq!(
            std::fs::read(&path).unwrap(),
            *payload,
            "fixture {} does not hold the measured bytes",
            String::from_utf8_lossy(name)
        );
    }
}
