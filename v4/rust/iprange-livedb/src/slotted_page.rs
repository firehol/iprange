//! Shared fixed-record slotted-page primitives.

use crate::contract::{u16_le, u32_le, u64_le, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};

pub(crate) const HEADER_SIZE: usize = 32;

#[derive(Clone, Copy)]
pub(crate) struct Header {
    pub(crate) item_count: usize,
    pub(crate) level: u16,
    pub(crate) lower: usize,
    pub(crate) upper: usize,
}

pub(crate) fn parse(
    page: &[u8; PAGE_SIZE],
    selected_txn: u64,
    page_type: u8,
    aux: u32,
    expected_level: Option<u16>,
) -> Result<Header> {
    if page[..4] != PAGE_MAGIC || page[5] != 0 || u16_le(page, 6) != HEADER_SIZE as u16 {
        return Err(Error::Corrupt("slotted-page header is invalid"));
    }
    let born_txn = u64_le(page, 8);
    if born_txn == 0 || born_txn > selected_txn {
        return Err(Error::Corrupt("slotted-page transaction is invalid"));
    }
    let item_count = usize::from(u16_le(page, 16));
    let level = u16_le(page, 18);
    if item_count == 0 || level > MAX_TREE_LEVEL {
        return Err(Error::Corrupt("slotted-page count or level is invalid"));
    }
    if expected_level.is_some_and(|expected| expected != level) {
        return Err(Error::Corrupt("slotted-page child level is invalid"));
    }
    if page[4] != page_type || u32_le(page, 24) != aux {
        return Err(Error::Corrupt(
            "slotted-page type or discriminator is invalid",
        ));
    }

    let lower = usize::from(u16_le(page, 20));
    let upper = usize::from(u16_le(page, 22));
    let expected_lower = item_count
        .checked_mul(2)
        .and_then(|size| size.checked_add(HEADER_SIZE))
        .ok_or(Error::Corrupt("slotted-page slot array overflows"))?;
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

pub(crate) fn cell<'a>(
    page: &'a [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    cell_len: usize,
) -> Result<&'a [u8]> {
    let start = slot_start(page, header, index)?;
    let end = start
        .checked_add(cell_len)
        .ok_or(Error::Corrupt("slotted-page cell end overflows"))?;
    if start < header.upper || end > PAGE_SIZE {
        return Err(Error::Corrupt(
            "slotted-page cell is outside the record area",
        ));
    }
    Ok(&page[start..end])
}

pub(crate) fn record<'a>(
    page: &'a [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    minimum_len: usize,
    maximum_len: usize,
) -> Result<&'a [u8]> {
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
        .ok_or(Error::Corrupt("slotted-page record end overflows"))?;
    if end > PAGE_SIZE {
        return Err(Error::Corrupt(
            "slotted-page record is outside the record area",
        ));
    }
    Ok(&page[start..end])
}

fn slot_start(page: &[u8; PAGE_SIZE], header: &Header, index: usize) -> Result<usize> {
    if index >= header.item_count {
        return Err(Error::Corrupt("slotted-page slot index is invalid"));
    }
    let slot = HEADER_SIZE + index * 2;
    if slot + 2 > header.lower {
        return Err(Error::Corrupt("slotted-page slot is outside the array"));
    }
    Ok(usize::from(u16_le(page, slot)))
}

pub(crate) struct Builder<'a> {
    page: &'a mut [u8; PAGE_SIZE],
    item_count: usize,
    upper: usize,
}

impl<'a> Builder<'a> {
    pub(crate) fn new(
        page: &'a mut [u8; PAGE_SIZE],
        page_type: u8,
        born_txn: u64,
        level: u16,
        aux: u32,
    ) -> Self {
        page.fill(0);
        page[..4].copy_from_slice(&PAGE_MAGIC);
        page[4] = page_type;
        put_u16(page, 6, HEADER_SIZE as u16);
        put_u64(page, 8, born_txn);
        put_u16(page, 18, level);
        put_u32(page, 24, aux);
        Self {
            page,
            item_count: 0,
            upper: PAGE_SIZE,
        }
    }

    pub(crate) fn push(&mut self, cell: &[u8]) -> Result<()> {
        let lower = HEADER_SIZE
            .checked_add((self.item_count + 1) * 2)
            .ok_or(Error::Corrupt("slotted-page slot array overflows"))?;
        let upper = self
            .upper
            .checked_sub(cell.len())
            .ok_or(Error::Corrupt("slotted-page record area overflows"))?;
        if lower > upper {
            return Err(Error::InvalidArgument("slotted page is full"));
        }
        self.page[upper..self.upper].copy_from_slice(cell);
        put_u16(self.page, HEADER_SIZE + self.item_count * 2, upper as u16);
        self.item_count += 1;
        self.upper = upper;
        Ok(())
    }

    pub(crate) fn finish(self) -> Result<()> {
        if self.item_count == 0 {
            return Err(Error::InvalidArgument(
                "reachable slotted page cannot be empty",
            ));
        }
        put_u16(self.page, 16, self.item_count as u16);
        put_u16(self.page, 20, (HEADER_SIZE + self.item_count * 2) as u16);
        put_u16(self.page, 22, self.upper as u16);
        let checksum = crc32c::crc32c_with_zeroed(self.page, 28, 4)
            .ok_or(Error::Corrupt("page checksum field is invalid"))?;
        put_u32(self.page, 28, checksum);
        Ok(())
    }
}

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
        assert_eq!(cell(&page, &header, 0, 12).unwrap(), &[1; 12]);
        assert_eq!(cell(&page, &header, 1, 12).unwrap(), &[2; 12]);
        assert_eq!(
            u32_le(&page, 28),
            crc32c::crc32c_with_zeroed(&page, 28, 4).unwrap()
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
}
