use std::ffi::OsString;
use std::os::fd::AsRawFd;
use std::os::unix::ffi::OsStringExt;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;

#[test]
fn binding_uses_raw_posix_bytes_and_exact_attempt_names() {
    let directory = TempDirectory::new();
    let name = OsString::from_vec(vec![b'f', 0x80]);
    let destination = Destination::bind(&directory.path.join(&name)).unwrap();
    assert_eq!(destination.main().bytes(), &[b'f', 0x80]);
    assert_eq!(
        destination.basename_commitment(),
        basename_commitment(BasenameEncoding::PosixBytes, &[b'f', 0x80]).unwrap()
    );
    assert_eq!(
        destination.output_name([0xab; 16]).unwrap().bytes(),
        b".iprange-publish-abababababababababababababababab.tmp"
    );
    assert_eq!(
        destination.reservation_name([0xcd; 16]).unwrap().bytes(),
        b".iprange-reservation-cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd.tmp"
    );
}

#[test]
fn private_creation_is_exclusive_nofollow_and_creator_only() {
    let directory = TempDirectory::new();
    let destination = Destination::bind(&directory.path.join("output.v4")).unwrap();
    let name = destination.output_name([1; 16]).unwrap();
    let file = destination.directory().create(&name).unwrap();
    let regular = Regular {
        identity: regular_identity(&file, destination.directory().identity().device).unwrap(),
        file,
    };
    regular
        .file
        .set_permissions(std::fs::Permissions::from_mode(0o644))
        .unwrap();
    assert!(matches!(
        regular.creator_only_commitment(),
        Err(NamespaceError::AccessPolicy)
    ));
    destination.secure_created(&regular).unwrap();
    assert_eq!(
        regular.creator_only_commitment().unwrap(),
        destination.security_commitment()
    );
    let metadata = regular.file.metadata().unwrap();
    assert_eq!(metadata.permissions().mode() & 0o7777, 0o600);
    assert_eq!(metadata.uid(), unsafe { libc::geteuid() });
    assert!(matches!(
        destination.directory().create(&name),
        Err(NamespaceError::Exists)
    ));
}

#[test]
fn inherited_extended_access_acl_is_removed() {
    let directory = TempDirectory::new();
    let destination = Destination::bind(&directory.path.join("output.v4")).unwrap();
    let name = destination.output_name([9; 16]).unwrap();
    let file = destination.directory().create(&name).unwrap();
    if !install_extended_acl(&file) {
        return;
    }
    let regular = Regular {
        identity: regular_identity(&file, destination.directory().identity().device).unwrap(),
        file,
    };
    assert!(matches!(
        regular.creator_only_commitment(),
        Err(NamespaceError::AccessPolicy)
    ));
    destination.secure_created(&regular).unwrap();
    assert_eq!(
        regular.creator_only_commitment().unwrap(),
        destination.security_commitment()
    );
}

#[test]
fn no_replace_never_overwrites_and_exact_unlink_checks_identity() {
    let directory = TempDirectory::new();
    let destination = Destination::bind(&directory.path.join("output.v4")).unwrap();
    let first_name = destination.output_name([2; 16]).unwrap();
    let second_name = destination.output_name([3; 16]).unwrap();
    let first = destination.directory().create(&first_name).unwrap();
    let first_identity =
        regular_identity(&first, destination.directory().identity().device).unwrap();
    let second = destination.directory().create(&second_name).unwrap();
    let second_identity =
        regular_identity(&second, destination.directory().identity().device).unwrap();

    assert!(matches!(
        destination
            .directory()
            .rename_noreplace(&first_name, &second_name),
        Err(NamespaceError::Exists)
    ));
    destination
        .directory()
        .verify_name(&first_name, first_identity)
        .unwrap();
    destination
        .directory()
        .verify_name(&second_name, second_identity)
        .unwrap();
    assert!(matches!(
        destination
            .directory()
            .unlink_exact(&first_name, second_identity),
        Err(NamespaceError::IdentityChanged)
    ));
    assert!(destination
        .directory()
        .unlink_exact(&first_name, first_identity)
        .unwrap());
    assert!(!destination
        .directory()
        .unlink_exact(&first_name, first_identity)
        .unwrap());
}

#[test]
fn symlinks_and_hard_links_fail_closed() {
    use std::os::unix::fs::symlink;

    let directory = TempDirectory::new();
    let destination = Destination::bind(&directory.path.join("output.v4")).unwrap();
    std::fs::write(directory.path.join("target"), b"x").unwrap();
    symlink("target", directory.path.join("link")).unwrap();
    let link = Name::new(b"link").unwrap();
    assert!(destination.directory().open_regular(&link, false).is_err());

    let source = Name::new(b"target").unwrap();
    std::fs::hard_link(
        directory.path.join("target"),
        directory.path.join("second-link"),
    )
    .unwrap();
    assert!(matches!(
        destination.directory().open_regular(&source, false),
        Err(NamespaceError::LinkCount(2))
    ));
}

#[test]
fn reserved_main_names_are_rejected_before_creation() {
    let directory = TempDirectory::new();
    for name in [".IpRaNgE-private", "feed.READERS"] {
        assert!(matches!(
            Destination::bind(&directory.path.join(name)),
            Err(NamespaceError::InvalidName)
        ));
    }
    assert!(directory.path.read_dir().unwrap().next().is_none());
}

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-publication-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.path).unwrap();
    }
}

fn install_extended_acl(file: &File) -> bool {
    const ACL_NAME: &[u8] = b"system.posix_acl_access\0";
    const USER_OBJ: u16 = 0x01;
    const USER: u16 = 0x02;
    const GROUP_OBJ: u16 = 0x04;
    const MASK: u16 = 0x10;
    const OTHER: u16 = 0x20;
    let mut bytes = Vec::with_capacity(44);
    bytes.extend_from_slice(&2u32.to_le_bytes());
    for (tag, permissions, id) in [
        (USER_OBJ, 6u16, u32::MAX),
        (USER, 4u16, unsafe { libc::geteuid() }.wrapping_add(1)),
        (GROUP_OBJ, 0u16, u32::MAX),
        (MASK, 4u16, u32::MAX),
        (OTHER, 0u16, u32::MAX),
    ] {
        bytes.extend_from_slice(&tag.to_le_bytes());
        bytes.extend_from_slice(&permissions.to_le_bytes());
        bytes.extend_from_slice(&id.to_le_bytes());
    }
    let result = unsafe {
        libc::fsetxattr(
            file.as_raw_fd(),
            ACL_NAME.as_ptr().cast(),
            bytes.as_ptr().cast(),
            bytes.len(),
            0,
        )
    };
    if result == 0 {
        return true;
    }
    let error = std::io::Error::last_os_error();
    if matches!(error.raw_os_error(), Some(libc::EOPNOTSUPP | libc::ENOSYS)) {
        return false;
    }
    panic!("failed to install test ACL: {error}");
}
