//! Ordered range-assignment tests against a scalar reference map.

use std::cell::Cell;

use super::*;
use crate::contract::PAGE_SIZE;
use crate::key::{Ipv4Key, Ipv6Key};

struct MemoryStore {
    target_txn: u64,
    pages: Vec<[u8; PAGE_SIZE]>,
    retired: Vec<u32>,
    discarded: Vec<u32>,
    reads: Cell<u64>,
    writes: u64,
}

impl MemoryStore {
    fn new() -> Self {
        Self {
            target_txn: 1,
            pages: vec![[0; PAGE_SIZE]; 2],
            retired: Vec::new(),
            discarded: Vec::new(),
            reads: Cell::new(0),
            writes: 0,
        }
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
        self.reads.set(self.reads.get() + 1);
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
        self.writes += 1;
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
        crate::test_support_tests::copy_pages(&mut self.pages, source, destination, copy)
    }

    fn discard_private(&mut self, page_number: u32) -> Result<()> {
        self.discarded.push(page_number);
        Ok(())
    }
}

impl crate::fixed_tree::RetiringStore for MemoryStore {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        self.retired.extend_from_slice(pages);
        Ok(())
    }
}

impl RangeStore for MemoryStore {
    fn range_record_added(&mut self, _value: u32) -> Result<()> {
        Ok(())
    }

    fn range_record_removed(&mut self, _value: u32) -> Result<()> {
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
fn private_coverage_union_matches_a_scalar_reference() {
    const SPACE: usize = 512;
    let mut expected = [false; SPACE];
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    let mut state = UnionState::default();
    let mut random = 0x243f_6a88_u32;

    for operation in 0..2_000 {
        random = random.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);
        let first = random as usize % SPACE;
        random = random.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);
        let second = random as usize % SPACE;
        let from = first.min(second);
        let to = first.max(second);
        union_private(
            &mut store,
            &mut root,
            &mut count,
            Ipv4Key(from as u32),
            Ipv4Key(to as u32),
            &mut state,
        )
        .unwrap();
        expected[from..=to].fill(true);

        let actual = ranges_v4(&store, root);
        assert_eq!(count as usize, actual.len());
        for (address, wanted) in expected.iter().enumerate() {
            let found = actual
                .iter()
                .any(|range| range.0 <= address as u32 && address as u32 <= range.1);
            assert_eq!(found, *wanted, "operation {operation}, address {address}");
        }
        assert!(actual.iter().all(|range| range.2 == 1));
        assert!(actual.windows(2).all(|pair| pair[0].1 + 1 < pair[1].0));
    }
}

#[test]
fn private_coverage_union_reuses_monotonic_edges() {
    for descending in [false, true] {
        let mut store = MemoryStore::new();
        let mut root = 0;
        let mut count = 0;
        let mut state = UnionState::default();
        let (_, work) = crate::work::measure(|| {
            for ordinal in 0..2_000_u32 {
                let key = if descending { 1_999 - ordinal } else { ordinal } * 4;
                union_private(
                    &mut store,
                    &mut root,
                    &mut count,
                    Ipv4Key(key),
                    Ipv4Key(key + 1),
                    &mut state,
                )
                .unwrap();
            }
        });
        assert_eq!(count, 2_000);
        assert!(work.pages_split > 0);
        assert_eq!(
            work.tree_lookups, work.pages_split,
            "only split refreshes may descend a monotonic tree"
        );
    }
}

#[test]
fn private_coverage_union_random_order_bounds_lookups_by_inputs_and_splits() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    let mut state = UnionState::default();
    let (_, work) = crate::work::measure(|| {
        for ordinal in 0..2_000_u32 {
            let key = (ordinal * 1_597 % 2_000) * 4;
            union_private(
                &mut store,
                &mut root,
                &mut count,
                Ipv4Key(key),
                Ipv4Key(key + 1),
                &mut state,
            )
            .unwrap();
        }
    });
    assert_eq!(count, 2_000);
    assert!(
        work.tree_lookups <= 2_000 + work.pages_split,
        "random union repeated tree searches: {work:?}"
    );
}

#[test]
fn private_coverage_union_splices_a_large_run_without_per_record_searches() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    let mut state = UnionState::default();
    for ordinal in 0..2_000_u32 {
        let key = ordinal * 4;
        union_private(
            &mut store,
            &mut root,
            &mut count,
            Ipv4Key(key),
            Ipv4Key(key + 1),
            &mut state,
        )
        .unwrap();
    }

    let (changed, work) = crate::work::measure(|| {
        union_private(
            &mut store,
            &mut root,
            &mut count,
            Ipv4Key(0),
            Ipv4Key(8_000),
            &mut state,
        )
    });
    assert!(changed.unwrap());
    assert_eq!(count, 1);
    assert_eq!(ranges_v4(&store, root), vec![(0, 8_000, 1)]);
    assert_eq!(work.ranges_coalesced, 2_000);
    assert_eq!(work.tree_lookups, 8, "one lookup per affected leaf");
    assert!(work.cell_probes < 2_200, "records were rescanned");
}

#[test]
fn big_endian_portable_range_record_matches_literal_bytes() {
    let range = Range {
        from: Ipv4Key(0x0102_0304),
        to: Ipv4Key(0x0506_0708),
        value: 0x090a_0b0c,
    };
    let encoded = EncodedRange::new(range).unwrap();
    assert_eq!(
        encoded.as_slice(),
        &[4, 3, 2, 1, 8, 7, 6, 5, 0x0c, 0x0b, 0x0a, 9]
    );

    let decoded = decode_cell::<Ipv4Key>(encoded.as_slice()).unwrap();
    assert_eq!(decoded.from, range.from);
    assert_eq!(decoded.to, range.to);
    assert_eq!(decoded.value, range.value);
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

    let (cleared, work) =
        crate::work::measure(|| clear(&mut store, &mut root, &mut count, Ipv4Key(40), Ipv4Key(60)));
    assert!(cleared.unwrap());
    assert_eq!(work.ranges_split, 1);
    assert_eq!(work.ranges_coalesced, 0);
    assert_eq!(work.ranges_emitted, 2);
    assert_eq!(ranges_v4(&store, root), vec![(0, 39, 7), (61, 100, 7)]);
    assert_eq!(count, 2);

    assert!(!clear(&mut store, &mut root, &mut count, Ipv4Key(40), Ipv4Key(60),).unwrap());
    assert_eq!(ranges_v4(&store, root), vec![(0, 39, 7), (61, 100, 7)]);
    assert_eq!(count, 2);

    let (assigned, work) = crate::work::measure(|| {
        assign(
            &mut store,
            &mut root,
            &mut count,
            Ipv4Key(40),
            Ipv4Key(60),
            7,
        )
    });
    assert!(assigned.unwrap());
    assert_eq!(work.ranges_split, 0);
    assert_eq!(work.ranges_coalesced, 2);
    assert_eq!(work.ranges_emitted, 1);
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
fn transforms_match_scalar_state_after_each_non_idempotent_operation() {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut count = 0;
    let mut expected = [None; 256];
    let mut random = 0x9e37_79b9u32;

    for step in 0..200 {
        random = random.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);
        let first = (random & 255) as usize;
        random = random.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);
        let second = (random & 255) as usize;
        let from = first.min(second);
        let to = first.max(second);
        let mode = step % 4;
        transform(
            &mut store,
            &mut root,
            &mut count,
            Ipv4Key(from as u32),
            Ipv4Key(to as u32),
            |_, old| Ok(mapped(old, mode)),
        )
        .unwrap();
        for value in &mut expected[from..=to] {
            *value = mapped(*value, mode);
        }
        for (address, wanted) in expected.iter().enumerate() {
            let actual = read_predecessor::<Ipv4Key, _>(&store, root, Ipv4Key(address as u32))
                .unwrap()
                .filter(|range| range.to.0 >= address as u32)
                .map(|range| range.value);
            assert_eq!(actual, *wanted, "step {step}, address {address}");
        }
    }
}

fn mapped(value: Option<u32>, mode: usize) -> Option<u32> {
    match mode {
        0 => value.map(|value| value ^ 3).filter(|value| *value != 0),
        1 => Some(value.unwrap_or(0) | 4),
        2 => value.filter(|value| *value != 7),
        _ => value.map_or(Some(9), |_| None),
    }
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
    let (_, work) = crate::work::measure(|| {
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
    });
    assert!(work.pages_split > 0);
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

#[test]
fn nested_assignment_page_work_is_not_quadratic() {
    let (small, _) = nested_assignment_work(512);
    let (large, work) = nested_assignment_work(1_024);
    assert!(
        large <= small * 3,
        "doubling input grew deterministic page work from {small} to {large}"
    );
    assert_eq!(work.tree_lookups, 1_023);
}

fn nested_assignment_work(count: u32) -> (u64, crate::work::Snapshot) {
    let mut store = MemoryStore::new();
    let mut root = 0;
    let mut records = 0;
    let end = count * 4 + 1;
    let (_, work) = crate::work::measure(|| {
        for index in 0..count {
            assign_private(
                &mut store,
                &mut root,
                &mut records,
                Ipv4Key(index),
                Ipv4Key(end - index),
                index % 2 + 1,
            )
            .unwrap();
        }
    });
    (store.reads.get() + store.writes, work)
}
