//! Shared harness for the legacy-surface parity tests.
//!
//! The compatibility currency of the released `iprange` CLI is the
//! process contract: exit code, stdout bytes, stderr bytes. The C tool
//! is the oracle, so every case here states its own expected contract
//! (which is what makes a regression detectable on a machine without the
//! reference installed) and, when the reference is present, additionally
//! compares all three against it byte for byte.
//!
//! Limitation: stdout and stderr are collected after the run, so the
//! pipes must stay smaller than the OS buffer (about 64 KiB). Cases here
//! print a handful of lines; a case with bulk output must drain the
//! pipes instead of using this helper.
//!
//! Runs that could hang or balloon are bounded two ways, because the
//! failure mode observed for a value-less trailing option was both at
//! once: an unbounded loop *and* gigabytes of allocation within seconds.
//! A wall-clock timeout alone would have taken the full timeout to fail
//! and could have exhausted memory first, so each run also gets an
//! address-space cap (`RLIMIT_AS`): an allocation bomb dies immediately
//! with `SIGABRT` instead of pressing on against the machine's limits.

#![cfg(unix)]
// The harness serves several test targets; any one of them uses a
// subset of it.
#![allow(dead_code)]

use std::ffi::{OsStr, OsString};
use std::os::unix::ffi::OsStringExt;
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

/// The engine under test (the `iprange` binary of this package).
pub const PROGRAM: &str = env!("CARGO_BIN_EXE_iprange");

/// Address space granted to every bounded run: far above what a correct
/// run of this tool needs, far below what a runaway loop can grow into.
pub const MEM_LIMIT_KIB: u64 = 2 * 1024 * 1024;

/// Wall-clock bound for a bounded run.
pub const TIME_BOUND: Duration = Duration::from_secs(10);

/// The outcome of one bounded run.
pub struct Run {
    /// Exit code, `None` when the process died from a signal.
    pub code: Option<i32>,
    /// Terminating signal, when there was one.
    pub signal: Option<i32>,
    /// The run was killed for exceeding [`TIME_BOUND`].
    pub timed_out: bool,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

impl Run {
    /// A one-line rendering for assertion messages.
    pub fn describe(&self) -> String {
        format!(
            "rc={:?} signal={:?} timed_out={} stdout={:?} stderr={:?}",
            self.code,
            self.signal,
            self.timed_out,
            String::from_utf8_lossy(&self.stdout),
            String::from_utf8_lossy(&self.stderr),
        )
    }
}

/// An OS string holding raw bytes (a legal POSIX argument or file name).
pub fn raw(bytes: &[u8]) -> OsString {
    OsStringExt::from_vec(bytes.to_vec())
}

/// A private scratch directory, removed when the returning value drops.
pub struct Scratch(PathBuf);

impl Scratch {
    pub fn new(tag: &str) -> Scratch {
        let mut dir = std::env::temp_dir();
        dir.push(format!(
            "iprange-parity-{}-{}-{tag}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("system clock after 1970")
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).expect("create the scratch directory");
        Scratch(dir)
    }

    pub fn path(&self) -> &Path {
        &self.0
    }

    /// Write `payload` to `name` (raw bytes, so a name may hold 0xFF).
    pub fn file(&self, name: &[u8], payload: &[u8]) -> PathBuf {
        let path = self.0.join(raw(name));
        std::fs::write(&path, payload).expect("write the fixture");
        path
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// Run `program` with these exact argv bytes and `stdin`, under the
/// memory cap and the wall-clock bound.
pub fn run_bounded(program: &Path, argv: &[&OsStr], stdin: &[u8]) -> Run {
    run_bounded_with_limit(program, argv, stdin, MEM_LIMIT_KIB, TIME_BOUND)
}

/// `run_bounded` with an explicit cap and bound (used by the harness
/// self-test, which proves the bounds actually fire).
pub fn run_bounded_with_limit(
    program: &Path,
    argv: &[&OsStr],
    stdin: &[u8],
    mem_limit_kib: u64,
    bound: Duration,
) -> Run {
    let mut cmd = Command::new(program);
    cmd.args(argv)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let limit = (mem_limit_kib * 1024) as libc::rlim_t;
    unsafe {
        // pre_exec runs in the child between fork and exec. Setting the
        // cap here (rather than in the tool) is what keeps a runaway
        // allocation from becoming the machine's problem.
        cmd.pre_exec(move || {
            let rlim = libc::rlimit {
                rlim_cur: limit,
                rlim_max: limit,
            };
            if libc::setrlimit(libc::RLIMIT_AS, &rlim) != 0 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let mut child = cmd.spawn().expect("spawn iprange");

    // Inputs here are small; a write error (child already gone, or a
    // closed stdin) is not the behaviour under test.
    if let (Some(mut pipe), false) = (child.stdin.take(), stdin.is_empty()) {
        use std::io::Write;
        let _ = pipe.write_all(stdin);
        drop(pipe);
    } else {
        child.stdin = None;
    }

    let deadline = Instant::now() + bound;
    let status = loop {
        match child.try_wait().expect("wait for iprange") {
            Some(status) => break status,
            None if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                return Run {
                    code: None,
                    signal: None,
                    timed_out: true,
                    stdout: Vec::new(),
                    stderr: Vec::new(),
                };
            }
            None => std::thread::sleep(Duration::from_millis(5)),
        }
    };
    let output = child.wait_with_output().expect("collect iprange output");
    Run {
        code: status.code(),
        signal: status.signal(),
        timed_out: false,
        stdout: output.stdout,
        stderr: output.stderr,
    }
}

/// The C reference binary, when one is installed. `IPRANGE_REFERENCE`
/// selects it explicitly; otherwise the installed `iprange` is used.
pub fn reference() -> Option<PathBuf> {
    if let Ok(explicit) = std::env::var("IPRANGE_REFERENCE") {
        let path = PathBuf::from(explicit);
        return is_executable(&path).then_some(path);
    }
    let path = PathBuf::from("/usr/bin/iprange");
    is_executable(&path).then_some(path)
}

fn is_executable(path: &Path) -> bool {
    std::fs::metadata(path)
        .map(|md| {
            use std::os::unix::fs::PermissionsExt;
            md.is_file() && md.permissions().mode() & 0o111 != 0
        })
        .unwrap_or(false)
}

/// Assert the engine's contract and, when the C reference is installed,
/// that all three channels match it exactly.
pub fn assert_parity(label: &str, argv: &[&OsStr], stdin: &[u8], expect: (i32, &[u8], &[u8])) {
    let engine = PathBuf::from(PROGRAM);
    let run = run_bounded(&engine, argv, stdin);
    assert!(
        !run.timed_out,
        "{label}: the run exceeded {} s: argv={argv:?}",
        TIME_BOUND.as_secs()
    );
    assert_eq!(
        (run.code, run.signal),
        (Some(expect.0), None),
        "{label}: wrong process contract ({}); expected rc {} with no signal",
        run.describe(),
        expect.0
    );
    assert_eq!(
        run.stdout, expect.1,
        "{label}: wrong stdout bytes (argv={argv:?})"
    );
    assert_eq!(
        run.stderr, expect.2,
        "{label}: wrong stderr bytes (argv={argv:?})"
    );

    let Some(reference) = reference() else {
        return;
    };
    let oracle = run_bounded(&reference, argv, stdin);
    assert!(
        !oracle.timed_out,
        "{label}: the C reference exceeded {} s (argv={argv:?})",
        TIME_BOUND.as_secs()
    );
    assert_eq!(
        (oracle.code, oracle.stdout.clone(), oracle.stderr.clone()),
        (Some(expect.0), expect.1.to_vec(), expect.2.to_vec()),
        "{label}: the pinned expectation drifted from {} (argv={argv:?})",
        reference.display()
    );
}
