// Wire evidence decoding and JSON encoding for the live lifecycle
// families (Rust handlers/lifecycle_live.rs and live.rs result
// conversions parity). database.create.resolve,
// database.live_transition.resolve and commit.resolve accept the
// complete factual result object the caller preserved from the
// operation that made the attempt; these decoders rebuild the public
// SDK result values from that strict wire shape. Every field the SDK
// consumes during resolution or identity validation is decoded
// exactly; missing, extra, or mistyped members are invalid params
// before any SDK work starts (iprange-jsonrpc-v1.md resolution
// attempts).
//
// Two wire members are intentionally approximate, exactly like the
// Rust decoders: the Housekeeping container state is inferred from
// the artifact list when absent (empty list -> none, non-empty ->
// visible), and the commit cleanup ledger is validated for shape but
// the nested artifact values are not consumed by the commit resolver,
// so the rebuilt CommitResult carries the empty ledger.

package handlers

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ---------------------------------------------------------------------------
// Primitive wire decoders (result-schema encodings).
// ---------------------------------------------------------------------------

// exactMembers enforces the exact member set of one wire object.
func exactMembers(object rawObject, required, optional []string, field string) error {
	for key := range object {
		if !containsString(required, key) && !containsString(optional, key) {
			return fmt.Errorf("%s has unknown member %q", field, key)
		}
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("%s is missing member %q", field, key)
		}
	}
	return nil
}

// wireString reads one strict string member; ok is false when the
// member is absent or not a string.
func wireString(object rawObject, field string) (string, bool) {
	raw, ok := object[field]
	if !ok || isRawNull(raw) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// wireBool reads one strict boolean member.
func wireBool(object rawObject, field string) (bool, error) {
	raw, ok := object[field]
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", field)
	}
	if isRawNull(raw) {
		return false, fmt.Errorf("%s must be a boolean", field)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s must be a boolean", field)
	}
	return value, nil
}

// hexDigit decodes one lowercase hex character.
func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// decodeHex decodes exactly length bytes of lowercase hex text.
func decodeHex(text string, length int) ([]byte, error) {
	if len(text) != length*2 {
		return nil, fmt.Errorf("must be %d lowercase hexadecimal characters", length*2)
	}
	out := make([]byte, 0, length)
	for i := 0; i < len(text); i += 2 {
		hi, ok1 := hexDigit(text[i])
		lo, ok2 := hexDigit(text[i+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("must be %d lowercase hexadecimal characters", length*2)
		}
		out = append(out, hi<<4|lo)
	}
	return out, nil
}

// hex16FromWire decodes one 32-character lowercase hex member.
func hex16FromWire(object rawObject, field string) ([16]byte, error) {
	var out [16]byte
	text, ok := wireString(object, field)
	if !ok {
		return out, fmt.Errorf("%s must be a string", field)
	}
	bytes, err := decodeHex(text, 16)
	if err != nil {
		return out, fmt.Errorf("%s must be 32 lowercase hexadecimal characters", field)
	}
	copy(out[:], bytes)
	return out, nil
}

// hex32FromWire decodes one 64-character lowercase hex member.
func hex32FromWire(object rawObject, field string) ([32]byte, error) {
	var out [32]byte
	text, ok := wireString(object, field)
	if !ok {
		return out, fmt.Errorf("%s must be a string", field)
	}
	bytes, err := decodeHex(text, 32)
	if err != nil {
		return out, fmt.Errorf("%s must be 64 lowercase hexadecimal characters", field)
	}
	copy(out[:], bytes)
	return out, nil
}

// decimalU64FromWire decodes one canonical unsigned decimal string
// member ("0" or digits without a leading zero).
func decimalU64FromWire(object rawObject, field string) (uint64, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	value, err := canonicalU64String(text)
	if err != nil {
		return 0, fmt.Errorf("%s must be a canonical unsigned decimal string", field)
	}
	return value, nil
}

// u32IntegerFromWire decodes one u32 JSON integer member.
func u32IntegerFromWire(object rawObject, field string) (uint32, error) {
	value, err := asUint64(object, field)
	if err != nil || value > 0xffffffff {
		return 0, fmt.Errorf("%s must be a u32 integer", field)
	}
	return uint32(value), nil
}

// u16IntegerFromWire decodes one u16 JSON integer member.
func u16IntegerFromWire(object rawObject, field string) (uint16, error) {
	value, err := asUint64(object, field)
	if err != nil || value > 0xffff {
		return 0, fmt.Errorf("%s must be a u16 integer", field)
	}
	return uint16(value), nil
}

// decodeFileIdentity decodes one wire volume/file identity pair into
// the kind-1 SDK identity (Rust decode_file_identity).
// artifactBasename renders one artifact basename to its documented
// wire form, honoring the platform encoding tag (Rust
// lifecycle::basename): encoding 2 (Windows UTF-16LE units) maps
// every stored byte to the same-numbered U+00xx character (the
// opaque per-byte form), and encoding 1 keeps the bytes as the
// text's UTF-8 encoding.  Encoding-1 bytes that are not valid UTF-8
// decode with the same run replacement as Rust from_utf8_lossy (one
// U+FFFD per maximal invalid run), so both products emit the same
// wire text for every stored byte sequence.  ASCII names render
// unchanged under both encodings.
func artifactBasename(bytes []byte, encoding uint16) string {
	if encoding == 2 {
		runes := make([]rune, len(bytes))
		for i, b := range bytes {
			runes[i] = rune(b)
		}
		return string(runes)
	}
	return strings.ToValidUTF8(string(bytes), "\ufffd")
}

// decodeArtifactBasename decodes one artifact basename wire string
// back to its stored bytes. Artifact basenames travel in the
// documented opaque per-byte wire form (iprange-jsonrpc-v1.md):
// encoding 2 (Windows UTF-16LE units) maps every stored byte to the
// same-numbered U+00xx character, and encoding 1 keeps the bytes as
// the text's UTF-8 encoding.
func decodeArtifactBasename(text string, encoding uint16, field string) ([]byte, error) {
	if encoding == 1 {
		return []byte(text), nil
	}
	bytes := make([]byte, 0, len(text))
	for _, ch := range text {
		if ch > 0xff {
			return nil, fmt.Errorf("%s has a character outside the per-byte wire form", field)
		}
		bytes = append(bytes, byte(ch))
	}
	return bytes, nil
}

func decodeFileIdentity(object rawObject, field string) (iprangedb.FileIdentity, error) {
	var identity iprangedb.FileIdentity
	if err := exactMembers(object, []string{"volume", "file"}, nil, field); err != nil {
		return identity, err
	}
	volume, err := decimalU64FromWire(object, "volume")
	if err != nil {
		return identity, fmt.Errorf("%s.%v", field, err)
	}
	file, err := decimalU64FromWire(object, "file")
	if err != nil {
		return identity, fmt.Errorf("%s.%v", field, err)
	}
	binary.LittleEndian.PutUint64(identity.Bytes[0:8], volume)
	binary.LittleEndian.PutUint64(identity.Bytes[8:16], file)
	identity.Kind = 1
	if runtime.GOOS == "windows" {
		identity.Kind = 2
	}
	return identity, nil
}

// memberIdentity decodes one identity-valued member of a wire object.
func memberIdentity(object rawObject, field string) (iprangedb.FileIdentity, error) {
	member, err := memberObject(object, field)
	if err != nil {
		return iprangedb.FileIdentity{}, fmt.Errorf("%s must be an object", field)
	}
	return decodeFileIdentity(member, field)
}

// optionalFileIdentityFromWire decodes one optional identity member:
// absent is the only absent form, null is rejected.
func optionalFileIdentityFromWire(object rawObject, field string) (*iprangedb.FileIdentity, error) {
	raw, ok := object[field]
	if !ok {
		return nil, nil
	}
	if isRawNull(raw) {
		return nil, fmt.Errorf("%s must not be null; absent is the only absent form", field)
	}
	member, err := decodeObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an object", field)
	}
	identity, err := decodeFileIdentity(member, field)
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

// localBasenameLayout mirrors the private memory layout of
// iprangedb.LocalBasename: two u16 header words followed by the fixed
// 512-byte payload. The compile-time assertions below pin the layout
// so the materialization cannot silently diverge.
type localBasenameLayout struct {
	encoding uint16
	length   uint16
	bytes    [512]byte
}

const (
	_ = unsafe.Sizeof(iprangedb.LocalBasename{}) == unsafe.Sizeof(localBasenameLayout{})
	_ = unsafe.Offsetof(localBasenameLayout{}.bytes) == 4
)

// pathBasename builds the SDK basename value for one database path
// (Rust LocalBasename::from_path; POSIX bytes carry encoding 1). The
// public SDK exposes no LocalBasename constructor, so the value is
// materialized through the fixed-layout copy above. TODO: replace with
// an exported SDK constructor when one exists; the removal criterion is
// an exported BasenameFromPath constructor in lifecycle_public.go.
func pathBasename(path string) (iprangedb.LocalBasename, error) {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return iprangedb.LocalBasename{}, fmt.Errorf("database path has no file name")
	}
	if len(name) > 512 {
		return iprangedb.LocalBasename{}, fmt.Errorf("database basename exceeds the portable result bound")
	}
	var out iprangedb.LocalBasename
	layout := (*localBasenameLayout)(unsafe.Pointer(&out))
	layout.encoding = 1
	layout.length = uint16(len(name))
	copy(layout.bytes[:], name)
	return out, nil
}

// decodeMainBasename verifies the wire main_basename against the
// destination path and returns the path-derived SDK basename the
// resolvers compare against (Rust decode_main_basename).
func decodeMainBasename(object rawObject, path string) (iprangedb.LocalBasename, error) {
	wire, ok := wireString(object, "main_basename")
	if !ok {
		return iprangedb.LocalBasename{}, fmt.Errorf("main_basename must be a string")
	}
	actual := filepath.Base(path)
	if actual == "." || actual == string(filepath.Separator) {
		return iprangedb.LocalBasename{}, fmt.Errorf("database path has no file name")
	}
	if wire != actual {
		return iprangedb.LocalBasename{}, fmt.Errorf("main_basename does not match the database path")
	}
	return pathBasename(path)
}

// MainBasenameFromWire is the exported form of decodeMainBasename.
func MainBasenameFromWire(object rawObject, path string) (iprangedb.LocalBasename, *rpc.HandlerError) {
	basename, err := decodeMainBasename(object, path)
	if err != nil {
		return iprangedb.LocalBasename{}, rpc.InvalidParamsError(err.Error())
	}
	return basename, nil
}

// valueTagFromWire decodes one wire {"hex": ...} value tag member.
// valueTagFromWire is the single authoritative value-tag decoder:
// exactly one of {text} (0 through 15 bytes without NUL) or {hex}
// (even lowercase hex encoding at most 15 bytes without a NUL byte;
// the empty string is the zero-byte tag). Mirrors Rust
// validate_value_tag + value_tag composition.
func valueTagFromWire(object rawObject, field string) (iprangedb.ValueTag, error) {
	var tag iprangedb.ValueTag
	if len(object) == 1 {
		if textRaw, ok := object["text"]; ok {
			if isRawNull(textRaw) {
				return tag, fmt.Errorf("%s.text must be a string; null is not valid", field)
			}
			var text string
			if err := json.Unmarshal(textRaw, &text); err != nil || len(text) > 15 || strings.IndexByte(text, 0) >= 0 {
				return tag, fmt.Errorf("%s.text must encode 0 through 15 bytes without NUL", field)
			}
			created, err := iprangedb.NewValueTag([]byte(text))
			if err != nil {
				return tag, fmt.Errorf("%s encodes an invalid value tag", field)
			}
			return created, nil
		}
		if hexRaw, ok := object["hex"]; ok {
			if isRawNull(hexRaw) {
				return tag, fmt.Errorf("%s.hex must be a string; null is not valid", field)
			}
			var text string
			if err := json.Unmarshal(hexRaw, &text); err != nil {
				return tag, fmt.Errorf("%s.hex must be a string", field)
			}
			if len(text) > 30 || len(text)%2 != 0 {
				return tag, fmt.Errorf("%s.hex must be even lowercase hex encoding at most 15 bytes", field)
			}
			bytes := make([]byte, 0, len(text)/2)
			for i := 0; i < len(text); i += 2 {
				hi, ok1 := hexDigit(text[i])
				lo, ok2 := hexDigit(text[i+1])
				if !ok1 || !ok2 {
					return tag, fmt.Errorf("%s.hex must be even lowercase hex encoding at most 15 bytes", field)
				}
				bytes = append(bytes, hi<<4|lo)
			}
			if len(bytes) > 15 {
				return tag, fmt.Errorf("%s.hex must be even lowercase hex encoding at most 15 bytes", field)
			}
			created, err := iprangedb.NewValueTag(bytes)
			if err != nil {
				return tag, fmt.Errorf("%s encodes an invalid value tag", field)
			}
			return created, nil
		}
	}
	return tag, fmt.Errorf("%s must contain exactly one of text or hex", field)
}

// decodeValueTagMember decodes one value-tag-valued member.
func decodeValueTagMember(object rawObject, field string) (iprangedb.ValueTag, error) {
	member, err := memberObject(object, field)
	if err != nil {
		return iprangedb.ValueTag{}, fmt.Errorf("%s must be an object", field)
	}
	return valueTagFromWire(member, field)
}

func addressFamilyFromWire(object rawObject, field string) (iprangedb.AddressFamily, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "ipv4":
		return iprangedb.AddressFamilyIPv4, nil
	case "ipv6":
		return iprangedb.AddressFamilyIPv6, nil
	}
	return 0, fmt.Errorf("%s must be ipv4 or ipv6", field)
}

func valueKindFromWire(object rawObject, field string) (iprangedb.ValueKind, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "direct":
		return iprangedb.ValueKindDirect, nil
	case "membership":
		return iprangedb.ValueKindMembership, nil
	case "structured":
		return iprangedb.ValueKindStructured, nil
	}
	return 0, fmt.Errorf("%s must be direct, membership, or structured", field)
}

func structureKindFromWire(object rawObject, field string) (iprangedb.StructureKind, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "none":
		return iprangedb.StructureKindNone, nil
	case "network_enrichment_v1":
		return iprangedb.StructureKindNetworkEnrichmentV1, nil
	}
	return 0, fmt.Errorf("%s must be none or network_enrichment_v1", field)
}

func creationStateFromWire(object rawObject, field string) (iprangedb.CreationState, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "not_created":
		return iprangedb.CreationStateNotCreated, nil
	case "created":
		return iprangedb.CreationStateCreated, nil
	case "outcome_unknown":
		return iprangedb.CreationStateOutcomeUnknown, nil
	}
	return 0, fmt.Errorf("%s must be not_created, created, or outcome_unknown", field)
}

func liveTransitionOperationFromWire(object rawObject, field string) (iprangedb.LiveTransitionOperation, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "initialize":
		return iprangedb.LiveTransitionInitialize, nil
	case "reset":
		return iprangedb.LiveTransitionReset, nil
	}
	return 0, fmt.Errorf("%s must be initialize or reset", field)
}

func liveTransitionStatusFromWire(object rawObject, field string) (iprangedb.LiveTransitionStatus, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "unchanged":
		return iprangedb.LiveTransitionStatusUnchanged, nil
	case "initialized":
		return iprangedb.LiveTransitionStatusInitialized, nil
	case "outcome_unknown":
		return iprangedb.LiveTransitionStatusOutcomeUnknown, nil
	}
	return 0, fmt.Errorf("%s must be unchanged, initialized, or outcome_unknown", field)
}

// resetPolicyFromWire decodes the optional reset_policy member; the
// caller rejects null before calling.
func resetPolicyFromWire(object rawObject) (iprangedb.LiveResetPolicy, error) {
	text, ok := wireString(object, "reset_policy")
	if !ok {
		return 0, fmt.Errorf("reset_policy must be a string")
	}
	switch text {
	case "rollback_safe":
		return iprangedb.LiveResetRollbackSafe, nil
	case "discard_previous":
		return iprangedb.LiveResetDiscardPrevious, nil
	}
	return 0, fmt.Errorf("reset_policy must be rollback_safe or discard_previous")
}

// liveCoordinationLocationFromWire decodes the new_sidecar_location
// member of one transition result.
func liveCoordinationLocationFromWire(object rawObject) (iprangedb.LiveCoordinationLocation, error) {
	text, ok := wireString(object, "new_sidecar_location")
	if !ok {
		return 0, fmt.Errorf("new_sidecar_location must be a string")
	}
	switch text {
	case "absent":
		return iprangedb.LiveCoordinationLocationAbsent, nil
	case "canonical":
		return iprangedb.LiveCoordinationLocationCanonical, nil
	case "private":
		return iprangedb.LiveCoordinationLocationPrivate, nil
	case "unclassified":
		return iprangedb.LiveCoordinationLocationUnclassified, nil
	}
	return 0, fmt.Errorf("new_sidecar_location must be absent, canonical, private, or unclassified")
}

func commitDurabilityFromWire(object rawObject, field string) (iprangedb.CommitStatus, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "not_committed":
		return iprangedb.CommitNotCommitted, nil
	case "committed":
		return iprangedb.CommitCommitted, nil
	case "outcome_unknown":
		return iprangedb.CommitOutcomeUnknown, nil
	}
	return 0, fmt.Errorf("%s must be not_committed, committed, or outcome_unknown", field)
}

// coordinationCleanupFromWire decodes the coordination residue class
// ({} or {"kind": ...}).
func coordinationCleanupFromWire(object rawObject, field string) (iprangedb.CoordinationCleanup, error) {
	if len(object) == 0 {
		return iprangedb.CoordinationCleanupNone, nil
	}
	if err := exactMembers(object, []string{"kind"}, nil, field); err != nil {
		return 0, err
	}
	kind, ok := wireString(object, "kind")
	if !ok {
		return 0, fmt.Errorf("%s must be {} or {kind: ...}", field)
	}
	switch kind {
	case "cleanup_guard":
		return iprangedb.CoordinationCleanupCleanupGuard, nil
	case "retained_reader_close_required":
		return iprangedb.CoordinationCleanupRetainedReaderCloseRequired, nil
	case "retained_writer_close_required":
		return iprangedb.CoordinationCleanupRetainedWriterCloseRequired, nil
	}
	return 0, fmt.Errorf("%s.kind is invalid", field)
}

// decodeCreationSecurity decodes the creator-only security evidence.
func decodeCreationSecurity(object rawObject) (iprangedb.CreationSecurity, error) {
	var security iprangedb.CreationSecurity
	if err := exactMembers(object, []string{"kind", "commitment"}, nil, "creation_security"); err != nil {
		return security, err
	}
	kind, err := u16IntegerFromWire(object, "kind")
	if err != nil {
		return security, fmt.Errorf("creation_security.%v", err)
	}
	commitment, err := hex32FromWire(object, "commitment")
	if err != nil {
		return security, err
	}
	security.Kind = kind
	security.Commitment = commitment
	return security, nil
}

// decodeHousekeeping decodes the preserved Housekeeping class (Rust
// decode_housekeeping): an absent container state with no artifacts is
// None; the emitted states round-trip exactly.
func decodeHousekeeping(object rawObject, field string) (iprangedb.Housekeeping, error) {
	if err := exactMembers(object, []string{"artifacts"}, []string{"state"}, field); err != nil {
		return iprangedb.HousekeepingNone, err
	}
	raw := object["artifacts"]
	if isRawNull(raw) {
		return iprangedb.HousekeepingNone, fmt.Errorf("%s.artifacts must be an array; null is not valid", field)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return iprangedb.HousekeepingNone, fmt.Errorf("%s.artifacts must be an array", field)
	}
	for _, entry := range entries {
		if _, err := decodeObject(entry); err != nil {
			return iprangedb.HousekeepingNone, fmt.Errorf("%s.artifacts entries must be objects", field)
		}
	}
	stateRaw, hasState := object["state"]
	if !hasState {
		if len(entries) == 0 {
			return iprangedb.HousekeepingNone, nil
		}
		return iprangedb.HousekeepingVisible, nil
	}
	if isRawNull(stateRaw) {
		return iprangedb.HousekeepingNone, fmt.Errorf("%s.state is invalid; null is not valid", field)
	}
	var state string
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		return iprangedb.HousekeepingNone, fmt.Errorf("%s.state is invalid", field)
	}
	switch state {
	case "crash_reappearance_possible":
		return iprangedb.HousekeepingCrashReappearancePossible, nil
	case "visible":
		return iprangedb.HousekeepingVisible, nil
	}
	return iprangedb.HousekeepingNone, fmt.Errorf("%s.state is invalid", field)
}

// decodeHousekeepingArtifacts decodes the visible_housekeeping ledger.
func decodeHousekeepingArtifacts(raw json.RawMessage) ([]iprangedb.HousekeepingArtifact, error) {
	if isRawNull(raw) {
		return nil, fmt.Errorf("visible_housekeeping must be an array; null is not valid")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("visible_housekeeping must be an array")
	}
	out := make([]iprangedb.HousekeepingArtifact, 0, len(entries))
	for _, entry := range entries {
		object, err := decodeObject(entry)
		if err != nil {
			return nil, fmt.Errorf("visible_housekeeping entry must be an object")
		}
		artifact, err := decodeHousekeepingArtifact(object)
		if err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}
	return out, nil
}

func housekeepingStateFromWire(object rawObject, field string) (iprangedb.HousekeepingState, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "move_pending":
		return iprangedb.HousekeepingMovePending, nil
	case "move_ambiguous":
		return iprangedb.HousekeepingMoveAmbiguous, nil
	case "inert":
		return iprangedb.HousekeepingInert, nil
	case "conflict":
		return iprangedb.HousekeepingConflict, nil
	}
	return 0, fmt.Errorf("housekeeping state is invalid")
}

func directoryRoleFromWire(object rawObject, field string) (iprangedb.DirectoryRole, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "destination":
		return iprangedb.DirectoryRoleDestination, nil
	case "scratch_directory":
		return iprangedb.DirectoryRoleScratchDirectory, nil
	case "main_file":
		return iprangedb.DirectoryRoleMainFile, nil
	}
	return 0, fmt.Errorf("directory_role is invalid")
}

func artifactPresenceFromWire(object rawObject, field string) (iprangedb.ArtifactPresence, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "absent":
		return iprangedb.ArtifactAbsent, nil
	case "present":
		return iprangedb.ArtifactPresent, nil
	case "unclassified":
		return iprangedb.ArtifactUnclassified, nil
	}
	return 0, fmt.Errorf("artifact presence is invalid")
}

func artifactKindFromWire(object rawObject, field string) (iprangedb.ArtifactKind, error) {
	text, ok := wireString(object, field)
	if !ok {
		return 0, fmt.Errorf("%s must be a string", field)
	}
	switch text {
	case "private_output":
		return iprangedb.ArtifactPrivateOutput, nil
	case "private_reservation":
		return iprangedb.ArtifactPrivateReservation, nil
	case "owned_coordination":
		return iprangedb.ArtifactOwnedCoordination, nil
	case "authorized_scratch":
		return iprangedb.ArtifactAuthorizedScratch, nil
	case "owned_main":
		return iprangedb.ArtifactOwnedMain, nil
	case "unpublished_main_tail":
		return iprangedb.ArtifactUnpublishedMainTail, nil
	}
	return 0, fmt.Errorf("artifact kind is invalid")
}

// decodeHousekeepingArtifact decodes one visible housekeeping artifact
// (Rust decode_housekeeping_artifact). The POSIX lifecycle never
// produces entries, but the strict decode keeps preserved evidence
// portable across platforms.
func decodeHousekeepingArtifact(object rawObject) (iprangedb.HousekeepingArtifact, error) {
	var artifact iprangedb.HousekeepingArtifact
	required := []string{
		"state", "directory_role", "directory_identity", "basename_encoding",
		"attempt_id", "ordinal", "envelope_basename", "envelope_identity",
		"source_basename", "inert_basename", "source_presence", "inert_presence",
		"kind", "creation_security", "selected_envelope_sequence",
	}
	optional := []string{"source_identity", "inert_identity"}
	if err := exactMembers(object, required, optional, "housekeeping artifact"); err != nil {
		return artifact, err
	}
	state, err := housekeepingStateFromWire(object, "state")
	if err != nil {
		return artifact, err
	}
	role, err := directoryRoleFromWire(object, "directory_role")
	if err != nil {
		return artifact, err
	}
	directoryIdentity, err := memberIdentity(object, "directory_identity")
	if err != nil {
		return artifact, err
	}
	basenameEncoding, err := u16IntegerFromWire(object, "basename_encoding")
	if err != nil {
		return artifact, err
	}
	attemptID, err := hex16FromWire(object, "attempt_id")
	if err != nil {
		return artifact, err
	}
	ordinal, err := u32IntegerFromWire(object, "ordinal")
	if err != nil {
		return artifact, err
	}
	envelopeBasename, ok := wireString(object, "envelope_basename")
	if !ok {
		return artifact, fmt.Errorf("envelope_basename must be a string")
	}
	envelopeIdentity, err := memberIdentity(object, "envelope_identity")
	if err != nil {
		return artifact, err
	}
	sourceBasename, ok := wireString(object, "source_basename")
	if !ok {
		return artifact, fmt.Errorf("source_basename must be a string")
	}
	inertBasename, ok := wireString(object, "inert_basename")
	if !ok {
		return artifact, fmt.Errorf("inert_basename must be a string")
	}
	sourcePresence, err := artifactPresenceFromWire(object, "source_presence")
	if err != nil {
		return artifact, err
	}
	sourceIdentity, err := optionalFileIdentityFromWire(object, "source_identity")
	if err != nil {
		return artifact, err
	}
	inertPresence, err := artifactPresenceFromWire(object, "inert_presence")
	if err != nil {
		return artifact, err
	}
	inertIdentity, err := optionalFileIdentityFromWire(object, "inert_identity")
	if err != nil {
		return artifact, err
	}
	kind, err := artifactKindFromWire(object, "kind")
	if err != nil {
		return artifact, err
	}
	securityObject, merr := memberObject(object, "creation_security")
	if merr != nil {
		return artifact, merr
	}
	creationSecurity, err := decodeCreationSecurity(securityObject)
	if err != nil {
		return artifact, err
	}
	sequence, err := decimalU64FromWire(object, "selected_envelope_sequence")
	if err != nil {
		return artifact, err
	}
	artifact.State = state
	artifact.DirectoryRole = role
	artifact.DirectoryIdentity = directoryIdentity
	artifact.BasenameEncoding = basenameEncoding
	artifact.AttemptID = attemptID
	artifact.Ordinal = ordinal
	envelopeBasenameBytes, err := decodeArtifactBasename(envelopeBasename, basenameEncoding, "envelope_basename")
	if err != nil {
		return artifact, err
	}
	artifact.EnvelopeBasename = envelopeBasenameBytes
	artifact.EnvelopeIdentity = envelopeIdentity
	sourceBasenameBytes, err := decodeArtifactBasename(sourceBasename, basenameEncoding, "source_basename")
	if err != nil {
		return artifact, err
	}
	artifact.SourceBasename = sourceBasenameBytes
	inertBasenameBytes, err := decodeArtifactBasename(inertBasename, basenameEncoding, "inert_basename")
	if err != nil {
		return artifact, err
	}
	artifact.InertBasename = inertBasenameBytes
	artifact.SourcePresence = sourcePresence
	artifact.SourceIdentity = sourceIdentity
	artifact.InertPresence = inertPresence
	artifact.InertIdentity = inertIdentity
	artifact.Kind = kind
	artifact.CreationSecurity = creationSecurity
	artifact.SelectedEnvelopeSequence = sequence
	return artifact, nil
}

// validateCommitCleanup checks the preserved cleanup ledger shape. The
// nested artifact values are not consumed by the commit resolver (the
// SDK resolves only the attempted transaction facts), so the ledger is
// validated but not reconstructed; the rebuilt CommitResult carries
// the empty ledger, exactly like the Rust decoder's documented
// approximation.
func validateCommitCleanup(object rawObject) error {
	if len(object) == 0 {
		return nil
	}
	if err := exactMembers(object, []string{"artifacts"}, nil, "cleanup"); err != nil {
		return err
	}
	if isRawNull(object["artifacts"]) {
		return fmt.Errorf("cleanup.artifacts must be an array; null is not valid")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(object["artifacts"], &entries); err != nil {
		return fmt.Errorf("cleanup.artifacts must be an array")
	}
	if len(entries) > 1 {
		return fmt.Errorf("cleanup.artifacts must contain at most one entry")
	}
	for _, entry := range entries {
		artifact, err := decodeObject(entry)
		if err != nil {
			return fmt.Errorf("cleanup artifact must be an object")
		}
		if err := validateCommitCleanupArtifact(artifact); err != nil {
			return err
		}
	}
	return nil
}

// validateCommitCleanupArtifact validates one cleanup-artifact shape:
// the exact member set and the wire types of every member (results.py
// COMMIT_CLEANUP_ARTIFACT). cleanup_error is a plain wire string, so
// only the string type is enforced here.
func validateCommitCleanupArtifact(object rawObject) error {
	required := []string{
		"directory_identity", "main_basename", "main_identity",
		"expected_database_id", "target_transaction_id", "target_commit_nonce",
		"committed_target_length", "cleanup_error",
	}
	optional := []string{"observed_tail_end_exclusive"}
	if err := exactMembers(object, required, optional, "cleanup artifact"); err != nil {
		return err
	}
	if _, err := memberIdentity(object, "directory_identity"); err != nil {
		return err
	}
	if _, ok := wireString(object, "main_basename"); !ok {
		return fmt.Errorf("main_basename must be a string")
	}
	if _, err := memberIdentity(object, "main_identity"); err != nil {
		return err
	}
	if _, err := hex16FromWire(object, "expected_database_id"); err != nil {
		return err
	}
	if _, err := decimalU64FromWire(object, "target_transaction_id"); err != nil {
		return err
	}
	if _, err := hex16FromWire(object, "target_commit_nonce"); err != nil {
		return err
	}
	if _, err := decimalU64FromWire(object, "committed_target_length"); err != nil {
		return err
	}
	if raw, ok := object["observed_tail_end_exclusive"]; ok {
		if isRawNull(raw) {
			return fmt.Errorf("observed_tail_end_exclusive must not be null; absent is the only absent form")
		}
		if _, err := decimalU64FromWire(object, "observed_tail_end_exclusive"); err != nil {
			return err
		}
	}
	if _, ok := wireString(object, "cleanup_error"); !ok {
		return fmt.Errorf("cleanup_error must be a string")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Complete evidence decoders for the three resolution methods.
// ---------------------------------------------------------------------------

// decodeCommitResult rebuilds a CommitResult from the complete object
// returned by a commit (Rust commit_result_from_wire).
func decodeCommitResult(object rawObject) (*iprangedb.CommitResult, error) {
	result := &iprangedb.CommitResult{}
	if err := exactMembers(object, []string{
		"attempted_database_id", "directory_identity", "main_identity",
		"attempted_transaction_id", "attempted_commit_nonce", "durability",
		"cleanup", "coordination_cleanup",
	}, nil, "commit_result"); err != nil {
		return nil, err
	}
	databaseID, err := hex16FromWire(object, "attempted_database_id")
	if err != nil {
		return nil, err
	}
	directoryIdentity, err := memberIdentity(object, "directory_identity")
	if err != nil {
		return nil, err
	}
	mainIdentity, err := memberIdentity(object, "main_identity")
	if err != nil {
		return nil, err
	}
	transactionID, err := decimalU64FromWire(object, "attempted_transaction_id")
	if err != nil {
		return nil, err
	}
	commitNonce, err := hex16FromWire(object, "attempted_commit_nonce")
	if err != nil {
		return nil, err
	}
	status, err := commitDurabilityFromWire(object, "durability")
	if err != nil {
		return nil, err
	}
	cleanup, err := memberObject(object, "cleanup")
	if err != nil {
		return nil, fmt.Errorf("cleanup must be an object")
	}
	if err := validateCommitCleanup(cleanup); err != nil {
		return nil, err
	}
	coordination, err := memberObject(object, "coordination_cleanup")
	if err != nil {
		return nil, fmt.Errorf("coordination_cleanup must be an object")
	}
	coordinationCleanup, err := coordinationCleanupFromWire(coordination, "coordination_cleanup")
	if err != nil {
		return nil, err
	}
	result.AttemptedDatabaseID = databaseID
	result.DirectoryIdentity = &directoryIdentity
	result.MainIdentity = &mainIdentity
	result.AttemptedTransactionID = transactionID
	result.AttemptedCommitNonce = commitNonce
	result.Status = status
	result.Cleanup = iprangedb.LiveCommitCleanupArtifacts{}
	result.CoordinationCleanup = coordinationCleanup
	result.Cause = nil
	return result, nil
}

// CommitResultFromWire rebuilds a CommitResult from the complete wire
// object returned by a commit (Rust commit_result_from_wire). The
// cleanup ledger is validated for shape but not reconstructed.
func CommitResultFromWire(object rawObject) (*iprangedb.CommitResult, *rpc.HandlerError) {
	result, err := decodeCommitResult(object)
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	return result, nil
}

// decodeCreateResult rebuilds a CreateResult from the complete object
// returned by database.create (Rust create_result_from_wire).
func decodeCreateResult(object rawObject, path string) (*iprangedb.CreateResult, error) {
	result := &iprangedb.CreateResult{}
	required := []string{
		"address_family", "value_kind", "structure_kind", "value_tag",
		"database_id", "commit_nonce", "sidecar_id", "main_basename",
		"reader_capacity", "state", "residue_possible", "housekeeping",
		"visible_housekeeping",
	}
	optional := []string{"directory_identity", "main_identity", "sidecar_identity"}
	if err := exactMembers(object, required, optional, "create_result"); err != nil {
		return nil, err
	}
	family, err := addressFamilyFromWire(object, "address_family")
	if err != nil {
		return nil, err
	}
	kind, err := valueKindFromWire(object, "value_kind")
	if err != nil {
		return nil, err
	}
	structure, err := structureKindFromWire(object, "structure_kind")
	if err != nil {
		return nil, err
	}
	tag, err := decodeValueTagMember(object, "value_tag")
	if err != nil {
		return nil, err
	}
	databaseID, err := hex16FromWire(object, "database_id")
	if err != nil {
		return nil, err
	}
	commitNonce, err := hex16FromWire(object, "commit_nonce")
	if err != nil {
		return nil, err
	}
	sidecarID, err := hex16FromWire(object, "sidecar_id")
	if err != nil {
		return nil, err
	}
	directoryIdentity, err := optionalFileIdentityFromWire(object, "directory_identity")
	if err != nil {
		return nil, err
	}
	basename, err := decodeMainBasename(object, path)
	if err != nil {
		return nil, err
	}
	mainIdentity, err := optionalFileIdentityFromWire(object, "main_identity")
	if err != nil {
		return nil, err
	}
	sidecarIdentity, err := optionalFileIdentityFromWire(object, "sidecar_identity")
	if err != nil {
		return nil, err
	}
	readerCapacity, err := u32IntegerFromWire(object, "reader_capacity")
	if err != nil {
		return nil, err
	}
	state, err := creationStateFromWire(object, "state")
	if err != nil {
		return nil, err
	}
	residuePossible, err := wireBool(object, "residue_possible")
	if err != nil {
		return nil, err
	}
	housekeepingObject, merr := memberObject(object, "housekeeping")
	if merr != nil {
		return nil, merr
	}
	housekeeping, err := decodeHousekeeping(housekeepingObject, "housekeeping")
	if err != nil {
		return nil, err
	}
	visible, err := decodeHousekeepingArtifacts(object["visible_housekeeping"])
	if err != nil {
		return nil, err
	}
	result.Family = family
	result.ValueKind = kind
	result.StructureKind = structure
	result.ValueTag = tag
	result.DatabaseID = databaseID
	result.CommitNonce = commitNonce
	result.SidecarID = sidecarID
	result.DirectoryIdentity = directoryIdentity
	result.MainBasename = basename
	result.MainIdentity = mainIdentity
	result.SidecarIdentity = sidecarIdentity
	result.ReaderCapacity = readerCapacity
	result.State = state
	result.ResiduePossible = residuePossible
	result.Housekeeping = housekeeping
	result.VisibleHousekeeping = visible
	result.Cause = nil
	return result, nil
}

// CreateResultFromWire rebuilds a CreateResult from the complete wire
// object returned by database.create (Rust create_result_from_wire).
func CreateResultFromWire(object rawObject, path string) (*iprangedb.CreateResult, *rpc.HandlerError) {
	result, err := decodeCreateResult(object, path)
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	return result, nil
}

// decodeLiveTransitionResult rebuilds a LiveTransitionResult from the
// complete object returned by database.initialize_live or
// database.reset_live (Rust live_transition_result_from_wire).
func decodeLiveTransitionResult(object rawObject, path string) (*iprangedb.LiveTransitionResult, error) {
	result := &iprangedb.LiveTransitionResult{}
	required := []string{
		"operation", "status", "database_id", "transaction_id", "commit_nonce",
		"directory_identity", "main_identity", "main_basename", "reader_capacity",
		"sidecar_id", "new_sidecar_location", "residue_possible", "housekeeping",
		"visible_housekeeping",
	}
	optional := []string{"reset_policy", "previous_sidecar_identity", "new_sidecar_identity"}
	if err := exactMembers(object, required, optional, "live_transition_result"); err != nil {
		return nil, err
	}
	operation, err := liveTransitionOperationFromWire(object, "operation")
	if err != nil {
		return nil, err
	}
	var resetPolicy *iprangedb.LiveResetPolicy
	if raw, ok := object["reset_policy"]; ok {
		if isRawNull(raw) {
			return nil, fmt.Errorf("reset_policy must not be null; absent is the only absent form")
		}
		policy, err := resetPolicyFromWire(object)
		if err != nil {
			return nil, err
		}
		resetPolicy = &policy
	}
	status, err := liveTransitionStatusFromWire(object, "status")
	if err != nil {
		return nil, err
	}
	databaseID, err := hex16FromWire(object, "database_id")
	if err != nil {
		return nil, err
	}
	transactionID, err := decimalU64FromWire(object, "transaction_id")
	if err != nil {
		return nil, err
	}
	commitNonce, err := hex16FromWire(object, "commit_nonce")
	if err != nil {
		return nil, err
	}
	directoryIdentity, err := memberIdentity(object, "directory_identity")
	if err != nil {
		return nil, err
	}
	mainIdentity, err := memberIdentity(object, "main_identity")
	if err != nil {
		return nil, err
	}
	basename, err := decodeMainBasename(object, path)
	if err != nil {
		return nil, err
	}
	readerCapacity, err := u32IntegerFromWire(object, "reader_capacity")
	if err != nil {
		return nil, err
	}
	sidecarID, err := hex16FromWire(object, "sidecar_id")
	if err != nil {
		return nil, err
	}
	previousSidecarIdentity, err := optionalFileIdentityFromWire(object, "previous_sidecar_identity")
	if err != nil {
		return nil, err
	}
	newSidecarIdentity, err := optionalFileIdentityFromWire(object, "new_sidecar_identity")
	if err != nil {
		return nil, err
	}
	location, err := liveCoordinationLocationFromWire(object)
	if err != nil {
		return nil, err
	}
	residuePossible, err := wireBool(object, "residue_possible")
	if err != nil {
		return nil, err
	}
	housekeepingObject, merr := memberObject(object, "housekeeping")
	if merr != nil {
		return nil, merr
	}
	housekeeping, err := decodeHousekeeping(housekeepingObject, "housekeeping")
	if err != nil {
		return nil, err
	}
	visible, err := decodeHousekeepingArtifacts(object["visible_housekeeping"])
	if err != nil {
		return nil, err
	}
	result.Operation = operation
	result.ResetPolicy = resetPolicy
	result.Status = status
	result.DatabaseID = databaseID
	result.TransactionID = transactionID
	result.CommitNonce = commitNonce
	result.DirectoryIdentity = &directoryIdentity
	result.MainIdentity = &mainIdentity
	result.MainBasename = basename
	result.ReaderCapacity = readerCapacity
	result.SidecarID = sidecarID
	result.PreviousSidecarIdentity = previousSidecarIdentity
	result.NewSidecarIdentity = newSidecarIdentity
	result.NewSidecarLocation = location
	result.ResiduePossible = residuePossible
	result.Housekeeping = housekeeping
	result.VisibleHousekeeping = visible
	result.Cause = nil
	return result, nil
}

// LiveTransitionResultFromWire rebuilds a LiveTransitionResult from
// the complete wire object returned by database.initialize_live or
// database.reset_live (Rust live_transition_result_from_wire).
func LiveTransitionResultFromWire(object rawObject, path string) (*iprangedb.LiveTransitionResult, *rpc.HandlerError) {
	result, err := decodeLiveTransitionResult(object, path)
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Result encoders for the live families (Rust live.rs conversions).
// ---------------------------------------------------------------------------

// TransitionOperationName maps the SDK operation to its wire name.
func TransitionOperationName(operation iprangedb.LiveTransitionOperation) string {
	switch operation {
	case iprangedb.LiveTransitionInitialize:
		return "initialize"
	case iprangedb.LiveTransitionReset:
		return "reset"
	}
	return "initialize"
}

// TransitionStatusName maps the SDK transition status to its wire name.
func TransitionStatusName(status iprangedb.LiveTransitionStatus) string {
	switch status {
	case iprangedb.LiveTransitionStatusUnchanged:
		return "unchanged"
	case iprangedb.LiveTransitionStatusInitialized:
		return "initialized"
	case iprangedb.LiveTransitionStatusOutcomeUnknown:
		return "outcome_unknown"
	}
	return "outcome_unknown"
}

// ResetPolicyName maps the SDK reset policy to its wire name.
func ResetPolicyName(policy iprangedb.LiveResetPolicy) string {
	switch policy {
	case iprangedb.LiveResetRollbackSafe:
		return "rollback_safe"
	case iprangedb.LiveResetDiscardPrevious:
		return "discard_previous"
	}
	return "rollback_safe"
}

// CoordinationLocationName maps the SDK coordination location to its
// wire name.
func CoordinationLocationName(location iprangedb.LiveCoordinationLocation) string {
	switch location {
	case iprangedb.LiveCoordinationLocationAbsent:
		return "absent"
	case iprangedb.LiveCoordinationLocationCanonical:
		return "canonical"
	case iprangedb.LiveCoordinationLocationPrivate:
		return "private"
	case iprangedb.LiveCoordinationLocationUnclassified:
		return "unclassified"
	}
	return "unclassified"
}

// ResidueStatusName maps the SDK residue status to its wire name.
func ResidueStatusName(status iprangedb.LiveResidueStatus) string {
	switch status {
	case iprangedb.LiveResidueStatusAbsent:
		return "absent"
	case iprangedb.LiveResidueStatusReady:
		return "ready"
	case iprangedb.LiveResidueStatusCompleted:
		return "completed"
	case iprangedb.LiveResidueStatusRemoved:
		return "removed"
	case iprangedb.LiveResidueStatusOutcomeUnknown:
		return "outcome_unknown"
	}
	return "outcome_unknown"
}

// ResidueKindName maps the SDK residue kind to its wire name.
func ResidueKindName(kind iprangedb.LiveResidueKind) string {
	switch kind {
	case iprangedb.LiveResidueKindCanonical:
		return "canonical"
	case iprangedb.LiveResidueKindPrivateReset:
		return "private_reset"
	}
	return "canonical"
}

// LocalFileRelationName maps the SDK local-file relation to its wire
// name.
func LocalFileRelationName(relation iprangedb.LocalFileRelation) string {
	switch relation {
	case iprangedb.LocalFileRelationSameLocalFile:
		return "same_local_file"
	case iprangedb.LocalFileRelationDifferentLocalFile:
		return "different_local_file"
	}
	return "different_local_file"
}

// CommitResolutionName maps the SDK commit resolution to its wire name.
func CommitResolutionName(resolution iprangedb.CommitResolution) string {
	switch resolution {
	case iprangedb.CommitResolutionCommitted:
		return "committed"
	case iprangedb.CommitResolutionNotCommitted:
		return "not_committed"
	case iprangedb.CommitResolutionSupersededUnknown:
		return "superseded_unknown"
	case iprangedb.CommitResolutionUnresolvable:
		return "unresolvable"
	}
	return "unresolvable"
}

// LiveTransitionResultJSON converts one SDK transition result to its
// wire object (Rust live.rs live_transition_result).
func LiveTransitionResultJSON(result *iprangedb.LiveTransitionResult) map[string]any {
	value := map[string]any{
		"operation":            TransitionOperationName(result.Operation),
		"status":               TransitionStatusName(result.Status),
		"database_id":          HexID(&result.DatabaseID),
		"transaction_id":       DecimalUint(result.TransactionID),
		"commit_nonce":         HexID(&result.CommitNonce),
		"directory_identity":   FileIdentityJSONOrError(result.DirectoryIdentity),
		"main_identity":        FileIdentityJSONOrError(result.MainIdentity),
		"main_basename":        LocalBasenameBytes(result.MainBasename),
		"reader_capacity":      result.ReaderCapacity,
		"sidecar_id":           HexID(&result.SidecarID),
		"new_sidecar_location": CoordinationLocationName(result.NewSidecarLocation),
		"residue_possible":     result.ResiduePossible,
		"housekeeping":         HousekeepingJSON(result.Housekeeping, result.VisibleHousekeeping),
		"visible_housekeeping": VisibleHousekeepingJSON(result.VisibleHousekeeping),
	}
	// Optional SDK fields are absent, never null (wire rule).
	if result.ResetPolicy != nil {
		value["reset_policy"] = ResetPolicyName(*result.ResetPolicy)
	}
	if result.PreviousSidecarIdentity != nil {
		value["previous_sidecar_identity"] = FileIdentityJSONOrError(result.PreviousSidecarIdentity)
	}
	if result.NewSidecarIdentity != nil {
		value["new_sidecar_identity"] = FileIdentityJSONOrError(result.NewSidecarIdentity)
	}
	return value
}

// LiveResidueResultJSON converts one SDK residue result to its wire
// object (Rust live.rs live_residue_result).
func LiveResidueResultJSON(result *iprangedb.LiveResidueResult) map[string]any {
	value := map[string]any{
		"status":               ResidueStatusName(result.Status),
		"residue_possible":     result.ResiduePossible,
		"housekeeping":         HousekeepingJSON(result.Housekeeping, result.VisibleHousekeeping),
		"visible_housekeeping": VisibleHousekeepingJSON(result.VisibleHousekeeping),
	}
	// Optional SDK facts are absent, never null (wire rule).
	if result.Kind != nil {
		value["kind"] = ResidueKindName(*result.Kind)
	}
	if result.DatabaseID != nil {
		value["database_id"] = HexID(result.DatabaseID)
	}
	if result.SidecarID != nil {
		value["sidecar_id"] = HexID(result.SidecarID)
	}
	if result.ReaderCapacity != nil {
		value["reader_capacity"] = *result.ReaderCapacity
	}
	if result.MainIdentity != nil {
		value["main_identity"] = FileIdentityJSONOrError(result.MainIdentity)
	}
	if result.SidecarIdentity != nil {
		value["sidecar_identity"] = FileIdentityJSONOrError(result.SidecarIdentity)
	}
	return value
}

// CommitResolutionResultJSON converts one SDK commit-resolution result
// to its wire object (Rust live.rs commit_resolution_result).
func CommitResolutionResultJSON(result *iprangedb.CommitResolutionResult) map[string]any {
	return map[string]any{
		"attempted_database_id":     HexID(&result.AttemptedDatabaseID),
		"attempted_transaction_id":  DecimalUint(result.AttemptedTransactionID),
		"attempted_commit_nonce":    HexID(&result.AttemptedCommitNonce),
		"actual_directory_identity": FileIdentityJSONOrError(&result.ActualDirectoryIdentity),
		"actual_main_identity":      FileIdentityJSONOrError(&result.ActualMainIdentity),
		"local_file_relation":       LocalFileRelationName(result.LocalFileRelation),
		"resolution":                CommitResolutionName(result.Resolution),
		"cleanup":                   CommitCleanupJSON(result.Cleanup),
		"coordination_cleanup":      CoordinationCleanupJSON(result.CoordinationCleanup),
	}
}
