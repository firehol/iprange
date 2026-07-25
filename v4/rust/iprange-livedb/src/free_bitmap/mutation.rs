//! Copy-on-write free-page bitmap mutation.

use crate::contract::{u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::fixed_tree::RetiredPages;
use crate::slotted_page::{put_u64, HEADER_SIZE};

use super::{
    branch_child, child_index, coverage, first_leaf_word, first_summary, leaf_word_index,
    new_branch, new_leaf, parse, require_bit, required_level, set_branch_child, stamp, BitmapStore,
    Header, MAX_BITMAP_LEVEL,
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
    page: [u8; PAGE_SIZE],
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
    mut cursor: Cursor,
    bit: u32,
    retired: &mut RetiredPages,
) -> Result<Option<Cursor>> {
    let index = child_index(bit, cursor.header.level)?;
    let child = branch_child(&cursor.page, &cursor.header, index, store.page_limit())?;
    if child == 0 {
        install_new_child(store, &mut cursor, index, bit)?;
        return Ok(None);
    }
    let next = touch_cursor(store, child, cursor.header.level - 1, retired)?;
    if next.page_number != child {
        replace_child(store, &mut cursor, index, next.page_number)?;
    }
    Ok(Some(next))
}

fn install_new_child<S: BitmapStore>(
    store: &mut S,
    cursor: &mut Cursor,
    index: usize,
    bit: u32,
) -> Result<()> {
    let child = new_subtree(store, cursor.header.level - 1, bit)?;
    replace_child(store, cursor, index, child)
}

fn replace_child<S: BitmapStore>(
    store: &mut S,
    cursor: &mut Cursor,
    index: usize,
    child: u32,
) -> Result<()> {
    set_branch_child(&mut cursor.page, index, child)?;
    stamp(&mut cursor.page)?;
    store.write(cursor.page_number, &cursor.page)
}

fn mark_leaf_free<S: BitmapStore>(store: &mut S, mut cursor: Cursor, bit: u32) -> Result<()> {
    let word_index = leaf_word_index(bit);
    let mask = 1u64 << (u64::from(bit) % 64);
    let at = HEADER_SIZE + word_index * 8;
    let word = u64_le(&cursor.page, at);
    if word & mask != 0 {
        return Err(Error::Corrupt("page is already free"));
    }
    put_u64(&mut cursor.page, at, word | mask);
    stamp(&mut cursor.page)?;
    store.write(cursor.page_number, &cursor.page)
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
    let (mut leaf, depth, base) = descend_lowest(store, cursor, retired, &mut path)?;
    let (selected, word_index, word, bit_in_word) =
        select_leaf(store, &leaf, &path[..depth], base, limit)?;
    if clear_leaf_bit(store, &mut leaf, word_index, word, bit_in_word)? {
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
        let (next, next_base) = descend_lowest_once(store, cursor, retired, path, depth, base)?;
        cursor = next;
        base = next_base;
        depth += 1;
    }
    Ok((cursor, depth, base))
}

fn descend_lowest_once<S: BitmapStore>(
    store: &mut S,
    mut cursor: Cursor,
    retired: &mut RetiredPages,
    path: &mut [Frame],
    depth: usize,
    base: u64,
) -> Result<(Cursor, u64)> {
    let index = first_summary(&cursor.page).ok_or(Error::Corrupt("free summary is empty"))?;
    path[depth] = Frame {
        page_number: cursor.page_number,
        child_index: index,
        level: cursor.header.level,
    };
    let base = add_child_base(base, cursor.header.level, index)?;
    let child = branch_child(&cursor.page, &cursor.header, index, store.page_limit())?;
    if child == 0 {
        return Err(Error::Corrupt("free summary names an absent child"));
    }
    let next = touch_cursor(store, child, cursor.header.level - 1, retired)?;
    if next.page_number != child {
        replace_child(store, &mut cursor, index, next.page_number)?;
    }
    Ok((next, base))
}

fn add_child_base(base: u64, level: u16, index: usize) -> Result<u64> {
    let child_coverage = coverage(level - 1)?;
    let offset = child_coverage
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
    let (word_index, word) =
        first_leaf_word(&leaf.page).ok_or(Error::Corrupt("free leaf is empty"))?;
    let bit_in_word = u64::from(word.trailing_zeros());
    let offset = (word_index as u64) * 64 + bit_in_word;
    let selected = base
        .checked_add(offset)
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
    let on_path = path.iter().any(|frame| frame.page_number == selected);
    if selected == leaf_page || on_path || store.allocation_forbidden(selected) {
        return Err(Error::Corrupt("free bit names protected allocator state"));
    }
    Ok(selected)
}

fn clear_leaf_bit<S: BitmapStore>(
    store: &mut S,
    leaf: &mut Cursor,
    word_index: usize,
    word: u64,
    bit_in_word: u64,
) -> Result<bool> {
    let at = HEADER_SIZE + word_index * 8;
    put_u64(&mut leaf.page, at, word & !(1u64 << bit_in_word));
    if first_leaf_word(&leaf.page).is_some() {
        stamp(&mut leaf.page)?;
        store.write(leaf.page_number, &leaf.page)?;
        return Ok(true);
    }
    store.discard_private(leaf.page_number)?;
    Ok(false)
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
    let mut parent = [0; PAGE_SIZE];
    store.read(frame.page_number, &mut parent)?;
    parse(&parent, store.target_txn(), Some(frame.level), false)?;
    set_branch_child(&mut parent, frame.child_index, 0)?;
    if first_summary(&parent).is_some() {
        stamp(&mut parent)?;
        store.write(frame.page_number, &parent)?;
        return Ok(true);
    }
    store.discard_private(frame.page_number)?;
    Ok(false)
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
    let (page_number, page, header) = touch(store, page_number, expected_level, retired)?;
    Ok(Cursor {
        page_number,
        page,
        header,
    })
}

fn touch<S: BitmapStore>(
    store: &mut S,
    page_number: u32,
    expected_level: u16,
    retired: &mut RetiredPages,
) -> Result<(u32, [u8; PAGE_SIZE], Header)> {
    let mut page = [0; PAGE_SIZE];
    store.read(page_number, &mut page)?;
    let born_txn = u64_le(&page, 8);
    let header = parse(
        &page,
        store.target_txn(),
        Some(expected_level),
        born_txn != store.target_txn(),
    )?;
    if born_txn == store.target_txn() {
        return Ok((page_number, page, header));
    }
    copy_private(store, page_number, page, header, retired)
}

fn copy_private<S: BitmapStore>(
    store: &mut S,
    page_number: u32,
    mut page: [u8; PAGE_SIZE],
    header: Header,
    retired: &mut RetiredPages,
) -> Result<(u32, [u8; PAGE_SIZE], Header)> {
    let private = store.allocate_bitmap_page()?;
    put_u64(&mut page, 8, store.target_txn());
    stamp(&mut page)?;
    store.write(private, &page)?;
    retired.push(page_number)?;
    Ok((private, page, header))
}

fn grow_root<S: BitmapStore>(store: &mut S, root: &mut u32, required: u16) -> Result<()> {
    let mut page = [0; PAGE_SIZE];
    store.read(*root, &mut page)?;
    let mut level = parse(&page, store.target_txn(), None, false)?.level;
    if level > required {
        return Err(Error::Corrupt("free bitmap root level is too high"));
    }
    while level < required {
        grow_root_once(store, root, level + 1)?;
        level += 1;
    }
    Ok(())
}

fn grow_root_once<S: BitmapStore>(store: &mut S, root: &mut u32, level: u16) -> Result<()> {
    let parent = store.allocate_bitmap_page()?;
    let mut page = new_branch(store.target_txn(), level);
    set_branch_child(&mut page, 0, *root)?;
    stamp(&mut page)?;
    store.write(parent, &page)?;
    *root = parent;
    Ok(())
}

fn new_subtree<S: BitmapStore>(store: &mut S, level: u16, bit: u32) -> Result<u32> {
    if level == 0 {
        new_leaf_subtree(store, bit)
    } else {
        new_branch_subtree(store, level, bit)
    }
}

fn new_leaf_subtree<S: BitmapStore>(store: &mut S, bit: u32) -> Result<u32> {
    let page_number = store.allocate_bitmap_page()?;
    let mut page = new_leaf(store.target_txn());
    let word = leaf_word_index(bit);
    put_u64(
        &mut page,
        HEADER_SIZE + word * 8,
        1u64 << (u64::from(bit) % 64),
    );
    stamp(&mut page)?;
    store.write(page_number, &page)?;
    Ok(page_number)
}

fn new_branch_subtree<S: BitmapStore>(store: &mut S, level: u16, bit: u32) -> Result<u32> {
    let child = new_subtree(store, level - 1, bit)?;
    let page_number = store.allocate_bitmap_page()?;
    let mut page = new_branch(store.target_txn(), level);
    set_branch_child(&mut page, child_index(bit, level)?, child)?;
    stamp(&mut page)?;
    store.write(page_number, &page)?;
    Ok(page_number)
}
