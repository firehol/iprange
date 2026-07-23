use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;

struct ThreadCountingAllocator;

#[global_allocator]
static TEST_ALLOCATOR: ThreadCountingAllocator = ThreadCountingAllocator;

std::thread_local! {
    static THREAD_ALLOCATION_COUNT: Cell<Option<usize>> = const { Cell::new(None) };
}

unsafe impl GlobalAlloc for ThreadCountingAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let _ = THREAD_ALLOCATION_COUNT.try_with(|count| {
            if let Some(current) = count.get() {
                count.set(Some(current + 1));
            }
        });
        unsafe { System.alloc(layout) }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) }
    }
}

pub(crate) fn count_thread_allocations<T>(operation: impl FnOnce() -> T) -> (T, usize) {
    THREAD_ALLOCATION_COUNT.with(|count| {
        assert_eq!(count.replace(Some(0)), None);
    });
    let result = operation();
    let allocations = THREAD_ALLOCATION_COUNT.with(|count| count.replace(None).unwrap());
    (result, allocations)
}
