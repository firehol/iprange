use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;

struct ThreadCountingAllocator;

#[global_allocator]
static TEST_ALLOCATOR: ThreadCountingAllocator = ThreadCountingAllocator;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct AllocationStats {
    pub(crate) count: usize,
    pub(crate) bytes: usize,
}

std::thread_local! {
    static THREAD_ALLOCATIONS: Cell<Option<AllocationStats>> = const { Cell::new(None) };
}

unsafe impl GlobalAlloc for ThreadCountingAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        record(layout.size());
        unsafe { System.alloc(layout) }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) }
    }

    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        record(layout.size());
        unsafe { System.alloc_zeroed(layout) }
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        record(new_size);
        unsafe { System.realloc(ptr, layout, new_size) }
    }
}

fn record(bytes: usize) {
    let _ = THREAD_ALLOCATIONS.try_with(|counter| {
        if let Some(mut current) = counter.get() {
            current.count += 1;
            current.bytes += bytes;
            counter.set(Some(current));
        }
    });
}

pub(crate) fn count_thread_allocations<T>(operation: impl FnOnce() -> T) -> (T, usize) {
    let (result, statistics) = measure_thread_allocations(operation);
    (result, statistics.count)
}

pub(crate) fn measure_thread_allocations<T>(operation: impl FnOnce() -> T) -> (T, AllocationStats) {
    THREAD_ALLOCATIONS.with(|counter| {
        assert_eq!(counter.replace(Some(AllocationStats::default())), None);
    });
    let result = operation();
    let statistics = THREAD_ALLOCATIONS.with(|counter| counter.replace(None).unwrap());
    (result, statistics)
}
