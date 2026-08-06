//! Ordered range traversal through a mutable page store.

use std::marker::PhantomData;

use crate::contract::{u64_le, MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::key::IpKey;
use crate::range_cursor::DirectRange;
use crate::range_tree::{self, Header};

const MAX_DEPTH: usize = MAX_TREE_LEVEL as usize;

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    next_child: usize,
    child_count: usize,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    next_child: 0,
    child_count: 0,
    level: 0,
};

pub(crate) struct Cursor<K> {
    selected_txn: u64,
    page_limit: u64,
    release_private: bool,
    path: [Frame; MAX_DEPTH],
    depth: usize,
    page: [u8; PAGE_SIZE],
    leaf: Option<Header>,
    index: usize,
    finished: bool,
    key: PhantomData<K>,
}

impl<K: IpKey> Cursor<K> {
    pub(crate) fn new<S: Store>(
        store: &mut S,
        meta: &MetaV4,
        release_private: bool,
    ) -> Result<Self> {
        if meta.address_family != K::FAMILY {
            return Err(Error::WrongAddressFamily(
                "stored range cursor has the wrong address family",
            ));
        }
        let mut cursor = Self {
            selected_txn: meta.txn_id,
            page_limit: meta.page_count,
            release_private,
            path: [EMPTY_FRAME; MAX_DEPTH],
            depth: 0,
            page: [0; PAGE_SIZE],
            leaf: None,
            index: 0,
            finished: meta.range_root == 0,
            key: PhantomData,
        };
        if !cursor.finished {
            cursor.descend_left(store, meta.range_root, None)?;
        }
        Ok(cursor)
    }

    pub(crate) fn next<S: Store>(&mut self, store: &mut S) -> Result<Option<DirectRange<K>>> {
        if self.finished {
            return Ok(None);
        }
        let Some(header) = self.leaf else {
            return Err(Error::Corrupt("stored range cursor has no leaf"));
        };
        let record = range_tree::leaf_record::<K>(&self.page, &header, self.index)?;
        let result = DirectRange {
            from: record.from,
            to: record.to,
            value: record.value,
        };
        self.index += 1;
        if self.index == header.item_count {
            self.advance(store)?;
        }
        Ok(Some(result))
    }

    fn descend_left<S: Store>(
        &mut self,
        store: &mut S,
        mut page_number: u32,
        mut expected_level: Option<u16>,
    ) -> Result<()> {
        loop {
            self.read(store, page_number)?;
            let header =
                range_tree::parse_header::<K>(&self.page, self.selected_txn, expected_level)?;
            if self.release_private && u64_le(&self.page, 8) != self.selected_txn {
                return Err(Error::Corrupt(
                    "consumed range tree contains a committed page",
                ));
            }
            if header.level == 0 {
                if self.release_private {
                    store.discard_private(page_number)?;
                }
                self.leaf = Some(header);
                self.index = 0;
                self.finished = false;
                return Ok(());
            }

            page_number = self.enter_branch(page_number, &header)?;
            expected_level = Some(header.level - 1);
        }
    }

    fn enter_branch(&mut self, page_number: u32, header: &Header) -> Result<u32> {
        let child = self.child(header, 0)?;
        let frame = self
            .path
            .get_mut(self.depth)
            .ok_or(Error::Corrupt("range tree exceeds its maximum height"))?;
        *frame = Frame {
            page_number,
            next_child: 1,
            child_count: header.item_count,
            level: header.level,
        };
        self.depth += 1;
        Ok(child)
    }

    fn advance<S: Store>(&mut self, store: &mut S) -> Result<()> {
        self.leaf = None;
        loop {
            let Some(slot) = self.depth.checked_sub(1) else {
                self.finished = true;
                return Ok(());
            };
            let mut frame = self.path[slot];
            if frame.next_child < frame.child_count {
                self.read(store, frame.page_number)?;
                let header = range_tree::parse_header::<K>(
                    &self.page,
                    self.selected_txn,
                    Some(frame.level),
                )?;
                if header.item_count != frame.child_count {
                    return Err(Error::Corrupt(
                        "range branch changed during stored traversal",
                    ));
                }
                let child = self.child(&header, frame.next_child)?;
                frame.next_child += 1;
                self.path[slot] = frame;
                self.depth = slot + 1;
                return self.descend_left(store, child, Some(frame.level - 1));
            }

            self.depth = slot;
            if self.release_private {
                store.discard_private(frame.page_number)?;
            }
        }
    }

    fn read<S: Store>(&mut self, store: &S, page_number: u32) -> Result<()> {
        if page_number < 2 || u64::from(page_number) >= self.page_limit {
            return Err(Error::Corrupt("stored range page is outside its source"));
        }
        store.read(page_number, &mut self.page)
    }

    fn child(&self, header: &Header, index: usize) -> Result<u32> {
        let child = range_tree::branch_child::<K>(&self.page, header, index)?;
        if child < 2 || u64::from(child) >= self.page_limit {
            return Err(Error::Corrupt("stored range child is outside its source"));
        }
        Ok(child)
    }
}
