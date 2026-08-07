#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, CommitDurability, Error, FeedName,
    FinishedWorkflow, Ipv4Key, Ipv6Key, LiveReader, LiveWriter, MembershipOperation,
    RangeDirection, TransactionBudget, ValueKind, ValueTag, MAX_METADATA_UNCOMPRESSED,
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
                "iprange-v4-feed-lifecycle-{label}-{}-{unique}",
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

fn range(from: u32, to: u32) -> AddressRange<Ipv4Key> {
    AddressRange {
        from: Ipv4Key(from),
        to: Ipv4Key(to),
    }
}

fn commit_created_feed(writer: &mut LiveWriter, feed: &str) {
    let cancellation = CancellationToken::new();
    let workflow = writer.begin_create_feed(name(feed), &cancellation).unwrap();
    match workflow.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            assert_eq!(
                prepared.commit().unwrap().durability,
                CommitDurability::Committed
            );
        }
        FinishedWorkflow::NoChange(_) => panic!("feed creation cannot be a no-op"),
    }
}

#[test]
fn rename_and_delete_preserve_other_feeds_and_reuse_the_committed_index() {
    let files = TestPair::new("roundtrip");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        4,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        let alpha = transaction.ensure_feed(name("alpha")).unwrap();
        let beta = transaction.ensure_feed(name("beta")).unwrap();
        transaction.ensure_feed(name("empty")).unwrap();
        let empty = transaction.empty_membership().unwrap();
        let alpha_member = transaction.add_feed(empty, alpha).unwrap();
        let beta_member = transaction.add_feed(empty, beta).unwrap();
        transaction
            .apply_v4(
                Ipv4Key(0),
                Ipv4Key(9),
                alpha_member,
                MembershipOperation::Union,
            )
            .unwrap();
        transaction
            .apply_v4(
                Ipv4Key(5),
                Ipv4Key(14),
                beta_member,
                MembershipOperation::Union,
            )
            .unwrap();
        transaction.commit().unwrap();
    }

    let mut old_reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    let alpha_index = old_reader.lookup_feed("alpha").unwrap().unwrap().index;
    let beta_index = old_reader.lookup_feed("beta").unwrap().unwrap().index;

    let cancellation = CancellationToken::new();
    let mut rename = writer
        .rename_feed(name("alpha"), name("renamed"), &cancellation)
        .unwrap();
    assert!(rename.set_metadata_json(b"{\"feed\":\"renamed\"}").unwrap());
    assert_eq!(
        rename.commit().unwrap().durability,
        CommitDurability::Committed
    );

    let mut renamed_reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(renamed_reader.lookup_feed("alpha").unwrap().is_none());
    assert_eq!(
        renamed_reader
            .lookup_feed("renamed")
            .unwrap()
            .unwrap()
            .index,
        alpha_index
    );
    assert_eq!(
        renamed_reader.lookup_feed("beta").unwrap().unwrap().index,
        beta_index
    );
    let overlap = renamed_reader
        .lookup_membership_v4(Ipv4Key(7))
        .unwrap()
        .unwrap();
    assert!(overlap.contains_index(alpha_index).unwrap());
    assert!(overlap.contains_index(beta_index).unwrap());
    assert_eq!(
        renamed_reader.metadata_json().unwrap().as_deref(),
        Some(&b"{\"feed\":\"renamed\"}"[..])
    );

    assert!(old_reader.lookup_feed("renamed").unwrap().is_none());
    assert_eq!(
        old_reader.lookup_feed("alpha").unwrap().unwrap().index,
        alpha_index
    );
    let old_membership = old_reader
        .lookup_membership_v4(Ipv4Key(2))
        .unwrap()
        .unwrap();
    assert!(old_membership.contains_index(alpha_index).unwrap());
    old_reader.close().unwrap();
    renamed_reader.close().unwrap();

    let mut delete = writer.delete_feed(name("renamed"), &cancellation).unwrap();
    assert!(delete.clear_metadata_json().unwrap());
    assert_eq!(
        delete.commit().unwrap().durability,
        CommitDurability::Committed
    );

    let mut deleted_reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(deleted_reader.lookup_feed("renamed").unwrap().is_none());
    assert!(deleted_reader
        .lookup_membership_v4(Ipv4Key(0))
        .unwrap()
        .is_none());
    let beta_only = deleted_reader
        .lookup_membership_v4(Ipv4Key(7))
        .unwrap()
        .unwrap();
    assert!(!beta_only.contains_index(alpha_index).unwrap());
    assert!(beta_only.contains_index(beta_index).unwrap());
    let mut beta_ranges = deleted_reader
        .feed_range_cursor_v4("beta", RangeDirection::Forward)
        .unwrap();
    assert_eq!(beta_ranges.next_range().unwrap(), Some(range(5, 14)));
    assert_eq!(beta_ranges.next_range().unwrap(), None);
    assert_eq!(deleted_reader.metadata_json_len().unwrap(), None);
    deleted_reader.close().unwrap();

    commit_created_feed(&mut writer, "reused");
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(
        reader.lookup_feed("reused").unwrap().unwrap().index,
        alpha_index
    );
    reader.close().unwrap();

    assert_eq!(
        writer
            .delete_feed(name("empty"), &cancellation)
            .unwrap()
            .commit()
            .unwrap()
            .durability,
        CommitDurability::Committed
    );
    writer.close().unwrap();
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(reader.lookup_feed("empty").unwrap().is_none());
    assert!(reader.lookup_feed("beta").unwrap().is_some());
    reader.close().unwrap();
}

#[test]
fn lifecycle_preconditions_and_precancellation_leave_the_writer_clean() {
    let files = TestPair::new("preconditions");
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
    commit_created_feed(&mut writer, "alpha");
    commit_created_feed(&mut writer, "beta");
    let cancellation = CancellationToken::new();

    assert!(matches!(
        writer.delete_feed(name("missing"), &cancellation),
        Err(Error::NameNotFound)
    ));
    assert!(matches!(
        writer.rename_feed(name("missing"), name("unused"), &cancellation),
        Err(Error::NameNotFound)
    ));
    assert!(matches!(
        writer.rename_feed(name("alpha"), name("beta"), &cancellation),
        Err(Error::NameExists)
    ));
    assert!(matches!(
        writer.rename_feed(name("alpha"), name("alpha"), &cancellation),
        Err(Error::NameExists)
    ));

    let cancelled = CancellationToken::new();
    cancelled.cancel();
    assert!(matches!(
        writer.delete_feed(name("alpha"), &cancelled),
        Err(Error::Cancelled)
    ));
    assert!(matches!(
        writer.rename_feed(name("alpha"), name("unused"), &cancelled),
        Err(Error::Cancelled)
    ));
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));
    writer.close().unwrap();
}

#[test]
fn lifecycle_failure_or_dropped_handle_cannot_publish_partial_state() {
    let files = TestPair::new("abort");
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
    commit_created_feed(&mut writer, "alpha");

    let cancellation = CancellationToken::new();
    drop(
        writer
            .rename_feed(name("alpha"), name("dropped"), &cancellation)
            .unwrap(),
    );
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::WrongState(_))
    ));
    assert!(matches!(
        writer.set_metadata_json(b"{}", &CancellationToken::new()),
        Err(Error::WrongState(_))
    ));
    assert_eq!(
        writer.abort().unwrap().outcome,
        iprange_livedb::AbortOutcome::Aborted
    );

    let mut oversized = writer
        .rename_feed(name("alpha"), name("oversized"), &cancellation)
        .unwrap();
    let invalid_metadata = vec![b'x'; MAX_METADATA_UNCOMPRESSED as usize + 1];
    assert!(matches!(
        oversized.set_metadata_json(&invalid_metadata),
        Err(Error::TransactionAborted(cause))
            if matches!(*cause, Error::InvalidArgument("metadata exceeds 1 MiB"))
    ));
    drop(oversized);
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let cancelled = CancellationToken::new();
    let delete = writer.delete_feed(name("alpha"), &cancelled).unwrap();
    cancelled.cancel();
    let result = delete.commit().unwrap();
    assert_eq!(result.durability, CommitDurability::NotCommitted);
    assert!(matches!(
        result.cause,
        Some(Error::TransactionAborted(cause)) if matches!(*cause, Error::Cancelled)
    ));
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    writer.close().unwrap();
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(reader.lookup_feed("alpha").unwrap().is_some());
    for absent in ["dropped", "oversized"] {
        assert!(reader.lookup_feed(absent).unwrap().is_none());
    }
    reader.close().unwrap();
}

#[test]
fn deleting_a_full_ipv6_feed_handles_the_complete_address_space() {
    let files = TestPair::new("ipv6");
    create_live(
        &files.main,
        AddressFamily::Ipv6,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut create = writer
        .begin_create_feed(name("all"), &cancellation)
        .unwrap();
    create
        .add_ranges_v6_slice(&[AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        }])
        .unwrap();
    match create.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("feed creation cannot be a no-op"),
    }

    assert_eq!(
        writer
            .delete_feed(name("all"), &cancellation)
            .unwrap()
            .commit()
            .unwrap()
            .durability,
        CommitDurability::Committed
    );
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(reader.lookup_feed("all").unwrap().is_none());
    assert_eq!(reader.info().unwrap().range_record_count, 0);
    assert!(reader.lookup_membership_v6(Ipv6Key::MIN).unwrap().is_none());
    assert!(reader.lookup_membership_v6(Ipv6Key::MAX).unwrap().is_none());
    reader.close().unwrap();
}

#[test]
fn direct_database_rejects_named_feed_lifecycle_operations() {
    let files = TestPair::new("wrong-mode");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"timestamp").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    assert!(matches!(
        writer.delete_feed(name("alpha"), &cancellation),
        Err(Error::WrongValueKind(_))
    ));
    assert!(matches!(
        writer.rename_feed(name("alpha"), name("beta"), &cancellation),
        Err(Error::WrongValueKind(_))
    ));
    writer.close().unwrap();
}
