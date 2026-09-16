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

use std::ffi::{OsStr, OsString};

fn main() {
    // Command-line arguments are byte strings: a POSIX file name may
    // hold bytes that are not valid UTF-8. `std::env::args()` panics
    // on such an argument, which would abort before mode selection and
    // so break both surfaces, while the released C tool and the Go port
    // classify the same name as an ordinary input. `args_os` keeps the
    // exact bytes for the whole run; the SDK worker reads its argv
    // with `args_os` for the same reason.
    let mut argv = std::env::args_os();
    let prog = argv.next().unwrap_or_else(|| OsString::from("iprange"));
    let args: Vec<OsString> = argv.collect();
    if args.first().map(OsString::as_os_str) == Some(OsStr::new("--jsonrpc")) {
        if args.len() != 1 {
            // `--jsonrpc` is exclusive: mixing it with legacy options
            // or inputs is invalid JSON-RPC startup (spec, Legacy
            // coexistence) and must never fall back to legacy parsing.
            eprintln!("iprange: --jsonrpc cannot be combined with other arguments");
            std::process::exit(1);
        }
        std::process::exit(rpc::run());
    }
    std::process::exit(legacy::run(&prog, &args));
}
