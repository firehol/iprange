#![cfg(unix)]

use std::alloc::{GlobalAlloc, Layout, System};
use std::fs::{File, OpenOptions};
use std::io::Write;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Mutex;

use iprange_livedb::os::FileWriter;
use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::spec;
use iprange_livedb::wire::Meta;
use iprange_livedb::{Error, Ipv4Key, Writer};

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

fn sparse_empty_file(pages: u64, tag: &str) -> (std::path::PathBuf, File) {
    let writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    let mut image = writer.into_image().unwrap();
    for page_no in 0..2 {
        let start = page_no * spec::PAGE_SIZE;
        let page = &mut image[start..start + spec::PAGE_SIZE];
        let mut meta = Meta::decode(page);
        meta.total_pages = pages;
        meta.encode_into(page);
    }

    let path = std::env::temp_dir().join(format!(
        "iprange-round6-open-memory-{tag}-{}",
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
    (path, file)
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
fn file_writer_open_heap_does_not_scale_with_committed_file_size() {
    let _guard = MEASURE_LOCK
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    const LARGE_PAGES: u64 = 16384; // 64 MiB sparse committed image.
    let (small_path, small_file) = sparse_empty_file(2, "small");
    drop(small_file);
    let small = open_allocated_bytes(&small_path);
    let _ = std::fs::remove_file(&small_path);

    let (large_path, large_file) = sparse_empty_file(LARGE_PAGES, "large");
    drop(large_file);
    let large = open_allocated_bytes(&large_path);
    let _ = std::fs::remove_file(&large_path);

    const TOLERANCE: usize = 32 << 10;
    assert!(
        large <= small + TOLERANCE,
        "file-backed writable open allocates with committed file size: small={small} large={large}"
    );
}

struct VirtualStore {
    image: Vec<u8>,
    pages: u32,
}

impl PageStore for VirtualStore {
    fn page(&self, pgno: u32) -> &[u8] {
        let base = pgno as usize * spec::PAGE_SIZE;
        &self.image[base..base + spec::PAGE_SIZE]
    }

    fn page_mut(&mut self, _pgno: u32) -> &mut [u8] {
        panic!("unexpected mutation")
    }

    fn copy_page(&mut self, _src_pgno: u32, _dst_pgno: u32) {
        panic!("unexpected copy")
    }

    fn alloc_page(&mut self) -> Result<u32, Error> {
        panic!("unexpected allocation")
    }

    fn total_pages(&self) -> u32 {
        self.pages
    }

    fn committed_pages(&self) -> u32 {
        self.pages
    }

    fn set_committed_pages(&mut self, _pages: u32) {
        panic!("unexpected commit")
    }

    fn committed_bytes(&self) -> &[u8] {
        &self.image
    }

    fn ensure_capacity(&mut self, _min_pages: u32) -> Result<(), Error> {
        panic!("unexpected growth")
    }

    fn sync(&self) -> Result<(), Error> {
        panic!("unexpected sync")
    }

    fn truncate(&mut self, _new_total_pages: u32) -> Result<(), Error> {
        panic!("unexpected truncate")
    }
}

fn malformed_core_open_allocated_bytes(pages: u32) -> (usize, Result<Writer<Ipv4Key>, Error>) {
    let writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    let mut image = writer.into_image().unwrap();
    for page_no in 0..2 {
        let start = page_no * spec::PAGE_SIZE;
        let page = &mut image[start..start + spec::PAGE_SIZE];
        let mut meta = Meta::decode(page);
        meta.total_pages = u64::from(pages);
        meta.encode_into(page);
    }
    ALLOCATED_BYTES.store(0, Ordering::Relaxed);
    COUNTING.store(true, Ordering::Relaxed);
    let opened = Writer::<Ipv4Key>::open(Box::new(VirtualStore { image, pages }));
    COUNTING.store(false, Ordering::Relaxed);
    let allocated = ALLOCATED_BYTES.load(Ordering::Relaxed);
    (allocated, opened)
}

#[test]
fn core_writer_rejects_short_store_without_page_count_sized_allocation() {
    let _guard = MEASURE_LOCK
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    const LARGE_PAGES: u32 = 8 << 20; // A 32 GiB logical database.
    let (allocated, result) = malformed_core_open_allocated_bytes(LARGE_PAGES);
    const TOLERANCE: usize = 4 << 20;
    assert!(
        allocated <= TOLERANCE,
        "core writable open reserved heap from untrusted page count before rejecting it: allocated={allocated}"
    );
    assert!(
        result.is_err(),
        "core writable open accepted metadata whose committed image is shorter than total_pages"
    );
}

fn tree_with_free_list(records: u32) -> Vec<u8> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..records {
        let ip = Ipv4Key(i * 2);
        writer.append(ip, ip, 1).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    writer.delete(Ipv4Key(0), Ipv4Key(0)).unwrap();
    writer.commit(2, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    let active = if first.txn_id >= second.txn_id {
        first
    } else {
        second
    };
    assert_ne!(
        active.free_list_head, 0,
        "fixture did not persist a free list"
    );
    image
}

fn core_image_open_allocated_bytes(image: Vec<u8>) -> usize {
    ALLOCATED_BYTES.store(0, Ordering::Relaxed);
    COUNTING.store(true, Ordering::Relaxed);
    let writer = Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).unwrap();
    COUNTING.store(false, Ordering::Relaxed);
    let allocated = ALLOCATED_BYTES.load(Ordering::Relaxed);
    drop(writer);
    allocated
}

#[test]
fn free_list_validation_heap_does_not_scale_with_live_tree_pages() {
    let _guard = MEASURE_LOCK
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    let small_image = tree_with_free_list(1_000);
    let large_image = tree_with_free_list(500_000);
    let small = core_image_open_allocated_bytes(small_image);
    let large = core_image_open_allocated_bytes(large_image);
    const TOLERANCE: usize = 16 << 10;
    assert!(
        large <= small + TOLERANCE,
        "free-list validation allocates per reachable tree page: small={small} large={large}"
    );
}
