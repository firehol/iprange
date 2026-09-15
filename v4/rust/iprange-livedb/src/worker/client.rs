mod recovery;
mod validation;

use std::path::PathBuf;
use std::process::{Child, Command, ExitStatus, Stdio};
use std::time::{Duration, Instant};

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::publication::PublicationProblem;

use super::control::{Control, FaultRecord, State, OWNED_FAULT_EXIT};
use super::{wire, wire_validation};

#[cfg(all(test, target_os = "linux"))]
use super::control::MappingRole;

pub(super) use recovery::{cleanup_checkpoint, fault_problem, recover};
pub(super) use validation::{inspect_recovery_candidates, validate};

#[cfg(all(test, target_os = "linux"))]
use recovery::{discard_clean, recover_once, RecoveryAttempt};
#[cfg(all(test, unix))]
use recovery::{read_recovery_callback_report, scratch_clean};
#[cfg(all(test, unix))]
use validation::read_validation_progress;

const START_LIMIT: Duration = Duration::from_secs(30);

fn record_unreadable_page(
    pages: &mut Vec<u32>,
    page: u32,
    max_heap_bytes: u64,
    repeated: &'static str,
) -> Result<()> {
    let insertion = match pages.binary_search(&page) {
        Ok(_) => return Err(Error::Conflict(repeated)),
        Err(insertion) => insertion,
    };
    let count = pages
        .len()
        .checked_add(1)
        .ok_or(Error::ArithmeticOverflow("unreadable source-page list"))?;
    let bytes = count
        .checked_mul(std::mem::size_of::<u32>())
        .ok_or(Error::ArithmeticOverflow("unreadable source-page list"))? as u64;
    if bytes > max_heap_bytes {
        return Err(Error::BudgetExceeded("unreadable source-page list"));
    }
    pages
        .try_reserve_exact(1)
        .map_err(|_| Error::BudgetExceeded("unreadable source-page list"))?;
    pages.insert(insertion, page);
    Ok(())
}

/// The number of descriptors the spawn path itself must prove claimable
/// before forking (design section 9.1): the three null descriptors the
/// parent opens to hand the child its standard streams, one per slot, which
/// is exactly what the standard library's null-stdio path claimed before
/// this owner existed. Sizing it below three makes the check refuse a spawn
/// the process could have completed, which moved the reference's own worker
/// arms one band later than the measured `validate` band 8 and
/// `recovery.inspect` band 7 of design section 7; sizing it above three
/// reserves for opens that belong to someone else. The child's own startup
/// opens are deliberately not included: those failures belong to the
/// operation that needed the descriptor and keep their own class (a child
/// that cannot open its control page or its source answers through the
/// handshake, which the handlers map to the io class of an unresourced
/// worker). The count comes from a descriptor-table read, never from opening
/// a caller-reachable path.
#[cfg(unix)]
const SPAWN_DESCRIPTOR_DEMAND: usize = 3;

/// Bound on the pre-fork headroom retry (design section 9.3). Owned by the
/// spawn as a named constant and deliberately far below START_LIMIT, so
/// headroom wait + spawn + the handshake's own START_LIMIT stay inside the
/// specification's 30 s worker-start bound.
#[cfg(unix)]
const SPAWN_DESCRIPTOR_WAIT: Duration = Duration::from_secs(5);

pub(super) fn spawn(control: &Control) -> Result<Process> {
    let candidates = worker_candidates()?;
    let mut last_error = None;
    let mut attempted = false;
    for executable in candidates {
        if !executable.is_file() {
            continue;
        }
        attempted = true;
        // Resourced per attempt: the descriptors are consumed by the spawn
        // and a candidate that turns out to be missing must not leak them
        // into the next attempt's table.
        let null = spawn_resourced_null_stdio(control)?;
        let child = spawn_child(&executable, control, null);
        match child {
            Ok(child) => return Ok(Process::new(child)),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                last_error = Some(error);
            }
            Err(error) => return Err(error.into()),
        }
    }
    if !attempted {
        return Err(Error::Unsupported(
            "SDK validation/recovery worker is unavailable",
        ));
    }
    Err(last_error.map_or_else(
        || Error::Unsupported("SDK validation/recovery worker is unavailable"),
        Error::Io,
    ))
}

/// Reserves the spawn's descriptor headroom and opens the stdio descriptor
/// the child receives, replacing std's `Stdio::null()` triple (design
/// sections 9.1-9.4). std opens the null device by name inside the spawn:
/// that open blocks forever on a planted FIFO, arrives after no bound can
/// observe it (the handshake clock has not started yet), and on the Go peer
/// registers the descriptor with the runtime poller. The owned open is
/// O_NONBLOCK with a descriptor-identity check, so a planted node answers
/// immediately instead of waiting for a writer that never comes. A spawn
/// that cannot be resourced answers the io class the reference answers for
/// a worker that cannot be resourced (mapped by the read-side handlers to
/// io/read_only_failure) and removes its control file; it never invents a
/// refusal for work it did not attempt.
#[cfg(unix)]
fn spawn_resourced_null_stdio(
    control: &Control,
) -> Result<(std::fs::File, std::fs::File, std::fs::File)> {
    if !wait_for_spawn_headroom() {
        abandon_spawn(control);
        return Err(Error::Io(std::io::Error::other(
            "worker spawn: descriptor table exhausted",
        )));
    }
    // One owned open per child slot. Duplicating a single open (try_clone)
    // would hold the original plus its clones across the spawn, so the
    // owned path would cost one descriptor more than the standard-library
    // null-stdio triple it replaces and the worker arms would start one
    // band later than the reference measured.
    let mut opened = Vec::with_capacity(3);
    for _ in 0..3 {
        match spawn_owned_null_stdio() {
            Ok(file) => opened.push(file),
            Err(error) => {
                abandon_spawn(control);
                return Err(Error::Io(std::io::Error::other(format!(
                    "worker spawn: null stdio: {error}"
                ))));
            }
        }
    }
    let stderr = opened.pop().expect("three opens");
    let stdout = opened.pop().expect("three opens");
    let stdin = opened.pop().expect("three opens");
    Ok((stdin, stdout, stderr))
}

#[cfg(not(unix))]
fn spawn_resourced_null_stdio(_: &Control) -> Result<()> {
    // Windows has no fd table to exhaust and std's null handle needs
    // neither; keep the standard-library path.
    Ok(())
}

#[cfg(not(unix))]
fn spawn_child(
    executable: &std::path::Path,
    control: &Control,
    _null: (),
) -> std::io::Result<Child> {
    Command::new(executable)
        .arg("--control")
        .arg(control.path())
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
}

#[cfg(unix)]
fn spawn_child(
    executable: &std::path::Path,
    control: &Control,
    null: (std::fs::File, std::fs::File, std::fs::File),
) -> std::io::Result<Child> {
    // os/exec's caller-supplied *os.File stdio is the Go-side shape of the
    // same promise: std duplicates the descriptors the owner opened and
    // opens no path itself. The parent's three null Files close when the
    // spawned command drops them (design section 5.5).
    let (stdin, stdout, stderr) = null;
    Command::new(executable)
        .arg("--control")
        .arg(control.path())
        .stdin(Stdio::from(stdin))
        .stdout(Stdio::from(stdout))
        .stderr(Stdio::from(stderr))
        .spawn()
}

fn abandon_spawn(control: &Control) {
    // Best-effort unlink of the private control file so an identical
    // request afterwards starts cleanly (spec: a worker arm that could
    // not start leaves no control file behind).
    let _ = std::fs::remove_file(control.path());
}

/// Retry bound for the pre-fork descriptor-table read. An unreadable
/// table is not a refusal: the owned null open is the ultimate EMFILE
/// guard and answers with the right class on its own.
#[cfg(unix)]
fn wait_for_spawn_headroom() -> bool {
    let deadline = Instant::now() + SPAWN_DESCRIPTOR_WAIT;
    loop {
        match free_claimable_descriptors() {
            None => return true,
            Some(free) if free >= SPAWN_DESCRIPTOR_DEMAND => return true,
            Some(_) => {
                if Instant::now() >= deadline {
                    return false;
                }
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

/// Counts free descriptor numbers from 0 to min(soft, 4096) with
/// fcntl(F_GETFD). Descriptors are allocated at the lowest free number,
/// so the window count is a sound lower bound on what the process can
/// claim right now, and a process whose whole low table is dense is not
/// this SDK (the reference measures its working set in single digits).
/// Returns None when the limit itself cannot be read.
#[cfg(unix)]
fn free_claimable_descriptors() -> Option<usize> {
    let mut limit = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    if unsafe { libc::getrlimit(libc::RLIMIT_NOFILE, &mut limit) } != 0 {
        return None;
    }
    let soft = usize::try_from(limit.rlim_cur).unwrap_or(usize::MAX);
    let window = soft.min(4096);
    let mut free = 0usize;
    for fd in 0..window as libc::c_int {
        if unsafe { libc::fcntl(fd, libc::F_GETFD, 0) } == -1 {
            free += 1;
        }
    }
    Some(free)
}

/// Opens the null device as the child's stdio: O_NONBLOCK open plus an
/// fstat identity check on the descriptor itself (never an earlier path
/// stat), then the flag is cleared so the child's standard streams keep
/// blocking semantics. A planted FIFO, symlink or unexpected device
/// answers as an error immediately; the child never sees it.
#[cfg(unix)]
fn spawn_owned_null_stdio() -> std::io::Result<std::fs::File> {
    use std::os::fd::FromRawFd;
    let path = std::ffi::CString::new("/dev/null")
        .map_err(|_| std::io::Error::other("worker spawn: null path has an embedded NUL"))?;
    let fd = loop {
        let opened =
            unsafe { libc::open(path.as_ptr(), libc::O_RDWR | libc::O_NONBLOCK | libc::O_CLOEXEC) };
        if opened >= 0 {
            break opened;
        }
        let error = std::io::Error::last_os_error();
        if error.raw_os_error() != Some(libc::EINTR) {
            return Err(error);
        }
    };
    let outcome = (|| -> std::io::Result<std::fs::File> {
        let mut stat: libc::stat = unsafe { std::mem::zeroed() };
        if unsafe { libc::fstat(fd, &mut stat) } != 0 {
            return Err(std::io::Error::last_os_error());
        }
        if (stat.st_mode & libc::S_IFMT) != libc::S_IFCHR {
            return Err(std::io::Error::other(
                "worker spawn: the null device name does not name a character device",
            ));
        }
        let (major, minor) = device_identity(stat.st_rdev);
        const NULL_MAJOR: u32 = if cfg!(target_os = "freebsd") { 3 } else { 1 };
        const NULL_MINOR: u32 = if cfg!(target_os = "freebsd") { 1 } else { 3 };
        if major != NULL_MAJOR || minor != NULL_MINOR {
            return Err(std::io::Error::other(
                "worker spawn: the null device name does not name the null device",
            ));
        }
        let flags = unsafe { libc::fcntl(fd, libc::F_GETFL) };
        if flags == -1 {
            return Err(std::io::Error::last_os_error());
        }
        if flags & libc::O_NONBLOCK != 0
            && unsafe { libc::fcntl(fd, libc::F_SETFL, flags & !libc::O_NONBLOCK) } != 0
        {
            return Err(std::io::Error::last_os_error());
        }
        Ok(unsafe { std::fs::File::from_raw_fd(fd) })
    })();
    if outcome.is_err() {
        unsafe { libc::close(fd) };
    }
    outcome
}

/// decodes dev_t into {major, minor} with the platform's own encoding
/// (Linux kDev_t; Apple/BSD 8-24 or 24-8 splits).
#[cfg(unix)]
fn device_identity(rdev: libc::dev_t) -> (u32, u32) {
    let rdev = rdev as u64;
    #[cfg(any(target_os = "linux", target_os = "android"))]
    {
        (
            (((rdev >> 8) & 0xfff) | ((rdev >> 28) & 0xfffff000)) as u32,
            ((rdev & 0xff) | ((rdev >> 12) & 0xfff00)) as u32,
        )
    }
    #[cfg(any(target_os = "macos", target_os = "ios"))]
    {
        (
            ((rdev >> 24) & 0xff) as u32,
            (rdev & 0xff_ffff) as u32,
        )
    }
    #[cfg(target_os = "freebsd")]
    {
        (
            ((rdev >> 24) & 0xff) as u32,
            (rdev & 0xff_ffff) as u32,
        )
    }
    #[cfg(not(any(
        target_os = "linux",
        target_os = "android",
        target_os = "macos",
        target_os = "ios",
        target_os = "freebsd"
    )))]
    {
        // Other unix platforms keep the character-device class check above
        // and do not pin the numeric identity.
        (0, 0)
    }
}

pub(super) fn start(child: &mut Process, control: &mut Control) -> Result<()> {
    handshake(child, control)?;
    control.set_state(State::Running);
    Ok(())
}

fn handshake(child: &mut Process, control: &mut Control) -> Result<()> {
    let deadline = Instant::now() + START_LIMIT;
    loop {
        if control.state() == Some(State::WorkerReady) {
            if control.worker_pid() != child.id() {
                child.abort();
                return Err(Error::Conflict("SDK worker identity does not match"));
            }
            #[cfg(unix)]
            control.remove_path()?;
            return Ok(());
        }
        if let Some(status) = child.try_wait()? {
            return Err(Error::Conflict(if status.success() {
                "SDK worker exited before its version handshake"
            } else {
                "SDK worker version or protocol does not match"
            }));
        }
        if Instant::now() >= deadline {
            child.abort();
            return Err(Error::Conflict("SDK worker version handshake timed out"));
        }
        std::thread::sleep(Duration::from_millis(1));
    }
}

pub(super) enum Drive {
    Complete { guard_pending: bool },
    Fault(FaultRecord),
}

pub(super) fn drive(
    child: &mut Process,
    control: &Control,
    cancellation: &CancellationToken,
) -> Result<Drive> {
    drive_loop(
        child,
        control,
        cancellation,
        "SDK worker emitted an unexpected event",
        |_state, _child, _control| Ok(false),
    )
}

fn drive_loop(
    child: &mut Process,
    control: &Control,
    cancellation: &CancellationToken,
    unexpected: &'static str,
    mut event: impl FnMut(State, &mut Process, &Control) -> Result<bool>,
) -> Result<Drive> {
    loop {
        let state = control.state();
        match state {
            Some(State::CancelPoll) => acknowledge_poll(control, cancellation),
            Some(State::Complete) => {
                let guard_pending = control.guard_pending();
                if guard_pending {
                    return Ok(Drive::Complete { guard_pending });
                }
                let status = child.wait()?;
                return status
                    .success()
                    .then_some(Drive::Complete { guard_pending })
                    .ok_or(Error::Conflict("SDK worker completion status is invalid"));
            }
            Some(State::Fault) => {
                let status = child.wait()?;
                if status.code() == Some(OWNED_FAULT_EXIT) {
                    return Ok(Drive::Fault(control.fault_record()?));
                }
                return Err(Error::Conflict("SDK worker fault record is untrusted"));
            }
            Some(State::Failed) => return Err(worker_failure(child, control)?),
            Some(state) if event(state, child, control)? => {}
            Some(State::Running) | Some(State::WorkerReady) | Some(State::Request) | None => {
                if !control.external_poll() && cancellation.is_cancelled() {
                    control.request_cancel();
                }
                if child.try_wait()?.is_some() {
                    return Err(Error::Conflict(
                        "SDK worker exited without a terminal record",
                    ));
                }
                std::thread::sleep(Duration::from_millis(1));
            }
            Some(_) => {
                child.abort();
                return Err(Error::Conflict(unexpected));
            }
        }
    }
}

fn advance_sequence(
    child: &mut Process,
    delivered: &mut u64,
    sequence: u64,
    invalid: &'static str,
) -> Result<()> {
    if sequence != delivered.saturating_add(1) {
        child.abort();
        return Err(Error::Conflict(invalid));
    }
    *delivered = sequence;
    Ok(())
}

fn acknowledge_callback(
    control: &Control,
    result: Result<bool>,
    callback: &mut Option<CallbackDecision>,
) -> Result<()> {
    match result {
        Ok(false) => control.set_response(0),
        Ok(true) => {
            *callback = Some(CallbackDecision::Stop);
            control.set_response(1);
        }
        Err(cause) => {
            let written = wire::write_worker_error(control, &cause);
            *callback = Some(CallbackDecision::Error(cause));
            written?;
            control.set_response(2);
        }
    }
    control.set_state(State::Running);
    Ok(())
}

enum CallbackDecision {
    Stop,
    Error(Error),
}

impl CallbackDecision {
    fn into_error(self) -> Error {
        match self {
            Self::Stop => Error::StoppedBySink,
            Self::Error(cause) => Error::SinkFailed(Box::new(cause)),
        }
    }
}

fn acknowledge_poll(control: &Control, cancellation: &CancellationToken) {
    let cancelled = cancellation.is_cancelled();
    control.set_response(u32::from(cancelled));
    if cancelled {
        control.request_cancel();
    }
    control.set_state(State::Running);
}

fn worker_failure(child: &mut Process, control: &Control) -> Result<Error> {
    let status = child.wait()?;
    if !status.success() {
        return Err(Error::Conflict(
            "SDK worker failure record has an invalid completion status",
        ));
    }
    wire::read_worker_error(control)
}

pub(super) struct Process {
    child: Option<Child>,
}

impl Process {
    fn new(child: Child) -> Self {
        Self { child: Some(child) }
    }

    fn id(&self) -> u32 {
        self.child.as_ref().map_or(0, Child::id)
    }

    fn wait(&mut self) -> std::io::Result<ExitStatus> {
        let status = self.child.as_mut().expect("active worker process").wait()?;
        self.child = None;
        Ok(status)
    }

    fn try_wait(&mut self) -> std::io::Result<Option<ExitStatus>> {
        let Some(child) = self.child.as_mut() else {
            return Ok(None);
        };
        let status = child.try_wait()?;
        if status.is_some() {
            self.child = None;
        }
        Ok(status)
    }

    fn active(&self) -> bool {
        self.child.is_some()
    }

    fn abort(&mut self) {
        if let Some(child) = self.child.as_mut() {
            let _ = child.kill();
            let _ = child.wait();
        }
        self.child = None;
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        self.abort();
    }
}

pub(crate) struct WorkerCleanup {
    child: Process,
    control: Control,
    last_problem: PublicationProblem,
}

impl WorkerCleanup {
    fn new(child: Process, control: Control, last_problem: PublicationProblem) -> Self {
        Self {
            child,
            control,
            last_problem,
        }
    }

    pub(crate) fn release(&mut self) -> Result<()> {
        if !self.child.active() {
            return Ok(());
        }
        self.control.set_state(State::CleanupRequest);
        let deadline = Instant::now() + START_LIMIT;
        loop {
            if self.control.state() == Some(State::CleanupResult) {
                let (complete, problem) = wire_validation::read_cleanup_result(&self.control)?;
                if complete {
                    let status = self.child.wait()?;
                    return if status.success() {
                        Ok(())
                    } else {
                        Err(Error::Conflict(
                            "SDK cleanup worker completion status is invalid",
                        ))
                    };
                }
                self.last_problem = problem.ok_or(Error::Conflict(
                    "SDK cleanup worker omitted its cleanup problem",
                ))?;
                return Err(self.operation_error());
            }
            if self.child.try_wait()?.is_some() {
                self.last_problem = PublicationProblem::new(
                    crate::ErrorCode::Conflict,
                    None,
                    "isolated cleanup worker exited unexpectedly",
                );
                return Err(self.operation_error());
            }
            if Instant::now() >= deadline {
                self.last_problem = PublicationProblem::new(
                    crate::ErrorCode::Conflict,
                    None,
                    "isolated cleanup worker timed out",
                );
                return Err(self.operation_error());
            }
            std::thread::sleep(Duration::from_millis(1));
        }
    }

    pub(crate) fn last_problem(&self) -> PublicationProblem {
        self.last_problem.clone()
    }

    fn operation_error(&self) -> Error {
        Error::WorkerOperation {
            code: self.last_problem.code,
            os_code: self.last_problem.os_code,
        }
    }
}

impl std::fmt::Debug for WorkerCleanup {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output
            .debug_struct("WorkerCleanup")
            .field("worker_pid", &self.child.active().then(|| self.child.id()))
            .field("last_problem", &self.last_problem)
            .finish()
    }
}

#[cfg(all(test, unix))]
#[path = "client_tests.rs"]
mod tests;

pub(crate) fn worker_candidates() -> Result<Vec<PathBuf>> {
    let name = format!("iprange-v4-worker{}", std::env::consts::EXE_SUFFIX);
    let current = std::env::current_exe()?;
    let mut candidates = Vec::with_capacity(2);
    if let Some(directory) = current.parent() {
        candidates.push(directory.join(&name));
        // Cargo places integration-test executables in `target/*/deps` and
        // package binaries in its parent. The build-ID handshake still rejects
        // every unrelated executable.
        if directory.file_name().is_some_and(|part| part == "deps") {
            if let Some(target) = directory.parent() {
                candidates.push(target.join(&name));
            }
        }
    }
    candidates.dedup();
    Ok(candidates)
}
