#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::recovery::{
    inspect_recovery_candidates, recover_immutable, RecoveryBudget, RecoveryInspectionMode,
    RecoverySinkControl, RecoveryUnknownEnvelope,
};
use iprange_livedb::validation::{
    validate, ValidationBudget, ValidationMode, ValidationSinkControl,
};
use iprange_livedb::{
    create_live, snapshot_to, AddressFamily, CancellationToken, CommitDurability, Error, FeedName,
    ImmutableReader, Ipv4Key, LiveReader, LiveWriter, NetworkEnrichmentV1,
    NetworkEnrichmentV1Location, RangeDirection, SnapshotBudget, SnapshotPublicationPolicy,
    SnapshotSourceMode, StructureKind, TransactionBudget, ValueKind, ValueTag,
};

struct Files {
    main: PathBuf,
    other: PathBuf,
    snapshot: PathBuf,
    recovered: PathBuf,
}

impl Files {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = std::env::temp_dir().join(format!(
            "iprange-v4-structured-{}-{unique}",
            std::process::id()
        ));
        Self {
            main: base.with_extension("live"),
            other: base.with_extension("other"),
            snapshot: base.with_extension("snapshot"),
            recovered: base.with_extension("recovered"),
        }
    }

    fn sidecar(&self) -> PathBuf {
        sidecar(&self.main)
    }
}

fn sidecar(path: &std::path::Path) -> PathBuf {
    let mut name = path.file_name().unwrap().to_os_string();
    name.push(".readers");
    path.with_file_name(name)
}

impl Drop for Files {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
        let _ = fs::remove_file(&self.other);
        let _ = fs::remove_file(sidecar(&self.other));
        let _ = fs::remove_file(&self.snapshot);
        let _ = fs::remove_file(&self.recovered);
    }
}

#[test]
fn structure_references_are_operation_and_database_bound() {
    let files = Files::new();
    create_structured(&files.main);
    create_structured(&files.other);
    let cancellation = CancellationToken::new();

    let mut first = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();
    let stale = {
        let mut transaction = first.begin_structured_transaction(&cancellation).unwrap();
        let structure = transaction
            .intern_network_enrichment_v1(
                NetworkEnrichmentV1 {
                    asn: 64512,
                    ..NetworkEnrichmentV1::default()
                },
                None,
            )
            .unwrap();
        transaction.abort().unwrap();
        structure
    };

    let mut transaction = first.begin_structured_transaction(&cancellation).unwrap();
    assert!(matches!(
        transaction.assign_v4(Ipv4Key(0), Ipv4Key(9), stale),
        Err(Error::StaleReference)
    ));
    transaction.abort().unwrap();

    let mut second = LiveWriter::open(&files.other, budget(), &cancellation).unwrap();
    let mut transaction = second.begin_structured_transaction(&cancellation).unwrap();
    assert!(matches!(
        transaction.assign_v4(Ipv4Key(0), Ipv4Key(9), stale),
        Err(Error::ForeignReference)
    ));
    transaction.abort().unwrap();
    first.close().unwrap();
    second.close().unwrap();
}

#[test]
fn abort_deduplication_release_and_reuse_preserve_a_clean_graph() {
    let files = Files::new();
    create_structured(&files.main);
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();

    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    let abandoned = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64512,
                ..NetworkEnrichmentV1::default()
            },
            None,
        )
        .unwrap();
    transaction
        .assign_v4(Ipv4Key(0), Ipv4Key(9), abandoned)
        .unwrap();
    transaction.abort().unwrap();

    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    assert!(reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(5))
        .unwrap()
        .is_none());
    reader.close().unwrap();

    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    let value = NetworkEnrichmentV1 {
        asn: 64513,
        ..NetworkEnrichmentV1::default()
    };
    let first = transaction
        .intern_network_enrichment_v1(value, None)
        .unwrap();
    let duplicate = transaction
        .intern_network_enrichment_v1(value, None)
        .unwrap();
    assert_eq!(first, duplicate);
    transaction
        .assign_v4(Ipv4Key(0), Ipv4Key(9), first)
        .unwrap();
    transaction
        .assign_v4(Ipv4Key(20), Ipv4Key(29), duplicate)
        .unwrap();
    transaction.commit().unwrap();

    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    transaction.clear_v4(Ipv4Key(0), Ipv4Key(29)).unwrap();
    transaction.commit().unwrap();

    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    let replacement = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64514,
                ..NetworkEnrichmentV1::default()
            },
            None,
        )
        .unwrap();
    transaction
        .assign_v4(Ipv4Key(40), Ipv4Key(49), replacement)
        .unwrap();
    transaction.commit().unwrap();

    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    assert!(reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(5))
        .unwrap()
        .is_none());
    assert_eq!(
        reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(45))
            .unwrap()
            .unwrap()
            .value()
            .asn,
        64514
    );
    reader.close().unwrap();
    writer.close().unwrap();
    validate_clean(&files.main);
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

fn create_structured(path: &std::path::Path) {
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Structured,
        StructureKind::NetworkEnrichmentV1,
        ValueTag::new(b"enrichment").unwrap(),
        64,
        &CancellationToken::new(),
    )
    .unwrap();
}

#[test]
fn typed_structure_and_lazy_membership_round_trip() {
    let files = Files::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Structured,
        StructureKind::NetworkEnrichmentV1,
        ValueTag::new(b"enrichment").unwrap(),
        4,
        &CancellationToken::new(),
    )
    .unwrap();

    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();
    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    let threat = transaction
        .ensure_feed(FeedName::new("threat-a").unwrap())
        .unwrap();
    let threat_index = threat.index();
    let empty = transaction.empty_membership().unwrap();
    let membership = transaction.add_feed(empty, threat).unwrap();
    let broad = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64512,
                country_id: 1,
                state_id: 2,
                city_id: 3,
                location: Some(NetworkEnrichmentV1Location {
                    latitude_microdegrees: 37_983_810,
                    longitude_microdegrees: 23_727_539,
                }),
            },
            Some(membership),
        )
        .unwrap();
    let narrow = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64513,
                ..NetworkEnrichmentV1::default()
            },
            None,
        )
        .unwrap();
    transaction
        .assign_v4(Ipv4Key(0), Ipv4Key(100), broad)
        .unwrap();
    transaction
        .assign_v4(Ipv4Key(20), Ipv4Key(30), narrow)
        .unwrap();
    transaction.clear_v4(Ipv4Key(25), Ipv4Key(27)).unwrap();
    assert_eq!(
        transaction.commit().unwrap().durability,
        CommitDurability::Committed
    );

    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    let info = reader.info().unwrap();
    assert_eq!(info.value_kind, ValueKind::Structured);
    assert_eq!(info.structure_kind, StructureKind::NetworkEnrichmentV1);

    let broad = reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(10))
        .unwrap()
        .unwrap();
    assert_eq!(broad.value().asn, 64512);
    assert!(broad
        .threat_membership()
        .unwrap()
        .unwrap()
        .contains_index(threat_index)
        .unwrap());
    let narrow = reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(24))
        .unwrap()
        .unwrap();
    assert_eq!(narrow.value().asn, 64513);
    assert!(narrow.threat_membership().unwrap().is_none());
    assert!(reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(26))
        .unwrap()
        .is_none());
    assert_eq!(
        reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(31))
            .unwrap()
            .unwrap()
            .value()
            .asn,
        64512
    );

    let mut threat_ranges = reader
        .feed_range_cursor_v4("threat-a", RangeDirection::Forward)
        .unwrap();
    let first = threat_ranges.next_range().unwrap().unwrap();
    assert_eq!((first.from, first.to), (Ipv4Key(0), Ipv4Key(19)));
    let second = threat_ranges.next_range().unwrap().unwrap();
    assert_eq!((second.from, second.to), (Ipv4Key(31), Ipv4Key(100)));
    assert!(threat_ranges.next_range().unwrap().is_none());

    let mut structures = reader
        .network_enrichment_v1_cursor_v4(RangeDirection::Forward)
        .unwrap();
    let first = structures.next_range().unwrap().unwrap();
    assert_eq!((first.from, first.to), (Ipv4Key(0), Ipv4Key(19)));
    assert_eq!(first.value.value().asn, 64512);

    reader.close().unwrap();

    snapshot_to(
        &files.main,
        SnapshotSourceMode::Live,
        &files.snapshot,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(4 * 1024 * 1024, 10_000, 3),
        &cancellation,
    )
    .unwrap();
    assert_enrichment(&files.snapshot, threat_index);

    let inspection = inspect_recovery_candidates(
        &files.snapshot,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(0, 1),
        &cancellation,
    )
    .unwrap();
    let recovered = recover_immutable(
        &files.snapshot,
        *inspection.candidate(0).unwrap(),
        &files.recovered,
        &RecoveryBudget::heap_only(8 * 1024 * 1024, 10_000, 2),
        &mut continue_unknown,
        &cancellation,
    )
    .unwrap();
    assert_eq!(recovered.report.structure_entries.accepted, 2);
    assert_eq!(recovered.report.ranges.accepted, 4);
    assert_enrichment(&files.recovered, threat_index);

    let mut transaction = writer.begin_structured_transaction(&cancellation).unwrap();
    let threat = transaction
        .lookup_feed(FeedName::new("threat-a").unwrap())
        .unwrap()
        .unwrap();
    let empty = transaction.empty_membership().unwrap();
    let membership = transaction.add_feed(empty, threat).unwrap();
    let stale_structure = transaction
        .intern_network_enrichment_v1(
            NetworkEnrichmentV1 {
                asn: 64514,
                ..NetworkEnrichmentV1::default()
            },
            Some(membership),
        )
        .unwrap();
    transaction.delete_feed(threat).unwrap();
    assert!(matches!(
        transaction.assign_v4(Ipv4Key(200), Ipv4Key(200), stale_structure),
        Err(Error::StaleReference)
    ));
    assert_eq!(
        transaction.commit().unwrap().durability,
        CommitDurability::Committed
    );

    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    assert!(reader.lookup_feed("threat-a").unwrap().is_none());
    let broad = reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(10))
        .unwrap()
        .unwrap();
    assert_eq!(broad.value().asn, 64512);
    assert!(broad.threat_membership().unwrap().is_none());
    reader.close().unwrap();
    writer.close().unwrap();

    let mut findings = Vec::new();
    let validated = validate(
        &files.main,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(8 * 1024 * 1024, 2),
        &cancellation,
        &mut |finding: &_| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(validated.valid, "{findings:?}");
}

fn continue_unknown(
    _: &RecoveryUnknownEnvelope,
) -> std::result::Result<RecoverySinkControl, iprange_livedb::Error> {
    Ok(RecoverySinkControl::Continue)
}

fn assert_enrichment(path: &std::path::Path, threat_index: u32) {
    let reader = ImmutableReader::open(path).unwrap();
    let broad = reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(10))
        .unwrap()
        .unwrap();
    assert_eq!(broad.value().asn, 64512);
    assert!(broad
        .threat_membership()
        .unwrap()
        .unwrap()
        .contains_index(threat_index)
        .unwrap());
    assert_eq!(
        reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(24))
            .unwrap()
            .unwrap()
            .value()
            .asn,
        64513
    );
    assert!(reader
        .lookup_network_enrichment_v1_v4(Ipv4Key(26))
        .unwrap()
        .is_none());
}

fn validate_clean(path: &std::path::Path) {
    let mut findings = Vec::new();
    let validated = validate(
        path,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(8 * 1024 * 1024, 2),
        &CancellationToken::new(),
        &mut |finding: &_| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(validated.valid, "{findings:?}");
}
