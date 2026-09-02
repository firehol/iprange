// Reclamation, publication residue inspection/resolution/removal, and
// the four offline maintenance kinds (Rust handlers/maintenance.rs
// parity): `iprange.v1.database.reclaim`,
// `iprange.v1.publication.inspect`, `iprange.v1.publication.resolve`,
// `iprange.v1.publication.residue.remove`,
// `iprange.v1.maintenance.list`, and `iprange.v1.maintenance.remove`.
//
// Residue authorities are retained inside one connection-scoped
// registry keyed by unpredictable tokens (the product executable
// serves exactly one JSON-RPC connection per process, mirroring the
// Rust thread-local registry). Maintenance list rows are collected
// per kind in caller order, bounded by max_entries, sorted by their
// canonical identity key, and published only after every scan
// succeeded; removal runs only on exact opaque entries the caller
// obtained unchanged from a list, never on synthesized basenames.

package handlers

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ---------------------------------------------------------------------------
// Shared numeric and identity wire decoding (maintenance entry forms and
// recovery candidates use the same exact shapes).
// ---------------------------------------------------------------------------

// decodeIdentityFromObject decodes one FILE_IDENTITY wire object
// (volume/file decimal pair) into the portable SDK identity of the
// current platform (Rust maintenance::identity_from_value).
func decodeIdentityFromObject(object rawObject) (iprangedb.FileIdentity, error) {
	var identity iprangedb.FileIdentity
	if err := exactMembers(object, []string{"volume", "file"}, nil, "identity"); err != nil {
		return identity, err
	}
	volume, err := canonicalU64FromRaw(object["volume"])
	if err != nil {
		return identity, fmt.Errorf("identity.volume must be a canonical unsigned decimal string")
	}
	file, err := canonicalU64FromRaw(object["file"])
	if err != nil {
		return identity, fmt.Errorf("identity.file must be a canonical unsigned decimal string")
	}
	identity.Kind = 1
	if runtime.GOOS == "windows" {
		identity.Kind = 2
	}
	binary.LittleEndian.PutUint64(identity.Bytes[0:8], volume)
	binary.LittleEndian.PutUint64(identity.Bytes[8:16], file)
	return identity, nil
}

// ---------------------------------------------------------------------------
// iprange.v1.database.reclaim
// ---------------------------------------------------------------------------

// ValidateDatabaseReclaimParams enforces the strict reclaim schema.
func ValidateDatabaseReclaimParams(params json.RawMessage) error {
	object, err := exactObject(params, "path", "max_transactions", "max_pages", "writer_budget")
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
	for _, field := range []string{"max_transactions", "max_pages"} {
		value, err := asDecimalString(object, field)
		if err != nil {
			return fmt.Errorf("%s must be a canonical unsigned decimal string", field)
		}
		if _, err := canonicalU64String(value); err != nil {
			return fmt.Errorf("%s must be a canonical unsigned decimal string", field)
		}
	}
	budget, err := memberObject(object, "writer_budget")
	if err != nil {
		return err
	}
	return validateWriterBudgetObject(budget)
}

// DatabaseReclaim implements iprange.v1.database.reclaim; the writer
// is closed on every terminal and the close facts are preserved in
// each error details (Rust database_reclaim).
func DatabaseReclaim(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
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
	maxTransactions, err := canonicalU64FromRaw(object["max_transactions"])
	if err != nil {
		return nil, rpc.InvalidParamsError("max_transactions must be a canonical unsigned decimal string")
	}
	maxPages, err := canonicalU64FromRaw(object["max_pages"])
	if err != nil {
		return nil, rpc.InvalidParamsError("max_pages must be a canonical unsigned decimal string")
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
	reclaim, reclaimErr := writer.Reclaim(maxTransactions, maxPages, st.Token())
	closeFacts, closeErr := CloseWriter(writer)
	if closeErr != nil {
		return nil, closeErr
	}

	if reclaimErr != nil {
		return nil, &rpc.HandlerError{
			Code: sdkCodeOf(reclaimErr), Outcome: "not_started", Message: reclaimErr.Error(),
			Details: map[string]any{"writer_close": closeFacts},
		}
	}
	reclamation := reclamationValue(reclaim)
	if reclaim.Outcome == iprangedb.ReclaimOutcomeCommitted {
		commit := reclaim.Commit
		if commit.Status != iprangedb.CommitCommitted || commit.Cause != nil {
			code := "io"
			message := "reclamation commit did not complete"
			if commit.Cause != nil {
				message = commit.Cause.Error()
				if typed, ok := commit.Cause.(*iprangedb.Error); ok {
					code = sdkCode(typed.Code)
				}
			}
			return nil, &rpc.HandlerError{
				Code: code, Outcome: CommitDurabilityName(commit.Status), Message: message,
				Details: map[string]any{"reclamation": reclamation, "writer_close": closeFacts},
			}
		}
	}
	if closeFacts["outcome"] == "close_incomplete" {
		outcome := "not_started"
		if reclaim.Outcome == iprangedb.ReclaimOutcomeCommitted {
			outcome = CommitDurabilityName(reclaim.Commit.Status)
		}
		return nil, &rpc.HandlerError{
			Code: "io", Outcome: outcome, Message: "live writer close is incomplete",
			Details: map[string]any{"reclamation": reclamation, "writer_close": closeFacts},
		}
	}
	return boundedResult(map[string]any{
		"method":       "iprange.v1.database.reclaim",
		"reclamation":  reclamation,
		"writer_close": closeFacts,
	})
}

// sdkCodeOf maps one SDK failure to its stable wire adapter code.
func sdkCodeOf(err error) string {
	if typed, ok := err.(*iprangedb.Error); ok {
		return sdkCode(typed.Code)
	}
	return "io"
}

// reclamationValue converts one SDK reclaim result to its wire object
// (results.py database.reclaim reclamation: an explicit kind
// discriminator).
func reclamationValue(result iprangedb.ReclaimResult) map[string]any {
	if result.Outcome != iprangedb.ReclaimOutcomeCommitted {
		return map[string]any{"kind": "no_change"}
	}
	commit, convErr := CommitResultJSON(&result.Commit)
	if convErr != nil {
		commit = map[string]any{"error": convErr.Message}
	}
	return map[string]any{
		"kind":              "commit",
		"transaction_count": DecimalUint(result.TransactionCount),
		"page_count":        DecimalUint(result.PageCount),
		"commit":            commit,
	}
}

// ---------------------------------------------------------------------------
// Publication residue inspection / resolution / removal
// ---------------------------------------------------------------------------

// residueHandleRegistry is the connection-scoped authority registry
// (Rust handlers/maintenance.rs RESIDUE_HANDLES thread-local): the
// product executable serves one connection per process on one worker
// goroutine, but the shared map is mutex-guarded so concurrent
// request handling stays safe.
var residueHandleRegistry = struct {
	sync.Mutex
	handles map[string]*iprangedb.PublicationResidueHandle
}{handles: make(map[string]*iprangedb.PublicationResidueHandle)}

// storeResidueHandle retains one authority under a fresh opaque token
// and returns the wire handle object.
func storeResidueHandle(handle *iprangedb.PublicationResidueHandle) (map[string]any, *rpc.HandlerError) {
	token, herr := rpc.NewHandle()
	if herr != nil {
		handle.Close()
		return nil, herr
	}
	residueHandleRegistry.Lock()
	residueHandleRegistry.handles[token] = handle
	residueHandleRegistry.Unlock()
	return residueHandleWire(token), nil
}

// takeResidueHandle consumes one retained authority by its token; a
// missing or already-consumed token is a product error.
func takeResidueHandle(token string) (*iprangedb.PublicationResidueHandle, *rpc.HandlerError) {
	residueHandleRegistry.Lock()
	handle := residueHandleRegistry.handles[token]
	if handle != nil {
		delete(residueHandleRegistry.handles, token)
	}
	residueHandleRegistry.Unlock()
	if handle == nil {
		return nil, rpc.NewHandlerError("handle_not_found", "not_started",
			"publication residue handle is unknown or already consumed")
	}
	return handle, nil
}

// residueHandleWire is the opaque wire form of one retained residue
// authority (spec publication.residue.remove handle).
func residueHandleWire(token string) map[string]any {
	return map[string]any{"kind": "publication_residue", "handle": token}
}

// residueHandleToken validates and extracts the token of one opaque
// handle object.
func residueHandleToken(object rawObject) (string, error) {
	if err := exactObjectRaw(object, "kind", "handle"); err != nil {
		return "", fmt.Errorf("handle must be an object")
	}
	kind, err := asString(object, "kind")
	if err != nil || kind != "publication_residue" {
		return "", fmt.Errorf("handle.kind is invalid")
	}
	token, err := asString(object, "handle")
	if err != nil {
		return "", fmt.Errorf("handle.handle must be a string")
	}
	if !validHandle(token) {
		return "", fmt.Errorf("handle.handle must be 32 lowercase hexadecimal characters")
	}
	return token, nil
}

// ValidatePublicationInspectParams enforces the strict inspect schema.
func ValidatePublicationInspectParams(params json.RawMessage) error {
	object, err := exactObject(params, "path")
	if err != nil {
		return err
	}
	path, err := asString(object, "path")
	if err != nil {
		return err
	}
	return validatePath(path)
}

// PublicationInspect implements iprange.v1.publication.inspect: one
// read-only residue inspection; the retained authority, when present,
// is stored under an opaque connection token for residue.remove.
func PublicationInspect(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingPath(path); herr != nil {
		return nil, herr
	}
	inspection, inspectErr := iprangedb.InspectPublicationResidue(path, st.Token())
	if inspectErr != nil {
		return nil, rpc.NewHandlerError(sdkCodeOf(inspectErr), "read_only_failure", inspectErr.Error())
	}
	converted := map[string]any{
		"directory_identity": FileIdentityJSONOrError(&inspection.DirectoryIdentity),
		"coordination":       residueCoordinationName(inspection.Coordination),
	}
	if inspection.CoordinationIdentity != nil {
		converted["coordination_identity"] = FileIdentityJSONOrError(inspection.CoordinationIdentity)
	}
	if inspection.Publication != nil {
		publication, convErr := PublicationResultJSON(inspection.Publication)
		if convErr != nil {
			inspection.Handle.Close()
			return nil, convErr
		}
		converted["publication"] = publication
	}
	if inspection.Handle != nil {
		handle, herr := storeResidueHandle(inspection.Handle)
		if herr != nil {
			return nil, herr
		}
		converted["handle"] = handle
	}
	return boundedResult(map[string]any{
		"method":     "iprange.v1.publication.inspect",
		"inspection": converted,
	})
}

// ValidatePublicationResolveParams enforces the strict resolve schema:
// path, resolution_mode, and an optional complete publication_result
// wired through the strict public decoder.
func ValidatePublicationResolveParams(params json.RawMessage) error {
	object, err := exactObjectOpt(params, []string{"path", "resolution_mode"}, []string{"publication_result"})
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
	mode, err := asString(object, "resolution_mode")
	if err != nil {
		return err
	}
	if mode != "complete" && mode != "remove" {
		return fmt.Errorf("resolution_mode must be complete or remove")
	}
	if raw, ok := object["publication_result"]; ok {
		if _, err := iprangedb.DecodePublicationResultJSON(raw); err != nil {
			return fmt.Errorf("publication_result is invalid: %v", err)
		}
	}
	return nil
}

// PublicationResolve implements iprange.v1.publication.resolve; the
// supplied complete publication result (when present) is decoded by
// the public SDK decoder before any destructive step runs (Rust
// publication_resolve).
func PublicationResolve(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	modeName, err := asString(object, "resolution_mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("resolution_mode must be complete or remove")
	}
	mode := iprangedb.PublicationResolutionComplete
	if modeName == "remove" {
		mode = iprangedb.PublicationResolutionRemove
	}
	var supplied *iprangedb.PublicationResult
	if raw, ok := object["publication_result"]; ok {
		decoded, decodeErr := iprangedb.DecodePublicationResultJSON(raw)
		if decodeErr != nil {
			return nil, rpc.InvalidParamsError("publication_result is invalid: " + decodeErr.Error())
		}
		supplied = decoded
	}
	result, resolveErr := iprangedb.ResolvePublication(path, supplied, mode, st.Token())
	if resolveErr != nil {
		return nil, rpc.NewHandlerError(sdkCodeOf(resolveErr), "not_started", resolveErr.Error())
	}
	publication, convErr := PublicationResultJSON(&result)
	if convErr != nil {
		return nil, convErr
	}
	if result.Cause != nil {
		outcome := publicationStatusOutcome(result.Publication)
		code := sdkCodeOf(result.Cause)
		if typed, ok := result.Cause.(*iprangedb.Error); ok {
			code = sdkCode(typed.Code)
		}
		return nil, &rpc.HandlerError{
			Code: code, Outcome: outcome,
			Message: "publication resolution failed: " + result.Cause.Error(),
			Details: map[string]any{"publication": publication},
		}
	}
	return boundedResult(map[string]any{
		"method":      "iprange.v1.publication.resolve",
		"publication": publication,
	})
}

// ValidatePublicationResidueRemoveParams enforces the opaque handle
// wire shape (kind publication_residue with a 32-hex handle).
func ValidatePublicationResidueRemoveParams(params json.RawMessage) error {
	object, err := exactObject(params, "handle")
	if err != nil {
		return err
	}
	handle, err := memberObject(object, "handle")
	if err != nil {
		return err
	}
	_, err = residueHandleToken(handle)
	return err
}

// PublicationResidueRemove implements
// iprange.v1.publication.residue.remove: the retained authority is
// consumed; an unknown token is a handle error and every factual
// field of a failed removal stays in the error details.
func PublicationResidueRemove(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	handle, err := memberObject(object, "handle")
	if err != nil {
		return nil, rpc.InvalidParamsError("handle must be an object")
	}
	token, err := residueHandleToken(handle)
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	authority, herr := takeResidueHandle(token)
	if herr != nil {
		return nil, herr
	}
	removal, removeErr := iprangedb.RemovePublicationResidue(authority, st.Token())
	if removeErr != nil {
		return nil, rpc.NewHandlerError(sdkCodeOf(removeErr), "not_started", removeErr.Error())
	}
	facts, herr := residueRemovalFacts(removal)
	if herr != nil {
		return nil, herr
	}
	if removal.Cause != nil {
		code := sdkCodeOf(removal.Cause)
		return nil, &rpc.HandlerError{
			Code: code, Outcome: "not_started",
			Message: "publication residue removal failed: " + removal.Cause.Error(),
			Details: map[string]any{"removal": facts},
		}
	}
	if removal.Handle != nil {
		next, herr := storeResidueHandle(removal.Handle)
		if herr != nil {
			return nil, herr
		}
		facts["handle"] = next
	}
	return boundedResult(map[string]any{
		"method":  "iprange.v1.publication.residue.remove",
		"removal": facts,
	})
}

// residueRemovalFacts converts the complete PublicationResidueRemoval
// facts to their wire object (results.py PUBLICATION_RESIDUE_REMOVAL).
func residueRemovalFacts(removal *iprangedb.PublicationResidueRemoval) (map[string]any, *rpc.HandlerError) {
	facts := map[string]any{
		"directory_identity":         FileIdentityJSONOrError(&removal.DirectoryIdentity),
		"coordination_identity":      FileIdentityJSONOrError(&removal.CoordinationIdentity),
		"later_coordination":         map[string]any{"kind": residueCoordinationName(removal.LaterCoordination)},
		"coordination_access_policy": AccessPolicyName(removal.CoordinationAccessPolicy),
		"cleanup":                    CleanupArtifactsJSON(removal.Cleanup),
		"coordination_cleanup":       CoordinationCleanupJSON(removal.CoordinationCleanup),
		"housekeeping":               HousekeepingJSON(removal.Housekeeping, removal.VisibleHousekeeping),
		"visible_housekeeping":       VisibleHousekeepingJSON(removal.VisibleHousekeeping),
	}
	if removal.Main != nil {
		facts["main"] = residueMainFacts(removal.Main)
	}
	return facts, nil
}

// residueMainFacts converts one retained destination main to its wire
// object.
func residueMainFacts(main *iprangedb.PublicationResidueMain) map[string]any {
	facts := map[string]any{
		"identity":      FileIdentityJSONOrError(&main.Identity),
		"content":       residueMainContentName(main.Content),
		"digest":        publicationDigestValue(&main.Digest),
		"access_policy": AccessPolicyName(main.AccessPolicy),
	}
	if main.Tuple != nil {
		facts["tuple"] = publicationTupleValue(main.Tuple)
	}
	return facts
}

// residueCoordinationName maps one residue coordination class to its
// wire name.
func residueCoordinationName(value iprangedb.PublicationResidueCoordination) string {
	switch value {
	case iprangedb.PublicationResidueCoordinationPublicationReservation:
		return "publication_reservation"
	case iprangedb.PublicationResidueCoordinationLiveSidecar:
		return "live_sidecar"
	case iprangedb.PublicationResidueCoordinationUnselectable:
		return "unselectable"
	default:
		return "absent"
	}
}

// residueMainContentName maps one retained main content class to its
// wire name.
func residueMainContentName(value iprangedb.PublicationResidueMainContent) string {
	if value == iprangedb.PublicationResidueMainContentV4 {
		return "v4"
	}
	return "other"
}

// publicationTupleValue converts one PublicationTuple to its wire
// object.
func publicationTupleValue(tuple *iprangedb.PublicationTuple) map[string]any {
	return map[string]any{
		"database_id":    HexID(&tuple.DatabaseID),
		"transaction_id": DecimalUint(tuple.TransactionID),
		"commit_nonce":   HexID(&tuple.CommitNonce),
	}
}

// publicationDigestValue converts one PublicationDigest to its wire
// object.
func publicationDigestValue(digest *iprangedb.PublicationDigest) map[string]any {
	return map[string]any{
		"byte_length": DecimalUint(digest.ByteLength),
		"sha512":      HexBytes(digest.SHA512[:]),
	}
}

// publicationStatusOutcome maps one SDK publication status to its wire
// outcome name.
func publicationStatusOutcome(status iprangedb.PublicationStatus) string {
	switch status {
	case iprangedb.PublicationPublished:
		return "published"
	case iprangedb.PublicationOutcomeUnknown:
		return "outcome_unknown"
	default:
		return "not_published"
	}
}

// collectedKind is one maintenance list row: the canonical identity
// key (attempt-id byte order or exact basename bytes) and the wire
// value; rows are sorted by key before publication (Rust
// maintenance_list).
type collectedKind struct {
	key   []byte
	value map[string]any
}

// ---------------------------------------------------------------------------
// iprange.v1.maintenance.list
// ---------------------------------------------------------------------------

// ValidateMaintenanceListParams enforces the strict list schema:
// directory, 1..4 unique kinds, max_entries 1..65536, and the JSONL
// output descriptor.
func ValidateMaintenanceListParams(params json.RawMessage) error {
	object, err := exactObject(params, "directory", "kinds", "max_entries", "output")
	if err != nil {
		return err
	}
	directory, err := asString(object, "directory")
	if err != nil {
		return err
	}
	if err := validatePath(directory); err != nil {
		return err
	}
	kinds, err := asStringArray(object, "kinds")
	if err != nil {
		return err
	}
	if len(kinds) == 0 || len(kinds) > 4 {
		return fmt.Errorf("kinds must contain 1 through 4 values")
	}
	seen := make(map[string]bool)
	for _, kind := range kinds {
		if kind != "scratch" && kind != "reservation" && kind != "publication_temp" && kind != "windows_housekeeping" {
			return fmt.Errorf("kind must be scratch, reservation, publication_temp, or windows_housekeeping")
		}
		if seen[kind] {
			return fmt.Errorf("kinds must be unique")
		}
		seen[kind] = true
	}
	maxEntries, err := asUint32(object, "max_entries")
	if err != nil || maxEntries == 0 {
		return fmt.Errorf("max_entries must be 1 through 65536")
	}
	output, err := memberObject(object, "output")
	if err != nil {
		return err
	}
	return validateOutputDescriptor(output)
}

// MaintenanceList implements iprange.v1.maintenance.list: every
// requested kind is scanned in caller order under the remaining entry
// budget, rows are published only after all scans succeeded, and each
// kind reports the number of delivered entries.
func MaintenanceList(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	directory, err := asString(object, "directory")
	if err != nil {
		return nil, rpc.InvalidParamsError("directory must be a string")
	}
	if herr := requireMaintenanceDirectory(directory); herr != nil {
		return nil, herr
	}
	kinds, err := asStringArray(object, "kinds")
	if err != nil {
		return nil, rpc.InvalidParamsError("kinds must be an array of strings")
	}
	maxEntries, err := asUint32(object, "max_entries")
	if err != nil || maxEntries == 0 {
		return nil, rpc.InvalidParamsError("max_entries must be 1 through 65536")
	}
	outputObject, merr := memberObject(object, "output")
	if merr != nil {
		return nil, rpc.InvalidParamsError("output must be an object")
	}
	outputPath, policy, budget, herr := decodeOutputDescriptor(outputObject)
	if herr != nil {
		return nil, herr
	}

	// Collection state: rows are written only after every scan
	// succeeded, so a refused or failed list never publishes a partial
	// destination (Rust maintenance_list).
	collected := make([][]collectedKind, 0, len(kinds))
	reports := make([]map[string]any, 0, len(kinds))
	total := 0
	for _, kind := range kinds {
		if total >= int(maxEntries) {
			break
		}
		remaining := int(maxEntries) - total
		var entries []collectedKind
		delivered, herr := scanMaintenanceKind(st, directory, kind, remaining, &entries)
		if herr != nil {
			return nil, herr
		}
		// Each kind is sorted by its canonical identity key: attempt-id
		// byte order for the fixed-format names, exact basename bytes for
		// windows housekeeping (Rust maintenance_list).
		sort.Slice(entries, func(i, j int) bool {
			return string(entries[i].key) < string(entries[j].key)
		})
		total += len(entries)
		reports = append(reports, map[string]any{"kind": kind, "entries": DecimalUint(delivered)})
		collected = append(collected, entries)
	}

	writer, herr := fileio.NewExportWriter(outputPath, policy, budget)
	if herr != nil {
		return nil, herr
	}
	for _, entries := range collected {
		for _, entry := range entries {
			row, err := json.Marshal(entry.value)
			if err != nil {
				writer.Abort()
				return nil, rpc.NewHandlerError("io", "not_started", "maintenance list row encoding failed")
			}
			if herr := writer.WriteLine(row, fileio.U64(0)); herr != nil {
				writer.Abort()
				return nil, herr
			}
		}
	}
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	return boundedResult(map[string]any{
		"method":  "iprange.v1.maintenance.list",
		"output":  outputFactsValue(facts),
		"reports": reports,
	})
}

// scanMaintenanceKind runs one kind-specific constant-memory scan and
// appends at most remaining entries to entries; the returned count is
// the number of delivered entries (a stopped scan is the bounded
// complete answer, exactly like the Rust list_reported).
func scanMaintenanceKind(st *rpc.SessionState, directory, kind string, remaining int, entries *[]collectedKind) (uint64, *rpc.HandlerError) {
	before := len(*entries)
	var herr *rpc.HandlerError
	switch kind {
	case "scratch":
		herr = listScratch(st, directory, remaining, entries)
	case "reservation":
		herr = listReservation(st, directory, remaining, entries)
	case "publication_temp":
		herr = listPublicationTemp(st, directory, remaining, entries)
	default:
		herr = listWindowsHousekeepingEntries(st, directory, remaining, entries)
	}
	if herr != nil {
		return 0, herr
	}
	return uint64(len(*entries) - before), nil
}

// ---------------------------------------------------------------------------
// Kind-specific constant-memory scans (maintenance.list)
// ---------------------------------------------------------------------------

// listScratch scans one scratch directory; the sink stops at the
// remaining budget and a stopped scan is the bounded complete answer
// (Rust ScratchCollector + list_reported).
func listScratch(st *rpc.SessionState, directory string, remaining int, entries *[]collectedKind) *rpc.HandlerError {
	_, err := iprangedb.ListAbandonedScratch(directory, st.Token(), func(entry *iprangedb.AbandonedScratchEntry) error {
		if len(*entries) >= remaining {
			return iprangedb.ErrMaintenanceSinkStop
		}
		*entries = append(*entries, collectedKind{key: scratchSortKey(entry), value: scratchEntryValue(directory, entry)})
		return nil
	})
	if err == nil || isStoppedBySink(err) {
		return nil
	}
	return readError(err)
}

// scratchSortKey is the attempt-id bytes plus the big-endian ordinal:
// canonical basename order equals this byte order (Rust
// ScratchCollector).
func scratchSortKey(entry *iprangedb.AbandonedScratchEntry) []byte {
	key := make([]byte, 0, 20)
	key = append(key, entry.AttemptID[:]...)
	var ordinal [4]byte
	binary.BigEndian.PutUint32(ordinal[:], entry.Ordinal)
	return append(key, ordinal[:]...)
}

// listReservation scans the abandoned private reservations of one
// directory (Rust ReservationCollector).
func listReservation(st *rpc.SessionState, directory string, remaining int, entries *[]collectedKind) *rpc.HandlerError {
	_, err := iprangedb.ListAbandonedReservationArtifacts(directory, st.Token(), func(entry *iprangedb.AbandonedReservationEntry) error {
		if len(*entries) >= remaining {
			return iprangedb.ErrMaintenanceSinkStop
		}
		*entries = append(*entries, collectedKind{key: entry.PublicationAttemptID[:], value: reservationEntryValue(directory, entry)})
		return nil
	})
	if err == nil || isStoppedBySink(err) {
		return nil
	}
	return readError(err)
}

// listPublicationTemp scans the abandoned private publication outputs
// of one directory (Rust PublicationTempCollector).
func listPublicationTemp(st *rpc.SessionState, directory string, remaining int, entries *[]collectedKind) *rpc.HandlerError {
	_, err := iprangedb.ListAbandonedPublicationTemps(directory, st.Token(), func(entry *iprangedb.AbandonedPublicationTempEntry) error {
		if len(*entries) >= remaining {
			return iprangedb.ErrMaintenanceSinkStop
		}
		*entries = append(*entries, collectedKind{key: entry.PublicationAttemptID[:], value: publicationTempEntryValue(directory, entry)})
		return nil
	})
	if err == nil || isStoppedBySink(err) {
		return nil
	}
	return readError(err)
}

// listWindowsHousekeepingEntries streams one offline GC housekeeping
// scan (Rust HousekeepingCollector); the sort key is the exact
// basename bytes.
func listWindowsHousekeepingEntries(st *rpc.SessionState, directory string, remaining int, entries *[]collectedKind) *rpc.HandlerError {
	_, err := iprangedb.ListWindowsHousekeeping(directory, st.Token(), func(entry *iprangedb.WindowsHousekeepingEntry) error {
		if len(*entries) >= remaining {
			return iprangedb.ErrMaintenanceSinkStop
		}
		*entries = append(*entries, collectedKind{key: entry.Basename, value: housekeepingEntryValue(directory, entry)})
		return nil
	})
	if err == nil || isStoppedBySink(err) {
		return nil
	}
	return readError(err)
}

// isStoppedBySink reports whether the scan ended with the SDK
// StoppedBySink class (the bounded complete answer).
func isStoppedBySink(err error) bool {
	var typed *iprangedb.Error
	return errors.As(err, &typed) && typed.Code == iprangedb.ErrorStoppedBySink
}

// scratchEntryValue converts one scratch entry to its wire list row
// (Rust scratch_entry_value).
func scratchEntryValue(directory string, entry *iprangedb.AbandonedScratchEntry) map[string]any {
	return map[string]any{
		"kind":               "scratch",
		"directory":          directory,
		"directory_identity": FileIdentityJSONOrError(&entry.DirectoryIdentity),
		"artifact_identity":  FileIdentityJSONOrError(&entry.ArtifactIdentity),
		"attempt_id":         HexID(&entry.AttemptID),
		"ordinal":            entry.Ordinal,
		"authentication":     scratchAuthenticationValue(entry.Authentication),
	}
}

// scratchAuthenticationValue converts one ownership-header
// authentication class to its wire object.
func scratchAuthenticationValue(auth iprangedb.AbandonedScratchAuthentication) map[string]any {
	if auth.Authenticated {
		owner := "validation"
		if auth.Owner == iprangedb.ScratchOwnerRecovery {
			owner = "recovery"
		}
		return map[string]any{"kind": "authenticated", "owner": owner}
	}
	return map[string]any{"kind": "unauthenticated"}
}

// reservationEntryValue converts one private reservation entry to its
// wire list row (Rust reservation_entry_value); the authenticated
// evidence is present only when the scan could read it.
func reservationEntryValue(directory string, entry *iprangedb.AbandonedReservationEntry) map[string]any {
	value := map[string]any{
		"kind":                   "reservation",
		"directory":              directory,
		"directory_identity":     FileIdentityJSONOrError(&entry.DirectoryIdentity),
		"artifact_identity":      FileIdentityJSONOrError(&entry.ArtifactIdentity),
		"publication_attempt_id": HexID(&entry.PublicationAttemptID),
	}
	if entry.Evidence != nil {
		evidence := map[string]any{
			"policy": reservationPolicyName(entry.Evidence.Policy),
			"phase":  reservationPhaseName(entry.Evidence.Phase),
			"output": map[string]any{
				"identity": FileIdentityJSONOrError(&entry.Evidence.Output.Identity),
				"tuple":    publicationTupleValue(&entry.Evidence.Output.Tuple),
				"digest":   publicationDigestValue(&entry.Evidence.Output.Digest),
			},
		}
		if entry.Evidence.Previous != nil {
			evidence["previous"] = map[string]any{
				"identity": FileIdentityJSONOrError(&entry.Evidence.Previous.Identity),
				"digest":   publicationDigestValue(&entry.Evidence.Previous.Digest),
			}
		}
		value["evidence"] = evidence
	}
	return value
}

// reservationPolicyName maps one reservation policy to its wire name.
func reservationPolicyName(policy iprangedb.AbandonedReservationPolicy) string {
	switch policy {
	case iprangedb.AbandonedReservationPolicyReplaceExisting:
		return "replace_existing"
	case iprangedb.AbandonedReservationPolicyReplaceExistingNoRollback:
		return "replace_existing_no_rollback"
	default:
		return "fail_if_exists"
	}
}

// reservationPhaseName maps one reservation phase to its wire name.
func reservationPhaseName(phase iprangedb.AbandonedReservationPhase) string {
	if phase == iprangedb.AbandonedReservationPhaseMainMayHaveBeenAttempted {
		return "main_may_have_been_attempted"
	}
	return "prepared"
}

// publicationTempEntryValue converts one private publication output
// entry to its wire list row (Rust publication_temp_entry_value); the
// tuple and digest evidence are both present or both absent.
func publicationTempEntryValue(directory string, entry *iprangedb.AbandonedPublicationTempEntry) map[string]any {
	value := map[string]any{
		"kind":                   "publication_temp",
		"directory":              directory,
		"directory_identity":     FileIdentityJSONOrError(&entry.DirectoryIdentity),
		"artifact_identity":      FileIdentityJSONOrError(&entry.ArtifactIdentity),
		"publication_attempt_id": HexID(&entry.PublicationAttemptID),
	}
	if entry.Tuple != nil {
		value["tuple"] = publicationTupleValue(entry.Tuple)
	}
	if entry.Digest != nil {
		value["digest"] = publicationDigestValue(entry.Digest)
	}
	return value
}

// housekeepingEntryValue converts one GC housekeeping candidate to
// its wire list row (Rust housekeeping_entry_value).
func housekeepingEntryValue(directory string, entry *iprangedb.WindowsHousekeepingEntry) map[string]any {
	value := map[string]any{
		"kind":               "windows_housekeeping",
		"directory":          directory,
		"directory_identity": FileIdentityJSONOrError(&entry.DirectoryIdentity),
		"candidate_kind":     housekeepingCandidateKindName(entry.CandidateKind),
		"basename_encoding":  entry.BasenameEncoding,
		"basename":           Base64Padded(entry.Basename),
	}
	if entry.Identity != nil {
		value["identity"] = FileIdentityJSONOrError(entry.Identity)
	}
	if entry.AttemptID != nil {
		value["attempt_id"] = HexID(entry.AttemptID)
	}
	if entry.Ordinal != nil {
		value["ordinal"] = *entry.Ordinal
	}
	if entry.Artifact != nil {
		value["artifact"] = HousekeepingArtifactJSON(entry.Artifact)
	}
	if entry.Problem != nil {
		value["problem"] = PublicationProblemJSON(entry.Problem)
	}
	return value
}

// housekeepingCandidateKindName maps one GC candidate class to its
// wire name.
func housekeepingCandidateKindName(kind iprangedb.WindowsHousekeepingCandidateKind) string {
	if kind == iprangedb.WindowsHousekeepingCandidateInertPayload {
		return "inert_payload"
	}
	return "envelope"
}

// ---------------------------------------------------------------------------
// iprange.v1.maintenance.remove
// ---------------------------------------------------------------------------

// ValidateMaintenanceRemoveParams enforces that remove takes exactly
// one opaque entry object; the strict per-kind field validation runs
// in the handler so every synthesized entry is refused before any
// destructive step (Rust validate_maintenance_remove).
func ValidateMaintenanceRemoveParams(params json.RawMessage) error {
	object, err := exactObject(params, "entry")
	if err != nil {
		return err
	}
	entry, err := memberObject(object, "entry")
	if err != nil {
		return err
	}
	if len(entry) == 0 {
		return fmt.Errorf("entry must be an object")
	}
	return nil
}

// MaintenanceRemove implements iprange.v1.maintenance.remove: one
// exact entry obtained unchanged from maintenance.list, dispatched by
// its kind; there is no arbitrary path-delete form.
func MaintenanceRemove(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	entry, err := memberObject(object, "entry")
	if err != nil {
		return nil, rpc.InvalidParamsError("entry must be an object")
	}
	kind, err := asString(entry, "kind")
	if err != nil {
		return nil, rpc.InvalidParamsError("entry.kind must be a string")
	}
	switch kind {
	case "scratch":
		directory, directoryIdentity, artifactIdentity, attemptID, ordinal, herr := scratchRemoveFields(entry)
		if herr != nil {
			return nil, herr
		}
		removal, err := iprangedb.RemoveAbandonedScratch(directory, directoryIdentity, attemptID, ordinal, artifactIdentity, st.Token())
		if err != nil {
			return nil, SDKError(err, "not_started")
		}
		return removalResult(&removal)
	case "reservation":
		directory, directoryIdentity, artifactIdentity, attemptID, herr := reservationRemoveFields(entry)
		if herr != nil {
			return nil, herr
		}
		removal, err := iprangedb.RemoveAbandonedReservationArtifact(directory, directoryIdentity, attemptID, artifactIdentity, st.Token())
		if err != nil {
			return nil, SDKError(err, "not_started")
		}
		return removalResult(&removal)
	case "publication_temp":
		directory, directoryIdentity, artifactIdentity, attemptID, tuple, digest, herr := publicationTempRemoveFields(entry)
		if herr != nil {
			return nil, herr
		}
		removal, err := iprangedb.RemoveAbandonedPublicationTemp(directory, directoryIdentity, attemptID, artifactIdentity, tuple, digest, st.Token())
		if err != nil {
			return nil, SDKError(err, "not_started")
		}
		return removalResult(&removal)
	case "windows_housekeeping":
		directory, directoryIdentity, attemptID, ordinal, envelopeIdentity, herr := housekeepingRemoveFields(entry)
		if herr != nil {
			return nil, herr
		}
		removal, err := iprangedb.RemoveWindowsHousekeeping(directory, directoryIdentity, attemptID, ordinal, envelopeIdentity, nil, st.Token())
		if err != nil {
			return nil, SDKError(err, "not_started")
		}
		return windowsRemovalResult(&removal)
	default:
		return nil, rpc.InvalidParamsError("entry.kind is invalid")
	}
}

// removalResult converts one abandoned-artifact removal terminal
// (Rust removal_result).
func removalResult(removal *iprangedb.AbandonedArtifactRemoval) (any, *rpc.HandlerError) {
	facts := removalFacts(removal)
	if removal.Cause != nil {
		return nil, &rpc.HandlerError{
			Code: sdkCodeOf(removal.Cause), Outcome: "not_started",
			Message: "maintenance removal failed: " + removal.Cause.Error(),
			Details: map[string]any{"removal": facts},
		}
	}
	return boundedResult(map[string]any{"method": "iprange.v1.maintenance.remove", "removal": facts})
}

// removalFacts converts the abandoned-artifact removal facts to their
// wire object (Rust removal_facts).
func removalFacts(removal *iprangedb.AbandonedArtifactRemoval) map[string]any {
	return map[string]any{
		"source_present":       removal.SourcePresent,
		"cleanup_state":        cleanupStateName(removal.CleanupState),
		"housekeeping":         HousekeepingJSON(removal.Housekeeping, removal.VisibleHousekeeping),
		"visible_housekeeping": VisibleHousekeepingJSON(removal.VisibleHousekeeping),
	}
}

// windowsRemovalResult converts one GC housekeeping removal terminal
// (Rust windows_removal_result).
func windowsRemovalResult(removal *iprangedb.WindowsHousekeepingRemoval) (any, *rpc.HandlerError) {
	facts := map[string]any{
		"housekeeping":         HousekeepingJSON(removal.Housekeeping, removal.VisibleHousekeeping),
		"visible_housekeeping": VisibleHousekeepingJSON(removal.VisibleHousekeeping),
	}
	if removal.Cause != nil {
		return nil, &rpc.HandlerError{
			Code: sdkCodeOf(removal.Cause), Outcome: "not_started",
			Message: "maintenance removal failed: " + removal.Cause.Error(),
			Details: map[string]any{"removal": facts},
		}
	}
	return boundedResult(map[string]any{"method": "iprange.v1.maintenance.remove", "removal": facts})
}

// scratchRemoveFields validates and extracts the exact scratch entry
// fields (Rust scratch_remove_fields).
func scratchRemoveFields(entry rawObject) (string, iprangedb.FileIdentity, iprangedb.FileIdentity, [16]byte, uint32, *rpc.HandlerError) {
	if err := exactObjectRaw(entry, "kind", "directory", "directory_identity", "artifact_identity", "attempt_id", "ordinal", "authentication"); err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, rpc.InvalidParamsError(err.Error())
	}
	directory, herr := removeDirectoryField(entry)
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, herr
	}
	directoryIdentity, herr := identityFromWire(entry, "directory_identity")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, herr
	}
	artifactIdentity, herr := identityFromWire(entry, "artifact_identity")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, herr
	}
	attemptID, herr := hex16Member(entry, "attempt_id")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, herr
	}
	ordinal, err := asUint32(entry, "ordinal")
	if err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, rpc.InvalidParamsError("entry.ordinal must be a u32 integer")
	}
	auth, err := memberObject(entry, "authentication")
	if err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, rpc.InvalidParamsError("entry.authentication must be an object")
	}
	if err := validateScratchAuthentication(auth); err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, 0, rpc.InvalidParamsError(err.Error())
	}
	return directory, directoryIdentity, artifactIdentity, attemptID, ordinal, nil
}

// zeroIdentity is the all-zero SDK identity used on malformed entries.
func zeroIdentity() iprangedb.FileIdentity { return iprangedb.FileIdentity{} }

// removeDirectoryField extracts and path-validates entry.directory.
func removeDirectoryField(entry rawObject) (string, *rpc.HandlerError) {
	directory, err := asString(entry, "directory")
	if err != nil {
		return "", rpc.InvalidParamsError("entry.directory must be a string")
	}
	if err := validatePath(directory); err != nil {
		return "", rpc.InvalidParamsError("entry.directory is invalid")
	}
	return directory, nil
}

// validateScratchAuthentication enforces the ownership-header wire
// object (kind authenticated with a validation/recovery owner, or
// unauthenticated).
func validateScratchAuthentication(auth rawObject) error {
	kind, err := asString(auth, "kind")
	if err != nil {
		return fmt.Errorf("entry.authentication.kind is invalid")
	}
	switch kind {
	case "authenticated":
		if err := exactObjectRaw(auth, "kind", "owner"); err != nil {
			return err
		}
		owner, err := asString(auth, "owner")
		if err != nil || owner != "validation" && owner != "recovery" {
			return fmt.Errorf("entry.authentication.owner is invalid")
		}
		return nil
	case "unauthenticated":
		return exactObjectRaw(auth, "kind")
	}
	return fmt.Errorf("entry.authentication.kind is invalid")
}

// identityFromWire decodes entry.<field> as one FILE_IDENTITY object.
func identityFromWire(entry rawObject, field string) (iprangedb.FileIdentity, *rpc.HandlerError) {
	object, err := memberObject(entry, field)
	if err != nil {
		return iprangedb.FileIdentity{}, rpc.InvalidParamsError("entry." + field + " must be an object")
	}
	identity, err := decodeIdentityFromObject(object)
	if err != nil {
		return iprangedb.FileIdentity{}, rpc.InvalidParamsError("entry." + field + " is invalid: " + err.Error())
	}
	return identity, nil
}

// hex16Member decodes entry.<field> as a 32-lowercase-hex identity.
func hex16Member(entry rawObject, field string) ([16]byte, *rpc.HandlerError) {
	value, err := hex16FromWire(entry, field)
	if err != nil {
		return [16]byte{}, rpc.InvalidParamsError("entry." + field + " must be 32 lowercase hexadecimal characters")
	}
	return value, nil
}

// reservationRemoveFields validates and extracts the exact
// reservation entry fields including its authenticated evidence
// (Rust reservation_remove_fields).
func reservationRemoveFields(entry rawObject) (string, iprangedb.FileIdentity, iprangedb.FileIdentity, [16]byte, *rpc.HandlerError) {
	if err := exactObjectRaw(entry, "kind", "directory", "directory_identity", "artifact_identity", "publication_attempt_id", "evidence"); err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, rpc.InvalidParamsError(err.Error())
	}
	directory, herr := removeDirectoryField(entry)
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, herr
	}
	directoryIdentity, herr := identityFromWire(entry, "directory_identity")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, herr
	}
	artifactIdentity, herr := identityFromWire(entry, "artifact_identity")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, herr
	}
	attemptID, herr := hex16Member(entry, "publication_attempt_id")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, herr
	}
	evidence, err := memberObject(entry, "evidence")
	if err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, rpc.InvalidParamsError("entry.evidence must be an object")
	}
	if herr := validateReservationEvidence(evidence); herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, herr
	}
	return directory, directoryIdentity, artifactIdentity, attemptID, nil
}

// validateReservationEvidence enforces the exact evidence object:
// policy, phase, output (identity, tuple, digest), and previous
// (identity, digest); null is never a valid absent form (Rust
// reservation_remove_fields).
func validateReservationEvidence(evidence rawObject) *rpc.HandlerError {
	if err := exactObjectRaw(evidence, "policy", "phase", "output", "previous"); err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	for _, field := range []string{"policy", "phase"} {
		if _, err := asString(evidence, field); err != nil {
			return rpc.InvalidParamsError("entry.evidence." + field + " must be a string")
		}
	}
	output, err := memberObject(evidence, "output")
	if err != nil {
		return rpc.InvalidParamsError("entry.evidence.output must be an object")
	}
	if err := exactObjectRaw(output, "identity", "tuple", "digest"); err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	identity, err := memberObject(output, "identity")
	if err != nil {
		return rpc.InvalidParamsError("entry.evidence.output.identity must be an object")
	}
	if err := validateIdentityObject(identity); err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	tuple, err := memberObject(output, "tuple")
	if err != nil {
		return rpc.InvalidParamsError("entry.evidence.output.tuple must be an object")
	}
	if err := validateTupleObject(tuple); err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	digest, err := memberObject(output, "digest")
	if err != nil {
		return rpc.InvalidParamsError("entry.evidence.output.digest must be an object")
	}
	if err := validateDigestObject(digest); err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	previous, err := memberObject(evidence, "previous")
	if err != nil {
		return rpc.InvalidParamsError("entry.evidence.previous must be an object")
	}
	if err := exactObjectRaw(previous, "identity", "digest"); err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	prevIdentity, err := memberObject(previous, "identity")
	if err != nil {
		return rpc.InvalidParamsError("entry.evidence.previous.identity must be an object")
	}
	if err := validateIdentityObject(prevIdentity); err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	prevDigest, err := memberObject(previous, "digest")
	if err != nil {
		return rpc.InvalidParamsError("entry.evidence.previous.digest must be an object")
	}
	return rpcInvalidOrNil(validateDigestObject(prevDigest))
}

func rpcInvalidOrNil(err error) *rpc.HandlerError {
	if err != nil {
		return rpc.InvalidParamsError(err.Error())
	}
	return nil
}

// validateTupleObject enforces one publication tuple wire object.
func validateTupleObject(tuple rawObject) error {
	if err := exactObjectRaw(tuple, "database_id", "transaction_id", "commit_nonce"); err != nil {
		return err
	}
	if err := validateHex16Member(tuple, "database_id"); err != nil {
		return err
	}
	if _, err := canonicalU64RawMember(tuple, "transaction_id"); err != nil {
		return fmt.Errorf("tuple.transaction_id must be a canonical unsigned decimal string")
	}
	return validateHex16Member(tuple, "commit_nonce")
}

func validateHex16Member(object rawObject, field string) error {
	text, err := asHexString(object, field)
	if err != nil || len(text) != 32 {
		return fmt.Errorf("%s must be 32 lowercase hexadecimal characters", field)
	}
	return nil
}

// validateDigestObject enforces one publication digest wire object.
func validateDigestObject(digest rawObject) error {
	if err := exactObjectRaw(digest, "byte_length", "sha512"); err != nil {
		return err
	}
	if _, err := canonicalU64RawMember(digest, "byte_length"); err != nil {
		return fmt.Errorf("digest.byte_length must be a canonical unsigned decimal string")
	}
	sha, err := asHexString(digest, "sha512")
	if err != nil || len(sha) != 128 {
		return fmt.Errorf("digest.sha512 must be 128 lowercase hexadecimal characters")
	}
	return nil
}

// publicationTempRemoveFields validates and extracts the exact
// publication-temp entry fields; the tuple and digest evidence are
// required objects exactly like the Rust remove fields.
func publicationTempRemoveFields(entry rawObject) (string, iprangedb.FileIdentity, iprangedb.FileIdentity, [16]byte, *iprangedb.PublicationTuple, *iprangedb.PublicationDigest, *rpc.HandlerError) {
	if err := exactObjectRaw(entry, "kind", "directory", "directory_identity", "artifact_identity", "publication_attempt_id", "tuple", "digest"); err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, rpc.InvalidParamsError(err.Error())
	}
	directory, herr := removeDirectoryField(entry)
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, herr
	}
	directoryIdentity, herr := identityFromWire(entry, "directory_identity")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, herr
	}
	artifactIdentity, herr := identityFromWire(entry, "artifact_identity")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, herr
	}
	attemptID, herr := hex16Member(entry, "publication_attempt_id")
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, herr
	}
	tupleRaw, err := memberObject(entry, "tuple")
	if err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, rpc.InvalidParamsError("entry.tuple must be an object")
	}
	tuple, herr := decodeTupleObject(tupleRaw)
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, herr
	}
	digestRaw, err := memberObject(entry, "digest")
	if err != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, rpc.InvalidParamsError("entry.digest must be an object")
	}
	digest, herr := decodeDigestObject(digestRaw)
	if herr != nil {
		return "", zeroIdentity(), zeroIdentity(), [16]byte{}, nil, nil, herr
	}
	return directory, directoryIdentity, artifactIdentity, attemptID, tuple, digest, nil
}

// decodeTupleObject converts one validated tuple wire object into the
// SDK tuple.
func decodeTupleObject(tuple rawObject) (*iprangedb.PublicationTuple, *rpc.HandlerError) {
	databaseID, err := hex16FromWire(tuple, "database_id")
	if err != nil {
		return nil, rpc.InvalidParamsError("entry.tuple.database_id is invalid")
	}
	transactionID, err := canonicalU64FromRaw(tuple["transaction_id"])
	if err != nil {
		return nil, rpc.InvalidParamsError("entry.tuple.transaction_id must be a canonical unsigned decimal string")
	}
	commitNonce, err := hex16FromWire(tuple, "commit_nonce")
	if err != nil {
		return nil, rpc.InvalidParamsError("entry.tuple.commit_nonce is invalid")
	}
	return &iprangedb.PublicationTuple{DatabaseID: databaseID, TransactionID: transactionID, CommitNonce: commitNonce}, nil
}

// decodeDigestObject converts one validated digest wire object into
// the SDK digest.
func decodeDigestObject(digest rawObject) (*iprangedb.PublicationDigest, *rpc.HandlerError) {
	byteLength, err := canonicalU64FromRaw(digest["byte_length"])
	if err != nil {
		return nil, rpc.InvalidParamsError("entry.digest.byte_length must be a canonical unsigned decimal string")
	}
	sha, err := asHexString(digest, "sha512")
	if err != nil || len(sha) != 128 {
		return nil, rpc.InvalidParamsError("entry.digest.sha512 must be 128 lowercase hexadecimal characters")
	}
	var digestBytes [64]byte
	for i := 0; i < 64; i++ {
		digestBytes[i] = hexNibble(sha[2*i])<<4 | hexNibble(sha[2*i+1])
	}
	return &iprangedb.PublicationDigest{ByteLength: byteLength, SHA512: digestBytes}, nil
}

// housekeepingRemoveFields validates and extracts the exact GC
// housekeeping entry fields; the payload evidence is never supplied
// by this method (Rust housekeeping_remove_fields with no payload
// identity).
func housekeepingRemoveFields(entry rawObject) (string, iprangedb.FileIdentity, [16]byte, uint32, iprangedb.FileIdentity, *rpc.HandlerError) {
	if err := exactObjectRaw(entry, "kind", "directory", "directory_identity", "candidate_kind", "basename_encoding", "basename", "identity", "attempt_id", "ordinal", "artifact", "problem"); err != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError(err.Error())
	}
	directory, herr := removeDirectoryField(entry)
	if herr != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), herr
	}
	directoryIdentity, herr := identityFromWire(entry, "directory_identity")
	if herr != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), herr
	}
	candidateKind, err := asString(entry, "candidate_kind")
	if err != nil || candidateKind != "envelope" && candidateKind != "inert_payload" {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError("entry.candidate_kind is invalid")
	}
	if _, err := asUint32(entry, "basename_encoding"); err != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError("entry.basename_encoding must be an integer")
	}
	basename, err := asString(entry, "basename")
	if err != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError("entry.basename must be a string")
	}
	if _, err := base64Decode(basename); err != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError("entry.basename must be valid base64")
	}
	identity, herr := identityFromWire(entry, "identity")
	if herr != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), herr
	}
	attemptID, herr := hex16Member(entry, "attempt_id")
	if herr != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), herr
	}
	ordinal, err := asUint32(entry, "ordinal")
	if err != nil {
		return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError("entry.ordinal must be a u32 integer")
	}
	if raw, ok := entry["artifact"]; ok && !isRawNull(raw) {
		if _, err := decodeObject(raw); err != nil {
			return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError("entry.artifact must be an object")
		}
	}
	if raw, ok := entry["problem"]; ok && !isRawNull(raw) {
		if _, err := decodeObject(raw); err != nil {
			return "", zeroIdentity(), [16]byte{}, 0, zeroIdentity(), rpc.InvalidParamsError("entry.problem must be an object")
		}
	}
	return directory, directoryIdentity, attemptID, ordinal, identity, nil
}

// ---------------------------------------------------------------------------
// Shared maintenance/recovery conversion helpers
// ---------------------------------------------------------------------------

// cleanupStateName maps one SDK cleanup state to its wire name.
func cleanupStateName(state iprangedb.CleanupState) string {
	if state == iprangedb.CleanupStateResiduePossible {
		return "residue_possible"
	}
	return "clean"
}

// privateOutputAttemptValue converts one private output attempt to
// its wire object (Rust maintenance::private_output_attempt_value);
// absent identities degrade to the error object form.
func privateOutputAttemptValue(attempt *iprangedb.PrivateOutputAttempt) map[string]any {
	value := map[string]any{
		"publication_attempt_id": HexID(&attempt.PublicationAttemptID),
		"directory_identity":     FileIdentityJSONOrError(&attempt.DirectoryIdentity),
		"basename_encoding":      attempt.BasenameEncoding,
		"basename":               string(attempt.Basename),
		"creation_security":      CreationSecurityJSON(&attempt.CreationSecurity),
	}
	if attempt.IdentityPresent {
		value["identity"] = FileIdentityJSONOrError(&attempt.Identity)
	} else {
		value["identity"] = nil
	}
	return value
}

// validateOutputDescriptor enforces the JSONL-only output descriptor
// shared by validate, recover, and maintenance.list (Rust
// maintenance::validate_output_descriptor): csv is unsupported for
// these methods because their rows carry nested evidence.
func validateOutputDescriptor(output rawObject) error {
	if err := exactObjectRaw(output, "path", "format", "publication_policy", "result_budget"); err != nil {
		return err
	}
	path, err := asString(output, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	format, err := asString(output, "format")
	if err != nil || format != "jsonl" {
		return fmt.Errorf("format must be jsonl")
	}
	policy, err := asString(output, "publication_policy")
	if err != nil {
		return err
	}
	if !validPublicationPolicyName(policy) {
		return fmt.Errorf("publication_policy is invalid")
	}
	budgetRaw, ok := output["result_budget"]
	if !ok {
		return fmt.Errorf("missing member \"result_budget\"")
	}
	return validateResultBudget(budgetRaw)
}

// decodeOutputDescriptor converts one validated JSONL output
// descriptor into the export destination facts.
func decodeOutputDescriptor(output rawObject) (string, iprangedb.PublicationPolicy, fileio.ExportBudget, *rpc.HandlerError) {
	path, err := asString(output, "path")
	if err != nil {
		return "", 0, fileio.ExportBudget{}, rpc.InvalidParamsError("output.path must be a string")
	}
	policyName, err := asString(output, "publication_policy")
	if err != nil {
		return "", 0, fileio.ExportBudget{}, rpc.InvalidParamsError("publication_policy is invalid")
	}
	budgetObj, err := memberObject(output, "result_budget")
	if err != nil {
		return "", 0, fileio.ExportBudget{}, rpc.InvalidParamsError("result_budget must be an object")
	}
	maxRows, err := canonicalU64FromRaw(budgetObj["max_rows"])
	if err != nil {
		return "", 0, fileio.ExportBudget{}, rpc.InvalidParamsError("result_budget.max_rows is invalid")
	}
	maxBytes, err := canonicalU64FromRaw(budgetObj["max_output_bytes"])
	if err != nil {
		return "", 0, fileio.ExportBudget{}, rpc.InvalidParamsError("result_budget.max_output_bytes is invalid")
	}
	openFiles, err := asUint32(budgetObj, "max_open_files")
	if err != nil {
		return "", 0, fileio.ExportBudget{}, rpc.InvalidParamsError("result_budget.max_open_files must be a u32 integer")
	}
	return path, policyByName(policyName), fileio.ExportBudget{MaxRows: maxRows, MaxOutputBytes: maxBytes, MaxOpenFiles: openFiles}, nil
}

// outputFactsValue converts one published export to its wire output
// facts object.
func outputFactsValue(facts *fileio.ExportFacts) map[string]any {
	return map[string]any{
		"path":   facts.Path,
		"sha256": facts.SHA256,
		"bytes":  DecimalUint(facts.Bytes),
		"rows":   DecimalUint(facts.Rows),
	}
}

// requireMaintenanceDirectory mirrors the Rust list preflight: the
// maintenance directory must exist and be a directory.
func requireMaintenanceDirectory(path string) *rpc.HandlerError {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("maintenance directory is not a directory: %s", path))
	case os.IsNotExist(err):
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("maintenance directory does not exist: %s", path))
	default:
		return rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("inspect maintenance directory %s: %v", path, err))
	}
}

// requireExistingPath mirrors the Rust inspect/validate preflight: the
// target path must exist.
func requireExistingPath(path string) *rpc.HandlerError {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return nil
	case os.IsNotExist(err):
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("path does not exist: %s", path))
	default:
		return rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("cannot inspect %s: %v", path, err))
	}
}

// RegisterMaintenance installs the reclamation, publication
// resolution, and offline maintenance handler family.
func RegisterMaintenance() {
	rpc.Register("iprange.v1.database.reclaim", ValidateDatabaseReclaimParams, DatabaseReclaim)
	rpc.Register("iprange.v1.maintenance.list", ValidateMaintenanceListParams, MaintenanceList)
	rpc.Register("iprange.v1.maintenance.remove", ValidateMaintenanceRemoveParams, MaintenanceRemove)
	rpc.Register("iprange.v1.publication.inspect", ValidatePublicationInspectParams, PublicationInspect)
	rpc.Register("iprange.v1.publication.residue.remove", ValidatePublicationResidueRemoveParams, PublicationResidueRemove)
	rpc.Register("iprange.v1.publication.resolve", ValidatePublicationResolveParams, PublicationResolve)
}
