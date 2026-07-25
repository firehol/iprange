//! Free bitmap tests across leaf and branch boundaries.

use super::*;

struct MemoryStore {
    txn: u64,
    pages: Vec<[u8; PAGE_SIZE]>,
    discarded: Vec<u32>,
    forbidden: u32,
}

impl MemoryStore {
    fn new(txn: u64) -> Self {
        Self {
            txn,
            pages: vec![[0; PAGE_SIZE]; 2],
            discarded: Vec::new(),
            forbidden: 0,
        }
    }
}

impl Store for MemoryStore {
    fn target_txn(&self) -> u64 {
        self.txn
    }

    fn page_limit(&self) -> u64 {
        self.pages.len() as u64
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        *page = *self
            .pages
            .get(page_number as usize)
            .ok_or(Error::Corrupt("test page is out of bounds"))?;
        Ok(())
    }

    fn allocate(&mut self) -> Result<u32> {
        self.allocate_bitmap_page()
    }

    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        *self
            .pages
            .get_mut(page_number as usize)
            .ok_or(Error::Corrupt("test page is out of bounds"))? = *page;
        Ok(())
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        self.discarded.push(page_number);
        Ok(())
    }
}

impl BitmapStore for MemoryStore {
    fn allocate_bitmap_page(&mut self) -> Result<u32> {
        let page_number = self.pages.len() as u32;
        self.pages.push([0; PAGE_SIZE]);
        Ok(page_number)
    }

    fn allocation_forbidden(&self, page_number: u32) -> bool {
        page_number == self.forbidden
    }
}

#[test]
fn lowest_free_page_is_selected_across_sparse_subtrees() {
    let mut store = MemoryStore::new(2);
    let mut root = 0;
    let limit = 9_000_000;
    for bit in [8_500_000, 64, 32_001, 3, 32_000] {
        set_free(&mut store, &mut root, limit, bit, &mut RetiredPages::new()).unwrap();
    }

    let mut selected = Vec::new();
    while let Some(page) =
        take_lowest(&mut store, &mut root, limit, &mut RetiredPages::new()).unwrap()
    {
        selected.push(page);
    }
    assert_eq!(selected, vec![3, 64, 32_000, 32_001, 8_500_000]);
    assert_eq!(root, 0);
    assert!(!store.discarded.is_empty());
}

#[test]
fn committed_path_is_copied_once_and_checksum_checked() {
    let mut store = MemoryStore::new(2);
    let mut root = 0;
    set_free(
        &mut store,
        &mut root,
        100_000,
        40_000,
        &mut RetiredPages::new(),
    )
    .unwrap();
    let committed = store.pages.clone();
    store.txn = 3;

    let mut retired = RetiredPages::new();
    set_free(&mut store, &mut root, 100_000, 40_001, &mut retired).unwrap();
    assert_eq!(retired.as_slice().len(), 2);
    assert_eq!(&store.pages[..committed.len()], committed.as_slice());

    let mut second = RetiredPages::new();
    set_free(&mut store, &mut root, 100_000, 40_002, &mut second).unwrap();
    assert!(second.as_slice().is_empty());

    let mut corrupt = MemoryStore::new(2);
    let mut corrupt_root = 0;
    set_free(
        &mut corrupt,
        &mut corrupt_root,
        100,
        50,
        &mut RetiredPages::new(),
    )
    .unwrap();
    corrupt.pages[corrupt_root as usize][100] ^= 1;
    corrupt.txn = 3;
    assert!(take_lowest(
        &mut corrupt,
        &mut corrupt_root,
        100,
        &mut RetiredPages::new()
    )
    .is_err());
}

#[test]
fn protected_page_is_rejected_before_overwrite() {
    let mut store = MemoryStore::new(2);
    let mut root = 0;
    set_free(&mut store, &mut root, 100, 50, &mut RetiredPages::new()).unwrap();
    store.forbidden = 50;
    assert!(take_lowest(&mut store, &mut root, 100, &mut RetiredPages::new()).is_err());
}
