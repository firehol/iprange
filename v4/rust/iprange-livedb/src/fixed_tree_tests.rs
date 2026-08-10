//! Generic fixed-tree mutation tests.

use std::cell::Cell;

use super::*;
use crate::contract::{u32_le, PAGE_SIZE};
use crate::mapping::{ByteRange, ByteSource};
use crate::slotted_page;

struct U32Codec;

impl Codec for U32Codec {
    type Key = u32;
    type Leaf = [u8; 8];

    const BRANCH_TYPE: u8 = 1;
    const LEAF_TYPE: u8 = 2;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 4;
    const LEAF_SIZE: usize = 8;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        Ok(u32_le(cell, 0))
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        cell.array(0)
            .ok_or(Error::Corrupt("test leaf size is invalid"))
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..4].copy_from_slice(&key.to_le_bytes());
    }
}

struct AcceptGap;

struct U32Item;

impl CursorItem<U32Codec> for U32Item {
    type Output = [u8; 8];

    fn read<S: ByteSource>(
        &mut self,
        page: S,
        header: &slotted_page::Header,
        _page_number: u32,
        index: usize,
    ) -> Result<Self::Output> {
        U32Codec::read_leaf(U32Codec::leaf_cell(page, header, index)?)
    }
}

impl LocalGap<U32Codec> for AcceptGap {
    type Reject = ();

    fn previous<B: ByteSource>(
        &mut self,
        _exact: bool,
        _cell: Option<B>,
    ) -> Result<LocalPrevious<Self::Reject>> {
        Ok(LocalPrevious::Accept)
    }

    fn next<B: ByteSource>(&mut self, _cell: Option<B>) -> Result<LocalNext<Self::Reject>> {
        Ok(LocalNext::Accept)
    }
}

struct WideCodec;

impl Codec for WideCodec {
    type Key = [u8; 56];
    type Leaf = [u8; 64];

    const BRANCH_TYPE: u8 = 1;
    const LEAF_TYPE: u8 = 2;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 56;
    const LEAF_SIZE: usize = 64;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        cell.array(0)
            .ok_or(Error::Corrupt("test wide key is truncated"))
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        cell.array(0)
            .ok_or(Error::Corrupt("test leaf size is invalid"))
    }

    fn write_key(key: Self::Key, output: &mut [u8]) {
        output[..56].copy_from_slice(&key);
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
struct VarKey {
    bytes: [u8; 32],
    len: u8,
}

struct VariableCodec;

impl Codec for VariableCodec {
    type Key = VarKey;
    type Leaf = (VarKey, u32);

    const BRANCH_TYPE: u8 = 1;
    const LEAF_TYPE: u8 = 2;
    const AUX: u32 = 0;
    const KEY_SIZE: usize = 0;
    const LEAF_SIZE: usize = 0;
    const MAX_BRANCH_SIZE: usize = 44;
    const MAX_LEAF_SIZE: usize = 44;

    fn read_key<S: ByteSource>(cell: S, _level: u16) -> Result<Self::Key> {
        decode_variable(cell).map(|(key, _)| key)
    }

    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf> {
        decode_variable(cell)
    }

    fn write_key(_key: Self::Key, _output: &mut [u8]) {}

    fn leaf_cell<S: ByteSource>(
        page: S,
        header: &slotted_page::Header,
        index: usize,
    ) -> Result<ByteRange<S>> {
        slotted_page::record(page, header, index, 13, Self::MAX_LEAF_SIZE)
    }

    fn branch_cell<S: ByteSource>(
        page: S,
        header: &slotted_page::Header,
        index: usize,
    ) -> Result<ByteRange<S>> {
        slotted_page::record(page, header, index, 13, Self::MAX_BRANCH_SIZE)
    }

    fn write_branch(key: Self::Key, child: u32, output: &mut [u8]) -> Result<usize> {
        encode_variable(key, child, output)
    }

    fn read_branch_child<S: ByteSource>(cell: S) -> Result<u32> {
        decode_variable(cell).map(|(_, child)| child)
    }
}

struct MemoryStore {
    target_txn: u64,
    pages: Vec<[u8; PAGE_SIZE]>,
    discarded: Vec<u32>,
    reads: Cell<u64>,
    inspections: Cell<u64>,
    writes: u64,
    updates: u64,
}

impl MemoryStore {
    fn new() -> Self {
        Self {
            target_txn: 1,
            pages: vec![[0; PAGE_SIZE]; 2],
            discarded: Vec::new(),
            reads: Cell::new(0),
            inspections: Cell::new(0),
            writes: 0,
            updates: 0,
        }
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        self.reads.set(self.reads.get() + 1);
        *page = *self
            .pages
            .get(page_number as usize)
            .ok_or(Error::Corrupt("test page is out of bounds"))?;
        Ok(())
    }
}

impl Store for MemoryStore {
    type ReadPage<'a> = &'a [u8; PAGE_SIZE];
    type WritePage<'a> = [u8; PAGE_SIZE];

    fn target_txn(&self) -> u64 {
        self.target_txn
    }

    fn page_limit(&self) -> u64 {
        self.pages.len() as u64
    }

    fn inspect_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        self.inspections.set(self.inspections.get() + 1);
        inspect(
            self.pages
                .get(page_number as usize)
                .ok_or(Error::Corrupt("test page is out of bounds"))?,
        )
    }

    fn allocate(&mut self) -> Result<u32> {
        let page_number = u32::try_from(self.pages.len())
            .map_err(|_| Error::InvalidArgument("test page space exhausted"))?;
        self.pages.push([0; PAGE_SIZE]);
        Ok(page_number)
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        self.updates += 1;
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
        self.reads.set(self.reads.get() + 1);
        self.writes += 1;
        let source = source as usize;
        let destination = destination as usize;
        if source == destination || source >= self.pages.len() || destination >= self.pages.len() {
            return Err(Error::Corrupt("test copy pages are invalid"));
        }
        let (source_page, destination_page) = if source < destination {
            let (left, right) = self.pages.split_at_mut(destination);
            (&left[source], &mut right[0])
        } else {
            let (left, right) = self.pages.split_at_mut(source);
            (&right[0], &mut left[destination])
        };
        copy(source_page, destination_page)
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

fn variable_key(key: u32) -> VarKey {
    let name = format!("{key:05}{}", "x".repeat((key as usize * 17) % 28));
    let mut bytes = [0; 32];
    bytes[..name.len()].copy_from_slice(name.as_bytes());
    VarKey {
        bytes,
        len: name.len() as u8,
    }
}

fn variable_record(key: u32, value: u32) -> Vec<u8> {
    let key = variable_key(key);
    let mut record = vec![0; 12 + usize::from(key.len)];
    encode_variable(key, value, &mut record).unwrap();
    record
}

fn encode_variable(key: VarKey, value: u32, output: &mut [u8]) -> Result<usize> {
    let len = 12 + usize::from(key.len);
    if output.len() < len {
        return Err(Error::InvalidArgument("variable test buffer is too small"));
    }
    output[..len].fill(0);
    output[..2].copy_from_slice(&(len as u16).to_le_bytes());
    output[4..8].copy_from_slice(&value.to_le_bytes());
    output[8] = key.len;
    output[12..len].copy_from_slice(&key.bytes[..usize::from(key.len)]);
    Ok(len)
}

fn decode_variable<S: ByteSource>(cell: S) -> Result<(VarKey, u32)> {
    let len = cell.byte(8).map(usize::from).unwrap_or(0);
    if !(1..=32).contains(&len)
        || cell.len() != 12 + len
        || cell.array::<2>(0).map(u16::from_le_bytes).map(usize::from) != Some(cell.len())
        || !cell.all_zero(2, 2)
        || !cell.all_zero(9, 3)
    {
        return Err(Error::Corrupt("variable test record is malformed"));
    }
    let mut bytes = [0; 32];
    if !cell.copy_range_to(12, &mut bytes[..len]) {
        return Err(Error::Corrupt("variable test record is truncated"));
    }
    Ok((
        VarKey {
            bytes,
            len: len as u8,
        },
        u32_le(cell, 4),
    ))
}

fn variable_lookup(store: &MemoryStore, root: u32, key: VarKey) -> Result<Option<u32>> {
    if root == 0 {
        return Ok(None);
    }
    let mut page_number = root;
    let mut page = [0; PAGE_SIZE];
    loop {
        store.read(page_number, &mut page)?;
        let header = page::parse::<VariableCodec, _>(&page, store.target_txn, None)?;
        let (index, exists) =
            page::lower_bound::<VariableCodec, _>(&page, &header, key, header.level == 0)?;
        if header.level == 0 {
            return exists
                .then(|| {
                    let cell = VariableCodec::leaf_cell(&page, &header, index)?;
                    decode_variable(cell).map(|(_, value)| value)
                })
                .transpose();
        }
        page_number =
            page::branch_child::<VariableCodec, _>(&page, &header, index, store.page_limit())?;
    }
}

fn lookup(store: &MemoryStore, root: u32, key: u32) -> Result<Option<u32>> {
    if root == 0 {
        return Ok(None);
    }
    let mut page_number = root;
    let mut page = [0; PAGE_SIZE];
    loop {
        store.read(page_number, &mut page)?;
        let header = page::parse::<U32Codec, _>(&page, store.target_txn, None)?;
        if header.level == 0 {
            let (index, exists) = page::lower_bound::<U32Codec, _>(&page, &header, key, true)?;
            if !exists {
                return Ok(None);
            }
            let cell = slotted_page::cell(&page, &header, index, U32Codec::LEAF_SIZE)?;
            return Ok(Some(u32_le(cell, 4)));
        }
        let (index, _) = page::lower_bound::<U32Codec, _>(&page, &header, key, false)?;
        page_number = page::branch_child::<U32Codec, _>(&page, &header, index, store.page_limit())?;
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
fn one_shot_reads_visit_each_path_page_once_without_starting_a_scan() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in 0..1_000 {
        insert::<U32Codec, _>(
            &mut store,
            &mut root,
            &record(key, key + 10),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    let root_header =
        page::parse::<U32Codec, _>(&store.pages[root as usize], store.target_txn, None).unwrap();
    let path_pages = u64::from(root_header.level) + 1;

    store.inspections.set(0);
    let (found, work) = crate::work::measure(|| {
        predecessor::<U32Codec, _>(&store, root, 501)
            .unwrap()
            .unwrap()
    });
    assert_eq!(u32_le(&found, 0), 501);
    assert_eq!(store.inspections.get(), path_pages);
    assert_eq!(work.tree_lookups, 1);
    assert_eq!(work.tree_descents + 1, path_pages);
    assert_eq!(work.source_passes, 0);

    store.inspections.set(0);
    let (found, work) = crate::work::measure(|| {
        at_or_after::<U32Codec, _>(&store, root, 501)
            .unwrap()
            .unwrap()
    });
    assert_eq!(u32_le(&found, 0), 501);
    assert_eq!(store.inspections.get(), path_pages);
    assert_eq!(work.tree_lookups, 1);
    assert_eq!(work.tree_descents + 1, path_pages);
    assert_eq!(work.source_passes, 0);
}

#[test]
fn lower_bound_reuses_its_final_probe() {
    let header = slotted_page::Header {
        item_count: 8,
        level: 0,
        lower: 0,
        upper: 0,
    };
    let mut calls = 0;
    let (result, work) = crate::work::measure(|| {
        page::lower_bound_by::<U32Codec, _>(&header, 5, true, |index| {
            calls += 1;
            Ok(index as u32)
        })
    });
    assert_eq!(result.unwrap(), (5, true));
    assert_eq!(calls, 3);
    assert_eq!(work.key_probes, 3);
}

#[test]
fn fixed_search_rejects_a_forged_page_shape_before_slot_access() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    insert::<U32Codec, _>(
        &mut store,
        &mut root,
        &record(1, 1),
        &mut RetiredPages::new(),
    )
    .unwrap();
    let mut header =
        page::parse::<U32Codec, _>(&store.pages[root as usize], store.target_txn, None).unwrap();
    header.item_count += 1;
    assert!(
        page::lower_bound::<U32Codec, _>(&store.pages[root as usize], &header, 1, true,).is_err()
    );
}

#[test]
fn fixed_search_checks_each_persistent_slot_extent() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    insert::<U32Codec, _>(
        &mut store,
        &mut root,
        &record(1, 1),
        &mut RetiredPages::new(),
    )
    .unwrap();
    let header =
        page::parse::<U32Codec, _>(&store.pages[root as usize], store.target_txn, None).unwrap();
    store.pages[root as usize][slotted_page::HEADER_SIZE..slotted_page::HEADER_SIZE + 2]
        .copy_from_slice(&0u16.to_le_bytes());
    assert!(
        page::lower_bound::<U32Codec, _>(&store.pages[root as usize], &header, 1, true,).is_err()
    );
}

#[test]
fn fixed_replacement_uses_one_capacity_probe_and_no_slot_scan() {
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

    let (changed, work) = crate::work::measure(|| {
        insert::<U32Codec, _>(
            &mut store,
            &mut root,
            &record(500, 7),
            &mut RetiredPages::new(),
        )
    });
    assert!(!changed.unwrap());
    assert_eq!(work.edit_fit_probes, 1);
    assert_eq!(work.slot_scan_steps, 0);
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
    let (inserted, work) = crate::work::measure(|| {
        insert::<U32Codec, _>(&mut store, &mut root, &record(1_000, 99), &mut retired)
    });
    inserted.unwrap();
    assert_eq!(work.leaf_validations, 1);
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
fn private_local_insert_inspects_and_updates_without_copying_the_leaf() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in (0..1_000).map(|key| key * 2) {
        insert::<U32Codec, _>(
            &mut store,
            &mut root,
            &record(key, key),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    store.reads.set(0);
    store.inspections.set(0);
    store.writes = 0;
    store.updates = 0;

    let mut retired = RetiredPages::new();
    let result = insert_if_local_gap::<U32Codec, _, _>(
        &mut store,
        &mut root,
        &record(501, 7),
        &mut retired,
        &mut AcceptGap,
    )
    .unwrap();

    assert!(matches!(result, LocalInsert::Inserted(_)));
    assert!(retired.as_slice().is_empty());
    assert_eq!(store.inspections.get(), 2);
    assert_eq!(store.reads.get(), 0);
    assert_eq!(store.updates, 1);
    assert_eq!(store.writes, 0);
    assert_eq!(lookup(&store, root, 501).unwrap(), Some(7));
}

#[test]
fn cached_leaf_insert_accepts_only_a_valid_private_interior_gap() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in [0, 2, 4] {
        insert::<U32Codec, _>(
            &mut store,
            &mut root,
            &record(key, key),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    let (_, work) = crate::work::measure(|| {
        insert_if_cached_interior_gap::<U32Codec, _, _>(
            &mut store,
            root,
            &record(3, 3),
            &mut AcceptGap,
        )
    });
    assert_eq!(work.tree_lookups, 0);
    assert_eq!(lookup(&store, root, 3).unwrap(), Some(3));
    assert_eq!(
        insert_if_cached_interior_gap::<U32Codec, _, _>(
            &mut store,
            root,
            &record(5, 5),
            &mut AcceptGap,
        )
        .unwrap(),
        CachedInsert::Miss
    );

    store.target_txn = 2;
    assert!(insert_if_cached_interior_gap::<U32Codec, _, _>(
        &mut store,
        root,
        &record(1, 1),
        &mut AcceptGap,
    )
    .is_err());
    store.target_txn = 1;

    store.pages[root as usize][slotted_page::HEADER_SIZE..slotted_page::HEADER_SIZE + 2]
        .copy_from_slice(&0u16.to_le_bytes());
    assert!(insert_if_cached_interior_gap::<U32Codec, _, _>(
        &mut store,
        root,
        &record(1, 1),
        &mut AcceptGap,
    )
    .is_err());
}

#[test]
fn private_tree_release_visits_every_page_once() {
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
    let page_count = store.pages.len() as u32;

    discard_private_tree::<U32Codec, _, _>(&mut store, root, || Ok(())).unwrap();
    store.discarded.sort_unstable();
    assert_eq!(store.discarded, (2..page_count).collect::<Vec<_>>());
}

#[test]
fn consuming_cursor_reads_in_order_without_per_record_tree_lookups() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in (0..1_000).rev() {
        insert::<U32Codec, _>(
            &mut store,
            &mut root,
            &record(key, key),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    let page_count = store.pages.len() as u32;
    store.discarded.clear();

    let (values, work) = crate::work::measure(|| {
        let mut cursor =
            Cursor::<U32Codec>::new_consuming(&mut store, root, CursorDirection::Forward).unwrap();
        let mut values = Vec::new();
        while let Some(cell) = cursor.next_consuming(&mut store, &mut U32Item).unwrap() {
            values.push((u32_le(&cell, 0), u32_le(&cell, 4)));
        }
        values
    });

    assert_eq!(values, (0..1_000).map(|key| (key, key)).collect::<Vec<_>>());
    assert_eq!(work.tree_lookups, 0);
    assert_eq!(work.source_passes, 1);
    store.discarded.sort_unstable();
    assert_eq!(store.discarded, (2..page_count).collect::<Vec<_>>());
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
fn known_deletion_uses_one_tree_lookup() {
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

    let (result, work) = crate::work::measure(|| {
        delete_existing::<U32Codec, _>(&mut store, &mut root, 451, &mut RetiredPages::new())
    });
    result.unwrap();
    assert_eq!(work.tree_lookups, 1);
    assert_eq!(lookup(&store, root, 451).unwrap(), None);
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
    let header = page::parse::<WideCodec, _>(&page, store.target_txn, None).unwrap();
    assert_eq!(header.level, 2);
    assert_eq!(
        predecessor::<WideCodec, _>(&store, root, wide_key(4_999))
            .unwrap()
            .unwrap()
            .as_slice(),
        wide_record(4_999)
    );
}

#[test]
fn variable_records_split_replace_and_delete_without_padding() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    for key in (0..3_000).rev() {
        insert::<VariableCodec, _>(
            &mut store,
            &mut root,
            &variable_record(key, key + 1),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    for key in 0..3_000 {
        assert_eq!(
            variable_lookup(&store, root, variable_key(key)).unwrap(),
            Some(key + 1)
        );
    }

    for key in (0..3_000).step_by(3) {
        delete::<VariableCodec, _>(
            &mut store,
            &mut root,
            variable_key(key),
            &mut RetiredPages::new(),
        )
        .unwrap();
    }
    for key in 0..3_000 {
        let expected = (key % 3 != 0).then_some(key + 1);
        assert_eq!(
            variable_lookup(&store, root, variable_key(key)).unwrap(),
            expected
        );
    }
}
