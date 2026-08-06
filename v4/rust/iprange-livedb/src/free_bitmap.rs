//! Hierarchical free-page bitmap with bounded four-page paths.

#[path = "free_bitmap/mutation.rs"]
mod mutation;

pub(crate) use mutation::{ensure_level, set_free, take_lowest};

use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::mapping::ByteSource;
use crate::slotted_page::{PageEdit, PageSink, HEADER_SIZE};

const BITMAP_BRANCH: u8 = 14;
const BITMAP_LEAF: u8 = 15;
const FREE_AUX: u32 = 1;
const LEAF_WORDS: usize = 500;
const LEAF_BITS: u64 = (LEAF_WORDS * 64) as u64;
const BRANCH_CHILDREN: usize = 256;
const LEAF_END: usize = HEADER_SIZE + LEAF_WORDS * 8;
const BRANCH_END: usize = HEADER_SIZE + 32 + BRANCH_CHILDREN * 4;
const MAX_BITMAP_LEVEL: u16 = 3;

pub(crate) trait BitmapStore: Store {
    fn allocate_bitmap_page(&mut self) -> Result<u32>;
    fn allocation_forbidden(&self, page_number: u32) -> bool;
}

#[derive(Clone, Copy)]
struct Header {
    level: u16,
}

fn parse<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
    verify_crc: bool,
) -> Result<Header> {
    let header = parse_header(page, selected_txn, expected_level)?;
    verify_checksum(page, verify_crc)?;
    validate_body(page, &header)?;
    Ok(header)
}

fn parse_header<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<Header> {
    if page.len() != PAGE_SIZE
        || !page.equals(0, &PAGE_MAGIC)
        || page.byte(5) != Some(0)
        || u16_le(page, 6) != HEADER_SIZE as u16
        || u32_le(page, 24) != FREE_AUX
    {
        return Err(Error::Corrupt("free bitmap header is invalid"));
    }
    let born_txn = u64_le(page, 8);
    let level = u16_le(page, 18);
    if born_txn == 0
        || born_txn > selected_txn
        || level > MAX_BITMAP_LEVEL
        || expected_level.is_some_and(|expected| expected != level)
        || u16_le(page, 16) == 0
    {
        return Err(Error::Corrupt("free bitmap ownership or level is invalid"));
    }
    Ok(Header { level })
}

fn verify_checksum<S: ByteSource>(page: S, required: bool) -> Result<()> {
    if required && crc32c::crc32c_source_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
        return Err(Error::Corrupt("free bitmap checksum is invalid"));
    }
    Ok(())
}

fn validate_body<S: ByteSource>(page: S, header: &Header) -> Result<()> {
    if header.level == 0 {
        if page.byte(4) != Some(BITMAP_LEAF)
            || usize::from(u16_le(page, 20)) != LEAF_END
            || usize::from(u16_le(page, 22)) != PAGE_SIZE
            || !page.all_zero(LEAF_END, PAGE_SIZE - LEAF_END)
            || first_leaf_word(page).is_none()
            || nonzero_leaf_words(page) != usize::from(u16_le(page, 16))
        {
            return Err(Error::Corrupt("free bitmap leaf is invalid"));
        }
    } else if page.byte(4) != Some(BITMAP_BRANCH)
        || usize::from(u16_le(page, 20)) != BRANCH_END
        || usize::from(u16_le(page, 22)) != PAGE_SIZE
        || !page.all_zero(BRANCH_END, PAGE_SIZE - BRANCH_END)
        || first_summary(page).is_none()
        || nonzero_children(page)? != usize::from(u16_le(page, 16))
    {
        return Err(Error::Corrupt("free bitmap branch is invalid"));
    }
    Ok(())
}

fn initialize<D: PageSink>(page: &mut D, page_type: u8, txn: u64, level: u16, lower: usize) {
    page.fill(0);
    page.write(0, &PAGE_MAGIC)
        .expect("fixed bitmap header fits");
    page.set_byte(4, page_type)
        .expect("fixed bitmap header fits");
    page.put_u16(6, HEADER_SIZE as u16)
        .expect("fixed bitmap header fits");
    page.put_u64(8, txn).expect("fixed bitmap header fits");
    page.put_u16(18, level).expect("fixed bitmap header fits");
    page.put_u16(20, lower as u16)
        .expect("fixed bitmap header fits");
    page.put_u16(22, PAGE_SIZE as u16)
        .expect("fixed bitmap header fits");
    page.put_u32(24, FREE_AUX)
        .expect("fixed bitmap header fits");
}

fn stamp<D: PageEdit>(page: &mut D) -> Result<()> {
    let count = if u16_le(page.view(), 18) == 0 {
        nonzero_leaf_words(page.view())
    } else {
        nonzero_children(page.view())?
    };
    page.put_u16(16, count as u16)
}

fn set_branch_child<D: PageEdit>(page: &mut D, index: usize, child: u32) -> Result<()> {
    if index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("free bitmap child index is invalid"));
    }
    page.put_u32(HEADER_SIZE + 32 + index * 4, child)?;
    let summary_at = HEADER_SIZE + (index / 64) * 8;
    let mask = 1u64 << (index % 64);
    let summary = u64_le(page.view(), summary_at);
    page.put_u64(
        summary_at,
        if child == 0 {
            summary & !mask
        } else {
            summary | mask
        },
    )
}

fn branch_child<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    if header.level == 0 || index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("free bitmap child lookup is invalid"));
    }
    let child = u32_le(page, HEADER_SIZE + 32 + index * 4);
    if child != 0 && (child < 2 || u64::from(child) >= page_limit) {
        return Err(Error::Corrupt("free bitmap child is outside page bounds"));
    }
    Ok(child)
}

fn nonzero_children<S: ByteSource>(page: S) -> Result<usize> {
    let mut count = 0;
    for index in 0..BRANCH_CHILDREN {
        let child = u32_le(page, HEADER_SIZE + 32 + index * 4);
        let summary = u64_le(page, HEADER_SIZE + (index / 64) * 8);
        if ((summary >> (index % 64)) & 1 != 0) != (child != 0) {
            return Err(Error::Corrupt("free bitmap summary disagrees with child"));
        }
        count += usize::from(child != 0);
    }
    Ok(count)
}

fn nonzero_leaf_words<S: ByteSource>(page: S) -> usize {
    (0..LEAF_WORDS)
        .filter(|&index| u64_le(page, HEADER_SIZE + index * 8) != 0)
        .count()
}

fn first_summary<S: ByteSource>(page: S) -> Option<usize> {
    (0..4).find_map(|word| {
        let value = u64_le(page, HEADER_SIZE + word * 8);
        (value != 0).then(|| word * 64 + value.trailing_zeros() as usize)
    })
}

fn first_leaf_word<S: ByteSource>(page: S) -> Option<(usize, u64)> {
    (0..LEAF_WORDS).find_map(|index| {
        let value = u64_le(page, HEADER_SIZE + index * 8);
        (value != 0).then_some((index, value))
    })
}

fn leaf_word_index(bit: u32) -> usize {
    ((u64::from(bit) % LEAF_BITS) / 64) as usize
}

fn child_index(bit: u32, level: u16) -> Result<usize> {
    Ok(((u64::from(bit) / coverage(level - 1)?) % BRANCH_CHILDREN as u64) as usize)
}

fn coverage(level: u16) -> Result<u64> {
    let mut value = LEAF_BITS;
    for _ in 0..level {
        value = value
            .checked_mul(BRANCH_CHILDREN as u64)
            .ok_or(Error::Corrupt("free bitmap coverage overflow"))?;
    }
    Ok(value)
}

fn required_level(limit: u64) -> Result<u16> {
    if !(2..=1u64 << 32).contains(&limit) {
        return Err(Error::InvalidArgument("free bitmap limit is invalid"));
    }
    for level in 0..=MAX_BITMAP_LEVEL {
        if coverage(level)? >= limit {
            return Ok(level);
        }
    }
    Err(Error::Corrupt("free bitmap limit exceeds maximum coverage"))
}

fn require_bit(limit: u64, bit: u32) -> Result<()> {
    required_level(limit)?;
    if bit < 2 || u64::from(bit) >= limit {
        return Err(Error::InvalidArgument(
            "free page is outside the bitmap limit",
        ));
    }
    Ok(())
}

#[cfg(test)]
#[path = "free_bitmap_tests.rs"]
mod tests;
