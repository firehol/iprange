//! FreeBSD inherited extended-ACL removal and proof.

use std::ffi::c_void;
use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;

use crate::publication::namespace::NamespaceError;

extern "C" {
    fn acl_get_fd(fd: libc::c_int) -> *mut c_void;
    fn acl_set_fd(fd: libc::c_int, acl: *mut c_void) -> libc::c_int;
    fn acl_strip_np(acl: *mut c_void, recalculate_mask: libc::c_int) -> *mut c_void;
    fn acl_is_trivial_np(acl: *mut c_void, trivial: *mut libc::c_int) -> libc::c_int;
    fn acl_free(acl: *mut c_void) -> libc::c_int;
}

pub(super) fn remove_inherited(file: &File) -> Result<(), NamespaceError> {
    let acl = get(file)?;
    let trivial = is_trivial(acl)?;
    if trivial {
        unsafe {
            acl_free(acl);
        }
        return Ok(());
    }
    let stripped = unsafe { acl_strip_np(acl, 1) };
    unsafe {
        acl_free(acl);
    }
    if stripped.is_null() {
        return Err(last_error("strip inherited access ACL"));
    }
    let result = unsafe { acl_set_fd(file.as_raw_fd(), stripped) };
    unsafe {
        acl_free(stripped);
    }
    if result == 0 {
        Ok(())
    } else {
        Err(last_error("apply stripped access ACL"))
    }
}

pub(super) fn require_trivial(file: &File) -> Result<(), NamespaceError> {
    let acl = get(file)?;
    let result = is_trivial(acl);
    unsafe {
        acl_free(acl);
    }
    match result? {
        true => Ok(()),
        false => Err(NamespaceError::AccessPolicy),
    }
}

fn get(file: &File) -> Result<*mut c_void, NamespaceError> {
    let acl = unsafe { acl_get_fd(file.as_raw_fd()) };
    if !acl.is_null() {
        return Ok(acl);
    }
    match io::Error::last_os_error().raw_os_error() {
        Some(libc::EOPNOTSUPP) => Err(NamespaceError::Unsupported),
        _ => Err(last_error("read access ACL")),
    }
}

fn is_trivial(acl: *mut c_void) -> Result<bool, NamespaceError> {
    let mut trivial = 0;
    if unsafe { acl_is_trivial_np(acl, &mut trivial) } == 0 {
        Ok(trivial != 0)
    } else {
        Err(last_error("verify access ACL"))
    }
}

fn last_error(operation: &'static str) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: io::Error::last_os_error(),
    }
}
