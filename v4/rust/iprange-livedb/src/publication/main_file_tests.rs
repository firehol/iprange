use std::ffi::OsStr;
use std::fs::{self, OpenOptions};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::MetadataExt;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::publication::namespace::Name;
use crate::publication::output::CreatedOutput;
use crate::publication::replacement;
use crate::publication::reservation_file::ReservationDraft;

#[test]
fn exact_inode_is_published_locked_and_cleanly_retired() {
    let directory = TempDirectory::new();
    let (output, reservation, paths) = armed_attempt(&directory.path);
    let output_identity = output.attempt.identity();
    let output_digest = output.sha512;
    let reservation_identity = reservation.identity;

    let published = publish(output, reservation).unwrap();
    assert!(!paths.private_output.exists());
    assert_eq!(paths.main.metadata().unwrap().ino(), output_identity.inode);
    assert_eq!(
        paths.coordination.metadata().unwrap().ino(),
        reservation_identity.inode
    );
    assert_eq!(published.output.sha512, output_digest);
    published.output.verify_main().unwrap();
    published
        .reservation
        .verify_after_main(&published.output)
        .unwrap();

    let main_contender = read_write(&paths.main);
    let reservation_contender = read_write(&paths.coordination);
    assert!(!live_lock::try_lock(&main_contender, MAIN_LIFETIME_LOCK, Mode::Exclusive).unwrap());
    assert!(!live_lock::try_lock(&reservation_contender, 0, Mode::Exclusive).unwrap());

    let completed = published.retire().unwrap();
    assert!(!paths.coordination.exists());
    assert_eq!(completed.reservation_identity, reservation_identity);
    completed._output_guard.verify_main().unwrap();
    assert!(!live_lock::try_lock(&main_contender, MAIN_LIFETIME_LOCK, Mode::Exclusive).unwrap());
}

#[test]
fn racing_main_is_never_overwritten_after_state2() {
    let directory = TempDirectory::new();
    let (output, reservation, paths) = armed_attempt(&directory.path);
    fs::write(&paths.main, b"foreign").unwrap();

    let failure = publish(output, reservation).unwrap_err();
    assert!(!failure.owner.main_call_started);
    assert!(!failure.owner.rename_succeeded);
    assert!(!failure.owner.desired_proven);
    assert!(matches!(
        failure.cause,
        Error::Namespace(NamespaceError::Exists)
    ));
    assert_eq!(fs::read(&paths.main).unwrap(), b"foreign");
    assert!(paths.private_output.exists());
    assert!(paths.coordination.exists());
}

#[test]
fn failure_after_rename_retains_an_ambiguous_complete_main() {
    let directory = TempDirectory::new();
    let (output, reservation, paths) = armed_attempt(&directory.path);
    let expected = output.attempt.identity();
    let failure = publish_with(output, reservation, |point| {
        if point == Point::MainRenamed {
            Err(Error::Injected)
        } else {
            Ok(())
        }
    })
    .unwrap_err();

    assert!(failure.owner.main_call_started);
    assert!(failure.owner.rename_succeeded);
    assert!(!failure.owner.desired_proven);
    assert!(matches!(failure.cause, Error::Injected));
    assert_eq!(paths.main.metadata().unwrap().ino(), expected.inode);
    assert!(!paths.private_output.exists());
    failure.owner.output.verify_main().unwrap();
    failure
        .owner
        .reservation
        .verify_after_main(&failure.owner.output)
        .unwrap();
}

#[test]
fn failure_after_desired_proof_remains_factually_published() {
    let directory = TempDirectory::new();
    let (output, reservation, paths) = armed_attempt(&directory.path);
    let failure = publish_with(output, reservation, |point| {
        if point == Point::DesiredProven {
            Err(Error::Injected)
        } else {
            Ok(())
        }
    })
    .unwrap_err();

    assert!(failure.owner.desired_proven);
    assert!(matches!(failure.cause, Error::Injected));
    failure.owner.output.verify_main().unwrap();
    assert!(paths.main.exists());
    assert!(paths.coordination.exists());
}

#[test]
fn retirement_failure_preserves_published_and_exact_cleanup_facts() {
    let directory = TempDirectory::new();
    let (output, reservation, paths) = armed_attempt(&directory.path);
    let published = publish(output, reservation).unwrap();
    let failure = published
        .retire_with(|point| {
            if point == Point::ReservationUnlinked {
                Err(Error::Injected)
            } else {
                Ok(())
            }
        })
        .unwrap_err();

    assert!(failure.owner.reservation_unlinked);
    assert!(!failure.owner.directory_synced);
    assert!(matches!(failure.cause, Error::Injected));
    assert_eq!(
        failure
            .owner
            .published
            .reservation
            .file
            .metadata()
            .unwrap()
            .nlink(),
        0
    );
    assert!(!paths.coordination.exists());
    failure.owner.published.output.verify_main().unwrap();
}

#[test]
fn replacement_exchange_keeps_both_inodes_locked_until_retirement() {
    let directory = TempDirectory::new();
    let (output, reservation, paths) = armed_replacement_attempt(&directory.path);
    let output_identity = output.attempt.identity();
    let previous_identity = output.previous.as_ref().unwrap().identity;

    let published = publish(output, reservation).unwrap();

    assert_eq!(paths.main.metadata().unwrap().ino(), output_identity.inode);
    assert_eq!(
        paths.private_output.metadata().unwrap().ino(),
        previous_identity.inode
    );
    let previous_contender = read_write(&paths.private_output);
    assert!(
        !live_lock::try_lock(&previous_contender, MAIN_LIFETIME_LOCK, Mode::Exclusive).unwrap()
    );

    let completed = published.retire().unwrap();
    assert!(!paths.private_output.exists());
    assert!(!paths.coordination.exists());
    completed._output_guard.verify_main().unwrap();
}

#[test]
fn replacement_retirement_failure_records_exact_previous_phase() {
    let directory = TempDirectory::new();
    let (output, reservation, paths) = armed_replacement_attempt(&directory.path);
    let published = publish(output, reservation).unwrap();
    let failure = published
        .retire_with(|point| {
            if point == Point::PreviousUnlinked {
                Err(Error::Injected)
            } else {
                Ok(())
            }
        })
        .unwrap_err();

    assert!(failure.owner.previous_unlinked);
    assert!(!failure.owner.previous_retired_proven);
    assert!(!failure.owner.reservation_unlinked);
    assert!(!paths.private_output.exists());
    assert!(paths.coordination.exists());
    failure.owner.published.output.verify_main().unwrap();
}

fn armed_attempt(directory: &Path) -> (PreparedOutput, ArmedReservation, Paths) {
    let main = directory.join("result.v4");
    let secured = CreatedOutput::create(&main).unwrap().secure().unwrap();
    let (attempt, file) = secured.into_parts();
    let private_output = named_path(directory, attempt.name());
    let mut builder = Builder::new_owned(file, direct_spec(), output_budget()).unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(9), 3).unwrap();
    let output = attempt
        .prepare_cancellable(
            builder.finish_owned().unwrap(),
            &crate::CancellationToken::new(),
        )
        .unwrap();
    let reservation = ReservationDraft::create(&output)
        .unwrap()
        .initialize(&output)
        .unwrap()
        .acquire(&output)
        .unwrap()
        .arm(&output)
        .unwrap();
    let coordination = named_path(directory, output.attempt.destination().coordination());
    (
        output,
        reservation,
        Paths {
            main,
            private_output,
            coordination,
        },
    )
}

fn armed_replacement_attempt(directory: &Path) -> (PreparedOutput, ArmedReservation, Paths) {
    let main = directory.join("result.v4");
    let secured = CreatedOutput::create(&main).unwrap().secure().unwrap();
    let (attempt, file) = secured.into_parts();
    let private_output = named_path(directory, attempt.name());
    let mut builder = Builder::new_owned(file, direct_spec(), output_budget()).unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(9), 3).unwrap();
    let output = attempt
        .prepare_cancellable(
            builder.finish_owned().unwrap(),
            &crate::CancellationToken::new(),
        )
        .unwrap();
    fs::write(&main, b"previous bytes").unwrap();
    let output = replacement::bind(output, &crate::CancellationToken::new()).unwrap();
    let reservation = ReservationDraft::create(&output)
        .unwrap()
        .initialize(&output)
        .unwrap()
        .acquire(&output)
        .unwrap()
        .arm(&output)
        .unwrap();
    let coordination = named_path(directory, output.attempt.destination().coordination());
    (
        output,
        reservation,
        Paths {
            main,
            private_output,
            coordination,
        },
    )
}

fn named_path(directory: &Path, name: &Name) -> PathBuf {
    directory.join(OsStr::from_bytes(name.bytes()))
}

fn read_write(path: &Path) -> std::fs::File {
    OpenOptions::new()
        .read(true)
        .write(true)
        .open(path)
        .unwrap()
}

fn direct_spec() -> OutputSpec {
    OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::RETENTION,
        database_id: [21; 16],
        transaction_id: 22,
        commit_nonce: [23; 16],
        feed_index_limit: 0,
    }
}

fn output_budget() -> OutputBudget {
    OutputBudget {
        max_output_pages: 100_000,
    }
}

struct Paths {
    main: PathBuf,
    private_output: PathBuf,
    coordination: PathBuf,
}

struct TempDirectory {
    path: PathBuf,
}

impl TempDirectory {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-main-publication-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
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
