use std::hint::black_box;

use iprange_livedb::{
    create_immutable_feed_v4, create_live, AddressFamily, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraSetOperation, CancellationToken, DirectJoinBudget, DirectJoinCell, DirectJoinSink,
    DirectJoinSource, FeedCardinality, FeedName, FeedOverlap, FeedPair, FeedSelection,
    FinishedHistoryProjection, HistoryProjectionSource, HistoryWindow, LiveReader, LiveWriter,
    MembershipAggregateSink, MembershipAggregationMode, MembershipAlgebra, MembershipAlgebraBudget,
    MembershipCrossCell, MembershipJoinSink, MembershipQueryBudget, PublicationPolicy,
    PublicationStatus, StructureKind, UncoveredFeed, ValueKind, ValueTag,
};

use crate::measure;
use crate::model::{transaction_budget, TestDatabase};
use crate::scenarios::direct::{seeded_direct, seeded_direct_with_tag};
use crate::scenarios::membership::populated_rotating;
use crate::scenarios::{
    close_reader, close_writer, count_points, immutable_result, reader_work, require_committed,
    require_count, result, ImmutableResultSpec, ScenarioResult,
};
use crate::source::{FeedShape, FeedShapeSource};

#[path = "sdk/workflow.rs"]
mod workflow;

const QUERY_HEAP: u64 = 64 * 1024 * 1024;

pub(super) fn immutable_feed(size: usize) -> Result<ScenarioResult, String> {
    let database = TestDatabase::new("immutable-feed-random")?;
    let mut source = FeedShapeSource::new(size, FeedShape::RandomDisjoint)?;
    let (operation, measured) = measure::operation(|| {
        create_immutable_feed_v4(
            database.main(),
            tag(b"downloaded")?,
            feed_name(0)?,
            None,
            PublicationPolicy::FailIfExists,
            &mut source,
            &immutable_budget(size),
            &CancellationToken::new(),
        )
        .map_err(|failure| format!("{failure:?}"))
    });
    let report = operation?;
    require_published(report.publication.publication)?;
    if report.report.input_record_count != size as u64
        || report.report.normalized_interval_count != size as u64
    {
        return Err(format!("unexpected immutable-feed report: {report:?}"));
    }
    immutable_result(
        ImmutableResultSpec {
            name: "immutable-feed-random",
            size,
            auxiliary: 0,
            work_units: size as u64,
            emitted_units: report.report.normalized_interval_count,
        },
        &database,
        measured,
        database.main(),
    )
}

pub(super) fn history_project(size: usize, windows: usize) -> Result<ScenarioResult, String> {
    let windows = windows.max(1);
    let source = seeded_direct_with_tag("history-source", size, 2, ValueTag::LAST_SEEN)?;
    let destination = TestDatabase::new("history-project")?;
    create_membership(&destination)?;
    let requests = history_windows(windows)?;
    let cancellation = CancellationToken::new();
    let mut source_reader = LiveReader::open(source.main(), &cancellation).map_err(display)?;
    let (operation, measured) = measure::operation(|| -> Result<_, String> {
        let mut writer = LiveWriter::open(
            destination.main(),
            transaction_budget(size, windows),
            &cancellation,
        )
        .map_err(display)?;
        let report = match writer
            .project_history(
                HistoryProjectionSource::Live(&source_reader),
                &requests,
                &cancellation,
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
                    "new history projection changed nothing: {report:?}"
                ));
            }
        };
        close_writer(&mut writer)?;
        Ok(report)
    });
    let report = operation?;
    if report.source_range_count != size as u64 || report.windows.len() != windows {
        return Err(format!("unexpected history projection report: {report:?}"));
    }
    close_reader(&mut source_reader)?;
    let mut output = result(
        "history-project",
        size,
        windows,
        size as u64,
        &destination,
        measured,
        destination.main(),
    )?;
    output.emitted_units = report.after_interval_count;
    Ok(output)
}

pub(super) fn matching_feeds(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let width = feeds.min(4);
    let database = populated_rotating("membership-matching-feeds", size, feeds, width)?;
    let cancellation = CancellationToken::new();
    let mut reader = LiveReader::open(database.main(), &cancellation).map_err(display)?;
    let (repetitions, work_units) = reader_work(size)?;
    let mut emitted = 0u64;
    let (operation, measured) = measure::operation(|| {
        count_points(size, repetitions, |address| {
            let mut point_emitted = 0u64;
            let report = reader
                .membership_query()
                .map_err(display)?
                .matching_feeds_v4(
                    address,
                    &mut |name: FeedName| {
                        black_box(name);
                        point_emitted = point_emitted
                            .checked_add(1)
                            .ok_or(iprange_livedb::Error::ArithmeticOverflow("matches"))?;
                        Ok(())
                    },
                    &cancellation,
                )
                .map_err(display)?;
            if report.matching_feed_count != point_emitted {
                return Err("matching-feed report disagrees with its sink".to_owned());
            }
            emitted = emitted
                .checked_add(point_emitted)
                .ok_or_else(|| "matching-feed result count overflow".to_owned())?;
            Ok(point_emitted != 0)
        })
    });
    let hits = operation?;
    require_count("matching feeds", hits, work_units, "addresses")?;
    close_reader(&mut reader)?;
    let mut output = result(
        "membership-matching-feeds",
        size,
        feeds,
        work_units,
        &database,
        measured,
        database.main(),
    )?;
    output.emitted_units = emitted;
    Ok(output)
}

pub(super) fn aggregate_cardinalities(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    aggregate(
        "membership-cardinalities",
        size,
        feeds.max(1),
        feeds.clamp(1, 4),
        MembershipAggregationMode::Cardinalities,
    )
}

pub(super) fn aggregate_selected_pair(size: usize) -> Result<ScenarioResult, String> {
    let pair = [FeedPair {
        left: feed_name(0)?,
        right: feed_name(1)?,
    }];
    aggregate(
        "membership-selected-pair",
        size,
        2,
        1,
        MembershipAggregationMode::SelectedPairs(&pair),
    )
}

pub(super) fn aggregate_all_pairs(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    aggregate(
        "membership-all-pairs",
        size,
        feeds.max(2),
        feeds.max(2).div_ceil(2),
        MembershipAggregationMode::AllPairs,
    )
}

fn aggregate(
    name: &'static str,
    size: usize,
    feeds: usize,
    width: usize,
    mode: MembershipAggregationMode<'_>,
) -> Result<ScenarioResult, String> {
    let database = populated_rotating(name, size, feeds, width)?;
    let cancellation = CancellationToken::new();
    let mut reader = LiveReader::open(database.main(), &cancellation).map_err(display)?;
    let scope = reader
        .membership_query()
        .map_err(display)?
        .all_feeds(query_budget(), &cancellation)
        .map_err(display)?;
    let mut sink = AggregateCounter::default();
    let (operation, measured) = measure::operation(|| {
        scope
            .aggregate(mode, &mut sink, &cancellation)
            .map_err(display)
    });
    let report = operation?;
    if report.scanned_range_count != size as u64
        || report.feed_result_count != feeds as u64
        || sink.feeds != report.feed_result_count
        || sink.pairs != report.pair_result_count
    {
        return Err(format!(
            "unexpected membership aggregation report: {report:?}"
        ));
    }
    drop(scope);
    close_reader(&mut reader)?;
    let mut output = result(
        name,
        size,
        feeds,
        size as u64,
        &database,
        measured,
        database.main(),
    )?;
    output.emitted_units = report
        .feed_result_count
        .checked_add(report.pair_result_count)
        .ok_or_else(|| "aggregation result count overflow".to_owned())?;
    Ok(output)
}

pub(super) fn direct_join(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let membership = populated_rotating("direct-provider-join", size, feeds, feeds.min(4))?;
    let provider = seeded_direct("direct-provider", size, 1)?;
    let cancellation = CancellationToken::new();
    let mut membership_reader =
        LiveReader::open(membership.main(), &cancellation).map_err(display)?;
    let mut provider_reader = LiveReader::open(provider.main(), &cancellation).map_err(display)?;
    let scope = membership_reader
        .membership_query()
        .map_err(display)?
        .all_feeds(query_budget(), &cancellation)
        .map_err(display)?;
    let mut sink = DirectCounter::default();
    let result_cells = (feeds as u64)
        .checked_mul(size.min(251) as u64)
        .ok_or_else(|| "direct-join result budget overflow".to_owned())?;
    let (operation, measured) = measure::operation(|| {
        scope
            .join_direct(
                DirectJoinSource::Live(&provider_reader),
                DirectJoinBudget {
                    max_result_cells: result_cells,
                },
                &mut sink,
                &cancellation,
            )
            .map_err(display)
    });
    let report = operation?;
    if report.membership_range_count != size as u64
        || report.direct_ranges_visited != size as u64
        || report.joined_segment_count != size as u64
        || sink.cells != report.result_cell_count
    {
        return Err(format!("unexpected direct join report: {report:?}"));
    }
    drop(scope);
    close_reader(&mut provider_reader)?;
    close_reader(&mut membership_reader)?;
    let mut output = result(
        "direct-provider-join",
        size,
        feeds,
        size as u64,
        &membership,
        measured,
        membership.main(),
    )?;
    output.emitted_units = report.result_cell_count;
    Ok(output)
}

pub(super) fn membership_join(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let feeds = feeds.max(1);
    let width = feeds.min(4);
    let left = populated_rotating("membership-provider-left", size, feeds, width)?;
    let right = populated_rotating("membership-provider-right", size, feeds, width)?;
    let cancellation = CancellationToken::new();
    let mut left_reader = LiveReader::open(left.main(), &cancellation).map_err(display)?;
    let mut right_reader = LiveReader::open(right.main(), &cancellation).map_err(display)?;
    let left_scope = left_reader
        .membership_query()
        .map_err(display)?
        .all_feeds(query_budget(), &cancellation)
        .map_err(display)?;
    let right_scope = right_reader
        .membership_query()
        .map_err(display)?
        .all_feeds(query_budget(), &cancellation)
        .map_err(display)?;
    let mut sink = MembershipCounter::default();
    let (operation, measured) = measure::operation(|| {
        left_scope
            .join_membership(&right_scope, &mut sink, &cancellation)
            .map_err(display)
    });
    let report = operation?;
    if report.left_range_count != size as u64
        || report.right_range_count != size as u64
        || report.joined_segment_count != size as u64
        || sink.cross != report.cross_result_count
        || sink.uncovered != report.uncovered_result_count
    {
        return Err(format!("unexpected membership join report: {report:?}"));
    }
    drop(right_scope);
    drop(left_scope);
    close_reader(&mut right_reader)?;
    close_reader(&mut left_reader)?;
    let mut output = result(
        "membership-provider-join",
        size,
        feeds,
        size as u64,
        &left,
        measured,
        left.main(),
    )?;
    output.emitted_units = report
        .cross_result_count
        .checked_add(report.uncovered_result_count)
        .ok_or_else(|| "membership join result count overflow".to_owned())?;
    Ok(output)
}

pub(super) fn algebra_count(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    algebra_analysis("algebra-count", size, feeds, |algebra, cancellation| {
        algebra
            .count(FeedSelection::All, cancellation)
            .map(|report| (report.source_range_count, 1))
            .map_err(display)
    })
}

pub(super) fn algebra_compare(size: usize, feeds: usize) -> Result<ScenarioResult, String> {
    let left = [feed_name(0)?];
    let right = [feed_name(1)?];
    algebra_analysis(
        "algebra-compare",
        size,
        feeds.max(2),
        |algebra, cancellation| {
            algebra
                .compare(
                    FeedSelection::Named(&left),
                    FeedSelection::Named(&right),
                    cancellation,
                )
                .map(|report| (report.source_range_count, 1))
                .map_err(display)
        },
    )
}

fn algebra_analysis<F>(
    name: &'static str,
    size: usize,
    feeds: usize,
    operation: F,
) -> Result<ScenarioResult, String>
where
    F: FnOnce(&MembershipAlgebra<'_>, &CancellationToken) -> Result<(u64, u64), String>,
{
    let feeds = feeds.max(2);
    let width = feeds.min(4);
    let left = populated_rotating("algebra-analysis-left", size, feeds, width)?;
    let right = populated_rotating("algebra-analysis-right", size, feeds, width)?;
    let cancellation = CancellationToken::new();
    let mut left_reader = LiveReader::open(left.main(), &cancellation).map_err(display)?;
    let mut right_reader = LiveReader::open(right.main(), &cancellation).map_err(display)?;
    let left_scope = all_scope(&left_reader, &cancellation)?;
    let right_scope = all_scope(&right_reader, &cancellation)?;
    let scopes = [&left_scope, &right_scope];
    let algebra =
        MembershipAlgebra::new(&scopes, algebra_budget(), &cancellation).map_err(display)?;
    let (operation, measured) = measure::operation(|| operation(&algebra, &cancellation));
    let (scanned, emitted) = operation?;
    if scanned != (size as u64).saturating_mul(2) {
        return Err(format!("{name} scanned {scanned} source ranges"));
    }
    drop(algebra);
    drop(right_scope);
    drop(left_scope);
    close_reader(&mut right_reader)?;
    close_reader(&mut left_reader)?;
    let mut output = result(name, size, feeds, scanned, &left, measured, left.main())?;
    output.emitted_units = emitted;
    Ok(output)
}

pub(super) fn algebra_publish(
    size: usize,
    feeds: usize,
    flat: bool,
) -> Result<ScenarioResult, String> {
    let name = if flat {
        "algebra-publish-flat"
    } else {
        "algebra-publish-preserve"
    };
    let feeds = feeds.max(2);
    let width = feeds.min(4);
    let left = populated_rotating("algebra-publish-left", size, feeds, width)?;
    let right = populated_rotating("algebra-publish-right", size, feeds, width)?;
    let output_path = left.path("algebra-output.v4");
    let cancellation = CancellationToken::new();
    let mut left_reader = LiveReader::open(left.main(), &cancellation).map_err(display)?;
    let mut right_reader = LiveReader::open(right.main(), &cancellation).map_err(display)?;
    let left_scope = all_scope(&left_reader, &cancellation)?;
    let right_scope = all_scope(&right_reader, &cancellation)?;
    let scopes = [&left_scope, &right_scope];
    let algebra =
        MembershipAlgebra::new(&scopes, algebra_budget(), &cancellation).map_err(display)?;
    let mode = if flat {
        AlgebraOutputMode::Flat(FeedName::new("result").map_err(display)?)
    } else {
        AlgebraOutputMode::PreserveFeeds
    };
    let (operation, measured) = measure::operation(|| {
        algebra
            .publish_set(
                &output_path,
                tag(b"algebra")?,
                AlgebraSetOperation::Union(FeedSelection::All),
                mode,
                None,
                PublicationPolicy::FailIfExists,
                algebra_output_budget(size),
                &cancellation,
            )
            .map_err(|failure| format!("{failure:?}"))
    });
    let report = operation?;
    require_published(report.publication.publication)?;
    if report.report.source_range_count != (size as u64).saturating_mul(2)
        || report.report.output_range_count != size as u64
        || report.report.output_feed_count != if flat { 1 } else { feeds as u64 }
    {
        return Err(format!("unexpected algebra publication report: {report:?}"));
    }
    drop(algebra);
    drop(right_scope);
    drop(left_scope);
    close_reader(&mut right_reader)?;
    close_reader(&mut left_reader)?;
    immutable_result(
        ImmutableResultSpec {
            name,
            size,
            auxiliary: feeds,
            work_units: report.report.source_range_count,
            emitted_units: report.report.output_range_count,
        },
        &left,
        measured,
        &output_path,
    )
}

pub(super) fn update_ipsets_workflow(
    size: usize,
    windows: usize,
) -> Result<ScenarioResult, String> {
    workflow::run(size, windows)
}

#[derive(Default)]
struct AggregateCounter {
    feeds: u64,
    pairs: u64,
}

impl MembershipAggregateSink for AggregateCounter {
    fn feed_cardinalities(&mut self, batch: &[FeedCardinality]) -> iprange_livedb::Result<()> {
        black_box(batch);
        self.feeds += batch.len() as u64;
        Ok(())
    }

    fn feed_overlaps(&mut self, batch: &[FeedOverlap]) -> iprange_livedb::Result<()> {
        black_box(batch);
        self.pairs += batch.len() as u64;
        Ok(())
    }
}

#[derive(Default)]
struct DirectCounter {
    cells: u64,
}

impl DirectJoinSink for DirectCounter {
    fn direct_join_cells(&mut self, batch: &[DirectJoinCell]) -> iprange_livedb::Result<()> {
        black_box(batch);
        self.cells += batch.len() as u64;
        Ok(())
    }
}

#[derive(Default)]
struct MembershipCounter {
    cross: u64,
    uncovered: u64,
}

impl MembershipJoinSink for MembershipCounter {
    fn membership_cross_cells(
        &mut self,
        batch: &[MembershipCrossCell],
    ) -> iprange_livedb::Result<()> {
        black_box(batch);
        self.cross += batch.len() as u64;
        Ok(())
    }

    fn uncovered_feeds(&mut self, batch: &[UncoveredFeed]) -> iprange_livedb::Result<()> {
        black_box(batch);
        self.uncovered += batch.len() as u64;
        Ok(())
    }
}

fn create_membership(database: &TestDatabase) -> Result<(), String> {
    create_live(
        database.main(),
        AddressFamily::Ipv4,
        ValueKind::Membership,
        StructureKind::None,
        tag(b"membership")?,
        1,
        &CancellationToken::new(),
    )
    .map_err(display)?;
    Ok(())
}

fn all_scope<'a>(
    reader: &'a LiveReader,
    cancellation: &CancellationToken,
) -> Result<iprange_livedb::MembershipScope<'a>, String> {
    reader
        .membership_query()
        .map_err(display)?
        .all_feeds(query_budget(), cancellation)
        .map_err(display)
}

fn history_windows(count: usize) -> Result<Vec<HistoryWindow>, String> {
    let mut windows = Vec::new();
    windows
        .try_reserve_exact(count)
        .map_err(|_| "history benchmark window allocation failed".to_owned())?;
    for index in 0..count {
        let cutoff = ((index + 1) as u64)
            .saturating_mul(251)
            .checked_div((count + 1) as u64)
            .unwrap_or(0) as u32;
        windows.push(HistoryWindow {
            feed_name: FeedName::new(&format!("history-{index:06}")).map_err(display)?,
            cutoff,
        });
    }
    Ok(windows)
}

fn immutable_budget(size: usize) -> iprange_livedb::ImmutableFeedBudget {
    let pages = (size as u64).div_ceil(8).saturating_add(20_000);
    iprange_livedb::ImmutableFeedBudget::new(QUERY_HEAP, pages, pages, 3)
}

fn algebra_output_budget(size: usize) -> AlgebraOutputBudget {
    AlgebraOutputBudget {
        max_output_pages: (size as u64).div_ceil(8).saturating_add(20_000),
        max_open_files: 3,
    }
}

const fn query_budget() -> MembershipQueryBudget {
    MembershipQueryBudget {
        max_heap_bytes: QUERY_HEAP,
    }
}

const fn algebra_budget() -> MembershipAlgebraBudget {
    MembershipAlgebraBudget {
        max_heap_bytes: QUERY_HEAP,
        max_sources: 2,
    }
}

fn feed_name(index: usize) -> Result<FeedName, String> {
    FeedName::new(&format!("feed-{index:06}")).map_err(display)
}

fn tag(value: &[u8]) -> Result<ValueTag, String> {
    ValueTag::new(value).ok_or_else(|| "invalid benchmark value tag".to_owned())
}

fn require_published(status: PublicationStatus) -> Result<(), String> {
    if status != PublicationStatus::Published {
        return Err(format!("publication did not complete: {status:?}"));
    }
    Ok(())
}

fn display(error: impl std::fmt::Display) -> String {
    error.to_string()
}
