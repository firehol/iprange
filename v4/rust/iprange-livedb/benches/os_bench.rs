//! v4 OS-layer benchmarks (scenarios 7-9 from SOW-0013).
//!
//! Run: cargo bench --manifest-path v4/rust/iprange-livedb/Cargo.toml --bench os_bench
//!
//! Scenarios:
//!   7. open_read    — MmapReader::open (file open + mmap + flock + §10 hardening)
//!   8. open_write   — FileWriter::open (file open + flock + mmap page store + free-set)
//!   9. create_file  — FileWriter::create (O_EXCL + initial write + fsync)
//!
//! These measure real file I/O. Temp files are created in setup (not timed) for
//! scenarios 7-8; scenario 9 measures the full create+commit+close cycle.

use criterion::{black_box, criterion_group, criterion_main, BenchmarkId, Criterion, Throughput};
use iprange_livedb::os::{FileWriter, MmapReader};
use iprange_livedb::{Ipv4Key, Writer};
use std::path::PathBuf;

/// Deterministic LCG (identical constants to the core bench and Go harness).
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

/// Create a temp file with `n` committed records, return its path.
fn make_db_file(tag: &str, n: usize) -> PathBuf {
    let path = std::env::temp_dir().join(format!(
        "iprange-v4-bench-{tag}-{n}-{pid}.iprdb",
        pid = std::process::id()
    ));
    let ranges = gen_ordered(n);
    let mut fw = FileWriter::<Ipv4Key>::create(&path, 1, 0, std::time::Duration::from_secs(30))
        .expect("create");
    for &(f, t) in &ranges {
        fw.set(Ipv4Key(f), Ipv4Key(t), &[1]).expect("set");
    }
    fw.commit(0).expect("commit");
    fw.close().expect("close");
    path
}

const SIZES: &[usize] = &[10_000, 100_000, 1_000_000];

// --- Scenario 7: open for reading (MmapReader::open) ---

fn bench_open_read_file(c: &mut Criterion) {
    let mut group = c.benchmark_group("7_open_read_file");
    let mut paths = Vec::new();
    for &n in SIZES {
        let path = make_db_file("rd", n);
        paths.push(path.clone());
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &path, |b, path| {
            b.iter(|| {
                let mr = MmapReader::open(path).unwrap();
                let r = mr.reader().unwrap();
                black_box(r);
                // mr dropped here → munmap + release LOCK_SH
                drop(mr);
            });
        });
    }
    group.finish();
    // Cleanup
    for p in paths {
        let _ = std::fs::remove_file(&p);
    }
}

// --- Scenario 8: open for writing (FileWriter::open) ---

fn bench_open_write_file(c: &mut Criterion) {
    let mut group = c.benchmark_group("8_open_write_file");
    let mut paths = Vec::new();
    for &n in SIZES {
        let path = make_db_file("wr", n);
        paths.push(path.clone());
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &path, |b, path| {
            b.iter(|| {
                let fw =
                    FileWriter::<Ipv4Key>::open(path, std::time::Duration::from_secs(30)).unwrap();
                black_box(fw.record_count());
                // fw dropped here → munmap + release LOCK_EX
            });
        });
    }
    group.finish();
    for p in paths {
        let _ = std::fs::remove_file(&p);
    }
}

// --- Scenario 9: create file (O_EXCL + write initial DB + fsync) ---

fn bench_create_file(c: &mut Criterion) {
    let mut group = c.benchmark_group("9_create_file");
    let counter = std::sync::atomic::AtomicU64::new(0);
    for &n in SIZES {
        let ranges = gen_ordered(n);
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(BenchmarkId::from_parameter(n), &ranges, |b, ranges| {
            b.iter_batched(
                || {
                    let i = counter.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    std::env::temp_dir().join(format!(
                        "iprange-v4-bench-cr-{n}-{pid}-{i}.iprdb",
                        pid = std::process::id()
                    ))
                },
                |path| {
                    let mut fw = FileWriter::<Ipv4Key>::create(
                        &path,
                        1,
                        0,
                        std::time::Duration::from_secs(30),
                    )
                    .unwrap();
                    for &(f, t) in ranges {
                        fw.set(Ipv4Key(f), Ipv4Key(t), &[1]).unwrap();
                    }
                    fw.commit(0).unwrap();
                    fw.close().unwrap();
                    black_box(&path);
                },
                criterion::BatchSize::LargeInput,
            );
        });
    }
    group.finish();
    // Cleanup any leftover files
    let pattern = format!("iprange-v4-bench-cr-*-{pid}-*", pid = std::process::id());
    if let Ok(entries) = std::fs::read_dir(std::env::temp_dir()) {
        for entry in entries.flatten() {
            if entry
                .file_name()
                .to_string_lossy()
                .contains(&pattern.replace('*', ""))
            {
                let _ = std::fs::remove_file(entry.path());
            }
        }
    }
}

criterion_group!(
    benches,
    bench_open_read_file,
    bench_open_write_file,
    bench_create_file,
);
criterion_main!(benches);
