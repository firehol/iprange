package iprangeformat

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// These structs mirror the language-neutral case schema in conformance/README.md.
type tcase struct {
	Name           string   `json:"name"`
	IPVersion      string   `json:"ip_version"`
	FeedMeta       tmeta    `json:"feed_meta"`
	LicenseFlags   uint32   `json:"license_flags"`
	GenerationUnix uint64   `json:"generation_unixtime"`
	Ranges         []trange `json:"ranges"`
	Expect         string   `json:"expect"`
	RejectClass    string   `json:"reject_class"`
}

type tmeta struct {
	Name          string `json:"name"`
	Category      string `json:"category"`
	Maintainer    string `json:"maintainer"`
	MaintainerURL string `json:"maintainer_url"`
	SourceURL     string `json:"source_url"`
	License       string `json:"license"`
}

type trange struct {
	Start string  `json:"start"`
	End   string  `json:"end"`
	Value *tvalue `json:"value"`
}

type tvalue struct {
	TypeID   uint32 `json:"type_id"`
	BytesHex string `json:"bytes_hex"`
}

func corpusDir() string { return filepath.Join("..", "conformance") }

func feedMeta(m tmeta) FeedMeta {
	return FeedMeta{
		Name: m.Name, Category: m.Category, Maintainer: m.Maintainer,
		MaintainerURL: m.MaintainerURL, SourceURL: m.SourceURL, License: m.License,
	}
}

func valueOf(r trange) (*Value, error) {
	if r.Value == nil {
		return nil, nil
	}
	b, err := hex.DecodeString(r.Value.BytesHex)
	if err != nil {
		return nil, err
	}
	return &Value{TypeID: r.Value.TypeID, Bytes: b}, nil
}

func v4Key(s string) (Ipv4Key, error) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return 0, err
	}
	b := a.As4()
	return Ipv4Key(binary.BigEndian.Uint32(b[:])), nil
}

func v6Key(s string) (Ipv6Key, error) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return Ipv6Key{}, err
	}
	b := a.As16()
	return Ipv6Key{Hi: binary.BigEndian.Uint64(b[0:8]), Lo: binary.BigEndian.Uint64(b[8:16])}, nil
}

func buildCase(c *tcase) ([]byte, error) {
	switch c.IPVersion {
	case "v4":
		w := NewWriterV4(feedMeta(c.FeedMeta), c.LicenseFlags, c.GenerationUnix)
		for _, r := range c.Ranges {
			s, err := v4Key(r.Start)
			if err != nil {
				return nil, err
			}
			e, err := v4Key(r.End)
			if err != nil {
				return nil, err
			}
			val, err := valueOf(r)
			if err != nil {
				return nil, err
			}
			if err := w.AddRange(s, e, val); err != nil {
				return nil, err
			}
		}
		return w.Build()
	case "v6":
		w := NewWriterV6(feedMeta(c.FeedMeta), c.LicenseFlags, c.GenerationUnix)
		for _, r := range c.Ranges {
			s, err := v6Key(r.Start)
			if err != nil {
				return nil, err
			}
			e, err := v6Key(r.End)
			if err != nil {
				return nil, err
			}
			val, err := valueOf(r)
			if err != nil {
				return nil, err
			}
			if err := w.AddRange(s, e, val); err != nil {
				return nil, err
			}
		}
		return w.Build()
	default:
		return nil, errStructural("unknown ip_version " + c.IPVersion)
	}
}

func errorClass(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Class
	}
	return "unknown"
}

func TestConformanceCorpus(t *testing.T) {
	casesDir := filepath.Join(corpusDir(), "cases")
	goldenDir := filepath.Join(corpusDir(), "golden")

	matches, err := filepath.Glob(filepath.Join(casesDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("no conformance cases found in %s", casesDir)
	}
	sort.Strings(matches)

	checked := 0
	for _, path := range matches {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var c tcase
		if err := json.Unmarshal(text, &c); err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		out, buildErr := buildCase(&c)
		switch c.Expect {
		case "reject":
			if buildErr == nil {
				t.Errorf("%s: expected rejection, got success", c.Name)
				continue
			}
			if c.RejectClass != "" && errorClass(buildErr) != c.RejectClass {
				t.Errorf("%s: wrong error class: got %q want %q (%v)", c.Name, errorClass(buildErr), c.RejectClass, buildErr)
			}
		case "bytes":
			if buildErr != nil {
				t.Errorf("%s: build failed: %v", c.Name, buildErr)
				continue
			}
			golden := filepath.Join(goldenDir, c.Name+".iprbin")
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Errorf("%s: missing golden %s: %v", c.Name, golden, err)
				continue
			}
			if !bytes.Equal(out, want) {
				t.Errorf("%s: Go output differs from the (Rust) golden: got %d bytes, want %d bytes", c.Name, len(out), len(want))
				continue
			}
			// A golden must also read back cleanly with the Go reader.
			if _, err := Open(out); err != nil {
				t.Errorf("%s: Go reader rejected its own output: %v", c.Name, err)
			}
		default:
			t.Errorf("%s: unknown expect %q", c.Name, c.Expect)
		}
		checked++
	}
	t.Logf("conformance: %d cases checked against the shared corpus", checked)
}
