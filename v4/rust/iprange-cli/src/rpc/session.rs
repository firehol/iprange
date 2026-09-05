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
//! - one active request set plus at most 16 queued requests; the
//!   admission counter counts queued members only and the one active
//!   member is not counted, so a member frees its slot when it starts
//!   executing; a request exceeding the bound fails with -32002
//!   server_busy;
//! - the reader stays active while work executes so cancellation
//!   and EOF are observed;
//! - the control plane (cancelled/pending ids, active keys, the
//!   cancellation token, shutting-down, fatal-error) is locked
//!   separately from connection resources, so a cancel notification,
//!   EOF, or termination signal can always cancel the active unit's
//!   token while its handler is running;
//! - cancel notifications apply during the frame scan: only ids
//!   admitted and not yet terminal (`pending`) are valid targets, so
//!   cancelling an id never poisons a later request that reuses it;
//!   a same-batch cancel may target an earlier sibling (already
//!   admitted and marked pending) but not a later sibling (not yet
//!   admitted); queued matches are skipped without a response; the
//!   active request set is signalled only when it contains the
//!   cancelled id;
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
//!   id null, but the service keeps serving; in a batch the
//!   unanswerable element answers -32001 with id null in its
//!   position in the response array and every sibling executes
//!   normally;
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

/// Cancellation and shutdown control plane, locked separately from
/// [`SessionState`].
///
/// A handler may run for a long time while the session-state mutex is
/// held (the worker locks it around every handler call), so a cancel
/// notification, EOF, or termination signal must not need that mutex
/// to reach the active unit's token. None of the fields here are ever
/// touched by a handler; the session loop and the worker update them
/// under this lock only, in short critical sections.
struct SessionControl {
    /// Request ids cancelled by the transport. Always a subset of
    /// `pending`: ids are removed when their unit reaches its terminal
    /// state so a reused id starts with a clean slate.
    cancelled: HashSet<String>,
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
    /// Cancellation signal for the active work unit; the worker
    /// replaces it once per unit.
    token: Arc<CancellationToken>,
    /// Ids of the request set currently executing.
    active_keys: HashSet<String>,
}

impl SessionControl {
    fn new() -> Self {
        SessionControl {
            cancelled: HashSet::default(),
            pending: HashSet::default(),
            shutting_down: false,
            fatal_error: None,
            token: Arc::new(CancellationToken::new()),
            active_keys: HashSet::default(),
        }
    }
}

/// Connection-owned state shared by handlers.
pub struct SessionState {
    /// Shared reader/cursor resources and their connection limits.
    pub resources: ConnectionState,
    /// Id of the request currently executing; cursor handlers size pages
    /// against the complete response-object ceiling using this id.
    pub active_request_id: Option<RequestId>,
    /// Cancellation/shutdown control plane; never held by handlers.
    control: Arc<Mutex<SessionControl>>,
}

impl Default for SessionState {
    fn default() -> Self {
        SessionState {
            resources: ConnectionState::default(),
            active_request_id: None,
            control: Arc::new(Mutex::new(SessionControl::new())),
        }
    }
}

impl SessionState {
    /// Clone of the active unit's cancellation token. Handlers poll
    /// this token during long SDK work; the session loop can cancel it
    /// at any time through the control plane.
    pub fn token(&self) -> Arc<CancellationToken> {
        self.control.lock().unwrap().token.clone()
    }

    /// Replace the active unit's token (test support: the worker
    /// installs tokens through its direct control-lock scope).
    #[cfg(test)]
    pub(crate) fn install_token(&self, token: Arc<CancellationToken>) {
        self.control.lock().unwrap().token = token;
    }
}
/// One element of a decoded frame in frame order.
///
/// `Busy` marks an element the read loop rejected with `server_busy`;
/// `Unanswerable` marks an element whose id alone cannot be echoed
/// within the response-object ceiling (-32001, id null). Keeping
/// rejected and unanswerable elements in position lets the worker emit
/// exactly one batch response array whose members follow the request
/// order.
enum WorkEntry {
    Execute(Request),
    Busy(Request),
    Unanswerable(Request),
}

impl WorkEntry {
    fn request(&self) -> &Request {
        match self {
            Self::Execute(request) | Self::Busy(request) | Self::Unanswerable(request) => request,
        }
    }
}

/// One decoded frame queued as a unit: array-order execution and one
/// response frame per input frame.
struct WorkUnit {
    entries: Vec<WorkEntry>,
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
    /// Resources and active-request identity; the worker holds this
    /// lock for the whole duration of a handler call.
    state: Arc<Mutex<SessionState>>,
    /// Cancellation/shutdown control plane; never held by handlers,
    /// so cancel/EOF always reach an active unit's token.
    control: Arc<Mutex<SessionControl>>,
    /// Requests admitted but not yet executing; a member frees its
    /// slot when it starts running, so this counts queued members
    /// only while a member is active.
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
        let control = Arc::new(Mutex::new(SessionControl::new()));
        let state = Arc::new(Mutex::new(SessionState {
            resources: ConnectionState::default(),
            active_request_id: None,
            control: Arc::clone(&control),
        }));
        Session {
            state,
            control,
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
        let worker_control = Arc::clone(&self.control);
        let worker_writer = Arc::clone(&writer);
        let worker_in_flight = Arc::clone(&self.in_flight);
        let worker_events = events_tx.clone();
        let work_rx = self.work_rx.take().expect("run once");
        let worker = std::thread::Builder::new()
            .name("iprange-jsonrpc".into())
            .spawn(move || {
                worker_loop(
                    worker_state,
                    worker_control,
                    worker_writer,
                    worker_in_flight,
                    work_rx,
                    worker_events,
                )
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

    /// Cancel one request id during the frame scan.
    ///
    /// The notification params must match the strict CANCEL schema
    /// (exactly one member `request_id`, string or integral number)
    /// before anything is cancelled; an invalid notification is
    /// ignored and produces no response. Only ids that were admitted
    /// before this element (pending ids from earlier frames, plus
    /// earlier siblings of the same frame, which the scan marked
    /// pending as it admitted them) and have not reached their
    /// terminal state can be cancelled: unknown, already terminal, and
    /// not-yet-admitted ids are ignored, so cancelling an id never
    /// poisons a later request that reuses it. An id in the
    /// currently executing request set also cancels the unit's
    /// cancellation token. The control plane is locked independently
    /// of session resources, so this applies even while a handler is
    /// running.
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
        let mut control = self.control.lock().unwrap();
        if !control.pending.contains(&cancel_id) {
            return;
        }
        control.cancelled.insert(cancel_id.clone());
        if control.active_keys.contains(&cancel_id) {
            control.token.cancel();
        }
    }

    /// Shared EOF/fatal cleanup: mark the session shutting down,
    /// cancel the current token (the active unit's token once the
    /// worker installed it), and close the work channel so the worker
    /// exits after draining queued units.
    fn begin_shutdown(&mut self) {
        {
            let mut control = self.control.lock().unwrap();
            control.shutting_down = true;
            control.token.cancel();
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
            self.control.lock().unwrap().fatal_error.take().map(|(kind, message)| {
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
    control: Arc<Mutex<SessionControl>>,
    writer: Arc<Mutex<FrameWriter<W>>>,
    in_flight: Arc<AtomicUsize>,
    rx: Receiver<WorkUnit>,
    events: Sender<SessionEvent>,
) {
    while let Ok(unit) = rx.recv() {
        let keys: HashSet<String> = unit
            .entries
            .iter()
            .filter(|entry| matches!(entry, WorkEntry::Execute(_)))
            .filter_map(|entry| request_key(entry.request()))
            .collect();
        // Token/flag update in one control lock scope: if shutdown
        // lands between a check and a fresh token install, the fresh
        // token would escape cancellation. A unit admitted before EOF
        // installs its own token (shutdown then cancels it: active,
        // factual). A unit still queued when EOF lands keeps the
        // already-cancelled token installed by begin_shutdown: quick
        // work answers normally, SDK long work aborts factually, and
        // no admitted unit is skipped. The channel close (work_tx
        // dropped by shutdown) ends the loop.
        {
            let mut c = control.lock().unwrap();
            if !c.shutting_down {
                c.token = Arc::new(CancellationToken::new());
            }
            c.active_keys = keys.clone();
        }
        let mut responses: Vec<Value> = Vec::with_capacity(unit.entries.len());
        for entry in &unit.entries {
            // A member frees its queue slot when it starts executing;
            // earlier members of the same unit stay counted until
            // then. Cancelled executes were admitted and still free
            // their slot; busy and unanswerable entries never
            // occupied one.
            if matches!(entry, WorkEntry::Execute(_)) {
                in_flight.fetch_sub(1, Ordering::Relaxed);
            }
            if let Some(response) = entry_response(&state, &control, entry) {
                responses.push(response);
            }
        }
        {
            // Terminal state: the unit's request ids are no longer
            // cancellation targets, so a later request reusing an id
            // starts with a clean slate.
            let mut c = control.lock().unwrap();
            for key in &keys {
                c.cancelled.remove(key);
                c.pending.remove(key);
            }
            c.active_keys.clear();
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
            control.lock().unwrap().fatal_error = Some((err.kind(), err.to_string()));
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
    // The scan admits one element at a time in array order: a cancel
    // element is applied immediately against ids admitted so far (an
    // active/queued request from an earlier frame or an earlier
    // sibling of this frame), and every ordinary element is marked
    // pending the moment it is admitted, so a later cancel element in
    // the same array can target it. Elements later in the array are
    // not yet admitted when a cancel is scanned, so they are not
    // cancellation targets (spec: only elements already queued).
    let mut entries = Vec::with_capacity(requests.len());
    let mut batch = false;
    for request in requests {
        if request.method == schema::CANCEL_METHOD {
            session.apply_cancel(&request);
            continue;
        }
        batch |= request.batch_index.is_some();
        let entry = admit_one(request, &session.in_flight);
        if matches!(entry, WorkEntry::Execute(_)) {
            mark_pending(&session.control, &entry);
        }
        entries.push(entry);
    }
    if entries.is_empty() {
        return Ok(());
    }
    if batch {
        // Every element answers inside one array in the
        // frame's order, including busy rejections.
        session
            .work_tx
            .as_ref()
            .unwrap()
            .send(WorkUnit {
                entries,
                batch,
            })
            .map_err(worker_gone)?;
    } else {
        // A single request keeps the standalone frame; a busy
        // rejection or an unanswerable id is answered immediately
        // and never occupies queue capacity.
        match entries.first() {
            Some(WorkEntry::Busy(request)) => {
                let payload = bounded_response(busy_response(request), request);
                let text = schema::encode_response_frame(&payload)
                    .expect("bounded response within frame limit");
                let mut w = writer.lock().unwrap();
                w.write_line(&text)?;
            }
            Some(WorkEntry::Unanswerable(_)) => {
                let text = schema::encode_response_frame(&unanswerable_response())
                    .expect("constant transport error within limits");
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
                        batch,
                    })
                    .map_err(worker_gone)?;
            }
        }
    }
    Ok(())
}

/// Record one admitted Execute key as a valid cancellation target.
///
/// Called during the frame scan, immediately after admission, so a
/// cancel element later in the same array can target the element.
fn mark_pending(control: &Arc<Mutex<SessionControl>>, entry: &WorkEntry) {
    if !matches!(entry, WorkEntry::Execute(_)) {
        return;
    }
    let mut c = control.lock().unwrap();
    if let Some(key) = request_key(entry.request()) {
        c.pending.insert(key);
    }
}

/// The worker thread ended (fatal write failure already reported);
/// the transport cannot continue.
fn worker_gone(_: std::sync::mpsc::SendError<WorkUnit>) -> io::Error {
    io::Error::new(io::ErrorKind::BrokenPipe, "jsonrpc worker terminated")
}

/// Admit ordinary frame elements against the queue bound.
///
/// An element whose id alone cannot be echoed within the
/// response-object ceiling becomes `Unanswerable` and never occupies
/// queue capacity. Rejected elements stay in position as `Busy`
/// entries and also never occupy queue capacity.
fn admit_one(request: Request, in_flight: &AtomicUsize) -> WorkEntry {
    if preflight_unanswerable_id(&request) {
        WorkEntry::Unanswerable(request)
    } else if in_flight.load(Ordering::Relaxed) >= QUEUED_LIMIT {
        WorkEntry::Busy(request)
    } else {
        in_flight.fetch_add(1, Ordering::Relaxed);
        WorkEntry::Execute(request)
    }
}

#[cfg(test)]
fn admit_frame(requests: Vec<Request>, in_flight: &AtomicUsize) -> Vec<WorkEntry> {
    requests
        .into_iter()
        .map(|request| admit_one(request, in_flight))
        .collect()
}

fn busy_response(request: &Request) -> Value {
    schema::error_response(
        request.id.as_ref().expect("ordinary requests carry ids"),
        schema::TRANSPORT_SERVER_BUSY,
        "server_busy",
        None,
    )
}

/// The -32001 id:null transport response for an element whose id
/// alone cannot be echoed within the response-object ceiling. Constant
/// payload shared by the immediate single-request path and in-position
/// batch elements.
fn unanswerable_response() -> Value {
    SchemaError::response(
        None,
        SchemaError {
            code: schema::TRANSPORT_FRAME_TOO_LARGE,
            message: "request id cannot be echoed within the response object limit".into(),
        },
    )
}

/// Build one frame-ordered response object, or `None` for a request
/// cancelled before execution (it is omitted from a batch array).
fn entry_response(
    state: &Arc<Mutex<SessionState>>,
    control: &Arc<Mutex<SessionControl>>,
    entry: &WorkEntry,
) -> Option<Value> {
    match entry {
        WorkEntry::Execute(request) => {
            let cancelled = {
                let c = control.lock().unwrap();
                request_key(request)
                    .map(|k| c.cancelled.contains(&k))
                    .unwrap_or(false)
            };
            if cancelled {
                return None;
            }
            Some(bounded_response(execute(state, request), request))
        }
        WorkEntry::Busy(request) => Some(bounded_response(busy_response(request), request)),
        WorkEntry::Unanswerable(_) => Some(unanswerable_response()),
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
/// can never be answered. Admission answers it -32001 with id null:
/// as the standalone response for a single request, or in position
/// inside a batch response array. An unanswerable id is an ordinary
/// (if unusable) request, not an oversized input frame, so the
/// service keeps serving after it.
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
        WorkUnit { entries, batch }
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

    /// Reader that delivers one input line per fill_buf call (a batch
    /// frame, then a follow-up frame), then waits until `marker`
    /// appears in the worker output before reporting EOF. Delivering
    /// one line at a time lets the next read_line observe the
    /// following frame (ResponseAwareReader returns the whole buffer
    /// exactly once, so it cannot carry a second frame).
    struct FrameSequenceReader {
        input: Vec<u8>,
        offset: usize,
        waiting: bool,
        output: Arc<Mutex<Vec<u8>>>,
        marker: &'static [u8],
    }

    impl std::io::Read for FrameSequenceReader {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            let data = self.fill_buf()?.to_vec();
            let n = data.len().min(buf.len());
            buf[..n].copy_from_slice(&data[..n]);
            self.consume(n);
            Ok(n)
        }
    }

    impl std::io::BufRead for FrameSequenceReader {
        fn fill_buf(&mut self) -> io::Result<&[u8]> {
            if !self.waiting {
                if self.offset == 0 {
                    // Deliver only the first line; the next read_line
                    // observes the following frame on a later fill_buf.
                    let nl = self.input.iter().position(|&b| b == b'\n').unwrap();
                    return Ok(&self.input[..nl + 1]);
                }
                if self.offset < self.input.len() {
                    return Ok(&self.input[self.offset..]);
                }
                self.waiting = true;
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
            self.offset = (self.offset + amt).min(self.input.len());
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
        session.control.lock().unwrap().pending.insert(key.clone());
        let cancel = Request {
            id: Some(RequestId::String("c".into())),
            method: schema::CANCEL_METHOD.into(),
            params: json!({"request_id": big}),
            batch_index: None,
        };
        session.apply_cancel(&cancel);
        let control = session.control.lock().unwrap();
        assert!(control.cancelled.contains(&key), "missing cancel key {key:?}");
    }

    #[test]
    fn unknown_cancel_id_does_not_poison_a_later_request() {
        // A cancel for an id that was never admitted is ignored; a
        // later request reusing the id still executes and responds.
        let mut session = Session::new();
        session.apply_cancel(&cancel_request("a"));
        assert!(session.control.lock().unwrap().cancelled.is_empty());

        let state = session.state.clone();
        let control = session.control.clone();
        let response = entry_response(&state, &control, &WorkEntry::Execute(request("a", None)));
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
        session.control.lock().unwrap().pending.insert("s:a".to_owned());
        session.apply_cancel(&cancel_request("a"));
        let response = entry_response(
            &session.state,
            &session.control,
            &WorkEntry::Execute(request("a", None)),
        );
        assert!(response.is_none());
    }

    #[test]
    fn cancel_after_terminal_state_is_ignored() {
        // The worker prunes both sets when a unit reaches its terminal
        // state; a cancel for the same id afterwards is ignored, so a
        // freshly admitted reuse of the id cannot be dropped.
        let mut session = Session::new();
        session.control.lock().unwrap().pending.insert("s:a".to_owned());
        session.apply_cancel(&cancel_request("a"));
        {
            let mut c = session.control.lock().unwrap();
            c.pending.remove("s:a");
            c.cancelled.remove("s:a");
        }
        session.apply_cancel(&cancel_request("a"));
        assert!(session.control.lock().unwrap().cancelled.is_empty());
    }

    #[test]
    fn malformed_cancel_params_are_ignored() {
        // The strict CANCEL schema is enforced before any cancellation:
        // extra members, non-string/non-integral request_id values,
        // missing members, and non-object params never cancel (and a
        // notification produces no response).
        let mut session = Session::new();
        session.control.lock().unwrap().pending.insert("s:a".to_owned());
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
                session.control.lock().unwrap().cancelled.is_empty(),
                "malformed cancel cancelled a request: {bad}"
            );
        }
        // A well-formed cancel still applies after invalid ones, so the
        // strict gate never poisons valid notifications.
        session.apply_cancel(&cancel_request("a"));
        assert!(session.control.lock().unwrap().cancelled.contains("s:a"));
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
    fn active_batch_member_frees_one_slot_at_a_time() {
        // A 16-member batch whose first member is slow occupies one
        // active slot plus 15 queued slots. While member 1 is blocked,
        // the 15 unexecuted batch members must still count against the
        // admission bound, so of 10 pipelined single requests exactly
        // 9 answer -32002 and 1 is admitted. Pre-fix the worker
        // subtracted the whole batch from the admission counter when
        // it picked the unit up, leaving the 15 pending members
        // uncounted and admitting all 10.
        let mut session = Session::new();
        // Simulate the slow first batch member: a helper thread holds
        // the session-state mutex (the worker locks it around every
        // handler call) until the test releases it.
        let state = session.state.clone();
        let (locked_tx, locked_rx) = std::sync::mpsc::channel::<()>();
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        let blocker = std::thread::spawn(move || {
            let _guard = state.lock().unwrap();
            let _ = locked_tx.send(());
            let _ = release_rx.recv();
        });
        locked_rx.recv().unwrap();

        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let writer = Arc::new(Mutex::new(FrameWriter::new(SharedVec(output.clone()))));
        let (events_tx, _events_rx) = std::sync::mpsc::channel::<SessionEvent>();
        let work_rx = session.work_rx.take().unwrap();
        let worker_state = session.state.clone();
        let worker_control = session.control.clone();
        let worker_writer = Arc::clone(&writer);
        let worker_in_flight = Arc::clone(&session.in_flight);
        let worker = std::thread::spawn(move || {
            worker_loop(
                worker_state,
                worker_control,
                worker_writer,
                worker_in_flight,
                work_rx,
                events_tx,
            )
        });

        // One 16-member batch through the real admission path: all 16
        // members are admitted (the queue starts empty) and the worker
        // blocks on member 1 behind the held state lock.
        let batch: Vec<Value> = (1..=16)
            .map(|i| {
                json!({
                    "jsonrpc": "2.0",
                    "id": format!("b{i}"),
                    "method": "iprange.v1.system.describe",
                    "params": {},
                })
            })
            .collect();
        handle_frame(
            &mut session,
            serde_json::to_vec(&Value::Array(batch)).unwrap(),
            &writer,
        )
        .unwrap();

        // Wait until the worker consumed the batch and freed member
        // 1's slot (15 counted, 1 blocked on the state lock) before
        // admitting the singles, so the admission decisions are
        // deterministic.
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
        while session.in_flight.load(Ordering::Relaxed) != 15 {
            assert!(
                std::time::Instant::now() < deadline,
                "worker never picked up the batch"
            );
            std::thread::yield_now();
        }

        // 10 pipelined singles while member 1 is still blocked: 1 is
        // admitted, the other 9 answer -32002 from the dispatcher.
        for i in 1..=10 {
            let frame = json!({
                "jsonrpc": "2.0",
                "id": format!("s{i}"),
                "method": "iprange.v1.system.describe",
                "params": {},
            });
            handle_frame(&mut session, serde_json::to_vec(&frame).unwrap(), &writer).unwrap();
        }
        assert_eq!(
            session.in_flight.load(Ordering::Relaxed),
            16,
            "one single must be admitted while the 15 batch members stay queued"
        );
        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        let busy_lines: Vec<&str> = text.lines().collect();
        assert_eq!(busy_lines.len(), 9, "exactly 9 busy responses: {text}");
        for (i, line) in busy_lines.iter().enumerate() {
            let payload: Value = serde_json::from_str(line).unwrap();
            assert_eq!(payload["id"], json!(format!("s{}", i + 2)));
            assert_eq!(
                payload["error"]["code"],
                json!(schema::TRANSPORT_SERVER_BUSY)
            );
        }

        // Let the slow member finish: the batch answers as one array
        // in frame order, then the admitted single executes.
        let _ = release_tx.send(());
        blocker.join().unwrap();
        let _ = session.work_tx.take();
        worker.join().unwrap();
        assert_eq!(
            session.in_flight.load(Ordering::Relaxed),
            0,
            "every execute must free its slot when it starts"
        );

        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        let mut lines = text.lines();
        // Skip the 9 busy responses already asserted pre-release; the
        // worker wrote everything after them.
        for _ in 0..9 {
            lines.next().expect("busy response line");
        }
        let payload: Value = serde_json::from_str(lines.next().expect("batch response")).unwrap();
        let members = payload.as_array().expect("batch answers one array");
        assert_eq!(members.len(), 16, "all batch members must answer: {text}");
        for (i, member) in members.iter().enumerate() {
            assert_eq!(member["id"], json!(format!("b{}", i + 1)));
            assert!(member.get("result").is_some());
        }
        let payload: Value =
            serde_json::from_str(lines.next().expect("admitted single response")).unwrap();
        assert_eq!(payload["id"], json!("s1"));
        assert!(payload.get("result").is_some());
        assert!(lines.next().is_none(), "no extra output: {text}");
    }

    #[test]
    fn batch_unit_answers_busy_members_inside_one_array_in_order() {
        let state = Arc::new(Mutex::new(SessionState::default()));
        let control = state.lock().unwrap().control.clone();
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
            .filter_map(|entry| entry_response(&state, &control, entry))
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
    fn batch_unit_answers_unanswerable_members_in_position() {
        // Worker-level ordering: the -32001 id:null element keeps its
        // frame position and the sibling executes normally.
        let state = Arc::new(Mutex::new(SessionState::default()));
        let control = state.lock().unwrap().control.clone();
        let huge = "I".repeat(super::super::framing::RESPONSE_OBJECT_LIMIT + 100);
        let work = unit(
            vec![
                WorkEntry::Unanswerable(request(&huge, Some(0))),
                WorkEntry::Execute(request("a", Some(1))),
            ],
            true,
        );
        let responses: Vec<Value> = work
            .entries
            .iter()
            .filter_map(|entry| entry_response(&state, &control, entry))
            .collect();
        let payload = Value::Array(responses);
        let members = payload.as_array().unwrap();
        assert_eq!(members.len(), 2);
        assert_eq!(members[0]["id"], Value::Null);
        assert_eq!(
            members[0]["error"]["code"],
            json!(schema::TRANSPORT_FRAME_TOO_LARGE)
        );
        assert_eq!(members[1]["id"], json!("a"));
        assert!(members[1].get("result").is_some());
        assert!(schema::encode_response_frame(&payload).is_ok());
    }

    #[test]
    fn unanswerable_entries_never_occupy_queue_capacity() {
        // An unanswerable id is answered without admission, so it
        // never consumes queue capacity even when the queue is full;
        // siblings still get normal busy/execute admission.
        let huge = "I".repeat(super::super::framing::RESPONSE_OBJECT_LIMIT + 100);
        let in_flight = AtomicUsize::new(QUEUED_LIMIT);
        let entries = admit_frame(vec![request(&huge, Some(0)), request("b", Some(1))], &in_flight);
        assert!(matches!(entries[0], WorkEntry::Unanswerable(_)));
        assert!(matches!(entries[1], WorkEntry::Busy(_)));
        assert_eq!(in_flight.load(Ordering::Relaxed), QUEUED_LIMIT);

        let in_flight = AtomicUsize::new(QUEUED_LIMIT - 1);
        let entries = admit_frame(
            vec![request(&huge, Some(0)), request("a", Some(1))],
            &in_flight,
        );
        assert!(matches!(entries[0], WorkEntry::Unanswerable(_)));
        assert!(matches!(entries[1], WorkEntry::Execute(_)));
        assert_eq!(in_flight.load(Ordering::Relaxed), QUEUED_LIMIT);
    }

    #[test]
    fn unanswerable_entries_are_not_cancellation_targets() {
        // Only admitted Execute ids become pending cancellation
        // targets; an unanswerable element answers unconditionally.
        let state = Arc::new(Mutex::new(SessionState::default()));
        let control = state.lock().unwrap().control.clone();
        let huge = "I".repeat(super::super::framing::RESPONSE_OBJECT_LIMIT + 100);
        let huge_key = format!("s:{huge}");
        let entries = vec![
            WorkEntry::Unanswerable(request(&huge, Some(0))),
            WorkEntry::Execute(request("a", Some(1))),
        ];
        for entry in &entries {
            mark_pending(&control, entry);
        }
        let c = control.lock().unwrap();
        assert!(!c.pending.contains(&huge_key), "unanswerable id must not be cancellable");
        assert!(c.pending.contains("s:a"));
    }

    #[test]
    fn non_batch_unit_answers_one_standalone_object() {
        let state = Arc::new(Mutex::new(SessionState::default()));
        let control = state.lock().unwrap().control.clone();
        let work = unit(vec![WorkEntry::Execute(request("a", None))], false);
        let responses: Vec<Value> = work
            .entries
            .iter()
            .filter_map(|entry| entry_response(&state, &control, entry))
            .collect();
        assert_eq!(responses.len(), 1);
        assert!(responses[0].get("result").is_some());
    }

    #[test]
    fn cancelled_batch_member_is_omitted_from_the_array() {
        let state = Arc::new(Mutex::new(SessionState::default()));
        let control = state.lock().unwrap().control.clone();
        {
            let mut c = control.lock().unwrap();
            c.pending.insert("s:a".to_owned());
            c.cancelled.insert("s:a".to_owned());
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
            .filter_map(|entry| entry_response(&state, &control, entry))
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
        let state = Arc::new(Mutex::new(SessionState::default()));
        let control = state.lock().unwrap().control.clone();
        {
            let mut c = control.lock().unwrap();
            c.shutting_down = true;
            c.token.cancel();
        }
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

        worker_loop(state, control, writer, in_flight.clone(), work_rx, events_tx);

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
        let state = Arc::new(Mutex::new(SessionState::default()));
        let control = state.lock().unwrap().control.clone();
        {
            let mut c = control.lock().unwrap();
            c.shutting_down = true;
            c.token.cancel();
        }
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

        worker_loop(state, control, writer, in_flight.clone(), work_rx, events_tx);

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
        let control = state.lock().unwrap().control.clone();
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

        worker_loop(state, control, writer, in_flight.clone(), work_rx, events_tx);

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
    fn batch_with_unanswerable_id_answers_elements_in_order_and_keeps_serving() {
        // A batch element whose id alone cannot be echoed answers
        // -32001 with id null in its position; siblings execute
        // normally and the connection keeps serving (spec batch
        // contract: one response array, elements in frame order).
        let huge = "I".repeat(super::super::framing::RESPONSE_OBJECT_LIMIT + 100);
        let batch = json!([
            {"jsonrpc": "2.0", "id": huge, "method": "iprange.v1.system.describe", "params": {}},
            {"jsonrpc": "2.0", "id": "ok", "method": "iprange.v1.system.describe", "params": {}},
        ]);
        let follow_up = json!({
            "jsonrpc": "2.0",
            "id": "after",
            "method": "iprange.v1.system.describe",
            "params": {},
        });
        let input = format!("{batch}\n{follow_up}\n").into_bytes();
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let reader = FrameSequenceReader {
            input,
            offset: 0,
            waiting: false,
            output: output.clone(),
            marker: b"\"id\":\"after\"",
        };
        let session = Session::new();
        session.run(reader, SharedVec(output.clone())).unwrap();
        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        let mut lines = text.lines();
        let payload: Value = serde_json::from_str(lines.next().expect("batch response")).unwrap();
        let members = payload.as_array().expect("batch answers exactly one array");
        assert_eq!(members.len(), 2, "both elements must answer: {text}");
        assert_eq!(members[0]["id"], Value::Null);
        assert_eq!(
            members[0]["error"]["code"],
            json!(schema::TRANSPORT_FRAME_TOO_LARGE)
        );
        assert_eq!(
            members[0]["error"]["message"],
            json!("request id cannot be echoed within the response object limit")
        );
        // The sibling executed the real method, not a busy error.
        assert_eq!(members[1]["id"], json!("ok"));
        assert!(members[1].get("error").is_none());
        assert_eq!(
            members[1]["result"]["method"],
            json!("iprange.v1.system.describe")
        );
        // The connection kept serving after the batch.
        let follow_up_payload: Value =
            serde_json::from_str(lines.next().expect("follow-up response")).unwrap();
        assert_eq!(follow_up_payload["id"], json!("after"));
        assert!(follow_up_payload.get("result").is_some());
        assert!(lines.next().is_none(), "no extra output: {text}");
    }

    #[test]
    fn single_unanswerable_request_answers_standalone_32001() {
        // The existing standalone shape for a single request whose id
        // cannot be echoed: one object with id null and -32001, never
        // an array.
        let huge = "I".repeat(super::super::framing::RESPONSE_OBJECT_LIMIT + 100);
        let frame = json!({
            "jsonrpc": "2.0",
            "id": huge,
            "method": "iprange.v1.system.describe",
            "params": {},
        });
        let input = format!("{frame}\n").into_bytes();
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let reader = ResponseAwareReader {
            input,
            delivered: false,
            output: output.clone(),
            marker: b"\"id\":null",
        };
        let session = Session::new();
        session.run(reader, SharedVec(output.clone())).unwrap();
        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        let payload: Value = serde_json::from_str(text.trim()).unwrap();
        assert!(
            payload.as_array().is_none(),
            "single request answers one object: {text}"
        );
        assert_eq!(payload["id"], Value::Null);
        assert_eq!(
            payload["error"]["code"],
            json!(schema::TRANSPORT_FRAME_TOO_LARGE)
        );
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

    #[test]
    fn cancel_and_eof_reach_an_active_handler_holding_the_state_lock() {
        // P1 regression: a cancel notification or EOF arriving while a
        // handler runs must reach the active unit's token even though
        // the worker holds the session-state mutex for the whole
        // handler call. Previously the control fields (pending,
        // cancelled, active keys, token) lived behind that same mutex,
        // so cancellation blocked until the handler finished: a
        // Ctrl+C left the process alive until the work ended.
        let mut session = Session::new();
        session
            .control
            .lock()
            .unwrap()
            .pending
            .insert("s:slow".to_owned());
        session
            .control
            .lock()
            .unwrap()
            .active_keys
            .insert("s:slow".to_owned());

        // Simulate the in-flight handler: a helper thread holds the
        // session-state (resources) mutex until signalled.
        let big = session.state.clone();
        let (locked_tx, locked_rx) = std::sync::mpsc::channel::<()>();
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        let handler = std::thread::spawn(move || {
            let _guard = big.lock().unwrap();
            let _ = locked_tx.send(());
            let _ = release_rx.recv();
        });
        locked_rx.recv().unwrap();

        // Cancel + EOF must apply while the handler runs: both complete
        // promptly (a timed handoff proves the pre-fix code would block
        // behind the state lock) and the active unit token is
        // cancelled.
        let cancel = cancel_request("slow");
        let (done_tx, done_rx) = std::sync::mpsc::channel::<()>();
        std::thread::scope(|scope| {
            scope.spawn(|| {
                session.apply_cancel(&cancel);
                session.begin_shutdown();
                let _ = done_tx.send(());
            });
            match done_rx.recv_timeout(std::time::Duration::from_secs(5)) {
                Ok(()) => {}
                Err(_) => panic!(
                    "cancel/shutdown blocked behind the active handler's state lock"
                ),
            }
        });
        assert!(
            session.control.lock().unwrap().token.is_cancelled(),
            "cancel must reach the active unit token"
        );

        // Release the simulated handler.
        let _ = release_tx.send(());
        handler.join().unwrap();

        // Worker-side factual outcome: a unit that was admitted before
        // EOF executes under the already-cancelled token and reports
        // the factual cancellation instead of running uncancelled.
        let fixture = crate::rpc::handlers::reader::test_support::create_direct_v6(
            "active-cancel-eof",
        );
        let (work_tx, work_rx) = std::sync::mpsc::channel::<WorkUnit>();
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let writer = Arc::new(Mutex::new(FrameWriter::new(SharedVec(output.clone()))));
        let in_flight = Arc::new(AtomicUsize::new(1));
        let (events_tx, _events_rx) = std::sync::mpsc::channel::<SessionEvent>();
        let open = Request {
            id: Some(RequestId::String("queued".into())),
            method: "iprange.v1.reader.open".into(),
            params: crate::rpc::handlers::reader::test_support::live_source(&fixture.path),
            batch_index: None,
        };
        work_tx
            .send(unit(vec![WorkEntry::Execute(open)], false))
            .unwrap();
        drop(work_tx);

        worker_loop(
            session.state.clone(),
            session.control.clone(),
            writer,
            in_flight.clone(),
            work_rx,
            events_tx,
        );

        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        let payload: Value = serde_json::from_str(text.trim()).unwrap();
        assert_eq!(payload["id"], json!("queued"));
        assert_eq!(
            payload["error"]["data"]["code"],
            json!("cancelled"),
            "admitted SDK work must end in a factual outcome: {text}"
        );
        assert_eq!(in_flight.load(Ordering::Relaxed), 0);
        fixture.remove();
    }

    #[test]
    fn same_batch_cancel_marks_an_earlier_sibling_before_later_elements_scan() {
        // P2: a cancel element may target an ordinary element already
        // queued from the same batch (spec, the sole exception to
        // array-order execution). Admission marks each element pending
        // during the scan, so a later cancel sees the earlier sibling
        // and the worker omits its response.
        let mut session = Session::new();
        let batch = json!([
            {"jsonrpc": "2.0", "id": "a", "method": "iprange.v1.system.describe", "params": {}},
            {"jsonrpc": "2.0", "method": "iprange.v1.cancel", "params": {"request_id": "a"}},
            {"jsonrpc": "2.0", "id": "b", "method": "iprange.v1.system.describe", "params": {}},
        ]);
        let line = serde_json::to_vec(&batch).unwrap();
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let writer = Arc::new(Mutex::new(FrameWriter::new(SharedVec(output.clone()))));
        handle_frame(&mut session, line, &writer).unwrap();
        {
            let c = session.control.lock().unwrap();
            assert!(c.pending.contains("s:a"), "earlier sibling must be pending");
            assert!(c.cancelled.contains("s:a"), "sibling cancel must apply");
            assert!(c.pending.contains("s:b"), "later sibling must stay pending");
            assert!(!c.cancelled.contains("s:b"), "later sibling must not be cancelled");
        }
        let work = session.work_rx.take().unwrap().recv().unwrap();
        assert_eq!(session.in_flight.load(Ordering::Relaxed), 2, "both executes occupy queue capacity");
        let responses: Vec<Value> = work
            .entries
            .iter()
            .filter_map(|entry| {
                entry_response(&session.state, &session.control, entry)
            })
            .collect();
        assert_eq!(ids(&Value::Array(responses.clone())), ["b"]);
    }

    #[test]
    fn same_batch_cancel_before_an_element_is_not_its_target() {
        // A cancel can only target an element already admitted from
        // the same batch; an element scanned after the cancel is not
        // yet pending and must still execute.
        let mut session = Session::new();
        let batch = json!([
            {"jsonrpc": "2.0", "method": "iprange.v1.cancel", "params": {"request_id": "a"}},
            {"jsonrpc": "2.0", "id": "a", "method": "iprange.v1.system.describe", "params": {}},
        ]);
        let line = serde_json::to_vec(&batch).unwrap();
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let writer = Arc::new(Mutex::new(FrameWriter::new(SharedVec(output.clone()))));
        handle_frame(&mut session, line, &writer).unwrap();
        assert!(
            !session.control.lock().unwrap().cancelled.contains("s:a"),
            "a not-yet-admitted element must not be a cancellation target"
        );
        let work = session.work_rx.take().unwrap().recv().unwrap();
        assert_eq!(session.in_flight.load(Ordering::Relaxed), 1);
        let responses: Vec<Value> = work
            .entries
            .iter()
            .filter_map(|entry| {
                entry_response(&session.state, &session.control, entry)
            })
            .collect();
        assert_eq!(ids(&Value::Array(responses.clone())), ["a"]);
    }

    #[test]
    fn same_batch_cancel_omits_the_sibling_on_the_wire() {
        // Full-loop evidence for the same-batch cancel exception: one
        // batch frame then EOF; the cancelled sibling produces no
        // member in the response array and the later sibling answers.
        let batch = json!([
            {"jsonrpc": "2.0", "id": "a", "method": "iprange.v1.system.describe", "params": {}},
            {"jsonrpc": "2.0", "method": "iprange.v1.cancel", "params": {"request_id": "a"}},
            {"jsonrpc": "2.0", "id": "b", "method": "iprange.v1.system.describe", "params": {}},
        ]);
        let input = format!("{batch}\n").into_bytes();
        let output: Arc<Mutex<Vec<u8>>> = Arc::new(Mutex::new(Vec::new()));
        let reader = PlainEofReader {
            remaining: Box::leak(input.into_boxed_slice()),
        };
        let session = Session::new();
        session.run(reader, SharedVec(output.clone())).unwrap();
        let text = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        let mut lines = text.lines();
        let payload: Value = serde_json::from_str(lines.next().expect("batch response")).unwrap();
        let members = payload.as_array().expect("batch answers one array");
        assert_eq!(members.len(), 1, "cancelled sibling must be omitted: {text}");
        assert_eq!(members[0]["id"], json!("b"));
        assert!(members[0].get("result").is_some());
        assert!(lines.next().is_none(), "no extra output: {text}");
    }
}
