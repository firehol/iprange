use std::hint::black_box;
use std::path::{Path, PathBuf};

use iprange_livedb::{
    snapshot_to,
    validation::{validate, ValidationBudget, ValidationMode, ValidationSinkControl},
    CancellationToken, CloseOutcome, CommitDurability, CommitResult, Ipv4Key, LiveReader,
    LiveWriter, SnapshotPublicationPolicy, SnapshotSourceMode,
};

use crate::measure::{self, FileSize, Measurement};
use crate::model::{snapshot_budget, TestDatabase};
use crate::source::FeedShape;

#[path = "scenarios/direct.rs"]
mod direct;
#[path = "scenarios/membership.rs"]
mod membership;
#[path = "scenarios/read.rs"]
mod read;
#[path = "scenarios/sdk.rs"]
mod sdk;

#[derive(Clone, Debug)]
pub(crate) struct ScenarioResult {
    pub(crate) name: &'static str,
    pub(crate) size: usize,
    pub(crate) auxiliary: usize,
    pub(crate) work_units: u64,
    pub(crate) emitted_units: u64,
    pub(crate) range_records: u64,
    pub(crate) feeds: u64,
    pub(crate) measurement: Measurement,
    pub(crate) file: FileSize,
    pub(crate) private_artifacts: u64,
}

pub(crate) struct ImmutableResultSpec {
    pub(crate) name: &'static str,
    pub(crate) size: usize,
    pub(crate) auxiliary: usize,
    pub(crate) work_units: u64,
    pub(crate) emitted_units: u64,
}

pub(crate) fn run(name: &str, size: usize, auxiliary: usize) -> Result<ScenarioResult, String> {
    if size == 0 {
        return Err("size must be positive".to_owned());
    }
    match name {
        "direct-replace" => direct::replace(size),
        "nested-overwrite" => direct::nested(size),
        "first-seen-refresh" => direct::first_seen(size),
        "last-seen-refresh" => direct::last_seen(size),
        "feed-replace" => membership::replace_feed(size, auxiliary),
        "feed-first-ascending" => membership::shaped_feed(
            "feed-first-ascending",
            size,
            FeedShape::AscendingDisjoint,
            false,
        ),
        "feed-second-ascending" => membership::shaped_feed(
            "feed-second-ascending",
            size,
            FeedShape::AscendingDisjoint,
            true,
        ),
        "feed-first-descending" => membership::shaped_feed(
            "feed-first-descending",
            size,
            FeedShape::DescendingDisjoint,
            false,
        ),
        "feed-second-descending" => membership::shaped_feed(
            "feed-second-descending",
            size,
            FeedShape::DescendingDisjoint,
            true,
        ),
        "feed-first-random" => {
            membership::shaped_feed("feed-first-random", size, FeedShape::RandomDisjoint, false)
        }
        "feed-second-random" => {
            membership::shaped_feed("feed-second-random", size, FeedShape::RandomDisjoint, true)
        }
        "feed-first-overlap" => membership::shaped_feed(
            "feed-first-overlap",
            size,
            FeedShape::RandomOverlapChain,
            false,
        ),
        "feed-second-overlap" => membership::shaped_feed(
            "feed-second-overlap",
            size,
            FeedShape::RandomOverlapChain,
            true,
        ),
        "membership-import" => membership::import(size, auxiliary),
        "live-membership-lookup" => membership::live_lookup(size, auxiliary),
        "immutable-membership-lookup" => membership::immutable_lookup(size, auxiliary),
        "live-feed-scan" => membership::live_scan(size, auxiliary),
        "immutable-feed-scan" => membership::immutable_scan(size, auxiliary),
        "live-direct-lookup" => read::live_direct_lookup(size),
        "immutable-direct-lookup" => read::immutable_direct_lookup(size),
        "live-direct-scan" => read::live_direct_scan(size),
        "immutable-direct-scan" => read::immutable_direct_scan(size),
        "live-open" => read::live_open(size, auxiliary),
        "snapshot" => read::snapshot(size),
        "immutable-feed-random" => sdk::immutable_feed(size),
        "history-project" => sdk::history_project(size, auxiliary),
        "membership-matching-feeds" => sdk::matching_feeds(size, auxiliary),
        "membership-cardinalities" => sdk::aggregate_cardinalities(size, auxiliary),
        "membership-selected-pair" => sdk::aggregate_selected_pair(size),
        "membership-all-pairs" => sdk::aggregate_all_pairs(size, auxiliary),
        "direct-provider-join" => sdk::direct_join(size, auxiliary),
        "membership-provider-join" => sdk::membership_join(size, auxiliary),
        "algebra-count" => sdk::algebra_count(size, auxiliary),
        "algebra-compare" => sdk::algebra_compare(size, auxiliary),
        "algebra-publish-preserve" => sdk::algebra_publish(size, auxiliary, false),
        "algebra-publish-flat" => sdk::algebra_publish(size, auxiliary, true),
        "update-ipsets-workflow" => sdk::update_ipsets_workflow(size, auxiliary),
        _ => Err(format!("unknown scenario {name:?}")),
    }
}

pub(crate) fn reader_work(size: usize) -> Result<(usize, u64), String> {
    if size == 0 {
        return Err("reader benchmark size must be nonzero".to_owned());
    }
    let repetitions = 1_000_000usize.div_ceil(size);
    let units = size
        .checked_mul(repetitions)
        .ok_or_else(|| "reader benchmark work count overflows usize".to_owned())?;
    Ok((
        repetitions,
        u64::try_from(units).map_err(|_| "reader benchmark work count exceeds u64".to_owned())?,
    ))
}

pub(crate) fn count_points(
    size: usize,
    repetitions: usize,
    mut present: impl FnMut(Ipv4Key) -> Result<bool, String>,
) -> Result<u64, String> {
    let mut hits = 0u64;
    for _ in 0..repetitions {
        for index in 0..size {
            let address = Ipv4Key((index as u32).saturating_mul(4));
            hits += u64::from(present(address)?);
        }
    }
    Ok(black_box(hits))
}

pub(crate) fn count_cursor<C>(
    repetitions: usize,
    mut open: impl FnMut() -> Result<C, String>,
    mut advance: impl FnMut(&mut C) -> Result<bool, String>,
) -> Result<u64, String> {
    let mut records = 0u64;
    for _ in 0..repetitions {
        let mut cursor = open()?;
        while advance(&mut cursor)? {
            records += 1;
        }
    }
    Ok(black_box(records))
}

pub(crate) fn require_count(
    label: &str,
    observed: u64,
    expected: u64,
    noun: &str,
) -> Result<(), String> {
    if observed != expected {
        return Err(format!("{label} returned {observed} of {expected} {noun}"));
    }
    Ok(())
}

pub(crate) fn immutable_snapshot(
    database: &TestDatabase,
    records: usize,
) -> Result<PathBuf, String> {
    let output = database.snapshot();
    let snapshot = snapshot_to(
        database.main(),
        SnapshotSourceMode::Live,
        &output,
        SnapshotPublicationPolicy::FailIfExists,
        &snapshot_budget(records),
        &CancellationToken::new(),
    )
    .map_err(|failure| format!("{failure:?}"))?;
    if snapshot.cleanup_state() != iprange_livedb::publication::CleanupState::Clean {
        return Err(format!("snapshot cleanup is incomplete: {snapshot:?}"));
    }
    Ok(output)
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
    validate_output(file_path, file_path == database.main())?;
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
        emitted_units: 0,
        range_records,
        feeds,
        measurement: measured,
        file: measure::file_size(file_path).map_err(|error| error.to_string())?,
        private_artifacts,
    })
}

pub(crate) fn immutable_result(
    spec: ImmutableResultSpec,
    database: &TestDatabase,
    measured: Measurement,
    file_path: &Path,
) -> Result<ScenarioResult, String> {
    validate_output(file_path, false)?;
    let reader =
        iprange_livedb::ImmutableReader::open(file_path).map_err(|error| error.to_string())?;
    let info = reader.info();
    drop(reader);
    let private_artifacts = database.private_artifacts()?;
    if private_artifacts != 0 {
        return Err(format!(
            "{} left {private_artifacts} private temporary artifacts",
            spec.name
        ));
    }
    Ok(ScenarioResult {
        name: spec.name,
        size: spec.size,
        auxiliary: spec.auxiliary,
        work_units: spec.work_units,
        emitted_units: spec.emitted_units,
        range_records: info.range_record_count,
        feeds: info.active_feed_count,
        measurement: measured,
        file: measure::file_size(file_path).map_err(|error| error.to_string())?,
        private_artifacts,
    })
}

fn validate_output(path: &Path, live: bool) -> Result<(), String> {
    let mode = if live {
        ValidationMode::LiveCurrent
    } else {
        ValidationMode::ImmutableCurrent
    };
    let mut sink =
        |_: &iprange_livedb::validation::ValidationFinding| Ok(ValidationSinkControl::Continue);
    let validated = validate(
        path,
        mode,
        &ValidationBudget::heap_only(64 * 1024 * 1024, 2),
        &iprange_livedb::CancellationToken::new(),
        &mut sink,
    )
    .map_err(|failure| format!("benchmark output validation failed: {failure:?}"))?;
    if !validated.valid {
        return Err(format!(
            "benchmark output has {} validation findings",
            validated.progress.finding_count
        ));
    }
    Ok(())
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
