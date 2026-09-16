//! Legacy input loading: the released C `iprange` file grammar
//! (`src/ipset_load.c`, `src/ipset6_load.c`), binary detection
//! (`src/ipset_binary.c`, `src/ipset6_binary.c`), hostname
//! classification through the DNS pool (`src/ipset_dns.c`,
//! `src/ipset6_dns.c`), and `@file`/`@dir` expansion
//! (`src/iprange.c`, `src/iprange6_main.c`).
//!
//! Every diagnostic below is a byte-for-byte copy of the C text
//! (C `fprintf(stderr, ...)` semantics, including the embedded
//! newline the C line buffer carries into `%s`).

use std::ffi::{OsStr, OsString};
use std::io::Read;
use std::path::{Path, PathBuf};

use super::argv;
use super::binary;
use super::diag::{cstr, Diag, DiagMsg};
use super::dns::{DnsError, Resolver};
use super::family::{Family, FamilyImpl};
use super::options::{Options, SourceKind};
use super::range::{IpNum, IpSet, Range};

/// One loaded ipset with its CSV name. The name is the C
/// `ips->filename`: the source path verbatim, `stdin`, or the `as
/// NAME` label (the C code never strips directories or extensions).
/// It is an `OsString` because a POSIX name may hold bytes that are
/// not valid UTF-8, and the released tool prints those bytes verbatim
/// in the CSV name column.
#[derive(Debug)]
pub struct Loaded<F: FamilyImpl> {
    pub name: OsString,
    pub set: IpSet<F>,
}

/// The complete load result: every set in C chain order plus the
/// group-B boundary in *loaded-set* units. The C argv scan records
/// the positional-operator boundary in *source* units
/// (`Options::group_b`); one `@file`/`@dir` source can expand to
/// several sets, so the boundary is converted here, where the
/// expansion happens.
#[derive(Debug)]
pub struct LoadedAll<F: FamilyImpl> {
    /// All sets in load order (group A first, then group B).
    pub sets: Vec<Loaded<F>>,
    /// Index of the first group-B set; `== sets.len()` when no
    /// positional operator was given.
    pub group_b: usize,
}

/// C `MAX_LINE` (fgets buffer): one line record is at most 1023
/// bytes plus the trailing newline slot.
const MAX_LINE: usize = 1024;

/// C `MAX_INPUT_ELEMENT` (IPv4 token buffer).
const MAX_TOKEN: usize = 255;
/// C `MAX_INPUT_ELEMENT6` (IPv6 token buffer).
const MAX_TOKEN6: usize = 256;

/// Binary header lines (exact first-record match, newline included).
const BINARY_HEADER_V10: &[u8] = b"iprange binary format v1.0\n";
const BINARY_HEADER_V20: &[u8] = b"iprange binary format v2.0\n";

/// The gate for the load-bookkeeping diagnostics that only the IPv4
/// twin prints.
///
/// `iprange6_run()` and `ipset6_load.c` carry no `debug` statement for
/// load bookkeeping: in `-6` mode the C prints only `Loading from ...
/// (IPv6 mode)` and the per-operation lines, never `Loaded ...`,
/// `Binary loaded ...`, `<name> is empty`, or the `@dir`/`@list`
/// expansion lines. Those come from the IPv4 `ipset_load()` and the
/// IPv4 `main()` argv scan (`src/ipset_load.c:275,289,418`,
/// `src/iprange.c:762,810,821,851,875,900`).
fn v4_verbose<F: FamilyImpl>(options: &Options) -> bool {
    options.debug && F::FAMILY == Family::V4
}

/// Load every source in argv order. Group A and group B are both
/// loaded here (per-source sets in argv order, each `@file`/`@dir`
/// source expanded in place); `ops` applies the
/// `--except`/`--diff`/`--compare-next` group-B split at
/// `LoadedAll::group_b`, which counts *loaded sets*, not argv
/// sources.
pub fn load_all<F: FamilyImpl>(options: &Options) -> Result<LoadedAll<F>, Diag> {
    let mut stdin = std::io::stdin().lock();
    load_all_impl::<F>(options, &mut stdin)
}

/// `load_all` with an injectable stdin (tests feed a `Cursor`).
fn load_all_impl<F: FamilyImpl>(
    options: &Options,
    stdin: &mut dyn Read,
) -> Result<LoadedAll<F>, Diag> {
    // One DNS resolver per run (C keeps one global pool for the
    // whole invocation; each file drains its own names via dns_done).
    let mut resolver = Resolver::new(
        options.dns_threads,
        options.dns_silent,
        options.dns_progress,
        F::FAMILY,
        options.debug,
    );

    let mut stdin_data: Option<Vec<u8>> = None;
    let mut dns_used = false;
    let mut loaded: Vec<Loaded<F>> = Vec::new();
    // C main() prints a contextual "Cannot load ..." line after every
    // ipset_load() failure; used when only the DNS finish() survives.
    let mut last_context = Diag::default();

    // Loaded-set boundary at the C positional operator: every set
    // added on or after the operator's source index belongs to group
    // B (C `read_second` splits by loaded ipset order, so expanded
    // @file/@dir sets are counted individually here).
    let mut boundary = 0usize;

    for (source_index, spec) in options.sources.iter().enumerate() {
        let mut sets: Vec<(OsString, IpSet<F>)> = match spec.kind {
            SourceKind::Path => {
                let arg = spec.arg.as_deref().unwrap_or(Path::new(""));
                if arg.as_os_str().is_empty() {
                    // `-` (or an empty argument) reads stdin. The C
                    // code consumes stdin once; a second `-` sees EOF
                    // and produces an empty set.
                    let data = read_stdin_once(stdin, &mut stdin_data);
                    let context = Diag::from("iprange: Cannot load ipset from stdin");
                    last_context = context.clone();
                    let (set, dns) = load_one::<F>(
                        OsStr::new("stdin"),
                        &data,
                        options,
                        &mut resolver,
                        &context,
                    )?;
                    dns_used |= dns;
                    vec![(OsString::from("stdin"), set)]
                } else {
                    // The name is the argv bytes: the open, the CSV
                    // name and both diagnostic lines use them verbatim,
                    // as C's fopen() and `fprintf(stderr, "%s")` do.
                    let arg_name = argv::bytes(arg.as_os_str());
                    let context = Diag::from_parts(&[b"iprange: Cannot load ipset: ", &arg_name]);
                    last_context = context.clone();
                    let data = read_legacy_input(arg).map_err(|e| {
                        Diag::from_parts(&[
                            b"iprange: ",
                            &arg_name,
                            b" - ",
                            strerror(&e).as_bytes(),
                            b"\n",
                            context.bytes(),
                        ])
                    })?;
                    let (set, dns) =
                        load_one::<F>(arg.as_os_str(), &data, options, &mut resolver, &context)?;
                    dns_used |= dns;
                    vec![(arg.to_path_buf().into_os_string(), set)]
                }
            }
            SourceKind::FileList => {
                let list = spec
                    .arg
                    .as_deref()
                    .ok_or_else(|| "iprange: @ source without a path".to_owned())?;
                expand_at::<F>(
                    list,
                    options,
                    &mut resolver,
                    &mut last_context,
                    &mut dns_used,
                )?
            }
        };

        // `as NAME` renames the last set the argument produced (C
        // renames `root_last`, which is the last loaded ipset).
        if let Some(label) = &spec.label {
            if let Some(last) = sets.last_mut() {
                last.0 = label.clone();
            }
        }
        loaded.extend(sets.into_iter().map(|(name, set)| Loaded { name, set }));
        if options.group_b == Some(source_index + 1) {
            boundary = loaded.len();
        }
    }
    if options.group_b.is_none() {
        boundary = loaded.len();
    }

    // C dns_done() runs at the end of every file load, but with no
    // DNS requests made it is an immediate no-op (made == 0), so the
    // pool finish() is only needed when a hostname was resolved.
    if dns_used && resolver.finish().is_err() {
        // C dns6_done() never fails; the IPv4 dns_done() failure is
        // reported by main() with the last source's context line.
        if F::FAMILY == Family::V4 {
            return Err(last_context);
        }
    }

    Ok(LoadedAll {
        sets: loaded,
        group_b: boundary,
    })
}

/// Read stdin once like C `fgets`; later `-` arguments get EOF.
fn read_stdin_once(stdin: &mut dyn Read, cache: &mut Option<Vec<u8>>) -> Vec<u8> {
    match cache {
        Some(_) => Vec::new(),
        None => {
            let mut buf = Vec::new();
            let _ = stdin.read_to_end(&mut buf); // C treats read errors as EOF
            *cache = Some(buf.clone());
            buf
        }
    }
}

/// `@path` expansion: a directory loads every regular file sorted by
/// name (C `qsort` + `strcmp` byte order on the full path); a plain
/// file is a file list of paths, one per line.
fn expand_at<F: FamilyImpl>(
    list: &Path,
    options: &Options,
    resolver: &mut Resolver,
    last_context: &mut Diag,
    dns_used: &mut bool,
) -> Result<Vec<(OsString, IpSet<F>)>, Diag> {
    // The `@` destination is named by bytes: every open, CSV name and
    // diagnostic uses them verbatim.
    let list_name = argv::bytes(list.as_os_str());
    match std::fs::metadata(list) {
        Ok(md) if md.is_dir() => {
            if v4_verbose::<F>(options) {
                Diag::from_parts(&[b"iprange: Loading files from directory ", &list_name]).emit();
            }

            let mut files: Vec<PathBuf> = Vec::new();
            let rd = std::fs::read_dir(list).map_err(|e| {
                Diag::from_parts(&[
                    b"iprange: Cannot access ",
                    &list_name,
                    b": ",
                    strerror(&e).as_bytes(),
                ])
            })?;
            for entry in rd {
                // C skips entries whose stat() fails; "." and ".."
                // are not produced by read_dir.
                let entry = entry.map_err(|e| {
                    Diag::from_parts(&[
                        b"iprange: Cannot access ",
                        &list_name,
                        b": ",
                        strerror(&e).as_bytes(),
                    ])
                })?;
                let path = list.join(entry.file_name());
                if std::fs::metadata(&path)
                    .map(|m| m.is_file())
                    .unwrap_or(false)
                {
                    files.push(path);
                }
            }
            files.sort();

            if files.is_empty() {
                if v4_verbose::<F>(options) {
                    Diag::from_parts(&[
                        b"iprange: Directory ",
                        &list_name,
                        b" is empty or contains no valid files",
                    ])
                    .emit();
                }
                return Err(Diag::from_parts(&[
                    b"iprange: No valid files found in directory: ",
                    &list_name,
                ]));
            }

            let mut sets = Vec::with_capacity(files.len());
            for path in &files {
                let path_name = argv::bytes(path.as_os_str());
                if v4_verbose::<F>(options) {
                    Diag::from_parts(&[
                        b"iprange: Loading file ",
                        &path_name,
                        b" from directory ",
                        &list_name,
                    ])
                    .emit();
                }
                let context = match F::FAMILY {
                    Family::V4 => Diag::from_parts(&[
                        b"iprange: Cannot load file ",
                        &path_name,
                        b" from directory ",
                        &list_name,
                    ]),
                    Family::V6 => Diag::from_parts(&[b"iprange: Cannot load file ", &path_name]),
                };
                *last_context = context.clone();
                let data = std::fs::read(path).map_err(|e| {
                    Diag::from_parts(&[
                        b"iprange: ",
                        &path_name,
                        b" - ",
                        strerror(&e).as_bytes(),
                        b"\n",
                        context.bytes(),
                    ])
                })?;
                let (set, dns) =
                    load_one::<F>(path.as_os_str(), &data, options, resolver, &context)?;
                *dns_used |= dns;
                sets.push((path.clone().into_os_string(), set));
            }
            Ok(sets)
        }
        Ok(_) => {
            // A non-directory @ target is a file list (C opendir()
            // fails with ENOTDIR and falls into the list branch).
            if v4_verbose::<F>(options) {
                Diag::from_parts(&[b"iprange: Loading files from list ", &list_name]).emit();
            }
            let content = std::fs::read(list).map_err(|e| {
                Diag::from_parts(&[
                    b"iprange: Cannot open file list: ",
                    &list_name,
                    b" - ",
                    strerror(&e).as_bytes(),
                ])
            })?;

            let mut sets: Vec<(OsString, IpSet<F>)> = Vec::new();
            let mut lineid = 0usize;
            for rec in Records::new(&content) {
                lineid += 1;
                // C keeps the record in a `char[]` and treats it as a C
                // string from the first byte: `*s == '\0'` ends the entry
                // (src/iprange.c:869-872), so everything after the first
                // NUL is invisible to the empty/comment test, the
                // trailing-whitespace trim, `fopen`, the CSV name and the
                // diagnostics alike.
                let s = cstr(skip_ws(rec));
                if matches!(
                    s.first(),
                    None | Some(b'\n') | Some(b'\r') | Some(b'#') | Some(b';')
                ) {
                    continue;
                }
                let path = argv::path_from_bytes(trim_trailing_ws(s));
                let path_name = argv::bytes(path.as_os_str());
                let lineid_text = lineid.to_string();
                if v4_verbose::<F>(options) {
                    Diag::from_parts(&[
                        b"iprange: Loading file ",
                        &path_name,
                        b" from list (line ",
                        lineid_text.as_bytes(),
                        b")",
                    ])
                    .emit();
                }
                let context = Diag::from_parts(&[
                    b"iprange: Cannot load file ",
                    &path_name,
                    b" from list ",
                    &list_name,
                    b" (line ",
                    lineid_text.as_bytes(),
                    b")",
                ]);
                *last_context = context.clone();
                let data = read_legacy_input(&path).map_err(|e| {
                    Diag::from_parts(&[
                        b"iprange: ",
                        &path_name,
                        b" - ",
                        strerror(&e).as_bytes(),
                        b"\n",
                        context.bytes(),
                    ])
                })?;
                let (set, dns) =
                    load_one::<F>(path.as_os_str(), &data, options, resolver, &context)?;
                *dns_used |= dns;
                sets.push((path.into_os_string(), set));
            }

            if sets.is_empty() {
                if v4_verbose::<F>(options) {
                    Diag::from_parts(&[
                        b"iprange: File list ",
                        &list_name,
                        b" is empty or contains no valid entries",
                    ])
                    .emit();
                }
                return Err(Diag::from_parts(&[
                    b"iprange: No valid files found in file list: ",
                    &list_name,
                ]));
            }
            Ok(sets)
        }
        Err(e) => Err(Diag::from_parts(&[
            b"iprange: Cannot access ",
            &list_name,
            b": ",
            strerror(&e).as_bytes(),
        ])),
    }
}

/// Per-file loading state accumulated while walking the records.
struct FileIssues {
    /// Any line failed to parse (C `parse_errors`).
    parse_failed: bool,
    /// At least one hostname was sent to the resolver (C
    /// `dns_requests_made > 0`); gates the run-end finish() call.
    dns_used: bool,
    /// At least one hostname failed to resolve (v4 fails the file;
    /// v6 ignores, mirroring `dns_done` vs `dns6_done`).
    dns_failed: bool,
    /// A DNS request could not be queued at all (C `dns_request()`
    /// / `dns6_request()` returning -1; fails the file in both
    /// families).
    request_failed: bool,
    /// Non-mapped IPv6 lines dropped in IPv4 mode (C
    /// `ipv6_dropped_in_ipv4_mode`, reset per successful load).
    dropped_v6: u64,
}

/// Load one file (or the stdin stream) into a fresh set. `context`
/// is the C main()-level error line printed by the caller when the
/// load fails (the load itself prints the specific diagnostics).
fn load_one<F: FamilyImpl>(
    name: &OsStr,
    data: &[u8],
    options: &Options,
    resolver: &mut Resolver,
    context: &Diag,
) -> Result<(IpSet<F>, bool), Diag> {
    // `name` reaches the caller as the CSV name of the set, byte for
    // byte, and it labels these diagnostics; C prints it through `%s`,
    // so its bytes go out verbatim.
    let name = argv::bytes(name);
    match F::FAMILY {
        Family::V4 => {
            if options.debug {
                Diag::from_parts(&[b"iprange: Loading from ", &name]).emit();
            }
        }
        Family::V6 => {
            if options.debug {
                Diag::from_parts(&[b"iprange: Loading from ", &name, b" (IPv6 mode)"]).emit();
            }
        }
    }

    let mut records = Records::new(data);
    let Some(first) = records.next() else {
        // C: the first fgets() returns NULL: valid empty set.
        if v4_verbose::<F>(options) {
            Diag::from_parts(&[b"iprange: ", &name, b" is empty"]).emit();
        }
        return Ok((IpSet::default(), false));
    };

    // The IPv6 loader strips a UTF-8 BOM from the first line only.
    let first = match F::FAMILY {
        Family::V6 if first.len() >= 3 && first[..3] == [0xEF, 0xBB, 0xBF] => &first[3..],
        _ => first,
    };

    // Binary detection: the whole first record must equal the header
    // line (newline included); the rest of the file is binary.
    if first == BINARY_HEADER_V10 || first == BINARY_HEADER_V20 {
        // The validator echoes the source label and the header values
        // with `%s`, so both travel as bytes. Every failure here is an
        // `ipset_load()` failure, and both C twins answer those with
        // the caller's context line (`src/iprange.c:911`,
        // `src/iprange6_main.c:320`), so it follows the specific
        // diagnostic exactly like the open-failure family.
        let with_context = |inner: Diag| Diag::from_parts(&[inner.bytes(), b"\n", context.bytes()]);
        let set = match F::FAMILY {
            Family::V4 if first == BINARY_HEADER_V10 => {
                let set = binary::load_v1(data, &name).map_err(|inner| {
                    with_context(Diag::from_parts(&[
                        inner.bytes(),
                        b"\niprange: Cannot fast load ",
                        &name,
                    ]))
                })?;
                convert_set::<u32, F>(set)
            }
            Family::V6 if first == BINARY_HEADER_V20 => {
                let set = binary::load_v2(data, &name).map_err(|inner| {
                    with_context(Diag::from_parts(&[
                        inner.bytes(),
                        b"\niprange: Cannot load binary v2 ",
                        &name,
                    ]))
                })?;
                convert_set::<u128, F>(set)
            }
            Family::V4 => {
                return Err(with_context(Diag::from_parts(&[
                    b"iprange: ",
                    &name,
                    b": IPv6 binary file cannot be loaded in IPv4 mode (use -6)",
                ])));
            }
            Family::V6 => {
                return Err(with_context(Diag::from_parts(&[
                    b"iprange: ",
                    &name,
                    b": IPv4 binary file cannot be loaded in IPv6 mode",
                ])));
            }
        };
        if v4_verbose::<F>(options) {
            Diag::from_parts(&[
                b"iprange: Binary loaded ",
                if set.optimized {
                    b"optimized"
                } else {
                    b"non-optimized"
                },
                b" ",
                &name,
            ])
            .emit();
        }
        return Ok((set, false));
    }

    let mut set: IpSet<F> = IpSet::default();
    let mut issues = FileIssues {
        parse_failed: false,
        dns_used: false,
        dns_failed: false,
        request_failed: false,
        dropped_v6: 0,
    };

    // C ipset_load() counts fgets records as "lines" (lineid starts
    // at 0 and increments once per record; long physical lines split
    // into several records with increasing ids).
    let mut lineid = 1usize;
    process_record::<F>(
        first,
        lineid,
        &name,
        options,
        resolver,
        &mut set,
        &mut issues,
    );
    for rec in records {
        lineid += 1;
        process_record::<F>(rec, lineid, &name, options, resolver, &mut set, &mut issues);
    }

    // C ipset_load() order: dns_done() drains the file's batch,
    // adds the replies in load order, and fails the file in v4 mode
    // when any reply failed; then the parse-errors check, the
    // IPv6-drop warning, and the debug "Loaded" line. The failure
    // itself is reported by the caller.
    if issues.dns_used {
        for reply in resolver.drain() {
            match reply.result {
                Ok(addrs) => {
                    // One entry (and one C `lines` unit) per
                    // reply address; per-name duplicates are
                    // dropped (v6 mapped-A duplicates too).
                    let mut seen = std::collections::HashSet::new();
                    for addr in addrs {
                        if seen.insert(addr) {
                            let ip = F::from_u128(addr);
                            add_entry(&mut set, Range { lo: ip, hi: ip }, &name, options);
                        }
                    }
                }
                Err(e) => {
                    // The parse worker renders the C final-failure
                    // line from the variant (silent gates the
                    // permanent failure class exactly like C
                    // dns_request_failed); the name contributes
                    // nothing.
                    match e {
                        DnsError::NotFound(msg) => {
                            if !options.dns_silent {
                                eprintln!("{msg}");
                            }
                        }
                        DnsError::System(msg) => {
                            eprintln!("{msg}");
                        }
                    }
                    issues.dns_failed = true;
                }
            }
        }
    }
    if (issues.dns_failed && F::FAMILY == Family::V4) || issues.request_failed {
        return Err(context.clone());
    }
    if issues.parse_failed {
        return Err(context.clone());
    }
    if issues.dropped_v6 > 0 {
        fmt_drop_warning(&name, issues.dropped_v6).emit();
    }
    if v4_verbose::<F>(options) {
        Diag::from_parts(&[
            b"iprange: Loaded ",
            if set.optimized {
                b"optimized"
            } else {
                b"non-optimized"
            },
            b" ",
            &name,
        ])
        .emit();
    }

    Ok((set, issues.dns_used))
}

/// Classify and apply one text record (C `parse_line` /
/// `parse_line6` plus the load-time action for each outcome).
#[allow(clippy::too_many_arguments)]
fn process_record<F: FamilyImpl>(
    rec: &[u8],
    lineid: usize,
    name: &[u8],
    options: &Options,
    resolver: &mut Resolver,
    set: &mut IpSet<F>,
    issues: &mut FileIssues,
) {
    // C classifies the fgets buffer through C-string APIs: every end-of-line
    // test in parse_line()/parse_line6() accepts '\0' (src/ipset_load.c:150,
    // 173, 195, 224; src/ipset6_load.c:68, 99, 127, 144), every token scan
    // stops at it, and the IPv6-drop scan strchr(line, ":")
    // (src/ipset_load.c:309) cannot see past it. Only the bytes before the
    // first NUL can change the outcome. The record itself stays raw: record
    // boundaries and ids come from the '\n' split in Records, and the echo of
    // an unparseable record already stops at the NUL like C's %s.
    let rec = cstr(rec);

    let outcome = match F::FAMILY {
        Family::V4 => classify_v4(rec, lineid),
        Family::V6 => classify_v6(rec),
    };

    match outcome {
        LineOutcome::Empty => {}

        LineOutcome::OneIp(tok) => {
            if let Err(inner) = add_token::<F>(&tok, options, set, name) {
                eprintln!("{inner}");
                fmt_cannot_understand(lineid, name, rec).emit();
                issues.parse_failed = true;
            }
        }

        LineOutcome::TwoIps(a, b) => {
            let r1 = match parse_token::<F>(&a, options) {
                Ok(r) => r,
                Err(inner) => {
                    eprintln!("{inner}");
                    fmt_cannot_understand(lineid, name, rec).emit();
                    issues.parse_failed = true;
                    return;
                }
            };
            let r2 = match parse_token::<F>(&b, options) {
                Ok(r) => r,
                Err(inner) => {
                    eprintln!("{inner}");
                    fmt_cannot_understand(lineid, name, rec).emit();
                    issues.parse_failed = true;
                    return;
                }
            };
            // IPv6 mode rejects ranges with mixed-family endpoints
            // (C classify_address() differs between the two tokens).
            if F::FAMILY == Family::V6
                && classify_token(a.as_bytes()) != classify_token(b.as_bytes())
            {
                eprintln!("{}", fmt_mixed_family(lineid, &a, &b));
                issues.parse_failed = true;
                return;
            }
            let lo = if r1.lo < r2.lo { r1.lo } else { r2.lo };
            let hi = if r1.hi > r2.hi { r1.hi } else { r2.hi };
            add_entry(set, Range { lo, hi }, name, options);
        }

        LineOutcome::WarnedRange { first, warning } => {
            // C prints during line classification and still adds the
            // first IP as a single entry.
            eprintln!("{warning}");
            if let Err(inner) = add_token::<F>(&first, options, set, name) {
                eprintln!("{inner}");
                fmt_cannot_understand(lineid, name, rec).emit();
                issues.parse_failed = true;
            }
        }

        LineOutcome::Hostname(host) => {
            issues.dns_used = true;
            if options.debug {
                match F::FAMILY {
                    Family::V4 => Diag::from_parts(&[
                        b"iprange: DNS resolution for hostname '",
                        host.as_bytes(),
                        b"' from line ",
                        lineid.to_string().as_bytes(),
                        b" of file ",
                        name,
                        b".",
                    ])
                    .emit(),
                    Family::V6 => Diag::from_parts(&[
                        b"iprange: DNS resolution for hostname '",
                        host.as_bytes(),
                        b"' from line ",
                        lineid.to_string().as_bytes(),
                        b" of file ",
                        name,
                        b" (IPv6 mode).",
                    ])
                    .emit(),
                }
            }
            // Queue the host; C dns_request()/dns6_request() returns
            // -1 (failing the file) only for empty/oversized names.
            // The replies are added by the per-file drain below.
            match resolver.request(&host) {
                Ok(()) => {}
                Err(e) => {
                    // Always printed (C does not gate this class).
                    eprintln!("{e}");
                    issues.request_failed = true;
                }
            }
        }

        LineOutcome::Invalid => {
            if F::FAMILY == Family::V4 && colons(rec) >= 2 {
                // C: any unparseable line with two colons is treated
                // as an IPv6 line in IPv4 mode: mapped ::ffff: lines
                // convert back to IPv4, everything else is dropped
                // with the per-file counter.
                match F::convert_foreign(&ascii_lossy(skip_ws(rec))) {
                    Some(range) => add_entry(set, range, name, options),
                    None => issues.dropped_v6 += 1,
                }
            } else {
                fmt_cannot_understand(lineid, name, rec).emit();
                issues.parse_failed = true;
            }
        }
    }
}

/// Parse one address token (`ADDR`, `ADDR/PREFIX`) with the family
/// parse policy. `prefix` is the family default applied when the
/// token carries no prefix (v4 `--default-prefix`, v6 always 128);
/// `fix_network` mirrors C `cidr_use_network` (--dont-fix-network).
fn parse_token<F: FamilyImpl>(token: &str, options: &Options) -> Result<Range<F>, String> {
    let prefix = F::default_prefix(options);
    F::parse_cidr(token, prefix, !options.dont_fix_network, prefix)
}

/// Parse one token and add it; Err carries the C family diagnostic.
fn add_token<F: FamilyImpl>(
    tok: &str,
    options: &Options,
    set: &mut IpSet<F>,
    name: &[u8],
) -> Result<(), String> {
    parse_token::<F>(tok, options).map(|range| add_entry(set, range, name, options))
}

/// Add one entry with the C `ipset_added_entry` accounting: `lines`
/// counts every successful add (even an adjacency merge), and the
/// first append that breaks the sorted/non-overlapping order clears
/// the optimized flag.
///
/// Under `-v` the C prints one `NON-OPTIMIZED` line naming the set,
/// the record, and both ranges at exactly that transition
/// (`src/ipset.h:107-121`, `src/ipset6.h:100-106`). The ordering rule
/// itself stays in `IpSet::add_range`; this wrapper only observes the
/// flag transition, so the diagnostic cannot drift from it.
fn add_entry<F: FamilyImpl>(set: &mut IpSet<F>, range: Range<F>, name: &[u8], options: &Options) {
    let was_optimized = set.optimized;
    let prev_entries = set.entries;
    let prev_last = set.ranges.last().copied();
    set.lines += 1;
    set.add_range(range);
    if !options.debug || !was_optimized || set.optimized || prev_entries == 0 {
        return;
    }
    let last = prev_last.expect("entries > 0 means a last range exists");
    let mut msg = DiagMsg::new(b"");
    // DiagMsg always writes the `iprange: ` prefix, so the label is
    // appended by hand to keep the C word order.
    let line = set.lines.to_string();
    let entry = prev_entries.to_string();
    msg.push(b"NON-OPTIMIZED ");
    msg.push(name);
    msg.push(b" at line ");
    msg.push(line.as_bytes());
    msg.push(b", entry ");
    msg.push(entry.as_bytes());
    msg.push(b", last was ");
    msg.push(F::fmt_addr(last.lo).as_bytes());
    if F::FAMILY == Family::V4 {
        msg.push(b" (");
        msg.number(last.lo.as_u128());
        msg.push(b")");
    }
    msg.push(b" - ");
    msg.push(F::fmt_addr(last.hi).as_bytes());
    if F::FAMILY == Family::V4 {
        msg.push(b" (");
        msg.number(last.hi.as_u128());
        msg.push(b")");
    }
    msg.push(b", new is ");
    msg.push(F::fmt_addr(range.lo).as_bytes());
    if F::FAMILY == Family::V4 {
        msg.push(b" (");
        msg.number(range.lo.as_u128());
        msg.push(b")");
    }
    msg.push(b" - ");
    msg.push(F::fmt_addr(range.hi).as_bytes());
    if F::FAMILY == Family::V4 {
        msg.push(b" (");
        msg.number(range.hi.as_u128());
        msg.push(b")");
    }
    msg.build().emit();
}

/// Family-neutral view of a concrete binary payload set. The only
/// call sites are the family-matched branches (V4 loads u32, V6
/// loads u128), so this is a widening/identity mapping; it exists
/// because `load_v1`/`load_v2` return concrete `IpSet` types while
/// `load_one` is generic over `F: FamilyImpl`.
fn convert_set<T: IpNum, F: IpNum>(set: IpSet<T>) -> IpSet<F> {
    IpSet {
        ranges: set
            .ranges
            .iter()
            .map(|r| Range {
                lo: F::from_u128(r.lo.as_u128()),
                hi: F::from_u128(r.hi.as_u128()),
            })
            .collect(),
        entries: set.entries,
        lines: set.lines,
        unique: set.unique,
        optimized: set.optimized,
    }
}

/// Count `:` bytes (C `strchr` loop detects a second colon).
fn colons(rec: &[u8]) -> usize {
    rec.iter().filter(|&&b| b == b':').count()
}

// ---------------------------------------------------------------------------
// Line grammar (exact C parse_line / parse_line6 ports)
// ---------------------------------------------------------------------------

/// A parsed line. `WarnedRange` carries the C classification-time
/// warning text; the first IP of the broken range is still added.
#[derive(Debug, PartialEq)]
enum LineOutcome {
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

fn skip_ws(mut s: &[u8]) -> &[u8] {
    loop {
        match s.split_first() {
            Some((b' ', rest)) | Some((b'\t', rest)) => s = rest,
            _ => return s,
        }
    }
}

/// C iprange_trim_trailing_whitespace(): strips `\n \r space \t`
/// from the end of a file-list path line.
fn trim_trailing_ws(mut s: &[u8]) -> &[u8] {
    loop {
        match s.split_last() {
            Some((&b, rest)) if b == b'\n' || b == b'\r' || b == b' ' || b == b'\t' => s = rest,
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
fn classify_token(t: &[u8]) -> u8 {
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
fn classify_v4(rec: &[u8], lineid: usize) -> LineOutcome {
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
fn classify_v6(rec: &[u8]) -> LineOutcome {
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
struct Records<'a> {
    data: &'a [u8],
    pos: usize,
}

impl<'a> Records<'a> {
    fn new(data: &'a [u8]) -> Self {
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

// ---------------------------------------------------------------------------
// Diagnostic texts (byte-for-byte C copies)
// ---------------------------------------------------------------------------

/// `iprange: Cannot understand line No N from NAME: LINE` - the raw
/// record is embedded verbatim, including its trailing newline (the
/// C buffer printed by %s), so the caller adds only the closing \n.
fn fmt_cannot_understand(lineid: usize, name: &[u8], raw: &[u8]) -> Diag {
    // C prints the line buffer with `%s`: the echo is the record's bytes
    // up to the first NUL, trailing newline included when no NUL comes
    // first (which is why the C output can show an apparent blank line
    // after the echoed record). The name and the record are both written
    // as bytes, so a damaged binary payload read as text echoes exactly
    // the prefix C would print and nothing beyond it.
    Diag::from_parts(&[
        b"iprange: Cannot understand line No ",
        lineid.to_string().as_bytes(),
        b" from ",
        name,
        b": ",
        cstr(raw),
    ])
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

fn fmt_mixed_family(lineid: usize, a: &str, b: &str) -> String {
    format!("iprange: Mixed-family range on line {lineid}: {a} - {b}")
}

fn fmt_drop_warning(name: &[u8], count: u64) -> Diag {
    Diag::from_parts(&[
        b"iprange: ",
        name,
        b": ",
        count.to_string().as_bytes(),
        b" IPv6 entries dropped (use -6 for IPv6 mode)",
    ])
}

#[cfg(unix)]
fn is_directory_read_error(e: &std::io::Error) -> bool {
    // glibc reports the failed `fgets()` on an open directory as
    // `EISDIR` from `read(2)`.
    e.raw_os_error() == Some(libc::EISDIR)
}

#[cfg(not(unix))]
fn is_directory_read_error(_e: &std::io::Error) -> bool {
    false
}

/// Read one legacy input file the way the released tool reads it.
///
/// glibc `fopen(path, "r")` succeeds for a directory and the first
/// `fgets()` then fails, so `ipset_load()` reports no error and returns
/// an empty ipset (`src/ipset_load.c:270-280`, `src/ipset6_load.c:200-218`).
/// A directory named on argv or by an `@file` list is therefore an empty
/// set with exit 0, and its content is never read.
///
/// Only `EISDIR` is empty. The C error path is a failed `fopen()`, and
/// that call reports `EACCES`, `ENOENT` and every other failure before
/// any read happens, so those keep their C diagnostic. This is
/// deliberately stricter than C, which cannot tell a read error other
/// than `EISDIR` apart from a directory either: a failed read that is
/// not a directory must stay an error here.
fn read_legacy_input(path: &Path) -> std::io::Result<Vec<u8>> {
    match std::fs::read(path) {
        Err(e) if is_directory_read_error(&e) => Ok(Vec::new()),
        other => other,
    }
}

/// C strerror(errno) text, without the Rust " (os error N)" suffix.
fn strerror(e: &std::io::Error) -> String {
    #[cfg(unix)]
    if let Some(errno) = e.raw_os_error() {
        // Safe: glibc strerror() returns a static TLS/static buffer.
        let msg = unsafe { std::ffi::CStr::from_ptr(libc::strerror(errno)) };
        return msg.to_string_lossy().into_owned();
    }
    e.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::legacy::options::SourceSpec;
    use std::ffi::{OsStr, OsString};
    use std::path::PathBuf;

    // ------------------------------------------------------------------
    // Test families: tiny IPv4/IPv6 twins so load decisions are
    // observable without depending on the ipv4/ipv6 workers.
    // ------------------------------------------------------------------

    #[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
    struct F4(u32);

    fn parse_v4(s: &str) -> Option<u32> {
        if let Ok(n) = s.parse::<u32>() {
            return Some(n);
        }
        let parts: Vec<&str> = s.split('.').collect();
        if parts.len() == 4 {
            let mut v = 0u32;
            for p in parts {
                let o: u32 = p.parse().ok()?;
                if o > 255 {
                    return None;
                }
                v = (v << 8) | o;
            }
            return Some(v);
        }
        None
    }

    impl IpNum for F4 {
        const BITS: u32 = 32;
        const MAX: Self = F4(u32::MAX);
        fn as_u128(self) -> u128 {
            self.0 as u128
        }
        fn from_u128(v: u128) -> Self {
            F4(v as u32)
        }
    }

    impl FamilyImpl for F4 {
        const FAMILY: Family = Family::V4;

        fn parse_addr(t: &str) -> Result<Self, String> {
            parse_v4(t)
                .map(F4)
                .ok_or_else(|| format!("iprange: Invalid address {t}."))
        }

        fn parse_cidr(
            token: &str,
            prefix: u32,
            fix_network: bool,
            dp: u32,
        ) -> Result<Range<Self>, String> {
            let (addr, pfx) = match token.split_once('/') {
                Some((a, p)) => (
                    a,
                    p.parse::<u32>()
                        .map_err(|_| format!("iprange: Invalid address {a}."))?,
                ),
                None => (token, prefix),
            };
            let _ = dp;
            let a = parse_v4(addr).ok_or_else(|| format!("iprange: Invalid address {addr}."))?;
            if pfx > 32 {
                return Err(format!("iprange: Invalid prefix /{pfx}"));
            }
            let netmask = if pfx == 0 { 0 } else { u32::MAX << (32 - pfx) };
            let lo = if fix_network { a & netmask } else { a };
            let hi = lo | !netmask;
            Ok(Range {
                lo: F4(lo),
                hi: F4(hi),
            })
        }

        fn parse_prefix(text: &str) -> Result<u32, String> {
            text.parse()
                .map_err(|_| format!("iprange: Invalid prefix {text}"))
        }

        fn fmt_addr(a: Self) -> String {
            a.0.to_string()
        }

        fn fmt_cidr(a: Self, p: u32) -> String {
            format!("{}/{}", a.0, p)
        }

        /// Exact C mapped-IPv6 policy on the whitespace-trimmed line:
        /// only a line-start `::ffff:` (case-insensitive f) converts,
        /// with the C `[0-9./]` scan and trailing-junk check.
        fn convert_foreign(line: &str) -> Option<Range<Self>> {
            let b = line.as_bytes();
            if b.len() < 7
                || b[0] != b':'
                || b[1] != b':'
                || !matches!(b[2], b'f' | b'F')
                || !matches!(b[3], b'f' | b'F')
                || !matches!(b[4], b'f' | b'F')
                || !matches!(b[5], b'f' | b'F')
                || b[6] != b':'
            {
                return None;
            }
            let tail = &b[7..];
            let mut i = 0;
            while i < tail.len() && (tail[i].is_ascii_digit() || tail[i] == b'.' || tail[i] == b'/')
            {
                i += 1;
            }
            let mut j = i;
            while j < tail.len() && (tail[j] == b' ' || tail[j] == b'\t') {
                j += 1;
            }
            if i == 0
                || !matches!(
                    tail.get(j),
                    None | Some(b'#') | Some(b';') | Some(b'\r') | Some(b'\n')
                )
            {
                return None;
            }
            let v4 = String::from_utf8_lossy(&tail[..i]);
            Self::parse_cidr(&v4, 32, true, 32).ok()
        }
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
    struct F6(u128);

    impl IpNum for F6 {
        const BITS: u32 = 128;
        const MAX: Self = F6(u128::MAX);
        fn as_u128(self) -> u128 {
            self.0
        }
        fn from_u128(v: u128) -> Self {
            F6(v)
        }
    }

    impl FamilyImpl for F6 {
        const FAMILY: Family = Family::V6;

        fn parse_addr(_t: &str) -> Result<Self, String> {
            // Only the family-flag paths exercise F6; parsing never
            // happens (the binary family-mismatch test fails first).
            panic!("F6 test family: parsing is unreachable")
        }
        fn parse_cidr(_t: &str, _p: u32, _f: bool, _d: u32) -> Result<Range<Self>, String> {
            panic!("F6 test family: parsing is unreachable")
        }
        fn parse_prefix(_t: &str) -> Result<u32, String> {
            panic!("F6 test family: parsing is unreachable")
        }
        fn fmt_addr(_a: Self) -> String {
            panic!("F6 test family: formatting is unreachable")
        }
        fn fmt_cidr(_a: Self, _p: u32) -> String {
            panic!("F6 test family: formatting is unreachable")
        }
        fn convert_foreign(_t: &str) -> Option<Range<Self>> {
            panic!("F6 test family: conversion is unreachable")
        }
    }

    /// The load-path error as text, for assertions that compare only
    /// ASCII diagnostics. Tests that pin non-UTF-8 bytes compare
    /// `Diag::bytes()` against the expected byte string.
    fn diag_text(d: &Diag) -> String {
        String::from_utf8_lossy(d.bytes()).into_owned()
    }

    fn opts() -> Options {
        Options::default()
    }

    fn path_spec(arg: &str) -> SourceSpec {
        SourceSpec {
            kind: SourceKind::Path,
            arg: Some(PathBuf::from(arg)),
            label: None,
        }
    }

    /// The released legacy surface reads its argv paths with a plain blocking
    /// open, exactly as the C oracle's `fopen` does, so a FIFO whose writer
    /// shows up later is a supported source. Routing this path through the
    /// never-blocking open that the JSON-RPC arms use (`io::caller_open`) would
    /// turn a valid delayed-producer invocation into a refusal, so this pin
    /// fails if the legacy path ever stops waiting for the writer.
    #[cfg(unix)]
    #[test]
    fn fifo_with_a_delayed_producer_loads_like_a_regular_file() {
        use std::io::Write as _;
        use std::os::unix::ffi::OsStrExt as _;
        use std::sync::mpsc;
        use std::thread;
        use std::time::{Duration, Instant};

        const PAYLOAD: &str = "1.2.3.4\n5.6.7.8\n";
        const EXPECTED: [Range<F4>; 2] = [
            Range {
                lo: F4(0x01020304),
                hi: F4(0x01020304),
            },
            Range {
                lo: F4(0x05060708),
                hi: F4(0x05060708),
            },
        ];

        let temp = TempDir::new("fifo-delayed");
        let fifo = temp.path.join("in.fifo");
        let name = std::ffi::CString::new(fifo.as_os_str().as_bytes()).unwrap();
        assert_eq!(
            unsafe { libc::mkfifo(name.as_ptr(), 0o600) },
            0,
            "mkfifo: {}",
            std::io::Error::last_os_error()
        );

        // The writer opens the FIFO long after the loader started, which is
        // what makes this a blocking-open test rather than an ordering test.
        let writer_path = fifo.clone();
        thread::Builder::new()
            .name("iprange-legacy-fifo-writer".to_owned())
            .spawn(move || {
                thread::sleep(Duration::from_millis(150));
                let mut file = std::fs::OpenOptions::new()
                    .write(true)
                    .open(&writer_path)
                    .expect("open the fifo for writing");
                file.write_all(PAYLOAD.as_bytes()).expect("write the fifo");
                file.flush().expect("flush the fifo");
                drop(file);
            })
            .expect("spawn the delayed fifo writer");

        // The load runs off-thread with a bounded join so a regression that
        // refuses the FIFO (or loses the writer and waits forever) fails this
        // test instead of hanging the suite.
        let (sender, receiver) = mpsc::channel();
        let load_path = fifo.to_string_lossy().into_owned();
        thread::Builder::new()
            .name("iprange-legacy-fifo-loader".to_owned())
            .spawn(move || {
                let mut options = opts();
                options.sources.push(path_spec(&load_path));
                let answer = load_all_impl::<F4>(&options, &mut std::io::Cursor::new(Vec::new()))
                    .map(|loaded| {
                        loaded
                            .sets
                            .into_iter()
                            .map(|entry| (entry.name, entry.set.ranges))
                            .collect::<Vec<_>>()
                    });
                let _ = sender.send(answer);
            })
            .expect("spawn the fifo loader");
        let started = Instant::now();
        let loaded = receiver
            .recv_timeout(Duration::from_secs(30))
            .expect("the legacy loader must finish waiting for the delayed writer")
            .expect("a fifo argv input with a delayed producer must load, not be refused");
        assert!(
            started.elapsed() >= Duration::from_millis(100),
            "the loader returned before the producer wrote the fifo"
        );
        assert_eq!(loaded.len(), 1, "one fifo source loads one set");
        assert_eq!(loaded[0].0, OsStr::new(&fifo), "named by the argv path");
        assert_eq!(loaded[0].1, EXPECTED.to_vec(), "the fifo payload loaded");
    }

    struct TempDir {
        path: std::path::PathBuf,
    }

    impl TempDir {
        fn new(tag: &str) -> TempDir {
            let path = std::env::temp_dir().join(format!(
                "iprange-parse-test-{}-{}-{tag}",
                std::process::id(),
                std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap()
                    .as_nanos()
            ));
            std::fs::create_dir_all(&path).unwrap();
            TempDir { path }
        }
        fn file(&self, name: &str, content: &str) -> String {
            let p = self.path.join(name);
            std::fs::write(&p, content).unwrap();
            p.to_string_lossy().into_owned()
        }
    }

    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.path);
        }
    }

    // ------------------------------------------------------------------
    // fgets record iteration (C MAX_LINE = 1024)
    // ------------------------------------------------------------------

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
        let o = opts();
        assert_eq!(
            parse_token::<F4>("1.2.3.4", &o).unwrap(),
            Range {
                lo: F4(0x01020304),
                hi: F4(0x01020304)
            }
        );
        // fix-network default: host bits masked, broadcast filled.
        assert_eq!(
            parse_token::<F4>("1.2.3.99/24", &o).unwrap(),
            Range {
                lo: F4(0x01020300),
                hi: F4(0x010203ff)
            }
        );
        let mut o = opts();
        o.dont_fix_network = true;
        assert_eq!(
            parse_token::<F4>("1.2.3.99/24", &o).unwrap(),
            Range {
                lo: F4(0x01020363),
                hi: F4(0x010203ff)
            }
        );
    }

    // ------------------------------------------------------------------
    // load_all: expansion, naming, group-b order, stdin, drops, errors
    // ------------------------------------------------------------------

    #[test]
    fn file_list_expansion_keeps_list_order_and_names() {
        let t = TempDir::new("list");
        let a = t.file("a.txt", "1.2.3.4\n");
        let b = t.file("b.txt", "5.6.7.8\n");
        let list = t.file("list.txt", &format!("{a}\n# comment\n\n  {b}  \n"));
        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(PathBuf::from(list.clone())),
            label: None,
        });
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        assert_eq!(loaded.sets.len(), 2);
        assert_eq!(loaded.sets[0].name, OsStr::new(&a));
        assert_eq!(loaded.sets[1].name, OsStr::new(&b));
        assert_eq!(
            loaded.sets[0].set.ranges,
            vec![Range {
                lo: F4(0x01020304),
                hi: F4(0x01020304)
            }]
        );
        assert_eq!(
            loaded.sets[1].set.ranges,
            vec![Range {
                lo: F4(0x05060708),
                hi: F4(0x05060708)
            }]
        );
    }

    #[test]
    fn directory_expansion_sorts_by_name_includes_hidden_skips_subdirs() {
        let t = TempDir::new("dir");
        std::fs::create_dir_all(t.path.join("sub")).unwrap();
        let _sub = t.file("sub/file.txt", "1.2.3.4\n");
        t.file("z.txt", "9.9.9.9\n");
        t.file(".hidden", "1.1.1.1\n");
        t.file("a.txt", "2.2.2.2\n");
        let dir = t.path.to_string_lossy().into_owned();
        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(PathBuf::from(dir.clone())),
            label: None,
        });
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        assert_eq!(loaded.sets.len(), 3, "subdirectory must be skipped");
        assert_eq!(loaded.sets[0].name, OsStr::new(&format!("{dir}/.hidden")));
        assert_eq!(loaded.sets[1].name, OsStr::new(&format!("{dir}/a.txt")));
        assert_eq!(loaded.sets[2].name, OsStr::new(&format!("{dir}/z.txt")));
        assert_eq!(loaded.sets[0].set.ranges[0].lo, F4(0x01010101));
    }

    #[test]
    fn as_label_renames_the_last_set_of_a_source() {
        let t = TempDir::new("label");
        let a = t.file("a.txt", "1.2.3.4\n");
        let b = t.file("b.txt", "5.6.7.8\n");
        let list = t.file("list.txt", &format!("{a}\n{b}\n"));
        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(PathBuf::from(list)),
            label: Some(OsString::from("LABEL")),
        });
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        assert_eq!(loaded.sets.len(), 2);
        assert_eq!(loaded.sets[0].name, OsStr::new(&a));
        assert_eq!(loaded.sets[1].name, "LABEL");
    }

    #[test]
    fn stdin_handling_names_and_eof() {
        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::Path,
            arg: None,
            label: None,
        });
        o.sources.push(SourceSpec {
            kind: SourceKind::Path,
            arg: None,
            label: None,
        });
        let input = b"1.2.3.4\n5.6.7.8\n";
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(input.to_vec())).unwrap();
        assert_eq!(loaded.sets.len(), 2);
        assert_eq!(loaded.sets[0].name, "stdin");
        assert_eq!(loaded.sets[0].set.lines, 2);
        // The second `-` sees EOF: an empty but valid set.
        assert_eq!(loaded.sets[1].name, "stdin");
        assert_eq!(loaded.sets[1].set.lines, 0);
        assert!(loaded.sets[1].set.ranges.is_empty());
    }

    #[test]
    fn empty_argument_reads_stdin_like_c() {
        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::Path,
            arg: Some(PathBuf::new()),
            label: Some(OsString::from("RENAMED")),
        });
        let loaded =
            load_all_impl::<F4>(&o, &mut std::io::Cursor::new(b"9.9.9.9\n".to_vec())).unwrap();
        assert_eq!(loaded.sets.len(), 1);
        assert_eq!(loaded.sets[0].name, "RENAMED");
        assert_eq!(loaded.sets[0].set.ranges[0].lo, F4(0x09090909));
    }

    #[test]
    fn group_b_keeps_all_sources_in_argv_order() {
        let t = TempDir::new("group");
        let a = t.file("a.txt", "1.1.1.1\n");
        let b = t.file("b.txt", "2.2.2.2\n");
        let c = t.file("c.txt", "3.3.3.3\n");
        let mut o = opts();
        o.sources.push(path_spec(&a));
        o.sources.push(path_spec(&b));
        o.sources.push(path_spec(&c));
        o.group_b = Some(2); // ops splits A/B; parse must not reorder
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        let names: Vec<String> = loaded
            .sets
            .iter()
            .map(|l| l.name.to_string_lossy().into_owned())
            .collect();
        assert_eq!(names, vec![a.as_str(), b.as_str(), c.as_str()]);
    }

    #[test]
    fn lines_counter_counts_added_entries_and_adjacency_merges() {
        let t = TempDir::new("lines");
        // Adjacent appends merge on disk but still count as added
        // entries (C ipset_added_entry increments lines first).
        let f = t.file("f.txt", "1.2.3.4\n1.2.3.5\n# c\n\n");
        let mut o = opts();
        o.sources.push(path_spec(&f));
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        assert_eq!(loaded.sets[0].set.lines, 2);
        assert_eq!(loaded.sets[0].set.entries, 1);

        let f2 = t.file("g.txt", "# c\n\n1.2.3.4\n");
        let mut o = opts();
        o.sources.push(path_spec(&f2));
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        assert_eq!(loaded.sets[0].set.lines, 1);
        assert_eq!(loaded.sets[0].set.ranges.len(), 1);
    }

    #[test]
    fn ipv4_mode_drops_unparseable_colon_lines_with_counter() {
        let t = TempDir::new("drop");
        let f = t.file("f.txt", "x:y:z\nq:r:s\n");
        let mut o = opts();
        o.sources.push(path_spec(&f));
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        assert!(loaded.sets[0].set.ranges.is_empty());
        assert_eq!(loaded.sets[0].set.lines, 0);
    }

    #[test]
    fn ipv4_mode_converts_mapped_ipv6_lines() {
        let t = TempDir::new("mapped");
        let f = t.file("f.txt", "::ffff:1.2.3.4\n1.2.3.5\n");
        let mut o = opts();
        o.sources.push(path_spec(&f));
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        // The mapped line converts to 1.2.3.4 and the adjacent
        // 1.2.3.5 merges into it (C ipset_added_entry adjacency).
        assert_eq!(
            loaded.sets[0].set.ranges,
            vec![Range {
                lo: F4(0x01020304),
                hi: F4(0x01020305)
            }]
        );
        assert_eq!(loaded.sets[0].set.entries, 1);
        assert_eq!(
            loaded.sets[0].set.lines, 2,
            "converted line counts as one entry"
        );
    }

    #[cfg(unix)]
    #[test]
    fn nonexistent_files_and_lists_produce_exact_c_errors() {
        let missing = "/nonexistent/iprange-parse-test-file";
        let mut o = opts();
        o.sources.push(path_spec(missing));
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();
        assert_eq!(
            diag_text(&err),
            format!(
                "iprange: {missing} - No such file or directory\niprange: Cannot load ipset: {missing}"
            )
        );

        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(PathBuf::from("/nonexistent/iprange-parse-test-list")),
            label: None,
        });
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();
        assert_eq!(
            diag_text(&err),
            "iprange: Cannot access /nonexistent/iprange-parse-test-list: No such file or directory"
        );
    }

    #[cfg(windows)]
    #[test]
    fn nonexistent_files_and_lists_report_the_missing_path() {
        // Windows reports its own local error text; the stable contract
        // is that the exact failing path is identified. The numeric
        // system code is locale-independent.
        let missing = "/nonexistent/iprange-parse-test-file";
        let mut o = opts();
        o.sources.push(path_spec(missing));
        let err =
            diag_text(&load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err());
        assert!(
            err.contains(missing),
            "missing file error names the path: {err}"
        );
        assert!(
            err.contains("os error"),
            "missing file carries the system error code: {err}"
        );
        assert!(
            err.contains("Cannot load ipset"),
            "missing file keeps the C context: {err}"
        );

        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(PathBuf::from("/nonexistent/iprange-parse-test-list")),
            label: None,
        });
        let err =
            diag_text(&load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err());
        assert!(
            err.contains("/nonexistent/iprange-parse-test-list"),
            "missing list names the path: {err}"
        );
        assert!(
            err.contains("Cannot access"),
            "missing list keeps the C context: {err}"
        );
    }

    #[test]
    fn empty_directory_and_empty_list_errors() {
        let t = TempDir::new("empty");
        let dir = t.path.to_string_lossy().into_owned();
        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(PathBuf::from(dir.clone())),
            label: None,
        });
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();
        assert_eq!(
            diag_text(&err),
            format!("iprange: No valid files found in directory: {dir}")
        );

        let list = t.file("list.txt", "# nothing\n\n");
        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(PathBuf::from(list.clone())),
            label: None,
        });
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();
        assert_eq!(
            diag_text(&err),
            format!("iprange: No valid files found in file list: {list}")
        );
    }

    #[test]
    fn parse_errors_fail_the_file_with_context() {
        let t = TempDir::new("parseerr");
        let f = t.file("f.txt", "1.2.3.4\n1.2.3.4 5.6.7.8\n9.9.9.9\n");
        let mut o = opts();
        o.sources.push(path_spec(&f));
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();
        assert_eq!(diag_text(&err), format!("iprange: Cannot load ipset: {f}"));
    }

    #[test]
    fn binary_family_mismatches_fail_before_parsing() {
        let t = TempDir::new("bimm");
        // v2 header in v4 mode: never reaches binary::load_v2.
        let f = t.file("v2.bin", "iprange binary format v2.0\n");
        let mut o = opts();
        o.sources.push(path_spec(&f));
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();
        // Every ipset_load() failure is followed by the caller's
        // context line (src/iprange.c:911), exactly like the
        // open-failure family above.
        assert_eq!(
            diag_text(&err),
            format!(
                "iprange: {f}: IPv6 binary file cannot be loaded in IPv4 mode (use -6)\niprange: Cannot load ipset: {f}"
            )
        );

        // v1 header in v6 mode: never reaches binary::load_v1.
        let g = t.file("v1.bin", "iprange binary format v1.0\n");
        let mut o = opts();
        o.family = Family::V6;
        o.sources.push(path_spec(&g));
        let err = load_all_impl::<F6>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();
        assert_eq!(
            diag_text(&err),
            format!(
                "iprange: {g}: IPv4 binary file cannot be loaded in IPv6 mode\niprange: Cannot load ipset: {g}"
            )
        );
    }

    #[test]
    fn empty_file_is_a_valid_empty_set() {
        let t = TempDir::new("emptyfile");
        let f = t.file("f.txt", "");
        let mut o = opts();
        o.sources.push(path_spec(&f));
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap();
        assert_eq!(loaded.sets[0].name, OsStr::new(&f));
        assert!(loaded.sets[0].set.ranges.is_empty());
        assert_eq!(loaded.sets[0].set.lines, 0);
    }

    // ------------------------------------------------------------------
    // Diagnostic text units
    // ------------------------------------------------------------------

    #[test]
    fn diagnosis_texts_are_c_exact() {
        // The raw record keeps its newline, so the C output ends with
        // the embedded newline plus the format's closing newline.
        assert_eq!(
            diag_text(&fmt_cannot_understand(3, b"f.txt", b"bad line\n")),
            "iprange: Cannot understand line No 3 from f.txt: bad line\n"
        );
        assert_eq!(
            diag_text(&fmt_cannot_understand(1, b"f.txt", b"last")),
            "iprange: Cannot understand line No 1 from f.txt: last"
        );
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
        assert_eq!(
            fmt_mixed_family(2, "::1", "1.2.3.4"),
            "iprange: Mixed-family range on line 2: ::1 - 1.2.3.4"
        );
        assert_eq!(
            diag_text(&fmt_drop_warning(b"f.txt", 3)),
            "iprange: f.txt: 3 IPv6 entries dropped (use -6 for IPv6 mode)"
        );
    }

    #[cfg(unix)]
    #[test]
    fn load_diagnostics_keep_the_name_bytes() {
        use std::os::unix::ffi::OsStrExt;
        // A POSIX name may hold bytes that are not valid UTF-8. C opens
        // those bytes and writes them to stderr verbatim (`fprintf
        // (stderr, "%s")` over the name), so the open-failure
        // diagnostic must carry them, not a U+FFFD rendering.
        let t = TempDir::new("namebytes");
        let mut raw = Vec::from(t.path.as_os_str().as_bytes());
        raw.extend_from_slice(b"/f\xff.txt");
        let missing = std::path::Path::new(OsStr::from_bytes(&raw));

        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::Path,
            arg: Some(missing.to_path_buf()),
            label: None,
        });
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();

        let mut want = Vec::from(b"iprange: ");
        want.extend_from_slice(&raw);
        want.extend_from_slice(b" - No such file or directory\niprange: Cannot load ipset: ");
        want.extend_from_slice(&raw);
        assert_eq!(err.bytes(), want.as_slice());
    }

    #[cfg(unix)]
    #[test]
    fn list_line_diagnostics_keep_the_name_bytes() {
        use std::os::unix::ffi::OsStrExt;
        // The same currency applies to the two channels an `@list` can
        // fail on: the name taken from the list record and the name of
        // the list itself.
        let t = TempDir::new("listbytes");
        let entry = Vec::from(b"nope\xffx.iprange");
        let list_raw = {
            let mut v = Vec::from(t.path.as_os_str().as_bytes());
            v.extend_from_slice(b"/badlist\xff.txt");
            v
        };
        let list = std::path::Path::new(OsStr::from_bytes(&list_raw));
        std::fs::write(list, b"nope\xffx.iprange\n").unwrap();

        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(list.to_path_buf()),
            label: None,
        });
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();

        let mut want = Vec::from(b"iprange: ");
        want.extend_from_slice(&entry);
        want.extend_from_slice(b" - No such file or directory\niprange: Cannot load file ");
        want.extend_from_slice(&entry);
        want.extend_from_slice(b" from list ");
        want.extend_from_slice(&list_raw);
        want.extend_from_slice(b" (line 1)");
        assert_eq!(err.bytes(), want.as_slice());
    }

    #[cfg(unix)]
    #[test]
    fn directory_entry_diagnostics_keep_the_name_bytes() {
        use std::os::unix::ffi::OsStrExt;
        // An `@dir` whose name is not valid UTF-8 fails with the raw
        // bytes in `Cannot access ...`.
        let mut raw = Vec::from(std::env::temp_dir().as_os_str().as_bytes());
        raw.extend_from_slice(b"/iprange-parse-missing-dir-\xff");
        let dir = std::path::Path::new(OsStr::from_bytes(&raw));

        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(dir.to_path_buf()),
            label: None,
        });
        let err = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new())).unwrap_err();

        let mut want = Vec::from(b"iprange: Cannot access ");
        want.extend_from_slice(&raw);
        want.extend_from_slice(b": No such file or directory");
        assert_eq!(err.bytes(), want.as_slice());
    }

    #[cfg(unix)]
    #[test]
    fn list_lines_open_the_exact_bytes_of_the_name() {
        use std::os::unix::ffi::OsStrExt;
        // A file list names its files by bytes (C `fopen(line, "r")`
        // over the record verbatim). Decoding the line to build the
        // path opens a *different* file: the U+FFFD name does not
        // exist, so the load fails with rc 1 where C merges and exits
        // 0, and the CSV name column would carry the decoded name.
        let t = TempDir::new("listopen");
        let name = Vec::from(b"bad\xffname.iprange");
        let full = {
            let mut v = Vec::from(t.path.as_os_str().as_bytes());
            v.push(b'/');
            v.extend_from_slice(&name);
            v
        };
        std::fs::write(std::path::Path::new(OsStr::from_bytes(&full)), b"4.4.4.4\n").unwrap();
        let list = t.path.join("list.txt");
        {
            let mut rec = Vec::from(&full[..]);
            rec.push(b'\n');
            std::fs::write(&list, &rec).unwrap();
        }

        let mut o = opts();
        o.sources.push(SourceSpec {
            kind: SourceKind::FileList,
            arg: Some(list),
            label: None,
        });
        let loaded = load_all_impl::<F4>(&o, &mut std::io::Cursor::new(Vec::new()))
            .expect("the list line names an existing file by its bytes");
        assert_eq!(loaded.sets.len(), 1);
        assert_eq!(
            loaded.sets[0].name.as_os_str().as_bytes(),
            full.as_slice(),
            "the CSV name is the list record bytes"
        );
        assert_eq!(loaded.sets[0].set.ranges.len(), 1);
    }

    #[cfg(unix)]
    #[test]
    fn cannot_understand_keeps_the_raw_record_bytes() {
        // The C line buffer is echoed with `%s`, so a record holding a
        // non-UTF-8 byte reaches stderr as those bytes.
        let d = fmt_cannot_understand(2, b"f.txt", b"1.2.3.4/9\xff\n");
        assert_eq!(
            d.bytes(),
            b"iprange: Cannot understand line No 2 from f.txt: 1.2.3.4/9\xff\n"
        );
    }
}
