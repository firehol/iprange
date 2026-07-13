//! Tests mirroring the Go scope_perf / issue3 / issue4 tests for the Rust
//! engine. Guards the issue-1 (open no longer loads the scope table into a
//! heap HashMap) and issue-2 (resolve is O(log S) via find_scope) refactor,
//! plus the issue-3 invariant and the issue-4 streaming foreign_vs_all API.

use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Instant;

use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::{Ipv4Key, Reader, Writer};

// ── Counting allocator (issue-1 memory proof) ──────────────────────────────

struct Counting;
static ALLOCED: AtomicUsize = AtomicUsize::new(0);

unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        ALLOCED.fetch_add(layout.size(), Ordering::Relaxed);
        System.alloc(layout)
    }
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        System.dealloc(ptr, layout)
    }
    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        if new_size > layout.size() {
            ALLOCED.fetch_add(new_size - layout.size(), Ordering::Relaxed);
        }
        System.realloc(ptr, layout, new_size)
    }
}

#[global_allocator]
static A: Counting = Counting;

// ── helpers ────────────────────────────────────────────────────────────────

#[allow(clippy::needless_range_loop)]
fn make_unique_bitmaps(n: usize, width: usize) -> Vec<Vec<u8>> {
    let mut out = Vec::with_capacity(n);
    for i in 0..n {
        let mut bm = vec![0u8; width];
        bm[0] = (i >> 8) as u8;
        bm[1] = i as u8;
        for j in 2..width {
            bm[j] = 0xA0 + ((j % 30) as u8);
        }
        out.push(bm);
    }
    out
}

fn build_mode2_db(n: usize, width: usize) -> (Vec<u8>, Vec<u32>) {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    let bms = make_unique_bitmaps(n, width);
    let mut ids = Vec::with_capacity(n);
    for (i, bm) in bms.iter().enumerate() {
        let id = w.scope_intern(bm).unwrap();
        ids.push(id);
        w.set(Ipv4Key(i as u32), Ipv4Key(i as u32), id).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    let img = w.into_image().unwrap();
    (img, ids)
}

fn open_writer(img: Vec<u8>) -> Writer<Ipv4Key> {
    // Move the image into VecPageStore (no clone) so we measure Writer::open's
    // own allocations, not a test-artifact image copy.
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    Writer::<Ipv4Key>::open(store).unwrap()
}

// ── issue 1: open must NOT materialize the scope table ─────────────────────

#[test]
fn scope_resolve_correct_after_reopen() {
    const N: usize = 2000;
    const WIDTH: usize = 8;
    let (img, ids) = build_mode2_db(N, WIDTH);
    let bms = make_unique_bitmaps(N, WIDTH);
    let w = open_writer(img);
    for i in 0..N {
        let got = w.scope_resolve(ids[i]).expect("scope missing");
        assert_eq!(got, bms[i], "bitmap mismatch at {}", i);
    }
}

#[test]
fn scope_open_alloc_is_constant() {
    // The headline issue-1 proof: opening a mode-2 writer must allocate roughly
    // CONSTANT bytes regardless of scope count, because the table is read on
    // demand. Before the fix the delta grew ~10x (eager HashMap load); after it
    // stays near-flat (only the page bitset grows, which is tiny in absolute
    // terms relative to the constant open overhead).
    const WIDTH: usize = 64;
    let measure = |n: usize| -> usize {
        let (img, _ids) = build_mode2_db(n, WIDTH);
        let before = ALLOCED.load(Ordering::Relaxed);
        let w = open_writer(img);
        let _ = w.scope_page_count();
        let after = ALLOCED.load(Ordering::Relaxed);
        after.saturating_sub(before)
    };
    let small = measure(2000);
    let large = measure(20000);
    eprintln!(
        "open alloc bytes: n=2000 -> {}, n=20000 -> {}",
        small, large
    );
    // Linear load would make large >> small (ratio ~10). Allow wide headroom
    // for the page-bitset which does grow with total pages.
    if small > 0 && large > small * 5 {
        panic!(
            "open allocations grow with scope count: small={} large={} ratio={:.1}",
            small,
            large,
            large as f64 / small as f64
        );
    }
}

// ── issue 2: resolve must be O(log S) ──────────────────────────────────────

#[test]
fn scope_resolve_is_sublinear() {
    const N: usize = 20000;
    const WIDTH: usize = 8;
    let (img, ids) = build_mode2_db(N, WIDTH);
    let w = open_writer(img);
    let first = ids[0];
    let last = ids[N - 1];
    let _ = w.scope_resolve(first); // warm

    let reps = 2000usize;
    let t_first = time_ns(reps, || {
        let _ = w.scope_resolve(first);
    });
    let t_last = time_ns(reps, || {
        let _ = w.scope_resolve(last);
    });
    eprintln!(
        "resolve first(id={})={}ns last(id={})={}ns ratio={:.2}",
        first,
        t_first,
        last,
        t_last,
        t_last as f64 / t_first as f64
    );
    // Linear would make last >> first; find_scope keeps both ~equal.
    if t_first > 0 && t_last as u64 > (t_first as u64) * 4 {
        panic!(
            "scope_resolve appears linear: ratio={:.2}",
            t_last as f64 / t_first as f64
        );
    }
}

fn time_ns(reps: usize, mut f: impl FnMut()) -> u128 {
    f(); // warm-up
    let start = Instant::now();
    for _ in 0..reps {
        f();
    }
    start.elapsed().as_nanos() / reps as u128
}

// ── intern dedup contract (must survive the refactor) ──────────────────────

#[test]
fn scope_intern_dedup_across_reopen() {
    let (img, ids) = build_mode2_db(3, 4);
    let bms = make_unique_bitmaps(3, 4);
    let mut w = open_writer(img);
    for i in 0..3 {
        let got = w.scope_intern(&bms[i]).unwrap();
        assert_eq!(got, ids[i], "intern dedup across reopen failed at {}", i);
    }
    // A brand-new bitmap must mint a new id > max committed.
    let mut fresh = vec![0u8; 4];
    fresh[0] = 0xFF;
    fresh[1] = 0xFF;
    let got = w.scope_intern(&fresh).unwrap();
    assert!(
        got > ids[2],
        "fresh bitmap did not get a new id: {} <= {}",
        got,
        ids[2]
    );
}

#[test]
fn scope_intern_dedup_within_txn_after_reopen() {
    let (img, _ids) = build_mode2_db(5, 4);
    let mut w = open_writer(img);
    let bm = vec![0x7Fu8, 0x7F, 0x7F, 0x7F];
    let id1 = w.scope_intern(&bm).unwrap();
    let id2 = w.scope_intern(&bm).unwrap();
    assert_eq!(id1, id2, "within-txn dedup failed");
}

// ── issue 7: first intern after reopen must NOT allocate O(S) heap ──────────

// The old lazy-index path built a bitmap→scope_id HashMap over the WHOLE
// committed scope table on the first intern that missed the new set. For 20k
// scopes that materialized ~20k Vec<u8> keys in one call. The fix replaces the
// materialization with find_scope_by_bitmap (streaming scan, O(1) heap). This
// test proves the first intern after reopen allocates bytes that stay roughly
// constant as the scope count grows (NOT proportional to S).
#[test]
fn scope_intern_after_reopen_alloc_is_constant() {
    const WIDTH: usize = 8;
    let measure = |n: usize| -> usize {
        let (img, _ids) = build_mode2_db(n, WIDTH);
        let bms = make_unique_bitmaps(n, WIDTH);
        let before = ALLOCED.load(Ordering::Relaxed);
        let mut w = open_writer(img);
        // First intern of an existing committed bitmap: used to trigger the
        // full committed-index load. Now streams the scope tree (O(1) heap).
        let _ = w.scope_intern(&bms[0]).unwrap();
        let _ = w.scope_intern(&bms[n - 1]).unwrap();
        let after = ALLOCED.load(Ordering::Relaxed);
        drop(w);
        after.saturating_sub(before)
    };
    let small = measure(2000);
    let large = measure(20000);
    eprintln!(
        "intern-after-reopen alloc: n=2000 -> {}, n=20000 -> {}",
        small, large
    );
    // A materializing implementation makes large >> small (ratio ~10). The
    // streaming scan keeps it near-constant; allow wide allocator headroom.
    if small > 0 && large > small * 5 {
        panic!(
            "first intern after reopen allocates O(S): small={} large={} ratio={:.1}",
            small,
            large,
            large as f64 / small as f64
        );
    }
}

// ── issue 3: record-only commit must not rebuild the scope table ───────────

#[test]
fn issue3_record_only_commit_does_not_rebuild_scope() {
    let mut w = Writer::<Ipv4Key>::create(2, 0).unwrap();
    let id1 = w.scope_intern(&[0x01]).unwrap();
    let id2 = w.scope_intern(&[0x02]).unwrap();
    w.set(Ipv4Key(10), Ipv4Key(20), id1).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), id2).unwrap();
    w.commit(1, u64::MAX).unwrap();
    let pages_before = w.scope_page_count();
    assert!(pages_before > 0, "expected a non-empty scope table");

    // Record-only mutations: no scope_intern / no feed-bit ops.
    w.delete(Ipv4Key(10), Ipv4Key(20)).unwrap();
    w.set(Ipv4Key(50), Ipv4Key(60), id2).unwrap();
    w.commit(2, u64::MAX).unwrap();
    // No rebuild ⇒ identical scope page count.
    assert_eq!(
        w.scope_page_count(),
        pages_before,
        "record-only commit rebuilt the scope table (page count changed)"
    );
    // Reader still sees the right data + scopes resolve correctly.
    let img = w.into_image().unwrap();
    let r = Reader::open(&img).unwrap();
    let sid = r.lookup(Ipv4Key(50)).unwrap().unwrap();
    assert_eq!(sid, id2);
}

// ── issue 4: foreign_vs_all streaming closure + slice wrapper ──────────────

#[test]
fn issue4_foreign_vs_all_closure_and_slice() {
    use iprange_livedb::overlap::{foreign_vs_all, foreign_vs_all_slice, FeedOverlap};

    let mut w = Writer::<Ipv4Key>::create(1, 0).unwrap(); // bitmap mode
    w.set(Ipv4Key(10), Ipv4Key(20), 0b001).unwrap();
    w.set(Ipv4Key(30), Ipv4Key(40), 0b110).unwrap();
    w.commit(1, u64::MAX).unwrap();

    // Foreign feed [15-35] overlaps both stored ranges.
    let foreign: Vec<(Ipv4Key, Ipv4Key)> = vec![(Ipv4Key(15), Ipv4Key(35))];

    // Closure form.
    let mut idx = 0usize;
    let mut next_foreign = || {
        if idx < foreign.len() {
            let r = foreign[idx];
            idx += 1;
            Some(r)
        } else {
            None
        }
    };
    let mut got_closure: Vec<FeedOverlap> = Vec::new();
    foreign_vs_all(&w, &mut next_foreign, &mut |feed, fid, cnt| {
        got_closure.push(FeedOverlap {
            feed_a: feed,
            feed_b: fid,
            ip_count: cnt,
        });
    })
    .unwrap();

    // Slice form.
    let mut got_slice: Vec<FeedOverlap> = Vec::new();
    foreign_vs_all_slice(&w, &foreign, &mut |feed, fid, cnt| {
        got_slice.push(FeedOverlap {
            feed_a: feed,
            feed_b: fid,
            ip_count: cnt,
        });
    })
    .unwrap();

    let normalize = |mut v: Vec<FeedOverlap>| -> Vec<FeedOverlap> {
        v.sort_by_key(|o| o.feed_a);
        v
    };
    let got_closure = normalize(got_closure);
    let got_slice = normalize(got_slice);

    let want = vec![
        FeedOverlap {
            feed_a: 0,
            feed_b: 0,
            ip_count: 6,
        },
        FeedOverlap {
            feed_a: 1,
            feed_b: 0,
            ip_count: 6,
        },
        FeedOverlap {
            feed_a: 2,
            feed_b: 0,
            ip_count: 6,
        },
    ];
    assert_eq!(got_closure, want, "closure form mismatch");
    assert_eq!(got_slice, want, "slice form mismatch");
}
