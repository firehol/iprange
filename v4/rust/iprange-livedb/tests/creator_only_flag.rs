//! The create flag is recorded in the sidecar and honored after close.

#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, CreationState, LiveReader, StructureKind,
    ValueKind, ValueTag,
};

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
fn protected_create_is_checked_after_close() {
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
fn unprotected_umask_0600_is_not_checked_after_close() {
    let main = path("plain");
    let old = unsafe { libc::umask(0) };
    create(&main, false);
    unsafe { libc::umask(old) };
    let mode = fs::metadata(&main).unwrap().permissions().mode() & 0o777;
    assert_ne!(mode, 0o600, "unprotected create forced mode 0600");
    LiveReader::open(&main, &CancellationToken::new())
        .expect("umask 0600 must not make an unprotected file fail open");
    let _ = fs::remove_file(&main);
}
