//! Atomic, durable, budget-bounded export writers (SOW-0028).
//!
//! Every `iprange.v1.export` destination is published through one
//! same-directory private temporary file, flushed with `fsync`, linked
//! or renamed into place under the requested publication policy, and
//! followed by a directory sync. Rows are streamed in bounded batches
//! through a fixed output buffer; no export ever materializes its
//! address set, and both the row and byte budgets are checked before
//! the next row is written.
//!
//! The format encoders here are deliberately independent of the SDK:
//! they format canonical numeric addresses and leave source iteration
//! to the JSON-RPC handler.

use std::fs::{self, File, OpenOptions};
use std::io::{BufWriter, Write};
use std::path::{Path, PathBuf};

use iprange_livedb::publication::PublicationPolicy;
use iprange_livedb::Cardinality129;
use serde_json::json;
use sha2::{Digest, Sha256};

use crate::rpc::dispatch::HandlerError;
use crate::rpc::new_handle;

/// Caller-supplied export limits (`result_budget`).
#[derive(Clone, Copy, Debug)]
pub(crate) struct ExportBudget {
    pub max_rows: u64,
    pub max_output_bytes: u64,
    pub max_open_files: u32,
}

/// Complete factual result of one published export file.
#[derive(Clone, Debug)]
pub(crate) struct ExportFacts {
    pub path: String,
    pub sha256: String,
    pub rows: u64,
    /// Exact 129-bit address cardinality (binary-format-v4.md section 17).
    pub addresses: Cardinality129,
    pub bytes: u64,
}

/// Buffered writer behind one atomically published export file.
///
/// `Drop` removes an unpublished temporary file, so every error path
/// (including budget refusal) leaves the destination namespace clean.
pub(crate) struct ExportWriter {
    file: BufWriter<File>,
    temporary: PathBuf,
    destination: PathBuf,
    policy: PublicationPolicy,
    budget: ExportBudget,
    rows: u64,
    bytes: u64,
    addresses: Cardinality129,
    digest: Sha256,
}

/// Address-count argument accepted by the export writer rows: a
/// plain `u128` count (existing row-dump callers) or an exact
/// `Cardinality129` (a full IPv6 space is 2^128 and needs bit 128).
pub(crate) trait IntoAddressCount {
    fn into_count(self) -> Cardinality129;
}

impl IntoAddressCount for u128 {
    fn into_count(self) -> Cardinality129 {
        Cardinality129::from_u128(self)
    }
}

impl IntoAddressCount for Cardinality129 {
    fn into_count(self) -> Cardinality129 {
        self
    }
}

impl ExportWriter {
    pub(crate) fn create(
        destination: &Path,
        policy: PublicationPolicy,
        budget: &ExportBudget,
    ) -> Result<Self, HandlerError> {
        if budget.max_open_files == 0 {
            return Err(HandlerError::new(
                "invalid_argument",
                "not_started",
                "export requires at least one open file",
            ));
        }
        let parent = destination
            .parent()
            .filter(|value| !value.as_os_str().is_empty())
            .unwrap_or_else(|| Path::new("."))
            .to_path_buf();
        let mut temporary = parent.clone();
        temporary.push(format!(".{}.export.tmp", new_handle()?));
        let file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)
            .map_err(|error| file_error(error, "create export output"))?;
        Ok(Self {
            file: BufWriter::with_capacity(64 * 1024, file),
            temporary,
            destination: destination.to_path_buf(),
            policy,
            budget: *budget,
            rows: 0,
            bytes: 0,
            addresses: Cardinality129::ZERO,
            digest: Sha256::new(),
        })
    }

    /// Append one LF-terminated row. The address delta is the exact
    /// number of addresses the row represents, not its text length; it
    /// may be a plain `u128` count (row-dump writers) or an exact
    /// `Cardinality129` count (export spans reach 2^128 for ::/0).
    pub(crate) fn write_line(
        &mut self,
        line: &str,
        addresses: impl IntoAddressCount,
    ) -> Result<(), HandlerError> {
        self.reserve(1, line.len() + 1)?;
        self.emit(line.as_bytes(), 1, addresses.into_count())?;
        self.emit(b"\n", 0, Cardinality129::ZERO)?;
        Ok(())
    }

    /// Append raw format bytes (CSV header or a legacy binary record).
    /// `rows` counts rows represented by the chunk, not byte length.
    pub(crate) fn write_chunk(
        &mut self,
        bytes: &[u8],
        rows: u64,
        addresses: impl IntoAddressCount,
    ) -> Result<(), HandlerError> {
        self.reserve(rows, bytes.len())?;
        self.emit(bytes, rows, addresses.into_count())?;
        Ok(())
    }

    /// Flush, sync, atomically publish, and sync the directory.
    pub(crate) fn finish(mut self) -> Result<ExportFacts, HandlerError> {
        let result = self.publish();
        // Always drop the temporary when publication did not consume it.
        let _ = fs::remove_file(&self.temporary);
        result
    }

    fn reserve(&mut self, rows: u64, byte_len: usize) -> Result<(), HandlerError> {
        let next_rows = self
            .rows
            .checked_add(rows)
            .ok_or_else(|| budget_error("row count overflow", self.budget.max_rows))?;
        if next_rows > self.budget.max_rows {
            return Err(budget_error(
                &format!("row {} exceeds max_rows", next_rows),
                self.budget.max_rows,
            ));
        }
        let next_bytes = self
            .bytes
            .checked_add(byte_len as u64)
            .ok_or_else(|| budget_error("byte count overflow", self.budget.max_output_bytes))?;
        if next_bytes > self.budget.max_output_bytes {
            return Err(budget_error(
                &format!("byte {} exceeds max_output_bytes", next_bytes),
                self.budget.max_output_bytes,
            ));
        }
        self.rows = next_rows;
        self.bytes = next_bytes;
        Ok(())
    }

    fn emit(
        &mut self,
        bytes: &[u8],
        _rows: u64,
        addresses: Cardinality129,
    ) -> Result<(), HandlerError> {
        // Exact 129-bit accumulation: one single-family export never
        // exceeds 2^128 (the full IPv6 space), so overflow is a
        // counter invariant violation, never a legitimate output.
        self.addresses = self.addresses.checked_add(addresses).map_err(|_| {
            HandlerError::new(
                "output_limit",
                "not_started",
                "export address cardinality exceeded the exact 129-bit counter",
            )
        })?;
        self.digest.update(bytes);
        self.file
            .write_all(bytes)
            .map_err(|error| file_error(error, "write export output"))
    }

    fn publish(&mut self) -> Result<ExportFacts, HandlerError> {
        self.file
            .flush()
            .map_err(|error| file_error(error, "sync export output"))?;
        self.file
            .get_ref()
            .sync_all()
            .map_err(|error| file_error(error, "sync export output"))?;
        match self.policy {
            PublicationPolicy::FailIfExists => {
                // Hard-link publication is the portable no-replacement
                // atom: the destination name appears only when complete.
                // Remove the private temporary before the directory sync
                // so a crash cannot leave the temporary name durable
                // (same order as the metadata/removals publication path).
                fs::hard_link(&self.temporary, &self.destination)
                    .map_err(|error| file_error(error, "publish export output"))?;
                // The destination name now exists with the complete
                // content; a failure to remove the private temporary no
                // longer changes that, so the durable state is unknown.
                fs::remove_file(&self.temporary).map_err(|error| {
                    self.publication_failure(error, "remove export temporary", false)
                })?;
            }
            PublicationPolicy::ReplaceExisting | PublicationPolicy::ReplaceExistingNoRollback => {
                // rename(2) and MoveFileExW(REPLACE_EXISTING) replace
                // the destination atomically on both supported families.
                fs::rename(&self.temporary, &self.destination)
                    .map_err(|error| file_error(error, "publish export output"))?;
            }
        }
        let parent = self
            .destination
            .parent()
            .filter(|value| !value.as_os_str().is_empty())
            .unwrap_or_else(|| Path::new("."));
        // The destination is visible; a directory-sync failure means the
        // namespace entry's durability is unproven (outcome_unknown).
        sync_directory(parent).map_err(|error| {
            self.publication_failure(error, "sync export output directory", true)
        })?;
        let digest = self.digest.clone().finalize();
        Ok(ExportFacts {
            path: self.destination.to_string_lossy().into_owned(),
            sha256: hex_digest(&digest),
            rows: self.rows,
            addresses: self.addresses,
            bytes: self.bytes,
        })
    }

    /// Adapter publication failure once the destination name is
    /// visible: the exported bytes are published, but the durable state
    /// of the publication is unknown, so the outcome is
    /// `outcome_unknown` and the error carries the adapter-owned
    /// publication evidence (iprange-jsonrpc-v1.md: an unknown
    /// publication maps to outcome_unknown). Failures before the
    /// destination appears keep `not_started` (or the pre-existing
    /// `name_exists`).
    fn publication_failure(
        &self,
        error: std::io::Error,
        stage: &str,
        temporary_removed: bool,
    ) -> HandlerError {
        HandlerError {
            code: "io",
            outcome: "outcome_unknown",
            message: format!("{stage}: {error}"),
            details: Some(json!({
                "publication": {
                    "outcome": "outcome_unknown",
                    "publication_policy": policy_name(self.policy),
                    "path": self.destination.to_string_lossy(),
                    "stage": stage,
                    "destination_visible": true,
                    "temporary_removed": temporary_removed,
                    "rows": self.rows.to_string(),
                    "bytes": self.bytes.to_string(),
                    "addresses": self.addresses.to_string(),
                    "sha256": hex_digest(&self.digest.clone().finalize()),
                }
            })),
        }
    }
}

fn policy_name(policy: PublicationPolicy) -> &'static str {
    match policy {
        PublicationPolicy::FailIfExists => "fail_if_exists",
        PublicationPolicy::ReplaceExisting => "replace_existing",
        PublicationPolicy::ReplaceExistingNoRollback => "replace_existing_no_rollback",
    }
}

impl Drop for ExportWriter {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.temporary);
    }
}

/// Budget refusal happens before the next row is written, so no
/// destination or partial file is ever visible.
fn budget_error(detail: &str, limit: u64) -> HandlerError {
    HandlerError::new(
        "output_limit",
        "not_started",
        format!("export refused before exceeding budget: {detail} (limit {limit})"),
    )
}

fn file_error(error: std::io::Error, operation: &str) -> HandlerError {
    let message = format!("{operation}: {error}");
    if error.kind() == std::io::ErrorKind::AlreadyExists {
        HandlerError::new("name_exists", "not_started", message)
    } else {
        HandlerError::new("io", "not_started", message)
    }
}

fn sync_directory(parent: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        File::open(parent)?.sync_all()?;
    }
    #[cfg(not(unix))]
    let _ = parent;
    Ok(())
}

pub(crate) fn hex_digest(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

/// Write the canonical address text for one numeric family-local
/// address into a reusable line buffer (no per-row allocation).
pub(crate) fn push_address(output: &mut String, value: u128, host_prefix: u32) {
    use std::fmt::Write as _;
    if host_prefix == 32 {
        let v4 = u32::try_from(value).expect("IPv4 value fits u32");
        let _ = write!(output, "{}", std::net::Ipv4Addr::from(v4));
    } else {
        let _ = write!(output, "{}", std::net::Ipv6Addr::from(value.to_be_bytes()));
    }
}

/// `io::Write` adapter over a `String` so `serde_json` can append a
/// compact value directly into a reusable line buffer.
struct StringWriter<'a>(&'a mut String);

impl std::io::Write for StringWriter<'_> {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        let text = std::str::from_utf8(bytes)
            .map_err(|error| std::io::Error::new(std::io::ErrorKind::InvalidData, error))?;
        self.0.push_str(text);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

/// Serialize one JSON value into a reusable line buffer without a
/// temporary string (all v4 wire values are valid UTF-8).
pub(crate) fn write_json_value(output: &mut String, value: &serde_json::Value) -> Result<(), HandlerError> {
    serde_json::to_writer(StringWriter(output), value).map_err(|error| {
        HandlerError::new(
            "io",
            "not_started",
            format!("export row JSON encoding failed: {error}"),
        )
    })
}

/// RFC-4180 CSV field: quote only when the field requires it and
/// double embedded quotes (reusable-buffer form).
pub(crate) fn write_csv_field(output: &mut String, value: &str) {
    let needs_quotes = value
        .bytes()
        .any(|byte| byte == b',' || byte == b'"' || byte == b'\r' || byte == b'\n');
    if !needs_quotes {
        output.push_str(value);
        return;
    }
    output.push('"');
    for character in value.chars() {
        if character == '"' {
            output.push('"');
        }
        output.push(character);
    }
    output.push('"');
}

/// JSON string literal with standard escaping, written straight into a
/// reusable line buffer (byte-identical with serde_json string output:
/// quotes and backslashes double to two bytes, control bytes escape to
/// `\b \f \n \r \t` or `\u00xx`, and every other character passes
/// through as its exact UTF-8 encoding).
pub(crate) fn push_json_string(output: &mut String, value: &str) {
    use std::fmt::Write as _;
    output.push('"');
    for character in value.chars() {
        match character {
            '"' => output.push_str("\\\""),
            '\\' => output.push_str("\\\\"),
            '\x08' => output.push_str("\\b"),
            '\x0c' => output.push_str("\\f"),
            '\n' => output.push_str("\\n"),
            '\r' => output.push_str("\\r"),
            '\t' => output.push_str("\\t"),
            character if (character as u32) < 0x20 => {
                let _ = write!(output, "\\u{:04x}", character as u32);
            }
            _ => output.push(character),
        }
    }
    output.push('"');
}


/// Enabled netset prefix lengths for one address family.
#[derive(Clone, Debug)]
pub(crate) struct PrefixFilter {
    host_prefix: u32,
    enabled: Vec<bool>,
}

impl PrefixFilter {
    pub(crate) fn all(host_prefix: u32) -> Self {
        Self {
            host_prefix,
            enabled: vec![true; host_prefix as usize + 1],
        }
    }

    /// Enable every prefix from `minimum` through the host prefix.
    pub(crate) fn min_prefix(host_prefix: u32, minimum: u32) -> Self {
        let mut filter = Self::all(host_prefix);
        for prefix in 0..minimum {
            filter.enabled[prefix as usize] = false;
        }
        filter
    }

    /// Enable exactly the listed prefixes (host prefix must be listed).
    pub(crate) fn listed(host_prefix: u32, prefixes: &[u32]) -> Self {
        let mut filter = Self::all(host_prefix);
        for enabled in filter.enabled.iter_mut() {
            *enabled = false;
        }
        for prefix in prefixes {
            filter.enabled[*prefix as usize] = true;
        }
        filter
    }

    fn enabled(&self, prefix: u32) -> bool {
        self.enabled[prefix as usize]
    }

    fn host_prefix(&self) -> u32 {
        self.host_prefix
    }
}

/// Exact inclusive size of one canonical `[from, to]` span.
///
/// The full IPv6 space is 2^128, which exceeds u128, so the span is
/// returned as an exact 129-bit cardinality (binary-format-v4.md
/// section 17).
pub(crate) fn inclusive_span(from: u128, to: u128) -> Cardinality129 {
    if from == 0 && to == u128::MAX {
        Cardinality129::FULL_IPV6_SPACE
    } else {
        Cardinality129::from_u128(to.saturating_sub(from).saturating_add(1))
    }
}

/// Write one `from-to` line; a singleton is emitted as its single
/// address (iprange-jsonrpc-v1.md, Export: `ranges`). Reuses the
/// caller's line buffer.
pub(crate) fn push_ranges_line(output: &mut String, from: u128, to: u128, host_prefix: u32) {
    if from == to {
        push_address(output, from, host_prefix);
    } else {
        push_address(output, from, host_prefix);
        output.push('-');
        push_address(output, to, host_prefix);
    }
}

/// Emit the canonical minimal CIDR blocks covering `[from, to]` using
/// only enabled prefixes, in address order (the released
/// `split_range()` algorithm generalized to both families).
///
/// Each line is written into `line` and the callback receives it with
/// its exact address span; the buffer is reused across blocks.
pub(crate) fn emit_netset(
    from: u128,
    to: u128,
    filter: &PrefixFilter,
    line: &mut String,
    emit: &mut dyn FnMut(&str, Cardinality129) -> Result<(), HandlerError>,
) -> Result<(), HandlerError> {
    split_netset(0, 0, from, to, filter, line, emit)
}

#[allow(clippy::too_many_arguments)]
fn split_netset(
    base: u128,
    prefix: u32,
    from: u128,
    to: u128,
    filter: &PrefixFilter,
    line: &mut String,
    emit: &mut dyn FnMut(&str, Cardinality129) -> Result<(), HandlerError>,
) -> Result<(), HandlerError> {
    let host = filter.host_prefix();
    let bits = host - prefix;
    let network_end = if bits >= 128 {
        u128::MAX
    } else {
        base | ((1u128 << bits) - 1)
    };
    if from == base && to == network_end && filter.enabled(prefix) {
        line.clear();
        push_address(line, base, host);
        if prefix != host {
            line.push('/');
            use std::fmt::Write as _;
            let _ = write!(line, "{prefix}");
        }
        let span = inclusive_span(base, network_end);
        return emit(line, span);
    }
    let half = base | (1u128 << (host - prefix - 1));
    if to < half {
        split_netset(base, prefix + 1, from, to, filter, line, emit)
    } else if from >= half {
        split_netset(half, prefix + 1, from, to, filter, line, emit)
    } else {
        split_netset(base, prefix + 1, from, half - 1, filter, line, emit)?;
        split_netset(half, prefix + 1, half, to, filter, line, emit)
    }
}

/// Emit every address in `[from, to]`, one per line, reusing `line`.
pub(crate) fn emit_ipset(
    from: u128,
    to: u128,
    host_prefix: u32,
    line: &mut String,
    emit: &mut dyn FnMut(&str) -> Result<(), HandlerError>,
) -> Result<(), HandlerError> {
    for address in from..=to {
        line.clear();
        push_address(line, address, host_prefix);
        emit(line)?;
    }
    Ok(())
}

/// The released legacy binary header. `records` are the canonical
/// optimized records that follow; `unique_ips` is the exact address
/// count.
///
/// The released header widths bound the representable cardinality:
/// IPv4 parses `unique ips` into a uint64 (src/ipset_binary.c) and
/// IPv6 into a uint128 (src/ipset6_binary.c). Callers must refuse
/// values beyond those widths before building the header; the full
/// IPv6 space (2^128) cannot be represented at all.
pub(crate) fn legacy_binary_header(ipv6: bool, records: u64, unique_ips: u128) -> String {
    let record_size: u64 = if ipv6 { 32 } else { 8 };
    let bytes = u64::from(record_size) * records + 4;
    let mut header = String::new();
    if ipv6 {
        header.push_str("iprange binary format v2.0\n");
        header.push_str("ipv6\n");
    } else {
        header.push_str("iprange binary format v1.0\n");
    }
    header.push_str("optimized\n");
    header.push_str(&format!("record size {record_size}\n"));
    header.push_str(&format!("records {records}\n"));
    header.push_str(&format!("bytes {bytes}\n"));
    // A v4 export has no source line accounting; the minimal factual
    // value that satisfies the released loader is the record count.
    header.push_str(&format!("lines {records}\n"));
    header.push_str(&format!("unique ips {unique_ips}\n"));
    header
}

pub(crate) const LEGACY_ENDIANNESS_MARKER: [u8; 4] = 0x1A2B_3C4Du32.to_ne_bytes();

/// One released `network_addr_t` record: two host-order `in_addr_t`
/// values in native byte order (src/ipset_binary.c).
pub(crate) fn legacy_binary_record_v4(from: u32, to: u32) -> [u8; 8] {
    let mut record = [0u8; 8];
    record[..4].copy_from_slice(&from.to_ne_bytes());
    record[4..].copy_from_slice(&to.to_ne_bytes());
    record
}

/// One released `network_addr6_t` record: two host-order `uint128_t`
/// values in native layout (src/ipset6_binary.c).
pub(crate) fn legacy_binary_record_v6(from: u128, to: u128) -> [u8; 32] {
    let mut record = [0u8; 32];
    record[..16].copy_from_slice(&from.to_ne_bytes());
    record[16..].copy_from_slice(&to.to_ne_bytes());
    record
}

/// Lower bound of the binary header used for early byte-budget checks
/// while counting records (all count fields have at least one digit).
pub(crate) fn legacy_binary_min_header_bytes(ipv6: bool) -> u64 {
    if ipv6 {
        108
    } else {
        100
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn lines(from: u128, to: u128, filter: &PrefixFilter) -> (Vec<String>, Vec<Cardinality129>) {
        let mut output = Vec::new();
        let mut spans = Vec::new();
        let mut line = String::new();
        emit_netset(from, to, filter, &mut line, &mut |text, span| {
            output.push(text.to_owned());
            spans.push(span);
            Ok(())
        })
        .unwrap();
        (output, spans)
    }

    #[test]
    fn netset_is_canonical_and_minimal() {
        let filter = PrefixFilter::all(32);
        let (output, spans) = lines(0xC000_0200, 0xC000_02FF, &filter);
        assert_eq!(output, ["192.0.2.0/24"]);
        assert_eq!(spans, [Cardinality129::from_u64(256)]);
        let (crossing, spans) = lines(0xC000_0204, 0xC000_0207, &filter);
        assert_eq!(crossing, ["192.0.2.4/30"]);
        assert_eq!(spans, [Cardinality129::from_u64(4)]);
        let (single, spans) = lines(0xC000_020A, 0xC000_020A, &filter);
        assert_eq!(single, ["192.0.2.10"]);
        assert_eq!(spans, [Cardinality129::from_u64(1)]);
    }

    #[test]
    fn netset_honors_min_prefix_and_prefixes() {
        let minimum = PrefixFilter::min_prefix(32, 25);
        let (output, spans) = lines(0xC000_0200, 0xC000_02FF, &minimum);
        assert_eq!(output, ["192.0.2.0/25", "192.0.2.128/25"]);
        assert_eq!(
            spans,
            [Cardinality129::from_u64(128), Cardinality129::from_u64(128)]
        );
        let listed = PrefixFilter::listed(32, &[24, 32]);
        let (output, _) = lines(0xC000_0200, 0xC000_02FF, &listed);
        assert_eq!(output, ["192.0.2.0/24"]);
        let host_only = PrefixFilter::listed(32, &[32]);
        let (expanded, _) = lines(0x0A00_0000, 0x0A00_0002, &host_only);
        assert_eq!(expanded, ["10.0.0.0", "10.0.0.1", "10.0.0.2"]);
    }

    #[test]
    fn netset_covers_boundary_splits_and_full_space() {
        let filter = PrefixFilter::all(32);
        let (output, _) = lines(0xC000_0205, 0xC000_0306, &filter);
        assert_eq!(
            output,
            [
                "192.0.2.5",
                "192.0.2.6/31",
                "192.0.2.8/29",
                "192.0.2.16/28",
                "192.0.2.32/27",
                "192.0.2.64/26",
                "192.0.2.128/25",
                "192.0.3.0/30",
                "192.0.3.4/31",
                "192.0.3.6",
            ]
        );
        let v6 = PrefixFilter::all(128);
        let (output, spans) = lines(
            0x2001_0db8_0000_0000_0000_0000_0000_0000,
            0x2001_0db8_0000_0000_0000_0000_0000_00ff,
            &v6,
        );
        assert_eq!(output, ["2001:db8::/120"]);
        assert_eq!(spans, [Cardinality129::from_u64(256)]);
        // The full IPv6 space is 2^128 addresses, exact 129-bit value.
        let (full, full_spans) = lines(0, u128::MAX, &v6);
        assert_eq!(full, ["::/0"]);
        assert_eq!(full_spans, [Cardinality129::FULL_IPV6_SPACE]);
    }

    #[test]
    fn ranges_lines_are_ordered_with_single_addresses() {
        let mut line = String::new();
        push_ranges_line(&mut line, 0xC000_0200, 0xC000_0204, 32);
        assert_eq!(line, "192.0.2.0-192.0.2.4");
        line.clear();
        push_ranges_line(&mut line, 0xC000_020A, 0xC000_020A, 32);
        assert_eq!(line, "192.0.2.10");
    }

    #[test]
    fn csv_fields_follow_rfc4180() {
        let mut line = String::new();
        write_csv_field(&mut line, "feed-a");
        assert_eq!(line, "feed-a");
        line.clear();
        write_csv_field(&mut line, "42");
        assert_eq!(line, "42");
        line.clear();
        write_csv_field(&mut line, r#"{"asn":64512,"country_id":1}"#);
        assert_eq!(line, r#""{""asn"":64512,""country_id"":1}""#);
    }

    #[test]
    fn ipset_expands_each_address() {
        let mut output = Vec::new();
        let mut line = String::new();
        emit_ipset(0xC000_020A, 0xC000_020C, 32, &mut line, &mut |text| {
            output.push(text.to_owned());
            Ok(())
        })
        .unwrap();
        assert_eq!(output, ["192.0.2.10", "192.0.2.11", "192.0.2.12"]);
    }

    #[test]
    fn legacy_binary_header_and_records_match_released_layout() {
        let header = legacy_binary_header(false, 2, 4660);
        assert_eq!(
            header,
            concat!(
                "iprange binary format v1.0\n",
                "optimized\n",
                "record size 8\n",
                "records 2\n",
                "bytes 20\n",
                "lines 2\n",
                "unique ips 4660\n"
            )
        );
        assert_eq!(legacy_binary_record_v4(0xC000_0200, 0xC000_02FF), {
            let mut expected = [0u8; 8];
            expected[..4].copy_from_slice(&0xC000_0200u32.to_ne_bytes());
            expected[4..].copy_from_slice(&0xC000_02FFu32.to_ne_bytes());
            expected
        });
        let v6_header = legacy_binary_header(true, 1, 1);
        assert!(v6_header.starts_with("iprange binary format v2.0\nipv6\noptimized\n"));
        assert!(v6_header.contains("record size 32\nrecords 1\nbytes 36\n"));
        assert_eq!(legacy_binary_record_v6(1, 2)[..16], 1u128.to_ne_bytes());
    }

    #[test]
    fn writer_refuses_budgets_before_exceeding_them() {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-export-budget-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir_all(&directory).unwrap();
        let destination = directory.join("out.netset");
        let budget = ExportBudget {
            max_rows: 2,
            max_output_bytes: 100,
            max_open_files: 1,
        };
        let mut writer =
            ExportWriter::create(&destination, PublicationPolicy::FailIfExists, &budget).unwrap();
        writer
            .write_line("192.0.2.0/24", Cardinality129::from_u64(256))
            .unwrap();
        writer
            .write_line("192.0.2.1", Cardinality129::from_u64(1))
            .unwrap();
        let row_error = writer
            .write_line("192.0.2.2", Cardinality129::from_u64(1))
            .unwrap_err();
        assert_eq!(
            (row_error.code, row_error.outcome),
            ("output_limit", "not_started")
        );
        let mut writer =
            ExportWriter::create(&destination, PublicationPolicy::FailIfExists, &budget).unwrap();
        writer
            .write_line("192.0.2.0/24", Cardinality129::from_u64(256))
            .unwrap();
        writer
            .write_line("198.51.100.0/24", Cardinality129::from_u64(256))
            .unwrap();
        let byte_error = writer
            .write_line(&"x".repeat(90), Cardinality129::from_u64(1))
            .unwrap_err();
        assert_eq!(
            (byte_error.code, byte_error.outcome),
            ("output_limit", "not_started")
        );
        drop(writer);
        assert!(!destination.exists());
        fs::remove_dir_all(&directory).unwrap();
    }

    #[test]
    fn writer_publishes_atomically_with_exact_digest() {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-export-digest-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir_all(&directory).unwrap();
        let destination = directory.join("out.ipset");
        let budget = ExportBudget {
            max_rows: 10,
            max_output_bytes: 1024,
            max_open_files: 1,
        };
        let mut writer =
            ExportWriter::create(&destination, PublicationPolicy::FailIfExists, &budget).unwrap();
        writer
            .write_line("192.0.2.10", Cardinality129::from_u64(1))
            .unwrap();
        writer
            .write_line("192.0.2.11", Cardinality129::from_u64(1))
            .unwrap();
        let facts = writer.finish().unwrap();
        let expected = b"192.0.2.10\n192.0.2.11\n";
        assert_eq!(fs::read(&destination).unwrap(), expected);
        assert_eq!(facts.bytes as usize, expected.len());
        assert_eq!(facts.rows, 2);
        assert_eq!(facts.addresses, Cardinality129::from_u64(2));
        assert_eq!(facts.sha256, hex_digest(&Sha256::digest(expected)));
        assert!(!directory.join(format!(".{}.export.tmp", "")).exists());
        fs::remove_dir_all(&directory).unwrap();
    }

    #[test]
    fn push_json_string_matches_serde_json_and_is_utf8_safe() {
        // The old byte loop mapped bytes >= 0x80 through
        // char::from(byte), Latin-1-mangling every multi-byte UTF-8
        // character (final-review finding T4). The encoder must be
        // byte-identical with serde_json string output: valid UTF-8
        // for non-ASCII inputs and unchanged for ASCII inputs.
        for input in [
            "feed-a",
            "42",
            "",
            "a\"b\\c",
            "line\nfeed\tTab\r",
            "\u{1}\u{1f}",
            "greek: αβγ δ",
            "emoji: 😀🚀",
            "中文字符 and \"quotes\"",
            "\u{7f}",
            "café au lait — naïve",
        ] {
            let mut line = String::new();
            push_json_string(&mut line, input);
            let expected =
                serde_json::to_string(&serde_json::Value::String(input.to_owned())).unwrap();
            assert_eq!(line, expected, "push_json_string diverged for {input:?}");
        }
    }

    #[test]
    fn publication_failure_carries_durable_state_evidence() {
        // Once the destination name is visible, a publication-stage
        // failure reports outcome_unknown with the adapter-owned
        // evidence: path, policy, stage, and the exact exported facts
        // (final-review finding T3).
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-publication-evidence-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir_all(&directory).unwrap();
        let destination = directory.join("out.net");
        let budget = ExportBudget {
            max_rows: 10,
            max_output_bytes: 1024,
            max_open_files: 1,
        };
        let mut writer =
            ExportWriter::create(&destination, PublicationPolicy::FailIfExists, &budget).unwrap();
        writer
            .write_line("192.0.2.10", Cardinality129::from_u64(1))
            .unwrap();
        writer
            .write_line("192.0.2.11", Cardinality129::from_u64(1))
            .unwrap();

        let error = writer.publication_failure(
            std::io::Error::new(std::io::ErrorKind::Other, "simulated sync failure"),
            "sync export output directory",
            true,
        );
        assert_eq!((error.code, error.outcome), ("io", "outcome_unknown"));
        assert!(error.message.contains("sync export output directory"));
        let publication = error.details.unwrap();
        assert_eq!(publication["publication"]["outcome"], "outcome_unknown");
        assert_eq!(
            publication["publication"]["publication_policy"],
            "fail_if_exists"
        );
        assert_eq!(
            publication["publication"]["path"],
            destination.to_string_lossy().to_string()
        );
        assert_eq!(publication["publication"]["stage"], "sync export output directory");
        assert_eq!(publication["publication"]["destination_visible"], true);
        assert_eq!(publication["publication"]["temporary_removed"], true);
        assert_eq!(publication["publication"]["rows"], "2");
        assert_eq!(publication["publication"]["bytes"], "22");
        assert_eq!(publication["publication"]["addresses"], "2");
        let expected = hex_digest(&Sha256::digest(b"192.0.2.10\n192.0.2.11\n"));
        assert_eq!(publication["publication"]["sha256"], expected);

        // The temporary-removal stage reports the temporary as still
        // present so the caller can find the residue.
        let kept = writer.publication_failure(
            std::io::Error::new(std::io::ErrorKind::Other, "simulated removal failure"),
            "remove export temporary",
            false,
        );
        assert_eq!(kept.outcome, "outcome_unknown");
        assert_eq!(
            kept.details.unwrap()["publication"]["temporary_removed"],
            false
        );
        drop(writer);
        fs::remove_dir_all(&directory).unwrap();
    }
}
