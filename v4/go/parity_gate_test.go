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

// rootSymbols returns the public free symbols and exported methods of the
// root package as a set of "Name" and "Type.Method" strings. Build tags
// are ignored (the root package surface is platform-neutral; the union is
// the API surface).
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
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							symbols[s.Name.Name] = true
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								symbols[n.Name] = true
							}
						}
					}
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
	closedSurfaces := map[string]bool{"Writer": true, "LiveWriter": true}
	recordedMethods := map[string]map[string]string{} // surface -> symbol -> rust_ref
	var failures []string
	present, missing, removePlanned := 0, 0, 0

	for _, row := range rows {
		sym := row.goSymbol
		switch row.status {
		case "present", "remove-planned":
			present++
			if row.status == "remove-planned" {
				removePlanned++
			}
			if sym != "" && strings.Contains(sym, ".") {
				surface, _, _ := strings.Cut(sym, ".")
				if closedSurfaces[surface] {
					if recordedMethods[surface] == nil {
						recordedMethods[surface] = map[string]string{}
					}
					recordedMethods[surface][sym] = row.rustRef
				}
			}
			if sym == "" || !symbols[sym] {
				failures = append(failures, "missing-required: "+row.class+" | "+row.rustRef+" | "+sym)
			}
		case "missing":
			missing++
			// Present rows flip to missing only in the implementing commit.
			if sym != "" && symbols[sym] {
				failures = append(failures, "unexpected-present: "+row.class+" | "+row.rustRef+" | "+sym+" | "+row.note)
			}
		default:
			t.Fatalf("parity_manifest.tsv: unknown status %q for %s", row.status, row.rustRef)
		}
	}

	// Every public method on the closed surfaces that actually exists
	// must be recorded in the ledger.
	for sym := range symbols {
		for surface := range closedSurfaces {
			prefix := surface + "."
			if strings.HasPrefix(sym, prefix) {
				if _, recorded := recordedMethods[surface][sym]; !recorded {
					failures = append(failures, "unrecorded-"+strings.ToLower(surface)+"-method: "+sym)
				}
			}
		}
	}

	t.Logf("parity ledger: %d present/remove-planned, %d missing", present, missing)
	if len(failures) > 0 {
		t.Errorf("parity ledger drifted from the compiled surface (%d):\n%s", len(failures), strings.Join(failures, "\n"))
	}
}
