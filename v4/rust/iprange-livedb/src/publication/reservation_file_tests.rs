use std::ffi::OsStr;
use std::fs::{self, OpenOptions};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};

use super::*;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::publication::output::CreatedOutput;

#[test]
fn initialized_reservation_has_exact_header_security_and_lock() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let draft = ReservationDraft::create(&output).unwrap();
    let private_path = named_path(&directory.path, &draft.name);
    let reservation = draft.initialize(&output).unwrap();

    assert_eq!(reservation.file.metadata().unwrap().len(), FILE_SIZE as u64);
    assert_eq!(reservation.file.metadata().unwrap().mode() & 0o7777, 0o600);
    assert_eq!(
        reservation.header.reservation_identity,
        reservation.identity.encode()
    );
    assert_eq!(
        reservation.header.output_identity,
        output.attempt.identity().encode()
    );
    assert_eq!(reservation.header.output_sha512, output.sha512);
    assert_eq!(
        reservation.header.basename_len,
        output.attempt.destination().main().bytes().len() as u32
    );
    select_exact(&reservation.mapping, reservation.header, 0).unwrap();

    let contender = OpenOptions::new()
        .read(true)
        .write(true)
        .open(private_path)
        .unwrap();
    assert!(!live_lock::try_lock_file(&contender, OPERATION_LOCK, Mode::Exclusive).unwrap());
}

#[test]
fn acquisition_and_arming_keep_one_inode_and_select_state2() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let draft = ReservationDraft::create(&output).unwrap();
    let private_path = named_path(&directory.path, &draft.name);
    let private = draft.initialize(&output).unwrap();
    let expected_identity = private.identity;
    let canonical_path = named_path(&directory.path, output.attempt.destination().coordination());

    let canonical = private.acquire(&output).unwrap();
    assert!(!private_path.exists());
    assert_eq!(
        canonical_path.metadata().unwrap().ino(),
        expected_identity.inode
    );
    select_exact(&canonical.mapping, canonical.header, 0).unwrap();

    let armed = canonical.arm(&output).unwrap();
    assert_eq!(armed.identity, expected_identity);
    assert_eq!(armed.header.state, State::MainMayHaveBeenAttempted);
    assert_eq!(armed.header.sequence, 2);
    select_exact(&armed.mapping, armed.header, 1).unwrap();
    output.verify_private().unwrap();

    let contender = OpenOptions::new()
        .read(true)
        .write(true)
        .open(canonical_path)
        .unwrap();
    assert!(!live_lock::try_lock_file(&contender, OPERATION_LOCK, Mode::Exclusive).unwrap());
}

#[test]
fn canonical_conflict_never_overwrites_and_returns_private_owner() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let draft = ReservationDraft::create(&output).unwrap();
    let private_path = named_path(&directory.path, &draft.name);
    let private = draft.initialize(&output).unwrap();
    let canonical_path = named_path(&directory.path, output.attempt.destination().coordination());
    fs::write(&canonical_path, b"foreign").unwrap();

    let failure = private.acquire(&output).unwrap_err();
    assert!(failure.owner.namespace_call_started);
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::Exists)
    ));
    assert!(private_path.exists());
    assert_eq!(fs::read(canonical_path).unwrap(), b"foreign");
    select_exact(
        &failure.owner.reservation.mapping,
        failure.owner.reservation.header,
        0,
    )
    .unwrap();
}

#[test]
fn initialization_failure_returns_the_created_reservation() {
    let directory = TempDirectory::new();
    let (output, output_path) = prepared_output(&directory.path);
    let draft = ReservationDraft::create(&output).unwrap();
    let private_path = named_path(&directory.path, &draft.name);
    fs::set_permissions(&output_path, fs::Permissions::from_mode(0o640)).unwrap();

    let failure = draft.initialize(&output).unwrap_err();
    assert!(matches!(
        failure.cause,
        Error::Output(output::Error::Namespace(NamespaceError::AccessPolicy))
    ));
    assert!(private_path.exists());
    assert_eq!(failure.owner.file.metadata().unwrap().len(), 0);
    assert!(!failure.owner.state1_selected);
}

#[test]
fn hard_linked_private_reservation_fails_closed_with_ownership() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let draft = ReservationDraft::create(&output).unwrap();
    let private_path = named_path(&directory.path, &draft.name);
    fs::hard_link(&private_path, directory.path.join("extra-link")).unwrap();

    let failure = draft.initialize(&output).unwrap_err();
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::LinkCount(2))
    ));
    assert_eq!(failure.owner.file.metadata().unwrap().nlink(), 2);
}

#[test]
fn canonical_tampering_fails_before_state2_and_retains_phase() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let canonical = ReservationDraft::create(&output)
        .unwrap()
        .initialize(&output)
        .unwrap()
        .acquire(&output)
        .unwrap();
    let canonical_path = named_path(&directory.path, output.attempt.destination().coordination());
    fs::hard_link(&canonical_path, directory.path.join("extra-link")).unwrap();

    let failure = canonical.arm(&output).unwrap_err();
    assert!(!failure.owner.state2_selected);
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::LinkCount(2))
    ));
    assert_eq!(
        failure.owner.reservation.file.metadata().unwrap().nlink(),
        2
    );
}

#[test]
fn existing_main_is_rejected_before_state2() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let canonical = ReservationDraft::create(&output)
        .unwrap()
        .initialize(&output)
        .unwrap()
        .acquire(&output)
        .unwrap();
    let main = named_path(&directory.path, output.attempt.destination().main());
    fs::write(&main, b"existing").unwrap();

    let failure = canonical.arm(&output).unwrap_err();
    assert!(!failure.owner.state2_selected);
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::Exists)
    ));
    assert_eq!(failure.owner.reservation.header.state, State::Prepared);
    assert_eq!(fs::read(main).unwrap(), b"existing");
}

#[test]
fn failure_after_state2_selection_retains_the_durable_phase() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let canonical = ReservationDraft::create(&output)
        .unwrap()
        .initialize(&output)
        .unwrap()
        .acquire(&output)
        .unwrap();
    let target = canonical.header.state2().unwrap();
    let mut owner = ArmingReservation {
        reservation: canonical,
        target: Some(target),
        state2_selected: false,
    };

    let result = arm_with(&mut owner, &output, |_| Err(Error::HeaderInvariant));
    assert!(matches!(result, Err(Error::HeaderInvariant)));
    assert!(owner.state2_selected);
    select_exact(&owner.reservation.mapping, target, 1).unwrap();
}

#[test]
fn failure_after_state1_selection_retains_the_result_boundary() {
    let directory = TempDirectory::new();
    let (output, _) = prepared_output(&directory.path);
    let mut draft = ReservationDraft::create(&output).unwrap();
    prepare_header(&mut draft, &output).unwrap();
    write_state1(&mut draft).unwrap();

    let result = lock_state1_with(&mut draft, &output, |_| Err(Error::HeaderInvariant));
    assert!(matches!(result, Err(Error::HeaderInvariant)));
    assert!(draft.state1_selected);
    select_exact(draft.mapping.as_ref().unwrap(), draft.header.unwrap(), 0).unwrap();
}

fn prepared_output(directory: &Path) -> (PreparedOutput, PathBuf) {
    let destination = directory.join("result.v4");
    let secured = CreatedOutput::create(&destination)
        .unwrap()
        .secure()
        .unwrap();
    let (attempt, file) = secured.into_parts();
    let private_path = named_path(directory, attempt.name());
    let mut builder = Builder::new_owned(file, direct_spec(), output_budget()).unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(9), 3).unwrap();
    let output = attempt
        .prepare_cancellable(
            builder.finish_owned().unwrap(),
            &crate::CancellationToken::new(),
        )
        .unwrap();
    (output, private_path)
}

fn named_path(directory: &Path, name: &Name) -> PathBuf {
    directory.join(OsStr::from_bytes(name.bytes()))
}

fn direct_spec() -> OutputSpec {
    OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        structure_kind: crate::contract::StructureKind::None,
        value_tag: ValueTag::FIRST_SEEN,
        database_id: [11; 16],
        transaction_id: 13,
        commit_nonce: [12; 16],
        feed_index_limit: 0,
    }
}

fn output_budget() -> OutputBudget {
    OutputBudget {
        max_output_pages: 100_000,
    }
}

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new() -> Self {
        let path = crate::test_support_tests::unique_path("iprange-v4-reservation-file");
        fs::create_dir(&path).unwrap();
        Self { path }
    }
}

impl Drop for TempDirectory {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).unwrap();
    }
}
