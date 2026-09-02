// Public one-inode immutable single-feed publication surface (Rust
// immutable_feed.rs parity): one unordered range source is normalized
// directly into a fresh published membership output under a caller
// budget, without an intermediate live database. The machine mirrors
// Rust create_immutable_feed_v4/v6: the budget prepares before any
// destination artifact, the source drains into a mapped workspace that
// shares the private attempt inode, the ordered normalized ranges
// stream into the append-only output pages, and the finished output
// publishes through the reservation machine. Every preparation failure
// collapses to Cause plus CleanupState like SnapshotPreparationFailure.

package iprangedb

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// RangeSource4 is one finite synchronous IPv4 source whose borrowed
// batches remain caller-owned (Rust source::RangeSource::next_batch):
// NextBatch returns one non-empty batch, (nil, nil) for the end, or an
// exact source error. An empty batch is refused by the machine exactly
// like the Rust source contract.
type RangeSource4 interface {
	NextBatch() ([]AddressRange4, error)
}

// RangeSource6 is the IPv6 form of RangeSource4.
type RangeSource6 interface {
	NextBatch() ([]AddressRange6, error)
}

// ImmutableFeedBudget bounds one immutable feed construction (Rust
// ImmutableFeedBudget): the maximum simultaneous retained heap bytes,
// the maximum output page count, the maximum workspace page count, and
// the maximum simultaneously open files. Validation mirrors Rust
// prepare_budget: at least two output pages and three open files, and
// the output plus workspace page count within the v4 page space.
type ImmutableFeedBudget struct {
	MaxHeapBytes      uint64
	MaxOutputPages    uint64
	MaxWorkspacePages uint64
	MaxOpenFiles      uint32
}

// ImmutableFeedReport is the exact semantic work completed before
// immutable publication (Rust ImmutableFeedReport): the input records
// drained from the source, the normalized interval count of the
// published feed, and the exact 129-bit address count.
type ImmutableFeedReport struct {
	InputRecordCount        uint64
	NormalizedIntervalCount uint64
	Addresses               Cardinality129
}

// ImmutableFeedResult is the successful terminal of one immutable feed
// construction (Rust ImmutableFeedResult): the exact report plus the
// publication result.
type ImmutableFeedResult struct {
	Report      ImmutableFeedReport
	Publication PublicationResult
}

// CleanupState reports the artifact state after the publication.
func (r ImmutableFeedResult) CleanupState() CleanupState {
	return r.Publication.CleanupState()
}

// ImmutableFeedPreparationFailure is the failing terminal of one
// immutable feed construction (Rust ImmutableFeedPreparationFailure
// projected onto the Go-visible fields, the SnapshotPreparationFailure
// precedent): the primary cause plus the full attempt facts (the
// private output identity, the cleanup ledger, the coordination cleanup
// class, and the housekeeping evidence). Cleanup is the derived state
// enum (clean exactly when the ledger is empty and no coordination
// guard is held, Rust cleanup_state()).
type ImmutableFeedPreparationFailure struct {
	Cause               error
	Cleanup             CleanupState
	Output              *PrivateOutputAttempt
	CleanupArtifacts    CleanupArtifacts
	CoordinationCleanup CoordinationCleanup
	Housekeeping        Housekeeping
	VisibleHousekeeping []HousekeepingArtifact
}

// Error renders the preparation failure.
func (f *ImmutableFeedPreparationFailure) Error() string {
	if f == nil {
		return "<nil>"
	}
	return "iprange v4 immutable feed preparation: " + f.Cause.Error()
}

// Unwrap exposes the primary cause.
func (f *ImmutableFeedPreparationFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// feedFailureOf converts one publication preparation failure into the
// public feed failure (Rust failure_from_early /
// ImmutableFeedPreparationFailure::from_publication: the machine facts
// carry the attempt identity, the cleanup ledger, the coordination
// class, and the housekeeping evidence; the derived enum matches the
// ledger and coordination rule).
func feedFailureOf(failure *publication.PublicationPreparationFailure) *ImmutableFeedPreparationFailure {
	return &ImmutableFeedPreparationFailure{
		Cause:               publicError(failure.Cause),
		Cleanup:             cleanupStateOf(failure.Cleanup, failure.CoordinationCleanup),
		Output:              failure.OutputAttempt(),
		CleanupArtifacts:    failure.Cleanup,
		CoordinationCleanup: failure.CoordinationCleanup,
		Housekeeping:        failure.Housekeeping,
		VisibleHousekeeping: failure.VisibleHousekeeping,
	}
}

// discardFeedFailure builds one feed preparation failure from an
// attempt discard (Rust ImmutableFeedPreparationFailure::discarded:
// the attempt identity and the fixed cleanup ledger of the removal).
func discardFeedFailure(cause error, attempt *publication.PublishAttempt) *ImmutableFeedPreparationFailure {
	output, artifact := attempt.DiscardFacts()
	cleanup := publication.NewCleanupArtifacts()
	if artifact != nil {
		cleanup.Push(*artifact)
	}
	return &ImmutableFeedPreparationFailure{
		Cause:               publicError(cause),
		Cleanup:             cleanupStateOf(cleanup, CoordinationCleanupNone),
		Output:              &output,
		CleanupArtifacts:    cleanup,
		CoordinationCleanup: CoordinationCleanupNone,
		Housekeeping:        HousekeepingNone,
		VisibleHousekeeping: nil,
	}
}

// CreateImmutableFeedV4 normalizes one unordered IPv4 feed directly
// into its immutable destination (Rust create_immutable_feed_v4):
// destination path, value tag, feed name, optional metadata JSON,
// publication policy, the finite batch source, the construction
// budget, and the optional cancellation token. On success the result
// carries the exact report and the publication; a refused or
// outcome-unknown publish is a result with its own Status/Cause, not an
// error, exactly like the publish_set surface. Every preparation
// failure returns *ImmutableFeedPreparationFailure. A nil budget, a
// nil source, and an invalid feed-name value are Go-boundary guards
// refused before any destination artifact exists; a nil cancellation
// token means uncancellable.
func CreateImmutableFeedV4(destination string, valueTag ValueTag, feedName FeedName, metadataJSON []byte, publicationPolicy PublicationPolicy, source RangeSource4, budget *ImmutableFeedBudget, cancellation *CancellationToken) (ImmutableFeedResult, error) {
	zero := ImmutableFeedResult{}
	if budget == nil {
		return zero, &ImmutableFeedPreparationFailure{
			Cause:   &Error{Code: ErrorInvalidArgument, Detail: "immutable feed budget is required"},
			Cleanup: CleanupStateClean,
		}
	}
	if source == nil {
		return zero, &ImmutableFeedPreparationFailure{
			Cause:   &Error{Code: ErrorInvalidArgument, Detail: "immutable feed source is required"},
			Cleanup: CleanupStateClean,
		}
	}
	if !format.FeedNameValidString(string(feedName)) {
		return zero, &ImmutableFeedPreparationFailure{
			Cause:   &Error{Code: ErrorNameInvalid, Detail: "feed name is invalid"},
			Cleanup: CleanupStateClean,
		}
	}
	var buffer []writer.FeedRange4
	nextBatch := func() ([]writer.FeedRange4, error) {
		batch, err := source.NextBatch()
		if err != nil {
			return nil, err
		}
		if batch == nil {
			return nil, nil
		}
		if len(batch) == 0 {
			// A non-nil empty batch crosses the seam as an empty
			// non-nil slice so the machine's empty-batch refusal fires
			// (Rust refuses empty batches at the source contract);
			// slicing a nil buffer would turn it into the end signal.
			return []writer.FeedRange4{}, nil
		}
		// The caller-owned public batch converts into one growable
		// writer batch; the machine consumes it fully before the next
		// source batch, so the steady-state allocation is bounded by
		// the largest source batch (reusable bounded workspace).
		if cap(buffer) < len(batch) {
			buffer = make([]writer.FeedRange4, len(batch))
		}
		buffer = buffer[:len(batch)]
		for i := range batch {
			buffer[i] = writer.FeedRange4{From: uint32(batch[i].From), To: uint32(batch[i].To)}
		}
		return buffer, nil
	}
	return createImmutableFeed(
		format.AddressFamilyIPv4, destination, valueTag, feedName, metadataJSON,
		publicationPolicy, budget, cancellation,
		func(builder *writer.OutputBuilder, spec writer.OutputSpec, prepared writer.ImmutableFeedPreparedBudget, attemptFile *os.File, check func() error) (writer.ImmutableFeedReport, error) {
			return writer.BuildImmutableFeedV4(builder, attemptFile, spec, prepared, feedName.String(), metadataJSON, nextBatch, check)
		},
	)
}

// CreateImmutableFeedV6 is the IPv6 form of CreateImmutableFeedV4
// (Rust create_immutable_feed_v6).
func CreateImmutableFeedV6(destination string, valueTag ValueTag, feedName FeedName, metadataJSON []byte, publicationPolicy PublicationPolicy, source RangeSource6, budget *ImmutableFeedBudget, cancellation *CancellationToken) (ImmutableFeedResult, error) {
	zero := ImmutableFeedResult{}
	if budget == nil {
		return zero, &ImmutableFeedPreparationFailure{
			Cause:   &Error{Code: ErrorInvalidArgument, Detail: "immutable feed budget is required"},
			Cleanup: CleanupStateClean,
		}
	}
	if source == nil {
		return zero, &ImmutableFeedPreparationFailure{
			Cause:   &Error{Code: ErrorInvalidArgument, Detail: "immutable feed source is required"},
			Cleanup: CleanupStateClean,
		}
	}
	if !format.FeedNameValidString(string(feedName)) {
		return zero, &ImmutableFeedPreparationFailure{
			Cause:   &Error{Code: ErrorNameInvalid, Detail: "feed name is invalid"},
			Cleanup: CleanupStateClean,
		}
	}
	var buffer []writer.FeedRange6
	nextBatch := func() ([]writer.FeedRange6, error) {
		batch, err := source.NextBatch()
		if err != nil {
			return nil, err
		}
		if batch == nil {
			return nil, nil
		}
		if len(batch) == 0 {
			// See the IPv4 facade: an empty batch stays a non-nil
			// empty slice so the machine refuses it.
			return []writer.FeedRange6{}, nil
		}
		if cap(buffer) < len(batch) {
			buffer = make([]writer.FeedRange6, len(batch))
		}
		buffer = buffer[:len(batch)]
		for i := range batch {
			buffer[i] = writer.FeedRange6{FromHi: batch[i].FromHi, FromLo: batch[i].FromLo, ToHi: batch[i].ToHi, ToLo: batch[i].ToLo}
		}
		return buffer, nil
	}
	return createImmutableFeed(
		format.AddressFamilyIPv6, destination, valueTag, feedName, metadataJSON,
		publicationPolicy, budget, cancellation,
		func(builder *writer.OutputBuilder, spec writer.OutputSpec, prepared writer.ImmutableFeedPreparedBudget, attemptFile *os.File, check func() error) (writer.ImmutableFeedReport, error) {
			return writer.BuildImmutableFeedV6(builder, attemptFile, spec, prepared, feedName.String(), metadataJSON, nextBatch, check)
		},
	)
}

// createImmutableFeed is the shared orchestration (Rust create()):
// budget preparation and the pre-create cancellation check before any
// destination artifact, the secured private attempt, the fresh output
// identity, the reference-batch heap charge, the output builder over
// the full output+workspace extent, the family build, and the publish
// terminal. Every failing build path closes the builder and discards
// the attempt with the exact cleanup state; the builder mapping is
// released before the attempt discard like the Rust drops.
func createImmutableFeed(
	family uint8, destination string, valueTag ValueTag, feedName FeedName, metadataJSON []byte,
	publicationPolicy PublicationPolicy, budget *ImmutableFeedBudget, cancellation *CancellationToken,
	build func(builder *writer.OutputBuilder, spec writer.OutputSpec, prepared writer.ImmutableFeedPreparedBudget, attemptFile *os.File, check func() error) (writer.ImmutableFeedReport, error),
) (ImmutableFeedResult, error) {
	zero := ImmutableFeedResult{}
	check := cancellation.check
	prepared, err := writer.PrepareImmutableFeedBudget(budget.internal())
	if err != nil {
		return zero, &ImmutableFeedPreparationFailure{Cause: publicError(err), Cleanup: CleanupStateClean}
	}
	if err := check(); err != nil {
		return zero, &ImmutableFeedPreparationFailure{Cause: publicError(err), Cleanup: CleanupStateClean}
	}
	attempt, failure := publication.CreatePublishAttempt(destination, publicationPolicy)
	if failure != nil {
		return zero, feedFailureOf(failure)
	}
	spec, err := writer.FreshOutputSpec(family, format.ValueKindMembership, format.StructureKindNone, valueTag.Wire(), 1)
	if err != nil {
		return zero, discardFeedFailure(err, attempt)
	}
	// The reference batch charges the operation heap exactly like Rust
	// new_owned_with_extent; the remaining heap becomes the normalize
	// and metadata budget (Rust heap.remaining()).
	heap := prepared.MaxHeapBytes
	membershipEntries := writer.ChargeReferenceBatch(&heap)
	prepared.MaxHeapBytes = heap
	builder, err := writer.NewImmutableFeedOutputBuilder(attempt.File(), spec, writer.OutputBudget{MaxOutputPages: prepared.MaxOutputPages}, prepared.TotalPages, membershipEntries)
	if err != nil {
		return zero, discardFeedFailure(err, attempt)
	}
	discarded := func(cause error) (ImmutableFeedResult, error) {
		// Rust drops the mapped writer in every failing path; Go must
		// release the builder before the attempt discard.
		closeErr := builder.Close()
		if closeErr != nil {
			cause = mergeErrors(cause, closeErr)
		}
		return zero, discardFeedFailure(cause, attempt)
	}
	report, err := build(builder, spec, prepared, attempt.File(), check)
	if err != nil {
		return discarded(err)
	}
	// Rust workflow::publish: the prepare machine re-checks cancellation
	// at its gate and throughout, the finished output (the attempt file
	// and the builder mapping) is consumed on every terminal, and the
	// one preparation failure surface carries the folded cleanup state.
	result, failure := attempt.Finish(publication.FinishedOutput{File: attempt.File(), Mapping: builder.Mapping(), Meta: builder.Meta()}, check)
	if failure != nil {
		return zero, feedFailureOf(failure)
	}
	return ImmutableFeedResult{
		Report: ImmutableFeedReport{
			InputRecordCount:        report.InputRecordCount,
			NormalizedIntervalCount: report.NormalizedIntervalCount,
			Addresses:               report.Addresses,
		},
		Publication: result,
	}, nil
}

// internal converts the public budget onto the machine budget (the
// PageBudget.internal() pattern).
func (b ImmutableFeedBudget) internal() writer.ImmutableFeedBudget {
	return writer.ImmutableFeedBudget{
		MaxHeapBytes:      b.MaxHeapBytes,
		MaxOutputPages:    b.MaxOutputPages,
		MaxWorkspacePages: b.MaxWorkspacePages,
		MaxOpenFiles:      b.MaxOpenFiles,
	}
}
