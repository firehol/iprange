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
pub use options::{Mode, Options, PrintMode, SourceKind, SourceSpec};
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
    // C iprange6_run() re-scans the whole argv whenever -6 is
    // present, so --min-prefix/--prefixes apply to the IPv6 prefix
    // array regardless of position (see those branches below).
    let has_v6 = args.iter().any(|a| a == "-6" || a == "--ipv6");

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
                // C usage() substitutes the program name and the
                // current dns-threads maximum into the format text.
                print!(
                    "{}",
                    USAGE
                        .replace("%s", "iprange")
                        .replace("%d", &options.dns_threads.to_string())
                );
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
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                // C bounds the percentage at SIZE_MAX - 100 so the
                // stored factor (100 + N) cannot wrap.
                let n = parse_size(
                    &arg,
                    &value,
                    "It must be a non-negative integer percentage.",
                    u64::MAX - 100,
                );
                options.mode = Mode::Reduce;
                options.reduce_factor = 100 + n;
            }
            "--ipset-reduce-entries" | "--reduce-entries" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                let n = parse_size(
                    &arg,
                    &value,
                    "It must be a non-negative integer.",
                    u64::MAX,
                );
                options.mode = Mode::Reduce;
                options.reduce_entries = n;
            }
            "--min-prefix" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                // C main() validates with the family active at this
                // argv position; iprange6_run() re-applies the
                // option to the IPv6 array whenever -6 is present.
                match options.family {
                    Family::V4 => {
                        let v = parse_number(
                            &arg, &value,
                            "It must be between 1 and 32.", 1, 32,
                        ) as usize;
                        for slot in 0..v {
                            options.prefix4_enabled[slot] = false;
                        }
                        if has_v6 {
                            for slot in 0..v {
                                options.prefix6_enabled[slot] = false;
                            }
                        }
                    }
                    Family::V6 => {
                        let v = parse_number(
                            &arg, &value,
                            "It must be between 1 and 128.", 1, 128,
                        ) as usize;
                        for slot in 0..v {
                            options.prefix6_enabled[slot] = false;
                        }
                    }
                }
            }
            "--prefixes" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                // C main() parses with strtol over comma/space
                // separated tokens; iprange6_run() re-applies the
                // option to the IPv6 array whenever -6 is present
                // (with the IPv6 1..128 bound at that phase). The
                // tokenizer is strtol-exact: whitespace, signs and
                // empty tokens behave like the C loop.
                match parse_prefix_list(&value, options.family) {
                    Ok(list) => {
                        for slot in 0..33 {
                            if slot < 32 && !list.contains(&slot) {
                                options.prefix4_enabled[slot] = false;
                            }
                        }
                        if has_v6 {
                            for slot in 0..129 {
                                if slot < 128 && !list.contains(&slot) {
                                    options.prefix6_enabled[slot] = false;
                                }
                            }
                        }
                    }
                    Err(message) => {
                        eprintln!("{message}");
                        std::process::exit(1);
                    }
                }
            }
            "--default-prefix" | "-p" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                if options.family == Family::V6 {
                    // C: IPv6 always uses /128; the value is
                    // consumed and not validated.
                    continue;
                }
                let v = parse_number(
                    &arg, &value,
                    "It must be between 0 and 32.", 0, 32,
                ) as u32;
                options.default_prefix = v;
            }
            "--dont-fix-network" => options.dont_fix_network = true,
            "--print-prefix" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                options.print.prefix_ips = value.clone();
                options.print.prefix_nets = value;
            }
            "--print-suffix" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                options.print.suffix_ips = value.clone();
                options.print.suffix_nets = value;
            }
            "--print-prefix-ips" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                options.print.prefix_ips = value;
            }
            "--print-suffix-ips" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                options.print.suffix_ips = value;
            }
            "--print-prefix-nets" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                options.print.prefix_nets = value;
            }
            "--print-suffix-nets" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                options.print.suffix_nets = value;
            }
            "--print-ranges" | "-j" => options.print.mode = PrintMode::Ranges,
            "--print-single-ips" | "-1" => options.print.mode = PrintMode::SingleIps,
            "--print-binary" => options.print.mode = PrintMode::Binary,
            "--quiet" => options.quiet = true,
            "--header" => options.header = true,
            "-v" => options.debug = true,
            "--dns-threads" => {
                let Some(value) = take_value(&mut i, args, &arg, &mut options) else {
                    continue;
                };
                let v = parse_number(
                    &arg, &value,
                    "It must be an integer greater than or equal to 1.",
                    1, i32::MAX as i64,
                ) as u32;
                options.dns_threads = v;
            }
            "--dns-silent" => options.dns_silent = true,
            "--dns-progress" => options.dns_progress = true,
            "as" => {
                if i + 1 >= args.len() {
                    // Trailing keyword: C's branch needs a next arg,
                    // so "as" falls through to the file branch.
                    options.sources.push(SourceSpec {
                        kind: SourceKind::Path,
                        arg: Some(arg.to_owned()),
                        label: None,
                    });
                } else if options.sources.is_empty() {
                    // No prior ipset: C ignores the keyword and the
                    // following token is an ordinary input (processed
                    // on the next iteration).
                } else if let Some(last) = options.sources.last_mut() {
                    let name = next_value(&mut i);
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
    // No sources at all: read stdin (C behavior for both families;
    // the IPv4 twin prints one debug note first).
    if options.sources.is_empty() {
        if options.debug && options.family == Family::V4 {
            eprintln!("iprange: No input files provided, reading from stdin");
        }
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
/// used for the reduce options (bounds checked per the C option;
/// out-of-bounds values print the C message).
fn parse_size(option: &str, value: &str, expected: &str, max: u64) -> u64 {
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
    if parsed > max {
        invalid_option_value(option, value, expected);
    }
    parsed
}

/// Consume the value of an argv option exactly like the C scan: a
/// value-less trailing option falls through to the file-input branch
/// (the option token becomes a load path), returning None; otherwise
/// the next token is returned.
fn take_value(
    i: &mut usize,
    args: &[String],
    arg: &str,
    options: &mut Options,
) -> Option<String> {
    if *i + 1 >= args.len() {
        options.sources.push(SourceSpec {
            kind: SourceKind::Path,
            arg: Some(arg.to_owned()),
            label: None,
        });
        None
    } else {
        *i += 1;
        Some(args[*i].clone())
    }
}

/// C `--prefixes` tokenizer (strtol base 10 over comma/space
/// separated tokens): leading whitespace and signs are consumed by
/// strtol, empty tokens yield 0 (invalid), and the first
/// out-of-range value aborts with the exact C message. Returns the
/// allowed prefixes or the full C diagnostic.
fn parse_prefix_list(
    text: &str,
    family: Family,
) -> Result<Vec<usize>, String> {
    let (max, invalid_text) = match family {
        Family::V4 => (32usize, "Only prefixes from 1 to 32 can be set (32 is always enabled)."),
        Family::V6 => (128usize, "Only prefixes from 1 to 128 can be set."),
    };
    let mut allowed: Vec<usize> = Vec::new();
    let bytes = text.as_bytes();
    let mut pos = 0usize;
    loop {
        if pos >= bytes.len() {
            break;
        }
        // strtol: skip leading whitespace, optional sign.
        while pos < bytes.len() && bytes[pos].is_ascii_whitespace() {
            pos += 1;
        }
        let start = pos;
        if pos < bytes.len() && (bytes[pos] == b'-' || bytes[pos] == b'+') {
            pos += 1;
        }
        let digits = pos;
        while pos < bytes.len() && bytes[pos].is_ascii_digit() {
            pos += 1;
        }
        let mut value: i64 = 0;
        if pos > digits {
            for &b in &bytes[digits..pos] {
                value = value
                    .checked_mul(10)
                    .and_then(|v| v.checked_add((b - b'0') as i64))
                    .unwrap_or(i64::MAX);
            }
            if bytes[start] == b'-' {
                value = -value;
            }
        }
        if pos == start {
            // No digits at all (empty token, comma, or junk): the C
            // strtol yields 0 and the bounds check rejects it.
            value = 0;
        }
        if value <= 0 || value as usize > max {
            return Err(format!(
                "iprange: {invalid_text} {value} is invalid."
            ));
        }
        allowed.push(value as usize);
        // Consume one separator (comma or space) like the C loop.
        if pos < bytes.len() && (bytes[pos] == b',' || bytes[pos] == b' ') {
            pos += 1;
        }
        // The C loop also stops on a second consecutive separator
        // (strtol on the comma yields 0 -> invalid).
        if pos < bytes.len() && (bytes[pos] == b',' || bytes[pos] == b' ') {
            return Err(format!("iprange: {invalid_text} 0 is invalid."));
        }
    }
    Ok(allowed)
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
