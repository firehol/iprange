use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, MetaV4};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, Mapping, PageView};
use crate::metadata::{self, Inflater};
use crate::validation::{ValidationObject, ValidationReason};

use super::page_set::PageSet;
use super::report::{emit_page_unknown, RecoverySink, Reporter};

pub(crate) fn read<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    max_heap_bytes: u64,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<Option<Vec<u8>>> {
    if meta.metadata_root == 0 {
        return Ok(None);
    }
    let mut output = output_buffer(meta, pages, max_heap_bytes)?;
    let complete = scan(mapping, meta, pages, cancellation, reporter, &mut output)?;
    reporter.metadata_finished(complete)?;
    Ok(complete.then_some(output))
}

fn output_buffer(meta: MetaV4, pages: &PageSet, max_heap_bytes: u64) -> Result<Vec<u8>> {
    let output_len = usize::try_from(meta.metadata_uncompressed_len)
        .map_err(|_| Error::BudgetExceeded("recovery metadata output"))?;
    let fixed = pages
        .retained_bytes()
        .checked_add(metadata::DEFLATE_HEAP_OVERHEAD)
        .ok_or(Error::ArithmeticOverflow("recovery metadata heap"))?;
    let available = max_heap_bytes
        .checked_sub(fixed)
        .ok_or(Error::BudgetExceeded("recovery metadata output"))?;
    if output_len as u64 > available {
        return Err(Error::BudgetExceeded("recovery metadata output"));
    }
    let mut output = Vec::new();
    output
        .try_reserve_exact(output_len)
        .map_err(|_| Error::BudgetExceeded("recovery metadata output"))?;
    if output.capacity() as u64 > available {
        return Err(Error::BudgetExceeded("recovery metadata output"));
    }
    output.resize(output_len, 0);
    Ok(output)
}

fn scan<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
    output: &mut [u8],
) -> Result<bool> {
    let mut inflater = Inflater::new(output);
    let mut chain = Chain::new(meta);
    while chain.remaining != 0 {
        cancellation.check()?;
        reporter.metadata_chunk_examined()?;
        let Some(chunk) = read_chunk(mapping, meta, pages, &mut chain, reporter)? else {
            return Ok(false);
        };
        if inflater.feed_source(chunk.bytes).is_err() {
            emit(
                reporter,
                ValidationReason::MetadataInvalid,
                Some(chain.page_number),
            )?;
            return Ok(false);
        }
        chain.advance(chunk.next, chunk.bytes.len())?;
    }
    finish_inflater(inflater, meta.metadata_compressed_len, reporter)
}

fn finish_inflater<S: RecoverySink>(
    inflater: Inflater<'_>,
    compressed_len: u64,
    reporter: &mut Reporter<'_, S>,
) -> Result<bool> {
    if inflater.finish(compressed_len).is_err() {
        emit(reporter, ValidationReason::MetadataInvalid, None)?;
        return Ok(false);
    }
    Ok(true)
}

struct Chain {
    page_number: u32,
    logical_offset: u64,
    remaining: u64,
    pages: [u32; metadata::MAX_PAGES],
    len: usize,
}

impl Chain {
    fn new(meta: MetaV4) -> Self {
        Self {
            page_number: meta.metadata_root,
            logical_offset: 0,
            remaining: meta.metadata_compressed_len,
            pages: [0; metadata::MAX_PAGES],
            len: 0,
        }
    }

    fn advance(&mut self, next: u32, length: usize) -> Result<()> {
        self.logical_offset = self
            .logical_offset
            .checked_add(length as u64)
            .ok_or(Error::ArithmeticOverflow("recovery metadata offset"))?;
        self.remaining -= length as u64;
        self.page_number = next;
        Ok(())
    }
}

fn read_chunk<'m, S: RecoverySink>(
    mapping: &'m Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    chain: &mut Chain,
    reporter: &mut Reporter<'_, S>,
) -> Result<Option<metadata::ParsedPage<PageView<'m>>>> {
    if !claim_page(meta, pages, chain, reporter)? {
        return Ok(None);
    }
    load_chunk(mapping, meta, chain, reporter)
}

fn claim_page<S: RecoverySink>(
    meta: MetaV4,
    pages: &mut PageSet,
    chain: &mut Chain,
    reporter: &mut Reporter<'_, S>,
) -> Result<bool> {
    if chain.len == chain.pages.len() {
        emit(reporter, ValidationReason::MetadataInvalid, None)?;
        return Ok(false);
    }
    if chain.page_number < 2 || u64::from(chain.page_number) >= meta.page_count {
        emit(
            reporter,
            ValidationReason::PageOutOfBounds,
            Some(chain.page_number),
        )?;
        return Ok(false);
    }
    if !pages.insert(chain.page_number)? {
        let reason = repeated_reason(&chain.pages[..chain.len], chain.page_number);
        emit(reporter, reason, Some(chain.page_number))?;
        return Ok(false);
    }
    chain.pages[chain.len] = chain.page_number;
    chain.len += 1;
    Ok(true)
}

fn repeated_reason(chain: &[u32], page_number: u32) -> ValidationReason {
    if chain.contains(&page_number) {
        ValidationReason::TreeCycle
    } else {
        ValidationReason::PageAlias
    }
}

fn load_chunk<'m, S: RecoverySink>(
    mapping: &'m Mapping,
    meta: MetaV4,
    chain: &Chain,
    reporter: &mut Reporter<'_, S>,
) -> Result<Option<metadata::ParsedPage<PageView<'m>>>> {
    let page = match mapping.page(chain.page_number, meta.page_count) {
        Ok(page) => page,
        Err(_) => {
            reject_page(reporter, chain.page_number, ValidationReason::IoError, true)?;
            return Ok(None);
        }
    };
    if crc32c::crc32c_source_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
        reject_page(
            reporter,
            chain.page_number,
            ValidationReason::PageCrcMismatch,
            false,
        )?;
        return Ok(None);
    }
    let parsed = match metadata::parse_page(
        page,
        chain.page_number,
        &meta,
        chain.logical_offset,
        chain.remaining,
    ) {
        Ok(parsed) => parsed,
        Err(_) => {
            reject_page(
                reporter,
                chain.page_number,
                ValidationReason::MetadataInvalid,
                false,
            )?;
            return Ok(None);
        }
    };
    reporter.page_accepted()?;
    Ok(Some(parsed))
}

fn reject_page<S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    page_number: u32,
    reason: ValidationReason,
    io_unreadable: bool,
) -> Result<()> {
    reporter.page_rejected(io_unreadable)?;
    emit(reporter, reason, Some(page_number))
}

fn emit<S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    reason: ValidationReason,
    page: Option<u32>,
) -> Result<()> {
    emit_page_unknown(reporter, reason, ValidationObject::Metadata, page)
}
