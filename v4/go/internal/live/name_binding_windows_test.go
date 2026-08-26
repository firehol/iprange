//go:build windows

// WindowsUtf16Le basename binding validation (Rust
// validate_windows_utf16le): even byte length, no zero/slash/backslash
// units, and well-formed surrogate pairs only.

package live

import (
	"testing"
	"unicode/utf16"
)

func TestWindowsUtf16BindingValidation(t *testing.T) {
	valid := []string{"main.iprdb", "x", "\U0001f600", "a\u00e9b"}
	for _, name := range valid {
		units := utf16.Encode([]rune(name))
		bytes := make([]byte, 0, len(units)*2)
		for _, unit := range units {
			bytes = append(bytes, byte(unit), byte(unit>>8))
		}
		if err := ValidateEncodingBinding(basenameEncodingWindowsUtf16Le, bytes); err != nil {
			t.Fatalf("valid utf-16 %q rejected: %v", name, err)
		}
	}
	invalid := []struct {
		name  string
		units []uint16
	}{
		{"lone high surrogate", []uint16{0xd800, 'a'}},
		{"lone low surrogate", []uint16{'a', 0xdc00}},
		{"high then non-low", []uint16{0xd800, 0xd800}},
		{"trailing high surrogate", []uint16{'a', 0xd800}},
		{"nul unit", []uint16{'a', 0}},
		{"slash unit", []uint16{'a', '/'}},
		{"backslash unit", []uint16{'a', '\\'}},
	}
	for _, vector := range invalid {
		bytes := make([]byte, 0, len(vector.units)*2)
		for _, unit := range vector.units {
			bytes = append(bytes, byte(unit), byte(unit>>8))
		}
		if err := ValidateEncodingBinding(basenameEncodingWindowsUtf16Le, bytes); err == nil {
			t.Fatalf("invalid utf-16 %s accepted", vector.name)
		}
	}
	if err := ValidateEncodingBinding(basenameEncodingWindowsUtf16Le, []byte{0x61}); err == nil {
		t.Fatal("odd-length utf-16 payload accepted")
	}
}
