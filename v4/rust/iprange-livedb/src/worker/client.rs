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

pub(super) fn spawn(control: &Control) -> Result<Process> {
    let mut last_error = None;
    let mut attempted = false;
    for executable in worker_candidates()? {
        if !executable.is_file() {
            continue;
        }
        attempted = true;
        let child = Command::new(&executable)
            .arg("--control")
            .arg(control.path())
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn();
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
