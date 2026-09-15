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
//!
//! On Windows there is no FIFO node type and no `O_NONBLOCK`, so the plain
//! read open stays the canonical behaviour, but the handle that open returned
//! is still judged before it is handed back (see `opened_is_regular`). The
//! accepted set is the one the Go owners accept on the same handle
//! (`internal/cli/fileio.OpenedRegular` and its handler counterparts judge
//! `file.Stat().Mode().IsRegular()` of the opened handle), so a directory,
//! named pipe, character device, or an un-followed reparse point is refused
//! with the caller's non-regular class on both engines.

use std::fs::{File, OpenOptions};
use std::io;
use std::path::Path;

/// Test-only witness of the descriptor judgment, compiled out of every
/// production build (the project's test-only observability rule).
///
/// A swap race proves that the *opened descriptor* decided an outcome only if
/// it can tell the descriptor judgment apart from the caller's own pre-check,
/// and the two deliberately share one refusal class and one message: a
/// pre-check that saw the swapped-in FIFO answers exactly like the descriptor
/// judgment does. This witness separates them by recording, for the arm thread
/// of one attempt, that `open_regular` was asked about the raced path and
/// whether it refused that descriptor.
///
/// It also makes the pins absolute rather than statistical: a caller that opens
/// the path itself can never produce a watched judgment, so it fails on the
/// first attempt that reads the file, whether or not the swap ever lands in the
/// window between the pre-check and the open.
#[cfg(all(test, unix))]
mod judge_witness {
    use std::cell::{Cell, RefCell};
    use std::path::{Path, PathBuf};

    thread_local! {
        /// The path this thread is racing, when any. Only a race arm thread
        /// sets it, so unrelated tests cannot influence a verdict.
        static WATCHED: RefCell<Option<PathBuf>> = const { RefCell::new(None) };
        /// Calls of `open_regular` for the watched path on this thread.
        static CALLS: Cell<u32> = const { Cell::new(0) };
        /// Calls of `open_regular` for the watched path that answered
        /// "not a regular file".
        static REFUSALS: Cell<u32> = const { Cell::new(0) };
    }

    /// What the witness saw during one arm run.
    pub(super) struct Judgment {
        pub calls: u32,
        pub refusals: u32,
    }

    /// Start watching `path` on the current thread and clear the counters.
    pub(super) fn begin(path: &Path) {
        WATCHED.with(|watched| *watched.borrow_mut() = Some(path.to_path_buf()));
        CALLS.with(|calls| calls.set(0));
        REFUSALS.with(|refusals| refusals.set(0));
    }

    /// Stop watching and report what the arm's run produced.
    pub(super) fn finish() -> Judgment {
        let judgment = Judgment {
            calls: CALLS.with(|calls| calls.get()),
            refusals: REFUSALS.with(|refusals| refusals.get()),
        };
        WATCHED.with(|watched| *watched.borrow_mut() = None);
        judgment
    }

    /// Record that `open_regular` judged `path`, and whether it refused it.
    /// Unwatched paths are ignored, so this costs nothing outside a race arm.
    pub(super) fn note(path: &Path, refused: bool) {
        if !WATCHED.with(|watched| watched.borrow().as_deref() == Some(path)) {
            return;
        }
        CALLS.with(|calls| calls.set(calls.get() + 1));
        if refused {
            REFUSALS.with(|refusals| refusals.set(refusals.get() + 1));
        }
    }
}

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
    let regular = file.metadata()?.file_type().is_file();
    #[cfg(all(test, unix))]
    judge_witness::note(path, !regular);
    if !regular {
        return Ok(None);
    }
    Ok(Some(file))
}

/// `ERROR_ACCESS_DENIED`: the failure `CreateFileW` returns when a read open
/// names a directory, because a directory handle needs backup semantics.
#[cfg(windows)]
const WINDOWS_ERROR_ACCESS_DENIED: i32 = 5;

/// Open `path` read-only and return it only when the handle that was actually
/// opened is a regular file.
///
/// Windows has no POSIX FIFO open-blocking hazard, so no open flag is needed;
/// what the platform still needs is a decision taken on the opened handle
/// rather than on the earlier path `stat`, which a writer can already have
/// made stale. A directory cannot be opened for reading at all without backup
/// semantics, so the `ERROR_ACCESS_DENIED` that the plain open returns for one
/// is classified through a handle that requests no access right at all: giving
/// the caller its non-regular refusal must not require a flag that could also
/// bypass the read permissions of a real file.
#[cfg(windows)]
pub(crate) fn open_regular(path: &Path) -> io::Result<Option<File>> {
    let file = match OpenOptions::new().read(true).open(path) {
        Ok(file) => file,
        Err(error) if error.raw_os_error() == Some(WINDOWS_ERROR_ACCESS_DENIED) => {
            return Ok(match opened_directory_without_backup_semantics(path) {
                // A directory (or any other non-regular handle) the plain open
                // could not return is the caller's non-regular refusal.
                Some(true) => None,
                // A regular file that merely denied the read, or a path that the
                // metadata-only open could not reach either: keep the original
                // error so the caller maps `NotFound` and the I/O class as before.
                Some(false) | None => return Err(error),
            });
        }
        Err(error) => return Err(error),
    };
    let regular = opened_is_regular(&file)?;
    Ok(if regular { Some(file) } else { None })
}

/// Open `path` with no access rights at all and report whether the node is not
/// a regular file. `None` means the probe itself could not classify the path,
/// which the caller must treat as "no new information".
///
/// `CreateFileW` is called directly because `OpenOptions` cannot express a
/// zero-access open: with no read, write, or append access it fails in user
/// mode, before any syscall, with `InvalidInput` ("must specify at least one
/// of read, write, or append access"), so a probe built on it would classify
/// nothing and every `ERROR_ACCESS_DENIED` read open would keep its I/O error.
///
/// `dwFlagsAndAttributes` deliberately stays free of
/// `FILE_FLAG_BACKUP_SEMANTICS`, which is what keeps the probe honest.
/// `CreateFileW` opens a directory only when that flag turns off the
/// `FILE_NON_DIRECTORY_FILE` create option it otherwise passes down, so
/// without it a directory name is refused with `ERROR_ACCESS_DENIED` whatever
/// access is requested, and the probe can never hold a handle that bypasses
/// the read permissions the caller does not have. The three outcomes are:
///
/// - the zero-access open succeeded: the object is not a directory, and the
///   attributes of the handle that was actually opened decide the answer
///   through `opened_is_regular`, the same classifier the content handle gets;
/// - the open was refused with `ERROR_ACCESS_DENIED`: a name-based attribute
///   query (`GetFileAttributesW`, which needs no handle and requests no access
///   right) separates a directory, which is the caller's non-regular refusal,
///   from a regular file whose read the security descriptor denied, which
///   keeps its original error;
/// - any other failure, or an attribute query that cannot reach the name:
///   `None`, so the caller keeps its original error.
///
/// The name-based query cannot widen what the caller accepts: it runs only
/// after the read open already failed, and it only ever chooses between two
/// refusals (non-regular and I/O error), never between a refusal and a read.
#[cfg(windows)]
fn opened_directory_without_backup_semantics(path: &Path) -> Option<bool> {
    use std::os::windows::ffi::OsStrExt;
    use std::os::windows::io::{FromRawHandle, RawHandle};
    use windows_sys::Win32::Foundation::{GetLastError, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::Storage::FileSystem::{
        CreateFileW, GetFileAttributesW, FILE_ATTRIBUTE_DIRECTORY, FILE_SHARE_DELETE,
        FILE_SHARE_READ, FILE_SHARE_WRITE, INVALID_FILE_ATTRIBUTES, OPEN_EXISTING,
    };

    let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
    // `FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE` is the share
    // mode `OpenOptions` uses for the content open, so the probe cannot add a
    // sharing conflict for any other holder of the same file. Zero access
    // rights cannot conflict with an existing share mode at all.
    let handle = unsafe {
        CreateFileW(
            wide.as_ptr(),
            0,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            std::ptr::null(),
            OPEN_EXISTING,
            0,
            std::ptr::null_mut(),
        )
    };
    if handle == INVALID_HANDLE_VALUE {
        if unsafe { GetLastError() } != WINDOWS_ERROR_ACCESS_DENIED as u32 {
            return None;
        }
        let attributes = unsafe { GetFileAttributesW(wide.as_ptr()) };
        if attributes == INVALID_FILE_ATTRIBUTES {
            return None;
        }
        return Some(attributes & FILE_ATTRIBUTE_DIRECTORY != 0);
    }
    // `File` owns the probe handle, so no exit path, including a classifier
    // error, can leak it.
    let probe = unsafe { File::from_raw_handle(handle as RawHandle) };
    Some(!opened_is_regular(&probe).ok()?)
}

/// Decide, from the opened handle alone, whether it refers to a regular file.
///
/// `GetFileType` separates the name-based object classes a read handle can
/// reach (disk, character device, named pipe) and `GetFileInformationByHandle`
/// supplies the attributes; the reparse tag is not part of that structure, so
/// a reparse point gets its tag from `FileAttributeTagInfo`, exactly as Go's
/// `os` package does when it computes the mode of an opened handle. Only the
/// Data Deduplication tag stays regular: a deduplicated file still supports
/// plain random-access reads, while every other reparse point is a surrogate
/// for something that is not a plain file.
#[cfg(windows)]
fn opened_is_regular(file: &File) -> io::Result<bool> {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Foundation::{GetLastError, SetLastError};
    use windows_sys::Win32::Storage::FileSystem::{
        FileAttributeTagInfo, GetFileInformationByHandle, GetFileInformationByHandleEx,
        GetFileType, BY_HANDLE_FILE_INFORMATION, FILE_ATTRIBUTE_DIRECTORY,
        FILE_ATTRIBUTE_REPARSE_POINT, FILE_ATTRIBUTE_TAG_INFO, FILE_TYPE_CHAR, FILE_TYPE_DISK,
        FILE_TYPE_PIPE,
    };

    let handle = file.as_raw_handle();
    // GetFileType reports FILE_TYPE_UNKNOWN for an unclassifiable handle and
    // for a failed call; the API contract is to clear the last error first so
    // the two can be told apart.
    unsafe { SetLastError(0) };
    let kind = unsafe { GetFileType(handle) };
    if kind != FILE_TYPE_DISK {
        if kind == FILE_TYPE_CHAR || kind == FILE_TYPE_PIPE {
            return Ok(false);
        }
        let code = unsafe { GetLastError() };
        if code != 0 {
            return Err(io::Error::from_raw_os_error(code as i32));
        }
        return Ok(false);
    }

    let mut information: BY_HANDLE_FILE_INFORMATION = unsafe { std::mem::zeroed() };
    if unsafe { GetFileInformationByHandle(handle, &mut information) } == 0 {
        return Err(io::Error::last_os_error());
    }
    if information.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY != 0 {
        return Ok(false);
    }
    if information.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        let mut tag: FILE_ATTRIBUTE_TAG_INFO = unsafe { std::mem::zeroed() };
        let queried = unsafe {
            GetFileInformationByHandleEx(
                handle,
                FileAttributeTagInfo,
                &mut tag as *mut FILE_ATTRIBUTE_TAG_INFO as *mut core::ffi::c_void,
                core::mem::size_of::<FILE_ATTRIBUTE_TAG_INFO>() as u32,
            )
        };
        if queried == 0 {
            return Err(io::Error::last_os_error());
        }
        if tag.ReparseTag != WINDOWS_IO_REPARSE_TAG_DEDUP {
            return Ok(false);
        }
    }
    Ok(true)
}

/// `IO_REPARSE_TAG_DEDUP`. It is not exported by the `windows-sys` feature set
/// this workspace pins, and the value is a fixed protocol constant.
#[cfg(windows)]
const WINDOWS_IO_REPARSE_TAG_DEDUP: u32 = 0x8000_0013;

/// Open `path` read-only with this platform's released buffered-open
/// behaviour. Platforms with neither POSIX FIFOs nor the Win32 handle queries
/// keep the caller's path pre-check as the only regular-file gate, exactly as
/// before.
#[cfg(all(not(unix), not(windows)))]
pub(crate) fn open_regular(path: &Path) -> io::Result<Option<File>> {
    let file = OpenOptions::new().read(true).open(path)?;
    let regular = file.metadata()?.file_type().is_file();
    #[cfg(all(test, unix))]
    judge_witness::note(path, !regular);
    Ok(if regular { Some(file) } else { None })
}

/// Test-only fixtures for the non-regular input pins.
///
/// Two facilities live here.
///
/// `prompt()` places a writerless FIFO and calls one arm's open conversion
/// helper directly, bypassing that arm's path pre-check, so the helper's own
/// `O_NONBLOCK` open and descriptor check are what is exercised. The open runs
/// on a separate thread with a bounded join: if `O_NONBLOCK` is ever dropped
/// the assertion fails instead of hanging the suite.
///
/// `swap_race()` drives a registered handler against a path another thread
/// keeps swapping between a regular file and a writerless FIFO, and
/// `control()` runs the same handler against a plain regular file. With
/// `assert_race()` they pin the *callers* of the descriptor judgment: a caller
/// that opens the path itself is convicted twice over, by the empty judge
/// witness in the control and by the wedge it takes in a raced attempt.
///
/// Set `IPRANGE_RACE_DEBUG` to print answers the pins classified as neither a
/// refusal nor an accepted outcome; that is how a vacuous or misclassified
/// fixture is diagnosed.
#[cfg(all(test, unix))]
pub(crate) mod pin_support {
    use std::ffi::CString;
    use std::io;
    use std::os::unix::ffi::OsStrExt;
    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::mpsc;
    use std::sync::Arc;
    use std::thread;
    use std::time::{Duration, Instant};

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

    /// How long one swap-race attempt may take before it is called a wedge.
    /// A FIFO open that waits for a writer never returns, so this bound only
    /// has to be far above the cost of one arm invocation.
    const ATTEMPT_BOUND: Duration = Duration::from_secs(6);

    /// How long one attempt keeps flipping the node under the arm. Every arm
    /// reaches its open within a few milliseconds of starting; the bound only
    /// limits the syscall rate of a wedged attempt.
    const FLIP_BOUND: Duration = Duration::from_millis(250);

    /// The answer a swap race expects from one arm.
    pub(crate) struct RaceRule<'a> {
        /// The refusal class the arm uses for a non-regular path.
        pub code: &'a str,
        /// The message the arm uses for a non-regular path.
        pub message: &'a str,
        /// Messages the arm may legitimately carry instead, because the
        /// fixture is designed to stop the workflow just after the open.
        pub other_accepted: &'a [&'a str],
    }

    /// What one swap-race attempt reported.
    enum Attempt {
        /// The arm did not return within [`ATTEMPT_BOUND`].
        Hung,
        /// The arm's own fixture or expectation failed.
        Panicked(String),
        /// The arm answered, together with what the descriptor-judgment
        /// witness saw while it ran.
        Answered(Option<(String, String)>, super::judge_witness::Judgment),
    }

    /// [`RaceRule`] in owned form, so the workers of one round can share it
    /// across threads.
    struct OwnedRule {
        code: String,
        message: String,
        other_accepted: Vec<String>,
    }

    impl OwnedRule {
        fn new(rule: &RaceRule<'_>) -> Self {
            Self {
                code: rule.code.to_owned(),
                message: rule.message.to_owned(),
                other_accepted: rule
                    .other_accepted
                    .iter()
                    .map(|accepted| (*accepted).to_owned())
                    .collect(),
            }
        }
    }

    /// Collected outcome of a swap race.
    pub(crate) struct RaceReport {
        pub attempts: usize,
        /// Rounds of `attempts` that ran; a round is repeated only while
        /// nothing has decided the pin.
        pub rounds: usize,
        /// Attempts refused by the caller's own pre-check, which saw the
        /// swapped-in FIFO before opening anything. Expected, and no evidence
        /// about what the open would have decided.
        pub precheck_refusals: usize,
        /// Attempts in which `open_regular` itself refused the swapped-in
        /// FIFO: the fact each pin exists to prove.
        pub descriptor_refusals: usize,
        /// Attempts that completed the arm's work (the swap lost the race).
        pub completed: usize,
        /// Attempts that answered with an accepted unrelated outcome.
        pub other: usize,
        /// Attempts that answered something other than a refusal the pre-check
        /// could have produced, without one watched call of the descriptor
        /// judgment: the signature of a caller that opens the path itself.
        pub unjudged: usize,
        /// Attempts that did not return within [`ATTEMPT_BOUND`].
        pub hangs: usize,
        /// Attempts whose arm panicked.
        pub panics: usize,
        /// Answers that broke [`RaceRule`].
        pub violations: Vec<String>,
        /// Accepted answers that matched neither the refusal nor
        /// `other_accepted`, kept so a report can show what the arm said.
        pub unmatched: Vec<String>,
    }

    impl RaceReport {
        fn new() -> Self {
            Self {
                attempts: 0,
                rounds: 0,
                precheck_refusals: 0,
                descriptor_refusals: 0,
                completed: 0,
                other: 0,
                unjudged: 0,
                hangs: 0,
                panics: 0,
                violations: Vec::new(),
                unmatched: Vec::new(),
            }
        }

        /// Whether more attempts can still change the verdict: one attempt
        /// that hung, panicked, answered outside the arm's class, or reached
        /// the raced path unjudged already decides the pin.
        fn decided(&self) -> bool {
            self.hangs + self.panics + self.unjudged > 0 || !self.violations.is_empty()
        }

        fn merge(&mut self, other: RaceReport) {
            self.attempts += other.attempts;
            self.precheck_refusals += other.precheck_refusals;
            self.descriptor_refusals += other.descriptor_refusals;
            self.completed += other.completed;
            self.other += other.other;
            self.unjudged += other.unjudged;
            self.hangs += other.hangs;
            self.panics += other.panics;
            self.violations.extend(other.violations);
            self.violations.truncate(4);
            self.unmatched.extend(other.unmatched);
            self.unmatched.truncate(4);
        }
    }

    /// Rounds of the whole batch to run while no attempt produced the fact the
    /// pin exists to prove. The window between a caller's pre-check and its
    /// open is a few instructions wide, so landing in it stays a coin, biased
    /// toward a hit by [`PRESSURES`] and by an arm that visits the path more
    /// than once; repeating the batch bounds the chance that an unusually
    /// quiet host reports an inconclusive race. Attempts stop as soon as the
    /// verdict is in, so a caller that bypasses the judgment costs one attempt
    /// rather than a batch.
    const ROUNDS: usize = 6;

    /// Threads kept runnable on the arm's core while an attempt runs.
    ///
    /// Whether the swap lands inside the window between the pre-check and the
    /// open depends on the arm being switched out there, which on an idle core
    /// it almost never is: an arm that reads one small file finishes inside a
    /// single scheduling slice. These threads make such switches ordinary, so
    /// the pin measures the caller's design instead of the host's load. They
    /// stop when the arm answers, or at the attempt bound if it never does.
    const PRESSURES: usize = 3;

    /// Drive `arm` `attempts` times against a path that is a regular file when
    /// the arm starts, while another thread swaps that path between the
    /// regular file and a writerless FIFO for as long as the arm runs.
    ///
    /// This is the arm-level pin for the descriptor judgment. Two independent
    /// facts convict a caller that opens the path itself:
    ///
    /// * the judge witness stays empty for it, so no attempt of it can be a
    ///   watched judgment ([`RaceReport::unjudged`]), whatever the swap does;
    /// * the one attempt in which the swap does land in the window blocks
    ///   inside `open(2)` forever ([`RaceReport::hangs`]), while an arm that
    ///   judges the opened descriptor answers with its own non-regular class
    ///   ([`RaceReport::descriptor_refusals`]).
    ///
    /// `arm` receives the caller path and reports its own answer: `None` for
    /// success, `Some((code, message))` for a refusal. It must own its state so
    /// attempts cannot influence each other.
    pub(crate) fn swap_race<A>(
        label: &str,
        content: &'static [u8],
        rule: &RaceRule<'_>,
        attempts: usize,
        concurrency: usize,
        arm: Arc<A>,
    ) -> RaceReport
    where
        A: Fn(PathBuf) -> Option<(String, String)> + Send + Sync + 'static,
    {
        assert!(attempts > 0 && concurrency > 0, "a race needs attempts");
        let rule = Arc::new(OwnedRule::new(rule));
        let mut report = RaceReport::new();
        for round in 0..ROUNDS {
            let round_report =
                race_round(label, round, content, &rule, attempts, concurrency, &arm);
            report.merge(round_report);
            report.rounds += 1;
            if report.descriptor_refusals > 0 || report.decided() {
                break;
            }
        }
        report
    }

    /// Run one batch of attempts across `concurrency` workers. Each worker
    /// classifies its own attempts, so the batch stops as soon as any worker
    /// has the verdict rather than after every attempt of the batch.
    fn race_round<A>(
        label: &str,
        round: usize,
        content: &'static [u8],
        rule: &Arc<OwnedRule>,
        attempts: usize,
        concurrency: usize,
        arm: &Arc<A>,
    ) -> RaceReport
    where
        A: Fn(PathBuf) -> Option<(String, String)> + Send + Sync + 'static,
    {
        let decided = Arc::new(AtomicBool::new(false));
        let mut workers = Vec::with_capacity(concurrency);
        for worker in 0..concurrency {
            let arm = Arc::clone(arm);
            let rule = Arc::clone(rule);
            let decided = Arc::clone(&decided);
            let name = format!("{label}-r{round}-{worker}");
            let spawned = thread::Builder::new()
                .name(format!("iprange-swap-race-{name}"))
                .spawn(move || {
                    let mut local = RaceReport::new();
                    let mut attempt = worker;
                    while attempt < attempts && !decided.load(Ordering::Acquire) {
                        let answer = race_one_attempt(&name, content, &arm, attempt, true);
                        local.attempts += 1;
                        classify(&mut local, answer, &rule);
                        if local.decided() {
                            decided.store(true, Ordering::Release);
                        }
                        attempt += concurrency;
                    }
                    local
                })
                .expect("spawn a swap-race worker");
            workers.push(spawned);
        }
        let mut report = RaceReport::new();
        for worker in workers {
            report.merge(worker.join().expect("swap-race worker panicked"));
        }
        report
    }

    /// Run the arm once on its own thread while this thread flips the node
    /// under it, with [`PRESSURES`] threads kept runnable beside the arm. A
    /// wedged arm's thread is left blocked in the open, which is exactly what
    /// the pin reports; the attempt is bounded by [`ATTEMPT_BOUND`] either way.
    fn race_one_attempt<A>(
        label: &str,
        content: &'static [u8],
        arm: &Arc<A>,
        attempt: usize,
        swap: bool,
    ) -> Attempt
    where
        A: Fn(PathBuf) -> Option<(String, String)> + Send + Sync + 'static,
    {
        let directory = race_dir(&format!("{label}-{attempt}"));
        // The template is never renamed away: each flip hard-links it under a
        // scratch name and moves that name onto the caller path, so the caller
        // path always holds a node and never a missing entry.
        let template = directory.join("template");
        if std::fs::write(&template, content).is_err() {
            remove_dir(&directory);
            return Attempt::Panicked(format!("write the swap template for {label}"));
        }
        let target = directory.join("caller-path");
        if std::fs::hard_link(&template, &target).is_err() {
            remove_dir(&directory);
            return Attempt::Panicked(format!("link the caller path of {label}"));
        }

        let (sender, receiver) = mpsc::channel();
        let go = Arc::new(AtomicBool::new(false));
        let answered = Arc::new(AtomicBool::new(false));
        let arm_go = Arc::clone(&go);
        let arm_answered = Arc::clone(&answered);
        let arm_target = target.to_path_buf();
        let arm_runner = Arc::clone(arm);
        let arm_core = attempt % worker_cores();
        // The handle is dropped on purpose: an arm that never returns from
        // `open(2)` is the fact the pin reports, and nothing may join it.
        let _arm_thread = thread::Builder::new()
            .name(format!("iprange-swap-race-{label}-arm"))
            .spawn(move || {
                pin_thread(arm_core);
                while !arm_go.load(Ordering::Acquire) {
                    thread::yield_now();
                }
                // Watched from the arm thread itself, so the witness attributes
                // the judgment to this attempt alone.
                super::judge_witness::begin(&arm_target);
                // A panicking arm must not strand the race: its absence of an
                // answer is reported as the broken fixture it is.
                let answer = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    arm_runner(arm_target)
                }));
                let judgment = super::judge_witness::finish();
                arm_answered.store(true, Ordering::Release);
                let _ = sender.send((answer, judgment));
            })
            .expect("spawn the raced arm");
        // The control run leaves the node alone; only a raced attempt needs
        // the threads that keep the arm's core busy.
        let pressers = if swap {
            start_pressures(label, attempt, &go, &answered)
        } else {
            Vec::new()
        };
        go.store(true, Ordering::Release);

        let fifo_source = directory.join("fifo-source");
        let regular_source = directory.join("regular-source");
        let started = Instant::now();
        let mut want_fifo = true;
        while swap && !answered.load(Ordering::Acquire) && started.elapsed() < FLIP_BOUND {
            if want_fifo {
                flip_to_fifo(&fifo_source, &target);
            } else {
                flip_to_regular(&template, &regular_source, &target);
            }
            want_fifo = !want_fifo;
        }
        // Restore a readable node whatever the last flip left, so an arm still
        // between its pre-check and its open sees a stable node, and so a
        // wedged attempt cannot wedge the cleanup of its own scratch directory.
        flip_to_regular(&template, &regular_source, &target);

        let answer = match receiver.recv_timeout(ATTEMPT_BOUND) {
            Ok((answer, judgment)) => match answer {
                Ok(answer) => Attempt::Answered(answer, judgment),
                Err(payload) => Attempt::Panicked(format!("{label}: {}", panic_detail(&payload))),
            },
            Err(_) => Attempt::Hung,
        };
        for presser in pressers {
            let _ = presser.join();
        }
        remove_dir(&directory);
        answer
    }

    /// The panic message of a caught arm, however the payload was carried.
    fn panic_detail(payload: &Box<dyn std::any::Any + Send>) -> String {
        payload
            .downcast_ref::<String>()
            .cloned()
            .or_else(|| {
                payload
                    .downcast_ref::<&str>()
                    .map(|text| (*text).to_owned())
            })
            .unwrap_or_else(|| "panicked without a message".to_owned())
    }

    /// Spawn the arm-core pressure threads. They wait for the arm to be
    /// released, keep it company on its core, and stop when it answers or when
    /// the attempt bound passes, so a wedged arm does not wedge the pressure
    /// threads too.
    fn start_pressures(
        label: &str,
        attempt: usize,
        go: &Arc<AtomicBool>,
        answered: &Arc<AtomicBool>,
    ) -> Vec<thread::JoinHandle<()>> {
        let cores = worker_cores();
        let deadline = Instant::now() + ATTEMPT_BOUND;
        (0..PRESSURES)
            .map(|index| {
                let go = Arc::clone(go);
                let answered = Arc::clone(answered);
                let name = format!("iprange-swap-race-{label}-press-{attempt}-{index}");
                thread::Builder::new()
                    .name(name)
                    .spawn(move || {
                        pin_thread(attempt % cores);
                        while !go.load(Ordering::Acquire) && Instant::now() < deadline {
                            thread::yield_now();
                        }
                        let mut ticks: u64 = 0;
                        while !answered.load(Ordering::Relaxed) && Instant::now() < deadline {
                            ticks = ticks.wrapping_add(1);
                            std::hint::black_box(ticks);
                            if ticks % 32 == 0 {
                                thread::yield_now();
                            }
                        }
                    })
                    .expect("spawn an arm-core pressure thread")
            })
            .collect()
    }

    /// Cores available to the suite; one, so the fixtures stay meaningful, when
    /// the host will not say.
    fn worker_cores() -> usize {
        thread::available_parallelism().map_or(1, |count| count.get().max(1))
    }

    /// Keep a race thread on one core, so the context switches the swap window
    /// needs are a property of the fixture rather than of how busy the rest of
    /// the host happens to be. Where affinity is unavailable the race still
    /// runs, just with the ambient switch rate.
    fn pin_thread(core: usize) {
        #[cfg(target_os = "linux")]
        unsafe {
            let mut set: libc::cpu_set_t = std::mem::zeroed();
            libc::CPU_SET(core % usize::from(u16::MAX), &mut set);
            libc::sched_setaffinity(0, std::mem::size_of::<libc::cpu_set_t>(), &set);
        }
        #[cfg(not(target_os = "linux"))]
        let _ = core;
    }

    /// The scratch root of a race.
    ///
    /// The arms publish to a destination inside their own attempt directory, so
    /// that directory must sit on a filesystem the durable namespace layer
    /// accepts (`publication::namespace::unix::require_local_filesystem` admits
    /// ext2/XFS/btrfs/f2fs/zfs/bcachefs and refuses tmpfs). A memory-backed
    /// scratch would make every arm fail its publication before it reached the
    /// raced path, which is the vacuous fixture these pins must not be.
    fn race_root() -> PathBuf {
        std::env::temp_dir()
    }

    fn race_dir(label: &str) -> PathBuf {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after the unix epoch")
            .as_nanos();
        let dir = race_root().join(format!(
            "iprange-caller-race-{label}-{}-{nanos}",
            std::process::id()
        ));
        std::fs::create_dir_all(&dir).expect("create the race scratch directory");
        dir
    }

    /// Move a writerless FIFO onto `target`. A failed flip is not a defect:
    /// the arm may see either node at either of its two syscalls, and the next
    /// cycle retries.
    fn flip_to_fifo(source: &Path, target: &Path) {
        if create_fifo(source) || (remove_quietly(source) && create_fifo(source)) {
            let _ = std::fs::rename(source, target);
        }
    }

    fn flip_to_regular(template: &Path, source: &Path, target: &Path) {
        if std::fs::hard_link(template, source).is_ok()
            || (remove_quietly(source) && std::fs::hard_link(template, source).is_ok())
        {
            let _ = std::fs::rename(source, target);
        }
    }

    fn create_fifo(path: &Path) -> bool {
        let name = match CString::new(path.as_os_str().as_bytes()) {
            Ok(name) => name,
            Err(_) => return false,
        };
        unsafe { libc::mkfifo(name.as_ptr(), 0o600) == 0 }
    }

    fn remove_quietly(path: &Path) -> bool {
        match std::fs::remove_file(path) {
            Ok(()) => true,
            Err(error) => error.kind() == io::ErrorKind::NotFound,
        }
    }

    fn classify(report: &mut RaceReport, answer: Attempt, rule: &OwnedRule) {
        let Attempt::Answered(answer, judgment) = answer else {
            match answer {
                Attempt::Hung => report.hangs += 1,
                Attempt::Panicked(detail) => {
                    report.panics += 1;
                    if report.violations.len() < 4 {
                        report.violations.push(detail);
                    }
                }
                Attempt::Answered(_, _) => unreachable!("handled above"),
            }
            return;
        };
        let Some((code, message)) = answer else {
            report.completed += 1;
            if judgment.calls == 0 {
                report.unjudged += 1;
            }
            return;
        };
        if message.contains(&rule.message) {
            if code != rule.code {
                report.violations.push(format!(
                    "the swapped-in fifo got class {code:?}, expected {:?}: {message}",
                    rule.code
                ));
            }
            // Only a judgment the witness saw counts: the pre-check refuses
            // with this same class and message, and it proves nothing about
            // what the opened descriptor would have decided.
            if judgment.refusals > 0 {
                report.descriptor_refusals += 1;
            } else {
                report.precheck_refusals += 1;
            }
            return;
        }
        if rule
            .other_accepted
            .iter()
            .any(|accepted| message.contains(accepted.as_str()))
        {
            report.other += 1;
            if judgment.calls == 0 {
                report.unjudged += 1;
            }
            return;
        }
        if message.contains("caller-path") {
            // An answer about the swapped path that is neither the arm's
            // non-regular refusal nor a declared post-open outcome means the
            // swap was judged somewhere other than the opened descriptor.
            report.violations.push(format!(
                "unexpected answer about the swapped path: {code}: {message}"
            ));
            return;
        }
        report.other += 1;
        if judgment.calls == 0 {
            report.unjudged += 1;
        }
        if report.unmatched.len() < 4 {
            report.unmatched.push(format!("{code}: {message}"));
            if std::env::var_os("IPRANGE_RACE_DEBUG").is_some() {
                eprintln!(
                    "race unmatched answer (calls={}): {code}: {message}",
                    judgment.calls
                );
            }
        }
    }

    /// Run the arm once against a plain regular file, with no swap and no
    /// pressure threads, and return what it answered and what the witness saw.
    ///
    /// This is the control that makes a race meaningful, and it is the half of
    /// the pin that does not depend on timing at all: an arm that reads the
    /// fixture must have asked the descriptor judgment about it, so a caller
    /// that opens the path itself fails here on the first attempt whatever the
    /// swap does later.
    pub(crate) fn control<A>(label: &str, content: &'static [u8], arm: &Arc<A>) -> Control
    where
        A: Fn(PathBuf) -> Option<(String, String)> + Send + Sync + 'static,
    {
        match race_one_attempt(label, content, arm, 0, false) {
            Attempt::Answered(answer, judgment) => Control {
                answered: true,
                answer,
                calls: judgment.calls,
                refusals: judgment.refusals,
            },
            Attempt::Hung => Control {
                answered: false,
                answer: None,
                calls: 0,
                refusals: 0,
            },
            Attempt::Panicked(detail) => panic!("control {label}: the arm panicked: {detail}"),
        }
    }

    /// What the unswapped control of one arm reported.
    pub(crate) struct Control {
        answered: bool,
        answer: Option<(String, String)>,
        calls: u32,
        refusals: u32,
    }

    /// Assert the control: the arm reads a plain regular file, refuses nothing,
    /// and does so through the descriptor judgment. A hung or refusing control
    /// means the fixture itself is broken, so no race result can be trusted.
    pub(crate) fn assert_control(arm: &str, control: &Control) {
        assert!(
            control.answered,
            "{arm}: the control did not return against a plain regular file"
        );
        assert_eq!(
            control.refusals, 0,
            "{arm}: the control refused a plain regular file with {:?}",
            control.answer
        );
        assert!(
            control.calls > 0,
            "{arm}: the control read the fixture without one watched call of the descriptor \
             judgment, so the caller opens the path itself and a fifo swapped in later would \
             wedge the request thread inside open(2)"
        );
    }

    /// The contract every arm-level swap race enforces, so each arm states its
    /// rule and nothing else:
    ///
    /// * the caller must have asked the descriptor judgment about the raced
    ///   path in every attempt that read or reported it, which is what a bare
    ///   open cannot survive no matter how the swap lands;
    /// * no attempt may wedge (the detection a bare open also earns whenever
    ///   the swap does land in the window);
    /// * the swap must reach the judgment at least once, so the pin cannot be
    ///   satisfied by the caller's pre-check alone;
    /// * the arm must also run against a plain regular file at least once, so
    ///   the refusals cannot be an artefact of a fixture that never opened;
    /// * and every answer must use the arm's own class.
    pub(crate) fn assert_race(arm: &str, report: &RaceReport, attempts: usize) {
        assert!(
            report.attempts > 0 && report.attempts <= attempts * report.rounds.max(1),
            "{arm}: the race ran {a}/{b} attempts over {r} round(s): \
             {h} hung, {p} panicked, {u} unjudged, {v} violation(s) [{vl}], {m} unmatched [{ml}]",
            a = report.attempts,
            b = attempts,
            r = report.rounds,
            h = report.hangs,
            p = report.panics,
            u = report.unjudged,
            v = report.violations.len(),
            vl = report.violations.join(" | "),
            m = report.unmatched.len(),
            ml = report.unmatched.join(" | "),
        );
        assert_eq!(
            report.panics,
            0,
            "{arm}: {} attempts panicked: {}",
            report.panics,
            report.violations.join(" | ")
        );
        assert_eq!(
            report.unjudged,
            0,
            "{arm}: {} of {} attempts reached the raced path without one watched \
             call of the descriptor judgment; the caller opened that path itself, \
             so a fifo swapped in after its pre-check would wedge the request \
             thread inside open(2) ({} completed, {} other, {} pre-check refusals)",
            report.unjudged,
            report.attempts,
            report.completed,
            report.other,
            report.precheck_refusals
        );
        assert_eq!(
            report.hangs, 0,
            "{arm}: {} of {} attempts did not return within the bound; the \
             caller opened a path without judging the descriptor it opened",
            report.hangs, report.attempts
        );
        assert!(
            report.descriptor_refusals > 0,
            "{arm}: no attempt met the swapped-in fifo at the judgment itself over {} \
             attempts in {} round(s), so the arm may be judging a second path lookup \
             instead of the descriptor it opened ({} pre-check refusals, {} completed, \
             {} other)",
            report.attempts,
            report.rounds,
            report.precheck_refusals,
            report.completed,
            report.other
        );
        assert!(
            report.violations.is_empty(),
            "{arm}: {} attempts answered outside the arm's class: {}{}",
            report.violations.len(),
            report.violations.join(" | "),
            if report.unmatched.is_empty() {
                String::new()
            } else {
                format!(" (also accepted: {})", report.unmatched.join(" | "))
            }
        );
    }
}
/// Test-only judgment pins for the Windows handle classifier. They exercise
/// the attribute and reparse-tag decision without a Windows host: the native
/// run of these pins happens on the authorized Windows validation host.
#[cfg(all(test, windows))]
mod windows_judgment_tests {
    use super::opened_is_regular;
    use std::fs::{File, OpenOptions};
    use std::os::windows::fs::symlink_file;

    fn write_file(path: &std::path::Path, bytes: &[u8]) {
        std::fs::write(path, bytes).expect("write the fixture");
    }

    fn scratch_dir(label: &str) -> std::path::PathBuf {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after the unix epoch")
            .as_nanos();
        let dir = std::env::temp_dir().join(format!(
            "iprange-caller-open-win-{label}-{}-{unique}",
            std::process::id()
        ));
        std::fs::create_dir_all(&dir).expect("create the pin scratch directory");
        dir
    }

    fn open_regular_fixture(path: &std::path::Path) -> File {
        OpenOptions::new()
            .read(true)
            .open(path)
            .expect("open the regular fixture")
    }

    /// A regular file is accepted, including one with the attributes a normal
    /// Windows deployment carries (read-only, hidden, system, archive).
    #[test]
    fn regular_handle_is_accepted() {
        let directory = scratch_dir("regular");
        let path = directory.join("input.txt");
        write_file(&path, b"10.0.0.1\n");
        let file = open_regular_fixture(&path);
        assert!(opened_is_regular(&file).expect("judge the regular handle"));
        drop(file);
        std::fs::remove_dir_all(&directory).expect("clean the fixture");
    }

    /// A directory has no read handle at all, so the caller-visible answer is
    /// produced by the metadata-only probe: not a regular file, rather than an
    /// I/O error. Both the probe and the entry point that consumes it are
    /// pinned, because the refusal the caller sees is produced by the arm in
    /// `open_regular`, not by the probe alone.
    #[test]
    fn directory_is_refused_as_not_regular() {
        let directory = scratch_dir("directory");
        let nested = directory.join("nested");
        std::fs::create_dir(&nested).expect("create the nested directory");
        let error = OpenOptions::new()
            .read(true)
            .open(&nested)
            .expect_err("a directory must not open for reading without backup semantics");
        assert_eq!(
            error.raw_os_error(),
            Some(super::WINDOWS_ERROR_ACCESS_DENIED),
            "unexpected failure for a directory read open: {error}"
        );
        assert_eq!(
            super::opened_directory_without_backup_semantics(&nested),
            Some(true),
            "the metadata-only probe must classify the directory as non-regular"
        );
        assert!(
            super::open_regular(&nested)
                .expect("the directory refusal must not surface as an I/O error")
                .is_none(),
            "the ERROR_ACCESS_DENIED arm must answer with the caller's \
             non-regular refusal"
        );
        std::fs::remove_dir_all(&directory).expect("clean the fixture");
    }

    /// Report one Windows pin as unscored because its fixture is not
    /// available on this host, naming the condition.
    ///
    /// A silent `return` here would let a run report a pin as passed without
    /// having judged anything, which is how an owner under test can rot: the
    /// status is therefore only accepted when the caller asked for a tally
    /// through `IPRANGE_V4_WIN_PIN_TALLY`, and the run fails when the fixture
    /// is unavailable and nobody asked to record it. The two pins below are
    /// the only users, so the helper sits with them.
    fn record_unsupported(pin: &str, reason: &str) {
        match std::env::var_os("IPRANGE_V4_WIN_PIN_TALLY") {
            Some(tally) => {
                use std::io::Write;
                let mut sink = std::fs::OpenOptions::new()
                    .create(true)
                    .append(true)
                    .open(&tally)
                    .unwrap_or_else(|error| {
                        panic!("{pin}: cannot record the unscored status in {tally:?}: {error}")
                    });
                writeln!(sink, "UNSCORED {pin}: {reason}")
                    .unwrap_or_else(|error| panic!("{pin}: cannot record the status: {error}"));
            }
            None => panic!(
                "{pin}: {reason}; the fixture is unavailable and no tally destination is \
                 configured, so the pin fails rather than passing unverified (set \
                 IPRANGE_V4_WIN_PIN_TALLY=<file> to record it as UNSCORED)"
            ),
        }
    }

    /// A character device is refused through the opened handle, exactly as the
    /// Go owners refuse it from `file.Stat().Mode().IsRegular()` of the handle
    /// they opened.
    ///
    /// The device is probed twice: once without the product path, to learn
    /// whether this host can open `\\.\NUL` at all, and once through
    /// `open_regular`, whose answer is the product behaviour under test. An
    /// error from the product path is therefore never an environment excuse.
    /// When the host cannot open the device, the pin is reported unscored with
    /// the named condition and only then returns: an unrecorded skip fails, so
    /// a run cannot report this pin as passed without having judged anything.
    #[test]
    fn character_device_is_refused_as_not_regular() {
        let nul = std::path::Path::new(r"\\.\NUL");
        match OpenOptions::new().read(true).open(nul) {
            Ok(handle) => drop(handle),
            Err(probe)
                if matches!(
                    probe.kind(),
                    std::io::ErrorKind::NotFound | std::io::ErrorKind::PermissionDenied
                ) =>
            {
                record_unsupported(
                    "character_device_is_refused_as_not_regular",
                    &format!(r"the host cannot open {nul:?} ({probe})"),
                );
                return;
            }
            Err(probe) => panic!(
                "unexpected failure probing {nul:?}: {probe}; only a missing or \
                 access-denied device may leave this pin unscored"
            ),
        }
        // The probe above proved the device is openable here, so an error from
        // the product path is a product answer, never an environment excuse.
        let opened = super::open_regular(nul).unwrap_or_else(|error| {
            panic!("the product must refuse {nul:?} as non-regular, not fail on it: {error}")
        });
        assert!(
            opened.is_none(),
            "a character device must be refused as non-regular, not read"
        );
    }

    /// A symlinked regular input stays accepted: these arms deliberately do not
    /// apply `O_NOFOLLOW` semantics, so `CreateFileW` resolves the link and the
    /// handle carries the target's attributes, not the link's reparse point.
    ///
    /// Creating a symlink needs the symbolic-link privilege (developer mode)
    /// and a reparse-capable volume, so the pin reports the exact missing
    /// condition instead of returning quietly. An unrecorded skip fails.
    #[test]
    fn symlinked_regular_input_is_accepted() {
        // ERROR_PRIVILEGE_NOT_HELD: the account lacks SeCreateSymbolicLinkPrivilege.
        // ERROR_INVALID_FUNCTION / ERROR_NOT_SUPPORTED: the volume cannot hold a
        // reparse point. Anything else is not a platform capability this pin
        // may treat as absent.
        const MISSING_CAPABILITY: [i32; 3] = [1314, 1, 50];
        let directory = scratch_dir("symlink");
        let target = directory.join("target.txt");
        let link = directory.join("link.txt");
        write_file(&target, b"10.0.0.1\n");
        if let Err(error) = symlink_file(&target, &link) {
            std::fs::remove_dir_all(&directory).ok();
            let reason = match error.raw_os_error() {
                Some(code) if MISSING_CAPABILITY.contains(&code) => {
                    format!("the host cannot create a symlink here (os error {code}: {error})")
                }
                _ => panic!(
                    "unexpected symlink failure (os error {:?}: {error}); only a \
                     missing privilege or a reparse-incapable volume may leave this \
                     pin unscored",
                    error.raw_os_error()
                ),
            };
            record_unsupported("symlinked_regular_input_is_accepted", &reason);
            return;
        }
        let file = open_regular_fixture(&link);
        assert!(
            opened_is_regular(&file).expect("judge the followed handle"),
            "a followed symlink must present a regular handle"
        );
        drop(file);
        std::fs::remove_dir_all(&directory).expect("clean the fixture");
    }

    /// A missing path is an error for the caller to classify (the arms map
    /// `NotFound` to their own missing-path refusal); it is never a silent
    /// `None`, which would report a missing file as a non-regular one.
    #[test]
    fn missing_path_is_an_error_not_a_refusal() {
        let directory = scratch_dir("missing");
        let path = directory.join("absent.txt");
        let error = super::open_regular(&path).expect_err("a missing path cannot open");
        assert_eq!(error.kind(), std::io::ErrorKind::NotFound);
        std::fs::remove_dir_all(&directory).ok();
    }

    /// The regular-file answer of the public entry, end to end: `Some(handle)`
    /// for a regular file, so the caller reads the bytes it inspected.
    #[test]
    fn open_regular_returns_the_handle_of_a_regular_file() {
        let directory = scratch_dir("accepted");
        let path = directory.join("input.txt");
        write_file(&path, b"hello");
        let opened = super::open_regular(&path).expect("open the regular input");
        let mut file = opened.expect("a regular input must not be refused");
        let mut bytes = Vec::new();
        std::io::Read::read_to_end(&mut file, &mut bytes).expect("read the fixture");
        assert_eq!(bytes, b"hello");
        drop(file);
        std::fs::remove_dir_all(&directory).expect("clean the fixture");
    }
}

/// Fail-closed shape check for the Windows judgment pins above.
///
/// Those pins are compiled only on Windows, so the host that runs the
/// battery cannot observe whether an unavailable fixture is reported or
/// merely returned. Reading this file is the check available to any host,
/// the same technique `tests/thread_creation_discipline.rs` uses for the
/// thread-creation contract. Reverting either pin to a quiet `return`, or
/// muting the tally requirement inside `record_unsupported`, fails here.
#[cfg(test)]
mod windows_pin_fail_closed_tests {
    const SOURCE: &str = include_str!("caller_open.rs");
    const RECORD: &str = "record_unsupported(";

    /// The text of one function, from its opening brace to the matching
    /// closing brace. Every brace in these bodies is balanced, so a depth
    /// counter is enough and an unbalanced body panics rather than guesses.
    fn body_of(name: &str) -> &str {
        let head = format!("fn {name}(");
        let start = SOURCE
            .find(&head)
            .unwrap_or_else(|| panic!("expected fn {name} in the pinned source"));
        let relative = SOURCE[start..]
            .find('{')
            .unwrap_or_else(|| panic!("expected a body for fn {name}"));
        let open = start + relative;
        let mut depth = 0i32;
        for (offset, byte) in SOURCE[open..].bytes().enumerate() {
            match byte {
                b'{' => depth += 1,
                b'}' => {
                    depth -= 1;
                    if depth == 0 {
                        return &SOURCE[open..open + offset + 1];
                    }
                }
                _ => {}
            }
        }
        panic!("unbalanced braces in fn {name}");
    }

    /// `body` with each sanctioned exit removed: a `record_unsupported(..)`
    /// call and the `return` that may follow it. Any `return` still in the
    /// text is an exit that reports nothing.
    fn without_recorded_exits(body: &str) -> String {
        let mut out = String::new();
        let mut rest = body;
        while let Some(at) = rest.find(RECORD) {
            out.push_str(&rest[..at]);
            let mut tail = &rest[at + RECORD.len()..];
            let mut depth = 1i32;
            let mut closed = None;
            for (offset, byte) in tail.bytes().enumerate() {
                match byte {
                    b'(' => depth += 1,
                    b')' => {
                        depth -= 1;
                        if depth == 0 {
                            closed = Some(offset + 1);
                            break;
                        }
                    }
                    _ => {}
                }
            }
            let closed = closed.unwrap_or_else(|| panic!("unterminated call to {RECORD}"));
            tail = tail[closed..].trim_start();
            if let Some(after) = tail.strip_prefix(';') {
                tail = after.trim_start();
            }
            if let Some(after) = tail.strip_prefix("return") {
                tail = after.trim_start();
                if let Some(after) = tail.strip_prefix(';') {
                    tail = after.trim_start();
                }
            }
            rest = tail;
        }
        out.push_str(rest);
        out
    }

    #[test]
    fn a_pin_that_cannot_build_its_fixture_must_report_it_unscored() {
        for pin in [
            "character_device_is_refused_as_not_regular",
            "symlinked_regular_input_is_accepted",
        ] {
            let body = body_of(pin);
            assert!(
                body.contains(RECORD),
                "{pin}: a fixture this host cannot create must be reported unscored, because a \
                 pin that returns quietly lets a run that judged nothing be tallied as a pass"
            );
            let leftover = without_recorded_exits(body);
            assert!(
                !leftover.contains("return"),
                "{pin}: only the recorded-unscored path may leave the pin early; found an exit \
                 that reports nothing"
            );
        }
    }

    #[test]
    fn an_unscored_pin_without_a_tally_destination_panics() {
        let body = body_of("record_unsupported");
        assert!(
            body.contains("IPRANGE_V4_WIN_PIN_TALLY"),
            "the tally destination is what turns a missing fixture into a recorded status"
        );
        assert!(
            body.contains("panic!"),
            "a missing fixture with no tally destination must fail, not pass unverified"
        );
    }
}
