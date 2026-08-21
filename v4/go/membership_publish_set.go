// Materialized algebra set publication (Rust
// membership_query/algebra/output.rs publish parity). PublishSet builds
// one set operation over pinned membership scopes directly into its own
// immutable v4 file: the budget and cancellation gates, the prepared
// plan, the private publication attempt, the one-shot output builder
// (feed catalog, metadata, the ordered output sweep), and the staged
// publication complete with the Rust-verbatim error surface.

package iprangedb

import (
	"errors"
	"fmt"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// PublicationPolicy is the namespace policy of one published output
// (Rust PublicationPolicy; the writer staging authority).
type PublicationPolicy = writer.PublicationPolicy

const (
	PolicyFailIfExists              = writer.PolicyFailIfExists
	PolicyReplaceExisting           = writer.PolicyReplaceExisting
	PolicyReplaceExistingNoRollback = writer.PolicyReplaceExistingNoRollback
)

// CleanupState reports whether an abandoned attempt artifact was
// provably removed (Rust CleanupState).
type CleanupState = writer.CleanupState

const (
	CleanupStateClean           = writer.CleanupStateClean
	CleanupStateResiduePossible = writer.CleanupStateResiduePossible
)

// PublicationStatus classifies one publication outcome (Rust
// PublicationStatus).
type PublicationStatus = writer.PublicationStatus

const (
	PublicationNotPublished   = writer.PublicationNotPublished
	PublicationPublished      = writer.PublicationPublished
	PublicationOutcomeUnknown = writer.PublicationOutcomeUnknown
)

// DestinationContent describes the destination slot after one
// publication attempt (Rust DestinationContent).
type DestinationContent = writer.DestinationContent

const (
	DestinationContentDesired      = writer.DestinationContentDesired
	DestinationContentAbsent       = writer.DestinationContentAbsent
	DestinationContentUnclassified = writer.DestinationContentUnclassified
)

// PublicationResult is the factual outcome of one publish call (Rust
// PublicationResult).
type PublicationResult = writer.PublicationResult

// AlgebraOutputBudget bounds one published set output (Rust
// AlgebraOutputBudget: max output pages and the maximum simultaneous
// open files).
type AlgebraOutputBudget struct {
	MaxOutputPages uint64
	MaxOpenFiles   uint32
}

// algebraSetKind is the unexported AlgebraSetOperation discriminant.
type algebraSetKind uint8

const (
	algebraSetUnion algebraSetKind = iota
	algebraSetIntersection
	algebraSetExclusion
)

// AlgebraSetOperation is one set operation over virtual global feeds
// (Rust AlgebraSetOperation). Construct with AlgebraSetUnion,
// AlgebraSetIntersection, or AlgebraSetExclusion.
type AlgebraSetOperation struct {
	kind     algebraSetKind
	selected FeedSelection
	included FeedSelection
	excluded FeedSelection
}

// AlgebraSetUnion selects the union of one feed selection.
func AlgebraSetUnion(selection FeedSelection) AlgebraSetOperation {
	return AlgebraSetOperation{kind: algebraSetUnion, selected: selection}
}

// AlgebraSetIntersection selects the intersection of one feed selection.
func AlgebraSetIntersection(selection FeedSelection) AlgebraSetOperation {
	return AlgebraSetOperation{kind: algebraSetIntersection, selected: selection}
}

// AlgebraSetExclusion selects the feeds of included minus the feeds of
// excluded.
func AlgebraSetExclusion(included, excluded FeedSelection) AlgebraSetOperation {
	return AlgebraSetOperation{kind: algebraSetExclusion, included: included, excluded: excluded}
}

// AlgebraOutputMode is the catalog shape of one published output (Rust
// AlgebraOutputMode).
type AlgebraOutputMode struct {
	preserve bool
	name     string
}

// AlgebraOutputModePreserveFeeds keeps one output feed per selected
// global catalog position.
func AlgebraOutputModePreserveFeeds() AlgebraOutputMode {
	return AlgebraOutputMode{preserve: true}
}

// AlgebraOutputModeFlat materializes one named output feed; the name
// must satisfy the v4 feed-name rule (Rust FeedName::new parity, refused
// at the boundary before the operation starts).
func AlgebraOutputModeFlat(name string) (AlgebraOutputMode, error) {
	if !format.FeedNameValidString(name) {
		return AlgebraOutputMode{}, &Error{Code: ErrorNameInvalid, Detail: "invalid feed name"}
	}
	return AlgebraOutputMode{name: name}, nil
}

// AlgebraSetReport is the exact work and content facts of one published
// set result (Rust AlgebraSetReport).
type AlgebraSetReport struct {
	SourceCount        uint64
	SourceRangeCount   uint64
	JoinedSegmentCount uint64
	OutputFeedCount    uint64
	OutputRangeCount   uint64
	OutputAddresses    Cardinality129
}

// AlgebraSetResult is one published set output and its exact semantic
// report (Rust AlgebraSetResult).
type AlgebraSetResult struct {
	Report      AlgebraSetReport
	Publication PublicationResult
}

// CleanupState reports the attempt cleanup of one failed publication.
func (r AlgebraSetResult) CleanupState() CleanupState {
	return r.Publication.Cleanup
}

// AlgebraPreparationFailure classes one failed set publication before
// the destination provably held the output (Rust AlgebraPreparationFailure
// = the snapshot preparation failure shapes collapsed on the Go
// boundary: early/new carry Clean, discarded carries the attempt discard
// state, and from_publication carries the staging result cleanup). Cause
// is the public typed error with the Rust-verbatim detail.
type AlgebraPreparationFailure struct {
	Cause   error
	Cleanup CleanupState
}

func (f *AlgebraPreparationFailure) Error() string {
	if f == nil {
		return "<nil>"
	}
	return "iprange v4 algebra preparation: " + f.Cause.Error()
}

func (f *AlgebraPreparationFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// PublishSet materializes one set operation directly into its final
// immutable v4 file (Rust MembershipAlgebra::publish_set): validate
// budget -> prepare -> create attempt -> build (feeds, metadata, the
// ordered output sweep) -> finish -> publish. Early and preparation
// failures are the typed AlgebraPreparationFailure surface with the
// attempt artifact discarded per the Rust shapes; a publish that was
// attempted but refused or left unprovable returns the result with its
// Publication status, destination content, and Cause exactly like the
// Rust Ok(AlgebraSetResult) classification.
func (a *MembershipAlgebra) PublishSet(destination string, valueTag ValueTag, operation AlgebraSetOperation, mode AlgebraOutputMode, metadataJSON []byte, policy PublicationPolicy, budget AlgebraOutputBudget, cancellation *CancellationToken) (AlgebraSetResult, error) {
	zero := AlgebraSetResult{}
	if err := a.openOK(); err != nil {
		return zero, err
	}
	readOperation, err := a.setOperation(operation)
	if err != nil {
		return zero, err
	}
	readMode, err := a.outputMode(mode)
	if err != nil {
		return zero, err
	}
	early := func(cause error) *AlgebraPreparationFailure {
		return &AlgebraPreparationFailure{Cause: publicError(cause), Cleanup: CleanupStateClean}
	}
	if err := validateAlgebraOutputBudget(budget, policy); err != nil {
		return zero, early(err)
	}
	if err := cancellation.check(); err != nil {
		return zero, early(err)
	}
	prepared, err := a.inner.PrepareAlgebraOutput(readOperation, readMode, cancellation.check)
	if err != nil {
		return zero, early(err)
	}
	attempt, err := writer.CreateAttempt(destination, policy)
	if err != nil {
		return zero, early(err)
	}
	spec, err := writer.FreshOutputSpec(a.inner.AddressFamily(), format.ValueKindMembership, format.StructureKindNone, valueTag.Wire(), uint64(prepared.OutputFeedCount()))
	if err != nil {
		return zero, &AlgebraPreparationFailure{Cause: publicError(err), Cleanup: attempt.Discard()}
	}
	// The immutable reference batch is sized and charged from the
	// operation heap exactly like Rust ReferenceBatch::new at builder
	// construction: the metadata budget below and the admission
	// decisions stay byte-identical with the authority.
	refEntries, err := prepared.ChargeReferenceBatch()
	if err != nil {
		return zero, &AlgebraPreparationFailure{Cause: publicError(err), Cleanup: attempt.Discard()}
	}
	builder, err := writer.NewOutputBuilder(attempt.AttemptPath(), spec, writer.OutputBudget{MaxOutputPages: budget.MaxOutputPages}, refEntries, nil)
	if err != nil {
		return zero, &AlgebraPreparationFailure{Cause: publicError(err), Cleanup: attempt.Discard()}
	}
	// Capture the attempt-file identity from the builder's own
	// descriptor: every later Discard is identity-guarded (Rust
	// CreatedOutput::create_with binds cleanup to the created inode).
	device, inode, idErr := builder.FileIdentity()
	if idErr != nil {
		closeErr := builder.Close()
		cleanup := attempt.Discard()
		if closeErr != nil {
			idErr = mergeErrors(idErr, closeErr)
		}
		return zero, &AlgebraPreparationFailure{Cause: publicError(idErr), Cleanup: cleanup}
	}
	attempt.SetFileIdentity(device, inode)
	discarded := func(cause error) (AlgebraSetResult, error) {
		// Rust drops the mapped writer in every path; Go must release the
		// exclusive lifetime lock before the caller can reopen the
		// destination, so the builder closes before the attempt discard.
		closeErr := builder.Close()
		cleanup := attempt.Discard()
		if closeErr != nil {
			// Keep the primary cause and attach the close error: a
			// cleanup-side close failure must not erase why the
			// operation failed.
			cause = mergeErrors(cause, closeErr)
		}
		return zero, &AlgebraPreparationFailure{Cause: publicError(cause), Cleanup: cleanup}
	}
	if metadataJSON != nil {
		if err := builder.WriteMetadata(metadataJSON, prepared.HeapRemaining()); err != nil {
			return discarded(err)
		}
	}
	built, err := a.inner.BuildAlgebraOutput(prepared, readMode, builder.PushFeed, func(words []uint64) (uint32, error) {
		return builder.InternMembership(writer.OutputWords(words))
	}, builder.PushInternedMembershipV4, builder.PushInternedMembershipV6, cancellation.check)
	if err != nil {
		return discarded(err)
	}
	if err := builder.Finish(); err != nil {
		return discarded(err)
	}
	// Rust publish re-checks cancellation at the publication gate
	// (workflow::publish prepare_cancellable and the policy
	// *_cancellable steps): a token cancelled during the build must not
	// proceed to the rename.
	if err := cancellation.check(); err != nil {
		return discarded(err)
	}
	result, err := writer.Publish(attempt, builder, policy)
	closeErr := builder.Close()
	if err != nil {
		var failure *writer.PublicationPreparationFailure
		if errors.As(err, &failure) {
			return zero, &AlgebraPreparationFailure{Cause: publicError(failure.Cause), Cleanup: failure.Cleanup}
		}
		return zero, publicError(err)
	}
	if closeErr != nil {
		if result.Cause != nil {
			// A refused or outcome-unknown publish keeps its Rust Ok
			// classification; the close failure is cleanup-side and is
			// attached as the secondary cause (Rust has no fallible
			// step after publish Ok - the builder drop is infallible).
			// The caller still inspects Status, DestinationContent,
			// and the result's own Cleanup state.
			if merged := mergeErrors(result.Cause, closeErr); merged != nil {
				result.Cause = merged
			}
			return AlgebraSetResult{
				Report: AlgebraSetReport{
					SourceCount:        built.SourceCount,
					SourceRangeCount:   built.SourceRangeCount,
					JoinedSegmentCount: built.JoinedSegmentCount,
					OutputFeedCount:    built.OutputFeedCount,
					OutputRangeCount:   built.OutputRangeCount,
					OutputAddresses:    built.OutputAddresses,
				},
				Publication: *result,
			}, nil
		}
		// The destination provably holds the published file; the close
		// failure is still reported as a hard error, conservative and
		// unchanged: the caller must not assume the sealed mapping was
		// released.
		return zero, &AlgebraPreparationFailure{Cause: publicError(closeErr), Cleanup: CleanupStateClean}
	}
	// A refused or outcome-unknown publish is a Rust Ok result carrying
	// its Cause, not an error: the caller inspects Status,
	// DestinationContent, and Cleanup.
	return AlgebraSetResult{
		Report: AlgebraSetReport{
			SourceCount:        built.SourceCount,
			SourceRangeCount:   built.SourceRangeCount,
			JoinedSegmentCount: built.JoinedSegmentCount,
			OutputFeedCount:    built.OutputFeedCount,
			OutputRangeCount:   built.OutputRangeCount,
			OutputAddresses:    built.OutputAddresses,
		},
		Publication: *result,
	}, nil
}

// mergeErrors keeps the primary cause of a failed publication and
// attaches a cleanup-side close error as the secondary. A single
// fmt.Errorf %w reproduces the Rust shape exactly: the primary stays the
// errors.As/Is/Unwrap target with the secondary present in the message
// only.
func mergeErrors(primary, secondary error) error {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%w; %v", primary, secondary)
}

// setOperation converts one public operation into the reader operation
// (Rust FeedName::new parity: invalid selection names fail before the
// operation starts).
func (a *MembershipAlgebra) setOperation(operation AlgebraSetOperation) (reader.AlgebraSetOperation, error) {
	switch operation.kind {
	case algebraSetUnion:
		selection, err := a.selection(operation.selected)
		if err != nil {
			return reader.AlgebraSetOperation{}, err
		}
		return reader.AlgebraSetUnion(selection), nil
	case algebraSetIntersection:
		selection, err := a.selection(operation.selected)
		if err != nil {
			return reader.AlgebraSetOperation{}, err
		}
		return reader.AlgebraSetIntersection(selection), nil
	case algebraSetExclusion:
		included, err := a.selection(operation.included)
		if err != nil {
			return reader.AlgebraSetOperation{}, err
		}
		excluded, err := a.selection(operation.excluded)
		if err != nil {
			return reader.AlgebraSetOperation{}, err
		}
		return reader.AlgebraSetExclusion(included, excluded), nil
	default:
		return reader.AlgebraSetOperation{}, &Error{Code: ErrorInvalidArgument, Detail: "unknown membership algebra set operation"}
	}
}

// outputMode converts one public output mode into the reader mode.
func (a *MembershipAlgebra) outputMode(mode AlgebraOutputMode) (reader.AlgebraOutputMode, error) {
	if mode.preserve {
		return reader.AlgebraOutputModePreserveFeeds(), nil
	}
	return reader.AlgebraOutputModeFlat(mode.name)
}

// validateAlgebraOutputBudget mirrors Rust output.rs validate_budget:
// at least two output pages and two open files for FailIfExists, three
// for the replacement policies.
func validateAlgebraOutputBudget(budget AlgebraOutputBudget, policy PublicationPolicy) error {
	if budget.MaxOutputPages < 2 {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra output pages"}
	}
	required := uint32(2)
	if policy != PolicyFailIfExists {
		required = 3
	}
	if budget.MaxOpenFiles < required {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership algebra output files"}
	}
	return nil
}
