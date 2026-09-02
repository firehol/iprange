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
