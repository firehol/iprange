package legacy

import (
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This file gates the class that cost four review rounds: the Go answer
// tag, the Go refuse tag, and the Rust hex-pin cfg are three
// hand-maintained platform lists, and any of them can drift without a
// single test failing — target_os = "darwin" compiled out silently on
// macOS for two rounds because rustc only warns (rc 0) and Go never
// looks at the Rust file, and round 5 caught the deeper shape: Go's
// linux term is an UMBRELLA that also matches GOOS=android, so a
// literal term comparison passes while the EFFECTIVE build set diverges
// from the authority (bionic refuses; the emulation answered). The
// gates here therefore evaluate what actually compiles — via go/build,
// the toolchain's own constraint evaluator — not the spelled terms.

var (
	// rustCfgTargetOs matches target_os = "<value>" inside a cfg(...) attribute.
	rustCfgTargetOs = regexp.MustCompile(`target_os\s*=\s*"([a-z_]+)"`)
	// rustPinBlock anchors on the hex-pin's attribute chain:
	// #[cfg(any( ... ))] #[test] fn NAME — its contents define the
	// Rust answering set.
	rustPinBlock = regexp.MustCompile(`(?s)#\[cfg\(any\((.*?)\)\)\]\s*#\[test\]\s*fn hex_numeric_hostnames_answer_locally_where_the_authority_answers\b.*?\)`)
)

// goKnownGOOS is the universe the numeric split must partition: every
// GOOS the toolchain can target for this CLI. A platform omitted from
// this list escapes the partition gate; adding it (or Go gaining one)
// means appending here — the list mirrors go tool's known platforms.
var goKnownGOOS = []string{
	"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos",
	"ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "tvos",
	"wasip1", "watchos", "windows",
}

// compilesFor asks the toolchain's own //go:build evaluator whether the
// named file in this package dir compiles for GOOS (GOARCH fixed: none
// of the split files carry arch terms).
func compilesFor(t *testing.T, file, goos string) bool {
	t.Helper()
	ctxt := build.Context{GOOS: goos, GOARCH: "amd64"}
	match, err := ctxt.MatchFile(".", file)
	if err != nil {
		t.Fatalf("go/build MatchFile(%s, %s): %v", file, goos, err)
	}
	return match
}

// TestNumericSplitPartitionsKnownPlatforms evaluates the two files
// through go/build over every known GOOS and fails if any platform
// lands on BOTH halves or NEITHER. Aliasing (linux=>android,
// darwin=>ios) cannot hide here because the toolchain resolves it.
func TestNumericSplitPartitionsKnownPlatforms(t *testing.T) {
	var both, neither []string
	for _, goos := range goKnownGOOS {
		a := compilesFor(t, "dns_numeric_answer.go", goos)
		r := compilesFor(t, "dns_numeric_refuse.go", goos)
		switch {
		case a && r:
			both = append(both, goos)
		case !a && !r:
			neither = append(neither, goos)
		}
	}
	if len(both) > 0 || len(neither) > 0 {
		t.Errorf("numeric-form split does not partition Go's known platforms: compiled in BOTH halves %v; in NEITHER %v "+
			"(the answer/refuse //go:build expressions must be exact complements over every GOOS)", both, neither)
	}
}

// TestNumericAnswerMatchesDocumentedSet pins the answering set to its
// documented basis — the platforms whose cited resolvers answer — so a
// retag that quietly ADDS or DROPS a platform must also rewrite the
// evidence comment and this table in the same edit.
func TestNumericAnswerMatchesDocumentedSet(t *testing.T) {
	documentedAnswer := map[string]bool{
		"linux":     true, // glibc (C oracle) + musl (__inet_aton, strtoul base 0)
		"darwin":    true, // Libinfo _gai_numerichost -> _inet_aton_check
		"ios":       true, // same Libinfo resolver, one shared system library
		"tvos":      true, // idem
		"watchos":   true, // idem
		"freebsd":   true, // KAME explore_numeric -> inet_aton
		"netbsd":    true, // KAME explore_numeric -> inet_aton
		"dragonfly": true, // FreeBSD import + KAME explore_numeric
	}
	var goSet []string
	for _, goos := range goKnownGOOS {
		if compilesFor(t, "dns_numeric_answer.go", goos) {
			goSet = append(goSet, goos)
		}
	}
	var missing, extra []string
	for p := range documentedAnswer {
		found := false
		for _, g := range goSet {
			if g == p {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, p)
		}
	}
	for _, g := range goSet {
		if !documentedAnswer[g] {
			extra = append(extra, g)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		t.Errorf("effective answer set does not match the documented basis: documented-but-not-compiled %v; compiled-but-undocumented %v "+
			"(android refuses via bionic inet_pton and must NOT answer; a platform whose resolver gains/loses inet_aton needs source evidence, a comment rewrite, and this table in the same change)",
			missing, extra)
	}
}

// TestRustHexCfgMatchesGoAnswerSetCrossLanguage is the cross-language
// gate: the Rust hex-pin cfg set and Go's EFFECTIVE answer set must name
// the same platforms once the macos/darwin spelling is normalized. This
// is the detector for "one engine was retagged and the other silently
// was not" — the shape of the round-2 dead-"darwin" defect and of the
// round-5 android-umbrella divergence, neither visible to any single-
// platform leg.
func TestRustHexCfgMatchesGoAnswerSetCrossLanguage(t *testing.T) {
	// The test runs with the package directory as cwd; the Rust source is
	// four levels up in the same checkout. A checkout that ships only
	// v4/go legitimately cannot run this gate; it says so.
	rustPath := filepath.Join("..", "..", "..", "..", "rust", "iprange-cli", "src", "legacy", "dns.rs")
	src, err := os.ReadFile(rustPath)
	if err != nil {
		t.Skipf("cross-language tag gate scoped out: %s is not in this checkout (%v); "+
			"the conformance corpus expects the full v4 tree", rustPath, err)
	}
	m := rustPinBlock.FindStringSubmatch(string(src))
	if m == nil {
		t.Fatalf("%s: no cfg(any(...)) #[test] fn hex_numeric_hostnames_answer_locally_where_the_authority_answers — pin renamed or its attribute chain changed shape without updating this gate", rustPath)
	}
	cfgBlock := m[0]

	rustSet := map[string]bool{}
	for _, mm := range rustCfgTargetOs.FindAllStringSubmatch(cfgBlock, -1) {
		value := mm[1]
		if !rustTargetOsVocabulary[value] {
			t.Errorf("Rust cfg value %q in the hex-pin set is not a valid rustc target_os "+
				"(dead arm: compiles out silently, rustc only warns)", value)
			continue
		}
		if goName, ok := rustToGoOs[value]; ok {
			value = goName
		}
		rustSet[value] = true
	}
	if len(rustSet) == 0 {
		t.Fatalf("%s: hex-pin cfg block lists no platforms: %q", rustPath, cfgBlock)
	}

	goSet := map[string]bool{}
	for _, goos := range goKnownGOOS {
		if compilesFor(t, "dns_numeric_answer.go", goos) {
			goSet[goos] = true
		}
	}

	var missingRust, missingGo []string
	for p := range goSet {
		if !rustSet[p] {
			missingRust = append(missingRust, p)
		}
	}
	for p := range rustSet {
		if !goSet[p] {
			missingGo = append(missingGo, p)
		}
	}
	if len(missingRust) > 0 || len(missingGo) > 0 {
		sort.Strings(missingRust)
		sort.Strings(missingGo)
		t.Errorf("numeric-form answering set diverged across languages: Go answers without a Rust pin %v; Rust pins without answering in Go %v "+
			"(one engine was retagged and the other was not — fix both halves and the SOW section together)", missingRust, missingGo)
	}
}

func fileLines(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// rustTargetOsVocabulary is the accepted rustc target_os value set for
// the platforms that can appear in these lists. A value outside it (the
// round-2 "darwin") is a dead cfg: the arm silently never compiles.
var rustTargetOsVocabulary = map[string]bool{
	"linux": true, "macos": true, "ios": true, "tvos": true,
	"watchos": true, "freebsd": true, "netbsd": true, "openbsd": true,
	"dragonfly": true, "windows": true, "android": true, "solaris": true,
	"aix": true,
}

// rustToGoOs maps a Rust target_os value to its Go GOOS spelling where
// they differ; everything else is spelled identically.
var rustToGoOs = map[string]string{"macos": "darwin"}

// TestBuildLinesParseSanity guards the gate itself: both halves' build
// lines must be present and mention at least one GOOS term, so a typo
// that strips the constraint line fails here rather than silently
// compiling everywhere.
func TestBuildLinesParseSanity(t *testing.T) {
	for _, f := range []string{"dns_numeric_answer.go", "dns_numeric_refuse.go"} {
		text := fileLines(t, f)
		if !strings.Contains(text, "//go:build") {
			t.Fatalf("%s: //go:build line missing — the file compiles on every platform", f)
		}
	}
}
