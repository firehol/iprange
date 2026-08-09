use std::fs;
use std::path::PathBuf;

use crate::test_alloc::count_thread_allocations;
use crate::{
    create_live, AddressFamily, AddressRange, CancellationToken, DirectRange, FinishedWorkflow,
    Ipv4Key, LiveWriter, ReclaimResult, TransactionBudget, ValueKind, ValueTag,
};

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new() -> Self {
        Self {
            main: crate::test_support_tests::unique_path("iprange-v4-workflow-allocation"),
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

fn changed(finished: FinishedWorkflow<'_>) -> crate::PreparedWorkflow<'_> {
    match finished {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(report) => {
            panic!("expected a changed workflow, got {report:?}")
        }
    }
}

#[test]
fn slice_ingestion_and_finish_allocate_nothing_per_record() {
    let files = TestPair::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"direct").unwrap(),
        1,
        &crate::CancellationToken::new(),
    )
    .unwrap();
    let budget = TransactionBudget {
        max_heap_bytes: 1,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    };
    let mut writer =
        LiveWriter::open(&files.main, budget, &crate::CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let ranges: Vec<_> = (0..1_000)
        .map(|index| DirectRange {
            from: Ipv4Key(index * 2),
            to: Ipv4Key(index * 2),
            value: index & 1,
        })
        .collect();
    let mut workflow = writer.begin_direct_replacement(&cancellation).unwrap();

    let ((result, work), allocations) =
        count_thread_allocations(|| crate::work::measure(|| workflow.add_ranges_v4_slice(&ranges)));
    result.unwrap();
    assert_eq!(allocations, 0);
    assert_eq!(work.source_passes, 1);
    assert_eq!(work.ranges_consumed, ranges.len() as u64);
    assert_eq!(work.ranges_emitted, ranges.len() as u64);
    assert_eq!(work.mapping_growths, 1);
    assert_eq!(work.mapping_remaps, 1);
    assert!(work.pages_created > 0);
    assert!(work.pages_split > 0);
    assert_eq!(work.pages_sealed, 0);

    let ((finished, work), allocations) =
        count_thread_allocations(|| crate::work::measure(|| workflow.finish_input()));
    let finished = finished.unwrap();
    assert_eq!(allocations, 0);
    assert_eq!(work.source_passes, 2);
    assert_eq!(work.ranges_consumed, ranges.len() as u64);
    assert_eq!(work.output_passes, 0);
    let prepared = changed(finished);
    let (commit, work) = crate::work::measure(|| prepared.commit());
    commit.unwrap();
    assert!(work.pages_sealed > 0);
    assert_eq!(work.mapping_flushes, 2);
    assert_eq!(work.file_syncs, 2);
    writer.close().unwrap();
}

#[test]
fn retention_ingestion_and_merge_allocate_nothing_per_record() {
    let files = TestPair::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let budget = TransactionBudget {
        max_heap_bytes: 1,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    };
    let mut writer = LiveWriter::open(&files.main, budget, &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let first: Vec<_> = (0..1_000)
        .map(|index| AddressRange {
            from: Ipv4Key(index * 4),
            to: Ipv4Key(index * 4 + 1),
        })
        .collect();
    let second: Vec<_> = (0..1_000)
        .map(|index| AddressRange {
            from: Ipv4Key(index * 4 + 1),
            to: Ipv4Key(index * 4 + 2),
        })
        .collect();

    let mut seed = writer.begin_retention_refresh(10, &cancellation).unwrap();
    seed.add_ranges_v4_slice(&first).unwrap();
    match seed.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("initial retention refresh changed nothing"),
    }

    let mut refresh = writer.begin_retention_refresh(20, &cancellation).unwrap();
    let ((result, work), allocations) =
        count_thread_allocations(|| crate::work::measure(|| refresh.add_ranges_v4_slice(&second)));
    result.unwrap();
    assert_eq!(allocations, 0);
    assert_eq!(work.source_passes, 1);
    assert_eq!(work.ranges_consumed, second.len() as u64);

    let ((finished, work), allocations) =
        count_thread_allocations(|| crate::work::measure(|| refresh.finish_input()));
    let finished = finished.unwrap();
    assert_eq!(allocations, 0);
    assert_eq!(work.source_passes, 2);
    assert_eq!(work.output_passes, 1);
    assert_eq!(work.ranges_consumed, (first.len() + second.len()) as u64);
    assert!(work.ranges_emitted > 0);
    assert!(work.pages_retired > 0);
    assert!(matches!(&finished, FinishedWorkflow::Changed(_)));
    finished.abort().unwrap();
    writer.close().unwrap();
}

#[test]
fn reclamation_counts_each_released_page_once() {
    let files = TestPair::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"direct").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let budget = TransactionBudget {
        max_heap_bytes: 1,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    };
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget, &cancellation).unwrap();

    let mut first = writer.begin_direct_transaction(&cancellation).unwrap();
    first.assign_v4(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    first.commit().unwrap();
    let mut second = writer.begin_direct_transaction(&cancellation).unwrap();
    let (result, work) = crate::work::measure(|| second.assign_v4(Ipv4Key(12), Ipv4Key(18), 2));
    result.unwrap();
    assert_eq!(work.pages_copied, 1);
    second.commit().unwrap();

    let (result, work) = crate::work::measure(|| writer.reclaim(10, 10_000, &cancellation));
    let ReclaimResult::Commit { page_count, .. } = result.unwrap() else {
        panic!("committed retirement was not reclaimed");
    };
    assert!(page_count > 0);
    assert_eq!(work.pages_reclaimed, page_count);
    assert!(work.pages_sealed > 0);
    writer.close().unwrap();
}
