// Legacy entry point: the one-pass argv scan of the released CLI
// (mode flags are positional, the last mode flag wins, inputs load
// in argv order, "as NAME" renames the last source) and the
// family dispatch into load -> operate -> print.

package legacy

import (
	"fmt"
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
			list, err := parsePrefixList(nextValue(), o.Family)
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
				// C: IPv6 always uses /128; the value is consumed
				// and not validated.
				nextValue()
				continue
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
			// Everything else is an input path: a file, "-" for
			// stdin, "@file" list, or "@dir" directory (the @
			// destination is classified on load).
			spec := SourceSpec{Kind: SourcePath, Arg: arg}
			if arg == "-" {
				spec.Arg = ""
			} else if rest, ok := strings.CutPrefix(arg, "@"); ok {
				spec = SourceSpec{Kind: SourceFileList, Arg: rest}
			}
			o.Sources = append(o.Sources, spec)
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

// requirePriorFile is the C "you must define an ipset before this"
// error for the positional operators.
func requirePriorFile(o *Options, option string) {
	if len(o.Sources) == 0 {
		fmt.Fprintf(os.Stderr, "iprange: %s requires an ipset to be defined before it.\n", option)
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

// parseNumber is the strict full-string decimal parse with i64
// bounds (C strtol semantics: empty and trailing junk rejected).
func parseNumber(option, value, expected string, min, max int64) int64 {
	if value == "" || value[0] < '0' || value[0] > '9' {
		invalidOptionValue(option, value, expected)
	}
	var parsed int64
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < '0' || b > '9' {
			invalidOptionValue(option, value, expected)
		}
		d := int64(b - '0')
		if parsed > (1<<63-1-d)/10 {
			invalidOptionValue(option, value, expected)
		}
		parsed = parsed*10 + d
	}
	if parsed < min || parsed > max {
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

// parsePrefixList parses the --prefixes value with strtol-exact
// comma/space tokenization (whitespace, signs and empty tokens
// behave like the C loop).
func parsePrefixList(value string, fam Family) ([]int, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	list := make([]int, 0, len(fields))
	for _, f := range fields {
		// C strtol: sign allowed, full-string, bound per family.
		p, err := parsePrefixToken(f, fam)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, nil
}

func parsePrefixToken(text string, fam Family) (int, error) {
	bytes := []byte(text)
	i := 0
	negative := false
	if i < len(bytes) && (bytes[i] == '-' || bytes[i] == '+') {
		negative = bytes[i] == '-'
		i++
	}
	start := i
	var value int64
	for i < len(bytes) && bytes[i] >= '0' && bytes[i] <= '9' {
		d := int64(bytes[i] - '0')
		if value > (1<<63-1-d)/10 {
			return 0, fmt.Errorf("iprange: Invalid prefix list value '%s'.", text)
		}
		value = value*10 + d
		i++
	}
	if negative {
		value = -value
	}
	if i != len(bytes) || i == start {
		return 0, fmt.Errorf("iprange: Invalid prefix list value '%s'.", text)
	}
	if fam == V6 {
		if value < 0 || value > 128 {
			return 0, fmt.Errorf("iprange: Invalid prefix list value '%s'.", text)
		}
		return int(value), nil
	}
	if value < 0 || value > 32 {
		return 0, fmt.Errorf("iprange: Invalid prefix list value '%s'.", text)
	}
	return int(value), nil
}
