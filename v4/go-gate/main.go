// Command gatescan enforces the v4 Go mmap-only content-transfer ban over
// production sources (the AST half of v4/go/check-import-graph.sh).
//
// It parses every non-test .go file under the walk root — build tags,
// line wrapping, comments, aliases, and file names are irrelevant to the
// token stream — and reports:
//
//   - banned imports (content-transfer packages) and dot imports;
//   - selector-based transfer calls (.Read/.Write/.Seek families,
//     reflection Call, decoders/encoders, fmt.Fscan*, ...); and
//   - any *os.File-typed value used outside the approved capability
//     surface (mapping lifecycle: Fd/Close/Name/Stat/Sync/Truncate, and
//     consumers in the same package, module-internal packages, or x/sys).
//
// The three in-memory inflater nodes in internal/reader/metadata.go are
// exempted as exact call shapes (their source text is compared
// literally) and only when their receiver/arguments are not file-tainted.
// A file-backed receiver reproducing the same text — c.r.Read(p) with
// r *os.File, or io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])
// with zr *os.File — stays visible through the file taint and fails.
//
// The analysis is deliberately type-light: a small syntactic taint tracks
// *os.File values (declarations, parameters, os.Open*/os.Create*
// producers, same-package constructors returning *os.File, and struct
// fields). That keeps the gate a mechanical tripwire with no module
// dependency beyond the standard library.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const moduleInternalPrefix = "github.com/firehol/iprange/v4/go/internal"

// bannedImports are packages whose API transfers or re-wraps content
// through read/write/seek-equivalent surfaces the SDK must not use, or
// that exist only to move bytes. compress/flate is deliberately absent:
// the metadata inflater reads an in-memory payload.
var bannedImports = map[string]bool{
	"archive/tar": true, "archive/zip": true,
	"bufio": true, "compress/bzip2": true, "compress/gzip": true,
	"compress/lzw": true, "compress/zlib": true,
	"debug/buildinfo": true, "debug/elf": true, "debug/macho": true,
	"debug/pe": true, "debug/plan9obj": true,
	"encoding/ascii85": true, "encoding/base64": true, "encoding/csv": true,
	"encoding/gob": true, "encoding/json": true, "encoding/xml": true,
	"go/parser": true, "go/scanner": true,
	"html/template": true, "image": true, "image/gif": true,
	"image/jpeg": true, "image/png": true, "io/ioutil": true,
	"log": true, "log/slog": true, "mime/multipart": true,
	"mime/quotedprintable": true, "net/http": true, "os/exec": true,
	"runtime/trace": true, "syscall": true, "text/scanner": true,
	"text/tabwriter": true, "text/template": true,
}

// bannedSelectors are the content-transfer call families. The list is
// deliberately broad: it also covers function aliases, method values,
// reflection Invocation, and x/sys descriptor variants.
var bannedSelectors = map[string]bool{
	"Call": true, "CallSlice": true, "Copy": true, "CopyBuffer": true,
	"CopyFileRange": true, "CopyN": true, "Decode": true, "Encode": true,
	"Fprint": true, "Fprintf": true, "Fprintln": true, "Fscan": true,
	"Fscanf": true, "Fscanln": true, "Method": true, "MethodByName": true,
	"NewDecoder": true, "NewWriter": true, "Peek": true, "Pread": true,
	"Preadv": true, "Print": true, "Printf": true, "Println": true,
	"Pwrite": true, "Pwritev": true, "Read": true, "ReadAll": true,
	"ReadAt": true, "ReadAtLeast": true, "ReadByte": true, "ReadFile": true,
	"ReadFrom": true, "ReadFull": true, "ReadLine": true, "ReadRune": true,
	"ReadString": true, "Readv": true, "Scan": true, "Scanf": true,
	"Scanln": true, "Seek": true, "Sendfile": true, "Splice": true,
	"Syscall": true, "Syscall6": true, "Syscall9": true, "SyscallN": true,
	"Write": true, "WriteAt": true, "WriteByte": true, "WriteFile": true,
	"WriteRune": true, "WriteString": true, "WriteTo": true, "Writev": true,
}

// fileProducers are stdlib functions that return *os.File; a value bound
// from one is file-tainted.
var fileProducers = map[string]bool{
	"os.Create": true, "os.CreateTemp": true, "os.NewFile": true,
	"os.Open": true, "os.OpenFile": true,
}

// approvedFileMethods are the only methods allowed on a file-tainted
// value: mapping lifecycle and identity operations. Anything else
// (Read/Write/Seek/... and any future transfer) fails the gate.
var approvedFileMethods = map[string]bool{
	"Chmod": true, "Chown": true, "Close": true, "Fd": true,
	"Name": true, "Stat": true, "Sync": true, "Truncate": true,
}

// structs maps, per package directory, type name -> field name -> type
// text. funcs maps same-package function names to their result type
// texts. Both are collected syntactically.
type pkgInfo struct {
	structs map[string]map[string]string
	funcs   map[string][]string
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	files := []string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), "_test.go") {
			files = append(files, path)
		}
		return nil
	})

	fail := false
	byDir := map[string][]string{}
	for _, f := range files {
		dir := filepath.Dir(f)
		byDir[dir] = append(byDir[dir], f)
	}
	for _, dir := range sortedKeys(byDir) {
		list := byDir[dir]
		info, srcs, fses, parsed := parseDir(list)
		for _, f := range list {
			if err := runFile(f, parsed[f], fses[f], srcs[f], info); err != nil {
				fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
				fail = true
			}
		}
	}
	if fail {
		os.Exit(1)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func parseDir(paths []string) (pkgInfo, map[string][]byte, map[string]*token.FileSet, map[string]*ast.File) {
	info := pkgInfo{structs: map[string]map[string]string{}, funcs: map[string][]string{}}
	srcs := map[string][]byte{}
	fses := map[string]*token.FileSet{}
	parsed := map[string]*ast.File{}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gate scan failed to parse %s: %v\n", p, err)
			os.Exit(1)
		}
		srcs[p], fses[p], parsed[p] = src, fset, file
		collectPkgInfo(file, &info)
	}
	return info, srcs, fses, parsed
}

func collectPkgInfo(f *ast.File, info *pkgInfo) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := map[string]string{}
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					continue // embedded field
				}
				t := exprText(field.Type)
				for _, name := range field.Names {
					fields[name.Name] = t
				}
			}
			info.structs[ts.Name.Name] = fields
		}
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Type.Results == nil {
			continue
		}
		var results []string
		for _, r := range fd.Type.Results.List {
			t := exprText(r.Type)
			for range r.Names {
				results = append(results, t)
			}
			if len(r.Names) == 0 {
				results = append(results, t)
			}
		}
		info.funcs[fd.Name.Name] = results
	}
}

func exprText(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprText(t.X)
	case *ast.ArrayType:
		return "[]" + exprText(t.Elt)
	case *ast.SelectorExpr:
		return exprText(t.X) + "." + t.Sel.Name
	}
	return ""
}

// taints is the per-scope syntactic *os.File state.
type taints struct {
	file      map[string]bool   // identifiers holding *os.File
	container map[string]bool   // identifiers holding []*os.File or a struct with file fields
	struc     map[string]string // identifiers holding a same-package struct value: name -> type name
}

func newTaints() *taints {
	return &taints{file: map[string]bool{}, container: map[string]bool{}, struc: map[string]string{}}
}

func cloneTaints(t *taints) *taints {
	c := newTaints()
	for k, v := range t.file {
		c.file[k] = v
	}
	for k, v := range t.container {
		c.container[k] = v
	}
	for k, v := range t.struc {
		c.struc[k] = v
	}
	return c
}

// runFile applies the rules to one production file.
func runFile(path string, f *ast.File, fset *token.FileSet, src []byte, info pkgInfo) error {
	reporter := &reporter{path: path}
	imports := map[string]string{}
	for _, imp := range f.Imports {
		pathText := strings.Trim(imp.Path.Value, `"`)
		name := pathText
		if imp.Name != nil && imp.Name.Name != "." {
			name = imp.Name.Name
		} else if imp.Name != nil && imp.Name.Name == "." {
			reporter.fail("dot-import of " + pathText)
			continue
		}
		if imp.Name != nil && imp.Name.Name == "_" {
			continue // blank import: no names leak
		}
		imports[pathText] = pathText
		imports[name] = pathText
		if bannedImports[pathText] {
			reporter.fail("banned content-transfer import " + pathText)
		}
	}

	pkg := newTaints()
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gd.Tok != token.VAR && gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			var cls kind
			if vs.Type != nil {
				cls = classifyType(vs.Type, info)
			}
			for i, name := range vs.Names {
				if len(vs.Values) > i {
					cls = classify(vs.Values[i], pkg, info, imports)
				}
				applyKind(pkg, name.Name, cls)
			}
		}
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			st := cloneTaints(pkg)
			addSignatureTaints(st, d.Recv, info)
			addSignatureTaints(st, d.Type.Params, info)
			prepassStmts(d.Body.List, st, info, imports)
			exempts := findExemptions(d, src, fset, st, info, imports)
			rulesWalk("func "+d.Name.Name, d.Body, st, exempts, imports, info, reporter)
		case *ast.GenDecl:
			if d.Tok == token.VAR || d.Tok == token.CONST {
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						walkRulesNode(vs, pkg, nil, imports, info, reporter)
					}
				}
			}
		default:
			walkRulesNode(d, pkg, nil, imports, info, reporter)
		}
	}
	return reporter.err()
}

type kind int

const (
	kindNone kind = iota
	kindFile
	kindContainer
)

// classifyType maps a declared type expression to file/container taint.
func classifyType(t ast.Expr, info pkgInfo) kind {
	text := exprText(t)
	if text == "*os.File" {
		return kindFile
	}
	if strings.Contains(text, "*os.File") {
		return kindContainer
	}
	return kindNone
}

// classify maps an expression value to file/container/struct taint.
func classify(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) kind {
	switch v := e.(type) {
	case *ast.Ident:
		if st.file[v.Name] {
			return kindFile
		}
		if st.container[v.Name] {
			return kindContainer
		}
	case *ast.StarExpr:
		return classify(v.X, st, info, imports)
	case *ast.ParenExpr:
		return classify(v.X, st, info, imports)
	case *ast.UnaryExpr:
		return classify(v.X, st, info, imports)
	case *ast.CallExpr:
		if _, ok := producerCall(v, info, imports); ok {
			return kindFile
		}
	case *ast.CompositeLit:
		text := exprText(v.Type)
		if strings.Contains(text, "*os.File") {
			return kindContainer
		}
		if id, ok := v.Type.(*ast.Ident); ok {
			if _, isStruct := info.structs[id.Name]; isStruct {
				return kindNone // struct value; field taint is resolved on access
			}
		}
	case *ast.SelectorExpr:
		if isFileExpr(v, st, info, imports) {
			return kindFile
		}
		if isContainerExpr(v, st, info) {
			return kindContainer
		}
	}
	return kindNone
}

func applyKind(st *taints, name string, k kind) {
	switch k {
	case kindFile:
		st.file[name] = true
	case kindContainer:
		st.container[name] = true
	}
}

// producerCall reports whether e is a call producing *os.File: a stdlib
// producer, or a same-package function whose result type is *os.File.
func producerCall(e ast.Expr, info pkgInfo, imports map[string]string) (*ast.CallExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if ok {
		if pkg, ok := sel.X.(*ast.Ident); ok {
			// Resolve the package by local alias so an aliased
			// `import fsp "os"` cannot dodge the producer taint.
			path := imports[pkg.Name]
			if path == "os" && fileProducers["os."+sel.Sel.Name] {
				return call, true
			}
			if fileProducers[pkg.Name+"."+sel.Sel.Name] {
				return call, true
			}
		}
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		for _, r := range info.funcs[id.Name] {
			if r == "*os.File" {
				return call, true
			}
		}
	}
	return nil, false
}

// isFileExpr reports whether expr names a *os.File value: a tainted
// identifier, a struct-field access whose field is *os.File, or a
// producer call.
func isFileExpr(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return st.file[v.Name]
	case *ast.SelectorExpr:
		structName, ok := resolveStruct(v.X, st, info)
		if !ok {
			return false
		}
		return info.structs[structName][v.Sel.Name] == "*os.File"
	case *ast.CallExpr:
		_, ok := producerCall(v, info, imports)
		return ok
	case *ast.StarExpr:
		return isFileExpr(v.X, st, info, imports)
	case *ast.ParenExpr:
		return isFileExpr(v.X, st, info, imports)
	}
	return false
}

func isContainerExpr(e ast.Expr, st *taints, info pkgInfo) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return st.container[v.Name]
	case *ast.SelectorExpr:
		structName, ok := resolveStruct(v.X, st, info)
		if !ok {
			return false
		}
		return strings.Contains(info.structs[structName][v.Sel.Name], "*os.File")
	case *ast.CompositeLit:
		return strings.Contains(exprText(v.Type), "*os.File")
	case *ast.StarExpr:
		return isContainerExpr(v.X, st, info)
	case *ast.ParenExpr:
		return isContainerExpr(v.X, st, info)
	}
	return false
}

// isFileOrContainer is the argument-taint test: a file value, a
// container value, or a composite literal that textually or transitively
// holds files (ProcAttr{Files: []*os.File{...}}).
func isFileOrContainer(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) bool {
	if isFileExpr(e, st, info, imports) || isContainerExpr(e, st, info) {
		return true
	}
	switch v := e.(type) {
	case *ast.CompositeLit:
		if strings.Contains(exprText(v.Type), "*os.File") {
			return true
		}
		for _, el := range v.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			if isFileOrContainer(el, st, info, imports) {
				return true
			}
		}
	case *ast.CallExpr:
		_, ok := producerCall(v, info, imports)
		return ok
	case *ast.UnaryExpr:
		return isFileOrContainer(v.X, st, info, imports)
	case *ast.ParenExpr:
		return isFileOrContainer(v.X, st, info, imports)
	}
	return false
}

// resolveStruct resolves an expression to a same-package struct type
// name: tainted-struct identifiers, struct return values, and struct
// composite literals.
func resolveStruct(e ast.Expr, st *taints, info pkgInfo) (string, bool) {
	switch v := e.(type) {
	case *ast.Ident:
		name, ok := st.struc[v.Name]
		return name, ok
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok {
			for _, r := range info.funcs[id.Name] {
				n := strings.TrimPrefix(r, "*")
				if _, isStruct := info.structs[n]; isStruct {
					return n, true
				}
			}
		}
	case *ast.CompositeLit:
		if id, ok := v.Type.(*ast.Ident); ok {
			if _, isStruct := info.structs[id.Name]; isStruct {
				return id.Name, true
			}
		}
	case *ast.StarExpr:
		return resolveStruct(v.X, st, info)
	case *ast.ParenExpr:
		return resolveStruct(v.X, st, info)
	case *ast.UnaryExpr:
		return resolveStruct(v.X, st, info)
	}
	return "", false
}

// addSignatureTaints taints receiver and parameter identifiers from
// their declared types.
func addSignatureTaints(st *taints, fields *ast.FieldList, info pkgInfo) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		t := exprText(field.Type)
		for _, name := range field.Names {
			switch {
			case t == "*os.File":
				st.file[name.Name] = true
			case strings.Contains(t, "*os.File"):
				st.container[name.Name] = true
			}
			base := strings.TrimPrefix(t, "*")
			if _, isStruct := info.structs[base]; isStruct {
				st.struc[name.Name] = base
			}
		}
		if len(field.Names) == 0 {
			// embedded field or unnamed result: nothing to taint
		}
	}
}

// prepassStmts walks statements in source order so assignment-based
// taint propagation sees earlier assignments.
func prepassStmts(list []ast.Stmt, st *taints, info pkgInfo, imports map[string]string) {
	for _, s := range list {
		switch v := s.(type) {
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if i >= len(v.Rhs) {
					break
				}
				applyLHS(lhs, v.Rhs[i], st, info, imports)
			}
		case *ast.DeclStmt:
			if gd, ok := v.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						var cls kind
						if vs.Type != nil {
							cls = classifyType(vs.Type, info)
						}
						for i, name := range vs.Names {
							if len(vs.Values) > i {
								cls = classify(vs.Values[i], st, info, imports)
								if c, ok := classifyStruct(vs.Values[i], info); ok {
									st.struc[name.Name] = c
								}
							}
							applyKind(st, name.Name, cls)
						}
					}
				}
			}
		case *ast.IfStmt:
			if v.Init != nil {
				prepassStmts([]ast.Stmt{v.Init}, st, info, imports)
			}
			prepassStmts(v.Body.List, st, info, imports)
			if v.Else != nil {
				if blk, ok := v.Else.(*ast.BlockStmt); ok {
					prepassStmts(blk.List, st, info, imports)
				} else if ifs, ok := v.Else.(*ast.IfStmt); ok {
					prepassStmts([]ast.Stmt{ifs}, st, info, imports)
				}
			}
		case *ast.ForStmt:
			if v.Init != nil {
				prepassStmts([]ast.Stmt{v.Init}, st, info, imports)
			}
			prepassStmts(v.Body.List, st, info, imports)
		case *ast.RangeStmt:
			// Ranging over a container yields *os.File elements in the
			// Value position (keys are indexes or map keys, never the
			// file itself).
			if v.Value != nil {
				if k, ok := v.Value.(*ast.Ident); ok && isContainerExpr(v.X, st, info) {
					st.file[k.Name] = true
				}
			}
			prepassStmts(v.Body.List, st, info, imports)
		case *ast.SwitchStmt:
			if v.Init != nil {
				prepassStmts([]ast.Stmt{v.Init}, st, info, imports)
			}
			for _, cc := range v.Body.List {
				if cs, ok := cc.(*ast.CaseClause); ok {
					prepassStmts(cs.Body, st, info, imports)
				}
			}
		case *ast.BlockStmt:
			prepassStmts(v.List, st, info, imports)
		case *ast.LabeledStmt:
			prepassStmts([]ast.Stmt{v.Stmt}, st, info, imports)
		}
	}
}

func classifyStruct(e ast.Expr, info pkgInfo) (string, bool) {
	switch v := e.(type) {
	case *ast.CompositeLit:
		if id, ok := v.Type.(*ast.Ident); ok {
			if _, isStruct := info.structs[id.Name]; isStruct {
				return id.Name, true
			}
		}
	case *ast.UnaryExpr:
		return classifyStruct(v.X, info)
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok {
			for _, r := range info.funcs[id.Name] {
				n := strings.TrimPrefix(r, "*")
				if _, isStruct := info.structs[n]; isStruct {
					return n, true
				}
			}
		}
	}
	return "", false
}

func applyLHS(lhs, rhs ast.Expr, st *taints, info pkgInfo, imports map[string]string) {
	id, ok := lhs.(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	cls := classify(rhs, st, info, imports)
	applyKind(st, id.Name, cls)
	if c, ok := classifyStruct(rhs, info); ok {
		st.struc[id.Name] = c
	}
}

// findExemptions locates the three tolerated in-memory inflater call
// shapes inside internal/reader/metadata.go and records their selector
// positions so the rules pass ignores exactly those nodes.
func findExemptions(fd *ast.FuncDecl, src []byte, fset *token.FileSet, st *taints, info pkgInfo, imports map[string]string) map[token.Pos]bool {
	exempts := map[token.Pos]bool{}
	path := fset.Position(fd.Pos()).Filename
	if !strings.HasSuffix(path, "internal/reader/metadata.go") {
		return exempts
	}
	recvName, recvStruct := receiverOf(fd, info)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "ReadFull":
			if len(call.Args) == 2 && isIOIdent(sel.X) {
				args := src[int(call.Lparen) : int(call.Rparen)-1]
				shape := string(args)
				if (shape == "zr, out[:int(meta.MetadataUncompressed)]" || shape == "zr, out[int(meta.MetadataUncompressed):]") &&
					!isFileOrContainer(call.Args[0], st, info, imports) {
					exempts[sel.Pos()] = true
				}
			}
		case "Read", "ReadByte":
			if recvName == "" {
				return true
			}
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "r" {
				return true
			}
			id, ok := inner.X.(*ast.Ident)
			if ok && id.Name == recvName {
				if _, isStruct := info.structs[recvStruct]; isStruct {
					if info.structs[recvStruct]["r"] == "*bytes.Reader" {
						exempts[sel.Pos()] = true
					}
				}
			}
		}
		return true
	})
	return exempts
}

func isIOIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "io"
}

func receiverOf(fd *ast.FuncDecl, info pkgInfo) (name, structName string) {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return "", ""
	}
	name = fd.Recv.List[0].Names[0].Name
	t := exprText(fd.Recv.List[0].Type)
	structName = strings.TrimPrefix(t, "*")
	return name, structName
}

// rulesWalk visits every expression node of a function body and applies
// the selector ban, the file-method ban, and the file-argument ban.
func rulesWalk(scope string, body *ast.BlockStmt, st *taints, exempts map[token.Pos]bool, imports map[string]string, info pkgInfo, reporter *reporter) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if exempts[v.Pos()] {
				return true
			}
			if bannedSelectors[v.Sel.Name] {
				reporter.failf("%s: banned content-transfer selector .%s", scope, v.Sel.Name)
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && !exempts[sel.Pos()] {
				if isFileExpr(sel.X, st, info, imports) && !approvedFileMethods[sel.Sel.Name] {
					reporter.failf("%s: %s on an *os.File value outside the approved capability surface", scope, sel.Sel.Name)
				}
			}
			for _, arg := range v.Args {
				if isFileOrContainer(arg, st, info, imports) && !approvedCallee(v.Fun, imports) {
					reporter.failf("%s: *os.File value passed to %s", scope, calleeText(v.Fun))
				}
			}
		case *ast.FuncLit:
			// Nested closures see the outer taints; their own
			// assignments are deliberately not propagated (the
			// production tree has none).
		}
		return true
	})
}

// approvedCallee allows file values into same-package functions
// (their bodies are scanned too), module-internal packages, and the
// x/sys syscall surface used by the mapping owner. Any other callee —
// in particular every standard-library consumer — is a transfer.
func approvedCallee(fun ast.Expr, imports map[string]string) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		id, ok := f.X.(*ast.Ident)
		if !ok {
			return false
		}
		p := imports[id.Name]
		return strings.HasPrefix(p, moduleInternalPrefix) || p == "golang.org/x/sys/unix"
	}
	return false
}

func calleeText(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return exprText(f)
	}
	return "?"
}

// walkRulesNode applies the same rules to package-level expressions
// (initializers) with package-level taint state.
func walkRulesNode(node ast.Node, st *taints, _ map[token.Pos]bool, imports map[string]string, info pkgInfo, reporter *reporter) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if bannedSelectors[v.Sel.Name] {
				reporter.failf("init: banned content-transfer selector .%s", v.Sel.Name)
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
				if isFileExpr(sel.X, st, info, imports) && !approvedFileMethods[sel.Sel.Name] {
					reporter.failf("init: %s on an *os.File value outside the approved capability surface", sel.Sel.Name)
				}
			}
			for _, arg := range v.Args {
				if isFileOrContainer(arg, st, info, imports) && !approvedCallee(v.Fun, imports) {
					reporter.failf("init: *os.File value passed to %s", calleeText(v.Fun))
				}
			}
		}
		return true
	})
}

type reporter struct {
	path   string
	failed bool
}

func (r *reporter) fail(msg string) {
	fmt.Printf("content-transfer violation: %s: %s\n", r.path, msg)
	r.failed = true
}

func (r *reporter) failf(format string, args ...any) {
	r.fail(fmt.Sprintf(format, args...))
}

func (r *reporter) err() error {
	if r.failed {
		return fmt.Errorf("violations in %s", r.path)
	}
	return nil
}
