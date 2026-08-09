use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use crate::test_alloc::count_thread_allocations;
use crate::{
    create_live, AddressFamily, AddressRange, CancellationToken, FeedName, FinishedWorkflow,
    Ipv4Key, LiveReader, LiveWriter, TransactionBudget, ValueKind, ValueTag,
};

use super::MembershipImportSource;

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-import-allocation-{label}-{}-{unique}",
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

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

fn create_feed(writer: &mut LiveWriter, name: &str, from: u32, to: u32) {
    let cancellation = CancellationToken::new();
    let mut workflow = writer
        .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
        .unwrap();
    workflow
        .add_ranges_v4_slice(&[AddressRange {
            from: Ipv4Key(from),
            to: Ipv4Key(to),
        }])
        .unwrap();
    match workflow.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("feed creation cannot be a no-op"),
    }
}

#[test]
fn open_import_processing_allocates_no_heap() {
    let source_files = TestPair::new("source");
    let destination_files = TestPair::new("destination");
    for files in [&source_files, &destination_files] {
        create_live(
            &files.main,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"membership").unwrap(),
            4,
            &crate::CancellationToken::new(),
        )
        .unwrap();
    }

    let mut source_writer = LiveWriter::open(
        &source_files.main,
        budget(),
        &crate::CancellationToken::new(),
    )
    .unwrap();
    create_feed(&mut source_writer, "alpha", 0, 199);
    create_feed(&mut source_writer, "beta", 100, 299);
    source_writer.close().unwrap();

    let mut source =
        LiveReader::open(&source_files.main, &crate::CancellationToken::new()).unwrap();
    let source_records = source.info().unwrap().range_record_count;
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(
        &destination_files.main,
        budget(),
        &crate::CancellationToken::new(),
    )
    .unwrap();
    let (import, begin_allocations) = count_thread_allocations(|| {
        writer.begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
    });
    assert_eq!(begin_allocations, 0);
    let ((finished, work), finish_allocations) =
        count_thread_allocations(|| crate::work::measure(|| import.unwrap().finish_input()));
    assert_eq!(finish_allocations, 0);
    assert_eq!(work.source_passes, 4);
    assert_eq!(work.output_passes, 1);
    assert_eq!(work.ranges_consumed, source_records);
    assert_eq!(work.ranges_emitted, source_records);
    match finished.unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("import unexpectedly did nothing"),
    }
    source.close().unwrap();
    writer.close().unwrap();
}
