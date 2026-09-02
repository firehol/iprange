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
		"housekeeping":                           map[string]any{"artifacts": []any{}},
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
	// Strictness: the optional later_attempt_or_sidecar_id member is
	// only legal absent; an explicit null member must fail.
	wire["later_attempt_or_sidecar_id"] = nil
	bad, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePublicationResultJSON(bad); err == nil {
		t.Fatal("null later_attempt_or_sidecar_id must fail")
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

// TestDecodePublicationResultWireStrictness pins every canonical
// strictness rule of the wire decoder against mutated evidence; each
// case must fail with an error. These are the committed negative
// cases behind the 12-mutation CLI parity probe.
func TestDecodePublicationResultWireStrictness(t *testing.T) {
	attempt := map[string]any{
		"database_id":                   "0102030405060708090a0b0c0d0e0f10",
		"transaction_id":                "1",
		"commit_nonce":                  "11111111111111111111111111111111",
		"publication_attempt_id":        "21212121212121212121212121212121",
		"directory_identity":            map[string]any{"volume": "1", "file": "2"},
		"destination_basename_encoding": 1,
		"destination_basename":          "Zm9vLm5ldHM=",
		"output_identity":               map[string]any{"volume": "1", "file": "2"},
		"output_byte_length":            "4096",
		"output_sha512":                 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"publication_policy":            "replace_existing",
		"reservation_identity":          map[string]any{"volume": "3", "file": "4"},
		"creation_security":             map[string]any{"kind": 1, "commitment": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	base := map[string]any{
		"attempt":                                attempt,
		"main_namespace_may_have_been_attempted": true,
		"publication":                            "published",
		"destination_content":                    "desired",
		"later_canonical":                        "none",
		"main_access_policy":                     "creator_only",
		"coordination_access_policy":             "absent",
		"cleanup":                                map[string]any{},
		"coordination_cleanup":                   map[string]any{},
		"housekeeping":                           map[string]any{"artifacts": []any{}},
		"visible_housekeeping":                   []any{},
		"live_lineage":                           map[string]any{"kind": "same_generation_exact_bytes"},
		"later_selected_transaction_id":          "99",
	}
	mutate := func(change func(w map[string]any)) []byte {
		data, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var w map[string]any
		if err := json.Unmarshal(data, &w); err != nil {
			t.Fatal(err)
		}
		change(w)
		data, err = json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	cases := map[string]func(w map[string]any){
		"null_coordination_cleanup": func(w map[string]any) { w["coordination_cleanup"] = nil },
		"null_visible_housekeeping": func(w map[string]any) { w["visible_housekeeping"] = nil },
		"null_artifacts":            func(w map[string]any) { w["housekeeping"].(map[string]any)["artifacts"] = nil },
		"empty_housekeeping_object": func(w map[string]any) { w["housekeeping"] = map[string]any{} },
		"missing_housekeeping":      func(w map[string]any) { delete(w, "housekeeping") },
		"leading_zero_decimal":      func(w map[string]any) { w["later_selected_transaction_id"] = "099" },
		"non_decimal_u64":           func(w map[string]any) { w["later_selected_transaction_id"] = "12x" },
		"bad_base64_unpadded": func(w map[string]any) {
			w["attempt"].(map[string]any)["destination_basename"] = "Zm9vLm5ldHM"
		},
		"bad_base64_trailing_bits": func(w map[string]any) {
			w["attempt"].(map[string]any)["destination_basename"] = "AB=="
		},
		"unknown_member": func(w map[string]any) { w["extra_member"] = 1 },
		"string_os_code": func(w map[string]any) { w["attempt"].(map[string]any)["destination_basename_encoding"] = "1" },
		"uppercase_hex": func(w map[string]any) {
			w["attempt"].(map[string]any)["database_id"] = "0102030405060708090A0B0C0D0E0F10"
		},
		"bad_wire_code":   func(w map[string]any) { w["publication"] = "partially_published" },
		"missing_attempt": func(w map[string]any) { delete(w, "attempt") },
		"null_later_attempt_or_sidecar": func(w map[string]any) {
			w["later_attempt_or_sidecar_id"] = nil
		},
	}
	for name, change := range cases {
		if _, err := DecodePublicationResultJSON(mutate(change)); err == nil {
			t.Fatalf("case %q must fail strict decoding", name)
		}
	}
	// The optional cleanup-artifact members (basename, identity,
	// creation_security, unpublished_tail) are permitted omissions.
	artifact := map[string]any{
		"kind":               "private_output",
		"directory_role":     "destination",
		"directory_identity": map[string]any{"volume": "1", "file": "2"},
		"basename_encoding":  1,
		"error":              map[string]any{"code": "io", "detail": "probe"},
	}
	sparseCleanup := mutate(func(w map[string]any) {
		w["cleanup"] = map[string]any{"artifacts": []any{artifact}}
	})
	if _, err := DecodePublicationResultJSON(sparseCleanup); err != nil {
		t.Fatalf("cleanup artifact without optional members must be accepted: %v", err)
	}
	withBasename := mutate(func(w map[string]any) {
		with := map[string]any{}
		for k, v := range artifact {
			with[k] = v
		}
		with["basename"] = "62617365"
		w["cleanup"] = map[string]any{"artifacts": []any{with}}
	})
	if _, err := DecodePublicationResultJSON(withBasename); err != nil {
		t.Fatalf("cleanup artifact with basename must be accepted: %v", err)
	}
}
