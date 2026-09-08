//! Same-file identity for the CLI output-over-source guard.
//!
//! The CLI refuses an output destination that is the same file as an
//! open reader's source, and renames while a reader is open must not
//! hide that identity: a pathname comparison misses a moved file, so
//! the guard also compares the file identity captured at open. POSIX
//! exposes the device+inode pair stably; Windows exposes the volume
//! serial and 64-bit file index through `GetFileInformationByHandle`
//! (the same windows-sys surface the namespace code uses).

use std::path::Path;

/// File identity of `path` at the moment of the call: `(device,
/// inode)` on POSIX and `(volume serial, file index)` on Windows.
/// Returns `None` when the path does not exist or the platform has no
/// stable identity source.
pub fn identity(path: &Path) -> Option<(u64, u64)> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        let meta = std::fs::metadata(path).ok()?;
        Some((meta.dev(), meta.ino()))
    }
    #[cfg(windows)]
    {
        windows_file_identity(path)
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = path;
        None
    }
}

#[cfg(windows)]
fn windows_file_identity(path: &Path) -> Option<(u64, u64)> {
    use std::os::windows::ffi::OsStrExt;
    use std::ptr::null_mut;

    use windows_sys::Win32::Foundation::{CloseHandle, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::Storage::FileSystem::{
        CreateFileW, GetFileInformationByHandle, BY_HANDLE_FILE_INFORMATION, FILE_FLAG_BACKUP_SEMANTICS,
        FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE, OPEN_EXISTING,
    };

    let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
    unsafe {
        // FILE_SHARE_DELETE mirrors how the products open their live
        // databases: the file remains nameable for identity checks
        // while a pinned reader holds it open.
        let handle = CreateFileW(
            wide.as_ptr(),
            FILE_READ_ATTRIBUTES,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            null_mut(),
            OPEN_EXISTING,
            FILE_FLAG_BACKUP_SEMANTICS,
            null_mut(),
        );
        if handle == INVALID_HANDLE_VALUE {
            return None;
        }
        let mut info: BY_HANDLE_FILE_INFORMATION = std::mem::zeroed();
        let ok = GetFileInformationByHandle(handle, &mut info);
        CloseHandle(handle);
        if ok == 0 {
            return None;
        }
        let dev = u64::from(info.dwVolumeSerialNumber);
        let ino = (u64::from(info.nFileIndexHigh) << 32) | u64::from(info.nFileIndexLow);
        Some((dev, ino))
    }
}
