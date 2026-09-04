// Live lifecycle, resolution-attempt, and live-mutation handlers
// (Rust handlers/live.rs parity, iprange-jsonrpc-v1.md). Owns one spec
// family: `database.*` live lifecycle and resolution methods,
// `commit.resolve`, `direct.replace`, and the two retention refreshes.
// Mutations open one clean writer, run one public high-level workflow,
// stage the requested metadata in that draft, commit when changed, and
// return the complete workflow, commit, and close facts. Input failure
// aborts the draft and never fabricates a commit.
//
// The retention refreshes additionally open one ephemeral reader over
// the caller-supplied current coverage source and stream one named
// membership feed into the refresh draft; the reader is closed before
// the draft finishes. first_seen.refresh writes an exact removal log
// to a same-directory private file and publishes it only after the
// commit is factually known to have committed.

package handlers

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// RegisterLive installs the live lifecycle, resolution, direct
// replacement, and retention handler families (called by the lead from
// register.go).
func RegisterLive() {
	rpc.Register("iprange.v1.database.initialize_live", ValidateDatabaseInitializeLive, DatabaseInitializeLive)
	rpc.Register("iprange.v1.database.reset_live", ValidateDatabaseResetLive, DatabaseResetLive)
	rpc.Register("iprange.v1.database.create.resolve", ValidateDatabaseCreateResolve, DatabaseCreateResolve)
	rpc.Register("iprange.v1.database.live_transition.resolve", ValidateDatabaseLiveTransitionResolve, DatabaseLiveTransitionResolve)
	rpc.Register("iprange.v1.database.live_residue.resolve", ValidateDatabaseLiveResidueResolve, DatabaseLiveResidueResolve)
	rpc.Register("iprange.v1.commit.resolve", ValidateCommitResolve, CommitResolve)
	rpc.Register("iprange.v1.direct.replace", ValidateDirectReplace, DirectReplace)
	rpc.Register("iprange.v1.retention.first_seen.refresh", ValidateFirstSeenRefresh, FirstSeenRefresh)
	rpc.Register("iprange.v1.retention.last_seen.refresh", ValidateLastSeenRefresh, LastSeenRefresh)
}

// ---------------------------------------------------------------------------
// Shared live-family helpers.
// ---------------------------------------------------------------------------

// rangeBatchCapacity is the streaming input batch size of the live
// workflows (Rust live.rs CSV_BATCH_CAPACITY).
const rangeBatchCapacity = 256

// requireExistingDatabase verifies that the mutation target exists and
// is a regular file (Rust live.rs require_existing_database).
func requireExistingDatabase(path string) *rpc.HandlerError {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		return nil
	case err == nil:
		return rpc.NewHandlerError("invalid_path", "not_started",
			"database is not a regular file: "+path)
	case errors.Is(err, os.ErrNotExist):
		return rpc.NewHandlerError("invalid_path", "not_started",
			"database does not exist: "+path)
	default:
		return rpc.NewHandlerError("io", "not_started",
			"inspect database "+path+": "+err.Error())
	}
}

// csvFailure is one classified direct-CSV input failure (Rust
// CsvFailure): invalid_path, io, or input_format, all before any
// durable attempt.
func csvFailure(code, message string) *rpc.HandlerError {
	return rpc.NewHandlerError(code, "not_started", message)
}

// fileError maps one removal-output file failure (Rust live.rs
// file_error).
func fileError(err error, operation string) *rpc.HandlerError {
	message := fmt.Sprintf("%s: %v", operation, err)
	if errors.Is(err, os.ErrExist) {
		return rpc.NewHandlerError("name_exists", "not_started", message)
	}
	return rpc.NewHandlerError("io", "not_started", message)
}

// ---------------------------------------------------------------------------
// Param validators.
// ---------------------------------------------------------------------------

// ValidateDatabaseInitializeLive enforces {path, reader_capacity}.
func ValidateDatabaseInitializeLive(params json.RawMessage) error {
	object, err := exactObject(params, "path", "reader_capacity")
	if err != nil {
		return err
	}
	if err := validatePathFrom(object, "path"); err != nil {
		return err
	}
	_, err = asUint32(object, "reader_capacity")
	return err
}

// ValidateDatabaseResetLive enforces {path, reader_capacity, policy}.
func ValidateDatabaseResetLive(params json.RawMessage) error {
	object, err := exactObject(params, "path", "reader_capacity", "policy")
	if err != nil {
		return err
	}
	if err := validatePathFrom(object, "path"); err != nil {
		return err
	}
	if _, err := asUint32(object, "reader_capacity"); err != nil {
		return err
	}
	policy, err := asString(object, "policy")
	if err != nil {
		return err
	}
	if policy != "rollback_safe" && policy != "discard_previous" {
		return fmt.Errorf("policy must be rollback_safe or discard_previous")
	}
	return nil
}

// ValidateDatabaseCreateResolve enforces {path, create_result,
// resolution_mode}.
func ValidateDatabaseCreateResolve(params json.RawMessage) error {
	object, err := exactObject(params, "path", "create_result", "resolution_mode")
	if err != nil {
		return err
	}
	if err := validatePathFrom(object, "path"); err != nil {
		return err
	}
	if _, err := memberObject(object, "create_result"); err != nil {
		return fmt.Errorf("create_result must be an object")
	}
	return validateResolutionMode(object)
}

// ValidateDatabaseLiveTransitionResolve enforces {path,
// live_transition_result, resolution_mode}.
func ValidateDatabaseLiveTransitionResolve(params json.RawMessage) error {
	object, err := exactObject(params, "path", "live_transition_result", "resolution_mode")
	if err != nil {
		return err
	}
	if err := validatePathFrom(object, "path"); err != nil {
		return err
	}
	if _, err := memberObject(object, "live_transition_result"); err != nil {
		return fmt.Errorf("live_transition_result must be an object")
	}
	return validateResolutionMode(object)
}

// ValidateDatabaseLiveResidueResolve enforces {path, resolution_mode}.
func ValidateDatabaseLiveResidueResolve(params json.RawMessage) error {
	object, err := exactObject(params, "path", "resolution_mode")
	if err != nil {
		return err
	}
	if err := validatePathFrom(object, "path"); err != nil {
		return err
	}
	return validateResolutionMode(object)
}

// ValidateCommitResolve enforces {path, commit_result, mode}.
func ValidateCommitResolve(params json.RawMessage) error {
	object, err := exactObject(params, "path", "commit_result", "mode")
	if err != nil {
		return err
	}
	if err := validatePathFrom(object, "path"); err != nil {
		return err
	}
	if _, err := memberObject(object, "commit_result"); err != nil {
		return fmt.Errorf("commit_result must be an object")
	}
	mode, err := asString(object, "mode")
	if err != nil {
		return err
	}
	if mode != "live" && mode != "immutable" {
		return fmt.Errorf("mode must be live or immutable")
	}
	return nil
}

// ValidateDirectReplace enforces {path, input, metadata,
// writer_budget} with the direct CSV descriptor.
func ValidateDirectReplace(params json.RawMessage) error {
	_, herr := decodeDirectReplaceParams(params)
	if herr != nil {
		return errors.New(herr.Message)
	}
	return nil
}

// ValidateFirstSeenRefresh enforces the first_seen refresh schema with
// the optional removals_output.
func ValidateFirstSeenRefresh(params json.RawMessage) error {
	_, herr := decodeRefreshParams(params, false)
	if herr != nil {
		return errors.New(herr.Message)
	}
	return nil
}

// ValidateLastSeenRefresh enforces the last_seen refresh schema.
func ValidateLastSeenRefresh(params json.RawMessage) error {
	_, herr := decodeRefreshParams(params, true)
	if herr != nil {
		return errors.New(herr.Message)
	}
	return nil
}

// validatePathFrom validates one path-valued member.
func validatePathFrom(object rawObject, field string) error {
	path, err := asString(object, field)
	if err != nil {
		return fmt.Errorf("%s must be a string", field)
	}
	return validatePath(path)
}

// validateResolutionMode enforces the shared resolution_mode member.
func validateResolutionMode(object rawObject) error {
	mode, err := asString(object, "resolution_mode")
	if err != nil {
		return err
	}
	if mode != "complete" && mode != "rollback" {
		return fmt.Errorf("resolution_mode must be complete or rollback")
	}
	return nil
}

// resolutionModeFromObject decodes the shared resolution_mode member.
func resolutionModeFromObject(object rawObject) (iprangedb.LiveTransitionResolutionMode, *rpc.HandlerError) {
	mode, err := asString(object, "resolution_mode")
	if err != nil {
		return 0, rpc.InvalidParamsError("resolution_mode must be complete or rollback")
	}
	switch mode {
	case "complete":
		return iprangedb.LiveTransitionResolutionComplete, nil
	case "rollback":
		return iprangedb.LiveTransitionResolutionRollback, nil
	}
	return 0, rpc.InvalidParamsError("resolution_mode must be complete or rollback")
}

// ---------------------------------------------------------------------------
// Live lifecycle and resolution-attempt handlers.
// ---------------------------------------------------------------------------

// DatabaseInitializeLive converts one quiescent immutable database
// into a live database and returns the complete transition facts.
func DatabaseInitializeLive(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "path", "reader_capacity")
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	capacity, err := asUint32(object, "reader_capacity")
	if err != nil {
		return nil, rpc.InvalidParamsError("reader_capacity must be a u32 integer")
	}
	if herr := requireExistingDatabase(path); herr != nil {
		return nil, herr
	}
	result, err := iprangedb.InitializeLive(path, capacity, st.Token())
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	value := LiveTransitionResultJSON(&result)
	value["method"] = "iprange.v1.database.initialize_live"
	return boundedResult(value)
}

// DatabaseResetLive replaces missing, corrupt, or obsolete live
// coordination while the main is quiescent and returns the complete
// transition facts.
func DatabaseResetLive(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "path", "reader_capacity", "policy")
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	capacity, err := asUint32(object, "reader_capacity")
	if err != nil {
		return nil, rpc.InvalidParamsError("reader_capacity must be a u32 integer")
	}
	policy := iprangedb.LiveResetDiscardPrevious
	if text, err := asString(object, "policy"); err == nil && text == "rollback_safe" {
		policy = iprangedb.LiveResetRollbackSafe
	}
	if herr := requireExistingDatabase(path); herr != nil {
		return nil, herr
	}
	result, err := iprangedb.ResetLiveCoordination(path, capacity, policy, st.Token())
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	value := LiveTransitionResultJSON(&result)
	value["method"] = "iprange.v1.database.reset_live"
	return boundedResult(value)
}

// DatabaseCreateResolve resolves only the exact creation attempt
// identified by the supplied create_result.
func DatabaseCreateResolve(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "path", "create_result", "resolution_mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingDatabase(path); herr != nil {
		return nil, herr
	}
	createObject, err := memberObject(object, "create_result")
	if err != nil {
		return nil, rpc.InvalidParamsError("create_result must be an object")
	}
	supplied, herr := CreateResultFromWire(createObject, path)
	if herr != nil {
		return nil, herr
	}
	mode, herr := resolutionModeFromObject(object)
	if herr != nil {
		return nil, herr
	}
	result, err := iprangedb.ResolveCreateLive(path, *supplied, mode, st.Token())
	if err != nil {
		return nil, SDKError(err, "outcome_unknown")
	}
	value, herr := CreateResultJSON(&result)
	if herr != nil {
		return nil, herr
	}
	value["method"] = "iprange.v1.database.create.resolve"
	return boundedResult(value)
}

// DatabaseLiveTransitionResolve resolves only the exact transition
// identified by the supplied live_transition_result.
func DatabaseLiveTransitionResolve(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "path", "live_transition_result", "resolution_mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingDatabase(path); herr != nil {
		return nil, herr
	}
	transitionObject, err := memberObject(object, "live_transition_result")
	if err != nil {
		return nil, rpc.InvalidParamsError("live_transition_result must be an object")
	}
	supplied, herr := LiveTransitionResultFromWire(transitionObject, path)
	if herr != nil {
		return nil, herr
	}
	mode, herr := resolutionModeFromObject(object)
	if herr != nil {
		return nil, herr
	}
	result, err := iprangedb.ResolveLiveTransition(path, *supplied, mode, st.Token())
	if err != nil {
		return nil, SDKError(err, "outcome_unknown")
	}
	value := LiveTransitionResultJSON(&result)
	value["method"] = "iprange.v1.database.live_transition.resolve"
	return boundedResult(value)
}

// DatabaseLiveResidueResolve resolves one interrupted canonical
// create/initialize or private reset without the lost in-memory
// result. The residue resolver may recover a transition whose main is
// already gone, so no existence pre-check applies here.
func DatabaseLiveResidueResolve(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "path", "resolution_mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	mode, herr := resolutionModeFromObject(object)
	if herr != nil {
		return nil, herr
	}
	result, err := iprangedb.ResolveInterruptedLiveTransition(path, mode, st.Token())
	if err != nil {
		return nil, SDKError(err, "outcome_unknown")
	}
	value := LiveResidueResultJSON(&result)
	value["method"] = "iprange.v1.database.live_residue.resolve"
	return boundedResult(value)
}

// CommitResolve proves one exact attempted transaction and nonce
// against the two meta pages of a database file.
func CommitResolve(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "path", "commit_result", "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingDatabase(path); herr != nil {
		return nil, herr
	}
	commitObject, err := memberObject(object, "commit_result")
	if err != nil {
		return nil, rpc.InvalidParamsError("commit_result must be an object")
	}
	supplied, herr := CommitResultFromWire(commitObject)
	if herr != nil {
		return nil, herr
	}
	mode := iprangedb.CommitResolutionModeImmutable
	if text, err := asString(object, "mode"); err == nil && text == "live" {
		mode = iprangedb.CommitResolutionModeLive
	}
	result, err := iprangedb.ResolveCommit(path, *supplied, mode, st.Token())
	if err != nil {
		return nil, SDKError(err, "outcome_unknown")
	}
	value := CommitResolutionResultJSON(result)
	value["method"] = "iprange.v1.commit.resolve"
	return boundedResult(value)
}

// ---------------------------------------------------------------------------
// Live mutation handlers.
// ---------------------------------------------------------------------------

// directReplaceParams is the decoded direct.replace contract.
type directReplaceParams struct {
	path         string
	inputPath    string
	maxLineBytes int
	metadata     MetadataValue
	budget       iprangedb.PageBudget
}

// decodeDirectReplaceParams decodes and strictly validates the
// direct.replace params.
func decodeDirectReplaceParams(params json.RawMessage) (*directReplaceParams, *rpc.HandlerError) {
	decoded := &directReplaceParams{}
	object, err := exactObject(params, "path", "input", "metadata", "writer_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil || validatePath(path) != nil {
		return nil, rpc.InvalidParamsError("path is invalid")
	}
	decoded.path = path
	input, err := memberObject(object, "input")
	if err != nil {
		return nil, rpc.InvalidParamsError("input must be an object")
	}
	if err := exactObjectRaw(input, "path", "max_line_bytes"); err != nil {
		return nil, rpc.InvalidParamsError("input is invalid")
	}
	inputPath, err := asString(input, "path")
	if err != nil || validatePath(inputPath) != nil {
		return nil, rpc.InvalidParamsError("input.path is invalid")
	}
	decoded.inputPath = inputPath
	maxLineBytes, err := asUint32(input, "max_line_bytes")
	if err != nil || maxLineBytes < 1 || maxLineBytes > 1_048_576 {
		return nil, rpc.InvalidParamsError("max_line_bytes must be 1 through 1048576")
	}
	decoded.maxLineBytes = int(maxLineBytes)
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

// DirectReplace runs one complete direct-replacement workflow from the
// direct CSV input descriptor.
func DirectReplace(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	decoded, herr := decodeDirectReplaceParams(params)
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
	info, err := writer.Info()
	if err != nil {
		return nil, CloseWriterFacts(writer, SDKError(err, "not_started"))
	}
	workflow, err := writer.BeginDirectReplacement(st.Token())
	if err != nil {
		return nil, CloseWriterFacts(writer, SDKError(err, "not_started"))
	}
	ipv6 := info.Family == iprangedb.AddressFamilyIPv6
	source, herr := openDirectCsv(decoded.inputPath, decoded.maxLineBytes, ipv6)
	if herr != nil {
		return nil, CloseWriterFacts(writer, herr)
	}
	if ipv6 {
		if herr := source.drainV6(workflow); herr != nil {
			return nil, CloseWriterFacts(writer, herr)
		}
	} else {
		if herr := source.drainV4(workflow); herr != nil {
			return nil, CloseWriterFacts(writer, herr)
		}
	}
	finished, err := workflow.FinishInput()
	if err != nil {
		return nil, CloseWriterFacts(writer, SDKError(err, "not_started"))
	}
	report := WorkflowReportJSON(finishedReport(finished))
	return publishLiveFacts(writer, finished, &decoded.metadata, st.Token(), "iprange.v1.direct.replace", report, nil)
}

// finishedReport returns the workflow report pointer of one finished
// workflow for the shared report conversion.
func finishedReport(finished *iprangedb.FinishedWorkflow) *iprangedb.WorkflowReport {
	report := finished.Report()
	return &report
}

// publishLiveFacts stages the requested metadata inside the finished
// workflow (Rust live.rs consume_finished + publisher_value): a
// changed draft commits with the metadata staged; a no-change draft
// commits the metadata through one fresh direct transaction; then the
// writer is closed and the complete workflow/commit/close facts are
// assembled. removals, when non-nil, places the unpublished removal
// facts inside every error details; the caller publishes or discards
// the collector afterwards.
func publishLiveFacts(writer *iprangedb.LiveWriter, finished *iprangedb.FinishedWorkflow, metadata *MetadataValue, token *iprangedb.CancellationToken, method string, report map[string]any, removals *removalCollector) (any, *rpc.HandlerError) {
	value, herr := publishLiveFactsInner(writer, finished, metadata, token, method, report)
	if herr != nil && removals != nil {
		herr = mergeDetailsMember(herr, "removals", removals.unpublishedFacts())
	}
	return value, herr
}

func publishLiveFactsInner(writer *iprangedb.LiveWriter, finished *iprangedb.FinishedWorkflow, metadata *MetadataValue, token *iprangedb.CancellationToken, method string, report map[string]any) (any, *rpc.HandlerError) {
	if !finished.IsChanged() {
		metadataLogicalChange, commit, herr := publishNoChangeDirect(writer, metadata, token)
		if herr != nil {
			return nil, FinishWriterError(writer, herr, report)
		}
		return FinishPublisher(writer, method, report, metadataLogicalChange, commit, nil)
	}
	metadataLogicalChange, commit, _, perr := PublishChanged(finished, metadata)
	if perr != nil {
		return nil, FinishWriterError(writer, perr, report)
	}
	return FinishPublisher(writer, method, report, metadataLogicalChange, &commit, nil)
}

// publishNoChangeDirect commits only the requested metadata through
// one fresh direct transaction (Rust live.rs publisher_value no-change
// arm). Direct databases cannot use the membership transaction of
// PublishNoChange; replacements always commit, clear commits only when
// metadata was present, keep commits nothing.
func publishNoChangeDirect(writer *iprangedb.LiveWriter, metadata *MetadataValue, token *iprangedb.CancellationToken) (string, *iprangedb.CommitResult, *rpc.HandlerError) {
	if metadata.Keep {
		return "unchanged", nil, nil
	}
	transaction, err := writer.BeginDirect(token)
	if err != nil {
		return "", nil, SDKError(err, "not_started")
	}
	if metadata.Clear {
		changed, err := transaction.ClearMetadataJSON()
		if err != nil {
			_, _ = transaction.Abort()
			return "", nil, SDKError(err, "not_started")
		}
		if !changed {
			_, _ = transaction.Abort()
			return "unchanged", nil, nil
		}
		commit, err := transaction.Commit()
		if err != nil {
			return "changed", &iprangedb.CommitResult{}, nil
		}
		return "changed", &commit, nil
	}
	if _, err := transaction.SetMetadataJSON(metadata.Bytes); err != nil {
		_, _ = transaction.Abort()
		return "", nil, SDKError(err, "not_started")
	}
	commit, err := transaction.Commit()
	if err != nil {
		return "changed", &iprangedb.CommitResult{}, nil
	}
	return "changed", &commit, nil
}

// mergeDetailsMember inserts one named member into an error's details,
// preserving every existing member.
func mergeDetailsMember(failure *rpc.HandlerError, name string, member any) *rpc.HandlerError {
	details := map[string]any{}
	if failure.Details != nil {
		if existing, ok := failure.Details.(map[string]any); ok {
			details = existing
		}
	}
	details[name] = member
	failure.Details = details
	return failure
}

// ---------------------------------------------------------------------------
// Direct CSV input source (bounded-batch streaming, Rust
// DirectCsvSource parity).
// ---------------------------------------------------------------------------

// directRecord is one parsed from,to,value CSV row before the
// family-specific conversion.
type directRecord struct {
	from  netip.Addr
	to    netip.Addr
	value uint32
}

// directCsvSource is the streaming `from,to,value` CSV reader for
// direct.replace. One bounded line and one bounded batch are retained;
// rows may be unordered, duplicate, or overlapping, exactly as the
// direct-replacement workflow requires.
type directCsvSource struct {
	reader       *bufio.Reader
	maxLineBytes int
	ipv6         bool
	line         []byte
	finished     bool
}

// openDirectCsv opens the input file and verifies the exact header.
func openDirectCsv(path string, maxLineBytes int, ipv6 bool) (*directCsvSource, *rpc.HandlerError) {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
	case err == nil:
		return nil, csvFailure("invalid_path", fmt.Sprintf("direct CSV input is not a regular file: %s", path))
	case errors.Is(err, os.ErrNotExist):
		return nil, csvFailure("invalid_path", fmt.Sprintf("direct CSV input does not exist: %s", path))
	default:
		return nil, csvFailure("io", fmt.Sprintf("inspect direct CSV input %s: %v", path, err))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, csvFailure("io", fmt.Sprintf("open direct CSV input %s: %v", path, err))
	}
	source := &directCsvSource{
		reader:       bufio.NewReader(file),
		maxLineBytes: maxLineBytes,
		ipv6:         ipv6,
		line:         make([]byte, 0, 256),
	}
	for {
		line, ok, herr := source.readLine()
		if herr != nil {
			return nil, herr
		}
		if !ok {
			return nil, csvFailure("input_format", "direct CSV input is empty")
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		if trimmed != "from,to,value" {
			return nil, csvFailure("input_format",
				fmt.Sprintf("direct CSV header must be exactly 'from,to,value', found %q", trimmed))
		}
		return source, nil
	}
}

// readLine fills the one reusable line buffer with the next physical
// row; ok is false at end of input. The max_line_bytes budget bounds
// the complete line before parsing.
func (s *directCsvSource) readLine() ([]byte, bool, *rpc.HandlerError) {
	s.line = s.line[:0]
	for {
		chunk, err := s.reader.ReadSlice('\n')
		s.line = append(s.line, chunk...)
		if len(s.line) > s.maxLineBytes+1 {
			return nil, false, csvFailure("input_format",
				fmt.Sprintf("direct CSV line exceeds max_line_bytes %d", s.maxLineBytes))
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			if len(s.line) == 0 {
				return nil, false, nil
			}
			break
		}
		if err != nil {
			return nil, false, csvFailure("io", fmt.Sprintf("read direct CSV input: %v", err))
		}
		break
	}
	if !utf8.Valid(s.line) {
		return nil, false, csvFailure("input_format", "direct CSV input is not valid UTF-8")
	}
	return s.line, true, nil
}

// nextBatch returns the next bounded batch of parsed rows, or nil at
// end of input; every parse failure is terminal.
func (s *directCsvSource) nextBatch() ([]directRecord, *rpc.HandlerError) {
	if s.finished {
		return nil, nil
	}
	batch := make([]directRecord, 0, rangeBatchCapacity)
	for len(batch) < rangeBatchCapacity {
		line, ok, herr := s.readLine()
		if herr != nil {
			s.finished = true
			return nil, herr
		}
		if !ok {
			s.finished = true
			break
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		record, herr := parseDirectRecord(trimmed, s.ipv6)
		if herr != nil {
			s.finished = true
			return nil, herr
		}
		batch = append(batch, record)
	}
	if len(batch) == 0 {
		return nil, nil
	}
	return batch, nil
}

// parseDirectRecord parses one `from,to,value` CSV row (Rust
// parse_record).
func parseDirectRecord(line string, ipv6 bool) (directRecord, *rpc.HandlerError) {
	columns := strings.Split(line, ",")
	if len(columns) != 3 {
		return directRecord{}, csvFailure("input_format", "direct CSV row must have exactly 3 columns: from,to,value")
	}
	from, herr := parseCsvAddress(strings.TrimSpace(columns[0]), ipv6)
	if herr != nil {
		return directRecord{}, herr
	}
	to, herr := parseCsvAddress(strings.TrimSpace(columns[1]), ipv6)
	if herr != nil {
		return directRecord{}, herr
	}
	if from.Compare(to) > 0 {
		return directRecord{}, csvFailure("input_format", "range start exceeds range end")
	}
	value, err := strconv.ParseUint(strings.TrimSpace(columns[2]), 10, 32)
	if err != nil {
		return directRecord{}, csvFailure("input_format", "value must be unsigned decimal 0 through 4294967295")
	}
	return directRecord{from: from, to: to, value: uint32(value)}, nil
}

// parseCsvAddress parses one canonical address of the database family
// (Rust parse_ipv4/parse_ipv6: both std parsers require canonical
// text, so netip is the exact Go counterpart).
func parseCsvAddress(text string, ipv6 bool) (netip.Addr, *rpc.HandlerError) {
	address, err := netip.ParseAddr(text)
	if err != nil {
		if ipv6 {
			return netip.Addr{}, csvFailure("input_format", "invalid IPv6 address: "+text)
		}
		return netip.Addr{}, csvFailure("input_format", "invalid IPv4 address: "+text)
	}
	if ipv6 {
		if !address.Is6() {
			return netip.Addr{}, csvFailure("input_format", "invalid IPv6 address: "+text)
		}
		return address, nil
	}
	if !address.Is4() {
		return netip.Addr{}, csvFailure("input_format", "invalid IPv4 address: "+text)
	}
	return address, nil
}

// drainV4 streams every parsed row into the IPv4 replacement draft.
func (s *directCsvSource) drainV4(workflow *iprangedb.DirectReplacement) *rpc.HandlerError {
	for {
		batch, herr := s.nextBatch()
		if herr != nil {
			return herr
		}
		if batch == nil {
			return nil
		}
		ranges := make([]iprangedb.DirectRangeV4, len(batch))
		for i, record := range batch {
			ranges[i] = iprangedb.DirectRangeV4{
				From:  ipv4Uint(record.from),
				To:    ipv4Uint(record.to),
				Value: record.value,
			}
		}
		if err := workflow.AddRangesV4(ranges); err != nil {
			return SDKError(err, "not_started")
		}
	}
}

// drainV6 streams every parsed row into the IPv6 replacement draft.
func (s *directCsvSource) drainV6(workflow *iprangedb.DirectReplacement) *rpc.HandlerError {
	for {
		batch, herr := s.nextBatch()
		if herr != nil {
			return herr
		}
		if batch == nil {
			return nil
		}
		ranges := make([]iprangedb.DirectRangeV6, len(batch))
		for i, record := range batch {
			from := ipv6Halves(record.from)
			to := ipv6Halves(record.to)
			ranges[i] = iprangedb.DirectRangeV6{
				FromHi: from.hi,
				FromLo: from.lo,
				ToHi:   to.hi,
				ToLo:   to.lo,
				Value:  record.value,
			}
		}
		if err := workflow.AddRangesV6(ranges); err != nil {
			return SDKError(err, "not_started")
		}
	}
}

// ipv4Uint converts one parsed IPv4 address to its numeric form.
func ipv4Uint(address netip.Addr) uint32 {
	bytes := address.As4()
	return uint32(bytes[0])<<24 | uint32(bytes[1])<<16 | uint32(bytes[2])<<8 | uint32(bytes[3])
}

// ipv6Halves converts one parsed IPv6 address to its numeric halves.
func ipv6Halves(address netip.Addr) struct{ hi, lo uint64 } {
	bytes := address.As16()
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(bytes[i])
		lo = lo<<8 | uint64(bytes[8+i])
	}
	return struct{ hi, lo uint64 }{hi: hi, lo: lo}
}

// ---------------------------------------------------------------------------
// Retention refreshes.
// ---------------------------------------------------------------------------

// refreshParams is the decoded retention refresh contract.
type refreshParams struct {
	path         string
	sourcePath   string
	sourceMode   string
	feed         string
	refreshValue uint32
	cutoff       uint32 // last_seen only
	removals     *removalsSettings
	metadata     MetadataValue
	budget       iprangedb.PageBudget
}

// decodeRefreshParams decodes and strictly validates the retention
// refresh params; lastSeen selects the last_seen schema.
func decodeRefreshParams(params json.RawMessage, lastSeen bool) (*refreshParams, *rpc.HandlerError) {
	decoded := &refreshParams{}
	var required []string
	var optional []string
	if lastSeen {
		required = []string{"path", "current", "refresh_value", "cutoff", "metadata", "writer_budget"}
	} else {
		required = []string{"path", "current", "refresh_value", "metadata", "writer_budget"}
		optional = []string{"removals_output"}
	}
	object, err := exactObjectOpt(params, required, optional)
	if err != nil {
		return nil, rpc.InvalidParamsError("params are invalid")
	}
	path, err := asString(object, "path")
	if err != nil || validatePath(path) != nil {
		return nil, rpc.InvalidParamsError("path is invalid")
	}
	decoded.path = path
	current, err := memberObject(object, "current")
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
	decoded.feed = feed
	refreshValue, err := asUint32(object, "refresh_value")
	if err != nil {
		return nil, rpc.InvalidParamsError("refresh_value must be a u32 integer")
	}
	decoded.refreshValue = refreshValue
	if lastSeen {
		cutoff, err := asUint32(object, "cutoff")
		if err != nil {
			return nil, rpc.InvalidParamsError("cutoff must be a u32 integer")
		}
		decoded.cutoff = cutoff
	} else if raw, ok := object["removals_output"]; ok {
		output, err := decodeObject(raw)
		if err != nil {
			return nil, rpc.InvalidParamsError("removals_output must be an object")
		}
		settings, herr := decodeRemovalsSettings(output)
		if herr != nil {
			return nil, herr
		}
		decoded.removals = settings
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

// decodeSource decodes one {path, mode} database-source member.
func decodeSource(object rawObject, label string) (string, string, *rpc.HandlerError) {
	if err := exactObjectRaw(object, "path", "mode"); err != nil {
		return "", "", rpc.InvalidParamsError(label + " must be an object")
	}
	path, err := asString(object, "path")
	if err != nil || validatePath(path) != nil {
		return "", "", rpc.InvalidParamsError(label + ".path is invalid")
	}
	mode, err := asString(object, "mode")
	if err != nil || (mode != "immutable" && mode != "live") {
		return "", "", rpc.InvalidParamsError(label + ".mode must be immutable or live")
	}
	return path, mode, nil
}

// FirstSeenRefresh runs one complete first-seen refresh over the
// current coverage source with the optional removals output.
func FirstSeenRefresh(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return runRefresh(st, params, false)
}

// LastSeenRefresh runs one complete last-seen refresh over the current
// coverage source.
func LastSeenRefresh(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	return runRefresh(st, params, true)
}

// runRefresh is the shared retention refresh driver (Rust live.rs
// first_seen_refresh/last_seen_refresh).
func runRefresh(st *rpc.SessionState, params json.RawMessage, lastSeen bool) (any, *rpc.HandlerError) {
	decoded, herr := decodeRefreshParams(params, lastSeen)
	if herr != nil {
		return nil, herr
	}
	if herr := requireExistingDatabase(decoded.path); herr != nil {
		return nil, herr
	}
	reader, herr := openDatabaseSource(decoded.sourcePath, decoded.sourceMode, "current coverage source", st.Token())
	if herr != nil {
		return nil, herr
	}
	info, err := readerInfoErr(reader)
	if err != nil {
		return nil, CloseOnError([]*rpc.ReaderValue{reader}, readError(err))
	}
	writer, err := iprangedb.OpenLiveWriter(decoded.path, decoded.budget, st.Token())
	if err != nil {
		return nil, CloseOnError([]*rpc.ReaderValue{reader}, SDKError(err, "not_started"))
	}
	var collector *removalCollector
	if decoded.removals != nil {
		collector, herr = newRemovalCollector(*decoded.removals, decoded.refreshValue)
		if herr != nil {
			return nil, closeRefreshFacts(reader, writer, herr)
		}
	}
	method := "iprange.v1.retention.last_seen.refresh"
	if !lastSeen {
		method = "iprange.v1.retention.first_seen.refresh"
	}
	var value any
	if lastSeen {
		value, herr = runLastSeenRefresh(writer, reader, info.Family, decoded.feed, decoded.refreshValue, decoded.cutoff, &decoded.metadata, st.Token(), method)
	} else {
		value, herr = runFirstSeenRefresh(writer, reader, info.Family, decoded.feed, decoded.refreshValue, collector, &decoded.metadata, st.Token(), method)
	}
	if herr != nil {
		if collector != nil {
			if derr := collector.discard(); derr != nil {
				herr = mergeDetailsMember(herr, "cleanup_failure", map[string]any{"code": derr.Code, "message": derr.Message})
			}
		}
		return nil, herr
	}
	if collector != nil {
		result, ok := value.(map[string]any)
		if !ok {
			return nil, rpc.NewHandlerError("io", "not_started", "internal: refresh result is not an object")
		}
		durability := "not_committed"
		if commit, ok := result["commit"].(map[string]any); ok {
			durability, _ = commit["durability"].(string)
		}
		if durability == "committed" {
			removals, perr := collector.publish()
			if perr != nil {
				details := map[string]any{
					"result":                       result,
					"removals_publication_failure": map[string]any{"code": perr.Code, "message": perr.Message},
				}
				return nil, &rpc.HandlerError{Code: perr.Code, Outcome: "committed",
					Message: "first-seen removals publication failed", Details: details}
			}
			result["removals"] = removals
		} else {
			if derr := collector.discard(); derr != nil {
				return nil, &rpc.HandlerError{Code: derr.Code, Outcome: "not_started",
					Message: derr.Message, Details: map[string]any{"result": result}}
			}
		}
	}
	return boundedResult(value)
}

// runFirstSeenRefresh runs the family-specific first-seen workflow and
// finishes the publisher facts (Rust run_first_seen_v4/v6).
func runFirstSeenRefresh(writer *iprangedb.LiveWriter, reader *rpc.ReaderValue, family iprangedb.AddressFamily, feed string, refreshValue uint32, collector *removalCollector, metadata *MetadataValue, token *iprangedb.CancellationToken, method string) (any, *rpc.HandlerError) {
	refresh, err := writer.BeginFirstSeenRefresh(refreshValue, token)
	if err != nil {
		return nil, closeRefreshFacts(reader, writer, SDKError(err, "not_started"))
	}
	var drainErr *rpc.HandlerError
	if family == iprangedb.AddressFamilyIPv6 {
		drainErr = drainFeedV6(reader, feed, true, refresh.AddRangesV6)
	} else {
		drainErr = drainFeedV4(reader, feed, true, refresh.AddRangesV4)
	}
	if drainErr != nil {
		return nil, closeRefreshFacts(reader, writer, drainErr)
	}
	sourceClose, closeErr := closeEphemeralReader(reader)
	if closeErr != nil {
		return nil, CloseWriterFacts(writer, closeErr)
	}
	var finished *iprangedb.FinishedWorkflow
	if collector != nil {
		if family == iprangedb.AddressFamilyIPv6 {
			finished, err = refresh.FinishInputWithRemovalsV6(collector.sink6())
		} else {
			finished, err = refresh.FinishInputWithRemovalsV4(collector.sink4())
		}
	} else {
		finished, err = refresh.FinishInput()
	}
	if err != nil {
		if collector != nil {
			if violation := collector.takeViolation(); violation != "" {
				failure := rpc.NewHandlerError("output_limit", "not_started", violation)
				return nil, mergeSourceClose(CloseWriterFacts(writer, failure), sourceClose)
			}
		}
		failure := SDKError(err, "not_started")
		return nil, mergeSourceClose(CloseWriterFacts(writer, failure), sourceClose)
	}
	return finishLiveRefresh(writer, finished, metadata, token, method, sourceClose)
}

// runLastSeenRefresh runs the family-specific last-seen workflow and
// finishes the publisher facts (Rust run_last_seen_v4/v6).
func runLastSeenRefresh(writer *iprangedb.LiveWriter, reader *rpc.ReaderValue, family iprangedb.AddressFamily, feed string, refreshValue, cutoff uint32, metadata *MetadataValue, token *iprangedb.CancellationToken, method string) (any, *rpc.HandlerError) {
	refresh, err := writer.BeginLastSeenRefresh(refreshValue, cutoff, token)
	if err != nil {
		return nil, closeRefreshFacts(reader, writer, SDKError(err, "not_started"))
	}
	var drainErr *rpc.HandlerError
	if family == iprangedb.AddressFamilyIPv6 {
		drainErr = drainFeedV6(reader, feed, true, refresh.AddRangesV6)
	} else {
		drainErr = drainFeedV4(reader, feed, true, refresh.AddRangesV4)
	}
	if drainErr != nil {
		return nil, closeRefreshFacts(reader, writer, drainErr)
	}
	sourceClose, closeErr := closeEphemeralReader(reader)
	if closeErr != nil {
		return nil, CloseWriterFacts(writer, closeErr)
	}
	finished, err := refresh.FinishInput()
	if err != nil {
		failure := SDKError(err, "not_started")
		return nil, mergeSourceClose(CloseWriterFacts(writer, failure), sourceClose)
	}
	return finishLiveRefresh(writer, finished, metadata, token, method, sourceClose)
}

// finishLiveRefresh converts the completed refresh workflow into the
// wire result and attaches the factual source close (Rust
// run_first_seen_v4 tail).
func finishLiveRefresh(writer *iprangedb.LiveWriter, finished *iprangedb.FinishedWorkflow, metadata *MetadataValue, token *iprangedb.CancellationToken, method string, sourceClose map[string]any) (any, *rpc.HandlerError) {
	report := WorkflowReportJSON(finishedReport(finished))
	value, herr := publishLiveFacts(writer, finished, metadata, token, method, report, nil)
	value, herr = withSourceClose(value, herr, sourceClose)
	return value, herr
}

// ---------------------------------------------------------------------------
// First-seen removal log collector (Rust RemovalCollector parity).
// ---------------------------------------------------------------------------

// removalsSettings is the validated first-seen removal output
// contract.
type removalsSettings struct {
	destination    string
	policy         iprangedb.PublicationPolicy
	maxRows        uint64
	maxOutputBytes uint64
	maxOpenFiles   uint32
}

// decodeRemovalsSettings decodes the removals_output member.
func decodeRemovalsSettings(object rawObject) (*removalsSettings, *rpc.HandlerError) {
	settings := &removalsSettings{}
	if err := exactObjectRaw(object, "path", "publication_policy", "result_budget"); err != nil {
		return nil, rpc.InvalidParamsError("removals_output is invalid")
	}
	path, err := asString(object, "path")
	if err != nil || validatePath(path) != nil {
		return nil, rpc.InvalidParamsError("removals_output.path is invalid")
	}
	settings.destination = path
	policyName, err := asString(object, "publication_policy")
	if err != nil || !validPublicationPolicyName(policyName) {
		return nil, rpc.InvalidParamsError("removals_output.publication_policy is invalid")
	}
	settings.policy = policyByName(policyName)
	budget, err := memberObject(object, "result_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError("result_budget must be an object")
	}
	if err := exactObjectRaw(budget, "max_rows", "max_output_bytes", "max_open_files"); err != nil {
		return nil, rpc.InvalidParamsError("result_budget is invalid")
	}
	maxRows, err := positiveU64String(budget, "max_rows")
	if err != nil {
		return nil, rpc.InvalidParamsError("result_budget.max_rows is invalid")
	}
	settings.maxRows = maxRows
	maxOutputBytes, err := positiveU64String(budget, "max_output_bytes")
	if err != nil {
		return nil, rpc.InvalidParamsError("result_budget.max_output_bytes is invalid")
	}
	settings.maxOutputBytes = maxOutputBytes
	maxOpenFiles, err := positiveU32(budget, "max_open_files")
	if err != nil {
		return nil, rpc.InvalidParamsError("result_budget.max_open_files is invalid")
	}
	settings.maxOpenFiles = maxOpenFiles
	return settings, nil
}

// positiveU64String decodes one positive canonical unsigned decimal
// string member.
func positiveU64String(object rawObject, field string) (uint64, error) {
	value, err := decimalU64FromWire(object, field)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return 0, fmt.Errorf("%s must be a positive canonical unsigned decimal string", field)
	}
	return value, nil
}

// positiveU32 decodes one positive u32 JSON integer member.
func positiveU32(object rawObject, field string) (uint32, error) {
	return asPositiveU32(object, field)
}

// removalCollector is the bounded, digest-tracking JSONL writer for
// first-seen removals (Rust RemovalCollector). The private temporary
// file is created in the destination directory before the refresh
// finishes, survives the commit decision, and is published only by
// publish(); every other terminal path discards it explicitly.
type removalCollector struct {
	file           *os.File
	buffered       *bufio.Writer
	temporary      string
	destination    string
	policy         iprangedb.PublicationPolicy
	maxRows        uint64
	maxOutputBytes uint64
	refreshValue   uint32
	rows           uint64
	bytes          uint64
	digest         hash.Hash
	line           []byte
	violation      string
}

// newRemovalCollector creates the private temporary removal output
// after every fallible pre-work has succeeded, so no early return can
// leak it (Rust RemovalCollector::new).
func newRemovalCollector(settings removalsSettings, refreshValue uint32) (*removalCollector, *rpc.HandlerError) {
	if settings.maxOpenFiles < 1 {
		return nil, rpc.NewHandlerError("invalid_argument", "not_started",
			"removal output requires at least one open file")
	}
	parent := filepath.Dir(settings.destination)
	if parent == "" {
		parent = "."
	}
	info, err := os.Stat(parent)
	switch {
	case err == nil && info.IsDir():
	case err == nil:
		return nil, rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("removal output parent is not a directory: %s", parent))
	case errors.Is(err, os.ErrNotExist):
		return nil, rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("removal output parent does not exist: %s", parent))
	default:
		return nil, rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("inspect removal output parent %s: %v", parent, err))
	}
	handle, herr := rpc.NewHandle()
	if herr != nil {
		return nil, herr
	}
	temporary := filepath.Join(parent, "."+handle+".removals.tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return nil, fileError(err, "create removal output")
	}
	return &removalCollector{
		file:           file,
		buffered:       bufio.NewWriterSize(file, 64*1024),
		temporary:      temporary,
		destination:    settings.destination,
		policy:         settings.policy,
		maxRows:        settings.maxRows,
		maxOutputBytes: settings.maxOutputBytes,
		refreshValue:   refreshValue,
		digest:         sha256.New(),
	}, nil
}

// sink4 adapts the collector to the IPv4 removal sink.
func (c *removalCollector) sink4() iprangedb.FirstSeenRemoval4Sink {
	return func(removals []iprangedb.FirstSeenRemoval4) error {
		for i := range removals {
			removal := &removals[i]
			line := fmt.Sprintf("{\"from\":\"%s\",\"to\":\"%s\",\"first_seen\":%d,\"removed_at\":%d,\"addresses\":\"%s\"}",
				ipv4Text(removal.From), ipv4Text(removal.To), removal.FirstSeen, c.refreshValue, removal.Addresses.String())
			if err := c.writeLine(line); err != nil {
				return err
			}
		}
		return nil
	}
}

// sink6 adapts the collector to the IPv6 removal sink.
func (c *removalCollector) sink6() iprangedb.FirstSeenRemoval6Sink {
	return func(removals []iprangedb.FirstSeenRemoval6) error {
		for i := range removals {
			removal := &removals[i]
			from := iprangedb.IPv6{Hi: removal.FromHi, Lo: removal.FromLo}
			to := iprangedb.IPv6{Hi: removal.ToHi, Lo: removal.ToLo}
			line := fmt.Sprintf("{\"from\":\"%s\",\"to\":\"%s\",\"first_seen\":%d,\"removed_at\":%d,\"addresses\":\"%s\"}",
				ipv6Text(from), ipv6Text(to), removal.FirstSeen, c.refreshValue, removal.Addresses.String())
			if err := c.writeLine(line); err != nil {
				return err
			}
		}
		return nil
	}
}

// ipv4Text renders one IPv4 address canonically.
func ipv4Text(value iprangedb.IPv4) string {
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}).String()
}

// ipv6Text renders one IPv6 address canonically (the same RFC 5952
// form as the Rust std emitter).
func ipv6Text(value iprangedb.IPv6) string {
	var bytes [16]byte
	for i := 0; i < 8; i++ {
		bytes[i] = byte(value.Hi >> (56 - 8*i))
		bytes[8+i] = byte(value.Lo >> (56 - 8*i))
	}
	return netip.AddrFrom16(bytes).String()
}

// writeLine appends one JSONL record under the exact row/byte budget
// and tracks the running digest (Rust write_line).
func (c *removalCollector) writeLine(line string) error {
	nextRows := c.rows + 1
	if nextRows < c.rows {
		return c.budgetViolation("row count overflow")
	}
	if nextRows > c.maxRows {
		return c.budgetViolation(fmt.Sprintf("row %d exceeds max_rows", nextRows))
	}
	nextBytes := c.bytes + uint64(len(line)) + 1
	if nextBytes < c.bytes {
		return c.budgetViolation("byte count overflow")
	}
	if nextBytes > c.maxOutputBytes {
		return c.budgetViolation(fmt.Sprintf("byte %d exceeds max_output_bytes", nextBytes))
	}
	if _, err := c.buffered.WriteString(line); err != nil {
		return &iprangedb.Error{Code: iprangedb.ErrorIO, Detail: err.Error()}
	}
	if _, err := c.buffered.WriteString("\n"); err != nil {
		return &iprangedb.Error{Code: iprangedb.ErrorIO, Detail: err.Error()}
	}
	c.digest.Write([]byte(line))
	c.digest.Write([]byte("\n"))
	c.rows = nextRows
	c.bytes = nextBytes
	return nil
}

// budgetViolation records the refusal detail and returns the SDK
// failure that stops the refresh workflow.
func (c *removalCollector) budgetViolation(detail string) error {
	c.violation = fmt.Sprintf("first-seen removals refused before exceeding budget: %s", detail)
	return &iprangedb.Error{Code: iprangedb.ErrorInvalidArgument,
		Detail: "first-seen removal output exceeded its result budget"}
}

// takeViolation returns and clears the recorded budget violation.
func (c *removalCollector) takeViolation() string {
	violation := c.violation
	c.violation = ""
	return violation
}

// unpublishedFacts is the removal evidence inside error details when
// the commit never published (Rust unpublished_facts).
func (c *removalCollector) unpublishedFacts() map[string]any {
	return map[string]any{"publication": removalPublicationFacts("not_published", "absent")}
}

// removalPublicationFacts is the adapter-owned artifact publication
// pair; no SDK PublicationResult exists for the removals file, so the
// facts carry only the outcome.
func removalPublicationFacts(publication, destinationContent string) map[string]any {
	return map[string]any{"publication": publication, "destination_content": destinationContent}
}

// discard explicitly removes the private temporary; removal failures
// are reported, never absorbed.  The file is closed first: Windows
// refuses to remove a file whose handle is still open (Go files do
// not share DELETE), and the success path of publishInner already
// closed it, so a second close is harmless.
func (c *removalCollector) discard() *rpc.HandlerError {
	_ = c.file.Close()
	err := os.Remove(c.temporary)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fileError(err, "remove removal output temporary")
	}
}

// publish flushes, syncs, atomically publishes, and syncs the
// directory; the caller invokes it only after the commit is factually
// known to have committed (Rust RemovalCollector::publish).
func (c *removalCollector) publish() (map[string]any, *rpc.HandlerError) {
	value, herr := c.publishInner()
	if herr != nil {
		// The private temporary is removed explicitly on publication
		// failure; a failed removal is reported with the error.  The
		// file is closed first: publishInner closes it only on the
		// success path, and Windows refuses to remove a file whose
		// handle is still open.
		_ = c.file.Close()
		if derr := os.Remove(c.temporary); derr != nil && !errors.Is(derr, os.ErrNotExist) {
			details := map[string]any{
				"cleanup_failure": map[string]any{"error": derr.Error(), "path": c.temporary},
			}
			herr.Details = details
		}
		return nil, herr
	}
	return value, nil
}

func (c *removalCollector) publishInner() (map[string]any, *rpc.HandlerError) {
	if err := c.buffered.Flush(); err != nil {
		return nil, fileError(err, "sync removal output")
	}
	if err := c.file.Sync(); err != nil {
		return nil, fileError(err, "sync removal output")
	}
	if err := c.file.Close(); err != nil {
		return nil, fileError(err, "sync removal output")
	}
	destinationContent := "created"
	if info, err := os.Stat(c.destination); err == nil && info.Mode().IsRegular() {
		destinationContent = "previous"
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fileError(err, "inspect removal destination")
	}
	switch c.policy {
	case iprangedb.PolicyFailIfExists:
		if err := os.Link(c.temporary, c.destination); err != nil {
			return nil, fileError(err, "publish removal output")
		}
		if err := os.Remove(c.temporary); err != nil {
			return nil, fileError(err, "remove removal temporary")
		}
	case iprangedb.PolicyReplaceExisting, iprangedb.PolicyReplaceExistingNoRollback:
		if err := os.Rename(c.temporary, c.destination); err != nil {
			return nil, fileError(err, "publish removal output")
		}
	}
	parent := filepath.Dir(c.destination)
	if parent == "" {
		parent = "."
	}
	if herr := syncOutputDirectory(parent); herr != nil {
		return nil, herr
	}
	return map[string]any{
		"publication": removalPublicationFacts("published", destinationContent),
		"output": map[string]any{
			"path":   c.destination,
			"sha256": HexBytes(c.digest.Sum(nil)),
			"bytes":  fmt.Sprintf("%d", c.bytes),
			"rows":   fmt.Sprintf("%d", c.rows),
		},
	}, nil
}
