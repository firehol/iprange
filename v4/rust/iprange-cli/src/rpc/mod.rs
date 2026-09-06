//! Bounded JSON-RPC 2.0 stdio service (iprange-jsonrpc-v1.md).
//!
//! Module boundaries (SOW-0028):
//! - `framing`: physical line transport, hard frame ceilings, CRLF/LF
//!   termination, batch bounds, queue bound;
//! - `schema`: strict envelope and response encoding, error payloads;
//! - `dispatch`: the fixed method registry and handler resolution;
//! - `session`: reader thread, main event loop, one active request
//!   plus bounded queue, cancellation, EOF/fatal shutdown;
//! - `handlers`: small method-family adapters over public
//!   `iprange-livedb` APIs.

pub mod dispatch;
pub mod framing;
pub mod handlers;
pub mod schema;
pub mod session;
pub mod state;

use std::io::{self, Write};

use self::dispatch::HandlerError;

/// Run the JSON-RPC service to EOF or fatal transport failure.
pub fn run() -> i32 {
    let stdin = io::stdin();
    let stdout = io::stdout();
    let session = session::Session::new();
    // Stdin is Read-only; BufReader supplies the BufRead transport
    // and is Send, so the session can move it into its reader thread.
    match session.run(io::BufReader::new(stdin), stdout) {
        Ok(()) => 0,
        Err(err) => {
            // Startup/framing failure and unrecoverable stdout failure
            // exit non-zero (documented transport behavior).  The
            // diagnostic write is best-effort (role-round finding): a
            // detached thread emits it and the main thread exits after
            // a bounded grace, so a full, undrained stderr pipe can
            // never block the process exit on the graceful fatal path
            // either (the same bound the forced signal exit uses).
            // The message may be cut off when stderr is writable but
            // slower, which is accepted.
            let message = format!("iprange: {err}");
            std::thread::spawn(move || {
                let _ = writeln!(io::stderr(), "{message}");
            });
            std::thread::sleep(std::time::Duration::from_millis(50));
            1
        }
    }
}

/// A connection-local opaque handle (32 lowercase hex characters).
///
/// Entropy failure is a server-side failure, not a silent zero handle:
/// temporary names must be unpredictable.
pub fn new_handle() -> Result<String, HandlerError> {
    let mut bytes = [0u8; 16];
    getrandom::fill(&mut bytes).map_err(|error| HandlerError {
        // Adapter product codes are a closed list (spec): `io` is the
        // documented adapter code for an OS-level resource failure.
        code: "io",
        outcome: "not_started",
        message: "secure handle generation failed".into(),
        details: Some(serde_json::json!({"cause": error.to_string()})),
    })?;
    Ok(bytes.iter().map(|b| format!("{b:02x}")).collect())
}
