use std::path::{Path, PathBuf};

use iprange_livedb::{
    create_immutable_feed_v4, create_live, AddressFamily, AlgebraOutputMode, AlgebraSetOperation,
    CancellationToken, DirectJoinBudget, DirectJoinSource, FeedSelection,
    FinishedHistoryProjection, FinishedWorkflow, HistoryProjectionSource, ImmutableReader,
    LiveReader, LiveWriter, MembershipAggregationMode, MembershipAlgebra, PublicationPolicy,
    RangeDirection, ValueKind, ValueTag, WorkflowReport,
};

use crate::measure;
use crate::model::{transaction_budget, TestDatabase};
use crate::scenarios::direct::seeded_direct;
use crate::scenarios::membership::populated;
use crate::scenarios::{
    close_reader, close_writer, immutable_result, require_committed, validate_output,
    ImmutableResultSpec, ScenarioResult,
};
use crate::source::AddressSource;

use super::{
    algebra_budget, algebra_output_budget, all_scope, history_windows, immutable_budget,
    require_published, tag, AggregateCounter, DirectCounter, MembershipCounter,
};

struct Files {
    previous: PathBuf,
    current: PathBuf,
    first_seen: PathBuf,
    last_seen: PathBuf,
    central: PathBuf,
    output: PathBuf,
}

struct Report {
    scanned: u64,
    emitted: u64,
}

pub(super) fn run(size: usize, windows: usize) -> Result<ScenarioResult, String> {
    let windows = windows.max(1);
    let workspace = TestDatabase::new("update-ipsets-workflow")?;
    let files = Files {
        previous: workspace.path("previous.v4"),
        current: workspace.path("current.v4"),
        first_seen: workspace.path("first-seen.v4"),
        last_seen: workspace.path("last-seen.v4"),
        central: workspace.path("central.v4"),
        output: workspace.path("output.v4"),
    };
    create_live_file(&files.first_seen, ValueKind::Direct, ValueTag::FIRST_SEEN)?;
    create_live_file(&files.last_seen, ValueKind::Direct, ValueTag::LAST_SEEN)?;
    create_live_file(&files.central, ValueKind::Membership, tag(b"membership")?)?;
    let requests = history_windows(windows)?;
    let cancellation = CancellationToken::new();
    seed_prior_round(&files, &requests, size, &cancellation)?;
    let direct_provider = seeded_direct("complete-direct-provider", size, 1)?;
    let membership_provider = populated("complete-membership-provider", size, 2)?;
    let shift = (size / 10).max(1) as u32;
    let mut source = AddressSource::new(size, shift)?;

    let (operation, measured) = measure::operation(|| {
        execute(
            &files,
            &direct_provider,
            &membership_provider,
            &requests,
            &mut source,
            size,
            &cancellation,
        )
    });
    let report = operation?;
    validate_output(&files.current, false)?;
    validate_output(&files.previous, false)?;
    validate_output(&files.first_seen, true)?;
    validate_output(&files.last_seen, true)?;
    validate_output(&files.central, true)?;
    immutable_result(
        ImmutableResultSpec {
            name: "update-ipsets-workflow",
            size,
            auxiliary: windows,
            work_units: report.scanned,
            emitted_units: report.emitted,
        },
        &workspace,
        measured,
        &files.output,
    )
}

#[allow(clippy::too_many_arguments)]
fn execute(
    files: &Files,
    direct_provider: &TestDatabase,
    membership_provider: &TestDatabase,
    windows: &[iprange_livedb::HistoryWindow],
    source: &mut AddressSource,
    size: usize,
    cancellation: &CancellationToken,
) -> Result<Report, String> {
    let current = create_immutable_feed_v4(
        &files.current,
        tag(b"downloaded")?,
        super::feed_name(0)?,
        None,
        PublicationPolicy::FailIfExists,
        source,
        &immutable_budget(size),
        cancellation,
    )
    .map_err(|failure| format!("{failure:?}"))?;
    require_published(current.publication.publication)?;
    let current_reader = ImmutableReader::open(&files.current).map_err(display)?;

    let first = refresh_first_seen(&files.first_seen, &current_reader, size, 200, cancellation)?;
    let last = refresh_last_seen(
        &files.last_seen,
        &current_reader,
        size,
        200,
        0,
        cancellation,
    )?;
    let base = apply_base_feed(&files.central, &current_reader, size, true, cancellation)?;

    let mut last_reader = LiveReader::open(&files.last_seen, cancellation).map_err(display)?;
    let history = project_history(&files.central, &last_reader, windows, size, cancellation)?;

    let mut central_reader = LiveReader::open(&files.central, cancellation).map_err(display)?;
    let mut direct_reader =
        LiveReader::open(direct_provider.main(), cancellation).map_err(display)?;
    let mut membership_reader =
        LiveReader::open(membership_provider.main(), cancellation).map_err(display)?;
    let central_scope = all_scope(&central_reader, cancellation)?;
    let provider_scope = all_scope(&membership_reader, cancellation)?;

    let mut aggregate_sink = AggregateCounter::default();
    let aggregate = central_scope
        .aggregate(
            MembershipAggregationMode::TargetAgainstScope(super::feed_name(0)?),
            &mut aggregate_sink,
            cancellation,
        )
        .map_err(display)?;

    let result_limit = (windows.len() as u64)
        .checked_add(1)
        .and_then(|feeds| feeds.checked_mul(252))
        .ok_or_else(|| "complete-workflow direct result limit overflow".to_owned())?;
    let mut direct_sink = DirectCounter::default();
    let direct = central_scope
        .join_direct(
            DirectJoinSource::Live(&direct_reader),
            DirectJoinBudget {
                max_result_cells: result_limit,
            },
            &mut direct_sink,
            cancellation,
        )
        .map_err(display)?;

    let mut membership_sink = MembershipCounter::default();
    let membership = central_scope
        .join_membership(&provider_scope, &mut membership_sink, cancellation)
        .map_err(display)?;

    let scopes = [&central_scope, &provider_scope];
    let algebra =
        MembershipAlgebra::new(&scopes, algebra_budget(), cancellation).map_err(display)?;
    let algebra_output = algebra
        .publish_set(
            &files.output,
            tag(b"published")?,
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            algebra_output_budget(size),
            cancellation,
        )
        .map_err(|failure| format!("{failure:?}"))?;
    require_published(algebra_output.publication.publication)?;

    let output_reader = ImmutableReader::open(&files.output).map_err(display)?;
    let final_ranges = {
        let mut cursor = output_reader
            .feed_range_cursor_v4(super::feed_name(0)?.as_str(), RangeDirection::Forward)
            .map_err(display)?;
        let mut final_ranges = 0u64;
        while cursor.next_range().map_err(display)?.is_some() {
            final_ranges = final_ranges
                .checked_add(1)
                .ok_or_else(|| "complete-workflow final range count overflow".to_owned())?;
        }
        final_ranges
    };
    let expected_final_ranges = size
        .checked_add((size / 10).max(1))
        .ok_or_else(|| "complete-workflow final range count overflow".to_owned())?
        as u64;
    if final_ranges != expected_final_ranges {
        return Err(format!(
            "complete workflow enumerated {final_ranges} of {expected_final_ranges} globally merged base ranges"
        ));
    }
    drop(output_reader);
    drop(algebra);
    drop(provider_scope);
    drop(central_scope);
    close_reader(&mut membership_reader)?;
    close_reader(&mut direct_reader)?;
    close_reader(&mut central_reader)?;
    close_reader(&mut last_reader)?;
    drop(current_reader);

    let scanned = checked_sum(&[
        current.report.input_record_count,
        first.input_record_count,
        last.input_record_count,
        base.input_record_count,
        history.source_range_count,
        aggregate.scanned_range_count,
        direct.membership_range_count,
        direct.direct_ranges_visited,
        membership.left_range_count,
        membership.right_range_count,
        algebra_output.report.source_range_count,
        final_ranges,
    ])?;
    let emitted = checked_sum(&[
        current.report.normalized_interval_count,
        history.after_interval_count,
        aggregate.feed_result_count,
        aggregate.pair_result_count,
        direct.result_cell_count,
        membership.cross_result_count,
        membership.uncovered_result_count,
        algebra_output.report.output_range_count,
    ])?;
    Ok(Report { scanned, emitted })
}

fn refresh_first_seen(
    path: &Path,
    current: &ImmutableReader,
    size: usize,
    refresh: u32,
    cancellation: &CancellationToken,
) -> Result<WorkflowReport, String> {
    let mut writer =
        LiveWriter::open(path, transaction_budget(size, 1), cancellation).map_err(display)?;
    let mut workflow = writer
        .begin_first_seen_refresh(refresh, cancellation)
        .map_err(display)?;
    workflow
        .add_ranges_v4(
            &mut current
                .named_feed_source_v4(super::feed_name(0)?.as_str())
                .map_err(display)?,
        )
        .map_err(display)?;
    let report = commit_workflow(workflow.finish_input().map_err(display)?)?;
    close_writer(&mut writer)?;
    Ok(report)
}

fn refresh_last_seen(
    path: &Path,
    current: &ImmutableReader,
    size: usize,
    refresh: u32,
    cutoff: u32,
    cancellation: &CancellationToken,
) -> Result<WorkflowReport, String> {
    let mut writer =
        LiveWriter::open(path, transaction_budget(size, 1), cancellation).map_err(display)?;
    let mut workflow = writer
        .begin_last_seen_refresh(refresh, cutoff, cancellation)
        .map_err(display)?;
    workflow
        .add_ranges_v4(
            &mut current
                .named_feed_source_v4(super::feed_name(0)?.as_str())
                .map_err(display)?,
        )
        .map_err(display)?;
    let report = commit_workflow(workflow.finish_input().map_err(display)?)?;
    close_writer(&mut writer)?;
    Ok(report)
}

fn apply_base_feed(
    path: &Path,
    current: &ImmutableReader,
    size: usize,
    replace: bool,
    cancellation: &CancellationToken,
) -> Result<WorkflowReport, String> {
    let mut writer =
        LiveWriter::open(path, transaction_budget(size, 1), cancellation).map_err(display)?;
    let feed = super::feed_name(0)?;
    let report = if replace {
        let mut workflow = writer
            .begin_replace_feed(feed, cancellation)
            .map_err(display)?;
        workflow
            .add_ranges_v4(
                &mut current
                    .named_feed_source_v4(feed.as_str())
                    .map_err(display)?,
            )
            .map_err(display)?;
        commit_workflow(workflow.finish_input().map_err(display)?)?
    } else {
        let mut workflow = writer
            .begin_create_feed(feed, cancellation)
            .map_err(display)?;
        workflow
            .add_ranges_v4(
                &mut current
                    .named_feed_source_v4(feed.as_str())
                    .map_err(display)?,
            )
            .map_err(display)?;
        commit_workflow(workflow.finish_input().map_err(display)?)?
    };
    close_writer(&mut writer)?;
    Ok(report)
}

fn seed_prior_round(
    files: &Files,
    windows: &[iprange_livedb::HistoryWindow],
    size: usize,
    cancellation: &CancellationToken,
) -> Result<(), String> {
    let mut source = AddressSource::new(size, 0)?;
    let previous = create_immutable_feed_v4(
        &files.previous,
        tag(b"downloaded")?,
        super::feed_name(0)?,
        None,
        PublicationPolicy::FailIfExists,
        &mut source,
        &immutable_budget(size),
        cancellation,
    )
    .map_err(|failure| format!("{failure:?}"))?;
    require_published(previous.publication.publication)?;
    let previous_reader = ImmutableReader::open(&files.previous).map_err(display)?;
    refresh_first_seen(&files.first_seen, &previous_reader, size, 100, cancellation)?;
    refresh_last_seen(
        &files.last_seen,
        &previous_reader,
        size,
        100,
        0,
        cancellation,
    )?;
    apply_base_feed(&files.central, &previous_reader, size, false, cancellation)?;
    drop(previous_reader);

    let mut last_reader = LiveReader::open(&files.last_seen, cancellation).map_err(display)?;
    project_history(&files.central, &last_reader, windows, size, cancellation)?;
    close_reader(&mut last_reader)
}

fn project_history(
    path: &Path,
    last_seen: &LiveReader,
    windows: &[iprange_livedb::HistoryWindow],
    size: usize,
    cancellation: &CancellationToken,
) -> Result<iprange_livedb::HistoryProjectionReport, String> {
    let mut writer = LiveWriter::open(
        path,
        transaction_budget(size, windows.len() + 1),
        cancellation,
    )
    .map_err(display)?;
    let report = match writer
        .project_history(
            HistoryProjectionSource::Live(last_seen),
            windows,
            cancellation,
        )
        .map_err(display)?
    {
        FinishedHistoryProjection::Changed(prepared) => {
            let report = prepared.report().clone();
            require_committed(prepared.commit().map_err(display)?)?;
            report
        }
        FinishedHistoryProjection::NoChange(report) => {
            return Err(format!(
                "complete history projection changed nothing: {report:?}"
            ));
        }
    };
    close_writer(&mut writer)?;
    Ok(report)
}

fn commit_workflow(finished: FinishedWorkflow<'_>) -> Result<WorkflowReport, String> {
    match finished {
        FinishedWorkflow::Changed(prepared) => {
            let report = *prepared.report();
            require_committed(prepared.commit().map_err(display)?)?;
            Ok(report)
        }
        FinishedWorkflow::NoChange(report) => Err(format!(
            "complete workflow unexpectedly changed nothing: {report:?}"
        )),
    }
}

fn create_live_file(path: &Path, kind: ValueKind, tag: ValueTag) -> Result<(), String> {
    create_live(
        path,
        AddressFamily::Ipv4,
        kind,
        tag,
        1,
        &CancellationToken::new(),
    )
    .map_err(display)?;
    Ok(())
}

fn checked_sum(values: &[u64]) -> Result<u64, String> {
    values.iter().try_fold(0u64, |total, value| {
        total
            .checked_add(*value)
            .ok_or_else(|| "complete-workflow counter overflow".to_owned())
    })
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}
