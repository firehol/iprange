//! v4 in-memory core benchmarks (scenarios 1-6 from SOW-0013).
//!
//! Run: cargo bench --manifest-path v4/rust/iprange-livedb/Cargo.toml --bench core
//!
//! Scenarios:
//!   1. scan       — ordered read (full traversal)
//!   2. append     — ordered write (monotonic disjoint keys)
//!   3. set_random — unordered write (random ranges)
//!   4. set_same   — write collision (re-set existing ranges)
//!   5. hit        — lookup existing keys
//!   6. miss       — lookup non-existing keys (gaps)
//!
//! All scenarios are parameterized by record count (10k, 100k, 1M) and use IPv4 with
//! scope_width=1 (the simplest and most common production shape).

use criterion::{
    black_box, criterion_group, criterion_main, BenchmarkId, Criterion, Throughput,
};
use iprange_livedb::{Ipv4Key, Reader, Writer};

/// Deterministic LCG (identical constants to the v3 speed test and the Go harness).
struct Lcg(u64);
impl Lcg {
    fn new(seed: u64) -> Self {
        Lcg(seed ^ 0x9e37_79b9_7f4a_7c15)
    }
    fn next(&mut self) -> u32 {
        self.0 = self.0.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
        (self.0 >> 33) as u32
    }
}

/// Generate `n` ascending disjoint ranges [start, end] with small random gaps.
fn gen_ordered(n: usize) -> Vec<(u32, u32)> {
    let mut rng = Lcg::new(1);
    let mut out = Vec::with_capacity(n);
    let mut cursor = 0u32;
    for _ in 0..n {
        let gap = 1 + rng.next() % 16;
        let width = rng.next() % 8;
        let start = cursor.saturating_add(gap);
        let end = start.saturating_add(width);
        if start == u32::MAX || end == u32::MAX {
            break;
        }
        out.push((start, end));
        cursor = end;
    }
    out
}

/// Generate `n` random ranges (may overlap) in a bounded key space.
fn gen_random(n: usize) -> Vec<(u32, u32)> {
    let mut rng = Lcg::new(2);
    let span = (n as u32 * 10).max(1000); // dense key space → heavy overlap
    let mut out = Vec::with_capacity(n);
    for _ in 0..n {
        let a = rng.next() % span;
        let b = rng.next() % span;
        let (from, to) = if a <= b { (a, b) } else { (b, a) };
        out.push((from, to));
    }
    out
}

/// Build a committed writer from `ranges` using `append` (trusted ordered fast-path).
fn build_db_append(ranges: &[(u32, u32)]) -> Writer<Ipv4Key> {
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    for &(f, t) in ranges {
        w.append(Ipv4Key(f), Ipv4Key(t), &[1]).unwrap();
    }
    w.commit(0).unwrap();
    w
}

const SIZES: &[usize] = &[10_000, 100_000, 1_000_000];

// --- Scenario 1: ordered read (scan) ---

fn bench_scan(c: &mut Criterion) {
    let mut group = c.benchmark_group("1_scan");
    for &n in SIZES {
        let ranges = gen_ordered(n);
        let w = build_db_append(&ranges);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &r, |b, r| {
            b.iter(|| {
                let mut count = 0u64;
                r.scan_v4(|_, _, _| count += 1).unwrap();
                black_box(count);
            });
        });
    }
    group.finish();
}

// --- Scenario 2: ordered write (append) ---

fn bench_append(c: &mut Criterion) {
    let mut group = c.benchmark_group("2_append");
    for &n in SIZES {
        let ranges = gen_ordered(n);
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &ranges, |b, ranges| {
            b.iter(|| {
                let mut w = Writer::<Ipv4Key>::create(1, 0);
                for &(f, t) in ranges {
                    w.append(Ipv4Key(f), Ipv4Key(t), &[1]).unwrap();
                }
                w.commit(0).unwrap();
                black_box(w);
            });
        });
    }
    group.finish();
}

// --- Scenario 3: unordered write (set random) ---

fn bench_set_random(c: &mut Criterion) {
    let mut group = c.benchmark_group("3_set_random");
    for &n in SIZES {
        let ranges = gen_random(n);
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &ranges, |b, ranges| {
            b.iter(|| {
                let mut w = Writer::<Ipv4Key>::create(1, 0);
                for &(f, t) in ranges {
                    w.set(Ipv4Key(f), Ipv4Key(t), &[1]).unwrap();
                }
                w.commit(0).unwrap();
                black_box(w);
            });
        });
    }
    group.finish();
}

// --- Scenario 4: write collision (re-set existing ranges) ---

fn bench_set_collision(c: &mut Criterion) {
    let mut group = c.benchmark_group("4_set_collision");
    for &n in SIZES {
        let ranges = gen_ordered(n);
        // Pre-build the DB; the bench re-sets the same ranges (every set is a collision).
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &ranges, |b, ranges| {
            b.iter_batched(
                || build_db_append(ranges),
                |w| {
                    let mut w = w;
                    for &(f, t) in ranges {
                        w.set(Ipv4Key(f), Ipv4Key(t), &[1]).unwrap();
                    }
                    black_box(w);
                },
                criterion::BatchSize::LargeInput,
            );
        });
    }
    group.finish();
}

// --- Scenario 5: lookup existing keys (hit) ---

fn bench_lookup_hit(c: &mut Criterion) {
    let mut group = c.benchmark_group("5_lookup_hit");
    for &n in SIZES {
        let ranges = gen_ordered(n);
        let w = build_db_append(&ranges);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        // Keys that hit: the midpoint of each range.
        let keys: Vec<u32> = ranges.iter().map(|&(f, t)| f + (t - f) / 2).collect();
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &(&r, &keys), |b, (r, keys)| {
            b.iter(|| {
                let mut found = 0u32;
                for &k in keys.iter() {
                    if r.lookup_v4(Ipv4Key(k)).unwrap().is_some() {
                        found += 1;
                    }
                }
                black_box(found);
            });
        });
    }
    group.finish();
}

// --- Scenario 6: lookup non-existing keys (miss) ---

fn bench_lookup_miss(c: &mut Criterion) {
    let mut group = c.benchmark_group("6_lookup_miss");
    for &n in SIZES {
        let ranges = gen_ordered(n);
        let w = build_db_append(&ranges);
        let img = w.into_image();
        let r = Reader::open(&img).unwrap();
        // Keys that miss: the gaps between ranges (each gap is 1-16 wide; use gap midpoint).
        let mut keys = Vec::with_capacity(ranges.len());
        for window in ranges.windows(2) {
            let gap_start = window[0].1 + 1;
            let gap_end = window[1].0;
            if gap_end > gap_start {
                keys.push(gap_start + (gap_end - gap_start) / 2);
            }
        }
        group.throughput(Throughput::Elements(keys.len() as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &(&r, &keys), |b, (r, keys)| {
            b.iter(|| {
                let mut found = 0u32;
                for &k in keys.iter() {
                    if r.lookup_v4(Ipv4Key(k)).unwrap().is_some() {
                        found += 1;
                    }
                }
                black_box(found);
            });
        });
    }
    group.finish();
}

// --- Scenario 7: open for reading (trusted, no validate) ---

fn bench_open_read(c: &mut Criterion) {
    let mut group = c.benchmark_group("7_open_read");
    for &n in SIZES {
        let ranges = gen_ordered(n);
        let w = build_db_append(&ranges);
        let img = w.into_image();
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &img, |b, img| {
            b.iter(|| {
                let r = Reader::open(img).unwrap();
                black_box(r);
            });
        });
    }
    group.finish();
}

// --- Scenario 7b: open + validate (full §9 walk) — for comparison ---

fn bench_open_validate(c: &mut Criterion) {
    let mut group = c.benchmark_group("7b_open_validate");
    for &n in SIZES {
        let ranges = gen_ordered(n);
        let w = build_db_append(&ranges);
        let img = w.into_image();
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &img, |b, img| {
            b.iter(|| {
                let r = Reader::open(img).unwrap();
                r.validate().unwrap();
                black_box(r);
            });
        });
    }
    group.finish();
}

criterion_group!(
    benches,
    bench_scan,
    bench_append,
    bench_set_random,
    bench_set_collision,
    bench_lookup_hit,
    bench_lookup_miss,
    bench_open_read,
    bench_open_validate,
);
criterion_main!(benches);
