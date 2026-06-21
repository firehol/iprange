package iprangeformat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type legacyManifest struct {
	Name      string `json:"name"`
	IPVersion string `json:"ip_version"`
	File      string `json:"file"`
	Optimized bool   `json:"optimized"`
	UniqueIPs string `json:"unique_ips"`
	Lines     uint64 `json:"lines"`
	Ranges    []struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"ranges"`
}

func TestLegacyCorpus(t *testing.T) {
	dir := filepath.Join("..", "conformance", "legacy")
	manifests, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) == 0 {
		t.Fatalf("no legacy manifests in %s", dir)
	}
	sort.Strings(manifests)

	for _, mpath := range manifests {
		raw, err := os.ReadFile(mpath)
		if err != nil {
			t.Fatal(err)
		}
		var m legacyManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", mpath, err)
		}
		b, err := os.ReadFile(filepath.Join(dir, m.File))
		if err != nil {
			t.Fatal(err)
		}
		lg, err := ParseLegacy(b)
		if err != nil {
			t.Errorf("%s: parse failed: %v", m.Name, err)
			continue
		}
		if lg.Optimized != m.Optimized || lg.Lines != m.Lines || lg.UniqueIPs != m.UniqueIPs {
			t.Errorf("%s: meta mismatch: %+v vs manifest", m.Name, lg)
		}

		switch m.IPVersion {
		case "v4":
			if lg.IsV6 || len(lg.RangesV4) != len(m.Ranges) {
				t.Errorf("%s: v4 range count/family mismatch", m.Name)
				continue
			}
			for i, want := range m.Ranges {
				s, _ := v4Key(want.Start)
				e, _ := v4Key(want.End)
				if lg.RangesV4[i][0] != s || lg.RangesV4[i][1] != e {
					t.Errorf("%s: range %d mismatch", m.Name, i)
				}
			}
			// migrate to v3 and verify.
			w := NewWriterV4(FeedMeta{Name: m.Name, Category: "migrated"}, 0, 0)
			for _, rg := range lg.RangesV4 {
				if err := w.AddRange(rg[0], rg[1], nil); err != nil {
					t.Fatal(err)
				}
			}
			v3, err := w.Build()
			if err != nil {
				t.Fatal(err)
			}
			r, err := Open(v3)
			if err != nil {
				t.Fatal(err)
			}
			if r.RecordCount() != uint64(len(lg.RangesV4)) {
				t.Errorf("%s: migrated record count", m.Name)
			}
			for _, rg := range lg.RangesV4 {
				if _, found, _ := r.LookupV4(rg[0]); !found {
					t.Errorf("%s: migrated lookup miss", m.Name)
				}
			}
		case "v6":
			if !lg.IsV6 || len(lg.RangesV6) != len(m.Ranges) {
				t.Errorf("%s: v6 range count/family mismatch", m.Name)
				continue
			}
			for i, want := range m.Ranges {
				s, _ := v6Key(want.Start)
				e, _ := v6Key(want.End)
				if lg.RangesV6[i][0] != s || lg.RangesV6[i][1] != e {
					t.Errorf("%s: range %d mismatch (got %+v)", m.Name, i, lg.RangesV6[i])
				}
			}
			w := NewWriterV6(FeedMeta{Name: m.Name, Category: "migrated"}, 0, 0)
			for _, rg := range lg.RangesV6 {
				if err := w.AddRange(rg[0], rg[1], nil); err != nil {
					t.Fatal(err)
				}
			}
			v3, err := w.Build()
			if err != nil {
				t.Fatal(err)
			}
			r, err := Open(v3)
			if err != nil {
				t.Fatal(err)
			}
			if r.RecordCount() != uint64(len(lg.RangesV6)) {
				t.Errorf("%s: migrated record count", m.Name)
			}
			for _, rg := range lg.RangesV6 {
				if _, found, _ := r.LookupV6(rg[0]); !found {
					t.Errorf("%s: migrated lookup miss", m.Name)
				}
			}
		default:
			t.Errorf("%s: unknown ip_version", m.Name)
		}
	}
}
