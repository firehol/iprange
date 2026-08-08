//! Retirement extent tests.

use super::*;
use crate::contract::PAGE_SIZE;

struct MemoryStore {
    txn: u64,
    pages: Vec<[u8; PAGE_SIZE]>,
    discarded: Vec<u32>,
}

impl MemoryStore {
    fn new(txn: u64) -> Self {
        Self {
            txn,
            pages: vec![[0; PAGE_SIZE]; 2],
            discarded: Vec::new(),
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
        let page_number = self.pages.len() as u32;
        self.pages.push([0; PAGE_SIZE]);
        Ok(page_number)
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

fn extents(store: &MemoryStore, root: u32) -> Vec<(u64, u32, u32)> {
    let mut output = Vec::new();
    let mut key = Key { txn: 0, first: 0 };
    while let Some(extent) = at_or_after(store, root, key).unwrap() {
        output.push((extent.key.txn, extent.key.first, extent.count));
        let Some(first) = extent.key.first.checked_add(1) else {
            break;
        };
        key = Key {
            txn: extent.key.txn,
            first,
        };
    }
    output
}

#[test]
fn big_endian_portable_retirement_extent_matches_literal_bytes() {
    let extent = Extent {
        key: Key {
            txn: 0x0807_0605_0403_0201,
            first: 0x0c0b_0a09,
        },
        count: 0x100f_0e0d,
    };
    let encoded = Encoded::new(extent);
    assert_eq!(
        encoded.0,
        [1, 2, 3, 4, 5, 6, 7, 8, 9, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10]
    );
    assert_eq!(decode_slice(&encoded.0).unwrap(), extent);
}

#[test]
fn arbitrary_page_order_coalesces_within_each_transaction() {
    let mut store = MemoryStore::new(9);
    let mut root = 0;
    let mut count = 0;
    for page in [10, 12, 11, 20, 22, 21, 9, 13] {
        assert!(add_page(&mut store, &mut root, &mut count, 9, page)
            .unwrap()
            .as_slice()
            .is_empty());
    }
    assert_eq!(extents(&store, root), vec![(9, 9, 5), (9, 20, 3)]);
    assert_eq!(count, 2);

    add_page(&mut store, &mut root, &mut count, 10, 14).unwrap();
    assert_eq!(
        extents(&store, root),
        vec![(9, 9, 5), (9, 20, 3), (10, 14, 1)]
    );
    assert_eq!(count, 3);
    assert!(add_page(&mut store, &mut root, &mut count, 9, 10).is_err());
    assert!(add_page(&mut store, &mut root, &mut count, 9, 20).is_err());
}

#[test]
fn first_change_of_a_committed_tree_reports_only_its_old_path() {
    let mut store = MemoryStore::new(2);
    let mut root = 0;
    let mut count = 0;
    for page in (2..1_000).step_by(2) {
        add_page(&mut store, &mut root, &mut count, 2, page).unwrap();
    }
    let old_root = root;
    let committed = store.pages.clone();
    store.txn = 3;

    let retired = add_page(&mut store, &mut root, &mut count, 3, 3).unwrap();
    assert_ne!(root, old_root);
    assert_eq!(retired.as_slice().len(), 2);
    assert_eq!(&store.pages[..committed.len()], committed.as_slice());

    let second = add_page(&mut store, &mut root, &mut count, 3, 5).unwrap();
    assert!(second.as_slice().is_empty());
}

#[test]
fn reclamation_selects_only_complete_oldest_safe_transactions() {
    let mut store = MemoryStore::new(5);
    store.pages.resize(100, [0; PAGE_SIZE]);
    let mut root = 0;
    let mut count = 0;
    for (txn, page) in [(2, 10), (2, 11), (3, 20), (3, 22), (4, 30)] {
        add_page(&mut store, &mut root, &mut count, txn, page).unwrap();
    }

    assert_eq!(
        select_reclamation(&store, root, 4, Some(3), 10, 10).unwrap(),
        Some(Reclamation {
            transactions: 2,
            pages: 4,
            through_txn: 3,
        })
    );
    assert_eq!(
        select_reclamation(&store, root, 4, None, 1, 10).unwrap(),
        Some(Reclamation {
            transactions: 1,
            pages: 2,
            through_txn: 2,
        })
    );
    assert_eq!(
        select_reclamation(&store, root, 4, None, 10, 3).unwrap(),
        Some(Reclamation {
            transactions: 1,
            pages: 2,
            through_txn: 2,
        })
    );
    assert!(matches!(
        select_reclamation(&store, root, 4, None, 10, 1),
        Err(Error::WorkLimitTooSmall { required_pages: 2 })
    ));
}
