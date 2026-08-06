//! Page-local insertion and split propagation.

use crate::contract::{MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page::{self, Builder};

use super::page::{
    self, branch_child, build_edit, build_pair_edit, key_at, lower_bound, parse, require_codec,
    CellBuf, Edit, PairEdit,
};
use super::{private_path, Codec, Frame, Path, RetiredPages, Store};

struct BranchSplit<K> {
    right_page: u32,
    right_first: K,
    left_first: K,
    level: u16,
}

struct LeafTarget {
    path: Path,
    page_number: u32,
    source: [u8; PAGE_SIZE],
    header: crate::slotted_page::Header,
    index: usize,
    exists: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LocalInsert {
    Inserted,
    General,
}

pub(crate) fn insert<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    retired: &mut RetiredPages,
) -> Result<bool> {
    require_leaf::<C>(leaf_cell)?;
    let key = C::read_key(leaf_cell, 0)?;
    if *root == 0 {
        *root = new_leaf::<C, S>(store, leaf_cell)?;
        return Ok(true);
    }
    let target = locate_leaf::<C, S>(store, root, key, retired)?;
    edit_leaf::<C, S>(store, root, leaf_cell, target)
}

pub(crate) fn insert_if_local_gap<C, S, F>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    retired: &mut RetiredPages,
    mut accepts: F,
) -> Result<LocalInsert>
where
    C: Codec,
    S: Store,
    F: FnMut(Option<&[u8]>, Option<&[u8]>) -> Result<bool>,
{
    require_leaf::<C>(leaf_cell)?;
    if *root == 0 {
        *root = new_leaf::<C, S>(store, leaf_cell)?;
        return Ok(LocalInsert::Inserted);
    }

    let key = C::read_key(leaf_cell, 0)?;
    let target = locate_leaf::<C, S>(store, root, key, retired)?;
    if !retired.as_slice().is_empty() {
        return Err(Error::Corrupt("private B+tree contains a committed page"));
    }
    if target.exists {
        return Ok(LocalInsert::General);
    }

    let previous = if target.index > 0 {
        Some(page::codec_cell::<C>(
            &target.source,
            &target.header,
            target.index - 1,
        )?)
    } else if target.path.frames[..target.path.depth]
        .iter()
        .all(|frame| frame.index == 0)
    {
        None
    } else {
        return Ok(LocalInsert::General);
    };
    let next = if target.index < target.header.item_count {
        Some(page::codec_cell::<C>(
            &target.source,
            &target.header,
            target.index,
        )?)
    } else if target.path.frames[..target.path.depth]
        .iter()
        .all(|frame| frame.index + 1 == frame.item_count)
    {
        None
    } else {
        return Ok(LocalInsert::General);
    };
    if !accepts(previous, next)? {
        return Ok(LocalInsert::General);
    }

    insert_local_leaf::<C, S>(store, root, leaf_cell, target)?;
    Ok(LocalInsert::Inserted)
}

fn insert_local_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    target: LeafTarget,
) -> Result<()> {
    if !slotted_page::insert_fits(&target.header, leaf_cell.len()) {
        edit_leaf::<C, S>(store, root, leaf_cell, target)?;
        return Ok(());
    }
    store.update_page(target.page_number, |page| {
        if !slotted_page::insert(page, &target.header, target.index, leaf_cell)? {
            return Err(Error::Corrupt(
                "private B+tree leaf changed during insertion",
            ));
        }
        Ok(())
    })?;
    if target.index == 0 {
        let key = C::read_key(leaf_cell, 0)?;
        propagate_first::<C, S>(store, root, &target.path, key)?;
    }
    Ok(())
}

fn require_leaf<C: Codec>(leaf_cell: &[u8]) -> Result<()> {
    require_codec::<C>()?;
    if leaf_cell.is_empty() || leaf_cell.len() > C::MAX_LEAF_SIZE {
        return Err(Error::InvalidArgument("wrong B+tree leaf size"));
    }
    C::validate_leaf(leaf_cell)
}

fn locate_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<LeafTarget> {
    let (path, leaf_page, source, header) = private_path::<C, S>(store, root, key, retired)?;
    let (index, exists) = lower_bound::<C>(&source, &header, key, true)?;
    Ok(LeafTarget {
        path,
        page_number: leaf_page,
        source,
        header,
        index,
        exists,
    })
}

fn edit_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    target: LeafTarget,
) -> Result<bool> {
    let edit = Edit {
        index: target.index,
        replace: target.exists,
        cell: leaf_cell,
    };
    let total = edit.total(target.header.item_count);
    if !page::edit_fits::<C>(&target.source, &target.header, edit, 0, total)? {
        return split_leaf::<C, S>(store, root, target, edit);
    }
    write_leaf::<C, S>(store, root, target, edit, total)
}

fn write_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: LeafTarget,
    edit: Edit<'_>,
    total: usize,
) -> Result<bool> {
    let mut output = [0; PAGE_SIZE];
    build_edit::<C>(&target.source, &target.header, edit, 0, total, &mut output)?;
    store.write(target.page_number, &output)?;
    if target.index == 0 {
        let key = C::read_key(edit.cell, 0)?;
        propagate_first::<C, S>(store, root, &target.path, key)?;
    }
    Ok(!target.exists)
}

fn new_leaf<C: Codec, S: Store>(store: &mut S, cell: &[u8]) -> Result<u32> {
    let page_number = store.allocate()?;
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, C::LEAF_TYPE, store.target_txn(), 0, C::AUX);
    builder.push(cell)?;
    builder.finish()?;
    store.write(page_number, &page)?;
    Ok(page_number)
}

fn split_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: LeafTarget,
    edit: Edit<'_>,
) -> Result<bool> {
    let total = edit.total(target.header.item_count);
    let middle = page::split_index::<C>(&target.source, &target.header, edit)?;
    let mut left = [0; PAGE_SIZE];
    let mut right = [0; PAGE_SIZE];
    build_edit::<C>(&target.source, &target.header, edit, 0, middle, &mut left)?;
    build_edit::<C>(
        &target.source,
        &target.header,
        edit,
        middle,
        total,
        &mut right,
    )?;
    publish_leaf_split::<C, S>(store, root, target, left, right)?;
    Ok(true)
}

fn publish_leaf_split<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: LeafTarget,
    left: [u8; PAGE_SIZE],
    right: [u8; PAGE_SIZE],
) -> Result<()> {
    let right_page = store.allocate()?;
    store.write(target.page_number, &left)?;
    store.write(right_page, &right)?;
    let left_header = parse::<C>(&left, store.target_txn(), Some(0))?;
    let right_header = parse::<C>(&right, store.target_txn(), Some(0))?;
    propagate_split::<C, S>(
        store,
        root,
        &target.path,
        target.page_number,
        key_at::<C>(&left, &left_header, 0)?,
        right_page,
        key_at::<C>(&right, &right_header, 0)?,
        0,
    )
}

#[allow(clippy::too_many_arguments)]
fn propagate_split<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    left_page: u32,
    left_first: C::Key,
    right_page: u32,
    right_first: C::Key,
    child_level: u16,
) -> Result<()> {
    propagate_split_from::<C, S>(
        store,
        root,
        path,
        path.depth,
        left_page,
        left_first,
        right_page,
        right_first,
        child_level,
    )
}

#[allow(clippy::too_many_arguments)]
fn propagate_split_from<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    mut depth: usize,
    mut left_page: u32,
    mut left_first: C::Key,
    mut right_page: u32,
    mut right_first: C::Key,
    mut child_level: u16,
) -> Result<()> {
    loop {
        if depth == 0 {
            *root = new_root::<C, S>(
                store,
                left_page,
                left_first,
                right_page,
                right_first,
                child_level + 1,
            )?;
            return Ok(());
        }
        depth -= 1;
        let frame = path.frames[depth];
        let split = insert_branch::<C, S>(store, frame, left_first, right_page, right_first)?;
        let Some(split) = split else {
            if frame.index == 0 {
                propagate_first_from::<C, S>(store, root, path, depth, left_first)?;
            }
            return Ok(());
        };
        left_page = frame.page_number;
        left_first = split.left_first;
        right_page = split.right_page;
        right_first = split.right_first;
        child_level = split.level;
    }
}

fn insert_branch<C: Codec, S: Store>(
    store: &mut S,
    frame: Frame,
    left_first: C::Key,
    right_page: u32,
    right_first: C::Key,
) -> Result<Option<BranchSplit<C::Key>>> {
    let mut source = [0; PAGE_SIZE];
    store.read(frame.page_number, &mut source)?;
    let header = parse::<C>(&source, store.target_txn(), None)?;
    let left_child = branch_child::<C>(&source, &header, frame.index, store.page_limit())?;
    let left = CellBuf::branch::<C>(left_first, left_child)?;
    let right = CellBuf::branch::<C>(right_first, right_page)?;
    let edit = PairEdit {
        index: frame.index,
        left: left.as_slice(),
        right: right.as_slice(),
    };
    apply_pair_edit::<C, S>(store, frame.page_number, &source, &header, edit)
}

fn apply_pair_edit<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    source: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
    edit: PairEdit<'_>,
) -> Result<Option<BranchSplit<C::Key>>> {
    let total = edit.total(header.item_count);
    if page::pair_fits::<C>(source, header, edit)? {
        write_pair_edit::<C, S>(store, page_number, source, header, edit, total)?;
        return Ok(None);
    }
    split_pair_edit::<C, S>(store, page_number, source, header, edit, total)
}

fn write_pair_edit<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    source: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
    edit: PairEdit<'_>,
    total: usize,
) -> Result<()> {
    let mut output = [0; PAGE_SIZE];
    build_pair_edit::<C>(source, header, edit, 0, total, &mut output)?;
    store.write(page_number, &output)
}

fn split_pair_edit<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    source: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
    edit: PairEdit<'_>,
    total: usize,
) -> Result<Option<BranchSplit<C::Key>>> {
    let middle = page::pair_split_index::<C>(source, header, edit)?;
    let mut left_page = [0; PAGE_SIZE];
    let mut right_page = [0; PAGE_SIZE];
    build_pair_edit::<C>(source, header, edit, 0, middle, &mut left_page)?;
    build_pair_edit::<C>(source, header, edit, middle, total, &mut right_page)?;
    split_branch::<C, S>(store, page_number, header, left_page, right_page)
}

fn split_branch<C: Codec, S: Store>(
    store: &mut S,
    left_page_number: u32,
    header: &crate::slotted_page::Header,
    left_page: [u8; PAGE_SIZE],
    right_page: [u8; PAGE_SIZE],
) -> Result<Option<BranchSplit<C::Key>>> {
    let right_page_number = store.allocate()?;
    store.write(left_page_number, &left_page)?;
    store.write(right_page_number, &right_page)?;
    let left_header = parse::<C>(&left_page, store.target_txn(), Some(header.level))?;
    let right_header = parse::<C>(&right_page, store.target_txn(), Some(header.level))?;
    Ok(Some(BranchSplit {
        right_page: right_page_number,
        right_first: key_at::<C>(&right_page, &right_header, 0)?,
        left_first: key_at::<C>(&left_page, &left_header, 0)?,
        level: header.level,
    }))
}

fn new_root<C: Codec, S: Store>(
    store: &mut S,
    left_page: u32,
    left_first: C::Key,
    right_page: u32,
    right_first: C::Key,
    level: u16,
) -> Result<u32> {
    if level > MAX_TREE_LEVEL {
        return Err(Error::InvalidArgument("B+tree height limit reached"));
    }
    let page_number = store.allocate()?;
    let left = CellBuf::branch::<C>(left_first, left_page)?;
    let right = CellBuf::branch::<C>(right_first, right_page)?;
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, C::BRANCH_TYPE, store.target_txn(), level, C::AUX);
    builder.push(left.as_slice())?;
    builder.push(right.as_slice())?;
    builder.finish()?;
    store.write(page_number, &page)?;
    Ok(page_number)
}

pub(super) fn propagate_first<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    key: C::Key,
) -> Result<()> {
    propagate_first_from::<C, S>(store, root, path, path.depth, key)
}

pub(super) fn propagate_first_from<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    mut depth: usize,
    key: C::Key,
) -> Result<()> {
    while depth > 0 {
        depth -= 1;
        let frame = path.frames[depth];
        let split = replace_first_branch::<C, S>(store, frame, key)?;
        if let Some(split) = split {
            return propagate_split_from::<C, S>(
                store,
                root,
                path,
                depth,
                frame.page_number,
                split.left_first,
                split.right_page,
                split.right_first,
                split.level,
            );
        }
        if frame.index != 0 {
            break;
        }
    }
    Ok(())
}

fn replace_first_branch<C: Codec, S: Store>(
    store: &mut S,
    frame: Frame,
    key: C::Key,
) -> Result<Option<BranchSplit<C::Key>>> {
    let mut source = [0; PAGE_SIZE];
    store.read(frame.page_number, &mut source)?;
    let header = parse::<C>(&source, store.target_txn(), None)?;
    let child = branch_child::<C>(&source, &header, frame.index, store.page_limit())?;
    let replacement = CellBuf::branch::<C>(key, child)?;
    let edit = Edit {
        index: frame.index,
        replace: true,
        cell: replacement.as_slice(),
    };
    apply_branch_replacement::<C, S>(store, frame.page_number, &source, &header, edit)
}

fn apply_branch_replacement<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    source: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
    edit: Edit<'_>,
) -> Result<Option<BranchSplit<C::Key>>> {
    if page::edit_fits::<C>(source, header, edit, 0, header.item_count)? {
        write_branch_replacement::<C, S>(store, page_number, source, header, edit)?;
        return Ok(None);
    }
    split_branch_replacement::<C, S>(store, page_number, source, header, edit)
}

fn write_branch_replacement<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    source: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
    edit: Edit<'_>,
) -> Result<()> {
    let mut output = [0; PAGE_SIZE];
    build_edit::<C>(source, header, edit, 0, header.item_count, &mut output)?;
    store.write(page_number, &output)
}

fn split_branch_replacement<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    source: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
    edit: Edit<'_>,
) -> Result<Option<BranchSplit<C::Key>>> {
    let middle = page::split_index::<C>(source, header, edit)?;
    let mut left = [0; PAGE_SIZE];
    let mut right = [0; PAGE_SIZE];
    build_edit::<C>(source, header, edit, 0, middle, &mut left)?;
    build_edit::<C>(source, header, edit, middle, header.item_count, &mut right)?;
    split_branch::<C, S>(store, page_number, header, left, right)
}
