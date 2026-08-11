//! Copy-on-write sparse-bitmap mutation.

use crate::bitmap_page::{leaf_word, set_leaf_word, MAX_LEVEL};
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, Store};
use crate::page_io::PageEdit;

use super::page::{branch_child, set_branch_child, set_pointer, stamp_leaf, Header};
use super::search::{contains, find_lowest};
use super::{
    add_child_base, child_index, coverage, leaf_word_index, require_bit, required_level,
    subtree_has_candidate, touch, Kind,
};

mod tree;

use tree::{grow_root, new_subtree, propagate, propagate_known, remove_empty_path};

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
    header: Header,
    base: u64,
}

struct BranchStep {
    index: usize,
    child: u32,
    child_base: u64,
}

#[derive(Clone, Copy)]
struct SetSpec {
    limit: u64,
    kind: Kind,
    bit: u32,
    candidate_hint: Option<bool>,
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
    set_inner(store, root, limit, kind, bit, retired, None)
}

pub(super) fn set_dense_append<S: Store>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    kind: Kind,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<()> {
    if u64::from(bit) + 1 != limit {
        return Err(Error::Corrupt(
            "dense bitmap append does not extend its limit",
        ));
    }
    set_inner(store, root, limit, kind, bit, retired, Some(false))
}

fn set_inner<S: Store>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    kind: Kind,
    bit: u32,
    retired: &mut RetiredPages,
    candidate_hint: Option<bool>,
) -> Result<()> {
    require_bit(limit, kind, bit)?;
    let spec = SetSpec {
        limit,
        kind,
        bit,
        candidate_hint,
    };
    let level = required_level(limit)?;
    if *root == 0 {
        *root = new_subtree(store, kind, level, 0, limit, bit)?;
        return Ok(());
    }
    grow_root(store, root, kind, level, limit)?;
    let mut cursor = start(store, *root, kind, level, limit, retired)?;
    *root = cursor.page_number;
    let mut path = EditPath::new();
    while cursor.header.level > 0 {
        let step = branch_step(store, &cursor, bit)?;
        path.push(frame(&cursor, &step))?;
        if step.child == 0 {
            insert_missing(store, &cursor, &path, step, spec)?;
            return Ok(());
        }
        cursor = touch_child(store, &cursor, step, limit, kind, retired)?;
    }
    set_leaf(store, &cursor, &path, spec)
}

pub(crate) fn clear<S: Store>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    kind: Kind,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<bool> {
    require_bit(limit, kind, bit)?;
    if !contains(store, *root, limit, kind, bit)? {
        return Ok(false);
    }
    if *root == 0 {
        return Err(Error::Corrupt("used bitmap root disappeared"));
    }
    let level = required_level(limit)?;
    let mut cursor = start(store, *root, kind, level, limit, retired)?;
    *root = cursor.page_number;
    let mut path = EditPath::new();
    while cursor.header.level > 0 {
        let step = branch_step(store, &cursor, bit)?;
        if step.child == 0 {
            return Err(Error::Corrupt("used bitmap bit path disappeared"));
        }
        path.push(frame(&cursor, &step))?;
        cursor = touch_child(store, &cursor, step, limit, kind, retired)?;
    }
    if clear_leaf(store, &cursor, &path, limit, kind, bit)? {
        remove_empty_path(store, root, &path, limit, kind)?;
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
    let (page_number, header) = touch(store, root, kind, level, 0, limit, retired)?;
    Ok(Cursor {
        page_number,
        header,
        base: 0,
    })
}

fn branch_step<S: Store>(store: &S, cursor: &Cursor, bit: u32) -> Result<BranchStep> {
    let index = child_index(bit, cursor.header.level)?;
    let span = coverage(cursor.header.level - 1)?;
    let child_base = add_child_base(cursor.base, span, index)?;
    let limit = store.page_limit();
    let child = store.inspect_page(cursor.page_number, |page| {
        branch_child(page, &cursor.header, index, limit)
    })?;
    Ok(BranchStep {
        index,
        child,
        child_base,
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
    parent: &Cursor,
    step: BranchStep,
    limit: u64,
    kind: Kind,
    retired: &mut RetiredPages,
) -> Result<Cursor> {
    let (page_number, header) = touch(
        store,
        step.child,
        kind,
        parent.header.level - 1,
        step.child_base,
        limit,
        retired,
    )?;
    if page_number != step.child {
        store.update_page(parent.page_number, |page| {
            set_pointer(page, &parent.header, step.index, page_number)
        })?;
    }
    Ok(Cursor {
        page_number,
        header,
        base: step.child_base,
    })
}

fn insert_missing<S: Store>(
    store: &mut S,
    cursor: &Cursor,
    path: &EditPath,
    step: BranchStep,
    spec: SetSpec,
) -> Result<()> {
    let child = new_subtree(
        store,
        spec.kind,
        cursor.header.level - 1,
        step.child_base,
        spec.limit,
        spec.bit,
    )?;
    let candidate = match spec.candidate_hint {
        Some(candidate) => candidate,
        None => subtree_has_candidate(store, child, spec.kind, step.child_base, spec.limit)?,
    };
    store.update_page(cursor.page_number, |page| {
        set_branch_child(page, &cursor.header, step.index, child, candidate)
    })?;
    match spec.candidate_hint {
        Some(candidate) => propagate_known(
            store,
            &path.frames,
            path.depth - 1,
            cursor.page_number,
            cursor.base,
            spec.limit,
            spec.kind,
            candidate,
        ),
        None => propagate(
            store,
            &path.frames,
            path.depth - 1,
            cursor.page_number,
            cursor.base,
            spec.limit,
            spec.kind,
        ),
    }
}

fn set_leaf<S: Store>(
    store: &mut S,
    cursor: &Cursor,
    path: &EditPath,
    spec: SetSpec,
) -> Result<()> {
    let word_index = leaf_word_index(spec.bit);
    let word_candidate = store.update_page(cursor.page_number, |page| {
        let word = leaf_word(page.view(), word_index)?;
        let mask = 1u64 << (u64::from(spec.bit) % 64);
        if word & mask != 0 {
            return Err(Error::Corrupt("used bitmap bit is already set"));
        }
        let next = word | mask;
        set_leaf_word(page, word_index, next)?;
        stamp_leaf(page, cursor.header.item_count + usize::from(word == 0))?;
        Ok(word_has_candidate(
            next,
            word_index,
            cursor.base,
            spec.limit,
            spec.kind,
        ))
    })?;
    let candidate = match spec.candidate_hint {
        Some(candidate) => candidate,
        None => {
            word_candidate
                || subtree_has_candidate(
                    store,
                    cursor.page_number,
                    spec.kind,
                    cursor.base,
                    spec.limit,
                )?
        }
    };
    propagate_known(
        store,
        &path.frames,
        path.depth,
        cursor.page_number,
        cursor.base,
        spec.limit,
        spec.kind,
        candidate,
    )
}

fn clear_leaf<S: Store>(
    store: &mut S,
    cursor: &Cursor,
    path: &EditPath,
    limit: u64,
    kind: Kind,
    bit: u32,
) -> Result<bool> {
    let word_index = leaf_word_index(bit);
    let count = store.update_page(cursor.page_number, |page| {
        let word = leaf_word(page.view(), word_index)?;
        let mask = 1u64 << (u64::from(bit) % 64);
        if word & mask == 0 {
            return Err(Error::Corrupt("used bitmap bit disappeared"));
        }
        let next = word & !mask;
        set_leaf_word(page, word_index, next)?;
        Ok(cursor.header.item_count - usize::from(next == 0))
    })?;
    if count == 0 {
        store.discard_private(cursor.page_number)?;
        return Ok(true);
    }
    store.update_page(cursor.page_number, |page| stamp_leaf(page, count))?;
    propagate_known(
        store,
        &path.frames,
        path.depth,
        cursor.page_number,
        cursor.base,
        limit,
        kind,
        true,
    )?;
    Ok(false)
}

fn word_has_candidate(word: u64, index: usize, base: u64, limit: u64, kind: Kind) -> bool {
    let word_base = base + index as u64 * 64;
    let first = word_base.max(kind.first_candidate());
    let end = word_base.saturating_add(64).min(limit);
    if first >= end {
        return false;
    }
    let start_bit = (first - word_base) as u32;
    let end_bit = (end - word_base) as u32;
    let lower = u64::MAX << start_bit;
    let upper = if end_bit == 64 {
        u64::MAX
    } else {
        (1u64 << end_bit) - 1
    };
    let eligible = lower & upper;
    word & eligible != eligible
}
