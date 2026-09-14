package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// API v1 budgets have no magic zero or unlimited value: every listed u32
// budget member is required and must be at least 1 (spec
// iprange-jsonrpc-v1.md, "Budgets"). Rust enforces this with
// reader::positive_u32 at each budget member
// (iprange-cli/src/rpc/handlers/reader.rs positive_u32), so a zero, a
// quoted zero, a negative, or a value past u32 range is invalid params
// (-32602) and is refused before any path is opened.
//
// The Go side enforces the same bound twice: in the validator, which is
// what the wire sees, and again in the decoder, so a converter can never
// build a zero-open-file budget from an object that skipped validation
// (the precedent is decodeWriterBudget).

// budgetRejectInputs are the values that must never become a budget
// member: the zero the SDK would treat as unlimited, its JSON-string
// spelling, a negative, and one past the u32 range.
var budgetRejectInputs = []string{"0", `"0"`, `-1`, `4294967296`}

// substituteBudgetMember rebuilds one request with budget.<member>
// replaced by the raw JSON text.
func substituteBudgetMember(t *testing.T, request map[string]any, budgetMember, member, raw string) json.RawMessage {
	t.Helper()
	object := map[string]any{}
	for k, v := range request {
		object[k] = v
	}
	budget := map[string]any{}
	for k, v := range object[budgetMember].(map[string]any) {
		budget[k] = v
	}
	budget[member] = json.RawMessage(raw)
	object[budgetMember] = budget
	return mustJSON(t, object)
}

func budgetSnapshotRequest(dir string) map[string]any {
	return map[string]any{
		"source":             map[string]any{"path": filepath.Join(dir, "src.iprange"), "mode": "immutable"},
		"destination":        filepath.Join(dir, "snap.iprange"),
		"publication_policy": "replace_existing",
		"snapshot_budget": map[string]any{
			"max_heap_bytes": "16777216", "max_output_pages": "512", "max_open_files": 32,
		},
	}
}

func budgetAlgebraPublishRequest(dir string) map[string]any {
	return map[string]any{
		"sources": []any{map[string]any{
			"source": map[string]any{"path": filepath.Join(dir, "mem.iprange"), "mode": "immutable"},
			"scope":  map[string]any{"mode": "all"},
			"membership_query_budget": map[string]any{
				"max_heap_bytes": "1048576",
			},
		}},
		"operation":          map[string]any{"kind": "union", "selection": map[string]any{"mode": "all"}},
		"output_mode":        map[string]any{"kind": "flat", "feed": "merged"},
		"value_tag":          map[string]any{"text": "algebra"},
		"metadata":           map[string]any{"mode": "clear"},
		"destination":        filepath.Join(dir, "alg.iprange"),
		"publication_policy": "fail_if_exists",
		"algebra_budget":     map[string]any{"max_heap_bytes": "1048576", "max_sources": 1},
		"algebra_output_budget": map[string]any{
			"max_output_pages": "20000", "max_open_files": 3,
		},
	}
}

func budgetExportRequest(dir string) map[string]any {
	return map[string]any{
		"source":             map[string]any{"path": filepath.Join(dir, "src.iprange"), "mode": "immutable"},
		"view":               map[string]any{"kind": "direct"},
		"format":             "ranges",
		"destination":        filepath.Join(dir, "out.ranges"),
		"publication_policy": "replace_existing",
		"result_budget":      map[string]any{"max_open_files": 3, "max_output_bytes": "65536", "max_rows": "64"},
	}
}

func budgetPublishRequest(dir string) map[string]any {
	return map[string]any{
		"input": map[string]any{"paths": []any{filepath.Join(dir, "ranges.txt")}, "family": "ipv4",
			"fix_network": false, "default_prefix": 32,
			"dns":             map[string]any{"threads": 1, "silent": true},
			"expand_at_paths": false, "max_line_bytes": 1024, "max_expanded_paths": 16},
		"feed":               "a",
		"value_tag":          map[string]any{"text": "a"},
		"metadata":           map[string]any{"mode": "clear"},
		"destination":        filepath.Join(dir, "pub.iprange"),
		"publication_policy": "replace_existing",
		"immutable_feed_budget": map[string]any{
			"max_heap_bytes": "16777216", "max_output_pages": "20000",
			"max_workspace_pages": "20000", "max_open_files": 3,
		},
	}
}

func budgetValidateRequest(dir string) map[string]any {
	return map[string]any{
		"path": filepath.Join(dir, "src.iprange"),
		"mode": map[string]any{"kind": "immutable_current"},
		"validation_budget": map[string]any{
			"max_heap_bytes": "16777216", "max_open_files": 4,
			"max_scratch_bytes": "0", "max_scratch_files": 0,
		},
		"findings_output": map[string]any{
			"path": filepath.Join(dir, "findings.jsonl"), "format": "jsonl",
			"publication_policy": "replace_existing",
			"result_budget":      map[string]any{"max_open_files": 3, "max_output_bytes": "65536", "max_rows": "64"},
		},
	}
}

func budgetRecoverRequest(dir string) map[string]any {
	return map[string]any{
		"source_path": filepath.Join(dir, "src.iprange"), "source_mode": "immutable",
		"candidate": map[string]any{
			"label": "newest", "meta_page": 1,
			"source_identity": map[string]any{"volume": "1", "file": "2"},
			"database_id":     "000102030405060708090a0b0c0d0e0f",
			"transaction_id":  "3",
			"commit_nonce":    "000102030405060708090a0b0c0d0e0f",
		},
		"destination": filepath.Join(dir, "rec.iprange"),
		"recovery_budget": map[string]any{
			"max_heap_bytes": "16777216", "max_open_files": 4, "max_output_pages": "20000",
			"max_scratch_bytes": "0", "max_scratch_files": 0,
		},
		"report_output": map[string]any{
			"path": filepath.Join(dir, "report.jsonl"), "format": "jsonl",
			"publication_policy": "replace_existing",
			"result_budget":      map[string]any{"max_open_files": 3, "max_output_bytes": "65536", "max_rows": "64"},
		},
	}
}

// TestBudgetValidatorsRefuseEveryNonPositiveU32 runs the Rust
// positive_budget_tests shape over every u32 budget member: the valid
// request must pass its validator, and each non-positive or out-of-range
// spelling of the member must be refused.
func TestBudgetValidatorsRefuseEveryNonPositiveU32(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		validate func(json.RawMessage) error
		request  map[string]any
		budget   string
		members  []string
	}{
		{"snapshot_budget.max_open_files", ValidateSnapshotParams, budgetSnapshotRequest(dir), "snapshot_budget", []string{"max_open_files"}},
		{"validation_budget.max_open_files", ValidateValidateParams, budgetValidateRequest(dir), "validation_budget", []string{"max_open_files"}},
		{"recovery_budget.max_open_files", ValidateRecoverParams, budgetRecoverRequest(dir), "recovery_budget", []string{"max_open_files"}},
		{"result_budget.report.max_open_files", ValidateRecoverParams, budgetRecoverRequest(dir), "report_output", nil},
		{"export.result_budget.max_open_files", ValidateExport, budgetExportRequest(dir), "result_budget", []string{"max_open_files"}},
		{"publish.result_budget.max_open_files", ValidateCurrentPublish, budgetPublishRequest(dir), "immutable_feed_budget", []string{"max_open_files"}},
		{"algebra_budget.max_sources", ValidateAlgebraPublishParams, budgetAlgebraPublishRequest(dir), "algebra_budget", []string{"max_sources"}},
		{"algebra_output_budget.max_open_files", ValidateAlgebraPublishParams, budgetAlgebraPublishRequest(dir), "algebra_output_budget", []string{"max_open_files"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.validate(mustJSON(t, tc.request)); err != nil {
				t.Fatalf("the valid baseline request was refused: %v", err)
			}
			members := tc.members
			if members == nil {
				members = []string{"max_open_files"}
				nested := map[string]any{}
				for k, v := range tc.request[tc.budget].(map[string]any) {
					nested[k] = v
				}
				// The descriptor cases carry the budget one level down.
				for _, raw := range budgetRejectInputs {
					nested["result_budget"] = json.RawMessage(mustJSON(t, map[string]any{
						"max_open_files": json.RawMessage(raw), "max_output_bytes": "65536", "max_rows": "64",
					}))
					request := map[string]any{}
					for k, v := range tc.request {
						request[k] = v
					}
					request[tc.budget] = nested
					if err := tc.validate(mustJSON(t, request)); err == nil {
						t.Fatalf("%s.%s = %s accepted, want invalid params", tc.budget, "result_budget.max_open_files", raw)
					} else if !strings.Contains(err.Error(), "positive") {
						t.Fatalf("%s: error %q does not report the positive-u32 bound", tc.name, err)
					}
				}
				return
			}
			for _, member := range members {
				for _, raw := range budgetRejectInputs {
					if err := tc.validate(substituteBudgetMember(t, tc.request, tc.budget, member, raw)); err == nil {
						t.Fatalf("%s.%s = %s accepted, want invalid params", tc.budget, member, raw)
					} else if !strings.Contains(err.Error(), "positive") {
						t.Fatalf("%s: error %q does not report the positive-u32 bound", tc.name, err)
					}
				}
				// The positive boundary stays accepted.
				if err := tc.validate(substituteBudgetMember(t, tc.request, tc.budget, member, "1")); err != nil {
					t.Fatalf("%s.%s = 1 refused: %v", tc.budget, member, err)
				}
				if err := tc.validate(substituteBudgetMember(t, tc.request, tc.budget, member, "4294967295")); err != nil {
					t.Fatalf("%s.%s = u32::MAX refused: %v", tc.budget, member, err)
				}
			}
		})
	}
}

// The decoders repeat the bound, so no path reaches the SDK with a
// zero-open-file budget even when the validator is bypassed (the
// established decodeWriterBudget precedent).
func TestBudgetDecodersRefuseZeroOpenFiles(t *testing.T) {
	dir := t.TempDir()
	t.Run("snapshot_budget", func(t *testing.T) {
		object, err := decodeObject(mustJSON(t, budgetSnapshotRequest(dir)))
		if err != nil {
			t.Fatal(err)
		}
		if _, herr := decodeSnapshotBudget(object); herr != nil {
			t.Fatalf("baseline snapshot budget refused: %v", herr)
		}
		bad := substituteBudgetMember(t, budgetSnapshotRequest(dir), "snapshot_budget", "max_open_files", "0")
		object, err = decodeObject(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, herr := decodeSnapshotBudget(object); herr == nil {
			t.Fatal("decodeSnapshotBudget accepted max_open_files = 0")
		}
	})
	t.Run("validation_budget", func(t *testing.T) {
		{
			object, err := decodeObject(mustJSON(t, budgetValidateRequest(dir)))
			if err != nil {
				t.Fatal(err)
			}
			if _, herr := decodeValidationBudget(object); herr != nil {
				t.Fatalf("baseline validation budget refused: %v", herr)
			}
			bad := substituteBudgetMember(t, budgetValidateRequest(dir), "validation_budget", "max_open_files", "0")
			object, err = decodeObject(bad)
			if err != nil {
				t.Fatal(err)
			}
			if _, herr := decodeValidationBudget(object); herr == nil {
				t.Fatal("decodeValidationBudget accepted max_open_files = 0")
			}
		}
	})
	t.Run("recovery_budget", func(t *testing.T) {
		object, err := decodeObject(mustJSON(t, budgetRecoverRequest(dir)))
		if err != nil {
			t.Fatal(err)
		}
		if _, herr := decodeRecoveryBudget(object); herr != nil {
			t.Fatalf("baseline recovery budget refused: %v", herr)
		}
		bad := substituteBudgetMember(t, budgetRecoverRequest(dir), "recovery_budget", "max_open_files", "0")
		object, err = decodeObject(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, herr := decodeRecoveryBudget(object); herr == nil {
			t.Fatal("decodeRecoveryBudget accepted max_open_files = 0")
		}
	})
	t.Run("export.result_budget", func(t *testing.T) {
		object, err := decodeObject(mustJSON(t, budgetExportRequest(dir)))
		if err != nil {
			t.Fatal(err)
		}
		budget, err := memberObject(object, "result_budget")
		if err != nil {
			t.Fatal(err)
		}
		if _, herr := decodeExportBudget(mustJSON(t, budget)); herr != nil {
			t.Fatalf("baseline result budget refused: %v", herr)
		}
		bad := substituteBudgetMember(t, budgetExportRequest(dir), "result_budget", "max_open_files", "0")
		object, err = decodeObject(bad)
		if err != nil {
			t.Fatal(err)
		}
		budget2, err2 := memberObject(object, "result_budget")
		if err2 != nil {
			t.Fatal(err2)
		}
		if _, herr := decodeExportBudget(mustJSON(t, budget2)); herr == nil {
			t.Fatal("decodeExportBudget accepted max_open_files = 0")
		}
	})
	t.Run("immutable_feed_budget", func(t *testing.T) {
		object, err := decodeObject(mustJSON(t, budgetPublishRequest(dir)))
		if err != nil {
			t.Fatal(err)
		}
		budget, err := memberObject(object, "immutable_feed_budget")
		if err != nil {
			t.Fatal(err)
		}
		if _, herr := immutableBudget(mustJSON(t, budget)); herr != nil {
			t.Fatalf("baseline immutable_feed_budget refused: %v", herr)
		}
		bad := substituteBudgetMember(t, budgetPublishRequest(dir), "immutable_feed_budget", "max_open_files", "0")
		object, err = decodeObject(bad)
		if err != nil {
			t.Fatal(err)
		}
		budget, err = memberObject(object, "immutable_feed_budget")
		if err != nil {
			t.Fatal(err)
		}
		if _, herr := immutableBudget(mustJSON(t, budget)); herr == nil {
			t.Fatal("immutableBudget accepted max_open_files = 0")
		}
	})
}

// max_scratch_files is the one u32 budget member the contract lets stay
// zero: the specification disables scratch by setting BOTH scratch
// limits to zero and omitting scratch_directory (iprange-jsonrpc-v1.md
// "Budgets"), and Rust recovery.rs requires exactly that pairing
// (disabled = bytes == "0" && files == 0). A blanket positive-u32 rule
// here would reject the documented disabled shape.
func TestScratchFilesKeepsThePairedZeroDisabledShape(t *testing.T) {
	dir := t.TempDir()
	base := budgetValidateRequest(dir)
	if err := ValidateValidateParams(mustJSON(t, base)); err != nil {
		t.Fatalf("the paired-zero disabled scratch shape must be accepted: %v", err)
	}
	// Enabling scratch requires both limits and the directory.
	enabled := map[string]any{
		"max_heap_bytes": "16777216", "max_open_files": 4,
		"max_scratch_bytes": "4096", "max_scratch_files": 1,
		"scratch_directory": dir,
	}
	request := map[string]any{}
	for k, v := range base {
		request[k] = v
	}
	request["validation_budget"] = enabled
	if err := ValidateValidateParams(mustJSON(t, request)); err != nil {
		t.Fatalf("the fully enabled scratch shape must be accepted: %v", err)
	}
	// A partial combination is invalid: nonzero bytes with zero files.
	partial := map[string]any{
		"max_heap_bytes": "16777216", "max_open_files": 4,
		"max_scratch_bytes": "4096", "max_scratch_files": 0,
	}
	request["validation_budget"] = partial
	if err := ValidateValidateParams(mustJSON(t, request)); err == nil {
		t.Fatal("a partial scratch configuration was accepted")
	}
	// Zero open files stays invalid in every shape.
	zeroFiles := map[string]any{
		"max_heap_bytes": "16777216", "max_open_files": 0,
		"max_scratch_bytes": "0", "max_scratch_files": 0,
	}
	request["validation_budget"] = zeroFiles
	if err := ValidateValidateParams(mustJSON(t, request)); err == nil {
		t.Fatal("validation_budget.max_open_files = 0 was accepted")
	}
}

// A non-positive budget is refused before any path access: none of the
// named files, including the destination directory, is created.
func TestNonPositiveBudgetRefusesBeforePathAccess(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "never-created")
	requests := []struct {
		name     string
		validate func(json.RawMessage) error
		request  map[string]any
	}{
		{"snapshot", ValidateSnapshotParams, budgetSnapshotRequest(dir)},
		{"export", ValidateExport, budgetExportRequest(dir)},
		{"publish", ValidateCurrentPublish, budgetPublishRequest(dir)},
		{"validate", ValidateValidateParams, budgetValidateRequest(dir)},
		{"recover", ValidateRecoverParams, budgetRecoverRequest(dir)},
	}
	for _, tc := range requests {
		for _, raw := range budgetRejectInputs {
			budgetKey := map[string]string{"snapshot": "snapshot_budget", "export": "result_budget",
				"publish": "immutable_feed_budget", "validate": "validation_budget",
				"recover": "recovery_budget"}[tc.name]
			bad := substituteBudgetMember(t, tc.request, budgetKey, "max_open_files", raw)
			if err := tc.validate(bad); err == nil {
				t.Fatalf("%s budget max_open_files = %s accepted", tc.name, raw)
			}
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the budget validators touched the destination tree: %v", err)
	}
}
