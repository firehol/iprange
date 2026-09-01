//! Process startup and exact legacy/JSON-RPC mode selection only.
//!
//! - `iprange --jsonrpc` (exactly one argument) runs the v1 JSON-RPC
//!   stdio service (`rpc::run`).
//! - Any other command line, including `--jsonrpc` mixed with other
//!   arguments, runs the released legacy grammar (`legacy::run`).
//! - `--jsonrpc` combined with any other argument is an invalid
//!   JSON-RPC startup; per the protocol contract it must NOT fall
//!   back to legacy parsing.

mod io;
mod legacy;
mod rpc;

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args == vec!["--jsonrpc".to_string()] {
        std::process::exit(rpc::run());
    }
    std::process::exit(legacy::run(&args));
}
