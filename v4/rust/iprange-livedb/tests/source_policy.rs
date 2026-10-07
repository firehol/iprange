//! The recovery destination's source-policy read follows the sidecar
//! record (spec 15.6): a sidecar that records protected answers
//! protected, one that records unprotected answers unprotected, and a
//! path with no sidecar follows the process switch. The classifier
//! assertions run on every platform (Windows protection is the DACL,
//! not mode bits); the mode discrimination is POSIX-only, detected
//! under umask 0 where a switch-following default for a protected
//! source would produce a 0666 artifact, and the FIFO twin is a unix
//! concept outright.

use std::fs;
#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;
use std::path::Path;

use iprange_livedb::{
    create_live, source_creator_only, AddressFamily, CancellationToken, StructureKind, ValueKind,
    ValueTag,
};

/// Serializes this binary's tests around the process switch: both tests
/// mutate or assert IPRANGE_CREATOR_ONLY, and the binary runs its tests
/// on parallel threads (the crate's documented serialization rule).
static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

fn create(main: &Path, creator_only: bool) {
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
    assert_eq!(result.state, iprange_livedb::CreationState::Created);
}

#[cfg(unix)]
unsafe fn source_policy_umask(mask: u32) -> u32 {
    extern "C" {
        fn umask(mask: u32) -> u32;
    }
    unsafe { umask(mask) }
}

#[test]
fn source_policy_follows_the_sidecar_record() {
    let _lock = ENV_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
    // umask 0 explicitly (unix): the mode assertions below assume it,
    // and an ambient umask like 0077 would strip group/other bits from
    // the unprotected create and false-red the != 0600 assertion.
    #[cfg(unix)]
    let mask = unsafe { source_policy_umask(0) };
    let directory = std::env::temp_dir().join(format!(
        "iprange-v4-source-policy-{}",
        std::process::id()
    ));
    fs::create_dir_all(&directory).unwrap();

    let protected = directory.join("protected.iprdb");
    create(&protected, true);
    assert!(
        source_creator_only(&protected),
        "protected source answered unprotected"
    );
    #[cfg(unix)]
    {
        let mode = fs::metadata(&protected).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600, "protected create did not keep mode 0600");
    }

    let unprotected = directory.join("plain.iprdb");
    create(&unprotected, false);
    assert!(
        !source_creator_only(&unprotected),
        "unprotected source answered protected"
    );
    #[cfg(unix)]
    {
        let mode = fs::metadata(&unprotected).unwrap().permissions().mode() & 0o777;
        assert_ne!(mode, 0o600, "unprotected create forced mode 0600");
    }

    // No sidecar (every immutable source): the process switch decides.
    let absent = directory.join("absent.iprdb");
    std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
    assert!(source_creator_only(&absent));
    std::env::set_var("IPRANGE_CREATOR_ONLY", "0");
    assert!(!source_creator_only(&absent));
    #[cfg(unix)]
    unsafe { source_policy_umask(mask) };
    match previous {
        Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
        None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
    }

    let _ = fs::remove_dir_all(&directory);
}

#[cfg(unix)]
#[test]
fn classifier_refuses_a_fifo_at_the_sidecar_name() {
    use std::os::unix::fs::FileTypeExt;

    let _lock = ENV_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");

    // A FIFO planted at the sidecar name must refuse (follow the
    // switch) instead of wedging the calling thread on a blocking
    // open. The call runs under a bounded watchdog so a prompt-open
    // regression fails this test in seconds instead of hanging the
    // binary (libtest has no per-test timeout).
    let directory = std::env::temp_dir().join(format!(
        "iprange-v4-fifo-sidecar-{}",
        std::process::id()
    ));
    let _ = fs::remove_dir_all(&directory);
    fs::create_dir_all(&directory).unwrap();
    let main = directory.join("fifo.iprdb");
    create(&main, true);
    let sidecar = iprange_livedb::sidecar_path(&main).unwrap();
    fs::remove_file(&sidecar).unwrap();
    let name = std::ffi::CString::new(sidecar.as_os_str().as_encoded_bytes().to_vec()).unwrap();
    let made = unsafe { libc_mkfifo(name.as_ptr(), 0o600) };
    assert_eq!(made, 0, "mkfifo at the sidecar path failed");
    let file_type = fs::metadata(&sidecar).unwrap().file_type();
    assert!(file_type.is_fifo(), "sidecar name is not a FIFO");

    std::env::set_var("IPRANGE_CREATOR_ONLY", "0");
    let (tx, rx) = std::sync::mpsc::channel();
    let classifier_main = main.clone();
    let worker = std::thread::spawn(move || {
        let answered = iprange_livedb::source_creator_only(&classifier_main);
        let _ = tx.send(answered);
    });
    let answered = match rx.recv_timeout(std::time::Duration::from_secs(10)) {
        Ok(answered) => answered,
        Err(_) => {
            panic!(
                "source_creator_only wedged on the FIFO sidecar: the \
                 prompt-open regression this detector pins"
            );
        }
    };
    assert!(
        !answered,
        "FIFO sidecar answered protected instead of following the switch"
    );
    let _ = worker.join();
    match previous {
        Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
        None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
    }
    let _ = fs::remove_dir_all(&directory);
}

#[cfg(unix)]
unsafe fn libc_mkfifo(path: *const std::os::raw::c_char, mode: u32) -> i32 {
    extern "C" {
        fn mkfifo(path: *const std::os::raw::c_char, mode: u32) -> i32;
    }
    unsafe { mkfifo(path, mode) }
}
