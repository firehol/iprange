#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live,
    validation::{validate, ValidationBudget, ValidationMode, ValidationSinkControl},
    AddressFamily, AddressRange, CancellationToken, Cardinality129, CommitDurability, DirectRange,
    Error, FinishedWorkflow, FirstSeenRemoval, Ipv4Key, Ipv6Key, LiveReader, LiveWriter,
    LogicalChange, RangeSource, ReclaimResult, TransactionBudget, ValueKind, ValueTag,
    WorkflowKind,
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
                "iprange-v4-workflow-{label}-{}-{unique}",
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

fn direct(from: u32, to: u32, value: u32) -> DirectRange<Ipv4Key> {
    DirectRange {
        from: Ipv4Key(from),
        to: Ipv4Key(to),
        value,
    }
}

fn address(from: u32, to: u32) -> AddressRange<Ipv4Key> {
    AddressRange {
        from: Ipv4Key(from),
        to: Ipv4Key(to),
    }
}

fn changed(finished: FinishedWorkflow<'_>) -> iprange_livedb::PreparedWorkflow<'_> {
    match finished {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(report) => {
            panic!("expected a changed workflow, got {report:?}")
        }
    }
}

fn validate_live(path: &std::path::Path) {
    let mut sink =
        |_: &iprange_livedb::validation::ValidationFinding| Ok(ValidationSinkControl::Continue);
    let result = validate(
        path,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(2 * 1024 * 1024, 2),
        &CancellationToken::new(),
        &mut sink,
    )
    .unwrap();
    assert!(result.valid);
}

#[test]
fn direct_replacement_preserves_order_reports_exactly_and_retires_old_tree() {
    let files = TestPair::new("direct");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let mut seed = writer.begin_direct_transaction(&cancellation).unwrap();
    seed.assign_v4(Ipv4Key(0), Ipv4Key(15), 9).unwrap();
    seed.assign_v4(Ipv4Key(60), Ipv4Key(69), 7).unwrap();
    seed.commit().unwrap();

    let mut workflow = writer.begin_direct_replacement(&cancellation).unwrap();
    workflow
        .add_ranges_v4_slice(&[direct(10, 30, 1), direct(40, 50, 2)])
        .unwrap();
    workflow
        .add_ranges_v4_slice(&[direct(20, 45, 3), direct(25, 25, 4)])
        .unwrap();
    let mut prepared = changed(workflow.finish_input().unwrap());
    let report = *prepared.report();
    assert_eq!(report.workflow, WorkflowKind::DirectReplacement);
    assert_eq!(report.logical_change, LogicalChange::Changed);
    assert_eq!(report.input_record_count, 4);
    assert_eq!(report.input_normalized_interval_count, 5);
    assert_eq!(report.before_range_record_count, 2);
    assert_eq!(report.after_range_record_count, 5);
    assert_eq!(report.input_addresses, Cardinality129::from_u64(41));
    assert_eq!(report.before_addresses, Cardinality129::from_u64(26));
    assert_eq!(report.after_addresses, Cardinality129::from_u64(41));
    assert_eq!(
        report.unchanged_value_addresses,
        Cardinality129::from_u64(0)
    );
    assert_eq!(report.changed_value_addresses, Cardinality129::from_u64(6));
    assert_eq!(report.added_addresses, Cardinality129::from_u64(35));
    assert_eq!(report.removed_addresses, Cardinality129::from_u64(20));
    prepared.set_metadata_json(b"{\"round\":2}").unwrap();
    assert_eq!(
        prepared.commit().unwrap().durability,
        CommitDurability::Committed
    );

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    for (at, value) in [
        (9, None),
        (10, Some(1)),
        (19, Some(1)),
        (20, Some(3)),
        (24, Some(3)),
        (25, Some(4)),
        (26, Some(3)),
        (45, Some(3)),
        (46, Some(2)),
        (50, Some(2)),
        (51, None),
    ] {
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(at)).unwrap(), value);
    }
    assert_eq!(
        reader.metadata_json().unwrap().as_deref(),
        Some(&b"{\"round\":2}"[..])
    );
    reader.close().unwrap();

    let mut equal = writer.begin_direct_replacement(&cancellation).unwrap();
    equal
        .add_ranges_v4_slice(&[
            direct(10, 30, 1),
            direct(40, 50, 2),
            direct(20, 45, 3),
            direct(25, 25, 4),
        ])
        .unwrap();
    match equal.finish_input().unwrap() {
        FinishedWorkflow::NoChange(report) => {
            assert_eq!(
                report.unchanged_value_addresses,
                Cardinality129::from_u64(41)
            );
        }
        FinishedWorkflow::Changed(_) => panic!("equal replacement created a transaction"),
    }

    assert!(matches!(
        writer
            .reclaim(10, 20_000, &CancellationToken::new())
            .unwrap(),
        ReclaimResult::Commit { .. }
    ));
    writer.close().unwrap();
}

#[test]
fn first_seen_refresh_keeps_old_values_removes_missing_and_marks_reappearance_new() {
    let files = TestPair::new("first-seen");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();

    let mut first = writer.begin_first_seen_refresh(100, &cancellation).unwrap();
    first
        .add_ranges_v4_slice(&[address(30, 40), address(10, 20)])
        .unwrap();
    changed(first.finish_input().unwrap()).commit().unwrap();

    let mut second = writer.begin_first_seen_refresh(200, &cancellation).unwrap();
    second
        .add_ranges_v4_slice(&[address(35, 45), address(15, 32), address(30, 38)])
        .unwrap();
    let prepared = changed(second.finish_input().unwrap());
    let report = *prepared.report();
    assert_eq!(report.workflow, WorkflowKind::FirstSeenRefresh);
    assert_eq!(report.input_record_count, 3);
    assert_eq!(report.input_normalized_interval_count, 1);
    assert_eq!(report.before_range_record_count, 2);
    assert_eq!(report.after_range_record_count, 4);
    assert_eq!(report.input_addresses, Cardinality129::from_u64(31));
    assert_eq!(report.before_addresses, Cardinality129::from_u64(22));
    assert_eq!(report.after_addresses, Cardinality129::from_u64(31));
    assert_eq!(
        report.unchanged_value_addresses,
        Cardinality129::from_u64(17)
    );
    assert_eq!(report.changed_value_addresses, Cardinality129::from_u64(0));
    assert_eq!(report.added_addresses, Cardinality129::from_u64(14));
    assert_eq!(report.removed_addresses, Cardinality129::from_u64(5));
    prepared.commit().unwrap();

    let mut no_change = writer.begin_first_seen_refresh(300, &cancellation).unwrap();
    no_change.add_ranges_v4_slice(&[address(15, 45)]).unwrap();
    match no_change.finish_input().unwrap() {
        FinishedWorkflow::NoChange(report) => {
            assert_eq!(report.logical_change, LogicalChange::NoChange);
            assert_eq!(
                report.unchanged_value_addresses,
                Cardinality129::from_u64(31)
            );
        }
        FinishedWorkflow::Changed(_) => panic!("equal refresh created a transaction"),
    }
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let mut removed = writer.begin_first_seen_refresh(400, &cancellation).unwrap();
    removed
        .add_ranges_v4_slice(&[address(15, 20), address(30, 45)])
        .unwrap();
    changed(removed.finish_input().unwrap()).commit().unwrap();

    let mut returned = writer.begin_first_seen_refresh(500, &cancellation).unwrap();
    returned.add_ranges_v4_slice(&[address(15, 45)]).unwrap();
    changed(returned.finish_input().unwrap()).commit().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    for (at, value) in [
        (14, None),
        (15, Some(100)),
        (20, Some(100)),
        (21, Some(500)),
        (29, Some(500)),
        (30, Some(100)),
        (40, Some(100)),
        (41, Some(200)),
        (45, Some(200)),
        (46, None),
    ] {
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(at)).unwrap(), value);
    }
    reader.close().unwrap();
}

#[test]
fn first_seen_removal_sink_is_batched_exact_and_atomic() {
    let files = TestPair::new("first-seen-removals");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();
    let ranges: Vec<_> = (0..130)
        .map(|index| address(index * 2, index * 2))
        .collect();
    let mut seed = writer.begin_first_seen_refresh(77, &cancellation).unwrap();
    seed.add_ranges_v4_slice(&ranges).unwrap();
    changed(seed.finish_input().unwrap()).commit().unwrap();

    let mut batch_lengths = Vec::new();
    let mut removals = Vec::new();
    let mut sink = |batch: &[FirstSeenRemoval<Ipv4Key>]| {
        batch_lengths.push(batch.len());
        removals.extend_from_slice(batch);
        Ok(())
    };
    let refresh = writer.begin_first_seen_refresh(88, &cancellation).unwrap();
    let prepared = changed(refresh.finish_input_with_removals_v4(&mut sink).unwrap());
    assert_eq!(batch_lengths, [64, 64, 2]);
    assert_eq!(removals.len(), 130);
    for (index, removal) in removals.iter().enumerate() {
        assert_eq!(removal.from, Ipv4Key(index as u32 * 2));
        assert_eq!(removal.to, removal.from);
        assert_eq!(removal.first_seen, 77);
        assert_eq!(removal.addresses, Cardinality129::from_u64(1));
    }
    assert_eq!(
        prepared.report().removed_addresses,
        Cardinality129::from_u64(130)
    );
    prepared.abort().unwrap();

    let refresh = writer.begin_first_seen_refresh(99, &cancellation).unwrap();
    let mut failing =
        |_batch: &[FirstSeenRemoval<Ipv4Key>]| Err(Error::InvalidArgument("removal sink failed"));
    assert!(matches!(
        refresh.finish_input_with_removals_v4(&mut failing),
        Err(Error::TransactionAborted(cause))
            if matches!(*cause, Error::InvalidArgument("removal sink failed"))
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    assert_eq!(reader.info().unwrap().range_record_count, 130);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(0)).unwrap(), Some(77));
    reader.close().unwrap();
}

#[test]
fn last_seen_refresh_updates_current_retains_recent_absence_and_expires_cutoff() {
    let files = TestPair::new("last-seen");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();

    let mut seed = writer
        .begin_last_seen_refresh(100, 0, &cancellation)
        .unwrap();
    seed.add_ranges_v4_slice(&[address(30, 40), address(10, 20)])
        .unwrap();
    changed(seed.finish_input().unwrap()).commit().unwrap();

    let mut refresh = writer
        .begin_last_seen_refresh(200, 50, &cancellation)
        .unwrap();
    refresh
        .add_ranges_v4_slice(&[address(35, 45), address(15, 32)])
        .unwrap();
    let prepared = changed(refresh.finish_input().unwrap());
    assert_eq!(prepared.report().workflow, WorkflowKind::LastSeenRefresh);
    assert_eq!(
        prepared.report().changed_value_addresses,
        Cardinality129::from_u64(15)
    );
    assert_eq!(
        prepared.report().added_addresses,
        Cardinality129::from_u64(14)
    );
    assert_eq!(prepared.report().removed_addresses, Cardinality129::ZERO);
    prepared.commit().unwrap();

    // An out-of-order refresh cannot move timestamps backwards. Values exactly
    // at the cutoff expire when absent.
    let mut replay = writer
        .begin_last_seen_refresh(150, 100, &cancellation)
        .unwrap();
    replay.add_ranges_v4_slice(&[address(18, 22)]).unwrap();
    changed(replay.finish_input().unwrap()).commit().unwrap();

    let mut expire = writer
        .begin_last_seen_refresh(300, 250, &cancellation)
        .unwrap();
    expire.add_ranges_v4_slice(&[address(20, 25)]).unwrap();
    changed(expire.finish_input().unwrap()).commit().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    for (at, value) in [
        (19, None),
        (20, Some(300)),
        (25, Some(300)),
        (26, None),
        (45, None),
    ] {
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(at)).unwrap(), value);
    }
    reader.close().unwrap();
}

#[test]
fn timestamp_refresh_requires_its_exact_direct_semantic() {
    let first = TestPair::new("first-tag");
    create_live(
        &first.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&first.main, budget(), &cancellation).unwrap();
    assert!(matches!(
        writer.begin_last_seen_refresh(1, 0, &cancellation),
        Err(Error::WrongValueTag(_))
    ));
    writer.close().unwrap();

    let last = TestPair::new("last-tag");
    create_live(
        &last.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let mut writer = LiveWriter::open(&last.main, budget(), &cancellation).unwrap();
    assert!(matches!(
        writer.begin_first_seen_refresh(1, &cancellation),
        Err(Error::WrongValueTag(_))
    ));
    writer.close().unwrap();
}

#[test]
fn first_seen_merge_reuses_coverage_page_before_expanding_it() {
    let files = TestPair::new("first-seen-reuse");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let old: Vec<_> = (0..100)
        .map(|index| address(index * 2, index * 2))
        .collect();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();
    let mut seed = writer.begin_first_seen_refresh(10, &cancellation).unwrap();
    seed.add_ranges_v4_slice(&old).unwrap();
    changed(seed.finish_input().unwrap()).commit().unwrap();
    writer.close().unwrap();
    validate_live(&files.main);

    let two_page_budget = TransactionBudget {
        max_heap_bytes: 1,
        max_private_pages: 2,
        max_file_growth_pages: 2,
        max_open_files: 2,
    };
    let mut writer = LiveWriter::open(&files.main, two_page_budget, &cancellation).unwrap();
    let mut refresh = writer.begin_first_seen_refresh(20, &cancellation).unwrap();
    refresh.add_ranges_v4_slice(&[address(0, 199)]).unwrap();
    let prepared = changed(refresh.finish_input().unwrap());
    let report = *prepared.report();
    assert_eq!(report.input_normalized_interval_count, 1);
    assert_eq!(report.before_range_record_count, 100);
    assert_eq!(report.after_range_record_count, 200);
    assert_eq!(
        report.unchanged_value_addresses,
        Cardinality129::from_u64(100)
    );
    assert_eq!(report.added_addresses, Cardinality129::from_u64(100));
    prepared.abort().unwrap();
    writer.close().unwrap();

    let one_page_budget = TransactionBudget {
        max_heap_bytes: 1,
        max_private_pages: 1,
        max_file_growth_pages: 1,
        max_open_files: 2,
    };
    let mut writer = LiveWriter::open(&files.main, one_page_budget, &cancellation).unwrap();
    let mut refresh = writer.begin_first_seen_refresh(20, &cancellation).unwrap();
    refresh.add_ranges_v4_slice(&[address(0, 199)]).unwrap();
    assert!(matches!(
        refresh.finish_input(),
        Err(Error::TransactionAborted(cause)) if matches!(*cause, Error::BudgetExceeded(_))
    ));
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));
    writer.close().unwrap();

    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();
    let mut refresh = writer.begin_first_seen_refresh(20, &cancellation).unwrap();
    refresh.add_ranges_v4_slice(&[address(0, 199)]).unwrap();
    changed(refresh.finish_input().unwrap()).commit().unwrap();
    writer.close().unwrap();
    validate_live(&files.main);

    let mut reader = LiveReader::open(&files.main, &cancellation).unwrap();
    for address in 0..200 {
        let expected = if address % 2 == 0 { Some(10) } else { Some(20) };
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(address)).unwrap(), expected);
    }
    reader.close().unwrap();
}

#[test]
fn first_seen_finish_cancellation_discards_the_complete_refresh() {
    let files = TestPair::new("first-seen-cancel");
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
    let seed_cancellation = CancellationToken::new();
    let mut seed = writer
        .begin_first_seen_refresh(10, &seed_cancellation)
        .unwrap();
    seed.add_ranges_v4_slice(&[address(10, 20)]).unwrap();
    changed(seed.finish_input().unwrap()).commit().unwrap();

    let cancellation = CancellationToken::new();
    let mut refresh = writer.begin_first_seen_refresh(20, &cancellation).unwrap();
    refresh.add_ranges_v4_slice(&[address(10, 30)]).unwrap();
    cancellation.cancel();
    assert!(matches!(
        refresh.finish_input(),
        Err(Error::TransactionAborted(cause)) if matches!(*cause, Error::Cancelled)
    ));
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(10));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(20)).unwrap(), Some(10));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(21)).unwrap(), None);
    reader.close().unwrap();
}

#[test]
fn unfinished_source_failure_and_cancellation_cannot_publish_partial_input() {
    let files = TestPair::new("abort");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"direct").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut seed = writer.begin_direct_transaction(&cancellation).unwrap();
    seed.assign_v4(Ipv4Key(1), Ipv4Key(2), 9).unwrap();
    seed.commit().unwrap();

    {
        let mut unfinished = writer.begin_direct_replacement(&cancellation).unwrap();
        unfinished
            .add_ranges_v4_slice(&[direct(10, 20, 1)])
            .unwrap();
    }
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::WrongState(_))
    ));
    assert!(matches!(
        writer.begin_direct_transaction(&cancellation),
        Err(Error::WrongState(_))
    ));
    assert_eq!(
        writer.abort().unwrap().outcome,
        iprange_livedb::AbortOutcome::Aborted
    );

    let prepared_drop = CancellationToken::new();
    {
        let mut workflow = writer.begin_direct_replacement(&prepared_drop).unwrap();
        workflow.add_ranges_v4_slice(&[direct(30, 40, 2)]).unwrap();
        let _prepared = changed(workflow.finish_input().unwrap());
    }
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::WrongState(_))
    ));
    assert!(matches!(
        writer.set_metadata_json(b"{}", &CancellationToken::new()),
        Err(Error::WrongState(_))
    ));
    assert!(matches!(
        writer.metadata_json_len(),
        Err(Error::WrongState(_))
    ));
    assert_eq!(
        writer.abort().unwrap().outcome,
        iprange_livedb::AbortOutcome::Aborted
    );

    let mut workflow = writer.begin_direct_replacement(&cancellation).unwrap();
    let mut source = FailingSource::new(direct(50, 60, 3));
    assert!(matches!(
        workflow.add_ranges_v4(&mut source),
        Err(Error::TransactionAborted(_))
    ));
    drop(workflow);
    assert!(matches!(
        writer.commit(&CancellationToken::new()),
        Err(Error::NoPendingTransaction)
    ));

    let cancel = CancellationToken::new();
    let mut workflow = writer.begin_direct_replacement(&cancel).unwrap();
    workflow.add_ranges_v4_slice(&[direct(70, 80, 4)]).unwrap();
    cancel.cancel();
    assert!(matches!(
        workflow.add_ranges_v4_slice(&[direct(90, 100, 5)]),
        Err(Error::TransactionAborted(cause)) if matches!(*cause, Error::Cancelled)
    ));
    drop(workflow);

    let publish_cancel = CancellationToken::new();
    let mut workflow = writer.begin_direct_replacement(&publish_cancel).unwrap();
    workflow
        .add_ranges_v4_slice(&[direct(110, 120, 6)])
        .unwrap();
    let prepared = changed(workflow.finish_input().unwrap());
    publish_cancel.cancel();
    let result = prepared.commit().unwrap();
    assert_eq!(result.durability, CommitDurability::NotCommitted);
    assert!(matches!(
        result.cause,
        Some(Error::TransactionAborted(cause)) if matches!(*cause, Error::Cancelled)
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().transaction_id, 2);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(1)).unwrap(), Some(9));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(55)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(75)).unwrap(), None);
    reader.close().unwrap();
}

#[test]
fn full_ipv6_first_seen_report_is_exact() {
    let files = TestPair::new("ipv6");
    create_live(
        &files.main,
        AddressFamily::Ipv6,
        ValueKind::Direct,
        ValueTag::FIRST_SEEN,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let mut workflow = writer.begin_first_seen_refresh(42, &cancellation).unwrap();
    workflow
        .add_ranges_v6_slice(&[AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        }])
        .unwrap();
    let prepared = changed(workflow.finish_input().unwrap());
    assert_eq!(
        prepared.report().input_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    assert_eq!(
        prepared.report().added_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    prepared.commit().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.lookup_direct_v6(Ipv6Key::MAX).unwrap(), Some(42));
    reader.close().unwrap();
}

#[test]
fn empty_input_retires_and_reclaims_a_multilevel_range_tree() {
    let files = TestPair::new("multilevel-retire");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"direct").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();
    let ranges: Vec<_> = (0..2_000)
        .map(|index| direct(index * 2, index * 2, index & 1))
        .collect();
    let mut initial = writer.begin_direct_replacement(&cancellation).unwrap();
    initial.add_ranges_v4_slice(&ranges).unwrap();
    changed(initial.finish_input().unwrap()).commit().unwrap();

    let mut empty = writer.begin_direct_replacement(&cancellation).unwrap();
    empty.add_ranges_v4_slice(&[]).unwrap();
    let prepared = changed(empty.finish_input().unwrap());
    assert_eq!(prepared.report().input_record_count, 0);
    assert_eq!(prepared.report().input_normalized_interval_count, 0);
    assert_eq!(
        prepared.report().removed_addresses,
        Cardinality129::from_u64(2_000)
    );
    prepared.commit().unwrap();
    assert!(matches!(
        writer
            .reclaim(10, 20_000, &CancellationToken::new())
            .unwrap(),
        ReclaimResult::Commit { .. }
    ));
    writer.close().unwrap();

    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    assert_eq!(reader.info().unwrap().range_record_count, 0);
    reader.close().unwrap();
}

struct FailingSource {
    batch: [DirectRange<Ipv4Key>; 1],
    state: u8,
}

impl FailingSource {
    fn new(range: DirectRange<Ipv4Key>) -> Self {
        Self {
            batch: [range],
            state: 0,
        }
    }
}

impl RangeSource<DirectRange<Ipv4Key>> for FailingSource {
    fn next_batch(&mut self) -> iprange_livedb::error::Result<Option<&[DirectRange<Ipv4Key>]>> {
        match self.state {
            0 => {
                self.state = 1;
                Ok(Some(&self.batch))
            }
            _ => Err(Error::InvalidArgument("synthetic source failure")),
        }
    }
}
