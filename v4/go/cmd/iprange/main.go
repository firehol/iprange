// Command iprange is the pure-Go product executable: the released
// legacy command-line surface plus the JSON-RPC 2.0 application API
// over a bidirectional stdin/stdout pipe through `iprange --jsonrpc`
// (SOW-0028). It calls only the public Go SDK for v4 persistence and
// never reaches into v4/go/internal/{reader,writer,...}.
package main

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/cli/handlers"
	"github.com/firehol/iprange/v4/go/internal/cli/legacy"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

func main() {
	argv := os.Args
	prog := "iprange"
	if len(argv) > 0 && argv[0] != "" {
		prog = argv[0]
	}
	args := argv[1:]
	if len(args) > 0 && args[0] == "--jsonrpc" {
		if len(args) != 1 {
			// `--jsonrpc` is exclusive: mixing it with legacy
			// options or inputs is invalid JSON-RPC startup and
			// must never fall back to legacy parsing.
			os.Stderr.WriteString("iprange: --jsonrpc cannot be combined with other arguments\n")
			os.Exit(1)
		}
		handlers.RegisterAll()
		os.Exit(rpc.Run())
	}
	os.Exit(legacy.Run(prog, args))
}
