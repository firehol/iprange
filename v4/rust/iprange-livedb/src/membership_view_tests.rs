//! Lazy membership-view tests.

use super::*;
use crate::bootstrap;
use crate::contract::{u16_le, AddressFamily, ValueTag, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;
use crate::database::ImmutableReader;
use crate::feed::FeedName;
use crate::slotted_page::{put_u16, put_u32, put_u64, Builder, HEADER_SIZE};
use crate::test_alloc::count_thread_allocations;
use std::fs::{self, File};
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

struct TestPath(PathBuf);

impl TestPath {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-membership-{}-{unique}",
            std::process::id()
        )))
    }
}

impl Drop for TestPath {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

fn fixture(meta: MetaV4, pages: &[(u32, [u8; PAGE_SIZE])]) -> (ImmutableReader, TestPath) {
    let path = TestPath::new();
    let mut image = vec![0; meta.page_count as usize * PAGE_SIZE];
    meta.encode_into((&mut image[..PAGE_SIZE]).try_into().unwrap());
    meta.encode_into((&mut image[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
    for &(number, page) in pages {
        let start = number as usize * PAGE_SIZE;
        image[start..start + PAGE_SIZE].copy_from_slice(&page);
    }
    fs::write(&path.0, image).unwrap();
    (ImmutableReader::open(&path.0).unwrap(), path)
}

fn membership_meta(
    page_count: u64,
    feeds: u64,
    feed_limit: u64,
    feed_used_root: u32,
    id_root: u32,
    hash_root: u32,
    id_used_root: u32,
) -> MetaV4 {
    let mut meta = bootstrap::tests::empty_direct_meta(1);
    meta.value_kind = ValueKind::Membership;
    meta.value_tag = ValueTag::new(b"membership").unwrap();
    meta.page_count = page_count;
    meta.range_record_count = 1;
    meta.active_feed_count = feeds;
    meta.feed_index_limit = feed_limit;
    meta.membership_entry_count = 1;
    meta.membership_id_limit = 2;
    meta.range_root = 2;
    meta.catalog_name_root = 3;
    meta.catalog_index_root = 4;
    meta.feed_used_root = feed_used_root;
    meta.membership_id_root = id_root;
    meta.membership_hash_root = hash_root;
    meta.membership_used_root = id_used_root;
    meta
}

fn fixed_page(page_type: u8, cells: &[&[u8]]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, page_type, 1, 0, 0);
    for &cell in cells {
        builder.push(cell).unwrap();
    }
    builder.finish().unwrap();
    page
}

fn range_leaf(id: u32) -> [u8; PAGE_SIZE] {
    let mut record = [0; 12];
    put_u32(&mut record, 0, 10);
    put_u32(&mut record, 4, 20);
    put_u32(&mut record, 8, id);
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, 2, 1, 0, AddressFamily::Ipv4 as u32);
    builder.push(&record).unwrap();
    builder.finish().unwrap();
    page
}

fn catalog_leaf(page_type: u8, entries: &[(u32, &str)]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, page_type, 1, 0, 0);
    for &(index, text) in entries {
        let name = FeedName::new(text).unwrap();
        let len = 12 + name.as_bytes().len();
        let mut record = [0; 267];
        put_u16(&mut record, 0, len as u16);
        put_u32(&mut record, 4, index);
        record[8] = name.as_bytes().len() as u8;
        record[12..len].copy_from_slice(name.as_bytes());
        builder.push(&record[..len]).unwrap();
    }
    builder.finish().unwrap();
    page
}

fn bitmap_leaf(aux: u32, indexes: &[u32]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = 15;
    put_u16(&mut page, 6, HEADER_SIZE as u16);
    put_u64(&mut page, 8, 1);
    put_u16(&mut page, 18, 0);
    put_u16(&mut page, 20, (HEADER_SIZE + 500 * 8) as u16);
    put_u16(&mut page, 22, PAGE_SIZE as u16);
    put_u32(&mut page, 24, aux);
    for &index in indexes {
        let at = HEADER_SIZE + index as usize / 64 * 8;
        let word = u64::from_le_bytes(page[at..at + 8].try_into().unwrap());
        put_u64(&mut page, at, word | 1u64 << (index % 64));
    }
    let words = (0..500)
        .filter(|word| page[HEADER_SIZE + word * 8..HEADER_SIZE + word * 8 + 8] != [0; 8])
        .count();
    put_u16(&mut page, 16, words as u16);
    stamp(&mut page);
    page
}

fn used_bitmap_branch(children: &[(usize, u32)]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = 14;
    put_u16(&mut page, 6, HEADER_SIZE as u16);
    put_u64(&mut page, 8, 1);
    put_u16(&mut page, 16, children.len() as u16);
    put_u16(&mut page, 18, 1);
    put_u16(&mut page, 20, (HEADER_SIZE + 32 + 256 * 4) as u16);
    put_u16(&mut page, 22, PAGE_SIZE as u16);
    put_u32(&mut page, 24, 2);
    for &(index, child) in children {
        let at = HEADER_SIZE + index / 64 * 8;
        let summary = u64::from_le_bytes(page[at..at + 8].try_into().unwrap());
        put_u64(&mut page, at, summary | 1u64 << (index % 64));
        put_u32(&mut page, HEADER_SIZE + 32 + index * 4, child);
    }
    stamp(&mut page);
    page
}

fn id_leaf(words: &[u64], blob_root: Option<u32>) -> [u8; PAGE_SIZE] {
    let inline = blob_root.is_none();
    let len = if inline { 64 + words.len() * 8 } else { 64 };
    let mut record = vec![0; len];
    put_u16(&mut record, 0, len as u16);
    record[2] = u8::from(!inline);
    put_u32(&mut record, 4, 1);
    put_u64(&mut record, 8, 1);
    put_u32(&mut record, 16, words.len() as u32);
    put_u32(&mut record, 20, (words.len() * 8) as u32);
    put_u32(&mut record, 24, blob_root.unwrap_or(0));
    if inline {
        for (index, word) in words.iter().copied().enumerate() {
            put_u64(&mut record, 64 + index * 8, word);
        }
    }
    fixed_page(8, &[&record])
}

fn hash_leaf(word_count: u32) -> [u8; PAGE_SIZE] {
    let mut record = [0; 40];
    put_u32(&mut record, 32, word_count);
    put_u32(&mut record, 36, 1);
    fixed_page(10, &[&record])
}

fn blob_branch(entries: &[(u64, u32)]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, 11, 1, 1, 1);
    for &(offset, child) in entries {
        let mut record = [0; 16];
        put_u64(&mut record, 0, offset);
        put_u32(&mut record, 8, child);
        builder.push(&record).unwrap();
    }
    builder.finish().unwrap();
    page
}

fn blob_leaf(offset: u64, words: &[u64]) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = 12;
    put_u16(&mut page, 6, HEADER_SIZE as u16);
    put_u64(&mut page, 8, 1);
    put_u16(&mut page, 16, 1);
    put_u16(&mut page, 20, (48 + words.len() * 8) as u16);
    put_u16(&mut page, 22, PAGE_SIZE as u16);
    put_u32(&mut page, 24, 1);
    put_u64(&mut page, 32, offset);
    put_u16(&mut page, 40, (words.len() * 8) as u16);
    for (index, word) in words.iter().copied().enumerate() {
        put_u64(&mut page, 48 + index * 8, word);
    }
    stamp(&mut page);
    page
}

fn stamp(page: &mut [u8; PAGE_SIZE]) {
    put_u32(page, 28, 0);
    let checksum = crc32c::crc32c_with_zeroed(page, 28, 4).unwrap();
    put_u32(page, 28, checksum);
}

fn inline_fixture(words: &[u64]) -> (ImmutableReader, TestPath) {
    let by_name = [(0, "f0"), (130, "f130"), (65, "f65")];
    let by_index = [(0, "f0"), (65, "f65"), (130, "f130")];
    let pages = [
        (2, range_leaf(1)),
        (3, catalog_leaf(4, &by_name)),
        (4, catalog_leaf(6, &by_index)),
        (5, bitmap_leaf(2, &[0, 65, 130])),
        (6, id_leaf(words, None)),
        (7, hash_leaf(words.len() as u32)),
        (8, bitmap_leaf(3, &[1])),
    ];
    fixture(membership_meta(9, 3, 131, 5, 6, 7, 8), &pages)
}

fn blob_fixture() -> (ImmutableReader, TestPath, MetaV4) {
    let by_index = [(0, "f0"), (65, "f65"), (130, "f130"), (32384, "f32384")];
    let by_name = [(0, "f0"), (130, "f130"), (32384, "f32384"), (65, "f65")];
    let mut words = vec![0; 507];
    words[0] = 1;
    words[1] = 2;
    words[2] = 4;
    words[506] = 1;
    let pages = [
        (2, range_leaf(1)),
        (3, catalog_leaf(4, &by_name)),
        (4, catalog_leaf(6, &by_index)),
        (5, used_bitmap_branch(&[(0, 6), (1, 7)])),
        (6, bitmap_leaf(2, &[0, 65, 130])),
        (7, bitmap_leaf(2, &[384])),
        (8, id_leaf(&words, Some(11))),
        (9, hash_leaf(507)),
        (10, bitmap_leaf(3, &[1])),
        (11, blob_branch(&[(0, 12), (4048, 13)])),
        (12, blob_leaf(0, &words[..506])),
        (13, blob_leaf(4048, &words[506..])),
    ];
    let meta = membership_meta(14, 4, 32385, 5, 8, 9, 10);
    let (reader, path) = fixture(meta, &pages);
    (reader, path, meta)
}

#[test]
fn inline_lookup_reads_words_batches_and_named_bits() {
    let (reader, _path) = inline_fixture(&[1, 2, 4]);
    assert!(reader.lookup_membership_v4(Ipv4Key(9)).unwrap().is_none());
    let view = reader.lookup_membership_v4(Ipv4Key(10)).unwrap().unwrap();
    assert_eq!(view.word_count().unwrap(), 3);
    assert_eq!(view.word(0).unwrap(), Some(1));
    assert_eq!(view.word(2).unwrap(), Some(4));
    assert_eq!(view.word(3).unwrap(), None);

    let mut output = [99; 4];
    assert_eq!(view.read_words(1, &mut output).unwrap(), 2);
    assert_eq!(output, [2, 4, 99, 99]);
    assert!(view.contains_index(0).unwrap());
    assert!(!view.contains_index(1).unwrap());
    assert!(view.contains_index(65).unwrap());
    assert!(view.contains_index(130).unwrap());
    assert!(view.contains_index(131).is_err());
}

#[test]
fn blob_batches_cross_leaf_boundaries_without_materializing() {
    let (reader, _path, _meta) = blob_fixture();
    let view = reader.lookup_membership_v4(Ipv4Key(20)).unwrap().unwrap();
    assert_eq!(view.word_count().unwrap(), 507);
    assert_eq!(view.word(506).unwrap(), Some(1));
    let mut output = [9; 4];
    assert_eq!(view.read_words(504, &mut output).unwrap(), 3);
    assert_eq!(output, [0, 0, 1, 9]);
    assert!(view.contains_index(32384).unwrap());
}

#[test]
fn warmed_lookup_and_inline_or_blob_reads_allocate_nothing() {
    let (reader, _path) = inline_fixture(&[1, 2, 4]);
    let (view, allocations) = count_thread_allocations(|| reader.lookup_membership_v4(Ipv4Key(10)));
    let view = view.unwrap().unwrap();
    assert_eq!(allocations, 0);
    let (word, allocations) = count_thread_allocations(|| view.word(1));
    assert_eq!(word.unwrap(), Some(2));
    assert_eq!(allocations, 0);

    let (reader, _path, _meta) = blob_fixture();
    let view = reader.lookup_membership_v4(Ipv4Key(10)).unwrap().unwrap();
    let mut words = [0; 3];
    let (count, allocations) = count_thread_allocations(|| view.read_words(504, &mut words));
    assert_eq!(count.unwrap(), 3);
    assert_eq!(allocations, 0);
}

#[test]
fn maximum_word_count_is_a_constant_size_view() {
    let path = TestPath::new();
    fs::write(&path.0, []).unwrap();
    let file = File::open(&path.0).unwrap();
    let mapping = Mapping::read_only(file, 0).unwrap();
    let mut meta = bootstrap::tests::empty_direct_meta(1);
    meta.value_kind = ValueKind::Membership;
    meta.feed_index_limit = 1u64 << 32;
    let view = MembershipView {
        mapping: &mapping,
        meta,
        record: Record {
            id: 1,
            word_count: 67_108_864,
            leaf_page: 2,
            leaf_index: 0,
            storage: Storage::Blob(2),
        },
        owner_identity: None,
    };
    let (count, allocations) = count_thread_allocations(|| view.word_count());
    assert_eq!(count.unwrap(), 67_108_864);
    assert_eq!(allocations, 0);
}

#[test]
fn selected_crc_is_not_implicit_but_structural_damage_fails() {
    let (reader, path) = inline_fixture(&[1, 2, 4]);
    drop(reader);
    let mut image = fs::read(&path.0).unwrap();
    image[6 * PAGE_SIZE + 28..6 * PAGE_SIZE + 32].fill(0xa5);
    fs::write(&path.0, image).unwrap();
    let reader = ImmutableReader::open(&path.0).unwrap();
    assert_eq!(
        reader
            .lookup_membership_v4(Ipv4Key(10))
            .unwrap()
            .unwrap()
            .word(2)
            .unwrap(),
        Some(4)
    );

    drop(reader);
    let mut image = fs::read(&path.0).unwrap();
    let slot = usize::from(u16_le(&image[6 * PAGE_SIZE..7 * PAGE_SIZE], HEADER_SIZE));
    image[6 * PAGE_SIZE + slot + 8..6 * PAGE_SIZE + slot + 16].fill(0);
    fs::write(&path.0, image).unwrap();
    let reader = ImmutableReader::open(&path.0).unwrap();
    assert!(matches!(
        reader.lookup_membership_v4(Ipv4Key(10)),
        Err(Error::Corrupt(_))
    ));
}

#[test]
fn selected_blob_child_and_leaf_geometry_fail_closed_without_crc_checks() {
    let (reader, path, _meta) = blob_fixture();
    drop(reader);
    let mut image = fs::read(&path.0).unwrap();
    image[12 * PAGE_SIZE + 28..12 * PAGE_SIZE + 32].fill(0xa5);
    fs::write(&path.0, &image).unwrap();
    let reader = ImmutableReader::open(&path.0).unwrap();
    assert_eq!(
        reader
            .lookup_membership_v4(Ipv4Key(10))
            .unwrap()
            .unwrap()
            .word(0)
            .unwrap(),
        Some(1)
    );
    drop(reader);

    let branch = &image[11 * PAGE_SIZE..12 * PAGE_SIZE];
    let second = usize::from(u16_le(branch, HEADER_SIZE + 2));
    image[11 * PAGE_SIZE + second + 8..11 * PAGE_SIZE + second + 12]
        .copy_from_slice(&u32::MAX.to_le_bytes());
    fs::write(&path.0, image).unwrap();
    let reader = ImmutableReader::open(&path.0).unwrap();
    let view = reader.lookup_membership_v4(Ipv4Key(10)).unwrap().unwrap();
    assert!(matches!(view.word(506), Err(Error::Corrupt(_))));

    let (reader, path, _meta) = blob_fixture();
    drop(reader);
    let mut image = fs::read(&path.0).unwrap();
    image[13 * PAGE_SIZE + 32..13 * PAGE_SIZE + 40].copy_from_slice(&4040u64.to_le_bytes());
    fs::write(&path.0, image).unwrap();
    let reader = ImmutableReader::open(&path.0).unwrap();
    let view = reader.lookup_membership_v4(Ipv4Key(10)).unwrap().unwrap();
    assert!(matches!(view.word(506), Err(Error::Corrupt(_))));
}

#[test]
fn wrong_kind_family_missing_id_and_trailing_zero_fail_closed() {
    let mut direct = bootstrap::tests::empty_direct_meta(1);
    direct.page_count = 2;
    let (reader, _path) = fixture(direct, &[]);
    assert!(matches!(
        reader.lookup_membership_v4(Ipv4Key(0)),
        Err(Error::WrongValueKind(_))
    ));

    let (reader, _path) = inline_fixture(&[1, 0]);
    let view = reader.lookup_membership_v4(Ipv4Key(10)).unwrap().unwrap();
    assert!(matches!(view.word(1), Err(Error::Corrupt(_))));

    let (reader, _path) = inline_fixture(&[1]);
    assert!(matches!(
        reader.lookup_membership_v6(Ipv6Key::MIN),
        Err(Error::WrongAddressFamily(_))
    ));

    let (reader, path) = inline_fixture(&[1]);
    drop(reader);
    let mut image = fs::read(&path.0).unwrap();
    let leaf = &image[2 * PAGE_SIZE..3 * PAGE_SIZE];
    let record = usize::from(u16_le(leaf, HEADER_SIZE));
    image[2 * PAGE_SIZE + record + 8..2 * PAGE_SIZE + record + 12]
        .copy_from_slice(&2u32.to_le_bytes());
    fs::write(&path.0, image).unwrap();
    let reader = ImmutableReader::open(&path.0).unwrap();
    assert!(matches!(
        reader.lookup_membership_v4(Ipv4Key(10)),
        Err(Error::Corrupt(_))
    ));
}

#[test]
fn live_view_rejects_a_foreign_process_owner() {
    let (_reader, path) = inline_fixture(&[1, 2, 4]);
    let file = File::open(&path.0).unwrap();
    let mapping = Mapping::read_only(file, fs::metadata(&path.0).unwrap().len()).unwrap();
    let meta = membership_meta(9, 3, 131, 5, 6, 7, 8);
    let foreign = crate::process_identity::ProcessIdentity::foreign();
    let view = lookup_v4(&mapping, &meta, Ipv4Key(10), Some(foreign))
        .unwrap()
        .unwrap();
    assert!(matches!(view.word_count(), Err(Error::ForkedHandle)));
}
