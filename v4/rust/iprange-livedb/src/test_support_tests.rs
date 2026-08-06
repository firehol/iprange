//! Shared adapters for in-memory unit-test pages.

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};

pub(crate) fn copy_pages<'a, T>(
    pages: &'a mut [[u8; PAGE_SIZE]],
    source: u32,
    destination: u32,
    copy: impl FnOnce(&'a [u8; PAGE_SIZE], &'a mut [u8; PAGE_SIZE]) -> Result<T>,
) -> Result<T> {
    let source = source as usize;
    let destination = destination as usize;
    if source == destination || source >= pages.len() || destination >= pages.len() {
        return Err(Error::Corrupt("test copy pages are invalid"));
    }
    let (source_page, destination_page) = if source < destination {
        let (left, right) = pages.split_at_mut(destination);
        (&left[source], &mut right[0])
    } else {
        let (left, right) = pages.split_at_mut(source);
        (&right[0], &mut left[destination])
    };
    copy(source_page, destination_page)
}
