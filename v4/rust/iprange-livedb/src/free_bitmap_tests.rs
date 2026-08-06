//! Free bitmap tests across leaf and branch boundaries.

use super::*;
use crate::contract::PAGE_SIZE;
use crate::fixed_tree::{RetiredPages, Store};

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

    fn seal_current(&mut self) {
        for page in &mut self.pages[2..] {
            if page[..4] == PAGE_MAGIC && u64_le(&*page, 8) == self.txn {
                crate::page_checksum::seal(page).unwrap();
            }
        }
    }
}

impl Store for MemoryStore {
    type ReadPage<'a> = &'a [u8; PAGE_SIZE];
    type WritePage<'a> = [u8; PAGE_SIZE];

    fn target_txn(&self) -> u64 {
        self.txn
    }

    fn page_limit(&self) -> u64 {
        self.pages.len() as u64
    }

    fn inspect_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        inspect(
            self.pages
                .get(page_number as usize)
                .ok_or(Error::Corrupt("test page is out of bounds"))?,
        )
    }

    fn allocate(&mut self) -> Result<u32> {
        self.allocate_bitmap_page()
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        update(
            self.pages
                .get_mut(page_number as usize)
                .ok_or(Error::Corrupt("test page is out of bounds"))?,
        )
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        crate::test_support_tests::copy_pages(&mut self.pages, source, destination, copy)
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
    store.seal_current();
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
    corrupt.seal_current();
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
