//! Fix D proof: file-backed `FileWriter::open` must NOT copy the committed
//! database image into the heap just to validate it.
//!
//! Before Fix D, `os.rs` did `vec![0u8; committed_bytes]` + `read_at` to build a
//! throwaway `VecPageStore` for pre-growth validation — an O(file_size) heap
//! allocation. Fix D validates IN-PLACE over a read-only mmap instead.
//!
//! This test asserts the allocation is a tiny FRACTION of the committed file
//! size (no full-image copy). It is deliberately distinct from
//! `round6_open_memory::file_writer_open_heap_does_not_scale_with_committed_file_size`,
//! which enforces strict flatness and is gated on a separate writer.rs change.

#![cfg(unix)]

use std::alloc::{GlobalAlloc, Layout, System};
use std::fs::OpenOptions;
use std::io::Write;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Mutex;

use iprange_livedb::os::FileWriter;
use iprange_livedb::spec;
use iprange_livedb::wire::Meta;
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
static MEASURE_LOCK: Mutex<()> = Mutex::new(());

/// Build a sparse file whose meta claims `pages` committed pages but only the
/// first two (meta) pages carry real data; the rest are holes. This is the
/// shape that maximizes the committed_bytes-to-real-data ratio, so any code
/// that heap-copies the committed image stands out immediately.
fn sparse_empty_file(pages: u64, tag: &str) -> std::path::PathBuf {
    let writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    let mut image = writer.into_image().unwrap();
    for page_no in 0..2u64 {
        let start = (page_no as usize) * spec::PAGE_SIZE;
        let page = &mut image[start..start + spec::PAGE_SIZE];
        let mut meta = Meta::decode(page);
        meta.total_pages = pages;
        meta.encode_into(page);
    }

    let path = std::env::temp_dir().join(format!(
        "iprange-round6-open-file-copy-{tag}-{}",
        std::process::id()
    ));
    let _ = std::fs::remove_file(&path);
    let mut file = OpenOptions::new()
        .create_new(true)
        .read(true)
        .write(true)
        .open(&path)
        .unwrap();
    file.write_all(&image).unwrap();
    file.set_len(pages * spec::PAGE_SIZE as u64).unwrap();
    drop(file);
    path
}

fn open_allocated_bytes(path: &std::path::Path) -> usize {
    ALLOCATED_BYTES.store(0, Ordering::Relaxed);
    COUNTING.store(true, Ordering::Relaxed);
    let writer = FileWriter::<Ipv4Key>::open(path).unwrap();
    COUNTING.store(false, Ordering::Relaxed);
    let allocated = ALLOCATED_BYTES.load(Ordering::Relaxed);
    writer.close();
    allocated
}

#[test]
fn file_writer_open_does_not_heap_copy_the_committed_image() {
    let _guard = MEASURE_LOCK
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);

    // 65536 pages = 256 MiB committed (sparse). A pre-Fix-D open would copy all
    // 256 MiB into a throwaway Vec; Fix D validates over a read-only mmap.
    const LARGE_PAGES: u64 = 65536;
    let committed_bytes = LARGE_PAGES as usize * spec::PAGE_SIZE;

    let path = sparse_empty_file(LARGE_PAGES, "large");
    let allocated = open_allocated_bytes(&path);
    let _ = std::fs::remove_file(&path);

    // The allocation MUST be a small fraction of the committed image. A full
    // copy would be ~committed_bytes (256 MiB); the mmap-based validation path
    // is kilobytes. The 1/32 (3.1%) bound is far above the real cost yet far
    // below a full-image copy, so it isolates the Fix D Vec-copy removal.
    assert!(
        allocated < committed_bytes / 32,
        "file-backed open heap-copied a large fraction of the committed image: \
         allocated={allocated} bytes, committed={committed_bytes} bytes"
    );
}
