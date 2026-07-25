use std::ffi::OsStr;
use std::fs;
use std::path::{Path, PathBuf};

use crate::error::ErrorCode;
use crate::publication::crash_tests::{run_child, Artifacts, TempDirectory};

use super::*;

#[test]
fn absence_and_one_private_reservation_are_reported_without_a_handle() {
    let empty = TempDirectory::new("residue-empty");
    let empty_main = empty.path.join("result.v4");
    let inspected = inspect_publication_residue(&empty_main, &CancellationToken::new()).unwrap();
    assert_eq!(
        inspected.coordination,
        PublicationResidueCoordination::Absent
    );
    assert!(inspected.coordination_identity.is_none());
    assert!(inspected.publication.is_none());
    assert!(inspected.handle.is_none());

    let directory = TempDirectory::new("residue-private");
    let main = directory.path.join("result.v4");
    run_child(&main, "publication.after_reservation_state1_sync");
    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    let publication = inspected.publication.unwrap();
    assert_eq!(
        inspected.coordination,
        PublicationResidueCoordination::Absent
    );
    assert!(inspected.handle.is_none());
    assert_eq!(publication.attempt.database_id, [41; 16]);
    assert_eq!(publication.attempt.transaction_id, 42);
    assert!(!publication.main_namespace_may_have_been_attempted);
}

#[test]
fn selectable_canonical_reservation_is_reconstructed_but_not_removed() {
    let directory = TempDirectory::new("residue-selectable");
    let main = directory.path.join("result.v4");
    run_child(&main, "publication.after_reservation_directory_sync");
    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    assert_eq!(
        inspected.coordination,
        PublicationResidueCoordination::PublicationReservation
    );
    assert!(inspected.publication.is_some());

    let error = remove_publication_residue(inspected.handle.unwrap(), &CancellationToken::new())
        .unwrap_err();
    assert_eq!(error.code, ErrorCode::Conflict);
    assert!(coordination_path(&main).exists());
}

#[test]
fn malformed_canonical_residue_is_removed_durably_and_exactly() {
    let directory = TempDirectory::new("residue-malformed");
    let main = directory.path.join("result.v4");
    let coordination = coordination_path(&main);
    fs::write(&coordination, b"malformed").unwrap();

    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    assert_eq!(
        inspected.coordination,
        PublicationResidueCoordination::Unselectable
    );
    assert!(inspected.publication.is_none());
    let result =
        remove_publication_residue(inspected.handle.unwrap(), &CancellationToken::new()).unwrap();

    assert_eq!(
        result.cleanup_state(),
        crate::publication::CleanupState::Clean
    );
    assert_eq!(
        result.later_coordination,
        PublicationResidueCoordination::Absent
    );
    assert!(result.main.is_none());
    assert!(!coordination.exists());
    assert!(!main.exists());
}

#[test]
fn removal_hashes_but_never_changes_an_arbitrary_main() {
    let directory = TempDirectory::new("residue-arbitrary-main");
    let main = directory.path.join("result.v4");
    let expected = b"arbitrary previous bytes";
    fs::write(&main, expected).unwrap();
    fs::write(coordination_path(&main), b"malformed").unwrap();

    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    let result =
        remove_publication_residue(inspected.handle.unwrap(), &CancellationToken::new()).unwrap();
    let evidence = result.main.unwrap();

    assert_eq!(fs::read(&main).unwrap(), expected);
    assert_eq!(evidence.content, PublicationResidueMainContent::Other);
    assert!(evidence.tuple.is_none());
    assert_eq!(evidence.digest.byte_length, expected.len() as u64);
}

#[test]
fn removal_reports_a_readable_v4_main_tuple_without_validating_its_graph() {
    let directory = TempDirectory::new("residue-v4-main");
    let main = directory.path.join("result.v4");
    run_child(&main, "publication.after_reservation_state1_sync");
    let artifacts = Artifacts::inspect(&directory.path, &main);
    fs::copy(&artifacts.private_outputs[0], &main).unwrap();
    fs::write(coordination_path(&main), b"malformed").unwrap();

    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    let result =
        remove_publication_residue(inspected.handle.unwrap(), &CancellationToken::new()).unwrap();
    let evidence = result.main.unwrap();

    assert_eq!(evidence.content, PublicationResidueMainContent::V4);
    assert_eq!(evidence.tuple.unwrap().database_id, [41; 16]);
    assert_eq!(evidence.tuple.unwrap().transaction_id, 42);
    assert!(main.exists());
}

#[test]
fn changed_or_newly_selectable_coordination_is_never_removed() {
    let directory = TempDirectory::new("residue-changed");
    let main = directory.path.join("result.v4");
    let coordination = coordination_path(&main);
    fs::write(&coordination, b"malformed").unwrap();
    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    fs::remove_file(&coordination).unwrap();
    fs::write(&coordination, b"replacement").unwrap();

    let error = remove_publication_residue(inspected.handle.unwrap(), &CancellationToken::new())
        .unwrap_err();
    assert_eq!(error.code, ErrorCode::CleanupConflict);
    assert_eq!(fs::read(&coordination).unwrap(), b"replacement");

    let source = TempDirectory::new("residue-selectable-source");
    let source_main = source.path.join("result.v4");
    run_child(&source_main, "publication.after_reservation_directory_sync");
    let selected_bytes = fs::read(coordination_path(&source_main)).unwrap();
    fs::write(&coordination, b"malformed").unwrap();
    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    fs::write(&coordination, selected_bytes).unwrap();

    let error = remove_publication_residue(inspected.handle.unwrap(), &CancellationToken::new())
        .unwrap_err();
    assert_eq!(error.code, ErrorCode::Conflict);
    assert!(coordination.exists());
}

#[test]
fn cancellation_and_a_ready_live_sidecar_change_nothing() {
    let directory = TempDirectory::new("residue-cancelled");
    let main = directory.path.join("result.v4");
    let coordination = coordination_path(&main);
    fs::write(&coordination, b"malformed").unwrap();
    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    cancellation.cancel();
    let error = remove_publication_residue(inspected.handle.unwrap(), &cancellation).unwrap_err();
    assert_eq!(error.code, ErrorCode::Cancelled);
    assert_eq!(fs::read(&coordination).unwrap(), b"malformed");

    fs::remove_file(&coordination).unwrap();
    let sidecar = crate::live_sidecar::Sidecar::create(&main, [1; 16], [2; 16], 2).unwrap();
    sidecar.publish_ready().unwrap();
    let inspected = inspect_publication_residue(&main, &CancellationToken::new()).unwrap();
    assert_eq!(
        inspected.coordination,
        PublicationResidueCoordination::LiveSidecar
    );
    let error = remove_publication_residue(inspected.handle.unwrap(), &CancellationToken::new())
        .unwrap_err();
    assert_eq!(error.code, ErrorCode::Conflict);
    assert!(coordination.exists());
}

#[test]
fn hard_linked_coordination_is_rejected_during_inspection() {
    let directory = TempDirectory::new("residue-hard-link");
    let main = directory.path.join("result.v4");
    let coordination = coordination_path(&main);
    fs::write(&coordination, b"malformed").unwrap();
    fs::hard_link(&coordination, directory.path.join("alias")).unwrap();

    let error = inspect_publication_residue(&main, &CancellationToken::new()).unwrap_err();
    assert_eq!(error.code, ErrorCode::Conflict);
    assert!(coordination.exists());
}

fn coordination_path(main: &Path) -> PathBuf {
    let mut name = main.file_name().unwrap().to_os_string();
    name.push(OsStr::new(".readers"));
    main.with_file_name(name)
}
