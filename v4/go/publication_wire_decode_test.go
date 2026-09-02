package iprangedb

import (
	"encoding/json"
	"testing"
)

// TestDecodePublicationResultWireRoundTrip pins the strict wire
// decoder against a complete publication_result object: envelope
// fields, exact decimal counts, lowercase hex identities, and the
// optional evidence members.
func TestDecodePublicationResultWireRoundTrip(t *testing.T) {
	attempt := map[string]any{
		"database_id":                   "0102030405060708090a0b0c0d0e0f10",
		"transaction_id":                "18446744073709551615",
		"commit_nonce":                  "1112131415161718191a1b1c1d1e1f20",
		"publication_attempt_id":        "2122232425262728292a2b2c2d2e2f30",
		"directory_identity":            map[string]any{"volume": "72623859790382856", "file": "651345242494996240"},
		"destination_basename_encoding": 1,
		"destination_basename":          "Zm9vLm5ldHM=", // "foo.nets" padded
		"output_identity":               map[string]any{"volume": "1", "file": "2"},
		"output_byte_length":            "4096",
		"output_sha512":                 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"publication_policy":            "replace_existing",
		"reservation_identity":          map[string]any{"volume": "3", "file": "4"},
		"creation_security":             map[string]any{"kind": 1, "commitment": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	wire := map[string]any{
		"attempt":                                attempt,
		"main_namespace_may_have_been_attempted": true,
		"publication":                            "published",
		"destination_content":                    "desired",
		"later_canonical":                        "none",
		"main_access_policy":                     "creator_only",
		"coordination_access_policy":             "absent",
		"cleanup":                                map[string]any{},
		"coordination_cleanup":                   map[string]any{},
		"housekeeping":                           map[string]any{},
		"visible_housekeeping":                   []any{},
		"live_lineage":                           map[string]any{"kind": "same_generation_exact_bytes"},
		"later_selected_transaction_id":          "99",
	}
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodePublicationResultJSON(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Attempt.DatabaseID != [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16} {
		t.Fatalf("database id = %x", result.Attempt.DatabaseID)
	}
	if result.Attempt.TransactionID != ^uint64(0) {
		t.Fatalf("transaction id = %d", result.Attempt.TransactionID)
	}
	if result.Attempt.DestinationBasenameEncoding != 1 || string(result.Attempt.DestinationBasename) != "foo.nets" {
		t.Fatalf("basename = %q enc %d", result.Attempt.DestinationBasename, result.Attempt.DestinationBasenameEncoding)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v", result.Publication)
	}
	if result.LiveLineage == nil || *result.LiveLineage != LiveLineageSameGenerationExactBytes {
		t.Fatalf("lineage = %v", result.LiveLineage)
	}
	if result.LaterSelectedTransactionID == nil || *result.LaterSelectedTransactionID != 99 {
		t.Fatalf("later transaction = %v", result.LaterSelectedTransactionID)
	}
	// Strictness: unknown member fails.
	wire["later_attempt_or_sidecar_id"] = "00000000000000000000000000000000"
	bad, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicationResultJSON(bad); err != nil {
		t.Fatalf("null id must be invalid, got %v", err)
	}
	delete(wire, "later_attempt_or_sidecar_id")
	wire["extra_member"] = 1
	bad, _ = json.Marshal(wire)
	if _, err := DecodePublicationResultJSON(bad); err == nil {
		t.Fatal("unknown member must fail")
	}
	// Null optional member must fail (absent is the only absent form).
	wire["live_lineage"] = nil
	bad, _ = json.Marshal(wire)
	if _, err := DecodePublicationResultJSON(bad); err == nil {
		t.Fatal("null optional member must fail")
	}
}
