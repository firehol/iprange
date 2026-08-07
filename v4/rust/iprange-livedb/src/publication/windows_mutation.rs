//! Atomic Windows namespace mutation through retained handles.

use std::fs::File;
use std::io;
use std::mem::{align_of, size_of};
use std::os::windows::io::AsRawHandle;

use windows_sys::Win32::Foundation::{
    ERROR_ALREADY_EXISTS, ERROR_FILE_EXISTS, ERROR_FILE_NOT_FOUND, ERROR_PATH_NOT_FOUND, HANDLE,
};
use windows_sys::Win32::Storage::FileSystem::{
    FileDispositionInfoEx, FileRenameInfo, FileRenameInfoEx, SetFileInformationByHandle,
    FILE_DISPOSITION_FLAG_DELETE, FILE_DISPOSITION_FLAG_POSIX_SEMANTICS, FILE_DISPOSITION_INFO_EX,
    FILE_INFO_BY_HANDLE_CLASS, FILE_RENAME_INFO,
};

use super::{sync_file, Directory, Identity, Name, NamespaceError};

impl Directory {
    pub(crate) fn rename_noreplace(
        &self,
        source: &Name,
        source_file: &File,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.rename(source, source_file, destination, FileRenameInfo, 0)
    }

    pub(crate) fn exchange(
        &self,
        _source: &Name,
        _source_file: &File,
        _destination: &Name,
    ) -> Result<(), NamespaceError> {
        Err(NamespaceError::Unsupported)
    }

    pub(crate) fn replace_discarding_destination(
        &self,
        source: &Name,
        source_file: &File,
        destination: &Name,
    ) -> Result<(), NamespaceError> {
        self.rename(
            source,
            source_file,
            destination,
            FileRenameInfoEx,
            0x1 | 0x2,
        )
    }

    fn rename(
        &self,
        source: &Name,
        source_file: &File,
        destination: &Name,
        information_class: FILE_INFO_BY_HANDLE_CLASS,
        flags: u32,
    ) -> Result<(), NamespaceError> {
        self.check_creator()?;
        let identity = super::regular_identity(source_file, self.identity)?;
        self.verify_name(source, identity)?;
        let buffer = rename_buffer(flags, self.file.as_raw_handle(), destination.units())?;
        if unsafe {
            SetFileInformationByHandle(
                source_file.as_raw_handle(),
                information_class,
                buffer.as_ptr().cast(),
                buffer.byte_len,
            )
        } == 0
        {
            let source = io::Error::last_os_error();
            return match source.raw_os_error().map(|code| code as u32) {
                Some(ERROR_ALREADY_EXISTS | ERROR_FILE_EXISTS) => Err(NamespaceError::Exists),
                Some(ERROR_FILE_NOT_FOUND | ERROR_PATH_NOT_FOUND) => Err(NamespaceError::Missing),
                _ => Err(NamespaceError::IoAt {
                    operation: "atomically rename retained Windows file",
                    source,
                }),
            };
        }
        sync_file(source_file).map_err(NamespaceError::Io)?;
        self.require_absent(source)?;
        self.verify_name(destination, identity)
    }

    pub(crate) fn unlink_exact(
        &self,
        name: &Name,
        expected: Identity,
    ) -> Result<bool, NamespaceError> {
        let Some(regular) = self.open_regular(name, true)? else {
            return Ok(false);
        };
        if regular.identity != expected {
            return Err(NamespaceError::IdentityChanged);
        }
        let disposition = FILE_DISPOSITION_INFO_EX {
            Flags: FILE_DISPOSITION_FLAG_DELETE | FILE_DISPOSITION_FLAG_POSIX_SEMANTICS,
        };
        if unsafe {
            SetFileInformationByHandle(
                regular.file.as_raw_handle(),
                FileDispositionInfoEx,
                (&disposition as *const FILE_DISPOSITION_INFO_EX).cast(),
                size_of::<FILE_DISPOSITION_INFO_EX>() as u32,
            )
        } == 0
        {
            return Err(NamespaceError::IoAt {
                operation: "remove exact retained Windows file",
                source: io::Error::last_os_error(),
            });
        }
        self.require_absent(name)?;
        Ok(true)
    }

    pub(crate) fn sync(&self) -> Result<(), NamespaceError> {
        self.verify()
    }
}

struct RenameBuffer {
    storage: Vec<usize>,
    byte_len: u32,
}

impl RenameBuffer {
    fn as_ptr(&self) -> *const usize {
        self.storage.as_ptr()
    }
}

fn rename_buffer(flags: u32, root: HANDLE, name: &[u16]) -> Result<RenameBuffer, NamespaceError> {
    let root_offset = align_up(size_of::<u32>(), align_of::<HANDLE>());
    let length_offset = root_offset + size_of::<HANDLE>();
    let name_offset = length_offset + size_of::<u32>();
    let name_bytes = name
        .len()
        .checked_mul(size_of::<u16>())
        .ok_or(NamespaceError::InvalidName)?;
    // Windows requires the supplied buffer to include the complete fixed
    // structure in addition to the variable filename bytes.
    let byte_len = size_of::<FILE_RENAME_INFO>()
        .checked_add(name_bytes)
        .ok_or(NamespaceError::InvalidName)?;
    let words = byte_len
        .checked_add(size_of::<usize>() - 1)
        .ok_or(NamespaceError::InvalidName)?
        / size_of::<usize>();
    let mut storage = vec![0usize; words];
    let bytes = storage.as_mut_ptr().cast::<u8>();
    unsafe {
        bytes.cast::<u32>().write_unaligned(flags);
        bytes
            .add(root_offset)
            .cast::<HANDLE>()
            .write_unaligned(root);
        bytes
            .add(length_offset)
            .cast::<u32>()
            .write_unaligned(name_bytes as u32);
        std::ptr::copy_nonoverlapping(
            name.as_ptr().cast::<u8>(),
            bytes.add(name_offset),
            name_bytes,
        );
    }
    Ok(RenameBuffer {
        storage,
        byte_len: byte_len as u32,
    })
}

const fn align_up(value: usize, alignment: usize) -> usize {
    (value + alignment - 1) & !(alignment - 1)
}
