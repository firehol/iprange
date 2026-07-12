//! Regression tests for audit findings.
//! Each test proves a specific bug exists (FAILS) until the bug is fixed.

use iprange_livedb::{Ipv4Key, Writer, Reader};
use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::spec::PAGE_SIZE;

// ── F1: Free-list self-referential corruption ──────────────────────────────

/// Detect free-list chain pages that list themselves as a free entry.
fn self_referential_chain_pages(data: &[u8]) -> Vec<u32> {
    let mut bad = Vec::new();
    for p in 0..data.len() / PAGE_SIZE {
        let base = p * PAGE_SIZE;
        if data.len() < base + 36 { break; }
        // PH_PAGE_TYPE is at offset 0 (1 byte)
        let page_type = data[base];
        const PAGE_TYPE_TXN_FREE: u8 = 9;
        if page_type != PAGE_TYPE_TXN_FREE { continue; }
        // PH_PGNO at offset 4 (4 bytes LE)
        let self_pgno = u32::from_le_bytes(
            data[base + 4..base + 8].try_into().unwrap()
        );
        // TXN_FREE_COUNT at offset 20 (4 bytes LE)
        let count = u32::from_le_bytes(
            data[base + 20..base + 24].try_into().unwrap()
        ).min(1016) as usize;
        // TXN_FREE_ARRAY at offset 32
        let array_off = base + 32;
        for i in 0..count {
            let off = array_off + i * 4;
            if off + 4 > data.len() { break; }
            let entry = u32::from_le_bytes(data[off..off + 4].try_into().unwrap());
            if entry == self_pgno {
                bad.push(self_pgno);
                break;
            }
        }
    }
    bad
}

#[test]
fn f1_no_self_referential_free_list() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..5000u32 { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
    w.commit(1, u64::MAX).unwrap();
    for i in 0..3000u32 { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
    w.commit(2, u64::MAX).unwrap();
    // No-op commit: must not produce a self-referential free-list entry.
    w.commit(3, u64::MAX).unwrap();

    let img = w.into_image().unwrap();
    let bad = self_referential_chain_pages(&img);
    assert!(bad.is_empty(), "self-referential free-list pages: {:?}", bad);
}

// ── F2: Mode-2 scope catastrophic growth ───────────────────────────────────

#[test]
fn f2_mode2_scope_stabilizes() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    let mut pages = Vec::new();
    for i in 1..=10u8 {
        w.scope_intern(&[i]).unwrap();
        w.commit(i as u64, u64::MAX).unwrap();
        pages.push(w.committed_pages());
    }
    eprintln!("pages trajectory: {:?}", pages);
    let growth = pages.last().unwrap() - pages[3];
    assert!(growth <= 3, "mode-2 file kept growing: pages={:?}", pages);
}

// ── F3: Mode-2 feed update persists scope (Rust only — Go has the bug) ─────

#[test]
fn f3_mode2_feed_update_persists() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    let id = w.scope_intern(&[1]).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), id).unwrap();
    w.commit(1, u64::MAX).unwrap();
    w.feed_add_range(Ipv4Key(10), Ipv4Key(20), 3).unwrap();
    w.commit(2, u64::MAX).unwrap();

    let img = w.into_image().unwrap();
    let store = VecPageStore::new(img);
    let r = Reader::open(store.committed_bytes()).unwrap();
    let scope_id = r.lookup(Ipv4Key(15)).expect("lookup error")
        .expect("record disappeared after feed update");
    assert!(scope_id > 0, "scope_id should be non-zero");
}

// ── F7: Delete-all shrinks tree ────────────────────────────────────────────

#[test]
fn f7_delete_all_shrinks_tree() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..10_000u32 { w.append(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
    w.commit(1, u64::MAX).unwrap();

    w.delete(Ipv4Key(0), Ipv4Key(9_999)).unwrap();
    w.commit(2, u64::MAX).unwrap();

    // The tree structure must collapse (root=0, height=0).
    assert_eq!(w.record_count(), 0);
    // Verify the committed image has 0 records.
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 0, "empty tree should have 0 records");

    // After reopening, a new insert should work from a fresh root.
    let store = VecPageStore::new(img);
    let mut w2 = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
    w2.set(Ipv4Key(42), Ipv4Key(42), 99).unwrap();
    w2.commit(3, u64::MAX).unwrap();
    let img2 = w2.into_image().unwrap();
    let r2 = Reader::open(&img2).unwrap();
    assert_eq!(r2.record_count(), 1);
    assert_eq!(r2.lookup(Ipv4Key(42)).unwrap(), Some(99));
}

// ── F8: ExtSort input last-wins ────────────────────────────────────────────

#[test]
fn f8_extsort_last_wins() {
    use iprange_livedb::extsort::{ExtSorter, ExtSortConfig};

    let config = ExtSortConfig { chunk_size: 1000, temp_dir: None };
    let mut sorter = ExtSorter::<Ipv4Key>::new(config);
    // Input order: [10,20] scope 1, then [0,30] scope 2.
    // The later [0,30] should win over [10,20] for the overlapping range.
    sorter.add(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    sorter.add(Ipv4Key(0), Ipv4Key(30), 2).unwrap();
    let mut stream = sorter.finish().unwrap();

    // After normalization, every record in [0,30] should have scope 2.
    while let Some(rec) = stream.next() {
        eprintln!("record: from={:?} to={:?} scope={}",
            rec.from, rec.to, rec.scope_id);
        assert_eq!(rec.scope_id, 2,
            "extsort lost input last-wins: got scope {} expected 2", rec.scope_id);
    }
}

// ── F9: Free-list pages have valid CRCs ────────────────────────────────────

#[test]
fn f9_free_list_pages_have_crc() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..1000u32 { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
    w.commit(1, u64::MAX).unwrap();
    for i in 0..1000u32 { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
    w.commit(2, u64::MAX).unwrap();

    let img = w.into_image().unwrap();
    let n_pages = img.len() / PAGE_SIZE;
    for p in 2..n_pages {
        let base = p * PAGE_SIZE;
        let page_type = img[base]; // PH_PAGE_TYPE at offset 0
        if page_type != 9 { continue; } // PAGE_TYPE_TXN_FREE
        let page = &img[base..base + PAGE_SIZE];
        assert!(iprange_livedb::crc32c::verify_page(page),
            "free-list page {} has invalid CRC", p);
    }
}
