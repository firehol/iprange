//go:build freebsd

package iprangedb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRecoverLiveRefusesBeforePathAccessOnFreeBSD proves the public live
// recovery entry refuses before creating an output. FreeBSD has no proven
// sidecar coordination, so the parent must not spawn a worker or leave a
// destination behind.
func TestRecoverLiveRefusesBeforePathAccessOnFreeBSD(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "recovered.iprdb")
	_, failure := RecoverLive(
		filepath.Join(dir, "missing-source.iprdb"),
		&RecoveryCandidate{Label: RecoveryCandidateNewest},
		destination,
		RecoveryHeapOnly(1024*1024, 100, 3),
		nil,
		nil,
	)
	if failure == nil {
		t.Fatal("RecoverLive succeeded on FreeBSD")
	}
	var public *Error
	if !errors.As(failure.Cause, &public) || public.Code != ErrorLiveCoordinationUnsupported {
		t.Fatalf("cause = %v, want live coordination unsupported", failure.Cause)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("live recovery created an output before refusal: %v", err)
	}
}
