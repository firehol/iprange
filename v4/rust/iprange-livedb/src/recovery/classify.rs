use crate::bootstrap::{MetaProblem, RecoveryMetaState};
use crate::contract::MetaV4;
use crate::error::Result;
use crate::validation::{LocalFileIdentity, ValidationProgress, ValidationReason};

use super::{RecoveryCandidate, RecoveryCandidateLabel};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum GenerationOrder {
    Proven { current: u8, previous: Option<u8> },
    Unproven,
}

pub(crate) struct ClassifiedMetas {
    states: [Option<RecoveryMetaState>; 2],
    pub(crate) order: GenerationOrder,
}

impl ClassifiedMetas {
    pub(crate) fn new(states: [Option<RecoveryMetaState>; 2]) -> Self {
        let order = classify_order(&states);
        Self { states, order }
    }

    pub(crate) fn current_recovery_meta(&self) -> Option<MetaV4> {
        let GenerationOrder::Proven { current, .. } = self.order else {
            return None;
        };
        self.states[usize::from(current)]?.recovery.ok()
    }

    pub(crate) fn candidates(&self, identity: LocalFileIdentity) -> [Option<RecoveryCandidate>; 2] {
        let mut candidates = [None, None];
        match self.order {
            GenerationOrder::Proven { current, previous } => {
                candidates[0] = self.candidate(identity, current, RecoveryCandidateLabel::Newest);
                if let Some(previous) = previous {
                    let slot = usize::from(candidates[0].is_some());
                    candidates[slot] =
                        self.candidate(identity, previous, RecoveryCandidateLabel::Previous);
                }
            }
            GenerationOrder::Unproven => {
                let mut slot = 0;
                for page in 0..2 {
                    let label = if page == 0 {
                        RecoveryCandidateLabel::UnorderedMeta0
                    } else {
                        RecoveryCandidateLabel::UnorderedMeta1
                    };
                    if let Some(candidate) = self.candidate(identity, page, label) {
                        candidates[slot] = Some(candidate);
                        slot += 1;
                    }
                }
            }
        }
        candidates
    }

    pub(crate) fn progress(&self) -> Result<ValidationProgress> {
        let mut progress = ValidationProgress::new();
        self.record_recovery_problems(&mut progress)?;
        self.record_order_problem(&mut progress)?;
        Ok(progress)
    }

    fn record_recovery_problems(&self, progress: &mut ValidationProgress) -> Result<()> {
        for state in self.states {
            match state {
                None => record_problem(progress, ValidationReason::IoError)?,
                Some(state) => {
                    if let Err(problem) = state.recovery {
                        record_problem(progress, reason(problem))?;
                    }
                }
            }
        }
        Ok(())
    }

    fn record_order_problem(&self, progress: &mut ValidationProgress) -> Result<()> {
        if self.order == GenerationOrder::Unproven
            && self.states.iter().all(|state| state.is_some())
            && self
                .states
                .iter()
                .all(|state| state.unwrap().recovery.is_ok())
        {
            record_problem(progress, ValidationReason::MetaInvalid)?;
        }
        Ok(())
    }

    pub(crate) fn token_matches(&self, token: &RecoveryCandidate) -> bool {
        self.candidates(token.source_identity)
            .iter()
            .flatten()
            .any(|candidate| candidate == token)
    }

    pub(crate) fn selected_meta(&self, token: &RecoveryCandidate) -> Option<MetaV4> {
        if !self.token_matches(token) {
            return None;
        }
        self.states[usize::from(token.meta_page)]?.recovery.ok()
    }

    fn candidate(
        &self,
        identity: LocalFileIdentity,
        page: u8,
        label: RecoveryCandidateLabel,
    ) -> Option<RecoveryCandidate> {
        let meta = self.states[usize::from(page)]?.recovery.ok()?;
        Some(RecoveryCandidate {
            label,
            meta_page: page,
            source_identity: identity,
            database_id: meta.database_id,
            transaction_id: meta.txn_id,
            commit_nonce: meta.commit_nonce,
        })
    }
}

fn classify_order(states: &[Option<RecoveryMetaState>; 2]) -> GenerationOrder {
    let (Some(state0), Some(state1)) = (states[0], states[1]) else {
        return GenerationOrder::Unproven;
    };
    let (Ok(meta0), Ok(meta1)) = (state0.order, state1.order) else {
        return GenerationOrder::Unproven;
    };
    if !meta0.static_identity_eq(&meta1) {
        return GenerationOrder::Unproven;
    }
    if meta0.txn_id == meta1.txn_id {
        return equal_order(meta0, meta1);
    }
    adjacent_order(meta0, meta1)
}

fn equal_order(meta0: MetaV4, meta1: MetaV4) -> GenerationOrder {
    if meta0 != meta1 {
        return GenerationOrder::Unproven;
    }
    GenerationOrder::Proven {
        current: (meta0.txn_id & 1) as u8,
        previous: None,
    }
}

fn adjacent_order(meta0: MetaV4, meta1: MetaV4) -> GenerationOrder {
    let (lower, higher, higher_page) = if meta0.txn_id < meta1.txn_id {
        (meta0, meta1, 1u8)
    } else {
        (meta1, meta0, 0u8)
    };
    if lower.txn_id.checked_add(1) != Some(higher.txn_id) {
        return GenerationOrder::Unproven;
    }
    if (higher.txn_id & 1) as u8 != higher_page {
        return GenerationOrder::Unproven;
    }
    GenerationOrder::Proven {
        current: higher_page,
        previous: Some(1 - higher_page),
    }
}

fn record_problem(progress: &mut ValidationProgress, reason: ValidationReason) -> Result<()> {
    progress.count_finding(reason)?;
    progress.mark_untraversable(true)
}

fn reason(problem: MetaProblem) -> ValidationReason {
    if problem == MetaProblem::Magic {
        ValidationReason::MetaUnavailable
    } else {
        ValidationReason::MetaInvalid
    }
}

#[cfg(test)]
#[path = "classify_tests.rs"]
mod tests;
