use std::ffi::OsStr;
use std::fs::{self, OpenOptions};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use sha2::{Digest, Sha512};

use super::*;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::test_alloc::count_thread_allocations;

#[test]
fn preparation_hashes_exact_bytes_and_retains_the_lifetime_lock() {
    let directory = TempDirectory::new();
    let (attempt, finished, private) = built_output(&directory.path);
    let expected_bytes = fs::read(&private).unwrap();
    let expected_digest: [u8; 64] = Sha512::digest(&expected_bytes).into();
    let expected_meta = finished.meta;

    let prepared = attempt
        .prepare_cancellable(finished, &crate::CancellationToken::new())
        .unwrap();
    assert_eq!(prepared.sha512, expected_digest);
    assert_eq!(prepared.byte_length, expected_bytes.len() as u64);
    assert_eq!(prepared.meta, expected_meta);

    let contender = OpenOptions::new()
        .read(true)
        .write(true)
        .open(&private)
        .unwrap();
    assert!(!live_lock::try_lock(&contender, MAIN_LIFETIME_LOCK, Mode::Exclusive).unwrap());
    drop(prepared);
    wait_for_exclusive_lock(&contender);
    live_lock::unlock(&contender, MAIN_LIFETIME_LOCK).unwrap();
}

#[test]
fn digest_reader_visits_every_byte_once_in_order() {
    let bytes = vec![0x5a; 2 * DIGEST_BUFFER_SIZE + 17];
    let mut expected_offset = 0usize;
    let mut calls = 0usize;
    let digest = digest_with(bytes.len() as u64, |offset, output| {
        assert_eq!(offset, expected_offset as u64);
        let end = expected_offset + output.len();
        output.copy_from_slice(&bytes[expected_offset..end]);
        expected_offset = end;
        calls += 1;
        Ok(())
    })
    .unwrap();

    assert_eq!(expected_offset, bytes.len());
    assert_eq!(calls, 3);
    assert_eq!(digest, Sha512::digest(&bytes).as_slice());
}

#[test]
fn warmed_preparation_allocates_no_heap() {
    let directory = TempDirectory::new();
    let (attempt, finished, _) = built_output(&directory.path);
    let _ = count_thread_allocations(|| ());
    let cancellation = crate::CancellationToken::new();
    let (result, allocations) =
        count_thread_allocations(|| attempt.prepare_cancellable(finished, &cancellation));
    let prepared = result.unwrap();
    assert_eq!(allocations, 0);
    drop(prepared);
}

#[test]
fn non_meta_corruption_does_not_trigger_implicit_validation() {
    let directory = TempDirectory::new();
    let (attempt, finished, _) = built_output(&directory.path);
    assert!(finished.meta.page_count > 2);
    let mut byte = [0; 1];
    file_io::read_exact_at(&finished.file, &mut byte, 2 * PAGE_SIZE as u64).unwrap();
    byte[0] ^= 1;
    file_io::write_exact_at(&finished.file, &byte, 2 * PAGE_SIZE as u64).unwrap();

    let prepared = attempt
        .prepare_cancellable(finished, &crate::CancellationToken::new())
        .unwrap();
    drop(prepared);
}

#[test]
fn hard_link_failure_returns_the_exact_owned_output() {
    let directory = TempDirectory::new();
    let (attempt, finished, private) = built_output(&directory.path);
    fs::hard_link(&private, directory.path.join("extra-link")).unwrap();
    let expected = attempt.identity();

    let failure = attempt
        .prepare_cancellable(finished, &crate::CancellationToken::new())
        .unwrap_err();
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::LinkCount(2))
    ));
    assert_eq!(failure.owner.attempt.identity(), expected);
    assert_eq!(failure.owner.finished.file.metadata().unwrap().nlink(), 2);
}

#[test]
fn private_name_replacement_returns_the_original_owned_inode() {
    let directory = TempDirectory::new();
    let (attempt, finished, private) = built_output(&directory.path);
    let displaced = directory.path.join("displaced");
    fs::rename(&private, &displaced).unwrap();
    fs::write(&private, b"foreign").unwrap();
    let expected = attempt.identity();

    let failure = attempt
        .prepare_cancellable(finished, &crate::CancellationToken::new())
        .unwrap_err();
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::IdentityChanged)
    ));
    assert_eq!(failure.owner.attempt.identity(), expected);
    assert_eq!(
        failure.owner.finished.file.metadata().unwrap().ino(),
        expected.inode
    );
}

#[test]
fn changed_access_policy_fails_before_digest() {
    let directory = TempDirectory::new();
    let (attempt, finished, _) = built_output(&directory.path);
    finished
        .file
        .set_permissions(fs::Permissions::from_mode(0o640))
        .unwrap();

    let failure = attempt
        .prepare_cancellable(finished, &crate::CancellationToken::new())
        .unwrap_err();
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::AccessPolicy)
    ));
}

#[test]
fn builder_meta_must_match_the_selected_file_meta() {
    let directory = TempDirectory::new();
    let (attempt, mut finished, _) = built_output(&directory.path);
    finished.meta.txn_id += 1;

    let failure = attempt
        .prepare_cancellable(finished, &crate::CancellationToken::new())
        .unwrap_err();
    assert!(matches!(failure.cause, Error::FinishedMetaChanged));
}

fn built_output(directory: &Path) -> (OutputAttempt, Finished, PathBuf) {
    let destination = directory.join("result.v4");
    let secured = CreatedOutput::create(&destination)
        .unwrap()
        .secure()
        .unwrap();
    let private = private_path(directory, secured.attempt.name());
    let (attempt, file) = secured.into_parts();
    let mut builder = Builder::new_owned(file, direct_spec(), output_budget()).unwrap();
    builder.push_direct_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    (attempt, builder.finish_owned().unwrap(), private)
}

fn private_path(directory: &Path, name: &Name) -> PathBuf {
    directory.join(OsStr::from_bytes(name.bytes()))
}

fn direct_spec() -> OutputSpec {
    OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::RETENTION,
        database_id: [3; 16],
        transaction_id: 7,
        commit_nonce: [4; 16],
        feed_index_limit: 0,
    }
}

fn output_budget() -> OutputBudget {
    OutputBudget {
        max_output_pages: 100_000,
    }
}

fn wait_for_exclusive_lock(file: &File) {
    for _ in 0..100 {
        if live_lock::try_lock(file, MAIN_LIFETIME_LOCK, Mode::Exclusive).unwrap() {
            return;
        }
        // A parallel crash test can briefly inherit a close-on-exec descriptor at fork.
        std::thread::sleep(Duration::from_millis(1));
    }
    panic!("exclusive lifetime lock remained held");
}

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-prepared-output-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).unwrap();
    }
}
