//! Necessary-work comparison evidence (SOW-0027 direction item 6): this
//! test replays the nested-overwrite and live-direct-random-lookup
//! benchmark scenarios over the same deterministic workloads as the Go
//! peer (v4/go/cmd/iprange-v4-bench/necessary_work_v4work_test.go) and
//! prints the work::Snapshot as tab-separated rows
//! (rust <scenario> <field> <count>). Run with --nocapture; the SOW
//! evidence CSV compares the counts to prove the Go reader/writer
//! performs no more necessary work than Rust.

use std::fs;
use std::path::PathBuf;

use crate::{
    create_live, AddressFamily, CancellationToken, DirectRange, FinishedWorkflow, Ipv4Key,
    LiveReader, LiveWriter, StructureKind, TransactionBudget, ValueKind, ValueTag,
};

const WORKLOAD_N: usize = 100_000;
const BATCH: usize = 1024;
const DISPERSED_SEED: u64 = 0x9e37_79b9_7f4a_7c15;
const WORK_UNITS: usize = 1_000_000;

fn direct_tag() -> ValueTag {
    ValueTag::new(b"timestamp").unwrap()
}

fn budget() -> TransactionBudget {
    let scale = WORKLOAD_N.saturating_mul(8).saturating_add(128).max(20_000);
    TransactionBudget {
        max_heap_bytes: 64 * 1024 * 1024,
        max_private_pages: scale as u64,
        max_file_growth_pages: scale as u64,
        max_open_files: 2,
    }
}

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new(label: &str) -> Self {
        Self {
            main: crate::test_support_tests::unique_path(label),
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

fn create(pair: &TestPair, reader_capacity: u32) {
    create_live(
        &pair.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        StructureKind::None,
        direct_tag(),
        reader_capacity,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn changed(finished: FinishedWorkflow<'_>) -> crate::PreparedWorkflow<'_> {
    match finished {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(report) => panic!("expected a changed workflow, got {report:?}"),
    }
}

/// One complete direct replacement fed from the shrinking nested pattern
/// (bench scenarios/direct.rs nested + apply_direct; writer open through
/// close is the measured region, matching Go applyDirect).
fn nested_overwrite(pair: &TestPair) {
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&pair.main, budget(), &cancellation).unwrap();
    let mut workflow = writer.begin_direct_replacement(&cancellation).unwrap();
    let end = (WORKLOAD_N as u32) * 4 + 1;
    let mut batch = Vec::with_capacity(BATCH);
    for index in 0..WORKLOAD_N {
        batch.push(DirectRange {
            from: Ipv4Key(index as u32),
            to: Ipv4Key(end - index as u32),
            value: index as u32 % 2 + 1,
        });
        if batch.len() == BATCH {
            workflow.add_ranges_v4_slice(&batch).unwrap();
            batch.clear();
        }
    }
    if !batch.is_empty() {
        workflow.add_ranges_v4_slice(&batch).unwrap();
    }
    let finished = workflow.finish_input().unwrap();
    changed(finished).commit().unwrap();
    writer.close().unwrap();
}

/// The seeded unordered direct map the lookup scenarios read
/// (bench seeded_direct + DirectSource::unordered).
fn seeded_direct(pair: &TestPair) {
    create(pair, 1);
    let cancellation = CancellationToken::new();
    let mut writer = LiveWriter::open(&pair.main, budget(), &cancellation).unwrap();
    let mut workflow = writer.begin_direct_replacement(&cancellation).unwrap();
    let mut batch = Vec::with_capacity(BATCH);
    let perm = Permutation::new(WORKLOAD_N, DISPERSED_SEED);
    for ordinal in 0..WORKLOAD_N {
        let index = perm.at(ordinal);
        let start = index as u32 * 4;
        batch.push(DirectRange {
            from: Ipv4Key(start),
            to: Ipv4Key(start + 1),
            value: index as u32 % 251 + 1,
        });
        if batch.len() == BATCH {
            workflow.add_ranges_v4_slice(&batch).unwrap();
            batch.clear();
        }
    }
    if !batch.is_empty() {
        workflow.add_ranges_v4_slice(&batch).unwrap();
    }
    let finished = workflow.finish_input().unwrap();
    changed(finished).commit().unwrap();
    writer.close().unwrap();
}

fn splitmix64(state: &mut u64) -> u64 {
    *state = state.wrapping_add(0x9e37_79b9_7f4a_7c15);
    let mut value = *state;
    value = (value ^ (value >> 30)).wrapping_mul(0xbf58_476d_1ce4_e5b9);
    value = (value ^ (value >> 27)).wrapping_mul(0x94d0_49bb_1331_11eb);
    value ^ (value >> 31)
}

fn random_points() -> Vec<Ipv4Key> {
    let mut points: Vec<Ipv4Key> = (0..WORKLOAD_N)
        .map(|index| Ipv4Key((index as u32).saturating_mul(4)))
        .collect();
    let mut state = 0x6a09_e667_f3bc_c909u64;
    for upper in (1..points.len()).rev() {
        let random = splitmix64(&mut state);
        let index = ((u128::from(random) * (upper + 1) as u128) >> 64) as usize;
        points.swap(upper, index);
    }
    points
}

struct Permutation {
    count: usize,
    step: usize,
    offset: usize,
}

impl Permutation {
    fn new(count: usize, seed: u64) -> Self {
        if count <= 1 {
            return Self {
                count,
                step: 1,
                offset: 0,
            };
        }
        let mut step = seed as usize % count;
        if step == 0 {
            step = 1;
        }
        while gcd(step, count) != 1 {
            step += 1;
            if step == count {
                step = 1;
            }
        }
        Self {
            count,
            step,
            offset: seed.rotate_left(17) as usize % count,
        }
    }

    fn at(&self, ordinal: usize) -> usize {
        if self.count <= 1 {
            return 0;
        }
        ((ordinal as u128 * self.step as u128 + self.offset as u128) % self.count as u128) as usize
    }
}

fn gcd(mut left: usize, mut right: usize) -> usize {
    while right != 0 {
        let remainder = left % right;
        left = right;
        right = remainder;
    }
    left
}

fn snapshot_rows(snapshot: crate::work::Snapshot) -> Vec<(&'static str, u64)> {
    vec![
        ("tree_lookups", snapshot.tree_lookups),
        ("tree_descents", snapshot.tree_descents),
        ("pages_visited", snapshot.pages_visited),
        ("page_parses", snapshot.page_parses),
        ("key_probes", snapshot.key_probes),
        ("cell_probes", snapshot.cell_probes),
        ("leaf_validations", snapshot.leaf_validations),
        ("bitmap_probes", snapshot.bitmap_probes),
        ("slot_reads", snapshot.slot_reads),
        ("slot_scan_steps", snapshot.slot_scan_steps),
        ("edit_fit_probes", snapshot.edit_fit_probes),
        ("pages_created", snapshot.pages_created),
        ("pages_copied", snapshot.pages_copied),
        ("pages_split", snapshot.pages_split),
        ("first_fence_updates", snapshot.first_fence_updates),
        ("edge_path_checks", snapshot.edge_path_checks),
        ("leaf_locator_hits", snapshot.leaf_locator_hits),
        ("leaf_locator_misses", snapshot.leaf_locator_misses),
        ("leaf_locator_fallbacks", snapshot.leaf_locator_fallbacks),
        ("pages_retired", snapshot.pages_retired),
        ("pages_reclaimed", snapshot.pages_reclaimed),
        ("pages_sealed", snapshot.pages_sealed),
        ("ranges_consumed", snapshot.ranges_consumed),
        ("ranges_emitted", snapshot.ranges_emitted),
        ("ranges_split", snapshot.ranges_split),
        ("ranges_coalesced", snapshot.ranges_coalesced),
        ("catalog_lookups", snapshot.catalog_lookups),
        ("catalog_interns", snapshot.catalog_interns),
        ("membership_lookups", snapshot.membership_lookups),
        ("membership_interns", snapshot.membership_interns),
        ("structure_lookups", snapshot.structure_lookups),
        ("structure_decodes", snapshot.structure_decodes),
        ("structure_interns", snapshot.structure_interns),
        ("mapping_growths", snapshot.mapping_growths),
        ("mapping_remaps", snapshot.mapping_remaps),
        ("mapping_flushes", snapshot.mapping_flushes),
        ("file_syncs", snapshot.file_syncs),
        ("bytes_moved", snapshot.bytes_moved),
        ("bytes_zeroed", snapshot.bytes_zeroed),
        ("membership_leaf_reads", snapshot.membership_leaf_reads),
        (
            "membership_refcount_batches",
            snapshot.membership_refcount_batches,
        ),
        ("membership_delta_spills", snapshot.membership_delta_spills),
        ("source_passes", snapshot.source_passes),
        ("input_source_passes", snapshot.input_source_passes),
        ("output_passes", snapshot.output_passes),
        ("history_window_tests", snapshot.history_window_tests),
        ("membership_decodes", snapshot.membership_decodes),
        (
            "membership_decode_cache_hits",
            snapshot.membership_decode_cache_hits,
        ),
        ("membership_word_reads", snapshot.membership_word_reads),
        ("membership_combinations", snapshot.membership_combinations),
        (
            "membership_intern_cache_hits",
            snapshot.membership_intern_cache_hits,
        ),
        (
            "aggregation_contributions",
            snapshot.aggregation_contributions,
        ),
        ("aggregation_results", snapshot.aggregation_results),
        ("join_advances", snapshot.join_advances),
    ]
}

fn print_rows(scenario: &str, snapshot: crate::work::Snapshot) {
    for (field, value) in snapshot_rows(snapshot) {
        println!("rust\t{scenario}\t{field}\t{value}");
    }
}

/// Runs both scenarios and prints the counter snapshot rows.
#[test]
fn dump_necessary_work() {
    // Write scenario: nested overwrite (open through close, matching the
    // Go applyDirect measured region).
    let write = TestPair::new("necessary-write");
    create(&write, 1);
    let (_, write_work) = crate::work::measure(|| nested_overwrite(&write));
    print_rows("nested-overwrite", write_work);
    drop(write);

    // Read scenario: live direct random lookups over the seeded map
    // (lookup loop only, matching the Go measured region).
    let read = TestPair::new("necessary-read");
    seeded_direct(&read);
    let points = random_points();
    let cancellation = CancellationToken::new();
    let mut reader = LiveReader::open(&read.main, &cancellation).unwrap();
    let repetitions = WORK_UNITS.div_ceil(WORKLOAD_N);
    let (_hits, read_work) = crate::work::measure(|| {
        let mut hits = 0u64;
        for _ in 0..repetitions {
            for &address in &points {
                hits += u64::from(reader.lookup_direct_v4(address).unwrap().is_some());
            }
            std::hint::black_box(hits);
        }
        hits
    });
    print_rows("live-direct-random-lookup", read_work);
    reader.close().unwrap();
}
