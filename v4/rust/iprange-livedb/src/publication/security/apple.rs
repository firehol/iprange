//! macOS inherited extended-ACL removal and proof.

use std::ffi::c_void;
use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;

use crate::publication::namespace::NamespaceError;

type FileSec = *mut c_void;
const FILESEC_ACL: libc::c_int = 5;
const REMOVE_ACL: *const c_void = 1usize as *const c_void;
const ACL_TYPE_EXTENDED: libc::c_int = 0x100;

extern "C" {
    fn filesec_init() -> FileSec;
    fn filesec_free(filesec: FileSec);
    fn filesec_set_property(
        filesec: FileSec,
        property: libc::c_int,
        value: *const c_void,
    ) -> libc::c_int;
    fn fchmodx_np(fd: libc::c_int, filesec: FileSec) -> libc::c_int;
    fn acl_get_fd_np(fd: libc::c_int, kind: libc::c_int) -> *mut c_void;
    fn acl_free(acl: *mut c_void) -> libc::c_int;
}

pub(super) fn remove_inherited(file: &File) -> Result<(), NamespaceError> {
    let filesec = unsafe { filesec_init() };
    if filesec.is_null() {
        return Err(last_error("allocate creator-only access policy"));
    }
    let property = unsafe { filesec_set_property(filesec, FILESEC_ACL, REMOVE_ACL) };
    let applied = if property == 0 {
        unsafe { fchmodx_np(file.as_raw_fd(), filesec) }
    } else {
        -1
    };
    unsafe { filesec_free(filesec) };
    if applied == 0 {
        Ok(())
    } else {
        Err(last_error("remove inherited access ACL"))
    }
}

pub(super) fn require_trivial(file: &File) -> Result<(), NamespaceError> {
    unsafe {
        *libc::__error() = 0;
    }
    let acl = unsafe { acl_get_fd_np(file.as_raw_fd(), ACL_TYPE_EXTENDED) };
    if !acl.is_null() {
        unsafe {
            acl_free(acl);
        }
        return Err(NamespaceError::AccessPolicy);
    }
    match io::Error::last_os_error().raw_os_error() {
        Some(libc::ENOENT) => Ok(()),
        Some(libc::EOPNOTSUPP) => Err(NamespaceError::Unsupported),
        _ => Err(last_error("verify absent access ACL")),
    }
}

fn last_error(operation: &'static str) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: io::Error::last_os_error(),
    }
}
