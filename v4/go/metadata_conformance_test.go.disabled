package iprangedb

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Bidirectional metadata cross-read conformance (§C, §12): a metadata-bearing file written
// by EITHER language is read identically by BOTH. Unlike the IP-tree conformance (Rust
// writes the only golden, both read), each writer's KV / scope-table encoding is
// independent, so the goldens are two-sided:
//
//   - Rust writes  ../conformance/files/<name>.r.iprdb
//   - Go writes    ../conformance/files/<name>.go.iprdb
//   - each reader opens BOTH and verifies the same expectations.
//
// All reads go through the Reader API (the consumer path being certified), never the
// Writer. Cases come from ../conformance/metadata_cases.json, plus one programmatic
// 25-scope case (meta_many) that overflows the 14-record scope-leaf capacity and so forces
// a multi-level scope tree — proving branchy-scope-table cross-read.

// --- corpus schema (mirrors metadata_cases.json) ---

type mcase struct {
	Name         string      `json:"name"`
	Family       string      `json:"family"`
	ScopeWidth   uint8       `json:"scope_width"`
	IPOps        []mIPOp     `json:"ip_ops"`
	Scopes       []mScope    `json:"scopes"`
	FileKV       []mKV       `json:"file_kv"`
	ExpectScopes []mExpScope `json:"expect_scopes"`
	ExpectFileKV []mKV       `json:"expect_file_kv"`
}

type mIPOp struct {
	Op    string `json:"op"`
	From  string `json:"from"`
	To    string `json:"to"`
	Scope []byte `json:"scope"`
}

type mScope struct {
	Name       string  `json:"name"`
	SetVersion *uint64 `json:"set_version"`
	SetType    *uint8  `json:"set_type"`
	KV         []mKV   `json:"kv"`
}

type mExpScope struct {
	ID      uint32 `json:"id"`
	Name    string `json:"name"`
	Version uint64 `json:"version"`
	Type    uint8  `json:"type"`
	KV      []mKV  `json:"kv"`
}

// mKV is one KV entry. The value is exactly one of value_hex or value_fill.
type mKV struct {
	Key       string  `json:"key"`
	Type      uint32  `json:"type"`
	ValueHex  *string `json:"value_hex"`
	ValueFill *mFill  `json:"value_fill"`
}

type mFill struct {
	Byte uint8  `json:"byte"`
	Len  uint32 `json:"len"`
}

// value materializes the value bytes from value_hex XOR value_fill.
func (k mKV) value(t *testing.T) []byte {
	t.Helper()
	switch {
	case k.ValueHex != nil && k.ValueFill == nil:
		b, err := hex.DecodeString(*k.ValueHex)
		if err != nil {
			t.Fatalf("kv %q: bad value_hex: %v", k.Key, err)
		}
		return b
	case k.ValueHex == nil && k.ValueFill != nil:
		b := make([]byte, k.ValueFill.Len)
		for i := range b {
			b[i] = k.ValueFill.Byte
		}
		return b
	default:
		t.Fatalf("kv %q: exactly one of value_hex/value_fill required", k.Key)
		return nil
	}
}

// textKV builds a type-0 (text) KV with a hex-encoded value — for the programmatic case.
func textKV(key string, value []byte) mKV {
	h := hex.EncodeToString(value)
	return mKV{Key: key, Type: 0, ValueHex: &h}
}

// --- corpus + golden paths ---

func goldenDir() string { return filepath.Join("..", "conformance", "files") }

// rustGolden is the other language's golden (Rust writes .r.iprdb).
func rustGolden(name string) string { return filepath.Join(goldenDir(), name+".r.iprdb") }

// goGolden is this language's golden (Go writes .go.iprdb).
func goGolden(name string) string { return filepath.Join(goldenDir(), name+".go.iprdb") }

// maybeWriteGolden writes the committed image as this language's golden when
// REGENERATE_GOLDENS is set (mirrors the IP-tree harness). Read-back is done by the caller.
func maybeWriteGolden(t *testing.T, name string, img []byte) {
	t.Helper()
	if os.Getenv("REGENERATE_GOLDENS") == "" {
		return
	}
	if err := os.MkdirAll(goldenDir(), 0o755); err != nil {
		t.Fatalf("mkdir goldens: %v", err)
	}
	if err := os.WriteFile(goGolden(name), img, 0o644); err != nil {
		t.Fatalf("write golden %s: %v", name, err)
	}
}

func loadMetaCases(t *testing.T) []mcase {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "conformance", "metadata_cases.json"))
	if err != nil {
		t.Fatalf("read metadata_cases.json: %v", err)
	}
	var cases []mcase
	if err := json.Unmarshal(text, &cases); err != nil {
		t.Fatalf("parse metadata_cases.json: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("metadata corpus is empty")
	}
	return cases
}

// --- expectations the Reader API must satisfy on any committed image ---

// verifyReader verifies a committed image through the Reader against the case's
// expectations. where labels which file (built / Rust golden) for a clear failure message.
func verifyReader(t *testing.T, img []byte, c *mcase, where string) {
	t.Helper()
	r, err := Open(img)
	if err != nil {
		t.Fatalf("%s %s: Open: %v", c.Name, where, err)
	}

	// ScopeList excludes FILE(0); compare (id, name) in ascending id order.
	got := r.ScopeList()
	if len(got) != len(c.ExpectScopes) {
		t.Fatalf("%s %s: ScopeList len %d, want %d", c.Name, where, len(got), len(c.ExpectScopes))
	}
	for i, es := range c.ExpectScopes {
		if got[i].ID != es.ID || !bytes.Equal(got[i].Name, []byte(es.Name)) {
			t.Fatalf("%s %s: ScopeList[%d] = (%d,%q), want (%d,%q)",
				c.Name, where, i, got[i].ID, got[i].Name, es.ID, es.Name)
		}
	}

	// Per-scope header fields + KV.
	for _, es := range c.ExpectScopes {
		name, ok := r.ScopeName(es.ID)
		if !ok || !bytes.Equal(name, []byte(es.Name)) {
			t.Fatalf("%s %s: ScopeName(%d) = (%q,%v), want %q", c.Name, where, es.ID, name, ok, es.Name)
		}
		ver, ok := r.ScopeVersion(es.ID)
		if !ok || ver != es.Version {
			t.Fatalf("%s %s: ScopeVersion(%d) = (%d,%v), want %d", c.Name, where, es.ID, ver, ok, es.Version)
		}
		typ, ok := r.ScopeType(es.ID)
		if !ok || typ != es.Type {
			t.Fatalf("%s %s: ScopeType(%d) = (%d,%v), want %d", c.Name, where, es.ID, typ, ok, es.Type)
		}
		verifyKV(t, r, es.ID, es.KV, c, where)
	}

	// FILE(0) target KV, plus FILE is never a "defined scope".
	verifyKV(t, r, fileScopeID, c.ExpectFileKV, c, where)
	if name, ok := r.ScopeName(fileScopeID); ok {
		t.Fatalf("%s %s: FILE(0) must not be a defined scope (got %q)", c.Name, where, name)
	}
}

// verifyKV checks MetaList(target) equals the expected sorted KV (byte-for-byte values,
// including overflow-spanning ones), and spot-checks MetaGet for a present + absent key.
func verifyKV(t *testing.T, r *Reader, target uint32, want []mKV, c *mcase, where string) {
	t.Helper()
	got, err := r.MetaList(target)
	if err != nil {
		t.Fatalf("%s %s: MetaList(%d): %v", c.Name, where, target, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s %s: MetaList(%d) len %d, want %d", c.Name, where, target, len(got), len(want))
	}
	for i, k := range want {
		wv := k.value(t)
		if !bytes.Equal(got[i].Key, []byte(k.Key)) || got[i].Type != k.Type || !bytes.Equal(got[i].Value, wv) {
			t.Fatalf("%s %s: MetaList(%d)[%d] = (%q,%d,%dB), want (%q,%d,%dB)",
				c.Name, where, target, i, got[i].Key, got[i].Type, len(got[i].Value), k.Key, k.Type, len(wv))
		}
	}

	if len(want) > 0 {
		// Present key: MetaGet returns (type, value) byte-for-byte.
		first := want[0]
		typ, val, ok, err := r.MetaGet(target, []byte(first.Key))
		if err != nil {
			t.Fatalf("%s %s: MetaGet(%d,%q): %v", c.Name, where, target, first.Key, err)
		}
		wv := first.value(t)
		if !ok || typ != first.Type || !bytes.Equal(val, wv) {
			t.Fatalf("%s %s: MetaGet(%d,%q) = (%d,%dB,%v), want (%d,%dB)",
				c.Name, where, target, first.Key, typ, len(val), ok, first.Type, len(wv))
		}
	}
	// Absent key: never present in any case (no corpus key is "__absent__").
	if _, _, ok, err := r.MetaGet(target, []byte("__absent__")); err != nil || ok {
		t.Fatalf("%s %s: MetaGet(%d, absent) = ok=%v err=%v, want miss", c.Name, where, target, ok, err)
	}
}

// --- build a case image via the Writer ---

// applyMeta defines the scopes (id = position+1), applies version/type/kv, writes FILE(0)
// kv. Generic over the key family so v4 and v6 share it.
func applyMeta[K ipKey[K]](t *testing.T, w *Writer[K], c *mcase) {
	t.Helper()
	for _, s := range c.Scopes {
		id, err := w.ScopeDefine([]byte(s.Name))
		if err != nil {
			t.Fatalf("%s: ScopeDefine(%q): %v", c.Name, s.Name, err)
		}
		if s.SetVersion != nil {
			if _, err := w.ScopeSetVersion(id, *s.SetVersion); err != nil {
				t.Fatalf("%s: ScopeSetVersion: %v", c.Name, err)
			}
		}
		if s.SetType != nil {
			if _, err := w.ScopeSetType(id, *s.SetType); err != nil {
				t.Fatalf("%s: ScopeSetType: %v", c.Name, err)
			}
		}
		for _, kv := range s.KV {
			must(t, w.MetaSet(id, []byte(kv.Key), kv.Type, kv.value(t)))
		}
	}
	for _, kv := range c.FileKV {
		must(t, w.MetaSet(fileScopeID, []byte(kv.Key), kv.Type, kv.value(t)))
	}
}

func buildMetaV4(t *testing.T, c *mcase) []byte {
	t.Helper()
	w := CreateV4(c.ScopeWidth, 0)
	applyMeta(t, w, c)
	for _, op := range c.IPOps {
		from, to := parseV4(t, op.From), parseV4(t, op.To)
		applyIPOp(t, func() error { return w.Set(from, to, op.Scope) },
			func() error { return w.Delete(from, to) }, op.Op, c.Name)
	}
	must(t, w.Commit(1))
	return w.Image()
}

func buildMetaV6(t *testing.T, c *mcase) []byte {
	t.Helper()
	w := CreateV6(c.ScopeWidth, 0)
	applyMeta(t, w, c)
	for _, op := range c.IPOps {
		from, to := parseV6(t, op.From), parseV6(t, op.To)
		applyIPOp(t, func() error { return w.Set(from, to, op.Scope) },
			func() error { return w.Delete(from, to) }, op.Op, c.Name)
	}
	must(t, w.Commit(1))
	return w.Image()
}

func applyIPOp(t *testing.T, set, del func() error, op, name string) {
	t.Helper()
	switch op {
	case "set":
		must(t, set())
	case "delete":
		must(t, del())
	default:
		t.Fatalf("case %s: bad ip op %q", name, op)
	}
}

func TestMetadataConformance(t *testing.T) {
	for _, c := range loadMetaCases(t) {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			var img []byte
			switch c.Family {
			case "v4":
				img = buildMetaV4(t, &c)
			case "v6":
				img = buildMetaV6(t, &c)
			default:
				t.Fatalf("unknown family %q", c.Family)
			}

			// 1) verify the freshly built image through the Reader.
			verifyReader(t, img, &c, "built")
			// 2) write this language's golden (Go → .go.iprdb) when regenerating.
			maybeWriteGolden(t, c.Name, img)
			// 3) cross-read: if the OTHER language's golden exists, Go reads it and verifies
			// the SAME expectations (clean bootstrap ⇒ missing golden ⇒ skip).
			if rb, err := os.ReadFile(rustGolden(c.Name)); err == nil {
				verifyReader(t, rb, &c, "rust-golden (Go reads Rust)")
			}
		})
	}
}

// --- programmatic 25-scope case (not in JSON; built identically in Rust) ---

// metaManyCase builds the deterministic 25-scope case: 25 > scope-leaf capacity (14) ⇒ a
// multi-level scope tree. The expectations are derived from the SAME loop the Rust side uses.
func metaManyCase() mcase {
	c := mcase{Name: "meta_many", Family: "v4", ScopeWidth: 0}
	for i := uint32(1); i <= 25; i++ {
		ver := uint64(i) * 10
		typ := uint8(i % 3)
		kv := textKV(fmt.Sprintf("k%d", i), []byte(fmt.Sprintf("v%d", i)))
		c.Scopes = append(c.Scopes, mScope{
			Name: fmt.Sprintf("scope-%d", i), SetVersion: &ver, SetType: &typ, KV: []mKV{kv},
		})
		c.ExpectScopes = append(c.ExpectScopes, mExpScope{
			ID: i, Name: fmt.Sprintf("scope-%d", i), Version: ver, Type: typ, KV: []mKV{kv},
		})
	}
	c.FileKV = []mKV{textKV("root", []byte("top"))}
	c.ExpectFileKV = c.FileKV
	return c
}

func TestMetadataManyScopesMultilevel(t *testing.T) {
	if !(25 > scopeLeafMax()) {
		t.Fatalf("expected scope-leaf cap < 25, got %d", scopeLeafMax())
	}
	c := metaManyCase()
	img := buildMetaV4(t, &c)

	verifyReader(t, img, &c, "built")
	maybeWriteGolden(t, c.Name, img)
	if rb, err := os.ReadFile(rustGolden(c.Name)); err == nil {
		verifyReader(t, rb, &c, "rust-golden (Go reads Rust)")
	}
}
