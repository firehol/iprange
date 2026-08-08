//! Reconciliation of the redundant recovery-readable feed catalogs.

use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, MetaV4};
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::feed_catalog;
use crate::mapping::{ByteSource, Mapping};
use crate::validation::{ValidationObject, ValidationReason};

use super::catalog_table::Builder;
pub(crate) use super::catalog_table::Catalog;
use super::page_set::PageSet;
use super::report::{emit_page_unknown as emit, RecoverySink, Reporter};
use super::tables::Tables;
use super::tree_scan::{self, CellLayout, Codec, TreeEvents};

pub(crate) fn count(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
) -> Result<u64> {
    let mut events = CountEvents { meta, count: 0 };
    tree_scan::scan::<NameCodec, _>(
        mapping,
        meta,
        meta.catalog_name_root,
        pages,
        cancellation,
        &mut events,
    )?;
    tree_scan::scan::<IndexCodec, _>(
        mapping,
        meta,
        meta.catalog_index_root,
        pages,
        cancellation,
        &mut events,
    )?;
    Ok(events.count)
}

pub(crate) fn recover<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    tables: &mut Tables,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<Catalog> {
    let mut builder = Builder::new(tables);
    {
        let mut events = Events {
            meta,
            object: NameCodec::OBJECT,
            reporter,
            builder: &mut builder,
        };
        tree_scan::scan::<NameCodec, _>(
            mapping,
            meta,
            meta.catalog_name_root,
            pages,
            cancellation,
            &mut events,
        )?;
        events.object = IndexCodec::OBJECT;
        tree_scan::scan::<IndexCodec, _>(
            mapping,
            meta,
            meta.catalog_index_root,
            pages,
            cancellation,
            &mut events,
        )?;
    }
    builder.finish(reporter)
}

struct Events<'a, 'b, 'c, S> {
    meta: MetaV4,
    object: ValidationObject,
    reporter: &'a mut Reporter<'b, S>,
    builder: &'a mut Builder<'c>,
}

impl<S: RecoverySink> TreeEvents for Events<'_, '_, '_, S> {
    fn page_accepted(&mut self) -> Result<()> {
        self.reporter.page_accepted()
    }

    fn page_rejected(&mut self, io_unreadable: bool) -> Result<()> {
        self.reporter.page_rejected(io_unreadable)
    }

    fn unknown(
        &mut self,
        reason: ValidationReason,
        object: ValidationObject,
        page: Option<u32>,
    ) -> Result<()> {
        emit(self.reporter, reason, object, page)
    }

    fn leaf<P: ByteSource>(&mut self, page: u32, _index: usize, cell: Option<P>) -> Result<()> {
        self.reporter.catalog_examined()?;
        let Some(entry) = cell.and_then(|cell| feed_catalog::decode_entry(cell).ok()) else {
            return self.reporter.catalog_rejected(1);
        };
        if u64::from(entry.index) >= self.meta.feed_index_limit {
            self.reporter.catalog_rejected(1)?;
            return emit(
                self.reporter,
                ValidationReason::CatalogInvalid,
                self.object,
                Some(page),
            );
        }
        self.builder.push(entry, self.reporter)
    }
}

struct CountEvents {
    meta: MetaV4,
    count: u64,
}

impl TreeEvents for CountEvents {
    fn page_accepted(&mut self) -> Result<()> {
        Ok(())
    }

    fn page_rejected(&mut self, _io_unreadable: bool) -> Result<()> {
        Ok(())
    }

    fn unknown(
        &mut self,
        _reason: ValidationReason,
        _object: ValidationObject,
        _page: Option<u32>,
    ) -> Result<()> {
        Ok(())
    }

    fn leaf<P: ByteSource>(&mut self, _page: u32, _index: usize, cell: Option<P>) -> Result<()> {
        let Some(entry) = cell.and_then(|cell| feed_catalog::decode_entry(cell).ok()) else {
            return Ok(());
        };
        if u64::from(entry.index) < self.meta.feed_index_limit {
            self.count = self
                .count
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("recovery catalog count"))?;
        }
        Ok(())
    }
}

struct NameCodec;

impl Codec for NameCodec {
    type Key = FeedName;

    const OBJECT: ValidationObject = ValidationObject::CatalogNameTree;
    const BRANCH_TYPE: u8 = feed_catalog::NAME_BRANCH;
    const LEAF_TYPE: u8 = feed_catalog::NAME_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: feed_catalog::NAME_RECORD_BASE + 1,
        maximum: feed_catalog::MAX_NAME_RECORD,
    };
    const LEAF_LAYOUT: CellLayout = Self::BRANCH_LAYOUT;
    const BRANCH_INVALID: ValidationReason = ValidationReason::CatalogInvalid;
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogInvalid;

    fn branch<P: ByteSource>(cell: P) -> Option<(Self::Key, u32)> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| (entry.name, entry.index))
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| entry.name)
    }
}

struct IndexCodec;

impl Codec for IndexCodec {
    type Key = u32;

    const OBJECT: ValidationObject = ValidationObject::CatalogIndexTree;
    const BRANCH_TYPE: u8 = feed_catalog::INDEX_BRANCH;
    const LEAF_TYPE: u8 = feed_catalog::INDEX_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(8);
    const LEAF_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: feed_catalog::NAME_RECORD_BASE + 1,
        maximum: feed_catalog::MAX_NAME_RECORD,
    };
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogInvalid;

    fn branch<P: ByteSource>(cell: P) -> Option<(Self::Key, u32)> {
        Some((u32_le(cell, 0), u32_le(cell, 4)))
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| entry.index)
    }
}
