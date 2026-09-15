//go:build unix

package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"golang.org/x/sys/unix"
)

// Every caller-supplied input path of these handlers must reach the
// caller-open owner, because the owner is what judges the descriptor that
// open(2) actually returned rather than the caller's earlier path check.
// The refusal-class pins call the owners directly and therefore stay green
// when a caller stops using them: reverting one of these four call sites to
// a plain os.Open removes the protection with no test failure at all (the
// operations finding of the wave-19.24 round).
//
// These pins are the caller half, mirroring the Rust judge-witness control
// (v4/rust/iprange-cli/src/io/caller_open.rs assert_control: an arm that
// read its fixture must have asked the descriptor judgment about it). Each
// case arms the witness, drives the real handler against a plain regular
// fixture, and requires the fixture's path to have been opened through
// calleropen.Open with O_NONBLOCK. An un-instrumented witness is
// unavailable here, so a caller that bypasses the owner answers with zero
// calls and the case fails whatever else it did.
//
// They are unix-tagged for the same reason the Rust witness is
// #[cfg(all(test, unix))]: the O_NONBLOCK open exists only on the POSIX
// arm of each owner (the Windows arm opens plainly because there is no
// FIFO open hazard there), so the pin's subject is the POSIX wiring. The
// platform-neutral half of the same owners is pinned in
// csv_opened_regular_test.go and opened_guard_test.go.
func TestProductionCallerPathsOpenThroughCallerOpen(t *testing.T) {
	t.Run("current.publish reads the @file-list and its input", func(t *testing.T) {
		dir := t.TempDir()
		ranges := filepath.Join(dir, "ranges.txt")
		writeCallerOpenFixture(t, ranges, "192.0.2.0-192.0.2.3\n")
		list := filepath.Join(dir, "list.txt")
		writeCallerOpenFixture(t, list, ranges+"\n")
		destination := filepath.Join(dir, "pub.iprange")

		witness, stop := calleropen.WatchOpensForTest()
		defer stop()
		_, herr := CurrentPublish(rpc.NewSessionState(), mustJSON(t, map[string]any{
			"input": map[string]any{"paths": []any{"@" + list}, "family": "ipv4",
				"fix_network": false, "default_prefix": 32,
				"dns":             map[string]any{"threads": 1, "silent": true},
				"expand_at_paths": true, "max_line_bytes": 1024, "max_expanded_paths": 16},
			"feed":               "a",
			"value_tag":          map[string]any{"text": "a"},
			"metadata":           map[string]any{"mode": "clear"},
			"destination":        destination,
			"publication_policy": "fail_if_exists",
			"immutable_feed_budget": map[string]any{
				"max_heap_bytes": "16777216", "max_output_pages": "20000",
				"max_workspace_pages": "20000", "max_open_files": 3,
			},
		}))
		if herr != nil {
			t.Fatalf("current.publish failed: code=%q message=%q", herr.Code, herr.Message)
		}
		// Both call sites of fileio.expandPaths/fileio.openInput are
		// separate reverts, so each path is pinned on its own: the list
		// file (input.go @file-list open) and the input it named
		// (input.go openInput).
		requirePromptCallerOpen(t, witness, "current.publish @file-list open (fileio/input.go)", list)
		requirePromptCallerOpen(t, witness, "current.publish input open (fileio/input.go)", ranges)
	})

	t.Run("direct.replace reads its CSV", func(t *testing.T) {
		dir := t.TempDir()
		database := newLiveFeed(t, dir, "db.iprange")
		csv := filepath.Join(dir, "rows.csv")
		writeCallerOpenFixture(t, csv, "from,to,value\n192.0.2.8,192.0.2.9,7\n")

		witness, stop := calleropen.WatchOpensForTest()
		defer stop()
		_, herr := DirectReplace(rpc.NewSessionState(), mustJSON(t, map[string]any{
			"path":  database,
			"input": map[string]any{"path": csv, "max_line_bytes": 1024},
			"metadata": map[string]any{
				"mode": "keep",
			},
			"writer_budget": writerBudgetJSON(4),
		}))
		if herr != nil {
			t.Fatalf("direct.replace failed: code=%q message=%q", herr.Code, herr.Message)
		}
		requirePromptCallerOpen(t, witness, "direct.replace CSV open (handlers/live.go)", csv)
	})

	t.Run("database.metadata.replace reads its metadata source", func(t *testing.T) {
		dir := t.TempDir()
		database := newLiveFeed(t, dir, "db.iprange")
		source := filepath.Join(dir, "metadata.json")
		writeCallerOpenFixture(t, source, "{\"pinned\":\"caller-open\"}\n")

		witness, stop := calleropen.WatchOpensForTest()
		defer stop()
		_, herr := DatabaseMetadataReplace(rpc.NewSessionState(), mustJSON(t, map[string]any{
			"path": database,
			"metadata": map[string]any{
				"mode": "replace_file", "path": source,
			},
			"writer_budget": writerBudgetJSON(4),
		}))
		if herr != nil {
			t.Fatalf("database.metadata.replace failed: code=%q message=%q", herr.Code, herr.Message)
		}
		requirePromptCallerOpen(t, witness, "metadata source open (handlers/lifecycle_facts.go)", source)
	})
}

// requirePromptCallerOpen asserts that one production handler opened the
// path it was given through calleropen.Open, with the prompt O_NONBLOCK the
// owner is required to pass. Zero calls means the caller opened the path
// itself; a missing flag means the owner lost the behaviour that keeps a
// FIFO behind the path from wedging the request thread.
func requirePromptCallerOpen(t *testing.T, witness *calleropen.Witness, label, path string) {
	t.Helper()
	counts := witness.Counts(path)
	if counts.OpenCalls == 0 {
		t.Fatalf("%s: %s was never opened through calleropen.Open, so the caller opened the path itself and judges a check that may already be stale (a FIFO behind it wedges the request inside open(2))",
			label, path)
	}
	if counts.LastFlags&unix.O_NONBLOCK == 0 {
		t.Fatalf("%s: %s was opened through calleropen.Open with flags %#x, which lacks O_NONBLOCK, so the open waits for a writer on a node that is not a regular file",
			label, path, counts.LastFlags)
	}
}

// writeCallerOpenFixture creates one caller-readable input file.
func writeCallerOpenFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
