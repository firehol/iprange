use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};

struct CountingAllocator;

#[global_allocator]
static ALLOCATOR: CountingAllocator = CountingAllocator;

static ACTIVE: AtomicBool = AtomicBool::new(false);
static CALLS: AtomicU64 = AtomicU64::new(0);
static BYTES: AtomicU64 = AtomicU64::new(0);

#[derive(Clone, Copy, Debug, Default)]
pub(crate) struct AllocationStats {
    pub(crate) calls: u64,
    pub(crate) bytes: u64,
}

unsafe impl GlobalAlloc for CountingAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let pointer = unsafe { System.alloc(layout) };
        record(pointer.is_null(), layout.size());
        pointer
    }

    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        let pointer = unsafe { System.alloc_zeroed(layout) };
        record(pointer.is_null(), layout.size());
        pointer
    }

    unsafe fn realloc(&self, pointer: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let replacement = unsafe { System.realloc(pointer, layout, new_size) };
        record(replacement.is_null(), new_size);
        replacement
    }

    unsafe fn dealloc(&self, pointer: *mut u8, layout: Layout) {
        unsafe { System.dealloc(pointer, layout) };
    }
}

fn record(failed: bool, bytes: usize) {
    if !failed && ACTIVE.load(Ordering::Relaxed) {
        CALLS.fetch_add(1, Ordering::Relaxed);
        BYTES.fetch_add(bytes as u64, Ordering::Relaxed);
    }
}

pub(crate) fn measure<T>(operation: impl FnOnce() -> T) -> (T, AllocationStats) {
    CALLS.store(0, Ordering::Relaxed);
    BYTES.store(0, Ordering::Relaxed);
    assert!(!ACTIVE.swap(true, Ordering::SeqCst));
    let result = operation();
    assert!(ACTIVE.swap(false, Ordering::SeqCst));
    (
        result,
        AllocationStats {
            calls: CALLS.load(Ordering::Relaxed),
            bytes: BYTES.load(Ordering::Relaxed),
        },
    )
}
