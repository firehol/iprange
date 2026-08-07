#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, CommitDurability, FeedName, LiveReader,
    LiveWriter, TransactionBudget, ValueKind, ValueTag,
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
                "iprange-v4-catalog-{label}-{}-{unique}",
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
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn name(value: &str) -> FeedName {
    FeedName::new(value).unwrap()
}

fn generated(index: u32) -> FeedName {
    name(&format!(
        "feed{index:04}{}z",
        "x".repeat((index as usize * 17) % 220)
    ))
}

#[test]
fn transaction_builds_splits_renames_and_reopens_catalog() {
    let files = TestPair::new("roundtrip");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();

    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        for index in (0..1_000).rev() {
            transaction.ensure_feed(generated(index)).unwrap();
        }
        let selected = transaction.lookup_feed(generated(500)).unwrap().unwrap();
        assert_eq!(selected.name(), generated(500));
        let renamed = transaction
            .rename_feed(selected, name("renamed500"))
            .unwrap();
        assert_eq!(renamed.name(), name("renamed500"));
        let committed = transaction.commit().unwrap();
        assert_eq!(committed.durability, CommitDurability::Committed);
    }

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(reader
        .lookup_feed(generated(500).as_str())
        .unwrap()
        .is_none());
    assert!(reader.lookup_feed("renamed500").unwrap().is_some());
    let mut cursor = reader.feed_cursor().unwrap();
    let mut count = 0;
    let mut previous = None;
    while let Some(entry) = cursor.next_feed().unwrap() {
        assert!(previous.map_or(true, |prior| prior < entry.index));
        previous = Some(entry.index);
        count += 1;
    }
    assert_eq!(count, 1_000);
    reader.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn dropped_transaction_requires_explicit_writer_abort() {
    let files = TestPair::new("drop-abort");
    create_live(
        &files.main,
        AddressFamily::Ipv6,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();

    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        transaction.ensure_feed(name("temporary")).unwrap();
    }
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(iprange_livedb::Error::WrongState(_))
    ));
    assert_eq!(
        writer.abort().unwrap().outcome,
        iprange_livedb::AbortOutcome::Aborted
    );
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(reader.lookup_feed("temporary").unwrap().is_none());
    reader.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn unused_membership_builder_commit_cleans_the_draft() {
    let files = TestPair::new("unused-builder");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();

    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        transaction.ensure_feed(name("existing")).unwrap();
        transaction.commit().unwrap();
    }
    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        let feed = transaction.lookup_feed(name("existing")).unwrap().unwrap();
        let empty = transaction.empty_membership().unwrap();
        let _unused = transaction.add_feed(empty, feed).unwrap();
        assert!(matches!(
            transaction.commit(),
            Err(iprange_livedb::Error::NoPendingTransaction)
        ));
    }
    assert!(writer
        .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
        .unwrap()
        .abort()
        .is_ok());
    writer.close().unwrap();
}
