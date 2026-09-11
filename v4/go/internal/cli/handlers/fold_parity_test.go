package handlers

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// TestWindowsFoldCorpusPin runs on every platform: windowsFoldPath
// is compiled everywhere (only sameCanonical is Windows-gated), and
// its byte output must equal the rustc 1.97.1 str::to_lowercase
// over the identical corpus on every Go toolchain.  The Go and Rust
// Windows tests pin the same hash through the production session
// code (wave 19 round 19.13 fold-parity findings).
func TestWindowsFoldCorpusPin(t *testing.T) {
	wantSHA := "3cdf661f6772e0ec6875a315d1662232cc1f80f11ab44edc65435f3b9992e4d4"
	var corpus strings.Builder
	corpus.Grow(14000000)
	for r := rune(0); r <= 0x10FFFF; r++ {
		if 0xD800 <= r && r <= 0xDFFF {
			continue
		}
		corpus.WriteRune(r)
		corpus.WriteRune(0x03A3)
		corpus.WriteRune('1')
	}
	for r := rune(0); r <= 0x10FFFF; r++ {
		if 0xD800 <= r && r <= 0xDFFF {
			continue
		}
		corpus.WriteRune('a')
		corpus.WriteRune(0x03A3)
		corpus.WriteRune(r)
		corpus.WriteRune('1')
	}
	corpus.WriteString("a\u03A3a\u03A3a\u03A3\u0308a\u0308\u03A3\u03A31\u03A3\u2160\u03A3a\u03A3\u0345a\u0345\u03A3a\u03A3\u1C89\u03A3")
	got := sha256.Sum256([]byte(windowsFoldPath(corpus.String())))
	if gotSHA := fmt.Sprintf("%x", got); gotSHA != wantSHA {
		t.Fatalf("fold string corpus sha256 %s, want %s", gotSHA, wantSHA)
	}
}
