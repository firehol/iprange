use std::fs;

use crate::publication::crash_tests::TempDirectory;

use super::*;

#[test]
fn incomplete_removal_retains_exact_authority_for_retry() {
    let directory = TempDirectory::new("residue-retry");
    let main_path = directory.path.join("result.v4");
    let coordination_path = main_path.with_file_name("result.v4.readers");
    fs::write(&coordination_path, b"malformed").unwrap();

    let inspected = inspect(&main_path, &CancellationToken::new()).unwrap();
    let mut handle = inspected.handle.unwrap().inner;
    verify_coordination(&handle).unwrap();
    live_lock::lock_cancellable(
        &handle.coordination,
        OPERATION_LOCK,
        Mode::Exclusive,
        &CancellationToken::new(),
    )
    .unwrap();
    reject_selectable(&handle.coordination).unwrap();
    let main = super::super::main::inspect(&handle.destination, &CancellationToken::new()).unwrap();
    let retired = super::super::retirement::retire(
        &handle.destination,
        &handle.coordination,
        handle.coordination_identity,
    )
    .unwrap();
    assert!(retired.cause.is_none());
    handle.retired = Some(Retired {
        main,
        housekeeping: retired.housekeeping,
        visible: retired.visible,
        retirement_pending: false,
    });

    let incomplete = incomplete(
        handle,
        cleanup_conflict("injected directory synchronization failure"),
    );
    assert_eq!(
        incomplete.coordination_cleanup,
        CoordinationCleanup::CleanupGuard
    );
    assert!(incomplete.handle.is_some());
    assert!(!coordination_path.exists());

    let completed = remove(incomplete.handle.unwrap().inner, &CancellationToken::new()).unwrap();
    assert!(completed.cause.is_none());
    assert!(completed.handle.is_none());
    assert_eq!(completed.coordination_cleanup, CoordinationCleanup::None);
    assert_eq!(
        completed.later_coordination,
        PublicationResidueCoordination::Absent
    );
}
