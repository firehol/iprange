//! Atomic mutation of names in one retained publication directory.

use std::io;
use std::os::fd::AsRawFd;

use super::{errno, Directory, Identity, Name, NamespaceError};

impl Directory {
    pub(crate) fn rename_noreplace(
        &self,
        source: &Name,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.check_creator()?;
        let result = unsafe {
            libc::renameat2(
                self.file.as_raw_fd(),
                source.c_str().as_ptr(),
                self.file.as_raw_fd(),
                destination.c_str().as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        };
        if result == 0 {
            return Ok(());
        }
        let source = io::Error::last_os_error();
        match source.raw_os_error() {
            Some(libc::EEXIST) => Err(NamespaceError::Exists),
            Some(libc::ENOSYS | libc::EINVAL | libc::EOPNOTSUPP) => {
                Err(NamespaceError::Unsupported)
            }
            _ => Err(NamespaceError::IoAt {
                operation: "publish name without replacement",
                source,
            }),
        }
    }

    pub(crate) fn rename_exchange(&self, left: &Name, right: &Name) -> Result<(), NamespaceError> {
        self.check_creator()?;
        let result = unsafe {
            libc::renameat2(
                self.file.as_raw_fd(),
                left.c_str().as_ptr(),
                self.file.as_raw_fd(),
                right.c_str().as_ptr(),
                libc::RENAME_EXCHANGE,
            )
        };
        if result == 0 {
            return Ok(());
        }
        let source = io::Error::last_os_error();
        match source.raw_os_error() {
            Some(libc::ENOENT) => Err(NamespaceError::Missing),
            Some(libc::ENOSYS | libc::EINVAL | libc::EOPNOTSUPP) => {
                Err(NamespaceError::Unsupported)
            }
            _ => Err(NamespaceError::IoAt {
                operation: "atomically exchange publication names",
                source,
            }),
        }
    }

    pub(crate) fn unlink_exact(
        &self,
        name: &Name,
        expected: Identity,
    ) -> Result<bool, NamespaceError> {
        let Some(found) = self.entry(name)? else {
            return Ok(false);
        };
        if !found.regular {
            return Err(NamespaceError::NotRegular);
        }
        if found.identity != expected {
            return Err(NamespaceError::IdentityChanged);
        }
        if found.links != 1 {
            return Err(NamespaceError::LinkCount(found.links));
        }
        let result = unsafe { libc::unlinkat(self.file.as_raw_fd(), name.c_str().as_ptr(), 0) };
        if result != 0 {
            return Err(errno("unlink exact file"));
        }
        Ok(true)
    }

    pub(crate) fn sync(&self) -> Result<(), NamespaceError> {
        self.check_creator()?;
        self.file.sync_all().map_err(|source| {
            if source.raw_os_error() == Some(libc::EINVAL) {
                NamespaceError::Unsupported
            } else {
                NamespaceError::IoAt {
                    operation: "synchronize retained directory",
                    source,
                }
            }
        })
    }
}
