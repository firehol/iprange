//go:build windows

// Wave-19.11 astra P1 regressions: the same-source guard must treat
// Windows filename-equivalence and drive semantics correctly for
// destinations that name the (absent) reader sidecar.  The guard is
// exercised through refuseOutputOverSource and through the real
// iprange.v1.database.metadata.get session call (the production call
// site), never by calling pathname.Push directly, so a revert of the
// canonicalAbsolute anchoring breaks these tests.

package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// windowsSidecarSpellings returns the destination spellings that name
// the absent reader sidecar of source (db + ".readers") under Windows
// filename semantics:
//
//   - driveRelative:      "C:db.readers"  (per-drive cwd, F1)
//   - driveRelativeUpper: "C:DB.READERS"  (F1+F2)
//   - absoluteUpper:      "C:\dir\DB.READERS" (F2)
//   - rooted:             "\dir\db.readers" (rooted-without-volume,
//     F3: Push anchors it on the source drive; raw concatenation
//     anchors it on the cwd and misses)
//
// dir[len(drive):] keeps the leading separator ("\Users\..."), so
// exactly one separator is added before it; a second one would turn
// the spelling into a UNC path that names a different share.
func windowsSidecarSpellings(dir, source string) []string {
	drive := filepath.VolumeName(dir) // "C:"
	base := filepath.Base(source)     // "db"
	rooted := string(os.PathSeparator) + strings.TrimLeft(dir[len(drive):], "\\/") +
		string(os.PathSeparator) + base + format.CoordinationSuffix
	return []string{
		drive + base + format.CoordinationSuffix,
		strings.ToUpper(drive + base + format.CoordinationSuffix),
		filepath.Join(dir, strings.ToUpper(base+format.CoordinationSuffix)),
		rooted,
		filepath.Join(dir, base+format.CoordinationSuffix+"."),
		filepath.Join(dir, base+format.CoordinationSuffix+" ")}
}

// TestRefuseOutputOverSourceWindowsSidecarSpellings pins the guard
// itself: every spelling that names the absent sidecar is refused,
// distinct destinations are accepted, and the per-drive working
// directory is what resolves drive-relative spellings.
// TestSameCanonicalWindowsFold pins the shared Windows fold: Unicode
// lowercase equivalence (ASCII, Latin-1, and the U+0130 expansion
// shared with Rust char::to_lowercase) and the Win32 trailing
// dot/space create normalization of the final component.  Distinct
// trailing-dot names (not the sidecar) stay unequal.
func TestSameCanonicalWindowsFold(t *testing.T) {
	equal := [][2]string{
		{"C:\\review\\db.readers", "c:\\review\\DB.READERS"},
		{"C:\\review\\db_ä.readers", "C:\\review\\DB_Ä.READERS"},
		{"C:\\review\\db_İ.readers", "C:\\review\\db_i̇.readers"},
		{"C:\\review\\db_꟎.readers", "C:\\review\\DB_꟏.READERS"},
		{"C:\\review\\db_꟒.readers", "C:\\review\\DB_ꟓ.READERS"},
		{"C:\\review\\db_꟔.readers", "C:\\review\\DB_ꟕ.READERS"},
		{"C:\\review\\db.readers.", "C:\\review\\db.readers"},
		{"C:\\review\\db.readers ", "C:\\review\\db.readers"},
		{"C:\\review\\..", "C:\\review\\.."},
	}
	for _, pair := range equal {
		if !sameCanonical(pair[0], pair[1]) {
			t.Errorf("sameCanonical(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	distinct := [][2]string{
		{"C:\\review\\other.readers.", "C:\\review\\db.readers"},
		{"C:\\review\\other.readers ", "C:\\review\\db.readers"},
		{"C:\\review\\db_ä.readers", "C:\\review\\db_ö.readers"},
	}
	for _, pair := range distinct {
		if sameCanonical(pair[0], pair[1]) {
			t.Errorf("sameCanonical(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

// TestRefuseOutputOverSourceWindowsNonASCII pins the non-ASCII class
// at the guard: a destination that differs from the absent sidecar
// only by script case is refused.
func TestRefuseOutputOverSourceWindowsNonASCII(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	source := filepath.Join(dir, "db_ä.bin")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{
		filepath.Join(dir, "DB_Ä.BIN.READERS"),
		filepath.Join(dir, "DB_Ä.BIN.READERS "),
	} {
		if herr := refuseOutputOverSource(destination, source, nil, nil); herr == nil {
			t.Fatalf("destination %q naming the absent sidecar accepted", destination)
		}
	}
}

func TestRefuseOutputOverSourceWindowsSidecarSpellings(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	source := filepath.Join(dir, "db.bin")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := source + format.CoordinationSuffix
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar must be absent for this regression: %v", err)
	}

	// The drive-relative spelling must resolve to the per-drive cwd,
	// which is the test working directory after chdir.
	driveRelative := filepath.VolumeName(dir) + filepath.Base(source) + format.CoordinationSuffix
	if got := canonicalAbsolute(driveRelative); got != canonicalAbsolute(sidecar) {
		t.Fatalf("canonicalAbsolute(%q) = %q, want %q (drive-relative must resolve the per-drive cwd)",
			driveRelative, got, canonicalAbsolute(sidecar))
	}

	for _, destination := range windowsSidecarSpellings(dir, source) {
		if herr := refuseOutputOverSource(destination, source, nil, nil); herr == nil {
			t.Fatalf("destination %q naming the absent sidecar accepted", destination)
		}
	}

	// Distinct destinations stay allowed: an absolute file in the
	// directory and a rooted-without-volume name on the drive root
	// that is not the sidecar (Push semantics; raw concatenation
	// would wrongly refuse or wrongly accept).
	allowed := []string{
		filepath.Join(dir, "other.bin"),
		string(os.PathSeparator) + "unrelated.bin",
	}
	for _, destination := range allowed {
		if herr := refuseOutputOverSource(destination, source, nil, nil); herr != nil {
			t.Fatalf("distinct destination %q refused: %v", destination, herr)
		}
	}
}

// TestSessionMetadataGetWindowsSidecarSpellings pins the production
// call site: iprange.v1.database.metadata.get refuses every spelling
// that names the absent sidecar (exact canonical refusal, unchanged
// source, absent sidecar, successful reopening) and still publishes
// to a distinct destination.
func TestSessionMetadataGetWindowsSidecarSpellings(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	source := newImmutableFeed(t, dir, "src.db", []byte("mymetadata"))
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := source + format.CoordinationSuffix

	for _, destination := range windowsSidecarSpellings(dir, source) {
		frame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.database.metadata.get","params":{"source":{"path":` +
			mustJSONString(source) + `,"mode":"immutable"},"delivery":{"mode":"file","path":` +
			mustJSONString(destination) + `,"publication_policy":"replace_existing","max_output_bytes":"1048576","max_open_files":8}}}`
		out := runSession(t, frame)
		if !strings.Contains(out, `"code":"invalid_argument"`) ||
			!strings.Contains(out, "destination must differ from the source database") {
			t.Fatalf("metadata.get to %q: output = %q, want the source-refusal error", destination, out)
		}
		if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
			t.Fatalf("metadata.get to %q created the sidecar: %v", destination, err)
		}
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("source bytes changed after refusals: %d -> %d bytes", len(before), len(after))
	}

	// Control: a distinct destination still publishes the metadata
	// bytes (this is what the guard must keep allowing).
	out := filepath.Join(dir, "meta.bin")
	frame := `{"jsonrpc":"2.0","id":"2","method":"iprange.v1.database.metadata.get","params":{"source":{"path":` +
		mustJSONString(source) + `,"mode":"immutable"},"delivery":{"mode":"file","path":` +
		mustJSONString(out) + `,"publication_policy":"fail_if_exists","max_output_bytes":"1048576","max_open_files":8}}}`
	res := runSession(t, frame)
	if strings.Contains(res, `"code":"invalid_argument"`) || strings.Contains(res, "destination must differ") {
		t.Fatalf("distinct metadata destination refused: %s", res)
	}
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "mymetadata" {
		t.Fatalf("metadata output %q, want mymetadata", content)
	}
}

func TestSameCanonicalWindowsFoldUnicode16(t *testing.T) {
	// rustc 1.97.1 (the Windows Rust product toolchain) applies the
	// Unicode-16 lowercase mappings below while the go1.26.5
	// (Windows Go product toolchain) tables predate them (added in
	// go1.27); windowsFoldPath maps them explicitly.  Pin every pair
	// so a Go or Rust toolchain table change cannot silently break
	// the byte-identical Windows fold (wave 19 round 19.13
	// fold-parity finding; differential enumeration in
	// v4/cli/evidence/fold-enum/).
	single := []struct{ up, lo rune }{
		{0x1C89, 0x1C8A},
		{0xA7CB, 0x0264},
		{0xA7CC, 0xA7CD},
		{0xA7CE, 0xA7CF},
		{0xA7D2, 0xA7D3},
		{0xA7D4, 0xA7D5},
		{0xA7DA, 0xA7DB},
		{0xA7DC, 0x019B},
	}
	for _, p := range single {
		up := fmt.Sprintf(`C:\review\db_%c.readers`, p.up)
		lo := fmt.Sprintf(`C:\review\DB_%c.READERS`, p.lo)
		if !sameCanonical(up, lo) {
			t.Errorf("fold mismatch: U+%04X vs U+%04X", p.up, p.lo)
		}
	}
	for up := 0x10D50; up <= 0x10D65; up++ { // Garay uppercase block
		lo := up + 0x20
		if !sameCanonical(fmt.Sprintf(`C:\review\db_%c.readers`, up),
			fmt.Sprintf(`C:\review\DB_%c.READERS`, lo)) {
			t.Errorf("fold mismatch: U+%04X vs U+%04X", up, lo)
		}
	}
	for up := 0x16EA0; up <= 0x16EB8; up++ { // Kirat Rai uppercase block
		lo := up + 0x1B
		if !sameCanonical(fmt.Sprintf(`C:\review\db_%c.readers`, up),
			fmt.Sprintf(`C:\review\DB_%c.READERS`, lo)) {
			t.Errorf("fold mismatch: U+%04X vs U+%04X", up, lo)
		}
	}
}
