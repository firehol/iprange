use crate::contract::ValueKind;
use crate::error::Result;
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog::{self, FeedCursor};
use crate::mapping::ByteSource;

use super::bitmap::{self, Kind};
use super::context::Context;
use super::tree::{self, CellLayout, Codec};
use super::{ValidationObject, ValidationReason, ValidationSink};

struct NameCodec;

impl Codec for NameCodec {
    type Key = FeedName;

    const BRANCH_TYPE: u8 = feed_catalog::NAME_BRANCH;
    const LEAF_TYPE: u8 = feed_catalog::NAME_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: feed_catalog::MIN_NAME_RECORD,
        maximum: feed_catalog::MAX_NAME_RECORD,
    };
    const LEAF_LAYOUT: CellLayout = Self::BRANCH_LAYOUT;
    const BRANCH_INVALID: ValidationReason = ValidationReason::CatalogNameInvalid;
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogNameInvalid;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| entry.name)
    }

    fn branch_child<P: ByteSource>(cell: P) -> Option<u32> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| entry.index)
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

    const BRANCH_TYPE: u8 = feed_catalog::INDEX_BRANCH;
    const LEAF_TYPE: u8 = feed_catalog::INDEX_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(feed_catalog::INDEX_BRANCH_SIZE);
    const LEAF_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: feed_catalog::MIN_NAME_RECORD,
        maximum: feed_catalog::MAX_NAME_RECORD,
    };
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogNameInvalid;

    fn branch_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        feed_catalog::decode_index_branch(cell)
            .ok()
            .map(|(index, _)| index)
    }

    fn branch_child<P: ByteSource>(cell: P) -> Option<u32> {
        feed_catalog::decode_index_branch(cell)
            .ok()
            .map(|(_, child)| child)
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| entry.index)
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
    let Ok(entry) = feed_catalog::decode_entry(cell) else {
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
    let mut cursor = match FeedCursor::new(context.mapping, &context.meta, None) {
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
