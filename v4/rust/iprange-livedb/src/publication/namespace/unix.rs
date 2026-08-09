//! Retained POSIX directory and exact one-component namespace operations.

use std::ffi::{CStr, CString, OsStr};
use std::fs::File;
use std::io;
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::Path;

#[cfg(test)]
use crate::name_binding::basename_commitment;
use crate::name_binding::BasenameEncoding;

use crate::publication::security;

#[cfg(test)]
use super::Destination;
use super::{is_nofollow_symlink, Entry, NamespaceError, Regular};

pub(crate) const IDENTITY_KIND: u16 = 1;
pub(crate) const BASENAME_ENCODING_KIND: u16 = 1;
pub(crate) const CREATION_SECURITY_KIND: u16 = 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Identity {
    pub(crate) device: u64,
    pub(crate) inode: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Name(CString);

impl Name {
    pub(crate) fn new(bytes: &[u8]) -> Result<Self, NamespaceError> {
        if bytes.is_empty() || bytes == b"." || bytes == b".." || bytes.contains(&b'/') {
            return Err(NamespaceError::InvalidName);
        }
        CString::new(bytes)
            .map(Self)
            .map_err(|_| NamespaceError::InvalidName)
    }

    pub(crate) fn bytes(&self) -> &[u8] {
        self.0.as_bytes()
    }

    pub(super) fn component_len(&self) -> usize {
        self.0.as_bytes().len()
    }

    pub(crate) fn from_component(component: &OsStr) -> Result<Self, NamespaceError> {
        Self::new(component.as_bytes())
    }

    fn c_str(&self) -> &CStr {
        &self.0
    }
}

pub(super) fn destination_names(
    component: &OsStr,
) -> Result<(Name, Name, BasenameEncoding), NamespaceError> {
    let main = Name::new(component.as_bytes())?;
    let mut coordination = Vec::with_capacity(main.bytes().len() + 8);
    coordination.extend_from_slice(main.bytes());
    coordination.extend_from_slice(b".readers");
    Ok((
        main,
        Name::new(&coordination)?,
        BasenameEncoding::PosixBytes,
    ))
}

#[derive(Debug)]
pub(crate) struct Directory {
    file: File,
    identity: Identity,
    pub(super) name_max: usize,
    pub(super) creator_pid: u32,
}

impl Directory {
    pub(crate) fn open(path: &Path) -> Result<Self, NamespaceError> {
        let file = std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC)
            .open(path)
            .map_err(|source| {
                if source.kind() == io::ErrorKind::NotFound {
                    NamespaceError::Missing
                } else {
                    NamespaceError::Io(source)
                }
            })?;
        let metadata = file.metadata().map_err(NamespaceError::Io)?;
        if !metadata.is_dir() {
            return Err(NamespaceError::NotDirectory);
        }
        require_local_filesystem(&file)?;
        let raw_name_max = unsafe { libc::fpathconf(file.as_raw_fd(), libc::_PC_NAME_MAX) };
        if raw_name_max <= 0 {
            return Err(NamespaceError::Unsupported);
        }
        let name_max = usize::try_from(raw_name_max).map_err(|_| NamespaceError::Unsupported)?;
        Ok(Self {
            file,
            identity: metadata_identity(&metadata),
            name_max,
            creator_pid: std::process::id(),
        })
    }

    pub(crate) fn identity(&self) -> Identity {
        self.identity
    }

    pub(crate) fn create(
        &self,
        name: &Name,
        _security: &security::Profile,
    ) -> Result<File, NamespaceError> {
        self.check_creator()?;
        self.require_name_lengths(&[name])?;
        let fd = unsafe {
            libc::openat(
                self.file.as_raw_fd(),
                name.c_str().as_ptr(),
                libc::O_CREAT
                    | libc::O_EXCL
                    | libc::O_RDWR
                    | libc::O_CLOEXEC
                    | libc::O_NOFOLLOW
                    | libc::O_NONBLOCK,
                security::CREATOR_MODE,
            )
        };
        if fd < 0 {
            let source = io::Error::last_os_error();
            if source.raw_os_error() == Some(libc::EEXIST) {
                return Err(NamespaceError::Exists);
            }
            return Err(NamespaceError::IoAt {
                operation: "create private file",
                source,
            });
        }
        Ok(unsafe { File::from_raw_fd(fd) })
    }

    pub(crate) fn open_regular(
        &self,
        name: &Name,
        writable: bool,
    ) -> Result<Option<Regular>, NamespaceError> {
        self.open_regular_with_links(name, writable, true, "open retained file")
    }

    #[cfg(any(target_os = "freebsd", test))]
    pub(crate) fn open_regular_any_link(
        &self,
        name: &Name,
        writable: bool,
    ) -> Result<Option<Regular>, NamespaceError> {
        self.open_regular_with_links(name, writable, false, "open retained transition file")
    }

    fn open_regular_with_links(
        &self,
        name: &Name,
        writable: bool,
        require_single_link: bool,
        operation: &'static str,
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
            return Err(NamespaceError::IoAt { operation, source });
        }
        let file = unsafe { File::from_raw_fd(fd) };
        let identity = if require_single_link {
            regular_identity(&file, self.identity)?
        } else {
            regular_identity_any_link(&file, self.identity)?
        };
        Ok(Some(Regular { file, identity }))
    }

    pub(crate) fn verify(&self) -> Result<(), NamespaceError> {
        self.check_creator()?;
        let metadata = self.file.metadata().map_err(NamespaceError::Io)?;
        if !metadata.is_dir() || metadata_identity(&metadata) != self.identity {
            return Err(NamespaceError::IdentityChanged);
        }
        require_local_filesystem(&self.file)
    }

    #[allow(clippy::unnecessary_cast)]
    pub(crate) fn entry(&self, name: &Name) -> Result<Option<Entry>, NamespaceError> {
        self.check_creator()?;
        let mut stat = std::mem::MaybeUninit::<libc::stat>::uninit();
        let result = unsafe {
            libc::fstatat(
                self.file.as_raw_fd(),
                name.c_str().as_ptr(),
                stat.as_mut_ptr(),
                libc::AT_SYMLINK_NOFOLLOW,
            )
        };
        if result != 0 {
            let source = io::Error::last_os_error();
            if source.raw_os_error() == Some(libc::ENOENT) {
                return Ok(None);
            }
            return Err(NamespaceError::IoAt {
                operation: "inspect retained name",
                source,
            });
        }
        let stat = unsafe { stat.assume_init() };
        Ok(Some(Entry {
            identity: Identity {
                device: stat.st_dev as u64,
                inode: stat.st_ino as u64,
            },
            links: stat.st_nlink as u64,
            regular: stat.st_mode & libc::S_IFMT == libc::S_IFREG,
        }))
    }
}

pub(crate) fn regular_identity(
    file: &File,
    directory_identity: Identity,
) -> Result<Identity, NamespaceError> {
    let metadata = file.metadata().map_err(NamespaceError::Io)?;
    if !metadata.is_file() {
        return Err(NamespaceError::NotRegular);
    }
    if metadata.dev() != directory_identity.device {
        return Err(NamespaceError::CrossFilesystem);
    }
    if metadata.nlink() != 1 {
        return Err(NamespaceError::LinkCount(metadata.nlink()));
    }
    Ok(metadata_identity(&metadata))
}

pub(crate) fn regular_identity_any_link(
    file: &File,
    directory_identity: Identity,
) -> Result<Identity, NamespaceError> {
    let metadata = file.metadata().map_err(NamespaceError::Io)?;
    if !metadata.is_file() {
        return Err(NamespaceError::NotRegular);
    }
    if metadata.dev() != directory_identity.device {
        return Err(NamespaceError::CrossFilesystem);
    }
    Ok(metadata_identity(&metadata))
}

pub(crate) fn retained_regular_identity(
    file: &File,
    require_single_link: bool,
) -> Result<Identity, NamespaceError> {
    let metadata = file.metadata().map_err(NamespaceError::Io)?;
    if !metadata.is_file() {
        return Err(NamespaceError::NotRegular);
    }
    if require_single_link && metadata.nlink() != 1 {
        return Err(NamespaceError::LinkCount(metadata.nlink()));
    }
    Ok(metadata_identity(&metadata))
}

pub(crate) fn regular_link_count(file: &File) -> Result<u64, NamespaceError> {
    file.metadata()
        .map(|metadata| metadata.nlink())
        .map_err(NamespaceError::Io)
}

pub(crate) fn sync_file(file: &File) -> Result<(), io::Error> {
    #[cfg(target_vendor = "apple")]
    {
        if unsafe { libc::fcntl(file.as_raw_fd(), libc::F_FULLFSYNC) } == 0 {
            return Ok(());
        }
        return Err(io::Error::last_os_error());
    }
    #[cfg(not(target_vendor = "apple"))]
    file.sync_all()
}

fn metadata_identity(metadata: &std::fs::Metadata) -> Identity {
    Identity {
        device: metadata.dev(),
        inode: metadata.ino(),
    }
}

fn require_local_filesystem(file: &File) -> Result<(), NamespaceError> {
    let mut stat = std::mem::MaybeUninit::<libc::statfs>::uninit();
    if unsafe { libc::fstatfs(file.as_raw_fd(), stat.as_mut_ptr()) } != 0 {
        return Err(errno("inspect publication filesystem"));
    }
    let stat = unsafe { stat.assume_init() };
    #[cfg(target_os = "linux")]
    {
        let filesystem = stat.f_type as u32;
        const EXT: u32 = 0x0000_ef53;
        const XFS: u32 = 0x5846_5342;
        const BTRFS: u32 = 0x9123_683e;
        const F2FS: u32 = 0xf2f5_2010;
        const ZFS: u32 = 0x2fc1_2fc1;
        const BCACHEFS: u32 = 0xca45_1a4e;
        if matches!(filesystem, EXT | XFS | BTRFS | F2FS | ZFS | BCACHEFS) {
            Ok(())
        } else {
            Err(NamespaceError::Unsupported)
        }
    }
    #[cfg(any(target_vendor = "apple", target_os = "freebsd"))]
    {
        #[cfg(target_vendor = "apple")]
        let is_local = stat.f_flags & (libc::MNT_LOCAL as u32) != 0;
        #[cfg(target_os = "freebsd")]
        let is_local = stat.f_flags & libc::MNT_LOCAL != 0;
        if is_local {
            Ok(())
        } else {
            Err(NamespaceError::Unsupported)
        }
    }
    #[cfg(not(any(target_os = "linux", target_vendor = "apple", target_os = "freebsd")))]
    Err(NamespaceError::Unsupported)
}

fn errno(operation: &'static str) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: io::Error::last_os_error(),
    }
}

#[cfg(test)]
#[path = "../namespace_tests.rs"]
mod tests;

#[path = "../namespace_scan.rs"]
mod scan;
pub(crate) use scan::ScanError;

#[path = "../namespace_identity.rs"]
mod identity;

#[path = "../namespace_mutation.rs"]
mod mutation;
