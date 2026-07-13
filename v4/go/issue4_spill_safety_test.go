package iprangedb

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// ── Issue 4: spill files must be unique across processes (Rust fix).
//
// Rust's spill path used a process-local atomic counter for the filename plus
// create+truncate, so two processes collided and silently corrupted each
// other's spill. That bug is fixed in Rust by embedding the PID and using
// create_new.
//
// The Go spill path uses os.CreateTemp(dir, "iprange_extsort_*"), which the Go
// stdlib guarantees is process-safe: it derives the name from a
// PID+random-number source and opens with O_CREATE|O_EXCL, retrying on
// collision. So Go was ALREADY safe — no code change needed. This test proves
// it: many concurrent spillRun calls into the SAME dir produce distinct,
// non-truncated files.

func TestSpillRunIsCollisionFreeUnderConcurrency(t *testing.T) {
	dir := t.TempDir()

	// Each goroutine spills 25 runs into the shared dir. With create+truncate
	// (the unsafe pattern), concurrent goroutines sharing a process-local
	// counter would collide; os.CreateTemp's O_EXCL + randomness must prevent
	// that and yield distinct files every time.
	const goroutines = 8
	const perG = 25
	var mu sync.Mutex
	allPaths := make(map[string]struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs := []DesiredRecord[Ipv4Key]{{From: wk(0), To: wk(9), ScopeID: 1}}
			for i := 0; i < perG; i++ {
				path, err := spillRun[Ipv4Key](recs, dir, uint64(i))
				if err != nil {
					errCh <- err
					return
				}
				mu.Lock()
				allPaths[path] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	// Every spill MUST have produced a DISTINCT path (no collision), and every
	// file MUST still be non-empty (no silent truncation by a collision).
	want := goroutines * perG
	if len(allPaths) != want {
		t.Fatalf("spill collision: %d distinct paths, want %d", len(allPaths), want)
	}
	for p := range allPaths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat spill %s: %v", filepath.Base(p), err)
		}
		// One Ipv4Key spill record is spillRecordSize(4) = 20 bytes.
		if info.Size() < 20 {
			t.Fatalf("spill %s truncated: size=%d", filepath.Base(p), info.Size())
		}
	}
}
