//! Atomic mutation of names in one retained publication directory.

use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;

use super::{errno, Directory, Identity, Name, NamespaceError};

impl Directory {
    pub(crate) fn rename_noreplace(
        &self,
        source: &Name,
        _source_file: &File,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.check_creator()?;
        #[cfg(target_os = "freebsd")]
        {
            return self.link_noreplace(source, destination);
        }
        #[cfg(any(target_os = "linux", target_vendor = "apple"))]
        {
            #[cfg(target_os = "linux")]
            let result = unsafe {
                libc::renameat2(
                    self.file.as_raw_fd(),
                    source.c_str().as_ptr(),
                    self.file.as_raw_fd(),
                    destination.c_str().as_ptr(),
                    libc::RENAME_NOREPLACE,
                )
            };
            #[cfg(target_vendor = "apple")]
            let result = unsafe {
                libc::renameatx_np(
                    self.file.as_raw_fd(),
                    source.c_str().as_ptr(),
                    self.file.as_raw_fd(),
                    destination.c_str().as_ptr(),
                    libc::RENAME_EXCL,
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
        #[cfg(not(any(target_os = "linux", target_vendor = "apple", target_os = "freebsd")))]
        Err(NamespaceError::Unsupported)
    }

    pub(crate) fn replace(
        &self,
        source: &Name,
        _source_file: &File,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.check_creator()?;
        #[cfg(any(target_os = "linux", target_vendor = "apple", target_os = "freebsd"))]
        {
            #[cfg(target_os = "linux")]
            let result = unsafe {
                libc::renameat2(
                    self.file.as_raw_fd(),
                    source.c_str().as_ptr(),
                    self.file.as_raw_fd(),
                    destination.c_str().as_ptr(),
                    libc::RENAME_EXCHANGE,
                )
            };
            #[cfg(target_vendor = "apple")]
            let result = unsafe {
                libc::renameatx_np(
                    self.file.as_raw_fd(),
                    source.c_str().as_ptr(),
                    self.file.as_raw_fd(),
                    destination.c_str().as_ptr(),
                    libc::RENAME_SWAP,
                )
            };
            #[cfg(target_os = "freebsd")]
            let result = unsafe {
                libc::renameat(
                    self.file.as_raw_fd(),
                    source.c_str().as_ptr(),
                    self.file.as_raw_fd(),
                    destination.c_str().as_ptr(),
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
                    operation: "atomically replace publication name",
                    source,
                }),
            }
        }
        #[cfg(not(any(target_os = "linux", target_vendor = "apple", target_os = "freebsd")))]
        Err(NamespaceError::Unsupported)
    }

    #[cfg(target_os = "freebsd")]
    fn link_noreplace(&self, source: &Name, destination: &Name) -> Result<(), NamespaceError> {
        let result = unsafe {
            libc::linkat(
                self.file.as_raw_fd(),
                source.c_str().as_ptr(),
                self.file.as_raw_fd(),
                destination.c_str().as_ptr(),
                0,
            )
        };
        if result != 0 {
            let source = io::Error::last_os_error();
            return match source.raw_os_error() {
                Some(libc::EEXIST) => Err(NamespaceError::Exists),
                _ => Err(NamespaceError::IoAt {
                    operation: "link publication name without replacement",
                    source,
                }),
            };
        }
        self.sync()?;
        let result = unsafe { libc::unlinkat(self.file.as_raw_fd(), source.c_str().as_ptr(), 0) };
        if result != 0 {
            return Err(errno("unlink private publication alias"));
        }
        self.sync()?;
        Ok(())
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
