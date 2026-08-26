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

use iprange_livedb::{ImmutableReader, Ipv4Key, NetworkEnrichmentV1Location};

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
    let history = ImmutableReader::open(root.join("go/history-membership-ipv4.iprdb")).unwrap();
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

    // The Go-produced structured fixtures open with the same reader and
    // resolve the canonical broad/narrow and plain/bare enrichment values
    // (the Rust conformance suite verifies every range, feed link, and
    // membership of these fixtures; the smoke verdict here is one typed
    // lookup per fixture plus the feed catalog of the threat fixture).
    let structured = ImmutableReader::open(root.join("go/structured-ipv4.iprdb")).unwrap();
    let broad = structured
        .lookup_network_enrichment_v1_v4(Ipv4Key(0x0a01_0000))
        .unwrap()
        .expect("go structured fixture 10.1.0.0")
        .value();
    assert_eq!(broad.asn, 64512, "go structured fixture broad asn");
    assert_eq!(
        broad.location,
        Some(NetworkEnrichmentV1Location {
            latitude_microdegrees: 37_983_810,
            longitude_microdegrees: 23_727_539
        })
    );
    assert_eq!(
        structured.lookup_feed("botnet").unwrap().unwrap().index,
        0,
        "go structured fixture feed botnet"
    );
    assert_eq!(
        structured.lookup_feed("scanner").unwrap().unwrap().index,
        1,
        "go structured fixture feed scanner"
    );
    // 10.1.0.100 is inside the cleared hole (10.1.0.100-109): the
    // lookup reports absence, proving the Go writer's clear reached the
    // on-disk range tree.
    assert!(
        structured
            .lookup_network_enrichment_v1_v4(Ipv4Key(0x0a01_0064))
            .unwrap()
            .is_none(),
        "go structured fixture clear hole 10.1.0.100"
    );

    let nothreat = ImmutableReader::open(root.join("go/structured-ipv4-nothreat.iprdb")).unwrap();
    let plain = nothreat
        .lookup_network_enrichment_v1_v4(Ipv4Key(0x0a02_0000))
        .unwrap()
        .expect("go nothreat fixture 10.2.0.0")
        .value();
    assert_eq!(plain.asn, 64514, "go nothreat fixture plain asn");
    assert!(
        nothreat.lookup_feed("botnet").unwrap().is_none(),
        "go nothreat fixture has no feeds"
    );
}
