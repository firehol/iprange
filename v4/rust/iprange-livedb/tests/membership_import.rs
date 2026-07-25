use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, Cardinality129, CommitDurability, Error,
    FeedName, FinishedWorkflow, ImmutableReader, Ipv4Key, Ipv6Key, LiveReader, LiveWriter,
    LogicalChange, MembershipImportSource, MembershipOperation, RangeDirection, TransactionBudget,
    ValueKind, ValueTag, WorkflowKind,
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
                "iprange-v4-import-{label}-{}-{unique}",
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

fn create_membership(files: &TestPair, family: AddressFamily, tag: &[u8]) {
    let result = create_live(
        &files.main,
        family,
        ValueKind::Membership,
        ValueTag::new(tag).unwrap(),
        4,
    )
    .unwrap();
    assert_eq!(result.state, iprange_livedb::CreationState::Created);
}

fn changed(finished: FinishedWorkflow<'_>) -> iprange_livedb::PreparedWorkflow<'_> {
    match finished {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(report) => panic!("expected a change, got {report:?}"),
    }
}

#[test]
fn live_import_unions_names_preserves_destination_and_reports_exactly() {
    let source_files = TestPair::new("source");
    let destination_files = TestPair::new("destination");
    create_membership(&source_files, AddressFamily::Ipv4, b"membership");
    create_membership(&destination_files, AddressFamily::Ipv4, b"membership");

    let mut source_writer = LiveWriter::open(&source_files.main, budget()).unwrap();
    {
        let mut transaction = source_writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        let alpha = transaction.ensure_feed(name("alpha")).unwrap();
        let beta = transaction.ensure_feed(name("beta")).unwrap();
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
        transaction.set_metadata_json(b"source-only").unwrap();
        transaction.commit().unwrap();
    }
    source_writer.close().unwrap();

    let mut writer = LiveWriter::open(&destination_files.main, budget()).unwrap();
    {
        let mut transaction = writer
            .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
            .unwrap();
        let alpha = transaction.ensure_feed(name("alpha")).unwrap();
        let charlie = transaction.ensure_feed(name("charlie")).unwrap();
        let empty = transaction.empty_membership().unwrap();
        let alpha_member = transaction.add_feed(empty, alpha).unwrap();
        let charlie_member = transaction.add_feed(empty, charlie).unwrap();
        transaction
            .apply_v4(
                Ipv4Key(8),
                Ipv4Key(12),
                alpha_member,
                MembershipOperation::Union,
            )
            .unwrap();
        transaction
            .apply_v4(
                Ipv4Key(20),
                Ipv4Key(29),
                charlie_member,
                MembershipOperation::Union,
            )
            .unwrap();
        transaction.set_metadata_json(b"destination-old").unwrap();
        transaction.commit().unwrap();
    }

    let source = LiveReader::open(&source_files.main).unwrap();
    let old_destination = LiveReader::open(&destination_files.main).unwrap();
    let cancellation = CancellationToken::new();
    let import = writer
        .begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
        .unwrap();
    let mut prepared = changed(import.finish_input().unwrap());
    let report = *prepared.report();
    assert_eq!(report.workflow, WorkflowKind::MembershipImport);
    assert_eq!(report.logical_change, LogicalChange::Changed);
    assert_eq!(report.input_record_count, 3);
    assert_eq!(report.input_normalized_interval_count, 3);
    assert_eq!(report.before_range_record_count, 2);
    assert_eq!(report.after_range_record_count, 4);
    assert_eq!(report.input_addresses, Cardinality129::from_u64(15));
    assert_eq!(report.before_addresses, Cardinality129::from_u64(15));
    assert_eq!(report.after_addresses, Cardinality129::from_u64(25));
    assert_eq!(
        report.unchanged_value_addresses,
        Cardinality129::from_u64(10)
    );
    assert_eq!(report.changed_value_addresses, Cardinality129::from_u64(5));
    assert_eq!(report.added_addresses, Cardinality129::from_u64(10));
    assert_eq!(report.removed_addresses, Cardinality129::ZERO);
    assert_eq!(report.source_feed_count, 2);
    assert_eq!(report.matched_feed_count, 1);
    assert_eq!(report.created_feed_count, 1);
    assert_eq!(report.source_distinct_membership_count, 3);
    assert_eq!(report.translated_membership_count, 3);
    prepared.set_metadata_json(b"destination-new").unwrap();
    assert_eq!(
        prepared.commit().unwrap().durability,
        CommitDurability::Committed
    );

    assert!(old_destination.lookup_feed("beta").unwrap().is_none());
    assert_eq!(
        old_destination.metadata_json().unwrap().as_deref(),
        Some(&b"destination-old"[..])
    );
    old_destination.close().unwrap();
    source.close().unwrap();

    let reader = LiveReader::open(&destination_files.main).unwrap();
    let alpha = reader.lookup_feed("alpha").unwrap().unwrap().index;
    let beta = reader.lookup_feed("beta").unwrap().unwrap().index;
    let charlie = reader.lookup_feed("charlie").unwrap().unwrap().index;
    for (address, expected) in [
        (0, (true, false, false)),
        (4, (true, false, false)),
        (5, (true, true, false)),
        (12, (true, true, false)),
        (13, (false, true, false)),
        (14, (false, true, false)),
        (20, (false, false, true)),
        (29, (false, false, true)),
    ] {
        let membership = reader
            .lookup_membership_v4(Ipv4Key(address))
            .unwrap()
            .unwrap();
        assert_eq!(membership.contains_index(alpha).unwrap(), expected.0);
        assert_eq!(membership.contains_index(beta).unwrap(), expected.1);
        assert_eq!(membership.contains_index(charlie).unwrap(), expected.2);
    }
    assert_eq!(
        reader.metadata_json().unwrap().as_deref(),
        Some(&b"destination-new"[..])
    );
    reader.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn immutable_equal_import_is_a_clean_no_change() {
    let source_files = TestPair::new("immutable-copy");
    let destination_files = TestPair::new("copy-origin");
    create_membership(&destination_files, AddressFamily::Ipv4, b"membership");

    let mut writer = LiveWriter::open(&destination_files.main, budget()).unwrap();
    let cancellation = CancellationToken::new();
    let mut create = writer
        .begin_create_feed(name("alpha"), &cancellation)
        .unwrap();
    create
        .add_ranges_v4_slice(&[iprange_livedb::AddressRange {
            from: Ipv4Key(10),
            to: Ipv4Key(19),
        }])
        .unwrap();
    changed(create.finish_input().unwrap()).commit().unwrap();
    writer.close().unwrap();
    fs::copy(&destination_files.main, &source_files.main).unwrap();

    let source = ImmutableReader::open(&source_files.main).unwrap();
    let destination = LiveReader::open(&destination_files.main).unwrap();
    assert_eq!(
        source.info().database_id,
        destination.info().unwrap().database_id
    );
    destination.close().unwrap();
    let mut writer = LiveWriter::open(&destination_files.main, budget()).unwrap();
    let import = writer
        .begin_membership_import(MembershipImportSource::Immutable(&source), &cancellation)
        .unwrap();
    match import.finish_input().unwrap() {
        FinishedWorkflow::NoChange(report) => {
            assert_eq!(report.logical_change, LogicalChange::NoChange);
            assert_eq!(
                report.unchanged_value_addresses,
                Cardinality129::from_u64(10)
            );
            assert_eq!(report.source_feed_count, 1);
            assert_eq!(report.matched_feed_count, 1);
            assert_eq!(report.created_feed_count, 0);
            assert_eq!(report.source_distinct_membership_count, 1);
            assert_eq!(report.translated_membership_count, 1);
        }
        FinishedWorkflow::Changed(_) => panic!("equal import changed the destination"),
    }
    assert!(matches!(writer.commit(), Err(Error::NoPendingTransaction)));
    writer.close().unwrap();
}

#[test]
fn empty_feed_import_is_a_catalog_change_and_full_ipv6_is_exact() {
    let empty_source_files = TestPair::new("empty-source");
    let empty_destination_files = TestPair::new("empty-destination");
    create_membership(&empty_source_files, AddressFamily::Ipv4, b"membership");
    create_membership(&empty_destination_files, AddressFamily::Ipv4, b"membership");
    let cancellation = CancellationToken::new();
    let mut source_writer = LiveWriter::open(&empty_source_files.main, budget()).unwrap();
    changed(
        source_writer
            .begin_create_feed(name("empty"), &cancellation)
            .unwrap()
            .finish_input()
            .unwrap(),
    )
    .commit()
    .unwrap();
    source_writer.close().unwrap();
    let source = LiveReader::open(&empty_source_files.main).unwrap();
    let mut writer = LiveWriter::open(&empty_destination_files.main, budget()).unwrap();
    let prepared = changed(
        writer
            .begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
            .unwrap()
            .finish_input()
            .unwrap(),
    );
    assert_eq!(prepared.report().source_feed_count, 1);
    assert_eq!(prepared.report().created_feed_count, 1);
    assert_eq!(prepared.report().after_addresses, Cardinality129::ZERO);
    prepared.commit().unwrap();
    source.close().unwrap();
    writer.close().unwrap();
    let reader = LiveReader::open(&empty_destination_files.main).unwrap();
    assert!(reader.lookup_feed("empty").unwrap().is_some());
    reader.close().unwrap();

    let source_files = TestPair::new("ipv6-source");
    let destination_files = TestPair::new("ipv6-destination");
    create_membership(&source_files, AddressFamily::Ipv6, b"membership");
    create_membership(&destination_files, AddressFamily::Ipv6, b"membership");
    let mut source_writer = LiveWriter::open(&source_files.main, budget()).unwrap();
    let mut create = source_writer
        .begin_create_feed(name("all"), &cancellation)
        .unwrap();
    create
        .add_ranges_v6_slice(&[iprange_livedb::AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        }])
        .unwrap();
    changed(create.finish_input().unwrap()).commit().unwrap();
    source_writer.close().unwrap();

    let source = LiveReader::open(&source_files.main).unwrap();
    let mut writer = LiveWriter::open(&destination_files.main, budget()).unwrap();
    let prepared = changed(
        writer
            .begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
            .unwrap()
            .finish_input()
            .unwrap(),
    );
    assert_eq!(
        prepared.report().input_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    assert_eq!(
        prepared.report().added_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    prepared.commit().unwrap();
    source.close().unwrap();
    writer.close().unwrap();
    let reader = LiveReader::open(&destination_files.main).unwrap();
    let mut ranges = reader
        .feed_range_cursor_v6("all", RangeDirection::Forward)
        .unwrap();
    assert_eq!(
        ranges.next_range().unwrap(),
        Some(iprange_livedb::AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        })
    );
    assert_eq!(ranges.next_range().unwrap(), None);
    reader.close().unwrap();
}

#[test]
fn import_preconditions_cancellation_source_failure_and_budget_failure_are_atomic() {
    let source_files = TestPair::new("failure-source");
    let destination_files = TestPair::new("failure-destination");
    create_membership(&source_files, AddressFamily::Ipv4, b"membership");
    create_membership(&destination_files, AddressFamily::Ipv4, b"membership");
    let cancellation = CancellationToken::new();
    let mut source_writer = LiveWriter::open(&source_files.main, budget()).unwrap();
    let mut create = source_writer
        .begin_create_feed(name("alpha"), &cancellation)
        .unwrap();
    create
        .add_ranges_v4_slice(&[iprange_livedb::AddressRange {
            from: Ipv4Key(1),
            to: Ipv4Key(2),
        }])
        .unwrap();
    changed(create.finish_input().unwrap()).commit().unwrap();
    source_writer.close().unwrap();
    let source = LiveReader::open(&source_files.main).unwrap();

    let mut writer = LiveWriter::open(&destination_files.main, budget()).unwrap();
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    assert!(matches!(
        writer.begin_membership_import(MembershipImportSource::Live(&source), &cancelled),
        Err(Error::Cancelled)
    ));
    let during = CancellationToken::new();
    let import = writer
        .begin_membership_import(MembershipImportSource::Live(&source), &during)
        .unwrap();
    during.cancel();
    assert!(matches!(
        import.finish_input(),
        Err(Error::TransactionAborted(cause)) if matches!(*cause, Error::Cancelled)
    ));
    assert!(matches!(writer.commit(), Err(Error::NoPendingTransaction)));
    writer.close().unwrap();

    let mut same_writer = LiveWriter::open(&source_files.main, budget()).unwrap();
    assert!(matches!(
        same_writer.begin_membership_import(MembershipImportSource::Live(&source), &cancellation),
        Err(Error::InvalidArgument(_))
    ));
    same_writer.close().unwrap();

    for (label, family, kind, tag) in [
        (
            "wrong-family",
            AddressFamily::Ipv6,
            ValueKind::Membership,
            &b"membership"[..],
        ),
        (
            "wrong-tag",
            AddressFamily::Ipv4,
            ValueKind::Membership,
            &b"other"[..],
        ),
        (
            "wrong-kind",
            AddressFamily::Ipv4,
            ValueKind::Direct,
            &b"membership"[..],
        ),
    ] {
        let incompatible_files = TestPair::new(label);
        create_live(
            &incompatible_files.main,
            family,
            kind,
            ValueTag::new(tag).unwrap(),
            4,
        )
        .unwrap();
        let incompatible = LiveReader::open(&incompatible_files.main).unwrap();
        let mut candidate = LiveWriter::open(&destination_files.main, budget()).unwrap();
        assert!(matches!(
            candidate.begin_membership_import(
                MembershipImportSource::Live(&incompatible),
                &cancellation
            ),
            Err(Error::WrongMode(_))
        ));
        assert!(matches!(
            candidate.commit(),
            Err(Error::NoPendingTransaction)
        ));
        candidate.close().unwrap();
        incompatible.close().unwrap();
    }

    let tiny = TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 0,
        max_file_growth_pages: 0,
        max_open_files: 2,
    };
    let mut tiny_writer = LiveWriter::open(&destination_files.main, tiny).unwrap();
    assert!(matches!(
        tiny_writer
            .begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
            .unwrap()
            .finish_input(),
        Err(Error::TransactionAborted(cause)) if matches!(*cause, Error::BudgetExceeded(_))
    ));
    assert!(matches!(
        tiny_writer.commit(),
        Err(Error::NoPendingTransaction)
    ));
    tiny_writer.close().unwrap();

    let broken_destination_files = TestPair::new("source-read-failure");
    create_membership(
        &broken_destination_files,
        AddressFamily::Ipv4,
        b"membership",
    );
    let mut reusable = LiveWriter::open(&broken_destination_files.main, budget()).unwrap();
    let import = reusable
        .begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
        .unwrap();
    fs::OpenOptions::new()
        .write(true)
        .open(&source_files.main)
        .unwrap()
        .set_len(2 * 4096)
        .unwrap();
    assert!(matches!(
        import.finish_input(),
        Err(Error::TransactionAborted(cause)) if matches!(*cause, Error::Corrupt(_))
    ));
    let create = reusable
        .begin_create_feed(name("writer-remains-usable"), &cancellation)
        .unwrap();
    changed(create.finish_input().unwrap()).commit().unwrap();
    reusable.close().unwrap();
    source.close().unwrap();
}
