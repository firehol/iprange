//! Hostile-input robustness (§9 / §10): `Reader::open` must return only `Ok` or a typed
//! `Err` — **never** panic, loop, or read out of bounds — on truncations, bit-flips, and
//! arbitrary buffers. (`Writer::open_image` runs the same validation, so this also
//! guards the writer's open path.)

use iprange_livedb::spec::PAGE_SIZE;
use iprange_livedb::{Ipv4Key, Reader, Writer};

/// A multi-level valid file with some freed (unreachable) pages, to exercise both the
/// reachable-page reject path and the unreachable-page ignore path.
fn valid_file() -> Vec<u8> {
    let mut w = Writer::<Ipv4Key>::create(1, 0);
    for i in 0..2000u32 {
        w.set(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2), &[(i & 0xff) as u8])
            .unwrap();
    }
    for i in (0..2000u32).step_by(5) {
        w.delete(Ipv4Key(i * 7), Ipv4Key(i * 7 + 2)).unwrap(); // frees pages
    }
    w.commit(0);
    w.into_image()
}

fn lcg(state: &mut u64) -> u64 {
    *state = state
        .wrapping_mul(6364136223846793005)
        .wrapping_add(1442695040888963407);
    *state >> 33
}

#[test]
fn truncations_never_panic() {
    let f = valid_file();
    let two = 2 * PAGE_SIZE;
    for len in 0..f.len() {
        // every byte through the meta region (where bootstrap is most fragile), strided
        // beyond — opening any prefix must never panic.
        if len < two || len % 37 == 0 {
            let _ = Reader::open(&f[..len]);
        }
    }
    let _ = Reader::open(&f);
}

#[test]
fn single_bit_flips_never_panic() {
    let f = valid_file();
    let mut s = 0x9e37_79b9_7f4a_7c15u64;
    for _ in 0..5000 {
        let pos = lcg(&mut s) as usize % f.len();
        let bit = (lcg(&mut s) & 7) as u8;
        let mut g = f.clone();
        g[pos] ^= 1 << bit;
        let _ = Reader::open(&g);
        let _ = Writer::<Ipv4Key>::open_image(g);
    }
}

#[test]
fn arbitrary_buffers_never_panic() {
    let mut s = 0x1234_5678_90ab_cdefu64;
    for &size in &[0usize, 1, 16, 100, 4095, 4096, 4097, 8191, 8192, 8193, 12288, 20000] {
        for _ in 0..40 {
            let mut buf = vec![0u8; size];
            for b in buf.iter_mut() {
                *b = lcg(&mut s) as u8;
            }
            let _ = Reader::open(&buf);
        }
    }
}

#[test]
fn tree_region_flip_never_silently_accepted() {
    // A bit flip in the data region (pages ≥ 2) is either detected (a reachable page's
    // checksum fails ⇒ reject) or ignored (an unreachable/free page ⇒ same data). It is
    // never accepted as a *different* reachable tree. (Meta-region flips are not tested
    // here: tearing the active meta legitimately recovers the previous committed state,
    // §6.3 — covered by the writer's crash-recovery tests.)
    let f = valid_file();
    let r0 = Reader::open(&f).unwrap();
    let mut base = Vec::new();
    r0.scan_v4(|a, b, sc| base.push((a.0, b.0, sc.to_vec()))).unwrap();

    let two = 2 * PAGE_SIZE;
    let mut s = 0xdead_beef_cafe_babeu64;
    for _ in 0..4000 {
        let pos = two + lcg(&mut s) as usize % (f.len() - two);
        let bit = (lcg(&mut s) & 7) as u8;
        let mut g = f.clone();
        g[pos] ^= 1 << bit;
        if let Ok(r) = Reader::open(&g) {
            let mut got = Vec::new();
            r.scan_v4(|a, b, sc| got.push((a.0, b.0, sc.to_vec()))).unwrap();
            assert_eq!(got, base, "accepted a corrupted reachable tree (pos {pos})");
        }
    }
}
