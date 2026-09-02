// Legacy CLI options: the exact option grammar and defaults of the
// released `iprange` command line.

package legacy

// SourceKind is how one input path turns into ipsets.
type SourceKind uint8

const (
	// SourcePath is a positional file path (or "-" for stdin).
	SourcePath SourceKind = iota
	// SourceFileList is "@file" or "@dir": the destination is
	// classified on load (a directory expands to its regular files
	// sorted by name).
	SourceFileList
)

// SourceSpec is one input argument with its optional "as NAME" label.
type SourceSpec struct {
	Kind  SourceKind
	Arg   string // the path as given; empty means stdin ("-")
	Label string // "as NAME" rename for CSV output; empty keeps the default name
}

// Mode is the mode-selecting option (C mode enum); the last mode
// flag in argv order wins.
type Mode uint8

const (
	ModeMerge Mode = iota
	ModeCommon
	ModeExcludeNext
	ModeDiff
	ModeCompare
	ModeCompareFirst
	ModeCompareNext
	ModeCountUnique
	ModeCountUniqueAll
	ModeReduce // --ipset-reduce N (IPv4 only; rejected in IPv6 mode)
)

// PrintMode is the one print shape of the run (C IPSET_PRINT_CMD);
// the last --print-* flag in argv wins.
type PrintMode uint8

const (
	PrintCidr      PrintMode = iota // default: per-range CIDR decomposition
	PrintRanges                     // --print-ranges / -j: one lo-hi line per range
	PrintSingleIps                  // --print-single-ips / -1: one address per line
	PrintBinary                     // --print-binary: the released v1.0/v2.0 payload
)

// Print holds the print-shape flags (accepted by merge/common/
// exclude/diff/reduce; ignored by the CSV modes).
type Print struct {
	Mode       PrintMode
	PrefixIps  string
	SuffixIps  string
	PrefixNets string
	SuffixNets string
}

// Options is everything the released CLI parses out of argv.
type Options struct {
	Family Family
	Mode   Mode
	// Sources in argv order, with labels.
	Sources []SourceSpec
	// GroupB is the index of the first group-B source, set by the
	// first positional operator (--except/--diff/--compare-next);
	// everything after it forms group B. -1 means none.
	GroupB int
	// DontFixNetwork: CIDR host addresses keep their raw address
	// instead of being masked to the network address.
	DontFixNetwork bool
	// DefaultPrefix (-p N; IPv4 only, 0..=32, default 32; accepted
	// but ignored in IPv6 mode, always /128 there).
	DefaultPrefix uint32
	// Prefix4Enabled / Prefix6Enabled: enabled prefix lengths for
	// CIDR decomposition; [33]/[129], all true initially. /32 (v6
	// /128) is never disabled.
	Prefix4Enabled [33]bool
	Prefix6Enabled [129]bool
	// ReduceFactor: --ipset-reduce N percent, stored as 100 + N,
	// default 120.
	ReduceFactor uint64
	// ReduceEntries: --ipset-reduce-entries N, default 16384.
	ReduceEntries uint64
	// DNSThreads: --dns-threads N, default 5.
	DNSThreads  uint32
	DNSSilent   bool
	DNSProgress bool // IPv4 only; inert in IPv6 mode
	Debug       bool // -v
	Quiet       bool // --quiet (diff only): suppress output, keep exit code
	Header      bool // --header: CSV header line in count/compare modes
	Print       Print
}

// DefaultOptions returns the released defaults.
func DefaultOptions() *Options {
	o := &Options{
		Family:        V4,
		Mode:          ModeMerge,
		GroupB:        -1,
		DefaultPrefix: 32,
		ReduceFactor:  120,
		ReduceEntries: 16384,
		DNSThreads:    5,
	}
	for i := range o.Prefix4Enabled {
		o.Prefix4Enabled[i] = true
	}
	for i := range o.Prefix6Enabled {
		o.Prefix6Enabled[i] = true
	}
	return o
}

// Enabled returns the prefix-enabled array for the selected family.
func (o *Options) Enabled() []bool {
	if o.Family == V6 {
		return o.Prefix6Enabled[:]
	}
	return o.Prefix4Enabled[:]
}
