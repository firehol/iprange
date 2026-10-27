use super::{validate_selected, BitmapStore, Frame};
use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};
use crate::fixed_tree::Store;

struct GuardStore {
    forbidden: u32,
}

impl Store for GuardStore {
    type ReadPage<'a> = &'a [u8; PAGE_SIZE];
    type WritePage<'a> = [u8; PAGE_SIZE];

    fn target_txn(&self) -> u64 {
        0
    }

    fn page_limit(&self) -> u64 {
        0
    }

    fn inspect_page<'a, T, F>(&'a self, _page_number: u32, _inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        Err(Error::Corrupt("guard store has no pages"))
    }

    fn allocate(&mut self) -> Result<u32> {
        Err(Error::Corrupt("guard store does not allocate"))
    }

    fn update_page<'a, T, F>(&'a mut self, _page_number: u32, _update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        Err(Error::Corrupt("guard store does not update"))
    }

    fn copy_page<'a, T, F>(&'a mut self, _source: u32, _destination: u32, _copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        Err(Error::Corrupt("guard store does not copy"))
    }

    fn discard_private(&mut self, _page_number: u32) -> Result<()> {
        Err(Error::Corrupt("guard store does not discard"))
    }
}

impl BitmapStore for GuardStore {
    fn allocate_bitmap_page(&mut self) -> Result<u32> {
        Err(Error::Corrupt("guard store does not allocate"))
    }

    fn allocation_forbidden(&self, page_number: u32) -> bool {
        page_number == self.forbidden
    }
}

#[test]
fn free_bit_guards_reject_self_ancestor_and_limit() {
    let store = GuardStore { forbidden: 0 };
    let ancestor = [Frame {
        page_number: 7,
        child_index: 0,
        level: 1,
    }];
    assert!(validate_selected(&store, 4, &[], 9, 9).is_err());
    assert!(validate_selected(&store, 4, &[], 1, 100).is_err());
    assert!(validate_selected(&store, 4, &[], 4, 100).is_err());
    assert!(validate_selected(&store, 4, &ancestor, 7, 100).is_err());
    assert_eq!(validate_selected(&store, 4, &ancestor, 8, 100).unwrap(), 8);
}
