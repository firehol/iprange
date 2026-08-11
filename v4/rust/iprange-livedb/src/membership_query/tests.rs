use std::fs;
use std::path::{Path, PathBuf};

use crate::test_alloc::{measure_thread_allocations, AllocationStats};
use crate::{
    create_live, AddressFamily, AddressRange, AlgebraCountReport, AlgebraOutputBudget,
    AlgebraOutputMode, AlgebraSetOperation, CancellationToken, DirectJoinBudget, DirectJoinCell,
    DirectJoinReport, DirectJoinSink, DirectJoinSource, DirectRange, FeedCardinality, FeedName,
    FeedOverlap, FeedSelection, FinishedWorkflow, Ipv4Key, LiveReader, LiveWriter,
    MembershipAggregateSink, MembershipAggregationMode, MembershipAlgebra, MembershipAlgebraBudget,
    MembershipCrossCell, MembershipJoinReport, MembershipJoinSink, MembershipQueryBudget,
    PublicationPolicy, TransactionBudget, UncoveredFeed, ValueKind, ValueTag,
};

struct Files(Vec<PathBuf>);

impl Files {
    fn new() -> Self {
        Self(Vec::new())
    }

    fn path(&mut self, label: &str) -> PathBuf {
        let path = crate::test_support_tests::unique_path(label);
        self.0.push(path.clone());
        path
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        for path in &self.0 {
            let _ = fs::remove_file(path);
            let mut sidecar = path.file_name().unwrap().to_os_string();
            sidecar.push(".readers");
            let _ = fs::remove_file(path.with_file_name(sidecar));
        }
    }
}

#[derive(Default)]
struct AggregateCount {
    feeds: u64,
    pairs: u64,
}

impl MembershipAggregateSink for AggregateCount {
    fn feed_cardinalities(&mut self, batch: &[FeedCardinality]) -> crate::Result<()> {
        self.feeds += batch.len() as u64;
        Ok(())
    }

    fn feed_overlaps(&mut self, batch: &[FeedOverlap]) -> crate::Result<()> {
        self.pairs += batch.len() as u64;
        Ok(())
    }
}

#[derive(Default)]
struct DirectCount(u64);

impl DirectJoinSink for DirectCount {
    fn direct_join_cells(&mut self, batch: &[DirectJoinCell]) -> crate::Result<()> {
        self.0 += batch.len() as u64;
        Ok(())
    }
}

#[derive(Default)]
struct MembershipCount {
    cross: u64,
    uncovered: u64,
}

impl MembershipJoinSink for MembershipCount {
    fn membership_cross_cells(&mut self, batch: &[MembershipCrossCell]) -> crate::Result<()> {
        self.cross += batch.len() as u64;
        Ok(())
    }

    fn uncovered_feeds(&mut self, batch: &[UncoveredFeed]) -> crate::Result<()> {
        self.uncovered += batch.len() as u64;
        Ok(())
    }
}

#[test]
fn query_join_and_algebra_allocations_do_not_scale_with_range_count() {
    let mut files = Files::new();
    let small_left = files.path("iprange-query-work-small-left");
    let small_right = files.path("iprange-query-work-small-right");
    let small_direct = files.path("iprange-query-work-small-direct");
    let large_left = files.path("iprange-query-work-large-left");
    let large_right = files.path("iprange-query-work-large-right");
    let large_direct = files.path("iprange-query-work-large-direct");
    create_membership(&small_left, 32, ["a", "b"], false);
    create_membership(&small_right, 32, ["c", "d"], true);
    create_direct(&small_direct, 32);
    create_membership(&large_left, 512, ["a", "b"], false);
    create_membership(&large_right, 512, ["c", "d"], true);
    create_direct(&large_direct, 512);

    let cancellation = CancellationToken::new();
    let mut small_left_reader = LiveReader::open(&small_left, &cancellation).unwrap();
    let mut small_right_reader = LiveReader::open(&small_right, &cancellation).unwrap();
    let mut small_direct_reader = LiveReader::open(&small_direct, &cancellation).unwrap();
    let mut large_left_reader = LiveReader::open(&large_left, &cancellation).unwrap();
    let mut large_right_reader = LiveReader::open(&large_right, &cancellation).unwrap();
    let mut large_direct_reader = LiveReader::open(&large_direct, &cancellation).unwrap();
    let small_left_scope = all_scope(&small_left_reader, &cancellation);
    let small_right_scope = all_scope(&small_right_reader, &cancellation);
    let large_left_scope = all_scope(&large_left_reader, &cancellation);
    let large_right_scope = all_scope(&large_right_reader, &cancellation);

    let mut matched = 0u64;
    let (_, point_allocations) = measure_thread_allocations(|| {
        small_left_reader
            .membership_query()
            .unwrap()
            .matching_feeds_v4(
                Ipv4Key(1),
                &mut |_name: FeedName| {
                    matched += 1;
                    Ok(())
                },
                &cancellation,
            )
            .unwrap()
    });
    assert_eq!(matched, 1);
    assert_eq!(point_allocations, AllocationStats::default());

    let (small_aggregate, small_aggregate_allocations) =
        aggregate(&small_left_scope, &cancellation);
    let (large_aggregate, large_aggregate_allocations) =
        aggregate(&large_left_scope, &cancellation);
    assert_eq!(small_aggregate_allocations, large_aggregate_allocations);
    assert_eq!(small_aggregate.0.input_source_passes, 1);
    assert_eq!(large_aggregate.0.input_source_passes, 1);
    assert_eq!(small_aggregate.0.membership_decodes, 2);
    assert_eq!(large_aggregate.0.membership_decodes, 2);
    assert_eq!(
        small_aggregate.0.membership_decodes + small_aggregate.0.membership_decode_cache_hits,
        small_aggregate.1.scanned_range_count
    );
    assert_eq!(
        large_aggregate.0.membership_decodes + large_aggregate.0.membership_decode_cache_hits,
        large_aggregate.1.scanned_range_count
    );

    let (small_direct_work, small_direct_allocations) =
        direct_join(&small_left_scope, &small_direct_reader, &cancellation);
    let (large_direct_work, large_direct_allocations) =
        direct_join(&large_left_scope, &large_direct_reader, &cancellation);
    assert_eq!(small_direct_allocations, large_direct_allocations);
    assert_eq!(small_direct_work.0.input_source_passes, 2);
    assert_eq!(large_direct_work.0.input_source_passes, 2);
    assert_eq!(small_direct_work.0.membership_decodes, 2);
    assert_eq!(large_direct_work.0.membership_decodes, 2);
    assert_eq!(
        small_direct_work.0.membership_decodes + small_direct_work.0.membership_decode_cache_hits,
        small_direct_work.1.membership_range_count
    );
    assert_eq!(
        large_direct_work.0.membership_decodes + large_direct_work.0.membership_decode_cache_hits,
        large_direct_work.1.membership_range_count
    );

    let (small_cross_work, small_cross_allocations) =
        membership_join(&small_left_scope, &small_right_scope, &cancellation);
    let (large_cross_work, large_cross_allocations) =
        membership_join(&large_left_scope, &large_right_scope, &cancellation);
    assert_eq!(small_cross_allocations, large_cross_allocations);
    assert_eq!(small_cross_work.0.input_source_passes, 2);
    assert_eq!(large_cross_work.0.input_source_passes, 2);
    assert_eq!(small_cross_work.0.membership_decodes, 4);
    assert_eq!(large_cross_work.0.membership_decodes, 4);
    assert_eq!(
        small_cross_work.0.membership_decodes + small_cross_work.0.membership_decode_cache_hits,
        small_cross_work.1.left_range_count + small_cross_work.1.right_range_count
    );
    assert_eq!(
        large_cross_work.0.membership_decodes + large_cross_work.0.membership_decode_cache_hits,
        large_cross_work.1.left_range_count + large_cross_work.1.right_range_count
    );

    let small_sources = [&small_left_scope, &small_right_scope];
    let large_sources = [&large_left_scope, &large_right_scope];
    let (rejected, rejected_allocations) = measure_thread_allocations(|| {
        MembershipAlgebra::new(
            &small_sources,
            MembershipAlgebraBudget {
                max_heap_bytes: 4 * 1024 * 1024,
                max_sources: 1,
            },
            &cancellation,
        )
    });
    assert!(matches!(rejected, Err(crate::Error::BudgetExceeded(_))));
    assert_eq!(rejected_allocations, AllocationStats::default());
    let small_algebra =
        MembershipAlgebra::new(&small_sources, algebra_budget(), &cancellation).unwrap();
    let large_algebra =
        MembershipAlgebra::new(&large_sources, algebra_budget(), &cancellation).unwrap();
    let (small_algebra_work, small_algebra_allocations) =
        algebra_count(&small_algebra, &cancellation);
    let (large_algebra_work, large_algebra_allocations) =
        algebra_count(&large_algebra, &cancellation);
    assert_eq!(small_algebra_allocations, large_algebra_allocations);
    assert_eq!(small_algebra_work.0.input_source_passes, 2);
    assert_eq!(large_algebra_work.0.input_source_passes, 2);
    assert_eq!(
        small_algebra_work.0.membership_decodes,
        large_algebra_work.0.membership_decodes
    );
    assert_eq!(
        small_algebra_work.0.membership_decodes + small_algebra_work.0.membership_decode_cache_hits,
        small_algebra_work.1.source_range_count
    );
    assert_eq!(
        large_algebra_work.0.membership_decodes + large_algebra_work.0.membership_decode_cache_hits,
        large_algebra_work.1.source_range_count
    );
    assert!(large_algebra_work.0.join_advances > small_algebra_work.0.join_advances);

    drop(large_algebra);
    drop(small_algebra);
    drop(large_right_scope);
    drop(large_left_scope);
    drop(small_right_scope);
    drop(small_left_scope);
    large_direct_reader.close().unwrap();
    large_right_reader.close().unwrap();
    large_left_reader.close().unwrap();
    small_direct_reader.close().unwrap();
    small_right_reader.close().unwrap();
    small_left_reader.close().unwrap();
}

#[test]
fn repeated_membership_ids_are_decoded_once_per_source() {
    let mut files = Files::new();
    let left = files.path("iprange-query-repeated-left");
    let right = files.path("iprange-query-repeated-right");
    let direct = files.path("iprange-query-repeated-direct");
    create_uniform_membership(&left, 512, "left", 0);
    create_uniform_membership(&right, 512, "right", 1);
    create_direct(&direct, 512);

    let cancellation = CancellationToken::new();
    let mut left_reader = LiveReader::open(&left, &cancellation).unwrap();
    let mut right_reader = LiveReader::open(&right, &cancellation).unwrap();
    let mut direct_reader = LiveReader::open(&direct, &cancellation).unwrap();
    let left_scope = all_scope(&left_reader, &cancellation);
    let right_scope = all_scope(&right_reader, &cancellation);

    let (aggregate_work, _) = aggregate(&left_scope, &cancellation);
    assert_eq!(aggregate_work.0.membership_decodes, 1);
    assert_eq!(aggregate_work.0.aggregation_contributions, 1);
    let (direct_work, _) = direct_join(&left_scope, &direct_reader, &cancellation);
    assert_eq!(direct_work.0.membership_decodes, 1);
    let (join_work, _) = membership_join(&left_scope, &right_scope, &cancellation);
    assert_eq!(join_work.0.membership_decodes, 2);

    let sources = [&left_scope, &right_scope];
    let algebra = MembershipAlgebra::new(&sources, algebra_budget(), &cancellation).unwrap();
    let (algebra_work, _) = algebra_count(&algebra, &cancellation);
    assert_eq!(algebra_work.0.membership_decodes, 2);

    drop(algebra);
    drop(right_scope);
    drop(left_scope);
    direct_reader.close().unwrap();
    right_reader.close().unwrap();
    left_reader.close().unwrap();
}

#[test]
fn recurring_algebra_output_memberships_are_interned_once_each() {
    let mut files = Files::new();
    let source = files.path("iprange-query-repeated-algebra-source");
    let output = files.path("iprange-query-repeated-algebra-output");
    create_membership(&source, 512, ["left", "right"], false);

    let cancellation = CancellationToken::new();
    let mut reader = LiveReader::open(&source, &cancellation).unwrap();
    let scope = all_scope(&reader, &cancellation);
    let sources = [&scope];
    let algebra = MembershipAlgebra::new(&sources, algebra_budget(), &cancellation).unwrap();
    let (published, work) = crate::work::measure(|| {
        algebra.publish_set(
            &output,
            ValueTag::new(b"result").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            AlgebraOutputBudget {
                max_output_pages: 10_000,
                max_open_files: 2,
            },
            &cancellation,
        )
    });
    let published = published.unwrap();
    assert_eq!(published.report.output_range_count, 1024);
    assert_eq!(work.membership_interns, 2);
    assert_eq!(work.membership_intern_cache_hits, 1022);
    assert_eq!(work.membership_refcount_batches, 1);

    drop(algebra);
    drop(scope);
    reader.close().unwrap();
}

fn aggregate(
    scope: &super::MembershipScope<'_>,
    cancellation: &CancellationToken,
) -> (
    (crate::work::Snapshot, super::MembershipAggregationReport),
    AllocationStats,
) {
    let mut sink = AggregateCount::default();
    let ((report, work), allocations) = measure_thread_allocations(|| {
        crate::work::measure(|| {
            scope.aggregate(MembershipAggregationMode::AllPairs, &mut sink, cancellation)
        })
    });
    let report = report.unwrap();
    assert_eq!(sink.feeds, report.feed_result_count);
    assert_eq!(sink.pairs, report.pair_result_count);
    ((work, report), allocations)
}

fn direct_join(
    scope: &super::MembershipScope<'_>,
    direct: &LiveReader,
    cancellation: &CancellationToken,
) -> ((crate::work::Snapshot, DirectJoinReport), AllocationStats) {
    let mut sink = DirectCount::default();
    let ((report, work), allocations) = measure_thread_allocations(|| {
        crate::work::measure(|| {
            scope.join_direct(
                DirectJoinSource::Live(direct),
                DirectJoinBudget {
                    max_result_cells: 16,
                },
                &mut sink,
                cancellation,
            )
        })
    });
    let report = report.unwrap();
    assert_eq!(sink.0, report.result_cell_count);
    ((work, report), allocations)
}

fn membership_join(
    left: &super::MembershipScope<'_>,
    right: &super::MembershipScope<'_>,
    cancellation: &CancellationToken,
) -> (
    (crate::work::Snapshot, MembershipJoinReport),
    AllocationStats,
) {
    let mut sink = MembershipCount::default();
    let ((report, work), allocations) = measure_thread_allocations(|| {
        crate::work::measure(|| left.join_membership(right, &mut sink, cancellation))
    });
    let report = report.unwrap();
    assert_eq!(sink.cross, report.cross_result_count);
    assert_eq!(sink.uncovered, report.uncovered_result_count);
    ((work, report), allocations)
}

fn algebra_count(
    algebra: &MembershipAlgebra<'_>,
    cancellation: &CancellationToken,
) -> ((crate::work::Snapshot, AlgebraCountReport), AllocationStats) {
    let ((report, work), allocations) = measure_thread_allocations(|| {
        crate::work::measure(|| algebra.count(FeedSelection::All, cancellation))
    });
    ((work, report.unwrap()), allocations)
}

fn all_scope<'a>(
    reader: &'a LiveReader,
    cancellation: &CancellationToken,
) -> super::MembershipScope<'a> {
    reader
        .membership_query()
        .unwrap()
        .all_feeds(
            MembershipQueryBudget {
                max_heap_bytes: 2 * 1024 * 1024,
            },
            cancellation,
        )
        .unwrap()
}

fn create_membership(path: &Path, blocks: u32, names: [&str; 2], shifted: bool) {
    let cancellation = CancellationToken::new();
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        crate::contract::StructureKind::None,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
    let first = (0..blocks)
        .map(|index| {
            let address = index * 4 + u32::from(shifted);
            AddressRange {
                from: Ipv4Key(address),
                to: Ipv4Key(address),
            }
        })
        .collect::<Vec<_>>();
    let second = (0..blocks)
        .map(|index| {
            let address = index * 4 + 1 + u32::from(shifted);
            AddressRange {
                from: Ipv4Key(address),
                to: Ipv4Key(address),
            }
        })
        .collect::<Vec<_>>();
    add_feed(&mut writer, names[0], &first, &cancellation);
    add_feed(&mut writer, names[1], &second, &cancellation);
    writer.close().unwrap();
}

fn create_uniform_membership(path: &Path, blocks: u32, name: &str, shift: u32) {
    let cancellation = CancellationToken::new();
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        crate::contract::StructureKind::None,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let ranges = (0..blocks)
        .map(|index| AddressRange {
            from: Ipv4Key(index * 4 + shift),
            to: Ipv4Key(index * 4 + shift),
        })
        .collect::<Vec<_>>();
    let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
    add_feed(&mut writer, name, &ranges, &cancellation);
    writer.close().unwrap();
}

fn add_feed(
    writer: &mut LiveWriter,
    name: &str,
    ranges: &[AddressRange<Ipv4Key>],
    cancellation: &CancellationToken,
) {
    let mut operation = writer
        .begin_create_feed(FeedName::new(name).unwrap(), cancellation)
        .unwrap();
    operation.add_ranges_v4_slice(ranges).unwrap();
    commit(operation.finish_input().unwrap());
}

fn create_direct(path: &Path, blocks: u32) {
    let cancellation = CancellationToken::new();
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        crate::contract::StructureKind::None,
        ValueTag::new(b"provider").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let ranges = (0..blocks)
        .flat_map(|index| {
            let base = index * 4;
            [
                DirectRange {
                    from: Ipv4Key(base),
                    to: Ipv4Key(base),
                    value: 1,
                },
                DirectRange {
                    from: Ipv4Key(base + 1),
                    to: Ipv4Key(base + 2),
                    value: 2,
                },
            ]
        })
        .collect::<Vec<_>>();
    let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
    let mut operation = writer.begin_direct_replacement(&cancellation).unwrap();
    operation.add_ranges_v4_slice(&ranges).unwrap();
    commit(operation.finish_input().unwrap());
    writer.close().unwrap();
}

fn commit(finished: FinishedWorkflow<'_>) {
    match finished {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("new workflow did not change"),
    }
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn algebra_budget() -> MembershipAlgebraBudget {
    MembershipAlgebraBudget {
        max_heap_bytes: 4 * 1024 * 1024,
        max_sources: 2,
    }
}
