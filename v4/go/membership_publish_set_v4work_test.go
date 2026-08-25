//go:build v4work

// Milestone 3 chunk 3b publish_set necessary-work pins (Rust
// membership_query/algebra/output.rs parity): the output sink interns
// each distinct output membership exactly once through the writer
// dictionary, serves every recurring segment from the reader-side
// sequence cache, and applies the whole refcount delta set in one
// batch. The corpus mirrors Rust tests.rs create_membership: two
// alternating single-address feeds over 512 blocks produce 1024 output
// ranges with exactly two recurring memberships. Any hot-path
// regression becomes visible in test builds; production builds compile
// the counters out.

package iprangedb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// publishSetCorpus builds one 512-block alternating two-feed membership
// database through the one-shot output builder (Rust create_membership:
// feed left at 4i, feed right at 4i+1).
func publishSetCorpus(t *testing.T, blocks uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "corpus.iprdb")
	spec, err := writer.FreshOutputSpec(format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, mustTag(t, "feeds").Wire(), 2)
	if err != nil {
		t.Fatal("spec:", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("create fixture:", err)
	}
	builder, err := writer.NewOutputBuilderOverFile(f, spec, writer.OutputBudget{MaxOutputPages: 1 << 16}, writer.ReferenceBatchEntryLimit)
	if err != nil {
		f.Close()
		t.Fatal("builder:", err)
	}
	f.Close()
	if err := builder.PushFeed("left", 0); err != nil {
		t.Fatal("left feed:", err)
	}
	if err := builder.PushFeed("right", 1); err != nil {
		t.Fatal("right feed:", err)
	}
	left, err := builder.InternMembership(writer.OutputWords{1})
	if err != nil {
		t.Fatal("intern left:", err)
	}
	right, err := builder.InternMembership(writer.OutputWords{2})
	if err != nil {
		t.Fatal("intern right:", err)
	}
	for index := uint32(0); index < blocks; index++ {
		address := index*4 + 0
		if err := builder.PushInternedMembershipV4(address, address, left); err != nil {
			t.Fatal("left row:", err)
		}
		if err := builder.PushInternedMembershipV4(address+1, address+1, right); err != nil {
			t.Fatal("right row:", err)
		}
	}
	if err := builder.Finish(); err != nil {
		t.Fatal("finish:", err)
	}
	if err := builder.Close(); err != nil {
		t.Fatal("close builder:", err)
	}
	return path
}

// TestPublishSetWorkCounters pins the Rust intern-pinning shape over a
// 1024-segment publish: two dictionary interns, 1022 sequence-cache
// hits, one refcount batch, and exactly 1024 output ranges.
func TestPublishSetWorkCounters(t *testing.T) {
	requirePublicationSecurity(t)
	corpus := publishSetCorpus(t, 512)
	db, err := OpenImmutable(corpus)
	if err != nil {
		t.Fatal("open corpus:", err)
	}
	defer mustClose(t, db)
	query, err := db.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scope, err := query.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope:", err)
	}
	algebra, err := NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if err != nil {
		t.Fatal("algebra:", err)
	}
	destination := publishDest(t, "recurring-output.iprdb")
	work.Reset()
	result, err := algebra.PublishSet(destination, mustTag(t, "result"), AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget(), nil)
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Report.OutputRangeCount != 1024 {
		t.Fatalf("output range count = %d, want 1024", result.Report.OutputRangeCount)
	}
	s := work.Read()
	if s.MembershipInterns != 2 {
		t.Fatalf("membership interns = %d, want 2", s.MembershipInterns)
	}
	if s.MembershipInternCacheHits != 1022 {
		t.Fatalf("membership intern cache hits = %d, want 1022", s.MembershipInternCacheHits)
	}
	if s.MembershipRefcountBatches != 1 {
		t.Fatalf("membership refcount batches = %d, want 1", s.MembershipRefcountBatches)
	}
	// The published output reprocesses cleanly through the reader.
	output, err := OpenImmutable(destination)
	if err != nil {
		t.Fatal("open output:", err)
	}
	defer mustClose(t, output)
	if info, err := output.Info(); err != nil || info.Family != AddressFamilyIPv4 || info.ValueKind != ValueKindMembership {
		t.Fatalf("output identity %+v/%v, want ipv4 membership", info, err)
	}
}
