//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

package iprangedb

// workerRouted reports whether this platform routes the faultable
// validation, inspection, and recovery operations through the
// version-matched worker binary (the worker cross-build matrix). On
// these platforms the routed facade tests run against the real worker.
func workerRouted() bool { return true }
