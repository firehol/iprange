// Named-feed lifecycle JSON-RPC handlers (Rust handlers/feeds.rs
// parity, iprange-jsonrpc-v1.md). Every mutation opens one clean live
// writer, performs one high-level SDK workflow, applies the requested
// metadata inside that draft, commits when changed, and returns the
// complete workflow and commit/close facts (results.py
// _FEED_SOURCE_COMMON).
//
// Create, replace, and import stream one named membership feed from an
// ephemeral source reader (immutable or live) into the workflow draft;
// the reader is closed before the draft finishes and the factual live
// close is reported as source_close. Delete and rename return the
// commit, metadata, and writer-close facts only: the SDK exposes no
// workflow report for them (product decision D2-B), and the
// catalog-changing outcome is carried by the commit facts.

package handlers

import (
	"encoding/json"
	"errors"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// RegisterFeeds installs the named-feed lifecycle handler families
// (called by the lead from register.go).
func RegisterFeeds() {
	rpc.Register("iprange.v1.feeds.create", ValidateFeedsCreate, FeedsCreate)
	rpc.Register("iprange.v1.feeds.replace", ValidateFeedsReplace, FeedsReplace)
	rpc.Register("iprange.v1.feeds.delete", ValidateFeedsDelete, FeedsDelete)
	rpc.Register("iprange.v1.feeds.rename", ValidateFeedsRename, FeedsRename)
	rpc.Register("iprange.v1.feeds.import", ValidateFeedsImport, FeedsImport)
}

// ---------------------------------------------------------------------------
// Strict params validators (each maps to methods.py).
// ---------------------------------------------------------------------------

// ValidateFeedsCreate enforces {path, feed, current, metadata,
// writer_budget}.
func ValidateFeedsCreate(params json.RawMessage) error {
	return feedMutationValidator(params, []string{"path", "feed", "current", "metadata", "writer_budget"})
}

// ValidateFeedsReplace enforces the create schema.
func ValidateFeedsReplace(params json.RawMessage) error {
	return feedMutationValidator(params, []string{"path", "feed", "current", "metadata", "writer_budget"})
}

// ValidateFeedsDelete enforces {path, feed, metadata, writer_budget}.
func ValidateFeedsDelete(params json.RawMessage) error {
	return feedMutationValidator(params, []string{"path", "feed", "metadata", "writer_budget"})
}

// ValidateFeedsRename enforces {path, old_feed, new_feed, metadata,
// writer_budget}.
func ValidateFeedsRename(params json.RawMessage) error {
	return feedMutationValidator(params, []string{"path", "old_feed", "new_feed", "metadata", "writer_budget"})
}

// ValidateFeedsImport enforces {path, source, metadata,
// writer_budget}.
func ValidateFeedsImport(params json.RawMessage) error {
	return feedMutationValidator(params, []string{"path", "source", "metadata", "writer_budget"})
}

// feedMutationValidator runs the shared strict decode of one feed
// mutation params object.
func feedMutationValidator(params json.RawMessage, required []string) error {
	_, herr := decodeFeedParams(params, required)
	if herr != nil {
		return errors.New(herr.Message)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Params decoding.
// ---------------------------------------------------------------------------

// feedParams is the decoded contract of one named-feed mutation.
type feedParams struct {
	path        string
	feed        iprangedb.FeedName
	oldFeed     iprangedb.FeedName
	newFeed     iprangedb.FeedName
	currentFeed string
	sourcePath  string
	sourceMode  string
	metadata    MetadataValue
	budget      iprangedb.PageBudget
}

// decodeFeedParams decodes one feed mutation params object against its
// exact required member set.
func decodeFeedParams(params json.RawMessage, required []string) (*feedParams, *rpc.HandlerError) {
	decoded := &feedParams{}
	object, err := exactObject(params, required...)
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil || validatePath(path) != nil {
		return nil, rpc.InvalidParamsError("path is invalid")
	}
	decoded.path = path
	for _, member := range []string{"feed", "old_feed", "new_feed"} {
		raw, ok := object[member]
		if !ok {
			continue
		}
		if isRawNull(raw) {
			return nil, rpc.InvalidParamsError(member + " must be a string; null is not valid")
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, rpc.InvalidParamsError(member + " must be a string")
		}
		name, err := iprangedb.NewFeedName(text)
		if err != nil {
			return nil, rpc.InvalidParamsError("feed does not use the v4 FeedName grammar")
		}
		switch member {
		case "feed":
			decoded.feed = name
		case "old_feed":
			decoded.oldFeed = name
		case "new_feed":
			decoded.newFeed = name
		}
	}
	if raw, ok := object["current"]; ok {
		current, err := decodeObject(raw)
		if err != nil {
			return nil, rpc.InvalidParamsError("current must be an object")
		}
		if err := exactObjectRaw(current, "source", "feed"); err != nil {
			return nil, rpc.InvalidParamsError("current is invalid")
		}
		source, err := memberObject(current, "source")
		if err != nil {
			return nil, rpc.InvalidParamsError("current.source must be an object")
		}
		sourcePath, sourceMode, herr := decodeSource(source, "current.source")
		if herr != nil {
			return nil, herr
		}
		decoded.sourcePath = sourcePath
		decoded.sourceMode = sourceMode
		feed, err := asString(current, "feed")
		if err != nil {
			return nil, rpc.InvalidParamsError("current.feed must be a string")
		}
		if _, err := iprangedb.NewFeedName(feed); err != nil {
			return nil, rpc.InvalidParamsError("current.feed is invalid")
		}
		decoded.currentFeed = feed
	}
	if raw, ok := object["source"]; ok {
		source, err := decodeObject(raw)
		if err != nil {
			return nil, rpc.InvalidParamsError("source must be an object")
		}
		sourcePath, sourceMode, herr := decodeSource(source, "source")
		if herr != nil {
			return nil, herr
		}
		decoded.sourcePath = sourcePath
		decoded.sourceMode = sourceMode
	}
	metadataObject, err := memberObject(object, "metadata")
	if err != nil {
		return nil, rpc.InvalidParamsError("metadata must be an object")
	}
	metadata, herr := MetadataValueFromObject(metadataObject)
	if herr != nil {
		return nil, herr
	}
	decoded.metadata = metadata
	budgetObject, err := memberObject(object, "writer_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError("writer_budget must be an object")
	}
	budget, err := writerBudgetFromObject(budgetObject)
	if err != nil {
		return nil, rpc.InvalidParamsError("writer_budget is invalid")
	}
	decoded.budget = budget
	return decoded, nil
}

// writerBudgetFromObject decodes the strict writer_budget object into
// the SDK page budget (Rust lifecycle.rs writer_budget; methods.py
// WRITER_BUDGET).
func writerBudgetFromObject(object rawObject) (iprangedb.PageBudget, error) {
	heap, err := positiveU64String(object, "max_heap_bytes")
	if err != nil {
		return iprangedb.PageBudget{}, err
	}
	privatePages, err := positiveU64String(object, "max_private_pages")
	if err != nil {
		return iprangedb.PageBudget{}, err
	}
	growthPages, err := positiveU64String(object, "max_growth_pages")
	if err != nil {
		return iprangedb.PageBudget{}, err
	}
	openFiles, err := positiveU32(object, "max_open_files")
	if err != nil {
		return iprangedb.PageBudget{}, err
	}
	return iprangedb.PageBudget{
		MaxHeapBytes:    heap,
		MaxPrivatePages: privatePages,
		MaxGrowthPages:  growthPages,
		MaxOpenFiles:    openFiles,
	}, nil
}

// ---------------------------------------------------------------------------
// Ephemeral source reader machinery (shared with the live family).
// ---------------------------------------------------------------------------

// openDatabaseSource opens one ephemeral database source by path and
// mode (Rust live.rs open_source_reader / feeds.rs open_temporary);
// label names the source in error messages.
func openDatabaseSource(path, mode, label string, token *iprangedb.CancellationToken) (*rpc.ReaderValue, *rpc.HandlerError) {
	return openReader(path, mode, label, token)
}

// readerInfo returns the info facts of either reader kind.
func readerInfoErr(reader *rpc.ReaderValue) (iprangedb.DatabaseInfo, error) {
	if reader.Live != nil {
		return reader.Live.Info()
	}
	return reader.Immutable.Info()
}

// feedPresent reports whether the named feed exists in the source
// catalog (Rust feeds.rs feed_present).
func feedPresent(reader *rpc.ReaderValue, name string) (bool, error) {
	if reader.Live != nil {
		_, found, err := reader.Live.LookupFeed(name)
		return found, err
	}
	_, found, err := reader.Immutable.LookupFeed(name)
	return found, err
}

// drainFeedV4 streams one named feed's IPv4 intervals into the
// workflow in bounded batches; a source without the feed contributes
// the empty input (Rust FeedInputV4).
func drainFeedV4(reader *rpc.ReaderValue, name string, present bool, add func([]iprangedb.AddressRange4) error) *rpc.HandlerError {
	if !present {
		return nil
	}
	var cursor *iprangedb.FeedRangeCursorV4
	var err error
	if reader.Live != nil {
		cursor, err = reader.Live.FeedRangeCursorV4(name, iprangedb.RangeDirectionForward)
	} else {
		cursor, err = reader.Immutable.FeedRangeCursorV4(name, iprangedb.RangeDirectionForward)
	}
	if err != nil {
		return readError(err)
	}
	batch := make([]iprangedb.AddressRange4, 0, rangeBatchCapacity)
	for {
		record, ok, err := cursor.NextRange()
		if err != nil {
			return readError(err)
		}
		if !ok {
			break
		}
		batch = append(batch, record)
		if len(batch) >= rangeBatchCapacity {
			if err := add(batch); err != nil {
				return SDKError(err, "not_started")
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := add(batch); err != nil {
			return SDKError(err, "not_started")
		}
	}
	return nil
}

// drainFeedV6 streams one named feed's IPv6 intervals into the
// workflow in bounded batches (Rust FeedInputV6).
func drainFeedV6(reader *rpc.ReaderValue, name string, present bool, add func([]iprangedb.AddressRange6) error) *rpc.HandlerError {
	if !present {
		return nil
	}
	var cursor *iprangedb.FeedRangeCursorV6
	var err error
	if reader.Live != nil {
		cursor, err = reader.Live.FeedRangeCursorV6(name, iprangedb.RangeDirectionForward)
	} else {
		cursor, err = reader.Immutable.FeedRangeCursorV6(name, iprangedb.RangeDirectionForward)
	}
	if err != nil {
		return readError(err)
	}
	batch := make([]iprangedb.AddressRange6, 0, rangeBatchCapacity)
	for {
		record, ok, err := cursor.NextRange()
		if err != nil {
			return readError(err)
		}
		if !ok {
			break
		}
		batch = append(batch, record)
		if len(batch) >= rangeBatchCapacity {
			if err := add(batch); err != nil {
				return SDKError(err, "not_started")
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := add(batch); err != nil {
			return SDKError(err, "not_started")
		}
	}
	return nil
}

// closeEphemeralReader closes one ephemeral source reader and returns
// its factual live-close result (nil for immutable sources; Rust
// reader.rs close_ephemeral_reader). An incomplete close is an error
// that still carries the close fact.
func closeEphemeralReader(reader *rpc.ReaderValue) (map[string]any, *rpc.HandlerError) {
	result, ok, err := reader.CloseLive()
	if err != nil {
		return nil, readError(err)
	}
	if !ok {
		// Immutable readers carry no close fact but their mapping must
		// still be released (Rust close_ephemeral_reader parity).
		if reader.Immutable != nil {
			if err := reader.Immutable.Close(); err != nil {
				return nil, readError(err)
			}
		}
		return nil, nil
	}
	closeFacts := map[string]any{
		"outcome":              CloseOutcomeName(result.Outcome),
		"cleanup":              map[string]any{},
		"coordination_cleanup": CoordinationCleanupJSON(result.CoordinationCleanup),
	}
	if result.Outcome != iprangedb.CloseOutcomeClosed || result.Cause != nil {
		code := "io"
		message := "live reader close is incomplete"
		if result.Cause != nil {
			if typed, ok := result.Cause.(*iprangedb.Error); ok {
				code = sdkCode(typed.Code)
			}
			message = result.Cause.Error()
		}
		return nil, &rpc.HandlerError{Code: code, Outcome: "read_only_failure", Message: message,
			Details: map[string]any{"source_close": closeFacts}}
	}
	return closeFacts, nil
}

// closeRefreshFacts closes the refresh source reader and writer on an
// error path, merging the factual close results into the error details
// (Rust live.rs close_refresh_facts).
func closeRefreshFacts(reader *rpc.ReaderValue, writer *iprangedb.LiveWriter, failure *rpc.HandlerError) *rpc.HandlerError {
	failure = CloseOnError([]*rpc.ReaderValue{reader}, failure)
	return CloseWriterFacts(writer, failure)
}

// mergeSourceClose places the already-factual source close next to the
// writer close facts in an error's details (Rust live.rs
// merge_source_close); a nil source close leaves the error unchanged.
func mergeSourceClose(failure *rpc.HandlerError, sourceClose map[string]any) *rpc.HandlerError {
	if sourceClose == nil {
		return failure
	}
	return mergeDetailsMember(failure, "source_close", sourceClose)
}

// closeFactOf extracts the factual source_close carried inside one
// close failure (Rust collect_workflow_facts double-fault arm).
func closeFactOf(closeErr *rpc.HandlerError) map[string]any {
	if closeErr == nil {
		return nil
	}
	if details, ok := closeErr.Details.(map[string]any); ok {
		if fact, ok := details["source_close"].(map[string]any); ok {
			return fact
		}
	}
	return nil
}

// withSourceClose attaches the factual source close to a wire outcome:
// a success carries it as the optional source_close member, an error
// carries it inside details next to the writer close facts (spec
// iprange-jsonrpc-v1.md factual close rule).
func withSourceClose(value any, failure *rpc.HandlerError, sourceClose map[string]any) (any, *rpc.HandlerError) {
	if sourceClose == nil {
		return value, failure
	}
	if failure != nil {
		return nil, mergeSourceClose(failure, sourceClose)
	}
	if result, ok := value.(map[string]any); ok {
		result["source_close"] = sourceClose
	}
	return value, nil
}

// sdkWorkflow maps one SDK workflow failure to a handler error.
func sdkWorkflow[T any](value T, err error) (T, *rpc.HandlerError) {
	if err != nil {
		var zero T
		return zero, SDKError(err, "not_started")
	}
	return value, nil
}

// ---------------------------------------------------------------------------
// Handlers.
// ---------------------------------------------------------------------------

// FeedsCreate creates one named feed from the current coverage source.
func FeedsCreate(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return publisherFeedWorkflow(st, params, "iprange.v1.feeds.create", true)
}

// FeedsReplace replaces one existing named feed from the current
// coverage source.
func FeedsReplace(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return publisherFeedWorkflow(st, params, "iprange.v1.feeds.replace", false)
}

// FeedsDelete deletes one existing named feed and publishes the
// metadata facts.
func FeedsDelete(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return feedChangeWorkflow(st, params, "iprange.v1.feeds.delete", false)
}

// FeedsRename renames one existing named feed and publishes the
// metadata facts.
func FeedsRename(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return feedChangeWorkflow(st, params, "iprange.v1.feeds.rename", true)
}

// FeedsImport imports the complete source catalog and memberships by
// name from one pinned membership reader.
func FeedsImport(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	decoded, herr := decodeFeedParams(params, []string{"path", "source", "metadata", "writer_budget"})
	if herr != nil {
		return nil, herr
	}
	if herr := requireExistingDatabase(decoded.path); herr != nil {
		return nil, herr
	}
	writer, err := iprangedb.OpenLiveWriter(decoded.path, decoded.budget, st.Token())
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	reader, herr := openDatabaseSource(decoded.sourcePath, decoded.sourceMode, "database source", st.Token())
	if herr != nil {
		return nil, CloseWriterFacts(writer, herr)
	}
	finished, wfErr := runImportWorkflow(writer, reader, st.Token())
	return finishFeedFacts(writer, reader, finished, wfErr, &decoded.metadata, st.Token(), "iprange.v1.feeds.import")
}

// publisherFeedWorkflow drives the create/replace named-feed workflow
// (Rust feeds.rs publisher_feed_workflow).
func publisherFeedWorkflow(st *rpc.SessionState, params json.RawMessage, method string, create bool) (any, *rpc.HandlerError) {
	decoded, herr := decodeFeedParams(params, []string{"path", "feed", "current", "metadata", "writer_budget"})
	if herr != nil {
		return nil, herr
	}
	if herr := requireExistingDatabase(decoded.path); herr != nil {
		return nil, herr
	}
	writer, err := iprangedb.OpenLiveWriter(decoded.path, decoded.budget, st.Token())
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	reader, herr := openDatabaseSource(decoded.sourcePath, decoded.sourceMode, "database source", st.Token())
	if herr != nil {
		return nil, CloseWriterFacts(writer, herr)
	}
	finished, wfErr := runFeedWorkflow(writer, reader, decoded.currentFeed, decoded.feed, create, st.Token())
	return finishFeedFacts(writer, reader, finished, wfErr, &decoded.metadata, st.Token(), method)
}

// runFeedWorkflow creates or replaces one named feed from the current
// coverage source ranges (Rust feeds.rs run_feed_workflow).
func runFeedWorkflow(writer *iprangedb.LiveWriter, reader *rpc.ReaderValue, currentFeed string, target iprangedb.FeedName, create bool, token *iprangedb.CancellationToken) (*iprangedb.FinishedWorkflow, *rpc.HandlerError) {
	info, err := readerInfoErr(reader)
	if err != nil {
		return nil, readError(err)
	}
	present, err := feedPresent(reader, currentFeed)
	if err != nil {
		return nil, readError(err)
	}
	var finished *iprangedb.FinishedWorkflow
	if info.Family == iprangedb.AddressFamilyIPv6 {
		if create {
			draft, herr := sdkWorkflow(writer.BeginCreateFeed(target, token))
			if herr != nil {
				return nil, herr
			}
			if herr := drainFeedV6(reader, currentFeed, present, draft.AddRangesV6); herr != nil {
				return nil, herr
			}
			finished, err = draft.FinishInput()
		} else {
			draft, herr := sdkWorkflow(writer.BeginReplaceFeed(target, token))
			if herr != nil {
				return nil, herr
			}
			if herr := drainFeedV6(reader, currentFeed, present, draft.AddRangesV6); herr != nil {
				return nil, herr
			}
			finished, err = draft.FinishInput()
		}
	} else if create {
		draft, herr := sdkWorkflow(writer.BeginCreateFeed(target, token))
		if herr != nil {
			return nil, herr
		}
		if herr := drainFeedV4(reader, currentFeed, present, draft.AddRangesV4); herr != nil {
			return nil, herr
		}
		finished, err = draft.FinishInput()
	} else {
		draft, herr := sdkWorkflow(writer.BeginReplaceFeed(target, token))
		if herr != nil {
			return nil, herr
		}
		if herr := drainFeedV4(reader, currentFeed, present, draft.AddRangesV4); herr != nil {
			return nil, herr
		}
		finished, err = draft.FinishInput()
	}
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	return finished, nil
}

// runImportWorkflow starts and finishes one complete membership import
// from the pinned reader (Rust feeds.rs run_import).
func runImportWorkflow(writer *iprangedb.LiveWriter, reader *rpc.ReaderValue, token *iprangedb.CancellationToken) (*iprangedb.FinishedWorkflow, *rpc.HandlerError) {
	var source iprangedb.MembershipImportSource
	if reader.Live != nil {
		source = iprangedb.MembershipImportSourceLive(reader.Live)
	} else {
		source = iprangedb.MembershipImportSourceImmutable(reader.Immutable)
	}
	draft, herr := sdkWorkflow(writer.BeginMembershipImport(source, token))
	if herr != nil {
		return nil, herr
	}
	finished, err := draft.FinishInput()
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	return finished, nil
}

// finishFeedFacts closes the ephemeral source reader, commits the
// workflow facts, and converts them into the wire result (Rust
// feeds.rs collect_workflow_facts + finish_workflow_facts).
func finishFeedFacts(writer *iprangedb.LiveWriter, reader *rpc.ReaderValue, finished *iprangedb.FinishedWorkflow, wfErr *rpc.HandlerError, metadata *MetadataValue, token *iprangedb.CancellationToken, method string) (any, *rpc.HandlerError) {
	sourceClose, closeErr := closeEphemeralReader(reader)
	var report map[string]any
	if finished != nil {
		report = WorkflowReportJSON(finishedReport(finished))
	}
	if closeErr != nil {
		if wfErr != nil {
			// Both the workflow and the reader close failed: keep the
			// workflow error primary and merge the factual reader-close
			// result it carried (Rust double-fault arm).
			return nil, WorkflowFailure(writer, mergeSourceClose(wfErr, closeFactOf(closeErr)))
		}
		// The workflow completed; keep its report with the close failure.
		details := map[string]any{"report": report}
		if closeFacts, cerr := CloseWriter(writer); cerr == nil {
			details["writer_close"] = closeFacts
		}
		closeErr.Details = details
		return nil, closeErr
	}
	if wfErr != nil {
		return nil, WorkflowFailure(writer, mergeSourceClose(wfErr, sourceClose))
	}
	if !finished.IsChanged() {
		metadataLogicalChange, commit, herr := PublishNoChange(writer, metadata, token)
		if herr != nil {
			return nil, mergeSourceClose(FinishWriterError(writer, herr, report), sourceClose)
		}
		value, ferr := FinishPublisher(writer, method, report, metadataLogicalChange, commit, nil)
		return withSourceClose(value, ferr, sourceClose)
	}
	metadataLogicalChange, commit, _, perr := PublishChanged(finished, metadata)
	if perr != nil {
		return nil, mergeSourceClose(FinishWriterError(writer, perr, report), sourceClose)
	}
	value, ferr := FinishPublisher(writer, method, report, metadataLogicalChange, &commit, nil)
	return withSourceClose(value, ferr, sourceClose)
}

// feedChangeWorkflow deletes or renames one existing feed and
// publishes the catalog-changing commit plus the metadata and
// writer-close facts (Rust feeds.rs feed_change_workflow). The SDK
// exposes no workflow report for these mutations; a failed change
// closes the writer and keeps the close facts in the error details.
func feedChangeWorkflow(st *rpc.SessionState, params json.RawMessage, method string, rename bool) (any, *rpc.HandlerError) {
	required := []string{"path", "feed", "metadata", "writer_budget"}
	if rename {
		required = []string{"path", "old_feed", "new_feed", "metadata", "writer_budget"}
	}
	decoded, herr := decodeFeedParams(params, required)
	if herr != nil {
		return nil, herr
	}
	if herr := requireExistingDatabase(decoded.path); herr != nil {
		return nil, herr
	}
	writer, err := iprangedb.OpenLiveWriter(decoded.path, decoded.budget, st.Token())
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	var prepared *iprangedb.PreparedFeedChange
	if rename {
		prepared, err = writer.RenameFeed(decoded.oldFeed, decoded.newFeed, st.Token())
	} else {
		prepared, err = writer.DeleteFeed(decoded.feed, st.Token())
	}
	if err != nil {
		failure := SDKError(err, "not_started")
		closeFacts, closeErr := CloseWriter(writer)
		details := map[string]any{}
		if closeErr != nil {
			details["writer_close_error"] = closeErr.Message
		} else {
			details["writer_close"] = closeFacts
		}
		failure.Details = details
		return nil, failure
	}
	metadataLogicalChange, commit, _, perr := PublishChanged(prepared, metadataValueOf(decoded))
	if perr != nil {
		return nil, FinishWriterError(writer, perr, nil)
	}
	return FinishPublisher(writer, method, nil, metadataLogicalChange, &commit, nil)
}

// metadataValueOf returns the decoded metadata terminal of the
// already-validated feed params.
func metadataValueOf(decoded *feedParams) *MetadataValue {
	return &decoded.metadata
}
