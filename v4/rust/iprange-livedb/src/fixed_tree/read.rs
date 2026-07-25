//! Bounded fixed-tree record searches.

use crate::contract::{MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page;

use super::page::{branch_child, lower_bound, parse};
use super::{Codec, Store};

const MAX_LEAF_SIZE: usize = 64;
const MAX_PATH: usize = MAX_TREE_LEVEL as usize;

/// One copied fixed-width leaf record.
pub(crate) struct LeafBuf {
    bytes: [u8; MAX_LEAF_SIZE],
    len: usize,
}

impl LeafBuf {
    pub(crate) fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    index: usize,
    item_count: usize,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    index: 0,
    item_count: 0,
    level: 0,
};

pub(crate) fn predecessor<C: Codec, S: Store>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<Option<LeafBuf>> {
    if root == 0 {
        return Ok(None);
    }
    let (_, _, page, header) = descend::<C, S>(store, root, key)?;
    let (index, exists) = lower_bound::<C>(&page, &header, key, true)?;
    if exists {
        return copy_leaf::<C>(&page, &header, index).map(Some);
    }
    let Some(index) = index.checked_sub(1) else {
        return Ok(None);
    };
    copy_leaf::<C>(&page, &header, index).map(Some)
}

pub(crate) fn at_or_after<C: Codec, S: Store>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<Option<LeafBuf>> {
    if root == 0 {
        return Ok(None);
    }
    let (path, mut depth, page, header) = descend::<C, S>(store, root, key)?;
    let (index, _) = lower_bound::<C>(&page, &header, key, true)?;
    if index < header.item_count {
        return copy_leaf::<C>(&page, &header, index).map(Some);
    }

    while depth > 0 {
        depth -= 1;
        let mut frame = path[depth];
        if frame.index + 1 >= frame.item_count {
            continue;
        }
        frame.index += 1;
        let mut branch = [0; PAGE_SIZE];
        store.read(frame.page_number, &mut branch)?;
        let branch_header = parse::<C>(&branch, store.target_txn(), Some(frame.level))?;
        let child = branch_child::<C>(&branch, &branch_header, frame.index, store.page_limit())?;
        let (page, header) = descend_left::<C, S>(store, child, frame.level - 1)?;
        return copy_leaf::<C>(&page, &header, 0).map(Some);
    }
    Ok(None)
}

fn descend<C: Codec, S: Store>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<(
    [Frame; MAX_PATH],
    usize,
    [u8; PAGE_SIZE],
    slotted_page::Header,
)> {
    let mut path = [EMPTY_FRAME; MAX_PATH];
    let mut depth = 0;
    let mut page_number = root;
    let mut expected = None;
    loop {
        let mut page = [0; PAGE_SIZE];
        store.read(page_number, &mut page)?;
        let header = parse::<C>(&page, store.target_txn(), expected)?;
        if header.level == 0 {
            return Ok((path, depth, page, header));
        }
        let (index, _) = lower_bound::<C>(&page, &header, key, false)?;
        let frame = path
            .get_mut(depth)
            .ok_or(Error::Corrupt("B+tree exceeds its maximum height"))?;
        *frame = Frame {
            page_number,
            index,
            item_count: header.item_count,
            level: header.level,
        };
        depth += 1;
        page_number = branch_child::<C>(&page, &header, index, store.page_limit())?;
        expected = Some(header.level - 1);
    }
}

fn descend_left<C: Codec, S: Store>(
    store: &S,
    mut page_number: u32,
    mut level: u16,
) -> Result<([u8; PAGE_SIZE], slotted_page::Header)> {
    loop {
        let mut page = [0; PAGE_SIZE];
        store.read(page_number, &mut page)?;
        let header = parse::<C>(&page, store.target_txn(), Some(level))?;
        if level == 0 {
            return Ok((page, header));
        }
        page_number = branch_child::<C>(&page, &header, 0, store.page_limit())?;
        level -= 1;
    }
}

fn copy_leaf<C: Codec>(
    page: &[u8; PAGE_SIZE],
    header: &slotted_page::Header,
    index: usize,
) -> Result<LeafBuf> {
    let source = slotted_page::cell(page, header, index, C::LEAF_SIZE)?;
    if source.len() > MAX_LEAF_SIZE {
        return Err(Error::Unsupported("fixed B+tree leaf is too large"));
    }
    C::validate_leaf(source)?;
    let mut output = LeafBuf {
        bytes: [0; MAX_LEAF_SIZE],
        len: source.len(),
    };
    output.bytes[..source.len()].copy_from_slice(source);
    Ok(output)
}
