//! Process-level termination-signal contract (SOW-0028 wave-10 D1).
//!
//! The `--jsonrpc` service must never ignore SIGINT/SIGTERM: an idle
//! session exits non-zero through the graceful fatal path, and a
//! wedged session (stdout stalled, events channel full, shutdown
//! unreachable) is force-exited non-zero by the bounded watcher
//! retry.  Mirrors the Go helper-process tests in
//! `v4/go/internal/cli/rpc/session_test.go`.

#![cfg(unix)]

use std::io::Write;
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

fn spawn_product() -> Child {
    Command::new(env!("CARGO_BIN_EXE_iprange"))
        .arg("--jsonrpc")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn iprange --jsonrpc")
}

fn signal_and_wait(mut child: Child, sig: libc::c_int, timeout: Duration) -> i32 {
    // SAFETY: targeted kill of the child process we spawned.
    let rc = unsafe { libc::kill(child.id() as libc::pid_t, sig) };
    assert_eq!(rc, 0, "kill failed");
    wait_nonzero(&mut child, timeout)
}

fn wait_nonzero(child: &mut Child, timeout: Duration) -> i32 {
    let deadline = Instant::now() + timeout;
    loop {
        if let Some(status) = child.try_wait().expect("try_wait") {
            return status.code().unwrap_or(-1);
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            panic!("process did not terminate within {timeout:?}");
        }
        std::thread::sleep(Duration::from_millis(20));
    }
}

#[test]
fn idle_session_signal_exits_nonzero() {
    for sig in [libc::SIGINT, libc::SIGTERM] {
        for _ in 0..2 {
            let child = spawn_product();
            std::thread::sleep(Duration::from_millis(300));
            let code = signal_and_wait(child, sig, Duration::from_secs(3));
            assert_eq!(code, 1, "idle signal {sig}: exit code {code}, want 1");
        }
    }
}

#[test]
fn wedged_session_signal_forces_nonzero_exit() {
    for sig in [libc::SIGINT, libc::SIGTERM] {
        for _ in 0..2 {
            let mut child = spawn_product();
            // Pipeline more frames than the transport can hold and
            // never read stdout: the worker blocks on the full stdout
            // pipe, the main loop on the full work queue, the reader
            // on the full events channel (the wedge state).  Writing
            // in a thread because stdin itself stops being drained.
            let mut stdin = child.stdin.take().expect("stdin");
            let writer = std::thread::spawn(move || {
                for i in 0..2000 {
                    let frame = format!(
                        "{{\"jsonrpc\":\"2.0\",\"id\":{i},\"method\":\"iprange.v1.system.describe\",\"params\":{{}}}}\n"
                    );
                    if stdin.write_all(frame.as_bytes()).is_err() {
                        break;
                    }
                    let _ = stdin.flush();
                }
            });
            std::thread::sleep(Duration::from_secs(1));
            let code = signal_and_wait(child, sig, Duration::from_secs(4));
            assert_eq!(code, 1, "wedged signal {sig}: exit code {code}, want 1");
            drop(writer);
        }
    }
}
