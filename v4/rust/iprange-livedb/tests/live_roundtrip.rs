#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, CommitDurability, CreationState, Error,
    ImmutableReader, Ipv4Key, Ipv6Key, LiveReader, LiveWriter, ReclaimResult, TransactionBudget,
    ValueKind, ValueTag,
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
                "iprange-v4-live-{label}-{}-{unique}",
                std::process::id()
            )),
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

#[test]
fn creation_failure_before_artifacts_is_reported_without_residue() {
    let files = TestPair::new("create-failure");
    let path = files.main.join("missing").join("database");
    let result = create_live(
        &path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(result.state, CreationState::NotCreated);
    assert!(!result.residue_possible);
    assert!(matches!(result.cause, Some(Error::Io(_))));
    assert!(!path.exists());
}

#[test]
fn live_generations_are_atomic_and_old_readers_stay_pinned() {
    let files = TestPair::new("generations");
    let created = create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(created.state, CreationState::Created);
    assert!(!created.residue_possible);
    assert!(ImmutableReader::open(&files.main).is_err());

    let mut old = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(old.info().unwrap().transaction_id, 1);

    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    assert!(matches!(
        LiveWriter::open(&files.main, budget(), &CancellationToken::new()),
        Err(Error::WriterBusy)
    ));
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(30), 1).unwrap();
    transaction.assign_v4(Ipv4Key(20), Ipv4Key(25), 2).unwrap();
    transaction.assign_v4(Ipv4Key(22), Ipv4Key(23), 3).unwrap();
    let committed = transaction.commit().unwrap();
    assert_eq!(committed.durability, CommitDurability::Committed);
    assert_eq!(committed.attempted_transaction_id, 2);
    assert!(committed.cause.is_none());

    assert_eq!(old.lookup_direct_v4(Ipv4Key(22)).unwrap(), None);
    let mut current = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(current.info().unwrap().transaction_id, 2);
    assert_eq!(current.lookup_direct_v4(Ipv4Key(19)).unwrap(), Some(1));
    assert_eq!(current.lookup_direct_v4(Ipv4Key(21)).unwrap(), Some(2));
    assert_eq!(current.lookup_direct_v4(Ipv4Key(22)).unwrap(), Some(3));
    assert!(matches!(
        LiveReader::open(&files.main, &CancellationToken::new()),
        Err(Error::ReaderCapacityExhausted)
    ));

    old.close().unwrap();
    let mut reused = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    reused.close().unwrap();
    current.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn abort_and_noop_never_publish_a_generation() {
    let files = TestPair::new("abort");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();

    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    assert!(!transaction.clear_v4(Ipv4Key(1), Ipv4Key(2)).unwrap());
    assert!(matches!(
        transaction.commit(),
        Err(Error::NoPendingTransaction)
    ));
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(1), Ipv4Key(2), 7).unwrap();
    assert_eq!(
        transaction.abort().unwrap().outcome,
        iprange_livedb::AbortOutcome::Aborted
    );
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(1)).unwrap(), None);
    reader.close().unwrap();
}

#[test]
fn full_ipv6_space_round_trips_without_endpoint_overflow() {
    let files = TestPair::new("ipv6");
    create_live(
        &files.main,
        AddressFamily::Ipv6,
        ValueKind::Direct,
        ValueTag::new(b"geo").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction
        .assign_v6(Ipv6Key::from_u128(0), Ipv6Key::from_u128(u128::MAX), 9)
        .unwrap();
    assert_eq!(
        transaction.commit().unwrap().durability,
        CommitDurability::Committed
    );
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(
        reader
            .lookup_direct_v6(Ipv6Key::from_u128(u128::MAX))
            .unwrap(),
        Some(9)
    );
    reader.close().unwrap();
}

#[test]
fn reclamation_waits_for_old_readers_then_auto_publishes() {
    let files = TestPair::new("reclaim");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    transaction.commit().unwrap();

    let mut pinned = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(pinned.info().unwrap().transaction_id, 2);
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(12), Ipv4Key(18), 2).unwrap();
    transaction.commit().unwrap();

    assert!(matches!(
        writer
            .reclaim(10, 10_000, &CancellationToken::new())
            .unwrap(),
        ReclaimResult::NoChange
    ));
    pinned.close().unwrap();

    let reclaimed = writer
        .reclaim(10, 10_000, &CancellationToken::new())
        .unwrap();
    match reclaimed {
        ReclaimResult::NoChange => panic!("released reader left reclamation blocked"),
        ReclaimResult::Commit {
            transaction_count,
            page_count,
            commit,
        } => {
            assert_eq!(transaction_count, 1);
            assert!(page_count > 0);
            assert_eq!(commit.attempted_transaction_id, 4);
            assert_eq!(commit.durability, CommitDurability::Committed);
        }
    }
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(11)).unwrap(), Some(1));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(2));
    reader.close().unwrap();
}

#[test]
fn safe_reclaimed_pages_are_reused_while_a_newer_reader_is_pinned() {
    let files = TestPair::new("reclaim-with-reader");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();

    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    for address in (0..1_024).step_by(2) {
        transaction
            .assign_v4(Ipv4Key(address), Ipv4Key(address), 1)
            .unwrap();
    }
    transaction.commit().unwrap();

    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.clear_v4(Ipv4Key(0), Ipv4Key(1_023)).unwrap();
    transaction.commit().unwrap();

    let mut pinned = LiveReader::open(&files.main, &cancellation).unwrap();
    assert_eq!(pinned.info().unwrap().transaction_id, 3);
    let reclaimed = writer.reclaim(10, 10_000, &cancellation).unwrap();
    assert!(matches!(
        reclaimed,
        ReclaimResult::Commit {
            transaction_count: 1,
            page_count: 2..,
            ..
        }
    ));
    writer.close().unwrap();

    let mut reuse_only = budget();
    reuse_only.max_file_growth_pages = 1;
    let mut writer = LiveWriter::open(&files.main, reuse_only, &cancellation).unwrap();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    for address in (2_000..3_024).step_by(2) {
        transaction
            .assign_v4(Ipv4Key(address), Ipv4Key(address), 9)
            .unwrap();
    }
    let commit = transaction.commit().unwrap();
    assert_eq!(commit.durability, CommitDurability::Committed, "{commit:?}");
    writer.close().unwrap();

    assert_eq!(pinned.lookup_direct_v4(Ipv4Key(2_150)).unwrap(), None);
    pinned.close().unwrap();
    let mut current = LiveReader::open(&files.main, &cancellation).unwrap();
    assert_eq!(current.lookup_direct_v4(Ipv4Key(2_150)).unwrap(), Some(9));
    current.close().unwrap();
}

#[test]
fn failed_reclamation_discards_its_complete_private_draft() {
    let files = TestPair::new("reclaim-abort");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    transaction.commit().unwrap();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(12), Ipv4Key(18), 2).unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();

    let mut tiny = budget();
    tiny.max_private_pages = 1;
    let mut writer = LiveWriter::open(&files.main, tiny, &CancellationToken::new()).unwrap();
    assert!(matches!(
        writer.reclaim(10, 10_000, &CancellationToken::new()),
        Err(Error::TransactionAborted(_))
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 3);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(11)).unwrap(), Some(1));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(2));
    reader.close().unwrap();

    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    assert!(matches!(
        writer
            .reclaim(10, 10_000, &CancellationToken::new())
            .unwrap(),
        ReclaimResult::Commit { .. }
    ));
    writer.close().unwrap();
}

#[test]
fn cancelled_reclamation_leaves_the_committed_generation_unchanged() {
    let files = TestPair::new("reclaim-cancel");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &token).unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    transaction.commit().unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(12), Ipv4Key(18), 2).unwrap();
    transaction.commit().unwrap();

    let cancelled = CancellationToken::new();
    cancelled.cancel();
    assert!(matches!(
        writer.reclaim(10, 10_000, &cancelled),
        Err(Error::Cancelled)
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 3);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(11)).unwrap(), Some(1));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(2));
    reader.close().unwrap();
}

#[test]
fn metadata_is_atomic_exact_and_visible_to_the_staging_writer() {
    let files = TestPair::new("metadata");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut old = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(old.metadata_json_len().unwrap(), None);

    let payload = b"{ definitely not required to be valid JSON }\n";
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let token = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    assert!(transaction.set_metadata_json(payload).unwrap());
    assert_eq!(
        transaction.metadata_json_len().unwrap(),
        Some(payload.len() as u64)
    );
    assert_eq!(
        transaction.metadata_json().unwrap().as_deref(),
        Some(&payload[..])
    );

    let mut too_small = vec![0x55; payload.len() - 1];
    assert!(matches!(
        transaction.read_metadata_json(&mut too_small),
        Err(Error::BufferTooSmall { required }) if required == payload.len() as u64
    ));
    assert!(too_small.iter().all(|&byte| byte == 0x55));
    assert!(matches!(
        transaction.clear_metadata_json(),
        Err(Error::WrongState(_))
    ));
    assert!(matches!(
        transaction.assign_v4(Ipv4Key(30), Ipv4Key(40), 9),
        Err(Error::WrongState(_))
    ));

    let commit = transaction.commit().unwrap();
    assert_eq!(commit.durability, CommitDurability::Committed);
    assert_eq!(old.metadata_json_len().unwrap(), None);

    let mut current = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(
        current.metadata_json().unwrap().as_deref(),
        Some(&payload[..])
    );
    assert_eq!(current.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(7));
    old.close().unwrap();
    current.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn equal_replacement_and_clear_have_the_exact_generation_semantics() {
    let files = TestPair::new("metadata-replace");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();

    assert!(!writer.clear_metadata_json(&cancellation).unwrap());
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));
    assert!(writer.set_metadata_json(b"", &cancellation).unwrap());
    assert_eq!(writer.metadata_json().unwrap(), Some(Vec::new()));
    assert_eq!(
        writer
            .commit(&CancellationToken::new())
            .unwrap()
            .attempted_transaction_id,
        2
    );

    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(1), Ipv4Key(2), 9).unwrap();
    assert_eq!(transaction.metadata_json().unwrap(), Some(Vec::new()));
    assert_eq!(
        transaction.abort().unwrap().outcome,
        iprange_livedb::AbortOutcome::Aborted
    );

    assert!(writer.set_metadata_json(b"", &cancellation).unwrap());
    assert_eq!(
        writer
            .commit(&CancellationToken::new())
            .unwrap()
            .attempted_transaction_id,
        3
    );
    assert!(writer.clear_metadata_json(&cancellation).unwrap());
    assert_eq!(writer.metadata_json_len().unwrap(), None);
    assert_eq!(
        writer
            .commit(&CancellationToken::new())
            .unwrap()
            .attempted_transaction_id,
        4
    );
    assert!(!writer.clear_metadata_json(&cancellation).unwrap());
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 4);
    assert_eq!(reader.metadata_json().unwrap(), None);
    reader.close().unwrap();
}

#[test]
fn metadata_resource_failure_aborts_all_earlier_draft_changes() {
    let files = TestPair::new("metadata-budget");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut tiny = budget();
    tiny.max_heap_bytes = 1;
    let mut writer = LiveWriter::open(&files.main, tiny, &CancellationToken::new()).unwrap();
    let token = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    assert!(matches!(
        transaction.set_metadata_json(b"x"),
        Err(Error::TransactionAborted(cause))
            if matches!(*cause, Error::BudgetExceeded(_))
    ));
    drop(transaction);
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), None);
    assert_eq!(reader.metadata_json().unwrap(), None);
    reader.close().unwrap();
}
