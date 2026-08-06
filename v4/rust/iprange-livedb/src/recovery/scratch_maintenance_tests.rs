use std::ffi::OsStr;
use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{symlink, FileExt};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;
use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag};
use crate::error::{Error, ErrorCode};
use crate::recovery::scratch::format::scratch_name;
use crate::recovery::scratch::Scratch;

#[test]
fn listing_reports_only_exact_names_and_authenticates_without_following() {
    let directory = TempDirectory::new("list");
    let (scratch, attempt, valid_path) = active_scratch(&directory.path);
    let valid_bytes = fs::read(&valid_path).unwrap();

    let short_attempt = [0x31; 16];
    fs::write(path_for(&directory.path, short_attempt, 7), b"short").unwrap();

    let mismatch_attempt = [0x32; 16];
    fs::write(path_for(&directory.path, mismatch_attempt, 8), &valid_bytes).unwrap();

    let symlink_attempt = [0x33; 16];
    let symlink_target = directory.path.join("target");
    fs::write(&symlink_target, b"target").unwrap();
    symlink(
        &symlink_target,
        path_for(&directory.path, symlink_attempt, 9),
    )
    .unwrap();

    let hardlink_attempt = [0x34; 16];
    let hardlink_target = directory.path.join("hardlink-target");
    fs::write(&hardlink_target, b"hardlink").unwrap();
    fs::hard_link(
        &hardlink_target,
        path_for(&directory.path, hardlink_attempt, 10),
    )
    .unwrap();

    let uppercase = directory
        .path
        .join(".iprange-scratch-ABABABABABABABABABABABABABABABAB-00000000.tmp");
    fs::write(uppercase, b"ignored").unwrap();
    fs::write(directory.path.join("ordinary-file"), b"ignored").unwrap();

    let (result, mut entries) = listed(&directory.path).unwrap();
    entries.sort_by_key(|entry| entry.ordinal);
    assert_eq!(result.entries, 5);
    assert!(entries
        .iter()
        .all(|entry| entry.directory_identity == result.directory_identity));
    assert_eq!(
        entries
            .iter()
            .find(|entry| entry.attempt_id == attempt)
            .unwrap()
            .authentication,
        AbandonedScratchAuthentication::Authenticated(ScratchOwnerKind::Recovery)
    );
    for candidate in [
        short_attempt,
        mismatch_attempt,
        symlink_attempt,
        hardlink_attempt,
    ] {
        assert_eq!(
            entries
                .iter()
                .find(|entry| entry.attempt_id == candidate)
                .unwrap()
                .authentication,
            AbandonedScratchAuthentication::Unauthenticated
        );
    }
    assert!(scratch.cleanup().clean());
}

#[test]
fn listing_honors_cancellation_stop_and_sink_errors() {
    let directory = TempDirectory::new("sink");
    let (scratch, _, _) = active_scratch(&directory.path);

    let cancelled = CancellationToken::new();
    cancelled.cancel();
    let error = list_abandoned_scratch(
        &directory.path,
        &cancelled,
        &mut |_entry: &AbandonedScratchEntry| Ok(AbandonedScratchSinkControl::Continue),
    )
    .unwrap_err();
    assert!(matches!(error, Error::Cancelled));

    let error = list_abandoned_scratch(
        &directory.path,
        &CancellationToken::new(),
        &mut |_entry: &AbandonedScratchEntry| Ok(AbandonedScratchSinkControl::Stop),
    )
    .unwrap_err();
    assert!(matches!(error, Error::StoppedBySink));

    let error = list_abandoned_scratch(
        &directory.path,
        &CancellationToken::new(),
        &mut |_entry: &AbandonedScratchEntry| {
            Err(Error::InvalidArgument("injected scratch-list sink failure"))
        },
    )
    .unwrap_err();
    assert!(matches!(
        error,
        Error::SinkFailed(cause) if cause.code() == ErrorCode::InvalidArgument
    ));

    let during_callback = CancellationToken::new();
    let callback_cancel = during_callback.clone();
    let error = list_abandoned_scratch(
        &directory.path,
        &during_callback,
        &mut move |_entry: &AbandonedScratchEntry| {
            callback_cancel.cancel();
            Ok(AbandonedScratchSinkControl::Continue)
        },
    )
    .unwrap_err();
    assert!(matches!(error, Error::Cancelled));
    assert!(scratch.cleanup().clean());
}

#[test]
fn exact_removal_is_cancellable_durable_and_idempotent() {
    let directory = TempDirectory::new("remove");
    let (scratch, attempt, path) = active_scratch(&directory.path);
    let (listed, entries) = listed(&directory.path).unwrap();
    let entry = entries
        .into_iter()
        .find(|entry| entry.attempt_id == attempt)
        .unwrap();
    drop(scratch);

    let cancelled = CancellationToken::new();
    cancelled.cancel();
    let error = remove_abandoned_scratch(
        &directory.path,
        listed.directory_identity,
        attempt,
        0,
        entry.artifact_identity,
        &cancelled,
    )
    .unwrap_err();
    assert!(matches!(error, Error::Cancelled));
    assert!(path.exists());

    assert!(
        remove_abandoned_scratch(
            &directory.path,
            listed.directory_identity,
            attempt,
            0,
            entry.artifact_identity,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );
    assert!(!path.exists());
    assert!(
        !remove_abandoned_scratch(
            &directory.path,
            listed.directory_identity,
            attempt,
            0,
            entry.artifact_identity,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );
}

#[test]
fn removal_rejects_directory_inode_header_and_name_replacement_conflicts() {
    let directory = TempDirectory::new("conflict");
    let (scratch, attempt, path) = active_scratch(&directory.path);
    let (listed, entries) = listed(&directory.path).unwrap();
    let entry = entries
        .into_iter()
        .find(|entry| entry.attempt_id == attempt)
        .unwrap();
    drop(scratch);

    let mut wrong_directory = listed.directory_identity;
    wrong_directory.bytes[0] ^= 1;
    let error = remove_abandoned_scratch(
        &directory.path,
        wrong_directory,
        attempt,
        0,
        entry.artifact_identity,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert!(matches!(error, Error::DirectoryIdentityMismatch));

    let mut wrong_artifact = entry.artifact_identity;
    wrong_artifact.bytes[8] ^= 1;
    let error = remove_abandoned_scratch(
        &directory.path,
        listed.directory_identity,
        attempt,
        0,
        wrong_artifact,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert!(matches!(error, Error::CleanupConflict(_)));

    let file = fs::OpenOptions::new().write(true).open(&path).unwrap();
    file.write_at(b"X", 0).unwrap();
    let error = remove_abandoned_scratch(
        &directory.path,
        listed.directory_identity,
        attempt,
        0,
        entry.artifact_identity,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert!(matches!(error, Error::CleanupConflict(_)));
    assert!(path.exists());

    fs::remove_file(&path).unwrap();
    fs::write(&path, b"replacement").unwrap();
    let error = remove_abandoned_scratch(
        &directory.path,
        listed.directory_identity,
        attempt,
        0,
        entry.artifact_identity,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert!(matches!(error, Error::CleanupConflict(_)));
    assert_eq!(fs::read(path).unwrap(), b"replacement");
}

fn listed(path: &Path) -> Result<(AbandonedScratchList, Vec<AbandonedScratchEntry>)> {
    let mut entries = Vec::new();
    let result = list_abandoned_scratch(
        path,
        &CancellationToken::new(),
        &mut |entry: &AbandonedScratchEntry| {
            entries.push(*entry);
            Ok(AbandonedScratchSinkControl::Continue)
        },
    )?;
    Ok((result, entries))
}

fn active_scratch(path: &Path) -> (Scratch, [u8; 16], PathBuf) {
    let mut scratch = Scratch::start(path, meta(), 4096, 2, 4).unwrap();
    let attempt = scratch.attempt_id;
    scratch.create().unwrap();
    let artifact = path_for(path, attempt, 0);
    (scratch, attempt, artifact)
}

fn path_for(directory: &Path, attempt: [u8; 16], ordinal: u32) -> PathBuf {
    let name = scratch_name(attempt, ordinal).unwrap();
    directory.join(OsStr::from_bytes(name.bytes()))
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

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new(label: &str) -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let sequence = NEXT.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-scratch-maintenance-{label}-{}-{unique}-{sequence}",
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
