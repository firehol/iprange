//! Open-file-description byte-range locks.

use std::fs::File;

use crate::cancellation::CancellationToken;
#[cfg(not(any(target_os = "linux", target_vendor = "apple", windows)))]
use crate::error::Error;
use crate::error::Result;

#[derive(Clone, Copy)]
pub(crate) enum Mode {
    Shared,
    Exclusive,
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
mod platform {
    use std::io;
    use std::os::fd::AsRawFd;

    use crate::error::Error;

    use super::*;

    pub(crate) fn lock(file: &File, offset: u64, mode: Mode) -> Result<()> {
        set(file, offset, mode, true).map(|_| ())
    }

    pub(crate) fn try_lock(file: &File, offset: u64, mode: Mode) -> Result<bool> {
        set(file, offset, mode, false)
    }

    pub(crate) fn unlock(file: &File, offset: u64) -> Result<()> {
        let mut lock = flock(offset, libc::F_UNLCK as libc::c_short)?;
        loop {
            let result = unsafe { libc::fcntl(file.as_raw_fd(), libc::F_OFD_SETLK, &mut lock) };
            if result == 0 {
                return Ok(());
            }
            let error = io::Error::last_os_error();
            if error.kind() != io::ErrorKind::Interrupted {
                return Err(error.into());
            }
        }
    }

    fn set(file: &File, offset: u64, mode: Mode, wait: bool) -> Result<bool> {
        let lock_type = match mode {
            Mode::Shared => libc::F_RDLCK,
            Mode::Exclusive => libc::F_WRLCK,
        };
        let mut lock = flock(offset, lock_type as libc::c_short)?;
        let command = if wait {
            libc::F_OFD_SETLKW
        } else {
            libc::F_OFD_SETLK
        };
        loop {
            let result = unsafe { libc::fcntl(file.as_raw_fd(), command, &mut lock) };
            if result == 0 {
                return Ok(true);
            }
            let error = io::Error::last_os_error();
            if error.kind() == io::ErrorKind::Interrupted {
                continue;
            }
            if !wait && matches!(error.raw_os_error(), Some(libc::EACCES | libc::EAGAIN)) {
                return Ok(false);
            }
            return Err(error.into());
        }
    }

    fn flock(offset: u64, lock_type: libc::c_short) -> Result<libc::flock> {
        let start = libc::off_t::try_from(offset)
            .map_err(|_| Error::InvalidArgument("lock offset exceeds off_t"))?;
        Ok(libc::flock {
            l_type: lock_type,
            l_whence: libc::SEEK_SET as libc::c_short,
            l_start: start,
            l_len: 1,
            l_pid: 0,
        })
    }
}

#[cfg(windows)]
mod platform {
    use std::io;
    use std::os::windows::io::AsRawHandle;

    use windows_sys::Win32::Foundation::{ERROR_LOCK_VIOLATION, HANDLE};
    use windows_sys::Win32::Storage::FileSystem::{
        LockFileEx, UnlockFileEx, LOCKFILE_EXCLUSIVE_LOCK, LOCKFILE_FAIL_IMMEDIATELY,
    };
    use windows_sys::Win32::System::IO::{OVERLAPPED, OVERLAPPED_0, OVERLAPPED_0_0};

    use super::*;

    pub(crate) fn lock(file: &File, offset: u64, mode: Mode) -> Result<()> {
        set(file, offset, mode, true).map(|_| ())
    }

    pub(crate) fn try_lock(file: &File, offset: u64, mode: Mode) -> Result<bool> {
        set(file, offset, mode, false)
    }

    pub(crate) fn unlock(file: &File, offset: u64) -> Result<()> {
        let mut overlapped = overlapped(offset);
        let result =
            unsafe { UnlockFileEx(handle(file), 0, 1, 0, &mut overlapped as *mut OVERLAPPED) };
        if result == 0 {
            Err(io::Error::last_os_error().into())
        } else {
            Ok(())
        }
    }

    fn set(file: &File, offset: u64, mode: Mode, wait: bool) -> Result<bool> {
        let mut flags = match mode {
            Mode::Shared => 0,
            Mode::Exclusive => LOCKFILE_EXCLUSIVE_LOCK,
        };
        if !wait {
            flags |= LOCKFILE_FAIL_IMMEDIATELY;
        }
        let mut overlapped = overlapped(offset);
        let result = unsafe {
            LockFileEx(
                handle(file),
                flags,
                0,
                1,
                0,
                &mut overlapped as *mut OVERLAPPED,
            )
        };
        if result != 0 {
            return Ok(true);
        }
        let error = io::Error::last_os_error();
        if !wait && error.raw_os_error() == Some(ERROR_LOCK_VIOLATION as i32) {
            Ok(false)
        } else {
            Err(error.into())
        }
    }

    fn handle(file: &File) -> HANDLE {
        file.as_raw_handle() as HANDLE
    }

    fn overlapped(offset: u64) -> OVERLAPPED {
        OVERLAPPED {
            Internal: 0,
            InternalHigh: 0,
            Anonymous: OVERLAPPED_0 {
                Anonymous: OVERLAPPED_0_0 {
                    Offset: offset as u32,
                    OffsetHigh: (offset >> 32) as u32,
                },
            },
            hEvent: std::ptr::null_mut(),
        }
    }
}

#[cfg(not(any(target_os = "linux", target_vendor = "apple", windows)))]
mod platform {
    use super::*;

    pub(crate) fn lock(_file: &File, _offset: u64, _mode: Mode) -> Result<()> {
        Err(Error::Unsupported(
            "live coordination is not implemented on this platform",
        ))
    }

    pub(crate) fn try_lock(_file: &File, _offset: u64, _mode: Mode) -> Result<bool> {
        Err(Error::Unsupported(
            "live coordination is not implemented on this platform",
        ))
    }

    pub(crate) fn unlock(_file: &File, _offset: u64) -> Result<()> {
        Err(Error::Unsupported(
            "live coordination is not implemented on this platform",
        ))
    }
}

pub(crate) use platform::{lock, try_lock, unlock};

pub(crate) fn lock_cancellable(
    file: &File,
    offset: u64,
    mode: Mode,
    cancellation: &CancellationToken,
) -> Result<()> {
    loop {
        cancellation.check()?;
        if try_lock(file, offset, mode)? {
            return Ok(());
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
}
