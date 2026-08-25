//go:build !linux || !amd64

package worker

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Control is the platform stub: the SIGBUS isolation machinery is proven
// for linux/amd64 only (Rust worker/posix.rs is cfg(unix) and the Go
// port targets the same host proof; darwin and freebsd keep the typed
// refusal stance recorded in the 4-12D native proof).
// Every constructor refuses before path access, exactly like the mapping
// owner's platform refusals, so the package cross-compiles while the
// worker surface stays typed and honest.
type Control struct{}

// workerRefusal is the single typed refusal for the whole worker surface
// on platforms without a proven implementation.
func workerRefusal() error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "worker SIGBUS isolation is not implemented on this platform"}
}

// CreateParent refuses worker creation on unsupported platforms.
func CreateParent() (*Control, error) {
	return nil, workerRefusal()
}

// OpenWorker refuses worker attachment on unsupported platforms.
func OpenWorker(_ string) (*Control, error) {
	return nil, workerRefusal()
}

// RemovePath is unreachable on unsupported platforms (no Control exists).
func (*Control) RemovePath() error { return nil }

// Arm refuses probing on unsupported platforms.
func (*Control) Arm(_ uint64, _ MappingRole, _ uintptr, _ uint64) error {
	return workerRefusal()
}

// Disarm is a no-op on unsupported platforms (no Control exists).
func (*Control) Disarm() {}

// FaultRecord refuses record reads on unsupported platforms.
func (*Control) FaultRecord() (FaultRecord, error) {
	return FaultRecord{}, workerRefusal()
}

// Close is a no-op on unsupported platforms (no Control exists).
func (*Control) Close() {}
