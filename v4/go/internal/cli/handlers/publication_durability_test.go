package handlers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// Adapter-owned outputs (metadata delivery, first-seen removals) are
// published through a private temporary and one atomic namespace step.
// The two sides of that step have different facts available: before the
// destination name exists, the operation is definitely not delivered, so
// the refusal is definite; after it exists, the bytes are delivered and
// only the durability of the namespace entry is unproven, so the answer
// MUST be the io class with outcome_unknown and the publication facts
// (stage, digest, counts, visibility, temporary state). Reporting a
// post-visibility failure as a definite refusal tells a caller it may
// delete or ignore a file that in fact holds the complete output, and
// removing that file would destroy delivered content
// (binary-format-v4.md, "Publication attempt outcomes and durability").
//
// The stages that an already-existing destination name or an injected error
// can trigger are platform-neutral and stay here. The stages that need a
// POSIX mode bit to fail one step on purpose live in unix-tagged sibling
// files: the unresolved directory sync of a destination whose parent is
// writable and searchable but not readable (mode 0311,
// publication_durability_unix_test.go) and the refusal of a destination whose
// parent is searchable but not writable (mode 0500,
// publication_durability_parent_unwritable_unix_test.go). Go's os.Chmod on
// Windows maps only the read-only attribute, so neither mode can express the
// denial there; see the two unix-tagged files for the Windows mechanism each
// one is waiting on.

// publicationFacts returns the details.publication member of one handler
// error, failing the test when it is absent.
func publicationFacts(t *testing.T, details any) map[string]any {
	t.Helper()
	members, ok := details.(map[string]any)
	if !ok {
		t.Fatalf("details is %T, want an object", details)
	}
	facts, ok := members["publication"].(map[string]any)
	if !ok {
		t.Fatalf("details has no publication member: %#v", members)
	}
	return facts
}

// requireNoPublicationFacts asserts that a definite refusal carries no
// publication facts: those members describe an unresolved publication.
func requireNoPublicationFacts(t *testing.T, details any) {
	t.Helper()
	if details == nil {
		return
	}
	members, ok := details.(map[string]any)
	if !ok {
		t.Fatalf("details is %T, want an object", details)
	}
	if _, present := members["publication"]; present {
		t.Fatalf("definite refusal carries publication facts: %#v", members)
	}
}

// directoryEntries lists the names in one directory, used to prove that no
// private temporary survived a refusal.
func directoryEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// metadataSHA256 is the digest the metadata publication facts must report.
func metadataSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return HexBytes(sum[:])
}

// A refusal before the destination name appears is definite: the outcome is
// not the unknown one, no publication facts accompany it, the previous file
// is intact, and no private temporary is left behind. This trigger is
// fail_if_exists against an existing destination, so the atomic publication
// step itself refuses. The other trigger of the same class, an unwritable
// parent that never lets the private name be created, needs POSIX mode bits
// and lives in publication_durability_parent_unwritable_unix_test.go.
func TestMetadataPublicationBeforeVisibilityIsDefiniteRefusal(t *testing.T) {
	t.Run("publish_refuses_existing_name", func(t *testing.T) {
		dir := t.TempDir()
		destination := filepath.Join(dir, "metadata.json")
		if err := os.WriteFile(destination, []byte("previous\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, herr := MetadataOutput(destination, []byte("payload"),
			iprangedb.PolicyFailIfExists, 1<<20, 8)
		if herr == nil {
			t.Fatal("fail_if_exists accepted an existing destination")
		}
		if herr.Code != "name_exists" || herr.Outcome != "read_only_failure" {
			t.Fatalf("code=%q outcome=%q, want name_exists/read_only_failure",
				herr.Code, herr.Outcome)
		}
		requireNoPublicationFacts(t, herr.Details)
		if got, err := os.ReadFile(destination); err != nil || string(got) != "previous\n" {
			t.Fatalf("previous destination = %q (err %v), want its original bytes", got, err)
		}
		if names := directoryEntries(t, dir); len(names) != 1 || names[0] != "metadata.json" {
			t.Fatalf("directory holds %v, want only metadata.json", names)
		}
	})
}

// The cleanup stage — removing the private name after the destination was
// published — can only report unproven durability, never a delivery failure.
// This is the same factual model as the export writer's publicationFailure
// (fileio/export_writer.go), so the class and every fact member are pinned.
func TestMetadataPublicationCleanupStageReportsUnknownOutcome(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "metadata.json")
	payload := []byte("payload")
	herr := metadataPublicationFailure(errors.New("permission denied"),
		"remove metadata temporary", destination, payload,
		iprangedb.PolicyFailIfExists, metadataSHA256(payload), false)
	if herr.Code != "io" || herr.Outcome != "outcome_unknown" {
		t.Fatalf("code=%q outcome=%q, want io/outcome_unknown", herr.Code, herr.Outcome)
	}
	facts := publicationFacts(t, herr.Details)
	want := map[string]any{
		"outcome":             "outcome_unknown",
		"publication_policy":  "fail_if_exists",
		"path":                destination,
		"stage":               "remove metadata temporary",
		"destination_visible": true,
		"temporary_removed":   false,
		"bytes":               fmt.Sprintf("%d", len(payload)),
		"rows":                "1",
		"sha256":              metadataSHA256(payload),
	}
	for name, expected := range want {
		if got, ok := facts[name]; !ok || got != expected {
			t.Fatalf("facts[%s] = %#v (present %v), want %#v", name, got, ok, expected)
		}
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reporting touched the destination: %v", err)
	}
}

// The removals collector is the other adapter-owned output: its publication
// failure must carry the same class and facts.
func TestRemovalsPublicationCleanupStageReportsUnknownOutcome(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "removals.jsonl")
	collector := newTestRemovalCollector(t, dir, "removals.jsonl",
		iprangedb.PolicyReplaceExisting)
	const row = `{"feed":"coverage"}`
	if err := collector.writeLine(row); err != nil {
		t.Fatal(err)
	}
	herr := collector.publicationFailure(errors.New("permission denied"),
		"remove removal temporary", "created", false)
	if herr.Code != "io" || herr.Outcome != "outcome_unknown" {
		t.Fatalf("code=%q outcome=%q, want io/outcome_unknown", herr.Code, herr.Outcome)
	}
	facts := publicationFacts(t, herr.Details)
	want := map[string]any{
		"outcome":             "outcome_unknown",
		"publication_policy":  "replace_existing",
		"path":                destination,
		"stage":               "remove removal temporary",
		"destination_visible": true,
		"destination_content": "created",
		"temporary_removed":   false,
		"rows":                "1",
		"bytes":               fmt.Sprintf("%d", len(row)+1),
	}
	for name, expected := range want {
		if got, ok := facts[name]; !ok || got != expected {
			t.Fatalf("facts[%s] = %#v (present %v), want %#v", name, got, ok, expected)
		}
	}
	if digest, ok := facts["sha256"].(string); !ok || len(digest) != 64 {
		t.Fatalf("facts.sha256 = %#v, want a 64-hex digest", facts["sha256"])
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reporting touched the destination: %v", err)
	}
}

// A removals refusal before the destination appears is definite too: the
// previous file is intact, no publication facts exist, and the private
// temporary is gone.
func TestRemovalsPublicationBeforeVisibilityIsDefiniteRefusal(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "removals.jsonl")
	if err := os.WriteFile(destination, []byte("previous\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := newTestRemovalCollector(t, dir, "removals.jsonl", iprangedb.PolicyFailIfExists)
	if err := collector.writeLine(`{"feed":"coverage"}`); err != nil {
		t.Fatal(err)
	}
	_, herr := collector.publish()
	if herr == nil {
		t.Fatal("fail_if_exists accepted an existing destination")
	}
	if herr.Code != "name_exists" || herr.Outcome != "not_started" {
		t.Fatalf("code=%q outcome=%q, want name_exists/not_started", herr.Code, herr.Outcome)
	}
	requireNoPublicationFacts(t, herr.Details)
	if got, err := os.ReadFile(destination); err != nil || string(got) != "previous\n" {
		t.Fatalf("previous destination = %q (err %v), want its original bytes", got, err)
	}
	if names := directoryEntries(t, dir); len(names) != 1 || names[0] != "removals.jsonl" {
		t.Fatalf("directory holds %v, want only removals.jsonl", names)
	}
}

// newTestRemovalCollector creates one collector with one row's worth of
// budget, registered for cleanup.
func newTestRemovalCollector(t *testing.T, dir, name string, policy iprangedb.PublicationPolicy) *removalCollector {
	t.Helper()
	settings := removalsSettings{
		destination:    filepath.Join(dir, name),
		policy:         policy,
		maxRows:        10,
		maxOutputBytes: 4096,
		maxOpenFiles:   1,
	}
	collector, herr := newRemovalCollector(settings, 84)
	if herr != nil {
		t.Fatalf("newRemovalCollector: code=%q outcome=%q message=%q",
			herr.Code, herr.Outcome, herr.Message)
	}
	t.Cleanup(func() {
		_ = collector.file.Close()
		_ = os.Remove(collector.temporary)
	})
	return collector
}
