// Package tree probe- and family-layer generation drivers (SOW-0027
// regression slices H and I-1b): `go generate ./internal/tree` re-emits
// the width-specialized fixed-cell search loops from
// internal/tree/genprobe into probe_widths.go and the per-family
// concrete gap/replace layer from internal/tree/genfamilies into
// range_gap_v4.go / range_gap_v6.go. The generated files are
// authoritative output, not hand-edited sources; change the generator
// templates instead.
package tree

//go:generate go run ./genprobe
//go:generate go run ./genfamilies
