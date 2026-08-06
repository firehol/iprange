//! Copy-on-write free-page bitmap mutation.

use crate::contract::u64_le;
use crate::error::{Error, Result};
use crate::fixed_tree::RetiredPages;
use crate::slotted_page::{PageEdit, PageSink, HEADER_SIZE};

use super::{
    branch_child, child_index, coverage, first_leaf_word, first_summary, initialize,
    leaf_word_index, parse, require_bit, required_level, set_branch_child, stamp, BitmapStore,
    Header, BITMAP_BRANCH, BITMAP_LEAF, LEAF_END, MAX_BITMAP_LEVEL,
};

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

struct Cursor {
    page_number: u32,
    header: Header,
}

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
    let cursor = touch_cursor(store, *root, required, retired)?;
    *root = cursor.page_number;
    insert_free(store, cursor, bit, retired)
}

fn insert_free<S: BitmapStore>(
    store: &mut S,
    mut cursor: Cursor,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<()> {
    while cursor.header.level > 0 {
        let Some(next) = descend_for_insert(store, cursor, bit, retired)? else {
            return Ok(());
        };
        cursor = next;
    }
    mark_leaf_free(store, cursor, bit)
}

fn descend_for_insert<S: BitmapStore>(
    store: &mut S,
    cursor: Cursor,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<Option<Cursor>> {
    let index = child_index(bit, cursor.header.level)?;
    let limit = store.page_limit();
    let child = store.inspect_page(cursor.page_number, |page| {
        branch_child(page, &cursor.header, index, limit)
    })?;
    if child == 0 {
        let child = new_subtree(store, cursor.header.level - 1, bit)?;
        replace_child(store, cursor.page_number, index, child)?;
        return Ok(None);
    }
    let next = touch_cursor(store, child, cursor.header.level - 1, retired)?;
    if next.page_number != child {
        replace_child(store, cursor.page_number, index, next.page_number)?;
    }
    Ok(Some(next))
}

fn replace_child<S: BitmapStore>(
    store: &mut S,
    page_number: u32,
    index: usize,
    child: u32,
) -> Result<()> {
    store.update_page(page_number, |page| {
        set_branch_child(page, index, child)?;
        stamp(page)
    })
}

fn mark_leaf_free<S: BitmapStore>(store: &mut S, cursor: Cursor, bit: u32) -> Result<()> {
    let word_index = leaf_word_index(bit);
    let mask = 1u64 << (u64::from(bit) % 64);
    let at = HEADER_SIZE + word_index * 8;
    store.update_page(cursor.page_number, |page| {
        let word = u64_le(page.view(), at);
        if word & mask != 0 {
            return Err(Error::Corrupt("page is already free"));
        }
        page.put_u64(at, word | mask)?;
        stamp(page)
    })
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
    take_from_nonempty(store, root, limit, retired).map(Some)
}

fn take_from_nonempty<S: BitmapStore>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<u32> {
    let required = required_level(limit)?;
    let cursor = touch_cursor(store, *root, required, retired)?;
    *root = cursor.page_number;
    let mut path = [EMPTY_FRAME; (MAX_BITMAP_LEVEL + 1) as usize];
    let (leaf, depth, base) = descend_lowest(store, cursor, retired, &mut path)?;
    let (selected, word_index, word, bit_in_word) =
        select_leaf(store, &leaf, &path[..depth], base, limit)?;
    if clear_leaf_bit(store, &leaf, word_index, word, bit_in_word)? {
        return Ok(selected);
    }
    prune_empty_path(store, root, &path, depth)?;
    Ok(selected)
}

fn descend_lowest<S: BitmapStore>(
    store: &mut S,
    mut cursor: Cursor,
    retired: &mut RetiredPages,
    path: &mut [Frame],
) -> Result<(Cursor, usize, u64)> {
    let mut depth = 0;
    let mut base = 0;
    while cursor.header.level > 0 {
        let index = store.inspect_page(cursor.page_number, |page| {
            first_summary(page).ok_or(Error::Corrupt("free summary is empty"))
        })?;
        path[depth] = Frame {
            page_number: cursor.page_number,
            child_index: index,
            level: cursor.header.level,
        };
        base = add_child_base(base, cursor.header.level, index)?;
        let limit = store.page_limit();
        let child = store.inspect_page(cursor.page_number, |page| {
            branch_child(page, &cursor.header, index, limit)
        })?;
        if child == 0 {
            return Err(Error::Corrupt("free summary names an absent child"));
        }
        let next = touch_cursor(store, child, cursor.header.level - 1, retired)?;
        if next.page_number != child {
            replace_child(store, cursor.page_number, index, next.page_number)?;
        }
        cursor = next;
        depth += 1;
    }
    Ok((cursor, depth, base))
}

fn add_child_base(base: u64, level: u16, index: usize) -> Result<u64> {
    let offset = coverage(level - 1)?
        .checked_mul(index as u64)
        .ok_or(Error::Corrupt("free bitmap coverage overflow"))?;
    base.checked_add(offset)
        .ok_or(Error::Corrupt("free bitmap coverage overflow"))
}

fn select_leaf<S: BitmapStore>(
    store: &S,
    leaf: &Cursor,
    path: &[Frame],
    base: u64,
    limit: u64,
) -> Result<(u32, usize, u64, u64)> {
    let (word_index, word) = store.inspect_page(leaf.page_number, |page| {
        first_leaf_word(page).ok_or(Error::Corrupt("free leaf is empty"))
    })?;
    let bit_in_word = u64::from(word.trailing_zeros());
    let selected = base
        .checked_add(word_index as u64 * 64 + bit_in_word)
        .ok_or(Error::Corrupt("free page number overflow"))?;
    let selected = validate_selected(store, leaf.page_number, path, selected, limit)?;
    Ok((selected, word_index, word, bit_in_word))
}

fn validate_selected<S: BitmapStore>(
    store: &S,
    leaf_page: u32,
    path: &[Frame],
    selected: u64,
    limit: u64,
) -> Result<u32> {
    if selected < 2 || selected >= limit || selected >= (1u64 << 32) {
        return Err(Error::Corrupt("free bit is outside allocatable bounds"));
    }
    let selected = selected as u32;
    if selected == leaf_page
        || path.iter().any(|frame| frame.page_number == selected)
        || store.allocation_forbidden(selected)
    {
        return Err(Error::Corrupt("free bit names protected allocator state"));
    }
    Ok(selected)
}

fn clear_leaf_bit<S: BitmapStore>(
    store: &mut S,
    leaf: &Cursor,
    word_index: usize,
    word: u64,
    bit_in_word: u64,
) -> Result<bool> {
    let at = HEADER_SIZE + word_index * 8;
    let nonempty = store.update_page(leaf.page_number, |page| {
        page.put_u64(at, word & !(1u64 << bit_in_word))?;
        if first_leaf_word(page.view()).is_some() {
            stamp(page)?;
            Ok(true)
        } else {
            Ok(false)
        }
    })?;
    if !nonempty {
        store.discard_private(leaf.page_number)?;
    }
    Ok(nonempty)
}

fn prune_empty_path<S: BitmapStore>(
    store: &mut S,
    root: &mut u32,
    path: &[Frame],
    mut depth: usize,
) -> Result<()> {
    while depth > 0 {
        depth -= 1;
        if prune_parent(store, path[depth])? {
            return Ok(());
        }
    }
    *root = 0;
    Ok(())
}

fn prune_parent<S: BitmapStore>(store: &mut S, frame: Frame) -> Result<bool> {
    let target_txn = store.target_txn();
    let nonempty = store.update_page(frame.page_number, |page| {
        parse(page.view(), target_txn, Some(frame.level), false)?;
        set_branch_child(page, frame.child_index, 0)?;
        if first_summary(page.view()).is_some() {
            stamp(page)?;
            Ok(true)
        } else {
            Ok(false)
        }
    })?;
    if !nonempty {
        store.discard_private(frame.page_number)?;
    }
    Ok(nonempty)
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

fn touch_cursor<S: BitmapStore>(
    store: &mut S,
    page_number: u32,
    expected_level: u16,
    retired: &mut RetiredPages,
) -> Result<Cursor> {
    let target_txn = store.target_txn();
    let (header, private) = store.inspect_page(page_number, |page| {
        let born_txn = u64_le(page, 8);
        let header = parse(
            page,
            target_txn,
            Some(expected_level),
            born_txn != target_txn,
        )?;
        Ok((header, born_txn == target_txn))
    })?;
    if private {
        return Ok(Cursor {
            page_number,
            header,
        });
    }

    let private_page = store.allocate_bitmap_page()?;
    store.copy_page(page_number, private_page, |source, output| {
        output.write_source(0, source)?;
        output.put_u64(8, target_txn)?;
        output.put_u32(28, 0)?;
        stamp(output)
    })?;
    retired.push(page_number)?;
    Ok(Cursor {
        page_number: private_page,
        header,
    })
}

fn grow_root<S: BitmapStore>(store: &mut S, root: &mut u32, required: u16) -> Result<()> {
    let target_txn = store.target_txn();
    let mut level =
        store.inspect_page(
            *root,
            |page| Ok(parse(page, target_txn, None, false)?.level),
        )?;
    if level > required {
        return Err(Error::Corrupt("free bitmap root level is too high"));
    }
    while level < required {
        let parent = store.allocate_bitmap_page()?;
        let child = *root;
        let next_level = level + 1;
        store.update_page(parent, |page| {
            initialize(
                page,
                BITMAP_BRANCH,
                target_txn,
                next_level,
                super::BRANCH_END,
            );
            set_branch_child(page, 0, child)?;
            stamp(page)
        })?;
        *root = parent;
        level = next_level;
    }
    Ok(())
}

fn new_subtree<S: BitmapStore>(store: &mut S, level: u16, bit: u32) -> Result<u32> {
    if level == 0 {
        let page_number = store.allocate_bitmap_page()?;
        let txn = store.target_txn();
        let word = leaf_word_index(bit);
        store.update_page(page_number, |page| {
            initialize(page, BITMAP_LEAF, txn, 0, LEAF_END);
            page.put_u64(HEADER_SIZE + word * 8, 1u64 << (u64::from(bit) % 64))?;
            stamp(page)
        })?;
        return Ok(page_number);
    }

    let child = new_subtree(store, level - 1, bit)?;
    let page_number = store.allocate_bitmap_page()?;
    let txn = store.target_txn();
    let index = child_index(bit, level)?;
    store.update_page(page_number, |page| {
        initialize(page, BITMAP_BRANCH, txn, level, super::BRANCH_END);
        set_branch_child(page, index, child)?;
        stamp(page)
    })?;
    Ok(page_number)
}
