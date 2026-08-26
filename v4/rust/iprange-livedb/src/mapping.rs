//! Checked raw access to retained file mappings.

use std::fmt;
use std::fs::File;

use memmap2::{MmapOptions, MmapRaw};

use crate::contract::PAGE_SIZE;
use crate::error::{combine_errors, Error, Result};

pub(crate) use crate::mapped_bytes::{
    ByteRange, ByteSource, BytesMut, BytesView, PageMut, PageView,
};

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

    #[inline]
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
        self.flush_prefix(offset + len)?;
        crate::work::mapping_flush(1);
        Ok(())
    }

    pub(crate) fn flush_page(&self, page_number: u32, page_limit: u64) -> Result<()> {
        let offset = page_offset(page_number, page_limit, self.len)?;
        self.flush_prefix(offset + PAGE_SIZE)?;
        crate::work::mapping_flush(1);
        Ok(())
    }

    /// Synchronize the mapped base prefix [0, bytes) to the file.
    ///
    /// This is the chosen native flush shape: the whole range from the
    /// mapping base through the end of the request. XNU rejects
    /// subranges that are not aligned to the hardware page boundary
    /// with EINVAL (verified natively on darwin 25.5, Apple Silicon
    /// 16 KiB pages: [4K:4K] fails while [16K:16K] and [32K:16K]
    /// succeed), so the literal-subrange shape of memmap2's
    /// flush_range is not portable; the base prefix [0, offset+len) is
    /// the one shape verified on linux, darwin, and freebsd and is the
    /// same shape the Go mapping uses. Pages before the request are
    /// already clean in the durability flows, so the wider msync is a
    /// no-op scan there. The caller proves bytes lands inside the
    /// mapped extent; the mapping base is always page-aligned.
    #[cfg(unix)]
    fn flush_prefix(&self, bytes: usize) -> Result<()> {
        // SAFETY: `bytes` was bounded inside the mapped extent by the
        // caller's checked_range or page_offset, and the mapping base
        // is page-aligned, so the msync arguments are valid.
        let result = unsafe {
            libc::msync(
                self.base()? as *mut libc::c_void,
                bytes as libc::size_t,
                libc::MS_SYNC,
            )
        };
        if result == 0 {
            Ok(())
        } else {
            Err(std::io::Error::last_os_error().into())
        }
    }

    /// Windows arm of the native base-prefix flush (Rust
    /// flush_prefix): FlushViewOfFile over [0, bytes), with the
    /// file-buffer flush left to sync_file exactly like the unix arm.
    #[cfg(windows)]
    fn flush_prefix(&self, bytes: usize) -> Result<()> {
        use windows_sys::Win32::System::Memory::FlushViewOfFile;

        // SAFETY: same bound as the unix arm; the view base is the
        // mapping start and `bytes` was proven inside the extent.
        let result = unsafe { FlushViewOfFile(self.base()? as *const core::ffi::c_void, bytes) };
        if result == 0 {
            Err(std::io::Error::last_os_error().into())
        } else {
            Ok(())
        }
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

pub(super) fn checked_subrange(at: usize, len: usize, extent: usize) -> Result<usize> {
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

    #[test]
    fn contiguous_zero_checks_cover_unaligned_words_and_tails() {
        let mut bytes = [0u8; 33];
        for at in 0..=bytes.len() {
            assert!(bytes.as_slice().all_zero(at, bytes.len() - at));
        }
        for changed in 0..bytes.len() {
            bytes[changed] = 1;
            assert!(!bytes.as_slice().all_zero(0, bytes.len()));
            let range = ByteRange::new(&bytes, changed, 1).unwrap();
            assert!(!range.all_zero(0, 1));
            bytes[changed] = 0;
        }
        assert!(!bytes.as_slice().all_zero(bytes.len(), 1));
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

    /// The native flush shape is the base prefix [0, offset+len): the
    /// one syscall shape verified on linux, darwin, and freebsd. On
    /// darwin with 16 KiB hardware pages this pins that every 4 KiB
    /// position of the requested range is accepted by XNU msync
    /// (memmap2's literal-subrange flush produces the [16K:4K]-style
    /// shapes XNU rejects with EINVAL); on every platform it also
    /// proves the flushed bytes survive a reopen after sync_file.
    #[test]
    fn flush_pins_the_base_prefix_shape_at_every_page_position() {
        let (_path, file) = TestFile::new();
        let pages = 16u64;
        file.set_len(pages * PAGE_SIZE as u64).unwrap();
        let mut mapping = Mapping::read_write(file, pages * PAGE_SIZE as u64).unwrap();

        for page in 0..pages {
            let mut page_view = mapping.page_mut(page as u32, pages).unwrap();
            page_view.put_u64(0, page * PAGE_SIZE as u64).unwrap();
        }

        // Every 4 KiB-aligned request shape, including the ones whose
        // literal subrange would be misaligned on 16 KiB hardware
        // pages.
        let mapped = pages * PAGE_SIZE as u64;
        for offset in (0..mapped).step_by(PAGE_SIZE) {
            for len in [
                PAGE_SIZE as u64,
                2 * PAGE_SIZE as u64,
                3 * PAGE_SIZE as u64,
                4 * PAGE_SIZE as u64,
            ] {
                if offset + len > mapped {
                    continue;
                }
                mapping.flush_range(offset, len).unwrap();
            }
        }
        for page in 0..pages {
            mapping.flush_page(page as u32, pages).unwrap();
        }
        mapping.sync_file().unwrap();

        let file = mapping.into_file();
        let mapping = Mapping::read_only(file, pages * PAGE_SIZE as u64).unwrap();
        for page in 0..pages {
            let offset = page * PAGE_SIZE as u64;
            assert_eq!(u64_le(mapping.page(page as u32, pages).unwrap(), 0), offset);
        }
    }

    /// flush_range refuses requests that reach past the mapped extent
    /// (Rust checked_range -> ArithmeticOverflow("mapped byte range")).
    /// The Go port refuses the same shape with CodeFormatInvalid ("flush
    /// range out of mapped extent", the mapping package's documented
    /// geometry class); no production caller can reach either arm
    /// because every caller pre-validates in-extent, page-aligned
    /// ranges.
    #[test]
    fn flush_range_rejects_ranges_beyond_the_mapping() {
        let (_path, file) = TestFile::new();
        file.set_len((2 * PAGE_SIZE) as u64).unwrap();
        let mapping = Mapping::read_write(file, (2 * PAGE_SIZE) as u64).unwrap();
        assert!(matches!(
            mapping.flush_range(PAGE_SIZE as u64, 2 * PAGE_SIZE as u64),
            Err(Error::ArithmeticOverflow(_))
        ));
        assert!(mapping.flush_range(0, 2 * PAGE_SIZE as u64).is_ok());
    }
}
