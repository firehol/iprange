use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, FinishedWorkflow, LiveWriter, ValueKind,
    ValueTag,
};

use crate::measure;
use crate::model::{transaction_budget, TestDatabase};
use crate::scenarios::{close_writer, require_committed, result, ScenarioResult};
use crate::source::{AddressSource, DirectSource};

pub(super) fn replace(size: usize) -> Result<ScenarioResult, String> {
    let database = create_direct("direct", direct_tag(b"timestamp")?, 1)?;
    let (operation, measured) =
        measure::operation(|| apply_direct(&database, DirectSource::unordered(size)?, size));
    operation?;
    result(
        "direct-replace",
        size,
        0,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn nested(size: usize) -> Result<ScenarioResult, String> {
    let database = create_direct("nested", direct_tag(b"timestamp")?, 1)?;
    let (operation, measured) =
        measure::operation(|| apply_direct(&database, DirectSource::nested(size)?, size));
    operation?;
    result(
        "nested-overwrite",
        size,
        0,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn retention(size: usize) -> Result<ScenarioResult, String> {
    let database = create_direct("retention", ValueTag::RETENTION, 1)?;
    apply_retention(&database, AddressSource::new(size, 0)?, size, 100)?;
    let shift = (size / 10).max(1) as u32;
    let (operation, measured) = measure::operation(|| {
        apply_retention(&database, AddressSource::new(size, shift)?, size, 200)
    });
    operation?;
    result(
        "retention-refresh",
        size,
        0,
        size as u64,
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn seeded_direct(
    label: &str,
    size: usize,
    reader_capacity: u32,
) -> Result<TestDatabase, String> {
    let database = create_direct(label, direct_tag(b"timestamp")?, reader_capacity)?;
    apply_direct(&database, DirectSource::unordered(size)?, size)?;
    Ok(database)
}

fn create_direct(label: &str, tag: ValueTag, reader_capacity: u32) -> Result<TestDatabase, String> {
    let database = TestDatabase::new(label)?;
    create_live(
        database.main(),
        AddressFamily::Ipv4,
        ValueKind::Direct,
        tag,
        reader_capacity,
        &CancellationToken::new(),
    )
    .map_err(display)?;
    Ok(database)
}

fn apply_direct(
    database: &TestDatabase,
    mut input: DirectSource,
    size: usize,
) -> Result<(), String> {
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(database.main(), transaction_budget(size, 1), &cancellation)
        .map_err(display)?;
    {
        let mut workflow = writer
            .begin_direct_replacement(&cancellation)
            .map_err(display)?;
        workflow.add_ranges_v4(&mut input).map_err(display)?;
        match workflow.finish_input().map_err(display)? {
            FinishedWorkflow::Changed(prepared) => {
                require_committed(prepared.commit().map_err(display)?)?;
            }
            FinishedWorkflow::NoChange(report) => {
                return Err(format!(
                    "replacement unexpectedly changed nothing: {report:?}"
                ));
            }
        }
    }
    close_writer(&mut writer)
}

fn apply_retention(
    database: &TestDatabase,
    mut input: AddressSource,
    size: usize,
    refresh: u32,
) -> Result<(), String> {
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(database.main(), transaction_budget(size, 1), &cancellation)
        .map_err(display)?;
    let mut workflow = writer
        .begin_retention_refresh(refresh, &cancellation)
        .map_err(display)?;
    workflow.add_ranges_v4(&mut input).map_err(display)?;
    match workflow.finish_input().map_err(display)? {
        FinishedWorkflow::Changed(prepared) => {
            require_committed(prepared.commit().map_err(display)?)?;
        }
        FinishedWorkflow::NoChange(report) => {
            return Err(format!("refresh unexpectedly changed nothing: {report:?}"));
        }
    }
    close_writer(&mut writer)
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}

fn direct_tag(bytes: &[u8]) -> Result<ValueTag, String> {
    ValueTag::new(bytes).ok_or_else(|| "invalid benchmark value tag".to_owned())
}
