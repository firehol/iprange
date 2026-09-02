package handlers

import (
	"encoding/json"
	"testing"
)

// publicationResultWithBasename builds a complete valid
// publication_result whose attempt uses the given destination
// basename.
func publicationResultWithBasename(basename string) map[string]any {
	attempt := map[string]any{
		"database_id":                   "0102030405060708090a0b0c0d0e0f10",
		"transaction_id":                "1",
		"commit_nonce":                  "11111111111111111111111111111111",
		"publication_attempt_id":        "21212121212121212121212121212121",
		"directory_identity":            map[string]any{"volume": "1", "file": "2"},
		"destination_basename_encoding": 1,
		"destination_basename":          basename,
		"output_identity":               map[string]any{"volume": "1", "file": "2"},
		"output_byte_length":            "4096",
		"output_sha512":                 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"publication_policy":            "replace_existing",
		"reservation_identity":          map[string]any{"volume": "3", "file": "4"},
		"creation_security":             map[string]any{"kind": 1, "commitment": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	return map[string]any{
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
	}
}

// TestBase64TrailingBitsRejected pins the Rust decode_base64
// trailing-bit rule on the CLI validation surfaces: publication
// evidence destination basenames and metadata replace_base64 blobs
// with non-canonical final quartets must be refused before any work
// runs (Rust lifecycle.rs decode_base64).
func TestBase64TrailingBitsRejected(t *testing.T) {
	for _, basename := range []string{"AB==", "Zh==", "Zx=="} {
		params, err := json.Marshal(map[string]any{
			"path":               "/tmp/probe",
			"resolution_mode":    "remove",
			"publication_result": publicationResultWithBasename(basename),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePublicationResolveParams(params); err == nil {
			t.Fatalf("publication.resolve with basename %q must be refused", basename)
		}
	}
	for _, blob := range []string{"AB==", "Zh==", "Zx=="} {
		params, err := json.Marshal(map[string]any{
			"path": "/tmp/probe",
			"metadata": map[string]any{
				"mode":   "replace_base64",
				"base64": blob,
			},
			"writer_budget": map[string]any{
				"size":    1,
				"staging": 1,
				"pages":   1,
				"fds":     1,
				"slots":   1,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateDatabaseMetadataReplaceParams(params); err == nil {
			t.Fatalf("metadata.replace_base64 with %q must be refused", blob)
		}
	}
	// Canonical forms still pass validation (the earlier probes in
	// this package prove the full round trip; here only the validator).
	for _, basename := range []string{"Zg==", "Zm9v"} {
		params, err := json.Marshal(map[string]any{
			"path":               "/tmp/probe",
			"resolution_mode":    "remove",
			"publication_result": publicationResultWithBasename(basename),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePublicationResolveParams(params); err != nil {
			t.Fatalf("canonical basename %q must pass: %v", basename, err)
		}
	}
}
