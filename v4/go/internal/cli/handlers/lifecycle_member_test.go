package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDatabaseCreateCreatorOnlyMemberPinsTheWireShape proves the
// documented member reaches the create path with the exact transport
// contract (Rust twin): absent follows the process switch, true/false
// are honored, and non-boolean values — including null, which the
// frozen oracle types boolean-only — are invalid-params refusals.
func TestDatabaseCreateCreatorOnlyMemberPinsTheWireShape(t *testing.T) {
	base := map[string]any{
		"path":            "/tmp/iprange-v4-member.v4",
		"family":          "ipv4",
		"value_kind":      "direct",
		"structure_kind":  "none",
		"value_tag":       map[string]any{"text": "asn"},
		"reader_capacity": float64(2),
	}
	marshal := func(extra map[string]any) json.RawMessage {
		object := map[string]any{}
		for key, value := range base {
			object[key] = value
		}
		for key, value := range extra {
			object[key] = value
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	for _, member := range []map[string]any{
		{"creator_only": true},
		{"creator_only": false},
		{},
	} {
		if err := ValidateDatabaseCreateParams(marshal(member)); err != nil {
			t.Fatalf("validate %+v: %v", member, err)
		}
	}
	for _, member := range []map[string]any{
		{"creator_only": nil},
		{"creator_only": "true"},
		{"creator_only": 1},
	} {
		err := ValidateDatabaseCreateParams(marshal(member))
		if err == nil || !strings.Contains(err.Error(), "creator_only must be a boolean") {
			t.Fatalf("validate %+v = %v, want the boolean refusal", member, err)
		}
	}
}
