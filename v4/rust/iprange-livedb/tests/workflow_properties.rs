use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, DirectRange,
    FinishedWorkflow, Ipv4Key, LiveReader, LiveWriter, LogicalChange, TransactionBudget, ValueKind,
    ValueTag, WorkflowReport,
};

const DOMAIN: usize = 128;

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
                "iprange-v4-workflow-property-{label}-{}-{unique}",
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

#[test]
fn randomized_direct_replacement_matches_scalar_state_and_report() {
    let files = TestPair::new("direct");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"direct").unwrap(),
        1,
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget()).unwrap();
    let mut random = Random(0x8bcf_28d1_930e_44a7);
    let mut before = [None; DOMAIN];

    for _ in 0..100 {
        let record_count = 1 + random.below(24) as usize;
        let mut records = Vec::with_capacity(record_count);
        let mut after = [None; DOMAIN];
        for _ in 0..record_count {
            let (from, to) = random.range();
            let value = random.below(6);
            records.push(DirectRange {
                from: Ipv4Key(from),
                to: Ipv4Key(to),
                value,
            });
            after[from as usize..=to as usize].fill(Some(value));
        }

        let mut workflow = writer.begin_direct_replacement(&cancellation).unwrap();
        for batch in records.chunks(3) {
            workflow.add_ranges_v4_slice(batch).unwrap();
        }
        let finished = workflow.finish_input().unwrap();
        assert_report(
            finished.report(),
            &before,
            &after,
            record_count as u64,
            runs(&after),
            coverage(&after),
        );
        finish(finished);
        assert_database(&files.main, &after);
        before = after;
    }
    writer.close().unwrap();
}

#[test]
fn randomized_retention_refresh_matches_full_delta_semantics() {
    let files = TestPair::new("retention");
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        1,
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&files.main, budget()).unwrap();
    let mut random = Random(0x57de_8a11_c442_793b);
    let mut before = [None; DOMAIN];

    for round in 1..=100u32 {
        let record_count = random.below(24) as usize;
        let mut records = Vec::with_capacity(record_count);
        let mut desired = [false; DOMAIN];
        for _ in 0..record_count {
            let (from, to) = random.range();
            records.push(AddressRange {
                from: Ipv4Key(from),
                to: Ipv4Key(to),
            });
            desired[from as usize..=to as usize].fill(true);
        }
        let mut after = [None; DOMAIN];
        for index in 0..DOMAIN {
            if desired[index] {
                after[index] = before[index].or(Some(round));
            }
        }

        let mut workflow = writer
            .begin_retention_refresh(round, &cancellation)
            .unwrap();
        for batch in records.chunks(4) {
            workflow.add_ranges_v4_slice(batch).unwrap();
        }
        let finished = workflow.finish_input().unwrap();
        assert_report(
            finished.report(),
            &before,
            &after,
            record_count as u64,
            boolean_runs(&desired),
            desired.iter().filter(|&&present| present).count() as u64,
        );
        finish(finished);
        assert_database(&files.main, &after);
        before = after;
    }
    writer.close().unwrap();
}

fn finish(finished: FinishedWorkflow<'_>) {
    match finished {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => {}
    }
}

fn assert_report(
    report: &WorkflowReport,
    before: &[Option<u32>; DOMAIN],
    after: &[Option<u32>; DOMAIN],
    input_records: u64,
    input_intervals: u64,
    input_addresses: u64,
) {
    let expected = compare(before, after);
    assert_eq!(report.logical_change, expected.logical_change);
    assert_eq!(report.input_record_count, input_records);
    assert_eq!(report.input_normalized_interval_count, input_intervals);
    assert_eq!(report.before_range_record_count, runs(before));
    assert_eq!(report.after_range_record_count, runs(after));
    assert_eq!(
        report.input_addresses,
        Cardinality129::from_u64(input_addresses)
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
        Cardinality129::from_u64(expected.unchanged)
    );
    assert_eq!(
        report.changed_value_addresses,
        Cardinality129::from_u64(expected.changed)
    );
    assert_eq!(
        report.added_addresses,
        Cardinality129::from_u64(expected.added)
    );
    assert_eq!(
        report.removed_addresses,
        Cardinality129::from_u64(expected.removed)
    );
}

fn assert_database(path: &PathBuf, expected: &[Option<u32>; DOMAIN]) {
    let reader = LiveReader::open(path).unwrap();
    for (address, value) in expected.iter().enumerate() {
        assert_eq!(
            reader.lookup_direct_v4(Ipv4Key(address as u32)).unwrap(),
            *value
        );
    }
    reader.close().unwrap();
}

struct Counts {
    logical_change: LogicalChange,
    unchanged: u64,
    changed: u64,
    added: u64,
    removed: u64,
}

fn compare(before: &[Option<u32>; DOMAIN], after: &[Option<u32>; DOMAIN]) -> Counts {
    let mut counts = Counts {
        logical_change: LogicalChange::NoChange,
        unchanged: 0,
        changed: 0,
        added: 0,
        removed: 0,
    };
    for (&old, &new) in before.iter().zip(after) {
        match (old, new) {
            (Some(left), Some(right)) if left == right => counts.unchanged += 1,
            (Some(_), Some(_)) => counts.changed += 1,
            (None, Some(_)) => counts.added += 1,
            (Some(_), None) => counts.removed += 1,
            (None, None) => {}
        }
    }
    if counts.changed != 0 || counts.added != 0 || counts.removed != 0 {
        counts.logical_change = LogicalChange::Changed;
    }
    counts
}

fn coverage(values: &[Option<u32>]) -> u64 {
    values.iter().filter(|value| value.is_some()).count() as u64
}

fn runs(values: &[Option<u32>]) -> u64 {
    values
        .iter()
        .enumerate()
        .filter(|(index, value)| value.is_some() && (*index == 0 || values[*index - 1] != **value))
        .count() as u64
}

fn boolean_runs(values: &[bool]) -> u64 {
    values
        .iter()
        .enumerate()
        .filter(|(index, present)| **present && (*index == 0 || !values[*index - 1]))
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
