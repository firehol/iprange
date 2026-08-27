//go:build linux && amd64

package iprangedb

// Cross-language live cooperation (SOW-0027 milestone 3 slice 3b): the
// Go parent spawns the Rust test binary as a child that opens the same
// live database with the Rust SDK (read, writer exclusion, pinned-read
// reclamation), and TestMixedLiveGoChild is the child entry the Rust
// parent spawns for the mirrored direction. Both parents are env-gated
// (IPRANGE_V4_MIXED_LIVE=1, documented in the conformance README) so
// plain suites stay fast; the battery is linux/amd64-only (recorded in
// SOW-0027).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	mixedLiveEnv       = "IPRANGE_V4_MIXED_LIVE"
	mixedGoChildMarker = "IPRANGE_V4_GO_MIXED_CHILD"
	mixedLiveDBEnv     = "IPRANGE_V4_MIXED_LIVE_DB"
	mixedLiveModeEnv   = "IPRANGE_V4_MIXED_LIVE_MODE"
	mixedChildTimeout  = 90 * time.Second
	mixedRustChildTest = "mixed_live_rust_child"
)

// rustMixedLiveBinary builds the Rust mixed_live test binary with cargo
// (incremental; the build cache keeps repeat runs cheap) and returns its
// executable path, or ok=false when the toolchain is unavailable.
func rustMixedLiveBinary(t *testing.T) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Log("mixed_live: cargo toolchain not found; skipping the Rust-child direction")
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), mixedChildTimeout*2)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nice", "cargo", "test",
		"--manifest-path", filepath.Join("..", "rust", "Cargo.toml"),
		"--test", "mixed_live", "--no-run", "--message-format=json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo test --no-run mixed_live failed: %v\n%s", err, out)
	}
	var binary string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		var msg struct {
			Reason string `json:"reason"`
			Target struct {
				Name string `json:"name"`
			} `json:"target"`
			Executable string `json:"executable"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Reason == "compiler-artifact" && msg.Target.Name == "mixed_live" && msg.Executable != "" {
			binary = msg.Executable
		}
	}
	if binary == "" {
		t.Fatal("cargo did not report the mixed_live test executable")
	}
	return binary, true
}

// runRustChild spawns one Rust child entry with the mode environment and
// returns the command (the caller waits or streams READY).
func runRustChild(binary, dbPath, mode string) *exec.Cmd {
	cmd := exec.Command(binary, "--ignored", "--exact", mixedRustChildTest, "--test-threads=1", "--nocapture", "--quiet")
	cmd.Env = append(os.Environ(), mixedLiveDBEnv+"="+dbPath, mixedLiveModeEnv+"="+mode)
	return cmd
}

// finishChild closes the child stdin (releasing a pinned child), waits
// for a clean exit, and fails the parent otherwise.
func finishChild(t *testing.T, cmd *exec.Cmd, stdin io.WriteCloser, label string) {
	t.Helper()
	if stdin != nil {
		if err := stdin.Close(); err != nil {
			t.Fatalf("%s: close child stdin: %v", label, err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: rust child failed: %v", label, err)
		}
	case <-time.After(mixedChildTimeout):
		_ = cmd.Process.Kill()
		t.Fatalf("%s: rust child timed out", label)
	}
}

// TestMixedLiveRustChild is the Go parent of the Rust child battery:
// generation read-back, writer exclusion with a held Go writer, and
// pinned-read reclamation across languages.
func TestMixedLiveRustChild(t *testing.T) {
	if os.Getenv(mixedLiveEnv) != "1" {
		t.Skip("mixed_live is explicit: set IPRANGE_V4_MIXED_LIVE=1")
	}
	binary, ok := rustMixedLiveBinary(t)
	if !ok {
		t.Skip("rust toolchain missing")
	}
	t.Run("reader", func(t *testing.T) {
		main := liveGenPath(t, "go-parent-reader")
		createDirectLive(t, main)
		w := openWriter(t, main)
		commitDirect(t, w, 10, 30, 1) // generation 2
		commitDirect(t, w, 12, 18, 2) // generation 3
		closeWriter(t, w)
		cmd := runRustChild(binary, main, "reader")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("rust reader child failed: %v", err)
		}
	})
	t.Run("exclusion", func(t *testing.T) {
		main := liveGenPath(t, "go-parent-exclusion")
		createDirectLive(t, main)
		w := openWriter(t, main)
		commitDirect(t, w, 10, 30, 1)
		// The Go writer stays open: the Rust child must be excluded.
		cmd := runRustChild(binary, main, "exclusion")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("rust exclusion child failed: %v", err)
		}
		closeWriter(t, w)
	})
	t.Run("pinned", func(t *testing.T) {
		main := liveGenPath(t, "go-parent-pinned")
		createDirectLive(t, main)
		w := openWriter(t, main)
		commitDirect(t, w, 10, 30, 1) // generation 2, pinned by the child
		cmd := runRustChild(binary, main, "pinned")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan error, 1)
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if scanner.Text() == "READY" {
					ready <- nil
					return
				}
			}
			ready <- fmt.Errorf("rust child exited before READY: %v", scanner.Err())
		}()
		select {
		case err := <-ready:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(mixedChildTimeout):
			_ = cmd.Process.Kill()
			t.Fatal("rust pinned child never became ready")
		}
		commitDirect(t, w, 12, 18, 2) // generation 3 retires generation 2
		result, err := w.Reclaim(10, 10_000, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != ReclaimOutcomeNoChange {
			t.Fatalf("reclaim while rust reader pinned = %v, want NoChange", result.Outcome)
		}
		finishChild(t, cmd, stdin, "pinned")
		result, err = w.Reclaim(10, 10_000, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome == ReclaimOutcomeNoChange {
			t.Fatal("reclaim after rust reader release stayed blocked")
		}
		closeWriter(t, w)
	})
}

// TestMixedLiveGoChild is the child entry the Rust parent spawns
// (mode via IPRANGE_V4_MIXED_LIVE_MODE); a normal suite run skips it.
func TestMixedLiveGoChild(t *testing.T) {
	if os.Getenv(mixedGoChildMarker) != "1" {
		t.Skip("mixed_live child entry: spawned by the Rust parent")
	}
	dbPath := os.Getenv(mixedLiveDBEnv)
	if dbPath == "" {
		t.Fatal("mixed_live child: missing DB env")
	}
	switch os.Getenv(mixedLiveModeEnv) {
	case "reader":
		r, err := OpenLiveReader(dbPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		info, err := r.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.TransactionID != 3 {
			t.Fatalf("reader transaction id = %d, want 3", info.TransactionID)
		}
		for probe, want := range map[IPv4]uint32{15: 2, 19: 1, 22: 1} {
			got, ok, err := r.LookupDirectV4(probe)
			if err != nil || !ok || got != want {
				t.Fatalf("lookup %d = %d ok %v err %v, want %d", probe, got, ok, err, want)
			}
		}
		if _, err := r.Close(); err != nil {
			t.Fatal(err)
		}
	case "exclusion":
		r, err := OpenLiveReader(dbPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Info(); err != nil {
			t.Fatal(err)
		}
		_, err = OpenLiveWriter(dbPath, DefaultBudget(), nil)
		if !isPubCode(err, ErrorWriterBusy) {
			t.Fatalf("second writer open = %v, want ErrorWriterBusy", err)
		}
		if _, err := r.Close(); err != nil {
			t.Fatal(err)
		}
	case "pinned":
		r, err := OpenLiveReader(dbPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		info, err := r.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.TransactionID != 2 {
			t.Fatalf("pinned reader transaction id = %d, want 2", info.TransactionID)
		}
		for probe, want := range map[IPv4]uint32{15: 1, 19: 1} {
			got, ok, err := r.LookupDirectV4(probe)
			if err != nil || !ok || got != want {
				t.Fatalf("pinned lookup %d = %d ok %v err %v, want %d", probe, got, ok, err, want)
			}
		}
		fmt.Println("READY")
		os.Stdout.Sync()
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatalf("pinned hold: %v", err)
		}
		// The pinned generation must survive the Rust parent's
		// generation 3 and its first reclamation.
		for probe, want := range map[IPv4]uint32{15: 1, 19: 1} {
			got, ok, err := r.LookupDirectV4(probe)
			if err != nil || !ok || got != want {
				t.Fatalf("pinned post-hold lookup %d = %d ok %v err %v, want %d", probe, got, ok, err, want)
			}
		}
		if _, err := r.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown mixed_live mode %q", os.Getenv(mixedLiveModeEnv))
	}
}

// ---- shared live helpers (mixed battery only) ----

func liveGenPath(t *testing.T, label string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "mixed-"+label+".iprdb")
}

func createDirectLive(t *testing.T, path string) {
	t.Helper()
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
}

func openWriter(t *testing.T, path string) *LiveWriter {
	t.Helper()
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func commitDirect(t *testing.T, w *LiveWriter, from, to, value uint32) {
	t.Helper()
	tx, err := w.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(from), IPv4(to), value); err != nil {
		t.Fatal(err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("commit = %+v err %v", res, err)
	}
}

func closeWriter(t *testing.T, w *LiveWriter) {
	t.Helper()
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
