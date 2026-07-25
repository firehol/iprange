//! File-backed draft integration tests.

use super::*;
use crate::bootstrap::tests::empty_direct_meta;
use crate::database::ImmutableReader;
use std::fs::{self, OpenOptions};
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

struct TestFile {
    path: PathBuf,
    file: File,
}

impl TestFile {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-draft-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)
            .unwrap();
        file.set_len((2 * PAGE_SIZE) as u64).unwrap();
        Self { path, file }
    }
}

impl Drop for TestFile {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}

fn publish(file: &File, meta: MetaV4, meta_page: u8) {
    file.set_len(meta.page_count * PAGE_SIZE as u64).unwrap();
    let mut page = [0; PAGE_SIZE];
    meta.encode_into(&mut page);
    file_io::write_exact_at(file, &page, u64::from(meta_page) * PAGE_SIZE as u64).unwrap();
    file.sync_all().unwrap();
}

#[test]
fn direct_drafts_publish_readable_cow_generations() {
    let test = TestFile::new();
    let creation = empty_direct_meta(1);
    publish(&test.file, creation, 0);
    publish(&test.file, creation, 1);

    let budget = PageBudget {
        max_heap_bytes: 0,
        max_private_pages: 20_000,
        max_growth_pages: 20_000,
    };
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    {
        let mut store = DraftStore::new(&test.file, creation.page_count, budget, &mut draft);
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
    publish(&test.file, draft.meta, 0);

    let reader = ImmutableReader::open(&test.path).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(5));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(30)).unwrap(), Some(99));
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(41)).unwrap(), None);

    let committed = draft.meta;
    let old_reader = ImmutableReader::open(&test.path).unwrap();
    let mut next = Draft::new(committed, [4; 16]).unwrap();
    {
        let mut store = DraftStore::new(&test.file, committed.page_count, budget, &mut next);
        store.clear_v4(Ipv4Key(0), Ipv4Key(1_000)).unwrap();
        store.assign_v4(Ipv4Key(9_000), Ipv4Key(9_100), 7).unwrap();
        store.prepare().unwrap();
    }
    assert!(next.meta.retired_extent_count > 0);
    assert_ne!(next.meta.retirement_root, 0);
    assert_eq!(old_reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), Some(5));
    assert_eq!(old_reader.lookup_direct_v4(Ipv4Key(9_050)).unwrap(), None);
    publish(&test.file, next.meta, 1);

    let reader = ImmutableReader::open(&test.path).unwrap();
    assert_eq!(reader.info().transaction_id, 3);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(10)).unwrap(), None);
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(9_050)).unwrap(), Some(7));
}

#[test]
fn ipv6_assignment_and_clear_use_the_same_file_store() {
    let test = TestFile::new();
    let mut creation = empty_direct_meta(1);
    creation.address_family = crate::contract::AddressFamily::Ipv6;
    let budget = PageBudget {
        max_heap_bytes: 0,
        max_private_pages: 100,
        max_growth_pages: 100,
    };
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    let mut store = DraftStore::new(&test.file, creation.page_count, budget, &mut draft);
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
    let test = TestFile::new();
    let creation = empty_direct_meta(1);
    let mut draft = Draft::new(creation, [3; 16]).unwrap();
    let mut store = DraftStore::new(
        &test.file,
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
