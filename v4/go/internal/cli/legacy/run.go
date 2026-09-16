// Legacy entry point: the one-pass argv scan of the released CLI
// (mode flags are positional, the last mode flag wins, inputs load
// in argv order, "as NAME" renames the last source) and the
// family dispatch into load -> operate -> print.

package legacy

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

// Run executes the legacy CLI and returns the process exit code.
// prog is argv[0] exactly as invoked (the C usage() prints it).
func Run(prog string, args []string) int {
	// C dies of SIGPIPE on a closed stdout; the Go runtime already
	// re-raises SIGPIPE for writes to fds 1/2, so no action is
	// needed for parity (the JSON-RPC transport disables that path).
	started := time.Now()
	o := DefaultOptions()
	// C iprange6_run() re-scans the whole argv whenever -6 is
	// present, so --min-prefix/--prefixes apply to the IPv6 prefix
	// array regardless of position.
	hasV6 := false
	for _, a := range args {
		if a == "-6" || a == "--ipv6" {
			hasV6 = true
			break
		}
	}

	i := 0
	nextValue := func() string {
		i++
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	for i < len(args) {
		arg := args[i]
		// C guards every value-taking option with `i+1 < argc`, so a
		// trailing option token is not an option at all: it falls
		// through to the input path (and is skipped there in IPv6
		// mode, exactly as the C IPv6 re-scan skips it).
		if i+1 >= len(args) && optionTakesValue(arg) {
			addInputArg(o, arg)
			i++
			continue
		}
		switch arg {
		case "-h", "--help":
			fmt.Print(Usage(prog, o.DNSThreads))
			return 0
		case "--version":
			fmt.Print(Version())
			return 0
		case "--has-compare", "--has-reduce":
			fmt.Fprintln(os.Stderr, "yes, compare and reduce is present.")
			return 0
		case "--has-filelist-loading", "--has-directory-loading":
			fmt.Fprintln(os.Stderr, "yes, @filename and @directory support is present.")
			return 0
		case "--has-ipv6":
			fmt.Fprintln(os.Stderr, "yes, IPv6 support is present.")
			return 0
		case "-4", "--ipv4":
			o.Family = V4
		case "-6", "--ipv6":
			o.Family = V6
		case "--optimize", "--combine", "--merge", "--union", "--union-all", "-J":
			o.Mode = ModeMerge
		case "--common", "--intersect", "--intersect-all":
			o.Mode = ModeCommon
		case "--exclude-next", "--except", "--complement-next", "--complement":
			requirePriorFile(o, "--except")
			o.Mode = ModeExcludeNext
			setGroupB(o)
		case "--diff", "--diff-next":
			requirePriorFile(o, "--diff")
			o.Mode = ModeDiff
			setGroupB(o)
		case "--compare":
			o.Mode = ModeCompare
		case "--compare-first":
			o.Mode = ModeCompareFirst
		case "--compare-next":
			requirePriorFile(o, "--compare-next")
			o.Mode = ModeCompareNext
			setGroupB(o)
		case "--count-unique", "-C":
			o.Mode = ModeCountUnique
		case "--count-unique-all":
			o.Mode = ModeCountUniqueAll
		case "--ipset-reduce", "--reduce-factor":
			// C bounds the percentage at SIZE_MAX - 100 so the
			// stored factor (100 + N) cannot wrap.
			n := parseSize(arg, nextValue(), "It must be a non-negative integer percentage.", ^uint64(0)-100)
			o.Mode = ModeReduce
			o.ReduceFactor = 100 + n
		case "--ipset-reduce-entries", "--reduce-entries":
			n := parseSize(arg, nextValue(), "It must be a non-negative integer.", ^uint64(0))
			o.Mode = ModeReduce
			o.ReduceEntries = n
		case "--min-prefix":
			// C main() validates with the family active at this argv
			// position; iprange6_run() re-applies the option to the
			// IPv6 array whenever -6 is present.
			switch o.Family {
			case V4:
				v := parseNumber(arg, nextValue(), "It must be between 1 and 32.", 1, 32)
				for slot := 0; slot < int(v); slot++ {
					o.Prefix4Enabled[slot] = false
				}
				if hasV6 {
					for slot := 0; slot < int(v); slot++ {
						o.Prefix6Enabled[slot] = false
					}
				}
			case V6:
				v := parseNumber(arg, nextValue(), "It must be between 1 and 128.", 1, 128)
				for slot := 0; slot < int(v); slot++ {
					o.Prefix6Enabled[slot] = false
				}
			}
		case "--prefixes":
			// C main() parses with strtol over comma/space separated
			// tokens; iprange6_run() re-applies the option to the
			// IPv6 array whenever -6 is present (with the IPv6
			// 1..128 bound at that phase).
			list, err := parsePrefixList(nextValue(), o.Family, o.Debug)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			for slot := 0; slot < 33; slot++ {
				if slot < 32 && !contains(list, slot) {
					o.Prefix4Enabled[slot] = false
				}
			}
			if hasV6 {
				for slot := 0; slot < 129; slot++ {
					if slot < 128 && !contains(list, slot) {
						o.Prefix6Enabled[slot] = false
					}
				}
			}
		case "--default-prefix", "-p":
			if o.Family == V6 {
				// C: IPv6 always uses /128, so main() skips the
				// option and its value unvalidated
				// (src/iprange.c:563) and iprange6_run() skips the
				// pair again (src/iprange6_main.c:152-157). Both
				// advance past the value, so it never becomes an
				// input.
				nextValue()
				break
			}
			o.DefaultPrefix = uint32(parseNumber(arg, nextValue(), "It must be between 0 and 32.", 0, 32))
		case "--dont-fix-network":
			o.DontFixNetwork = true
		case "--print-prefix":
			v := nextValue()
			o.Print.PrefixIps = v
			o.Print.PrefixNets = v
		case "--print-suffix":
			v := nextValue()
			o.Print.SuffixIps = v
			o.Print.SuffixNets = v
		case "--print-prefix-ips":
			o.Print.PrefixIps = nextValue()
		case "--print-suffix-ips":
			o.Print.SuffixIps = nextValue()
		case "--print-prefix-nets":
			o.Print.PrefixNets = nextValue()
		case "--print-suffix-nets":
			o.Print.SuffixNets = nextValue()
		case "--print-ranges", "-j":
			o.Print.Mode = PrintRanges
		case "--print-single-ips", "-1":
			o.Print.Mode = PrintSingleIps
		case "--print-binary":
			o.Print.Mode = PrintBinary
		case "--quiet":
			o.Quiet = true
		case "--header":
			o.Header = true
		case "-v":
			o.Debug = true
		case "--dns-threads":
			o.DNSThreads = uint32(parseNumber(arg, nextValue(), "It must be an integer greater than or equal to 1.", 1, int64(^uint32(0)>>1)))
		case "--dns-silent":
			o.DNSSilent = true
		case "--dns-progress":
			o.DNSProgress = true
		case "as":
			if i+1 >= len(args) {
				// Trailing keyword: C's branch needs a next arg, so
				// "as" falls through to the file branch.
				o.Sources = append(o.Sources, SourceSpec{Kind: SourcePath, Arg: arg})
			} else if len(o.Sources) == 0 {
				// No prior ipset: C ignores the keyword and the
				// following token is an ordinary input.
			} else {
				o.Sources[len(o.Sources)-1].Label = nextValue()
			}
		default:
			addInputArg(o, arg)
		}
		i++
	}
	// No sources at all: read stdin (C behavior for both families;
	// the IPv4 twin prints one debug note first).
	if len(o.Sources) == 0 {
		if o.Debug && o.Family == V4 {
			fmt.Fprintln(os.Stderr, "iprange: No input files provided, reading from stdin")
		}
		o.Sources = append(o.Sources, SourceSpec{Kind: SourcePath})
	}

	return dispatch(o, started)
}

// optionTakesValue lists the options whose C branch is guarded by
// `i+1 < argc` and therefore consumes the next argv element. A trailing
// occurrence is not an option: C leaves the branch unentered and the
// token is handled as an input.
func optionTakesValue(arg string) bool {
	switch arg {
	case "--min-prefix", "--prefixes", "--default-prefix", "-p",
		"--ipset-reduce", "--reduce-factor",
		"--ipset-reduce-entries", "--reduce-entries",
		"--print-prefix", "--print-prefix-ips", "--print-prefix-nets",
		"--print-suffix", "--print-suffix-ips", "--print-suffix-nets",
		"--dns-threads":
		return true
	}
	return false
}

// addInputArg records one argv element that the C option scanner did not
// consume: a file, "-" for stdin, "@file" list, or "@dir" directory (the
// @ destination is classified on load).
//
// In IPv6 mode C runs a second scan over the whole argv
// (src/iprange6_main.c:176) and skips every dash-prefixed element other
// than "-", so an unrecognized option is neither an input nor an error
// there. The IPv4 main has no such skip: the token is opened as a file.
func addInputArg(o *Options, arg string) {
	if o.Family == V6 && len(arg) > 1 && arg[0] == '-' {
		return
	}
	spec := SourceSpec{Kind: SourcePath, Arg: arg}
	if arg == "-" {
		spec.Arg = ""
	} else if rest, ok := strings.CutPrefix(arg, "@"); ok {
		spec = SourceSpec{Kind: SourceFileList, Arg: rest}
	}
	o.Sources = append(o.Sources, spec)
}

// requirePriorFile is the C positional-operator guard: --except, --diff
// and --compare-next need an already-loaded ipset, and C names the
// canonical option in the diagnostic regardless of the alias used
// (src/iprange.c:616,624,641).
func requirePriorFile(o *Options, option string) {
	// C gates this on `active_family != 6` (src/iprange.c:615,623,640):
	// in IPv6 mode main() defers every load to iprange6_run(), which
	// re-derives the two groups itself, so the guard does not apply and
	// the run fails later with "No valid ipsets to process."
	if o.Family == V6 {
		return
	}
	if len(o.Sources) == 0 {
		fmt.Fprintf(os.Stderr, "iprange: An ipset is needed before %s\n", option)
		os.Exit(1)
	}
}

// setGroupB records the first group-B boundary (exclude/diff/
// compare-next semantics).
func setGroupB(o *Options) {
	if o.GroupB < 0 {
		o.GroupB = len(o.Sources)
	}
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// dispatch runs the family pipeline: load -> operate -> print.
func dispatch(o *Options, started time.Time) int {
	loadDone := time.Now()
	loaded, err := loadAll(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	thinkDone := time.Now()
	ret := execute(o, loaded)
	stop := time.Now()
	if o.Debug && o.Family == V4 {
		fmt.Fprintf(os.Stderr,
			"completed in %.5f seconds (read %.5f + think %.5f + speak %.5f)\n",
			stop.Sub(started).Seconds(),
			loadDone.Sub(started).Seconds(),
			thinkDone.Sub(loadDone).Seconds(),
			stop.Sub(thinkDone).Seconds(),
		)
	}
	return ret
}

// invalidOptionValue is the C option-value error text; it exits 1
// exactly like parse_long_option_or_die.
func invalidOptionValue(option, value, expected string) {
	fmt.Fprintf(os.Stderr, "iprange: Invalid value '%s' for %s. %s\n", value, option, expected)
	os.Exit(1)
}

// parseNumber is C parse_long_option_or_die (src/iprange.c:452-463) and
// the single owner of the signed numeric legacy options (--min-prefix,
// --default-prefix/-p, --dns-threads). strtol(3) accepts leading
// whitespace and an optional sign, so a value is valid only when it is
// fully consumed and lies inside [min, max]; the bounds stay per-option.
// The unsigned reduce options use parseSize instead, which per
// src/iprange.c:465-479 must not accept a sign.
//
// strtol10 saturates at the long bounds rather than reporting ERANGE.
// Every caller bounds max below LONG_MAX and min above LONG_MIN, so an
// out-of-long value reaches C's diagnostic through the range test.
func parseNumber(option, value, expected string, min, max int64) int64 {
	parsed, consumed := strtol10(value)
	if consumed == 0 || consumed != len(value) || parsed < min || parsed > max {
		invalidOptionValue(option, value, expected)
	}
	return parsed
}

// parseSize is the strict full-string unsigned decimal parse used by
// the reduce options (bounds checked per the C option).
func parseSize(option, value, expected string, max uint64) uint64 {
	if value == "" || value[0] < '0' || value[0] > '9' {
		invalidOptionValue(option, value, expected)
	}
	var parsed uint64
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < '0' || b > '9' {
			invalidOptionValue(option, value, expected)
		}
		d := uint64(b - '0')
		if parsed > (^uint64(0)-d)/10 {
			invalidOptionValue(option, value, expected)
		}
		parsed = parsed*10 + d
	}
	if parsed > max {
		invalidOptionValue(option, value, expected)
	}
	return parsed
}

// parsePrefixList parses a --prefixes value with the C strtol loop
// (src/iprange.c:534-559 for IPv4, src/iprange6_main.c:127-146 for IPv6):
// comma- or space-separated tokens, each parsed with strtol(3) base 10 and
// truncated to int before the bound test, so 0 and negatives are rejected
// and an overflow reports the truncated value. limit is 32 for IPv4 and 128
// for IPv6; the diagnostic text differs per family, as in C.
// parsePrefixList tokenizes the --prefixes value. `debug` is the state
// of -v at this argv position: the C parses the option inside its
// sequential scan, so a -v that comes after --prefixes announces
// nothing.
func parsePrefixList(value string, fam Family, debug bool) ([]int, error) {
	limit, diag := int64(32), "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). %d is invalid."
	if fam == V6 {
		limit, diag = int64(128), "iprange: Only prefixes from 1 to 128 can be set. %d is invalid."
	}
	var list []int
	s := -1 // C's previous token start (NULL before the first iteration)
	e := 0
	for e < len(value) && e != s {
		s = e
		parsed, next := strtol10(value[e:])
		j := int32(parsed) // C casts the long to int before testing it
		if j <= 0 || int64(j) > limit {
			return nil, fmt.Errorf(diag, j)
		}
		// The IPv4 argv scan announces each accepted token with no
		// `iprange: ` prefix (src/iprange.c:549); iprange6_run()
		// parses the option without any debug output.
		if debug && fam == V4 {
			fmt.Fprintf(os.Stderr, "Enabling prefix %d\n", j)
		}
		list = append(list, int(j))
		if e+next < len(value) && (value[e+next] == ',' || value[e+next] == ' ') {
			next++
		}
		e += next
	}
	return list, nil
}

// strtol10 mirrors C strtol(s, &end, 10): leading whitespace, an optional
// sign, decimal digits, and saturation at the long bounds. It returns the
// value and the number of bytes consumed; with no digits it returns (0, 0),
// matching the C endptr == nb case.
func strtol10(s string) (int64, int) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' ||
		s[i] == '\v' || s[i] == '\f' || s[i] == '\r') {
		i++
	}
	negative := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		negative = s[i] == '-'
		i++
	}
	start := i
	var mag uint64
	overflow := false
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		d := uint64(s[i] - '0')
		if mag > (^uint64(0)-d)/10 {
			overflow = true
		}
		if !overflow {
			mag = mag*10 + d
		}
		i++
	}
	if i == start {
		return 0, 0
	}
	const maxLong = int64(^uint64(0) >> 1)
	if overflow || mag > uint64(maxLong) {
		if negative {
			return math.MinInt64, i
		}
		return math.MaxInt64, i
	}
	v := int64(mag)
	if negative {
		v = -v
	}
	return v, i
}
