package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// writeUnopenedDatabase leaves one path that exists but cannot be
// opened as a v4 database, so the source open fails for a domain reason.
func writeUnopenableDatabase(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not a v4 database"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// refreshParams builds one otherwise-valid retention refresh request
// whose coverage source is the given path and mode.
func refreshRequestParams(path, sourcePath, sourceMode string, budget json.RawMessage) map[string]any {
	return map[string]any{
		"path": path,
		"current": map[string]any{
			"source": map[string]any{"path": sourcePath, "mode": sourceMode},
			"feed":   "alpha",
		},
		"refresh_value": 1,
		"cutoff":        5,
		"metadata":      map[string]any{"mode": "clear"},
		"writer_budget": json.RawMessage(budget),
	}
}

// The retention refreshes read their coverage source as read-only
// pre-work that never starts a durable attempt, so a source that cannot
// be opened carries outcome not_started (Rust live.rs
// open_source_reader -> lifecycle::sdk_error(&error, "not_started")).
// The feeds family keeps read_only_failure for the same physical
// failure (Rust feeds.rs open_temporary -> reader::read_error), so the
// two families must not share one outcome.
func TestRefreshSourceOpenFailureUsesNotStartedOutcome(t *testing.T) {
	dir := t.TempDir()
	// The mutation target is a real live database so the writer open
	// succeeds and the refusal under test is the coverage-source open.
	database := newLiveFeed(t, dir, "db.iprange")
	source := writeUnopenableDatabase(t, dir, "source.iprange")
	budget := writerBudgetJSON(4)
	for _, refresh := range []func(*rpc.SessionState, json.RawMessage) (any, *rpc.HandlerError){
		FirstSeenRefresh, LastSeenRefresh,
	} {
		st := rpc.NewSessionState()
		_, herr := refresh(st, mustJSON(t, refreshRequestParams(database, source, "immutable", budget)))
		if herr == nil {
			t.Fatal("refresh accepted an unopenable coverage source")
		}
		if herr.Outcome != "not_started" {
			t.Fatalf("refresh source-open outcome = %q, want not_started", herr.Outcome)
		}
	}
	st := rpc.NewSessionState()
	_, herr := FeedsImport(st, mustJSON(t, map[string]any{
		"path":          database,
		"source":        map[string]any{"path": source, "mode": "immutable"},
		"metadata":      map[string]any{"mode": "clear"},
		"writer_budget": json.RawMessage(budget),
	}))
	if herr == nil {
		t.Fatal("feeds.import accepted an unopenable source")
	}
	if herr.Outcome != "read_only_failure" {
		t.Fatalf("feeds.import source-open outcome = %q, want read_only_failure", herr.Outcome)
	}
}
