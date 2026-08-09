use std::fs::{self, OpenOptions};
use std::path::{Path, PathBuf};

use super::*;
use crate::contract::{AddressFamily, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::{Error, ErrorCode};
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::mapping::test_support as file_io;
use crate::publication::PublicationStatus;
use crate::validation::ValidationBudget;
use crate::CancellationToken;

struct TestDirectory {
    path: PathBuf,
}

impl TestDirectory {
    fn new(label: &str) -> Self {
        let path =
            crate::test_support_tests::unique_path(&format!("iprange-v4-public-recovery-{label}"));
        fs::create_dir(&path).unwrap();
        Self { path }
    }

    fn file(&self, name: &str) -> PathBuf {
        self.path.join(name)
    }
}

impl Drop for TestDirectory {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}

#[test]
fn sink_failure_returns_partial_facts_and_removes_the_private_output() {
    let directory = TestDirectory::new("sink");
    let source = directory.file("source.v4");
    let output = directory.file("output.v4");
    incomplete_source(&source);
    let candidate = candidate(&source);

    let failure = recover_immutable(
        &source,
        candidate,
        &output,
        &budget(),
        &mut |_: &RecoveryUnknownEnvelope| {
            Err(Error::InvalidArgument("injected recovery sink failure"))
        },
        &CancellationToken::new(),
    )
    .unwrap_err();

    assert_eq!(failure.cause.code, ErrorCode::SinkFailed);
    assert_eq!(failure.report.unknown_envelopes, 1);
    assert!(failure.cleanup.is_empty());
    assert!(!output.exists());
    assert_no_private_names(&directory.path);
}

#[test]
fn final_source_identity_recheck_blocks_publication_and_cleans_output() {
    let directory = TestDirectory::new("source-replaced");
    let source = directory.file("source.v4");
    let displaced = directory.file("displaced.v4");
    let output = directory.file("output.v4");
    incomplete_source(&source);
    let candidate = candidate(&source);
    let mut replaced = false;

    let failure = recover_immutable(
        &source,
        candidate,
        &output,
        &budget(),
        &mut |_: &RecoveryUnknownEnvelope| {
            if !replaced {
                fs::rename(&source, &displaced).unwrap();
                fs::copy(&displaced, &source).unwrap();
                replaced = true;
            }
            Ok(RecoverySinkControl::Continue)
        },
        &CancellationToken::new(),
    )
    .unwrap_err();

    assert_eq!(failure.cause.code, ErrorCode::RecoveryCandidateChanged);
    assert!(failure.cleanup.is_empty());
    assert!(!output.exists());
    assert_no_private_names(&directory.path);
}

#[test]
fn cancellation_after_damage_delivery_aborts_before_publication() {
    let directory = TestDirectory::new("cancel");
    let source = directory.file("source.v4");
    let output = directory.file("output.v4");
    incomplete_source(&source);
    let candidate = candidate(&source);
    let cancellation = CancellationToken::new();

    let failure = recover_immutable(
        &source,
        candidate,
        &output,
        &budget(),
        &mut |_: &RecoveryUnknownEnvelope| {
            cancellation.cancel();
            Ok(RecoverySinkControl::Continue)
        },
        &cancellation,
    )
    .unwrap_err();

    assert_eq!(failure.cause.code, ErrorCode::Cancelled);
    assert!(!output.exists());
    assert_no_private_names(&directory.path);
}

#[test]
fn destination_race_returns_a_terminal_nonpublication_result() {
    let directory = TestDirectory::new("destination-race");
    let source = directory.file("source.v4");
    let output = directory.file("output.v4");
    incomplete_source(&source);
    let candidate = candidate(&source);
    let mut created = false;

    let result = recover_immutable(
        &source,
        candidate,
        &output,
        &budget(),
        &mut |_: &RecoveryUnknownEnvelope| {
            if !created {
                fs::write(&output, b"foreign").unwrap();
                created = true;
            }
            Ok(RecoverySinkControl::Continue)
        },
        &CancellationToken::new(),
    )
    .unwrap();

    assert_ne!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(fs::read(&output).unwrap(), b"foreign");
    assert!(result.publication.cause.is_some());
}

fn incomplete_source(path: &Path) {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .open(path)
        .unwrap();
    let builder = Builder::new_owned(
        file,
        OutputSpec {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [0x11; 16],
            transaction_id: 1,
            commit_nonce: [0x22; 16],
            feed_index_limit: 0,
        },
        OutputBudget {
            max_output_pages: 100,
        },
    )
    .unwrap();
    let mut finished = builder.finish_owned().unwrap();
    finished.meta.page_count = 3;
    finished.meta.range_root = 2;
    finished.meta.range_record_count = 1;
    let mut page = [0; PAGE_SIZE];
    finished.meta.encode_into(&mut page);
    file_io::write_exact_at(&finished.file, &page, 0).unwrap();
    file_io::write_exact_at(&finished.file, &page, PAGE_SIZE as u64).unwrap();
    finished.file.sync_all().unwrap();
}

fn candidate(path: &Path) -> RecoveryCandidate {
    let inspection = inspect_recovery_candidates(
        path,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(0, 1),
        &CancellationToken::new(),
    )
    .unwrap();
    *inspection.candidate(0).unwrap()
}

fn budget() -> RecoveryBudget {
    RecoveryBudget::heap_only(1024 * 1024, 100, 2)
}

fn assert_no_private_names(directory: &Path) {
    for entry in fs::read_dir(directory).unwrap() {
        let name = entry.unwrap().file_name();
        assert!(
            !name.to_string_lossy().starts_with(".iprange-"),
            "private recovery artifact remained: {name:?}"
        );
    }
}
