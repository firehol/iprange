//! Shared fixed-record slotted-page primitives.

use crate::contract::{u16_le, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::mapping::{ByteRange, ByteSource};
use crate::page_header;
use crate::page_io::{PageEdit, PageSink};

pub(crate) const HEADER_SIZE: usize = page_header::SIZE;
const MAX_SLOT_COUNT: usize = (PAGE_SIZE - HEADER_SIZE) / 2;

#[derive(Clone, Copy)]
pub(crate) struct Header {
    pub(crate) item_count: usize,
    pub(crate) level: u16,
    pub(crate) lower: usize,
    pub(crate) upper: usize,
}

#[derive(Clone, Copy)]
pub(crate) enum HeaderProblem {
    Header,
    Born,
    Type,
    Level,
    Shape,
}

#[derive(Clone, Copy)]
pub(crate) enum CellLayout {
    Fixed(usize),
    Variable { minimum: usize, maximum: usize },
}

#[derive(Clone, Copy)]
pub(crate) struct LayoutInspection<S> {
    pub(crate) reserved_nonzero: bool,
    page: S,
    header: Header,
    layout: CellLayout,
}

impl<S: ByteSource> LayoutInspection<S> {
    pub(crate) fn cells(self) -> LayoutCells<S> {
        LayoutCells {
            inspection: self,
            index: 0,
        }
    }
}

pub(crate) struct LayoutCells<S> {
    inspection: LayoutInspection<S>,
    index: usize,
}

impl<S: ByteSource> Iterator for LayoutCells<S> {
    type Item = ByteRange<S>;

    #[inline]
    fn next(&mut self) -> Option<Self::Item> {
        if self.index == self.inspection.header.item_count {
            return None;
        }
        crate::work::cell_probe(1);
        crate::work::slot_read(1);
        let slot = HEADER_SIZE + self.index * 2;
        self.index += 1;
        // SAFETY: `inspect_layout` proved the complete slot array.
        let start = usize::from(u16::from_le_bytes(unsafe {
            self.inspection.page.array_unchecked(slot)
        }));
        let len = match self.inspection.layout {
            CellLayout::Fixed(len) => len,
            CellLayout::Variable { .. } => {
                // SAFETY: `inspect_layout` proved the variable record header.
                usize::from(u16::from_le_bytes(unsafe {
                    self.inspection.page.array_unchecked(start)
                }))
            }
        };
        // SAFETY: `inspect_layout` proved this complete record extent, and all
        // page offsets and record lengths fit in `u32`.
        Some(unsafe { ByteRange::new_unchecked(self.inspection.page, start, len) })
    }

    fn size_hint(&self) -> (usize, Option<usize>) {
        let remaining = self.inspection.header.item_count - self.index;
        (remaining, Some(remaining))
    }
}

impl<S: ByteSource> ExactSizeIterator for LayoutCells<S> {}

/// One fixed-record page whose common shape has been checked once for search.
#[derive(Clone, Copy)]
pub(crate) struct FixedSearch<S> {
    page: S,
    header: Header,
    cell_len: usize,
}

impl<S: ByteSource> FixedSearch<S> {
    pub(crate) fn new(page: S, header: &Header, cell_len: usize) -> Result<Self> {
        if page.len() != PAGE_SIZE || !shape_valid(*header) || cell_len == 0 || cell_len > PAGE_SIZE
        {
            return Err(Error::Corrupt("fixed slotted-page search shape is invalid"));
        }
        Ok(Self {
            page,
            header: *header,
            cell_len,
        })
    }

    /// Read a fixed cell at an index already bounded by the search algorithm.
    ///
    /// The slot offset follows from the checked page shape. The persistent slot
    /// value remains untrusted and is checked on every probe.
    #[inline(always)]
    pub(crate) unsafe fn cell_at(self, index: usize) -> Result<ByteRange<S>> {
        debug_assert!(index < self.header.item_count);
        crate::work::cell_probe(1);
        crate::work::slot_read(1);
        let slot = HEADER_SIZE + index * 2;
        // SAFETY: `FixedSearch::new` checked the canonical slot-array shape and
        // the caller guarantees `index < item_count`.
        let start = usize::from(u16::from_le_bytes(unsafe {
            self.page.array_unchecked(slot)
        }));
        if start < self.header.upper || start > PAGE_SIZE - self.cell_len {
            return Err(Error::Corrupt(
                "slotted-page cell is outside the record area",
            ));
        }
        // SAFETY: The extent check above proves the complete cell is in-page;
        // page offsets and fixed cell sizes trivially fit in `u32`.
        Ok(unsafe { ByteRange::new_unchecked(self.page, start, self.cell_len) })
    }
}

pub(crate) fn inspect_tree_header<S: ByteSource>(
    page: S,
    selected_txn: u64,
    branch_type: u8,
    leaf_type: u8,
    aux: u32,
    expected_level: Option<u16>,
) -> std::result::Result<Header, HeaderProblem> {
    let header = raw_header(page);
    if !page_header::common_valid(page) {
        return Err(HeaderProblem::Header);
    }
    if !page_header::born_valid(page, selected_txn) {
        return Err(HeaderProblem::Born);
    }
    if !tree_kind_valid_at_level(page, header.level, branch_type, leaf_type, aux) {
        return Err(HeaderProblem::Type);
    }
    if header.level > MAX_TREE_LEVEL
        || expected_level.is_some_and(|expected| expected != header.level)
    {
        return Err(HeaderProblem::Level);
    }
    if !shape_valid(header) {
        return Err(HeaderProblem::Shape);
    }
    Ok(header)
}

pub(crate) fn tree_kind_valid<S: ByteSource>(
    page: S,
    branch_type: u8,
    leaf_type: u8,
    aux: u32,
) -> bool {
    tree_kind_valid_at_level(page, page_header::level(page), branch_type, leaf_type, aux)
}

fn tree_kind_valid_at_level<S: ByteSource>(
    page: S,
    level: u16,
    branch_type: u8,
    leaf_type: u8,
    aux: u32,
) -> bool {
    let expected_type = if level == 0 { leaf_type } else { branch_type };
    page_header::kind_valid(page, expected_type, aux)
}

pub(crate) fn parse<S: ByteSource>(
    page: S,
    selected_txn: u64,
    page_type: u8,
    aux: u32,
    expected_level: Option<u16>,
) -> Result<Header> {
    crate::work::page_parse(1);
    validate_identity(page, selected_txn, page_type, aux)?;
    parse_shape(page, expected_level)
}

pub(crate) fn parse_tree<S: ByteSource>(
    page: S,
    selected_txn: u64,
    branch_type: u8,
    leaf_type: u8,
    aux: u32,
    expected_level: Option<u16>,
) -> Result<Header> {
    crate::work::page_parse(1);
    inspect_tree_header(
        page,
        selected_txn,
        branch_type,
        leaf_type,
        aux,
        expected_level,
    )
    .map_err(|problem| match problem {
        HeaderProblem::Header => Error::Corrupt("slotted-page header is invalid"),
        HeaderProblem::Born => Error::Corrupt("slotted-page transaction is invalid"),
        HeaderProblem::Type => Error::Corrupt("slotted-page type or discriminator is invalid"),
        HeaderProblem::Level => Error::Corrupt("slotted-page child level is invalid"),
        HeaderProblem::Shape => Error::Corrupt("slotted-page bounds are invalid"),
    })
}

fn validate_identity<S: ByteSource>(
    page: S,
    selected_txn: u64,
    page_type: u8,
    aux: u32,
) -> Result<()> {
    if !page_header::common_valid(page) {
        return Err(Error::Corrupt("slotted-page header is invalid"));
    }
    if !page_header::born_valid(page, selected_txn) {
        return Err(Error::Corrupt("slotted-page transaction is invalid"));
    }
    if !page_header::kind_valid(page, page_type, aux) {
        return Err(Error::Corrupt(
            "slotted-page type or discriminator is invalid",
        ));
    }
    Ok(())
}

fn parse_shape<S: ByteSource>(page: S, expected_level: Option<u16>) -> Result<Header> {
    let header = raw_header(page);
    if header.item_count == 0 || header.level > MAX_TREE_LEVEL {
        return Err(Error::Corrupt("slotted-page count or level is invalid"));
    }
    if expected_level.is_some_and(|expected| expected != header.level) {
        return Err(Error::Corrupt("slotted-page child level is invalid"));
    }
    if !shape_valid(header) {
        return Err(Error::Corrupt("slotted-page bounds are invalid"));
    }
    Ok(header)
}

fn raw_header<S: ByteSource>(page: S) -> Header {
    Header {
        item_count: page_header::item_count(page),
        level: page_header::level(page),
        lower: page_header::lower(page),
        upper: page_header::upper(page),
    }
}

fn shape_valid(header: Header) -> bool {
    // `item_count` comes from one u16 wire field, so this arithmetic fits even
    // on the supported 32-bit targets.
    let expected_lower = HEADER_SIZE + header.item_count * 2;
    header.item_count != 0
        && expected_lower == header.lower
        && header.lower <= header.upper
        && header.upper < PAGE_SIZE
}

pub(crate) fn cell<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    cell_len: usize,
) -> Result<ByteRange<S>> {
    crate::work::cell_probe(1);
    let start = slot_start(page, header, index)?;
    let end = start
        .checked_add(cell_len)
        .ok_or_else(|| Error::corrupt("slotted-page cell end overflows"))?;
    if start < header.upper || end > PAGE_SIZE {
        return Err(Error::Corrupt(
            "slotted-page cell is outside the record area",
        ));
    }
    ByteRange::new(page, start, cell_len)
        .ok_or_else(|| Error::corrupt("slotted-page cell is outside the record area"))
}

pub(crate) fn record<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    minimum_len: usize,
    maximum_len: usize,
) -> Result<ByteRange<S>> {
    crate::work::cell_probe(1);
    let start = slot_start(page, header, index)?;
    if start < header.upper || start + 2 > PAGE_SIZE {
        return Err(Error::Corrupt(
            "slotted-page record is outside the record area",
        ));
    }
    let record_len = usize::from(u16_le(page, start));
    if !(minimum_len..=maximum_len).contains(&record_len) {
        return Err(Error::Corrupt("slotted-page record length is invalid"));
    }
    let end = start
        .checked_add(record_len)
        .ok_or_else(|| Error::corrupt("slotted-page record end overflows"))?;
    if end > PAGE_SIZE {
        return Err(Error::Corrupt(
            "slotted-page record is outside the record area",
        ));
    }
    ByteRange::new(page, start, record_len)
        .ok_or_else(|| Error::corrupt("slotted-page record is outside the record area"))
}

pub(crate) fn inspect_layout<S: ByteSource>(
    page: S,
    header: &Header,
    layout: CellLayout,
) -> Option<LayoutInspection<S>> {
    if page.len() != PAGE_SIZE || !shape_valid(*header) {
        return None;
    }
    let mut used = [0u64; PAGE_SIZE / 64];
    let minimum = match layout {
        CellLayout::Fixed(length) => inspect_fixed_extents(page, header, length, &mut used)?,
        CellLayout::Variable { minimum, maximum } => {
            inspect_variable_extents(page, header, minimum, maximum, &mut used)?
        }
    };
    if minimum != header.upper {
        return None;
    }
    let mut reserved_nonzero = !page.all_zero(header.lower, header.upper - header.lower);
    if !reserved_nonzero {
        reserved_nonzero = unmarked_nonzero(page, &used, header.upper);
    }
    Some(LayoutInspection {
        reserved_nonzero,
        page,
        header: *header,
        layout,
    })
}

fn inspect_fixed_extents<S: ByteSource>(
    page: S,
    header: &Header,
    length: usize,
    used: &mut [u64; PAGE_SIZE / 64],
) -> Option<usize> {
    let maximum_start = PAGE_SIZE.checked_sub(length)?;
    let mut minimum = PAGE_SIZE;
    for index in 0..header.item_count {
        crate::work::cell_probe(1);
        crate::work::slot_read(1);
        let slot = HEADER_SIZE + index * 2;
        // SAFETY: `inspect_layout` checked the canonical slot-array shape and
        // this loop keeps `index < item_count`.
        let start = usize::from(u16::from_le_bytes(unsafe { page.array_unchecked(slot) }));
        if start < header.upper || start > maximum_start {
            return None;
        }
        if !mark_extent(used, start, start + length) {
            return None;
        }
        minimum = minimum.min(start);
    }
    Some(minimum)
}

fn inspect_variable_extents<S: ByteSource>(
    page: S,
    header: &Header,
    minimum_len: usize,
    maximum_len: usize,
    used: &mut [u64; PAGE_SIZE / 64],
) -> Option<usize> {
    let mut minimum = PAGE_SIZE;
    for index in 0..header.item_count {
        let record = record(page, header, index, minimum_len, maximum_len).ok()?;
        let start = record.source_offset();
        let end = start.checked_add(record.len())?;
        if !mark_extent(used, start, end) {
            return None;
        }
        minimum = minimum.min(start);
    }
    Some(minimum)
}

fn mark_extent(bits: &mut [u64; PAGE_SIZE / 64], start: usize, end: usize) -> bool {
    if start >= end || end > PAGE_SIZE {
        return false;
    }
    let first = start / 64;
    let last = (end - 1) / 64;
    if first == last {
        let mask = (u64::MAX << (start % 64)) & end_mask(end % 64);
        return mark_word(&mut bits[first], mask);
    }
    if !mark_word(&mut bits[first], u64::MAX << (start % 64)) {
        return false;
    }
    for word in &mut bits[first + 1..last] {
        if !mark_word(word, u64::MAX) {
            return false;
        }
    }
    mark_word(&mut bits[last], end_mask(end % 64))
}

#[inline]
fn end_mask(bit: usize) -> u64 {
    if bit == 0 {
        u64::MAX
    } else {
        (1u64 << bit) - 1
    }
}

#[inline]
fn mark_word(word: &mut u64, mask: u64) -> bool {
    if *word & mask != 0 {
        return false;
    }
    *word |= mask;
    true
}

fn unmarked_nonzero<S: ByteSource>(page: S, used: &[u64; PAGE_SIZE / 64], start: usize) -> bool {
    for (word_index, marked) in used.iter().enumerate().skip(start / 64) {
        let base = word_index * 64;
        let in_range = if base < start {
            u64::MAX << (start - base)
        } else {
            u64::MAX
        };
        let mut unmarked = !marked & in_range;
        while unmarked != 0 {
            let bit = unmarked.trailing_zeros() as usize;
            let length = (unmarked >> bit).trailing_ones() as usize;
            if !page.all_zero(base + bit, length) {
                return true;
            }
            let mask = if length == 64 {
                u64::MAX
            } else {
                ((1u64 << length) - 1) << bit
            };
            unmarked &= !mask;
        }
    }
    false
}

fn slot_start<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<usize> {
    crate::work::slot_read(1);
    if index >= header.item_count {
        return Err(Error::Corrupt("slotted-page slot index is invalid"));
    }
    let slot = HEADER_SIZE + index * 2;
    if slot + 2 > header.lower {
        return Err(Error::Corrupt("slotted-page slot is outside the array"));
    }
    Ok(usize::from(u16_le(page, slot)))
}

pub(crate) fn insert<D: PageEdit, S: ByteSource>(
    page: &mut D,
    header: &Header,
    index: usize,
    cell: S,
) -> Result<bool> {
    if index > header.item_count {
        return Err(Error::Corrupt("slotted-page insertion index is invalid"));
    }
    if cell.is_empty() {
        return Err(Error::InvalidArgument("slotted-page record is empty"));
    }
    if !insert_fits(header, cell.len()) {
        return Ok(false);
    }
    let upper = header.upper - cell.len();
    let lower = header.lower + 2;
    let slot = HEADER_SIZE + index * 2;
    if slot != header.lower {
        page.copy_within(slot, slot + 2, header.lower - slot)?;
    }
    page.write_source(upper, cell)?;
    page.put_u16(slot, upper as u16)?;
    page.put_u16(page_header::ITEM_COUNT, (header.item_count + 1) as u16)?;
    page.put_u16(page_header::LOWER, lower as u16)?;
    page.put_u16(page_header::UPPER, upper as u16)?;
    Ok(true)
}

pub(crate) fn replace<D: PageEdit, S: ByteSource>(
    page: &mut D,
    header: &Header,
    index: usize,
    old_len: usize,
    cell: S,
) -> Result<bool> {
    if index >= header.item_count || old_len == 0 || cell.is_empty() {
        return Err(Error::Corrupt("slotted-page replacement is invalid"));
    }
    let start = record_start(page.view(), header, index, old_len)?;

    if cell.len() > old_len {
        let growth = cell.len() - old_len;
        if header.lower > header.upper.saturating_sub(growth) {
            return Ok(false);
        }
        page.copy_within(header.upper, header.upper - growth, start - header.upper)?;
        adjust_slots_before(page, header, index, start, false, growth)?;
        let new_start = start - growth;
        page.write_source(new_start, cell)?;
        page.put_u16(HEADER_SIZE + index * 2, new_start as u16)?;
        page.put_u16(page_header::UPPER, (header.upper - growth) as u16)?;
    } else {
        let shrink = old_len - cell.len();
        if shrink != 0 {
            page.copy_within(header.upper, header.upper + shrink, start - header.upper)?;
            page.zero(header.upper, shrink)?;
            adjust_slots_before(page, header, index, start, true, shrink)?;
        }
        let new_start = start + shrink;
        page.write_source(new_start, cell)?;
        page.put_u16(HEADER_SIZE + index * 2, new_start as u16)?;
        page.put_u16(page_header::UPPER, (header.upper + shrink) as u16)?;
    }
    Ok(true)
}

pub(crate) fn remove<D: PageEdit>(
    page: &mut D,
    header: &Header,
    index: usize,
    old_len: usize,
) -> Result<()> {
    if index >= header.item_count || header.item_count <= 1 || old_len == 0 {
        return Err(Error::Corrupt("slotted-page removal is invalid"));
    }
    let start = record_start(page.view(), header, index, old_len)?;

    page.copy_within(header.upper, header.upper + old_len, start - header.upper)?;
    page.zero(header.upper, old_len)?;
    adjust_slots_before(page, header, index, start, true, old_len)?;
    let slot = HEADER_SIZE + index * 2;
    page.copy_within(slot + 2, slot, header.lower - slot - 2)?;
    page.put_u16(header.lower - 2, 0)?;
    page.put_u16(page_header::ITEM_COUNT, (header.item_count - 1) as u16)?;
    page.put_u16(page_header::LOWER, (header.lower - 2) as u16)?;
    page.put_u16(page_header::UPPER, (header.upper + old_len) as u16)?;
    Ok(())
}

pub(crate) fn remove_fixed_range<D: PageEdit>(
    page: &mut D,
    header: &Header,
    start: usize,
    count: usize,
    cell_len: usize,
) -> Result<Header> {
    let end = start
        .checked_add(count)
        .ok_or_else(|| Error::corrupt("slotted-page removal range overflows"))?;
    if count == 0 || end > header.item_count || count >= header.item_count || cell_len == 0 {
        return Err(Error::Corrupt("slotted-page removal range is invalid"));
    }
    let mut physical_to_logical = [u16::MAX; MAX_SLOT_COUNT];
    let positions = fixed_positions(page.view(), header, cell_len, &mut physical_to_logical)?;

    let remaining = header.item_count - count;
    let mut destination = PAGE_SIZE;
    for physical in (0..positions.len()).rev() {
        let logical = usize::from(positions[physical]);
        if logical < start || logical >= end {
            let output = if logical < start {
                logical
            } else {
                logical - count
            };
            let source = header.upper + physical * cell_len;
            destination -= cell_len;
            page.copy_within(source, destination, cell_len)?;
            page.put_u16(HEADER_SIZE + output * 2, destination as u16)?;
        }
    }
    let lower = HEADER_SIZE + remaining * 2;
    page.zero(lower, header.lower - lower)?;
    page.zero(header.upper, destination - header.upper)?;
    page.put_u16(page_header::ITEM_COUNT, remaining as u16)?;
    page.put_u16(page_header::LOWER, lower as u16)?;
    page.put_u16(page_header::UPPER, destination as u16)?;
    Ok(Header {
        item_count: remaining,
        level: header.level,
        lower,
        upper: destination,
    })
}

pub(crate) fn truncate<D: PageEdit>(page: &mut D, header: &Header, keep: usize) -> Result<Header> {
    if keep == 0 || keep > header.item_count {
        return Err(Error::Corrupt("slotted-page truncation is invalid"));
    }
    if keep == header.item_count {
        return Ok(*header);
    }
    let mut storage = [PhysicalRecord::EMPTY; MAX_SLOT_COUNT];
    let records = storage
        .get_mut(..header.item_count)
        .ok_or_else(|| Error::corrupt("slotted-page slot count is invalid"))?;
    for (index, record) in records.iter_mut().enumerate() {
        crate::work::slot_scan_step(1);
        let start = slot_start(page.view(), header, index)?;
        if start < header.upper || start >= PAGE_SIZE {
            return Err(Error::Corrupt("slotted-page record offset is invalid"));
        }
        *record = PhysicalRecord {
            start: start as u16,
            index: index as u16,
        };
    }
    records.sort_unstable_by_key(|record| record.start);
    if usize::from(records[0].start) != header.upper
        || records
            .windows(2)
            .any(|pair| pair[0].start == pair[1].start)
    {
        return Err(Error::Corrupt("slotted-page record offsets are invalid"));
    }

    let mut destination = PAGE_SIZE;
    for physical in (0..records.len()).rev() {
        let record = records[physical];
        let start = usize::from(record.start);
        let end = records
            .get(physical + 1)
            .map_or(PAGE_SIZE, |next| usize::from(next.start));
        if usize::from(record.index) < keep {
            let len = end - start;
            let new_start = destination - len;
            page.copy_within(start, new_start, len)?;
            page.put_u16(
                HEADER_SIZE + usize::from(record.index) * 2,
                new_start as u16,
            )?;
            destination = new_start;
        }
    }

    finish_truncate(page, header, keep, destination)
}

pub(crate) fn truncate_fixed<D: PageEdit>(
    page: &mut D,
    header: &Header,
    keep: usize,
    cell_len: usize,
) -> Result<Header> {
    if keep == 0 || keep > header.item_count || cell_len == 0 {
        return Err(Error::Corrupt("fixed slotted-page truncation is invalid"));
    }
    if keep == header.item_count {
        return Ok(*header);
    }
    let mut physical_to_logical = [u16::MAX; MAX_SLOT_COUNT];
    let positions = fixed_positions(page.view(), header, cell_len, &mut physical_to_logical)?;

    let mut destination = PAGE_SIZE;
    for physical in (0..positions.len()).rev() {
        let logical = usize::from(positions[physical]);
        if logical < keep {
            let start = header.upper + physical * cell_len;
            destination -= cell_len;
            page.copy_within(start, destination, cell_len)?;
            page.put_u16(HEADER_SIZE + logical * 2, destination as u16)?;
        }
    }

    finish_truncate(page, header, keep, destination)
}

fn fixed_positions<'a, S: ByteSource>(
    page: S,
    header: &Header,
    cell_len: usize,
    storage: &'a mut [u16; MAX_SLOT_COUNT],
) -> Result<&'a [u16]> {
    let payload = header
        .item_count
        .checked_mul(cell_len)
        .and_then(|bytes| header.upper.checked_add(bytes))
        .ok_or_else(|| Error::corrupt("fixed slotted-page payload overflows"))?;
    if cell_len == 0 || payload != PAGE_SIZE {
        return Err(Error::Corrupt("fixed slotted-page payload is not packed"));
    }
    let positions = storage
        .get_mut(..header.item_count)
        .ok_or_else(|| Error::corrupt("slotted-page slot count is invalid"))?;
    for logical in 0..header.item_count {
        crate::work::slot_scan_step(1);
        let start = slot_start(page, header, logical)?;
        let offset = start
            .checked_sub(header.upper)
            .ok_or_else(|| Error::corrupt("fixed slotted-page record is outside payload"))?;
        if offset % cell_len != 0 {
            return Err(Error::Corrupt("fixed slotted-page record is misaligned"));
        }
        let physical = offset / cell_len;
        let slot = positions
            .get_mut(physical)
            .ok_or_else(|| Error::corrupt("fixed slotted-page record is outside payload"))?;
        if *slot != u16::MAX {
            return Err(Error::Corrupt("fixed slotted-page records overlap"));
        }
        *slot = logical as u16;
    }
    if positions.contains(&u16::MAX) {
        return Err(Error::Corrupt("fixed slotted-page payload has a gap"));
    }
    Ok(positions)
}

fn finish_truncate<D: PageEdit>(
    page: &mut D,
    header: &Header,
    keep: usize,
    upper: usize,
) -> Result<Header> {
    let lower = HEADER_SIZE + keep * 2;
    page.zero(lower, header.lower - lower)?;
    page.zero(header.upper, upper - header.upper)?;
    page.put_u16(page_header::ITEM_COUNT, keep as u16)?;
    page.put_u16(page_header::LOWER, lower as u16)?;
    page.put_u16(page_header::UPPER, upper as u16)?;
    Ok(Header {
        item_count: keep,
        level: header.level,
        lower,
        upper,
    })
}

#[derive(Clone, Copy)]
struct PhysicalRecord {
    start: u16,
    index: u16,
}

impl PhysicalRecord {
    const EMPTY: Self = Self { start: 0, index: 0 };
}

fn record_start<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    cell_len: usize,
) -> Result<usize> {
    let start = slot_start(page, header, index)?;
    let Some(end) = start.checked_add(cell_len) else {
        return Err(Error::Corrupt("slotted-page record extent is invalid"));
    };
    if start < header.upper || end > PAGE_SIZE {
        return Err(Error::Corrupt("slotted-page record extent is invalid"));
    }
    Ok(start)
}

fn adjust_slots_before<D: PageEdit>(
    page: &mut D,
    header: &Header,
    target: usize,
    before: usize,
    add: bool,
    amount: usize,
) -> Result<()> {
    for index in 0..header.item_count {
        crate::work::slot_scan_step(1);
        if index == target {
            continue;
        }
        let at = HEADER_SIZE + index * 2;
        let old = usize::from(u16_le(page.view(), at));
        if old >= before {
            continue;
        }
        let adjusted = if add {
            old.checked_add(amount)
        } else {
            old.checked_sub(amount)
        }
        .ok_or_else(|| Error::corrupt("slotted-page slot adjustment overflows"))?;
        if adjusted >= PAGE_SIZE {
            return Err(Error::Corrupt("slotted-page slot adjustment is invalid"));
        }
        page.put_u16(at, adjusted as u16)?;
    }
    Ok(())
}

pub(crate) fn insert_fits(header: &Header, cell_len: usize) -> bool {
    cell_len != 0
        && header
            .upper
            .checked_sub(cell_len)
            .is_some_and(|upper| header.lower + 2 <= upper)
}

pub(crate) fn replace_fits(header: &Header, old_len: usize, new_len: usize) -> bool {
    old_len != 0 && new_len != 0 && new_len <= old_len.saturating_add(header.upper - header.lower)
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Appender {
    item_count: usize,
    upper: usize,
}

impl Appender {
    pub(crate) fn new<D: PageSink + ?Sized>(
        page: &mut D,
        page_type: u8,
        born_txn: u64,
        level: u16,
        aux: u32,
    ) -> Self {
        page_header::initialize(
            page,
            page_header::Fields {
                page_type,
                born_txn,
                item_count: 0,
                level,
                lower: HEADER_SIZE as u16,
                upper: PAGE_SIZE as u16,
                aux,
            },
        )
        .expect("fixed mapped header fits");
        Self {
            item_count: 0,
            upper: PAGE_SIZE,
        }
    }

    pub(crate) fn try_push<D: PageSink + ?Sized, S: ByteSource>(
        &mut self,
        page: &mut D,
        cell: S,
    ) -> Result<bool> {
        if cell.is_empty() {
            return Err(Error::InvalidArgument("slotted-page record is empty"));
        }
        let lower = HEADER_SIZE
            .checked_add((self.item_count + 1) * 2)
            .ok_or_else(|| Error::corrupt("slotted-page slot array overflows"))?;
        let upper = self
            .upper
            .checked_sub(cell.len())
            .ok_or_else(|| Error::corrupt("slotted-page record area overflows"))?;
        if lower > upper {
            return Ok(false);
        }
        page.write_source(upper, cell)?;
        page.put_u16(HEADER_SIZE + self.item_count * 2, upper as u16)?;
        self.item_count += 1;
        self.upper = upper;
        Ok(true)
    }

    pub(crate) fn finish<D: PageSink + ?Sized>(self, page: &mut D) -> Result<()> {
        if self.item_count == 0 {
            return Err(Error::InvalidArgument(
                "reachable slotted page cannot be empty",
            ));
        }
        page.put_u16(page_header::ITEM_COUNT, self.item_count as u16)?;
        page.put_u16(
            page_header::LOWER,
            (HEADER_SIZE + self.item_count * 2) as u16,
        )?;
        page.put_u16(page_header::UPPER, self.upper as u16)?;
        Ok(())
    }
}

pub(crate) struct Builder<'a, D: PageSink + ?Sized> {
    page: &'a mut D,
    appender: Appender,
}

impl<'a, D: PageSink + ?Sized> Builder<'a, D> {
    pub(crate) fn new(page: &'a mut D, page_type: u8, born_txn: u64, level: u16, aux: u32) -> Self {
        let appender = Appender::new(page, page_type, born_txn, level, aux);
        Self { page, appender }
    }

    pub(crate) fn push<S: ByteSource>(&mut self, cell: S) -> Result<()> {
        if self.appender.try_push(self.page, cell)? {
            Ok(())
        } else {
            Err(Error::InvalidArgument("slotted page is full"))
        }
    }

    pub(crate) fn finish(self) -> Result<()> {
        self.appender.finish(self.page)
    }
}

#[cfg(test)]
#[inline]
pub(crate) fn put_u16(bytes: &mut [u8], at: usize, value: u16) {
    bytes[at..at + 2].copy_from_slice(&value.to_le_bytes());
}

#[inline]
pub(crate) fn put_u32(bytes: &mut [u8], at: usize, value: u32) {
    bytes[at..at + 4].copy_from_slice(&value.to_le_bytes());
}

#[inline]
pub(crate) fn put_u64(bytes: &mut [u8], at: usize, value: u64) {
    bytes[at..at + 8].copy_from_slice(&value.to_le_bytes());
}

#[cfg(test)]
#[path = "slotted_page_tests.rs"]
mod tests;
