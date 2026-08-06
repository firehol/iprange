//! Ordered range pages for one append-only immutable output.

use std::fs::File;

use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::slotted_page::Appender;

use super::{append_page, reserve_page, write_page, OutputBudget};

const RANGE_BRANCH: u8 = 1;
const RANGE_LEAF: u8 = 2;
const BRANCH_LEVELS: usize = 6;
const MAX_RANGE_CELL: usize = 36;
const MAX_BRANCH_CELL: usize = 20;

#[derive(Debug)]
// Both variants own the same fixed page workspace; boxing one would add heap
// work while reducing the outer value by less than one page.
#[allow(clippy::large_enum_variant)]
pub(super) enum Ranges {
    V4(Ordered<Ipv4Key>),
    V6(Ordered<Ipv6Key>),
}

impl Ranges {
    pub(super) fn new(family: AddressFamily, born_txn: u64, value_kind: ValueKind) -> Self {
        match family {
            AddressFamily::Ipv4 => Self::V4(Ordered::new(born_txn, value_kind)),
            AddressFamily::Ipv6 => Self::V6(Ordered::new(born_txn, value_kind)),
        }
    }

    pub(super) fn push_v4(
        &mut self,
        file: &File,
        meta: &mut MetaV4,
        budget: OutputBudget,
        record: Record<Ipv4Key>,
    ) -> Result<()> {
        match self {
            Self::V4(ranges) => ranges.push(file, meta, budget, record),
            Self::V6(_) => Err(Error::WrongAddressFamily(
                "ordered range output is not IPv4",
            )),
        }
    }

    pub(super) fn push_v6(
        &mut self,
        file: &File,
        meta: &mut MetaV4,
        budget: OutputBudget,
        record: Record<Ipv6Key>,
    ) -> Result<()> {
        match self {
            Self::V6(ranges) => ranges.push(file, meta, budget, record),
            Self::V4(_) => Err(Error::WrongAddressFamily(
                "ordered range output is not IPv6",
            )),
        }
    }

    pub(super) fn finish(
        &mut self,
        file: &File,
        meta: &mut MetaV4,
        budget: OutputBudget,
    ) -> Result<(u32, u64)> {
        match self {
            Self::V4(ranges) => ranges.finish(file, meta, budget),
            Self::V6(ranges) => ranges.finish(file, meta, budget),
        }
    }
}

#[derive(Clone, Copy, Debug)]
pub(super) struct Record<K> {
    pub(super) from: K,
    pub(super) to: K,
    pub(super) value: u32,
}

#[derive(Clone, Copy, Debug)]
struct Node<K> {
    first: K,
    page_number: u32,
}

#[derive(Debug)]
struct PackedPage<K> {
    bytes: [u8; PAGE_SIZE],
    appender: Option<Appender>,
    first: Option<K>,
    page_number: Option<u32>,
}

impl<K: Copy> PackedPage<K> {
    fn empty() -> Self {
        Self {
            bytes: [0; PAGE_SIZE],
            appender: None,
            first: None,
            page_number: None,
        }
    }

    fn start(&mut self, page_type: u8, born_txn: u64, level: u16, aux: u32, page: Option<u32>) {
        self.appender = Some(Appender::new(
            &mut self.bytes,
            page_type,
            born_txn,
            level,
            aux,
        ));
        self.first = None;
        self.page_number = page;
    }

    fn push(&mut self, first: K, cell: &[u8]) -> Result<bool> {
        let Some(appender) = self.appender.as_mut() else {
            return Err(Error::Corrupt("ordered range page is not active"));
        };
        let appended = appender.try_push(&mut self.bytes, cell)?;
        if appended && self.first.is_none() {
            self.first = Some(first);
        }
        Ok(appended)
    }

    fn finish(&mut self) -> Result<(K, usize, Option<u32>)> {
        let appender = self
            .appender
            .take()
            .ok_or(Error::Corrupt("ordered range page is not active"))?;
        let item_count = appender.item_count();
        appender.finish(&mut self.bytes)?;
        let first = self
            .first
            .take()
            .ok_or(Error::Corrupt("ordered range page has no first key"))?;
        Ok((first, item_count, self.page_number.take()))
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
    fn empty() -> Self {
        Self {
            page: PackedPage::empty(),
            only_child: None,
            emitted: false,
        }
    }
}

#[derive(Debug)]
pub(super) struct Ordered<K> {
    born_txn: u64,
    value_kind: ValueKind,
    leaf: PackedPage<K>,
    branches: [BranchLevel<K>; BRANCH_LEVELS],
    previous: Option<Record<K>>,
    record_count: u64,
}

impl<K: IpKey> Ordered<K> {
    fn new(born_txn: u64, value_kind: ValueKind) -> Self {
        Self {
            born_txn,
            value_kind,
            leaf: PackedPage::empty(),
            branches: std::array::from_fn(|_| BranchLevel::empty()),
            previous: None,
            record_count: 0,
        }
    }

    fn push(
        &mut self,
        file: &File,
        meta: &mut MetaV4,
        budget: OutputBudget,
        record: Record<K>,
    ) -> Result<()> {
        self.require_record(record)?;
        let Some(next_count) = self.record_count.checked_add(1) else {
            return Err(Error::ArithmeticOverflow("range record count"));
        };
        let mut cell = [0; MAX_RANGE_CELL];
        let cell_len = K::WIDTH * 2 + 4;
        record.from.write_le(&mut cell);
        record.to.write_le(&mut cell[K::WIDTH..]);
        cell[K::WIDTH * 2..cell_len].copy_from_slice(&record.value.to_le_bytes());

        if !self.leaf.active() {
            self.start_leaf(meta, budget)?;
        }
        if !self.leaf.push(record.from, &cell[..cell_len])? {
            let node = self.flush_leaf(file, meta)?;
            self.push_node(file, meta, budget, 0, node)?;
            self.start_leaf(meta, budget)?;
            if !self.leaf.push(record.from, &cell[..cell_len])? {
                return Err(Error::Corrupt("range record does not fit an empty leaf"));
            }
        }

        self.previous = Some(record);
        self.record_count = next_count;
        Ok(())
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
                "immutable output ranges are not strictly ordered",
            ));
        }
        if previous.value == record.value && previous.to.checked_next() == Some(record.from) {
            return Err(Error::InvalidArgument(
                "adjacent equal ranges are not canonical",
            ));
        }
        Ok(())
    }

    fn start_leaf(&mut self, meta: &mut MetaV4, budget: OutputBudget) -> Result<()> {
        let page_number = reserve_page(meta, budget)?;
        self.leaf.start(
            RANGE_LEAF,
            self.born_txn,
            0,
            K::FAMILY as u32,
            Some(page_number),
        );
        Ok(())
    }

    fn flush_leaf(&mut self, file: &File, meta: &MetaV4) -> Result<Node<K>> {
        let (first, _, page_number) = self.leaf.finish()?;
        let page_number = page_number.ok_or(Error::Corrupt("range leaf has no output page"))?;
        write_page(file, meta, page_number, &self.leaf.bytes)?;
        Ok(Node { first, page_number })
    }

    fn push_node(
        &mut self,
        file: &File,
        meta: &mut MetaV4,
        budget: OutputBudget,
        level_index: usize,
        node: Node<K>,
    ) -> Result<()> {
        if level_index == BRANCH_LEVELS {
            return Err(Error::PageSpaceExhausted);
        }
        if !self.branches[level_index].page.active() {
            self.start_branch(level_index);
        }

        let mut cell = [0; MAX_BRANCH_CELL];
        let cell_len = K::WIDTH + 4;
        node.first.write_le(&mut cell);
        cell[K::WIDTH..cell_len].copy_from_slice(&node.page_number.to_le_bytes());
        if self.branches[level_index]
            .page
            .push(node.first, &cell[..cell_len])?
        {
            let count = self.branches[level_index]
                .page
                .appender
                .expect("active branch page has an appender")
                .item_count();
            self.branches[level_index].only_child = (count == 1).then_some(node);
            return Ok(());
        }

        let parent = self.flush_branch(file, meta, budget, level_index)?;
        self.branches[level_index].emitted = true;
        self.push_node(file, meta, budget, level_index + 1, parent)?;
        self.start_branch(level_index);
        if !self.branches[level_index]
            .page
            .push(node.first, &cell[..cell_len])?
        {
            return Err(Error::Corrupt(
                "range branch record does not fit an empty page",
            ));
        }
        self.branches[level_index].only_child = Some(node);
        Ok(())
    }

    fn start_branch(&mut self, level_index: usize) {
        self.branches[level_index].page.start(
            RANGE_BRANCH,
            self.born_txn,
            level_index as u16 + 1,
            K::FAMILY as u32,
            None,
        );
        self.branches[level_index].only_child = None;
    }

    fn flush_branch(
        &mut self,
        file: &File,
        meta: &mut MetaV4,
        budget: OutputBudget,
        level_index: usize,
    ) -> Result<Node<K>> {
        let (first, _, reserved) = self.branches[level_index].page.finish()?;
        if reserved.is_some() {
            return Err(Error::Corrupt("range branch unexpectedly reserved a page"));
        }
        self.branches[level_index].only_child = None;
        let page_number = append_page(file, meta, budget, &self.branches[level_index].page.bytes)?;
        Ok(Node { first, page_number })
    }

    fn finish(
        &mut self,
        file: &File,
        meta: &mut MetaV4,
        budget: OutputBudget,
    ) -> Result<(u32, u64)> {
        if self.record_count == 0 {
            return Ok((0, 0));
        }
        let leaf = self.flush_leaf(file, meta)?;
        self.push_node(file, meta, budget, 0, leaf)?;

        for level_index in 0..BRANCH_LEVELS {
            if !self.branches[level_index].page.active() {
                continue;
            }
            if !self.branches[level_index].emitted {
                if let Some(child) = self.branches[level_index].only_child {
                    return Ok((child.page_number, self.record_count));
                }
                let root = self.flush_branch(file, meta, budget, level_index)?;
                return Ok((root.page_number, self.record_count));
            }
            let node = self.flush_branch(file, meta, budget, level_index)?;
            self.push_node(file, meta, budget, level_index + 1, node)?;
        }
        Err(Error::PageSpaceExhausted)
    }
}
