use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use crate::{
    create_live, AddressFamily, CancellationToken, ImmutableReader, LiveReader, ValueKind, ValueTag,
};

use super::*;

struct Files {
    main: PathBuf,
    attempt_id: [u8; 16],
}

impl Files {
    fn new(label: &str, attempt_byte: u8) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-resolution-{label}-{}-{unique}",
                std::process::id()
            )),
            attempt_id: [attempt_byte; 16],
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
    let files = Files::new("initialize-complete", 0x41);
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
    let files = Files::new("initialize-rollback", 0x42);
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
    let files = Files::new("reset-complete", 0x43);
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
    live_sidecar::sync_parent(&sidecar.path).unwrap();
    let supplied = supplied(
        LiveTransitionOperation::Reset,
        &main,
        files.attempt_id,
        2,
        Some(live_sidecar::public_identity(previous)),
        sidecar.local_identity(),
        LiveCoordinationLocation::Private,
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
fn exchanged_reset_cleans_the_exact_previous_sidecar() {
    let files = Files::new("reset-exchanged", 0x44);
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
        files.attempt_id,
        2,
        Some(live_sidecar::public_identity(previous)),
        sidecar.local_identity(),
        LiveCoordinationLocation::Canonical,
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
    live_sidecar::sync_parent(&files.sidecar()).unwrap();
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
    live_sidecar::sync_parent(&sidecar.path).unwrap();
    let result = supplied(
        LiveTransitionOperation::Initialize,
        &main,
        files.attempt_id,
        2,
        None,
        sidecar.local_identity(),
        LiveCoordinationLocation::Canonical,
    );
    drop(sidecar);
    drop(main);
    result
}

fn supplied(
    operation: LiveTransitionOperation,
    main: &LockedMain,
    sidecar_id: [u8; 16],
    reader_capacity: u32,
    previous_sidecar_identity: Option<LocalFileIdentity>,
    sidecar_identity: live_sidecar::Identity,
    location: LiveCoordinationLocation,
) -> LiveTransitionResult {
    LiveTransitionResult {
        operation,
        reset_policy: (operation == LiveTransitionOperation::Reset)
            .then_some(LiveResetPolicy::RollbackSafe),
        status: LiveTransitionStatus::OutcomeUnknown,
        database_id: main.bootstrap.meta.database_id,
        transaction_id: main.bootstrap.meta.txn_id,
        commit_nonce: main.bootstrap.meta.commit_nonce,
        directory_identity: main.directory_identity,
        main_identity: main.public_identity,
        main_basename: main.basename,
        reader_capacity,
        sidecar_id,
        previous_sidecar_identity,
        new_sidecar_identity: Some(live_sidecar::public_identity(sidecar_identity)),
        new_sidecar_location: location,
        residue_possible: true,
        cause: None,
    }
}
