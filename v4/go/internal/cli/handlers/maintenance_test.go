package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// reservationEvidence builds one valid reservation remove evidence
// object; previous is included only when it is non-nil (Rust
// reservation_entry_value omits the member when there is no previous
// block, so the remove row must accept its absence).
func reservationEvidence(previous any) map[string]any {
	evidence := map[string]any{
		"policy": "replace_existing",
		"phase":  "prepared",
		"output": map[string]any{
			"identity": map[string]any{"volume": "1", "file": "2"},
			"tuple": map[string]any{
				"database_id":    "11111111111111111111111111111111",
				"transaction_id": "1",
				"commit_nonce":   "22222222222222222222222222222222",
			},
			"digest": map[string]any{
				"byte_length": "64",
				"sha512":      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	}
	if previous != nil {
		evidence["previous"] = previous
	}
	return evidence
}

// reservationPrevious builds one valid reservation previous block.
func reservationPrevious() map[string]any {
	return map[string]any{
		"identity": map[string]any{"volume": "3", "file": "4"},
		"digest": map[string]any{
			"byte_length": "32",
			"sha512":      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
}

// reservationRemoveEntry builds one full maintenance.remove
// reservation entry (the exact list row shape) around the given
// evidence value.
func reservationRemoveEntry(evidence any) map[string]any {
	return map[string]any{
		"kind":                   "reservation",
		"directory":              "/tmp/probe",
		"directory_identity":     map[string]any{"volume": "1", "file": "2"},
		"artifact_identity":      map[string]any{"volume": "9", "file": "8"},
		"publication_attempt_id": "11111111111111111111111111111111",
		"evidence":               evidence,
	}
}

// decodeEvidence decodes one evidence map into the raw object the
// wire validator validates.
func decodeEvidence(t *testing.T, evidence map[string]any) rawObject {
	t.Helper()
	decoded, err := decodeObject(mustJSON(t, evidence))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

// decodeReservationRemoveEntry decodes one full entry map into the
// raw object the wire handlers validate.
func decodeReservationRemoveEntry(t *testing.T, entry map[string]any) rawObject {
	t.Helper()
	obj, err := decodeObject(mustJSON(t, map[string]any{"entry": entry}))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := memberObject(obj, "entry")
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

// TestReservationRemoveEvidenceAbsentPreviousAccepted pins that a
// maintenance.remove reservation entry whose evidence omits the
// optional previous member (exactly as maintenance.list emits the
// row when there is no previous block) validates and is accepted by
// the removal wire handler.
func TestReservationRemoveEvidenceAbsentPreviousAccepted(t *testing.T) {
	evidence := reservationEvidence(nil)
	if herr := validateReservationEvidence(decodeEvidence(t, evidence)); herr != nil {
		t.Fatalf("evidence without previous must validate: %v", herr)
	}
	entry := decodeReservationRemoveEntry(t, reservationRemoveEntry(evidence))
	if _, _, _, _, herr := reservationRemoveFields(entry); herr != nil {
		t.Fatalf("reservation remove entry without previous must be accepted: %v", herr)
	}
}

// TestReservationRemoveEvidencePresentPreviousAccepted pins that the
// optional previous member still validates exactly as before when it
// is present as an object with identity and digest.
func TestReservationRemoveEvidencePresentPreviousAccepted(t *testing.T) {
	evidence := reservationEvidence(reservationPrevious())
	if herr := validateReservationEvidence(decodeEvidence(t, evidence)); herr != nil {
		t.Fatalf("evidence with previous object must validate: %v", herr)
	}
	entry := decodeReservationRemoveEntry(t, reservationRemoveEntry(evidence))
	if _, _, _, _, herr := reservationRemoveFields(entry); herr != nil {
		t.Fatalf("reservation remove entry with previous object must be accepted: %v", herr)
	}
}

// TestReservationRemoveEvidenceNullPreviousRefused pins that a
// present-but-null previous member is never a valid absent form:
// absent is the only absent form (Rust reservation_remove_fields).
func TestReservationRemoveEvidenceNullPreviousRefused(t *testing.T) {
	evidence := reservationEvidence(nil)
	evidence["previous"] = nil
	if herr := validateReservationEvidence(decodeEvidence(t, evidence)); herr == nil {
		t.Fatal("evidence with previous:null must be refused")
	}
	entry := decodeReservationRemoveEntry(t, reservationRemoveEntry(evidence))
	if _, _, _, _, herr := reservationRemoveFields(entry); herr == nil {
		t.Fatal("reservation remove entry with previous:null must be refused")
	}
}

// TestReservationRemoveEvidenceUnknownKeysRefused pins that the
// evidence exact-fields contract still rejects any member beyond
// policy, phase, output, and previous, and still requires the three
// mandatory members.
func TestReservationRemoveEvidenceUnknownKeysRefused(t *testing.T) {
	evidence := reservationEvidence(reservationPrevious())
	evidence["extra"] = "x"
	if herr := validateReservationEvidence(decodeEvidence(t, evidence)); herr == nil {
		t.Fatal("evidence with unknown member must be refused")
	}
	entry := decodeReservationRemoveEntry(t, reservationRemoveEntry(evidence))
	if _, _, _, _, herr := reservationRemoveFields(entry); herr == nil {
		t.Fatal("reservation remove entry with unknown evidence member must be refused")
	}

	delete(evidence, "output")
	if herr := validateReservationEvidence(decodeEvidence(t, evidence)); herr == nil {
		t.Fatal("evidence without required output must be refused")
	}
	entry = decodeReservationRemoveEntry(t, reservationRemoveEntry(evidence))
	if _, _, _, _, herr := reservationRemoveFields(entry); herr == nil {
		t.Fatal("reservation remove entry without evidence.output must be refused")
	}
}

// housekeepingRemoveEntry builds one maintenance.remove
// windows_housekeeping entry (the exact list row shape). artifact and
// problem are included only when they are non-nil, exactly like
// maintenance.list emits them.
func housekeepingRemoveEntry(artifact, problem any) map[string]any {
	entry := map[string]any{
		"kind":               "windows_housekeeping",
		"directory":          "/tmp/probe",
		"directory_identity": map[string]any{"volume": "1", "file": "2"},
		"candidate_kind":     "envelope",
		"basename_encoding":  2,
		"basename":           "LgBpAHAAcgBhAG4AZwBlAC0AZwBjAGEAdQB0AGgALQAxAC4AdABtAHAA",
		"identity":           map[string]any{"volume": "3", "file": "4"},
		"attempt_id":         "11111111111111111111111111111111",
		"ordinal":            1,
	}
	if artifact != nil {
		entry["artifact"] = artifact
	}
	if problem != nil {
		entry["problem"] = problem
	}
	return entry
}

// TestHousekeepingRemoveAbsentOptionalMembersAccepted pins that a
// maintenance.remove windows_housekeeping entry without the optional
// artifact and problem members (exactly as maintenance.list emits a
// clean row) is accepted by the removal wire handler.
func TestHousekeepingRemoveAbsentOptionalMembersAccepted(t *testing.T) {
	entry := decodeReservationRemoveEntry(t, housekeepingRemoveEntry(nil, nil))
	if _, _, _, _, _, herr := housekeepingRemoveFields(entry); herr != nil {
		t.Fatalf("clean housekeeping row without artifact/problem must be accepted: %v", herr)
	}
}

// TestHousekeepingRemovePresentOptionalMembersAccepted pins that the
// same row with both optional members present (a row whose list-time
// classification carried an artifact and a problem) is also accepted.
func TestHousekeepingRemovePresentOptionalMembersAccepted(t *testing.T) {
	artifact := map[string]any{"kind": "private_output"}
	problem := map[string]any{"code": "cleanup_conflict"}
	entry := decodeReservationRemoveEntry(t,
		housekeepingRemoveEntry(artifact, problem))
	if _, _, _, _, _, herr := housekeepingRemoveFields(entry); herr != nil {
		t.Fatalf("housekeeping row with artifact/problem must be accepted: %v", herr)
	}
}

// TestHousekeepingRemoveUnknownMemberRefused pins the strictness side:
// a synthesized entry with an extra member is refused before any
// destructive step.
func TestHousekeepingRemoveUnknownMemberRefused(t *testing.T) {
	entry := housekeepingRemoveEntry(nil, nil)
	entry["extra"] = "x"
	decoded := decodeReservationRemoveEntry(t, entry)
	if _, _, _, _, _, herr := housekeepingRemoveFields(decoded); herr == nil {
		t.Fatal("housekeeping remove entry with unknown member must be refused")
	}
}

// ---------------------------------------------------------------------------
// maintenance.list -> maintenance.remove row round-trip
// ---------------------------------------------------------------------------

// maintenanceFileIdentityOf builds one portable local file identity of the
// current platform from a device and inode pair, the form the wire encodes
// as {"volume","file"}.
func maintenanceFileIdentityOf(device, inode uint64) iprangedb.FileIdentity {
	identity := iprangedb.FileIdentity{Kind: 1}
	if runtime.GOOS == "windows" {
		identity.Kind = 2
	}
	binary.LittleEndian.PutUint64(identity.Bytes[0:8], device)
	binary.LittleEndian.PutUint64(identity.Bytes[8:16], inode)
	return identity
}

// maintenanceScratchArtifact writes one recovery-scratch artifact under its
// exact 62-byte basename and returns its path. The 128-byte ownership
// header follows the recorded scratch format of binary-format-v4.md:
// magic, fixed fields, meta facts, attempt, ordinal, the platform
// creator-only security kind, the captured creator commitment, and the
// CRC-32C of the header with its own checksum field zeroed. The header
// authenticates, so the row lists as authenticated and the removal
// machine accepts it.
func maintenanceScratchArtifact(t *testing.T, directory string, attempt [16]byte, ordinal uint32) string {
	t.Helper()
	var header [128]byte
	copy(header[0:8], "IPR4SCR1")
	format.PutU16(header[8:10], 1)
	format.PutU16(header[10:12], 128)
	format.PutU16(header[12:14], 2) // owner kind: recovery
	copy(header[16:32], []byte{1})  // database id
	format.PutU64(header[32:40], 7) // transaction id
	copy(header[40:56], []byte{2})  // commit nonce
	copy(header[56:72], attempt[:])
	format.PutU32(header[72:76], ordinal)
	securityKind := uint16(1)
	if runtime.GOOS == "windows" {
		securityKind = 2
	}
	format.PutU16(header[76:78], securityKind)
	profile, err := security.Capture()
	if err != nil {
		t.Fatalf("capture the creator profile: %v", err)
	}
	commitment := profile.Commitment()
	copy(header[80:112], commitment[:])
	checksum, ok := format.CRC32CWithZeroed(header[:], 124, 4)
	if !ok {
		t.Fatal("the scratch header has a fixed checksum range")
	}
	format.PutU32(header[124:128], checksum)
	path := filepath.Join(directory, fmt.Sprintf(".iprange-scratch-%x-%08x.tmp", attempt, ordinal))
	if err := os.WriteFile(path, header[:], 0o600); err != nil {
		t.Fatalf("write the scratch artifact: %v", err)
	}
	return path
}

// maintenancePrivateResidue writes one exact-pattern private artifact of
// the given prefix whose content is neither a readable reservation record
// nor readable v4 geometry, which is what a killed publisher leaves behind.
// maintenance.list reports such an artifact without its optional evidence
// members.
func maintenancePrivateResidue(t *testing.T, directory, prefix string, attempt [16]byte) string {
	t.Helper()
	path := filepath.Join(directory, fmt.Sprintf("%s%x.tmp", prefix, attempt))
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write %s residue: %v", prefix, err)
	}
	return path
}

// maintenanceListRows runs iprange.v1.maintenance.list over one directory
// and returns the exact JSONL row bytes it published, in list order.
func maintenanceListRows(t *testing.T, directory string, kinds []string) [][]byte {
	t.Helper()
	rowsPath := filepath.Join(t.TempDir(), "maintenance-rows.jsonl")
	params := mustJSON(t, map[string]any{
		"directory":   directory,
		"kinds":       kinds,
		"max_entries": 64,
		"output": map[string]any{
			"path":               rowsPath,
			"format":             "jsonl",
			"publication_policy": "fail_if_exists",
			"result_budget": map[string]any{
				"max_rows": "64", "max_output_bytes": "65536", "max_open_files": 3,
			},
		},
	})
	if err := ValidateMaintenanceListParams(params); err != nil {
		t.Fatalf("maintenance.list params must validate: %v", err)
	}
	if _, herr := MaintenanceList(rpc.NewSessionState(), params); herr != nil {
		t.Fatalf("maintenance.list %v: [%s] %s", kinds, herr.Code, herr.Message)
	}
	published, err := os.ReadFile(rowsPath)
	if err != nil {
		t.Fatalf("read the published rows: %v", err)
	}
	var rows [][]byte
	for _, line := range bytes.Split(published, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			rows = append(rows, line)
		}
	}
	return rows
}

// maintenanceRemoveRow submits one exact maintenance.list row as the entry
// of iprange.v1.maintenance.remove. The row bytes are spliced into the
// params verbatim, so the handler sees what the list published and the
// test reconstructs no field.
func maintenanceRemoveRow(t *testing.T, row []byte) (map[string]any, *rpc.HandlerError) {
	t.Helper()
	params := make([]byte, 0, len(row)+len(`{"entry":}`))
	params = append(params, `{"entry":`...)
	params = append(params, row...)
	params = append(params, '}')
	if err := ValidateMaintenanceRemoveParams(params); err != nil {
		t.Fatalf("maintenance.remove params of a listed row must validate: %v", err)
	}
	result, herr := MaintenanceRemove(rpc.NewSessionState(), json.RawMessage(params))
	if herr != nil {
		return nil, herr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode the removal result: %v", err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode the removal result: %v", err)
	}
	return decoded, nil
}

// decodeMaintenanceRow reads one published JSONL row back into its members.
func decodeMaintenanceRow(t *testing.T, row []byte) map[string]any {
	t.Helper()
	object := map[string]any{}
	if err := json.Unmarshal(row, &object); err != nil {
		t.Fatalf("decode list row %s: %v", row, err)
	}
	return object
}

// requireMaintenanceRemovalFacts checks one successful removal terminal
// against the source-presence fact the caller expects: the first removal
// of a listed row reports the artifact it retired, a second removal of the
// same row reports the durable absence.
func requireMaintenanceRemovalFacts(t *testing.T, result map[string]any, sourcePresent bool) {
	t.Helper()
	if got := result["method"]; got != "iprange.v1.maintenance.remove" {
		t.Fatalf("result.method = %v, want iprange.v1.maintenance.remove", got)
	}
	removal, ok := result["removal"].(map[string]any)
	if !ok {
		t.Fatalf("result.removal = %T, want an object", result["removal"])
	}
	if got := removal["source_present"]; got != sourcePresent {
		t.Fatalf("removal.source_present = %v, want %v", got, sourcePresent)
	}
	if got := removal["cleanup_state"]; got != "clean" {
		t.Fatalf("removal.cleanup_state = %v, want clean", got)
	}
}

// maintenanceRoundtripAttempt is one fixed attempt identity per kind, so
// each kind owns an exact private name that no other probe shares.
func maintenanceRoundtripAttempt(marker byte) [16]byte {
	var attempt [16]byte
	for index := range attempt {
		attempt[index] = byte(index*16) | marker
	}
	attempt[15] = marker
	return attempt
}

// requireMaintenanceRoundtrip creates residue of one kind in a fresh
// directory, lists it, and removes the exact bytes the list published.
// Every kind must accept its own unchanged row, retire the artifact, and
// report the durable absence when the same row is replayed.
func requireMaintenanceRoundtrip(t *testing.T, kind string, mark byte) {
	t.Helper()
	directory := t.TempDir()
	var residue string
	switch kind {
	case "scratch":
		residue = maintenanceScratchArtifact(t, directory, maintenanceRoundtripAttempt(mark), 7)
	case "reservation":
		residue = maintenancePrivateResidue(t, directory, ".iprange-reservation-", maintenanceRoundtripAttempt(mark))
	case "publication_temp":
		residue = maintenancePrivateResidue(t, directory, ".iprange-publish-", maintenanceRoundtripAttempt(mark))
	default:
		t.Fatalf("unknown maintenance kind %q", kind)
	}
	rows := maintenanceListRows(t, directory, []string{kind})
	if len(rows) != 1 {
		t.Fatalf("%s: list published %d rows %q, want the one planted artifact", kind, len(rows), rows)
	}
	row := rows[0]
	if got := decodeMaintenanceRow(t, row)["kind"]; got != kind {
		t.Fatalf("%s: row kind = %v", kind, got)
	}
	result, herr := maintenanceRemoveRow(t, row)
	if herr != nil {
		t.Fatalf("%s: the unchanged list row %s must be accepted by maintenance.remove: [%s] %s",
			kind, row, herr.Code, herr.Message)
	}
	requireMaintenanceRemovalFacts(t, result, true)
	if _, err := os.Lstat(residue); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: residue %s survived the removal (err = %v)", kind, residue, err)
	}
	result, herr = maintenanceRemoveRow(t, row)
	if herr != nil {
		t.Fatalf("%s: replaying the same row must report the durable absence: [%s] %s",
			kind, herr.Code, herr.Message)
	}
	requireMaintenanceRemovalFacts(t, result, false)
	if left := maintenanceListRows(t, directory, []string{kind}); len(left) != 0 {
		t.Fatalf("%s: list still reports %d rows after the removal: %q", kind, len(left), left)
	}
}

// TestMaintenanceListRowsRoundTripIntoRemoveForEveryRemovableKind is the
// round-trip gate of iprange-jsonrpc-v1.md: for every maintenance kind this
// platform can produce, residue is planted, listed, and removed with the
// exact row bytes the list published. The reservation and publication_temp
// rows of unreadable residue carry no evidence members at all, which is the
// form a killed publisher leaves behind and the form the removal wire
// handler must accept.
func TestMaintenanceListRowsRoundTripIntoRemoveForEveryRemovableKind(t *testing.T) {
	// Each kind runs as its own subtest so one refused row cannot hide a
	// second refused row.
	for _, probe := range []struct {
		kind  string
		mark  byte
		extra func(t *testing.T, directory, residue string)
	}{
		{kind: "scratch", mark: 0xa1},
		{kind: "reservation", mark: 0xb2},
		{kind: "publication_temp", mark: 0xc3},
	} {
		kind, mark := probe.kind, probe.mark
		t.Run(kind, func(t *testing.T) {
			requireMaintenanceRoundtrip(t, kind, mark)
		})
	}

	// One directory holding every kind at once keeps the list ordering and
	// the per-kind row shapes the removal must accept.
	directory := t.TempDir()
	paths := []string{
		maintenanceScratchArtifact(t, directory, maintenanceRoundtripAttempt(0xd1), 3),
		maintenancePrivateResidue(t, directory, ".iprange-reservation-", maintenanceRoundtripAttempt(0xe2)),
		maintenancePrivateResidue(t, directory, ".iprange-publish-", maintenanceRoundtripAttempt(0xf3)),
	}
	kinds := []string{"scratch", "reservation", "publication_temp"}
	rows := maintenanceListRows(t, directory, kinds)
	if len(rows) != len(kinds) {
		t.Fatalf("list published %d rows %q, want one per kind", len(rows), rows)
	}
	for index, kind := range kinds {
		if got := decodeMaintenanceRow(t, rows[index])["kind"]; got != kind {
			t.Fatalf("row %d kind = %v, want %s", index, got, kind)
		}
	}
	// The unreadable reservation and the partial publication output are
	// reported without their optional evidence members.
	if _, present := decodeMaintenanceRow(t, rows[1])["evidence"]; present {
		t.Fatalf("reservation row of an unreadable record must omit evidence: %s", rows[1])
	}
	publication := decodeMaintenanceRow(t, rows[2])
	for _, member := range []string{"tuple", "digest"} {
		if _, present := publication[member]; present {
			t.Fatalf("publication_temp row of partial content must omit %s: %s", member, rows[2])
		}
	}
	for index, row := range rows {
		result, herr := maintenanceRemoveRow(t, row)
		if herr != nil {
			t.Fatalf("the unchanged %s row %s must be accepted by maintenance.remove: [%s] %s",
				kinds[index], row, herr.Code, herr.Message)
		}
		requireMaintenanceRemovalFacts(t, result, true)
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("residue %s survived the removal (err = %v)", path, err)
		}
	}
	if left := maintenanceListRows(t, directory, kinds); len(left) != 0 {
		t.Fatalf("list still reports %d rows after the removals: %q", len(left), left)
	}
}

// maintenanceRowProbe is one row the list emitters produce, with the
// removal outcome its shape must produce. authorized marks a row that
// carries the opaque authenticated removal identity of its kind; only such
// a row is a removable entry (iprange-jsonrpc-v1.md, maintenance.list), so
// only such a row may reach the SDK.
type maintenanceRowProbe struct {
	name       string
	row        map[string]any
	authorized bool
}

// maintenanceListEmittedRows returns one row per (kind, scan outcome) pair
// maintenance.list can publish, produced by the list emitters themselves so
// the row shape cannot drift from what the method serves.
func maintenanceListEmittedRows(directory string) []maintenanceRowProbe {
	identity := maintenanceFileIdentityOf(1, 2)
	artifact := maintenanceFileIdentityOf(3, 4)
	attempt := [16]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x01}
	tuple := &iprangedb.PublicationTuple{
		DatabaseID:    [16]byte{1, 2, 3},
		TransactionID: 7,
		CommitNonce:   [16]byte{4, 5, 6},
	}
	digest := &iprangedb.PublicationDigest{ByteLength: 8192}
	ordinal := uint32(3)
	evidence := func(previous bool) *iprangedb.AbandonedReservationEvidence {
		value := &iprangedb.AbandonedReservationEvidence{
			Policy: iprangedb.AbandonedReservationPolicyReplaceExisting,
			Phase:  iprangedb.AbandonedReservationPhasePrepared,
			Output: iprangedb.PublicationOutputEvidence{
				Identity: identity, Tuple: *tuple, Digest: *digest,
			},
		}
		if previous {
			value.Previous = &iprangedb.AbandonedReservationPrevious{Identity: artifact, Digest: *digest}
		}
		return value
	}
	// The Windows housekeeping scan reports a candidate whose name is not
	// canonical without its attempt identity, ordinal, or envelope
	// identity. Such a row documents the residue, but it carries no
	// authenticated removal identity, so it is not a removable entry.
	housekeeping := func(authorized, optional bool) *iprangedb.WindowsHousekeepingEntry {
		entry := &iprangedb.WindowsHousekeepingEntry{
			DirectoryIdentity: identity,
			CandidateKind:     iprangedb.WindowsHousekeepingCandidateEnvelope,
			BasenameEncoding:  2,
			Basename:          []byte{0x2e, 0, 0x74, 0},
		}
		if authorized {
			entry.Identity = &artifact
			entry.AttemptID = &attempt
			entry.Ordinal = &ordinal
		}
		if optional {
			entry.Artifact = &iprangedb.HousekeepingArtifact{}
			entry.Problem = &iprangedb.Error{Code: iprangedb.ErrorCleanupConflict, Detail: "names conflict"}
		}
		return entry
	}
	scratch := func(authenticated bool) *iprangedb.AbandonedScratchEntry {
		entry := &iprangedb.AbandonedScratchEntry{
			DirectoryIdentity: identity, ArtifactIdentity: artifact,
			AttemptID: attempt, Ordinal: ordinal,
		}
		if authenticated {
			entry.Authentication = iprangedb.AbandonedScratchAuthentication{
				Authenticated: true, Owner: iprangedb.ScratchOwnerRecovery,
			}
		}
		return entry
	}
	rows := []maintenanceRowProbe{
		{"scratch authenticated", scratchEntryValue(directory, scratch(true)), true},
		{"scratch unauthenticated", scratchEntryValue(directory, scratch(false)), true},
		{"reservation without evidence", reservationEntryValue(directory, &iprangedb.AbandonedReservationEntry{
			DirectoryIdentity: identity, ArtifactIdentity: artifact, PublicationAttemptID: attempt,
		}), true},
		{"reservation with evidence", reservationEntryValue(directory, &iprangedb.AbandonedReservationEntry{
			DirectoryIdentity: identity, ArtifactIdentity: artifact, PublicationAttemptID: attempt,
			Evidence: evidence(false),
		}), true},
		{"reservation with evidence and previous", reservationEntryValue(directory, &iprangedb.AbandonedReservationEntry{
			DirectoryIdentity: identity, ArtifactIdentity: artifact, PublicationAttemptID: attempt,
			Evidence: evidence(true),
		}), true},
		{"publication_temp without evidence", publicationTempEntryValue(directory, &iprangedb.AbandonedPublicationTempEntry{
			DirectoryIdentity: identity, ArtifactIdentity: artifact, PublicationAttemptID: attempt,
		}), true},
		{"publication_temp with evidence", publicationTempEntryValue(directory, &iprangedb.AbandonedPublicationTempEntry{
			DirectoryIdentity: identity, ArtifactIdentity: artifact, PublicationAttemptID: attempt,
			Tuple: tuple, Digest: digest,
		}), true},
		{"windows_housekeeping without the optional members", housekeepingEntryValue(directory, housekeeping(true, false)), true},
		{"windows_housekeeping with every member", housekeepingEntryValue(directory, housekeeping(true, true)), true},
		{"windows_housekeeping without a removal identity", housekeepingEntryValue(directory, housekeeping(false, false)), false},
	}
	// The authenticated removal identity of a windows_housekeeping entry is
	// indivisible: a row that carries only part of it names residue the
	// removal cannot authorize, so it is not a removable entry and remove
	// must refuse it rather than fabricate the missing half.
	partialHousekeeping := func(member string) map[string]any {
		row := housekeepingEntryValue(directory, housekeeping(true, false))
		delete(row, member)
		return row
	}
	rows = append(rows,
		maintenanceRowProbe{"windows_housekeeping without envelope identity", partialHousekeeping("identity"), false},
		maintenanceRowProbe{"windows_housekeeping without attempt id", partialHousekeeping("attempt_id"), false},
		maintenanceRowProbe{"windows_housekeeping without ordinal", partialHousekeeping("ordinal"), false},
	)
	return rows
}

// TestMaintenanceRemoveAcceptsEveryRemovableListRow hands each row the list
// emitters produce to maintenance.remove unchanged. An authorized row must
// reach the SDK: the refusals a caller may then see are the factual SDK
// outcomes of the probe directory (a foreign directory identity, or the
// platform that has no housekeeping machine), never a schema refusal of a
// member the list is allowed to omit. A row without its kind's authenticated
// removal identity is not a removable entry and is refused before any
// destructive step.
func TestMaintenanceRemoveAcceptsEveryRemovableListRow(t *testing.T) {
	directory := t.TempDir()
	for _, probe := range maintenanceListEmittedRows(directory) {
		row, err := rpc.MarshalJSONL(probe.row)
		if err != nil {
			t.Fatalf("%s: encode the emitted row: %v", probe.name, err)
		}
		_, herr := maintenanceRemoveRow(t, row)
		if !probe.authorized {
			if herr == nil || herr.Code != "invalid_argument" {
				t.Fatalf("%s: a row without its authenticated removal identity must be refused before any destructive step, got %v (%s)",
					probe.name, herr, row)
			}
			continue
		}
		if herr == nil {
			continue
		}
		if herr.Code == "invalid_argument" {
			t.Fatalf("%s: maintenance.remove refused a removable row maintenance.list emits: %s: [%s] %s",
				probe.name, row, herr.Code, herr.Message)
		}
		if strings.Contains(string(row), `"windows_housekeeping"`) {
			if runtime.GOOS == "windows" {
				continue
			}
			if herr.Code != "os_unsupported" {
				t.Fatalf("%s: the housekeeping removal must reach the platform refusal, got [%s] %s",
					probe.name, herr.Code, herr.Message)
			}
			continue
		}
		if herr.Code != "directory_identity_mismatch" {
			t.Fatalf("%s: the removal must reach the SDK identity proof, got [%s] %s",
				probe.name, herr.Code, herr.Message)
		}
	}
}

// TestMaintenanceRemoveStillValidatesEveryPresentMember pins the other half
// of the round-trip rule: a member the list may omit is optional, but a
// member a caller actually sends is validated exactly as before, so a
// truncated, forged, or half-evidence row is refused before any destructive
// step.
func TestMaintenanceRemoveStillValidatesEveryPresentMember(t *testing.T) {
	directory := t.TempDir()
	rows := map[string]map[string]any{}
	for _, probe := range maintenanceListEmittedRows(directory) {
		rows[probe.name] = probe.row
	}
	clone := func(name string) map[string]any {
		encoded, err := json.Marshal(rows[name])
		if err != nil {
			t.Fatalf("clone %s: %v", name, err)
		}
		object := map[string]any{}
		if err := json.Unmarshal(encoded, &object); err != nil {
			t.Fatalf("clone %s: %v", name, err)
		}
		return object
	}
	nested := func(object map[string]any, path ...string) map[string]any {
		current := object
		for _, member := range path {
			next, ok := current[member].(map[string]any)
			if !ok {
				t.Fatalf("member %q of the emitted row is not an object", member)
			}
			current = next
		}
		return current
	}
	const removableRow = "publication_temp with evidence"

	// publication_temp: the evidence pair is optional as a pair, and every
	// member that is present must be complete.
	tupleWithoutDigest := clone(removableRow)
	delete(tupleWithoutDigest, "digest")
	digestWithoutTuple := clone(removableRow)
	delete(digestWithoutTuple, "tuple")
	nullTuple := clone("publication_temp without evidence")
	nullTuple["tuple"] = nil
	badDigest := clone(removableRow)
	nested(badDigest, "digest")["sha512"] = "not-a-digest"
	foreign := clone("publication_temp without evidence")
	foreign["basename"] = "synthesized"
	noArtifact := clone("publication_temp without evidence")
	delete(noArtifact, "artifact_identity")

	// reservation: evidence is optional, but a present evidence object is
	// validated to the same depth as before.
	nullEvidence := clone("reservation without evidence")
	nullEvidence["evidence"] = nil
	forgedEvidence := clone("reservation with evidence")
	nested(forgedEvidence, "evidence")["forged"] = true
	blindEvidence := clone("reservation with evidence")
	delete(nested(blindEvidence, "evidence"), "output")
	truncatedEvidence := clone("reservation with evidence")
	nested(truncatedEvidence, "evidence", "output", "digest")["byte_length"] = "08192"

	// scratch: the ownership-header class is always part of the row.
	noAuthentication := clone("scratch authenticated")
	delete(noAuthentication, "authentication")
	forgedAuthentication := clone("scratch authenticated")
	nested(forgedAuthentication, "authentication")["owner"] = "attacker"

	cases := []struct {
		name  string
		entry map[string]any
	}{
		{"publication_temp tuple without digest", tupleWithoutDigest},
		{"publication_temp digest without tuple", digestWithoutTuple},
		{"publication_temp null tuple", nullTuple},
		{"publication_temp malformed digest", badDigest},
		{"publication_temp unknown member", foreign},
		{"publication_temp without artifact identity", noArtifact},
		{"reservation null evidence", nullEvidence},
		{"reservation evidence with unknown member", forgedEvidence},
		{"reservation evidence without output", blindEvidence},
		{"reservation evidence with non-canonical byte_length", truncatedEvidence},
		{"scratch without authentication", noAuthentication},
		{"scratch with a forged owner", forgedAuthentication},
	}
	for _, probe := range cases {
		row, err := rpc.MarshalJSONL(probe.entry)
		if err != nil {
			t.Fatalf("%s: encode the row: %v", probe.name, err)
		}
		_, herr := maintenanceRemoveRow(t, row)
		if herr == nil || herr.Code != "invalid_argument" {
			t.Fatalf("%s: must be refused with invalid_argument, got %v (%s)", probe.name, herr, row)
		}
	}
}
