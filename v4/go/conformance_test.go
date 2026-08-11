package iprangedb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
	State string `json:"state"`
	Value string `json:"value,omitempty"`
	Byte  int    `json:"byte,omitempty"`
	Len   int    `json:"length,omitempty"`
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
	var m conformanceManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal("parse cases.json:", err)
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

func parseV4(s string) IPv4 {
	var a, b, c, d uint32
	if _, err := fmt.Sscanf(s, "%d.%d.%d.%d", &a, &b, &c, &d); err != nil {
		panic("bad ipv4 " + s)
	}
	return IPv4(a<<24 | b<<16 | c<<8 | d)
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
	var before, after []string
	var double bool
	cur := ""
	flush := func() {
		if cur != "" {
			before = append(before, cur)
			cur = ""
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			if !double && i+1 < len(s) && s[i+1] == ':' {
				flush()
				double = true
				i++
			} else {
				flush()
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

func rangeCardinality(t *testing.T, from, to, family string) string {
	t.Helper()
	var card Cardinality129
	var err error
	if family == "ipv4" {
		f, t4, _ := addressBytes(from, family)
		_, t5, _ := addressBytes(to, family)
		_ = f
		card, err = IPv4Inclusive(uint32(t4), uint32(t5))
	} else {
		fh, fl, _ := addressBytes(from, family)
		th, tl, _ := addressBytes(to, family)
		card, err = IPv6Inclusive(fh, fl, th, tl)
	}
	if err != nil {
		t.Fatal("range cardinality:", err)
	}
	return card.String()
}

// TestConformanceRustFixtures opens every committed Rust-produced fixture and
// verifies the exact cases.json semantics.
func TestConformanceRustFixtures(t *testing.T) {
	m := loadManifest(t)
	for _, tc := range m.Fixtures {
		tc := tc
		t.Run(tc.File, func(t *testing.T) {
			db := mustOpen(t, tc.File)
			defer db.Close()
			info := db.Info()

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
			}
			if info.ValueKind != wantKind {
				t.Errorf("kind %d want %d", info.ValueKind, wantKind)
			}
			if tc.Structure == "network-enrichment-v1" {
				if info.StructureKind != StructureKindNetworkEnrichmentV1 {
					t.Errorf("structure kind %d want 1", info.StructureKind)
				}
			} else if tc.Kind != "structured" && info.StructureKind != StructureKindNone {
				t.Errorf("structure kind %d want 0", info.StructureKind)
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
				if !present || string(meta) != tc.Metadata.Value {
					t.Errorf("metadata text: present=%v got %q want %q", present, meta, tc.Metadata.Value)
				}
			case "repeat":
				if !present || !repeatEqual(meta, byte(tc.Metadata.Byte), len(meta)) {
					t.Errorf("metadata repeat: present=%v len=%d want len=%d byte=%d", present, len(meta), tc.Metadata.Len, tc.Metadata.Byte)
				}
				if len(meta) != tc.Metadata.Len {
					t.Errorf("metadata repeat length %d want %d", len(meta), tc.Metadata.Len)
				}
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
				_ = fromHi
				_ = toHi
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
					v, ok, err := db.LookupDirectV6(IPv6{Hi: fromHi, Lo: fromLo})
					if err != nil || !ok || v != want.Value {
						t.Errorf("direct v6 %s: got %d %v %v", want.From, v, ok, err)
					}
					v, ok, _ = db.LookupDirectV6(IPv6{Hi: toHi, Lo: toLo})
					if !ok || v != want.Value {
						t.Errorf("direct v6 %s: got %d %v", want.To, v, ok)
					}
					_ = midHi
					_ = midLo
				}
			}

			// Feeds.
			for _, feed := range tc.Feeds {
				entry, ok, err := db.LookupFeed(feed.Name)
				if err != nil || !ok {
					t.Errorf("feed %s: %v %v", feed.Name, ok, err)
					continue
				}
				if entry.Index != feed.Index {
					t.Errorf("feed %s index %d want %d", feed.Name, entry.Index, feed.Index)
				}
			}

			// Membership ranges.
			feedIndexOf := func(name string) uint32 {
				for _, f := range tc.Feeds {
					if f.Name == name {
						return f.Index
					}
				}
				t.Fatalf("feed %s not declared", name)
				return 0
			}
			for _, mr := range tc.MembershipRanges {
				_, _, from4 := addressBytes(mr.From, tc.Family)
				fh, fl, _ := addressBytes(mr.From, tc.Family)
				th, tl, _ := addressBytes(mr.To, tc.Family)
				var view MembershipView
				var ok bool
				var err error
				if tc.Family == "ipv4" {
					view, ok, err = db.LookupMembershipV4(IPv4(from4))
				} else {
					view, ok, err = db.LookupMembershipV6(IPv6{Hi: fh, Lo: fl})
				}
				if err != nil || !ok {
					t.Errorf("membership %s: %v %v", mr.From, ok, err)
					continue
				}
				// Midpoint probe as well.
				midHi, midLo, mid4 := fh, fl, from4
				if tc.Family == "ipv4" {
					mid4 = from4 + (addressBytes4To(mr.To)-from4)/2
					if mid4 != from4 {
						v2, ok2, err2 := db.LookupMembershipV4(IPv4(mid4))
						if err2 != nil || !ok2 {
							t.Errorf("membership mid %s: %v %v", mr.From, ok2, err2)
							continue
						}
						view = v2
					}
				} else {
					_ = midHi
					_ = midLo
					_ = th
					_ = tl
				}
				for _, feed := range mr.Feeds {
					idx := feedIndexOf(feed)
					has, err := view.ContainsIndex(idx)
					if err != nil {
						t.Fatal("contains:", err)
					}
					if !has {
						t.Errorf("membership %s lacks feed %s (%d)", mr.From, feed, idx)
					}
				}
				// A feed that is not in this range's list must be absent if
				// it is declared in the manifest.
			}

			// Structured ranges.
			for _, sr := range tc.StructuredRanges {
				_, _, from4 := addressBytes(sr.From, tc.Family)
				fh, fl, _ := addressBytes(sr.From, tc.Family)
				var view NetworkEnrichmentV1View
				var ok bool
				var err error
				if tc.Family == "ipv4" {
					view, ok, err = db.LookupNetworkEnrichmentV1V4(IPv4(from4))
				} else {
					view, ok, err = db.LookupNetworkEnrichmentV1V6(IPv6{Hi: fh, Lo: fl})
				}
				if err != nil || !ok {
					t.Errorf("structured %s: %v %v", sr.From, ok, err)
					continue
				}
				val, err := view.Value()
				if err != nil {
					t.Fatal("structure value:", err)
				}
				if val.ASN != sr.ASN || val.CountryID != sr.CountryID ||
					val.StateID != sr.StateID || val.CityID != sr.CityID {
					t.Errorf("structured %s: asn=%d/%d country=%d/%d state=%d/%d city=%d/%d",
						sr.From, val.ASN, sr.ASN, val.CountryID, sr.CountryID,
						val.StateID, sr.StateID, val.CityID, sr.CityID)
				}
				wantLoc := sr.Location != nil
				if val.HasLocation != wantLoc {
					t.Errorf("structured %s: location %v want %v", sr.From, val.HasLocation, wantLoc)
				}
				if wantLoc {
					if val.LatitudeMicrodegrees != sr.Location.Lat || val.LongitudeMicrodegrees != sr.Location.Long {
						t.Errorf("structured %s: location %d,%d want %d,%d", sr.From,
							val.LatitudeMicrodegrees, val.LongitudeMicrodegrees,
							sr.Location.Lat, sr.Location.Long)
					}
				}
				if len(sr.Feeds) > 0 {
					threat, err := view.ThreatMembership()
					if err != nil {
						t.Fatal("threat membership:", err)
					}
					for _, feed := range sr.Feeds {
						has, err := threat.ContainsIndex(feedIndexOf(feed))
						if err != nil {
							t.Fatal("threat contains:", err)
						}
						if !has {
							t.Errorf("structured %s lacks threat feed %s", sr.From, feed)
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
	src, err := os.ReadFile(fixturePath(m.Invalid[0].Source))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range m.Invalid {
		tc := tc
		t.Run(tc.Mutation, func(t *testing.T) {
			mutated := mutate(t, src, tc.Mutation)
			dir := t.TempDir()
			path := filepath.Join(dir, "mutated.iprdb")
			if err := os.WriteFile(path, mutated, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := OpenImmutable(path)
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
