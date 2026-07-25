//! Constant-memory iteration of one retained directory.

use std::ffi::CStr;
use std::io;
use std::os::fd::AsRawFd;

use super::{Directory, NamespaceError};

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
        let mut stream = Stream::open(self).map_err(ScanError::Namespace)?;
        let visited = stream.visit(&mut visitor);
        self.verify().map_err(ScanError::Namespace)?;
        visited
    }
}

struct Stream(*mut libc::DIR);

impl Stream {
    fn open(directory: &Directory) -> Result<Self, NamespaceError> {
        directory.check_creator()?;
        let fd = unsafe {
            libc::openat(
                directory.file.as_raw_fd(),
                b".\0".as_ptr().cast(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            )
        };
        if fd < 0 {
            return Err(last_error("open retained directory stream"));
        }
        let stream = unsafe { libc::fdopendir(fd) };
        if stream.is_null() {
            let source = io::Error::last_os_error();
            unsafe {
                libc::close(fd);
            }
            return Err(NamespaceError::IoAt {
                operation: "open retained directory stream",
                source,
            });
        }
        Ok(Self(stream))
    }

    fn visit<E>(
        &mut self,
        visitor: &mut impl FnMut(&[u8]) -> Result<(), E>,
    ) -> Result<(), ScanError<E>> {
        while let Some(name) = self.next().map_err(ScanError::Namespace)? {
            if name != b"." && name != b".." {
                visitor(name).map_err(ScanError::Visitor)?;
            }
        }
        Ok(())
    }

    fn next(&mut self) -> Result<Option<&[u8]>, NamespaceError> {
        set_errno(0);
        let entry = unsafe { libc::readdir(self.0) };
        if entry.is_null() {
            return match get_errno() {
                0 => Ok(None),
                _ => Err(last_error("read retained directory stream")),
            };
        }
        let name = unsafe { CStr::from_ptr((*entry).d_name.as_ptr()) };
        Ok(Some(name.to_bytes()))
    }
}

impl Drop for Stream {
    fn drop(&mut self) {
        unsafe {
            libc::closedir(self.0);
        }
    }
}

fn get_errno() -> i32 {
    unsafe { *libc::__errno_location() }
}

fn set_errno(value: i32) {
    unsafe {
        *libc::__errno_location() = value;
    }
}

fn last_error(operation: &'static str) -> NamespaceError {
    NamespaceError::IoAt {
        operation,
        source: io::Error::last_os_error(),
    }
}
