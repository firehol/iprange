use crate::bitmap_page::{self, Header, HeaderProblem, BRANCH_CHILDREN, LEAF_WORDS, MAX_LEVEL};
use crate::error::{Error, Result};
use crate::mapping::ByteSource;

use super::context::Context;
use super::{ValidationObject, ValidationReason, ValidationSink};

mod query;
mod word_cache;

pub(crate) use query::contains;
use query::require_header as require_query_header;
pub(crate) use word_cache::WordCache;

pub(crate) use crate::bitmap_page::Kind;

trait ValidationKind {
    fn object(self) -> ValidationObject;
}

impl ValidationKind for Kind {
    fn object(self) -> ValidationObject {
        match self {
            Kind::Free => ValidationObject::FreeBitmap,
            Kind::Feed => ValidationObject::FeedUsedBitmap,
            Kind::Membership => ValidationObject::MembershipUsedBitmap,
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
    let header =
        match bitmap_page::checked_header(page, context.meta.txn_id, kind, Some(expected_level)) {
            Ok(header) => header,
            Err(problem) => {
                emit_header_problem(context, page_number, kind, problem)?;
                return Ok(None);
            }
        };
    if !bitmap_page::reserved_zero(page, header.level) {
        context.emit(
            ValidationReason::PageReservedNonzero,
            kind.object(),
            Some(page_number),
            None,
            None,
        )?;
    }
    Ok(Some(header))
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
        let word = bitmap_page::leaf_word(page, index)?;
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
        let child = bitmap_page::branch_child(page, index)?;
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
        if bitmap_page::summary_bit(page, index)? != expected {
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

fn coverage(level: u16) -> Result<u64> {
    bitmap_page::coverage(level)
}

fn required_level(limit: u64) -> Result<u16> {
    bitmap_page::required_level(limit)
}

#[derive(Clone, Copy)]
struct NodeResult {
    set_bits: u64,
    has_one: bool,
    has_candidate: bool,
}
