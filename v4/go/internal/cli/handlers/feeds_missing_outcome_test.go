package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// newLiveMembershipFeed creates one live membership database with the
// single feed "alpha" (one address) through the public SDK, so the named
// feed workflows reach the catalog probe instead of refusing the value
// kind first.
func newLiveMembershipFeed(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	tag, err := iprangedb.NewValueTag([]byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iprangedb.CreateLive(path, iprangedb.AddressFamilyIPv4,
		iprangedb.ValueKindMembership, iprangedb.StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
	writer, err := iprangedb.OpenLiveWriter(path, iprangedb.DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = writer.Close() }()
	feed, err := iprangedb.NewFeedName("alpha")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := writer.BeginCreateFeed(feed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.AddRangesV4([]iprangedb.AddressRange4{
		{From: iprangedb.IPv4(0x01010101), To: iprangedb.IPv4(0x01010102)},
	}); err != nil {
		t.Fatal(err)
	}
	finished, err := workflow.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if finished.IsChanged() {
		if _, err := finished.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// feeds.delete and feeds.rename select the feed through the writer's
// read-only catalog probe once the writer is open, so a missing feed is
// the read_only_failure outcome with the name_not_found class (Rust
// feeds.rs run_feed_change -> sdk() -> reader::read_error). The feeds
// family already reported read_only_failure for a failing source open,
// so the two arms of the same family must not disagree about whether
// read-only work happened.
func TestFeedsDeleteAndRenameMissingFeedUseReadOnlyOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		call   func(*rpc.SessionState, json.RawMessage) (any, *rpc.HandlerError)
		params func(path string) any
	}{
		{"feeds.delete", FeedsDelete, func(p string) any {
			return map[string]any{"path": p, "feed": "absent-feed",
				"metadata":      map[string]any{"mode": "keep"},
				"writer_budget": json.RawMessage(writerBudgetJSON(4))}
		}},
		{"feeds.rename", FeedsRename, func(p string) any {
			return map[string]any{"path": p, "old_feed": "absent-feed", "new_feed": "renamed",
				"metadata":      map[string]any{"mode": "keep"},
				"writer_budget": json.RawMessage(writerBudgetJSON(4))}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			database := newLiveMembershipFeed(t, dir, "db.iprange")
			_, herr := tc.call(rpc.NewSessionState(), mustJSON(t, tc.params(database)))
			if herr == nil {
				t.Fatalf("%s accepted a missing feed", tc.name)
			}
			if herr.Code != "name_not_found" || herr.Outcome != "read_only_failure" {
				t.Fatalf("%s: code=%q outcome=%q, want name_not_found/read_only_failure",
					tc.name, herr.Code, herr.Outcome)
			}
		})
	}
}

// The writer open itself stays not_started: no read-only catalog probe
// ever ran, so the arm that cannot even open the database keeps the
// outcome that says so.
func TestFeedsDeleteOnUnopenableDatabaseStaysNotStarted(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.iprange")
	if err := os.WriteFile(junk, []byte("not a v4 database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, herr := FeedsDelete(rpc.NewSessionState(), mustJSON(t, map[string]any{
		"path": junk, "feed": "alpha", "metadata": map[string]any{"mode": "keep"},
		"writer_budget": json.RawMessage(writerBudgetJSON(4))}))
	if herr == nil {
		t.Fatal("feeds.delete accepted an unopenable database")
	}
	if herr.Outcome != "not_started" {
		t.Fatalf("feeds.delete writer-open outcome = %q, want not_started", herr.Outcome)
	}
}
