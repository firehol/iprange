package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// Writer arms that address a live database through a symlink must report
// the live wrong-state class, not the io class of the failed open. Rust
// opens the writer target with Directory::open_regular_with_links, which
// classifies the O_NOFOLLOW ELOOP errno as NamespaceError::NotRegular and
// folds it through namespace_error into Error::WrongMode; the mapping
// owner therefore has to classify the *failed* open, not only a
// descriptor it actually obtained (live_namespace.rs:307-310).
//
// The read-only arms keep their own, already-aligned class: Rust
// database_file::open_read_only propagates the io::Error of the same
// failed open, so a symlinked source stays the io class there.
//
// The sibling case that pins every writer arm keeps the plain io class when
// the writer-target open fails for another reason needs a POSIX mode bit to
// make that open fail, so it lives in writer_symlink_class_unix_test.go.
func symlinkToLive(t *testing.T, dir, name string) (target, link string) {
	t.Helper()
	target = newLiveFeed(t, dir, name)
	link = filepath.Join(dir, name+".lnk")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
	return target, link
}

// writerArmsOnPath lists every method that opens the live database
// through the writer path, with one otherwise-valid request whose target
// is the given path.
func writerArmsOn(dir string) []struct {
	name   string
	call   func(*rpc.SessionState, json.RawMessage) (any, *rpc.HandlerError)
	params func(path string) any
} {
	budget := writerBudgetJSON(4)
	arms := []struct {
		name   string
		call   func(*rpc.SessionState, json.RawMessage) (any, *rpc.HandlerError)
		params func(path string) any
	}{
		{"direct.replace", DirectReplace, func(p string) any {
			return map[string]any{"path": p, "input": map[string]any{
				"path": filepath.Join(dir, "rows.csv"), "max_line_bytes": 1024},
				"metadata": map[string]any{"mode": "keep"}, "writer_budget": budget}
		}},
		{"database.metadata.replace", DatabaseMetadataReplace, func(p string) any {
			return map[string]any{"path": p, "metadata": map[string]any{
				"mode": "replace_utf8", "text": "q"}, "writer_budget": budget}
		}},
		{"database.reclaim", DatabaseReclaim, func(p string) any {
			return map[string]any{"path": p, "max_transactions": "8",
				"max_pages": "8", "writer_budget": budget}
		}},
		{"feeds.create", FeedsCreate, func(p string) any {
			return map[string]any{"path": p, "feed": "z", "current": map[string]any{
				"source": map[string]any{"path": p, "mode": "immutable"}, "feed": "alpha"},
				"metadata": map[string]any{"mode": "keep"}, "writer_budget": budget}
		}},
		{"feeds.delete", FeedsDelete, func(p string) any {
			return map[string]any{"path": p, "feed": "alpha",
				"metadata": map[string]any{"mode": "keep"}, "writer_budget": budget}
		}},
		{"feeds.rename", FeedsRename, func(p string) any {
			return map[string]any{"path": p, "old_feed": "alpha", "new_feed": "beta",
				"metadata": map[string]any{"mode": "keep"}, "writer_budget": budget}
		}},
		{"feeds.import", FeedsImport, func(p string) any {
			return map[string]any{"path": p, "source": map[string]any{
				"path": p, "mode": "immutable"},
				"metadata": map[string]any{"mode": "keep"}, "writer_budget": budget}
		}},
	}
	return arms
}

// A symlinked writer target is the wrong-state class on every writer
// arm, with the not_started outcome (no durable attempt has begun).
// Before the classification of the failed read-write open, each arm
// answered io/not_started, which advertises a storage failure for what
// is a namespace-policy refusal.
func TestWriterArmsRefuseSymlinkedLiveDatabaseAsWrongState(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "rows.csv")
	if err := os.WriteFile(csv, []byte("192.0.2.0-192.0.2.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, link := symlinkToLive(t, dir, "db.iprange")
	for _, arm := range writerArmsOn(dir) {
		t.Run(arm.name, func(t *testing.T) {
			_, herr := arm.call(rpc.NewSessionState(), mustJSON(t, arm.params(link)))
			if herr == nil {
				t.Fatalf("%s accepted a symlinked live database", arm.name)
			}
			if herr.Code != "wrong_state" || herr.Outcome != "not_started" {
				t.Fatalf("%s: code=%q outcome=%q, want wrong_state/not_started",
					arm.name, herr.Code, herr.Outcome)
			}
		})
	}
}
