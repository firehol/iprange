#![cfg(unix)]

use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

use iprange_livedb::os::FileWriter;
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

fn scope_only_image(scopes: usize) -> Vec<u8> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    for i in 0..scopes {
        let bitmap = [i as u8, (i >> 8) as u8, (i >> 16) as u8, 0xa5];
        writer.scope_intern(&bitmap).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    writer.into_image().unwrap()
}

fn scope_commit_allocated_bytes(scopes: usize, tag: &str) -> usize {
    let path = std::env::temp_dir().join(format!(
        "iprange-round6-scope-commit-{tag}-{}",
        std::process::id()
    ));
    let _ = std::fs::remove_file(&path);
    std::fs::write(&path, scope_only_image(scopes)).unwrap();
    let mut writer = FileWriter::<Ipv4Key>::open(&path).unwrap();
    writer
        .scope_intern(&[0xff, 0xff, 0xff, 0xff, 0x01])
        .unwrap();

    ALLOCATED_BYTES.store(0, Ordering::Relaxed);
    COUNTING.store(true, Ordering::Relaxed);
    writer.commit(2).unwrap();
    COUNTING.store(false, Ordering::Relaxed);
    let allocated = ALLOCATED_BYTES.load(Ordering::Relaxed);
    writer.close();
    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
    allocated
}

#[test]
fn adding_one_scope_commit_heap_does_not_scale_with_committed_scope_count() {
    let small = scope_commit_allocated_bytes(100, "small");
    let large = scope_commit_allocated_bytes(20_000, "large");
    const TOLERANCE: usize = 256 << 10;
    assert!(
        large <= small + TOLERANCE,
        "committing one new scope materialized all committed scopes: small={small} large={large}"
    );
}
