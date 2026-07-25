//! Fixed-width page encoding and search.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page::{self, Builder, Header};

use super::Codec;

const MAX_CELL_SIZE: usize = 64;

pub(super) struct CellBuf {
    bytes: [u8; MAX_CELL_SIZE],
    len: usize,
}

impl CellBuf {
    pub(super) fn branch<C: Codec>(key: C::Key, child: u32) -> Result<Self> {
        let len = C::KEY_SIZE + 4;
        if len > MAX_CELL_SIZE {
            return Err(Error::Unsupported("fixed B+tree key is too large"));
        }
        let mut cell = Self {
            bytes: [0; MAX_CELL_SIZE],
            len,
        };
        C::write_key(key, &mut cell.bytes[..C::KEY_SIZE]);
        cell.bytes[C::KEY_SIZE..len].copy_from_slice(&child.to_le_bytes());
        Ok(cell)
    }

    pub(super) fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

#[derive(Clone, Copy)]
pub(super) struct Edit<'a> {
    pub(super) index: usize,
    pub(super) replace: bool,
    pub(super) cell: &'a [u8],
}

impl Edit<'_> {
    pub(super) fn total(&self, source_count: usize) -> usize {
        source_count + usize::from(!self.replace)
    }
}

pub(super) fn build_edit<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: Edit<'_>,
    start: usize,
    end: usize,
    output: &mut [u8; PAGE_SIZE],
) -> Result<()> {
    let page_type = page_type::<C>(header.level);
    let cell_size = cell_size::<C>(header.level);
    let mut builder = Builder::new(output, page_type, u64_le(source, 8), header.level, C::AUX);
    for virtual_index in start..end {
        if virtual_index == edit.index {
            builder.push(edit.cell)?;
            continue;
        }
        let source_index = if virtual_index > edit.index && !edit.replace {
            virtual_index - 1
        } else {
            virtual_index
        };
        builder.push(slotted_page::cell(source, header, source_index, cell_size)?)?;
    }
    builder.finish()
}

pub(super) fn build_remove<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    removed: usize,
    output: &mut [u8; PAGE_SIZE],
) -> Result<()> {
    if removed >= header.item_count || header.item_count <= 1 {
        return Err(Error::InvalidArgument(
            "fixed B+tree removal would create an empty page",
        ));
    }
    let mut builder = Builder::new(
        output,
        page_type::<C>(header.level),
        u64_le(source, 8),
        header.level,
        C::AUX,
    );
    let size = cell_size::<C>(header.level);
    for index in 0..header.item_count {
        if index != removed {
            builder.push(slotted_page::cell(source, header, index, size)?)?;
        }
    }
    builder.finish()
}

pub(super) fn copy_page<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    target_txn: u64,
    page_limit: u64,
    output: &mut [u8; PAGE_SIZE],
) -> Result<()> {
    let mut builder = Builder::new(
        output,
        page_type::<C>(header.level),
        target_txn,
        header.level,
        C::AUX,
    );
    let size = cell_size::<C>(header.level);
    let mut previous = None;
    for index in 0..header.item_count {
        let cell = slotted_page::cell(source, header, index, size)?;
        let key = C::read_key(cell);
        if previous.is_some_and(|prior| prior >= key) {
            return Err(Error::Corrupt("B+tree page keys are not increasing"));
        }
        if header.level == 0 {
            C::validate_leaf(cell)?;
        } else {
            require_child::<C>(cell, page_limit)?;
        }
        previous = Some(key);
        builder.push(cell)?;
    }
    builder.finish()
}

pub(super) fn parse<C: Codec>(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<Header> {
    let level = u16_le(page, 18);
    slotted_page::parse(
        page,
        selected_txn,
        page_type::<C>(level),
        C::AUX,
        expected_level,
    )
}

pub(super) fn lower_bound<C: Codec>(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    key: C::Key,
    insertion: bool,
) -> Result<(usize, bool)> {
    let size = cell_size::<C>(header.level);
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        if key_at_sized::<C>(page, header, middle, size)? < key {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    let exists = lower < header.item_count && key_at_sized::<C>(page, header, lower, size)? == key;
    if insertion || exists || lower == 0 {
        Ok((lower, exists))
    } else {
        Ok((lower - 1, false))
    }
}

pub(super) fn key_at<C: Codec>(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    index: usize,
) -> Result<C::Key> {
    key_at_sized::<C>(page, header, index, cell_size::<C>(header.level))
}

pub(super) fn branch_child<C: Codec>(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    let cell = slotted_page::cell(page, header, index, C::KEY_SIZE + 4)?;
    require_child::<C>(cell, page_limit)
}

pub(super) fn fits(item_count: usize, cell_size: usize) -> bool {
    slotted_page::HEADER_SIZE
        .checked_add(item_count.saturating_mul(2 + cell_size))
        .is_some_and(|used| used <= PAGE_SIZE)
}

pub(super) fn require_codec<C: Codec>() -> Result<()> {
    if C::KEY_SIZE == 0
        || C::LEAF_SIZE < C::KEY_SIZE
        || C::KEY_SIZE + 4 > MAX_CELL_SIZE
        || C::LEAF_SIZE > MAX_CELL_SIZE
    {
        return Err(Error::Unsupported("invalid fixed B+tree codec"));
    }
    Ok(())
}

fn key_at_sized<C: Codec>(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    size: usize,
) -> Result<C::Key> {
    Ok(C::read_key(slotted_page::cell(page, header, index, size)?))
}

fn require_child<C: Codec>(cell: &[u8], page_limit: u64) -> Result<u32> {
    let child = u32_le(cell, C::KEY_SIZE);
    if child < 2 || u64::from(child) >= page_limit {
        return Err(Error::Corrupt("B+tree child page is invalid"));
    }
    Ok(child)
}

fn page_type<C: Codec>(level: u16) -> u8 {
    if level == 0 {
        C::LEAF_TYPE
    } else {
        C::BRANCH_TYPE
    }
}

fn cell_size<C: Codec>(level: u16) -> usize {
    if level == 0 {
        C::LEAF_SIZE
    } else {
        C::KEY_SIZE + 4
    }
}
