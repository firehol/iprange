package handlers

import (
	"testing"
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
