use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;

use super::super::context::Context;
use super::super::ValidationSink;
use super::{coverage, required_level, Kind, BRANCH_TYPE, HEADER_SIZE, LEAF_TYPE};

pub(crate) fn contains<S: ValidationSink>(
    context: &Context<'_, S>,
    root: u32,
    limit: u64,
    kind: Kind,
    bit: u32,
) -> Result<bool> {
    let Some(mut query) = BitmapQuery::new(root, limit, kind, bit)? else {
        return Ok(false);
    };
    loop {
        if let QueryStep::Found(value) = query.step(context)? {
            return Ok(value);
        }
    }
}

struct BitmapQuery {
    page_number: u32,
    level: u16,
    base: u64,
    bit: u32,
    kind: Kind,
}

enum QueryStep {
    Next,
    Found(bool),
}

impl BitmapQuery {
    fn new(root: u32, limit: u64, kind: Kind, bit: u32) -> Result<Option<Self>> {
        if root == 0 || !bit_in_range(kind, bit, limit) {
            return Ok(None);
        }
        Ok(Some(Self {
            page_number: root,
            level: required_level(limit)?,
            base: 0,
            bit,
            kind,
        }))
    }

    fn step<S: ValidationSink>(&mut self, context: &Context<'_, S>) -> Result<QueryStep> {
        let mut page = [0; PAGE_SIZE];
        file_io::read_page(
            context.file,
            self.page_number,
            context.meta.page_count,
            &mut page,
        )?;
        require_header(&page, context.meta.txn_id, self.kind, self.level)?;
        if self.level == 0 {
            return Ok(QueryStep::Found(query_leaf(&page, self.bit, self.base)));
        }
        let Some((child, base)) = query_child(&page, self.bit, self.base, self.level)? else {
            return Ok(QueryStep::Found(false));
        };
        self.page_number = child;
        self.base = base;
        self.level -= 1;
        Ok(QueryStep::Next)
    }
}

fn bit_in_range(kind: Kind, bit: u32, limit: u64) -> bool {
    u64::from(bit) >= kind.first_candidate() && u64::from(bit) < limit
}

fn query_leaf(page: &[u8; PAGE_SIZE], bit: u32, base: u64) -> bool {
    let local = u64::from(bit) - base;
    let word = u64_le(page, HEADER_SIZE + (local / 64) as usize * 8);
    word & (1u64 << (local % 64)) != 0
}

fn query_child(
    page: &[u8; PAGE_SIZE],
    bit: u32,
    base: u64,
    level: u16,
) -> Result<Option<(u32, u64)>> {
    let span = coverage(level - 1)?;
    let index = ((u64::from(bit) - base) / span) as usize;
    let child = u32_le(page, HEADER_SIZE + 32 + index * 4);
    if child == 0 {
        return Ok(None);
    }
    Ok(Some((child, query_child_base(base, span, index)?)))
}

fn query_child_base(base: u64, span: u64, index: usize) -> Result<u64> {
    let offset = span
        .checked_mul(index as u64)
        .ok_or(Error::ArithmeticOverflow("validation bitmap query"))?;
    base.checked_add(offset)
        .ok_or(Error::ArithmeticOverflow("validation bitmap query"))
}

pub(super) fn require_header(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    kind: Kind,
    expected_level: u16,
) -> Result<()> {
    if !header_valid(page, selected_txn, kind, expected_level) {
        return Err(Error::Corrupt(
            "validated bitmap changed during cross-check",
        ));
    }
    Ok(())
}

fn header_valid(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    kind: Kind,
    expected_level: u16,
) -> bool {
    let born = u64_le(page, 8);
    let expected_type = if expected_level == 0 {
        LEAF_TYPE
    } else {
        BRANCH_TYPE
    };
    page[..4] == PAGE_MAGIC
        && page[4] == expected_type
        && page[5] == 0
        && u16_le(page, 6) == HEADER_SIZE as u16
        && born != 0
        && born <= selected_txn
        && u16_le(page, 18) == expected_level
        && u32_le(page, 24) == kind.aux()
}
