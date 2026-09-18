//! Streaming parser for released legacy-compatible IP-range text and binary.
//!
//! The parser keeps only one bounded line and one bounded range batch in
//! memory. JSON-RPC publication and the future released legacy surface share
//! this adapter; neither caller receives a complete materialized feed.

use std::collections::VecDeque;
use std::fs::File;
use std::io::{self, BufRead, BufReader, Read};
use std::net::{IpAddr, Ipv6Addr, ToSocketAddrs};
use std::path::{Path, PathBuf};

use iprange_livedb::error::Error;
use iprange_livedb::{AddressRange, Ipv4Key, Ipv6Key, RangeSource};

const BATCH_CAPACITY: usize = 256;
const HOSTNAME_BATCH_CAPACITY: usize = 4096;
const BINARY_V4_HEADER: &[u8] = b"iprange binary format v1.0";
const BINARY_V6_HEADER: &[u8] = b"iprange binary format v2.0";
const ENDIAN_MARKER: u32 = 0x1a2b_3c4d;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AddressFamilyInput {
    Ipv4,
    Ipv6,
}

/// Released text-input controls that affect address normalization.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct TextInputOptions {
    pub family: AddressFamilyInput,
    pub fix_network: bool,
    pub default_prefix: u32,
    pub dns_threads: usize,
    pub dns_silent: bool,
    pub max_line_bytes: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum InputErrorKind {
    InvalidPath,
    Io,
    Format,
}

/// A bounded source error with the stable adapter classification.
#[derive(Debug)]
pub struct InputError {
    kind: InputErrorKind,
    message: String,
}

impl InputError {
    fn invalid_path(message: impl Into<String>) -> Self {
        Self {
            kind: InputErrorKind::InvalidPath,
            message: message.into(),
        }
    }

    fn io(message: impl Into<String>) -> Self {
        Self {
            kind: InputErrorKind::Io,
            message: message.into(),
        }
    }

    fn format(message: impl Into<String>) -> Self {
        Self {
            kind: InputErrorKind::Format,
            message: message.into(),
        }
    }

    pub fn code(&self) -> &'static str {
        match self.kind {
            InputErrorKind::InvalidPath => "invalid_path",
            InputErrorKind::Io => "io",
            InputErrorKind::Format => "input_format",
        }
    }

    pub fn message(&self) -> &str {
        &self.message
    }
}

impl From<io::Error> for InputError {
    fn from(value: io::Error) -> Self {
        Self::io(value.to_string())
    }
}

#[derive(Debug)]
enum ActiveInput {
    Text {
        reader: BufReader<File>,
        first_line: bool,
        dropped_ipv6: u64,
        hostnames: Vec<String>,
    },
    Binary {
        reader: BufReader<File>,
        remaining: u64,
        optimized: bool,
        /// C's DERIVED `payload_is_optimized` (src/ipset_binary.c:65-76,
        /// src/ipset6_binary.c:35-46): false once a record broke strict
        /// ascending, disjoint, non-adjacent order against its
        /// predecessor. It is NOT the header flag. C's v2 loader
        /// recomputes and compares the unique count while this holds
        /// (src/ipset6_binary.c:54-65) -- so a header spelling
        /// "non-optimized" over records that happen to be strictly
        /// ordered IS compared, exactly as C compares it -- and C
        /// trusts the header once it clears (adjacent records: disjoint,
        /// yet C stops recomputing). C also refuses a header claiming
        /// optimized over a payload whose derived flag cleared.
        payload_optimized: bool,
        /// False once any record started at or inside the envelope
        /// (maximum record end so far), i.e. the records seen are no
        /// longer pairwise disjoint. While true, C's merged unique
        /// count equals the running sum (adjacency and gaps do not
        /// change a disjoint union's cardinality), so the v1
        /// comparison is exact; once false, only the interval bound
        /// remains. Distinct from `payload_optimized`: adjacency
        /// clears that flag without making the sum inexact.
        count_exact: bool,
        expected_unique: u128,
        actual_unique: u128,
        /// Running maximum of record ends: the disjointness envelope.
        envelope: Option<u128>,
        /// Largest single-record cardinality seen so far. C's merged
        /// count is at least one record's size regardless of order or
        /// overlap and at most the running sum; when `count_exact` is
        /// false these two bounds bracket C's in-memory sort-merge,
        /// making both contradiction directions outside the interval
        /// decidable (see the end-of-payload comparison).
        max_unique: u128,
        previous: Option<(u128, u128)>,
    },
}

/// A finite, bounded-batch source over one or more expanded input paths.
#[derive(Debug)]
pub struct TextInputSource<K> {
    paths: VecDeque<PathBuf>,
    active_path: Option<String>,
    active: Option<ActiveInput>,
    options: TextInputOptions,
    /// Reusable text-line buffer: one allocation serves every line of
    /// every text input instead of a fresh Vec per line.
    line_buf: Vec<u8>,
    batch: Vec<AddressRange<K>>,
    /// Ranges parked while one resolution group overflows the fixed
    /// batch (astra gate finding P1-1): resolved answers arrive faster
    /// than the batch drains, so the surplus waits here and the next
    /// `next_family_batch` hands it out first. Without this, 257
    /// answers from a single hostname group used to fail the whole
    /// feed with "range does not fit the bounded parser batch/family"
    /// even though the batched publication contract only requires
    /// bounded batches, not bounded resolution groups. The queue holds
    /// at most one group's surplus (the drain runs before any new
    /// input read); the ring may keep its capacity after a drain --
    /// bounded amortization by design, not accumulation across groups.
    /// Scope note (security round-9): the group's answer fan-out is
    /// uncapped per name (the resolver keeps every address), exactly
    /// as the released C loader mallocs one reply node per answer; the
    /// publication max_heap_bytes budget guards the livedb heap, not
    /// this adapter, so "bounded memory" here means bounded batch and
    /// bounded queue per group, never a cap on resolver answers.
    pending: VecDeque<ParsedRange>,
    finished: bool,
    last_error: Option<&'static str>,
    last_message: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub(crate) struct ParsedRange {
    from: u128,
    to: u128,
    ipv4: bool,
}

#[derive(Debug, PartialEq)]
enum ParsedLine {
    Empty,
    Range(ParsedRange),
    Hostname(Vec<u8>),
    DroppedIpv6,
}

enum Step {
    /// One text line; the bytes live in the source's reusable line
    /// buffer, so producing a line never allocates.
    TextLine,
    TextFinished,
    BinaryRecord(ParsedRange),
    BinaryEnd,
}

impl ParsedRange {
    fn single(address: u128, ipv4: bool) -> Self {
        Self {
            from: address,
            to: address,
            ipv4,
        }
    }
}

pub(crate) trait InputKey {
    fn family_matches(range: ParsedRange) -> bool;
    fn address_range(range: ParsedRange) -> AddressRange<Self>
    where
        Self: Sized;
}

impl InputKey for Ipv4Key {
    fn family_matches(range: ParsedRange) -> bool {
        range.ipv4
    }

    fn address_range(range: ParsedRange) -> AddressRange<Self> {
        AddressRange {
            from: Ipv4Key(range.from as u32),
            to: Ipv4Key(range.to as u32),
        }
    }
}

impl InputKey for Ipv6Key {
    fn family_matches(range: ParsedRange) -> bool {
        !range.ipv4
    }

    fn address_range(range: ParsedRange) -> AddressRange<Self> {
        AddressRange {
            from: Ipv6Key::from_u128(range.from),
            to: Ipv6Key::from_u128(range.to),
        }
    }
}

impl<K: InputKey> TextInputSource<K> {
    pub fn new(
        paths: Vec<String>,
        options: TextInputOptions,
        expand_at_paths: bool,
        max_expanded_paths: usize,
    ) -> Result<Self, InputError> {
        if paths.is_empty() || paths.len() > max_expanded_paths {
            return Err(InputError::invalid_path(format!(
                "input path count must be 1 through {max_expanded_paths}"
            )));
        }
        let expanded = expand_paths(
            paths,
            expand_at_paths,
            max_expanded_paths,
            options.max_line_bytes,
        )?;
        Ok(Self {
            paths: expanded,
            active_path: None,
            active: None,
            options,
            line_buf: Vec::new(),
            batch: Vec::with_capacity(BATCH_CAPACITY),
            pending: VecDeque::new(),
            finished: false,
            last_error: None,
            last_message: None,
        })
    }

    /// Return the adapter classification of the most recent source failure.
    pub fn last_input_error(&self) -> Option<&'static str> {
        self.last_error
    }

    pub fn last_input_message(&self) -> Option<&str> {
        self.last_message.as_deref()
    }

    fn path_label(&self) -> &str {
        self.active_path.as_deref().unwrap_or("input")
    }

    fn open_next(&mut self) -> Result<bool, InputError> {
        while let Some(path) = self.paths.pop_front() {
            let display = path.to_string_lossy().into_owned();
            let file = open_input(&path).map_err(|error| {
                self.last_error = Some(error.code());
                let message = format!("{display}: {}", error.message);
                self.last_message = Some(message.clone());
                InputError {
                    kind: error.kind,
                    message,
                }
            })?;
            let mut reader = BufReader::new(file);
            let mut first = Vec::new();
            let mut synthetic_newline = false;
            let Some(had_newline) =
                read_limited_line(&mut reader, self.options.max_line_bytes, &mut first)?
            else {
                continue;
            };
            self.active_path = Some(display.clone());
            if had_newline {
                if self.options.family == AddressFamilyInput::Ipv6 {
                    strip_bom(&mut first);
                }
                if first == BINARY_V4_HEADER {
                    if self.options.family == AddressFamilyInput::Ipv6 {
                        return Err(self.format_error("IPv4 binary file cannot load in IPv6 mode"));
                    }
                    self.open_binary(reader, false)?;
                    return Ok(true);
                }
                if first == BINARY_V6_HEADER {
                    if self.options.family == AddressFamilyInput::Ipv4 {
                        return Err(self.format_error("IPv6 binary file cannot load in IPv4 mode"));
                    }
                    self.open_binary(reader, true)?;
                    return Ok(true);
                }
            } else {
                first.push(b'\n');
                if self.options.family == AddressFamilyInput::Ipv6 {
                    strip_bom(&mut first);
                }
                synthetic_newline = true;
            }
            self.active = Some(ActiveInput::Text {
                reader,
                first_line: true,
                dropped_ipv6: 0,
                hostnames: Vec::new(),
            });
            if synthetic_newline {
                first.pop();
            }
            let parsed = parse_text_line(&first, self.options)
                .map_err(|message| self.format_error(message))?;
            self.consume_parsed(parsed)
                .map_err(|error| self.remember(error))?;
            if let Some(ActiveInput::Text { first_line, .. }) = self.active.as_mut() {
                *first_line = false;
            }
            return Ok(true);
        }
        Ok(false)
    }

    fn open_binary(&mut self, mut reader: BufReader<File>, ipv6: bool) -> Result<(), InputError> {
        let record_size: u128 = if ipv6 { 32 } else { 8 };
        if ipv6 {
            require_binary_line(&mut reader, b"ipv6", self.options.max_line_bytes)?;
        }
        let optimized = match binary_line(&mut reader, self.options.max_line_bytes)? {
            Some(line) if line == b"optimized" => true,
            Some(line) if line == b"non-optimized" => false,
            _ => return Err(self.format_error("invalid binary optimized flag")),
        };
        let expected_size =
            binary_number(&mut reader, b"record size ", self.options.max_line_bytes)?
                .ok_or_else(|| self.format_error("invalid binary record size"))?;
        if expected_size != record_size {
            return Err(self.format_error("invalid binary record size"));
        }
        let records = binary_number(&mut reader, b"records ", self.options.max_line_bytes)?
            .ok_or_else(|| self.format_error("invalid binary record count"))?;
        let bytes = binary_number(&mut reader, b"bytes ", self.options.max_line_bytes)?
            .ok_or_else(|| self.format_error("invalid binary byte count"))?;
        let lines = binary_number(&mut reader, b"lines ", self.options.max_line_bytes)?
            .ok_or_else(|| self.format_error("invalid binary line count"))?;
        let expected_unique =
            binary_number(&mut reader, b"unique ips ", self.options.max_line_bytes)?
                .ok_or_else(|| self.format_error("invalid binary unique count"))?;
        let expected_bytes = record_size
            .checked_mul(records)
            .and_then(|value| value.checked_add(4))
            .ok_or_else(|| self.format_error("binary byte count overflows"))?;
        let records = u64::try_from(records)
            .map_err(|_| self.format_error("binary record count exceeds the platform bound"))?;
        if bytes != expected_bytes {
            return Err(self.format_error("binary byte count does not match records"));
        }
        if lines < u128::from(records) {
            return Err(self.format_error("binary line count is below record count"));
        }
        // C validates an empty payload's header in both loaders
        // (src/ipset_binary.c:49-53, src/ipset6_binary.c:22-26): zero
        // records with a nonzero "unique ips" is a mismatch, never a
        // vacuous pass. The comparison half below derives from the
        // streamed records; this one needs nothing but the header.
        if records == 0 && expected_unique != 0 {
            return Err(self.format_error("binary unique count does not match payload"));
        }
        if expected_unique < u128::from(records) && !(ipv6 && expected_unique == 0) {
            return Err(self.format_error("binary unique count is below record count"));
        }
        let mut marker = [0u8; 4];
        if let Err(error) = reader.read_exact(&mut marker) {
            return Err(self.io_error(format!("binary endianness marker: {error}")));
        }
        if u32::from_ne_bytes(marker) != ENDIAN_MARKER {
            return Err(self.format_error("binary endianness is incompatible"));
        }
        self.active = Some(ActiveInput::Binary {
            reader,
            remaining: records,
            optimized,
            payload_optimized: true,
            count_exact: true,
            envelope: None,
            expected_unique,
            actual_unique: 0,
            max_unique: 0,
            previous: None,
        });
        Ok(())
    }

    fn consume_parsed(&mut self, parsed: ParsedLine) -> Result<(), InputError> {
        match parsed {
            ParsedLine::Empty => Ok(()),
            ParsedLine::DroppedIpv6 => {
                if let Some(ActiveInput::Text { dropped_ipv6, .. }) = self.active.as_mut() {
                    *dropped_ipv6 += 1;
                }
                Ok(())
            }
            ParsedLine::Range(range) => self.push_range(range),
            ParsedLine::Hostname(hostname) => {
                let hostname = String::from_utf8(hostname)
                    .map_err(|_| self.format_error("hostname is not UTF-8"))?;
                let names = if let Some(ActiveInput::Text { hostnames, .. }) = self.active.as_mut()
                {
                    if hostnames.len() < HOSTNAME_BATCH_CAPACITY {
                        hostnames.push(hostname);
                        None
                    } else {
                        let mut names = vec![hostname];
                        std::mem::swap(&mut names, hostnames);
                        Some(names)
                    }
                } else {
                    return Err(self.format_error("text input is not active"));
                };
                match names {
                    Some(names) => self.resolve_names(names),
                    None => Ok(()),
                }
            }
        }
    }

    fn resolve_names(&mut self, names: Vec<String>) -> Result<(), InputError> {
        let addresses = resolve_hostnames(
            &names,
            self.options.family,
            self.options.dns_threads,
            self.options.dns_silent,
        )
        .map_err(|message| self.format_error(message))?;
        for address in addresses {
            let range = dns_range(address, self.options.family)
                .map_err(|message| self.format_error(message))?;
            self.push_range(range)?;
        }
        Ok(())
    }

    fn push_range(&mut self, range: ParsedRange) -> Result<(), InputError>
    where
        K: InputKey,
    {
        if !K::family_matches(range) {
            return Err(self.format_error("range does not fit the bounded parser batch/family"));
        }
        if self.batch.len() >= BATCH_CAPACITY {
            // A resolution group can outpace the drain: one group of
            // up to HOSTNAME_BATCH_CAPACITY names answers with more
            // addresses than the batch has slots (the batch itself is
            // filled one range per loop step elsewhere, so this is the
            // only surplus path and the queue stays bounded by the
            // group). The surplus parks FIFO and the next batch hands
            // it out first, instead of failing the feed. Family
            // refusals stay errors: only capacity is deferred.
            self.pending.push_back(range);
        } else {
            self.batch.push(K::address_range(range));
        }
        Ok(())
    }

    fn format_error(&mut self, message: impl Into<String>) -> InputError {
        self.last_error = Some("input_format");
        let error = InputError::format(message);
        self.last_message = Some(error.message().to_owned());
        error
    }

    fn io_error(&mut self, message: impl Into<String>) -> InputError {
        self.last_error = Some("io");
        let error = InputError::io(message);
        self.last_message = Some(error.message().to_owned());
        error
    }

    fn remember(&mut self, error: InputError) -> InputError {
        self.last_error = Some(error.code());
        self.last_message = Some(error.message().to_owned());
        error
    }
}

impl RangeSource<AddressRange<Ipv4Key>> for TextInputSource<Ipv4Key> {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv4Key>]>> {
        self.next_family_batch()
            .map(|option| option.map(|()| self.batch.as_slice()))
    }
}

impl RangeSource<AddressRange<Ipv6Key>> for TextInputSource<Ipv6Key> {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv6Key>]>> {
        self.next_family_batch()
            .map(|option| option.map(|()| self.batch.as_slice()))
    }
}

impl<K: InputKey> TextInputSource<K> {
    fn next_family_batch(&mut self) -> iprange_livedb::Result<Option<()>> {
        if self.finished && self.pending.is_empty() {
            return Ok(None);
        }
        self.batch.clear();
        loop {
            // Parked surplus first: a previous resolution group may
            // have filled the batch and deferred the rest (P1-1).
            while self.batch.len() < BATCH_CAPACITY {
                match self.pending.pop_front() {
                    Some(range) => self.batch.push(K::address_range(range)),
                    None => break,
                }
            }
            if self.batch.len() >= BATCH_CAPACITY {
                return Ok(Some(()));
            }
            if self.active.is_none() {
                match self.open_next().map_err(input_sdk_error)? {
                    true => {}
                    false => {
                        self.finished = true;
                        if self.batch.is_empty() && self.pending.is_empty() {
                            return Ok(None);
                        }
                        return Ok(Some(()));
                    }
                }
            }

            let step = match self.read_step() {
                Ok(step) => step,
                Err(error) => return Err(input_sdk_error(self.remember(error))),
            };
            match step {
                Step::TextLine => {
                    let parsed = parse_text_line(&self.line_buf, self.options)
                        .map_err(|message| self.format_error(message))
                        .map_err(input_sdk_error)?;
                    self.consume_parsed(parsed)
                        .map_err(|error| self.remember(error))
                        .map_err(input_sdk_error)?;
                }
                Step::TextFinished => {
                    let (dropped_ipv6, hostnames) = match self.active.take() {
                        Some(ActiveInput::Text {
                            dropped_ipv6,
                            hostnames,
                            ..
                        }) => (dropped_ipv6, hostnames),
                        _ => (0, Vec::new()),
                    };
                    if !hostnames.is_empty() {
                        self.resolve_names(hostnames)
                            .map_err(|error| self.remember(error))
                            .map_err(input_sdk_error)?;
                    }
                    if dropped_ipv6 > 0 {
                        stderr_diag(format!(
                            "iprange: {}: {dropped_ipv6} IPv6 entries dropped (use -6 for IPv6 mode)",
                            self.path_label()
                        ));
                    }
                    self.active_path = None;
                }
                Step::BinaryRecord(record) => {
                    if let Some(ActiveInput::Binary { remaining, .. }) = self.active.as_mut() {
                        *remaining -= 1;
                    }
                    self.push_range(record)
                        .map_err(|error| self.remember(error))
                        .map_err(input_sdk_error)?;
                }
                Step::BinaryEnd => {
                    self.active = None;
                    self.active_path = None;
                }
            }
        }
    }

    fn read_step(&mut self) -> Result<Step, InputError> {
        match self.active.as_mut() {
            Some(ActiveInput::Text { reader, .. }) => {
                if read_limited_line(reader, self.options.max_line_bytes, &mut self.line_buf)?
                    .is_some()
                {
                    Ok(Step::TextLine)
                } else {
                    Ok(Step::TextFinished)
                }
            }
            Some(ActiveInput::Binary {
                reader,
                remaining,
                optimized,
                payload_optimized,
                count_exact,
                envelope,
                expected_unique,
                actual_unique,
                max_unique,
                previous,
            }) => {
                let ipv6 = self.options.family == AddressFamilyInput::Ipv6;
                if *remaining == 0 {
                    let mut trailing = [0u8; 1];
                    let count = reader
                        .read(&mut trailing)
                        .map_err(|error| InputError::io(format!("check binary tail: {error}")))?;
                    if count != 0 {
                        return Err(InputError::format("trailing data after binary payload"));
                    }
                    // The unique-count comparison per family, mirroring
                    // the authoritative readers exactly (parity
                    // round-9 F2; astra P1-3 for the v2 wrap).
                    //
                    // v1 (C src/ipset_binary.c:41-140, legacy/binary.rs
                    // validate_payload_v1): EVERY payload is compared,
                    // sort-merged when unordered. A clean (strictly
                    // ascending, disjoint, non-adjacent) payload's
                    // running sum is exactly C's merged count, so it
                    // is compared in full -- including a
                    // non-optimized header C would also check (the
                    // case the old `optimized &&` guard skipped). For
                    // an unordered payload the sum over-counts
                    // overlaps, so only the derivable direction is
                    // refused: merged <= sum, hence a header ABOVE the
                    // sum contradicts C's own comparison and is
                    // rejected here exactly as C rejects it. A header
                    // at-or-below the sum of a contradictory payload
                    // cannot be counted by a bounded stream; that
                    // residual is one-sided (it can only believe
                    // FEWER addresses than C's merged count, never
                    // more) and is stated here rather than hidden.
                    //
                    // v2 (C src/ipset6_binary.c:13-70): only an
                    // optimized header is recomputed; a non-optimized
                    // v2 header is trusted verbatim (C's own comment),
                    // so no comparison runs for it -- refusing one
                    // would reject input both legacy readers load.
                    let mismatch = if ipv6 {
                        // v2: C recomputes and compares while its
                        // DERIVED flag holds (src/ipset6_binary.c:54-65),
                        // and trusts the header once it clears -- so
                        // this keys on `payload_optimized`, not on the
                        // header claim and not on `count_exact`.
                        *payload_optimized && *actual_unique != *expected_unique
                    } else if *count_exact {
                        *actual_unique != *expected_unique
                    } else {
                        *expected_unique > *actual_unique || *expected_unique < *max_unique
                    };
                    if mismatch {
                        return Err(InputError::format(
                            "binary unique count does not match payload",
                        ));
                    }
                    return Ok(Step::BinaryEnd);
                }
                let size = if ipv6 { 32 } else { 8 };
                let mut bytes = [0u8; 32];
                reader
                    .read_exact(&mut bytes[..size])
                    .map_err(|error| InputError::io(format!("read binary record: {error}")))?;
                let record = binary_record(ipv6, &bytes[..size])
                    .ok_or_else(|| InputError::format("invalid binary range"))?;
                if record.from > record.to {
                    return Err(InputError::format("binary range start exceeds end"));
                }
                // Inclusive cardinality and the running total follow the
                // authoritative legacy readers (astra gate finding P1-3).
                // C sums v2 cardinality with wrapping u128 arithmetic
                // (src/ipset6_binary.c:49, u128_add(u128_sub(..), ONE)
                // accumulated by u128_add), so the full-universe range
                // ::/0 contributes 2^128 and wraps to zero -- exactly the
                // header count of an optimized full-universe payload,
                // which both legacy readers accept. The Rust legacy
                // reader replicates the same rule (legacy/binary.rs:286).
                // v1/IPv4 keeps C's checked accumulator
                // (src/ipset_binary.c:34-39, binary_add_unique_ips
                // refuses a u64 overflow), so overflow stays an error
                // there.
                let unique = if ipv6 {
                    record.to.wrapping_sub(record.from).wrapping_add(1)
                } else {
                    record
                        .to
                        .checked_sub(record.from)
                        .and_then(|value| value.checked_add(1))
                        .ok_or_else(|| InputError::format("binary range size overflows"))?
                };
                let next_unique = if ipv6 {
                    actual_unique.wrapping_add(unique)
                } else {
                    actual_unique
                        .checked_add(unique)
                        .ok_or_else(|| InputError::format("binary unique count overflows"))?
                };
                // Order/adjacency is tracked for EVERY payload (astra
                // parity round-9 F2): C validates the header's unique
                // count against every v1 payload, deriving the true
                // count by sort-merge for unordered ones
                // (src/ipset_binary.c:41-140). A bounded stream cannot
                // sort-merge, so what it can decide it must: a
                // strictly ascending, disjoint, non-adjacent payload's
                // running sum IS C's merged count, so that case is
                // compared below; a payload whose records break that
                // order cannot be counted at all by v1 rules, and
                // quietly skipping C's integrity check (accepting any
                // header number, as the previous code did for
                // non-optimized v1) is the failure being closed. A
                // header that CLAIMS optimized over a broken-order
                // payload was already refused, and still is.
                // Derived-optimized walk, mirroring C
                // (src/ipset_binary.c:65-76, src/ipset6_binary.c:35-46):
                // a record at/below the previous end, adjacent to it,
                // or out of order clears the derived flag; a header
                // that claimed optimized over such a payload is C's
                // "claims to be optimized" refusal (still keyed on the
                // header claim, so a non-optimized header over the
                // same payload stays loadable exactly as both legacy
                // readers load it).
                if let Some((_, previous_to)) = *previous {
                    if record.from <= previous_to
                        || previous_to.checked_add(1) == Some(record.from)
                    {
                        // C refuses a header that claims optimized over
                        // a payload that is not (src/ipset_binary.c:143-146,
                        // src/ipset6_binary.c:66-69, legacy/binary.rs:277-282).
                        if *optimized {
                            return Err(InputError::format(
                                "optimized binary payload is unordered, overlapping, or adjacent",
                            ));
                        }
                        *payload_optimized = false;
                    }
                }
                // Pairwise-disjointness envelope: while every record
                // starts strictly after the maximum end so far, the
                // records are disjoint, so C's merged count equals
                // the running sum even when the header is not
                // optimized-ordered (gaps and adjacency do not change
                // a disjoint union).
                if let Some(top) = *envelope {
                    if record.from <= top {
                        *count_exact = false;
                    }
                }
                *envelope = Some(envelope.map_or(record.to, |top| top.max(record.to)));
                *max_unique = (*max_unique).max(unique);
                *actual_unique = next_unique;
                *previous = Some((record.from, record.to));
                Ok(Step::BinaryRecord(record))
            }
            None => Err(InputError::format("input is not active")),
        }
    }
}

fn expand_paths(
    paths: Vec<String>,
    expand_at_paths: bool,
    max_expanded_paths: usize,
    max_line_bytes: usize,
) -> Result<VecDeque<PathBuf>, InputError> {
    let mut expanded = VecDeque::with_capacity(paths.len());
    for path in paths {
        if !expand_at_paths || !path.starts_with('@') {
            push_bounded(&mut expanded, PathBuf::from(path), max_expanded_paths)?;
            continue;
        }
        let referenced = Path::new(&path[1..]);
        let metadata = match referenced.symlink_metadata() {
            Ok(value) => value,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                return Err(InputError::invalid_path(format!(
                    "file-list or directory does not exist: {}",
                    referenced.display()
                )));
            }
            Err(error) => {
                return Err(InputError::io(format!(
                    "inspect file-list or directory {}: {error}",
                    referenced.display()
                )));
            }
        };
        if metadata.is_dir() {
            let mut entries = Vec::new();
            for entry in (referenced.read_dir()).map_err(|error| {
                InputError::io(format!("read directory {}: {error}", referenced.display()))
            })? {
                let entry = entry.map_err(|error| {
                    InputError::io(format!("read directory {}: {error}", referenced.display()))
                })?;
                let path = entry.path();
                let is_regular = path
                    .metadata()
                    .map(|value| value.is_file())
                    .unwrap_or(false);
                if is_regular {
                    entries.push(path);
                }
            }
            if entries.is_empty() {
                return Err(InputError::invalid_path(format!(
                    "directory contains no regular files: {}",
                    referenced.display()
                )));
            }
            entries.sort();
            for entry in entries {
                push_bounded(&mut expanded, entry, max_expanded_paths)?;
            }
            continue;
        }
        if !metadata.is_file() {
            return Err(InputError::invalid_path(format!(
                "file list is not a regular file: {}",
                referenced.display()
            )));
        }
        let file = open_file_list(referenced)?;
        let mut reader = BufReader::new(file);
        let mut loaded = false;
        loop {
            let mut line = Vec::new();
            if read_limited_line(&mut reader, max_line_bytes, &mut line)?.is_none() {
                break;
            }
            let trimmed = trim_file_list_line(&line);
            if trimmed.is_empty() {
                continue;
            }
            let path = PathBuf::from(String::from_utf8_lossy(trimmed).into_owned());
            push_bounded(&mut expanded, path, max_expanded_paths)?;
            loaded = true;
        }
        if !loaded {
            return Err(InputError::invalid_path(format!(
                "file list contains no paths: {}",
                referenced.display()
            )));
        }
    }
    Ok(expanded)
}

/// Open one already-inspected `@`-list.
///
/// The `symlink_metadata()` check in `expand_paths` cannot close the window
/// before the open, so only this descriptor decides what will be read: a
/// FIFO swapped into the path after that check is refused with the same
/// class the check uses for a non-regular path, never read.
fn open_file_list(path: &Path) -> Result<File, InputError> {
    let opened = crate::io::caller_open::open_regular(path)
        .map_err(|error| InputError::io(format!("open file list {}: {error}", path.display())))?;
    match opened {
        Some(file) => Ok(file),
        None => Err(InputError::invalid_path(format!(
            "file list is not a regular file: {}",
            path.display()
        ))),
    }
}

fn push_bounded(
    paths: &mut VecDeque<PathBuf>,
    path: PathBuf,
    max_expanded_paths: usize,
) -> Result<(), InputError> {
    if paths.len() >= max_expanded_paths {
        return Err(InputError::invalid_path(format!(
            "@-expansion exceeds the maximum of {max_expanded_paths} paths"
        )));
    }
    paths.push_back(path);
    Ok(())
}

fn trim_file_list_line(line: &[u8]) -> &[u8] {
    let start = line
        .iter()
        .position(|byte| *byte != b' ' && *byte != b'\t')
        .unwrap_or(line.len());
    let rest = &line[start..];
    if rest.first() == Some(&b'#') || rest.first() == Some(&b';') || rest.first() == Some(&b'\r') {
        return &[];
    }
    let mut end = rest.len();
    while end > 0 && matches!(rest[end - 1], b' ' | b'\t' | b'\r') {
        end -= 1;
    }
    &rest[..end]
}

fn open_input(path: &Path) -> Result<File, InputError> {
    match path.metadata() {
        Ok(value) if !value.is_file() => {
            return Err(InputError::invalid_path(format!(
                "input is not a regular file: {}",
                path.display()
            )));
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            return Err(InputError::invalid_path(format!(
                "input does not exist: {}",
                path.display()
            )));
        }
        Err(error) => {
            return Err(InputError::io(format!(
                "inspect input {}: {error}",
                path.display()
            )));
        }
        Ok(_) => {}
    }
    open_input_file(path)
}

/// Open one already-inspected input path.
///
/// The `metadata()` call in `open_input` cannot close the window before
/// the open, so only this descriptor decides what will be read: a FIFO
/// swapped into the path after that check is refused with the same class
/// the check uses for a non-regular path, never read.
fn open_input_file(path: &Path) -> Result<File, InputError> {
    let opened = crate::io::caller_open::open_regular(path).map_err(|error| {
        if error.kind() == io::ErrorKind::NotFound {
            InputError::invalid_path(format!("input does not exist: {}", path.display()))
        } else {
            InputError::io(format!("open input {}: {error}", path.display()))
        }
    })?;
    match opened {
        Some(file) => Ok(file),
        None => Err(InputError::invalid_path(format!(
            "input is not a regular file: {}",
            path.display()
        ))),
    }
}

fn read_limited_line<R: BufRead>(
    reader: &mut R,
    max_line_bytes: usize,
    output: &mut Vec<u8>,
) -> Result<Option<bool>, InputError> {
    output.clear();
    loop {
        let chunk = reader.fill_buf().map_err(InputError::from)?;
        if chunk.is_empty() {
            return Ok((!output.is_empty()).then_some(false));
        }
        let Some(position) = chunk.iter().position(|byte| *byte == b'\n') else {
            append_limited(output, chunk, max_line_bytes)?;
            let length = chunk.len();
            reader.consume(length);
            continue;
        };
        append_limited(output, &chunk[..position], max_line_bytes)?;
        reader.consume(position + 1);
        return Ok(Some(true));
    }
}

fn append_limited(
    output: &mut Vec<u8>,
    bytes: &[u8],
    max_line_bytes: usize,
) -> Result<(), InputError> {
    let next = output
        .len()
        .checked_add(bytes.len())
        .ok_or_else(|| InputError::format("input line length overflows"))?;
    if next > max_line_bytes {
        return Err(InputError::format(format!(
            "input line exceeds {max_line_bytes} bytes"
        )));
    }
    output.extend_from_slice(bytes);
    Ok(())
}

fn strip_bom(line: &mut Vec<u8>) {
    if line.starts_with(&[0xef, 0xbb, 0xbf]) {
        line.drain(..3);
    }
}

fn parse_text_line(line: &[u8], options: TextInputOptions) -> Result<ParsedLine, String> {
    // C classifies the fgets buffer as a C string: every end-of-line test
    // in parse_line()/parse_line6() stops at a NUL (src/ipset_load.c:150,
    // 173, 195, 224; src/ipset6_load.c:68, 99, 127, 144), so only the
    // bytes before the first NUL can change the outcome. The legacy
    // reader applies this with cstr() (src/legacy/parse.rs); the
    // streaming adapter must not diverge (astra gate finding P1-2).
    let line = match line.iter().position(|byte| *byte == 0) {
        Some(end) => &line[..end],
        None => line,
    };
    let rest = trim_leading(line);
    // C treats a record whose first surviving character is CR as an empty
    // line (src/ipset_load.c classify: Some(b'\r') | Some(b'\n') => Empty),
    // so a CRLF feed's blank line -- the record is exactly "\r" once the
    // reader strips LF -- is an empty record, not an input error.
    if rest.is_empty() || matches!(rest[0], b'#' | b';' | b'\r') {
        return Ok(ParsedLine::Empty);
    }
    if options.family == AddressFamilyInput::Ipv4 {
        if let Some(result) = parse_ipv4_mode_line(rest, options) {
            return result;
        }
        if rest.iter().filter(|byte| **byte == b':').count() >= 2 {
            if let Some(result) = parse_v4_mode_mapped(rest, options) {
                return result;
            }
            return Ok(ParsedLine::DroppedIpv6);
        }
    } else if let Some(result) = parse_ipv6_mode_line(rest, options)? {
        return Ok(result);
    }
    // C hostname_v4()/hostname_v6() resolve the SCANNED TOKEN, never the
    // rest of the line (src/legacy/parse.rs:1087-1100): a trailing
    // comment or CR is legal after the token but must not reach the
    // resolver. Handing the whole trimmed line to the resolver made
    // "host # comment" a lookup for "host # comment" (astra gate
    // finding P1-2). scan_while caps the token exactly like C's
    // MAX_TOKEN/MAX_TOKEN6 scan; the 255-byte bound is kept as the
    // accept test.
    if let Some(token) = hostname_token(rest, options.family) {
        return Ok(ParsedLine::Hostname(token));
    }
    Err(format!(
        "invalid input line: {}",
        String::from_utf8_lossy(line)
    ))
}

fn parse_ipv4_mode_line(
    line: &[u8],
    options: TextInputOptions,
) -> Option<Result<ParsedLine, String>> {
    let (first, after_first) = scan_while(line, is_ipv4_token_byte);
    if first.is_empty() {
        return None;
    }
    if complete_after_token(after_first) {
        return Some(parse_v4_endpoint(first, options).map(ParsedLine::Range));
    }
    let (_, next_without_spaces) = scan_while(after_first, |byte| *byte == b' ' || *byte == b'\t');
    if next_without_spaces.first() != Some(&b'-') {
        if first.contains(&b'/') || complete_ipv4_candidate(first) {
            return Some(Err(format!(
                "line looks like an IPv4 address but is invalid: {}",
                String::from_utf8_lossy(line)
            )));
        }
        return None;
    }
    let (_, after_dash_without_dash) =
        scan_while(after_first, |byte| *byte == b' ' || *byte == b'\t');
    if after_dash_without_dash.first() != Some(&b'-') {
        return Some(Err(format!(
            "invalid IPv4 range: {}",
            String::from_utf8_lossy(line)
        )));
    }
    let (_, after_dash) = scan_while(&after_dash_without_dash[1..], |byte| {
        *byte == b' ' || *byte == b'\t'
    });
    let (second, after_second) = scan_while(after_dash, is_ipv4_token_byte);
    if second.is_empty() || !complete_after_token(after_second) {
        return Some(Err(format!(
            "invalid IPv4 range: {}",
            String::from_utf8_lossy(line)
        )));
    }
    let left = match parse_v4_endpoint(first, options) {
        Ok(value) => value,
        Err(message) => return Some(Err(message)),
    };
    let right = match parse_v4_endpoint(second, options) {
        Ok(value) => value,
        Err(message) => return Some(Err(message)),
    };
    Some(Ok(ParsedLine::Range(joined_range(left, right))))
}

fn parse_ipv6_mode_line(
    line: &[u8],
    options: TextInputOptions,
) -> Result<Option<ParsedLine>, String> {
    let (first, after_first) = scan_while(line, is_ipv6_token_byte);
    if first.is_empty() {
        return Ok(None);
    }
    if complete_after_token(after_first) {
        if !first.contains(&b':') && classify_v4_token(first).is_none() {
            return Ok(None);
        }
        return parse_v6_endpoint(first, options).map(|value| Some(ParsedLine::Range(value)));
    }
    let (_, next_without_spaces) = scan_while(after_first, |byte| *byte == b' ' || *byte == b'\t');
    if next_without_spaces.first() != Some(&b'-') {
        if first.contains(&b':') || classify_v4_token(first).is_some() {
            return Err(format!(
                "line looks like an address but is invalid: {}",
                String::from_utf8_lossy(line)
            ));
        }
        return Ok(None);
    }
    let (_, after_dash_without_dash) =
        scan_while(after_first, |byte| *byte == b' ' || *byte == b'\t');
    if after_dash_without_dash.first() != Some(&b'-') {
        return Err(format!(
            "invalid IPv6 range: {}",
            String::from_utf8_lossy(line)
        ));
    }
    let (_, after_dash) = scan_while(&after_dash_without_dash[1..], |byte| {
        *byte == b' ' || *byte == b'\t'
    });
    let (second, after_second) = scan_while(after_dash, is_ipv6_token_byte);
    if second.is_empty() || !complete_after_token(after_second) {
        return Err(format!(
            "invalid IPv6 range: {}",
            String::from_utf8_lossy(line)
        ));
    }
    let first_family = token_address_family(first);
    let second_family = token_address_family(second);
    if first_family != second_family && first_family.is_some() && second_family.is_some() {
        return Err(format!(
            "mixed-family range: {}",
            String::from_utf8_lossy(line)
        ));
    }
    let left = parse_v6_endpoint(first, options)?;
    let right = parse_v6_endpoint(second, options)?;
    Ok(Some(ParsedLine::Range(joined_range(left, right))))
}

fn parse_v4_mode_mapped(
    line: &[u8],
    options: TextInputOptions,
) -> Option<Result<ParsedLine, String>> {
    let rest = trim_leading(line);
    if rest.len() < 8 || !rest[..7].eq_ignore_ascii_case(b"::ffff:") {
        return None;
    }
    let (token, after) = scan_while(&rest[7..], is_ipv4_token_byte);
    if token.is_empty() || !complete_after_token(after) {
        return None;
    }
    Some(parse_v4_endpoint(token, options).map(ParsedLine::Range))
}

fn joined_range(left: ParsedRange, right: ParsedRange) -> ParsedRange {
    let from = left.from.min(right.from);
    let to = left.to.max(right.to);
    ParsedRange {
        from,
        to,
        ipv4: left.ipv4 && right.ipv4,
    }
}

fn parse_v4_endpoint(token: &[u8], options: TextInputOptions) -> Result<ParsedRange, String> {
    if token.len() > 255 {
        return Err("IPv4 input token exceeds 255 bytes".into());
    }
    let (address_token, prefix) = match token.iter().position(|byte| *byte == b'/') {
        Some(position) => {
            let prefix = parse_v4_prefix(&token[position + 1..])?;
            (&token[..position], prefix)
        }
        None => (token, options.default_prefix.min(32)),
    };
    let address = parse_inet_aton(address_token)?;
    let from = if options.fix_network {
        network_v4(address, prefix)
    } else {
        address
    };
    let to = broadcast_v4(from, prefix);
    Ok(ParsedRange {
        from: u128::from(from),
        to: u128::from(to),
        ipv4: true,
    })
}

fn parse_v4_prefix(token: &[u8]) -> Result<u32, String> {
    let text = std::str::from_utf8(token).map_err(|_| "IPv4 prefix is not UTF-8".to_string())?;
    if let Ok(prefix) = text.parse::<u32>() {
        if prefix <= 32 {
            return Ok(prefix);
        }
        return Err(format!("IPv4 prefix is out of range: {text}"));
    }
    let mask = parse_inet_aton(token)?;
    if mask == !0u32 {
        return Err(format!("invalid IPv4 netmask: {text}"));
    }
    let inverted = !mask;
    if inverted == 0 || (inverted & (inverted.wrapping_add(1))) != 0 {
        return Err(format!("invalid IPv4 netmask: {text}"));
    }
    Ok(32 - inverted.count_ones())
}

fn parse_inet_aton(token: &[u8]) -> Result<u32, String> {
    let text = std::str::from_utf8(token).map_err(|_| "IPv4 address is not UTF-8".to_string())?;
    let mut parts: Vec<u64> = Vec::new();
    let mut current = String::new();
    for byte in text.chars() {
        if byte == '.' {
            if current.is_empty() {
                return Err(format!("invalid IPv4 address: {text}"));
            }
            parts.push(parse_ipv4_number(&current, text)?);
            current.clear();
        } else {
            current.push(byte);
        }
    }
    if current.is_empty() || parts.len() >= 4 {
        return Err(format!("invalid IPv4 address: {text}"));
    }
    parts.push(parse_ipv4_number(&current, text)?);
    let values: [u64; 4] = match parts.len() {
        1 => [parts[0], 0, 0, 0],
        2 => [parts[0], parts[1], 0, 0],
        3 => [parts[0], parts[1], parts[2], 0],
        _ => [parts[0], parts[1], parts[2], parts[3]],
    };
    let result = match parts.len() {
        1 if values[0] <= u32::MAX as u64 => values[0] as u32,
        2 if values[0] <= u8::MAX as u64 && values[1] <= 0x00ff_ffff => {
            ((values[0] as u32) << 24) | values[1] as u32
        }
        3 if values[0] <= u8::MAX as u64
            && values[1] <= u8::MAX as u64
            && values[2] <= u16::MAX as u64 =>
        {
            ((values[0] as u32) << 24) | ((values[1] as u32) << 16) | values[2] as u32
        }
        4 if values.iter().all(|value| *value <= u8::MAX as u64) => {
            ((values[0] as u32) << 24)
                | ((values[1] as u32) << 16)
                | ((values[2] as u32) << 8)
                | values[3] as u32
        }
        _ => return Err(format!("invalid IPv4 address: {text}")),
    };
    Ok(result)
}

fn parse_ipv4_number(token: &str, address: &str) -> Result<u64, String> {
    let bytes = token.as_bytes();
    if bytes.is_empty() {
        return Err(format!("invalid IPv4 address: {address}"));
    }
    let (digits, radix) =
        if bytes.len() >= 2 && bytes[0] == b'0' && (bytes[1] == b'x' || bytes[1] == b'X') {
            (&bytes[2..], 16)
        } else if bytes.len() >= 2 && bytes[0] == b'0' {
            (&bytes[1..], 8)
        } else {
            (bytes, 10)
        };
    if digits.is_empty() {
        return Ok(0);
    }
    let mut value = 0u64;
    for byte in digits {
        let digit = match (*byte as char).to_digit(radix) {
            Some(value) => value as u64,
            None => return Err(format!("invalid IPv4 address: {address}")),
        };
        value = value
            .checked_mul(radix as u64)
            .and_then(|product| product.checked_add(digit))
            .ok_or_else(|| format!("invalid IPv4 address: {address}"))?;
        if value > u32::MAX as u64 {
            return Err(format!("invalid IPv4 address: {address}"));
        }
    }
    Ok(value)
}

fn network_v4(address: u32, prefix: u32) -> u32 {
    if prefix == 0 {
        0
    } else {
        address & (!0u32 << (32 - prefix))
    }
}

fn broadcast_v4(address: u32, prefix: u32) -> u32 {
    if prefix == 0 {
        u32::MAX
    } else if prefix == 32 {
        address
    } else {
        address | ((!0u32) >> prefix)
    }
}

fn parse_v6_endpoint(token: &[u8], options: TextInputOptions) -> Result<ParsedRange, String> {
    if token.len() > 256 {
        return Err("IPv6 input token exceeds 256 bytes".into());
    }
    if token.contains(&b':') {
        let (address_token, prefix) = match token.iter().position(|byte| *byte == b'/') {
            Some(position) => {
                let text = std::str::from_utf8(&token[position + 1..])
                    .map_err(|_| "IPv6 prefix is not UTF-8".to_string())?;
                let prefix = text
                    .parse::<u32>()
                    .map_err(|_| format!("invalid IPv6 prefix: {text}"))?;
                if prefix > 128 {
                    return Err(format!("IPv6 prefix is out of range: {text}"));
                }
                (&token[..position], prefix)
            }
            None => (token, options.default_prefix),
        };
        let text = std::str::from_utf8(address_token)
            .map_err(|_| "IPv6 address is not UTF-8".to_string())?;
        let address: Ipv6Addr = text
            .parse()
            .map_err(|_| format!("invalid IPv6 address: {text}"))?;
        let value = u128::from(address);
        let from = if options.fix_network {
            network_v6(value, prefix)
        } else {
            value
        };
        let to = broadcast_v6(from, prefix);
        return Ok(ParsedRange {
            from,
            to,
            ipv4: false,
        });
    }

    let endpoint = parse_v4_endpoint(token, options)?;
    if options.default_prefix >= 32 {
        let mapped_from = u128::from(ipv4_mapped(
            u32::try_from(endpoint.from).expect("IPv4 endpoint"),
        ));
        let mapped_to = u128::from(ipv4_mapped(
            u32::try_from(endpoint.to).expect("IPv4 endpoint"),
        ));
        return Ok(ParsedRange {
            from: mapped_from,
            to: mapped_to,
            ipv4: false,
        });
    }
    Ok(endpoint)
}

fn network_v6(address: u128, prefix: u32) -> u128 {
    if prefix == 0 {
        0
    } else if prefix == 128 {
        address
    } else {
        address & (!(u128::MAX >> (128 - prefix)))
    }
}

fn broadcast_v6(address: u128, prefix: u32) -> u128 {
    if prefix == 0 {
        u128::MAX
    } else if prefix == 128 {
        address
    } else {
        address | (u128::MAX >> prefix)
    }
}

fn ipv4_mapped(address: u32) -> Ipv6Addr {
    Ipv6Addr::from(0x0000_ffff_0000_0000u128 | u128::from(address))
}

fn scan_while(line: &[u8], accepted: fn(&u8) -> bool) -> (&[u8], &[u8]) {
    let end = line
        .iter()
        .position(|byte| !accepted(byte))
        .unwrap_or(line.len());
    (&line[..end], &line[end..])
}

fn trim_leading(line: &[u8]) -> &[u8] {
    let start = line
        .iter()
        .position(|byte| *byte != b' ' && *byte != b'\t')
        .unwrap_or(line.len());
    &line[start..]
}

fn complete_after_token(rest: &[u8]) -> bool {
    let rest = trim_leading(rest);
    rest.is_empty() || matches!(rest[0], b'#' | b';' | b'\r' | 0)
}

fn is_ipv4_token_byte(byte: &u8) -> bool {
    byte.is_ascii_digit() || *byte == b'.' || *byte == b'/'
}

fn is_ipv6_token_byte(byte: &u8) -> bool {
    byte.is_ascii_hexdigit() || *byte == b':' || *byte == b'.' || *byte == b'/'
}

fn complete_ipv4_candidate(token: &[u8]) -> bool {
    let mut dots = 0;
    let mut digits = 0;
    for byte in token {
        if byte.is_ascii_digit() {
            digits += 1;
        } else if *byte == b'.' && digits > 0 {
            dots += 1;
            digits = 0;
        } else {
            return false;
        }
    }
    dots == 3 && digits > 0
}

fn classify_v4_token(token: &[u8]) -> Option<bool> {
    (!token.is_empty()
        && (token.contains(&b'.')
            || token.contains(&b'/')
            || token.iter().all(|byte| byte.is_ascii_digit())))
    .then_some(true)
}

fn token_address_family(token: &[u8]) -> Option<bool> {
    if token.contains(&b':') {
        Some(false)
    } else {
        classify_v4_token(token)
    }
}

/// C hostname_v4()/hostname_v6() (src/legacy/parse.rs:1087-1100):
/// scan the token, accept only when what follows (after spaces) is
/// empty or a comment/CR, and return the TOKEN itself. The old
/// `hostname_is_complete` predicate checked the shape but the caller
/// then handed the whole line to the resolver, so a trailing comment
/// or CR rode along into the lookup.
///
/// The token cap is family-specific because the legacy buffers are
/// (MAX_TOKEN = C MAX_INPUT_ELEMENT 255 for IPv4, src/ipset_dns.c:3;
/// MAX_TOKEN6 = C MAX_INPUT_ELEMENT6 256, src/ipset6_load.h:6): a
/// 256-byte name is valid IPv6-mode input and must not be refused
/// here while both legacy readers accept it.
fn hostname_token(line: &[u8], family: AddressFamilyInput) -> Option<Vec<u8>> {
    let max = match family {
        AddressFamilyInput::Ipv4 => 255,
        AddressFamilyInput::Ipv6 => 256,
    };
    let (token, rest) = scan_while(line, is_hostname_byte);
    if token.is_empty() || token.len() > max || !complete_after_token(rest) {
        return None;
    }
    Some(token.to_vec())
}

fn is_hostname_byte(byte: &u8) -> bool {
    byte.is_ascii_alphanumeric() || matches!(*byte, b'_' | b'-' | b'.')
}

fn dns_range(address: IpAddr, family: AddressFamilyInput) -> Result<ParsedRange, String> {
    match (family, address) {
        (AddressFamilyInput::Ipv4, IpAddr::V4(address)) => {
            let value = u128::from(u32::from(address));
            Ok(ParsedRange::single(value, true))
        }
        (AddressFamilyInput::Ipv6, IpAddr::V4(address)) => {
            let value = u128::from(ipv4_mapped(u32::from(address)));
            Ok(ParsedRange::single(value, false))
        }
        (AddressFamilyInput::Ipv6, IpAddr::V6(address)) => {
            let value = u128::from(address);
            Ok(ParsedRange::single(value, false))
        }
        (AddressFamilyInput::Ipv4, IpAddr::V6(_)) => {
            Err("IPv4 DNS response contains an IPv6 address".into())
        }
    }
}

fn resolve_hostnames(
    names: &[String],
    family: AddressFamilyInput,
    threads: usize,
    silent: bool,
) -> Result<Vec<IpAddr>, String> {
    let workers = names.len().min(threads.max(1));
    let mut results: Vec<Result<Vec<IpAddr>, String>> = Vec::with_capacity(workers);
    std::thread::scope(|scope| {
        let handles: Vec<_> = (0..workers)
            .map(|worker| {
                scope.spawn(move || {
                    let mut output = Vec::new();
                    for (index, name) in names.iter().enumerate() {
                        if index % workers != worker {
                            continue;
                        }
                        resolve_one(name, silent, &mut output)?;
                    }
                    Ok(output)
                })
            })
            .collect();
        for handle in handles {
            match handle.join() {
                Ok(result) => results.push(result),
                Err(_) => results.push(Err("DNS worker panicked".into())),
            }
        }
    });
    let mut addresses = Vec::new();
    for result in results {
        addresses.extend(result?);
    }
    if family == AddressFamilyInput::Ipv4 {
        addresses.retain(|address| matches!(address, IpAddr::V4(_)));
        if addresses.is_empty() {
            return Err("DNS response contains no A records".into());
        }
    }
    Ok(addresses)
}

/// Write one advisory diagnostic without ever blocking the caller:
/// a full stderr pipe must not stall the input worker and wedge
/// session shutdown (external review finding).  One dedicated
/// drainer thread writes the queue; per-diagnostic threads are never
/// spawned, so a sustained full pipe can at most drop diagnostics
/// (and block the single drainer) instead of accumulating one
/// blocked thread per message until a spawn panics.  Diagnostics are
/// advisory, never error semantics.
fn stderr_diag(message: String) {
    if !DIAG_STARTED.swap(true, std::sync::atomic::Ordering::Relaxed) {
        // Best-effort single spawn; a failed spawn leaves the flag
        // false so a later diagnostic retries, and no worker path
        // ever panics on thread creation (operations finding).
        if std::thread::Builder::new()
            .name("iprange-stderr-diag".to_owned())
            .spawn(diag_loop)
            .is_err()
        {
            DIAG_STARTED.store(false, std::sync::atomic::Ordering::Relaxed);
        }
    }
    let Ok(mut queue) = DIAG_QUEUE.lock() else {
        return; // poisoned lock: drop the advisory diagnostic
    };
    if queue.len() < DIAG_QUEUE_CAP {
        queue.push_back(message);
    }
}

const DIAG_QUEUE_CAP: usize = 256;

static DIAG_QUEUE: std::sync::Mutex<std::collections::VecDeque<String>> =
    std::sync::Mutex::new(std::collections::VecDeque::new());

static DIAG_STARTED: std::sync::atomic::AtomicBool =
    std::sync::atomic::AtomicBool::new(false);

fn diag_loop() {
    loop {
        let message = match DIAG_QUEUE.lock() {
            Ok(mut queue) => queue.pop_front(),
            Err(_) => None, // poisoned lock: keep the drainer alive
        };
        match message {
            Some(text) => eprintln!("{text}"),
            None => std::thread::sleep(std::time::Duration::from_millis(20)),
        }
    }
}

fn resolve_one(name: &str, silent: bool, output: &mut Vec<IpAddr>) -> Result<(), String> {
    for attempt in 1..=20 {
        match (name, 80).to_socket_addrs() {
            Ok(values) => {
                output.extend(values.map(|value| value.ip()));
                return Ok(());
            }
            Err(error) => {
                let message = error.to_string();
                let temporary = message.contains("Temporary failure in name resolution");
                if temporary && attempt < 20 {
                    if !silent {
                        stderr_diag(format!("iprange: DNS: '{name}' will be retried: {error}"));
                    }
                    std::thread::sleep(std::time::Duration::from_secs(1));
                    continue;
                }
                if !silent {
                    stderr_diag(format!("iprange: DNS: '{name}' failed permanently: {error}"));
                }
                return Err(format!("DNS resolution failed for '{name}': {message}"));
            }
        }
    }
    unreachable!("the DNS retry loop always returns by its twentieth attempt")
}

fn binary_line<R: BufRead>(
    reader: &mut R,
    max_line_bytes: usize,
) -> Result<Option<Vec<u8>>, InputError> {
    let mut line = Vec::new();
    let Some(had_newline) = read_limited_line(reader, max_line_bytes, &mut line)? else {
        return Ok(None);
    };
    if !had_newline {
        return Ok(None);
    }
    if line.last() == Some(&b'\r') {
        line.pop();
    }
    Ok(Some(line))
}

fn require_binary_line<R: BufRead>(
    reader: &mut R,
    expected: &[u8],
    max_line_bytes: usize,
) -> Result<(), InputError> {
    match binary_line(reader, max_line_bytes)? {
        Some(line) if line == expected => Ok(()),
        _ => Err(InputError::format("invalid binary header line")),
    }
}

fn binary_number<R: BufRead>(
    reader: &mut R,
    prefix: &[u8],
    max_line_bytes: usize,
) -> Result<Option<u128>, InputError> {
    let Some(line) = binary_line(reader, max_line_bytes)? else {
        return Ok(None);
    };
    if !line.starts_with(prefix) {
        return Ok(None);
    }
    let text = std::str::from_utf8(&line[prefix.len()..])
        .map_err(|_| InputError::format("binary numeric field is not UTF-8"))?;
    text.parse::<u128>()
        .map(Some)
        .map_err(|_| InputError::format("invalid binary numeric field"))
}

fn binary_record(ipv6: bool, bytes: &[u8]) -> Option<ParsedRange> {
    if ipv6 {
        let from = u128::from_ne_bytes(bytes[..16].try_into().ok()?);
        let to = u128::from_ne_bytes(bytes[16..].try_into().ok()?);
        Some(ParsedRange {
            from,
            to,
            ipv4: false,
        })
    } else {
        let from = u32::from_ne_bytes(bytes[..4].try_into().ok()?);
        let to = u32::from_ne_bytes(bytes[4..].try_into().ok()?);
        Some(ParsedRange {
            from: u128::from(from),
            to: u128::from(to),
            ipv4: true,
        })
    }
}

fn input_sdk_error(_error: InputError) -> Error {
    Error::InvalidArgument("legacy-compatible input source failed")
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::str::FromStr;

    fn options(family: AddressFamilyInput, prefix: u32, fix: bool) -> TextInputOptions {
        TextInputOptions {
            family,
            fix_network: fix,
            default_prefix: prefix,
            dns_threads: 1,
            dns_silent: true,
            max_line_bytes: 1_048_576,
        }
    }

    fn v4(from: u32, to: u32) -> ParsedRange {
        ParsedRange {
            from: u128::from(from),
            to: u128::from(to),
            ipv4: true,
        }
    }

    fn v6(from: u128, to: u128) -> ParsedRange {
        ParsedRange {
            from,
            to,
            ipv4: false,
        }
    }

    #[test]
    fn ipv4_forms_match_released_parser() {
        let opts = options(AddressFamilyInput::Ipv4, 32, true);
        assert_eq!(
            parse_text_line(b"1.2.3.4", opts).unwrap(),
            ParsedLine::Range(v4(0x01020304, 0x01020304))
        );
        assert_eq!(
            parse_text_line(b"10.0.0.7/24", opts).unwrap(),
            ParsedLine::Range(v4(0x0a000000, 0x0a0000ff))
        );
        assert_eq!(
            parse_text_line(b"10.0.0.7/255.255.255.0", opts).unwrap(),
            ParsedLine::Range(v4(0x0a000000, 0x0a0000ff))
        );
        assert_eq!(
            parse_text_line(b"10.0.0.10 - 10.0.0.8", opts).unwrap(),
            ParsedLine::Range(v4(0x0a000008, 0x0a00000a))
        );
        assert_eq!(
            parse_text_line(b"10.0.0.0/29 - 10.0.0.8/31", opts).unwrap(),
            ParsedLine::Range(v4(0x0a000000, 0x0a000009))
        );
        assert_eq!(
            parse_text_line(b"  010.0.0.1 # comment\r", opts).unwrap(),
            ParsedLine::Range(v4(0x08000001, 0x08000001))
        );
        assert_eq!(
            parse_text_line(b"10.3", opts).unwrap(),
            ParsedLine::Range(v4(0x0a000003, 0x0a000003))
        );
        assert_eq!(
            parse_text_line(b"# comment", opts).unwrap(),
            ParsedLine::Empty
        );
        assert_eq!(
            parse_text_line(b"::ffff:1.2.3.4", opts).unwrap(),
            ParsedLine::Range(v4(0x01020304, 0x01020304))
        );
        assert_eq!(
            parse_text_line(b"2001:db8::1", opts).unwrap(),
            ParsedLine::DroppedIpv6
        );
        assert!(parse_text_line(b"1.2.3.999", opts).is_err());
    }

    #[test]
    fn network_fixing_and_default_prefix_are_exact() {
        let no_fix = options(AddressFamilyInput::Ipv4, 32, false);
        assert_eq!(
            parse_text_line(b"1.2.3.5/30", no_fix).unwrap(),
            ParsedLine::Range(v4(0x01020305, 0x01020307))
        );
        let prefix = options(AddressFamilyInput::Ipv4, 30, true);
        assert_eq!(
            parse_text_line(b"1.2.3.5", prefix).unwrap(),
            ParsedLine::Range(v4(0x01020304, 0x01020307))
        );
        let ipv6_prefix = options(AddressFamilyInput::Ipv6, 64, true);
        assert_eq!(
            parse_text_line(b"2001:db8:0:1:0:0:0:1", ipv6_prefix).unwrap(),
            ParsedLine::Range(v6(
                u128::from(Ipv6Addr::from_str("2001:db8:0:1::").unwrap()),
                u128::from(Ipv6Addr::from_str("2001:db8:0:1:ffff:ffff:ffff:ffff").unwrap()),
            ))
        );
    }

    #[test]
    fn ipv6_maps_v4_and_ranges_normally() {
        let opts = options(AddressFamilyInput::Ipv6, 128, true);
        let mapped = u128::from(Ipv6Addr::from_str("::ffff:10.0.0.1").unwrap());
        assert_eq!(
            parse_text_line(b"10.0.0.1", opts).unwrap(),
            ParsedLine::Range(v6(mapped, mapped))
        );
        let one = u128::from(Ipv6Addr::from_str("2001:db8::1").unwrap());
        let ten = u128::from(Ipv6Addr::from_str("2001:db8::10").unwrap());
        assert_eq!(
            parse_text_line(b"2001:db8::10 - 2001:db8::1", opts).unwrap(),
            ParsedLine::Range(v6(one, ten))
        );
        assert!(parse_text_line(b"10.0.0.1 - 2001:db8::1", opts).is_err());
    }

    #[test]
    fn hostname_detection_rejects_ranges_and_bad_suffixes() {
        let opts = options(AddressFamilyInput::Ipv4, 32, true);
        assert!(matches!(
            parse_text_line(b"host.example", opts).unwrap(),
            ParsedLine::Hostname(_)
        ));
        assert!(parse_text_line(b"host.example - other", opts).is_err());
        assert!(parse_text_line(b"1.2.3.4#comment", opts).is_ok());
    }

    #[test]
    fn streaming_lexer_matches_legacy_record_rules() {
        // Astra gate finding P1-2: the streaming lexer must classify
        // records exactly as the legacy C loader does (cstr truncation
        // at NUL, CR-leading records empty) and resolve the hostname
        // TOKEN, not the whole line. Each assertion here is the
        // pre-fix failure: "\r" and the NUL record used to be
        // input_format errors, and "localhost # comment\r" used to be
        // handed to the resolver whole-line.
        let opts = options(AddressFamilyInput::Ipv4, 32, true);
        // A CRLF feed's blank line is the record "\r" once the reader
        // strips LF: empty, not an error (src/ipset_load.c classify).
        assert_eq!(parse_text_line(b"\r", opts).unwrap(), ParsedLine::Empty);
        assert_eq!(
            parse_text_line(b"  \r", opts).unwrap(),
            ParsedLine::Empty,
            "spaces then CR still begin the record with CR after trimming"
        );
        // C stops at the first NUL: the surviving text is empty.
        assert_eq!(
            parse_text_line(b"\x00garbage", opts).unwrap(),
            ParsedLine::Empty
        );
        assert_eq!(
            parse_text_line(b"1.2.3.4\x00junk", opts).unwrap(),
            ParsedLine::Range(v4(0x01020304, 0x01020304)),
            "bytes after the NUL cannot change the outcome"
        );
        // The resolver sees the token, never the trailing suffix.
        assert_eq!(
            parse_text_line(b"localhost # comment\r", opts).unwrap(),
            ParsedLine::Hostname(b"localhost".to_vec())
        );
        assert_eq!(
            parse_text_line(b"host.example\r", opts).unwrap(),
            ParsedLine::Hostname(b"host.example".to_vec())
        );
        // A numeric record keeps the trailing-CR acceptance.
        assert_eq!(
            parse_text_line(b"1.2.3.4\r", opts).unwrap(),
            ParsedLine::Range(v4(0x01020304, 0x01020304))
        );
        // The hostname token cap is family-specific exactly as the
        // legacy buffers: C MAX_INPUT_ELEMENT 255 (src/ipset_dns.c:3)
        // and MAX_INPUT_ELEMENT6 256 (src/ipset6_load.h:6), so a
        // 256-byte name is IPv6-valid input and a 257-byte name is not.
        let name256 = vec![b'a'; 256];
        let v6opts = options(AddressFamilyInput::Ipv6, 128, true);
        assert!(matches!(
            parse_text_line(&name256, v6opts).unwrap(),
            ParsedLine::Hostname(t) if t.len() == 256
        ));
        assert!(parse_text_line(&name256, opts).is_err(),
                "256-byte names exceed C MAX_INPUT_ELEMENT: v4 mode refuses");
        let mut name257 = vec![b'a'; 257];
        assert!(parse_text_line(&name257, v6opts).is_err());
        name257.truncate(0);
    }

    #[test]
    fn batch_surplus_parks_fifo_instead_of_failing() {
        // Astra gate finding P1-1: one resolution group answering
        // with more addresses than BATCH_CAPACITY used to fail the
        // whole feed with "range does not fit the bounded parser
        // batch/family". The guarantee is capacity deferral: the
        // surplus parks (bounded by the group) AND the next batch
        // hands it out first -- both halves are asserted here (a
        // discarding drain fails the collection below; tester
        // round-9 F1). The corpus case publish.dns_overflow_batch
        // proves publication of a large resolution group succeeds
        // end-to-end; this pin owns the park-and-drain contract.
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-input-pending-{}-{unique}",
            std::process::id()
        ));
        std::fs::write(&path, b"").unwrap();
        let mut source = TextInputSource::<Ipv4Key>::new(
            vec![path.display().to_string()],
            options(AddressFamilyInput::Ipv4, 32, true),
            true,
            10,
        )
        .unwrap();
        let total = BATCH_CAPACITY + 44;
        for index in 0..total {
            source
                .push_range(v4(index as u32 + 1, index as u32 + 1))
                .expect("capacity overflow must park, not fail");
        }
        assert_eq!(source.batch.len(), BATCH_CAPACITY);
        assert_eq!(source.pending.len(), total - BATCH_CAPACITY);
        let parked: Vec<u32> = source
            .pending
            .iter()
            .map(|range| u32::try_from(range.from).expect("in range"))
            .collect();
        assert_eq!(parked.first().copied(), Some(BATCH_CAPACITY as u32 + 1));
        assert_eq!(parked.last().copied(), Some(total as u32));
        assert!(
            parked.windows(2).all(|pair| pair[0] + 1 == pair[1]),
            "the parked surplus must be FIFO"
        );
        // Family refusals remain errors: only capacity defers.
        assert!(source.push_range(v6(1, 1)).is_err());

        // The drain half (tester round-9 F1): next_batch must hand out
        // the parked surplus FIFO, and every parked range must reach a
        // batch. A drain that pops and discards passes every
        // push-side assertion above and fails the count/order checks
        // here. The loop's first action is `batch.clear()`, so the
        // pre-filled batch is scratch; what must survive is the queue.
        let mut handed = Vec::new();
        while let Some(batch) = source.next_batch().unwrap() {
            assert!(!batch.is_empty(), "a non-empty queue must hand out a batch");
            assert!(batch.len() <= BATCH_CAPACITY);
            handed.extend(batch.iter().map(|range| u32::try_from(range.from.0).unwrap()));
        }
        // Nothing was lost and nothing reordered: the parked ranges
        // arrive in exactly the order they parked. (The pre-filled
        // head of the original group was cleared as loop scratch, so
        // the expectation is the parked tail, not the full group.)
        let parked_expect: Vec<u32> =
            ((BATCH_CAPACITY + 1) as u32..=total as u32).collect();
        assert_eq!(handed, parked_expect, "the drain must hand out the parked FIFO in full");
        std::fs::remove_file(path).unwrap();
    }

    #[test]
    fn binary_v6_full_universe_range_loads_like_legacy() {
        // Astra gate finding P1-3: an optimized v2 payload with one
        // record ::/0 and header "unique ips 0" is valid for the
        // authoritative readers (C wraps the 2^128 cardinality,
        // src/ipset6_binary.c:49; legacy/binary.rs:286 replicates),
        // so the streaming reader must accept it too.
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-input-v6full-{}-{unique}",
            std::process::id()
        ));
        let mut bytes = Vec::new();
        bytes.extend_from_slice(b"iprange binary format v2.0\n");
        bytes.extend_from_slice(b"ipv6\n");
        bytes.extend_from_slice(b"optimized\n");
        bytes.extend_from_slice(b"record size 32\n");
        bytes.extend_from_slice(b"records 1\n");
        bytes.extend_from_slice(b"bytes 36\n");
        bytes.extend_from_slice(b"lines 1\n");
        bytes.extend_from_slice(b"unique ips 0\n");
        bytes.extend_from_slice(&0x1a2b_3c4du32.to_ne_bytes());
        bytes.extend_from_slice(&[0u8; 16]);
        bytes.extend_from_slice(&[0xffu8; 16]);
        std::fs::write(&path, bytes).unwrap();

        let mut source = TextInputSource::<Ipv6Key>::new(
            vec![path.display().to_string()],
            options(AddressFamilyInput::Ipv6, 128, true),
            true,
            10,
        )
        .unwrap();
        let ranges = source.next_batch().unwrap().unwrap();
        assert_eq!(ranges.len(), 1);
        assert_eq!((ranges[0].from.hi, ranges[0].from.lo), (0, 0));
        assert_eq!((ranges[0].to.hi, ranges[0].to.lo), (u64::MAX, u64::MAX));
        assert!(source.next_batch().unwrap().is_none());
        std::fs::remove_file(path).unwrap();
    }

    #[test]
    fn binary_v6_record_reads_lo_then_hi() {
        // Parity round-9 F1: the v2 record fields are lo (bytes 0-7)
        // then hi (bytes 8-15) -- the spec layout, C src/uint128.h,
        // this reader's u128::from_ne_bytes, and Go's own legacy
        // reader all agree; a hi/lo swap in either engine corrupts
        // every limb-asymmetric range silently (same record count,
        // different addresses). Distinct quads expose the order.
        let mut bytes = [0u8; 32];
        bytes[0] = 5; // from.lo
        bytes[16] = 9;
        bytes[24] = 1; // to.hi
        let record = binary_record(true, &bytes).expect("decode");
        assert_eq!(
            (record.from, record.to),
            (u128::from(5u32), (u128::from(1u64) << 64) | 9)
        );
    }

    #[test]
    fn binary_unique_count_follows_c_validation() {
        // Parity round-9 F2: C validates the header's unique count
        // for EVERY v1 payload (src/ipset_binary.c:41-140, sort-merge
        // for unordered) and for v2 while its derived optimized flag
        // holds (src/ipset6_binary.c:35-65); the streaming reader must
        // not skip the comparison because the header spells
        // "non-optimized". The payloads are the canonical base64 the
        // Go mirror test decodes (fixture bytes are shared between
        // the engines) and match what the released C oracle loads or
        // refuses on this workstation.
        fn payload(b64: &str) -> Vec<u8> {
            // Standard base64, decoded without a crate: the fixtures
            // are short and every byte is verified against the C run.
            const TABLE: &[u8; 64] =
                b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
            let mut out = Vec::new();
            let mut acc: u32 = 0;
            let mut bits = 0u32;
            for byte in b64.bytes() {
                if byte == b'=' {
                    break;
                }
                let value = TABLE
                    .iter()
                    .position(|candidate| *candidate == byte)
                    .expect("fixture is base64") as u32;
                acc = (acc << 6) | value;
                bits += 6;
                if bits >= 8 {
                    bits -= 8;
                    out.push((acc >> bits) as u8);
                }
            }
            out
        }
        fn load(bytes: &[u8], family: AddressFamilyInput) -> Result<(), String> {
            let stamp = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos();
            let path = std::env::temp_dir().join(format!(
                "iprange-input-count-{}-{stamp}",
                std::process::id()
            ));
            std::fs::write(&path, bytes).unwrap();
            let result = match family {
                AddressFamilyInput::Ipv4 => {
                    let mut source = TextInputSource::<Ipv4Key>::new(
                        vec![path.display().to_string()],
                        options(AddressFamilyInput::Ipv4, 32, true),
                        true,
                        10,
                    )
                    .map_err(|e| e.message().to_owned())?;
                    let mut drain = Ok(());
                    loop {
                        match source.next_batch() {
                            Ok(Some(_)) => {}
                            Ok(None) => break,
                            Err(_) => {
                                drain = Err(source
                                    .last_input_message()
                                    .unwrap_or("sdk error")
                                    .to_owned());
                                break;
                            }
                        }
                    }
                    drain
                }
                AddressFamilyInput::Ipv6 => {
                    let mut source = TextInputSource::<Ipv6Key>::new(
                        vec![path.display().to_string()],
                        options(AddressFamilyInput::Ipv6, 128, true),
                        true,
                        10,
                    )
                    .map_err(|e| e.message().to_owned())?;
                    let mut drain = Ok(());
                    loop {
                        match source.next_batch() {
                            Ok(Some(_)) => {}
                            Ok(None) => break,
                            Err(_) => {
                                drain = Err(source
                                    .last_input_message()
                                    .unwrap_or("sdk error")
                                    .to_owned());
                                break;
                            }
                        }
                    }
                    drain
                }
            };
            std::fs::remove_file(path).unwrap();
            result
        }
        // v1 non-optimized, disjoint records (1..2, 5..5): sum 3.
        // A header claiming 99 contradicts C's comparison (the case
        // the old `optimized &&` guard skipped); the honest 3 loads.
        assert!(load(
            &payload("aXByYW5nZSBiaW5hcnkgZm9ybWF0IHYxLjAKbm9uLW9wdGltaXplZApyZWNvcmQgc2l6ZSA4CnJlY29yZHMgMgpieXRlcyAyMApsaW5lcyAyCnVuaXF1ZSBpcHMgOTkKTTwrGgEAAAACAAAABQAAAAUAAAA="),
            AddressFamilyInput::Ipv4,
        )
        .is_err());
        assert!(load(
            &payload("aXByYW5nZSBiaW5hcnkgZm9ybWF0IHYxLjAKbm9uLW9wdGltaXplZApyZWNvcmQgc2l6ZSA4CnJlY29yZHMgMgpieXRlcyAyMApsaW5lcyAyCnVuaXF1ZSBpcHMgMwpNPCsaAQAAAAIAAAAFAAAABQAAAA=="),
            AddressFamilyInput::Ipv4,
        )
        .is_ok());
        // v1 overlapping records (1..3, 3..5): sum 6, merged 5, max
        // record 3. Headers outside [3, 6] contradict C's merge in
        // one of the two derivable directions and must fail both
        // times; a header inside the bracket is C-loadable only
        // through the sort-merge the stream cannot do -- see the
        // documented residual in read_step.
        assert!(load(
            &payload("aXByYW5nZSBiaW5hcnkgZm9ybWF0IHYxLjAKbm9uLW9wdGltaXplZApyZWNvcmQgc2l6ZSA4CnJlY29yZHMgMgpieXRlcyAyMApsaW5lcyAyCnVuaXF1ZSBpcHMgMTAKTTwrGgEAAAADAAAAAwAAAAUAAAA="),
            AddressFamilyInput::Ipv4,
        )
        .is_err());
        assert!(load(
            &payload("aXByYW5nZSBiaW5hcnkgZm9ybWF0IHYxLjAKbm9uLW9wdGltaXplZApyZWNvcmQgc2l6ZSA4CnJlY29yZHMgMgpieXRlcyAyMApsaW5lcyAyCnVuaXF1ZSBpcHMgMQpNPCsaAQAAAAMAAAADAAAABQAAAA=="),
            AddressFamilyInput::Ipv4,
        )
        .is_err());
        // C refuses an empty payload whose header counts anything
        // (src/ipset_binary.c:49-53, and the v2 loader's mirror).
        assert!(load(
            &payload("aXByYW5nZSBiaW5hcnkgZm9ybWF0IHYxLjAKb3B0aW1pemVkCnJlY29yZCBzaXplIDgKcmVjb3JkcyAwCmJ5dGVzIDQKbGluZXMgMAp1bmlxdWUgaXBzIDQKTTwrGg=="),
            AddressFamilyInput::Ipv4,
        )
        .is_err());
        // v2 non-optimized with an ADJACENT pair (0..9, 10..19): the
        // adjacency clears C's derived flag, and C then trusts the
        // header (src/ipset6_binary.c:57-60) -- header 7 matches
        // neither sum nor merge and is still loadable input. A reader
        // that compared anyway would reject what both legacy readers
        // load.
        assert!(load(
            &payload("aXByYW5nZSBiaW5hcnkgZm9ybWF0IHYyLjAKaXB2Ngpub24tb3B0aW1pemVkCnJlY29yZCBzaXplIDMyCnJlY29yZHMgMgpieXRlcyA2OApsaW5lcyAyCnVuaXF1ZSBpcHMgNwpNPCsaAAAAAAAAAAAAAAAAAAAAAAkAAAAAAAAAAAAAAAAAAAAKAAAAAAAAAAAAAAAAAAAAEwAAAAAAAAAAAAAAAAAAAA=="),
            AddressFamilyInput::Ipv6,
        )
        .is_ok());
    }

    #[test]
    fn bom_behavior_matches_released_family_parsers() {
        let ipv4 = options(AddressFamilyInput::Ipv4, 32, true);
        assert!(parse_text_line("\u{feff}1.2.3.4".as_bytes(), ipv4).is_err());

        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-input-bom-{}-{unique}",
            std::process::id()
        ));
        std::fs::create_dir(&directory).unwrap();
        let first = directory.join("first.txt");
        let second = directory.join("second.txt");
        std::fs::write(&first, b"\xef\xbb\xbf2001:db8::1\n").unwrap();
        std::fs::write(&second, b"2001:db8::1\n\xef\xbb\xbf2001:db8::2\n").unwrap();
        let mut source = TextInputSource::<Ipv6Key>::new(
            vec![first.display().to_string()],
            options(AddressFamilyInput::Ipv6, 128, true),
            true,
            10,
        )
        .unwrap();
        assert_eq!(source.next_batch().unwrap().unwrap().len(), 1);

        let mut source = TextInputSource::<Ipv6Key>::new(
            vec![second.display().to_string()],
            options(AddressFamilyInput::Ipv6, 128, true),
            true,
            10,
        )
        .unwrap();
        assert!(source.next_batch().is_err());
        std::fs::remove_file(first).unwrap();
        std::fs::remove_file(second).unwrap();
        std::fs::remove_dir(directory).unwrap();
    }

    #[test]
    fn legacy_v4_binary_input_streams_record_payload() {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-input-binary-{}-{unique}",
            std::process::id()
        ));
        let mut bytes = Vec::new();
        bytes.extend_from_slice(b"iprange binary format v1.0\n");
        bytes.extend_from_slice(b"optimized\n");
        bytes.extend_from_slice(b"record size 8\n");
        bytes.extend_from_slice(b"records 2\n");
        bytes.extend_from_slice(b"bytes 20\n");
        bytes.extend_from_slice(b"lines 2\n");
        bytes.extend_from_slice(b"unique ips 3\n");
        bytes.extend_from_slice(&0x1a2b_3c4du32.to_ne_bytes());
        bytes.extend_from_slice(&1u32.to_ne_bytes());
        bytes.extend_from_slice(&2u32.to_ne_bytes());
        bytes.extend_from_slice(&5u32.to_ne_bytes());
        bytes.extend_from_slice(&5u32.to_ne_bytes());
        std::fs::write(&path, bytes).unwrap();

        let mut source = TextInputSource::<Ipv4Key>::new(
            vec![path.display().to_string()],
            options(AddressFamilyInput::Ipv4, 32, true),
            true,
            10,
        )
        .unwrap();
        let ranges = source.next_batch().unwrap().unwrap();
        assert_eq!(ranges.len(), 2);
        assert_eq!((ranges[0].from.0, ranges[0].to.0), (1, 2));
        assert_eq!((ranges[1].from.0, ranges[1].to.0), (5, 5));
        assert!(source.next_batch().unwrap().is_none());
        std::fs::remove_file(path).unwrap();
    }

    #[test]
    fn at_expansion_bounds_total_paths_and_reads_lists() {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-input-expand-{}-{unique}",
            std::process::id()
        ));
        std::fs::create_dir(&directory).unwrap();
        let first = directory.join("01.txt");
        let second = directory.join("10.txt");
        std::fs::write(&first, b"1.2.3.4\n").unwrap();
        std::fs::write(&second, b"5.6.7.8\n").unwrap();
        let expanded = expand_paths(
            vec![format!("@{}", directory.display())],
            true,
            1,
            1_048_576,
        )
        .unwrap_err();
        assert_eq!(expanded.code(), "invalid_path");
        let expanded = expand_paths(
            vec![format!("@{}", directory.display())],
            true,
            2,
            1_048_576,
        )
        .unwrap();
        assert_eq!(expanded.len(), 2);
        std::fs::remove_file(first).unwrap();
        std::fs::remove_file(second).unwrap();
        std::fs::remove_dir(directory).unwrap();
    }

    #[test]
    fn line_bound_applies_before_parsing() {
        let mut reader = io::Cursor::new(b"12345\n");
        let mut line = Vec::new();
        let error = read_limited_line(&mut reader, 4, &mut line).unwrap_err();
        assert_eq!(error.code(), "input_format");
    }
}

/// Every caller path is inspected before it is opened, and an inspection
/// cannot close the window before the open. Each pin below therefore calls
/// one arm's own open helper with a writerless FIFO already at the path, so
/// the open itself — never the earlier check — is what must refuse, with
/// that arm's non-regular class. The helpers run on a bounded thread: an
/// open that waits for a writer fails the test instead of hanging the suite.
#[cfg(all(test, unix))]
mod open_refusal_tests {
    use super::{open_file_list, open_input_file};
    use crate::io::caller_open::pin_support;

    #[test]
    fn input_open_refuses_fifo_as_invalid_path() {
        let directory = pin_support::scratch_dir("input");
        let path = directory.join("in.txt");
        pin_support::mkfifo(&path);
        let refusal = pin_support::prompt("input", move || {
            open_input_file(&path)
                .err()
                .map(|error| (error.code().to_owned(), error.message().to_owned()))
        });
        pin_support::remove_dir(&directory);
        let (code, message) = refusal.expect("a writerless fifo must not open as an input");
        assert_eq!(code, "invalid_path");
        assert!(
            message.contains("not a regular file"),
            "unexpected refusal: {message}"
        );
    }

    #[test]
    fn file_list_open_refuses_fifo_as_invalid_path() {
        let directory = pin_support::scratch_dir("file-list");
        let path = directory.join("list.txt");
        pin_support::mkfifo(&path);
        let refusal = pin_support::prompt("file-list", move || {
            open_file_list(&path)
                .err()
                .map(|error| (error.code().to_owned(), error.message().to_owned()))
        });
        pin_support::remove_dir(&directory);
        let (code, message) = refusal.expect("a writerless fifo must not open as a file list");
        assert_eq!(code, "invalid_path");
        assert!(
            message.contains("file list is not a regular file"),
            "unexpected refusal: {message}"
        );
    }
}

/// Arm-level pins for the two caller-path opens of this module.
///
/// The helper-level pins above prove that `open_input_file` and
/// `open_file_list` refuse a standing FIFO; they cannot prove that the input
/// arms call them, because replacing either call with a plain `File::open`
/// keeps those pins green while reintroducing the wedge: a FIFO swapped into the
/// path after the arm's own `metadata()`/`symlink_metadata()` check would block
/// the request thread inside `open(2)` forever. Each pin below therefore drives
/// the registered handler behind `iprange.v1.current.publish` — the function the
/// JSON-RPC dispatcher calls — while the path is flipped between a regular file
/// and a writerless FIFO.
///
/// The input pin repeats the caller path inside one request so a single attempt
/// gets one pre-check/open coin per repetition; the file-list pin reads a list
/// that yields no paths, so an arm that opened its list normally stops before
/// any publication work.
#[cfg(all(test, unix))]
mod open_caller_tests {
    use crate::io::caller_open::pin_support::{self, RaceRule};
    use crate::rpc::handlers::publish;
    use crate::rpc::session::SessionState;
    use serde_json::json;
    use std::path::PathBuf;

    /// Coin-flips per attempt and the attempts each pin runs. One flip landing
    /// between the arm's pre-check and its open is what a bare open cannot
    /// survive; the measured per-visit hit rate on this host is recorded in the
    /// wave report, and these counts leave a margin of many orders of magnitude
    /// over the chance that every attempt misses.
    const PATH_REPEATS: usize = 24;
    const INPUT_ATTEMPTS: usize = 40;
    const LIST_ATTEMPTS: usize = 200;
    const CONCURRENCY: usize = 4;

    fn publish_params(paths: Vec<String>, destination: &str) -> serde_json::Value {
        json!({
            "input": {
                "paths": paths,
                "family": "ipv4",
                "fix_network": true,
                "default_prefix": 32,
                "dns": {"threads": 1, "silent": true},
                "expand_at_paths": true,
                "max_line_bytes": 1048576,
                "max_expanded_paths": 100000
            },
            "feed": "feed-a",
            "value_tag": {"hex": "aa"},
            "metadata": {"mode": "clear"},
            "destination": destination,
            "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {
                "max_heap_bytes": "2097152",
                "max_output_pages": "10000",
                "max_workspace_pages": "10000",
                "max_open_files": 3
            }
        })
    }

    /// Drive one publish request whose input path is the raced file.
    fn run(params: serde_json::Value) -> Option<(String, String)> {
        publish::validate_current_publish(&params)
            .expect("the fixture params must satisfy the method schema");
        let mut state = SessionState::default();
        publish::current_publish(&mut state, params)
            .err()
            .map(|error| (error.code.to_owned(), error.message))
    }

    #[test]
    fn swapped_fifo_is_refused_by_the_input_arm() {
        let rule = RaceRule {
            code: "invalid_path",
            message: "input is not a regular file",
            other_accepted: &[],
        };
        let content: &'static [u8] = b"10.0.0.1\n";
        let arm = std::sync::Arc::new(|input: PathBuf| {
            let directory = input.parent().expect("scratch directory of the input");
            let paths = vec![input.display().to_string(); PATH_REPEATS];
            run(publish_params(
                paths,
                &directory.join("out.iprange").display().to_string(),
            ))
        });
        pin_support::assert_control(
            "text input",
            &pin_support::control("input-arm-control", content, &arm),
        );
        let report = pin_support::swap_race(
            "input-arm",
            content,
            &rule,
            INPUT_ATTEMPTS,
            CONCURRENCY,
            arm,
        );
        pin_support::assert_race("text input", &report, INPUT_ATTEMPTS);
    }

    #[test]
    fn swapped_fifo_is_refused_by_the_file_list_arm() {
        let rule = RaceRule {
            code: "invalid_path",
            message: "file list is not a regular file",
            other_accepted: &["file list contains no paths"],
        };
        let content: &'static [u8] = b"# a list with no paths\n";
        let arm = std::sync::Arc::new(|list: PathBuf| {
            let directory = list.parent().expect("scratch directory of the list");
            run(publish_params(
                vec![format!("@{}", list.display())],
                &directory.join("out.iprange").display().to_string(),
            ))
        });
        pin_support::assert_control(
            "@-file-list",
            &pin_support::control("file-list-arm-control", content, &arm),
        );
        let report = pin_support::swap_race(
            "file-list-arm",
            content,
            &rule,
            LIST_ATTEMPTS,
            CONCURRENCY,
            arm,
        );
        pin_support::assert_race("@-file-list", &report, LIST_ATTEMPTS);
    }
}
