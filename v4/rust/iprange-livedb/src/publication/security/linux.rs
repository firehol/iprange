//! Linux inherited-access-ACL removal and proof.

use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;

use crate::publication::namespace::NamespaceError;

const ACCESS_ACL: &[u8] = b"system.posix_acl_access\0";

pub(super) fn remove_inherited(file: &File) -> Result<(), NamespaceError> {
    let result = unsafe { libc::fremovexattr(file.as_raw_fd(), ACCESS_ACL.as_ptr().cast()) };
    if result == 0 {
        return Ok(());
    }
    let source = io::Error::last_os_error();
    match source.raw_os_error() {
        Some(libc::ENODATA | libc::EOPNOTSUPP) => Ok(()),
        _ => Err(NamespaceError::IoAt {
            operation: "remove inherited access ACL",
            source,
        }),
    }
}

pub(super) fn require_trivial(file: &File) -> Result<(), NamespaceError> {
    let result = unsafe {
        libc::fgetxattr(
            file.as_raw_fd(),
            ACCESS_ACL.as_ptr().cast(),
            std::ptr::null_mut(),
            0,
        )
    };
    if result >= 0 {
        return Err(NamespaceError::AccessPolicy);
    }
    let source = io::Error::last_os_error();
    match source.raw_os_error() {
        Some(libc::ENODATA | libc::EOPNOTSUPP) => Ok(()),
        _ => Err(NamespaceError::IoAt {
            operation: "verify absent access ACL",
            source,
        }),
    }
}
