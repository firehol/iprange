//go:build !unix

package worker

// isDescriptorExhaustion is false where the platform hands out no descriptor
// table to exhaust; the worker's own class then stands.
func isDescriptorExhaustion(error) bool { return false }
