use std::path::PathBuf;

use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::publication::{CleanupArtifacts, CleanupState, CoordinationCleanup};
use crate::recovery::RecoverySourceCleanupGuard;

/// Explicit validation source and coordination mode.
#[derive(Clone, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum ValidationMode {
    ImmutableCurrent,
    LiveCurrent,
    OfflineCandidate(crate::recovery::RecoveryCandidate),
}

/// Maximum resources retained by one validation operation.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ValidationBudget {
    pub max_heap_bytes: u64,
    pub max_open_files: u32,
    pub max_scratch_bytes: u64,
    pub max_scratch_files: u32,
    pub scratch_directory: Option<PathBuf>,
}

impl ValidationBudget {
    /// A validation budget which forbids external scratch files.
    pub const fn heap_only(max_heap_bytes: u64, max_open_files: u32) -> Self {
        Self {
            max_heap_bytes,
            max_open_files,
            max_scratch_bytes: 0,
            max_scratch_files: 0,
            scratch_directory: None,
        }
    }

    pub(crate) fn validate(&self) -> Result<()> {
        if self.max_open_files == 0 {
            return Err(Error::BudgetExceeded(
                "validation requires at least one open file",
            ));
        }
        let scratch_enabled = self.max_scratch_bytes != 0 || self.max_scratch_files != 0;
        if scratch_enabled != self.scratch_directory.is_some() {
            return Err(Error::InvalidArgument(
                "scratch directory and scratch limits must be supplied together",
            ));
        }
        if self.max_scratch_bytes == 0 && self.max_scratch_files != 0
            || self.max_scratch_bytes != 0 && self.max_scratch_files == 0
        {
            return Err(Error::InvalidArgument(
                "both scratch byte and file limits must be nonzero",
            ));
        }
        Ok(())
    }
}

/// Stable validation defect classes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum ValidationReason {
    MetaUnavailable,
    MetaInvalid,
    MetaStaticMismatch,
    FileGeometryInvalid,
    RootCountInvalid,
    IoError,
    ArithmeticOverflow,
    PageOutOfBounds,
    PageHeaderInvalid,
    PageCrcMismatch,
    PageTypeMismatch,
    PageBornTxnInvalid,
    PageReservedNonzero,
    TreeCycle,
    PageAlias,
    TreeLevelInvalid,
    TreeOrderInvalid,
    TreeFenceInvalid,
    RangeReversed,
    RangeOverlap,
    RangeNotCoalesced,
    CatalogNameInvalid,
    CatalogBijectionInvalid,
    CatalogBitmapInvalid,
    MembershipBitmapInvalid,
    MembershipHashInvalid,
    MembershipReverseIndexInvalid,
    MembershipRefcountInvalid,
    MembershipActiveFeedInvalid,
    BlobInvalid,
    MetadataZlibInvalid,
    MetadataLengthInvalid,
    BitmapSummaryInvalid,
    AllocationPartitionInvalid,
    RetirementOrderInvalid,
    RetirementListInvalid,
    CatalogInvalid,
    MembershipMissing,
    MembershipInvalid,
    MetadataInvalid,
}

impl ValidationReason {
    pub const COUNT: usize = 40;

    #[inline]
    pub(crate) const fn index(self) -> usize {
        self as usize
    }

    pub(crate) const fn from_wire(value: u8) -> Option<Self> {
        Some(match value {
            0 => Self::MetaUnavailable,
            1 => Self::MetaInvalid,
            2 => Self::MetaStaticMismatch,
            3 => Self::FileGeometryInvalid,
            4 => Self::RootCountInvalid,
            5 => Self::IoError,
            6 => Self::ArithmeticOverflow,
            7 => Self::PageOutOfBounds,
            8 => Self::PageHeaderInvalid,
            9 => Self::PageCrcMismatch,
            10 => Self::PageTypeMismatch,
            11 => Self::PageBornTxnInvalid,
            12 => Self::PageReservedNonzero,
            13 => Self::TreeCycle,
            14 => Self::PageAlias,
            15 => Self::TreeLevelInvalid,
            16 => Self::TreeOrderInvalid,
            17 => Self::TreeFenceInvalid,
            18 => Self::RangeReversed,
            19 => Self::RangeOverlap,
            20 => Self::RangeNotCoalesced,
            21 => Self::CatalogNameInvalid,
            22 => Self::CatalogBijectionInvalid,
            23 => Self::CatalogBitmapInvalid,
            24 => Self::MembershipBitmapInvalid,
            25 => Self::MembershipHashInvalid,
            26 => Self::MembershipReverseIndexInvalid,
            27 => Self::MembershipRefcountInvalid,
            28 => Self::MembershipActiveFeedInvalid,
            29 => Self::BlobInvalid,
            30 => Self::MetadataZlibInvalid,
            31 => Self::MetadataLengthInvalid,
            32 => Self::BitmapSummaryInvalid,
            33 => Self::AllocationPartitionInvalid,
            34 => Self::RetirementOrderInvalid,
            35 => Self::RetirementListInvalid,
            36 => Self::CatalogInvalid,
            37 => Self::MembershipMissing,
            38 => Self::MembershipInvalid,
            39 => Self::MetadataInvalid,
            _ => return None,
        })
    }
}

/// Stable owning graph or object classes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum ValidationObject {
    FileGeometry,
    Meta,
    RangeTree,
    CatalogNameTree,
    CatalogIndexTree,
    MembershipDictionary,
    MembershipReverseIndex,
    MembershipBlob,
    Metadata,
    FreeBitmap,
    FeedUsedBitmap,
    MembershipUsedBitmap,
    RetirementTree,
    RetirementBlob,
}

impl ValidationObject {
    pub const COUNT: usize = 14;

    #[inline]
    pub(crate) const fn index(self) -> usize {
        self as usize
    }

    pub(crate) const fn from_wire(value: u8) -> Option<Self> {
        Some(match value {
            0 => Self::FileGeometry,
            1 => Self::Meta,
            2 => Self::RangeTree,
            3 => Self::CatalogNameTree,
            4 => Self::CatalogIndexTree,
            5 => Self::MembershipDictionary,
            6 => Self::MembershipReverseIndex,
            7 => Self::MembershipBlob,
            8 => Self::Metadata,
            9 => Self::FreeBitmap,
            10 => Self::FeedUsedBitmap,
            11 => Self::MembershipUsedBitmap,
            12 => Self::RetirementTree,
            13 => Self::RetirementBlob,
            _ => return None,
        })
    }
}

/// Half-open physical byte interval in the retained source file.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PhysicalByteInterval {
    pub start: u64,
    pub end_exclusive: u64,
}

/// Independently trusted inclusive logical address fence.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ValidationAddressFence {
    Ipv4 { from: Ipv4Key, to: Ipv4Key },
    Ipv6 { from: Ipv6Key, to: Ipv6Key },
}

/// One deterministic streamed validation defect.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ValidationFinding {
    pub sequence: u64,
    pub reason: ValidationReason,
    pub object: ValidationObject,
    pub page_number: Option<u32>,
    pub physical_bytes: Option<PhysicalByteInterval>,
    pub related_page_number: Option<u32>,
    pub address_fence: Option<ValidationAddressFence>,
}

/// Sink response for one borrowed validation finding.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ValidationSinkControl {
    Continue,
    Stop,
}

/// Synchronous streamed validation finding consumer.
pub trait ValidationSink {
    fn finding(&mut self, finding: &ValidationFinding) -> Result<ValidationSinkControl>;
}

impl<F> ValidationSink for F
where
    F: FnMut(&ValidationFinding) -> Result<ValidationSinkControl>,
{
    fn finding(&mut self, finding: &ValidationFinding) -> Result<ValidationSinkControl> {
        self(finding)
    }
}

/// Exact local identity of the retained source inode.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct LocalFileIdentity {
    pub kind: u16,
    pub bytes: [u8; 32],
}

/// Selected generation proved by a completed validation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ValidatedGeneration {
    pub address_family: AddressFamily,
    pub value_kind: ValueKind,
    pub value_tag: ValueTag,
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub page_count: u64,
    pub roots: [u32; 10],
}

/// Counters available from both completed and interrupted validation.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ValidationProgress {
    pub checked_unique_pages: u64,
    pub finding_count: u64,
    pub untraversable_subgraphs: u64,
    pub bounded_possible_span_addresses: Cardinality129,
    pub has_unbounded_unknown: bool,
    reason_counts: [u64; ValidationReason::COUNT],
    object_counts: [u64; ValidationObject::COUNT],
}

impl ValidationProgress {
    pub(crate) const fn new() -> Self {
        Self {
            checked_unique_pages: 0,
            finding_count: 0,
            untraversable_subgraphs: 0,
            bounded_possible_span_addresses: Cardinality129::ZERO,
            has_unbounded_unknown: false,
            reason_counts: [0; ValidationReason::COUNT],
            object_counts: [0; ValidationObject::COUNT],
        }
    }

    pub fn findings_for(&self, reason: ValidationReason) -> u64 {
        self.reason_counts[reason.index()]
    }

    pub fn examined_for(&self, object: ValidationObject) -> u64 {
        self.object_counts[object.index()]
    }

    pub(crate) fn count_finding(&mut self, reason: ValidationReason) -> Result<()> {
        self.finding_count = self
            .finding_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("validation finding count"))?;
        self.reason_counts[reason.index()] = self.reason_counts[reason.index()]
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow(
                "validation per-reason finding count",
            ))?;
        Ok(())
    }

    pub(crate) fn count_page(&mut self, object: ValidationObject) -> Result<()> {
        self.checked_unique_pages = self
            .checked_unique_pages
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("validation checked-page count"))?;
        self.object_counts[object.index()] = self.object_counts[object.index()]
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow(
                "validation per-object page count",
            ))?;
        Ok(())
    }

    pub(crate) fn mark_untraversable(&mut self, unbounded: bool) -> Result<()> {
        self.untraversable_subgraphs =
            self.untraversable_subgraphs
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow(
                    "validation untraversable-subgraph count",
                ))?;
        self.has_unbounded_unknown |= unbounded;
        Ok(())
    }

    pub(crate) fn wire_counts(
        &self,
    ) -> (
        &[u64; ValidationReason::COUNT],
        &[u64; ValidationObject::COUNT],
    ) {
        (&self.reason_counts, &self.object_counts)
    }

    pub(crate) fn from_wire(
        checked_unique_pages: u64,
        finding_count: u64,
        untraversable_subgraphs: u64,
        bounded_possible_span_addresses: Cardinality129,
        has_unbounded_unknown: bool,
        reason_counts: [u64; ValidationReason::COUNT],
        object_counts: [u64; ValidationObject::COUNT],
    ) -> Self {
        Self {
            checked_unique_pages,
            finding_count,
            untraversable_subgraphs,
            bounded_possible_span_addresses,
            has_unbounded_unknown,
            reason_counts,
            object_counts,
        }
    }
}

/// Completed factual validation report.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ValidationResult {
    pub valid: bool,
    pub file_identity: LocalFileIdentity,
    pub generation: Option<ValidatedGeneration>,
    pub progress: ValidationProgress,
}

/// Operational validation failure with truthful partial counters.
#[derive(Debug)]
pub struct ValidationFailure {
    pub cause: Error,
    pub progress: Box<ValidationProgress>,
    pub cleanup: Box<CleanupArtifacts>,
    pub coordination_cleanup: CoordinationCleanup,
    pub source_cleanup: Option<RecoverySourceCleanupGuard>,
}

impl ValidationFailure {
    pub const fn cleanup_state(&self) -> CleanupState {
        if self.cleanup.is_empty() && matches!(self.coordination_cleanup, CoordinationCleanup::None)
        {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}
