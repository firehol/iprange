package iprangedb

import (
	"os"
	"path/filepath"
	"testing"
)

// ── Issue 1: a truncated spill file must surface via Err(), not look like a
// clean EOF, and Migrate/MigrateFeed must reject it instead of committing the
// partial data read so far. ─────────────────────────────────────────────────────

// TestTruncatedSpillStreamReportsErr verifies the extsort layer: a spill file
// truncated mid-record makes the finished stream report a non-nil Err() after
// Next() returns nil.
func TestTruncatedSpillStreamReportsErr(t *testing.T) {
	dir := t.TempDir()

	// chunk_size=2 over 4 records forces two spill runs, so Finish returns a
	// MergeStream-backed stream (the path that reads spill files lazily).
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 2, TempDir: dir})
	must(t, sorter.Add(wk(0), wk(0), 1))
	must(t, sorter.Add(wk(10), wk(10), 1))
	must(t, sorter.Add(wk(20), wk(20), 1))
	must(t, sorter.Add(wk(30), wk(30), 1))

	// Truncate the first spill file mid-record. Each Ipv4Key spill record is
	// spillRecordSize(4) = 2*4+4+8 = 20 bytes; 2 records = 40 bytes. Keep the
	// 1st record intact + half of the 2nd → truncated mid-record.
	matches, err := filepath.Glob(filepath.Join(dir, "iprange_extsort_*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 2 {
		t.Fatalf("expected >=2 spill files, got %d", len(matches))
	}
	target := matches[0]
	halfRecord := spillRecordSize(4) // 20 bytes
	if err := os.Truncate(target, int64(halfRecord+halfRecord/2)); err != nil {
		t.Fatal(err)
	}

	stream, err := sorter.Finish()
	if err != nil {
		t.Fatalf("Finish should defer the truncation error to Err(), got: %v", err)
	}
	// Drain whatever records are still readable; Next returns nil at the
	// truncated run. That nil MUST NOT be mistaken for a clean EOF.
	for stream.Next() != nil {
	}
	if err := stream.Err(); err == nil {
		t.Fatal("truncated spill must surface a deferred error via Err(), not look like clean EOF")
	}
}

// truncatedStream is a desired stream backed by an in-memory buffer that reports
// a deferred read error once the buffered records are drained — simulating a
// truncated spill file detected lazily during iteration.
type truncatedStream struct {
	records []DesiredRecord[Ipv4Key]
	pos     int
}

func (s *truncatedStream) Peek() *DesiredRecord[Ipv4Key] {
	if s.pos >= len(s.records) {
		return nil
	}
	return &s.records[s.pos]
}
func (s *truncatedStream) Next() *DesiredRecord[Ipv4Key] {
	if s.pos >= len(s.records) {
		return nil
	}
	r := s.records[s.pos]
	s.pos++
	return &r
}
func (s *truncatedStream) Err() error {
	// Once the buffer is drained, simulate that the underlying spill had a
	// truncated tail — there should be more data, but a read failed.
	if s.pos >= len(s.records) {
		return errf("truncated", "spill file partial record")
	}
	return nil
}

// TestMigrateRejectsTruncatedStream verifies Migrate returns an error when the
// desired stream signals a truncation error, instead of committing the partial
// data it managed to read.
func TestMigrateRejectsTruncatedStream(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(wk(5), wk(5), 1))
	must(t, w.Commit(0, ^uint64(0)))

	desired := &truncatedStream{
		records: []DesiredRecord[Ipv4Key]{
			{From: wk(0), To: wk(0), ScopeID: 2},
			{From: wk(10), To: wk(10), ScopeID: 2},
		},
	}
	if _, err := Migrate(w, desired, nil); err == nil {
		t.Fatal("Migrate with a truncated stream must error, not commit partial data")
	}
}

// TestMigrateFeedRejectsTruncatedStream verifies MigrateFeed returns an error on
// a truncated desired stream.
func TestMigrateFeedRejectsTruncatedStream(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(wk(5), wk(5), 1))
	must(t, w.Commit(0, ^uint64(0)))

	desired := &truncatedStream{
		records: []DesiredRecord[Ipv4Key]{
			{From: wk(0), To: wk(0), ScopeID: 2},
			{From: wk(10), To: wk(10), ScopeID: 2},
		},
	}
	if _, err := MigrateFeed(w, 0, desired, nil); err == nil {
		t.Fatal("MigrateFeed with a truncated stream must error, not commit partial data")
	}
}
