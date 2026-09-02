package format

import "testing"

// TestErrorCodeWireVocabularyRoundTrip pins the complete closed wire
// vocabulary: every code in the table has a unique wire name,
// every name resolves back to the same code, and the table size stays
// exactly 69 (the closed product list of iprange-jsonrpc-v1.md).
func TestErrorCodeWireVocabularyRoundTrip(t *testing.T) {
	seen := make(map[string]bool, len(errorCodeWireNames))
	for code, name := range errorCodeWireNames {
		if seen[name] {
			t.Fatalf("duplicate wire name %q", name)
		}
		seen[name] = true
		if got, ok := ErrorCodeWireName(code); !ok || got != name {
			t.Fatalf("ErrorCodeWireName(%d) = %q, %v; want %q", code, got, ok, name)
		}
		back, ok := ErrorCodeFromWireName(name)
		if !ok || back != code {
			t.Fatalf("round-trip %q -> code %d, want %d", name, back, code)
		}
	}
	if len(seen) != 69 {
		t.Fatalf("wire vocabulary has %d names, want 69", len(seen))
	}
	if len(errorCodeFromWireNames) != len(errorCodeWireNames) {
		t.Fatalf("reverse table size %d != forward table size %d",
			len(errorCodeFromWireNames), len(errorCodeWireNames))
	}
}
