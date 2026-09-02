// Shared publisher-facts conversions (Rust handlers/lifecycle.rs
// parity): metadata staging values, commit/close facts, local file
// identities, publication evidence facts, and the SDK error adapter.
// These helpers are the single authority for the wire shapes every
// mutation family emits.

package handlers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// MetadataValue is the requested metadata terminal of one workflow:
// keep the current metadata, clear it, or replace it with exact bytes.
type MetadataValue struct {
	Keep  bool
	Clear bool
	Bytes []byte // set when replacing
}

// MetadataValueFromObject decodes the strict `metadata` parameter
// object (mode keep/clear/replace_utf8/replace_base64/replace_file).
func MetadataValueFromObject(object rawObject) (MetadataValue, *rpc.HandlerError) {
	mode, err := asString(object, "mode")
	if err != nil {
		return MetadataValue{}, rpc.InvalidParamsError("metadata.mode is invalid")
	}
	switch mode {
	case "keep":
		return MetadataValue{Keep: true}, nil
	case "clear":
		return MetadataValue{Clear: true}, nil
	case "replace_utf8":
		text, err := asString(object, "text")
		if err != nil {
			return MetadataValue{}, rpc.InvalidParamsError("metadata.text must be a string")
		}
		return MetadataValue{Bytes: []byte(text)}, nil
	case "replace_base64":
		text, err := asString(object, "base64")
		if err != nil {
			return MetadataValue{}, rpc.InvalidParamsError("metadata.base64 must be a string")
		}
		decoded, derr := base64Decode(text)
		if derr != nil {
			return MetadataValue{}, rpc.InvalidParamsError("metadata.base64 must be a string")
		}
		return MetadataValue{Bytes: decoded}, nil
	case "replace_file":
		path, err := asString(object, "path")
		if err != nil {
			return MetadataValue{}, rpc.InvalidParamsError("metadata.path must be a string")
		}
		bytes, herr := readMetadataFile(path)
		if herr != nil {
			return MetadataValue{}, herr
		}
		return MetadataValue{Bytes: bytes}, nil
	}
	return MetadataValue{}, rpc.InvalidParamsError("metadata.mode is invalid")
}

// base64Decode is the canonical wire base64 decoder (the wire
// metadata blob encoding; Rust lifecycle.rs decode_base64), delegated
// to the single internal/format authority.
func base64Decode(text string) ([]byte, error) {
	return format.DecodeCanonicalBase64(text)
}

// readMetadataFile reads a metadata source with the exact 20 MiB cap,
// so a file that grows between the size check and the read cannot
// drive an unbounded heap allocation.
func readMetadataFile(path string) ([]byte, *rpc.HandlerError) {
	const maxMetadata = int(iprangedb.MaxMetadataUncompressed)
	file, err := os.Open(path)
	if err != nil {
		return nil, rpc.NewHandlerError("io", "not_started",
			"cannot read metadata file: "+err.Error())
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, rpc.NewHandlerError("io", "not_started",
			"cannot inspect metadata file: "+err.Error())
	}
	if info.Size() > int64(maxMetadata) {
		return nil, rpc.NewHandlerError("invalid_argument", "not_started",
			fmt.Sprintf("metadata file is %d bytes, limit is %d", info.Size(), iprangedb.MaxMetadataUncompressed))
	}
	bytes := make([]byte, info.Size())
	if _, err := file.Read(bytes); err != nil {
		return nil, rpc.NewHandlerError("io", "not_started",
			"cannot read metadata file: "+err.Error())
	}
	return bytes, nil
}

// CommitResultJSON converts one SDK commit result to its wire object.
func CommitResultJSON(result *iprangedb.CommitResult) (map[string]any, *rpc.HandlerError) {
	return map[string]any{
		"attempted_database_id":    HexID(&result.AttemptedDatabaseID),
		"directory_identity":       FileIdentityJSONOrError(result.DirectoryIdentity),
		"main_identity":            FileIdentityJSONOrError(result.MainIdentity),
		"attempted_transaction_id": DecimalUint(result.AttemptedTransactionID),
		"attempted_commit_nonce":   HexID(&result.AttemptedCommitNonce),
		"durability":               CommitDurabilityName(result.Status),
		"cleanup":                  CommitCleanupJSON(result.Cleanup),
		"coordination_cleanup":     CoordinationCleanupJSON(result.CoordinationCleanup),
	}, nil
}

// CloseResultJSON converts one SDK live close result to its wire
// object; abort facts are absent, never null (wire rule).
func CloseResultJSON(result *iprangedb.LiveCloseResult) (map[string]any, *rpc.HandlerError) {
	value := map[string]any{
		"outcome":              CloseOutcomeName(result.Outcome),
		"cleanup":              CommitCleanupJSON(result.Cleanup),
		"coordination_cleanup": CoordinationCleanupJSON(result.CoordinationCleanup),
	}
	if result.AbortOutcome != nil {
		value["abort_outcome"] = AbortOutcomeName(*result.AbortOutcome)
	}
	return value, nil
}

// CommitDurabilityName maps the SDK commit status to its wire name.
func CommitDurabilityName(status iprangedb.CommitStatus) string {
	switch status {
	case iprangedb.CommitNotCommitted:
		return "not_committed"
	case iprangedb.CommitCommitted:
		return "committed"
	case iprangedb.CommitOutcomeUnknown:
		return "outcome_unknown"
	}
	return "outcome_unknown"
}

func CloseOutcomeName(outcome iprangedb.CloseOutcome) string {
	switch outcome {
	case iprangedb.CloseOutcomeClosed:
		return "closed"
	case iprangedb.CloseOutcomeCloseIncomplete:
		return "close_incomplete"
	}
	return "close_incomplete"
}

func AbortOutcomeName(outcome iprangedb.AbortOutcome) string {
	switch outcome {
	case iprangedb.AbortOutcomeAborted:
		return "aborted"
	case iprangedb.AbortOutcomeAbortIncomplete:
		return "abort_incomplete"
	}
	return "abort_incomplete"
}

// FileIdentityJSON decodes one local identity to its documented
// volume/file decimal pair (little-endian device and inode). The
// identity kind and zero-padding are validated exactly like the Rust
// adapter; unsupported kinds are a handler error.
func FileIdentityJSON(identity *iprangedb.FileIdentity) (map[string]any, *rpc.HandlerError) {
	if identity == nil {
		return nil, rpc.NewHandlerError("io", "not_started", "missing local file identity")
	}
	bytes := identity.Bytes
	tailZero := false
	switch identity.Kind {
	case 1:
		tailZero = allZero(bytes[16:32])
	case 2:
		tailZero = allZero(bytes[24:32])
	}
	if !tailZero {
		return nil, rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("unsupported local file identity kind %d", identity.Kind))
	}
	device := binary.LittleEndian.Uint64(bytes[0:8])
	file := binary.LittleEndian.Uint64(bytes[8:16])
	return map[string]any{
		"volume": DecimalUint(device),
		"file":   DecimalUint(file),
	}, nil
}

// allZero reports whether every byte of the slice is zero.
func allZero(bytes []byte) bool {
	for _, b := range bytes {
		if b != 0 {
			return false
		}
	}
	return true
}

// FileIdentityJSONOrError returns the identity facts or an embedded
// error object where a robust evidence surface must not fail the whole
// result (the optional identity fields of housekeeping artifacts).
func FileIdentityJSONOrError(identity *iprangedb.FileIdentity) any {
	value, err := FileIdentityJSON(identity)
	if err != nil {
		return map[string]any{"error": err.Message}
	}
	return value
}

// LocalBasenameBytes returns the basename content.
func LocalBasenameBytes(basename iprangedb.LocalBasename) string {
	return string(basename.Bytes())
}

// CommitCleanupJSON converts the commit cleanup ledger to its wire
// object; an empty ledger yields {} (absent, never null).
func CommitCleanupJSON(cleanup iprangedb.LiveCommitCleanupArtifacts) map[string]any {
	if cleanup.Empty() {
		return map[string]any{}
	}
	var artifacts []any
	for entry := cleanup.Entry(); entry != nil; entry = nil {
		artifact := map[string]any{
			"directory_identity":      FileIdentityJSONOrError(entry.DirectoryIdentity),
			"main_basename":           LocalBasenameBytes(entry.MainBasename),
			"main_identity":           FileIdentityJSONOrError(entry.MainIdentity),
			"expected_database_id":    HexID(&entry.ExpectedDatabaseID),
			"target_transaction_id":   DecimalUint(entry.TargetTransactionID),
			"target_commit_nonce":     HexID(&entry.TargetCommitNonce),
			"committed_target_length": DecimalUint(entry.CommittedTargetLength),
			"cleanup_error":           sdkCode(entry.CleanupError),
		}
		if entry.ObservedTailEndExclusive != nil {
			artifact["observed_tail_end_exclusive"] = DecimalUint(*entry.ObservedTailEndExclusive)
		}
		artifacts = append(artifacts, artifact)
		break
	}
	return map[string]any{"artifacts": artifacts}
}

// CoordinationCleanupJSON converts the coordination residue class to
// its wire value ({} when none).
func CoordinationCleanupJSON(value iprangedb.CoordinationCleanup) map[string]any {
	switch value {
	case iprangedb.CoordinationCleanupNone:
		return map[string]any{}
	case iprangedb.CoordinationCleanupCleanupGuard:
		return map[string]any{"kind": "cleanup_guard"}
	case iprangedb.CoordinationCleanupRetainedReaderCloseRequired:
		return map[string]any{"kind": "retained_reader_close_required"}
	case iprangedb.CoordinationCleanupRetainedWriterCloseRequired:
		return map[string]any{"kind": "retained_writer_close_required"}
	}
	return map[string]any{}
}

// SDKError maps one SDK failure with the given sentence outcome.
func SDKError(err error, outcome string) *rpc.HandlerError {
	var typed *iprangedb.Error
	if errors.As(err, &typed) {
		return rpc.NewHandlerError(sdkCode(typed.Code), outcome, typed.Error())
	}
	return rpc.NewHandlerError("io", outcome, err.Error())
}
