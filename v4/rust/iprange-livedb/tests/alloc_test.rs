//! Rule 1 verification: zero heap allocation in the writer hot path.
//!
//! Uses a counting global allocator gated by a flag. Only allocations during
//! the measured region are counted, excluding test framework overhead.

use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

use iprange_livedb::{Ipv4Key, Writer};

struct CountingAlloc;

unsafe impl GlobalAlloc for CountingAlloc {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        if COUNTING.load(Ordering::SeqCst) {
            ALLOCS.fetch_add(1, Ordering::SeqCst);
        }
        System.alloc(layout)
    }
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        System.dealloc(ptr, layout);
    }
    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        if COUNTING.load(Ordering::SeqCst) {
            ALLOCS.fetch_add(1, Ordering::SeqCst);
        }
        System.alloc_zeroed(layout)
    }
    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        if COUNTING.load(Ordering::SeqCst) {
            ALLOCS.fetch_add(1, Ordering::SeqCst);
        }
        System.realloc(ptr, layout, new_size)
    }
}

#[global_allocator]
static ALLOCATOR: CountingAlloc = CountingAlloc;

static ALLOCS: AtomicUsize = AtomicUsize::new(0);
static COUNTING: AtomicBool = AtomicBool::new(false);

fn begin() { ALLOCS.store(0, Ordering::SeqCst); COUNTING.store(true, Ordering::SeqCst); }
fn end() -> usize { COUNTING.store(false, Ordering::SeqCst); ALLOCS.load(Ordering::SeqCst) }

fn prime_writer(n: u32) -> Writer<Ipv4Key> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..n { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
    w.commit(0, u64::MAX).unwrap();
    for i in 0..n { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
    for i in 0..n { w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap(); }
    w.commit(1, u64::MAX).unwrap();
    w
}

#[test]
fn zero_heap_set() {
    let mut w = prime_writer(1000);
    begin();
    for i in 0..1000u32 { w.set(Ipv4Key(i), Ipv4Key(i), i + 1).unwrap(); }
    assert_eq!(end(), 0, "set hot path allocated");
}

#[test]
fn zero_heap_delete() {
    let mut w = prime_writer(1000);
    begin();
    for i in 0..1000u32 { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
    assert_eq!(end(), 0, "delete hot path allocated");
}

#[test]
fn zero_heap_churn() {
    let mut w = prime_writer(1000);
    begin();
    for i in 0..1000u32 { w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap(); }
    for i in 0..1000u32 { w.set(Ipv4Key(i), Ipv4Key(i), i + 100).unwrap(); }
    assert_eq!(end(), 0, "churn hot path allocated");
}

// append (tree growth) is NOT zero-alloc: when the free-list is exhausted,
// the store extends (Vec resize for VecPageStore, ftruncate for MmapStore).
// This is file growth, not hot-path allocation. The steady-state operations
// (set, delete, churn) are proven zero-alloc above.
