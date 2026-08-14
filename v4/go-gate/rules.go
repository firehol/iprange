package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/token"
	"go/types"
	"strings"
)

const moduleInternalPrefix = "github.com/firehol/iprange/v4/go/internal"

// xsysImport is the only permitted external syscall surface.
const xsysImport = "golang.org/x/sys/unix"

// bannedImports are packages whose API transfers or re-wraps content
// through read/write/seek-equivalent surfaces the SDK must not use, or
// that exist only to move bytes. compress/flate is deliberately absent:
// the metadata inflater reads an in-memory payload.
var bannedImports = map[string]bool{
	"C":           true, // cgo: C.pread etc. would bypass every Go selector ban
	"unsafe":      true, // unsafe.Slice over a mapped descriptor would mint page views the type layer cannot trace
	"reflect":     true, // reflect.Value.Bytes/Slice/String hand out the underlying mapped bytes without the selector layer seeing them
	"archive/tar": true, "archive/zip": true,
	"bufio": true, "compress/bzip2": true, "compress/gzip": true,
	"compress/lzw": true, "compress/zlib": true,
	"debug/buildinfo": true, "debug/elf": true, "debug/macho": true,
	"debug/pe": true, "debug/plan9obj": true,
	"embed":            true, // compile-time file copy violates the mmap-only contract
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
// reflection Invocation, x/sys descriptor variants, and the subprocess
// escape (dup the descriptor onto stdin, then exec a reader).
var bannedSelectors = map[string]bool{
	"Call": true, "CallSlice": true, "Clonefile": true, "Clonefileat": true,
	"Copy": true, "CopyBuffer": true, "CopyFS": true,
	"CopyFileRange": true, "CopyN": true, "Decode": true, "Dup": true, "Dup2": true, "Dup3": true,
	"Encode": true, "Exec": true, "FcntlInt": true, "ForkExec": true,
	"IoctlFileClone": true, "IoctlFileCloneRange": true, "IoctlFileDedupeRange": true,
	"Tee": true, "Vmsplice": true,
	"Fprint": true, "Fprintf": true, "Fprintln": true, "Fscan": true,
	"Fscanf": true, "Fscanln": true, "Method": true, "MethodByName": true,
	"NewDecoder": true, "NewWriter": true, "Peek": true, "Pread": true,
	"Preadv": true, "Print": true, "Printf": true, "Println": true,
	"Preadv2": true, "Pwrite": true, "Pwritev": true, "Pwritev2": true,
	"RawSyscall": true, "RawSyscall6": true, "RawSyscall9": true,
	"RawSyscallN": true, "RawSyscallNoError": true, "Read": true,
	"ReadAll": true,
	"ReadAt":  true, "ReadAtLeast": true, "ReadByte": true, "ReadFile": true,
	"ReadFrom": true, "ReadFull": true, "ReadLine": true, "ReadRune": true,
	"ReadString": true, "Readv": true, "Scan": true, "Scanf": true,
	"Scanln": true, "Seek": true, "Sendfile": true, "Splice": true,
	"StartProcess": true,
	"Syscall":      true, "Syscall6": true, "Syscall9": true, "SyscallN": true,
	"SyscallNoError": true,
	"Write":          true, "WriteAt": true, "WriteByte": true, "WriteFile": true,
	"WriteRune": true, "WriteString": true, "WriteTo": true, "Writev": true,
}

// approvedFileMethods are the lifecycle and identity methods a live
// descriptor may call directly; everything else on a *os.File/*os.Root
// value is a capability violation (the content-transfer families are
// banned separately by name).
var approvedFileMethods = map[string]bool{
	"Chmod": true, "Chown": true, "Close": true, "Fd": true,
	"Name": true, "Stat": true, "Sync": true, "Truncate": true,
}

// inMemoryReaders are concrete standard-library byte containers: a value
// whose static type is one of these can never hide a file, so their
// read/seek selectors are not content transfer.
var inMemoryReaders = map[string]bool{
	"bytes.Reader": true, "bytes.Buffer": true, "strings.Reader": true,
}

// PageSize mirrors format.PageSize; the gate pins the module's own
// constant by value so a format change cannot silently weaken the rule.
const pageSize = 4096

// MaxMetadataChunkLen mirrors format.MaxMetadataChunkLen: the page-bound
// codec caps a metadata chunk at PageSize-48, so an append of a decoded
// chunk is never a complete-page copy. Pinned by value with the same
// drift warning as pageSize.
const maxMetadataChunkLen = 4048

// Format record caps mirrored from the format package by value: feed-name
// records cap at 255 bytes (uint8 length plus grammar), blob leaf data at
// 4048, inline membership bitmaps at PageSize-96, and structure payloads at
// the fixed 32. Every decoded record byte field is grammar-bounded below a
// complete page; the pins keep record flows legal at copy/append sinks.
const (
	maxFeedNameLen           = 255
	maxBlobLeafDataLen       = 4048
	maxInlineBitmapLen       = 4000 // PageSize - 32 - membershipLeafFixed(64)
	networkEnrichPayloadSize = 32
)

func buildContext(cfg osConfig) *build.Context {
	ctx := build.Default
	ctx.GOOS = cfg.GOOS
	ctx.GOARCH = cfg.GOARCH
	ctx.CgoEnabled = false
	return &ctx
}

// packageCheck is the typed result of one package under one OS config.
type packageCheck struct {
	pkg      *types.Package
	info     *types.Info
	fset     *token.FileSet
	loader   *loader
	files    []*parsedFile
	pf       *pageFlow
	varInits map[*types.Var]ast.Expr
	// pkgBindings records the package-scope initializer expression of
	// every variable, keyed by the variable's object. The per-function
	// statement state seeds its local binding map from it, so a
	// package-level method value (var get = holder.Get) keeps its
	// receiver when called from any function.
	pkgBindings     map[*types.Var]ast.Expr
	reassignedVars  map[*types.Var]bool
	pkgFuncLitBound map[*types.Var]bool
	// pkgFuncNonLitBound records package-scope function variables that
	// receive a non-literal value anywhere: a reassignment to an
	// unscanned callee, an address-taken store, or a loop rebind makes
	// the variable's runtime value unknowable, so the func-literal
	// exemption below no longer applies.
	pkgFuncNonLitBound map[*types.Var]bool
}

// typesChecker type-checks one package's parsed files with the loader.
type typesChecker struct {
	loader *loader
	fset   *token.FileSet
}

func (tc *typesChecker) check(path string, files []*parsedFile) (*packageCheck, error) {
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Implicits:  map[ast.Node]types.Object{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
	asts := make([]*ast.File, 0, len(files))
	for _, f := range files {
		asts = append(asts, f.ast)
	}
	conf := types.Config{Importer: tc.loader}
	pkg, err := conf.Check(path, tc.fset, asts, info)
	if err != nil {
		return nil, err
	}
	// Package-level variable initializers keyed by their resolved object.
	// Function-typed variables are approved call targets only when their
	// initializer provably binds a function whose body is scanned here,
	// and only when the variable is never reassigned.
	varInits := map[*types.Var]ast.Expr{}
	pkgBindings := map[*types.Var]ast.Expr{}
	// pkgFuncLitBound records package-scope function variables that are
	// bound to a func literal anywhere in the package (declaration
	// initializer or assignment). Such a variable's possible values all
	// come from scanned code: the literal bodies are visited by the
	// rules walker and independently policed (an os.Open inside them is
	// caught at the assignment site), so the fail-closed
	// interface-result rule must not double-flag calls through them.
	// Variables with no literal binding anywhere are genuinely opaque
	// and stay fail-closed; variables that ever receive a non-literal
	// value are equally opaque and stay fail-closed too.
	pkgFuncLitBound := map[*types.Var]bool{}
	pkgFuncNonLitBound := map[*types.Var]bool{}
	reassigned := map[*types.Var]bool{}
	for _, f := range asts {
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					break
				}
				obj, ok := info.Defs[name].(*types.Var)
				if ok && obj.Parent() == pkg.Scope() {
					varInits[obj] = vs.Values[i]
					pkgBindings[obj] = vs.Values[i]
					if _, isLit := unparen(vs.Values[i]).(*ast.FuncLit); isLit {
						pkgFuncLitBound[obj] = true
					} else {
						pkgFuncNonLitBound[obj] = true
					}
				}
			}
			return true
		})
	}
	for _, f := range asts {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range v.Lhs {
					if id, ok := unparen(lhs).(*ast.Ident); ok {
						if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
							reassigned[obj] = true
							if i < len(v.Rhs) {
								if _, isLit := unparen(v.Rhs[i]).(*ast.FuncLit); isLit {
									pkgFuncLitBound[obj] = true
								} else {
									pkgFuncNonLitBound[obj] = true
								}
							} else {
								pkgFuncNonLitBound[obj] = true
							}
						}
					}
				}
			case *ast.IncDecStmt:
				if id, ok := unparen(v.X).(*ast.Ident); ok {
					if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
						reassigned[obj] = true
						pkgFuncNonLitBound[obj] = true
					}
				}
			case *ast.RangeStmt:
				// for x = range ... and for _, x = range ... both rebind
				// the loop variables; neither is an AssignStmt, so count
				// them as reassignments when they name package-level vars.
				for _, e := range []ast.Expr{v.Key, v.Value} {
					if id, ok := unparen(e).(*ast.Ident); ok {
						if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
							reassigned[obj] = true
							pkgFuncNonLitBound[obj] = true
						}
					}
				}
			case *ast.UnaryExpr:
				// Taking a package-level variable's address permits a
				// store through that pointer (p := &f; *p = bytes.Clone),
				// so the initializer is no longer proof of the callee.
				if v.Op == token.AND {
					if id, ok := unparen(v.X).(*ast.Ident); ok {
						if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
							reassigned[obj] = true
							pkgFuncNonLitBound[obj] = true
						}
					}
				}
			}
			return true
		})
	}
	return &packageCheck{pkg: pkg, info: info, fset: tc.fset, loader: tc.loader, files: files, pf: nil, varInits: varInits, pkgBindings: pkgBindings, reassignedVars: reassigned, pkgFuncLitBound: pkgFuncLitBound, pkgFuncNonLitBound: pkgFuncNonLitBound}, nil
}

// fileRules carries one file's rule pass.
type fileRules struct {
	rep         *reporter
	pc          *packageCheck
	imports     map[string]string
	path        string
	exempts     map[token.Pos]bool
	pkgFuncVars map[types.Object]bool
}

// runRules applies every rule family to one file of one package.
func runRules(rep *reporter, f *ast.File, pc *packageCheck, path string) {
	w := &fileRules{rep: rep, pc: pc, imports: checkImports(rep, f), path: path}
	w.exempts = findExemptions(w, f, path)
	w.pkgFuncVars = collectPkgFuncVars(w.pc.info, w.pc.files)
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body == nil {
			rep.fail(fd.Pos(), "bodyless function declaration %s (assembly stub)", fd.Name.Name)
		}
	}
	checkDirectives(rep, f)
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			// Each ReturnStmt is checked against the result context of
			// its own enclosing function literal: a nested literal's
			// context must not leak into the enclosing body's returns
			// (nor the enclosing context into the literal's).
			retCtx := map[ast.Node][]types.Type{}
			mapReturnCtxs(w, d.Body, resultTypes(w, d.Type), retCtx)
			ast.Inspect(d.Body, func(n ast.Node) bool {
				if ret, ok := n.(*ast.ReturnStmt); ok {
					w.checkReturnCtx(ret, retCtx[ret])
					return true
				}
				return w.visit(n)
			})
		default:
			ast.Inspect(d, w.visit)
		}
	}
}

// mapReturnCtxs records, for every ReturnStmt in n, the result types of
// the function (literal or declaration) that directly contains it.
func mapReturnCtxs(w *fileRules, n ast.Node, ctx []types.Type, out map[ast.Node][]types.Type) {
	switch v := n.(type) {
	case nil:
		return
	case *ast.ReturnStmt:
		out[v] = ctx
	case *ast.FuncLit:
		// Only the literal's body runs in the literal's own context; the
		// enclosing context resumes for the literal's siblings.
		mapReturnCtxs(w, v.Body, resultTypes(w, v.Type), out)
	default:
		ast.Inspect(n, func(c ast.Node) bool {
			if c == nil || c == n {
				return true // descend into the direct children only
			}
			mapReturnCtxs(w, c, ctx, out)
			return false
		})
	}
}

// resultTypes returns the result types of a function type.
func resultTypes(w *fileRules, ft *ast.FuncType) []types.Type {
	var out []types.Type
	if ft == nil || ft.Results == nil {
		return out
	}
	for _, r := range ft.Results.List {
		t := w.typeOf(r.Type)
		for range r.Names {
			out = append(out, t)
		}
		if len(r.Names) == 0 {
			out = append(out, t)
		}
	}
	return out
}

// checkReturnCtx flags returns that launder file-bearing values into
// non-bearing result slots, and page views converted into owned arrays
// or strings at or above a complete page.
func (w *fileRules) checkReturnCtx(v *ast.ReturnStmt, results []types.Type) {
	for i, rv := range v.Results {
		var dst types.Type
		if i < len(results) {
			dst = results[i]
		}
		w.checkLaunderValue(rv.Pos(), rv, dst)
		if pv := w.pageValue(rv); pv.tainted && pageFull(pv) {
			w.checkArrayConversionSink(rv.Pos(), dst, pv)
		}
	}
}

// checkImports reports banned content-transfer imports and dot imports.
func checkImports(rep *reporter, f *ast.File) map[string]string {
	imports := map[string]string{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := path
		if imp.Name != nil && imp.Name.Name != "." {
			name = imp.Name.Name
		} else if imp.Name != nil && imp.Name.Name == "." {
			rep.fail(imp.Pos(), "dot-import of %s", path)
			continue
		}
		if imp.Name != nil && imp.Name.Name == "_" {
			continue // blank import: no names leak
		}
		imports[path] = path
		imports[name] = path
		if bannedImports[path] {
			rep.fail(imp.Pos(), "banned content-transfer import %s", path)
		}
	}
	return imports
}

// checkDirectives rejects //go:linkname and //go:embed directives: both
// attach or copy bytes the type layer cannot see (embed is also a banned
// import; the directive form is the textual escape).
func checkDirectives(rep *reporter, f *ast.File) {
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:linkname") {
				rep.fail(c.Pos(), "//go:linkname directive (syscall body bypass)")
			}
			if strings.HasPrefix(c.Text, "//go:embed") {
				rep.fail(c.Pos(), "//go:embed directive (compile-time file copy)")
			}
		}
	}
}

// fileValueType reports whether a VALUE whose static type is t can hold a
// live descriptor itself: *os.File/*os.Root (including type aliases and
// defined types over them), containers of them (slice/array/map/chan),
// func values whose results produce them, and multi-result tuples.
//
// A struct that merely CONTAINS a file-typed FIELD is deliberately not a
// file-typed value: the descriptor is only reachable through the field,
// and the capability rules see the field access separately. This keeps
// the mapping owner (Mapping{file, data, size}) and the reader
// (ImmutableReader{m *mapping.Mapping}) usable without laundering their
// scalar fields.
func fileValueType(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || t == types.Typ[types.Invalid] {
		return false
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.(type) {
	case *types.Pointer:
		return fileValueType(u.Elem(), seen)
	case *types.Alias:
		// type X = *os.File (and alias chains, including aliases of
		// imported aliases) resolve to the descriptor underneath; without
		// this case every alias-spelled file type is invisible.
		return fileValueType(types.Unalias(u), seen)
	case *types.Named:
		if isFileNamed(u) {
			return true
		}
		return fileValueType(u.Underlying(), seen)
	case *types.Slice:
		return fileValueType(u.Elem(), seen)
	case *types.Array:
		return fileValueType(u.Elem(), seen)
	case *types.Chan:
		return fileValueType(u.Elem(), seen)
	case *types.Map:
		return fileValueType(u.Key(), seen) || fileValueType(u.Elem(), seen)
	case *types.Signature:
		// A func value that merely ACCEPTS a file does not hold a
		// descriptor; one whose results produce files does.
		res := u.Results()
		for i := 0; i < res.Len(); i++ {
			if fileValueType(res.At(i).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Tuple:
		for i := 0; i < u.Len(); i++ {
			if fileValueType(u.At(i).Type(), seen) {
				return true
			}
		}
		return false
	}
	return false
}

// structContainsFile reports whether a VALUE of type t holds a
// file-bearing field somewhere inside a struct: fileValueType
// deliberately excludes structs (owner types stay usable), but when the
// struct crosses into an interface slot its typed fields disappear from
// the capability walk, so the crossing itself is the launder.
func structContainsFile(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || t == types.Typ[types.Invalid] {
		return false
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch u := types.Unalias(t).(type) {
	case *types.Pointer:
		return structContainsFile(u.Elem(), seen)
	case *types.Alias:
		return structContainsFile(types.Unalias(u), seen)
	case *types.Named:
		return structContainsFile(u.Underlying(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			ft := u.Field(i).Type()
			if fileValueType(ft, map[types.Type]bool{}) || structContainsFile(ft, seen) {
				return true
			}
		}
		return false
	case *types.Slice:
		return structContainsFile(u.Elem(), seen)
	case *types.Array:
		return structContainsFile(u.Elem(), seen)
	case *types.Chan:
		return structContainsFile(u.Elem(), seen)
	case *types.Map:
		return structContainsFile(u.Key(), seen) || structContainsFile(u.Elem(), seen)
	}
	return false
}

// isInterfaceType reports whether t is an interface type, including a
// named interface (io.Reader is *types.Named over an interface; the type
// erasure rule must see both spellings).
func isInterfaceType(t types.Type) bool {
	switch u := types.Unalias(t).(type) {
	case *types.Interface:
		return true
	case *types.Named:
		_, ok := u.Underlying().(*types.Interface)
		return ok
	}
	return false
}

func isFileNamed(n *types.Named) bool {
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "os" {
		return false
	}
	return obj.Name() == "File" || obj.Name() == "Root"
}

func (w *fileRules) typeOf(e ast.Expr) types.Type {
	tv, ok := w.pc.info.Types[e]
	if !ok {
		return nil
	}
	return tv.Type
}

// approvedCallee allows file- and page-bearing values into same-package
// functions (their bodies are scanned too), module-internal packages, and
// the x/sys syscall surface used by the mapping owner. Any other callee —
// in particular every standard-library consumer — is a transfer.
func (w *fileRules) approvedCallee(fun ast.Expr) bool {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		obj := w.pc.info.Uses[f]
		switch o := obj.(type) {
		case *types.Func:
			if pkg := o.Pkg(); pkg != nil {
				return pkg.Path() == w.pc.pkg.Path()
			}
			return false
		case *types.Builtin:
			// Builtins do not move a mapped view into owned memory by
			// themselves; the copy/append/conversion page sinks are
			// checked separately.
			return true
		case *types.Var:
			// A function-typed variable can hold a callee whose body is
			// not part of the scanned source (a stdlib function, a bound
			// method value, a func parameter). Approving it would let a
			// full mapped page or a file descriptor flow into owned
			// memory through an indirection the source scan cannot
			// follow, so such calls are transfers unless the variable
			// provably binds a scanned function.
			return w.approvedFuncVar(o, 0)
		}
		return false
	case *ast.SelectorExpr:
		obj := w.pc.info.Uses[f.Sel]
		fn, ok := obj.(*types.Func)
		if !ok {
			return false
		}
		if pkg := fn.Pkg(); pkg != nil {
			p := pkg.Path()
			if p == w.pc.pkg.Path() || strings.HasPrefix(p, moduleInternalPrefix) || p == xsysImport {
				if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
					return false
				}
				return true
			}
		}
	}
	return false
}

func (w *fileRules) visit(n ast.Node) bool {
	switch v := n.(type) {
	case *ast.SelectorExpr:
		w.checkSelector(v)
	case *ast.CallExpr:
		w.checkCall(v)
	case *ast.AssignStmt:
		w.checkAssign(v)
	case *ast.SendStmt:
		w.checkSend(v)
	case *ast.TypeAssertExpr:
		w.checkTypeAssert(v)
	case *ast.CompositeLit:
		w.checkComposite(v)
	case *ast.ValueSpec:
		for i, val := range v.Values {
			if i < len(v.Names) {
				w.checkLaunderValue(v.Pos(), val, w.typeOf(v.Names[i]))
			}
		}
	case *ast.RangeStmt:
		w.checkRange(v)
	}
	return true
}

// checkRange flags range statements whose key/value targets are
// interface-typed variables: a file-bearing element bound into an any
// slot erases the descriptor from the capability walk (var x any;
// for _, x = range files { return x }).
func (w *fileRules) checkRange(v *ast.RangeStmt) {
	xt := w.typeOf(v.X)
	if xt == nil {
		return
	}
	keyT := collectionKeyType(xt)
	valT := collectionElementType(xt)
	for _, tgt := range []struct {
		expr ast.Expr
		slot types.Type
	}{{v.Key, keyT}, {v.Value, valT}} {
		if tgt.expr == nil || tgt.slot == nil {
			continue
		}
		tt := w.typeOf(tgt.expr)
		if tt == nil {
			continue
		}
		bears := fileValueType(tgt.slot, map[types.Type]bool{})
		holds := !bears && structContainsFile(tgt.slot, map[types.Type]bool{})
		if !bears && !holds {
			continue
		}
		// The target must keep the descriptor visible: an
		// interface-typed variable erases it.
		if isInterfaceType(tt) && !fileValueType(tt, map[types.Type]bool{}) {
			w.fail(v.Pos(), "file-bearing range value bound into an interface variable (launder)")
		}
	}
}

// fmtCallee reports whether fun names a formatting function in the fmt
// package (the diagnostics spread exemption in checkCall).
func (w *fileRules) fmtCallee(fun ast.Expr) bool {
	sel, ok := unparen(fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := w.pc.info.Uses[sel.Sel].(*types.Func)
	if !ok {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == "fmt"
}

// checkStringParamCalls flags module-function calls that pass a full
// mapped view to a parameter the callee converts into an owned string
// (return string(p) recorded as stringParams in the callee summary): the
// owned copy happens inside the scanned helper, where the parameter
// bound is unknowable, so the call site must fail closed.
func (w *fileRules) checkStringParamCalls(v *ast.CallExpr, fn *types.Func) {
	if w.pc.pf == nil || w.pc.pf.store == nil || fn.Pkg() == nil {
		return
	}
	sums := w.pc.pf.summaries
	pkgPath := fn.Pkg().Path()
	if pkgPath != w.pc.pkg.Path() {
		sums = w.pc.pf.store.pkgs[pkgPath]
	}
	if sums == nil {
		return
	}
	key := fn.Name()
	receiverSlot := false
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
		receiverSlot = true
	}
	fs, ok := sums[key]
	if !ok || (len(fs.stringParams) == 0 && len(fs.fmtSpreadParams) == 0) {
		return
	}
	// Method summaries count the receiver as parameter slot 0. A method
	// VALUE call carries the receiver as the selector expression; a
	// method-EXPRESSION call (or an alias of one) carries it as the
	// first explicit argument.
	recvOffset := 0
	var recvExpr ast.Expr
	if sel, ok := unparen(v.Fun).(*ast.SelectorExpr); ok {
		if selRecv, isSel := w.pc.info.Selections[sel]; isSel && selRecv.Kind() == types.MethodVal {
			recvExpr = sel.X
			recvOffset = 1
		}
	}
	argAt := func(slot int) (ast.Expr, bool) {
		if receiverSlot && slot == 0 && recvExpr != nil {
			return recvExpr, true
		}
		ai := slot - recvOffset
		if ai < 0 || ai >= len(v.Args) {
			return nil, false
		}
		return v.Args[ai], true
	}
	for pi := range fs.stringParams {
		arg, ok := argAt(pi)
		if !ok {
			continue
		}
		if pv := w.pageValue(arg); pv.tainted && pageFull(pv) {
			w.fail(v.Pos(), "mapped page view passed to %s: parameter %d is converted to an owned string inside the callee", calleeText(v.Fun), pi+1)
		}
	}
	for pi := range fs.fmtSpreadParams {
		arg, ok := argAt(pi)
		if !ok {
			continue
		}
		if pv := w.pageValue(arg); pv.tainted && pageFull(pv) {
			w.fail(v.Pos(), "mapped page view passed to %s: parameter %d is spread into fmt inside the callee (complete page into owned memory)", calleeText(v.Fun), pi+1)
		}
	}
}

// approvedFuncVar approves a call through a function-typed variable only
// when the variable's package-level initializer provably names a function
// whose body is scanned in this tree: a func literal, a direct reference
// to an approved function, or a bounded chain of variable references
// ending in one. Local variables, parameters, and values bound to
// unlisted callees (stdlib functions, method values) are never approved.
func (w *fileRules) approvedFuncVar(v *types.Var, depth int) bool {
	if v == nil || depth > 2 {
		return false
	}
	// A variable that is reassigned anywhere may hold a callee whose body
	// is not scanned (e.g. a later assignment of bytes.Clone), so only
	// never-reassigned variables keep their initializer as proof.
	if w.pc.reassignedVars[v] {
		return false
	}
	init, ok := w.pc.varInits[v]
	if !ok {
		return false
	}
	switch i := unparen(init).(type) {
	case *ast.FuncLit:
		return true
	case *ast.Ident:
		obj := w.pc.info.Uses[i]
		if fn, ok := obj.(*types.Func); ok {
			return w.approvedFuncPkg(fn)
		}
		if ov, ok := obj.(*types.Var); ok {
			return w.approvedFuncVar(ov, depth+1)
		}
		return false
	case *ast.SelectorExpr:
		fn, ok := w.pc.info.Uses[i.Sel].(*types.Func)
		if !ok {
			return false
		}
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
			// An interface method expression has no scanned body: the
			// implementation dispatches dynamically, so a call through
			// the alias is an unproven indirection no matter how the
			// alias itself is spelled.
			return false
		}
		return w.approvedFuncPkg(fn)
	}
	return false
}

// isMappingOwnerPath reports whether pkgPath is the mapping owner, the
// only package allowed to create and destroy live descriptors.
func isMappingOwnerPath(pkgPath string) bool {
	return strings.HasSuffix(pkgPath, "/internal/mapping")
}

// approvedFuncPkg applies the callee package policy: the current package,
// module-internal packages, and the pinned x/sys syscall surface.
func (w *fileRules) approvedFuncPkg(fn *types.Func) bool {
	if pkg := fn.Pkg(); pkg != nil {
		p := pkg.Path()
		return p == w.pc.pkg.Path() || strings.HasPrefix(p, moduleInternalPrefix) || p == xsysImport
	}
	return false
}

// methodReceiverBearsFile reports whether the resolved method's DECLARED
// receiver is a file-typed value. Using the declared receiver (not the
// selector's static receiver) makes promoted methods from embedded
// *os.File/*os.Root visible: (*Wrapper).Open resolves to (*os.Root).Open,
// whose receiver *os.Root is file-typed, so the capability surface holds.
func methodReceiverBearsFile(sel *types.Selection) bool {
	if sel == nil {
		return false
	}
	obj := sel.Obj()
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	return fileValueType(sig.Recv().Type(), map[types.Type]bool{})
}

// checkSelector enforces the banned-selector ban and the receiver side of
// the file capability surface. Field selections never transfer content and
// are always allowed; method selections are restricted when the method's
// declared receiver is file-typed (a live descriptor), with only the
// approved lifecycle methods and the banned-selector set applying.
func (w *fileRules) checkSelector(v *ast.SelectorExpr) {
	if w.exempts[v.Pos()] {
		return
	}
	if bannedSelectors[v.Sel.Name] {
		w.fail(v.Pos(), "banned content-transfer selector .%s", v.Sel.Name)
	}
	if obj, ok := w.pc.info.Uses[v.Sel].(*types.Var); ok && obj.Pkg() != nil &&
		obj.Pkg().Path() == "os" && !isMappingOwnerPath(w.pc.pkg.Path()) {
		// os.Stdin/os.Stdout/os.Stderr are pre-minted live descriptors:
		// reading them outside the mapping owner leaks a capability the
		// source scan cannot trace to an approved producer.
		if fileValueType(obj.Type(), map[types.Type]bool{}) {
			w.fail(v.Pos(), "file-bearing os variable %s outside the mapping owner (capability launder)", v.Sel.Name)
		}
	}
	if sel := w.pc.info.Selections[v]; sel != nil {
		if sel.Kind() == types.FieldVal {
			return // field access: no transfer, no capability change
		}
		if methodReceiverBearsFile(sel) && !approvedFileMethods[v.Sel.Name] {
			w.fail(v.Pos(), "%s on a file-bearing receiver outside the approved capability surface", v.Sel.Name)
		}
		return
	}
	// Method expressions and method values bound without an invocation:
	// (*os.Root).Open, file.Read, root.Open — the resolved object is the
	// declared method.
	if obj, ok := w.pc.info.Uses[v.Sel].(*types.Func); ok {
		sig, ok := obj.Type().(*types.Signature)
		if ok && sig.Recv() != nil && fileValueType(sig.Recv().Type(), map[types.Type]bool{}) &&
			!approvedFileMethods[v.Sel.Name] {
			w.fail(v.Pos(), "%s method expression on a file-bearing receiver outside the approved capability surface", v.Sel.Name)
		}
	}
}

// checkCall applies the receiver rule, the file-argument ban, the
// interface-erasure rule, conversions, and the builtin page sinks.
func (w *fileRules) checkCall(v *ast.CallExpr) {
	fun := unparen(v.Fun)
	exempt := false
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		exempt = w.exempts[sel.Pos()]
		if !exempt && methodReceiverBearsFile(w.pc.info.Selections[sel]) && !approvedFileMethods[sel.Sel.Name] {
			w.fail(v.Pos(), "%s on a file-bearing receiver outside the approved capability surface", sel.Sel.Name)
		}
	}
	// Builtin page sinks: copy/append of a full page view into an owned
	// buffer (the complete-page ownership rule).
	if id, ok := fun.(*ast.Ident); ok {
		switch id.Name {
		case "copy":
			w.checkCopy(v)
			return
		case "append":
			w.checkAppend(v)
			return
		}
	}
	// Type conversion: X(f) where X is a type. A file-typed source
	// converted into a non-file-bearing target erases the descriptor.
	if w.isTypeExpr(fun) {
		if len(v.Args) == 1 {
			src := w.typeOf(v.Args[0])
			dst := w.typeOf(v)
			if src != nil && dst != nil && fileValueType(src, map[types.Type]bool{}) &&
				!fileValueType(dst, map[types.Type]bool{}) {
				w.fail(v.Pos(), "file-bearing value converted into a non-file-bearing type (launder)")
			}
			// any(H{F: f}): a struct holding a file field converted into
			// an interface erases the descriptor's static type the same
			// way the interface-slot store does.
			if src != nil && dst != nil && !fileValueType(dst, map[types.Type]bool{}) &&
				isInterfaceType(dst) && structContainsFile(src, map[types.Type]bool{}) {
				w.fail(v.Pos(), "struct holding a file-bearing field converted into an interface type (launder)")
			}
			if pv := w.pageValue(v.Args[0]); pv.tainted && pageFull(pv) && dst != nil {
				w.checkArrayConversionSink(v.Pos(), dst, pv)
			}
		}
		return
	}
	approved := w.approvedCallee(fun)
	formals := w.callFormals(fun)
	// A call with no result (a void callback or an unproven function
	// variable) can still copy a full mapped page into owned memory
	// inside a body the scan cannot follow; the fail-closed contract
	// treats such calls as transfers when a variable indirection hides
	// the callee. Calls with a scalar result stay reads: a checksum or
	// length lookup cannot move the bytes into owned storage.
	voidVarCall := false
	varIndirect := false
	switch f := fun.(type) {
	case *ast.Ident:
		switch obj := w.pc.info.Uses[f].(type) {
		case *types.Var:
			if rt := w.typeOf(v); rt == nil || isVoidTuple(rt) {
				voidVarCall = true
			}
			// A call through an unproven function-typed variable (a
			// parameter callback, a stdlib-bound value) has a body the
			// scan cannot follow: even a scalar result (func([]byte) int)
			// can copy a full mapped view into owned memory inside that
			// body, so such calls are transfers when they receive a page.
			if !w.approvedFuncVar(obj, 0) {
				varIndirect = true
			}
		}
	case *ast.SelectorExpr:
		switch obj := w.pc.info.Uses[f.Sel].(type) {
		case *types.Func:
			// A concrete method on a value type has a scanned body, but
			// an interface method dispatches to an unknowable
			// implementation: c.Apply(page) on a CB interface can copy
			// the full page inside an unscanned method body.
			if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
				varIndirect = true
			}
		default:
			// h.cb(page) with cb a function-typed field of a struct: the
			// callee is not statically visible, so the call is an
			// unproven indirection like a function variable.
			varIndirect = true
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		// fs[0](page): a call through a slice/array element or map
		// lookup has an unknowable callee body.
		varIndirect = true
	case *ast.StarExpr:
		// (*p)(page): a call through a dereferenced function pointer has
		// an unknowable callee body.
		varIndirect = true
	case *ast.CallExpr, *ast.TypeAssertExpr:
		// factory()(page) and x.(func([]byte) int)(page): the callee is
		// produced by an expression the scan cannot resolve to a body.
		varIndirect = true
	}
	transfer := !approved && !exempt && (resultHoldsBytes(w.typeOf(v)) || voidVarCall)
	// An opaque method-value call whose RECEIVER carries a mapped view
	// can copy the full page into an owned result (an interface method
	// with a string/byte result, a struct function field) even when no
	// explicit argument names the page: the body is unprovable, so the
	// call itself is the transfer point.
	if transfer {
		if sel, ok := fun.(*ast.SelectorExpr); ok {
			if selRecv, isSel := w.pc.info.Selections[sel]; isSel && selRecv.Kind() == types.MethodVal {
				if pv := w.pageValue(sel.X); pv.tainted && pageFull(pv) {
					w.fail(v.Pos(), "mapped page view passed to %s on an unprovable receiver (complete page into owned memory)", calleeText(fun))
				}
			}
		}
	}
	// A call whose RESULT is a live descriptor is itself a capacity mint:
	// closures and function variables can materialize a file (os.Stdout,
	// os.Pipe) that no argument rule ever sees, and os functions mint
	// files by construction. Outside the mapping owner every such shape
	// is a capability launder. The mapping owner keeps its constructor
	// surface (os.OpenFile/os.NewFile) and is policed by the receiver
	// and selector rules instead.
	resT := w.typeOf(v)
	// An unproven function variable whose result is an interface can
	// materialize a file-backed value (a func() io.Reader factory with an
	// unscanned body); the interface erasure rules cannot see through it,
	// so outside the mapping owner it fails closed like a concrete
	// file-bearing result. A variable bound to a func literal anywhere in
	// the package is not opaque: its literal bodies are scanned and
	// policed at their own sites, so the call itself stays benign.
	interfaceVarResult := false
	if id, ok := fun.(*ast.Ident); ok {
		if v, isVar := w.pc.info.Uses[id].(*types.Var); isVar {
			if w.pkgFuncVars[v] && !w.approvedFuncVar(v, 0) && (!w.pc.pkgFuncLitBound[v] || w.pc.pkgFuncNonLitBound[v]) {
				interfaceVarResult = isInterfaceType(resT)
			} else if !w.pkgFuncVars[v] && resT != nil && isInterfaceType(resT) && types.NewMethodSet(resT).Len() == 0 {
				// A parameter or local function value returning an
				// EMPTY interface (func() any) is fully opaque: the
				// body is not scanned here and the result can hold
				// anything, so the call fails closed like an opaque
				// package function variable.
				interfaceVarResult = true
			}
		}
	}
	// An interface-method call returning an interface (c.Codec() any) can
	// materialize a file-backed value through an unscanned implementation
	// body: outside the mapping owner the same fail-closed rule applies.
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		if obj, ok := w.pc.info.Uses[sel.Sel].(*types.Func); ok {
			if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
				interfaceVarResult = isInterfaceType(resT)
			}
		}
	}
	if resT != nil && !isMappingOwnerPath(w.pc.pkg.Path()) &&
		(fileValueType(resT, map[types.Type]bool{}) || interfaceVarResult) {
		switch f := fun.(type) {
		case *ast.FuncLit:
			w.fail(v.Pos(), "func literal returns a file-bearing value outside the mapping owner (capability launder)")
		case *ast.Ident:
			switch obj := w.pc.info.Uses[f].(type) {
			case *types.Var:
				w.fail(v.Pos(), "function variable %s returns a file-bearing value outside the mapping owner (capability launder)", f.Name)
			case *types.Func:
				if pkg := obj.Pkg(); pkg != nil && pkg.Path() == "os" {
					w.fail(v.Pos(), "os function %s returns a file-bearing value outside the mapping owner (capability launder)", f.Name)
				}
			}
		case *ast.SelectorExpr:
			// A concrete method on a scanned receiver type has a
			// policed body; an os function mints by construction; any
			// other selector binding (a struct function field h.get(),
			// an interface method dispatch) is an indirect callee whose
			// body the scan cannot follow, so its file-bearing result
			// fails closed.
			if obj, ok := w.pc.info.Uses[f.Sel].(*types.Func); ok {
				if pkg := obj.Pkg(); pkg != nil && pkg.Path() == "os" {
					w.fail(v.Pos(), "os function %s returns a file-bearing value outside the mapping owner (capability launder)", f.Sel.Name)
				} else if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) &&
					(fileValueType(resT, map[types.Type]bool{}) || (resT != nil && isInterfaceType(resT) && types.NewMethodSet(resT).Len() == 0)) {
					// An interface method with a CONCRETE file-bearing
					// result dispatches to implementations the call site
					// cannot prove benign (a nil factory is not proof):
					// the capability fails closed here, while the
					// concrete method bodies stay policed at their own
					// sites. An EMPTY-interface result is equally
					// opaque: the implementation body is not scanned,
					// and an any result can materialize a file-backed
					// value the rules below can never see. Interface
					// results like io.ReadCloser are content-transfer
					// concerns, not capability mints.
					w.fail(v.Pos(), "interface method %s returns a file-bearing value outside the mapping owner (capability launder)", f.Sel.Name)
				}
			} else {
				w.fail(v.Pos(), "%s returns a file-bearing value outside the mapping owner (capability launder)", calleeText(fun))
			}
		default:
			// Index, call-produced, and type-asserted callees have
			// unknowable bodies; a file-bearing result from any of them
			// fails closed.
			w.fail(v.Pos(), "indirect call returns a file-bearing value outside the mapping owner (capability launder)")
		}
	}
	for i, arg := range v.Args {
		t := w.typeOf(arg)
		if t != nil && fileValueType(t, map[types.Type]bool{}) && !approved {
			w.fail(v.Pos(), "*os.File-bearing value passed to %s", calleeText(fun))
		}
		// The fmt spread exemption: variadic []any diagnostics helpers
		// (corrupt, headerErr) pass their argument slice straight into
		// fmt.Sprintf for bounded error text; the spread itself is not
		// an owned byte-copy sink. The callee's own parameter stays a
		// carrier, so element extraction out of it is still policed.
		// The exemption applies only to clean spreads and to the
		// helper's own param-sourced slice: a CONCRETE collection that
		// holds a live mapped view (args := []any{page};
		// fmt.Sprintf("%s", args...)) hands the full page to the owned
		// formatter and is the same complete-page copy as passing it
		// directly.
		spread := v.Ellipsis.IsValid() && i == len(v.Args)-1 && w.fmtCallee(fun)
		if spread {
			if pv := w.pageValue(arg); pv.tainted && pageFull(pv) && !pv.hasSrc {
				w.fail(v.Pos(), "mapped page view spread into %s (complete page into owned memory)", calleeText(fun))
			}
			continue
		}
		pageArg := w.pageValue(arg)
		if pageArg.tainted && pageFull(pageArg) && (transfer || varIndirect) {
			w.fail(v.Pos(), "mapped page view passed to %s (complete page into owned memory)", calleeText(fun))
		}
		// The owned byte-builder family copies its argument into an owned
		// heap buffer: bytes.NewBuffer(v), bytes.Buffer.Write*(v), and
		// strings.Builder.Write*(v) own the bytes afterward, so a full
		// mapped view reaching them is the complete-page violation even
		// though their result types are structs or scalar counts the
		// transfer rule cannot see.
		if pageArg.tainted && pageFull(pageArg) && w.ownedCopySink(fun) {
			w.fail(v.Pos(), "mapped page view copied into an owned byte builder (%s)", calleeText(fun))
		}
	}
	w.checkInterfaceErasure(v, formals)
	// A module helper that converts one of its parameters into an owned
	// string (string(p) recorded in its summary) copies the caller's
	// bytes; the conversion inside the callee cannot see that the bound
	// is a full mapped page, so the call site fails closed.
	if fn := callCalleeFuncOrVar(w, fun); fn != nil {
		w.checkStringParamCalls(v, fn)
	}
}

// callCalleeFuncOrVar resolves the statically visible callee of an
// ordinary function or method call, following approved package-level
// function-typed variable aliases (var a = f; a(page)) to the function
// they provably bind; nil means the callee is an indirect shape
// (variable, index, call, type-assert) with no body to summarize.
func callCalleeFuncOrVar(w *fileRules, fun ast.Expr) *types.Func {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		switch obj := w.pc.info.Uses[f].(type) {
		case *types.Func:
			return obj
		case *types.Var:
			return w.funcVarCallee(obj, 0)
		}
	case *ast.SelectorExpr:
		fn, _ := w.pc.info.Uses[f.Sel].(*types.Func)
		return fn
	}
	return nil
}

// funcVarCallee follows a package-level function-typed variable's
// initializer chain to a plain function, mirroring approvedFuncVar's
// proof rules (never-reassigned, same-package initializer, bounded
// depth). Only plain functions are returned: func literals have no
// summary entry to consult.
func (w *fileRules) funcVarCallee(v *types.Var, depth int) *types.Func {
	if v == nil || depth > 2 || v.Parent() != w.pc.pkg.Scope() || w.pc.reassignedVars[v] {
		return nil
	}
	init, ok := w.pc.varInits[v]
	if !ok {
		return nil
	}
	switch i := unparen(init).(type) {
	case *ast.Ident:
		switch o := w.pc.info.Uses[i].(type) {
		case *types.Func:
			return o
		case *types.Var:
			return w.funcVarCallee(o, depth+1)
		}
	case *ast.SelectorExpr:
		fn, _ := w.pc.info.Uses[i.Sel].(*types.Func)
		return fn
	}
	return nil
}

// collectPkgFuncVars records package-level function-typed variables
// declared anywhere in the package (with or without an initializer). The
// capability rules apply the fail-closed interface-result test to package
// vars only: local variables are either literal-bound (their body is
// scanned) or trace back to a package variable or a caller-supplied
// parameter, both of which are policed at their own boundary. Collecting
// from every package file keeps the rule independent of the file the
// call site lives in (a factory declared in a.go and called from b.go
// must not escape the rule).
func collectPkgFuncVars(info *types.Info, files []*parsedFile) map[types.Object]bool {
	out := map[types.Object]bool{}
	for _, pf := range files {
		for _, decl := range pf.ast.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					obj := info.ObjectOf(name)
					if obj == nil {
						continue
					}
					t := obj.Type()
					sig, ok := t.(*types.Signature)
					if !ok {
						// A named function type (type F func() any; var f F)
						// hides the signature behind a *types.Named; the
						// fail-closed interface-result rule must see it.
						if n, isNamed := types.Unalias(t).(*types.Named); isNamed {
							sig, _ = n.Underlying().(*types.Signature)
						}
					}
					if sig != nil {
						out[obj] = true
					}
				}
			}
		}
	}
	return out
}

// isVoidTuple reports whether t is the empty multi-result tuple of a
// call without results.
func isVoidTuple(t types.Type) bool {
	tup, ok := t.(*types.Tuple)
	return ok && tup.Len() == 0
}

// resultHoldsBytes reports whether the call's result type can carry byte
// content to an owned destination: byte slices/arrays (and named aliases),
// strings, interfaces, structs, pointers, and func types. Callees whose
// result is a plain scalar (binary.Uint*, crc32/adler checksums, len, ...)
// cannot transfer a page into owned memory, so passing a mapped view to
// them is a read, not an ownership transfer, and stays legal.
func resultHoldsBytes(t types.Type) bool {
	if t == nil {
		return true // unknown result: conservative
	}
	return typeCanCarryPage(t) || byteHoldingContainer(t, map[types.Type]bool{})
}

// byteHoldingContainer covers the non-byte-slice shapes that can still
// hold an owned byte copy of a page.
func byteHoldingContainer(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.(type) {
	case *types.Basic:
		return u.Kind() == types.String
	case *types.Interface:
		return true // dynamic value can be an owned page copy
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if byteHoldingContainer(u.Field(i).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Pointer:
		return byteHoldingContainer(u.Elem(), seen)
	case *types.Signature:
		r := u.Results()
		for i := 0; i < r.Len(); i++ {
			if byteHoldingContainer(r.At(i).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Tuple:
		for i := 0; i < u.Len(); i++ {
			if byteHoldingContainer(u.At(i).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Named:
		return byteHoldingContainer(u.Underlying(), seen)
	case *types.Alias:
		return byteHoldingContainer(types.Unalias(u), seen)
	}
	return false
}

// isTypeExpr reports whether the call's function position names a type
// (a conversion) rather than a function or method.
// ownedCopySink reports whether the callee copies its byte argument into
// an owned heap buffer whose result type hides the copy from the
// resultHoldsBytes transfer rule: bytes.NewBuffer, bytes.Buffer.Write*
// (the buffer owns the appended bytes), and strings.Builder.Write*
// (the builder owns the appended bytes). bytes.NewReader is deliberately
// absent: it wraps the input without copying, so the mapped view stays a
// view and the bounded-record rule applies.
func (w *fileRules) ownedCopySink(fun ast.Expr) bool {
	sel, ok := unparen(fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	receiver := w.typeOf(sel.X)
	if t, ok := receiver.(*types.Pointer); ok {
		receiver = types.Unalias(t.Elem())
	}
	recvName := ""
	if n, ok := receiver.(*types.Named); ok {
		recvName = n.Obj().Name()
	}
	switch recvName {
	case "Buffer", "Builder":
		switch sel.Sel.Name {
		case "Write", "WriteString", "WriteByte", "WriteRune":
			return true
		}
		return false
	}
	// bytes.NewBuffer(page): the package-level constructor of the owned
	// buffer. Its result is *bytes.Buffer, whose internal []byte the
	// transfer rule cannot see.
	if fn, ok := w.pc.info.Uses[sel.Sel].(*types.Func); ok {
		if pkg := fn.Pkg(); pkg != nil && pkg.Path() == "bytes" && fn.Name() == "NewBuffer" {
			return true
		}
	}
	return false
}

func (w *fileRules) isTypeExpr(fun ast.Expr) bool {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		obj := w.pc.info.Uses[f]
		if obj == nil {
			if _, ok := w.pc.info.Defs[f].(*types.TypeName); ok {
				return true
			}
			return false
		}
		_, ok := obj.(*types.TypeName)
		return ok
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.StarExpr, *ast.StructType:
		return true
	case *ast.SelectorExpr:
		_, ok := w.pc.info.Uses[f.Sel].(*types.TypeName)
		return ok
	case *ast.IndexExpr:
		return w.isTypeExpr(f.X)
	case *ast.IndexListExpr:
		return w.isTypeExpr(f.X)
	case *ast.ParenExpr:
		return w.isTypeExpr(f.X)
	}
	return false
}

// callFormals returns the declared parameter types of the callee, or nil
// when the callee is not a plain function/var/method reference with a
// known signature (conversions, builtins, closures by value).
func (w *fileRules) callFormals(fun ast.Expr) []types.Type {
	var sig *types.Signature
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		obj := w.pc.info.Uses[f]
		switch o := obj.(type) {
		case *types.Func:
			sig, _ = o.Type().(*types.Signature)
		case *types.Var:
			sig, _ = o.Type().(*types.Signature)
		}
	case *ast.SelectorExpr:
		if fn, ok := w.pc.info.Uses[f.Sel].(*types.Func); ok {
			sig, _ = fn.Type().(*types.Signature)
		}
	}
	if sig == nil || sig.Params() == nil {
		return nil
	}
	out := make([]types.Type, sig.Params().Len())
	for i := 0; i < sig.Params().Len(); i++ {
		out[i] = sig.Params().At(i).Type()
	}
	return out
}

// checkInterfaceErasure flags a file-typed argument placed into an
// interface formal parameter, or into a type parameter whose instantiated
// result no longer bears the file. Both shapes erase the descriptor's
// static type; the launder checks then cannot see it downstream.
func (w *fileRules) checkInterfaceErasure(v *ast.CallExpr, formals []types.Type) {
	if len(formals) == 0 {
		return
	}
	resT := w.typeOf(v)
	resBears := resT != nil && fileValueType(resT, map[types.Type]bool{})
	for i, arg := range v.Args {
		if i >= len(formals) {
			break
		}
		at := w.typeOf(arg)
		if at == nil {
			continue
		}
		bears := fileValueType(at, map[types.Type]bool{})
		// A struct holding a file field passed into an interface
		// formal erases the struct type: the descriptor inside the
		// field becomes invisible to the launder checks downstream, so
		// it fails closed like a directly file-bearing argument.
		holds := !bears && isInterfaceType(formals[i]) && structContainsFile(at, map[types.Type]bool{})
		if !bears && !holds {
			continue
		}
		if isInterfaceType(formals[i]) {
			w.fail(v.Pos(), "file-bearing argument laundered into an interface parameter (type erasure)")
		} else if _, ok := formals[i].(*types.TypeParam); ok {
			if !resBears {
				w.fail(v.Pos(), "file-bearing argument through a generic callee erased into a non-file-bearing result (type erasure)")
			}
		}
	}
}

// checkCopy flags copy(dst, src) when the copied span can be a complete
// page: src is a mapped page view with an unbounded/unknown bound, and dst
// is an owned buffer that is not statically tiny.
func (w *fileRules) checkCopy(v *ast.CallExpr) {
	if len(v.Args) != 2 {
		return
	}
	src := w.pageValue(v.Args[1])
	if !src.tainted || !pageFull(src) {
		return
	}
	dst := v.Args[0]
	if dstMax := w.ownedCap(dst); dstMax >= 0 && dstMax < pageSize {
		return // statically bounded record copy
	}
	w.fail(v.Pos(), "copy of a mapped page view into an owned buffer (complete page)")
}

// checkAppend flags append(dst, src...) when src is a mapped page view.
func (w *fileRules) checkAppend(v *ast.CallExpr) {
	if len(v.Args) < 2 {
		return
	}
	src := w.pageValue(v.Args[len(v.Args)-1])
	if src.tainted && pageFull(src) {
		w.fail(v.Pos(), "append of a mapped page view into an owned buffer (complete page)")
	}
	// An append whose destination is a complete mapped page view (a
	// minted Page/View, page[0:4096:4096], or a view spanning one or more
	// full pages such as page[0:8192:8192]) forces Go to allocate a fresh
	// owned array and copy the whole span into it. Bounded views and
	// owned buffers stay legal.
	if dv := w.pageValue(v.Args[0]); dv.tainted && pageFull(dv) {
		w.fail(v.Pos(), "append into a complete mapped page view (full page reallocated into owned memory)")
	}
	// A file-bearing argument appended into a non-file-bearing element
	// slot erases the descriptor (append([]any{}, f)); struct values
	// holding a file field launder the same way into interface element
	// types.
	elemT := collectionElementType(w.typeOf(v.Args[0]))
	for _, a := range v.Args[1:] {
		at := w.typeOf(a)
		if at == nil || elemT == nil {
			continue
		}
		bears := fileValueType(at, map[types.Type]bool{})
		holds := !bears && isInterfaceType(elemT) && structContainsFile(at, map[types.Type]bool{})
		if !bears && !holds {
			continue
		}
		if !fileValueType(elemT, map[types.Type]bool{}) {
			w.fail(v.Pos(), "file-bearing value appended into a non-file-bearing collection (launder)")
		}
	}
}

// ownedCap returns the definite byte capacity of an owned destination
// expression: array types and constant slice bounds; -1 when unknowable.
func (w *fileRules) ownedCap(e ast.Expr) int64 {
	t := w.typeOf(e)
	if t == nil {
		return maxUnknown
	}
	switch u := t.(type) {
	case *types.Array:
		return u.Len()
	case *types.Slice:
		if se, ok := unparen(e).(*ast.SliceExpr); ok {
			if se.Slice3 {
				return maxUnknown
			}
			lo, okLo := constIntExpr(se.Low)
			hi, okHi := constIntExpr(se.High)
			switch {
			case okLo && okHi:
				return hi - lo
			case !okLo && okHi:
				return hi
			case okLo && se.High == nil:
				if base := w.ownedCap(se.X); base >= 0 {
					return base - lo
				}
			case !okLo && !okHi:
				if base := w.ownedCap(se.X); base >= 0 {
					return base
				}
			}
		}
	}
	return maxUnknown
}

func constIntExpr(e ast.Expr) (int64, bool) {
	switch v := unparen(e).(type) {
	case *ast.BasicLit:
		if v.Kind == token.INT {
			var n int64
			if _, err := fmt.Sscanf(v.Value, "%d", &n); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// pageFull reports whether the tainted value can span a complete page.
// A definite symbolic bound (a constant slice span such as page[48:112])
// beats an unset maxLen; an unknown or linear-form span stays full.
func pageFull(pv pageValue) bool {
	if pv.maxLen >= pageSize || pv.maxLen == maxUnknown {
		return true
	}
	if pv.hasSym {
		if c, ok := pv.sym.isConst(); ok {
			return c == maxUnknown || c >= pageSize
		}
		return true
	}
	return false
}

// pageValue resolves the mapped-page taint of an expression, including
// through single-argument conversions ([N]byte(page), string(page)) whose
// taint the flow engine derives from the converted argument.
func (w *fileRules) pageValue(e ast.Expr) pageValue {
	if w.pc.pf == nil {
		return pageValue{}
	}
	if pv, ok := w.pc.pf.values[e]; ok {
		return pv
	}
	if call, ok := unparen(e).(*ast.CallExpr); ok && w.isTypeExpr(call.Fun) && len(call.Args) == 1 {
		if pv, ok := w.pc.pf.values[call.Args[0]]; ok {
			return pv
		}
	}
	return pageValue{}
}

// checkAssign flags assignment-side launders and page conversions.
func (w *fileRules) checkAssign(v *ast.AssignStmt) {
	for i, rhs := range v.Rhs {
		if i >= len(v.Lhs) {
			break
		}
		w.checkLaunderValue(v.Lhs[i].Pos(), rhs, w.typeOf(v.Lhs[i]))
		if pv := w.pageValue(rhs); pv.tainted && pageFull(pv) {
			w.checkArrayConversionSink(v.Lhs[i].Pos(), lhsTypeForCheck(w, v.Lhs[i]), pv)
		}
		// m[f] = 1: a runtime map store launders the descriptor through
		// the KEY when the map's key slot cannot bear it (composite map
		// keys are checked at the literal itself).
		if ix, ok := unparen(v.Lhs[i]).(*ast.IndexExpr); ok {
			if mt := w.typeOf(ix.X); mt != nil {
				if kt := collectionKeyType(mt); kt != nil {
					if it := w.typeOf(ix.Index); it != nil {
						bears := fileValueType(it, map[types.Type]bool{})
						// A struct holding a file field stored into an
						// interface-typed map key erases the key type;
						// mirror the collection-slot rule so
						// m[H{F: f}] = 1 fails like m[f] = 1.
						holds := !bears && isInterfaceType(kt) && structContainsFile(it, map[types.Type]bool{})
						if (bears || holds) && !fileValueType(kt, map[types.Type]bool{}) {
							w.fail(v.Lhs[i].Pos(), "file-bearing value stored into a non-file-bearing map key (launder)")
						}
					}
				}
			}
		}
	}
}

func lhsTypeForCheck(w *fileRules, lhs ast.Expr) types.Type { return w.typeOf(lhs) }

func (w *fileRules) checkSend(v *ast.SendStmt) {
	t := w.typeOf(v.Value)
	if t != nil && fileValueType(t, map[types.Type]bool{}) {
		ct := w.typeOf(v.Chan)
		if ct == nil || !fileValueType(ct, map[types.Type]bool{}) {
			w.fail(v.Pos(), "file-bearing value sent on a channel that cannot carry it (launder)")
		}
	}
	// ch <- H{F: f}: a struct holding a file field sent on a channel
	// whose element slot erases the descriptor is the same launder.
	if t != nil && structContainsFile(t, map[types.Type]bool{}) {
		ct := w.typeOf(v.Chan)
		if ct == nil {
			return
		}
		et := collectionElementType(ct)
		if et == nil || (!fileValueType(et, map[types.Type]bool{}) && (isInterfaceType(et) || !structContainsFile(et, map[types.Type]bool{}))) {
			w.fail(v.Pos(), "struct holding a file-bearing field sent on a channel that cannot carry it (launder)")
		}
	}
}

func (w *fileRules) checkTypeAssert(v *ast.TypeAssertExpr) {
	src := w.typeOf(v.X)
	dst := w.typeOf(v)
	if src != nil && dst != nil && fileValueType(src, map[types.Type]bool{}) &&
		!fileValueType(dst, map[types.Type]bool{}) {
		w.fail(v.Pos(), "file-bearing value asserted into a non-file-bearing type (launder)")
	}
}

// checkArrayConversionSink flags [N]byte(page) with N >= PageSize and
// string(page) with a definite full-page bound. Defined (named) array and
// string types are unwrapped so type pageArr [4096]byte and
// type pageStr string conversions cannot hide the owned copy.
func (w *fileRules) checkArrayConversionSink(pos token.Pos, dst types.Type, pv pageValue) {
	if dst == nil {
		return
	}
	dst = unwrapToUnderlying(dst)
	if arr, ok := dst.(*types.Array); ok && arr.Len() >= pageSize && pv.tainted && pageFull(pv) {
		w.fail(pos, "array conversion of a mapped page view into an owned [%d]byte", arr.Len())
	}
	if b, ok := dst.(*types.Basic); ok && b.Kind() == types.String && pv.tainted && !pv.hasSrc && !definiteSubPage(pv) {
		w.fail(pos, "string conversion of a full-page view")
	}
}

// definiteSubPage reports whether a page-tainted value is statically
// bounded below a complete page: a definite maxLen or a constant
// symbolic slice bound under pageSize. Every other tainted value is
// treated as possibly spanning a complete page, because string
// conversion copies exactly the value's runtime length into owned
// memory and an unknown bound may be 4096 at runtime. Untainted values
// (plain []byte parameters and locally owned buffers) never reach this
// test: only page-provenance views do.
func definiteSubPage(pv pageValue) bool {
	if pv.hasSym {
		if c, ok := pv.sym.isConst(); ok {
			return c >= 0 && c < pageSize
		}
		return false
	}
	return pv.maxLen >= 0 && pv.maxLen < pageSize
}

// definitePageSpan reports whether a tainted value's length is statically
// known to span at least one complete page: a constant maxLen or a
// constant symbolic slice bound. Used by the array conversion and
// owned-capacity sinks.
func definitePageSpan(pv pageValue) bool {
	if pv.maxLen >= pageSize {
		return true
	}
	if pv.hasSym {
		if c, ok := pv.sym.isConst(); ok {
			return c >= pageSize
		}
	}
	return false
}

// unwrapToUnderlying strips named types and aliases to the underlying
// type, so conversions to defined array/string types are matched by the
// conversion sinks.
func unwrapToUnderlying(t types.Type) types.Type {
	for {
		switch v := types.Unalias(t).(type) {
		case *types.Named:
			t = v.Underlying()
		default:
			return t
		}
	}
}

// checkComposite flags literals that launder file-bearing values into
// non-bearing fields.
func (w *fileRules) checkComposite(v *ast.CompositeLit) {
	typ := w.typeOf(v)
	if typ == nil {
		return
	}
	if st, ok := derefStruct(typ); ok {
		for i, el := range v.Elts {
			var field *types.Var
			var val ast.Expr
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					for j := 0; j < st.NumFields(); j++ {
						if st.Field(j).Name() == id.Name {
							field = st.Field(j)
							break
						}
					}
				}
				val = kv.Value
			} else if i < st.NumFields() {
				field = st.Field(i)
				val = el
			}
			if field == nil || val == nil {
				continue
			}
			stv := w.typeOf(val)
			if stv != nil && fileValueType(stv, map[types.Type]bool{}) &&
				!fileValueType(field.Type(), map[types.Type]bool{}) {
				w.fail(val.Pos(), "file-bearing value stored into field %s of a non-file-bearing type (launder)", field.Name())
			}
		}
		return
	}
	// Slice/array/map literals: an element (and a map key) whose static
	// type is file-bearing must stay in a file-bearing slot. Hiding one
	// inside an interface-valued collection ([]any{f}, map[any]int{f:1})
	// erases the descriptor from the capability walk, so the slot must
	// itself bear files.
	elemT := collectionElementType(typ)
	keyT := collectionKeyType(typ)
	for _, el := range v.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if keyT != nil {
				w.checkCollectionSlot(kv.Key.Pos(), kv.Key, keyT, "map key")
			}
			w.checkCollectionSlot(kv.Value.Pos(), kv.Value, elemT, "collection element")
			continue
		}
		w.checkCollectionSlot(el.Pos(), el, elemT, "collection element")
	}
}

// checkCollectionSlot flags a file-bearing value (or a struct holding a
// file field, when the slot is an interface) placed into a collection
// slot whose type loses the descriptor.
func (w *fileRules) checkCollectionSlot(pos token.Pos, val ast.Expr, slotT types.Type, what string) {
	stv := w.typeOf(val)
	if stv == nil || slotT == nil {
		return
	}
	bears := fileValueType(stv, map[types.Type]bool{})
	holds := !bears && isInterfaceType(slotT) && structContainsFile(stv, map[types.Type]bool{})
	if !bears && !holds {
		return
	}
	if !fileValueType(slotT, map[types.Type]bool{}) {
		w.fail(pos, "file-bearing value stored into a non-file-bearing %s (launder)", what)
	}
}

// collectionKeyType returns the key type of a map type (through defined
// types and pointers), or nil for non-map types.
func collectionKeyType(t types.Type) types.Type {
	u := unwrapToUnderlying(t)
	switch v := u.(type) {
	case *types.Map:
		return v.Key()
	case *types.Pointer:
		return collectionKeyType(v.Elem())
	}
	return nil
}

// collectionElementType returns the element type of a slice, array, or
// map type (through defined types and pointers), or nil for other types.
func collectionElementType(t types.Type) types.Type {
	u := unwrapToUnderlying(t)
	switch v := u.(type) {
	case *types.Slice:
		return v.Elem()
	case *types.Array:
		return v.Elem()
	case *types.Map:
		return v.Elem()
	case *types.Pointer:
		return collectionElementType(v.Elem())
	}
	return nil
}

// checkLaunderValue flags any assignment-like placement of a
// file-bearing value into a slot whose type no longer bears it.
func (w *fileRules) checkLaunderValue(pos token.Pos, val ast.Expr, dstType types.Type) {
	stv := w.typeOf(val)
	if stv == nil || dstType == nil {
		return
	}
	if fileValueType(stv, map[types.Type]bool{}) && !fileValueType(dstType, map[types.Type]bool{}) {
		w.fail(pos, "file-bearing value assigned into a non-file-bearing slot (launder)")
	}
	// A struct holding a file field keeps the descriptor reachable and
	// typed while the struct type survives; an interface slot erases it
	// (return H{F: f} as any), so the crossing into an interface is the
	// launder.
	if !fileValueType(dstType, map[types.Type]bool{}) && isInterfaceType(dstType) &&
		structContainsFile(stv, map[types.Type]bool{}) {
		w.fail(pos, "struct holding a file-bearing field placed into an interface slot (launder)")
	}
	if pv := w.pageValue(val); pv.tainted {
		w.checkArrayConversionSink(pos, dstType, pv)
	}
}

func (w *fileRules) fail(pos token.Pos, format string, args ...any) {
	w.rep.fail(pos, format, args...)
}

func calleeText(fun ast.Expr) string {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return exprText(f.X) + "." + f.Sel.Name
	}
	return "?"
}

func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.ParenExpr:
		return "(" + exprText(v.X) + ")"
	case *ast.StarExpr:
		return "*" + exprText(v.X)
	}
	return "..."
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// findExemptions locates the tolerated in-memory inflater call shapes
// inside internal/reader/metadata.go. Two families are exempted:
//
//   - io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]) and
//     io.ReadFull(zr, out[int(meta.MetadataUncompressed):]) — the exact
//     two inflation reads — only when the first argument is not
//     file-bearing and not a page view;
//   - Read/ReadByte selectors whose receiver's static type is a concrete
//     in-memory byte container (bytes.Reader, bytes.Buffer,
//     strings.Reader): a file cannot hide behind a concrete in-memory
//     type.
func findExemptions(w *fileRules, f *ast.File, path string) map[token.Pos]bool {
	exempts := map[token.Pos]bool{}
	if !strings.HasSuffix(path, "internal/reader/metadata.go") {
		return exempts
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "ReadFull" && len(call.Args) == 2 {
			if id, ok := unparen(sel.X).(*ast.Ident); ok && id.Name == "io" {
				a0 := call.Args[0]
				// Only the exact variable-shaped stream argument (zr)
				// is exempt: a freshly constructed reader over a mapped
				// page (bytes.NewReader(page)) copies the complete page
				// into the owned output inside ReadFull and must not
				// inherit the exemption.
				a0t := w.typeOf(a0)
				pv := w.pageValue(a0)
				if isVariableRef(a0) && (a0t == nil || !fileValueType(a0t, map[types.Type]bool{})) && !pv.tainted {
					exempts[sel.Pos()] = true
				}
			}
		}
		if (sel.Sel.Name == "Read" || sel.Sel.Name == "ReadByte") && len(call.Args) <= 2 {
			// Only variable-shaped receivers (cr.r) are exempt: a
			// receiver the call itself constructs (bytes.NewReader(page))
			// can wrap a mapped view, and reading from it into owned
			// memory is a complete-page copy. A tainted receiver (a
			// local holding a page-wrapping reader) fails the same way.
			if isVariableRef(sel.X) && !w.pageValue(sel.X).tainted {
				recv := w.typeOf(sel.X)
				if recv != nil {
					name := concreteTypeName(recv)
					if inMemoryReaders[name] {
						exempts[sel.Pos()] = true
					}
				}
			}
		}
		return true
	})
	return exempts
}

// isVariableRef reports whether e names an existing variable (an
// identifier, a field selection, or a dereference) rather than
// constructing a fresh value.
func isVariableRef(e ast.Expr) bool {
	switch unparen(e).(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.StarExpr:
		return true
	}
	return false
}

// concreteTypeName renders a concrete (non-interface) receiver type as
// "pkg.Name".
func concreteTypeName(t types.Type) string {
	for {
		switch v := t.(type) {
		case *types.Pointer:
			t = v.Elem()
		case *types.Named:
			if obj := v.Obj(); obj != nil && obj.Pkg() != nil {
				return obj.Pkg().Name() + "." + obj.Name()
			}
			return ""
		default:
			return ""
		}
	}
}
