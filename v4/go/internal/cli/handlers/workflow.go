// Shared publisher-facts machinery for the mutation handler families
// (Rust handlers/workflow.rs parity).
//
// The algebra, history, and feeds mutation handlers all finish the
// same way: stage the requested metadata inside one prepared SDK draft
// (or one fresh membership transaction when no draft exists), commit
// when changed, close the writer, and convert the commit/close facts.
// This file is the single authority for that finalization; families
// add only their own draft implementations.

package handlers

import (
	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// CloseWriter closes the live writer and converts its close result.
func CloseWriter(writer *iprangedb.LiveWriter) (map[string]any, *rpc.HandlerError) {
	result, err := writer.Close()
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	return CloseResultJSON(&result)
}

// CommitDraft is one prepared SDK draft that accepts metadata staging
// and a final commit (Rust CommitDraft).
type CommitDraft interface {
	SetMetadataJSON(input []byte) (bool, error)
	ClearMetadataJSON() (bool, error)
	Commit() (iprangedb.CommitResult, error)
}

// PublishChanged stages the requested metadata inside one changed
// prepared draft and commits; the returned commit result carries the
// exact durability facts (the caller converts it).
func PublishChanged(draft CommitDraft, metadata *MetadataValue) (string, iprangedb.CommitResult, bool, *rpc.HandlerError) {
	metadataLogicalChange := "unchanged"
	switch {
	case metadata.Keep:
		// keep
	case metadata.Clear:
		switch changed, err := draft.ClearMetadataJSON(); {
		case err != nil:
			return "", iprangedb.CommitResult{}, false, SDKError(err, "not_started")
		case changed:
			metadataLogicalChange = "changed"
		}
	default:
		if _, err := draft.SetMetadataJSON(metadata.Bytes); err != nil {
			return "", iprangedb.CommitResult{}, false, SDKError(err, "not_started")
		}
		metadataLogicalChange = "changed"
	}
	commit, err := draft.Commit()
	if err != nil {
		return metadataLogicalChange, iprangedb.CommitResult{}, true, nil
	}
	return metadataLogicalChange, commit, true, nil
}

// PublishNoChange commits only the requested metadata through one
// fresh membership transaction when no workflow draft exists.
// Replacements always commit; clear commits only when metadata was
// present. A no-op keep returns no commit.
func PublishNoChange(writer *iprangedb.LiveWriter, metadata *MetadataValue, cancellation *iprangedb.CancellationToken) (string, *iprangedb.CommitResult, *rpc.HandlerError) {
	if metadata.Keep {
		return "unchanged", nil, nil
	}
	transaction, err := writer.BeginMembershipTransaction(cancellation)
	if err != nil {
		return "", nil, SDKError(err, "not_started")
	}
	if metadata.Clear {
		changed, err := transaction.ClearMetadataJSON()
		if err != nil {
			_ = transaction.Abort()
			return "", nil, SDKError(err, "not_started")
		}
		if !changed {
			_ = transaction.Abort()
			return "unchanged", nil, nil
		}
		commit, err := transaction.Commit()
		if err != nil {
			return "changed", &iprangedb.CommitResult{}, nil
		}
		return "changed", &commit, nil
	}
	if _, err := transaction.SetMetadataJSON(metadata.Bytes); err != nil {
		_ = transaction.Abort()
		return "", nil, SDKError(err, "not_started")
	}
	commit, err := transaction.Commit()
	if err != nil {
		return "changed", &iprangedb.CommitResult{}, nil
	}
	return "changed", &commit, nil
}

// FinishPublisher converts the final commit/close facts into the
// publisher result or a product error that preserves every factual
// field (Rust finish_publisher).
func FinishPublisher(writer *iprangedb.LiveWriter, method string, report any, metadataLogicalChange string, commit *iprangedb.CommitResult, commitErr error) (any, *rpc.HandlerError) {
	var closeFacts map[string]any
	var closeErr *rpc.HandlerError
	closeFacts, closeErr = CloseWriter(writer)
	if closeErr != nil {
		// The writer is in an unusable close state; keep the facts.
		closeFacts = map[string]any{"outcome": "close_incomplete", "cleanup": map[string]any{}, "coordination_cleanup": map[string]any{}}
	}
	if commitErr != nil {
		failure := SDKError(commitErr, "not_started")
		details := map[string]any{
			"metadata_logical_change": metadataLogicalChange,
			"writer_close":            closeFacts,
			"failure":                 map[string]any{"code": failure.Code, "message": failure.Message},
		}
		if report != nil {
			details["report"] = report
		}
		return nil, &rpc.HandlerError{Code: failure.Code, Outcome: failure.Outcome, Message: failure.Message, Details: details}
	}
	if commit != nil {
		if commit.Status != iprangedb.CommitCommitted || commit.Cause != nil {
			code := "io"
			message := "publisher commit did not complete"
			if commit.Cause != nil {
				code = sdkCode(commit.Cause.(*iprangedb.Error).Code)
				message = commit.Cause.Error()
			}
			commitFacts, cerr := CommitResultJSON(commit)
			if cerr != nil {
				commitFacts = map[string]any{}
			}
			details := map[string]any{
				"metadata_logical_change": metadataLogicalChange,
				"commit":                  commitFacts,
				"writer_close":            closeFacts,
			}
			if report != nil {
				details["report"] = report
			}
			return nil, &rpc.HandlerError{
				Code:    code,
				Outcome: DurabilityOutcome(commit.Status),
				Message: message,
				Details: details,
			}
		}
	}
	result := map[string]any{
		"method":                  method,
		"metadata_logical_change": metadataLogicalChange,
		"writer_close":            closeFacts,
	}
	if report != nil {
		result["report"] = report
	}
	if commit != nil {
		commitFacts, cerr := CommitResultJSON(commit)
		if cerr != nil {
			return nil, cerr
		}
		result["commit"] = commitFacts
	}
	if outcome, ok := result["writer_close"].(map[string]any)["outcome"].(string); ok && outcome == "close_incomplete" {
		sentence := "not_started"
		if commit != nil {
			sentence = "committed"
		}
		return nil, &rpc.HandlerError{
			Code:    "io",
			Outcome: sentence,
			Message: "live writer close is incomplete",
			Details: result,
		}
	}
	return boundedResult(result)
}

// WorkflowFailure aborts a failed workflow, closes the writer, and
// keeps the close facts (Rust workflow_failure).
func WorkflowFailure(writer *iprangedb.LiveWriter, failure *rpc.HandlerError) *rpc.HandlerError {
	_ = writer.Abort()
	if closeFacts, err := CloseWriter(writer); err == nil {
		return mergeWriterFacts(failure, closeFacts, nil)
	}
	return failure
}

// FinishWriterError closes the writer after a metadata-stage failure;
// the draft is already aborted by the SDK, so the completed logical
// report is preserved (Rust finish_writer_error).
func FinishWriterError(writer *iprangedb.LiveWriter, failure *rpc.HandlerError, report any) *rpc.HandlerError {
	closeFacts, err := CloseWriter(writer)
	if err != nil {
		return failure
	}
	return mergeWriterFacts(failure, closeFacts, report)
}

// mergeWriterFacts merges the factual writer close and the completed
// report into the error's existing details; the incoming error may
// already carry reader-close facts, which are preserved.
func mergeWriterFacts(failure *rpc.HandlerError, closeFacts map[string]any, report any) *rpc.HandlerError {
	details := map[string]any{}
	if failure.Details != nil {
		if existing, ok := failure.Details.(map[string]any); ok {
			details = existing
		}
	}
	if report != nil {
		details["report"] = report
	}
	if closeFacts != nil {
		details["writer_close"] = closeFacts
	}
	failure.Details = details
	return failure
}

// CloseWriterFacts closes the writer on an error path and merges the
// close facts (Rust live.rs close_writer_facts).
func CloseWriterFacts(writer *iprangedb.LiveWriter, failure *rpc.HandlerError) *rpc.HandlerError {
	if closeFacts, err := CloseWriter(writer); err == nil {
		return mergeWriterFacts(failure, closeFacts, nil)
	}
	return failure
}

// WorkflowKindName maps the SDK workflow kind to its wire name.
func WorkflowKindName(kind iprangedb.WorkflowKind) string {
	switch kind {
	case iprangedb.WorkflowCreateFeed:
		return "create_feed"
	case iprangedb.WorkflowReplaceFeed:
		return "replace_feed"
	case iprangedb.WorkflowDirectReplacement:
		return "direct_replacement"
	case iprangedb.WorkflowFirstSeenRefresh:
		return "first_seen_refresh"
	case iprangedb.WorkflowLastSeenRefresh:
		return "last_seen_refresh"
	case iprangedb.WorkflowMembershipImport:
		return "membership_import"
	}
	return "unknown"
}

// LogicalChangeName maps the SDK logical change to its wire name.
func LogicalChangeName(change iprangedb.LogicalChange) string {
	switch change {
	case iprangedb.LogicalChanged:
		return "changed"
	case iprangedb.LogicalNoChange:
		return "unchanged"
	}
	return "unchanged"
}

// WorkflowReportJSON converts the SDK workflow report to its wire
// object (every count is an exact decimal string).
func WorkflowReportJSON(report *iprangedb.WorkflowReport) map[string]any {
	return map[string]any{
		"workflow":                         WorkflowKindName(report.Workflow),
		"logical_change":                   LogicalChangeName(report.LogicalChange),
		"input_record_count":               DecimalUint(report.InputRecordCount),
		"input_normalized_interval_count":  DecimalUint(report.InputNormalizedIntervalCount),
		"before_range_record_count":        DecimalUint(report.BeforeRangeRecordCount),
		"after_range_record_count":         DecimalUint(report.AfterRangeRecordCount),
		"input_addresses":                  report.InputAddresses.String(),
		"before_addresses":                 report.BeforeAddresses.String(),
		"after_addresses":                  report.AfterAddresses.String(),
		"unchanged_value_addresses":        report.UnchangedValueAddresses.String(),
		"changed_value_addresses":          report.ChangedValueAddresses.String(),
		"added_addresses":                  report.AddedAddresses.String(),
		"removed_addresses":                report.RemovedAddresses.String(),
		"source_feed_count":                DecimalUint(report.SourceFeedCount),
		"matched_feed_count":               DecimalUint(report.MatchedFeedCount),
		"created_feed_count":               DecimalUint(report.CreatedFeedCount),
		"source_distinct_membership_count": DecimalUint(report.SourceDistinctMembershipCount),
		"translated_membership_count":      DecimalUint(report.TranslatedMembershipCount),
	}
}
