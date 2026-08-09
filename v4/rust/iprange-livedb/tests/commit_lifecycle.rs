#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

#[cfg(windows)]
use iprange_livedb::validation::{
    validate, ValidationBudget, ValidationMode, ValidationSinkControl,
};
use iprange_livedb::{
    create_live, resolve_commit, AbortOutcome, AddressFamily, CancellationToken, CloseOutcome,
    CommitCleanupArtifacts, CommitDurability, CommitResolution, CommitResolutionMode, CommitResult,
    Error, Ipv4Key, LiveReader, LiveWriter, LocalFileRelation, TransactionBudget, ValueKind,
    ValueTag,
};

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-commit-{label}-{}-{unique}",
                std::process::id()
            )),
        }
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.main.with_file_name(name)
    }

    fn saved_main(&self) -> PathBuf {
        self.main.with_extension("saved")
    }
}

impl Drop for TestPair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
        let _ = fs::remove_file(self.saved_main());
    }
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

fn create(files: &TestPair) {
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn commit_one(writer: &mut LiveWriter, value: u32) -> CommitResult {
    let token = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction
        .assign_v4(Ipv4Key(value), Ipv4Key(value), value)
        .unwrap();
    transaction.commit().unwrap()
}

fn altered_attempt(source: &CommitResult, transaction_id: u64, nonce: [u8; 16]) -> CommitResult {
    CommitResult {
        attempted_database_id: source.attempted_database_id,
        directory_identity: source.directory_identity,
        main_identity: source.main_identity,
        attempted_transaction_id: transaction_id,
        attempted_commit_nonce: nonce,
        durability: CommitDurability::OutcomeUnknown,
        cleanup: CommitCleanupArtifacts::default(),
        coordination_cleanup: iprange_livedb::publication::CoordinationCleanup::None,
        cause: None,
    }
}

#[test]
fn commit_result_and_live_resolution_report_exact_facts() {
    let files = TestPair::new("resolve");
    create(&files);
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let committed = commit_one(&mut writer, 7);

    assert_eq!(committed.durability, CommitDurability::Committed);
    assert_eq!(committed.attempted_transaction_id, 2);
    assert_ne!(committed.directory_identity.bytes, [0; 32]);
    assert_ne!(committed.main_identity.bytes, [0; 32]);
    assert!(committed.cleanup.is_empty());
    assert!(matches!(
        resolve_commit(
            &files.main,
            &committed,
            CommitResolutionMode::Live,
            &cancellation,
        ),
        Err(Error::WriterBusy)
    ));

    assert_eq!(writer.close().unwrap().outcome, CloseOutcome::Closed);
    let resolved = resolve_commit(
        &files.main,
        &committed,
        CommitResolutionMode::Live,
        &cancellation,
    )
    .unwrap();
    assert_eq!(resolved.resolution, CommitResolution::Committed);
    assert_eq!(
        resolved.local_file_relation,
        LocalFileRelation::SameLocalFile
    );

    let wrong_nonce = altered_attempt(&committed, committed.attempted_transaction_id, [0x55; 16]);
    assert_eq!(
        resolve_commit(
            &files.main,
            &wrong_nonce,
            CommitResolutionMode::Live,
            &cancellation,
        )
        .unwrap()
        .resolution,
        CommitResolution::NotCommitted
    );
}

#[test]
fn later_generations_do_not_invent_an_old_commit_outcome() {
    let files = TestPair::new("superseded");
    create(&files);
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let first = commit_one(&mut writer, 7);
    let _second = commit_one(&mut writer, 8);
    writer.close().unwrap();

    let old_unknown = altered_attempt(&first, 1, [0x66; 16]);
    let resolved = resolve_commit(
        &files.main,
        &old_unknown,
        CommitResolutionMode::Live,
        &cancellation,
    )
    .unwrap();
    assert_eq!(resolved.resolution, CommitResolution::SupersededUnknown);
}

#[test]
fn resolution_reports_a_deliberate_logical_copy_as_a_different_local_file() {
    let source = TestPair::new("copy-source");
    let copy = TestPair::new("copy-destination");
    create(&source);
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&source.main, budget(), &CancellationToken::new()).unwrap();
    let committed = commit_one(&mut writer, 7);
    writer.close().unwrap();
    fs::copy(&source.main, &copy.main).unwrap();

    let resolved = resolve_commit(
        &copy.main,
        &committed,
        CommitResolutionMode::Immutable,
        &cancellation,
    )
    .unwrap();
    assert_eq!(resolved.resolution, CommitResolution::Committed);
    assert_eq!(
        resolved.local_file_relation,
        LocalFileRelation::DifferentLocalFile
    );
}

#[test]
fn pending_close_aborts_and_releases_the_lease_without_consuming_the_handle() {
    let files = TestPair::new("close");
    create(&files);
    let committed_length = fs::metadata(&files.main).unwrap().len();
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    drop(transaction);
    #[cfg(not(windows))]
    {
        let file = fs::OpenOptions::new()
            .write(true)
            .open(&files.main)
            .unwrap();
        file.set_len(committed_length + 4096).unwrap();
    }

    let closed = writer.close().unwrap();
    assert_eq!(closed.outcome, CloseOutcome::Closed);
    assert_eq!(closed.abort_outcome, Some(AbortOutcome::Aborted));
    assert!(closed.cleanup.is_empty());
    assert_eq!(fs::metadata(&files.main).unwrap().len(), committed_length);

    let mut replacement =
        LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    replacement.close().unwrap();
    assert_eq!(writer.close().unwrap().outcome, CloseOutcome::Closed);

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), None);
    reader.close().unwrap();
}

#[cfg(windows)]
#[test]
fn mapped_reader_retains_abort_capacity_for_reuse_then_allows_shrink() {
    let files = TestPair::new("mapped-tail");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let initial_length = fs::metadata(&files.main).unwrap().len();
    let cancellation = CancellationToken::new();
    let mut pinned = LiveReader::open(&files.main, &cancellation).unwrap();

    let mut bounded = budget();
    bounded.max_private_pages = 128;
    bounded.max_file_growth_pages = 128;
    let mut writer = LiveWriter::open(&files.main, bounded, &cancellation).unwrap();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    let retained_capacity = fs::metadata(&files.main).unwrap().len();
    assert!(retained_capacity > initial_length);
    assert_eq!(transaction.abort().unwrap().outcome, AbortOutcome::Aborted);
    assert_eq!(fs::metadata(&files.main).unwrap().len(), retained_capacity);

    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(30), Ipv4Key(40), 9).unwrap();
    assert_eq!(fs::metadata(&files.main).unwrap().len(), retained_capacity);
    let committed = transaction.commit().unwrap();
    assert_eq!(committed.durability, CommitDurability::Committed);
    let mut current = LiveReader::open(&files.main, &cancellation).unwrap();
    let committed_length = current.info().unwrap().page_count * 4096;
    current.close().unwrap();
    assert!(committed_length < retained_capacity);
    assert_eq!(fs::metadata(&files.main).unwrap().len(), retained_capacity);
    assert_eq!(writer.close().unwrap().outcome, CloseOutcome::Closed);
    assert_eq!(fs::metadata(&files.main).unwrap().len(), retained_capacity);

    let mut findings = Vec::new();
    let validated = validate(
        &files.main,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(1024 * 1024, 2),
        &cancellation,
        &mut |finding: &iprange_livedb::validation::ValidationFinding| {
            findings.push(*finding);
            Ok(ValidationSinkControl::Continue)
        },
    )
    .unwrap();
    assert!(validated.valid, "validation findings: {findings:?}");
    assert!(findings.is_empty());
    assert_eq!(
        validated.generation.unwrap().page_count * 4096,
        committed_length
    );

    pinned.close().unwrap();
    let mut writer = LiveWriter::open(&files.main, bounded, &cancellation).unwrap();
    assert_eq!(fs::metadata(&files.main).unwrap().len(), committed_length);
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(50), Ipv4Key(60), 11).unwrap();
    assert_eq!(
        transaction.commit().unwrap().durability,
        CommitDurability::Committed
    );
    writer.close().unwrap();

    let physical_length = fs::metadata(&files.main).unwrap().len();
    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    assert_eq!(physical_length, reader.info().unwrap().page_count * 4096);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(35)).unwrap(), Some(9));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(55)).unwrap(), Some(11));
    reader.close().unwrap();
}

#[cfg(unix)]
#[test]
fn failed_abort_is_close_only_and_retries_the_exact_tail_cleanup() {
    let files = TestPair::new("abort-retry");
    create(&files);
    let committed_length = fs::metadata(&files.main).unwrap().len();
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    drop(transaction);

    let file = fs::OpenOptions::new()
        .write(true)
        .open(&files.main)
        .unwrap();
    file.set_len(committed_length + 4096).unwrap();
    drop(file);
    fs::rename(&files.main, files.saved_main()).unwrap();

    let aborted = writer.abort().unwrap();
    assert_eq!(aborted.outcome, AbortOutcome::AbortIncomplete);
    assert_eq!(aborted.cleanup.len(), 1);
    let tail = aborted.cleanup.get(0).unwrap();
    assert_eq!(tail.committed_target_length, committed_length);
    assert_eq!(
        tail.observed_tail_end_exclusive,
        Some(committed_length + 4096)
    );
    assert!(matches!(
        writer.begin_direct_transaction(&CancellationToken::new()),
        Err(Error::WrongMode(_))
    ));

    fs::rename(files.saved_main(), &files.main).unwrap();
    let closed = writer.close().unwrap();
    assert_eq!(closed.outcome, CloseOutcome::Closed);
    assert_eq!(closed.abort_outcome, Some(AbortOutcome::Aborted));
    assert!(closed.cleanup.is_empty());
    assert_eq!(fs::metadata(&files.main).unwrap().len(), committed_length);
}

#[test]
#[cfg(not(windows))]
fn failed_abort_never_extends_a_shortened_committed_file() {
    let files = TestPair::new("abort-short-file");
    create(&files);
    let committed = fs::read(&files.main).unwrap();
    let committed_length = committed.len() as u64;
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    drop(transaction);

    let shortened_length = committed_length - 4096;
    fs::OpenOptions::new()
        .write(true)
        .open(&files.main)
        .unwrap()
        .set_len(shortened_length)
        .unwrap();

    let closed = writer.close().unwrap();
    assert_eq!(closed.outcome, CloseOutcome::CloseIncomplete);
    assert_eq!(closed.abort_outcome, Some(AbortOutcome::AbortIncomplete));
    assert!(closed.cleanup.is_empty());
    assert_eq!(fs::metadata(&files.main).unwrap().len(), shortened_length);

    fs::write(&files.main, committed).unwrap();
    let closed = writer.close().unwrap();
    assert_eq!(closed.outcome, CloseOutcome::Closed);
    assert_eq!(closed.abort_outcome, Some(AbortOutcome::Aborted));
}

#[test]
fn commit_resolution_removes_only_the_unpublished_tail() {
    let files = TestPair::new("resolve-tail");
    create(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let committed = commit_one(&mut writer, 7);
    writer.close().unwrap();

    let committed_length = fs::metadata(&files.main).unwrap().len();
    let file = fs::OpenOptions::new()
        .write(true)
        .open(&files.main)
        .unwrap();
    file.set_len(committed_length + 4096).unwrap();
    file.sync_all().unwrap();
    drop(file);

    let resolved = resolve_commit(
        &files.main,
        &committed,
        CommitResolutionMode::Live,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.resolution, CommitResolution::Committed);
    assert!(resolved.cleanup.is_empty());
    assert_eq!(fs::metadata(&files.main).unwrap().len(), committed_length);

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(7)).unwrap(), Some(7));
    reader.close().unwrap();
}
