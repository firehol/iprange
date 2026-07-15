//! Rule 1 verification: zero heap allocation in the writer hot path.
//!
//! Uses a counting global allocator gated by a flag. Only allocations during
//! the measured region are counted, excluding test framework overhead.

use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Mutex;

use iprange_livedb::overlap::{all_to_all_overlap, foreign_vs_all_slice};
use iprange_livedb::scope_table::MAX_BITMAP_WIDTH;
use iprange_livedb::spec;
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
// Serialize measurement windows: the global allocator is process-wide, so
// parallel tests would otherwise pollute each other's counted regions.
static MEASURE_LOCK: Mutex<()> = Mutex::new(());

fn measure_lock() -> std::sync::MutexGuard<'static, ()> {
    MEASURE_LOCK
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner)
}

fn begin() {
    ALLOCS.store(0, Ordering::SeqCst);
    COUNTING.store(true, Ordering::SeqCst);
}
fn end() -> usize {
    COUNTING.store(false, Ordering::SeqCst);
    ALLOCS.load(Ordering::SeqCst)
}

fn prime_writer(n: u32) -> Writer<Ipv4Key> {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..n {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    for i in 0..n {
        w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
    }
    for i in 0..n {
        w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
    }
    w.commit(1, u64::MAX).unwrap();
    w
}

#[test]
fn zero_heap_set() {
    let _guard = measure_lock();
    let mut w = prime_writer(1000);
    begin();
    for i in 0..1000u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i + 1).unwrap();
    }
    assert_eq!(end(), 0, "set hot path allocated");
}

#[test]
fn zero_heap_delete() {
    let _guard = measure_lock();
    let mut w = prime_writer(1000);
    begin();
    for i in 0..1000u32 {
        w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
    }
    assert_eq!(end(), 0, "delete hot path allocated");
}

#[test]
fn zero_heap_churn() {
    let _guard = measure_lock();
    let mut w = prime_writer(1000);
    begin();
    for i in 0..1000u32 {
        w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
    }
    for i in 0..1000u32 {
        w.set(Ipv4Key(i), Ipv4Key(i), i + 100).unwrap();
    }
    assert_eq!(end(), 0, "churn hot path allocated");
}

fn overflow_overlap_writer(records: u32) -> Writer<Ipv4Key> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let mut bitmap = vec![0u8; MAX_BITMAP_WIDTH + 1];
    bitmap[0] = 1;
    bitmap[MAX_BITMAP_WIDTH] = 1;
    let id = writer.scope_intern(&bitmap).unwrap();
    for i in 0..records {
        let ip = Ipv4Key(i * 2);
        writer.append(ip, ip, id).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    writer
}

fn count_all_to_all_allocations(records: u32) -> usize {
    let writer = overflow_overlap_writer(records);
    begin();
    all_to_all_overlap(&writer, &mut |_| {}).unwrap();
    end()
}

#[test]
fn overflow_all_to_all_allocations_do_not_scale_with_record_count() {
    let _guard = measure_lock();
    let one = count_all_to_all_allocations(1);
    let many = count_all_to_all_allocations(100);
    assert!(
        many <= one + 4,
        "all-to-all allocations scale with records sharing one overflow scope: one={one} many={many}"
    );
}

fn count_foreign_vs_all_allocations(records: u32) -> usize {
    let writer = overflow_overlap_writer(records);
    let foreign = [(Ipv4Key(0), Ipv4Key((records - 1) * 2))];
    begin();
    foreign_vs_all_slice(&writer, &foreign, &mut |_, _, _| {}).unwrap();
    end()
}

#[test]
fn overflow_foreign_vs_all_allocations_do_not_scale_with_record_count() {
    let _guard = measure_lock();
    let one = count_foreign_vs_all_allocations(1);
    let many = count_foreign_vs_all_allocations(100);
    assert!(
        many <= one + 4,
        "foreign-vs-all allocations scale with records sharing one overflow scope: one={one} many={many}"
    );
}

// append (tree growth) is NOT zero-alloc: when the free-list is exhausted,
// the store extends (Vec resize for VecPageStore, ftruncate for MmapStore).
// This is file growth, not hot-path allocation. The steady-state operations
// (set, delete, churn) are proven zero-alloc above.
