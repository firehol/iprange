package iprangedb

// Parity gate for the Go/Rust v4 SDK reconciliation ledger (SOW-0027
// milestone 1). The ledger (parity_manifest.tsv) records every material
// Rust public operation and its Go symbol or missing status. This test
// enforces the ledger against the compiled root-package surface:
//
//   - a row with status present or remove-planned requires the listed Go
//     symbol to exist in this package;
//   - a row with status missing requires the symbol to stay absent (rows
//     flip to present only in the commit that implements the operation);
//   - the off-contract Writer surface was removed in SOW-0027 slice 2c:
//     no public Writer symbol may exist, and a re-added sidecar-free
//     mutation method fails CI as unrecorded until it is deliberately
//     recorded in the ledger.
//
// The ledger is the record; this test is the tripwire that keeps the
// record truthful across milestones.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rootSymbols returns the public free functions and exported methods of
// the root package as a set of "Name" and "Type.Method" strings. The
// operation ledger tracks functions and methods only; exported types and
// constants mirror the Rust counterparts by name and are not enumerated
// as rows. Build tags are ignored (the root package surface is
// platform-neutral; the union is the API surface).
func rootSymbols(t *testing.T) map[string]bool {
	t.Helper()
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	symbols := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					symbols[d.Name.Name] = true
					continue
				}
				recv := receiverType(d.Recv)
				if recv != "" {
					symbols[recv+"."+d.Name.Name] = true
				}
			}
		}
	}
	return symbols
}

// receiverType extracts the exported type name of one method receiver.
func receiverType(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	expr := fields.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.Ident:
			if e.IsExported() {
				return e.Name
			}
			return ""
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		default:
			return ""
		}
	}
}

// parityManifestRow is one parity ledger row.
type parityManifestRow struct {
	class    string
	rustRef  string
	goSymbol string
	status   string
	note     string
}

func loadParityManifest(t *testing.T) []parityManifestRow {
	t.Helper()
	data, err := os.ReadFile("parity_manifest.tsv")
	if err != nil {
		t.Fatal(err)
	}
	var rows []parityManifestRow
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("parity_manifest.tsv:%d: want 5 tab fields, got %d", i+1, len(fields))
		}
		rows = append(rows, parityManifestRow{
			class:    fields[0],
			rustRef:  fields[1],
			goSymbol: fields[2],
			status:   fields[3],
			note:     fields[4],
		})
	}
	return rows
}

func TestParityLedgerMatchesTheGoSurface(t *testing.T) {
	symbols := rootSymbols(t)
	rows := loadParityManifest(t)
	// The removed off-contract Writer surface must stay absent and the
	// normative LiveWriter surface is closed: every public method that
	// actually exists must be recorded in the ledger, so a new
	// unrecorded method fails CI until it is deliberately recorded.
	recordedSymbols := map[string]string{} // goSymbol -> rust_ref
	var failures []string
	present, missing := 0, 0
	missingRefs := map[string]string{} // rustRef -> note

	for _, row := range rows {
		sym := row.goSymbol
		if sym != "" {
			recordedSymbols[sym] = row.rustRef
		}
		switch row.status {
		case "present", "remove-planned":
			present++
			if sym == "" || !symbols[sym] {
				failures = append(failures, "missing-required: "+row.class+" | "+row.rustRef+" | "+sym)
			}
		case "missing":
			missing++
			missingRefs[row.rustRef] = row.note
			if strings.TrimSpace(row.note) == "" {
				failures = append(failures, "missing-without-record: "+row.class+" | "+row.rustRef)
			}
			// A row flips from missing to present only in the commit
			// that implements the operation.
			if sym != "" && symbols[sym] {
				failures = append(failures, "unexpected-present: "+row.class+" | "+row.rustRef+" | "+sym+" | "+row.note)
			}
		case "removed":
			// Deliberate absence with evidence in the note (slice 2c
			// off-contract removals): the operation must stay absent,
			// like a missing row, but it is closed by decision rather
			// than tracked as required-missing work.
			if strings.TrimSpace(row.note) == "" {
				failures = append(failures, "removed-without-record: "+row.class+" | "+row.rustRef)
			}
			if sym != "" && symbols[sym] {
				failures = append(failures, "unexpected-present: "+row.class+" | "+row.rustRef+" | "+sym+" | "+row.note)
			}
		default:
			t.Fatalf("parity_manifest.tsv: unknown status %q for %s", row.status, row.rustRef)
		}
	}

	// Every exported root symbol (free function or method on an
	// exported type) must be recorded in the ledger: the full Go
	// surface is closed, so a new public symbol fails CI until it is
	// deliberately recorded with its Rust counterpart (or marked as a
	// Go-surface convenience with rust_ref "-").
	for sym := range symbols {
		if _, recorded := recordedSymbols[sym]; !recorded {
			failures = append(failures, "unrecorded-symbol: "+sym)
		}
	}

	// Required-missing enforcement: the ledger may keep an operation
	// missing only while it stays on the recorded closure-required
	// list, and every listed item must remain visible in the ledger.
	// Implementing an item flips its row to present and shrinks the
	// list in the same commit.
	requiredMissing := map[string]string{
		"CancellationToken::from_poll":                       "Rust async poll integration is not portable to Go; the nil token is the uncancellable form (recorded divergence)",
		"scratch_maintenance remove_checkpointed_scratch":    "public scratch-removal export; the internal machine exists in internal/recovery",
		"binary-format-v4.md:3155+ version-matched worker":   "worker containment implemented (m5 slice E + regression 2026-08-29 fail-closed) as an internal routing mechanism with no root symbol; the row stays missing-by-symbol with the implementation evidence in its note",
		"publication/security/apple.rs filesec creator-only": "darwin creator-only publication machine is internal (implemented and proven in the authorized arm64 round)",
		"lib-reexport Cardinality129":                        "public typed cardinality re-export; Go keeps Cardinality129 internal",
	}
	for ref, note := range requiredMissing {
		if _, listed := missingRefs[ref]; !listed {
			failures = append(failures, "required-missing-not-listed: "+ref+" ("+note+")")
		}
	}
	for ref := range missingRefs {
		if _, allowed := requiredMissing[ref]; !allowed {
			failures = append(failures, "unplanned-missing: "+ref)
		}
	}

	t.Logf("parity ledger: %d present, %d required-missing, %d rows", present, missing, len(rows))
	if len(failures) > 0 {
		t.Errorf("parity ledger drifted from the compiled surface (%d):\n%s", len(failures), strings.Join(failures, "\n"))
	}
}
