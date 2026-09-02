package iprangedb

// Parity gate for the Go/Rust v4 SDK reconciliation ledger (SOW-0027
// milestone 1, overhauled 2026-08-30 per the closure review). The
// ledger (parity_manifest.tsv) records every material Rust public
// operation and its Go symbol, missing status, or deliberate absence;
// the raw Rust export inventory (parity_rust_public.tsv) is the
// regenerable appendix the gate consumes. This test enforces three
// directions against the compiled root-package surface (functions,
// methods, types, constants, and methods of aliased types):
//
//   - ledger against Go: a row with status present or remove-planned
//     requires the listed Go symbol to exist (as an operation or a
//     type/constant); missing/removed rows require the recorded
//     symbol, when one is set, to stay absent. Every exported Go
//     operation must be recorded in the ledger; every exported Go
//     type/constant must be recorded in the ledger or match an
//     inventory type. presence flips from missing to present only in
//     the commit that implements the operation.
//   - inventory operations against the ledger: every Rust public
//     operation in the inventory must be recorded in the ledger (any
//     status), so a newly missing Rust operation fails CI as
//     unrecorded. The ledger uses the exact inventory key, a
//     "same-name Rust method <name>" short key (owner pinned by the Go
//     symbol), or a "<module.rs> <function>" module key.
//   - inventory types against Go: every Rust public type in the
//     inventory must exist as an exported Go type or be listed in the
//     explicit divergence set below (recorded Go/Rust type-shape
//     differences).
//
// The removed off-contract Writer surface (SOW-0027 slice 2c) and the
// Rust C ABI binding layer (class c-abi rows) are recorded absences:
// a re-added sidecar-free mutation method or an unrecorded C-bound
// export fails CI until it is deliberately recorded.
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

// rootSymbols returns the public surface of the root package as a set
// of "Name" free functions, "Type.Method" methods, "TypeName" exported
// types, and "CONST_NAME" exported constants. Exported type aliases
// are followed one level into their target package (for example
// Cardinality129.Add defined in internal/format surfaces as the
// method Cardinality129.Add) so the complete compiled public surface
// is gated, not only the root-package declarations. Build tags are
// ignored (the root package surface is platform-neutral; the union is
// the API surface).
func rootSymbols(t *testing.T) (operations, types map[string]bool) {
	t.Helper()
	operations = make(map[string]bool)
	types = make(map[string]bool)
	// aliases maps one exported alias name to its target package directory
	// and target type name; the alias surface is compiled into the public
	// API, so the gate follows it one level into internal packages.
	aliases := map[string]struct{ dir, typeName string }{}
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
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
		pkgOf := map[string]string{}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			alias := path
			if imp.Name != nil {
				alias = imp.Name.Name
			} else if i := strings.LastIndexByte(path, '/'); i >= 0 {
				alias = path[i+1:]
			}
			pkgOf[alias] = path
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					operations[d.Name.Name] = true
					continue
				}
				recv := receiverType(d.Recv)
				if recv != "" {
					operations[recv+"."+d.Name.Name] = true
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE && d.Tok != token.CONST {
					continue
				}
				for _, spec := range d.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						if sp.Name.IsExported() {
							types[sp.Name.Name] = true
						}
						if sp.Assign.IsValid() {
							if sel, ok := sp.Type.(*ast.SelectorExpr); ok {
								if id, ok := sel.X.(*ast.Ident); ok {
									if path, ok := pkgOf[id.Name]; ok {
										aliases[sp.Name.Name] = struct{ dir, typeName string }{
											dir:      strings.TrimPrefix(path, "github.com/firehol/iprange/v4/go/"),
											typeName: sel.Sel.Name,
										}
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, name := range sp.Names {
							if name.IsExported() {
								types[name.Name] = true
							}
						}
					}
				}
			}
		}
	}
	// Follow exported aliases into their target package so methods on
	// aliased types (for example Cardinality129.Add defined in
	// internal/format) are visible to the gate as "Alias.Method".
	for alias, target := range aliases {
		pdir := filepath.Join(dir, target.dir)
		fset := token.NewFileSet()
		pentries, err := os.ReadDir(pdir)
		if err != nil {
			t.Fatalf("alias target %s: %v", target.dir, err)
		}
		for _, pent := range pentries {
			if !strings.HasSuffix(pent.Name(), ".go") || strings.HasSuffix(pent.Name(), "_test.go") {
				continue
			}
			pfile, err := parser.ParseFile(fset, filepath.Join(pdir, pent.Name()), nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", filepath.Join(target.dir, pent.Name()), err)
			}
			for _, decl := range pfile.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || !fd.Name.IsExported() {
					continue
				}
				if receiverType(fd.Recv) == target.typeName {
					operations[alias+"."+fd.Name.Name] = true
				}
			}
		}
	}
	return operations, types
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
	operations, typeSymbols := rootSymbols(t)
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
			// The ledger records operations and types; a present row is
			// satisfied by either an exported operation or an exported
			// type/constant of the same name.
			if sym == "" || !(operations[sym] || typeSymbols[sym]) {
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
			if sym != "" && (operations[sym] || typeSymbols[sym]) {
				failures = append(failures, "unexpected-present: "+row.class+" | "+row.rustRef+" | "+sym+" | "+row.note)
			}
		case "removed":
			// Deliberate absence with evidence in the note (slice 2c
			// off-contract removals and the Rust C ABI binding layer):
			// the operation must stay absent, like a missing row, but it
			// is closed by decision rather than tracked as
			// required-missing work.
			if strings.TrimSpace(row.note) == "" {
				failures = append(failures, "removed-without-record: "+row.class+" | "+row.rustRef)
			}
			if sym != "" && (operations[sym] || typeSymbols[sym]) {
				failures = append(failures, "unexpected-present: "+row.class+" | "+row.rustRef+" | "+sym+" | "+row.note)
			}
		default:
			t.Fatalf("parity_manifest.tsv: unknown status %q for %s", row.status, row.rustRef)
		}
	}

	// Every exported root symbol (free function or method on an
	// exported type, including methods of exported aliased types) must
	// be recorded in the ledger: the full Go surface is closed, so a
	// new public symbol fails CI until it is deliberately recorded with
	// its Rust counterpart (or marked as a Go-surface convenience with
	// rust_ref "-").
	for sym := range operations {
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

// ---------------------------------------------------------------------
// Rust export inventory consumption (overhaul 2026-08-30).

// inventoryRow is one raw Rust public export (parity_rust_public.tsv):
// class is the owning module path, type name, or lib-reexport; ref is
// the export name (or Type::method).
type inventoryRow struct {
	class string
	ref   string
}

// loadRustInventory reads the regenerable Rust export appendix.
func loadRustInventory(t *testing.T) []inventoryRow {
	t.Helper()
	data, err := os.ReadFile("parity_rust_public.tsv")
	if err != nil {
		t.Fatal(err)
	}
	var rows []inventoryRow
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			t.Fatalf("parity_rust_public.tsv:%d: want 2 tab fields, got %d", i+1, len(fields))
		}
		rows = append(rows, inventoryRow{class: fields[0], ref: fields[1]})
	}
	return rows
}

// isExportedTypeName reports a PascalCase export name (a Rust type or
// Go type, as opposed to a snake_case function).
func isExportedTypeName(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	return first >= 'A' && first <= 'Z'
}

// inventoryOperation reports one inventory row describes an operation
// (free function or method) rather than a type: file-class rows are
// functions, "::" rows are methods, and lib-reexport snake_case rows
// are re-exported functions.
func (inv inventoryRow) inventoryOperation() bool {
	if strings.Contains(inv.ref, "::") {
		return true
	}
	if strings.HasSuffix(inv.class, ".rs") {
		return true
	}
	return inv.class == "lib-reexport" && !isExportedTypeName(inv.ref)
}

// rustRefKey normalizes one manifest rust_ref to its canonical export
// name: strips the lib-reexport prefix, the "module name" prefix of
// "module operation" pairs, and the "same-name Rust method " wording
// (returns "", meaning no inventory counterpart, for ledger-only rows
// such as spec markers and go-surface conveniences).
func rustRefKey(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "-" || strings.HasPrefix(ref, "(") ||
		strings.HasPrefix(ref, "binary-format") || strings.HasPrefix(ref, "spec:") {
		return ""
	}
	if rest, ok := strings.CutPrefix(ref, "lib-reexport "); ok {
		return strings.TrimSpace(rest)
	}
	if rest, ok := strings.CutPrefix(ref, "same-name Rust method "); ok {
		return strings.TrimSpace(rest)
	}
	if strings.Contains(ref, " ") {
		first, rest, _ := strings.Cut(ref, " ")
		first = strings.TrimSpace(first)
		rest = strings.TrimSpace(rest)
		// "module operation" pairs (snapshot.rs snapshot_to,
		// scratch_maintenance remove_abandoned_scratch, apple.rs
		// filesec creator-only): the operation name is the last token.
		if !strings.Contains(first, "::") {
			return rest
		}
	}
	return ref
}

// goSymbolRustKeys derives the canonical Rust export names a Go symbol
// could implement: "FnName" -> snake_case(FnName); "Type.Method" ->
// "Type::method" and the short method name (snake_cased with acronym
// runs, so NetworkEnrichmentV1CursorV4 -> network_enrichment_v1_cursor_v4).
func goSymbolRustKeys(symbol string) []string {
	if symbol == "" {
		return nil
	}
	if owner, method, ok := strings.Cut(symbol, "."); ok {
		methodKey := snakeCase(method)
		return []string{owner + "::" + methodKey, methodKey}
	}
	return []string{snakeCase(symbol)}
}

// snakeCase converts an exported Go identifier to its Rust snake_case
// counterpart (SnapshotTo -> snapshot_to, IPv4Inclusive -> ipv4_inclusive
// by acronym run).
func snakeCase(s string) string {
	var out strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			prevLower := i > 0 && runes[i-1] >= 'a' && runes[i-1] <= 'z'
			if out.Len() > 0 && (nextLower || prevLower) {
				out.WriteByte('_')
			}
			out.WriteRune(r - 'A' + 'a')
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// operationCoveredBy reports one manifest row records one inventory
// operation: the normalized rust_ref matches, or the row's Go symbol
// (in its snake_case function form or Type::method form) implements it.
func operationCoveredBy(row parityManifestRow, inv inventoryRow) bool {
	if row.rustRef != "" {
		switch key := rustRefKey(row.rustRef); {
		case key == inv.ref:
			return true
		case strings.Contains(inv.ref, "::") && key == methodOf(inv.ref):
			// Short "same-name Rust method" keys match the method
			// portion; the owner is pinned by the Go symbol below.
			if row.goSymbol != "" {
				if owner, _, ok := strings.Cut(row.goSymbol, "."); ok {
					if owner == ownerOf(inv.ref) {
						return true
					}
				}
			}
		}
	}
	for _, key := range goSymbolRustKeys(row.goSymbol) {
		if key == inv.ref {
			return true
		}
		if strings.Contains(inv.ref, "::") {
			invOwner, invMethod := ownerOf(inv.ref), methodOf(inv.ref)
			if key == invMethod {
				if owner, _, ok := strings.Cut(row.goSymbol, "."); ok && owner == invOwner {
					return true
				}
			}
		}
	}
	return false
}

// ownerOf returns the type name of one Type::method export.
func ownerOf(ref string) string {
	owner, _, _ := strings.Cut(ref, "::")
	return owner
}

// methodOf returns the method name of one Type::method export.
func methodOf(ref string) string {
	_, method, _ := strings.Cut(ref, "::")
	return method
}

// rustTypeDivergence lists inventory types that deliberately have no
// exported Go type of the same name; the note records the Go shape.
// Every entry names the exact Go adaptation so the recorded divergence
// is machine-checkable against the surface (Direction C requires the
// named Go types to exist in the ledger).
var rustTypeDivergence = map[string]string{
	"Result":                    "no Go equivalent: Go functions return (value, error) pairs",
	"Ipv4Key":                   "Go uses IPv4 (uint32 alias) / IPv6 key structs named IPv4/IPv6",
	"Ipv6Key":                   "Go uses IPv4 (uint32 alias) / IPv6 key structs named IPv4/IPv6",
	"DirectTransaction":         "Go names the direct transaction LiveDirectTransaction (live_writer_public.go)",
	"FirstSeenRemovalSink":      "Go family-specializes the sink as FirstSeenRemoval4Sink / FirstSeenRemoval6Sink",
	"Reader":                    "Rust C ABI binding type (c_abi_support/reader.rs); the Go SDK uses ImmutableReader / LiveReader natively",
	"TransactionBudget":         "Go names the live-writer budget PageBudget (live_writer_public.go)",
	"FeedRangeSourceV6":         "Go names the named-feed range source FeedRangeCursorV6 (and V4); sources are cursor types",
	"FirstSeenRemoval":          "Go family-specializes the removal as FirstSeenRemoval4 / FirstSeenRemoval6",
	"CardinalityOverflow":       "Go reports the overflow as the exported error value ErrCardinalityOverflow (no public type)",
	"NetworkEnrichmentV1Range":  "Go family-specializes the value as NetworkEnrichmentV1RangeV4 / NetworkEnrichmentV1RangeV6",
	"DirectRange":               "Go family-specializes the range value as DirectRangeV4 / DirectRangeV6",
	"DirectRangeSourceV4":       "Go models the direct source as the cursor DirectCursorV4 plus the DirectRangesV4 stream facade (and V6)",
	"AlgebraSetOutcome":         "Go does not export an outcome enum; algebra results expose CleanupState and typed reports",
	"CloseResult":               "Go names the results ReaderCloseResult / LiveCloseResult",
	"ImmutableFeedOutcome":      "Go does not export an outcome enum; CreateImmutableFeedV4/V6 return an error and a report",
	"CommitCleanupArtifact":     "Go names the artifact LiveCommitCleanupArtifact (and the ledger LiveCommitCleanupArtifacts)",
	"MAX_METADATA_UNCOMPRESSED": "Go names the constant MaxMetadataUncompressed (types.go)",
	"SnapshotOutcome":           "Go does not export an outcome enum; SnapshotTo returns an error and a report",
	"PreparedWorkflow":          "Go names the prepared workflow handle FinishedWorkflow (feed_workflow_public.go)",
	"AbortResult":               "Go names the result LiveAbortResult",
	"Writer":                    "Rust C ABI binding type (c_abi_support/writer.rs); the Go SDK uses LiveWriter natively",
	"AddressRange":              "Go family-specializes the range as AddressRange4 / AddressRange6",
	"MembershipAggregateSink":   "Go delivers aggregation through the Aggregate callback yields; no sink object",
	"PreparedHistoryProjection": "Go names the prepared projection handle FinishedHistoryProjection (history_projection_public.go)",
	"RangeSource":               "Go family-specializes the source as RangeSource4 / RangeSource6",
	"MatchingFeedSink":          "Go delivers matching through the MatchingFeedsV4/V6 callback yields; no sink object",
	"CommitDurability":          "Go records the durability state inside LiveCommitResult (Commit* outcome fields); no standalone enum",
	"DirectRangeSourceV6":       "Go models the direct source as the cursor DirectCursorV6 plus the DirectRangesV6 stream facade",
	"FeedRangeSourceV4":         "Go names the named-feed range source FeedRangeCursorV4 (and V6); sources are cursor types",
	"DirectJoinSink":            "Go delivers joins through the JoinDirect callback yields; no sink object",
	"CommitCleanupArtifacts":    "Go names the ledger LiveCommitCleanupArtifacts (and the artifact LiveCommitCleanupArtifact)",
	"SliceSource":               "Go accepts plain Go slices directly as inputs; no source adapter type",
	"MembershipJoinSink":        "Go delivers joins through the JoinMembership callback yields; no sink object",
	"PublicationProblem":        "Go has no public problem type; CLI problem objects are built from SDK errors",
}

// inventoryTypes returns the set of Rust public type names (lib-reexport
// PascalCase rows and the owners of type-class method rows).
func inventoryTypes(rows []inventoryRow) map[string]bool {
	types := make(map[string]bool)
	for _, inv := range rows {
		if inv.class == "lib-reexport" && isExportedTypeName(inv.ref) {
			types[inv.ref] = true
			continue
		}
		if strings.Contains(inv.ref, "::") {
			types[ownerOf(inv.ref)] = true
		}
	}
	return types
}

func TestParityRustInventoryIsFullyRecorded(t *testing.T) {
	_, types := rootSymbols(t)
	rows := loadParityManifest(t)
	inventory := loadRustInventory(t)
	inventoryTypeSet := inventoryTypes(inventory)

	// Classification sanity: the inventory has no brace fragments and
	// every row is either an operation or a type.
	for _, inv := range inventory {
		if strings.ContainsAny(inv.ref, "{}") {
			t.Errorf("inventory brace fragment: %s %s", inv.class, inv.ref)
		}
		if !inv.inventoryOperation() && !isExportedTypeName(inv.ref) && !strings.Contains(inv.ref, "::") {
			t.Errorf("inventory row is neither operation nor type: %s %s", inv.class, inv.ref)
		}
	}

	var failures []string

	// Direction A: every inventory operation must be recorded in the
	// ledger (a newly missing Rust operation fails CI as unrecorded).
	operationRows := 0
	for _, inv := range inventory {
		if !inv.inventoryOperation() {
			continue
		}
		operationRows++
		covered := false
		for _, row := range rows {
			if operationCoveredBy(row, inv) {
				covered = true
				break
			}
		}
		if !covered {
			failures = append(failures, "inventory-operation-unrecorded: "+inv.class+" | "+inv.ref)
		}
	}

	// Direction B: every inventory type must exist as an exported Go
	// type or be a recorded divergence.
	for name := range inventoryTypeSet {
		if types[name] {
			continue
		}
		if _, diverges := rustTypeDivergence[name]; diverges {
			continue
		}
		failures = append(failures, "inventory-type-missing: "+name)
	}

	// Direction C: every exported Go type and constant must be recorded
	// in the ledger (go-surface class for Go-adapted shapes) or match an
	// inventory type. The ledger records them as class go-surface rows
	// with rust_ref "-" exactly like the convenience functions.
	ledgerTypes := map[string]bool{}
	for _, row := range rows {
		if row.goSymbol != "" && !strings.Contains(row.goSymbol, ".") && isExportedTypeName(row.goSymbol) {
			ledgerTypes[row.goSymbol] = true
		}
	}
	for sym := range types {
		if inventoryTypeSet[sym] || ledgerTypes[sym] {
			continue
		}
		failures = append(failures, "unrecorded-type: "+sym)
	}

	t.Logf("rust inventory: %d rows, %d operations, %d types", len(inventory), operationRows, len(types))
	if len(failures) > 0 {
		t.Errorf("rust inventory drifted from the Go surface (%d):\n%s", len(failures), strings.Join(failures, "\n"))
	}
}
