use std::ffi::OsStr;
use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::path::{Path, PathBuf};

use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::publication::crash_tests::{run_child, run_replacement_child};
use crate::publication::output::CreatedOutput;

use super::*;

#[test]
fn listing_reports_only_stable_exact_names_and_optional_content_evidence() {
    let directory = TempDirectory::new();
    let complete = complete_output(&directory.path, [1; 16]);
    let partial_id = [2; 16];
    let partial = directory.path.join(name(partial_id));
    fs::write(&partial, b"partial").unwrap();
    fs::write(
        directory.path.join(".iprange-publish-NOT-AN-ATTEMPT.tmp"),
        b"foreign",
    )
    .unwrap();

    let mut entries = Vec::new();
    let summary = list_abandoned_publication_temps(
        &directory.path,
        &CancellationToken::new(),
        &mut |entry: &AbandonedPublicationTempEntry| {
            entries.push(*entry);
            Ok(AbandonedPublicationTempSinkControl::Continue)
        },
    )
    .unwrap();

    assert_eq!(summary.entries, 2);
    assert_eq!(entries.len(), 2);
    let complete_entry = entry(&entries, complete.attempt);
    assert_eq!(
        complete_entry.tuple,
        Some(PublicationTuple {
            database_id: [11; 16],
            transaction_id: 12,
            commit_nonce: [13; 16],
        })
    );
    assert!(complete_entry.digest.is_some());
    let partial_entry = entry(&entries, partial_id);
    assert!(partial_entry.tuple.is_none());
    assert!(partial_entry.digest.is_none());
}

#[test]
fn exact_removal_handles_complete_partial_and_already_absent_outputs() {
    let directory = TempDirectory::new();
    let complete = complete_output(&directory.path, [3; 16]);
    let partial_id = [4; 16];
    let partial = directory.path.join(name(partial_id));
    fs::write(&partial, b"partial").unwrap();
    let entries = listed(&directory.path);
    let complete_entry = entry(&entries, complete.attempt);
    let partial_entry = entry(&entries, partial_id);

    assert!(
        remove_abandoned_publication_temp(
            &directory.path,
            complete_entry.directory_identity,
            complete_entry.publication_attempt_id,
            complete_entry.artifact_identity,
            complete_entry.tuple,
            complete_entry.digest,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );
    assert!(
        remove_abandoned_publication_temp(
            &directory.path,
            partial_entry.directory_identity,
            partial_entry.publication_attempt_id,
            partial_entry.artifact_identity,
            None,
            None,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );
    assert!(
        !remove_abandoned_publication_temp(
            &directory.path,
            partial_entry.directory_identity,
            partial_entry.publication_attempt_id,
            partial_entry.artifact_identity,
            None,
            None,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );
}

#[test]
fn removal_rejects_changed_identity_content_and_directory() {
    let directory = TempDirectory::new();
    let complete = complete_output(&directory.path, [5; 16]);
    let entry = entry(&listed(&directory.path), complete.attempt);
    let mut wrong_directory = entry.directory_identity;
    wrong_directory.bytes[0] ^= 1;
    assert_eq!(
        remove_abandoned_publication_temp(
            &directory.path,
            wrong_directory,
            entry.publication_attempt_id,
            entry.artifact_identity,
            entry.tuple,
            entry.digest,
            &CancellationToken::new(),
        )
        .unwrap_err()
        .code(),
        crate::ErrorCode::DirectoryIdentityMismatch
    );

    fs::write(&complete.path, b"changed").unwrap();
    assert_eq!(
        remove_abandoned_publication_temp(
            &directory.path,
            entry.directory_identity,
            entry.publication_attempt_id,
            entry.artifact_identity,
            entry.tuple,
            entry.digest,
            &CancellationToken::new(),
        )
        .unwrap_err()
        .code(),
        crate::ErrorCode::CleanupConflict
    );
    assert!(complete.path.exists());
}

#[test]
fn listing_honors_cancellation_stop_and_sink_failure() {
    let directory = TempDirectory::new();
    complete_output(&directory.path, [6; 16]);
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    let error = list_abandoned_publication_temps(
        &directory.path,
        &cancelled,
        &mut |_entry: &AbandonedPublicationTempEntry| {
            Ok(AbandonedPublicationTempSinkControl::Continue)
        },
    )
    .unwrap_err();
    assert_eq!(error.code(), crate::ErrorCode::Cancelled);

    let error = list_abandoned_publication_temps(
        &directory.path,
        &CancellationToken::new(),
        &mut |_entry: &AbandonedPublicationTempEntry| Ok(AbandonedPublicationTempSinkControl::Stop),
    )
    .unwrap_err();
    assert_eq!(error.code(), crate::ErrorCode::StoppedBySink);

    let error = list_abandoned_publication_temps(
        &directory.path,
        &CancellationToken::new(),
        &mut |_entry: &AbandonedPublicationTempEntry| Err(crate::Error::InvalidArgument("sink")),
    )
    .unwrap_err();
    assert_eq!(error.code(), crate::ErrorCode::SinkFailed);
}

#[test]
fn reservation_listing_reports_bound_policy_phase_and_previous_evidence() {
    let fail_directory = TempDirectory::new();
    let fail_main = fail_directory.path.join("fail.v4");
    run_child(&fail_main, "publication.after_reservation_state1_sync");
    let fail_entries = listed_reservations(&fail_directory.path);
    assert_eq!(fail_entries.len(), 1);
    let fail = fail_entries[0];
    let fail_evidence = fail.evidence.unwrap();
    assert_eq!(
        fail_evidence.policy,
        AbandonedReservationPolicy::FailIfExists
    );
    assert_eq!(fail_evidence.phase, AbandonedReservationPhase::Prepared);
    assert_eq!(fail_evidence.output.tuple.database_id, [41; 16]);
    assert_eq!(fail_evidence.output.tuple.transaction_id, 42);
    assert_eq!(fail_evidence.output.tuple.commit_nonce, [43; 16]);
    assert!(fail_evidence.output.digest.byte_length > 2 * 4096);
    assert!(fail_evidence.previous.is_none());

    let replace_directory = TempDirectory::new();
    let replace_main = replace_directory.path.join("replace.v4");
    run_replacement_child(&replace_main, "publication.after_reservation_state1_sync");
    let replace = listed_reservations(&replace_directory.path)[0];
    let replace_evidence = replace.evidence.unwrap();
    assert_eq!(
        replace_evidence.policy,
        AbandonedReservationPolicy::ReplaceExisting
    );
    assert_eq!(replace_evidence.phase, AbandonedReservationPhase::Prepared);
    assert_eq!(
        replace_evidence.previous.unwrap().1.byte_length,
        b"previous bytes".len() as u64
    );
}

#[test]
fn reservation_listing_includes_malformed_exact_names_without_evidence() {
    let directory = TempDirectory::new();
    let malformed_id = [7; 16];
    fs::write(
        directory.path.join(reservation_name(malformed_id)),
        b"partial",
    )
    .unwrap();
    fs::write(
        directory
            .path
            .join(".iprange-reservation-NOT-AN-ATTEMPT.tmp"),
        b"foreign",
    )
    .unwrap();

    let entries = listed_reservations(&directory.path);
    assert_eq!(entries.len(), 1);
    assert_eq!(entries[0].publication_attempt_id, malformed_id);
    assert!(entries[0].evidence.is_none());
}

#[test]
fn reservation_removal_handles_bound_malformed_and_already_absent_artifacts() {
    let directory = TempDirectory::new();
    let main = directory.path.join("result.v4");
    run_child(&main, "publication.after_reservation_state1_sync");
    let bound = listed_reservations(&directory.path)[0];
    assert!(
        remove_abandoned_reservation_artifact(
            &directory.path,
            bound.directory_identity,
            bound.publication_attempt_id,
            bound.artifact_identity,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );

    let malformed_id = [8; 16];
    fs::write(
        directory.path.join(reservation_name(malformed_id)),
        b"partial",
    )
    .unwrap();
    let malformed = reservation_entry(&listed_reservations(&directory.path), malformed_id);
    assert!(
        remove_abandoned_reservation_artifact(
            &directory.path,
            malformed.directory_identity,
            malformed.publication_attempt_id,
            malformed.artifact_identity,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );
    assert!(
        !remove_abandoned_reservation_artifact(
            &directory.path,
            malformed.directory_identity,
            malformed.publication_attempt_id,
            malformed.artifact_identity,
            &CancellationToken::new(),
        )
        .unwrap()
        .source_present
    );
}

#[test]
fn reservation_removal_rejects_wrong_directory_identity_and_header_binding() {
    let directory = TempDirectory::new();
    let main = directory.path.join("result.v4");
    run_child(&main, "publication.after_reservation_state1_sync");
    let bound = listed_reservations(&directory.path)[0];
    let mut wrong_directory = bound.directory_identity;
    wrong_directory.bytes[0] ^= 1;
    assert_eq!(
        remove_abandoned_reservation_artifact(
            &directory.path,
            wrong_directory,
            bound.publication_attempt_id,
            bound.artifact_identity,
            &CancellationToken::new(),
        )
        .unwrap_err()
        .code(),
        crate::ErrorCode::DirectoryIdentityMismatch
    );

    let source = directory
        .path
        .join(reservation_name(bound.publication_attempt_id));
    let copied_id = [9; 16];
    let copied = directory.path.join(reservation_name(copied_id));
    fs::copy(source, copied).unwrap();
    let copied = reservation_entry(&listed_reservations(&directory.path), copied_id);
    assert!(copied.evidence.is_none());
    assert_eq!(
        remove_abandoned_reservation_artifact(
            &directory.path,
            copied.directory_identity,
            copied.publication_attempt_id,
            copied.artifact_identity,
            &CancellationToken::new(),
        )
        .unwrap_err()
        .code(),
        crate::ErrorCode::CleanupConflict
    );
}

#[test]
fn reservation_listing_honors_cancellation_stop_and_sink_failure() {
    let directory = TempDirectory::new();
    fs::write(directory.path.join(reservation_name([10; 16])), b"partial").unwrap();
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    let error = list_abandoned_reservation_artifacts(
        &directory.path,
        &cancelled,
        &mut |_entry: &AbandonedReservationEntry| Ok(AbandonedReservationSinkControl::Continue),
    )
    .unwrap_err();
    assert_eq!(error.code(), crate::ErrorCode::Cancelled);

    let error = list_abandoned_reservation_artifacts(
        &directory.path,
        &CancellationToken::new(),
        &mut |_entry: &AbandonedReservationEntry| Ok(AbandonedReservationSinkControl::Stop),
    )
    .unwrap_err();
    assert_eq!(error.code(), crate::ErrorCode::StoppedBySink);

    let error = list_abandoned_reservation_artifacts(
        &directory.path,
        &CancellationToken::new(),
        &mut |_entry: &AbandonedReservationEntry| Err(crate::Error::InvalidArgument("sink")),
    )
    .unwrap_err();
    assert_eq!(error.code(), crate::ErrorCode::SinkFailed);
}

fn listed(directory: &Path) -> Vec<AbandonedPublicationTempEntry> {
    let mut entries = Vec::new();
    list_abandoned_publication_temps(
        directory,
        &CancellationToken::new(),
        &mut |entry: &AbandonedPublicationTempEntry| {
            entries.push(*entry);
            Ok(AbandonedPublicationTempSinkControl::Continue)
        },
    )
    .unwrap();
    entries
}

fn listed_reservations(directory: &Path) -> Vec<AbandonedReservationEntry> {
    let mut entries = Vec::new();
    list_abandoned_reservation_artifacts(
        directory,
        &CancellationToken::new(),
        &mut |entry: &AbandonedReservationEntry| {
            entries.push(*entry);
            Ok(AbandonedReservationSinkControl::Continue)
        },
    )
    .unwrap();
    entries
}

fn reservation_entry(
    entries: &[AbandonedReservationEntry],
    attempt: [u8; 16],
) -> AbandonedReservationEntry {
    *entries
        .iter()
        .find(|entry| entry.publication_attempt_id == attempt)
        .unwrap()
}

fn entry(
    entries: &[AbandonedPublicationTempEntry],
    attempt: [u8; 16],
) -> AbandonedPublicationTempEntry {
    *entries
        .iter()
        .find(|entry| entry.publication_attempt_id == attempt)
        .unwrap()
}

fn complete_output(directory: &Path, _seed: [u8; 16]) -> Complete {
    let main = directory.join("result.v4");
    let secured = CreatedOutput::create(&main).unwrap().secure().unwrap();
    let (attempt, file) = secured.into_parts();
    let path = directory.join(OsStr::from_bytes(attempt.name().bytes()));
    let mut builder = Builder::new_owned(
        file,
        OutputSpec {
            address_family: AddressFamily::Ipv4,
            value_kind: ValueKind::Direct,
            value_tag: ValueTag::RETENTION,
            database_id: [11; 16],
            transaction_id: 12,
            commit_nonce: [13; 16],
            feed_index_limit: 0,
        },
        OutputBudget {
            max_output_pages: 1_000,
        },
    )
    .unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(2), 3).unwrap();
    let output = attempt
        .prepare_cancellable(
            builder.finish_owned().unwrap(),
            &crate::CancellationToken::new(),
        )
        .unwrap();
    let complete = Complete {
        attempt: output.attempt.attempt_id(),
        path,
    };
    drop(output);
    complete
}

fn name(attempt: [u8; 16]) -> String {
    let mut name = String::from(".iprange-publish-");
    for byte in attempt {
        use std::fmt::Write;
        write!(&mut name, "{byte:02x}").unwrap();
    }
    name.push_str(".tmp");
    name
}

fn reservation_name(attempt: [u8; 16]) -> String {
    let mut name = String::from(".iprange-reservation-");
    for byte in attempt {
        use std::fmt::Write;
        write!(&mut name, "{byte:02x}").unwrap();
    }
    name.push_str(".tmp");
    name
}

struct Complete {
    attempt: [u8; 16],
    path: PathBuf,
}

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new() -> Self {
        let path = crate::test_support_tests::unique_path("iprange-v4-publication-maintenance");
        fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).unwrap();
    }
}
