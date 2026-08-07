use std::fs;
#[cfg(target_os = "linux")]
use std::fs::OpenOptions;
#[cfg(target_os = "linux")]
use std::io::{Read, Seek, SeekFrom, Write};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::publication::{CleanupState, PublicationStatus};
use iprange_livedb::recovery::{
    inspect_recovery_candidates, recover_immutable, recover_live, recover_offline,
    OfflineQuiescenceCertification, RecoveryBudget, RecoveryInspectionMode, RecoverySinkControl,
};
use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, ErrorCode, FeedName, ImmutableReader, Ipv4Key,
    LiveReader, LiveWriter, MembershipOperation, TransactionBudget, ValueKind, ValueTag,
};

struct TestFiles {
    directory: PathBuf,
    live: PathBuf,
    source: PathBuf,
    output: PathBuf,
}

impl TestFiles {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-v4-recovery-operation-{label}-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&directory).unwrap();
        Self {
            live: directory.join("live.v4"),
            source: directory.join("source.v4"),
            output: directory.join("output.v4"),
            directory,
        }
    }
}

impl Drop for TestFiles {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.directory);
    }
}

#[test]
fn immutable_direct_recovery_publishes_a_new_canonical_database() {
    let files = populated_direct(TestFiles::new("immutable-direct"), 2);
    fs::copy(&files.live, &files.source).unwrap();
    let candidate = inspect(&files.source, RecoveryInspectionMode::Immutable, 1);
    let source_database = candidate.database_id();

    let result = recover_immutable(
        &files.source,
        candidate,
        &files.output,
        &recovery_budget(2),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(result.report.ranges.accepted, 1);
    assert!(result.scratch.is_none());
    assert!(!sidecar(&files.output).exists());
    let reader = ImmutableReader::open(&files.output).unwrap();
    assert_ne!(reader.info().database_id, source_database);
    assert_eq!(reader.info().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(7));
}

#[test]
fn offline_recovery_can_select_the_exact_previous_generation() {
    let files = populated_direct(TestFiles::new("offline-previous"), 2);
    let inspection = iprange_livedb::recovery::inspect_recovery_candidates(
        &files.live,
        RecoveryInspectionMode::Offline,
        &iprange_livedb::validation::ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    let previous = *inspection.candidate(1).unwrap();

    let result = recover_offline(
        &files.live,
        previous,
        &files.output,
        OfflineQuiescenceCertification::CallerCertified,
        &recovery_budget(2),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(result.report.ranges.accepted, 0);
    let reader = ImmutableReader::open(&files.output).unwrap();
    assert_eq!(reader.info().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), None);
}

#[test]
fn live_recovery_pins_and_releases_the_exact_current_generation() {
    let files = populated_direct(TestFiles::new("live"), 1);
    let candidate = inspect(&files.live, RecoveryInspectionMode::Live, 2);

    let result = recover_live(
        &files.live,
        candidate,
        &files.output,
        &recovery_budget(3),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert!(!sidecar(&files.output).exists());
    let mut reader = LiveReader::open(&files.live, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(7));
    reader.close().unwrap();
}

#[cfg(target_os = "linux")]
#[test]
fn terminal_recovery_sink_result_wins_over_a_later_sidecar_sigbus() {
    for sink_error in [false, true] {
        let files = populated_direct(TestFiles::new("live-terminal-sidecar-fault"), 1);
        let candidate = inspect(&files.live, RecoveryInspectionMode::Live, 2);
        let root = selected_range_root(&files.live);
        let mut main = OpenOptions::new().write(true).open(&files.live).unwrap();
        main.seek(SeekFrom::Start(u64::from(root) * 4096 + 100))
            .unwrap();
        main.write_all(&[0x5a]).unwrap();
        main.sync_all().unwrap();

        let sidecar = sidecar(&files.live);
        let sidecar_len = fs::metadata(&sidecar).unwrap().len();
        let failure = recover_live(
            &files.live,
            candidate,
            &files.output,
            &recovery_budget(3),
            &mut |_: &iprange_livedb::recovery::RecoveryUnknownEnvelope| {
                OpenOptions::new()
                    .write(true)
                    .open(&sidecar)
                    .unwrap()
                    .set_len(4096)
                    .unwrap();
                if sink_error {
                    Err(iprange_livedb::Error::InvalidArgument(
                        "injected recovery sink failure",
                    ))
                } else {
                    Ok(RecoverySinkControl::Stop)
                }
            },
            &CancellationToken::new(),
        )
        .unwrap_err();
        assert_eq!(
            failure.cause.code,
            if sink_error {
                ErrorCode::SinkFailed
            } else {
                ErrorCode::StoppedBySink
            }
        );
        assert_eq!(failure.report.unknown_envelopes, 1);
        assert!(failure.cleanup.is_empty(), "{failure:#?}");
        assert!(!files.output.exists());
        assert_no_private_artifacts(&files.directory);

        OpenOptions::new()
            .write(true)
            .open(&sidecar)
            .unwrap()
            .set_len(sidecar_len)
            .unwrap();
        let mut reader = LiveReader::open(&files.live, &CancellationToken::new()).unwrap();
        reader.close().unwrap();
    }
}

#[test]
fn immutable_membership_recovery_preserves_names_indexes_and_memberships() {
    let files = populated_membership(TestFiles::new("membership"));
    fs::copy(&files.live, &files.source).unwrap();
    let candidate = inspect(&files.source, RecoveryInspectionMode::Immutable, 1);

    recover_immutable(
        &files.source,
        candidate,
        &files.output,
        &recovery_budget(2),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap();

    let reader = ImmutableReader::open(&files.output).unwrap();
    let alpha = reader.lookup_feed("alpha").unwrap().unwrap();
    let beta = reader.lookup_feed("beta").unwrap().unwrap();
    let membership = reader.lookup_membership_v4(Ipv4Key(7)).unwrap().unwrap();
    assert!(membership.contains_index(alpha.index).unwrap());
    assert!(membership.contains_index(beta.index).unwrap());
}

#[test]
fn stale_candidate_existing_destination_and_cancellation_publish_nothing() {
    let files = populated_direct(TestFiles::new("preparation-failures"), 2);
    fs::copy(&files.live, &files.source).unwrap();
    let candidate = inspect(&files.source, RecoveryInspectionMode::Immutable, 1);
    let displaced = files.directory.join("displaced.v4");
    fs::rename(&files.source, &displaced).unwrap();
    fs::copy(&displaced, &files.source).unwrap();

    let failure = recover_immutable(
        &files.source,
        candidate,
        &files.output,
        &recovery_budget(2),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::RecoveryCandidateChanged);
    assert!(!files.output.exists());

    fs::write(&files.output, b"foreign").unwrap();
    let candidate = inspect(&files.source, RecoveryInspectionMode::Immutable, 1);
    let failure = recover_immutable(
        &files.source,
        candidate,
        &files.output,
        &recovery_budget(2),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::NameExists);
    assert_eq!(fs::read(&files.output).unwrap(), b"foreign");

    fs::remove_file(&files.output).unwrap();
    let cancellation = CancellationToken::new();
    cancellation.cancel();
    let failure = recover_immutable(
        &files.source,
        candidate,
        &files.output,
        &recovery_budget(2),
        &mut continue_unknown,
        &cancellation,
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::Cancelled);
    assert!(!files.output.exists());
    assert_no_private_artifacts(&files.directory);
}

#[test]
fn live_recovery_rejects_a_previous_candidate_without_claiming_capacity() {
    let files = populated_direct(TestFiles::new("live-previous"), 1);
    let inspection = iprange_livedb::recovery::inspect_recovery_candidates(
        &files.live,
        RecoveryInspectionMode::Offline,
        &iprange_livedb::validation::ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    let previous = *inspection.candidate(1).unwrap();

    let failure = recover_live(
        &files.live,
        previous,
        &files.output,
        &recovery_budget(3),
        &mut continue_unknown,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::InvalidArgument);
    let mut reader = LiveReader::open(&files.live, &CancellationToken::new()).unwrap();
    reader.close().unwrap();
    assert!(!files.output.exists());
}

fn populated_direct(files: TestFiles, reader_capacity: u32) -> TestFiles {
    create_live(
        &files.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        reader_capacity,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer =
        LiveWriter::open(&files.live, transaction_budget(), &CancellationToken::new()).unwrap();
    let token = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();
    files
}

fn populated_membership(files: TestFiles) -> TestFiles {
    create_live(
        &files.live,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer =
        LiveWriter::open(&files.live, transaction_budget(), &CancellationToken::new()).unwrap();
    let mut transaction = writer
        .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
        .unwrap();
    let alpha = transaction
        .ensure_feed(FeedName::new("alpha").unwrap())
        .unwrap();
    let beta = transaction
        .ensure_feed(FeedName::new("beta").unwrap())
        .unwrap();
    let empty = transaction.empty_membership().unwrap();
    let alpha = transaction.add_feed(empty, alpha).unwrap();
    let both = transaction.add_feed(alpha, beta).unwrap();
    transaction
        .apply_v4(Ipv4Key(5), Ipv4Key(9), both, MembershipOperation::Replace)
        .unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();
    files
}

fn inspect(
    path: &Path,
    mode: RecoveryInspectionMode,
    open_files: u32,
) -> iprange_livedb::recovery::RecoveryCandidate {
    let inspection = inspect_recovery_candidates(
        path,
        mode,
        &iprange_livedb::validation::ValidationBudget::heap_only(0, open_files),
        &CancellationToken::new(),
    )
    .unwrap();
    *inspection.candidate(0).unwrap()
}

#[cfg(target_os = "linux")]
fn selected_range_root(path: &Path) -> u32 {
    let mut file = OpenOptions::new().read(true).open(path).unwrap();
    let mut metas = [[0u8; 4096]; 2];
    file.read_exact(&mut metas[0]).unwrap();
    file.read_exact(&mut metas[1]).unwrap();
    let transaction = |page: &[u8; 4096]| u64::from_le_bytes(page[48..56].try_into().unwrap());
    let selected = usize::from(transaction(&metas[1]) > transaction(&metas[0]));
    u32::from_le_bytes(metas[selected][144..148].try_into().unwrap())
}

fn recovery_budget(open_files: u32) -> RecoveryBudget {
    RecoveryBudget::heap_only(16 * 1024 * 1024, 100_000, open_files)
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn continue_unknown(
    _: &iprange_livedb::recovery::RecoveryUnknownEnvelope,
) -> iprange_livedb::Result<RecoverySinkControl> {
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
            "private recovery artifact remained: {name:?}"
        );
    }
}
