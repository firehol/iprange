package writer

// Platform gate for the whole writer package (SOW-0025 4-12D, freebsd
// class): every test in this package creates a database file through
// mapping.Create, and the exclusive lifetime-lock machine is
// implemented on linux, darwin, and windows
// (mapping.CoordinationSupported). On platforms where the machine is
// absent the entire package is skipped honestly instead of failing
// every fixture; on the supported platforms nothing changes. The gate
// mirrors the internal/live package gate.

import (
	"fmt"
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/mapping"
)

func TestMain(m *testing.M) {
	if !mapping.CoordinationSupported() {
		fmt.Printf("database file creation is not supported on this platform; skipping the writer package\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
