use std::hint::black_box;

use iprange_livedb::{
    snapshot_to, CancellationToken, ImmutableReader, LiveReader, RangeDirection,
    SnapshotPublicationPolicy, SnapshotSourceMode,
};

use crate::measure;
use crate::model::snapshot_budget;
use crate::scenarios::direct::seeded_direct;
use crate::scenarios::{
    close_reader, count_cursor, count_points, immutable_snapshot, reader_work, require_count,
    result, ScenarioResult,
};

pub(super) fn live_direct_lookup(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("live-direct-lookup", size, 1)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_points(size, repetitions, |address| {
            reader
                .lookup_direct_v4(address)
                .map(|value| value.is_some())
                .map_err(display)
        })
    });
    let hits = operation?;
    require_count("live direct lookup", hits, work_units, "addresses")?;
    close_reader(&mut reader)?;
    result(
        "live-direct-lookup",
        size,
        0,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_direct_lookup(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("immutable-direct-lookup", size, 1)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_points(size, repetitions, |address| {
            reader
                .lookup_direct_v4(address)
                .map(|value| value.is_some())
                .map_err(display)
        })
    });
    let hits = operation?;
    require_count("immutable direct lookup", hits, work_units, "addresses")?;
    drop(reader);
    result(
        "immutable-direct-lookup",
        size,
        0,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn live_direct_scan(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("live-direct-scan", size, 1)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_cursor(
            repetitions,
            || {
                reader
                    .direct_cursor_v4(RangeDirection::Forward)
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
    require_count("live direct scan", records, work_units, "ranges")?;
    close_reader(&mut reader)?;
    result(
        "live-direct-scan",
        size,
        0,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_direct_scan(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("immutable-direct-scan", size, 1)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_cursor(
            repetitions,
            || {
                reader
                    .direct_cursor_v4(RangeDirection::Forward)
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
    require_count("immutable direct scan", records, work_units, "ranges")?;
    drop(reader);
    result(
        "immutable-direct-scan",
        size,
        0,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn live_open(size: usize, capacity: usize) -> Result<ScenarioResult, String> {
    let capacity =
        u32::try_from(capacity.max(1)).map_err(|_| "reader capacity exceeds u32".to_owned())?;
    let database = seeded_direct("live-open", size, capacity)?;
    let iterations = 100u64;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut selected = 0u64;
        for _ in 0..iterations {
            let mut reader =
                LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
            selected ^= reader.info().map_err(display)?.transaction_id;
            close_reader(&mut reader)?;
        }
        Ok(black_box(selected))
    });
    let _ = operation?;
    result(
        "live-open",
        size,
        capacity as usize,
        iterations,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn snapshot(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("snapshot", size, 2)?;
    let output = database.snapshot();
    let (operation, measured) = measure::operation(|| {
        snapshot_to(
            database.main(),
            SnapshotSourceMode::Live,
            &output,
            SnapshotPublicationPolicy::FailIfExists,
            &snapshot_budget(size),
            &CancellationToken::new(),
        )
    });
    let snapshot = operation.map_err(|failure| format!("{failure:?}"))?;
    if snapshot.cleanup_state() != iprange_livedb::publication::CleanupState::Clean {
        return Err(format!("snapshot cleanup is incomplete: {snapshot:?}"));
    }
    result(
        "snapshot",
        size,
        0,
        size as u64,
        &database,
        measured,
        &output,
    )
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}
