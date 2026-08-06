use crate::contract::{u16_le, u32_le, ValueKind};
use crate::error::Result;
use crate::feed::{FeedEntry, FeedName, MAX_FEED_NAME};
use crate::feed_catalog::{self, FeedCursor};
use crate::mapping::ByteSource;

use super::bitmap::{self, Kind};
use super::context::Context;
use super::tree::{self, CellLayout, Codec};
use super::{ValidationObject, ValidationReason, ValidationSink};

const NAME_BRANCH: u8 = 3;
const NAME_LEAF: u8 = 4;
const INDEX_BRANCH: u8 = 5;
const INDEX_LEAF: u8 = 6;
const RECORD_BASE: usize = 12;
const MIN_RECORD: usize = RECORD_BASE + 1;
const MAX_RECORD: usize = RECORD_BASE + MAX_FEED_NAME;

struct NameCodec;

impl Codec for NameCodec {
    type Key = FeedName;

    const BRANCH_TYPE: u8 = NAME_BRANCH;
    const LEAF_TYPE: u8 = NAME_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: MIN_RECORD,
        maximum: MAX_RECORD,
    };
    const LEAF_LAYOUT: CellLayout = Self::BRANCH_LAYOUT;
    const BRANCH_INVALID: ValidationReason = ValidationReason::CatalogNameInvalid;
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogNameInvalid;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        decode(cell).map(|entry| entry.name)
    }

    fn branch_child<P: ByteSource>(cell: P) -> Option<u32> {
        decode(cell).map(|entry| entry.index)
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        decode(cell).map(|entry| entry.name)
    }
}

struct IndexCodec;

impl Codec for IndexCodec {
    type Key = u32;

    const BRANCH_TYPE: u8 = INDEX_BRANCH;
    const LEAF_TYPE: u8 = INDEX_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(8);
    const LEAF_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: MIN_RECORD,
        maximum: MAX_RECORD,
    };
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogNameInvalid;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        Some(u32_le(cell, 0))
    }

    fn branch_child<P: ByteSource>(cell: P) -> Option<u32> {
        Some(u32_le(cell, 4))
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        decode(cell).map(|entry| entry.index)
    }
}

pub(crate) fn validate<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    if context.meta.value_kind != ValueKind::Membership {
        return Ok(());
    }
    validate_trees(context)?;
    validate_used_bitmap(context)?;
    cross_check(context)
}

fn validate_trees<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    let name_root = context.meta.catalog_name_root;
    let names = tree::walk::<NameCodec, S, _>(
        context,
        name_root,
        ValidationObject::CatalogNameTree,
        validate_record,
    )?;
    let index_root = context.meta.catalog_index_root;
    let indexes = tree::walk::<IndexCodec, S, _>(
        context,
        index_root,
        ValidationObject::CatalogIndexTree,
        validate_record,
    )?;
    if names.records != context.meta.active_feed_count
        || indexes.records != context.meta.active_feed_count
    {
        count_mismatch(context)?;
    }
    Ok(())
}

fn validate_used_bitmap<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    let used_root = context.meta.feed_used_root;
    let limit = context.meta.feed_index_limit;
    let used = bitmap::validate(context, used_root, limit, Kind::Feed)?;
    if used != context.meta.active_feed_count {
        context.emit(
            ValidationReason::CatalogBitmapInvalid,
            ValidationObject::FeedUsedBitmap,
            None,
            None,
            None,
        )?;
    }
    Ok(())
}

fn validate_record<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    cell: P,
) -> Result<()> {
    let Some(entry) = decode(cell) else {
        return Ok(());
    };
    if u64::from(entry.index) >= context.meta.feed_index_limit {
        context.emit(
            ValidationReason::CatalogBijectionInvalid,
            ValidationObject::CatalogIndexTree,
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(())
}

fn cross_check<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    let mut cursor = match FeedCursor::new(context.mapping, &context.meta) {
        Ok(cursor) => cursor,
        Err(_) => return bijection_finding(context),
    };
    loop {
        context.checkpoint()?;
        let entry = match cursor.next_feed() {
            Ok(Some(entry)) => entry,
            Ok(None) => return Ok(()),
            Err(_) => return bijection_finding(context),
        };
        if !pair_matches(context, entry)? {
            bijection_finding(context)?;
        }
    }
}

fn pair_matches<S: ValidationSink>(context: &Context<'_, S>, entry: FeedEntry) -> Result<bool> {
    let name = match feed_catalog::lookup(context.mapping, &context.meta, &entry.name) {
        Ok(name) => name,
        Err(_) => return Ok(false),
    };
    if name != Some(entry) {
        return Ok(false);
    }
    bitmap::contains(
        context,
        context.meta.feed_used_root,
        context.meta.feed_index_limit,
        Kind::Feed,
        entry.index,
    )
    .or(Ok(false))
}

fn decode<P: ByteSource>(cell: P) -> Option<FeedEntry> {
    let name_len = usize::from(cell.byte(8)?);
    if cell.len() != RECORD_BASE + name_len
        || usize::from(u16_le(cell, 0)) != cell.len()
        || u16_le(cell, 2) != 0
        || !cell.all_zero(9, 3)
    {
        return None;
    }
    Some(FeedEntry {
        index: u32_le(cell, 4),
        name: FeedName::from_source(cell, RECORD_BASE, name_len)?,
    })
}

fn count_mismatch<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    context.emit(
        ValidationReason::RootCountInvalid,
        ValidationObject::CatalogIndexTree,
        None,
        None,
        None,
    )
}

fn bijection_finding<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    context.emit(
        ValidationReason::CatalogBijectionInvalid,
        ValidationObject::CatalogIndexTree,
        None,
        None,
        None,
    )
}
