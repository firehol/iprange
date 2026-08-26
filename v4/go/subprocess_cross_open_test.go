package iprangedb

// Mixed subprocess cross-open gate (SOW-0025 chunk-6 design record D6):
// a spawned child of this test binary opens the Rust-produced fixture,
// opens the Go-produced fixture, and runs one create-commit-read-back
// roundtrip through the real writer/reader path. A child that dies, hangs,
// or reports an error fails the parent test; the child inherits nothing of
// the circuit-control environment.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	subprocessChildTest = "^TestGoSubprocessChild$"
	subprocessSpawned   = "IPRANGE_V4_GO_SUBPROCESS"
	subprocessTimeout   = 90 * time.Second
)

// runGoSubprocess spawns this test binary as the cross-open child and
// requires a clean exit (Rust mixed_subprocess.rs parent parity).
func runGoSubprocess(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+subprocessChildTest)
	// Strip inherited control variables so a stray environment cannot
	// redirect the child (the chunk-5 crash pattern).
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		// Strip both the chunk-6 cross-open controls and the chunk-5
		// crash-test controls so a stray ambient variable cannot
		// redirect the child (the chunk-5 crash pattern).
		if strings.HasPrefix(kv, "IPRANGE_V4_GO_") || strings.HasPrefix(kv, "IPRANGE_V4_TEST_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, subprocessSpawned+"=1")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("go cross-open child timed out after %s", subprocessTimeout)
		}
		t.Fatalf("go cross-open child failed: %v", err)
	}
}

// TestGoSubprocessCrossOpen is the parent gate: the child proves the real
// opened paths in a separate process whose exit code is the verdict.
func TestGoSubprocessCrossOpen(t *testing.T) {
	requireLiveCreation(t)
	runGoSubprocess(t)
}

// TestGoSubprocessChild is the subprocess entry point; it only runs when
// the parent set the spawn marker (a normal suite run skips it).
func TestGoSubprocessChild(t *testing.T) {
	requireLiveCreation(t)
	if os.Getenv(subprocessSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	// Self-deadline so a hang cannot linger after the parent died.
	time.AfterFunc(subprocessTimeout, func() { os.Exit(1) })

	// 1. The Rust-produced fixture cross-opens in the Go reader.
	rustPath, err := filepath.Abs(filepath.Join("..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(rustPath)
	if err != nil {
		t.Fatalf("go child: rust fixture open: %v", err)
	}
	if v, ok, err := r.LookupDirectV4(IPv4(0x0a000010)); err != nil || !ok || v != 3 {
		r.Close()
		t.Fatalf("go child: rust fixture lookup = (%d, %v, %v), want (3, true, nil)", v, ok, err)
	}
	r.Close()

	// 2. The Go-produced fixture cross-opens (self-conformance).
	goPath, err := filepath.Abs(filepath.Join("..", "conformance", "go", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	r, err = OpenImmutable(goPath)
	if err != nil {
		t.Fatalf("go child: go fixture open: %v", err)
	}
	if v, ok, err := r.LookupDirectV4(IPv4(0x0a000010)); err != nil || !ok || v != 3 {
		r.Close()
		t.Fatalf("go child: go fixture lookup = (%d, %v, %v), want (3, true, nil)", v, ok, err)
	}
	r.Close()

	// 2b. The Go-produced history projection destination opens with the
	// three last-seen feeds and the full 1000-point range tree (the
	// conformance suite verifies every range; this smoke verdict is
	// catalog + record count).
	historyPath, err := filepath.Abs(filepath.Join("..", "conformance", "go", "history-membership-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := OpenImmutable(historyPath)
	if err != nil {
		t.Fatalf("go child: go history fixture open: %v", err)
	}
	for name, wantIndex := range map[string]uint32{"one": 0, "two": 1, "three": 2} {
		entry, found, err := h.LookupFeed(name)
		if err != nil || !found || entry.Index != wantIndex {
			t.Fatalf("go child: history feed %q = %+v found %v err %v, want index %d", name, entry, found, err, wantIndex)
		}
	}
	historyInfo, err := h.Info()
	if err != nil {
		t.Fatalf("go child: history info: %v", err)
	}
	if historyInfo.RangeRecordCount != 1000 || historyInfo.ActiveFeedCount != 3 {
		t.Fatalf("go child: history info = %+v, want 1000 ranges and 3 feeds", historyInfo)
	}
	h.Close()

	// 3. One full create -> write -> commit -> read-back roundtrip.
	path := filepath.Join(t.TempDir(), "child-roundtrip.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}, 4, nil); err != nil {
		t.Fatalf("go child: create: %v", err)
	}
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatalf("go child: open writer: %v", err)
	}
	tx, err := w.BeginDirect(nil)
	if err != nil {
		t.Fatalf("go child: begin: %v", err)
	}
	if _, err := tx.AssignV4(IPv4(100), IPv4(200), 77); err != nil {
		t.Fatalf("go child: assign: %v", err)
	}
	if _, err := tx.SetMetadataJSON([]byte(`{"child":"go"}`)); err != nil {
		t.Fatalf("go child: metadata: %v", err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("go child: commit = %+v err %v", res, err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("go child: close writer: %v", err)
	}
	liveReadback, err := OpenLiveReader(path, nil)
	if err != nil {
		t.Fatalf("go child: readback open: %v", err)
	}
	defer liveReadback.Close()
	if v, ok, err := liveReadback.LookupDirectV4(IPv4(150)); err != nil || !ok || v != 77 {
		t.Fatalf("go child: readback lookup = (%d, %v, %v), want (77, true, nil)", v, ok, err)
	}
	meta, present, err := liveReadback.MetadataJSON()
	if err != nil || !present || string(meta) != `{"child":"go"}` {
		t.Fatalf("go child: readback metadata = %q present %v err %v", meta, present, err)
	}
}
