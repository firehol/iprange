//! Hierarchical free-page bitmap with bounded four-page paths.

#[path = "free_bitmap/mutation.rs"]
mod mutation;

pub(crate) use mutation::{ensure_level, set_free, take_lowest};

use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::slotted_page::{put_u16, put_u32, put_u64, HEADER_SIZE};

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
    /// Allocate without consulting the free bitmap being changed.
    fn allocate_bitmap_page(&mut self) -> Result<u32>;

    /// Reject allocator metadata and current roots if corruption marks them free.
    fn allocation_forbidden(&self, page_number: u32) -> bool;
}

#[derive(Clone, Copy)]
struct Header {
    level: u16,
}

fn parse(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
    verify_crc: bool,
) -> Result<Header> {
    let header = parse_header(page, selected_txn, expected_level)?;
    verify_checksum(page, verify_crc)?;
    validate_body(page, &header)?;
    Ok(header)
}

fn parse_header(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<Header> {
    validate_prefix(page)?;
    let born_txn = u64_le(page, 8);
    let level = u16_le(page, 18);
    if born_txn == 0
        || born_txn > selected_txn
        || level > MAX_BITMAP_LEVEL
        || expected_level.is_some_and(|expected| expected != level)
    {
        return Err(Error::Corrupt("free bitmap ownership or level is invalid"));
    }
    let item_count = usize::from(u16_le(page, 16));
    if item_count == 0 {
        return Err(Error::Corrupt("reachable free bitmap page is empty"));
    }
    Ok(Header { level })
}

fn validate_prefix(page: &[u8; PAGE_SIZE]) -> Result<()> {
    if page[..4] != PAGE_MAGIC
        || page[5] != 0
        || u16_le(page, 6) != HEADER_SIZE as u16
        || u32_le(page, 24) != FREE_AUX
    {
        return Err(Error::Corrupt("free bitmap header is invalid"));
    }
    Ok(())
}

fn verify_checksum(page: &[u8; PAGE_SIZE], required: bool) -> Result<()> {
    if required && crc32c::crc32c_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
        return Err(Error::Corrupt("free bitmap checksum is invalid"));
    }
    Ok(())
}

fn validate_body(page: &[u8; PAGE_SIZE], header: &Header) -> Result<()> {
    if header.level == 0 {
        validate_leaf(page)
    } else {
        validate_branch(page)
    }
}

fn validate_leaf(page: &[u8; PAGE_SIZE]) -> Result<()> {
    if page[4] != BITMAP_LEAF
        || u16_le(page, 20) as usize != LEAF_END
        || u16_le(page, 22) as usize != PAGE_SIZE
        || page[LEAF_END..].iter().any(|&byte| byte != 0)
        || first_leaf_word(page).is_none()
        || nonzero_leaf_words(page) != usize::from(u16_le(page, 16))
    {
        return Err(Error::Corrupt("free bitmap leaf is invalid"));
    }
    Ok(())
}

fn validate_branch(page: &[u8; PAGE_SIZE]) -> Result<()> {
    if page[4] != BITMAP_BRANCH
        || u16_le(page, 20) as usize != BRANCH_END
        || u16_le(page, 22) as usize != PAGE_SIZE
        || page[BRANCH_END..].iter().any(|&byte| byte != 0)
        || first_summary(page).is_none()
        || nonzero_children(page)? != usize::from(u16_le(page, 16))
    {
        return Err(Error::Corrupt("free bitmap branch is invalid"));
    }
    Ok(())
}

fn new_leaf(txn: u64) -> [u8; PAGE_SIZE] {
    new_page(BITMAP_LEAF, txn, 0, LEAF_END)
}

fn new_branch(txn: u64, level: u16) -> [u8; PAGE_SIZE] {
    new_page(BITMAP_BRANCH, txn, level, BRANCH_END)
}

fn new_page(page_type: u8, txn: u64, level: u16, lower: usize) -> [u8; PAGE_SIZE] {
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = page_type;
    put_u16(&mut page, 6, HEADER_SIZE as u16);
    put_u64(&mut page, 8, txn);
    put_u16(&mut page, 18, level);
    put_u16(&mut page, 20, lower as u16);
    put_u16(&mut page, 22, PAGE_SIZE as u16);
    put_u32(&mut page, 24, FREE_AUX);
    page
}

fn stamp(page: &mut [u8; PAGE_SIZE]) -> Result<()> {
    let count = if u16_le(page, 18) == 0 {
        nonzero_leaf_words(page)
    } else {
        nonzero_children(page)?
    };
    put_u16(page, 16, count as u16);
    Ok(())
}

fn set_branch_child(page: &mut [u8; PAGE_SIZE], index: usize, child: u32) -> Result<()> {
    if index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("free bitmap child index is invalid"));
    }
    put_u32(page, HEADER_SIZE + 32 + index * 4, child);
    let summary_at = HEADER_SIZE + (index / 64) * 8;
    let mask = 1u64 << (index % 64);
    let summary = u64_le(page, summary_at);
    put_u64(
        page,
        summary_at,
        if child == 0 {
            summary & !mask
        } else {
            summary | mask
        },
    );
    Ok(())
}

fn branch_child(
    page: &[u8; PAGE_SIZE],
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

fn nonzero_children(page: &[u8; PAGE_SIZE]) -> Result<usize> {
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

fn nonzero_leaf_words(page: &[u8; PAGE_SIZE]) -> usize {
    (0..LEAF_WORDS)
        .filter(|&index| u64_le(page, HEADER_SIZE + index * 8) != 0)
        .count()
}

fn first_summary(page: &[u8; PAGE_SIZE]) -> Option<usize> {
    (0..4).find_map(|word| {
        let value = u64_le(page, HEADER_SIZE + word * 8);
        (value != 0).then(|| word * 64 + value.trailing_zeros() as usize)
    })
}

fn first_leaf_word(page: &[u8; PAGE_SIZE]) -> Option<(usize, u64)> {
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
