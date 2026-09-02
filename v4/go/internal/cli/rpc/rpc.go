// Package rpc implements the JSON-RPC 2.0 transport of the product
// executable (iprange-jsonrpc-v1.md): framing, session, dispatcher,
// cancellation and shutdown over stdin/stdout. The handler families
// live in the sibling handlers package and call only the public Go
// SDK.
package rpc

import (
	"fmt"
	"os"
)

// Run executes the JSON-RPC transport until EOF or fatal error and
// returns the process exit code.
func Run() int {
	session := NewSession()
	if err := session.Run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "iprange: %v\n", err)
		return 1
	}
	return 0
}
