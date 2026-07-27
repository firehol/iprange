//! Retained Windows directory and exact one-component namespace operations.

use std::fs::File;
use std::io;
use std::mem::{size_of, zeroed};
use std::os::windows::ffi::{OsStrExt, OsStringExt};
use std::os::windows::io::{AsRawHandle, FromRawHandle};
use std::path::Path;
use std::ptr::null_mut;

use windows_sys::Win32::Foundation::{
    ERROR_FILE_NOT_FOUND, ERROR_PATH_NOT_FOUND, GENERIC_READ, GENERIC_WRITE, INVALID_HANDLE_VALUE,
};
use windows_sys::Win32::Storage::FileSystem::{
    CreateFileW, FileIdInfo, FlushFileBuffers, GetFileInformationByHandle,
    GetFileInformationByHandleEx, GetFinalPathNameByHandleW, GetVolumeInformationByHandleW,
    BY_HANDLE_FILE_INFORMATION, DELETE, FILE_ATTRIBUTE_DIRECTORY, FILE_ATTRIBUTE_NORMAL,
    FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
    FILE_FLAG_WRITE_THROUGH, FILE_ID_INFO, FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE,
    FILE_SHARE_READ, FILE_SHARE_WRITE, FILE_WRITE_ATTRIBUTES, OPEN_EXISTING, READ_CONTROL,
};

use crate::name_binding::{basename_commitment, BasenameEncoding};
use crate::path;
use crate::publication::security;

use super::NamespaceError;

pub(crate) const IDENTITY_KIND: u16 = 2;
pub(crate) const BASENAME_ENCODING_KIND: u16 = 2;
pub(crate) const CREATION_SECURITY_KIND: u16 = 2;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Identity {
    volume: u64,
    file_id: [u8; 16],
}

impl Identity {
    pub(crate) fn encode(self) -> [u8; 32] {
        let mut bytes = [0; 32];
        bytes[..8].copy_from_slice(&self.volume.to_le_bytes());
        bytes[8..24].copy_from_slice(&self.file_id);
        bytes
    }

    pub(crate) fn decode(bytes: [u8; 32]) -> Option<Self> {
        if bytes == [0; 32] || bytes[24..].iter().any(|&byte| byte != 0) {
            return None;
        }
        Some(Self {
            volume: u64::from_le_bytes(bytes[..8].try_into().expect("fixed volume identity")),
            file_id: bytes[8..24].try_into().expect("fixed file identity"),
        })
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Name {
    units: Box<[u16]>,
    bytes: Box<[u8]>,
}

impl Name {
    pub(crate) fn new(bytes: &[u8]) -> Result<Self, NamespaceError> {
        if bytes.is_empty()
            || bytes == b"."
            || bytes == b".."
            || bytes
                .iter()
                .any(|byte| !byte.is_ascii() || matches!(*byte, 0 | b'/' | b'\\' | b':'))
        {
            return Err(NamespaceError::InvalidName);
        }
        Self::from_units(bytes.iter().map(|&byte| u16::from(byte)).collect())
    }

    fn from_units(units: Vec<u16>) -> Result<Self, NamespaceError> {
        let mut bytes = Vec::with_capacity(units.len() * 2);
        for unit in &units {
            bytes.extend_from_slice(&unit.to_le_bytes());
        }
        basename_commitment(BasenameEncoding::WindowsUtf16Le, &bytes)
            .map_err(|_| NamespaceError::InvalidName)?;
        Ok(Self {
            units: units.into_boxed_slice(),
            bytes: bytes.into_boxed_slice(),
        })
    }

    pub(crate) fn bytes(&self) -> &[u8] {
        &self.bytes
    }

    pub(crate) fn from_component(component: &std::ffi::OsStr) -> Result<Self, NamespaceError> {
        Self::from_units(component.encode_wide().collect())
    }

    pub(crate) fn from_encoded(bytes: &[u8]) -> Result<Self, NamespaceError> {
        if bytes.len() % 2 != 0 {
            return Err(NamespaceError::InvalidName);
        }
        let units = bytes
            .chunks_exact(2)
            .map(|pair| u16::from_le_bytes([pair[0], pair[1]]))
            .collect();
        Self::from_units(units)
    }

    fn units(&self) -> &[u16] {
        &self.units
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
        let main = Name::from_units(component.encode_wide().collect())?;
        let mut coordination = main.units().to_vec();
        coordination.extend(".readers".encode_utf16());
        let coordination = Name::from_units(coordination)?;
        let directory = Directory::open(parent(path))?;
        directory.require_name_lengths(&[&main, &coordination])?;
        let basename_commitment =
            basename_commitment(BasenameEncoding::WindowsUtf16Le, main.bytes())
                .map_err(|_| NamespaceError::InvalidName)?;
        Ok(Self {
            directory,
            main,
            coordination,
            basename_commitment,
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

    pub(crate) fn create(&self, name: &Name) -> Result<File, NamespaceError> {
        self.directory.create(name, &self.security)
    }

    pub(crate) fn secure_created(&self, file: &File) -> Result<(), NamespaceError> {
        security::secure_creator_only(file, &self.security)
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
        let file = open_directory(path)?;
        let info = handle_info(&file)?;
        if info.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY == 0
            || info.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT != 0
        {
            return Err(NamespaceError::NotDirectory);
        }
        let identity = file_identity(&file)?;
        let name_max = require_local_ntfs(&file)?;
        Ok(Self {
            file,
            identity,
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
        profile: &security::Profile,
    ) -> Result<File, NamespaceError> {
        self.check_creator()?;
        self.require_name_lengths(&[name])?;
        let path = self.entry_path(name)?;
        security::create_private(&path, profile, true).map_err(|error| match error {
            NamespaceError::IoAt { source, .. }
                if source.raw_os_error()
                    == Some(windows_sys::Win32::Foundation::ERROR_FILE_EXISTS as i32) =>
            {
                NamespaceError::Exists
            }
            error => error,
        })
    }

    pub(crate) fn open_regular(
        &self,
        name: &Name,
        writable: bool,
    ) -> Result<Option<Regular>, NamespaceError> {
        self.check_creator()?;
        let access = GENERIC_READ
            | READ_CONTROL
            | if writable {
                GENERIC_WRITE | FILE_WRITE_ATTRIBUTES | DELETE
            } else {
                0
            };
        let Some(file) = self.open_entry(name, access, writable)? else {
            return Ok(None);
        };
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
        let info = handle_info(&self.file)?;
        if info.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY == 0
            || info.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT != 0
            || file_identity(&self.file)? != self.identity
        {
            return Err(NamespaceError::IdentityChanged);
        }
        require_local_ntfs(&self.file).map(|_| ())
    }

    pub(crate) fn entry(&self, name: &Name) -> Result<Option<Entry>, NamespaceError> {
        self.check_creator()?;
        let Some(file) = self.open_entry(name, FILE_READ_ATTRIBUTES | READ_CONTROL, false)? else {
            return Ok(None);
        };
        let info = handle_info(&file)?;
        Ok(Some(Entry {
            identity: file_identity(&file)?,
            links: u64::from(info.nNumberOfLinks),
            regular: info.dwFileAttributes
                & (FILE_ATTRIBUTE_DIRECTORY | FILE_ATTRIBUTE_REPARSE_POINT)
                == 0,
        }))
    }

    fn open_entry(
        &self,
        name: &Name,
        access: u32,
        write_through: bool,
    ) -> Result<Option<File>, NamespaceError> {
        let path = self.entry_path(name)?;
        let flags = FILE_ATTRIBUTE_NORMAL
            | FILE_FLAG_OPEN_REPARSE_POINT
            | if write_through {
                FILE_FLAG_WRITE_THROUGH
            } else {
                0
            };
        open_path(
            &path,
            access,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            OPEN_EXISTING,
            flags,
        )
    }

    fn entry_path(&self, name: &Name) -> Result<std::path::PathBuf, NamespaceError> {
        let mut units = final_path(&self.file)?;
        if !units.ends_with(&[b'\\' as u16]) {
            units.push(b'\\' as u16);
        }
        units.extend_from_slice(name.units());
        Ok(std::path::PathBuf::from(std::ffi::OsString::from_wide(
            &units,
        )))
    }

    fn require_name_lengths(&self, names: &[&Name]) -> Result<(), NamespaceError> {
        if names.iter().any(|name| name.units().len() > self.name_max) {
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

pub(crate) fn regular_identity(
    file: &File,
    directory_identity: Identity,
) -> Result<Identity, NamespaceError> {
    let info = handle_info(file)?;
    if info.dwFileAttributes & (FILE_ATTRIBUTE_DIRECTORY | FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
        return Err(NamespaceError::NotRegular);
    }
    let identity = file_identity(file)?;
    if identity.volume != directory_identity.volume {
        return Err(NamespaceError::CrossFilesystem);
    }
    if info.nNumberOfLinks != 1 {
        return Err(NamespaceError::LinkCount(u64::from(info.nNumberOfLinks)));
    }
    Ok(identity)
}

pub(crate) fn regular_identity_any_link(
    file: &File,
    directory_identity: Identity,
) -> Result<Identity, NamespaceError> {
    let info = handle_info(file)?;
    if info.dwFileAttributes & (FILE_ATTRIBUTE_DIRECTORY | FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
        return Err(NamespaceError::NotRegular);
    }
    let identity = file_identity(file)?;
    if identity.volume != directory_identity.volume {
        return Err(NamespaceError::CrossFilesystem);
    }
    Ok(identity)
}

pub(crate) fn retained_regular_identity(
    file: &File,
    require_single_link: bool,
) -> Result<Identity, NamespaceError> {
    let info = handle_info(file)?;
    if info.dwFileAttributes & (FILE_ATTRIBUTE_DIRECTORY | FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
        return Err(NamespaceError::NotRegular);
    }
    if require_single_link && info.nNumberOfLinks != 1 {
        return Err(NamespaceError::LinkCount(u64::from(info.nNumberOfLinks)));
    }
    file_identity(file)
}

pub(crate) fn regular_link_count(file: &File) -> Result<u64, NamespaceError> {
    handle_info(file).map(|info| u64::from(info.nNumberOfLinks))
}

pub(crate) fn sync_file(file: &File) -> Result<(), io::Error> {
    if unsafe { FlushFileBuffers(file.as_raw_handle()) } == 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

fn open_directory(path: &Path) -> Result<File, NamespaceError> {
    let file = open_path(
        path,
        GENERIC_READ | FILE_READ_ATTRIBUTES,
        FILE_SHARE_READ | FILE_SHARE_WRITE,
        OPEN_EXISTING,
        FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT,
    )?
    .ok_or(NamespaceError::Missing)?;
    Ok(file)
}

fn open_path(
    path: &Path,
    access: u32,
    share: u32,
    creation: u32,
    flags: u32,
) -> Result<Option<File>, NamespaceError> {
    let mut units: Vec<u16> = path.as_os_str().encode_wide().collect();
    if units.contains(&0) {
        return Err(NamespaceError::InvalidName);
    }
    units.push(0);
    let handle = unsafe {
        CreateFileW(
            units.as_ptr(),
            access,
            share,
            null_mut(),
            creation,
            flags,
            null_mut(),
        )
    };
    if handle == INVALID_HANDLE_VALUE {
        let source = io::Error::last_os_error();
        return match source.raw_os_error().map(|code| code as u32) {
            Some(ERROR_FILE_NOT_FOUND | ERROR_PATH_NOT_FOUND) => Ok(None),
            _ => Err(NamespaceError::IoAt {
                operation: "open retained Windows path",
                source,
            }),
        };
    }
    Ok(Some(unsafe { File::from_raw_handle(handle) }))
}

fn handle_info(file: &File) -> Result<BY_HANDLE_FILE_INFORMATION, NamespaceError> {
    let mut info = unsafe { zeroed::<BY_HANDLE_FILE_INFORMATION>() };
    if unsafe { GetFileInformationByHandle(file.as_raw_handle(), &mut info) } == 0 {
        return Err(last_error("inspect retained Windows handle"));
    }
    Ok(info)
}

fn file_identity(file: &File) -> Result<Identity, NamespaceError> {
    let mut info = FILE_ID_INFO::default();
    if unsafe {
        GetFileInformationByHandleEx(
            file.as_raw_handle(),
            FileIdInfo,
            (&mut info as *mut FILE_ID_INFO).cast(),
            size_of::<FILE_ID_INFO>() as u32,
        )
    } == 0
    {
        return Err(last_error("read retained Windows file identity"));
    }
    Ok(Identity {
        volume: info.VolumeSerialNumber,
        file_id: info.FileId.Identifier,
    })
}

fn require_local_ntfs(file: &File) -> Result<usize, NamespaceError> {
    const NTFS: [u16; 4] = [b'N' as u16, b'T' as u16, b'F' as u16, b'S' as u16];
    let mut maximum = 0;
    let mut filesystem = [0u16; 16];
    if unsafe {
        GetVolumeInformationByHandleW(
            file.as_raw_handle(),
            null_mut(),
            0,
            null_mut(),
            &mut maximum,
            null_mut(),
            filesystem.as_mut_ptr(),
            filesystem.len() as u32,
        )
    } == 0
    {
        return Err(last_error("inspect publication volume"));
    }
    let length = filesystem
        .iter()
        .position(|&unit| unit == 0)
        .unwrap_or(filesystem.len());
    if filesystem[..length] != NTFS {
        return Err(NamespaceError::Unsupported);
    }
    usize::try_from(maximum).map_err(|_| NamespaceError::Unsupported)
}

fn final_path(file: &File) -> Result<Vec<u16>, NamespaceError> {
    let required = unsafe { GetFinalPathNameByHandleW(file.as_raw_handle(), null_mut(), 0, 0) };
    if required == 0 {
        return Err(last_error("size retained Windows directory path"));
    }
    let mut units = vec![0u16; required as usize + 1];
    let written = unsafe {
        GetFinalPathNameByHandleW(
            file.as_raw_handle(),
            units.as_mut_ptr(),
            units.len() as u32,
            0,
        )
    };
    if written == 0 || written as usize >= units.len() {
        return Err(last_error("read retained Windows directory path"));
    }
    units.truncate(written as usize);
    Ok(units)
}

fn parent(path: &Path) -> &Path {
    match path.parent() {
        Some(parent) if !parent.as_os_str().is_empty() => parent,
        _ => Path::new("."),
    }
}

fn last_error(operation: &'static str) -> NamespaceError {
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

#[path = "../windows_mutation.rs"]
mod mutation;

#[path = "../windows_scan.rs"]
mod scan;
pub(crate) use scan::ScanError;
