use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::MetadataExt;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::crc32c;

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-recovery-scratch-{label}-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}

#[test]
fn exact_names_headers_io_and_cleanup_round_trip() {
    let directory = TempDirectory::new("roundtrip");
    let mut scratch = Scratch::start(&directory.path, meta(), 4096, 2, 4).unwrap();
    let attempt = scratch.attempt_id;
    let first = scratch.create().unwrap();
    let second = scratch.create().unwrap();
    let first_name = scratch_name(attempt, 0).unwrap();
    let second_name = scratch_name(attempt, 1).unwrap();

    assert_eq!(
        first_name.bytes(),
        expected_name(attempt, b"00000000").as_slice()
    );
    assert_eq!(
        second_name.bytes(),
        expected_name(attempt, b"00000001").as_slice()
    );

    let bytes = fs::read(
        directory
            .path
            .join(std::ffi::OsStr::from_bytes(first_name.bytes())),
    )
    .unwrap();
    assert_eq!(bytes.len(), HEADER_SIZE as usize);
    assert_eq!(&bytes[..8], b"IPR4SCR1");
    assert_eq!(u16::from_le_bytes(bytes[8..10].try_into().unwrap()), 1);
    assert_eq!(u16::from_le_bytes(bytes[10..12].try_into().unwrap()), 128);
    assert_eq!(u16::from_le_bytes(bytes[12..14].try_into().unwrap()), 2);
    assert_eq!(&bytes[16..32], &[0x11; 16]);
    assert_eq!(u64::from_le_bytes(bytes[32..40].try_into().unwrap()), 9);
    assert_eq!(&bytes[40..56], &[0x22; 16]);
    assert_eq!(&bytes[56..72], &attempt);
    assert_eq!(u32::from_le_bytes(bytes[72..76].try_into().unwrap()), 0);
    assert_eq!(u16::from_le_bytes(bytes[76..78].try_into().unwrap()), 1);
    assert_eq!(
        u32::from_le_bytes(bytes[124..128].try_into().unwrap()),
        crc32c::crc32c_with_zeroed(&bytes, 124, 4).unwrap()
    );

    scratch.write(first, HEADER_SIZE, b"abcdef").unwrap();
    let mut read = [0; 6];
    scratch.read(first, HEADER_SIZE, &mut read).unwrap();
    assert_eq!(&read, b"abcdef");
    assert_eq!(scratch.length(first), HEADER_SIZE + 6);
    scratch.resize(first, HEADER_SIZE + 64).unwrap();
    let detached = scratch.detach(first).unwrap();
    detached.write(HEADER_SIZE, b"detached").unwrap();
    let mut detached_read = [0; 8];
    detached.read(HEADER_SIZE, &mut detached_read).unwrap();
    assert_eq!(&detached_read, b"detached");
    assert_eq!(scratch.attach(detached), first);
    scratch.reset(first).unwrap();
    assert_eq!(scratch.length(first), HEADER_SIZE);
    assert_eq!(scratch.length(second), HEADER_SIZE);

    let cleanup = scratch.cleanup();
    assert!(cleanup.clean());
    assert_eq!(cleanup.attempt_id, attempt);
    assert_eq!(fs::read_dir(&directory.path).unwrap().count(), 0);
}

#[test]
fn byte_file_and_descriptor_budgets_fail_before_growth() {
    let directory = TempDirectory::new("budgets");
    assert!(matches!(
        Scratch::start(&directory.path, meta(), 127, 2, 4),
        Err(Error::BudgetExceeded("recovery scratch bytes"))
    ));
    assert!(matches!(
        Scratch::start(&directory.path, meta(), 4096, 0, 4),
        Err(Error::BudgetExceeded(
            "recovery scratch requires one file descriptor"
        ))
    ));
    assert!(matches!(
        Scratch::start(&directory.path, meta(), 4096, 2, 2),
        Err(Error::BudgetExceeded(
            "recovery scratch requires one file descriptor"
        ))
    ));
    let scratch = Scratch::start(&directory.path, meta(), 4096, 1, 3).unwrap();
    assert!(matches!(
        scratch.require_external_sort(),
        Err(Error::BudgetExceeded(
            "external recovery sort requires two scratch files"
        ))
    ));
    assert!(scratch.cleanup().clean());
    let scratch = Scratch::start(&directory.path, meta(), 4096, 2, 3).unwrap();
    assert!(matches!(
        scratch.require_external_sort(),
        Err(Error::BudgetExceeded(
            "external recovery sort requires two scratch files"
        ))
    ));
    assert!(scratch.cleanup().clean());

    let mut scratch = Scratch::start(&directory.path, meta(), 256, 2, 4).unwrap();
    let first = scratch.create().unwrap();
    let _second = scratch.create().unwrap();
    assert!(matches!(
        scratch.write(first, HEADER_SIZE, &[1]),
        Err(Error::BudgetExceeded("recovery scratch bytes"))
    ));
    assert!(scratch.cleanup().clean());
}

#[test]
fn exclusive_creation_never_replaces_a_matching_lookalike() {
    let directory = TempDirectory::new("exclusive");
    let mut scratch = Scratch::start(&directory.path, meta(), 4096, 2, 4).unwrap();
    let name = scratch_name(scratch.attempt_id, 0).unwrap();
    let path = directory
        .path
        .join(std::ffi::OsStr::from_bytes(name.bytes()));
    fs::write(&path, b"foreign").unwrap();

    assert!(matches!(scratch.create(), Err(Error::NameExists)));
    assert_eq!(fs::read(&path).unwrap(), b"foreign");
    assert!(scratch.cleanup().clean());
    assert_eq!(fs::read(&path).unwrap(), b"foreign");
}

#[test]
fn changed_link_count_is_returned_as_exact_residue() {
    let directory = TempDirectory::new("links");
    let mut scratch = Scratch::start(&directory.path, meta(), 4096, 2, 4).unwrap();
    let attempt = scratch.attempt_id;
    scratch.create().unwrap();
    let name = scratch_name(attempt, 0).unwrap();
    let path = directory
        .path
        .join(std::ffi::OsStr::from_bytes(name.bytes()));
    let alias = directory.path.join("alias");
    fs::hard_link(&path, &alias).unwrap();

    let cleanup = scratch.cleanup();
    assert!(!cleanup.clean());
    assert_eq!(cleanup.residues.len(), 1);
    assert_eq!(cleanup.residues[0].ordinal, 0);
    assert_eq!(cleanup.residues[0].basename.as_ref(), name.bytes());
    assert_eq!(cleanup.residues[0].problem.code, ErrorCode::CleanupConflict);
    assert_eq!(fs::metadata(&path).unwrap().nlink(), 2);
}

fn meta() -> MetaV4 {
    MetaV4 {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::RETENTION,
        database_id: [0x11; 16],
        txn_id: 9,
        commit_nonce: [0x22; 16],
        page_count: 2,
        range_record_count: 0,
        active_feed_count: 0,
        feed_index_limit: 0,
        membership_entry_count: 0,
        membership_id_limit: 0,
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

fn expected_name(attempt: [u8; 16], ordinal: &[u8; 8]) -> Vec<u8> {
    let mut name = b".iprange-scratch-".to_vec();
    for byte in attempt {
        name.push(hex(byte >> 4));
        name.push(hex(byte & 0x0f));
    }
    name.push(b'-');
    name.extend_from_slice(ordinal);
    name.extend_from_slice(b".tmp");
    name
}
