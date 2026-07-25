//! Generic fixed-tree mutation tests.

use super::*;
use crate::contract::{u32_le, PAGE_SIZE};
use crate::slotted_page;

struct U32Codec;

impl Codec for U32Codec {
    type Key = u32;

    const BRANCH_TYPE: u8 = 1;
    const LEAF_TYPE: u8 = 2;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 4;
    const LEAF_SIZE: usize = 8;

    fn read_key(cell: &[u8]) -> Self::Key {
        u32_le(cell, 0)
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..4].copy_from_slice(&key.to_le_bytes());
    }

    fn validate_leaf(cell: &[u8]) -> Result<()> {
        if cell.len() == Self::LEAF_SIZE {
            Ok(())
        } else {
            Err(Error::Corrupt("test leaf size is invalid"))
        }
    }
}

struct WideCodec;

impl Codec for WideCodec {
    type Key = [u8; 56];

    const BRANCH_TYPE: u8 = 1;
    const LEAF_TYPE: u8 = 2;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 56;
    const LEAF_SIZE: usize = 64;

    fn read_key(cell: &[u8]) -> Self::Key {
        cell[..56].try_into().unwrap()
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..56].copy_from_slice(&key);
    }

    fn validate_leaf(cell: &[u8]) -> Result<()> {
        if cell.len() == Self::LEAF_SIZE {
            Ok(())
        } else {
            Err(Error::Corrupt("test leaf size is invalid"))
        }
    }
}

struct MemoryStore {
    target_txn: u64,
    pages: Vec<[u8; PAGE_SIZE]>,
    discarded: Vec<u32>,
}

impl MemoryStore {
    fn new() -> Self {
        Self {
            target_txn: 1,
            pages: vec![[0; PAGE_SIZE]; 2],
            discarded: Vec::new(),
        }
    }
}

impl Store for MemoryStore {
    fn target_txn(&self) -> u64 {
        self.target_txn
    }

    fn page_limit(&self) -> u64 {
        self.pages.len() as u64
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        let source = self
            .pages
            .get(page_number as usize)
            .ok_or(Error::Corrupt("test page is out of bounds"))?;
        *page = *source;
        Ok(())
    }

    fn allocate(&mut self) -> Result<u32> {
        let page_number = u32::try_from(self.pages.len())
            .map_err(|_| Error::InvalidArgument("test page space exhausted"))?;
        self.pages.push([0; PAGE_SIZE]);
        Ok(page_number)
    }

    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        let destination = self
            .pages
            .get_mut(page_number as usize)
            .ok_or(Error::Corrupt("test page is out of bounds"))?;
        *destination = *page;
        Ok(())
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        self.discarded.push(page_number);
        Ok(())
    }
}

fn record(key: u32, value: u32) -> [u8; 8] {
    let mut cell = [0; 8];
    cell[..4].copy_from_slice(&key.to_le_bytes());
    cell[4..].copy_from_slice(&value.to_le_bytes());
    cell
}

fn wide_key(key: u32) -> [u8; 56] {
    let mut encoded = [0; 56];
    encoded[..4].copy_from_slice(&key.to_be_bytes());
    encoded
}

fn wide_record(key: u32) -> [u8; 64] {
    let mut cell = [0; 64];
    cell[..56].copy_from_slice(&wide_key(key));
    cell[56..].copy_from_slice(&(key as u64).to_le_bytes());
    cell
}

fn lookup(store: &MemoryStore, root: u32, key: u32) -> Result<Option<u32>> {
    if root == 0 {
        return Ok(None);
    }
    let mut page_number = root;
    let mut page = [0; PAGE_SIZE];
    loop {
        store.read(page_number, &mut page)?;
        let header = page::parse::<U32Codec>(&page, store.target_txn, None)?;
        if header.level == 0 {
            let (index, exists) = page::lower_bound::<U32Codec>(&page, &header, key, true)?;
            if !exists {
                return Ok(None);
            }
            let cell = slotted_page::cell(&page, &header, index, U32Codec::LEAF_SIZE)?;
            return Ok(Some(u32_le(cell, 4)));
        }
        let (index, _) = page::lower_bound::<U32Codec>(&page, &header, key, false)?;
        page_number = page::branch_child::<U32Codec>(&page, &header, index, store.page_limit())?;
    }
}

#[test]
fn inserts_replace_and_split_without_losing_order() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in (0..1_000).rev() {
        let mut retired = RetiredPages::new();
        assert!(
            insert::<U32Codec, _>(&mut store, &mut root, &record(key, key + 10), &mut retired)
                .unwrap()
        );
        assert!(retired.as_slice().is_empty());
    }
    assert_ne!(root, 0);
    for key in 0..1_000 {
        assert_eq!(lookup(&store, root, key).unwrap(), Some(key + 10));
    }
    assert_eq!(lookup(&store, root, 1_001).unwrap(), None);
    assert_eq!(
        u32_le(
            predecessor::<U32Codec, _>(&store, root, 501)
                .unwrap()
                .unwrap()
                .as_slice(),
            0
        ),
        501
    );
    assert_eq!(
        u32_le(
            at_or_after::<U32Codec, _>(&store, root, 501)
                .unwrap()
                .unwrap()
                .as_slice(),
            0
        ),
        501
    );
    assert!(predecessor::<U32Codec, _>(&store, root, 0)
        .unwrap()
        .is_some());
    assert!(at_or_after::<U32Codec, _>(&store, root, 1_001)
        .unwrap()
        .is_none());

    let mut retired = RetiredPages::new();
    assert!(!insert::<U32Codec, _>(&mut store, &mut root, &record(500, 7), &mut retired).unwrap());
    assert_eq!(lookup(&store, root, 500).unwrap(), Some(7));
}

#[test]
fn next_transaction_copies_only_its_selected_path() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in 0..1_000 {
        insert::<U32Codec, _>(
            &mut store,
            &mut root,
            &record(key, key),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    let old_root = root;
    let committed = store.pages.clone();
    store.target_txn = 2;

    let mut retired = RetiredPages::new();
    insert::<U32Codec, _>(&mut store, &mut root, &record(1_000, 99), &mut retired).unwrap();
    assert_ne!(root, old_root);
    assert_eq!(retired.as_slice().len(), 2);
    assert_eq!(&store.pages[..committed.len()], committed.as_slice());
    assert_eq!(lookup(&store, root, 999).unwrap(), Some(999));
    assert_eq!(lookup(&store, root, 1_000).unwrap(), Some(99));

    let mut same_path = RetiredPages::new();
    insert::<U32Codec, _>(&mut store, &mut root, &record(1_001, 100), &mut same_path).unwrap();
    assert!(same_path.as_slice().is_empty());
}

#[test]
fn deletion_removes_empty_children_and_collapses_the_root() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in 0..900 {
        insert::<U32Codec, _>(
            &mut store,
            &mut root,
            &record(key, key),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    assert!(
        !delete::<U32Codec, _>(&mut store, &mut root, 1_000, &mut RetiredPages::new()).unwrap()
    );

    for key in 0..900 {
        assert!(
            delete::<U32Codec, _>(&mut store, &mut root, key, &mut RetiredPages::new()).unwrap()
        );
        assert_eq!(lookup(&store, root, key).unwrap(), None);
    }
    assert_eq!(root, 0);
    assert!(!store.discarded.is_empty());
}

#[test]
fn branch_splits_create_and_search_a_three_level_tree() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in (0..5_000).rev() {
        insert::<WideCodec, _>(
            &mut store,
            &mut root,
            &wide_record(key),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }

    let mut page = [0; PAGE_SIZE];
    store.read(root, &mut page).unwrap();
    let header = page::parse::<WideCodec>(&page, store.target_txn, None).unwrap();
    assert_eq!(header.level, 2);
    assert_eq!(
        predecessor::<WideCodec, _>(&store, root, wide_key(4_999))
            .unwrap()
            .unwrap()
            .as_slice(),
        wide_record(4_999)
    );
}
