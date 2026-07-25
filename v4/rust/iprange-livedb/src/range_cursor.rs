//! Allocation-free ordered range cursors.

use std::fmt;
use std::fs::File;
use std::marker::PhantomData;

use crate::contract::{MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_tree::{self, Header};

/// Direction of ordered cursor movement.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RangeDirection {
    Forward,
    Backward,
}

/// One canonical direct-value interval.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DirectRange<K> {
    pub from: K,
    pub to: K,
    pub value: u32,
}

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    index: usize,
    item_count: usize,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    index: 0,
    item_count: 0,
    level: 0,
};

struct Cursor<'a, K> {
    file: &'a File,
    meta: MetaV4,
    direction: RangeDirection,
    path: [Frame; MAX_TREE_LEVEL as usize],
    depth: usize,
    page: [u8; PAGE_SIZE],
    leaf: Option<Header>,
    index: usize,
    needs_advance: bool,
    finished: bool,
    owner_pid: Option<u32>,
    key: PhantomData<K>,
}

impl<'a, K: IpKey> Cursor<'a, K> {
    fn new(
        file: &'a File,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_pid: Option<u32>,
    ) -> Result<Self> {
        let mut cursor = Self {
            file,
            meta: *meta,
            direction,
            path: [EMPTY_FRAME; MAX_TREE_LEVEL as usize],
            depth: 0,
            page: [0; PAGE_SIZE],
            leaf: None,
            index: 0,
            needs_advance: false,
            finished: meta.range_root == 0,
            owner_pid,
            key: PhantomData,
        };
        if !cursor.finished {
            cursor.descend_edge(meta.range_root, None)?;
        }
        Ok(cursor)
    }

    fn seek(&mut self, target: K) -> Result<()> {
        self.require_owner()?;
        self.depth = 0;
        self.needs_advance = false;
        self.finished = self.meta.range_root == 0;
        if self.finished {
            return Ok(());
        }
        if let Err(error) = self.seek_inner(target) {
            self.finished = true;
            return Err(error);
        }
        Ok(())
    }

    fn seek_inner(&mut self, target: K) -> Result<()> {
        let mut page_number = self.meta.range_root;
        let mut expected_level = None;
        loop {
            let header = self.read(page_number, expected_level)?;
            if header.level == 0 {
                return self.seek_leaf(&header, target);
            }
            let found =
                range_tree::greatest_not_after::<K>(&self.page, &header, K::WIDTH + 4, target)?;
            let index = match (found, self.direction) {
                (Some(index), _) => index,
                (None, RangeDirection::Forward) => 0,
                (None, RangeDirection::Backward) => {
                    self.finished = true;
                    return Ok(());
                }
            };
            let child = range_tree::branch_child::<K>(&self.page, &header, index)?;
            self.push(page_number, index, &header)?;
            page_number = child;
            expected_level = Some(header.level - 1);
        }
    }

    fn seek_leaf(&mut self, header: &Header, target: K) -> Result<()> {
        let found =
            range_tree::greatest_not_after::<K>(&self.page, header, K::WIDTH * 2 + 4, target)?;
        match self.direction {
            RangeDirection::Backward => match found {
                Some(index) => self.set_leaf(*header, index),
                None => self.finished = true,
            },
            RangeDirection::Forward => {
                let index = match found {
                    None => 0,
                    Some(index) => {
                        let record = range_tree::leaf_record::<K>(&self.page, header, index)?;
                        usize::from(target > record.to) + index
                    }
                };
                if index < header.item_count {
                    self.set_leaf(*header, index);
                } else {
                    self.leaf = Some(*header);
                    self.index = header.item_count - 1;
                    self.advance_leaf()?;
                }
            }
        }
        Ok(())
    }

    fn next(&mut self) -> Result<Option<DirectRange<K>>> {
        self.require_owner()?;
        if self.finished {
            return Ok(None);
        }
        if self.needs_advance {
            if let Err(error) = self.advance() {
                self.finished = true;
                return Err(error);
            }
            if self.finished {
                return Ok(None);
            }
        }
        let header = self.leaf.as_ref().expect("active cursor has a leaf");
        let record = match range_tree::leaf_record::<K>(&self.page, header, self.index) {
            Ok(record) => record,
            Err(error) => {
                self.finished = true;
                return Err(error);
            }
        };
        self.needs_advance = true;
        Ok(Some(DirectRange {
            from: record.from,
            to: record.to,
            value: record.value,
        }))
    }

    fn advance(&mut self) -> Result<()> {
        let item_count = self
            .leaf
            .as_ref()
            .expect("active cursor has a leaf")
            .item_count;
        let in_leaf = match self.direction {
            RangeDirection::Forward => self.index + 1 < item_count,
            RangeDirection::Backward => self.index > 0,
        };
        if in_leaf {
            match self.direction {
                RangeDirection::Forward => self.index += 1,
                RangeDirection::Backward => self.index -= 1,
            }
            self.needs_advance = false;
            return Ok(());
        }
        self.advance_leaf()
    }

    fn advance_leaf(&mut self) -> Result<()> {
        while self.depth > 0 {
            let slot = self.depth - 1;
            let mut frame = self.path[slot];
            let has_sibling = match self.direction {
                RangeDirection::Forward => frame.index + 1 < frame.item_count,
                RangeDirection::Backward => frame.index > 0,
            };
            if !has_sibling {
                self.depth = slot;
                continue;
            }
            match self.direction {
                RangeDirection::Forward => frame.index += 1,
                RangeDirection::Backward => frame.index -= 1,
            }
            self.path[slot] = frame;
            self.depth = slot + 1;
            let header = self.read(frame.page_number, Some(frame.level))?;
            if header.item_count != frame.item_count {
                return Err(crate::error::Error::Corrupt(
                    "range branch changed during cursor traversal",
                ));
            }
            let child = range_tree::branch_child::<K>(&self.page, &header, frame.index)?;
            return self.descend_edge(child, Some(frame.level - 1));
        }
        self.finished = true;
        Ok(())
    }

    fn descend_edge(&mut self, mut page_number: u32, mut expected: Option<u16>) -> Result<()> {
        loop {
            let header = self.read(page_number, expected)?;
            if header.level == 0 {
                let index = match self.direction {
                    RangeDirection::Forward => 0,
                    RangeDirection::Backward => header.item_count - 1,
                };
                self.set_leaf(header, index);
                return Ok(());
            }
            let index = match self.direction {
                RangeDirection::Forward => 0,
                RangeDirection::Backward => header.item_count - 1,
            };
            let child = range_tree::branch_child::<K>(&self.page, &header, index)?;
            self.push(page_number, index, &header)?;
            page_number = child;
            expected = Some(header.level - 1);
        }
    }

    fn read(&mut self, page_number: u32, expected_level: Option<u16>) -> Result<Header> {
        file_io::read_page(self.file, page_number, self.meta.page_count, &mut self.page)?;
        range_tree::parse_header::<K>(&self.page, self.meta.txn_id, expected_level)
    }

    fn push(&mut self, page_number: u32, index: usize, header: &Header) -> Result<()> {
        let frame = self
            .path
            .get_mut(self.depth)
            .ok_or(crate::error::Error::Corrupt(
                "range tree exceeds its maximum height",
            ))?;
        *frame = Frame {
            page_number,
            index,
            item_count: header.item_count,
            level: header.level,
        };
        self.depth += 1;
        Ok(())
    }

    fn set_leaf(&mut self, header: Header, index: usize) {
        self.leaf = Some(header);
        self.index = index;
        self.needs_advance = false;
        self.finished = false;
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid.is_some_and(|pid| pid != std::process::id()) {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

macro_rules! public_cursor {
    ($name:ident, $key:ty) => {
        pub struct $name<'a> {
            inner: Cursor<'a, $key>,
        }

        impl<'a> $name<'a> {
            pub(crate) fn new(
                file: &'a File,
                meta: &MetaV4,
                direction: RangeDirection,
            ) -> Result<Self> {
                Ok(Self {
                    inner: Cursor::new(file, meta, direction, None)?,
                })
            }

            pub(crate) fn new_live(
                file: &'a File,
                meta: &MetaV4,
                direction: RangeDirection,
                owner_pid: u32,
            ) -> Result<Self> {
                Ok(Self {
                    inner: Cursor::new(file, meta, direction, Some(owner_pid))?,
                })
            }

            /// Reposition to a containing range or the nearest range in this direction.
            pub fn seek(&mut self, target: $key) -> Result<()> {
                self.inner.seek(target)
            }

            /// Return the next range in the cursor's selected direction.
            pub fn next_range(&mut self) -> Result<Option<DirectRange<$key>>> {
                self.inner.next()
            }
        }

        impl fmt::Debug for $name<'_> {
            fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
                output
                    .debug_struct(stringify!($name))
                    .field("direction", &self.inner.direction)
                    .field("finished", &self.inner.finished)
                    .finish_non_exhaustive()
            }
        }
    };
}

public_cursor!(DirectCursorV4, Ipv4Key);
public_cursor!(DirectCursorV6, Ipv6Key);
