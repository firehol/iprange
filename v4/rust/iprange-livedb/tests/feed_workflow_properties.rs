#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, CommitDurability,
    FeedName, FinishedWorkflow, Ipv4Key, LiveReader, LiveWriter, LogicalChange, TransactionBudget,
    ValueKind, ValueTag,
};

const DOMAIN: usize = 128;

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
                "iprange-v4-feed-property-{}-{unique}",
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
        max_private_pages: 100_000,
        max_file_growth_pages: 100_000,
        max_open_files: 2,
    }
}

fn name(value: &str) -> FeedName {
    FeedName::new(value).unwrap()
}

#[test]
fn randomized_feed_replacement_matches_scalar_sets_and_preserves_other_feed() {
    let files = TestPair::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"membership").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new()).unwrap();

    let target = writer
        .begin_create_feed(name("target"), &cancellation)
        .unwrap();
    finish(target.finish_input().unwrap());
    let mut other = writer
        .begin_create_feed(name("other"), &cancellation)
        .unwrap();
    let other_ranges: Vec<_> = (0..DOMAIN)
        .step_by(4)
        .map(|index| AddressRange {
            from: Ipv4Key(index as u32),
            to: Ipv4Key((index + 1) as u32),
        })
        .collect();
    other.add_ranges_v4_slice(&other_ranges).unwrap();
    finish(other.finish_input().unwrap());

    let mut random = Random(0xa7b1_1c49_d38e_52f0);
    let mut before = [false; DOMAIN];
    let mut other_expected = [false; DOMAIN];
    for range in &other_ranges {
        other_expected[range.from.0 as usize..=range.to.0 as usize].fill(true);
    }

    for iteration in 0..100 {
        let record_count = random.below(24) as usize;
        let mut records = Vec::with_capacity(record_count);
        let mut after = [false; DOMAIN];
        for _ in 0..record_count {
            let (from, to) = random.range();
            records.push(AddressRange {
                from: Ipv4Key(from),
                to: Ipv4Key(to),
            });
            after[from as usize..=to as usize].fill(true);
        }

        let mut workflow = writer
            .begin_replace_feed(name("target"), &cancellation)
            .unwrap();
        for batch in records.chunks(4) {
            workflow.add_ranges_v4_slice(batch).unwrap();
        }
        let finished = workflow.finish_input().unwrap();
        assert_report(finished.report(), &before, &after, record_count as u64);
        finish(finished);
        assert_database(&files.main, &after, &other_expected, iteration);
        before = after;
    }
    writer.close().unwrap();
}

fn finish(finished: FinishedWorkflow<'_>) {
    if let FinishedWorkflow::Changed(prepared) = finished {
        let committed = prepared.commit().unwrap();
        assert_eq!(
            committed.durability,
            CommitDurability::Committed,
            "{committed:?}"
        );
    }
}

fn assert_report(
    report: &iprange_livedb::WorkflowReport,
    before: &[bool; DOMAIN],
    after: &[bool; DOMAIN],
    input_records: u64,
) {
    let unchanged = paired_count(before, after, true, true);
    let added = paired_count(before, after, false, true);
    let removed = paired_count(before, after, true, false);
    let logical = if added == 0 && removed == 0 {
        LogicalChange::NoChange
    } else {
        LogicalChange::Changed
    };
    assert_eq!(report.logical_change, logical);
    assert_eq!(report.input_record_count, input_records);
    assert_eq!(report.input_normalized_interval_count, runs(after));
    assert_eq!(report.before_range_record_count, runs(before));
    assert_eq!(report.after_range_record_count, runs(after));
    assert_eq!(
        report.input_addresses,
        Cardinality129::from_u64(coverage(after))
    );
    assert_eq!(
        report.before_addresses,
        Cardinality129::from_u64(coverage(before))
    );
    assert_eq!(
        report.after_addresses,
        Cardinality129::from_u64(coverage(after))
    );
    assert_eq!(
        report.unchanged_value_addresses,
        Cardinality129::from_u64(unchanged)
    );
    assert_eq!(report.changed_value_addresses, Cardinality129::ZERO);
    assert_eq!(report.added_addresses, Cardinality129::from_u64(added));
    assert_eq!(report.removed_addresses, Cardinality129::from_u64(removed));
}

fn paired_count(before: &[bool], after: &[bool], old: bool, new: bool) -> u64 {
    before
        .iter()
        .zip(after)
        .filter(|(left, right)| **left == old && **right == new)
        .count() as u64
}

fn assert_database(
    path: &PathBuf,
    target: &[bool; DOMAIN],
    other: &[bool; DOMAIN],
    iteration: usize,
) {
    let mut reader = LiveReader::open(path, &CancellationToken::new()).unwrap();
    let target_index = reader.lookup_feed("target").unwrap().unwrap().index;
    let other_index = reader.lookup_feed("other").unwrap().unwrap().index;
    for address in 0..DOMAIN {
        let membership = reader
            .lookup_membership_v4(Ipv4Key(address as u32))
            .unwrap();
        let word = membership.as_ref().and_then(|view| view.word(0).unwrap());
        assert_eq!(
            membership
                .as_ref()
                .map(|view| view.contains_index(target_index).unwrap())
                .unwrap_or(false),
            target[address],
            "target membership at address {address}, iteration {iteration}, word {word:?}"
        );
        assert_eq!(
            membership
                .as_ref()
                .map(|view| view.contains_index(other_index).unwrap())
                .unwrap_or(false),
            other[address],
            "other membership at address {address}, iteration {iteration}, word {word:?}"
        );
    }
    reader.close().unwrap();
}

fn coverage(values: &[bool]) -> u64 {
    values.iter().filter(|&&value| value).count() as u64
}

fn runs(values: &[bool]) -> u64 {
    values
        .iter()
        .enumerate()
        .filter(|(index, value)| **value && (*index == 0 || !values[*index - 1]))
        .count() as u64
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
