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

/// Spawn the product with a stderr file descriptor that is a full,
/// never-drained pipe, so the forced-exit diagnostic can never be
/// written (external review finding).  Returns the child and the read
/// end the caller must keep alive for the child's lifetime.
fn spawn_product_full_stderr() -> (Child, std::os::fd::OwnedFd) {
    use std::os::fd::{FromRawFd, OwnedFd};
    let mut fds = [0 as libc::c_int; 2];
    let rc = unsafe { libc::pipe(fds.as_mut_ptr()) };
    assert_eq!(rc, 0, "pipe");
    let read_fd = fds[0];
    let write_fd = fds[1];
    unsafe {
        let flags = libc::fcntl(write_fd, libc::F_GETFL, 0);
        libc::fcntl(write_fd, libc::F_SETFL, flags | libc::O_NONBLOCK);
        loop {
            let n = libc::write(
                write_fd,
                b" ".as_ptr() as *const libc::c_void,
                4096,
            );
            if n < 0 {
                break;
            }
        }
        libc::fcntl(write_fd, libc::F_SETFL, flags);
    }
    let write_owned = unsafe { OwnedFd::from_raw_fd(write_fd) };
    let child = Command::new(env!("CARGO_BIN_EXE_iprange"))
        .arg("--jsonrpc")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::from(write_owned))
        .spawn()
        .expect("spawn iprange --jsonrpc with full stderr");
    let read_owned = unsafe { OwnedFd::from_raw_fd(read_fd) };
    (child, read_owned)
}

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
fn full_stderr_signal_forces_nonzero_exit() {
    // External review finding: the forced exit must never depend on a
    // blocking diagnostic write.  With stderr a full, undrained pipe,
    // the product must still exit non-zero within the bounded
    // watchdog window.
    for sig in [libc::SIGINT, libc::SIGTERM] {
        let (child, _read_end) = spawn_product_full_stderr();
        std::thread::sleep(Duration::from_millis(300));
        let code = signal_and_wait(child, sig, Duration::from_secs(4));
        assert_eq!(code, 1, "full-stderr signal {sig}: exit code {code}, want 1");
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

// eof_first_signal_wins_over_exit_zero: the supervisor stop shape
// (close stdin + signal back-to-back) must never observe a clean
// exit.  The Rust EOF tail polls the watcher's recorded flag for the
// same 25 ms grace window Go uses, so a signal delivered after the
// shutdown drain but before the process exits still wins (third
// role-round parity fix); without the grace the shape exited 0 in
// ~3/4 of trials and the test flaked under load (1/3 full-suite
// runs), which is why an earlier committed version was removed
// before the grace existed.

#[test]
fn eof_first_signal_wins_over_exit_zero() {
    use std::io::{BufRead, BufReader};
    for sig in [libc::SIGINT, libc::SIGTERM] {
        for _ in 0..2 {
            let mut child = spawn_product();
            {
                let stdin = child.stdin.as_mut().expect("stdin");
                let _ = stdin.write_all(
                    b"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n");
                let _ = stdin.flush();
            }
            drop(child.stdin.take()); // EOF right after the request
            let stdout = child.stdout.as_mut().expect("stdout");
            let mut reader = BufReader::new(stdout);
            let mut line = String::new();
            let _ = reader.read_line(&mut line);
            assert!(line.contains("\"id\":1"), "describe response: {line}");
            let code = signal_and_wait(child, sig, Duration::from_secs(3));
            assert_eq!(code, 1, "eof-first signal {sig}: exit code {code}, want 1");
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
