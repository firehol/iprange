use std::fs;

use super::super::direct_build::construct;
use super::super::{RecoveryBudget, RecoverySinkControl, RecoveryUnknownEnvelope};
use super::tests::{
    finish_ranges, output_builder, output_ranges, rewrite_second_start, source_builder,
    source_mapping, swap_first_two_records, Paths,
};
use crate::validation::ValidationReason;
use crate::{CancellationToken, Error};

#[test]
fn ordered_recovery_uses_one_file_backed_page_table() {
    let paths = Paths::new("page-table");
    let ranges = [(0, 9, 1), (20, 29, 2), (40, 49, 3)];
    let meta = finish_ranges(source_builder(&paths.source), &ranges);
    let budget = RecoveryBudget {
        max_heap_bytes: 32,
        max_output_pages: 20_000,
        max_open_files: 3,
        max_scratch_bytes: 16 * 1024,
        max_scratch_files: 1,
        scratch_directory: Some(paths.scratch.clone()),
    };

    let source = source_mapping(&paths.source);
    let result = construct(
        &source,
        meta,
        output_builder(&paths.output),
        &budget,
        &CancellationToken::new(),
        &mut |_: &RecoveryUnknownEnvelope| Ok(RecoverySinkControl::Continue),
    )
    .unwrap();
    drop(result.finished.file);

    assert!(result.scratch.as_ref().is_some_and(|value| value.clean()));
    assert_eq!(output_ranges(&paths.output), ranges);
    assert_eq!(fs::read_dir(&paths.scratch).unwrap().count(), 0);
}

#[test]
fn sink_stop_during_external_output_cleans_every_scratch_file() {
    let paths = Paths::new("external-stop");
    let ranges: Vec<_> = (0..120u32)
        .map(|index| {
            let from = index * 3;
            (from, from + 1, index % 7)
        })
        .collect();
    let meta = finish_ranges(source_builder(&paths.source), &ranges);
    rewrite_second_start(&paths.source, meta, 1);
    swap_first_two_records(&paths.source, meta);
    let budget = RecoveryBudget {
        max_heap_bytes: 32,
        max_output_pages: 20_000,
        max_open_files: 4,
        max_scratch_bytes: 16 * 1024,
        max_scratch_files: 2,
        scratch_directory: Some(paths.scratch.clone()),
    };
    let mut saw_order_damage = false;

    let source = source_mapping(&paths.source);
    let failure = construct(
        &source,
        meta,
        output_builder(&paths.output),
        &budget,
        &CancellationToken::new(),
        &mut |envelope: &RecoveryUnknownEnvelope| {
            if envelope.reason == ValidationReason::TreeOrderInvalid {
                saw_order_damage = true;
                Ok(RecoverySinkControl::Continue)
            } else if envelope.reason == ValidationReason::RangeOverlap {
                Ok(RecoverySinkControl::Stop)
            } else {
                Ok(RecoverySinkControl::Continue)
            }
        },
    )
    .unwrap_err();

    assert!(saw_order_damage);
    assert!(matches!(failure.cause, Error::StoppedBySink));
    assert!(failure.scratch.as_ref().is_some_and(|value| value.clean()));
    assert_eq!(fs::read_dir(&paths.scratch).unwrap().count(), 0);
    drop(failure.builder.into_file());
}
