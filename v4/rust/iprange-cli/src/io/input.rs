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
        expected_unique: u128,
        actual_unique: u128,
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
            expected_unique,
            actual_unique: 0,
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
        if self.batch.len() >= BATCH_CAPACITY || !K::family_matches(range) {
            return Err(self.format_error("range does not fit the bounded parser batch/family"));
        }
        self.batch.push(K::address_range(range));
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
        if self.finished {
            return Ok(None);
        }
        self.batch.clear();
        loop {
            if self.batch.len() >= BATCH_CAPACITY {
                return Ok(Some(()));
            }
            if self.active.is_none() {
                match self.open_next().map_err(input_sdk_error)? {
                    true => {}
                    false => {
                        self.finished = true;
                        if self.batch.is_empty() {
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
                        eprintln!(
                            "iprange: {}: {dropped_ipv6} IPv6 entries dropped (use -6 for IPv6 mode)",
                            self.path_label()
                        );
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
                expected_unique,
                actual_unique,
                previous,
            }) => {
                if *remaining == 0 {
                    let mut trailing = [0u8; 1];
                    let count = reader
                        .read(&mut trailing)
                        .map_err(|error| InputError::io(format!("check binary tail: {error}")))?;
                    if count != 0 {
                        return Err(InputError::format("trailing data after binary payload"));
                    }
                    if *optimized && *actual_unique != *expected_unique {
                        return Err(InputError::format(
                            "binary unique count does not match payload",
                        ));
                    }
                    return Ok(Step::BinaryEnd);
                }
                let ipv6 = self.options.family == AddressFamilyInput::Ipv6;
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
                let unique = record
                    .to
                    .checked_sub(record.from)
                    .and_then(|value| value.checked_add(1))
                    .ok_or_else(|| InputError::format("binary range size overflows"))?;
                let next_unique = actual_unique
                    .checked_add(unique)
                    .ok_or_else(|| InputError::format("binary unique count overflows"))?;
                if *optimized {
                    if let Some((_, previous_to)) = *previous {
                        if record.from <= previous_to
                            || previous_to.checked_add(1) == Some(record.from)
                        {
                            return Err(InputError::format(
                                "optimized binary payload is unordered, overlapping, or adjacent",
                            ));
                        }
                    }
                }
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
        let file = File::open(referenced).map_err(|error| {
            InputError::io(format!("open file list {}: {error}", referenced.display()))
        })?;
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
    File::open(path).map_err(|error| {
        if error.kind() == io::ErrorKind::NotFound {
            InputError::invalid_path(format!("input does not exist: {}", path.display()))
        } else {
            InputError::io(format!("open input {}: {error}", path.display()))
        }
    })
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
    let rest = trim_leading(line);
    if rest.is_empty() || rest[0] == b'#' || rest[0] == b';' {
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
    if hostname_is_complete(rest) {
        return Ok(ParsedLine::Hostname(rest.to_vec()));
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

fn hostname_is_complete(line: &[u8]) -> bool {
    let (token, rest) = scan_while(line, is_hostname_byte);
    if token.len() > 255 {
        return false;
    }
    !token.is_empty() && complete_after_token(rest)
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
                        eprintln!("iprange: DNS: '{name}' will be retried: {error}");
                    }
                    std::thread::sleep(std::time::Duration::from_secs(1));
                    continue;
                }
                if !silent {
                    eprintln!("iprange: DNS: '{name}' failed permanently: {error}");
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
    fn bom_behavior_matches_released_family_parsers() {
        let ipv4 = options(AddressFamilyInput::Ipv4, 32, true);
        assert!(parse_text_line("\u{feff}1.2.3.4".as_bytes(), ipv4).is_err());

        let directory = std::env::temp_dir().join(format!(
            "iprange-input-bom-{}-{:?}",
            std::process::id(),
            std::time::SystemTime::now()
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
        let path = std::env::temp_dir().join(format!(
            "iprange-input-binary-{}-{:?}",
            std::process::id(),
            std::time::SystemTime::now()
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
        let directory = std::env::temp_dir().join(format!(
            "iprange-input-expand-{}-{:?}",
            std::process::id(),
            std::time::SystemTime::now()
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
