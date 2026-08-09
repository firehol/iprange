//! Immutable database bootstrap.

use std::fs::{self, File, OpenOptions};
use std::path::Path;

#[cfg(unix)]
use std::os::unix::fs::OpenOptionsExt;
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt, OpenOptionsExt};

use crate::bootstrap::{self, Bootstrap, MetaSelection, OpenMode};
use crate::contract::{AddressFamily, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::feed_catalog::FeedCursor;
use crate::feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::membership_view::MembershipView;
use crate::path;
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, RangeDirection};
use crate::reader_core::ReaderCore;
use crate::{
    live_lock::{self, Mode},
    live_sidecar::MAIN_LIFETIME_LOCK,
};

/// Public logical identity and selected generation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DatabaseInfo {
    pub address_family: AddressFamily,
    pub value_kind: ValueKind,
    pub value_tag: ValueTag,
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub page_count: u64,
    pub range_record_count: u64,
    pub active_feed_count: u64,
    pub meta_selection: MetaSelection,
}

/// Reader pinned to one immutable file generation.
#[derive(Debug)]
pub struct ImmutableReader {
    core: ReaderCore,
}

impl ImmutableReader {
    /// Open a sidecar-free immutable v4 file without validating its page graph.
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref();
        let sidecar = path::canonical_sidecar(path)?;
        require_sidecar_absent(&sidecar)?;

        let (file, identity) = open_immutable_source(path, &sidecar)?;
        let (mapping, bootstrap) = select_immutable_generation(path, &sidecar, file, identity)?;
        Ok(Self {
            core: ReaderCore::new(mapping, bootstrap, None),
        })
    }

    /// Identity and counters from the selected metadata page.
    pub fn info(&self) -> DatabaseInfo {
        self.core.info()
    }

    /// Look up one address in an IPv4 direct-value database.
    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.core.read().lookup_direct_v4(address)
    }

    /// Look up one address in an IPv6 direct-value database.
    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.core.read().lookup_direct_v6(address)
    }

    /// Open an ordered cursor over an IPv4 direct-value database.
    pub fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.core.read().direct_cursor_v4(direction)
    }

    /// Open an ordered cursor over an IPv6 direct-value database.
    pub fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.core.read().direct_cursor_v6(direction)
    }

    /// Look up one exact feed name in a membership database.
    pub fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        self.core.read().lookup_feed(name)
    }

    /// Enumerate feeds in ascending feed-index order.
    pub fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        self.core.read().feed_cursor()
    }

    /// Open an ordered cursor over one exact IPv4 named feed.
    pub fn feed_range_cursor_v4(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV4<'_>> {
        self.core.read().feed_range_cursor_v4(name, direction)
    }

    /// Open an ordered cursor over one exact IPv6 named feed.
    pub fn feed_range_cursor_v6(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV6<'_>> {
        self.core.read().feed_range_cursor_v6(name, direction)
    }

    /// Look up one address in an IPv4 membership database.
    pub fn lookup_membership_v4(&self, address: Ipv4Key) -> Result<Option<MembershipView<'_>>> {
        self.core.read().lookup_membership_v4(address)
    }

    /// Look up one address in an IPv6 membership database.
    pub fn lookup_membership_v6(&self, address: Ipv6Key) -> Result<Option<MembershipView<'_>>> {
        self.core.read().lookup_membership_v6(address)
    }

    /// Exact decompressed metadata length, or absence.
    pub fn metadata_json_len(&self) -> Option<u64> {
        self.core.read().metadata_json_len()
    }

    /// Fill caller storage with the exact opaque metadata bytes.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.core.read().read_metadata_json(output)
    }

    /// Return the complete bounded metadata value, or absence.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.core.read().metadata_json()
    }

    pub(crate) fn core(&self) -> &ReaderCore {
        &self.core
    }
}

fn open_immutable_source(
    path: &Path,
    sidecar: &Path,
) -> Result<(File, crate::live_namespace::Identity)> {
    let file = open_read_only(path)?;
    let identity = crate::live_namespace::identity_any_link(&file)?;
    live_lock::lock_file(&file, MAIN_LIFETIME_LOCK, Mode::Shared)?;
    crate::live_namespace::verify_path_any_link(path, identity)?;
    require_sidecar_absent(sidecar)?;
    Ok((file, identity))
}

fn select_immutable_generation(
    path: &Path,
    sidecar: &Path,
    file: File,
    identity: crate::live_namespace::Identity,
) -> Result<(Mapping, Bootstrap)> {
    let (mapping, bootstrap) = map_reader(file, OpenMode::ImmutableReader)?;
    crate::live_cleanup::require_main_available(path, identity, bootstrap.meta.database_id)?;
    crate::live_namespace::verify_path_any_link(path, identity)?;
    require_sidecar_absent(sidecar)?;
    Ok((mapping, bootstrap))
}

pub(crate) fn map_reader(file: File, mode: OpenMode) -> Result<(Mapping, Bootstrap)> {
    require_regular_file(&file)?;
    let physical_bytes = file.metadata()?.len();
    if physical_bytes < (2 * PAGE_SIZE) as u64 {
        return Err(bootstrap::BootstrapError::FileTooShort.into());
    }
    if physical_bytes % PAGE_SIZE as u64 != 0 {
        return Err(bootstrap::BootstrapError::FileUnaligned.into());
    }
    let mut mapping = Mapping::read_only(file, (2 * PAGE_SIZE) as u64)?;
    let bootstrap = bootstrap_mapping(&mapping, physical_bytes, mode)?;
    mapping.remap(bootstrap.committed_bytes)?;
    Ok((mapping, bootstrap))
}

pub(crate) fn map_writer(file: File) -> Result<(Mapping, Bootstrap)> {
    require_regular_file(&file)?;
    let physical_bytes = file.metadata()?.len();
    if physical_bytes < (2 * PAGE_SIZE) as u64 {
        return Err(bootstrap::BootstrapError::FileTooShort.into());
    }
    if physical_bytes % PAGE_SIZE as u64 != 0 {
        return Err(bootstrap::BootstrapError::FileUnaligned.into());
    }
    let mut mapping = Mapping::read_write(file, (2 * PAGE_SIZE) as u64)?;
    let bootstrap = bootstrap_mapping(&mapping, physical_bytes, OpenMode::Writer)?;
    mapping.remap(bootstrap.committed_bytes)?;
    Ok((mapping, bootstrap))
}

pub(crate) fn bootstrap_mapping(
    mapping: &Mapping,
    physical_bytes: u64,
    mode: OpenMode,
) -> Result<Bootstrap> {
    let page0 = mapping.page(0, 2)?;
    let page1 = mapping.page(1, 2)?;
    Ok(bootstrap::open_meta_pages(
        page0,
        page1,
        physical_bytes,
        mode,
    )?)
}

pub(crate) fn bootstrap_file(file: &File, mode: OpenMode) -> Result<Bootstrap> {
    require_regular_file(file)?;
    let physical_bytes = file.metadata()?.len();
    if physical_bytes < (2 * PAGE_SIZE) as u64 {
        return Err(bootstrap::BootstrapError::FileTooShort.into());
    }
    if physical_bytes % PAGE_SIZE as u64 != 0 {
        return Err(bootstrap::BootstrapError::FileUnaligned.into());
    }
    let mapping = Mapping::read_only_view(file, (2 * PAGE_SIZE) as u64)?;
    bootstrap_mapping(&mapping, physical_bytes, mode)
}

pub(crate) fn bootstrap_file_faultable(file: &File, mode: OpenMode) -> Result<Bootstrap> {
    require_regular_file(file)?;
    let physical_bytes = file.metadata()?.len();
    if physical_bytes < (2 * PAGE_SIZE) as u64 {
        return Err(bootstrap::BootstrapError::FileTooShort.into());
    }
    if physical_bytes % PAGE_SIZE as u64 != 0 {
        return Err(bootstrap::BootstrapError::FileUnaligned.into());
    }
    let mapping = Mapping::read_only_view(file, (2 * PAGE_SIZE) as u64)?;
    crate::worker::probe_source(&mapping, || {
        let pages = faultable_meta_pages(&mapping)?;
        Ok(bootstrap::open_meta_pages(
            pages[0],
            pages[1],
            physical_bytes,
            mode,
        )?)
    })
}

pub(crate) fn database_id_from_file_faultable(file: &File) -> Result<[u8; 16]> {
    let mapping = Mapping::read_only_view(file, (2 * PAGE_SIZE) as u64)?;
    crate::worker::probe_source(&mapping, || {
        let pages = faultable_meta_pages(&mapping)?;
        Ok(bootstrap::database_id_from_meta_pages(pages[0], pages[1])?)
    })
}

#[derive(Clone, Copy)]
enum FaultableMetaPage<'a> {
    Mapped(crate::mapping::PageView<'a>),
    Unreadable,
}

impl crate::mapping::ByteSource for FaultableMetaPage<'_> {
    fn len(self) -> usize {
        match self {
            Self::Mapped(_) => PAGE_SIZE,
            Self::Unreadable => 0,
        }
    }

    fn byte(self, at: usize) -> Option<u8> {
        match self {
            Self::Mapped(page) => page.byte(at),
            Self::Unreadable => None,
        }
    }
}

fn faultable_meta_pages(mapping: &Mapping) -> Result<[FaultableMetaPage<'_>; 2]> {
    let page = |number| {
        if crate::worker::source_page_unreadable(number) {
            Ok(FaultableMetaPage::Unreadable)
        } else {
            mapping.page(number, 2).map(FaultableMetaPage::Mapped)
        }
    };
    Ok([page(0)?, page(1)?])
}

pub(crate) fn require_regular_file(file: &File) -> Result<()> {
    if !file.metadata()?.file_type().is_file() {
        return Err(Error::InvalidArgument(
            "database path is not a regular file",
        ));
    }
    Ok(())
}

pub(crate) fn require_sidecar_absent(sidecar: &Path) -> Result<()> {
    match fs::symlink_metadata(sidecar) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error.into()),
        Ok(_) => Err(Error::WrongMode(
            "immutable open requires the canonical .readers sidecar to be absent",
        )),
    }
}

#[cfg(unix)]
pub(crate) fn open_read_only(path: &Path) -> Result<File> {
    let mut options = OpenOptions::new();
    options.read(true);
    options.custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC);
    Ok(options.open(path)?)
}

#[cfg(windows)]
pub(crate) fn open_read_only(path: &Path) -> Result<File> {
    use windows_sys::Win32::Storage::FileSystem::{
        FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_OPEN_REPARSE_POINT, FILE_SHARE_DELETE,
        FILE_SHARE_READ, FILE_SHARE_WRITE,
    };

    let mut options = OpenOptions::new();
    options
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT);
    let file = options.open(path)?;
    if file.metadata()?.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return Err(Error::WrongMode("database path is a Windows reparse point"));
    }
    Ok(file)
}

#[cfg(not(any(unix, windows)))]
pub(crate) fn open_read_only(_path: &Path) -> Result<File> {
    Err(Error::Unsupported(
        "safe no-follow file open is unavailable",
    ))
}

#[cfg(test)]
#[path = "database_tests.rs"]
mod tests;
