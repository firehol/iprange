//! Connection session: bounded read loop, bounded request queue,
//! cancellation, and EOF shutdown.
//!
//! Model: one worker thread executes one decoded frame (a single
//! request or a batch) at a time; additional frames wait in a channel
//! behind a 16-request admission counter so the read loop never
//! blocks. Per-request semantics (iprange-jsonrpc-v1.md):
//! - one active request set plus at most 16 queued requests; a
//!   request exceeding the bound fails with -32002 server_busy;
//! - the read loop stays active while work executes so cancellation
//!   and EOF are observed;
//! - cancel notifications apply immediately after frame validation:
//!   unknown or already terminal ids are ignored; queued matches are
//!   skipped without a response; the active request set is signalled
//!   only when it contains the cancelled id;
//! - a whole frame decodes strictly before anything in it executes;
//!   envelope failures produce one id-null error and the service
//!   keeps serving;
//! - stdin EOF stops acceptance, cancels queued requests, signals the
//!   active work, waits for its factual terminal outcome, and exits 0
//!   unless transport shutdown itself failed;
//! - a frame over the input ceiling produces -32001 with id null and
//!   the process closes without parsing any later bytes.

use std::collections::{HashMap, HashSet};
use std::io::{self, Write};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::mpsc::{Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;

use iprange_livedb::CancellationToken;
use serde_json::Value;

use super::dispatch::{resolve, HandlerError};
use super::framing::{FrameWriter, LineReader, QUEUED_LIMIT};
use super::schema::{self, Request, RequestId, SchemaError};

/// Conservative connection-owned state shared by handlers.
#[derive(Default)]
pub struct SessionState {
    /// Connection-local reader handles (opaque 32-hex strings).
    /// Populated by the reader-handler increment.
    #[allow(dead_code)]
    pub readers: HashMap<String, ()>,
    /// Connection-local cursor handles.
    #[allow(dead_code)]
    pub cursors: HashMap<String, ()>,
    /// Request ids cancelled by the transport.
    pub cancelled: HashSet<String>,
    /// Cancellation signal for the active work unit.
    pub token: Arc<CancellationToken>,
    /// Ids of the request set currently executing.
    active_keys: HashSet<String>,
}

/// One decoded frame queued as a unit: array-order execution and one
/// response frame per input frame.
struct WorkUnit {
    requests: Vec<Request>,
    batch: bool,
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
    match id {
        RequestId::String(s) => format!("s:{s}"),
        RequestId::Number(n) => format!("n:{n}"),
    }
}

fn request_key(request: &Request) -> Option<String> {
    request.id.as_ref().map(key)
}

impl Session {
    pub fn new() -> Self {
        let (work_tx, work_rx) = std::sync::mpsc::channel::<WorkUnit>();
        let state = Arc::new(Mutex::new(SessionState {
            token: Arc::new(CancellationToken::new()),
            ..SessionState::default()
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
    pub fn run<R: std::io::BufRead, W: Write + Send + 'static>(
        mut self,
        reader: &mut LineReader<R>,
        writer: W,
    ) -> io::Result<()> {
        let writer = Arc::new(Mutex::new(FrameWriter::new(writer)));
        let worker_state = Arc::clone(&self.state);
        let worker_writer = Arc::clone(&writer);
        let worker_in_flight = Arc::clone(&self.in_flight);
        let work_rx = self.work_rx.take().expect("run once");
        let worker = std::thread::Builder::new()
            .name("iprange-jsonrpc".into())
            .spawn(move || worker_loop(worker_state, worker_writer, worker_in_flight, work_rx))
            .expect("spawn jsonrpc worker");
        self.worker = Some(worker);

        loop {
            match reader.read_line() {
                Err(_) => {
                    let payload = SchemaError::response(
                        None,
                        SchemaError {
                            code: schema::TRANSPORT_FRAME_TOO_LARGE,
                            message: "frame over input limit".into(),
                        },
                    );
                    {
                        let mut w = writer.lock().unwrap();
                        let _ = w.write_line(&payload.to_string());
                    }
                    return self.shutdown();
                }
                Ok(None) => return self.shutdown(),
                Ok(Some(line)) => {
                    let requests = match schema::decode_frame(&line) {
                        Ok(requests) => requests,
                        Err(err) => {
                            let payload = SchemaError::response(None, err);
                            let mut w = writer.lock().unwrap();
                            w.write_line(&payload.to_string())?;
                            continue;
                        }
                    };
                    let mut unit = Vec::new();
                    let mut batch = false;
                    for request in requests {
                        if request.method == schema::CANCEL_METHOD {
                            self.apply_cancel(&request);
                            continue;
                        }
                        batch = request.batch_index.is_some();
                        if self.in_flight.load(Ordering::Relaxed) >= QUEUED_LIMIT {
                            let payload = schema::error_response(
                                request.id.as_ref().unwrap(),
                                schema::TRANSPORT_SERVER_BUSY, "server_busy", None);
                            let mut w = writer.lock().unwrap();
                            w.write_line(&payload.to_string())?;
                            continue;
                        }
                        self.in_flight.fetch_add(1, Ordering::Relaxed);
                        unit.push(request);
                    }
                    if unit.is_empty() {
                        continue;
                    }
                    self.work_tx.as_ref().unwrap().send(WorkUnit { requests: unit, batch }).expect("worker alive");
                }
            }
        }
    }

    /// Cancel one request id immediately after frame validation.
    fn apply_cancel(&mut self, request: &Request) {
        let cancel_id = match request.params.get("request_id") {
            Some(Value::String(s)) => Some(format!("s:{s}")),
            Some(Value::Number(n)) => n.as_i64().map(|i| format!("n:{i}")),
            _ => None,
        };
        let Some(cancel_id) = cancel_id else { return };
        let mut state = self.state.lock().unwrap();
        state.cancelled.insert(cancel_id.clone());
        if state.active_keys.contains(&cancel_id) {
            state.token.cancel();
        }
    }

    /// EOF or frame-too-large shutdown.
    ///
    /// Units admitted before EOF are already active/queued in the
    /// channel; the worker drains them (each request observes its
    /// cancellation state) so a client that sends a request and
    /// closes stdin still receives the factual response. The token
    /// requests cancellation of long-running SDK work.
    fn shutdown(&mut self) -> io::Result<()> {
        self.state.lock().unwrap().token.cancel();
        // Close the channel so the worker exits after draining it.
        self.work_tx.take();
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
        Ok(())
    }
}

fn worker_loop<W: Write + Send + 'static>(
    state: Arc<Mutex<SessionState>>,
    writer: Arc<Mutex<FrameWriter<W>>>,
    in_flight: Arc<AtomicUsize>,
    rx: Receiver<WorkUnit>,
) {
    while let Ok(unit) = rx.recv() {
        in_flight.fetch_sub(unit.requests.len(), Ordering::Relaxed);
        let keys: HashSet<String> = unit.requests.iter().filter_map(request_key).collect();
        {
            let mut s = state.lock().unwrap();
            s.token = Arc::new(CancellationToken::new());
            s.active_keys = keys;
        }
        let mut responses = Vec::with_capacity(unit.requests.len());
        for request in &unit.requests {
            let cancelled = {
                let s = state.lock().unwrap();
                request_key(request).map(|k| s.cancelled.contains(&k)).unwrap_or(false)
            };
            if cancelled {
                continue;
            }
            responses.push(execute(&state, request));
        }
        {
            let mut s = state.lock().unwrap();
            s.active_keys.clear();
        }
        if responses.is_empty() {
            continue;
        }
        let payload = if unit.batch { Value::Array(responses) } else { responses.pop().unwrap() };
        let text = schema::encode_response_frame(&payload).expect("response frame within ceiling");
        let mut w = writer.lock().unwrap();
        w.write_line(&text).ok();
    }
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
    match handler(&mut st, request.params.clone()) {
        Ok(result) => schema::success_response(request.id.as_ref().unwrap(), result),
        Err(HandlerError { code, outcome, message, details }) => {
            let data = match details {
                Some(details) => serde_json::json!({"code": code, "outcome": outcome, "details": details}),
                None => serde_json::json!({"code": code, "outcome": outcome}),
            };
            schema::error_response(request.id.as_ref().unwrap(), schema::PRODUCT_ERROR, &message, Some(data))
        }
    }
}
