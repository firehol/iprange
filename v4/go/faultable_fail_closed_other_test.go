//go:build !((linux || darwin || freebsd || windows) && (amd64 || arm64))

package iprangedb

import (
	"errors"
	"testing"
)

// TestFaultableOperationsFailClosedWithoutWorker pins the spec-mandated
// fail-closed stance on platforms without a worker build
// (binary-format-v4.md section 19): validation, candidate inspection,
// and recovery refuse with the ErrorOSUnsupported worker-unavailable
// class before any source scan. A nonexistent source path proves no
// scan ran (the in-process machines would report Missing instead).
func TestFaultableOperationsFailClosedWithoutWorker(t *testing.T) {
	missing := t.TempDir() + "/no-such-source.iprdb"

	_, failure := Validate(missing, ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, nil)
	if failure == nil || failure.Cause == nil || !isOSUnsupported(t, failure.Cause) {
		t.Fatalf("Validate: failure = %v, want OSUnsupported cause", failure)
	}

	_, err := InspectRecoveryCandidates(missing, RecoveryInspectionImmutable, HeapOnly(0, 1), nil)
	if err == nil || !isOSUnsupported(t, err) {
		t.Fatalf("InspectRecoveryCandidates: err = %v, want OSUnsupported", err)
	}

	_, offlineFailure := RecoverOffline(missing, nil, t.TempDir()+"/out.iprdb", CallerCertified, RecoveryHeapOnly(1<<20, 100, 2), nil, NewCancellationToken())
	if offlineFailure == nil || offlineFailure.Cause == nil || !isOSUnsupported(t, offlineFailure.Cause) {
		t.Fatalf("RecoverOffline: failure = %v, want OSUnsupported cause", offlineFailure)
	}

	_, liveFailure := RecoverLive(missing, nil, t.TempDir()+"/live-out.iprdb", RecoveryHeapOnly(1<<20, 100, 2), nil, NewCancellationToken())
	if liveFailure == nil || liveFailure.Cause == nil || !isOSUnsupported(t, liveFailure.Cause) {
		t.Fatalf("RecoverLive: failure = %v, want OSUnsupported cause", liveFailure)
	}
}

// isOSUnsupported reports whether the public error chain carries the
// worker-unavailable class.
func isOSUnsupported(t *testing.T, err error) bool {
	t.Helper()
	var public *Error
	if !errors.As(err, &public) {
		t.Fatalf("cause is not a public *Error: %T %v", err, err)
	}
	return public.Code == ErrorOSUnsupported
}
