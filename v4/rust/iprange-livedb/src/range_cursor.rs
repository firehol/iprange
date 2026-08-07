//! Allocation-free ordered range cursors.

use std::fmt;
use std::marker::PhantomData;

use crate::contract::{MetaV4, MAX_TREE_LEVEL};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::{Mapping, PageView};
use crate::process_identity::ProcessIdentity;
use crate::range_tree::{self, Header};

/// Direction of ordered cursor movement.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RangeDirection {
    Forward,
    Backward,
}

/// One inclusive direct-value interval.
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

#[derive(Clone, Copy)]
struct Leaf {
    page_number: u32,
    header: Header,
}

pub(crate) struct CursorState<K> {
    meta: MetaV4,
    direction: RangeDirection,
    path: [Frame; MAX_TREE_LEVEL as usize],
    depth: usize,
    leaf: Option<Leaf>,
    index: usize,
    needs_advance: bool,
    finished: bool,
    owner_identity: Option<ProcessIdentity>,
    key: PhantomData<K>,
}

impl<K: IpKey> CursorState<K> {
    pub(crate) fn new(
        mapping: &Mapping,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        let mut cursor = Self {
            meta: *meta,
            direction,
            path: [EMPTY_FRAME; MAX_TREE_LEVEL as usize],
            depth: 0,
            leaf: None,
            index: 0,
            needs_advance: false,
            finished: meta.range_root == 0,
            owner_identity,
            key: PhantomData,
        };
        if !cursor.finished {
            cursor.descend_edge(mapping, meta.range_root, None)?;
        }
        Ok(cursor)
    }

    pub(crate) fn seek(&mut self, mapping: &Mapping, target: K) -> Result<()> {
        self.require_owner()?;
        self.depth = 0;
        self.needs_advance = false;
        self.finished = self.meta.range_root == 0;
        if self.finished {
            return Ok(());
        }
        if let Err(error) = self.seek_inner(mapping, target) {
            self.finished = true;
            return Err(error);
        }
        Ok(())
    }

    fn seek_inner(&mut self, mapping: &Mapping, target: K) -> Result<()> {
        let mut page_number = self.meta.range_root;
        let mut expected_level = None;
        loop {
            let page = mapping.page(page_number, self.meta.page_count)?;
            let header = range_tree::parse_header::<K, _>(page, self.meta.txn_id, expected_level)?;
            if header.level == 0 {
                return self.seek_leaf(mapping, page_number, page, header, target);
            }
            let found =
                range_tree::greatest_not_after::<K, _>(page, &header, K::WIDTH + 4, target)?;
            let index = match (found, self.direction) {
                (Some(index), _) => index,
                (None, RangeDirection::Forward) => 0,
                (None, RangeDirection::Backward) => {
                    self.finished = true;
                    return Ok(());
                }
            };
            let child = range_tree::branch_child::<K, _>(page, &header, index)?;
            self.push(page_number, index, &header)?;
            page_number = child;
            expected_level = Some(header.level - 1);
        }
    }

    fn seek_leaf(
        &mut self,
        mapping: &Mapping,
        page_number: u32,
        page: PageView<'_>,
        header: Header,
        target: K,
    ) -> Result<()> {
        let found =
            range_tree::greatest_not_after::<K, _>(page, &header, K::WIDTH * 2 + 4, target)?;
        match self.direction {
            RangeDirection::Backward => match found {
                Some(index) => self.set_leaf(page_number, header, index),
                None => self.finished = true,
            },
            RangeDirection::Forward => {
                let index = match found {
                    None => 0,
                    Some(index) => {
                        let record = range_tree::leaf_record::<K, _>(page, &header, index)?;
                        usize::from(target > record.to) + index
                    }
                };
                if index < header.item_count {
                    self.set_leaf(page_number, header, index);
                } else {
                    self.leaf = Some(Leaf {
                        page_number,
                        header,
                    });
                    self.index = header.item_count - 1;
                    self.advance_leaf(mapping)?;
                }
            }
        }
        Ok(())
    }

    pub(crate) fn next(&mut self, mapping: &Mapping) -> Result<Option<DirectRange<K>>> {
        self.require_owner()?;
        if self.finished {
            return Ok(None);
        }
        if self.needs_advance {
            if let Err(error) = self.advance(mapping) {
                self.finished = true;
                return Err(error);
            }
            if self.finished {
                return Ok(None);
            }
        }
        let leaf = self.leaf.expect("active cursor has a leaf");
        let page = mapping.page(leaf.page_number, self.meta.page_count)?;
        let record = match range_tree::leaf_record::<K, _>(page, &leaf.header, self.index) {
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

    fn advance(&mut self, mapping: &Mapping) -> Result<()> {
        let item_count = self
            .leaf
            .as_ref()
            .expect("active cursor has a leaf")
            .header
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
        self.advance_leaf(mapping)
    }

    fn advance_leaf(&mut self, mapping: &Mapping) -> Result<()> {
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
            let page = mapping.page(frame.page_number, self.meta.page_count)?;
            let header =
                range_tree::parse_header::<K, _>(page, self.meta.txn_id, Some(frame.level))?;
            if header.item_count != frame.item_count {
                return Err(crate::error::Error::Corrupt(
                    "range branch changed during cursor traversal",
                ));
            }
            let child = range_tree::branch_child::<K, _>(page, &header, frame.index)?;
            return self.descend_edge(mapping, child, Some(frame.level - 1));
        }
        self.finished = true;
        Ok(())
    }

    fn descend_edge(
        &mut self,
        mapping: &Mapping,
        mut page_number: u32,
        mut expected: Option<u16>,
    ) -> Result<()> {
        loop {
            let page = mapping.page(page_number, self.meta.page_count)?;
            let header = range_tree::parse_header::<K, _>(page, self.meta.txn_id, expected)?;
            if header.level == 0 {
                let index = match self.direction {
                    RangeDirection::Forward => 0,
                    RangeDirection::Backward => header.item_count - 1,
                };
                self.set_leaf(page_number, header, index);
                return Ok(());
            }
            let index = match self.direction {
                RangeDirection::Forward => 0,
                RangeDirection::Backward => header.item_count - 1,
            };
            let child = range_tree::branch_child::<K, _>(page, &header, index)?;
            self.push(page_number, index, &header)?;
            page_number = child;
            expected = Some(header.level - 1);
        }
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

    fn set_leaf(&mut self, page_number: u32, header: Header, index: usize) {
        self.leaf = Some(Leaf {
            page_number,
            header,
        });
        self.index = index;
        self.needs_advance = false;
        self.finished = false;
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_identity.is_some_and(|owner| !owner.is_current()) {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

pub(crate) struct Cursor<'a, K> {
    mapping: &'a Mapping,
    state: CursorState<K>,
}

impl<'a, K: IpKey> Cursor<'a, K> {
    pub(crate) fn new(
        mapping: &'a Mapping,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        Ok(Self {
            mapping,
            state: CursorState::new(mapping, meta, direction, owner_identity)?,
        })
    }

    pub(crate) fn seek(&mut self, target: K) -> Result<()> {
        self.state.seek(self.mapping, target)
    }

    pub(crate) fn next(&mut self) -> Result<Option<DirectRange<K>>> {
        self.state.next(self.mapping)
    }
}

macro_rules! public_cursor {
    ($name:ident, $key:ty) => {
        pub struct $name<'a> {
            inner: Cursor<'a, $key>,
        }

        impl<'a> $name<'a> {
            pub(crate) fn new(
                mapping: &'a Mapping,
                meta: &MetaV4,
                direction: RangeDirection,
            ) -> Result<Self> {
                Ok(Self {
                    inner: Cursor::new(mapping, meta, direction, None)?,
                })
            }

            pub(crate) fn new_live(
                mapping: &'a Mapping,
                meta: &MetaV4,
                direction: RangeDirection,
                owner_identity: ProcessIdentity,
            ) -> Result<Self> {
                Ok(Self {
                    inner: Cursor::new(mapping, meta, direction, Some(owner_identity))?,
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
                    .field("direction", &self.inner.state.direction)
                    .field("finished", &self.inner.state.finished)
                    .finish_non_exhaustive()
            }
        }
    };
}

public_cursor!(DirectCursorV4, Ipv4Key);
public_cursor!(DirectCursorV6, Ipv6Key);
