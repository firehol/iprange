//! Sparse-bitmap structural edits.

use crate::contract::{u16_le, u32_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::slotted_page::HEADER_SIZE;

use super::super::page::{
    coverage_intersects, initialize_summary, new_page, page_has_candidate, set_branch_child,
    stamp_leaf, Header, BRANCH_END, BRANCH_TYPE, LEAF_END, LEAF_TYPE,
};
use super::super::{
    add_child_base, child_index, coverage, leaf_word_index, subtree_has_candidate, Kind,
};
use super::{EditPath, Frame};

pub(super) fn propagate<S: Store>(
    store: &mut S,
    path: &[Frame],
    mut depth: usize,
    mut child: [u8; PAGE_SIZE],
    mut child_base: u64,
    limit: u64,
    kind: Kind,
) -> Result<()> {
    while depth > 0 {
        depth -= 1;
        let frame = path[depth];
        let parent_base = parent_base(frame)?;
        let mut parent = [0; PAGE_SIZE];
        store.read(frame.page_number, &mut parent)?;
        let header = super::super::page::parse(
            &parent,
            store.target_txn(),
            kind,
            Some(frame.level),
            parent_base,
            limit,
        )?;
        let candidate = page_has_candidate(&child, child_base, limit, kind)?;
        let child_page = u32_le(&parent, HEADER_SIZE + 32 + frame.child_index * 4);
        set_branch_child(
            &mut parent,
            &header,
            frame.child_index,
            child_page,
            candidate,
        )?;
        store.write(frame.page_number, &parent)?;
        child = parent;
        child_base = parent_base;
    }
    Ok(())
}

pub(super) fn remove_empty_path<S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &EditPath,
    limit: u64,
    kind: Kind,
) -> Result<()> {
    let mut depth = path.depth;
    while depth > 0 {
        depth -= 1;
        let frame = path.frames[depth];
        let (page, parent_base) = clear_child(store, frame, limit, kind)?;
        if retain_parent(store, path, depth, frame, page, parent_base, limit, kind)? {
            return Ok(());
        }
        store.discard_private(frame.page_number)?;
    }
    *root = 0;
    Ok(())
}

fn clear_child<S: Store>(
    store: &S,
    frame: Frame,
    limit: u64,
    kind: Kind,
) -> Result<([u8; PAGE_SIZE], u64)> {
    let parent_base = parent_base(frame)?;
    let mut page = [0; PAGE_SIZE];
    store.read(frame.page_number, &mut page)?;
    let header = super::super::page::parse(
        &page,
        store.target_txn(),
        kind,
        Some(frame.level),
        parent_base,
        limit,
    )?;
    let span = coverage(frame.level - 1)?;
    let candidate = coverage_intersects(frame.child_base, span, kind.first(), limit);
    set_branch_child(&mut page, &header, frame.child_index, 0, candidate)?;
    Ok((page, parent_base))
}

#[allow(clippy::too_many_arguments)]
fn retain_parent<S: Store>(
    store: &mut S,
    path: &EditPath,
    depth: usize,
    frame: Frame,
    page: [u8; PAGE_SIZE],
    parent_base: u64,
    limit: u64,
    kind: Kind,
) -> Result<bool> {
    if u16_le(&page, 16) == 0 {
        return Ok(false);
    }
    store.write(frame.page_number, &page)?;
    propagate(store, &path.frames, depth, page, parent_base, limit, kind)?;
    Ok(true)
}

fn parent_base(frame: Frame) -> Result<u64> {
    let offset = coverage(frame.level - 1)?
        .checked_mul(frame.child_index as u64)
        .ok_or(Error::ArithmeticOverflow("used bitmap coverage"))?;
    frame
        .child_base
        .checked_sub(offset)
        .ok_or(Error::Corrupt("used bitmap child base underflows"))
}

pub(super) fn grow_root<S: Store>(
    store: &mut S,
    root: &mut u32,
    kind: Kind,
    required: u16,
    limit: u64,
) -> Result<()> {
    let mut page = [0; PAGE_SIZE];
    store.read(*root, &mut page)?;
    let mut level = u16_le(&page, 18);
    super::super::page::parse(&page, store.target_txn(), kind, Some(level), 0, limit)?;
    if level > required {
        return Err(Error::Corrupt("used bitmap root level is too high"));
    }
    while level < required {
        let (parent, next) = grow_one(store, *root, &page, level, kind, limit)?;
        *root = parent;
        page = next;
        level += 1;
    }
    Ok(())
}

fn grow_one<S: Store>(
    store: &mut S,
    root: u32,
    page: &[u8; PAGE_SIZE],
    level: u16,
    kind: Kind,
    limit: u64,
) -> Result<(u32, [u8; PAGE_SIZE])> {
    let candidate = page_has_candidate(page, 0, limit, kind)?;
    let parent = store.allocate()?;
    let mut next = new_page(BRANCH_TYPE, store.target_txn(), level + 1, kind, BRANCH_END);
    initialize_summary(&mut next, 0, coverage(level)?, kind.first(), limit);
    let header = Header {
        level: level + 1,
        item_count: 0,
    };
    set_branch_child(&mut next, &header, 0, root, candidate)?;
    store.write(parent, &next)?;
    Ok((parent, next))
}

pub(super) fn new_subtree<S: Store>(
    store: &mut S,
    kind: Kind,
    level: u16,
    base: u64,
    limit: u64,
    bit: u32,
) -> Result<u32> {
    if level == 0 {
        return new_leaf(store, kind, bit);
    }
    let span = coverage(level - 1)?;
    let index = child_index(bit, level)?;
    let child_base = add_child_base(base, span, index)?;
    let child = new_subtree(store, kind, level - 1, child_base, limit, bit)?;
    new_branch(store, kind, level, base, limit, index, child, child_base)
}

fn new_leaf<S: Store>(store: &mut S, kind: Kind, bit: u32) -> Result<u32> {
    let page_number = store.allocate()?;
    let mut page = new_page(LEAF_TYPE, store.target_txn(), 0, kind, LEAF_END);
    crate::slotted_page::put_u64(
        &mut page,
        HEADER_SIZE + leaf_word_index(bit) * 8,
        1u64 << (u64::from(bit) % 64),
    );
    stamp_leaf(&mut page, 1)?;
    store.write(page_number, &page)?;
    Ok(page_number)
}

#[allow(clippy::too_many_arguments)]
fn new_branch<S: Store>(
    store: &mut S,
    kind: Kind,
    level: u16,
    base: u64,
    limit: u64,
    index: usize,
    child: u32,
    child_base: u64,
) -> Result<u32> {
    let page_number = store.allocate()?;
    let mut page = new_page(BRANCH_TYPE, store.target_txn(), level, kind, BRANCH_END);
    initialize_summary(&mut page, base, coverage(level - 1)?, kind.first(), limit);
    let header = Header {
        level,
        item_count: 0,
    };
    let candidate = subtree_has_candidate(store, child, kind, child_base, limit)?;
    set_branch_child(&mut page, &header, index, child, candidate)?;
    store.write(page_number, &page)?;
    Ok(page_number)
}
