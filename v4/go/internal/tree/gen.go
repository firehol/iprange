// Package tree probe-loop generation driver (SOW-0027 regression slice
// H): `go generate ./internal/tree` re-emits the width-specialized
// fixed-cell search loops from internal/tree/genprobe into
// probe_widths.go. The generated file is authoritative output, not a
// hand-edited source; change the generator template instead.
package tree

//go:generate go run ./genprobe
