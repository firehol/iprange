use std::ffi::OsStr;
use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::path::{Path, PathBuf};

use super::*;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::error::ErrorCode;
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::publication::output::CreatedOutput;
use crate::publication::replacement;
use crate::publication::reservation_file::ReservationDraft;
use crate::publication::result::PublicationStatus;
use crate::publication::CleanupState;
use crate::test_alloc::count_thread_allocations;
use crate::validation::LocalFileIdentity;

#[test]
fn success_returns_exact_published_facts_and_no_residue() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let expected_attempt = output.attempt.attempt_id();
    let expected_output = output.attempt.identity().encode();
    let expected_length = output.byte_length;
    let expected_digest = output.sha512;

    let result = fail_if_exists_cancellable(output, &crate::CancellationToken::new()).unwrap();

    assert_eq!(result.publication, PublicationStatus::Published);
    assert_eq!(result.destination_content, DestinationContent::Desired);
    assert!(result.main_namespace_may_have_been_attempted);
    assert_eq!(result.main_access_policy, AccessPolicy::CreatorOnly);
    assert_eq!(result.coordination_access_policy, AccessPolicy::Absent);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert!(result.cause.is_none());
    assert_eq!(result.attempt.publication_attempt_id, expected_attempt);
    assert_eq!(result.attempt.output_identity.bytes, expected_output);
    assert_eq!(result.attempt.output_byte_length, expected_length);
    assert_eq!(result.attempt.output_sha512, expected_digest);
    assert_eq!(&*result.attempt.destination_basename, b"result.v4");
    assert!(paths.main.exists());
    assert!(!paths.private_output.exists());
    assert!(!paths.private_reservation.exists());
    assert!(!paths.coordination.exists());
}

#[test]
fn pre_boundary_failure_returns_preparation_error_after_exact_cleanup() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let failure = fail_if_exists_with(output, |point| {
        if point == Point::ReservationCreated {
            Err(Problem::injected())
        } else {
            Ok(())
        }
    })
    .unwrap_err();

    assert_eq!(failure.cause, Problem::injected());
    assert_eq!(failure.cleanup_state(), CleanupState::Clean);
    assert_eq!(
        &*failure.private_output_basename,
        file_name(&paths.private_output)
    );
    assert!(!paths.main.exists());
    assert!(!paths.private_output.exists());
    assert!(!paths.private_reservation.exists());
    assert!(!paths.coordination.exists());
}

#[test]
fn state1_failure_is_not_published_and_cleans_both_artifacts() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let result = fail_if_exists_with(output, |point| {
        if point == Point::State1Selected {
            Err(Problem::injected())
        } else {
            Ok(())
        }
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.destination_content, DestinationContent::Absent);
    assert!(!result.main_namespace_may_have_been_attempted);
    assert_eq!(result.coordination_access_policy, AccessPolicy::Absent);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(result.cause, Some(Problem::injected()));
    assert_no_attempt_files(&paths);
}

#[test]
fn acquired_state1_failure_retires_the_canonical_reservation() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let result = fail_if_exists_with(output, |point| {
        if point == Point::ReservationAcquired {
            Err(Problem::injected())
        } else {
            Ok(())
        }
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_no_attempt_files(&paths);
}

#[test]
fn state2_failure_retains_resolver_authority_without_cleanup_residue() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let result = fail_if_exists_with(output, |point| {
        if point == Point::State2Selected {
            Err(Problem::injected())
        } else {
            Ok(())
        }
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::OutcomeUnknown);
    assert_eq!(result.destination_content, DestinationContent::Unclassified);
    assert!(result.main_namespace_may_have_been_attempted);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert!(paths.private_output.exists());
    assert!(paths.coordination.exists());
    assert!(!paths.main.exists());
}

#[test]
fn main_race_after_state2_is_outcome_unknown_and_never_overwrites() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let result = fail_if_exists_with(output, |point| {
        if point == Point::State2Selected {
            fs::write(&paths.main, b"racing-main").unwrap();
        }
        Ok(())
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::OutcomeUnknown);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(fs::read(&paths.main).unwrap(), b"racing-main");
    assert!(paths.private_output.exists());
    assert!(paths.coordination.exists());
}

#[test]
fn failed_shared_directory_sync_ledgers_both_unlinked_artifacts() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let result = fail_if_exists_with(output, |point| match point {
        Point::State1Selected | Point::CleanupDirectorySync => Err(Problem::injected()),
        _ => Ok(()),
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.cleanup_state(), CleanupState::ResiduePossible);
    assert_eq!(result.cleanup.len(), 2);
    assert_eq!(
        result
            .cleanup
            .iter()
            .map(|artifact| artifact.kind)
            .collect::<Vec<_>>(),
        [
            ArtifactKind::PrivateOutput,
            ArtifactKind::PrivateReservation
        ]
    );
    assert!(!paths.private_output.exists());
    assert!(!paths.private_reservation.exists());
}

#[test]
fn individual_cleanup_failures_report_only_the_exact_owned_artifact() {
    for (cleanup_point, expected_kind) in [
        (Point::CleanupOutput, ArtifactKind::PrivateOutput),
        (Point::CleanupReservation, ArtifactKind::PrivateReservation),
    ] {
        let directory = TempDirectory::new();
        let (output, paths) = prepared_output(&directory.path);
        let result = fail_if_exists_with(output, |point| {
            if point == Point::State1Selected || point == cleanup_point {
                Err(Problem::injected())
            } else {
                Ok(())
            }
        })
        .unwrap();

        assert_eq!(result.cleanup.len(), 1);
        let artifact = result.cleanup.get(0).unwrap();
        assert_eq!(artifact.kind, expected_kind);
        let expected_name = match expected_kind {
            ArtifactKind::PrivateOutput => file_name(&paths.private_output),
            ArtifactKind::PrivateReservation => file_name(&paths.private_reservation),
            ArtifactKind::OwnedCoordination
            | ArtifactKind::AuthorizedScratch
            | ArtifactKind::OwnedMain
            | ArtifactKind::UnpublishedMainTail => {
                panic!("direct publication returned an unrelated cleanup kind")
            }
        };
        assert_eq!(&*artifact.basename, expected_name);
    }
}

#[test]
fn foreign_coordination_is_preserved_while_owned_artifacts_are_cleaned() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    fs::write(&paths.coordination, b"foreign").unwrap();

    let result = fail_if_exists_cancellable(output, &crate::CancellationToken::new()).unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.destination_content, DestinationContent::Absent);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(result.cause.unwrap().code, ErrorCode::NameExists);
    assert_eq!(fs::read(&paths.coordination).unwrap(), b"foreign");
    assert!(!paths.private_output.exists());
    assert!(!paths.private_reservation.exists());
}

#[test]
fn existing_main_is_never_removed_or_classified_without_reading_it() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    fs::write(&paths.main, b"existing").unwrap();

    let result = fail_if_exists_cancellable(output, &crate::CancellationToken::new()).unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.destination_content, DestinationContent::Unclassified);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(fs::read(&paths.main).unwrap(), b"existing");
    assert!(!paths.private_output.exists());
    assert!(!paths.coordination.exists());
}

#[test]
fn published_reservation_conflict_is_reported_as_cleanup_residue() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let extra = directory.path.join("extra-link");
    let result = fail_if_exists_with(output, |point| {
        if point == Point::DesiredProven {
            fs::hard_link(&paths.coordination, &extra).unwrap();
        }
        Ok(())
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::Published);
    assert_eq!(result.destination_content, DestinationContent::Desired);
    assert_eq!(result.cleanup_state(), CleanupState::ResiduePossible);
    assert_eq!(result.cleanup.len(), 1);
    assert_eq!(
        result.cleanup.get(0).unwrap().kind,
        ArtifactKind::PrivateReservation
    );
    assert!(paths.main.exists());
    assert!(paths.coordination.exists());
    assert!(extra.exists());
}

#[test]
fn post_proof_failure_remains_published_and_cleanup_still_runs() {
    let directory = TempDirectory::new();
    let (output, paths) = prepared_output(&directory.path);
    let result = fail_if_exists_with(output, |point| {
        if point == Point::DesiredProven {
            Err(Problem::injected())
        } else {
            Ok(())
        }
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::Published);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(result.cause, Some(Problem::injected()));
    assert!(paths.main.exists());
    assert!(!paths.coordination.exists());
}

#[test]
fn post_boundary_success_allocates_no_heap() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let seed = Seed::capture(&output);
    let reservation = ReservationDraft::create(&output)
        .unwrap()
        .initialize(&output)
        .unwrap();

    let (result, allocations) = count_thread_allocations(|| {
        from_private(
            seed,
            output,
            reservation,
            None,
            &mut |_| Ok(()),
            false,
            &mut |_| Ok(()),
        )
    });

    assert_eq!(allocations, 0);
    assert_eq!(result.unwrap().publication, PublicationStatus::Published);
}

#[test]
fn mapped_housekeeping_checkpoint_reports_the_new_envelope_and_owned_sources() {
    for (kind, expected_cleanup) in [
        (ArtifactKind::OwnedCoordination, 1),
        (ArtifactKind::PrivateOutput, 2),
    ] {
        let directory = TempDirectory::new();
        let (output, _) = prepared_output(&directory.path);
        let reservation_identity = output.attempt.identity();
        let seed = Seed::capture(&output);
        let artifact = crate::publication::HousekeepingArtifact {
            state: crate::publication::HousekeepingState::MovePending,
            directory_role: crate::publication::DirectoryRole::Destination,
            directory_identity: local_identity(41),
            basename_encoding: 1,
            attempt_id: [42; 16],
            ordinal: u32::from(kind == ArtifactKind::OwnedCoordination),
            envelope_basename: b".iprange-gcauth-test.tmp".as_slice().into(),
            envelope_identity: local_identity(43),
            source_basename: b"source.tmp".as_slice().into(),
            inert_basename: b".iprange-gc-test.tmp".as_slice().into(),
            source_presence: crate::publication::ArtifactPresence::Present,
            source_identity: Some(local_identity(44)),
            inert_presence: crate::publication::ArtifactPresence::Absent,
            inert_identity: None,
            kind,
            creation_security: crate::publication::CreationSecurity {
                kind: 1,
                commitment: [45; 32],
            },
            selected_envelope_sequence: 0,
        };
        let mut observed = None;

        observe_published_housekeeping(&seed, reservation_identity, &artifact, &mut |checkpoint| {
            let PublicationCheckpoint::Result(result) = checkpoint else {
                panic!("housekeeping checkpoint must be a publication result");
            };
            observed = Some(result.clone());
            Ok(())
        })
        .unwrap();

        let result = observed.unwrap();
        assert_eq!(result.publication, PublicationStatus::Published);
        assert_eq!(result.destination_content, DestinationContent::Desired);
        assert_eq!(
            result.housekeeping,
            crate::publication::Housekeeping::Visible
        );
        assert_eq!(&*result.visible_housekeeping, &[artifact]);
        assert_eq!(result.cleanup.len(), expected_cleanup);
        assert_eq!(result.cleanup.get(0).unwrap().kind, kind);
        assert_eq!(
            result.coordination_access_policy,
            AccessPolicy::ChangedOrUnproven
        );
        assert_eq!(result.cause.as_ref().unwrap().code, ErrorCode::Io);
    }
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn replacement_publishes_exact_output_and_retires_the_previous_inode() {
    let directory = TempDirectory::new();
    let (output, paths, previous) = prepared_replacement_output(&directory.path);

    let result = replace_existing_cancellable(output, &CancellationToken::new()).unwrap();

    assert_eq!(result.publication, PublicationStatus::Published);
    assert_eq!(result.destination_content, DestinationContent::Desired);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(
        result.attempt.previous_destination.as_ref().unwrap().sha512,
        previous
    );
    assert!(paths.main.exists());
    assert!(!paths.private_output.exists());
    assert!(!paths.private_reservation.exists());
    assert!(!paths.coordination.exists());
}

#[test]
fn replacement_state1_failure_preserves_and_classifies_previous_bytes() {
    let directory = TempDirectory::new();
    let (output, paths, _) = prepared_replacement_output(&directory.path);
    let result = publish_with(output, None, |point| {
        if point == Point::State1Selected {
            Err(Problem::injected())
        } else {
            Ok(())
        }
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.destination_content, DestinationContent::Previous);
    assert_eq!(fs::read(&paths.main).unwrap(), b"previous bytes");
    assert!(!paths.private_output.exists());
    assert!(!paths.private_reservation.exists());
    assert!(!paths.coordination.exists());
}

#[test]
fn replacement_path_race_is_detected_before_state2() {
    let directory = TempDirectory::new();
    let (output, paths, _) = prepared_replacement_output(&directory.path);
    let displaced = directory.path.join("displaced");
    let result = publish_with(output, None, |point| {
        if point == Point::ReservationAcquired {
            fs::rename(&paths.main, &displaced).unwrap();
            fs::write(&paths.main, b"racing bytes").unwrap();
        }
        Ok(())
    })
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.destination_content, DestinationContent::Unclassified);
    assert_eq!(fs::read(&paths.main).unwrap(), b"racing bytes");
    assert_eq!(fs::read(&displaced).unwrap(), b"previous bytes");
    assert!(!paths.private_output.exists());
    assert!(!paths.coordination.exists());
}

fn prepared_output(directory: &Path) -> (PreparedOutput, Paths) {
    let main = directory.join("result.v4");
    let secured = CreatedOutput::create(&main).unwrap().secure().unwrap();
    let (attempt, file) = secured.into_parts();
    let private_output = named_path(directory, attempt.name());
    let private_reservation = named_path(
        directory,
        &attempt
            .destination()
            .reservation_name(attempt.attempt_id())
            .unwrap(),
    );
    let coordination = named_path(directory, attempt.destination().coordination());
    let mut builder = Builder::new_owned(file, direct_spec(), output_budget()).unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(9), 3).unwrap();
    let output = attempt
        .prepare_cancellable(
            builder.finish_owned().unwrap(),
            &crate::CancellationToken::new(),
        )
        .unwrap();
    (
        output,
        Paths {
            main,
            private_output,
            private_reservation,
            coordination,
        },
    )
}

fn prepared_replacement_output(directory: &Path) -> (PreparedOutput, Paths, [u8; 64]) {
    let (output, paths) = prepared_output(directory);
    fs::write(&paths.main, b"previous bytes").unwrap();
    let output = replacement::bind(output, &CancellationToken::new()).unwrap();
    let digest = output.previous.as_ref().unwrap().sha512;
    (output, paths, digest)
}

fn named_path(directory: &Path, name: &crate::publication::namespace::Name) -> PathBuf {
    directory.join(OsStr::from_bytes(name.bytes()))
}

fn file_name(path: &Path) -> &[u8] {
    path.file_name().unwrap().as_bytes()
}

fn assert_no_attempt_files(paths: &Paths) {
    assert!(!paths.main.exists());
    assert!(!paths.private_output.exists());
    assert!(!paths.private_reservation.exists());
    assert!(!paths.coordination.exists());
}

fn direct_spec() -> OutputSpec {
    OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::FIRST_SEEN,
        database_id: [31; 16],
        transaction_id: 32,
        commit_nonce: [33; 16],
        feed_index_limit: 0,
    }
}

fn output_budget() -> OutputBudget {
    OutputBudget {
        max_output_pages: 100_000,
    }
}

fn local_identity(byte: u8) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: 1,
        bytes: [byte; 32],
    }
}

struct Paths {
    main: PathBuf,
    private_output: PathBuf,
    private_reservation: PathBuf,
    coordination: PathBuf,
}

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new() -> Self {
        let path = crate::test_support_tests::unique_path("iprange-v4-publication-attempt");
        fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).unwrap();
    }
}
