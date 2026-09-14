//go:build unix

package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// Post-visibility durability of the adapter-owned outputs, triggered the same
// way on both languages: a destination parent that is writable and searchable
// but not readable (mode 0311). Every publication step succeeds — creating the
// private name, writing, syncing, and the atomic link or rename all need write
// and search only — and exactly `open(2)` of the directory for the durability
// sync fails. That is the unresolved window the specification describes: the
// destination holds the complete bytes, their durability is unproven, and the
// implementation must not remove the possibly published artifact.

// unreadableOutputDir creates one output directory that is 0311 and registers
// the permission restore so the test's own cleanup can remove it.
func unreadableOutputDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// syncStage is the directory-synchronization stage name for one output kind.
const (
	metadataSyncStage = "sync metadata output directory"
	removalsSyncStage = "sync removal output directory"
)

// Metadata delivery whose directory sync failed is an unresolved publication:
// the io class with outcome_unknown and the full publication facts, and the
// destination must still hold the complete payload.
func TestMetadataPublicationUnresolvedSyncIsOutcomeUnknown(t *testing.T) {
	for _, policy := range []iprangedb.PublicationPolicy{
		iprangedb.PolicyFailIfExists, iprangedb.PolicyReplaceExisting,
		iprangedb.PolicyReplaceExistingNoRollback,
	} {
		t.Run(policyNameOf(policy), func(t *testing.T) {
			dir := unreadableOutputDir(t)
			destination := filepath.Join(dir, "metadata.json")
			payload := []byte("metadata payload")
			if policy != iprangedb.PolicyFailIfExists {
				// Replacement policies must overwrite a previous file, which
				// also proves the destination really was replaced.
				if err := os.WriteFile(destination, []byte("previous\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, herr := MetadataOutput(destination, payload, policy, 1<<20, 8)
			if herr == nil {
				t.Fatal("synchronizing an unreadable directory was accepted")
			}
			if herr.Code != "io" || herr.Outcome != "outcome_unknown" {
				t.Fatalf("code=%q outcome=%q, want io/outcome_unknown",
					herr.Code, herr.Outcome)
			}
			facts := publicationFacts(t, herr.Details)
			want := map[string]any{
				"outcome":             "outcome_unknown",
				"publication_policy":  policyNameOf(policy),
				"path":                destination,
				"stage":               metadataSyncStage,
				"destination_visible": true,
				"temporary_removed":   true,
				"bytes":               "16",
				"rows":                "1",
				"sha256":              metadataSHA256(payload),
			}
			for name, expected := range want {
				if got, ok := facts[name]; !ok || got != expected {
					t.Fatalf("facts[%s] = %#v (present %v), want %#v", name, got, ok, expected)
				}
			}
			if got, err := os.ReadFile(destination); err != nil || string(got) != string(payload) {
				t.Fatalf("destination = %q (err %v), want the complete payload: the report must "+
					"never destroy a possibly published artifact", got, err)
			}
		})
	}
}

// The removals output has the same semantics on its own: an unresolved sync
// is io/outcome_unknown with its publication facts, and the rows stay on disk.
func TestRemovalsPublicationUnresolvedSyncIsOutcomeUnknown(t *testing.T) {
	dir := unreadableOutputDir(t)
	collector := newTestRemovalCollector(t, dir, "removals.jsonl", iprangedb.PolicyReplaceExisting)
	const row = `{"feed":"coverage"}`
	if err := collector.writeLine(row); err != nil {
		t.Fatal(err)
	}
	_, herr := collector.publish()
	if herr == nil {
		t.Fatal("synchronizing an unreadable directory was accepted")
	}
	if herr.Code != "io" || herr.Outcome != "outcome_unknown" {
		t.Fatalf("code=%q outcome=%q, want io/outcome_unknown", herr.Code, herr.Outcome)
	}
	facts := publicationFacts(t, herr.Details)
	want := map[string]any{
		"outcome":             "outcome_unknown",
		"publication_policy":  "replace_existing",
		"path":                filepath.Join(dir, "removals.jsonl"),
		"stage":               removalsSyncStage,
		"destination_visible": true,
		"destination_content": "created",
		"temporary_removed":   true,
		"rows":                "1",
		"bytes":               "20",
	}
	for name, expected := range want {
		if got, ok := facts[name]; !ok || got != expected {
			t.Fatalf("facts[%s] = %#v (present %v), want %#v", name, got, ok, expected)
		}
	}
	written, err := os.ReadFile(filepath.Join(dir, "removals.jsonl"))
	if err != nil || string(written) != row+"\n" {
		t.Fatalf("removals output = %q (err %v), want the complete row", written, err)
	}
}

// The database commit and the durability of the auxiliary removals output are
// two separate facts and neither may be reported as the other: the committed
// transaction stays reported as committed, while the output whose sync stayed
// unresolved is outcome_unknown with its facts.
func TestFirstSeenRefreshKeepsCommitWhenRemovalsOutputSyncIsUnresolved(t *testing.T) {
	dir := t.TempDir()
	source := newLiveCoverageFeed(t, dir, "coverage.db", "coverage",
		iprangedb.IPv4(0xC0000201), iprangedb.IPv4(0xC000020A), iprangedb.IPv4(0xC6336405))
	target := newFirstSeenTarget(t, dir, "target.db")
	narrow := newLiveCoverageFeed(t, dir, "narrow.db", "coverage", iprangedb.IPv4(0xC6336405))

	// Prime: the first refresh records first_seen for the full coverage.
	if _, herr := FirstSeenRefresh(rpc.NewSessionState(), mustJSON(t, durabilityRefreshParams(target, source, 42, nil))); herr != nil {
		t.Fatalf("priming refresh: code=%q outcome=%q message=%q", herr.Code, herr.Outcome, herr.Message)
	}

	output := unreadableOutputDir(t)
	removals := filepath.Join(output, "removals.jsonl")
	_, herr := FirstSeenRefresh(rpc.NewSessionState(), mustJSON(t,
		durabilityRefreshParams(target, narrow, 84, map[string]any{
			"path":               removals,
			"publication_policy": "replace_existing",
			"result_budget": map[string]any{
				"max_rows":         "10",
				"max_output_bytes": "4096",
				"max_open_files":   1,
			},
		})))
	if herr == nil {
		t.Fatal("synchronizing an unreadable directory was accepted")
	}
	if herr.Code != "io" || herr.Outcome != "committed" {
		t.Fatalf("code=%q outcome=%q, want io/committed (the committed transaction owns the reply)",
			herr.Code, herr.Outcome)
	}
	details, ok := herr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details is %T, want an object", herr.Details)
	}
	result, ok := details["result"].(map[string]any)
	if !ok {
		t.Fatalf("details.result is missing: %#v", details)
	}
	commit, ok := result["commit"].(map[string]any)
	if !ok || commit["durability"] != "committed" {
		t.Fatalf("the commit fact must survive the auxiliary failure: %#v", result["commit"])
	}
	failure, ok := details["removals_publication_failure"].(map[string]any)
	if !ok {
		t.Fatalf("details has no removals_publication_failure: %#v", details)
	}
	if failure["code"] != "io" || failure["outcome"] != "outcome_unknown" {
		t.Fatalf("auxiliary failure code=%#v outcome=%#v, want io/outcome_unknown",
			failure["code"], failure["outcome"])
	}
	facts, ok := failure["publication"].(map[string]any)
	if !ok {
		t.Fatalf("the auxiliary failure must carry its publication facts: %#v", failure)
	}
	if facts["stage"] != removalsSyncStage || facts["destination_visible"] != true {
		t.Fatalf("facts stage=%#v visible=%#v, want %q/true",
			facts["stage"], facts["destination_visible"], removalsSyncStage)
	}
	if facts["rows"] != "2" {
		t.Fatalf("facts.rows = %#v, want the two dropped addresses", facts["rows"])
	}
	written, err := os.ReadFile(removals)
	if err != nil || len(written) == 0 || written[0] != '{' {
		t.Fatalf("removals output = %q (err %v), want the published rows", written, err)
	}
}

// refreshParams builds one first-seen refresh request, optionally with the
// removals_output member.
func durabilityRefreshParams(target, source string, refreshValue int, removals map[string]any) map[string]any {
	params := map[string]any{
		"path": target,
		"current": map[string]any{
			"feed":   "coverage",
			"source": map[string]any{"path": source, "mode": "live"},
		},
		"refresh_value": refreshValue,
		"metadata":      map[string]any{"mode": "keep"},
		"writer_budget": json.RawMessage(writerBudgetJSON(4)),
	}
	if removals != nil {
		params["removals_output"] = removals
	}
	return params
}

// newFirstSeenTarget creates one empty live IPv4 direct database tagged
// first_seen, the target shape the retention refresh workflows update.
func newFirstSeenTarget(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if _, err := iprangedb.CreateLive(path, iprangedb.AddressFamilyIPv4,
		iprangedb.ValueKindDirect, iprangedb.StructureKindNone,
		iprangedb.ValueTagFirstSeen(), 4, nil); err != nil {
		t.Fatalf("create first_seen target: %v", err)
	}
	return path
}

// policyNameOf names one publication policy exactly as the wire facts do.
func policyNameOf(policy iprangedb.PublicationPolicy) string {
	return fileio.PolicyName(policy)
}

// newLiveCoverageFeed creates one live IPv4 membership database whose feed
// holds the given single-address ranges, the source shape a retention refresh
// reads.
func newLiveCoverageFeed(t *testing.T, dir, name, feed string, addresses ...iprangedb.IPv4) string {
	t.Helper()
	path := filepath.Join(dir, name)
	tag, err := iprangedb.NewValueTag([]byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iprangedb.CreateLive(path, iprangedb.AddressFamilyIPv4,
		iprangedb.ValueKindMembership, iprangedb.StructureKindNone, tag, 4, nil); err != nil {
		t.Fatalf("create coverage source: %v", err)
	}
	writer, err := iprangedb.OpenLiveWriter(path, iprangedb.DefaultBudget(), nil)
	if err != nil {
		t.Fatalf("open coverage source writer: %v", err)
	}
	named, err := iprangedb.NewFeedName(feed)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := writer.BeginCreateFeed(named, nil)
	if err != nil {
		t.Fatalf("create feed %s: %v", feed, err)
	}
	ranges := make([]iprangedb.AddressRange4, 0, len(addresses))
	for _, address := range addresses {
		ranges = append(ranges, iprangedb.AddressRange4{From: address, To: address})
	}
	if err := workflow.AddRangesV4(ranges); err != nil {
		t.Fatalf("add coverage ranges: %v", err)
	}
	finished, err := workflow.FinishInput()
	if err != nil {
		t.Fatalf("finish coverage input: %v", err)
	}
	if finished.IsChanged() {
		if _, err := finished.Commit(); err != nil {
			t.Fatalf("commit coverage: %v", err)
		}
	}
	if _, err := writer.Close(); err != nil {
		t.Fatalf("close coverage writer: %v", err)
	}
	return path
}
