use std::fs;
use std::path::PathBuf;

use crate::test_alloc::count_thread_allocations;
use crate::{
    create_live, AddressFamily, AddressRange, CancellationToken, CommitDurability, FeedName,
    FinishedWorkflow, Ipv4Key, LiveWriter, TransactionBudget, ValueKind, ValueTag,
};

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new() -> Self {
        Self {
            main: crate::test_support_tests::unique_path("iprange-v4-feed-allocation"),
        }
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.main.with_file_name(name)
    }
}

impl Drop for TestPair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
    }
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 1,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

#[test]
fn slice_ingestion_and_feed_comparison_allocate_nothing_per_record() {
    let files = TestPair::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        crate::contract::StructureKind::None,
        ValueTag::new(b"membership").unwrap(),
        1,
        &crate::CancellationToken::new(),
    )
    .unwrap();
    let mut writer =
        LiveWriter::open(&files.main, budget(), &crate::CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let ranges: Vec<_> = (0..1_000)
        .map(|index| AddressRange {
            from: Ipv4Key(index * 2),
            to: Ipv4Key(index * 2),
        })
        .collect();
    let (workflow, work) = crate::work::measure(|| {
        writer.begin_create_feed(FeedName::new("feed").unwrap(), &cancellation)
    });
    let mut workflow = workflow.unwrap();
    assert_eq!(work.catalog_lookups, 1);
    assert_eq!(work.catalog_interns, 1);
    assert_eq!(work.membership_interns, 1);

    let ((result, work), allocations) =
        count_thread_allocations(|| crate::work::measure(|| workflow.add_ranges_v4_slice(&ranges)));
    result.unwrap();
    assert_eq!(allocations, 0);
    assert_eq!(work.source_passes, 1);
    assert_eq!(work.ranges_consumed, ranges.len() as u64);
    assert_eq!(work.ranges_emitted, ranges.len() as u64 - 1);
    assert_eq!(work.membership_lookups, 0);
    assert_eq!(work.membership_interns, 0);
    assert_eq!(work.output_passes, 0);

    let ((finished, work), allocations) =
        count_thread_allocations(|| crate::work::measure(|| workflow.finish_input()));
    let finished = finished.unwrap();
    assert_eq!(allocations, 0);
    assert_eq!(work.tree_lookups, 1);
    assert_eq!(
        work.source_passes, 1,
        "ordered empty-map creation rescanned the completed range tree"
    );
    assert_eq!(work.output_passes, 0);
    assert_eq!(
        work.ranges_consumed, 0,
        "ordered empty-map creation decoded the completed ranges again"
    );
    assert_eq!(work.ranges_emitted, 1);
    assert_eq!(work.membership_lookups, 1);
    assert_eq!(work.membership_interns, 0);
    assert_eq!(work.membership_refcount_batches, 1);
    assert_eq!(work.membership_delta_spills, 1);
    assert!(matches!(&finished, FinishedWorkflow::Changed(_)));
    finished.abort().unwrap();
    writer.close().unwrap();
}

#[test]
fn second_feed_aggregates_alternating_membership_deltas() {
    let files = TestPair::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        crate::contract::StructureKind::None,
        ValueTag::new(b"membership").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let ranges: Vec<_> = (0..1_000)
        .map(|index| AddressRange {
            from: Ipv4Key(index * 2),
            to: Ipv4Key(index * 2),
        })
        .collect();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();

    let mut first = writer
        .begin_create_feed(FeedName::new("first").unwrap(), &cancellation)
        .unwrap();
    first.add_ranges_v4_slice(&ranges).unwrap();
    match first.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            assert_eq!(
                prepared.commit().unwrap().durability,
                CommitDurability::Committed
            );
        }
        FinishedWorkflow::NoChange(_) => panic!("feed creation cannot be a no-op"),
    }

    let mut second = writer
        .begin_create_feed(FeedName::new("second").unwrap(), &cancellation)
        .unwrap();
    second.add_ranges_v4_slice(&ranges).unwrap();
    let (finished, work) = crate::work::measure(|| second.finish_input());
    assert_eq!(work.output_passes, 1);
    assert_eq!(
        work.membership_refcount_batches, 3,
        "a uniform merge submitted range-proportional refcount changes: {work:?}"
    );
    assert_eq!(
        work.membership_delta_spills, 3,
        "a two-ID merge performed range-proportional delta-tree work: {work:?}"
    );
    finished.unwrap().abort().unwrap();
    writer.close().unwrap();
}

#[test]
fn exact_feed_workflows_lookup_each_name_once() {
    let files = TestPair::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        crate::contract::StructureKind::None,
        ValueTag::new(b"membership").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let name = FeedName::new("feed").unwrap();
    let renamed = FeedName::new("renamed").unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &cancellation).unwrap();

    let create = writer.begin_create_feed(name, &cancellation).unwrap();
    match create.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            let committed = prepared.commit().unwrap();
            assert_eq!(committed.durability, CommitDurability::Committed);
        }
        FinishedWorkflow::NoChange(_) => panic!("feed creation cannot be a no-op"),
    }

    let (replace, work) = crate::work::measure(|| writer.begin_replace_feed(name, &cancellation));
    assert_eq!(work.catalog_lookups, 1);
    drop(replace.unwrap());
    writer.abort().unwrap();

    let (rename, work) = crate::work::measure(|| writer.rename_feed(name, renamed, &cancellation));
    assert_eq!(work.catalog_lookups, 2);
    rename.unwrap().abort().unwrap();

    let (delete, work) = crate::work::measure(|| writer.delete_feed(name, &cancellation));
    assert_eq!(work.catalog_lookups, 1);
    delete.unwrap().abort().unwrap();

    writer.close().unwrap();
}
