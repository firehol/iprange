use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, MetaV4, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::file_io;
use crate::metadata::{self, Inflater};
use crate::validation::{PhysicalByteInterval, ValidationObject, ValidationReason};

use super::page_set::PageSet;
use super::report::{RecoverySink, Reporter, Unknown};

pub(crate) fn read<S: RecoverySink>(
    file: &File,
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
    let complete = scan(file, meta, pages, cancellation, reporter, &mut output)?;
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
    file: &File,
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
        let Some(chunk) = read_chunk(file, meta, pages, &mut chain, reporter)? else {
            return Ok(false);
        };
        if inflater.feed(chunk.bytes()).is_err() {
            emit(
                reporter,
                ValidationReason::MetadataInvalid,
                Some(chain.page_number),
            )?;
            return Ok(false);
        }
        chain.advance(chunk.next, chunk.length)?;
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

struct Chunk {
    page: [u8; PAGE_SIZE],
    next: u32,
    length: usize,
}

impl Chunk {
    fn bytes(&self) -> &[u8] {
        &self.page[metadata::DATA_OFFSET..metadata::DATA_OFFSET + self.length]
    }
}

fn read_chunk<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    pages: &mut PageSet,
    chain: &mut Chain,
    reporter: &mut Reporter<'_, S>,
) -> Result<Option<Chunk>> {
    if !claim_page(meta, pages, chain, reporter)? {
        return Ok(None);
    }
    load_chunk(file, meta, chain, reporter)
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

fn load_chunk<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    chain: &Chain,
    reporter: &mut Reporter<'_, S>,
) -> Result<Option<Chunk>> {
    let mut page = [0; PAGE_SIZE];
    if file_io::read_page(file, chain.page_number, meta.page_count, &mut page).is_err() {
        reject_page(reporter, chain.page_number, ValidationReason::IoError, true)?;
        return Ok(None);
    }
    if crc32c::crc32c_with_zeroed(&page, 28, 4) != Some(u32_le(&page, 28)) {
        reject_page(
            reporter,
            chain.page_number,
            ValidationReason::PageCrcMismatch,
            false,
        )?;
        return Ok(None);
    }
    let parsed = match metadata::parse_page(
        &page,
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
    let chunk = Chunk {
        next: parsed.next,
        length: parsed.bytes.len(),
        page,
    };
    reporter.page_accepted()?;
    Ok(Some(chunk))
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
    reporter.unknown(Unknown {
        reason,
        object: ValidationObject::Metadata,
        page_number: page,
        physical_bytes: page.map(page_interval),
        address_fence: None,
        contributes_to_possible_span: false,
        has_unbounded_extent: false,
    })
}

fn page_interval(page: u32) -> PhysicalByteInterval {
    let start = u64::from(page) * PAGE_SIZE as u64;
    PhysicalByteInterval {
        start,
        end_exclusive: start + PAGE_SIZE as u64,
    }
}
