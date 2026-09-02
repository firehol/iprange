// `iprange.v1.snapshot`: compact unsigned immutable snapshot (Rust
// handlers/snapshot.rs parity). One pinned v4 generation is copied
// into a fresh published output under the caller budget through the
// public SDK worker path; the CLI process never scans database pages
// (spec snapshot, validate, recovery, and resolution).

package handlers

import (
	"encoding/json"
	"strconv"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ValidateSnapshotParams enforces the strict snapshot schema (v4/cli/
// schema/methods.py): source object with path/mode, destination path,
// publication policy, and the three-field snapshot budget.
func ValidateSnapshotParams(params json.RawMessage) error {
	object, err := exactObject(params, "source", "destination", "publication_policy", "snapshot_budget")
	if err != nil {
		return err
	}
	source, err := memberObject(object, "source")
	if err != nil {
		return err
	}
	if err := exactObjectRaw(source, "path", "mode"); err != nil {
		return err
	}
	sourcePath, err := asString(source, "path")
	if err != nil {
		return err
	}
	if err := validatePath(sourcePath); err != nil {
		return err
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return err
	}
	if mode != "immutable" && mode != "live" {
		return errSnapSourceMode
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return err
	}
	if err := validatePath(destination); err != nil {
		return err
	}
	policy, err := asString(object, "publication_policy")
	if err != nil {
		return err
	}
	if !validPublicationPolicyName(policy) {
		return errSnapPolicy
	}
	budget, err := memberObject(object, "snapshot_budget")
	if err != nil {
		return err
	}
	return validateSnapshotBudget(budget)
}

var (
	errSnapSourceMode = errString("source.mode must be immutable or live")
	errSnapPolicy     = errString("publication_policy is invalid")
)

func errString(message string) error { return errStringValue{message} }

type errStringValue struct{ message string }

func (e errStringValue) Error() string { return e.message }

// validateSnapshotBudget enforces max_heap_bytes and
// max_output_pages as positive decimals and max_open_files as a
// positive u32.
func validateSnapshotBudget(budget rawObject) error {
	if err := exactObjectRaw(budget, "max_heap_bytes", "max_output_pages", "max_open_files"); err != nil {
		return err
	}
	for _, field := range []string{"max_heap_bytes", "max_output_pages"} {
		value, err := asDecimalString(budget, field)
		if err != nil || !positiveDecimal(value) {
			return errUnexpected("snapshot_budget." + field + " must be a positive canonical unsigned decimal string")
		}
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return errUnexpected("snapshot_budget." + field + " must be a positive canonical unsigned decimal string")
		}
	}
	openFiles, err := asUint32(budget, "max_open_files")
	if err != nil || openFiles == 0 {
		return errUnexpected("snapshot_budget.max_open_files must be a positive u32 integer")
	}
	return nil
}

func errUnexpected(message string) error { return errStringValue{message} }

// decodeSnapshotBudget converts the validated wire budget into the
// SDK SnapshotBudget.
func decodeSnapshotBudget(object rawObject) (*iprangedb.SnapshotBudget, *rpc.HandlerError) {
	budget, err := memberObject(object, "snapshot_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError("snapshot_budget must be an object")
	}
	heap, err := canonicalU64(budget["max_heap_bytes"])
	if err != nil {
		return nil, rpc.InvalidParamsError("snapshot_budget.max_heap_bytes is invalid")
	}
	outputPages, err := canonicalU64(budget["max_output_pages"])
	if err != nil {
		return nil, rpc.InvalidParamsError("snapshot_budget.max_output_pages is invalid")
	}
	openFiles, err := asUint32(budget, "max_open_files")
	if err != nil {
		return nil, rpc.InvalidParamsError("snapshot_budget.max_open_files must be a u32 integer")
	}
	return &iprangedb.SnapshotBudget{
		MaxHeapBytes:   heap,
		MaxOutputPages: outputPages,
		MaxOpenFiles:   openFiles,
	}, nil
}

// Snapshot implements iprange.v1.snapshot: the complete SnapshotResult
// publication facts on success, and the publication or preparation
// failure facts in the error details on every failing terminal (Rust
// snapshot).
func Snapshot(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	source, err := memberObject(object, "source")
	if err != nil {
		return nil, rpc.InvalidParamsError("source must be an object")
	}
	sourcePath, err := asString(source, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("source.path must be a string")
	}
	sourceModeName, err := asString(source, "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("source.mode must be immutable or live")
	}
	sourceMode := iprangedb.SnapshotSourceImmutable
	if sourceModeName == "live" {
		sourceMode = iprangedb.SnapshotSourceLive
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return nil, rpc.InvalidParamsError("destination must be a string")
	}
	policyName, err := asString(object, "publication_policy")
	if err != nil {
		return nil, rpc.InvalidParamsError("publication_policy is invalid")
	}
	budget, herr := decodeSnapshotBudget(object)
	if herr != nil {
		return nil, herr
	}
	result, err := iprangedb.SnapshotTo(sourcePath, sourceMode, destination, policyByName(policyName), budget, st.Token())
	if err != nil {
		return nil, snapshotPreparationFailure(err)
	}
	return snapshotSuccess(&result)
}

// snapshotSuccess converts one completed snapshot; an SDK publication
// cause becomes a product error that keeps the complete publication
// facts (Rust publication_success).
func snapshotSuccess(result *iprangedb.SnapshotResult) (any, *rpc.HandlerError) {
	publication, convErr := PublicationResultJSON(&result.Publication)
	if convErr != nil {
		return nil, convErr
	}
	if result.Publication.Cause != nil {
		code := sdkCodeOf(result.Publication.Cause)
		return nil, &rpc.HandlerError{
			Code:    code,
			Outcome: publicationStatusOutcome(result.Publication.Publication),
			Message: "snapshot publication failed: " + result.Publication.Cause.Error(),
			Details: map[string]any{"publication": publication},
		}
	}
	return boundedResult(map[string]any{
		"method":      "iprange.v1.snapshot",
		"publication": publication,
	})
}

// snapshotPreparationFailure converts one SnapshotPreparationFailure:
// the attempt never completed a durable publication, so the outcome is
// not_started and the discarded-attempt and residue facts stay in the
// details (Rust publication_failure).
func snapshotPreparationFailure(err error) *rpc.HandlerError {
	failure, ok := err.(*iprangedb.SnapshotPreparationFailure)
	if !ok {
		return SDKError(err, "not_started")
	}
	code := sdkCodeOf(failure.Cause)
	if typed, ok := failure.Cause.(*iprangedb.Error); ok {
		code = publicationCode(typed.Code)
	}
	// The Go SDK collapses the Rust preparation terminal to the primary
	// cause and the cleanup state of the private attempt artifact (the
	// AlgebraPreparationFailure precedent); those are the factual fields
	// this adapter can report.
	return &rpc.HandlerError{
		Code:    code,
		Outcome: "not_started",
		Message: "snapshot preparation failed: " + failure.Cause.Error(),
		Details: map[string]any{"cleanup_state": cleanupStateName(failure.Cleanup)},
	}
}

// publicationCode maps the publication-specific SDK error classes to
// their stable adapter codes (Rust snapshot.rs publication_code);
// every other class keeps the shared reader mapping.
func publicationCode(code iprangedb.ErrorCode) string {
	switch code {
	case iprangedb.ErrorPublicationUnsupported:
		return "publication_unsupported"
	case iprangedb.ErrorOSUnsupported:
		return "os_unsupported"
	case iprangedb.ErrorDurabilityUnsupported:
		return "durability_unsupported"
	case iprangedb.ErrorAccessPolicyUnsupported:
		return "access_policy_unsupported"
	case iprangedb.ErrorSnapshotPreparationFailed:
		return "snapshot_preparation_failed"
	case iprangedb.ErrorLiveCoordinationUnsupported:
		return "live_coordination_unsupported"
	}
	return sdkCode(code)
}

// RegisterSnapshot installs the snapshot handler family.
func RegisterSnapshot() {
	rpc.Register("iprange.v1.snapshot", ValidateSnapshotParams, Snapshot)
}
