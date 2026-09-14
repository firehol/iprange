//! Shared unit-test support.

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};

static NEXT_PATH: AtomicU64 = AtomicU64::new(0);

pub(crate) fn unique_path(prefix: &str) -> PathBuf {
    let time = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let sequence = NEXT_PATH.fetch_add(1, Ordering::Relaxed);
    std::env::temp_dir().join(format!("{prefix}-{}-{time}-{sequence}", std::process::id()))
}

/// Open `path` as a FIFO that no writer has opened. FIFO is the only kind
/// whose open can wait for a partner on every supported platform, so it is
/// the one non-regular kind pinned unconditionally.
#[cfg(unix)]
pub(crate) fn writerless_fifo(label: &str) -> (PathBuf, PathBuf) {
    use std::ffi::CString;
    use std::os::unix::ffi::OsStrExt;

    let directory = unique_path(&format!("iprange-nonregular-{label}"));
    std::fs::create_dir_all(&directory).expect("create the non-regular scratch directory");
    let path = directory.join("node");
    let name =
        CString::new(path.as_os_str().as_bytes()).expect("fifo path must not contain a NUL byte");
    assert_eq!(
        unsafe { libc::mkfifo(name.as_ptr(), 0o600) },
        0,
        "mkfifo {path:?}: {}",
        std::io::Error::last_os_error()
    );
    (directory, path)
}

/// The non-regular path kinds that every authoritative-fd open arm must
/// classify rather than block on. Whether opening a directory or a socket
/// inode succeeds differs across the supported unixes, so the non-FIFO
/// kinds are qualified only where the dual-language battery runs (Linux).
#[cfg(target_os = "linux")]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum NonRegularPath {
    /// Writerless FIFO: a read-only open would wait for a writer, and an
    /// unopened reader would wait for a reader.
    Fifo,
    /// Directory.
    Directory,
    /// Bound `AF_UNIX` socket file: no stream open is possible.
    Socket,
    /// Symbolic link whose target is a writerless FIFO.
    SymlinkFifo,
    /// Symbolic link whose target is a directory.
    SymlinkDirectory,
    /// The system's own character device.
    CharacterDevice,
    /// A name that does not exist.
    Missing,
}

/// One materialised [`NonRegularPath`] plus the scratch directory that owns
/// it. `path` is what an open arm receives; `cleanup` removes the scratch
/// directory (and is a no-op for the system character device).
#[cfg(target_os = "linux")]
#[must_use = "the fixture owns the scratch directory its path lives in"]
pub(crate) struct NonRegularFixture {
    pub(crate) path: PathBuf,
    scratch: Option<PathBuf>,
}

#[cfg(target_os = "linux")]
impl NonRegularFixture {
    pub(crate) fn cleanup(&self) {
        if let Some(directory) = &self.scratch {
            let _ = std::fs::remove_dir_all(directory);
        }
    }
}

/// Materialise one [`NonRegularPath`] under a private scratch directory.
#[cfg(target_os = "linux")]
pub(crate) fn non_regular(kind: NonRegularPath, label: &str) -> NonRegularFixture {
    use std::ffi::CString;
    use std::os::unix::ffi::OsStrExt;
    use std::os::unix::net::UnixListener;
    use std::path::Path;

    fn mkfifo(path: &Path) {
        let name = CString::new(path.as_os_str().as_bytes())
            .expect("fifo path must not contain a NUL byte");
        assert_eq!(
            unsafe { libc::mkfifo(name.as_ptr(), 0o600) },
            0,
            "mkfifo {path:?}: {}",
            std::io::Error::last_os_error()
        );
    }

    let scratch = unique_path(&format!("iprange-nonregular-{label}"));
    std::fs::create_dir_all(&scratch).expect("create the non-regular scratch directory");
    let path = scratch.join("node");
    match kind {
        NonRegularPath::Fifo => mkfifo(&path),
        NonRegularPath::Directory => std::fs::create_dir(&path).expect("create the directory"),
        NonRegularPath::Socket => {
            let listener = UnixListener::bind(&path).expect("bind the AF_UNIX socket file");
            // Dropping a UnixListener unlinks its path, and the pins need
            // the socket file to stay in the namespace.
            std::mem::forget(listener);
        }
        NonRegularPath::SymlinkFifo => {
            let target = scratch.join("target-fifo");
            mkfifo(&target);
            std::os::unix::fs::symlink(&target, &path).expect("link to the fifo");
        }
        NonRegularPath::SymlinkDirectory => {
            let target = scratch.join("target-dir");
            std::fs::create_dir(&target).expect("create the linked directory");
            std::os::unix::fs::symlink(&target, &path).expect("link to the directory");
        }
        NonRegularPath::CharacterDevice => {
            // The system's null device is the portable character device; it
            // is not owned by this fixture, so nothing removes it.
            std::fs::symlink_metadata("/dev/null")
                .expect("the system null device must exist on unix");
            return NonRegularFixture {
                path: PathBuf::from("/dev/null"),
                scratch: Some(scratch),
            };
        }
        NonRegularPath::Missing => {}
    }
    NonRegularFixture {
        path,
        scratch: Some(scratch),
    }
}

/// Run one open off-thread and fail the test, rather than hang the suite, if
/// it does not return promptly. A writerless FIFO open without `O_NONBLOCK`
/// never returns, so this bound is what turns a lost flag into a red test.
/// The bound is far above the cost of one `open(2)` plus `fstat(2)`.
#[cfg(unix)]
pub(crate) fn open_is_prompt<T>(label: &str, open: impl FnOnce() -> T + Send + 'static) -> T
where
    T: Send + 'static,
{
    use std::sync::mpsc;
    use std::thread;
    use std::time::Duration;

    const PROMPT_BOUND: Duration = Duration::from_secs(5);

    let (sender, receiver) = mpsc::channel();
    thread::Builder::new()
        .name(format!("iprange-open-pin-{label}"))
        .spawn(move || {
            let _ = sender.send(open());
        })
        .expect("spawn the pinned open");
    receiver
        .recv_timeout(PROMPT_BOUND)
        .unwrap_or_else(|_| panic!("{label}: the open did not return within {PROMPT_BOUND:?}"))
}

pub(crate) fn copy_pages<'a, T>(
    pages: &'a mut [[u8; PAGE_SIZE]],
    source: u32,
    destination: u32,
    copy: impl FnOnce(&'a [u8; PAGE_SIZE], &'a mut [u8; PAGE_SIZE]) -> Result<T>,
) -> Result<T> {
    let source = source as usize;
    let destination = destination as usize;
    if source == destination || source >= pages.len() || destination >= pages.len() {
        return Err(Error::Corrupt("test copy pages are invalid"));
    }
    let (source_page, destination_page) = if source < destination {
        let (left, right) = pages.split_at_mut(destination);
        (&left[source], &mut right[0])
    } else {
        let (left, right) = pages.split_at_mut(source);
        (&right[0], &mut left[destination])
    };
    copy(source_page, destination_page)
}
