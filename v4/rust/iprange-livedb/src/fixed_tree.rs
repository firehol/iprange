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
pub(crate) use insert::insert;
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
    fn allocate(&mut self) -> Result<u32>;
    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()>;
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
