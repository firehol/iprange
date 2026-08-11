//! Canonical mapping, bootstrap, and empty-image operations for v4 database files.

use std::fs::{self, File, OpenOptions};
use std::path::Path;

#[cfg(unix)]
use std::os::unix::fs::OpenOptionsExt;
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt, OpenOptionsExt};

use crate::bootstrap::{self, Bootstrap, OpenMode};
use crate::contract::{AddressFamily, MetaV4, StructureKind, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, Mapping};

#[derive(Clone, Copy)]
pub(crate) struct EmptySpec {
    pub(crate) address_family: AddressFamily,
    pub(crate) value_kind: ValueKind,
    pub(crate) structure_kind: StructureKind,
    pub(crate) value_tag: ValueTag,
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) feed_index_limit: u64,
}

impl EmptySpec {
    pub(crate) const fn live(
        address_family: AddressFamily,
        value_kind: ValueKind,
        structure_kind: StructureKind,
        value_tag: ValueTag,
        database_id: [u8; 16],
        commit_nonce: [u8; 16],
    ) -> Self {
        Self {
            address_family,
            value_kind,
            structure_kind,
            value_tag,
            database_id,
            transaction_id: 1,
            commit_nonce,
            feed_index_limit: 0,
        }
    }
}

pub(crate) fn empty_meta(spec: EmptySpec) -> MetaV4 {
    MetaV4 {
        address_family: spec.address_family,
        value_kind: spec.value_kind,
        structure_kind_code: spec.structure_kind as u8,
        value_tag: spec.value_tag,
        database_id: spec.database_id,
        txn_id: spec.transaction_id,
        commit_nonce: spec.commit_nonce,
        page_count: 2,
        range_record_count: 0,
        active_feed_count: 0,
        feed_index_limit: spec.feed_index_limit,
        membership_entry_count: 0,
        membership_id_limit: u64::from(matches!(
            spec.value_kind,
            ValueKind::Membership | ValueKind::Structured
        )),
        metadata_uncompressed_len: 0,
        metadata_compressed_len: 0,
        retired_extent_count: 0,
        range_root: 0,
        catalog_name_root: 0,
        catalog_index_root: 0,
        feed_used_root: 0,
        membership_id_root: 0,
        membership_hash_root: 0,
        membership_used_root: 0,
        metadata_root: 0,
        free_bitmap_root: 0,
        retirement_root: 0,
        allocator_reserve: [0; 4],
        structure_entry_count: 0,
        structure_id_limit: u64::from(spec.value_kind == ValueKind::Structured),
        structure_id_root: 0,
        structure_hash_root: 0,
        structure_used_root: 0,
    }
}

pub(crate) fn write_empty(file: &File, spec: EmptySpec) -> Result<()> {
    file.set_len((2 * PAGE_SIZE) as u64)?;
    let mut mapping = Mapping::read_write_view(file, (2 * PAGE_SIZE) as u64)?;
    let meta = empty_meta(spec);
    meta.encode_mapped(mapping.page_mut(0, 2)?)?;
    meta.encode_mapped(mapping.page_mut(1, 2)?)?;
    mapping.flush_range(0, (2 * PAGE_SIZE) as u64)?;
    file.sync_all()?;
    Ok(())
}

pub(crate) fn is_exact_empty(file: &File, spec: EmptySpec) -> Result<bool> {
    let opened = bootstrap_file(file, OpenMode::Writer)?;
    Ok(opened.physical_bytes == opened.committed_bytes && opened.meta == empty_meta(spec))
}

pub(crate) fn map_reader(file: File, mode: OpenMode) -> Result<(Mapping, Bootstrap)> {
    require_regular_file(&file)?;
    let physical_bytes = file.metadata()?.len();
    bootstrap::require_geometry(physical_bytes)?;
    let mut mapping = Mapping::read_only(file, (2 * PAGE_SIZE) as u64)?;
    let bootstrap = bootstrap_mapping(&mapping, physical_bytes, mode)?;
    mapping.remap(bootstrap.committed_bytes)?;
    Ok((mapping, bootstrap))
}

pub(crate) fn map_writer(file: File) -> Result<(Mapping, Bootstrap)> {
    require_regular_file(&file)?;
    let physical_bytes = file.metadata()?.len();
    bootstrap::require_geometry(physical_bytes)?;
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
    bootstrap::require_geometry(physical_bytes)?;
    let mapping = Mapping::read_only_view(file, (2 * PAGE_SIZE) as u64)?;
    bootstrap_mapping(&mapping, physical_bytes, mode)
}

pub(crate) fn bootstrap_file_faultable(file: &File, mode: OpenMode) -> Result<Bootstrap> {
    require_regular_file(file)?;
    let physical_bytes = file.metadata()?.len();
    bootstrap::require_geometry(physical_bytes)?;
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

impl ByteSource for FaultableMetaPage<'_> {
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
