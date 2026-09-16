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

/// `run_bounded` with the working directory pinned to `cwd`, so a case
/// can name its fixtures by relative path and keep the pinned
/// expectation free of the scratch-directory name.
pub fn run_bounded_in(program: &Path, cwd: &Path, argv: &[&OsStr], stdin: &[u8]) -> Run {
    run_bounded_at(program, Some(cwd), argv, stdin, MEM_LIMIT_KIB, TIME_BOUND)
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
    run_bounded_at(program, None, argv, stdin, mem_limit_kib, bound)
}

fn run_bounded_at(
    program: &Path,
    cwd: Option<&Path>,
    argv: &[&OsStr],
    stdin: &[u8],
    mem_limit_kib: u64,
    bound: Duration,
) -> Run {
    let mut cmd = Command::new(program);
    if let Some(cwd) = cwd {
        cmd.current_dir(cwd);
    }
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

/// The single line the C `-v` run prints at exit
/// (`src/iprange.c:1216`): `completed in %0.5f seconds (read %0.5f +
/// think %0.5f + speak %0.5f)`. Its four durations are wall-clock
/// measurements of this process, so no engine can reproduce them.
///
/// The pattern is deliberately exact - the literal prefix, the literal
/// separators, and one unsigned decimal with exactly five fraction
/// digits at each of the four positions (`%0.5f` always emits five) -
/// and it is anchored to the whole line. Nothing else can match it, so
/// this is a per-line exception, not an output normalization: a
/// missing, duplicated, misplaced, or differently shaped timing line
/// still fails the comparison.
pub fn is_wallclock_line(line: &[u8]) -> bool {
    wallclock_shape(line).is_ok()
}

fn wallclock_shape(line: &[u8]) -> Result<(), ()> {
    fn decimal(s: &[u8]) -> Option<(&[u8], usize)> {
        let digits = s.iter().take_while(|b| b.is_ascii_digit()).count();
        if digits == 0 {
            return None;
        }
        let rest = &s[digits..];
        let rest = rest.strip_prefix(b".")?;
        let frac = rest.iter().take_while(|b| b.is_ascii_digit()).count();
        if frac != 5 {
            return None;
        }
        Some((&rest[frac..], digits + 1 + frac))
    }
    let mut s = line.strip_prefix(b"completed in ").ok_or(())?;
    for sep in [
        b" seconds (read ".as_slice(),
        b" + think ".as_slice(),
        b" + speak ".as_slice(),
    ] {
        s = decimal(s).ok_or(())?.0;
        s = s.strip_prefix(sep).ok_or(())?;
    }
    s = decimal(s).ok_or(())?.0;
    match s.strip_prefix(b")") {
        Some(b"") => Ok(()),
        _ => Err(()),
    }
}

/// Replace every wall-clock line by the literal `<WALLCLOCK>` marker.
/// The comparison is otherwise byte for byte, so a case states where
/// the timing line belongs and how many there are.
pub fn mask_wallclock(bytes: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(bytes.len());
    for (i, line) in bytes.split(|b| *b == b'\n').enumerate() {
        if i > 0 {
            out.push(b'\n');
        }
        if is_wallclock_line(line) {
            out.extend_from_slice(b"<WALLCLOCK>");
        } else {
            out.extend_from_slice(line);
        }
    }
    out
}

/// Assert the contract of a `-v` run: exit code and stdout byte for
/// byte, and stderr byte for byte once the single wall-clock line is
/// masked. `expect.2` carries the literal `<WALLCLOCK>` line where the
/// C prints its timing line (the IPv6 twin prints none). The C
/// reference, when installed, must agree with the same masked bytes.
pub fn assert_verbose_parity(
    label: &str,
    cwd: &Path,
    argv: &[&OsStr],
    stdin: &[u8],
    expect: (i32, &[u8], &[u8]),
) {
    assert_parity_masked(label, cwd, argv, stdin, expect, &mask_wallclock);
}

/// The same contract with an extra line-level mask owned by the caller.
///
/// `mask` is applied to the engine stderr, to the C stderr, and to the pinned
/// expectation, so a caller can retire a line the C itself does not reproduce
/// (a line derived from elapsed time) while every other line stays byte-exact.
/// `mask_wallclock` is always applied first; `expect.2` therefore still carries
/// the literal `<WALLCLOCK>` line.
pub fn assert_parity_masked(
    label: &str,
    cwd: &Path,
    argv: &[&OsStr],
    stdin: &[u8],
    expect: (i32, &[u8], &[u8]),
    mask: &dyn Fn(&[u8]) -> Vec<u8>,
) {
    let engine = PathBuf::from(PROGRAM);
    let run = run_bounded_in(&engine, cwd, argv, stdin);
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
    assert_eq!(run.stdout, expect.1, "{label}: wrong stdout bytes");
    let masked = mask(&mask_wallclock(&run.stderr));
    let expected = mask(expect.2);
    assert_eq!(
        masked,
        expected,
        "{label}: wrong stderr bytes (raw {:?})",
        String::from_utf8_lossy(&run.stderr)
    );

    let Some(reference) = reference() else {
        return;
    };
    let oracle = run_bounded_in(&reference, cwd, argv, stdin);
    assert!(
        !oracle.timed_out,
        "{label}: the C reference exceeded {} s (argv={argv:?})",
        TIME_BOUND.as_secs()
    );
    assert_eq!(
        (oracle.code, oracle.stdout.clone()),
        (Some(expect.0), expect.1.to_vec()),
        "{label}: the C reference disagrees on rc/stdout"
    );
    assert_eq!(
        mask(&mask_wallclock(&oracle.stderr)),
        expected,
        "{label}: the pinned expectation drifted from {} (raw C stderr {:?})",
        reference.display(),
        String::from_utf8_lossy(&oracle.stderr)
    );
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
