//go:build v4work

// Necessary-work pins for the structured transaction (SOW-0025 slice C),
// mirroring Rust structured_value/manager.rs accounting: every intern
// attempt counts exactly once (created, deduplicated, or
// membership-linked), assign and clear add no structure intern work,
// and the feed-deletion transform re-interns each stored payload once.

package iprangedb

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestWorkStructuredInternPins pins the structure_intern counter across
// the structured transaction lifecycle: intern attempts, dedup hits,
// assign/clear silence, and the delete-feed re-intern transform.
func TestWorkStructuredInternPins(t *testing.T) {
	requireFileCreation(t)
	path := structuredDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx, err := w.BeginStructuredTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	threat, err := tx.EnsureFeed(feedName(t, "threat-a"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := tx.AddFeed(empty, threat)
	if err != nil {
		t.Fatal(err)
	}

	work.Reset()
	first, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64512), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if snap := work.Read(); snap.StructureInterns != 1 {
		t.Fatalf("first intern = %d, want 1", snap.StructureInterns)
	}
	// A deduplicated equal payload still costs one intern attempt (Rust
	// work::structure_intern fires at the top of intern()).
	duplicate, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64512), MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if first != duplicate {
		t.Fatal("deduplicated references differ")
	}
	if snap := work.Read(); snap.StructureInterns != 2 {
		t.Fatalf("duplicate intern = %d, want 2", snap.StructureInterns)
	}
	// A membership-linked payload is one more intern attempt (the
	// membership intern machinery itself is pinned by slice B).
	linked, err := tx.InternNetworkEnrichmentV1(enrichmentValue(64513), membership)
	if err != nil {
		t.Fatal(err)
	}
	if snap := work.Read(); snap.StructureInterns != 3 {
		t.Fatalf("membership-linked intern = %d, want 3", snap.StructureInterns)
	}

	// Assign and clear add no structure intern work.
	if _, err := tx.AssignV4(IPv4(0), IPv4(100), linked); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(0), IPv4(9), first); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ClearV4(IPv4(20), IPv4(30)); err != nil {
		t.Fatal(err)
	}
	if snap := work.Read(); snap.StructureInterns != 3 {
		t.Fatalf("assign/clear intern work = %d, want 3", snap.StructureInterns)
	}

	// Deleting the feed re-interns every stored payload that carries
	// its bit: the linked segments [0,19] and [31,100] both drop the
	// feed and re-intern the payload (the second re-intern dedups, but
	// each attempt counts; the unlinked [0,9] structure is untouched).
	work.Reset()
	if err := tx.DeleteFeed(threat); err != nil {
		t.Fatal(err)
	}
	if snap := work.Read(); snap.StructureInterns != 2 {
		t.Fatalf("delete-feed re-interns = %d, want 2", snap.StructureInterns)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
}
