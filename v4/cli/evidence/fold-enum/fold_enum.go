package main

import (
	"fmt"
	"unicode"
)

// Verbatim replica of windowsFoldPath's per-rune mapping from
// v4/go/internal/cli/handlers/export.go AFTER the wave 19.13
// correction (full Unicode-16 mapping set).
func foldRune(r rune) string {
	switch r {
	case 0x0130:
		return "i\u0307"
	case 0x1C89:
		return string(rune(0x1C8A))
	case 0xA7CB:
		return string(rune(0x0264))
	case 0xA7CC:
		return string(rune(0xA7CD))
	case 0xA7CE, 0xA7D2, 0xA7D4:
		return string(rune(r + 1))
	case 0xA7DA:
		return string(rune(0xA7DB))
	case 0xA7DC:
		return string(rune(0x019B))
	}
	if r >= 0x10D50 && r <= 0x10D65 {
		return string(rune(r + 0x20))
	}
	if r >= 0x16EA0 && r <= 0x16EB8 {
		return string(rune(r + 0x1B))
	}
	return string(unicode.ToLower(r))
}

func main() {
	for r := rune(0); r <= 0x10FFFF; r++ {
		if 0xD800 <= r && r <= 0xDFFF {
			continue
		}
		out := foldRune(r)
		fmt.Printf("U+%04X ", r)
		for _, c := range out {
			fmt.Printf("U+%04X ", c)
		}
		fmt.Println()
	}
}
