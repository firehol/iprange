use super::*;

#[test]
fn sparse_maximum_page_does_not_size_the_heap_table() {
    let mut set = PageSet::allocate(1024, u64::from(u32::MAX), None).unwrap();
    assert!(set.insert(u32::MAX).unwrap());
    assert!(!set.insert(u32::MAX).unwrap());
    assert!(set.slot_count() <= 128);
}

#[test]
fn full_heap_table_fails_before_allocation_or_looping() {
    let mut set = PageSet::allocate(64, 100, None).unwrap();
    for page in 0..6 {
        assert!(set.insert(page).unwrap());
    }
    assert!(matches!(
        set.insert(7),
        Err(Error::BudgetExceeded("recovery page-ownership table"))
    ));
}

#[cfg(any(unix, windows))]
mod linux {
    use std::fs;
    use std::path::PathBuf;
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};

    use super::*;
    use crate::contract::{AddressFamily, ValueKind, ValueTag};

    static SEQUENCE: AtomicU64 = AtomicU64::new(0);

    struct Directory(PathBuf);

    impl Directory {
        fn new() -> Self {
            let time = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos();
            let sequence = SEQUENCE.fetch_add(1, Ordering::Relaxed);
            let path = std::env::temp_dir().join(format!(
                "iprange-v4-page-set-{}-{time}-{sequence}",
                std::process::id()
            ));
            fs::create_dir(&path).unwrap();
            Self(path)
        }
    }

    impl Drop for Directory {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn full_heap_migrates_once_to_fixed_scratch_and_resets() {
        let directory = Directory::new();
        let budget = budget(&directory, 4096);
        let mut set = PageSet::for_recovery(64, 100, meta(), &budget).unwrap();

        for page in 0..100 {
            assert!(set.insert(page).unwrap());
        }
        assert!(!set.insert(42).unwrap());
        assert_eq!(set.retained_bytes(), 0);
        assert_eq!(fs::read_dir(&directory.0).unwrap().count(), 1);

        set.reset().unwrap();
        assert!(set.insert(u32::MAX).unwrap());
        assert!(!set.insert(u32::MAX).unwrap());

        let cleanup = set.cleanup().expect("fallback created one attempt");
        assert!(cleanup.clean());
        assert_eq!(fs::read_dir(&directory.0).unwrap().count(), 0);
    }

    #[test]
    fn insufficient_scratch_fails_without_an_artifact() {
        let directory = Directory::new();
        let budget = budget(&directory, 256);
        let mut set = PageSet::for_recovery(64, 100, meta(), &budget).unwrap();
        for page in 0..6 {
            assert!(set.insert(page).unwrap());
        }
        assert!(matches!(
            set.insert(7),
            Err(Error::BudgetExceeded("recovery page-ownership scratch"))
        ));
        assert!(set.cleanup().is_none());
        assert_eq!(fs::read_dir(&directory.0).unwrap().count(), 0);
    }

    fn budget(directory: &Directory, max_scratch_bytes: u64) -> RecoveryBudget {
        RecoveryBudget {
            max_heap_bytes: 64,
            max_output_pages: 1000,
            max_open_files: 4,
            max_scratch_bytes,
            max_scratch_files: 2,
            scratch_directory: Some(directory.0.clone()),
        }
    }

    fn meta() -> MetaV4 {
        MetaV4 {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [0x11; 16],
            txn_id: 9,
            commit_nonce: [0x22; 16],
            page_count: 100,
            range_record_count: 0,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: 0,
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retired_extent_count: 0,
            range_root: 0,
            catalog_name_root: 0,
            catalog_index_root: 0,
            feed_used_root: 0,
            membership_id_root: 0,
            membership_hash_root: 0,
            membership_used_root: 0,
            metadata_root: 0,
            free_bitmap_root: 0,
            retirement_root: 0,
            allocator_reserve: [0; 4],
        }
    }
}
