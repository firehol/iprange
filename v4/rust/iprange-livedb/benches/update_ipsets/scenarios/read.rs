use std::hint::black_box;

use iprange_livedb::{
    snapshot_to,
    validation::{validate, ValidationBudget, ValidationMode, ValidationSinkControl},
    CancellationToken, ImmutableReader, LiveReader, RangeDirection, SnapshotPublicationPolicy,
    SnapshotSourceMode,
};

use crate::measure;
use crate::model::snapshot_budget;
use crate::scenarios::direct::{seeded_direct, seeded_direct_v6};
use crate::scenarios::membership::populated_rotating;
use crate::scenarios::{
    close_reader, count_cursor, count_points, count_random_points, count_random_points_v6,
    immutable_snapshot, random_points, random_points_v6, reader_work, require_count, result,
    ScenarioResult,
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

pub(super) fn live_direct_random_lookup(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("live-direct-random-lookup", size, 1)?;
    let points = random_points(size)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_random_points(&points, repetitions, |address| {
            reader
                .lookup_direct_v4(address)
                .map(|value| value.is_some())
                .map_err(display)
        })
    });
    let hits = operation?;
    require_count("live random direct lookup", hits, work_units, "addresses")?;
    close_reader(&mut reader)?;
    result(
        "live-direct-random-lookup",
        size,
        0,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_direct_random_lookup(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("immutable-direct-random-lookup", size, 1)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let points = random_points(size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_random_points(&points, repetitions, |address| {
            reader
                .lookup_direct_v4(address)
                .map(|value| value.is_some())
                .map_err(display)
        })
    });
    let hits = operation?;
    require_count(
        "immutable random direct lookup",
        hits,
        work_units,
        "addresses",
    )?;
    drop(reader);
    result(
        "immutable-direct-random-lookup",
        size,
        0,
        work_units,
        &database,
        measured,
        &snapshot,
    )
}

pub(super) fn live_direct_random_lookup_v6(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct_v6("live-direct-random-lookup-v6", size, 1)?;
    let points = random_points_v6(size)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_random_points_v6(&points, repetitions, |address| {
            reader
                .lookup_direct_v6(address)
                .map(|value| value.is_some())
                .map_err(display)
        })
    });
    let hits = operation?;
    require_count("live random direct v6 lookup", hits, work_units, "addresses")?;
    close_reader(&mut reader)?;
    result(
        "live-direct-random-lookup-v6",
        size,
        0,
        work_units,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn immutable_direct_random_lookup_v6(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct_v6("immutable-direct-random-lookup-v6", size, 1)?;
    let snapshot = immutable_snapshot(&database, size)?;
    let points = random_points_v6(size)?;
    let reader = ImmutableReader::open(&snapshot).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let (operation, measured) = measure::operation(|| {
        count_random_points_v6(&points, repetitions, |address| {
            reader
                .lookup_direct_v6(address)
                .map(|value| value.is_some())
                .map_err(display)
        })
    });
    let hits = operation?;
    require_count(
        "immutable random direct v6 lookup",
        hits,
        work_units,
        "addresses",
    )?;
    drop(reader);
    result(
        "immutable-direct-random-lookup-v6",
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

pub(super) fn live_validation(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("live-validation", size, 1)?;
    validate_case(
        "live-validation",
        size,
        &database,
        database.main(),
        ValidationMode::LiveCurrent,
        0,
    )
}

pub(super) fn immutable_validation(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("immutable-validation", size, 1)?;
    let snapshot = immutable_snapshot(&database, size)?;
    validate_case(
        "immutable-validation",
        size,
        &database,
        &snapshot,
        ValidationMode::ImmutableCurrent,
        0,
    )
}

pub(super) fn live_membership_validation(
    size: usize,
    feeds: usize,
) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let database = populated_rotating("live-membership-validation", size, feeds, feeds.min(8))?;
    validate_case(
        "live-membership-validation",
        size,
        &database,
        database.main(),
        ValidationMode::LiveCurrent,
        feeds,
    )
}

fn validate_case(
    name: &'static str,
    size: usize,
    database: &crate::model::TestDatabase,
    file: &std::path::Path,
    mode: ValidationMode,
    auxiliary: usize,
) -> Result<ScenarioResult, String> {
    let (operation, measured) = measure::operation(|| {
        let mut sink =
            |_: &iprange_livedb::validation::ValidationFinding| Ok(ValidationSinkControl::Continue);
        validate(
            file,
            mode,
            &ValidationBudget::heap_only(64 * 1024 * 1024, 2),
            &CancellationToken::new(),
            &mut sink,
        )
    });
    let validated = operation.map_err(|failure| format!("{failure:?}"))?;
    if !validated.valid {
        return Err(format!(
            "{name} found {} validation failures",
            validated.progress.finding_count
        ));
    }
    result(
        name,
        size,
        auxiliary,
        validated.progress.checked_unique_pages,
        database,
        measured,
        file,
    )
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}
