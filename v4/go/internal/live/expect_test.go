// Shared test helper: the exact format-error-code assertion used by
// the sidecar, lifecycle, and crash tests on every OS.

package live

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// expectCode fails the test unless err carries the exact format error
// code.
func expectCode(t *testing.T, err error, code format.ErrorCode) {
	t.Helper()
	var e *format.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected code %d, got %v", code, err)
	}
	if e.Code != code {
		t.Fatalf("expected code %d, got %d (%v)", code, e.Code, err)
	}
}
