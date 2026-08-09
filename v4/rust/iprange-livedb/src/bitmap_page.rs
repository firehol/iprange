//! Canonical hierarchical-bitmap page layout and scalar access.

use crate::contract::{u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::format::page_type;
use crate::mapping::ByteSource;
use crate::page_header;
use crate::slotted_page::{PageEdit, PageSink, HEADER_SIZE};

pub(crate) const BRANCH_TYPE: u8 = page_type::USED_BITMAP_BRANCH;
pub(crate) const LEAF_TYPE: u8 = page_type::USED_BITMAP_LEAF;
pub(crate) const LEAF_WORDS: usize = 500;
pub(crate) const LEAF_BITS: u64 = (LEAF_WORDS * 64) as u64;
pub(crate) const BRANCH_CHILDREN: usize = 256;
pub(crate) const LEAF_END: usize = HEADER_SIZE + LEAF_WORDS * 8;
pub(crate) const BRANCH_END: usize = HEADER_SIZE + 32 + BRANCH_CHILDREN * 4;
pub(crate) const MAX_LEVEL: u16 = 3;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(crate) enum Kind {
    Free = 1,
    Feed = 2,
    Membership = 3,
}

impl Kind {
    pub(crate) const fn first_candidate(self) -> u64 {
        match self {
            Self::Membership => 1,
            Self::Free | Self::Feed => 0,
        }
    }
}

#[derive(Clone, Copy)]
pub(crate) struct Header {
    pub(crate) level: u16,
    pub(crate) item_count: usize,
}

#[derive(Clone, Copy)]
pub(crate) enum HeaderProblem {
    Header,
    Born,
    Level,
    Type,
}

pub(crate) fn inspect_header<S: ByteSource>(
    page: S,
    selected_txn: u64,
    kind: Kind,
    expected_level: Option<u16>,
) -> Result<Header> {
    checked_header(page, selected_txn, kind, expected_level)
        .map_err(|_| Error::Corrupt("bitmap page header is invalid"))
}

pub(crate) fn checked_header<S: ByteSource>(
    page: S,
    selected_txn: u64,
    kind: Kind,
    expected_level: Option<u16>,
) -> std::result::Result<Header, HeaderProblem> {
    if let Some(problem) = header_problem(page, selected_txn, kind, expected_level) {
        return Err(problem);
    }
    Ok(Header {
        level: page_header::level(page),
        item_count: page_header::item_count(page),
    })
}

pub(crate) fn header_problem<S: ByteSource>(
    page: S,
    selected_txn: u64,
    kind: Kind,
    expected_level: Option<u16>,
) -> Option<HeaderProblem> {
    let level = page_header::level(page);
    let lower = page_lower(level);
    if !page_header::common_valid(page)
        || page_header::lower(page) != lower
        || page_header::upper(page) != PAGE_SIZE
    {
        return Some(HeaderProblem::Header);
    }
    if !page_header::born_valid(page, selected_txn) {
        return Some(HeaderProblem::Born);
    }
    if level > MAX_LEVEL || expected_level.is_some_and(|expected| expected != level) {
        return Some(HeaderProblem::Level);
    }
    let expected_type = if level == 0 { LEAF_TYPE } else { BRANCH_TYPE };
    if !page_header::kind_valid(page, expected_type, kind as u32) {
        return Some(HeaderProblem::Type);
    }
    let count = page_header::item_count(page);
    let maximum = if level == 0 {
        LEAF_WORDS
    } else {
        BRANCH_CHILDREN
    };
    if count == 0 || count > maximum {
        return Some(HeaderProblem::Header);
    }
    None
}

pub(crate) const fn page_lower(level: u16) -> usize {
    if level == 0 {
        LEAF_END
    } else {
        BRANCH_END
    }
}

pub(crate) fn reserved_zero<S: ByteSource>(page: S, level: u16) -> bool {
    let lower = page_lower(level);
    page.all_zero(lower, PAGE_SIZE - lower)
}

pub(crate) fn initialize<D: PageSink>(page: &mut D, txn: u64, level: u16, kind: Kind) {
    page.fill(0);
    page.write(0, &PAGE_MAGIC)
        .expect("fixed bitmap header fits");
    page.set_byte(
        page_header::TYPE,
        if level == 0 { LEAF_TYPE } else { BRANCH_TYPE },
    )
    .expect("fixed bitmap header fits");
    page.put_u16(page_header::HEADER_BYTES, HEADER_SIZE as u16)
        .expect("fixed bitmap header fits");
    page.put_u64(page_header::BORN_TXN, txn)
        .expect("fixed bitmap header fits");
    page.put_u16(page_header::LEVEL, level)
        .expect("fixed bitmap header fits");
    page.put_u16(page_header::LOWER, page_lower(level) as u16)
        .expect("fixed bitmap header fits");
    page.put_u16(page_header::UPPER, PAGE_SIZE as u16)
        .expect("fixed bitmap header fits");
    page.put_u32(page_header::AUX, kind as u32)
        .expect("fixed bitmap header fits");
}

pub(crate) fn leaf_word<S: ByteSource>(page: S, index: usize) -> Result<u64> {
    if index >= LEAF_WORDS {
        return Err(Error::Corrupt("bitmap word index is invalid"));
    }
    Ok(u64_le(page, HEADER_SIZE + index * 8))
}

pub(crate) fn set_leaf_word<D: PageEdit>(page: &mut D, index: usize, word: u64) -> Result<()> {
    if index >= LEAF_WORDS {
        return Err(Error::Corrupt("bitmap word index is invalid"));
    }
    page.put_u64(HEADER_SIZE + index * 8, word)
}

pub(crate) fn branch_child<S: ByteSource>(page: S, index: usize) -> Result<u32> {
    if index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("bitmap child index is invalid"));
    }
    Ok(u32_le(page, HEADER_SIZE + 32 + index * 4))
}

pub(crate) fn set_branch_child<D: PageEdit>(page: &mut D, index: usize, child: u32) -> Result<()> {
    if index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("bitmap child index is invalid"));
    }
    page.put_u32(HEADER_SIZE + 32 + index * 4, child)
}

pub(crate) fn summary_bit<S: ByteSource>(page: S, index: usize) -> Result<bool> {
    if index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("bitmap summary index is invalid"));
    }
    Ok(u64_le(page, HEADER_SIZE + (index / 64) * 8) & (1u64 << (index % 64)) != 0)
}

pub(crate) fn set_summary<D: PageEdit>(page: &mut D, index: usize, value: bool) -> Result<()> {
    if index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("bitmap summary index is invalid"));
    }
    let at = HEADER_SIZE + (index / 64) * 8;
    let mask = 1u64 << (index % 64);
    let word = u64_le(page.view(), at);
    page.put_u64(at, if value { word | mask } else { word & !mask })
}

pub(crate) fn first_summary<S: ByteSource>(page: S, start: usize) -> Option<usize> {
    if start >= BRANCH_CHILDREN {
        return None;
    }
    let mut word_index = start / 64;
    let mut word = u64_le(page, HEADER_SIZE + word_index * 8) & (u64::MAX << (start % 64));
    loop {
        if word != 0 {
            return Some(word_index * 64 + word.trailing_zeros() as usize);
        }
        word_index += 1;
        if word_index == 4 {
            return None;
        }
        word = u64_le(page, HEADER_SIZE + word_index * 8);
    }
}

pub(crate) fn first_leaf_word<S: ByteSource>(page: S) -> Option<(usize, u64)> {
    (0..LEAF_WORDS).find_map(|index| {
        let value = leaf_word(page, index).expect("bounded bitmap word index");
        (value != 0).then_some((index, value))
    })
}

pub(crate) fn nonzero_leaf_words<S: ByteSource>(page: S) -> usize {
    (0..LEAF_WORDS)
        .filter(|&index| leaf_word(page, index).expect("bounded bitmap word index") != 0)
        .count()
}

pub(crate) fn coverage(level: u16) -> Result<u64> {
    let mut value = LEAF_BITS;
    for _ in 0..level {
        value = value
            .checked_mul(BRANCH_CHILDREN as u64)
            .ok_or(Error::ArithmeticOverflow("bitmap coverage"))?;
    }
    Ok(value)
}

pub(crate) fn required_level(limit: u64) -> Result<u16> {
    if limit == 0 || limit > 1u64 << 32 {
        return Err(Error::InvalidArgument("bitmap limit is invalid"));
    }
    for level in 0..=MAX_LEVEL {
        if coverage(level)? >= limit {
            return Ok(level);
        }
    }
    Err(Error::ArithmeticOverflow("bitmap limit"))
}

pub(crate) fn leaf_word_index(bit: u32) -> usize {
    ((u64::from(bit) % LEAF_BITS) / 64) as usize
}

pub(crate) fn child_index(bit: u32, level: u16) -> Result<usize> {
    Ok(((u64::from(bit) / coverage(level - 1)?) % BRANCH_CHILDREN as u64) as usize)
}
