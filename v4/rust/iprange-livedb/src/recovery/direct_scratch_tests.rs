use std::fs::{self, File};

use super::super::direct_build::construct;
use super::super::{RecoveryBudget, RecoverySinkControl, RecoveryUnknownEnvelope};
use super::tests::{
    finish_ranges, output_builder, rewrite_second_start, source_builder, swap_first_two_records,
    Paths,
};
use crate::validation::ValidationReason;
use crate::{CancellationToken, Error};

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
        max_heap_bytes: 256,
        max_output_pages: 20_000,
        max_open_files: 4,
        max_scratch_bytes: 4096,
        max_scratch_files: 2,
        scratch_directory: Some(paths.scratch.clone()),
    };
    let mut saw_order_damage = false;

    let failure = construct(
        &File::open(&paths.source).unwrap(),
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
