use std::fs;
use std::path::PathBuf;

use crate::test_alloc::measure_thread_allocations;
use crate::{
    create_live, AddressFamily, CancellationToken, DirectRange, FeedName,
    FinishedHistoryProjection, HistoryProjectionSource, HistoryWindow, Ipv4Key, LiveReader,
    LiveWriter, TransactionBudget, ValueKind, ValueTag,
};

struct Files {
    source: PathBuf,
    destination: PathBuf,
}

impl Files {
    fn new() -> Self {
        Self {
            source: crate::test_support_tests::unique_path("iprange-v4-history-work-source"),
            destination: crate::test_support_tests::unique_path(
                "iprange-v4-history-work-destination",
            ),
        }
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        for path in [&self.source, &self.destination] {
            let _ = fs::remove_file(path);
            let mut sidecar = path.file_name().unwrap().to_os_string();
            sidecar.push(".readers");
            let _ = fs::remove_file(path.with_file_name(sidecar));
        }
    }
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

#[test]
fn one_source_pass_and_only_window_proportional_allocations() {
    let files = Files::new();
    let cancellation = CancellationToken::new();
    create_live(
        &files.source,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
        1,
        &cancellation,
    )
    .unwrap();
    create_live(
        &files.destination,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let ranges: Vec<_> = (0..1_000)
        .map(|index| DirectRange {
            from: Ipv4Key(index * 2),
            to: Ipv4Key(index * 2),
            value: 10 + index % 3,
        })
        .collect();
    let mut source_writer = LiveWriter::open(&files.source, budget(), &cancellation).unwrap();
    let mut replacement = source_writer
        .begin_direct_replacement(&cancellation)
        .unwrap();
    replacement.add_ranges_v4_slice(&ranges).unwrap();
    match replacement.finish_input().unwrap() {
        crate::FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        crate::FinishedWorkflow::NoChange(_) => panic!("source replacement did not change"),
    }
    source_writer.close().unwrap();

    let mut source = LiveReader::open(&files.source, &cancellation).unwrap();
    let mut writer = LiveWriter::open(&files.destination, budget(), &cancellation).unwrap();
    let windows = [
        HistoryWindow {
            feed_name: FeedName::new("one").unwrap(),
            cutoff: 9,
        },
        HistoryWindow {
            feed_name: FeedName::new("two").unwrap(),
            cutoff: 10,
        },
        HistoryWindow {
            feed_name: FeedName::new("three").unwrap(),
            cutoff: 11,
        },
    ];
    {
        let ((result, work), allocations) = measure_thread_allocations(|| {
            crate::work::measure(|| {
                writer.project_history(
                    HistoryProjectionSource::Live(&source),
                    &windows,
                    &cancellation,
                )
            })
        });
        let result = result.unwrap();
        assert!(
            allocations.count <= 14,
            "unexpected allocations: {allocations:?}"
        );
        assert!(
            allocations.bytes < 16 * 1024,
            "unexpected heap growth: {allocations:?}"
        );
        assert_eq!(work.input_source_passes, 1);
        assert_eq!(work.source_passes, 3);
        assert_eq!(work.ranges_consumed, ranges.len() as u64);
        assert_eq!(work.ranges_emitted, ranges.len() as u64);
        assert_eq!(work.output_passes, 1);
        assert_eq!(
            work.history_window_tests,
            ranges.len() as u64 * windows.len() as u64
        );
        match result {
            FinishedHistoryProjection::Changed(prepared) => {
                prepared.abort().unwrap();
            }
            FinishedHistoryProjection::NoChange(_) => panic!("new feeds did not change catalog"),
        }
    }
    source.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn unused_history_prefixes_are_not_interned() {
    let files = Files::new();
    let cancellation = CancellationToken::new();
    create_live(
        &files.source,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
        1,
        &cancellation,
    )
    .unwrap();
    create_live(
        &files.destination,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let windows: Vec<_> = (0..64)
        .map(|index| HistoryWindow {
            feed_name: FeedName::new(&format!("history-{index:02}")).unwrap(),
            cutoff: index,
        })
        .collect();
    let mut source = LiveReader::open(&files.source, &cancellation).unwrap();
    let mut writer = LiveWriter::open(&files.destination, budget(), &cancellation).unwrap();
    let (result, work) = crate::work::measure(|| {
        writer.project_history(
            HistoryProjectionSource::Live(&source),
            &windows,
            &cancellation,
        )
    });
    assert_eq!(work.membership_interns, 0);
    match result.unwrap() {
        FinishedHistoryProjection::Changed(prepared) => {
            prepared.abort().unwrap();
        }
        FinishedHistoryProjection::NoChange(_) => panic!("new feeds did not change catalog"),
    }
    source.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn streamed_window_preparation_failure_aborts_the_entire_draft() {
    let files = Files::new();
    let cancellation = CancellationToken::new();
    create_live(
        &files.source,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
        1,
        &cancellation,
    )
    .unwrap();
    create_live(
        &files.destination,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();

    let mut source = LiveReader::open(&files.source, &cancellation).unwrap();
    let mut writer = LiveWriter::open(&files.destination, budget(), &cancellation).unwrap();
    let failure = writer.project_history_state_from(
        HistoryProjectionSource::Live(&source),
        2,
        [
            Ok(HistoryWindow {
                feed_name: FeedName::new("partially-created").unwrap(),
                cutoff: 0,
            }),
            Err(crate::Error::InvalidArgument("injected window failure")),
        ],
        &cancellation,
    );
    let failure = failure.unwrap_err();
    assert!(
        matches!(
            &failure,
            crate::Error::TransactionAborted(cause)
                if matches!(cause.as_ref(), crate::Error::InvalidArgument(_))
        ),
        "unexpected history preparation failure: {failure:?}"
    );

    let operation = writer
        .begin_create_feed(FeedName::new("still-usable").unwrap(), &cancellation)
        .unwrap();
    match operation.finish_input().unwrap() {
        crate::FinishedWorkflow::Changed(prepared) => {
            prepared.abort().unwrap();
        }
        crate::FinishedWorkflow::NoChange(_) => panic!("new feed did not change catalog"),
    }
    source.close().unwrap();
    writer.close().unwrap();
}
