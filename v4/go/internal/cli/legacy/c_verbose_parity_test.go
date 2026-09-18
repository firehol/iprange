// Byte-exact -v diagnostics, byte-exact names, and the binary-format
// validation messages of the legacy CLI, against the released C tool.
//
// The three classes pinned here are the ones the external review ruled
// existing compatibility requirements:
//
//  1. Verbose content. The C owns specific lines: "Optimizing combined
//     ipset" when the printed set is not yet optimized
//     (src/ipset_print.c:140-142 calls ipset_optimize, which logs at
//     src/ipset_optimize.c:43,47), "Printing <group-A name> with ..."
//     because ipset_exclude() names its result after its first operand
//     (src/ipset_exclude.c:23), one "Finding common IPs in A and B" for
//     the compare modes because the released build defines
//     COMPARE_WITH_COMMON (CMakeLists.txt:76, configure.ac:99,
//     src/iprange.c:1053,1096,1138), "NON-OPTIMIZED ..." at the ordering
//     transition (src/ipset.h:107-121, src/ipset6.h:100-106), and
//     "Enabling prefix N" per --prefixes token (src/iprange.c:549, with no
//     "iprange: " prefix). The IPv6 twin prints none of the load
//     bookkeeping lines: src/iprange6_main.c has no debug statement and
//     src/ipset6_load.c only logs "Loading from ... (IPv6 mode)".
//
//  2. Name bytes. A POSIX file name is any sequence of non-NUL, non-slash
//     bytes, and the C echoes it with %s, so a name holding 0xFF must reach
//     stderr as those bytes.
//
//  3. Binary input. The v1/v2 header validators echo the source label and
//     the offending header value (src/ipset_binary.c, src/ipset6_binary.c),
//     and every ipset_load() failure is followed by the caller's context
//     line "Cannot load ipset: <name>" (src/iprange.c:911,
//     src/iprange6_main.c:320).
//
// Each expectation is the C's own rc/stdout/stderr, measured over a fixture
// directory that the test recreates byte for byte, so the pinned names are
// relative and independent of the scratch path.
//
// The one comparison exception is the C's exit-time timing line, which
// reports this process's own wall clock and cannot be reproduced. It is
// handled per line, not by normalization: isWallclockLine accepts only a
// line that is exactly "completed in <d>.<5 digits> seconds (read <d>.<5
// digits> + think <d>.<5 digits> + speak <d>.<5 digits>)", and each pinned
// case states where that single line belongs (the IPv6 twin prints no timing
// line at all). A missing, duplicated, misplaced, or differently shaped
// timing line fails the case.

package legacy

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cReference is the released C tool, when one is installed.
const cReference = "/usr/bin/iprange"

// isWallclockLine reports whether line is exactly the C's exit-time timing
// line (src/iprange.c:1216): "completed in %0.5f seconds (read %0.5f +
// think %0.5f + speak %0.5f)". The four durations are wall-clock
// measurements of the running process, so no engine can reproduce them.
//
// The test is deliberately exact - the literal prefix, the literal
// separators, and one unsigned decimal with exactly five fraction digits at
// each of the four positions (%0.5f always emits five) - anchored to the
// whole line. Nothing else matches, which is what keeps this a per-line
// exception rather than an output normalization.
func isWallclockLine(line string) bool {
	rest, ok := strings.CutPrefix(line, "completed in ")
	if !ok {
		return false
	}
	for _, sep := range []string{" seconds (read ", " + think ", " + speak "} {
		var decimal string
		decimal, rest, ok = cutDecimal(rest)
		if !ok || decimal == "" {
			return false
		}
		rest, ok = strings.CutPrefix(rest, sep)
		if !ok {
			return false
		}
	}
	if decimal, tail, ok := cutDecimal(rest); !ok || decimal == "" || tail != ")" {
		return false
	}
	return true
}

// cutDecimal splits one "<digits>.<5 digits>" off the front of s, as C
// %0.5f prints it.
func cutDecimal(s string) (decimal, rest string, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) || s[i] != '.' {
		return "", s, false
	}
	j := i + 1
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j-i-1 != 5 {
		return "", s, false
	}
	return s[:j], s[j:], true
}

// maskWallclock replaces every timing line by the literal "<WALLCLOCK>"
// marker. The comparison is otherwise byte for byte.
func maskWallclock(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if isWallclockLine(line) {
			lines[i] = "<WALLCLOCK>"
		}
	}
	return strings.Join(lines, "\n")
}

// The C run ends the process (C calls exit()), so the observable bytes can
// only be captured from a child process: the test re-executes itself with an
// environment marker holding the argv to run, and the parent compares the
// child exit status, stdout and stderr.
// An environment value may not hold NUL, and an argument may hold any
// other byte, so the child receives its argv as one base64 token per
// argument.
const cRunArgsEnv = "W13_LEGACY_C_PARITY_ARGV"

func encodeArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = base64.StdEncoding.EncodeToString([]byte(a))
	}
	return strings.Join(parts, " ")
}

func decodeArgv(spec string) []string {
	if spec == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(spec, " ") {
		b, err := base64.StdEncoding.DecodeString(part)
		if err != nil {
			return nil
		}
		out = append(out, string(b))
	}
	return out
}

func runLegacyChild(t *testing.T, dir string, argv []string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestLegacyChildRunsOneCase", "-test.v=false")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), cRunArgsEnv+"="+encodeArgv(argv))
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), out.String(), errs.String()
		}
		t.Fatalf("%v: child did not run: %v", argv, err)
	}
	return 0, out.String(), errs.String()
}

// TestLegacyChildRunsOneCase is the child side of runLegacyChild. It is not
// a case of its own.
func TestLegacyChildRunsOneCase(t *testing.T) {
	spec, ok := os.LookupEnv(cRunArgsEnv)
	if !ok {
		t.Skip("child mode not requested")
	}
	argv := decodeArgv(spec)
	if argv == nil {
		t.Fatal("the child received no argv")
	}
	os.Exit(Run("iprange", argv))
}

// cOracleUnavailable states, in the case's own log, that the comparison
// against the released C tool cannot run here, and reports whether that is
// so.
//
// The C CLI has no native Windows build, so on that host the reference leg
// of every parity case is unavailable rather than passed. The case is not
// skipped: the engine's own pinned bytes above it are what decide it, and
// they were already compared before this point. The marker is a fixed
// prefix so a report can count the unavailable legs on a host instead of
// having to infer them from the absence of failures.
func cOracleUnavailable(t *testing.T) bool {
	t.Helper()
	if _, err := os.Stat(cReference); err == nil {
		return false
	}
	t.Logf("C-ORACLE-UNAVAILABLE %s", cOracleStatus())
	return true
}

// runCChild runs the released C tool with cwd pinned to the fixture
// directory, so the names in its diagnostics are the relative ones the
// expectations pin.
func runCChild(t *testing.T, dir string, argv []string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(cReference, argv...)
	cmd.Dir = dir
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), out.String(), errs.String()
		}
		t.Fatalf("%v: C reference did not run: %v", argv, err)
	}
	return 0, out.String(), errs.String()
}

// cFixture mirrors the directory the expectations were measured over. The
// binary payloads are the C's own --print-binary output, so a validation
// case tests the released format rather than a reconstruction of it.
func writeFixtures(t *testing.T, dir string) {
	t.Helper()
	for name, payload := range cFixtures {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, name := range []string{"emptydir", "emptydir6"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("create the empty directory %s: %v", name, err)
		}
	}
}

// assertCParity checks one measured contract: the engine's rc, stdout and
// masked stderr must equal the pinned bytes, and the C reference - when it
// is installed - must agree with the same pinned bytes.
func assertCParity(t *testing.T, dir string, c cCase) {
	t.Helper()
	rc, out, errs := runLegacyChild(t, dir, c.argv)
	masked := maskWallclock(errs)
	if rc != c.rc {
		t.Errorf("%s: rc = %d, want %d (stderr %q)", c.label, rc, c.rc, errs)
	}
	if out != c.stdout {
		t.Errorf("%s: stdout = %q, want %q", c.label, out, c.stdout)
	}
	if masked != c.stderr {
		t.Errorf("%s: stderr = %q, want %q", c.label, masked, c.stderr)
	}
	if cOracleUnavailable(t) {
		return
	}
	crc, cout, cerrs := runCChild(t, dir, c.argv)
	if crc != c.rc || cout != c.stdout || maskWallclock(cerrs) != c.stderr {
		t.Errorf("%s: the pinned expectation drifted from %s:\n got rc %d out %q err %q\nwant rc %d out %q err %q",
			c.label, cReference, crc, cout, maskWallclock(cerrs), c.rc, c.stdout, c.stderr)
	}
}

func runCParityCases(t *testing.T, cases []cCase) {
	t.Helper()
	dir := t.TempDir()
	writeFixtures(t, dir)
	for _, c := range cases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			// The cases run in one process against one fixture
			// directory; the child does not modify it.
			assertCParity(t, dir, c)
		})
	}
}

func TestVerboseDiagnosticsIPv4MatchC(t *testing.T) { runCParityCases(t, cCasesVerboseIpv4) }

func TestVerboseDiagnosticsIPv6MatchC(t *testing.T) { runCParityCases(t, cCasesVerboseIpv6) }

func TestOperationDiagnosticsCarryNameBytes(t *testing.T) {
	runCParityCases(t, cCasesNamesWithInvalidBytes)
}

func TestBinaryInputDiagnosticsMatchC(t *testing.T) { runCParityCases(t, cCasesBinaryValidation) }

// The pinned cases only mean something if the fixture directory is the one
// that was measured, including the names that are not valid UTF-8 and the
// directories that hold no files.
func TestCParityFixtureSetIsComplete(t *testing.T) {
	dir := t.TempDir()
	writeFixtures(t, dir)
	for _, name := range []string{"one.iprange", "dir/a.txt", "dir/z\xffy.txt",
		"bad\xffname.iprange", "v1\xff.bin", "d_badflag.bin", "d6_badfamily.bin"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("fixture %q: %v", name, err)
		}
	}
	for _, name := range []string{"emptydir", "emptydir6"} {
		entries, err := os.ReadDir(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(entries) != 0 {
			t.Errorf("%s is not empty", name)
		}
	}
}

// The wall-clock pattern is the only comparison exception, so its exact
// shape is pinned here: the accepted form, and the near misses that must
// still be reported as divergences.
func TestIsWallclockLineShape(t *testing.T) {
	accepted := []string{
		"completed in 0.00005 seconds (read 0.00004 + think 0.00000 + speak 0.00001)",
		"completed in 123456.50000 seconds (read 0.00000 + think 0.00000 + speak 0.00000)",
		"completed in 0.00000 seconds (read 0.00004 + think 0.00000 + speak 0.00001)",
	}
	rejected := []string{
		"",
		"completed in 0.0000 seconds (read 0.00004 + think 0.00000 + speak 0.00001)",
		"completed in 0.000005 seconds (read 0.00004 + think 0.00000 + speak 0.00001)",
		"completed in 0 seconds (read 0.00004 + think 0.00000 + speak 0.00001)",
		"iprange: completed in 0.00000 seconds (read 0.00004 + think 0.00000 + speak 0.00001)",
		"completed in 0.00000 seconds (read 0.00004 + think 0.00000)",
		"completed in 0.00000 seconds (read 0.00004 + think 0.00000 + speak 0.00001) ",
		"printing 0.00000",
	}
	for _, line := range accepted {
		if !isWallclockLine(line) {
			t.Errorf("accepted line rejected: %q", line)
		}
	}
	for _, line := range rejected {
		if isWallclockLine(line) {
			t.Errorf("rejected line accepted: %q", line)
		}
	}
	if got := maskWallclock("a\ncompleted in 0.00000 seconds (read 0.00000 + think 0.00000 + speak 0.00000)\nb\n"); got != "a\n<WALLCLOCK>\nb\n" {
		t.Errorf("maskWallclock = %q", got)
	}
}

type cCase struct {
	label          string
	argv           []string
	rc             int
	stdout, stderr string
}

var cCasesVerboseIpv4 = []cCase{
	{
		label: "merge two sets",
		argv:  []string{"-v", "one.iprange", "two.iprange"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "merge three sets",
		argv:  []string{"-v", "one.iprange", "two.iprange", "three.iprange"},
		rc:    0, stdout: "10.0.0.0/29\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Merging two.iprange to combined ipset\niprange: Merging three.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /29 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "merge single set",
		argv:  []string{"-v", "one.iprange"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "union two sets",
		argv:  []string{"-v", "--union", "one.iprange", "two.iprange"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "reduce merged sets",
		argv:  []string{"-v", "--ipset-reduce", "20", "one.iprange", "two.iprange"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n\nCounting prefixes in combined ipset\nBreak down by prefix:\n\t- prefix /30 counts 1 entries\nTotal 1 entries generated\nAcceptable is to reach 16384 entries by reducing prefixes\n\tNothing more to reduce\n\nEliminated 0 out of 1 prefixes (1 remain in the final set).\n\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "except names result by group A",
		argv:  []string{"-v", "one.iprange", "--except", "two.iprange"},
		rc:    0, stdout: "10.0.0.0/31\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Removing IPs in two.iprange from one.iprange\niprange: Printing one.iprange with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "except three group A sets",
		argv:  []string{"-v", "one.iprange", "two.iprange", "--except", "two.iprange"},
		rc:    0, stdout: "10.0.0.0/31\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to one.iprange\niprange: Optimizing one.iprange\niprange: Removing IPs in two.iprange from one.iprange\niprange: Printing one.iprange with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "compare finds common ips",
		argv:  []string{"-v", "--compare", "one.iprange", "two.iprange"},
		rc:    0, stdout: "one.iprange,two.iprange,1,1,4,2,4,2\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "compare three sets",
		argv:  []string{"-v", "--compare", "one.iprange", "two.iprange", "three.iprange"},
		rc:    0, stdout: "one.iprange,two.iprange,1,1,4,2,4,2\none.iprange,three.iprange,1,1,4,4,8,0\ntwo.iprange,three.iprange,1,1,2,4,6,0\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Is already optimized three.iprange\niprange: Finding common IPs in one.iprange and two.iprange\niprange: Finding common IPs in one.iprange and three.iprange\niprange: Finding common IPs in two.iprange and three.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "compare first",
		argv:  []string{"-v", "--compare-first", "one.iprange", "two.iprange", "three.iprange"},
		rc:    0, stdout: "two.iprange,1,2,2\nthree.iprange,1,4,0\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Loading from three.iprange\niprange: Loaded optimized three.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Is already optimized three.iprange\niprange: Finding common IPs in two.iprange and one.iprange\niprange: Finding common IPs in three.iprange and one.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "compare next",
		argv:  []string{"-v", "one.iprange", "--compare-next", "two.iprange"},
		rc:    0, stdout: "one.iprange,two.iprange,1,1,4,2,4,2\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "common of two sets",
		argv:  []string{"-v", "--common", "one.iprange", "two.iprange"},
		rc:    0, stdout: "10.0.0.2/31\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding common IPs in one.iprange and two.iprange\niprange: Printing common with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "diff of two sets",
		argv:  []string{"-v", "one.iprange", "--diff", "two.iprange"},
		rc:    1, stdout: "10.0.0.0/31\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding diff IPs in one.iprange and two.iprange\niprange: Printing diff with 1 ranges, 2 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "count unique merged",
		argv:  []string{"-v", "--count-unique", "one.iprange", "two.iprange"},
		rc:    0, stdout: "1,4\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n<WALLCLOCK>\n",
	},
	{
		label: "count unique single set",
		argv:  []string{"-v", "--count-unique", "one.iprange"},
		rc:    0, stdout: "1,4\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "count unique all",
		argv:  []string{"-v", "--count-unique-all", "one.iprange", "two.iprange"},
		rc:    0, stdout: "one.iprange,1,4\ntwo.iprange,1,2\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized one.iprange\niprange: Is already optimized two.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "print binary merged",
		argv:  []string{"-v", "--print-binary", "one.iprange", "two.iprange"},
		rc:    0, stdout: "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 2\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\n<WALLCLOCK>\n",
	},
	{
		label: "print ranges merged",
		argv:  []string{"-v", "--print-ranges", "one.iprange", "two.iprange"},
		rc:    0, stdout: "10.0.0.0-10.0.0.3\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\ntotals: 2 lines read, 1 distinct IP ranges found, 0 CIDR prefixes, 1 ranges printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "enabling prefix announcements",
		argv:  []string{"-v", "--prefixes", "24,32", "one.iprange"},
		rc:    0, stdout: "10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n", stderr: "Enabling prefix 24\nEnabling prefix 32\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n4 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 4 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "enabling prefix duplicates",
		argv:  []string{"-v", "--prefixes", "24,24,8", "one.iprange"},
		rc:    0, stdout: "10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n", stderr: "Enabling prefix 24\nEnabling prefix 24\nEnabling prefix 8\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n4 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 4 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "dir expansion",
		argv:  []string{"-v", "@dir"},
		rc:    0, stdout: "8.8.8.8\n9.9.9.9\n", stderr: platformPin("iprange: Loading files from directory dir\niprange: Loading file dir/a.txt from directory dir\niprange: Loading from dir/a.txt\niprange: Loaded optimized dir/a.txt\niprange: Loading file dir/z\xffy.txt from directory dir\niprange: Loading from dir/z\xffy.txt\niprange: Loaded optimized dir/z\xffy.txt\niprange: Merging dir/z\xffy.txt to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n"),
	},
	{
		label: "list expansion",
		argv:  []string{"-v", "@list.txt"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "iprange: Loading files from list list.txt\niprange: Loading file one.iprange from list (line 1)\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Loading file two.iprange from list (line 2)\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "empty file reports",
		argv:  []string{"-v", "empty.iprange"},
		rc:    0, stdout: "", stderr: "iprange: Loading from empty.iprange\niprange: empty.iprange is empty\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "stdin",
		argv:  []string{"-v", "-"},
		rc:    0, stdout: "", stderr: "iprange: Loading from stdin\niprange: stdin is empty\niprange: Printing combined ipset with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "unsorted reports non-optimized",
		argv:  []string{"-v", "unsorted.iprange"},
		rc:    0, stdout: "1.1.1.1\n10.0.0.0/30\n10.0.0.8/30\n", stderr: "iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 3 ranges, 9 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 2 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 3 lines read, 3 distinct IP ranges found, 2 CIDR prefixes, 3 CIDRs printed, 9 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "unsorted merge",
		argv:  []string{"-v", "unsorted.iprange", "one.iprange"},
		rc:    0, stdout: "1.1.1.1\n10.0.0.0/30\n10.0.0.8/30\n", stderr: "iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Merging one.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 3 ranges, 9 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 2 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 4 lines read, 3 distinct IP ranges found, 2 CIDR prefixes, 3 CIDRs printed, 9 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "unsorted except names result",
		argv:  []string{"-v", "unsorted.iprange", "--except", "two.iprange"},
		rc:    0, stdout: "1.1.1.1\n10.0.0.0/31\n10.0.0.8/30\n", stderr: "iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Optimizing unsorted.iprange\niprange: Removing IPs in two.iprange from unsorted.iprange\niprange: Printing unsorted.iprange with 3 ranges, 7 unique IPs\n\n3 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 4 lines read, 3 distinct IP ranges found, 3 CIDR prefixes, 3 CIDRs printed, 7 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "unsorted compare",
		argv:  []string{"-v", "--compare", "unsorted.iprange", "two.iprange"},
		rc:    0, stdout: "unsorted.iprange,two.iprange,3,1,9,2,9,2\n", stderr: "iprange: Loading from unsorted.iprange\niprange: NON-OPTIMIZED unsorted.iprange at line 2, entry 1, last was 10.0.0.8 (167772168) - 10.0.0.11 (167772171), new is 10.0.0.0 (167772160) - 10.0.0.3 (167772163)\niprange: Loaded non-optimized unsorted.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Optimizing unsorted.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in unsorted.iprange and two.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "single ips",
		argv:  []string{"-v", "-1", "one.iprange"},
		rc:    0, stdout: "10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n", stderr: "iprange: Loading from one.iprange\niprange: Loaded optimized one.iprange\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 4 IPs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "ranges mode",
		argv:  []string{"-v", "-r", "one.iprange"},
		rc:    1, stdout: "", stderr: "iprange: -r - No such file or directory\niprange: Cannot load ipset: -r\n",
	},
}

var cCasesVerboseIpv6 = []cCase{
	{
		label: "v6 merge",
		argv:  []string{"-6", "-v", "one6.iprange", "two6.iprange"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n",
	},
	{
		label: "v6 merge three",
		argv:  []string{"-6", "-v", "one6.iprange", "two6.iprange", "three6.iprange"},
		rc:    0, stdout: "2001:db8::/125\n2001:db8:0:1::/64\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Loading from three6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Merging three6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 2 ranges, 18446744073709551624 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /64 counts 1 entries\n\t- prefix /125 counts 1 entries\n\ntotals: 3 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 18446744073709551624 unique IPs\n",
	},
	{
		label: "v6 single set",
		argv:  []string{"-6", "-v", "one6.iprange"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n",
	},
	{
		label: "v6 except names result",
		argv:  []string{"-6", "-v", "one6.iprange", "--except", "two6.iprange"},
		rc:    0, stdout: "2001:db8::4/126\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Removing IPs in two6.iprange from one6.iprange (IPv6)\niprange: Printing one6.iprange (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n",
	},
	{
		label: "v6 compare keeps combining",
		argv:  []string{"-6", "-v", "--compare", "one6.iprange", "two6.iprange"},
		rc:    0, stdout: "one6.iprange,two6.iprange,1,1,8,4,8,4\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Combining one6.iprange and two6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\n",
	},
	{
		label: "v6 compare first",
		argv:  []string{"-6", "-v", "--compare-first", "one6.iprange", "two6.iprange", "three6.iprange"},
		rc:    0, stdout: "two6.iprange,1,4,4\nthree6.iprange,1,18446744073709551616,0\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Loading from three6.iprange (IPv6 mode)\niprange: Combining two6.iprange and one6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\niprange: Combining three6.iprange and one6.iprange (IPv6)\niprange: Optimizing combined (IPv6)\n",
	},
	{
		label: "v6 common",
		argv:  []string{"-6", "-v", "--common", "one6.iprange", "two6.iprange"},
		rc:    0, stdout: "2001:db8::/126\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Finding common IPs in one6.iprange and two6.iprange (IPv6)\niprange: Printing common (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n",
	},
	{
		label: "v6 diff",
		argv:  []string{"-6", "-v", "one6.iprange", "--diff", "two6.iprange"},
		rc:    1, stdout: "2001:db8::4/126\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Finding diff IPs in one6.iprange and two6.iprange (IPv6)\niprange: Printing diff (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n",
	},
	{
		label: "v6 count unique merged",
		argv:  []string{"-6", "-v", "--count-unique", "one6.iprange", "two6.iprange"},
		rc:    0, stdout: "1,8\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\n",
	},
	{
		label: "v6 count unique single",
		argv:  []string{"-6", "-v", "--count-unique", "one6.iprange"},
		rc:    0, stdout: "1,8\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\n",
	},
	{
		label: "v6 count unique all",
		argv:  []string{"-6", "-v", "--count-unique-all", "one6.iprange", "two6.iprange"},
		rc:    0, stdout: "one6.iprange,1,8\ntwo6.iprange,1,4\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\n",
	},
	{
		label: "v6 print binary merged",
		argv:  []string{"-6", "-v", "--print-binary", "one6.iprange", "two6.iprange"},
		rc:    0, stdout: "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 2\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\n",
	},
	{
		label: "v6 prefixes silent",
		argv:  []string{"-6", "-v", "--prefixes", "24,128", "one6.iprange"},
		rc:    0, stdout: "2001:db8::\n2001:db8::1\n2001:db8::2\n2001:db8::3\n2001:db8::4\n2001:db8::5\n2001:db8::6\n2001:db8::7\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n8 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 8 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 8 CIDRs printed, 8 unique IPs\n",
	},
	{
		label: "v6 dir expansion silent",
		argv:  []string{"-6", "-v", "@dir6"},
		rc:    0, stdout: "2001:db8::/126\n", stderr: platformPin("iprange: Loading from dir6/a.txt (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /126 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n"),
	},
	{
		label: "v6 list expansion silent",
		argv:  []string{"-6", "-v", "@list6.txt"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "iprange: Loading from one6.iprange (IPv6 mode)\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Merging two6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n",
	},
	{
		label: "v6 empty dir",
		argv:  []string{"-6", "-v", "@emptydir6"},
		rc:    1, stdout: "", stderr: "iprange: No valid files found in directory: emptydir6\n",
	},
	{
		label: "v6 empty list",
		argv:  []string{"-6", "-v", "@emptylist.txt"},
		rc:    1, stdout: "", stderr: "iprange: No valid files found in file list: emptylist.txt\n",
	},
	{
		label: "v6 empty file silent",
		argv:  []string{"-6", "-v", "empty6.iprange"},
		rc:    0, stdout: "", stderr: "iprange: Loading from empty6.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n",
	},
	{
		label: "v6 stdin silent",
		argv:  []string{"-6", "-v", "-"},
		rc:    0, stdout: "", stderr: "iprange: Loading from stdin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 0 ranges, 0 unique IPs\n\n0 printed CIDRs, break down by prefix:\n\ntotals: 0 lines read, 0 distinct IP ranges found, 0 CIDR prefixes, 0 CIDRs printed, 0 unique IPs\n",
	},
	{
		label: "v6 binary load silent",
		argv:  []string{"-6", "-v", "v2.bin"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "iprange: Loading from v2.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n",
	},
	{
		label: "v6 unsorted non-optimized",
		argv:  []string{"-6", "-v", "unsorted6.iprange"},
		rc:    0, stdout: "2001:db8::/120\n", stderr: "iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 256 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /120 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 256 unique IPs\n",
	},
	{
		label: "v6 unsorted merge",
		argv:  []string{"-6", "-v", "unsorted6.iprange", "one6.iprange"},
		rc:    0, stdout: "2001:db8::/120\n", stderr: "iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Loading from one6.iprange (IPv6 mode)\niprange: Merging one6.iprange to combined ipset (IPv6)\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 1 ranges, 256 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /120 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 256 unique IPs\n",
	},
	{
		label: "v6 unsorted except",
		argv:  []string{"-6", "-v", "unsorted6.iprange", "--except", "two6.iprange"},
		rc:    0, stdout: "2001:db8::4/126\n2001:db8::8/125\n2001:db8::10/124\n2001:db8::20/123\n2001:db8::40/122\n2001:db8::80/121\n", stderr: "iprange: Loading from unsorted6.iprange (IPv6 mode)\niprange: NON-OPTIMIZED unsorted6.iprange at line 2, entry 1, last was 2001:db8:: - 2001:db8::ff, new is 2001:db8:: - 2001:db8::3\niprange: Loading from two6.iprange (IPv6 mode)\niprange: Optimizing unsorted6.iprange (IPv6)\niprange: Removing IPs in two6.iprange from unsorted6.iprange (IPv6)\niprange: Printing unsorted6.iprange (IPv6) with 1 ranges, 252 unique IPs\n\n6 printed CIDRs, break down by prefix:\n\t- prefix /121 counts 1 entries\n\t- prefix /122 counts 1 entries\n\t- prefix /123 counts 1 entries\n\t- prefix /124 counts 1 entries\n\t- prefix /125 counts 1 entries\n\t- prefix /126 counts 1 entries\n\ntotals: 3 lines read, 1 distinct IP ranges found, 6 CIDR prefixes, 6 CIDRs printed, 252 unique IPs\n",
	},
}

var cCasesNamesWithInvalidBytes = []cCase{
	{
		label: "merge names the bytes",
		argv:  []string{"-v", "bad\xffname.iprange", "two.iprange"},
		rc:    0, stdout: "1.2.3.4\n10.0.0.2/31\n", stderr: "iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Merging two.iprange to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 3 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 3 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "as label with 0xFF",
		argv:  []string{"-v", "bad\xffname.iprange", "as", "lab\xffel"},
		rc:    0, stdout: "1.2.3.4\n", stderr: "iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "except names the bytes",
		argv:  []string{"-v", "bad\xffname.iprange", "--except", "two.iprange"},
		rc:    0, stdout: "1.2.3.4\n", stderr: "iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Removing IPs in two.iprange from bad\xffname.iprange\niprange: Printing bad\xffname.iprange with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "compare names the bytes",
		argv:  []string{"-v", "--compare", "bad\xffname.iprange", "two.iprange"},
		rc:    0, stdout: "bad\xffname.iprange,two.iprange,1,1,1,2,3,0\n", stderr: "iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\xffname.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in bad\xffname.iprange and two.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "compare next names the bytes",
		argv:  []string{"-v", "bad\xffname.iprange", "--compare-next", "two.iprange"},
		rc:    0, stdout: "bad\xffname.iprange,two.iprange,1,1,1,2,3,0\n", stderr: "iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\xffname.iprange\niprange: Is already optimized two.iprange\niprange: Finding common IPs in bad\xffname.iprange and two.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "diff names the bytes",
		argv:  []string{"-v", "bad\xffname.iprange", "--diff", "two.iprange"},
		rc:    1, stdout: "1.2.3.4\n10.0.0.2/31\n", stderr: "iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Finding diff IPs in bad\xffname.iprange and two.iprange\niprange: Printing diff with 2 ranges, 3 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /31 counts 1 entries\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 2 CIDR prefixes, 2 CIDRs printed, 3 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "count unique all bytes",
		argv:  []string{"-v", "--count-unique-all", "bad\xffname.iprange", "two.iprange"},
		rc:    0, stdout: "bad\xffname.iprange,1,1\ntwo.iprange,1,2\n", stderr: "iprange: Loading from bad\xffname.iprange\niprange: Loaded optimized bad\xffname.iprange\niprange: Loading from two.iprange\niprange: Loaded optimized two.iprange\niprange: Is already optimized bad\xffname.iprange\niprange: Is already optimized two.iprange\n<WALLCLOCK>\n",
	},
	{
		label: "binary bytes in v4",
		argv:  []string{"-v", "v1\xff.bin"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "iprange: Loading from v1\xff.bin\niprange: Binary loaded optimized v1\xff.bin\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "binary bytes in v6",
		argv:  []string{"-6", "-v", "v2\xff.bin"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "iprange: Loading from v2\xff.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n",
	},
	{
		label: "v6 name bytes",
		argv:  []string{"-6", "-v", "bad6\xffname.iprange"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "iprange: Loading from bad6\xffname.iprange (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n",
	},
}

var cCasesBinaryValidation = []cCase{
	{
		label: "damaged flag line",
		argv:  []string{"d_badflag.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_badflag.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'.\niprange: Cannot fast load d_badflag.bin\niprange: Cannot load ipset: d_badflag.bin\n",
	},
	{
		label: "damaged flag line verbose",
		argv:  []string{"-v", "d_badflag.bin"},
		rc:    1, stdout: "", stderr: "iprange: Loading from d_badflag.bin\niprange: d_badflag.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'.\niprange: Cannot fast load d_badflag.bin\niprange: Cannot load ipset: d_badflag.bin\n",
	},
	{
		label: "damaged record size",
		argv:  []string{"d_badrecsize.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_badrecsize.bin: invalid record size 9 (expected 8)\niprange: Cannot fast load d_badrecsize.bin\niprange: Cannot load ipset: d_badrecsize.bin\n",
	},
	{
		label: "damaged record size verbose",
		argv:  []string{"-v", "d_badrecsize.bin"},
		rc:    1, stdout: "", stderr: "iprange: Loading from d_badrecsize.bin\niprange: d_badrecsize.bin: invalid record size 9 (expected 8)\niprange: Cannot fast load d_badrecsize.bin\niprange: Cannot load ipset: d_badrecsize.bin\n",
	},
	{
		label: "records not a number",
		argv:  []string{"d_badrecords.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_badrecords.bin: invalid records value 'abc\n'\niprange: Cannot fast load d_badrecords.bin\niprange: Cannot load ipset: d_badrecords.bin\n",
	},
	{
		label: "records overflow bound",
		argv:  []string{"d_hugerecords.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_hugerecords.bin: invalid number of records (18446744073709551615)\niprange: Cannot fast load d_hugerecords.bin\niprange: Cannot load ipset: d_hugerecords.bin\n",
	},
	{
		label: "bytes mismatch",
		argv:  []string{"d_badbytes.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_badbytes.bin invalid number of bytes, found 999, expected 12.\niprange: Cannot fast load d_badbytes.bin\niprange: Cannot load ipset: d_badbytes.bin\n",
	},
	{
		label: "bytes mismatch verbose",
		argv:  []string{"-v", "d_badbytes.bin"},
		rc:    1, stdout: "", stderr: "iprange: Loading from d_badbytes.bin\niprange: d_badbytes.bin invalid number of bytes, found 999, expected 12.\niprange: Cannot fast load d_badbytes.bin\niprange: Cannot load ipset: d_badbytes.bin\n",
	},
	{
		label: "lines below entries",
		argv:  []string{"d_badlines.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_badlines.bin: lines (0) cannot be less than entries (1)\niprange: Cannot fast load d_badlines.bin\niprange: Cannot load ipset: d_badlines.bin\n",
	},
	{
		label: "unique ips not a number",
		argv:  []string{"d_badunique.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_badunique.bin: invalid unique ips value 'qq\n'\niprange: Cannot fast load d_badunique.bin\niprange: Cannot load ipset: d_badunique.bin\n",
	},
	{
		label: "unique ips below entries",
		argv:  []string{"d_lowunique.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_lowunique.bin: unique IPs (0) cannot be less than entries (1)\niprange: Cannot fast load d_lowunique.bin\niprange: Cannot load ipset: d_lowunique.bin\n",
	},
	{
		label: "truncated payload",
		argv:  []string{"d_truncated.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_truncated.bin: expected to load 1 entries, loaded 0\niprange: Cannot fast load d_truncated.bin\niprange: Cannot load ipset: d_truncated.bin\n",
	},
	{
		label: "truncated payload verbose",
		argv:  []string{"-v", "d_truncated.bin"},
		rc:    1, stdout: "", stderr: "iprange: Loading from d_truncated.bin\niprange: d_truncated.bin: expected to load 1 entries, loaded 0\niprange: Cannot fast load d_truncated.bin\niprange: Cannot load ipset: d_truncated.bin\n",
	},
	{
		label: "trailing data",
		argv:  []string{"d_trailing.bin"},
		rc:    1, stdout: "", stderr: "iprange: d_trailing.bin: trailing data found after binary payload\niprange: Cannot fast load d_trailing.bin\niprange: Cannot load ipset: d_trailing.bin\n",
	},
	{
		label: "trailing data verbose",
		argv:  []string{"-v", "d_trailing.bin"},
		rc:    1, stdout: "", stderr: "iprange: Loading from d_trailing.bin\niprange: d_trailing.bin: trailing data found after binary payload\niprange: Cannot fast load d_trailing.bin\niprange: Cannot load ipset: d_trailing.bin\n",
	},
	{
		label: "bad endianness marker",
		argv:  []string{"d_nomarker.bin"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "",
	},
	{
		label: "v2 in v4 mode",
		argv:  []string{"v2.bin"},
		rc:    1, stdout: "", stderr: "iprange: v2.bin: IPv6 binary file cannot be loaded in IPv4 mode (use -6)\niprange: Cannot load ipset: v2.bin\n",
	},
	{
		label: "v1 in v6 mode",
		argv:  []string{"-6", "v1.bin"},
		rc:    1, stdout: "", stderr: "iprange: v1.bin: IPv4 binary file cannot be loaded in IPv6 mode\niprange: Cannot load ipset: v1.bin\n",
	},
	{
		label: "v6 damaged record size",
		argv:  []string{"-6", "d6_badrecsize.bin"},
		rc:    1, stdout: "", stderr: "iprange: d6_badrecsize.bin expected optimized flag but found 'record size 16\n'.\niprange: Cannot load binary v2 d6_badrecsize.bin\niprange: Cannot load ipset: d6_badrecsize.bin\n",
	},
	{
		label: "v6 damaged bytes",
		argv:  []string{"-6", "d6_badbytes.bin"},
		rc:    1, stdout: "", stderr: "iprange: d6_badbytes.bin expected records count but found 'bytes 5\n'.\niprange: Cannot load binary v2 d6_badbytes.bin\niprange: Cannot load ipset: d6_badbytes.bin\n",
	},
	{
		label: "v6 damaged bytes verbose",
		argv:  []string{"-6", "-v", "d6_badbytes.bin"},
		rc:    1, stdout: "", stderr: "iprange: Loading from d6_badbytes.bin (IPv6 mode)\niprange: d6_badbytes.bin expected records count but found 'bytes 5\n'.\niprange: Cannot load binary v2 d6_badbytes.bin\niprange: Cannot load ipset: d6_badbytes.bin\n",
	},
	{
		label: "v6 damaged unique ips",
		argv:  []string{"-6", "d6_badunique.bin"},
		rc:    1, stdout: "", stderr: "iprange: d6_badunique.bin expected lines count but found 'unique ips zz\n'.\niprange: Cannot load binary v2 d6_badunique.bin\niprange: Cannot load ipset: d6_badunique.bin\n",
	},
	{
		label: "v6 damaged family line",
		argv:  []string{"-6", "d6_badfamily.bin"},
		rc:    1, stdout: "", stderr: "iprange: d6_badfamily.bin expected family 'ipv6' but found 'family ipv4\n'.\niprange: Cannot load binary v2 d6_badfamily.bin\niprange: Cannot load ipset: d6_badfamily.bin\n",
	},
	{
		label: "v6 unique below entries",
		argv:  []string{"-6", "d6_lowunique.bin"},
		rc:    1, stdout: "", stderr: "iprange: d6_lowunique.bin expected lines count but found 'unique ips 0\n'.\niprange: Cannot load binary v2 d6_lowunique.bin\niprange: Cannot load ipset: d6_lowunique.bin\n",
	},
	{
		label: "damaged flag line bytes",
		argv:  []string{"bad\xffflag.bin"},
		rc:    1, stdout: "", stderr: "iprange: bad\xffflag.bin 2nd line should be the optimized flag, but found 'maybe-optimized\n'.\niprange: Cannot fast load bad\xffflag.bin\niprange: Cannot load ipset: bad\xffflag.bin\n",
	},
	{
		label: "damaged records bytes",
		argv:  []string{"bad\xffrecs.bin"},
		rc:    1, stdout: "", stderr: "iprange: bad\xffrecs.bin: invalid records value 'abc\n'\niprange: Cannot fast load bad\xffrecs.bin\niprange: Cannot load ipset: bad\xffrecs.bin\n",
	},
	{
		label: "v6 damaged bytes name bytes",
		argv:  []string{"-6", "d6bad\xffbytes.bin"},
		rc:    1, stdout: "", stderr: "iprange: d6bad\xffbytes.bin expected records count but found 'bytes 5\n'.\niprange: Cannot load binary v2 d6bad\xffbytes.bin\niprange: Cannot load ipset: d6bad\xffbytes.bin\n",
	},
	{
		label: "bad marker name bytes",
		argv:  []string{"bad\xffnomark.bin"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "",
	},
	{
		label: "valid v1 binary",
		argv:  []string{"v1.bin"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "",
	},
	{
		label: "valid v1 binary verbose",
		argv:  []string{"-v", "v1.bin"},
		rc:    0, stdout: "10.0.0.0/30\n", stderr: "iprange: Loading from v1.bin\niprange: Binary loaded optimized v1.bin\niprange: Printing combined ipset with 1 ranges, 4 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /30 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 4 unique IPs\n<WALLCLOCK>\n",
	},
	{
		label: "valid v2 binary",
		argv:  []string{"-6", "v2.bin"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "",
	},
	{
		// C prints the record buffer with %s, so an embedded NUL ends the
		// echo: nothing after it may reach stderr (src/ipset_load.c:343).
		label: "record echo stops at NUL",
		argv:  []string{"nulsp.iprange"},
		rc:    1, stdout: "", stderr: "iprange: Cannot understand line No 1 from nulsp.iprange: x y\niprange: Cannot load ipset: nulsp.iprange\n",
	},
	{
		label: "valid v2 binary verbose",
		argv:  []string{"-6", "-v", "v2.bin"},
		rc:    0, stdout: "2001:db8::/125\n", stderr: "iprange: Loading from v2.bin (IPv6 mode)\niprange: Printing combined ipset (IPv6) with 1 ranges, 8 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /125 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 8 unique IPs\n",
	},
}

// cFixtures recreates the measured fixture directory: every input
// file by its exact name bytes (some hold 0xFF). The binary
// payloads are the C's own --print-binary output.
var cFixtures = map[string]string{
	"one.iprange":          "10.0.0.0/30\n",
	"two.iprange":          "10.0.0.2/31\n",
	"three.iprange":        "10.0.0.4/30\n",
	"one6.iprange":         "2001:db8::/125\n",
	"two6.iprange":         "2001:db8::/126\n",
	"three6.iprange":       "2001:db8:0:1::/64\n",
	"empty.iprange":        "",
	"empty6.iprange":       "",
	"unsorted.iprange":     "10.0.0.8/30\n10.0.0.0/30\n1.1.1.1\n",
	"unsorted6.iprange":    "2001:db8::/120\n2001:db8::/126\n",
	"dir/a.txt":            "8.8.8.8\n",
	"dir/z\xffy.txt":       "9.9.9.9\n",
	"dir6/a.txt":           "2001:db8::/126\n",
	"list.txt":             "one.iprange\ntwo.iprange\n",
	"list6.txt":            "one6.iprange\ntwo6.iprange\n",
	"bad\xffname.iprange":  "1.2.3.4\n",
	"bad6\xffname.iprange": "2001:db8::/125\n",
	"emptylist.txt":        "# nothing\n",
	"v1.bin":               "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"v2.bin":               "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"v1\xff.bin":           "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"v2\xff.bin":           "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"bad\xffflag.bin":      "iprange binary format v1.0\nmaybe-optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"bad\xffnomark.bin":    "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"bad\xffrecs.bin":      "iprange binary format v1.0\noptimized\nrecord size 8\nrecords abc\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d6_badbytes.bin":      "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nbytes 5\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"d6_badfamily.bin":     "iprange binary format v2.0\nfamily ipv4\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"d6_badrecsize.bin":    "iprange binary format v2.0\nipv6\nrecord size 16\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"d6_badunique.bin":     "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nunique ips zz\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"d6_lowunique.bin":     "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nunique ips 0\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"d6_wrongheader.bin":   "BINARY-BROKEN\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"d6bad\xffbytes.bin":   "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nbytes 5\nbytes 36\nlines 1\nunique ips 8\nM<+\x1a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 \x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xb8\x0d\x01 ",
	"d_badbytes.bin":       "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 999\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_badflag.bin":        "iprange binary format v1.0\nmaybe-optimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_badlines.bin":       "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 0\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_badrecords.bin":     "iprange binary format v1.0\noptimized\nrecord size 8\nrecords abc\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_badrecsize.bin":     "iprange binary format v1.0\noptimized\nrecord size 9\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_badunique.bin":      "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips qq\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_hugerecords.bin":    "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 18446744073709551615\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_lowunique.bin":      "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 0\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_nomarker.bin":       "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"d_trailing.bin":       "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\njunk",
	"d_truncated.bin":      "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00",
	"d_wrongheader.bin":    "BINARY-BROKEN\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 4\nM<+\x1a\x00\x00\x00\n\x03\x00\x00\n",
	"nulsp.iprange":        "x y\x00z\n",
}
