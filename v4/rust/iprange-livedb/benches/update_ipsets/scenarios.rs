use std::path::Path;

use iprange_livedb::{CloseOutcome, CommitDurability, CommitResult, LiveReader, LiveWriter};

use crate::measure::{self, FileSize, Measurement};
use crate::model::TestDatabase;

#[path = "scenarios/direct.rs"]
mod direct;
#[path = "scenarios/membership.rs"]
mod membership;
#[path = "scenarios/read.rs"]
mod read;

#[derive(Clone, Debug)]
pub(crate) struct ScenarioResult {
    pub(crate) name: &'static str,
    pub(crate) size: usize,
    pub(crate) auxiliary: usize,
    pub(crate) work_units: u64,
    pub(crate) range_records: u64,
    pub(crate) feeds: u64,
    pub(crate) measurement: Measurement,
    pub(crate) file: FileSize,
    pub(crate) private_artifacts: u64,
}

pub(crate) fn run(name: &str, size: usize, auxiliary: usize) -> Result<ScenarioResult, String> {
    if size == 0 {
        return Err("size must be positive".to_owned());
    }
    match name {
        "direct-replace" => direct::replace(size),
        "nested-overwrite" => direct::nested(size),
        "retention-refresh" => direct::retention(size),
        "feed-replace" => membership::replace_feed(size, auxiliary),
        "membership-lookup" => membership::lookup(size, auxiliary),
        "feed-scan" => membership::scan(size, auxiliary),
        "direct-lookup" => read::direct_lookup(size),
        "direct-scan" => read::direct_scan(size),
        "live-open" => read::live_open(size, auxiliary),
        "snapshot" => read::snapshot(size),
        _ => Err(format!("unknown scenario {name:?}")),
    }
}

pub(crate) fn result(
    name: &'static str,
    size: usize,
    auxiliary: usize,
    work_units: u64,
    database: &TestDatabase,
    measured: Measurement,
    file_path: &Path,
) -> Result<ScenarioResult, String> {
    let (range_records, feeds) = live_info(database)?;
    let private_artifacts = database.private_artifacts()?;
    if private_artifacts != 0 {
        return Err(format!(
            "{name} left {private_artifacts} private temporary artifacts"
        ));
    }
    Ok(ScenarioResult {
        name,
        size,
        auxiliary,
        work_units,
        range_records,
        feeds,
        measurement: measured,
        file: measure::file_size(file_path).map_err(|error| error.to_string())?,
        private_artifacts,
    })
}

pub(crate) fn require_committed(commit: CommitResult) -> Result<(), String> {
    if commit.durability != CommitDurability::Committed {
        return Err(format!("commit did not complete: {commit:?}"));
    }
    Ok(())
}

pub(crate) fn close_writer(writer: &mut LiveWriter) -> Result<(), String> {
    let result = writer.close().map_err(|error| error.to_string())?;
    if result.outcome != CloseOutcome::Closed {
        return Err(format!("writer close did not complete: {result:?}"));
    }
    Ok(())
}

pub(crate) fn close_reader(reader: &mut LiveReader) -> Result<(), String> {
    let result = reader.close().map_err(|error| error.to_string())?;
    if result.outcome != CloseOutcome::Closed {
        return Err(format!("reader close did not complete: {result:?}"));
    }
    Ok(())
}

fn live_info(database: &TestDatabase) -> Result<(u64, u64), String> {
    let cancellation = iprange_livedb::CancellationToken::new();
    let mut reader =
        LiveReader::open(database.main(), &cancellation).map_err(|error| error.to_string())?;
    let info = reader.info().map_err(|error| error.to_string())?;
    close_reader(&mut reader)?;
    Ok((info.range_record_count, info.active_feed_count))
}
