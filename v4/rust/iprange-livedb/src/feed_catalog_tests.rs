//! Named-feed catalog reader tests.

use super::*;
use crate::bootstrap;
use crate::contract::u16_le;
use crate::contract::{AddressFamily, ValueTag, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;
use crate::database::ImmutableReader;
use crate::mapping::Mapping;
use crate::slotted_page::{put_u16, put_u32, put_u64, Builder, HEADER_SIZE};
use crate::test_alloc::count_thread_allocations;
use std::fs::{self, File};
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
            "iprange-v4-feed-{label}-{}-{unique}",
            std::process::id()
        )))
    }
}

impl Drop for TestPath {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

fn name(value: &str) -> FeedName {
    FeedName::new(value).unwrap()
}

fn meta(
    page_count: u64,
    name_root: u32,
    index_root: u32,
    bitmap_root: u32,
    active: u64,
    limit: u64,
) -> MetaV4 {
    let mut meta = bootstrap::tests::empty_direct_meta(1);
    meta.address_family = AddressFamily::Ipv4;
    meta.value_kind = ValueKind::Membership;
    meta.value_tag = ValueTag::new(b"membership").unwrap();
    meta.page_count = page_count;
    meta.active_feed_count = active;
    meta.feed_index_limit = limit;
    meta.membership_id_limit = 1;
    meta.catalog_name_root = name_root;
    meta.catalog_index_root = index_root;
    meta.feed_used_root = bitmap_root;
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
    (ImmutableReader::open(&path.0).unwrap(), path)
}

fn catalog_page(page_type: u8, level: u16, entries: &[(FeedName, u32)]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, page_type, 1, level, 0);
    for &(name, value) in entries {
        let mut record = [0; MAX_NAME_RECORD];
        let len = NAME_RECORD_BASE + name.as_bytes().len();
        put_u16(&mut record, 0, len as u16);
        put_u32(&mut record, 4, value);
        record[8] = name.as_bytes().len() as u8;
        record[NAME_RECORD_BASE..len].copy_from_slice(name.as_bytes());
        builder.push(&record[..len]).unwrap();
    }
    builder.finish().unwrap();
    page
}

fn index_branch(entries: &[(u32, u32)]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, INDEX_BRANCH, 1, 1, 0);
    for &(first, child) in entries {
        let mut record = [0; 8];
        put_u32(&mut record, 0, first);
        put_u32(&mut record, 4, child);
        builder.push(&record).unwrap();
    }
    builder.finish().unwrap();
    page
}

fn used_bitmap(indexes: &[u32]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = 15;
    put_u16(&mut page, 6, HEADER_SIZE as u16);
    put_u64(&mut page, 8, 1);
    put_u16(&mut page, 18, 0);
    put_u16(&mut page, 20, (HEADER_SIZE + 500 * 8) as u16);
    put_u16(&mut page, 22, PAGE_SIZE as u16);
    put_u32(&mut page, 24, 2);
    for &index in indexes {
        let at = HEADER_SIZE + index as usize / 64 * 8;
        let value = u64::from_le_bytes(page[at..at + 8].try_into().unwrap());
        put_u64(&mut page, at, value | 1u64 << (index % 64));
    }
    let nonzero = (0..500)
        .filter(|word| page[HEADER_SIZE + word * 8..HEADER_SIZE + word * 8 + 8] != [0; 8])
        .count();
    put_u16(&mut page, 16, nonzero as u16);
    let checksum = crc32c::crc32c_with_zeroed(&page, 28, 4).unwrap();
    put_u32(&mut page, 28, checksum);
    page
}

fn two_feed_fixture(active: u64) -> (ImmutableReader, TestPath, MetaV4) {
    let alpha = name("alpha");
    let beta = name("beta");
    let pages = [
        (2, catalog_page(NAME_BRANCH, 1, &[(alpha, 3), (beta, 4)])),
        (3, catalog_page(NAME_LEAF, 0, &[(alpha, 9)])),
        (4, catalog_page(NAME_LEAF, 0, &[(beta, 2)])),
        (5, index_branch(&[(2, 6), (9, 7)])),
        (6, catalog_page(INDEX_LEAF, 0, &[(beta, 2)])),
        (7, catalog_page(INDEX_LEAF, 0, &[(alpha, 9)])),
        (8, used_bitmap(&[2, 9])),
    ];
    let meta = meta(9, 2, 5, 8, active, 10);
    let (reader, path) = fixture(meta, &pages);
    (reader, path, meta)
}

#[test]
fn exact_lookup_and_numeric_cursor_cross_leaf_boundaries() {
    let (reader, _path, _meta) = two_feed_fixture(2);
    assert_eq!(reader.lookup_feed("alpha").unwrap().unwrap().index, 9);
    assert_eq!(reader.lookup_feed("beta").unwrap().unwrap().index, 2);
    assert_eq!(reader.lookup_feed("aardvark").unwrap(), None);
    assert_eq!(reader.lookup_feed("gamma").unwrap(), None);

    let mut cursor = reader.feed_cursor().unwrap();
    assert_eq!(
        cursor.next_feed().unwrap(),
        Some(FeedEntry {
            name: name("beta"),
            index: 2
        })
    );
    assert_eq!(
        cursor.next_feed().unwrap(),
        Some(FeedEntry {
            name: name("alpha"),
            index: 9
        })
    );
    assert_eq!(cursor.next_feed().unwrap(), None);
    assert_eq!(cursor.next_feed().unwrap(), None);
}

#[test]
fn catalog_lookup_counts_one_root_to_leaf_path() {
    let (reader, _path, _meta) = two_feed_fixture(2);
    let (entry, work) = crate::work::measure(|| reader.lookup_feed("alpha"));
    assert_eq!(entry.unwrap().unwrap().index, 9);
    assert_eq!(work.catalog_lookups, 1);
    assert_eq!(work.tree_lookups, 1);
    assert_eq!(work.tree_descents, 1);
    assert_eq!(work.pages_visited, 2);
}

#[test]
fn empty_catalog_and_maximum_name_are_supported() {
    let empty = meta(2, 0, 0, 0, 0, 0);
    let (reader, _path) = fixture(empty, &[]);
    assert_eq!(reader.lookup_feed("anything").unwrap(), None);
    assert_eq!(reader.feed_cursor().unwrap().next_feed().unwrap(), None);

    let maximum = name(&format!("a{}z", "_".repeat(253)));
    let pages = [
        (2, catalog_page(NAME_LEAF, 0, &[(maximum, 0)])),
        (3, catalog_page(INDEX_LEAF, 0, &[(maximum, 0)])),
        (4, used_bitmap(&[0])),
    ];
    let (reader, _path) = fixture(meta(5, 2, 3, 4, 1, 1), &pages);
    assert_eq!(
        reader.lookup_feed(maximum.as_str()).unwrap(),
        Some(FeedEntry {
            name: maximum,
            index: 0
        })
    );
}

#[test]
fn query_grammar_and_database_kind_fail_before_page_access() {
    let (reader, _path, _meta) = two_feed_fixture(2);
    assert!(matches!(reader.lookup_feed("Bad"), Err(Error::NameInvalid)));

    let direct = bootstrap::tests::empty_direct_meta(1);
    let (reader, _path) = fixture(direct, &[]);
    assert!(matches!(
        reader.lookup_feed("alpha"),
        Err(Error::WrongValueKind(_))
    ));
    assert!(matches!(
        reader.feed_cursor(),
        Err(Error::WrongValueKind(_))
    ));
}

#[test]
fn selected_record_child_and_declared_limit_are_checked_without_crc() {
    let mut leaf = catalog_page(NAME_LEAF, 0, &[(name("alpha"), 0)]);
    leaf[28..32].fill(0xa5);
    let pages = [
        (2, leaf),
        (3, catalog_page(INDEX_LEAF, 0, &[(name("alpha"), 0)])),
        (4, used_bitmap(&[0])),
    ];
    let (reader, _path) = fixture(meta(5, 2, 3, 4, 1, 1), &pages);
    assert_eq!(reader.lookup_feed("alpha").unwrap().unwrap().index, 0);

    let pages = [
        (
            2,
            catalog_page(NAME_BRANCH, 1, &[(name("alpha"), u32::MAX)]),
        ),
        (3, index_branch(&[(0, 4)])),
        (4, catalog_page(INDEX_LEAF, 0, &[(name("alpha"), 0)])),
        (5, used_bitmap(&[0])),
    ];
    let (reader, _path) = fixture(meta(6, 2, 3, 5, 1, 1), &pages);
    assert!(matches!(
        reader.lookup_feed("alpha"),
        Err(Error::Corrupt(_))
    ));

    let pages = [
        (2, catalog_page(NAME_LEAF, 0, &[(name("alpha"), 2)])),
        (3, catalog_page(INDEX_LEAF, 0, &[(name("alpha"), 2)])),
        (4, used_bitmap(&[0])),
    ];
    let (reader, _path) = fixture(meta(5, 2, 3, 4, 1, 1), &pages);
    assert!(matches!(
        reader.lookup_feed("alpha"),
        Err(Error::Corrupt(_))
    ));
}

#[test]
fn malformed_variable_record_is_rejected() {
    let mut page = catalog_page(NAME_LEAF, 0, &[(name("alpha"), 0)]);
    let slot = usize::from(u16_le(&page, HEADER_SIZE));
    page[slot + 2] = 1;
    let pages = [
        (2, page),
        (3, catalog_page(INDEX_LEAF, 0, &[(name("alpha"), 0)])),
        (4, used_bitmap(&[0])),
    ];
    let (reader, _path) = fixture(meta(5, 2, 3, 4, 1, 1), &pages);
    assert!(matches!(
        reader.lookup_feed("alpha"),
        Err(Error::Corrupt(_))
    ));
}

#[test]
fn cursor_detects_count_and_order_disagreement() {
    let (reader, _path, _meta) = two_feed_fixture(3);
    let mut cursor = reader.feed_cursor().unwrap();
    assert!(cursor.next_feed().unwrap().is_some());
    assert!(cursor.next_feed().unwrap().is_some());
    assert!(matches!(cursor.next_feed(), Err(Error::Corrupt(_))));

    let pages = [
        (2, catalog_page(NAME_LEAF, 0, &[(name("alpha"), 2)])),
        (
            3,
            catalog_page(INDEX_LEAF, 0, &[(name("alpha"), 2), (name("beta"), 1)]),
        ),
        (4, used_bitmap(&[1, 2])),
    ];
    let (reader, _path) = fixture(meta(5, 2, 3, 4, 2, 3), &pages);
    let mut cursor = reader.feed_cursor().unwrap();
    assert!(cursor.next_feed().unwrap().is_some());
    assert!(matches!(cursor.next_feed(), Err(Error::Corrupt(_))));
}

#[test]
fn lookup_and_cross_leaf_cursor_step_allocate_nothing() {
    let (reader, _path, _meta) = two_feed_fixture(2);
    assert!(reader.lookup_feed("alpha").unwrap().is_some());
    let (result, allocations) = count_thread_allocations(|| reader.lookup_feed("alpha"));
    assert_eq!(result.unwrap().unwrap().index, 9);
    assert_eq!(allocations, 0);

    let mut cursor = reader.feed_cursor().unwrap();
    assert_eq!(cursor.next_feed().unwrap().unwrap().index, 2);
    let (result, allocations) = count_thread_allocations(|| cursor.next_feed());
    assert_eq!(result.unwrap().unwrap().index, 9);
    assert_eq!(allocations, 0);
}

#[test]
fn live_cursor_rejects_a_foreign_process_owner() {
    let (_reader, path, meta) = two_feed_fixture(2);
    let file = File::open(&path.0).unwrap();
    let mapping = Mapping::read_only(file, fs::metadata(&path.0).unwrap().len()).unwrap();
    let foreign = crate::process_identity::ProcessIdentity::foreign();
    let mut cursor = FeedCursor::new(&mapping, &meta, Some(foreign)).unwrap();
    assert!(matches!(cursor.next_feed(), Err(Error::ForkedHandle)));
}
