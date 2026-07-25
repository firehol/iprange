use super::*;
use crate::contract::u16_le;
use crate::fixed_tree::RetiringStore;

struct MemoryStore {
    txn: u64,
    pages: Vec<[u8; PAGE_SIZE]>,
    discarded: Vec<u32>,
    retired: Vec<u32>,
}

impl MemoryStore {
    fn new() -> Self {
        Self {
            txn: 1,
            pages: vec![[0; PAGE_SIZE]; 2],
            discarded: Vec::new(),
            retired: Vec::new(),
        }
    }
}

impl RetiringStore for MemoryStore {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        self.retired.extend_from_slice(pages);
        Ok(())
    }
}

impl Store for MemoryStore {
    fn target_txn(&self) -> u64 {
        self.txn
    }

    fn page_limit(&self) -> u64 {
        self.pages.len() as u64
    }

    fn read(&self, page_number: u32, output: &mut [u8; PAGE_SIZE]) -> Result<()> {
        *output = *self
            .pages
            .get(page_number as usize)
            .ok_or(Error::Corrupt("test bitmap page is out of bounds"))?;
        Ok(())
    }

    fn allocate(&mut self) -> Result<u32> {
        let page = self.pages.len() as u32;
        self.pages.push([0; PAGE_SIZE]);
        Ok(page)
    }

    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        *self
            .pages
            .get_mut(page_number as usize)
            .ok_or(Error::Corrupt("test bitmap page is out of bounds"))? = *page;
        Ok(())
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        self.discarded.push(page_number);
        Ok(())
    }
}

#[test]
fn lowest_zero_crosses_leaf_boundary_and_reuses_cleared_bit() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for bit in 0..32_000 {
        set(
            &mut store,
            &mut root,
            32_002,
            Kind::Feed,
            bit,
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    assert_eq!(
        take_lowest(
            &mut store,
            &mut root,
            32_002,
            Kind::Feed,
            &mut RetiredPages::new()
        )
        .unwrap(),
        Some(32_000)
    );
    assert!(clear(
        &mut store,
        &mut root,
        32_002,
        Kind::Feed,
        17,
        &mut RetiredPages::new()
    )
    .unwrap());
    assert_eq!(
        take_lowest(
            &mut store,
            &mut root,
            32_002,
            Kind::Feed,
            &mut RetiredPages::new()
        )
        .unwrap(),
        Some(17)
    );
}

#[test]
fn membership_zero_is_never_an_allocation_candidate() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    assert_eq!(
        take_lowest(
            &mut store,
            &mut root,
            3,
            Kind::Membership,
            &mut RetiredPages::new()
        )
        .unwrap(),
        Some(1)
    );
    assert_eq!(
        take_lowest(
            &mut store,
            &mut root,
            3,
            Kind::Membership,
            &mut RetiredPages::new()
        )
        .unwrap(),
        Some(2)
    );
    assert_eq!(
        take_lowest(
            &mut store,
            &mut root,
            3,
            Kind::Membership,
            &mut RetiredPages::new()
        )
        .unwrap(),
        None
    );
}

#[test]
fn clear_of_final_bit_omits_the_root() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    set(
        &mut store,
        &mut root,
        8,
        Kind::Feed,
        3,
        &mut RetiredPages::new(),
    )
    .unwrap();
    assert!(clear(
        &mut store,
        &mut root,
        8,
        Kind::Feed,
        3,
        &mut RetiredPages::new()
    )
    .unwrap());
    assert_eq!(root, 0);
    assert_eq!(store.discarded.len(), 1);
}

#[test]
fn committed_paths_are_copied_before_mutation() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    set(
        &mut store,
        &mut root,
        40_000,
        Kind::Feed,
        32_001,
        &mut RetiredPages::new(),
    )
    .unwrap();
    let committed_root = root;
    let committed = store.pages.clone();
    store.txn = 2;
    let mut retired = RetiredPages::new();
    assert!(clear(
        &mut store,
        &mut root,
        40_000,
        Kind::Feed,
        32_001,
        &mut retired
    )
    .unwrap());
    assert_ne!(root, committed_root);
    assert_eq!(&store.pages[..committed.len()], committed.as_slice());
    assert_eq!(retired.as_slice().len(), 2);
}

#[test]
fn membership_limit_and_root_level_shrink_with_the_highest_id() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for bit in [1, 32_001] {
        set(
            &mut store,
            &mut root,
            32_002,
            Kind::Membership,
            bit,
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    store.txn = 2;
    let mut retired = RetiredPages::new();
    assert!(clear(
        &mut store,
        &mut root,
        32_002,
        Kind::Membership,
        32_001,
        &mut retired,
    )
    .unwrap());
    store.retire_pages(retired.as_slice()).unwrap();
    let limit = shrink_membership(&mut store, &mut root, 32_002).unwrap();
    assert_eq!(limit, 2);
    assert_ne!(root, 0);
    assert_eq!(u16_le(&store.pages[root as usize], 18), 0);
    assert!(search::contains(&store, root, limit, Kind::Membership, 1).unwrap());

    let mut retired = RetiredPages::new();
    assert!(clear(
        &mut store,
        &mut root,
        limit,
        Kind::Membership,
        1,
        &mut retired,
    )
    .unwrap());
    store.retire_pages(retired.as_slice()).unwrap();
    assert_eq!(shrink_membership(&mut store, &mut root, limit).unwrap(), 1);
    assert_eq!(root, 0);
}
