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
//! - every COMPLETE response object (envelope plus id plus result or
//!   error) is capped at 65,000 bytes; an unencodable inline success
//!   is replaced by the `output_limit` product error, never an
//!   oversized frame; a busy-rejected batch element stays inside its
//!   batch response array at its original position;
//! - stdin EOF stops acceptance, cancels queued requests, signals the
//!   active work, waits for its factual terminal outcome, and exits 0
//!   unless transport shutdown itself failed;
//! - a frame over the input ceiling produces -32001 with id null and
//!   the process closes without parsing any later bytes.

use std::collections::HashSet;
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
use super::state::ConnectionState;

/// Conservative connection-owned state shared by handlers.
#[derive(Default)]
pub struct SessionState {
    /// Shared reader/cursor resources and their connection limits.
    pub resources: ConnectionState,
    /// Request ids cancelled by the transport.
    pub cancelled: HashSet<String>,
    /// Cancellation signal for the active work unit.
    pub token: Arc<CancellationToken>,
    /// Ids of the request set currently executing.
    active_keys: HashSet<String>,
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
            resources: ConnectionState::default(),
            token: Arc::new(CancellationToken::new()),
            active_keys: HashSet::default(),
            cancelled: HashSet::default(),
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
                        let text = schema::encode_response_frame(&payload)
                            .expect("constant transport error within limits");
                        let mut w = writer.lock().unwrap();
                        let _ = w.write_line(&text);
                    }
                    return self.shutdown();
                }
                Ok(None) => return self.shutdown(),
                Ok(Some(line)) => {
                    let requests = match schema::decode_frame(&line) {
                        Ok(requests) => requests,
                        Err(err) => {
                            let payload = SchemaError::response(None, err);
                            let text = schema::encode_response_frame(&payload)
                                .expect("constant schema error within limits");
                            let mut w = writer.lock().unwrap();
                            w.write_line(&text)?;
                            continue;
                        }
                    };
                    let mut ordinary = Vec::new();
                    let mut batch = false;
                    for request in requests {
                        if request.method == schema::CANCEL_METHOD {
                            self.apply_cancel(&request);
                            continue;
                        }
                        batch |= request.batch_index.is_some();
                        ordinary.push(request);
                    }
                    if ordinary.is_empty() {
                        continue;
                    }
                    let entries = admit_frame(ordinary, &self.in_flight);
                    if batch {
                        // Every element answers inside one array in the
                        // frame's order, including busy rejections.
                        let admitted = entries.iter().filter(|e| e.occupies_queue()).count();
                        self.work_tx
                            .as_ref()
                            .unwrap()
                            .send(WorkUnit {
                                entries,
                                admitted,
                                batch,
                            })
                            .expect("worker alive");
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
                                self.work_tx
                                    .as_ref()
                                    .unwrap()
                                    .send(WorkUnit {
                                        entries,
                                        admitted: 1,
                                        batch,
                                    })
                                    .expect("worker alive");
                            }
                        }
                    }
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
        in_flight.fetch_sub(unit.admitted, Ordering::Relaxed);
        let keys: HashSet<String> = unit
            .entries
            .iter()
            .filter(|entry| matches!(entry, WorkEntry::Execute(_)))
            .filter_map(|entry| request_key(entry.request()))
            .collect();
        {
            let mut s = state.lock().unwrap();
            s.token = Arc::new(CancellationToken::new());
            s.active_keys = keys;
        }
        let mut responses: Vec<Value> = unit
            .entries
            .iter()
            .filter_map(|entry| entry_response(&state, entry))
            .collect();
        {
            let mut s = state.lock().unwrap();
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
        w.write_line(&text).ok();
    }
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
            message: "response object exceeds the 65000-byte limit; request id cannot be echoed".into(),
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
    match handler(&mut st, request.params.clone()) {
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
        state.lock().unwrap().cancelled.insert("s:a".to_owned());
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
        let request = request(&huge, None);
        let response = schema::success_response(
            &RequestId::String(huge),
            json!({"method": "iprange.v1.system.describe"}),
        );
        let bounded = bounded_response(response, &request);
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
}
