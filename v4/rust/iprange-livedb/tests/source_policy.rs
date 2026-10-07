//! The recovery destination's source-policy read follows the sidecar
//! record (spec 15.6): a sidecar that records protected answers
//! protected, one that records unprotected answers unprotected, and a
//! path with no sidecar follows the process switch. Detected under
//! umask 0, where a switch-following default for a protected source
//! would produce a 0666 artifact.

#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;

use iprange_livedb::{
    create_live, source_creator_only, AddressFamily, CancellationToken, StructureKind, ValueKind,
    ValueTag,
};

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

#[test]
fn source_policy_follows_the_sidecar_record() {
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
    let mode = fs::metadata(&protected).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode, 0o600, "protected create did not keep mode 0600");

    let unprotected = directory.join("plain.iprdb");
    create(&unprotected, false);
    assert!(
        !source_creator_only(&unprotected),
        "unprotected source answered protected"
    );
    let mode = fs::metadata(&unprotected).unwrap().permissions().mode() & 0o777;
    assert_ne!(mode, 0o600, "unprotected create forced mode 0600");

    // No sidecar (every immutable source): the process switch decides.
    let absent = directory.join("absent.iprdb");
    std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
    assert!(source_creator_only(&absent));
    std::env::set_var("IPRANGE_CREATOR_ONLY", "0");
    assert!(!source_creator_only(&absent));
    std::env::remove_var("IPRANGE_CREATOR_ONLY");

    let _ = fs::remove_dir_all(&directory);
}

#[test]
fn classifier_refuses_a_fifo_at_the_sidecar_name() {
    use std::os::unix::fs::FileTypeExt;

    // A FIFO planted at the sidecar name must refuse (follow the
    // switch) instead of wedging the calling thread on a blocking
    // open.
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
    let answered = iprange_livedb::source_creator_only(&main);
    assert!(
        !answered,
        "FIFO sidecar answered protected instead of following the switch"
    );
    std::env::remove_var("IPRANGE_CREATOR_ONLY");
    let _ = fs::remove_dir_all(&directory);
}

unsafe fn libc_mkfifo(path: *const std::os::raw::c_char, mode: u32) -> i32 {
    extern "C" {
        fn mkfifo(path: *const std::os::raw::c_char, mode: u32) -> i32;
    }
    unsafe { mkfifo(path, mode) }
}
