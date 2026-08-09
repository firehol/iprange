use std::fs;
use std::path::PathBuf;

use crate::{
    create_live, AddressFamily, CancellationToken, ImmutableReader, LiveReader, ValueKind, ValueTag,
};

use super::*;

struct Files {
    main: PathBuf,
    attempt_id: [u8; 16],
}

impl Files {
    fn new(label: &str) -> Self {
        Self {
            main: crate::test_support_tests::unique_path(&format!("iprange-v4-resolution-{label}")),
            attempt_id: crate::random::nonzero_128().unwrap(),
        }
    }

    fn sidecar(&self) -> PathBuf {
        crate::path::canonical_sidecar(&self.main).unwrap()
    }

    fn private(&self) -> PathBuf {
        crate::path::live_transition_temp(&self.main).unwrap()
    }

    fn create(&self) {
        create_live(
            &self.main,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            ValueTag::new(b"asn").unwrap(),
            1,
            &crate::CancellationToken::new(),
        )
        .unwrap();
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
        let _ = fs::remove_file(self.private());
    }
}

#[test]
fn creating_initialize_can_be_completed_exactly() {
    let files = Files::new("initialize-complete");
    let supplied = prepare_initialize(&files);

    let resolved = resolve_live_transition(
        &files.main,
        &supplied,
        LiveTransitionResolutionMode::Complete,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.status, LiveTransitionStatus::Initialized);
    let mut reader = LiveReader::open(&files.main, &crate::CancellationToken::new()).unwrap();
    reader.close().unwrap();
}

#[test]
fn creating_initialize_can_be_rolled_back_exactly() {
    let files = Files::new("initialize-rollback");
    let supplied = prepare_initialize(&files);

    let resolved = resolve_live_transition(
        &files.main,
        &supplied,
        LiveTransitionResolutionMode::Rollback,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.status, LiveTransitionStatus::Unchanged);
    assert!(!files.sidecar().exists());
    assert_eq!(
        ImmutableReader::open(&files.main)
            .unwrap()
            .info()
            .transaction_id,
        1
    );
}

#[test]
fn prepared_reset_over_corrupt_coordination_can_be_completed() {
    let files = Files::new("reset-complete");
    files.create();
    fs::write(files.sidecar(), b"corrupt").unwrap();
    let token = CancellationToken::new();
    let main = LockedMain::open(&files.main, &token).unwrap();
    let previous = existing_identity(&files.sidecar()).unwrap().unwrap();
    let sidecar = Sidecar::reserve_at(
        files.private(),
        main.bootstrap.meta.database_id,
        files.attempt_id,
        2,
    )
    .unwrap();
    sidecar.initialize_creating().unwrap();
    sidecar.publish_ready().unwrap();
    crate::live_namespace::sync_parent(&sidecar.path).unwrap();
    let supplied = supplied(
        LiveTransitionOperation::Reset,
        &main,
        Some(LiveResetPolicy::DiscardPrevious),
        SidecarFacts {
            id: files.attempt_id,
            capacity: 2,
            previous: Some(crate::live_namespace::public_identity(previous)),
            identity: sidecar.local_identity(),
            location: LiveCoordinationLocation::Private,
        },
    );
    drop(sidecar);
    drop(main);

    let resolved = resolve_live_transition(
        &files.main,
        &supplied,
        LiveTransitionResolutionMode::Complete,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.status, LiveTransitionStatus::Initialized);
    let mut first = LiveReader::open(&files.main, &crate::CancellationToken::new()).unwrap();
    let mut second = LiveReader::open(&files.main, &crate::CancellationToken::new()).unwrap();
    first.close().unwrap();
    second.close().unwrap();
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn exchanged_reset_cleans_the_exact_previous_sidecar() {
    let files = Files::new("reset-exchanged");
    files.create();
    fs::write(files.sidecar(), b"corrupt").unwrap();
    let token = CancellationToken::new();
    let main = LockedMain::open(&files.main, &token).unwrap();
    let previous = existing_identity(&files.sidecar()).unwrap().unwrap();
    let sidecar = Sidecar::reserve_at(
        files.private(),
        main.bootstrap.meta.database_id,
        files.attempt_id,
        2,
    )
    .unwrap();
    sidecar.initialize_creating().unwrap();
    sidecar.publish_ready().unwrap();
    let supplied = supplied(
        LiveTransitionOperation::Reset,
        &main,
        Some(LiveResetPolicy::RollbackSafe),
        SidecarFacts {
            id: files.attempt_id,
            capacity: 2,
            previous: Some(crate::live_namespace::public_identity(previous)),
            identity: sidecar.local_identity(),
            location: LiveCoordinationLocation::Canonical,
        },
    );
    crate::live_lifecycle::namespace::install(
        &files.private(),
        &sidecar.file,
        &files.sidecar(),
        sidecar.local_identity(),
        Some(previous),
        LiveResetPolicy::RollbackSafe,
    )
    .unwrap();
    crate::live_namespace::sync_parent(&files.sidecar()).unwrap();
    drop(sidecar);
    drop(main);

    let resolved = resolve_live_transition(
        &files.main,
        &supplied,
        LiveTransitionResolutionMode::Complete,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.status, LiveTransitionStatus::Initialized);
    assert!(!files.private().exists());
    let mut reader = LiveReader::open(&files.main, &CancellationToken::new()).unwrap();
    reader.close().unwrap();
}

fn prepare_initialize(files: &Files) -> LiveTransitionResult {
    files.create();
    fs::remove_file(files.sidecar()).unwrap();
    let main = LockedMain::open(&files.main, &CancellationToken::new()).unwrap();
    let sidecar = Sidecar::reserve(
        &files.main,
        main.bootstrap.meta.database_id,
        files.attempt_id,
        2,
    )
    .unwrap();
    sidecar.initialize_creating().unwrap();
    crate::live_namespace::sync_parent(&sidecar.path).unwrap();
    let result = supplied(
        LiveTransitionOperation::Initialize,
        &main,
        None,
        SidecarFacts {
            id: files.attempt_id,
            capacity: 2,
            previous: None,
            identity: sidecar.local_identity(),
            location: LiveCoordinationLocation::Canonical,
        },
    );
    drop(sidecar);
    drop(main);
    result
}

struct SidecarFacts {
    id: [u8; 16],
    capacity: u32,
    previous: Option<LocalFileIdentity>,
    identity: crate::live_namespace::Identity,
    location: LiveCoordinationLocation,
}

fn supplied(
    operation: LiveTransitionOperation,
    main: &LockedMain,
    reset_policy: Option<LiveResetPolicy>,
    sidecar: SidecarFacts,
) -> LiveTransitionResult {
    LiveTransitionResult {
        operation,
        reset_policy,
        status: LiveTransitionStatus::OutcomeUnknown,
        database_id: main.bootstrap.meta.database_id,
        transaction_id: main.bootstrap.meta.txn_id,
        commit_nonce: main.bootstrap.meta.commit_nonce,
        directory_identity: main.directory_identity,
        main_identity: main.public_identity,
        main_basename: main.basename,
        reader_capacity: sidecar.capacity,
        sidecar_id: sidecar.id,
        previous_sidecar_identity: sidecar.previous,
        new_sidecar_identity: Some(crate::live_namespace::public_identity(sidecar.identity)),
        new_sidecar_location: sidecar.location,
        residue_possible: true,
        housekeeping: crate::publication::Housekeeping::None,
        visible_housekeeping: Box::default(),
        cause: None,
    }
}
