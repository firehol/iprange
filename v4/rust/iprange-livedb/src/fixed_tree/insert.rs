//! Page-local insertion and split propagation.

use crate::contract::MAX_TREE_LEVEL;
use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::slotted_page::{self, Builder, PageEdit};

use super::page::{
    self, branch_child, build_edit, build_pair_edit, key_at, lower_bound, parse, require_codec,
    CellBuf, Edit, PairEdit,
};
use super::{
    private_path, private_path_select, Codec, Frame, LeafSelector, Path, PrivateLeaf, RetiredPages,
    Store,
};

struct BranchSplit<K> {
    right_page: u32,
    right_first: K,
    left_first: K,
    level: u16,
}

struct LeafTarget {
    path: Path,
    page_number: u32,
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
    if *root == 0 {
        *root = new_leaf::<C, S>(store, leaf_cell)?;
        return Ok(true);
    }
    let key = C::read_key(leaf_cell, 0)?;
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
    let mut selector = GapSelector::<C, F> {
        key,
        cell_len: leaf_cell.len(),
        accepts: &mut accepts,
        marker: std::marker::PhantomData,
    };
    let leaf = private_path_select::<C, S, _>(store, root, key, retired, &mut selector)?;
    if !retired.as_slice().is_empty() {
        return Err(Error::Corrupt("private B+tree contains a committed page"));
    }
    let GapDecision::Insert { index, fits } = leaf.selection else {
        return Ok(LocalInsert::General);
    };
    let target = LeafTarget {
        path: leaf.path,
        page_number: leaf.page_number,
        header: leaf.header,
        index,
        exists: false,
    };
    if fits {
        apply_leaf_edit::<C, S>(
            store,
            target.page_number,
            &target.header,
            Edit {
                index: target.index,
                replace: false,
                cell: leaf_cell,
            },
        )?;
        if target.index == 0 {
            propagate_first::<C, S>(store, root, &target.path, key)?;
        }
    } else {
        edit_leaf::<C, S>(store, root, leaf_cell, target)?;
    }
    Ok(LocalInsert::Inserted)
}

#[derive(Clone, Copy)]
enum GapDecision {
    General,
    Insert { index: usize, fits: bool },
}

struct GapSelector<'a, C: Codec, F> {
    key: C::Key,
    cell_len: usize,
    accepts: &'a mut F,
    marker: std::marker::PhantomData<C>,
}

impl<C, S, F> LeafSelector<C, S> for GapSelector<'_, C, F>
where
    C: Codec,
    S: Store,
    F: FnMut(Option<&[u8]>, Option<&[u8]>) -> Result<bool>,
{
    type Selection = GapDecision;

    fn select<'a>(
        &mut self,
        page: S::ReadPage<'a>,
        header: &crate::slotted_page::Header,
        path: &Path,
    ) -> Result<Self::Selection>
    where
        S: 'a,
    {
        let (index, exists) = lower_bound::<C, _>(page, header, self.key, true)?;
        if exists {
            return Ok(GapDecision::General);
        }
        let previous = if index > 0 {
            Some(super::read::copy_leaf::<C, _>(page, header, index - 1)?)
        } else if path.frames[..path.depth]
            .iter()
            .all(|frame| frame.index == 0)
        {
            None
        } else {
            return Ok(GapDecision::General);
        };
        let next = if index < header.item_count {
            Some(super::read::copy_leaf::<C, _>(page, header, index)?)
        } else if path.frames[..path.depth]
            .iter()
            .all(|frame| frame.index + 1 == frame.item_count)
        {
            None
        } else {
            return Ok(GapDecision::General);
        };
        if !(self.accepts)(
            previous.as_ref().map(super::LeafBuf::as_slice),
            next.as_ref().map(super::LeafBuf::as_slice),
        )? {
            return Ok(GapDecision::General);
        }
        Ok(GapDecision::Insert {
            index,
            fits: slotted_page::insert_fits(header, self.cell_len),
        })
    }
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
    let leaf = private_path::<C, S>(store, root, key, retired)?;
    inspect_leaf_target::<C, S>(store, leaf, key)
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
    let fits = store.inspect_page(target.page_number, |page| {
        page::edit_fits::<C, _>(page, &target.header, edit, 0, total)
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

fn apply_leaf_edit<C: Codec, S: Store>(
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

fn new_leaf<C: Codec, S: Store>(store: &mut S, cell: &[u8]) -> Result<u32> {
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
        let left = slotted_page::truncate(page, header, keep)?;
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
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    let (header, left_child) = store.inspect_page(frame.page_number, |source| {
        let header = parse::<C, _>(source, target_txn, None)?;
        let child = branch_child::<C, _>(source, &header, frame.index, page_limit)?;
        Ok((header, child))
    })?;
    let left = CellBuf::branch::<C>(left_first, left_child)?;
    let right = CellBuf::branch::<C>(right_first, right_page)?;
    let edit = PairEdit {
        index: frame.index,
        left: left.as_slice(),
        right: right.as_slice(),
    };
    apply_pair_edit::<C, S>(store, frame.page_number, &header, edit)
}

fn apply_pair_edit<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: PairEdit<'_>,
) -> Result<Option<BranchSplit<C::Key>>> {
    let fits = store.inspect_page(page_number, |source| {
        page::pair_fits::<C, _>(source, header, edit)
    })?;
    if fits {
        store.update_page(page_number, |page| apply_pair::<C, _>(page, header, edit))?;
        return Ok(None);
    }
    split_pair_edit::<C, S>(store, page_number, header, edit)
}

fn apply_pair<C: Codec, D: PageEdit>(
    page: &mut D,
    header: &crate::slotted_page::Header,
    edit: PairEdit<'_>,
) -> Result<()> {
    let old_len = C::branch_cell(page.view(), header, edit.index)?.len();
    if !slotted_page::replace(page, header, edit.index, old_len, edit.left)? {
        return Err(Error::Corrupt("B+tree pair replacement no longer fits"));
    }
    let current = parse::<C, _>(page.view(), u64::MAX, Some(header.level))?;
    if !slotted_page::insert(page, &current, edit.index + 1, edit.right)? {
        return Err(Error::Corrupt("B+tree pair insertion no longer fits"));
    }
    Ok(())
}

fn split_pair_edit<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: PairEdit<'_>,
) -> Result<Option<BranchSplit<C::Key>>> {
    let total = edit.total(header.item_count);
    let middle = store.inspect_page(page_number, |source| {
        page::pair_split_index::<C, _>(source, header, edit)
    })?;
    let right_page = store.allocate()?;
    store.copy_page(page_number, right_page, |source, output| {
        build_pair_edit::<C, _, _>(source, header, edit, middle, total, output)
    })?;
    keep_left_pair::<C, S>(store, page_number, header, edit, middle)?;
    crate::work::page_split(1);
    Ok(Some(BranchSplit {
        right_page,
        right_first: first_key::<C, S>(store, right_page, header.level)?,
        left_first: first_key::<C, S>(store, page_number, header.level)?,
        level: header.level,
    }))
}

fn keep_left_pair<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    header: &crate::slotted_page::Header,
    edit: PairEdit<'_>,
    middle: usize,
) -> Result<()> {
    store.update_page(page_number, |page| {
        if middle <= edit.index {
            slotted_page::truncate(page, header, middle)?;
            return Ok(());
        }
        if middle == edit.index + 1 {
            let left = slotted_page::truncate(page, header, middle)?;
            let old_len = C::branch_cell(page.view(), &left, edit.index)?.len();
            if slotted_page::replace(page, &left, edit.index, old_len, edit.left)? {
                return Ok(());
            }
            return Err(Error::Corrupt("B+tree split replacement no longer fits"));
        }
        let left = slotted_page::truncate(page, header, middle - 1)?;
        apply_pair::<C, _>(page, &left, edit)
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
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    let (header, child) = store.inspect_page(frame.page_number, |source| {
        let header = parse::<C, _>(source, target_txn, None)?;
        let child = branch_child::<C, _>(source, &header, frame.index, page_limit)?;
        Ok((header, child))
    })?;
    let replacement = CellBuf::branch::<C>(key, child)?;
    let edit = Edit {
        index: frame.index,
        replace: true,
        cell: replacement.as_slice(),
    };
    let fits = store.inspect_page(frame.page_number, |source| {
        page::edit_fits::<C, _>(source, &header, edit, 0, header.item_count)
    })?;
    if fits {
        apply_leaf_edit::<C, S>(store, frame.page_number, &header, edit)?;
        return Ok(None);
    }

    let middle = store.inspect_page(frame.page_number, |source| {
        page::split_index::<C, _>(source, &header, edit)
    })?;
    let right_page = store.allocate()?;
    store.copy_page(frame.page_number, right_page, |source, output| {
        build_edit::<C, _, _>(source, &header, edit, middle, header.item_count, output)
    })?;
    keep_left_edit::<C, S>(store, frame.page_number, &header, edit, middle)?;
    crate::work::page_split(1);
    Ok(Some(BranchSplit {
        right_page,
        right_first: first_key::<C, S>(store, right_page, header.level)?,
        left_first: first_key::<C, S>(store, frame.page_number, header.level)?,
        level: header.level,
    }))
}

fn first_key<C: Codec, S: Store>(store: &S, page_number: u32, level: u16) -> Result<C::Key> {
    let target_txn = store.target_txn();
    store.inspect_page(page_number, |page| {
        let header = parse::<C, _>(page, target_txn, Some(level))?;
        key_at::<C, _>(page, &header, 0)
    })
}
