package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/token"
	"go/types"
	"sort"
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
// lifecycleOwnerOnly are content-transfer-adjacent lifecycle syscalls
// that are legal only inside the mapping owner (internal/mapping): the
// single authority for file growth and durability. Every other package
// must go through the owner's typed methods (Grow/Flush/SyncFile);
// a future writer package calling raw ftruncate/msync/fsync is the same
// single-authority erosion the gate exists to pin.
var lifecycleOwnerOnly = map[string]bool{
	"Fallocate": true, "Fdatasync": true, "Fsync": true, "Ftruncate": true,
	"Msync": true, "Sync": true, "SyncFileRange": true, "Syncfs": true,
	"Truncate": true,
}

var bannedSelectors = map[string]bool{
	"Call": true, "CallSlice": true, "Clonefile": true, "Clonefileat": true,
	"Copy": true, "CopyBuffer": true, "CopyFS": true,
	"CopyFileRange": true, "CopyN": true, "Decode": true, "Dup": true, "Dup2": true, "Dup3": true,
	"Encode": true, "Exec": true, "Fallocate": true, "FcntlInt": true, "ForkExec": true,
	"Fdatasync": true, "Fsync": true, "Ftruncate": true,
	"IoctlFileClone": true, "IoctlFileCloneRange": true, "IoctlFileDedupeRange": true,
	"Tee": true, "Vmsplice": true,
	"Fprint": true, "Fprintf": true, "Fprintln": true, "Fscan": true,
	"Fscanf": true, "Fscanln": true, "Method": true, "MethodByName": true, "Msync": true,
	"Sync": true, "SyncFileRange": true, "Syncfs": true,
	"NewDecoder": true, "NewWriter": true, "NewWriterDict": true,
	"Peek": true, "Pread": true,
	"Preadv": true, "Print": true, "Printf": true, "Println": true,
	"Preadv2": true, "Pwrite": true, "Pwritev": true, "Pwritev2": true,
	"RawSyscall": true, "RawSyscall6": true, "RawSyscall9": true,
	"RawSyscallN": true, "RawSyscallNoError": true, "Read": true,
	"ReadAll": true, "Recvfrom": true, "Recvmsg": true, "Recvmmsg": true,
	"ReadAt": true, "ReadAtLeast": true, "ReadByte": true, "ReadFile": true,
	"ReadFrom": true, "ReadFull": true, "ReadLine": true, "ReadRune": true,
	"ReadString": true, "Readv": true, "Scan": true, "Scanf": true,
	"Scanln": true, "Seek": true, "Sendfile": true, "Splice": true,
	"Sendmmsg": true, "Sendmsg": true, "Sendto": true, "StartProcess": true,
	"Truncate": true,
	"Syscall":  true, "Syscall6": true, "Syscall9": true, "SyscallN": true,
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
	"Name": true, "Stat": true,
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
	pkgBindings    map[*types.Var]ast.Expr
	reassignedVars map[*types.Var]bool
	// localFuncInits records the declaration initializer of every local
	// function-typed variable (f := func... / var f = func...) keyed by
	// its object. A never-reassigned local whose initializer is a func
	// literal or a provably scanned function is itself scanned: the
	// scanned-callback fence can admit it as an argument to approved
	// module callees.
	localFuncInits map[*types.Var]ast.Expr
	// localReassigned marks local function-typed variables written after
	// their declaration (plain assignment, redefinition of an existing
	// name, address-taken store): their runtime value is no longer
	// provable from the declaration initializer.
	localReassigned map[*types.Var]bool
	pkgFuncLitBound map[*types.Var]bool
	// pkgFuncNonLitBound records package-scope function variables that
	// receive a non-literal value anywhere: a reassignment to an
	// unscanned callee, an address-taken store, or a loop rebind makes
	// the variable's runtime value unknowable, so the func-literal
	// exemption below no longer applies.
	pkgFuncNonLitBound map[*types.Var]bool
	// funcParams records the func-typed formal parameters of every
	// FuncDecl in the package; calls through them are approved inside
	// module-internal packages (the callback fence polices the call
	// sites).
	funcParams map[*types.Var]bool
	// storeInterfaces caches the approved store/codec interface values
	// (approvedStoreInterfaces), resolved lazily per scanned package.
	storeInterfaces []*types.Interface
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
	localFuncInits := map[*types.Var]ast.Expr{}
	localReassigned := map[*types.Var]bool{}
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
				} else if ok {
					localFuncInits[obj] = vs.Values[i]
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
						} else if obj, ok := info.Uses[id].(*types.Var); ok {
							// A local function variable written after
							// its declaration can no longer be proven to
							// hold the declaration initializer.
							localReassigned[obj] = true
						} else if d, ok := info.Defs[id].(*types.Var); ok && i < len(v.Rhs) {
							// f := func...: the declaration initializer
							// of a new local binding.
							localFuncInits[d] = v.Rhs[i]
						}
					}
				}
			case *ast.IncDecStmt:
				if id, ok := unparen(v.X).(*ast.Ident); ok {
					if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
						reassigned[obj] = true
						pkgFuncNonLitBound[obj] = true
					} else if obj, ok := info.Uses[id].(*types.Var); ok {
						localReassigned[obj] = true
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
				// Taking a variable's address permits a store through
				// that pointer (p := &f; *p = bytes.Clone), so the
				// initializer is no longer proof of the callee.
				if v.Op == token.AND {
					if id, ok := unparen(v.X).(*ast.Ident); ok {
						if obj, ok := info.Uses[id].(*types.Var); ok && obj.Parent() == pkg.Scope() {
							reassigned[obj] = true
							pkgFuncNonLitBound[obj] = true
						} else if obj, ok := info.Uses[id].(*types.Var); ok {
							localReassigned[obj] = true
						}
					}
				}
			}
			return true
		})
	}
	sorted := append([]*parsedFile{}, files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	return &packageCheck{pkg: pkg, info: info, fset: tc.fset, loader: tc.loader, files: sorted, pf: nil, varInits: varInits, pkgBindings: pkgBindings, reassignedVars: reassigned, localFuncInits: localFuncInits, localReassigned: localReassigned, pkgFuncLitBound: pkgFuncLitBound, pkgFuncNonLitBound: pkgFuncNonLitBound, funcParams: collectPkgFuncParams(info, sorted)}, nil
}

// fileRules carries one file's rule pass.
type fileRules struct {
	rep         *reporter
	pc          *packageCheck
	imports     map[string]string
	path        string
	exempts     map[token.Pos]bool
	pkgFuncVars map[types.Object]bool
	curFunc     *ast.FuncDecl // FuncDecl whose body is being walked
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
			w.curFunc = d
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

// approvedModuleInterfaces is the explicit set of module-internal
// interfaces whose dispatch is an approved indirection. The seven names
// are the writer's whole store/codec surface: tree.Store and
// RetiringStore (COW mutation), tree.LocalGap (gap-callback cells),
// bitmap.BitmapStore (free-page authority), tree.Codec (per-tree wire
// contract), writer.rangeFamily (per-family range codec over Codec),
// and writer.RangeStore (range value accounting over RetiringStore).
// Approval is by (package, name), never by declaration site: an
// interface declared in a module-internal package can be satisfied by an
// OUT-OF-MODULE type (stdlib or a future dependency) whose method body
// the gate cannot scan, so a general declaration-based approval would
// let a full mapped page launder into owned memory with no diagnostics.
// The named interfaces are safe to approve because no type outside the
// scanned source satisfies their method sets today: Codec, LocalGap and
// rangeFamily reference module-internal types (tree.Key, tree.LocalPrevious,
// LocalNext, rangeRecord), and the Store-family method names plus exact
// signatures exist nowhere in the standard library or x/sys. LocalGap
// dispatch is additionally bounded by construction: the tree core hands
// it exactly codec.LeafSize() cells (12/36 bytes for the range families),
// never a whole page - a future codec family whose leaf size approaches
// page size must re-verify this property. Any new
// interface must be added here together with its satisfier argument
// before its dispatch becomes approved; otherwise it keeps failing
// closed everywhere.
var approvedModuleInterfaces = []struct {
	path string
	name string
}{
	{"github.com/firehol/iprange/v4/go/internal/tree", "Codec"},
	{"github.com/firehol/iprange/v4/go/internal/tree", "Store"},
	{"github.com/firehol/iprange/v4/go/internal/tree", "RetiringStore"},
	{"github.com/firehol/iprange/v4/go/internal/tree", "LocalGap"},
	{"github.com/firehol/iprange/v4/go/internal/bitmap", "BitmapStore"},
	{"github.com/firehol/iprange/v4/go/internal/writer", "rangeFamily"},
	{"github.com/firehol/iprange/v4/go/internal/writer", "RangeStore"},
}

// isStoreCallbackImpl reports whether the enclosing function is an
// implementation of an approved store-callback method (Inspect/Update/
// CopyPage on tree.Store/RetiringStore/BitmapStore). The store contract
// hands the callback MAPPED page views; an implementation passing OWNED
// buffers would make the dispatch-site callback seeding bless copies of
// complete mapped pages into owned memory, so every invocation of the
// callback formal in such an implementation must receive a mapped view.
func (w *fileRules) isStoreCallbackImpl() bool {
	fd := w.curFunc
	if fd == nil || fd.Recv == nil || len(fd.Recv.List) == 0 || !storeCallbackMethod(fd.Name.Name) {
		return false
	}
	rt := w.pc.info.TypeOf(fd.Recv.List[0].Type)
	if rt == nil {
		return false
	}
	for _, iface := range w.pc.approvedStoreInterfaces() {
		if types.Implements(rt, iface) {
			return true
		}
	}
	return false
}

// approvedStoreInterfaces resolves the approved store/codec
// interfaces from the scanned module (approvedModuleInterfaces maps
// import path -> interface name); the resolution is cached per scanned
// package. An unresolvable name fails closed: the module cannot be
// type-checked without its own package set.
func (pc *packageCheck) approvedStoreInterfaces() []*types.Interface {
	if pc.storeInterfaces != nil {
		return pc.storeInterfaces
	}
	var out []*types.Interface
	for _, pair := range approvedModuleInterfaces {
		pkg, err := pc.loader.Import(pair.path)
		if err != nil {
			continue
		}
		tn, ok := pkg.Scope().Lookup(pair.name).(*types.TypeName)
		if !ok {
			continue
		}
		iface, ok := tn.Type().Underlying().(*types.Interface)
		if !ok {
			continue
		}
		out = append(out, iface)
	}
	pc.storeInterfaces = out
	return out
}

// approvedModuleInternalInterface reports whether t is one of the
// explicitly approved module-internal store/codec interfaces (see
// approvedModuleInterfaces). Every other interface - declared inside or
// outside the module - keeps failing closed as an unproven indirection:
// dispatching through it is approved by neither the callee rule nor the
// store-callback seeding.
func approvedModuleInternalInterface(t types.Type) bool {
	u := types.Unalias(t)
	named, ok := u.(*types.Named)
	if !ok {
		return false
	}
	if _, isIface := named.Underlying().(*types.Interface); !isIface {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	for _, pair := range approvedModuleInterfaces {
		if pair.path == obj.Pkg().Path() && pair.name == obj.Name() {
			return true
		}
	}
	return false
}

// moduleInternalInterface reports whether t is an interface type declared
// in a module-internal package. Such an interface can only receive
// implementations from inside the module (declaration is module-local),
// but its method set may still be satisfiable by an out-of-module type
// bound at a call site; this predicate therefore only drives FAIL-CLOSED
// checks (receiver/erasure data is concrete at the call site) and never
// approves a dispatch on its own. Dispatch approval is the narrower
// approvedModuleInternalInterface.
func moduleInternalInterface(t types.Type) bool {
	u := types.Unalias(t)
	named, ok := u.(*types.Named)
	if !ok {
		return false
	}
	if _, isIface := named.Underlying().(*types.Interface); !isIface {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	p := obj.Pkg().Path()
	return p == moduleInternalPrefix || strings.HasPrefix(p, moduleInternalPrefix+"/")
}

// moduleInternalPackage reports whether pkgPath is a module-internal
// package (all its code is scanned).
func moduleInternalPackage(pkgPath string) bool {
	return pkgPath == moduleInternalPrefix || strings.HasPrefix(pkgPath, moduleInternalPrefix+"/")
}

func (w *fileRules) typeOf(e ast.Expr) types.Type {
	tv, ok := w.pc.info.Types[e]
	if !ok {
		return nil
	}
	return tv.Type
}

// approvedGenericCallee approves a generic instantiation whose generic
// function is a scanned module function: same-package or
// module-internal, exactly like the direct function spelling.
func (w *fileRules) approvedGenericCallee(x ast.Expr) bool {
	fn := callCalleeFuncOrVar(w, x)
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	p := fn.Pkg().Path()
	return p == w.pc.pkg.Path() || moduleInternalPackage(p)
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
			// provably binds a scanned function. A func-typed FORMAL
			// PARAMETER of the current function is the one exception:
			// every caller of the function is scanned (the package is
			// module-internal), and the call-site callback fence
			// requires the argument to be a scanned callback, so the
			// chain bottoms out in a policed body.
			if w.approvedFuncParamVar(o) {
				return true
			}
			// A func-typed range variable over a callback container
			// (for _, cb := range cbs { cb(v) }) is an element of the
			// container, whose call sites bind scanned callbacks: the
			// loop value is a scanned callee exactly like the formal
			// itself, so calls through it are approved transfers.
			if w.rangeVarHoldsCallback(o) {
				return true
			}
			return w.approvedFuncVar(o, 0)
		}
		return false
	case *ast.IndexExpr:
		if w.approvedGenericCallee(f.X) {
			return true
		}
		// An indexed element of a func-typed FORMAL container
		// (cbs[i](v) with cbs ...func(page []byte) error) is a scanned
		// callback: the container's call sites bind scanned callbacks,
		// so the element call is an approved transfer like the formal
		// itself.
		return w.indexCalleeOverFuncFormal(f)
	case *ast.IndexListExpr:
		if w.approvedGenericCallee(f.X) {
			return true
		}
		return w.indexCalleeOverFuncFormal(f)
	case *ast.SelectorExpr:
		obj := w.pc.info.Uses[f.Sel]
		fn, ok := obj.(*types.Func)
		if !ok {
			return false
		}
		if pkg := fn.Pkg(); pkg != nil {
			p := pkg.Path()
			if p == w.pc.pkg.Path() || moduleInternalPackage(p) || p == xsysImport {
				if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
					// Only the explicitly approved store/codec
					// interfaces dispatch as approved callees: their
					// method sets have no out-of-module satisfier in
					// the current dependency graph (see
					// approvedModuleInterfaces). Any other interface,
					// module-declared or external, is an unproven
					// indirection: its concrete method body could
					// launder a mapped view into owned memory without
					// the scan seeing it, so such calls fail closed.
					// The receiver/argument checks below still fail
					// approved dispatches when the receiver or an
					// argument CONCRETELY carries a full mapped page
					// (field promotion or a direct binding), because
					// the concrete implementation is erased at the call
					// site and its own conversion rules cannot see the
					// receiver's data.
					return approvedModuleInternalInterface(sig.Recv().Type())
				}
				return true
			}
		}
	}
	return false
}

// pkgFuncParams records every func-typed formal parameter of the
// package's FuncDecls (methods included). Calls through such parameters
// are approved inside module-internal packages: the callee chain is
// caller-supplied, and the call-site callback fence requires every
// argument bound to a func-typed formal to be a scanned callback.
func collectPkgFuncParams(info *types.Info, files []*parsedFile) map[*types.Var]bool {
	out := map[*types.Var]bool{}
	for _, pf := range files {
		for _, decl := range pf.ast.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Type.Params == nil {
				continue
			}
			for _, f := range fd.Type.Params.List {
				if funcSignature(info.TypeOf(f.Type)) == nil {
					continue
				}
				for _, name := range f.Names {
					if obj, ok := info.Defs[name].(*types.Var); ok {
						out[obj] = true
					}
				}
			}
		}
	}
	return out
}

// approvedFuncParamVar reports whether a func-typed variable is a formal
// parameter of a function in this module-internal package. The current
// package must be module-internal: then every caller of the declaring
// function is scanned, and the callback fence at the call sites
// guarantees the parameter only ever receives a scanned callback.
func (w *fileRules) approvedFuncParamVar(v *types.Var) bool {
	if v == nil || !moduleInternalPackage(w.pc.pkg.Path()) || w.pc.funcParams == nil {
		return false
	}
	if !w.pc.funcParams[v] {
		return false
	}
	return funcSignature(v.Type()) != nil
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
	case *ast.TypeSwitchStmt:
		w.checkTypeSwitch(v)
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
	var sig *types.Signature
	if s, ok := fn.Type().(*types.Signature); ok {
		sig = s
		if sig.Recv() != nil {
			key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
			receiverSlot = true
		}
	}
	fs, ok := sums[key]
	if !ok || (len(fs.stringParams) == 0 && len(fs.fmtSpreadParams) == 0) {
		return
	}
	// Method summaries count the receiver as parameter slot 0. A method
	// VALUE call carries the receiver as the selector expression; a
	// method-EXPRESSION call (or an alias of one) carries it as the
	// first explicit argument. A method value stored in a local
	// (get := b.String; get()) carries the receiver as the binding's
	// capture, resolved by the flow pass into callMethodValues.
	recvOffset := 0
	var recvExpr ast.Expr
	if mvr, ok := w.methodValueCallee(v); ok {
		recvExpr = mvr.recv
	} else if sel, ok := unparen(v.Fun).(*ast.SelectorExpr); ok {
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
	// A variadic parameter slot receives EVERY trailing argument: the
	// summary records one union slot for the whole element collection,
	// so each trailing argument must be checked against the conversion
	// (sink([]byte{1}, page) with sink(xs ...[]byte) converts xs[1]).
	variadicSlot := -1
	if sig != nil && sig.Variadic() && sig.Params().Len() > 0 {
		variadicSlot = sig.Params().Len() - 1
		if receiverSlot {
			variadicSlot++
		}
	}
	slotArgs := func(pi int) []ast.Expr {
		if pi == variadicSlot {
			var out []ast.Expr
			for ai := pi - recvOffset; ai < len(v.Args); ai++ {
				out = append(out, v.Args[ai])
			}
			return out
		}
		if arg, ok := argAt(pi); ok {
			return []ast.Expr{arg}
		}
		return nil
	}
	for pi := range fs.stringParams {
		for _, arg := range slotArgs(pi) {
			pv := w.pageValue(arg)
			if pv.tainted && pageFull(pv) {
				w.fail(v.Pos(), "mapped page view passed to %s: parameter %d is converted to an owned string inside the callee", calleeText(v.Fun), pi+1)
			}
		}
	}
	for pi := range fs.fmtSpreadParams {
		for _, arg := range slotArgs(pi) {
			if pv := w.pageValue(arg); pv.tainted && pageFull(pv) {
				w.fail(v.Pos(), "mapped page view passed to %s: parameter %d is spread into fmt inside the callee (complete page into owned memory)", calleeText(v.Fun), pi+1)
			}
		}
	}
}

// checkParamCopyCalls fails module callee call sites that bind an owned
// destination and a mapped full-page source to a callee parameter pair
// the callee copies between (copy(paramD[..], paramS[..]) recorded in
// its summary). The definition site cannot decide: both sides are
// caller-bound. The recorded pairs compose through call chains, so the
// owned/mapped decision is enforced at the binding site where both
// values are concrete.
func (w *fileRules) checkParamCopyCalls(v *ast.CallExpr, fn *types.Func) {
	if w.pc.pf == nil || w.pc.pf.store == nil || fn == nil || fn.Pkg() == nil {
		return
	}
	pkgPath := fn.Pkg().Path()
	sums := w.pc.pf.summaries
	if pkgPath != w.pc.pkg.Path() {
		sums = w.pc.pf.store.pkgs[pkgPath]
	}
	if sums == nil {
		return
	}
	key := fn.Name()
	receiverSlot := false
	var sig *types.Signature
	if s, ok := fn.Type().(*types.Signature); ok {
		sig = s
		if sig.Recv() != nil {
			key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
			receiverSlot = true
		}
	}
	fs, ok := sums[key]
	if !ok || len(fs.copyParams) == 0 {
		return
	}
	recvOffset := 0
	var recvExpr ast.Expr
	if mvr, ok := w.methodValueCallee(v); ok {
		recvExpr = mvr.recv
	} else if sel, ok := unparen(v.Fun).(*ast.SelectorExpr); ok {
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
	for d, srcs := range fs.copyParams {
		dstArg, dok := argAt(d)
		if !dok {
			continue
		}
		// "Owned" means not page-tainted at all: a mapped destination, a
		// page-tainted local, or a parameter-sourced buffer is decided
		// elsewhere in the chain; a clean local buffer is owned.
		dpv := w.pageValue(dstArg)
		if dpv.tainted {
			continue
		}
		for _, s := range srcs {
			srcArg, sok := argAt(s)
			if !sok {
				continue
			}
			spv := w.pageValue(srcArg)
			if !spv.tainted {
				continue
			}
			if pageFull(spv) {
				w.fail(v.Pos(), "copy of a mapped page view into an owned buffer through %s (complete page)", calleeText(v.Fun))
				continue
			}
			if spv.maxLen > 0 && spv.maxLen < pageSize {
				// A bounded span through a copy-parameter helper into an
				// owned destination: two calls assembling disjoint halves
				// into ONE caller buffer copy a complete page, exactly
				// like bounded copies in the calling function.
				if obj, path := w.boundedCopyKey(dstArg); obj != nil {
					w.accumulateBoundedSpan(obj, path, spv.maxLen, v.Pos(), "bounded mapped-page spans copied through "+calleeText(v.Fun)+" into one owned buffer (complete page)")
				}
			}
		}
	}
}

// checkCallbackInvokeCalls fails module callee call sites where an
// enclosing store implementation forwards its callback formal into a
// callee func-typed parameter the callee invokes. The store contract
// hands the callback mapped page views; the callee's summary records
// which of its parameters flow into the callback, and each of those
// arguments must be a mapped view at this call site. Invocations the
// callee cannot trace to a parameter fail closed: the views are not
// provably the call site's mapped views, so blessing them would launder
// complete pages into owned memory exactly like the direct-invocation
// counter-check.
func (w *fileRules) checkCallbackInvokeCalls(v *ast.CallExpr, fn *types.Func) {
	if w.pc.pf == nil || w.pc.pf.store == nil || fn == nil || fn.Pkg() == nil || !w.isStoreCallbackImpl() {
		return
	}
	pkgPath := fn.Pkg().Path()
	sums := w.pc.pf.summaries
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
	if !ok || (len(fs.callbackInvokes) == 0 && len(fs.callbackInvokesInternal) == 0 &&
		len(fs.fieldInvokes) == 0 && len(fs.fieldInvokesInternal) == 0) {
		return
	}
	recvOffset := 0
	var recvExpr ast.Expr
	if mvr, ok := w.methodValueCallee(v); ok {
		recvExpr = mvr.recv
	} else if sel, ok := unparen(v.Fun).(*ast.SelectorExpr); ok {
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
	// The forwarded value must be the enclosing store implementation's
	// own callback formal, directly or through a local alias recorded
	// by the flow pass (cb := fn, or cb := func(a, b []byte) error {
	// return fn(a, b) }): only then does the store contract apply to
	// the views the callee hands it. A wrapper literal forwards only
	// the closure parameter positions recorded in its alias; identity
	// aliases forward every position.
	fail := func(fnArg ast.Expr) {
		w.fail(v.Pos(), "store callback %s must receive mapped page views through %s (an owned callback buffer launders complete pages into owned memory)", calleeText(fnArg), calleeText(v.Fun))
	}
	positionForwarded := func(al *callbackAlias, i int) bool {
		if al == nil || al.forwarded == nil {
			return true
		}
		return i < len(al.forwarded) && al.forwarded[i]
	}
	for fnSlot, byteSlots := range fs.callbackInvokes {
		if fnSlot < 0 {
			// paramAliases -1: the callee invoked a callback VALUE it
			// could not attribute to a named parameter (a method value
			// through an identity passthrough, an unresolved local).
			// The value may be this store's own callback formal, so the
			// views handed to it must be mapped views at this call site;
			// an owned buffer would launder a complete page into owned
			// memory through an invocation the scan cannot trace. The
			// internal marker still applies: a byte view the callee
			// could not trace to a parameter (a locally minted owned
			// buffer) must fail closed exactly like the positive-slot
			// branch.
			if fs.callbackInvokesInternal[fnSlot] {
				w.fail(v.Pos(), "store callback forwarded through %s is invoked inside it with buffers the scan cannot prove mapped (complete pages into owned memory)", calleeText(v.Fun))
				continue
			}
			for _, bs := range byteSlots {
				ba, ok := argAt(bs)
				if !ok {
					continue
				}
				if pv := w.pageValue(ba); !pv.mapped && !w.compositeCarrierMapped(ba) {
					w.fail(v.Pos(), "store callback forwarded through %s is invoked inside it with buffers the scan cannot prove mapped (complete pages into owned memory)", calleeText(v.Fun))
					break
				}
			}
			continue
		}
		fnArg, ok := argAt(fnSlot)
		if !ok {
			continue
		}
		// A method value of a carrier (h := car{cb: fn};
		// runCb(h.run, ...)) forwards the callback the same way a plain
		// forward does: the method body invokes the bound field with
		// its own parameters, so the byte arguments at THIS call site
		// must be mapped views. An internal method body (it mints the
		// views itself) fails closed.
		mvForwards, mvInternal := w.methodValueCarriesCallback(fnArg)
		var al *callbackAlias
		if !w.forwardedCallback(fnArg) && !mvForwards {
			// A struct-field argument (runCb(s.cb, x, y)) whose field
			// key a store implementation bound to its callback formal
			// (s.cb = fn), a call-result argument (runCb(s.cbGet(), x,
			// y)) whose getter returns the carrier field or
			// identity-returns its own func-typed parameter, and a
			// local alias all forward the formal exactly like a direct
			// pass: the byte arguments at this call site must be
			// mapped views. The binding record is the module-wide
			// carrier record, so a field bound in another store method
			// of the same module is followed too; an unbound field or
			// an un-attributable call result is not a store callback
			// and stays outside the contract.
			if ok, al2 := w.callbackArgAlias(fnArg, 0); ok {
				al = al2
			} else {
				a, aok := w.aliasCallback(fnArg)
				if !aok {
					continue
				}
				al = &a
			}
		}
		if fs.callbackInvokesInternal[fnSlot] || mvInternal {
			w.fail(v.Pos(), "store callback forwarded through %s is invoked inside it with buffers the scan cannot prove mapped (complete pages into owned memory)", calleeText(v.Fun))
			continue
		}
		for i, bs := range byteSlots {
			if !positionForwarded(al, i) {
				continue
			}
			ba, ok := argAt(bs)
			if !ok {
				continue
			}
			if pv := w.pageValue(ba); !pv.mapped && !w.compositeCarrierMapped(ba) {
				fail(fnArg)
				break
			}
		}
	}
	for fnSlot := range fs.callbackInvokesInternal {
		if _, traced := fs.callbackInvokes[fnSlot]; traced {
			continue
		}
		if fnSlot < 0 {
			// Negative-only internal record: the callee invoked an
			// un-attributable callback value entirely with views it
			// could not trace to a parameter. argAt can never resolve
			// the callback argument, so the store call site fails
			// closed instead of dropping the record.
			w.fail(v.Pos(), "store callback forwarded through %s is invoked inside it with buffers the scan cannot prove mapped (complete pages into owned memory)", calleeText(v.Fun))
			continue
		}
		fnArg, ok := argAt(fnSlot)
		if !ok {
			continue
		}
		if !w.forwardedCallback(fnArg) {
			if _, aok := w.aliasCallback(fnArg); !aok {
				continue
			}
		}
		w.fail(v.Pos(), "store callback forwarded through %s is invoked inside it with buffers the scan cannot prove mapped (complete pages into owned memory)", calleeText(v.Fun))
	}
	// Struct-field carriers: the callee invokes a struct field with
	// byte-slice arguments (h.cb(a, b)); when the caller binds that
	// canonical field key to its own store callback formal (a direct
	// record in the caller's summary), the byte arguments at this call
	// site must be mapped views, exactly like a func-typed forward.
	// Forwarded records (this call site is a helper in the middle of a
	// chain) defer to the store-implementation call site through the
	// composition. Only store-implementation callers carry the callback
	// formal, so the fence runs inside the existing isStoreCallbackImpl
	// guard of this function.
	for fk, byteSlots := range fs.fieldInvokes {
		if fs.fieldInvokesInternal[fk] {
			w.fail(v.Pos(), "store callback forwarded through %s is invoked inside it with buffers the scan cannot prove mapped (complete pages into owned memory)", calleeText(v.Fun))
			continue
		}
		encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
		if !ok {
			continue
		}
		recs := encl.fieldAliases[fk]
		direct := recs[:0]
		for _, r := range recs {
			if !r.forwarded {
				direct = append(direct, r)
			}
		}
		if len(direct) == 0 {
			// The current implementation never bound this carrier key
			// (or only forwards it), so no sanctioned callback exists
			// for the field callee inside the callee's body: a CONCRETE
			// mapped view must not enter that unprovable field body.
			// Owned buffers and parameter-sourced views stay legal
			// (owned buffers cannot launder complete pages, and
			// parameters are decided by the store call sites upward).
			for _, bs := range byteSlots {
				ba, ok := argAt(bs)
				if !ok {
					continue
				}
				if w.paramOf(ba) {
					continue
				}
				if pv := w.pageValue(ba); pv.tainted && pageFull(pv) {
					w.fail(v.Pos(), "mapped page view passed to a struct-field callee through %s (complete page into owned memory)", calleeText(v.Fun))
					break
				}
			}
			continue
		}
		// The callee signature decides which arguments are byte slots
		// and which parameter carries the struct; the records use the
		// summary layout, receiver at slot 0. A nested carrier record
		// matches a parameter declared as the OUTER carrier type (the
		// path's first step); a flat record matches the leaf key type.
		// Every direct record for the key is tried: the same key bound
		// flat AND nested keeps both enforcements alive.
		sig, _ := fn.Type().(*types.Signature)
		if sig == nil {
			continue
		}
		typeAt := func(slot int) types.Type {
			if sig.Recv() != nil {
				if slot == 0 {
					return sig.Recv().Type()
				}
				if slot-1 < sig.Params().Len() {
					return sig.Params().At(slot - 1).Type()
				}
				return nil
			}
			if slot < sig.Params().Len() {
				return sig.Params().At(slot).Type()
			}
			return nil
		}
		n := sig.Params().Len()
		if sig.Recv() != nil {
			n++
		}
		for _, rec := range direct {
			want := fk.typ
			if rec.path != "" {
				if i := strings.IndexByte(rec.path, '.'); i > 0 {
					want = rec.path[:i]
				}
			}
			for slot := 0; slot < n; slot++ {
				pt := typeAt(slot)
				if pt == nil || w.pc.pf.canonFieldType(pt) != want {
					continue
				}
				carrierArg, ok := argAt(slot)
				if !ok {
					continue
				}
				for _, bs := range byteSlots {
					ba, ok := argAt(bs)
					if !ok {
						continue
					}
					if pv := w.pageValue(ba); !pv.mapped && !w.compositeCarrierMapped(ba) {
						w.fail(v.Pos(), "store callback %s must receive mapped page views through %s (an owned callback buffer launders complete pages into owned memory)", w.carrierText(carrierArg), calleeText(v.Fun))
						break
					}
				}
				break
			}
		}
	}
}

// forwardedCallback reports whether a func-typed argument is the
// enclosing store implementation's own callback formal, directly or
// assembled into a func-container composite literal ([]func{fn, fn}):
// an element of the literal is the formal, so the byte views at this
// call site must be mapped exactly like a direct forward.
func (w *fileRules) forwardedCallback(e ast.Expr) bool {
	if lit, isLit := unparen(e).(*ast.CompositeLit); isLit && elemFuncType(w.typeOf(lit)) {
		for _, el := range lit.Elts {
			if w.callbackSlotOf(el) {
				return true
			}
		}
		return false
	}
	id, ok := unparen(e).(*ast.Ident)
	if !ok {
		return false
	}
	obj, ok := w.pc.info.Uses[id].(*types.Var)
	return ok && w.approvedFuncParamVar(obj)
}

// aliasCallback resolves a local func-typed argument through the
// enclosing store implementation's recorded aliases: the local must
// wrap the implementation's own callback formal for the fence to
// follow it.
func (w *fileRules) aliasCallback(e ast.Expr) (callbackAlias, bool) {
	id, ok := unparen(e).(*ast.Ident)
	if !ok {
		return callbackAlias{}, false
	}
	obj, ok := w.pc.info.Uses[id].(*types.Var)
	if !ok || w.pc.pf == nil || w.curFunc == nil {
		return callbackAlias{}, false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return callbackAlias{}, false
	}
	if al, ok := encl.callbackAliases[obj]; ok {
		return al, ok
	}
	// An alias recorded only in paramAliases survives instability
	// (reassignment, branch install, address taken): the value may
	// still hold the formal, so the invocation fence follows it
	// fail-closed (-1 marks a proven-formal-returning call result
	// whose argument could not be attributed).
	if ps, ok := encl.paramAliases[obj]; ok {
		return callbackAlias{slot: ps, forwarded: nil}, true
	}
	return callbackAlias{}, false
}

// callbackArgAlias resolves a func-typed argument of a store-callback
// forward to its alias marker, following direct formals, recorded
// aliases, carrier fields, and call results that return them: a getter
// returning the carrier field (cbGet() returning s.cb) forwards the
// stored callback exactly like the direct selector spelling, and an
// identity getter returning one of its own func-typed parameters
// (fwd(fn) returning f) forwards that argument position. A result the
// scan cannot attribute stays outside the contract: the argument is
// not provably the store's callback formal.
func (w *fileRules) callbackArgAlias(e ast.Expr, depth int) (bool, *callbackAlias) {
	if depth > 2 {
		return false, nil
	}
	switch a := unparen(e).(type) {
	case *ast.Ident:
		if w.forwardedCallback(a) {
			return true, nil
		}
		al, ok := w.aliasCallback(a)
		return ok, &al
	case *ast.SelectorExpr:
		if w.pc.pf != nil {
			if key, kk := w.pc.pf.fieldSlotKeyOf(w.pc.info, a); kk && w.moduleFieldCarrier(key) {
				return true, &callbackAlias{slot: -2, forwarded: nil}
			}
		}
		return false, nil
	case *ast.CallExpr:
		if w.pc.pf == nil {
			return false, nil
		}
		fs := w.calleeSummary(a.Fun)
		if fs == nil {
			return false, nil
		}
		if p, ok := fs.returnSlotAliases[0]; ok {
			if p == -2 {
				// Different branches return different parameters: the
				// result is one of them, so the fence follows it
				// fail-closed.
				return true, &callbackAlias{slot: -2, forwarded: nil}
			}
			if p < 0 || p >= len(a.Args) {
				return false, nil
			}
			return w.callbackArgAlias(a.Args[p], depth+1)
		}
		if fk, ok := fs.returnFieldKeys[0]; ok && fk != multiReturnKey && w.moduleFieldCarrier(fk) {
			return true, &callbackAlias{slot: -2, forwarded: nil}
		}
		return false, nil
	}
	return false, nil
}

// paramOf reports whether e names a []byte-typed parameter of the
// enclosing function (receiver included), whose mappedness is decided
// by the store call sites through the composition rather than by a
// concrete local value here.
func (w *fileRules) paramOf(e ast.Expr) bool {
	id, ok := unparen(e).(*ast.Ident)
	if !ok || w.curFunc == nil {
		return false
	}
	obj, ok := w.pc.info.Uses[id].(*types.Var)
	if !ok || !paramCanCarryPage(obj.Type()) {
		return false
	}
	for _, f := range w.curFunc.Type.Params.List {
		for _, name := range f.Names {
			if w.pc.info.ObjectOf(name) == obj {
				return true
			}
		}
	}
	return false
}

// methodValueCarriesCallback reports whether a func-typed call argument
// is a method value or method expression whose body invokes a struct
// field the enclosing store implementation bound to its callback
// formal (h := car{cb: fn}; runCb(h.run, ...) and
// runCbME((*car).run, h, ...): the method forwards the callback, so
// the byte arguments at the call site must be mapped views). A local
// initialized to the method value (mv := h.run) resolves through its
// single definition, address-taken included (the value may still be
// the method). The second result reports that the method invokes the
// bound field with buffers its own body mints, which fails closed at
// the call site.
func (w *fileRules) methodValueCarriesCallback(e ast.Expr) (bool, bool) {
	if w.pc.pf == nil || w.curFunc == nil {
		return false, false
	}
	if id, isID := unparen(e).(*ast.Ident); isID {
		obj, ok := w.pc.info.Uses[id].(*types.Var)
		if !ok || w.curFunc.Body == nil {
			return false, false
		}
		init, single, taken := varDefOf(w.pc.info, w.curFunc.Body, obj)
		if init == nil || (!single && !taken) {
			return false, false
		}
		return w.methodValueCarriesCallback(init)
	}
	sel, ok := unparen(e).(*ast.SelectorExpr)
	if !ok {
		return false, false
	}
	recv, isSel := w.pc.info.Selections[sel]
	if !isSel || (recv.Kind() != types.MethodVal && recv.Kind() != types.MethodExpr) {
		return false, false
	}
	mfn, ok := w.pc.info.Uses[sel.Sel].(*types.Func)
	if !ok || mfn.Pkg() == nil {
		return false, false
	}
	p := mfn.Pkg().Path()
	if p != w.pc.pkg.Path() && !moduleInternalPackage(p) {
		return false, false
	}
	msum := w.calleeSummary(sel)
	if msum == nil {
		return false, false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return false, false
	}
	internal := false
	for fk := range msum.fieldInvokes {
		if msum.fieldInvokesInternal[fk] {
			internal = true
			continue
		}
		for _, r := range encl.fieldAliases[fk] {
			if !r.forwarded {
				return true, internal
			}
		}
	}
	// The method value forwards a field no key of which the current
	// function bound directly: the callback decision belongs to the
	// store call sites bound-carrier fence, not here.
	return false, false
}

// checkCarrierViewCallSites enforces the carrier mapped-view fence at
// NON-store-implementation call sites: when a scanned callee's summary
// records a struct-field callback invocation (fs.fieldInvokes) and a
// byte argument at THIS call site is a concrete mapped view (a local
// mint, not a current-function parameter), the mapped page enters an
// unprovable field callee through the carrier chain. Store
// implementations keep their own call-site enforcement (the direct
// carrier record decides whether the views may reach the callback);
// anywhere else the carrier key's existence in an unrelated store
// implementation must not weaken the generic fence (Sartre P2-2).
// Parameter-sourced arguments are skipped: their mappedness is decided
// by the composition at the store-implementation call sites.
func (w *fileRules) checkCarrierViewCallSites(v *ast.CallExpr, fn *types.Func) {
	if w.pc.pf == nil || w.pc.pf.store == nil || fn == nil || fn.Pkg() == nil || w.isStoreCallbackImpl() {
		return
	}
	sums := w.pc.pf.summaries
	if fn.Pkg().Path() != w.pc.pkg.Path() {
		sums = w.pc.pf.store.pkgs[fn.Pkg().Path()]
	}
	if sums == nil {
		return
	}
	key := fn.Name()
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
	}
	fs, ok := sums[key]
	if !ok {
		return
	}
	if len(fs.fieldInvokes) == 0 && len(fs.paramFieldInvokes) == 0 {
		return
	}
	recvExpr := ast.Expr(nil)
	if sel, ok := unparen(v.Fun).(*ast.SelectorExpr); ok {
		if selRecv, isSel := w.pc.info.Selections[sel]; isSel && selRecv.Kind() == types.MethodVal {
			recvExpr = sel.X
		}
	}
	argAt := func(slot int) (ast.Expr, bool) {
		// Callee summary layout: the receiver occupies slot 0 of a
		// method. A method-VALUE call binds the receiver expression
		// separately and shifts explicit arguments by one; a method
		// EXPRESSION or a free function maps summary slots straight
		// onto the call arguments.
		if recvExpr != nil && slot == 0 {
			return recvExpr, true
		}
		ai := slot
		if recvExpr != nil {
			ai = slot - 1
		}
		if ai < 0 || ai >= len(v.Args) {
			return nil, false
		}
		return v.Args[ai], true
	}
	paramOf := func(e ast.Expr) bool {
		id, ok := unparen(e).(*ast.Ident)
		if !ok || w.curFunc == nil {
			return false
		}
		obj, ok := w.pc.info.Uses[id].(*types.Var)
		if !ok || !paramCanCarryPage(obj.Type()) {
			return false
		}
		for _, f := range w.curFunc.Type.Params.List {
			for _, name := range f.Names {
				if w.pc.info.ObjectOf(name) == obj {
					return true
				}
			}
		}
		return false
	}
	argMapped := func(ba ast.Expr) bool {
		if paramOf(ba) {
			return false
		}
		pv := w.pageValue(ba)
		if pv.tainted && pageFull(pv) {
			w.fail(v.Pos(), "mapped page view passed to a struct-field callee through %s (complete page into owned memory)", calleeText(v.Fun))
			return true
		}
		return false
	}
	for fk, byteSlots := range fs.fieldInvokes {
		if fs.fieldInvokesInternal[fk] {
			continue
		}
		for _, bs := range byteSlots {
			ba, ok := argAt(bs)
			if !ok {
				continue
			}
			if argMapped(ba) {
				return
			}
		}
	}
	// Cascade records (paramFieldInvokes): a parameter of the callee
	// reaches a struct-field callee inside it; when a CONCRETE mapped
	// view is bound to that parameter at this call site, the mapped
	// page enters the unprovable field callee with no direct carrier
	// record anywhere to enforce at. Parameter-sourced arguments skip
	// and cascade to the next call level.
	for p := range fs.paramFieldInvokes {
		ba, ok := argAt(p)
		if !ok {
			continue
		}
		if argMapped(ba) {
			return
		}
	}
}

// checkStoreCallbackViews enforces the store-callback counter-check at
// an invocation point inside a store callback implementation
// (Inspect/Update/CopyPage on an approved store interface): every
// byte-slice argument of the invocation must be a provably MAPPED view.
// The callback formal receives views seeded as mapped at the dispatch
// site; an owned buffer bound here would make the seeding bless copies
// of complete mapped pages into owned memory. The callee itself can be
// the formal, a local identity alias (cb := fn, any block), a recorded
// wrapper, a struct field that holds the formal (s.cb = fn), or a
// forwarder literal that receives the formal as a func-typed argument.
// storeCallbackCallee reports whether a call inside a store callback
// implementation invokes the sanctioned store callback itself: the
// callback formal, a recorded alias/return-wrapper of it, a carrier
// field, an indexed slot, an asserted holder, or a call-result wrapper.
// Such invocations REQUIRE mapped views (checkStoreCallbackViews
// counter-checks owned buffers), so handing the mapped page to them is
// the store contract, not an unproven transfer: the generic
// fail-closed fence must not double-flag the honest twins while the
// owned-view direction stays policed.
func (w *fileRules) storeCallbackCallee(v *ast.CallExpr) bool {
	if !w.isStoreCallbackImpl() {
		return false
	}
	switch f := unparen(v.Fun).(type) {
	case *ast.Ident:
		obj, ok := w.pc.info.Uses[f].(*types.Var)
		if !ok {
			return false
		}
		return w.approvedFuncParamVar(obj) || w.paramAliasedFuncVar(obj, 0) ||
			w.recordedCallbackAlias(obj) || w.formalAliasedLocal(obj) || w.localMethodCarrier(obj) || w.forwardsCallbackFormal(v)
	case *ast.SelectorExpr:
		// Only a field the current implementation PROVABLY bound to
		// its callback formal is sanctioned: a bare func-typed field
		// (hook, decorator, never-assigned handler) has an unprovable
		// body, and a mapped page must not enter it (the P336 class).
		return w.fieldHoldsCallbackFormal(f)
	case *ast.IndexExpr, *ast.IndexListExpr:
		return w.indexHoldsCallbackFormal(f)
	case *ast.TypeAssertExpr:
		return w.callbackSlotOf(f)
	case *ast.CallExpr:
		return w.callResultHoldsCallbackFormal(f)
	}
	return false
}

func (w *fileRules) checkStoreCallbackViews(v *ast.CallExpr, fun ast.Expr) {
	sig := funcSignature(w.typeOf(fun))
	if sig == nil || sig.Params() == nil {
		return
	}
	params := sig.Params()
	for i := 0; i < params.Len() && i < len(v.Args); i++ {
		if _, isSlice := types.Unalias(params.At(i).Type()).(*types.Slice); isSlice {
			if pv := w.pageValue(v.Args[i]); !pv.mapped && !w.compositeCarrierMapped(v.Args[i]) {
				w.fail(v.Pos(), "store callback %s must receive a mapped page view (an owned callback buffer launders complete pages into owned memory)", calleeText(fun))
			}
		}
	}
}

// recordedCallbackAlias reports whether a local function-typed variable
// is a callback alias recorded by the flow pass for the current
// function: cb := fn, cb := func(a, b []byte) error { return fn(a, b) },
// chains of both, and block-scoped or var-declared forms of either.
func (w *fileRules) recordedCallbackAlias(v *types.Var) bool {
	if v == nil || w.pc.pf == nil || w.curFunc == nil {
		return false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return false
	}
	_, ok = encl.callbackAliases[v]
	return ok
}

// localMethodCarrier reports whether a func-typed local was bound to a
// method VALUE or method EXPRESSION of a carrier type (mv := h.run,
// m := (*car).run): the method body invokes a field the enclosing
// store implementation bound to its callback formal, so invoking the
// local with byte views must demand mapped views exactly like
// invoking the method directly.
func (w *fileRules) localMethodCarrier(v *types.Var) bool {
	if v == nil || w.curFunc == nil || w.curFunc.Body == nil {
		return false
	}
	init, single, taken := varDefOf(w.pc.info, w.curFunc.Body, v)
	if init == nil || !single || taken {
		return false
	}
	fw, _ := w.methodValueCarriesCallback(init)
	return fw
}

// formalAliasedLocal reports whether a func-typed local of the current
// function was recorded by the flow pass as a may-be alias of the
// store callback formal (paramAliases: identity aliases, wrapper
// literals, field/index carriers, and call results of formal-returning
// helpers). The record survives instability (reassignment, branch
// install, address taken), so the invocation fences fail closed on
// un-mapped views instead of dropping the chain; -1 marks a call
// result of a proven formal-returning callee whose own argument could
// not be attributed.
func (w *fileRules) formalAliasedLocal(v *types.Var) bool {
	if v == nil || w.pc.pf == nil || w.curFunc == nil {
		return false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return false
	}
	_, ok = encl.paramAliases[v]
	return ok
}

// fieldHoldsCallbackFormal reports whether a field selection is a plain
// struct field that the current function assigned the store callback
// formal (s.cb = fn), keyed by the canonical struct type and field name
// so a caller's anonymous struct carrier matches a helper's named
// parameter with the same fields. Forwarded records (the field arrived
// as a function parameter, not an assignment of this body) are not
// direct local holders and resolve to false, exactly like the flow-side
// slotOfExpr; method values and interface dispatches are not storage
// slots either.
func (w *fileRules) fieldHoldsCallbackFormal(sel *ast.SelectorExpr) bool {
	if w.pc.pf == nil || w.curFunc == nil {
		return false
	}
	key, ok := w.pc.pf.fieldSlotKeyOf(w.pc.info, sel)
	if !ok {
		return false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return false
	}
	for _, r := range encl.fieldAliases[key] {
		if !r.forwarded {
			return true
		}
	}
	return false
}

// moduleFieldCarrier returns the canonical field key of e (a plain
// field selection or an assertion chain over one) when the key is
// recorded as a DIRECT callback-formal carrier in some
// store-implementation summary of the scanned module. Only direct
// records count: a forwarded (helper-carried) record means the key
// flows through a chain whose enforcement happens at the store call
// site, and the generic unprovable-callee fence must not weaken itself
// on such keys unless the store site will enforce them.
func (w *fileRules) moduleFieldCarrier(key fieldSlotKey) bool {
	if w.pc.pf == nil || w.pc.pf.store == nil {
		return false
	}
	seen := map[string]bool{}
	check := func(sums map[string]*funcSummary) bool {
		for k, fs := range sums {
			if seen[k] {
				continue
			}
			seen[k] = true
			name := k
			if i := strings.LastIndexByte(k, '.'); i >= 0 {
				name = k[i+1:]
			}
			if !storeCallbackMethod(name) {
				continue
			}
			for _, r := range fs.fieldAliases[key] {
				if !r.forwarded {
					return true
				}
			}
		}
		return false
	}
	if check(w.pc.pf.summaries) {
		return true
	}
	for _, sums := range w.pc.pf.store.pkgs {
		if check(sums) {
			return true
		}
	}
	return false
}

// storeCarrierTracedFieldCall reports whether an unprovable
// struct-field callee is a recorded store-callback carrier invocation:
// the current function traced the call into fs.fieldInvokes, the
// canonical key is a direct carrier of some store implementation in the
// module, and every byte argument at this call is parameter-sourced
// (its mappedness is decided by the store call sites through the
// composition, not by a concrete local value). Such calls are enforced
// by checkCallbackInvokeCalls at the carrier call sites, so the generic
// unprovable-callee transfer fence must not double-flag them; anything
// else keeps the fence.
func (w *fileRules) storeCarrierTracedFieldCall(v *ast.CallExpr, fun ast.Expr) bool {
	if w.pc.pf == nil || w.curFunc == nil {
		return false
	}
	key, ok := w.pc.pf.fieldCalleeKey(w.pc.info, fun)
	if !ok {
		return false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return false
	}
	if _, traced := encl.fieldInvokes[key]; !traced {
		return false
	}
	if !w.moduleFieldCarrier(key) {
		return false
	}
	hasSlice := false
	for _, a := range v.Args {
		a = unparen(a)
		for {
			if se, ok := a.(*ast.SliceExpr); ok {
				a = unparen(se.X)
				continue
			}
			break
		}
		id, ok := a.(*ast.Ident)
		if !ok || w.curFunc == nil || w.curFunc.Type.Params == nil {
			return false
		}
		obj, ok := w.pc.info.Uses[id].(*types.Var)
		if !ok || !paramCanCarryPage(obj.Type()) {
			return false
		}
		hasSlice = true
		found := false
		for _, f := range w.curFunc.Type.Params.List {
			for _, name := range f.Names {
				if w.pc.info.ObjectOf(name) == obj {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return hasSlice
}

// callbackSlotOf reports whether a func-typed expression ultimately
// names the store-callback formal of the current function: the formal
// itself, an identity or type-assertion alias of it, a struct field or
// indexed slot that holds it, or a type assertion of any of these. The
// rules-side counterpart of the flow-pass slotOfExpr; the two must
// agree on every shape so the counter-check fires exactly when the flow
// records an invocation.
func (w *fileRules) callbackSlotOf(e ast.Expr) bool {
	switch t := unparen(e).(type) {
	case *ast.Ident:
		obj, ok := w.pc.info.Uses[t].(*types.Var)
		if !ok || funcSignature(obj.Type()) == nil {
			return false
		}
		return w.approvedFuncParamVar(obj) || w.paramAliasedFuncVar(obj, 0) || w.recordedCallbackAlias(obj)
	case *ast.SelectorExpr:
		return w.fieldHoldsCallbackFormal(t)
	case *ast.IndexExpr, *ast.IndexListExpr:
		return w.indexHoldsCallbackFormal(t)
	case *ast.TypeAssertExpr:
		return w.callbackSlotOf(t.X)
	case *ast.CompositeLit:
		// A func-container composite literal ([]func{fn, fn}) assembles
		// the container from its elements: a composite whose element
		// resolves to the store formal is a container of the callback
		// (the flow-side slotOfExpr agrees). Any element can be
		// selected by an index, so one formal-bound element is enough.
		if elemFuncType(w.typeOf(t)) {
			for _, el := range t.Elts {
				if w.callbackSlotOf(el) {
					return true
				}
			}
		}
	case *ast.CallExpr:
		// A conversion (any(s.cb), (cbSig)(fn)) is an identity on the
		// bound value; a real call result resolves through the callee
		// summary's return records in callResultHoldsCallbackFormal.
		if isConversionCallExpr(w.pc.info, t) {
			return w.callbackSlotOf(t.Args[0])
		}
	}
	return false
}

// indexHoldsCallbackFormal reports whether an indexed callee names a
// container slot the current function assigned the store callback
// formal (arr[0] = fn, m["cb"] = fn, hs := []func{fn}), matching the
// flow-pass indexAliases record by root object, selector path, and
// constant index; a non-constant callee index hits the catch-all key.
func (w *fileRules) indexHoldsCallbackFormal(e ast.Expr) bool {
	if w.pc.pf == nil || w.curFunc == nil {
		return false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return false
	}
	key, ok := indexSlotKeyOf(w.pc.info, e)
	if !ok {
		return false
	}
	if _, ok := encl.indexAliases[key]; ok {
		return true
	}
	if key.index != "" {
		_, ok = encl.indexAliases[indexSlotKey{root: key.root, path: key.path}]
		return ok
	}
	return false
}

// rangeVarHoldsCallback reports whether a func-typed local is a range
// variable over a container of the current function that holds the
// store callback formal (for _, cb := range cbs with cbs a func-typed
// formal, or fns a local slice whose elements hold the formal): an
// invocation through the loop value is an element invocation of the
// callback, so the byte views it receives are policed by the same
// fences as a direct formal call.
func (w *fileRules) rangeVarHoldsCallback(v *types.Var) bool {
	if w.pc.pf == nil || w.curFunc == nil || v == nil || funcSignature(v.Type()) == nil {
		return false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok || encl.rangeVars == nil {
		return false
	}
	c, ok := encl.rangeVars[v]
	if !ok {
		return false
	}
	if id, isID := unparen(c).(*ast.Ident); isID {
		if obj, isVar := w.pc.info.Uses[id].(*types.Var); isVar && elemFuncType(obj.Type()) {
			// A func-CONTAINER formal (for _, cb := range cbs): the
			// container is a scanned callback container exactly like
			// the formal itself only when it provably receives the
			// store callback (storeCbBoundParam): then the binding call
			// sites are policed by the store fence. Any other
			// func-container formal has unproven elements and keeps the
			// fail-closed rule.
			if w.storeCbBoundParam(obj) {
				return true
			}
			// A local func-CONTAINER (fns := []func{fn, fn}) holds the
			// formal when the flow recorded any formal-bound element
			// for it: a variable-index or ranged dispatch can name the
			// callback.
			for k := range encl.indexAliases {
				if k.root == obj {
					return true
				}
			}
		}
	}
	return w.callbackSlotOf(c)
}

// storeCbBoundParam reports whether a func-typed (or func-container)
// formal parameter of the enclosing function provably receives the
// store callback formal: a store implementation binds its own callback
// formal into the slot, directly or through a chain of forwarding
// helpers (summaryStore.storeCbSlots, computed module-wide after the
// summaries stabilize). Only such containers are scanned callback
// containers at their definition site; any other func-container formal
// keeps the unproven-indirection fail-closed rule, because nothing
// guarantees its call sites bind scanned callbacks.
func (w *fileRules) storeCbBoundParam(obj *types.Var) bool {
	if obj == nil || w.curFunc == nil || w.pc.pf == nil || w.pc.pf.store == nil || w.pc.pf.store.storeCbSlots == nil {
		return false
	}
	if w.curFunc.Type.Params == nil {
		return false
	}
	idx := 0
	if w.curFunc.Recv != nil && len(w.curFunc.Recv.List) > 0 {
		idx = 1
	}
	for _, f := range w.curFunc.Type.Params.List {
		for _, name := range f.Names {
			if w.pc.info.ObjectOf(name) == obj {
				return w.pc.pf.store.storeCbSlots[funcKey(w.curFunc)][idx]
			}
			idx++
		}
	}
	return false
}

// indexCalleeOverFuncFormal reports whether an indexed callee names an
// element of a func-typed container formal that provably receives the
// store callback (cbs[i](v) with cbs ...func(page []byte) error bound
// by a store implementation): the container is a scanned callback
// container exactly like the formal itself (storeCbBoundParam), so the
// element invocation is a scanned call rather than an unproven
// indirection. Any other func-container formal has unproven elements
// and keeps the fail-closed rule (fs[0](page) with fs a plain
// []func([]byte) int parameter must stay an unproven callee).
func (w *fileRules) indexCalleeOverFuncFormal(e ast.Expr) bool {
	key, ok := indexSlotKeyOf(w.pc.info, e)
	if !ok {
		return false
	}
	obj, isVar := key.root.(*types.Var)
	if !isVar || !elemFuncType(obj.Type()) {
		return false
	}
	return w.storeCbBoundParam(obj)
}

// callResultHoldsCallbackFormal reports whether a call-typed callee
// (id(...)(args...)) is the result of a local return wrapper that
// passed the store callback formal through (id := func(f F) F { return
// f }; id(s.cb.(T))(out, out) invokes the asserted formal).
func (w *fileRules) callResultHoldsCallbackFormal(fun ast.Expr) bool {
	if w.pc.pf == nil || w.curFunc == nil {
		return false
	}
	encl, ok := w.pc.pf.summaries[funcKey(w.curFunc)]
	if !ok {
		return false
	}
	ic, ok := unparen(fun).(*ast.CallExpr)
	if !ok {
		return false
	}
	// A scanned function or method returning the stored formal
	// (s.getCB()(out, out) with getCB returning s.cb) or one of its own
	// func-typed parameters unchanged resolves the result the same way:
	// the caller's own fieldAliases decide a field-return key, the
	// caller's argument decides a parameter-identity result. Checked
	// before the local-ident path because the callee may be a selector
	// (method) or a free function, not a local variable.
	//
	// The callee may also be a LOCAL func-typed variable provably bound
	// to a module function or method value (g := passthrough; cb :=
	// g(fn), mv := s.getCB; cb := mv()): resolve it through its single
	// never-address-taken definition so the scanned summary applies.
	callee := ic.Fun
	for {
		id, isID := unparen(callee).(*ast.Ident)
		if !isID {
			break
		}
		obj, isVar := w.pc.info.Uses[id].(*types.Var)
		if !isVar || w.curFunc == nil || w.curFunc.Body == nil {
			break
		}
		init, single, taken := varDefOf(w.pc.info, w.curFunc.Body, obj)
		if init == nil || (!single && !taken) {
			break
		}
		callee = init
	}
	if fs := w.calleeSummary(callee); fs != nil {
		if fk, ok := fs.returnFieldKeys[0]; ok {
			if fk == multiReturnKey {
				return true
			}
			for _, r := range encl.fieldAliases[fk] {
				if !r.forwarded {
					return true
				}
			}
			return false
		}
		if p, ok := fs.returnSlotAliases[0]; ok {
			if p == -2 {
				return true
			}
			if p < 0 || p >= len(ic.Args) {
				return false
			}
			return w.callbackSlotOf(ic.Args[p])
		}
	}
	id, ok := unparen(ic.Fun).(*ast.Ident)
	if !ok {
		return false
	}
	obj, ok := w.pc.info.Uses[id].(*types.Var)
	if !ok {
		return false
	}
	pos, ok := encl.returnAliases[obj]
	if !ok {
		return false
	}
	if pos == -2 {
		// Different branches return different parameters: the result is
		// one of them, so the fence fails closed.
		return true
	}
	if pos < 0 || pos >= len(ic.Args) {
		return false
	}
	return w.callbackSlotOf(ic.Args[pos])
}

// calleeSummary returns the scanned funcSummary of a statically visible
// module callee expression (a function or method), or nil when the
// callee is not a scanned module function.
func (w *fileRules) calleeSummary(e ast.Expr) *funcSummary {
	if w.pc.pf == nil || w.pc.pf.store == nil {
		return nil
	}
	fn := callCalleeFuncOrVar(w, e)
	if fn == nil || fn.Pkg() == nil {
		return nil
	}
	sums := w.pc.pf.summaries
	if fn.Pkg().Path() != w.pc.pkg.Path() {
		sums = w.pc.pf.store.pkgs[fn.Pkg().Path()]
	}
	if sums == nil {
		return nil
	}
	key := fn.Name()
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
	}
	return sums[key]
}

// forwardsCallbackFormal reports whether the call passes the current
// store implementation's callback formal (directly, through a local
// alias, or through a field holder) as a func-typed argument. A
// forwarder literal or helper receiving the formal must hand it mapped
// views, so the store-implementation call site enforces the same
// counter-check on its byte arguments.
func (w *fileRules) forwardsCallbackFormal(v *ast.CallExpr) bool {
	resolves := func(e ast.Expr) bool {
		return w.callbackSlotOf(e)
	}
	for _, arg := range v.Args {
		if resolves(arg) {
			return true
		}
	}
	return false
}

// checkFuncTypedArgs enforces the scanned-callback fence: an approved
// module callee with a func-typed formal may only receive a callback
// whose body is part of the scanned tree. Every implementer and caller
// of module-internal code is scanned, so a func literal, a provably
// scanned package function variable, a func-typed formal parameter of
// the current function, or a method value on a scanned concrete type
// all bottom out in policed bodies; anything else (a stdlib binding, an
// interface method value, an opaque local) could launder a complete
// mapped page into memory the gate cannot see.
func (w *fileRules) checkFuncTypedArgs(v *ast.CallExpr, fun ast.Expr, formals []types.Type, approved bool) {
	if !approved || len(formals) == 0 {
		return
	}
	for i, arg := range v.Args {
		ft := types.Type(nil)
		if i < len(formals) {
			ft = formals[i]
		} else if sl, ok := types.Unalias(formals[len(formals)-1]).(*types.Slice); ok {
			ft = sl.Elem()
		} else {
			break
		}
		if !isFuncType(ft) {
			continue
		}
		if w.scannedCallback(arg) {
			continue
		}
		w.fail(v.Pos(), "func-typed argument to %s is not a scanned callback (complete page into owned memory)", calleeText(fun))
	}
}

// scannedCallback reports whether a func-typed argument names a callback
// whose body is scanned by this gate.
func (w *fileRules) scannedCallback(arg ast.Expr) bool {
	switch a := unparen(arg).(type) {
	case *ast.FuncLit:
		return true
	case *ast.Ident:
		switch obj := w.pc.info.Uses[a].(type) {
		case *types.Func:
			if obj.Pkg() == nil {
				return false
			}
			p := obj.Pkg().Path()
			return p == w.pc.pkg.Path() || moduleInternalPackage(p)
		case *types.Var:
			return w.approvedFuncVar(obj, 0) || w.approvedFuncParamVar(obj) || w.approvedLocalFuncVar(obj, 0) || w.paramAliasedFuncVar(obj, 0) || w.recordedCallbackAlias(obj) || w.formalAliasedLocal(obj) || w.localMethodCarrier(obj)
		}
		return false
	case *ast.SelectorExpr:
		// A func-typed FIELD argument (h.cb): scanned when the field
		// key is a recorded direct store-carrier of some
		// implementation - the carrier composition fences its call
		// sites, so the field's body needs no separate scan.
		if v, isVar := w.pc.info.Uses[a.Sel].(*types.Var); isVar && funcSignature(v.Type()) != nil {
			if w.pc.pf != nil {
				if key, ok := w.pc.pf.fieldSlotKeyOf(w.pc.info, a); ok {
					return w.moduleFieldCarrier(key)
				}
			}
			return false
		}
		fn, ok := w.pc.info.Uses[a.Sel].(*types.Func)
		if !ok || fn.Pkg() == nil {
			return false
		}
		p := fn.Pkg().Path()
		if p != w.pc.pkg.Path() && !moduleInternalPackage(p) {
			return false
		}
		// A method value on an interface dispatches; only concrete
		// scanned receiver types have a policed body.
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
			return !isInterfaceType(sig.Recv().Type())
		}
		return true
	case *ast.CallExpr:
		// A func-typed argument produced by a helper call (h1(g)
		// passed on): the callee's return records decide whether the
		// result is one of the helper's own func-typed parameters
		// (identity chains), a carrier field read, or an unknown value.
		if w.pc.pf == nil || w.pc.pf.store == nil {
			return false
		}
		fs := w.calleeSummary(a.Fun)
		if fs == nil {
			return false
		}
		if p, ok := fs.returnSlotAliases[0]; ok {
			if p == -2 {
				// Different branches return different parameters: the
				// result is one of them, so the fence fails closed.
				return true
			}
			if p < 0 || p >= len(a.Args) {
				return false
			}
			return w.scannedCallback(a.Args[p])
		}
		if fk, ok := fs.returnFieldKeys[0]; ok {
			if fk == multiReturnKey {
				return true
			}
			return w.moduleFieldCarrier(fk)
		}
		return false
	}
	return false
}

// isFuncType reports whether t is a function (signature) type.
func isFuncType(t types.Type) bool {
	return funcSignature(t) != nil
}

// funcSignature unwraps aliases and defined func types (type F func(...)
// with a named declaration) to the underlying signature.
func funcSignature(t types.Type) *types.Signature {
	if t == nil {
		return nil
	}
	t = types.Unalias(t)
	if n, ok := t.(*types.Named); ok {
		t = n.Underlying()
	}
	sig, _ := t.(*types.Signature)
	return sig
}

// methodValueCallee returns the flow-pass resolution of a method-value
// call (get := b.String; get()): the method and the captured receiver.
func (w *fileRules) methodValueCallee(v *ast.CallExpr) (methodValueCall, bool) {
	if w.pc.pf == nil {
		return methodValueCall{}, false
	}
	mvr, ok := w.pc.pf.callMethodValues[v]
	return mvr, ok
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

// approvedLocalFuncVar reports whether a local function variable
// provably binds a scanned callback from its declaration initializer: a
// func literal (its body is part of this package), a scanned package
// function, or a chain of never-reassigned local aliases to either. A
// local written after declaration or declared without an initializer has
// no provable binding; the callback fence treats it like any other
// opaque local.
func (w *fileRules) approvedLocalFuncVar(v *types.Var, depth int) bool {
	if v == nil || depth > 2 || w.pc.localReassigned[v] || funcSignature(v.Type()) == nil {
		return false
	}
	init, ok := w.pc.localFuncInits[v]
	if !ok {
		return false
	}
	switch i := unparen(init).(type) {
	case *ast.FuncLit:
		return true
	case *ast.Ident:
		switch o := w.pc.info.Uses[i].(type) {
		case *types.Func:
			return w.approvedFuncPkg(o)
		case *types.Var:
			if o.Parent() == w.pc.pkg.Scope() {
				return w.approvedFuncVar(o, 0)
			}
			return w.approvedLocalFuncVar(o, depth+1)
		}
		return false
	case *ast.SelectorExpr:
		fn, ok := w.pc.info.Uses[i.Sel].(*types.Func)
		if !ok {
			return false
		}
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
			// An interface method expression has no scanned body: the
			// implementation dispatches dynamically, so an alias to it
			// is an unproven indirection no matter how it is spelled.
			return false
		}
		return w.approvedFuncPkg(fn)
	}
	return false
}

// paramAliasedFuncVar reports whether a local function-typed variable
// is a never-reassigned identity alias of a func-typed FORMAL parameter
// of the enclosing function (cb := fn, or a chain cb2 := cb of such
// aliases). The formal's call sites are policed by the callback fence
// (every argument bound to a func-typed formal must be a scanned
// callback), so the alias is a scanned callback exactly like the formal
// itself. A literal wrapper is NOT covered here: literal wrappers are
// admitted by approvedLocalFuncVar and the store-callback fence follows
// their recorded aliases.
func (w *fileRules) paramAliasedFuncVar(v *types.Var, depth int) bool {
	if v == nil || depth > 2 || w.pc.localReassigned[v] || funcSignature(v.Type()) == nil {
		return false
	}
	init, ok := w.pc.localFuncInits[v]
	if !ok {
		return false
	}
	// A type-assertion initializer binds the same closure value as its
	// base: f := fn.(T), f := s.cb.(T), f := arr[0].(T) are scanned
	// callbacks exactly like the base expression.
	src := unparen(init)
	for {
		if ta, isTa := src.(*ast.TypeAssertExpr); isTa {
			src = unparen(ta.X)
			continue
		}
		break
	}
	switch s := src.(type) {
	case *ast.Ident:
		switch o := w.pc.info.Uses[s].(type) {
		case *types.Var:
			if w.approvedFuncParamVar(o) {
				return true
			}
			// An any-typed holder the flow recorded as bound to the
			// formal (var box any = fn; f := box.(T)) is a scanned
			// callback exactly like the formal: the box's value is the
			// closure the formal's call sites police.
			if w.recordedCallbackAlias(o) {
				return true
			}
			return w.paramAliasedFuncVar(o, depth+1)
		}
	case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		return w.callbackSlotOf(s)
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
	if bannedSelectors[v.Sel.Name] && !(lifecycleOwnerOnly[v.Sel.Name] && isMappingOwnerPath(w.pc.pkg.Path())) {
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
			// A func-typed formal parameter of a module-internal
			// function is the approved exception: its call sites are
			// scanned, and the callback fence requires them to pass a
			// scanned callback.
			formalLike := w.approvedFuncParamVar(obj)
			aliasLike := w.paramAliasedFuncVar(obj, 0) || w.recordedCallbackAlias(obj) || w.formalAliasedLocal(obj) || w.localMethodCarrier(obj)
			// A func-typed range variable over a callback container
			// (for _, cb := range cbs { cb(x) }) is an element
			// invocation of the callback: the container's call sites
			// bind scanned callbacks, so the loop value is a scanned
			// callee exactly like the formal itself, and the store
			// counter-check demands mapped views for its byte args.
			rangeLike := w.rangeVarHoldsCallback(obj)
			if !w.approvedFuncVar(obj, 0) && !formalLike && !aliasLike && !rangeLike {
				varIndirect = true
			}
			if w.isStoreCallbackImpl() && (formalLike || aliasLike || rangeLike || w.forwardsCallbackFormal(v)) {
				w.checkStoreCallbackViews(v, fun)
			}
		}
	case *ast.SelectorExpr:
		if w.isStoreCallbackImpl() && w.fieldHoldsCallbackFormal(f) {
			w.checkStoreCallbackViews(v, fun)
		} else if w.isStoreCallbackImpl() {
			// A struct-field func callee with NO record in this
			// function (the field was bound through a setter method,
			// a global helper, or a pointer receiver) still hands the
			// views to whatever value the field holds: when the field
			// holds the store callback, owned views launder through it
			// exactly like the recorded shapes, and when it holds any
			// other function the views enter an unproven body. The
			// byte-argument counter-check fails closed here on owned
			// views and accepts mapped views, so the honest twins
			// (mapped views through a cross-function binding) stay
			// legal.
			if fld, isFld := w.pc.info.Uses[f.Sel].(*types.Var); isFld && funcSignature(fld.Type()) != nil {
				w.checkStoreCallbackViews(v, fun)
			}
		}
		switch obj := w.pc.info.Uses[f.Sel].(type) {
		case *types.Func:
			// A concrete method on a value type has a scanned body, but
			// an interface method dispatches to an unknowable
			// implementation: c.Apply(page) on a CB interface can copy
			// the full page inside an unscanned method body. Only the
			// explicitly approved store/codec interfaces dispatch
			// without indirection; every other interface - including
			// module-declared ones with an out-of-module satisfier -
			// is an unproven callee.
			if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) &&
				!approvedModuleInternalInterface(sig.Recv().Type()) {
				varIndirect = true
			}
		default:
			// h.cb(page) with cb a function-typed field of a struct: the
			// callee is not statically visible, so the call is an
			// unproven indirection like a function variable. A recorded
			// store-callback carrier field is the approved exception: its
			// call sites are scanned and the callback fence requires the
			// carrier argument's byte views to be mapped, so the fence
			// replaces the generic transfer check for parameter-sourced
			// arguments.
			if !w.storeCarrierTracedFieldCall(v, f) {
				varIndirect = true
			}
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		// A generic instantiation of a scanned module function is a
		// direct approved call (approvedCallee resolves it), so only
		// UNRESOLVED index callees are indirect.
		if callCalleeFuncOrVar(w, f) != nil {
			break
		}
		// fs[0](page): a call through a slice/array element or map
		// lookup has an unknowable callee body. When the indexed
		// container slot holds the store callback formal (arr[0] = fn;
		// arr[0](out, out)), the clean owned buffers launder through it,
		// so the counter-check demands mapped views like every holder.
		if w.isStoreCallbackImpl() && w.indexHoldsCallbackFormal(f) {
			w.checkStoreCallbackViews(v, f)
		}
		// An indexed element of a func-typed FORMAL container
		// (cbs[i](v) with cbs ...func(page []byte) error) is a scanned
		// callback exactly like the container itself: the call sites
		// binding the container are policed, so the element call is not
		// an unproven indirection, and the store counter-check applies
		// to the views it receives.
		if w.indexCalleeOverFuncFormal(f) {
			if w.isStoreCallbackImpl() {
				w.checkStoreCallbackViews(v, f)
			}
			break
		}
		varIndirect = true
	case *ast.StarExpr:
		// (*p)(page): a call through a dereferenced function pointer has
		// an unknowable callee body.
		varIndirect = true
	case *ast.TypeAssertExpr:
		// x.(func([]byte) int)(page): a type assertion over a base that
		// holds the store callback formal (s.cb.(T)(out, out)) launders
		// clean owned buffers exactly like the base holder itself.
		if w.isStoreCallbackImpl() && w.callbackSlotOf(f) {
			w.checkStoreCallbackViews(v, f)
		}
		varIndirect = true
	case *ast.CallExpr:
		// factory()(page): a call-typed callee produced by a return
		// wrapper that passed the store callback formal through
		// (id := func(f F) F { return f }; id(s.cb.(T))(out, out)) is
		// the formal at the invocation point.
		if w.isStoreCallbackImpl() && w.callResultHoldsCallbackFormal(f) {
			w.checkStoreCallbackViews(v, f)
		}
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
		// A method VALUE called through its variable (get := b.String;
		// get()) hides the receiver at the call site: the captured
		// receiver expression is only visible through the flow-pass
		// resolution, so the same full-page receiver transfer applies
		// there. Scanned method values are approved callees and never
		// reach this branch; external method bodies are unprovable.
		if mvr, ok := w.methodValueCallee(v); ok {
			if pv := w.pageValue(mvr.recv); pv.tainted && pageFull(pv) {
				w.fail(v.Pos(), "mapped page view passed to %s on an unprovable receiver (complete page into owned memory)", calleeText(v.Fun))
			}
		}
	}
	// A module-internal interface method whose receiver CONCRETELY
	// carries a complete mapped page (a struct field bound to a page, or
	// a local binding) can launder it into owned memory inside an
	// implementation this call site cannot resolve: every implementation
	// is scanned, but the receiver data is erased here, so the call
	// fails closed like the external interface case. The conservative
	// parameter fallback (a Store/Codec interface parameter that only
	// MIGHT receive a page from a caller) is not concrete and stays
	// benign: the scanned implementations receive no page data through
	// the erased receiver, and page-bearing ARGUMENTS of the dispatch
	// are policed at the same call site.
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		if selRecv, isSel := w.pc.info.Selections[sel]; isSel && selRecv.Kind() == types.MethodVal {
			if obj, ok := w.pc.info.Uses[sel.Sel].(*types.Func); ok {
				if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) &&
					moduleInternalInterface(sig.Recv().Type()) {
					if pv := w.pageValue(sel.X); pv.tainted && pageFull(pv) && w.pageFieldPromoted(sel.X) {
						w.fail(v.Pos(), "mapped page view passed to %s on a module-internal interface receiver (complete page into owned memory)", calleeText(fun))
					}
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
				} else if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && !isInterfaceType(sig.Recv().Type()) && !w.approvedFuncPkg(obj) {
					// A concrete method on a type declared OUTSIDE the
					// scanned module (net.TCPListener.File, a third-party
					// wrapper) has an unscanned body that can mint a real
					// descriptor: the os-only selector check also missed
					// this class, so it fails closed like the interface
					// dispatch case. Methods on scanned receiver types
					// keep their policed bodies; the x/sys surface is
					// the pinned external syscall authority.
					w.fail(v.Pos(), "external method %s returns a file-bearing value outside the mapping owner (capability launder)", f.Sel.Name)
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
		if pageArg.tainted && pageFull(pageArg) && !w.storeCarrierTracedFieldCall(v, fun) && !w.storeCallbackCallee(v) &&
			!exempt && (transfer || varIndirect || (w.unprovenVarCallee(fun) && w.pageFieldPromoted(arg))) {
			w.fail(v.Pos(), "mapped page view passed to %s (complete page into owned memory)", calleeText(fun))
		}
		// The owned byte-builder family copies its argument into an owned
		// heap buffer: bytes.NewBuffer(v), bytes.Buffer.Write*(v), and
		// strings.Builder.Write*(v) own the bytes afterward, so a full
		// mapped view reaching them is the complete-page violation even
		// though their result types are structs or scalar counts the
		// transfer rule cannot see.
		if pageArg.tainted && pageFull(pageArg) && !exempt && w.ownedCopySink(fun) {
			w.fail(v.Pos(), "mapped page view copied into an owned byte builder (%s)", calleeText(fun))
		}
	}
	w.checkInterfaceErasure(v, formals)
	// A module helper that converts one of its parameters into an owned
	// string (string(p) recorded in its summary) copies the caller's
	// bytes; the conversion inside the callee cannot see that the bound
	// is a full mapped page, so the call site fails closed.
	fn := callCalleeFuncOrVar(w, fun)
	if fn != nil {
		w.checkCarrierViewCallSites(v, fn)
	}
	if fn == nil {
		// A method value stored in a local is not a statically visible
		// callee; the flow pass resolved it, and the string-parameter
		// rule needs the captured receiver to check the call site.
		if mvr, ok := w.methodValueCallee(v); ok {
			fn = mvr.fn
		}
	}
	if fn != nil {
		w.checkStringParamCalls(v, fn)
		w.checkParamCopyCalls(v, fn)
		w.checkCallbackInvokeCalls(v, fn)
	}
	// Func-typed formals of approved module callees receive a scanned
	// callback: the callee hands it page views by contract, so an
	// unprovable argument would launder a complete mapped page into a
	// body the gate cannot follow.
	w.checkFuncTypedArgs(v, fun, formals, approved)
	// A helper whose parameter is written element-wise, called inside a
	// page-sourcing loop with an owned destination argument, copies the
	// complete page through the helper. The flow pass recorded the
	// destination expressions at the call site.
	if w.pc.pf != nil {
		if dests, ok := w.pc.pf.pageSinkCalls[v]; ok && len(dests) > 0 {
			w.fail(v.Pos(), "mapped page copied element-wise through %s into an owned buffer (complete page)", calleeText(v.Fun))
		}
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
	case *ast.IndexExpr:
		// A generic instantiation (runG[T](...)) names the generic
		// function itself.
		return callCalleeFuncOrVar(w, f.X)
	case *ast.IndexListExpr:
		return callCalleeFuncOrVar(w, f.X)
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
	fn, ok := w.pc.info.Uses[sel.Sel].(*types.Func)
	if !ok {
		return false
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return false
	}
	switch {
	case pkg.Path() == "bytes" && fn.Name() == "NewBuffer":
		return true
	case pkg.Path() == "math/big" && fn.Name() == "SetBytes":
		// big.Int.SetBytes copies its byte argument into owned
		// []big.Word limbs, which resultHoldsBytes cannot see; a
		// full mapped page must not land there (spec:108).
		return true
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
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.StructType:
		return true
	case *ast.StarExpr:
		// (*T)(x) is a conversion; (*p)(x) with p a func-typed VARIABLE
		// is a call through a dereferenced function pointer with an
		// unknowable callee body. The pointee must itself name a type
		// for the expression to be a type expression: a pointer-deref
		// call would otherwise fall into the conversion branch and skip
		// the unproven-callee page-argument fence entirely.
		return w.isTypeExpr(f.X)
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
			// A method EXPRESSION call (T.M(recv, args...)) carries the
			// receiver as an explicit first argument, unlike a method
			// VALUE call (recv.M(args...)); the formal list must line
			// up with the arguments, so the receiver type is prepended.
			// Without this, checkFuncTypedArgs and checkInterfaceErasure
			// shift by one: the receiver value is checked against a
			// func-typed formal and every func-typed argument is
			// misread as a callback or a carrier, falsely rejecting
			// honest direct method-expression calls.
			if sig != nil && sig.Recv() != nil {
				if selRecv, isSel := w.pc.info.Selections[f]; isSel && selRecv.Kind() == types.MethodExpr {
					out := make([]types.Type, 0, sig.Params().Len()+1)
					out = append(out, sig.Recv().Type())
					for i := 0; i < sig.Params().Len(); i++ {
						out = append(out, sig.Params().At(i).Type())
					}
					return out
				}
			}
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
		ft := types.Type(nil)
		if i < len(formals) {
			ft = formals[i]
		} else if sl, ok := types.Unalias(formals[len(formals)-1]).(*types.Slice); ok {
			// Trailing arguments past the last formal land in a
			// variadic element slot: the erasure check applies to the
			// element type (…any erases like any, …T like T).
			ft = sl.Elem()
		} else {
			break // extra arguments cannot type-check against a non-variadic callee
		}
		if ft == nil {
			continue
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
		holds := !bears && isInterfaceType(ft) && structContainsFile(at, map[types.Type]bool{})
		if !bears && !holds {
			continue
		}
		if isInterfaceType(ft) {
			// Only the explicitly approved store/codec interfaces
			// accept implementations from scanned code: the erased
			// descriptor is re-inspected at every use inside the
			// module, so the erasure is not a launder. Every other
			// interface keeps failing closed (an out-of-module
			// implementation could launder the erased descriptor).
			if !approvedModuleInternalInterface(ft) {
				w.fail(v.Pos(), "file-bearing argument laundered into an interface parameter (type erasure)")
			}
		} else if _, ok := ft.(*types.TypeParam); ok {
			if !resBears {
				w.fail(v.Pos(), "file-bearing argument through a generic callee erased into a non-file-bearing result (type erasure)")
			}
		}
	}
}

// checkCopy flags copy(dst, src) when the copied span can be a complete
// page: src is a mapped page view with an unbounded/unknown bound, and dst
// is an owned buffer that is not statically tiny. Three shapes are benign
// and return before the owned-destination test:
//
//   - a MAPPED destination: every copied byte stays inside the mapping;
//   - a same-root intra-buffer copy: the bytes move inside one buffer
//     (the slotted-page record shifts of internal/format);
//   - a parameter-sourced destination paired with a parameter-sourced
//     source: both sides are caller-bound, and the callee's copy-parameter
//     summary makes the call sites fail an owned destination bound with a
//     mapped full-page source.
func (w *fileRules) checkCopy(v *ast.CallExpr) {
	// The exact-shape findExemptions branch (writer metadata chain
	// geometry) may sanction one precise copy position; positions are
	// unique, so a mutation cannot inherit the exemption by editing the
	// expression at the exempted site.
	if w.exempts[v.Pos()] {
		return
	}
	if len(v.Args) != 2 {
		return
	}
	src := w.pageValue(v.Args[1])
	if !src.tainted {
		return
	}
	dst := v.Args[0]
	// Slice-header destinations never own page bytes.
	if elemT := collectionElementType(w.typeOf(dst)); elemT != nil && !byteElementType(elemT) {
		return
	}
	// A mapped destination keeps the copied bytes inside the mapping:
	// writing into a page view never creates an owned complete page.
	if pvd := w.pageValue(dst); pvd.tainted && pvd.mapped {
		return
	}
	// A copy between two views of the same buffer stays inside that
	// buffer (an intra-page record move), whether the buffer is mapped
	// (writer page mutations) or an owned test page.
	if d, s := w.pageRoot(dst), w.pageRoot(v.Args[1]); d != nil && d == s {
		return
	}
	dpv := w.pageValue(dst)
	// Definite symbolic bounds count as bounded spans too (a slice with
	// constant low/high carries a constant maxLen).
	if src.hasSym {
		if c, ok := src.sym.isConst(); ok && c > 0 && c < pageSize {
			src.maxLen = c
		}
	}
	// Repeated bounded copies into one destination can assemble a
	// complete page from sub-page spans; count them before the full-page
	// fast path. A parameter-sourced destination is caller-bound, so the
	// callee's call sites decide (the copy-parameter rule); the span
	// accumulation only applies to objects with a definite owned shape.
	if dpv.hasSrc {
		if src.hasSrc {
			// Both sides caller-bound: the call-site copy-parameter rule
			// fails an owned destination bound with a mapped full-page
			// source, so the definition site lets the pair through.
			return
		}
		// An unconditional mapped source into a caller-bound destination
		// is a complete-page copy regardless of what the caller binds:
		// fail here, the mapped content cannot be re-bound away.
		if src.maxLen > 0 && src.maxLen < pageSize {
			return // bounded unconditional span into a param stays sub-page at every call site
		}
		if pageFull(src) {
			w.fail(v.Pos(), "copy of a mapped page view into an owned buffer (complete page)")
		}
		return
	}
	if src.maxLen > 0 && src.maxLen < pageSize {
		if obj, path := w.boundedCopyKey(dst); obj != nil {
			w.accumulateBoundedSpan(obj, path, src.maxLen, v.Pos(), "bounded mapped-page spans assembled into an owned PageSize-capable buffer (complete page)")
		}
		return
	}
	if !pageFull(src) {
		return
	}
	if elemT := collectionElementType(w.typeOf(dst)); elemT != nil && !byteElementType(elemT) {
		return // slice-header copy, not page-byte ownership
	}
	if dstCap := w.ownedCap(dst); dstCap >= 0 && dstCap < pageSize {
		// A destination-bounded copy of a full mapped source is
		// sub-page by itself, but repeated calls can still assemble a
		// complete page into one owned buffer (e.g. page[48:] then
		// page[:48] from the tail of the same mapped page); count the
		// span exactly like the source-bounded branch so the assembly
		// accumulator sees the full picture (battery 321).
		if obj, path := w.boundedCopyKey(dst); obj != nil {
			w.accumulateBoundedSpan(obj, path, dstCap, v.Pos(), "bounded mapped-page spans copied into one owned buffer (complete page)")
		}
		return
	}
	w.fail(v.Pos(), "copy of a mapped page view into an owned buffer (complete page)")
}

// boundedChainRoot resolves the root identifier of a selector or pointer
// destination chain (h.Buf, (*p), h.Inner.Buf).
func boundedChainRoot(e ast.Expr) *ast.Ident {
	for {
		switch d := unparen(e).(type) {
		case *ast.SelectorExpr:
			e = d.X
		case *ast.StarExpr:
			e = d.X
		case *ast.ParenExpr:
			e = d.X
		case *ast.SliceExpr, *ast.IndexExpr, *ast.TypeAssertExpr:
			e = chainInner(d)
		default:
			id, _ := d.(*ast.Ident)
			return id
		}

	}
}

// pageRoot resolves the root object of a slice/index/selector/pointer
// destination or source chain (page[slot+2:], h.Buf[lo:hi], (*p)[lo:]).
// Two expressions sharing one root object name the same backing buffer,
// so a copy between them is an intra-buffer move, never an ownership
// transfer.
func (w *fileRules) pageRoot(e ast.Expr) types.Object {
	root := boundedChainRoot(unparen(e))
	if root == nil {
		return nil
	}
	if obj := w.pc.info.Uses[root]; obj != nil {
		return obj
	}
	return w.pc.info.Defs[root]
}

// chainInner returns the base expression of a destination chain step.
func chainInner(e ast.Expr) ast.Expr {
	switch d := e.(type) {
	case *ast.SliceExpr:
		return d.X
	case *ast.IndexExpr:
		return d.X
	case *ast.TypeAssertExpr:
		return d.X
	case *ast.SelectorExpr:
		return d.X
	case *ast.StarExpr:
		return d.X
	case *ast.ParenExpr:
		return d.X
	}
	return e
}

// boundedCopyKey resolves a bounded copy destination to its canonical
// accumulation key: root object plus flattened field path.
// canonicalSpanKey follows selector-field aliases to one accumulation key.
func (w *fileRules) canonicalSpanKey(key boundedSpanKey) boundedSpanKey {
	if w.pc.pf == nil {
		return key
	}
	for d := 0; d < 8; d++ {
		next, ok := w.pc.pf.spanAliases[key]
		if !ok || next == key {
			return key
		}
		key = next
	}
	return key
}

func (w *fileRules) boundedCopyKey(dst ast.Expr) (types.Object, string) {
	root := w.boundedCopyTarget(dst)
	id, ok := root.(*ast.Ident)
	if !ok {
		return nil, ""
	}
	obj := w.pc.info.Uses[id]
	if obj == nil {
		obj = w.pc.info.Defs[id]
	}
	if obj == nil {
		return nil, ""
	}
	if w.pc.pf != nil {
		for d := 0; d < 8; d++ {
			next, ok := w.pc.pf.appendAliases[obj]
			if !ok {
				break
			}
			if next == nil {
				break
			}
			if next == obj {
				obj = nil
				break
			}
			obj = next
		}
	}
	if obj == nil {
		return nil, ""
	}
	key := boundedSpanKey{obj: obj, path: w.destinationFieldPath(dst)}
	key = w.canonicalSpanKey(key)
	return key.obj, key.path
}

// boundedAppendKey resolves an append destination to the same canonical
// key used by bounded copies.
func (w *fileRules) boundedAppendKey(e ast.Expr) (types.Object, string) {
	root := boundedChainRoot(unparen(e))
	if root == nil {
		return nil, ""
	}
	obj := w.pc.info.Uses[root]
	if obj == nil {
		obj = w.pc.info.Defs[root]
	}
	if obj == nil {
		return nil, ""
	}
	key := boundedSpanKey{obj: obj, path: w.destinationFieldPath(e)}
	key = w.canonicalSpanKey(key)
	return key.obj, key.path
}

// accumulateBoundedSpan adds a bounded mapped-page span to one canonical
// destination and fails when that destination can now hold PageSize.
func (w *fileRules) accumulateBoundedSpan(obj types.Object, path string, span int64, pos token.Pos, msg string) {
	if w.pc.pf == nil {
		return
	}
	key := boundedSpanKey{obj: obj, path: path}
	prev := int64(w.pc.pf.boundedPageSpans[key])
	total := prev + span
	w.pc.pf.boundedPageSpans[key] = int(total)
	if total >= pageSize {
		w.fail(pos, "%s", msg)
	}
}

// destinationFieldPath returns the flattened selector field path of a
// destination expression, or the empty path for a direct variable.
func (w *fileRules) destinationFieldPath(e ast.Expr) string {
	e = unparen(e)
	parts := []string{}
	for {
		switch d := e.(type) {
		case *ast.SliceExpr, *ast.IndexExpr:
			e = chainInner(d)
		case *ast.StarExpr:
			e = d.X
		case *ast.ParenExpr:
			e = d.X
		case *ast.SelectorExpr:
			parts = append([]string{d.Sel.Name}, parts...)
			e = d.X
		default:
			return strings.Join(parts, ".")
		}
	}
}

// boundedCopyTarget resolves the expression that owns a bounded copy
// destination: the identifier itself, or the root identifier of index,
// slice, selector, pointer, and paren chains.
func (w *fileRules) boundedCopyTarget(dst ast.Expr) ast.Expr {
	dst = unparen(dst)
	switch d := dst.(type) {
	case *ast.Ident:
		return d
	case *ast.SliceExpr, *ast.IndexExpr, *ast.SelectorExpr, *ast.StarExpr, *ast.ParenExpr, *ast.TypeAssertExpr:
		return w.boundedCopyTarget(chainInner(d))
	}
	return dst
}

// byteElementType reports whether t is the byte type itself.
func byteElementType(t types.Type) bool {
	b, ok := unwrapToUnderlying(types.Unalias(t)).(*types.Basic)
	return ok && b.Kind() == types.Byte
}

// checkAppend flags append(dst, src...) when src is a mapped page view.
func (w *fileRules) checkAppend(v *ast.CallExpr) {
	if len(v.Args) < 2 {
		return
	}
	byteElemT := collectionElementType(w.typeOf(v.Args[0]))
	span := int64(0)
	for _, a := range v.Args[1:] {
		src := w.pageValue(a)
		if !src.tainted {
			continue
		}
		if pageFull(src) && (byteElemT == nil || byteElementType(byteElemT)) {
			w.fail(v.Pos(), "append of a mapped page view into an owned buffer (complete page)")
			return
		}
		if src.maxLen > 0 && src.maxLen < pageSize {
			if src.hasSym {
				if c, ok := src.sym.isConst(); ok && c > 0 && c < pageSize {
					src.maxLen = c
				}
			}
			span += src.maxLen
		}
	}
	// Repeated sub-page appends into one destination variable assemble a
	// complete owned page from bounded spans. Byte-span accumulation is
	// meaningful only for byte-element destinations; [][]byte appends
	// slice headers, not page bytes.
	if span > 0 && byteElemT != nil && byteElementType(byteElemT) {
		obj, path := w.boundedAppendKey(v.Args[0])
		if w.pc.pf != nil && obj != nil {
			if root, ok := w.pc.pf.appendCallRoots[v]; ok && root != nil {
				obj = root
			}
		}
		if obj != nil {
			if w.pc.pf != nil {
				root := obj
				for d := 0; d < 8; d++ {
					next, ok := w.pc.pf.appendAliases[root]
					if !ok {
						break
					}
					if next == nil {
						break
					}
					if next == root {
						root = nil
						break
					}
					root = next
				}
				obj = root
			}
			if obj == nil {
				return
			}
			w.accumulateBoundedSpan(obj, path, span, v.Pos(), "bounded mapped-page spans appended repeatedly into one owned buffer (complete page)")
		}
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

// compositeCarrierMapped answers whether a struct composite literal
// argument provably carries only mapped byte views, re-derived from its
// element expressions. The flow pass computes the same conjunction when
// it evaluates the literal (evalComposite), but the field-promotion pass
// rewrites the literal's cached whole-value taint afterwards without the
// mapped flag (promoteFullPageFields), so the store-callback fence would
// otherwise over-reject an honest pair{a: x, b: y} wrapper argument whose
// elements are all mapped views. The check is read-only and fail-safe:
// every byte-carrying field must have a corresponding element expression
// present in the flow cache with mapped=true; any missing element, any
// non-mapped element, or any non-literal argument stays unproven and the
// existing fail-closed path applies unchanged.
func (w *fileRules) compositeCarrierMapped(e ast.Expr) bool {
	lit, ok := unparen(e).(*ast.CompositeLit)
	if !ok || w.pc.pf == nil {
		return false
	}
	typ := w.typeOf(lit)
	styp, ok := derefStruct(typ)
	if !ok || styp.NumFields() == 0 {
		return false
	}
	// Map field names to the literal's own element expressions, then
	// require every byte-carrying struct field to be present and mapped.
	vals := map[string]ast.Expr{}
	for i, el := range lit.Elts {
		var field string
		var val ast.Expr
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if fid, ok := unparen(kv.Key).(*ast.Ident); ok {
				field, val = fid.Name, kv.Value
			}
		} else if i < styp.NumFields() {
			field, val = styp.Field(i).Name(), el
		}
		if field != "" && val != nil {
			vals[field] = val
		}
	}
	for i := 0; i < styp.NumFields(); i++ {
		ft := styp.Field(i).Type()
		if _, isSlice := types.Unalias(ft).(*types.Slice); !isSlice {
			continue
		}
		val, ok := vals[styp.Field(i).Name()]
		if !ok {
			return false
		}
		pv, ok := w.pc.pf.values[val]
		if !ok || !pv.mapped {
			return false
		}
	}
	return true
}

// unprovenVarCallee reports whether fun is called through a
// function-typed variable or a struct function field: a callee the scan
// cannot resolve to one body at this call site, even when the variable is
// an approved formal parameter (whose bound callbacks are all scanned).
func (w *fileRules) unprovenVarCallee(fun ast.Expr) bool {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		_, ok := w.pc.info.Uses[f].(*types.Var)
		return ok
	case *ast.SelectorExpr:
		_, ok := w.pc.info.Uses[f.Sel].(*types.Var)
		return ok
	}
	return false
}

// pageFieldPromoted reports whether the flow pass synthesized e's
// whole-value page taint from a struct-field taint (b.Data = page with
// b itself clean; cb(b)). Opaque-call rules use the marker to fail
// calls handing a field-hidden complete page to a callee body the call
// site cannot see, while whole-value page arguments (a store callback
// receiving the mapped page itself by contract) stay benign for
// approved callees.
func (w *fileRules) pageFieldPromoted(e ast.Expr) bool {
	return w.pc.pf != nil && w.pc.pf.fieldPromoted[e]
}

// pageDerivedByte reports whether a scalar RHS expression derives from
// a page-tainted container: page[i], page[k], or a variable bound to one.
func (w *fileRules) pageDerivedByte(e ast.Expr) bool {
	e = unparen(e)
	// A direct index whose base is a page-tainted slice derives its byte
	// from the page even though the byte value itself is clean.
	if ix, ok := e.(*ast.IndexExpr); ok {
		if pv := w.pageValue(ix.X); pv.tainted {
			return true
		}
	}
	// A local variable holds a byte read earlier in the loop; flow still
	// records page-derived byte values as clean, so conservatively treat
	// any identifier RHS inside this loop context as page-derived. The
	// enclosing mark proves the destination is in a page-sourcing loop.
	if _, ok := e.(*ast.Ident); ok {
		return true
	}
	// A helper call may compute from the page byte (b+1, transform(b)).
	if _, ok := e.(*ast.CallExpr); ok {
		return true
	}
	// A binary expression containing a page-derived identifier.
	if be, ok := e.(*ast.BinaryExpr); ok {
		return w.pageDerivedByte(be.X) || w.pageDerivedByte(be.Y)
	}
	return w.pageValue(e).tainted
}

// checkAssign flags assignment-side launders and page conversions.
func (w *fileRules) checkAssign(v *ast.AssignStmt) {
	for i, rhs := range v.Rhs {
		if i >= len(v.Lhs) {
			break
		}
		w.checkLaunderValue(v.Lhs[i].Pos(), rhs, w.typeOf(v.Lhs[i]))
		if pv := w.pageValue(rhs); pv.tainted && pageFull(pv) {
			dst := lhsTypeForCheck(w, v.Lhs[i])
			if dst == nil {
				// Short-var LHS idents do not always carry a type in
				// info.Types; the conversion expression's own type is
				// the same string/array shape.
				dst = w.typeOf(rhs)
			}
			w.checkArrayConversionSink(v.Lhs[i].Pos(), dst, pv)
		}
		// An element write into a page-sourced buffer (marked by the
		// pageflow engine inside a page-sourcing loop) is a complete-page
		// copy: the buffer has received PageSize element writes from a
		// page-tainted source.
		if lv := w.pageValue(v.Lhs[i]); lv.tainted && pageFull(lv) {
			if ix, ok := unparen(v.Lhs[i]).(*ast.IndexExpr); ok {
				// Element writes into a MAPPED page (page[i] = b) stay
				// inside the mapping: the writer's normal mutation never
				// creates owned page bytes. The aggregation rule targets
				// owned destinations only.
				if bpv := w.pageValue(ix.X); bpv.tainted && bpv.mapped {
					continue
				}
				// A destination-ranging loop (for i := range out) may
				// only initialize the buffer (out[i] = 0); it is a copy
				// only when the RHS derives from a page. A page-sourcing
				// loop always writes page bytes.
				destOnly := w.pc.pf != nil && w.pc.pf.destAggregated[v.Lhs[i]]
				if !destOnly || w.pageDerivedByte(rhs) {
					w.fail(v.Lhs[i].Pos(), "element-wise copy of a mapped page into an owned buffer (complete page)")
				}
			}
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
	// A type assertion that RECOVERS a file descriptor from an
	// interface value (factory().(*os.File) with factory func()
	// io.Reader) names the descriptor as a typed file the capability
	// walk can see downstream. The interface's static type cannot
	// prove the descriptor is absent (os.File satisfies io.Reader),
	// and an unscanned producer can mint one, so the recovery itself
	// fails closed; only a method-set interface that *os.File does
	// not implement stays benign.
	if src != nil && dst != nil && fileValueType(dst, map[types.Type]bool{}) && isInterfaceType(src) &&
		(types.NewMethodSet(src).Len() == 0 || w.fileImplementableInterface(src)) {
		w.fail(v.Pos(), "file-bearing value asserted out of an interface value (capability launder)")
	}
}

// checkTypeSwitch flags a type switch that RECOVERS a file descriptor
// from an interface value (switch v := factory().(type) { case *os.File:
// ... }): like the explicit assertion form, the guarded interface's
// static type cannot prove the descriptor is absent, and an unscanned
// producer can mint one, so the case itself fails closed. Benign
// switches over concrete (non-interface) values and default-only or
// non-file case lists stay clean.
func (w *fileRules) checkTypeSwitch(v *ast.TypeSwitchStmt) {
	var guard ast.Expr
	switch a := v.Assign.(type) {
	case *ast.AssignStmt:
		if len(a.Rhs) == 1 {
			if ta, ok := unparen(a.Rhs[0]).(*ast.TypeAssertExpr); ok {
				guard = ta.X
			}
		}
	case *ast.ExprStmt:
		if ta, ok := unparen(a.X.(*ast.TypeAssertExpr)).(*ast.TypeAssertExpr); ok {
			guard = ta.X
		}
	}
	if guard == nil {
		return
	}
	src := w.typeOf(guard)
	if src == nil || !isInterfaceType(src) ||
		(types.NewMethodSet(src).Len() != 0 && !w.fileImplementableInterface(src)) {
		return
	}
	for _, st := range v.Body.List {
		cc, ok := st.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, caseExpr := range cc.List {
			if ct := w.typeOf(caseExpr); ct != nil && fileValueType(ct, map[types.Type]bool{}) {
				w.fail(v.Pos(), "file-bearing value recovered from an interface value by a type switch (capability launder)")
				return
			}
		}
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
	// string(page) materializes a complete mapped page in owned memory.
	// hasSrc values are normally caller-bound (the copy-parameter and
	// string-parameter call-site rules decide), but a PROVABLY mapped
	// parameter-sourced value - a seeded store-callback formal, whose
	// every invocation receives a mapped view by contract - has no
	// caller-supplied bound to decide anything: the conversion fails
	// here like any other full mapped page (round-8 qwen P2 shape).
	if b, ok := dst.(*types.Basic); ok && b.Kind() == types.String && pv.tainted && (!pv.hasSrc || pv.mapped) && !definiteSubPage(pv) {
		w.fail(pos, "string conversion of a full-page view")
	}
}

// fileImplementableInterface reports whether a value of interface type
// t can itself BE a *os.File at runtime: os.File's method set satisfies
// the interface's method set. An unproven call returning such an
// interface can mint the mapping owner's descriptor, so the capability
// check treats it like an empty-interface result. Interfaces with a
// union type set are not a single concrete shape: the precise method-set
// subset test only applies to plain method-set interfaces.
func (w *fileRules) fileImplementableInterface(t types.Type) bool {
	if t == nil || !isInterfaceType(t) {
		return false
	}
	iface, ok := types.Unalias(t).Underlying().(*types.Interface)
	if !ok || !iface.IsMethodSet() {
		return false
	}
	var osFile *types.Named
	for _, imp := range w.pc.pkg.Imports() {
		if imp.Path() != "os" {
			continue
		}
		if tn, ok := imp.Scope().Lookup("File").(*types.TypeName); ok {
			osFile, _ = tn.Type().(*types.Named)
		}
	}
	if osFile == nil {
		return false
	}
	return types.Implements(types.NewPointer(osFile), iface)
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

// carrierText renders a carrier argument for fence diagnostics: the
// expression text when it names a value, else the value's type name
// (a composite literal arg to a nested-carrier call reads as its
// struct type instead of "?").
func (w *fileRules) carrierText(e ast.Expr) string {
	if t := calleeText(e); t != "?" {
		return t
	}
	if tv, ok := w.pc.info.Types[unparen(e)]; ok && tv.Type != nil {
		if n, ok := types.Unalias(tv.Type).(*types.Named); ok {
			return n.Obj().Name()
		}
		return tv.Type.String()
	}
	return "carrier"
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
	if strings.HasSuffix(path, "internal/mapping/mapping_sync_darwin.go") {
		// macOS durability requires fcntl(F_FULLFSYNC), the only fcntl
		// variant in the banned content-transfer set. The call passes a
		// raw descriptor number and moves no mapped bytes, so it is
		// exempt ONLY as the exact three-argument call whose command
		// argument is the x/sys constant unix.F_FULLFSYNC: any other
		// FcntlInt command (F_DUPFD, F_DUPFD_CLOEXEC, F_PREALLOCATE, ...)
		// added to this file must still be rejected.
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "FcntlInt" || len(call.Args) < 2 {
				return true
			}
			recv, ok := unparen(sel.X).(*ast.Ident)
			if !ok || recv.Name != "unix" {
				return true
			}
			cmd, ok := unparen(call.Args[1]).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			cmdPkg, ok := unparen(cmd.X).(*ast.Ident)
			if !ok || cmdPkg.Name != "unix" || cmd.Sel.Name != "F_FULLFSYNC" {
				return true
			}
			exempts[sel.Pos()] = true
			return true
		})
		return exempts
	}
	if strings.HasSuffix(path, "internal/writer/reclaim.go") {
		// The commit nonce needs the OS CSPRNG (Rust random::nonzero_128
		// over getrandom). crypto/rand.Read is the only API for it and the
		// name collides with the content-transfer Read ban, but the call
		// fills an owned 16-byte buffer and can never move mapped bytes.
		// Only the exact single-argument crypto/rand.Read shape is
		// exempt here; any other receiver (an aliased non-crypto rand)
		// or any other Read shape must still be rejected.
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Read" || len(call.Args) != 1 {
				return true
			}
			recv, ok := unparen(sel.X).(*ast.Ident)
			if !ok || recv.Name != "rand" {
				return true
			}
			// The receiver ident must resolve to crypto/rand's Read: an
			// aliased import, a struct field, or any other selector of
			// the same name must not inherit the exemption. The
			// argument must be an owned buffer - a mapped page view or
			// a file-bearing value stays banned.
			if obj, ok := w.pc.info.Uses[sel.Sel].(*types.Func); !ok || obj.Pkg() == nil ||
				obj.Pkg().Path() != "crypto/rand" {
				return true
			}
			a0 := call.Args[0]
			if w.pageValue(a0).tainted ||
				(w.typeOf(a0) != nil && fileValueType(w.typeOf(a0), map[types.Type]bool{})) {
				return true
			}
			exempts[sel.Pos()] = true
			return true
		})
		return exempts
	}
	if strings.HasSuffix(path, "internal/writer/metadata.go") {
		// The writer metadata compressor is the second legal bounded
		// in-memory payload (Rust metadata.rs compress; SOW-0025 D4):
		// caller-owned metadata bytes move through a zlib-framed deflate
		// stream and the stored-zlib fallback into an owned buffer whose
		// length is capped by MetadataCompressedBound, then land through
		// the exact chunk geometry into mapped pages. Exact shapes
		// exempted in THIS file only:
		//
		//   - flate.NewWriter on the compress/flate package ident;
		//   - Write/Close on a non-tainted variable receiver whose
		//     static type is *flate.Writer (the compressor local);
		//   - Write/WriteByte on a non-tainted variable receiver whose
		//     static type is *bytes.Buffer (the owned output local; a
		//     concrete in-memory container can never hide a file);
		//   - copy(page[48:], chunk) where page is the mapped view
		//     formal of a store.Update callback in this file and the
		//     destination is the fixed 48-byte-header slice (the chunk
		//     is bounded by MaxMetadataChunkLen, so the mapping-side
		//     write can never mint an owned complete page).
		//
		// Every other selector and every other builtin call in the file
		// keeps failing closed; battery pins pin the boundary.
		// The exemption is binding-keyed: only the actual Update-callback
		// formal variables are exempt, resolved through go/types var
		// identity. A same-file owned local that merely shares the
		// formal's NAME (e.g. page := make([]byte, format.PageSize))
		// must not inherit the mapped-page copy exemption (battery 321).
		updatePageVars := map[*types.Var]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Update" {
				return true
			}
			lit, ok := unparen(call.Args[1]).(*ast.FuncLit)
			if !ok || lit.Type == nil || lit.Type.Params == nil || len(lit.Type.Params.List) != 1 {
				return true
			}
			field := lit.Type.Params.List[0]
			at, ok := field.Type.(*ast.ArrayType)
			if !ok || at.Len != nil {
				return true
			}
			elt, ok := at.Elt.(*ast.Ident)
			if !ok || elt.Name != "byte" {
				return true
			}
			for _, name := range field.Names {
				if tv, ok := w.pc.info.Defs[name].(*types.Var); ok {
					updatePageVars[tv] = true
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "copy" && len(call.Args) == 2 {
				sl, ok := unparen(call.Args[0]).(*ast.SliceExpr)
				if !ok {
					return true
				}
				pid, ok := unparen(sl.X).(*ast.Ident)
				if !ok || sl.High != nil || sl.Slice3 {
					return true
				}
				tv, ok := w.pc.info.Uses[pid].(*types.Var)
				if !ok || !updatePageVars[tv] {
					return true
				}
				low, ok := sl.Low.(*ast.BasicLit)
				if !ok || low.Kind != token.INT || low.Value != "48" {
					return true
				}
				exempts[call.Pos()] = true
				return true
			}
			sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "NewWriter":
				if id, ok := unparen(sel.X).(*ast.Ident); ok && id.Name == "flate" && len(call.Args) == 2 {
					if obj, ok := w.pc.info.Uses[sel.Sel].(*types.Func); ok && obj.Pkg() != nil && obj.Pkg().Path() == "compress/flate" {
						exempts[sel.Pos()] = true
					}
				}
			case "Write", "WriteByte", "Close":
				if sel.Sel.Name == "Write" || sel.Sel.Name == "WriteByte" {
					if len(call.Args) != 1 {
						return true
					}
				} else if len(call.Args) != 0 {
					return true
				}
				if !isVariableRef(sel.X) || w.pageValue(sel.X).tainted {
					return true
				}
				// The byte argument must not provably alias the mapping
				// (a store-callback page view would be a complete-page
				// copy into the owned compressor/buffer) and must not be
				// a file-bearing value: only the caller-owned metadata
				// payload and small literal headers/trailers qualify.
				if len(call.Args) == 1 {
					a0 := call.Args[0]
					if w.pageValue(a0).mapped {
						return true
					}
					if t := w.typeOf(a0); t != nil && fileValueType(t, map[types.Type]bool{}) {
						return true
					}
				}
				switch concreteTypeName(w.typeOf(sel.X)) {
				case "flate.Writer":
					if sel.Sel.Name == "Write" || sel.Sel.Name == "Close" {
						exempts[sel.Pos()] = true
					}
				case "bytes.Buffer":
					if sel.Sel.Name == "Write" || sel.Sel.Name == "WriteByte" {
						exempts[sel.Pos()] = true
					}
				}
			}
			return true
		})
		return exempts
	}
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
