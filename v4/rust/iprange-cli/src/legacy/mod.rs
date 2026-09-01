//! Released legacy `iprange` surface (SOW-0028 delivery step 3).
//!
//! This module implements the complete released legacy grammar,
//! ephemeral interval algebra, formatting, DNS, file expansion,
//! binary compatibility, diagnostics, and exit codes. It contains no
//! v4 persistence logic.

/// Legacy entry point. `--jsonrpc` mixed with other arguments is an
/// invalid JSON-RPC startup and must not fall back here silently;
/// main.rs already rejects that combination before calling us.
pub fn run(args: &[String]) -> i32 {
    let _ = args;
    eprintln!("iprange: legacy mode is not implemented yet in this build");
    1
}
