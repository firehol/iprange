use super::*;
use crate::contract::{StructureKind, PAGE_SIZE};
use crate::fixed_tree::Store;
use crate::mapping::ByteSource;
use crate::structured_value::codec::{encode, encode_hash, HashCodec, HashKey};

struct MemoryStore {
    pages: Vec<[u8; PAGE_SIZE]>,
}

impl MemoryStore {
    fn new() -> Self {
        Self {
            pages: vec![[0; PAGE_SIZE]; 2],
        }
    }
}

impl Store for MemoryStore {
    type ReadPage<'a> = &'a [u8; PAGE_SIZE];
    type WritePage<'a> = [u8; PAGE_SIZE];

    fn target_txn(&self) -> u64 {
        1
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
                .ok_or(Error::Corrupt("test structure page is out of bounds"))?,
        )
    }

    fn allocate(&mut self) -> Result<u32> {
        let page = self.pages.len() as u32;
        self.pages.push([0; PAGE_SIZE]);
        Ok(page)
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        update(
            self.pages
                .get_mut(page_number as usize)
                .ok_or(Error::Corrupt("test structure page is out of bounds"))?,
        )
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        crate::test_support_tests::copy_pages(&mut self.pages, source, destination, copy)
    }

    fn discard_private(&mut self, _page_number: u32) -> Result<()> {
        Ok(())
    }
}

impl RetiringStore for MemoryStore {
    fn retire_pages(&mut self, _pages: &[u32]) -> Result<()> {
        Ok(())
    }
}

struct TestCodec;

impl PayloadCodec for TestCodec {
    const KIND: StructureKind = StructureKind::NetworkEnrichmentV1;
    const PAYLOAD_SIZE: usize = 4;

    fn validate<S: ByteSource>(payload: S) -> Result<()> {
        if payload.len() == Self::PAYLOAD_SIZE {
            Ok(())
        } else {
            Err(Error::Corrupt("test structure payload is malformed"))
        }
    }

    fn membership_id(_payload: &Payload) -> u32 {
        0
    }

    fn is_absent(payload: &Payload) -> bool {
        payload.as_slice().iter().all(|byte| *byte == 0)
    }
}

fn state() -> State {
    State {
        id_root: 0,
        hash_root: 0,
        used_root: 0,
        entry_count: 0,
        id_limit: 1,
    }
}

fn payload(bytes: [u8; 4]) -> Payload {
    Payload::new(&bytes).unwrap()
}

#[test]
fn equal_payloads_deduplicate_and_lowest_released_id_is_reused() {
    let mut store = MemoryStore::new();
    let mut state = state();
    let first = intern::<TestCodec, _>(&mut store, &mut state, payload([1, 2, 3, 4])).unwrap();
    let same = intern::<TestCodec, _>(&mut store, &mut state, payload([1, 2, 3, 4])).unwrap();
    let second = intern::<TestCodec, _>(&mut store, &mut state, payload([5, 6, 7, 8])).unwrap();
    assert_eq!(same.id, first.id);
    assert!(!same.created);
    assert_ne!(second.id, first.id);

    for id in [first.id, second.id] {
        apply_delta::<TestCodec, _>(&mut store, &mut state, Delta { id, change: 1 }).unwrap();
    }
    apply_delta::<TestCodec, _>(
        &mut store,
        &mut state,
        Delta {
            id: first.id,
            change: -1,
        },
    )
    .unwrap();
    let replacement =
        intern::<TestCodec, _>(&mut store, &mut state, payload([9, 10, 11, 12])).unwrap();
    assert_eq!(replacement.id, first.id);
}

#[test]
fn equal_digest_with_unequal_payload_does_not_merge() {
    let mut store = MemoryStore::new();
    let mut state = state();
    let wanted = payload([1, 2, 3, 4]);
    let digest = payload_digest::<TestCodec>(&wanted).unwrap();
    let fake_id = allocate_id(&mut store, &mut state).unwrap();
    let (record, len) = encode::<TestCodec>(fake_id, digest, payload([5, 6, 7, 8])).unwrap();
    table::insert::<TestCodec, _>(
        &mut store,
        &mut state.id_root,
        state.id_limit,
        &record[..len],
    )
    .unwrap();
    insert::<HashCodec<TestCodec>, _>(
        &mut store,
        &mut state.hash_root,
        &encode_hash(HashKey {
            digest,
            id: fake_id,
        }),
    )
    .unwrap();
    state.entry_count = 1;

    let actual = intern::<TestCodec, _>(&mut store, &mut state, wanted).unwrap();
    assert!(actual.created);
    assert_ne!(actual.id, fake_id);
}

#[test]
fn id_and_hash_indexes_remain_exact_after_branch_growth() {
    let mut store = MemoryStore::new();
    let mut state = state();
    let mut ids = Vec::new();
    for value in 1u32..=512 {
        let interned =
            intern::<TestCodec, _>(&mut store, &mut state, payload(value.to_le_bytes())).unwrap();
        assert!(interned.created);
        ids.push((interned.id, value));
    }

    assert_eq!(state.entry_count, 512);
    for (id, value) in ids {
        let record = find::<TestCodec, _>(&store, state.id_root, state.id_limit, id)
            .unwrap()
            .unwrap();
        assert_eq!(record.payload, payload(value.to_le_bytes()));
        let duplicate =
            intern::<TestCodec, _>(&mut store, &mut state, payload(value.to_le_bytes())).unwrap();
        assert_eq!(duplicate.id, id);
        assert!(!duplicate.created);
    }
}

#[test]
fn direct_table_root_shrinks_when_trailing_ids_are_released() {
    let mut store = MemoryStore::new();
    let mut state = state();
    let count = table::leaf_slots::<TestCodec>() + 2;
    for value in 1..=count as u32 {
        let interned =
            intern::<TestCodec, _>(&mut store, &mut state, payload(value.to_le_bytes())).unwrap();
        apply_delta::<TestCodec, _>(
            &mut store,
            &mut state,
            Delta {
                id: interned.id,
                change: 1,
            },
        )
        .unwrap();
    }
    assert_eq!(
        store
            .inspect_page(state.id_root, |page| {
                Ok(table::parse::<TestCodec, _>(page, 1, None)?.level)
            })
            .unwrap(),
        1
    );

    for id in ((table::leaf_slots::<TestCodec>() as u32)..=count as u32).rev() {
        apply_delta::<TestCodec, _>(&mut store, &mut state, Delta { id, change: -1 }).unwrap();
    }
    assert_eq!(state.id_limit, table::leaf_slots::<TestCodec>() as u64);
    assert_eq!(
        store
            .inspect_page(state.id_root, |page| {
                Ok(table::parse::<TestCodec, _>(page, 1, None)?.level)
            })
            .unwrap(),
        0
    );
}
