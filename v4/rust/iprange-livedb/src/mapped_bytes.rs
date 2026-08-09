//! Checked byte and page views into one retained file mapping.

use std::fmt;
use std::marker::PhantomData;
use std::ptr;

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};

use super::mapping::{checked_subrange, Mapping};

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
    pub(super) fn new(pointer: *const u8, len: usize) -> Self {
        Self {
            pointer,
            len,
            _mapping: PhantomData,
        }
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
}

/// Raw read-only access to exactly one mapped database page.
#[derive(Clone, Copy, Debug)]
pub(crate) struct PageView<'a> {
    pointer: *const u8,
    _mapping: PhantomData<&'a Mapping>,
}

impl<'a> PageView<'a> {
    pub(super) fn new(pointer: *const u8) -> Self {
        Self {
            pointer,
            _mapping: PhantomData,
        }
    }

    pub(crate) fn byte(self, at: usize) -> Option<u8> {
        read_byte(self.pointer, PAGE_SIZE, at)
    }

    pub(crate) fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        read_array(self.pointer, PAGE_SIZE, at)
    }

    pub(crate) fn range(self, at: usize, len: usize) -> Option<BytesView<'a>> {
        let pointer = subrange(self.pointer, PAGE_SIZE, at, len)?;
        Some(BytesView::new(pointer, len))
    }
}

trait MappedSource: Copy {
    fn pointer(self) -> *const u8;
    fn extent(self) -> usize;
}

impl MappedSource for BytesView<'_> {
    fn pointer(self) -> *const u8 {
        self.pointer
    }

    fn extent(self) -> usize {
        self.len
    }
}

impl MappedSource for PageView<'_> {
    fn pointer(self) -> *const u8 {
        self.pointer
    }

    fn extent(self) -> usize {
        PAGE_SIZE
    }
}

impl<S: MappedSource> ByteSource for S {
    fn len(self) -> usize {
        self.extent()
    }

    fn byte(self, at: usize) -> Option<u8> {
        read_byte(self.pointer(), self.extent(), at)
    }

    fn array<const N: usize>(self, at: usize) -> Option<[u8; N]> {
        read_array(self.pointer(), self.extent(), at)
    }

    unsafe fn array_unchecked<const N: usize>(self, at: usize) -> [u8; N] {
        // SAFETY: Forwarded with the same caller contract.
        unsafe { read_array_unchecked(self.pointer(), at) }
    }

    fn copy_range_to(self, at: usize, output: &mut [u8]) -> bool {
        let Some(source) = subrange(self.pointer(), self.extent(), at, output.len()) else {
            return false;
        };
        // SAFETY: The source was checked and cannot alias a safe output slice.
        unsafe { ptr::copy_nonoverlapping(source, output.as_mut_ptr(), output.len()) };
        true
    }

    unsafe fn copy_range_to_ptr(self, at: usize, destination: *mut u8, len: usize) -> bool {
        // SAFETY: Forwarded with the same caller contract.
        unsafe { copy_to_ptr(self.pointer(), self.extent(), at, destination, len) }
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
    pub(super) fn new(pointer: *mut u8, len: usize) -> Self {
        Self {
            pointer,
            len,
            _mapping: PhantomData,
        }
    }

    pub(crate) fn fill(&mut self, value: u8) {
        fill(self.pointer, self.len, value);
    }

    pub(crate) fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        write(self.pointer, self.len, at, bytes)
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
    pub(super) fn new(pointer: *mut u8) -> Self {
        Self {
            pointer,
            _mapping: PhantomData,
        }
    }

    pub(crate) fn view(&self) -> PageView<'_> {
        PageView::new(self.pointer.cast_const())
    }

    pub(crate) fn fill(&mut self, value: u8) {
        fill(self.pointer, PAGE_SIZE, value);
    }

    pub(crate) fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        write(self.pointer, PAGE_SIZE, at, bytes)
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

#[inline]
fn read_byte(pointer: *const u8, len: usize, at: usize) -> Option<u8> {
    (at < len).then(|| {
        // SAFETY: The index was checked inside the live mapped range.
        unsafe { ptr::read(pointer.add(at)) }
    })
}

#[inline]
fn read_array<const N: usize>(pointer: *const u8, len: usize, at: usize) -> Option<[u8; N]> {
    if at.checked_add(N)? > len {
        return None;
    }
    // SAFETY: The complete source range was checked above.
    Some(unsafe { read_array_unchecked(pointer, at) })
}

#[inline]
unsafe fn read_array_unchecked<const N: usize>(pointer: *const u8, at: usize) -> [u8; N] {
    let mut output = [0; N];
    // SAFETY: The caller guarantees that the complete source range exists.
    unsafe { ptr::copy_nonoverlapping(pointer.add(at), output.as_mut_ptr(), N) };
    output
}

#[inline]
fn subrange(pointer: *const u8, extent: usize, at: usize, len: usize) -> Option<*const u8> {
    if at.checked_add(len)? > extent {
        return None;
    }
    // SAFETY: `at` is within the live parent view.
    Some(unsafe { pointer.add(at) })
}

#[inline]
unsafe fn copy_to_ptr(
    pointer: *const u8,
    extent: usize,
    at: usize,
    destination: *mut u8,
    len: usize,
) -> bool {
    let Some(source) = subrange(pointer, extent, at, len) else {
        return false;
    };
    // SAFETY: Both ranges are valid. `copy` permits mapped overlap.
    unsafe { ptr::copy(source, destination, len) };
    true
}

#[inline]
fn fill(pointer: *mut u8, len: usize, value: u8) {
    if value == 0 {
        crate::work::bytes_zeroed(len as u64);
    } else {
        crate::work::bytes_moved(len as u64);
    }
    // SAFETY: The view exclusively owns the checked mapped range.
    unsafe { ptr::write_bytes(pointer, value, len) };
}

#[inline]
fn write(pointer: *mut u8, extent: usize, at: usize, bytes: &[u8]) -> Result<()> {
    let len = checked_subrange(at, bytes.len(), extent)?;
    crate::work::bytes_moved(len as u64);
    // SAFETY: The destination was checked and safe slices cannot alias it.
    unsafe { ptr::copy_nonoverlapping(bytes.as_ptr(), pointer.add(at), len) };
    Ok(())
}
