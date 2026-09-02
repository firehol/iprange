//! Released legacy `iprange` surface (SOW-0028 delivery step 3).
//!
//! This module implements the complete released legacy grammar,
//! ephemeral interval algebra, formatting, DNS, file expansion,
//! binary compatibility, diagnostics, and exit codes. It contains no
//! v4 persistence logic. Legacy mode runs the exact C oracle
//! semantics: one authoritative generic core (range/ops/print) with
//! IPv4/IPv6 family hooks.

mod binary;
pub mod dns;
mod family;
mod ipv4;
mod ipv6;
mod ops;
mod options;
mod parse;
mod print;
mod range;
mod usage;

use std::time::Instant;

use family::{Family, FamilyImpl};
pub use options::{Mode, Options, SourceKind, SourceSpec};
pub use range::IpNum;

use crate::legacy::usage::USAGE;

/// The version string reported by `--version`: the fresh-configure
/// tree version (configure.ac), matching a current C oracle build.
const VERSION: &str = "2.1.2_master";

/// Legacy entry point. `--jsonrpc` mixed with other arguments is an
/// invalid JSON-RPC startup and must not fall back here silently;
/// main.rs already rejects that combination before calling us.
pub fn run(args: &[String]) -> i32 {
    // The C binary dies of SIGPIPE on a closed stdout; the Rust
    // runtime ignores SIGPIPE by default. Restore the default
    // disposition for legacy mode only (the JSON-RPC transport keeps
    // the runtime default and reports write errors as fatal). The
    // concept does not exist on Windows (no SIGPIPE).
    #[cfg(unix)]
    unsafe {
        libc::signal(libc::SIGPIPE, libc::SIG_DFL);
    }

    let started = Instant::now();
    let mut options = Options::default();

    // One-pass argv scan: flags are positional and the last mode flag
    // wins; file arguments load as their own ipsets in order; `as NAME`
    // renames the last source.
    let mut i = 0usize;
    while i < args.len() {
        let arg = args[i].as_str();
        let next_value = |i: &mut usize| -> String {
            *i += 1;
            args.get(*i).cloned().unwrap_or_default()
        };
        match arg {
            "-h" | "--help" => {
                print!("{}", USAGE.replace("%s", "iprange"));
                return 0;
            }
            "--version" => {
                version();
                return 0;
            }
            "--has-compare" | "--has-reduce" => {
                eprintln!("yes, compare and reduce is present.");
                return 0;
            }
            "--has-filelist-loading" | "--has-directory-loading" => {
                eprintln!("yes, @filename and @directory support is present.");
                return 0;
            }
            "--has-ipv6" => {
                eprintln!("yes, IPv6 support is present.");
                return 0;
            }
            "-4" | "--ipv4" => options.family = Family::V4,
            "-6" | "--ipv6" => options.family = Family::V6,
            "--optimize" | "--combine" | "--merge" | "--union" | "--union-all" | "-J" => {
                options.mode = Mode::Merge
            }
            "--common" | "--intersect" | "--intersect-all" => {
                options.mode = Mode::Common;
            }
            "--exclude-next" | "--except" | "--complement-next" | "--complement" => {
                require_prior_file(&options, "--except");
                options.mode = Mode::ExcludeNext;
                set_group_b(&mut options);
            }
            "--diff" | "--diff-next" => {
                require_prior_file(&options, "--diff");
                options.mode = Mode::Diff;
                set_group_b(&mut options);
            }
            "--compare" => options.mode = Mode::Compare,
            "--compare-first" => options.mode = Mode::CompareFirst,
            "--compare-next" => {
                require_prior_file(&options, "--compare-next");
                options.mode = Mode::CompareNext;
                set_group_b(&mut options);
            }
            "--count-unique" | "-C" => options.mode = Mode::CountUnique,
            "--count-unique-all" => options.mode = Mode::CountUniqueAll,
            "--ipset-reduce" | "--reduce-factor" => {
                let v = next_value(&mut i);
                let n = parse_size(&arg, &v, "positive integer");
                options.mode = Mode::Reduce;
                options.reduce_factor = 100 + n;
            }
            "--ipset-reduce-entries" | "--reduce-entries" => {
                let v = next_value(&mut i);
                let n = parse_size(&arg, &v, "positive integer");
                options.mode = Mode::Reduce;
                options.reduce_entries = n;
            }
            "--min-prefix" => {
                let v = next_value(&mut i);
                let (min, max, n) = match options.family {
                    Family::V4 => (1i64, 32i64, 33usize),
                    Family::V6 => (1i64, 128i64, 129usize),
                };
                let v = parse_number(&arg, &v, "prefix length", min, max) as usize;
                for slot in 0..n {
                    if slot < v {
                        match options.family {
                            Family::V4 => options.prefix4_enabled[slot] = false,
                            Family::V6 => options.prefix6_enabled[slot] = false,
                        }
                    }
                }
            }
            "--prefixes" => {
                let v = next_value(&mut i);
                let (max, n) = match options.family {
                    Family::V4 => (32usize, 33usize),
                    Family::V6 => (128usize, 129usize),
                };
                let mut slots: Vec<usize> = Vec::new();
                for part in v.split(|c: char| c == ',' || c == ' ' || c == '\t') {
                    if part.is_empty() {
                        continue;
                    }
                    let p = parse_number(&arg, part, "prefix list", 1, max as i64) as usize;
                    slots.push(p);
                }
                for slot in 0..n {
                    if slot < max && !slots.contains(&slot) {
                        match options.family {
                            Family::V4 => options.prefix4_enabled[slot] = false,
                            Family::V6 => options.prefix6_enabled[slot] = false,
                        }
                    }
                }
            }
            "--default-prefix" | "-p" => {
                let v = next_value(&mut i);
                let v = parse_number(&arg, &v, "0..32", 0, 32) as u32;
                options.default_prefix = v;
            }
            "--dont-fix-network" => options.dont_fix_network = true,
            "--print-prefix" => {
                let v = next_value(&mut i);
                options.print.prefix = v.clone();
                options.print.prefix_ips = v.clone();
                options.print.prefix_nets = v;
            }
            "--print-suffix" => {
                let v = next_value(&mut i);
                options.print.suffix = v.clone();
                options.print.suffix_ips = v.clone();
                options.print.suffix_nets = v;
            }
            "--print-prefix-ips" => {
                options.print.prefix_ips = next_value(&mut i);
            }
            "--print-suffix-ips" => {
                options.print.suffix_ips = next_value(&mut i);
            }
            "--print-prefix-nets" => {
                options.print.prefix_nets = next_value(&mut i);
            }
            "--print-suffix-nets" => {
                options.print.suffix_nets = next_value(&mut i);
            }
            "--print-ranges" | "-j" => options.print.ranges = true,
            "--print-single-ips" | "-1" => options.print.single_ips = true,
            "--print-binary" => options.print.binary = true,
            "--quiet" => options.quiet = true,
            "--header" => options.header = true,
            "-v" => options.debug = true,
            "--dns-threads" => {
                let v = next_value(&mut i);
                let v = parse_number(&arg, &v, "positive integer", 1, i32::MAX as i64) as u32;
                options.dns_threads = v;
            }
            "--dns-silent" => options.dns_silent = true,
            "--dns-progress" => options.dns_progress = true,
            "as" => {
                let name = next_value(&mut i);
                if let Some(last) = options.sources.last_mut() {
                    last.label = Some(name);
                }
            }
            _ => {
                // Everything else is an input path: a file, `-` for
                // stdin, `@file` list, or `@dir` directory (the @
                // destination is classified on load).
                let (kind, path) = if arg == "-" {
                    (SourceKind::Path, None)
                } else if let Some(rest) = arg.strip_prefix('@') {
                    (SourceKind::FileList, Some(rest.to_owned()))
                } else {
                    (SourceKind::Path, Some(arg.to_owned()))
                };
                options.sources.push(SourceSpec {
                    kind,
                    arg: path,
                    label: None,
                });
            }
        }
        i += 1;
    }
    // No sources at all: read stdin (C behavior for both families).
    if options.sources.is_empty() {
        options.sources.push(SourceSpec {
            kind: SourceKind::Path,
            arg: None,
            label: None,
        });
    }

    dispatch(&options, started)
}

/// The first positional operator splits the sources: everything after
/// it is group B (exclude/diff/compare-next semantics).
fn set_group_b(options: &mut Options) {
    if options.group_b.is_none() {
        options.group_b = Some(options.sources.len());
    }
}

fn dispatch(options: &Options, started: Instant) -> i32 {
    match options.family {
        Family::V4 => run_family::<u32>(options, started),
        Family::V6 => run_family::<u128>(options, started),
    }
}

/// Full pipeline for one family: load -> operate -> print.
///
/// The IPv4 C twin prints one `-v` timing line at exit (read = argv
/// scan + file loading, think = operation, speak = printing); the
/// IPv6 twin has no such line.
fn run_family<F: FamilyImpl>(options: &Options, started: Instant) -> i32 {
    let load_done = Instant::now();
    let mut loaded = match parse::load_all::<F>(options) {
        Ok(loaded) => loaded,
        Err(message) => {
            eprintln!("{message}");
            return 1;
        }
    };
    let think_done = Instant::now();
    let ret = ops::execute::<F>(options, &mut loaded);
    let stop = Instant::now();
    if options.debug && options.family == Family::V4 {
        eprintln!(
            "completed in {:.5} seconds (read {:.5} + think {:.5} + speak {:.5})",
            stop.duration_since(started).as_secs_f64(),
            load_done.duration_since(started).as_secs_f64(),
            think_done.duration_since(load_done).as_secs_f64(),
            stop.duration_since(think_done).as_secs_f64(),
        );
    }
    ret
}

fn version() {
    print!(
        "iprange {VERSION}\n\
         Copyright (C) 2015-2026 Costa Tsaousis for FireHOL (Refactored and extended)\n\
         Copyright (C) 2004 Paul Townsend (Adapted)\n\
         Copyright (C) 2003 Gabriel L. Somlo (Original)\n\
         \n\
         License: GPLv2+: GNU GPL version 2 or later <http://gnu.org/licenses/gpl2.html>.\n\
         This program comes with ABSOLUTELY NO WARRANTY; This is free software, and\n\
         you are welcome to redistribute it under certain conditions;\n\
         See COPYING distributed in the source for details.\n"
    );
}

fn invalid_option_value(option: &str, value: &str, expected: &str) -> ! {
    eprintln!("iprange: Invalid value '{value}' for {option}. {expected}");
    std::process::exit(1);
}

/// Strict full-string decimal parse with i64 bounds (C strtol
/// semantics: sign, empty, and trailing junk rejected).
fn parse_number(option: &str, value: &str, expected: &str, min: i64, max: i64) -> i64 {
    let bytes = value.as_bytes();
    if bytes.is_empty() || !bytes[0].is_ascii_digit() {
        invalid_option_value(option, value, expected);
    }
    let mut parsed: i64 = 0;
    for &b in bytes {
        if !b.is_ascii_digit() {
            invalid_option_value(option, value, expected);
        }
        parsed = parsed
            .checked_mul(10)
            .and_then(|v| v.checked_add((b - b'0') as i64))
            .unwrap_or_else(|| invalid_option_value(option, value, expected));
    }
    if parsed < min || parsed > max {
        invalid_option_value(option, value, expected);
    }
    parsed
}

/// Strict full-string unsigned decimal parse (C strtoull semantics),
/// used for the reduce options (0 ..= u64::MAX).
fn parse_size(option: &str, value: &str, expected: &str) -> u64 {
    let bytes = value.as_bytes();
    if bytes.is_empty() || !bytes[0].is_ascii_digit() {
        invalid_option_value(option, value, expected);
    }
    let mut parsed: u64 = 0;
    for &b in bytes {
        if !b.is_ascii_digit() {
            invalid_option_value(option, value, expected);
        }
        parsed = parsed
            .checked_mul(10)
            .and_then(|v| v.checked_add((b - b'0') as u64))
            .unwrap_or_else(|| invalid_option_value(option, value, expected));
    }
    parsed
}

/// IPv4 positional operators require a prior loaded ipset (C error
/// text; the IPv6 path has no such check).
fn require_prior_file(options: &Options, option: &str) {
    if options.family == Family::V6 || !options.sources.is_empty() {
        return;
    }
    eprintln!("iprange: An ipset is needed before {option}");
    std::process::exit(1);
}
