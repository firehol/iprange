use super::*;
use crate::bootstrap::tests::empty_direct_meta;
use crate::contract::PAGE_SIZE;

fn encoded(meta: MetaV4) -> RecoveryMetaState {
    let mut page = [0; PAGE_SIZE];
    meta.encode_into(&mut page);
    crate::bootstrap::classify_recovery_meta(&page)
}

fn identity() -> LocalFileIdentity {
    LocalFileIdentity {
        kind: 1,
        bytes: [7; 32],
    }
}

#[test]
fn equal_creation_metas_expose_one_deterministic_newest_candidate() {
    let meta = empty_direct_meta(1);
    let classified = ClassifiedMetas::new([Some(encoded(meta)), Some(encoded(meta))]);
    assert_eq!(
        classified.order,
        GenerationOrder::Proven {
            current: 1,
            previous: None,
        }
    );
    let candidates = classified.candidates(identity());
    let candidate = candidates[0].unwrap();
    assert_eq!(candidate.label, RecoveryCandidateLabel::Newest);
    assert_eq!(candidate.meta_page, 1);
    assert!(candidates[1].is_none());
}

#[test]
fn adjacent_metas_expose_newest_then_previous() {
    let old = empty_direct_meta(1);
    let mut new = old;
    new.txn_id = 2;
    new.commit_nonce = [3; 16];
    let classified = ClassifiedMetas::new([Some(encoded(new)), Some(encoded(old))]);
    assert_eq!(
        classified.order,
        GenerationOrder::Proven {
            current: 0,
            previous: Some(1),
        }
    );
    let candidates = classified.candidates(identity());
    assert_eq!(
        candidates.map(|candidate| candidate.map(|candidate| candidate.label)),
        [
            Some(RecoveryCandidateLabel::Newest),
            Some(RecoveryCandidateLabel::Previous),
        ]
    );
}

#[test]
fn swapped_adjacent_metas_are_unordered_not_current() {
    let old = empty_direct_meta(1);
    let mut new = old;
    new.txn_id = 2;
    new.commit_nonce = [3; 16];
    let classified = ClassifiedMetas::new([Some(encoded(old)), Some(encoded(new))]);
    assert_eq!(classified.order, GenerationOrder::Unproven);
    let candidates = classified.candidates(identity());
    assert_eq!(
        candidates.map(|candidate| candidate.map(|candidate| candidate.label)),
        [
            Some(RecoveryCandidateLabel::UnorderedMeta0),
            Some(RecoveryCandidateLabel::UnorderedMeta1),
        ]
    );
    assert_eq!(
        classified
            .progress()
            .unwrap()
            .findings_for(ValidationReason::MetaInvalid),
        1
    );
}

#[test]
fn unreadable_current_does_not_promote_the_previous_meta() {
    let old = empty_direct_meta(1);
    let mut current = old;
    current.txn_id = 2;
    current.commit_nonce = [3; 16];
    current.range_record_count = 1;
    let classified = ClassifiedMetas::new([Some(encoded(current)), Some(encoded(old))]);
    assert_eq!(
        classified.order,
        GenerationOrder::Proven {
            current: 0,
            previous: Some(1),
        }
    );
    assert!(classified.current_recovery_meta().is_none());
    let candidates = classified.candidates(identity());
    assert_eq!(
        candidates[0].unwrap().label,
        RecoveryCandidateLabel::Previous
    );
    assert!(candidates[1].is_none());
}
