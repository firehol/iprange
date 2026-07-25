use std::fs::{self, File, OpenOptions};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use super::super::direct_build::{construct, DirectConstruction};
use super::*;
use crate::cardinality::Cardinality129;
use crate::contract::{ValueTag, PAGE_SIZE};
use crate::database::ImmutableReader;
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::range_cursor::RangeDirection;
use crate::range_tree;
use crate::recovery::{RecoverySinkControl, RecoveryUnknownEnvelope};
use crate::slotted_page::{self, Header};
use crate::validation::{validate, ValidationBudget, ValidationMode, ValidationSinkControl};
use crate::{crc32c, file_io};

pub(super) struct Paths {
    pub(super) source: PathBuf,
    pub(super) output: PathBuf,
    pub(super) scratch: PathBuf,
}

impl Paths {
    pub(super) fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = std::env::temp_dir().join(format!(
            "iprange-v4-direct-recovery-{label}-{}-{unique}",
            std::process::id()
        ));
        let scratch = base.with_extension("scratch");
        fs::create_dir(&scratch).unwrap();
        Self {
            source: base.with_extension("source"),
            output: base.with_extension("output"),
            scratch,
        }
    }
}

impl Drop for Paths {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.source);
        let _ = fs::remove_file(&self.output);
        let _ = fs::remove_dir_all(&self.scratch);
    }
}

#[test]
fn ordered_direct_recovery_streams_a_canonical_output() {
    let paths = Paths::new("ordered");
    let source = source_builder(&paths.source);
    let meta = finish_ranges(
        source,
        &[(0, 9, 1), (10, 19, 2), (30, 39, 2), (100, 199, 3)],
    );
    let source = File::open(&paths.source).unwrap();
    let output = output_builder(&paths.output);
    let mut unknown = Vec::new();

    let DirectConstruction {
        finished, report, ..
    } = construct(
        &source,
        meta,
        output,
        &budget(1024 * 1024),
        &CancellationToken::new(),
        &mut |envelope: &RecoveryUnknownEnvelope| {
            unknown.push(*envelope);
            Ok(RecoverySinkControl::Continue)
        },
    )
    .unwrap();
    drop(finished.file);

    assert!(unknown.is_empty());
    assert_eq!(report.ranges.examined, 4);
    assert_eq!(report.ranges.accepted, 4);
    assert_eq!(report.ranges.rejected, 0);
    assert_eq!(report.verified_addresses, Cardinality129::from_u64(130));
    assert_eq!(
        output_ranges(&paths.output),
        vec![(0, 9, 1), (10, 19, 2), (30, 39, 2), (100, 199, 3)]
    );
    validate_clean(&paths.output);
}

#[test]
fn crc_damaged_leaf_is_skipped_and_reported_as_unbounded() {
    let paths = Paths::new("crc");
    let mut source = source_builder(&paths.source);
    for index in 0..2_000u32 {
        let from = index * 3;
        source
            .push_direct_v4(Ipv4Key(from), Ipv4Key(from + 1), index)
            .unwrap();
    }
    let finished = source.finish().unwrap();
    let meta = finished.meta;
    let damaged = first_child(&finished.file, meta);
    corrupt_crc(&finished.file, damaged);
    drop(finished.file);

    let source = File::open(&paths.source).unwrap();
    let mut unknown = Vec::new();
    let DirectConstruction {
        finished, report, ..
    } = construct(
        &source,
        meta,
        output_builder(&paths.output),
        &budget(2 * 1024 * 1024),
        &CancellationToken::new(),
        &mut |envelope: &RecoveryUnknownEnvelope| {
            unknown.push(*envelope);
            Ok(RecoverySinkControl::Continue)
        },
    )
    .unwrap();
    drop(finished.file);

    assert!(report.has_unbounded_unknown);
    assert_eq!(report.pages.rejected, 1);
    assert_eq!(report.pages.io_unreadable, 0);
    assert!(report.ranges.accepted > 0);
    assert!(report.ranges.accepted < 2_000);
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::PageCrcMismatch));
    validate_clean(&paths.output);
}

#[test]
fn an_overlap_component_is_rejected_whole() {
    let paths = Paths::new("overlap");
    let meta = finish_ranges(
        source_builder(&paths.source),
        &[(0, 9, 1), (20, 29, 2), (40, 49, 3)],
    );
    rewrite_second_start(&paths.source, meta, 5);
    let source = File::open(&paths.source).unwrap();
    let mut unknown = Vec::new();
    let DirectConstruction {
        finished, report, ..
    } = construct(
        &source,
        meta,
        output_builder(&paths.output),
        &budget(1024 * 1024),
        &CancellationToken::new(),
        &mut |envelope: &RecoveryUnknownEnvelope| {
            unknown.push(*envelope);
            Ok(RecoverySinkControl::Continue)
        },
    )
    .unwrap();
    drop(finished.file);

    assert_eq!(report.ranges.examined, 3);
    assert_eq!(report.ranges.accepted, 1);
    assert_eq!(report.ranges.rejected, 2);
    assert_eq!(report.rejected_addresses, Cardinality129::from_u64(30));
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::RangeOverlap));
    assert_eq!(output_ranges(&paths.output), vec![(40, 49, 3)]);
    validate_clean(&paths.output);
}

#[test]
fn disordered_readable_records_are_sorted_with_bounded_heap() {
    let paths = Paths::new("unordered");
    let meta = finish_ranges(
        source_builder(&paths.source),
        &[(0, 9, 1), (20, 29, 2), (40, 49, 3)],
    );
    swap_first_two_records(&paths.source, meta);
    let source = File::open(&paths.source).unwrap();
    let mut unknown = Vec::new();
    let DirectConstruction {
        finished, report, ..
    } = construct(
        &source,
        meta,
        output_builder(&paths.output),
        &budget(4096),
        &CancellationToken::new(),
        &mut |envelope: &RecoveryUnknownEnvelope| {
            unknown.push(*envelope);
            Ok(RecoverySinkControl::Continue)
        },
    )
    .unwrap();
    drop(finished.file);

    assert_eq!(report.ranges.accepted, 3);
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::TreeOrderInvalid));
    assert_eq!(
        output_ranges(&paths.output),
        vec![(0, 9, 1), (20, 29, 2), (40, 49, 3)]
    );
    validate_clean(&paths.output);
}

#[test]
fn disordered_recovery_refuses_insufficient_heap_before_output_mutation() {
    let paths = Paths::new("budget");
    let meta = finish_ranges(
        source_builder(&paths.source),
        &[(0, 9, 1), (20, 29, 2), (40, 49, 3)],
    );
    swap_first_two_records(&paths.source, meta);
    let source = File::open(&paths.source).unwrap();
    let output = output_builder(&paths.output);
    let failure = construct(
        &source,
        meta,
        output,
        &budget(80),
        &CancellationToken::new(),
        &mut |_: &RecoveryUnknownEnvelope| Ok(RecoverySinkControl::Continue),
    )
    .unwrap_err();

    assert!(matches!(
        failure.cause,
        Error::BudgetExceeded("recovery page-ownership table")
    ));
    assert_eq!(failure.report.ranges.examined, 3);
    drop(failure.builder.into_file());
}

#[cfg(target_os = "linux")]
#[test]
fn disordered_direct_recovery_uses_bounded_multi_pass_scratch() {
    let paths = Paths::new("external");
    let ranges: Vec<_> = (0..120u32)
        .map(|index| {
            let from = index * 3;
            (from, from + 1, index % 7)
        })
        .collect();
    let meta = finish_ranges(source_builder(&paths.source), &ranges);
    swap_first_two_records(&paths.source, meta);
    let budget = RecoveryBudget {
        max_heap_bytes: 256,
        max_output_pages: 20_000,
        max_open_files: 4,
        max_scratch_bytes: 4096,
        max_scratch_files: 2,
        scratch_directory: Some(paths.scratch.clone()),
    };

    let result = construct(
        &File::open(&paths.source).unwrap(),
        meta,
        output_builder(&paths.output),
        &budget,
        &CancellationToken::new(),
        &mut |_: &RecoveryUnknownEnvelope| Ok(RecoverySinkControl::Continue),
    )
    .unwrap();
    drop(result.finished.file);

    let cleanup = result.scratch.expect("external sort records its attempt");
    assert!(cleanup.clean());
    assert_ne!(cleanup.attempt_id, [0; 16]);
    assert_eq!(result.report.ranges.accepted, ranges.len() as u64);
    assert_eq!(output_ranges(&paths.output), ranges);
    assert_eq!(fs::read_dir(&paths.scratch).unwrap().count(), 0);
    validate_clean(&paths.output);
}

#[test]
fn complete_metadata_is_preserved_and_damaged_metadata_is_omitted() {
    let clean = Paths::new("metadata-clean");
    let payload = br#"{"source":"recovery"}"#;
    let mut source = source_builder(&clean.source);
    source.push_direct_v4(Ipv4Key(10), Ipv4Key(19), 7).unwrap();
    source.write_metadata(payload).unwrap();
    let finished = source.finish().unwrap();
    let meta = finished.meta;
    drop(finished.file);

    let DirectConstruction {
        finished, report, ..
    } = construct(
        &File::open(&clean.source).unwrap(),
        meta,
        output_builder(&clean.output),
        &budget(2 * 1024 * 1024),
        &CancellationToken::new(),
        &mut |_: &RecoveryUnknownEnvelope| Ok(RecoverySinkControl::Continue),
    )
    .unwrap();
    drop(finished.file);
    assert_eq!(report.metadata_chunks.accepted, 1);
    assert_eq!(
        ImmutableReader::open(&clean.output)
            .unwrap()
            .metadata_json()
            .unwrap()
            .as_deref(),
        Some(payload.as_slice())
    );

    let damaged = Paths::new("metadata-damaged");
    fs::copy(&clean.source, &damaged.source).unwrap();
    corrupt_crc(
        &OpenOptions::new()
            .read(true)
            .write(true)
            .open(&damaged.source)
            .unwrap(),
        meta.metadata_root,
    );
    let mut unknown = Vec::new();
    let DirectConstruction {
        finished, report, ..
    } = construct(
        &File::open(&damaged.source).unwrap(),
        meta,
        output_builder(&damaged.output),
        &budget(2 * 1024 * 1024),
        &CancellationToken::new(),
        &mut |envelope: &RecoveryUnknownEnvelope| {
            unknown.push(*envelope);
            Ok(RecoverySinkControl::Continue)
        },
    )
    .unwrap();
    drop(finished.file);
    assert_eq!(report.metadata_chunks.rejected, 1);
    assert!(!report.has_unbounded_unknown);
    assert!(unknown.iter().any(|item| {
        item.object == crate::validation::ValidationObject::Metadata
            && item.reason == ValidationReason::PageCrcMismatch
    }));
    assert!(ImmutableReader::open(&damaged.output)
        .unwrap()
        .metadata_json()
        .unwrap()
        .is_none());
    validate_clean(&damaged.output);
}

pub(super) fn source_builder(path: &Path) -> Builder {
    Builder::new(
        create(path),
        OutputSpec {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [1; 16],
            transaction_id: 7,
            commit_nonce: [2; 16],
            feed_index_limit: 0,
        },
        output_budget(),
    )
    .unwrap()
}

pub(super) fn output_builder(path: &Path) -> Builder {
    Builder::new(
        create(path),
        OutputSpec {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [3; 16],
            transaction_id: 1,
            commit_nonce: [4; 16],
            feed_index_limit: 0,
        },
        output_budget(),
    )
    .unwrap()
}

pub(super) fn finish_ranges(mut builder: Builder, ranges: &[(u32, u32, u32)]) -> MetaV4 {
    for &(from, to, value) in ranges {
        builder
            .push_direct_v4(Ipv4Key(from), Ipv4Key(to), value)
            .unwrap();
    }
    let finished = builder.finish().unwrap();
    let meta = finished.meta;
    drop(finished.file);
    meta
}

fn first_child(file: &File, meta: MetaV4) -> u32 {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, meta.range_root, meta.page_count, &mut page).unwrap();
    let header = range_tree::parse_header::<Ipv4Key>(&page, meta.txn_id, None).unwrap();
    assert!(header.level > 0);
    range_tree::branch_child::<Ipv4Key>(&page, &header, 0).unwrap()
}

fn corrupt_crc(file: &File, page_number: u32) {
    let mut page = [0; PAGE_SIZE];
    file_io::read_exact_at(file, &mut page, u64::from(page_number) * PAGE_SIZE as u64).unwrap();
    page[100] ^= 0x5a;
    file_io::write_exact_at(file, &page, u64::from(page_number) * PAGE_SIZE as u64).unwrap();
}

pub(super) fn rewrite_second_start(path: &Path, meta: MetaV4, from: u32) {
    edit_root_leaf(path, meta, |page, header| {
        let start = usize::from(u16::from_le_bytes(page[34..36].try_into().unwrap()));
        page[start..start + 4].copy_from_slice(&from.to_le_bytes());
        assert!(header.item_count >= 2);
    });
}

pub(super) fn swap_first_two_records(path: &Path, meta: MetaV4) {
    edit_root_leaf(path, meta, |page, header| {
        assert!(header.item_count >= 2);
        let first = usize::from(u16::from_le_bytes(page[32..34].try_into().unwrap()));
        let second = usize::from(u16::from_le_bytes(page[34..36].try_into().unwrap()));
        let mut saved = [0; 12];
        saved.copy_from_slice(&page[first..first + 12]);
        let other: [u8; 12] = page[second..second + 12].try_into().unwrap();
        page[first..first + 12].copy_from_slice(&other);
        page[second..second + 12].copy_from_slice(&saved);
    });
}

fn edit_root_leaf(path: &Path, meta: MetaV4, edit: impl FnOnce(&mut [u8; PAGE_SIZE], Header)) {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .open(path)
        .unwrap();
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(&file, meta.range_root, meta.page_count, &mut page).unwrap();
    let header = range_tree::parse_header::<Ipv4Key>(&page, meta.txn_id, None).unwrap();
    assert_eq!(header.level, 0);
    edit(&mut page, header);
    let checksum = crc32c::crc32c_with_zeroed(&page, 28, 4).unwrap();
    slotted_page::put_u32(&mut page, 28, checksum);
    file_io::write_exact_at(&file, &page, u64::from(meta.range_root) * PAGE_SIZE as u64).unwrap();
}

fn output_ranges(path: &Path) -> Vec<(u32, u32, u32)> {
    let reader = ImmutableReader::open(path).unwrap();
    let mut cursor = reader.direct_cursor_v4(RangeDirection::Forward).unwrap();
    let mut ranges = Vec::new();
    while let Some(range) = cursor.next_range().unwrap() {
        ranges.push((range.from.0, range.to.0, range.value));
    }
    ranges
}

fn validate_clean(path: &Path) {
    let result = validate(
        path,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(2 * 1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |_: &crate::validation::ValidationFinding| Ok(ValidationSinkControl::Continue),
    )
    .unwrap();
    assert!(result.valid, "{:?}", result.progress);
}

fn create(path: &Path) -> File {
    OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .open(path)
        .unwrap()
}

fn budget(max_heap_bytes: u64) -> RecoveryBudget {
    RecoveryBudget::heap_only(max_heap_bytes, 20_000, 2)
}

fn output_budget() -> OutputBudget {
    OutputBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_output_pages: 20_000,
    }
}
