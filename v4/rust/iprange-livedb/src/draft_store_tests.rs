//! File-backed draft integration tests.

use super::*;
use crate::bootstrap::tests::empty_direct_meta;
use crate::database::ImmutableReader;
use crate::mapping::{ByteSource, Mapping};
use std::fs::{self, OpenOptions};
use std::path::PathBuf;

struct TestFile {
    path: PathBuf,
    mapping: Mapping,
}

impl TestFile {
    fn new() -> Self {
        let unique = u128::from_le_bytes(crate::random::nonzero_128().unwrap());
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-draft-{}-{unique:032x}",
            std::process::id()
        ));
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)
            .unwrap();
        file.set_len((2 * PAGE_SIZE) as u64).unwrap();
        Self {
            path,
            mapping: Mapping::read_write(file, (2 * PAGE_SIZE) as u64).unwrap(),
        }
    }
}

impl Drop for TestFile {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}

fn publish(mapping: &mut Mapping, meta: MetaV4, meta_page: u8) {
    let committed_bytes = meta.page_count * PAGE_SIZE as u64;
    if committed_bytes <= mapping.len() {
        mapping.shrink_or_retain(committed_bytes).unwrap();
    } else {
        mapping.resize(committed_bytes).unwrap();
    }
    meta.encode_mapped(
        mapping
            .page_mut(u32::from(meta_page), meta.page_count)
            .unwrap(),
    )
    .unwrap();
    mapping
        .flush_page(u32::from(meta_page), meta.page_count)
        .unwrap();
    mapping.sync_file().unwrap();
}

#[test]
fn direct_drafts_publish_readable_cow_generations() {
    let mut test = TestFile::new();
    let creation = empty_direct_meta(1);
    publish(&mut test.mapping, creation, 0);
    publish(&mut test.mapping, creation, 1);

    let budget = PageBudget {
        max_heap_bytes: 0,
        max_private_pages: 20_000,
        max_growth_pages: 20_000,
    };
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    {
        let mut store = DraftStore::new(&mut test.mapping, creation.page_count, budget, &mut draft);
        for key in (0..2_000_u32).rev() {
            store
                .assign_v4(Ipv4Key(key * 2), Ipv4Key(key * 2), key)
                .unwrap();
        }
        store.assign_v4(Ipv4Key(20), Ipv4Key(40), 99).unwrap();
        store.prepare().unwrap();
    }
    assert!(draft.changed());
    assert_eq!(draft.meta.txn_id, 2);
    assert!(draft.meta.range_record_count < 2_000);
    assert!(draft.meta.allocator_reserve.iter().all(|&page| page != 0));
    publish(&mut test.mapping, draft.meta, 0);

    let reader = ImmutableReader::open(&test.path).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(5));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(30)).unwrap(), Some(99));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(41)).unwrap(), None);
    drop(reader);

    let committed = draft.meta;
    let old_reader = ImmutableReader::open(&test.path).unwrap();
    let mut next = Draft::new(committed, [4; 16]).unwrap();
    {
        let mut store = DraftStore::new(&mut test.mapping, committed.page_count, budget, &mut next);
        store.clear_v4(Ipv4Key(0), Ipv4Key(1_000)).unwrap();
        store.assign_v4(Ipv4Key(9_000), Ipv4Key(9_100), 7).unwrap();
        store.prepare().unwrap();
    }
    assert!(next.meta.retired_extent_count > 0);
    assert_ne!(next.meta.retirement_root, 0);
    assert_eq!(old_reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(5));
    assert_eq!(old_reader.lookup_direct_v4(Ipv4Key(9_050)).unwrap(), None);
    drop(old_reader);
    publish(&mut test.mapping, next.meta, 1);

    let reader = ImmutableReader::open(&test.path).unwrap();
    assert_eq!(reader.info().transaction_id, 3);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(9_050)).unwrap(), Some(7));
}

#[test]
fn ipv6_assignment_and_clear_use_the_same_file_store() {
    let mut test = TestFile::new();
    let mut creation = empty_direct_meta(1);
    creation.address_family = crate::contract::AddressFamily::Ipv6;
    let budget = PageBudget {
        max_heap_bytes: 0,
        max_private_pages: 100,
        max_growth_pages: 100,
    };
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    let mut store = DraftStore::new(&mut test.mapping, creation.page_count, budget, &mut draft);
    store
        .assign_v6(Ipv6Key::from_u128(0), Ipv6Key::from_u128(u128::MAX), 8)
        .unwrap();
    store
        .clear_v6(Ipv6Key::from_u128(1), Ipv6Key::from_u128(u128::MAX - 1))
        .unwrap();
    store.prepare().unwrap();
    assert_eq!(draft.meta.range_record_count, 2);
}

#[test]
fn page_budget_failure_happens_before_the_first_allocation() {
    let mut test = TestFile::new();
    let creation = empty_direct_meta(1);
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    let mut store = DraftStore::new(
        &mut test.mapping,
        creation.page_count,
        PageBudget {
            max_heap_bytes: 0,
            max_private_pages: 0,
            max_growth_pages: 0,
        },
        &mut draft,
    );
    assert!(matches!(
        store.assign_v4(Ipv4Key(1), Ipv4Key(2), 3),
        Err(Error::BudgetExceeded(_))
    ));
    assert_eq!(draft.meta.range_root, 0);
    assert_eq!(draft.meta.page_count, 2);
    assert!(!draft.changed());
}

#[test]
fn mapped_page_cannot_bypass_the_current_page_limit() {
    let mut test = TestFile::new();
    let creation = empty_direct_meta(1);
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    let mut store = DraftStore::new(
        &mut test.mapping,
        creation.page_count,
        PageBudget {
            max_heap_bytes: 2 * PAGE_SIZE as u64,
            max_private_pages: 1,
            max_growth_pages: 1,
        },
        &mut draft,
    );
    let page_number = store.allocate().unwrap();
    store.inspect_page(page_number, |_| Ok(())).unwrap();

    store.draft.meta.page_count = u64::from(page_number);
    assert!(matches!(
        store.inspect_page(page_number, |_| Ok(())),
        Err(Error::Corrupt(_))
    ));
}

#[test]
fn mutation_defers_each_data_page_checksum_until_prepare() {
    let mut test = TestFile::new();
    let creation = empty_direct_meta(1);
    publish(&mut test.mapping, creation, 0);
    publish(&mut test.mapping, creation, 1);
    let budget = PageBudget {
        max_heap_bytes: 4 * 1024 * 1024,
        max_private_pages: 20_000,
        max_growth_pages: 20_000,
    };
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    crate::page_checksum::work::reset();
    {
        let mut store = DraftStore::new(&mut test.mapping, creation.page_count, budget, &mut draft);
        for key in 0..2_000_u32 {
            store
                .assign_v4(Ipv4Key(key * 2), Ipv4Key(key * 2), key)
                .unwrap();
        }
        assert_eq!(crate::page_checksum::work::count(), 0);
        store.prepare().unwrap();
    }

    let mut current_pages = 0;
    for page_number in 2..draft.meta.page_count {
        let page = test
            .mapping
            .page(page_number as u32, draft.meta.page_count)
            .unwrap();
        if page.equals(0, &crate::contract::PAGE_MAGIC)
            && crate::contract::u64_le(page, 8) == draft.meta.txn_id
        {
            current_pages += 1;
            assert_eq!(
                u32_le(page, 28),
                crate::crc32c::crc32c_source_with_zeroed(page, 28, 4).unwrap()
            );
        }
    }
    assert_eq!(crate::page_checksum::work::count(), current_pages);
}
