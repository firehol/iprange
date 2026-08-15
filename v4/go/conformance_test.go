package iprangedb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// strictUnmarshal unmarshals JSON with case-sensitive field names,
// duplicate-field rejection, and trailing-data rejection, mirroring Rust's
// serde derived deserializer semantics. Go's encoding/json is
// case-insensitive even with DisallowUnknownFields and accepts duplicate
// fields with last-value semantics; this wrapper validates every nested
// object's keys against the known manifest field set and rejects duplicate
// keys within any single object.
func strictUnmarshal(data []byte, v any) error {
	// Reject duplicate keys within any single object. The manifest is a
	// flat top-level object with nested arrays of objects; a simple
	// stack-based scanner detects duplicates without a full JSON parser.
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	// First pass: decode into a generic structure and validate every
	// nested object's keys against the known field set.
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if err := validateJSONKeys(probe, ""); err != nil {
		return err
	}
	// Second pass: decode into the typed struct with DisallowUnknownFields.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// Reject trailing data: any non-EOF result from a second Decode means
	// the input has extra bytes after the manifest value.
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing data after manifest")
	}
	return nil
}

// rejectDuplicateKeys scans the raw JSON for duplicate keys within any
// single object. It uses a stack of maps: each { pushes a new map, each }
// pops it, and each key is checked against the current top of the stack.
// String values are skipped (keys inside strings are not object keys).
func rejectDuplicateKeys(data []byte) error {
	type frame struct {
		keys map[string]bool
	}
	var stack []frame
	var i int
	for i < len(data) {
		switch data[i] {
		case '{':
			stack = append(stack, frame{keys: map[string]bool{}})
			i++
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			i++
		case '"':
			// Find the end of the string.
			j := i + 1
			for j < len(data) && data[j] != '"' {
				if data[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(data) {
				return fmt.Errorf("unterminated string at offset %d", i)
			}
			// Validate the string is valid UTF-8 with no unpaired surrogates,
			// mirroring Rust's serde_json::from_slice which rejects both.
			// Go's json.Unmarshal replaces invalid UTF-8 with U+FFFD, so we
			// validate the raw bytes before decoding: every byte outside an
			// escape sequence must be valid UTF-8, and every \u escape must
			// form a valid surrogate pair.
			raw := data[i+1 : j]
			if !utf8.Valid(raw) {
				return fmt.Errorf("invalid UTF-8 in string at offset %d", i)
			}
			// Check for unpaired surrogates in \u escapes.
			for k := 0; k < len(raw); k++ {
				if raw[k] == '\\' && k+1 < len(raw) && raw[k+1] == 'u' {
					if k+5 >= len(raw) {
						return fmt.Errorf("truncated \\u escape at offset %d", i+k)
					}
					// Decode the 4 hex digits.
					var cp uint32
					for _, c := range raw[k+2 : k+6] {
						var d uint32
						switch {
						case c >= '0' && c <= '9':
							d = uint32(c - '0')
						case c >= 'a' && c <= 'f':
							d = uint32(c-'a') + 10
						case c >= 'A' && c <= 'F':
							d = uint32(c-'A') + 10
						default:
							return fmt.Errorf("invalid \\u escape at offset %d", i+k)
						}
						cp = cp<<4 | d
					}
					if cp >= 0xD800 && cp <= 0xDBFF {
						// High surrogate: must be followed by \uDC00-\uDFFF.
						if k+11 >= len(raw) || raw[k+6] != '\\' || raw[k+7] != 'u' {
							return fmt.Errorf("unpaired high surrogate at offset %d", i+k)
						}
						var lo uint32
						for _, c := range raw[k+8 : k+12] {
							var d uint32
							switch {
							case c >= '0' && c <= '9':
								d = uint32(c - '0')
							case c >= 'a' && c <= 'f':
								d = uint32(c-'a') + 10
							case c >= 'A' && c <= 'F':
								d = uint32(c-'A') + 10
							default:
								return fmt.Errorf("invalid \\u escape at offset %d", i+k+6)
							}
							lo = lo<<4 | d
						}
						if lo < 0xDC00 || lo > 0xDFFF {
							return fmt.Errorf("unpaired high surrogate at offset %d", i+k)
						}
						k += 11
					} else if cp >= 0xDC00 && cp <= 0xDFFF {
						return fmt.Errorf("unpaired low surrogate at offset %d", i+k)
					}
					k += 5
				}
			}
			// Decode the string for the duplicate-key check.
			var decoded string
			if err := json.Unmarshal(data[i:j+1], &decoded); err != nil {
				return fmt.Errorf("invalid string at offset %d: %v", i, err)
			}
			// Check if this is a key (followed by :).
			k := j + 1
			for k < len(data) && (data[k] == ' ' || data[k] == '\t' || data[k] == '\n' || data[k] == '\r') {
				k++
			}
			if k < len(data) && data[k] == ':' && len(stack) > 0 {
				// Decode the key to handle escaped forms (\u0073chema ==
				// schema): two raw spellings that decode to the same key
				// are duplicates, mirroring Rust's decoded-field comparison.
				if stack[len(stack)-1].keys[decoded] {
					return fmt.Errorf("duplicate key %q", decoded)
				}
				stack[len(stack)-1].keys[decoded] = true
			}
			i = j + 1
		default:
			i++
		}
	}
	return nil
}

// validateJSONKeys recursively checks that every object key in the decoded
// JSON exactly matches a known field name for its context. The path
// parameter is the dot-separated path to the current object (for error
// messages). The known fields are the exact struct tags from the manifest
// types; any key not in the known set is rejected.
func validateJSONKeys(v any, path string) error {
	switch val := v.(type) {
	case map[string]any:
		for key, child := range val {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if !knownJSONField(childPath) {
				return fmt.Errorf("unknown or case-mismatched field %q", childPath)
			}
			if err := validateJSONKeys(child, childPath); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range val {
			if err := validateJSONKeys(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// knownJSONField reports whether the dot-separated path is a known manifest
// field. Array indices are stripped before the lookup: "fixtures[0].file"
// is checked as "fixtures.file". The set covers every field in the manifest
// schema (version 2); any key not in the set is rejected.
func knownJSONField(path string) bool {
	// Strip array indices: "fixtures[0].file" -> "fixtures.file".
	// The path uses [N] for array elements; remove the [N] entirely.
	clean := path
	for {
		start := strings.Index(clean, "[")
		if start < 0 {
			break
		}
		end := strings.Index(clean[start:], "]")
		if end < 0 {
			break
		}
		clean = clean[:start] + clean[start+end+1:]
	}
	// The exact field set for the manifest schema (version 2).
	switch clean {
	case "schema", "fixtures", "invalid_cases",
		"fixtures.file", "fixtures.producer", "fixtures.family",
		"fixtures.kind", "fixtures.structure", "fixtures.tag",
		"fixtures.metadata", "fixtures.metadata.state",
		"fixtures.metadata.value", "fixtures.metadata.byte",
		"fixtures.metadata.length",
		"fixtures.direct_ranges", "fixtures.direct_ranges.from",
		"fixtures.direct_ranges.to", "fixtures.direct_ranges.value",
		"fixtures.membership_ranges", "fixtures.membership_ranges.from",
		"fixtures.membership_ranges.to", "fixtures.membership_ranges.feeds",
		"fixtures.structured_ranges", "fixtures.structured_ranges.from",
		"fixtures.structured_ranges.to", "fixtures.structured_ranges.asn",
		"fixtures.structured_ranges.country_id", "fixtures.structured_ranges.state_id",
		"fixtures.structured_ranges.city_id", "fixtures.structured_ranges.location",
		"fixtures.structured_ranges.location.latitude_microdegrees",
		"fixtures.structured_ranges.location.longitude_microdegrees",
		"fixtures.structured_ranges.feeds",
		"fixtures.feeds", "fixtures.feeds.name", "fixtures.feeds.index",
		"fixtures.cardinality", "fixtures.address_count",
		"invalid_cases.mutation", "invalid_cases.source",
		"invalid_cases.expected_error":
		return true
	}
	return false
}

// Conformance cross-open tests: every committed Rust-produced fixture is
// opened through the public Go API and verified against the language-neutral
// cases.json expectations, and the three invalid mutations are rejected with
// the exact typed error.

type conformanceCase struct {
	File             string            `json:"file"`
	Producer         string            `json:"producer"`
	Family           string            `json:"family"`
	Kind             string            `json:"kind"`
	Tag              string            `json:"tag"`
	Structure        string            `json:"structure,omitempty"`
	Metadata         metadataExpect    `json:"metadata"`
	AddressCount     string            `json:"address_count"`
	DirectRanges     []directExpect    `json:"direct_ranges,omitempty"`
	Feeds            []feedExpect      `json:"feeds,omitempty"`
	MembershipRanges []membershipRange `json:"membership_ranges,omitempty"`
	StructuredRanges []structuredRange `json:"structured_ranges,omitempty"`
}

type metadataExpect struct {
	State string          `json:"state"`
	Value json.RawMessage `json:"value"`
	Byte  json.RawMessage `json:"byte"`
	Len   json.RawMessage `json:"length"`
}

type directExpect struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Value uint32 `json:"value"`
}

type feedExpect struct {
	Name  string `json:"name"`
	Index uint32 `json:"index"`
}

type membershipRange struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Feeds []string `json:"feeds"`
}

type structuredRange struct {
	From      string `json:"from"`
	To        string `json:"to"`
	ASN       uint32 `json:"asn"`
	CountryID uint32 `json:"country_id"`
	StateID   uint32 `json:"state_id"`
	CityID    uint32 `json:"city_id"`
	Location  *struct {
		Lat  int32 `json:"latitude_microdegrees"`
		Long int32 `json:"longitude_microdegrees"`
	} `json:"location"`
	Feeds []string `json:"feeds"`
}

type conformanceManifest struct {
	Schema   int               `json:"schema"`
	Fixtures []conformanceCase `json:"fixtures"`
	Invalid  []invalidExpect   `json:"invalid_cases"`
}

type invalidExpect struct {
	Source        string `json:"source"`
	Mutation      string `json:"mutation"`
	ExpectedError string `json:"expected_error"`
}

func loadManifest(t *testing.T) conformanceManifest {
	t.Helper()
	raw, err := os.ReadFile("../conformance/cases.json")
	if err != nil {
		t.Fatal("read cases.json:", err)
	}
	// Parse with a strict decoder that rejects case-insensitive field names
	// and duplicate fields, mirroring Rust's serde deny_unknown_fields
	// and derived deserializer semantics.
	var m conformanceManifest
	if err := strictUnmarshal(raw, &m); err != nil {
		t.Fatal("parse cases.json:", err)
	}

	// Reject an unsupported manifest schema, mirroring Rust
	// conformance_support/mod.rs:23.
	if m.Schema != 2 {
		t.Fatalf("unsupported conformance schema %d, want 2", m.Schema)
	}
	// Compare the manifest inventory against the actual directory: every
	// committed .iprdb fixture must be listed, and no extra .iprdb file
	// may exist, mirroring Rust's assert_fixture_inventory (verify.rs:77-99).
	manifestFiles := map[string]bool{}
	for _, fx := range m.Fixtures {
		manifestFiles[fx.File] = true
	}
	var diskFiles []string
	err = filepath.Walk("../conformance", func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && strings.HasSuffix(path, ".iprdb") {
			rel, err := filepath.Rel("../conformance", path)
			if err != nil {
				return err
			}
			diskFiles = append(diskFiles, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal("walk conformance dir:", err)
	}
	for _, f := range diskFiles {
		if !manifestFiles[f] {
			t.Fatalf("committed fixture %s not in manifest", f)
		}
	}
	// Enforce the exact fixture inventory and producer coverage: all six
	// committed Rust fixtures must be present with producer "rust", and
	// no extra fixtures may appear.
	want := []string{
		"rust/direct-ipv4.iprdb",
		"rust/first-seen-ipv6.iprdb",
		"rust/membership-ipv4.iprdb",
		"rust/membership-ipv6.iprdb",
		"rust/structured-ipv4.iprdb",
		"rust/structured-ipv4-nothreat.iprdb",
	}
	if len(m.Fixtures) != len(want) {
		t.Fatalf("fixture count %d, want %d", len(m.Fixtures), len(want))
	}
	seen := map[string]bool{}
	for _, fx := range m.Fixtures {
		if fx.Producer != "rust" {
			t.Fatalf("fixture %s: producer %q, want rust", fx.File, fx.Producer)
		}
		seen[fx.File] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("fixture %s missing from manifest", w)
		}
	}
	// Enforce the exact invalid-mutation inventory, mirroring Rust
	// conformance_support/verify.rs:31.
	if len(m.Invalid) != 3 {
		t.Fatalf("invalid case count %d, want 3", len(m.Invalid))
	}
	// Reject explicit null range arrays: a typed vector in Rust rejects
	// null, so Go must too. A nil slice from an omitted field is valid;
	// a nil slice from explicit null is not distinguishable after
	// unmarshaling, so we re-check the raw JSON for the field name.
	// The check runs on the original manifest bytes, not re-marshaled
	// structs (omitempty would erase the null before checking).
	var rawFixtures []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &struct {
		Fixtures *[]map[string]json.RawMessage `json:"fixtures"`
	}{Fixtures: &rawFixtures}); err != nil {
		t.Fatal("parse cases.json fixtures:", err)
	}
	for i, rawFx := range rawFixtures {
		for _, field := range []string{"direct_ranges", "membership_ranges", "structured_ranges", "feeds"} {
			if raw, ok := rawFx[field]; ok && string(raw) == "null" {
				t.Fatalf("fixture %d: %s is explicitly null", i, field)
			}
		}
	}
	// Reject wrong-kind range arrays: a direct fixture must have no
	// membership or structured ranges, and so on, mirroring Rust's
	// assert_direct/assert_membership/assert_structured empty-array
	// assertions (verify.rs:146-148, 208-209, 248-249).
	for _, fx := range m.Fixtures {
		switch fx.Kind {
		case "direct":
			if len(fx.MembershipRanges) > 0 || len(fx.StructuredRanges) > 0 {
				t.Fatalf("direct fixture %s has membership/structured ranges", fx.File)
			}
		case "membership":
			if len(fx.DirectRanges) > 0 || len(fx.StructuredRanges) > 0 {
				t.Fatalf("membership fixture %s has direct/structured ranges", fx.File)
			}
		case "structured":
			if len(fx.DirectRanges) > 0 || len(fx.MembershipRanges) > 0 {
				t.Fatalf("structured fixture %s has direct/membership ranges", fx.File)
			}
		}
	}
	// Validate canonical range invariants, mirroring Rust's
	// assert_canonical_memberships (verify.rs:468-482) and
	// assert_canonical_structures (verify.rs:382-395): bounds, ordering,
	// no overlap, and no uncoalesced adjacent ranges.
	// addr128 returns the address as (hi, lo) for comparison; for IPv4,
	// hi=0 and lo is the 32-bit address.
	addr128 := func(s string, family string) (uint64, uint64) {
		h, l, v4 := addressBytes(s, family)
		if family == "ipv4" {
			return 0, uint64(v4)
		}
		return h, l
	}
	for _, fx := range m.Fixtures {
		family := fx.Family
		// Membership ranges.
		var prevToHi, prevToLo uint64
		var prevFeeds []string
		for i, mr := range fx.MembershipRanges {
			fh, fl := addr128(mr.From, family)
			th, tl := addr128(mr.To, family)
			if fh > th || (fh == th && fl > tl) {
				t.Fatalf("membership fixture %s range %d: from > to", fx.File, i)
			}
			if len(mr.Feeds) == 0 {
				t.Fatalf("membership fixture %s range %d: empty feeds", fx.File, i)
			}
			if i > 0 {
				if prevToHi > fh || (prevToHi == fh && prevToLo >= fl) {
					t.Fatalf("membership fixture %s range %d: overlaps previous range", fx.File, i)
				}
				// Adjacent ranges with identical feeds must be coalesced.
				// Handle low-word overflow: prevToLo == MaxUint64 means
				// the next address is prevToHi+1, 0.
				adjacent := false
				if prevToLo < ^uint64(0) {
					adjacent = prevToHi == fh && prevToLo+1 == fl
				} else {
					adjacent = prevToHi+1 == fh && fl == 0
				}
				if adjacent {
					same := len(prevFeeds) == len(mr.Feeds)
					if same {
						for j := range prevFeeds {
							if prevFeeds[j] != mr.Feeds[j] {
								same = false
								break
							}
						}
					}
					if same {
						t.Fatalf("membership fixture %s range %d: uncoalesced with previous", fx.File, i)
					}
				}
			}
			prevToHi, prevToLo = th, tl
			prevFeeds = mr.Feeds
		}
		// Structured ranges.
		var prevSToHi, prevSToLo uint64
		var prevSASN, prevSCountry, prevSState, prevSCity uint32
		var prevSFeeds []string
		var prevSLoc *struct {
			Lat  int32 `json:"latitude_microdegrees"`
			Long int32 `json:"longitude_microdegrees"`
		}
		for i, sr := range fx.StructuredRanges {
			fh, fl := addr128(sr.From, family)
			th, tl := addr128(sr.To, family)
			if fh > th || (fh == th && fl > tl) {
				t.Fatalf("structured fixture %s range %d: from > to", fx.File, i)
			}
			if i > 0 {
				if prevSToHi > fh || (prevSToHi == fh && prevSToLo >= fl) {
					t.Fatalf("structured fixture %s range %d: overlaps previous range", fx.File, i)
				}
				// Adjacent ranges with identical values must be coalesced.
				// Handle low-word overflow: prevSToLo == MaxUint64 means
				// the next address is prevSToHi+1, 0.
				sAdjacent := false
				if prevSToLo < ^uint64(0) {
					sAdjacent = prevSToHi == fh && prevSToLo+1 == fl
				} else {
					sAdjacent = prevSToHi+1 == fh && fl == 0
				}
				sameLoc := (prevSLoc == nil) == (sr.Location == nil)
				if sameLoc && prevSLoc != nil {
					sameLoc = prevSLoc.Lat == sr.Location.Lat && prevSLoc.Long == sr.Location.Long
				}
				if sAdjacent &&
					prevSASN == sr.ASN && prevSCountry == sr.CountryID &&
					prevSState == sr.StateID && prevSCity == sr.CityID &&
					sameLoc {
					same := len(prevSFeeds) == len(sr.Feeds)
					if same {
						for j := range prevSFeeds {
							if prevSFeeds[j] != sr.Feeds[j] {
								same = false
								break
							}
						}
					}
					if same {
						t.Fatalf("structured fixture %s range %d: uncoalesced with previous", fx.File, i)
					}
				}
			}
			prevSToHi, prevSToLo = th, tl
			prevSASN, prevSCountry, prevSState, prevSCity = sr.ASN, sr.CountryID, sr.StateID, sr.CityID
			prevSFeeds = sr.Feeds
			prevSLoc = sr.Location
		}
	}
	// Validate direct-range canonical invariants, mirroring Rust's
	// assert_canonical_direct (verify.rs:193-205): bounds, ordering,
	// no overlap, and no uncoalesced adjacent ranges with equal values.
	for _, fx := range m.Fixtures {
		family := fx.Family
		var prevToHi, prevToLo uint64
		var prevValue uint32
		for i, dr := range fx.DirectRanges {
			fh, fl := addr128(dr.From, family)
			th, tl := addr128(dr.To, family)
			if fh > th || (fh == th && fl > tl) {
				t.Fatalf("direct fixture %s range %d: from > to", fx.File, i)
			}
			if i > 0 {
				if prevToHi > fh || (prevToHi == fh && prevToLo >= fl) {
					t.Fatalf("direct fixture %s range %d: overlaps previous range", fx.File, i)
				}
				// Adjacent ranges with equal values must be coalesced.
				adjacent := false
				if prevToLo < ^uint64(0) {
					adjacent = prevToHi == fh && prevToLo+1 == fl
				} else {
					adjacent = prevToHi+1 == fh && fl == 0
				}
				if adjacent && prevValue == uint32(dr.Value) {
					t.Fatalf("direct fixture %s range %d: uncoalesced with previous", fx.File, i)
				}
			}
			prevToHi, prevToLo = th, tl
			prevValue = uint32(dr.Value)
		}
	}
	// Reject state-inapplicable metadata fields, mirroring Rust's tagged
	// MetadataExpectation enum (model.rs:126-133): absent/empty must not
	// carry value/byte/length; text must not carry byte/length; repeat
	// must carry byte and length but not value. Presence-aware: a
	// json.RawMessage is nil when the field is omitted, non-nil when
	// explicitly present (even as zero/empty).
	for _, fx := range m.Fixtures {
		switch fx.Metadata.State {
		case "absent", "empty":
			if fx.Metadata.Value != nil || fx.Metadata.Byte != nil || fx.Metadata.Len != nil {
				t.Fatalf("fixture %s: metadata state %q carries value/byte/length", fx.File, fx.Metadata.State)
			}
		case "text":
			if fx.Metadata.Byte != nil || fx.Metadata.Len != nil {
				t.Fatalf("fixture %s: metadata state %q carries byte/length", fx.File, fx.Metadata.State)
			}
		case "repeat":
			if fx.Metadata.Value != nil {
				t.Fatalf("fixture %s: metadata state %q carries value", fx.File, fx.Metadata.State)
			}
			if fx.Metadata.Byte == nil || fx.Metadata.Len == nil {
				t.Fatalf("fixture %s: metadata state %q missing byte/length", fx.File, fx.Metadata.State)
			}
			var byteVal int
			if err := json.Unmarshal(fx.Metadata.Byte, &byteVal); err != nil {
				t.Fatalf("fixture %s: metadata byte: %v", fx.File, err)
			}
			if byteVal < 0 || byteVal > 255 {
				t.Fatalf("fixture %s: metadata byte %d out of range", fx.File, byteVal)
			}
		}
	}
	// Validate that feeds arrays are present (not nil) for fixtures that
	// declare feeds in their ranges, mirroring Rust's typed vector fields
	// (model.rs:70-79). The top-level feeds array is optional (Rust
	// defaults it at model.rs:74-75), but every membership or structured
	// range must carry its own feeds array.
	for _, fx := range m.Fixtures {
		for i, mr := range fx.MembershipRanges {
			if mr.Feeds == nil {
				t.Fatalf("fixture %s membership range %d: feeds array is missing", fx.File, i)
			}
		}
		for i, sr := range fx.StructuredRanges {
			if sr.Feeds == nil {
				t.Fatalf("fixture %s structured range %d: feeds array is missing", fx.File, i)
			}
		}
	}
	// Validate feed index presence and type: every feed must carry its
	// index field as a valid u32 (Rust's required u32 rejects null; Go's
	// uint32 accepts null as zero, so we check the raw JSON).
	for _, rawFx := range rawFixtures {
		if rawFeeds, ok := rawFx["feeds"]; ok && string(rawFeeds) != "null" {
			var feeds []map[string]json.RawMessage
			if err := json.Unmarshal(rawFeeds, &feeds); err != nil {
				t.Fatalf("feeds decode: %v", err)
			}
			for i, rawFeed := range feeds {
				rawIdx, ok := rawFeed["index"]
				if !ok {
					t.Fatalf("feed %d: index field is missing", i)
				}
				if string(rawIdx) == "null" {
					t.Fatalf("feed %d: index is null", i)
				}
				var idx uint32
				if err := json.Unmarshal(rawIdx, &idx); err != nil {
					t.Fatalf("feed %d: index is not a valid u32: %v", i, err)
				}
			}
		}
	}
	// Reject duplicate feed names or indices within any fixture, mirroring
	// Rust's exact catalog comparison (verify.rs:253-267).
	for _, fx := range m.Fixtures {
		names := map[string]bool{}
		indices := map[uint32]bool{}
		for _, f := range fx.Feeds {
			if names[f.Name] {
				t.Fatalf("fixture %s: duplicate feed name %q", fx.File, f.Name)
			}
			names[f.Name] = true
			if indices[f.Index] {
				t.Fatalf("fixture %s: duplicate feed index %d", fx.File, f.Index)
			}
			indices[f.Index] = true
		}
	}
	return m
}

func fixturePath(rel string) string {
	return filepath.Join("..", "conformance", filepath.FromSlash(rel))
}

func mustOpen(t *testing.T, rel string) *ImmutableReader {
	t.Helper()
	db, err := OpenImmutable(fixturePath(rel))
	if err != nil {
		t.Fatalf("open %s: %v", rel, err)
	}
	return db
}

// mustClose asserts that Close succeeds; a leaked pin would make it report
// ErrorHandleBusy and fail the cleanup.
func mustClose(t *testing.T, db *ImmutableReader) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// mustClosePin asserts that a pin closes exactly once.
func mustClosePin(t *testing.T, pin *Pin) {
	t.Helper()
	if err := pin.Close(); err != nil {
		t.Errorf("pin close: %v", err)
	}
}

func parseV4(s string) IPv4 {
	// Strict dotted-quad parse: exactly four octets, each 0-255, with
	// full input consumption. fmt.Sscanf accepts trailing text and
	// out-of-range octets; this parser rejects both.
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		panic("bad ipv4 " + s)
	}
	var v uint32
	for _, part := range parts {
		if part == "" || len(part) > 3 {
			panic("bad ipv4 octet " + s)
		}
		var octet uint32
		for _, c := range part {
			if c < '0' || c > '9' {
				panic("bad ipv4 octet " + s)
			}
			octet = octet*10 + uint32(c-'0')
		}
		if octet > 255 {
			panic("bad ipv4 octet " + s)
		}
		v = v<<8 | octet
	}
	return IPv4(v)
}

func parseV6Full(s string) IPv6 {
	// Accept the two fixture shapes used by cases.json: full forms and
	// 2001:db8::ffff style. Fixtures use only full 8-group or the two forms
	// below; parse by expanding "::".
	expanded := expandV6(s)
	if len(expanded) != 16 {
		panic("bad ipv6 " + s)
	}
	var v IPv6
	for i := 0; i < 8; i++ {
		w := uint64(expanded[2*i])<<8 | uint64(expanded[2*i+1])
		if i < 4 {
			v.Hi = v.Hi<<16 | w
		} else {
			v.Lo = v.Lo<<16 | w
		}
	}
	return v
}

func expandV6(s string) []byte {
	// Strict IPv6 parse: exactly 8 groups, at most one "::", each group
	// 1-4 hex digits, full input consumption. netip.ParseAddr normalizes
	// malformed forms; this parser rejects them.
	var before, after []string
	var double bool
	cur := ""
	flush := func() {
		if cur != "" {
			if double {
				after = append(after, cur)
			} else {
				before = append(before, cur)
			}
			cur = ""
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			// Check for triple colon BEFORE consuming the double colon.
			if i+2 < len(s) && s[i+1] == ':' && s[i+2] == ':' {
				panic("bad ipv6 triple colon " + s)
			}
			if !double && i+1 < len(s) && s[i+1] == ':' {
				flush()
				double = true
				i++
			} else if double && i+1 < len(s) && s[i+1] == ':' {
				// A second "::" is invalid.
				panic("bad ipv6 multiple :: " + s)
			} else {
				flush()
				// A leading single colon is invalid (only "::" can start).
				if i == 0 {
					panic("bad ipv6 leading colon " + s)
				}
				// A trailing single colon is invalid.
				if i+1 == len(s) {
					panic("bad ipv6 trailing colon " + s)
				}
			}
		default:
			cur += string(s[i])
		}
	}
	if cur != "" {
		if double {
			after = append(after, cur)
		} else {
			before = append(before, cur)
		}
	}
	// Validate group count: before + after must total exactly 8 when
	// there is no "::", or at most 7 when there is one.
	if !double && len(before) != 8 {
		panic("bad ipv6 group count " + s)
	}
	if double && len(before)+len(after) > 7 {
		panic("bad ipv6 group count " + s)
	}
	// Validate each group is 1-4 hex digits.
	for _, g := range before {
		if len(g) == 0 || len(g) > 4 {
			panic("bad ipv6 group " + s)
		}
		for _, c := range g {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				panic("bad ipv6 group " + s)
			}
		}
	}
	for _, g := range after {
		if len(g) == 0 || len(g) > 4 {
			panic("bad ipv6 group " + s)
		}
		for _, c := range g {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				panic("bad ipv6 group " + s)
			}
		}
	}
	out := make([]byte, 16)
	write := func(groups []string, start int) {
		for gi, g := range groups {
			var v uint16
			for i := 0; i < len(g); i++ {
				var d uint16
				switch {
				case g[i] >= '0' && g[i] <= '9':
					d = uint16(g[i] - '0')
				case g[i] >= 'a' && g[i] <= 'f':
					d = uint16(g[i]-'a') + 10
				case g[i] >= 'A' && g[i] <= 'F':
					d = uint16(g[i]-'A') + 10
				default:
					panic("bad hex " + s)
				}
				v = v<<4 | d
			}
			pos := (start + gi) * 2
			out[pos] = byte(v >> 8)
			out[pos+1] = byte(v)
		}
	}
	write(before, 0)
	write(after, 8-len(after))
	return out
}

func addressBytes(s string, family string) (hi, lo uint64, v4 uint32) {
	if family == "ipv4" {
		return 0, 0, uint32(parseV4(s))
	}
	v := parseV6Full(s)
	return v.Hi, v.Lo, 0
}

// TestConformanceRustFixtures opens every committed Rust-produced fixture and
// verifies the exact cases.json semantics.
func TestConformanceRustFixtures(t *testing.T) {
	m := loadManifest(t)
	for _, tc := range m.Fixtures {
		tc := tc
		t.Run(tc.File, func(t *testing.T) {
			db := mustOpen(t, tc.File)
			t.Cleanup(func() { mustClose(t, db) })
			pin, err := db.Pin()
			if err != nil {
				t.Fatal("pin:", err)
			}
			t.Cleanup(func() { mustClosePin(t, pin) })
			info, err := db.Info()
			if err != nil {
				t.Fatal(err)
			}

			wantFamily := uint8(4)
			if tc.Family == "ipv6" {
				wantFamily = 6
			}
			if uint8(info.Family) != wantFamily {
				t.Errorf("family %d want %d", info.Family, wantFamily)
			}
			var wantKind ValueKind
			switch tc.Kind {
			case "direct":
				wantKind = ValueKindDirect
			case "membership":
				wantKind = ValueKindMembership
			case "structured":
				wantKind = ValueKindStructured
			default:
				t.Fatalf("unknown kind %q", tc.Kind)
			}
			if info.ValueKind != wantKind {
				t.Errorf("kind %d want %d", info.ValueKind, wantKind)
			}
			switch tc.Structure {
			case "network-enrichment-v1":
				if info.StructureKind != StructureKindNetworkEnrichmentV1 {
					t.Errorf("structure kind %d want 1", info.StructureKind)
				}
			case "":
				if tc.Kind == "structured" {
					t.Fatalf("structured fixture %s has no structure kind", tc.File)
				}
				if info.StructureKind != StructureKindNone {
					t.Errorf("structure kind %d want 0", info.StructureKind)
				}
			default:
				t.Fatalf("unknown structure %q", tc.Structure)
			}
			// Tag, selection, and geometry assertions (verify.rs: value_tag,
			// MetaSelection::ProvenCurrent, page_count*4096 == file length).
			wantTag, err := NewValueTag([]byte(tc.Tag))
			if err != nil {
				t.Fatal("tag:", err)
			}
			if info.ValueTag != wantTag {
				t.Errorf("value tag %q want %q", info.ValueTag.Wire(), wantTag.Wire())
			}
			// DirectSemantic classification per fixture kind and tag
			// (verify.rs semantic mapping; only direct databases carry a
			// tag-derived semantic).
			wantSem, wantOK := DirectSemanticGeneric, tc.Kind == "direct"
			if tc.Tag == "first_seen" {
				wantSem = DirectSemanticFirstSeen
			}
			if tc.Tag == "last_seen" {
				wantSem = DirectSemanticLastSeen
			}
			gotSem, gotOK := info.DirectSemantic()
			if gotSem != wantSem || gotOK != wantOK {
				t.Errorf("direct semantic %d/%v want %d/%v", gotSem, gotOK, wantSem, wantOK)
			}
			if info.MetaSelection != MetaSelectionProvenCurrent {
				t.Errorf("meta selection %v want ProvenCurrent", info.MetaSelection)
			}
			st, err := os.Stat(fixturePath(tc.File))
			if err != nil {
				t.Fatal("stat:", err)
			}
			if info.PageCount*4096 != uint64(st.Size()) {
				t.Errorf("page count %d * 4096 != file size %d", info.PageCount, st.Size())
			}
			switch tc.Kind {
			case "direct":
				if info.RangeRecordCount != uint64(len(tc.DirectRanges)) {
					t.Errorf("range record count %d want %d", info.RangeRecordCount, len(tc.DirectRanges))
				}
				if info.ActiveFeedCount != 0 {
					t.Errorf("direct fixture with %d active feeds", info.ActiveFeedCount)
				}
			case "membership":
				if info.RangeRecordCount != uint64(len(tc.MembershipRanges)) {
					t.Errorf("range record count %d want %d", info.RangeRecordCount, len(tc.MembershipRanges))
				}
			case "structured":
				if info.RangeRecordCount != uint64(len(tc.StructuredRanges)) {
					t.Errorf("range record count %d want %d", info.RangeRecordCount, len(tc.StructuredRanges))
				}
			}

			// Metadata states.
			meta, present, err := db.MetadataJSON()
			if err != nil {
				t.Fatal("metadata:", err)
			}
			switch tc.Metadata.State {
			case "absent":
				if present || meta != nil {
					t.Errorf("metadata absent: present=%v len=%d", present, len(meta))
				}
			case "empty":
				if !present || len(meta) != 0 {
					t.Errorf("metadata empty: present=%v len=%d", present, len(meta))
				}
			case "text":
				var want string
				if err := json.Unmarshal(tc.Metadata.Value, &want); err != nil {
					t.Fatalf("metadata text value: %v", err)
				}
				if !present || string(meta) != want {
					t.Errorf("metadata text: present=%v got %q want %q", present, meta, want)
				}
			case "repeat":
				var wantByte int
				var wantLen int
				if err := json.Unmarshal(tc.Metadata.Byte, &wantByte); err != nil {
					t.Fatalf("metadata repeat byte: %v", err)
				}
				if err := json.Unmarshal(tc.Metadata.Len, &wantLen); err != nil {
					t.Fatalf("metadata repeat length: %v", err)
				}
				if !present || !repeatEqual(meta, byte(wantByte), len(meta)) {
					t.Errorf("metadata repeat: present=%v len=%d want len=%d byte=%d", present, len(meta), wantLen, wantByte)
				}
				if len(meta) != wantLen {
					t.Errorf("metadata repeat length %d want %d", len(meta), wantLen)
				}
			default:
				t.Fatalf("unknown metadata state %q", tc.Metadata.State)
			}

			// Cardinality string.
			card, err := db.Cardinality()
			if err != nil {
				t.Fatal("cardinality:", err)
			}
			if card.String() != tc.AddressCount {
				t.Errorf("cardinality %s want %s", card.String(), tc.AddressCount)
			}

			// Direct ranges.
			for _, want := range tc.DirectRanges {
				fromHi, fromLo, from4 := addressBytes(want.From, tc.Family)
				toHi, toLo, to4 := addressBytes(want.To, tc.Family)
				mid4 := from4 + 1
				midHi, midLo := fromHi, fromLo
				midLo++
				if midLo == 0 {
					midHi++
				}
				if tc.Family == "ipv4" {
					if mid4 != to4 {
						c, ok3, err := db.LookupDirectV4(IPv4(mid4))
						if err != nil || !ok3 || c != want.Value {
							t.Errorf("direct %s mid: got %d %v %v want %d", want.From, c, ok3, err, want.Value)
						}
					}
					v, ok, err := db.LookupDirectV4(IPv4(from4))
					if err != nil || !ok || v != want.Value {
						t.Errorf("direct %s: got %d %v %v", want.From, v, ok, err)
					}
					v, ok, _ = db.LookupDirectV4(IPv4(to4))
					if !ok || v != want.Value {
						t.Errorf("direct %s: got %d %v", want.To, v, ok)
					}
					// boundary minus one must be absent or different
					if from4 > 0 {
						prev, ok, _ := db.LookupDirectV4(IPv4(from4 - 1))
						if ok && prev == want.Value {
							t.Errorf("direct before %s: same value leaks beyond range", want.From)
						}
					}
				} else {
					if fromHi != toHi || fromLo != toLo {
						// Midpoint probe of the v6 range.
						c, ok3, err := db.LookupDirectV6(IPv6{Hi: midHi, Lo: midLo})
						if err != nil || !ok3 || c != want.Value {
							t.Errorf("direct v6 %s mid: got %d %v %v want %d", want.From, c, ok3, err, want.Value)
						}
					}
					v, ok, err := db.LookupDirectV6(IPv6{Hi: fromHi, Lo: fromLo})
					if err != nil || !ok || v != want.Value {
						t.Errorf("direct v6 %s: got %d %v %v", want.From, v, ok, err)
					}
					v, ok, _ = db.LookupDirectV6(IPv6{Hi: toHi, Lo: toLo})
					if !ok || v != want.Value {
						t.Errorf("direct v6 %s: got %d %v", want.To, v, ok)
					}
				}
			}

			// Absence at the family edges and inside inter-range gaps.
			if tc.Family == "ipv4" && len(tc.DirectRanges) > 0 {
				first, last := tc.DirectRanges[0], tc.DirectRanges[len(tc.DirectRanges)-1]
				_, _, f4 := addressBytes(first.From, "ipv4")
				_, _, l4 := addressBytes(last.To, "ipv4")
				if f4 > 0 {
					if _, ok, err := db.LookupDirectV4(IPv4(0)); err != nil || ok {
						t.Errorf("0.0.0.0: found=%v err=%v want absent", ok, err)
					}
				}
				if l4 < 0xffffffff {
					if _, ok, err := db.LookupDirectV4(IPv4(0xffffffff)); err != nil || ok {
						t.Errorf("255.255.255.255: found=%v err=%v want absent", ok, err)
					}
				}
				for i := 1; i < len(tc.DirectRanges); i++ {
					_, _, prevTo := addressBytes(tc.DirectRanges[i-1].To, "ipv4")
					_, _, curFrom := addressBytes(tc.DirectRanges[i].From, "ipv4")
					if curFrom > prevTo+1 {
						if _, ok, err := db.LookupDirectV4(IPv4(prevTo + 1)); err != nil || ok {
							t.Errorf("gap after %s: found=%v err=%v want absent", tc.DirectRanges[i-1].To, ok, err)
						}
					}
				}
			} else if tc.Family == "ipv6" && len(tc.DirectRanges) > 0 {
				first, last := tc.DirectRanges[0], tc.DirectRanges[len(tc.DirectRanges)-1]
				fh, fl, _ := addressBytes(first.From, "ipv6")
				th, tl, _ := addressBytes(last.To, "ipv6")
				if fh != 0 || fl != 0 {
					if _, ok, err := db.LookupDirectV6(IPv6{Hi: 0, Lo: 0}); err != nil || ok {
						t.Errorf(":: : found=%v err=%v want absent", ok, err)
					}
				}
				if th != ^uint64(0) || tl != ^uint64(0) {
					if _, ok, err := db.LookupDirectV6(IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}); err != nil || ok {
						t.Errorf("ffff:…: found=%v err=%v want absent", ok, err)
					}
				}
			}

			// Exact range enumeration: the public scan must yield exactly the
			// canonical ranges, in ascending order, with no extra records and
			// a total equal to the declared address count. Only direct
			// fixtures have direct ranges.
			// Feed catalog checks apply to every fixture: feed-bearing
			// fixtures must resolve every declared feed, and no-feed
			// fixtures must have zero active feeds (an extra unreferenced
			// catalog feed would be a divergence from the manifest).
			if info.ActiveFeedCount != uint64(len(tc.Feeds)) {
				t.Errorf("active feed count %d want %d", info.ActiveFeedCount, len(tc.Feeds))
			}
			if len(tc.Feeds) > 0 {
				// The manifest feed array must be in strictly ascending
				// feed-index order, mirroring Rust's ordered catalog
				// vector comparison (verify.rs:216-227). Sparse indices
				// are valid; contiguity is not required.
				for i := 1; i < len(tc.Feeds); i++ {
					if tc.Feeds[i].Index <= tc.Feeds[i-1].Index {
						t.Errorf("feed %s index %d not ascending after %s index %d",
							tc.Feeds[i].Name, tc.Feeds[i].Index, tc.Feeds[i-1].Name, tc.Feeds[i-1].Index)
					}
				}
				// Every declared feed resolves to its exact index, and
				// undeclared names are absent.
				for _, feed := range tc.Feeds {
					entry, ok, err := db.LookupFeed(feed.Name)
					if err != nil || !ok {
						t.Errorf("feed %s: %v %v", feed.Name, ok, err)
						continue
					}
					if entry.Index != feed.Index {
						t.Errorf("feed %s index %d want %d", feed.Name, entry.Index, feed.Index)
					}
					if entry.Name != feed.Name {
						t.Errorf("feed %s name %q want %q", feed.Name, entry.Name, feed.Name)
					}
				}
				for _, absent := range []string{"feed-999", "zz-not-declared"} {
					en, ok, err := db.LookupFeed(absent)
					if err != nil || ok {
						t.Errorf("undeclared feed %q: %v %v", absent, ok, err)
					}
					if ok && en.Index != 0 {
						t.Errorf("undeclared feed resolved to %d", en.Index)
					}
				}
			}

			// Exact range enumeration: the public scan must yield exactly the
			// canonical ranges, in ascending order, with no extra records and
			// a total equal to the declared address count. Only direct
			// fixtures have direct ranges.
			if tc.Kind != "direct" {
				goto membershipCheck
			}
			if tc.Family == "ipv4" {
				var scanned []DirectRangeV4
				err := db.DirectRangesV4(func(r DirectRangeV4) error {
					scanned = append(scanned, r)
					return nil
				})
				if err != nil {
					t.Fatal("scan v4:", err)
				}
				if len(scanned) != len(tc.DirectRanges) {
					t.Errorf("scan count %d want %d", len(scanned), len(tc.DirectRanges))
				}
				var total uint64
				for i, r := range scanned {
					if i < len(tc.DirectRanges) {
						want := tc.DirectRanges[i]
						_, _, wf := addressBytes(want.From, "ipv4")
						_, _, wt := addressBytes(want.To, "ipv4")
						if r.From != wf || r.To != wt || r.Value != uint32(want.Value) {
							t.Errorf("scan[%d] = %x-%x=%d want %x-%x=%d", i, r.From, r.To, r.Value, wf, wt, want.Value)
						}
					}
					if i > 0 && scanned[i-1].From >= r.From {
						t.Errorf("scan not strictly ascending at %d", i)
					}
					total += uint64(r.To) - uint64(r.From) + 1
				}
				if tc.AddressCount != "" && fmt.Sprint(total) != tc.AddressCount {
					t.Errorf("scan total %d want %s", total, tc.AddressCount)
				}
			} else {
				var scanned []DirectRangeV6
				err := db.DirectRangesV6(func(r DirectRangeV6) error {
					scanned = append(scanned, r)
					return nil
				})
				if err != nil {
					t.Fatal("scan v6:", err)
				}
				if len(scanned) != len(tc.DirectRanges) {
					t.Errorf("scan v6 count %d want %d", len(scanned), len(tc.DirectRanges))
				}
				for i, r := range scanned {
					if i < len(tc.DirectRanges) {
						want := tc.DirectRanges[i]
						fh, fl, _ := addressBytes(want.From, "ipv6")
						th, tl, _ := addressBytes(want.To, "ipv6")
						if r.FromHi != fh || r.FromLo != fl || r.ToHi != th || r.ToLo != tl || r.Value != uint32(want.Value) {
							t.Errorf("scan v6[%d] mismatch", i)
						}
					}
					if i > 0 {
						prev, cur := scanned[i-1], r
						if prev.FromHi > cur.FromHi || (prev.FromHi == cur.FromHi && prev.FromLo >= cur.FromLo) {
							t.Errorf("scan v6 not strictly ascending at %d", i)
						}
					}
				}
			}

			// Membership ranges.
		membershipCheck:
			feedIndexOf := func(name string) uint32 {
				for _, f := range tc.Feeds {
					if f.Name == name {
						return f.Index
					}
				}
				t.Fatalf("feed %s not declared", name)
				return 0
			}
			// verifyMembershipView checks the exact bitmap semantics of a
			// membership view against the expected feeds for one range,
			// mirroring Rust assert_membership_at: word count, exact words,
			// and every listed/unlisted feed.
			verifyMembershipView := func(view MembershipView, feeds []string, label string) {
				listed := map[uint32]bool{}
				for _, feed := range feeds {
					listed[feedIndexOf(feed)] = true
				}
				for _, f := range tc.Feeds {
					has, err := view.ContainsIndex(f.Index)
					if err != nil {
						t.Fatalf("%s contains %s: %v", label, f.Name, err)
					}
					if listed[f.Index] && !has {
						t.Errorf("%s lacks feed %s", label, f.Name)
					}
					if !listed[f.Index] && has {
						t.Errorf("%s contains undeclared feed %s (%d)", label, f.Name, f.Index)
					}
				}
				maxBit := uint32(0)
				for _, feed := range feeds {
					if idx := feedIndexOf(feed); idx > maxBit {
						maxBit = idx
					}
				}
				wantWords := int(maxBit/64 + 1)
				gotWords, err := view.WordCount()
				if err != nil {
					t.Fatalf("%s word count: %v", label, err)
				}
				if int(gotWords) != wantWords {
					t.Errorf("%s word count %d want %d", label, gotWords, wantWords)
				}
				words := make([]uint64, wantWords)
				n, err := view.ReadWords(0, words)
				if err != nil || n != wantWords {
					t.Errorf("%s read words: n=%d err=%v", label, n, err)
				}
				expected := make([]uint64, wantWords)
				for _, feed := range feeds {
					idx := feedIndexOf(feed)
					expected[idx/64] |= uint64(1) << (idx % 64)
				}
				for i := range expected {
					if words[i] != expected[i] {
						t.Errorf("%s word %d = %x want %x", label, i, words[i], expected[i])
					}
				}
			}
			for _, mr := range tc.MembershipRanges {
				_, _, from4 := addressBytes(mr.From, tc.Family)
				fh, fl, _ := addressBytes(mr.From, tc.Family)
				th, tl, _ := addressBytes(mr.To, tc.Family)
				var view MembershipView
				var ok bool
				var err error
				if tc.Family == "ipv4" {
					view, ok, err = pin.LookupMembershipV4(IPv4(from4))
				} else {
					view, ok, err = pin.LookupMembershipV6(IPv6{Hi: fh, Lo: fl})
				}
				if err != nil || !ok {
					t.Errorf("membership %s: %v %v", mr.From, ok, err)
					continue
				}
				// Verify the from view's exact bitmap before the midpoint
				// probe replaces it.
				verifyMembershipView(view, mr.Feeds, "membership "+mr.From)
				// Midpoint probe as well: verify its bitmap separately.
				mid4 := from4
				if tc.Family == "ipv4" {
					mid4 = from4 + (addressBytes4To(mr.To)-from4)/2
					if mid4 != from4 {
						v2, ok2, err2 := pin.LookupMembershipV4(IPv4(mid4))
						if err2 != nil || !ok2 {
							t.Errorf("membership mid %s: %v %v", mr.From, ok2, err2)
							continue
						}
						verifyMembershipView(v2, mr.Feeds, "membership mid "+mr.From)
					}
				} else {
					// Midpoint probe only when the range spans more than one
					// address (from != to). A singleton range's midpoint is
					// out of range.
					if fh != th || fl != tl {
						midLo := fl + 1
						midHi := fh
						if midLo == 0 {
							midHi++
						}
						v2, ok2, err2 := pin.LookupMembershipV6(IPv6{Hi: midHi, Lo: midLo})
						if err2 != nil || !ok2 {
							t.Errorf("membership v6 mid %s: %v %v", mr.From, ok2, err2)
							continue
						}
						verifyMembershipView(v2, mr.Feeds, "membership v6 mid "+mr.From)
					}
				}
				// Any index at or beyond the generation limit is
				// InvalidArgument; 0xffffffff is always beyond any limit.
				if _, err := view.ContainsIndex(0xffffffff); err == nil || errorAsCode(err) != ErrorInvalidArgument {
					t.Errorf("membership %s feed-limit probe: %v", mr.From, err)
				}
			}

			// Absence at the family edges and inside inter-range gaps of the
			// membership ranges.
			if len(tc.MembershipRanges) > 0 {
				// probeMembership returns (present, error). An error is
				// never "present": the endpoint checks must distinguish
				// a valid view from a lookup failure.
				probeMembership := func(hi, lo uint64) (bool, error) {
					if tc.Family == "ipv4" {
						_, ok, err := pin.LookupMembershipV4(IPv4(uint32(lo)))
						return ok, err
					}
					_, ok, err := pin.LookupMembershipV6(IPv6{Hi: hi, Lo: lo})
					return ok, err
				}
				first, last := tc.MembershipRanges[0], tc.MembershipRanges[len(tc.MembershipRanges)-1]
				fh, fl, _ := addressBytes(first.From, tc.Family)
				th, tl, _ := addressBytes(last.To, tc.Family)
				if tc.Family == "ipv4" {
					_, _, f4 := addressBytes(first.From, "ipv4")
					if f4 > 0 {
						if present, err := probeMembership(0, uint64(f4-1)); err != nil || present {
							t.Errorf("membership from-1 (0x%x): present=%v err=%v, want absent", f4-1, present, err)
						}
					}
					if f4 > 0 {
						if present, err := probeMembership(0, 0); err != nil || present {
							t.Errorf("membership 0.0.0.0: present=%v err=%v, want absent", present, err)
						}
					}
					if th < 0xffffffff {
						if present, err := probeMembership(0, 0xffffffff); err != nil || present {
							t.Errorf("membership 255.255.255.255: present=%v err=%v, want absent", present, err)
						}
					}
					for i := 1; i < len(tc.MembershipRanges); i++ {
						_, _, prevTo := addressBytes(tc.MembershipRanges[i-1].To, "ipv4")
						_, _, curFrom := addressBytes(tc.MembershipRanges[i].From, "ipv4")
						if curFrom > prevTo+1 {
							if present, err := probeMembership(0, uint64(prevTo+1)); err != nil || present {
								t.Errorf("membership gap after %s: present=%v err=%v, want absent", tc.MembershipRanges[i-1].To, present, err)
							}
						}
					}
					// Probe the exact end of each range (to) with full bitmap
					// verification, and just past it (to+1) for absence,
					// mirroring Rust membership_probes.
					for i, mr := range tc.MembershipRanges {
						_, _, to4 := addressBytes(mr.To, "ipv4")
						if present, err := probeMembership(0, uint64(to4)); err != nil || !present {
							t.Errorf("membership to %s (0x%x): present=%v err=%v, want present", mr.To, to4, present, err)
						} else {
							view, _, _ := pin.LookupMembershipV4(IPv4(to4))
							verifyMembershipView(view, mr.Feeds, "membership to "+mr.To)
						}
						if to4 < 0xffffffff {
							adjacent := i+1 < len(tc.MembershipRanges)
							if adjacent {
								_, _, nextFrom := addressBytes(tc.MembershipRanges[i+1].From, "ipv4")
								adjacent = nextFrom == to4+1
							}
							// Adjacent to+1 is the next range's from, which
							// is verified with the next range's feeds at its
							// own from probe — no separate to+1 check needed.
							if !adjacent {
								if present, err := probeMembership(0, uint64(to4+1)); err != nil || present {
									t.Errorf("membership to+1 %s (0x%x): present=%v err=%v, want absent", mr.To, to4+1, present, err)
								}
							}
						}
						// Probe just before the start (from-1) for absence,
						// except at the family minimum or when the previous
						// range ends exactly at from-1 (adjacent ranges).
						_, _, from4 := addressBytes(mr.From, "ipv4")
						if from4 > 0 {
							adjacent := false
							if i > 0 {
								_, _, prevTo := addressBytes(tc.MembershipRanges[i-1].To, "ipv4")
								adjacent = prevTo == from4-1
							}
							if !adjacent {
								if present, err := probeMembership(0, uint64(from4-1)); err != nil || present {
									t.Errorf("membership from-1 %s (0x%x): present=%v err=%v, want absent", mr.From, from4-1, present, err)
								}
							}
						}
					}
				} else {
					if fh != 0 || fl != 0 {
						if present, err := probeMembership(0, 0); err != nil || present {
							t.Errorf("membership :: : present=%v err=%v, want absent", present, err)
						}
					}
					if th != ^uint64(0) || tl != ^uint64(0) {
						if present, err := probeMembership(^uint64(0), ^uint64(0)); err != nil || present {
							t.Errorf("membership ffff:…: present=%v err=%v, want absent", present, err)
						}
					}
					// Probe the exact end of each range (to) with full bitmap
					// verification, just past it (to+1) for absence, and just
					// before the start (from-1) for absence, mirroring Rust
					// membership_probes.
					for i, mr := range tc.MembershipRanges {
						mh, ml, _ := addressBytes(mr.To, "ipv6")
						if present, err := probeMembership(mh, ml); err != nil || !present {
							t.Errorf("membership v6 to %s: present=%v err=%v, want present", mr.To, present, err)
						} else {
							view, _, _ := pin.LookupMembershipV6(IPv6{Hi: mh, Lo: ml})
							verifyMembershipView(view, mr.Feeds, "membership v6 to "+mr.To)
						}
						// to+1: absent only when the next range does not start there.
						toHi, toLo := mh, ml
						if toLo < ^uint64(0) {
							toLo++
						} else {
							toHi++
							toLo = 0
						}
						adjacent := i+1 < len(tc.MembershipRanges)
						if adjacent {
							nh, nl, _ := addressBytes(tc.MembershipRanges[i+1].From, "ipv6")
							adjacent = nh == toHi && nl == toLo
						}
						// Adjacent to+1 is the next range's from, which is
						// verified with the next range's feeds at its own
						// from probe — no separate to+1 check needed.
						if !adjacent && (toHi != 0 || toLo != 0) {
							if present, err := probeMembership(toHi, toLo); err != nil || present {
								t.Errorf("membership v6 to+1 %s: present=%v err=%v, want absent", mr.To, present, err)
							}
						}
						// from-1: absent except at the family minimum or when
						// the previous range ends exactly at from-1.
						fh2, fl2, _ := addressBytes(mr.From, "ipv6")
						if fh2 != 0 || fl2 != 0 {
							if fl2 > 0 {
								fl2--
							} else {
								fh2--
								fl2 = ^uint64(0)
							}
							adjacent := false
							if i > 0 {
								ph, pl, _ := addressBytes(tc.MembershipRanges[i-1].To, "ipv6")
								adjacent = ph == fh2 && pl == fl2
							}
							if !adjacent {
								if present, err := probeMembership(fh2, fl2); err != nil || present {
									t.Errorf("membership v6 from-1 %s: present=%v err=%v, want absent", mr.From, present, err)
								}
							}
						}
					}
				}
			}

			// Structured ranges.
			// verifyStructuredValue checks the exact structured value and
			// threat membership of a view against the expected range,
			// mirroring Rust's structured_probes + assert_structure_at.
			verifyStructuredValue := func(view NetworkEnrichmentV1View, sr structuredRange, label string) {
				val, err := view.Value()
				if err != nil {
					t.Fatalf("%s structure value: %v", label, err)
				}
				if val.ASN != sr.ASN || val.CountryID != sr.CountryID ||
					val.StateID != sr.StateID || val.CityID != sr.CityID {
					t.Errorf("%s: asn=%d/%d country=%d/%d state=%d/%d city=%d/%d",
						label, val.ASN, sr.ASN, val.CountryID, sr.CountryID,
						val.StateID, sr.StateID, val.CityID, sr.CityID)
				}
				wantLoc := sr.Location != nil
				if val.HasLocation != wantLoc {
					t.Errorf("%s: location %v want %v", label, val.HasLocation, wantLoc)
				}
				if wantLoc {
					if val.Location.LatitudeMicrodegrees != sr.Location.Lat ||
						val.Location.LongitudeMicrodegrees != sr.Location.Long {
						t.Errorf("%s: location %d,%d want %d,%d", label,
							val.Location.LatitudeMicrodegrees, val.Location.LongitudeMicrodegrees,
							sr.Location.Lat, sr.Location.Long)
					}
				}
				threat, present, err := view.ThreatMembership()
				if err != nil {
					t.Fatalf("%s threat membership: %v", label, err)
				}
				if len(sr.Feeds) == 0 {
					if present {
						t.Errorf("%s: unexpected threat membership", label)
					}
				} else {
					if !present {
						t.Errorf("%s: missing threat membership", label)
					}
					listed := map[uint32]bool{}
					for _, feed := range sr.Feeds {
						listed[feedIndexOf(feed)] = true
					}
					for _, f := range tc.Feeds {
						has, err := threat.ContainsIndex(f.Index)
						if err != nil {
							t.Fatalf("%s threat contains: %v", label, err)
						}
						if listed[f.Index] && !has {
							t.Errorf("%s lacks threat feed %s", label, f.Name)
						}
						if !listed[f.Index] && has {
							t.Errorf("%s contains undeclared threat feed %s (%d)", label, f.Name, f.Index)
						}
					}
				}
			}
			for _, sr := range tc.StructuredRanges {
				_, _, from4 := addressBytes(sr.From, tc.Family)
				fh, fl, _ := addressBytes(sr.From, tc.Family)
				var view NetworkEnrichmentV1View
				var ok bool
				var err error
				if tc.Family == "ipv4" {
					view, ok, err = pin.LookupNetworkEnrichmentV1V4(IPv4(from4))
				} else {
					view, ok, err = pin.LookupNetworkEnrichmentV1V6(IPv6{Hi: fh, Lo: fl})
				}
				if err != nil || !ok {
					t.Errorf("structured %s: %v %v", sr.From, ok, err)
					continue
				}
				verifyStructuredValue(view, sr, "structured "+sr.From)
				// Midpoint probe as well, mirroring the membership test.
				if tc.Family == "ipv4" {
					mid4 := from4 + (addressBytes4To(sr.To)-from4)/2
					if mid4 != from4 {
						v2, ok2, err2 := pin.LookupNetworkEnrichmentV1V4(IPv4(mid4))
						if err2 != nil || !ok2 {
							t.Errorf("structured mid %s: %v %v", sr.From, ok2, err2)
							continue
						}
						verifyStructuredValue(v2, sr, "structured mid "+sr.From)
					}
				} else {
					// Midpoint probe only when the range spans more than one
					// address (from != to). A singleton range's midpoint is
					// out of range.
					th2, tl2, _ := addressBytes(sr.To, "ipv6")
					if fh != th2 || fl != tl2 {
						midLo := fl + 1
						midHi := fh
						if midLo == 0 {
							midHi++
						}
						v2, ok2, err2 := pin.LookupNetworkEnrichmentV1V6(IPv6{Hi: midHi, Lo: midLo})
						if err2 != nil || !ok2 {
							t.Errorf("structured v6 mid %s: %v %v", sr.From, ok2, err2)
							continue
						}
						verifyStructuredValue(v2, sr, "structured v6 mid "+sr.From)
					}
				}
			}

			// Absence at the family edges and inside inter-range gaps of
			// the structured ranges: found=false with nil error, never a
			// stale view and never corruption (Rust verifier probes the
			// same positions).
			if len(tc.StructuredRanges) > 0 {
				// probeStructured returns (present, error). An error is
				// never "present": the endpoint checks must distinguish
				// a valid view from a lookup failure.
				probeStructured := func(hi, lo uint64) (bool, error) {
					if tc.Family == "ipv4" {
						_, ok, err := pin.LookupNetworkEnrichmentV1V4(IPv4(uint32(lo)))
						return ok, err
					}
					_, ok, err := pin.LookupNetworkEnrichmentV1V6(IPv6{Hi: hi, Lo: lo})
					return ok, err
				}
				first, last := tc.StructuredRanges[0], tc.StructuredRanges[len(tc.StructuredRanges)-1]
				fh, fl, _ := addressBytes(first.From, tc.Family)
				th, tl, _ := addressBytes(last.To, tc.Family)
				if tc.Family == "ipv4" {
					_, _, f4 := addressBytes(first.From, "ipv4")
					if f4 > 0 {
						if present, err := probeStructured(0, uint64(f4-1)); err != nil || present {
							t.Errorf("structured from-1 (0x%x): present=%v err=%v, want absent", f4-1, present, err)
						}
					}
					if f4 > 0 {
						if present, err := probeStructured(0, 0); err != nil || present {
							t.Errorf("structured 0.0.0.0: present=%v err=%v, want absent", present, err)
						}
					}
					if th < 0xffffffff {
						if present, err := probeStructured(0, 0xffffffff); err != nil || present {
							t.Errorf("structured 255.255.255.255: present=%v err=%v, want absent", present, err)
						}
					}
					for i := 1; i < len(tc.StructuredRanges); i++ {
						_, _, prevTo := addressBytes(tc.StructuredRanges[i-1].To, "ipv4")
						_, _, curFrom := addressBytes(tc.StructuredRanges[i].From, "ipv4")
						if curFrom > prevTo+1 {
							if present, err := probeStructured(0, uint64(prevTo+1)); err != nil || present {
								t.Errorf("structured gap after %s: present=%v err=%v, want absent", tc.StructuredRanges[i-1].To, present, err)
							}
						}
					}
					// Probe the exact end of each range (to) with full value
					// verification, just past it (to+1) for absence, and just
					// before the start (from-1) for absence, mirroring Rust
					// structured_probes.
					for i, sr := range tc.StructuredRanges {
						_, _, to4 := addressBytes(sr.To, "ipv4")
						if present, err := probeStructured(0, uint64(to4)); err != nil || !present {
							t.Errorf("structured to %s (0x%x): present=%v err=%v, want present", sr.To, to4, present, err)
						} else {
							view, _, _ := pin.LookupNetworkEnrichmentV1V4(IPv4(to4))
							verifyStructuredValue(view, sr, "structured to "+sr.To)
						}
						if to4 < 0xffffffff {
							adjacent := i+1 < len(tc.StructuredRanges)
							if adjacent {
								_, _, nextFrom := addressBytes(tc.StructuredRanges[i+1].From, "ipv4")
								adjacent = nextFrom == to4+1
							}
							// Adjacent to+1 is the next range's from, which
							// is verified with the next range's value at its
							// own from probe — no separate to+1 check needed.
							if !adjacent {
								if present, err := probeStructured(0, uint64(to4+1)); err != nil || present {
									t.Errorf("structured to+1 %s (0x%x): present=%v err=%v, want absent", sr.To, to4+1, present, err)
								}
							}
						}
						// from-1: absent except at the family minimum or when
						// the previous range ends exactly at from-1.
						_, _, from4 := addressBytes(sr.From, "ipv4")
						if from4 > 0 {
							adjacent := false
							if i > 0 {
								_, _, prevTo := addressBytes(tc.StructuredRanges[i-1].To, "ipv4")
								adjacent = prevTo == from4-1
							}
							if !adjacent {
								if present, err := probeStructured(0, uint64(from4-1)); err != nil || present {
									t.Errorf("structured from-1 %s (0x%x): present=%v err=%v, want absent", sr.From, from4-1, present, err)
								}
							}
						}
					}
				} else {
					if fh != 0 || fl != 0 {
						if present, err := probeStructured(0, 0); err != nil || present {
							t.Errorf("structured :: : present=%v err=%v, want absent", present, err)
						}
					}
					if th != ^uint64(0) || tl != ^uint64(0) {
						if present, err := probeStructured(^uint64(0), ^uint64(0)); err != nil || present {
							t.Errorf("structured ffff:…: present=%v err=%v, want absent", present, err)
						}
					}
					// Probe the exact end of each range (to) with full value
					// verification, just past it (to+1) for absence, and just
					// before the start (from-1) for absence, mirroring Rust
					// structured_probes.
					for i, sr := range tc.StructuredRanges {
						mh, ml, _ := addressBytes(sr.To, "ipv6")
						if present, err := probeStructured(mh, ml); err != nil || !present {
							t.Errorf("structured v6 to %s: present=%v err=%v, want present", sr.To, present, err)
						} else {
							view, _, _ := pin.LookupNetworkEnrichmentV1V6(IPv6{Hi: mh, Lo: ml})
							verifyStructuredValue(view, sr, "structured v6 to "+sr.To)
						}
						toHi, toLo := mh, ml
						if toLo < ^uint64(0) {
							toLo++
						} else {
							toHi++
							toLo = 0
						}
						adjacent := i+1 < len(tc.StructuredRanges)
						if adjacent {
							nh, nl, _ := addressBytes(tc.StructuredRanges[i+1].From, "ipv6")
							adjacent = nh == toHi && nl == toLo
						}
						// Adjacent to+1 is the next range's from, which is
						// verified with the next range's value at its own
						// from probe — no separate to+1 check needed.
						if !adjacent && (toHi != 0 || toLo != 0) {
							if present, err := probeStructured(toHi, toLo); err != nil || present {
								t.Errorf("structured v6 to+1 %s: present=%v err=%v, want absent", sr.To, present, err)
							}
						}
						// from-1: absent except at the family minimum or when
						// the previous range ends exactly at from-1.
						fh2, fl2, _ := addressBytes(sr.From, "ipv6")
						if fh2 != 0 || fl2 != 0 {
							if fl2 > 0 {
								fl2--
							} else {
								fh2--
								fl2 = ^uint64(0)
							}
							adjacent := false
							if i > 0 {
								ph, pl, _ := addressBytes(tc.StructuredRanges[i-1].To, "ipv6")
								adjacent = ph == fh2 && pl == fl2
							}
							if !adjacent {
								if present, err := probeStructured(fh2, fl2); err != nil || present {
									t.Errorf("structured v6 from-1 %s: present=%v err=%v, want absent", sr.From, present, err)
								}
							}
						}
					}
				}
			}
		})
	}
}

// TestConformanceInvalidMutations rejects the three invalid mutations with
// the exact typed error.
func TestConformanceInvalidMutations(t *testing.T) {
	m := loadManifest(t)
	for _, tc := range m.Invalid {
		tc := tc
		t.Run(tc.Mutation, func(t *testing.T) {
			src, err := os.ReadFile(fixturePath(tc.Source))
			if err != nil {
				t.Fatal(err)
			}
			mutated := mutate(t, src, tc.Mutation)
			dir := t.TempDir()
			path := filepath.Join(dir, "mutated.iprdb")
			if err := os.WriteFile(path, mutated, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = OpenImmutable(path)
			if err == nil {
				t.Fatal("expected open failure")
			}
			var public *Error
			if !errorAs(err, &public) {
				t.Fatalf("error not typed: %v", err)
			}
			switch tc.ExpectedError {
			case "format-invalid":
				if public.Code != ErrorFormatInvalid {
					t.Errorf("code %d want %d (%v)", public.Code, ErrorFormatInvalid, err)
				}
			default:
				t.Fatalf("unhandled expected error %q", tc.ExpectedError)
			}
		})
	}
}

// mutate applies one conformance mutation to the fixture bytes, exactly
// matching the Rust corpus generator's mutations.
func mutate(t *testing.T, src []byte, mutation string) []byte {
	t.Helper()
	out := make([]byte, len(src))
	copy(out, src)
	switch mutation {
	case "wrong-magic":
		out[0] = 'X'
		out[formatPage] = 'X'
	case "short":
		return out[:formatPage]
	case "unaligned":
		return append(out, 0)
	default:
		t.Fatalf("unknown mutation %q", mutation)
	}
	return out
}

const formatPage = 4096

func repeatEqual(b []byte, value byte, _ int) bool {
	for _, c := range b {
		if c != value {
			return false
		}
	}
	return true
}

func addressBytes4To(s string) uint32 {
	return uint32(parseV4(s))
}

// errorAs is a tiny errors.As wrapper for one target type.
func errorAs(err error, target **Error) bool {
	for err != nil {
		if typed, ok := err.(*Error); ok {
			*target = typed
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestWrongAPIModeProbes pins the Rust-exact pre-checks: every public query
// validates value kind and address family before touching any page
// (reader_core/generation.rs require_direct / require_membership_family,
// membership_view.rs require_kind, structured_value/view.rs require_kind,
// feed_catalog.rs require_membership). Nothing below may return data or a
// format error: wrong kind yields WrongValueKind or WrongStructureKind and
// wrong family yields WrongAddressFamily, even on healthy files.
func TestWrongAPIModeProbes(t *testing.T) {
	type probe struct {
		file      string
		name      string
		run       func(db *ImmutableReader, pin *Pin) error
		wantCode  ErrorCode
		pointless bool
	}
	ip4 := IPv4(0x0a000000) // 10.0.0.0
	ip6 := IPv6{Hi: 0, Lo: 0}
	fe := &Error{}
	possible := func(db *ImmutableReader, want ErrorCode, run func() error) error {
		err := run()
		if err == nil {
			return fmt.Errorf("expected code %d, got nil (probably returned data or absent-false)", want)
		}
		if !errorAs(err, &fe) {
			return fmt.Errorf("expected typed code %d, got %v", want, err)
		}
		if fe.Code != want {
			return fmt.Errorf("expected code %d, got %d (%s)", want, fe.Code, fe.Detail)
		}
		return nil
	}
	probes := []probe{
		// direct-ipv4.iprdb: kind Direct, family IPv4.
		{"direct-ipv4.iprdb", "direct-v4-ok", func(db *ImmutableReader, pin *Pin) error {
			_, _, err := db.LookupDirectV4(ip4)
			return err
		}, 0, true},
		{"direct-ipv4.iprdb", "direct-v6-on-v4", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongAddressFamily, func() error { _, _, e := db.LookupDirectV6(ip6); return e })
		}, ErrorWrongAddressFamily, false},
		{"direct-ipv4.iprdb", "membership-on-direct", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongValueKind, func() error { _, _, e := pin.LookupMembershipV4(ip4); return e })
		}, ErrorWrongValueKind, false},
		{"direct-ipv4.iprdb", "feed-on-direct", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongValueKind, func() error { _, _, e := db.LookupFeed("feed-000"); return e })
		}, ErrorWrongValueKind, false},
		{"direct-ipv4.iprdb", "scan-v6-on-v4", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongAddressFamily, func() error { return db.DirectRangesV6(func(DirectRangeV6) error { return nil }) })
		}, ErrorWrongAddressFamily, false},
		{"direct-ipv4.iprdb", "enrichment-on-direct", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongStructureKind, func() error { _, _, e := pin.LookupNetworkEnrichmentV1V4(ip4); return e })
		}, ErrorWrongStructureKind, false},
		// first-seen-ipv6.iprdb: kind Direct, family IPv6.
		{"first-seen-ipv6.iprdb", "direct-v4-on-v6", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongAddressFamily, func() error { _, _, e := db.LookupDirectV4(ip4); return e })
		}, ErrorWrongAddressFamily, false},
		{"first-seen-ipv6.iprdb", "direct-v6-ok", func(db *ImmutableReader, pin *Pin) error {
			_, _, err := db.LookupDirectV6(ip6)
			return err
		}, 0, true},
		// membership-ipv4.iprdb: kind Membership, family IPv4.
		{"membership-ipv4.iprdb", "direct-on-membership", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongValueKind, func() error { _, _, e := db.LookupDirectV4(ip4); return e })
		}, ErrorWrongValueKind, false},
		{"membership-ipv4.iprdb", "membership-ok", func(db *ImmutableReader, pin *Pin) error {
			_, found, err := pin.LookupMembershipV4(ip4)
			if err != nil || !found {
				return fmt.Errorf("expected bitmap, got %v %v", found, err)
			}
			return nil
		}, 0, true},
		{"membership-ipv4.iprdb", "membership-v6-on-v4", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongAddressFamily, func() error { _, _, e := pin.LookupMembershipV6(ip6); return e })
		}, ErrorWrongAddressFamily, false},
		{"membership-ipv4.iprdb", "feed-ok", func(db *ImmutableReader, pin *Pin) error {
			_, _, err := db.LookupFeed("feed-000")
			return err
		}, 0, true},
		{"membership-ipv4.iprdb", "enrichment-on-membership", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongStructureKind, func() error { _, _, e := pin.LookupNetworkEnrichmentV1V4(ip4); return e })
		}, ErrorWrongStructureKind, false},
		// membership-ipv6.iprdb: kind Membership, family IPv6.
		{"membership-ipv6.iprdb", "membership-v4-on-v6", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongAddressFamily, func() error { _, _, e := pin.LookupMembershipV4(ip4); return e })
		}, ErrorWrongAddressFamily, false},
		// structured-ipv4.iprdb: kind Structured + NetworkEnrichmentV1, family IPv4.
		{"structured-ipv4.iprdb", "membership-on-structured", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongValueKind, func() error { _, _, e := pin.LookupMembershipV4(ip4); return e })
		}, ErrorWrongValueKind, false},
		{"structured-ipv4.iprdb", "enrichment-ok", func(db *ImmutableReader, pin *Pin) error {
			_, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(0x0a010000)) // 10.1.0.0
			if err != nil || !found {
				return fmt.Errorf("expected view, got %v %v", found, err)
			}
			return nil
		}, 0, true},
		{"structured-ipv4.iprdb", "feed-on-structured", func(db *ImmutableReader, pin *Pin) error {
			_, _, err := db.LookupFeed("botnet")
			return err
		}, 0, true},
		{"structured-ipv4.iprdb", "enrichment-v6-on-structured-v4", func(db *ImmutableReader, pin *Pin) error {
			return possible(db, ErrorWrongAddressFamily, func() error { _, _, e := pin.LookupNetworkEnrichmentV1V6(ip6); return e })
		}, ErrorWrongAddressFamily, false},
	}
	for _, p := range probes {
		t.Run(p.file+"/"+p.name, func(t *testing.T) {
			db, err := OpenImmutable(fixturePath("rust/" + p.file))
			if err != nil {
				t.Fatal("open:", err)
			}
			defer db.Close()
			pin, err := db.Pin()
			if err != nil {
				t.Fatal("pin:", err)
			}
			defer pin.Close()
			err = p.run(db, pin)
			if p.pointless {
				if err != nil {
					t.Fatal("expected nil:", err)
				}
				return
			}
			if err != nil {
				t.Fatal("probe failed:", err)
			}
		})
	}
}

// TestHandleLifetime pins the pin contract: a reader with a live pin
// returns ErrorHandleBusy on Close; after the pin is closed the reader
// closes cleanly; a second Close reports WrongState; view operations
// through a closed pin report WrongState.
func TestHandleLifetime(t *testing.T) {
	r := mustOpen(t, "rust/membership-ipv4.iprdb")
	pin, err := r.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	view, found, err := pin.LookupMembershipV4(IPv4(0x0a000000))
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	if err := r.Close(); err == nil {
		t.Fatal("close with live pin must report ErrorHandleBusy")
	} else if fe := errorAsCode(err); fe != ErrorHandleBusy {
		t.Fatalf("code %v want %d", err, ErrorHandleBusy)
	}
	// The view still works while the pin is open.
	if _, err := view.ContainsIndex(0); err != nil {
		t.Fatal("view after busy close:", err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal("pin close:", err)
	}
	// A second pin close reports WrongState.
	if fe := errorAsCode(pin.Close()); fe != ErrorWrongState {
		t.Fatalf("second pin close code %v want %d", fe, ErrorWrongState)
	}
	// View operations through the closed pin report WrongState.
	if _, _, err := view.Word(0); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("view after pin close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal("close after pin:", err)
	}
	// Double close.
	if fe := errorAsCode(r.Close()); fe != ErrorWrongState {
		t.Fatalf("double close code %v want %d", fe, ErrorWrongState)
	}
}

// TestOperationsAfterClose pins the reader-after-close contract: every
// reader-level operation on a closed reader reports WrongState, Pin reports
// WrongState, and pin-level lookups on a pin of a closed reader are
// impossible because Pin refuses first.
func TestOperationsAfterClose(t *testing.T) {
	db := mustOpen(t, "rust/direct-ipv4.iprdb")
	if err := db.Close(); err != nil {
		t.Fatal("close:", err)
	}
	if _, _, err := db.LookupDirectV4(IPv4(0x0a00000a)); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("direct after close: %v", err)
	}
	if _, _, err := db.LookupFeed("feed-000"); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("feed after close: %v", err)
	}
	if err := db.DirectRangesV4(func(DirectRangeV4) error { return nil }); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("scan after close: %v", err)
	}
	if _, _, err := db.MetadataJSON(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("metadata after close: %v", err)
	}
	if _, err := db.Cardinality(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("cardinality after close: %v", err)
	}
	if _, err := db.Info(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("info after close: %v", err)
	}
	if _, err := db.Pin(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("pin after close: %v", err)
	}
}

// errorAsCode extracts the public error code, or 0 for other errors/nil.
func errorAsCode(err error) ErrorCode {
	var fe *Error
	if err != nil && errorAs(err, &fe) {
		return fe.Code
	}
	return 0
}

// TestClosedPinViewRejectsOperations pins WrongState on every view operation
// after its pin is closed. A threat view derived from a structured view
// shares the same pin and stays valid while the pin is open.
func TestClosedPinViewRejectsOperations(t *testing.T) {
	r := mustOpen(t, "rust/structured-ipv4.iprdb")
	pin, err := r.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	view, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(0x0a010000))
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	threat, present, err := view.ThreatMembership()
	if err != nil || !present {
		t.Fatalf("threat: %v %v", present, err)
	}
	if _, err := threat.ContainsIndex(0); err != nil {
		t.Fatalf("threat through live pin: %v", err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal("pin close:", err)
	}
	if _, err := view.Value(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("value after pin close: %v", err)
	}
	if _, err := threat.ContainsIndex(0); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("threat after pin close: %v", err)
	}
}

// TestViewCopiesSharePin pins the copy semantics of views: Go values are
// copyable, and every copy of one view shares the same pin, so copies never
// double-release anything (there is no per-view release). A fresh live view
// still prevents Close through its pin (HandleBusy), and closing the pin
// invalidates every copy with WrongState.
func TestViewCopiesSharePin(t *testing.T) {
	r := mustOpen(t, "rust/membership-ipv4.iprdb")
	pin, err := r.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	v1, found, err := pin.LookupMembershipV4(IPv4(0x0a000000))
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	v2 := v1 // copy shares the same pin
	if _, _, err := v2.Word(0); err != nil {
		t.Fatalf("copy word: %v", err)
	}
	// A second pin still blocks Close; the copied views changed nothing.
	pin2, err := r.Pin()
	if err != nil {
		t.Fatal("second pin:", err)
	}
	if err := r.Close(); errorAsCode(err) != ErrorHandleBusy {
		t.Fatalf("close with live pins: %v", err)
	}
	if err := pin2.Close(); err != nil {
		t.Fatal("pin2 close:", err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal("pin close:", err)
	}
	// Every copy of the view reports WrongState after the pin is closed.
	if _, _, err := v1.Word(0); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("original after pin close: %v", err)
	}
	if _, _, err := v2.Word(0); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("copy after pin close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close after pins: %v", err)
	}
	// The mapping is released now; a stale view copy must still report
	// WrongState through the closed pin instead of touching the released
	// bitmap bytes.
	if _, _, err := v1.Word(0); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("stale view after reader close: %v", err)
	}
}

// TestScanCallbackErrorPassthrough pins that a caller error from a scan
// callback is returned unchanged, never reinterpreted as database
// corruption.
func TestScanCallbackErrorPassthrough(t *testing.T) {
	r := mustOpen(t, "rust/direct-ipv4.iprdb")
	sentinel := fmt.Errorf("caller-stopped: %d", 42)
	err := r.DirectRangesV4(func(DirectRangeV4) error { return sentinel })
	if err != sentinel {
		t.Fatalf("callback error rewritten: %v", err)
	}
}

// TestPinPointerAliasSharesClose pins the Pin pointer contract: aliasing
// the pointer shares one close state (closing through either alias closes
// the single logical pin, a second close reports WrongState, and the pin
// count is decremented exactly once so the reader closes cleanly).
func TestPinPointerAliasSharesClose(t *testing.T) {
	r := mustOpen(t, "rust/membership-ipv4.iprdb")
	p1, err := r.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	p2 := p1 // pointer alias: one logical pin, one close state
	if _, _, err := p2.LookupMembershipV4(IPv4(0x0a000000)); err != nil {
		t.Fatal("lookup through alias:", err)
	}
	if err := p1.Close(); err != nil {
		t.Fatal("close through first alias:", err)
	}
	if fe := errorAsCode(p2.Close()); fe != ErrorWrongState {
		t.Fatalf("close through second alias must report WrongState, got %v", fe)
	}
	// The single decrement leaves the reader closable.
	if err := r.Close(); err != nil {
		t.Fatalf("reader close after alias close: %v", err)
	}
}

// TestPinValueCopySharesClose pins that a VALUE copy (p2 := *p1) shares the
// same private close state as the original and the same single pin count.
// Before the shared-state fix, a value copy carried its own closed flag and
// a second Close double-decremented the reader's pin count, letting the
// reader close while a live pin copy still existed.
func TestPinValueCopySharesClose(t *testing.T) {
	r := mustOpen(t, "rust/membership-ipv4.iprdb")
	p1, err := r.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	p2 := *p1 // value copy: must reference the same pinState
	// Both copies operate through the one live pin.
	if _, _, err := p2.LookupMembershipV4(IPv4(0x0a000000)); err != nil {
		t.Fatal("lookup through value copy:", err)
	}
	// Close through one copy; the other copy must observe the single
	// close state (WrongState, never a second decrement).
	if err := p1.Close(); err != nil {
		t.Fatal("close through first copy:", err)
	}
	if fe := errorAsCode(p2.Close()); fe != ErrorWrongState {
		t.Fatalf("close through second copy must report WrongState, got %v", fe)
	}
	// Exactly one decrement happened: the reader closes cleanly.
	if err := r.Close(); err != nil {
		t.Fatalf("reader close after value-copy close: %v", err)
	}
}

// TestPinValueCopyKeepsReaderBusy pins HandleBusy while two value copies of
// one logical pin are still open, and clean close once both observed the
// single state (one real decrement).
func TestPinValueCopyKeepsReaderBusy(t *testing.T) {
	r := mustOpen(t, "rust/membership-ipv4.iprdb")
	p1, err := r.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	p2 := *p1
	if fe := errorAsCode(r.Close()); fe != ErrorHandleBusy {
		t.Fatalf("reader close with live pin must report HandleBusy, got %v", fe)
	}
	if err := p2.Close(); err != nil {
		t.Fatal("close copy:", err)
	}
	if fe := errorAsCode(p1.Close()); fe != ErrorWrongState {
		t.Fatalf("second close must report WrongState, got %v", fe)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("reader close after both copies observed close: %v", err)
	}
}

// TestPinZeroValueClose pins the zero Pin contract: a Pin that was never
// created by a reader (zero value) reports WrongState instead of panicking,
// matching the inert zero-view contract.
func TestPinZeroValueClose(t *testing.T) {
	var p Pin
	if fe := errorAsCode(p.Close()); fe != ErrorWrongState {
		t.Fatalf("zero Pin close must report WrongState, got %v", fe)
	}
}

// TestPinValueCopyCannotReleaseSecondPin pins the exact audit scenario: two
// independent pins exist, then one value copy of the first pin is made and
// both the first pin and its copy are closed. The reader must remain
// HandleBusy because the second independent pin is still live. Before the
// shared pinState fix, every Close decremented the reader's pin count even
// for value copies, so these two closes drained the count and closed the
// reader under the live second pin.
func TestPinValueCopyCannotReleaseSecondPin(t *testing.T) {
	r := mustOpen(t, "rust/membership-ipv4.iprdb")
	p1, err := r.Pin()
	if err != nil {
		t.Fatal("pin 1:", err)
	}
	p2, err := r.Pin()
	if err != nil {
		t.Fatal("pin 2:", err)
	}
	p1c := *p1 // value copy of the first logical pin
	if err := p1.Close(); err != nil {
		t.Fatal("close pin 1:", err)
	}
	if fe := errorAsCode(p1c.Close()); fe != ErrorWrongState {
		t.Fatalf("close of pin-1 copy must report WrongState, got %v", fe)
	}
	if fe := errorAsCode(r.Close()); fe != ErrorHandleBusy {
		t.Fatalf("reader close with live pin 2 must report HandleBusy, got %v", fe)
	}
	if err := p2.Close(); err != nil {
		t.Fatal("close pin 2:", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("reader close after all pins released: %v", err)
	}
}

// TestLookupFeedIntoBufferTooSmall pins the zero-allocation feed lookup
// contract: a short buffer returns BufferTooSmall with the required size in
// NameLen, an exact-size buffer copies the full name, and a zero-length
// buffer returns BufferTooSmall with the size.
func TestLookupFeedIntoBufferTooSmall(t *testing.T) {
	r, err := OpenImmutable(fixturePath("rust/membership-ipv4.iprdb"))
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	defer r.Close()
	p, err := r.Pin()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Find a declared feed name from the fixture manifest.
	m := loadManifest(t)
	var feedName string
	var feedIndex uint32
	for _, fx := range m.Fixtures {
		if fx.File == "rust/membership-ipv4.iprdb" {
			if len(fx.Feeds) == 0 {
				t.Skip("no feeds in fixture")
			}
			feedName = fx.Feeds[0].Name
			feedIndex = fx.Feeds[0].Index
			break
		}
	}

	entry, ok, err := r.LookupFeed(feedName)
	if err != nil || !ok {
		t.Fatalf("lookup feed %s: %v %v", feedName, ok, err)
	}

	// Short buffer: BufferTooSmall with the required size.
	short := make([]byte, len(entry.Name)-1)
	fi, ok, err := p.LookupFeedInto(feedName, short)
	if err == nil || errorAsCode(err) != ErrorBufferTooSmall {
		t.Fatalf("short buffer: err=%v, want BufferTooSmall", err)
	}
	if ok {
		t.Fatal("short buffer: ok=true, want false")
	}
	if fi.NameLen != len(entry.Name) {
		t.Fatalf("short buffer: NameLen=%d, want %d", fi.NameLen, len(entry.Name))
	}

	// Exact buffer: full name copied, ok=true.
	exact := make([]byte, len(entry.Name))
	fi, ok, err = p.LookupFeedInto(feedName, exact)
	if err != nil || !ok {
		t.Fatalf("exact buffer: err=%v ok=%v, want nil true", err, ok)
	}
	if string(exact) != feedName {
		t.Fatalf("exact buffer: name %q want %q", exact, feedName)
	}
	if fi.NameLen != len(feedName) {
		t.Fatalf("exact buffer: NameLen=%d, want %d", fi.NameLen, len(feedName))
	}
	if fi.Index != feedIndex {
		t.Fatalf("exact buffer: Index=%d, want %d", fi.Index, feedIndex)
	}

	// Zero-length buffer: BufferTooSmall with the required size.
	fi, ok, err = p.LookupFeedInto(feedName, []byte{})
	if err == nil || errorAsCode(err) != ErrorBufferTooSmall {
		t.Fatalf("zero buffer: err=%v, want BufferTooSmall", err)
	}
	if ok {
		t.Fatal("zero buffer: ok=true, want false")
	}
	if fi.NameLen != len(feedName) {
		t.Fatalf("zero buffer: NameLen=%d, want %d", fi.NameLen, len(feedName))
	}
}
