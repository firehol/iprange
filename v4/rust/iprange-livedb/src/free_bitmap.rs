//! Hierarchical free-page bitmap with bounded four-page paths.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, Store};
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

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    child_index: usize,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    child_index: 0,
    level: 0,
};

pub(crate) fn set_free<S: BitmapStore>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<()> {
    require_bit(limit, bit)?;
    let required = required_level(limit)?;
    if *root == 0 {
        *root = new_subtree(store, required, bit)?;
        return Ok(());
    }
    grow_root(store, root, required)?;

    let (mut page_number, mut page, mut header) = touch(store, *root, required, retired)?;
    *root = page_number;
    while header.level > 0 {
        let index = child_index(bit, header.level)?;
        let child = branch_child(&page, &header, index, store.page_limit())?;
        if child == 0 {
            let new_child = new_subtree(store, header.level - 1, bit)?;
            set_branch_child(&mut page, index, new_child)?;
            stamp(&mut page)?;
            store.write(page_number, &page)?;
            return Ok(());
        }
        let (private_child, child_page, child_header) =
            touch(store, child, header.level - 1, retired)?;
        if private_child != child {
            set_branch_child(&mut page, index, private_child)?;
            stamp(&mut page)?;
            store.write(page_number, &page)?;
        }
        page_number = private_child;
        page = child_page;
        header = child_header;
    }

    let word_index = leaf_word_index(bit);
    let mask = 1u64 << (u64::from(bit) % 64);
    let at = HEADER_SIZE + word_index * 8;
    let word = u64_le(&page, at);
    if word & mask != 0 {
        return Err(Error::Corrupt("page is already free"));
    }
    put_u64(&mut page, at, word | mask);
    stamp(&mut page)?;
    store.write(page_number, &page)
}

pub(crate) fn take_lowest<S: BitmapStore>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<Option<u32>> {
    if *root == 0 {
        return Ok(None);
    }
    let required = required_level(limit)?;
    let (mut page_number, mut page, mut header) = touch(store, *root, required, retired)?;
    *root = page_number;
    let mut path = [EMPTY_FRAME; (MAX_BITMAP_LEVEL + 1) as usize];
    let mut depth = 0;
    let mut base = 0u64;

    while header.level > 0 {
        let index = first_summary(&page).ok_or(Error::Corrupt("free summary is empty"))?;
        path[depth] = Frame {
            page_number,
            child_index: index,
            level: header.level,
        };
        depth += 1;
        let child_coverage = coverage(header.level - 1)?;
        base = base
            .checked_add(
                child_coverage
                    .checked_mul(index as u64)
                    .ok_or(Error::Corrupt("free bitmap coverage overflow"))?,
            )
            .ok_or(Error::Corrupt("free bitmap coverage overflow"))?;
        let child = branch_child(&page, &header, index, store.page_limit())?;
        if child == 0 {
            return Err(Error::Corrupt("free summary names an absent child"));
        }
        let (private_child, child_page, child_header) =
            touch(store, child, header.level - 1, retired)?;
        if private_child != child {
            set_branch_child(&mut page, index, private_child)?;
            stamp(&mut page)?;
            store.write(page_number, &page)?;
        }
        page_number = private_child;
        page = child_page;
        header = child_header;
    }

    let (word_index, word) = first_leaf_word(&page).ok_or(Error::Corrupt("free leaf is empty"))?;
    let bit_in_word = word.trailing_zeros() as u64;
    let selected = base
        .checked_add((word_index as u64) * 64 + bit_in_word)
        .ok_or(Error::Corrupt("free page number overflow"))?;
    if selected < 2 || selected >= limit || selected >= (1u64 << 32) {
        return Err(Error::Corrupt("free bit is outside allocatable bounds"));
    }
    let selected = selected as u32;
    if selected == page_number
        || path[..depth]
            .iter()
            .any(|frame| frame.page_number == selected)
        || store.allocation_forbidden(selected)
    {
        return Err(Error::Corrupt("free bit names protected allocator state"));
    }

    let at = HEADER_SIZE + word_index * 8;
    put_u64(&mut page, at, word & !(1u64 << bit_in_word));
    if first_leaf_word(&page).is_some() {
        stamp(&mut page)?;
        store.write(page_number, &page)?;
        return Ok(Some(selected));
    }

    store.discard_private(page_number)?;
    while depth > 0 {
        depth -= 1;
        let frame = path[depth];
        let mut parent = [0; PAGE_SIZE];
        store.read(frame.page_number, &mut parent)?;
        parse(&parent, store.target_txn(), Some(frame.level), false)?;
        set_branch_child(&mut parent, frame.child_index, 0)?;
        if first_summary(&parent).is_some() {
            stamp(&mut parent)?;
            store.write(frame.page_number, &parent)?;
            return Ok(Some(selected));
        }
        store.discard_private(frame.page_number)?;
    }
    *root = 0;
    Ok(Some(selected))
}

pub(crate) fn ensure_level<S: BitmapStore>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
) -> Result<()> {
    if *root == 0 {
        return Ok(());
    }
    loop {
        let required = required_level(limit.max(store.page_limit()))?;
        let before = store.page_limit();
        grow_root(store, root, required)?;
        if store.page_limit() == before {
            return Ok(());
        }
    }
}

fn touch<S: BitmapStore>(
    store: &mut S,
    page_number: u32,
    expected_level: u16,
    retired: &mut RetiredPages,
) -> Result<(u32, [u8; PAGE_SIZE], Header)> {
    let mut page = [0; PAGE_SIZE];
    store.read(page_number, &mut page)?;
    let born_txn = u64_le(&page, 8);
    let header = parse(
        &page,
        store.target_txn(),
        Some(expected_level),
        born_txn != store.target_txn(),
    )?;
    if born_txn == store.target_txn() {
        return Ok((page_number, page, header));
    }
    let private = store.allocate_bitmap_page()?;
    put_u64(&mut page, 8, store.target_txn());
    stamp(&mut page)?;
    store.write(private, &page)?;
    retired.push(page_number)?;
    Ok((private, page, header))
}

fn grow_root<S: BitmapStore>(store: &mut S, root: &mut u32, required: u16) -> Result<()> {
    let mut page = [0; PAGE_SIZE];
    store.read(*root, &mut page)?;
    let mut level = parse(&page, store.target_txn(), None, false)?.level;
    if level > required {
        return Err(Error::Corrupt("free bitmap root level is too high"));
    }
    while level < required {
        let parent = store.allocate_bitmap_page()?;
        let mut page = new_branch(store.target_txn(), level + 1);
        set_branch_child(&mut page, 0, *root)?;
        stamp(&mut page)?;
        store.write(parent, &page)?;
        *root = parent;
        level += 1;
    }
    Ok(())
}

fn new_subtree<S: BitmapStore>(store: &mut S, level: u16, bit: u32) -> Result<u32> {
    if level == 0 {
        let page_number = store.allocate_bitmap_page()?;
        let mut page = new_leaf(store.target_txn());
        let word = leaf_word_index(bit);
        put_u64(
            &mut page,
            HEADER_SIZE + word * 8,
            1u64 << (u64::from(bit) % 64),
        );
        stamp(&mut page)?;
        store.write(page_number, &page)?;
        return Ok(page_number);
    }
    let child = new_subtree(store, level - 1, bit)?;
    let page_number = store.allocate_bitmap_page()?;
    let mut page = new_branch(store.target_txn(), level);
    set_branch_child(&mut page, child_index(bit, level)?, child)?;
    stamp(&mut page)?;
    store.write(page_number, &page)?;
    Ok(page_number)
}

fn parse(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
    verify_crc: bool,
) -> Result<Header> {
    if page[..4] != PAGE_MAGIC
        || page[5] != 0
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
    {
        return Err(Error::Corrupt("free bitmap ownership or level is invalid"));
    }
    let item_count = usize::from(u16_le(page, 16));
    if item_count == 0 {
        return Err(Error::Corrupt("reachable free bitmap page is empty"));
    }
    if verify_crc && crc32c::crc32c_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
        return Err(Error::Corrupt("free bitmap checksum is invalid"));
    }

    if level == 0 {
        if page[4] != BITMAP_LEAF
            || u16_le(page, 20) as usize != LEAF_END
            || u16_le(page, 22) as usize != PAGE_SIZE
            || page[LEAF_END..].iter().any(|&byte| byte != 0)
            || first_leaf_word(page).is_none()
            || nonzero_leaf_words(page) != item_count
        {
            return Err(Error::Corrupt("free bitmap leaf is invalid"));
        }
    } else if page[4] != BITMAP_BRANCH
        || u16_le(page, 20) as usize != BRANCH_END
        || u16_le(page, 22) as usize != PAGE_SIZE
        || page[BRANCH_END..].iter().any(|&byte| byte != 0)
        || first_summary(page).is_none()
        || nonzero_children(page)? != item_count
    {
        return Err(Error::Corrupt("free bitmap branch is invalid"));
    }
    Ok(Header { level })
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
    put_u32(page, 28, 0);
    let checksum = crc32c::crc32c_with_zeroed(page, 28, 4)
        .ok_or(Error::Corrupt("free bitmap checksum field is invalid"))?;
    put_u32(page, 28, checksum);
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
