// Platform-scoped renderings for the byte-exact legacy parity tables.
//
// Three of the pinned bytes in this package are decided by the platform
// rather than by the tool, and each has its own rendering below:
//
//   - the name a directory expansion gives to an entry. C joins the
//     directory and the entry with "%s/%s" (src/iprange.c:772). Each
//     engine instead uses the platform join, so the name is a usable path
//     on the platform that printed it, and both engines use one rule so
//     the `name` column of `--count-unique --header` agrees byte for byte
//     across them on any platform (Rust Path::join at
//     v4/rust/iprange-cli/src/legacy/parse.rs:260, Go pathname.Push in
//     parse.go expandAt). The pins here were measured on Linux, where the
//     two spellings coincide.
//   - the name a file keeps once the platform has stored it, when the
//     bytes are not valid UTF-8. A POSIX file name is a byte string and
//     comes back unchanged; a Windows name is UTF-16, so a byte with no
//     representation comes back as U+FFFD.
//   - the message half of an OS failure diagnostic, which is the
//     platform's own text for the code the platform reported (see
//     overCapPin below and strerror in parse.go).
//
// None of these is a normalization. Each rendering rewrites a pinned
// expectation into the single spelling the platform running the test
// produces, every other byte is still compared exactly, the substituted
// regions are enumerated rather than pattern-matched, and
// TestPlatformPinRenderingsAreEffective fails if a rendering ever stops
// applying where it is used -- so a pin that drifts away from the table it
// documents is reported, not silently passed.
package legacy

import (
	"runtime"
	"strings"
	"testing"
)

// expansionPinDirs are the directories whose entries the parity tables
// name, spelled exactly as the fixtures create them. The list is explicit
// so the rendering cannot rewrite an unrelated slash anywhere else.
var expansionPinDirs = []string{"dir", "dir6", "cmp_dirv"}

// posixLineCapMessage is the text the pins carry for the diagnostic whose
// message half the OS supplies: glibc strerror(ENAMETOOLONG), which the
// released tool prints and every platform in this project's shared errno
// table reproduces deliberately (parse_strerror_other.go). overCapPin
// substitutes it only on the target that has no table entry for the code
// its open actually failed with.
const posixLineCapMessage = "File name too long"

// expansionPin renders one expectation for the entry-naming rule of the
// platform running the test.
func expansionPin(s string) string {
	if runtime.GOOS != "windows" {
		return s
	}
	for _, dir := range expansionPinDirs {
		s = strings.ReplaceAll(s, dir+"/", dir+`\`)
	}
	return s
}

// platformPin applies every rendering this platform requires to one pinned
// expectation. The order is fixed: the raw-byte name exists inside the
// expansion name, so the byte substitution runs first and the separator
// substitution still sees the result.
func platformPin(s string) string {
	return expansionPin(overCapPin(rawBytePin(s)))
}

// TestPlatformPinRenderingsAreEffective pins that each rendering is the
// identity exactly where it is documented to be, and applies exactly where
// it is documented to apply. Without it a rendering could rot into a no-op
// and the tables would silently stop testing the platform they name.
func TestPlatformPinRenderingsAreEffective(t *testing.T) {
	const posixName = "z\xffy.txt"

	gotName := rawBytePin(posixName)
	if runtime.GOOS == "windows" {
		if gotName == posixName {
			t.Errorf("rawBytePin did not render the invalid byte on Windows: %q", gotName)
		}
		if gotName != "z\ufffdy.txt" {
			t.Errorf("Windows stores an unrepresentable name byte as U+FFFD, got %q", gotName)
		}
	} else if gotName != posixName {
		t.Errorf("a POSIX file name is a byte string; rawBytePin changed %q to %q", posixName, gotName)
	}

	for _, sample := range []string{"dir/a.txt", "dir6/a.txt", "cmp_dirv/a.iprange"} {
		got := expansionPin(sample)
		if runtime.GOOS == "windows" {
			if got == sample || strings.HasSuffix(got, "/a.txt") || strings.HasSuffix(got, "/a.iprange") {
				t.Errorf("expansionPin kept the POSIX separator on Windows for %q: %q", sample, got)
			}
			if got != strings.Replace(sample, "/", `\`, 1) {
				t.Errorf("expansionPin rendered %q as %q, want the same name with the platform separator",
					sample, got)
			}
		} else if got != sample {
			t.Errorf("expansionPin rewrote %q to %q on %s, where the expansion name is the pinned bytes",
				sample, got, runtime.GOOS)
		}
	}

	// The over-cap rendering is checked where its code exists: this platform
	// keeps the contractual glibc text, and Windows is pinned by
	// TestOverCapPinIsThePlatformsOwnMessage in its own file.
	const lineCap = "iprange: n - " + posixLineCapMessage + "\n"
	if runtime.GOOS != "windows" {
		if got := overCapPin(lineCap); got != lineCap {
			t.Errorf("overCapPin rewrote the contractual glibc text on %s: %q", runtime.GOOS, got)
		}
	}
}
