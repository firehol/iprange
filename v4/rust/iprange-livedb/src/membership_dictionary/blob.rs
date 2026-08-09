//! Fixed-memory construction of immutable membership blobs.

use crate::contract::{u16_le, u32_le, u64_le, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiringStore, Store};
use crate::format::page_type;
use crate::mapping::ByteSource;
use crate::slotted_page::{self, put_u32, put_u64, Builder, PageSink, HEADER_SIZE};

use super::Words;

const BRANCH_TYPE: u8 = page_type::MEMBERSHIP_BLOB_BRANCH;
const LEAF_TYPE: u8 = page_type::MEMBERSHIP_BLOB_LEAF;
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
    let (level, born_txn) =
        store.inspect_page(page_number, |page| Ok((u16_le(page, 18), u64_le(page, 8))))?;
    if level == 0 {
        store.inspect_page(page_number, |page| {
            release_leaf(page, store.target_txn(), expected_level, total, next)
        })?;
    } else {
        release_branch(
            store,
            page_number,
            level,
            expected_level,
            total,
            next,
            depth,
        )?;
    }
    release_owned_page(store, page_number, born_txn)
}

fn release_branch<S: RetiringStore>(
    store: &mut S,
    page_number: u32,
    level: u16,
    expected_level: Option<u16>,
    total: u64,
    next: &mut u64,
    depth: u16,
) -> Result<()> {
    let item_count = store.inspect_page(page_number, |page| {
        slotted_page::parse(
            page,
            store.target_txn(),
            BRANCH_TYPE,
            MEMBERSHIP_KIND,
            expected_level,
        )
        .map(|header| header.item_count)
    })?;
    for index in 0..item_count {
        let child = store.inspect_page(page_number, |page| {
            let header = slotted_page::parse(
                page,
                store.target_txn(),
                BRANCH_TYPE,
                MEMBERSHIP_KIND,
                expected_level,
            )?;
            let cell = slotted_page::cell(page, &header, index, 16)?;
            let child = u32_le(cell, 8);
            if u64_le(cell, 0) != *next
                || u32_le(cell, 12) != 0
                || child < 2
                || u64::from(child) >= store.page_limit()
            {
                return Err(Error::Corrupt("membership blob branch record is malformed"));
            }
            Ok(child)
        })?;
        release_page(store, child, Some(level - 1), total, next, depth + 1)?;
    }
    Ok(())
}

fn release_leaf<P: ByteSource>(
    page: P,
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

fn require_release_leaf_identity<P: ByteSource>(
    page: P,
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<()> {
    if !page.equals(0, &PAGE_MAGIC)
        || page.byte(4) != Some(LEAF_TYPE)
        || page.byte(5) != Some(0)
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

fn require_release_leaf_layout<P: ByteSource>(page: P, data_len: u64) -> Result<()> {
    if u16_le(page, 16) != 1
        || u16_le(page, 20) as usize != LEAF_DATA + data_len as usize
        || u16_le(page, 22) as usize != PAGE_SIZE
        || u32_le(page, 24) != MEMBERSHIP_KIND
        || data_len == 0
        || data_len > LEAF_BYTES as u64
        || data_len % 8 != 0
        || !page.all_zero(42, 6)
    {
        return Err(Error::Corrupt("membership blob leaf layout is malformed"));
    }
    Ok(())
}

fn require_release_leaf_coverage<P: ByteSource>(
    page: P,
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
    let page_number = store.allocate()?;
    let target_txn = store.target_txn();
    store.update_page(page_number, |page| {
        page.fill(0);
        page.write(0, &PAGE_MAGIC)?;
        page.set_byte(4, LEAF_TYPE)?;
        page.put_u16(6, HEADER_SIZE as u16)?;
        page.put_u64(8, target_txn)?;
        page.put_u16(16, 1)?;
        page.put_u16(20, (LEAF_DATA + count as usize * 8) as u16)?;
        page.put_u16(22, PAGE_SIZE as u16)?;
        page.put_u32(24, MEMBERSHIP_KIND)?;
        page.put_u64(32, u64::from(offset_words) * 8)?;
        page.put_u16(40, (count * 8) as u16)
    })?;
    const WORD_CHUNK: usize = 64;
    let mut values = [0u64; WORD_CHUNK];
    let mut written = 0u32;
    while written < count {
        let chunk = (count - written).min(WORD_CHUNK as u32) as usize;
        words.read_words(store, offset_words + written, &mut values[..chunk])?;
        store.update_page(page_number, |page| {
            for (index, value) in values[..chunk].iter().enumerate() {
                page.put_u64(LEAF_DATA + (written as usize + index) * 8, *value)?;
            }
            Ok(())
        })?;
        written += chunk as u32;
    }
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
    let target_txn = store.target_txn();
    store.update_page(page_number, |page| {
        let mut builder = Builder::new(
            page,
            BRANCH_TYPE,
            target_txn,
            child_level + 1,
            MEMBERSHIP_KIND,
        );
        for node in &level.nodes[..level.len] {
            let mut record = [0; 16];
            put_u64(&mut record, 0, node.offset);
            put_u32(&mut record, 8, node.page);
            builder.push(&record)?;
        }
        builder.finish()
    })?;
    let result = Node {
        offset: level.nodes[0].offset,
        page: page_number,
        level: child_level + 1,
    };
    *level = EMPTY_LEVEL;
    Ok(result)
}
