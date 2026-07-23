//! Private transaction resource and cleanup ownership contracts.
//!
//! This module owns no filesystem resources and performs no cleanup from
//! destructors. Callers provide all obligation storage up front.

use crate::error::ErrorCode;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PrivateWriterResourceBudget {
    max_heap_bytes: u64,
    max_private_pages: u64,
    max_file_growth_pages: u64,
    max_open_files: u32,
}

impl PrivateWriterResourceBudget {
    pub(crate) const fn new(
        max_heap_bytes: u64,
        max_private_pages: u64,
        max_file_growth_pages: u64,
        max_open_files: u32,
    ) -> Self {
        Self {
            max_heap_bytes,
            max_private_pages,
            max_file_growth_pages,
            max_open_files,
        }
    }

    pub(crate) const fn max_heap_bytes(self) -> u64 {
        self.max_heap_bytes
    }

    pub(crate) const fn max_private_pages(self) -> u64 {
        self.max_private_pages
    }

    pub(crate) const fn max_file_growth_pages(self) -> u64 {
        self.max_file_growth_pages
    }

    pub(crate) const fn max_open_files(self) -> u32 {
        self.max_open_files
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct PrivateWriterResourceDelta {
    heap_bytes: u64,
    private_pages: u64,
    file_growth_pages: u64,
    open_files: u32,
}

impl PrivateWriterResourceDelta {
    pub(crate) const fn new(
        heap_bytes: u64,
        private_pages: u64,
        file_growth_pages: u64,
        open_files: u32,
    ) -> Self {
        Self {
            heap_bytes,
            private_pages,
            file_growth_pages,
            open_files,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateWriterResource {
    HeapBytes,
    PrivatePages,
    FileGrowthPages,
    OpenFiles,
    CleanupObligations,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateWriterContractError {
    ArithmeticOverflow(PrivateWriterResource),
    InsufficientResourceBudget {
        resource: PrivateWriterResource,
        required: u64,
        actual: u64,
    },
    ResourceUsageUnderflow {
        resource: PrivateWriterResource,
        used: u64,
        release: u64,
    },
    GrowthExceedsPrivatePages {
        growth: u64,
        private_pages: u64,
    },
    CleanupStorageNotEmpty {
        index: usize,
    },
    CleanupStorageCorrupt {
        index: usize,
    },
    CleanupExecutorMissing,
    CleanupInterruptedAttemptUnrecorded {
        index: usize,
    },
    CleanupInterruptedAttemptMismatch {
        expected: Option<usize>,
        actual: usize,
    },
    InvalidCoordinationCleanupShape,
    CleanupGuardUnavailable,
}

impl PrivateWriterContractError {
    pub(crate) const fn code(self) -> ErrorCode {
        match self {
            Self::ArithmeticOverflow(_) => ErrorCode::ArithmeticOverflow,
            Self::InsufficientResourceBudget { .. } => ErrorCode::InsufficientResourceBudget,
            Self::ResourceUsageUnderflow { .. }
            | Self::GrowthExceedsPrivatePages { .. }
            | Self::CleanupStorageNotEmpty { .. }
            | Self::CleanupStorageCorrupt { .. }
            | Self::CleanupExecutorMissing
            | Self::CleanupInterruptedAttemptUnrecorded { .. }
            | Self::CleanupInterruptedAttemptMismatch { .. }
            | Self::InvalidCoordinationCleanupShape
            | Self::CleanupGuardUnavailable => ErrorCode::WrongState,
        }
    }
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PrivateWriterResourceUsage {
    budget: PrivateWriterResourceBudget,
    heap_bytes: u64,
    private_pages: u64,
    file_growth_pages: u64,
    open_files: u32,
}

impl PrivateWriterResourceUsage {
    pub(crate) const fn new(budget: PrivateWriterResourceBudget) -> Self {
        Self {
            budget,
            heap_bytes: 0,
            private_pages: 0,
            file_growth_pages: 0,
            open_files: 0,
        }
    }

    pub(crate) const fn budget(&self) -> PrivateWriterResourceBudget {
        self.budget
    }

    pub(crate) const fn current(&self) -> PrivateWriterResourceDelta {
        PrivateWriterResourceDelta {
            heap_bytes: self.heap_bytes,
            private_pages: self.private_pages,
            file_growth_pages: self.file_growth_pages,
            open_files: self.open_files,
        }
    }

    pub(crate) fn acquire(
        &mut self,
        delta: PrivateWriterResourceDelta,
    ) -> Result<(), PrivateWriterContractError> {
        let next_heap = checked_add(
            self.heap_bytes,
            delta.heap_bytes,
            PrivateWriterResource::HeapBytes,
        )?;
        let next_private = checked_add(
            self.private_pages,
            delta.private_pages,
            PrivateWriterResource::PrivatePages,
        )?;
        let next_growth = checked_add(
            self.file_growth_pages,
            delta.file_growth_pages,
            PrivateWriterResource::FileGrowthPages,
        )?;
        let next_open = self.open_files.checked_add(delta.open_files).ok_or(
            PrivateWriterContractError::ArithmeticOverflow(PrivateWriterResource::OpenFiles),
        )?;

        if next_growth > next_private {
            return Err(PrivateWriterContractError::GrowthExceedsPrivatePages {
                growth: next_growth,
                private_pages: next_private,
            });
        }
        require_within(
            PrivateWriterResource::HeapBytes,
            next_heap,
            self.budget.max_heap_bytes,
        )?;
        require_within(
            PrivateWriterResource::PrivatePages,
            next_private,
            self.budget.max_private_pages,
        )?;
        require_within(
            PrivateWriterResource::FileGrowthPages,
            next_growth,
            self.budget.max_file_growth_pages,
        )?;
        require_within(
            PrivateWriterResource::OpenFiles,
            u64::from(next_open),
            u64::from(self.budget.max_open_files),
        )?;

        self.heap_bytes = next_heap;
        self.private_pages = next_private;
        self.file_growth_pages = next_growth;
        self.open_files = next_open;
        Ok(())
    }

    pub(crate) fn release(
        &mut self,
        delta: PrivateWriterResourceDelta,
    ) -> Result<(), PrivateWriterContractError> {
        let next_heap = checked_sub(
            self.heap_bytes,
            delta.heap_bytes,
            PrivateWriterResource::HeapBytes,
        )?;
        let next_private = checked_sub(
            self.private_pages,
            delta.private_pages,
            PrivateWriterResource::PrivatePages,
        )?;
        let next_growth = checked_sub(
            self.file_growth_pages,
            delta.file_growth_pages,
            PrivateWriterResource::FileGrowthPages,
        )?;
        let next_open = self.open_files.checked_sub(delta.open_files).ok_or(
            PrivateWriterContractError::ResourceUsageUnderflow {
                resource: PrivateWriterResource::OpenFiles,
                used: u64::from(self.open_files),
                release: u64::from(delta.open_files),
            },
        )?;

        if next_growth > next_private {
            return Err(PrivateWriterContractError::GrowthExceedsPrivatePages {
                growth: next_growth,
                private_pages: next_private,
            });
        }

        self.heap_bytes = next_heap;
        self.private_pages = next_private;
        self.file_growth_pages = next_growth;
        self.open_files = next_open;
        Ok(())
    }
}

fn checked_add(
    current: u64,
    delta: u64,
    resource: PrivateWriterResource,
) -> Result<u64, PrivateWriterContractError> {
    current
        .checked_add(delta)
        .ok_or(PrivateWriterContractError::ArithmeticOverflow(resource))
}

fn checked_sub(
    current: u64,
    delta: u64,
    resource: PrivateWriterResource,
) -> Result<u64, PrivateWriterContractError> {
    current
        .checked_sub(delta)
        .ok_or(PrivateWriterContractError::ResourceUsageUnderflow {
            resource,
            used: current,
            release: delta,
        })
}

fn require_within(
    resource: PrivateWriterResource,
    required: u64,
    actual: u64,
) -> Result<(), PrivateWriterContractError> {
    if required > actual {
        return Err(PrivateWriterContractError::InsufficientResourceBudget {
            resource,
            required,
            actual,
        });
    }
    Ok(())
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateCleanupState {
    Clean,
    ResiduePossible,
}

#[derive(Debug)]
pub(crate) struct PrivateCleanupRetryReport<'error, E> {
    attempted: usize,
    cleaned: usize,
    remaining: usize,
    compaction_scans: usize,
    first_cause: Option<&'error E>,
}

impl<E> PrivateCleanupRetryReport<'_, E> {
    pub(crate) const fn attempted(&self) -> usize {
        self.attempted
    }

    pub(crate) const fn cleaned(&self) -> usize {
        self.cleaned
    }

    pub(crate) const fn remaining(&self) -> usize {
        self.remaining
    }

    pub(crate) const fn compaction_scans(&self) -> usize {
        self.compaction_scans
    }

    pub(crate) const fn first_cause(&self) -> Option<&E> {
        self.first_cause
    }
}

#[derive(Debug)]
pub(crate) struct PrivateCleanupEntry<I, O, E> {
    obligation: I,
    owner: O,
    last_error: Option<E>,
    proven_clean: bool,
    attempting: bool,
}

#[derive(Debug)]
pub(crate) struct FixedCleanupLedger<'storage, I, O, E> {
    entries: &'storage mut [Option<PrivateCleanupEntry<I, O, E>>],
    len: usize,
    has_proven_clean: bool,
}

impl<'storage, I, O, E> FixedCleanupLedger<'storage, I, O, E> {
    pub(crate) fn new(
        entries: &'storage mut [Option<PrivateCleanupEntry<I, O, E>>],
    ) -> Result<Self, PrivateWriterContractError> {
        if let Some(index) = entries.iter().position(Option::is_some) {
            return Err(PrivateWriterContractError::CleanupStorageNotEmpty { index });
        }
        Ok(Self {
            entries,
            len: 0,
            has_proven_clean: false,
        })
    }

    pub(crate) const fn len(&self) -> usize {
        self.len
    }

    pub(crate) const fn capacity(&self) -> usize {
        self.entries.len()
    }

    pub(crate) const fn is_empty(&self) -> bool {
        self.len == 0
    }

    pub(crate) fn obligation(&self, index: usize) -> Option<&I> {
        self.entries
            .get(index)
            .and_then(Option::as_ref)
            .map(|entry| &entry.obligation)
    }

    pub(crate) fn owner(&self, index: usize) -> Option<&O> {
        self.entries
            .get(index)
            .and_then(Option::as_ref)
            .map(|entry| &entry.owner)
    }

    pub(crate) fn last_error(&self, index: usize) -> Option<&E> {
        self.entries
            .get(index)
            .and_then(Option::as_ref)
            .and_then(|entry| entry.last_error.as_ref())
    }

    pub(crate) fn is_proven_clean(&self, index: usize) -> Option<bool> {
        self.entries
            .get(index)
            .and_then(Option::as_ref)
            .map(|entry| entry.proven_clean)
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn append(
        &mut self,
        obligation: I,
        owner: O,
    ) -> Result<(), (I, O, PrivateWriterContractError)> {
        if self.len == self.entries.len() {
            let Some(required) = self.len.checked_add(1) else {
                return Err((
                    obligation,
                    owner,
                    PrivateWriterContractError::ArithmeticOverflow(
                        PrivateWriterResource::CleanupObligations,
                    ),
                ));
            };
            return Err((
                obligation,
                owner,
                PrivateWriterContractError::InsufficientResourceBudget {
                    resource: PrivateWriterResource::CleanupObligations,
                    required: u64::try_from(required).unwrap_or(u64::MAX),
                    actual: u64::try_from(self.entries.len()).unwrap_or(u64::MAX),
                },
            ));
        }
        if self.entries[self.len].is_some() {
            return Err((
                obligation,
                owner,
                PrivateWriterContractError::CleanupStorageCorrupt { index: self.len },
            ));
        }
        self.entries[self.len] = Some(PrivateCleanupEntry {
            obligation,
            owner,
            last_error: None,
            proven_clean: false,
            attempting: false,
        });
        self.len += 1;
        Ok(())
    }

    pub(crate) fn retry_all<'ledger, F>(
        &'ledger mut self,
        executor: Option<&mut F>,
    ) -> Result<PrivateCleanupRetryReport<'ledger, E>, PrivateWriterContractError>
    where
        F: FnMut(&I, &mut O) -> Result<(), E>,
    {
        if let Some(index) = self.interrupted_attempt_index()? {
            return Err(PrivateWriterContractError::CleanupInterruptedAttemptUnrecorded { index });
        }
        let Some(executor) = executor else {
            return Err(PrivateWriterContractError::CleanupExecutorMissing);
        };
        if let Some(index) = self.entries[..self.len].iter().position(Option::is_none) {
            return Err(PrivateWriterContractError::CleanupStorageCorrupt { index });
        }

        let (mut cleaned, mut compaction_scans) = self.compact_proven()?;
        let mut attempted = 0usize;
        for index in 0..self.len {
            let outcome = {
                let Some(entry) = self.entries[index].as_mut() else {
                    return Err(PrivateWriterContractError::CleanupStorageCorrupt { index });
                };
                entry.attempting = true;
                attempted += 1;
                executor(&entry.obligation, &mut entry.owner)
            };
            let entry = self.entries[index]
                .as_mut()
                .ok_or(PrivateWriterContractError::CleanupStorageCorrupt { index })?;
            entry.attempting = false;
            match outcome {
                Ok(()) => {
                    let entry = self.entries[index]
                        .as_mut()
                        .ok_or(PrivateWriterContractError::CleanupStorageCorrupt { index })?;
                    entry.proven_clean = true;
                    self.has_proven_clean = true;
                    let previous = entry.last_error.take();
                    drop(previous);
                }
                Err(cause) => {
                    let entry = self.entries[index]
                        .as_mut()
                        .ok_or(PrivateWriterContractError::CleanupStorageCorrupt { index })?;
                    let previous = entry.last_error.replace(cause);
                    drop(previous);
                }
            }
        }
        let (newly_cleaned, final_scans) = self.compact_proven()?;
        cleaned += newly_cleaned;
        compaction_scans += final_scans;
        let first_cause = self.entries[..self.len]
            .iter()
            .find_map(|entry| entry.as_ref().and_then(|entry| entry.last_error.as_ref()));
        Ok(PrivateCleanupRetryReport {
            attempted,
            cleaned,
            remaining: self.len,
            compaction_scans,
            first_cause,
        })
    }

    pub(crate) fn interrupted_attempt_index(
        &self,
    ) -> Result<Option<usize>, PrivateWriterContractError> {
        let mut interrupted = None;
        for (index, entry) in self.entries[..self.len].iter().enumerate() {
            let entry = entry
                .as_ref()
                .ok_or(PrivateWriterContractError::CleanupStorageCorrupt { index })?;
            if entry.attempting {
                if interrupted.is_some() {
                    return Err(PrivateWriterContractError::CleanupStorageCorrupt { index });
                }
                interrupted = Some(index);
            }
        }
        Ok(interrupted)
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn record_interrupted_error(
        &mut self,
        index: usize,
        cause: E,
    ) -> Result<(), (E, PrivateWriterContractError)> {
        let expected = match self.interrupted_attempt_index() {
            Ok(expected) => expected,
            Err(error) => return Err((cause, error)),
        };
        if expected != Some(index) {
            return Err((
                cause,
                PrivateWriterContractError::CleanupInterruptedAttemptMismatch {
                    expected,
                    actual: index,
                },
            ));
        }
        let Some(entry) = self.entries[index].as_mut() else {
            return Err((
                cause,
                PrivateWriterContractError::CleanupStorageCorrupt { index },
            ));
        };
        let previous = entry.last_error.replace(cause);
        entry.attempting = false;
        drop(previous);
        Ok(())
    }

    fn compact_proven(&mut self) -> Result<(usize, usize), PrivateWriterContractError> {
        if !self.has_proven_clean {
            return Ok((0, 0));
        }
        if let Some(index) = self.entries[..self.len].iter().position(Option::is_none) {
            return Err(PrivateWriterContractError::CleanupStorageCorrupt { index });
        }

        let original_len = self.len;
        let mut retained = 0usize;
        let mut cleaned = 0usize;
        for index in 0..original_len {
            let proven_clean = self.entries[index]
                .as_ref()
                .ok_or(PrivateWriterContractError::CleanupStorageCorrupt { index })?
                .proven_clean;
            if self.entries[index]
                .as_ref()
                .ok_or(PrivateWriterContractError::CleanupStorageCorrupt { index })?
                .attempting
            {
                return Err(
                    PrivateWriterContractError::CleanupInterruptedAttemptUnrecorded { index },
                );
            }
            if proven_clean {
                let cleaned_entry = self.entries[index].take();
                cleaned += 1;
                drop(cleaned_entry);
            } else {
                if retained != index {
                    self.entries[retained] = self.entries[index].take();
                }
                retained += 1;
            }
        }
        self.len = retained;
        self.has_proven_clean = false;
        Ok((cleaned, original_len))
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PrivateCoordinationCleanup {
    None,
    CleanupGuard,
    RetainedReaderCloseRequired,
    RetainedWriterCloseRequired,
}

impl PrivateCoordinationCleanup {
    pub(crate) const fn is_none(self) -> bool {
        matches!(self, Self::None)
    }
}

#[derive(Debug)]
pub(crate) struct PrivateTerminalCleanup<'storage, I, O, E, G> {
    ledger: FixedCleanupLedger<'storage, I, O, E>,
    coordination: PrivateCoordinationCleanup,
    cleanup_guard: Option<G>,
}

impl<'storage, I, O, E, G> PrivateTerminalCleanup<'storage, I, O, E, G> {
    #[allow(clippy::result_large_err, clippy::type_complexity)]
    pub(crate) fn new(
        ledger: FixedCleanupLedger<'storage, I, O, E>,
        coordination: PrivateCoordinationCleanup,
        cleanup_guard: Option<G>,
    ) -> Result<
        Self,
        (
            FixedCleanupLedger<'storage, I, O, E>,
            Option<G>,
            PrivateWriterContractError,
        ),
    > {
        let valid_shape = matches!(
            (coordination, cleanup_guard.is_some()),
            (PrivateCoordinationCleanup::CleanupGuard, true)
                | (
                    PrivateCoordinationCleanup::None
                        | PrivateCoordinationCleanup::RetainedReaderCloseRequired
                        | PrivateCoordinationCleanup::RetainedWriterCloseRequired,
                    false
                )
        );
        if !valid_shape {
            return Err((
                ledger,
                cleanup_guard,
                PrivateWriterContractError::InvalidCoordinationCleanupShape,
            ));
        }
        Ok(Self {
            ledger,
            coordination,
            cleanup_guard,
        })
    }

    pub(crate) fn cleanup_state(&self) -> PrivateCleanupState {
        if self.ledger.is_empty() && self.coordination.is_none() {
            PrivateCleanupState::Clean
        } else {
            PrivateCleanupState::ResiduePossible
        }
    }

    pub(crate) const fn ledger(&self) -> &FixedCleanupLedger<'storage, I, O, E> {
        &self.ledger
    }

    pub(crate) fn ledger_mut(&mut self) -> &mut FixedCleanupLedger<'storage, I, O, E> {
        &mut self.ledger
    }

    pub(crate) const fn coordination(&self) -> PrivateCoordinationCleanup {
        self.coordination
    }

    pub(crate) fn take_cleanup_guard(&mut self) -> Result<G, PrivateWriterContractError> {
        if self.coordination != PrivateCoordinationCleanup::CleanupGuard {
            return Err(PrivateWriterContractError::CleanupGuardUnavailable);
        }
        self.cleanup_guard
            .take()
            .ok_or(PrivateWriterContractError::CleanupGuardUnavailable)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_alloc::count_thread_allocations;
    use core::cell::Cell;

    const fn budget() -> PrivateWriterResourceBudget {
        PrivateWriterResourceBudget::new(10, 4, 3, 2)
    }

    #[test]
    fn immutable_budget_and_exact_limit_acquire_release() {
        let limits = budget();
        assert_eq!(limits.max_heap_bytes(), 10);
        assert_eq!(limits.max_private_pages(), 4);
        assert_eq!(limits.max_file_growth_pages(), 3);
        assert_eq!(limits.max_open_files(), 2);

        let exact = PrivateWriterResourceDelta::new(10, 4, 3, 2);
        let mut usage = PrivateWriterResourceUsage::new(limits);
        assert_eq!(usage.budget(), limits);
        usage.acquire(exact).unwrap();
        assert_eq!(usage.current(), exact);
        usage.release(exact).unwrap();
        assert_eq!(usage.current(), PrivateWriterResourceDelta::default());
    }

    #[test]
    fn released_simultaneous_capacity_can_be_reacquired_exactly() {
        let mut usage = PrivateWriterResourceUsage::new(budget());
        usage
            .acquire(PrivateWriterResourceDelta::new(6, 3, 2, 2))
            .unwrap();
        usage
            .release(PrivateWriterResourceDelta::new(2, 1, 1, 1))
            .unwrap();
        assert_eq!(usage.current(), PrivateWriterResourceDelta::new(4, 2, 1, 1));

        usage
            .acquire(PrivateWriterResourceDelta::new(6, 2, 2, 1))
            .unwrap();
        assert_eq!(
            usage.current(),
            PrivateWriterResourceDelta::new(10, 4, 3, 2)
        );
    }

    #[test]
    fn every_one_over_budget_failure_is_atomic() {
        let cases = [
            (
                PrivateWriterResourceDelta::new(11, 0, 0, 0),
                PrivateWriterResource::HeapBytes,
            ),
            (
                PrivateWriterResourceDelta::new(0, 5, 0, 0),
                PrivateWriterResource::PrivatePages,
            ),
            (
                PrivateWriterResourceDelta::new(0, 4, 4, 0),
                PrivateWriterResource::FileGrowthPages,
            ),
            (
                PrivateWriterResourceDelta::new(0, 0, 0, 3),
                PrivateWriterResource::OpenFiles,
            ),
        ];
        for (delta, expected_resource) in cases {
            let mut usage = PrivateWriterResourceUsage::new(budget());
            let before = usage.current();
            let error = usage.acquire(delta).unwrap_err();
            assert!(matches!(
                error,
                PrivateWriterContractError::InsufficientResourceBudget {
                    resource,
                    ..
                } if resource == expected_resource
            ));
            assert_eq!(usage.current(), before);
            assert_eq!(error.code(), ErrorCode::InsufficientResourceBudget);
        }
    }

    #[test]
    fn arithmetic_overflow_and_underflow_are_exact_and_atomic() {
        let maximum = PrivateWriterResourceBudget::new(u64::MAX, u64::MAX, u64::MAX, u32::MAX);
        let overflow_cases = [
            (
                PrivateWriterResourceDelta::new(u64::MAX, 0, 0, 0),
                PrivateWriterResourceDelta::new(1, 0, 0, 0),
                PrivateWriterResource::HeapBytes,
            ),
            (
                PrivateWriterResourceDelta::new(0, u64::MAX, 0, 0),
                PrivateWriterResourceDelta::new(0, 1, 0, 0),
                PrivateWriterResource::PrivatePages,
            ),
            (
                PrivateWriterResourceDelta::new(0, u64::MAX, u64::MAX, 0),
                PrivateWriterResourceDelta::new(0, 0, 1, 0),
                PrivateWriterResource::FileGrowthPages,
            ),
            (
                PrivateWriterResourceDelta::new(0, 0, 0, u32::MAX),
                PrivateWriterResourceDelta::new(0, 0, 0, 1),
                PrivateWriterResource::OpenFiles,
            ),
        ];
        for (initial, overflow, resource) in overflow_cases {
            let mut usage = PrivateWriterResourceUsage::new(maximum);
            usage.acquire(initial).unwrap();
            let before = usage.current();
            let error = usage.acquire(overflow).unwrap_err();
            assert_eq!(
                error,
                PrivateWriterContractError::ArithmeticOverflow(resource)
            );
            assert_eq!(usage.current(), before);
            assert_eq!(error.code(), ErrorCode::ArithmeticOverflow);
        }

        let underflow_cases = [
            (
                PrivateWriterResourceDelta::new(1, 0, 0, 0),
                PrivateWriterResource::HeapBytes,
            ),
            (
                PrivateWriterResourceDelta::new(0, 1, 0, 0),
                PrivateWriterResource::PrivatePages,
            ),
            (
                PrivateWriterResourceDelta::new(0, 0, 1, 0),
                PrivateWriterResource::FileGrowthPages,
            ),
            (
                PrivateWriterResourceDelta::new(0, 0, 0, 1),
                PrivateWriterResource::OpenFiles,
            ),
        ];
        for (release, resource) in underflow_cases {
            let mut usage = PrivateWriterResourceUsage::new(budget());
            let before = usage.current();
            let error = usage.release(release).unwrap_err();
            assert_eq!(
                error,
                PrivateWriterContractError::ResourceUsageUnderflow {
                    resource,
                    used: 0,
                    release: 1,
                }
            );
            assert_eq!(usage.current(), before);
            assert_eq!(error.code(), ErrorCode::WrongState);
        }
    }

    #[test]
    fn growth_never_exceeds_private_pages_on_acquire_or_release() {
        let mut usage = PrivateWriterResourceUsage::new(budget());
        let before = usage.current();
        assert_eq!(
            usage
                .acquire(PrivateWriterResourceDelta::new(0, 0, 1, 0))
                .unwrap_err(),
            PrivateWriterContractError::GrowthExceedsPrivatePages {
                growth: 1,
                private_pages: 0,
            }
        );
        assert_eq!(usage.current(), before);

        usage
            .acquire(PrivateWriterResourceDelta::new(0, 2, 2, 0))
            .unwrap();
        let before = usage.current();
        assert_eq!(
            usage
                .release(PrivateWriterResourceDelta::new(0, 1, 0, 0))
                .unwrap_err(),
            PrivateWriterContractError::GrowthExceedsPrivatePages {
                growth: 2,
                private_pages: 1,
            }
        );
        assert_eq!(usage.current(), before);
    }

    #[test]
    fn multidimension_failure_changes_no_counter() {
        let mut usage = PrivateWriterResourceUsage::new(budget());
        usage
            .acquire(PrivateWriterResourceDelta::new(2, 2, 1, 1))
            .unwrap();
        let before = usage.current();
        assert!(usage
            .acquire(PrivateWriterResourceDelta::new(1, 1, 1, 2))
            .is_err());
        assert_eq!(usage.current(), before);
        assert!(usage
            .release(PrivateWriterResourceDelta::new(1, 1, 2, 1))
            .is_err());
        assert_eq!(usage.current(), before);
    }

    #[derive(Debug, PartialEq, Eq)]
    struct Cause(u8);

    #[derive(Debug, PartialEq, Eq)]
    struct Identity {
        id: u8,
    }

    #[derive(Debug)]
    struct Owner<'a> {
        attempts: &'a Cell<usize>,
        drops: &'a Cell<usize>,
    }

    impl Drop for Owner<'_> {
        fn drop(&mut self) {
            self.drops.set(self.drops.get() + 1);
        }
    }

    #[test]
    fn cleanup_retries_every_obligation_compacts_stably_and_keeps_first_cause() {
        let attempts = Cell::new(0);
        let drops = Cell::new(0);
        let mut storage = [None, None, None];
        let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
        for id in 1..=3 {
            ledger
                .append(
                    Identity { id },
                    Owner {
                        attempts: &attempts,
                        drops: &drops,
                    },
                )
                .unwrap();
        }

        let mut first_pass = |identity: &Identity, owner: &mut Owner<'_>| {
            owner.attempts.set(owner.attempts.get() + 1);
            if identity.id == 1 || identity.id == 3 {
                Err(Cause(identity.id))
            } else {
                Ok(())
            }
        };
        {
            let report = ledger.retry_all(Some(&mut first_pass)).unwrap();
            assert_eq!(report.attempted(), 3);
            assert_eq!(report.cleaned(), 1);
            assert_eq!(report.remaining(), 2);
            assert_eq!(report.first_cause(), Some(&Cause(1)));
        }
        assert_eq!(attempts.get(), 3);
        assert_eq!(drops.get(), 1);
        assert_eq!(ledger.obligation(0), Some(&Identity { id: 1 }));
        assert_eq!(ledger.obligation(1), Some(&Identity { id: 3 }));
        assert_eq!(ledger.last_error(0), Some(&Cause(1)));
        assert_eq!(ledger.last_error(1), Some(&Cause(3)));

        let mut second_pass = |identity: &Identity, owner: &mut Owner<'_>| {
            owner.attempts.set(owner.attempts.get() + 1);
            if identity.id == 3 {
                Err(Cause(3))
            } else {
                Ok(())
            }
        };
        {
            let report = ledger.retry_all(Some(&mut second_pass)).unwrap();
            assert_eq!(report.attempted(), 2);
            assert_eq!(report.cleaned(), 1);
            assert_eq!(report.remaining(), 1);
            assert_eq!(report.first_cause(), Some(&Cause(3)));
        }
        assert_eq!(attempts.get(), 5);
        assert_eq!(drops.get(), 2);
        assert_eq!(ledger.obligation(0), Some(&Identity { id: 3 }));
        assert_eq!(ledger.last_error(0), Some(&Cause(3)));

        let mut third_pass = |_identity: &Identity, owner: &mut Owner<'_>| {
            owner.attempts.set(owner.attempts.get() + 1);
            Ok::<(), Cause>(())
        };
        {
            let report = ledger.retry_all(Some(&mut third_pass)).unwrap();
            assert_eq!(report.first_cause(), None);
        }
        assert!(ledger.is_empty());
        assert_eq!(attempts.get(), 6);
        assert_eq!(drops.get(), 3);
    }

    #[test]
    fn executor_can_mutate_only_the_owner_not_the_obligation_identity() {
        let attempts = Cell::new(0);
        let drops = Cell::new(0);
        let mut storage = [None];
        let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
        ledger
            .append(
                Identity { id: 41 },
                Owner {
                    attempts: &attempts,
                    drops: &drops,
                },
            )
            .unwrap();

        fn execute(identity: &Identity, owner: &mut Owner<'_>) -> Result<(), Cause> {
            // The shared identity reference makes an attempted `identity.id = ...`
            // mutation a compile-time error; only retry ownership is mutable.
            assert_eq!(identity.id, 41);
            owner.attempts.set(owner.attempts.get() + 1);
            Err(Cause(identity.id))
        }
        {
            let report = ledger.retry_all(Some(&mut execute)).unwrap();
            assert_eq!(report.first_cause(), Some(&Cause(41)));
        }
        assert_eq!(ledger.obligation(0), Some(&Identity { id: 41 }));
        assert_eq!(ledger.last_error(0), Some(&Cause(41)));
        assert_eq!(attempts.get(), 1);
    }

    #[test]
    fn executor_unwind_at_every_position_preserves_contiguous_retry_authority() {
        use std::panic::{catch_unwind, AssertUnwindSafe};

        for panic_at in 1..=3 {
            let attempts = Cell::new(0);
            let drops = Cell::new(0);
            let mut storage = [None, None, None];
            {
                let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
                for id in 1..=3 {
                    ledger
                        .append(
                            Identity { id },
                            Owner {
                                attempts: &attempts,
                                drops: &drops,
                            },
                        )
                        .unwrap();
                }

                let mut call = 0usize;
                let outcome = catch_unwind(AssertUnwindSafe(|| {
                    let mut executor = |identity: &Identity, owner: &mut Owner<'_>| {
                        call += 1;
                        owner.attempts.set(owner.attempts.get() + 1);
                        if call == panic_at {
                            panic!("injected cleanup executor unwind");
                        }
                        if identity.id == 2 {
                            Err(Cause(2))
                        } else {
                            Ok(())
                        }
                    };
                    ledger.retry_all(Some(&mut executor)).map(|_| ())
                }));
                assert!(outcome.is_err());

                let expected_ids = [1, 2, 3];
                assert_eq!(ledger.len(), expected_ids.len());
                for (index, expected_id) in expected_ids.iter().copied().enumerate() {
                    assert_eq!(ledger.obligation(index).unwrap().id, expected_id);
                    assert!(ledger.owner(index).is_some());
                }
                assert_eq!(ledger.is_proven_clean(0), Some(panic_at >= 2));
                assert_eq!(ledger.is_proven_clean(1), Some(false));
                assert_eq!(ledger.is_proven_clean(2), Some(false));
                let interrupted = panic_at - 1;
                assert_eq!(
                    ledger.interrupted_attempt_index().unwrap(),
                    Some(interrupted)
                );
                let retry_calls = Cell::new(0usize);
                let mut forbidden_retry = |_identity: &Identity, _owner: &mut Owner<'_>| {
                    retry_calls.set(retry_calls.get() + 1);
                    Ok::<(), Cause>(())
                };
                assert_eq!(
                    ledger.retry_all(Some(&mut forbidden_retry)).unwrap_err(),
                    PrivateWriterContractError::CleanupInterruptedAttemptUnrecorded {
                        index: interrupted,
                    }
                );
                assert_eq!(retry_calls.get(), 0);

                let wrong_index = (interrupted + 1) % 3;
                let returned = ledger
                    .record_interrupted_error(wrong_index, Cause(200 + panic_at as u8))
                    .unwrap_err();
                assert_eq!(returned.0, Cause(200 + panic_at as u8));
                assert!(matches!(
                    returned.1,
                    PrivateWriterContractError::CleanupInterruptedAttemptMismatch {
                        expected: Some(index),
                        actual,
                    } if index == interrupted && actual == wrong_index
                ));
                assert_eq!(
                    ledger.interrupted_attempt_index().unwrap(),
                    Some(interrupted)
                );

                let interrupted_cause = Cause(100 + panic_at as u8);
                ledger
                    .record_interrupted_error(interrupted, interrupted_cause)
                    .unwrap();
                assert_eq!(ledger.interrupted_attempt_index().unwrap(), None);
                assert_eq!(
                    ledger.last_error(interrupted),
                    Some(&Cause(100 + panic_at as u8))
                );
                if panic_at == 3 {
                    assert_eq!(ledger.last_error(1), Some(&Cause(2)));
                    assert_eq!(ledger.last_error(0), None);
                } else {
                    for index in 0..ledger.len() {
                        if index != interrupted {
                            assert_eq!(ledger.last_error(index), None);
                        }
                    }
                }

                let mut finish = |_identity: &Identity, owner: &mut Owner<'_>| {
                    owner.attempts.set(owner.attempts.get() + 1);
                    Ok::<(), Cause>(())
                };
                {
                    let report = ledger.retry_all(Some(&mut finish)).unwrap();
                    assert_eq!(report.attempted(), if panic_at == 1 { 3 } else { 2 });
                    assert_eq!(report.cleaned(), 3);
                    assert_eq!(report.first_cause(), None);
                }
                assert!(ledger.is_empty());
            }
            drop(storage);
            assert_eq!(drops.get(), 3);
        }
    }

    #[test]
    fn completed_cleanup_pass_has_exact_linear_callback_and_compaction_work() {
        fn run<const N: usize>() {
            let calls = Cell::new(0usize);
            let mut storage: [Option<PrivateCleanupEntry<usize, usize, u8>>; N] =
                core::array::from_fn(|_| None);
            let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
            for id in 0..N {
                ledger.append(id, id).unwrap();
            }

            let mut executor = |_identity: &usize, _owner: &mut usize| {
                calls.set(calls.get() + 1);
                Ok::<(), u8>(())
            };
            let report = ledger.retry_all(Some(&mut executor)).unwrap();
            assert_eq!(report.attempted(), N);
            assert_eq!(report.cleaned(), N);
            assert_eq!(report.compaction_scans(), N);
            assert_eq!(calls.get(), N);
            assert_eq!(report.remaining(), 0);
        }

        run::<1>();
        run::<64>();
        run::<1024>();
    }

    #[test]
    fn missing_executor_and_full_ledger_never_lose_authority() {
        let attempts = Cell::new(0);
        let drops = Cell::new(0);
        let mut storage = [None];
        {
            let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
            ledger
                .append(
                    Identity { id: 1 },
                    Owner {
                        attempts: &attempts,
                        drops: &drops,
                    },
                )
                .unwrap();
            let error = ledger
                .retry_all::<fn(&Identity, &mut Owner<'_>) -> Result<(), Cause>>(None)
                .unwrap_err();
            assert_eq!(error, PrivateWriterContractError::CleanupExecutorMissing);
            assert_eq!(error.code(), ErrorCode::WrongState);
            assert_eq!(ledger.len(), 1);
            assert_eq!(drops.get(), 0);

            let returned = ledger
                .append(
                    Identity { id: 2 },
                    Owner {
                        attempts: &attempts,
                        drops: &drops,
                    },
                )
                .unwrap_err();
            assert_eq!(returned.0.id, 2);
            assert_eq!(returned.2.code(), ErrorCode::InsufficientResourceBudget);
            drop(returned.1);
            assert_eq!(drops.get(), 1);
        }
        assert_eq!(drops.get(), 1);
        drop(storage);
        assert_eq!(drops.get(), 2);
    }

    #[derive(Debug)]
    struct Guard<'a> {
        cleanup_calls: &'a Cell<usize>,
        drops: &'a Cell<usize>,
    }

    impl Guard<'_> {
        fn cleanup(&mut self) {
            self.cleanup_calls.set(self.cleanup_calls.get() + 1);
        }
    }

    impl Drop for Guard<'_> {
        fn drop(&mut self) {
            self.drops.set(self.drops.get() + 1);
        }
    }

    #[test]
    fn cleanup_state_is_derived_and_guard_is_take_once() {
        let cleanup_calls = Cell::new(0);
        let drops = Cell::new(0);
        let mut storage: [Option<PrivateCleanupEntry<u8, u8, u8>>; 1] = [None];
        let ledger = FixedCleanupLedger::new(&mut storage).unwrap();
        let mut terminal = PrivateTerminalCleanup::new(
            ledger,
            PrivateCoordinationCleanup::CleanupGuard,
            Some(Guard {
                cleanup_calls: &cleanup_calls,
                drops: &drops,
            }),
        )
        .unwrap();
        assert_eq!(
            terminal.cleanup_state(),
            PrivateCleanupState::ResiduePossible
        );
        assert_eq!(
            terminal.coordination(),
            PrivateCoordinationCleanup::CleanupGuard
        );
        assert_eq!(terminal.ledger().capacity(), 1);
        assert_eq!(terminal.ledger_mut().len(), 0);

        let mut guard = terminal.take_cleanup_guard().unwrap();
        assert_eq!(
            terminal.cleanup_state(),
            PrivateCleanupState::ResiduePossible
        );
        assert_eq!(
            terminal.coordination(),
            PrivateCoordinationCleanup::CleanupGuard
        );
        assert_eq!(
            terminal.take_cleanup_guard().unwrap_err(),
            PrivateWriterContractError::CleanupGuardUnavailable
        );
        guard.cleanup();
        drop(guard);
        assert_eq!(cleanup_calls.get(), 1);
        assert_eq!(drops.get(), 1);
    }

    #[test]
    fn retained_coordination_and_nonempty_ledger_are_residue() {
        let mut storage: [Option<PrivateCleanupEntry<u8, u8, u8>>; 1] = [None];
        let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
        ledger.append(7u8, 9u8).unwrap();
        let terminal = PrivateTerminalCleanup::new(
            ledger,
            PrivateCoordinationCleanup::RetainedWriterCloseRequired,
            None::<()>,
        )
        .unwrap();
        assert_eq!(
            terminal.cleanup_state(),
            PrivateCleanupState::ResiduePossible
        );

        let mut empty: [Option<PrivateCleanupEntry<u8, u8, u8>>; 0] = [];
        let ledger = FixedCleanupLedger::new(&mut empty).unwrap();
        let terminal = PrivateTerminalCleanup::new(
            ledger,
            PrivateCoordinationCleanup::RetainedReaderCloseRequired,
            None::<()>,
        )
        .unwrap();
        assert_eq!(
            terminal.cleanup_state(),
            PrivateCleanupState::ResiduePossible
        );

        let mut empty: [Option<PrivateCleanupEntry<u8, u8, u8>>; 0] = [];
        let ledger = FixedCleanupLedger::new(&mut empty).unwrap();
        let terminal =
            PrivateTerminalCleanup::new(ledger, PrivateCoordinationCleanup::None, None::<()>)
                .unwrap();
        assert_eq!(terminal.cleanup_state(), PrivateCleanupState::Clean);
    }

    #[test]
    fn dropping_guard_owner_never_starts_cleanup() {
        let cleanup_calls = Cell::new(0);
        let drops = Cell::new(0);
        let mut storage: [Option<PrivateCleanupEntry<u8, u8, u8>>; 0] = [];
        let ledger = FixedCleanupLedger::new(&mut storage).unwrap();
        let terminal = PrivateTerminalCleanup::new(
            ledger,
            PrivateCoordinationCleanup::CleanupGuard,
            Some(Guard {
                cleanup_calls: &cleanup_calls,
                drops: &drops,
            }),
        )
        .unwrap();
        drop(terminal);
        assert_eq!(cleanup_calls.get(), 0);
        assert_eq!(drops.get(), 1);
    }

    #[test]
    fn invalid_coordination_shape_returns_every_consumed_authority() {
        let attempts = Cell::new(0);
        let obligation_drops = Cell::new(0);
        let guard_cleanup_calls = Cell::new(0);
        let guard_drops = Cell::new(0);
        let mut storage: [Option<PrivateCleanupEntry<Identity, Owner<'_>, Cause>>; 1] = [None];
        let guard = {
            let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
            ledger
                .append(
                    Identity { id: 7 },
                    Owner {
                        attempts: &attempts,
                        drops: &obligation_drops,
                    },
                )
                .unwrap();
            let (ledger, guard, error) = PrivateTerminalCleanup::new(
                ledger,
                PrivateCoordinationCleanup::None,
                Some(Guard {
                    cleanup_calls: &guard_cleanup_calls,
                    drops: &guard_drops,
                }),
            )
            .unwrap_err();

            assert_eq!(
                error,
                PrivateWriterContractError::InvalidCoordinationCleanupShape
            );
            assert_eq!(error.code(), ErrorCode::WrongState);
            assert_eq!(ledger.len(), 1);
            assert_eq!(ledger.obligation(0), Some(&Identity { id: 7 }));
            assert_eq!(obligation_drops.get(), 0);
            assert_eq!(guard_cleanup_calls.get(), 0);
            assert_eq!(guard_drops.get(), 0);
            guard
        };
        assert_eq!(obligation_drops.get(), 0);

        let mut guard = guard.unwrap();
        guard.cleanup();
        drop(guard);
        assert_eq!(guard_cleanup_calls.get(), 1);
        assert_eq!(guard_drops.get(), 1);
        drop(storage);
        assert_eq!(obligation_drops.get(), 1);
    }

    #[test]
    fn warmed_contract_paths_allocate_nothing() {
        let (_, allocations) = count_thread_allocations(|| {
            let limits = PrivateWriterResourceBudget::new(64, 4, 4, 2);
            let mut usage = PrivateWriterResourceUsage::new(limits);
            usage
                .acquire(PrivateWriterResourceDelta::new(32, 2, 1, 1))
                .unwrap();
            usage
                .release(PrivateWriterResourceDelta::new(16, 1, 0, 1))
                .unwrap();
            assert!(matches!(
                usage
                    .acquire(PrivateWriterResourceDelta::new(49, 0, 0, 0))
                    .unwrap_err(),
                PrivateWriterContractError::InsufficientResourceBudget {
                    resource: PrivateWriterResource::HeapBytes,
                    ..
                }
            ));
            assert!(matches!(
                usage
                    .release(PrivateWriterResourceDelta::new(17, 0, 0, 0))
                    .unwrap_err(),
                PrivateWriterContractError::ResourceUsageUnderflow {
                    resource: PrivateWriterResource::HeapBytes,
                    ..
                }
            ));
            assert!(matches!(
                usage
                    .release(PrivateWriterResourceDelta::new(0, 1, 0, 0))
                    .unwrap_err(),
                PrivateWriterContractError::GrowthExceedsPrivatePages { .. }
            ));

            {
                let mut storage = [None, None];
                let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
                ledger.append(1usize, 11usize).unwrap();
                ledger.append(2usize, 12usize).unwrap();
                let returned = ledger.append(3usize, 13usize).unwrap_err();
                assert_eq!(returned.0, 3);
                assert_eq!(returned.1, 13);
                assert_eq!(returned.2.code(), ErrorCode::InsufficientResourceBudget);
                assert_eq!(
                    ledger
                        .retry_all::<fn(&usize, &mut usize) -> Result<(), u8>>(None)
                        .unwrap_err(),
                    PrivateWriterContractError::CleanupExecutorMissing
                );
                let mut fail =
                    |identity: &usize, _owner: &mut usize| Err(u8::try_from(*identity).unwrap());
                {
                    let report = ledger.retry_all(Some(&mut fail)).unwrap();
                    assert_eq!(report.first_cause(), Some(&1));
                    assert_eq!(report.compaction_scans(), 0);
                }
                assert_eq!(ledger.last_error(0), Some(&1));
                assert_eq!(ledger.last_error(1), Some(&2));

                let mut executor = |_identity: &usize, _owner: &mut usize| Ok::<(), u8>(());
                {
                    let report = ledger.retry_all(Some(&mut executor)).unwrap();
                    assert_eq!(report.cleaned(), 2);
                    assert_eq!(report.compaction_scans(), 2);
                }

                let mut terminal = PrivateTerminalCleanup::new(
                    ledger,
                    PrivateCoordinationCleanup::CleanupGuard,
                    Some(9u8),
                )
                .unwrap();
                assert_eq!(
                    terminal.cleanup_state(),
                    PrivateCleanupState::ResiduePossible
                );
                assert_eq!(terminal.take_cleanup_guard().unwrap(), 9);
                assert_eq!(
                    terminal.take_cleanup_guard().unwrap_err(),
                    PrivateWriterContractError::CleanupGuardUnavailable
                );
            }

            {
                let mut storage: [Option<PrivateCleanupEntry<usize, usize, u8>>; 0] = [];
                let ledger = FixedCleanupLedger::new(&mut storage).unwrap();
                let (returned_ledger, returned_guard, error) = PrivateTerminalCleanup::new(
                    ledger,
                    PrivateCoordinationCleanup::None,
                    Some(7u8),
                )
                .unwrap_err();
                assert!(returned_ledger.is_empty());
                assert_eq!(returned_guard, Some(7));
                assert_eq!(
                    error,
                    PrivateWriterContractError::InvalidCoordinationCleanupShape
                );
            }

            {
                let mut storage = [None];
                let mut ledger = FixedCleanupLedger::new(&mut storage).unwrap();
                ledger.append(5usize, 15usize).unwrap();
                ledger.entries[0].as_mut().unwrap().attempting = true;
                assert_eq!(
                    ledger
                        .retry_all::<fn(&usize, &mut usize) -> Result<(), u8>>(None)
                        .unwrap_err(),
                    PrivateWriterContractError::CleanupInterruptedAttemptUnrecorded { index: 0 }
                );
                let returned = ledger.record_interrupted_error(1, 8u8).unwrap_err();
                assert_eq!(returned.0, 8);
                ledger.record_interrupted_error(0, 9u8).unwrap();
                assert_eq!(ledger.last_error(0), Some(&9));
            }
        });
        assert_eq!(allocations, 0);
    }
}
