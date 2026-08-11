use std::fs::{self, File, OpenOptions};
use std::path::{Path, PathBuf};

use super::*;
use crate::contract::{AddressFamily, StructureKind, ValueTag, PAGE_SIZE};
use crate::crc32c;
use crate::database::ImmutableReader;
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::immutable_output::{MembershipWords, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::mapping::test_support as file_io;
use crate::mapping::Mapping;
use crate::range_tree;
use crate::recovery::{RecoveryReport, RecoverySinkControl, RecoveryUnknownEnvelope};
use crate::structured_value::codec;
use crate::structured_value::{table, NetworkEnrichmentV1, NetworkEnrichmentV1Codec};
use crate::validation::{
    validate, ValidationBudget, ValidationMode, ValidationObject, ValidationReason,
    ValidationSinkControl,
};

struct Paths {
    source: PathBuf,
    output: PathBuf,
}

impl Paths {
    fn new(label: &str) -> Self {
        let base = crate::test_support_tests::unique_path(&format!(
            "iprange-v4-structured-recovery-{label}"
        ));
        Self {
            source: base.with_extension("source"),
            output: base.with_extension("output"),
        }
    }
}

impl Drop for Paths {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.source);
        let _ = fs::remove_file(&self.output);
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
fn damaged_structure_rejects_only_its_dependent_range() {
    let paths = Paths::new("structure-digest");
    let mut source = source_builder(&paths.source);
    source
        .push_network_enrichment_v1_v4::<Words>(Ipv4Key(0), Ipv4Key(9), enrichment(64512), None)
        .unwrap();
    source
        .push_network_enrichment_v1_v4::<Words>(Ipv4Key(20), Ipv4Key(29), enrichment(64513), None)
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    let damaged_id = range_tree::lookup(&finished.mapping, &meta, Ipv4Key(5))
        .unwrap()
        .unwrap();
    rewrite_structure_record(&finished.file, meta, damaged_id, |record| record[16] ^= 1);
    drop(finished);

    let mut findings = Vec::new();
    let validation = validate(
        &paths.source,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(16 * 1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |finding: &crate::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(!validation.valid);
    assert!(findings.iter().any(|finding| {
        finding.reason == ValidationReason::StructureHashInvalid
            && finding.object == ValidationObject::StructureDictionary
    }));

    let (report, unknown) = recover(&paths, meta, RecoverySinkControl::Continue).unwrap();
    assert_eq!(report.structure_entries.examined, 2);
    assert_eq!(report.structure_entries.accepted, 1);
    assert_eq!(report.structure_entries.rejected, 1);
    assert_eq!(report.ranges.accepted, 1);
    assert_eq!(report.ranges.rejected, 1);
    assert!(unknown.iter().any(|item| {
        item.reason == ValidationReason::StructureHashInvalid
            && item.object == ValidationObject::StructureDictionary
    }));

    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert!(reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(5))
        .unwrap()
        .is_none());
    assert_eq!(
        reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(25))
            .unwrap()
            .unwrap()
            .value()
            .asn,
        64513
    );
    validate_clean(&paths.output);
}

#[test]
fn missing_membership_rejects_only_dependent_structure() {
    let paths = Paths::new("membership");
    let mut source = source_builder(&paths.source);
    source
        .push_feed(FeedName::new("threat").unwrap(), 1)
        .unwrap();
    source
        .push_network_enrichment_v1_v4(
            Ipv4Key(0),
            Ipv4Key(9),
            enrichment(64512),
            Some(&Words(vec![1 << 1])),
        )
        .unwrap();
    source
        .push_network_enrichment_v1_v4::<Words>(Ipv4Key(20), Ipv4Key(29), enrichment(64513), None)
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    corrupt_crc(&finished.file, meta.membership_id_root);
    drop(finished);

    let (report, unknown) = recover(&paths, meta, RecoverySinkControl::Continue).unwrap();
    assert_eq!(report.structure_entries.accepted, 1);
    assert_eq!(report.structure_entries.rejected, 1);
    assert_eq!(report.ranges.accepted, 1);
    assert_eq!(report.ranges.rejected, 1);
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::StructureMembershipInvalid));

    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert!(reader.lookup_feed("threat").unwrap().is_some());
    assert!(reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(5))
        .unwrap()
        .is_none());
    assert_eq!(
        reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(25))
            .unwrap()
            .unwrap()
            .value()
            .asn,
        64513
    );
    validate_clean(&paths.output);
}

#[test]
fn stopping_at_structured_damage_aborts_construction() {
    let paths = Paths::new("stop");
    let mut source = source_builder(&paths.source);
    source
        .push_network_enrichment_v1_v4::<Words>(Ipv4Key(0), Ipv4Key(9), enrichment(64512), None)
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    let id = range_tree::lookup(&finished.mapping, &meta, Ipv4Key(5))
        .unwrap()
        .unwrap();
    rewrite_structure_record(&finished.file, meta, id, |record| record[16] ^= 1);
    drop(finished);

    let failure = recover(&paths, meta, RecoverySinkControl::Stop).unwrap_err();
    assert!(matches!(failure, Error::StoppedBySink));
}

#[test]
fn invalid_structure_branch_pointer_is_reported_and_best_effort_recovers_other_leaves() {
    let paths = Paths::new("branch-pointer");
    let mut source = source_builder(&paths.source);
    for index in 0..51u32 {
        let from = Ipv4Key(index * 2);
        source
            .push_network_enrichment_v1_v4::<Words>(
                from,
                Ipv4Key(from.0 + 1),
                enrichment(64_000 + index),
                None,
            )
            .unwrap();
    }
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    rewrite_structure_branch_child(&finished.file, meta, 1, meta.page_count as u32 + 10);
    drop(finished);

    let mut findings = Vec::new();
    let validation = validate(
        &paths.source,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(16 * 1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |finding: &crate::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(!validation.valid);
    assert!(findings.iter().any(|finding| {
        finding.reason == ValidationReason::PageOutOfBounds
            && finding.object == ValidationObject::StructureDictionary
    }));

    let (report, unknown) = recover(&paths, meta, RecoverySinkControl::Continue).unwrap();
    assert_eq!(report.structure_entries.accepted, 49);
    assert_eq!(report.ranges.accepted, 49);
    assert_eq!(report.ranges.rejected, 2);
    assert!(unknown.iter().any(|item| {
        item.reason == ValidationReason::PageOutOfBounds
            && item.object == ValidationObject::StructureDictionary
    }));

    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert_eq!(
        reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(1))
            .unwrap()
            .unwrap()
            .value()
            .asn,
        64_000
    );
    assert!(reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(101))
        .unwrap()
        .is_none());
    validate_clean(&paths.output);
}

fn enrichment(asn: u32) -> NetworkEnrichmentV1 {
    NetworkEnrichmentV1 {
        asn,
        ..NetworkEnrichmentV1::default()
    }
}

fn source_builder(path: &Path) -> Builder {
    builder(
        path,
        OutputSpec {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Structured,
            structure_kind: StructureKind::NetworkEnrichmentV1,
            value_tag: ValueTag::new(b"enrichment").unwrap(),
            database_id: [11; 16],
            transaction_id: 7,
            commit_nonce: [12; 16],
            feed_index_limit: 64,
        },
    )
}

fn output_builder(path: &Path, source: MetaV4) -> Builder {
    builder(
        path,
        OutputSpec {
            address_family: source.address_family,
            value_kind: source.value_kind,
            structure_kind: StructureKind::NetworkEnrichmentV1,
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

fn recover(
    paths: &Paths,
    meta: MetaV4,
    control: RecoverySinkControl,
) -> std::result::Result<(RecoveryReport, Vec<RecoveryUnknownEnvelope>), Error> {
    let source = File::open(&paths.source).unwrap();
    let source = Mapping::read_only(source, meta.page_count * PAGE_SIZE as u64).unwrap();
    let mut unknown = Vec::new();
    let result = construct(
        &source,
        meta,
        output_builder(&paths.output, meta),
        &RecoveryBudget::heap_only(8 * 1024 * 1024, 100_000, 2),
        &CancellationToken::new(),
        &mut |item: &RecoveryUnknownEnvelope| {
            unknown.push(*item);
            Ok(control)
        },
    );
    match result {
        Ok(result) => {
            drop(result.finished.file);
            Ok((result.report, unknown))
        }
        Err(failure) => {
            drop(failure.builder.into_file());
            Err(failure.cause)
        }
    }
}

fn rewrite_structure_record(file: &File, meta: MetaV4, id: u32, edit: impl FnOnce(&mut [u8])) {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, meta.structure_id_root, meta.page_count, &mut page).unwrap();
    table::parse::<NetworkEnrichmentV1Codec, _>(&page, meta.txn_id, Some(0)).unwrap();
    let size = codec::record_size::<NetworkEnrichmentV1Codec>();
    let start = crate::page_header::SIZE
        + id as usize % table::leaf_slots::<NetworkEnrichmentV1Codec>() * size;
    let end = start + size;
    assert_eq!(
        codec::decode_record::<NetworkEnrichmentV1Codec, _>(&page[start..end])
            .unwrap()
            .id,
        id
    );
    edit(&mut page[start..end]);
    stamp(&mut page);
    file_io::write_exact_at(
        file,
        &page,
        u64::from(meta.structure_id_root) * PAGE_SIZE as u64,
    )
    .unwrap();
}

fn rewrite_structure_branch_child(file: &File, meta: MetaV4, index: usize, child: u32) {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, meta.structure_id_root, meta.page_count, &mut page).unwrap();
    table::parse::<NetworkEnrichmentV1Codec, _>(&page, meta.txn_id, Some(1)).unwrap();
    let offset = crate::page_header::SIZE + index * 4;
    assert_ne!(
        table::raw_branch_child(&page, index).unwrap(),
        0,
        "the fixture must populate the corrupted branch"
    );
    page[offset..offset + 4].copy_from_slice(&child.to_le_bytes());
    stamp(&mut page);
    file_io::write_exact_at(
        file,
        &page,
        u64::from(meta.structure_id_root) * PAGE_SIZE as u64,
    )
    .unwrap();
}

fn corrupt_crc(file: &File, page_number: u32) {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, page_number, u64::MAX, &mut page).unwrap();
    page[100] ^= 1;
    file_io::write_exact_at(file, &page, u64::from(page_number) * PAGE_SIZE as u64).unwrap();
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
