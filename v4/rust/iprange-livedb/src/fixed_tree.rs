//! Generic COW B+tree for fixed-width leaf records.

use std::fmt;

use crate::contract::{u64_le, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page::{Builder, Header};

mod delete;
mod page;
mod read;

pub(crate) use delete::delete;
use page::{
    branch_child, build_edit, copy_page, fits, key_at, lower_bound, parse, require_codec, CellBuf,
    Edit,
};
pub(crate) use read::{at_or_after, predecessor, LeafBuf};

const MAX_PATH: usize = MAX_TREE_LEVEL as usize;

pub(crate) trait Codec {
    type Key: Copy + Ord + fmt::Debug;

    const BRANCH_TYPE: u8;
    const LEAF_TYPE: u8;
    const AUX: u32;
    const KEY_SIZE: usize;
    const LEAF_SIZE: usize;

    fn read_key(cell: &[u8]) -> Self::Key;
    fn write_key(key: Self::Key, output: &mut [u8]);
    fn validate_leaf(cell: &[u8]) -> Result<()>;
}

pub(crate) trait Store {
    fn target_txn(&self) -> u64;
    fn page_limit(&self) -> u64;
    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()>;
    fn allocate(&mut self) -> Result<u32>;
    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()>;
    fn discard_private(&mut self, page_number: u32) -> Result<()>;
}

#[derive(Debug)]
pub(crate) struct RetiredPages {
    pages: [u32; MAX_PATH + 1],
    len: usize,
}

impl RetiredPages {
    pub(crate) const fn new() -> Self {
        Self {
            pages: [0; MAX_PATH + 1],
            len: 0,
        }
    }

    pub(crate) fn push(&mut self, page_number: u32) -> Result<()> {
        let slot = self
            .pages
            .get_mut(self.len)
            .ok_or(Error::Corrupt("COW path exceeds the maximum tree height"))?;
        *slot = page_number;
        self.len += 1;
        Ok(())
    }

    pub(crate) fn as_slice(&self) -> &[u32] {
        &self.pages[..self.len]
    }

    pub(crate) fn extend(&mut self, pages: &[u32]) -> Result<()> {
        for &page_number in pages {
            self.push(page_number)?;
        }
        Ok(())
    }
}

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    index: usize,
}

struct BranchSplit<K> {
    right_page: u32,
    right_first: K,
    left_first: K,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    index: 0,
};

struct Path {
    frames: [Frame; MAX_PATH],
    depth: usize,
}

impl Path {
    fn new() -> Self {
        Self {
            frames: [EMPTY_FRAME; MAX_PATH],
            depth: 0,
        }
    }

    fn push(&mut self, frame: Frame) -> Result<()> {
        let slot = self
            .frames
            .get_mut(self.depth)
            .ok_or(Error::Corrupt("B+tree exceeds its maximum height"))?;
        *slot = frame;
        self.depth += 1;
        Ok(())
    }
}

pub(crate) fn insert<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    retired: &mut RetiredPages,
) -> Result<bool> {
    require_codec::<C>()?;
    if leaf_cell.len() != C::LEAF_SIZE {
        return Err(Error::InvalidArgument("wrong fixed B+tree leaf size"));
    }
    C::validate_leaf(leaf_cell)?;
    let key = C::read_key(leaf_cell);
    if *root == 0 {
        *root = new_leaf::<C, S>(store, leaf_cell)?;
        return Ok(true);
    }

    let (path, leaf_page) = private_path::<C, S>(store, root, key, retired)?;
    let mut source = [0; PAGE_SIZE];
    store.read(leaf_page, &mut source)?;
    let header = parse::<C>(&source, store.target_txn(), Some(0))?;
    let (index, exists) = lower_bound::<C>(&source, &header, key, true)?;
    let edit = Edit {
        index,
        replace: exists,
        cell: leaf_cell,
    };

    if fits(edit.total(header.item_count), C::LEAF_SIZE) {
        let mut output = [0; PAGE_SIZE];
        build_edit::<C>(
            &source,
            &header,
            edit,
            0,
            edit.total(header.item_count),
            &mut output,
        )?;
        store.write(leaf_page, &output)?;
        if index == 0 {
            propagate_first::<C, S>(store, &path, key)?;
        }
        return Ok(!exists);
    }

    split_leaf::<C, S>(store, root, &path, leaf_page, &source, &header, edit)
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

fn private_path<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<(Path, u32)> {
    let (mut page_number, mut page, mut header) = touch::<C, S>(store, *root, None, retired)?;
    *root = page_number;
    let mut path = Path::new();

    while header.level > 0 {
        let (index, _) = lower_bound::<C>(&page, &header, key, false)?;
        let child = branch_child::<C>(&page, &header, index, store.page_limit())?;
        path.push(Frame { page_number, index })?;
        let expected = Some(header.level - 1);
        let (private_child, child_page, child_header) =
            touch::<C, S>(store, child, expected, retired)?;
        if private_child != child {
            replace_branch::<C, S>(store, page_number, index, None, private_child)?;
        }
        page_number = private_child;
        page = child_page;
        header = child_header;
    }
    Ok((path, page_number))
}

fn touch<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    expected_level: Option<u16>,
    retired: &mut RetiredPages,
) -> Result<(u32, [u8; PAGE_SIZE], Header)> {
    let mut source = [0; PAGE_SIZE];
    store.read(page_number, &mut source)?;
    let header = parse::<C>(&source, store.target_txn(), expected_level)?;
    if u64_le(&source, 8) == store.target_txn() {
        return Ok((page_number, source, header));
    }

    let private_page = store.allocate()?;
    let mut output = [0; PAGE_SIZE];
    copy_page::<C>(
        &source,
        &header,
        store.target_txn(),
        store.page_limit(),
        &mut output,
    )?;
    store.write(private_page, &output)?;
    retired.push(page_number)?;
    Ok((private_page, output, header))
}

fn split_leaf<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    leaf_page: u32,
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: Edit<'_>,
) -> Result<bool> {
    let total = edit.total(header.item_count);
    let middle = total / 2;
    let mut left = [0; PAGE_SIZE];
    let mut right = [0; PAGE_SIZE];
    build_edit::<C>(source, header, edit, 0, middle, &mut left)?;
    build_edit::<C>(source, header, edit, middle, total, &mut right)?;
    let right_page = store.allocate()?;
    store.write(leaf_page, &left)?;
    store.write(right_page, &right)?;

    let left_header = parse::<C>(&left, store.target_txn(), Some(0))?;
    let right_header = parse::<C>(&right, store.target_txn(), Some(0))?;
    let left_first = key_at::<C>(&left, &left_header, 0)?;
    let right_first = key_at::<C>(&right, &right_header, 0)?;
    propagate_split::<C, S>(
        store,
        root,
        path,
        leaf_page,
        left_first,
        right_page,
        right_first,
        0,
    )?;
    Ok(true)
}

#[allow(clippy::too_many_arguments)]
fn propagate_split<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    mut left_page: u32,
    mut left_first: C::Key,
    mut right_page: u32,
    mut right_first: C::Key,
    mut child_level: u16,
) -> Result<()> {
    let mut depth = path.depth;
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
                propagate_first_from::<C, S>(store, path, depth, left_first)?;
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

    let mut replaced = [0; PAGE_SIZE];
    let replace = Edit {
        index: frame.index,
        replace: true,
        cell: left.as_slice(),
    };
    build_edit::<C>(
        &source,
        &header,
        replace,
        0,
        header.item_count,
        &mut replaced,
    )?;
    let replaced_header = parse::<C>(&replaced, store.target_txn(), Some(header.level))?;
    let insert = Edit {
        index: frame.index + 1,
        replace: false,
        cell: right.as_slice(),
    };
    let total = header.item_count + 1;
    if fits(total, C::KEY_SIZE + 4) {
        let mut output = [0; PAGE_SIZE];
        build_edit::<C>(&replaced, &replaced_header, insert, 0, total, &mut output)?;
        store.write(frame.page_number, &output)?;
        return Ok(None);
    }

    let middle = total / 2;
    let mut left_page = [0; PAGE_SIZE];
    let mut right_page = [0; PAGE_SIZE];
    build_edit::<C>(
        &replaced,
        &replaced_header,
        insert,
        0,
        middle,
        &mut left_page,
    )?;
    build_edit::<C>(
        &replaced,
        &replaced_header,
        insert,
        middle,
        total,
        &mut right_page,
    )?;
    let new_right = store.allocate()?;
    store.write(frame.page_number, &left_page)?;
    store.write(new_right, &right_page)?;
    let left_header = parse::<C>(&left_page, store.target_txn(), Some(header.level))?;
    let right_header = parse::<C>(&right_page, store.target_txn(), Some(header.level))?;
    Ok(Some(BranchSplit {
        right_page: new_right,
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

fn propagate_first<C: Codec, S: Store>(store: &mut S, path: &Path, key: C::Key) -> Result<()> {
    propagate_first_from::<C, S>(store, path, path.depth, key)
}

fn propagate_first_from<C: Codec, S: Store>(
    store: &mut S,
    path: &Path,
    mut depth: usize,
    key: C::Key,
) -> Result<()> {
    while depth > 0 {
        depth -= 1;
        let frame = path.frames[depth];
        replace_branch::<C, S>(store, frame.page_number, frame.index, Some(key), 0)?;
        if frame.index != 0 {
            break;
        }
    }
    Ok(())
}

fn replace_branch<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    index: usize,
    key: Option<C::Key>,
    child: u32,
) -> Result<()> {
    let mut source = [0; PAGE_SIZE];
    store.read(page_number, &mut source)?;
    let header = parse::<C>(&source, store.target_txn(), None)?;
    let old_key = key_at::<C>(&source, &header, index)?;
    let key = key.unwrap_or(old_key);
    let child = if child == 0 {
        branch_child::<C>(&source, &header, index, store.page_limit())?
    } else {
        child
    };
    let replacement = CellBuf::branch::<C>(key, child)?;
    let edit = Edit {
        index,
        replace: true,
        cell: replacement.as_slice(),
    };
    let mut output = [0; PAGE_SIZE];
    build_edit::<C>(&source, &header, edit, 0, header.item_count, &mut output)?;
    store.write(page_number, &output)
}

#[cfg(test)]
#[path = "fixed_tree_tests.rs"]
mod tests;
