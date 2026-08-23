//go:build v4work

// Crash-point state matrix for CreateLive and InitializeLive (Rust
// live_crash_tests.rs create/initialize arms): each named point exits
// the child with code 86 at the exact physical step, and the parent
// verifies the resulting artifact set and sidecar header state. The
// resolver-driven recovery assertions land with the publication
// resolver chunk (4-8); this gate proves the crash leaves exactly the
// Rust artifact state.

package live

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
)

const (
	crashChildTest = "^TestLiveCrashChild$"
	crashActionEnv = "IPRANGE_V4_TEST_ACTION"
	crashPathEnv   = "IPRANGE_V4_TEST_PATH"
	crashTimeout   = 60 * time.Second
)

// TestLiveCrashChild runs the named action at the named path with the
// armed crash point and dies at it with Rust's code 86.
func TestLiveCrashChild(t *testing.T) {
	action := os.Getenv(crashActionEnv)
	path := os.Getenv(crashPathEnv)
	if action == "" || path == "" {
		t.Skip("not a crash child")
	}
	switch action {
	case "create":
		if _, err := CreateLive(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 2, nil); err != nil {
			t.Fatal(err)
		}
	case "initialize":
		if _, err := InitializeLive(path, 2, nil); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown crash action %q", action)
	}
}

// runCrashChild spawns this test binary as the crash child with one
// crash point armed and requires exit code 86.
func runCrashChild(t *testing.T, path, action, point string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), crashTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+crashChildTest)
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		crashActionEnv+"="+action,
		crashPathEnv+"="+path,
		"IPRANGE_V4_TEST_CRASH_AT="+point,
	)
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("crash child at %s: err=%v, want exit 86", point, err)
	}
}

func TestCreateCrashPointsLeaveExactArtifacts(t *testing.T) {
	cases := []struct {
		point        string
		mainPresent  bool
		sidecarState sidecarState
	}{
		{"create.after_sidecar_sync", false, stateCreating},
		{"create.after_sidecar_parent_sync", false, stateCreating},
		{"create.after_main_sync", true, stateCreating},
		{"create.after_main_parent_sync", true, stateCreating},
		{"create.after_ready_write", true, stateReady},
		{"create.after_ready_sync", true, stateReady},
		{"create.after_ready_parent_sync", true, stateReady},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			runCrashChild(t, main, "create", tc.point)

			if tc.mainPresent {
				boot := readBootstrap(t, main)
				if boot.Meta.TxnID != 1 {
					t.Fatalf("crashed main txn = %d, want 1", boot.Meta.TxnID)
				}
			} else if _, err := os.Lstat(main); !os.IsNotExist(err) {
				t.Fatalf("main exists before its crash point: %v", err)
			}

			if got := sidecarStateOf(t, main); got != int(tc.sidecarState) {
				t.Fatalf("sidecar state = %d, want %d", got, tc.sidecarState)
			}
		})
	}
}

func TestInitializeCrashPointsLeaveExactArtifacts(t *testing.T) {
	cases := []struct {
		point        string
		sidecarState sidecarState
	}{
		{"live_initialize.after_creating_sync", stateCreating},
		{"live_initialize.after_creating_parent_sync", stateCreating},
		{"live_initialize.after_ready_sync", stateReady},
		{"live_initialize.after_ready_parent_sync", stateReady},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(main + ".readers"); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(main)
			if err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, main, "initialize", tc.point)

			after, err := os.ReadFile(main)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("initialize crash changed the main bytes")
			}
			if got := sidecarStateOf(t, main); got != int(tc.sidecarState) {
				t.Fatalf("sidecar state = %d, want %d", got, tc.sidecarState)
			}
		})
	}
}
