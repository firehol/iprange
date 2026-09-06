//! Process-level termination-signal contract (SOW-0028 wave-10 D1).
//!
//! The `--jsonrpc` service must never ignore SIGINT/SIGTERM: an idle
//! session exits non-zero through the graceful fatal path; wedged
//! sessions (stalled stdout, full or partially-filled events
//! channel, shutdown drain) are force-exited non-zero by the
//! process-lifetime bound.  Mirrors the Go helper-process tests in
//! `v4/go/internal/cli/rpc/session_signal_unix_test.go`.

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

fn flood_frames(count: usize, _close: bool) -> Vec<String> {
    (0..count)
        .map(|i| {
            format!(
                "{{\"jsonrpc\":\"2.0\",\"id\":{i},\"method\":\"iprange.v1.system.describe\",\"params\":{{}}}}\n"
            )
        })
        .collect()
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

fn wedge_trial(sig: libc::c_int, frames: usize, close_stdin: bool) -> i32 {
    let mut child = spawn_product();
    let mut stdin = child.stdin.take().expect("stdin");
    let writer = std::thread::spawn(move || {
        for frame in flood_frames(frames, close_stdin) {
            if stdin.write_all(frame.as_bytes()).is_err() {
                break;
            }
            let _ = stdin.flush();
        }
        if close_stdin {
            let _ = stdin.flush();
        }
    });
    std::thread::sleep(Duration::from_secs(1));
    if close_stdin {
        let _ = writer.join();
        // drop(stdin) happens when the thread's closure ends; the
        // product sees EOF on its stdin pipe.
    }
    let code = signal_and_wait(child, sig, Duration::from_secs(4));
    assert_eq!(code, 1, "wedge frames={frames} close={close_stdin} signal {sig}: exit code {code}, want 1");
    code
}

#[test]
fn wedged_session_signal_forces_nonzero_exit() {
    // Full-flood wedge: far more frames than the transport can hold;
    // the events channel is full at signal time.
    for sig in [libc::SIGINT, libc::SIGTERM] {
        for _ in 0..2 {
            wedge_trial(sig, 2000, false);
        }
    }
}

#[test]
fn partial_wedge_signal_forces_nonzero_exit() {
    // Second role-round finding: a few dozen frames (below the
    // 64-slot events channel capacity) with stdin open and stdout
    // never read.  A delivered Fatal event would sit unprocessed;
    // only the process-lifetime bound can serve the signal.
    for sig in [libc::SIGINT, libc::SIGTERM] {
        for _ in 0..2 {
            wedge_trial(sig, 60, false);
        }
    }
}

#[test]
fn drain_wedge_signal_forces_nonzero_exit() {
    // Second role-round finding: the worker is blocked mid-write on a
    // full stdout pipe while the main loop joins it after EOF (the
    // shutdown drain).  The events channel is empty, so a delivered
    // Fatal event would sit unprocessed; only the process-lifetime
    // bound can serve the signal.
    for sig in [libc::SIGINT, libc::SIGTERM] {
        for _ in 0..2 {
            wedge_trial(sig, 60, true);
        }
    }
}
