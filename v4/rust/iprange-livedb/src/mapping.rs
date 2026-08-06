//! Checked raw access to retained file mappings.

use std::fmt;
use std::fs::File;
use std::marker::PhantomData;
use std::ptr;

use memmap2::{MmapOptions, MmapRaw};

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};

/// Read-only bytes without requiring a Rust reference into mapped storage.
pub(crate) trait ByteSource: Copy {
    fn len(self) -> usize;
    fn byte(self, at: usize) -> Option<u8>;

    fn is_empty(self) -> bool {
        self.len() == 0
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        let end = at.checked_add(N)?;
        if end > self.len() {
            return None;
        }
        let mut output = [0; N];
        for (index, byte) in output.iter_mut().enumerate() {
            *byte = self.byte(at + index)?;
        }
        Some(output)
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
    at: usize,
    len: usize,
}

impl<S: ByteSource> ByteRange<S> {
    pub(crate) fn new(source: S, at: usize, len: usize) -> Option<Self> {
        at.checked_add(len)
            .is_some_and(|end| end <= source.len())
            .then_some(Self { source, at, len })
    }

    pub(crate) const fn source_offset(self) -> usize {
        self.at
    }
}

#[cfg(test)]
impl<'a, const N: usize> ByteRange<&'a [u8; N]> {
    pub(crate) fn as_slice(self) -> &'a [u8] {
        &self.source[self.at..self.at + self.len]
    }
}

impl<S: ByteSource> ByteSource for ByteRange<S> {
    fn len(self) -> usize {
        self.len
    }

    fn byte(self, at: usize) -> Option<u8> {
        (at < self.len)
            .then(|| self.at.checked_add(at))
            .flatten()
            .and_then(|index| self.source.byte(index))
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        let end = at.checked_add(N)?;
        (end <= self.len)
            .then(|| self.source.array(self.at + at))
            .flatten()
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        at.checked_add(output.len())
            .is_some_and(|end| end <= self.len)
            && self.source.copy_range_to(self.at + at, output)
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        if !at.checked_add(len).is_some_and(|end| end <= self.len) {
            return false;
        }
        // SAFETY: The subrange was checked and the caller owns the destination.
        unsafe {
            self.source
                .copy_range_to_ptr(self.at + at, destination, len)
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
}

impl fmt::Debug for Mapping {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("Mapping")
            .field("retains_file", &self.file.is_some())
            .field("len", &self.len)
            .field("access", &self.access)
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

    pub(crate) fn len(&self) -> u64 {
        self.len as u64
    }

    pub(crate) fn page(&self, page_number: u32, page_limit: u64) -> Result<PageView<'_>> {
        let offset = page_offset(page_number, page_limit, self.len)?;
        let base = self.base()?;
        // SAFETY: `offset..offset + PAGE_SIZE` was checked inside this live map.
        let pointer = unsafe { base.add(offset) };
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
        Ok((PageView::new(source), PageMut::new(destination)))
    }

    pub(crate) fn flush_range(&self, offset: u64, len: u64) -> Result<()> {
        let (offset, len) = checked_range(offset, len, self.len)?;
        self.raw()?.flush_range(offset, len)?;
        Ok(())
    }

    pub(crate) fn flush_page(&self, page_number: u32, page_limit: u64) -> Result<()> {
        let offset = page_offset(page_number, page_limit, self.len)?;
        self.raw()?.flush_range(offset, PAGE_SIZE)?;
        Ok(())
    }

    pub(crate) fn sync_file(&self) -> Result<()> {
        sync_file(self.file())
    }

    /// Drop the current map, resize the file, and establish a replacement map.
    pub(crate) fn resize(&mut self, len: u64) -> Result<()> {
        self.require_write()?;
        let len = checked_len(len)?;
        self.map = None;
        self.file().set_len(len as u64)?;
        match map_nonempty(self.file(), len, self.access) {
            Ok(map) => {
                self.map = map;
                self.len = len;
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
        match map_nonempty(self.file(), len, self.access) {
            Ok(map) => {
                self.map = map;
                self.len = len;
                Ok(())
            }
            Err(error) => {
                self.len = 0;
                Err(error)
            }
        }
    }

    fn raw(&self) -> Result<&MmapRaw> {
        self.map
            .as_ref()
            .ok_or(Error::WrongState("file mapping is unavailable"))
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
pub(crate) struct PageView<'a>(BytesView<'a>);

impl<'a> PageView<'a> {
    fn new(pointer: *const u8) -> Self {
        Self(BytesView::new(pointer, PAGE_SIZE))
    }

    pub(crate) fn byte(self, at: usize) -> Option<u8> {
        self.0.byte(at)
    }

    pub(crate) fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        self.0.array(at)
    }

    pub(crate) fn range(self, at: usize, len: usize) -> Option<BytesView<'a>> {
        self.0.bytes(at, len)
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

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        self.range(at, output.len())
            .is_some_and(|bytes| bytes.copy_to(output))
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        // SAFETY: Forwarded with the same caller contract.
        unsafe { self.0.copy_range_to_ptr(at, destination, len) }
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
        // SAFETY: This view exclusively owns the checked mapped range.
        unsafe { ptr::write_bytes(self.pointer, value, self.len) };
    }

    pub(crate) fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        let len = checked_subrange(at, bytes.len(), self.len)?;
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
        // SAFETY: This view exclusively owns the mapped page range.
        unsafe { ptr::write_bytes(self.pointer, value, PAGE_SIZE) };
    }

    pub(crate) fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        let len = checked_subrange(at, bytes.len(), PAGE_SIZE)?;
        // SAFETY: The destination range was checked and the input cannot alias
        // the raw mapping through a safe Rust slice.
        unsafe { ptr::copy_nonoverlapping(bytes.as_ptr(), self.pointer.add(at), len) };
        Ok(())
    }

    pub(crate) fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()> {
        let len = checked_subrange(at, bytes.len(), PAGE_SIZE)?;
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
        .ok_or(Error::ArithmeticOverflow("mapped page offset"))?;
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
pub(crate) mod test_support {
    use super::*;

    pub(crate) fn map(file: &File) -> Result<Mapping> {
        Mapping::read_only_view(file, file.metadata()?.len())
    }

    pub(crate) fn read_exact_at(file: &File, output: &mut [u8], offset: u64) -> Result<()> {
        let mapping = map(file)?;
        if mapping.bytes(offset, output.len())?.copy_to(output) {
            Ok(())
        } else {
            Err(Error::Corrupt("test mapping changed while copying"))
        }
    }

    pub(crate) fn write_exact_at(file: &File, input: &[u8], offset: u64) -> Result<()> {
        let mut mapping = Mapping::read_write_view(file, file.metadata()?.len())?;
        mapping.bytes_mut(offset, input.len())?.write(0, input)
    }

    pub(crate) fn read_page(
        file: &File,
        page_number: u32,
        page_limit: u64,
        output: &mut [u8; PAGE_SIZE],
    ) -> Result<()> {
        let mapping = map(file)?;
        if mapping
            .page(page_number, page_limit)?
            .copy_range_to(0, output)
        {
            Ok(())
        } else {
            Err(Error::Corrupt("test page changed while copying"))
        }
    }
}

#[cfg(test)]
mod tests {
    use std::fs::{self, OpenOptions};
    use std::path::PathBuf;
    use std::sync::atomic::{AtomicU64, Ordering};

    use crate::contract::u64_le;

    use super::*;

    static NEXT_FILE: AtomicU64 = AtomicU64::new(0);

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
}
