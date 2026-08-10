use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, FeedName, FinishedWorkflow, ImmutableReader,
    Ipv4Key, LiveReader, LiveWriter, MembershipImportSource, MembershipOperation, RangeDirection,
    ValueKind, ValueTag,
};

use crate::measure;
use crate::model::{transaction_budget, TestDatabase};
use crate::scenarios::{
    close_reader, close_writer, count_cursor, count_points, count_random_points,
    immutable_snapshot, random_points, reader_work, require_committed, require_count, result,
    ScenarioResult,
};
use crate::source::{AddressSource, FeedShape, FeedShapeSource};

pub(super) fn shaped_feed(
    name: &'static str,
    size: usize,
    shape: FeedShape,
    second: bool,
) -> Result<ScenarioResult, String> {
    let database = TestDatabase::new(name)?;
    if second {
        create_membership_file(&database)?;
        create_shaped_feed(&database, "first", size, shape)?;
    }
    let (operation, measured) = measure::operation(|| -> Result<_, String> {
        if !second {
            create_membership_file(&database)?;
        }
        create_shaped_feed(
            &database,
            if second { "second" } else { "first" },
            size,
            shape,
        )
    });
    let report = operation?;
    let expected = shape.expected_intervals(size);
    if report.input_record_count != size as u64
        || report.input_normalized_interval_count != expected
    {
        return Err(format!("unexpected shaped-feed report: {report:?}"));
    }
    let measured_result = result(
        name,
        size,
        usize::from(second),
        size as u64,
        &database,
        measured,
        database.main(),
    )?;
    let expected_feeds = if second { 2 } else { 1 };
    if measured_result.range_records != expected || measured_result.feeds != expected_feeds {
        return Err(format!(
            "{name} produced {} ranges and {} feeds; expected {expected} and {expected_feeds}",
            measured_result.range_records, measured_result.feeds
        ));
    }
    Ok(measured_result)
}

pub(super) fn replace_feed(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("feed-replace", size.min(128), feeds)?;
    let phase = (size / 11).max(1) as u32;
    let cancellation = CancellationToken::new();
    let (operation, measured) = measure::operation(|| -> Result<(), String> {
        let mut writer = LiveWriter::open(
            database.main(),
            transaction_budget(size, feeds),
            &cancellation,
        )
        .map_err(display)?;
        let mut workflow = writer
            .begin_replace_feed(feed_name(feeds - 1)?, &cancellation)
            .map_err(display)?;
        workflow
            .add_ranges_v4(&mut AddressSource::new(size, phase)?)
            .map_err(display)?;
        match workflow.finish_input().map_err(display)? {
            FinishedWorkflow::Changed(prepared) => {
                require_committed(prepared.commit().map_err(display)?)?;
            }
            FinishedWorkflow::NoChange(report) => {
                return Err(format!("feed replacement changed nothing: {report:?}"));
            }
        }
        close_writer(&mut writer)
    });
    operation?;
    result(
        "feed-replace",
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn import(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let source = populated("membership-import-source", size, feeds)?;
    let destination = populated(
        "membership-import-destination",
        size.min(128),
        feeds.div_ceil(2),
    )?;
    let cancellation = CancellationToken::new();
    let mut source_reader = LiveReader::open(source.main(), &cancellation).map_err(display)?;
    let (operation, measured) = measure::operation(|| -> Result<(), String> {
        let mut writer = LiveWriter::open(
            destination.main(),
            transaction_budget(size, feeds),
            &cancellation,
        )
        .map_err(display)?;
        let workflow = writer
            .begin_membership_import(MembershipImportSource::Live(&source_reader), &cancellation)
            .map_err(display)?;
        match workflow.finish_input().map_err(display)? {
            FinishedWorkflow::Changed(prepared) => {
                require_committed(prepared.commit().map_err(display)?)?;
            }
            FinishedWorkflow::NoChange(report) => {
                return Err(format!("membership import changed nothing: {report:?}"));
            }
        }
        close_writer(&mut writer)
    });
    operation?;
    close_reader(&mut source_reader)?;
    result(
        "membership-import",
        size,
        feeds,
        size as u64,
        &destination,
        measured,
        destination.main(),
    )
}

pub(super) fn live_lookup(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("live-membership-lookup", size, feeds)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (target, repetitions, work_units) =
        membership_reader_work(size, feeds, |name| reader.lookup_feed(name))?;
    let (operation, measured) = measure::operation(|| {
        count_points(size, repetitions, |address| {
            membership_contains(reader.lookup_membership_v4(address), target)
        })
    });
    let hits = operation?;
    require_count("live membership lookup", hits, work_units, "addresses")?;
    close_reader(&mut reader)?;
    result(
        "live-membership-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_lookup(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("immutable-membership-lookup", size, feeds)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (target, repetitions, work_units) =
        membership_reader_work(size, feeds, |name| reader.lookup_feed(name))?;
    let (operation, measured) = measure::operation(|| {
        count_points(size, repetitions, |address| {
            membership_contains(reader.lookup_membership_v4(address), target)
        })
    });
    let hits = operation?;
    require_count("immutable membership lookup", hits, work_units, "addresses")?;
    drop(reader);
    result(
        "immutable-membership-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn live_random_lookup(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("live-membership-random-lookup", size, feeds)?;
    let points = random_points(size)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (target, repetitions, work_units) =
        membership_reader_work(size, feeds, |name| reader.lookup_feed(name))?;
    let (operation, measured) = measure::operation(|| {
        count_random_points(&points, repetitions, |address| {
            membership_contains(reader.lookup_membership_v4(address), target)
        })
    });
    let hits = operation?;
    require_count(
        "live random membership lookup",
        hits,
        work_units,
        "addresses",
    )?;
    close_reader(&mut reader)?;
    result(
        "live-membership-random-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_random_lookup(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("immutable-membership-random-lookup", size, feeds)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let points = random_points(size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (target, repetitions, work_units) =
        membership_reader_work(size, feeds, |name| reader.lookup_feed(name))?;
    let (operation, measured) = measure::operation(|| {
        count_random_points(&points, repetitions, |address| {
            membership_contains(reader.lookup_membership_v4(address), target)
        })
    });
    let hits = operation?;
    require_count(
        "immutable random membership lookup",
        hits,
        work_units,
        "addresses",
    )?;
    drop(reader);
    result(
        "immutable-membership-random-lookup",
        size,
        feeds,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn live_scan(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("live-feed-scan", size, feeds)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (name, repetitions, work_units) = membership_scan_work(size, feeds)?;
    let (operation, measured) = measure::operation(|| {
        count_cursor(
            repetitions,
            || {
                reader
                    .feed_range_cursor_v4(name.as_str(), RangeDirection::Forward)
                    .map_err(display)
            },
            |cursor| {
                cursor
                    .next_range()
                    .map(|range| range.is_some())
                    .map_err(display)
            },
        )
    });
    let records = operation?;
    require_count("live feed scan", records, work_units, "ranges")?;
    close_reader(&mut reader)?;
    result(
        "live-feed-scan",
        size,
        feeds,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_scan(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("immutable-feed-scan", size, feeds)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (name, repetitions, work_units) = membership_scan_work(size, feeds)?;
    let (operation, measured) = measure::operation(|| {
        count_cursor(
            repetitions,
            || {
                reader
                    .feed_range_cursor_v4(name.as_str(), RangeDirection::Forward)
                    .map_err(display)
            },
            |cursor| {
                cursor
                    .next_range()
                    .map(|range| range.is_some())
                    .map_err(display)
            },
        )
    });
    let records = operation?;
    require_count("immutable feed scan", records, work_units, "ranges")?;
    drop(reader);
    result(
        "immutable-feed-scan",
        size,
        feeds,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn populated(label: &str, ranges: usize, feeds: usize) -> Result<TestDatabase, String> {
    let database = TestDatabase::new(label)?;
    create_live(
        database.main(),
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").ok_or_else(|| "invalid benchmark value tag".to_owned())?,
        1,
        &CancellationToken::new(),
    )
    .map_err(display)?;
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        database.main(),
        transaction_budget(ranges, feeds),
        &cancellation,
    )
    .map_err(display)?;
    let mut transaction = writer
        .begin_membership_transaction(&cancellation)
        .map_err(display)?;
    let mut membership = transaction.empty_membership().map_err(display)?;
    for index in 0..feeds {
        let feed = transaction
            .ensure_feed(feed_name(index)?)
            .map_err(display)?;
        membership = transaction.add_feed(membership, feed).map_err(display)?;
    }
    for index in 0..ranges {
        let start = index as u32 * 4;
        transaction
            .apply_v4(
                Ipv4Key(start),
                Ipv4Key(start + 1),
                membership,
                MembershipOperation::Replace,
            )
            .map_err(display)?;
    }
    require_committed(transaction.commit().map_err(display)?)?;
    close_writer(&mut writer)?;
    Ok(database)
}

pub(super) fn populated_rotating(
    label: &str,
    ranges: usize,
    feeds: usize,
    width: usize,
) -> Result<TestDatabase, String> {
    let feeds = feeds.max(1);
    let width = width.clamp(1, feeds);
    let database = TestDatabase::new(label)?;
    create_membership_file(&database)?;
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        database.main(),
        transaction_budget(ranges, feeds),
        &cancellation,
    )
    .map_err(display)?;
    let mut transaction = writer
        .begin_membership_transaction(&cancellation)
        .map_err(display)?;
    let mut feed_refs = Vec::new();
    feed_refs
        .try_reserve_exact(feeds)
        .map_err(|_| "benchmark feed-reference allocation failed".to_owned())?;
    for index in 0..feeds {
        feed_refs.push(
            transaction
                .ensure_feed(feed_name(index)?)
                .map_err(display)?,
        );
    }
    let mut memberships = Vec::new();
    memberships
        .try_reserve_exact(feeds)
        .map_err(|_| "benchmark membership-reference allocation failed".to_owned())?;
    for start in 0..feeds {
        let mut membership = transaction.empty_membership().map_err(display)?;
        for offset in 0..width {
            membership = transaction
                .add_feed(membership, feed_refs[(start + offset) % feeds])
                .map_err(display)?;
        }
        memberships.push(membership);
    }
    for index in 0..ranges {
        let start = index as u32 * 4;
        transaction
            .apply_v4(
                Ipv4Key(start),
                Ipv4Key(start + 1),
                memberships[index % feeds],
                MembershipOperation::Replace,
            )
            .map_err(display)?;
    }
    require_committed(transaction.commit().map_err(display)?)?;
    close_writer(&mut writer)?;
    Ok(database)
}

fn create_membership_file(database: &TestDatabase) -> Result<(), String> {
    create_live(
        database.main(),
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").ok_or_else(|| "invalid benchmark value tag".to_owned())?,
        1,
        &CancellationToken::new(),
    )
    .map_err(display)?;
    Ok(())
}

fn create_shaped_feed(
    database: &TestDatabase,
    name: &str,
    size: usize,
    shape: FeedShape,
) -> Result<iprange_livedb::WorkflowReport, String> {
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(database.main(), transaction_budget(size, 2), &cancellation)
        .map_err(display)?;
    let report = {
        let mut workflow = writer
            .begin_create_feed(FeedName::new(name).map_err(display)?, &cancellation)
            .map_err(display)?;
        workflow
            .add_ranges_v4(&mut FeedShapeSource::new(size, shape)?)
            .map_err(display)?;
        let finished = workflow.finish_input().map_err(display)?;
        let report = *finished.report();
        match finished {
            FinishedWorkflow::Changed(prepared) => {
                require_committed(prepared.commit().map_err(display)?)?;
            }
            FinishedWorkflow::NoChange(report) => {
                return Err(format!("feed creation changed nothing: {report:?}"));
            }
        }
        report
    };
    close_writer(&mut writer)?;
    Ok(report)
}

fn feed_name(index: usize) -> Result<FeedName, String> {
    FeedName::new(&format!("feed-{index:06}")).map_err(display)
}

fn membership_reader_work(
    size: usize,
    feeds: usize,
    lookup: impl FnOnce(&str) -> iprange_livedb::Result<Option<iprange_livedb::FeedEntry>>,
) -> Result<(u32, usize, u64), String> {
    let name = feed_name(feeds - 1)?;
    let target = lookup(name.as_str())
        .map_err(display)?
        .ok_or_else(|| "target feed is absent".to_owned())?;
    let (repetitions, work_units) = reader_work(size)?;
    Ok((target.index, repetitions, work_units))
}

fn membership_scan_work(size: usize, feeds: usize) -> Result<(FeedName, usize, u64), String> {
    let name = feed_name(feeds - 1)?;
    let (repetitions, work_units) = reader_work(size)?;
    Ok((name, repetitions, work_units))
}

fn membership_contains(
    result: iprange_livedb::Result<Option<iprange_livedb::MembershipView<'_>>>,
    feed_index: u32,
) -> Result<bool, String> {
    let Some(membership) = result.map_err(display)? else {
        return Ok(false);
    };
    membership.contains_index(feed_index).map_err(display)
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}
