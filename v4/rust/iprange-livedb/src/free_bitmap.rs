//! Hierarchical free-page bitmap with bounded four-page paths.

#[path = "free_bitmap/mutation.rs"]
mod mutation;

pub(crate) use mutation::{ensure_level, set_free, take_lowest};

use crate::bitmap_page::{self, Kind};
use crate::error::{Error, Result};
use crate::fixed_tree::Store;
use crate::mapping::ByteSource;
use crate::page_io::PageEdit;

use bitmap_page::{Header, BRANCH_CHILDREN};

pub(crate) trait BitmapStore: Store {
    fn allocate_bitmap_page(&mut self) -> Result<u32>;
    fn allocation_forbidden(&self, page_number: u32) -> bool;
}

fn parse<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
    verify_committed: bool,
) -> Result<Header> {
    let header = bitmap_page::inspect_header(page, selected_txn, Kind::Free, expected_level)
        .map_err(|_| Error::Corrupt("free bitmap header is invalid"))?;
    if verify_committed {
        verify_checksum(page)?;
        validate_body(page, &header)?;
    }
    Ok(header)
}

fn verify_checksum<S: ByteSource>(page: S) -> Result<()> {
    if !crate::page_checksum::valid(page) {
        return Err(Error::Corrupt("free bitmap checksum is invalid"));
    }
    Ok(())
}

fn validate_body<S: ByteSource>(page: S, header: &Header) -> Result<()> {
    if header.level == 0 {
        if !bitmap_page::reserved_zero(page, header.level)
            || first_leaf_word(page).is_none()
            || bitmap_page::nonzero_leaf_words(page) != header.item_count
        {
            return Err(Error::Corrupt("free bitmap leaf is invalid"));
        }
    } else if !bitmap_page::reserved_zero(page, header.level)
        || first_summary(page).is_none()
        || nonzero_children(page)? != header.item_count
    {
        return Err(Error::Corrupt("free bitmap branch is invalid"));
    }
    Ok(())
}

fn set_branch_child<D: PageEdit>(
    page: &mut D,
    header: &Header,
    index: usize,
    child: u32,
) -> Result<usize> {
    bitmap_page::replace_branch_child(page, header, index, child, child != 0)
}

fn branch_child<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    bitmap_page::checked_branch_child(page, header, index, page_limit)
}

fn nonzero_children<S: ByteSource>(page: S) -> Result<usize> {
    let mut count = 0;
    for index in 0..BRANCH_CHILDREN {
        let child = bitmap_page::branch_child(page, index)?;
        if bitmap_page::summary_bit(page, index)? != (child != 0) {
            return Err(Error::Corrupt("free bitmap summary disagrees with child"));
        }
        count += usize::from(child != 0);
    }
    Ok(count)
}

fn first_summary<S: ByteSource>(page: S) -> Option<usize> {
    bitmap_page::first_summary(page, 0)
}

fn first_leaf_word<S: ByteSource>(page: S) -> Option<(usize, u64)> {
    bitmap_page::first_leaf_word(page)
}

fn required_level(limit: u64) -> Result<u16> {
    if !(2..=1u64 << 32).contains(&limit) {
        return Err(Error::InvalidArgument("free bitmap limit is invalid"));
    }
    bitmap_page::required_level(limit)
}

fn require_bit(limit: u64, bit: u32) -> Result<()> {
    required_level(limit)?;
    if bit < 2 || u64::from(bit) >= limit {
        return Err(Error::InvalidArgument(
            "free page is outside the bitmap limit",
        ));
    }
    Ok(())
}

#[cfg(test)]
#[path = "free_bitmap_tests.rs"]
mod tests;
