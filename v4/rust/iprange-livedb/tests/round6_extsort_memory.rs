use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

use iprange_livedb::extsort::{ExtSortConfig, ExtSorter};
use iprange_livedb::Ipv4Key;

struct CountingAlloc;

unsafe impl GlobalAlloc for CountingAlloc {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        if COUNTING.load(Ordering::Relaxed) {
            ALLOCATED_BYTES.fetch_add(layout.size(), Ordering::Relaxed);
        }
        System.alloc(layout)
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        System.dealloc(ptr, layout);
    }

    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        if COUNTING.load(Ordering::Relaxed) {
            ALLOCATED_BYTES.fetch_add(layout.size(), Ordering::Relaxed);
        }
        System.alloc_zeroed(layout)
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        if COUNTING.load(Ordering::Relaxed) {
            ALLOCATED_BYTES.fetch_add(new_size, Ordering::Relaxed);
        }
        System.realloc(ptr, layout, new_size)
    }
}

#[global_allocator]
static ALLOCATOR: CountingAlloc = CountingAlloc;
static COUNTING: AtomicBool = AtomicBool::new(false);
static ALLOCATED_BYTES: AtomicUsize = AtomicUsize::new(0);

fn finish_allocated_bytes(records: usize, tag: &str) -> usize {
    let dir = std::env::temp_dir().join(format!(
        "iprange-round6-extsort-memory-{tag}-{}",
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: records,
        temp_dir: Some(dir.clone()),
    });
    for i in 0..records as u32 {
        let ip = Ipv4Key(i * 2);
        sorter.add(ip, ip, 1).unwrap();
    }

    ALLOCATED_BYTES.store(0, Ordering::Relaxed);
    COUNTING.store(true, Ordering::Relaxed);
    let stream = sorter.finish().unwrap();
    COUNTING.store(false, Ordering::Relaxed);
    let allocated = ALLOCATED_BYTES.load(Ordering::Relaxed);
    drop(stream);
    let _ = std::fs::remove_dir_all(dir);
    allocated
}

#[test]
fn external_sorter_finish_heap_does_not_scale_with_single_run_size() {
    let small = finish_allocated_bytes(100, "small");
    let large = finish_allocated_bytes(100_000, "large");
    const TOLERANCE: usize = 128 << 10;
    assert!(
        large <= small + TOLERANCE,
        "external sorter finish materialized its single spill run: small={small} large={large}"
    );
}
