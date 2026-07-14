use iprange_livedb::free_list::validate_chain_crc;
use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::scope_table::MAX_BITMAP_WIDTH;
use iprange_livedb::spec;
use iprange_livedb::wire::{finalize_checksum, Meta, PageHeader};
use iprange_livedb::{Ipv4Key, Reader, Result, Writer};
use std::sync::atomic::{AtomicUsize, Ordering};

#[test]
fn scope_validation_rejects_cross_leaf_ordering() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    for i in 0..20u8 {
        writer.scope_intern(&[i, 1]).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let root_base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let root = &image[root_base..root_base + spec::PAGE_SIZE];
    let header = PageHeader::decode(root);
    assert_eq!(header.page_type, spec::PAGE_TYPE_SCOPE_BRANCH);
    let second_child = u32_at(root, spec::PAGE_HEADER_SIZE + 4 + spec::SCOPE_KEY_WIDTH);
    let leaf_base = second_child as usize * spec::PAGE_SIZE;
    let leaf = &mut image[leaf_base..leaf_base + spec::PAGE_SIZE];
    put_u32(leaf, spec::PAGE_HEADER_SIZE, 1);
    finalize_checksum(leaf);

    let reader = Reader::open(&image).unwrap();
    assert!(
        reader.validate().is_err(),
        "Reader::validate accepted checksum-valid cross-leaf scope disorder"
    );
    assert!(
        Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).is_err(),
        "writable open accepted checksum-valid cross-leaf scope disorder"
    );
}

#[test]
fn scope_validation_rejects_oversized_bitmap_length() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    writer.scope_intern(&[1]).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let leaf_base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    let leaf = &mut image[leaf_base..leaf_base + spec::PAGE_SIZE];
    assert_eq!(
        PageHeader::decode(leaf).page_type,
        spec::PAGE_TYPE_SCOPE_LEAF
    );
    put_u16(
        leaf,
        spec::PAGE_HEADER_SIZE + 4,
        (MAX_BITMAP_WIDTH + 1) as u16,
    );
    finalize_checksum(leaf);

    let reader = Reader::open(&image).unwrap();
    assert!(
        reader.validate().is_err(),
        "Reader::validate accepted bitmap_len beyond the on-disk payload"
    );
    assert!(
        Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).is_err(),
        "writable open accepted bitmap_len beyond the on-disk payload"
    );
}

#[test]
fn indirect_validation_rejects_dangling_record_scope() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let scope = writer.scope_intern(&[1]).unwrap();
    writer.set(Ipv4Key(1), Ipv4Key(10), scope).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let root_base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let root = &mut image[root_base..root_base + spec::PAGE_SIZE];
    assert_eq!(PageHeader::decode(root).page_type, spec::PAGE_TYPE_LEAF);
    put_u32(
        root,
        spec::PAGE_HEADER_SIZE + 2 * meta.key_width as usize,
        scope + 1,
    );
    finalize_checksum(root);
    let reader = Reader::open(&image).unwrap();
    assert!(
        reader.validate().is_err(),
        "Reader::validate accepted a record referencing an undefined scope"
    );
    assert!(
        Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).is_err(),
        "writable open accepted a record referencing an undefined scope"
    );
}

#[test]
fn free_list_rejects_reserved_and_live_pages() {
    let (base, meta) = free_list_fixture();
    let cases = [
        ("meta-page-zero", 0),
        ("active-data-root", meta.root_pgno),
        ("free-list-head", meta.free_list_head),
    ];
    for (name, target) in cases {
        let mut image = base.clone();
        let page_base = meta.free_list_head as usize * spec::PAGE_SIZE;
        let page = &mut image[page_base..page_base + spec::PAGE_SIZE];
        assert!(u32_at(page, spec::TXN_FREE_COUNT) > 0);
        put_u32(page, spec::TXN_FREE_ARRAY, target);
        finalize_checksum(page);
        assert!(
            Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image))).is_err(),
            "writable open accepted {name} page {target} as free"
        );
    }
}

fn free_list_fixture() -> (Vec<u8>, Meta) {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..500u32 {
        writer.set(Ipv4Key(i * 2), Ipv4Key(i * 2), 1).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    for i in 0..250u32 {
        writer.delete(Ipv4Key(i * 2), Ipv4Key(i * 2)).unwrap();
    }
    writer.commit(2, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    assert_ne!(meta.free_list_head, 0);
    assert_ne!(meta.root_pgno, 0);
    (image, meta)
}

struct BoundedPageStore {
    image: Vec<u8>,
    reads: AtomicUsize,
    committed: u32,
}

impl PageStore for BoundedPageStore {
    fn page(&self, pgno: u32) -> &[u8] {
        let reads = self.reads.fetch_add(1, Ordering::Relaxed) + 1;
        assert!(
            reads <= self.total_pages() as usize,
            "free-list traversal exceeded total_pages"
        );
        let base = pgno as usize * spec::PAGE_SIZE;
        &self.image[base..base + spec::PAGE_SIZE]
    }

    fn page_mut(&mut self, pgno: u32) -> &mut [u8] {
        let base = pgno as usize * spec::PAGE_SIZE;
        &mut self.image[base..base + spec::PAGE_SIZE]
    }

    fn copy_page(&mut self, src_pgno: u32, dst_pgno: u32) {
        let src = src_pgno as usize * spec::PAGE_SIZE;
        let dst = dst_pgno as usize * spec::PAGE_SIZE;
        self.image.copy_within(src..src + spec::PAGE_SIZE, dst);
    }

    fn alloc_page(&mut self) -> Result<u32> {
        let pgno = self.total_pages();
        self.image.resize(self.image.len() + spec::PAGE_SIZE, 0);
        Ok(pgno)
    }

    fn total_pages(&self) -> u32 {
        (self.image.len() / spec::PAGE_SIZE) as u32
    }

    fn committed_pages(&self) -> u32 {
        self.committed
    }

    fn set_committed_pages(&mut self, pages: u32) {
        self.committed = pages;
    }

    fn committed_bytes(&self) -> &[u8] {
        &self.image[..self.committed as usize * spec::PAGE_SIZE]
    }

    fn ensure_capacity(&mut self, min_pages: u32) -> Result<()> {
        self.image.resize(min_pages as usize * spec::PAGE_SIZE, 0);
        Ok(())
    }

    fn sync(&self) -> Result<()> {
        Ok(())
    }

    fn truncate(&mut self, new_total_pages: u32) -> Result<()> {
        self.image
            .truncate(new_total_pages as usize * spec::PAGE_SIZE);
        Ok(())
    }
}

#[test]
fn free_list_cycle_is_rejected_within_file_bounds() {
    let mut store = BoundedPageStore {
        image: vec![0u8; 3 * spec::PAGE_SIZE],
        reads: AtomicUsize::new(0),
        committed: 3,
    };
    let page = store.page_mut(2);
    PageHeader::write(page, spec::PAGE_TYPE_TXN_FREE, 0, 2);
    put_u32(page, spec::TXN_FREE_NEXT, 2);
    finalize_checksum(page);

    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        validate_chain_crc(&store, 2)
    }));
    match result {
        Ok(Err(_)) => {}
        Ok(Ok(())) => panic!("validate_chain_crc accepted a self-referential chain"),
        Err(_) => panic!("free-list cycle was not detected before rereading pages"),
    }
}

#[cfg(feature = "export-v3")]
#[test]
fn export_v3_validates_structure_before_export() {
    use iprange_livedb::export::{export_v3, V3Meta};

    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    writer.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let root_base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let root = &mut image[root_base..root_base + spec::PAGE_SIZE];
    root[spec::PH_RESERVED] = 1;
    finalize_checksum(root);

    let reader = Reader::open(&image).unwrap();
    assert!(reader.validate().is_err(), "fixture is not invalid");
    assert!(
        export_v3(&image, 7, V3Meta::default()).is_err(),
        "export_v3 exported a checksum-valid but structurally invalid v4 image"
    );
}

#[test]
fn reader_operations_never_panic_on_malformed_leaf_count() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    writer.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let mut image = writer.into_image().unwrap();
    let meta = active_meta(&image);
    let root_base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let root = &mut image[root_base..root_base + spec::PAGE_SIZE];
    put_u16(root, spec::PH_ENTRY_COUNT, u16::MAX);
    finalize_checksum(root);

    let lookup = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        if let Ok(reader) = Reader::open(&image) {
            let _ = reader.lookup_v4(Ipv4Key(15));
        }
    }));
    assert!(lookup.is_ok(), "lookup panicked on malformed entry_count");

    let scan = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        if let Ok(reader) = Reader::open(&image) {
            let _ = reader.scan_v4(|_, _, _| {});
        }
    }));
    assert!(scan.is_ok(), "scan panicked on malformed entry_count");
}

fn active_meta(image: &[u8]) -> Meta {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        first
    } else {
        second
    }
}

fn u32_at(bytes: &[u8], offset: usize) -> u32 {
    u32::from_le_bytes(bytes[offset..offset + 4].try_into().unwrap())
}

fn put_u16(bytes: &mut [u8], offset: usize, value: u16) {
    bytes[offset..offset + 2].copy_from_slice(&value.to_le_bytes());
}

fn put_u32(bytes: &mut [u8], offset: usize, value: u32) {
    bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
}
