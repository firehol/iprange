// Legacy input loading: the released C `iprange` file grammar
// (`src/ipset_load.c`, `src/ipset6_load.c`), binary detection
// (`src/ipset_binary.c`, `src/ipset6_binary.c`), hostname routing
// through the DNS pool (`src/ipset_dns.c`, `src/ipset6_dns.c`), and
// `@file`/`@dir` expansion (`src/iprange.c`, `src/iprange6_main.c`).
//
// Every diagnostic below is a byte-for-byte copy of the C text
// (C `fprintf(stderr, ...)` semantics, including the embedded
// newline the C line buffer carries into `%s`). This file is the
// structural port of v4/rust/iprange-cli/src/legacy/parse.rs; the
// family-generic mechanics of the Rust core map onto the
// Family-tagged IpSet of this package instead of generics.

package legacy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"syscall"
)

// C MAX_LINE (fgets buffer): one line record is at most 1023 bytes
// plus the trailing newline slot.
const maxLine = 1024

// C MAX_INPUT_ELEMENT (IPv4 token buffer) and MAX_INPUT_ELEMENT6
// (IPv6 token buffer).
const (
	maxToken  = 255
	maxToken6 = 256
)

// LoadedSet is one loaded ipset with its CSV name. The name is the
// C `ips->filename`: the source path verbatim, `stdin`, or the `as
// NAME` label (the C code never strips directories or extensions).
type LoadedSet struct {
	Name string
	Set  *IpSet
}

// Loaded is the complete load result: every set in C chain order
// plus the group-B boundary in *loaded-set* units. The C argv scan
// records the positional-operator boundary in *source* units
// (Options.GroupB); one `@file`/`@dir` source can expand to several
// sets, so the boundary is converted here, where the expansion
// happens.
type Loaded struct {
	// Sets holds all sets in load order (group A first, then group B).
	Sets []LoadedSet
	// GroupB is the index of the first group-B set; == len(Sets)
	// when no positional operator was given.
	GroupB int
}

// loadAll loads every source in argv order and returns the sets for
// the execute phase. Group A and group B are both loaded here
// (per-source sets in argv order, each `@file`/`@dir` source
// expanded in place).
func loadAll(o *Options) (*Loaded, error) {
	return loadAllImpl(o, os.Stdin)
}

// loadAllImpl is loadAll with an injectable stdin (tests feed a
// reader).
func loadAllImpl(o *Options, stdin io.Reader) (*Loaded, error) {
	// One DNS resolver per run (C keeps one global pool for the
	// whole invocation; each file drains its own names via dns_done).
	resolver := NewResolver(o.DNSThreads, o.DNSSilent, o.DNSProgress, o.Family, o.Debug)

	var (
		stdinData  []byte
		stdinUsed  bool
		dnsUsed    bool
		loaded     []LoadedSet
		lastSource = "" // C main() context line after a load failure
		boundary   int
	)

	for i, spec := range o.Sources {
		var sets []LoadedSet
		var err error
		switch spec.Kind {
		case SourcePath:
			if spec.Arg == "" {
				// `-` (or an empty argument) reads stdin. The C
				// code consumes stdin once; a second `-` sees EOF
				// and produces an empty set.
				data := readStdinOnce(stdin, &stdinData, &stdinUsed)
				context := "iprange: Cannot load ipset from stdin"
				lastSource = context
				var set *IpSet
				var dns bool
				set, dns, err = loadOne(o, resolver, "stdin", data, context)
				if err == nil {
					dnsUsed = dnsUsed || dns
					sets = []LoadedSet{{Name: "stdin", Set: set}}
				}
			} else {
				arg := spec.Arg
				context := "iprange: Cannot load ipset: " + arg
				lastSource = context
				var data []byte
				data, err = os.ReadFile(arg)
				if err != nil {
					err = fmt.Errorf("iprange: %s - %s\n%s", arg, strerror(err), context)
				} else {
					var set *IpSet
					var dns bool
					set, dns, err = loadOne(o, resolver, arg, data, context)
					if err == nil {
						dnsUsed = dnsUsed || dns
						sets = []LoadedSet{{Name: arg, Set: set}}
					}
				}
			}
		case SourceFileList:
			sets, err = expandAt(o, resolver, spec.Arg, &lastSource, &dnsUsed)
		}
		if err != nil {
			return nil, err
		}

		// `as NAME` renames the last set the argument produced (C
		// renames `root_last`, which is the last loaded ipset).
		if spec.Label != "" {
			if n := len(sets); n > 0 {
				sets[n-1].Name = spec.Label
			}
		}
		loaded = append(loaded, sets...)
		// Loaded-set boundary at the C positional operator: every
		// set added on or after the operator's source index belongs
		// to group B (C `read_second` splits by loaded ipset order,
		// so expanded @file/@dir sets are counted individually
		// here).
		if o.GroupB == i+1 {
			boundary = len(loaded)
		}
	}
	if o.GroupB < 0 {
		boundary = len(loaded)
	}

	// C dns_done() runs at the end of every file load, but with no
	// DNS requests made it is an immediate no-op (made == 0), so the
	// pool finish is only needed when a hostname was resolved.
	if dnsUsed && resolver.Finish() {
		// The finish reports the C dns_done() failure; the IPv4
		// failure is announced by main() with the last source's
		// context line (v6 never fails).
		return nil, errors.New(lastSource)
	}

	return &Loaded{Sets: loaded, GroupB: boundary}, nil
}

// readStdinOnce reads stdin like C fgets does across the whole run;
// later `-` arguments get EOF.
func readStdinOnce(stdin io.Reader, cache *[]byte, used *bool) []byte {
	if *used {
		return nil
	}
	// C treats read errors as EOF.
	data, _ := io.ReadAll(stdin)
	*cache = data
	*used = true
	return data
}

// expandAt expands `@path`: a directory loads every regular file
// sorted by name (C qsort + strcmp byte order on the full path); a
// plain file is a file list of paths, one per line.
func expandAt(o *Options, resolver *Resolver, list string, lastSource *string, dnsUsed *bool) ([]LoadedSet, error) {
	md, err := os.Stat(list)
	if err != nil {
		return nil, fmt.Errorf("iprange: Cannot access %s: %s", list, strerror(err))
	}

	if md.IsDir() {
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: Loading files from directory %s\n", list)
		}

		var files []string
		entries, err := os.ReadDir(list)
		if err != nil {
			return nil, fmt.Errorf("iprange: Cannot access %s: %s", list, strerror(err))
		}
		for _, entry := range entries {
			// C skips entries whose stat() fails; "." and ".." are
			// not produced by os.ReadDir.
			path := list + "/" + entry.Name()
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.Mode().IsRegular() {
				files = append(files, path)
			}
		}
		sort.Strings(files)

		if len(files) == 0 {
			if o.Debug {
				fmt.Fprintf(os.Stderr, "iprange: Directory %s is empty or contains no valid files\n", list)
			}
			return nil, fmt.Errorf("iprange: No valid files found in directory: %s", list)
		}

		sets := make([]LoadedSet, 0, len(files))
		for _, path := range files {
			if o.Debug {
				fmt.Fprintf(os.Stderr, "iprange: Loading file %s from directory %s\n", path, list)
			}
			var context string
			if o.Family == V4 {
				context = fmt.Sprintf("iprange: Cannot load file %s from directory %s", path, list)
			} else {
				context = fmt.Sprintf("iprange: Cannot load file %s", path)
			}
			*lastSource = context
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("iprange: %s - %s\n%s", path, strerror(err), context)
			}
			set, dns, err := loadOne(o, resolver, path, data, context)
			if err != nil {
				return nil, err
			}
			*dnsUsed = *dnsUsed || dns
			sets = append(sets, LoadedSet{Name: path, Set: set})
		}
		return sets, nil
	}

	// A non-directory @ target is a file list (C opendir() fails
	// with ENOTDIR and falls into the list branch).
	if o.Debug {
		fmt.Fprintf(os.Stderr, "iprange: Loading files from list %s\n", list)
	}
	content, err := os.ReadFile(list)
	if err != nil {
		return nil, fmt.Errorf("iprange: Cannot open file list: %s - %s", list, strerror(err))
	}

	var sets []LoadedSet
	lineid := 0
	recs := &records{data: content}
	for rec := recs.next(); rec != nil; rec = recs.next() {
		lineid++
		s := skipWs(rec)
		if len(s) == 0 || s[0] == '\n' || s[0] == '\r' || s[0] == '#' || s[0] == ';' {
			continue
		}
		path := string(trimTrailingWs(s))
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: Loading file %s from list (line %d)\n", path, lineid)
		}
		context := fmt.Sprintf("iprange: Cannot load file %s from list %s (line %d)", path, list, lineid)
		*lastSource = context
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("iprange: %s - %s\n%s", path, strerror(err), context)
		}
		set, dns, err := loadOne(o, resolver, path, data, context)
		if err != nil {
			return nil, err
		}
		*dnsUsed = *dnsUsed || dns
		sets = append(sets, LoadedSet{Name: path, Set: set})
	}

	if len(sets) == 0 {
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: File list %s is empty or contains no valid entries\n", list)
		}
		return nil, fmt.Errorf("iprange: No valid files found in file list: %s", list)
	}
	return sets, nil
}

// fileIssues is the per-file loading state accumulated while walking
// the records (C ipset_load() locals plus dns_done() outcome).
type fileIssues struct {
	parseFailed   bool // any line failed to parse (C parse_errors)
	dnsUsed       bool // at least one hostname queued (C dns_requests_made > 0)
	dnsFailed     bool // at least one reply failed (v4 fails the file; v6 ignores)
	requestFailed bool // a request could not be queued (C dns_request() == -1; fails both families)
	droppedV6     int  // non-mapped IPv6 lines dropped in IPv4 mode (C counter, per successful load)
}

// loadOne loads one file (or the stdin stream) into a fresh set.
// `context` is the C main()-level error line printed by the caller
// when the load fails (the load itself prints the specific
// diagnostics). The bool result reports whether any hostname was
// queued (it gates the run-end resolver finish).
func loadOne(o *Options, resolver *Resolver, name string, data []byte, context string) (*IpSet, bool, error) {
	if o.Debug {
		if o.Family == V4 {
			fmt.Fprintf(os.Stderr, "iprange: Loading from %s\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "iprange: Loading from %s (IPv6 mode)\n", name)
		}
	}

	recs := &records{data: data}
	first := recs.next()
	if first == nil {
		// C: the first fgets() returns NULL: valid empty set.
		if o.Debug {
			fmt.Fprintf(os.Stderr, "iprange: %s is empty\n", name)
		}
		return NewIpSet(o.Family), false, nil
	}

	// The IPv6 loader strips a UTF-8 BOM from the first line only.
	if o.Family == V6 && len(first) >= 3 && first[0] == 0xEF && first[1] == 0xBB && first[2] == 0xBF {
		first = first[3:]
	}

	// Binary detection: the whole first record must equal the header
	// line (newline included); the rest of the file is binary.
	if string(first) == binaryHeaderV10 || string(first) == binaryHeaderV20 {
		var set *IpSet
		var err error
		switch {
		case o.Family == V4 && string(first) == binaryHeaderV10:
			set, err = LoadV1(data, name)
			if err != nil {
				return nil, false, fmt.Errorf("%s\niprange: Cannot fast load %s", err, name)
			}
		case o.Family == V6 && string(first) == binaryHeaderV20:
			set, err = LoadV2(data, name)
			if err != nil {
				return nil, false, fmt.Errorf("%s\niprange: Cannot load binary v2 %s", err, name)
			}
		case o.Family == V4:
			return nil, false, fmt.Errorf("iprange: %s: IPv6 binary file cannot be loaded in IPv4 mode (use -6)", name)
		default:
			return nil, false, fmt.Errorf("iprange: %s: IPv4 binary file cannot be loaded in IPv6 mode", name)
		}
		if o.Debug {
			kind := "non-optimized"
			if set.Optimized {
				kind = "optimized"
			}
			fmt.Fprintf(os.Stderr, "iprange: Binary loaded %s %s\n", kind, name)
		}
		return set, false, nil
	}

	set := NewIpSet(o.Family)
	var issues fileIssues

	// C ipset_load() counts fgets records as "lines" (lineid starts
	// at 0 and increments once per record; long physical lines split
	// into several records with increasing ids).
	lineid := 1
	processRecord(o, resolver, first, lineid, name, set, &issues)
	for rec := recs.next(); rec != nil; rec = recs.next() {
		lineid++
		processRecord(o, resolver, rec, lineid, name, set, &issues)
	}

	// C ipset_load() order: dns_done() drains the file's batch, adds
	// the replies in load order, and fails the file in v4 mode when
	// any reply failed; then the parse-errors check, the IPv6-drop
	// warning, and the debug "Loaded" line. The failure itself is
	// reported by the caller.
	if issues.dnsUsed {
		for _, reply := range resolver.Drain() {
			if reply.Err == nil {
				// One entry (and one C `lines` unit) per reply
				// address; per-name duplicates are dropped (v6
				// mapped-A duplicates too).
				seen := make(map[IP128]struct{}, len(reply.Addrs))
				for _, addr := range reply.Addrs {
					if _, dup := seen[addr]; dup {
						continue
					}
					seen[addr] = struct{}{}
					addEntry(set, Range{Lo: addr, Hi: addr})
				}
			} else {
				// The DNS pool renders the C failure line; silent
				// gates the not-found class exactly like C
				// dns_request_failed(), never the system class. The
				// name contributes nothing.
				if !o.DNSSilent || !reply.Err.(dnsSilentGated).silentGated() {
					fmt.Fprintln(os.Stderr, reply.Err)
				}
				issues.dnsFailed = true
			}
		}
	}
	if (issues.dnsFailed && o.Family == V4) || issues.requestFailed {
		return nil, false, errors.New(context)
	}
	if issues.parseFailed {
		return nil, false, errors.New(context)
	}
	if issues.droppedV6 > 0 {
		fmt.Fprintln(os.Stderr, fmtDropWarning(name, issues.droppedV6))
	}
	if o.Debug {
		kind := "non-optimized"
		if set.Optimized {
			kind = "optimized"
		}
		fmt.Fprintf(os.Stderr, "iprange: Loaded %s %s\n", kind, name)
	}

	return set, issues.dnsUsed, nil
}

// dnsSilentGated is implemented by the DNS pool's not-found reply
// error (C EAI_NONAME/EAI_FAIL/EAI_FAMILY and the EAI_AGAIN
// permanent-failure class). --dns-silent suppresses its pre-rendered
// line; system-class errors (EAI_SYSTEM and friends) always print.
// The DNS pool marks its not-found type with
// `silentGated() bool { return true }` (same package, unexported
// method); an unmarked error defaults to the gated class, matching
// the common failure mode.
type dnsSilentGated interface {
	silentGated() bool
}

// Line outcomes of the two C grammars (IPSET_LINE_TYPE /
// IPSET6_LINE_TYPE).
type lineKind uint8

const (
	lineEmpty lineKind = iota
	lineOneIP
	lineTwoIPs
	lineHostname
	lineWarnedRange
	lineInvalid
)

// lineOutcome is one classified record. warnRange carries the
// C classification-time warning text; the first IP of the broken
// range is still added.
type lineOutcome struct {
	kind    lineKind
	tok     string
	tok2    string
	warning string
}

// classifyAndAdd classifies one record and applies the load-time
// action of its outcome (C parse_line/parse_line6 plus the
// ipset_load() switch).
func processRecord(o *Options, resolver *Resolver, rec []byte, lineid int, name string, set *IpSet, issues *fileIssues) {
	var out lineOutcome
	if o.Family == V4 {
		out = classifyV4(rec, lineid)
	} else {
		out = classifyV6(rec)
	}

	switch out.kind {
	case lineEmpty:
		// nothing on this line
	case lineOneIP:
		if err := addToken(o, out.tok, set); err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, fmtCannotUnderstand(lineid, name, rec))
			issues.parseFailed = true
		}
	case lineTwoIPs:
		r1, err := parseToken(o, out.tok)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, fmtCannotUnderstand(lineid, name, rec))
			issues.parseFailed = true
			return
		}
		r2, err := parseToken(o, out.tok2)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, fmtCannotUnderstand(lineid, name, rec))
			issues.parseFailed = true
			return
		}
		// IPv6 mode rejects ranges with mixed-family endpoints
		// (C classify_address() differs between the two tokens).
		if o.Family == V6 && Classify(out.tok) != Classify(out.tok2) {
			fmt.Fprintln(os.Stderr, fmtMixedFamily(lineid, out.tok, out.tok2))
			issues.parseFailed = true
			return
		}
		lo := r1.Lo
		if r2.Lo.Compare(lo) < 0 {
			lo = r2.Lo
		}
		hi := r1.Hi
		if r2.Hi.Compare(hi) > 0 {
			hi = r2.Hi
		}
		addEntry(set, Range{Lo: lo, Hi: hi})
	case lineWarnedRange:
		// C prints during line classification and still adds the
		// first IP as a single entry.
		fmt.Fprintln(os.Stderr, out.warning)
		if err := addToken(o, out.tok, set); err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, fmtCannotUnderstand(lineid, name, rec))
			issues.parseFailed = true
		}
	case lineHostname:
		issues.dnsUsed = true
		if o.Debug {
			if o.Family == V4 {
				fmt.Fprintf(os.Stderr, "iprange: DNS resolution for hostname '%s' from line %d of file %s.\n", out.tok, lineid, name)
			} else {
				fmt.Fprintf(os.Stderr, "iprange: DNS resolution for hostname '%s' from line %d of file %s (IPv6 mode).\n", out.tok, lineid, name)
			}
		}
		// Queue the host; C dns_request()/dns6_request() returns -1
		// (failing the file) only for empty/oversized names. The
		// replies are added by the per-file drain below.
		if err := resolver.Request(out.tok); err != nil {
			// Always printed (C does not gate this class).
			fmt.Fprintln(os.Stderr, err)
			issues.requestFailed = true
		}
	case lineInvalid:
		if o.Family == V4 && colons(rec) >= 2 {
			// C: any unparseable line with two colons is treated as
			// an IPv6 line in IPv4 mode: mapped ::ffff: lines
			// convert back to IPv4, everything else is dropped with
			// the per-file counter.
			if r, ok := ConvertForeignV4(string(skipWs(rec))); ok {
				addEntry(set, r)
			} else {
				issues.droppedV6++
			}
		} else {
			fmt.Fprintln(os.Stderr, fmtCannotUnderstand(lineid, name, rec))
			issues.parseFailed = true
		}
	}
}

// parseToken parses one address token (ADDR, ADDR/PREFIX) with the
// family parse policy. `prefix` is the family default applied when
// the token carries no prefix (v4 --default-prefix, v6 always 128);
// fixNetwork mirrors C cidr_use_network (--dont-fix-network).
func parseToken(o *Options, token string) (Range, error) {
	if o.Family == V6 {
		// The v6 parser handles v4-class tokens too (normalized to
		// mapped IPv6); the C v6 loader never reads --default-prefix.
		return ParseCIDRV6(token, 128, !o.DontFixNetwork, 128)
	}
	return ParseCIDRV4(token, o.DefaultPrefix, !o.DontFixNetwork, o.DefaultPrefix)
}

// addToken parses one token and adds it; the error carries the C
// family diagnostic.
func addToken(o *Options, tok string, set *IpSet) error {
	r, err := parseToken(o, tok)
	if err != nil {
		return err
	}
	addEntry(set, r)
	return nil
}

// addEntry adds one range with the C `lines` accounting: every
// successful add increments lines, even when it adjacency-merges
// into the last range (C ipset_added_entry).
func addEntry(set *IpSet, r Range) {
	set.Lines++
	set.AddRange(r)
}

// colons counts `:` bytes (C strchr loop detects a second colon).
func colons(rec []byte) int {
	n := 0
	for _, b := range rec {
		if b == ':' {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Line grammar (exact C parse_line / parse_line6 ports)
// ---------------------------------------------------------------------------

func isHostnameChar(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		b == '_' || b == '-' || b == '.'
}

func isV4TokenChar(b byte) bool {
	return (b >= '0' && b <= '9') || b == '.' || b == '/'
}

func isV6TokenChar(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') ||
		b == ':' || b == '.' || b == '/'
}

func skipWs(s []byte) []byte {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}

// trimTrailingWs is C iprange_trim_trailing_whitespace(): strips
// `\n \r space \t` from the end of a file-list path line.
func trimTrailingWs(s []byte) []byte {
	for len(s) > 0 {
		b := s[len(s)-1]
		if b != '\n' && b != '\r' && b != ' ' && b != '\t' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// scan reads the leading run of predicate-true bytes, capped at the
// C token-buffer size (ipstr[MAX_INPUT_ELEMENT(-6)]).
func scan(s []byte, n int, pred func(byte) bool) (tok, rest []byte) {
	end := len(s)
	if end > n {
		end = n
	}
	i := 0
	for i < end && pred(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// tokenLooksIPLike is C token_looks_ip_like(): has a dot or a slash.
func tokenLooksIPLike(t []byte) bool {
	return bytes.IndexByte(t, '.') >= 0 || bytes.IndexByte(t, '/') >= 0
}

// tokenIsCompleteV4 is C token_is_complete_ipv4_candidate().
func tokenIsCompleteV4(t []byte) bool {
	if bytes.IndexByte(t, '/') >= 0 {
		return true
	}
	dots, digits := 0, 0
	for _, b := range t {
		if b >= '0' && b <= '9' {
			digits++
			continue
		}
		if b == '.' && digits > 0 {
			dots++
			digits = 0
			continue
		}
		return false
	}
	return dots == 3 && digits > 0
}

// lineIsHostnameCandidate is C line_is_hostname_candidate(): the
// whole line is hostname chars plus trailing whitespace and a
// comment or end of line.
func lineIsHostnameCandidate(line []byte) bool {
	s := skipWs(line)
	has := false
	for len(s) > 0 && isHostnameChar(s[0]) {
		has = true
		s = s[1:]
	}
	if !has {
		return false
	}
	s = skipWs(s)
	return len(s) == 0 || s[0] == '#' || s[0] == ';' || s[0] == '\r' || s[0] == '\n'
}

// classifyV4 is C parse_line() (src/ipset_load.c); lineid feeds the
// two warnings that the parser prints while classifying.
func classifyV4(rec []byte, lineid int) lineOutcome {
	s := skipWs(rec)
	if len(s) == 0 {
		return lineOutcome{kind: lineEmpty}
	}
	switch s[0] {
	case '#', ';', '\r', '\n':
		return lineOutcome{kind: lineEmpty}
	}

	tok, rest0 := scan(s, maxToken, isV4TokenChar)
	if len(tok) == 0 {
		return hostnameV4(rec)
	}

	hostnameCandidate := lineIsHostnameCandidate(rec)
	rest := skipWs(rest0)
	if len(rest) > 0 {
		switch rest[0] {
		case '#', ';':
			return lineOutcome{kind: lineOneIP, tok: string(tok)}
		case '\r', '\n':
			return lineOutcome{kind: lineOneIP, tok: string(tok)}
		}
	} else {
		return lineOutcome{kind: lineOneIP, tok: string(tok)}
	}

	if rest[0] != '-' {
		if bytes.IndexByte(tok, '/') >= 0 {
			return lineOutcome{kind: lineInvalid}
		}
		if tokenLooksIPLike(tok) && tokenIsCompleteV4(tok) {
			return lineOutcome{kind: lineInvalid}
		}
		if hostnameCandidate {
			return hostnameV4(rec)
		}
		return lineOutcome{kind: lineInvalid}
	}

	after := skipWs(rest[1:])
	if len(after) > 0 {
		switch after[0] {
		case '#', ';':
			return lineOutcome{
				kind:    lineWarnedRange,
				tok:     string(tok),
				warning: fmtIgnoreText(lineid, string(after)),
			}
		case '\r', '\n':
			return lineOutcome{
				kind:    lineWarnedRange,
				tok:     string(tok),
				warning: fmtIncompleteV4(lineid),
			}
		}
	} else {
		return lineOutcome{
			kind:    lineWarnedRange,
			tok:     string(tok),
			warning: fmtIncompleteV4(lineid),
		}
	}

	tok2, rest2 := scan(after, maxToken, isV4TokenChar)
	if len(tok2) == 0 {
		if bytes.IndexByte(tok, '/') < 0 && !tokenIsCompleteV4(tok) && hostnameCandidate {
			return hostnameV4(rec)
		}
		return lineOutcome{kind: lineInvalid}
	}
	rest2 = skipWs(rest2)
	if len(rest2) == 0 || rest2[0] == '#' || rest2[0] == ';' || rest2[0] == '\r' || rest2[0] == '\n' {
		return lineOutcome{kind: lineTwoIPs, tok: string(tok), tok2: string(tok2)}
	}
	if bytes.IndexByte(tok, '/') < 0 && !tokenIsCompleteV4(tok) && hostnameCandidate {
		return hostnameV4(rec)
	}
	return lineOutcome{kind: lineInvalid}
}

// hostnameV4 is C parse_hostname() (IPv4 form): hostname chars, then
// whitespace, then a comment or end of line.
func hostnameV4(rec []byte) lineOutcome {
	h, rest := scan(skipWs(rec), maxToken, isHostnameChar)
	if len(h) == 0 {
		return lineOutcome{kind: lineInvalid}
	}
	rest = skipWs(rest)
	if len(rest) == 0 || rest[0] == '#' || rest[0] == ';' || rest[0] == '\r' || rest[0] == '\n' {
		return lineOutcome{kind: lineHostname, tok: string(h)}
	}
	return lineOutcome{kind: lineInvalid}
}

// classifyV6 is C parse_line6() (src/ipset6_load.c). The "incomplete
// range" warning carries no line number and has a single text for
// comments and end-of-line alike.
func classifyV6(rec []byte) lineOutcome {
	s := skipWs(rec)
	if len(s) == 0 {
		return lineOutcome{kind: lineEmpty}
	}
	switch s[0] {
	case '#', ';', '\r', '\n':
		return lineOutcome{kind: lineEmpty}
	}

	tok, rest0 := scan(s, maxToken6, isV6TokenChar)
	if len(tok) == 0 {
		// Direct hostname path (C scans from the line start).
		h, rest := scan(skipWs(rec), maxToken6, isHostnameChar)
		if len(h) == 0 {
			return lineOutcome{kind: lineInvalid}
		}
		rest = skipWs(rest)
		if len(rest) == 0 || rest[0] == '#' || rest[0] == ';' || rest[0] == '\r' || rest[0] == '\n' {
			return lineOutcome{kind: lineHostname, tok: string(h)}
		}
		return lineOutcome{kind: lineInvalid}
	}

	hasColon := bytes.IndexByte(tok, ':') >= 0
	rest := skipWs(rest0)
	if len(rest) == 0 || rest[0] == '#' || rest[0] == ';' || rest[0] == '\r' || rest[0] == '\n' {
		return lineOutcome{kind: lineOneIP, tok: string(tok)}
	}

	if rest[0] != '-' {
		// The token is not an address of any family: retry as a
		// hostname from the start of the line (C behavior).
		if !hasColon && Classify(string(tok)) == ClassOther {
			h, r2 := scan(skipWs(rec), maxToken6, isHostnameChar)
			if len(h) > 0 {
				r2 = skipWs(r2)
				if len(r2) == 0 || r2[0] == '#' || r2[0] == ';' || r2[0] == '\r' || r2[0] == '\n' {
					return lineOutcome{kind: lineHostname, tok: string(h)}
				}
			}
		}
		return lineOutcome{kind: lineInvalid}
	}

	after := skipWs(rest[1:])
	if len(after) == 0 || after[0] == '#' || after[0] == ';' || after[0] == '\r' || after[0] == '\n' {
		return lineOutcome{
			kind:    lineWarnedRange,
			tok:     string(tok),
			warning: fmtIncompleteV6(),
		}
	}

	tok2, rest2 := scan(after, maxToken6, isV6TokenChar)
	if len(tok2) == 0 {
		return lineOutcome{kind: lineInvalid}
	}
	rest2 = skipWs(rest2)
	if len(rest2) == 0 || rest2[0] == '#' || rest2[0] == ';' || rest2[0] == '\r' || rest2[0] == '\n' {
		return lineOutcome{kind: lineTwoIPs, tok: string(tok), tok2: string(tok2)}
	}
	return lineOutcome{kind: lineInvalid}
}

// records is the C fgets(line, MAX_LINE, fp) record iteration: each
// record is at most 1023 bytes and includes the newline when one
// appears within that window; long physical lines continue as
// further records.
type records struct {
	data []byte
	pos  int
}

func (r *records) next() []byte {
	if r.pos >= len(r.data) {
		return nil
	}
	start := r.pos
	end := start + maxLine - 1
	if end > len(r.data) {
		end = len(r.data)
	}
	if k := bytes.IndexByte(r.data[start:end], '\n'); k >= 0 {
		r.pos = start + k + 1
		return r.data[start : start+k+1]
	}
	r.pos = end
	return r.data[start:end]
}

// ---------------------------------------------------------------------------
// Diagnostic texts (byte-for-byte C copies)
// ---------------------------------------------------------------------------

// fmtCannotUnderstand embeds the raw record verbatim, including its
// trailing newline (the C buffer printed by %s), so the caller adds
// only the closing newline.
func fmtCannotUnderstand(lineid int, name string, raw []byte) string {
	return fmt.Sprintf("iprange: Cannot understand line No %d from %s: %s", lineid, name, string(raw))
}

func fmtIgnoreText(lineid int, found string) string {
	return fmt.Sprintf("iprange: Ignoring text on line %d, expected an ip address after -, but found '%s'", lineid, found)
}

func fmtIncompleteV4(lineid int) string {
	return fmt.Sprintf("iprange: Incomplete range on line %d, expected an ip address after -, but line ended", lineid)
}

func fmtIncompleteV6() string {
	return "iprange: Incomplete range on line, expected an address after -"
}

func fmtMixedFamily(lineid int, a, b string) string {
	return fmt.Sprintf("iprange: Mixed-family range on line %d: %s - %s", lineid, a, b)
}

func fmtDropWarning(name string, count int) string {
	return fmt.Sprintf("iprange: %s: %d IPv6 entries dropped (use -6 for IPv6 mode)", name, count)
}

// strerror mirrors the C strerror(errno) text (glibc) so the
// diagnostics are byte-exact. Go's syscall errors carry the numeric
// errno but the standard library has no strerror, so the texts the
// legacy surface can realistically produce are pinned in a table
// generated from the oracle libc; unmapped errnos fall back to the
// Go text.
func strerror(e error) string {
	var errno syscall.Errno
	if errors.As(e, &errno) {
		if msg, ok := errnoText[errno]; ok {
			return msg
		}
	}
	return e.Error()
}

var errnoText = map[syscall.Errno]string{
	syscall.EPERM:           "Operation not permitted",
	syscall.ENOENT:          "No such file or directory",
	syscall.ESRCH:           "No such process",
	syscall.EINTR:           "Interrupted system call",
	syscall.EIO:             "Input/output error",
	syscall.ENXIO:           "No such device or address",
	syscall.E2BIG:           "Argument list too long",
	syscall.ENOEXEC:         "Exec format error",
	syscall.EBADF:           "Bad file descriptor",
	syscall.ECHILD:          "No child processes",
	syscall.EAGAIN:          "Resource temporarily unavailable",
	syscall.ENOMEM:          "Cannot allocate memory",
	syscall.EACCES:          "Permission denied",
	syscall.EFAULT:          "Bad address",
	syscall.ENOTBLK:         "Block device required",
	syscall.EBUSY:           "Device or resource busy",
	syscall.EEXIST:          "File exists",
	syscall.EXDEV:           "Invalid cross-device link",
	syscall.ENODEV:          "No such device",
	syscall.ENOTDIR:         "Not a directory",
	syscall.EISDIR:          "Is a directory",
	syscall.EINVAL:          "Invalid argument",
	syscall.ENFILE:          "Too many open files in system",
	syscall.EMFILE:          "Too many open files",
	syscall.ENOTTY:          "Inappropriate ioctl for device",
	syscall.ETXTBSY:         "Text file busy",
	syscall.EFBIG:           "File too large",
	syscall.ENOSPC:          "No space left on device",
	syscall.ESPIPE:          "Illegal seek",
	syscall.EROFS:           "Read-only file system",
	syscall.EMLINK:          "Too many links",
	syscall.EPIPE:           "Broken pipe",
	syscall.EDOM:            "Numerical argument out of domain",
	syscall.ERANGE:          "Numerical result out of range",
	syscall.EDEADLK:         "Resource deadlock avoided",
	syscall.ENAMETOOLONG:    "File name too long",
	syscall.ENOLCK:          "No locks available",
	syscall.ENOSYS:          "Function not implemented",
	syscall.ENOTEMPTY:       "Directory not empty",
	syscall.ELOOP:           "Too many levels of symbolic links",
	syscall.ENODATA:         "No data available",
	syscall.ETIME:           "Timer expired",
	syscall.EOVERFLOW:       "Value too large for defined data type",
	syscall.ESTALE:          "Stale file handle",
	syscall.EDQUOT:          "Disk quota exceeded",
	syscall.EOWNERDEAD:      "Owner died",
	syscall.ENOTRECOVERABLE: "State not recoverable",
}
