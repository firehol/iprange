//! Shared fixed-record slotted-page primitives.

use crate::contract::{u16_le, u32_le, u64_le, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::mapping::{ByteRange, ByteSource, PageMut};

pub(crate) const HEADER_SIZE: usize = 32;

#[derive(Clone, Copy)]
pub(crate) struct Header {
    pub(crate) item_count: usize,
    pub(crate) level: u16,
    pub(crate) lower: usize,
    pub(crate) upper: usize,
}

pub(crate) fn parse<S: ByteSource>(
    page: S,
    selected_txn: u64,
    page_type: u8,
    aux: u32,
    expected_level: Option<u16>,
) -> Result<Header> {
    validate_identity(page, selected_txn, page_type, aux)?;
    parse_shape(page, expected_level)
}

fn validate_identity<S: ByteSource>(
    page: S,
    selected_txn: u64,
    page_type: u8,
    aux: u32,
) -> Result<()> {
    if page.len() != PAGE_SIZE
        || !page.equals(0, &PAGE_MAGIC)
        || page.byte(5) != Some(0)
        || u16_le(page, 6) != HEADER_SIZE as u16
    {
        return Err(Error::Corrupt("slotted-page header is invalid"));
    }
    let born_txn = u64_le(page, 8);
    if born_txn == 0 || born_txn > selected_txn {
        return Err(Error::Corrupt("slotted-page transaction is invalid"));
    }
    if page.byte(4) != Some(page_type) || u32_le(page, 24) != aux {
        return Err(Error::Corrupt(
            "slotted-page type or discriminator is invalid",
        ));
    }
    Ok(())
}

fn parse_shape<S: ByteSource>(page: S, expected_level: Option<u16>) -> Result<Header> {
    let item_count = usize::from(u16_le(page, 16));
    let level = u16_le(page, 18);
    if item_count == 0 || level > MAX_TREE_LEVEL {
        return Err(Error::Corrupt("slotted-page count or level is invalid"));
    }
    if expected_level.is_some_and(|expected| expected != level) {
        return Err(Error::Corrupt("slotted-page child level is invalid"));
    }

    let lower = usize::from(u16_le(page, 20));
    let upper = usize::from(u16_le(page, 22));
    let expected_lower = item_count
        .checked_mul(2)
        .and_then(|size| size.checked_add(HEADER_SIZE))
        .ok_or_else(|| Error::corrupt("slotted-page slot array overflows"))?;
    if lower != expected_lower || lower > upper || upper >= PAGE_SIZE {
        return Err(Error::Corrupt("slotted-page bounds are invalid"));
    }
    Ok(Header {
        item_count,
        level,
        lower,
        upper,
    })
}

pub(crate) fn cell<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    cell_len: usize,
) -> Result<ByteRange<S>> {
    let start = slot_start(page, header, index)?;
    let end = start
        .checked_add(cell_len)
        .ok_or_else(|| Error::corrupt("slotted-page cell end overflows"))?;
    if start < header.upper || end > PAGE_SIZE {
        return Err(Error::Corrupt(
            "slotted-page cell is outside the record area",
        ));
    }
    ByteRange::new(page, start, cell_len).ok_or(Error::Corrupt(
        "slotted-page cell is outside the record area",
    ))
}

pub(crate) fn record<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    minimum_len: usize,
    maximum_len: usize,
) -> Result<ByteRange<S>> {
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
    ByteRange::new(page, start, record_len).ok_or(Error::Corrupt(
        "slotted-page record is outside the record area",
    ))
}

fn slot_start<S: ByteSource>(page: S, header: &Header, index: usize) -> Result<usize> {
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
    let boundary = if index == 0 {
        PAGE_SIZE
    } else {
        slot_start(page.view(), header, index - 1)?
    };
    let moved = boundary
        .checked_sub(header.upper)
        .ok_or_else(|| Error::corrupt("slotted-page record order is invalid"))?;
    page.copy_within(header.upper, upper, moved)?;
    let slot = HEADER_SIZE + index * 2;
    page.copy_within(slot, slot + 2, header.lower - slot)?;
    for shifted in index + 1..=header.item_count {
        let at = HEADER_SIZE + shifted * 2;
        let old = usize::from(u16_le(page.view(), at));
        let adjusted = old
            .checked_sub(cell.len())
            .ok_or_else(|| Error::corrupt("slotted-page slot underflows"))?;
        page.put_u16(at, adjusted as u16)?;
    }
    let inserted = boundary - cell.len();
    page.write_source(inserted, cell)?;
    page.put_u16(slot, inserted as u16)?;
    page.put_u16(16, (header.item_count + 1) as u16)?;
    page.put_u16(20, lower as u16)?;
    page.put_u16(22, upper as u16)?;
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
    let start = slot_start(page.view(), header, index)?;
    let boundary = if index == 0 {
        PAGE_SIZE
    } else {
        slot_start(page.view(), header, index - 1)?
    };
    if match start.checked_add(old_len) {
        Some(end) => end != boundary,
        None => true,
    } {
        return Err(Error::Corrupt("slotted-page records are not canonical"));
    }

    if cell.len() > old_len {
        let growth = cell.len() - old_len;
        if header.lower > header.upper.saturating_sub(growth) {
            return Ok(false);
        }
        page.copy_within(header.upper, header.upper - growth, start - header.upper)?;
        adjust_slots(page, header, index + 1, false, growth)?;
        let new_start = start - growth;
        page.write_source(new_start, cell)?;
        page.put_u16(HEADER_SIZE + index * 2, new_start as u16)?;
        page.put_u16(22, (header.upper - growth) as u16)?;
    } else {
        let shrink = old_len - cell.len();
        if shrink != 0 {
            page.copy_within(header.upper, header.upper + shrink, start - header.upper)?;
            page.zero(header.upper, shrink)?;
            adjust_slots(page, header, index + 1, true, shrink)?;
        }
        let new_start = start + shrink;
        page.write_source(new_start, cell)?;
        page.put_u16(HEADER_SIZE + index * 2, new_start as u16)?;
        page.put_u16(22, (header.upper + shrink) as u16)?;
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
    let start = slot_start(page.view(), header, index)?;
    let boundary = if index == 0 {
        PAGE_SIZE
    } else {
        slot_start(page.view(), header, index - 1)?
    };
    if match start.checked_add(old_len) {
        Some(end) => end != boundary,
        None => true,
    } {
        return Err(Error::Corrupt("slotted-page records are not canonical"));
    }

    page.copy_within(header.upper, header.upper + old_len, start - header.upper)?;
    page.zero(header.upper, old_len)?;
    adjust_slots(page, header, index + 1, true, old_len)?;
    let slot = HEADER_SIZE + index * 2;
    page.copy_within(slot + 2, slot, header.lower - slot - 2)?;
    page.put_u16(header.lower - 2, 0)?;
    page.put_u16(16, (header.item_count - 1) as u16)?;
    page.put_u16(20, (header.lower - 2) as u16)?;
    page.put_u16(22, (header.upper + old_len) as u16)?;
    Ok(())
}

pub(crate) fn truncate<D: PageEdit>(page: &mut D, header: &Header, keep: usize) -> Result<Header> {
    if keep == 0 || keep > header.item_count {
        return Err(Error::Corrupt("slotted-page truncation is invalid"));
    }
    if keep == header.item_count {
        return Ok(*header);
    }
    let upper = slot_start(page.view(), header, keep - 1)?;
    let lower = HEADER_SIZE + keep * 2;
    page.zero(lower, header.lower - lower)?;
    page.zero(header.upper, upper - header.upper)?;
    page.put_u16(16, keep as u16)?;
    page.put_u16(20, lower as u16)?;
    page.put_u16(22, upper as u16)?;
    Ok(Header {
        item_count: keep,
        level: header.level,
        lower,
        upper,
    })
}

fn adjust_slots<D: PageEdit>(
    page: &mut D,
    header: &Header,
    start: usize,
    add: bool,
    amount: usize,
) -> Result<()> {
    for index in start..header.item_count {
        let at = HEADER_SIZE + index * 2;
        let old = usize::from(u16_le(page.view(), at));
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
        page.fill(0);
        page.write(0, &PAGE_MAGIC)
            .expect("fixed mapped header fits");
        page.set_byte(4, page_type)
            .expect("fixed mapped header fits");
        page.put_u16(6, HEADER_SIZE as u16)
            .expect("fixed mapped header fits");
        page.put_u64(8, born_txn).expect("fixed mapped header fits");
        page.put_u16(18, level).expect("fixed mapped header fits");
        page.put_u32(24, aux).expect("fixed mapped header fits");
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
        page.put_u16(16, self.item_count as u16)?;
        page.put_u16(20, (HEADER_SIZE + self.item_count * 2) as u16)?;
        page.put_u16(22, self.upper as u16)?;
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

pub(crate) trait PageSink {
    fn fill(&mut self, value: u8);
    fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()>;
    fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()>;
    fn copy_within(&mut self, source_at: usize, destination_at: usize, len: usize) -> Result<()>;
    fn set_byte(&mut self, at: usize, value: u8) -> Result<()>;
    fn put_u16(&mut self, at: usize, value: u16) -> Result<()>;
    fn put_u32(&mut self, at: usize, value: u32) -> Result<()>;
    fn put_u64(&mut self, at: usize, value: u64) -> Result<()>;
}

pub(crate) trait PageEdit: PageSink {
    type View<'a>: ByteSource
    where
        Self: 'a;

    fn view(&self) -> Self::View<'_>;
    fn zero(&mut self, at: usize, len: usize) -> Result<()>;
}

impl PageSink for PageMut<'_> {
    fn fill(&mut self, value: u8) {
        PageMut::fill(self, value);
    }

    fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        PageMut::write(self, at, bytes)
    }

    fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()> {
        PageMut::write_source(self, at, bytes)
    }

    fn copy_within(&mut self, source_at: usize, destination_at: usize, len: usize) -> Result<()> {
        PageMut::copy_within(self, source_at, destination_at, len)
    }

    fn set_byte(&mut self, at: usize, value: u8) -> Result<()> {
        PageMut::set_byte(self, at, value)
    }

    fn put_u16(&mut self, at: usize, value: u16) -> Result<()> {
        PageMut::put_u16(self, at, value)
    }

    fn put_u32(&mut self, at: usize, value: u32) -> Result<()> {
        PageMut::put_u32(self, at, value)
    }

    fn put_u64(&mut self, at: usize, value: u64) -> Result<()> {
        PageMut::put_u64(self, at, value)
    }
}

impl PageEdit for PageMut<'_> {
    type View<'a>
        = crate::mapping::PageView<'a>
    where
        Self: 'a;

    fn view(&self) -> Self::View<'_> {
        PageMut::view(self)
    }

    fn zero(&mut self, at: usize, len: usize) -> Result<()> {
        PageMut::zero(self, at, len)
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
impl PageSink for [u8; PAGE_SIZE] {
    fn fill(&mut self, value: u8) {
        <[u8]>::fill(self, value);
    }

    fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        let destination = self
            .get_mut(at..at.saturating_add(bytes.len()))
            .ok_or(Error::ArithmeticOverflow("test page write"))?;
        destination.copy_from_slice(bytes);
        Ok(())
    }

    fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()> {
        let destination = self
            .get_mut(at..at.saturating_add(bytes.len()))
            .ok_or(Error::ArithmeticOverflow("test page write"))?;
        if bytes.copy_range_to(0, destination) {
            Ok(())
        } else {
            Err(Error::Corrupt("test page source changed"))
        }
    }

    fn copy_within(&mut self, source_at: usize, destination_at: usize, len: usize) -> Result<()> {
        let end = source_at
            .checked_add(len)
            .ok_or(Error::ArithmeticOverflow("test page copy"))?;
        if end > PAGE_SIZE || destination_at.saturating_add(len) > PAGE_SIZE {
            return Err(Error::ArithmeticOverflow("test page copy"));
        }
        <[u8]>::copy_within(self, source_at..end, destination_at);
        Ok(())
    }

    fn set_byte(&mut self, at: usize, value: u8) -> Result<()> {
        *self
            .get_mut(at)
            .ok_or(Error::ArithmeticOverflow("test page byte"))? = value;
        Ok(())
    }

    fn put_u16(&mut self, at: usize, value: u16) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }

    fn put_u32(&mut self, at: usize, value: u32) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }

    fn put_u64(&mut self, at: usize, value: u64) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }
}

#[cfg(test)]
impl PageEdit for [u8; PAGE_SIZE] {
    type View<'a>
        = &'a [u8; PAGE_SIZE]
    where
        Self: 'a;

    fn view(&self) -> Self::View<'_> {
        self
    }

    fn zero(&mut self, at: usize, len: usize) -> Result<()> {
        let destination = self
            .get_mut(at..at.saturating_add(len))
            .ok_or(Error::ArithmeticOverflow("test page zero"))?;
        destination.fill(0);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builder_and_parser_round_trip_fixed_cells() {
        let mut page = [0; PAGE_SIZE];
        let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
        builder.push(&[1; 12]).unwrap();
        builder.push(&[2; 12]).unwrap();
        builder.finish().unwrap();

        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert_eq!(header.item_count, 2);
        assert_eq!(cell(&page, &header, 0, 12).unwrap().as_slice(), &[1; 12]);
        assert_eq!(cell(&page, &header, 1, 12).unwrap().as_slice(), &[2; 12]);
        assert_eq!(u32_le(&page, 28), 0);
        crate::page_checksum::seal(&mut page).unwrap();
        assert_eq!(
            u32_le(&page, 28),
            crate::crc32c::crc32c_with_zeroed(&page, 28, 4).unwrap()
        );
    }

    #[test]
    fn builder_rejects_an_overfull_or_empty_page() {
        let mut page = [0; PAGE_SIZE];
        assert!(Builder::new(&mut page, 2, 1, 0, 4).finish().is_err());

        let mut page = [0; PAGE_SIZE];
        let mut builder = Builder::new(&mut page, 2, 1, 0, 4);
        assert!(builder.push(&[0; PAGE_SIZE]).is_err());
    }

    #[test]
    fn in_place_insertion_changes_only_slots_and_free_space() {
        let mut page = [0; PAGE_SIZE];
        let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
        builder.push(b"aa").unwrap();
        builder.push(b"cc").unwrap();
        builder.finish().unwrap();

        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert!(insert(&mut page, &header, 1, b"bb").unwrap());
        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert!(insert(&mut page, &header, 0, b"00").unwrap());
        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert!(insert(&mut page, &header, 4, b"zz").unwrap());

        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        let records: Vec<&[u8]> = (0..header.item_count)
            .map(|index| cell(&page, &header, index, 2).unwrap().as_slice())
            .collect();
        assert_eq!(records, [b"00", b"aa", b"bb", b"cc", b"zz"]);
        assert!(page[header.lower..header.upper]
            .iter()
            .all(|byte| *byte == 0));
    }

    #[test]
    fn in_place_insertion_does_not_modify_a_full_page() {
        let mut page = [0; PAGE_SIZE];
        let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
        builder.push(&[1; PAGE_SIZE - HEADER_SIZE - 2]).unwrap();
        builder.finish().unwrap();
        let before = page;
        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();

        assert!(!insert(&mut page, &header, 1, b"x").unwrap());
        assert_eq!(page, before);
    }

    #[test]
    fn in_place_edits_clear_vacated_record_bytes() {
        let mut page = [0; PAGE_SIZE];
        let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
        for cell in [b"aaaa", b"bbbb", b"cccc", b"dddd"] {
            builder.push(cell).unwrap();
        }
        builder.finish().unwrap();

        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert!(replace(&mut page, &header, 1, 4, b"b").unwrap());
        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert!(page[header.lower..header.upper]
            .iter()
            .all(|&byte| byte == 0));

        remove(&mut page, &header, 2, 4).unwrap();
        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert!(page[header.lower..header.upper]
            .iter()
            .all(|&byte| byte == 0));

        truncate(&mut page, &header, 2).unwrap();
        let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
        assert!(page[header.lower..header.upper]
            .iter()
            .all(|&byte| byte == 0));
        assert_eq!(cell(&page, &header, 0, 4).unwrap().as_slice(), b"aaaa");
        assert_eq!(cell(&page, &header, 1, 1).unwrap().as_slice(), b"b");
    }
}
