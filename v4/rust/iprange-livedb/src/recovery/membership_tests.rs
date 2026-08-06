use std::fs::{self, File, OpenOptions};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;
use crate::contract::{u16_le, ValueTag, PAGE_SIZE};
use crate::database::ImmutableReader;
use crate::feed::FeedName;
use crate::immutable_output::{MembershipWords, OutputBudget, OutputSpec};
use crate::membership_tree;
use crate::recovery::{RecoverySinkControl, RecoveryUnknownEnvelope};
use crate::validation::{
    validate, ValidationBudget, ValidationMode, ValidationObject, ValidationReason,
    ValidationSinkControl,
};
use crate::{crc32c, file_io, range_tree, slotted_page};

struct Paths {
    source: PathBuf,
    output: PathBuf,
    scratch: PathBuf,
}

impl Paths {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = std::env::temp_dir().join(format!(
            "iprange-v4-membership-recovery-{label}-{}-{unique}",
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

struct Words(Vec<u64>);

impl MembershipWords for Words {
    fn word_count(&self) -> u32 {
        self.0.len() as u32
    }

    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        let start = start as usize;
        output.copy_from_slice(&self.0[start..start + output.len()]);
        Ok(())
    }
}

#[test]
fn clean_membership_recovery_preserves_feeds_bitmaps_metadata_and_high_water() {
    let paths = Paths::new("clean");
    let mut source = source_builder(&paths.source, 32_002);
    add_wide_feeds(&mut source);
    let wide = wide_words();
    let alpha = Words(vec![1 << 3]);
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &wide)
        .unwrap();
    source
        .push_membership_v4(Ipv4Key(20), Ipv4Key(29), &alpha)
        .unwrap();
    source
        .push_membership_v4(Ipv4Key(40), Ipv4Key(49), &wide)
        .unwrap();
    source
        .write_metadata_with_budget(br#"{"kind":"membership"}"#, 8 * 1024 * 1024)
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    drop(finished.file);

    let (report, unknown) = recover(&paths, meta);
    assert!(unknown.is_empty());
    assert_eq!(report.catalog_entries.examined, 6);
    assert_eq!(report.catalog_entries.accepted, 6);
    assert_eq!(report.membership_entries.examined, 2);
    assert_eq!(report.membership_entries.accepted, 2);
    assert_eq!(report.ranges.accepted, 3);

    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert_eq!(reader.info().active_feed_count, 3);
    assert_eq!(reader.info().range_record_count, 3);
    assert_eq!(reader.lookup_feed("alpha").unwrap().unwrap().index, 3);
    assert_eq!(reader.lookup_feed("middle").unwrap().unwrap().index, 31_999);
    assert_eq!(reader.lookup_feed("omega").unwrap().unwrap().index, 32_001);
    let membership = reader.lookup_membership_v4(Ipv4Key(5)).unwrap().unwrap();
    assert!(membership.contains_index(3).unwrap());
    assert!(membership.contains_index(31_999).unwrap());
    assert!(membership.contains_index(32_001).unwrap());
    assert_eq!(
        reader.metadata_json().unwrap().unwrap(),
        br#"{"kind":"membership"}"#
    );
    validate_clean(&paths.output);
}

#[test]
fn either_catalog_tree_is_sufficient_for_equal_conflict_free_pairs() {
    let paths = Paths::new("one-catalog");
    let mut source = source_builder(&paths.source, 64);
    source.push_feed(FeedName::new("a").unwrap(), 1).unwrap();
    source.push_feed(FeedName::new("b").unwrap(), 5).unwrap();
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &Words(vec![1 << 1]))
        .unwrap();
    source
        .push_membership_v4(Ipv4Key(20), Ipv4Key(29), &Words(vec![1 << 5]))
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    corrupt_crc(&finished.file, meta.catalog_name_root);
    drop(finished.file);

    let (report, unknown) = recover(&paths, meta);
    assert_eq!(report.pages.rejected, 1);
    assert_eq!(report.catalog_entries.examined, 2);
    assert_eq!(report.catalog_entries.accepted, 2);
    assert!(unknown.iter().any(|item| {
        item.reason == ValidationReason::PageCrcMismatch
            && item.object == ValidationObject::CatalogNameTree
    }));
    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert_eq!(reader.lookup_feed("a").unwrap().unwrap().index, 1);
    assert_eq!(reader.lookup_feed("b").unwrap().unwrap().index, 5);
    assert!(reader
        .lookup_membership_v4(Ipv4Key(5))
        .unwrap()
        .unwrap()
        .contains_index(1)
        .unwrap());
    validate_clean(&paths.output);
}

#[test]
fn catalog_conflicts_reject_only_dependent_memberships_and_ranges() {
    let paths = Paths::new("catalog-conflict");
    let mut source = source_builder(&paths.source, 64);
    source.push_feed(FeedName::new("a").unwrap(), 1).unwrap();
    source.push_feed(FeedName::new("b").unwrap(), 5).unwrap();
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &Words(vec![1 << 1]))
        .unwrap();
    source
        .push_membership_v4(Ipv4Key(20), Ipv4Key(29), &Words(vec![1 << 5]))
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    rewrite_name_index(&finished.file, meta, "a", 7);
    drop(finished.file);

    let (report, unknown) = recover(&paths, meta);
    assert_eq!(report.catalog_entries.examined, 4);
    assert_eq!(report.catalog_entries.accepted, 2);
    assert_eq!(report.catalog_entries.rejected, 2);
    assert_eq!(report.membership_entries.accepted, 1);
    assert_eq!(report.membership_entries.rejected, 1);
    assert_eq!(report.ranges.accepted, 1);
    assert_eq!(report.ranges.rejected, 1);
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::CatalogInvalid));
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::MembershipMissing));

    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert!(reader.lookup_feed("a").unwrap().is_none());
    assert_eq!(reader.lookup_feed("b").unwrap().unwrap().index, 5);
    assert!(reader.lookup_membership_v4(Ipv4Key(5)).unwrap().is_none());
    assert!(reader
        .lookup_membership_v4(Ipv4Key(25))
        .unwrap()
        .unwrap()
        .contains_index(5)
        .unwrap());
    validate_clean(&paths.output);
}

#[test]
fn damaged_blob_rejects_its_membership_and_known_range() {
    let paths = Paths::new("blob");
    let mut source = source_builder(&paths.source, 32_002);
    add_wide_feeds(&mut source);
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &wide_words())
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    let id = range_tree::lookup(&finished.file, &meta, Ipv4Key(5))
        .unwrap()
        .unwrap();
    let record = membership_tree::find(&finished.file, &meta, id)
        .unwrap()
        .unwrap();
    let membership_tree::Storage::Blob(root) = record.storage else {
        panic!("wide membership must use a blob");
    };
    corrupt_crc(&finished.file, root);
    drop(finished.file);

    let (report, unknown) = recover(&paths, meta);
    assert_eq!(report.membership_entries.rejected, 1);
    assert_eq!(report.ranges.rejected, 1);
    assert!(unknown.iter().any(|item| {
        item.reason == ValidationReason::PageCrcMismatch
            && item.object == ValidationObject::MembershipBlob
    }));
    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert_eq!(reader.info().active_feed_count, 3);
    assert_eq!(reader.info().range_record_count, 0);
    validate_clean(&paths.output);
}

#[cfg(any(unix, windows))]
#[test]
fn ordered_recovery_spills_all_tables_to_one_file() {
    let paths = Paths::new("table-scratch");
    let mut source = source_builder(&paths.source, 2048);
    for index in 1..=1000 {
        source
            .push_feed(FeedName::new(&format!("feed-{index:04}")).unwrap(), index)
            .unwrap();
    }
    let mut words = vec![0; 16];
    words[15] = 1 << 40;
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &Words(words))
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    drop(finished.file);

    let budget = RecoveryBudget {
        max_heap_bytes: 64 * 1024,
        max_output_pages: 100_000,
        max_open_files: 3,
        max_scratch_bytes: 2 * 1024 * 1024,
        max_scratch_files: 1,
        scratch_directory: Some(paths.scratch.clone()),
    };
    let source = File::open(&paths.source).unwrap();
    let result = construct(
        &source,
        meta,
        output_builder(&paths.output, meta),
        &budget,
        &CancellationToken::new(),
        &mut |_: &RecoveryUnknownEnvelope| Ok(RecoverySinkControl::Continue),
    )
    .unwrap();
    drop(result.finished.file);

    assert!(result.scratch.as_ref().is_some_and(|value| value.clean()));
    assert_eq!(result.report.catalog_entries.accepted, 2000);
    assert_eq!(fs::read_dir(&paths.scratch).unwrap().count(), 0);
    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert_eq!(reader.info().active_feed_count, 1000);
    assert_eq!(reader.lookup_feed("feed-0001").unwrap().unwrap().index, 1);
    assert_eq!(
        reader.lookup_feed("feed-1000").unwrap().unwrap().index,
        1000
    );
    assert!(reader
        .lookup_membership_v4(Ipv4Key(5))
        .unwrap()
        .unwrap()
        .contains_index(1000)
        .unwrap());
    validate_clean(&paths.output);
}

#[cfg(any(unix, windows))]
#[test]
fn table_scratch_budget_failure_removes_its_partial_file() {
    let paths = Paths::new("table-budget");
    let mut source = source_builder(&paths.source, 64);
    for index in 1..=20 {
        source
            .push_feed(FeedName::new(&format!("feed-{index:02}")).unwrap(), index)
            .unwrap();
    }
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &Words(vec![1 << 1]))
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    drop(finished.file);

    let budget = RecoveryBudget {
        max_heap_bytes: 4096,
        max_output_pages: 100_000,
        max_open_files: 3,
        max_scratch_bytes: 128,
        max_scratch_files: 1,
        scratch_directory: Some(paths.scratch.clone()),
    };
    let source = File::open(&paths.source).unwrap();
    let failure = construct(
        &source,
        meta,
        output_builder(&paths.output, meta),
        &budget,
        &CancellationToken::new(),
        &mut |_: &RecoveryUnknownEnvelope| Ok(RecoverySinkControl::Continue),
    )
    .unwrap_err();

    assert!(matches!(
        failure.cause,
        Error::BudgetExceeded("recovery scratch bytes")
    ));
    assert!(failure.scratch.as_ref().is_some_and(|value| value.clean()));
    assert_eq!(fs::read_dir(&paths.scratch).unwrap().count(), 0);
    drop(failure.builder.into_file());
}

fn source_builder(path: &Path, feed_index_limit: u64) -> Builder {
    builder(
        path,
        OutputSpec {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Membership,
            value_tag: ValueTag::new(b"feeds").unwrap(),
            database_id: [11; 16],
            transaction_id: 7,
            commit_nonce: [12; 16],
            feed_index_limit,
        },
    )
}

fn output_builder(path: &Path, source: MetaV4) -> Builder {
    builder(
        path,
        OutputSpec {
            address_family: source.address_family,
            value_kind: source.value_kind,
            value_tag: source.value_tag,
            database_id: [21; 16],
            transaction_id: 1,
            commit_nonce: [22; 16],
            feed_index_limit: source.feed_index_limit,
        },
    )
}

fn builder(path: &Path, spec: OutputSpec) -> Builder {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .open(path)
        .unwrap();
    Builder::new_owned(
        file,
        spec,
        OutputBudget {
            max_output_pages: 100_000,
        },
    )
    .unwrap()
}

fn recover(paths: &Paths, meta: MetaV4) -> (RecoveryReport, Vec<RecoveryUnknownEnvelope>) {
    let source = File::open(&paths.source).unwrap();
    let mut unknown = Vec::new();
    let result = construct(
        &source,
        meta,
        output_builder(&paths.output, meta),
        &RecoveryBudget::heap_only(8 * 1024 * 1024, 100_000, 2),
        &CancellationToken::new(),
        &mut |item: &RecoveryUnknownEnvelope| {
            unknown.push(*item);
            Ok(RecoverySinkControl::Continue)
        },
    )
    .unwrap();
    drop(result.finished.file);
    (result.report, unknown)
}

fn add_wide_feeds(output: &mut Builder) {
    for (name, index) in [("alpha", 3), ("middle", 31_999), ("omega", 32_001)] {
        output
            .push_feed(FeedName::new(name).unwrap(), index)
            .unwrap();
    }
}

fn wide_words() -> Words {
    let mut words = vec![0; 501];
    words[0] = 1 << 3;
    words[499] = 1 << 63;
    words[500] = 1 << 1;
    Words(words)
}

fn corrupt_crc(file: &File, page_number: u32) {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, page_number, u64::MAX, &mut page).unwrap();
    page[100] ^= 1;
    file_io::write_exact_at(file, &page, u64::from(page_number) * PAGE_SIZE as u64).unwrap();
}

fn rewrite_name_index(file: &File, meta: MetaV4, name: &str, index: u32) {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, meta.catalog_name_root, meta.page_count, &mut page).unwrap();
    let header = slotted_page::parse(
        &page,
        meta.txn_id,
        crate::feed_catalog::NAME_LEAF,
        0,
        Some(0),
    )
    .unwrap();
    for position in 0..header.item_count {
        let start = usize::from(u16_le(&page, slotted_page::HEADER_SIZE + position * 2));
        let length = usize::from(u16_le(&page, start));
        let entry = crate::feed_catalog::decode_entry(&page[start..start + length]).unwrap();
        if entry.name.as_str() == name {
            page[start + 4..start + 8].copy_from_slice(&index.to_le_bytes());
            stamp(&mut page);
            file_io::write_exact_at(
                file,
                &page,
                u64::from(meta.catalog_name_root) * PAGE_SIZE as u64,
            )
            .unwrap();
            return;
        }
    }
    panic!("feed name not found");
}

fn stamp(page: &mut [u8; PAGE_SIZE]) {
    let checksum = crc32c::crc32c_with_zeroed(page, 28, 4).unwrap();
    page[28..32].copy_from_slice(&checksum.to_le_bytes());
}

fn validate_clean(path: &Path) {
    let result = validate(
        path,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(16 * 1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |_finding: &crate::validation::ValidationFinding| Ok(ValidationSinkControl::Continue),
    )
    .unwrap();
    assert!(result.valid, "{:?}", result.progress);
}

#[path = "membership_adversarial_tests.rs"]
mod adversarial;
