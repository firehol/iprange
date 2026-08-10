#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, snapshot_to, AddressFamily, AddressRange, CancellationToken, FeedName,
    FinishedWorkflow, ImmutableReader, Ipv4Key, LiveReader, LiveWriter, SnapshotBudget,
    SnapshotPublicationPolicy, SnapshotSourceMode, TransactionBudget, ValueKind, ValueTag,
};

struct Files {
    paths: Vec<PathBuf>,
}

impl Files {
    fn new() -> Self {
        Self { paths: Vec::new() }
    }

    fn path(&mut self, label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-chain-{label}-{}-{unique}",
            std::process::id()
        ));
        self.paths.push(path.clone());
        path
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        for path in &self.paths {
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

fn create(path: &Path, kind: ValueKind, tag: ValueTag) {
    create_live(
        path,
        AddressFamily::Ipv4,
        kind,
        tag,
        4,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn commit(finished: FinishedWorkflow<'_>) {
    match finished {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(report) => panic!("expected change: {report:?}"),
    }
}

#[test]
fn pinned_live_and_immutable_ranges_chain_without_materialization() {
    let mut files = Files::new();
    let source_path = files.path("source");
    let snapshot_path = files.path("snapshot");
    let membership_path = files.path("membership");
    let first_seen_path = files.path("first-seen");
    let last_seen_path = files.path("last-seen");
    let direct_path = files.path("direct");
    let cancellation = CancellationToken::new();
    let mut ranges: Vec<_> = (0..600)
        .rev()
        .map(|index| AddressRange {
            from: Ipv4Key(index * 2),
            to: Ipv4Key(index * 2),
        })
        .collect();

    create(
        &source_path,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
    );
    let mut writer = LiveWriter::open(&source_path, budget(), &cancellation).unwrap();
    let mut feed = writer
        .begin_create_feed(FeedName::new("source").unwrap(), &cancellation)
        .unwrap();
    feed.add_ranges_v4_slice(&ranges).unwrap();
    commit(feed.finish_input().unwrap());
    writer.close().unwrap();

    let mut source = LiveReader::open(&source_path, &cancellation).unwrap();

    create(
        &membership_path,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
    );
    let mut writer = LiveWriter::open(&membership_path, budget(), &cancellation).unwrap();
    let mut destination = writer
        .begin_create_feed(FeedName::new("copy").unwrap(), &cancellation)
        .unwrap();
    let mut mapped = source.named_feed_source_v4("source").unwrap();
    destination.add_ranges_v4(&mut mapped).unwrap();
    let finished = destination.finish_input().unwrap();
    assert_eq!(finished.report().input_record_count, 600);
    assert_eq!(finished.report().input_normalized_interval_count, 600);
    commit(finished);
    writer.close().unwrap();

    create(&first_seen_path, ValueKind::Direct, ValueTag::FIRST_SEEN);
    let mut writer = LiveWriter::open(&first_seen_path, budget(), &cancellation).unwrap();
    let mut refresh = writer.begin_first_seen_refresh(100, &cancellation).unwrap();
    let mut mapped = source.named_feed_source_v4("source").unwrap();
    refresh.add_ranges_v4(&mut mapped).unwrap();
    commit(refresh.finish_input().unwrap());
    writer.close().unwrap();

    create(&last_seen_path, ValueKind::Direct, ValueTag::LAST_SEEN);
    let mut writer = LiveWriter::open(&last_seen_path, budget(), &cancellation).unwrap();
    let mut refresh = writer
        .begin_last_seen_refresh(200, 0, &cancellation)
        .unwrap();
    let mut mapped = source.named_feed_source_v4("source").unwrap();
    refresh.add_ranges_v4(&mut mapped).unwrap();
    commit(refresh.finish_input().unwrap());
    writer.close().unwrap();

    snapshot_to(
        &source_path,
        SnapshotSourceMode::Live,
        &snapshot_path,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(2 * 1024 * 1024, 20_000, 3),
        &cancellation,
    )
    .unwrap();
    source.close().unwrap();

    let immutable = ImmutableReader::open(&snapshot_path).unwrap();
    let mut writer = LiveWriter::open(&membership_path, budget(), &cancellation).unwrap();
    let mut replacement = writer
        .begin_replace_feed(FeedName::new("copy").unwrap(), &cancellation)
        .unwrap();
    let mut mapped = immutable.named_feed_source_v4("source").unwrap();
    replacement.add_ranges_v4(&mut mapped).unwrap();
    match replacement.finish_input().unwrap() {
        FinishedWorkflow::NoChange(report) => assert_eq!(report.input_record_count, 600),
        FinishedWorkflow::Changed(_) => panic!("identical immutable source changed the feed"),
    }
    writer.close().unwrap();

    create(
        &direct_path,
        ValueKind::Direct,
        ValueTag::new(b"timestamps").unwrap(),
    );
    let mut first_seen = LiveReader::open(&first_seen_path, &cancellation).unwrap();
    let mut writer = LiveWriter::open(&direct_path, budget(), &cancellation).unwrap();
    let mut replacement = writer.begin_direct_replacement(&cancellation).unwrap();
    let mut mapped = first_seen.direct_range_source_v4().unwrap();
    replacement.add_ranges_v4(&mut mapped).unwrap();
    commit(replacement.finish_input().unwrap());
    writer.close().unwrap();

    ranges.reverse();
    first_seen.close().unwrap();

    let mut direct = LiveReader::open(&direct_path, &cancellation).unwrap();
    let mut last_seen = LiveReader::open(&last_seen_path, &cancellation).unwrap();
    for range in ranges.iter().step_by(113) {
        assert_eq!(direct.lookup_direct_v4(range.from).unwrap(), Some(100));
        assert_eq!(last_seen.lookup_direct_v4(range.from).unwrap(), Some(200));
    }
    direct.close().unwrap();
    last_seen.close().unwrap();
}
