//! Codec-driven page encoding and search.

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};
use crate::mapping::{ByteRange, ByteSource};
use crate::page_header;
use crate::page_io::{PageEdit, PageSink};
use crate::slotted_page::{self, Builder, Header};

use super::Codec;

pub(super) struct CellBuf {
    bytes: [u8; MAX_TREE_CELL],
    len: usize,
}

impl CellBuf {
    pub(super) fn branch<C: Codec>(key: C::Key, child: u32) -> Result<Self> {
        let mut cell = Self {
            bytes: [0; MAX_TREE_CELL],
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

const MAX_TREE_CELL: usize = 512;

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
pub(super) struct Replacement<'a> {
    pub(super) index: usize,
    pub(super) cells: &'a [&'a [u8]],
}

impl Replacement<'_> {
    pub(super) fn total(&self, source_count: usize) -> usize {
        source_count + self.cells.len() - 1
    }
}

#[derive(Clone, Copy)]
enum Cell<'a, S> {
    Edit(&'a [u8]),
    Existing(ByteRange<S>),
}

impl<S: ByteSource> ByteSource for Cell<'_, S> {
    fn len(self) -> usize {
        match self {
            Self::Edit(bytes) => bytes.len(),
            Self::Existing(bytes) => bytes.len(),
        }
    }

    fn byte(self, at: usize) -> Option<u8> {
        match self {
            Self::Edit(bytes) => bytes.byte(at),
            Self::Existing(bytes) => bytes.byte(at),
        }
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        match self {
            Self::Edit(bytes) => bytes.array(at),
            Self::Existing(bytes) => bytes.array(at),
        }
    }

    unsafe fn array_unchecked<const N: usize>(self, at: usize) -> [u8; N] {
        match self {
            // SAFETY: Forwarded with the same caller contract.
            Self::Edit(bytes) => unsafe { bytes.array_unchecked(at) },
            // SAFETY: Forwarded with the same caller contract.
            Self::Existing(bytes) => unsafe { bytes.array_unchecked(at) },
        }
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        match self {
            Self::Edit(bytes) => bytes.copy_range_to(at, output),
            Self::Existing(bytes) => bytes.copy_range_to(at, output),
        }
    }
}

pub(super) fn build_edit<C: Codec, S: ByteSource, D: PageSink + ?Sized>(
    source: S,
    header: &Header,
    edit: Edit<'_>,
    start: usize,
    end: usize,
    output: &mut D,
) -> Result<()> {
    let page_type = page_type::<C>(header.level);
    let mut builder = Builder::new(
        output,
        page_type,
        page_header::born_txn(source),
        header.level,
        C::AUX,
    );
    for virtual_index in start..end {
        builder.push(virtual_cell::<C, _>(source, header, edit, virtual_index)?)?;
    }
    builder.finish()
}

pub(super) fn parse<C: Codec, S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<Header> {
    slotted_page::parse_tree(
        page,
        selected_txn,
        C::BRANCH_TYPE,
        C::LEAF_TYPE,
        C::AUX,
        expected_level,
    )
}

pub(super) fn lower_bound<C: Codec, S: ByteSource>(
    page: S,
    header: &Header,
    key: C::Key,
    insertion: bool,
) -> Result<(usize, bool)> {
    lower_bound_by::<C, _>(header, key, insertion, |index| {
        key_at::<C, _>(page, header, index)
    })
}

pub(super) fn lower_bound_by<C: Codec, F>(
    header: &Header,
    key: C::Key,
    insertion: bool,
    mut key_at: F,
) -> Result<(usize, bool)>
where
    F: FnMut(usize) -> Result<C::Key>,
{
    let mut lower = 0;
    let mut upper = header.item_count;
    let mut last = None;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        crate::work::key_probe(1);
        let current = key_at(middle)?;
        last = Some((middle, current));
        if current < key {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    let exists = if lower >= header.item_count {
        false
    } else {
        let current = match last.filter(|(index, _)| *index == lower) {
            Some((_, current)) => current,
            None => {
                crate::work::key_probe(1);
                key_at(lower)?
            }
        };
        current == key
    };
    if insertion || exists || lower == 0 {
        Ok((lower, exists))
    } else {
        Ok((lower - 1, false))
    }
}

pub(super) fn key_at<C: Codec, S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
) -> Result<C::Key> {
    C::read_key(codec_cell::<C, _>(page, header, index)?, header.level)
}

pub(super) fn branch_child<C: Codec, S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    let cell = C::branch_cell(page, header, index)?;
    require_child::<C, _>(cell, page_limit)
}

pub(super) fn edit_fits<C: Codec, S: ByteSource>(
    source: S,
    header: &Header,
    edit: Edit<'_>,
) -> Result<bool> {
    crate::work::edit_fit_probe(1);
    if edit.replace {
        if edit.index >= header.item_count {
            return Err(Error::Corrupt("B+tree replacement index is invalid"));
        }
        let old_len = codec_cell::<C, _>(source, header, edit.index)?.len();
        return Ok(slotted_page::replace_fits(header, old_len, edit.cell.len()));
    }
    if edit.index > header.item_count {
        return Err(Error::Corrupt("B+tree insertion index is invalid"));
    }
    Ok(slotted_page::insert_fits(header, edit.cell.len()))
}

pub(super) fn split_index<C: Codec, S: ByteSource>(
    source: S,
    header: &Header,
    edit: Edit<'_>,
) -> Result<usize> {
    let total = edit.total(header.item_count);
    if let Some(cell_len) = C::fixed_cell_size(header.level) {
        return fixed_split_index(total, cell_len);
    }
    split_by_size(total, |index| {
        Ok(virtual_cell::<C, _>(source, header, edit, index)?.len())
    })
}

pub(super) fn build_replacement<C: Codec, S: ByteSource, D: PageSink + ?Sized>(
    source: S,
    header: &Header,
    edit: Replacement<'_>,
    start: usize,
    end: usize,
    output: &mut D,
) -> Result<()> {
    let mut builder = Builder::new(
        output,
        page_type::<C>(header.level),
        page_header::born_txn(source),
        header.level,
        C::AUX,
    );
    for index in start..end {
        builder.push(replacement_cell::<C, _>(source, header, edit, index)?)?;
    }
    builder.finish()
}

pub(super) fn replacement_fits<C: Codec, S: ByteSource>(
    source: S,
    header: &Header,
    edit: Replacement<'_>,
) -> Result<bool> {
    crate::work::edit_fit_probe(1);
    if edit.index >= header.item_count || edit.cells.is_empty() {
        return Err(Error::Corrupt("B+tree replacement is invalid"));
    }
    let old_len = codec_cell::<C, _>(source, header, edit.index)?.len();
    let available = old_len
        .checked_add(header.upper - header.lower)
        .ok_or_else(|| Error::arithmetic_overflow("B+tree replacement capacity"))?;
    let payload = edit.cells.iter().try_fold(0usize, |total, cell| {
        total
            .checked_add(cell.len())
            .ok_or_else(|| Error::arithmetic_overflow("B+tree replacement size"))
    })?;
    let required = edit
        .cells
        .len()
        .checked_sub(1)
        .and_then(|extra| extra.checked_mul(2))
        .and_then(|slots| payload.checked_add(slots))
        .ok_or_else(|| Error::arithmetic_overflow("B+tree replacement size"))?;
    Ok(required <= available)
}

pub(super) fn replacement_split_index<C: Codec, S: ByteSource>(
    source: S,
    header: &Header,
    edit: Replacement<'_>,
) -> Result<usize> {
    let total = edit.total(header.item_count);
    if let Some(cell_len) = C::fixed_cell_size(header.level) {
        return fixed_split_index(total, cell_len);
    }
    split_by_size(total, |index| {
        Ok(replacement_cell::<C, _>(source, header, edit, index)?.len())
    })
}

pub(super) fn truncate<C: Codec, D: PageEdit>(
    page: &mut D,
    header: &Header,
    keep: usize,
) -> Result<Header> {
    if let Some(cell_len) = C::fixed_cell_size(header.level) {
        slotted_page::truncate_fixed(page, header, keep, cell_len)
    } else {
        slotted_page::truncate(page, header, keep)
    }
}

pub(super) fn require_codec<C: Codec>() -> Result<()> {
    if C::MAX_BRANCH_SIZE == 0
        || C::MAX_LEAF_SIZE == 0
        || C::MAX_BRANCH_SIZE + 2 + slotted_page::HEADER_SIZE > PAGE_SIZE
        || C::MAX_LEAF_SIZE + 2 + slotted_page::HEADER_SIZE > PAGE_SIZE
        || C::MAX_BRANCH_SIZE > MAX_TREE_CELL
    {
        return Err(Error::Unsupported("invalid B+tree codec"));
    }
    Ok(())
}

fn page_size(count: usize, payload: usize) -> Result<usize> {
    count
        .checked_mul(2)
        .and_then(|slots| slots.checked_add(slotted_page::HEADER_SIZE))
        .and_then(|base| base.checked_add(payload))
        .ok_or_else(|| Error::arithmetic_overflow("B+tree page size"))
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
        .ok_or_else(|| Error::invalid_argument("B+tree record cannot be split"))
}

fn fixed_split_index(total: usize, cell_len: usize) -> Result<usize> {
    if total < 2 {
        return Err(Error::Corrupt("B+tree split has fewer than two records"));
    }
    let middle = total / 2;
    let left_payload = middle
        .checked_mul(cell_len)
        .ok_or_else(|| Error::arithmetic_overflow("B+tree split size"))?;
    let right_payload = (total - middle)
        .checked_mul(cell_len)
        .ok_or_else(|| Error::arithmetic_overflow("B+tree split size"))?;
    if page_size(middle, left_payload)? > PAGE_SIZE
        || page_size(total - middle, right_payload)? > PAGE_SIZE
    {
        return Err(Error::InvalidArgument("B+tree record cannot be split"));
    }
    Ok(middle)
}

fn payload_size<F>(total: usize, cell_len: &mut F) -> Result<usize>
where
    F: FnMut(usize) -> Result<usize>,
{
    (0..total).try_fold(0usize, |used, index| {
        used.checked_add(cell_len(index)?)
            .ok_or_else(|| Error::arithmetic_overflow("B+tree split size"))
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
            .ok_or_else(|| Error::arithmetic_overflow("B+tree split size"))?;
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
        .ok_or_else(|| Error::corrupt("B+tree split size changed"))?;
    let right = page_size(total - middle, right_payload)?;
    if left > PAGE_SIZE || right > PAGE_SIZE {
        return Ok(None);
    }
    Ok(Some(left.abs_diff(right)))
}

fn virtual_cell<'a, C: Codec, S: ByteSource>(
    source: S,
    header: &Header,
    edit: Edit<'a>,
    virtual_index: usize,
) -> Result<Cell<'a, S>> {
    if virtual_index == edit.index {
        return Ok(Cell::Edit(edit.cell));
    }
    let source_index = if virtual_index > edit.index && !edit.replace {
        virtual_index - 1
    } else {
        virtual_index
    };
    codec_cell::<C, S>(source, header, source_index).map(Cell::Existing)
}

fn replacement_cell<'a, C: Codec, S: ByteSource>(
    source: S,
    header: &Header,
    edit: Replacement<'a>,
    index: usize,
) -> Result<Cell<'a, S>> {
    if let Some(cell) = index
        .checked_sub(edit.index)
        .and_then(|offset| edit.cells.get(offset))
    {
        return Ok(Cell::Edit(cell));
    }
    let source_index = if index >= edit.index + edit.cells.len() {
        index - edit.cells.len() + 1
    } else {
        index
    };
    codec_cell::<C, S>(source, header, source_index).map(Cell::Existing)
}

pub(super) fn codec_cell<C: Codec, S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
) -> Result<ByteRange<S>> {
    if header.level == 0 {
        C::leaf_cell(page, header, index)
    } else {
        C::branch_cell(page, header, index)
    }
}

fn require_child<C: Codec, S: ByteSource>(cell: S, page_limit: u64) -> Result<u32> {
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
