//! Robustness: structural rejection, round-trip property checks, and panic-safety.
//!
//! These complement the conformance corpus: the corpus pins exact bytes, while these
//! stress the reader against malformed input (it must always return `Result`, never
//! panic or read out of bounds) and check writer↔reader agreement over random inputs.

use iprange_format::{Error, FeedMeta, Ipv4Key, Reader, Value, Writer};

/// Tiny deterministic LCG (no dep) — reproducible "random" inputs.
struct Lcg(u64);
impl Lcg {
    fn new(seed: u64) -> Self {
        Lcg(seed ^ 0x9e37_79b9_7f4a_7c15)
    }
    fn next_u32(&mut self) -> u32 {
        self.0 = self.0.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
        (self.0 >> 33) as u32
    }
}

fn meta() -> FeedMeta {
    FeedMeta {
        name: "robust".into(),
        category: "test".into(),
        ..Default::default()
    }
}

fn valid_v4() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::new(meta(), 0, 1700);
    w.add_range(Ipv4Key(0x0a00_0000), Ipv4Key(0x0a00_00ff), None).unwrap();
    w.add_range(
        Ipv4Key(0x0b00_0000),
        Ipv4Key(0x0b00_000f),
        Some(Value { type_id: 2, bytes: vec![1, 2, 3, 4] }),
    )
    .unwrap();
    w.build().unwrap()
}

// ---- structural rejections (header fields are not hashed → deterministic class) ----

fn put_u16(b: &mut [u8], at: usize, v: u16) {
    b[at..at + 2].copy_from_slice(&v.to_le_bytes());
}

#[test]
fn reject_bad_magic() {
    let mut b = valid_v4();
    b[0] = b'X';
    assert!(matches!(Reader::open(&b), Err(Error::BadMagic)));
}

#[test]
fn reject_bad_major() {
    let mut b = valid_v4();
    put_u16(&mut b, 8, 4);
    assert!(matches!(Reader::open(&b), Err(Error::UnsupportedMajor(4))));
}

#[test]
fn reject_bad_header_size() {
    let mut b = valid_v4();
    put_u16(&mut b, 12, 80); // != 72 for v3.0
    assert!(matches!(Reader::open(&b), Err(Error::BadHeaderSize(80))));
}

#[test]
fn reject_reserved_flag_bit() {
    let mut b = valid_v4();
    put_u16(&mut b, 14, 0b10); // reserved header flag bit
    assert!(matches!(Reader::open(&b), Err(Error::NonZeroReserved(_))));
}

#[test]
fn reject_file_size_mismatch() {
    let mut b = valid_v4();
    let n = b.len() as u64;
    b[16..24].copy_from_slice(&(n + 1).to_le_bytes()); // claim one extra byte
    assert!(matches!(Reader::open(&b), Err(Error::FileSizeMismatch { .. })));
}

#[test]
fn reject_directory_count_too_small() {
    let mut b = valid_v4();
    b[32..36].copy_from_slice(&2u32.to_le_bytes()); // < 3
    assert!(matches!(Reader::open(&b), Err(Error::Structural(_))));
}

#[test]
fn reject_truncation() {
    let b = valid_v4();
    for cut in [1usize, 50, 72, b.len() - 1] {
        if cut < b.len() {
            let t = &b[..cut];
            // Must not panic; must reject (too short or size mismatch).
            assert!(Reader::open(t).is_err(), "truncation to {cut} must be rejected");
        }
    }
}

// ---- panic-safety: malformed input must always return Result, never panic ----

#[test]
fn single_byte_flips_never_panic() {
    let base = valid_v4();
    for i in 0..base.len() {
        for xor in [0x01u8, 0x80, 0xff] {
            let mut b = base.clone();
            b[i] ^= xor;
            // The result may be Ok (benign byte, e.g. inside generation_unixtime) or
            // Err; the contract is only that open() returns without panicking.
            let _ = Reader::open(&b);
        }
    }
}

#[test]
fn random_buffers_never_panic() {
    let mut rng = Lcg::new(0xC0FFEE);
    for len in [0usize, 1, 8, 71, 72, 73, 200, 1024] {
        for _ in 0..64 {
            let buf: Vec<u8> = (0..len).map(|_| rng.next_u32() as u8).collect();
            let _ = Reader::open(&buf);
            let _ = Reader::open_metadata_only(&buf);
        }
    }
}

#[test]
fn truncations_of_valid_never_panic() {
    let base = valid_v4();
    for cut in 0..=base.len() {
        let _ = Reader::open(&base[..cut]);
    }
}

// ---- property: writer↔reader agreement over random disjoint inputs ----

#[test]
fn round_trip_random_disjoint_ranges() {
    for seed in 0..40u64 {
        let mut rng = Lcg::new(seed);
        let n = 1 + (rng.next_u32() % 60);
        // Build disjoint, ascending ranges with random gaps and values.
        let mut cursor: u32 = rng.next_u32() % 1000;
        let mut spec: Vec<(u32, u32, Option<u32>)> = Vec::new();
        for _ in 0..n {
            let gap = 1 + (rng.next_u32() % 50);
            let width = rng.next_u32() % 40;
            let start = cursor.checked_add(gap);
            let start = match start {
                Some(s) => s,
                None => break,
            };
            let end = match start.checked_add(width) {
                Some(e) => e,
                None => break,
            };
            let val = if rng.next_u32() % 2 == 0 {
                None
            } else {
                Some(rng.next_u32() % 4) // a few distinct values to exercise dedup
            };
            spec.push((start, end, val));
            cursor = end;
        }

        let mut w = Writer::<Ipv4Key>::new(meta(), 0, seed);
        for &(s, e, v) in &spec {
            let value = v.map(|x| Value { type_id: 2, bytes: x.to_le_bytes().to_vec() });
            w.add_range(Ipv4Key(s), Ipv4Key(e), value).unwrap();
        }
        let bytes = w.build().unwrap();
        let r = Reader::open(&bytes).unwrap();

        // Every range's endpoints and midpoint must be found; gaps must miss.
        for &(s, e, _) in &spec {
            assert!(r.lookup_v4(Ipv4Key(s)).unwrap().is_some(), "seed {seed}: start {s} missing");
            assert!(r.lookup_v4(Ipv4Key(e)).unwrap().is_some(), "seed {seed}: end {e} missing");
            let mid = s + (e - s) / 2;
            assert!(r.lookup_v4(Ipv4Key(mid)).unwrap().is_some(), "seed {seed}: mid {mid} missing");
        }
        // A point strictly between two spec ranges (in a gap) must miss. Check the
        // byte just below each range start that isn't covered by the previous range.
        for win in spec.windows(2) {
            let prev_end = win[0].1;
            let next_start = win[1].0;
            if next_start > prev_end + 1 {
                let gap_point = prev_end + 1;
                assert!(
                    r.lookup_v4(Ipv4Key(gap_point)).unwrap().is_none(),
                    "seed {seed}: gap point {gap_point} unexpectedly present"
                );
            }
        }
    }
}
