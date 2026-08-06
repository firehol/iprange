use super::*;
use crate::contract::PAGE_SIZE;

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
                .ok_or(Error::Corrupt("test membership page is out of bounds"))?,
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
                .ok_or(Error::Corrupt("test membership page is out of bounds"))?,
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

impl RetiringStore for MemoryStore {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        self.retired.extend_from_slice(pages);
        Ok(())
    }
}

struct Pattern {
    word_count: u32,
    salt: u64,
}

impl Words<MemoryStore> for Pattern {
    fn word_count(&self) -> u32 {
        self.word_count
    }

    fn read_words(&self, _store: &MemoryStore, start: u32, output: &mut [u64]) -> Result<()> {
        for (offset, word) in output.iter_mut().enumerate() {
            let index = u64::from(start) + offset as u64;
            *word = index
                .wrapping_mul(0x9e37_79b9_7f4a_7c15)
                .rotate_left((index % 63) as u32)
                ^ self.salt;
        }
        Ok(())
    }
}

fn empty_state() -> State {
    State {
        id_root: 0,
        hash_root: 0,
        used_root: 0,
        entry_count: 0,
        id_limit: 1,
    }
}

#[test]
fn inline_and_blob_values_deduplicate_reuse_ids_and_release_pages() {
    let mut store = MemoryStore::new();
    let mut state = empty_state();
    let small = Pattern {
        word_count: 4,
        salt: 11,
    };
    let large = Pattern {
        word_count: 700,
        salt: 29,
    };

    let first = intern(&mut store, &mut state, &small).unwrap();
    let blob = intern(&mut store, &mut state, &large).unwrap();
    assert!(first.created && blob.created);
    assert_ne!(first.id, blob.id);
    assert_eq!(intern(&mut store, &mut state, &large).unwrap().id, blob.id);
    assert!(matches!(
        find_record(&store, state.id_root, first.id)
            .unwrap()
            .unwrap()
            .record
            .storage,
        Storage::Inline
    ));
    assert!(matches!(
        find_record(&store, state.id_root, blob.id)
            .unwrap()
            .unwrap()
            .record
            .storage,
        Storage::Blob(_)
    ));

    let mut actual = [0; 80];
    let mut expected = [0; 80];
    read_words(&store, state.id_root, blob.id, 503, &mut actual).unwrap();
    large.read_words(&store, 503, &mut expected).unwrap();
    assert_eq!(actual, expected);

    apply_delta(
        &mut store,
        &mut state,
        Delta {
            id: first.id,
            change: 1,
        },
    )
    .unwrap();
    apply_delta(
        &mut store,
        &mut state,
        Delta {
            id: blob.id,
            change: 1,
        },
    )
    .unwrap();
    for id in [first.id, blob.id] {
        let found = find_record(&store, state.id_root, id).unwrap().unwrap();
        assert_eq!(found.record.refcount, 1);
    }
    apply_delta(
        &mut store,
        &mut state,
        Delta {
            id: first.id,
            change: -1,
        },
    )
    .unwrap();

    let replacement = Pattern {
        word_count: 3,
        salt: 47,
    };
    assert_eq!(
        intern(&mut store, &mut state, &replacement).unwrap().id,
        first.id
    );
    apply_delta(
        &mut store,
        &mut state,
        Delta {
            id: blob.id,
            change: -1,
        },
    )
    .unwrap();
    assert!(!store.discarded.is_empty());
}

#[test]
fn added_bit_extends_across_a_word_boundary() {
    let mut store = MemoryStore::new();
    let mut state = empty_state();
    let first = intern_added_bit(&mut store, &mut state, 0, 0, 63).unwrap();
    let second = intern_added_bit(&mut store, &mut state, first.id, first.word_count, 64).unwrap();
    assert_eq!(first.word_count, 1);
    assert_eq!(second.word_count, 2);

    let mut words = [0; 2];
    read_words(&store, state.id_root, second.id, 0, &mut words).unwrap();
    assert_eq!(words, [1 << 63, 1]);
    let duplicate =
        intern_added_bit(&mut store, &mut state, second.id, second.word_count, 64).unwrap();
    assert_eq!(duplicate.id, second.id);
    assert!(!duplicate.created);
}

#[test]
fn reverse_lookup_full_compares_equal_hash_candidates() {
    let mut store = MemoryStore::new();
    let mut state = empty_state();
    let stored = Pattern {
        word_count: 8,
        salt: 3,
    };
    let wanted = Pattern {
        word_count: 8,
        salt: 5,
    };
    let digest = hash_words(&store, &wanted).unwrap();
    let fake_id = allocate_id(&mut store, &mut state).unwrap();
    let record = encode_record(&mut store, &stored, fake_id, digest).unwrap();
    mutate_insert::<IdCodec, _>(&mut store, &mut state.id_root, record.as_slice()).unwrap();
    mutate_insert::<HashCodec, _>(
        &mut store,
        &mut state.hash_root,
        &encode_hash(HashKey {
            digest,
            word_count: wanted.word_count,
            id: fake_id,
        }),
    )
    .unwrap();
    state.entry_count = 1;

    let interned = intern(&mut store, &mut state, &wanted).unwrap();
    assert!(interned.created);
    assert_ne!(interned.id, fake_id);
    apply_delta(
        &mut store,
        &mut state,
        Delta {
            id: fake_id,
            change: 0,
        },
    )
    .unwrap();
}

#[test]
fn committed_blob_pages_are_retired_without_being_overwritten() {
    let mut store = MemoryStore::new();
    let mut state = empty_state();
    let value = Pattern {
        word_count: 900,
        salt: 71,
    };
    let interned = intern(&mut store, &mut state, &value).unwrap();
    apply_delta(
        &mut store,
        &mut state,
        Delta {
            id: interned.id,
            change: 1,
        },
    )
    .unwrap();
    let found = find_record(&store, state.id_root, interned.id)
        .unwrap()
        .unwrap();
    let Storage::Blob(blob_root) = found.record.storage else {
        panic!("large membership was not stored as a blob");
    };
    let committed = store.pages.clone();

    store.txn = 2;
    apply_delta(
        &mut store,
        &mut state,
        Delta {
            id: interned.id,
            change: -1,
        },
    )
    .unwrap();
    assert_eq!(state.entry_count, 0);
    assert_eq!(state.id_limit, 1);
    assert!(store.retired.contains(&blob_root));
    assert_eq!(&store.pages[..committed.len()], committed.as_slice());
}
