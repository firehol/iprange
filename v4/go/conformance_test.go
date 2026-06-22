package iprangedb

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Language-neutral behavioral conformance (§12): the shared op-sequences in
// ../conformance/cases.json are run through the writer + reader, and the resulting scan /
// point-lookup results must match the expected values. Then each Rust-written golden
// ../conformance/files/<name>.iprdb is read back and its scan must match expect_scan (the
// mandatory cross-read check, §12 — proving the Go reader reads Rust-written files
// identically).

// These structs mirror the case schema in ../../conformance/README.md.
type ccase struct {
	Name       string  `json:"name"`
	Family     string  `json:"family"`
	ScopeWidth uint8   `json:"scope_width"`
	Ops        []cop   `json:"ops"`
	ExpectScan [][]any `json:"expect_scan"`   // [from, to, scope[]] decimal-string keys
	ExpectLook [][]any `json:"expect_lookup"` // [ip, scope|null]
}

type cop struct {
	Op    string `json:"op"`
	From  string `json:"from"`
	To    string `json:"to"`
	Scope []byte `json:"scope"`
}

// corpusPath resolves a path under v4/conformance. The Go module dir is v4/go and tests
// run with cwd = that dir, so the shared corpus is one level up (../conformance).
func corpusPath(rel string) string { return filepath.Join("..", "conformance", rel) }

// parseV4 parses a decimal uint32 string.
func parseV4(t *testing.T, s string) Ipv4Key {
	t.Helper()
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		t.Fatalf("parse v4 key %q: %v", s, err)
	}
	return Ipv4Key(uint32(v))
}

// parseV6 parses a decimal u128 string into (hi, lo).
func parseV6(t *testing.T, s string) Ipv6Key {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() < 0 || n.BitLen() > 128 {
		t.Fatalf("parse v6 key %q", s)
	}
	lo := new(big.Int).And(n, new(big.Int).SetUint64(maxUint64))
	hi := new(big.Int).Rsh(n, 64)
	return Ipv6Key{Hi: hi.Uint64(), Lo: lo.Uint64()}
}

// scanTriple is one [from, to, scope] entry in key order.
type scanTriple struct {
	from, to string
	scope    []byte
}

func wantScan(t *testing.T, raw [][]any) []scanTriple {
	t.Helper()
	out := make([]scanTriple, 0, len(raw))
	for _, e := range raw {
		out = append(out, scanTriple{from: e[0].(string), to: e[1].(string), scope: jsonBytes(e[2])})
	}
	return out
}

// jsonBytes converts a JSON array of numbers (json.Unmarshal yields []any of float64)
// into a byte slice.
func jsonBytes(v any) []byte {
	arr, _ := v.([]any)
	out := make([]byte, len(arr))
	for i, x := range arr {
		out[i] = byte(x.(float64))
	}
	return out
}

func loadCases(t *testing.T) []ccase {
	t.Helper()
	text, err := os.ReadFile(corpusPath("cases.json"))
	if err != nil {
		t.Fatalf("read cases.json: %v", err)
	}
	var cases []ccase
	if err := json.Unmarshal(text, &cases); err != nil {
		t.Fatalf("parse cases.json: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return cases
}

func TestBehavioralConformance(t *testing.T) {
	for _, c := range loadCases(t) {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			switch c.Family {
			case "v4":
				runConfV4(t, &c)
			case "v6":
				runConfV6(t, &c)
			default:
				t.Fatalf("unknown family %q", c.Family)
			}
		})
	}
}

func runConfV4(t *testing.T, c *ccase) {
	w := CreateV4(c.ScopeWidth, 0)
	for _, op := range c.Ops {
		from, to := parseV4(t, op.From), parseV4(t, op.To)
		switch op.Op {
		case "set":
			must(t, w.Set(from, to, op.Scope))
		case "delete":
			must(t, w.Delete(from, to))
		default:
			t.Fatalf("bad op %q", op.Op)
		}
		// Commit per op: realistic, and reclaims this txn's COW garbage (D7) so the
		// committed golden stays compact instead of one page per set.
		must(t, w.Commit(0))
	}
	if len(c.Ops) == 0 {
		must(t, w.Commit(0))
	}
	img := w.Image()
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}

	got := scanV4(t, r)
	assertScan(t, got, wantScan(t, c.ExpectScan), "scan mismatch")

	for _, lk := range c.ExpectLook {
		ip := parseV4(t, lk[0].(string))
		scope, ok, err := r.LookupV4(ip)
		if err != nil {
			t.Fatal(err)
		}
		assertLookup(t, scope, ok, lk[1], "lookup %s", lk[0])
	}

	// Cross-read: read the Rust-written golden and assert its scan matches.
	crossReadV4(t, c)
}

func runConfV6(t *testing.T, c *ccase) {
	w := CreateV6(c.ScopeWidth, 0)
	for _, op := range c.Ops {
		from, to := parseV6(t, op.From), parseV6(t, op.To)
		switch op.Op {
		case "set":
			must(t, w.Set(from, to, op.Scope))
		case "delete":
			must(t, w.Delete(from, to))
		default:
			t.Fatalf("bad op %q", op.Op)
		}
		must(t, w.Commit(0))
	}
	if len(c.Ops) == 0 {
		must(t, w.Commit(0))
	}
	img := w.Image()
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}

	got := scanV6(t, r)
	assertScan(t, got, wantScan(t, c.ExpectScan), "scan mismatch")

	for _, lk := range c.ExpectLook {
		ip := parseV6(t, lk[0].(string))
		scope, ok, err := r.LookupV6(ip)
		if err != nil {
			t.Fatal(err)
		}
		assertLookup(t, scope, ok, lk[1], "lookup %s", lk[0])
	}

	crossReadV6(t, c)
}

func crossReadV4(t *testing.T, c *ccase) {
	bytesGolden, err := os.ReadFile(corpusPath(filepath.Join("files", c.Name+".iprdb")))
	if err != nil {
		t.Fatalf("read golden %s.iprdb (cross-read): %v", c.Name, err)
	}
	gr, err := Open(bytesGolden)
	if err != nil {
		t.Fatalf("Go reader rejected Rust golden %s.iprdb: %v", c.Name, err)
	}
	assertScan(t, scanV4(t, gr), wantScan(t, c.ExpectScan), "golden cross-read mismatch")
}

func crossReadV6(t *testing.T, c *ccase) {
	bytesGolden, err := os.ReadFile(corpusPath(filepath.Join("files", c.Name+".iprdb")))
	if err != nil {
		t.Fatalf("read golden %s.iprdb (cross-read): %v", c.Name, err)
	}
	gr, err := Open(bytesGolden)
	if err != nil {
		t.Fatalf("Go reader rejected Rust golden %s.iprdb: %v", c.Name, err)
	}
	assertScan(t, scanV6(t, gr), wantScan(t, c.ExpectScan), "golden cross-read mismatch")
}

func scanV4(t *testing.T, r *Reader) []scanTriple {
	var got []scanTriple
	if err := r.ScanV4(func(f, to Ipv4Key, s []byte) {
		got = append(got, scanTriple{
			from:  strconv.FormatUint(uint64(f), 10),
			to:    strconv.FormatUint(uint64(to), 10),
			scope: append([]byte(nil), s...),
		})
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func scanV6(t *testing.T, r *Reader) []scanTriple {
	var got []scanTriple
	if err := r.ScanV6(func(f, to Ipv6Key, s []byte) {
		got = append(got, scanTriple{
			from:  v6ToDecimal(f),
			to:    v6ToDecimal(to),
			scope: append([]byte(nil), s...),
		})
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

// v6ToDecimal renders an Ipv6Key as its decimal u128 string.
func v6ToDecimal(k Ipv6Key) string {
	n := new(big.Int).SetUint64(k.Hi)
	n.Lsh(n, 64)
	n.Or(n, new(big.Int).SetUint64(k.Lo))
	return n.String()
}

func assertScan(t *testing.T, got, want []scanTriple, msg string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d records, want %d\n got %v\n want %v", msg, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i].from != want[i].from || got[i].to != want[i].to || !bytes.Equal(got[i].scope, want[i].scope) {
			t.Fatalf("%s at %d: got %v want %v", msg, i, got[i], want[i])
		}
	}
}

func assertLookup(t *testing.T, scope []byte, ok bool, want any, format string, args ...any) {
	t.Helper()
	if want == nil {
		if ok {
			t.Fatalf(format+": expected miss, got %x", append(args, scope)...)
		}
		return
	}
	wantScope := jsonBytes(want)
	if !ok || !bytes.Equal(scope, wantScope) {
		t.Fatalf(format+": got %x ok=%v, want %x", append(args, scope, ok, wantScope)...)
	}
}
