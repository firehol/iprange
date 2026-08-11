//! Shared mapped-page access for explicit recovery.

use crate::mapping::{Mapping, PageView};
use crate::validation::ValidationReason;

#[derive(Clone, Copy)]
pub(crate) struct Problem {
    pub(crate) reason: ValidationReason,
    pub(crate) io_unreadable: bool,
}

pub(crate) fn checked(
    mapping: &Mapping,
    page_number: u32,
    page_count: u64,
) -> std::result::Result<PageView<'_>, Problem> {
    let page = mapping.page(page_number, page_count).map_err(|_| Problem {
        reason: ValidationReason::IoError,
        io_unreadable: true,
    })?;
    if !crate::page_checksum::valid(page) {
        return Err(Problem {
            reason: ValidationReason::PageCrcMismatch,
            io_unreadable: false,
        });
    }
    Ok(page)
}
