// Legacy CLI compatibility against the C binary, for the argument and
// input surface: the --prefixes value grammar, the inet_aton numeric forms
// accepted by the C DNS path, a bare directory as an input file, and the
// handling of dash-prefixed arguments. Every expectation here is ground
// truth taken from the C reference (src/iprange.c, src/iprange6_main.c,
// src/ipset_dns.c) and re-measured with the C binary.

package legacy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// G1: --prefixes accepts 1..limit and nothing else, and reports the value
// C reports: the strtol result truncated to int, so an overflow prints the
// wrapped number rather than the text that was typed.
func TestPrefixesRejectsZeroAndReportsCValues(t *testing.T) {
	cases := []struct {
		value string
		want  []int  // nil means C rejected the value
		err   string // exact C stderr line when rejected
	}{
		{"0", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		{"1,0", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		{"33", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 33 is invalid."},
		{"-1", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). -1 is invalid."},
		{"abc", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		{"3x", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		{"0x10", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		{",,", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		{"1,,2", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		// strtol saturates at LONG_MAX and C then casts to int.
		{"9223372036854775807", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). -1 is invalid."},
		{"2147483648", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). -2147483648 is invalid."},
		{"-99999999999999999999", nil, "iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). 0 is invalid."},
		// Accepted forms, including the empty value (the C loop never runs)
		// and a value whose truncation lands inside the range.
		{"", []int{}, ""},
		{"3,", []int{3}, ""},
		{"1 2", []int{1, 2}, ""},
		{"+5", []int{5}, ""},
		{" 5 ", []int{5}, ""},
		{"007", []int{7}, ""},
		{"4294967299", []int{3}, ""},
	}
	for _, tc := range cases {
		got, err := parsePrefixList(tc.value, V4, false)
		if tc.want == nil {
			if err == nil {
				t.Fatalf("--prefixes %q accepted as %v, want rejection", tc.value, got)
			}
			if err.Error() != tc.err {
				t.Fatalf("--prefixes %q error\n got %q\nwant %q", tc.value, err.Error(), tc.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("--prefixes %q rejected: %v", tc.value, err)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("--prefixes %q = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// G1: the IPv6 twin of --prefixes has its own bound (1..128) and its own
// C message, which omits the "(32 is always enabled)" clause.
func TestPrefixesV6BoundAndMessage(t *testing.T) {
	if _, err := parsePrefixList("0", V6, false); err == nil {
		t.Fatal("--prefixes 0 accepted in IPv6 mode, want rejection")
	} else if want := "iprange: Only prefixes from 1 to 128 can be set. 0 is invalid."; err.Error() != want {
		t.Fatalf("error\n got %q\nwant %q", err.Error(), want)
	}
	if _, err := parsePrefixList("129", V6, false); err == nil {
		t.Fatal("--prefixes 129 accepted in IPv6 mode, want rejection")
	} else if want := "iprange: Only prefixes from 1 to 128 can be set. 129 is invalid."; err.Error() != want {
		t.Fatalf("error\n got %q\nwant %q", err.Error(), want)
	}
	got, err := parsePrefixList("128", V6, false)
	if err != nil || fmt.Sprint(got) != fmt.Sprint([]int{128}) {
		t.Fatalf("--prefixes 128 (IPv6) = %v, %v; want [128], nil", got, err)
	}
}

// numericFormCases are the glibc inet_aton(3) forms the C oracle answers
// locally, paired with the answer the C binary itself printed. They drive
// both platform halves: the emulation on the platform whose authority
// answers them, and the refusal on every other platform, so one table
// pins the rule and it runs on every supported target.
var numericFormCases = map[string]string{
	"0x0A000001":      "10.0.0.1",
	"0x1.0x2.0x3.0x4": "1.2.3.4",
	"0x7f000001":      "127.0.0.1",
	"0x7f.1":          "127.0.0.1",
	"0xA.1":           "10.0.0.1",
	"0xA":             "0.0.0.10",
	"0x0a0b0c0d":      "10.11.12.13",
	"0X0A000001":      "10.0.0.1",
	"0xffffffff":      "255.255.255.255",
	"0x1.0xffffff":    "1.255.255.255",
	"1.1.0xffff":      "1.1.255.255",
	"0x1.2":           "1.0.0.2",
	"010.0x1":         "8.0.0.1",
}

// G2: the C resolver is glibc getaddrinfo(), which answers numeric forms
// locally instead of querying DNS. The Go resolver only parses strict
// dotted quads, so without the emulation the same input fails with a DNS
// error the C binary does not produce. Values are the C binary's own
// output, measured on the platform whose authority answers them (the
// C oracle exists on Linux; see cOracleStatus and dns_numeric_*.go).
func TestDNSNumericFormsMatchC(t *testing.T) {
	if !numericFormsAnswerHere {
		t.Skipf("scoped, not run: this platform's resolver does not answer the glibc numeric " +
			"forms and no C oracle exists here; the refusal shape is pinned by " +
			"TestDNSNumericFormsAreNotAnsweredHere")
	}
	cases := numericFormCases
	for host, want := range cases {
		ips, err := lookupLegacyHost("ip4", host)
		if err != nil {
			t.Fatalf("%q: %v (want the numeric answer %s)", host, err, want)
		}
		if len(ips) != 1 || ips[0].String() != want {
			t.Fatalf("%q resolved to %v, want [%s]", host, ips, want)
		}
	}
	// Forms the C rejects must not be answered locally, so they still go
	// to the resolver exactly as C's getaddrinfo does.
	for _, host := range numericFormRejections {
		if _, err := inetAton(host); err == nil {
			t.Fatalf("inetAton(%q) accepted, want rejection", host)
		}
	}
}

// TestDNSNumericFormsAreNotAnsweredHere pins the other platform half of
// the same table: the local emulation must not fire on a platform whose
// authority refuses the glibc numeric forms — Windows (Winsock
// WSAHOST_NOT_FOUND, proven natively), OpenBSD (strict dotted-quad/IPv6
// numeric), and the unqualified default (dns_numeric_refuse.go lists the
// basis; dns_numeric_answer.go lists the answering platforms). The
// lookup must reach the resolver and fail, never answer from the numeric
// form; otherwise Go exits 0 with content where the Rust authority exits
// 1, a divergence on the contractual dimensions.
func TestDNSNumericFormsAreNotAnsweredHere(t *testing.T) {
	if numericFormsAnswerHere {
		t.Skip("scoped, not run: this platform's authority answers the numeric forms; " +
			"the matching half is TestDNSNumericFormsMatchC")
	}
	for host := range numericFormCases {
		ips, err := lookupLegacyHost("ip4", host)
		if err == nil {
			t.Fatalf("%q answered %v locally on a platform whose authority refuses it; "+
				"the emulation must not fire here", host, ips)
		}
	}
	// The parser the emulation would use still parses the forms — the
	// scoping lives in the resolver path, not in the record parser, which
	// keeps `1.2.3.4/0x10` CIDR parsing intact on every platform.
	if _, err := inetAton("0x7f000001"); err != nil {
		t.Fatalf("inetAton must still parse the hex form (record parsing is universal): %v", err)
	}
}

// numericFormRejections are forms the C rejects (glibc does not answer
// them); the resolver must receive them on every platform.
var numericFormRejections = []string{"0x", "0xzz", "0x1.", "1.0x", "0x100000000",
	"1.1.0x10000", "0x1.0x1.0x1.0x100", "0x100.1.1.1", "-1", "0x1_2"}

// G2: the numeric short-circuit must not hide a real lookup failure, and
// must not invent an answer for a name.
func TestDNSNumericFormsFallThroughToResolver(t *testing.T) {
	if _, err := lookupLegacyHost("ip4", "invalid.invalid"); err == nil {
		t.Fatal("lookup of invalid.invalid. succeeded, want a resolver error")
	}
}

// TestExpansionNamesAreTheSharedCrossEngineBytes pins the directory
// expansion entry names byte for byte.
//
// These bytes are public output: each one is the `name` column of the
// `name,entries,unique_ips` row the `--count-unique --header` mode writes
// (ops.go ModeCountUniqueAll -> writeUniqueRow, whose grammar is C
// iprange_csv_write_unique_row, src/iprange.c:76). The Rust twin of this
// table is `expansion_names_are_the_shared_cross_engine_bytes` in
// v4/rust/iprange-cli/src/legacy/parse.rs, and the two tables are the same
// bytes on purpose: with the row grammar shared by C and the name pinned
// here, the rows the two engines print for one tree are byte-identical on
// each platform, which is the cross-engine contract decision 1A of
// SOW-0028 requires. The qualification leg also runs both products over
// this fixture shape and compares the bytes they actually printed, so the
// tables cannot drift apart unnoticed.
//
// The separator is the platform's, because both engines name an entry with
// the platform join (Go pathname.Push in expandAt, Rust Path::join), while
// C joined with the literal "%s/%s" (src/iprange.c:772) in the only build
// of C that exists.
func TestExpansionNamesAreTheSharedCrossEngineBytes(t *testing.T) {
	dirNames := []string{".hidden", "a.txt", "z.txt"}
	pins := []string{"csvpin/.hidden", "csvpin/a.txt", "csvpin/z.txt"}
	if runtime.GOOS == "windows" {
		for i := range pins {
			pins[i] = strings.ReplaceAll(pins[i], "/", `\`)
		}
	}
	base := t.TempDir()
	dir := filepath.Join(base, "csvpin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range dirNames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("192.0.2.7\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	o := DefaultOptions()
	o.Sources = []SourceSpec{{Kind: SourceFileList, Arg: dir}}
	loaded, err := loadAllImpl(o, emptyReader{})
	if err != nil {
		t.Fatalf("@directory expansion failed: %v", err)
	}
	if len(loaded.Sets) != len(pins) {
		t.Fatalf("expanded %d sets, want %d", len(loaded.Sets), len(pins))
	}
	sep := string(os.PathSeparator)
	for i, pin := range pins {
		// The fixture root is absolute, so the shared literal is the tail of
		// the printed name: "<platform separator><entry>" is the part the join
		// rule produces, and it is the part the Rust twin pins alongside the
		// same full-name assertion.
		cut := strings.LastIndexAny(pin, "/"+sep)
		name := pin[cut+1:]
		shared := sep + name
		want := dir + shared
		got := loaded.Sets[i].Name
		if got != want {
			t.Errorf("entry %d name = %q, want %q (the root plus the shared bytes %q)", i, got, want, shared)
		}
		if !strings.HasSuffix(got, shared) {
			t.Errorf("entry %d name %q does not end with the shared bytes %q", i, got, shared)
		}
	}
}

// G4: on Linux glibc fopen(path,"r") succeeds for a directory and the first
// fgets then fails, so C's ipset_load() reports no error and yields an empty
// set (exit status 0). Other read errors keep the C diagnostic.
func TestBareDirectoryInputIsEmptySetNotError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "host"), []byte("192.0.2.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Sources = []SourceSpec{{Kind: SourcePath, Arg: dir}}
	loaded, err := loadAllImpl(o, emptyReader{})
	if err != nil {
		t.Fatalf("directory input = %v, want an empty set and no error (C exit 0)", err)
	}
	if len(loaded.Sets) != 1 {
		t.Fatalf("loaded %d sets, want 1", len(loaded.Sets))
	}
	if got := len(loaded.Sets[0].Set.Ranges); got != 0 {
		t.Fatalf("directory contributed %d ranges, want 0", got)
	}
	if loaded.Sets[0].Name != dir {
		t.Fatalf("set name = %q, want the path %q", loaded.Sets[0].Name, dir)
	}
}

// G4: a directory named by a "@file list" entry goes through the same C
// ipset_load(), so it is an empty set there too; the other entries of the
// list still load.
func TestDirectoryInsideFileListIsEmptySet(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(t.TempDir(), "list.txt")
	real := filepath.Join(dir, "hosts")
	if err := os.WriteFile(real, []byte("192.0.2.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list, []byte(real+"\n"+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Sources = []SourceSpec{{Kind: SourceFileList, Arg: list}}
	loaded, err := loadAllImpl(o, emptyReader{})
	if err != nil {
		t.Fatalf("@list with a directory entry = %v, want no error (C exit 0)", err)
	}
	if len(loaded.Sets) != 2 {
		t.Fatalf("loaded %d sets, want 2 (the directory contributes an empty one)", len(loaded.Sets))
	}
	if got := len(loaded.Sets[0].Set.Ranges); got != 1 {
		t.Fatalf("regular entry contributed %d ranges, want 1", got)
	}
	if got := len(loaded.Sets[1].Set.Ranges); got != 0 {
		t.Fatalf("directory entry contributed %d ranges, want 0", got)
	}
	// A list whose only entry is a directory yields one empty set, and the
	// "No valid files found" error belongs to @directory, not to @list.
	onlyDir := filepath.Join(dir, "only")
	if err := os.WriteFile(onlyDir, []byte(dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o = DefaultOptions()
	o.Sources = []SourceSpec{{Kind: SourceFileList, Arg: onlyDir}}
	loaded, err = loadAllImpl(o, emptyReader{})
	if err != nil {
		t.Fatalf("@list with only a directory entry = %v, want no error", err)
	}
	if len(loaded.Sets) != 1 || len(loaded.Sets[0].Set.Ranges) != 0 {
		t.Fatalf("@list with only a directory entry loaded %+v, want one empty set", loaded.Sets)
	}
}

// emptyReader stands in for stdin when no source reads it.
type emptyReader struct{}

func (emptyReader) Read(p []byte) (int, error) { return 0, os.ErrClosed }

// G5: in IPv6 mode C re-scans argv and skips every dash-prefixed argument
// other than "-", so an unrecognized option is neither an input nor an
// error; the IPv4 main has no such skip and opens it as a file.
func TestUnrecognizedDashArgumentsByFamily(t *testing.T) {
	v6 := DefaultOptions()
	v6.Family = V6
	for _, arg := range []string{"-c", "--bogus-opt", "--", "--bogus"} {
		addInputArg(v6, arg)
	}
	if len(v6.Sources) != 0 {
		t.Fatalf("IPv6 sources = %+v, want none: C skips unrecognized dash arguments", v6.Sources)
	}
	addInputArg(v6, "-")
	if len(v6.Sources) != 1 || v6.Sources[0].Arg != "" {
		t.Fatalf(`IPv6 "-" = %+v, want stdin`, v6.Sources)
	}

	v4 := DefaultOptions()
	addInputArg(v4, "-c")
	if len(v4.Sources) != 1 || v4.Sources[0].Arg != "-c" {
		t.Fatalf(`IPv4 "-c" = %+v, want it opened as a file like C does`, v4.Sources)
	}

	// The skip is decided by the family active at that argument, so a
	// dash argument seen before -6 is still a file.
	mixed := DefaultOptions()
	addInputArg(mixed, "-c")
	mixed.Family = V6
	addInputArg(mixed, "--bogus")
	if len(mixed.Sources) != 1 || mixed.Sources[0].Arg != "-c" {
		t.Fatalf("pre--ipv6 dash argument = %+v, want only %q loaded as a file", mixed.Sources, "-c")
	}
}

// captureRun runs the legacy entry point with args and returns the exit
// code with the bytes it wrote to stdout and stderr.
func captureRun(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	inFile, err := os.CreateTemp(t.TempDir(), "w8-in")
	if err != nil {
		t.Fatal(err)
	}
	defer inFile.Close()
	if _, err := inFile.Write([]byte(stdin)); err != nil {
		t.Fatal(err)
	}
	if _, err := inFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	outFile, err := os.CreateTemp(t.TempDir(), "w8-out")
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	errFile, err := os.CreateTemp(t.TempDir(), "w8-err")
	if err != nil {
		t.Fatal(err)
	}
	defer errFile.Close()
	os.Stdin, os.Stdout, os.Stderr = inFile, outFile, errFile
	rc := Run("iprange", args)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	out, err := os.ReadFile(outFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	errs, err := os.ReadFile(errFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	return rc, string(out), string(errs)
}

// G5: C guards every value-taking option with `i+1 < argc`, so a trailing
// option token is not an option and falls through to the input path.
func TestTrailingValueOptionIsAnInput(t *testing.T) {
	opts := []string{"--min-prefix", "--prefixes", "--default-prefix", "-p",
		"--dns-threads", "--print-prefix", "--print-prefix-ips", "--print-prefix-nets",
		"--print-suffix", "--print-suffix-ips", "--print-suffix-nets",
		"--ipset-reduce", "--reduce-factor", "--ipset-reduce-entries", "--reduce-entries"}
	for _, opt := range opts {
		if !optionTakesValue(opt) {
			t.Fatalf("%s is not listed as value-taking, but C consumes its next argument", opt)
		}
		// IPv4: the token becomes an input path that cannot be opened.
		rc, out, errs := captureRun(t, "", opt)
		wantErr := "iprange: " + opt + " - No such file or directory\niprange: Cannot load ipset: " + opt + "\n"
		if rc != 1 || out != "" || errs != wantErr {
			t.Fatalf("trailing %s = rc %d out %q err %q; want rc 1, empty stdout, %q",
				opt, rc, out, errs, wantErr)
		}
		// IPv6: the same token is skipped by the re-scan, so stdin is the
		// input and the run succeeds.
		rc, out, errs = captureRun(t, "::/0\n", "-6", opt)
		if rc != 0 || out != "::/0\n" || errs != "" {
			t.Fatalf("trailing -6 %s = rc %d out %q err %q; want rc 0 and no output", opt, rc, out, errs)
		}
	}
	for _, opt := range []string{"--merge", "-6", "-1", "--quiet", "as", "-"} {
		if optionTakesValue(opt) {
			t.Fatalf("%s must not be listed as value-taking", opt)
		}
	}
}

// G5: the "ipset is needed before --diff/--except/--compare-next" guard is
// IPv4-only, and its diagnostic bytes name the canonical option.
//
// The guard ends the process (C calls exit(1)), so the observable bytes can
// only be captured from a child process: the test re-executes itself with
// an environment marker and checks the child exit status, stdout and stderr.
func TestPriorIpsetGuardDiagnostics(t *testing.T) {
	const modeEnv = "W8_LEGACY_PRIOR_GUARD_MODE"
	const optEnv = "W8_LEGACY_PRIOR_GUARD_OPTION"
	if mode := os.Getenv(modeEnv); mode != "" {
		o := DefaultOptions()
		if mode == "v6" {
			o.Family = V6
		}
		requirePriorFile(o, os.Getenv(optEnv))
		return
	}
	cases := []struct {
		mode, option, wantErr string
		wantRC                int
	}{
		{"v4", "--except", "iprange: An ipset is needed before --except\n", 1},
		{"v4", "--diff", "iprange: An ipset is needed before --diff\n", 1},
		{"v4", "--compare-next", "iprange: An ipset is needed before --compare-next\n", 1},
		// C skips this guard entirely once the family is IPv6
		// (active_family != 6 at src/iprange.c:615,623,640); the run fails
		// later with "No valid ipsets to process." instead.
		{"v6", "--diff", "", 0},
		{"v6", "--except", "", 0},
		{"v6", "--compare-next", "", 0},
	}
	for _, tc := range cases {
		cmd := exec.Command(os.Args[0], "-test.run=TestPriorIpsetGuardDiagnostics", "-test.v=false")
		cmd.Env = append(os.Environ(), modeEnv+"="+tc.mode, optEnv+"="+tc.option)
		// The child is still a test binary, so its own PASS line is not
		// part of the CLI surface: only the exit status and stderr are.
		var errs bytes.Buffer
		cmd.Stdout, cmd.Stderr = io.Discard, &errs
		err := cmd.Run()
		rc := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			rc = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: child did not run: %v", tc.option, err)
		}
		if rc != tc.wantRC || errs.String() != tc.wantErr {
			t.Fatalf("requirePriorFile(%s, %s) = rc %d stderr %q; want rc %d, stderr %q",
				tc.mode, tc.option, rc, errs.String(), tc.wantRC, tc.wantErr)
		}
	}
}

func TestLookupLegacyHostKeepsIPv6MappedForm(t *testing.T) {
	if !numericFormsAnswerHere {
		t.Skip("scoped, not run: the numeric form is refused on this platform; " +
			"see TestDNSNumericFormsAreNotAnsweredHere")
	}
	ips, err := lookupLegacyHost("ip", "0x0A000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("::ffff:10.0.0.1")) {
		t.Fatalf("IPv6 lookup of a numeric form = %v, want ::ffff:10.0.0.1", ips)
	}
}
