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

func buildContext(cfg osConfig) *build.Context {
	ctx := build.Default
	ctx.GOOS = cfg.GOOS
	ctx.GOARCH = cfg.GOARCH
	ctx.CgoEnabled = false
	return &ctx
}

// packageCheck is the typed result of one package under one OS config.
type packageCheck struct {
	pkg            *types.Package
	info           *types.Info
	fset           *token.FileSet
	loader         *loader
	files          []*parsedFile
	pf             *pageFlow
	varInits       map[*types.Var]ast.Expr
	reassignedVars map[*types.Var]bool
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
				}
			}
			return true
		})
	}
	for _, f := range asts {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range v.Lhs {
					if id, ok := unparen(lhs).(*ast.Ident); ok {
						if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
							reassigned[obj] = true
						}
					}
				}
			case *ast.IncDecStmt:
				if id, ok := unparen(v.X).(*ast.Ident); ok {
					if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
						reassigned[obj] = true
					}
				}
			}
			return true
		})
	}
	return &packageCheck{pkg: pkg, info: info, fset: tc.fset, loader: tc.loader, files: files, pf: nil, varInits: varInits, reassignedVars: reassigned}, nil
}

// fileRules carries one file's rule pass.
type fileRules struct {
	rep     *reporter
	pc      *packageCheck
	imports map[string]string
	path    string
	exempts map[token.Pos]bool
}

// runRules applies every rule family to one file of one package.
func runRules(rep *reporter, f *ast.File, pc *packageCheck, path string) {
	w := &fileRules{rep: rep, pc: pc, imports: checkImports(rep, f), path: path}
	w.exempts = findExemptions(w, f, path)
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
			results := resultTypes(w, d.Type)
			ast.Inspect(d.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.FuncLit); ok {
					// result types switch to the literal's own
					results = resultTypes(w, lit.Type)
				}
				if ret, ok := n.(*ast.ReturnStmt); ok {
					w.checkReturnCtx(ret, results)
					return true
				}
				return w.visit(n)
			})
		default:
			ast.Inspect(d, w.visit)
		}
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
			return p == w.pc.pkg.Path() || strings.HasPrefix(p, moduleInternalPrefix) || p == xsysImport
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
	}
	return true
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
		return w.approvedFuncPkg(fn)
	}
	return false
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
			if pv := w.pageValue(v.Args[0]); pv.tainted && pageFull(pv) && dst != nil {
				w.checkArrayConversionSink(v.Pos(), dst, pv)
			}
		}
		return
	}
	approved := w.approvedCallee(fun)
	formals := w.callFormals(fun)
	transfer := !approved && !exempt && resultHoldsBytes(w.typeOf(v))
	for _, arg := range v.Args {
		t := w.typeOf(arg)
		if t != nil && fileValueType(t, map[types.Type]bool{}) && !approved {
			w.fail(v.Pos(), "*os.File-bearing value passed to %s", calleeText(fun))
		}
		if pv := w.pageValue(arg); pv.tainted && pageFull(pv) && transfer {
			w.fail(v.Pos(), "mapped page view passed to %s (complete page into owned memory)", calleeText(fun))
		}
	}
	w.checkInterfaceErasure(v, formals)
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
		if at == nil || !fileValueType(at, map[types.Type]bool{}) {
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
// string(page) with a definite full-page bound.
func (w *fileRules) checkArrayConversionSink(pos token.Pos, dst types.Type, pv pageValue) {
	if dst == nil {
		return
	}
	if arr, ok := dst.(*types.Array); ok && arr.Len() >= pageSize && pv.tainted && pageFull(pv) {
		w.fail(pos, "array conversion of a mapped page view into an owned [%d]byte", arr.Len())
	}
	if _, ok := dst.(*types.Basic); ok && dst.String() == "string" && pv.tainted && pv.maxLen >= pageSize {
		w.fail(pos, "string conversion of a definite full-page view")
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
	// Slice/array/map literals: elements are checked by the assignment
	// and argument rules; nothing extra is needed here.
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
				a0 := w.typeOf(call.Args[0])
				pv := w.pageValue(call.Args[0])
				if (a0 == nil || !fileValueType(a0, map[types.Type]bool{})) && !pv.tainted {
					exempts[sel.Pos()] = true
				}
			}
		}
		if (sel.Sel.Name == "Read" || sel.Sel.Name == "ReadByte") && len(call.Args) <= 2 {
			recv := w.typeOf(sel.X)
			if recv != nil {
				name := concreteTypeName(recv)
				if inMemoryReaders[name] {
					exempts[sel.Pos()] = true
				}
			}
		}
		return true
	})
	return exempts
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
