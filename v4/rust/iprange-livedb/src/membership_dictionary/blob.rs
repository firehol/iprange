//! Fixed-memory construction of immutable membership blobs.

use crate::contract::{u16_le, u32_le, u64_le, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiringStore, Store};
use crate::slotted_page::{self, put_u16, put_u32, put_u64, Builder, HEADER_SIZE};

use super::Words;

const BRANCH_TYPE: u8 = 11;
const LEAF_TYPE: u8 = 12;
const MEMBERSHIP_KIND: u32 = 1;
const LEAF_DATA: usize = 48;
const LEAF_BYTES: usize = PAGE_SIZE - LEAF_DATA;
const LEAF_WORDS: usize = LEAF_BYTES / 8;
const BRANCH_ITEMS: usize = (PAGE_SIZE - HEADER_SIZE) / 18;
const BUILD_LEVELS: usize = 5;

#[derive(Clone, Copy)]
struct Node {
    offset: u64,
    page: u32,
    level: u16,
}

const EMPTY_NODE: Node = Node {
    offset: 0,
    page: 0,
    level: 0,
};

#[derive(Clone, Copy)]
struct Level {
    nodes: [Node; BRANCH_ITEMS],
    len: usize,
}

const EMPTY_LEVEL: Level = Level {
    nodes: [EMPTY_NODE; BRANCH_ITEMS],
    len: 0,
};

pub(super) fn build<S: Store, W: Words<S>>(store: &mut S, words: &W) -> Result<u32> {
    if words.word_count() == 0 {
        return Err(Error::InvalidArgument(
            "empty membership has no blob representation",
        ));
    }
    let mut levels = [EMPTY_LEVEL; BUILD_LEVELS];
    let mut offset_words = 0u32;
    while offset_words < words.word_count() {
        let count = (words.word_count() - offset_words).min(LEAF_WORDS as u32);
        let node = write_leaf(store, words, offset_words, count)?;
        push(store, &mut levels, 0, node)?;
        offset_words += count;
    }
    finish(store, &mut levels)
}

pub(super) fn release<S: RetiringStore>(store: &mut S, root: u32, word_count: u32) -> Result<()> {
    let total = u64::from(word_count) * 8;
    let mut next = 0;
    release_page(store, root, None, total, &mut next, 0)?;
    if next != total {
        return Err(Error::Corrupt(
            "membership blob does not cover its declared length",
        ));
    }
    Ok(())
}

fn release_page<S: RetiringStore>(
    store: &mut S,
    page_number: u32,
    expected_level: Option<u16>,
    total: u64,
    next: &mut u64,
    depth: u16,
) -> Result<()> {
    if depth > MAX_TREE_LEVEL {
        return Err(Error::Corrupt("membership blob exceeds its maximum height"));
    }
    let mut page = [0; PAGE_SIZE];
    store.read(page_number, &mut page)?;
    let level = u16_le(&page, 18);
    if level == 0 {
        release_leaf(&page, store.target_txn(), expected_level, total, next)?;
    } else {
        release_branch(store, &page, level, expected_level, total, next, depth)?;
    }
    release_owned_page(store, page_number, u64_le(&page, 8))
}

fn release_branch<S: RetiringStore>(
    store: &mut S,
    page: &[u8; PAGE_SIZE],
    level: u16,
    expected_level: Option<u16>,
    total: u64,
    next: &mut u64,
    depth: u16,
) -> Result<()> {
    let header = slotted_page::parse(
        page,
        store.target_txn(),
        BRANCH_TYPE,
        MEMBERSHIP_KIND,
        expected_level,
    )?;
    for index in 0..header.item_count {
        let cell = slotted_page::cell(page, &header, index, 16)?;
        let child = u32_le(cell, 8);
        if u64_le(cell, 0) != *next
            || u32_le(cell, 12) != 0
            || child < 2
            || u64::from(child) >= store.page_limit()
        {
            return Err(Error::Corrupt("membership blob branch record is malformed"));
        }
        release_page(store, child, Some(level - 1), total, next, depth + 1)?;
    }
    Ok(())
}

fn release_leaf(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
    total: u64,
    next: &mut u64,
) -> Result<()> {
    let data_len = u64::from(u16_le(page, 40));
    let end = next
        .checked_add(data_len)
        .ok_or(Error::Corrupt("membership blob length overflows"))?;
    require_release_leaf_identity(page, selected_txn, expected_level)?;
    require_release_leaf_layout(page, data_len)?;
    require_release_leaf_coverage(page, *next, end, data_len, total)?;
    *next = end;
    Ok(())
}

fn require_release_leaf_identity(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<()> {
    if page[..4] != PAGE_MAGIC
        || page[4] != LEAF_TYPE
        || page[5] != 0
        || u16_le(page, 6) != HEADER_SIZE as u16
        || u64_le(page, 8) == 0
        || u64_le(page, 8) > selected_txn
        || u16_le(page, 18) != 0
        || expected_level.is_some_and(|level| level != 0)
    {
        return Err(Error::Corrupt("membership blob leaf identity is malformed"));
    }
    Ok(())
}

fn require_release_leaf_layout(page: &[u8; PAGE_SIZE], data_len: u64) -> Result<()> {
    if u16_le(page, 16) != 1
        || u16_le(page, 20) as usize != LEAF_DATA + data_len as usize
        || u16_le(page, 22) as usize != PAGE_SIZE
        || u32_le(page, 24) != MEMBERSHIP_KIND
        || data_len == 0
        || data_len > LEAF_BYTES as u64
        || data_len % 8 != 0
        || page[42..48] != [0; 6]
    {
        return Err(Error::Corrupt("membership blob leaf layout is malformed"));
    }
    Ok(())
}

fn require_release_leaf_coverage(
    page: &[u8; PAGE_SIZE],
    next: u64,
    end: u64,
    data_len: u64,
    total: u64,
) -> Result<()> {
    if u64_le(page, 32) != next || end > total || (end < total && data_len != LEAF_BYTES as u64) {
        return Err(Error::Corrupt("membership blob leaf coverage is malformed"));
    }
    Ok(())
}

fn release_owned_page<S: RetiringStore>(
    store: &mut S,
    page_number: u32,
    born_txn: u64,
) -> Result<()> {
    if born_txn == store.target_txn() {
        store.discard_private(page_number)
    } else {
        store.retire_pages(&[page_number])
    }
}

fn write_leaf<S: Store, W: Words<S>>(
    store: &mut S,
    words: &W,
    offset_words: u32,
    count: u32,
) -> Result<Node> {
    let mut values = [0u64; LEAF_WORDS];
    words.read_words(store, offset_words, &mut values[..count as usize])?;
    let page_number = store.allocate()?;
    let mut page = [0; PAGE_SIZE];
    page[..4].copy_from_slice(&PAGE_MAGIC);
    page[4] = LEAF_TYPE;
    put_u16(&mut page, 6, HEADER_SIZE as u16);
    put_u64(&mut page, 8, store.target_txn());
    put_u16(&mut page, 16, 1);
    put_u16(&mut page, 20, (LEAF_DATA + count as usize * 8) as u16);
    put_u16(&mut page, 22, PAGE_SIZE as u16);
    put_u32(&mut page, 24, MEMBERSHIP_KIND);
    put_u64(&mut page, 32, u64::from(offset_words) * 8);
    put_u16(&mut page, 40, (count * 8) as u16);
    for (index, value) in values[..count as usize].iter().enumerate() {
        put_u64(&mut page, LEAF_DATA + index * 8, *value);
    }
    stamp(&mut page)?;
    store.write(page_number, &page)?;
    Ok(Node {
        offset: u64::from(offset_words) * 8,
        page: page_number,
        level: 0,
    })
}

fn push<S: Store>(
    store: &mut S,
    levels: &mut [Level; BUILD_LEVELS],
    level: usize,
    node: Node,
) -> Result<()> {
    if level >= BUILD_LEVELS {
        return Err(Error::Corrupt("membership blob exceeds its height bound"));
    }
    if levels[level].len == BRANCH_ITEMS {
        let parent = flush(store, &mut levels[level])?;
        push(store, levels, level + 1, parent)?;
    }
    let slot = levels[level].len;
    levels[level].nodes[slot] = node;
    levels[level].len += 1;
    Ok(())
}

fn finish<S: Store>(store: &mut S, levels: &mut [Level; BUILD_LEVELS]) -> Result<u32> {
    loop {
        let mut count = 0usize;
        let mut only = EMPTY_NODE;
        let mut lowest = None;
        for (index, level) in levels.iter().enumerate() {
            if level.len != 0 && lowest.is_none() {
                lowest = Some(index);
            }
            count += level.len;
            if level.len == 1 {
                only = level.nodes[0];
            }
        }
        if count == 1 {
            return Ok(only.page);
        }
        let level = lowest.ok_or(Error::Corrupt("membership blob builder is empty"))?;
        let parent = flush(store, &mut levels[level])?;
        push(store, levels, level + 1, parent)?;
    }
}

fn flush<S: Store>(store: &mut S, level: &mut Level) -> Result<Node> {
    if level.len == 0 {
        return Err(Error::Corrupt("membership blob level is empty"));
    }
    let child_level = level.nodes[0].level;
    if level.nodes[..level.len]
        .iter()
        .any(|node| node.level != child_level)
    {
        return Err(Error::Corrupt("membership blob level mixes child heights"));
    }
    let page_number = store.allocate()?;
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(
        &mut page,
        BRANCH_TYPE,
        store.target_txn(),
        child_level + 1,
        MEMBERSHIP_KIND,
    );
    for node in &level.nodes[..level.len] {
        let mut record = [0; 16];
        put_u64(&mut record, 0, node.offset);
        put_u32(&mut record, 8, node.page);
        builder.push(&record)?;
    }
    builder.finish()?;
    store.write(page_number, &page)?;
    let result = Node {
        offset: level.nodes[0].offset,
        page: page_number,
        level: child_level + 1,
    };
    *level = EMPTY_LEVEL;
    Ok(result)
}

fn stamp(page: &mut [u8; PAGE_SIZE]) -> Result<()> {
    let checksum = crate::crc32c::crc32c_with_zeroed(page, 28, 4)
        .ok_or(Error::Corrupt("membership blob checksum field is invalid"))?;
    put_u32(page, 28, checksum);
    Ok(())
}
