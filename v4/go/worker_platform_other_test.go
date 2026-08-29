//go:build !((linux || darwin || freebsd || windows) && (amd64 || arm64))

package iprangedb

// workerRouted reports false on every platform without a worker build:
// the public validation, candidate-inspection, and recovery facades
// refuse with the ErrorOSUnsupported worker-unavailable class before
// any source scan or destination mutation (binary-format-v4.md section
// 19), so tests that need a live worker skip, and the refusal itself
// is pinned by TestFaultableOperationsFailClosedWithoutWorker.
func workerRouted() bool { return false }
