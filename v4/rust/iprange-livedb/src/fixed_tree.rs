//! Generic slotted-page COW B+tree.

use std::fmt;

use crate::contract::{u64_le, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page::Header;

mod delete;
mod insert;
mod page;
mod read;
mod walk;

pub(crate) use delete::delete;
pub(crate) use insert::{insert, insert_if_local_gap, LocalInsert};
use page::{branch_child, build_edit, copy_page, key_at, lower_bound, parse, CellBuf, Edit};
pub(crate) use read::{at_or_after, predecessor, LeafBuf};
pub(crate) use walk::{discard_private_tree, retire_tree};

const MAX_PATH: usize = MAX_TREE_LEVEL as usize;

pub(crate) trait Codec {
    type Key: Copy + Ord + fmt::Debug;

    const BRANCH_TYPE: u8;
    const LEAF_TYPE: u8;
    const AUX: u32;
    const KEY_SIZE: usize;
    const LEAF_SIZE: usize;
    const MAX_BRANCH_SIZE: usize = Self::KEY_SIZE + 4;
    const MAX_LEAF_SIZE: usize = Self::LEAF_SIZE;

    fn read_key(cell: &[u8], level: u16) -> Result<Self::Key>;
    fn write_key(key: Self::Key, output: &mut [u8]);
    fn validate_leaf(cell: &[u8]) -> Result<()>;

    fn leaf_cell<'a>(page: &'a [u8; PAGE_SIZE], header: &Header, index: usize) -> Result<&'a [u8]> {
        crate::slotted_page::cell(page, header, index, Self::LEAF_SIZE)
    }

    fn branch_cell<'a>(
        page: &'a [u8; PAGE_SIZE],
        header: &Header,
        index: usize,
    ) -> Result<&'a [u8]> {
        crate::slotted_page::cell(page, header, index, Self::KEY_SIZE + 4)
    }

    fn write_branch(key: Self::Key, child: u32, output: &mut [u8]) -> Result<usize> {
        let len = Self::KEY_SIZE + 4;
        if output.len() < len {
            return Err(Error::Unsupported("B+tree branch buffer is too small"));
        }
        Self::write_key(key, &mut output[..Self::KEY_SIZE]);
        output[Self::KEY_SIZE..len].copy_from_slice(&child.to_le_bytes());
        Ok(len)
    }

    fn read_branch_child(cell: &[u8]) -> Result<u32> {
        cell.get(Self::KEY_SIZE..Self::KEY_SIZE + 4)
            .and_then(|bytes| bytes.try_into().ok())
            .map(u32::from_le_bytes)
            .ok_or(Error::Corrupt("B+tree branch record is too short"))
    }
}

pub(crate) trait Store {
    fn target_txn(&self) -> u64;
    fn page_limit(&self) -> u64;
    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()>;
    fn inspect_page<T, F>(&self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(&[u8; PAGE_SIZE]) -> Result<T>,
    {
        let mut page = [0; PAGE_SIZE];
        self.read(page_number, &mut page)?;
        inspect(&page)
    }
    fn allocate(&mut self) -> Result<u32>;
    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()>;
    fn update_page<F>(&mut self, page_number: u32, update: F) -> Result<()>
    where
        F: FnOnce(&mut [u8; PAGE_SIZE]) -> Result<()>,
    {
        let mut page = [0; PAGE_SIZE];
        self.read(page_number, &mut page)?;
        update(&mut page)?;
        self.write(page_number, &page)
    }
    fn discard_private(&mut self, page_number: u32) -> Result<()>;
}

pub(crate) trait RetiringStore: Store {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()>;
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
    item_count: usize,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    index: 0,
    item_count: 0,
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

struct PrivateTraversal {
    page_number: u32,
    expected_level: Option<u16>,
    parent: Option<(u32, usize)>,
    path: Path,
}

impl PrivateTraversal {
    fn new(root: u32) -> Self {
        Self {
            page_number: root,
            expected_level: None,
            parent: None,
            path: Path::new(),
        }
    }

    fn descend(
        &mut self,
        page_number: u32,
        level: u16,
        index: usize,
        item_count: usize,
        child: u32,
    ) -> Result<()> {
        self.path.push(Frame {
            page_number,
            index,
            item_count,
        })?;
        self.parent = Some((page_number, index));
        self.page_number = child;
        self.expected_level = Some(level - 1);
        Ok(())
    }

    fn take_path(&mut self) -> Path {
        std::mem::replace(&mut self.path, Path::new())
    }
}

enum LeafChoice<T> {
    Return(T),
    Copy,
}

struct PrivateLeaf<T> {
    path: Path,
    page_number: u32,
    header: Header,
    page: Option<[u8; PAGE_SIZE]>,
    selection: Option<T>,
}

enum InspectedPage<T> {
    Committed,
    Branch {
        level: u16,
        index: usize,
        item_count: usize,
        child: u32,
    },
    Leaf {
        header: Header,
        choice: LeafChoice<T>,
    },
}

fn private_path<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<(Path, u32, [u8; PAGE_SIZE], Header)> {
    let leaf = private_path_select::<C, S, (), _>(store, root, key, retired, |_, _, _| {
        Ok(LeafChoice::Copy)
    })?;
    let Some(page) = leaf.page else {
        unreachable!("copy-only private path returned a leaf selection")
    };
    Ok((leaf.path, leaf.page_number, page, leaf.header))
}

fn private_path_select<C, S, T, F>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
    mut select: F,
) -> Result<PrivateLeaf<T>>
where
    C: Codec,
    S: Store,
    F: FnMut(&[u8; PAGE_SIZE], &Header, &Path) -> Result<LeafChoice<T>>,
{
    let mut traversal = PrivateTraversal::new(*root);

    loop {
        match inspect_page::<C, S, T, F>(
            store,
            traversal.page_number,
            traversal.expected_level,
            key,
            &traversal.path,
            &mut select,
        )? {
            InspectedPage::Committed => {
                if let Some(leaf) = advance_committed::<C, S, T, F>(
                    store,
                    root,
                    key,
                    retired,
                    &mut traversal,
                    &mut select,
                )? {
                    return Ok(leaf);
                }
            }
            InspectedPage::Branch {
                level,
                index,
                item_count,
                child,
            } => {
                traversal.descend(traversal.page_number, level, index, item_count, child)?;
            }
            InspectedPage::Leaf { header, choice } => {
                let page_number = traversal.page_number;
                return finish_leaf(&mut traversal, page_number, header, choice, || {
                    let mut page = [0; PAGE_SIZE];
                    store.read(page_number, &mut page)?;
                    Ok(page)
                });
            }
        }
    }
}

fn advance_committed<C, S, T, F>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
    traversal: &mut PrivateTraversal,
    select: &mut F,
) -> Result<Option<PrivateLeaf<T>>>
where
    C: Codec,
    S: Store,
    F: FnMut(&[u8; PAGE_SIZE], &Header, &Path) -> Result<LeafChoice<T>>,
{
    let (private_page, page, header) = touch::<C, S>(
        store,
        traversal.page_number,
        traversal.expected_level,
        retired,
    )?;
    publish_touched_page::<C, S>(store, root, traversal.parent, private_page)?;
    if header.level == 0 {
        return select_committed_leaf(traversal, private_page, page, header, select).map(Some);
    }
    descend_committed_branch::<C, S>(store, traversal, key, private_page, &page, &header)?;
    Ok(None)
}

fn publish_touched_page<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    parent: Option<(u32, usize)>,
    private_page: u32,
) -> Result<()> {
    if let Some((parent_page, parent_index)) = parent {
        replace_branch::<C, S>(store, parent_page, parent_index, None, private_page)?;
    } else {
        *root = private_page;
    }
    Ok(())
}

fn select_committed_leaf<T, F>(
    traversal: &mut PrivateTraversal,
    page_number: u32,
    page: [u8; PAGE_SIZE],
    header: Header,
    select: &mut F,
) -> Result<PrivateLeaf<T>>
where
    F: FnMut(&[u8; PAGE_SIZE], &Header, &Path) -> Result<LeafChoice<T>>,
{
    let choice = select(&page, &header, &traversal.path)?;
    finish_leaf(traversal, page_number, header, choice, || Ok(page))
}

fn descend_committed_branch<C: Codec, S: Store>(
    store: &S,
    traversal: &mut PrivateTraversal,
    key: C::Key,
    private_page: u32,
    page: &[u8; PAGE_SIZE],
    header: &Header,
) -> Result<()> {
    let (index, _) = lower_bound::<C>(page, header, key, false)?;
    let child = branch_child::<C>(page, header, index, store.page_limit())?;
    traversal.descend(private_page, header.level, index, header.item_count, child)
}

fn finish_leaf<T, F>(
    traversal: &mut PrivateTraversal,
    page_number: u32,
    header: Header,
    choice: LeafChoice<T>,
    load: F,
) -> Result<PrivateLeaf<T>>
where
    F: FnOnce() -> Result<[u8; PAGE_SIZE]>,
{
    let (page, selection) = match choice {
        LeafChoice::Return(selection) => (None, Some(selection)),
        LeafChoice::Copy => (Some(load()?), None),
    };
    Ok(PrivateLeaf {
        path: traversal.take_path(),
        page_number,
        header,
        page,
        selection,
    })
}

fn inspect_page<C, S, T, F>(
    store: &S,
    page_number: u32,
    expected_level: Option<u16>,
    key: C::Key,
    path: &Path,
    select: &mut F,
) -> Result<InspectedPage<T>>
where
    C: Codec,
    S: Store,
    F: FnMut(&[u8; PAGE_SIZE], &Header, &Path) -> Result<LeafChoice<T>>,
{
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    store.inspect_page(page_number, |page| {
        let header = parse::<C>(page, target_txn, expected_level)?;
        if u64_le(page, 8) != target_txn {
            return Ok(InspectedPage::Committed);
        }
        if header.level == 0 {
            return Ok(InspectedPage::Leaf {
                header,
                choice: select(page, &header, path)?,
            });
        }
        let (index, _) = lower_bound::<C>(page, &header, key, false)?;
        let child = branch_child::<C>(page, &header, index, page_limit)?;
        Ok(InspectedPage::Branch {
            level: header.level,
            index,
            item_count: header.item_count,
            child,
        })
    })
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
