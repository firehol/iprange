use std::fs;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::error::ErrorCode;
use crate::immutable_output::{Builder, OutputBudget, OutputSpec};
use crate::key::Ipv4Key;
use crate::publication::attempt;
use crate::publication::crash_tests::{run_child, run_replacement_child, Artifacts, TempDirectory};
use crate::publication::output::CreatedOutput;
use crate::publication::replacement;
use crate::publication::reservation;
use crate::publication::result::{
    AccessPolicy, CleanupArtifacts, DestinationContent, LaterCanonical, PublicationStatus,
};
use crate::publication::{CleanupState, Housekeeping};
use crate::validation::{self, ValidationBudget, ValidationMode, ValidationSinkControl};
use crate::ImmutableReader;

use super::*;

const PRE_MAIN: &[&str] = &[
    "publication.after_reservation_state1_sync",
    "publication.after_reservation_rename",
    "publication.after_reservation_directory_sync",
    "publication.after_reservation_state2_write",
    "publication.after_reservation_state2_sync",
    "publication.after_reservation_state2_selection",
];

const POST_MAIN: &[&str] = &[
    "publication.after_main_rename",
    "publication.after_main_sync",
    "publication.after_main_directory_sync",
    "publication.after_main_proof",
];

const REPLACEMENT_POST_MAIN: &[&str] = &[
    "publication.after_main_rename",
    "publication.after_main_sync",
    "publication.after_main_directory_sync",
    "publication.after_main_proof",
    "publication.after_previous_unlink",
];

#[test]
fn complete_resumes_every_pre_main_crash_state() {
    for point in PRE_MAIN {
        let directory = TempDirectory::new(point);
        let main = directory.path.join("result.v4");
        run_child(&main, point);

        let result = resolve(&main, None, Mode::Complete, &CancellationToken::new()).unwrap();

        assert_published(&result, point);
        assert_clean(&directory, &main, point);
        assert_eq!(
            ImmutableReader::open(&main).unwrap().info().transaction_id,
            42
        );
    }
}

#[test]
fn remove_discards_every_pre_main_crash_state() {
    for point in PRE_MAIN {
        let directory = TempDirectory::new(point);
        let main = directory.path.join("result.v4");
        run_child(&main, point);

        let result = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap();

        assert_eq!(
            result.publication,
            PublicationStatus::NotPublished,
            "{point}"
        );
        assert_eq!(
            result.destination_content,
            DestinationContent::Absent,
            "{point}"
        );
        assert_eq!(result.cleanup_state(), CleanupState::Clean, "{point}");
        assert!(!main.exists(), "{point}");
        assert_clean(&directory, &main, point);
    }
}

#[test]
fn replacement_complete_resumes_every_pre_main_crash_state() {
    for point in PRE_MAIN {
        let directory = TempDirectory::new(point);
        let main = directory.path.join("result.v4");
        run_replacement_child(&main, point);

        let result = resolve(&main, None, Mode::Complete, &CancellationToken::new()).unwrap();

        assert_published(&result, point);
        assert!(result.attempt.previous_destination.is_some(), "{point}");
        assert_clean(&directory, &main, point);
    }
}

#[test]
fn replacement_remove_preserves_previous_for_every_pre_main_crash_state() {
    for point in PRE_MAIN {
        let directory = TempDirectory::new(point);
        let main = directory.path.join("result.v4");
        run_replacement_child(&main, point);

        let result = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap();

        assert_eq!(
            result.publication,
            PublicationStatus::NotPublished,
            "{point}"
        );
        assert_eq!(
            result.destination_content,
            DestinationContent::Previous,
            "{point}"
        );
        assert_eq!(fs::read(&main).unwrap(), b"previous bytes", "{point}");
        assert_clean(&directory, &main, point);
    }
}

#[test]
fn replacement_both_modes_finish_every_post_exchange_crash_state() {
    for point in REPLACEMENT_POST_MAIN {
        for mode in [Mode::Complete, Mode::Remove] {
            let directory = TempDirectory::new(point);
            let main = directory.path.join("result.v4");
            run_replacement_child(&main, point);

            let result = resolve(&main, None, mode, &CancellationToken::new()).unwrap();

            assert_published(&result, point);
            assert!(result.attempt.previous_destination.is_some(), "{point}");
            assert_clean(&directory, &main, point);
        }
    }
}

#[test]
fn complete_restores_a_private_state2_reservation_before_publication() {
    let directory = TempDirectory::new("private-state2");
    let main = directory.path.join("result.v4");
    run_child(&main, "publication.after_reservation_state2_selection");
    let artifacts = Artifacts::inspect(&directory.path, &main);
    let bytes = fs::read(&artifacts.coordination).unwrap();
    let header = reservation::select(bytes.as_slice()).unwrap().header;
    let private = directory
        .path
        .join(private_reservation_name(header.attempt_id));
    fs::rename(&artifacts.coordination, &private).unwrap();

    let result = resolve(&main, None, Mode::Complete, &CancellationToken::new()).unwrap();

    assert_published(&result, "private-state2");
    assert_clean(&directory, &main, "private-state2");
}

#[test]
fn both_modes_finish_cleanup_after_every_main_crash_state() {
    for point in POST_MAIN {
        for mode in [Mode::Complete, Mode::Remove] {
            let directory = TempDirectory::new(point);
            let main = directory.path.join("result.v4");
            run_child(&main, point);

            let result = resolve(&main, None, mode, &CancellationToken::new()).unwrap();

            assert_published(&result, point);
            assert_clean(&directory, &main, point);
            assert_eq!(
                ImmutableReader::open(&main).unwrap().info().transaction_id,
                42
            );
        }
    }
}

#[test]
fn missing_output_makes_complete_unresolvable_without_cleaning_reservation() {
    let directory = TempDirectory::new("missing-output");
    let main = directory.path.join("result.v4");
    run_child(&main, PRE_MAIN[0]);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    fs::remove_file(&artifacts.private_outputs[0]).unwrap();

    let problem = resolve(&main, None, Mode::Complete, &CancellationToken::new()).unwrap_err();

    assert_eq!(problem.code, ErrorCode::Unresolvable);
    assert_eq!(
        Artifacts::inspect(&directory.path, &main)
            .private_reservations
            .len(),
        1
    );
}

#[test]
fn desired_bytes_remain_published_when_main_access_changes() {
    use std::os::unix::fs::PermissionsExt;

    let directory = TempDirectory::new("changed-main-access");
    let main = directory.path.join("result.v4");
    run_child(&main, POST_MAIN[0]);
    fs::set_permissions(&main, fs::Permissions::from_mode(0o644)).unwrap();

    let result = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap();

    assert_published(&result, "changed-main-access");
    assert_eq!(result.main_access_policy, AccessPolicy::ChangedOrUnproven);
    assert_eq!(
        fs::metadata(&main).unwrap().permissions().mode() & 0o7777,
        0o644
    );
}

#[test]
fn supplied_result_resolves_after_reservation_retirement() {
    for mode in [Mode::Complete, Mode::Remove] {
        let directory = TempDirectory::new("supplied-result");
        let main = directory.path.join("result.v4");
        let original = publish(&main, [41; 16], 42);

        let result = resolve(&main, Some(&original), mode, &CancellationToken::new()).unwrap();

        assert_published(&result, "supplied-result");
        assert_clean(&directory, &main, "supplied-result");
    }
}

#[test]
fn supplied_replacement_result_resolves_after_reservation_retirement() {
    for mode in [Mode::Complete, Mode::Remove] {
        let directory = TempDirectory::new("supplied-replacement-result");
        let main = directory.path.join("result.v4");
        fs::write(&main, b"previous bytes").unwrap();
        let original = publish_replacement(&main);

        let result = resolve(&main, Some(&original), mode, &CancellationToken::new()).unwrap();

        assert_published(&result, "supplied-replacement-result");
        assert!(result.attempt.previous_destination.is_some());
        assert_clean(&directory, &main, "supplied-replacement-result");
    }
}

#[test]
fn supplied_result_is_rejected_before_inspection_for_another_path() {
    let first = TempDirectory::new("result-binding-first");
    let first_main = first.path.join("result.v4");
    let result = publish(&first_main, [41; 16], 42);

    let same_directory = first.path.join("other.v4");
    let problem = resolve(
        &same_directory,
        Some(&result),
        Mode::Remove,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(problem.code, ErrorCode::DestinationNameMismatch);

    let second = TempDirectory::new("result-binding-second");
    let second_main = second.path.join("result.v4");
    let problem = resolve(
        &second_main,
        Some(&result),
        Mode::Remove,
        &CancellationToken::new(),
    )
    .unwrap_err();
    assert_eq!(problem.code, ErrorCode::DirectoryIdentityMismatch);
    assert!(!same_directory.exists());
    assert!(!second_main.exists());
}

#[test]
fn malformed_exact_private_reservation_from_result_is_never_removed_online() {
    let directory = TempDirectory::new("malformed-result-reservation");
    let main = directory.path.join("result.v4");
    let result = publish(&main, [41; 16], 42);
    let name = private_reservation_name(result.attempt.publication_attempt_id);
    let reservation = directory.path.join(name);
    fs::write(&reservation, [0; 8192]).unwrap();

    let problem = resolve(
        &main,
        Some(&result),
        Mode::Remove,
        &CancellationToken::new(),
    )
    .unwrap_err();

    assert_eq!(problem.code, ErrorCode::Unresolvable);
    assert_eq!(fs::read(&reservation).unwrap(), [0; 8192]);
    assert!(main.exists());
}

#[test]
fn valid_later_reservation_is_retained_when_old_desired_main_is_proven() {
    let directory = TempDirectory::new("later-reservation");
    let main = directory.path.join("result.v4");
    let original = publish(&main, [41; 16], 42);
    run_child(&main, "publication.after_reservation_directory_sync");

    let result = resolve(
        &main,
        Some(&original),
        Mode::Remove,
        &CancellationToken::new(),
    )
    .unwrap();

    assert_eq!(result.publication, PublicationStatus::Published);
    assert_eq!(result.destination_content, DestinationContent::Desired);
    assert_eq!(
        result.later_canonical,
        LaterCanonical::ReservationOrTransition
    );
    assert_eq!(result.coordination_access_policy, AccessPolicy::CreatorOnly);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    assert!(artifacts.coordination.exists());
    assert_eq!(artifacts.private_outputs.len(), 1);
}

#[test]
fn different_private_attempt_is_a_conflict_not_a_later_owner() {
    let directory = TempDirectory::new("different-private-attempt");
    let main = directory.path.join("result.v4");
    let original = publish(&main, [41; 16], 42);
    let expected = fs::read(&main).unwrap();
    run_child(&main, PRE_MAIN[0]);

    let problem = resolve(
        &main,
        Some(&original),
        Mode::Remove,
        &CancellationToken::new(),
    )
    .unwrap_err();

    assert_eq!(problem.code, ErrorCode::Conflict);
    assert_eq!(fs::read(&main).unwrap(), expected);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    assert_eq!(artifacts.private_outputs.len(), 1);
    assert_eq!(artifacts.private_reservations.len(), 1);
    assert!(!artifacts.coordination.exists());
}

#[test]
fn canonical_reuse_during_cleanup_is_reclassified_before_return() {
    let directory = TempDirectory::new("cleanup-canonical-reuse");
    let main = directory.path.join("result.v4");
    run_child(&main, POST_MAIN[0]);
    let destination = Destination::bind(&main).unwrap();
    let original = reservation_inspection::discover(&destination, &CancellationToken::new())
        .unwrap()
        .unwrap();
    let original_header = original.header;
    fs::remove_file(directory.path.join("result.v4.readers")).unwrap();
    destination.directory().sync().unwrap();
    run_child(&main, "publication.after_reservation_directory_sync");
    let summary = cleanup::Summary {
        artifacts: CleanupArtifacts::new(),
        housekeeping: Housekeeping::None,
        visible_housekeeping: Vec::new(),
        main_absent: false,
        coordination_absent: false,
    };

    let problem = verify_no_later(&destination, Some(&original), &summary).unwrap_err();
    assert_eq!(problem.code, ErrorCode::Conflict);

    let later = final_later(
        &destination,
        original_header,
        Some(&original),
        None,
        &summary,
    )
    .unwrap()
    .unwrap();
    assert_eq!(later.location, Location::Canonical);
    assert_ne!(later.header.attempt_id, original_header.attempt_id);
}

#[test]
fn equivalent_desired_inode_satisfies_the_postcondition_and_cleans_old_attempt() {
    let directory = TempDirectory::new("equivalent-main");
    let main = directory.path.join("result.v4");
    run_child(&main, PRE_MAIN[0]);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    fs::copy(&artifacts.private_outputs[0], &main).unwrap();

    let result = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap();

    assert_published(&result, "equivalent-main");
    assert_clean(&directory, &main, "equivalent-main");
    assert!(main.exists());
}

#[test]
fn desired_main_stays_published_when_foreign_private_output_cannot_be_cleaned() {
    use std::io::{Seek, SeekFrom, Write};

    let directory = TempDirectory::new("foreign-private-output");
    let main = directory.path.join("result.v4");
    run_child(&main, PRE_MAIN[0]);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    fs::copy(&artifacts.private_outputs[0], &main).unwrap();
    let mut private = fs::OpenOptions::new()
        .write(true)
        .open(&artifacts.private_outputs[0])
        .unwrap();
    private.seek(SeekFrom::End(-1)).unwrap();
    private.write_all(&[0xff]).unwrap();
    private.sync_all().unwrap();

    let result = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap();

    assert_eq!(result.publication, PublicationStatus::Published);
    assert_eq!(result.destination_content, DestinationContent::Desired);
    assert_eq!(result.cleanup_state(), CleanupState::ResiduePossible);
    assert_eq!(result.cleanup.len(), 1);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    assert_eq!(artifacts.private_outputs.len(), 1);
    assert!(artifacts.private_reservations.is_empty());
}

#[test]
fn complete_never_overwrites_another_complete_main() {
    let directory = TempDirectory::new("other-main");
    let main = directory.path.join("result.v4");
    run_child(&main, PRE_MAIN[0]);
    let foreign = directory.path.join("foreign.v4");
    publish(&foreign, [51; 16], 52);
    fs::rename(&foreign, &main).unwrap();
    let expected = fs::read(&main).unwrap();

    let result = resolve(&main, None, Mode::Complete, &CancellationToken::new()).unwrap();

    assert_eq!(result.publication, PublicationStatus::NotPublished);
    assert_eq!(result.destination_content, DestinationContent::Other);
    assert_eq!(result.cleanup_state(), CleanupState::Clean);
    assert_eq!(fs::read(&main).unwrap(), expected);
    assert_clean(&directory, &main, "other-main");
}

#[test]
fn malformed_main_and_cancelled_resolution_change_nothing() {
    for cancelled in [false, true] {
        let directory = TempDirectory::new("closed-resolution");
        let main = directory.path.join("result.v4");
        run_child(&main, PRE_MAIN[0]);
        if !cancelled {
            fs::write(&main, b"not a v4 file").unwrap();
        }
        let cancellation = CancellationToken::new();
        if cancelled {
            cancellation.cancel();
        }

        let problem = resolve(&main, None, Mode::Remove, &cancellation).unwrap_err();

        assert_eq!(
            problem.code,
            if cancelled {
                ErrorCode::Cancelled
            } else {
                ErrorCode::Conflict
            }
        );
        let artifacts = Artifacts::inspect(&directory.path, &main);
        assert_eq!(artifacts.private_outputs.len(), 1);
        assert_eq!(artifacts.private_reservations.len(), 1);
    }
}

#[test]
fn contended_reservation_lock_wait_observes_cancellation() {
    use std::time::{Duration, Instant};

    let directory = TempDirectory::new("cancel-contended-lock");
    let main = directory.path.join("result.v4");
    run_child(&main, PRE_MAIN[0]);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    let held = fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open(&artifacts.private_reservations[0])
        .unwrap();
    crate::live_lock::lock_file(&held, 0, crate::live_lock::Mode::Exclusive).unwrap();
    let cancellation = CancellationToken::new();
    let canceller = cancellation.clone();
    let thread = std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(20));
        canceller.cancel();
    });
    let started = Instant::now();

    let problem = resolve(&main, None, Mode::Remove, &cancellation).unwrap_err();

    thread.join().unwrap();
    assert_eq!(problem.code, ErrorCode::Cancelled);
    assert!(started.elapsed() < Duration::from_secs(1));
    let after = Artifacts::inspect(&directory.path, &main);
    assert_eq!(after.private_outputs.len(), 1);
    assert_eq!(after.private_reservations.len(), 1);
}

#[test]
fn resolver_does_not_replace_explicit_structural_validation() {
    use std::io::{Seek, SeekFrom, Write};

    let directory = TempDirectory::new("no-implicit-validation");
    let main = directory.path.join("result.v4");
    run_child(&main, PRE_MAIN[0]);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    let output_path = &artifacts.private_outputs[0];
    let mut output = fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open(output_path)
        .unwrap();
    let byte_length = output.metadata().unwrap().len();
    assert!(byte_length > 8192);
    output.seek(SeekFrom::End(-1)).unwrap();
    output.write_all(&[0xa5]).unwrap();
    output.sync_all().unwrap();
    let mapping = crate::mapping::Mapping::read_only_view(&output, byte_length).unwrap();
    let digest = crate::publication::output::digest(&mapping, byte_length).unwrap();

    let mut record = fs::read(&artifacts.private_reservations[0]).unwrap();
    let mut header = reservation::select(record.as_slice()).unwrap().header;
    header.output_sha512 = digest;
    record.fill(0);
    let block: &mut [u8; 4096] = (&mut record[..4096]).try_into().unwrap();
    header.encode(block).unwrap();
    fs::write(&artifacts.private_reservations[0], record).unwrap();

    let result = resolve(&main, None, Mode::Complete, &CancellationToken::new()).unwrap();
    assert_published(&result, "no-implicit-validation");

    let budget = ValidationBudget::heap_only(64 * 1024 * 1024, 1);
    let mut sink = |_finding: &validation::ValidationFinding| Ok(ValidationSinkControl::Continue);
    let validation = validation::validate(
        &main,
        ValidationMode::ImmutableCurrent,
        &budget,
        &CancellationToken::new(),
        &mut sink,
    )
    .unwrap();
    assert!(!validation.valid);
}

#[test]
fn remove_can_finish_when_output_is_missing_or_access_changed() {
    use std::os::unix::fs::PermissionsExt;

    for changed_access in [false, true] {
        let directory = TempDirectory::new("remove-partial");
        let main = directory.path.join("result.v4");
        run_child(&main, PRE_MAIN[0]);
        let artifacts = Artifacts::inspect(&directory.path, &main);
        if changed_access {
            fs::set_permissions(
                &artifacts.private_outputs[0],
                fs::Permissions::from_mode(0o644),
            )
            .unwrap();
        } else {
            fs::remove_file(&artifacts.private_outputs[0]).unwrap();
        }

        let result = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap();

        assert_eq!(result.publication, PublicationStatus::NotPublished);
        assert_eq!(result.destination_content, DestinationContent::Absent);
        assert_clean(&directory, &main, "remove-partial");
    }
}

#[test]
fn symlink_replacing_an_exact_private_output_is_a_conflict() {
    use std::os::unix::fs::symlink;

    let directory = TempDirectory::new("private-output-symlink");
    let main = directory.path.join("result.v4");
    run_child(&main, PRE_MAIN[0]);
    let artifacts = Artifacts::inspect(&directory.path, &main);
    fs::remove_file(&artifacts.private_outputs[0]).unwrap();
    symlink(
        &artifacts.private_reservations[0],
        &artifacts.private_outputs[0],
    )
    .unwrap();

    let problem = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap_err();

    assert_eq!(problem.code, ErrorCode::Conflict);
    assert!(fs::symlink_metadata(&artifacts.private_outputs[0])
        .unwrap()
        .file_type()
        .is_symlink());
    assert!(artifacts.private_reservations[0].exists());
}

#[test]
fn resolution_without_a_result_or_reservation_is_unresolvable() {
    let directory = TempDirectory::new("no-authority");
    let main = directory.path.join("result.v4");
    let problem = resolve(&main, None, Mode::Remove, &CancellationToken::new()).unwrap_err();
    assert_eq!(problem.code, ErrorCode::Unresolvable);
    assert!(!main.exists());
}

fn assert_published(result: &PublicationResult, label: &str) {
    assert_eq!(result.publication, PublicationStatus::Published, "{label}");
    assert_eq!(
        result.destination_content,
        DestinationContent::Desired,
        "{label}"
    );
    assert_eq!(result.cleanup_state(), CleanupState::Clean, "{label}");
    assert_eq!(
        result.coordination_access_policy,
        AccessPolicy::Absent,
        "{label}"
    );
}

fn assert_clean(directory: &TempDirectory, main: &std::path::Path, label: &str) {
    let artifacts = Artifacts::inspect(&directory.path, main);
    assert!(artifacts.private_outputs.is_empty(), "{label}");
    assert!(artifacts.private_reservations.is_empty(), "{label}");
    assert!(!artifacts.coordination.exists(), "{label}");
}

fn publish(
    main: &std::path::Path,
    database_id: [u8; 16],
    transaction_id: u64,
) -> PublicationResult {
    let secured = CreatedOutput::create(main).unwrap().secure().unwrap();
    let (attempt, file) = secured.into_parts();
    let spec = OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::RETENTION,
        database_id,
        transaction_id,
        commit_nonce: [43; 16],
        feed_index_limit: 0,
    };
    let budget = OutputBudget {
        max_output_pages: 100_000,
    };
    let mut builder = Builder::new_owned(file, spec, budget).unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(9), 17).unwrap();
    let output = attempt
        .prepare_cancellable(builder.finish_owned().unwrap(), &CancellationToken::new())
        .unwrap();
    attempt::fail_if_exists_cancellable(output, &CancellationToken::new()).unwrap()
}

fn publish_replacement(main: &std::path::Path) -> PublicationResult {
    let secured = CreatedOutput::create(main).unwrap().secure().unwrap();
    let (attempt, file) = secured.into_parts();
    let spec = OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::RETENTION,
        database_id: [41; 16],
        transaction_id: 42,
        commit_nonce: [43; 16],
        feed_index_limit: 0,
    };
    let budget = OutputBudget {
        max_output_pages: 100_000,
    };
    let mut builder = Builder::new_owned(file, spec, budget).unwrap();
    builder.push_direct_v4(Ipv4Key(1), Ipv4Key(9), 17).unwrap();
    let output = attempt
        .prepare_cancellable(builder.finish_owned().unwrap(), &CancellationToken::new())
        .unwrap();
    let output = replacement::bind(output, &CancellationToken::new()).unwrap();
    attempt::replace_existing_cancellable(output, &CancellationToken::new()).unwrap()
}

fn private_reservation_name(attempt: [u8; 16]) -> String {
    let mut name = String::from(".iprange-reservation-");
    for byte in attempt {
        use std::fmt::Write;
        write!(&mut name, "{byte:02x}").unwrap();
    }
    name.push_str(".tmp");
    name
}
