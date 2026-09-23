package handlers

import (
	"encoding/json"
	"os"
	"testing"
)

// writerBudgetJSON builds one otherwise-valid writer_budget object with
// max_open_files replaced by value.
func writerBudgetJSON(maxOpenFiles any) json.RawMessage {
	data, err := json.Marshal(map[string]any{
		"max_heap_bytes":    "16777216",
		"max_private_pages": "256",
		"max_growth_pages":  "256",
		"max_open_files":    maxOpenFiles,
	})
	if err != nil {
		panic(err)
	}
	return data
}

// writer_budget.max_open_files = 0 is invalid params (Rust lifecycle.rs
// validates the member with positive_u32). The refusal must come from
// the validator: a zero budget that crosses it surfaces later as
// insufficient_resource_budget or a handler-level invalid_argument,
// after live writer opens — a different contract than the one Rust
// reports.
func TestValidateWriterBudgetObjectRefusesZeroOpenFiles(t *testing.T) {
	object, err := decodeObject(writerBudgetJSON(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWriterBudgetObject(object); err == nil {
		t.Fatal("validateWriterBudgetObject accepted max_open_files = 0")
	}
	// The other members stay valid, so the refusal is the lower bound
	// and not a shape problem.
	for _, value := range []any{1, 4, uint32(0xFFFFFFFF)} {
		object, err := decodeObject(writerBudgetJSON(value))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateWriterBudgetObject(object); err != nil {
			t.Fatalf("validateWriterBudgetObject(%v) = %v, want acceptance", value, err)
		}
	}
}

// The converter repeats the validator's lower bound, so no caller can
// build a zero-open-file page budget from an unchecked object.
func TestDecodeWriterBudgetRefusesZeroOpenFiles(t *testing.T) {
	object, err := decodeObject(writerBudgetJSON(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeWriterBudget(object); err == nil {
		t.Fatal("decodeWriterBudget accepted max_open_files = 0")
	}
	object, err = decodeObject(writerBudgetJSON(4))
	if err != nil {
		t.Fatal(err)
	}
	budget, err := decodeWriterBudget(object)
	if err != nil {
		t.Fatalf("decodeWriterBudget(4) = %v", err)
	}
	if budget.MaxOpenFiles != 4 {
		t.Fatalf("MaxOpenFiles = %d, want 4", budget.MaxOpenFiles)
	}
}

// Methods that carry writer_budget refuse a zero max_open_files in the
// validator without touching the path they were given: the fresh
// directory stays empty, so no writer or source was opened.
func TestWriterBudgetZeroRefusedBeforeFilesystemWork(t *testing.T) {
	cases := []struct {
		name     string
		validate func(json.RawMessage) error
		build    func(dir string, budget json.RawMessage) map[string]any
	}{
		{"database.metadata.replace", ValidateDatabaseMetadataReplaceParams, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange",
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"database.reclaim", ValidateDatabaseReclaimParams, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange",
				"max_transactions": "8", "max_pages": "8",
				"writer_budget": json.RawMessage(b)}
		}},
		{"direct.replace", ValidateDirectReplace, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange",
				"input":         map[string]any{"path": dir + "/absent.csv", "max_line_bytes": 1048576},
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"retention.first_seen.refresh", ValidateFirstSeenRefresh, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange",
				"current":       map[string]any{"source": map[string]any{"path": dir + "/absent-src.iprange", "mode": "immutable"}, "feed": "beta"},
				"refresh_value": 1,
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"retention.last_seen.refresh", ValidateLastSeenRefresh, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange",
				"current":       map[string]any{"source": map[string]any{"path": dir + "/absent-src.iprange", "mode": "immutable"}, "feed": "beta"},
				"refresh_value": 1,
				"cutoff":        5,
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"feeds.create", ValidateFeedsCreate, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange", "feed": "alpha",
				"current":       map[string]any{"source": map[string]any{"path": dir + "/absent-src.iprange", "mode": "immutable"}, "feed": "beta"},
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"feeds.replace", ValidateFeedsReplace, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange", "feed": "alpha",
				"current":       map[string]any{"source": map[string]any{"path": dir + "/absent-src.iprange", "mode": "immutable"}, "feed": "beta"},
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"feeds.delete", ValidateFeedsDelete, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange", "feed": "alpha",
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"feeds.rename", ValidateFeedsRename, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange", "old_feed": "a", "new_feed": "b",
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
		{"feeds.import", ValidateFeedsImport, func(dir string, b json.RawMessage) map[string]any {
			return map[string]any{"path": dir + "/absent.iprange",
				"source":        map[string]any{"path": dir + "/absent-src.iprange", "mode": "immutable"},
				"metadata":      map[string]any{"mode": "clear"},
				"writer_budget": json.RawMessage(b)}
		}},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if err := tc.validate(mustJSON(t, tc.build(dir, writerBudgetJSON(0)))); err == nil {
			t.Fatalf("%s accepted writer_budget.max_open_files = 0", tc.name)
		}
		if err := tc.validate(mustJSON(t, tc.build(dir, writerBudgetJSON(4)))); err != nil {
			t.Fatalf("%s rejected a valid budget: %v", tc.name, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("%s left %d filesystem entries behind a refused budget", tc.name, len(entries))
		}
	}
}
