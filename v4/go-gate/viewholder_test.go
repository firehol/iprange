package main

import (
	"go/token"
	"testing"
)

// checkViewHolderExports is the SOW-0025 boundary whitelist: mapped page
// views may exist only in internal/mapping, internal/format,
// internal/reader, internal/writer, and the public facade. These unit
// cases pin the rule itself; battery forms B1-B4 pin the end-to-end
// behavior for the archived full battery.
func TestViewHolderExports(t *testing.T) {
	mapped := fieldTaint{tainted: true, mapped: true}
	bounded := fieldTaint{tainted: true} // record value, not mapped

	cases := []struct {
		name    string
		pkg     string
		sums    map[string]*funcSummary
		wantGap bool
	}{
		{
			name: "holder export stays legal (reader)",
			pkg:  "github.com/firehol/iprange/v4/go/internal/reader",
			sums: map[string]*funcSummary{
				"page": {results: []fieldTaint{mapped}},
			},
		},
		{
			name: "holder export stays legal (public facade)",
			pkg:  "github.com/firehol/iprange/v4/go",
			sums: map[string]*funcSummary{
				"leaf": {results: []fieldTaint{mapped}},
			},
		},
		{
			name: "non-holder mapped result fails",
			pkg:  "github.com/firehol/iprange/v4/go/internal/tree",
			sums: map[string]*funcSummary{
				"leak": {results: []fieldTaint{mapped}},
			},
			wantGap: true,
		},
		{
			name: "non-holder mapped struct-field result fails",
			pkg:  "github.com/firehol/iprange/v4/go/internal/bitmap",
			sums: map[string]*funcSummary{
				"leak": {fields: map[string]fieldTaint{"Data": mapped}},
			},
			wantGap: true,
		},
		{
			name: "non-holder bounded record value stays legal",
			pkg:  "github.com/firehol/iprange/v4/go/internal/tree",
			sums: map[string]*funcSummary{
				"insert": {results: []fieldTaint{bounded}},
			},
		},
		{
			// The summary machinery records mapped provenance on the
			// per-source maxSrc records (maxSrcOf propagates pv.mapped);
			// fieldTaint.mapped is AND-joined and false for
			// single-source results, so this is the authoritative
			// signal checkViewHolderExports must honor.
			name: "non-holder result with a mapped source record fails",
			pkg:  "github.com/firehol/iprange/v4/go/internal/retire",
			sums: map[string]*funcSummary{
				"leak": {results: []fieldTaint{{tainted: true, srcs: []maxSrc{{kind: "const", constVal: 4096, mapped: true}}}}},
			},
			wantGap: true,
		},
		{
			name: "non-holder result with owned sources stays legal",
			pkg:  "github.com/firehol/iprange/v4/go/internal/retire",
			sums: map[string]*funcSummary{
				"copy": {results: []fieldTaint{{tainted: true, srcs: []maxSrc{{kind: "const", constVal: 4096}}}}},
			},
		},
		{
			name: "non-holder without any page flow stays legal",
			pkg:  "github.com/firehol/iprange/v4/go/internal/retire",
			sums: map[string]*funcSummary{
				"clean": {results: []fieldTaint{{}}},
			},
		},
		{
			// The public facade is the last holder: everything it
			// exports reaches application code, so exported mapped
			// results must fail even inside the holder set.
			name: "public facade exported mapped result fails",
			pkg:  "github.com/firehol/iprange/v4/go",
			sums: map[string]*funcSummary{
				"PageBytes": {results: []fieldTaint{mapped}},
			},
			wantGap: true,
		},
		{
			name: "public facade exported mapped struct-field result fails",
			pkg:  "github.com/firehol/iprange/v4/go",
			sums: map[string]*funcSummary{
				"View": {fields: map[string]fieldTaint{"Page": mapped}},
			},
			wantGap: true,
		},
		{
			// Unexported root helpers may pass views between holders;
			// the exported wrapper that consumes them is what must stay
			// clean.
			name: "public facade unexported mapped result stays legal",
			pkg:  "github.com/firehol/iprange/v4/go",
			sums: map[string]*funcSummary{
				"leaf": {results: []fieldTaint{mapped}},
			},
		},
		{
			name: "public facade exported bounded record value stays legal",
			pkg:  "github.com/firehol/iprange/v4/go",
			sums: map[string]*funcSummary{
				"Lookup": {results: []fieldTaint{bounded}},
			},
		},
		{
			name: "public facade method exported mapped result fails",
			pkg:  "github.com/firehol/iprange/v4/go",
			sums: map[string]*funcSummary{
				"ImmutableReader.PageBytes": {results: []fieldTaint{mapped}},
			},
			wantGap: true,
		},
		{
			name: "public facade method unexported mapped result stays legal",
			pkg:  "github.com/firehol/iprange/v4/go",
			sums: map[string]*funcSummary{
				"ImmutableReader.pageBytes": {results: []fieldTaint{mapped}},
			},
		},
		{
			name: "new package mapped result fails",
			pkg:  "github.com/firehol/iprange/v4/go/gatemut_leak",
			sums: map[string]*funcSummary{
				"leak": {results: []fieldTaint{mapped}},
			},
			wantGap: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gaps := []string{}
			checkViewHolderExports(func(_ token.Pos, format string, args ...any) {
				gaps = append(gaps, format)
			}, c.pkg, c.sums)
			if (len(gaps) > 0) != c.wantGap {
				t.Fatalf("got %d violations, want gap=%v: %v", len(gaps), c.wantGap, gaps)
			}
		})
	}
}
