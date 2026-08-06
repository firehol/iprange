//! Ordered range-page construction for one append-only immutable output.

use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::{Mapping, PageMut};
use crate::range_bulk::{Builder, Sink};

use super::{reserve_page, OutputBudget};

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
        mapping: &mut Mapping,
        meta: &mut MetaV4,
        budget: OutputBudget,
        record: Record<Ipv4Key>,
    ) -> Result<()> {
        match self {
            Self::V4(ranges) => ranges.push(
                &mut OutputSink {
                    mapping,
                    meta,
                    budget,
                },
                record,
            ),
            Self::V6(_) => Err(Error::WrongAddressFamily(
                "ordered range output is not IPv4",
            )),
        }
    }

    pub(super) fn push_v6(
        &mut self,
        mapping: &mut Mapping,
        meta: &mut MetaV4,
        budget: OutputBudget,
        record: Record<Ipv6Key>,
    ) -> Result<()> {
        match self {
            Self::V6(ranges) => ranges.push(
                &mut OutputSink {
                    mapping,
                    meta,
                    budget,
                },
                record,
            ),
            Self::V4(_) => Err(Error::WrongAddressFamily(
                "ordered range output is not IPv6",
            )),
        }
    }

    pub(super) fn finish(
        &mut self,
        mapping: &mut Mapping,
        meta: &mut MetaV4,
        budget: OutputBudget,
    ) -> Result<(u32, u64)> {
        match self {
            Self::V4(ranges) => ranges.finish(&mut OutputSink {
                mapping,
                meta,
                budget,
            }),
            Self::V6(ranges) => ranges.finish(&mut OutputSink {
                mapping,
                meta,
                budget,
            }),
        }
    }
}

struct OutputSink<'a> {
    mapping: &'a mut Mapping,
    meta: &'a mut MetaV4,
    budget: OutputBudget,
}

impl Sink for OutputSink<'_> {
    type WritePage<'a>
        = PageMut<'a>
    where
        Self: 'a;

    fn allocate(&mut self) -> Result<u32> {
        reserve_page(self.meta, self.budget)
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        if page_number < 2 || u64::from(page_number) >= self.meta.page_count {
            return Err(Error::Corrupt("immutable range page is outside bounds"));
        }
        let mut page = self.mapping.page_mut(page_number, self.meta.page_count)?;
        update(&mut page)
    }
}
