use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::format::page_type;
use crate::mapping::{ByteRange, ByteSource, PageView};
use crate::metadata::Inflater;

use super::context::Context;
use super::{ValidationObject, ValidationReason, ValidationSink};

const PAGE_TYPE: u8 = page_type::METADATA;
const HEADER_SIZE: usize = 32;
const DATA_OFFSET: usize = 48;
const CHUNK_CAPACITY: usize = PAGE_SIZE - DATA_OFFSET;
const MAX_PAGES: usize = 260;
const INFLATER_OVERHEAD: u64 = 64 * 1024;

pub(crate) fn validate<S: ValidationSink>(context: &mut Context<'_, S>) -> Result<()> {
    if context.meta.metadata_root == 0 {
        return Ok(());
    }
    let output_len = usize::try_from(context.meta.metadata_uncompressed_len)
        .map_err(|_| Error::BudgetExceeded("validation metadata output"))?;
    let retained = context
        .meta
        .metadata_uncompressed_len
        .checked_add(INFLATER_OVERHEAD)
        .ok_or(Error::ArithmeticOverflow("validation metadata heap"))?;
    context.reserve_heap(retained, "validation metadata output")?;
    let mut output = Vec::new();
    output
        .try_reserve_exact(output_len)
        .map_err(|_| Error::BudgetExceeded("validation metadata output"))?;
    output.resize(output_len, 0);

    let result = validate_chain(context, &mut output);
    drop(output);
    context.release_heap(retained);
    result
}

fn validate_chain<S: ValidationSink>(
    context: &mut Context<'_, S>,
    output: &mut [u8],
) -> Result<()> {
    let mut inflater = Some(Inflater::new(output));
    let mut page_number = context.meta.metadata_root;
    let mut offset = 0u64;
    let mut remaining = context.meta.metadata_compressed_len;
    let mut path = [0; MAX_PAGES];
    let mut depth = 0usize;

    while remaining != 0 {
        context.checkpoint()?;
        if depth == MAX_PAGES {
            length_finding(context, Some(page_number))?;
            context.mark_untraversable(false)?;
            return Ok(());
        }
        path[depth] = page_number;
        let Some(step) = consume_page(
            context,
            &mut inflater,
            page_number,
            offset,
            remaining,
            &path[..depth],
        )?
        else {
            return Ok(());
        };
        offset = offset
            .checked_add(step.length)
            .ok_or(Error::ArithmeticOverflow("validation metadata offset"))?;
        remaining -= step.length;
        page_number = step.next;
        depth += 1;
    }
    finish_chain(context, inflater, page_number)
}

fn consume_page<S: ValidationSink>(
    context: &mut Context<'_, S>,
    inflater: &mut Option<Inflater<'_>>,
    page_number: u32,
    offset: u64,
    remaining: u64,
    path: &[u32],
) -> Result<Option<PageStep>> {
    let Some(page) = context.read_graph_page(page_number, ValidationObject::Metadata, path)? else {
        return Ok(None);
    };
    let Some(chunk) = parse_page(context, page_number, page, offset, remaining)? else {
        context.mark_untraversable(false)?;
        return Ok(None);
    };
    feed_chunk(context, inflater, page_number, chunk.bytes)?;
    Ok(Some(PageStep {
        next: chunk.next,
        length: chunk.bytes.len() as u64,
    }))
}

fn feed_chunk<S: ValidationSink, P: ByteSource>(
    context: &mut Context<'_, S>,
    inflater: &mut Option<Inflater<'_>>,
    page_number: u32,
    bytes: P,
) -> Result<()> {
    let Some(decoder) = inflater.as_mut() else {
        return Ok(());
    };
    if decoder.feed_source(bytes).is_ok() {
        return Ok(());
    }
    context.emit(
        ValidationReason::MetadataZlibInvalid,
        ValidationObject::Metadata,
        Some(page_number),
        None,
        None,
    )?;
    *inflater = None;
    Ok(())
}

fn finish_chain<S: ValidationSink>(
    context: &mut Context<'_, S>,
    inflater: Option<Inflater<'_>>,
    next: u32,
) -> Result<()> {
    if next != 0 {
        length_finding(context, Some(next))?;
    }
    let Some(decoder) = inflater else {
        return Ok(());
    };
    if decoder
        .finish(context.meta.metadata_compressed_len)
        .is_err()
    {
        context.emit(
            ValidationReason::MetadataZlibInvalid,
            ValidationObject::Metadata,
            None,
            None,
            None,
        )?;
    }
    Ok(())
}

fn parse_page<'m, S: ValidationSink>(
    context: &mut Context<'m, S>,
    page_number: u32,
    page: PageView<'m>,
    expected_offset: u64,
    remaining: u64,
) -> Result<Option<Chunk<PageView<'m>>>> {
    let length = usize::from(u16_le(page, 36));
    let expected_length = remaining.min(CHUNK_CAPACITY as u64) as usize;
    let final_chunk = remaining == length as u64;
    let next = u32_le(page, 32);
    if let Some(problem) = page_problem(
        page,
        context.meta.txn_id,
        length,
        expected_length,
        expected_offset,
        next,
        final_chunk,
    ) {
        report_page_problem(context, page_number, problem)?;
        return Ok(None);
    };
    if !page.all_zero(DATA_OFFSET + length, PAGE_SIZE - DATA_OFFSET - length) {
        page_finding(context, page_number, ValidationReason::PageReservedNonzero)?;
    }
    Ok(Some(Chunk {
        next,
        bytes: ByteRange::new(page, DATA_OFFSET, length)
            .ok_or(Error::Corrupt("metadata payload is outside its page"))?,
    }))
}

#[derive(Clone, Copy)]
enum PageProblem {
    Header,
    Born,
    Type,
    Length,
}

#[allow(clippy::too_many_arguments)]
fn page_problem<P: ByteSource>(
    page: P,
    txn_id: u64,
    length: usize,
    expected_length: usize,
    expected_offset: u64,
    next: u32,
    final_chunk: bool,
) -> Option<PageProblem> {
    if !common_header(page) {
        return Some(PageProblem::Header);
    }
    if !born_valid(page, txn_id) {
        return Some(PageProblem::Born);
    }
    if !metadata_identity(page) {
        return Some(PageProblem::Type);
    }
    if !chunk_geometry(page, length, expected_length, expected_offset) {
        return Some(PageProblem::Length);
    }
    if !link_valid(next, final_chunk) {
        return Some(PageProblem::Length);
    }
    None
}

fn report_page_problem<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    problem: PageProblem,
) -> Result<()> {
    match problem {
        PageProblem::Header => {
            page_finding(context, page_number, ValidationReason::PageHeaderInvalid)
        }
        PageProblem::Born => {
            page_finding(context, page_number, ValidationReason::PageBornTxnInvalid)
        }
        PageProblem::Type => page_finding(context, page_number, ValidationReason::PageTypeMismatch),
        PageProblem::Length => length_finding(context, Some(page_number)),
    }
}

fn common_header<P: ByteSource>(page: P) -> bool {
    page.equals(0, &PAGE_MAGIC) && page.byte(5) == Some(0) && u16_le(page, 6) == HEADER_SIZE as u16
}

fn born_valid<P: ByteSource>(page: P, txn_id: u64) -> bool {
    let born = u64_le(page, 8);
    born != 0 && born <= txn_id
}

fn metadata_identity<P: ByteSource>(page: P) -> bool {
    page.byte(4) == Some(PAGE_TYPE) && u32_le(page, 24) == 0
}

fn chunk_geometry<P: ByteSource>(
    page: P,
    length: usize,
    expected_length: usize,
    expected_offset: u64,
) -> bool {
    u16_le(page, 16) == 1
        && u16_le(page, 18) == 0
        && length == expected_length
        && u16_le(page, 38) == 0
        && u64_le(page, 40) == expected_offset
        && usize::from(u16_le(page, 20)) == DATA_OFFSET + length
        && usize::from(u16_le(page, 22)) == PAGE_SIZE
}

fn link_valid(next: u32, final_chunk: bool) -> bool {
    (final_chunk && next == 0) || (!final_chunk && next != 0)
}

fn page_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: u32,
    reason: ValidationReason,
) -> Result<()> {
    context.emit(
        reason,
        ValidationObject::Metadata,
        Some(page_number),
        None,
        None,
    )
}

fn length_finding<S: ValidationSink>(
    context: &mut Context<'_, S>,
    page_number: Option<u32>,
) -> Result<()> {
    context.emit(
        ValidationReason::MetadataLengthInvalid,
        ValidationObject::Metadata,
        page_number,
        None,
        None,
    )
}

struct Chunk<P> {
    next: u32,
    bytes: ByteRange<P>,
}

struct PageStep {
    next: u32,
    length: u64,
}
