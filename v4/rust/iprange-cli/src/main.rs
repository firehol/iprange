//! Process startup and exact legacy/JSON-RPC mode selection only.
//!
//! - `iprange --jsonrpc` (exactly one argument) runs the v1 JSON-RPC
//!   stdio service (`rpc::run`).
//! - `--jsonrpc` combined with any other argument is an invalid
//!   JSON-RPC startup: it prints a diagnostic to stderr and exits 1
//!   without falling back to legacy parsing.
//! - Any other command line runs the released legacy grammar
//!   (`legacy::run`).

mod io;
mod legacy;
mod rpc;

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.first().map(String::as_str) == Some("--jsonrpc") {
        if args.len() != 1 {
            // `--jsonrpc` is exclusive: mixing it with legacy options
            // or inputs is invalid JSON-RPC startup (spec, Legacy
            // coexistence) and must never fall back to legacy parsing.
            eprintln!("iprange: --jsonrpc cannot be combined with other arguments");
            std::process::exit(1);
        }
        std::process::exit(rpc::run());
    }
    std::process::exit(legacy::run(&args));
}
