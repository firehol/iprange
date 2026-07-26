//! Retained POSIX directory and exact one-component namespace operations.

use std::ffi::{CStr, CString, OsStr};
use std::fs::File;
use std::io;
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::Path;

use crate::name_binding::{basename_commitment, BasenameEncoding};
use crate::path;

use crate::publication::security;

use super::NamespaceError;

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

    pub(crate) fn from_component(component: &OsStr) -> Result<Self, NamespaceError> {
        Self::new(component.as_bytes())
    }

    fn c_str(&self) -> &CStr {
        &self.0
    }
}

#[derive(Debug)]
pub(crate) struct Destination {
    directory: Directory,
    main: Name,
    coordination: Name,
    basename_commitment: [u8; 32],
    security: security::Profile,
}

impl Destination {
    pub(crate) fn bind(path: &Path) -> Result<Self, NamespaceError> {
        let component = path.file_name().ok_or(NamespaceError::InvalidName)?;
        path::validate_main_name(component).map_err(|_| NamespaceError::InvalidName)?;
        let directory = Directory::open(parent(path))?;
        let main = Name::new(component.as_bytes())?;
        let mut coordination = Vec::with_capacity(main.bytes().len() + 8);
        coordination.extend_from_slice(main.bytes());
        coordination.extend_from_slice(b".readers");
        let coordination = Name::new(&coordination)?;
        directory.require_name_lengths(&[&main, &coordination])?;
        let commitment = basename_commitment(BasenameEncoding::PosixBytes, main.bytes())
            .map_err(|_| NamespaceError::InvalidName)?;
        Ok(Self {
            directory,
            main,
            coordination,
            basename_commitment: commitment,
            security: security::Profile::capture()?,
        })
    }

    pub(crate) fn directory(&self) -> &Directory {
        &self.directory
    }

    pub(crate) fn main(&self) -> &Name {
        &self.main
    }

    pub(crate) fn coordination(&self) -> &Name {
        &self.coordination
    }

    pub(crate) fn basename_commitment(&self) -> [u8; 32] {
        self.basename_commitment
    }

    pub(crate) fn security_commitment(&self) -> [u8; 32] {
        self.security.commitment()
    }

    pub(crate) fn secure_created(&self, file: &File) -> Result<(), NamespaceError> {
        security::secure_creator_only(file, &self.security)
    }

    pub(crate) fn create(&self, name: &Name) -> Result<File, NamespaceError> {
        self.directory.create(name, &self.security)
    }

    pub(crate) fn verify_created(&self, file: &File) -> Result<(), NamespaceError> {
        if security::creator_only_commitment(file)? != self.security.commitment() {
            return Err(NamespaceError::AccessPolicy);
        }
        Ok(())
    }

    pub(crate) fn require_fail_if_exists_available(&self) -> Result<(), NamespaceError> {
        self.directory.require_absent(&self.main)?;
        self.directory.require_absent(&self.coordination)
    }

    pub(crate) fn output_name(&self, attempt: [u8; 16]) -> Result<Name, NamespaceError> {
        self.attempt_name(b".iprange-publish-", attempt)
    }

    pub(crate) fn reservation_name(&self, attempt: [u8; 16]) -> Result<Name, NamespaceError> {
        self.attempt_name(b".iprange-reservation-", attempt)
    }

    fn attempt_name(&self, prefix: &[u8], attempt: [u8; 16]) -> Result<Name, NamespaceError> {
        if attempt == [0; 16] {
            return Err(NamespaceError::InvalidName);
        }
        let mut bytes = Vec::with_capacity(prefix.len() + 36);
        bytes.extend_from_slice(prefix);
        for byte in attempt {
            bytes.push(hex(byte >> 4));
            bytes.push(hex(byte & 0x0f));
        }
        bytes.extend_from_slice(b".tmp");
        let name = Name::new(&bytes)?;
        self.directory.require_name_lengths(&[&name])?;
        Ok(name)
    }
}

#[derive(Debug)]
pub(crate) struct Directory {
    file: File,
    identity: Identity,
    name_max: usize,
    creator_pid: u32,
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
            return Err(NamespaceError::IoAt {
                operation: "open retained file",
                source,
            });
        }
        let file = unsafe { File::from_raw_fd(fd) };
        let identity = regular_identity(&file, self.identity)?;
        Ok(Some(Regular { file, identity }))
    }

    pub(crate) fn verify_name(
        &self,
        name: &Name,
        expected: Identity,
    ) -> Result<(), NamespaceError> {
        let found = self.entry(name)?.ok_or(NamespaceError::Missing)?;
        if !found.regular {
            return Err(NamespaceError::NotRegular);
        }
        if found.identity != expected {
            return Err(NamespaceError::IdentityChanged);
        }
        if found.links != 1 {
            return Err(NamespaceError::LinkCount(found.links));
        }
        Ok(())
    }

    pub(crate) fn require_absent(&self, name: &Name) -> Result<(), NamespaceError> {
        if self.entry(name)?.is_some() {
            return Err(NamespaceError::Exists);
        }
        Ok(())
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

    fn require_name_lengths(&self, names: &[&Name]) -> Result<(), NamespaceError> {
        if names.iter().any(|name| name.bytes().len() > self.name_max) {
            return Err(NamespaceError::InvalidName);
        }
        Ok(())
    }

    fn check_creator(&self) -> Result<(), NamespaceError> {
        if std::process::id() != self.creator_pid {
            return Err(NamespaceError::ForkedHandle);
        }
        Ok(())
    }
}

#[derive(Debug)]
pub(crate) struct Regular {
    pub(crate) file: File,
    pub(crate) identity: Identity,
}

impl Regular {
    pub(crate) fn creator_only_commitment(&self) -> Result<[u8; 32], NamespaceError> {
        security::creator_only_commitment(&self.file)
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Entry {
    pub(crate) identity: Identity,
    pub(crate) links: u64,
    pub(crate) regular: bool,
}

fn parent(path: &Path) -> &Path {
    match path.parent() {
        Some(parent) if !parent.as_os_str().is_empty() => parent,
        _ => Path::new("."),
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

fn hex(value: u8) -> u8 {
    match value {
        0..=9 => b'0' + value,
        _ => b'a' + value - 10,
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
