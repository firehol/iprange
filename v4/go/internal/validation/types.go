package validation

// Public validation surface (Rust validation/types.rs): the mode,
// budget, reason and object classes, the streamed finding shape, the
// sink control, the local identity, the proven generation, the
// progress counters, and the completed result / operational failure.
// The wire values of the enums match the Rust declarations.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// ValidationMode selects the source binding of one validation
// operation (Rust ValidationMode). The Go enum cannot carry the
// candidate payload of the Rust OfflineCandidate variant; the
// offline-candidate arm is entered through the recovery package
// (ValidateOfflineCandidate), which composes the shared sweep.
type ValidationMode uint8

const (
	ValidationModeImmutableCurrent ValidationMode = 1
	ValidationModeLiveCurrent      ValidationMode = 2
	ValidationModeOfflineCandidate ValidationMode = 3
)

// ValidationBudget bounds one validation operation (Rust
// ValidationBudget). Scratch limits are accepted and recorded; the
// 4-10 recovery follow-up adds the scratch machinery, so the current
// sweep is heap-only by construction.
type ValidationBudget struct {
	MaxHeapBytes     uint64
	MaxOpenFiles     uint32
	MaxScratchBytes  uint64
	MaxScratchFiles  uint32
	ScratchDirectory string
}

// HeapOnly builds a budget which forbids external scratch files (Rust
// ValidationBudget::heap_only).
func HeapOnly(maxHeapBytes uint64, maxOpenFiles uint32) *ValidationBudget {
	return &ValidationBudget{
		MaxHeapBytes:    maxHeapBytes,
		MaxOpenFiles:    maxOpenFiles,
		MaxScratchBytes: 0,
		MaxScratchFiles: 0,
	}
}

// Validate checks the budget invariants (Rust ValidationBudget::
// validate): at least one open file, scratch limits supplied together
// with the directory, and byte/file scratch limits both or neither.
// The recovery inspection and worker surfaces call the exported
// entry.
func (b *ValidationBudget) Validate() error { return b.validate() }

// validate checks the budget invariants (Rust ValidationBudget::
// validate).
func (b *ValidationBudget) validate() error {
	if b.MaxOpenFiles == 0 {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "validation requires at least one open file"}
	}
	scratchEnabled := b.MaxScratchBytes != 0 || b.MaxScratchFiles != 0
	if scratchEnabled != (b.ScratchDirectory != "") {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "scratch directory and scratch limits must be supplied together"}
	}
	if (b.MaxScratchBytes == 0 && b.MaxScratchFiles != 0) || (b.MaxScratchBytes != 0 && b.MaxScratchFiles == 0) {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "both scratch byte and file limits must be nonzero"}
	}
	return nil
}

// ValidationReason is the deterministic defect class of one validation
// finding (Rust ValidationReason; COUNT and order are wire-stable).
type ValidationReason uint8

const (
	ReasonMetaUnavailable ValidationReason = iota
	ReasonMetaInvalid
	ReasonMetaStaticMismatch
	ReasonFileGeometryInvalid
	ReasonRootCountInvalid
	ReasonIoError
	ReasonArithmeticOverflow
	ReasonPageOutOfBounds
	ReasonPageHeaderInvalid
	ReasonPageCrcMismatch
	ReasonPageTypeMismatch
	ReasonPageBornTxnInvalid
	ReasonPageReservedNonzero
	ReasonTreeCycle
	ReasonPageAlias
	ReasonTreeLevelInvalid
	ReasonTreeOrderInvalid
	ReasonTreeFenceInvalid
	ReasonRangeReversed
	ReasonRangeOverlap
	ReasonRangeNotCoalesced
	ReasonCatalogNameInvalid
	ReasonCatalogBijectionInvalid
	ReasonCatalogBitmapInvalid
	ReasonMembershipBitmapInvalid
	ReasonMembershipHashInvalid
	ReasonMembershipReverseIndexInvalid
	ReasonMembershipRefcountInvalid
	ReasonMembershipActiveFeedInvalid
	ReasonBlobInvalid
	ReasonMetadataZlibInvalid
	ReasonMetadataLengthInvalid
	ReasonBitmapSummaryInvalid
	ReasonAllocationPartitionInvalid
	ReasonRetirementOrderInvalid
	ReasonRetirementListInvalid
	ReasonCatalogInvalid
	ReasonMembershipMissing
	ReasonMembershipInvalid
	ReasonMetadataInvalid
	ReasonStructurePayloadInvalid
	ReasonStructureHashInvalid
	ReasonStructureReverseIndexInvalid
	ReasonStructureRefcountInvalid
	ReasonStructureMembershipInvalid
	ReasonStructureMissing
	ReasonStructureInvalid
)

// ValidationReasonCount is the number of reason classes (Rust
// ValidationReason::COUNT).
const ValidationReasonCount = 47

// ValidationObject is the stable owning graph or object class of one
// finding (Rust ValidationObject; COUNT and order are wire-stable).
type ValidationObject uint8

const (
	ObjectFileGeometry ValidationObject = iota
	ObjectMeta
	ObjectRangeTree
	ObjectCatalogNameTree
	ObjectCatalogIndexTree
	ObjectMembershipDictionary
	ObjectMembershipReverseIndex
	ObjectMembershipBlob
	ObjectMetadata
	ObjectFreeBitmap
	ObjectFeedUsedBitmap
	ObjectMembershipUsedBitmap
	ObjectRetirementTree
	ObjectRetirementBlob
	ObjectStructureDictionary
	ObjectStructureReverseIndex
	ObjectStructureUsedBitmap
)

// ValidationObjectCount is the number of object classes (Rust
// ValidationObject::COUNT).
const ValidationObjectCount = 17

// PhysicalByteInterval is a half-open physical byte interval in the
// retained source file (Rust PhysicalByteInterval).
type PhysicalByteInterval struct {
	Start        uint64
	EndExclusive uint64
}

// ValidationAddressFence is an independently trusted inclusive
// logical address fence (Rust ValidationAddressFence; the Go peer
// keeps raw keys and no validator populates a fence yet).
type ValidationAddressFence struct {
	IPv4   bool
	From   uint64
	To     uint64
	FromV6 [16]byte
	ToV6   [16]byte
}

// ValidationFinding is one deterministic streamed validation defect
// (Rust ValidationFinding).
type ValidationFinding struct {
	Sequence          uint64
	Reason            ValidationReason
	Object            ValidationObject
	PageNumber        *uint32
	PhysicalBytes     *PhysicalByteInterval
	RelatedPageNumber *uint32
	AddressFence      *ValidationAddressFence
}

// ValidationSinkControl is the sink response for one borrowed finding
// (Rust ValidationSinkControl).
type ValidationSinkControl uint8

const (
	SinkContinue ValidationSinkControl = iota
	SinkStop
)

// LocalFileIdentity is the exact portable identity of one retained
// inode (Rust validation::LocalFileIdentity; the publication package
// owns the encoding authority).
type LocalFileIdentity = publication.LocalFileIdentity

// ValidatedGeneration is the selected generation proved by a completed
// validation (Rust ValidatedGeneration). The fields mirror the
// format.Meta scalars; Roots is the 13-root vector (Rust
// MetaV4::roots, the Go roots live across the format meta fields).
type ValidatedGeneration struct {
	AddressFamily uint8
	ValueKind     uint8
	StructureKind uint8
	ValueTag      [16]byte
	DatabaseID    [16]byte
	TransactionID uint64
	CommitNonce   [16]byte
	PageCount     uint64
	Roots         [13]uint32
}

// generation builds the validated generation of one selected meta
// (Rust validation::generation).
func generation(meta format.Meta) *ValidatedGeneration {
	return &ValidatedGeneration{
		AddressFamily: meta.AddressFamily,
		ValueKind:     meta.ValueKind,
		StructureKind: meta.StructureKind,
		ValueTag:      meta.ValueTag,
		DatabaseID:    meta.DatabaseID,
		TransactionID: meta.TxnID,
		CommitNonce:   meta.CommitNonce,
		PageCount:     meta.PageCount,
		Roots: [13]uint32{
			meta.RangeRoot, meta.CatalogNameRoot, meta.CatalogIndexRoot,
			meta.FeedUsedRoot, meta.MembershipIDRoot, meta.MembershipHashRoot,
			meta.MembershipUsedRoot, meta.MetadataRoot, meta.FreeBitmapRoot,
			meta.RetirementRoot, meta.StructureIDRoot, meta.StructureHashRoot,
			meta.StructureUsedRoot,
		},
	}
}

// ValidationProgress carries the counters available from both
// completed and interrupted validation (Rust ValidationProgress).
// BoundedPossibleSpanAddresses is the Cardinality129 counter of the
// range span accounting; the Go peer keeps the same overflow-safe
// counter as the reader package.
type ValidationProgress struct {
	CheckedUniquePages           uint64
	FindingCount                 uint64
	UntraversableSubgraphs       uint64
	BoundedPossibleSpanAddresses Cardinality129
	HasUnboundedUnknown          bool
	reasonCounts                 [ValidationReasonCount]uint64
	objectCounts                 [ValidationObjectCount]uint64
}

// Cardinality129 is the overflow-safe bounded count of Rust
// Cardinality129 (internal/format owns the shared authority).
type Cardinality129 = format.Cardinality129

// ValidationResult is the completed factual validation report (Rust
// ValidationResult).
type ValidationResult struct {
	Valid        bool
	FileIdentity LocalFileIdentity
	Generation   *ValidatedGeneration
	Progress     ValidationProgress
}

// ValidationFailure is the operational validation failure with
// truthful partial counters (Rust ValidationFailure; the cleanup
// authorities are the 4-8 publication shapes, empty on the
// immutable path).
type ValidationFailure struct {
	Cause               error
	Progress            *ValidationProgress
	Cleanup             publication.CleanupArtifacts
	CoordinationCleanup publication.CoordinationCleanup
	SourceCleanup       any
}

// CleanupState reports whether the failure left residue (Rust
// ValidationFailure::cleanup_state).
func (f *ValidationFailure) CleanupState() publication.CleanupState {
	if f.Cleanup.Empty() && f.CoordinationCleanup == publication.CoordinationCleanupNone {
		return publication.CleanupStateClean
	}
	return publication.CleanupStateResiduePossible
}

// NewProgress returns the zero counters of one validation operation
// (Rust ValidationProgress::new).
func NewProgress() ValidationProgress {
	return ValidationProgress{}
}

// ProgressFromCounters builds one validation progress from its full
// counter set (Rust ValidationProgress::new plus the count arms; the
// worker boundary reconstructs a domain progress from the wire form
// because the per-reason and per-object counter arrays are private).
func ProgressFromCounters(checkedUniquePages, findingCount, untraversableSubgraphs uint64, hasUnboundedUnknown bool, bounded Cardinality129, reasons [ValidationReasonCount]uint64, objects [ValidationObjectCount]uint64) *ValidationProgress {
	return &ValidationProgress{
		CheckedUniquePages:           checkedUniquePages,
		FindingCount:                 findingCount,
		UntraversableSubgraphs:       untraversableSubgraphs,
		HasUnboundedUnknown:          hasUnboundedUnknown,
		BoundedPossibleSpanAddresses: bounded,
		reasonCounts:                 reasons,
		objectCounts:                 objects,
	}
}

// Failure builds one operational validation failure with the partial
// progress and the clean ledger (Rust validation::failure; the
// recovery offline-candidate arm composes its terminal through this
// exported entry).
func Failure(cause error, progress *ValidationProgress) *ValidationFailure {
	return failureOf(cause, progress)
}

// Generation builds the validated generation of one selected meta
// (Rust validation::generation; the recovery offline-candidate arm
// composes its result through this exported entry).
func Generation(meta format.Meta) *ValidatedGeneration {
	return generation(meta)
}

// FindingsFor reports the finding count of one reason class (Rust
// ValidationProgress::findings_for).
func (p *ValidationProgress) FindingsFor(reason ValidationReason) uint64 {
	return p.reasonCounts[reason]
}

// ExaminedFor reports the page count of one object class (Rust
// ValidationProgress::examined_for).
func (p *ValidationProgress) ExaminedFor(object ValidationObject) uint64 {
	return p.objectCounts[object]
}

// countFinding increments the finding counters (Rust
// ValidationProgress::count_finding; overflow is the
// ArithmeticOverflow class).
func (p *ValidationProgress) countFinding(reason ValidationReason) error {
	p.FindingCount++
	if p.FindingCount == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation finding count"}
	}
	n := p.reasonCounts[reason] + 1
	if n == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation per-reason finding count"}
	}
	p.reasonCounts[reason] = n
	return nil
}

// countPage increments the checked-page counters (Rust
// ValidationProgress::count_page).
func (p *ValidationProgress) countPage(object ValidationObject) error {
	p.CheckedUniquePages++
	if p.CheckedUniquePages == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation checked-page count"}
	}
	n := p.objectCounts[object] + 1
	if n == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation per-object page count"}
	}
	p.objectCounts[object] = n
	return nil
}

// CountFinding counts one finding into a validation progress (Rust
// ValidationProgress::count_finding; the recovery inspection records
// classification findings through this exported entry).
func CountFinding(p *ValidationProgress, reason ValidationReason) error {
	return p.countFinding(reason)
}

// MarkUntraversable counts one untraversable subgraph and records the
// unbounded-unknown flag (Rust ValidationProgress::
// mark_untraversable; the recovery inspection entry).
func MarkUntraversable(p *ValidationProgress, unbounded bool) error {
	return p.markUntraversable(unbounded)
}

// markUntraversable counts one untraversable subgraph and records the
// unbounded-unknown flag (Rust ValidationProgress::
// mark_untraversable).
func (p *ValidationProgress) markUntraversable(unbounded bool) error {
	p.UntraversableSubgraphs++
	if p.UntraversableSubgraphs == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation untraversable-subgraph count"}
	}
	p.HasUnboundedUnknown = p.HasUnboundedUnknown || unbounded
	return nil
}
