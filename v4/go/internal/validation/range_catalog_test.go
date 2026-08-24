package validation

// Slice-C range and catalog validator tests over the Rust conformance
// corpus: every corpus fixture must pass its family walk and its catalog
// checks, and targeted mutations must produce the exact Rust reason
// classes. The context is built directly over a fixture mapping so the
// slice-C validators are exercised without the free-bitmap arm (slice
// D).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// fixturePath resolves one conformance corpus file.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "conformance", "rust", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s missing: %v", name, err)
	}
	return path
}

// fixtureContext opens one corpus fixture into a validation context with
// the given heap budget (the corpus metadata/table sizes need more than
// the sweep-test 1 MiB).
func fixtureContext(t *testing.T, name string, maxHeap uint64) *context {
	t.Helper()
	path := fixturePath(t, name)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	m, err := mapping.MapFile(file, uint64(info.Size()), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p0, err := m.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := m.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := bootstrap.Open(p0, p1, uint64(info.Size()), bootstrap.ModeImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := newContext(m, res.Meta, HeapOnly(maxHeap, 1), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// collectContextFindings runs one validator over a context and returns
// the findings.
func collectContextFindings(t *testing.T, ctx *context, run func(*context) error) []ValidationFinding {
	t.Helper()
	var findings []ValidationFinding
	ctx.sink = SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	})
	if err := run(ctx); err != nil {
		t.Fatalf("validator failed: %v", err)
	}
	// The sink is borrowed: copy the page values the way the sweep
	// helper does (emit borrows only for the call).
	for i := range findings {
		if findings[i].PageNumber != nil {
			page := *findings[i].PageNumber
			findings[i].PageNumber = &page
		}
	}
	return findings
}

func TestValidateRangeCorpusClean(t *testing.T) {
	// Every corpus range tree passes its family walk with zero findings.
	cases := []struct {
		fixture string
		family  uint8
	}{
		{"direct-ipv4.iprdb", 4},
		{"first-seen-ipv6.iprdb", 6},
		{"membership-ipv4.iprdb", 4},
		{"membership-ipv6.iprdb", 6},
		{"structured-ipv4.iprdb", 4},
		{"structured-ipv4-nothreat.iprdb", 4},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			ctx := fixtureContext(t, tc.fixture, 1<<30)
			if ctx.meta.AddressFamily != tc.family {
				t.Fatalf("family %d want %d", ctx.meta.AddressFamily, tc.family)
			}
			if findings := collectContextFindings(t, ctx, validateRange); len(findings) != 0 {
				t.Fatalf("findings %+v", findings)
			}
		})
	}
}

func TestValidateCatalogCorpusClean(t *testing.T) {
	// Every catalog-bearing corpus fixture passes the name/index walks,
	// the used bitmap, and the bijection cross-check.
	cases := []string{
		"membership-ipv4.iprdb",
		"membership-ipv6.iprdb",
		"structured-ipv4.iprdb",
		"structured-ipv4-nothreat.iprdb",
	}
	for _, fixture := range cases {
		t.Run(fixture, func(t *testing.T) {
			ctx := fixtureContext(t, fixture, 1<<30)
			if findings := collectContextFindings(t, ctx, validateCatalog); len(findings) != 0 {
				t.Fatalf("findings %+v", findings)
			}
		})
	}
}

func TestValidateCatalogSkipsDirect(t *testing.T) {
	// A direct database carries no catalog: the validator is a no-op
	// (Rust value-kind gate).
	ctx := fixtureContext(t, "direct-ipv4.iprdb", 1<<20)
	if findings := collectContextFindings(t, ctx, validateCatalog); len(findings) != 0 {
		t.Fatalf("findings %+v", findings)
	}
}
