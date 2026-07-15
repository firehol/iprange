use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

use iprange_livedb::overlap::foreign_vs_all;
use iprange_livedb::spec;
use iprange_livedb::{Ipv4Key, Writer};

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

fn bitmap_overlap_writer(records: u32) -> Writer<Ipv4Key> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_BITMAP, 0).unwrap();
    for i in 0..records {
        let ip = Ipv4Key(i * 2);
        writer.append(ip, ip, 3).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    writer
}

fn foreign_overlap_allocated_bytes(records: u32) -> usize {
    let writer = bitmap_overlap_writer(records);
    let mut yielded = false;
    let mut callbacks = 0u32;
    ALLOCATED_BYTES.store(0, Ordering::Relaxed);
    COUNTING.store(true, Ordering::Relaxed);
    foreign_vs_all(
        &writer,
        || {
            if yielded {
                None
            } else {
                yielded = true;
                Some((Ipv4Key(0), Ipv4Key((records - 1) * 2)))
            }
        },
        &mut |_, _, _| callbacks += 1,
    )
    .unwrap();
    COUNTING.store(false, Ordering::Relaxed);
    assert_eq!(callbacks, records * 2);
    ALLOCATED_BYTES.load(Ordering::Relaxed)
}

#[test]
fn foreign_vs_all_heap_does_not_scale_with_stored_leaf_count() {
    let small = foreign_overlap_allocated_bytes(1_000);
    let large = foreign_overlap_allocated_bytes(500_000);
    const TOLERANCE: usize = 4 << 10;
    assert!(
        large <= small + TOLERANCE,
        "foreign_vs_all collected every stored leaf page: small={small} large={large}"
    );
}
