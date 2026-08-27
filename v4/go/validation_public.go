// Public validation surface (Rust validation.rs validate + the
// validation::types re-exports): one explicit bounded full-file
// validation over the selected source mode without changing the
// source. The sweep streams deterministic findings into the sink and
// reports the completed factual result or the operational failure
// with its partial progress. The shapes below alias the internal
// validation types exactly like the Rust crate re-exports them; the
// cancellation token folds into the internal checkpoint hook. The
// offline-candidate mode is entered through the recovery package
// (the Go enum cannot carry the Rust candidate payload).

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/routing"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// ValidationMode selects the source binding of one validation
// operation (Rust ValidationMode). The Go enum cannot carry the
// candidate payload of the Rust OfflineCandidate variant; that arm is
// entered through the recovery package (ValidateOfflineCandidate).
type ValidationMode = validation.ValidationMode

const (
	// ValidationModeImmutableCurrent validates the committed
	// generation of one immutable database path under a shared
	// lifetime lock.
	ValidationModeImmutableCurrent = validation.ValidationModeImmutableCurrent
	// ValidationModeLiveCurrent validates the registered live source
	// of one live database path (or the bootstrap registration when
	// the committed generation cannot be selected).
	ValidationModeLiveCurrent = validation.ValidationModeLiveCurrent
	// ValidationModeOfflineCandidate marks the retained
	// recovery-candidate validation arm; the bare Go enum carries no
	// candidate token, so the arm is entered through the recovery
	// package.
	ValidationModeOfflineCandidate = validation.ValidationModeOfflineCandidate
)

// ValidationBudget bounds one validation operation (Rust
// ValidationBudget).
type ValidationBudget = validation.ValidationBudget

// HeapOnly builds a budget which forbids external scratch files (Rust
// ValidationBudget::heap_only).
func HeapOnly(maxHeapBytes uint64, maxOpenFiles uint32) *ValidationBudget {
	return validation.HeapOnly(maxHeapBytes, maxOpenFiles)
}

// ValidationReason is the deterministic defect class of one validation
// finding (Rust ValidationReason; COUNT and order are wire-stable).
type ValidationReason = validation.ValidationReason

const (
	ReasonMetaUnavailable               = validation.ReasonMetaUnavailable
	ReasonMetaInvalid                   = validation.ReasonMetaInvalid
	ReasonMetaStaticMismatch            = validation.ReasonMetaStaticMismatch
	ReasonFileGeometryInvalid           = validation.ReasonFileGeometryInvalid
	ReasonRootCountInvalid              = validation.ReasonRootCountInvalid
	ReasonIoError                       = validation.ReasonIoError
	ReasonArithmeticOverflow            = validation.ReasonArithmeticOverflow
	ReasonPageOutOfBounds               = validation.ReasonPageOutOfBounds
	ReasonPageHeaderInvalid             = validation.ReasonPageHeaderInvalid
	ReasonPageCrcMismatch               = validation.ReasonPageCrcMismatch
	ReasonPageTypeMismatch              = validation.ReasonPageTypeMismatch
	ReasonPageBornTxnInvalid            = validation.ReasonPageBornTxnInvalid
	ReasonPageReservedNonzero           = validation.ReasonPageReservedNonzero
	ReasonTreeCycle                     = validation.ReasonTreeCycle
	ReasonPageAlias                     = validation.ReasonPageAlias
	ReasonTreeLevelInvalid              = validation.ReasonTreeLevelInvalid
	ReasonTreeOrderInvalid              = validation.ReasonTreeOrderInvalid
	ReasonTreeFenceInvalid              = validation.ReasonTreeFenceInvalid
	ReasonRangeReversed                 = validation.ReasonRangeReversed
	ReasonRangeOverlap                  = validation.ReasonRangeOverlap
	ReasonRangeNotCoalesced             = validation.ReasonRangeNotCoalesced
	ReasonCatalogNameInvalid            = validation.ReasonCatalogNameInvalid
	ReasonCatalogBijectionInvalid       = validation.ReasonCatalogBijectionInvalid
	ReasonCatalogBitmapInvalid          = validation.ReasonCatalogBitmapInvalid
	ReasonMembershipBitmapInvalid       = validation.ReasonMembershipBitmapInvalid
	ReasonMembershipHashInvalid         = validation.ReasonMembershipHashInvalid
	ReasonMembershipReverseIndexInvalid = validation.ReasonMembershipReverseIndexInvalid
	ReasonMembershipRefcountInvalid     = validation.ReasonMembershipRefcountInvalid
	ReasonMembershipActiveFeedInvalid   = validation.ReasonMembershipActiveFeedInvalid
	ReasonBlobInvalid                   = validation.ReasonBlobInvalid
	ReasonMetadataZlibInvalid           = validation.ReasonMetadataZlibInvalid
	ReasonMetadataLengthInvalid         = validation.ReasonMetadataLengthInvalid
	ReasonBitmapSummaryInvalid          = validation.ReasonBitmapSummaryInvalid
	ReasonAllocationPartitionInvalid    = validation.ReasonAllocationPartitionInvalid
	ReasonRetirementOrderInvalid        = validation.ReasonRetirementOrderInvalid
	ReasonRetirementListInvalid         = validation.ReasonRetirementListInvalid
	ReasonCatalogInvalid                = validation.ReasonCatalogInvalid
	ReasonMembershipMissing             = validation.ReasonMembershipMissing
	ReasonMembershipInvalid             = validation.ReasonMembershipInvalid
	ReasonMetadataInvalid               = validation.ReasonMetadataInvalid
	ReasonStructurePayloadInvalid       = validation.ReasonStructurePayloadInvalid
	ReasonStructureHashInvalid          = validation.ReasonStructureHashInvalid
	ReasonStructureReverseIndexInvalid  = validation.ReasonStructureReverseIndexInvalid
	ReasonStructureRefcountInvalid      = validation.ReasonStructureRefcountInvalid
	ReasonStructureMembershipInvalid    = validation.ReasonStructureMembershipInvalid
	ReasonStructureMissing              = validation.ReasonStructureMissing
	ReasonStructureInvalid              = validation.ReasonStructureInvalid
)

// ValidationReasonCount is the number of reason classes (Rust
// ValidationReason::COUNT).
const ValidationReasonCount = validation.ValidationReasonCount

// ValidationObject is the stable owning graph or object class of one
// finding (Rust ValidationObject; COUNT and order are wire-stable).
type ValidationObject = validation.ValidationObject

const (
	ObjectFileGeometry           = validation.ObjectFileGeometry
	ObjectMeta                   = validation.ObjectMeta
	ObjectRangeTree              = validation.ObjectRangeTree
	ObjectCatalogNameTree        = validation.ObjectCatalogNameTree
	ObjectCatalogIndexTree       = validation.ObjectCatalogIndexTree
	ObjectMembershipDictionary   = validation.ObjectMembershipDictionary
	ObjectMembershipReverseIndex = validation.ObjectMembershipReverseIndex
	ObjectMembershipBlob         = validation.ObjectMembershipBlob
	ObjectMetadata               = validation.ObjectMetadata
	ObjectFreeBitmap             = validation.ObjectFreeBitmap
	ObjectFeedUsedBitmap         = validation.ObjectFeedUsedBitmap
	ObjectMembershipUsedBitmap   = validation.ObjectMembershipUsedBitmap
	ObjectRetirementTree         = validation.ObjectRetirementTree
	ObjectRetirementBlob         = validation.ObjectRetirementBlob
	ObjectStructureDictionary    = validation.ObjectStructureDictionary
	ObjectStructureReverseIndex  = validation.ObjectStructureReverseIndex
	ObjectStructureUsedBitmap    = validation.ObjectStructureUsedBitmap
)

// ValidationObjectCount is the number of object classes (Rust
// ValidationObject::COUNT).
const ValidationObjectCount = validation.ValidationObjectCount

// PhysicalByteInterval is a half-open physical byte interval in the
// retained source file (Rust PhysicalByteInterval).
type PhysicalByteInterval = validation.PhysicalByteInterval

// ValidationAddressFence is an independently trusted inclusive
// logical address fence (Rust ValidationAddressFence).
type ValidationAddressFence = validation.ValidationAddressFence

// ValidationFinding is one deterministic streamed validation defect
// (Rust ValidationFinding).
type ValidationFinding = validation.ValidationFinding

// ValidationSinkControl is the sink response for one borrowed finding
// (Rust ValidationSinkControl).
type ValidationSinkControl = validation.ValidationSinkControl

const (
	// SinkContinue asks the sweep to continue after one finding.
	SinkContinue = validation.SinkContinue
	// SinkStop asks the sweep to stop after one finding.
	SinkStop = validation.SinkStop
)

// ValidationSink consumes one borrowed validation finding and decides
// whether the sweep continues (Rust ValidationSink). A nil sink (or a
// nil function adapter) behaves like Continue for every finding.
type ValidationSink = validation.ValidationSink

// SinkFunc adapts a plain function to the sink interface (Rust impl
// ValidationSink for F: FnMut(&ValidationFinding) -> Result<...>).
type SinkFunc = validation.SinkFunc

// LocalFileIdentity is the exact portable identity of one retained
// inode (Rust validation::LocalFileIdentity).
type LocalFileIdentity = validation.LocalFileIdentity

// ValidatedGeneration is the selected generation proved by a completed
// validation (Rust ValidatedGeneration).
type ValidatedGeneration = validation.ValidatedGeneration

// ValidationProgress carries the counters available from both
// completed and interrupted validation (Rust ValidationProgress).
type ValidationProgress = validation.ValidationProgress

// ValidationResult is the completed factual validation report (Rust
// ValidationResult).
type ValidationResult = validation.ValidationResult

// ValidationFailure is the operational validation failure with
// truthful partial counters (Rust ValidationFailure).
type ValidationFailure = validation.ValidationFailure

// Validate runs one explicit validation over the selected source
// without changing it (Rust validation::validate): the mode preflight
// and budget checks run before any path access (on linux/amd64 the
// preflight runs and then the operation routes through the isolated
// worker client, exactly like the Rust public entry), the sweep
// streams findings into sink, and the failure carries the partial
// progress and the cleanup ledger. cancellation, when non-nil, is
// checked between bounded steps. Exactly one of the result and the
// failure is non-nil.
func Validate(path string, mode ValidationMode, budget *ValidationBudget, cancellation *CancellationToken, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	result, failure := routing.Validate(path, mode, budget, cancellation.hook(), sink)
	if failure != nil {
		converted := *failure
		converted.Cause = publicError(failure.Cause)
		return nil, &converted
	}
	return result, nil
}
