//! Fixed-memory construction of immutable membership blobs.

use crate::blob_tree;
use crate::contract::{MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiringStore, Store};
use crate::mapping::ByteSource;
use crate::page_io::PageSink;
use crate::slotted_page::{self, put_u32, put_u64, Builder, HEADER_SIZE};

use super::Words;

const LEAF_WORDS: usize = blob_tree::LEAF_CAPACITY / 8;
const BRANCH_ITEMS: usize = (PAGE_SIZE - HEADER_SIZE) / (blob_tree::BRANCH_RECORD_SIZE + 2);
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
    let (level, born_txn) = store.inspect_page(page_number, |page| {
        Ok((
            crate::page_header::level(page),
            crate::page_header::born_txn(page),
        ))
    })?;
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
    let header = store.inspect_page(page_number, |page| {
        slotted_page::parse(
            page,
            store.target_txn(),
            blob_tree::BRANCH_TYPE,
            blob_tree::MEMBERSHIP_KIND,
            expected_level,
        )
    })?;
    for index in 0..header.item_count {
        let child = store.inspect_page(page_number, |page| {
            let cell = slotted_page::cell(page, &header, index, blob_tree::BRANCH_RECORD_SIZE)?;
            let record = blob_tree::decode_branch_record(cell)?;
            if record.offset != *next
                || record.child < 2
                || u64::from(record.child) >= store.page_limit()
            {
                return Err(Error::Corrupt("membership blob branch record is malformed"));
            }
            Ok(record.child)
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
    blob_tree::require_leaf_identity(page, selected_txn, expected_level)?;
    *next = blob_tree::leaf_geometry(page, expected_level, *next, total)?.end;
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
        blob_tree::initialize_leaf(
            page,
            target_txn,
            u64::from(offset_words) * 8,
            count as usize * 8,
        )
    })?;
    const WORD_CHUNK: usize = 64;
    let mut values = [0u64; WORD_CHUNK];
    let mut written = 0u32;
    while written < count {
        let chunk = (count - written).min(WORD_CHUNK as u32) as usize;
        words.read_words(store, offset_words + written, &mut values[..chunk])?;
        store.update_page(page_number, |page| {
            for (index, value) in values[..chunk].iter().enumerate() {
                page.put_u64(
                    blob_tree::LEAF_DATA + (written as usize + index) * 8,
                    *value,
                )?;
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
            blob_tree::BRANCH_TYPE,
            target_txn,
            child_level + 1,
            blob_tree::MEMBERSHIP_KIND,
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
