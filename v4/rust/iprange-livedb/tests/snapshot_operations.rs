use std::fs;
use std::path::{Path, PathBuf};
#[cfg(target_os = "linux")]
use std::sync::atomic::{AtomicBool, Ordering};
#[cfg(target_os = "linux")]
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::publication::{
    resolve_publication, CleanupState, PublicationPolicy, PublicationResolutionMode,
    PublicationStatus,
};
use iprange_livedb::{
    create_live, snapshot_to, AddressFamily, CancellationToken, ErrorCode, FeedName,
    ImmutableReader, Ipv4Key, LiveWriter, MembershipOperation, SnapshotBudget,
    SnapshotPublicationPolicy, SnapshotSourceMode, TransactionBudget, ValueKind, ValueTag,
};
#[cfg(target_os = "linux")]
use iprange_livedb::{DirectRange, FinishedWorkflow};
use sha2::{Digest, Sha512};

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
            "iprange-v4-snapshot-{label}-{}-{unique}",
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
fn immutable_direct_snapshot_preserves_identity_generation_ranges_and_metadata() {
    let files = populated_direct(TestFiles::new("immutable-direct"));
    fs::copy(&files.live, &files.source).unwrap();
    let source = ImmutableReader::open(&files.source).unwrap();
    let source_info = source.info();
    let source_length = fs::metadata(&files.source).unwrap().len();

    let result = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(
        result.publication.attempt.publication_policy,
        PublicationPolicy::FailIfExists
    );
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert!(!sidecar(&files.output).exists());
    let output = ImmutableReader::open(&files.output).unwrap();
    assert_eq!(output.info().database_id, source_info.database_id);
    assert_eq!(output.info().transaction_id, source_info.transaction_id);
    assert_eq!(output.info().commit_nonce, source_info.commit_nonce);
    assert_eq!(output.lookup_direct_v4(Ipv4Key(9)).unwrap(), None);
    assert_eq!(output.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(1));
    assert_eq!(output.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(2));
    assert_eq!(output.lookup_direct_v4(Ipv4Key(21)).unwrap(), None);
    assert_eq!(
        output.metadata_json().unwrap().as_deref(),
        Some(br#"{"source":"test"}"#.as_slice())
    );
    assert!(fs::metadata(&files.output).unwrap().len() <= source_length);
}

#[test]
fn live_membership_snapshot_preserves_names_indexes_bitmaps_and_metadata() {
    let files = populated_membership(TestFiles::new("live-membership"));
    let mut source =
        iprange_livedb::LiveReader::open(&files.live, &CancellationToken::new()).unwrap();
    let source_info = source.info().unwrap();
    let alpha_index = source.lookup_feed("alpha").unwrap().unwrap().index;
    let beta_index = source.lookup_feed("beta").unwrap().unwrap().index;
    source.close().unwrap();

    snapshot_to(
        &files.live,
        SnapshotSourceMode::Live,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(3),
        &CancellationToken::new(),
    )
    .unwrap();

    let output = ImmutableReader::open(&files.output).unwrap();
    assert_eq!(output.info().database_id, source_info.database_id);
    assert_eq!(output.info().transaction_id, source_info.transaction_id);
    assert_eq!(
        output.lookup_feed("alpha").unwrap().unwrap().index,
        alpha_index
    );
    assert_eq!(
        output.lookup_feed("beta").unwrap().unwrap().index,
        beta_index
    );
    let membership = output.lookup_membership_v4(Ipv4Key(7)).unwrap().unwrap();
    assert!(membership.contains_index(alpha_index).unwrap());
    assert!(membership.contains_index(beta_index).unwrap());
    assert_eq!(
        output.metadata_json().unwrap().as_deref(),
        Some(b"{}".as_slice())
    );
}

#[test]
fn cancellation_existing_destination_and_budget_failure_publish_nothing() {
    let files = populated_direct(TestFiles::new("preconditions"));
    fs::copy(&files.live, &files.source).unwrap();

    let cancelled = CancellationToken::new();
    cancelled.cancel();
    let failure = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &cancelled,
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::Cancelled);
    assert!(!files.output.exists());

    fs::write(&files.output, b"foreign").unwrap();
    let failure = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::NameExists);
    assert_eq!(fs::read(&files.output).unwrap(), b"foreign");

    fs::remove_file(&files.output).unwrap();
    let failure = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(16 * 1024 * 1024, 1, 2),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::InsufficientResourceBudget);
    assert!(!files.output.exists());
    assert_no_private_artifacts(&files.directory);
}

#[test]
fn heap_and_exact_output_page_budgets_fail_before_publication() {
    let files = populated_direct(TestFiles::new("resource-boundaries"));
    fs::copy(&files.live, &files.source).unwrap();

    let heap_failure = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(16, 100_000, 2),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(
        heap_failure.cause.code,
        ErrorCode::InsufficientResourceBudget
    );
    assert!(!files.output.exists());
    assert_no_private_artifacts(&files.directory);

    snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &CancellationToken::new(),
    )
    .unwrap();
    let exact_pages = fs::metadata(&files.output).unwrap().len() / 4096;
    assert!(exact_pages > 2);
    fs::remove_file(&files.output).unwrap();

    let page_failure = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(16 * 1024 * 1024, exact_pages - 1, 2),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(
        page_failure.cause.code,
        ErrorCode::InsufficientResourceBudget
    );
    assert!(!files.output.exists());
    assert_no_private_artifacts(&files.directory);

    snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(16 * 1024 * 1024, exact_pages, 2),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(
        fs::metadata(&files.output).unwrap().len() / 4096,
        exact_pages
    );
}

#[test]
fn live_snapshot_requires_the_sidecar_descriptor_budget() {
    let files = populated_direct(TestFiles::new("live-budget"));
    let failure = snapshot_to(
        &files.live,
        SnapshotSourceMode::Live,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::InsufficientResourceBudget);
    assert!(!files.output.exists());
}

#[test]
fn replacement_accepts_arbitrary_previous_bytes_and_reports_exact_evidence() {
    let files = populated_direct(TestFiles::new("replace-arbitrary"));
    fs::copy(&files.live, &files.source).unwrap();
    let previous = b"not a v4 database";
    fs::write(&files.output, previous).unwrap();

    let result = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::ReplaceExisting,
        &budget(3),
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(
        result.publication.attempt.publication_policy,
        PublicationPolicy::ReplaceExisting
    );
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    let evidence = result
        .publication
        .attempt
        .previous_destination
        .as_ref()
        .unwrap();
    assert_eq!(evidence.byte_length, previous.len() as u64);
    assert_eq!(evidence.sha512, Sha512::digest(previous).as_slice());
    assert_ne!(
        evidence.identity,
        result.publication.attempt.output_identity
    );
    assert_eq!(
        ImmutableReader::open(&files.output)
            .unwrap()
            .lookup_direct_v4(Ipv4Key(15))
            .unwrap(),
        Some(2)
    );
    assert_no_private_artifacts(&files.directory);
}

#[test]
fn no_rollback_replacement_is_explicit_and_cannot_be_removed_after_publication() {
    let files = populated_direct(TestFiles::new("replace-no-rollback"));
    fs::copy(&files.live, &files.source).unwrap();
    fs::write(&files.output, b"previous").unwrap();

    let result = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::ReplaceExistingNoRollback,
        &budget(3),
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(
        result.publication.attempt.publication_policy,
        PublicationPolicy::ReplaceExistingNoRollback
    );
    assert!(result.publication.attempt.previous_destination.is_some());
    let published = fs::read(&files.output).unwrap();
    let error = resolve_publication(
        &files.output,
        Some(&result.publication),
        PublicationResolutionMode::Remove,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(error.code, ErrorCode::Unresolvable);
    assert_eq!(fs::read(&files.output).unwrap(), published);
    assert_no_private_artifacts(&files.directory);
}

#[test]
fn immutable_snapshot_can_compact_its_own_path_by_replacement() {
    let files = populated_direct(TestFiles::new("replace-self"));
    fs::copy(&files.live, &files.source).unwrap();
    let before = ImmutableReader::open(&files.source).unwrap().info();

    let result = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.source,
        SnapshotPublicationPolicy::ReplaceExisting,
        &budget(3),
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert!(result.publication.attempt.previous_destination.is_some());
    assert_ne!(
        result
            .publication
            .attempt
            .previous_destination
            .as_ref()
            .unwrap()
            .identity,
        result.publication.attempt.output_identity
    );
    let after = ImmutableReader::open(&files.source).unwrap();
    assert_eq!(after.info().database_id, before.database_id);
    assert_eq!(after.info().transaction_id, before.transaction_id);
    assert_eq!(after.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(2));
    assert_no_private_artifacts(&files.directory);
}

#[test]
fn replacement_requires_an_existing_destination_and_rejects_live_self() {
    let files = populated_direct(TestFiles::new("replace-preconditions"));
    fs::copy(&files.live, &files.source).unwrap();

    let missing = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::ReplaceExisting,
        &budget(3),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(missing.cause.code, ErrorCode::NameNotFound);
    assert!(!files.output.exists());
    assert_no_private_artifacts(&files.directory);

    let live_bytes = fs::read(&files.live).unwrap();
    let self_failure = snapshot_to(
        &files.live,
        SnapshotSourceMode::Live,
        &files.live,
        SnapshotPublicationPolicy::ReplaceExisting,
        &budget(3),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(self_failure.cause.code, ErrorCode::InvalidArgument);
    assert_eq!(fs::read(&files.live).unwrap(), live_bytes);
    assert_no_private_artifacts(&files.directory);
}

#[test]
fn malformed_traversal_fails_cleanly_but_crc_damage_is_not_implicitly_validated() {
    let malformed = populated_direct(TestFiles::new("malformed-traversal"));
    fs::copy(&malformed.live, &malformed.source).unwrap();
    mutate_selected_range_root(&malformed.source, |page| {
        page[..4].copy_from_slice(b"BAD!");
    });
    assert!(ImmutableReader::open(&malformed.source).is_ok());

    let failure = snapshot_to(
        &malformed.source,
        SnapshotSourceMode::Immutable,
        &malformed.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(failure.cause.code, ErrorCode::FormatInvalid);
    assert!(!malformed.output.exists());
    assert_no_private_artifacts(&malformed.directory);

    let unchecked_crc = populated_direct(TestFiles::new("unchecked-crc"));
    fs::copy(&unchecked_crc.live, &unchecked_crc.source).unwrap();
    mutate_selected_range_root(&unchecked_crc.source, |page| page[28] ^= 0xff);

    snapshot_to(
        &unchecked_crc.source,
        SnapshotSourceMode::Immutable,
        &unchecked_crc.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(
        ImmutableReader::open(&unchecked_crc.output)
            .unwrap()
            .lookup_direct_v4(Ipv4Key(15))
            .unwrap(),
        Some(2)
    );
}

#[cfg(target_os = "linux")]
#[test]
fn live_source_replacement_after_reader_claim_blocks_publication() {
    let files = populated_large_direct(TestFiles::new("live-source-race"), 20_000);
    let source = files.live.clone();
    let moved = files.directory.join("moved-live.v4");
    let sidecar = sidecar(&source);
    let done = Arc::new(AtomicBool::new(false));
    let controller_done = Arc::clone(&done);
    let controller = std::thread::spawn(move || {
        let gate = fs::OpenOptions::new()
            .read(true)
            .write(true)
            .open(&sidecar)
            .unwrap();
        while !controller_done.load(Ordering::Acquire) {
            let bytes = fs::read(&sidecar).unwrap();
            let reader_active = bytes[4096..].iter().any(|&byte| byte != 0);
            if reader_active && try_exclusive_gate(&gate) {
                fs::rename(&source, &moved).unwrap();
                unlock_gate(&gate);
                return true;
            }
            std::thread::yield_now();
        }
        false
    });

    let outcome = snapshot_to(
        &files.live,
        SnapshotSourceMode::Live,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(3),
        &CancellationToken::new(),
    );
    done.store(true, Ordering::Release);
    assert!(
        controller.join().unwrap(),
        "controller missed the live claim"
    );
    let failure = outcome.unwrap_err();

    assert!(matches!(
        failure.cause.code,
        ErrorCode::RecoveryCandidateChanged | ErrorCode::LiveRecoveryCoordinationUnavailable
    ));
    assert!(!files.output.exists());
    assert_no_private_artifacts(&files.directory);
}

#[cfg(target_os = "linux")]
#[test]
fn immutable_source_replacement_during_copy_blocks_publication() {
    let files = populated_large_direct(TestFiles::new("immutable-source-race"), 5_000);
    fs::copy(&files.live, &files.source).unwrap();
    let source = files.source.clone();
    let moved = files.directory.join("moved-source.v4");
    let directory = files.directory.clone();
    let done = Arc::new(AtomicBool::new(false));
    let controller_done = Arc::clone(&done);
    let controller = std::thread::spawn(move || {
        while !controller_done.load(Ordering::Acquire) {
            let output_started = fs::read_dir(&directory).unwrap().any(|entry| {
                entry
                    .unwrap()
                    .file_name()
                    .to_string_lossy()
                    .starts_with(".iprange-publish-")
            });
            if output_started {
                fs::rename(&source, &moved).unwrap();
                return true;
            }
            std::thread::yield_now();
        }
        false
    });

    let outcome = snapshot_to(
        &files.source,
        SnapshotSourceMode::Immutable,
        &files.output,
        SnapshotPublicationPolicy::FailIfExists,
        &budget(2),
        &CancellationToken::new(),
    );
    done.store(true, Ordering::Release);
    assert!(
        controller.join().unwrap(),
        "controller missed private-output creation"
    );
    let failure = outcome.unwrap_err();

    assert_eq!(failure.cause.code, ErrorCode::RecoveryCandidateChanged);
    assert!(!files.output.exists());
    assert_no_private_artifacts(&files.directory);
}

#[cfg(target_os = "linux")]
#[test]
fn live_snapshot_pins_its_generation_while_a_writer_advances() {
    let files = populated_large_direct(TestFiles::new("live-pinned-generation"), 5_000);
    let mut before =
        iprange_livedb::LiveReader::open(&files.live, &CancellationToken::new()).unwrap();
    let before_info = before.info().unwrap();
    before.close().unwrap();

    let source = files.live.clone();
    let output = files.output.clone();
    let done = Arc::new(AtomicBool::new(false));
    let snapshot_done = Arc::clone(&done);
    let snapshot = std::thread::spawn(move || {
        let result = snapshot_to(
            source,
            SnapshotSourceMode::Live,
            output,
            SnapshotPublicationPolicy::FailIfExists,
            &budget(3),
            &CancellationToken::new(),
        );
        snapshot_done.store(true, Ordering::Release);
        result
    });
    assert!(
        wait_for_reader(&sidecar(&files.live), &done),
        "snapshot did not expose its reader claim"
    );

    let mut writer =
        LiveWriter::open(&files.live, transaction_budget(), &CancellationToken::new()).unwrap();
    let token = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction
        .assign_v4(Ipv4Key(1_000_000), Ipv4Key(1_000_000), 999)
        .unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();
    snapshot.join().unwrap().unwrap();

    let output = ImmutableReader::open(&files.output).unwrap();
    assert_eq!(output.info().transaction_id, before_info.transaction_id);
    assert_eq!(output.lookup_direct_v4(Ipv4Key(1_000_000)).unwrap(), None);
    let mut live =
        iprange_livedb::LiveReader::open(&files.live, &CancellationToken::new()).unwrap();
    assert!(live.info().unwrap().transaction_id > before_info.transaction_id);
    assert_eq!(
        live.lookup_direct_v4(Ipv4Key(1_000_000)).unwrap(),
        Some(999)
    );
    live.close().unwrap();
}

fn populated_direct(files: TestFiles) -> TestFiles {
    create_live(
        &files.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"timestamp").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer =
        LiveWriter::open(&files.live, transaction_budget(), &CancellationToken::new()).unwrap();
    let cancellation = iprange_livedb::CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    transaction.assign_v4(Ipv4Key(12), Ipv4Key(18), 2).unwrap();
    transaction
        .set_metadata_json(br#"{"source":"test"}"#)
        .unwrap();
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
    transaction.set_metadata_json(b"{}").unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();
    files
}

#[cfg(target_os = "linux")]
fn populated_large_direct(files: TestFiles, count: u32) -> TestFiles {
    create_live(
        &files.live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"timestamp").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let ranges: Vec<_> = (0..count)
        .map(|index| DirectRange {
            from: Ipv4Key(index * 2),
            to: Ipv4Key(index * 2),
            value: index % 251 + 1,
        })
        .collect();
    let mut writer = LiveWriter::open(
        &files.live,
        TransactionBudget {
            max_heap_bytes: 2 * 1024 * 1024,
            max_private_pages: 100_000,
            max_file_growth_pages: 100_000,
            max_open_files: 2,
        },
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut workflow = writer.begin_direct_replacement(&cancellation).unwrap();
    workflow.add_ranges_v4_slice(&ranges).unwrap();
    match workflow.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("large source unexpectedly unchanged"),
    }
    writer.close().unwrap();
    files
}

fn budget(open_files: u32) -> SnapshotBudget {
    SnapshotBudget::new(16 * 1024 * 1024, 100_000, open_files)
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
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
            "private snapshot artifact remained: {name:?}"
        );
    }
}

fn mutate_selected_range_root(path: &Path, mutate: impl FnOnce(&mut [u8])) {
    let mut bytes = fs::read(path).unwrap();
    let left_txn = u64::from_le_bytes(bytes[48..56].try_into().unwrap());
    let right_txn = u64::from_le_bytes(bytes[4096 + 48..4096 + 56].try_into().unwrap());
    let meta = if right_txn > left_txn { 4096 } else { 0 };
    let root = u32::from_le_bytes(bytes[meta + 144..meta + 148].try_into().unwrap()) as usize;
    assert!(root >= 2);
    let start = root * 4096;
    mutate(&mut bytes[start..start + 4096]);
    fs::write(path, bytes).unwrap();
}

#[cfg(target_os = "linux")]
fn try_exclusive_gate(file: &fs::File) -> bool {
    use std::os::fd::AsRawFd;

    let mut lock = libc::flock {
        l_type: libc::F_WRLCK as libc::c_short,
        l_whence: libc::SEEK_SET as libc::c_short,
        l_start: 0,
        l_len: 1,
        l_pid: 0,
    };
    let result = unsafe { libc::fcntl(file.as_raw_fd(), libc::F_OFD_SETLK, &mut lock) };
    if result == 0 {
        return true;
    }
    let error = std::io::Error::last_os_error();
    if matches!(error.raw_os_error(), Some(libc::EACCES | libc::EAGAIN)) {
        false
    } else {
        panic!("gate lock failed: {error}")
    }
}

#[cfg(target_os = "linux")]
fn unlock_gate(file: &fs::File) {
    use std::os::fd::AsRawFd;

    let mut lock = libc::flock {
        l_type: libc::F_UNLCK as libc::c_short,
        l_whence: libc::SEEK_SET as libc::c_short,
        l_start: 0,
        l_len: 1,
        l_pid: 0,
    };
    assert_eq!(
        unsafe { libc::fcntl(file.as_raw_fd(), libc::F_OFD_SETLK, &mut lock) },
        0
    );
}

#[cfg(target_os = "linux")]
fn wait_for_reader(sidecar: &Path, done: &AtomicBool) -> bool {
    while !done.load(Ordering::Acquire) {
        let bytes = fs::read(sidecar).unwrap();
        if bytes[4096..].iter().any(|&byte| byte != 0) {
            return true;
        }
        std::thread::yield_now();
    }
    false
}
