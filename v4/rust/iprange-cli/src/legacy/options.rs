//! Legacy CLI options: the exact option grammar and defaults of the
//! released `iprange` command line (SOW-0028 delivery step 3).

use crate::legacy::family::Family;

/// How a group of input paths turns into ipsets.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SourceKind {
    /// A positional file path (or `-` for stdin).
    Path,
    /// `@file` (or `@dir`): the destination is classified on load
    /// (a directory expands to its regular files sorted by name).
    FileList,
}

/// One input argument and its optional `as NAME` label.
#[derive(Clone, Debug)]
pub struct SourceSpec {
    pub kind: SourceKind,
    /// The path argument as given, or None for stdin (`-`).
    pub arg: Option<String>,
    /// `as NAME` rename for CSV output; None keeps the default name.
    pub label: Option<String>,
}

/// The mode-selecting options (C `mode` enum); the last mode flag in
/// argv order wins.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Mode {
    #[default]
    Merge,
    Common,
    ExcludeNext,
    Diff,
    Compare,
    CompareFirst,
    CompareNext,
    CountUnique,
    CountUniqueAll,
    /// `--ipset-reduce N` (IPv4 only; rejected in IPv6 mode).
    Reduce,
}

/// Print-shape flags (accepted by merge/common/exclude/diff/reduce;
/// ignored by the CSV modes).
#[derive(Clone, Debug, Default)]
pub struct Print {
    /// `--print-ranges` / `-j`.
    pub ranges: bool,
    /// `--print-single-ips` / `-1`.
    pub single_ips: bool,
    /// `--print-binary`.
    pub binary: bool,
    /// Prefix/suffix wrappers; the family splits ips and nets sets.
    pub prefix: String,
    pub suffix: String,
    pub prefix_ips: String,
    pub suffix_ips: String,
    pub prefix_nets: String,
    pub suffix_nets: String,
}

/// Everything the released CLI parses out of argv.
#[derive(Clone, Debug)]
pub struct Options {
    /// `-4`/`--ipv4` or `-6`/`--ipv6`; the default family is IPv4.
    pub family: Family,
    pub mode: Mode,
    /// Input sources in argv order, with labels.
    pub sources: Vec<SourceSpec>,
    /// Index of the first group-B source: set by the first positional
    /// operator (--except/--diff/--compare-next); everything after it
    /// forms group B.
    pub group_b: Option<usize>,
    /// `--dont-fix-network`: CIDR host addresses keep their raw
    /// address instead of being masked to the network address.
    pub dont_fix_network: bool,
    /// `--default-prefix N` / `-p N` (IPv4 only; 0..=32, default 32;
    /// accepted but ignored in IPv6 mode, always /128 there).
    pub default_prefix: u32,
    /// Enabled prefix lengths for CIDR decomposition; [33] for IPv4,
    /// [129] for IPv6, all true initially. `/32` (v6 `/128`) is never
    /// disabled. Mutated by `--min-prefix`, `--prefixes`, and reduce.
    pub prefix4_enabled: [bool; 33],
    pub prefix6_enabled: [bool; 129],
    /// `--ipset-reduce N` percent (stored as 100 + N), default 120.
    pub reduce_factor: u64,
    /// `--ipset-reduce-entries N` minimum accepted entries, default
    /// 16384.
    pub reduce_entries: u64,
    /// `--dns-threads N`, default 5.
    pub dns_threads: u32,
    /// `--dns-silent`.
    pub dns_silent: bool,
    /// `--dns-progress` (IPv4 only; inert in IPv6 mode).
    pub dns_progress: bool,
    /// `-v`: debug messages to stderr.
    pub debug: bool,
    /// `--quiet` (diff only): suppress diff output, keep the exit
    /// code.
    pub quiet: bool,
    /// `--header`: CSV header line in the count/compare modes.
    pub header: bool,
    /// `--print-*` wrappers and shape flags.
    pub print: Print,
}

impl Default for Options {
    fn default() -> Self {
        Options {
            family: Family::V4,
            mode: Mode::Merge,
            sources: Vec::new(),
            group_b: None,
            dont_fix_network: false,
            default_prefix: 32,
            prefix4_enabled: [true; 33],
            prefix6_enabled: [true; 129],
            reduce_factor: 120,
            reduce_entries: 16384,
            dns_threads: 5,
            dns_silent: false,
            dns_progress: false,
            debug: false,
            quiet: false,
            header: false,
            print: Print::default(),
        }
    }
}

impl Options {
    /// The prefix-enabled array for the selected family.
    pub fn enabled(&self) -> &[bool] {
        match self.family {
            Family::V4 => &self.prefix4_enabled,
            Family::V6 => &self.prefix6_enabled,
        }
    }

    /// The prefix-enabled array for the selected family, mutably.
    pub fn enabled_mut(&mut self) -> &mut [bool] {
        match self.family {
            Family::V4 => &mut self.prefix4_enabled,
            Family::V6 => &mut self.prefix6_enabled,
        }
    }
}
