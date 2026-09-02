// Package rpc implements the JSON-RPC 2.0 transport of the product
// executable (iprange-jsonrpc-v1.md): framing, session, dispatcher,
// cancellation and shutdown over stdin/stdout. The handler families
// live in the sibling handlers package and call only the public Go
// SDK.
package rpc

import "os"

// Run executes the JSON-RPC transport until EOF or fatal error and
// returns the process exit code.
func Run() int {
	// Implemented in the JSON-RPC delivery step of SOW-0028
	// milestone 3; legacy surface lands first.
	os.Stderr.WriteString("iprange: --jsonrpc transport not implemented yet\n")
	return 1
}
