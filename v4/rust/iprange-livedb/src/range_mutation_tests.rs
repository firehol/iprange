//! Ordered range-assignment tests against a scalar reference map.

use super::*;
use crate::key::{Ipv4Key, Ipv6Key};

struct MemoryStore {
    target_txn: u64,
    pages: Vec<[u8; PAGE_SIZE]>,
    retired: Vec<u32>,
    discarded: Vec<u32>,
}

impl MemoryStore {
    fn new() -> Self {
        Self {
            target_txn: 1,
            pages: vec![[0; PAGE_SIZE]; 2],
            retired: Vec::new(),
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
        *page = *self
            .pages
            .get(page_number as usize)
            .ok_or(Error::Corrupt("test page is out of bounds"))?;
        Ok(())
    }

    fn allocate(&mut self) -> Result<u32> {
        let page_number = u32::try_from(self.pages.len())
            .map_err(|_| Error::InvalidArgument("test page space exhausted"))?;
        self.pages.push([0; PAGE_SIZE]);
        Ok(page_number)
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

impl RangeStore for MemoryStore {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        self.retired.extend_from_slice(pages);
        Ok(())
    }
}

fn ranges_v4(store: &MemoryStore, root: u32) -> Vec<(u32, u32, u32)> {
    let mut result = Vec::new();
    let mut key = Ipv4Key::MIN;
    while let Some(range) = read_at_or_after::<Ipv4Key, _>(store, root, key).unwrap() {
        result.push((range.from.0, range.to.0, range.value));
        let Some(next) = range.from.checked_next() else {
            break;
        };
        key = next;
    }
    result
}

fn ranges_v6(store: &MemoryStore, root: u32) -> Vec<(u128, u128, u32)> {
    let mut result = Vec::new();
    let mut key = Ipv6Key::MIN;
    while let Some(range) = read_at_or_after::<Ipv6Key, _>(store, root, key).unwrap() {
        result.push((range.from.to_u128(), range.to.to_u128(), range.value));
        let Some(next) = range.from.checked_next() else {
            break;
        };
        key = next;
    }
    result
}

#[test]
fn overlapping_ranges_apply_in_arrival_order() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;

    assert!(assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(0),
        Ipv4Key(100),
        1,
    )
    .unwrap());
    assert!(assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(20),
        Ipv4Key(30),
        2,
    )
    .unwrap());
    assert_eq!(
        ranges_v4(&store, root),
        vec![(0, 19, 1), (20, 30, 2), (31, 100, 1)]
    );

    assert!(assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(25),
        Ipv4Key(120),
        3,
    )
    .unwrap());
    assert!(assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(121),
        Ipv4Key(130),
        3,
    )
    .unwrap());
    assert_eq!(
        ranges_v4(&store, root),
        vec![(0, 19, 1), (20, 24, 2), (25, 130, 3)]
    );
    assert_eq!(count, 3);

    assert!(!assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(40),
        Ipv4Key(50),
        3,
    )
    .unwrap());
    assert_eq!(count, 3);
}

#[test]
fn clear_splits_and_coalesces_without_touching_absent_space() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(0),
        Ipv4Key(100),
        7,
    )
    .unwrap();

    assert!(clear(&mut store, &mut root, &mut count, Ipv4Key(40), Ipv4Key(60),).unwrap());
    assert_eq!(ranges_v4(&store, root), vec![(0, 39, 7), (61, 100, 7)]);
    assert_eq!(count, 2);

    assert!(!clear(&mut store, &mut root, &mut count, Ipv4Key(40), Ipv4Key(60),).unwrap());
    assert_eq!(ranges_v4(&store, root), vec![(0, 39, 7), (61, 100, 7)]);
    assert_eq!(count, 2);

    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(40),
        Ipv4Key(60),
        7,
    )
    .unwrap();
    assert_eq!(ranges_v4(&store, root), vec![(0, 100, 7)]);
    assert_eq!(count, 1);
}

#[test]
fn endpoint_arithmetic_handles_both_full_address_spaces() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key::MIN,
        Ipv4Key::MAX,
        11,
    )
    .unwrap();
    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(1),
        Ipv4Key(u32::MAX - 1),
        12,
    )
    .unwrap();
    assert_eq!(
        ranges_v4(&store, root),
        vec![(0, 0, 11), (1, u32::MAX - 1, 12), (u32::MAX, u32::MAX, 11),]
    );

    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv6Key::MIN,
        Ipv6Key::MAX,
        21,
    )
    .unwrap();
    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv6Key::from_u128(1),
        Ipv6Key::from_u128(u128::MAX - 1),
        22,
    )
    .unwrap();
    assert_eq!(
        ranges_v6(&store, root),
        vec![
            (0, 0, 21),
            (1, u128::MAX - 1, 22),
            (u128::MAX, u128::MAX, 21),
        ]
    );
}

#[test]
fn randomized_sequence_matches_a_scalar_reference_map() {
    const SPACE: usize = 256;
    let mut expected = [None; SPACE];
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    let mut random = 0x6d2b_79f5_u32;

    for operation in 0..1_000 {
        random = random.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);
        let a = (random as usize) % SPACE;
        random = random.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);
        let b = (random as usize) % SPACE;
        let from = a.min(b);
        let to = a.max(b);
        random = random.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);

        if operation % 4 == 0 {
            clear(
                &mut store,
                &mut root,
                &mut count,
                Ipv4Key(from as u32),
                Ipv4Key(to as u32),
            )
            .unwrap();
            expected[from..=to].fill(None);
        } else {
            let value = random % 7;
            assign(
                &mut store,
                &mut root,
                &mut count,
                Ipv4Key(from as u32),
                Ipv4Key(to as u32),
                value,
            )
            .unwrap();
            expected[from..=to].fill(Some(value));
        }

        let actual = ranges_v4(&store, root);
        assert_eq!(count as usize, actual.len());
        for (address, wanted) in expected.iter().enumerate() {
            let found = actual
                .iter()
                .find(|range| range.0 <= address as u32 && address as u32 <= range.1)
                .map(|range| range.2);
            assert_eq!(found, *wanted, "operation {operation}, address {address}");
        }
        for pair in actual.windows(2) {
            assert!(pair[0].1 < pair[1].0);
            assert!(pair[0].1.checked_add(1) != Some(pair[1].0) || pair[0].2 != pair[1].2);
        }
    }
}

#[test]
fn many_disjoint_ranges_split_leaves_and_cow_only_once_per_path() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    for key in (0..2_000_u32).rev() {
        assign(
            &mut store,
            &mut root,
            &mut count,
            Ipv4Key(key * 2),
            Ipv4Key(key * 2),
            key,
        )
        .unwrap();
    }
    assert_eq!(count, 2_000);
    let committed = store.pages.clone();
    store.target_txn = 2;

    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(8_000),
        Ipv4Key(8_000),
        9,
    )
    .unwrap();
    assert!(!store.retired.is_empty());
    assert_eq!(&store.pages[..committed.len()], committed.as_slice());

    let retired_after_first_write = store.retired.len();
    assign(
        &mut store,
        &mut root,
        &mut count,
        Ipv4Key(8_002),
        Ipv4Key(8_002),
        10,
    )
    .unwrap();
    assert_eq!(store.retired.len(), retired_after_first_write);
    assert_eq!(count, 2_002);
}
