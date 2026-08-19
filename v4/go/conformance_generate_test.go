package iprangedb

// Go-produced conformance fixture generation (SOW-0025 chunk-6 design
// record D5): the two direct fixtures the Go writer can currently produce
// use the exact op sequence of the Rust generators, with producer-tagged
// metadata, so both readers cross-open them. Membership/structured
// fixtures are blocked on their edit cores (later chunks) and stay
// Rust-only.
//
// The test is env-gated (IPRANGE_V4_GO_REGENERATE_FIXTURES=1), mirroring
// the Rust #[ignore] regeneration entry point: a normal suite run never
// writes into the committed corpus.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// goFixturePath resolves one Go-produced fixture path under the shared
// conformance corpus.
func goFixturePath(name string) string {
	return filepath.Join("..", "conformance", "go", name)
}

// copyFile is the test-only publish helper: the staged file may live on a
// different device than the corpus, so publication copies next to the
// target and renames over it (Rust generate.rs publish parity).
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// regenDirectIPv4 writes direct-ipv4.iprdb into dir with the exact op
// sequence of the Rust direct_ipv4 generator (generate.rs:175): the four
// assigns and one clear coalesce into the same four canonical ranges, and
// the metadata is producer-tagged so the two producers stay
// distinguishable.
func regenDirectIPv4(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "direct-ipv4.iprdb")
	created, err := Create(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag)
	if err != nil {
		t.Fatal(err)
	}
	if created.TransactionID != 1 {
		t.Fatalf("fresh fixture txn = %d, want 1", created.TransactionID)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	ops := []struct {
		kind  string
		from  IPv4
		to    IPv4
		value uint32
	}{
		{"assign", IPv4(0x0a000014), IPv4(0x0a00001d), 1}, // 10.0.0.20-10.0.0.29
		{"assign", IPv4(0x0a00000a), IPv4(0x0a000019), 2}, // 10.0.0.10-10.0.0.25
		{"assign", IPv4(0x0a00000f), IPv4(0x0a000011), 3}, // 10.0.0.15-10.0.0.17
		{"clear", IPv4(0x0a000016), IPv4(0x0a00001b), 0},  // 10.0.0.22-10.0.0.27
		{"assign", IPv4(0x0a00001e), IPv4(0x0a00001f), 1}, // 10.0.0.30-10.0.0.31
	}
	for _, op := range ops {
		switch op.kind {
		case "assign":
			if changed, err := tx.AssignV4(op.from, op.to, op.value); err != nil || !changed {
				t.Fatalf("assign %v-%v = changed %v err %v", op.from, op.to, changed, err)
			}
		case "clear":
			if changed, err := tx.ClearV4(op.from, op.to); err != nil || !changed {
				t.Fatalf("clear %v-%v = changed %v err %v", op.from, op.to, changed, err)
			}
		}
	}
	if changed, err := tx.SetMetadataJSON([]byte(`{"fixture":"go-direct-ipv4","producer":"go"}`)); err != nil || !changed {
		t.Fatalf("metadata set = changed %v err %v", changed, err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("fixture commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// regenFirstSeenIPv6 writes first-seen-ipv6.iprdb into dir: the whole
// IPv6 space assigned 1_700_000_000 with present-empty metadata, mirroring
// the Rust first_seen_ipv6 generator via the first-seen refresh workflow
// (generate.rs:199).
func regenFirstSeenIPv6(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("first_seen"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "first-seen-ipv6.iprdb")
	if _, err := Create(path, AddressFamilyIPv6, ValueKindDirect, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV6(IPv6{}, IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}, 1_700_000_000); err != nil || !changed {
		t.Fatalf("whole-space v6 assign = changed %v err %v", changed, err)
	}
	if changed, err := tx.SetMetadataJSON([]byte{}); err != nil || !changed {
		t.Fatalf("empty metadata set = changed %v err %v", changed, err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("fixture commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRegenerateGoFixtures regenerates both Go-produced fixtures with the
// Rust two-phase contract: generate both files into a staging corpus,
// verify the staging corpus with the exact same conformance suite in a
// subprocess, and only then publish each file next to its committed
// target and rename over it. A failed verification leaves the committed
// corpus untouched. Set IPRANGE_V4_GO_REGENERATE_FIXTURES=1 to run.
func TestRegenerateGoFixtures(t *testing.T) {
	if os.Getenv("IPRANGE_V4_GO_REGENERATE_FIXTURES") == "" {
		t.Skip("fixture regeneration is explicit: set IPRANGE_V4_GO_REGENERATE_FIXTURES=1")
	}
	corpus := filepath.Join("..", "conformance")
	staging := t.TempDir()

	// Stage: a full corpus copy with only the go/ fixtures replaced
	// (the inventory check in loadManifest requires the other six
	// files to be present and listed).
	for _, name := range []string{"cases.json"} {
		data, err := os.ReadFile(filepath.Join(corpus, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(staging, "rust"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"rust/direct-ipv4.iprdb", "rust/first-seen-ipv6.iprdb",
		"rust/membership-ipv4.iprdb", "rust/membership-ipv6.iprdb",
		"rust/structured-ipv4.iprdb", "rust/structured-ipv4-nothreat.iprdb",
	} {
		if err := copyFile(filepath.Join(corpus, name), filepath.Join(staging, name)); err != nil {
			t.Fatal(err)
		}
	}
	goDir := filepath.Join(staging, "go")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regenDirectIPv4(t, goDir)
	regenFirstSeenIPv6(t, goDir)

	// Verify: the staged corpus must pass the full conformance suite
	// (same binary, same test, corpus root redirected by env).
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestConformanceRustFixtures$")
	cmd.Env = append(os.Environ(), "IPRANGE_V4_GO_CORPUS="+staging)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("staged corpus verification timed out")
		}
		t.Fatalf("staged corpus verification failed: %v (committed corpus untouched)", err)
	}

	// Publish: copy next to the target, then rename over it. The target
	// file is replaced atomically only after verification passed.
	publish := func(name string) {
		t.Helper()
		stage := filepath.Join(goDir, name)
		target := filepath.Join(corpus, "go", name)
		replacement := target + ".replacement"
		if err := copyFile(stage, replacement); err != nil {
			t.Fatal(err)
		}
		// Windows rename refuses an existing destination; the committed
		// target must be removed first (Rust generate.rs publish parity,
		// which gates the same removal on cfg(windows)). On POSIX the
		// rename stays the atomic replace.
		if runtime.GOOS == "windows" {
			if _, err := os.Stat(target); err == nil {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := os.Rename(replacement, target); err != nil {
			t.Fatal(err)
		}
	}
	publish("direct-ipv4.iprdb")
	publish("first-seen-ipv6.iprdb")
}
