//! Range-tree point-lookup tests.

use super::*;
use crate::bootstrap;
use crate::contract::{AddressFamily, MetaV4};
use crate::database::ImmutableReader;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_cursor::{DirectRange, RangeDirection};
use crate::test_alloc::count_thread_allocations;
use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

struct TestPath(PathBuf);

impl TestPath {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-range-{label}-{}-{unique}",
            std::process::id()
        )))
    }
}

impl Drop for TestPath {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

fn meta(family: AddressFamily, page_count: u64, root: u32, records: u64) -> MetaV4 {
    let mut meta = bootstrap::tests::empty_direct_meta(1);
    meta.address_family = family;
    meta.page_count = page_count;
    meta.range_root = root;
    meta.range_record_count = records;
    meta
}

fn fixture(meta: MetaV4, pages: &[(u32, [u8; PAGE_SIZE])]) -> (ImmutableReader, TestPath) {
    let path = TestPath::new("fixture");
    let mut image = vec![0; meta.page_count as usize * PAGE_SIZE];
    meta.encode_into((&mut image[..PAGE_SIZE]).try_into().unwrap());
    meta.encode_into((&mut image[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
    for (number, page) in pages {
        let start = *number as usize * PAGE_SIZE;
        image[start..start + PAGE_SIZE].copy_from_slice(page);
    }
    fs::write(&path.0, image).unwrap();
    let reader = ImmutableReader::open(&path.0).unwrap();
    (reader, path)
}

fn slotted_page(
    page_type: u8,
    level: u16,
    family: AddressFamily,
    cells: &[Vec<u8>],
) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = page_type;
    page[6..8].copy_from_slice(&(HEADER_SIZE as u16).to_le_bytes());
    page[8..16].copy_from_slice(&1u64.to_le_bytes());
    page[16..18].copy_from_slice(&(cells.len() as u16).to_le_bytes());
    page[18..20].copy_from_slice(&level.to_le_bytes());
    let lower = HEADER_SIZE + cells.len() * 2;
    page[20..22].copy_from_slice(&(lower as u16).to_le_bytes());
    page[24..28].copy_from_slice(&(family as u32).to_le_bytes());

    let mut upper = PAGE_SIZE;
    for (index, cell) in cells.iter().enumerate() {
        upper -= cell.len();
        page[upper..upper + cell.len()].copy_from_slice(cell);
        let slot = HEADER_SIZE + index * 2;
        page[slot..slot + 2].copy_from_slice(&(upper as u16).to_le_bytes());
    }
    page[22..24].copy_from_slice(&(upper as u16).to_le_bytes());
    page
}

fn v4_leaf(ranges: &[(u32, u32, u32)]) -> [u8; PAGE_SIZE] {
    let cells: Vec<Vec<u8>> = ranges
        .iter()
        .map(|&(from, to, value)| {
            let mut cell = Vec::with_capacity(12);
            cell.extend_from_slice(&from.to_le_bytes());
            cell.extend_from_slice(&to.to_le_bytes());
            cell.extend_from_slice(&value.to_le_bytes());
            cell
        })
        .collect();
    slotted_page(RANGE_LEAF, 0, AddressFamily::Ipv4, &cells)
}

fn v4_branch(entries: &[(u32, u32)]) -> [u8; PAGE_SIZE] {
    let cells: Vec<Vec<u8>> = entries
        .iter()
        .map(|&(first, child)| {
            let mut cell = Vec::with_capacity(8);
            cell.extend_from_slice(&first.to_le_bytes());
            cell.extend_from_slice(&child.to_le_bytes());
            cell
        })
        .collect();
    slotted_page(RANGE_BRANCH, 1, AddressFamily::Ipv4, &cells)
}

fn v6_leaf(ranges: &[(Ipv6Key, Ipv6Key, u32)]) -> [u8; PAGE_SIZE] {
    let cells: Vec<Vec<u8>> = ranges
        .iter()
        .map(|&(from, to, value)| {
            let mut cell = Vec::with_capacity(36);
            put_v6(&mut cell, from);
            put_v6(&mut cell, to);
            cell.extend_from_slice(&value.to_le_bytes());
            cell
        })
        .collect();
    slotted_page(RANGE_LEAF, 0, AddressFamily::Ipv6, &cells)
}

fn put_v6(output: &mut Vec<u8>, key: Ipv6Key) {
    output.extend_from_slice(&key.lo.to_le_bytes());
    output.extend_from_slice(&key.hi.to_le_bytes());
}

#[test]
fn ipv4_leaf_lookup_handles_gaps_boundaries_and_zero() {
    let leaf = v4_leaf(&[(0, 0, 0), (10, 20, 42), (u32::MAX, u32::MAX, 7)]);
    let (reader, _path) = fixture(meta(AddressFamily::Ipv4, 3, 2, 3), &[(2, leaf)]);

    assert_eq!(reader.lookup_direct_v4(Ipv4Key(0)).unwrap(), Some(0));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(9)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(42));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(20)).unwrap(), Some(42));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(21)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key::MAX).unwrap(), Some(7));
}

#[test]
fn full_space_ipv6_lookup_does_not_overflow() {
    let leaf = v6_leaf(&[(Ipv6Key::MIN, Ipv6Key::MAX, 9)]);
    let (reader, _path) = fixture(meta(AddressFamily::Ipv6, 3, 2, 1), &[(2, leaf)]);

    assert_eq!(reader.lookup_direct_v6(Ipv6Key::MIN).unwrap(), Some(9));
    assert_eq!(reader.lookup_direct_v6(Ipv6Key::MAX).unwrap(), Some(9));
}

#[test]
fn branch_descent_uses_real_first_keys() {
    let branch = v4_branch(&[(10, 3), (100, 4)]);
    let first = v4_leaf(&[(10, 20, 1)]);
    let second = v4_leaf(&[(100, 110, 2)]);
    let (reader, _path) = fixture(
        meta(AddressFamily::Ipv4, 5, 2, 2),
        &[(2, branch), (3, first), (4, second)],
    );

    assert_eq!(reader.lookup_direct_v4(Ipv4Key(5)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(1));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(50)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(105)).unwrap(), Some(2));
}

#[test]
fn successful_lookup_allocates_nothing() {
    let leaf = v4_leaf(&[(10, 20, 42)]);
    let (reader, _path) = fixture(meta(AddressFamily::Ipv4, 3, 2, 1), &[(2, leaf)]);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(42));

    let (result, allocations) = count_thread_allocations(|| reader.lookup_direct_v4(Ipv4Key(15)));
    assert_eq!(result.unwrap(), Some(42));
    assert_eq!(allocations, 0);
}

#[test]
fn malformed_selected_pages_fail_safely_without_crc_validation() {
    let mut bad_child = v4_branch(&[(10, 9)]);
    bad_child[28..32].fill(0xa5);
    let (reader, _path) = fixture(meta(AddressFamily::Ipv4, 3, 2, 1), &[(2, bad_child)]);
    assert!(matches!(
        reader.lookup_direct_v4(Ipv4Key(10)),
        Err(Error::Corrupt(_))
    ));

    let mut leaf = v4_leaf(&[(10, 20, 1)]);
    leaf[28..32].fill(0xa5);
    let (reader, _path) = fixture(meta(AddressFamily::Ipv4, 3, 2, 1), &[(2, leaf)]);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(1));
}

#[test]
fn cursors_cross_leaf_boundaries_in_both_directions() {
    let branch = v4_branch(&[(10, 3), (100, 4)]);
    let first = v4_leaf(&[(10, 20, 1), (30, 40, 2)]);
    let second = v4_leaf(&[(100, 110, 3)]);
    let (reader, _path) = fixture(
        meta(AddressFamily::Ipv4, 5, 2, 3),
        &[(2, branch), (3, first), (4, second)],
    );

    let expected = [
        DirectRange {
            from: Ipv4Key(10),
            to: Ipv4Key(20),
            value: 1,
        },
        DirectRange {
            from: Ipv4Key(30),
            to: Ipv4Key(40),
            value: 2,
        },
        DirectRange {
            from: Ipv4Key(100),
            to: Ipv4Key(110),
            value: 3,
        },
    ];

    let mut forward = reader.direct_cursor_v4(RangeDirection::Forward).unwrap();
    for range in expected {
        assert_eq!(forward.next_range().unwrap(), Some(range));
    }
    assert_eq!(forward.next_range().unwrap(), None);

    let mut backward = reader.direct_cursor_v4(RangeDirection::Backward).unwrap();
    for range in expected.into_iter().rev() {
        assert_eq!(backward.next_range().unwrap(), Some(range));
    }
    assert_eq!(backward.next_range().unwrap(), None);
}

#[test]
fn cursor_seek_includes_a_containing_range_then_follows_direction() {
    let branch = v4_branch(&[(10, 3), (100, 4)]);
    let first = v4_leaf(&[(10, 20, 1)]);
    let second = v4_leaf(&[(100, 110, 2)]);
    let (reader, _path) = fixture(
        meta(AddressFamily::Ipv4, 5, 2, 2),
        &[(2, branch), (3, first), (4, second)],
    );

    let mut forward = reader.direct_cursor_v4(RangeDirection::Forward).unwrap();
    forward.seek(Ipv4Key(15)).unwrap();
    assert_eq!(forward.next_range().unwrap().unwrap().from, Ipv4Key(10));
    forward.seek(Ipv4Key(21)).unwrap();
    assert_eq!(forward.next_range().unwrap().unwrap().from, Ipv4Key(100));
    forward.seek(Ipv4Key(111)).unwrap();
    assert_eq!(forward.next_range().unwrap(), None);

    let mut backward = reader.direct_cursor_v4(RangeDirection::Backward).unwrap();
    backward.seek(Ipv4Key(105)).unwrap();
    assert_eq!(backward.next_range().unwrap().unwrap().from, Ipv4Key(100));
    backward.seek(Ipv4Key(99)).unwrap();
    assert_eq!(backward.next_range().unwrap().unwrap().from, Ipv4Key(10));
    backward.seek(Ipv4Key(9)).unwrap();
    assert_eq!(backward.next_range().unwrap(), None);
}

#[test]
fn warmed_cursor_step_allocates_nothing_even_across_a_leaf() {
    let branch = v4_branch(&[(10, 3), (100, 4)]);
    let first = v4_leaf(&[(10, 20, 1)]);
    let second = v4_leaf(&[(100, 110, 2)]);
    let (reader, _path) = fixture(
        meta(AddressFamily::Ipv4, 5, 2, 2),
        &[(2, branch), (3, first), (4, second)],
    );
    let mut cursor = reader.direct_cursor_v4(RangeDirection::Forward).unwrap();
    assert_eq!(cursor.next_range().unwrap().unwrap().value, 1);

    let (result, allocations) = count_thread_allocations(|| cursor.next_range());
    assert_eq!(result.unwrap().unwrap().value, 2);
    assert_eq!(allocations, 0);
}
