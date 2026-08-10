#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live,
    recovery::{inspect_recovery_candidates, RecoveryCandidateLabel, RecoveryInspectionMode},
    validation::{
        validate, ValidationBudget, ValidationMode, ValidationReason, ValidationSinkControl,
    },
    AddressFamily, CancellationToken, ErrorCode, Ipv4Key, LiveWriter, TransactionBudget, ValueKind,
    ValueTag,
};

struct Paths {
    live: PathBuf,
    snapshot: PathBuf,
}

impl Paths {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = std::env::temp_dir().join(format!("iprange-v4-recovery-{label}-{unique}"));
        Self {
            live: base.with_extension("live"),
            snapshot: base.with_extension("v4"),
        }
    }
}

impl Drop for Paths {
    fn drop(&mut self) {
        remove_pair(&self.live);
        remove_pair(&self.snapshot);
    }
}

#[test]
fn immutable_inspection_labels_newest_then_previous() {
    let paths = populated(Paths::new("immutable"));
    fs::copy(&paths.live, &paths.snapshot).unwrap();

    let inspection = inspect_recovery_candidates(
        &paths.snapshot,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(inspection.candidate_count(), 2);
    assert_eq!(
        inspection.candidate(0).unwrap().label(),
        RecoveryCandidateLabel::Newest
    );
    assert_eq!(inspection.candidate(0).unwrap().transaction_id(), 2);
    assert_eq!(
        inspection.candidate(1).unwrap().label(),
        RecoveryCandidateLabel::Previous
    );
    assert_eq!(inspection.candidate(1).unwrap().transaction_id(), 1);
    assert_eq!(inspection.progress.finding_count, 0);
}

#[test]
fn live_inspection_is_read_only_and_returns_only_proven_current() {
    let paths = populated(Paths::new("live"));
    let sidecar = sidecar_path(&paths.live);
    let before = fs::read(&sidecar).unwrap();

    let inspection = inspect_recovery_candidates(
        &paths.live,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 2),
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(inspection.candidate_count(), 1);
    assert_eq!(
        inspection.candidate(0).unwrap().label(),
        RecoveryCandidateLabel::Newest
    );
    assert_eq!(inspection.candidate(0).unwrap().transaction_id(), 2);
    assert_eq!(fs::read(sidecar).unwrap(), before);
}

#[test]
fn live_inspection_checks_cancellation_across_reader_capacity() {
    let paths = Paths::new("live-capacity-cancellation");
    create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        64,
        &CancellationToken::new(),
    )
    .unwrap();
    let sidecar = sidecar_path(&paths.live);
    let before = fs::read(&sidecar).unwrap();
    let polls = Arc::new(AtomicUsize::new(0));
    let observed = Arc::clone(&polls);
    let cancellation = CancellationToken::from_poll(Arc::new(move || {
        observed.fetch_add(1, Ordering::SeqCst);
        false
    }));

    inspect_recovery_candidates(
        &paths.live,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 2),
        &cancellation,
    )
    .unwrap();

    assert!(polls.load(Ordering::SeqCst) >= 64);
    assert_eq!(fs::read(sidecar).unwrap(), before);
}

#[test]
fn offline_candidate_validation_accepts_an_exact_previous_generation() {
    let paths = populated(Paths::new("offline"));
    let inspection = inspect_recovery_candidates(
        &paths.live,
        RecoveryInspectionMode::Offline,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    let previous = *inspection.candidate(1).unwrap();
    let mut findings = Vec::new();

    let result = validate(
        &paths.live,
        ValidationMode::OfflineCandidate(previous),
        &ValidationBudget::heap_only(1024, 1),
        &CancellationToken::new(),
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();

    assert!(result.valid);
    assert!(findings.is_empty());
    assert_eq!(result.generation.unwrap().transaction_id, 1);
}

#[test]
fn stale_offline_candidate_is_rejected_before_graph_access() {
    let paths = populated(Paths::new("stale"));
    let inspection = inspect_recovery_candidates(
        &paths.live,
        RecoveryInspectionMode::Offline,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    let previous = *inspection.candidate(1).unwrap();

    let mut writer =
        LiveWriter::open(&paths.live, transaction_budget(), &CancellationToken::new()).unwrap();
    let token = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(30), Ipv4Key(40), 8).unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();

    let failure = validate(
        &paths.live,
        ValidationMode::OfflineCandidate(previous),
        &ValidationBudget::heap_only(1024, 1),
        &CancellationToken::new(),
        &mut |_: &iprange_livedb::validation::ValidationFinding| {
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap_err();
    assert_eq!(failure.cause.code(), ErrorCode::RecoveryCandidateChanged);
    assert_eq!(failure.progress.checked_unique_pages, 0);
}

#[test]
fn candidate_token_is_bound_to_the_exact_source_inode() {
    let paths = populated(Paths::new("identity"));
    let inspection = inspect_recovery_candidates(
        &paths.live,
        RecoveryInspectionMode::Offline,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    let previous = *inspection.candidate(1).unwrap();
    fs::copy(&paths.live, &paths.snapshot).unwrap();

    let failure = validate(
        &paths.snapshot,
        ValidationMode::OfflineCandidate(previous),
        &ValidationBudget::heap_only(1024, 1),
        &CancellationToken::new(),
        &mut |_: &iprange_livedb::validation::ValidationFinding| {
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap_err();

    assert_eq!(failure.cause.code(), ErrorCode::RecoveryCandidateChanged);
    assert_eq!(failure.progress.checked_unique_pages, 0);
}

#[test]
fn unreadable_meta_pages_are_a_successful_zero_candidate_diagnostic() {
    let paths = Paths::new("unreadable");
    let file = fs::File::create(&paths.snapshot).unwrap();
    file.set_len(8192).unwrap();
    file.sync_all().unwrap();

    let inspection = inspect_recovery_candidates(
        &paths.snapshot,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(inspection.candidate_count(), 0);
    assert_eq!(
        inspection
            .progress
            .findings_for(ValidationReason::MetaUnavailable),
        2
    );
    assert!(inspection.progress.has_unbounded_unknown);
}

#[test]
fn live_inspection_requires_bound_coordination_and_explicit_resources() {
    let paths = populated(Paths::new("coordination"));
    fs::copy(&paths.live, &paths.snapshot).unwrap();

    let error = inspect_recovery_candidates(
        &paths.snapshot,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 2),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(error.code(), ErrorCode::LiveRecoveryCoordinationUnavailable);

    let error = inspect_recovery_candidates(
        &paths.live,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(error.code(), ErrorCode::InsufficientResourceBudget);

    let cancellation = CancellationToken::new();
    cancellation.cancel();
    let error = inspect_recovery_candidates(
        &paths.live,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 2),
        &cancellation,
    )
    .unwrap_err();
    assert_eq!(error.code(), ErrorCode::Cancelled);
}

fn populated(paths: Paths) -> Paths {
    create_live(
        &paths.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer =
        LiveWriter::open(&paths.live, transaction_budget(), &CancellationToken::new()).unwrap();
    let token = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();
    paths
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 1024 * 1024,
        max_private_pages: 100,
        max_file_growth_pages: 100,
        max_open_files: 2,
    }
}

fn sidecar_path(path: &Path) -> PathBuf {
    let mut sidecar = path.as_os_str().to_os_string();
    sidecar.push(".readers");
    PathBuf::from(sidecar)
}

fn remove_pair(path: &Path) {
    let _ = fs::remove_file(path);
    let _ = fs::remove_file(sidecar_path(path));
}
