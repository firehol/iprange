//! Strict v1 envelope decoding, response encoding, and error payloads.
//!
//! Everything here is transport contract (iprange-jsonrpc-v1.md):
//! - requests: `jsonrpc:"2.0"`, string or integral id, method prefix
//!   `iprange.v1.`, object params, unknown members rejected;
//! - notifications: only `iprange.v1.cancel` may omit the id;
//! - batches: 1..=16 objects, executed in array order, valid
//!   notifications excluded from the response array;
//! - responses: id required, exactly one of result/error, error
//!   objects carry integer code + string message + optional data;
//! - ceilings: response object 65,000 bytes; response frame
//!   1,048,576 bytes.

use serde_json::{json, Value};

use super::framing::{OUTPUT_FRAME_LIMIT, RESPONSE_OBJECT_LIMIT};

pub const STD_PARSE_ERROR: i64 = -32700;
pub const STD_INVALID_REQUEST: i64 = -32600;
pub const STD_METHOD_NOT_FOUND: i64 = -32601;
pub const STD_INVALID_PARAMS: i64 = -32602;
pub const TRANSPORT_FRAME_TOO_LARGE: i64 = -32001;
pub const TRANSPORT_SERVER_BUSY: i64 = -32002;
pub const PRODUCT_ERROR: i64 = -32010;

pub const METHOD_PREFIX: &str = "iprange.v1.";
pub const CANCEL_METHOD: &str = "iprange.v1.cancel";

/// A request id: an exact JSON string or an integral JSON number.
///
/// serde_json preserves i64 and u64 exactly, which covers every integral
/// JSON number the v1 contract accepts (the Python authority parses any
/// Python int; JSON text cannot express values beyond u64 with exact
/// integral semantics here).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RequestId {
    String(String),
    Number(serde_json::Number),
}

impl Default for RequestId {
    /// Only used to satisfy derive(Default) on connection state; the
    /// active id is always overwritten before a handler runs.
    fn default() -> Self {
        RequestId::String(String::new())
    }
}

impl RequestId {
    pub fn as_json(&self) -> Value {
        match self {
            RequestId::String(s) => Value::String(s.clone()),
            RequestId::Number(n) => Value::Number(n.clone()),
        }
    }
    /// Canonical correlation key used for cancellation and busy tracking.
    pub fn key(&self) -> String {
        match self {
            RequestId::String(s) => format!("s:{s}"),
            RequestId::Number(n) => format!("n:{n}"),
        }
    }
}

/// A decoded and envelope-valid request or notification.
#[derive(Debug, Clone)]
pub struct Request {
    pub id: Option<RequestId>,
    pub method: String,
    pub params: Value,
    pub batch_index: Option<usize>,
}

#[derive(Debug)]
pub struct SchemaError {
    pub code: i64,
    pub message: String,
}

impl SchemaError {
    pub fn parse(message: impl Into<String>) -> Self {
        Self {
            code: STD_PARSE_ERROR,
            message: message.into(),
        }
    }
    pub fn invalid(message: impl Into<String>) -> Self {
        Self {
            code: STD_INVALID_REQUEST,
            message: message.into(),
        }
    }
    pub fn response(id: Option<RequestId>, err: SchemaError) -> Value {
        let mut payload =
            json!({"jsonrpc": "2.0", "error": {"code": err.code, "message": err.message}});
        match &id {
            Some(id) => payload["id"] = id.as_json(),
            None => payload["id"] = Value::Null,
        }
        payload
    }
}

/// True when the preserved number text is an integral literal
/// (arbitrary_precision stores the exact lexical token).
pub(crate) fn integral_text(n: &serde_json::Number) -> bool {
    let text = n.to_string();
    !text.contains('.') && !text.contains('e') && !text.contains('E')
}

/// The cancellation/queue key of a `cancel.request_id` number: the same
/// canonical correlation key a request id of the same numeric text would
/// produce, so arbitrary-precision ids cancel correctly.
pub(crate) fn number_cancel_key(n: &Value) -> Option<String> {
    let number = n.as_number()?;
    if number.is_i64() || number.is_u64() || integral_text(number) {
        Some(format!("n:{number}"))
    } else {
        None
    }
}

fn valid_id(v: &Value) -> Option<RequestId> {
    match v {
        Value::String(s) => Some(RequestId::String(s.clone())),
        // Accept any integral JSON number. serde_json runs with
        // arbitrary_precision in this crate, so Number preserves the
        // exact decimal text of out-of-range integers; only true
        // floats (containing '.', 'e', or 'E') are rejected.
        Value::Number(n) if n.is_i64() || n.is_u64() || integral_text(n) => {
            Some(RequestId::Number(n.clone()))
        }
        _ => None,
    }
}

fn decode_envelope(item: &Value, batch_index: Option<usize>) -> Result<Request, SchemaError> {
    let obj = item
        .as_object()
        .ok_or_else(|| SchemaError::invalid("request must be an object"))?;
    for key in obj.keys() {
        if key != "jsonrpc" && key != "id" && key != "method" && key != "params" {
            return Err(SchemaError::invalid(format!(
                "unknown request member {key:?}"
            )));
        }
    }
    if obj.get("jsonrpc").and_then(|v| v.as_str()) != Some("2.0") {
        return Err(SchemaError::invalid("jsonrpc must be \"2.0\""));
    }
    let method = match obj.get("method") {
        Some(Value::String(m)) if m.starts_with(METHOD_PREFIX) => m.clone(),
        Some(Value::String(_)) => {
            return Err(SchemaError {
                code: STD_METHOD_NOT_FOUND,
                message: "method must start with iprange.v1.".into(),
            });
        }
        _ => {
            return Err(SchemaError::invalid(
                "method must be a string starting with iprange.v1.",
            ))
        }
    };
    let id = match obj.get("id") {
        None => None,
        Some(v) => Some(
            valid_id(v)
                .ok_or_else(|| SchemaError::invalid("id must be a string or integral number"))?,
        ),
    };
    let params = match obj.get("params") {
        Some(Value::Object(_)) => obj.get("params").unwrap().clone(),
        _ => {
            return Err(SchemaError {
                code: STD_INVALID_PARAMS,
                message: "params must be an object".into(),
            })
        }
    };
    if id.is_none() && method != CANCEL_METHOD {
        return Err(SchemaError::invalid(
            "notifications are not accepted except iprange.v1.cancel",
        ));
    }
    Ok(Request {
        id,
        method,
        params,
        batch_index,
    })
}

/// Decode one physical line into a single request or an ordered batch.
pub fn decode_frame(line: &[u8]) -> Result<Vec<Request>, SchemaError> {
    if line.len() > super::framing::INPUT_FRAME_LIMIT {
        return Err(SchemaError {
            code: TRANSPORT_FRAME_TOO_LARGE,
            message: "frame over input limit".into(),
        });
    }
    // Only LF and CRLF terminate a frame; a bare trailing CR is part
    // of the payload and is rejected below as an unescaped line break.
    let line = match line.strip_suffix(b"\n") {
        Some(rest) => match rest.strip_suffix(b"\r") {
            Some(crlf) => crlf,
            None => rest,
        },
        None => line,
    };
    if line.contains(&b'\n') || line.contains(&b'\r') {
        return Err(SchemaError::parse("unescaped line break inside frame"));
    }
    // The transport is a UTF-8 byte stream; reject invalid UTF-8 with
    // a deterministic parse error instead of letting the JSON decoder
    // apply a lossy replacement.
    std::str::from_utf8(line).map_err(|_| SchemaError::parse("frame is not valid UTF-8"))?;
    let value: Value = serde_json::from_slice(line)
        .map_err(|e| SchemaError::parse(format!("parse error: {e}")))?;
    if let Some(arr) = value.as_array() {
        if arr.is_empty() || arr.len() > super::framing::BATCH_LIMIT {
            return Err(SchemaError::invalid(format!(
                "batch length {} outside 1..{}",
                arr.len(),
                super::framing::BATCH_LIMIT
            )));
        }
        return arr
            .iter()
            .enumerate()
            .map(|(i, item)| decode_envelope(item, Some(i)))
            .collect();
    }
    if !value.is_object() {
        return Err(SchemaError::invalid("request must be an object"));
    }
    Ok(vec![decode_envelope(&value, None)?])
}

/// Encode one response object with the 65,000-byte ceiling.
pub fn encode_response_object(payload: &Value) -> Result<String, SchemaError> {
    let text = payload.to_string();
    if text.len() > RESPONSE_OBJECT_LIMIT {
        return Err(SchemaError {
            code: TRANSPORT_FRAME_TOO_LARGE,
            message: "response object over 65,000-byte limit".into(),
        });
    }
    Ok(text)
}

/// Encode one response frame with the 1,048,576-byte ceiling.
pub fn encode_response_frame(payload: &Value) -> Result<String, SchemaError> {
    let text = payload.to_string();
    if text.len() > OUTPUT_FRAME_LIMIT {
        return Err(SchemaError {
            code: TRANSPORT_FRAME_TOO_LARGE,
            message: "response frame over 1,048,576-byte limit".into(),
        });
    }
    Ok(text)
}

pub fn success_response(id: &RequestId, result: Value) -> Value {
    json!({"jsonrpc": "2.0", "id": id.as_json(), "result": result})
}

pub fn error_response(id: &RequestId, code: i64, message: &str, data: Option<Value>) -> Value {
    let mut payload = json!({"jsonrpc": "2.0", "id": id.as_json(),
                             "error": {"code": code, "message": message}});
    if let Some(data) = data {
        payload["error"]["data"] = data;
    }
    payload
}

#[cfg(test)]
mod tests {
    use super::*;

    fn decode(s: &str) -> Result<Vec<Request>, SchemaError> {
        decode_frame(s.as_bytes())
    }

    #[test]
    fn accepts_minimal_requests() {
        let reqs = decode(
            r#"{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}"#,
        )
        .unwrap();
        assert_eq!(reqs.len(), 1);
        assert_eq!(reqs[0].method, "iprange.v1.system.describe");
    }

    #[test]
    fn rejects_envelope_violations() {
        for bad in [
            r#"{"id":"1","method":"iprange.v1.system.describe","params":{}}"#,
            r#"{"jsonrpc":"1.0","id":"1","method":"iprange.v1.system.describe","params":{}}"#,
            r#"{"jsonrpc":"2.0","id":1.5,"method":"iprange.v1.system.describe","params":{}}"#,
            r#"{"jsonrpc":"2.0","id":true,"method":"iprange.v1.system.describe","params":{}}"#,
            r#"{"jsonrpc":"2.0","id":"1","method":"other.x","params":{}}"#,
            r#"{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":[],"extra":1}"#,
            r#"{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":null}"#,
            r#"{"jsonrpc":"2.0","id":null,"method":"iprange.v1.system.describe","params":{}}"#,
        ] {
            assert!(decode(bad).is_err(), "accepted {bad}");
        }
    }

    #[test]
    fn params_are_required() {
        // The v1 contract says every request has object-valued params.
        assert!(
            decode(r#"{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe"}"#).is_err()
        );
        assert!(decode(r#"{"jsonrpc":"2.0","method":"iprange.v1.cancel"}"#).is_err());
    }

    #[test]
    fn notification_only_cancel() {
        assert!(
            decode(r#"{"jsonrpc":"2.0","method":"iprange.v1.system.describe","params":{}}"#)
                .is_err()
        );
        let reqs =
            decode(r#"{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"1"}}"#)
                .unwrap();
        assert!(reqs[0].id.is_none());
        assert_eq!(reqs[0].method, CANCEL_METHOD);
    }

    #[test]
    fn invalid_utf8_is_a_deterministic_parse_error() {
        let err = decode_frame(b"{\"id\":\"\xff\xfe\"}").unwrap_err();
        assert_eq!(err.code, STD_PARSE_ERROR);
        assert_eq!(err.message, "frame is not valid UTF-8");
        let err = decode_frame(b"\xff").unwrap_err();
        assert_eq!(err.code, STD_PARSE_ERROR);
        assert_eq!(err.message, "frame is not valid UTF-8");
    }

    #[test]
    fn crlf_terminated_frames_decode() {
        let inner =
            r#"{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}"#;
        let reqs = decode_frame(format!("{inner}\r\n").as_bytes()).unwrap();
        assert_eq!(reqs.len(), 1);
        assert_eq!(reqs[0].method, "iprange.v1.system.describe");
    }

    #[test]
    fn batch_bounds() {
        let inner =
            r#"{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}"#;
        let reqs = decode(&format!("[{inner},{inner}]")).unwrap();
        assert_eq!(reqs.len(), 2);
        // A single-element batch is valid (1..=16).
        assert_eq!(decode(&format!("[{inner}]")).unwrap().len(), 1);
        assert!(decode("[]").is_err());
    }
}
