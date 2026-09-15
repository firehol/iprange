// Package rpc implements the JSON-RPC 2.0 transport of the product
// executable (iprange-jsonrpc-v1.md): framing, session, dispatcher,
// cancellation and shutdown over stdin/stdout. The handler families
// live in the sibling handlers package and call only the public Go
// SDK.
package rpc

import (
	"fmt"
	"os"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// Run executes the JSON-RPC transport until EOF or fatal error and
// returns the process exit code.
func Run() int {
	// The poller-readiness decision (design section 6) is taken here,
	// while the process is still single-goroutine and before the
	// session starts its reader and worker goroutines, before the
	// first frame is read, and therefore before the first handler open,
	// worker spawn or force-exit wait can exist. One decision per
	// process; nothing is reserved and no request is refused by it.
	calleropen.InitPollerReadiness()
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
		calleropen.Sleep(forceExitDiagnosticGrace)
		return 1
	}
	return 0
}
