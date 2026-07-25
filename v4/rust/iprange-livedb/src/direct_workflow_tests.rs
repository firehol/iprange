use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use crate::test_alloc::count_thread_allocations;
use crate::{
    create_live, AddressFamily, CancellationToken, DirectRange, FinishedWorkflow, Ipv4Key,
    LiveWriter, TransactionBudget, ValueKind, ValueTag,
};

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-workflow-allocation-{}-{unique}",
                std::process::id()
            )),
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

    let (result, allocations) = count_thread_allocations(|| workflow.add_ranges_v4_slice(&ranges));
    result.unwrap();
    assert_eq!(allocations, 0);

    let (finished, allocations) = count_thread_allocations(|| workflow.finish_input());
    let finished = finished.unwrap();
    assert_eq!(allocations, 0);
    assert!(matches!(&finished, FinishedWorkflow::Changed(_)));
    finished.abort().unwrap();
    writer.close().unwrap();
}
