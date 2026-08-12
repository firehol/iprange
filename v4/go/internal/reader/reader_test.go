package reader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "conformance", "rust", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "conformance", "rust")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func openFixture(t *testing.T, name string) *ImmutableReader {
	t.Helper()
	r, err := OpenImmutable(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// isFormatError reports whether err carries the given format error code.
func isFormatError(err error, code format.ErrorCode) bool {
	for err != nil {
		if fe, ok := err.(*format.Error); ok {
			return fe.Code == code
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func copyFixture(t *testing.T, name, destName string) string {
	t.Helper()
	raw, err := os.ReadFile(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, destName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustCode(err error) format.ErrorCode {
	if err == nil {
		return 0
	}
	ferr, ok := err.(*format.Error)
	if !ok {
		return 0
	}
	return ferr.Code
}

func TestOpenDirectFixture(t *testing.T) {
	r, err := OpenImmutable(fixture(t, "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Selection() != MetaSelectionProvenCurrent {
		t.Fatalf("selection %d", r.Selection())
	}
	if r.Meta().TxnID != 2 {
		t.Fatalf("txn %d", r.Meta().TxnID)
	}
	// Whole-page CRC of both metas is part of identity; selection proven.
}

func TestOpenRejections(t *testing.T) {
	// Symlink traversal.
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real.iprdb")
		raw, err := os.ReadFile(fixture(t, "direct-ipv4.iprdb"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(real, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link.iprdb")
		if err := os.Symlink(real, link); err != nil {
			t.Skip("symlinks unsupported:", err)
		}
		if _, err := OpenImmutable(link); err == nil {
			t.Fatal("symlink open accepted")
		}
	})
	// Reserved basenames.
	for _, name := range []string{".iprange-x.iprdb", "main.readers"} {
		t.Run("basename-"+name, func(t *testing.T) {
			path := copyFixture(t, "direct-ipv4.iprdb", name)
			if _, err := OpenImmutable(path); err == nil {
				t.Fatalf("reserved basename %q accepted", name)
			}
		})
	}
	// External sidecar present.
	t.Run("sidecar", func(t *testing.T) {
		raw, err := os.ReadFile(fixture(t, "direct-ipv4.iprdb"))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		main := filepath.Join(dir, "live.iprdb")
		if err := os.WriteFile(main, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(main+".readers", []byte{0}, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenImmutable(main); err == nil {
			t.Fatal("live database with sidecar opened immutable")
		}
	})
}

// patchMeta mutates both meta pages with repaired CRCs.
func patchMeta(t *testing.T, path string, mutate func(page []byte)) {
	t.Helper()
	patchMetaEach(t, path, func(_ int, page []byte) { mutate(page) })
}

// patchMetaEach mutates each meta page independently with repaired CRCs.
func patchMetaEach(t *testing.T, path string, mutate func(pg int, page []byte)) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for p := 0; p < 2; p++ {
		page := make([]byte, format.PageSize)
		if _, err := file.ReadAt(page, int64(p*format.PageSize)); err != nil {
			t.Fatal(err)
		}
		mutate(p, page)
		format.PutU32(page[252:256], format.MetaCRC32C(page))
		if _, err := file.WriteAt(page, int64(p*format.PageSize)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBootstrapSelectionMatrix(t *testing.T) {
	// Sole meta: damage page 0's magic; page 1 must be selected as SoleMeta1.
	t.Run("sole-meta-1", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "sole.iprdb")
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte{'X'}, 0); err != nil {
			t.Fatal(err)
		}
		file.Close()
		r, err := OpenImmutable(path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if r.Selection() != MetaSelectionSoleMeta1 {
			t.Fatalf("selection %d", r.Selection())
		}
	})
	// Both metas damaged: not v4.
	t.Run("both-damaged", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "both.iprdb")
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		file.WriteAt([]byte{'X'}, 0)
		file.WriteAt([]byte{'X'}, format.PageSize)
		file.Close()
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
	// Conflicting static identity with valid CRCs: page 1 changes family to
	// 6 (still a valid identity) while page 0 stays family 4.
	t.Run("conflicting-identity", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "conflict.iprdb")
		patchMetaEach(t, path, func(pg int, page []byte) {
			if pg == 1 {
				page[11] = 6
			}
		})
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
	// Direct file with an unknown nonzero structure kind is the
	// KindInvariant class (bootstrap.rs validate_direct), not the typed
	// unsupported error: only a structured file with an unknown nonzero
	// kind reports UnsupportedStructure.
	t.Run("direct-unknown-structure-kind", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "struct2.iprdb")
		patchMeta(t, path, func(page []byte) { page[13] = 2 })
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
	// Transaction gap: page 1 txn jumps by five.
	t.Run("txn-gap", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "gap.iprdb")
		patchMetaEach(t, path, func(pg int, page []byte) {
			extra := uint64(0)
			if pg == 1 {
				extra = 5
			}
			txn := format.U64(page[48:56]) + extra + 100
			format.PutU64(page[48:56], txn)
		})
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
	// Equal txn with differing meta images.
	t.Run("equal-txn-differing-images", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "equal.iprdb")
		patchMetaEach(t, path, func(pg int, page []byte) {
			format.PutU64(page[48:56], 7000) // same txn both pages
			if pg == 1 {
				page[56] ^= 0xff // differing nonce → differing images
			}
		})
		if _, err := OpenImmutable(path); mustCode(err) != format.CodeFormatInvalid {
			t.Fatalf("code %v want 32", err)
		}
	})
}

func TestOpenAllFixtures(t *testing.T) {
	for _, name := range []string{
		"direct-ipv4.iprdb", "first-seen-ipv6.iprdb", "membership-ipv4.iprdb",
		"membership-ipv6.iprdb", "structured-ipv4.iprdb",
	} {
		if _, err := OpenImmutable(fixture(t, name)); err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
	}
}

func TestCloseReleasesMapping(t *testing.T) {
	r := openFixture(t, "direct-ipv4.iprdb")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Double close must not panic.
	_ = r.Close()
}

// TestLookupBoundarySemantics probes boundary-adjacent points on the
// direct fixture's exact records.
func TestLookupBoundarySemantics(t *testing.T) {
	r := openFixture(t, "direct-ipv4.iprdb")
	cases := []struct {
		addr  uint32
		value uint32
		found bool
	}{
		{0x0a000009, 0, false},
		{0x0a00000a, 2, true},
		{0x0a00000e, 2, true},
		{0x0a00000f, 3, true},
		{0x0a000011, 3, true},
		{0x0a000012, 2, true},
		{0x0a000015, 2, true},
		{0x0a000016, 0, false},
		{0x0a00001c, 1, true},
		{0x0a00001f, 1, true},
		{0x0a000020, 0, false},
	}
	for _, tc := range cases {
		v, found, err := r.LookupDirect4(tc.addr)
		if err != nil {
			t.Fatal(err)
		}
		if found != tc.found || (found && v != tc.value) {
			t.Errorf("addr %x: (%d,%v) want (%d,%v)", tc.addr, v, found, tc.value, tc.found)
		}
	}
}

// TestFullIPv6Lookup probes the full-space first-seen fixture.
func TestFullIPv6Lookup(t *testing.T) {
	r := openFixture(t, "first-seen-ipv6.iprdb")
	for _, addr := range []struct {
		hi, lo uint64
	}{
		{0, 0},
		{1, 2},
		{^uint64(0) / 2, 42},
		{^uint64(0), ^uint64(0)},
	} {
		v, found, err := r.LookupDirect6(addr.hi, addr.lo)
		if err != nil {
			t.Fatal(err)
		}
		if !found || v != 1700000000 {
			t.Errorf("addr %x:%x: (%d,%v)", addr.hi, addr.lo, v, found)
		}
	}
}

// TestMembershipViewWords verifies word-level access on the membership
// fixtures.
func TestMembershipViewWords(t *testing.T) {
	r := openFixture(t, "membership-ipv4.iprdb")
	view, found, err := r.LookupMembership4(0x0a000000) // 10.0.0.0
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	// 10.0.0.0-255 has feed-000, feed-reused(5), feed-063, feed-064,
	// feed-069; bits 0, 5, 63, 64, 69.
	for _, idx := range []uint32{0, 5, 63, 64, 69} {
		has, err := view.ContainsIndex(idx)
		if err != nil {
			t.Fatal(err)
		}
		if !has {
			t.Errorf("expected feed %d in 10.0.0.0 membership", idx)
		}
	}
	for _, idx := range []uint32{1, 2, 62, 65} {
		has, err := view.ContainsIndex(idx)
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Errorf("unexpected feed %d in 10.0.0.0 membership", idx)
		}
	}
	// An index at or beyond the generation's feed limit is InvalidArgument.
	if _, err := view.ContainsIndex(70); err == nil || !isFormatError(err, format.CodeInvalidArgument) {
		t.Fatalf("out-of-limit feed: %v", err)
	}
	w0, ok, err := view.Word(0)
	if err != nil || !ok {
		t.Fatalf("word0: %v %v", ok, err)
	}
	if w0&(1<<0) == 0 || w0&(1<<5) == 0 || w0&(1<<63) == 0 || w0&(1<<1) != 0 {
		t.Fatalf("word0 bits %x", w0)
	}
	w1, ok, err := view.Word(1)
	if err != nil || !ok {
		t.Fatalf("word1: %v %v", ok, err)
	}
	if w1&(1<<0) == 0 || w1&(1<<5) == 0 {
		t.Fatalf("word1 bits %x", w1)
	}
}
