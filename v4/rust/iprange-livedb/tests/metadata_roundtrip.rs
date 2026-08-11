#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::recovery::{
    inspect_recovery_candidates, recover_immutable, RecoveryBudget, RecoveryInspectionMode,
    RecoverySinkControl, RecoveryUnknownEnvelope,
};
use iprange_livedb::validation::{
    validate, ValidationBudget, ValidationFinding, ValidationMode, ValidationSinkControl,
};
use iprange_livedb::{
    create_live, snapshot_to, AddressFamily, CancellationToken, CommitDurability, Error,
    ImmutableReader, Ipv4Key, LiveReader, LiveWriter, ReclaimResult, SnapshotBudget,
    SnapshotPublicationPolicy, SnapshotSourceMode, TransactionBudget, ValueKind, ValueTag,
    MAX_METADATA_UNCOMPRESSED,
};

struct TestPair {
    main: PathBuf,
    snapshot: PathBuf,
    recovered: PathBuf,
}

impl TestPair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = std::env::temp_dir().join(format!(
            "iprange-v4-metadata-{label}-{}-{unique}",
            std::process::id()
        ));
        Self {
            main: base.with_extension("live"),
            snapshot: base.with_extension("snapshot"),
            recovered: base.with_extension("recovered"),
        }
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.main.with_file_name(name)
    }
}

impl Drop for TestPair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
        let _ = fs::remove_file(&self.snapshot);
        let _ = fs::remove_file(&self.recovered);
    }
}

fn budget(max_heap_bytes: u64) -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

fn pseudo_random(length: usize) -> Vec<u8> {
    let mut state = 0x52dce729u32;
    (0..length)
        .map(|_| {
            state ^= state << 13;
            state ^= state >> 17;
            state ^= state << 5;
            state as u8
        })
        .collect()
}

#[test]
fn maximum_metadata_uses_the_exact_minimum_heap_budget() {
    const LIMIT: usize = 20 * 1024 * 1024;
    const STORED_BOUND: u64 = 20_973_131;

    let files = TestPair::new("maximum");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        iprange_livedb::StructureKind::None,
        ValueTag::FIRST_SEEN,
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let payload = pseudo_random(LIMIT);
    let mut writer =
        LiveWriter::open(&files.main, budget(STORED_BOUND), &CancellationToken::new()).unwrap();
    let cancellation = iprange_livedb::CancellationToken::new();
    assert!(writer.set_metadata_json(&payload, &cancellation).unwrap());
    assert_eq!(
        writer.commit(&CancellationToken::new()).unwrap().durability,
        CommitDurability::Committed
    );

    let mut pinned = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(pinned.metadata_json_len().unwrap(), Some(LIMIT as u64));
    assert_eq!(
        pinned.metadata_json().unwrap().as_deref(),
        Some(&payload[..])
    );
    let validation = validate(
        &files.main,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(64 * 1024 * 1024, 2),
        &CancellationToken::new(),
        &mut continue_validation,
    )
    .unwrap();
    assert!(validation.valid);

    snapshot_to(
        &files.main,
        SnapshotSourceMode::Live,
        &files.snapshot,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(64 * 1024 * 1024, 20_000, 3),
        &CancellationToken::new(),
    )
    .unwrap();
    let snapshot = ImmutableReader::open(&files.snapshot).unwrap();
    assert_eq!(
        snapshot.metadata_json().unwrap().as_deref(),
        Some(&payload[..])
    );
    drop(snapshot);

    let inspection = inspect_recovery_candidates(
        &files.snapshot,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    recover_immutable(
        &files.snapshot,
        *inspection.candidate(0).unwrap(),
        &files.recovered,
        &RecoveryBudget::heap_only(64 * 1024 * 1024, 20_000, 2),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap();
    let recovered = ImmutableReader::open(&files.recovered).unwrap();
    assert_eq!(
        recovered.metadata_json().unwrap().as_deref(),
        Some(&payload[..])
    );
    drop(recovered);

    assert!(writer.clear_metadata_json(&cancellation).unwrap());
    assert_eq!(
        writer.commit(&CancellationToken::new()).unwrap().durability,
        CommitDurability::Committed
    );
    assert_eq!(
        pinned.metadata_json().unwrap().as_deref(),
        Some(&payload[..])
    );
    let mut current = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(current.metadata_json().unwrap(), None);
    pinned.close().unwrap();
    current.close().unwrap();

    assert!(matches!(
        writer
            .reclaim(10, 10_000, &CancellationToken::new())
            .unwrap(),
        ReclaimResult::Commit { .. }
    ));
    writer.close().unwrap();
}

fn continue_unknown(
    _: &RecoveryUnknownEnvelope,
) -> std::result::Result<RecoverySinkControl, iprange_livedb::Error> {
    Ok(RecoverySinkControl::Continue)
}

fn continue_validation(
    _: &ValidationFinding,
) -> std::result::Result<ValidationSinkControl, iprange_livedb::Error> {
    Ok(ValidationSinkControl::Continue)
}

#[test]
fn oversized_input_is_a_precondition_error_and_preserves_the_draft() {
    let files = TestPair::new("oversized");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        iprange_livedb::StructureKind::None,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(
        &files.main,
        budget(2 * 1024 * 1024),
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = iprange_livedb::CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    assert!(matches!(
        transaction.set_metadata_json(&vec![0; MAX_METADATA_UNCOMPRESSED as usize + 1]),
        Err(Error::InvalidArgument(_))
    ));
    assert_eq!(
        transaction.commit().unwrap().durability,
        CommitDurability::Committed
    );
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(7));
    assert_eq!(reader.metadata_json().unwrap(), None);
    reader.close().unwrap();
}
