//! Codec-driven page encoding and search.

use crate::contract::{u16_le, u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page::{self, Builder, Header};

use super::Codec;

pub(super) struct CellBuf {
    bytes: [u8; PAGE_SIZE],
    len: usize,
}

impl CellBuf {
    pub(super) fn branch<C: Codec>(key: C::Key, child: u32) -> Result<Self> {
        let mut cell = Self {
            bytes: [0; PAGE_SIZE],
            len: 0,
        };
        cell.len = C::write_branch(key, child, &mut cell.bytes)?;
        if cell.len == 0 || cell.len > C::MAX_BRANCH_SIZE {
            return Err(Error::Unsupported("B+tree branch encoding is invalid"));
        }
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

#[derive(Clone, Copy)]
pub(super) struct PairEdit<'a> {
    pub(super) index: usize,
    pub(super) left: &'a [u8],
    pub(super) right: &'a [u8],
}

impl PairEdit<'_> {
    pub(super) fn total(&self, source_count: usize) -> usize {
        source_count + 1
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
    let mut builder = Builder::new(output, page_type, u64_le(source, 8), header.level, C::AUX);
    for virtual_index in start..end {
        builder.push(virtual_cell::<C>(source, header, edit, virtual_index)?)?;
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
    for index in 0..header.item_count {
        if index != removed {
            builder.push(codec_cell::<C>(source, header, index)?)?;
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
    let mut previous = None;
    for index in 0..header.item_count {
        let cell = codec_cell::<C>(source, header, index)?;
        let key = C::read_key(cell, header.level)?;
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
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        if key_at::<C>(page, header, middle)? < key {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    let exists = lower < header.item_count && key_at::<C>(page, header, lower)? == key;
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
    C::read_key(codec_cell::<C>(page, header, index)?, header.level)
}

pub(super) fn branch_child<C: Codec>(
    page: &[u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    let cell = C::branch_cell(page, header, index)?;
    require_child::<C>(cell, page_limit)
}

pub(super) fn edit_fits<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: Edit<'_>,
    start: usize,
    end: usize,
) -> Result<bool> {
    Ok(encoded_size::<C>(source, header, edit, start, end)? <= PAGE_SIZE)
}

pub(super) fn split_index<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: Edit<'_>,
) -> Result<usize> {
    let total = edit.total(header.item_count);
    split_by_size(total, |index| {
        Ok(virtual_cell::<C>(source, header, edit, index)?.len())
    })
}

pub(super) fn build_pair_edit<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: PairEdit<'_>,
    start: usize,
    end: usize,
    output: &mut [u8; PAGE_SIZE],
) -> Result<()> {
    let mut builder = Builder::new(
        output,
        page_type::<C>(header.level),
        u64_le(source, 8),
        header.level,
        C::AUX,
    );
    for index in start..end {
        builder.push(pair_cell::<C>(source, header, edit, index)?)?;
    }
    builder.finish()
}

pub(super) fn pair_fits<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: PairEdit<'_>,
) -> Result<bool> {
    Ok(pair_size::<C>(source, header, edit, 0, edit.total(header.item_count))? <= PAGE_SIZE)
}

pub(super) fn pair_split_index<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: PairEdit<'_>,
) -> Result<usize> {
    let total = edit.total(header.item_count);
    split_by_size(total, |index| {
        Ok(pair_cell::<C>(source, header, edit, index)?.len())
    })
}

pub(super) fn require_codec<C: Codec>() -> Result<()> {
    if C::MAX_BRANCH_SIZE == 0
        || C::MAX_LEAF_SIZE == 0
        || C::MAX_BRANCH_SIZE + 2 + slotted_page::HEADER_SIZE > PAGE_SIZE
        || C::MAX_LEAF_SIZE + 2 + slotted_page::HEADER_SIZE > PAGE_SIZE
    {
        return Err(Error::Unsupported("invalid B+tree codec"));
    }
    Ok(())
}

fn encoded_size<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: Edit<'_>,
    start: usize,
    end: usize,
) -> Result<usize> {
    let count = end
        .checked_sub(start)
        .ok_or(Error::Corrupt("B+tree page range is reversed"))?;
    let payload = (start..end).try_fold(0usize, |used, index| {
        used.checked_add(virtual_cell::<C>(source, header, edit, index)?.len())
            .ok_or(Error::ArithmeticOverflow("B+tree page size"))
    })?;
    page_size(count, payload)
}

fn page_size(count: usize, payload: usize) -> Result<usize> {
    count
        .checked_mul(2)
        .and_then(|slots| slots.checked_add(slotted_page::HEADER_SIZE))
        .and_then(|base| base.checked_add(payload))
        .ok_or(Error::ArithmeticOverflow("B+tree page size"))
}

fn pair_size<C: Codec>(
    source: &[u8; PAGE_SIZE],
    header: &Header,
    edit: PairEdit<'_>,
    start: usize,
    end: usize,
) -> Result<usize> {
    let count = end
        .checked_sub(start)
        .ok_or(Error::Corrupt("B+tree page range is reversed"))?;
    let payload = (start..end).try_fold(0usize, |used, index| {
        used.checked_add(pair_cell::<C>(source, header, edit, index)?.len())
            .ok_or(Error::ArithmeticOverflow("B+tree page size"))
    })?;
    page_size(count, payload)
}

fn split_by_size<F>(total: usize, mut cell_len: F) -> Result<usize>
where
    F: FnMut(usize) -> Result<usize>,
{
    if total < 2 {
        return Err(Error::Corrupt("B+tree split has fewer than two records"));
    }
    let payload = payload_size(total, &mut cell_len)?;
    choose_split(total, payload, &mut cell_len)?
        .ok_or(Error::InvalidArgument("B+tree record cannot be split"))
}

fn payload_size<F>(total: usize, cell_len: &mut F) -> Result<usize>
where
    F: FnMut(usize) -> Result<usize>,
{
    (0..total).try_fold(0usize, |used, index| {
        used.checked_add(cell_len(index)?)
            .ok_or(Error::ArithmeticOverflow("B+tree split size"))
    })
}

fn choose_split<F>(total: usize, payload: usize, cell_len: &mut F) -> Result<Option<usize>>
where
    F: FnMut(usize) -> Result<usize>,
{
    let mut left_payload = 0usize;
    let mut best = None;
    for middle in 1..total {
        left_payload = left_payload
            .checked_add(cell_len(middle - 1)?)
            .ok_or(Error::ArithmeticOverflow("B+tree split size"))?;
        let Some(difference) = split_difference(total, middle, payload, left_payload)? else {
            continue;
        };
        if best.map_or(true, |(_, prior)| difference < prior) {
            best = Some((middle, difference));
        }
    }
    Ok(best.map(|(middle, _)| middle))
}

fn split_difference(
    total: usize,
    middle: usize,
    payload: usize,
    left_payload: usize,
) -> Result<Option<usize>> {
    let left = page_size(middle, left_payload)?;
    let right_payload = payload
        .checked_sub(left_payload)
        .ok_or(Error::Corrupt("B+tree split size changed"))?;
    let right = page_size(total - middle, right_payload)?;
    if left > PAGE_SIZE || right > PAGE_SIZE {
        return Ok(None);
    }
    Ok(Some(left.abs_diff(right)))
}

fn virtual_cell<'a, C: Codec>(
    source: &'a [u8; PAGE_SIZE],
    header: &Header,
    edit: Edit<'a>,
    virtual_index: usize,
) -> Result<&'a [u8]> {
    if virtual_index == edit.index {
        return Ok(edit.cell);
    }
    let source_index = if virtual_index > edit.index && !edit.replace {
        virtual_index - 1
    } else {
        virtual_index
    };
    codec_cell::<C>(source, header, source_index)
}

fn pair_cell<'a, C: Codec>(
    source: &'a [u8; PAGE_SIZE],
    header: &Header,
    edit: PairEdit<'a>,
    index: usize,
) -> Result<&'a [u8]> {
    if index == edit.index {
        return Ok(edit.left);
    }
    if index == edit.index + 1 {
        return Ok(edit.right);
    }
    let source_index = if index > edit.index + 1 {
        index - 1
    } else {
        index
    };
    codec_cell::<C>(source, header, source_index)
}

pub(super) fn codec_cell<'a, C: Codec>(
    page: &'a [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
) -> Result<&'a [u8]> {
    if header.level == 0 {
        C::leaf_cell(page, header, index)
    } else {
        C::branch_cell(page, header, index)
    }
}

fn require_child<C: Codec>(cell: &[u8], page_limit: u64) -> Result<u32> {
    let child = C::read_branch_child(cell)?;
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
