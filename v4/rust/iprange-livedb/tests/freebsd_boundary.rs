#![cfg(target_os = "freebsd")]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::publication::{CleanupState, CoordinationCleanup, PublicationStatus};
use iprange_livedb::recovery::{
    inspect_recovery_candidates, recover_immutable, recover_live, recover_offline,
    OfflineQuiescenceCertification, RecoveryBudget, RecoveryInspectionMode, RecoverySinkControl,
    RecoveryUnknownEnvelope,
};
use iprange_livedb::snapshot::{
    snapshot_to, SnapshotBudget, SnapshotPublicationPolicy, SnapshotSourceMode,
};
use iprange_livedb::validation::{
    validate, LocalFileIdentity, ValidationBudget, ValidationFinding, ValidationMode,
    ValidationSinkControl,
};
use iprange_livedb::{
    create_live, initialize_live, reset_live_coordination, resolve_commit,
    resolve_interrupted_live_transition, AddressFamily, CancellationToken, CommitCleanupArtifacts,
    CommitDurability, CommitResolutionMode, CommitResult, ErrorCode, ImmutableReader, Ipv4Key,
    LiveReader, LiveResetPolicy, LiveTransitionResolutionMode, LiveWriter, TransactionBudget,
    ValueKind, ValueTag,
};

struct Files {
    directory: PathBuf,
    source: PathBuf,
}

impl Files {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-v4-freebsd-{label}-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&directory).unwrap();
        let source = directory.join("source.v4");
        fs::copy(fixture(), &source).unwrap();
        Self { directory, source }
    }

    fn path(&self, name: &str) -> PathBuf {
        self.directory.join(name)
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.directory);
    }
}

#[test]
fn immutable_validation_recovery_and_publication_are_supported() {
    let files = Files::new("immutable");
    let address = Ipv4Key(u32::from_be_bytes([10, 0, 0, 15]));

    let source = ImmutableReader::open(&files.source).unwrap();
    let source_info = source.info();
    assert_eq!(source.lookup_direct_v4(address).unwrap(), Some(3));
    drop(source);

    let validated = validate(
        &files.source,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(16 * 1024 * 1024, 1),
        &CancellationToken::new(),
        &mut continue_validation,
    )
    .unwrap();
    assert!(validated.valid);
    assert_eq!(
        validated.generation.unwrap().database_id,
        source_info.database_id
    );

    let inspection = inspect_recovery_candidates(
        &files.source,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    let candidate = *inspection.candidate(0).unwrap();

    let recovered = files.path("recovered.v4");
    let recovery = recover_immutable(
        &files.source,
        candidate,
        &recovered,
        &recovery_budget(),
        &mut continue_recovery,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(
        recovery.publication.publication,
        PublicationStatus::Published
    );
    assert_eq!(recovery.cleanup_state(), CleanupState::Clean);
    assert_eq!(
        ImmutableReader::open(&recovered)
            .unwrap()
            .lookup_direct_v4(address)
            .unwrap(),
        Some(3)
    );

    let offline = inspect_recovery_candidates(
        &files.source,
        RecoveryInspectionMode::Offline,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    let offline_output = files.path("offline-recovered.v4");
    recover_offline(
        &files.source,
        *offline.candidate(0).unwrap(),
        &offline_output,
        OfflineQuiescenceCertification::CallerCertified,
        &recovery_budget(),
        &mut continue_recovery,
        &CancellationToken::new(),
    )
    .unwrap();
    assert!(ImmutableReader::open(&offline_output).is_ok());

    let snapshot = files.path("snapshot.v4");
    let result = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &snapshot,
        SnapshotPublicationPolicy::FailIfExists,
        &snapshot_budget(2),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);

    let replaced = files.path("replaced.v4");
    fs::write(&replaced, b"previous destination").unwrap();
    let result = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &replaced,
        SnapshotPublicationPolicy::ReplaceExistingNoRollback,
        &snapshot_budget(3),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert!(ImmutableReader::open(&replaced).is_ok());

    let strict = files.path("strict.v4");
    let previous = b"strict previous destination";
    fs::write(&strict, previous).unwrap();
    let failure = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &strict,
        SnapshotPublicationPolicy::ReplaceExisting,
        &snapshot_budget(3),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::DurabilityUnsupported);
    assert_eq!(fs::read(&strict).unwrap(), previous);
    assert_no_private_artifacts(&files.directory);
}

#[test]
fn every_constructible_live_entry_rejects_before_mutation() {
    let files = Files::new("live-rejection");
    let original = fs::read(&files.source).unwrap();
    let cancellation = CancellationToken::new();
    let created = files.path("created.v4");

    assert_unsupported(create_live(
        &created,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        1,
        &cancellation,
    ));
    assert!(!created.exists());
    assert!(!sidecar(&created).exists());

    assert_unsupported(initialize_live(&files.source, 1, &cancellation));
    assert_unsupported(LiveReader::open(&files.source, &cancellation));
    assert_unsupported(LiveWriter::open(
        &files.source,
        transaction_budget(),
        &cancellation,
    ));
    assert_unsupported(inspect_recovery_candidates(
        &files.source,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 2),
        &cancellation,
    ));

    let validation = validate(
        &files.source,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(16 * 1024 * 1024, 2),
        &cancellation,
        &mut continue_validation,
    )
    .unwrap_err();
    assert_eq!(
        validation.cause.code(),
        ErrorCode::LiveCoordinationUnsupported
    );
    assert_eq!(validation.progress.checked_unique_pages, 0);

    let inspection = inspect_recovery_candidates(
        &files.source,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(0, 1),
        &cancellation,
    )
    .unwrap();
    let recovery_output = files.path("live-recovery.v4");
    let recovery = recover_live(
        &files.source,
        *inspection.candidate(0).unwrap(),
        &recovery_output,
        &recovery_budget(),
        &mut continue_recovery,
        &cancellation,
    )
    .unwrap_err();
    assert_eq!(recovery.cause.code, ErrorCode::LiveCoordinationUnsupported);
    assert!(!recovery_output.exists());

    let snapshot_output = files.path("live-snapshot.v4");
    let snapshot = snapshot_to(
        &files.source,
        SnapshotSourceMode::Live,
        &snapshot_output,
        SnapshotPublicationPolicy::FailIfExists,
        &snapshot_budget(3),
        &cancellation,
    )
    .unwrap_err();
    assert_eq!(snapshot.cause.code, ErrorCode::LiveCoordinationUnsupported);
    assert!(!snapshot_output.exists());

    assert_unsupported(resolve_interrupted_live_transition(
        &files.source,
        LiveTransitionResolutionMode::Complete,
        &cancellation,
    ));
    assert_unsupported(resolve_commit(
        &files.source,
        &synthetic_commit(),
        CommitResolutionMode::Live,
        &cancellation,
    ));

    let sidecar = sidecar(&files.source);
    fs::write(&sidecar, b"existing coordination sentinel").unwrap();
    let sidecar_before = fs::read(&sidecar).unwrap();
    assert_unsupported(reset_live_coordination(
        &files.source,
        1,
        LiveResetPolicy::DiscardPrevious,
        &cancellation,
    ));
    assert_eq!(fs::read(&sidecar).unwrap(), sidecar_before);
    assert_eq!(fs::read(&files.source).unwrap(), original);
    assert_no_private_artifacts(&files.directory);
}

fn fixture() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance/rust/direct-ipv4.iprdb")
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 1024 * 1024,
        max_private_pages: 100,
        max_file_growth_pages: 100,
        max_open_files: 2,
    }
}

fn recovery_budget() -> RecoveryBudget {
    RecoveryBudget::heap_only(16 * 1024 * 1024, 10_000, 2)
}

fn snapshot_budget(max_open_files: u32) -> SnapshotBudget {
    SnapshotBudget::new(16 * 1024 * 1024, 10_000, max_open_files)
}

fn synthetic_commit() -> CommitResult {
    let identity = LocalFileIdentity {
        kind: 1,
        bytes: [1; 32],
    };
    CommitResult {
        attempted_database_id: [1; 16],
        directory_identity: identity,
        main_identity: identity,
        attempted_transaction_id: 1,
        attempted_commit_nonce: [1; 16],
        durability: CommitDurability::OutcomeUnknown,
        cleanup: CommitCleanupArtifacts::default(),
        coordination_cleanup: CoordinationCleanup::None,
        cause: None,
    }
}

fn assert_unsupported<T>(result: iprange_livedb::Result<T>) {
    match result {
        Err(error) => assert_eq!(error.code(), ErrorCode::LiveCoordinationUnsupported),
        Ok(_) => panic!("live operation unexpectedly succeeded on FreeBSD"),
    }
}

fn continue_validation(_: &ValidationFinding) -> iprange_livedb::Result<ValidationSinkControl> {
    Ok(ValidationSinkControl::Continue)
}

fn continue_recovery(_: &RecoveryUnknownEnvelope) -> iprange_livedb::Result<RecoverySinkControl> {
    Ok(RecoverySinkControl::Continue)
}

fn sidecar(path: &Path) -> PathBuf {
    let mut name = path.file_name().unwrap().to_os_string();
    name.push(".readers");
    path.with_file_name(name)
}

fn assert_no_private_artifacts(directory: &Path) {
    for entry in fs::read_dir(directory).unwrap() {
        let name = entry.unwrap().file_name();
        assert!(
            !name.to_string_lossy().starts_with(".iprange-"),
            "private artifact remained: {name:?}"
        );
    }
}
