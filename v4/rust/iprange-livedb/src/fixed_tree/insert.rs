//! Page-local insertion and split propagation.

use crate::contract::MAX_TREE_LEVEL;
use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::page_io::PageEdit;
use crate::slotted_page::{self, Builder};

use super::gap::{Edge, PrivatePosition};
use super::page::{
    self, branch_child, build_edit, build_replacement, parse, require_codec, CellBuf, Edit,
    Replacement,
};
use super::{first_key, private_path, Codec, Frame, Path, PrivateLeaf, RetiredPages, Store};

struct BranchSplit<K> {
    right_page: u32,
    right_first: K,
    left_first: K,
    level: u16,
}

pub(super) struct LeafTarget {
    pub(super) path: Path,
    pub(super) page_number: u32,
    pub(super) header: crate::slotted_page::Header,
    pub(super) index: usize,
    pub(super) exists: bool,
}

pub(crate) fn insert<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    retired: &mut RetiredPages,
) -> Result<bool> {
    require_leaf::<C>(leaf_cell)?;
    if *root == 0 {
        *root = new_leaf::<C, S>(store, leaf_cell)?;
        return Ok(true);
    }
    let key = C::read_key(leaf_cell, 0)?;
    let target = locate_leaf::<C, S>(store, root, key, retired)?;
    edit_leaf::<C, S>(store, root, leaf_cell, target)
}

pub(crate) fn replace_leaf_with<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    cells: &[&[u8]],
    retired: &mut RetiredPages,
) -> Result<()> {
    require_replacement::<C>(key, cells)?;
    let leaf = private_path::<C, S>(store, root, key, retired)?;
    let target = inspect_leaf_target::<C, S>(store, leaf, key)?;
    if !target.exists {
        return Err(Error::Corrupt("B+tree replacement key is missing"));
    }
    replace_target::<C, S>(store, root, target, cells)
}

pub(super) fn replace_target<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: LeafTarget,
    cells: &[&[u8]],
) -> Result<()> {
    let edit = Replacement {
        index: target.index,
        cells,
    };
    let Some(split) = apply_replacement::<C, S>(store, target.page_number, &target.header, edit)?
    else {
        return Ok(());
    };
    if split.level != 0 {
        return Err(Error::Corrupt("B+tree leaf replacement changed level"));
    }
    propagate_split::<C, S>(
        store,
        root,
        &target.path,
        target.page_number,
        split.left_first,
        split.right_page,
        split.right_first,
        0,
    )
}

pub(super) fn require_leaf<C: Codec>(leaf_cell: &[u8]) -> Result<()> {
    require_codec::<C>()?;
    if leaf_cell.is_empty() || leaf_cell.len() > C::MAX_LEAF_SIZE {
        return Err(Error::InvalidArgument("wrong B+tree leaf size"));
    }
    C::validate_leaf(leaf_cell)
}

pub(super) fn require_replacement<C: Codec>(key: C::Key, cells: &[&[u8]]) -> Result<()> {
    if !(2..=3).contains(&cells.len()) {
        return Err(Error::InvalidArgument(
            "B+tree leaf replacement requires two or three cells",
        ));
    }
    let mut previous = None;
    for cell in cells {
        require_leaf::<C>(cell)?;
        let current = C::read_key(*cell, 0)?;
        if previous.is_some_and(|prior| prior >= current) {
            return Err(Error::InvalidArgument(
                "B+tree replacement keys are not increasing",
            ));
        }
        previous = Some(current);
    }
    if C::read_key(cells[0], 0)? != key {
        return Err(Error::InvalidArgument(
            "B+tree replacement changed its first key",
        ));
    }
    Ok(())
}

fn locate_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<LeafTarget> {
    let leaf = private_path::<C, S>(store, root, key, retired)?;
    inspect_leaf_target::<C, S>(store, leaf, key)
}

pub(super) fn locate_private_position<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
) -> Result<PrivatePosition> {
    let mut retired = RetiredPages::new();
    let leaf = private_path::<C, S>(store, root, key, &mut retired)?;
    if !retired.as_slice().is_empty() {
        return Err(Error::Corrupt("private B+tree position retired a page"));
    }
    Ok(PrivatePosition {
        path: leaf.path,
        page_number: leaf.page_number,
    })
}

fn inspect_leaf_target<C: Codec, S: Store>(
    _store: &S,
    leaf: PrivateLeaf<(usize, bool)>,
    _key: C::Key,
) -> Result<LeafTarget> {
    let (index, exists) = leaf.selection;
    Ok(LeafTarget {
        path: leaf.path,
        page_number: leaf.page_number,
        header: leaf.header,
        index,
        exists,
    })
}

pub(super) fn edit_leaf<C: Codec, S: Store>(
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
    let fits = store.inspect_page(target.page_number, |page| {
        page::edit_fits::<C, _>(page, &target.header, edit)
    })?;
    if !fits {
        split_leaf::<C, S>(store, root, target, edit)?;
        return Ok(true);
    }
    apply_leaf_edit::<C, S>(store, target.page_number, &target.header, edit)?;
    if target.index == 0 {
        let key = C::read_key(edit.cell, 0)?;
        propagate_first::<C, S>(store, root, &target.path, key)?;
    }
    Ok(!target.exists)
}

pub(super) fn apply_leaf_edit<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: Edit<'_>,
) -> Result<()> {
    store.update_page(page_number, |page| apply_edit::<C, _>(page, header, edit))
}

fn apply_edit<C: Codec, D: PageEdit>(
    page: &mut D,
    header: &crate::slotted_page::Header,
    edit: Edit<'_>,
) -> Result<()> {
    let changed = if edit.replace {
        let old_len = page::codec_cell::<C, _>(page.view(), header, edit.index)?.len();
        slotted_page::replace(page, header, edit.index, old_len, edit.cell)?
    } else {
        slotted_page::insert(page, header, edit.index, edit.cell)?
    };
    if changed {
        Ok(())
    } else {
        Err(Error::Corrupt("B+tree edit no longer fits"))
    }
}

pub(super) fn new_leaf<C: Codec, S: Store>(store: &mut S, cell: &[u8]) -> Result<u32> {
    let page_number = store.allocate()?;
    let txn = store.target_txn();
    store.update_page(page_number, |page| {
        let mut builder = Builder::new(page, C::LEAF_TYPE, txn, 0, C::AUX);
        builder.push(cell)?;
        builder.finish()
    })?;
    Ok(page_number)
}

fn split_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: LeafTarget,
    edit: Edit<'_>,
) -> Result<()> {
    let total = edit.total(target.header.item_count);
    let middle = store.inspect_page(target.page_number, |source| {
        page::split_index::<C, _>(source, &target.header, edit)
    })?;
    split_leaf_at::<C, S>(store, root, target, edit, middle, total)
}

pub(super) fn split_leaf_at_edge<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: LeafTarget,
    leaf_cell: &[u8],
    edge: Edge,
) -> Result<()> {
    let edit = Edit {
        index: match edge {
            Edge::First => 0,
            Edge::Last => target.header.item_count,
        },
        replace: false,
        cell: leaf_cell,
    };
    let total = target.header.item_count + 1;
    let middle = match edge {
        Edge::First => 1,
        Edge::Last => target.header.item_count,
    };
    split_leaf_at::<C, S>(store, root, target, edit, middle, total)
}

fn split_leaf_at<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: LeafTarget,
    edit: Edit<'_>,
    middle: usize,
    total: usize,
) -> Result<()> {
    let right_page = store.allocate()?;
    store.copy_page(target.page_number, right_page, |source, output| {
        build_edit::<C, _, _>(source, &target.header, edit, middle, total, output)
    })?;
    keep_left_edit::<C, S>(store, target.page_number, &target.header, edit, middle)?;

    let left_first = first_key::<C, S>(store, target.page_number, 0)?;
    let right_first = first_key::<C, S>(store, right_page, 0)?;
    propagate_split::<C, S>(
        store,
        root,
        &target.path,
        target.page_number,
        left_first,
        right_page,
        right_first,
        0,
    )?;
    crate::work::page_split(1);
    Ok(())
}

fn keep_left_edit<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: Edit<'_>,
    middle: usize,
) -> Result<()> {
    let txn = store.target_txn();
    store.update_page(page_number, |page| {
        let edit_in_left = edit.index < middle;
        let keep = middle - usize::from(edit_in_left && !edit.replace);
        if keep == 0 {
            let mut builder = Builder::new(
                page,
                if header.level == 0 {
                    C::LEAF_TYPE
                } else {
                    C::BRANCH_TYPE
                },
                txn,
                header.level,
                C::AUX,
            );
            builder.push(edit.cell)?;
            return builder.finish();
        }
        let left = page::truncate::<C, _>(page, header, keep)?;
        if edit_in_left {
            apply_edit::<C, _>(page, &left, edit)?;
        }
        Ok(())
    })
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
        let frame = path.frame(depth);
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
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    let (header, left_child) = store.inspect_page(frame.page_number, |source| {
        let header = parse::<C, _>(source, target_txn, None)?;
        let child = branch_child::<C, _>(source, &header, frame.index, page_limit)?;
        Ok((header, child))
    })?;
    let left = CellBuf::branch::<C>(left_first, left_child)?;
    let right = CellBuf::branch::<C>(right_first, right_page)?;
    let cells = [left.as_slice(), right.as_slice()];
    let edit = Replacement {
        index: frame.index,
        cells: &cells,
    };
    apply_replacement::<C, S>(store, frame.page_number, &header, edit)
}

fn apply_replacement<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: Replacement<'_>,
) -> Result<Option<BranchSplit<C::Key>>> {
    let fits = store.inspect_page(page_number, |source| {
        page::replacement_fits::<C, _>(source, header, edit)
    })?;
    if fits {
        store.update_page(page_number, |page| {
            apply_cells::<C, _>(page, header, edit.index, edit.cells)
        })?;
        return Ok(None);
    }
    split_replacement::<C, S>(store, page_number, header, edit)
}

fn apply_cells<C: Codec, D: PageEdit>(
    page: &mut D,
    header: &crate::slotted_page::Header,
    index: usize,
    cells: &[&[u8]],
) -> Result<()> {
    let old_len = page::codec_cell::<C, _>(page.view(), header, index)?.len();
    if !slotted_page::replace(page, header, index, old_len, cells[0])? {
        return Err(Error::Corrupt("B+tree replacement no longer fits"));
    }
    for (offset, cell) in cells[1..].iter().enumerate() {
        let current = parse::<C, _>(page.view(), u64::MAX, Some(header.level))?;
        if !slotted_page::insert(page, &current, index + offset + 1, *cell)? {
            return Err(Error::Corrupt(
                "B+tree replacement insertion no longer fits",
            ));
        }
    }
    Ok(())
}

fn split_replacement<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: Replacement<'_>,
) -> Result<Option<BranchSplit<C::Key>>> {
    let total = edit.total(header.item_count);
    let middle = store.inspect_page(page_number, |source| {
        page::replacement_split_index::<C, _>(source, header, edit)
    })?;
    let right_page = store.allocate()?;
    store.copy_page(page_number, right_page, |source, output| {
        build_replacement::<C, _, _>(source, header, edit, middle, total, output)
    })?;
    keep_left_replacement::<C, S>(store, page_number, header, edit, middle)?;
    crate::work::page_split(1);
    Ok(Some(BranchSplit {
        right_page,
        right_first: first_key::<C, S>(store, right_page, header.level)?,
        left_first: first_key::<C, S>(store, page_number, header.level)?,
        level: header.level,
    }))
}

fn keep_left_replacement<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: Replacement<'_>,
    middle: usize,
) -> Result<()> {
    store.update_page(page_number, |page| {
        if middle <= edit.index {
            page::truncate::<C, _>(page, header, middle)?;
            return Ok(());
        }
        if middle < edit.index + edit.cells.len() {
            let left = page::truncate::<C, _>(page, header, edit.index + 1)?;
            return apply_cells::<C, _>(
                page,
                &left,
                edit.index,
                &edit.cells[..middle - edit.index],
            );
        }
        let keep = middle - (edit.cells.len() - 1);
        let left = page::truncate::<C, _>(page, header, keep)?;
        apply_cells::<C, _>(page, &left, edit.index, edit.cells)
    })
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
    let txn = store.target_txn();
    store.update_page(page_number, |page| {
        let mut builder = Builder::new(page, C::BRANCH_TYPE, txn, level, C::AUX);
        builder.push(left.as_slice())?;
        builder.push(right.as_slice())?;
        builder.finish()
    })?;
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
        let frame = path.frame(depth);
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
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    let (header, child) = store.inspect_page(frame.page_number, |source| {
        let header = parse::<C, _>(source, target_txn, None)?;
        let child = branch_child::<C, _>(source, &header, frame.index, page_limit)?;
        Ok((header, child))
    })?;
    let replacement = CellBuf::branch::<C>(key, child)?;
    let cells = [replacement.as_slice()];
    let edit = Replacement {
        index: frame.index,
        cells: &cells,
    };
    apply_replacement::<C, S>(store, frame.page_number, &header, edit)
}
