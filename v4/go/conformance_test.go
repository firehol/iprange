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
					if fromHi != toHi || fromLo+1 != toLo {
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

			// Feed catalog checks only apply to membership-capable files.
			if len(tc.Feeds) > 0 {
				// Every declared feed resolves to its exact index, the declared
				// count matches the meta, and undeclared names are absent.
				if info.ActiveFeedCount != uint64(len(tc.Feeds)) {
					t.Errorf("active feed count %d want %d", info.ActiveFeedCount, len(tc.Feeds))
				}
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
				// Midpoint probe as well.
				mid4 := from4
				if tc.Family == "ipv4" {
					mid4 = from4 + (addressBytes4To(mr.To)-from4)/2
					if mid4 != from4 {
						v2, ok2, err2 := pin.LookupMembershipV4(IPv4(mid4))
						if err2 != nil || !ok2 {
							t.Errorf("membership mid %s: %v %v", mr.From, ok2, err2)
							continue
						}
						view = v2
					}
				} else {
					midLo := fl + 1
					midHi := fh
					if midLo == 0 {
						midHi++
					}
					if fh != th || fl+1 != tl {
						v2, ok2, err2 := pin.LookupMembershipV6(IPv6{Hi: midHi, Lo: midLo})
						if err2 != nil || !ok2 {
							t.Errorf("membership v6 mid %s: %v %v", mr.From, ok2, err2)
							continue
						}
						view = v2
					}
				}
				// Every listed feed must be present.
				for _, feed := range mr.Feeds {
					has, err := view.ContainsIndex(feedIndexOf(feed))
					if err != nil {
						t.Fatal("contains:", err)
					}
					if !has {
						t.Errorf("membership %s lacks feed %s", mr.From, feed)
					}
				}
				// A declared feed that is not listed for this range must be
				// absent from this range's bitmap.
				listed := map[uint32]bool{}
				for _, feed := range mr.Feeds {
					listed[feedIndexOf(feed)] = true
				}
				for _, f := range tc.Feeds {
					if !listed[f.Index] {
						has, err := view.ContainsIndex(f.Index)
						if err != nil {
							t.Fatal("contains:", err)
						}
						if has {
							t.Errorf("membership %s contains undeclared feed %s (%d)", mr.From, f.Name, f.Index)
						}
					}
				}
				// Word-exact bitmap verification: the stored word count must
				// be exactly the words needed for the highest listed feed,
				// and every word must equal the canonical expectation.
				maxBit := uint32(0)
				for _, feed := range mr.Feeds {
					if idx := feedIndexOf(feed); idx > maxBit {
						maxBit = idx
					}
				}
				wantWords := int(maxBit/64 + 1)
				gotWords, err := view.WordCount()
				if err != nil {
					t.Fatal("word count:", err)
				}
				if int(gotWords) != wantWords {
					t.Errorf("membership %s word count %d want %d", mr.From, gotWords, wantWords)
				}
				words := make([]uint64, wantWords)
				n, err := view.ReadWords(0, words)
				if err != nil || n != wantWords {
					t.Errorf("membership %s read words: n=%d err=%v", mr.From, n, err)
				}
				expected := make([]uint64, wantWords)
				for _, feed := range mr.Feeds {
					idx := feedIndexOf(feed)
					expected[idx/64] |= uint64(1) << (idx % 64)
				}
				for i := range expected {
					if words[i] != expected[i] {
						t.Errorf("membership %s word %d = %x want %x", mr.From, i, words[i], expected[i])
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
				probeMembership := func(hi, lo uint64) bool {
					if tc.Family == "ipv4" {
						_, ok, err := pin.LookupMembershipV4(IPv4(uint32(lo)))
						return err != nil || ok
					}
					_, ok, err := pin.LookupMembershipV6(IPv6{Hi: hi, Lo: lo})
					return err != nil || ok
				}
				first, last := tc.MembershipRanges[0], tc.MembershipRanges[len(tc.MembershipRanges)-1]
				fh, fl, _ := addressBytes(first.From, tc.Family)
				th, tl, _ := addressBytes(last.To, tc.Family)
				if tc.Family == "ipv4" {
					_, _, f4 := addressBytes(first.From, "ipv4")
					if f4 > 0 && probeMembership(0, uint64(f4-1)) {
						t.Errorf("membership from-1 (0x%x): want absent", f4-1)
					}
					if f4 > 0 && probeMembership(0, 0) {
						t.Errorf("membership 0.0.0.0: want absent")
					}
					if th < 0xffffffff && probeMembership(0, 0xffffffff) {
						t.Errorf("membership 255.255.255.255: want absent")
					}
					for i := 1; i < len(tc.MembershipRanges); i++ {
						_, _, prevTo := addressBytes(tc.MembershipRanges[i-1].To, "ipv4")
						_, _, curFrom := addressBytes(tc.MembershipRanges[i].From, "ipv4")
						if curFrom > prevTo+1 && probeMembership(0, uint64(prevTo+1)) {
							t.Errorf("membership gap after %s: want absent", tc.MembershipRanges[i-1].To)
						}
					}
					// Probe the exact end of each range (to) and just past
					// it (to+1), mirroring Rust membership_probes. to+1 is
					// absent only when the next range does not start there.
					for i, mr := range tc.MembershipRanges {
						_, _, to4 := addressBytes(mr.To, "ipv4")
						if !probeMembership(0, uint64(to4)) {
							t.Errorf("membership to %s (0x%x): want present", mr.To, to4)
						}
						if to4 < 0xffffffff {
							adjacent := i+1 < len(tc.MembershipRanges)
							if adjacent {
								_, _, nextFrom := addressBytes(tc.MembershipRanges[i+1].From, "ipv4")
								adjacent = nextFrom == to4+1
							}
							if !adjacent && probeMembership(0, uint64(to4+1)) {
								t.Errorf("membership to+1 %s (0x%x): want absent", mr.To, to4+1)
							}
						}
					}
				} else {
					if (fh != 0 || fl != 0) && probeMembership(0, 0) {
						t.Errorf("membership :: : want absent")
					}
					if (th != ^uint64(0) || tl != ^uint64(0)) && probeMembership(^uint64(0), ^uint64(0)) {
						t.Errorf("membership ffff:…: want absent")
					}
				}
			}

			// Structured ranges.
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
					if val.Location.LatitudeMicrodegrees != sr.Location.Lat ||
						val.Location.LongitudeMicrodegrees != sr.Location.Long {
						t.Errorf("structured %s: location %d,%d want %d,%d", sr.From,
							val.Location.LatitudeMicrodegrees, val.Location.LongitudeMicrodegrees,
							sr.Location.Lat, sr.Location.Long)
					}
				}
				threat, present, err := view.ThreatMembership()
				if err != nil {
					t.Fatal("threat membership:", err)
				}
				if len(sr.Feeds) == 0 {
					// No-threat structured value: canonical absence
					// (membership id zero) reports present=false with nil
					// error, mirroring the Rust Option result.
					if present {
						t.Errorf("structured %s: unexpected threat membership", sr.From)
					}
				} else {
					if !present {
						t.Errorf("structured %s: missing threat membership", sr.From)
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

			// Absence at the family edges and inside inter-range gaps of
			// the structured ranges: found=false with nil error, never a
			// stale view and never corruption (Rust verifier probes the
			// same positions).
			if len(tc.StructuredRanges) > 0 {
				probeStructured := func(hi, lo uint64) bool {
					if tc.Family == "ipv4" {
						_, ok, err := pin.LookupNetworkEnrichmentV1V4(IPv4(uint32(lo)))
						return err != nil || ok
					}
					_, ok, err := pin.LookupNetworkEnrichmentV1V6(IPv6{Hi: hi, Lo: lo})
					return err != nil || ok
				}
				first, last := tc.StructuredRanges[0], tc.StructuredRanges[len(tc.StructuredRanges)-1]
				fh, fl, _ := addressBytes(first.From, tc.Family)
				th, tl, _ := addressBytes(last.To, tc.Family)
				if tc.Family == "ipv4" {
					_, _, f4 := addressBytes(first.From, "ipv4")
					if f4 > 0 && probeStructured(0, uint64(f4-1)) {
						t.Errorf("structured from-1 (0x%x): want absent", f4-1)
					}
					if f4 > 0 && probeStructured(0, 0) {
						t.Errorf("structured 0.0.0.0: want absent")
					}
					if th < 0xffffffff && probeStructured(0, 0xffffffff) {
						t.Errorf("structured 255.255.255.255: want absent")
					}
					for i := 1; i < len(tc.StructuredRanges); i++ {
						_, _, prevTo := addressBytes(tc.StructuredRanges[i-1].To, "ipv4")
						_, _, curFrom := addressBytes(tc.StructuredRanges[i].From, "ipv4")
						if curFrom > prevTo+1 && probeStructured(0, uint64(prevTo+1)) {
							t.Errorf("structured gap after %s: want absent", tc.StructuredRanges[i-1].To)
						}
					}
					// Probe the exact end of each range (to) and just past
					// it (to+1), mirroring Rust membership_probes. to+1 is
					// absent only when the next range does not start there.
					for i, sr := range tc.StructuredRanges {
						_, _, to4 := addressBytes(sr.To, "ipv4")
						if !probeStructured(0, uint64(to4)) {
							t.Errorf("structured to %s (0x%x): want present", sr.To, to4)
						}
						if to4 < 0xffffffff {
							adjacent := i+1 < len(tc.StructuredRanges)
							if adjacent {
								_, _, nextFrom := addressBytes(tc.StructuredRanges[i+1].From, "ipv4")
								adjacent = nextFrom == to4+1
							}
							if !adjacent && probeStructured(0, uint64(to4+1)) {
								t.Errorf("structured to+1 %s (0x%x): want absent", sr.To, to4+1)
							}
						}
					}
				} else {
					if (fh != 0 || fl != 0) && probeStructured(0, 0) {
						t.Errorf("structured :: : want absent")
					}
					if (th != ^uint64(0) || tl != ^uint64(0)) && probeStructured(^uint64(0), ^uint64(0)) {
						t.Errorf("structured ffff:…: want absent")
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
