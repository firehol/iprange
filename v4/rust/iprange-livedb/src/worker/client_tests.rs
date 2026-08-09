use std::fs::{self, OpenOptions};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};

use crate::publication::{CreationSecurity, Housekeeping};
use crate::validation::LocalFileIdentity;

use super::super::control::{
    CallbackCheckpoint, Control, ScratchCheckpoint, ScratchCheckpointEntry,
};
use super::{
    cleanup_checkpoint, read_recovery_callback_report, read_validation_progress, scratch_clean,
};

#[test]
fn callback_checkpoints_round_trip_complete_progress() {
    use crate::recovery::RecoveryReport;
    use crate::validation::{ValidationObject, ValidationProgress, ValidationReason};

    let control = Control::create_parent().unwrap();
    let mut report = RecoveryReport::default();
    report.pages.examined = 19;
    report.ranges.accepted = 7;
    report.unknown_envelopes = 3;
    report.has_unbounded_unknown = true;
    control.begin_callback_checkpoint();
    let mut output = super::super::wire::Writer::new_callback_checkpoint(&control);
    super::super::wire_recovery::report(&mut output, &report).unwrap();
    output.finish().unwrap();
    control.seal_callback_checkpoint(CallbackCheckpoint::RecoveryReport);
    assert_eq!(read_recovery_callback_report(&control).unwrap(), report);

    let mut reasons = [0; ValidationReason::COUNT];
    reasons[ValidationReason::PageCrcMismatch as usize] = 2;
    let mut objects = [0; ValidationObject::COUNT];
    objects[ValidationObject::RangeTree as usize] = 11;
    let progress = ValidationProgress::from_wire(
        11,
        2,
        1,
        crate::Cardinality129::from_u128(29),
        false,
        reasons,
        objects,
    );
    control.begin_callback_checkpoint();
    let mut output = super::super::wire::Writer::new_callback_checkpoint(&control);
    super::super::wire::progress(&mut output, &progress).unwrap();
    output.finish().unwrap();
    control.seal_callback_checkpoint(CallbackCheckpoint::ValidationProgress);
    assert_eq!(read_validation_progress(&control).unwrap(), Some(progress));
}

#[test]
fn worker_problem_preserves_unregistered_utf8_detail() {
    let control = Control::create_parent().unwrap();
    let expected = crate::publication::PublicationProblem::with_owned_detail(
        crate::ErrorCode::CleanupConflict,
        Some(17),
        "exact worker detail outside any static registry".to_owned(),
    );
    let mut output = super::super::wire::Writer::new(&control);
    super::super::wire_publication::problem(&mut output, &expected).unwrap();
    output.finish().unwrap();

    let mut input = super::super::wire::Reader::new(&control).unwrap();
    let actual = super::super::wire_publication::read_problem(&mut input).unwrap();
    input.finish().unwrap();
    assert_eq!(actual, expected);
}

#[test]
fn worker_problem_rejects_non_utf8_detail() {
    let control = Control::create_parent().unwrap();
    let mut output = super::super::wire::Writer::new(&control);
    output
        .u32(crate::ErrorCode::CleanupConflict as u32)
        .unwrap();
    output.bool(false).unwrap();
    output.sized_bytes(&[0xff]).unwrap();
    output.finish().unwrap();

    let mut input = super::super::wire::Reader::new(&control).unwrap();
    assert!(matches!(
        super::super::wire_publication::read_problem(&mut input),
        Err(crate::Error::Corrupt(
            "worker publication error detail is not UTF-8"
        ))
    ));
}

#[cfg(target_os = "linux")]
#[test]
fn source_sigbus_is_classified_cleaned_and_restartable() {
    use crate::bootstrap;
    use crate::contract::{AddressFamily, ValueTag, PAGE_SIZE};
    use crate::mapping::test_support as file_io;
    use crate::publication::PublicationStatus;
    use crate::recovery::{
        RecoveryBudget, RecoveryCandidate, RecoveryCandidateLabel, RecoverySinkControl, WorkerMode,
    };
    use crate::validation::ValidationReason;
    use crate::{page_checksum, range_tree, slotted_page, CancellationToken};

    let directory = TempDirectory::new("source-fault");
    let source = directory.0.join("source.v4");
    let output = directory.0.join("output.v4");
    let (meta, last_page) = fault_fixture(&source);
    let source_identity = metadata_identity(&fs::metadata(&source).unwrap());
    let candidate = RecoveryCandidate {
        label: RecoveryCandidateLabel::Newest,
        meta_page: 1,
        source_identity,
        database_id: meta.database_id,
        transaction_id: meta.txn_id,
        commit_nonce: meta.commit_nonce,
    };
    let budget = RecoveryBudget::heap_only(2 * 1024 * 1024, 100, 2);
    let cancellation = CancellationToken::new();
    let mut delivered = 0;
    let mut truncated = false;
    let first = super::recover_once(
        &source,
        candidate,
        &output,
        WorkerMode::Immutable,
        &budget,
        &mut |unknown: &crate::recovery::RecoveryUnknownEnvelope| {
            assert_eq!(unknown.reason, ValidationReason::PageCrcMismatch);
            assert_eq!(unknown.page_number, Some(3));
            if !truncated {
                OpenOptions::new()
                    .write(true)
                    .open(&source)
                    .unwrap()
                    .set_len(4 * PAGE_SIZE as u64)
                    .unwrap();
                truncated = true;
            }
            Ok(RecoverySinkControl::Continue)
        },
        &cancellation,
        &[],
        &mut delivered,
    );
    let (fault, output_attempt, scratch) = match first {
        super::RecoveryAttempt::Interrupted {
            fault,
            output,
            scratch,
            ..
        } => (fault, output, scratch),
        _ => panic!("source fault was not returned as an owned interruption"),
    };
    assert!(truncated);
    assert_eq!(delivered, 1);
    assert_eq!(fault.role, super::MappingRole::Source);
    assert_eq!(fault.relative, 4 * PAGE_SIZE as u64);
    assert_eq!(fault.mapping_len, 5 * PAGE_SIZE as u64);
    let (discarded, scratch) = crate::worker::cleanup::discard(
        &output,
        output_attempt,
        budget.scratch_directory.as_deref(),
        scratch,
    );
    assert!(super::discard_clean(&discarded));
    assert!(scratch_clean(&scratch));

    let source_file = OpenOptions::new()
        .read(true)
        .write(true)
        .open(&source)
        .unwrap();
    source_file.set_len(5 * PAGE_SIZE as u64).unwrap();
    file_io::write_exact_at(&source_file, &last_page, 4 * PAGE_SIZE as u64).unwrap();
    source_file.sync_all().unwrap();

    let mut observed = Vec::new();
    let second = super::recover_once(
        &source,
        candidate,
        &output,
        WorkerMode::Immutable,
        &budget,
        &mut |unknown: &crate::recovery::RecoveryUnknownEnvelope| {
            observed.push(*unknown);
            Ok(RecoverySinkControl::Continue)
        },
        &cancellation,
        &[4],
        &mut delivered,
    );
    let result = match second {
        super::RecoveryAttempt::Complete(result) => result.unwrap(),
        _ => panic!("recovery did not complete after marking the exact page unreadable"),
    };
    assert_eq!(delivered, 2);
    assert_eq!(result.report.pages.io_unreadable, 1);
    assert_eq!(observed.len(), 1);
    assert_eq!(observed[0].reason, ValidationReason::IoError);
    assert_eq!(observed[0].page_number, Some(4));
    assert_eq!(result.publication.publication, PublicationStatus::Published);
    assert_eq!(
        crate::database::ImmutableReader::open(&output)
            .unwrap()
            .info()
            .range_record_count,
        0
    );

    fn fault_fixture(path: &Path) -> (crate::contract::MetaV4, [u8; PAGE_SIZE]) {
        let mut meta = bootstrap::tests::empty_direct_meta(1);
        meta.value_tag = ValueTag::RETENTION;
        meta.page_count = 5;
        meta.range_root = 2;
        meta.range_record_count = 2;

        let mut root = [0; PAGE_SIZE];
        let mut builder = slotted_page::Builder::new(
            &mut root,
            range_tree::RANGE_BRANCH,
            meta.txn_id,
            1,
            AddressFamily::Ipv4 as u32,
        );
        for (first, child) in [(10u32, 3u32), (100, 4)] {
            let mut cell = [0; 8];
            cell[..4].copy_from_slice(&first.to_le_bytes());
            cell[4..].copy_from_slice(&child.to_le_bytes());
            builder.push(&cell).unwrap();
        }
        builder.finish().unwrap();
        page_checksum::seal(&mut root).unwrap();

        let mut damaged = leaf(meta.txn_id, 10, 20, 1);
        damaged[100] ^= 0x5a;
        let last = leaf(meta.txn_id, 100, 110, 2);
        let mut image = vec![0; 5 * PAGE_SIZE];
        meta.encode_into((&mut image[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut image[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        image[2 * PAGE_SIZE..3 * PAGE_SIZE].copy_from_slice(&root);
        image[3 * PAGE_SIZE..4 * PAGE_SIZE].copy_from_slice(&damaged);
        image[4 * PAGE_SIZE..].copy_from_slice(&last);
        fs::write(path, image).unwrap();
        (meta, last)
    }

    fn leaf(txn: u64, from: u32, to: u32, value: u32) -> [u8; PAGE_SIZE] {
        let mut page = [0; PAGE_SIZE];
        let mut builder = slotted_page::Builder::new(
            &mut page,
            range_tree::RANGE_LEAF,
            txn,
            0,
            AddressFamily::Ipv4 as u32,
        );
        let mut cell = [0; 12];
        cell[..4].copy_from_slice(&from.to_le_bytes());
        cell[4..8].copy_from_slice(&to.to_le_bytes());
        cell[8..].copy_from_slice(&value.to_le_bytes());
        builder.push(&cell).unwrap();
        builder.finish().unwrap();
        page_checksum::seal(&mut page).unwrap();
        page
    }
}

#[test]
fn scratch_checkpoint_keeps_two_exact_nonoverlapping_entries() {
    let control = Control::create_parent().unwrap();
    let directory = identity(11, 12);
    let first = identity(21, 22);
    let second = identity(31, 32);
    let security = CreationSecurity {
        kind: crate::publication::namespace::CREATION_SECURITY_KIND,
        commitment: [0x5a; 32],
    };
    control
        .start_scratch_checkpoint([0x41; 16], directory, &security)
        .unwrap();
    control.add_scratch_checkpoint(7, first).unwrap();
    control.add_scratch_checkpoint(8, second).unwrap();

    let checkpoint = control.scratch_checkpoint().unwrap().unwrap();
    assert_eq!(checkpoint.attempt_id, [0x41; 16]);
    assert_eq!(checkpoint.directory_identity, directory);
    assert_eq!(checkpoint.creation_security, security);
    assert_eq!(checkpoint.entries.len(), 2);
    assert_eq!(checkpoint.entries[0].ordinal, 7);
    assert_eq!(checkpoint.entries[0].identity, first);
    assert_eq!(checkpoint.entries[1].ordinal, 8);
    assert_eq!(checkpoint.entries[1].identity, second);
}

#[test]
fn checkpoint_cleanup_removes_headerless_exact_scratch() {
    let directory = TempDirectory::new("headerless");
    let checkpoint = create_checkpoint(&directory.0, false);
    let cleanup = cleanup_checkpoint(Some(&directory.0), Some(checkpoint)).unwrap();

    assert!(scratch_clean(&Some(cleanup)));
    assert_eq!(fs::read_dir(&directory.0).unwrap().count(), 0);
}

#[test]
fn checkpoint_cleanup_reports_changed_link_count_without_unlinking() {
    let directory = TempDirectory::new("links");
    let checkpoint = create_checkpoint(&directory.0, true);
    let cleanup = cleanup_checkpoint(Some(&directory.0), Some(checkpoint)).unwrap();

    assert!(!scratch_clean(&Some(cleanup.clone())));
    assert_eq!(cleanup.residues.len(), 1);
    assert!(matches!(cleanup.housekeeping, Housekeeping::None));
    assert_eq!(fs::read_dir(&directory.0).unwrap().count(), 2);
}

fn create_checkpoint(directory: &Path, alias: bool) -> ScratchCheckpoint {
    let attempt_id = [0x32; 16];
    let ordinal = 9;
    let basename = crate::recovery::checkpoint_basename(attempt_id, ordinal).unwrap();
    let path = directory.join(std::ffi::OsStr::from_bytes(&basename));
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&path)
        .unwrap();
    if alias {
        fs::hard_link(&path, directory.join("alias")).unwrap();
    }
    ScratchCheckpoint {
        attempt_id,
        directory_identity: metadata_identity(&fs::metadata(directory).unwrap()),
        creation_security: CreationSecurity {
            kind: crate::publication::namespace::CREATION_SECURITY_KIND,
            commitment: [0x6b; 32],
        },
        entries: vec![ScratchCheckpointEntry {
            ordinal,
            identity: metadata_identity(&file.metadata().unwrap()),
        }],
    }
}

fn identity(device: u64, inode: u64) -> LocalFileIdentity {
    let mut bytes = [0; 32];
    bytes[..8].copy_from_slice(&device.to_le_bytes());
    bytes[8..16].copy_from_slice(&inode.to_le_bytes());
    LocalFileIdentity {
        kind: crate::publication::namespace::IDENTITY_KIND,
        bytes,
    }
}

fn metadata_identity(metadata: &fs::Metadata) -> LocalFileIdentity {
    identity(metadata.dev(), metadata.ino())
}

struct TempDirectory(PathBuf);

impl TempDirectory {
    fn new(label: &str) -> Self {
        let path =
            crate::test_support_tests::unique_path(&format!("iprange-v4-worker-scratch-{label}"));
        fs::create_dir(&path).unwrap();
        Self(path)
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
