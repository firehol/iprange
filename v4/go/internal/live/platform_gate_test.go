package live

// Platform gate for the whole live package: every test in this package
// builds or inspects live database pairs, and live creation needs the
// creator-only security machine (linux/freebsd in pure Go) plus the
// proven live coordination. On platforms where CreationSupported
// refuses, the entire package is skipped honestly instead of failing
// every fixture; on linux nothing changes. The v4work crash matrices
// are covered by the same gate: their parents never run on such
// platforms, so the crash child arm is never spawned.

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if err := CreationSupported(); err != nil {
		fmt.Printf("live database creation is not supported on this platform (%v); skipping the live package\n", err)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
