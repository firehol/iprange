// SOW-0025 chunk-6 design record D6: mixed subprocess cross-open gate.
// The Rust parent spawns this test binary as a child process; the child
// opens the Rust-produced fixture and the Go-produced fixture with the
// real immutable reader and exits with the verdict. A child that dies,
// hangs, or reports a lookup mismatch fails the parent test. Both sides
// carry a 90 s deadline so a hung child cannot stall the suite
// (Go subprocess_cross_open_test.go parity).

use std::path::{Path, PathBuf};
use std::process::Command;
use std::thread;
use std::time::{Duration, Instant};

use iprange_livedb::{ImmutableReader, Ipv4Key};

const CHILD: &str = "go_produced_fixtures_cross_open_child";
const CHILD_TIMEOUT: Duration = Duration::from_secs(90);

fn corpus_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance")
}

#[test]
fn go_produced_fixtures_cross_open() {
    let mut child = Command::new(std::env::current_exe().unwrap())
        .args(["--ignored", "--exact", CHILD, "--test-threads=1"])
        .spawn()
        .unwrap();
    let deadline = Instant::now() + CHILD_TIMEOUT;
    loop {
        match child.try_wait().unwrap() {
            Some(status) => {
                assert!(status.success());
                break;
            }
            None if Instant::now() > deadline => {
                child.kill().unwrap();
                panic!("mixed subprocess child timed out after {CHILD_TIMEOUT:?}");
            }
            None => thread::sleep(Duration::from_millis(100)),
        }
    }
}

#[test]
#[ignore = "subprocess child entry point (spawned by go_produced_fixtures_cross_open)"]
fn go_produced_fixtures_cross_open_child() {
    // Self-deadline so a hang cannot linger after the parent died.
    thread::spawn(|| {
        thread::sleep(CHILD_TIMEOUT);
        std::process::exit(1);
    });

    let root = corpus_root();

    // The Rust-produced fixture still opens and matches the contract.
    let rust = ImmutableReader::open(root.join("rust/direct-ipv4.iprdb")).unwrap();
    assert_eq!(
        rust.lookup_direct_v4(Ipv4Key(0x0a00_0010)).unwrap(),
        Some(3),
        "rust fixture 10.0.0.16"
    );

    // The Go-produced fixture opens with the same reader and matches.
    let go = ImmutableReader::open(root.join("go/direct-ipv4.iprdb")).unwrap();
    assert_eq!(
        go.lookup_direct_v4(Ipv4Key(0x0a00_0010)).unwrap(),
        Some(3),
        "go fixture 10.0.0.16"
    );

    // The Go-produced history projection destination opens and carries
    // the three last-seen feeds with their full 1000-point range tree
    // (the Rust conformance suite verifies every range and projection
    // of this fixture; the smoke verdict here is catalog + record
    // count).
    let history =
        ImmutableReader::open(root.join("go/history-membership-ipv4.iprdb")).unwrap();
    for (name, index) in [("one", 0u32), ("two", 1), ("three", 2)] {
        assert_eq!(
            history.lookup_feed(name).unwrap().unwrap().index,
            index,
            "go history fixture feed {name}"
        );
    }
    let history_info = history.info();
    assert_eq!(history_info.range_record_count, 1000);
    assert_eq!(history_info.active_feed_count, 3);
}
