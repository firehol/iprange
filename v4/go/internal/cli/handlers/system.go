// `iprange.v1.system.describe`: capability discovery (Rust
// handlers/system.rs). The Go product executable advertises the
// registered v1 method inventory, the production limits of the
// transport, and the CLI-local fault-worker probe; it never exposes
// test-only fields.

package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// productVersion is the product-executable version string reported by
// system.describe (parallel to the Rust build's CARGO_PKG_VERSION).
const productVersion = "0.0.0"

// ValidateDescribeParams enforces that system.describe takes no
// parameters beyond the empty object.
func ValidateDescribeParams(params json.RawMessage) error {
	_, err := exactObject(params)
	if err != nil {
		return err
	}
	return nil
}

// Describe implements the capability snapshot (spec system.describe):
// methods, limits, worker availabilityable, and no platform result
// fields. The methods list is exactly the registered inventory in
// bytewise order; cancel is a transport notification and is never
// advertised.
func Describe(st *rpc.SessionState, _ json.RawMessage) (any, *rpc.HandlerError) {
	return map[string]any{
		"method":          "iprange.v1.system.describe",
		"product":         "iprange",
		"product_version": productVersion,
		"implementation":  "go",
		"jsonrpc_version": "2.0",
		"api_version":     "1",
		"format":          "iprange-v4-phase1-unsigned",
		"platform":        platformName(),
		"families":        []string{"ipv4", "ipv6"},
		"methods":         rpc.Advertised(),
		"export_formats": []string{
			"netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary",
		},
		"limits": map[string]any{
			"input_frame_bytes":     "1048576",
			"output_frame_bytes":    "1048576",
			"response_object_bytes": "65000",
			"batch_requests":        rpc.BatchLimit,
			"queued_requests":       rpc.QueuedLimit,
			"reader_handles":        64,
			"cursor_handles":        64,
			"lookup_addresses":      4096,
			"cursor_records":        4096,
		},
		"fault_worker": map[string]any{
			"available": faultWorkerAvailable(),
			"protocol":  "1",
		},
		"platform_result_fields": []string{},
	}, nil
}

func platformName() string {
	switch runtime.GOOS {
	case "linux":
		return "linux"
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "freebsd":
		return "freebsd"
	default:
		return "other"
	}
}

// faultWorkerAvailable reports whether a candidate version-matched
// worker executable exists beside the running binary (spec
// system.describe.fault_worker; Rust worker/client.rs
// worker_candidates). The probe never spawns the worker; the full
// version handshake runs only when validate or recover starts, and
// rejects every unrelated executable. The candidate rule mirrors the
// SDK rule: the binary's own directory, plus the deps-parent fallback
// used by test builds.
func faultWorkerAvailable() bool {
	current, err := os.Executable()
	if err != nil {
		return false
	}
	name := "iprange-v4-worker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	directory := filepath.Dir(current)
	if isFile(filepath.Join(directory, name)) {
		return true
	}
	if filepath.Base(directory) == "deps" {
		return isFile(filepath.Join(filepath.Dir(directory), name))
	}
	return false
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
