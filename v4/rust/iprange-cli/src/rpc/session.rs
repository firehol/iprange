//! Connection session: reader thread, main event loop, bounded request
//! queue, cancellation, EOF shutdown, and fatal transport failures.
//!
//! Model: a reader thread forwards one physical input frame at a time
//! to the main loop as `SessionEvent::Line`; the main loop applies
//! cancellation and queue admission; one worker thread executes one
//! decoded frame (a single request or a batch) at a time; additional
//! frames wait in a channel behind a 16-request admission counter so
//! the transport never blocks. Per-request semantics
//! (iprange-jsonrpc-v1.md):
//! - one active request set plus at most 16 queued requests; a
//!   request exceeding the bound fails with -32002 server_busy;
//! - the reader stays active while work executes so cancellation
//!   and EOF are observed;
//! - cancel notifications apply immediately after frame validation:
//!   only ids admitted but not yet terminal (`pending`) are valid
//!   cancellation targets, so cancelling an id never poisons a later
//!   request that reuses it; queued matches are skipped without a
//!   response; the active request set is signalled only when it
//!   contains the cancelled id;
//! - a whole frame decodes strictly before anything in it executes;
//!   envelope failures produce one id-null error and the service
//!   keeps serving;
//! - every COMPLETE response object (envelope plus id plus result or
//!   error) is capped at 65,000 bytes; an unencodable inline success
//!   is replaced by the `output_limit` product error, never an
//!   oversized frame; a busy-rejected batch element stays inside its
//!   batch response array at its original position;
//! - stdin EOF stops acceptance, cancels queued requests and the
//!   active work token, lets every admitted unit reach a factual
//!   terminal outcome (quick work answers normally; SDK long work
//!   aborts through the cancelled token), closes all cursors and
//!   registered live readers, and exits 0 unless transport shutdown
//!   itself failed;
//! - a frame over the input ceiling produces -32001 with id null and
//!   the process closes without parsing any later bytes;
//! - a request whose id alone cannot be echoed within the
//!   65,000-byte response-object limit also produces -32001 with
//!   id null, but the service keeps serving;
//! - broken stdout, termination signals, and stdin read errors are
//!   fatal transport failures: the main loop runs the same
//!   cancellation/handle-cleanup path as EOF and the process exits
//!   non-zero.

use std::collections::HashSet;
use std::io::{self, BufRead, Write};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::mpsc::{Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;

use iprange_livedb::CancellationToken;
use serde_json::Value;

use super::dispatch::{resolve, HandlerError};
use super::framing::{FrameWriter, LineReader, QUEUED_LIMIT};
use super::schema::{self, Request, RequestId, SchemaError};
use super::state::ConnectionState;

/// Conservative connection-owned state shared by handlers.
#[derive(Default)]
pub struct SessionState {
    /// Shared reader/cursor resources and their connection limits.
    pub resources: ConnectionState,
    /// Request ids cancelled by the transport. Always a subset of
    /// `pending`: ids are removed when their unit reaches its terminal
    /// state so a reused id starts with a clean slate.
    pub cancelled: HashSet<String>,
    /// Request ids admitted but not yet terminal; only these ids are
    /// valid cancellation targets.
    pending: HashSet<String>,
    /// Set by EOF/fatal shutdown; the worker skips every unit drained
    /// after this flag is set (queued requests are cancelled).
    shutting_down: bool,
    /// First worker write failure (kind + message; io::Error is not
    /// Clone); makes the shutdown path exit non-zero when stdout broke
    /// while draining queued units.
    fatal_error: Option<(io::ErrorKind, String)>,
    /// Cancellation signal for the active work unit.
    pub token: Arc<CancellationToken>,
    /// Ids of the request set currently executing.
    active_keys: HashSet<String>,
    /// Id of the request currently executing; cursor handlers size pages
    /// against the complete response-object ceiling using this id.
    pub active_request_id: Option<RequestId>,
}
/// One element of a decoded frame in frame order.
///
/// `Busy` marks an element the read loop rejected with `server_busy`.
/// Keeping rejected elements in position lets the worker emit exactly
/// one batch response array whose members follow the request order.
enum WorkEntry {
    Execute(Request),
    Busy(Request),
}

impl WorkEntry {
    fn request(&self) -> &Request {
        match self {
            Self::Execute(request) | Self::Busy(request) => request,
        }
    }

    fn occupies_queue(&self) -> bool {
        matches!(self, Self::Execute(_))
    }
}

/// One decoded frame queued as a unit: array-order execution and one
/// response frame per input frame.
struct WorkUnit {
    entries: Vec<WorkEntry>,
    /// Elements that occupied queue capacity and must release it.
    admitted: usize,
    batch: bool,
}

/// Events the transport threads report to the main session loop.
#[derive(Debug)]
enum SessionEvent {
    /// One physical input frame (`Ok(Some)`), EOF (`Ok(None)`), or a
    /// line-read failure: the frame-over-input-ceiling case
    /// (`Err(LineReadError::FrameTooLarge)`) or a real stdin io error
    /// (`Err(LineReadError::Io)`), which is fatal like broken stdout.
    Line(Result<Option<Vec<u8>>, super::framing::LineReadError>),
    /// Unrecoverable transport failure: broken stdout, a termination
    /// signal (Unix), or a signal-watcher spawn failure.
    Fatal(io::Error),
}

pub struct Session {
    state: Arc<Mutex<SessionState>>,
    /// Requests admitted to the channel but not yet executed.
    in_flight: Arc<AtomicUsize>,
    work_tx: Option<Sender<WorkUnit>>,
    work_rx: Option<Receiver<WorkUnit>>,
    worker: Option<JoinHandle<()>>,
}

fn key(id: &RequestId) -> String {
    id.key()
}

fn request_key(request: &Request) -> Option<String> {
    request.id.as_ref().map(key)
}

impl Session {
    pub fn new() -> Self {
        let (work_tx, work_rx) = std::sync::mpsc::channel::<WorkUnit>();
        let state = Arc::new(Mutex::new(SessionState {
            resources: ConnectionState::default(),
            token: Arc::new(CancellationToken::new()),
            active_keys: HashSet::default(),
            cancelled: HashSet::default(),
            pending: HashSet::default(),
            shutting_down: false,
            fatal_error: None,
            active_request_id: None,
        }));
        Session {
            state,
            in_flight: Arc::new(AtomicUsize::new(0)),
            work_tx: Some(work_tx),
            work_rx: Some(work_rx),
            worker: None,
        }
    }

    /// Run the service until EOF or a fatal transport failure.
    ///
    /// One reader thread forwards input frames and one worker thread
    /// executes them; this thread runs the event loop. Termination
    /// signals (Unix) are installed before the threads spawn.
    pub fn run<R: BufRead + Send + 'static, W: Write + Send + 'static>(
        mut self,
        reader: R,
        writer: W,
    ) -> io::Result<()> {
        let (events_tx, events_rx) = std::sync::mpsc::channel::<SessionEvent>();

        // Block SIGINT/SIGTERM in this (main) thread before the
        // transport threads spawn so every thread inherits the mask;
        // the watcher reports their delivery as a fatal failure.
        signals::watch(events_tx.clone());

        let writer = Arc::new(Mutex::new(FrameWriter::new(writer)));
        let worker_state = Arc::clone(&self.state);
        let worker_writer = Arc::clone(&writer);
        let worker_in_flight = Arc::clone(&self.in_flight);
        let worker_events = events_tx.clone();
        let work_rx = self.work_rx.take().expect("run once");
        let worker = std::thread::Builder::new()
            .name("iprange-jsonrpc".into())
            .spawn(move || {
                worker_loop(worker_state, worker_writer, worker_in_flight, work_rx, worker_events)
            })
            .expect("spawn jsonrpc worker");
        self.worker = Some(worker);

        // Never joined: it ends by itself at EOF / frame-too-large and
        // is killed with the process on a fatal transport failure.
        let reader_events = events_tx.clone();
        std::thread::Builder::new()
            .name("iprange-stdin".into())
            .spawn(move || reader_loop(LineReader::new(reader), reader_events))
            .expect("spawn stdin reader");

        loop {
            match events_rx.recv() {
                Err(_) => {
                    // Unreachable while the signal watcher (Unix) or
                    // worker holds a sender; defensive fatal.
                    return self.fatal(io::Error::new(
                        io::ErrorKind::UnexpectedEof,
                        "stdin reader terminated",
                    ));
                }
                Ok(SessionEvent::Fatal(err)) => return self.fatal(err),
                Ok(SessionEvent::Line(Err(super::framing::LineReadError::FrameTooLarge))) => {
                    let payload = SchemaError::response(
                        None,
                        SchemaError {
                            code: schema::TRANSPORT_FRAME_TOO_LARGE,
                            message: "frame over input limit".into(),
                        },
                    );
                    let text = schema::encode_response_frame(&payload)
                        .expect("constant transport error within limits");
                    let mut w = writer.lock().unwrap();
                    if let Err(err) = w.write_line(&text) {
                        return self.fatal(err);
                    }
                    return self.shutdown();
                }
                Ok(SessionEvent::Line(Err(super::framing::LineReadError::Io(error)))) => {
                    // A real stdin read error is not EOF: the input
                    // transport failed, so the session runs the fatal
                    // cancellation/cleanup path and exits non-zero.
                    return self.fatal(io::Error::new(
                        error.kind(),
                        format!("stdin read failed: {error}"),
                    ));
                }
                Ok(SessionEvent::Line(Ok(None))) => return self.shutdown(),
                Ok(SessionEvent::Line(Ok(Some(line)))) => {
                    if let Err(err) = handle_frame(&mut self, line, &writer) {
                        return self.fatal(err);
                    }
                }
            }
        }
    }

    /// Cancel one request id immediately after frame validation.
    ///
    /// The notification params must match the strict CANCEL schema
    /// (exactly one member `request_id`, string or integral number)
    /// before anything is cancelled; an invalid notification is
    /// ignored and produces no response. Only ids that were admitted
    /// and have not reached their terminal state (`pending`) can be
    /// cancelled: unknown and already terminal ids are ignored, so
    /// cancelling an id never poisons a later request that reuses it.
    /// An id in the currently executing request set also cancels the
    /// unit's cancellation token.
    fn apply_cancel(&mut self, request: &Request) {
        if !schema::valid_cancel_params(&request.params) {
            return;
        }
        let cancel_id = match request.params.get("request_id") {
            Some(Value::String(s)) => Some(format!("s:{s}")),
            Some(n @ Value::Number(_)) => schema::number_cancel_key(n),
            _ => None,
        };
        let Some(cancel_id) = cancel_id else { return };
        let mut state = self.state.lock().unwrap();
        if !state.pending.contains(&cancel_id) {
            return;
        }
        state.cancelled.insert(cancel_id.clone());
        if state.active_keys.contains(&cancel_id) {
            state.token.cancel();
        }
    }

    /// Shared EOF/fatal cleanup: mark the session shutting down,
    /// cancel the current token (the active unit's token once the
    /// worker installed it), and close the work channel so the worker
    /// exits after draining queued units.
    fn begin_shutdown(&mut self) {
        {
            let mut s = self.state.lock().unwrap();
            s.shutting_down = true;
            s.token.cancel();
        }
        self.work_tx.take();
    }

    /// Close all connection resources held at shutdown: drop cursor
    /// checkpoints and close every registered live reader in
    /// deterministic handle order (see `ConnectionState::close_all`).
    ///
    /// Must run only after the worker joined, so no thread is left
    /// holding the state mutex while readers are closed. Returns the
    /// first close failure; an incomplete live-reader close is a
    /// transport-shutdown failure, never silently converted to Ok.
    fn close_registered_resources(&mut self) -> Option<io::Error> {
        let failures = {
            let mut state = self.state.lock().unwrap();
            state.resources.close_all()
        };
        if failures.is_empty() {
            return None;
        }
        let mut message = String::from("transport shutdown failed to close a live reader");
        for (handle, error) in failures {
            message.push_str(&format!("; {handle}: {error}"));
        }
        Some(io::Error::new(io::ErrorKind::Other, message))
    }

    /// EOF shutdown: stop acceptance, cancel queued units, request
    /// cancellation of the active unit, wait for its factual terminal
    /// outcome, and close every connection resource.
    ///
    /// Exits zero unless the transport itself failed: a worker write
    /// failure observed while draining queued units (broken stdout)
    /// or an incomplete live-reader close is a fatal transport
    /// failure (non-zero exit).
    fn shutdown(&mut self) -> io::Result<()> {
        self.begin_shutdown();
        let worker_failed = if let Some(worker) = self.worker.take() {
            let _ = worker.join();
            self.state.lock().unwrap().fatal_error.take().map(|(kind, message)| {
                io::Error::new(kind, message)
            })
        } else {
            None
        };
        let close_failed = self.close_registered_resources();
        match (worker_failed, close_failed) {
            (Some(first), Some(second)) => Err(io::Error::new(
                first.kind(),
                format!("{first}; {second}"),
            )),
            (Some(err), None) | (None, Some(err)) => Err(err),
            (None, None) => Ok(()),
        }
    }

    /// Fatal transport failure: same cancellation and handle cleanup
    /// as EOF (including closing connection resources), then report
    /// the failure (non-zero exit).
    fn fatal(&mut self, err: io::Error) -> io::Result<()> {
        self.begin_shutdown();
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
        let close_failed = self.close_registered_resources();
        match close_failed {
            Some(close) => Err(io::Error::new(err.kind(), format!("{err}; {close}"))),
            None => Err(err),
        }
    }
}

/// Forward every physical input frame from the reader to the main loop.
///
/// Returns after EOF or the frame-over-ceiling failure. On a fatal
/// transport failure it stays blocked on the input; the main loop
/// deliberately never joins this thread and the process exits when
/// the main function returns.
fn reader_loop<R: BufRead>(mut reader: LineReader<R>, events: Sender<SessionEvent>) {
    loop {
        match reader.read_line() {
            Ok(Some(line)) => {
                if events.send(SessionEvent::Line(Ok(Some(line)))).is_err() {
                    return;
                }
            }
            Ok(None) => {
                let _ = events.send(SessionEvent::Line(Ok(None)));
                return;
            }
            Err(err) => {
                let _ = events.send(SessionEvent::Line(Err(err)));
                return;
            }
        }
    }
}

fn worker_loop<W: Write + Send + 'static>(
    state: Arc<Mutex<SessionState>>,
    writer: Arc<Mutex<FrameWriter<W>>>,
    in_flight: Arc<AtomicUsize>,
    rx: Receiver<WorkUnit>,
    events: Sender<SessionEvent>,
) {
    while let Ok(unit) = rx.recv() {
        in_flight.fetch_sub(unit.admitted, Ordering::Relaxed);
        let keys: HashSet<String> = unit
            .entries
            .iter()
            .filter(|entry| matches!(entry, WorkEntry::Execute(_)))
            .filter_map(|entry| request_key(entry.request()))
            .collect();
        // Token/flag update in one lock scope: if shutdown lands
        // between a check and a fresh token install, the fresh token
        // would escape cancellation. A unit admitted before EOF
        // installs its own token (shutdown then cancels it: active,
        // factual). A unit still queued when EOF lands keeps the
        // already-cancelled token installed by begin_shutdown: quick
        // work answers normally, SDK long work aborts factually, and
        // no admitted unit is skipped. The channel close (work_tx
        // dropped by shutdown) ends the loop.
        {
            let mut s = state.lock().unwrap();
            if !s.shutting_down {
                s.token = Arc::new(CancellationToken::new());
            }
            s.active_keys = keys.clone();
        }
        let mut responses: Vec<Value> = unit
            .entries
            .iter()
            .filter_map(|entry| entry_response(&state, entry))
            .collect();
        {
            // Terminal state: the unit's request ids are no longer
            // cancellation targets, so a later request reusing an id
            // starts with a clean slate.
            let mut s = state.lock().unwrap();
            for key in &keys {
                s.cancelled.remove(key);
                s.pending.remove(key);
            }
            s.active_keys.clear();
        }
        if responses.is_empty() {
            continue;
        }
        let payload = if unit.batch {
            Value::Array(responses)
        } else {
            responses.pop().unwrap()
        };
        // Every member is bounded by the response-object ceiling and a
        // batch has at most 16 members, so the array cannot exceed the
        // frame ceiling (iprange-jsonrpc-v1.md, Framing).
        let text = schema::encode_response_frame(&payload).expect("response frame within ceiling");
        let mut w = writer.lock().unwrap();
        if let Err(err) = w.write_line(&text) {
            // Broken stdout is a fatal transport failure: record it,
            // report it to the main loop, and stop executing.
            state.lock().unwrap().fatal_error = Some((err.kind(), err.to_string()));
            let _ = events.send(SessionEvent::Fatal(err));
            return;
        }
    }
}

/// Decode one input frame, apply cancellation, admit requests, and
/// queue or directly answer the resulting work unit.
fn handle_frame<W: Write>(
    session: &mut Session,
    line: Vec<u8>,
    writer: &Arc<Mutex<FrameWriter<W>>>,
) -> io::Result<()> {
    let requests = match schema::decode_frame(&line) {
        Ok(requests) => requests,
        Err(err) => {
            let payload = SchemaError::response(None, err);
            let text = schema::encode_response_frame(&payload)
                .expect("constant schema error within limits");
            let mut w = writer.lock().unwrap();
            w.write_line(&text)?;
            return Ok(());
        }
    };
    let mut ordinary = Vec::new();
    let mut batch = false;
    for request in requests {
        if request.method == schema::CANCEL_METHOD {
            session.apply_cancel(&request);
            continue;
        }
        batch |= request.batch_index.is_some();
        ordinary.push(request);
    }
    if ordinary.is_empty() {
        return Ok(());
    }
    if ordinary
        .iter()
        .any(|request| preflight_unanswerable_id(request))
    {
        let payload = SchemaError::response(
            None,
            SchemaError {
                code: schema::TRANSPORT_FRAME_TOO_LARGE,
                message: "request id cannot be echoed within the response object limit".into(),
            },
        );
        let text = schema::encode_response_frame(&payload)
            .expect("constant transport error within limits");
        let mut w = writer.lock().unwrap();
        w.write_line(&text)?;
        return Ok(());
    }
    let entries = admit_frame(ordinary, &session.in_flight);
    mark_pending(&session.state, &entries);
    if batch {
        // Every element answers inside one array in the
        // frame's order, including busy rejections.
        let admitted = entries.iter().filter(|e| e.occupies_queue()).count();
        session
            .work_tx
            .as_ref()
            .unwrap()
            .send(WorkUnit {
                entries,
                admitted,
                batch,
            })
            .map_err(worker_gone)?;
    } else {
        // A single request keeps the standalone frame;
        // a busy rejection is answered immediately and
        // never occupies queue capacity.
        match entries.first() {
            Some(WorkEntry::Busy(request)) => {
                let payload = bounded_response(busy_response(request), request);
                let text = schema::encode_response_frame(&payload)
                    .expect("bounded response within frame limit");
                let mut w = writer.lock().unwrap();
                w.write_line(&text)?;
            }
            _ => {
                session
                    .work_tx
                    .as_ref()
                    .unwrap()
                    .send(WorkUnit {
                        entries,
                        admitted: 1,
                        batch,
                    })
                    .map_err(worker_gone)?;
            }
        }
    }
    Ok(())
}

/// Record every admitted Execute key as a valid cancellation target.
fn mark_pending(state: &Arc<Mutex<SessionState>>, entries: &[WorkEntry]) {
    let mut s = state.lock().unwrap();
    for entry in entries {
        if matches!(entry, WorkEntry::Execute(_)) {
            if let Some(key) = request_key(entry.request()) {
                s.pending.insert(key);
            }
        }
    }
}

/// The worker thread ended (fatal write failure already reported);
/// the transport cannot continue.
fn worker_gone(_: std::sync::mpsc::SendError<WorkUnit>) -> io::Error {
    io::Error::new(io::ErrorKind::BrokenPipe, "jsonrpc worker terminated")
}

/// Admit ordinary frame elements against the queue bound.
///
/// Rejected elements stay in position as `Busy` entries and never
/// occupy queue capacity.
fn admit_frame(requests: Vec<Request>, in_flight: &AtomicUsize) -> Vec<WorkEntry> {
    let mut entries = Vec::with_capacity(requests.len());
    for request in requests {
        if in_flight.load(Ordering::Relaxed) >= QUEUED_LIMIT {
            entries.push(WorkEntry::Busy(request));
        } else {
            in_flight.fetch_add(1, Ordering::Relaxed);
            entries.push(WorkEntry::Execute(request));
        }
    }
    entries
}

fn busy_response(request: &Request) -> Value {
    schema::error_response(
        request.id.as_ref().expect("ordinary requests carry ids"),
        schema::TRANSPORT_SERVER_BUSY,
        "server_busy",
        None,
    )
}

/// Build one frame-ordered response object, or `None` for a request
/// cancelled before execution (it is omitted from a batch array).
fn entry_response(state: &Arc<Mutex<SessionState>>, entry: &WorkEntry) -> Option<Value> {
    match entry {
        WorkEntry::Execute(request) => {
            let cancelled = {
                let s = state.lock().unwrap();
                request_key(request)
                    .map(|k| s.cancelled.contains(&k))
                    .unwrap_or(false)
            };
            if cancelled {
                return None;
            }
            Some(bounded_response(execute(state, request), request))
        }
        WorkEntry::Busy(request) => Some(bounded_response(busy_response(request), request)),
    }
}

/// Enforce the 65,000-byte ceiling on the COMPLETE response object
/// (envelope, id, and payload) before any frame is written.
///
/// A success that cannot fit is replaced by the documented
/// `output_limit` product error. An oversized product error keeps its
/// stable `data.code` and `data.outcome` and drops only its free-form
/// details. When the request id alone makes every faithful response
/// oversized, the id cannot be echoed; JSON-RPC 2.0's convention for
/// an unusable id is one `id: null` invalid-request error, which is
/// the only remaining response that always satisfies the ceiling.
/// Preflight: a request id that alone makes even the smallest faithful
/// response (an error of the documented shape) exceed RESPONSE_OBJECT_LIMIT
/// can never be answered. The frame answers -32001 with id null; an
/// unanswerable id is an ordinary (if unusable) request, not an
/// oversized input frame, so the service keeps serving after it.
fn preflight_unanswerable_id(request: &Request) -> bool {
    let id = request.id.as_ref().expect("ordinary requests carry ids");
    let probe = schema::error_response(
        id,
        schema::PRODUCT_ERROR,
        "response object exceeds the 65000-byte limit",
        Some(serde_json::json!({
            "code": "output_limit",
            "outcome": "read_only_failure",
        })),
    );
    schema::encode_response_object(&probe).is_err()
}

fn bounded_response(response: Value, request: &Request) -> Value {
    if schema::encode_response_object(&response).is_ok() {
        return response;
    }
    if response.get("error").is_some() {
        if let Some(data) = response["error"].get("data").cloned() {
            let mut reduced = serde_json::Map::new();
            if let Some(code) = data.get("code") {
                reduced.insert("code".into(), code.clone());
            }
            if let Some(outcome) = data.get("outcome") {
                reduced.insert("outcome".into(), outcome.clone());
            }
            let mut trimmed = response.clone();
            trimmed["error"]["data"] = Value::Object(reduced);
            if schema::encode_response_object(&trimmed).is_ok() {
                return trimmed;
            }
        }
    }
    let replacement = schema::error_response(
        request.id.as_ref().expect("ordinary requests carry ids"),
        schema::PRODUCT_ERROR,
        "response object exceeds the 65000-byte limit",
        Some(serde_json::json!({
            "code": "output_limit",
            "outcome": "read_only_failure",
        })),
    );
    if schema::encode_response_object(&replacement).is_ok() {
        return replacement;
    }
    SchemaError::response(
        None,
        SchemaError {
            code: super::schema::TRANSPORT_FRAME_TOO_LARGE,
            message: "response object exceeds the 65000-byte limit; request id cannot be echoed"
                .into(),
        },
    )
}

fn execute(state: &Arc<Mutex<SessionState>>, request: &Request) -> Value {
    let Some((validator, handler)) = resolve(&request.method) else {
        return schema::error_response(
            request.id.as_ref().unwrap(),
            schema::STD_METHOD_NOT_FOUND,
            &format!("unknown method {}", request.method),
            None,
        );
    };
    if let Err(message) = validator(&request.params) {
        return schema::error_response(
            request.id.as_ref().unwrap(),
            schema::STD_INVALID_PARAMS,
            &message,
            None,
        );
    }
    let mut st = state.lock().unwrap();
    st.active_request_id = request.id.clone();
    let handled = handler(&mut st, request.params.clone());
    st.active_request_id = None;
    match handled {
        Ok(result) => schema::success_response(request.id.as_ref().unwrap(), result),
        Err(HandlerError {
            code,
            outcome,
            message,
            details,
        }) => {
            let data = match details {
                Some(details) => {
                    serde_json::json!({"code": code, "outcome": outcome, "details": details})
                }
                None => serde_json::json!({"code": code, "outcome": outcome}),
            };
            schema::error_response(
                request.id.as_ref().unwrap(),
                schema::PRODUCT_ERROR,
                &message,
                Some(data),
            )
        }
    }
}

/// Termination-signal handling (Unix): SIGINT/SIGTERM are blocked in
/// this thread before the transport threads spawn (threads inherit
/// the mask); a watcher thread reports their delivery as a fatal
/// transport failure so the process exits non-zero after the same
/// cancellation/cleanup path as broken stdout.
#[cfg(unix)]
mod signals {
    use super::SessionEvent;
    use std::io::{self, ErrorKind};
    use std::sync::mpsc::Sender;

    /// Block SIGINT/SIGTERM and spawn the watcher. The watcher never
    /// exits; the process terminates when the main function returns.
    pub fn watch(events: Sender<SessionEvent>) {
        let mut set: libc::sigset_t = unsafe { std::mem::zeroed() };
        unsafe {
            libc::sigemptyset(&mut set);
            libc::sigaddset(&mut set, libc::SIGINT);
            libc::sigaddset(&mut set, libc::SIGTERM);
            // Blocking in the main thread before the transport threads
            // spawn makes every thread inherit the mask.
            libc::pthread_sigmask(libc::SIG_BLOCK, &set, std::ptr::null_mut());
        }
        let watcher_events = events.clone();
        let watcher = std::thread::Builder::new()
            .name("iprange-signals".into())
            .spawn(move || loop {
                let mut signal: libc::c_int = 0;
                // sigwait atomically consumes the pending signal; the
                // mask keeps it undeliverable to other threads.
                if unsafe { libc::sigwait(&set, &mut signal) } == 0 {
                    let _ = watcher_events.send(SessionEvent::Fatal(io::Error::new(
                        ErrorKind::Interrupted,
                        format!("terminated by signal {signal}"),
                    )));
                } else {
                    // sigwait cannot fail for a valid blocked set on
                    // the process's own signal mask; avoid a spin on
                    // the impossible error path.
                    std::thread::yield_now();
                }
            });
        // A failed spawn must not silently disable signal handling.
        if let Err(error) = watcher {
            let _ = events.send(SessionEvent::Fatal(io::Error::new(
                ErrorKind::Other,
                format!("signal watcher spawn failed: {error}"),
            )));
        }
    }
}

/// No termination-signal watcher on non-Unix platforms.
#[cfg(not(unix))]
mod signals {
    use super::SessionEvent;
    use std::sync::mpsc::Sender;

    /// No-op: termination handling is Unix-only.
    pub fn watch(_events: Sender<SessionEvent>) {}
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn request(id: &str, index: Option<usize>) -> Request {
        Request {
            id: Some(RequestId::String(id.to_owned())),
            method: "iprange.v1.system.describe".to_owned(),
            params: json!({}),
            batch_index: index,
        }
    }

    fn cancel_request(id: &str) -> Request {
        Request {
            id: None,
            method: schema::CANCEL_METHOD.into(),
            params: json!({"request_id": id}),
            batch_index: None,
        }
    }

    fn unit(entries: Vec<WorkEntry>, batch: bool) -> WorkUnit {
        let admitted = entries
            .iter()
            .filter(|entry| entry.occupies_queue())
            .count();
        WorkUnit {
            entries,
            admitted,
            batch,
        }
    }

    fn ids(payload: &Value) -> Vec<String> {
        payload
            .as_array()
            .unwrap()
            .iter()
            .map(|response| response["id"].as_str().unwrap_or_default().to_owned())
            .collect()
    }

    /// Writer that appends to a shared buffer (thread-safe capture).
    #[derive(Clone)]
    struct SharedVec(Arc<Mutex<Vec<u8>>>);

    impl Write for SharedVec {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(buf);
            Ok(buf.len())
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    /// Writer whose every write fails like stdout on a broken pipe.
    struct FailingWriter;

    impl Write for FailingWriter {
        fn write(&mut self, _buf: &[u8]) -> io::Result<usize> {
            Err(io::Error::new(io::ErrorKind::BrokenPipe, "broken pipe"))
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    /// Reader that yields one frame and then blocks forever,
    /// simulating a client that keeps stdin open without sending more.
    struct StdinOpenReader {
        remaining: &'static [u8],
    }

    impl std::io::Read for StdinOpenReader {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            if self.remaining.is_empty() {
                std::thread::park();
                return Ok(0);
            }
            let n = self.remaining.len().min(buf.len());
            buf[..n].copy_from_slice(&self.remaining[..n]);
            Ok(n)
        }
    }

    impl std::io::BufRead for StdinOpenReader {
        fn fill_buf(&mut self) -> io::Result<&[u8]> {
            if self.remaining.is_empty() {
                std::thread::park();
            }
            Ok(self.remaining)
        }
        fn consume(&mut self, amt: usize) {
            self.remaining = &self.remaining[amt..];
        }
    }

    /// Reader that delivers its input and then reports EOF on the next
    /// fill_buf, without waiting for worker output: the plain
    /// request-then-close transport pattern.
    struct PlainEofReader {
        remaining: &'static [u8],
    }

    impl std::io::Read for PlainEofReader {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            let data = self.fill_buf()?.to_vec();
            let n = data.len().min(buf.len());
            buf[..n].copy_from_slice(&data[..n]);
            self.consume(n);
            Ok(n)
        }
    }

    impl std::io::BufRead for PlainEofReader {
        fn fill_buf(&mut self) -> io::Result<&[u8]> {
            if self.remaining.is_empty() {
                return Ok(&[]);
            }
            Ok(self.remaining)
        }
        fn consume(&mut self, amt: usize) {
            self.remaining = &self.remaining[amt..];
        }
    }

    /// Reader that delivers its input and then fails like stdin on a
    /// broken pipe instead of reporting EOF: a real io error must
    /// never masquerade as a clean end of input.
    struct ErrorAfterFrameReader {
        remaining: &'static [u8],
    }

    impl std::io::Read for ErrorAfterFrameReader {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            let data = self.fill_buf()?.to_vec();
            let n = data.len().min(buf.len());
            buf[..n].copy_from_slice(&data[..n]);
            self.consume(n);
            Ok(n)
        }
    }

    impl std::io::BufRead for ErrorAfterFrameReader {
        fn fill_buf(&mut self) -> io::Result<&[u8]> {
            if self.remaining.is_empty() {
                return Err(io::Error::new(io::ErrorKind::BrokenPipe, "stdin broken"));
            }
            Ok(self.remaining)
        }
        fn consume(&mut self, amt: usize) {
            self.remaining = &self.remaining[amt..];
        }
    }

    /// Reader that delivers its bytes, then waits until the worker's
    /// response appears on the shared output before reporting EOF.
    /// Makes the EOF shutdown deterministic: the unit is complete
    /// before shutdown starts, so the worker is between units when
    /// EOF lands and the response is always written.
    struct ResponseAwareReader {
        input: Vec<u8>,
        delivered: bool,
        output: Arc<Mutex<Vec<u8>>>,
        marker: &'static [u8],
    }

    impl std::io::Read for ResponseAwareReader {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            // LineReader only uses fill_buf/consume; implement Read for
            // the BufRead supertrait as a passthrough.
            let data = self.fill_buf()?.to_vec();
            let n = data.len().min(buf.len());
            buf[..n].copy_from_slice(&data[..n]);
            self.consume(n);
            Ok(n)
        }
    }

    impl std::io::BufRead for ResponseAwareReader {
        fn fill_buf(&mut self) -> io::Result<&[u8]> {
            if !self.delivered {
                self.delivered = true;
                return Ok(&self.input);
            }
            let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
            loop {
                let seen = {
                    let output = self.output.lock().unwrap();
                    output
                        .windows(self.marker.len())
                        .any(|window| window == self.marker)
                };
                if seen {
                    return Ok(&[]);
                }
                if std::time::Instant::now() > deadline {
                    panic!("test reader: worker response never appeared");
                }
                std::thread::yield_now();
            }
        }
        fn consume(&mut self, amt: usize) {
            self.input.drain(..amt);
        }
    }

    #[test]
    fn arbitrary_precision_numeric_ids_cancel_like_any_other_id() {
        // request_id may be any integral JSON number; the cancel key must
        // match the request key for out-of-range (arbitrary precision)
        // numbers too (spec cancel contract).
        // 2**100 as a JSON number literal: serde_json arbitrary_precision
        // preserves the exact text.
        let big = json!(1267650600228229401496703205376u128);
        let key = format!("n:{big}");
        let mut session = Session::new();
        // A cancel is only valid for an admitted (pending) id.
        session.state.lock().unwrap().pending.insert(key.clone());
        let cancel = Request {
            id: Some(RequestId::String("c".into())),
            method: schema::CANCEL_METHOD.into(),
            params: json!({"request_id": big}),
            batch_index: None,
        };
        session.apply_cancel(&cancel);
        let state = session.state.lock().unwrap();
        assert!(state.cancelled.contains(&key), "missing cancel key {key:?}");
    }

    #[test]
    fn unknown_cancel_id_does_not_poison_a_later_request() {
        // A cancel for an id that was never admitted is ignored; a
        // later request reusing the id still executes and responds.
        let mut session = Session::new();
        session.apply_cancel(&cancel_request("a"));
        assert!(session.state.lock().unwrap().cancelled.is_empty());

        let state = session.state.clone();
        let response = entry_response(&state, &WorkEntry::Execute(request("a", None)));
        assert!(
            response.is_some(),
            "later request with the same id was poisoned by an ignored cancel"
        );
    }

    #[test]
    fn admitted_cancel_id_omits_the_queued_response() {
        // Read loop admitted the request (pending) and a cancel arrived
        // before the worker picked it up: the response is omitted.
        let mut session = Session::new();
        session.state.lock().unwrap().pending.insert("s:a".to_owned());
        session.apply_cancel(&cancel_request("a"));
        let response = entry_response(&session.state, &WorkEntry::Execute(request("a", None)));
        assert!(response.is_none());
    }

    #[test]
    fn cancel_after_terminal_state_is_ignored() {
        // The worker prunes both sets when a unit reaches its terminal
        // state; a cancel for the same id afterwards is ignored, so a
        // freshly admitted reuse of the id cannot be dropped.
        let mut session = Session::new();
        session.state.lock().unwrap().pending.insert("s:a".to_owned());
        session.apply_cancel(&cancel_request("a"));
        {
            let mut s = session.state.lock().unwrap();
            s.pending.remove("s:a");
            s.cancelled.remove("s:a");
        }
        session.apply_cancel(&cancel_request("a"));
        assert!(session.state.lock().unwrap().cancelled.is_empty());
    }

    #[test]
    fn malformed_cancel_params_are_ignored() {
        // The strict CANCEL schema is enforced before any cancellation:
        // extra members, non-string/non-integral request_id values,
        // missing members, and non-object params never cancel (and a
        // notification produces no response).
        let mut session = Session::new();
        session.state.lock().unwrap().pending.insert("s:a".to_owned());
        for bad in [
            json!({"request_id": "a", "extra": 1}),
            json!({"request_id": 1.5}),
            json!({"request_id": null}),
            json!({"request_id": true}),
            json!({"request_id": {}}),
            json!({"request_id": ["a"]}),
            json!({}),
            json!({"other": "a"}),
            json!([]),
        ] {
            let cancel = Request {
                id: None,
                method: schema::CANCEL_METHOD.into(),
                params: bad.clone(),
                batch_index: None,
            };
            session.apply_cancel(&cancel);
            assert!(
                session.state.lock().unwrap().cancelled.is_empty(),
                "malformed cancel cancelled a request: {bad}"
            );
        }
        // A well-formed cancel still applies after invalid ones, so the
        // strict gate never poisons valid notifications.
        session.apply_cancel(&cancel_request("a"));
        assert!(session.state.lock().unwrap().cancelled.contains("s:a"));
    }

    #[test]
    fn unanswerable_request_id_answers_32001_with_id_null() {
        // An id that alone makes every faithful response exceed the
        // response-object ceiling can never be answered: the frame
        // answers -32001 with id null and the service keeps serving
        // (the id is unusable, not an oversized input frame).
        let huge = "I".repeat(super::super::framing::RESPONSE_OBJECT_LIMIT + 100);
        let huge_request = request(&huge, None);
        assert!(preflight_unanswerable_id(&huge_request));
        let normal = request("a", None);
        assert!(!preflight_unanswerable_id(&normal));
        // The bounded fallback still returns the transport shape for
        // direct callers; production admission never reaches it.
        let response = schema::success_response(
            &RequestId::String(huge),
            json!({"method": "iprange.v1.system.describe"}),
        );
        let bounded = bounded_response(response, &huge_request);
        assert_eq!(bounded["id"], Value::Null);
        assert_eq!(
            bounded["error"]["code"],
            json!(schema::TRANSPORT_FRAME_TOO_LARGE)
        );
    }

    #[test]
    fn admission_preserves_batch_order_under_queue_pressure() {
        let in_flight = AtomicUsize::new(QUEUED_LIMIT);
        let entries = admit_frame(
            vec![
                request("a", Some(0)),
                request("b", Some(1)),
                request("c", Some(2)),
            ],
            &in_flight,
        );
        assert!(entries
            .iter()
            .all(|entry| matches!(entry, WorkEntry::Busy(_))));
        assert_eq!(in_flight.load(Ordering::Relaxed), QUEUED_LIMIT);

        let in_flight = AtomicUsize::new(QUEUED_LIMIT - 1);
        let entries = admit_frame(
            vec![
                request("a", Some(0)),
                request("b", Some(1)),
                request("c", Some(2)),
            ],
            &in_flight,
        );
        assert!(matches!(entries[0], WorkEntry::Execute(_)));
        assert!(matches!(entries[1], WorkEntry::Busy(_)));
        assert!(matches!(entries[2], WorkEntry::Busy(_)));
        assert_eq!(in_flight.load(Ordering::Relaxed), QUEUED_LIMIT);
    }

    #[test]
    fn batch_unit_answers_busy_members_inside_one_array_in_order() {
        let state = Arc::new(Mutex::new(SessionState::default()));
        let work = unit(
            vec![
                WorkEntry::Execute(request("a", Some(0))),
                WorkEntry::Busy(request("b", Some(1))),
                WorkEntry::Execute(request("c", Some(2))),
            ],
            true,
        );
        let responses: Vec<Value> = work
            .entries
            .iter()
            .filter_map(|entry| entry_response(&state, entry))
            .collect();
        let payload = Value::Array(responses);
        assert_eq!(ids(&payload), ["a", "b", "c"]);
        let members = payload.as_array().unwrap();
        assert!(members[0].get("result").is_some());
        assert_eq!(
            members[1]["error"]["code"],
            json!(schema::TRANSPORT_SERVER_BUSY)
        );
        assert_eq!(members[1]["error"]["message"], json!("server_busy"));
        assert!(members[2].get("result").is_some());
        assert!(schema::encode_response_frame(&payload).is_ok());
    }

    #[test]
    fn non_batch_unit_answers_one_standalone_object() {
        let state = Arc::new(Mutex::new(SessionState::default()));
        let work = unit(vec![WorkEntry::Execute(request("a", None))], false);
        let responses: Vec<Value> = work
            .entries
            .iter()
            .filter_map(|entry| entry_response(&state, entry))
            .collect();
        assert_eq!(responses.len(), 1);
        assert!(responses[0].get("result").is_some());
    }

    #[test]
    fn cancelled_batch_member_is_omitted_from_the_array() {
        let state = Arc::new(Mutex::new(SessionState::default()));
        {
            let mut s = state.lock().unwrap();
            s.pending.insert("s:a".to_owned());
            s.cancelled.insert("s:a".to_owned());
        }
        let work = unit(
            vec![
                WorkEntry::Execute(request("a", Some(0))),
                WorkEntry::Busy(request("b", Some(1))),
            ],
            true,
        );
        let responses: Vec<Value> = work
            .entries
            .iter()
            .filter_map(|entry| entry_response(&state, entry))
            .collect();
        assert_eq!(ids(&Value::Array(responses.clone())), ["b"]);
        assert_eq!(
            responses[0]["error"]["code"],
            json!(schema::TRANSPORT_SERVER_BUSY)
        );
    }

    #[test]
    fn oversized_complete_success_becomes_the_output_limit_product_error() {
        let long_id = "R".repeat(1_000);
        let request = request(&long_id, None);
        let big = json!({
            "method": "iprange.v1.reader.lookup",
            "matches": vec!["x".repeat(200); 400],
        });
        let oversized = schema::success_response(&RequestId::String(long_id.clone()), big.clone());
        assert!(schema::encode_response_object(&oversized).is_err());
        let bounded = bounded_response(oversized, &request);
        assert_eq!(bounded["id"], json!(long_id));
        assert_eq!(bounded["error"]["code"], json!(schema::PRODUCT_ERROR));
        assert_eq!(
            bounded["error"]["data"],
            json!({
                "code": "output_limit",
                "outcome": "read_only_failure",
            })
        );
        // The replacement must satisfy both ceilings: the complete
        // object stays under 65,000 bytes and the frame is valid.
        assert!(schema::encode_response_object(&bounded).is_ok());
        assert!(schema::encode_response_frame(&bounded).is_ok());
        let _ = big;
    }

    #[test]
    fn unencodable_request_id_answers_with_one_id_null_transport_error() {
        let huge = "I".repeat(super::super::framing::RESPONSE_OBJECT_LIMIT);
        let huge_request = request(&huge, None);
        let response = schema::success_response(
            &RequestId::String(huge),
            json!({"method": "iprange.v1.system.describe"}),
        );
        let bounded = bounded_response(response, &huge_request);
        assert_eq!(bounded["id"], Value::Null);
        assert_eq!(
            bounded["error"]["code"],
            json!(schema::TRANSPORT_FRAME_TOO_LARGE)
        );
        assert_eq!(
            bounded["error"]["message"],
            json!("response object exceeds the 65000-byte limit; request id cannot be echoed")
        );
        assert!(schema::encode_response_object(&bounded).is_ok());
        assert!(schema::encode_response_frame(&bounded).is_ok());
    }

    #[test]
    fn oversized_product_error_keeps_code_and_outcome_and_drops_details() {
        let request = request("a", None);
        let response = schema::error_response(
            &RequestId::String("a".to_owned()),
            schema::PRODUCT_ERROR,
            "snapshot preparation failed",
            Some(json!({
                "code": "name_exists",
                "outcome": "not_started",
                "details": {"residue": vec!["y".repeat(200); 400]},
            })),
        );
        assert!(schema::encode_response_object(&response).is_err());
        let bounded = bounded_response(response, &request);
        assert_eq!(bounded["error"]["code"], json!(schema::PRODUCT_ERROR));
        assert_eq!(bounded["error"]["data"]["code"], json!("name_exists"));
        assert_eq!(bounded["error"]["data"]["outcome"], json!("not_started"));
        assert!(bounded["error"]["data"].get("details").is_none());
        assert!(schema::encode_response_object(&bounded).is_ok());
    }

    #[test]
    fn eof_shutdown_executes_queued_units_under_the_cancelled_token() {
        // Units admitted before EOF are executed, never skipped: the
        // worker keeps the already-cancelled token installed by
        // shutdown, so quick work (system.describe) answers normally
        // and SDK long work aborts factually.
        let (work_tx, work_rx) = std::sync::mpsc::channel::<WorkUnit>();
        let token = Arc::new(iprange_livedb::CancellationToken::new());
        token.cancel();
        let state = Arc::new(Mutex::new(SessionState {
            shutting_down: true,
            token,
            ..SessionState::default()
        }));
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let writer = Arc::new(Mutex::new(FrameWriter::new(SharedVec(output.clone()))));
        let in_flight = Arc::new(AtomicUsize::new(3));
        let (events_tx, _events_rx) = std::sync::mpsc::channel::<SessionEvent>();

        for id in ["a", "b", "c"] {
            work_tx
                .send(unit(vec![WorkEntry::Execute(request(id, None))], false))
                .unwrap();
        }
        drop(work_tx);

        worker_loop(state, writer, in_flight.clone(), work_rx, events_tx);

        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        for id in ["a", "b", "c"] {
            assert!(
                text.contains(&format!("\"id\":\"{id}\"")),
                "queued unit {id} must answer after EOF: {text}"
            );
        }
        assert_eq!(
            in_flight.load(Ordering::Relaxed),
            0,
            "queue capacity must be released for drained units"
        );
    }

    #[test]
    fn queued_live_open_at_eof_reports_factual_cancellation() {
        // A unit still queued when EOF lands executes under the
        // cancelled token: SDK long work (a live reader open) aborts
        // with the factual cancellation outcome instead of being
        // skipped or running uncancelled.
        let fixture = crate::rpc::handlers::reader::test_support::create_direct_v6(
            "eof-queued-cancel",
        );
        let (work_tx, work_rx) = std::sync::mpsc::channel::<WorkUnit>();
        let token = Arc::new(iprange_livedb::CancellationToken::new());
        token.cancel();
        let state = Arc::new(Mutex::new(SessionState {
            shutting_down: true,
            token,
            ..SessionState::default()
        }));
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let writer = Arc::new(Mutex::new(FrameWriter::new(SharedVec(output.clone()))));
        let in_flight = Arc::new(AtomicUsize::new(1));
        let (events_tx, _events_rx) = std::sync::mpsc::channel::<SessionEvent>();

        let open = Request {
            id: Some(RequestId::String("slow".into())),
            method: "iprange.v1.reader.open".into(),
            params: crate::rpc::handlers::reader::test_support::live_source(&fixture.path),
            batch_index: None,
        };
        work_tx
            .send(unit(vec![WorkEntry::Execute(open)], false))
            .unwrap();
        drop(work_tx);

        worker_loop(state, writer, in_flight.clone(), work_rx, events_tx);

        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        let payload: Value = serde_json::from_str(text.trim()).unwrap();
        assert_eq!(payload["id"], json!("slow"));
        assert_eq!(
            payload["error"]["data"]["code"],
            json!("cancelled"),
            "queued SDK work must end in a factual outcome: {text}"
        );
        assert_eq!(in_flight.load(Ordering::Relaxed), 0);
        fixture.remove();
    }

    #[test]
    fn worker_write_failure_sends_fatal_and_stops() {
        let (work_tx, work_rx) = std::sync::mpsc::channel::<WorkUnit>();
        let state = Arc::new(Mutex::new(SessionState::default()));
        let writer = Arc::new(Mutex::new(FrameWriter::new(FailingWriter)));
        let in_flight = Arc::new(AtomicUsize::new(2));
        let (events_tx, events_rx) = std::sync::mpsc::channel::<SessionEvent>();

        work_tx
            .send(unit(vec![WorkEntry::Execute(request("a", None))], false))
            .unwrap();
        work_tx
            .send(unit(vec![WorkEntry::Execute(request("b", None))], false))
            .unwrap();
        drop(work_tx);

        worker_loop(state, writer, in_flight.clone(), work_rx, events_tx);

        match events_rx.recv().unwrap() {
            SessionEvent::Fatal(err) => assert_eq!(err.kind(), io::ErrorKind::BrokenPipe),
            other => panic!("expected Fatal, got {other:?}"),
        }
        assert_eq!(
            in_flight.load(Ordering::Relaxed),
            1,
            "the unit after the write failure must not be drained"
        );
    }

    #[test]
    fn broken_stdout_exits_nonzero_through_the_fatal_path() {
        // The client keeps stdin open; the worker's first write fails
        // and the main loop turns it into a fatal exit.
        let reader = StdinOpenReader {
            remaining:
                b"{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n",
        };
        let session = Session::new();
        let err = session.run(reader, FailingWriter).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::BrokenPipe);
    }

    #[test]
    fn eof_after_one_request_answers_it_and_exits_zero() {
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let reader = ResponseAwareReader {
            input:
                b"{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n"
                    .to_vec(),
            delivered: false,
            output: output.clone(),
            marker: b"\"id\":\"1\"",
        };
        let session = Session::new();
        session.run(reader, SharedVec(output.clone())).unwrap();
        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        assert!(text.contains("\"id\":\"1\""), "unexpected output: {text}");
    }

    #[test]
    fn eof_immediately_after_one_request_answers_it_and_exits_zero() {
        // The plain request-then-close transport pattern (the T1
        // reproducer): the unit is admitted before EOF, shutdown must
        // execute it under the cancelled token and flush the response
        // frame instead of skipping it.
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let reader = PlainEofReader {
            remaining:
                b"{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n",
        };
        let session = Session::new();
        session.run(reader, SharedVec(output.clone())).unwrap();
        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        assert!(text.contains("\"id\":\"1\""), "unexpected output: {text}");
    }

    #[test]
    fn stdin_io_error_is_fatal_not_eof() {
        // A real stdin read error must surface as a fatal
        // input-transport event (non-zero exit) instead of the clean
        // zero-exit EOF path.
        let reader = ErrorAfterFrameReader {
            remaining:
                b"{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n",
        };
        let session = Session::new();
        let err = session
            .run(reader, SharedVec(Arc::new(Mutex::new(Vec::new()))))
            .unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::BrokenPipe);
    }

    #[test]
    fn eof_shutdown_closes_registered_live_readers() {
        // Opening a live reader and then closing stdin must release the
        // sidecar reader slot: shutdown closes every registered live
        // reader (spec Shutdown). The strongest evidence is the sidecar
        // slot itself: slot 0 lives at page 0 + slot 0 (offset 0x1000,
        // 16 bytes) and a cleared slot is all zero.
        let fixture = crate::rpc::handlers::reader::test_support::create_direct_v6(
            "eof-closes-readers",
        );
        let frame = format!(
            "{{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"method\":\"iprange.v1.reader.open\",\"params\":{}}}\n",
            crate::rpc::handlers::reader::test_support::live_source(&fixture.path)
        );
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        // Deliver the frame, wait for the open response, then EOF, so
        // the reader is registered before shutdown starts.
        let reader = ResponseAwareReader {
            input: frame.into_bytes(),
            delivered: false,
            output: output.clone(),
            marker: b"\"reader\":\"",
        };
        let session = Session::new();
        session.run(reader, SharedVec(output.clone())).unwrap();

        let sidecar = std::fs::read(fixture.sidecar()).unwrap();
        assert!(
            sidecar.len() >= 0x1010,
            "sidecar too small: {} bytes",
            sidecar.len()
        );
        assert!(
            sidecar[0x1000..0x1010].iter().all(|&byte| byte == 0),
            "live reader slot must be cleared at EOF shutdown"
        );
        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        assert!(
            text.contains("\"method\":\"iprange.v1.reader.open\""),
            "open must have succeeded before EOF: {text}"
        );
        fixture.remove();
    }
}
