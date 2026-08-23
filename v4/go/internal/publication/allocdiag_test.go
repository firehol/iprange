//go:build linux && !race

// Allocation diagnostic for the publication machines (run with
// go test -v -run TestAttemptAllocDiagnostic). It logs the exact
// one-run success-path allocation count of fromPrivate (the Rust
// count_thread_allocations pin shape) and the per-primitive boundary
// costs used to justify the pinned budget in attempt_alloc_test.go.
// The helpers build the finished-output fixture on a testing.TB so
// benchmarks can measure the same paths.

package publication

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

func TestAttemptAllocDiagnostic(t *testing.T) {
	dir := t.TempDir()
	attempt, file := testSecuredAttemptBench(t, dir, "result.v4")
	finished := finishOutputFixture(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatal(failure)
	}
	defer prepared.Close()
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatal(initFailure)
	}
	seed := captureSeed(prepared)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, publishFailure := fromPrivate(seed, prepared, *private, nil, func(attemptPoint) error { return nil }, false, noopAttemptObserver)
	runtime.ReadMemStats(&after)
	if publishFailure != nil || result.Publication != PublicationPublished {
		t.Fatalf("fromPrivate: %v %v", result.Publication, publishFailure)
	}
	t.Logf("fromPrivate success path allocations: %d (pinned budget 58)", after.Mallocs-before.Mallocs)
}

func testSecuredAttemptBench(t testing.TB, dir, mainName string) (outputAttempt, *os.File) {
	t.Helper()
	created, err := createOutput(filepath.Join(dir, mainName))
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	secured, failure := created.secure()
	if failure != nil {
		t.Fatalf("secure output: %v", failure)
	}
	attempt, file := secured.intoParts()
	return attempt, file
}

// finishOutputFixture writes the two meta pages of the empty direct
// fixture into one secured output file and maps them (the
// testFinishedOutput body with a testing.TB receiver).
func finishOutputFixture(t testing.TB, file *os.File) FinishedOutput {
	t.Helper()
	page0, page1, _ := testFinishedPages()
	if _, err := file.WriteAt(page0, 0); err != nil {
		t.Fatalf("write meta page 0: %v", err)
	}
	if _, err := file.WriteAt(page1, format.PageSize); err != nil {
		t.Fatalf("write meta page 1: %v", err)
	}
	mapped, err := mapping.MapFile(file, 2*format.PageSize, false)
	if err != nil {
		t.Fatalf("map finished output: %v", err)
	}
	meta, ok := format.ParseIdentity(page0)
	if !ok {
		t.Fatal("test meta page does not parse")
	}
	return FinishedOutput{File: file, Mapping: mapped, Meta: meta}
}
