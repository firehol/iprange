//! Canonical common header fields for mapped v4 database pages.

use crate::contract::{u16_le, u32_le, u32_source, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::error::Result;
use crate::mapping::ByteSource;
use crate::page_io::PageSink;

pub(crate) const SIZE: usize = 32;
const MAGIC: usize = 0;
const MAGIC_WORD: u32 = u32::from_le_bytes(PAGE_MAGIC);
pub(crate) const TYPE: usize = 4;
pub(crate) const FLAGS: usize = 5;
pub(crate) const HEADER_BYTES: usize = 6;
pub(crate) const BORN_TXN: usize = 8;
pub(crate) const ITEM_COUNT: usize = 16;
pub(crate) const LEVEL: usize = 18;
pub(crate) const LOWER: usize = 20;
pub(crate) const UPPER: usize = 22;
pub(crate) const AUX: usize = 24;

#[derive(Clone, Copy)]
pub(crate) enum CommonProblem {
    Header,
    Born,
}

#[derive(Clone, Copy)]
pub(crate) struct Fields {
    pub(crate) page_type: u8,
    pub(crate) born_txn: u64,
    pub(crate) item_count: u16,
    pub(crate) level: u16,
    pub(crate) lower: u16,
    pub(crate) upper: u16,
    pub(crate) aux: u32,
}

pub(crate) fn initialize<D: PageSink + ?Sized>(page: &mut D, fields: Fields) -> Result<()> {
    page.fill(0);
    page.write(MAGIC, &PAGE_MAGIC)?;
    page.set_byte(TYPE, fields.page_type)?;
    page.put_u16(HEADER_BYTES, SIZE as u16)?;
    page.put_u64(BORN_TXN, fields.born_txn)?;
    page.put_u16(ITEM_COUNT, fields.item_count)?;
    page.put_u16(LEVEL, fields.level)?;
    page.put_u16(LOWER, fields.lower)?;
    page.put_u16(UPPER, fields.upper)?;
    page.put_u32(AUX, fields.aux)
}

pub(crate) fn common_problem<S: ByteSource>(page: S, selected_txn: u64) -> Option<CommonProblem> {
    if !common_valid(page) {
        Some(CommonProblem::Header)
    } else if !born_valid(page, selected_txn) {
        Some(CommonProblem::Born)
    } else {
        None
    }
}

pub(crate) fn common_valid<S: ByteSource>(page: S) -> bool {
    page.len() == PAGE_SIZE
        && has_magic(page)
        && page.byte(FLAGS) == Some(0)
        && u16_le(page, HEADER_BYTES) == SIZE as u16
}

pub(crate) fn has_magic<S: ByteSource>(page: S) -> bool {
    u32_source(page, MAGIC) == Some(MAGIC_WORD)
}

pub(crate) fn owned_by<S: ByteSource>(page: S, txn: u64) -> bool {
    has_magic(page) && born_txn(page) == txn
}

pub(crate) fn born_valid<S: ByteSource>(page: S, selected_txn: u64) -> bool {
    let born = born_txn(page);
    born != 0 && born <= selected_txn
}

pub(crate) fn kind_valid<S: ByteSource>(page: S, page_type: u8, aux: u32) -> bool {
    page.byte(TYPE) == Some(page_type) && u32_le(page, AUX) == aux
}

pub(crate) fn page_type<S: ByteSource>(page: S) -> Option<u8> {
    page.byte(TYPE)
}

pub(crate) fn born_txn<S: ByteSource>(page: S) -> u64 {
    u64_le(page, BORN_TXN)
}

pub(crate) fn item_count<S: ByteSource>(page: S) -> usize {
    usize::from(u16_le(page, ITEM_COUNT))
}

pub(crate) fn level<S: ByteSource>(page: S) -> u16 {
    u16_le(page, LEVEL)
}

pub(crate) fn lower<S: ByteSource>(page: S) -> usize {
    usize::from(u16_le(page, LOWER))
}

pub(crate) fn upper<S: ByteSource>(page: S) -> usize {
    usize::from(u16_le(page, UPPER))
}
