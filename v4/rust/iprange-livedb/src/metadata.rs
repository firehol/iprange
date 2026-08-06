//! Opaque bounded zlib metadata and its forward COW page chain.

use std::fs::File;

use flate2::{Compress, Compression, Decompress, FlushCompress, FlushDecompress, Status};

use crate::contract::{
    u16_le, u32_le, u64_le, MetaV4, MAX_METADATA_UNCOMPRESSED, PAGE_MAGIC, PAGE_SIZE,
};
use crate::error::{Error, Result};
use crate::file_io;
use crate::fixed_tree::Store;
use crate::slotted_page::{put_u16, put_u32, put_u64};

const PAGE_TYPE: u8 = 13;
const BODY_OFFSET: usize = 32;
pub(crate) const DATA_OFFSET: usize = 48;
const CHUNK_CAPACITY: usize = PAGE_SIZE - DATA_OFFSET;
pub(crate) const MAX_PAGES: usize = 260;
// Covers the pinned miniz backend's fixed workspace; allocation tests enforce it.
pub(crate) const DEFLATE_HEAP_OVERHEAD: u64 = 512 * 1024;

pub(crate) fn compressed_bound(uncompressed_len: usize) -> usize {
    let blocks = std::cmp::max(1, uncompressed_len.div_ceil(65_535));
    uncompressed_len + 5 * blocks + 6
}

pub(crate) fn compress(input: &[u8], max_heap_bytes: u64) -> Result<Vec<u8>> {
    if input.len() as u64 > MAX_METADATA_UNCOMPRESSED {
        return Err(Error::InvalidArgument("metadata exceeds 1 MiB"));
    }
    let bound = compressed_bound(input.len());
    if bound as u64 > max_heap_bytes {
        return Err(Error::BudgetExceeded("metadata compression heap"));
    }

    let mut output = Vec::new();
    output
        .try_reserve_exact(bound)
        .map_err(|_| Error::BudgetExceeded("metadata compression heap"))?;
    output.resize(bound, 0);
    if max_heap_bytes >= bound as u64 + DEFLATE_HEAP_OVERHEAD {
        if let Some(length) = try_deflate(input, &mut output)? {
            output.truncate(length);
            return Ok(output);
        }
    }

    output.clear();
    encode_stored_zlib(input, &mut output);
    debug_assert!(output.len() <= bound);
    Ok(output)
}

fn try_deflate(input: &[u8], output: &mut [u8]) -> Result<Option<usize>> {
    let mut encoder = Compress::new(Compression::default(), true);
    loop {
        let input_at = encoder.total_in() as usize;
        let output_at = encoder.total_out() as usize;
        if output_at == output.len() {
            return Ok(None);
        }
        let status = match encoder.compress(
            &input[input_at..],
            &mut output[output_at..],
            FlushCompress::Finish,
        ) {
            Ok(status) => status,
            Err(_) => return Ok(None),
        };
        let next_input = encoder.total_in() as usize;
        let next_output = encoder.total_out() as usize;
        if status == Status::StreamEnd {
            return Ok((next_input == input.len()).then_some(next_output));
        }
        if next_input == input_at && next_output == output_at {
            return Ok(None);
        }
    }
}

fn encode_stored_zlib(input: &[u8], output: &mut Vec<u8>) {
    output.extend_from_slice(&[0x78, 0x01]);
    if input.is_empty() {
        append_stored_block(output, &[], true);
    } else {
        let blocks = input.chunks(65_535);
        let count = blocks.len();
        for (index, block) in blocks.enumerate() {
            append_stored_block(output, block, index + 1 == count);
        }
    }
    output.extend_from_slice(&adler32(input).to_be_bytes());
}

fn append_stored_block(output: &mut Vec<u8>, bytes: &[u8], final_block: bool) {
    let length = bytes.len() as u16;
    output.push(u8::from(final_block));
    output.extend_from_slice(&length.to_le_bytes());
    output.extend_from_slice(&(!length).to_le_bytes());
    output.extend_from_slice(bytes);
}

fn adler32(input: &[u8]) -> u32 {
    const MODULUS: u32 = 65_521;
    let mut low = 1u32;
    let mut high = 0u32;
    for chunk in input.chunks(5_552) {
        for &byte in chunk {
            low += u32::from(byte);
            high += low;
        }
        low %= MODULUS;
        high %= MODULUS;
    }
    (high << 16) | low
}

pub(crate) fn write_chain<S: Store>(store: &mut S, compressed: &[u8]) -> Result<u32> {
    if compressed.is_empty()
        || compressed.len() > compressed_bound(MAX_METADATA_UNCOMPRESSED as usize)
    {
        return Err(Error::InvalidArgument(
            "compressed metadata length is invalid",
        ));
    }
    let count = compressed.len().div_ceil(CHUNK_CAPACITY);
    if count > MAX_PAGES {
        return Err(Error::Corrupt("metadata chain exceeds its fixed bound"));
    }

    let mut pages = [0u32; MAX_PAGES];
    for slot in &mut pages[..count] {
        *slot = store.allocate()?;
    }
    for index in 0..count {
        let start = index * CHUNK_CAPACITY;
        let end = std::cmp::min(start + CHUNK_CAPACITY, compressed.len());
        let next = pages.get(index + 1).copied().unwrap_or(0);
        let mut page = [0; PAGE_SIZE];
        encode_page(
            &mut page,
            store.target_txn(),
            next,
            start as u64,
            &compressed[start..end],
        );
        store.write(pages[index], &page)?;
    }
    Ok(pages[0])
}

pub(crate) fn collect_pages<S: Store>(store: &S, meta: &MetaV4) -> Result<ChainPages> {
    let mut output = ChainPages {
        pages: [0; MAX_PAGES],
        len: 0,
    };
    walk(
        meta,
        |page_number, page| store.read(page_number, page),
        |page_number, _| {
            let slot = output
                .pages
                .get_mut(output.len)
                .ok_or(Error::Corrupt("metadata chain exceeds its fixed bound"))?;
            *slot = page_number;
            output.len += 1;
            Ok(())
        },
    )?;
    Ok(output)
}

pub(crate) struct ChainPages {
    pages: [u32; MAX_PAGES],
    len: usize,
}

impl ChainPages {
    pub(crate) fn as_slice(&self) -> &[u32] {
        &self.pages[..self.len]
    }
}

pub(crate) fn read(file: &File, meta: &MetaV4, output: &mut [u8]) -> Result<Option<usize>> {
    if meta.metadata_root == 0 {
        return Ok(None);
    }
    let required = usize::try_from(meta.metadata_uncompressed_len)
        .map_err(|_| Error::Corrupt("metadata length is not addressable"))?;
    if output.len() < required {
        return Err(Error::BufferTooSmall {
            required: meta.metadata_uncompressed_len,
        });
    }

    let mut inflater = Inflater::new(&mut output[..required]);
    walk(
        meta,
        |page_number, page| file_io::read_page(file, page_number, meta.page_count, page),
        |_, bytes| inflater.feed(bytes),
    )?;
    inflater.finish(meta.metadata_compressed_len)?;
    Ok(Some(required))
}

pub(crate) fn read_vec(file: &File, meta: &MetaV4) -> Result<Option<Vec<u8>>> {
    if meta.metadata_root == 0 {
        return Ok(None);
    }
    let length = usize::try_from(meta.metadata_uncompressed_len)
        .map_err(|_| Error::Corrupt("metadata length is not addressable"))?;
    let mut output = vec![0; length];
    if read(file, meta, &mut output)? != Some(length) {
        return Err(Error::Corrupt("metadata length changed while reading"));
    }
    Ok(Some(output))
}

fn walk(
    meta: &MetaV4,
    mut read_page: impl FnMut(u32, &mut [u8; PAGE_SIZE]) -> Result<()>,
    mut visit: impl FnMut(u32, &[u8]) -> Result<()>,
) -> Result<()> {
    if meta.metadata_root == 0 {
        return Ok(());
    }
    let mut page_number = meta.metadata_root;
    let mut logical_offset = 0u64;
    let mut remaining = meta.metadata_compressed_len;
    let mut pages = 0usize;

    while remaining != 0 {
        if pages == MAX_PAGES {
            return Err(Error::Corrupt("metadata chain exceeds its fixed bound"));
        }
        let mut page = [0; PAGE_SIZE];
        read_page(page_number, &mut page)?;
        let parsed = parse_page(&page, page_number, meta, logical_offset, remaining)?;
        visit(page_number, parsed.bytes)?;
        logical_offset += parsed.bytes.len() as u64;
        remaining -= parsed.bytes.len() as u64;
        page_number = parsed.next;
        pages += 1;
    }
    if page_number != 0 {
        return Err(Error::Corrupt("metadata chain has an extra page"));
    }
    Ok(())
}

pub(crate) struct ParsedPage<'a> {
    pub(crate) next: u32,
    pub(crate) bytes: &'a [u8],
}

pub(crate) fn parse_page<'a>(
    page: &'a [u8; PAGE_SIZE],
    page_number: u32,
    meta: &MetaV4,
    expected_offset: u64,
    remaining: u64,
) -> Result<ParsedPage<'a>> {
    require_page_header(page, meta)?;
    let next = u32_le(page, 32);
    let length = usize::from(u16_le(page, 36));
    let expected_length = std::cmp::min(remaining, CHUNK_CAPACITY as u64) as usize;
    require_page_body(page, expected_offset, length, expected_length)?;
    require_page_link(
        page_number,
        next,
        meta.page_count,
        remaining == length as u64,
    )?;
    Ok(ParsedPage {
        next,
        bytes: &page[DATA_OFFSET..DATA_OFFSET + length],
    })
}

fn require_page_header(page: &[u8; PAGE_SIZE], meta: &MetaV4) -> Result<()> {
    require_page_identity(page)?;
    require_page_generation(page, meta)
}

fn require_page_identity(page: &[u8; PAGE_SIZE]) -> Result<()> {
    if page[..4] != PAGE_MAGIC
        || page[4] != PAGE_TYPE
        || page[5] != 0
        || u16_le(page, 6) != BODY_OFFSET as u16
        || u32_le(page, 24) != 0
    {
        return Err(Error::Corrupt("metadata page header is invalid"));
    }
    Ok(())
}

fn require_page_generation(page: &[u8; PAGE_SIZE], meta: &MetaV4) -> Result<()> {
    let born_txn = u64_le(page, 8);
    if born_txn == 0 || born_txn > meta.txn_id || u16_le(page, 16) != 1 || u16_le(page, 18) != 0 {
        return Err(Error::Corrupt("metadata page header is invalid"));
    }
    Ok(())
}

fn require_page_body(
    page: &[u8; PAGE_SIZE],
    expected_offset: u64,
    length: usize,
    expected_length: usize,
) -> Result<()> {
    if length != expected_length
        || u16_le(page, 38) != 0
        || u64_le(page, 40) != expected_offset
        || usize::from(u16_le(page, 20)) != DATA_OFFSET + length
        || usize::from(u16_le(page, 22)) != PAGE_SIZE
    {
        return Err(Error::Corrupt("metadata page body is invalid"));
    }
    if page[DATA_OFFSET + length..].iter().any(|&byte| byte != 0) {
        return Err(Error::Corrupt("metadata page body is invalid"));
    }
    Ok(())
}

fn require_page_link(
    page_number: u32,
    next: u32,
    page_count: u64,
    final_chunk: bool,
) -> Result<()> {
    if (final_chunk && next != 0)
        || (!final_chunk && (next < 2 || next == page_number || u64::from(next) >= page_count))
    {
        return Err(Error::Corrupt("metadata page link is invalid"));
    }
    Ok(())
}

fn encode_page(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    next: u32,
    logical_offset: u64,
    bytes: &[u8],
) {
    page.fill(0);
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = PAGE_TYPE;
    put_u16(page, 6, BODY_OFFSET as u16);
    put_u64(page, 8, born_txn);
    put_u16(page, 16, 1);
    put_u16(page, 20, (DATA_OFFSET + bytes.len()) as u16);
    put_u16(page, 22, PAGE_SIZE as u16);
    put_u32(page, 32, next);
    put_u16(page, 36, bytes.len() as u16);
    put_u64(page, 40, logical_offset);
    page[DATA_OFFSET..DATA_OFFSET + bytes.len()].copy_from_slice(bytes);
}

pub(crate) struct Inflater<'a> {
    decoder: Decompress,
    output: &'a mut [u8],
    written: usize,
    ended: bool,
}

struct InflateStep {
    status: Status,
    consumed: usize,
    produced: usize,
}

impl<'a> Inflater<'a> {
    pub(crate) fn new(output: &'a mut [u8]) -> Self {
        Self {
            decoder: Decompress::new(true),
            output,
            written: 0,
            ended: false,
        }
    }

    pub(crate) fn feed(&mut self, mut input: &[u8]) -> Result<()> {
        if self.ended {
            return Err(Error::Corrupt("metadata zlib stream has trailing bytes"));
        }
        while !input.is_empty() {
            let step = self.step(input, FlushDecompress::None)?;
            input = &input[step.consumed..];
            if step.status == Status::StreamEnd {
                if !input.is_empty() {
                    return Err(Error::Corrupt("metadata zlib stream has trailing bytes"));
                }
                self.ended = true;
                return Ok(());
            }
            if step.consumed == 0 && step.produced == 0 {
                return Err(Error::Corrupt("metadata zlib stream made no progress"));
            }
        }
        Ok(())
    }

    pub(crate) fn finish(mut self, compressed_len: u64) -> Result<()> {
        while !self.ended {
            let step = self.step(&[], FlushDecompress::Finish)?;
            self.ended = step.status == Status::StreamEnd;
            if !self.ended && step.consumed == 0 && step.produced == 0 {
                return Err(Error::Corrupt("metadata zlib stream is incomplete"));
            }
        }
        if self.decoder.total_in() != compressed_len || self.written != self.output.len() {
            return Err(Error::Corrupt("metadata zlib length is invalid"));
        }
        Ok(())
    }

    fn step(&mut self, input: &[u8], flush: FlushDecompress) -> Result<InflateStep> {
        let before_input = self.decoder.total_in();
        let before_output = self.decoder.total_out();
        let output_full = self.written == self.output.len();
        let mut overflow = [0u8; 1];
        let output = if output_full {
            &mut overflow
        } else {
            &mut self.output[self.written..]
        };
        let status = self
            .decoder
            .decompress(input, output, flush)
            .map_err(|_| Error::Corrupt("metadata zlib stream is invalid"))?;
        let consumed = (self.decoder.total_in() - before_input) as usize;
        let produced = (self.decoder.total_out() - before_output) as usize;
        if output_full && produced != 0 {
            return Err(Error::Corrupt("metadata exceeds its declared length"));
        }
        self.written += produced;
        Ok(InflateStep {
            status,
            consumed,
            produced,
        })
    }
}

#[cfg(test)]
#[path = "metadata_tests.rs"]
mod tests;
