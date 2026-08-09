//! Complete CRC-checked recovery scan of one membership bitmap blob.

use crate::blob_tree;
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::Result;
use crate::mapping::{ByteRange, ByteSource, Mapping, PageView};
use crate::slotted_page::{self, Header};
use crate::validation::{ValidationObject, ValidationReason};

use super::page_set::PageSet;
use super::report::{emit_page_unknown, RecoverySink, Reporter};
use super::tree_scan::CellLayout;

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
        match crate::page_header::page_type(page) {
            Some(blob_tree::LEAF_TYPE) => {
                self.leaf(page_number, page, expected_level, expected_start, length)
            }
            Some(blob_tree::BRANCH_TYPE) => self.branch(
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
        if !crate::page_checksum::valid(page) {
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
        let Some(geometry) = leaf_geometry(page, self.meta, expected_level, expected_start, length)
        else {
            return self.reject(page_number, ValidationReason::BlobInvalid, false);
        };
        if !page.all_zero(
            blob_tree::LEAF_DATA + geometry.data_len,
            PAGE_SIZE - blob_tree::LEAF_DATA - geometry.data_len,
        ) {
            return self.reject(page_number, ValidationReason::BlobInvalid, false);
        }
        self.reporter.page_accepted()?;
        let bytes = blob_tree::leaf_bytes(page, geometry)
            .expect("validated blob payload is inside its mapped page");
        (self.consume)(bytes)?;
        Ok(Some(Span {
            start: geometry.start,
            end: geometry.end,
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
            blob_tree::BRANCH_TYPE,
            blob_tree::MEMBERSHIP_KIND,
            expected_level,
        ) {
            Ok(header)
                if header.level > 0
                    && slotted_page::inspect_layout(
                        page,
                        &header,
                        CellLayout::Fixed(blob_tree::BRANCH_RECORD_SIZE),
                    )
                    .is_some_and(|inspection| !inspection.reserved_nonzero) =>
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
            let cell = slotted_page::cell(page, &header, index, blob_tree::BRANCH_RECORD_SIZE)?;
            let record = blob_tree::decode_branch_record(cell)
                .expect("branch validation accepted this record");
            let Some(span) = self.node(
                record.child,
                Some(header.level - 1),
                record.offset,
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
            if span.start != record.offset || previous_end.is_some_and(|end| end != span.start) {
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
) -> Option<blob_tree::LeafGeometry> {
    if !leaf_identity_valid(page, meta) {
        return None;
    }
    blob_tree::leaf_geometry(page, expected_level, expected_start, length).ok()
}

fn leaf_identity_valid<P: ByteSource>(page: P, meta: MetaV4) -> bool {
    blob_tree::require_leaf_identity(page, meta.txn_id, Some(0)).is_ok()
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
        let cell = slotted_page::cell(page, header, index, blob_tree::BRANCH_RECORD_SIZE)?;
        let Ok(record) = blob_tree::decode_branch_record(cell) else {
            return Ok(false);
        };
        if !branch_record_valid(
            record,
            previous,
            index == 0,
            expected_start,
            length,
            page_count,
        ) {
            return Ok(false);
        }
        previous = Some(record.offset);
    }
    Ok(true)
}

#[allow(clippy::too_many_arguments)]
fn branch_record_valid(
    record: blob_tree::BranchRecord,
    previous: Option<u64>,
    first: bool,
    expected_start: u64,
    length: u64,
    page_count: u64,
) -> bool {
    page_in_bounds(record.child, page_count)
        && record.offset < length
        && previous.map_or(true, |prior| prior < record.offset)
        && (!first || record.offset == expected_start)
}

fn emit<S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    reason: ValidationReason,
    page: Option<u32>,
) -> Result<()> {
    emit_page_unknown(reporter, reason, ValidationObject::MembershipBlob, page)
}
