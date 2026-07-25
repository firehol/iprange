//! Copy-on-write sparse-bitmap mutation.

use crate::contract::{u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, Store};
use crate::slotted_page::{put_u64, HEADER_SIZE};

use super::page::{branch_child, set_branch_child, set_pointer, stamp_leaf, Header, MAX_LEVEL};
use super::search::{contains, find_lowest};
use super::{
    add_child_base, child_index, coverage, leaf_word_index, require_bit, required_level,
    subtree_has_candidate, touch, Kind,
};

mod tree;

use tree::{grow_root, new_subtree, propagate, remove_empty_path};

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    child_index: usize,
    level: u16,
    child_base: u64,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    child_index: 0,
    level: 0,
    child_base: 0,
};

struct EditPath {
    frames: [Frame; (MAX_LEVEL + 1) as usize],
    depth: usize,
}

impl EditPath {
    fn new() -> Self {
        Self {
            frames: [EMPTY_FRAME; (MAX_LEVEL + 1) as usize],
            depth: 0,
        }
    }

    fn push(&mut self, frame: Frame) -> Result<()> {
        let slot = self
            .frames
            .get_mut(self.depth)
            .ok_or(Error::Corrupt("used bitmap path exceeds maximum height"))?;
        *slot = frame;
        self.depth += 1;
        Ok(())
    }
}

struct Cursor {
    page_number: u32,
    page: [u8; PAGE_SIZE],
    header: Header,
    base: u64,
}

struct BranchStep {
    index: usize,
    child: u32,
    child_base: u64,
}

pub(crate) fn take_lowest<S: Store>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    kind: Kind,
    retired: &mut RetiredPages,
) -> Result<Option<u32>> {
    let Some(bit) = find_lowest(store, *root, limit, kind)? else {
        return Ok(None);
    };
    set(store, root, limit, kind, bit, retired)?;
    Ok(Some(bit))
}

pub(crate) fn set<S: Store>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    kind: Kind,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<()> {
    require_bit(limit, kind, bit)?;
    let level = required_level(limit)?;
    if initialize_root(store, root, kind, level, limit, bit)? {
        return Ok(());
    }
    grow_root(store, root, kind, level, limit)?;
    let cursor = start(store, *root, kind, level, limit, retired)?;
    *root = cursor.page_number;
    let mut path = EditPath::new();
    match descend_for_set(store, cursor, &mut path, limit, kind, bit, retired)? {
        None => Ok(()),
        Some(cursor) => set_leaf(store, cursor, &path, limit, kind, bit),
    }
}

pub(crate) fn clear<S: Store>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    kind: Kind,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<bool> {
    if !clear_is_needed(store, *root, limit, kind, bit)? {
        return Ok(false);
    }
    let level = required_level(limit)?;
    let mut cursor = start(store, *root, kind, level, limit, retired)?;
    *root = cursor.page_number;
    let mut path = EditPath::new();
    cursor = descend_existing(store, cursor, &mut path, limit, kind, bit, retired)?;
    if clear_leaf(store, cursor, &path, limit, kind, bit)? {
        remove_empty_path(store, root, &path, limit, kind)?;
    }
    Ok(true)
}

fn initialize_root<S: Store>(
    store: &mut S,
    root: &mut u32,
    kind: Kind,
    level: u16,
    limit: u64,
    bit: u32,
) -> Result<bool> {
    if *root != 0 {
        return Ok(false);
    }
    *root = new_subtree(store, kind, level, 0, limit, bit)?;
    Ok(true)
}

fn clear_is_needed<S: Store>(
    store: &S,
    root: u32,
    limit: u64,
    kind: Kind,
    bit: u32,
) -> Result<bool> {
    require_bit(limit, kind, bit)?;
    if !contains(store, root, limit, kind, bit)? {
        return Ok(false);
    }
    if root == 0 {
        return Err(Error::Corrupt("used bitmap root disappeared"));
    }
    Ok(true)
}

fn start<S: Store>(
    store: &mut S,
    root: u32,
    kind: Kind,
    level: u16,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<Cursor> {
    let (page_number, page, header) = touch(store, root, kind, level, 0, limit, retired)?;
    Ok(Cursor {
        page_number,
        page,
        header,
        base: 0,
    })
}

#[allow(clippy::too_many_arguments)]
fn descend_for_set<S: Store>(
    store: &mut S,
    mut cursor: Cursor,
    path: &mut EditPath,
    limit: u64,
    kind: Kind,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<Option<Cursor>> {
    while cursor.header.level > 0 {
        let step = branch_step(store, &cursor, bit)?;
        path.push(frame(&cursor, &step))?;
        if step.child == 0 {
            insert_missing(store, &mut cursor, path, step, limit, kind, bit)?;
            return Ok(None);
        }
        cursor = touch_child(store, &mut cursor, step, limit, kind, retired)?;
    }
    Ok(Some(cursor))
}

#[allow(clippy::too_many_arguments)]
fn descend_existing<S: Store>(
    store: &mut S,
    mut cursor: Cursor,
    path: &mut EditPath,
    limit: u64,
    kind: Kind,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<Cursor> {
    while cursor.header.level > 0 {
        let step = branch_step(store, &cursor, bit)?;
        if step.child == 0 {
            return Err(Error::Corrupt("used bitmap bit path disappeared"));
        }
        path.push(frame(&cursor, &step))?;
        cursor = touch_child(store, &mut cursor, step, limit, kind, retired)?;
    }
    Ok(cursor)
}

fn branch_step<S: Store>(store: &S, cursor: &Cursor, bit: u32) -> Result<BranchStep> {
    let index = child_index(bit, cursor.header.level)?;
    let span = coverage(cursor.header.level - 1)?;
    Ok(BranchStep {
        index,
        child: branch_child(&cursor.page, &cursor.header, index, store.page_limit())?,
        child_base: add_child_base(cursor.base, span, index)?,
    })
}

fn frame(cursor: &Cursor, step: &BranchStep) -> Frame {
    Frame {
        page_number: cursor.page_number,
        child_index: step.index,
        level: cursor.header.level,
        child_base: step.child_base,
    }
}

fn touch_child<S: Store>(
    store: &mut S,
    parent: &mut Cursor,
    step: BranchStep,
    limit: u64,
    kind: Kind,
    retired: &mut RetiredPages,
) -> Result<Cursor> {
    let (page_number, page, header) = touch(
        store,
        step.child,
        kind,
        parent.header.level - 1,
        step.child_base,
        limit,
        retired,
    )?;
    if page_number != step.child {
        set_pointer(&mut parent.page, &parent.header, step.index, page_number)?;
        store.write(parent.page_number, &parent.page)?;
    }
    Ok(Cursor {
        page_number,
        page,
        header,
        base: step.child_base,
    })
}

fn insert_missing<S: Store>(
    store: &mut S,
    cursor: &mut Cursor,
    path: &EditPath,
    step: BranchStep,
    limit: u64,
    kind: Kind,
    bit: u32,
) -> Result<()> {
    let child = new_subtree(
        store,
        kind,
        cursor.header.level - 1,
        step.child_base,
        limit,
        bit,
    )?;
    let candidate = subtree_has_candidate(store, child, kind, step.child_base, limit)?;
    set_branch_child(
        &mut cursor.page,
        &cursor.header,
        step.index,
        child,
        candidate,
    )?;
    store.write(cursor.page_number, &cursor.page)?;
    propagate(
        store,
        &path.frames,
        path.depth - 1,
        cursor.page,
        cursor.base,
        limit,
        kind,
    )
}

fn set_leaf<S: Store>(
    store: &mut S,
    mut cursor: Cursor,
    path: &EditPath,
    limit: u64,
    kind: Kind,
    bit: u32,
) -> Result<()> {
    let at = HEADER_SIZE + leaf_word_index(bit) * 8;
    let word = u64_le(&cursor.page, at);
    let mask = 1u64 << (u64::from(bit) % 64);
    if word & mask != 0 {
        return Err(Error::Corrupt("used bitmap bit is already set"));
    }
    put_u64(&mut cursor.page, at, word | mask);
    stamp_leaf(
        &mut cursor.page,
        cursor.header.item_count + usize::from(word == 0),
    )?;
    store.write(cursor.page_number, &cursor.page)?;
    propagate(
        store,
        &path.frames,
        path.depth,
        cursor.page,
        cursor.base,
        limit,
        kind,
    )
}

fn clear_leaf<S: Store>(
    store: &mut S,
    mut cursor: Cursor,
    path: &EditPath,
    limit: u64,
    kind: Kind,
    bit: u32,
) -> Result<bool> {
    let at = HEADER_SIZE + leaf_word_index(bit) * 8;
    let word = u64_le(&cursor.page, at);
    let mask = 1u64 << (u64::from(bit) % 64);
    if word & mask == 0 {
        return Err(Error::Corrupt("used bitmap bit disappeared"));
    }
    let next = word & !mask;
    put_u64(&mut cursor.page, at, next);
    let count = cursor.header.item_count - usize::from(next == 0);
    if count == 0 {
        store.discard_private(cursor.page_number)?;
        return Ok(true);
    }
    stamp_leaf(&mut cursor.page, count)?;
    store.write(cursor.page_number, &cursor.page)?;
    propagate(
        store,
        &path.frames,
        path.depth,
        cursor.page,
        cursor.base,
        limit,
        kind,
    )?;
    Ok(false)
}
