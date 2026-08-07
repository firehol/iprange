//! Isolated mapped-fault worker used only by explicit validation and recovery.

// Worker results retain complete diagnostic and cleanup state by value.
#![allow(clippy::result_large_err)]

mod cleanup;
mod client;
mod control;
#[cfg(unix)]
mod posix;
#[cfg(windows)]
mod windows;
mod wire;
mod wire_cleanup;
mod wire_publication;
mod wire_recovery;
mod wire_validation;

use std::cell::{Cell, RefCell};
use std::ffi::OsString;
use std::sync::Arc;

use control::{CallbackCheckpoint, Control, Opcode, State};

pub(crate) use client::WorkerCleanup;

const EXIT_USAGE: i32 = 64;
const EXIT_PROTOCOL: i32 = 65;

thread_local! {
    static CURRENT_CONTROL: Cell<*const Control> = const { Cell::new(std::ptr::null()) };
    static NEXT_MAPPING_GENERATION: Cell<u64> = const { Cell::new(1) };
    static UNREADABLE_SOURCE_PAGES: RefCell<Vec<u32>> = const { RefCell::new(Vec::new()) };
}

pub(crate) fn main() -> i32 {
    match run(std::env::args_os().skip(1).collect()) {
        Ok(()) => 0,
        Err(code) => code,
    }
}

fn run(arguments: Vec<OsString>) -> Result<(), i32> {
    let [flag, path] = arguments.as_slice() else {
        return Err(EXIT_USAGE);
    };
    if flag != "--control" {
        return Err(EXIT_USAGE);
    }
    let control = Arc::new(Control::open_worker(path).map_err(|_| EXIT_PROTOCOL)?);
    control.verify_request().map_err(|_| EXIT_PROTOCOL)?;
    control.set_worker_pid(std::process::id());
    control.set_state(State::WorkerReady);
    control
        .wait_for(State::Running)
        .map_err(|_| EXIT_PROTOCOL)?;

    #[cfg(unix)]
    let _fault_handler = posix::Handler::install(&control).map_err(|_| EXIT_PROTOCOL)?;
    #[cfg(windows)]
    let _fault_handler = windows::Handler::install(&control).map_err(|_| EXIT_PROTOCOL)?;

    let _context = Context::enter(&control).map_err(|_| EXIT_PROTOCOL)?;
    let guard = match control.opcode().ok_or(EXIT_PROTOCOL)? {
        Opcode::InspectRecoveryCandidates => {
            let result = inspect(&control);
            wire::write_inspection_result(&control, &result).map(|()| None)
        }
        Opcode::Validate => run_validation(&control),
        Opcode::Recover => run_recovery(&control),
        Opcode::CleanupRecoveryAttempt => cleanup::run_worker(&control),
    };
    let mut guard = match guard {
        Ok(guard) => guard,
        Err(cause) => {
            wire::write_worker_error(&control, &cause).map_err(|_| EXIT_PROTOCOL)?;
            control.set_state(State::Failed);
            return Ok(());
        }
    };
    control.set_guard_pending(guard.is_some());
    control.set_state(State::Complete);
    if let Some(guard) = guard.as_mut() {
        serve_cleanup(&control, guard).map_err(|_| EXIT_PROTOCOL)?;
    }
    Ok(())
}

fn run_recovery(
    control: &Arc<Control>,
) -> crate::error::Result<Option<crate::recovery::RecoverySourceCleanupGuard>> {
    let request = wire_recovery::read_request(control)?;
    set_unreadable_source_pages(request.unreadable_pages)?;
    let (attempt, file) = crate::publication::output::resume_secured_output(
        &request.destination_path,
        &request.output,
    )
    .map_err(|_| crate::Error::Conflict("worker recovery output ownership changed"))?;
    let shared = Arc::clone(control);
    let cancellation =
        crate::CancellationToken::from_poll(Arc::new(move || shared.request_external_poll()));
    let mut sink = RecoveryProxy {
        control,
        suppress_through: request.delivered_unknowns,
    };
    let mut outcome = crate::recovery::recover_precreated_local(
        &request.source_path,
        request.candidate,
        request.mode,
        &request.budget,
        &mut sink,
        &cancellation,
        attempt,
        file,
    );
    let guard = match &mut outcome {
        Ok(_) => None,
        Err(failure) => failure.source_cleanup.take(),
    };
    let problem = guard.as_ref().map(|guard| guard.last_problem());
    wire_recovery::write_outcome(control, &outcome, problem)?;
    Ok(guard)
}

struct RecoveryProxy<'a> {
    control: &'a Control,
    suppress_through: u64,
}

impl crate::recovery::RecoverySink for RecoveryProxy<'_> {
    fn unknown(
        &mut self,
        envelope: &crate::recovery::RecoveryUnknownEnvelope,
    ) -> crate::error::Result<crate::recovery::RecoverySinkControl> {
        if envelope.sequence <= self.suppress_through {
            return Ok(crate::recovery::RecoverySinkControl::Continue);
        }
        wire_recovery::write_unknown(self.control, envelope)?;
        self.control.set_response(0);
        self.control.set_state(State::Unknown);
        while self.control.state() == Some(State::Unknown) {
            if !self.control.parent_alive() {
                return Err(crate::Error::Cancelled);
            }
            std::thread::sleep(std::time::Duration::from_millis(1));
        }
        match self.control.response() {
            0 => Ok(crate::recovery::RecoverySinkControl::Continue),
            1 => Ok(crate::recovery::RecoverySinkControl::Stop),
            2 => Err(wire_recovery::read_callback_error(self.control)?),
            _ => Err(crate::Error::Conflict(
                "worker recovery callback response is invalid",
            )),
        }
    }
}

struct Context;

impl Context {
    fn enter(control: &Control) -> crate::error::Result<Self> {
        CURRENT_CONTROL.with(|current| {
            if !current.get().is_null() {
                return Err(crate::Error::Conflict("worker context is already active"));
            }
            current.set(control);
            Ok(Self)
        })
    }
}

impl Drop for Context {
    fn drop(&mut self) {
        CURRENT_CONTROL.with(|current| current.set(std::ptr::null()));
    }
}

pub(crate) fn probe_source<T>(
    mapping: &crate::mapping::Mapping,
    operation: impl FnOnce() -> crate::error::Result<T>,
) -> crate::error::Result<T> {
    probe_mapping(mapping, control::MappingRole::Source, operation)
}

pub(crate) fn enter_source(
    mapping: &crate::mapping::Mapping,
) -> crate::error::Result<Probe<'static>> {
    enter_region(mapping.region()?, control::MappingRole::Source)
}

pub(crate) fn enter_output(
    mapping: &crate::mapping::Mapping,
) -> crate::error::Result<Probe<'static>> {
    enter_region(mapping.region()?, control::MappingRole::Output)
}

pub(crate) fn enter_coordination(
    mapping: &crate::mapping::Mapping,
) -> crate::error::Result<Probe<'static>> {
    enter_region(mapping.region()?, control::MappingRole::Coordination)
}

#[cfg(windows)]
pub(crate) fn enter_artifact(
    mapping: &crate::mapping::Mapping,
    kind: crate::publication::ArtifactKind,
) -> crate::error::Result<Probe<'static>> {
    let role = if matches!(kind, crate::publication::ArtifactKind::AuthorizedScratch) {
        control::MappingRole::Scratch
    } else {
        control::MappingRole::Output
    };
    enter_region(mapping.region()?, role)
}

pub(crate) fn probe_output<T>(
    mapping: &crate::mapping::Mapping,
    operation: impl FnOnce() -> crate::error::Result<T>,
) -> crate::error::Result<T> {
    probe_output_region(mapping.region()?, operation)
}

pub(crate) fn probe_scratch<T>(
    mapping: &crate::mapping::Mapping,
    operation: impl FnOnce() -> crate::error::Result<T>,
) -> crate::error::Result<T> {
    probe_region(mapping.region()?, control::MappingRole::Scratch, operation)
}

pub(crate) fn probe_output_region<T>(
    region: (*const u8, usize),
    operation: impl FnOnce() -> crate::error::Result<T>,
) -> crate::error::Result<T> {
    probe_region(region, control::MappingRole::Output, operation)
}

pub(crate) fn probe_scratch_region<T>(
    region: (*const u8, usize),
    operation: impl FnOnce() -> crate::error::Result<T>,
) -> crate::error::Result<T> {
    probe_region(region, control::MappingRole::Scratch, operation)
}

fn probe_mapping<T>(
    mapping: &crate::mapping::Mapping,
    role: control::MappingRole,
    operation: impl FnOnce() -> crate::error::Result<T>,
) -> crate::error::Result<T> {
    probe_region(mapping.region()?, role, operation)
}

fn probe_region<T>(
    (base, len): (*const u8, usize),
    role: control::MappingRole,
    operation: impl FnOnce() -> crate::error::Result<T>,
) -> crate::error::Result<T> {
    let probe = enter_region((base, len), role)?;
    let result = operation();
    drop(probe);
    result
}

fn enter_region(
    (base, len): (*const u8, usize),
    role: control::MappingRole,
) -> crate::error::Result<Probe<'static>> {
    let control = CURRENT_CONTROL.with(Cell::get);
    if control.is_null() {
        return Ok(Probe {
            control: None,
            previous: None,
        });
    }
    #[cfg(unix)]
    posix::verify_owned(unsafe { &*control })?;
    #[cfg(windows)]
    windows::verify_owned(unsafe { &*control })?;
    let previous = unsafe { &*control }.registration()?;
    let generation = NEXT_MAPPING_GENERATION.with(|next| {
        let generation = next.get();
        next.set(generation.checked_add(1).unwrap_or(1));
        generation
    });
    unsafe { &*control }.arm(generation, role, base, len)?;
    Ok(Probe {
        control: Some(unsafe { &*control }),
        previous,
    })
}

pub(crate) struct Probe<'a> {
    control: Option<&'a Control>,
    previous: Option<control::MappingRegistration>,
}

impl Drop for Probe<'_> {
    fn drop(&mut self) {
        let Some(control) = self.control else {
            return;
        };
        if let Some(previous) = self.previous {
            let _ = control.arm(
                previous.generation,
                previous.role,
                previous.base,
                previous.len,
            );
        } else {
            control.disarm();
        }
    }
}

fn inspect(
    control: &Arc<Control>,
) -> crate::error::Result<crate::recovery::RecoveryCandidateInspection> {
    let (path, mode, budget, unreadable_pages) = wire::read_inspection_request(control)?;
    set_unreadable_source_pages(unreadable_pages)?;
    let shared = Arc::clone(control);
    let cancellation =
        crate::CancellationToken::from_poll(Arc::new(move || shared.request_external_poll()));
    crate::recovery::inspection::inspect_recovery_candidates_local(
        &path,
        mode,
        &budget,
        &cancellation,
    )
}

fn run_validation(
    control: &Arc<Control>,
) -> crate::error::Result<Option<crate::recovery::RecoverySourceCleanupGuard>> {
    let request = wire_validation::read_request(control)?;
    set_unreadable_source_pages(request.unreadable_pages)?;
    let shared = Arc::clone(control);
    let cancellation =
        crate::CancellationToken::from_poll(Arc::new(move || shared.request_external_poll()));
    let mut sink = ValidationProxy {
        control,
        suppress_through: request.delivered_findings,
    };
    let mut result = crate::validation::validate_local(
        &request.path,
        request.mode,
        &request.budget,
        &cancellation,
        &mut sink,
    );
    let guard = match &mut result {
        Ok(_) => None,
        Err(failure) => failure.source_cleanup.take(),
    };
    let problem = guard.as_ref().map(|guard| guard.last_problem());
    wire_validation::write_result(control, &result, problem)?;
    Ok(guard)
}

fn serve_cleanup(
    control: &Control,
    guard: &mut crate::recovery::RecoverySourceCleanupGuard,
) -> crate::error::Result<()> {
    loop {
        while control.state() != Some(State::CleanupRequest) {
            if !control.parent_alive() {
                return Ok(());
            }
            std::thread::sleep(std::time::Duration::from_millis(1));
        }
        match guard.retry_cleanup() {
            Ok(_) => {
                wire_validation::write_cleanup_result(control, true, None)?;
                control.set_guard_pending(false);
                control.set_state(State::CleanupResult);
                return Ok(());
            }
            Err(problem) => {
                wire_validation::write_cleanup_result(control, false, Some(problem))?;
                control.set_state(State::CleanupResult);
            }
        }
    }
}

struct ValidationProxy<'a> {
    control: &'a Control,
    suppress_through: u64,
}

impl crate::validation::ValidationSink for ValidationProxy<'_> {
    fn finding(
        &mut self,
        finding: &crate::validation::ValidationFinding,
    ) -> crate::error::Result<crate::validation::ValidationSinkControl> {
        if finding.sequence <= self.suppress_through {
            return Ok(crate::validation::ValidationSinkControl::Continue);
        }
        wire_validation::write_finding(self.control, finding)?;
        self.control.set_response(0);
        self.control.set_state(State::Finding);
        while self.control.state() == Some(State::Finding) {
            if !self.control.parent_alive() {
                return Err(crate::Error::Cancelled);
            }
            std::thread::sleep(std::time::Duration::from_millis(1));
        }
        match self.control.response() {
            0 => Ok(crate::validation::ValidationSinkControl::Continue),
            1 => Ok(crate::validation::ValidationSinkControl::Stop),
            2 => Err(wire_validation::read_callback_error(self.control)?),
            _ => Err(crate::Error::Conflict(
                "worker validation callback response is invalid",
            )),
        }
    }
}

fn set_unreadable_source_pages(mut pages: Vec<u32>) -> crate::error::Result<()> {
    pages.sort_unstable();
    if pages.windows(2).any(|pair| pair[0] == pair[1]) {
        return Err(crate::Error::InvalidArgument(
            "unreadable source pages contain duplicates",
        ));
    }
    UNREADABLE_SOURCE_PAGES.with(|selected| *selected.borrow_mut() = pages);
    Ok(())
}

pub(crate) fn source_page_unreadable(page: u32) -> bool {
    UNREADABLE_SOURCE_PAGES.with(|pages| pages.borrow().binary_search(&page).is_ok())
}

pub(crate) fn unreadable_source_pages() -> Vec<u32> {
    UNREADABLE_SOURCE_PAGES.with(|pages| pages.borrow().clone())
}

pub(crate) fn checkpoint_recovery(
    outcome: &crate::recovery::RecoveryOutcome,
) -> crate::error::Result<()> {
    let control = CURRENT_CONTROL.with(Cell::get);
    if control.is_null() {
        return Ok(());
    }
    let control = unsafe { &*control };
    control.begin_recovery_checkpoint();
    wire_recovery::write_outcome(control, outcome, None)?;
    control.seal_recovery_checkpoint();
    Ok(())
}

pub(crate) fn checkpoint_recovery_progress(
    report: &crate::recovery::RecoveryReport,
) -> crate::error::Result<()> {
    let control = CURRENT_CONTROL.with(Cell::get);
    if control.is_null() {
        return Ok(());
    }
    let control = unsafe { &*control };
    control.begin_callback_checkpoint();
    let mut output = wire::Writer::new_callback_checkpoint(control);
    wire_recovery::report(&mut output, report)?;
    output.finish()?;
    control.seal_callback_checkpoint(CallbackCheckpoint::RecoveryReport);
    Ok(())
}

pub(crate) fn checkpoint_validation_progress(
    progress: &crate::validation::ValidationProgress,
) -> crate::error::Result<()> {
    let control = CURRENT_CONTROL.with(Cell::get);
    if control.is_null() {
        return Ok(());
    }
    let control = unsafe { &*control };
    control.begin_callback_checkpoint();
    let mut output = wire::Writer::new_callback_checkpoint(control);
    wire::progress(&mut output, progress)?;
    output.finish()?;
    control.seal_callback_checkpoint(CallbackCheckpoint::ValidationProgress);
    Ok(())
}

pub(crate) fn start_scratch_checkpoint(
    attempt_id: [u8; 16],
    directory_identity: crate::validation::LocalFileIdentity,
    creation_security: &crate::publication::CreationSecurity,
) -> crate::error::Result<()> {
    let control = CURRENT_CONTROL.with(Cell::get);
    if control.is_null() {
        return Ok(());
    }
    unsafe { &*control }.start_scratch_checkpoint(attempt_id, directory_identity, creation_security)
}

pub(crate) fn add_scratch_checkpoint(
    ordinal: u32,
    identity: crate::validation::LocalFileIdentity,
) -> crate::error::Result<()> {
    let control = CURRENT_CONTROL.with(Cell::get);
    if control.is_null() {
        return Ok(());
    }
    unsafe { &*control }.add_scratch_checkpoint(ordinal, identity)
}

pub(crate) fn inspect_recovery_candidates(
    path: &std::path::Path,
    mode: crate::recovery::RecoveryInspectionMode,
    budget: &crate::validation::ValidationBudget,
    cancellation: &crate::CancellationToken,
) -> crate::error::Result<crate::recovery::RecoveryCandidateInspection> {
    client::inspect_recovery_candidates(path, mode, budget, cancellation)
}

pub(crate) fn validate<S: crate::validation::ValidationSink>(
    path: &std::path::Path,
    mode: &crate::validation::ValidationMode,
    budget: &crate::validation::ValidationBudget,
    cancellation: &crate::CancellationToken,
    sink: &mut S,
) -> std::result::Result<crate::validation::ValidationResult, crate::validation::ValidationFailure>
{
    client::validate(path, mode, budget, cancellation, sink)
}

pub(crate) fn recover<S: crate::recovery::RecoverySink>(
    source_path: &std::path::Path,
    candidate: crate::recovery::RecoveryCandidate,
    destination_path: &std::path::Path,
    mode: crate::recovery::WorkerMode,
    budget: &crate::recovery::RecoveryBudget,
    sink: &mut S,
    cancellation: &crate::CancellationToken,
) -> crate::recovery::RecoveryOutcome {
    client::recover(
        source_path,
        candidate,
        destination_path,
        mode,
        budget,
        sink,
        cancellation,
    )
}
