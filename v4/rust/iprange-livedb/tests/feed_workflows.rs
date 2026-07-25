use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, Error, FeedName,
    FinishedWorkflow, Ipv4Key, Ipv6Key, LiveReader, LiveWriter, LogicalChange, MembershipOperation,
    RangeDirection, RangeSource, TransactionBudget, ValueKind, ValueTag, WorkflowKind,
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
                "iprange-v4-feed-workflow-{label}-{}-{unique}",
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

fn changed(finished: FinishedWorkflow<'_>) -> iprange_livedb::PreparedWorkflow<'_> {
    match finished {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(report) => {
            panic!("expected changed workflow, got {report:?}")
        }
    }
}

#[test]
fn create_feed_normalizes_reports_and_exposes_ordered_projection() {
    let files = TestPair::new("create");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();

    let mut workflow = writer
        .begin_create_feed(name("alpha"), &cancellation)
        .unwrap();
    workflow
        .add_ranges_v4_slice(&[range(20, 30), range(10, 15), range(14, 22)])
        .unwrap();
    let mut prepared = changed(workflow.finish_input().unwrap());
    let report = *prepared.report();
    assert_eq!(report.workflow, WorkflowKind::CreateFeed);
    assert_eq!(report.logical_change, LogicalChange::Changed);
    assert_eq!(report.input_record_count, 3);
    assert_eq!(report.input_normalized_interval_count, 1);
    assert_eq!(report.before_range_record_count, 0);
    assert_eq!(report.after_range_record_count, 1);
    assert_eq!(report.input_addresses, Cardinality129::from_u64(21));
    assert_eq!(report.before_addresses, Cardinality129::ZERO);
    assert_eq!(report.after_addresses, Cardinality129::from_u64(21));
    assert_eq!(report.unchanged_value_addresses, Cardinality129::ZERO);
    assert_eq!(report.changed_value_addresses, Cardinality129::ZERO);
    assert_eq!(report.added_addresses, Cardinality129::from_u64(21));
    assert_eq!(report.removed_addresses, Cardinality129::ZERO);
    prepared.set_metadata_json(b"{\"feed\":\"alpha\"}").unwrap();
    prepared.commit().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    let alpha = reader.lookup_feed("alpha").unwrap().unwrap();
    let membership = reader.lookup_membership_v4(Ipv4Key(20)).unwrap().unwrap();
    assert!(membership.contains_index(alpha.index).unwrap());
    let mut forward = reader
        .feed_range_cursor_v4("alpha", RangeDirection::Forward)
        .unwrap();
    assert_eq!(forward.next_range().unwrap(), Some(range(10, 30)));
    assert_eq!(forward.next_range().unwrap(), None);
    let mut backward = reader
        .feed_range_cursor_v4("alpha", RangeDirection::Backward)
        .unwrap();
    assert_eq!(backward.next_range().unwrap(), Some(range(10, 30)));
    assert_eq!(backward.next_range().unwrap(), None);
    assert_eq!(
        reader.metadata_json().unwrap().as_deref(),
        Some(&b"{\"feed\":\"alpha\"}"[..])
    );
    reader.close().unwrap();

    assert!(matches!(
        writer.begin_create_feed(name("alpha"), &cancellation),
        Err(Error::NameExists)
    ));
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let empty = writer
        .begin_create_feed(name("empty"), &cancellation)
        .unwrap();
    let prepared = changed(empty.finish_input().unwrap());
    assert_eq!(prepared.report().after_range_record_count, 0);
    assert_eq!(prepared.report().after_addresses, Cardinality129::ZERO);
    prepared.commit().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert!(reader.lookup_feed("empty").unwrap().is_some());
    assert_eq!(
        reader
            .feed_range_cursor_v4("empty", RangeDirection::Forward)
            .unwrap()
            .next_range()
            .unwrap(),
        None
    );
    reader.close().unwrap();
}

#[test]
fn replace_feed_preserves_other_feeds_reports_and_detects_no_change() {
    let files = TestPair::new("replace");
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
                Ipv4Key(20),
                Ipv4Key(29),
                alpha_member,
                MembershipOperation::Union,
            )
            .unwrap();
        transaction
            .apply_v4(
                Ipv4Key(5),
                Ipv4Key(24),
                beta_member,
                MembershipOperation::Union,
            )
            .unwrap();
        transaction.commit().unwrap();
    }
    let mut before = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    let alpha_index = before.lookup_feed("alpha").unwrap().unwrap().index;
    let beta_index = before.lookup_feed("beta").unwrap().unwrap().index;
    before.close().unwrap();

    let cancellation = CancellationToken::new();
    let mut workflow = writer
        .begin_replace_feed(name("alpha"), &cancellation)
        .unwrap();
    workflow
        .add_ranges_v4_slice(&[range(8, 12), range(0, 3), range(3, 9)])
        .unwrap();
    let prepared = changed(workflow.finish_input().unwrap());
    let report = *prepared.report();
    assert_eq!(report.workflow, WorkflowKind::ReplaceFeed);
    assert_eq!(report.input_record_count, 3);
    assert_eq!(report.input_normalized_interval_count, 1);
    assert_eq!(report.before_range_record_count, 2);
    assert_eq!(report.after_range_record_count, 1);
    assert_eq!(report.input_addresses, Cardinality129::from_u64(13));
    assert_eq!(report.before_addresses, Cardinality129::from_u64(20));
    assert_eq!(report.after_addresses, Cardinality129::from_u64(13));
    assert_eq!(
        report.unchanged_value_addresses,
        Cardinality129::from_u64(10)
    );
    assert_eq!(report.changed_value_addresses, Cardinality129::ZERO);
    assert_eq!(report.added_addresses, Cardinality129::from_u64(3));
    assert_eq!(report.removed_addresses, Cardinality129::from_u64(10));
    prepared.commit().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(
        reader.lookup_feed("alpha").unwrap().unwrap().index,
        alpha_index
    );
    assert_eq!(
        reader.lookup_feed("beta").unwrap().unwrap().index,
        beta_index
    );
    let mut alpha = reader
        .feed_range_cursor_v4("alpha", RangeDirection::Forward)
        .unwrap();
    assert_eq!(alpha.next_range().unwrap(), Some(range(0, 12)));
    assert_eq!(alpha.next_range().unwrap(), None);
    let mut beta = reader
        .feed_range_cursor_v4("beta", RangeDirection::Backward)
        .unwrap();
    assert_eq!(beta.next_range().unwrap(), Some(range(5, 24)));
    assert_eq!(beta.next_range().unwrap(), None);
    reader.close().unwrap();

    let mut equal = writer
        .begin_replace_feed(name("alpha"), &cancellation)
        .unwrap();
    equal.add_ranges_v4_slice(&[range(0, 12)]).unwrap();
    match equal.finish_input().unwrap() {
        FinishedWorkflow::NoChange(report) => {
            assert_eq!(report.logical_change, LogicalChange::NoChange);
            assert_eq!(
                report.unchanged_value_addresses,
                Cardinality129::from_u64(13)
            );
        }
        FinishedWorkflow::Changed(_) => panic!("equal feed replacement changed"),
    }
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let empty = writer
        .begin_replace_feed(name("alpha"), &cancellation)
        .unwrap();
    changed(empty.finish_input().unwrap()).commit().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(
        reader.lookup_feed("alpha").unwrap().unwrap().index,
        alpha_index
    );
    assert_eq!(
        reader
            .feed_range_cursor_v4("alpha", RangeDirection::Forward)
            .unwrap()
            .next_range()
            .unwrap(),
        None
    );
    assert_eq!(
        reader
            .feed_range_cursor_v4("beta", RangeDirection::Forward)
            .unwrap()
            .next_range()
            .unwrap(),
        Some(range(5, 24))
    );
    reader.close().unwrap();
}

#[test]
fn feed_input_failures_and_cancellation_abort_the_complete_workflow() {
    let files = TestPair::new("failure");
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
    let cancellation = CancellationToken::new();

    let mut workflow = writer
        .begin_create_feed(name("failed"), &cancellation)
        .unwrap();
    let mut source = FailingSource::new(range(10, 20));
    assert!(matches!(
        workflow.add_ranges_v4(&mut source),
        Err(Error::TransactionAborted(_))
    ));
    drop(workflow);
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let mut wrong_family = writer
        .begin_create_feed(name("wrong-family"), &cancellation)
        .unwrap();
    assert!(matches!(
        wrong_family.add_ranges_v6_slice(&[AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        }]),
        Err(Error::TransactionAborted(_))
    ));
    drop(wrong_family);

    let cancelled = CancellationToken::new();
    let mut workflow = writer
        .begin_create_feed(name("cancelled"), &cancelled)
        .unwrap();
    cancelled.cancel();
    assert!(matches!(
        workflow.add_ranges_v4_slice(&[range(0, 100)]),
        Err(Error::TransactionAborted(cause)) if matches!(*cause, Error::Cancelled)
    ));
    drop(workflow);

    {
        let mut unfinished = writer
            .begin_create_feed(name("unfinished"), &cancellation)
            .unwrap();
        unfinished.add_ranges_v4_slice(&[range(1, 2)]).unwrap();
    }
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::WrongState(_))
    ));
    assert_eq!(
        writer.abort().unwrap().outcome,
        iprange_livedb::AbortOutcome::Aborted
    );

    assert!(matches!(
        writer.begin_replace_feed(name("missing"), &cancellation),
        Err(Error::NameNotFound)
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    for feed in ["failed", "wrong-family", "cancelled", "unfinished"] {
        assert!(reader.lookup_feed(feed).unwrap().is_none());
    }
    reader.close().unwrap();
}

#[test]
fn full_ipv6_feed_cardinality_is_exact() {
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
    let mut workflow = writer
        .begin_create_feed(name("all"), &cancellation)
        .unwrap();
    workflow
        .add_ranges_v6_slice(&[AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        }])
        .unwrap();
    let prepared = changed(workflow.finish_input().unwrap());
    assert_eq!(
        prepared.report().after_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    prepared.commit().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(
        reader
            .feed_range_cursor_v6("all", RangeDirection::Forward)
            .unwrap()
            .next_range()
            .unwrap(),
        Some(AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        })
    );
    reader.close().unwrap();
}

struct FailingSource {
    batch: [AddressRange<Ipv4Key>; 1],
    state: u8,
}

impl FailingSource {
    fn new(range: AddressRange<Ipv4Key>) -> Self {
        Self {
            batch: [range],
            state: 0,
        }
    }
}

impl RangeSource<AddressRange<Ipv4Key>> for FailingSource {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv4Key>]>> {
        match self.state {
            0 => {
                self.state = 1;
                Ok(Some(&self.batch))
            }
            _ => Err(Error::InvalidArgument("synthetic source failure")),
        }
    }
}
