use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use crate::{create_live, AddressFamily, CancellationToken, LiveReader, ValueKind, ValueTag};

use super::*;

struct Files(PathBuf);

impl Files {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-create-resolution-{label}-{}-{unique}",
            std::process::id()
        )))
    }

    fn sidecar(&self) -> PathBuf {
        crate::path::canonical_sidecar(&self.0).unwrap()
    }

    fn create(&self) -> CreateResult {
        create_live(
            &self.0,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            ValueTag::new(b"asn").unwrap(),
            2,
            &crate::CancellationToken::new(),
        )
        .unwrap()
    }

    fn interrupted_sidecar_only(&self) -> CreateResult {
        let mut result = self.create();
        fs::remove_file(&self.0).unwrap();
        fs::remove_file(self.sidecar()).unwrap();
        let sidecar = Sidecar::reserve(
            &self.0,
            result.database_id,
            result.sidecar_id,
            result.reader_capacity,
        )
        .unwrap();
        sidecar.initialize_creating().unwrap();
        crate::live_namespace::sync_parent(&sidecar.path).unwrap();
        result.state = CreationState::OutcomeUnknown;
        result.residue_possible = true;
        result.main_identity = None;
        result.sidecar_identity = Some(crate::live_namespace::public_identity(
            sidecar.local_identity(),
        ));
        result
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
        let _ = fs::remove_file(self.sidecar());
    }
}

#[test]
fn sidecar_only_creation_can_be_completed() {
    let files = Files::new("complete");
    let supplied = files.interrupted_sidecar_only();

    let resolved = resolve_create_live(
        &files.0,
        &supplied,
        LiveTransitionResolutionMode::Complete,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.state, CreationState::Created);
    assert!(resolved.main_identity.is_some());
    let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
    reader.close().unwrap();
}

#[test]
fn sidecar_only_creation_can_be_rolled_back() {
    let files = Files::new("rollback");
    let supplied = files.interrupted_sidecar_only();

    let resolved = resolve_create_live(
        &files.0,
        &supplied,
        LiveTransitionResolutionMode::Rollback,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.state, CreationState::NotCreated);
    assert!(!files.0.exists());
    assert!(!files.sidecar().exists());
}

#[test]
fn a_ready_pair_is_never_removed_by_resolution() {
    let files = Files::new("ready");
    let supplied = files.create();

    let resolved = resolve_create_live(
        &files.0,
        &supplied,
        LiveTransitionResolutionMode::Rollback,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.state, CreationState::Created);
    let mut reader = LiveReader::open(&files.0, &crate::CancellationToken::new()).unwrap();
    reader.close().unwrap();
}
