use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, CommitDurability, Error, FeedName, Ipv4Key,
    LiveReader, LiveWriter, TransactionBudget, ValueKind, ValueTag,
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
                "iprange-v4-advanced-{label}-{}-{unique}",
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

#[test]
fn direct_transaction_applies_every_interval_in_arrival_order() {
    let files = TestPair::new("arrival");
    create(&files);
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();

    transaction.assign_v4(Ipv4Key(0), Ipv4Key(100), 1).unwrap();
    transaction.assign_v4(Ipv4Key(20), Ipv4Key(30), 2).unwrap();
    transaction.clear_v4(Ipv4Key(25), Ipv4Key(27)).unwrap();
    transaction.set_metadata_json(br#"{"round":1}"#).unwrap();
    let result = transaction.commit().unwrap();
    assert_eq!(result.durability, CommitDurability::Committed);

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(19)).unwrap(), Some(1));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(24)).unwrap(), Some(2));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(26)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(28)).unwrap(), Some(2));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(31)).unwrap(), Some(1));
    assert_eq!(reader.metadata_json().unwrap().unwrap(), br#"{"round":1}"#);
    reader.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn stored_cancellation_aborts_the_complete_direct_draft() {
    let files = TestPair::new("cancel");
    create(&files);
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();

    token.cancel();
    assert!(matches!(
        transaction.assign_v4(Ipv4Key(30), Ipv4Key(40), 8),
        Err(Error::TransactionAborted(_))
    ));
    drop(transaction);
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), None);
    reader.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn stored_cancellation_aborts_the_complete_membership_draft() {
    let files = TestPair::new("membership-cancel");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut transaction = writer.begin_membership_transaction(&token).unwrap();
    transaction
        .ensure_feed(FeedName::new("one").unwrap())
        .unwrap();

    token.cancel();
    assert!(matches!(
        transaction.ensure_feed(FeedName::new("two").unwrap()),
        Err(Error::TransactionAborted(_))
    ));
    drop(transaction);
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().active_feed_count, 0);
    reader.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn cancellation_at_commit_returns_factual_noncommit_and_cleans_the_draft() {
    let files = TestPair::new("commit-cancel");
    create(&files);
    let token = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &token).unwrap();
    let mut transaction = writer.begin_direct_transaction(&token).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    token.cancel();

    let result = transaction.commit().unwrap();
    assert_eq!(
        result.durability,
        iprange_livedb::CommitDurability::NotCommitted
    );
    assert!(matches!(
        result.cause,
        Some(Error::TransactionAborted(cause)) if matches!(*cause, Error::Cancelled)
    ));
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 1);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), None);
    reader.close().unwrap();
    writer.close().unwrap();
}
