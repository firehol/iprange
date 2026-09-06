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

/// A stderr file descriptor that is a full, never-drained pipe, so a
/// child diagnostic write can never complete (external review
/// finding).  Returns the write end for the child's fd 2 and the read
/// end the caller must keep alive for the child's lifetime.
fn full_stderr_pipe() -> (std::os::fd::OwnedFd, std::os::fd::OwnedFd) {
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
    let read_owned = unsafe { OwnedFd::from_raw_fd(read_fd) };
    (write_owned, read_owned)
}

/// A stdout file descriptor that fails every write: the write end of
/// a pipe whose read end is already closed.  The child's write fails
/// with EPIPE (Rust ignores SIGPIPE), so the session error reaches
/// the graceful fatal path (role-round finding).
fn broken_stdout() -> std::os::fd::OwnedFd {
    use std::os::fd::{FromRawFd, OwnedFd};
    let mut fds = [0 as libc::c_int; 2];
    let rc = unsafe { libc::pipe(fds.as_mut_ptr()) };
    assert_eq!(rc, 0, "pipe stdout");
    unsafe {
        libc::close(fds[0]);
    }
    unsafe { OwnedFd::from_raw_fd(fds[1]) }
}

/// Spawn the product with a stderr file descriptor that is a full,
/// never-drained pipe, so the forced-exit diagnostic can never be
/// written (external review finding).  Returns the child and the read
/// end the caller must keep alive for the child's lifetime.
fn spawn_product_full_stderr() -> (Child, std::os::fd::OwnedFd) {
    let (write_owned, read_owned) = full_stderr_pipe();
    let child = Command::new(env!("CARGO_BIN_EXE_iprange"))
        .arg("--jsonrpc")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::from(write_owned))
        .spawn()
        .expect("spawn iprange --jsonrpc with full stderr");
    (child, read_owned)
}

/// Spawn the product with a stdout that fails every write and a
/// stderr that is a full, never-drained pipe, so the graceful-fatal
/// diagnostic can never be written either (role-round finding).
/// Returns the child and the stderr read end to keep alive.
fn spawn_product_graceful_fatal_full_stderr() -> (Child, std::os::fd::OwnedFd) {
    let (stderr_write, stderr_read) = full_stderr_pipe();
    let stdout_write = broken_stdout();
    let child = Command::new(env!("CARGO_BIN_EXE_iprange"))
        .arg("--jsonrpc")
        .stdin(Stdio::piped())
        .stdout(Stdio::from(stdout_write))
        .stderr(Stdio::from(stderr_write))
        .spawn()
        .expect("spawn iprange --jsonrpc with broken stdout and full stderr");
    (child, stderr_read)
}

/// Spawn the product with a stdout that fails every write and a
/// normal piped stderr (control for the graceful-fatal diagnostic).
fn spawn_product_broken_stdout() -> Child {
    let stdout_write = broken_stdout();
    Command::new(env!("CARGO_BIN_EXE_iprange"))
        .arg("--jsonrpc")
        .stdin(Stdio::piped())
        .stdout(Stdio::from(stdout_write))
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn iprange --jsonrpc with broken stdout")
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
fn graceful_fatal_full_stderr_exits_nonzero() {
    // Role-round finding: the graceful fatal path (rpc::run's
    // diagnostic on a session error) must be as independent of a
    // blocked stderr write as the forced signal exit.  A response
    // write that fails (stdout closed) fails the session; with stderr
    // a full, undrained pipe the process must still exit 1 within the
    // bounded grace window, with no signal at all.
    for _ in 0..2 {
        let (mut child, _read_end) = spawn_product_graceful_fatal_full_stderr();
        let mut stdin = child.stdin.take().expect("stdin");
        stdin
            .write_all(
                b"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n",
            )
            .expect("write describe");
        drop(stdin);
        let code = wait_nonzero(&mut child, Duration::from_secs(3));
        assert_eq!(
            code, 1,
            "graceful fatal, full stderr: exit code {code}, want 1"
        );
    }
}

#[test]
fn graceful_fatal_diagnostic_still_reported() {
    // Control for the graceful-fatal fix: with a drained stderr the
    // best-effort diagnostic must still land before the exit (only
    // the blocked write is abandoned, not the message).
    let mut child = spawn_product_broken_stdout();
    let mut stdin = child.stdin.take().expect("stdin");
    stdin
        .write_all(
            b"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n",
        )
        .expect("write describe");
    drop(stdin);
    let output = child.wait_with_output().expect("wait_with_output");
    assert_eq!(
        output.status.code(),
        Some(1),
        "graceful fatal control exit {:?}",
        output.status
    );
    let text = String::from_utf8_lossy(&output.stderr);
    assert!(
        text.contains("iprange:"),
        "diagnostic missing from drained stderr: {text}"
    );
}

#[test]
fn oversized_unterminated_frame_answers_and_exits() {
    // Role-round finding: an over-limit frame whose terminator never
    // arrives must still produce -32001 (id null) and a non-zero exit
    // (spec iprange-jsonrpc-v1.md framing section): the frame is
    // invalid once the accumulated bytes exceed the ceiling, so the
    // reader must not wait for LF or EOF.
    for _ in 0..2 {
        let mut child = spawn_product();
        let mut stdin = child.stdin.take().expect("stdin");
        // 1,048,576 is INPUT_FRAME_LIMIT; LIMIT+2 bytes, no LF, and
        // stdin stays open on purpose.
        let limit = 1_048_576usize;
        let chunk = vec![b'x'; 65536];
        let mut written = 0usize;
        while written < limit + 2 {
            let take = chunk.len().min(limit + 2 - written);
            written += stdin.write(&chunk[..take]).expect("write bytes");
        }
        let mut stdout = child.stdout.take().expect("stdout");
        let code = wait_nonzero(&mut child, Duration::from_secs(3));
        assert_eq!(
            code, 1,
            "oversized unterminated: exit code {code}, want 1"
        );
        use std::io::Read;
        let mut text = String::new();
        stdout
            .read_to_string(&mut text)
            .expect("read stdout after exit");
        let mut lines = text.lines();
        let first = lines.next().expect("one -32001 response");
        // -32001 is a JSON number, never quoted.
        assert!(
            first.contains("-32001"),
            "response is not -32001: {first}"
        );
        assert!(
            first.contains("\"id\":null"),
            "over-limit response must carry id null: {first}"
        );
        assert!(
            lines.next().is_none(),
            "only the -32001 response may appear: {text}"
        );
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
