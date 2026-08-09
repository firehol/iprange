//! Checked raw access to retained file mappings.

use std::fmt;
use std::fs::File;
use std::marker::PhantomData;
use std::ptr;

use memmap2::{MmapOptions, MmapRaw};

use crate::contract::PAGE_SIZE;
use crate::error::{combine_errors, Error, Result};

/// Read-only bytes without requiring a Rust reference into mapped storage.
pub(crate) trait ByteSource: Copy {
    fn len(self) -> usize;
    fn byte(self, at: usize) -> Option<u8>;

    /// Read an array whose complete range the caller already checked.
    ///
    /// # Safety
    ///
    /// `at..at + N` must be inside this source.
    unsafe fn array_unchecked<const N: usize>(self, at: usize) -> [u8; N] {
        let mut output = [0; N];
        for (index, byte) in output.iter_mut().enumerate() {
            *byte = self
                .byte(at + index)
                .expect("caller checked the byte-source range");
        }
        output
    }

    fn is_empty(self) -> bool {
        self.len() == 0
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        let end = at.checked_add(N)?;
        if end > self.len() {
            return None;
        }
        // SAFETY: The complete range was checked above.
        Some(unsafe { self.array_unchecked(at) })
    }

    fn equals(self, at: usize, expected: &[u8]) -> bool {
        at.checked_add(expected.len())
            .is_some_and(|end| end <= self.len())
            && expected
                .iter()
                .enumerate()
                .all(|(index, byte)| self.byte(at + index) == Some(*byte))
    }

    fn all_zero(self, at: usize, len: usize) -> bool {
        at.checked_add(len).is_some_and(|end| end <= self.len())
            && (at..at + len).all(|index| self.byte(index) == Some(0))
    }

    fn same(self, other: Self, at: usize, len: usize) -> bool {
        at.checked_add(len)
            .is_some_and(|end| end <= self.len() && end <= other.len())
            && (at..at + len).all(|index| self.byte(index) == other.byte(index))
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        let Some(end) = at.checked_add(output.len()) else {
            return false;
        };
        if end > self.len() {
            return false;
        }
        for (index, byte) in output.iter_mut().enumerate() {
            let Some(value) = self.byte(at + index) else {
                return false;
            };
            *byte = value;
        }
        true
    }

    /// Copy into a caller-validated raw destination.
    ///
    /// # Safety
    ///
    /// `destination` must be writable for `len` bytes. The caller must permit
    /// overlap when the source can name the same mapping.
    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        let Some(end) = at.checked_add(len) else {
            return false;
        };
        if end > self.len() {
            return false;
        }
        for index in 0..len {
            let Some(value) = self.byte(at + index) else {
                return false;
            };
            // SAFETY: Required by this method's contract.
            unsafe { ptr::write(destination.add(index), value) };
        }
        true
    }
}

impl ByteSource for &[u8] {
    fn len(self) -> usize {
        <[u8]>::len(self)
    }

    fn byte(self, at: usize) -> Option<u8> {
        self.get(at).copied()
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        self.get(at..at.checked_add(N)?)?.try_into().ok()
    }

    unsafe fn array_unchecked<const N: usize>(self, at: usize) -> [u8; N] {
        let mut output = [0; N];
        // SAFETY: The caller guarantees that the complete source range exists.
        unsafe { ptr::copy_nonoverlapping(self.as_ptr().add(at), output.as_mut_ptr(), N) };
        output
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        self.get(at..at.saturating_add(output.len()))
            .is_some_and(|bytes| {
                output.copy_from_slice(bytes);
                true
            })
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        let Some(source) = self.get(at..at.saturating_add(len)) else {
            return false;
        };
        // SAFETY: The source range was checked and the destination contract is
        // carried by the caller.
        unsafe { ptr::copy_nonoverlapping(source.as_ptr(), destination, len) };
        true
    }
}

impl<const N: usize> ByteSource for &[u8; N] {
    fn len(self) -> usize {
        N
    }

    fn byte(self, at: usize) -> Option<u8> {
        self.get(at).copied()
    }

    fn array<const M: usize>(self, at: usize) -> Option<[u8; M]> {
        self.get(at..at.checked_add(M)?)?.try_into().ok()
    }

    unsafe fn array_unchecked<const M: usize>(self, at: usize) -> [u8; M] {
        // SAFETY: Forwarded with the same caller contract.
        unsafe { self.as_slice().array_unchecked(at) }
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        self.as_slice().copy_range_to(at, output)
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        // SAFETY: Forwarded with the same caller contract.
        unsafe { self.as_slice().copy_range_to_ptr(at, destination, len) }
    }
}

/// A checked subrange of any read-only byte source.
#[derive(Clone, Copy, Debug)]
pub(crate) struct ByteRange<S> {
    source: S,
    at: u32,
    len: u32,
}

impl<S: ByteSource> ByteRange<S> {
    pub(crate) fn new(source: S, at: usize, len: usize) -> Option<Self> {
        let end = at.checked_add(len)?;
        if end > source.len() {
            return None;
        }
        Some(Self {
            source,
            at: u32::try_from(at).ok()?,
            len: u32::try_from(len).ok()?,
        })
    }

    pub(crate) const fn source_offset(self) -> usize {
        self.at as usize
    }
}

#[cfg(test)]
impl<'a, const N: usize> ByteRange<&'a [u8; N]> {
    pub(crate) fn as_slice(self) -> &'a [u8] {
        let at = self.at as usize;
        &self.source[at..at + self.len as usize]
    }
}

impl<S: ByteSource> ByteSource for ByteRange<S> {
    fn len(self) -> usize {
        self.len as usize
    }

    fn byte(self, at: usize) -> Option<u8> {
        (at < self.len())
            .then(|| self.source_offset().checked_add(at))
            .flatten()
            .and_then(|index| self.source.byte(index))
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        let end = at.checked_add(N)?;
        if end > self.len() {
            return None;
        }
        // SAFETY: Construction checked this subrange inside `source`, and the
        // relative range was checked above.
        Some(unsafe { self.source.array_unchecked(self.source_offset() + at) })
    }

    unsafe fn array_unchecked<const N: usize>(self, at: usize) -> [u8; N] {
        // SAFETY: Construction checked this subrange inside `source`, and the
        // caller guarantees that the relative range is inside this subrange.
        unsafe { self.source.array_unchecked(self.source_offset() + at) }
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        at.checked_add(output.len())
            .is_some_and(|end| end <= self.len())
            && self.source.copy_range_to(self.source_offset() + at, output)
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        if !at.checked_add(len).is_some_and(|end| end <= self.len()) {
            return false;
        }
        // SAFETY: The subrange was checked and the caller owns the destination.
        unsafe {
            self.source
                .copy_range_to_ptr(self.source_offset() + at, destination, len)
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Access {
    ReadOnly,
    ReadWrite,
}

/// One retained file and its current mapping.
pub(crate) struct Mapping {
    file: Option<File>,
    map: Option<MmapRaw>,
    len: usize,
    access: Access,
    unreadable_pages: Option<Box<[u32]>>,
}

impl fmt::Debug for Mapping {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("Mapping")
            .field("retains_file", &self.file.is_some())
            .field("len", &self.len)
            .field("access", &self.access)
            .field(
                "unreadable_pages",
                &self
                    .unreadable_pages
                    .as_ref()
                    .map_or(0, |pages| pages.len()),
            )
            .finish()
    }
}

impl Mapping {
    pub(crate) fn read_only(file: File, len: u64) -> Result<Self> {
        Self::new(file, len, Access::ReadOnly)
    }

    pub(crate) fn read_write(file: File, len: u64) -> Result<Self> {
        Self::new(file, len, Access::ReadWrite)
    }

    fn new(file: File, len: u64, access: Access) -> Result<Self> {
        let len = checked_len(len)?;
        require_file_extent(&file, len)?;
        let map = map_nonempty(&file, len, access)?;
        Ok(Self {
            file: Some(file),
            map,
            len,
            access,
            unreadable_pages: None,
        })
    }

    /// Establish a read-only mapping while the containing source retains the
    /// file handle and its lifetime lock.
    pub(crate) fn read_only_view(file: &File, len: u64) -> Result<Self> {
        Self::view(file, len, Access::ReadOnly)
    }

    /// Establish a writable mapping while the containing artifact retains its
    /// file handle and serializes mapped mutation.
    pub(crate) fn read_write_view(file: &File, len: u64) -> Result<Self> {
        Self::view(file, len, Access::ReadWrite)
    }

    fn view(file: &File, len: u64, access: Access) -> Result<Self> {
        let len = checked_len(len)?;
        require_file_extent(file, len)?;
        let map = map_nonempty(file, len, access)?;
        Ok(Self {
            file: None,
            map,
            len,
            access,
            unreadable_pages: None,
        })
    }

    pub(crate) fn file(&self) -> &File {
        self.file
            .as_ref()
            .expect("owned database mapping retains its file")
    }

    pub(crate) fn into_file(mut self) -> File {
        self.map = None;
        self.file
            .take()
            .expect("owned database mapping retains its file")
    }

    pub(crate) fn unmap(&mut self) {
        self.map = None;
        self.len = 0;
    }

    pub(crate) fn len(&self) -> u64 {
        self.len as u64
    }

    pub(crate) fn region(&self) -> Result<(*const u8, usize)> {
        Ok((self.base()?, self.len))
    }

    pub(crate) fn set_unreadable_pages(&mut self, pages: &[u32]) -> Result<()> {
        if pages.windows(2).any(|pair| pair[0] >= pair[1]) {
            return Err(Error::InvalidArgument(
                "unreadable mapped pages must be sorted and unique",
            ));
        }
        self.unreadable_pages = (!pages.is_empty()).then(|| pages.into());
        Ok(())
    }

    pub(crate) fn page(&self, page_number: u32, page_limit: u64) -> Result<PageView<'_>> {
        if self
            .unreadable_pages
            .as_deref()
            .is_some_and(|pages| pages.binary_search(&page_number).is_ok())
        {
            return Err(Error::Io(std::io::Error::from_raw_os_error(libc::EIO)));
        }
        let offset = page_offset(page_number, page_limit, self.len)?;
        let base = self.base()?;
        // SAFETY: `offset..offset + PAGE_SIZE` was checked inside this live map.
        let pointer = unsafe { base.add(offset) };
        crate::work::page_visited(1);
        Ok(PageView::new(pointer))
    }

    pub(crate) fn bytes(&self, offset: u64, len: usize) -> Result<BytesView<'_>> {
        let (offset, len) = checked_range(offset, len as u64, self.len)?;
        let base = self.base()?;
        // SAFETY: The requested range was checked inside this live map.
        Ok(BytesView::new(unsafe { base.add(offset) }, len))
    }

    pub(crate) fn bytes_mut(&mut self, offset: u64, len: usize) -> Result<BytesMut<'_>> {
        self.require_write()?;
        let (offset, len) = checked_range(offset, len as u64, self.len)?;
        let base = self.base_mut()?;
        // SAFETY: The requested range was checked inside this exclusive map.
        Ok(BytesMut::new(unsafe { base.add(offset) }, len))
    }

    pub(crate) fn page_mut(&mut self, page_number: u32, page_limit: u64) -> Result<PageMut<'_>> {
        self.require_write()?;
        let offset = page_offset(page_number, page_limit, self.len)?;
        let base = self.base_mut()?;
        // SAFETY: `offset..offset + PAGE_SIZE` was checked inside this live map,
        // and the exclusive mapping borrow owns mutation for the returned view.
        let pointer = unsafe { base.add(offset) };
        crate::work::page_visited(1);
        Ok(PageMut::new(pointer))
    }

    pub(crate) fn page_pair(
        &mut self,
        source: u32,
        destination: u32,
        page_limit: u64,
    ) -> Result<(PageView<'_>, PageMut<'_>)> {
        self.require_write()?;
        if source == destination {
            return Err(Error::InvalidArgument(
                "mapped source and destination pages must differ",
            ));
        }
        let source_offset = page_offset(source, page_limit, self.len)?;
        let destination_offset = page_offset(destination, page_limit, self.len)?;
        let base = self.base_mut()?;
        // SAFETY: Both disjoint page ranges were checked inside this live map.
        let source = unsafe { base.add(source_offset).cast_const() };
        // SAFETY: See above; distinct page numbers make the ranges disjoint.
        let destination = unsafe { base.add(destination_offset) };
        crate::work::page_visited(2);
        Ok((PageView::new(source), PageMut::new(destination)))
    }

    pub(crate) fn flush_range(&self, offset: u64, len: u64) -> Result<()> {
        let (offset, len) = checked_range(offset, len, self.len)?;
        self.raw()?.flush_range(offset, len)?;
        crate::work::mapping_flush(1);
        Ok(())
    }

    pub(crate) fn flush_page(&self, page_number: u32, page_limit: u64) -> Result<()> {
        let offset = page_offset(page_number, page_limit, self.len)?;
        self.raw()?.flush_range(offset, PAGE_SIZE)?;
        crate::work::mapping_flush(1);
        Ok(())
    }

    pub(crate) fn sync_file(&self) -> Result<()> {
        sync_file(self.file())?;
        crate::work::file_sync(1);
        Ok(())
    }

    /// Drop the current map, resize the file, and establish a replacement map.
    pub(crate) fn resize(&mut self, len: u64) -> Result<()> {
        self.require_write()?;
        let len = checked_len(len)?;
        let previous = self.len;
        self.map = None;
        self.file().set_len(len as u64)?;
        self.replace_map(len)?;
        if len > previous {
            crate::work::mapping_growth(1);
        }
        Ok(())
    }

    /// Shrink to one committed extent, retaining an aligned Windows tail when
    /// another mapped view makes truncation impossible.
    pub(crate) fn shrink_or_retain(&mut self, len: u64) -> Result<u64> {
        self.require_write()?;
        let mapped_len = checked_len(len)?;
        if self.len == mapped_len && self.file().metadata()?.len() == len {
            return Ok(len);
        }
        self.map = None;
        match shrink_file_or_retain(self.file(), len) {
            Ok(physical_len) => self.replace_map(mapped_len).map(|()| physical_len),
            Err(cause) => Err(combine_errors(cause, self.replace_map(mapped_len))),
        }
    }

    fn replace_map(&mut self, len: usize) -> Result<()> {
        match map_nonempty(self.file(), len, self.access) {
            Ok(map) => {
                self.map = map;
                self.len = len;
                crate::work::mapping_remap(1);
                Ok(())
            }
            Err(error) => {
                self.len = 0;
                Err(error)
            }
        }
    }

    /// Re-establish a map after a failed resize without changing the file.
    pub(crate) fn remap(&mut self, len: u64) -> Result<()> {
        let len = checked_len(len)?;
        require_file_extent(self.file(), len)?;
        self.map = None;
        self.replace_map(len)
    }

    fn raw(&self) -> Result<&MmapRaw> {
        match self.map.as_ref() {
            Some(map) => Ok(map),
            None => Err(Error::WrongState("file mapping is unavailable")),
        }
    }

    fn base(&self) -> Result<*const u8> {
        Ok(self.raw()?.as_ptr())
    }

    fn base_mut(&mut self) -> Result<*mut u8> {
        Ok(self.raw()?.as_mut_ptr())
    }

    fn require_write(&self) -> Result<()> {
        if self.access == Access::ReadWrite {
            Ok(())
        } else {
            Err(Error::WrongMode("file mapping is read-only"))
        }
    }
}

pub(crate) fn shrink_file_or_retain(file: &File, len: u64) -> Result<u64> {
    let physical_len = file.metadata()?.len();
    if physical_len < len {
        return Err(Error::Corrupt(
            "main file is shorter than its committed generation",
        ));
    }
    if physical_len == len {
        return Ok(len);
    }
    match file.set_len(len) {
        Ok(()) => Ok(len),
        Err(source) if mapped_view_prevents_shrink(&source) => Ok(physical_len),
        Err(source) => Err(source.into()),
    }
}

#[cfg(windows)]
fn mapped_view_prevents_shrink(source: &std::io::Error) -> bool {
    use windows_sys::Win32::Foundation::ERROR_USER_MAPPED_FILE;

    source.raw_os_error().map(|code| code as u32) == Some(ERROR_USER_MAPPED_FILE)
}

#[cfg(not(windows))]
fn mapped_view_prevents_shrink(_source: &std::io::Error) -> bool {
    false
}

/// Raw read-only bytes tied to one live mapping borrow.
#[derive(Clone, Copy)]
pub(crate) struct BytesView<'a> {
    pointer: *const u8,
    len: usize,
    _mapping: PhantomData<&'a Mapping>,
}

impl fmt::Debug for BytesView<'_> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("BytesView")
            .field("len", &self.len)
            .finish()
    }
}

impl<'a> BytesView<'a> {
    fn new(pointer: *const u8, len: usize) -> Self {
        Self {
            pointer,
            len,
            _mapping: PhantomData,
        }
    }

    pub(crate) const fn len(self) -> usize {
        self.len
    }

    pub(crate) fn byte(self, at: usize) -> Option<u8> {
        (at < self.len).then(|| {
            // SAFETY: The index was checked and the mapping borrow is live.
            unsafe { ptr::read(self.pointer.add(at)) }
        })
    }

    pub(crate) fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        let end = at.checked_add(N)?;
        if end > self.len {
            return None;
        }
        let mut output = [0; N];
        // SAFETY: Source and destination are valid, disjoint ranges of `N` bytes.
        unsafe { ptr::copy_nonoverlapping(self.pointer.add(at), output.as_mut_ptr(), N) };
        Some(output)
    }

    pub(crate) fn bytes(self, at: usize, len: usize) -> Option<Self> {
        let end = at.checked_add(len)?;
        if end > self.len {
            return None;
        }
        // SAFETY: `at` is within the live parent view.
        Some(Self::new(unsafe { self.pointer.add(at) }, len))
    }

    pub(crate) fn copy_to(self, output: &mut [u8]) -> bool {
        if output.len() != self.len {
            return false;
        }
        // SAFETY: Both ranges are valid and cannot overlap because `output` is
        // ordinary caller-owned memory outside the raw mapping view.
        unsafe { ptr::copy_nonoverlapping(self.pointer, output.as_mut_ptr(), self.len) };
        true
    }

    pub(crate) const fn as_ptr(self) -> *const u8 {
        self.pointer
    }
}

impl ByteSource for BytesView<'_> {
    fn len(self) -> usize {
        self.len()
    }

    fn byte(self, at: usize) -> Option<u8> {
        self.byte(at)
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        self.array(at)
    }

    unsafe fn array_unchecked<const N: usize>(self, at: usize) -> [u8; N] {
        let mut output = [0; N];
        // SAFETY: The caller guarantees that the complete source range exists.
        unsafe { ptr::copy_nonoverlapping(self.pointer.add(at), output.as_mut_ptr(), N) };
        output
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        self.bytes(at, output.len())
            .is_some_and(|bytes| bytes.copy_to(output))
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        let Some(source) = self.bytes(at, len) else {
            return false;
        };
        // SAFETY: Both ranges are valid. `copy` permits mapped overlap.
        unsafe { ptr::copy(source.as_ptr(), destination, len) };
        true
    }
}

/// Raw read-only access to exactly one mapped database page.
#[derive(Clone, Copy, Debug)]
pub(crate) struct PageView<'a> {
    pointer: *const u8,
    _mapping: PhantomData<&'a Mapping>,
}

impl<'a> PageView<'a> {
    fn new(pointer: *const u8) -> Self {
        Self {
            pointer,
            _mapping: PhantomData,
        }
    }

    pub(crate) fn byte(self, at: usize) -> Option<u8> {
        (at < PAGE_SIZE).then(|| {
            // SAFETY: The index was checked inside the live mapped page.
            unsafe { ptr::read(self.pointer.add(at)) }
        })
    }

    pub(crate) fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        let end = at.checked_add(N)?;
        if end > PAGE_SIZE {
            return None;
        }
        let mut output = [0; N];
        // SAFETY: Source and destination are valid, disjoint ranges of `N` bytes.
        unsafe { ptr::copy_nonoverlapping(self.pointer.add(at), output.as_mut_ptr(), N) };
        Some(output)
    }

    pub(crate) fn range(self, at: usize, len: usize) -> Option<BytesView<'a>> {
        let end = at.checked_add(len)?;
        if end > PAGE_SIZE {
            return None;
        }
        // SAFETY: `at` is within the live mapped page.
        Some(BytesView::new(unsafe { self.pointer.add(at) }, len))
    }
}

impl ByteSource for PageView<'_> {
    fn len(self) -> usize {
        PAGE_SIZE
    }

    fn byte(self, at: usize) -> Option<u8> {
        self.byte(at)
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        self.array(at)
    }

    unsafe fn array_unchecked<const N: usize>(self, at: usize) -> [u8; N] {
        let mut output = [0; N];
        // SAFETY: The caller guarantees that the complete source range exists.
        unsafe { ptr::copy_nonoverlapping(self.pointer.add(at), output.as_mut_ptr(), N) };
        output
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        self.range(at, output.len())
            .is_some_and(|bytes| bytes.copy_to(output))
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        let Some(source) = self.range(at, len) else {
            return false;
        };
        // SAFETY: Both ranges are valid. `copy` permits mapped overlap.
        unsafe { ptr::copy(source.as_ptr(), destination, len) };
        true
    }
}

/// Exclusive raw mutation of exactly one mapped database page.
pub(crate) struct PageMut<'a> {
    pointer: *mut u8,
    _mapping: PhantomData<&'a mut Mapping>,
}

/// Exclusive raw mutation of one checked mapped byte range.
pub(crate) struct BytesMut<'a> {
    pointer: *mut u8,
    len: usize,
    _mapping: PhantomData<&'a mut Mapping>,
}

impl<'a> BytesMut<'a> {
    fn new(pointer: *mut u8, len: usize) -> Self {
        Self {
            pointer,
            len,
            _mapping: PhantomData,
        }
    }

    pub(crate) fn fill(&mut self, value: u8) {
        if value == 0 {
            crate::work::bytes_zeroed(self.len as u64);
        } else {
            crate::work::bytes_moved(self.len as u64);
        }
        // SAFETY: This view exclusively owns the checked mapped range.
        unsafe { ptr::write_bytes(self.pointer, value, self.len) };
    }

    pub(crate) fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        let len = checked_subrange(at, bytes.len(), self.len)?;
        crate::work::bytes_moved(len as u64);
        // SAFETY: The destination was checked and safe slices cannot alias it.
        unsafe { ptr::copy_nonoverlapping(bytes.as_ptr(), self.pointer.add(at), len) };
        Ok(())
    }

    pub(crate) fn put_u64(&mut self, at: usize, value: u64) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }
}

impl fmt::Debug for PageMut<'_> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output.write_str("PageMut")
    }
}

impl<'a> PageMut<'a> {
    fn new(pointer: *mut u8) -> Self {
        Self {
            pointer,
            _mapping: PhantomData,
        }
    }

    pub(crate) fn view(&self) -> PageView<'_> {
        PageView::new(self.pointer.cast_const())
    }

    pub(crate) fn fill(&mut self, value: u8) {
        if value == 0 {
            crate::work::bytes_zeroed(PAGE_SIZE as u64);
        } else {
            crate::work::bytes_moved(PAGE_SIZE as u64);
        }
        // SAFETY: This view exclusively owns the mapped page range.
        unsafe { ptr::write_bytes(self.pointer, value, PAGE_SIZE) };
    }

    pub(crate) fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        let len = checked_subrange(at, bytes.len(), PAGE_SIZE)?;
        crate::work::bytes_moved(len as u64);
        // SAFETY: The destination range was checked and the input cannot alias
        // the raw mapping through a safe Rust slice.
        unsafe { ptr::copy_nonoverlapping(bytes.as_ptr(), self.pointer.add(at), len) };
        Ok(())
    }

    pub(crate) fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()> {
        let len = checked_subrange(at, bytes.len(), PAGE_SIZE)?;
        crate::work::bytes_moved(len as u64);
        // SAFETY: The destination was checked inside this exclusive mapped
        // page. `ByteSource` permits overlap for mapped sources.
        if unsafe { bytes.copy_range_to_ptr(0, self.pointer.add(at), len) } {
            Ok(())
        } else {
            Err(Error::Corrupt("mapped source changed while copying"))
        }
    }

    pub(crate) fn zero(&mut self, at: usize, len: usize) -> Result<()> {
        let len = checked_subrange(at, len, PAGE_SIZE)?;
        crate::work::bytes_zeroed(len as u64);
        // SAFETY: The destination range was checked inside the exclusive page.
        unsafe { ptr::write_bytes(self.pointer.add(at), 0, len) };
        Ok(())
    }

    pub(crate) fn copy_within(
        &mut self,
        source_at: usize,
        destination_at: usize,
        len: usize,
    ) -> Result<()> {
        checked_subrange(source_at, len, PAGE_SIZE)?;
        checked_subrange(destination_at, len, PAGE_SIZE)?;
        crate::work::bytes_moved(len as u64);
        // SAFETY: Both ranges were checked; `ptr::copy` permits overlap.
        unsafe {
            ptr::copy(
                self.pointer.add(source_at),
                self.pointer.add(destination_at),
                len,
            )
        };
        Ok(())
    }

    pub(crate) fn set_byte(&mut self, at: usize, value: u8) -> Result<()> {
        checked_subrange(at, 1, PAGE_SIZE)?;
        crate::work::bytes_moved(1);
        // SAFETY: The one-byte destination was checked.
        unsafe { ptr::write(self.pointer.add(at), value) };
        Ok(())
    }

    pub(crate) fn put_u16(&mut self, at: usize, value: u16) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }

    pub(crate) fn put_u32(&mut self, at: usize, value: u32) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }

    pub(crate) fn put_u64(&mut self, at: usize, value: u64) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }

    pub(crate) const fn as_ptr(&self) -> *const u8 {
        self.pointer.cast_const()
    }
}

fn checked_len(len: u64) -> Result<usize> {
    usize::try_from(len).map_err(|_| Error::ArithmeticOverflow("mapping length"))
}

fn require_file_extent(file: &File, len: usize) -> Result<()> {
    if file.metadata()?.len() < len as u64 {
        Err(Error::Corrupt("mapping exceeds the file extent"))
    } else {
        Ok(())
    }
}

fn map_nonempty(file: &File, len: usize, access: Access) -> Result<Option<MmapRaw>> {
    if len == 0 {
        return Ok(None);
    }
    let mut options = MmapOptions::new();
    options.len(len);
    match access {
        Access::ReadOnly => Ok(Some(options.map_raw_read_only(file)?)),
        Access::ReadWrite => Ok(Some(options.map_raw(file)?)),
    }
}

fn page_offset(page_number: u32, page_limit: u64, map_len: usize) -> Result<usize> {
    if u64::from(page_number) >= page_limit {
        return Err(Error::Corrupt("page number is outside mapped bounds"));
    }
    let offset = (page_number as usize)
        .checked_mul(PAGE_SIZE)
        .ok_or_else(|| Error::arithmetic_overflow("mapped page offset"))?;
    checked_subrange(offset, PAGE_SIZE, map_len)?;
    Ok(offset)
}

fn checked_range(offset: u64, len: u64, extent: usize) -> Result<(usize, usize)> {
    let offset =
        usize::try_from(offset).map_err(|_| Error::ArithmeticOverflow("mapped range offset"))?;
    let len = usize::try_from(len).map_err(|_| Error::ArithmeticOverflow("mapped range length"))?;
    checked_subrange(offset, len, extent)?;
    Ok((offset, len))
}

fn checked_subrange(at: usize, len: usize, extent: usize) -> Result<usize> {
    if at.checked_add(len).is_some_and(|end| end <= extent) {
        Ok(len)
    } else {
        Err(Error::ArithmeticOverflow("mapped byte range"))
    }
}

#[cfg(target_os = "macos")]
fn sync_file(file: &File) -> Result<()> {
    use std::os::fd::AsRawFd;

    // SAFETY: `file` retains a valid descriptor for the duration of the call.
    let result = unsafe { libc::fcntl(file.as_raw_fd(), libc::F_FULLFSYNC) };
    if result == -1 {
        Err(std::io::Error::last_os_error().into())
    } else {
        Ok(())
    }
}

#[cfg(not(target_os = "macos"))]
fn sync_file(file: &File) -> Result<()> {
    file.sync_all()?;
    Ok(())
}

#[cfg(test)]
#[path = "mapping_test.rs"]
pub(crate) mod test_support;

#[cfg(test)]
mod tests {
    use std::fs::{self, OpenOptions};
    use std::path::PathBuf;
    use std::sync::atomic::{AtomicU64, Ordering};

    use crate::contract::u64_le;

    use super::*;

    static NEXT_FILE: AtomicU64 = AtomicU64::new(0);

    #[cfg(target_pointer_width = "64")]
    #[derive(Clone, Copy)]
    struct LargeSource;

    #[cfg(target_pointer_width = "64")]
    impl ByteSource for LargeSource {
        fn len(self) -> usize {
            usize::try_from(u64::from(u32::MAX) + 2).unwrap()
        }

        fn byte(self, _at: usize) -> Option<u8> {
            None
        }
    }

    struct TestFile(PathBuf);

    impl TestFile {
        fn new() -> (Self, File) {
            let suffix = NEXT_FILE.fetch_add(1, Ordering::Relaxed);
            let path = std::env::temp_dir()
                .join(format!("iprange-v4-map-{}-{suffix}", std::process::id()));
            let file = OpenOptions::new()
                .read(true)
                .write(true)
                .create_new(true)
                .open(&path)
                .unwrap();
            (Self(path), file)
        }
    }

    impl Drop for TestFile {
        fn drop(&mut self) {
            let _ = fs::remove_file(&self.0);
        }
    }

    #[test]
    fn exact_page_and_subrange_views_carry_no_redundant_extent() {
        assert_eq!(
            std::mem::size_of::<PageView<'static>>(),
            std::mem::size_of::<usize>()
        );
        assert_eq!(
            std::mem::size_of::<ByteRange<PageView<'static>>>(),
            std::mem::size_of::<usize>() + 2 * std::mem::size_of::<u32>()
        );

        let bytes = [1, 2, 3, 4];
        let range = ByteRange::new(&bytes, 1, 2).unwrap();
        assert_eq!(range.array::<2>(0), Some([2, 3]));
        assert_eq!(range.array::<2>(1), None);
    }

    #[cfg(target_pointer_width = "64")]
    #[test]
    fn compact_subrange_rejects_unrepresentable_offsets_and_lengths() {
        let beyond_u32 = u32::MAX as usize + 1;
        assert!(ByteRange::new(LargeSource, beyond_u32, 0).is_none());
        assert!(ByteRange::new(LargeSource, 0, beyond_u32).is_none());
        assert!(ByteRange::new(LargeSource, u32::MAX as usize, 1).is_some());
    }

    #[test]
    fn raw_pages_are_checked_and_persisted() {
        let (_path, file) = TestFile::new();
        file.set_len((3 * PAGE_SIZE) as u64).unwrap();
        let mut mapping = Mapping::read_write(file, (3 * PAGE_SIZE) as u64).unwrap();

        {
            let mut page = mapping.page_mut(2, 3).unwrap();
            page.fill(0);
            page.put_u64(17, 0x8877_6655_4433_2211).unwrap();
        }
        mapping.flush_page(2, 3).unwrap();
        mapping.sync_file().unwrap();

        let file = mapping.into_file();
        let mapping = Mapping::read_only(file, (3 * PAGE_SIZE) as u64).unwrap();
        assert_eq!(
            u64_le(mapping.page(2, 3).unwrap(), 17),
            0x8877_6655_4433_2211
        );
        assert!(mapping.page(3, 3).is_err());
    }

    #[test]
    fn mapped_page_pair_copies_without_an_owned_page() {
        let (_path, file) = TestFile::new();
        file.set_len((2 * PAGE_SIZE) as u64).unwrap();
        let mut mapping = Mapping::read_write(file, (2 * PAGE_SIZE) as u64).unwrap();
        {
            let mut source = mapping.page_mut(0, 2).unwrap();
            source.fill(0);
            source.write(100, b"mapped").unwrap();
        }
        {
            let (source, mut destination) = mapping.page_pair(0, 1, 2).unwrap();
            destination.fill(0);
            destination
                .write_source(200, source.range(100, 6).unwrap())
                .unwrap();
        }
        assert!(mapping
            .page(1, 2)
            .unwrap()
            .range(200, 6)
            .unwrap()
            .equals(0, b"mapped"));
    }

    #[test]
    fn resize_unmaps_before_changing_the_extent() {
        let (_path, file) = TestFile::new();
        file.set_len((2 * PAGE_SIZE) as u64).unwrap();
        let mut mapping = Mapping::read_write(file, (2 * PAGE_SIZE) as u64).unwrap();
        mapping.resize((4 * PAGE_SIZE) as u64).unwrap();
        assert_eq!(mapping.len(), (4 * PAGE_SIZE) as u64);
        mapping.page_mut(3, 4).unwrap().set_byte(0, 9).unwrap();
        mapping.resize((3 * PAGE_SIZE) as u64).unwrap();
        assert_eq!(
            mapping.file().metadata().unwrap().len(),
            (3 * PAGE_SIZE) as u64
        );
    }

    #[cfg(windows)]
    #[test]
    fn windows_shrink_retains_capacity_until_other_views_close() {
        let (_path, file) = TestFile::new();
        file.set_len((4 * PAGE_SIZE) as u64).unwrap();
        let mut writer = Mapping::read_write(file, (4 * PAGE_SIZE) as u64).unwrap();
        let reader_file = writer.file().try_clone().unwrap();
        let reader = Mapping::read_only(reader_file, (2 * PAGE_SIZE) as u64).unwrap();

        assert_eq!(
            writer.shrink_or_retain((3 * PAGE_SIZE) as u64).unwrap(),
            (4 * PAGE_SIZE) as u64
        );
        assert_eq!(writer.len(), (3 * PAGE_SIZE) as u64);
        assert_eq!(
            writer.file().metadata().unwrap().len(),
            (4 * PAGE_SIZE) as u64
        );

        drop(reader);
        assert_eq!(
            writer.shrink_or_retain((3 * PAGE_SIZE) as u64).unwrap(),
            (3 * PAGE_SIZE) as u64
        );
        assert_eq!(
            writer.file().metadata().unwrap().len(),
            (3 * PAGE_SIZE) as u64
        );
    }
}
