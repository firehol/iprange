use std::hint::black_box;

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, FeedName, FinishedWorkflow, Ipv4Key, LiveReader,
    LiveWriter, MembershipOperation, RangeDirection, ValueKind, ValueTag,
};

use crate::measure;
use crate::model::{transaction_budget, TestDatabase};
use crate::scenarios::{close_reader, close_writer, require_committed, result, ScenarioResult};
use crate::source::AddressSource;

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

pub(super) fn lookup(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("membership-lookup", size, feeds)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let target = reader
        .lookup_feed(feed_name(feeds - 1)?.as_str())
        .map_err(display)?
        .ok_or_else(|| "target feed is absent".to_owned())?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut hits = 0u64;
        for index in 0..size {
            let address = Ipv4Key((index as u32).saturating_mul(4));
            if let Some(membership) = reader.lookup_membership_v4(address).map_err(display)? {
                hits += u64::from(membership.contains_index(target.index).map_err(display)?);
            }
        }
        Ok(black_box(hits))
    });
    let hits = operation?;
    if hits != size as u64 {
        return Err(format!("membership lookup found {hits} of {size}"));
    }
    close_reader(&mut reader)?;
    result(
        "membership-lookup",
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn scan(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated("feed-scan", size, feeds)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let name = feed_name(feeds - 1)?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut cursor = reader
            .feed_range_cursor_v4(name.as_str(), RangeDirection::Forward)
            .map_err(display)?;
        let mut records = 0u64;
        while cursor.next_range().map_err(display)?.is_some() {
            records += 1;
        }
        Ok(black_box(records))
    });
    let records = operation?;
    if records != size as u64 {
        return Err(format!("feed scan returned {records} of {size} ranges"));
    }
    close_reader(&mut reader)?;
    result(
        "feed-scan",
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

fn populated(label: &str, ranges: usize, feeds: usize) -> Result<TestDatabase, String> {
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

fn feed_name(index: usize) -> Result<FeedName, String> {
    FeedName::new(&format!("feed-{index:06}")).map_err(display)
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}
