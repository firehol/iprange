//! Issue 8: compact_if_needed must not walk the whole tree every commit, and
//! compaction must still fire when the tree is genuinely sparse.

use iprange_livedb::{Ipv4Key, Reader, Writer};

// Build a dense tree, then delete most records in one txn. Commit must compact
// the now-sparse tree (page count drops sharply) and preserve correctness.
#[test]
fn compact_fires_after_sparse_delete() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    // Dense: 4000 single-IP records (key_width=4 → leaf_max=340 → ~12 leaves).
    for i in 0u32..4000 {
        w.set(Ipv4Key(i), Ipv4Key(i), 1).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    let dense_pages = w.tree_page_count();
    assert!(
        dense_pages >= 12,
        "dense tree should span multiple pages, got {}",
        dense_pages
    );

    // Delete ~90% in one txn → tree becomes sparse (same page count, few records).
    for i in 0u32..3600 {
        w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
    }
    w.commit(2, u64::MAX).unwrap();

    let sparse_pages = w.tree_page_count();
    // Compaction must have rebuilt the tree into far fewer pages.
    assert!(
        sparse_pages * 4 < dense_pages,
        "compaction did not shrink the sparse tree: dense={} sparse={}",
        dense_pages,
        sparse_pages
    );

    // Correctness: surviving records resolve, deleted ones do not.
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    assert!(
        r.lookup(Ipv4Key(3700)).unwrap().is_some(),
        "surviving record missing"
    );
    assert!(
        r.lookup(Ipv4Key(0)).unwrap().is_none(),
        "deleted record still present"
    );
}

// Repeated record-only commits on a dense tree must not trigger compaction
// (the tree is already dense). Guards against a false-positive compaction loop.
#[test]
fn no_compaction_on_dense_appends() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0u32..1000 {
        w.set(Ipv4Key(i), Ipv4Key(i), 1).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    let pages_before = w.tree_page_count();

    // A few small appends + commits — tree stays dense.
    for round in 0u32..5 {
        w.set(Ipv4Key(10_000 + round), Ipv4Key(10_000 + round), 1)
            .unwrap();
        w.commit(2 + round as u64, u64::MAX).unwrap();
    }
    let pages_after = w.tree_page_count();
    // Pages should grow by at most a couple (dense appends), not explode.
    assert!(
        pages_after <= pages_before + 4,
        "dense appends caused unexpected page growth: before={} after={}",
        pages_before,
        pages_after
    );
}
