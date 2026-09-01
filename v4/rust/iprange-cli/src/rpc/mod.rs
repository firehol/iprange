//! Bounded JSON-RPC 2.0 stdio service (iprange-jsonrpc-v1.md).
//!
//! Module boundaries (SOW-0028):
//! - `framing`: physical line transport, hard frame ceilings, CRLF/LF
//!   termination, batch bounds, queue bound;
//! - `schema`: strict envelope and response encoding, error payloads;
//! - `dispatch`: the fixed method registry and handler resolution;
//! - `session`: read loop, one active request plus bounded queue,
//!   cancellation, connection-owned readers/cursors, shutdown;
//! - `handlers`: small method-family adapters over public
//!   `iprange-livedb` APIs.

pub mod dispatch;
pub mod framing;
pub mod handlers;
pub mod schema;
pub mod session;

use std::io::{self, Write};

/// Run the JSON-RPC service to EOF or fatal transport failure.
pub fn run() -> i32 {
    let stdin = io::stdin();
    let stdout = io::stdout();
    let mut reader = framing::LineReader::new(stdin.lock());
    let session = session::Session::new();
    match session.run(&mut reader, stdout) {
        Ok(()) => 0,
        Err(err) => {
            // Startup/framing failure and unrecoverable stdout failure
            // exit non-zero (documented transport behavior).
            let _ = writeln!(io::stderr(), "iprange: {err}");
            1
        }
    }
}

/// A connection-local opaque handle (32 lowercase hex characters).
/// Used by the reader/cursor handler increment.
#[allow(dead_code)]
pub fn new_handle() -> String {
    let mut bytes = [0u8; 16];
    let _ = getrandom::fill(&mut bytes);
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}
