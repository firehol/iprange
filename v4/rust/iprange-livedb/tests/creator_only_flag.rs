//! The create flag is recorded in the sidecar and honored after close.

#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

#[cfg(unix)]
use std::fs;
#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;
#[cfg(unix)]
use std::path::PathBuf;
#[cfg(unix)]
use std::time::{SystemTime, UNIX_EPOCH};

#[cfg(unix)]
use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, CreationState, LiveReader, StructureKind,
    ValueKind, ValueTag,
};

#[cfg(unix)]
unsafe fn libc_umask(mask: u32) -> u32 {
    extern "C" {
        fn umask(mask: u32) -> u32;
    }
    unsafe { umask(mask) }
}

/// Serializes this binary's tests: they run on parallel threads, and the
/// switch tests below mutate or assert the process-wide environment, so
/// an unserialized run could flip the switch under another test's create.
static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

#[cfg(unix)]
fn path(label: &str) -> PathBuf {
    let unique = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    std::env::temp_dir().join(format!(
        "iprange-v4-creator-only-{label}-{}-{unique}",
        std::process::id()
    ))
}

#[test]
fn creator_only_switch_defaults_off() {
    let _lock = ENV_LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
    std::env::remove_var("IPRANGE_CREATOR_ONLY");
    assert!(!iprange_livedb::creator_only_requested());
    std::env::set_var("IPRANGE_CREATOR_ONLY", "0");
    assert!(!iprange_livedb::creator_only_requested());
    std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
    assert!(iprange_livedb::creator_only_requested());
    match previous {
        Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
        None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
    }
}

#[test]
#[cfg(unix)]
fn snapshot_follows_an_unprotected_database() {
    let _lock = ENV_LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    let directory = path("snapshot");
    fs::create_dir(&directory).unwrap();
    let source = directory.join("plain.iprdb");
    let previous = unsafe { libc_umask(0) };
    create(&source, false);
    unsafe { libc_umask(previous) };
    let destination = directory.join("snap.iprdb");
    let result = iprange_livedb::snapshot_to(
        &source,
        iprange_livedb::SnapshotSourceMode::Live,
        &destination,
        iprange_livedb::SnapshotPublicationPolicy::FailIfExists,
        &iprange_livedb::SnapshotBudget::new(16 * 1024 * 1024, 100_000, 4),
        &CancellationToken::new(),
    )
    .unwrap();
    assert_eq!(
        result.publication.publication,
        iprange_livedb::publication::PublicationStatus::Published
    );
    let mode = fs::metadata(&destination).unwrap().permissions().mode() & 0o777;
    assert_ne!(mode, 0o600, "unprotected snapshot forced mode 0600");
    let _ = fs::remove_dir_all(&directory);
}

#[cfg(unix)]
fn create(main: &PathBuf, creator_only: bool) {
    let result = create_live(
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
    assert_eq!(result.state, CreationState::Created);
}

#[test]
#[cfg(unix)]
fn protected_create_is_checked_after_close() {
    let _lock = ENV_LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    let main = path("protected");
    create(&main, true);
    assert_eq!(fs::metadata(&main).unwrap().permissions().mode() & 0o777, 0o600);
    LiveReader::open(&main, &CancellationToken::new()).unwrap();

    let mut permissions = fs::metadata(&main).unwrap().permissions();
    permissions.set_mode(0o644);
    fs::set_permissions(&main, permissions).unwrap();
    assert!(LiveReader::open(&main, &CancellationToken::new()).is_err());
    let _ = fs::remove_file(&main);
    let _ = fs::remove_file(main.with_extension("v4.readers"));
}

#[test]
#[cfg(unix)]
fn unprotected_umask_0600_is_not_checked_after_close() {
    let _lock = ENV_LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    let main = path("plain");
    let old = unsafe { libc_umask(0) };
    create(&main, false);
    unsafe { libc_umask(old) };
    // The unprotected exact-default arm (parity round 9): the LIVE MAIN
    // file's umask default is pinned exactly — 0666 under umask 0 —
    // because every earlier pin was a != 0600 negation and a hard-coded
    // 0644 at the destination creator passed the entire tree while the
    // spec 15.6 contract says the process default. The readers sidecar
    // is pinned with it.
    let mode = fs::metadata(&main).unwrap().permissions().mode() & 0o777;
    assert_eq!(
        mode, 0o666,
        "unprotected main mode is {mode:o}, want the exact umask default 0666 (a hard-coded 0644 would fail this arm)"
    );
    let sidecar = iprange_livedb::sidecar_path(&main).unwrap();
    let sidecar_mode = fs::metadata(&sidecar).unwrap().permissions().mode() & 0o777;
    assert_eq!(
        sidecar_mode, 0o666,
        "unprotected readers mode is {sidecar_mode:o}, want the exact umask default 0666"
    );
    LiveReader::open(&main, &CancellationToken::new())
        .expect("umask 0600 must not make an unprotected file fail open");
    let _ = fs::remove_file(&main);
    let _ = fs::remove_file(&sidecar);
}
