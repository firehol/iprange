//! Recovery follows the source sidecar's recorded choice end to end:
//! a protected source recovers to a 0600 output through the worker
//! (the parent creates the attempt under the source policy, and the
//! worker resumes it under the policy the attempt facts record — the
//! arm the round-1 fix restored), and an unprotected source does not
//! force 0600.

#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
#[cfg(unix)]
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

fn directory(label: &str) -> PathBuf {
    let directory = std::env::temp_dir().join(format!(
        "iprange-v4-recovery-follows-{label}-{}",
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

/// Serializes this binary's tests around the process switch: both
/// tests mutate or assert IPRANGE_CREATOR_ONLY, and the binary runs
/// its tests on parallel threads.
static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

#[test]
fn recovery_follows_the_source_sidecar_end_to_end() {
    let _lock = ENV_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
    std::env::remove_var("IPRANGE_CREATOR_ONLY");
    #[cfg(unix)]
    let mask = unsafe { test_umask(0) };
    let directory = directory("switch-off");

    let protected = directory.join("protected.v4");
    create_source(&protected, true);
    let destination = recover(&directory, &protected, "protected-out");
    let _ = &destination;
    #[cfg(unix)]
    {
        let mode = fs::metadata(&destination).unwrap().permissions().mode() & 0o777;
        assert_eq!(
            mode,
            0o600,
            "protected source recovered to mode {mode:o}, want 0600 end to end"
        );
    }

    let plain = directory.join("plain.v4");
    create_source(&plain, false);
    let destination = recover(&directory, &plain, "plain-out");
    let _ = &destination;
    #[cfg(unix)]
    {
        let mode = fs::metadata(&destination).unwrap().permissions().mode() & 0o777;
        assert_ne!(mode, 0o600, "unprotected source forced mode 0600");
    }

    // The unprotected exact-default arm (parity round 7): spec 15.6
    // says an unprotected artifact keeps the umask default, and every
    // earlier pin was a != 0600 negation — a hard-coded 0644 passed the
    // whole suite while the true default under umask 027 is 0640. This
    // arm pins the exact mode; the owner-bit floor keeps 0640 (owner
    // bits present) at 0640.
    #[cfg(unix)]
    {
        let plain = directory.join("plain.v4");
        let inspection = inspect_recovery_candidates(
            &plain,
            RecoveryInspectionMode::Live,
            &ValidationBudget::heap_only(1 << 20, 8),
            &CancellationToken::new(),
        )
        .unwrap();
        let candidate = inspection.candidate(0).unwrap().clone();
        let exact_mask = unsafe { test_umask(0o027) };
        let exact = directory.join("exact-out.v4");
        let result = recover_live(
            &plain,
            candidate,
            &exact,
            &RecoveryBudget::heap_only(16 << 20, 100_000, 4),
            &mut |_envelope: &iprange_livedb::recovery::RecoveryUnknownEnvelope| {
                Ok(RecoverySinkControl::Continue)
            },
            &CancellationToken::new(),
        )
        .unwrap();
        unsafe { test_umask(exact_mask) };
        let mode = fs::metadata(&exact).unwrap().permissions().mode() & 0o777;
        assert_eq!(
            mode, 0o640,
            "unprotected recovery mode is {mode:o}, want the exact umask default 0640 (a hard-coded 0644 would fail this arm)"
        );
        assert_eq!(
            result.publication.publication,
            iprange_livedb::publication::PublicationStatus::Published
        );
    }
    #[cfg(unix)]
    unsafe { test_umask(mask) };

    let _ = fs::remove_dir_all(&directory);
    match previous {
        Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
        None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
    }
}

// The switch-ON arm: an unprotected source recovered with the process
// switch on must still bind the recorded choice (unprotected) — the
// facts the attempt records win over the worker's process switch.
// Under umask 0 a switch-following worker would create 0600 where the
// recorded facts keep the 0666 default. Go twin:
// TestRecoveryFollowsSourceWithSwitchOn.
#[test]
fn recovery_follows_the_source_with_the_switch_on() {
    let _lock = ENV_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
    std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
    #[cfg(unix)]
    let mask = unsafe { test_umask(0) };
    let directory = directory("switch-on");

    let plain = directory.join("plain.v4");
    create_source(&plain, false);
    let destination = recover(&directory, &plain, "plain-out");
    let _ = &destination;
    // recover() asserted Published; the mode discrimination is POSIX.
    #[cfg(unix)]
    {
        let mode = fs::metadata(&destination).unwrap().permissions().mode() & 0o777;
        assert_ne!(
            mode,
            0o600,
            "switch-on worker forced 0600 on an unprotected source: the recorded facts must win"
        );
    }

    #[cfg(unix)]
    unsafe { test_umask(mask) };
    let _ = fs::remove_dir_all(&directory);
    match previous {
        Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
        None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
    }
}

#[cfg(unix)]
unsafe fn test_umask(mask: u32) -> u32 {
    extern "C" {
        fn umask(mask: u32) -> u32;
    }
    unsafe { umask(mask) }
}

// The worker path survives a hostile umask (parity round 5): the
// control file is re-opened O_RDWR by the worker, so a umask stripping
// owner bits at create used to fail the re-open EACCES and surface as
// a misleading "SDK worker version or protocol does not match"
// Conflict. The unprotected create floors the owner read/write bits
// after create; recovery under umask 0200 must therefore publish.
// Inspection runs under the normal umask (its validation scratch is
// the separately dispositioned residue class); the hostile window
// wraps only the worker-coordinated recover. Go twin:
// TestRecoverySurvivesAHostileUmask.
#[test]
fn recovery_survives_a_hostile_umask() {
    let _lock = ENV_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
    std::env::remove_var("IPRANGE_CREATOR_ONLY");
    let directory = directory("hostile-umask");

    let plain = directory.join("plain.v4");
    create_source(&plain, false);
    // Inspection runs under the normal umask (its validation scratch is
    // the separately dispositioned residue class); the hostile window
    // wraps only the worker-coordinated recover_live.
    let inspection = inspect_recovery_candidates(
        &plain,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(1 << 20, 8),
        &CancellationToken::new(),
    )
    .unwrap();
    let candidate = inspection.candidate(0).unwrap().clone();
    let destination = directory.join("plain-out.v4");
    #[cfg(unix)]
    let mask = unsafe { test_umask(0o200) };
    let recovered = recover_live(
        &plain,
        candidate,
        &destination,
        &RecoveryBudget::heap_only(16 << 20, 100_000, 4),
        &mut |_envelope: &iprange_livedb::recovery::RecoveryUnknownEnvelope| {
            Ok(RecoverySinkControl::Continue)
        },
        &CancellationToken::new(),
    );
    // Restore the umask and the environment BEFORE any assertion can
    // panic, so a failure cannot leak the hostile window into whichever
    // test runs next (the Go twin restores first for the same reason).
    #[cfg(unix)]
    unsafe { test_umask(mask) };
    let _ = fs::remove_dir_all(&directory);
    match previous {
        Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
        None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
    }
    let result = recovered.unwrap();
    assert_eq!(
        result.publication.publication,
        iprange_livedb::publication::PublicationStatus::Published,
        "worker-coordinated recovery under a hostile umask must publish (the pre-fix shape failed the worker handshake with a misleading Conflict)"
    );
}
