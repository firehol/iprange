//! Selected membership-dictionary record reads.

use std::fs::File;

use crate::contract::{u16_le, u32_le, u64_le, MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::slotted_page::{self, Header};

const ID_BRANCH: u8 = 7;
const ID_LEAF: u8 = 8;
const RECORD_BASE: usize = 64;
const MAX_WORD_COUNT: u32 = 67_108_864;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Storage {
    Inline,
    Blob(u32),
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Record {
    pub(crate) id: u32,
    pub(crate) word_count: u32,
    pub(crate) leaf_page: u32,
    pub(crate) leaf_index: usize,
    pub(crate) storage: Storage,
}

pub(crate) fn find(file: &File, meta: &MetaV4, id: u32) -> Result<Option<Record>> {
    if id == 0 {
        return Err(Error::Corrupt("stored membership ID is zero"));
    }
    if meta.membership_id_root == 0 {
        return Ok(None);
    }
    let mut page_number = meta.membership_id_root;
    let mut expected = None;
    let mut page = [0; PAGE_SIZE];

    for _ in 0..=MAX_TREE_LEVEL {
        file_io::read_page(file, page_number, meta.page_count, &mut page)?;
        let header = parse_header(&page, meta.txn_id, expected)?;
        let (position, exact) = lower_bound(&page, &header, id, meta)?;
        if header.level == 0 {
            return leaf_result(&page, &header, page_number, position, exact, meta);
        }
        let Some(position) = position.checked_sub(usize::from(!exact)) else {
            return Ok(None);
        };
        page_number = branch_child(&page, &header, position, meta.page_count)?;
        expected = Some(header.level - 1);
    }
    Err(Error::Corrupt(
        "membership ID tree exceeds its maximum height",
    ))
}

pub(crate) fn read_inline_words(
    file: &File,
    meta: &MetaV4,
    selected: Record,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, selected.leaf_page, meta.page_count, &mut page)?;
    let header = parse_header(&page, meta.txn_id, Some(0))?;
    let record = decode_record(
        &page,
        &header,
        selected.leaf_page,
        selected.leaf_index,
        meta,
    )?;
    require_same_inline(record, selected)?;
    let bytes = slotted_page::record(&page, &header, selected.leaf_index, RECORD_BASE, PAGE_SIZE)?;
    let start = inline_offset(start)?;
    for (index, word) in output.iter_mut().enumerate() {
        *word = u64_le(bytes, start + index * 8);
    }
    Ok(())
}

fn require_same_inline(record: Record, selected: Record) -> Result<()> {
    if record.id != selected.id
        || record.word_count != selected.word_count
        || record.storage != Storage::Inline
    {
        return Err(Error::Corrupt("membership dictionary entry changed"));
    }
    Ok(())
}

fn inline_offset(word: u32) -> Result<usize> {
    usize::try_from(word)
        .ok()
        .and_then(|word| word.checked_mul(8))
        .and_then(|offset| offset.checked_add(RECORD_BASE))
        .ok_or(Error::ArithmeticOverflow("membership word offset"))
}

fn lower_bound(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    target: u32,
    meta: &MetaV4,
) -> Result<(usize, bool)> {
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        if key_at(page, header, middle, meta)? < target {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    let exact = lower < header.item_count && key_at(page, header, lower, meta)? == target;
    Ok((lower, exact))
}

fn key_at(page: &[u8; PAGE_SIZE], header: &Header, index: usize, meta: &MetaV4) -> Result<u32> {
    if header.level == 0 {
        return Ok(decode_record(page, header, 0, index, meta)?.id);
    }
    let id = u32_le(slotted_page::cell(page, header, index, 8)?, 0);
    require_id(id, meta.membership_id_limit)?;
    Ok(id)
}

fn leaf_result(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    page_number: u32,
    position: usize,
    exact: bool,
    meta: &MetaV4,
) -> Result<Option<Record>> {
    if !exact {
        return Ok(None);
    }
    decode_record(page, header, page_number, position, meta).map(Some)
}

fn decode_record(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    page_number: u32,
    index: usize,
    meta: &MetaV4,
) -> Result<Record> {
    let bytes = slotted_page::record(page, header, index, RECORD_BASE, PAGE_SIZE)?;
    let id = u32_le(bytes, 4);
    let word_count = u32_le(bytes, 16);
    let bitmap_len = u32_le(bytes, 20);
    let blob_root = u32_le(bytes, 24);
    require_record_fields(bytes, id, word_count, bitmap_len, meta)?;
    let storage = storage(bytes, bitmap_len, blob_root, meta.page_count)?;
    Ok(Record {
        id,
        word_count,
        leaf_page: page_number,
        leaf_index: index,
        storage,
    })
}

fn require_record_fields(
    bytes: &[u8],
    id: u32,
    word_count: u32,
    bitmap_len: u32,
    meta: &MetaV4,
) -> Result<()> {
    require_id(id, meta.membership_id_limit)?;
    if u64_le(bytes, 8) == 0
        || word_count == 0
        || word_count > MAX_WORD_COUNT
        || u64::from(word_count) > maximum_words(meta.feed_index_limit)
        || bitmap_len != word_count.checked_mul(8).unwrap()
        || bytes[3] != 0
        || u32_le(bytes, 28) != 0
    {
        return Err(Error::Corrupt("membership dictionary record is malformed"));
    }
    Ok(())
}

fn require_id(id: u32, limit: u64) -> Result<()> {
    if id == 0 || u64::from(id) >= limit {
        return Err(Error::Corrupt(
            "membership ID is outside the declared namespace",
        ));
    }
    Ok(())
}

fn storage(bytes: &[u8], bitmap_len: u32, blob_root: u32, page_count: u64) -> Result<Storage> {
    match bytes[2] {
        0 if blob_root == 0 && bytes.len() == RECORD_BASE + bitmap_len as usize => {
            Ok(Storage::Inline)
        }
        1 if bytes.len() == RECORD_BASE => {
            if blob_root < 2 || u64::from(blob_root) >= page_count {
                return Err(Error::Corrupt("membership blob root is invalid"));
            }
            Ok(Storage::Blob(blob_root))
        }
        _ => Err(Error::Corrupt("membership dictionary storage is malformed")),
    }
}

fn maximum_words(feed_index_limit: u64) -> u64 {
    feed_index_limit.saturating_add(63) / 64
}

fn branch_child(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    page_count: u64,
) -> Result<u32> {
    let child = u32_le(slotted_page::cell(page, header, index, 8)?, 4);
    if child < 2 || u64::from(child) >= page_count {
        return Err(Error::Corrupt(
            "membership dictionary child is outside page bounds",
        ));
    }
    Ok(child)
}

fn parse_header(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected: Option<u16>,
) -> Result<Header> {
    let level = u16_le(page, 18);
    let page_type = if level == 0 { ID_LEAF } else { ID_BRANCH };
    slotted_page::parse(page, selected_txn, page_type, 0, expected)
}
