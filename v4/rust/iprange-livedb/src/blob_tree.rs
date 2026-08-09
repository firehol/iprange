//! Selected membership-blob word reads.

use crate::contract::{u16_le, u32_le, u64_le, MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::PageSource;
use crate::format::{page_type, Generation};
use crate::mapping::{ByteRange, ByteSource, Mapping};
use crate::page_header;
use crate::page_io::PageSink;
use crate::slotted_page::{self, Header};

pub(crate) const BRANCH_TYPE: u8 = page_type::MEMBERSHIP_BLOB_BRANCH;
pub(crate) const LEAF_TYPE: u8 = page_type::MEMBERSHIP_BLOB_LEAF;
pub(crate) const MEMBERSHIP_KIND: u32 = 1;
pub(crate) const BRANCH_RECORD_SIZE: usize = 16;
pub(crate) const LEAF_DATA: usize = 48;
pub(crate) const LEAF_CAPACITY: usize = PAGE_SIZE - LEAF_DATA;

const BRANCH_OFFSET_OFFSET: usize = 0;
const BRANCH_CHILD_OFFSET: usize = 8;
const BRANCH_RESERVED_OFFSET: usize = 12;
const LEAF_START_OFFSET: usize = 32;
const LEAF_LENGTH_OFFSET: usize = 40;
const LEAF_RESERVED_OFFSET: usize = 42;

pub(crate) fn read_words(
    mapping: &Mapping,
    meta: &MetaV4,
    root: u32,
    total_words: u32,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    read_words_from(
        &Generation::new(mapping, *meta),
        root,
        total_words,
        start,
        output,
    )
}

pub(crate) fn read_words_from<S: PageSource>(
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
        source.view_page(leaf.page_number, |page| {
            for index in 0..count {
                output[written + index] = u64_le(page, LEAF_DATA + local + index * 8);
            }
            Ok(())
        })?;
        written += count;
        offset = offset
            .checked_add((count * 8) as u64)
            .ok_or(Error::ArithmeticOverflow("membership blob offset"))?;
    }
    Ok(())
}

struct Leaf {
    page_number: u32,
    offset: u64,
    data_len: usize,
}

fn find_leaf<S: PageSource>(source: &S, root: u32, total_bytes: u64, target: u64) -> Result<Leaf> {
    crate::work::tree_lookup(1);
    if target >= total_bytes {
        return Err(Error::Corrupt("membership blob request exceeds its length"));
    }
    let mut page_number = root;
    let mut expected = None;
    let mut expected_offset = 0;

    for _ in 0..=MAX_TREE_LEVEL {
        enum Step {
            Leaf(LeafGeometry),
            Branch { child: u32, offset: u64, level: u16 },
        }
        let step = source.view_page(page_number, |page| {
            let level = page_header::level(page);
            if level == 0 {
                return parse_leaf_info(
                    page,
                    source.selected_txn(),
                    expected,
                    expected_offset,
                    total_bytes,
                    target,
                )
                .map(Step::Leaf);
            }
            let header = parse_branch(page, source.selected_txn(), expected)?;
            let first = branch_record(page, &header, 0, source.selected_page_limit())?;
            if first.offset != expected_offset {
                return Err(Error::Corrupt(
                    "membership blob branch starts at a wrong offset",
                ));
            }
            let record = select_branch(page, &header, target, source.selected_page_limit())?;
            Ok(Step::Branch {
                child: record.child,
                offset: record.offset,
                level: header.level,
            })
        })?;
        match step {
            Step::Leaf(info) => {
                return Ok(Leaf {
                    page_number,
                    offset: info.start,
                    data_len: info.data_len,
                });
            }
            Step::Branch {
                child,
                offset,
                level,
            } => {
                page_number = child;
                expected_offset = offset;
                expected = Some(level - 1);
                crate::work::tree_descent(1);
            }
        }
    }
    Err(Error::Corrupt(
        "membership blob tree exceeds its maximum height",
    ))
}

#[derive(Clone, Copy)]
pub(crate) struct BranchRecord {
    pub(crate) offset: u64,
    pub(crate) child: u32,
}

fn select_branch<S: ByteSource>(
    page: S,
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

fn branch_record<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_count: u64,
) -> Result<BranchRecord> {
    let bytes = slotted_page::cell(page, header, index, BRANCH_RECORD_SIZE)?;
    let record = decode_branch_record(bytes)?;
    if record.child < 2 || u64::from(record.child) >= page_count {
        return Err(Error::Corrupt("membership blob branch record is malformed"));
    }
    Ok(record)
}

pub(crate) fn decode_branch_record<S: ByteSource>(cell: S) -> Result<BranchRecord> {
    if cell.len() != BRANCH_RECORD_SIZE || u32_le(cell, BRANCH_RESERVED_OFFSET) != 0 {
        return Err(Error::Corrupt("membership blob branch record is malformed"));
    }
    Ok(BranchRecord {
        offset: u64_le(cell, BRANCH_OFFSET_OFFSET),
        child: u32_le(cell, BRANCH_CHILD_OFFSET),
    })
}

pub(crate) fn encode_branch_record(record: BranchRecord) -> [u8; BRANCH_RECORD_SIZE] {
    let mut bytes = [0; BRANCH_RECORD_SIZE];
    bytes[BRANCH_OFFSET_OFFSET..BRANCH_CHILD_OFFSET].copy_from_slice(&record.offset.to_le_bytes());
    bytes[BRANCH_CHILD_OFFSET..BRANCH_RESERVED_OFFSET].copy_from_slice(&record.child.to_le_bytes());
    bytes
}

fn parse_branch<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected: Option<u16>,
) -> Result<Header> {
    slotted_page::parse(page, selected_txn, BRANCH_TYPE, MEMBERSHIP_KIND, expected)
}

#[derive(Clone, Copy)]
pub(crate) struct LeafGeometry {
    pub(crate) start: u64,
    pub(crate) end: u64,
    pub(crate) data_len: usize,
}

fn parse_leaf_info<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
    expected_offset: u64,
    total_bytes: u64,
    target: u64,
) -> Result<LeafGeometry> {
    require_leaf_identity(page, selected_txn, expected_level)?;
    let geometry = leaf_geometry(page, expected_level, expected_offset, total_bytes)?;
    if target < geometry.start || target >= geometry.end {
        return Err(Error::Corrupt(
            "membership blob leaf does not cover the requested bytes",
        ));
    }
    Ok(geometry)
}

pub(crate) fn require_leaf_identity<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<()> {
    if !page_header::common_valid(page)
        || !page_header::kind_valid(page, LEAF_TYPE, MEMBERSHIP_KIND)
        || !page_header::born_valid(page, selected_txn)
        || page_header::level(page) != 0
        || expected_level.is_some_and(|level| level != 0)
    {
        return Err(Error::Corrupt("membership blob leaf identity is malformed"));
    }
    Ok(())
}

pub(crate) fn leaf_geometry<S: ByteSource>(
    page: S,
    expected_level: Option<u16>,
    expected_start: u64,
    total_bytes: u64,
) -> Result<LeafGeometry> {
    let data_len = usize::from(u16_le(page, LEAF_LENGTH_OFFSET));
    let start = u64_le(page, LEAF_START_OFFSET);
    let end = start
        .checked_add(data_len as u64)
        .ok_or(Error::Corrupt("membership blob leaf end overflows"))?;
    if expected_level.is_some_and(|level| level != 0)
        || page_header::item_count(page) != 1
        || page_header::level(page) != 0
        || page_header::lower(page) != LEAF_DATA + data_len
        || page_header::upper(page) != PAGE_SIZE
        || !page_header::kind_valid(page, LEAF_TYPE, MEMBERSHIP_KIND)
        || !page.all_zero(LEAF_RESERVED_OFFSET, LEAF_DATA - LEAF_RESERVED_OFFSET)
        || !(1..=LEAF_CAPACITY).contains(&data_len)
        || data_len % 8 != 0
        || start != expected_start
        || start % 8 != 0
        || end > total_bytes
        || (end < total_bytes && data_len != LEAF_CAPACITY)
    {
        return Err(Error::Corrupt("membership blob leaf layout is malformed"));
    }
    Ok(LeafGeometry {
        start,
        end,
        data_len,
    })
}

pub(crate) fn initialize_leaf<D: PageSink>(
    page: &mut D,
    born_txn: u64,
    start: u64,
    data_len: usize,
) -> Result<()> {
    if !(1..=LEAF_CAPACITY).contains(&data_len) || data_len % 8 != 0 || start % 8 != 0 {
        return Err(Error::InvalidArgument(
            "membership blob leaf geometry is invalid",
        ));
    }
    page_header::initialize(
        page,
        page_header::Fields {
            page_type: LEAF_TYPE,
            born_txn,
            item_count: 1,
            level: 0,
            lower: (LEAF_DATA + data_len) as u16,
            upper: PAGE_SIZE as u16,
            aux: MEMBERSHIP_KIND,
        },
    )?;
    page.put_u64(LEAF_START_OFFSET, start)?;
    page.put_u16(LEAF_LENGTH_OFFSET, data_len as u16)
}

pub(crate) fn leaf_bytes<S: ByteSource>(page: S, geometry: LeafGeometry) -> Result<ByteRange<S>> {
    ByteRange::new(page, LEAF_DATA, geometry.data_len).ok_or(Error::Corrupt(
        "membership blob payload is outside its page",
    ))
}
