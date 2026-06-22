//! Early Rust-vs-Go speed check (SOW-0002 decision point). Same LCG and workload as
//! the Go `TestSpeedReport`, so the numbers are directly comparable.
//!
//! Run release + show output (it is `#[ignore]` so the normal suite stays fast):
//!   cargo test -p iprange-format --release --test speed -- --ignored --nocapture

use std::time::Instant;

use iprange_format::{FeedMeta, Ipv4Key, Reader, Writer};

/// The shared deterministic generator (identical constants to the Go harness).
struct Lcg(u64);
impl Lcg {
    fn new(seed: u64) -> Self {
        Lcg(seed ^ 0x9e37_79b9_7f4a_7c15)
    }
    fn next(&mut self) -> u32 {
        self.0 = self
            .0
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        (self.0 >> 33) as u32
    }
}

fn meta() -> FeedMeta {
    FeedMeta {
        name: "firehol_level1".into(),
        category: "attacks".into(),
        license: "GPL-2.0".into(),
        ..Default::default()
    }
}

#[test]
#[ignore = "benchmark; run explicitly with --release --ignored --nocapture"]
fn speed_report() {
    const N: usize = 200_000;
    const LOOKUPS: usize = 1_000_000;

    // Build the same ascending disjoint workload as Go's genWorkload(N).
    let mut rng = Lcg::new(1);
    let mut spec: Vec<(u32, u32)> = Vec::with_capacity(N);
    let mut cursor: u32 = 0;
    for _ in 0..N {
        let gap = 1 + rng.next() % 16;
        let width = rng.next() % 8;
        let start = match cursor.checked_add(gap) {
            Some(s) => s,
            None => break,
        };
        let end = match start.checked_add(width) {
            Some(e) => e,
            None => break,
        };
        spec.push((start, end));
        cursor = end;
    }

    let t0 = Instant::now();
    let mut w = Writer::<Ipv4Key>::new(meta(), 0, 1700);
    for &(s, e) in &spec {
        w.add_range(Ipv4Key(s), Ipv4Key(e), None).unwrap();
    }
    let bytes = w.build().unwrap();
    let build_dur = t0.elapsed();

    let r = Reader::open(&bytes).unwrap();

    let mut rng = Lcg::new(99);
    let mut hits = 0usize;
    let t1 = Instant::now();
    for _ in 0..LOOKUPS {
        let key = Ipv4Key(rng.next());
        if r.lookup_v4(key).unwrap().is_some() {
            hits += 1;
        }
    }
    let lookup_dur = t1.elapsed();

    eprintln!(
        "RUST build: {} ranges -> {} bytes in {:?} ({:.1} ns/range)",
        spec.len(),
        bytes.len(),
        build_dur,
        build_dur.as_nanos() as f64 / spec.len() as f64
    );
    eprintln!(
        "RUST lookup: {} lookups in {:?} ({:.1} ns/op), hit rate {:.1}%",
        LOOKUPS,
        lookup_dur,
        lookup_dur.as_nanos() as f64 / LOOKUPS as f64,
        100.0 * hits as f64 / LOOKUPS as f64
    );
}
