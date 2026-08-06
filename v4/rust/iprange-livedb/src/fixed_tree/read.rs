//! Bounded mapped-page tree searches.

use crate::contract::MAX_TREE_LEVEL;
use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::slotted_page;

use super::page::{branch_child, codec_cell, key_at, lower_bound, parse};
use super::{Codec, Store};

const MAX_PATH: usize = MAX_TREE_LEVEL as usize;
const MAX_COPIED_LEAF: usize = 512;

pub(crate) struct LeafBuf {
    bytes: [u8; MAX_COPIED_LEAF],
    len: usize,
}

#[derive(Clone, Copy)]
pub(crate) struct LeafLocation {
    pub(super) page_number: u32,
    pub(super) header: slotted_page::Header,
    pub(super) index: usize,
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
    let Some(location) = predecessor_location::<C, S>(store, root, key)? else {
        return Ok(None);
    };
    inspect_leaf::<C, S, _, _>(store, location, |cell| {
        if cell.len() > C::MAX_LEAF_SIZE || cell.len() > MAX_COPIED_LEAF {
            return Err(Error::Unsupported("B+tree leaf is too large to copy"));
        }
        let mut output = LeafBuf {
            bytes: [0; MAX_COPIED_LEAF],
            len: cell.len(),
        };
        if !cell.copy_range_to(0, &mut output.bytes[..cell.len()]) {
            return Err(Error::Corrupt("B+tree leaf source changed"));
        }
        Ok(Some(output))
    })
}

pub(crate) fn predecessor_location<C: Codec, S: Store>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<Option<LeafLocation>> {
    let Some((_, _, page_number, header)) = descend::<C, S>(store, root, key)? else {
        return Ok(None);
    };
    store.inspect_page(page_number, |page| {
        let (index, exists) = lower_bound::<C, _>(page, &header, key, true)?;
        let index = if exists {
            index
        } else if let Some(index) = index.checked_sub(1) {
            index
        } else {
            return Ok(None);
        };
        Ok(Some(LeafLocation {
            page_number,
            header,
            index,
        }))
    })
}

pub(crate) fn inspect_leaf<'a, C: Codec, S: Store, T, F>(
    store: &'a S,
    location: LeafLocation,
    inspect: F,
) -> Result<T>
where
    F: FnOnce(crate::mapping::ByteRange<S::ReadPage<'a>>) -> Result<T>,
{
    store.inspect_page(location.page_number, |page| {
        let header = parse::<C, _>(page, store.target_txn(), Some(0))?;
        if header.item_count != location.header.item_count {
            return Err(Error::Corrupt("B+tree leaf changed during inspection"));
        }
        let cell = codec_cell::<C, _>(page, &header, location.index)?;
        C::validate_leaf(cell)?;
        inspect(cell)
    })
}

pub(crate) fn at_or_after<C: Codec, S: Store>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<Option<LeafBuf>> {
    let Some((path, depth, page_number, header)) = descend::<C, S>(store, root, key)? else {
        return Ok(None);
    };
    let found = store.inspect_page(page_number, |page| {
        let (index, _) = lower_bound::<C, _>(page, &header, key, true)?;
        if index < header.item_count {
            copy_leaf::<C, _>(page, &header, index).map(Some)
        } else {
            Ok(None)
        }
    })?;
    if found.is_some() {
        return Ok(found);
    }
    next_leaf::<C, S>(store, &path, depth)
}

pub(super) fn contains<C: Codec, S: Store>(store: &S, root: u32, key: C::Key) -> Result<bool> {
    let Some((_, _, page_number, header)) = descend::<C, S>(store, root, key)? else {
        return Ok(false);
    };
    store.inspect_page(page_number, |page| {
        let (index, exists) = lower_bound::<C, _>(page, &header, key, true)?;
        if !exists {
            return Ok(false);
        }
        Ok(key_at::<C, _>(page, &header, index)? == key)
    })
}

fn next_leaf<C: Codec, S: Store>(
    store: &S,
    path: &[Frame; MAX_PATH],
    mut depth: usize,
) -> Result<Option<LeafBuf>> {
    while depth > 0 {
        depth -= 1;
        let frame = path[depth];
        if frame.index + 1 >= frame.item_count {
            continue;
        }
        return first_in_right_subtree::<C, S>(store, frame).map(Some);
    }
    Ok(None)
}

fn first_in_right_subtree<C: Codec, S: Store>(store: &S, frame: Frame) -> Result<LeafBuf> {
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    let child = store.inspect_page(frame.page_number, |page| {
        let header = parse::<C, _>(page, target_txn, Some(frame.level))?;
        branch_child::<C, _>(page, &header, frame.index + 1, page_limit)
    })?;
    let (page_number, header) = descend_left::<C, S>(store, child, frame.level - 1)?;
    store.inspect_page(page_number, |page| copy_leaf::<C, _>(page, &header, 0))
}

type Descent = ([Frame; MAX_PATH], usize, u32, slotted_page::Header);

fn descend<C: Codec, S: Store>(store: &S, root: u32, key: C::Key) -> Result<Option<Descent>> {
    if root == 0 {
        return Ok(None);
    }
    let mut path = [EMPTY_FRAME; MAX_PATH];
    let mut depth = 0;
    let mut page_number = root;
    let mut expected = None;
    loop {
        let target_txn = store.target_txn();
        let page_limit = store.page_limit();
        let (header, selected) = store.inspect_page(page_number, |page| {
            let header = parse::<C, _>(page, target_txn, expected)?;
            let selected = if header.level == 0 {
                None
            } else {
                let (index, _) = lower_bound::<C, _>(page, &header, key, false)?;
                let child = branch_child::<C, _>(page, &header, index, page_limit)?;
                Some((index, child))
            };
            Ok((header, selected))
        })?;
        let Some((index, child)) = selected else {
            return Ok(Some((path, depth, page_number, header)));
        };
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
        page_number = child;
        expected = Some(header.level - 1);
    }
}

fn descend_left<C: Codec, S: Store>(
    store: &S,
    mut page_number: u32,
    mut level: u16,
) -> Result<(u32, slotted_page::Header)> {
    loop {
        let target_txn = store.target_txn();
        let page_limit = store.page_limit();
        let (header, child) = store.inspect_page(page_number, |page| {
            let header = parse::<C, _>(page, target_txn, Some(level))?;
            let child = if level == 0 {
                None
            } else {
                Some(branch_child::<C, _>(page, &header, 0, page_limit)?)
            };
            Ok((header, child))
        })?;
        let Some(child) = child else {
            return Ok((page_number, header));
        };
        page_number = child;
        level -= 1;
    }
}

pub(super) fn copy_leaf<C: Codec, S: ByteSource>(
    page: S,
    header: &slotted_page::Header,
    index: usize,
) -> Result<LeafBuf> {
    let source = codec_cell::<C, _>(page, header, index)?;
    if source.len() > C::MAX_LEAF_SIZE || source.len() > MAX_COPIED_LEAF {
        return Err(Error::Unsupported("B+tree leaf is too large to copy"));
    }
    C::validate_leaf(source)?;
    let mut output = LeafBuf {
        bytes: [0; MAX_COPIED_LEAF],
        len: source.len(),
    };
    if !source.copy_range_to(0, &mut output.bytes[..source.len()]) {
        return Err(Error::Corrupt("B+tree leaf source changed"));
    }
    Ok(output)
}
