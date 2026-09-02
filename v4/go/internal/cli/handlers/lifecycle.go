// Explicit database creation and metadata replacement (Rust
// handlers/lifecycle.rs parity): `iprange.v1.database.create` and
// `iprange.v1.database.metadata.replace`.
//
// Creation resolves an outcome-unknown create attempt through the
// SDK resolver before reporting, exactly like the Rust handler: the
// result is either the complete CreateResult or a product error that
// preserves the factual create_result in its details. Metadata
// replacement opens one clean live writer, stages the requested
// metadata inside a fresh typed transaction matching the database
// value kind (the Go SDK has no kind-less writer metadata stage), and
// closes the writer; every replacement is committed even when its
// bytes equal the prior bytes, while a clear commits only when
// metadata was present (iprange-jsonrpc-v1.md database.metadata.replace).

package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ValidateDatabaseCreateParams enforces the strict database.create
// params schema (v4/cli/schema/methods.py): exact members, path
// bounds, the family/value-kind/structure-kind compatibility rule,
// the strict value tag, and a u32 reader capacity.
func ValidateDatabaseCreateParams(params json.RawMessage) error {
	object, err := exactObject(params, "path", "family", "value_kind", "structure_kind", "value_tag", "reader_capacity")
	if err != nil {
		return err
	}
	path, err := asString(object, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	family, err := asString(object, "family")
	if err != nil {
		return err
	}
	if family != "ipv4" && family != "ipv6" {
		return fmt.Errorf("family must be ipv4 or ipv6")
	}
	valueKind, err := asString(object, "value_kind")
	if err != nil {
		return err
	}
	if valueKind != "direct" && valueKind != "membership" && valueKind != "structured" {
		return fmt.Errorf("value_kind must be direct, membership, or structured")
	}
	if err := validateCreateStructureKind(valueKind, object); err != nil {
		return err
	}
	tag, err := memberObject(object, "value_tag")
	if err != nil {
		return err
	}
	if err := validateValueTagObject(tag); err != nil {
		return err
	}
	if _, err := asUint32(object, "reader_capacity"); err != nil {
		return err
	}
	return nil
}

// validateCreateStructureKind enforces that network_enrichment_v1
// pairs only with structured values and none pairs with the rest.
func validateCreateStructureKind(valueKind string, object rawObject) error {
	structure, err := asString(object, "structure_kind")
	if err != nil {
		return err
	}
	valid := (valueKind == "direct" || valueKind == "membership") && structure == "none" ||
		valueKind == "structured" && structure == "network_enrichment_v1"
	if !valid {
		return fmt.Errorf("structure_kind is incompatible with value_kind")
	}
	return nil
}

// validateValueTagObject enforces the strict value-tag object: exactly
// one of text (at most 15 bytes without NUL) or hex (even lowercase
// hex encoding at most 15 bytes without a NUL byte).
func validateValueTagObject(tag rawObject) error {
	if len(tag) == 1 {
		if text, ok := tag["text"]; ok {
			if isRawNull(text) {
				return fmt.Errorf("value_tag.text must be a string; null is not valid")
			}
			var value string
			if err := json.Unmarshal(text, &value); err != nil || strings.IndexByte(value, 0) >= 0 || len(value) > 15 {
				return fmt.Errorf("value_tag.text must encode 0 through 15 bytes without NUL")
			}
			return nil
		}
		if hex, ok := tag["hex"]; ok {
			if isRawNull(hex) {
				return fmt.Errorf("value_tag.hex must be a string; null is not valid")
			}
			var value string
			if err := json.Unmarshal(hex, &value); err != nil {
				return fmt.Errorf("value_tag.hex must be a string")
			}
			if len(value) > 30 || len(value)%2 != 0 {
				return fmt.Errorf("value_tag.hex must be even lowercase hex encoding at most 15 bytes")
			}
			for i := 0; i < len(value); i++ {
				c := value[i]
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
					return fmt.Errorf("value_tag.hex must be even lowercase hex encoding at most 15 bytes")
				}
			}
			for i := 0; i < len(value); i += 2 {
				if value[i] == '0' && value[i+1] == '0' {
					return fmt.Errorf("value_tag.hex must not encode a NUL byte")
				}
			}
			return nil
		}
	}
	return fmt.Errorf("value_tag must contain exactly one of text or hex")
}

// ValidateDatabaseMetadataReplaceParams enforces the strict
// metadata.replace params: path, a replacement metadata (keep is
// invalid for this method), and the writer budget.
func ValidateDatabaseMetadataReplaceParams(params json.RawMessage) error {
	object, err := exactObject(params, "path", "metadata", "writer_budget")
	if err != nil {
		return err
	}
	path, err := asString(object, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	metadata, err := memberObject(object, "metadata")
	if err != nil {
		return err
	}
	if err := validateReplacementMetadata(metadata); err != nil {
		return err
	}
	budget, err := memberObject(object, "writer_budget")
	if err != nil {
		return err
	}
	return validateWriterBudgetObject(budget)
}

// validateReplacementMetadata enforces the METADATA_REPLACEMENT_INPUT
// forms: clear, replace_utf8, replace_base64, or replace_file; keep
// is rejected.
func validateReplacementMetadata(metadata rawObject) error {
	mode, err := asString(metadata, "mode")
	if err != nil {
		return fmt.Errorf("metadata.mode is invalid for this method")
	}
	switch mode {
	case "clear":
		return exactObjectRaw(metadata, "mode")
	case "replace_utf8":
		if err := exactObjectRaw(metadata, "mode", "text"); err != nil {
			return err
		}
		if _, err := asString(metadata, "text"); err != nil {
			return fmt.Errorf("metadata.text must be a string")
		}
		return nil
	case "replace_base64":
		if err := exactObjectRaw(metadata, "mode", "base64"); err != nil {
			return err
		}
		text, err := asString(metadata, "base64")
		if err != nil {
			return fmt.Errorf("metadata.base64 must be a string")
		}
		if _, err := base64Decode(text); err != nil {
			return fmt.Errorf("metadata.base64 must be a string")
		}
		return nil
	case "replace_file":
		if err := exactObjectRaw(metadata, "mode", "path"); err != nil {
			return err
		}
		text, err := asString(metadata, "path")
		if err != nil {
			return err
		}
		return validatePath(text)
	}
	return fmt.Errorf("metadata.mode is invalid for this method")
}

// validateWriterBudgetObject enforces the WRITER_BUDGET shape: three
// positive decimal u64 strings and one positive u32 integer.
func validateWriterBudgetObject(budget rawObject) error {
	if err := exactObjectRaw(budget, "max_heap_bytes", "max_private_pages", "max_growth_pages", "max_open_files"); err != nil {
		return err
	}
	for _, field := range []string{"max_heap_bytes", "max_private_pages", "max_growth_pages"} {
		if _, err := asPositiveU64String(budget, field); err != nil {
			return fmt.Errorf("writer_budget.%s must be a positive canonical unsigned decimal string", field)
		}
	}
	if _, err := asUint32(budget, "max_open_files"); err != nil {
		return fmt.Errorf("writer_budget.max_open_files must be a positive u32 integer")
	}
	return nil
}

// decodeWriterBudget converts the validated wire budget into the SDK
// PageBudget limits.
func decodeWriterBudget(budget rawObject) (iprangedb.PageBudget, error) {
	heap, err := canonicalU64FromRaw(budget["max_heap_bytes"])
	if err != nil {
		return iprangedb.PageBudget{}, err
	}
	privatePages, err := canonicalU64FromRaw(budget["max_private_pages"])
	if err != nil {
		return iprangedb.PageBudget{}, err
	}
	growthPages, err := canonicalU64FromRaw(budget["max_growth_pages"])
	if err != nil {
		return iprangedb.PageBudget{}, err
	}
	openFiles, err := asUint32(budget, "max_open_files")
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

// DatabaseCreate implements iprange.v1.database.create: one explicit
// SDK creation with the fixed creator-only security; an
// outcome-unknown attempt is resolved before reporting (Rust
// database_create).
func DatabaseCreate(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireCreateDestinationParent(path); herr != nil {
		return nil, herr
	}
	family, err := asString(object, "family")
	if err != nil {
		return nil, rpc.InvalidParamsError("family must be ipv4 or ipv6")
	}
	valueKind, err := asString(object, "value_kind")
	if err != nil {
		return nil, rpc.InvalidParamsError("value_kind must be direct, membership, or structured")
	}
	structureKind, err := asString(object, "structure_kind")
	if err != nil {
		return nil, rpc.InvalidParamsError("structure_kind is invalid")
	}
	tag, err := valueTagFromObject(object)
	if err != nil {
		return nil, rpc.InvalidParamsError("value_tag is invalid")
	}
	readerCapacity, err := asUint32(object, "reader_capacity")
	if err != nil {
		return nil, rpc.InvalidParamsError("reader_capacity must be a u32 integer")
	}

	result, createErr := iprangedb.CreateLive(path, familyByName(family), valueKindByName(valueKind),
		structureKindByName(structureKind), tag, readerCapacity, st.Token())
	if createErr != nil {
		return nil, SDKError(createErr, "not_started")
	}
	if result.State != iprangedb.CreationStateCreated {
		resolved, resolveErr := iprangedb.ResolveCreateLive(path, result, iprangedb.LiveTransitionResolutionComplete, st.Token())
		if resolveErr != nil {
			return nil, SDKError(resolveErr, "outcome_unknown")
		}
		result = resolved
	}
	if result.State != iprangedb.CreationStateCreated {
		facts, convErr := CreateResultJSON(&result)
		if convErr != nil {
			return nil, convErr
		}
		code := "io"
		message := "live database creation did not complete"
		if result.Cause != nil {
			message = result.Cause.Error()
			if typed, ok := result.Cause.(*iprangedb.Error); ok {
				code = sdkCode(typed.Code)
			}
		}
		outcome := "outcome_unknown"
		if result.State == iprangedb.CreationStateNotCreated {
			outcome = "not_started"
		}
		return nil, &rpc.HandlerError{
			Code: code, Outcome: outcome, Message: message,
			Details: map[string]any{"create_result": facts},
		}
	}
	facts, convErr := CreateResultJSON(&result)
	if convErr != nil {
		return nil, convErr
	}
	facts["method"] = "iprange.v1.database.create"
	return boundedResult(facts)
}

// familyByName maps one wire family name to the SDK family.
func familyByName(name string) iprangedb.AddressFamily {
	if name == "ipv6" {
		return iprangedb.AddressFamilyIPv6
	}
	return iprangedb.AddressFamilyIPv4
}

// valueKindByName maps one wire value-kind name to the SDK kind.
func valueKindByName(name string) iprangedb.ValueKind {
	switch name {
	case "membership":
		return iprangedb.ValueKindMembership
	case "structured":
		return iprangedb.ValueKindStructured
	default:
		return iprangedb.ValueKindDirect
	}
}

// structureKindByName maps one wire structure-kind name to the SDK
// kind (structured values always pair with network_enrichment_v1).
func structureKindByName(name string) iprangedb.StructureKind {
	if name == "network_enrichment_v1" {
		return iprangedb.StructureKindNetworkEnrichmentV1
	}
	return iprangedb.StructureKindNone
}

// valueTagFromObject decodes the strict value-tag wire object into
// the canonical SDK tag (valueTagFromWire authority).
func valueTagFromObject(object rawObject) (iprangedb.ValueTag, error) {
	tag, err := memberObject(object, "value_tag")
	if err != nil {
		return iprangedb.ValueTag{}, err
	}
	return valueTagFromWire(tag, "value_tag")
}

// CreateResultJSON converts one SDK creation result to its complete
// wire object (results.py CREATE_RESULT); absent identities are
// omitted, never null (wire rule).
func CreateResultJSON(result *iprangedb.CreateResult) (map[string]any, *rpc.HandlerError) {
	value := map[string]any{
		"address_family":       AddressFamilyName(result.Family),
		"value_kind":           ValueKindName(result.ValueKind),
		"structure_kind":       StructureKindName(result.StructureKind),
		"value_tag":            ValueTagJSON(result.ValueTag),
		"database_id":          HexID(&result.DatabaseID),
		"commit_nonce":         HexID(&result.CommitNonce),
		"sidecar_id":           HexID(&result.SidecarID),
		"main_basename":        LocalBasenameBytes(result.MainBasename),
		"reader_capacity":      result.ReaderCapacity,
		"state":                creationStateName(result.State),
		"residue_possible":     result.ResiduePossible,
		"housekeeping":         HousekeepingJSON(result.Housekeeping, result.VisibleHousekeeping),
		"visible_housekeeping": VisibleHousekeepingJSON(result.VisibleHousekeeping),
	}
	if result.DirectoryIdentity != nil {
		value["directory_identity"] = FileIdentityJSONOrError(result.DirectoryIdentity)
	}
	if result.MainIdentity != nil {
		value["main_identity"] = FileIdentityJSONOrError(result.MainIdentity)
	}
	if result.SidecarIdentity != nil {
		value["sidecar_identity"] = FileIdentityJSONOrError(result.SidecarIdentity)
	}
	return value, nil
}

// creationStateName maps one SDK creation state to its wire name.
func creationStateName(state iprangedb.CreationState) string {
	switch state {
	case iprangedb.CreationStateCreated:
		return "created"
	case iprangedb.CreationStateOutcomeUnknown:
		return "outcome_unknown"
	default:
		return "not_created"
	}
}

// requireCreateDestinationParent mirrors the Rust create preflight: a
// missing or non-directory parent is a product error before any SDK
// work, so a typo never creates private artifacts next to the target.
func requireCreateDestinationParent(path string) *rpc.HandlerError {
	if filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) || path == "" {
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("database destination has no file name: %s", path))
	}
	parent := filepath.Dir(path)
	if parent == "" {
		parent = "."
	}
	info, err := os.Stat(parent)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("database parent is not a directory: %s", parent))
	case os.IsNotExist(err):
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("database parent does not exist: %s", parent))
	default:
		return rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("inspect database parent %s: %v", parent, err))
	}
}

// requireExistingLiveDatabase mirrors the Rust metadata/reclaim
// preflight: the target must be one regular file.
func requireExistingLiveDatabase(path string) *rpc.HandlerError {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		return nil
	case err == nil:
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("live database is not a regular file: %s", path))
	case os.IsNotExist(err):
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("live database does not exist: %s", path))
	default:
		return rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("inspect live database %s: %v", path, err))
	}
}

// metadataTransaction is the metadata-only mutation surface shared by
// the three advanced transaction types: the Go SDK stages writer
// metadata inside a typed transaction (Rust LiveWriter::set_metadata_json).
type metadataTransaction interface {
	SetMetadataJSON(input []byte) (bool, error)
	ClearMetadataJSON() (bool, error)
	Commit() (iprangedb.CommitResult, error)
}

// beginMetadataTransaction opens the transaction matching the
// database's value kind, so metadata replacement works on direct,
// membership, and structured databases alike (Rust's kind-less writer
// stage has no direct Go counterpart).
func beginMetadataTransaction(writer *iprangedb.LiveWriter, cancellation *iprangedb.CancellationToken) (metadataTransaction, *rpc.HandlerError) {
	info, err := writer.Info()
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	switch info.ValueKind {
	case iprangedb.ValueKindDirect:
		transaction, err := writer.BeginDirect(cancellation)
		if err != nil {
			return nil, SDKError(err, "not_started")
		}
		return transaction, nil
	case iprangedb.ValueKindStructured:
		transaction, err := writer.BeginStructuredTransaction(cancellation)
		if err != nil {
			return nil, SDKError(err, "not_started")
		}
		return transaction, nil
	default:
		transaction, err := writer.BeginMembershipTransaction(cancellation)
		if err != nil {
			return nil, SDKError(err, "not_started")
		}
		return transaction, nil
	}
}

// DatabaseMetadataReplace implements iprange.v1.database.metadata.replace:
// the result carries the logical change, the commit facts when a
// commit was attempted, and the writer close; every terminal path
// preserves the factual fields in the error details (Rust
// database_metadata_replace).
func DatabaseMetadataReplace(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingLiveDatabase(path); herr != nil {
		return nil, herr
	}
	metadataObject, merr := memberObject(object, "metadata")
	if merr != nil {
		return nil, rpc.InvalidParamsError(merr.Error())
	}
	metadata, herr := MetadataValueFromObject(metadataObject)
	if herr != nil {
		return nil, herr
	}
	if metadata.Keep {
		return nil, rpc.InvalidParamsError("metadata.mode is invalid for this method")
	}
	budgetObject, merr := memberObject(object, "writer_budget")
	if merr != nil {
		return nil, rpc.InvalidParamsError("writer_budget is invalid")
	}
	budget, err := decodeWriterBudget(budgetObject)
	if err != nil {
		return nil, rpc.InvalidParamsError("writer_budget is invalid")
	}

	writer, openErr := iprangedb.OpenLiveWriter(path, budget, st.Token())
	if openErr != nil {
		return nil, SDKError(openErr, "not_started")
	}
	transaction, beginErr := beginMetadataTransaction(writer, st.Token())
	if beginErr != nil {
		// The writer opened but the typed stage could not start; close
		// it and report the stage failure with the close facts.
		closeFacts, closeErr := CloseWriter(writer)
		if closeErr != nil {
			closeFacts = map[string]any{"outcome": "close_incomplete", "cleanup": map[string]any{}, "coordination_cleanup": map[string]any{}}
		}
		return nil, &rpc.HandlerError{
			Code: beginErr.Code, Outcome: beginErr.Outcome, Message: beginErr.Message,
			Details: map[string]any{
				"logical_change": "unchanged",
				"writer_close":   closeFacts,
				"failure":        map[string]any{"code": beginErr.Code, "message": beginErr.Message},
			},
		}
	}

	stagedChanged, stageErr := stageMetadata(transaction, &metadata)
	shouldCommit := !metadata.Keep && (metadata.Bytes != nil || stagedChanged)
	var commit *iprangedb.CommitResult
	var commitErr error
	if shouldCommit {
		result, err := transaction.Commit()
		commit = &result
		commitErr = err
	}
	if stageErr != nil {
		closeFacts, closeErr := CloseWriter(writer)
		if closeErr != nil {
			closeFacts = map[string]any{"outcome": "close_incomplete", "cleanup": map[string]any{}, "coordination_cleanup": map[string]any{}}
		}
		failure := SDKError(stageErr, "not_started")
		return nil, &rpc.HandlerError{
			Code: failure.Code, Outcome: failure.Outcome, Message: failure.Message,
			Details: map[string]any{
				"logical_change": "unchanged",
				"writer_close":   closeFacts,
				"failure":        map[string]any{"code": failure.Code, "message": failure.Message},
			},
		}
	}

	logicalChange := "unchanged"
	if commit != nil {
		logicalChange = "changed"
	}
	closeFacts, closeErr := CloseWriter(writer)
	if closeErr != nil {
		closeFacts = map[string]any{"outcome": "close_incomplete", "cleanup": map[string]any{}, "coordination_cleanup": map[string]any{}}
	}
	details := map[string]any{
		"logical_change": logicalChange,
		"writer_close":   closeFacts,
	}
	if commitErr != nil {
		failure := SDKError(commitErr, "not_started")
		details["failure"] = map[string]any{"code": failure.Code, "message": failure.Message}
		return nil, &rpc.HandlerError{
			Code: failure.Code, Outcome: failure.Outcome, Message: failure.Message,
			Details: details,
		}
	}
	if commit != nil {
		commitFacts, convErr := CommitResultJSON(commit)
		if convErr != nil {
			return nil, convErr
		}
		details["commit"] = commitFacts
		if commit.Status != iprangedb.CommitCommitted || commit.Cause != nil {
			code := "io"
			message := "metadata commit did not complete"
			if commit.Cause != nil {
				message = commit.Cause.Error()
				if typed, ok := commit.Cause.(*iprangedb.Error); ok {
					code = sdkCode(typed.Code)
				}
			}
			return nil, &rpc.HandlerError{
				Code: code, Outcome: CommitDurabilityName(commit.Status), Message: message,
				Details: details,
			}
		}
	}
	if closeFacts["outcome"] == "close_incomplete" {
		outcome := "not_started"
		if commit != nil {
			outcome = CommitDurabilityName(commit.Status)
		}
		return nil, &rpc.HandlerError{
			Code: "io", Outcome: outcome, Message: "live writer close is incomplete",
			Details: details,
		}
	}
	details["method"] = "iprange.v1.database.metadata.replace"
	return boundedResult(details)
}

// stageMetadata applies one replacement or clear inside the typed
// transaction; keep is rejected by the validator and refused here.
func stageMetadata(transaction metadataTransaction, metadata *MetadataValue) (bool, error) {
	if metadata.Clear {
		return transaction.ClearMetadataJSON()
	}
	return transaction.SetMetadataJSON(metadata.Bytes)
}

// RegisterLifecycle installs the database lifecycle handler family.
func RegisterLifecycle() {
	rpc.Register("iprange.v1.database.create", ValidateDatabaseCreateParams, DatabaseCreate)
	rpc.Register("iprange.v1.database.metadata.replace", ValidateDatabaseMetadataReplaceParams, DatabaseMetadataReplace)
}
