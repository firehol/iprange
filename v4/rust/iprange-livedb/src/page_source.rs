//! Bounds-checked positional page reads into caller-owned storage.

use crate::bootstrap::Bootstrap;
use crate::contract::PAGE_SIZE;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PageIoKind {
    NotFound,
    PermissionDenied,
    UnexpectedEof,
    InvalidInput,
    Interrupted,
    OutOfMemory,
    Other,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PageIoEvidence {
    pub(crate) kind: PageIoKind,
    pub(crate) raw_os_error: Option<i32>,
}

#[cfg(feature = "std")]
impl PageIoEvidence {
    pub(crate) fn from_error(error: &std::io::Error) -> Self {
        let kind = match error.kind() {
            std::io::ErrorKind::NotFound => PageIoKind::NotFound,
            std::io::ErrorKind::PermissionDenied => PageIoKind::PermissionDenied,
            std::io::ErrorKind::UnexpectedEof => PageIoKind::UnexpectedEof,
            std::io::ErrorKind::InvalidInput => PageIoKind::InvalidInput,
            std::io::ErrorKind::Interrupted => PageIoKind::Interrupted,
            std::io::ErrorKind::OutOfMemory => PageIoKind::OutOfMemory,
            _ => PageIoKind::Other,
        };
        Self {
            kind,
            raw_os_error: error.raw_os_error(),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PageSourceError {
    PageOutOfBounds(u32),
    OffsetOverflow,
    CommittedLengthMismatch,
    ForkedHandle,
    ShortRead {
        offset: u64,
        expected: usize,
        actual: usize,
    },
    Io(PageIoEvidence),
}

pub(crate) trait PositionalRead {
    /// Reject inherited live handles even when a higher layer can satisfy an
    /// operation from its owned page cache without issuing another read.
    fn check_access(&self) -> Result<(), PageSourceError> {
        Ok(())
    }

    /// Read exact bytes and enforce the same access check before touching the
    /// underlying source.
    fn read_exact_at(&self, offset: u64, bytes: &mut [u8]) -> Result<(), PageSourceError>;
}

impl PositionalRead for [u8] {
    fn read_exact_at(&self, offset: u64, bytes: &mut [u8]) -> Result<(), PageSourceError> {
        let start = usize::try_from(offset).map_err(|_| PageSourceError::OffsetOverflow)?;
        let end = start
            .checked_add(bytes.len())
            .ok_or(PageSourceError::OffsetOverflow)?;
        let source = self.get(start..end).ok_or(PageSourceError::ShortRead {
            offset,
            expected: bytes.len(),
            actual: self.len().saturating_sub(start).min(bytes.len()),
        })?;
        bytes.copy_from_slice(source);
        Ok(())
    }
}

/// Typed read-only access to pages inside one selected committed extent.
///
/// Implementations always copy into caller-owned storage. In particular, a
/// live implementation must not return a reference into concurrently mutable
/// file storage.
pub(crate) trait CommittedPageSource {
    /// Reject inherited or otherwise inaccessible handles even when a cursor
    /// could satisfy the operation from its own page buffer.
    fn check_access(&self) -> Result<(), PageSourceError> {
        Ok(())
    }

    fn read_page(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PageSourceError>;
}

impl<S: CommittedPageSource + ?Sized> CommittedPageSource for &S {
    #[inline]
    fn check_access(&self) -> Result<(), PageSourceError> {
        (**self).check_access()
    }

    #[inline]
    fn read_page(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PageSourceError> {
        (**self).read_page(pgno, destination)
    }
}

/// Immutable slice adapter used by codec and traversal tests. The declared
/// committed extent remains independent from the physical slice length so a
/// truncated source produces exact short-read evidence at read time.
#[derive(Clone, Copy, Debug)]
pub(crate) struct SlicePageSource<'a> {
    bytes: &'a [u8],
    page_count: u64,
}

impl<'a> SlicePageSource<'a> {
    pub(crate) const fn new(bytes: &'a [u8], page_count: u64) -> Self {
        Self { bytes, page_count }
    }
}

impl CommittedPageSource for SlicePageSource<'_> {
    fn read_page(
        &self,
        pgno: u32,
        destination: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PageSourceError> {
        if pgno < 2 || u64::from(pgno) >= self.page_count {
            return Err(PageSourceError::PageOutOfBounds(pgno));
        }
        let offset = u64::from(pgno)
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(PageSourceError::OffsetOverflow)?;
        self.bytes.read_exact_at(offset, destination)
    }
}

#[derive(Debug)]
pub(crate) struct PinnedPageSource<'a, S: PositionalRead + ?Sized> {
    source: &'a S,
    bootstrap: Bootstrap,
}

impl<'a, S: PositionalRead + ?Sized> PinnedPageSource<'a, S> {
    pub(crate) fn new(source: &'a S, bootstrap: Bootstrap) -> Result<Self, PageSourceError> {
        let expected = bootstrap
            .meta
            .page_count
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(PageSourceError::OffsetOverflow)?;
        if expected != bootstrap.committed_bytes {
            return Err(PageSourceError::CommittedLengthMismatch);
        }
        Ok(Self { source, bootstrap })
    }

    #[inline]
    pub(crate) const fn bootstrap(&self) -> Bootstrap {
        self.bootstrap
    }

    pub(crate) fn read_page(
        &self,
        pgno: u32,
        page: &mut [u8; PAGE_SIZE],
    ) -> Result<(), PageSourceError> {
        <Self as CommittedPageSource>::read_page(self, pgno, page)
    }

    #[inline]
    pub(crate) fn check_access(&self) -> Result<(), PageSourceError> {
        <Self as CommittedPageSource>::check_access(self)
    }
}

impl<S: PositionalRead + ?Sized> CommittedPageSource for PinnedPageSource<'_, S> {
    fn check_access(&self) -> Result<(), PageSourceError> {
        self.source.check_access()
    }

    fn read_page(&self, pgno: u32, page: &mut [u8; PAGE_SIZE]) -> Result<(), PageSourceError> {
        if pgno < 2 || u64::from(pgno) >= self.bootstrap.meta.page_count {
            return Err(PageSourceError::PageOutOfBounds(pgno));
        }
        let offset = u64::from(pgno)
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(PageSourceError::OffsetOverflow)?;
        let end = offset
            .checked_add(PAGE_SIZE as u64)
            .ok_or(PageSourceError::OffsetOverflow)?;
        if end > self.bootstrap.committed_bytes {
            return Err(PageSourceError::PageOutOfBounds(pgno));
        }
        self.source.read_exact_at(offset, page)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::bootstrap::{Bootstrap, MetaSelection};

    struct InjectedPositional {
        access: Option<PageSourceError>,
        read: Option<PageSourceError>,
    }

    impl PositionalRead for InjectedPositional {
        fn check_access(&self) -> Result<(), PageSourceError> {
            self.access.map_or(Ok(()), Err)
        }

        fn read_exact_at(&self, _: u64, _: &mut [u8]) -> Result<(), PageSourceError> {
            self.check_access()?;
            self.read.map_or(Ok(()), Err)
        }
    }

    fn bootstrap(page_count: u64) -> Bootstrap {
        let mut meta = empty_direct_meta(1);
        meta.page_count = page_count;
        Bootstrap {
            meta,
            selection: MetaSelection::ProvenCurrent,
            selected_meta_page: 0,
            committed_bytes: page_count * PAGE_SIZE as u64,
            physical_bytes: page_count * PAGE_SIZE as u64,
        }
    }

    #[test]
    fn slice_adapter_reports_exact_positional_short_read() {
        let bytes = [0u8; 2 * PAGE_SIZE + 17];
        let source = SlicePageSource::new(&bytes, 3);
        let mut page = [0u8; PAGE_SIZE];
        assert_eq!(
            source.read_page(2, &mut page),
            Err(PageSourceError::ShortRead {
                offset: (2 * PAGE_SIZE) as u64,
                expected: PAGE_SIZE,
                actual: 17,
            })
        );
    }

    #[test]
    fn pinned_source_preserves_io_and_fork_evidence_exactly() {
        let io = PageSourceError::Io(PageIoEvidence {
            kind: PageIoKind::PermissionDenied,
            raw_os_error: Some(13),
        });
        let source = InjectedPositional {
            access: None,
            read: Some(io),
        };
        let pinned = PinnedPageSource::new(&source, bootstrap(3)).unwrap();
        assert_eq!(pinned.read_page(2, &mut [0; PAGE_SIZE]), Err(io));

        let source = InjectedPositional {
            access: Some(PageSourceError::ForkedHandle),
            read: None,
        };
        let pinned = PinnedPageSource::new(&source, bootstrap(3)).unwrap();
        assert_eq!(pinned.check_access(), Err(PageSourceError::ForkedHandle));
        assert_eq!(
            pinned.read_page(2, &mut [0; PAGE_SIZE]),
            Err(PageSourceError::ForkedHandle)
        );
    }
}
