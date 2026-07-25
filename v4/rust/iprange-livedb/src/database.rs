//! Immutable database bootstrap.

use std::fs::{self, File, OpenOptions};
use std::path::Path;

#[cfg(unix)]
use std::os::unix::fs::OpenOptionsExt;

use crate::bootstrap::{self, Bootstrap, MetaSelection, OpenMode};
use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::feed_catalog::{self, FeedCursor};
use crate::feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
use crate::file_io;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::membership_view::{self, MembershipView};
use crate::metadata;
use crate::path;
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, RangeDirection};
use crate::range_tree;
use crate::{
    live_lock::{self, Mode},
    live_sidecar::{self, MAIN_LIFETIME_LOCK},
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

impl DatabaseInfo {
    fn from_bootstrap(bootstrap: Bootstrap) -> Self {
        let meta = bootstrap.meta;
        Self {
            address_family: meta.address_family,
            value_kind: meta.value_kind,
            value_tag: meta.value_tag,
            database_id: meta.database_id,
            transaction_id: meta.txn_id,
            commit_nonce: meta.commit_nonce,
            page_count: meta.page_count,
            range_record_count: meta.range_record_count,
            active_feed_count: meta.active_feed_count,
            meta_selection: bootstrap.selection,
        }
    }
}

/// Reader pinned to one immutable file generation.
#[derive(Debug)]
pub struct ImmutableReader {
    core: ReaderCore,
}

#[derive(Debug)]
pub(crate) struct ReaderCore {
    file: File,
    bootstrap: Bootstrap,
}

impl ImmutableReader {
    /// Open a sidecar-free immutable v4 file without validating its page graph.
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref();
        let sidecar = path::canonical_sidecar(path)?;
        require_sidecar_absent(&sidecar)?;

        let (file, identity) = open_immutable_source(path, &sidecar)?;
        let bootstrap = select_immutable_generation(path, &sidecar, &file, identity)?;
        Ok(Self {
            core: ReaderCore { file, bootstrap },
        })
    }

    /// Identity and counters from the selected metadata page.
    pub fn info(&self) -> DatabaseInfo {
        self.core.info()
    }

    /// Look up one address in an IPv4 direct-value database.
    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.core.lookup_direct_v4(address)
    }

    /// Look up one address in an IPv6 direct-value database.
    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.core.lookup_direct_v6(address)
    }

    /// Open an ordered cursor over an IPv4 direct-value database.
    pub fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.core.direct_cursor_v4(direction)
    }

    /// Open an ordered cursor over an IPv6 direct-value database.
    pub fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.core.direct_cursor_v6(direction)
    }

    /// Look up one exact feed name in a membership database.
    pub fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        self.core.lookup_feed(name)
    }

    /// Enumerate feeds in ascending feed-index order.
    pub fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        self.core.feed_cursor()
    }

    /// Open an ordered cursor over one exact IPv4 named feed.
    pub fn feed_range_cursor_v4(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV4<'_>> {
        self.core.feed_range_cursor_v4(name, direction)
    }

    /// Open an ordered cursor over one exact IPv6 named feed.
    pub fn feed_range_cursor_v6(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV6<'_>> {
        self.core.feed_range_cursor_v6(name, direction)
    }

    /// Look up one address in an IPv4 membership database.
    pub fn lookup_membership_v4(&self, address: Ipv4Key) -> Result<Option<MembershipView<'_>>> {
        self.core.lookup_membership_v4(address, None)
    }

    /// Look up one address in an IPv6 membership database.
    pub fn lookup_membership_v6(&self, address: Ipv6Key) -> Result<Option<MembershipView<'_>>> {
        self.core.lookup_membership_v6(address, None)
    }

    /// Exact decompressed metadata length, or absence.
    pub fn metadata_json_len(&self) -> Option<u64> {
        self.core.metadata_json_len()
    }

    /// Fill caller storage with the exact opaque metadata bytes.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.core.read_metadata_json(output)
    }

    /// Return the complete bounded metadata value, or absence.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.core.metadata_json()
    }

    pub(crate) fn import_parts(&self) -> (&File, MetaV4) {
        self.core.import_parts()
    }
}

fn open_immutable_source(path: &Path, sidecar: &Path) -> Result<(File, live_sidecar::Identity)> {
    let file = open_read_only(path)?;
    let identity = live_sidecar::identity_any_link(&file)?;
    live_lock::lock(&file, MAIN_LIFETIME_LOCK, Mode::Shared)?;
    live_sidecar::verify_path_any_link(path, identity)?;
    require_sidecar_absent(sidecar)?;
    Ok((file, identity))
}

fn select_immutable_generation(
    path: &Path,
    sidecar: &Path,
    file: &File,
    identity: live_sidecar::Identity,
) -> Result<Bootstrap> {
    let bootstrap = bootstrap_file(file, OpenMode::ImmutableReader)?;
    live_sidecar::verify_path_any_link(path, identity)?;
    require_sidecar_absent(sidecar)?;
    Ok(bootstrap)
}

impl ReaderCore {
    pub(crate) fn new(file: File, bootstrap: Bootstrap) -> Self {
        Self { file, bootstrap }
    }

    pub(crate) fn info(&self) -> DatabaseInfo {
        DatabaseInfo::from_bootstrap(self.bootstrap)
    }

    pub(crate) fn import_parts(&self) -> (&File, MetaV4) {
        (&self.file, self.bootstrap.meta)
    }

    pub(crate) const fn file(&self) -> &File {
        &self.file
    }

    pub(crate) fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.require_direct(AddressFamily::Ipv4)?;
        range_tree::lookup(&self.file, &self.bootstrap.meta, address)
    }

    pub(crate) fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.require_direct(AddressFamily::Ipv6)?;
        range_tree::lookup(&self.file, &self.bootstrap.meta, address)
    }

    pub(crate) fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.require_direct(AddressFamily::Ipv4)?;
        DirectCursorV4::new(&self.file, &self.bootstrap.meta, direction)
    }

    pub(crate) fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.require_direct(AddressFamily::Ipv6)?;
        DirectCursorV6::new(&self.file, &self.bootstrap.meta, direction)
    }

    pub(crate) fn direct_cursor_v4_live(
        &self,
        direction: RangeDirection,
        owner_pid: u32,
    ) -> Result<DirectCursorV4<'_>> {
        self.require_direct(AddressFamily::Ipv4)?;
        DirectCursorV4::new_live(&self.file, &self.bootstrap.meta, direction, owner_pid)
    }

    pub(crate) fn direct_cursor_v6_live(
        &self,
        direction: RangeDirection,
        owner_pid: u32,
    ) -> Result<DirectCursorV6<'_>> {
        self.require_direct(AddressFamily::Ipv6)?;
        DirectCursorV6::new_live(&self.file, &self.bootstrap.meta, direction, owner_pid)
    }

    pub(crate) fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        feed_catalog::require_membership(&self.bootstrap.meta)?;
        let name = crate::feed::FeedName::new(name)?;
        feed_catalog::lookup(&self.file, &self.bootstrap.meta, &name)
    }

    pub(crate) fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        feed_catalog::require_membership(&self.bootstrap.meta)?;
        FeedCursor::new(&self.file, &self.bootstrap.meta)
    }

    pub(crate) fn feed_cursor_live(&self, owner_pid: u32) -> Result<FeedCursor<'_>> {
        feed_catalog::require_membership(&self.bootstrap.meta)?;
        FeedCursor::new_live(&self.file, &self.bootstrap.meta, owner_pid)
    }

    pub(crate) fn feed_range_cursor_v4(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV4<'_>> {
        self.require_membership_family(AddressFamily::Ipv4)?;
        let feed = self.require_feed(name)?;
        FeedRangeCursorV4::new(&self.file, &self.bootstrap.meta, feed.index, direction)
    }

    pub(crate) fn feed_range_cursor_v6(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV6<'_>> {
        self.require_membership_family(AddressFamily::Ipv6)?;
        let feed = self.require_feed(name)?;
        FeedRangeCursorV6::new(&self.file, &self.bootstrap.meta, feed.index, direction)
    }

    pub(crate) fn feed_range_cursor_v4_live(
        &self,
        name: &str,
        direction: RangeDirection,
        owner_pid: u32,
    ) -> Result<FeedRangeCursorV4<'_>> {
        self.require_membership_family(AddressFamily::Ipv4)?;
        let feed = self.require_feed(name)?;
        FeedRangeCursorV4::new_live(
            &self.file,
            &self.bootstrap.meta,
            feed.index,
            direction,
            owner_pid,
        )
    }

    pub(crate) fn feed_range_cursor_v6_live(
        &self,
        name: &str,
        direction: RangeDirection,
        owner_pid: u32,
    ) -> Result<FeedRangeCursorV6<'_>> {
        self.require_membership_family(AddressFamily::Ipv6)?;
        let feed = self.require_feed(name)?;
        FeedRangeCursorV6::new_live(
            &self.file,
            &self.bootstrap.meta,
            feed.index,
            direction,
            owner_pid,
        )
    }

    pub(crate) fn lookup_membership_v4(
        &self,
        address: Ipv4Key,
        owner_pid: Option<u32>,
    ) -> Result<Option<MembershipView<'_>>> {
        membership_view::lookup_v4(&self.file, &self.bootstrap.meta, address, owner_pid)
    }

    pub(crate) fn lookup_membership_v6(
        &self,
        address: Ipv6Key,
        owner_pid: Option<u32>,
    ) -> Result<Option<MembershipView<'_>>> {
        membership_view::lookup_v6(&self.file, &self.bootstrap.meta, address, owner_pid)
    }

    pub(crate) fn metadata_json_len(&self) -> Option<u64> {
        (self.bootstrap.meta.metadata_root != 0)
            .then_some(self.bootstrap.meta.metadata_uncompressed_len)
    }

    pub(crate) fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        metadata::read(&self.file, &self.bootstrap.meta, output)
    }

    pub(crate) fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        metadata::read_vec(&self.file, &self.bootstrap.meta)
    }

    fn require_direct(&self, family: AddressFamily) -> Result<()> {
        if self.bootstrap.meta.value_kind != ValueKind::Direct {
            return Err(Error::InvalidArgument(
                "direct lookup requires a direct-value database",
            ));
        }
        if self.bootstrap.meta.address_family != family {
            return Err(Error::InvalidArgument(
                "lookup address family does not match the database",
            ));
        }
        Ok(())
    }

    fn require_membership_family(&self, family: AddressFamily) -> Result<()> {
        feed_catalog::require_membership(&self.bootstrap.meta)?;
        if self.bootstrap.meta.address_family != family {
            return Err(Error::InvalidArgument(
                "feed cursor address family does not match the database",
            ));
        }
        Ok(())
    }

    fn require_feed(&self, name: &str) -> Result<FeedEntry> {
        let name = crate::feed::FeedName::new(name)?;
        feed_catalog::lookup(&self.file, &self.bootstrap.meta, &name)?.ok_or(Error::NameNotFound)
    }
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
    let mut metas = [0u8; 2 * PAGE_SIZE];
    file_io::read_exact_at(file, &mut metas, 0)?;
    let meta0 = (&metas[..PAGE_SIZE]).try_into().unwrap();
    let meta1 = (&metas[PAGE_SIZE..]).try_into().unwrap();
    Ok(bootstrap::open_meta_pages(
        meta0,
        meta1,
        physical_bytes,
        mode,
    )?)
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

#[cfg(not(unix))]
pub(crate) fn open_read_only(_path: &Path) -> Result<File> {
    Err(Error::Unsupported(
        "safe no-follow file open is not implemented on this platform",
    ))
}

#[cfg(test)]
#[path = "database_tests.rs"]
mod tests;
