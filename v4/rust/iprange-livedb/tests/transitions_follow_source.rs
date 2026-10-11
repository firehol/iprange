//! The initialize/reset transitions follow the recorded source choice
//! (the milestone's own rule applied to its own transitions): resetting
//! an UNPROTECTED database records Unprotected, and the next live open
//! succeeds without a creator-only proof. The hardcoded-true defect
//! (the sol milestone gate's P1) recorded Protected over an
//! unprotected source and locked later live opens out.
#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, initialize_live, reset_live_coordination, AddressFamily,
    CancellationToken, LiveReader, LiveResetPolicy, LiveWriter,
    TransactionBudget, ValueKind, ValueTag,
};

// The strongest reset policy the platform supports (rollback-safe
// needs renameat2 exchange; Windows uses discard-previous). The Go
// twin: resetPolicy in lifecycle_follows_source_test.go.
#[cfg(target_os = "linux")]
fn reset_policy() -> LiveResetPolicy {
    LiveResetPolicy::RollbackSafe
}

#[cfg(not(target_os = "linux"))]
fn reset_policy() -> LiveResetPolicy {
    LiveResetPolicy::DiscardPrevious
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 1_000,
        max_file_growth_pages: 1_000,
        max_open_files: 2,
    }
}

// A RAII umask window (restored on drop; the test owns its thread).
#[cfg(unix)]
struct UmaskWindow(u32);
#[cfg(unix)]
impl UmaskWindow {
    fn new(mask: u32) -> Self {
        extern "C" {
            fn umask(mask: u32) -> u32;
        }
        UmaskWindow(unsafe { umask(mask) })
    }
}
#[cfg(unix)]
impl Drop for UmaskWindow {
    fn drop(&mut self) {
        extern "C" {
            fn umask(mask: u32) -> u32;
        }
        unsafe { umask(self.0) };
    }
}

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-follows-source-{label}-{}-{unique}",
                std::process::id()
            )),
        }
    }
}

impl Drop for TestPair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        let _ = fs::remove_file(self.main.with_file_name(name));
    }
}

fn create_unprotected(files: &TestPair) {
    // A fixed umask window so the mode assertions hold regardless of
    // the ambient umask (sol round-4 P2): 0666 & ~022 == 0644.
    #[cfg(unix)]
    let _umask = crate::UmaskWindow::new(0o022);
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        iprange_livedb::StructureKind::None,
        ValueTag::new(b"asn").unwrap(),
        3,
        &CancellationToken::new(),
        false,
    )
    .unwrap();
}

#[cfg(unix)]
fn mode_of(path: &std::path::Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    fs::metadata(path).unwrap().permissions().mode() & 0o777
}

#[test]
fn reset_of_unprotected_database_stays_openable() {
    // The reset transition rewrites coordination for an existing
    // UNPROTECTED live database; the replacement sidecar must record
    // Unprotected, and a later live open (which demands the proof only
    // for Protected sources) must succeed.
    let files = TestPair::new("reset-unprotected");
    create_unprotected(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new())
        .unwrap();
    writer.close().unwrap();

    let active = CancellationToken::new();
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
    reset_live_coordination(
        &files.main,
        3,
        LiveResetPolicy::RollbackSafe,
        &CancellationToken::new(),
    )
        .unwrap();

    // The very next open after the maintenance operation.
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
    let mut writer =
        LiveWriter::open(&files.main, budget(), &active).unwrap();
    writer.close().unwrap();
}

#[test]
fn initialize_of_unprotected_immutable_source_stays_openable() {
    let files = TestPair::new("initialize-unprotected");
    create_unprotected(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new())
        .unwrap();
    writer.close().unwrap();
    // The immutable shape (the sidecar absent — the transition's own
    // fixture pattern), then the initialize transition: the result
    // must open live without a creator-only proof.
    {
        let mut name = files.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        let _ = fs::remove_file(files.main.with_file_name(name));
    }
    initialize_live(&files.main, 3, &CancellationToken::new()).unwrap();

    let active = CancellationToken::new();
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
    let mut writer =
        LiveWriter::open(&files.main, budget(), &active).unwrap();
    writer.close().unwrap();
}

#[cfg(unix)]
#[test]
fn unprotected_modes_survive_the_transitions() {
    // The process-switch fallback shape: an unprotected source under
    // umask 022 keeps group/other read through both transitions.
    let files = TestPair::new("modes-survive");
    create_unprotected(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new())
        .unwrap();
    writer.close().unwrap();

    #[cfg(unix)]
    let _umask = UmaskWindow::new(0o022);
    reset_live_coordination(
        &files.main,
        3,
        reset_policy(),
        &CancellationToken::new(),
    )
        .unwrap();
    let mode = mode_of(&files.main);
    assert_eq!(
        mode, 0o644,
        "the reset must not flip an unprotected main to a protected mode"
    );

    {
        let mut name = files.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        let _ = fs::remove_file(files.main.with_file_name(name));
    }
    initialize_live(&files.main, 3, &CancellationToken::new()).unwrap();
    let mode = mode_of(&files.main);
    assert_eq!(
        mode, 0o644,
        "the initialize must not flip an unprotected main either"
    );
}

#[test]
fn switch_on_cannot_lock_out_an_unprotected_main() {
    // The sol gate's round-2 P1: with IPRANGE_CREATOR_ONLY=1 and a
    // missing sidecar, the transition must NOT record Protected over
    // an unprotected main (the next open would demand a proof the
    // 0644 file cannot satisfy). The compatible fallback classifies
    // from the main's own state.
    let files = TestPair::new("switch-on-unprotected");
    create_unprotected(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new())
        .unwrap();
    writer.close().unwrap();
    {
        let mut name = files.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        let _ = fs::remove_file(files.main.with_file_name(name));
    }
    std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
    let result = initialize_live(&files.main, 3, &CancellationToken::new()).unwrap();
    std::env::remove_var("IPRANGE_CREATOR_ONLY");
    assert_eq!(result.status, iprange_livedb::LiveTransitionStatus::Initialized);

    let active = CancellationToken::new();
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
}

#[cfg(unix)]
#[test]
fn switch_on_preserves_an_already_protected_main() {
    // A main that already satisfies the protected contract (0600,
    // single link) records Protected even with the switch off —
    // and stays openable.
    let files = TestPair::new("switch-on-protected");
    create_unprotected(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new())
        .unwrap();
    writer.close().unwrap();
    {
        use std::os::unix::fs::PermissionsExt;
        let mut perms = fs::metadata(&files.main).unwrap().permissions();
        perms.set_mode(0o600);
        fs::set_permissions(&files.main, perms).unwrap();
    }
    {
        let mut name = files.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        let _ = fs::remove_file(files.main.with_file_name(name));
    }
    std::env::remove_var("IPRANGE_CREATOR_ONLY");
    let result = initialize_live(&files.main, 3, &CancellationToken::new()).unwrap();
    assert_eq!(result.status, iprange_livedb::LiveTransitionStatus::Initialized);
    // The PRESERVATION assertion (sol round-4 P2): reopening alone
    // cannot detect a fallback that always records Unprotected — the
    // sidecar itself must record Protected for a protected main.
    assert_eq!(
        iprange_livedb::source_creator_only(&files.main),
        true,
        "a protected main must preserve the Protected policy through the transition"
    );

    let active = CancellationToken::new();
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
    let mut writer = LiveWriter::open(&files.main, budget(), &active).unwrap();
    writer.close().unwrap();
}

#[cfg(target_os = "linux")]
#[test]
fn acl_carrying_main_is_not_classified_protected() {
    // The sol gate's round-3 P1: mode 0600 with an extended access
    // ACL passes a mode+nlink classifier but fails the authoritative
    // proof; the transition must classify it Unprotected.
    let files = TestPair::new("acl-main");
    create_unprotected(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new())
        .unwrap();
    writer.close().unwrap();
    {
        use std::os::unix::fs::PermissionsExt;
        let mut perms = fs::metadata(&files.main).unwrap().permissions();
        perms.set_mode(0o600);
        fs::set_permissions(&files.main, perms).unwrap();
    }
    // A named-user ACL entry granting nothing: the mask stays empty
    // (the mode's group-class bits show the mask, so the mode stays
    // 0600), but the access ACL is EXTENDED — the exact shape a
    // mode-only classifier misses and the complete proof rejects.
    let status = std::process::Command::new("setfacl")
        .args(["-m", "u:65534:---", &files.main.to_string_lossy()])
        .status();
    match status {
        Ok(status) if status.success() => {}
        _ => {
            eprintln!("setfacl unavailable; skipping");
            return;
        }
    }
    // Assert the fixture: mode must still be 0600 (an extended ACL
    // the mode-only classifier would miss), and the proof must fail.
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = fs::metadata(&files.main).unwrap().permissions().mode() & 0o7777;
        assert_eq!(mode, 0o600, "the ACL fixture must keep mode 0600");
    }
    {
        let mut name = files.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        let _ = fs::remove_file(files.main.with_file_name(name));
    }
    std::env::remove_var("IPRANGE_CREATOR_ONLY");
    let result = initialize_live(&files.main, 3, &CancellationToken::new()).unwrap();
    assert_eq!(result.status, iprange_livedb::LiveTransitionStatus::Initialized);
    let active = CancellationToken::new();
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
}

#[cfg(unix)]
#[test]
fn special_bits_main_is_not_classified_protected() {
    // Mode 04600 (setuid): Rust metadata.mode() & 0o7777 != 0o600
    // must classify Unprotected — the proof would reject setuid.
    let files = TestPair::new("special-bits");
    create_unprotected(&files);
    let mut writer = LiveWriter::open(&files.main, budget(), &CancellationToken::new())
        .unwrap();
    writer.close().unwrap();
    {
        use std::os::unix::fs::PermissionsExt;
        let mut perms = fs::metadata(&files.main).unwrap().permissions();
        perms.set_mode(0o4600);
        fs::set_permissions(&files.main, perms).unwrap();
    }
    {
        let mut name = files.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        let _ = fs::remove_file(files.main.with_file_name(name));
    }
    let result = initialize_live(&files.main, 3, &CancellationToken::new()).unwrap();
    assert_eq!(result.status, iprange_livedb::LiveTransitionStatus::Initialized);
    let active = CancellationToken::new();
    let mut reader = LiveReader::open(&files.main, &active).unwrap();
    reader.close().unwrap();
}
