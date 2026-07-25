//! Selected membership-blob word reads.

use std::fs::File;

use crate::contract::{u16_le, u32_le, u64_le, MetaV4, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::fixed_tree::Store;
use crate::slotted_page::{self, Header, HEADER_SIZE};

const BLOB_BRANCH: u8 = 11;
const BLOB_LEAF: u8 = 12;
const MEMBERSHIP_BLOB: u32 = 1;
const LEAF_DATA: usize = 48;
const MAX_DATA: usize = PAGE_SIZE - LEAF_DATA;

pub(crate) fn read_words(
    file: &File,
    meta: &MetaV4,
    root: u32,
    total_words: u32,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    read_words_source(&FileSource { file, meta }, root, total_words, start, output)
}

pub(crate) fn read_words_from<S: Store>(
    store: &S,
    root: u32,
    total_words: u32,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    read_words_source(store, root, total_words, start, output)
}

trait Source {
    fn selected_txn(&self) -> u64;
    fn page_count(&self) -> u64;
    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()>;
}

impl<S: Store> Source for S {
    fn selected_txn(&self) -> u64 {
        self.target_txn()
    }

    fn page_count(&self) -> u64 {
        self.page_limit()
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        Store::read(self, page_number, page)
    }
}

struct FileSource<'a> {
    file: &'a File,
    meta: &'a MetaV4,
}

impl Source for FileSource<'_> {
    fn selected_txn(&self) -> u64 {
        self.meta.txn_id
    }

    fn page_count(&self) -> u64 {
        self.meta.page_count
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        file_io::read_page(self.file, page_number, self.meta.page_count, page)
    }
}

fn read_words_source<S: Source>(
    source: &S,
    root: u32,
    total_words: u32,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let total_bytes = u64::from(total_words) * 8;
    let mut offset = u64::from(start) * 8;
    let mut written = 0;
    while written < output.len() {
        let leaf = find_leaf(source, root, total_bytes, offset)?;
        let local = usize::try_from(offset - leaf.offset)
            .map_err(|_| Error::ArithmeticOverflow("membership blob offset"))?;
        let available = (leaf.data_len - local) / 8;
        let count = available.min(output.len() - written);
        if count == 0 {
            return Err(Error::Corrupt(
                "membership blob cannot advance by a complete word",
            ));
        }
        for index in 0..count {
            output[written + index] = u64_le(&leaf.page, LEAF_DATA + local + index * 8);
        }
        written += count;
        offset = offset
            .checked_add((count * 8) as u64)
            .ok_or(Error::ArithmeticOverflow("membership blob offset"))?;
    }
    Ok(())
}

struct Leaf {
    page: [u8; PAGE_SIZE],
    offset: u64,
    data_len: usize,
}

fn find_leaf<S: Source>(source: &S, root: u32, total_bytes: u64, target: u64) -> Result<Leaf> {
    if target >= total_bytes {
        return Err(Error::Corrupt("membership blob request exceeds its length"));
    }
    let mut page_number = root;
    let mut expected = None;
    let mut expected_offset = 0;
    let mut page = [0; PAGE_SIZE];

    for _ in 0..=MAX_TREE_LEVEL {
        source.read(page_number, &mut page)?;
        let level = u16_le(&page, 18);
        if level == 0 {
            return parse_leaf(
                page,
                source.selected_txn(),
                expected,
                expected_offset,
                total_bytes,
                target,
            );
        }
        let header = parse_branch(&page, source.selected_txn(), expected)?;
        let first = branch_record(&page, &header, 0, source.page_count())?;
        if first.offset != expected_offset {
            return Err(Error::Corrupt(
                "membership blob branch starts at a wrong offset",
            ));
        }
        let record = select_branch(&page, &header, target, source.page_count())?;
        page_number = record.child;
        expected_offset = record.offset;
        expected = Some(header.level - 1);
    }
    Err(Error::Corrupt(
        "membership blob tree exceeds its maximum height",
    ))
}

struct BranchRecord {
    offset: u64,
    child: u32,
}

fn select_branch(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    target: u64,
    page_count: u64,
) -> Result<BranchRecord> {
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        if branch_record(page, header, middle, page_count)?.offset <= target {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    let index = lower
        .checked_sub(1)
        .ok_or(Error::Corrupt("membership blob has no covering child"))?;
    branch_record(page, header, index, page_count)
}

fn branch_record(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    page_count: u64,
) -> Result<BranchRecord> {
    let bytes = slotted_page::cell(page, header, index, 16)?;
    let child = u32_le(bytes, 8);
    if u32_le(bytes, 12) != 0 || child < 2 || u64::from(child) >= page_count {
        return Err(Error::Corrupt("membership blob branch record is malformed"));
    }
    Ok(BranchRecord {
        offset: u64_le(bytes, 0),
        child,
    })
}

fn parse_branch(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected: Option<u16>,
) -> Result<Header> {
    slotted_page::parse(page, selected_txn, BLOB_BRANCH, MEMBERSHIP_BLOB, expected)
}

fn parse_leaf(
    page: [u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
    expected_offset: u64,
    total_bytes: u64,
    target: u64,
) -> Result<Leaf> {
    let offset = u64_le(&page, 32);
    let data_len = usize::from(u16_le(&page, 40));
    require_leaf_identity(&page, selected_txn, expected_level)?;
    require_leaf_layout(&page, data_len)?;
    if offset != expected_offset || offset % 8 != 0 {
        return Err(Error::Corrupt("membership blob leaf offset is malformed"));
    }
    let end = offset
        .checked_add(data_len as u64)
        .ok_or(Error::Corrupt("membership blob leaf end overflows"))?;
    require_leaf_coverage(offset, end, data_len, total_bytes, target)?;
    Ok(Leaf {
        page,
        offset,
        data_len,
    })
}

fn require_leaf_identity(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<()> {
    let born_txn = u64_le(page, 8);
    if page[..4] != PAGE_MAGIC
        || page[4] != BLOB_LEAF
        || page[5] != 0
        || u16_le(page, 6) as usize != HEADER_SIZE
        || born_txn == 0
        || born_txn > selected_txn
        || expected_level.is_some_and(|level| level != 0)
    {
        return Err(Error::Corrupt("membership blob leaf identity is malformed"));
    }
    Ok(())
}

fn require_leaf_layout(page: &[u8; PAGE_SIZE], data_len: usize) -> Result<()> {
    require_leaf_fixed_layout(page)?;
    require_leaf_data_layout(page, data_len)
}

fn require_leaf_fixed_layout(page: &[u8; PAGE_SIZE]) -> Result<()> {
    if u16_le(page, 16) != 1
        || u16_le(page, 18) != 0
        || u16_le(page, 22) as usize != PAGE_SIZE
        || u32_le(page, 24) != MEMBERSHIP_BLOB
        || page[42..48] != [0; 6]
    {
        return Err(Error::Corrupt("membership blob leaf layout is malformed"));
    }
    Ok(())
}

fn require_leaf_data_layout(page: &[u8; PAGE_SIZE], data_len: usize) -> Result<()> {
    if data_len == 0
        || data_len > MAX_DATA
        || data_len % 8 != 0
        || u16_le(page, 20) as usize != LEAF_DATA + data_len
    {
        return Err(Error::Corrupt(
            "membership blob leaf data geometry is malformed",
        ));
    }
    Ok(())
}

fn require_leaf_coverage(
    offset: u64,
    end: u64,
    data_len: usize,
    total_bytes: u64,
    target: u64,
) -> Result<()> {
    if end > total_bytes
        || target < offset
        || target >= end
        || (end < total_bytes && data_len != MAX_DATA)
        || (end == total_bytes && total_bytes - offset != data_len as u64)
    {
        return Err(Error::Corrupt(
            "membership blob leaf does not cover the requested bytes",
        ));
    }
    Ok(())
}
