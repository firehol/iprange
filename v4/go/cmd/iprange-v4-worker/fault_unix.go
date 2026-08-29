//go:build (linux || darwin || freebsd) && (amd64 || arm64)

package main

import (
	"runtime"

	"github.com/firehol/iprange/v4/go/internal/worker"
)

// installFaultHandler installs the POSIX SIGBUS containment handler of
// this worker session (Rust posix.rs Handler::install). The alternate
// signal stack is per-thread and Go migrates goroutines between
// threads, so the worker pins one OS thread for the whole session
// before installing (posix.rs install runs on the process's single
// thread).
func installFaultHandler(control *worker.Control) (interface{ Close() }, error) {
	runtime.LockOSThread()
	handler, err := control.InstallHandler()
	if err != nil {
		return nil, err
	}
	return handler, nil
}
