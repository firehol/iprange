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
    create_live, resolve_commit, snapshot_to, AddressFamily, CancellationToken, CommitResolution,
    CommitResolutionMode, CommitResult, Error, ImmutableReader, Ipv4Key, LiveReader, LiveWriter,
    LocalFileRelation, ReclaimResult, SnapshotBudget, SnapshotPublicationPolicy,
    SnapshotSourceMode, StructureKind, TransactionBudget, ValueKind, ValueTag,
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
    let _ = commit_gen_result(writer, from, to, value);
}

fn commit_gen_result(writer: &mut LiveWriter, from: u32, to: u32, value: u32) -> CommitResult {
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction
        .assign_v4(Ipv4Key(from), Ipv4Key(to), value)
        .unwrap();
    transaction.commit().unwrap()
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

fn run_go_child_with_snapshot(binary: &Path, main: &Path, snapshot: &Path) -> GoChildRun {
    let mut child = Command::new(binary)
        .current_dir(env!("CARGO_MANIFEST_DIR").to_string() + "/../../go")
        .args(["-test.run=^TestMixedLiveGoChild$"])
        .env("IPRANGE_V4_GO_MIXED_CHILD", "1")
        .env("IPRANGE_V4_MIXED_LIVE_DB", main)
        .env("IPRANGE_V4_MIXED_LIVE_MODE", "snapshot")
        .env("IPRANGE_V4_MIXED_LIVE_SNAPSHOT", snapshot)
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
    // A second replacement (generation 4) keeps the stale slot pinned:
    // the stale-slot reservation must survive repeated sidecar
    // replacements in the foreign language.
    commit_gen(&mut writer, 20, 40, 3);
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

fn altered_attempt(source: &CommitResult, transaction_id: u64, nonce: [u8; 16]) -> CommitResult {
    // The norm only records outcome-unknown durability with empty
    // cleanup for a replayed attempt (commit_lifecycle parity).
    CommitResult {
        attempted_database_id: source.attempted_database_id,
        directory_identity: source.directory_identity,
        main_identity: source.main_identity,
        attempted_transaction_id: transaction_id,
        attempted_commit_nonce: nonce,
        durability: iprange_livedb::CommitDurability::OutcomeUnknown,
        cleanup: iprange_livedb::CommitCleanupArtifacts::default(),
        coordination_cleanup: iprange_livedb::publication::CoordinationCleanup::None,
        cause: None,
    }
}

#[test]
fn go_child_reads_after_sidecar_replacement() {
    if env::var("IPRANGE_V4_MIXED_LIVE").as_deref() != Ok("1") {
        eprintln!("mixed_live: set IPRANGE_V4_MIXED_LIVE=1 to run the cross-language battery");
        return;
    }
    let Some(binary) = go_test_binary() else {
        return;
    };
    let main = unique_path("sidecar");
    create(&main);
    let sidecar = PathBuf::from(format!("{}.readers", main.display()));
    let mut writer = LiveWriter::open(&main, budget(), &CancellationToken::new()).unwrap();
    // The external reader sidecar is a full replacement on every commit
    // (one header page plus capacity 16-byte slots: 4160 bytes at
    // capacity 4), never an append; the Go child must read the newest
    // generation after the Rust parent replaced the sidecar twice.
    let sidecar_bytes = || -> Vec<u8> {
        let bytes = std::fs::read(&sidecar)
            .unwrap_or_else(|error| panic!("read {}: {error}", sidecar.display()));
        assert_eq!(
            bytes.len(),
            4160,
            "sidecar length must be 4160 (capacity 4 slots)"
        );
        bytes
    };
    commit_gen(&mut writer, 10, 30, 1); // generation 2
    let first = sidecar_bytes();
    // A held reader slot makes the replacement visible: the slot table
    // must record the reader across the commit, so the sidecar content
    // changes while its canonical length stays fixed (replaced, never
    // appended).
    let mut reader = LiveReader::open(&main, &CancellationToken::new()).unwrap();
    commit_gen(&mut writer, 12, 18, 2); // generation 3
    let second = sidecar_bytes();
    if first == second {
        panic!("sidecar identical across replacement commits; want a replaced file");
    }
    reader.close().unwrap();
    writer.close().unwrap();
    let deadline = Instant::now() + Duration::from_secs(90);
    let mut run = run_go_child(&binary, &main, "reader");
    finish_go_child(&mut run, deadline, "reader");
    drop(run);
    cleanup(&main);
    drop(std::fs::remove_file(&binary));
}

#[test]
fn go_child_resolves_cross_language() {
    if env::var("IPRANGE_V4_MIXED_LIVE").as_deref() != Ok("1") {
        eprintln!("mixed_live: set IPRANGE_V4_MIXED_LIVE=1 to run the cross-language battery");
        return;
    }
    let Some(binary) = go_test_binary() else {
        return;
    };
    let main = unique_path("resolve");
    create(&main);
    let mut writer = LiveWriter::open(&main, budget(), &CancellationToken::new()).unwrap();
    commit_gen_result(&mut writer, 10, 20, 7); // generation 2
    let committed = commit_gen_result(&mut writer, 10, 20, 8); // generation 3
    writer.close().unwrap();
    // Classify the canonical attempt set with this SDK first.
    let token = CancellationToken::new();
    let correct = resolve_commit(&main, &committed, CommitResolutionMode::Live, &token).unwrap();
    assert_eq!(correct.resolution, CommitResolution::Committed);
    assert_eq!(
        correct.local_file_relation,
        LocalFileRelation::SameLocalFile
    );
    let wrong_nonce = altered_attempt(&committed, committed.attempted_transaction_id, [0x55; 16]);
    assert_eq!(
        resolve_commit(&main, &wrong_nonce, CommitResolutionMode::Live, &token)
            .unwrap()
            .resolution,
        CommitResolution::NotCommitted
    );
    let old_unknown = altered_attempt(&committed, 1, [0x66; 16]);
    assert_eq!(
        resolve_commit(&main, &old_unknown, CommitResolutionMode::Live, &token)
            .unwrap()
            .resolution,
        CommitResolution::SupersededUnknown
    );
    // The Go child commits its own generation on the same live database
    // and must classify the identical set with the Go SDK.
    let deadline = Instant::now() + Duration::from_secs(90);
    let mut run = run_go_child(&binary, &main, "resolve");
    finish_go_child(&mut run, deadline, "resolve");
    drop(run);
    cleanup(&main);
    drop(std::fs::remove_file(&binary));
}

#[test]
fn go_child_reads_cross_language_snapshot() {
    if env::var("IPRANGE_V4_MIXED_LIVE").as_deref() != Ok("1") {
        eprintln!("mixed_live: set IPRANGE_V4_MIXED_LIVE=1 to run the cross-language battery");
        return;
    }
    let Some(binary) = go_test_binary() else {
        return;
    };
    let main = unique_path("snap-main");
    let snapshot = unique_path("snap-out");
    create(&main);
    let mut writer = LiveWriter::open(&main, budget(), &CancellationToken::new()).unwrap();
    commit_gen(&mut writer, 10, 20, 7); // generation 2
    writer.close().unwrap();
    snapshot_to(
        &main,
        SnapshotSourceMode::Live,
        &snapshot,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(32 * 1024 * 1024, 200_000, 3),
        &CancellationToken::new(),
    )
    .unwrap();
    let deadline = Instant::now() + Duration::from_secs(90);
    let mut run = run_go_child_with_snapshot(&binary, &main, &snapshot);
    finish_go_child(&mut run, deadline, "snapshot");
    drop(run);
    cleanup(&main);
    drop(std::fs::remove_file(&snapshot));
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
            // The pinned view must survive the parent's generation 3
            // and 4 (repeated sidecar replacements): generation 2
            // values stay visible to this reader.
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(1));
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(19)).unwrap(), Some(1));
            reader.close().unwrap();
        }
        Ok("resolve") => {
            // The parent committed generation 2 on this live database
            // and resolved the canonical attempt set with its SDK.
            // Commit our own generation 3 on the same database and
            // classify the identical set: the two SDKs must agree.
            let mut writer = LiveWriter::open(&main, budget(), &CancellationToken::new()).unwrap();
            commit_gen_result(&mut writer, 15, 19, 5);
            let committed = commit_gen_result(&mut writer, 15, 19, 6);
            writer.close().unwrap();
            let token = CancellationToken::new();
            let correct =
                resolve_commit(&main, &committed, CommitResolutionMode::Live, &token).unwrap();
            assert_eq!(correct.resolution, CommitResolution::Committed);
            assert_eq!(
                correct.local_file_relation,
                LocalFileRelation::SameLocalFile
            );
            let wrong_nonce =
                altered_attempt(&committed, committed.attempted_transaction_id, [0x55; 16]);
            assert_eq!(
                resolve_commit(&main, &wrong_nonce, CommitResolutionMode::Live, &token)
                    .unwrap()
                    .resolution,
                CommitResolution::NotCommitted
            );
            let old_unknown = altered_attempt(&committed, 1, [0x66; 16]);
            assert_eq!(
                resolve_commit(&main, &old_unknown, CommitResolutionMode::Live, &token)
                    .unwrap()
                    .resolution,
                CommitResolution::SupersededUnknown
            );
        }
        Ok("snapshot") => {
            // The parent snapshotted a live database through its public
            // SnapshotTo; open the compact output with our reader.
            let snapshot =
                PathBuf::from(env::var("IPRANGE_V4_MIXED_LIVE_SNAPSHOT").expect("snapshot env"));
            let reader = ImmutableReader::open(&snapshot).unwrap();
            assert_eq!(reader.info().transaction_id, 2);
            assert_eq!(reader.lookup_direct_v4(Ipv4Key(15)).unwrap(), Some(7));
        }
        other => panic!("unknown mixed_live mode {other:?}"),
    }
}
