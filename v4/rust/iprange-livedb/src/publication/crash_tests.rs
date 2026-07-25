use std::ffi::OsStr;
use std::fs::{self, File};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::MetadataExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::ErrorCode;
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::publication::attempt;
use crate::publication::file_inspection::{self, Content};
use crate::publication::namespace::Destination;
use crate::publication::output::CreatedOutput;
use crate::publication::reservation::{self, State};
use crate::publication::reservation_inspection::{self, Location};
use crate::test_alloc::count_thread_allocations;
use crate::ImmutableReader;

const CHILD_TEST: &str = "publication::crash_tests::crash_child";
const CHILD_PATH: &str = "IPRANGE_V4_PUBLICATION_CRASH_PATH";

#[test]
fn reservation_crashes_leave_one_complete_output_and_selectable_authority() {
    for (point, location, expected_state) in [
        (
            "publication.after_reservation_state1_sync",
            ReservationPlace::Private,
            Some(State::Prepared),
        ),
        (
            "publication.after_reservation_rename",
            ReservationPlace::Canonical,
            Some(State::Prepared),
        ),
        (
            "publication.after_reservation_directory_sync",
            ReservationPlace::Canonical,
            Some(State::Prepared),
        ),
        (
            "publication.after_reservation_state2_write",
            ReservationPlace::Canonical,
            None,
        ),
        (
            "publication.after_reservation_state2_sync",
            ReservationPlace::Canonical,
            Some(State::MainMayHaveBeenAttempted),
        ),
        (
            "publication.after_reservation_state2_selection",
            ReservationPlace::Canonical,
            Some(State::MainMayHaveBeenAttempted),
        ),
    ] {
        let directory = TempDirectory::new(point);
        let main = directory.path.join("result.v4");
        run_child(&main, point);
        let artifacts = Artifacts::inspect(&directory.path, &main);
        let destination = Destination::bind(&main).unwrap();
        let inspected = reservation_inspection::discover(&destination, &CancellationToken::new())
            .unwrap()
            .unwrap();

        assert!(!main.exists(), "{point}");
        assert_eq!(inspected.location, location, "{point}");
        assert!(
            file_inspection::main(&destination, inspected.header, &CancellationToken::new())
                .unwrap()
                .is_none()
        );
        let output =
            file_inspection::private(&destination, inspected.header, &CancellationToken::new())
                .unwrap()
                .unwrap();
        assert_eq!(output.content, Content::Desired, "{point}");
        assert_eq!(output.identity.encode(), inspected.header.output_identity);
        assert_eq!(artifacts.private_outputs.len(), 1, "{point}");
        assert_complete_output(&artifacts.private_outputs[0]);
        let reservation = match location {
            ReservationPlace::Private => {
                assert!(!artifacts.coordination.exists(), "{point}");
                assert_eq!(artifacts.private_reservations.len(), 1, "{point}");
                &artifacts.private_reservations[0]
            }
            ReservationPlace::Canonical => {
                assert!(artifacts.coordination.exists(), "{point}");
                assert!(artifacts.private_reservations.is_empty(), "{point}");
                &artifacts.coordination
            }
        };
        let selected = selected_reservation(reservation);
        if let Some(expected) = expected_state {
            assert_eq!(selected.state, expected, "{point}");
        } else {
            assert!(
                matches!(
                    selected.state,
                    State::Prepared | State::MainMayHaveBeenAttempted
                ),
                "{point}"
            );
        }
        assert_eq!(
            selected.output_identity,
            local_identity(&artifacts.private_outputs[0]),
            "{point}"
        );
    }
}

#[test]
fn main_crashes_expose_only_the_complete_desired_inode_behind_reservation() {
    for point in [
        "publication.after_main_rename",
        "publication.after_main_sync",
        "publication.after_main_directory_sync",
        "publication.after_main_proof",
    ] {
        let directory = TempDirectory::new(point);
        let main = directory.path.join("result.v4");
        run_child(&main, point);
        let artifacts = Artifacts::inspect(&directory.path, &main);
        let destination = Destination::bind(&main).unwrap();
        let inspected = reservation_inspection::discover(&destination, &CancellationToken::new())
            .unwrap()
            .unwrap();
        let output =
            file_inspection::main(&destination, inspected.header, &CancellationToken::new())
                .unwrap()
                .unwrap();

        assert_complete_output(&main);
        assert_eq!(inspected.location, Location::Canonical, "{point}");
        assert_eq!(output.content, Content::Desired, "{point}");
        assert_eq!(output.identity.encode(), inspected.header.output_identity);
        assert!(file_inspection::private(
            &destination,
            inspected.header,
            &CancellationToken::new()
        )
        .unwrap()
        .is_none());
        assert!(artifacts.private_outputs.is_empty(), "{point}");
        assert!(artifacts.private_reservations.is_empty(), "{point}");
        assert!(artifacts.coordination.exists(), "{point}");
        let selected = selected_reservation(&artifacts.coordination);
        assert_eq!(selected.state, State::MainMayHaveBeenAttempted, "{point}");
        assert_eq!(selected.output_identity, local_identity(&main), "{point}");
        assert!(ImmutableReader::open(&main).is_err(), "{point}");
    }
}

#[test]
fn private_scan_requires_one_unique_bound_reservation() {
    let directory = TempDirectory::new("duplicate-private-reservations");
    let main = directory.path.join("result.v4");
    run_child(&main, "publication.after_reservation_state1_sync");
    run_child(&main, "publication.after_reservation_state1_sync");
    let destination = Destination::bind(&main).unwrap();

    let problem =
        reservation_inspection::discover(&destination, &CancellationToken::new()).unwrap_err();
    assert_eq!(problem.code, ErrorCode::Conflict);
    assert_eq!(
        Artifacts::inspect(&directory.path, &main)
            .private_reservations
            .len(),
        2
    );
}

#[test]
fn malformed_canonical_reservation_is_not_private_scan_authority() {
    let directory = TempDirectory::new("malformed-canonical");
    let main = directory.path.join("result.v4");
    let artifacts = Artifacts::inspect(&directory.path, &main);
    fs::write(&artifacts.coordination, [0; 2 * PAGE_SIZE]).unwrap();
    let destination = Destination::bind(&main).unwrap();

    let problem =
        reservation_inspection::discover(&destination, &CancellationToken::new()).unwrap_err();
    assert_eq!(problem.code, ErrorCode::Unresolvable);
    assert_eq!(
        fs::read(&artifacts.coordination).unwrap(),
        [0; 2 * PAGE_SIZE]
    );
}

#[test]
fn empty_private_scan_is_cancellable_and_allocation_bounded() {
    let directory = TempDirectory::new("bounded-private-scan");
    let main = directory.path.join("result.v4");
    for index in 0..512 {
        fs::write(directory.path.join(format!("foreign-{index}")), b"x").unwrap();
    }
    let destination = Destination::bind(&main).unwrap();
    let cancellation = CancellationToken::new();
    let (found, allocations) =
        count_thread_allocations(|| reservation_inspection::discover(&destination, &cancellation));
    assert!(found.unwrap().is_none());
    assert_eq!(allocations, 0);

    let cancellation = CancellationToken::new();
    cancellation.cancel();
    let problem = reservation_inspection::discover(&destination, &cancellation).unwrap_err();
    assert_eq!(problem.code, ErrorCode::Cancelled);
}

#[test]
fn retirement_crashes_leave_a_normally_openable_complete_main() {
    for point in [
        "publication.after_reservation_unlink",
        "publication.after_retirement_sync",
    ] {
        let directory = TempDirectory::new(point);
        let main = directory.path.join("result.v4");
        run_child(&main, point);
        let artifacts = Artifacts::inspect(&directory.path, &main);

        assert_complete_output(&main);
        assert!(artifacts.private_outputs.is_empty(), "{point}");
        assert!(artifacts.private_reservations.is_empty(), "{point}");
        assert!(!artifacts.coordination.exists(), "{point}");
        let reader = ImmutableReader::open(&main).unwrap();
        assert_eq!(reader.info().transaction_id, 42);
    }
}

#[test]
#[ignore = "subprocess entry point"]
fn crash_child() {
    let main = PathBuf::from(std::env::var_os(CHILD_PATH).unwrap());
    let secured = CreatedOutput::create(&main).unwrap().secure().unwrap();
    let (attempt, file) = secured.into_parts();
    let mut builder = Builder::new(file, direct_spec(), output_budget()).unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(9), 17).unwrap();
    let output = attempt.prepare(builder.finish().unwrap()).unwrap();
    let _ = attempt::fail_if_exists(output);
    panic!("configured publication crash point was not reached");
}

pub(super) fn run_child(main: &Path, point: &str) {
    let status = Command::new(std::env::current_exe().unwrap())
        .arg("--ignored")
        .arg("--exact")
        .arg(CHILD_TEST)
        .env(CHILD_PATH, main)
        .env("IPRANGE_V4_TEST_CRASH_AT", point)
        .status()
        .unwrap();
    assert_eq!(status.code(), Some(86), "{point}");
}

fn assert_complete_output(path: &Path) {
    let file = File::open(path).unwrap();
    let length = file.metadata().unwrap().len();
    let mut pages = [0; 2 * PAGE_SIZE];
    crate::file_io::read_exact_at(&file, &mut pages, 0).unwrap();
    let left: &[u8; PAGE_SIZE] = pages[..PAGE_SIZE].try_into().unwrap();
    let right: &[u8; PAGE_SIZE] = pages[PAGE_SIZE..].try_into().unwrap();
    let opened =
        bootstrap::open_meta_pages(left, right, length, OpenMode::ImmutableReader).unwrap();
    assert_eq!(opened.meta.database_id, [41; 16]);
    assert_eq!(opened.meta.txn_id, 42);
    assert_eq!(opened.meta.commit_nonce, [43; 16]);
}

fn selected_reservation(path: &Path) -> reservation::Header {
    let bytes = fs::read(path).unwrap();
    let selected = reservation::select(&bytes).unwrap().header;
    assert_eq!(selected.database_id, [41; 16]);
    assert_eq!(selected.transaction_id, 42);
    assert_eq!(selected.commit_nonce, [43; 16]);
    assert_eq!(selected.reservation_identity, local_identity(path));
    selected
}

fn local_identity(path: &Path) -> [u8; 32] {
    let metadata = path.metadata().unwrap();
    let mut identity = [0; 32];
    identity[..8].copy_from_slice(&metadata.dev().to_le_bytes());
    identity[8..16].copy_from_slice(&metadata.ino().to_le_bytes());
    identity
}

fn direct_spec() -> OutputSpec {
    OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::RETENTION,
        database_id: [41; 16],
        transaction_id: 42,
        commit_nonce: [43; 16],
        feed_index_limit: 0,
    }
}

fn output_budget() -> OutputBudget {
    OutputBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_output_pages: 100_000,
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum ReservationPlace {
    Private,
    Canonical,
}

impl PartialEq<ReservationPlace> for Location {
    fn eq(&self, other: &ReservationPlace) -> bool {
        matches!(
            (self, other),
            (Self::Private, ReservationPlace::Private)
                | (Self::Canonical, ReservationPlace::Canonical)
        )
    }
}

pub(super) struct Artifacts {
    pub(super) private_outputs: Vec<PathBuf>,
    pub(super) private_reservations: Vec<PathBuf>,
    pub(super) coordination: PathBuf,
}

impl Artifacts {
    pub(super) fn inspect(directory: &Path, main: &Path) -> Self {
        let mut private_outputs = Vec::new();
        let mut private_reservations = Vec::new();
        for entry in fs::read_dir(directory).unwrap() {
            let path = entry.unwrap().path();
            let name = path.file_name().unwrap().as_bytes();
            if name.starts_with(b".iprange-publish-") {
                private_outputs.push(path);
            } else if name.starts_with(b".iprange-reservation-") {
                private_reservations.push(path);
            }
        }
        let mut coordination_name = main.file_name().unwrap().to_os_string();
        coordination_name.push(OsStr::new(".readers"));
        Self {
            private_outputs,
            private_reservations,
            coordination: main.with_file_name(coordination_name),
        }
    }
}

pub(super) struct TempDirectory {
    pub(super) path: PathBuf,
}

impl TempDirectory {
    pub(super) fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-publication-crash-{}-{}-{unique}",
            std::process::id(),
            label.rsplit('.').next().unwrap()
        ));
        fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).unwrap();
    }
}
