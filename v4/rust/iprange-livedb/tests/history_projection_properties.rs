#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, DirectRange,
    FeedName, FinishedHistoryProjection, FinishedWorkflow, HistoryProjectionReport,
    HistoryProjectionSource, HistoryWindow, HistoryWindowReport, Ipv4Key, LiveReader, LiveWriter,
    LogicalChange, TransactionBudget, ValueKind, ValueTag,
};

const DOMAIN: usize = 128;
const WINDOW_COUNT: usize = 4;

struct Files {
    source: PathBuf,
    destination: PathBuf,
}

impl Files {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = format!(
            "iprange-v4-history-property-{}-{unique}",
            std::process::id()
        );
        Self {
            source: std::env::temp_dir().join(format!("{base}-source")),
            destination: std::env::temp_dir().join(format!("{base}-destination")),
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
        max_private_pages: 100_000,
        max_file_growth_pages: 100_000,
        max_open_files: 2,
    }
}

fn finish_workflow(finished: FinishedWorkflow<'_>) {
    match finished {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => {}
    }
}

fn finish_projection(
    finished: FinishedHistoryProjection<'_>,
    expected_change: LogicalChange,
) -> HistoryProjectionReport {
    match finished {
        FinishedHistoryProjection::Changed(prepared) => {
            assert_eq!(expected_change, LogicalChange::Changed);
            let report = prepared.report().clone();
            prepared.commit().unwrap();
            report
        }
        FinishedHistoryProjection::NoChange(report) => {
            assert_eq!(expected_change, LogicalChange::NoChange);
            report
        }
    }
}

#[test]
fn randomized_projection_matches_independent_scalar_model() {
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

    let mut destination = LiveWriter::open(&files.destination, budget(), &cancellation).unwrap();
    let unrelated: Vec<_> = (0..DOMAIN)
        .step_by(5)
        .map(|address| AddressRange {
            from: Ipv4Key(address as u32),
            to: Ipv4Key(address as u32),
        })
        .collect();
    let mut create = destination
        .begin_create_feed(FeedName::new("unrelated").unwrap(), &cancellation)
        .unwrap();
    create.add_ranges_v4_slice(&unrelated).unwrap();
    finish_workflow(create.finish_input().unwrap());

    let names = [
        FeedName::new("history-a").unwrap(),
        FeedName::new("history-b").unwrap(),
        FeedName::new("history-c").unwrap(),
        FeedName::new("history-d").unwrap(),
    ];
    let mut before = [[false; DOMAIN]; WINDOW_COUNT];
    let mut random = Random(0x0ef4_ba82_4981_7c61);

    for round in 0..40 {
        let input_count = random.below(32) as usize;
        let mut input = Vec::with_capacity(input_count);
        let mut source_model = [None; DOMAIN];
        for _ in 0..input_count {
            let (from, to) = random.range();
            let value = random.below(16);
            input.push(DirectRange {
                from: Ipv4Key(from),
                to: Ipv4Key(to),
                value,
            });
            source_model[from as usize..=to as usize].fill(Some(value));
        }
        let mut source_writer = LiveWriter::open(&files.source, budget(), &cancellation).unwrap();
        let mut replacement = source_writer
            .begin_direct_replacement(&cancellation)
            .unwrap();
        for batch in input.chunks(5) {
            replacement.add_ranges_v4_slice(batch).unwrap();
        }
        finish_workflow(replacement.finish_input().unwrap());
        source_writer.close().unwrap();

        let cutoffs = [
            random.below(18),
            random.below(18),
            random.below(18),
            random.below(18),
        ];
        let windows: [HistoryWindow; WINDOW_COUNT] = std::array::from_fn(|index| HistoryWindow {
            feed_name: names[index],
            cutoff: cutoffs[index],
        });
        let after: [[bool; DOMAIN]; WINDOW_COUNT] = std::array::from_fn(|window| {
            std::array::from_fn(|address| {
                source_model[address].is_some_and(|value| value > cutoffs[window])
            })
        });
        let created = round == 0;
        let expected_change = if created || before != after {
            LogicalChange::Changed
        } else {
            LogicalChange::NoChange
        };

        let mut source = LiveReader::open(&files.source, &cancellation).unwrap();
        let finished = destination
            .project_history(
                HistoryProjectionSource::Live(&source),
                &windows,
                &cancellation,
            )
            .unwrap();
        let report = finish_projection(finished, expected_change);
        assert_eq!(report.logical_change, expected_change);
        assert_eq!(
            report.created_feed_count,
            u64::from(created) * WINDOW_COUNT as u64
        );
        assert_eq!(report.source_range_count, value_runs(&source_model));
        assert_eq!(report.source_addresses, count_present(&source_model));
        for window in 0..WINDOW_COUNT {
            assert_window(
                &report.windows[window],
                windows[window],
                created,
                &before[window],
                &after[window],
            );
        }
        let before_union = union(&before);
        let after_union = union(&after);
        assert_aggregate(&report, &before_union, &after_union);

        let mut reader = LiveReader::open(&files.destination, &cancellation).unwrap();
        let indexes = std::array::from_fn::<_, WINDOW_COUNT, _>(|index| {
            reader
                .lookup_feed(names[index].as_str())
                .unwrap()
                .unwrap()
                .index
        });
        let unrelated_index = reader.lookup_feed("unrelated").unwrap().unwrap().index;
        for (address, _) in after[0].iter().enumerate() {
            let membership = reader
                .lookup_membership_v4(Ipv4Key(address as u32))
                .unwrap();
            for window in 0..WINDOW_COUNT {
                let actual = membership
                    .as_ref()
                    .is_some_and(|view| view.contains_index(indexes[window]).unwrap());
                assert_eq!(actual, after[window][address]);
            }
            let actual_unrelated = membership
                .as_ref()
                .is_some_and(|view| view.contains_index(unrelated_index).unwrap());
            assert_eq!(actual_unrelated, address % 5 == 0);
        }
        reader.close().unwrap();
        source.close().unwrap();
        before = after;
    }
    destination.close().unwrap();
}

fn assert_window(
    report: &HistoryWindowReport,
    window: HistoryWindow,
    created: bool,
    before: &[bool; DOMAIN],
    after: &[bool; DOMAIN],
) {
    assert_eq!(report.feed_name, window.feed_name);
    assert_eq!(report.cutoff, window.cutoff);
    assert_eq!(report.created, created);
    assert_eq!(report.before_interval_count, bool_runs(before));
    assert_eq!(report.after_interval_count, bool_runs(after));
    assert_eq!(report.before_addresses, bool_count(before));
    assert_eq!(report.after_addresses, bool_count(after));
    let (unchanged, added, removed) = changes(before, after);
    assert_eq!(report.unchanged_addresses, unchanged);
    assert_eq!(report.added_addresses, added);
    assert_eq!(report.removed_addresses, removed);
}

fn assert_aggregate(
    report: &HistoryProjectionReport,
    before: &[bool; DOMAIN],
    after: &[bool; DOMAIN],
) {
    assert_eq!(report.before_interval_count, bool_runs(before));
    assert_eq!(report.after_interval_count, bool_runs(after));
    assert_eq!(report.before_addresses, bool_count(before));
    assert_eq!(report.after_addresses, bool_count(after));
    let (unchanged, added, removed) = changes(before, after);
    assert_eq!(report.unchanged_addresses, unchanged);
    assert_eq!(report.added_addresses, added);
    assert_eq!(report.removed_addresses, removed);
}

fn union(states: &[[bool; DOMAIN]; WINDOW_COUNT]) -> [bool; DOMAIN] {
    std::array::from_fn(|address| states.iter().any(|state| state[address]))
}

fn bool_runs(values: &[bool; DOMAIN]) -> u64 {
    values
        .iter()
        .enumerate()
        .filter(|&(index, value)| *value && (index == 0 || !values[index - 1]))
        .count() as u64
}

fn bool_count(values: &[bool; DOMAIN]) -> Cardinality129 {
    Cardinality129::from_u64(values.iter().filter(|&&value| value).count() as u64)
}

fn changes(
    before: &[bool; DOMAIN],
    after: &[bool; DOMAIN],
) -> (Cardinality129, Cardinality129, Cardinality129) {
    let mut unchanged = 0;
    let mut added = 0;
    let mut removed = 0;
    for (&before, &after) in before.iter().zip(after) {
        match (before, after) {
            (true, true) => unchanged += 1,
            (false, true) => added += 1,
            (true, false) => removed += 1,
            (false, false) => {}
        }
    }
    (
        Cardinality129::from_u64(unchanged),
        Cardinality129::from_u64(added),
        Cardinality129::from_u64(removed),
    )
}

fn value_runs(values: &[Option<u32>; DOMAIN]) -> u64 {
    values
        .iter()
        .enumerate()
        .filter(|&(index, value)| value.is_some() && (index == 0 || values[index - 1] != *value))
        .count() as u64
}

fn count_present(values: &[Option<u32>; DOMAIN]) -> Cardinality129 {
    Cardinality129::from_u64(values.iter().filter(|value| value.is_some()).count() as u64)
}

struct Random(u64);

impl Random {
    fn next(&mut self) -> u32 {
        self.0 ^= self.0 << 13;
        self.0 ^= self.0 >> 7;
        self.0 ^= self.0 << 17;
        self.0 as u32
    }

    fn below(&mut self, limit: u32) -> u32 {
        self.next() % limit
    }

    fn range(&mut self) -> (u32, u32) {
        let left = self.below(DOMAIN as u32);
        let right = self.below(DOMAIN as u32);
        (left.min(right), left.max(right))
    }
}
