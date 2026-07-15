//go:build unix

package iprangedb

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func round6ScopeOnlyImage(t *testing.T, scopes int) []byte {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < scopes; i++ {
		bitmap := []byte{byte(i), byte(i >> 8), byte(i >> 16), 0xa5}
		if _, err := w.ScopeIntern(bitmap); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed scope image")
	}
	return image
}

func round6ScopeCommitAllocatedBytes(t *testing.T, scopes int) uint64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scopes.iprdb")
	if err := os.WriteFile(path, round6ScopeOnlyImage(t, scopes), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeIntern([]byte{0xff, 0xff, 0xff, 0xff, 0x01}); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := w.Commit(2); err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return after.TotalAlloc - before.TotalAlloc
}

func TestRound6AddingOneScopeCommitHeapDoesNotScaleWithCommittedScopeCount(t *testing.T) {
	small := round6ScopeCommitAllocatedBytes(t, 100)
	large := round6ScopeCommitAllocatedBytes(t, 20000)
	const tolerance = 256 << 10
	if large > small+tolerance {
		t.Fatalf("committing one new scope materialized all committed scopes: small=%d large=%d", small, large)
	}
}
