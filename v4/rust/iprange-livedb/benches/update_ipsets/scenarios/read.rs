use std::hint::black_box;

use iprange_livedb::{
    snapshot_to, CancellationToken, Ipv4Key, LiveReader, RangeDirection, SnapshotPublicationPolicy,
    SnapshotSourceMode,
};

use crate::measure;
use crate::model::snapshot_budget;
use crate::scenarios::direct::seeded_direct;
use crate::scenarios::{close_reader, result, ScenarioResult};

pub(super) fn direct_lookup(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("direct-lookup", size, 1)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut hits = 0u64;
        for index in 0..size {
            let address = Ipv4Key((index as u32).saturating_mul(4));
            hits += u64::from(reader.lookup_direct_v4(address).map_err(display)?.is_some());
        }
        Ok(black_box(hits))
    });
    let hits = operation?;
    if hits != size as u64 {
        return Err(format!("direct lookup found {hits} of {size}"));
    }
    close_reader(&mut reader)?;
    result(
        "direct-lookup",
        size,
        0,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn direct_scan(size: usize) -> Result<ScenarioResult, String> {
    let database = seeded_direct("direct-scan", size, 1)?;
    let mut reader =
        LiveReader::open(database.main(), &CancellationToken::new()).map_err(display)?;
    let (operation, measured) = measure::operation(|| -> Result<u64, String> {
        let mut cursor = reader
            .direct_cursor_v4(RangeDirection::Forward)
            .map_err(display)?;
        let mut records = 0u64;
        while cursor.next_range().map_err(display)?.is_some() {
            records += 1;
        }
        Ok(black_box(records))
    });
    let records = operation?;
    if records != size as u64 {
        return Err(format!("direct scan returned {records} of {size} ranges"));
    }
    close_reader(&mut reader)?;
    result(
        "direct-scan",
        size,
        0,
        size as u64,
        &database,
        measured,
        database.main(),
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
