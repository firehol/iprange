//! Ordered range-page construction for one append-only immutable output.

use std::fs::File;

use crate::contract::{AddressFamily, MetaV4, ValueKind, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_bulk::{Builder, Sink};

use super::{reserve_page, write_page, OutputBudget};

pub(super) use crate::range_bulk::Record;

#[derive(Debug)]
// Both variants own the same fixed page workspace; boxing one would add heap
// work while reducing the outer value by less than one page.
#[allow(clippy::large_enum_variant)]
pub(super) enum Ranges {
    V4(Builder<Ipv4Key>),
    V6(Builder<Ipv6Key>),
}

impl Ranges {
    pub(super) fn new(family: AddressFamily, born_txn: u64, value_kind: ValueKind) -> Self {
        match family {
            AddressFamily::Ipv4 => Self::V4(Builder::new(born_txn, value_kind)),
            AddressFamily::Ipv6 => Self::V6(Builder::new(born_txn, value_kind)),
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
            Self::V4(ranges) => ranges.push(&mut OutputSink { file, meta, budget }, record),
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
            Self::V6(ranges) => ranges.push(&mut OutputSink { file, meta, budget }, record),
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
            Self::V4(ranges) => ranges.finish(&mut OutputSink { file, meta, budget }),
            Self::V6(ranges) => ranges.finish(&mut OutputSink { file, meta, budget }),
        }
    }
}

struct OutputSink<'a> {
    file: &'a File,
    meta: &'a mut MetaV4,
    budget: OutputBudget,
}

impl Sink for OutputSink<'_> {
    fn allocate(&mut self) -> Result<u32> {
        reserve_page(self.meta, self.budget)
    }

    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        write_page(self.file, self.meta, page_number, page)
    }
}
