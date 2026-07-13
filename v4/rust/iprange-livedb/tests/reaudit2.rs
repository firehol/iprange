//! Re-audit 2: comprehensive tests for the remaining v4 issues.
//!
//! Each test targets one specific concern from the audit:
//! - **C1** — MVCC atomicity: a file-backed reader pins its transaction snapshot
//!   across two subsequent writer commits (the writer must not recycle pages the
//!   pinned reader still needs).
//! - **C2** — ExtSort randomized last-wins: 30 overlapping random ranges sorted
//!   with `chunk_size = 3` (≈10 spill runs) must assign every IP the scope of the
//!   LAST input record covering it.
//! - **C3** — CRC validation on open: `Writer::open` must reject a file whose two
//!   meta pages both fail CRC (torn write / corruption).
//! - **C4** — bitmap scope width cap: `scope_intern` must reject a bitmap wider
//!   than `MAX_BITMAP_WIDTH` (256), otherwise the fixed-size scope entry silently
//!   truncates it on encode.
//! - **A1** — free-list / chain growth: 100 real churn cycles (delete + reinsert +
//!   commit each) must not grow the file unboundedly.

use iprange_livedb::page_store::VecPageStore;
use iprange_livedb::{ext_sort, DesiredRecord, ExtSortConfig, Ipv4Key, Reader, Writer};

// ── deterministic xorshift32 PRNG (no external deps, fully reproducible) ──────
struct Rng(u32);
impl Rng {
    fn next(&mut self) -> u32 {
        let mut x = self.0;
        x ^= x << 13;
        x ^= x >> 17;
        x ^= x << 5;
        self.0 = x;
        x
    }
}

// ── C1: MVCC atomicity — reader pins snapshot across writer commits ──────────

#[cfg(feature = "os")]
#[test]
fn reaudit2_c1_mvcc_reader_pins_snapshot_across_commits() {
    use iprange_livedb::os::{FileWriter, MmapReader};
    let path =
        std::env::temp_dir().join(format!("iprange_reaudit2_c1_{}.iprdb", std::process::id()));
    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));

    // txn 1: insert 1000 records; key 500 carries scope 11.
    {
        let mut w = FileWriter::<Ipv4Key>::create(&path, 0, 0).unwrap();
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), 11).unwrap();
        }
        w.commit(1).unwrap();
        w.close();
    }

    // Open a reader AFTER txn 1 — it pins the txn-1 snapshot.
    let rdr = MmapReader::open(&path).unwrap();
    {
        let r = rdr.reader().unwrap();
        assert_eq!(
            r.lookup(Ipv4Key(500)).unwrap(),
            Some(11),
            "txn1 initial scope"
        );
    }

    // txn 2: overwrite key 500 -> scope 22.
    {
        let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();
        w.set(Ipv4Key(500), Ipv4Key(500), 22).unwrap();
        w.commit(2).unwrap();
        w.close();
    }

    // txn 3: full churn — delete everything, reinsert with a different scope.
    {
        let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();
        for i in 0..1000u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), 33).unwrap();
        }
        w.commit(3).unwrap();
        w.close();
    }

    // The reader pinned at txn 1 MUST still observe scope 11 for key 500. If the
    // writer recycled a page the reader still descends through, the mmap would
    // reflect the newer transaction and this assertion would fail.
    {
        let r = rdr.reader().unwrap();
        assert_eq!(
            r.lookup(Ipv4Key(500)).unwrap(),
            Some(11),
            "MVCC violation: reader observed a post-txn1 value (saw {:?}, want 11)",
            r.lookup(Ipv4Key(500)).unwrap()
        );
        // Sample a few more keys to be thorough.
        for i in [0u32, 1, 250, 999] {
            assert_eq!(r.lookup(Ipv4Key(i)).unwrap(), Some(11), "MVCC at key {i}");
        }
    }

    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
}

// ── C2: ExtSort randomized last-wins across many spill runs ──────────────────

#[test]
fn reaudit2_c2_extsort_randomized_last_wins() {
    // 30 random overlapping ranges over [0, 200) with scopes in [1, 10].
    let mut rng = Rng(0xDEAD_BEEF);
    let mut input: Vec<DesiredRecord<Ipv4Key>> = Vec::with_capacity(30);
    for _ in 0..30 {
        let a = rng.next() % 200;
        let b = rng.next() % 200;
        let (from, to) = if a <= b { (a, b) } else { (b, a) };
        let scope = (rng.next() % 10) + 1;
        input.push(DesiredRecord {
            from: Ipv4Key(from),
            to: Ipv4Key(to),
            scope_id: scope,
        });
    }

    // Brute-force reference: expected[ip] = scope of the LAST input record
    // covering ip (None if no record covers it).
    const SPAN: u32 = 200;
    let mut expected: Vec<Option<u32>> = vec![None; (SPAN + 1) as usize];
    for rec in &input {
        for ip in rec.from.0..=rec.to.0 {
            expected[ip as usize] = Some(rec.scope_id);
        }
    }

    // chunk_size = 3 => ~10 spill runs, exercising cross-run last-wins merge.
    let config = ExtSortConfig {
        chunk_size: 3,
        temp_dir: Some(std::env::temp_dir()),
    };

    // (a) scope correctness: every emitted segment must match the reference.
    let mut stream = ext_sort(input.clone(), &config).unwrap();
    let mut mismatches = 0u32;
    while let Some(seg) = stream.next() {
        for ip in seg.from.0..=seg.to.0 {
            let want = expected[ip as usize];
            if want != Some(seg.scope_id) {
                mismatches += 1;
                if mismatches <= 5 {
                    eprintln!(
                        "C2 scope mismatch at ip={ip}: ext_sort={}, expected={:?}",
                        seg.scope_id, want
                    );
                }
            }
        }
    }
    assert_eq!(
        mismatches, 0,
        "ext_sort last-wins violated at {mismatches} IPs"
    );

    // (b) coverage correctness: ext_sort's coverage must equal the reference.
    let mut stream2 = ext_sort(input, &config).unwrap();
    let mut covered = vec![false; (SPAN + 1) as usize];
    while let Some(seg) = stream2.next() {
        for ip in seg.from.0..=seg.to.0 {
            covered[ip as usize] = true;
        }
    }
    for ip in 0..=SPAN as usize {
        assert_eq!(
            covered[ip],
            expected[ip].is_some(),
            "coverage mismatch at ip={ip}: covered={} but expected_some={}",
            covered[ip],
            expected[ip].is_some()
        );
    }
}

// ── C3: Writer::open must reject a file whose meta pages fail CRC ────────────
//
// The core in-memory `Writer::open` decodes both meta pages by `txn_id` without
// verifying their per-page CRC32C. A torn write or byte corruption that leaves
// the bytes decodable (but CRC-invalid) is silently accepted. The file-backed
// openers (`os::FileWriter`, `os::MmapReader`) DO check CRC; the gap is the core
// open path used by every non-file consumer (and by reopen-over-image tests).
const CRC_CORRUPT_OFFSET: usize = 42; // META_CREATED_UNIXTIME — not validated by open

#[test]
fn reaudit2_c3_writer_open_rejects_corrupt_meta() {
    // Build a valid, CRC-sealed image.
    let img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        w.set(Ipv4Key(1), Ipv4Key(10), 7).unwrap();
        w.commit(0, u64::MAX).unwrap();
        w.into_image().unwrap()
    };
    assert!(img.len() >= 2 * 4096, "image must hold two meta pages");

    // Sanity: the clean image verifies.
    assert!(
        iprange_livedb::crc32c::verify_page(&img[..4096]),
        "clean meta page 0 must verify"
    );

    // Corrupt a body byte (NOT the checksum field [8..16)) on BOTH meta pages.
    let mut bad = img.clone();
    bad[CRC_CORRUPT_OFFSET] ^= 0xFF;
    bad[4096 + CRC_CORRUPT_OFFSET] ^= 0xFF;

    // Both metas must now fail CRC...
    assert!(
        !iprange_livedb::crc32c::verify_page(&bad[..4096]),
        "corrupted meta page 0 must fail CRC"
    );
    assert!(
        !iprange_livedb::crc32c::verify_page(&bad[4096..8192]),
        "corrupted meta page 1 must fail CRC"
    );

    // ...so Writer::open MUST reject the file.
    let store = VecPageStore::new(bad);
    let result = Writer::<Ipv4Key>::open(Box::new(store));
    assert!(
        result.is_err(),
        "Writer::open accepted a CRC-corrupt file (missing CRC validation on open)"
    );
}

// ── C4: scope_intern must reject a bitmap wider than MAX_BITMAP_WIDTH (256) ───
//
// The scope-table leaf entry is a fixed-size record (SCOPE_ENTRY_SIZE = 4 + 2 +
// 256 = 262 bytes); `encode_entry` silently truncates the bitmap at
// MAX_BITMAP_WIDTH. Interning a 257-byte bitmap therefore loses the trailing
// byte on disk — a silent data-corruption bug. `scope_intern` must reject it up
// front.
#[test]
fn reaudit2_c4_scope_intern_rejects_oversized_bitmap() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap(); // scope_mode = indirect
    let oversized = vec![0u8; 257];
    let result = w.scope_intern(&oversized);
    assert!(
        result.is_err(),
        "scope_intern accepted a 257-byte bitmap (MAX_BITMAP_WIDTH is 256) — silent truncation on encode"
    );

    // A 256-byte bitmap is the maximum legal width and must be accepted.
    let legal = vec![0u8; 256];
    let id = w.scope_intern(&legal);
    assert!(
        id.is_ok(),
        "256-byte bitmap (the cap) should be accepted: {:?}",
        id
    );
}

// ── A1: 100 churn cycles must not grow the file unboundedly ──────────────────
//
// The append-only tombstone free-list records every freed page as a chain entry.
// 100 real churn cycles (each deleting and re-inserting the live set) append
// entries every commit; if the chain is not compacted, the page count grows
// without bound. This is distinct from the no-op-commit leak (reaudit2_noop),
// which only commits without data changes.
#[test]
fn reaudit2_a1_churn_cycles_do_not_grow_unbounded() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    // Small live set (~one leaf) so the tree itself stays tiny and any growth is
    // dominated by the free-list chain.
    for i in 0..50u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    let start = w.committed_pages();

    for cycle in 2..=101u64 {
        for i in 0..50u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..50u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), cycle as u32).unwrap();
        }
        w.commit(cycle, u64::MAX).unwrap();
    }
    let end = w.committed_pages();
    eprintln!("A1 churn: start={start} pages, end={end} pages after 100 cycles");

    // The live tree is a handful of pages; 100 churn cycles must not balloon the
    // file. 50 pages is a generous ceiling that still catches unbounded chain
    // growth (which would reach hundreds).
    assert!(
        end <= 50,
        "100 churn cycles grew the file to {end} pages (start {start}) — free-list chain not compacting"
    );

    // Correctness: the final state must reflect the last cycle (scope = 101).
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    for i in 0..50u32 {
        assert_eq!(
            r.lookup(Ipv4Key(i)).unwrap(),
            Some(101),
            "data corrupted at key {i}"
        );
    }
}
