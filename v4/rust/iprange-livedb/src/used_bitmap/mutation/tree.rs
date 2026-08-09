//! Sparse-bitmap structural edits.

use crate::bitmap_page;
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::page_io::PageEdit;

use super::super::page::{
    coverage_intersects, initialize, initialize_summary, parse, set_branch_child, stamp_leaf,
    Header,
};
use super::super::{
    add_child_base, child_index, coverage, leaf_word_index, subtree_has_candidate, Kind,
};
use super::{EditPath, Frame};

pub(super) fn propagate<S: Store>(
    store: &mut S,
    path: &[Frame],
    mut depth: usize,
    mut child_page: u32,
    mut child_base: u64,
    limit: u64,
    kind: Kind,
) -> Result<()> {
    while depth > 0 {
        depth -= 1;
        let frame = path[depth];
        let parent_base = parent_base(frame)?;
        let candidate = subtree_has_candidate(store, child_page, kind, child_base, limit)?;
        let target_txn = store.target_txn();
        store.update_page(frame.page_number, |page| {
            let header = parse(
                page.view(),
                target_txn,
                kind,
                Some(frame.level),
                parent_base,
                limit,
            )?;
            let child = bitmap_page::branch_child(page.view(), frame.child_index)?;
            set_branch_child(page, &header, frame.child_index, child, candidate)
        })?;
        child_page = frame.page_number;
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
        let parent_base = parent_base(frame)?;
        let span = coverage(frame.level - 1)?;
        let candidate = coverage_intersects(frame.child_base, span, kind.first_candidate(), limit);
        let target_txn = store.target_txn();
        let remaining = store.update_page(frame.page_number, |page| {
            let header = parse(
                page.view(),
                target_txn,
                kind,
                Some(frame.level),
                parent_base,
                limit,
            )?;
            set_branch_child(page, &header, frame.child_index, 0, candidate)?;
            Ok(header.item_count - 1)
        })?;
        if remaining != 0 {
            propagate(
                store,
                &path.frames,
                depth,
                frame.page_number,
                parent_base,
                limit,
                kind,
            )?;
            return Ok(());
        }
        store.discard_private(frame.page_number)?;
    }
    *root = 0;
    Ok(())
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
    let target_txn = store.target_txn();
    let mut level = store.inspect_page(*root, |page| {
        Ok(parse(page, target_txn, kind, None, 0, limit)?.level)
    })?;
    if level > required {
        return Err(Error::Corrupt("used bitmap root level is too high"));
    }
    while level < required {
        let candidate = subtree_has_candidate(store, *root, kind, 0, limit)?;
        let parent = store.allocate()?;
        let child = *root;
        let next_level = level + 1;
        store.update_page(parent, |page| {
            initialize(page, target_txn, next_level, kind);
            initialize_summary(page, 0, coverage(level)?, kind.first_candidate(), limit)?;
            set_branch_child(
                page,
                &Header {
                    level: next_level,
                    item_count: 0,
                },
                0,
                child,
                candidate,
            )
        })?;
        *root = parent;
        level = next_level;
    }
    Ok(())
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
        let page_number = store.allocate()?;
        let txn = store.target_txn();
        store.update_page(page_number, |page| {
            initialize(page, txn, 0, kind);
            bitmap_page::set_leaf_word(page, leaf_word_index(bit), 1u64 << (u64::from(bit) % 64))?;
            stamp_leaf(page, 1)
        })?;
        return Ok(page_number);
    }

    let span = coverage(level - 1)?;
    let index = child_index(bit, level)?;
    let child_base = add_child_base(base, span, index)?;
    let child = new_subtree(store, kind, level - 1, child_base, limit, bit)?;
    let candidate = subtree_has_candidate(store, child, kind, child_base, limit)?;
    let page_number = store.allocate()?;
    let txn = store.target_txn();
    store.update_page(page_number, |page| {
        initialize(page, txn, level, kind);
        initialize_summary(page, base, span, kind.first_candidate(), limit)?;
        set_branch_child(
            page,
            &Header {
                level,
                item_count: 0,
            },
            index,
            child,
            candidate,
        )
    })?;
    Ok(page_number)
}
