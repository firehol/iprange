use crate::error::{Error, Result};
use crate::mapping::{ByteSource, PageView};
use crate::metadata::{self, Inflater};

use super::context::Context;
use super::{ValidationObject, ValidationReason, ValidationSink};

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
    let mut path = [0; metadata::MAX_PAGES];
    let mut depth = 0usize;

    while remaining != 0 {
        context.checkpoint()?;
        if depth == metadata::MAX_PAGES {
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
) -> Result<Option<metadata::ParsedPage<PageView<'m>>>> {
    if !metadata::common_header_valid(page) {
        page_finding(context, page_number, ValidationReason::PageHeaderInvalid)?;
        return Ok(None);
    }
    if !metadata::born_valid(page, context.meta.txn_id) {
        page_finding(context, page_number, ValidationReason::PageBornTxnInvalid)?;
        return Ok(None);
    }
    if !metadata::metadata_identity_valid(page) {
        page_finding(context, page_number, ValidationReason::PageTypeMismatch)?;
        return Ok(None);
    }
    let Some(fields) = metadata::chunk_fields(
        page,
        page_number,
        context.meta.page_count,
        expected_offset,
        remaining,
    ) else {
        length_finding(context, Some(page_number))?;
        return Ok(None);
    };
    if !metadata::reserved_zero(page, fields.length) {
        page_finding(context, page_number, ValidationReason::PageReservedNonzero)?;
    }
    Ok(Some(metadata::ParsedPage {
        next: fields.next,
        bytes: metadata::chunk_bytes(page, fields.length)
            .ok_or(Error::Corrupt("metadata payload is outside its page"))?,
    }))
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

struct PageStep {
    next: u32,
    length: u64,
}
