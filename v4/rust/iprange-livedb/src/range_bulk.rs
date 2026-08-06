//! Direct mapped-page construction of an ordered canonical range tree.

use std::marker::PhantomData;

use crate::contract::ValueKind;
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::key::IpKey;
use crate::slotted_page::{Appender, PageSink};

const RANGE_BRANCH: u8 = 1;
const RANGE_LEAF: u8 = 2;
const BRANCH_LEVELS: usize = 6;
const MAX_RANGE_CELL: usize = 36;
const MAX_BRANCH_CELL: usize = 20;

pub(crate) trait Sink {
    type WritePage<'a>: PageSink
    where
        Self: 'a;

    fn allocate(&mut self) -> Result<u32>;
    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>;
}

impl<T: Store> Sink for T {
    type WritePage<'a>
        = T::WritePage<'a>
    where
        Self: 'a;

    fn allocate(&mut self) -> Result<u32> {
        Store::allocate(self)
    }

    fn update_page<'a, R, F>(&'a mut self, page_number: u32, update: F) -> Result<R>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<R>,
    {
        Store::update_page(self, page_number, update)
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Record<K> {
    pub(crate) from: K,
    pub(crate) to: K,
    pub(crate) value: u32,
}

#[derive(Clone, Copy, Debug)]
struct Node<K> {
    first: K,
    page_number: u32,
}

enum FinishedLevel<K> {
    Root(u32),
    Parent(Node<K>),
}

#[derive(Debug)]
struct PackedPage<K> {
    appender: Option<Appender>,
    first: Option<K>,
    page_number: Option<u32>,
}

impl<K: Copy> PackedPage<K> {
    const fn empty() -> Self {
        Self {
            appender: None,
            first: None,
            page_number: None,
        }
    }

    fn start<S: Sink>(
        &mut self,
        sink: &mut S,
        page_type: u8,
        born_txn: u64,
        level: u16,
        aux: u32,
    ) -> Result<()> {
        let page_number = sink.allocate()?;
        let appender = sink.update_page(page_number, |page| {
            Ok(Appender::new(page, page_type, born_txn, level, aux))
        })?;
        self.appender = Some(appender);
        self.first = None;
        self.page_number = Some(page_number);
        Ok(())
    }

    fn push<S: Sink>(&mut self, sink: &mut S, first: K, cell: &[u8]) -> Result<bool> {
        let page_number = self
            .page_number
            .ok_or(Error::Corrupt("ordered range page has no output page"))?;
        let appender = self
            .appender
            .as_mut()
            .ok_or(Error::Corrupt("ordered range page is not active"))?;
        let appended = sink.update_page(page_number, |page| appender.try_push(page, cell))?;
        if appended && self.first.is_none() {
            self.first = Some(first);
        }
        Ok(appended)
    }

    fn finish<S: Sink>(&mut self, sink: &mut S) -> Result<Node<K>> {
        let appender = self
            .appender
            .take()
            .ok_or(Error::Corrupt("ordered range page is not active"))?;
        let page_number = self
            .page_number
            .take()
            .ok_or(Error::Corrupt("ordered range page has no output page"))?;
        sink.update_page(page_number, |page| appender.finish(page))?;
        let first = self
            .first
            .take()
            .ok_or(Error::Corrupt("ordered range page has no first key"))?;
        Ok(Node { first, page_number })
    }

    fn active(&self) -> bool {
        self.appender.is_some()
    }
}

#[derive(Debug)]
struct BranchLevel<K> {
    page: PackedPage<K>,
    only_child: Option<Node<K>>,
    emitted: bool,
}

impl<K: Copy> BranchLevel<K> {
    const fn empty() -> Self {
        Self {
            page: PackedPage::empty(),
            only_child: None,
            emitted: false,
        }
    }
}

#[derive(Debug)]
pub(crate) struct Builder<K> {
    born_txn: u64,
    value_kind: ValueKind,
    leaf: PackedPage<K>,
    branches: [BranchLevel<K>; BRANCH_LEVELS],
    previous: Option<Record<K>>,
    record_count: u64,
    key: PhantomData<K>,
}

impl<K: IpKey> Builder<K> {
    pub(crate) fn new(born_txn: u64, value_kind: ValueKind) -> Self {
        Self {
            born_txn,
            value_kind,
            leaf: PackedPage::empty(),
            branches: std::array::from_fn(|_| BranchLevel::empty()),
            previous: None,
            record_count: 0,
            key: PhantomData,
        }
    }

    pub(crate) fn push<S: Sink>(&mut self, sink: &mut S, record: Record<K>) -> Result<()> {
        self.require_record(record)?;
        let next_count = self
            .record_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("range record count"))?;
        let mut cell = [0; MAX_RANGE_CELL];
        let cell_len = K::WIDTH * 2 + 4;
        record.from.write_le(&mut cell);
        record.to.write_le(&mut cell[K::WIDTH..]);
        cell[K::WIDTH * 2..cell_len].copy_from_slice(&record.value.to_le_bytes());
        self.push_leaf_cell(sink, record.from, &cell[..cell_len])?;
        self.previous = Some(record);
        self.record_count = next_count;
        Ok(())
    }

    fn push_leaf_cell<S: Sink>(&mut self, sink: &mut S, first: K, cell: &[u8]) -> Result<()> {
        if !self.leaf.active() {
            self.leaf
                .start(sink, RANGE_LEAF, self.born_txn, 0, K::FAMILY as u32)?;
        }
        if self.leaf.push(sink, first, cell)? {
            return Ok(());
        }
        let node = self.leaf.finish(sink)?;
        self.push_node(sink, 0, node)?;
        self.leaf
            .start(sink, RANGE_LEAF, self.born_txn, 0, K::FAMILY as u32)?;
        if self.leaf.push(sink, first, cell)? {
            Ok(())
        } else {
            Err(Error::Corrupt("range record does not fit an empty leaf"))
        }
    }

    fn require_record(&self, record: Record<K>) -> Result<()> {
        if record.from > record.to {
            return Err(Error::InvalidArgument("range start is after its end"));
        }
        if self.value_kind == ValueKind::Membership && record.value == 0 {
            return Err(Error::Corrupt("membership range value is zero"));
        }
        let Some(previous) = self.previous else {
            return Ok(());
        };
        if previous.to >= record.from {
            return Err(Error::InvalidArgument(
                "ordered output ranges are not strictly increasing",
            ));
        }
        if previous.value == record.value && previous.to.checked_next() == Some(record.from) {
            return Err(Error::InvalidArgument(
                "adjacent equal ranges are not canonical",
            ));
        }
        Ok(())
    }

    fn push_node<S: Sink>(
        &mut self,
        sink: &mut S,
        level_index: usize,
        node: Node<K>,
    ) -> Result<()> {
        if level_index == BRANCH_LEVELS {
            return Err(Error::PageSpaceExhausted);
        }

        if !self.branches[level_index].page.active() {
            if let Some(first) = self.branches[level_index].only_child.take() {
                self.start_branch(sink, level_index)?;
                if !self.push_branch_cell(sink, level_index, first)? {
                    return Err(Error::Corrupt("range branch cell does not fit"));
                }
            } else {
                self.branches[level_index].only_child = Some(node);
                return Ok(());
            }
        }
        if self.push_branch_cell(sink, level_index, node)? {
            return Ok(());
        }

        let parent = self.branches[level_index].page.finish(sink)?;
        self.branches[level_index].emitted = true;
        self.push_node(sink, level_index + 1, parent)?;
        self.branches[level_index].only_child = Some(node);
        Ok(())
    }

    fn start_branch<S: Sink>(&mut self, sink: &mut S, level_index: usize) -> Result<()> {
        self.branches[level_index].page.start(
            sink,
            RANGE_BRANCH,
            self.born_txn,
            level_index as u16 + 1,
            K::FAMILY as u32,
        )
    }

    fn push_branch_cell<S: Sink>(
        &mut self,
        sink: &mut S,
        level_index: usize,
        node: Node<K>,
    ) -> Result<bool> {
        let mut cell = [0; MAX_BRANCH_CELL];
        let cell_len = K::WIDTH + 4;
        node.first.write_le(&mut cell);
        cell[K::WIDTH..cell_len].copy_from_slice(&node.page_number.to_le_bytes());
        self.branches[level_index]
            .page
            .push(sink, node.first, &cell[..cell_len])
    }

    pub(crate) fn finish<S: Sink>(&mut self, sink: &mut S) -> Result<(u32, u64)> {
        if self.record_count == 0 {
            return Ok((0, 0));
        }
        let leaf = self.leaf.finish(sink)?;
        self.push_node(sink, 0, leaf)?;

        for level_index in 0..BRANCH_LEVELS {
            match self.finish_level(sink, level_index)? {
                None => {}
                Some(FinishedLevel::Root(root)) => return Ok((root, self.record_count)),
                Some(FinishedLevel::Parent(node)) => {
                    self.push_node(sink, level_index + 1, node)?;
                }
            }
        }
        Err(Error::PageSpaceExhausted)
    }

    fn finish_level<S: Sink>(
        &mut self,
        sink: &mut S,
        level_index: usize,
    ) -> Result<Option<FinishedLevel<K>>> {
        if self.branches[level_index].page.active() {
            let node = self.branches[level_index].page.finish(sink)?;
            return Ok(Some(if self.branches[level_index].emitted {
                FinishedLevel::Parent(node)
            } else {
                FinishedLevel::Root(node.page_number)
            }));
        }
        let Some(child) = self.branches[level_index].only_child.take() else {
            return Ok(None);
        };
        if !self.branches[level_index].emitted {
            return Ok(Some(FinishedLevel::Root(child.page_number)));
        }
        self.start_branch(sink, level_index)?;
        if !self.push_branch_cell(sink, level_index, child)? {
            return Err(Error::Corrupt("range branch cell does not fit"));
        }
        let node = self.branches[level_index].page.finish(sink)?;
        Ok(Some(FinishedLevel::Parent(node)))
    }
}
