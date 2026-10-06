use std::fs;
use std::path::PathBuf;

use crate::{create_live, AddressFamily, CancellationToken, LiveReader, ValueKind, ValueTag};

use super::*;

struct Files(PathBuf);

impl Files {
    fn new(label: &str) -> Self {
        Self(crate::test_support_tests::unique_path(&format!(
            "iprange-v4-create-resolution-{label}"
        )))
    }

    fn sidecar(&self) -> PathBuf {
        crate::path::canonical_sidecar(&self.0).unwrap()
    }

    fn create(&self) -> CreateResult {
        self.create_with(true)
    }

    fn create_with(&self, creator_only: bool) -> CreateResult {
        create_live(
            &self.0,
            AddressFamily::Ipv4,
            ValueKind::Direct,
            crate::contract::StructureKind::None,
            ValueTag::new(b"asn").unwrap(),
            2,
            &crate::CancellationToken::new(),
            creator_only,
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
                true,
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
#[cfg(unix)]
fn repair_follows_the_sidecars_recorded_choice() {
    use std::os::unix::fs::PermissionsExt as _;

    // An interrupted create whose sidecar records Unprotected must
    // repair to an unprotected main: the recreation follows the
    // sidecar, never a hardcoded creator-only default (SOW-0035 AC2,
    // "repair recreations").
    let files = Files::new("repair-unprotected");
    let mut supplied = files.create_with(false);
    fs::remove_file(&files.0).unwrap();
    supplied.state = CreationState::OutcomeUnknown;
    supplied.residue_possible = true;

    let resolved = resolve_create_live(
        &files.0,
        &supplied,
        LiveTransitionResolutionMode::Complete,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.state, CreationState::Created);
    let mode = fs::metadata(&files.0).unwrap().permissions().mode() & 0o777;
    assert_ne!(mode, 0o600, "unprotected sidecar repaired to mode 0600");

    // And the mirror: a sidecar recording Protected repairs protected.
    let files = Files::new("repair-protected");
    let mut supplied = files.create_with(true);
    fs::remove_file(&files.0).unwrap();
    supplied.state = CreationState::OutcomeUnknown;
    supplied.residue_possible = true;
    let resolved = resolve_create_live(
        &files.0,
        &supplied,
        LiveTransitionResolutionMode::Complete,
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(resolved.state, CreationState::Created);
    let mode = fs::metadata(&files.0).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode, 0o600, "protected sidecar did not repair to 0600");
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

#[test]
fn sidecar_policy_span_arms_decode_exactly() {
    // The policy span accepts exactly the three canonical pairs:
    // generation-0 legacy (pre-decision, proof-checked), generation-1
    // unprotected, generation-1 protected. Everything else is corrupt,
    // and a corrupt span never opens as though it recorded a choice.
    use crate::live_sidecar::Policy;
    let files = Files::new("policy-arms");
    let result = files.create_with(true);
    let sidecar_path = files.sidecar();
    let mut page = fs::read(&sidecar_path).unwrap();
    let mut write_policy = |generation: u16, byte: u8| {
        page[20..22].copy_from_slice(&generation.to_le_bytes());
        page[22] = byte;
        let checksum = crate::crc32c::crc32c_with_zeroed(&page[..4096], 64, 4).unwrap();
        page[64..68].copy_from_slice(&checksum.to_le_bytes());
        fs::write(&sidecar_path, &page).unwrap();
    };
    let decode = || {
        let file = fs::File::open(&sidecar_path).unwrap();
        crate::live_sidecar::read_header(&file).map(|(_, header)| header.policy)
    };

    write_policy(0, 0);
    assert_eq!(decode().unwrap(), Policy::Legacy);
    write_policy(1, 0);
    assert_eq!(decode().unwrap(), Policy::Unprotected);
    write_policy(1, 1);
    assert_eq!(decode().unwrap(), Policy::Protected);
    for (generation, byte) in [(0u16, 1u8), (2, 0), (2, 1), (1, 2), (1, 0xFF)] {
        write_policy(generation, byte);
        assert!(
            decode().is_err(),
            "policy span ({generation}, {byte}) decoded instead of failing corrupt"
        );
    }
    drop(result);
}
