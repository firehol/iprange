// Parity of the legacy CLI's numeric option values with the C reference.
//
// C parses --min-prefix, --default-prefix/-p and --dns-threads with
// parse_long_option_or_die() (src/iprange.c:452-463): strtol(3) base 10, so
// leading whitespace and an optional sign are accepted, and a value is valid
// only when it is fully consumed and lies inside the option's [min, max].
// The unsigned reduce options use parse_size_option_or_die()
// (src/iprange.c:465-479), which first tests that the value starts with a
// digit and therefore must reject a sign. Every expectation below was taken
// from the C binary.

package legacy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// strtol10 is the owner of the strtol grammar the signed numeric options
// rely on. It returns the value and the number of bytes consumed; a string
// with no digits consumes nothing, which is how the caller detects C's
// endptr == value.
func TestStrtol10Grammar(t *testing.T) {
	const maxLong = int64(^uint64(0) >> 1)
	cases := []struct {
		in   string
		want int64
		used int
	}{
		{"0", 0, 1},
		{"32", 32, 2},
		{"032", 32, 3},
		{"+5", 5, 2},
		{"-5", -5, 2},
		{"+0", 0, 2},
		{"-0", 0, 2},
		{" 5", 5, 2},
		{"\t5", 5, 2},
		{"\n5", 5, 2},
		{" 128", 128, 4},
		{"+2147483647", 2147483647, 11},
		// Trailing junk stops the parse; the caller rejects the value.
		{"5 ", 5, 1},
		{"5x", 5, 1},
		{"0x10", 0, 1},
		{"1,2", 1, 1},
		// C sets endptr back to the start when it converts nothing.
		{"", 0, 0},
		{" ", 0, 0},
		{"+", 0, 0},
		{"-", 0, 0},
		{"  ", 0, 0},
		{"abc", 0, 0},
		{",", 0, 0},
		// Out of long range saturates, exactly as C's strtol does before
		// the caller's bound test rejects it.
		{"9223372036854775807", maxLong, 19},
		{"9223372036854775808", maxLong, 19},
		{"99999999999999999999", maxLong, 20},
		{"-9223372036854775808", -maxLong - 1, 20},
		{"-99999999999999999999", -maxLong - 1, 21},
	}
	for _, tc := range cases {
		got, used := strtol10(tc.in)
		if got != tc.want || used != tc.used {
			t.Errorf("strtol10(%q) = (%d, %d), want (%d, %d)", tc.in, got, used, tc.want, tc.used)
		}
	}
}

// numericCLI is one invocation of the legacy option scanner plus the exact
// bytes C produces for it. rc 0 rows run in-process; a rejection exits the
// process, so those rows run in a re-executed copy of the test binary.
type numericCLI struct {
	name string
	args []string // argv after the program name; the input file is last
	rc   int
	out  string
	err  string
}

// numericChildEnv marks the re-executed copy of the test binary that runs one
// CLI invocation, so a rejected value (which exits) can be observed from
// outside the process that parsed it.
const numericChildEnv = "IPRANGE_LEGACY_NUMERIC_CHILD"

// The signed numeric options accept strtol's grammar, and every accepted
// spelling selects the same value as the plain digits. The rejected spellings
// carry the per-option C diagnostic.
func TestNumericOptionSpellingsMatchC(t *testing.T) {
	dir := t.TempDir()
	in4 := filepath.Join(dir, "in4")
	in6 := filepath.Join(dir, "in6")
	missing := filepath.Join(dir, "no-such-file")
	if err := os.WriteFile(in4, []byte("10.0.0.0/30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in6, []byte("2001:db8::/124\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		minPfx4 = "It must be between 1 and 32."
		minPfx6 = "It must be between 1 and 128."
		defPfx  = "It must be between 0 and 32."
		threads = "It must be an integer greater than or equal to 1."
	)
	bad := func(msg, option, value string) string {
		return "iprange: Invalid value '" + value + "' for " + option + ". " + msg + "\n"
	}

	cases := []numericCLI{
		// --min-prefix, IPv4 bound 1..32.
		{"min-prefix 30", []string{"--min-prefix", "30", in4}, 0, "10.0.0.0/30\n", ""},
		{"min-prefix +30", []string{"--min-prefix", "+30", in4}, 0, "10.0.0.0/30\n", ""},
		{"min-prefix ' 30'", []string{"--min-prefix", " 30", in4}, 0, "10.0.0.0/30\n", ""},
		{"min-prefix 030", []string{"--min-prefix", "030", in4}, 0, "10.0.0.0/30\n", ""},
		{"min-prefix +31 splits", []string{"--min-prefix", "+31", in4}, 0, "10.0.0.0/31\n10.0.0.2/31\n", ""},
		{"min-prefix ' 31' splits", []string{"--min-prefix", " 31", in4}, 0, "10.0.0.0/31\n10.0.0.2/31\n", ""},
		{"min-prefix +32 singles", []string{"--min-prefix", "+32", in4}, 0, "10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n", ""},
		{"min-prefix 0", []string{"--min-prefix", "0", in4}, 1, "", bad(minPfx4, "--min-prefix", "0")},
		{"min-prefix +0", []string{"--min-prefix", "+0", in4}, 1, "", bad(minPfx4, "--min-prefix", "+0")},
		{"min-prefix -0", []string{"--min-prefix", "-0", in4}, 1, "", bad(minPfx4, "--min-prefix", "-0")},
		{"min-prefix 33", []string{"--min-prefix", "33", in4}, 1, "", bad(minPfx4, "--min-prefix", "33")},
		{"min-prefix -1", []string{"--min-prefix", "-1", in4}, 1, "", bad(minPfx4, "--min-prefix", "-1")},
		{"min-prefix abc", []string{"--min-prefix", "abc", in4}, 1, "", bad(minPfx4, "--min-prefix", "abc")},
		{"min-prefix 30x", []string{"--min-prefix", "30x", in4}, 1, "", bad(minPfx4, "--min-prefix", "30x")},
		{"min-prefix ' 30 '", []string{"--min-prefix", " 30 ", in4}, 1, "", bad(minPfx4, "--min-prefix", " 30 ")},
		{"min-prefix 0x10", []string{"--min-prefix", "0x10", in4}, 1, "", bad(minPfx4, "--min-prefix", "0x10")},
		{"min-prefix empty", []string{"--min-prefix", "", in4}, 1, "", bad(minPfx4, "--min-prefix", "")},
		{"min-prefix overflow", []string{"--min-prefix", "99999999999999999999", in4}, 1, "", bad(minPfx4, "--min-prefix", "99999999999999999999")},
		{"min-prefix LONG_MAX", []string{"--min-prefix", "9223372036854775807", in4}, 1, "", bad(minPfx4, "--min-prefix", "9223372036854775807")},
		// Before -6 the IPv4 bound is the one that applies.
		{"min-prefix 100 -6", []string{"--min-prefix", "100", "-6", in6}, 1, "", bad(minPfx4, "--min-prefix", "100")},

		// --min-prefix, IPv6 bound 1..128 (src/iprange6_main.c:111-124).
		{"v6 min-prefix 128", []string{"-6", "--min-prefix", "128", in6}, 0, v6Singles, ""},
		{"v6 min-prefix +128", []string{"-6", "--min-prefix", "+128", in6}, 0, v6Singles, ""},
		{"v6 min-prefix ' 128'", []string{"-6", "--min-prefix", " 128", in6}, 0, v6Singles, ""},
		{"v6 min-prefix +1", []string{"-6", "--min-prefix", "+1", in6}, 0, "2001:db8::/124\n", ""},
		{"v6 min-prefix ' 1'", []string{"-6", "--min-prefix", " 1", in6}, 0, "2001:db8::/124\n", ""},
		{"v6 min-prefix 0", []string{"-6", "--min-prefix", "0", in6}, 1, "", bad(minPfx6, "--min-prefix", "0")},
		{"v6 min-prefix 129", []string{"-6", "--min-prefix", "129", in6}, 1, "", bad(minPfx6, "--min-prefix", "129")},
		{"v6 min-prefix -1", []string{"-6", "--min-prefix", "-1", in6}, 1, "", bad(minPfx6, "--min-prefix", "-1")},
		{"v6 min-prefix 128x", []string{"-6", "--min-prefix", "128x", in6}, 1, "", bad(minPfx6, "--min-prefix", "128x")},

		// --default-prefix and -p, IPv4 bound 0..32. The diagnostic names the
		// option token as it was typed.
		{"default-prefix 28", []string{"--default-prefix", "28", in4}, 0, "10.0.0.0/30\n", ""},
		{"default-prefix +28", []string{"--default-prefix", "+28", in4}, 0, "10.0.0.0/30\n", ""},
		{"default-prefix ' 28'", []string{"--default-prefix", " 28", in4}, 0, "10.0.0.0/30\n", ""},
		{"default-prefix +0", []string{"--default-prefix", "+0", in4}, 0, "10.0.0.0/30\n", ""},
		{"default-prefix -0", []string{"--default-prefix", "-0", in4}, 0, "10.0.0.0/30\n", ""},
		{"default-prefix 33", []string{"--default-prefix", "33", in4}, 1, "", bad(defPfx, "--default-prefix", "33")},
		{"default-prefix abc", []string{"--default-prefix", "abc", in4}, 1, "", bad(defPfx, "--default-prefix", "abc")},
		{"default-prefix ' 28 '", []string{"--default-prefix", " 28 ", in4}, 1, "", bad(defPfx, "--default-prefix", " 28 ")},
		{"default-prefix empty", []string{"--default-prefix", "", in4}, 1, "", bad(defPfx, "--default-prefix", "")},
		{"-p +28", []string{"-p", "+28", in4}, 0, "10.0.0.0/30\n", ""},
		{"-p ' 28'", []string{"-p", " 28", in4}, 0, "10.0.0.0/30\n", ""},
		{"-p 33", []string{"-p", "33", in4}, 1, "", bad(defPfx, "-p", "33")},
		{"-p abc", []string{"-p", "abc", in4}, 1, "", bad(defPfx, "-p", "abc")},
		// The family active at that point in argv decides the bound.
		{"default-prefix 33 -6", []string{"--default-prefix", "33", "-6", in6}, 1, "", bad(defPfx, "--default-prefix", "33")},

		// --dns-threads, bound 1..INT_MAX, with no family guard
		// (src/iprange.c:699-701).
		{"dns-threads +1", []string{"--dns-threads", "+1", in4}, 0, "10.0.0.0/30\n", ""},
		{"dns-threads ' 1'", []string{"--dns-threads", " 1", in4}, 0, "10.0.0.0/30\n", ""},
		{"dns-threads INT_MAX", []string{"--dns-threads", "2147483647", in4}, 0, "10.0.0.0/30\n", ""},
		{"dns-threads +INT_MAX", []string{"--dns-threads", "+2147483647", in4}, 0, "10.0.0.0/30\n", ""},
		{"dns-threads 0", []string{"--dns-threads", "0", in4}, 1, "", bad(threads, "--dns-threads", "0")},
		{"dns-threads +0", []string{"--dns-threads", "+0", in4}, 1, "", bad(threads, "--dns-threads", "+0")},
		{"dns-threads 2147483648", []string{"--dns-threads", "2147483648", in4}, 1, "", bad(threads, "--dns-threads", "2147483648")},
		{"dns-threads LONG_MAX", []string{"--dns-threads", "9223372036854775807", in4}, 1, "", bad(threads, "--dns-threads", "9223372036854775807")},
		{"v6 dns-threads +1", []string{"-6", "--dns-threads", "+1", in6}, 0, "2001:db8::/124\n", ""},
		{"v6 dns-threads 0", []string{"-6", "--dns-threads", "0", in6}, 1, "", bad(threads, "--dns-threads", "0")},

		// Once IPv6 is active --default-prefix/-p is skipped without being
		// parsed (src/iprange.c:563, src/iprange6_main.c:152-157) and above
		// all it must not fall through as an input file.
		{"v6 default-prefix 999", []string{"-6", "--default-prefix", "999", in6}, 0, "2001:db8::/124\n", ""},
		{"v6 default-prefix abc", []string{"-6", "--default-prefix", "abc", in6}, 0, "2001:db8::/124\n", ""},
		{"v6 default-prefix missing", []string{"-6", "--default-prefix", missing, in6}, 0, "2001:db8::/124\n", ""},
		{"v6 -p missing", []string{"-6", "-p", missing, in6}, 0, "2001:db8::/124\n", ""},

		// Controls: the unsigned reduce parser must keep rejecting a sign and
		// whitespace (src/iprange.c:468).
		{"reduce-entries 5", []string{"--reduce-entries", "5", in4}, 0, "10.0.0.0/30\n", ""},
		{"reduce-entries +5", []string{"--reduce-entries", "+5", in4}, 1, "",
			"iprange: Invalid value '+5' for --reduce-entries. It must be a non-negative integer.\n"},
		{"reduce-entries ' 5'", []string{"--reduce-entries", " 5", in4}, 1, "",
			"iprange: Invalid value ' 5' for --reduce-entries. It must be a non-negative integer.\n"},
		{"reduce-factor +5", []string{"--reduce-factor", "+5", in4}, 1, "",
			"iprange: Invalid value '+5' for --reduce-factor. It must be a non-negative integer percentage.\n"},
		{"ipset-reduce-entries -1", []string{"--ipset-reduce-entries", "-1", in4}, 1, "",
			"iprange: Invalid value '-1' for --ipset-reduce-entries. It must be a non-negative integer.\n"},
	}

	if idx := os.Getenv(numericChildEnv); idx != "" {
		var args []string
		raw, err := base64.StdEncoding.DecodeString(idx)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			panic(err)
		}
		os.Exit(Run("iprange", args))
	}

	// Every row runs in a child: C's option parser exits the process on a
	// rejected value, so the status and the bytes are only observable from
	// outside the process that parsed them. The child calls os.Exit, so it
	// writes nothing but the CLI's own output.
	for _, tc := range cases {
		raw, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestNumericOptionSpellingsMatchC", "-test.v=false")
		cmd.Env = append(os.Environ(), numericChildEnv+"="+base64.StdEncoding.EncodeToString(raw))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.Stdin = bytes.NewReader(nil)
		runErr := cmd.Run()
		rc := 0
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			rc = exitErr.ExitCode()
		} else if runErr != nil {
			t.Fatalf("%s: child did not run: %v", tc.name, runErr)
		}
		if rc != tc.rc || stdout.String() != tc.out || stderr.String() != tc.err {
			t.Errorf("%s = rc %d out %q err %q; want rc %d, out %q, err %q",
				tc.name, rc, stdout.String(), stderr.String(), tc.rc, tc.out, tc.err)
		}
	}
}

// v6Singles is the /124 expressed as single addresses, which is what C prints
// when --min-prefix disables every prefix down to 128.
const v6Singles = "2001:db8::\n2001:db8::1\n2001:db8::2\n2001:db8::3\n" +
	"2001:db8::4\n2001:db8::5\n2001:db8::6\n2001:db8::7\n" +
	"2001:db8::8\n2001:db8::9\n2001:db8::a\n2001:db8::b\n" +
	"2001:db8::c\n2001:db8::d\n2001:db8::e\n2001:db8::f\n"
