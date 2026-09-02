package handlers

import (
	"encoding/json"
	"testing"
)

// TestValidatorStrictCanonicalDecimals pins that every param validator
// rejects non-canonical decimal strings at validation time with the
// same strictness as the Rust u64_string parsers: non-digits, leading
// zeros, and overflow never pass the validator stage.
func TestValidatorStrictCanonicalDecimals(t *testing.T) {
	budget := func(fields map[string]any) map[string]any {
		base := map[string]any{
			"max_heap_bytes":    "67108864",
			"max_private_pages": "64",
			"max_growth_pages":  "64",
			"max_open_files":    32,
		}
		for k, v := range fields {
			base[k] = v
		}
		return base
	}
	for _, bad := range []string{"abc", "00", "18446744073709551616"} {
		// writer budget (database.metadata.replace)
		params, err := json.Marshal(map[string]any{
			"path":          "/tmp/probe",
			"metadata":      map[string]any{"mode": "clear"},
			"writer_budget": budget(map[string]any{"max_heap_bytes": bad}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateDatabaseMetadataReplaceParams(params); err == nil {
			t.Fatalf("writer_budget max_heap_bytes=%q must be refused at validation", bad)
		}
		// validation budget (iprange.v1.validate)
		vparams, err := json.Marshal(map[string]any{
			"path": "/tmp/probe",
			"mode": map[string]any{"kind": "immutable_current"},
			"validation_budget": map[string]any{
				"max_heap_bytes":    bad,
				"max_open_files":    32,
				"max_scratch_bytes": "0",
				"max_scratch_files": 0,
			},
			"findings_output": map[string]any{
				"path": "/tmp/findings.jsonl", "format": "jsonl",
				"publication_policy": "none",
				"result_budget":      map[string]any{"max_rows": "1", "max_output_bytes": "4096", "max_open_files": 1},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateValidateParams(vparams); err == nil {
			t.Fatalf("validation_budget max_heap_bytes=%q must be refused at validation", bad)
		}
		// snapshot budget (iprange.v1.snapshot)
		sparams, err := json.Marshal(map[string]any{
			"source": map[string]any{"path": "/tmp/probe", "mode": "immutable"},
			"snapshot_budget": map[string]any{
				"max_heap_bytes":   bad,
				"max_output_pages": "64",
				"max_open_files":   32,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateSnapshotParams(sparams); err == nil {
			t.Fatalf("snapshot_budget max_heap_bytes=%q must be refused at validation", bad)
		}
	}
	// delivery max_output_bytes (export/query file delivery)
	dparams, err := json.Marshal(map[string]any{
		"delivery": map[string]any{
			"mode":               "file",
			"path":               "/tmp/probe",
			"publication_policy": "replace_existing",
			"max_output_bytes":   "abc",
			"max_open_files":     32,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodeObject(dparams)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDelivery(raw); err == nil {
		t.Fatal("delivery max_output_bytes=\"abc\" must be refused at validation")
	}
	// canonical values still pass validation
	good, err := json.Marshal(map[string]any{
		"path":          "/tmp/probe",
		"metadata":      map[string]any{"mode": "clear"},
		"writer_budget": budget(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDatabaseMetadataReplaceParams(good); err != nil {
		t.Fatalf("canonical writer budget must pass: %v", err)
	}
}

// TestValidatorRejectsNullMembers pins that a present null member is
// never treated as absent or as a zero value: every primitive decoder
// refuses null with the same message class as the Rust as_str/as_u64/
// as_object validators (absent is the only absent form).
func TestValidatorRejectsNullMembers(t *testing.T) {
	// validate with a null scratch_directory (disable-scratch form
	// requires the member to be absent, not null).
	vparams, err := json.Marshal(map[string]any{
		"path": "/tmp/probe",
		"mode": map[string]any{"kind": "immutable_current"},
		"validation_budget": map[string]any{
			"max_heap_bytes":    "1",
			"max_open_files":    32,
			"max_scratch_bytes": "0",
			"max_scratch_files": 0,
			"scratch_directory": nil,
		},
		"findings_output": map[string]any{
			"path": "/tmp/f.jsonl", "format": "jsonl",
			"publication_policy": "none",
			"result_budget":      map[string]any{"max_rows": "1", "max_output_bytes": "4096", "max_open_files": 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateValidateParams(vparams); err == nil {
		t.Fatal("validation_budget.scratch_directory=null must be refused")
	}
	// writer budget with a null integral member.
	wparams, err := json.Marshal(map[string]any{
		"path":     "/tmp/probe",
		"metadata": map[string]any{"mode": "clear"},
		"writer_budget": map[string]any{
			"max_heap_bytes": "64", "max_private_pages": "64",
			"max_growth_pages": "64", "max_open_files": nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDatabaseMetadataReplaceParams(wparams); err == nil {
		t.Fatal("writer_budget.max_open_files=null must be refused")
	}
	// reader.open with a null source object.
	rparams, err := json.Marshal(map[string]any{"source": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReaderOpen(rparams); err == nil {
		t.Fatal("reader.open source=null must be refused")
	}
	// delivery with a null max_output_bytes string.
	dparams, err := json.Marshal(map[string]any{
		"delivery": map[string]any{
			"mode": "file", "path": "/tmp/probe", "publication_policy": "none",
			"max_output_bytes": nil, "max_open_files": 32,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodeObject(dparams)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDelivery(raw); err == nil {
		t.Fatal("delivery.max_output_bytes=null must be refused")
	}
}

// housekeepingEntry builds one valid maintenance.remove housekeeping
// entry (the artifact and problem members are required objects;
// Rust maintenance.rs remove entry fields).
func housekeepingEntry() map[string]any {
	return map[string]any{
		"kind":               "housekeeping",
		"directory":          "/tmp/probe",
		"directory_identity": map[string]any{"volume": "1", "file": "2"},
		"candidate_kind":     "envelope",
		"basename_encoding":  1,
		"basename":           "Zg==",
		"identity":           map[string]any{"volume": "1", "file": "2"},
		"attempt_id":         "11111111111111111111111111111111",
		"ordinal":            1,
		"artifact":           map[string]any{},
		"problem":            map[string]any{},
	}
}

// TestHousekeepingRemoveNullArtifactPins that a present-but-null
// artifact or problem member in a maintenance.remove entry is refused
// (Rust maintenance.rs as_object ok_or). A complete valid entry passes.
func TestHousekeepingRemoveNullArtifact(t *testing.T) {
	valid := housekeepingEntry()
	obj, err := decodeObject(mustJSON(t, map[string]any{"entry": valid}))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := memberObject(obj, "entry")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, herr := housekeepingRemoveFields(entry); herr != nil {
		t.Fatalf("valid housekeeping entry must pass: %v", herr)
	}
	for _, member := range []string{"artifact", "problem"} {
		broken := housekeepingEntry()
		broken[member] = nil
		obj, err := decodeObject(mustJSON(t, map[string]any{"entry": broken}))
		if err != nil {
			t.Fatal(err)
		}
		entry, err := memberObject(obj, "entry")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, _, herr := housekeepingRemoveFields(entry); herr == nil {
			t.Fatalf("entry.%s=null must be refused", member)
		}
	}
}

// TestCanonicalDecimalEverySurface extends the strictness table to the
// recovery candidate, maintenance tuple, and maintenance digest
// surfaces named by the SOW record.
func TestCanonicalDecimalEverySurface(t *testing.T) {
	candidate := map[string]any{
		"label": "newest", "meta_page": 0,
		"source_identity": map[string]any{"volume": "1", "file": "2"},
		"database_id":     "11111111111111111111111111111111",
		"transaction_id":  "01",
		"commit_nonce":    "11111111111111111111111111111111",
	}
	obj, err := decodeObject(mustJSON(t, candidate))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCandidateObject(obj); err == nil {
		t.Fatal(`candidate.transaction_id="01" must be refused`)
	}
	tuple := map[string]any{
		"database_id":    "11111111111111111111111111111111",
		"transaction_id": "abc",
		"commit_nonce":   "11111111111111111111111111111111",
	}
	obj, err = decodeObject(mustJSON(t, tuple))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTupleObject(obj); err == nil {
		t.Fatal(`tuple.transaction_id="abc" must be refused`)
	}
	digest := map[string]any{
		"byte_length": "064",
		"sha512":      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	obj, err = decodeObject(mustJSON(t, digest))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDigestObject(obj); err == nil {
		t.Fatal(`digest.byte_length="064" must be refused`)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
