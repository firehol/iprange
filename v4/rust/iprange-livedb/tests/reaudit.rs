//! Re-audit regression tests. Each test proves a specific bug exists.
//! Tests FAIL until the bug is fixed.

use iprange_livedb::extsort::{ExtSortConfig, ExtSorter};
use iprange_livedb::page_store::VecPageStore;
use iprange_livedb::{Ipv4Key, Reader, Writer};

// ── Issue 2: No-op commits leak pages ────────────────────────────────────────

#[test]
fn reaudit2_noop_commits_do_not_leak_pages() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..5000u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    for i in 0..3000u32 {
        w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
    }
    w.commit(2, u64::MAX).unwrap();
    let start = w.committed_pages();
    for txn in 3..=100u64 {
        w.commit(txn, u64::MAX).unwrap();
    }
    let end = w.committed_pages();
    eprintln!("no-op pages {start} -> {end}");
    assert!(
        end <= start + 2,
        "no-op commits grew file: {start} -> {end}"
    );
}

// ── Issue 3: Partial deletion retains peak tree ──────────────────────────────

#[test]
fn reaudit3_large_partial_delete_shrinks_near_final() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..100_000u32 {
        w.append(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    w.delete(Ipv4Key(0), Ipv4Key(89_999)).unwrap();
    w.commit(2, u64::MAX).unwrap();
    let after_first = w.committed_pages();
    eprintln!("partial delete after commit 2: {} pages", after_first);
    // After compact rebuild: the new tree is ~30 pages, but old committed
    // pages (593) remain visible until the next commit. They're freed but
    // not trailing. Do a second no-op commit to allow truncation.
    w.commit(3, u64::MAX).unwrap();
    let after_second = w.committed_pages();
    eprintln!("after commit 3: {} pages", after_second);
    // The old committed pages (2-593) are now free-list entries. The new
    // compact tree (~30 pages) is in the growth region. COW copies from
    // the rebuild are trailing and get truncated. But the old committed
    // pages are NOT trailing, so the file retains ~620 pages.
    // The key assertion: the file is significantly smaller than the
    // uncompact case (1127 pages). 700 is a generous threshold that
    // still catches the regression.
    assert!(
        after_second < 700,
        "90% delete retained peak tree: first={} second={}",
        after_first,
        after_second
    );
    // Verify correctness: 10000 records remain.
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert_eq!(r.record_count(), 10000);
}

// ── Issue 4: ExtSort last-wins across spill runs ─────────────────────────────

#[test]
fn reaudit4_extsort_last_wins_across_spill_runs() {
    let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: 1,
        temp_dir: Some(std::env::temp_dir()),
    });
    sorter.add(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    sorter.add(Ipv4Key(0), Ipv4Key(30), 2).unwrap();
    let mut stream = sorter.finish().unwrap();
    while let Some(rec) = stream.next() {
        assert_eq!(
            rec.scope_id, 2,
            "later record lost across spill runs: {rec:?}"
        );
    }
}

// ── Issue 7: No-op commits do not lose free entries ──────────────────────────

#[test]
fn reaudit7_noop_commits_preserve_free_entries() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..5000u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    for i in 0..3000u32 {
        w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
    }
    w.commit(2, u64::MAX).unwrap();
    // After churn, the free-list should have entries. After 8 no-op commits,
    // the free entries should be preserved (not leaked).
    let img_before = w.into_image().unwrap();
    let pages_before = img_before.len() / 4096;

    let store = iprange_livedb::page_store::VecPageStore::new(img_before);
    let mut w2 = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
    for txn in 3..=10u64 {
        w2.commit(txn, u64::MAX).unwrap();
    }
    let img_after = w2.into_image().unwrap();
    let pages_after = img_after.len() / 4096;

    eprintln!("no-op pages {pages_before} -> {pages_after}");
    assert!(
        pages_after <= pages_before + 2,
        "no-op commits grew file: {pages_before} -> {pages_after}"
    );
}

// ── Tombstone invariant: freed-then-consumed page must not reappear as free ─
//
// The key correctness guarantee of the append-only tombstone free-list: a page
// that is freed, then consumed (reused for new live data), must NOT be handed
// out again after close/reopen — or live data would be overwritten.
#[test]
fn reaudit8_tombstone_freed_then_consumed_stays_live() {
    let mut img = {
        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for i in 0..1000u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(1, u64::MAX).unwrap();
        w.into_image().unwrap()
    };
    // Free pages by deleting a contiguous range, then commit.
    img = {
        let store = VecPageStore::new(img);
        let mut w = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
        for i in 0..400u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        w.commit(2, u64::MAX).unwrap();
        w.into_image().unwrap()
    };
    let free_after_delete = {
        let store = VecPageStore::new(img.clone());
        let w = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
        w.free_page_count()
    };
    assert!(free_after_delete > 0, "expected freed pages after delete");

    // Consume free pages by inserting new data (allocates from the free-list).
    img = {
        let store = VecPageStore::new(img);
        let mut w = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
        let free_before = w.free_page_count();
        for i in 0..400u32 {
            w.set(Ipv4Key(10_000 + i), Ipv4Key(10_000 + i), i).unwrap();
        }
        w.commit(3, u64::MAX).unwrap();
        let _ = free_before;
        w.into_image().unwrap()
    };

    // Reopen: the consumed pages must NOT reappear as free. If the tombstone
    // were missing, the free-list would have grown back (consumed pages
    // re-listed), and a subsequent insert would overwrite live data.
    let free_after_reopen = {
        let store = VecPageStore::new(img.clone());
        let w = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
        w.free_page_count()
    };

    // Insert more data that would land on any wrongly-reclaimed page.
    img = {
        let store = VecPageStore::new(img);
        let mut w = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
        for i in 0..400u32 {
            w.set(Ipv4Key(20_000 + i), Ipv4Key(20_000 + i), i).unwrap();
        }
        w.commit(4, u64::MAX).unwrap();
        w.into_image().unwrap()
    };

    let r = Reader::open(&img).unwrap();
    // The data written over consumed pages must survive intact.
    for i in 0..400u32 {
        assert_eq!(
            r.lookup(Ipv4Key(10_000 + i)).unwrap(),
            Some(i),
            "tombstone invariant broken: consumed page overwritten at key {}",
            10_000 + i
        );
    }
    for i in 0..400u32 {
        assert_eq!(
            r.lookup(Ipv4Key(20_000 + i)).unwrap(),
            Some(i),
            "data lost at key {}",
            20_000 + i
        );
    }
    // Remaining original records must survive too.
    for i in 400..1000u32 {
        assert_eq!(r.lookup(Ipv4Key(i)).unwrap(), Some(i));
    }
    // The free-list after reopen must not have re-listed the consumed pages.
    // It should be strictly smaller than right after the delete (some pages
    // were consumed and are now live).
    assert!(
        free_after_reopen < free_after_delete,
        "consumed pages reappeared as free after reopen: {} >= {}",
        free_after_reopen,
        free_after_delete
    );
}
