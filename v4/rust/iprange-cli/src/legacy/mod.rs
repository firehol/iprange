//! Released legacy `iprange` surface (SOW-0028 delivery step 3).
//!
//! This module implements the complete released legacy grammar,
//! ephemeral interval algebra, formatting, DNS, file expansion,
//! binary compatibility, diagnostics, and exit codes. It contains no
//! v4 persistence logic. Legacy mode runs the exact C oracle
//! semantics: one authoritative generic core (range/ops/print) with
//! IPv4/IPv6 family hooks.
//!
//! Public surface (the Go port must reproduce these names):
//! [`run`], [`Mode`], [`Options`], [`PrintMode`], [`SourceKind`],
//! [`SourceSpec`], [`IpNum`], and [`dns`] (`Resolver`, `DnsError`).
//! Everything else in this module is crate-internal.

pub(crate) mod argv;
mod binary;
mod diag;
pub mod dns;
mod family;
mod ipv4;
mod ipv6;
mod ops;
mod options;
mod parse;
mod print;
mod range;
#[cfg(test)]
mod tests;
mod usage;

use std::ffi::{OsStr, OsString};
use std::path::PathBuf;
use std::time::Instant;

use argv::bytes as arg_bytes;
use argv::text as arg_text;
use family::{Family, FamilyImpl};
pub use options::{Mode, Options, PrintMode, SourceKind, SourceSpec};
pub use range::IpNum;

/// The version string reported by `--version`: the fresh-configure
/// tree version (configure.ac), matching a current C oracle build.
const VERSION: &str = "2.1.2_master";

/// Legacy entry point. `--jsonrpc` mixed with other arguments is an
/// invalid JSON-RPC startup and must not fall back here silently;
/// main.rs already rejects that combination before calling us.
///
/// `args` are the raw argv bytes. A POSIX argument may hold bytes that
/// are not valid UTF-8, so option tokens are recognised through a
/// lossy view that no such argument can match, while every value and
/// path keeps the original bytes.
pub fn run(prog: &OsStr, args: &[OsString]) -> i32 {
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
    let has_v6 = args
        .iter()
        .any(|a| arg_text(a) == "-6" || arg_text(a) == "--ipv6");

    // One-pass argv scan: flags are positional and the last mode flag
    // wins; file arguments load as their own ipsets in order; `as NAME`
    // renames the last source.
    let mut i = 0usize;
    while i < args.len() {
        let arg_os = args[i].as_os_str();
        // `arg` identifies the option tokens only (see
        // `legacy::argv::text`); values, names and paths always use
        // `arg_os`, never `arg`.
        let arg = arg_text(arg_os);
        let next_value = |i: &mut usize| -> OsString {
            *i += 1;
            args.get(*i).cloned().unwrap_or_default()
        };
        match &*arg {
            "-h" | "--help" => {
                // C usage(argv[0]) substitutes the invocation name
                // and the current dns-threads maximum into the format
                // text (the full argv[0], not the basename).
                usage::print(prog, options.dns_threads);
                return 0;
            }
            "--version" => {
                version();
                return 0;
            }
            "--has-compare" | "--has-reduce" => {
                crate::legacy::argv::eprint_line(&format!("yes, compare and reduce is present."));
                return 0;
            }
            "--has-filelist-loading" | "--has-directory-loading" => {
                crate::legacy::argv::eprint_line(&format!("yes, @filename and @directory support is present."));
                return 0;
            }
            "--has-ipv6" => {
                crate::legacy::argv::eprint_line(&format!("yes, IPv6 support is present."));
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
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
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
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                let n = parse_size(&arg, &value, "It must be a non-negative integer.", u64::MAX);
                options.mode = Mode::Reduce;
                options.reduce_entries = n;
            }
            "--min-prefix" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                // C main() validates with the family active at this
                // argv position; iprange6_run() re-applies the
                // option to the IPv6 array whenever -6 is present.
                match options.family {
                    Family::V4 => {
                        let v = parse_number(&arg, &value, "It must be between 1 and 32.", 1, 32)
                            as usize;
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
                        let v = parse_number(&arg, &value, "It must be between 1 and 128.", 1, 128)
                            as usize;
                        for slot in 0..v {
                            options.prefix6_enabled[slot] = false;
                        }
                    }
                }
            }
            "--prefixes" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                // C main() parses with strtol over comma/space
                // separated tokens; iprange6_run() re-applies the
                // option to the IPv6 array whenever -6 is present
                // (with the IPv6 1..128 bound at that phase). The
                // tokenizer is strtol-exact: whitespace, signs and
                // empty tokens behave like the C loop.
                match parse_prefix_list(&value, options.family, options.debug) {
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
                        crate::legacy::argv::eprint_line(&format!("{message}"));
                        std::process::exit(1);
                    }
                }
            }
            "--default-prefix" | "-p" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                // C validates only when the family active at this argv
                // position is IPv4 (`src/iprange.c:564`: with `-6` seen
                // earlier the value is consumed and ignored, since the
                // IPv6 parser always uses /128). The value was already
                // consumed by `take_value`, so control must reach the
                // loop's `i += 1`; a `continue` here would re-read the
                // value as an input argument.
                if options.family == Family::V4 {
                    let v =
                        parse_number(&arg, &value, "It must be between 0 and 32.", 0, 32) as u32;
                    options.default_prefix = v;
                }
            }
            "--dont-fix-network" => options.dont_fix_network = true,
            "--print-prefix" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                options.print.prefix_ips = value.clone();
                options.print.prefix_nets = value;
            }
            "--print-suffix" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                options.print.suffix_ips = value.clone();
                options.print.suffix_nets = value;
            }
            "--print-prefix-ips" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                options.print.prefix_ips = value;
            }
            "--print-suffix-ips" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                options.print.suffix_ips = value;
            }
            "--print-prefix-nets" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                options.print.prefix_nets = value;
            }
            "--print-suffix-nets" => {
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
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
                let Some(value) = take_value(&mut i, args, arg_os, &mut options) else {
                    // Value-less trailing option: already classified.
                    break;
                };
                let v = parse_number(
                    &arg,
                    &value,
                    "It must be an integer greater than or equal to 1.",
                    1,
                    i32::MAX as i64,
                ) as u32;
                options.dns_threads = v;
            }
            "--dns-silent" => options.dns_silent = true,
            "--dns-progress" => options.dns_progress = true,
            "as" => {
                if i + 1 >= args.len() {
                    // Trailing keyword: C's branch needs a next arg,
                    // so "as" falls through to the input branch (it is
                    // not dash-prefixed, so both twins load it).
                    if let Some(spec) = input_source(options.family, arg_os) {
                        options.sources.push(spec);
                    }
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
                if let Some(spec) = input_source(options.family, arg_os) {
                    options.sources.push(spec);
                }
            }
        }
        i += 1;
    }
    // No sources at all: read stdin (C behavior for both families;
    // the IPv4 twin prints one debug note first).
    if options.sources.is_empty() {
        if options.debug && options.family == Family::V4 {
            crate::legacy::argv::eprint_line(&format!("iprange: No input files provided, reading from stdin"));
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
            // The load-path error currency carries the name bytes, so
            // the line goes out as bytes (see `legacy::diag`).
            message.emit();
            return 1;
        }
    };
    let think_done = Instant::now();
    let ret = ops::execute::<F>(options, &mut loaded);
    let stop = Instant::now();
    if options.debug && options.family == Family::V4 {
        crate::legacy::argv::eprint_line(&format!(
            "completed in {:.5} seconds (read {:.5} + think {:.5} + speak {:.5})",
            stop.duration_since(started).as_secs_f64(),
            load_done.duration_since(started).as_secs_f64(),
            think_done.duration_since(load_done).as_secs_f64(),
            stop.duration_since(think_done).as_secs_f64(),
        ));
    }
    ret
}

fn version() {
    use std::io::Write;
    let mut stdout = std::io::stdout().lock();
    let _ = stdout.write_all(
        format!(
            "iprange {VERSION}\n\
             Copyright (C) 2015-2026 Costa Tsaousis for FireHOL (Refactored and extended)\n\
             Copyright (C) 2004 Paul Townsend (Adapted)\n\
             Copyright (C) 2003 Gabriel L. Somlo (Original)\n\
             \n\
             License: GPLv2+: GNU GPL version 2 or later <http://gnu.org/licenses/gpl2.html>.\n\
             This program comes with ABSOLUTELY NO WARRANTY; This is free software, and\n\
             you are welcome to redistribute it under certain conditions;\n\
             See COPYING distributed in the source for details.\n"
        )
        .as_bytes(),
    );
}

fn invalid_option_value(option: &str, value: &OsStr, expected: &str) -> ! {
    // The value is argv: C echoes its bytes verbatim, so the line is
    // assembled from bytes instead of `eprintln!`.
    let value = arg_bytes(value);
    argv::eprint_raw(&[
        b"iprange: Invalid value '",
        &value,
        b"' for ",
        option.as_bytes(),
        b". ",
        expected.as_bytes(),
    ]);
    std::process::exit(1);
}

/// C `strtol(s, &end, 10)` over the whole of `bytes`: leading
/// whitespace, an optional sign, decimal digits, then saturation at the
/// `long` bounds.
///
/// Returns the value, the number of bytes consumed, and whether the
/// magnitude did not fit a `long` (C reports that as `ERANGE`). With no
/// digits it returns `(0, 0, false)`, which is the C case of an
/// `endptr` left at the start of the string.
///
/// The `long` here is the LP64 `long` of the released binary: 64 bits.
/// `--prefixes` casts this value to `int` before testing it, which is
/// where the int-truncation behaviour of that option comes from.
fn strtol10(bytes: &[u8]) -> (i64, usize, bool) {
    let mut pos = 0usize;
    while pos < bytes.len() && bytes[pos].is_ascii_whitespace() {
        pos += 1;
    }
    let negative = pos < bytes.len() && bytes[pos] == b'-';
    if pos < bytes.len() && (bytes[pos] == b'-' || bytes[pos] == b'+') {
        pos += 1;
    }
    let digits = pos;
    // glibc detects overflow against the signed range the value will
    // take, so -9223372036854775808 (LONG_MIN) is exact and only
    // LONG_MIN - 1 saturates.
    let limit: u64 = if negative {
        i64::MAX as u64 + 1
    } else {
        i64::MAX as u64
    };
    let mut magnitude: u64 = 0;
    let mut too_big = false;
    while pos < bytes.len() && bytes[pos].is_ascii_digit() {
        let digit = u64::from(bytes[pos] - b'0');
        // Keep consuming so the byte count stays exact, but stop
        // accumulating once the value cannot fit a `long`.
        if too_big || magnitude > limit / 10 || (magnitude == limit / 10 && digit > limit % 10) {
            too_big = true;
        } else {
            magnitude = magnitude * 10 + digit;
        }
        pos += 1;
    }
    if pos == digits {
        return (0, 0, false);
    }
    if too_big {
        return (if negative { i64::MIN } else { i64::MAX }, pos, true);
    }
    if negative {
        // `magnitude` is at most 2^63: `as i64` maps it onto the
        // negative side, and wrapping_neg is exact across that range
        // (an ordinary negation of `i64::MIN` would overflow).
        return (magnitude.wrapping_neg() as i64, pos, false);
    }
    (magnitude as i64, pos, false)
}

/// C `parse_long_option_or_die()` (`src/iprange.c:452-463`), the parser
/// behind `--min-prefix`, `--default-prefix`/`-p` and `--dns-threads`:
/// `strtol` must consume the whole value (leading whitespace and a sign
/// are therefore accepted), `ERANGE` is an error rather than a wrapped
/// value, and the bound is tested on the `long`, not on an `int`.
fn parse_number(option: &str, value: &OsStr, expected: &str, min: i64, max: i64) -> i64 {
    let bytes = arg_bytes(value);
    let (parsed, consumed, out_of_range) = strtol10(&bytes);
    if consumed == 0 || consumed != bytes.len() || out_of_range {
        invalid_option_value(option, value, expected);
    }
    if parsed < min || parsed > max {
        invalid_option_value(option, value, expected);
    }
    parsed
}

/// Strict full-string unsigned decimal parse (C strtoull semantics),
/// used for the reduce options (bounds checked per the C option;
/// out-of-bounds values print the C message).
fn parse_size(option: &str, value: &OsStr, expected: &str, max: u64) -> u64 {
    let bytes = arg_bytes(value);
    if bytes.is_empty() || !bytes[0].is_ascii_digit() {
        invalid_option_value(option, value, expected);
    }
    let mut parsed: u64 = 0;
    for &b in &*bytes {
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

/// Consume the value of an argv option exactly like the C scan.
///
/// Every C option branch is guarded by `i+1 < argc`, so a value-less
/// trailing option is never taken as an option: the token falls through
/// to the input branch (`legacy::input_source`) and the scan ends. The
/// caller must therefore stop scanning when this returns `None`
/// (`break`), because the token has already been classified; resuming
/// the loop would classify the same token again forever.
fn take_value(
    i: &mut usize,
    args: &[OsString],
    arg: &OsStr,
    options: &mut Options,
) -> Option<OsString> {
    if *i + 1 >= args.len() {
        if let Some(spec) = input_source(options.family, arg) {
            options.sources.push(spec);
        }
        return None;
    }
    *i += 1;
    Some(args[*i].clone())
}

/// The C input branch of the option scan: a file, `-` for stdin, an
/// `@file` list, or an `@dir` directory (the `@` destination is
/// classified on load).
///
/// Returns `None` for the one token class the scan drops: in IPv6 mode
/// the IPv4 main skips loading (`src/iprange.c:724`) and `iprange6_run`
/// then discards every dash-prefixed token that is not exactly `-` as a
/// flag (`src/iprange6_main.c:176`), so it never reaches either twin's
/// loader. The IPv4 default main has no such skip: a dash-prefixed name
/// is an ordinary file there.
fn input_source(family: Family, arg: &OsStr) -> Option<SourceSpec> {
    let spec = if arg == "-" {
        SourceSpec {
            kind: SourceKind::Path,
            arg: None,
            label: None,
        }
    } else if let Some(list) = argv::strip_at(arg) {
        SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(list),
            label: None,
        }
    } else if family == Family::V6 && argv::is_dash_option(arg) {
        return None;
    } else {
        SourceSpec {
            kind: SourceKind::Path,
            arg: Some(PathBuf::from(arg.to_os_string())),
            label: None,
        }
    };
    Some(spec)
}

/// C `--prefixes` tokenizer: the loop at `src/iprange.c:534-559`
/// (IPv4, bound 32) and `src/iprange6_main.c:127-146` (IPv6, bound
/// 128).
///
/// Each token is parsed by `strtol` base 10, so leading whitespace and
/// an optional sign belong to the number and a token with no digits
/// yields 0. The result is then cast from `long` to `int` *before* the
/// bound test, and the `int` is what the diagnostic prints. That
/// truncation is the observable behaviour: `2147483648` is reported as
/// -2147483648, `4294967299` is accepted as prefix 3, and a value that
/// saturates at `LONG_MAX`/`LONG_MIN` is reported as -1/0. `ERANGE` is
/// not checked by this branch, so it only shows up through the cast.
///
/// `debug` is the state of `-v` at this argv position: the C parses
/// the option inside its sequential scan, so a `-v` that comes after
/// `--prefixes` announces nothing.
///
/// Returns the allowed prefixes, or the complete C diagnostic for the
/// first token the cast left outside 1..=max.
fn parse_prefix_list(text: &OsStr, family: Family, debug: bool) -> Result<Vec<usize>, String> {
    let (max, invalid_text) = match family {
        Family::V4 => (
            32i64,
            "Only prefixes from 1 to 32 can be set (32 is always enabled).",
        ),
        Family::V6 => (128i64, "Only prefixes from 1 to 128 can be set."),
    };
    let bytes = arg_bytes(text);
    let mut allowed: Vec<usize> = Vec::new();
    let mut pos = 0usize;
    while pos < bytes.len() {
        let (value, consumed, _) = strtol10(&bytes[pos..]);
        // C: `j = (int)strtol(...)`, then `if(j <= 0 || j > <bound>)`.
        let truncated = value as i32;
        if truncated <= 0 || i64::from(truncated) > max {
            return Err(format!("iprange: {invalid_text} {truncated} is invalid."));
        }
        // The IPv4 argv scan announces each accepted token, with no
        // `iprange: ` prefix (`src/iprange.c:549`); `iprange6_run()`
        // parses the option without any debug output.
        if debug && family == Family::V4 {
            crate::legacy::argv::eprint_line(&format!("Enabling prefix {truncated}"));
        }
        allowed.push(truncated as usize);
        pos += consumed;
        // One separator is consumed per token; a token that yields no
        // digits (a double comma, junk, a lone separator) is rejected
        // above, so this loop cannot stall.
        if pos < bytes.len() && (bytes[pos] == b',' || bytes[pos] == b' ') {
            pos += 1;
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
    crate::legacy::argv::eprint_line(&format!("iprange: An ipset is needed before {option}"));
    std::process::exit(1);
}
