// SDK error mapping and response-ceiling preflight helpers shared by
// every handler family (Rust handlers/reader.rs parity).

package handlers

import (
	"encoding/json"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// sdkCode maps one SDK error code to its stable wire adapter code
// (the closed product code list of iprange-jsonrpc-v1.md). The
// vocabulary is the single internal/format error-code table; an
// out-of-table code degrades to the generic io name.
func sdkCode(code iprangedb.ErrorCode) string {
	if name, ok := format.ErrorCodeWireName(code); ok {
		return name
	}
	return "io"
}

// readError converts one SDK failure of a read-only operation.
func readError(err error) *rpc.HandlerError {
	if typed, ok := err.(*iprangedb.Error); ok {
		return rpc.NewHandlerError(sdkCode(typed.Code), "read_only_failure", typed.Error())
	}
	return rpc.NewHandlerError("io", "read_only_failure", err.Error())
}

// sdk converts an SDK result of a read-only operation.
func sdk[T any](result T, err error) (T, *rpc.HandlerError) {
	if err != nil {
		var zero T
		return zero, readError(err)
	}
	return result, nil
}

// boundedResult enforces the 65,000-byte response-object ceiling on a
// complete result before it is returned.
func boundedResult(result any) (any, *rpc.HandlerError) {
	probe := map[string]any{"result": result}
	if _, serr := rpc.EncodeResponseObjectProbe(probe); serr != nil {
		return nil, rpc.NewHandlerError("output_limit", "read_only_failure",
			"response object exceeds the 65000-byte limit")
	}
	return result, nil
}

// WidestU64 is the longest portably representable decimal of a u64.
const WidestU64 = "18446744073709551615"

// Widest129 is the longest decimal of the exact 129-bit address
// cardinality (binary-format-v4.md section 17).
const Widest129 = "680564733841876926926749214863536422911"

// WidestIdentity is the largest observable {volume, file} identity pair.
func WidestIdentity() map[string]any {
	return map[string]any{"volume": WidestU64, "file": WidestU64}
}

// WidestCloseFact is the largest observable live-close fact emitted by
// the adapter close owners.
func WidestCloseFact() map[string]any {
	return map[string]any{
		"outcome":              "close_incomplete",
		"cleanup":              map[string]any{},
		"coordination_cleanup": map[string]any{"kind": "retained_writer_close_required"},
	}
}

// PreflightResponseMargin covers constants the preflight template does
// not model; refusing slightly early is the honest direction.
const PreflightResponseMargin = 2048

// preflightResponse bounds the complete response object of a method
// before any work runs: the envelope carries the real echoed request
// id and the caller's worst-case result object. A response whose real
// report passes this probe always fits the ceiling; a request that
// cannot fit is refused with output_limit/not_started before any
// writer is opened or file is published.
func preflightResponse(state *rpc.SessionState, worst any) *rpc.HandlerError {
	envelope := map[string]any{"jsonrpc": "2.0", "result": worst}
	if id := state.ActiveRequestID; id != nil {
		envelope["id"] = json.RawMessage(id.AsJSON())
	}
	text, serr := rpc.EncodeResponseObjectProbe(envelope)
	if serr == nil && len(text) <= rpc.ResponseObjectLimit-PreflightResponseMargin {
		return nil
	}
	return rpc.NewHandlerError("output_limit", "not_started",
		"response object exceeds the 65000-byte limit")
}
