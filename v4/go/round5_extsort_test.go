package iprangedb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRound5ExternalSorterRejectsZeroChunkSize(t *testing.T) {
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 0, TempDir: t.TempDir()})
	if err := sorter.Add(1, 1, 1); err == nil {
		t.Fatal("NewExtSorter silently replaced an explicitly invalid chunk size")
	}
	if _, err := ExtSort([]DesiredRecord[Ipv4Key]{{From: 1, To: 1, ScopeID: 1}}, &ExtSortConfig{ChunkSize: 0, TempDir: t.TempDir()}); err == nil {
		t.Fatal("ExtSort silently replaced an explicitly invalid chunk size")
	}
}

func TestRound5ExternalSorterSpillFailureIsTerminal(t *testing.T) {
	parent := t.TempDir()
	tempDir := filepath.Join(parent, "missing")
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: tempDir})
	if err := sorter.Add(10, 10, 1); err == nil {
		t.Fatal("spill unexpectedly succeeded into a missing directory")
	}
	if err := os.Mkdir(tempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sorter.Add(20, 20, 2); err == nil {
		t.Fatal("sorter accepted Add after a spill I/O failure")
	}
	if _, err := sorter.Finish(); err == nil {
		t.Fatal("sorter accepted Finish after a spill I/O failure")
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("terminal spill failure left files behind: %v", entries)
	}
}

func TestRound5ExternalSorterMultiPassFailureCleansEveryOwnedRun(t *testing.T) {
	dir := t.TempDir()
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: dir})
	for i := uint32(0); i < extSortMergeFan+8; i++ {
		if err := sorter.Add(Ipv4Key(i*2), Ipv4Key(i*2), i+1); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	if len(sorter.runPaths) <= extSortMergeFan {
		t.Fatalf("fixture has %d runs, want a multi-pass merge", len(sorter.runPaths))
	}
	if err := os.Truncate(sorter.runPaths[0], int64(spillRecordSize(4)-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := sorter.Finish(); err == nil {
		t.Fatal("Finish accepted a truncated run during multi-pass merge")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed multi-pass merge leaked %d owned spill files: %v", len(entries), entries)
	}
}
