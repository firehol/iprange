//! Immutable database open tests.

use super::*;
use crate::bootstrap::{self, MetaSelection};
use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::Error;
use crate::path;
use std::fs;

struct TestPath(std::path::PathBuf);

impl TestPath {
    fn new(label: &str) -> Self {
        Self(crate::test_support_tests::unique_path(&format!(
            "iprange-v4-{label}"
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

fn empty_meta(address_family: AddressFamily, value_kind: ValueKind, value_tag: ValueTag) -> MetaV4 {
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
        Err(Error::WrongAddressFamily(_))
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
        Err(Error::WrongValueKind(_))
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
fn open_does_not_validate_the_metadata_chain() {
    let path = TestPath::new("no-implicit-metadata-validation");
    let mut meta = empty_meta(AddressFamily::Ipv4, ValueKind::Direct, ValueTag::RETENTION);
    meta.page_count = 3;
    meta.metadata_root = 2;
    meta.metadata_uncompressed_len = 0;
    meta.metadata_compressed_len = 8;
    write_image(&path.0, meta, 3);

    let reader = ImmutableReader::open(&path.0).unwrap();
    assert_eq!(reader.metadata_json_len(), Some(0));
    assert!(matches!(reader.metadata_json(), Err(Error::Corrupt(_))));
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

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
#[test]
fn immutable_reader_holds_the_shared_lifetime_lock() {
    let path = TestPath::new("immutable-lifetime");
    write_image(
        &path.0,
        empty_meta(AddressFamily::Ipv4, ValueKind::Direct, ValueTag::RETENTION),
        2,
    );
    let reader = ImmutableReader::open(&path.0).unwrap();
    let competing = crate::live_namespace::open_rw(&path.0).unwrap();

    assert!(!crate::live_lock::try_lock_file(
        &competing,
        crate::live_sidecar::MAIN_LIFETIME_LOCK,
        crate::live_lock::Mode::Exclusive,
    )
    .unwrap());
    drop(reader);
    assert!(crate::live_lock::try_lock_file(
        &competing,
        crate::live_sidecar::MAIN_LIFETIME_LOCK,
        crate::live_lock::Mode::Exclusive,
    )
    .unwrap());
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
