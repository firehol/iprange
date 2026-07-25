use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, CommitDurability, Error, FeedName, Ipv4Key,
    LiveReader, LiveWriter, MembershipOperation, TransactionBudget, ValueKind, ValueTag,
};

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-membership-{}-{unique}",
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

fn feed_name(name: &str) -> FeedName {
    FeedName::new(name).unwrap()
}

#[test]
fn membership_algebra_commits_canonical_ranges_and_reclaims_unused_values() {
    let files = TestPair::new();
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
        let feed_a = transaction.ensure_feed(feed_name("a")).unwrap();
        let feed_b = transaction.ensure_feed(feed_name("b")).unwrap();
        let feed_c = transaction.ensure_feed(feed_name("c")).unwrap();
        let mut names = Vec::new();
        {
            let mut cursor = transaction.feed_cursor().unwrap();
            while let Some(feed) = cursor.next_feed().unwrap() {
                names.push(feed.name().as_str().to_owned());
            }
        }
        assert_eq!(names, ["a", "b", "c"]);
        let empty = transaction.empty_membership().unwrap();
        let a = transaction.add_feed(empty, feed_a).unwrap();
        let b = transaction.add_feed(empty, feed_b).unwrap();
        let ab = transaction.add_feed(a, feed_b).unwrap();
        let only_b = transaction.add_feed(empty, feed_b).unwrap();
        let ba = transaction.add_feed(only_b, feed_a).unwrap();
        let _unused_c = transaction.add_feed(empty, feed_c).unwrap();

        transaction
            .apply_v4(Ipv4Key(0), Ipv4Key(99), a, MembershipOperation::Replace)
            .unwrap();
        transaction
            .apply_v4(Ipv4Key(20), Ipv4Key(79), b, MembershipOperation::Union)
            .unwrap();
        assert!(!transaction
            .apply_v4(Ipv4Key(20), Ipv4Key(39), ba, MembershipOperation::Union,)
            .unwrap());
        transaction
            .apply_v4(Ipv4Key(40), Ipv4Key(59), a, MembershipOperation::Xor)
            .unwrap();
        transaction
            .apply_v4(Ipv4Key(50), Ipv4Key(69), b, MembershipOperation::Difference)
            .unwrap();
        transaction
            .apply_v4(
                Ipv4Key(10),
                Ipv4Key(89),
                b,
                MembershipOperation::Intersection,
            )
            .unwrap();
        assert_eq!(ab, ba);
        let committed = transaction.commit().unwrap();
        assert_eq!(committed.durability, CommitDurability::Committed);
    }

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    let index_a = reader.lookup_feed("a").unwrap().unwrap().index;
    let index_b = reader.lookup_feed("b").unwrap().unwrap().index;
    for (address, expected) in [
        (0, Some((true, false))),
        (9, Some((true, false))),
        (10, None),
        (20, Some((false, true))),
        (49, Some((false, true))),
        (50, None),
        (70, Some((false, true))),
        (79, Some((false, true))),
        (80, None),
        (90, Some((true, false))),
        (99, Some((true, false))),
        (100, None),
    ] {
        let membership = reader.lookup_membership_v4(Ipv4Key(address)).unwrap();
        match (membership, expected) {
            (None, None) => {}
            (Some(membership), Some((a, b))) => {
                assert_eq!(membership.contains_index(index_a).unwrap(), a);
                assert_eq!(membership.contains_index(index_b).unwrap(), b);
            }
            _ => panic!("wrong membership at {address}"),
        }
    }
    assert_eq!(reader.info().unwrap().range_record_count, 4);
    reader.close().unwrap();

    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        let feed_b = transaction.lookup_feed(feed_name("b")).unwrap().unwrap();
        let empty = transaction.empty_membership().unwrap();
        let membership_b = transaction.add_feed(empty, feed_b).unwrap();
        transaction.delete_feed(feed_b).unwrap();
        assert!(matches!(
            transaction.apply_v4(
                Ipv4Key(0),
                Ipv4Key(0),
                membership_b,
                MembershipOperation::Union,
            ),
            Err(Error::StaleReference)
        ));
        assert_eq!(
            transaction.commit().unwrap().durability,
            CommitDurability::Committed
        );
    }
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(reader.lookup_feed("b").unwrap().is_none());
    assert_eq!(reader.info().unwrap().range_record_count, 2);
    assert!(reader.lookup_membership_v4(Ipv4Key(20)).unwrap().is_none());
    assert!(reader.lookup_membership_v4(Ipv4Key(90)).unwrap().is_some());
    reader.close().unwrap();

    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        let reused = transaction.ensure_feed(feed_name("d")).unwrap();
        assert_eq!(reused.name(), feed_name("d"));
        assert_eq!(
            transaction.commit().unwrap().durability,
            CommitDurability::Committed
        );
    }
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_feed("d").unwrap().unwrap().index, index_b);
    reader.close().unwrap();

    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        let empty = transaction.empty_membership().unwrap();
        transaction
            .apply_v4(Ipv4Key(0), Ipv4Key(99), empty, MembershipOperation::Replace)
            .unwrap();
        assert_eq!(
            transaction.commit().unwrap().durability,
            CommitDurability::Committed
        );
    }
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().range_record_count, 0);
    assert!(reader.lookup_membership_v4(Ipv4Key(20)).unwrap().is_none());
    reader.close().unwrap();
    writer.close().unwrap();
}
