use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::mapping::ByteSource;

use super::context::Context;
use super::{ValidationObject, ValidationReason, ValidationSink};

mod query;
mod word_cache;

pub(crate) use query::contains;
use query::require_header as require_query_header;
pub(crate) use word_cache::WordCache;

const BRANCH_TYPE: u8 = 14;
const LEAF_TYPE: u8 = 15;
const HEADER_SIZE: usize = 32;
const LEAF_WORDS: usize = 500;
const LEAF_BITS: u64 = (LEAF_WORDS * 64) as u64;
const BRANCH_CHILDREN: usize = 256;
const LEAF_END: usize = HEADER_SIZE + LEAF_WORDS * 8;
const BRANCH_END: usize = HEADER_SIZE + 32 + BRANCH_CHILDREN * 4;
const MAX_LEVEL: u16 = 3;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Kind {
    Free,
    Feed,
    Membership,
}

impl Kind {
    fn aux(self) -> u32 {
        match self {
            Self::Free => 1,
            Self::Feed => 2,
            Self::Membership => 3,
        }
    }

    fn first_candidate(self) -> u64 {
        match self {
            Self::Membership => 1,
            Self::Free | Self::Feed => 0,
        }
    }

    fn object(self) -> ValidationObject {
        match self {
            Self::Free => ValidationObject::FreeBitmap,
            Self::Feed => ValidationObject::FeedUsedBitmap,
            Self::Membership => ValidationObject::MembershipUsedBitmap,
        }
    }
}

pub(crate) fn validate<S: ValidationSink>(
    context: &mut Context<'_, S>,
    root: u32,
    limit: u64,
    kind: Kind,
) -> Result<u64> {
    if root == 0 {
        return Ok(0);
    }
    if limit == 0 {
        context.emit(
            ValidationReason::BitmapSummaryInvalid,
            kind.object(),
            Some(root),
            None,
            None,
        )?;
        return Ok(0);
    }

    let required = required_level(limit)?;
    let mut path = [0; MAX_LEVEL as usize + 1];
    let result = validate_node(context, root, required, 0, limit, kind, &mut path, 0)?;
    Ok(result.map_or(0, |result| result.set_bits))
}

#[allow(clippy::too_many_arguments)]
fn validate_node<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    expected_level: u16,
    base: u64,
    limit: u64,
    kind: Kind,
    path: &mut [u32; MAX_LEVEL as usize + 1],
    depth: usize,
) -> Result<Option<NodeResult>> {
    let Some(slot) = path.get_mut(depth) else {
        context.emit(
            ValidationReason::TreeLevelInvalid,
            kind.object(),
            Some(page_number),
            None,
            None,
        )?;
        return Ok(None);
    };
    *slot = page_number;
    let Some(page) = context.read_graph_page(page_number, kind.object(), &path[..depth])? else {
        return Ok(None);
    };
    let Some(header) = parse_header(context, page_number, page, expected_level, kind)? else {
        return Ok(None);
    };
    if header.level == 0 {
        validate_leaf(context, page_number, page, base, limit, kind, header)
    } else {
        validate_branch(
            context,
            page_number,
            page,
            base,
            limit,
            kind,
            header,
            path,
            depth,
        )
    }
}

fn parse_header<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    expected_level: u16,
    kind: Kind,
) -> Result<Option<Header>> {
    let level = u16_le(page, 18);
    let lower = if level == 0 { LEAF_END } else { BRANCH_END };
    if let Some(problem) = header_problem(page, context.meta.txn_id, expected_level, kind, lower) {
        emit_header_problem(context, page_number, kind, problem)?;
        return Ok(None);
    }
    if !page.all_zero(lower, PAGE_SIZE - lower) {
        context.emit(
            ValidationReason::PageReservedNonzero,
            kind.object(),
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(Some(Header {
        level,
        item_count: usize::from(u16_le(page, 16)),
    }))
}

#[derive(Clone, Copy)]
enum HeaderProblem {
    Header,
    Born,
    Level,
    Type,
}

fn header_problem<P: ByteSource>(
    page: P,
    txn_id: u64,
    expected_level: u16,
    kind: Kind,
    lower: usize,
) -> Option<HeaderProblem> {
    if !common_header_valid(page, lower) {
        return Some(HeaderProblem::Header);
    }
    if !born_valid(page, txn_id) {
        return Some(HeaderProblem::Born);
    }
    let level = u16_le(page, 18);
    if level > MAX_LEVEL || level != expected_level {
        return Some(HeaderProblem::Level);
    }
    if !page_kind_valid(page, level, kind) {
        return Some(HeaderProblem::Type);
    }
    None
}

fn common_header_valid<P: ByteSource>(page: P, lower: usize) -> bool {
    page.equals(0, &PAGE_MAGIC)
        && page.byte(5) == Some(0)
        && u16_le(page, 6) == HEADER_SIZE as u16
        && usize::from(u16_le(page, 20)) == lower
        && usize::from(u16_le(page, 22)) == PAGE_SIZE
}

fn born_valid<P: ByteSource>(page: P, txn_id: u64) -> bool {
    let born = u64_le(page, 8);
    born != 0 && born <= txn_id
}

fn page_kind_valid<P: ByteSource>(page: P, level: u16, kind: Kind) -> bool {
    let expected_type = if level == 0 { LEAF_TYPE } else { BRANCH_TYPE };
    page.byte(4) == Some(expected_type) && u32_le(page, 24) == kind.aux()
}

fn emit_header_problem<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    kind: Kind,
    problem: HeaderProblem,
) -> Result<()> {
    let reason = match problem {
        HeaderProblem::Header => ValidationReason::PageHeaderInvalid,
        HeaderProblem::Born => ValidationReason::PageBornTxnInvalid,
        HeaderProblem::Level => ValidationReason::TreeLevelInvalid,
        HeaderProblem::Type => ValidationReason::PageTypeMismatch,
    };
    context.emit(reason, kind.object(), Some(page_number), None, None)
}

fn validate_leaf<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    base: u64,
    limit: u64,
    kind: Kind,
    header: Header,
) -> Result<Option<NodeResult>> {
    let mut totals = LeafTotals::default();
    for index in 0..LEAF_WORDS {
        context.checkpoint()?;
        let word = u64_le(page, HEADER_SIZE + index * 8);
        let (word_base, valid, valid_mask) =
            validate_leaf_word(context, page_number, base, limit, kind, index, word)?;
        totals.add(context, word_base, word, valid, valid_mask, kind)?;
    }
    if totals.nonzero_words != header.item_count {
        context.emit(
            ValidationReason::PageHeaderInvalid,
            kind.object(),
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(Some(totals.result()))
}

#[derive(Default)]
struct LeafTotals {
    nonzero_words: usize,
    set_bits: u64,
    has_candidate: bool,
}

impl LeafTotals {
    fn add<S: ValidationSink>(
        &mut self,
        context: &mut Context<'_, S>,
        word_base: u64,
        word: u64,
        valid: u64,
        valid_mask: u64,
        kind: Kind,
    ) -> Result<()> {
        self.nonzero_words += usize::from(word != 0);
        self.set_bits = self
            .set_bits
            .checked_add(u64::from(valid.count_ones()))
            .ok_or(Error::ArithmeticOverflow("validation bitmap bit count"))?;
        match kind {
            Kind::Free => mark_free_bits(context, word_base, valid)?,
            Kind::Feed | Kind::Membership => {
                self.has_candidate |= (!valid) & valid_mask != 0;
            }
        }
        Ok(())
    }

    fn result(self) -> NodeResult {
        NodeResult {
            set_bits: self.set_bits,
            has_one: self.set_bits != 0,
            has_candidate: self.has_candidate,
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn validate_leaf_word<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    base: u64,
    limit: u64,
    kind: Kind,
    index: usize,
    word: u64,
) -> Result<(u64, u64, u64)> {
    let word_base = base
        .checked_add((index * 64) as u64)
        .ok_or(Error::ArithmeticOverflow("validation bitmap word offset"))?;
    let valid_mask = in_range_mask(word_base, limit, kind);
    if word & !valid_mask != 0 {
        context.emit(
            ValidationReason::BitmapSummaryInvalid,
            kind.object(),
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok((word_base, word & valid_mask, valid_mask))
}

#[allow(clippy::too_many_arguments)]
fn validate_branch<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    page_number: u32,
    page: P,
    base: u64,
    limit: u64,
    kind: Kind,
    header: Header,
    path: &mut [u32; MAX_LEVEL as usize + 1],
    depth: usize,
) -> Result<Option<NodeResult>> {
    let child_span = coverage(header.level - 1)?;
    let mut totals = BranchTotals::default();
    for index in 0..BRANCH_CHILDREN {
        context.checkpoint()?;
        let child = u32_le(page, HEADER_SIZE + 32 + index * 4);
        totals.child_count += usize::from(child != 0);
        let result = validate_branch_child(
            context,
            child,
            index,
            base,
            child_span,
            limit,
            kind,
            header.level,
            path,
            depth,
        )?;
        let Some(result) = result else {
            continue;
        };
        totals.add(context, page_number, page, index, kind, result)?;
    }
    if totals.child_count != header.item_count {
        context.emit(
            ValidationReason::PageHeaderInvalid,
            kind.object(),
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(Some(totals.result()))
}

#[derive(Default)]
struct BranchTotals {
    child_count: usize,
    set_bits: u64,
    has_one: bool,
    has_candidate: bool,
}

impl BranchTotals {
    fn add<S: ValidationSink, P: ByteSource>(
        &mut self,
        context: &mut Context<'_, S>,
        page_number: u32,
        page: P,
        index: usize,
        kind: Kind,
        result: NodeResult,
    ) -> Result<()> {
        let expected = match kind {
            Kind::Free => result.has_one,
            Kind::Feed | Kind::Membership => result.has_candidate,
        };
        if summary_bit(page, index) != expected {
            context.emit(
                ValidationReason::BitmapSummaryInvalid,
                kind.object(),
                Some(page_number),
                None,
                None,
            )?;
        }
        self.set_bits = self
            .set_bits
            .checked_add(result.set_bits)
            .ok_or(Error::ArithmeticOverflow("validation bitmap bit count"))?;
        self.has_one |= result.has_one;
        self.has_candidate |= result.has_candidate;
        Ok(())
    }

    fn result(self) -> NodeResult {
        NodeResult {
            set_bits: self.set_bits,
            has_one: self.has_one,
            has_candidate: self.has_candidate,
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn validate_branch_child<S: ValidationSink>(
    context: &mut Context<'_, S>,
    child: u32,
    index: usize,
    base: u64,
    child_span: u64,
    limit: u64,
    kind: Kind,
    level: u16,
    path: &mut [u32; MAX_LEVEL as usize + 1],
    depth: usize,
) -> Result<Option<NodeResult>> {
    let child_base = bitmap_child_base(base, child_span, index)?;
    if child == 0 {
        return Ok(Some(absent_result(child_base, child_span, limit, kind)));
    }
    validate_node(
        context,
        child,
        level - 1,
        child_base,
        limit,
        kind,
        path,
        depth + 1,
    )
}

fn bitmap_child_base(base: u64, span: u64, index: usize) -> Result<u64> {
    let offset = span
        .checked_mul(index as u64)
        .ok_or(Error::ArithmeticOverflow("validation bitmap child offset"))?;
    base.checked_add(offset)
        .ok_or(Error::ArithmeticOverflow("validation bitmap child offset"))
}

fn absent_result(base: u64, span: u64, limit: u64, kind: Kind) -> NodeResult {
    let end = base.saturating_add(span).min(limit);
    NodeResult {
        set_bits: 0,
        has_one: false,
        has_candidate: base.max(kind.first_candidate()) < end,
    }
}

fn mark_free_bits<S: ValidationSink>(
    context: &mut Context<'_, S>,
    word_base: u64,
    mut word: u64,
) -> Result<()> {
    while word != 0 {
        let bit = u64::from(word.trailing_zeros());
        let page = word_base
            .checked_add(bit)
            .ok_or(Error::ArithmeticOverflow("validation free-page number"))?;
        context.mark_allocated(page as u32, ValidationObject::FreeBitmap)?;
        word &= word - 1;
    }
    Ok(())
}

fn in_range_mask(base: u64, limit: u64, kind: Kind) -> u64 {
    if base >= limit {
        return 0;
    }
    let valid = (limit - base).min(64);
    let mut mask = if valid == 64 {
        u64::MAX
    } else {
        (1u64 << valid) - 1
    };
    let first = kind.first_candidate();
    if base < first {
        let excluded = (first - base).min(64);
        mask &= u64::MAX.checked_shl(excluded as u32).unwrap_or(0);
    }
    if kind == Kind::Free && base == 0 {
        mask &= !3;
    }
    mask
}

fn summary_bit<P: ByteSource>(page: P, index: usize) -> bool {
    u64_le(page, HEADER_SIZE + (index / 64) * 8) & (1u64 << (index % 64)) != 0
}

fn coverage(level: u16) -> Result<u64> {
    let mut value = LEAF_BITS;
    for _ in 0..level {
        value = value
            .checked_mul(BRANCH_CHILDREN as u64)
            .ok_or(Error::ArithmeticOverflow("validation bitmap coverage"))?;
    }
    Ok(value)
}

fn required_level(limit: u64) -> Result<u16> {
    for level in 0..=MAX_LEVEL {
        if coverage(level)? >= limit {
            return Ok(level);
        }
    }
    Err(Error::ArithmeticOverflow("validation bitmap limit"))
}

#[derive(Clone, Copy)]
struct Header {
    level: u16,
    item_count: usize,
}

#[derive(Clone, Copy)]
struct NodeResult {
    set_bits: u64,
    has_one: bool,
    has_candidate: bool,
}
