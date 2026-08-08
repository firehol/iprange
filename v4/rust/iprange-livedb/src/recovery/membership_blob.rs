//! Complete CRC-checked recovery scan of one membership bitmap blob.

use crate::cancellation::CancellationToken;
use crate::contract::{u16_le, u32_le, u64_le, MetaV4, MAX_TREE_LEVEL, PAGE_MAGIC, PAGE_SIZE};
use crate::crc32c;
use crate::error::Result;
use crate::mapping::{ByteRange, ByteSource, Mapping, PageView};
use crate::slotted_page::{self, Header};
use crate::validation::{ValidationObject, ValidationReason};

use super::page_set::PageSet;
use super::report::{emit_page_unknown, RecoverySink, Reporter};
use super::tree_scan::{self, CellLayout};

const BRANCH_TYPE: u8 = 11;
const LEAF_TYPE: u8 = 12;
const MEMBERSHIP_KIND: u32 = 1;
const LEAF_DATA: usize = 48;
const LEAF_CAPACITY: usize = PAGE_SIZE - LEAF_DATA;

#[derive(Clone, Copy)]
struct Span {
    start: u64,
    end: u64,
    complete: bool,
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn scan<'m, S, F>(
    mapping: &'m Mapping,
    meta: MetaV4,
    root: u32,
    word_count: u32,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
    consume: F,
) -> Result<bool>
where
    S: RecoverySink,
    F: FnMut(ByteRange<PageView<'m>>) -> Result<()>,
{
    let length = u64::from(word_count) * 8;
    if !valid_root(root, length) {
        emit(reporter, ValidationReason::BlobInvalid, None)?;
        return Ok(false);
    }
    let mut scanner = Scanner {
        mapping,
        meta,
        pages,
        cancellation,
        reporter,
        consume,
    };
    let mut path = [0; MAX_TREE_LEVEL as usize + 1];
    let Some(span) = scanner.node(root, None, 0, length, &mut path, 0)? else {
        return Ok(false);
    };
    let complete = complete_span(span, length);
    if !complete {
        emit(scanner.reporter, ValidationReason::BlobInvalid, Some(root))?;
    }
    Ok(complete)
}

fn valid_root(root: u32, length: u64) -> bool {
    root >= 2 && length != 0
}

fn complete_span(span: Span, length: u64) -> bool {
    span.complete && span.start == 0 && span.end == length
}

struct Scanner<'m, 'a, 'b, S, F> {
    mapping: &'m Mapping,
    meta: MetaV4,
    pages: &'a mut PageSet,
    cancellation: &'a CancellationToken,
    reporter: &'a mut Reporter<'b, S>,
    consume: F,
}

impl<'m, S, F> Scanner<'m, '_, '_, S, F>
where
    S: RecoverySink,
    F: FnMut(ByteRange<PageView<'m>>) -> Result<()>,
{
    #[allow(clippy::too_many_arguments)]
    fn node(
        &mut self,
        page_number: u32,
        expected_level: Option<u16>,
        expected_start: u64,
        length: u64,
        path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
        depth: usize,
    ) -> Result<Option<Span>> {
        self.cancellation.check()?;
        if !self.claim(page_number, path, depth)? {
            return Ok(None);
        }
        let Some(page) = self.load(page_number)? else {
            return Ok(None);
        };
        match page.byte(4) {
            Some(LEAF_TYPE) => self.leaf(page_number, page, expected_level, expected_start, length),
            Some(BRANCH_TYPE) => self.branch(
                page_number,
                page,
                expected_level,
                expected_start,
                length,
                path,
                depth,
            ),
            _ => self.reject(page_number, ValidationReason::PageTypeMismatch, false),
        }
    }

    fn claim(
        &mut self,
        page_number: u32,
        path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
        depth: usize,
    ) -> Result<bool> {
        if depth >= path.len() {
            emit(
                self.reporter,
                ValidationReason::TreeLevelInvalid,
                Some(page_number),
            )?;
            return Ok(false);
        }
        if !page_in_bounds(page_number, self.meta.page_count) {
            emit(
                self.reporter,
                ValidationReason::PageOutOfBounds,
                Some(page_number),
            )?;
            return Ok(false);
        }
        if !self.pages.insert(page_number)? {
            emit(
                self.reporter,
                repeated_reason(&path[..depth], page_number),
                Some(page_number),
            )?;
            return Ok(false);
        }
        path[depth] = page_number;
        Ok(true)
    }

    fn load(&mut self, page_number: u32) -> Result<Option<PageView<'m>>> {
        let page = match self.mapping.page(page_number, self.meta.page_count) {
            Ok(page) => page,
            Err(_) => {
                self.reject(page_number, ValidationReason::IoError, true)?;
                return Ok(None);
            }
        };
        if crc32c::crc32c_source_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
            self.reject(page_number, ValidationReason::PageCrcMismatch, false)?;
            return Ok(None);
        }
        Ok(Some(page))
    }

    fn leaf(
        &mut self,
        page_number: u32,
        page: PageView<'m>,
        expected_level: Option<u16>,
        expected_start: u64,
        length: u64,
    ) -> Result<Option<Span>> {
        let Some((start, end, data_len)) =
            leaf_geometry(page, self.meta, expected_level, expected_start, length)
        else {
            return self.reject(page_number, ValidationReason::BlobInvalid, false);
        };
        if !page.all_zero(LEAF_DATA + data_len, PAGE_SIZE - LEAF_DATA - data_len) {
            return self.reject(page_number, ValidationReason::BlobInvalid, false);
        }
        self.reporter.page_accepted()?;
        let bytes = ByteRange::new(page, LEAF_DATA, data_len)
            .expect("validated blob payload is inside its mapped page");
        (self.consume)(bytes)?;
        Ok(Some(Span {
            start,
            end,
            complete: true,
        }))
    }

    #[allow(clippy::too_many_arguments)]
    fn branch(
        &mut self,
        page_number: u32,
        page: PageView<'m>,
        expected_level: Option<u16>,
        expected_start: u64,
        length: u64,
        path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
        depth: usize,
    ) -> Result<Option<Span>> {
        let header = match slotted_page::parse(
            page,
            self.meta.txn_id,
            BRANCH_TYPE,
            MEMBERSHIP_KIND,
            expected_level,
        ) {
            Ok(header)
                if header.level > 0
                    && tree_scan::layout_valid(page, &header, CellLayout::Fixed(16)) =>
            {
                header
            }
            _ => return self.reject(page_number, ValidationReason::BlobInvalid, false),
        };
        self.reporter.page_accepted()?;
        if !branch_records_valid(page, &header, expected_start, length, self.meta.page_count)? {
            emit(
                self.reporter,
                ValidationReason::BlobInvalid,
                Some(page_number),
            )?;
            return Ok(None);
        }
        self.branch_children(page_number, page, header, length, path, depth)
    }

    fn branch_children(
        &mut self,
        page_number: u32,
        page: PageView<'m>,
        header: Header,
        length: u64,
        path: &mut [u32; MAX_TREE_LEVEL as usize + 1],
        depth: usize,
    ) -> Result<Option<Span>> {
        let mut first = None;
        let mut previous_end = None;
        let mut complete = true;
        for index in 0..header.item_count {
            self.cancellation.check()?;
            let cell = slotted_page::cell(page, &header, index, 16)?;
            let offset = u64_le(cell, 0);
            let child = u32_le(cell, 8);
            let Some(span) = self.node(
                child,
                Some(header.level - 1),
                offset,
                length,
                path,
                depth + 1,
            )?
            else {
                complete = false;
                previous_end = None;
                continue;
            };
            first.get_or_insert(span.start);
            if span.start != offset || previous_end.is_some_and(|end| end != span.start) {
                emit(
                    self.reporter,
                    ValidationReason::BlobInvalid,
                    Some(page_number),
                )?;
                complete = false;
            }
            previous_end = Some(span.end);
            complete &= span.complete;
        }
        Ok(first.map(|start| Span {
            start,
            end: previous_end.unwrap_or(start),
            complete,
        }))
    }

    fn reject(
        &mut self,
        page_number: u32,
        reason: ValidationReason,
        io_unreadable: bool,
    ) -> Result<Option<Span>> {
        self.reporter.page_rejected(io_unreadable)?;
        emit(self.reporter, reason, Some(page_number))?;
        Ok(None)
    }
}

fn page_in_bounds(page: u32, page_count: u64) -> bool {
    page >= 2 && u64::from(page) < page_count
}

fn repeated_reason(path: &[u32], page: u32) -> ValidationReason {
    if path.contains(&page) {
        ValidationReason::TreeCycle
    } else {
        ValidationReason::PageAlias
    }
}

fn leaf_geometry<P: ByteSource>(
    page: P,
    meta: MetaV4,
    expected_level: Option<u16>,
    expected_start: u64,
    length: u64,
) -> Option<(u64, u64, usize)> {
    let data_len = usize::from(u16_le(page, 40));
    let start = u64_le(page, 32);
    let end = start.checked_add(data_len as u64)?;
    let valid = leaf_identity_valid(page, meta)
        && leaf_header_valid(page, expected_level, data_len)
        && leaf_payload_valid(data_len, start, end, expected_start, length);
    valid.then_some((start, end, data_len))
}

fn leaf_identity_valid<P: ByteSource>(page: P, meta: MetaV4) -> bool {
    let born = u64_le(page, 8);
    page.equals(0, &PAGE_MAGIC)
        && page.byte(4) == Some(LEAF_TYPE)
        && page.byte(5) == Some(0)
        && u16_le(page, 6) == slotted_page::HEADER_SIZE as u16
        && born != 0
        && born <= meta.txn_id
}

fn leaf_header_valid<P: ByteSource>(page: P, expected_level: Option<u16>, data_len: usize) -> bool {
    expected_level.map_or(true, |level| level == 0)
        && u16_le(page, 16) == 1
        && u16_le(page, 18) == 0
        && usize::from(u16_le(page, 20)) == LEAF_DATA + data_len
        && usize::from(u16_le(page, 22)) == PAGE_SIZE
        && u32_le(page, 24) == MEMBERSHIP_KIND
        && page.all_zero(42, LEAF_DATA - 42)
}

fn leaf_payload_valid(
    data_len: usize,
    start: u64,
    end: u64,
    expected_start: u64,
    length: u64,
) -> bool {
    (1..=LEAF_CAPACITY).contains(&data_len)
        && data_len % 8 == 0
        && start == expected_start
        && end <= length
        && (end == length || data_len == LEAF_CAPACITY)
}

fn branch_records_valid<P: ByteSource>(
    page: P,
    header: &Header,
    expected_start: u64,
    length: u64,
    page_count: u64,
) -> Result<bool> {
    let mut previous = None;
    for index in 0..header.item_count {
        let cell = slotted_page::cell(page, header, index, 16)?;
        let offset = u64_le(cell, 0);
        let child = u32_le(cell, 8);
        if !branch_record_valid(
            cell,
            child,
            offset,
            previous,
            index == 0,
            expected_start,
            length,
            page_count,
        ) {
            return Ok(false);
        }
        previous = Some(offset);
    }
    Ok(true)
}

#[allow(clippy::too_many_arguments)]
fn branch_record_valid<P: ByteSource>(
    cell: P,
    child: u32,
    offset: u64,
    previous: Option<u64>,
    first: bool,
    expected_start: u64,
    length: u64,
    page_count: u64,
) -> bool {
    u32_le(cell, 12) == 0
        && page_in_bounds(child, page_count)
        && offset < length
        && previous.map_or(true, |prior| prior < offset)
        && (!first || offset == expected_start)
}

fn emit<S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    reason: ValidationReason,
    page: Option<u32>,
) -> Result<()> {
    emit_page_unknown(reporter, reason, ValidationObject::MembershipBlob, page)
}
