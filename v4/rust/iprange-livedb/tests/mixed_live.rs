//! Cross-language live cooperation (SOW-0027 milestone 3 slice 3b).
//!
//! The Rust parent spawns the Go test binary as a child that opens the
//! same live database with the Go SDK (read, exclusion, pinned-read
//! reclamation); the `mixed_live_rust_child` entry is spawned by the Go
//! test parent to run the mirrored direction with the Rust SDK.
//!
//! Both parents are env-gated (IPRANGE_V4_MIXED_LIVE=1, documented in
//! the conformance README) so plain suites stay fast; the children are
//! explicit entry points exactly like the same-language subprocess
//! pattern. The battery is linux/amd64-only (recorded in SOW-0027).

use std::env;
use std::io::{BufRead, BufReader, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, Error, Ipv4Key, LiveReader, LiveWriter,
    ReclaimResult, StructureKind, TransactionBudget, ValueKind, ValueTag,
};

static COUNTER: AtomicU64 = AtomicU64::new(0);

fn unique_path(label: &str) -> PathBuf {
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let n = COUNTER.fetch_add(1, Ordering::Relaxed);
    std::env::temp_dir().join(format!(
        "iprange-v4-mixed-{label}-{}-{nanos}-{n}",
        std::process::id()
    ))
}

fn cleanup(main: &Path) {
    let _ = std::fs::remove_file(main);
    let mut sidecar = main.as_os_str().to_owned();
    sidecar.push(".readers");
    let _ = std::fs::remove_file(Path::new(&sidecar));
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

fn create(main: &Path) {
    create_live(
        main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        StructureKind::None,
        ValueTag::new(b"asn").unwrap(),
        4,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn commit_gen(writer: &mut LiveWriter, from: u32, to: u32, value: u32) {
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction
        .assign_v4(Ipv4Key(from), Ipv4Key(to), value)
        .unwrap();
    transaction.commit().unwrap();
}

// ---------------------------------------------------------------------
// Go parent helper: build the Go test binary once and run a child.

fn go_test_binary() -> Option<PathBuf> {
    if Command::new("go").arg("version").output().is_err() {
        eprintln!("mixed_live: go toolchain not found; skipping the Go-child direction");
        return None;
    }
    let out = unique_path("go-child.test");
    let status = Command::new("nice")
        .args(["go", "test", "-c", "-o", out.to_str().unwrap(), "."])
        .current_dir(env!("CARGO_MANIFEST_DIR").to_string() + "/../../go")
        .status()
        .unwrap();
    if !status.success() {
        eprintln!("mixed_live: go test -c failed; skipping the Go-child direction");
        return None;
    }
    Some(out)
}

struct GoChildRun {
    child: std::process::Child,
    stdin: Option<std::process::ChildStdin>,
    // The stdout read end stays open until the child has exited so its
    // final report cannot hit a dropped pipe (SIGPIPE).
    stdout: Option<BufReader<std::process::ChildStdout>>,
    stderr: Option<std::process::ChildStderr>,
}

fn run_go_child(binary: &Path, main: &Path, mode: &str) -> GoChildRun {
    // The Go test binary's TestMain builds the worker harness and
    // locates the v4/go module root from the working directory, so the
    // child must start inside the Go module (same-language subprocess
    // pattern runs it from v4/go too).
    let go_module = env!("CARGO_MANIFEST_DIR").to_string() + "/../../go";
    let mut child = Command::new(binary)
        .current_dir(&go_module)
        .args(["-test.run=^TestMixedLiveGoChild$"])
        .env("IPRANGE_V4_GO_MIXED_CHILD", "1")
        .env("IPRANGE_V4_MIXED_LIVE_DB", main)
        .env("IPRANGE_V4_MIXED_LIVE_MODE", mode)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let stdin = child.stdin.take().unwrap();
    let stdout = child.stdout.take().unwrap();
    let stderr = child.stderr.take();
    GoChildRun {
        child,
        stdin: Some(stdin),
        stdout: Some(BufReader::new(stdout)),
        stderr,
    }
}

fn finish_go_child(run: &mut GoChildRun, deadline: Instant, label: &str) {
    // Close stdin so a pinned child can release, then demand a clean
    // exit before the deadline.
    drop(run.stdin.take());
    loop {
        match run.child.try_wait().unwrap() {
            Some(status) => {
                if !status.success() {
                    let mut err_text = String::new();
                    if let Some(mut stdout) = run.stdout.take() {
                        let _ = stdout.read_to_string(&mut err_text);
                    }
                    if let Some(mut stderr) = run.stderr.take() {
                        let _ = stderr.read_to_string(&mut err_text);
                    }
                    panic!("{label}: go child failed: {status:?}\n{err_text}");
                }
                return;
            }
            None if Instant::now() > deadline => {
                let _ = run.child.kill();
                panic!("{label}: go child timed out");
            }
            None => std::thread::sleep(Duration::from_millis(50)),
        }
    }
}

fn wait_ready(run: &mut GoChildRun, deadline: Instant, label: &str) {
    let mut reader = run.stdout.take().expect("go child stdout");
    loop {
        let mut line = String::new();
        match reader.read_line(&mut line) {
            Ok(0) => {
                if Instant::now() > deadline {
                    let _ = run.child.kill();
                    panic!("{label}: go child never became ready");
                }
                std::thread::sleep(Duration::from_millis(10));
            }
            Ok(_) if line.trim_end() == "READY" => {
                // Keep the pipe open for the child's final report.
                run.stdout = Some(reader);
                return;
            }
            Ok(_) => continue,
            Err(error) => panic!("{label}: go child stdout: {error}"),
        }
    }
}

#[test]
fn go_child_reads_go_and_committed_generations() {
    if env::var("IPRANGE_V4_MIXED_LIVE").as_deref() != Ok("1") {
        eprintln!("mixed_live: set IPRANGE_V4_MIXED_LIVE=1 to run the cross-language battery");
        return;
    }
    let Some(binary) = go_test_binary() else {
        return;
    };
    let main = unique_path("reader");
    create(&main);
    {
        let mut writer = LiveWriter::open(&main, budget(), &CancellationToken::new()).unwrap();
        commit_gen(&mut writer, 10, 30, 1); // generation 2
        commit_gen(&mut writer, 12, 18, 2); // generation 3
        writer.close().unwrap();
    }
    let deadline = Instant::now() + Duration::from_secs(90);
    let mut run = run_go_child(&binary, &main, "reader");
    finish_go_child(&mut run, deadline, "reader");
    drop(run);
    cleanup(&main);
    drop(std::fs::remove_file(&binary));
}

#[test]
fn go_child_observes_writer_exclusion() {
    if env::var("IPRANGE_V4_MIXED_LIVE").as_deref() != Ok("1") {
        eprintln!("mixed_live: set IPRANGE_V4_MIXED_LIVE=1 to run the cross-language battery");
        return;
    }
    let Some(binary) = go_test_binary() else {
        return;
    };
    let main = unique_path("exclusion");
    create(&main);
    let mut writer = LiveWriter::open(&main, budget(), &CancellationToken::new()).unwrap();
    commit_gen(&mut writer, 10, 30, 1);
    // The writer stays open: the Go child must fail to open its own.
    let deadline = Instant::now() + Duration::from_secs(90);
    let mut run = run_go_child(&binary, &main, "exclusion");
    finish_go_child(&mut run, deadline, "exclusion");
    drop(run);
    writer.close().unwrap();
    cleanup(&main);
    drop(std::fs::remove_file(&binary));
}

#[test]
fn go_child_pins_reclamation_across_languages() {
    if env::var("IPRANGE_V4_MIXED_LIVE").as_deref() != Ok("1") {
        eprintln!("mixed_live: set IPRANGE_V4_MIXED_LIVE=1 to run the cross-language battery");
        return;
    }
    let Some(binary) = go_test_binary() else {
        return;
    };
    let main = unique_path("reclaim");
    create(&main);
    let mut writer = LiveWriter::open(&main, budget(), &CancellationToken::new()).unwrap();
    commit_gen(&mut writer, 10, 30, 1); // generation 2, pinned by the child
    let deadline = Instant::now() + Duration::from_secs(90);
    let mut run = run_go_child(&binary, &main, "pinned");
    wait_ready(&mut run, deadline, "pinned");
    commit_gen(&mut writer, 12, 18, 2); // generation 3 retires generation 2
    assert!(matches!(
        writer
            .reclaim(10, 10_000, &CancellationToken::new())
            .unwrap(),
        ReclaimResult::NoChange
    ));
    finish_go_child(&mut run, deadline, "pinned");
    drop(run);
    match writer
        .reclaim(10, 10_000, &CancellationToken::new())
        .unwrap()
    {
        ReclaimResult::NoChange => panic!("released child left reclamation blocked"),
        _ => {}
    }
    writer.close().unwrap();
    cleanup(&main);
    drop(std::fs::remove_file(&binary));
}

// ---------------------------------------------------------------------
// Rust child entry (spawned by the Go parent, mode via env).

#[test]
#[ignore = "spawned by the Go mixed_live parent (IPRANGE_V4_MIXED_LIVE=1)"]
fn mixed_live_rust_child() {
    let main = PathBuf::from(env::var("IPRANGE_V4_MIXED_LIVE_DB").expect("child db env"));
    match env::var("IPRANGE_V4_MIXED_LIVE_MODE").as_deref() {
        Ok("reader") => {
            let mut reader = LiveReader::open(&main, &CancellationToken::new()).unwrap();
            assert_eq!(reader.info().unwrap().transaction_id, 3);
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(2));
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(19)).unwrap(), Some(1));
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(22)).unwrap(), Some(1));
            reader.close().unwrap();
        }
        Ok("exclusion") => {
            let mut reader = LiveReader::open(&main, &CancellationToken::new()).unwrap();
            assert!(reader.info().is_ok());
            assert!(matches!(
                LiveWriter::open(&main, budget(), &CancellationToken::new()),
                Err(Error::WriterBusy)
            ));
            reader.close().unwrap();
        }
        Ok("pinned") => {
            let mut reader = LiveReader::open(&main, &CancellationToken::new()).unwrap();
            assert_eq!(reader.info().unwrap().transaction_id, 2);
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(1));
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(19)).unwrap(), Some(1));
            println!("READY");
            std::io::stdout().flush().unwrap();
            // Hold the pinned generation until the parent releases us.
            let mut sink = Vec::new();
            std::io::stdin().read_to_end(&mut sink).unwrap();
            // The pinned view must survive the parent's generation 3:
            // generation 2 values stay visible to this reader.
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(1));
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(19)).unwrap(), Some(1));
            reader.close().unwrap();
        }
        other => panic!("unknown mixed_live mode {other:?}"),
    }
}
