//! Opaque bounded zlib metadata and its forward COW page chain.

use flate2::{Compress, Compression, Decompress, FlushCompress, FlushDecompress, Status};

use crate::contract::{u16_le, u32_le, u64_le, MetaV4, MAX_METADATA_UNCOMPRESSED, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::format::page_type;
use crate::mapping::{ByteRange, ByteSource, Mapping};
use crate::page_header;
use crate::page_io::PageEdit;

pub(crate) const PAGE_TYPE: u8 = page_type::METADATA;
pub(crate) const DATA_OFFSET: usize = 48;
pub(crate) const CHUNK_CAPACITY: usize = PAGE_SIZE - DATA_OFFSET;
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
        let target_txn = store.target_txn();
        store.update_page(pages[index], |page| {
            encode_page(
                page,
                target_txn,
                next,
                start as u64,
                &compressed[start..end],
            )
        })?;
    }
    Ok(pages[0])
}

pub(crate) fn collect_pages<S: Store>(store: &S, meta: &MetaV4) -> Result<ChainPages> {
    let mut output = ChainPages {
        pages: [0; MAX_PAGES],
        len: 0,
    };
    let mut page_number = meta.metadata_root;
    let mut logical_offset = 0u64;
    let mut remaining = meta.metadata_compressed_len;
    while remaining != 0 {
        let slot = output
            .pages
            .get_mut(output.len)
            .ok_or(Error::Corrupt("metadata chain exceeds its fixed bound"))?;
        *slot = page_number;
        output.len += 1;
        let parsed = store.inspect_page(page_number, |page| {
            let parsed = parse_page(page, page_number, meta, logical_offset, remaining)?;
            Ok((parsed.next, parsed.bytes.len()))
        })?;
        logical_offset += parsed.1 as u64;
        remaining -= parsed.1 as u64;
        page_number = parsed.0;
    }
    if page_number != 0 {
        return Err(Error::Corrupt("metadata chain has an extra page"));
    }
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

pub(crate) fn read(mapping: &Mapping, meta: &MetaV4, output: &mut [u8]) -> Result<Option<usize>> {
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
    walk_mapping(mapping, meta, &mut inflater)?;
    inflater.finish(meta.metadata_compressed_len)?;
    Ok(Some(required))
}

pub(crate) fn read_vec(mapping: &Mapping, meta: &MetaV4) -> Result<Option<Vec<u8>>> {
    if meta.metadata_root == 0 {
        return Ok(None);
    }
    let length = usize::try_from(meta.metadata_uncompressed_len)
        .map_err(|_| Error::Corrupt("metadata length is not addressable"))?;
    let mut output = vec![0; length];
    if read(mapping, meta, &mut output)? != Some(length) {
        return Err(Error::Corrupt("metadata length changed while reading"));
    }
    Ok(Some(output))
}

fn walk_mapping(mapping: &Mapping, meta: &MetaV4, inflater: &mut Inflater<'_>) -> Result<()> {
    let mut page_number = meta.metadata_root;
    let mut logical_offset = 0u64;
    let mut remaining = meta.metadata_compressed_len;
    let mut pages = 0usize;
    while remaining != 0 {
        if pages == MAX_PAGES {
            return Err(Error::Corrupt("metadata chain exceeds its fixed bound"));
        }
        let page = mapping.page(page_number, meta.page_count)?;
        let parsed = parse_page(page, page_number, meta, logical_offset, remaining)?;
        let length = parsed.bytes.len();
        inflater.feed_source(parsed.bytes)?;
        logical_offset += length as u64;
        remaining -= length as u64;
        page_number = parsed.next;
        pages += 1;
    }
    if page_number != 0 {
        return Err(Error::Corrupt("metadata chain has an extra page"));
    }
    Ok(())
}

pub(crate) struct ParsedPage<S> {
    pub(crate) next: u32,
    pub(crate) bytes: ByteRange<S>,
}

pub(crate) fn parse_page<S: ByteSource>(
    page: S,
    page_number: u32,
    meta: &MetaV4,
    expected_offset: u64,
    remaining: u64,
) -> Result<ParsedPage<S>> {
    require_page_header(page, meta)?;
    let fields = chunk_fields(
        page,
        page_number,
        meta.page_count,
        expected_offset,
        remaining,
    )
    .ok_or(Error::Corrupt("metadata page body is invalid"))?;
    if !reserved_zero(page, fields.length) {
        return Err(Error::Corrupt("metadata page body is invalid"));
    }
    Ok(ParsedPage {
        next: fields.next,
        bytes: chunk_bytes(page, fields.length)
            .ok_or(Error::Corrupt("metadata page body is invalid"))?,
    })
}

fn require_page_header<S: ByteSource>(page: S, meta: &MetaV4) -> Result<()> {
    require_page_identity(page)?;
    require_page_generation(page, meta)
}

fn require_page_identity<S: ByteSource>(page: S) -> Result<()> {
    if !common_header_valid(page) || !metadata_identity_valid(page) {
        return Err(Error::Corrupt("metadata page header is invalid"));
    }
    Ok(())
}

fn require_page_generation<S: ByteSource>(page: S, meta: &MetaV4) -> Result<()> {
    if !born_valid(page, meta.txn_id) {
        return Err(Error::Corrupt("metadata page header is invalid"));
    }
    Ok(())
}

#[derive(Clone, Copy)]
pub(crate) struct ChunkFields {
    pub(crate) next: u32,
    pub(crate) length: usize,
}

pub(crate) fn common_header_valid<S: ByteSource>(page: S) -> bool {
    page_header::common_valid(page)
}

pub(crate) fn born_valid<S: ByteSource>(page: S, selected_txn: u64) -> bool {
    page_header::born_valid(page, selected_txn)
}

pub(crate) fn metadata_identity_valid<S: ByteSource>(page: S) -> bool {
    page_header::kind_valid(page, PAGE_TYPE, 0)
}

pub(crate) fn chunk_fields<S: ByteSource>(
    page: S,
    page_number: u32,
    page_count: u64,
    expected_offset: u64,
    remaining: u64,
) -> Option<ChunkFields> {
    let next = u32_le(page, 32);
    let length = usize::from(u16_le(page, 36));
    let expected_length = remaining.min(CHUNK_CAPACITY as u64) as usize;
    let final_chunk = remaining == length as u64;
    if length != expected_length
        || length == 0
        || u16_le(page, 38) != 0
        || u64_le(page, 40) != expected_offset
        || page_header::lower(page) != DATA_OFFSET + length
        || page_header::upper(page) != PAGE_SIZE
        || page_header::item_count(page) != 1
        || page_header::level(page) != 0
        || !link_valid(page_number, next, page_count, final_chunk)
    {
        return None;
    }
    Some(ChunkFields { next, length })
}

fn link_valid(page_number: u32, next: u32, page_count: u64, final_chunk: bool) -> bool {
    (final_chunk && next == 0)
        || (!final_chunk && next >= 2 && next != page_number && u64::from(next) < page_count)
}

pub(crate) fn reserved_zero<S: ByteSource>(page: S, length: usize) -> bool {
    page.all_zero(DATA_OFFSET + length, PAGE_SIZE - DATA_OFFSET - length)
}

pub(crate) fn chunk_bytes<S: ByteSource>(page: S, length: usize) -> Option<ByteRange<S>> {
    ByteRange::new(page, DATA_OFFSET, length)
}

fn encode_page<P: PageEdit>(
    page: &mut P,
    born_txn: u64,
    next: u32,
    logical_offset: u64,
    bytes: &[u8],
) -> Result<()> {
    page_header::initialize(
        page,
        page_header::Fields {
            page_type: PAGE_TYPE,
            born_txn,
            item_count: 1,
            level: 0,
            lower: (DATA_OFFSET + bytes.len()) as u16,
            upper: PAGE_SIZE as u16,
            aux: 0,
        },
    )?;
    page.put_u32(32, next)?;
    page.put_u16(36, bytes.len() as u16)?;
    page.put_u64(40, logical_offset)?;
    page.write(DATA_OFFSET, bytes)
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

    pub(crate) fn feed_source<S: ByteSource>(&mut self, input: S) -> Result<()> {
        const CHUNK: usize = 256;
        let mut buffer = [0u8; CHUNK];
        let mut offset = 0usize;
        while offset < input.len() {
            let length = (input.len() - offset).min(CHUNK);
            if !input.copy_range_to(offset, &mut buffer[..length]) {
                return Err(Error::Corrupt("metadata mapping changed while reading"));
            }
            self.feed(&buffer[..length])?;
            offset += length;
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
