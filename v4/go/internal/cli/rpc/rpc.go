// Package rpc implements the JSON-RPC 2.0 transport of the product
// executable (iprange-jsonrpc-v1.md): framing, session, dispatcher,
// cancellation and shutdown over stdin/stdout. The handler families
// live in the sibling handlers package and call only the public Go
// SDK.
package rpc

import (
	"fmt"
	"os"
	"time"
)

// Run executes the JSON-RPC transport until EOF or fatal error and
// returns the process exit code.
func Run() int {
	session := NewSession()
	if err := session.Run(os.Stdin, os.Stdout); err != nil {
		// Best-effort diagnostic (role-round finding): the write runs
		// detached and the exit is bounded by forceExitDiagnosticGrace,
		// so a full, undrained stderr pipe can never block the process
		// exit on the graceful fatal path either (the same bound the
		// forced signal exit uses).  The message may be cut off when
		// stderr is writable but slower, which is accepted.
		go func() {
			fmt.Fprintf(os.Stderr, "iprange: %v\n", err)
		}()
		time.Sleep(forceExitDiagnosticGrace)
		return 1
	}
	return 0
}
