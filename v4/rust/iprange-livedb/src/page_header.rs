//! Canonical common header fields for mapped v4 database pages.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_MAGIC, PAGE_SIZE};
use crate::mapping::ByteSource;

pub(crate) const SIZE: usize = 32;
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
        && page.equals(0, &PAGE_MAGIC)
        && page.byte(FLAGS) == Some(0)
        && u16_le(page, HEADER_BYTES) == SIZE as u16
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
