//! Authoritative-descriptor opens for caller-supplied input paths.
//!
//! Every caller-supplied path is inspected before it is opened, and the
//! inspection is a separate syscall from the open: a writer can replace the
//! inspected regular file with a FIFO in between. A plain `open(2)` on a
//! FIFO with no writer waits for a writer forever, which blocks the single
//! JSON-RPC request thread and stops the whole session. The opens here ask
//! the kernel never to block in `open(2)` and then classify the descriptor
//! that was actually opened, so a swapped-in FIFO is refused with the same
//! arm-exact class as a FIFO caught by the pre-check.
//!
//! `O_NOFOLLOW` is deliberately not applied to these arms: a symlinked
//! regular input is accepted by the released surface, and refusing symlinks
//! here would change an established refusal class. The iprange-livedb
//! opens that bind a database through a directory handle
//! (`database_file::open_read_only`, `publication::namespace::unix`) own
//! the no-follow policy; nothing in this module reads or writes persistent
//! database bytes.

use std::fs::{File, OpenOptions};
use std::io;
use std::path::Path;

/// Open `path` read-only without ever blocking in `open(2)`, returning
/// `None` when the opened descriptor is not a regular file.
///
/// A caller that receives `None` must refuse the path with the class it
/// already uses for a non-regular path: reading such a descriptor is never
/// valid, and a FIFO read would wait for a writer.
#[cfg(unix)]
pub(crate) fn open_regular(path: &Path) -> io::Result<Option<File>> {
    use std::os::unix::fs::OpenOptionsExt;

    let mut options = OpenOptions::new();
    options.read(true);
    // Regular files ignore O_NONBLOCK, so this flag only changes what
    // happens on a node type that would otherwise block the open.
    options.custom_flags(libc::O_NONBLOCK);
    let file = options.open(path)?;
    // The decision is taken on the opened descriptor, never on a second
    // path lookup: the descriptor is the object whose bytes would be read.
    if !file.metadata()?.file_type().is_file() {
        return Ok(None);
    }
    Ok(Some(file))
}

/// Open `path` read-only with this platform's released buffered-open
/// behaviour. Windows has no FIFO node type and its `open` cannot wait for
/// a writer, so the caller's path pre-check stays the only regular-file
/// gate, exactly as before.
#[cfg(not(unix))]
pub(crate) fn open_regular(path: &Path) -> io::Result<Option<File>> {
    OpenOptions::new().read(true).open(path).map(Some)
}

/// Test-only helpers that keep the non-regular pins prompt.
///
/// Each pin places a writerless FIFO and calls one arm's open conversion
/// helper directly, bypassing that arm's path pre-check, so the helper's
/// own `O_NONBLOCK` open and descriptor check are what is exercised. The
/// open runs on a separate thread with a bounded join: if `O_NONBLOCK` is
/// ever dropped the assertion fails instead of hanging the suite.
#[cfg(all(test, unix))]
pub(crate) mod pin_support {
    use std::ffi::CString;
    use std::io;
    use std::os::unix::ffi::OsStrExt;
    use std::path::{Path, PathBuf};
    use std::sync::mpsc;
    use std::thread;
    use std::time::Duration;

    /// How long an arm's open may take before the pin calls it a wedge.
    /// A writerless FIFO open without `O_NONBLOCK` never returns, so this
    /// bound only has to be far above the cost of one `open(2)` plus `fstat`.
    const PROMPT_BOUND: Duration = Duration::from_secs(5);

    pub(crate) fn scratch_dir(label: &str) -> PathBuf {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after the unix epoch")
            .as_nanos();
        let dir = std::env::temp_dir().join(format!(
            "iprange-caller-open-{label}-{}-{nanos}",
            std::process::id()
        ));
        std::fs::create_dir_all(&dir).expect("create the pin scratch directory");
        dir
    }

    pub(crate) fn remove_dir(dir: &Path) {
        let _ = std::fs::remove_dir_all(dir);
    }

    /// Place a FIFO that no writer has opened at `path`.
    pub(crate) fn mkfifo(path: &Path) {
        let name = CString::new(path.as_os_str().as_bytes())
            .expect("fifo path must be valid filesystem bytes");
        let rc = unsafe { libc::mkfifo(name.as_ptr(), 0o600) };
        assert_eq!(rc, 0, "mkfifo {path:?}: {}", io::Error::last_os_error());
    }

    /// Run one open off-thread and fail, rather than hang, if it does not
    /// return promptly.
    pub(crate) fn prompt<T>(label: &str, open: impl FnOnce() -> T + Send + 'static) -> T
    where
        T: Send + 'static,
    {
        let (sender, receiver) = mpsc::channel();
        thread::Builder::new()
            .name(format!("iprange-open-pin-{label}"))
            .spawn(move || {
                let _ = sender.send(open());
            })
            .expect("spawn the pinned open");
        receiver.recv_timeout(PROMPT_BOUND).unwrap_or_else(|_| {
            panic!("{label}: the open did not return within {PROMPT_BOUND:?} (lost O_NONBLOCK?)")
        })
    }
}
