//! Open-file-description byte-range locks.

use std::fs::File;

use crate::error::{Error, Result};

#[derive(Clone, Copy)]
pub(crate) enum Mode {
    Shared,
    Exclusive,
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
mod platform {
    use std::io;
    use std::os::fd::AsRawFd;

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

#[cfg(not(any(target_os = "linux", target_vendor = "apple")))]
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
