//! Recovery follows the source sidecar's recorded choice end to end:
//! a protected source recovers to a 0600 output through the worker
//! (the parent creates the attempt under the source policy, and the
//! worker resumes it under the policy the attempt facts record — the
//! arm the round-1 fix restored), and an unprotected source does not
//! force 0600.

#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;

use iprange_livedb::recovery::{
    inspect_recovery_candidates, recover_live, RecoveryBudget, RecoveryInspectionMode,
    RecoverySinkControl,
};
use iprange_livedb::validation::ValidationBudget;
use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, StructureKind, ValueKind, ValueTag,
};

fn directory() -> PathBuf {
    let directory = std::env::temp_dir().join(format!(
        "iprange-v4-recovery-follows-{}",
        std::process::id()
    ));
    let _ = fs::remove_dir_all(&directory);
    fs::create_dir_all(&directory).unwrap();
    directory
}

fn create_source(main: &PathBuf, creator_only: bool) {
    create_live(
        main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        StructureKind::None,
        ValueTag::new(b"asn").unwrap(),
        2,
        &CancellationToken::new(),
        creator_only,
    )
    .unwrap();
}

fn recover(directory: &PathBuf, source: &PathBuf, label: &str) -> PathBuf {
    let inspection = inspect_recovery_candidates(
        source,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(1 << 20, 8),
        &CancellationToken::new(),
    )
    .unwrap();
    let candidate = inspection.candidate(0).unwrap().clone();
    let destination = directory.join(format!("{label}.v4"));
    let result = recover_live(
        source,
        candidate,
        &destination,
        &RecoveryBudget::heap_only(16 << 20, 100_000, 4),
        &mut |_envelope: &iprange_livedb::recovery::RecoveryUnknownEnvelope| {
            Ok(RecoverySinkControl::Continue)
        },
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(
        result.publication.publication,
        iprange_livedb::publication::PublicationStatus::Published
    );
    destination
}

#[test]
fn recovery_follows_the_source_sidecar_end_to_end() {
    let directory = directory();
    std::env::remove_var("IPRANGE_CREATOR_ONLY");

    let protected = directory.join("protected.v4");
    create_source(&protected, true);
    let destination = recover(&directory, &protected, "protected-out");
    let mode = fs::metadata(&destination).unwrap().permissions().mode() & 0o777;
    assert_eq!(
        mode,
        0o600,
        "protected source recovered to mode {mode:o}, want 0600 end to end"
    );

    let plain = directory.join("plain.v4");
    create_source(&plain, false);
    let destination = recover(&directory, &plain, "plain-out");
    let mode = fs::metadata(&destination).unwrap().permissions().mode() & 0o777;
    assert_ne!(mode, 0o600, "unprotected source forced mode 0600");

    let _ = fs::remove_dir_all(&directory);
}
