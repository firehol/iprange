//! Constant-memory enumeration through a retained Windows directory handle.

use std::fs::File;
use std::mem::size_of;
use std::os::windows::ffi::OsStringExt;
use std::os::windows::io::AsRawHandle;

use windows_sys::Win32::Foundation::ERROR_NO_MORE_FILES;
use windows_sys::Win32::Storage::FileSystem::{
    FileIdBothDirectoryInfo, GetFileInformationByHandleEx, FILE_ID_BOTH_DIR_INFO,
};

use super::{file_identity, final_path, open_directory, Directory, NamespaceError};

const BUFFER_SIZE: usize = 64 * 1024;

#[derive(Debug)]
pub(crate) enum ScanError<E> {
    Namespace(NamespaceError),
    Visitor(E),
}

impl Directory {
    pub(crate) fn scan<E>(
        &self,
        mut visitor: impl FnMut(&[u8]) -> Result<(), E>,
    ) -> Result<(), ScanError<E>> {
        self.verify().map_err(ScanError::Namespace)?;
        let stream = self.stream().map_err(ScanError::Namespace)?;
        let mut buffer = vec![0u8; BUFFER_SIZE];
        loop {
            if unsafe {
                GetFileInformationByHandleEx(
                    stream.as_raw_handle(),
                    FileIdBothDirectoryInfo,
                    buffer.as_mut_ptr().cast(),
                    buffer.len() as u32,
                )
            } == 0
            {
                let source = std::io::Error::last_os_error();
                if source.raw_os_error().map(|code| code as u32) == Some(ERROR_NO_MORE_FILES) {
                    break;
                }
                return Err(ScanError::Namespace(NamespaceError::IoAt {
                    operation: "enumerate retained Windows directory",
                    source,
                }));
            }
            visit_buffer(&buffer, &mut visitor)?;
        }
        self.verify().map_err(ScanError::Namespace)
    }

    fn stream(&self) -> Result<File, NamespaceError> {
        let path =
            std::path::PathBuf::from(std::ffi::OsString::from_wide(&final_path(&self.file)?));
        let file = open_directory(&path)?;
        if file_identity(&file)? != self.identity {
            return Err(NamespaceError::IdentityChanged);
        }
        Ok(file)
    }
}

fn visit_buffer<E>(
    buffer: &[u8],
    visitor: &mut impl FnMut(&[u8]) -> Result<(), E>,
) -> Result<(), ScanError<E>> {
    let mut offset = 0usize;
    loop {
        let header_size = size_of::<FILE_ID_BOTH_DIR_INFO>() - size_of::<u16>();
        if offset
            .checked_add(header_size)
            .map_or(true, |end| end > buffer.len())
        {
            return Err(ScanError::Namespace(NamespaceError::IdentityChanged));
        }
        let entry = unsafe { &*buffer.as_ptr().add(offset).cast::<FILE_ID_BOTH_DIR_INFO>() };
        let units_len = usize::try_from(entry.FileNameLength)
            .ok()
            .filter(|length| length % 2 == 0)
            .map(|length| length / 2)
            .ok_or(ScanError::Namespace(NamespaceError::IdentityChanged))?;
        let name_end = header_size
            .checked_add(units_len * 2)
            .and_then(|length| offset.checked_add(length))
            .ok_or(ScanError::Namespace(NamespaceError::IdentityChanged))?;
        if name_end > buffer.len() {
            return Err(ScanError::Namespace(NamespaceError::IdentityChanged));
        }
        let units = unsafe {
            std::slice::from_raw_parts((entry.FileName.as_ptr()).cast::<u16>(), units_len)
        };
        if let Some(ascii) = ascii_name(units) {
            if ascii != b"." && ascii != b".." {
                visitor(&ascii).map_err(ScanError::Visitor)?;
            }
        }
        if entry.NextEntryOffset == 0 {
            return Ok(());
        }
        let next = usize::try_from(entry.NextEntryOffset)
            .ok()
            .and_then(|step| offset.checked_add(step))
            .filter(|&next| next > offset && next < buffer.len())
            .ok_or(ScanError::Namespace(NamespaceError::IdentityChanged))?;
        offset = next;
    }
}

fn ascii_name(units: &[u16]) -> Option<Vec<u8>> {
    units
        .iter()
        .copied()
        .map(u8::try_from)
        .collect::<Result<Vec<_>, _>>()
        .ok()
        .filter(|bytes| bytes.iter().all(u8::is_ascii))
}
