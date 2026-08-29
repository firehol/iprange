//go:build windows && (amd64 || arm64)

package main

import (
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// installFaultHandler installs the vectored EXCEPTION_IN_PAGE_ERROR
// containment handler of this worker session (Rust windows.rs
// Handler::install).
func installFaultHandler(control *worker.Control) (interface{ Close() }, error) {
	return control.InstallHandler()
}
