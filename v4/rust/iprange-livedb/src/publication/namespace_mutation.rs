//! Atomic mutation of names in one retained publication directory.

use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;
#[cfg(any(target_os = "freebsd", test))]
use std::os::fd::FromRawFd;

use super::{errno, Directory, Identity, Name, NamespaceError};
#[cfg(any(target_os = "freebsd", test))]
use super::{is_nofollow_symlink, regular_identity_any_link, Entry, Regular};

#[cfg(any(target_os = "freebsd", test))]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum LinkState {
    SourceOnly,
    Linked,
    Complete,
}

impl Directory {
    pub(crate) fn rename_noreplace(
        &self,
        source: &Name,
        source_file: &File,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.check_creator()?;
        #[cfg(target_os = "freebsd")]
        {
            self.link_noreplace(source, source_file, destination)
        }
        #[cfg(not(target_os = "freebsd"))]
        let _ = source_file;
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

    pub(crate) fn exchange(
        &self,
        _source: &Name,
        _source_file: &File,
        _destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.check_creator()?;
        #[cfg(any(target_os = "linux", target_vendor = "apple"))]
        {
            #[cfg(target_os = "linux")]
            let result = unsafe {
                libc::renameat2(
                    self.file.as_raw_fd(),
                    _source.c_str().as_ptr(),
                    self.file.as_raw_fd(),
                    _destination.c_str().as_ptr(),
                    libc::RENAME_EXCHANGE,
                )
            };
            #[cfg(target_vendor = "apple")]
            let result = unsafe {
                libc::renameatx_np(
                    self.file.as_raw_fd(),
                    _source.c_str().as_ptr(),
                    self.file.as_raw_fd(),
                    _destination.c_str().as_ptr(),
                    libc::RENAME_SWAP,
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
        #[cfg(not(any(target_os = "linux", target_vendor = "apple")))]
        Err(NamespaceError::Unsupported)
    }

    pub(crate) fn replace_discarding_destination(
        &self,
        source: &Name,
        _source_file: &File,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.check_creator()?;
        #[cfg(any(target_os = "linux", target_vendor = "apple", target_os = "freebsd"))]
        {
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
                    operation: "atomically replace and discard publication destination",
                    source,
                }),
            }
        }
        #[cfg(not(any(target_os = "linux", target_vendor = "apple", target_os = "freebsd")))]
        Err(NamespaceError::Unsupported)
    }

    #[cfg(target_os = "freebsd")]
    fn link_noreplace(
        &self,
        source: &Name,
        source_file: &File,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        let expected = regular_identity_any_link(source_file, self.identity)?;
        self.require_source(source, expected)?;
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
            let error = io::Error::last_os_error();
            return match error.raw_os_error() {
                Some(libc::EEXIST)
                    if matches!(
                        self.link_state(source, destination, expected),
                        Ok(LinkState::Linked)
                    ) =>
                {
                    self.finish_noreplace_transition(source, destination, expected)
                }
                Some(libc::EEXIST) => Err(NamespaceError::Exists),
                _ => Err(NamespaceError::IoAt {
                    operation: "link publication name without replacement",
                    source: error,
                }),
            };
        }
        crate::fault::crash("publication.freebsd.after_noreplace_link");
        self.finish_noreplace_transition(source, destination, expected)
    }

    #[cfg(any(target_os = "freebsd", test))]
    pub(crate) fn finish_noreplace_transition(
        &self,
        source: &Name,
        destination: &Name,
        expected: Identity,
    ) -> Result<(), NamespaceError> {
        match self.link_state(source, destination, expected)? {
            LinkState::SourceOnly => return Err(NamespaceError::Missing),
            LinkState::Complete => return self.prove_link_complete(source, destination, expected),
            LinkState::Linked => {}
        }
        self.sync()?;
        crate::fault::crash("publication.freebsd.after_noreplace_link_sync");
        self.unlink_link_alias(source, destination, expected)?;
        crate::fault::crash("publication.freebsd.after_noreplace_alias_unlink");
        self.sync()?;
        crate::fault::crash("publication.freebsd.after_noreplace_alias_sync");
        self.prove_link_complete(source, destination, expected)
    }

    #[cfg(any(target_os = "freebsd", test))]
    pub(crate) fn open_regular_any_link(
        &self,
        name: &Name,
        writable: bool,
    ) -> Result<Option<Regular>, NamespaceError> {
        self.check_creator()?;
        let access = if writable {
            libc::O_RDWR
        } else {
            libc::O_RDONLY
        };
        let fd = unsafe {
            libc::openat(
                self.file.as_raw_fd(),
                name.c_str().as_ptr(),
                access | libc::O_CLOEXEC | libc::O_NOFOLLOW | libc::O_NONBLOCK,
            )
        };
        if fd < 0 {
            let source = io::Error::last_os_error();
            if source.raw_os_error() == Some(libc::ENOENT) {
                return Ok(None);
            }
            if is_nofollow_symlink(&source) {
                return Err(NamespaceError::NotRegular);
            }
            return Err(NamespaceError::IoAt {
                operation: "open retained transition file",
                source,
            });
        }
        let file = unsafe { File::from_raw_fd(fd) };
        let identity = regular_identity_any_link(&file, self.identity)?;
        Ok(Some(Regular { file, identity }))
    }

    #[cfg(target_os = "freebsd")]
    fn require_source(&self, source: &Name, expected: Identity) -> Result<(), NamespaceError> {
        let entry = self.entry(source)?.ok_or(NamespaceError::Missing)?;
        require_entry(entry, expected, 1)
    }

    #[cfg(any(target_os = "freebsd", test))]
    fn link_state(
        &self,
        source: &Name,
        destination: &Name,
        expected: Identity,
    ) -> Result<LinkState, NamespaceError> {
        let source = self.entry(source)?;
        let destination = self.entry(destination)?;
        match (source, destination) {
            (Some(source), None) => {
                require_entry(source, expected, 1)?;
                Ok(LinkState::SourceOnly)
            }
            (Some(source), Some(destination)) => {
                require_entry(source, expected, 2)?;
                require_entry(destination, expected, 2)?;
                Ok(LinkState::Linked)
            }
            (None, Some(destination)) => {
                require_entry(destination, expected, 1)?;
                Ok(LinkState::Complete)
            }
            (None, None) => Err(NamespaceError::Missing),
        }
    }

    #[cfg(any(target_os = "freebsd", test))]
    fn unlink_link_alias(
        &self,
        source: &Name,
        destination: &Name,
        expected: Identity,
    ) -> Result<(), NamespaceError> {
        if self.link_state(source, destination, expected)? != LinkState::Linked {
            return Err(NamespaceError::IdentityChanged);
        }
        let result = unsafe { libc::unlinkat(self.file.as_raw_fd(), source.c_str().as_ptr(), 0) };
        if result != 0 {
            return Err(errno("unlink private publication alias"));
        }
        Ok(())
    }

    #[cfg(any(target_os = "freebsd", test))]
    fn prove_link_complete(
        &self,
        source: &Name,
        destination: &Name,
        expected: Identity,
    ) -> Result<(), NamespaceError> {
        self.verify()?;
        if self.link_state(source, destination, expected)? != LinkState::Complete {
            return Err(NamespaceError::IdentityChanged);
        }
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

#[cfg(any(target_os = "freebsd", test))]
fn require_entry(entry: Entry, expected: Identity, links: u64) -> Result<(), NamespaceError> {
    if !entry.regular {
        return Err(NamespaceError::NotRegular);
    }
    if entry.identity != expected {
        return Err(NamespaceError::IdentityChanged);
    }
    if entry.links != links {
        return Err(NamespaceError::LinkCount(entry.links));
    }
    Ok(())
}
