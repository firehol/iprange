#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, snapshot_to, AddressFamily, AddressRange, CancellationToken, Cardinality129,
    DirectRange, FeedName, FinishedHistoryProjection, FinishedWorkflow, HistoryProjectionReport,
    HistoryProjectionSource, HistoryWindow, ImmutableReader, Ipv4Key, Ipv6Key, LiveReader,
    LiveWriter, RangeDirection, SnapshotBudget, SnapshotPublicationPolicy, SnapshotSourceMode,
    TransactionBudget, ValueKind, ValueTag,
};

struct Files(Vec<PathBuf>);

impl Files {
    fn new() -> Self {
        Self(Vec::new())
    }

    fn path(&mut self, label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-history-{label}-{}-{unique}",
            std::process::id()
        ));
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

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 4 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn create(path: &Path, family: AddressFamily, kind: ValueKind, tag: ValueTag) {
    create_live(
        path,
        family,
        kind,
        iprange_livedb::StructureKind::None,
        tag,
        4,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn commit_workflow(finished: FinishedWorkflow<'_>) {
    match finished {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(report) => panic!("expected change: {report:?}"),
    }
}

fn commit_history(finished: FinishedHistoryProjection<'_>) {
    match finished {
        FinishedHistoryProjection::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedHistoryProjection::NoChange(report) => panic!("expected change: {report:?}"),
    }
}

fn commit_history_with_metadata(finished: FinishedHistoryProjection<'_>, metadata: &[u8]) {
    match finished {
        FinishedHistoryProjection::Changed(mut prepared) => {
            prepared.set_metadata_json(metadata).unwrap();
            prepared.commit().unwrap();
        }
        FinishedHistoryProjection::NoChange(report) => panic!("expected change: {report:?}"),
    }
}

fn take_no_change(finished: FinishedHistoryProjection<'_>) -> HistoryProjectionReport {
    match finished {
        FinishedHistoryProjection::NoChange(report) => report,
        FinishedHistoryProjection::Changed(prepared) => {
            prepared.abort().unwrap();
            panic!("expected no change")
        }
    }
}

fn create_feed(writer: &mut LiveWriter, name: &str, ranges: &[AddressRange<Ipv4Key>]) {
    let cancellation = CancellationToken::new();
    let mut operation = writer
        .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
        .unwrap();
    operation.add_ranges_v4_slice(ranges).unwrap();
    commit_workflow(operation.finish_input().unwrap());
}

fn assert_feed_v4(reader: &LiveReader, name: &str, expected: &[AddressRange<Ipv4Key>]) {
    let mut cursor = reader
        .feed_range_cursor_v4(name, RangeDirection::Forward)
        .unwrap();
    for &range in expected {
        assert_eq!(cursor.next_range().unwrap(), Some(range));
    }
    assert_eq!(cursor.next_range().unwrap(), None);
}

#[test]
fn projects_multiple_windows_once_and_preserves_unrelated_feeds() {
    let mut files = Files::new();
    let source_path = files.path("source");
    let destination_path = files.path("destination");
    let cancellation = CancellationToken::new();
    create(
        &source_path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
    );
    create(
        &destination_path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
    );

    let mut source_writer = LiveWriter::open(&source_path, budget(), &cancellation).unwrap();
    let mut replacement = source_writer
        .begin_direct_replacement(&cancellation)
        .unwrap();
    replacement
        .add_ranges_v4_slice(&[
            DirectRange {
                from: Ipv4Key(40),
                to: Ipv4Key(49),
                value: 20,
            },
            DirectRange {
                from: Ipv4Key(0),
                to: Ipv4Key(9),
                value: 10,
            },
            DirectRange {
                from: Ipv4Key(20),
                to: Ipv4Key(29),
                value: 30,
            },
            DirectRange {
                from: Ipv4Key(10),
                to: Ipv4Key(19),
                value: 20,
            },
        ])
        .unwrap();
    commit_workflow(replacement.finish_input().unwrap());
    source_writer.close().unwrap();

    let mut writer = LiveWriter::open(&destination_path, budget(), &cancellation).unwrap();
    create_feed(
        &mut writer,
        "unrelated",
        &[AddressRange {
            from: Ipv4Key(5),
            to: Ipv4Key(15),
        }],
    );
    create_feed(
        &mut writer,
        "recent",
        &[AddressRange {
            from: Ipv4Key(0),
            to: Ipv4Key(19),
        }],
    );
    create_feed(
        &mut writer,
        "very-recent",
        &[AddressRange {
            from: Ipv4Key(25),
            to: Ipv4Key(35),
        }],
    );
    let mut source = LiveReader::open(&source_path, &cancellation).unwrap();
    let windows = [
        HistoryWindow {
            feed_name: FeedName::new("recent").unwrap(),
            cutoff: 15,
        },
        HistoryWindow {
            feed_name: FeedName::new("very-recent").unwrap(),
            cutoff: 25,
        },
        HistoryWindow {
            feed_name: FeedName::new("future").unwrap(),
            cutoff: 30,
        },
    ];

    let result = writer
        .project_history(
            HistoryProjectionSource::Live(&source),
            &windows,
            &cancellation,
        )
        .unwrap();
    let report = result.report();
    assert_eq!(report.source_range_count, 4);
    assert_eq!(report.source_addresses, Cardinality129::from_u64(40));
    assert_eq!(report.created_feed_count, 1);
    assert_eq!(report.before_interval_count, 2);
    assert_eq!(report.after_interval_count, 2);
    assert_eq!(report.before_addresses, Cardinality129::from_u64(31));
    assert_eq!(report.after_addresses, Cardinality129::from_u64(30));
    assert_eq!(report.unchanged_addresses, Cardinality129::from_u64(15));
    assert_eq!(report.added_addresses, Cardinality129::from_u64(15));
    assert_eq!(report.removed_addresses, Cardinality129::from_u64(16));

    let recent = &report.windows[0];
    assert!(!recent.created);
    assert_eq!(recent.before_interval_count, 1);
    assert_eq!(recent.after_interval_count, 2);
    assert_eq!(recent.before_addresses, Cardinality129::from_u64(20));
    assert_eq!(recent.after_addresses, Cardinality129::from_u64(30));
    assert_eq!(recent.unchanged_addresses, Cardinality129::from_u64(10));
    assert_eq!(recent.added_addresses, Cardinality129::from_u64(20));
    assert_eq!(recent.removed_addresses, Cardinality129::from_u64(10));

    let very_recent = &report.windows[1];
    assert!(!very_recent.created);
    assert_eq!(very_recent.before_addresses, Cardinality129::from_u64(11));
    assert_eq!(very_recent.after_addresses, Cardinality129::from_u64(10));
    assert_eq!(very_recent.unchanged_addresses, Cardinality129::from_u64(5));
    assert_eq!(very_recent.added_addresses, Cardinality129::from_u64(5));
    assert_eq!(very_recent.removed_addresses, Cardinality129::from_u64(6));
    assert!(report.windows[2].created);
    assert_eq!(report.windows[2].after_addresses, Cardinality129::ZERO);

    commit_history_with_metadata(result, br#"{"history":true}"#);

    let mut destination = LiveReader::open(&destination_path, &cancellation).unwrap();
    assert_feed_v4(
        &destination,
        "unrelated",
        &[AddressRange {
            from: Ipv4Key(5),
            to: Ipv4Key(15),
        }],
    );
    assert_feed_v4(
        &destination,
        "recent",
        &[
            AddressRange {
                from: Ipv4Key(10),
                to: Ipv4Key(29),
            },
            AddressRange {
                from: Ipv4Key(40),
                to: Ipv4Key(49),
            },
        ],
    );
    assert_feed_v4(
        &destination,
        "very-recent",
        &[AddressRange {
            from: Ipv4Key(20),
            to: Ipv4Key(29),
        }],
    );
    assert_feed_v4(&destination, "future", &[]);
    assert_eq!(
        destination.metadata_json().unwrap().unwrap(),
        br#"{"history":true}"#
    );
    destination.close().unwrap();

    let result = writer
        .project_history(
            HistoryProjectionSource::Live(&source),
            &windows,
            &cancellation,
        )
        .unwrap();
    let report = take_no_change(result);
    assert_eq!(report.created_feed_count, 0);
    assert_eq!(
        report.windows[0].unchanged_addresses,
        Cardinality129::from_u64(30)
    );
    source.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn immutable_empty_source_keeps_empty_feeds_and_rejects_invalid_requests_cleanly() {
    let mut files = Files::new();
    let live_source_path = files.path("live-source");
    let source_path = files.path("immutable-source");
    let destination_path = files.path("destination-empty");
    let wrong_source_path = files.path("wrong-source");
    let cancellation = CancellationToken::new();
    create(
        &live_source_path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
    );
    snapshot_to(
        &live_source_path,
        SnapshotSourceMode::Live,
        &source_path,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(2 * 1024 * 1024, 100, 3),
        &cancellation,
    )
    .unwrap();
    create(
        &destination_path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
    );
    create(
        &wrong_source_path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"timestamps").unwrap(),
    );

    let source = ImmutableReader::open(&source_path).unwrap();
    let mut writer = LiveWriter::open(&destination_path, budget(), &cancellation).unwrap();
    let window = HistoryWindow {
        feed_name: FeedName::new("empty").unwrap(),
        cutoff: 0,
    };
    let result = writer
        .project_history(
            HistoryProjectionSource::Immutable(&source),
            &[window],
            &cancellation,
        )
        .unwrap();
    assert_eq!(result.report().source_range_count, 0);
    assert_eq!(result.report().source_addresses, Cardinality129::ZERO);
    assert!(result.report().windows[0].created);
    commit_history(result);

    assert!(writer
        .project_history(
            HistoryProjectionSource::Immutable(&source),
            &[],
            &cancellation
        )
        .is_err());
    assert!(writer
        .project_history(
            HistoryProjectionSource::Immutable(&source),
            &[window, window],
            &cancellation,
        )
        .is_err());
    let mut wrong = LiveReader::open(&wrong_source_path, &cancellation).unwrap();
    assert!(writer
        .project_history(
            HistoryProjectionSource::Live(&wrong),
            &[window],
            &cancellation,
        )
        .is_err());

    let mut operation = writer
        .begin_create_feed(FeedName::new("still-usable").unwrap(), &cancellation)
        .unwrap();
    operation.add_ranges_v4_slice(&[]).unwrap();
    commit_workflow(operation.finish_input().unwrap());
    wrong.close().unwrap();
    writer.close().unwrap();
}

#[test]
fn full_ipv6_space_is_counted_exactly() {
    let mut files = Files::new();
    let source_path = files.path("source-v6");
    let destination_path = files.path("destination-v6");
    let cancellation = CancellationToken::new();
    create(
        &source_path,
        AddressFamily::Ipv6,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
    );
    create(
        &destination_path,
        AddressFamily::Ipv6,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
    );
    let mut source_writer = LiveWriter::open(&source_path, budget(), &cancellation).unwrap();
    let mut replacement = source_writer
        .begin_direct_replacement(&cancellation)
        .unwrap();
    replacement
        .add_ranges_v6_slice(&[DirectRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
            value: 1,
        }])
        .unwrap();
    commit_workflow(replacement.finish_input().unwrap());
    source_writer.close().unwrap();

    let mut source = LiveReader::open(&source_path, &cancellation).unwrap();
    let mut writer = LiveWriter::open(&destination_path, budget(), &cancellation).unwrap();
    let result = writer
        .project_history(
            HistoryProjectionSource::Live(&source),
            &[HistoryWindow {
                feed_name: FeedName::new("all").unwrap(),
                cutoff: 0,
            }],
            &cancellation,
        )
        .unwrap();
    assert_eq!(
        result.report().after_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    assert_eq!(
        result.report().windows[0].added_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    commit_history(result);
    let mut destination = LiveReader::open(&destination_path, &cancellation).unwrap();
    let mut cursor = destination
        .feed_range_cursor_v6("all", RangeDirection::Forward)
        .unwrap();
    assert_eq!(
        cursor.next_range().unwrap(),
        Some(AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        })
    );
    assert_eq!(cursor.next_range().unwrap(), None);
    destination.close().unwrap();
    source.close().unwrap();
    writer.close().unwrap();
}
