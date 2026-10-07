//go:build !windows

package live

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A FIFO planted at the sidecar name must refuse (follow the switch)
// instead of blocking the caller on a plain open. The call runs under a
// bounded watchdog so a prompt-open regression fails this test in
// seconds instead of hanging the package until go test's outer
// timeout. A FIFO is a unix concept; the zero-identity twin in
// source_policy_test.go runs on every platform.
func TestSourceCreatorOnlyRefusesAFifoSidecar(t *testing.T) {
	t.Setenv("IPRANGE_CREATOR_ONLY", "0")
	dir := t.TempDir()
	main := filepath.Join(dir, "fifo.iprdb")
	createPolicySource(t, main, true)
	sidecar, err := CanonicalSidecarPath(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(sidecar, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	type answer struct {
		creatorOnly bool
	}
	answers := make(chan answer, 1)
	go func() {
		answers <- answer{SourceCreatorOnly(main)}
	}()
	select {
	case got := <-answers:
		if got.creatorOnly {
			t.Fatal("FIFO sidecar answered protected instead of following the switch")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SourceCreatorOnly wedged on the FIFO sidecar: the prompt-open regression this detector pins")
	}
}
