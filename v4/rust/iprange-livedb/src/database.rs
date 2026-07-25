//! Immutable database bootstrap.

use std::fs::{self, File, OpenOptions};
use std::io::Read;
use std::path::Path;

#[cfg(unix)]
use std::os::unix::fs::OpenOptionsExt;

use crate::bootstrap::{self, Bootstrap, MetaSelection, OpenMode};
use crate::contract::{AddressFamily, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::path;
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, RangeDirection};
use crate::range_tree;

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
    file: File,
    bootstrap: Bootstrap,
}

impl ImmutableReader {
    /// Open a sidecar-free immutable v4 file without validating its page graph.
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref();
        let sidecar = path::canonical_sidecar(path)?;
        require_sidecar_absent(&sidecar)?;

        let mut file = open_read_only(path)?;
        require_regular_file(&file)?;
        let physical_bytes = file.metadata()?.len();
        if physical_bytes < (2 * PAGE_SIZE) as u64 {
            return Err(bootstrap::BootstrapError::FileTooShort.into());
        }
        if physical_bytes % PAGE_SIZE as u64 != 0 {
            return Err(bootstrap::BootstrapError::FileUnaligned.into());
        }

        let mut metas = [0u8; 2 * PAGE_SIZE];
        file.read_exact(&mut metas)?;
        let meta0 = (&metas[..PAGE_SIZE]).try_into().unwrap();
        let meta1 = (&metas[PAGE_SIZE..]).try_into().unwrap();
        let bootstrap =
            bootstrap::open_meta_pages(meta0, meta1, physical_bytes, OpenMode::ImmutableReader)?;
        require_sidecar_absent(&sidecar)?;
        Ok(Self { file, bootstrap })
    }

    /// Identity and counters from the selected metadata page.
    pub fn info(&self) -> DatabaseInfo {
        DatabaseInfo::from_bootstrap(self.bootstrap)
    }

    /// Look up one address in an IPv4 direct-value database.
    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.require_direct(AddressFamily::Ipv4)?;
        range_tree::lookup(&self.file, &self.bootstrap.meta, address)
    }

    /// Look up one address in an IPv6 direct-value database.
    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.require_direct(AddressFamily::Ipv6)?;
        range_tree::lookup(&self.file, &self.bootstrap.meta, address)
    }

    /// Open an ordered cursor over an IPv4 direct-value database.
    pub fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.require_direct(AddressFamily::Ipv4)?;
        DirectCursorV4::new(&self.file, &self.bootstrap.meta, direction)
    }

    /// Open an ordered cursor over an IPv6 direct-value database.
    pub fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.require_direct(AddressFamily::Ipv6)?;
        DirectCursorV6::new(&self.file, &self.bootstrap.meta, direction)
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
}

fn require_regular_file(file: &File) -> Result<()> {
    if !file.metadata()?.file_type().is_file() {
        return Err(Error::InvalidArgument(
            "database path is not a regular file",
        ));
    }
    Ok(())
}

fn require_sidecar_absent(sidecar: &Path) -> Result<()> {
    match fs::symlink_metadata(sidecar) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error.into()),
        Ok(_) => Err(Error::WrongMode(
            "immutable open requires the canonical .readers sidecar to be absent",
        )),
    }
}

#[cfg(unix)]
fn open_read_only(path: &Path) -> Result<File> {
    let mut options = OpenOptions::new();
    options.read(true);
    options.custom_flags(libc::O_NOFOLLOW);
    Ok(options.open(path)?)
}

#[cfg(not(unix))]
fn open_read_only(_path: &Path) -> Result<File> {
    Err(Error::Unsupported(
        "safe no-follow file open is not implemented on this platform",
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::MetaV4;
    use std::time::{SystemTime, UNIX_EPOCH};

    struct TestPath(std::path::PathBuf);

    impl TestPath {
        fn new(label: &str) -> Self {
            let unique = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos();
            Self(std::env::temp_dir().join(format!(
                "iprange-v4-{label}-{}-{unique}",
                std::process::id()
            )))
        }
    }

    impl Drop for TestPath {
        fn drop(&mut self) {
            let _ = fs::remove_file(&self.0);
            if let Ok(sidecar) = path::canonical_sidecar(&self.0) {
                let _ = fs::remove_file(sidecar);
            }
        }
    }

    fn empty_meta(
        address_family: AddressFamily,
        value_kind: ValueKind,
        value_tag: ValueTag,
    ) -> MetaV4 {
        MetaV4 {
            address_family,
            value_kind,
            value_tag,
            database_id: [1; 16],
            txn_id: 1,
            commit_nonce: [2; 16],
            page_count: 2,
            range_record_count: 0,
            active_feed_count: 0,
            feed_index_limit: 0,
            membership_entry_count: 0,
            membership_id_limit: u64::from(value_kind == ValueKind::Membership),
            metadata_uncompressed_len: 0,
            metadata_compressed_len: 0,
            retirement_batch_count: 0,
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
        }
    }

    fn write_image(path: &Path, meta: MetaV4, page_count: usize) {
        let mut bytes = vec![0u8; page_count * PAGE_SIZE];
        meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        fs::write(path, bytes).unwrap();
    }

    #[test]
    fn opens_empty_direct_database() {
        let path = TestPath::new("open-direct");
        write_image(
            &path.0,
            empty_meta(AddressFamily::Ipv4, ValueKind::Direct, ValueTag::RETENTION),
            2,
        );
        let reader = ImmutableReader::open(&path.0).unwrap();
        let info = reader.info();
        assert_eq!(info.address_family, AddressFamily::Ipv4);
        assert_eq!(info.value_kind, ValueKind::Direct);
        assert_eq!(info.value_tag, ValueTag::RETENTION);
        assert_eq!(info.transaction_id, 1);
        assert_eq!(info.page_count, 2);
        assert_eq!(info.range_record_count, 0);
        assert_eq!(info.meta_selection, MetaSelection::ProvenCurrent);
        assert_ne!(info.database_id, [0; 16]);
        assert_ne!(info.commit_nonce, [0; 16]);
        assert_eq!(fs::metadata(&path.0).unwrap().len(), 8192);
        assert_eq!(reader.lookup_direct_v4(Ipv4Key(1)).unwrap(), None);
        assert!(matches!(
            reader.lookup_direct_v6(Ipv6Key::MIN),
            Err(Error::InvalidArgument(_))
        ));
    }

    #[test]
    fn opens_empty_membership_database() {
        let path = TestPath::new("open-membership");
        write_image(
            &path.0,
            empty_meta(
                AddressFamily::Ipv6,
                ValueKind::Membership,
                ValueTag::new(b"feeds").unwrap(),
            ),
            2,
        );
        let reader = ImmutableReader::open(&path.0).unwrap();
        let info = reader.info();
        assert_eq!(info.address_family, AddressFamily::Ipv6);
        assert_eq!(info.value_kind, ValueKind::Membership);
        assert_eq!(info.value_tag.bytes(), b"feeds");
        assert!(matches!(
            reader.lookup_direct_v6(Ipv6Key::MIN),
            Err(Error::InvalidArgument(_))
        ));
    }

    #[test]
    fn open_does_not_validate_non_meta_pages() {
        let path = TestPath::new("no-implicit-validation");
        let mut meta = empty_meta(AddressFamily::Ipv4, ValueKind::Direct, ValueTag::RETENTION);
        meta.page_count = 3;
        meta.range_record_count = 1;
        meta.range_root = 2;
        write_image(&path.0, meta, 3);

        let reader = ImmutableReader::open(&path.0).unwrap();
        assert_eq!(reader.info().range_record_count, 1);
    }

    #[test]
    fn open_rejects_short_and_unaligned_files() {
        let short = TestPath::new("short");
        fs::write(&short.0, vec![0; PAGE_SIZE]).unwrap();
        assert!(matches!(
            ImmutableReader::open(&short.0),
            Err(Error::Format(bootstrap::BootstrapError::FileTooShort))
        ));

        let unaligned = TestPath::new("unaligned");
        fs::write(&unaligned.0, vec![0; 2 * PAGE_SIZE + 1]).unwrap();
        assert!(matches!(
            ImmutableReader::open(&unaligned.0),
            Err(Error::Format(bootstrap::BootstrapError::FileUnaligned))
        ));
    }

    #[test]
    fn open_rejects_a_present_sidecar() {
        let path = TestPath::new("live");
        write_image(
            &path.0,
            empty_meta(AddressFamily::Ipv4, ValueKind::Direct, ValueTag::RETENTION),
            2,
        );
        fs::write(path::canonical_sidecar(&path.0).unwrap(), b"present").unwrap();
        assert!(matches!(
            ImmutableReader::open(&path.0),
            Err(Error::WrongMode(_))
        ));
    }

    #[cfg(unix)]
    #[test]
    fn open_rejects_symlinks() {
        use std::os::unix::fs::symlink;

        let path = TestPath::new("target");
        write_image(
            &path.0,
            empty_meta(AddressFamily::Ipv4, ValueKind::Direct, ValueTag::RETENTION),
            2,
        );

        let link = TestPath::new("symlink");
        symlink(&path.0, &link.0).unwrap();
        assert!(ImmutableReader::open(&link.0).is_err());
    }
}
