//! Generic mapped-page COW B+tree.

use std::fmt;
use std::mem::MaybeUninit;

use crate::contract::MAX_TREE_LEVEL;
use crate::error::{Error, Result};
use crate::mapping::{ByteRange, ByteSource};
use crate::page_header;
use crate::page_io::{PageEdit, PageSink};
use crate::slotted_page::{self, Header};

mod cursor;
mod delete;
mod gap;
mod insert;
mod page;
mod query;
mod read;
mod walk;

pub(crate) use cursor::{
    Cursor, Direction as CursorDirection, Item as CursorItem, Seek as CursorSeek, SeekPosition,
};
#[cfg(test)]
pub(crate) use delete::delete;
pub(crate) use delete::delete_existing;
pub(crate) use delete::remove_leaf_run;
pub(crate) use gap::{
    insert_if_edge_gap, insert_if_local_gap, insert_rejected_gap, replace_local_predecessor_with,
    replace_local_run, root_position, Edge, EdgeInsert, LocalGap, LocalInsert, LocalNext,
    LocalPrevious, LocalReject, LocalRun, PrivatePosition,
};
pub(crate) use insert::{insert, replace_leaf_with};
use page::{branch_child, codec_cell, key_at, lower_bound, parse, CellBuf};
pub(crate) use query::{query, LeafQuery};
pub(crate) use read::{at_or_after, inspect_leaf, predecessor, predecessor_located, LeafLocation};
pub(crate) use walk::{discard_private_tree, retire_tree};

const MAX_PATH: usize = MAX_TREE_LEVEL as usize;

pub(crate) trait Codec {
    type Key: Copy + Ord + fmt::Debug;
    type Leaf: Copy;

    const BRANCH_TYPE: u8;
    const LEAF_TYPE: u8;
    const AUX: u32;
    const KEY_SIZE: usize;
    const LEAF_SIZE: usize;
    const MAX_BRANCH_SIZE: usize = Self::KEY_SIZE + 4;
    const MAX_LEAF_SIZE: usize = Self::LEAF_SIZE;

    fn read_key<S: ByteSource>(cell: S, level: u16) -> Result<Self::Key>;
    fn read_leaf<S: ByteSource>(cell: S) -> Result<Self::Leaf>;
    fn write_key(key: Self::Key, output: &mut [u8]);

    fn validate_leaf<S: ByteSource>(cell: S) -> Result<()> {
        crate::work::leaf_validation(1);
        Self::read_leaf(cell).map(drop)
    }

    fn fixed_cell_size(level: u16) -> Option<usize> {
        if level == 0 {
            (Self::LEAF_SIZE != 0).then_some(Self::LEAF_SIZE)
        } else {
            (Self::KEY_SIZE != 0 && Self::MAX_BRANCH_SIZE == Self::KEY_SIZE + 4)
                .then_some(Self::KEY_SIZE + 4)
        }
    }

    fn leaf_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        slotted_page::cell(page, header, index, Self::LEAF_SIZE)
    }

    fn branch_cell<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<ByteRange<S>> {
        slotted_page::cell(page, header, index, Self::KEY_SIZE + 4)
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

    fn read_branch_child<S: ByteSource>(cell: S) -> Result<u32> {
        cell.array(Self::KEY_SIZE)
            .map(u32::from_le_bytes)
            .ok_or_else(|| Error::corrupt("B+tree branch record is too short"))
    }
}

pub(crate) trait Store {
    type ReadPage<'a>: ByteSource
    where
        Self: 'a;
    type WritePage<'a>: PageEdit
    where
        Self: 'a;

    fn target_txn(&self) -> u64;
    fn page_limit(&self) -> u64;
    fn inspect_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>;
    fn allocate(&mut self) -> Result<u32>;
    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>;
    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>;
    fn discard_private(&mut self, page_number: u32) -> Result<()>;

    fn copy_for_cow(&mut self, source: u32, destination: u32) -> Result<()> {
        let target_txn = self.target_txn();
        self.copy_page(source, destination, |source, output| {
            output.write_source(0, source)?;
            output.put_u64(page_header::BORN_TXN, target_txn)?;
            crate::page_checksum::clear(output)
        })
    }
}

pub(crate) trait PageSource {
    type Page<'a>: ByteSource
    where
        Self: 'a;

    fn selected_txn(&self) -> u64;
    fn selected_page_limit(&self) -> u64;
    fn view_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::Page<'a>) -> Result<T>;
}

impl<T: Store> PageSource for T {
    type Page<'a>
        = T::ReadPage<'a>
    where
        Self: 'a;

    fn selected_txn(&self) -> u64 {
        Store::target_txn(self)
    }

    fn selected_page_limit(&self) -> u64 {
        Store::page_limit(self)
    }

    fn view_page<'a, R, F>(&'a self, page_number: u32, inspect: F) -> Result<R>
    where
        F: FnOnce(Self::Page<'a>) -> Result<R>,
    {
        Store::inspect_page(self, page_number, inspect)
    }
}

pub(crate) trait RetiringStore: Store {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()>;
}

pub(crate) fn insert_retiring<C: Codec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    record: &[u8],
) -> Result<bool> {
    let mut retired = RetiredPages::new();
    let inserted = insert::<C, S>(store, root, record, &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    Ok(inserted)
}

pub(crate) fn delete_retiring<C: Codec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
) -> Result<()> {
    let mut retired = RetiredPages::new();
    delete_existing::<C, S>(store, root, key, &mut retired)?;
    store.retire_pages(retired.as_slice())
}

fn first_key<C: Codec, S: Store>(store: &S, page_number: u32, level: u16) -> Result<C::Key> {
    let target_txn = store.target_txn();
    store.inspect_page(page_number, |page| {
        let header = parse::<C, _>(page, target_txn, Some(level))?;
        key_at::<C, _>(page, &header, 0)
    })
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
            .ok_or_else(|| Error::corrupt("COW path exceeds the maximum tree height"))?;
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

#[derive(Clone, Copy, Debug)]
pub(super) struct Frame {
    pub(super) page_number: u32,
    pub(super) index: usize,
    pub(super) item_count: usize,
}

pub(super) struct Path {
    frames: [MaybeUninit<Frame>; MAX_PATH],
    pub(super) depth: usize,
}

impl Path {
    pub(super) fn new() -> Self {
        Self {
            frames: [MaybeUninit::uninit(); MAX_PATH],
            depth: 0,
        }
    }

    #[inline]
    fn push(&mut self, frame: Frame) -> Result<()> {
        let slot = self
            .frames
            .get_mut(self.depth)
            .ok_or_else(|| Error::corrupt("B+tree exceeds its maximum height"))?;
        slot.write(frame);
        self.depth += 1;
        Ok(())
    }

    #[inline(always)]
    pub(super) fn as_slice(&self) -> &[Frame] {
        // SAFETY: push initializes exactly the prefix counted by depth.
        unsafe { std::slice::from_raw_parts(self.frames.as_ptr().cast(), self.depth) }
    }

    #[inline(always)]
    pub(super) fn frame(&self, index: usize) -> Frame {
        self.as_slice()[index]
    }
}

impl fmt::Debug for Path {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.debug_list().entries(self.as_slice()).finish()
    }
}

pub(super) struct PrivateLeaf<T> {
    pub(super) path: Path,
    pub(super) page_number: u32,
    pub(super) header: Header,
    pub(super) selection: T,
}

pub(super) trait LeafSelector<C: Codec, S: Store> {
    type Selection;

    fn select<'a>(
        &mut self,
        page: S::ReadPage<'a>,
        header: &Header,
        path: &Path,
    ) -> Result<Self::Selection>
    where
        S: 'a;
}

enum InspectedPage<T> {
    Leaf {
        header: Header,
        private: bool,
        selection: T,
    },
    Branch {
        header: Header,
        private: bool,
        index: usize,
        child: u32,
    },
}

pub(super) fn private_path<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<PrivateLeaf<(usize, bool)>> {
    private_path_select::<C, S, _>(store, root, key, retired, &mut KeySelector { key })
}

struct KeySelector<K> {
    key: K,
}

impl<C: Codec, S: Store> LeafSelector<C, S> for KeySelector<C::Key> {
    type Selection = (usize, bool);

    fn select<'a>(
        &mut self,
        page: S::ReadPage<'a>,
        header: &Header,
        _path: &Path,
    ) -> Result<Self::Selection>
    where
        S: 'a,
    {
        lower_bound::<C, _>(page, header, self.key, true)
    }
}

struct ExistingLeafSelector<C: Codec> {
    key: C::Key,
    codec: std::marker::PhantomData<C>,
}

struct ExistingLeaf<L> {
    index: usize,
    offset: usize,
    len: usize,
    value: L,
}

impl<C: Codec, S: Store> LeafSelector<C, S> for ExistingLeafSelector<C> {
    type Selection = ExistingLeaf<C::Leaf>;

    fn select<'a>(
        &mut self,
        page: S::ReadPage<'a>,
        header: &Header,
        _path: &Path,
    ) -> Result<Self::Selection>
    where
        S: 'a,
    {
        let (index, exact) = lower_bound::<C, _>(page, header, self.key, true)?;
        if !exact {
            return Err(Error::Corrupt("B+tree update key is missing"));
        }
        let cell = codec_cell::<C, _>(page, header, index)?;
        Ok(ExistingLeaf {
            index,
            offset: cell.source_offset(),
            len: cell.len(),
            value: C::read_leaf(cell)?,
        })
    }
}

pub(super) fn private_path_select<C, S, L>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
    selector: &mut L,
) -> Result<PrivateLeaf<L::Selection>>
where
    C: Codec,
    S: Store,
    L: LeafSelector<C, S>,
{
    crate::work::tree_lookup(1);
    let mut path = Path::new();
    let mut page_number = *root;
    let mut expected_level = None;
    let mut parent = None;

    loop {
        let inspected =
            inspect::<C, S, L>(store, page_number, expected_level, key, &path, selector)?;
        let (header, private) = match inspected {
            InspectedPage::Leaf {
                header, private, ..
            }
            | InspectedPage::Branch {
                header, private, ..
            } => (header, private),
        };
        let active_page = if private {
            page_number
        } else {
            let copied = touch(store, page_number, retired)?;
            if let Some((parent_page, parent_index)) = parent {
                replace_branch_child::<C, S>(store, parent_page, parent_index, copied)?;
            } else {
                *root = copied;
            }
            copied
        };

        match inspected {
            InspectedPage::Leaf { selection, .. } => {
                return Ok(PrivateLeaf {
                    path,
                    page_number: active_page,
                    header,
                    selection,
                });
            }
            InspectedPage::Branch { index, child, .. } => {
                path.push(Frame {
                    page_number: active_page,
                    index,
                    item_count: header.item_count,
                })?;
                parent = Some((active_page, index));
                page_number = child;
                expected_level = Some(header.level - 1);
                crate::work::tree_descent(1);
            }
        }
    }
}

pub(crate) enum LeafU64Mutation {
    Replace(u64),
    Delete,
}

pub(crate) fn mutate_leaf_u64<C, S, F>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    field_offset: usize,
    retired: &mut RetiredPages,
    decide: F,
) -> Result<C::Leaf>
where
    C: Codec,
    S: Store,
    F: FnOnce(C::Leaf) -> Result<LeafU64Mutation>,
{
    let mut selector = ExistingLeafSelector::<C> {
        key,
        codec: std::marker::PhantomData,
    };
    let leaf = private_path_select::<C, S, _>(store, root, key, retired, &mut selector)?;
    let selected = leaf.selection;
    match decide(selected.value)? {
        LeafU64Mutation::Replace(replacement) => {
            store.update_page(leaf.page_number, |page| {
                if match field_offset.checked_add(8) {
                    Some(end) => end > selected.len,
                    None => true,
                } {
                    return Err(Error::Corrupt("B+tree update field is outside its leaf"));
                }
                page.put_u64(selected.offset + field_offset, replacement)
            })?;
        }
        LeafU64Mutation::Delete => delete::delete_target::<C, S>(
            store,
            root,
            delete::Target {
                path: leaf.path,
                page_number: leaf.page_number,
                header: leaf.header,
                index: selected.index,
            },
        )?,
    }
    Ok(selected.value)
}

fn inspect<C, S, L>(
    store: &S,
    page_number: u32,
    expected_level: Option<u16>,
    key: C::Key,
    path: &Path,
    selector: &mut L,
) -> Result<InspectedPage<L::Selection>>
where
    C: Codec,
    S: Store,
    L: LeafSelector<C, S>,
{
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    store.inspect_page(page_number, |page| {
        let header = parse::<C, _>(page, target_txn, expected_level)?;
        let private = page_header::born_txn(page) == target_txn;
        if header.level == 0 {
            let selection = selector.select(page, &header, path)?;
            return Ok(InspectedPage::Leaf {
                header,
                private,
                selection,
            });
        }
        let (index, _) = lower_bound::<C, _>(page, &header, key, false)?;
        let child = branch_child::<C, _>(page, &header, index, page_limit)?;
        Ok(InspectedPage::Branch {
            header,
            private,
            index,
            child,
        })
    })
}

fn touch<S: Store>(store: &mut S, page_number: u32, retired: &mut RetiredPages) -> Result<u32> {
    let private_page = store.allocate()?;
    store.copy_for_cow(page_number, private_page)?;
    retired.push(page_number)?;
    Ok(private_page)
}

fn replace_branch_child<C: Codec, S: Store>(
    store: &mut S,
    page_number: u32,
    index: usize,
    child: u32,
) -> Result<()> {
    let target_txn = store.target_txn();
    let page_limit = store.page_limit();
    let (header, key, old_len) = store.inspect_page(page_number, |page| {
        let header = parse::<C, _>(page, target_txn, None)?;
        let key = key_at::<C, _>(page, &header, index)?;
        let old_len = C::branch_cell(page, &header, index)?.len();
        branch_child::<C, _>(page, &header, index, page_limit)?;
        Ok((header, key, old_len))
    })?;
    let replacement = CellBuf::branch::<C>(key, child)?;
    if replacement.as_slice().len() != old_len {
        return Err(Error::Corrupt("B+tree child replacement changed key size"));
    }
    store.update_page(page_number, |page| {
        if slotted_page::replace(page, &header, index, old_len, replacement.as_slice())? {
            Ok(())
        } else {
            Err(Error::Corrupt("B+tree child replacement no longer fits"))
        }
    })
}

#[cfg(test)]
#[path = "fixed_tree_tests.rs"]
mod tests;
