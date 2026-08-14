package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

// pageflow computes the mapped-page taint of every expression: values that
// alias the mapping (mapping.Page/View results, the mapping data slice,
// reader page helpers, and any slice or record derived from them). The
// complete-page ownership rule consumes these values. The taint is
// interprocedural through small symbolic summaries, so a full-page copy
// through a helper (reader.page, format.DecodeMetadataChunk) stays
// visible.

// maxLen: -1 means unknown (conservatively treated as a full page); n is a
// definite byte length.
const maxUnknown = -1

// symbol is a linear form over parameter indices plus a constant; it
// models integer expressions so slice lengths stay precise across
// function boundaries.
type symbol struct {
	coeff map[int]int64
	c     int64
}

func symConst(v int64) symbol { return symbol{c: v} }
func symParam(i int) symbol   { return symbol{coeff: map[int]int64{i: 1}} }

func (s symbol) isConst() (int64, bool) {
	if len(s.coeff) == 0 {
		return s.c, true
	}
	return 0, false
}

func (s symbol) add(o symbol) symbol {
	out := symbol{coeff: map[int]int64{}, c: s.c + o.c}
	for k, v := range s.coeff {
		out.coeff[k] += v
	}
	for k, v := range o.coeff {
		out.coeff[k] += v
	}
	return out
}

func (s symbol) sub(o symbol) symbol {
	out := symbol{coeff: map[int]int64{}, c: s.c - o.c}
	for k, v := range s.coeff {
		out.coeff[k] += v
	}
	for k, v := range o.coeff {
		out.coeff[k] -= v
	}
	return out
}

// pageValue is the taint of one expression. sym (when valid) is the
// symbolic length of the tainted slice; maxLen is its definite bound when
// known, maxUnknown otherwise.
type pageValue struct {
	tainted bool
	maxLen  int64
	sym     symbol
	hasSym  bool
	// srcParam/srcField record that the taint comes from a function
	// parameter (whole value, or its named struct field). Summaries use
	// them so a helper's result stays tainted exactly when the caller's
	// argument was tainted.
	srcParam int
	srcField string
	hasSrc   bool
}

// maxSrc is one contribution to a tainted result's maxLen. "const"
// carries a constant bound, "paramMax" inherits the maxLen of a tainted
// parameter, "value" inherits the caller-supplied value of a plain length
// parameter (like format.PageSize).
type maxSrc struct {
	param    int
	constVal int64
	kind     string // "const" | "paramMax" | "value" | "param" | "paramField"
	field    string // "paramField": struct field of the argument
}

type fieldTaint struct {
	tainted bool
	srcs    []maxSrc
}

type funcSummary struct {
	results []fieldTaint
	fields  map[string]fieldTaint // struct-result fields (result 0)
	params  int
	// stringParams records parameter indexes whose bytes are converted
	// to an owned string somewhere in the body (return string(p)).
	// Call sites passing a full mapped view to such a parameter create
	// an owned complete-page copy even though the conversion inside the
	// helper is caller-bound and cannot see the bound itself.
	stringParams map[int]bool
	// fmtSpreadParams records parameter indexes that the body spreads
	// into a fmt call (fmt.Sprintf("%s", a...) with a the function's own
	// variadic parameter). The spread itself is exempted at the helper
	// definition (the param source keeps the call call-site-sensitive),
	// but a call site passing a full mapped view into that slot hands
	// the complete page to the owned formatter inside the helper.
	fmtSpreadParams map[int]bool
	// variadic records the parameter slot of a variadic parameter, or -1:
	// a call site binds every trailing argument into that slot (joined),
	// so xs[1] of a func(xs ...[]byte) reads any of the caller's trailing
	// arguments instead of only the first one.
	variadic int
}

// argFlow binds one call-site argument: its value taint and, for struct
// values, the tainted fields of that struct.
type argFlow struct {
	pv     pageValue
	fields map[string]pageValue
}

// eval binds caller argument taints, argument values, and argument struct
// fields to the summary.
func (fs *funcSummary) eval(args []pageValue, argVals []symbol, argFlows []argFlow) (pageValue, map[string]pageValue) {
	val := func(s maxSrc) int64 {
		switch s.kind {
		case "const":
			return s.constVal
		case "paramMax":
			if s.param >= 0 && s.param < len(args) {
				if a := args[s.param]; a.hasSym {
					if c, ok := a.sym.isConst(); ok {
						return c
					}
				}
				return args[s.param].maxLen
			}
			return maxUnknown
		case "param":
			// A result that is literally a parameter keeps the caller's
			// argument bound: a bounded slice argument (page[48:112])
			// stays below a complete page, a full page stays full. The
			// zero symbol must not be read as constant: only hasSym
			// values carry a real bound.
			if s.param >= 0 && s.param < len(args) {
				if a := args[s.param]; a.hasSym {
					if c, ok := a.sym.isConst(); ok {
						return c
					}
				}
				return args[s.param].maxLen
			}
			return maxUnknown
		case "value":
			if s.param >= 0 && s.param < len(argVals) {
				if c, ok := argVals[s.param].isConst(); ok {
					return c
				}
			}
			return maxUnknown
		}
		return maxUnknown
	}
	taintOf := func(s maxSrc) bool {
		switch s.kind {
		case "param":
			return s.param >= 0 && s.param < len(args) && args[s.param].tainted
		case "paramField":
			if s.param >= 0 && s.param < len(argFlows) {
				if fv, ok := argFlows[s.param].fields[s.field]; ok {
					return fv.tainted
				}
			}
			return false
		}
		return true // const / paramMax / value sources are tainted by construction
	}
	out := pageValue{}
	var fields map[string]pageValue
	for _, r := range fs.results {
		// A result with several recorded sources is tainted when ANY
		// source is tainted at the call site: a helper choosing between
		// two parameters (choose(a, b, takeB)) records both, and the
		// first may be a clean nil while the second carries the page.
		if !r.tainted || !anyTainted(r.srcs, taintOf) {
			continue
		}
		out.tainted = true
		for _, s := range r.srcs {
			if !taintOf(s) {
				continue
			}
			if m := val(s); m == maxUnknown || m > out.maxLen {
				out.maxLen = m
			}
		}
	}
	for name, r := range fs.fields {
		if !r.tainted || !anyTainted(r.srcs, taintOf) {
			continue
		}
		if fields == nil {
			fields = map[string]pageValue{}
		}
		pv := pageValue{tainted: true, maxLen: maxUnknown}
		for _, s := range r.srcs {
			if !taintOf(s) {
				continue
			}
			m := val(s)
			if m == maxUnknown || m > pv.maxLen {
				pv.maxLen = m
			}
		}
		fields[name] = pv
	}
	return out, fields
}

// anyTainted reports whether at least one source is tainted at the call
// site; maxLen therefore accumulates only over the tainted sources.
func anyTainted(srcs []maxSrc, taintOf func(maxSrc) bool) bool {
	for _, s := range srcs {
		if taintOf(s) {
			return true
		}
	}
	return false
}

// evalResults returns one concrete value per summarized result slot, so a
// multi-result assignment (a, b := f(page)) distributes taint per slot
// instead of collapsing every rhs into the first left-hand side.
func (fs *funcSummary) evalResults(args []pageValue, argVals []symbol, argFlows []argFlow) []pageValue {
	val := func(s maxSrc) int64 {
		switch s.kind {
		case "const":
			return s.constVal
		case "paramMax":
			if s.param >= 0 && s.param < len(args) {
				if a := args[s.param]; a.hasSym {
					if c, ok := a.sym.isConst(); ok {
						return c
					}
				}
				return args[s.param].maxLen
			}
			return maxUnknown
		case "param":
			// A result that is literally a parameter keeps the caller's
			// argument bound: a bounded slice argument (page[48:112])
			// stays below a complete page, a full page stays full. The
			// zero symbol must not be read as constant: only hasSym
			// values carry a real bound.
			if s.param >= 0 && s.param < len(args) {
				if a := args[s.param]; a.hasSym {
					if c, ok := a.sym.isConst(); ok {
						return c
					}
				}
				return args[s.param].maxLen
			}
			return maxUnknown
		case "value":
			if s.param >= 0 && s.param < len(argVals) {
				if c, ok := argVals[s.param].isConst(); ok {
					return c
				}
			}
			return maxUnknown
		}
		return maxUnknown
	}
	taintOf := func(s maxSrc) bool {
		switch s.kind {
		case "param":
			return s.param >= 0 && s.param < len(args) && args[s.param].tainted
		case "paramField":
			if s.param >= 0 && s.param < len(argFlows) {
				if fv, ok := argFlows[s.param].fields[s.field]; ok {
					return fv.tainted
				}
			}
			return false
		}
		return true
	}
	out := make([]pageValue, len(fs.results))
	for i, r := range fs.results {
		if !r.tainted || !anyTainted(r.srcs, taintOf) {
			continue
		}
		pv := pageValue{tainted: true}
		for _, s := range r.srcs {
			if !taintOf(s) {
				continue
			}
			if m := val(s); m == maxUnknown || m > pv.maxLen {
				pv.maxLen = m
			}
		}
		out[i] = pv
	}
	return out
}

// summaryStore holds the computed summaries of every module package.
type summaryStore struct {
	pkgs map[string]map[string]*funcSummary // import path -> summaries
}

func newSummaryStore() *summaryStore {
	return &summaryStore{pkgs: map[string]map[string]*funcSummary{}}
}

// pageFlow is the interpreter for one package's rules pass.
type pageFlow struct {
	pc         *packageCheck
	path       string
	summaries  map[string]*funcSummary // current package
	store      *summaryStore
	values     map[ast.Expr]pageValue
	callFields map[*ast.CallExpr]map[string]pageValue // struct-result fields of the last evaluated calls
	// callFieldsFailClosed records calls whose callFields are worst-case
	// fail-closed over-approximations (an opaque callee's struct result
	// MAY hold a mapped page in every byte-carrying field). Field reads
	// consult them so an opaque result named into a sink fails closed,
	// but promoteFullPageFields must not graduate them into whole-value
	// full-page taint: that would flag clean stdlib values (io.NopCloser(
	// x.Get()()) over a bytes.Reader-like result) as complete-page
	// transfers. Only fields proven by summaries/literals/stores are
	// promoted.
	callFieldsFailClosed map[*ast.CallExpr]bool
	callResults          map[*ast.CallExpr][]pageValue // per-slot results of the last evaluated module calls
	// callMethodValues records calls resolved through a method VALUE
	// stored in a local or package variable (get := b.String; get()):
	// the resolved method and its receiver expression. The rule pass
	// uses them for call-site checks (string-param conversions on the
	// receiver) that a bare callee lookup cannot see.
	callMethodValues map[*ast.CallExpr]methodValueCall
	accum            bool // final sweep: keep expression caches for the rule pass
}

// methodValueCall is one resolved method-value call: the method and the
// receiver expression bound at the binding site.
type methodValueCall struct {
	fn   *types.Func
	recv ast.Expr
}

// clearExprCaches resets the per-analysis expression caches. Every
// fixpoint pass clears them so a value computed against a less stable
// callee summary is never reused later in the iteration; the final
// accumulation sweep (accum) keeps them so the rule pass can look up
// the value of any scanned expression.
func (pf *pageFlow) clearExprCaches() {
	if pf.accum {
		return
	}
	pf.values = map[ast.Expr]pageValue{}
	pf.callFields = map[*ast.CallExpr]map[string]pageValue{}
	pf.callFieldsFailClosed = map[*ast.CallExpr]bool{}
	pf.callResults = map[*ast.CallExpr][]pageValue{}
	pf.callMethodValues = map[*ast.CallExpr]methodValueCall{}
}

// summarizePackage computes the symbolic summaries of one package,
// iterating to a fixpoint so intra-package helper chains compose.
func summarizePackage(pc *packageCheck, path string, store *summaryStore, files []*parsedFile, pf *pageFlow) (map[string]*funcSummary, *pageFlow) {
	if store.pkgs[path] != nil {
		return store.pkgs[path], pf
	}
	pf.pc = pc
	pf.path = path
	pf.store = store
	sums := map[string]*funcSummary{}
	store.pkgs[path] = sums
	pf.summaries = sums

	pkgVars := map[string]pageValue{}
	pkgStructs := map[types.Object]map[string]pageValue{}
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							break
						}
						st := newStmtState(pf, nil, pkgVars, pkgStructs)
						if pv := pf.evalExpr(st, vs.Values[i]); pv.tainted {
							pkgVars[name.Name] = pv
						}
						// Whole-struct package initializers carry their
						// field taints into the shared package field
						// state: var g = B{Data: ...} followed by g.Data
						// in another function must stay tainted.
						obj := pf.pc.info.ObjectOf(name)
						if obj != nil {
							var fields map[string]pageValue
							if lit := structLitOf(vs.Values[i]); lit != nil {
								fields = pf.compositeFields(st, lit)
							} else if call, ok := unparen(vs.Values[i]).(*ast.CallExpr); ok {
								if cf, ok := pf.callFields[call]; ok {
									fields = cf
								}
							}
							if len(fields) > 0 {
								gm := pkgStructs[obj]
								if gm == nil {
									gm = map[string]pageValue{}
									pkgStructs[obj] = gm
								}
								for k, fv := range fields {
									if !fv.tainted {
										continue
									}
									if prev, ok := gm[k]; ok && prev.tainted {
										gm[k] = joinPageValue(prev, fv)
									} else {
										gm[k] = fv
									}
								}
							}
						}
					}
				}
			}
		}
	}
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fs := &funcSummary{fields: map[string]fieldTaint{}, params: countParams(fd), variadic: variadicSlotOf(fd)}
			sums[funcKey(fd)] = fs
			st := newStmtState(pf, fd, pkgVars, pkgStructs)
			pf.analyzeFunc(st, fs)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, f := range files {
			for _, decl := range f.ast.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				key := funcKey(fd)
				before := summaryDup(sums[key])
				st := newStmtState(pf, fd, pkgVars, pkgStructs)
				pf.analyzeFunc(st, sums[key])
				if !summaryEqual(before, sums[key]) {
					changed = true
				}
			}
		}
	}
	// Final accumulation sweep: the rule pass reads per-expression
	// values by lookup after analysis, so the last fixpoint pass (with
	// its cache clears) is not enough. Replay every package variable
	// initializer and function body once against the stabilized summary
	// set, keeping the caches this time, and leave the complete value
	// map behind for the rules.
	pf.accum = true
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							break
						}
						st := newStmtState(pf, nil, pkgVars, pkgStructs)
						if pv := pf.evalExpr(st, vs.Values[i]); pv.tainted {
							pkgVars[name.Name] = pv
						}
						// Whole-struct package initializers carry their
						// field taints into the shared package field
						// state: var g = B{Data: ...} followed by g.Data
						// in another function must stay tainted.
						obj := pf.pc.info.ObjectOf(name)
						if obj != nil {
							var fields map[string]pageValue
							if lit := structLitOf(vs.Values[i]); lit != nil {
								fields = pf.compositeFields(st, lit)
							} else if call, ok := unparen(vs.Values[i]).(*ast.CallExpr); ok {
								if cf, ok := pf.callFields[call]; ok {
									fields = cf
								}
							}
							if len(fields) > 0 {
								gm := pkgStructs[obj]
								if gm == nil {
									gm = map[string]pageValue{}
									pkgStructs[obj] = gm
								}
								for k, fv := range fields {
									if !fv.tainted {
										continue
									}
									if prev, ok := gm[k]; ok && prev.tainted {
										gm[k] = joinPageValue(prev, fv)
									} else {
										gm[k] = fv
									}
								}
							}
						}
					}
				}
			}
		}
	}
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := funcKey(fd)
			st := newStmtState(pf, fd, pkgVars, pkgStructs)
			pf.analyzeFunc(st, sums[key])
		}
	}
	return sums, pf
}

func countParams(fd *ast.FuncDecl) int {
	n := 0
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		n++
	}
	if fd.Type.Params != nil {
		for _, f := range fd.Type.Params.List {
			n += len(f.Names)
		}
	}
	return n
}

// variadicSlotOf returns the parameter slot index of the variadic
// parameter of fd (the receiver is slot 0 of a method), or -1 when the
// function has no variadic parameter.
func variadicSlotOf(fd *ast.FuncDecl) int {
	slot := 0
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		slot = 1
	}
	if fd.Type.Params == nil {
		return -1
	}
	for _, f := range fd.Type.Params.List {
		if _, ok := f.Type.(*ast.Ellipsis); ok {
			return slot
		}
		slot += len(f.Names)
	}
	return -1
}

func funcKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return recvTypeName(fd.Recv.List[0].Type) + "." + fd.Name.Name
}

func recvTypeName(t ast.Expr) string {
	for {
		switch v := t.(type) {
		case *ast.StarExpr:
			t = v.X
		case *ast.IndexExpr:
			t = v.X
		case *ast.IndexListExpr:
			t = v.X
		case *ast.ParenExpr:
			t = v.X
		case *ast.Ident:
			return v.Name
		default:
			return "?"
		}
	}
}

func summaryDup(fs *funcSummary) *funcSummary {
	out := &funcSummary{
		results:  append([]fieldTaint{}, fs.results...),
		fields:   map[string]fieldTaint{},
		params:   fs.params,
		variadic: fs.variadic,
	}
	for k, v := range fs.fields {
		out.fields[k] = v
	}
	if len(fs.stringParams) > 0 {
		out.stringParams = map[int]bool{}
		for k := range fs.stringParams {
			out.stringParams[k] = true
		}
	}
	if len(fs.fmtSpreadParams) > 0 {
		out.fmtSpreadParams = map[int]bool{}
		for k := range fs.fmtSpreadParams {
			out.fmtSpreadParams[k] = true
		}
	}
	return out
}

func summaryEqual(a, b *funcSummary) bool {
	if len(a.results) != len(b.results) || len(a.fields) != len(b.fields) {
		return false
	}
	for i := range a.results {
		if !fieldEqual(a.results[i], b.results[i]) {
			return false
		}
	}
	for k, v := range a.fields {
		if !fieldEqual(v, b.fields[k]) {
			return false
		}
	}
	if !stringParamsEqual(a.stringParams, b.stringParams) {
		return false
	}
	if !stringParamsEqual(a.fmtSpreadParams, b.fmtSpreadParams) {
		return false
	}
	return true
}

func stringParamsEqual(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func fieldEqual(a, b fieldTaint) bool {
	if a.tainted != b.tainted || len(a.srcs) != len(b.srcs) {
		return false
	}
	for i := range a.srcs {
		if a.srcs[i] != b.srcs[i] {
			return false
		}
	}
	return true
}

// stmtState is the interpreter state of one function body.
// stmtState is the interpreter state of one function body. Variables and
// parameters are keyed by their resolved types.Object: a Go identifier has
// a distinct AST node at every mention, so node pointers can never be the
// binding key; the object is the same for the definition and all uses.
type stmtState struct {
	pf         *pageFlow
	fd         *ast.FuncDecl
	params     map[types.Object]int
	stmtVars   map[types.Object]pageValue
	structs    map[types.Object]map[string]pageValue
	pkgVars    map[string]pageValue
	pkgStructs map[types.Object]map[string]pageValue
	// localFuncs records the current func-literal binding of local
	// function-typed variables, so a call through a locally declared
	// closure (id := func(p []byte) []byte { return p }) resolves the
	// literal body instead of dropping the call's result taint.
	localFuncs map[types.Object]*ast.FuncLit
	// localBindings records the most recent expression bound to each
	// local variable. Calls through a variable whose binding is a
	// selector resolve that selector's method (get := r.page; get(1)
	// dispatches to the same method summary as r.page(1)); chains of
	// variable bindings are followed with a depth cap.
	localBindings map[types.Object]ast.Expr
	// ambigBind marks local function variables whose binding diverged
	// across branch paths: no single provable callee exists, so calls
	// through them fail closed instead of resolving one branch's body.
	ambigBind map[types.Object]bool
}

func newStmtState(pf *pageFlow, fd *ast.FuncDecl, pkgVars map[string]pageValue, pkgStructs map[types.Object]map[string]pageValue) *stmtState {
	st := &stmtState{
		pf:            pf,
		fd:            fd,
		params:        map[types.Object]int{},
		stmtVars:      map[types.Object]pageValue{},
		structs:       map[types.Object]map[string]pageValue{},
		pkgVars:       pkgVars,
		pkgStructs:    pkgStructs,
		localFuncs:    map[types.Object]*ast.FuncLit{},
		localBindings: map[types.Object]ast.Expr{},
		ambigBind:     map[types.Object]bool{},
	}
	if pf != nil && pf.pc != nil {
		for k, v := range pf.pc.pkgBindings {
			st.localBindings[k] = v
		}
	}
	if fd != nil {
		idx := 0
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			// The receiver is parameter slot 0 of a method: field reads
			// on the receiver (func (b box2) Get() []byte { return
			// b.Data }) are param-field sources, and call sites that
			// invoke through a method selector bind the receiver
			// expression as the first argument value.
			if len(fd.Recv.List[0].Names) > 0 {
				st.params[pf.pc.info.ObjectOf(fd.Recv.List[0].Names[0])] = 0
			}
			idx = 1
		}
		if fd.Type.Params != nil {
			for _, f := range fd.Type.Params.List {
				for _, name := range f.Names {
					st.params[pf.pc.info.ObjectOf(name)] = idx
					idx++
				}
			}
		}
	}
	return st
}

// objOfDeref resolves the binding object of an identifier expression,
// unwrapping one pointer dereference: (*b).Data and b.Data address the
// same local variable's field state.
func objOfDeref(st *stmtState, e ast.Expr) types.Object {
	e = unparen(e)
	if star, ok := e.(*ast.StarExpr); ok {
		e = unparen(star.X)
	}
	if id, ok := e.(*ast.Ident); ok {
		return st.pf.pc.info.ObjectOf(id)
	}
	return nil
}

// objOf resolves the binding object of an identifier expression.
func objOf(st *stmtState, e ast.Expr) types.Object {
	if st == nil {
		return nil
	}
	if id, ok := unparen(e).(*ast.Ident); ok {
		return st.pf.pc.info.ObjectOf(id)
	}
	return nil
}

func (pf *pageFlow) analyzeFunc(st *stmtState, fs *funcSummary) {
	// The expression cache is per-analysis: it must never serve a value
	// computed in an earlier fixpoint pass, or a call result cached
	// before a callee summary stabilized stays stale for the rest of the
	// iteration (choose/late ordering). Summaries are the only state
	// that crosses passes.
	pf.clearExprCaches()
	fs.results = make([]fieldTaint, 0)
	fs.fields = map[string]fieldTaint{}
	fs.stringParams = map[int]bool{}
	named := map[types.Object]int{}
	if st.fd.Type.Results != nil {
		slot := 0
		for _, r := range st.fd.Type.Results.List {
			if len(r.Names) == 0 {
				fs.results = append(fs.results, fieldTaint{})
				slot++
				continue
			}
			for _, name := range r.Names {
				fs.results = append(fs.results, fieldTaint{})
				named[pf.pc.info.ObjectOf(name)] = slot
				slot++
			}
		}
	}
	pf.analyzeStmts(st, st.fd.Body.List, fs)
	pf.noteStringConvs(st, fs, st.fd.Body)
	pf.noteFmtSpreads(st, fs, st.fd.Body)
	// Named results with a naked return: the body's stores to the named
	// result variables are the function's results. Fields of a named
	// struct result are recorded the same way.
	for obj, slot := range named {
		if pv, ok := st.stmtVars[obj]; ok && pv.tainted {
			fs.results[slot] = joinFieldTaint(fs.results[slot], fieldTaint{tainted: true, srcs: maxSrcOf(pv)})
		}
		if m, ok := st.structs[obj]; ok {
			for k, fv := range m {
				if fv.tainted {
					fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
				}
			}
		}
	}
}

func (pf *pageFlow) analyzeStmts(st *stmtState, list []ast.Stmt, fs *funcSummary) {
	for _, s := range list {
		switch v := s.(type) {
		case *ast.AssignStmt:
			// A multi-left-hand-side assignment of one summarized call
			// distributes the call's per-result taint to the matching
			// slot; the old pair-wise mapping collapsed the whole call
			// into the first lhs and left the others untainted.
			if len(v.Rhs) == 1 && len(v.Lhs) > 1 {
				if call, ok := unparen(v.Rhs[0]).(*ast.CallExpr); ok {
					// Evaluate the call in the CURRENT statement state:
					// a per-result distribution must never consume a
					// stale result cached by an earlier fixpoint pass
					// (that non-monotonicity dropped taint from
					// multi-result helpers).
					delete(pf.values, call)
					pf.evalExpr(st, call)
					if res, ok := pf.callResults[call]; ok {
						for i, lhs := range v.Lhs {
							var pv pageValue
							if i < len(res) {
								pv = res[i]
							}
							pf.bindLocalFunc(st, lhs, nil)
							pf.recordBinding(st, lhs, nil)
							assignTarget(st, lhs, pv)
						}
						// Struct-result calls also record their field
						// taints per slot (chunk, err := Decode(page)
						// keeps chunk.Data page-sourced).
						if fields, ok := pf.callFields[call]; ok {
							for _, lhs := range v.Lhs {
								if obj := objOf(st, lhs); obj != nil {
									if st.structs[obj] == nil {
										st.structs[obj] = map[string]pageValue{}
									}
									for k, fv := range fields {
										st.structs[obj][k] = fv
									}
								}
							}
						}
						delete(pf.callResults, call)
						delete(pf.callFields, call)
						break
					}
				}
			}
			for i, rhs := range v.Rhs {
				if i >= len(v.Lhs) {
					break
				}
				pv := pf.evalExpr(st, rhs)
				var fields map[string]pageValue
				if lit := structLitOf(rhs); lit != nil {
					fields = pf.compositeFields(st, lit)
				} else {
					switch r := unparen(rhs).(type) {
					case *ast.CallExpr:
						fields = pf.callFields[r]
					case *ast.CompositeLit:
						fields = pf.compositeFields(st, r)
					case *ast.IndexExpr:
						// x0 := xs[0] with xs a container of struct
						// elements keeps the element field taints on x0.
						if o := objOf(st, r.X); o != nil {
							if em, ok := st.structs[o]; ok {
								m := map[string]pageValue{}
								for k, fv := range em {
									m[k] = fv
								}
								fields = m
							}
						}
					}
				}
				var lit *ast.FuncLit
				if l, ok := unparen(rhs).(*ast.FuncLit); ok {
					lit = l
				}
				pf.bindLocalFunc(st, v.Lhs[i], lit)
				pf.recordBinding(st, v.Lhs[i], v.Rhs[i])
				pf.materializeStructFields(st, v.Lhs[i], v.Rhs[i])
				assignTarget(st, v.Lhs[i], pv)
				if obj := objOf(st, v.Lhs[i]); obj != nil && len(fields) > 0 {
					if st.structs[obj] == nil {
						st.structs[obj] = map[string]pageValue{}
					}
					for k, fv := range fields {
						st.structs[obj][k] = fv
					}
					// A whole-struct store to a package-level variable
					// joins the shared package field state: g = B{Data:
					// page} in one helper must be visible to field reads
					// in the other functions of the package.
					if obj.Parent() == st.pf.pc.pkg.Scope() && st.pkgStructs != nil {
						gm := st.pkgStructs[obj]
						if gm == nil {
							gm = map[string]pageValue{}
							st.pkgStructs[obj] = gm
						}
						for k, fv := range fields {
							if !fv.tainted {
								continue
							}
							if prev, ok := gm[k]; ok && prev.tainted {
								gm[k] = joinPageValue(prev, fv)
							} else {
								gm[k] = fv
							}
						}
					}
				}
				// An indexed store of a struct value keeps the element
				// field taints on the container: xs[0] = B{Data: page}
				// followed by xs[0].Data must stay tainted.
				if ix, ok := unparen(v.Lhs[i]).(*ast.IndexExpr); ok && len(fields) > 0 {
					if cobj := objOf(st, ix.X); cobj != nil {
						if st.structs[cobj] == nil {
							st.structs[cobj] = map[string]pageValue{}
						}
						for k, fv := range fields {
							if prev, ok := st.structs[cobj][k]; ok {
								st.structs[cobj][k] = joinPageValue(prev, fv)
							} else {
								st.structs[cobj][k] = fv
							}
						}
					}
				}
				// A struct-valued map key (or slice index) keeps its
				// element field taints on the container: m[S{Data:
				// page}] = 1 followed by for k := range m { k.(S).Data }
				// must stay tainted, because the range visits every key.
				if ix, ok := unparen(v.Lhs[i]).(*ast.IndexExpr); ok {
					if cobj := objOf(st, ix.X); cobj != nil {
						if kl := structLitOf(ix.Index); kl != nil {
							if kf := pf.compositeFields(st, kl); len(kf) > 0 {
								if st.structs[cobj] == nil {
									st.structs[cobj] = map[string]pageValue{}
								}
								for k, fv := range kf {
									if prev, ok := st.structs[cobj][k]; ok {
										st.structs[cobj][k] = joinPageValue(prev, fv)
									} else {
										st.structs[cobj][k] = fv
									}
								}
							}
						}
					}
				}
				if call, ok := unparen(rhs).(*ast.CallExpr); ok {
					delete(pf.callFields, call)
				}
			}
		case *ast.DeclStmt:
			if gd, ok := v.Decl.(*ast.GenDecl); ok {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for i, name := range vs.Names {
							if i < len(vs.Values) {
								pv := pf.evalExpr(st, vs.Values[i])
								obj := pf.pc.info.ObjectOf(name)
								var lit *ast.FuncLit
								if l, ok := unparen(vs.Values[i]).(*ast.FuncLit); ok {
									lit = l
								}
								pf.bindLocalFunc(st, name, lit)
								pf.recordBinding(st, name, vs.Values[i])
								pf.materializeStructFields(st, name, vs.Values[i])
								if pv.tainted {
									st.stmtVars[obj] = pv
								} else {
									delete(st.stmtVars, obj)
								}
								// var b B = B{Data: page} keeps the
								// composite field taints on the local
								// variable exactly like the assignment
								// form b = B{Data: page}; a later b.Data
								// read stays tainted.
								var fields map[string]pageValue
								if litv := structLitOf(vs.Values[i]); litv != nil {
									fields = pf.compositeFields(st, litv)
								} else if call, ok := unparen(vs.Values[i]).(*ast.CallExpr); ok {
									fields = pf.callFields[call]
								} else if ix, ok := unparen(vs.Values[i]).(*ast.IndexExpr); ok {
									if o := objOf(st, ix.X); o != nil {
										if em, ok := st.structs[o]; ok {
											m := map[string]pageValue{}
											for k, fv := range em {
												m[k] = fv
											}
											fields = m
										}
									}
								}
								if obj != nil && len(fields) > 0 {
									if st.structs[obj] == nil {
										st.structs[obj] = map[string]pageValue{}
									}
									for k, fv := range fields {
										st.structs[obj][k] = fv
									}
								}
							}
						}
					}
				}
			}
		case *ast.ReturnStmt:
			for i, rv := range v.Results {
				pv := pf.evalExpr(st, rv)
				if i < len(fs.results) {
					if pv.tainted {
						fs.results[i] = joinFieldTaint(fs.results[i], fieldTaint{tainted: true, srcs: maxSrcOf(pv)})
					}
				}
			}
			// Struct-field results are recorded for every returned
			// expression, so a multi-result return (chunk, err) keeps the
			// struct's field taints (chunk.Data) in the summary.
			for _, rv := range v.Results {
				pf.propagateStructResult(st, rv, fs)
			}
		case *ast.IfStmt:
			if v.Init != nil {
				pf.analyzeStmts(st, []ast.Stmt{v.Init}, fs)
			}
			if v.Cond != nil {
				pf.evalExpr(st, v.Cond)
			}
			pre := st.clone()
			pf.analyzeStmts(st, v.Body.List, fs)
			elseS := pre.clone()
			if blk, ok := v.Else.(*ast.BlockStmt); ok {
				pf.analyzeStmts(elseS, blk.List, fs)
			} else if v.Else != nil {
				pf.analyzeStmts(elseS, []ast.Stmt{v.Else}, fs)
			}
			st.joinWith(elseS)
		case *ast.ForStmt:
			if v.Init != nil {
				pf.analyzeStmts(st, []ast.Stmt{v.Init}, fs)
			}
			if v.Cond != nil {
				pf.evalExpr(st, v.Cond)
			}
			pre := st.clone()
			pf.analyzeStmts(st, v.Body.List, fs)
			if v.Post != nil {
				pf.analyzeStmts(st, []ast.Stmt{v.Post}, fs)
			}
			st.joinWith(pre) // zero iterations stay possible
		case *ast.RangeStmt:
			// Range variables are bound from the container's element
			// taint: a page collection ranged over (for _, p := range
			// [][]byte{page}) must taint the loop value, or appends of
			// it inside the body lose the source.
			// A range variable that already holds a local function or
			// method binding is REBOUND by the loop (for _, f = range
			// fs): after the loop the binding is one of the container's
			// elements, so calls through it fail closed.
			for _, rv := range []ast.Expr{v.Key, v.Value} {
				if rv == nil {
					continue
				}
				if obj := objOf(st, rv); obj != nil {
					if _, ok := st.localBindings[obj]; ok {
						st.invalidateFuncBinding(obj)
					}
					if _, ok := st.localFuncs[obj]; ok {
						st.invalidateFuncBinding(obj)
					}
				}
			}
			if v.X != nil {
				// A container of struct elements keeps its element field
				// taints on the range value (for _, x := range []box8{
				// {Data: page}} binds x.Data), like the indexed read
				// path.
				valObj := objOf(st, v.Value)
				keyObj := objOf(st, v.Key)
				if obj := objOf(st, v.X); obj != nil {
					if elemFields, ok := st.structs[obj]; ok && len(elemFields) > 0 {
						// A key-only range of a map (for k := range m)
						// pulls the container's key-field taints out
						// through the key variable; a two-variable range
						// binds them to the value.
						tgt := valObj
						if tgt == nil {
							tgt = keyObj
						}
						if tgt != nil {
							if st.structs[tgt] == nil {
								st.structs[tgt] = map[string]pageValue{}
							}
							for k, fv := range elemFields {
								st.structs[tgt][k] = fv
							}
						}
					}
				} else if cl, ok := unparen(v.X).(*ast.CompositeLit); ok && valObj != nil {
					// The container is an inline literal of struct
					// values: the range value is one element, so the
					// literal's per-element field taints bind the loop
					// variable (for _, x := range []B{{Data: p}}).
					if m := pf.compositeFields(st, cl); len(m) > 0 {
						if st.structs[valObj] == nil {
							st.structs[valObj] = map[string]pageValue{}
						}
						for k, fv := range m {
							st.structs[valObj][k] = fv
						}
					}
				}
				if xpv := pf.evalExpr(st, v.X); xpv.tainted {
					for _, rv := range []ast.Expr{v.Key, v.Value} {
						if rv == nil {
							continue
						}
						if obj := objOf(st, rv); obj != nil && typeCanCarryPage(obj.Type()) {
							st.stmtVars[obj] = derivedPageValue(xpv)
						}
					}
				}
			}
			pre := st.clone()
			pf.analyzeStmts(st, v.Body.List, fs)
			st.joinWith(pre) // zero iterations stay possible
		case *ast.ExprStmt:
			pf.evalExpr(st, v.X)
		case *ast.SendStmt:
			// A send of a page view taints the channel variable; a later
			// receive (p := <-ch) derives the taint from it.
			pv := pf.evalExpr(st, v.Value)
			assignTarget(st, v.Chan, pv)
		case *ast.SwitchStmt:
			if v.Init != nil {
				pf.analyzeStmts(st, []ast.Stmt{v.Init}, fs)
			}
			if v.Tag != nil {
				pf.evalExpr(st, v.Tag)
			}
			pf.switchJoin(st, v.Body.List, fs)
		case *ast.TypeSwitchStmt:
			if v.Init != nil {
				pf.analyzeStmts(st, []ast.Stmt{v.Init}, fs)
			}
			if v.Assign != nil {
				pf.analyzeStmts(st, []ast.Stmt{v.Assign}, fs)
			}
			pf.switchJoin(st, v.Body.List, fs)
		case *ast.DeferStmt:
			pf.evalExpr(st, v.Call)
		case *ast.GoStmt:
			pf.evalExpr(st, v.Call)
		case *ast.SelectStmt:
			pre := st.clone()
			first := true
			for _, c := range v.Body.List {
				cc, ok := c.(*ast.CommClause)
				if !ok {
					continue
				}
				branch := st
				if !first {
					branch = pre.clone()
				}
				switch comm := cc.Comm.(type) {
				case *ast.SendStmt:
					pv := pf.evalExpr(branch, comm.Value)
					assignTarget(branch, comm.Chan, pv)
				case *ast.AssignStmt:
					// Receive form (p := <-ch): reuses the normal
					// assignment path including the ARROW receive taint.
					pf.analyzeStmts(branch, []ast.Stmt{comm}, fs)
				case *ast.ExprStmt:
					pf.evalExpr(branch, comm.X)
				}
				pf.analyzeStmts(branch, cc.Body, fs)
				if !first {
					st.joinWith(branch)
				}
				first = false
			}
		case *ast.LabeledStmt:
			pf.analyzeStmts(st, []ast.Stmt{v.Stmt}, fs)
		case *ast.BlockStmt:
			pf.analyzeStmts(st, v.List, fs)
		}
	}
}

// switchJoin analyzes every case body from the pre-switch state and joins
// the results, so a taint introduced on any path survives the switch.
func (pf *pageFlow) switchJoin(st *stmtState, body []ast.Stmt, fs *funcSummary) {
	pre := st.clone()
	first := true
	var fallState *stmtState
	for _, c := range body {
		cc, ok := c.(*ast.CaseClause)
		if !ok || len(cc.Body) == 0 {
			continue
		}
		branch := st
		if !first {
			branch = pre.clone()
		}
		if fallState != nil {
			// The previous case ends with fallthrough: its end state is
			// visible in this case's body.
			branch.joinWith(fallState)
		}
		pf.analyzeStmts(branch, cc.Body, fs)
		if !first {
			st.joinWith(branch)
		}
		first = false
		falls := false
		for _, b := range cc.Body {
			if br, ok := b.(*ast.BranchStmt); ok && br.Tok == token.FALLTHROUGH {
				falls = true
				break
			}
		}
		if falls {
			fallState = branch.clone()
		} else {
			fallState = nil
		}
	}
}

// bindLocalFunc records or clears the func-literal binding of a function
// variable assignment; a binding is cleared when the rhs is not a literal.
func (pf *pageFlow) bindLocalFunc(st *stmtState, lhs ast.Expr, lit *ast.FuncLit) {
	obj := objOf(st, lhs)
	if obj == nil {
		// A store through a pointer (*p = f) rebinds the pointed-to
		// variable: when the pointer's own binding provably names the
		// target (p := &f) that target's callable binding is gone;
		// otherwise every local callable binding is unproven after the
		// write. Both shapes fail closed on later calls.
		if star, ok := unparen(lhs).(*ast.StarExpr); ok {
			if p := objOf(st, star.X); p != nil {
				if bind, ok := st.localBindings[p]; ok {
					if ua, ok := unparen(bind).(*ast.UnaryExpr); ok && ua.Op == token.AND {
						if o := objOf(st, ua.X); o != nil {
							st.invalidateFuncBinding(o)
							return
						}
					}
				}
			}
			st.invalidateAllFuncBindings()
			return
		}
		return
	}
	if lit != nil {
		st.localFuncs[obj] = lit
	} else {
		delete(st.localFuncs, obj)
	}
}

// invalidateFuncBinding drops the recorded callable binding of one
// variable and marks it ambiguous, so later calls through it fail
// closed. Package-scope objects keep their initializer resolution
// (their reassignments are policed by reassignedVars instead).
func (st *stmtState) invalidateFuncBinding(obj types.Object) {
	delete(st.localBindings, obj)
	delete(st.localFuncs, obj)
	if obj.Parent() != st.pf.pc.pkg.Scope() {
		st.ambigBind[obj] = true
	}
}

// invalidateAllFuncBindings drops every recorded local callable binding
// after an unprovable rebinding (an unknown pointer write, a range
// sweep): no binding of the current state is trusted afterwards.
func (st *stmtState) invalidateAllFuncBindings() {
	for obj := range st.localFuncs {
		st.invalidateFuncBinding(obj)
	}
	for obj := range st.localBindings {
		st.invalidateFuncBinding(obj)
	}
}

// recordBinding stores the expression currently bound to a local
// variable, used to resolve calls through method values and function
// variable aliases. The binding is recorded for the identifier targets
// of plain assignments; a binding is cleared by blank or multi-target
// assignment (where no single provable value exists).
func (pf *pageFlow) recordBinding(st *stmtState, lhs ast.Expr, rhs ast.Expr) {
	obj := objOf(st, lhs)
	if obj == nil {
		return
	}
	if st.ambigBind[obj] {
		// An ambiguous rebinding must not be re-recorded from a later
		// statement: the binding is unknown, not this expression.
		delete(st.localBindings, obj)
		return
	}
	if rhs == nil {
		delete(st.localBindings, obj)
		return
	}
	switch unparen(rhs).(type) {
	case *ast.CallExpr, *ast.FuncLit:
		delete(st.localBindings, obj)
	default:
		st.localBindings[obj] = rhs
	}
}

// materializeStructFields copies the field knowledge of src (a struct
// variable, its address, or a dereference of it) onto dst, so a plain
// identity copy keeps the per-field taints: c := b (b a struct parameter
// holding a caller page in one field) followed by c.Data must resolve
// like b.Data. Parameter fields are materialized per declared field from
// the parameter's static type; local struct-var and package-level field
// maps are copied as recorded.
func (pf *pageFlow) materializeStructFields(st *stmtState, dst ast.Expr, src ast.Expr) {
	var id *ast.Ident
	switch e := unparen(src).(type) {
	case *ast.Ident:
		id = e
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			id = identOf(e.X)
		}
	case *ast.StarExpr:
		id = identOf(e.X)
	}
	if id == nil {
		return
	}
	dstObj := objOf(st, dst)
	srcObj := pf.pc.info.ObjectOf(id)
	if dstObj == nil || srcObj == nil {
		return
	}
	var fields map[string]pageValue
	if m, ok := st.structs[srcObj]; ok {
		fields = m
	} else if gm, ok := st.pkgStructs[srcObj]; ok {
		fields = gm
	}
	// Parameter structs keep a full per-field fallback on the copy:
	// fields the local state never recorded (or recorded clean for
	// another field only) still arrive from the caller, so a partial
	// local record must not suppress the taint of the untouched fields.
	if idx, ok := st.params[srcObj]; ok {
		for path, ft := range paramLeafPaths(srcObj.Type()) {
			if !paramCanCarryPage(ft) {
				continue
			}
			if _, recorded := fields[path]; recorded {
				continue
			}
			if fields == nil {
				fields = map[string]pageValue{}
			}
			fields[path] = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: path, hasSrc: true}
		}
	}
	if len(fields) == 0 {
		return
	}
	if st.structs[dstObj] == nil {
		st.structs[dstObj] = map[string]pageValue{}
	}
	for k, fv := range fields {
		st.structs[dstObj][k] = fv
	}
}

// resolveMethodValue follows a variable's recorded binding to a method
// value: get := r.page then get(1) resolves the same method as r.page(1),
// with the same receiver expression. Chains of plain variable aliases are
// followed with a depth cap; anything else is unresolvable.
func (pf *pageFlow) resolveMethodValue(st *stmtState, v *types.Var, depth int) (*types.Func, ast.Expr) {
	if v == nil || depth > 3 || pf.pc.reassignedVars[v] || (st != nil && st.ambigBind[v]) {
		return nil, nil
	}
	b, ok := st.localBindings[v]
	if !ok {
		return nil, nil
	}
	switch ub := unparen(b).(type) {
	case *ast.SelectorExpr:
		fn, ok := pf.pc.info.Uses[ub.Sel].(*types.Func)
		if !ok {
			return nil, nil
		}
		if sig, ok := fn.Type().(*types.Signature); !ok || sig.Recv() == nil {
			return nil, nil
		}
		if selRecv, isSel := pf.pc.info.Selections[ub]; isSel && selRecv.Kind() == types.MethodVal {
			// Method value (r.page): the receiver is the selector's X
			// expression and binds the summary's receiver slot.
			return fn, ub.X
		}
		// Method EXPRESSION (box.Get): the binding names the type, not a
		// value; the call's first argument is the receiver and lands in
		// the receiver slot naturally (evalCall leaves recvExpr nil and
		// binds args[0] to slot 0). Returning ub.X (the type expression)
		// would push a clean type value into the receiver slot and shift
		// the real receiver out of reach.
		return fn, nil
	case *ast.Ident:
		if ov, ok := pf.pc.info.Uses[ub].(*types.Var); ok {
			return pf.resolveMethodValue(st, ov, depth+1)
		}
	}
	return nil, nil
}

// resolveMethodExpr handles method EXPRESSIONS calling the receiver
// explicitly: R.page(r, pgno) already carries the receiver as its first
// argument, so the call needs no receiver prefix; the selector check
// below distinguishes the two shapes via types.Info.Selections.
func (pf *pageFlow) resolveMethodExpr(call *ast.CallExpr) (ast.Expr, bool) {
	sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	if _, ok := pf.pc.info.Uses[sel.Sel].(*types.Func); !ok {
		return nil, false
	}
	if selRecv, ok := pf.pc.info.Selections[sel]; ok && selRecv.Kind() == types.MethodVal {
		return sel.X, true
	}
	return nil, false
}

// maxSrcOf converts a concrete pageValue into summary sources (constants,
// single-parameter value dependencies, and param-sourced taints survive).
func maxSrcOf(pv pageValue) []maxSrc {
	if pv.hasSrc {
		if pv.srcField != "" {
			return []maxSrc{{param: pv.srcParam, kind: "paramField", field: pv.srcField}}
		}
		return []maxSrc{{param: pv.srcParam, kind: "param"}}
	}
	if pv.hasSym {
		if c, ok := pv.sym.isConst(); ok {
			return []maxSrc{{param: -1, kind: "const", constVal: c}}
		}
		if len(pv.sym.coeff) == 1 && pv.sym.c == 0 {
			for p := range pv.sym.coeff {
				return []maxSrc{{param: p, kind: "value"}}
			}
		}
		return []maxSrc{{param: -1, kind: "const", constVal: maxUnknown}}
	}
	if pv.maxLen >= 0 {
		return []maxSrc{{param: -1, kind: "const", constVal: pv.maxLen}}
	}
	return []maxSrc{{param: -1, kind: "const", constVal: maxUnknown}}
}

func assignTarget(st *stmtState, lhs ast.Expr, pv pageValue) {
	switch v := unparen(lhs).(type) {
	case *ast.Ident:
		obj := st.pf.pc.info.ObjectOf(v)
		if st.pkgVars != nil && obj != nil && obj.Parent() == st.pf.pc.pkg.Scope() {
			// Package-scope stores are visible to every function of the
			// package: the shared map keeps cross-function state flows
			// (a global assigned a page in one helper, read by another)
			// visible to summaries. Writes JOIN instead of replacing:
			// two functions may store different bounds (setFull stores
			// the whole page, setBound a one-byte slice) and the global
			// can hold either at a call site, so the conservative join
			// (larger bound wins, unknown dominates) is the sound state.
			// A clean store never erases a page taint another function
			// introduced, or the summary fixpoint would cycle.
			if pv.tainted {
				if prev, ok := st.pkgVars[v.Name]; ok && prev.tainted {
					st.pkgVars[v.Name] = joinPageValue(prev, pv)
				} else {
					st.pkgVars[v.Name] = pv
				}
			}
		}
		if pv.tainted {
			st.stmtVars[obj] = pv
		} else {
			delete(st.stmtVars, obj)
		}
	case *ast.SelectorExpr:
		// b.Data = p, (*b).Data = p, and o.Inner.Data = p: the selector
		// chain resolves the base object and the flattened field path
		// (nested stores record "Inner.Data"), so the matching read path
		// (selectorChain in evalExpr) sees the taint.
		if obj, path := selectorChain(st, v); obj != nil {
			m := st.structs[obj]
			if m == nil {
				m = map[string]pageValue{}
				st.structs[obj] = m
			}
			if obj.Parent() != st.pf.pc.pkg.Scope() {
				// Local variables: a clean store to one field records a
				// clean marker instead of deleting the entry, so a later
				// read of a DIFFERENT field still falls back to its
				// parameter source (sink6 writes b.Other after the
				// caller stored a page into b.Data) while a read of the
				// clean-stored field itself stays clean.
				m[path] = pv
			} else if pv.tainted {
				m[path] = pv
			} else {
				// Package-level variables: the shared pkgStructs map is
				// the cross-function authority, so a clean local store
				// removes the local entry and the read falls through to
				// the package state (which only records tainted field
				// stores and joins them).
				delete(m, path)
			}
			if obj.Parent() == st.pf.pc.pkg.Scope() && st.pkgStructs != nil {
				if !pv.tainted {
					return
				}
				gm := st.pkgStructs[obj]
				if gm == nil {
					gm = map[string]pageValue{}
					st.pkgStructs[obj] = gm
				}
				if prev, ok := gm[path]; ok && prev.tainted {
					gm[path] = joinPageValue(prev, pv)
				} else {
					gm[path] = pv
				}
			}
		}
	case *ast.IndexExpr:
		// An element store (slots[0] = page, m[k] = page) makes the
		// container itself page-carrying; element reads derive the taint.
		// A clean store to another element (slots[1] = []byte{0}) must
		// never erase the container taint: element writes join, they do
		// not replace the whole container value.
		if pv.tainted {
			assignTarget(st, v.X, pv)
		}
		// The store's INDEX is a possible page carrier too: m[&page] = 1
		// keeps the full view reachable through a later range of the
		// keys, so a tainted key joins the container value.
		if kpv := st.pf.evalExpr(st, v.Index); kpv.tainted {
			assignTarget(st, v.X, kpv)
		}
	case *ast.IndexListExpr:
		if pv.tainted {
			assignTarget(st, v.X, pv)
		}
	case *ast.StarExpr:
		// A dereference store (*holder = page) marks the pointed-to
		// variable; dereference reads propagate it.
		assignTarget(st, v.X, pv)
	}
}

// joinPageValue merges two possible values of one variable conservatively:
// a tainted path wins, and the larger bound wins (unknown means a possible
// full page). Used at branch and loop merges so a full-page assignment on
// one path is not forgotten because another path ends clean.
func joinPageValue(a, b pageValue) pageValue {
	if !a.tainted {
		return b
	}
	if !b.tainted {
		return a
	}
	if a.maxLen == maxUnknown || b.maxLen == maxUnknown {
		return pageValue{tainted: true, maxLen: maxUnknown}
	}
	if a.maxLen >= b.maxLen {
		return a
	}
	return b
}

// joinFieldTaint merges two possible taint records of one summary result:
// a value tainted on any path keeps the union of its length sources.
func joinFieldTaint(a, b fieldTaint) fieldTaint {
	if !a.tainted {
		return b
	}
	if !b.tainted {
		return a
	}
	out := fieldTaint{tainted: true}
	seen := map[maxSrc]bool{}
	for _, src := range append(append([]maxSrc{}, a.srcs...), b.srcs...) {
		if !seen[src] {
			seen[src] = true
			out.srcs = append(out.srcs, src)
		}
	}
	return out
}

// clone copies the statement state for branch analysis; the expression
// cache and call-field map stay shared (both are keyed by AST node).
func (st *stmtState) clone() *stmtState {
	cp := *st
	cp.stmtVars = map[types.Object]pageValue{}
	for k, v := range st.stmtVars {
		cp.stmtVars[k] = v
	}
	cp.structs = map[types.Object]map[string]pageValue{}
	for k, v := range st.structs {
		m := map[string]pageValue{}
		for fk, fv := range v {
			m[fk] = fv
		}
		cp.structs[k] = m
	}
	cp.localFuncs = map[types.Object]*ast.FuncLit{}
	for k, v := range st.localFuncs {
		cp.localFuncs[k] = v
	}
	cp.localBindings = map[types.Object]ast.Expr{}
	for k, v := range st.localBindings {
		cp.localBindings[k] = v
	}
	cp.ambigBind = map[types.Object]bool{}
	for k := range st.ambigBind {
		cp.ambigBind[k] = true
	}
	return &cp
}

// joinWith merges another possible state into this one: every variable or
// struct field set in other becomes a possible value here.
func (st *stmtState) joinWith(other *stmtState) {
	// Ambiguity is sticky across BOTH directions: a divergence created
	// inside a nested branch of the other path (an else-nested if) must
	// invalidate this path's binding too, or the join would re-establish
	// a provable callee from the branch that stayed clean while the
	// page-returning branch is still possible.
	for k := range other.ambigBind {
		if st.ambigBind[k] {
			continue
		}
		st.ambigBind[k] = true
		delete(st.localFuncs, k)
		delete(st.localBindings, k)
	}
	for k, v := range other.stmtVars {
		if cur, ok := st.stmtVars[k]; ok {
			st.stmtVars[k] = joinPageValue(cur, v)
		} else {
			st.stmtVars[k] = v
		}
	}
	for k, m := range other.structs {
		cur := st.structs[k]
		if cur == nil {
			cur = map[string]pageValue{}
			st.structs[k] = cur
		}
		for fk, fv := range m {
			if cf, ok := cur[fk]; ok {
				cur[fk] = joinPageValue(cf, fv)
			} else {
				// The field exists on this path only. A clean local
				// store on one path must not erase the other path's
				// fallback (a parameter or package field that still
				// holds the caller's page): taint wins, clean degrades
				// to the fallback by deleting the entry so the read
				// consults pkg/param sources again.
				if fv.tainted {
					cur[fk] = fv
				} else {
					delete(cur, fk)
				}
			}
		}
	}
	// The reverse direction: a clean marker set in this branch but
	// absent in other (or on a path that never touched the struct at
	// all) degrades to the fallback the same way, while a tainted entry
	// survives (the other path may have the page too).
	for k, cur := range st.structs {
		om, otherTouched := other.structs[k]
		for fk, fv := range cur {
			if fv.tainted {
				continue
			}
			if !otherTouched {
				delete(cur, fk)
				continue
			}
			if _, ok := om[fk]; !ok {
				delete(cur, fk)
			}
		}
	}
	for k, v := range other.localFuncs {
		if st.ambigBind[k] {
			continue
		}
		if cur, ok := st.localFuncs[k]; ok && cur != v {
			// Divergent literal bindings on alternative paths: the call
			// through this variable has no single provable callee. The
			// ambiguity is sticky: a later branch that re-binds the
			// same variable to its pre-branch literal must not
			// re-establish a provable callee (the page-returning branch
			// is still possible).
			st.ambigBind[k] = true
			delete(st.localFuncs, k)
			continue
		}
		st.localFuncs[k] = v
	}
	for k, v := range other.localBindings {
		if st.ambigBind[k] {
			continue
		}
		if cur, ok := st.localBindings[k]; ok && cur != v {
			// Divergent bindings on alternative paths: the call through
			// this variable has no single provable callee. Two
			// different AST nodes with the same text bind the same
			// callee and are not divergent.
			same := false
			if cur != nil && v != nil {
				ct, vt := exprText(cur), exprText(v)
				same = ct != "..." && ct == vt
			}
			if !same {
				st.ambigBind[k] = true
				delete(st.localBindings, k)
				continue
			}
		}
		st.localBindings[k] = v
	}
}

func (pf *pageFlow) evalExpr(st *stmtState, e ast.Expr) pageValue {
	if e == nil {
		return pageValue{}
	}
	if pv, ok := pf.values[e]; ok && !pv.hasSym && !pv.hasSrc {
		// cached concrete taint; symbolic and param-sourced results are
		// re-derived at every use so call-site context stays live.
		if pv.tainted {
			return pv
		}
	}
	var out pageValue
	switch v := unparen(e).(type) {
	case *ast.Ident:
		if obj := st.pf.pc.info.ObjectOf(v); obj != nil {
			if pv, ok := st.stmtVars[obj]; ok {
				out = pv
			} else if idx, ok := st.params[obj]; ok && paramCanCarryPage(obj.Type()) {
				out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, hasSrc: true}
			} else if obj.Parent() == st.pf.pc.pkg.Scope() {
				// Package-scope variable read: the shared pkgVars map
				// carries initializer taint and cross-function stores
				// (a global assigned a page in one helper is visible to
				// every function of the package).
				if pv, ok := st.pkgVars[v.Name]; ok {
					out = pv
				}
			}
		} else if pv, ok := st.pkgVars[v.Name]; ok {
			out = pv
		}
	case *ast.SelectorExpr:
		if obj := objOfDeref(st, v.X); obj != nil {
			// The local field map, the package field map, and the
			// parameter field source are consulted in order: a field
			// never stored locally (or stored clean only for another
			// field) must still see the package-level taint and the
			// param-derived source, or writing b.Other after the caller
			// stored a page into b.Data would launder the read of
			// b.Data.
			found := false
			if m, ok := st.structs[obj]; ok {
				if pv, ok := m[v.Sel.Name]; ok {
					out = pv
					found = true
				}
			}
			// A locally recorded field (tainted or a clean-store marker)
			// wins over the package state and the param source: only a
			// field that was never stored locally falls back.
			if !found {
				if gm, ok := st.pkgStructs[obj]; ok {
					if pv, ok := gm[v.Sel.Name]; ok {
						out = pv
						found = true
					}
				}
			}
			if !found {
				if idx, ok := st.params[obj]; ok && paramCanCarryPage(paramFieldType(obj, v.Sel.Name)) {
					out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: v.Sel.Name, hasSrc: true}
				}
			}
		} else if call, ok := unparen(v.X).(*ast.CallExpr); ok {
			// box5(page).Data: a field select directly on a struct-valued
			// call result reads the recorded field taints of the call.
			// The call must be re-evaluated with its cached result dropped:
			// an earlier fixpoint pass cached the call's result before
			// the argument taints stabilized, and the cache hit would
			// return that stale value without refreshing callFields
			// (evalCall records them only when it runs).
			delete(pf.values, call)
			pf.evalExpr(st, call)
			if m, ok := pf.callFields[call]; ok {
				if pv, ok := m[v.Sel.Name]; ok {
					out = pv
				}
			}
		} else if lit := structLitOf(v.X); lit != nil {
			// S{Data: page}.Data and &S{Data: page}.Data: a field select
			// on an inline struct literal reads the literal's field
			// taints.
			if m := pf.compositeFields(st, lit); m != nil {
				if pv, ok := m[v.Sel.Name]; ok {
					out = pv
				}
			}
		} else if ta, ok := unparen(v.X).(*ast.TypeAssertExpr); ok {
			// v.(B).Data with v an interface variable holding a struct
			// literal: the assertion keeps the recorded field taints.
			if obj := objOf(st, ta.X); obj != nil {
				if m, ok := st.structs[obj]; ok {
					if pv, ok := m[v.Sel.Name]; ok {
						out = pv
					}
				}
			}
		} else if obj, path := selectorChain(st, v); obj != nil {
			// o.Inner.Data and deeper chains resolve through the
			// flattened path recorded by compositeFields.
			if m, ok := st.structs[obj]; ok {
				if pv, ok := m[path]; ok {
					out = pv
				}
			}
			// A nested read off a struct PARAMETER (o.Inner.Data with o
			// a caller-bound parameter) has no recorded local store: the
			// leaf field keeps the parameter source, so summary results
			// of take(o) stay caller-dependent exactly like direct
			// field reads do.
			if !out.tainted {
				if idx, ok := st.params[obj]; ok {
					if ft := leafPathType(obj.Type(), path); ft != nil && paramCanCarryPage(ft) {
						out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: path, hasSrc: true}
					}
				}
			}
		} else if ix, ok := unparen(v.X).(*ast.IndexExpr); ok {
			// []B{{Data: page}}[0].Data: a field select on an indexed
			// composite literal reads the union of the elements' field
			// taints (the extraction may name any element).
			if lit := structLitOf(ix.X); lit != nil {
				var m map[string]pageValue
				for _, el := range lit.Elts {
					var val ast.Expr
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						val = kv.Value
					} else {
						val = el
					}
					elLit := structLitOf(val)
					if elLit == nil {
						continue
					}
					if fm := pf.compositeFields(st, elLit); fm != nil {
						for k, fv := range fm {
							if prev, ok := m[k]; ok {
								m[k] = joinPageValue(prev, fv)
							} else {
								if m == nil {
									m = map[string]pageValue{}
								}
								m[k] = fv
							}
						}
					}
				}
				if pv, ok := m[v.Sel.Name]; ok {
					out = pv
				}
			} else if obj := objOf(st, ix.X); obj != nil {
				// xs[0].Data with xs a bound container: the container's
				// element-field taints were recorded when the slice
				// literal was assigned (xs := []box8{{Data: page}}).
				if m, ok := st.structs[obj]; ok {
					if pv, ok := m[v.Sel.Name]; ok {
						out = pv
					}
				}
			} else if call, ok := unparen(ix.X).(*ast.CallExpr); ok {
				// makeList(page)[0].Data: a field select on an indexed
				// call result reads the container field taints the
				// summary recorded for the call. The call must be
				// re-evaluated like the un-indexed call case: a call
				// node first reached through this branch may never have
				// run evalCall in this statement state, and a stale
				// cached result from an earlier fixpoint pass also
				// needs callFields refreshed.
				delete(pf.values, call)
				pf.evalExpr(st, call)
				if m, ok := pf.callFields[call]; ok {
					if pv, ok := m[v.Sel.Name]; ok {
						out = pv
					}
				}
			}
		}
	case *ast.SliceExpr:
		base := pf.evalExpr(st, v.X)
		if base.tainted {
			sym := sliceLenSym(v, st)
			maxLen := int64(maxUnknown)
			if c, ok := sym.isConst(); ok {
				maxLen = c
			}
			out = pageValue{tainted: true, maxLen: maxLen, sym: sym, hasSym: true}
		}
	case *ast.CompositeLit:
		out = pf.evalComposite(st, v)
	case *ast.CallExpr:
		out = pf.evalCall(st, v)
	case *ast.FuncLit:
		// A closure in expression position: its body may receive tainted
		// captured variables, so it is analyzed in the enclosing state.
		// The literal's own parameters are bound at call time (direct
		// calls) or remain unbound when the closure is passed on.
		pf.analyzeFuncLitBody(st, v)
	case *ast.UnaryExpr:
		switch v.Op {
		case token.AND, token.MUL:
			out = pf.evalExpr(st, v.X)
		case token.ARROW:
			// A receive takes the channel's element taint: a page sent
			// on the channel earlier (SendStmt) stays tainted on the
			// receiving side.
			out = pf.evalExpr(st, v.X)
		}
	case *ast.StarExpr:
		// Dereference expressions (*p) are StarExpr nodes in go/ast, the
		// same shape as pointer types; in expression position this
		// propagates the value taint of the pointed-to variable.
		out = pf.evalExpr(st, v.X)
	case *ast.IndexExpr:
		// Element extraction: p[0] of a [][]byte yields one page view.
		// The element is tainted only when its own type can carry page
		// bytes (a byte read from []byte stays clean). A param-derived
		// base keeps its source so the summary stays argument-dependent:
		// a generic xs[0] must not report "always tainted".
		if base := pf.evalExpr(st, v.X); base.tainted {
			if t := pf.pc.info.Types[v].Type; t != nil && typeCanCarryPage(t) {
				out = derivedPageValue(base)
			}
		}
	case *ast.IndexListExpr:
		if base := pf.evalExpr(st, v.X); base.tainted {
			if t := pf.pc.info.Types[v].Type; t != nil && typeCanCarryPage(t) {
				out = derivedPageValue(base)
			}
		}
	case *ast.TypeAssertExpr:
		// x.([]byte): the asserted value keeps the mapped-view taint when
		// the asserted type can hold page bytes; asserting to a scalar
		// stays clean.
		if pv := pf.evalExpr(st, v.X); pv.tainted {
			if t := pf.pc.info.Types[v].Type; t != nil && typeCanCarryPage(t) {
				out = derivedPageValue(pv)
			}
		}
	case *ast.ParenExpr:
		out = pf.evalExpr(st, v.X)
	}
	pf.values[e] = out
	return out
}

// derivedPageValue returns the taint of a value derived from a tainted
// base (element extraction, type assertion). The base's definite bound
// is carried (a type assertion returns exactly the asserted value; an
// extraction may return the bound-carrying element), and a
// parameter-sourced base keeps its source so summaries of param-derived
// results stay caller-dependent instead of reporting "always tainted".
func derivedPageValue(base pageValue) pageValue {
	out := pageValue{tainted: true, maxLen: base.maxLen, hasSym: base.hasSym, sym: base.sym}
	if base.hasSrc {
		out.srcParam = base.srcParam
		out.srcField = base.srcField
		out.hasSrc = true
	}
	return out
}

// elemCarriesPage reports whether a slice/array/map composite literal's
// element values can themselves be page views (the value a literal builds
// is the collection of its elements).
func elemCarriesPage(t types.Type) (bool, bool) {
	switch u := types.Unalias(t).(type) {
	case *types.Slice:
		return typeCanCarryPage(u.Elem()), true
	case *types.Array:
		return typeCanCarryPage(u.Elem()), true
	case *types.Map:
		return typeCanCarryPage(u.Elem()), true
	case *types.Named:
		return elemCarriesPage(u.Underlying())
	}
	return false, false
}

// paramIndexOf reports whether x is (possibly parenthesized) the ident of
// parameter i of the current function.
func paramIndexOf(st *stmtState, x ast.Expr) (int, bool) {
	if st == nil {
		return 0, false
	}
	if id, ok := unparen(x).(*ast.Ident); ok {
		idx, ok := st.params[st.pf.pc.info.ObjectOf(id)]
		return idx, ok
	}
	return 0, false
}

// evalComposite returns the taint of a struct/slice/array composite
// literal: a struct value is tainted when any of its evaluated fields is
// tainted (the descriptor or page lives inside the value).
func (pf *pageFlow) evalComposite(st *stmtState, v *ast.CompositeLit) pageValue {
	typ := pf.pc.info.Types[v].Type
	if typ == nil {
		return pageValue{}
	}
	stt, ok := derefStruct(typ)
	if !ok {
		// Slice/array/map literals with page-carrying elements: an
		// element that is itself a page view taints the composite (the
		// literal value is the slice header of exactly those views), so
		// a [][]byte{page} argument keeps the page taint into a
		// parameter summary. Byte-element literals are owned bytes and
		// stay clean.
		carr, ok := elemCarriesPage(typ)
		if !ok {
			return pageValue{}
		}
		_ = carr
		tainted := false
		hasUnknown := false
		maxLen := int64(0)
		var elemSym symbol
		elemSymOK := false
		for _, el := range v.Elts {
			var val ast.Expr
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				val = kv.Value
			} else {
				val = el
			}
			if val == nil {
				continue
			}
			// Map keys are first-class values of the literal too: a
			// page-bearing key (*[]byte holding a mapped view) keeps the
			// page alive inside the collection just like a tainted
			// element does.
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if kpv := pf.evalExpr(st, kv.Key); kpv.tainted {
					tainted = true
					if kpv.maxLen == maxUnknown {
						hasUnknown = true
					} else if kpv.maxLen > maxLen {
						maxLen = kpv.maxLen
					}
					if kpv.hasSym {
						if c, ok := kpv.sym.isConst(); ok && (!elemSymOK || c > elemSym.c) {
							elemSym = kpv.sym
							elemSymOK = true
						}
					}
				}
			}
			if pv := pf.evalExpr(st, val); pv.tainted {
				tainted = true
				if pv.maxLen == maxUnknown {
					hasUnknown = true
				} else if pv.maxLen > maxLen {
					maxLen = pv.maxLen
				}
				if pv.hasSym {
					if c, ok := pv.sym.isConst(); ok && (!elemSymOK || c > elemSym.c) {
						elemSym = pv.sym
						elemSymOK = true
					}
				}
			}
		}
		if !tainted {
			return pageValue{}
		}
		out := pageValue{tainted: true, maxLen: maxLen}
		if hasUnknown {
			out.maxLen = maxUnknown
		}
		if elemSymOK && elemSym.c > out.maxLen {
			out.maxLen = elemSym.c
			out.hasSym = true
			out.sym = elemSym
		}
		return out
	}
	tainted := false
	hasUnknown := false
	maxLen := int64(0)
	for i, el := range v.Elts {
		var val ast.Expr
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			val = kv.Value
			_ = i
		} else if i < stt.NumFields() {
			val = el
		}
		if val == nil {
			continue
		}
		pv := pf.evalExpr(st, val)
		if pv.tainted {
			tainted = true
			if pv.maxLen == maxUnknown {
				hasUnknown = true
			} else if pv.maxLen > maxLen {
				maxLen = pv.maxLen
			}
		}
	}
	if !tainted {
		return pageValue{}
	}
	if hasUnknown {
		maxLen = maxUnknown
	}
	return pageValue{tainted: true, maxLen: maxLen}
}

// compositeFields returns the per-field taint of a struct composite
// literal, used to bind struct-valued arguments at call sites.
func (pf *pageFlow) compositeFields(st *stmtState, v *ast.CompositeLit) map[string]pageValue {
	typ := pf.pc.info.Types[v].Type
	if typ == nil {
		return nil
	}
	stt, ok := derefStruct(typ)
	if !ok {
		// Slice/array/map literals: the per-element struct-field taints
		// are treated as the container's fields, so xs := []box8{{Data:
		// page}} keeps Data on the bound variable and xs[0].Data reads
		// stay tainted (the index itself is not a page-carrying value,
		// box8 is a struct). Map and array elements join the same way.
		var out map[string]pageValue
		for _, el := range v.Elts {
			var val ast.Expr
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				val = kv.Value
			} else {
				val = el
			}
			if l := structLitOf(val); l != nil {
				if fm := pf.compositeFields(st, l); fm != nil {
					for k, fv := range fm {
						if fv.tainted {
							if prev, ok := out[k]; ok {
								out[k] = joinPageValue(prev, fv)
							} else {
								if out == nil {
									out = map[string]pageValue{}
								}
								out[k] = fv
							}
						}
					}
				}
			}
		}
		return out
	}
	var out map[string]pageValue
	for i, el := range v.Elts {
		var field *types.Var
		var val ast.Expr
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			id, _ := kv.Key.(*ast.Ident)
			for j := 0; j < stt.NumFields(); j++ {
				if stt.Field(j).Name() == id.Name {
					field = stt.Field(j)
					val = kv.Value
					break
				}
			}
		} else if i < stt.NumFields() {
			field = stt.Field(i)
			val = el
		}
		if field == nil || val == nil {
			continue
		}
		if pv := pf.evalExpr(st, val); pv.tainted {
			if out == nil {
				out = map[string]pageValue{}
			}
			out[field.Name()] = pv
			// Nested and embedded struct values keep their field taints
			// reachable: o.Inner.Data resolves through the flattened
			// "Inner.Data" path, and an embedded struct's fields are
			// promoted to plain "Data" names.
			if inner := structLitOf(val); inner != nil {
				if fm := pf.compositeFields(st, inner); len(fm) > 0 {
					for k, fv := range fm {
						out[field.Name()+"."+k] = fv
						if field.Anonymous() {
							out[k] = fv
						}
					}
				}
			}
		}
	}
	return out
}

// selectorChain resolves a (possibly nested) selector expression to its
// base object and the dotted path of the field names: o.Inner.Data
// yields (o, "Inner.Data").
func selectorChain(st *stmtState, e ast.Expr) (types.Object, string) {
	parts := []string{}
	for {
		e = unparen(e)
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			break
		}
		parts = append([]string{sel.Sel.Name}, parts...)
		e = sel.X
	}
	if len(parts) == 0 {
		return nil, ""
	}
	obj := objOfDeref(st, unparen(e))
	if obj == nil {
		return nil, ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "." + p
	}
	return obj, out
}

// noteStringConvs records parameter indexes converted to owned strings
// anywhere in a function body: string(p) at a call site with a full
// mapped view becomes an owned complete-page copy, and the conversion's
// own statement is caller-bound so the sink inside the helper cannot
// see that the parameter is full.
func (pf *pageFlow) noteStringConvs(st *stmtState, fs *funcSummary, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !pf.pc.info.Types[call.Fun].IsType() || len(call.Args) != 1 {
			return true
		}
		dst := pf.pc.info.Types[call].Type
		if dst == nil {
			return true
		}
		// Defined string types (type S string) convert a full mapped
		// view into an owned string of the same bytes; the summary must
		// record the conversion like string(p) does.
		b, ok := unwrapToUnderlying(types.Unalias(dst)).(*types.Basic)
		if !ok || b.Kind() != types.String {
			return true
		}
		if pv := pf.evalExpr(st, call.Args[0]); pv.hasSrc {
			if fs.stringParams == nil {
				fs.stringParams = map[int]bool{}
			}
			fs.stringParams[pv.srcParam] = true
		}
		// string(xs[i]) of a parameter-sourced slice (variadic or not)
		// converts whichever element the index names: the whole slot is
		// a converting slot, and the call-site check enumerates every
		// trailing argument of a variadic slot.
		if ix, ok := unparen(call.Args[0]).(*ast.IndexExpr); ok {
			if id, ok := unparen(ix.X).(*ast.Ident); ok {
				if obj := pf.pc.info.ObjectOf(id); obj != nil {
					if idx, ok := st.params[obj]; ok {
						if fs.stringParams == nil {
							fs.stringParams = map[int]bool{}
						}
						fs.stringParams[idx] = true
					}
				}
			}
		}
		return true
	})
	pf.propagateConvertParamSources(st, fs, body, false)
}

// noteFmtSpreads records parameter indexes that the body spreads into a
// fmt call. The rules pass exempts such spreads at the helper definition
// (a param-sourced slice stays call-site-sensitive), but the spread is a
// real complete-page copy once a caller passes a full mapped view into
// that slot: the exemption must not launder through a helper that exists
// only to format its argument.
func (pf *pageFlow) noteFmtSpreads(st *stmtState, fs *funcSummary, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !call.Ellipsis.IsValid() || len(call.Args) == 0 {
			return true
		}
		sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return true
		}
		fn, ok := pf.pc.info.Uses[sel.Sel].(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "fmt" {
			return true
		}
		if pv := pf.evalExpr(st, call.Args[len(call.Args)-1]); pv.hasSrc {
			if fs.fmtSpreadParams == nil {
				fs.fmtSpreadParams = map[int]bool{}
			}
			fs.fmtSpreadParams[pv.srcParam] = true
		}
		return true
	})
	pf.propagateConvertParamSources(st, fs, body, true)
}

// propagateConvertParamSources records the caller's parameter indexes
// that reach a callee parameter the callee converts into an owned string
// (or spreads into fmt): the conversion happens inside the callee's
// body, so the caller's own summary must carry the marker or a whole
// chain of helpers would lose the full-page call at the outermost call
// site (s(b) inside outer must make outer's parameter a converting
// slot).
func (pf *pageFlow) propagateConvertParamSources(st *stmtState, fs *funcSummary, body *ast.BlockStmt, spread bool) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || pf.pc.info.Types[call.Fun].IsType() {
			return true
		}
		fn, ok := calleeObject(pf.pc, call).(*types.Func)
		if !ok || fn.Pkg() == nil {
			return true
		}
		pkgPath := fn.Pkg().Path()
		sums := pf.summaries
		if pkgPath != pf.path {
			sums = pf.store.pkgs[pkgPath]
		}
		if sums == nil {
			return true
		}
		key := fn.Name()
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
			key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
		}
		cfs, ok := sums[key]
		if !ok {
			return true
		}
		var convs map[int]bool
		if spread {
			convs = cfs.fmtSpreadParams
		} else {
			convs = cfs.stringParams
		}
		if len(convs) == 0 {
			return true
		}
		// Method-value calls bind the receiver to callee slot 0 (the
		// layout of the call-site check); a method-expression call
		// carries it as the first explicit argument.
		recvOffset := 0
		if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
			if selRecv, isSel := pf.pc.info.Selections[sel]; isSel && selRecv.Kind() == types.MethodVal {
				recvOffset = 1
			}
		}
		for pi := range convs {
			var a ast.Expr
			if pi == 0 && recvOffset == 1 {
				if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
					a = sel.X
				}
			} else {
				ai := pi - recvOffset
				if ai < 0 || ai >= len(call.Args) {
					continue
				}
				a = call.Args[ai]
			}
			if a == nil {
				continue
			}
			// The argument must be a plain parameter reference (the
			// parameter itself, or one of its field selections): only
			// then is the callee's converting slot provably the
			// caller's parameter. The argument is deliberately NOT
			// flow-evaluated: evaluating derived expressions here would
			// warm the shared expression cache with states that differ
			// from the call-site state and could turn bounded
			// record-slice arguments into false complete-page flags at
			// the call-site check.
			srcIdx := -1
			if id, ok := unparen(a).(*ast.Ident); ok {
				if obj := pf.pc.info.ObjectOf(id); obj != nil {
					if idx, ok := st.params[obj]; ok {
						srcIdx = idx
					}
				}
			}
			if srcIdx < 0 {
				continue
			}
			if spread {
				if fs.fmtSpreadParams == nil {
					fs.fmtSpreadParams = map[int]bool{}
				}
				fs.fmtSpreadParams[srcIdx] = true
			} else {
				if fs.stringParams == nil {
					fs.stringParams = map[int]bool{}
				}
				fs.stringParams[srcIdx] = true
			}
		}
		return true
	})
}

// structLitOf unwraps a struct composite literal from an optional
// address-of operator: &B{Data: page} carries the same field taints as
// B{Data: page}, and the pointer result is dereferenced on use.
func structLitOf(e ast.Expr) *ast.CompositeLit {
	switch v := unparen(e).(type) {
	case *ast.CompositeLit:
		return v
	case *ast.UnaryExpr:
		if v.Op == token.AND {
			if lit, ok := unparen(v.X).(*ast.CompositeLit); ok {
				return lit
			}
		}
	}
	return nil
}

func identOf(e ast.Expr) *ast.Ident {
	switch v := unparen(e).(type) {
	case *ast.Ident:
		return v
	}
	return nil
}

// sliceLenSym computes the symbolic length of a slice expression.
func sliceLenSym(v *ast.SliceExpr, st *stmtState) symbol {
	a, okA := symbolOf(v.Low, st)
	b, okB := symbolOf(v.High, st)
	switch {
	case okA && okB:
		return b.sub(a)
	case v.Low == nil && okB:
		return b
	case v.High == nil && okA:
		// x[a:] — bounded only when the base bound is known; the
		// interpreter pages this as unknown (-1).
		return symConst(maxUnknown)
	}
	return symConst(maxUnknown)
}

// mappingImportPath and formatImportPath are the module packages whose
// APIs own page-minted values.
const mappingImportPath = moduleImportPrefix + "/internal/mapping"
const formatImportPath = moduleImportPrefix + "/internal/format"

// formatFieldCaps pins the byte-field bound of the format package's
// record decoders. The record grammar bounds every decoded byte field
// below a complete page (uint8 name lengths plus validation, blob leaf
// and inline bitmap caps, fixed structure payloads), so a decoded
// record view can never span a complete page: pinning the field keeps
// decode-and-copy flows (FeedEntry.Name reaching LookupFeedInto's copy)
// legal exactly like the DecodeMetadataChunk mint, instead of
// collapsing the record to an unknown bound once it round-trips through
// a local struct box (box5/return s flows). Whole-page views
// (DecodePageHeader/OpenSlottedHeader .Page) stay full-tainted: they
// are deliberately not in the table.
var formatFieldCaps = map[string]struct {
	field string
	max   int64
}{
	"DecodeCatalogNameRecord": {"Name", maxFeedNameLen},
	"DecodeCatalogNameBranch": {"FirstName", maxFeedNameLen},
	"DecodeBlobLeaf":          {"Data", maxBlobLeafDataLen},
	"DecodeMembershipIDLeaf":  {"Inline", maxInlineBitmapLen},
	"DecodeStructureIDRecord": {"Payload", networkEnrichPayloadSize},
}

// evalCall resolves callee summaries and mints.
func (pf *pageFlow) evalCall(st *stmtState, call *ast.CallExpr) pageValue {
	// Direct call of a function literal: bind the literal's parameters to
	// the evaluated argument taints and analyze the body in the enclosing
	// variable state, then carry the literal's return taint as the call
	// result (a closure returning a mapped view or an owned copy of it).
	if lit, ok := unparen(call.Fun).(*ast.FuncLit); ok {
		return pf.analyzeFuncLitCall(st, lit, call)
	}
	obj := calleeObject(pf.pc, call)
	// Calls through a function-typed variable: resolve the variable to
	// the concrete callable it provably binds — a func literal (local or
	// package-level initializer chain, analyzed here with the call-site
	// arguments bound so sinks and result taints are visible) or a plain
	// function reached through the chain (evaluated through its summary).
	// A method VALUE stored in a local (get := r.page; get(1)) resolves
	// to the method with the same receiver expression, so the page-taint
	// of the method's result stays visible.
	var recvExpr ast.Expr
	if v, ok := obj.(*types.Var); ok {
		if lit, fn := pf.calleeTarget(st, v, 0); lit != nil {
			return pf.analyzeFuncLitCall(st, lit, call)
		} else if fn != nil {
			obj = fn
		} else if mfn, mexpr := pf.resolveMethodValue(st, v, 0); mfn != nil {
			obj = mfn
			recvExpr = mexpr
			pf.callMethodValues[call] = methodValueCall{fn: mfn, recv: mexpr}
		}
	}
	// Direct method calls: the receiver is the selector's X; method
	// expressions (R.page(r, pgno)) already carry it as an explicit
	// argument.
	if recvExpr == nil {
		if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
			if selRecv, isSel := pf.pc.info.Selections[sel]; isSel && selRecv.Kind() == types.MethodVal {
				// A method VALUE call: the receiver is the selector's X.
				// Method EXPRESSION calls (R.page(R, 1)) carry the
				// receiver as the first explicit argument and must not
				// bind the type expression into the receiver slot.
				recvExpr = sel.X
			}
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		// Builtins (copy/append/len/delete) and type conversions never
		// return taint from their own computation, but their arguments
		// can carry it: the rule pass reads the complete-page sinks
		// through the shared expression cache, so every argument must be
		// evaluated here. append is the one exception: its RESULT owns
		// the appended source bytes, so the statement state must carry
		// that taint (out = append(out, page...)) or a later append of
		// out through a branch or loop join would be invisible.
		for _, a := range call.Args {
			pf.promoteFullPageFields(st, a)
		}
		// Type conversions (any(page), []byte(x), string(x), [N]byte(x))
		// keep the bytes' taint: boxing into an interface or converting
		// between byte shapes does not launder a mapped view.
		if len(call.Args) == 1 && pf.pc.info.Types[call.Fun].IsType() {
			return pf.evalExpr(st, call.Args[0])
		}
		if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "append" && len(call.Args) >= 2 {
			// append's result owns the appended source bytes: the
			// statement state must carry the taint so a later append of
			// the result (through a branch or loop join) stays visible.
			out := pageValue{}
			for _, a := range call.Args[1:] {
				pv := pf.evalExpr(st, a)
				if !pv.tainted {
					continue
				}
				if !out.tainted {
					out = pageValue{tainted: true, maxLen: pv.maxLen}
				} else if pv.maxLen == maxUnknown || pv.maxLen > out.maxLen {
					out.maxLen = pv.maxLen
				}
			}
			return out
		}
		// An unproven callee whose result could be a mapped page fails
		// closed: a function-typed variable without a provable body, a
		// struct function field, or an index/call/type-assert produced
		// callee can return a page (captured or minted) with no page
		// argument at this call site. Builtins and type conversions
		// returned above; direct function calls resolve to their
		// summaries and reach the module path below.
		if indirectCallee(call.Fun, pf.pc) {
			pf.callFieldsFailClosed[call] = true
			if stt, ok := derefStruct(pf.pc.info.Types[call].Type); ok {
				fields := map[string]pageValue{}
				for i := 0; i < stt.NumFields(); i++ {
					f := stt.Field(i)
					if typeCanCarryPage(f.Type()) {
						fields[f.Name()] = pageValue{tainted: true, maxLen: maxUnknown}
					}
				}
				if len(fields) > 0 {
					pf.callFields[call] = fields
				}
			}
			if resultHoldsPage(pf.pc.info.Types[call].Type) {
				return pageValue{tainted: true, maxLen: maxUnknown}
			}
		}
		return pageValue{}
	}
	// Arguments are evaluated for every resolved call too (mints, stdlib
	// consumers, and module summaries), so tainted expressions inside
	// arguments are always visible to the rule pass. Field-only page
	// taints are promoted onto the argument node as well, so the
	// string-parameter and opaque-call checks see a struct variable
	// whose page was stored into a field.
	for _, a := range call.Args {
		pf.promoteFullPageFields(st, a)
	}
	pkgPath := fn.Pkg().Path()
	recv := ""
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv = recvTypeNameFromTypes(sig.Recv().Type())
	}
	// Mints: the mmap call returns the mapped bytes; the mapping owner's
	// Page/View hand out views of those bytes; the metadata chunk codec
	// exposes a page slice whose length is pinned below a complete page
	// by format's own reject rule (format/metadata.go).
	if pkgPath == "golang.org/x/sys/unix" && fn.Name() == "Mmap" {
		return pageValue{tainted: true, maxLen: maxUnknown}
	}
	if pkgPath == mappingImportPath && recv == "Mapping" {
		switch fn.Name() {
		case "Page":
			return pageValue{tainted: true, maxLen: pageSize}
		case "View":
			// View(off, length): the length argument bounds the view; a
			// constant bound keeps the result below a complete page.
			pv := pageValue{tainted: true, maxLen: maxUnknown}
			if len(call.Args) == 2 {
				if s, ok := symbolOf(call.Args[1], st); ok {
					if c, ok := s.isConst(); ok {
						pv.maxLen = c
					}
				}
			}
			return pv
		}
	}
	if pkgPath == formatImportPath && fn.Name() == "DecodeMetadataChunk" {
		pf.callFields[call] = map[string]pageValue{"Data": {tainted: true, maxLen: maxMetadataChunkLen}}
		return pageValue{tainted: true, maxLen: maxMetadataChunkLen}
	}
	if cap, ok := formatFieldCaps[fn.Name()]; ok && pkgPath == formatImportPath {
		pf.callFields[call] = map[string]pageValue{cap.field: {tainted: true, maxLen: cap.max}}
		return pageValue{}
	}
	if !strings.HasPrefix(pkgPath, moduleImportPrefix) {
		return pageValue{}
	}
	sums := pf.summaries
	if pkgPath != pf.path {
		sums = pf.store.pkgs[pkgPath]
	}
	if sums == nil {
		return pageValue{}
	}
	key := fn.Name()
	if recv != "" {
		key = recv + "." + fn.Name()
	}
	fs, ok := sums[key]
	if !ok {
		// A module-declared interface method dispatches to an
		// implementation this scan cannot follow; a byte-holding result
		// may be a mapped page regardless of the arguments. Fail closed
		// on the result (and on byte-bearing fields of a struct result)
		// so a downstream copy stays visible. Concrete methods without a
		// body are rejected as assembly stubs by the declaration rule.
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
			// The receiver is the transfer argument of an unprovable
			// interface-method call: unlike the explicit-argument loop,
			// it is never evaluated by the miss path, so a
			// page-carrying receiver (a mapped view stored whole, or a
			// struct field the interface variable picked up from a
			// struct store) would be invisible to the rule pass's
			// receiver transfer check. Record the receiver's joined
			// taint (whole value plus struct fields) on its expression
			// so the call site fails closed as a complete-page
			// transfer when the receiver carries a full mapped view.
			if recvExpr != nil {
				pf.promoteFullPageFields(st, recvExpr)
			}
			if resultHoldsPage(pf.pc.info.Types[call].Type) {
				return pageValue{tainted: true, maxLen: maxUnknown}
			}
			// A struct result whose byte-bearing fields the
			// implementation fills from a mapped view is invisible to
			// this call site; every page-carrying field fails closed so
			// the caller's field reads and copies stay visible.
			pf.callFieldsFailClosed[call] = true
			if stt, ok := derefStruct(pf.pc.info.Types[call].Type); ok {
				fields := map[string]pageValue{}
				for i := 0; i < stt.NumFields(); i++ {
					f := stt.Field(i)
					if typeCanCarryPage(f.Type()) {
						fields[f.Name()] = pageValue{tainted: true, maxLen: maxUnknown}
					}
				}
				if len(fields) > 0 {
					pf.callFields[call] = fields
				}
			}
		}
		return pageValue{}
	}
	// The receiver binds argument slot 0 of a method value call; the
	// explicit arguments follow (the same layout the summary analysis
	// uses, where the receiver is parameter slot 0).
	argOff := 0
	if recvExpr != nil {
		argOff = 1
	}
	args := make([]pageValue, len(call.Args)+argOff)
	argVals := make([]symbol, len(call.Args)+argOff)
	argFlows := make([]argFlow, len(call.Args)+argOff)
	if recvExpr != nil {
		pf.promoteFullPageFields(st, recvExpr)
		args[0] = pf.evalExpr(st, recvExpr)
		argVals[0], _ = symbolOf(recvExpr, st)
		argFlows[0] = pf.argFlowOf(st, recvExpr)
	}
	slot := argOff
	vi := -1
	if fs != nil {
		vi = fs.variadic
	}
	for _, a := range call.Args {
		pf.promoteFullPageFields(st, a)
		pv := pf.evalExpr(st, a)
		sv, _ := symbolOf(a, st)
		af := pf.argFlowOf(st, a)
		if vi >= 0 && slot >= vi {
			// Variadic parameter: every trailing argument can name the
			// slot, so the summary binding joins them (xs[1] inside the
			// callee reads any of the caller's remaining arguments).
			args[vi] = joinPageValue(args[vi], pv)
			if c, ok := argVals[vi].isConst(); ok {
				if c2, ok2 := sv.isConst(); ok2 {
					if c2 > c {
						argVals[vi] = sv
					}
				} else {
					argVals[vi] = symbol{}
				}
			}
			for k, fv := range af.fields {
				if prev, ok := argFlows[vi].fields[k]; ok {
					argFlows[vi].fields[k] = joinPageValue(prev, fv)
				} else {
					if argFlows[vi].fields == nil {
						argFlows[vi].fields = map[string]pageValue{}
					}
					argFlows[vi].fields[k] = fv
				}
			}
			continue
		}
		args[slot] = pv
		argVals[slot] = sv
		argFlows[slot] = af
		slot++
	}
	rv, fields := fs.eval(args, argVals, argFlows)
	pf.callResults[call] = fs.evalResults(args, argVals, argFlows)
	// Struct fields are recorded even when the whole result slot is
	// tainted: (chunk, err) helpers hand the page out through chunk.Data,
	// and the statement state reads that field when the append sink runs.
	if len(fields) > 0 {
		pf.callFields[call] = fields
	}
	if rv.tainted {
		return rv
	}
	if len(fields) > 0 {
		return pageValue{tainted: true, maxLen: maxUnknown}
	}
	return pageValue{}
}

// evalLitResults resolves the per-result values of an analyzed func
// literal using the bound argument values: a result that is literally the
// literal's parameter keeps the caller's bound (page[48:112] stays 64),
// while derivations without a concrete length stay conservatively unknown.
func (pf *pageFlow) evalLitResults(fs *funcSummary, bound []pageValue, _ *ast.FuncType) []pageValue {
	res := make([]pageValue, len(fs.results))
	for i, r := range fs.results {
		if !r.tainted {
			continue
		}
		pv := pageValue{tainted: true}
		for _, src := range r.srcs {
			m := int64(maxUnknown)
			switch src.kind {
			case "const":
				m = src.constVal
			case "param":
				if src.param >= 0 && src.param < len(bound) {
					b := bound[src.param]
					m = b.maxLen
					if b.hasSym {
						if c, ok := b.sym.isConst(); ok {
							m = c
						}
					}
				}
			case "paramMax":
				if src.param >= 0 && src.param < len(bound) {
					b := bound[src.param]
					m = b.maxLen
					if b.hasSym {
						if c, ok := b.sym.isConst(); ok {
							m = c
						}
					}
				}
			}
			if m == maxUnknown || m > pv.maxLen {
				pv.maxLen = m
			}
		}
		res[i] = pv
	}
	return res
}

// litResultFields resolves the per-field taints of an analyzed func
// literal against the caller's bound argument values, mirroring
// evalLitResults for struct results: a field that is literally the
// literal's parameter keeps the caller's bound, and constant sources
// keep their bounds. The rule pass reads .Data off a closure call
// (f := func(p []byte) B { return B{Data: p} }; f(page).Data) through
// callFields; without this the field read has no recorded taint.
func (pf *pageFlow) litResultFields(fs *funcSummary, bound []pageValue) map[string]pageValue {
	var fields map[string]pageValue
	for name, ft := range fs.fields {
		if !ft.tainted {
			continue
		}
		pv := pageValue{tainted: true}
		for _, src := range ft.srcs {
			m := int64(maxUnknown)
			switch src.kind {
			case "const":
				m = src.constVal
			case "param", "paramMax":
				if src.param >= 0 && src.param < len(bound) {
					b := bound[src.param]
					m = b.maxLen
					if b.hasSym {
						if c, ok := b.sym.isConst(); ok {
							m = c
						}
					}
				}
			}
			if m == maxUnknown || m > pv.maxLen {
				pv.maxLen = m
			}
		}
		if fields == nil {
			fields = map[string]pageValue{}
		}
		fields[name] = pv
	}
	return fields
}

// calleeTarget resolves the concrete callable a function-typed variable
// provably binds: a local variable currently bound to a func literal, or
// a package-level initializer chain ending in a func literal or a plain
// function. Reassigned variables have no provable binding. Locals bound
// to non-literals (parameters, stdlib functions) stay unresolved.
func (pf *pageFlow) calleeTarget(st *stmtState, v *types.Var, depth int) (*ast.FuncLit, *types.Func) {
	if v == nil || depth > 2 || pf.pc.reassignedVars[v] || (st != nil && st.ambigBind[v]) {
		return nil, nil
	}
	if lit, ok := st.localFuncs[v]; ok {
		return lit, nil
	}
	init, ok := pf.pc.varInits[v]
	if !ok {
		return nil, nil
	}
	switch i := unparen(init).(type) {
	case *ast.FuncLit:
		return i, nil
	case *ast.Ident:
		switch o := pf.pc.info.Uses[i].(type) {
		case *types.Var:
			return pf.calleeTarget(st, o, depth+1)
		case *types.Func:
			return nil, o
		}
	case *ast.SelectorExpr:
		if fn, ok := pf.pc.info.Uses[i.Sel].(*types.Func); ok {
			// A method VALUE binding (var get = holder.Get) keeps its
			// receiver: resolving it to the bare function would bind the
			// call's explicit arguments to the receiver slot and drop
			// the holder's field state. Method EXPRESSIONS (var get =
			// box.Get) carry the receiver as the first explicit
			// argument and resolve to the bare function.
			if selRecv, isSel := pf.pc.info.Selections[i]; isSel && selRecv.Kind() == types.MethodVal {
				return nil, nil
			}
			return nil, fn
		}
	}
	return nil, nil
}

// analyzeFuncLitBody analyzes a closure body in the current statement
// state. The closure shares the enclosing variable scope, so captured
// variables keep their taint; parameters are left unbound (they are
// assigned at direct-call sites).
func (pf *pageFlow) analyzeFuncLitBody(st *stmtState, lit *ast.FuncLit) {
	pf.clearExprCaches()
	fs := &funcSummary{fields: map[string]fieldTaint{}, stringParams: map[int]bool{}}
	pf.analyzeStmts(st, lit.Body.List, fs)
	pf.noteStringConvs(st, fs, lit.Body)
	pf.noteFmtSpreads(st, fs, lit.Body)
}

// analyzeFuncLitCall binds a closure's parameters to the call-site
// argument taints, analyzes the body, and returns the closure's result
// taints as the call result. Every result slot is recorded so a
// multi-result closure assignment distributes taint per slot.
func (pf *pageFlow) analyzeFuncLitCall(st *stmtState, lit *ast.FuncLit, call *ast.CallExpr) pageValue {
	pf.clearExprCaches()
	args := call.Args
	fs := &funcSummary{fields: map[string]fieldTaint{}, stringParams: map[int]bool{}}
	if lit.Type.Results != nil {
		// One result slot per named result variable: (a, b []byte)
		// declares two results in one field list, and the named-result
		// post-pass below indexes by name.
		for _, r := range lit.Type.Results.List {
			if len(r.Names) == 0 {
				fs.results = append(fs.results, fieldTaint{})
				continue
			}
			for range r.Names {
				fs.results = append(fs.results, fieldTaint{})
			}
		}
	}
	var bound []pageValue
	if lit.Type.Params != nil {
		idx := 0
		vi := -1
		for _, f := range lit.Type.Params.List {
			if _, ok := f.Type.(*ast.Ellipsis); ok {
				vi = idx
			}
			idx += len(f.Names)
		}
		idx = 0
		arg := 0
		for _, f := range lit.Type.Params.List {
			for _, name := range f.Names {
				obj := pf.pc.info.ObjectOf(name)
				if idx == vi {
					// Variadic parameter: every trailing argument joins
					// the closure's binding, so xs[1] inside the body
					// can name any of them.
					var jo pageValue
					for arg < len(args) {
						pf.promoteFullPageFields(st, args[arg])
						jo = joinPageValue(jo, pf.evalExpr(st, args[arg]))
						arg++
					}
					if jo.tainted {
						st.stmtVars[obj] = jo
					} else {
						delete(st.stmtVars, obj)
					}
					bound = append(bound, jo)
					idx++
					continue
				}
				if arg < len(args) {
					pf.promoteFullPageFields(st, args[arg])
					pv := pf.evalExpr(st, args[arg])
					bound = append(bound, pv)
					if pv.tainted {
						st.stmtVars[obj] = pv
					} else {
						delete(st.stmtVars, obj)
					}
					// The closure parameter carries the argument's
					// struct-field knowledge: f := func(x B) []byte {
					// return x.Data }; f(b) with b.Data = page must
					// resolve x.Data through the literal body.
					pf.materializeStructFields(st, name, args[arg])
				}
				arg++
				idx++
			}
		}
	}
	pf.analyzeStmts(st, lit.Body.List, fs)
	pf.noteStringConvs(st, fs, lit.Body)
	pf.noteFmtSpreads(st, fs, lit.Body)
	// Named results with a naked return: stores to the named result
	// variables are the closure's results (analyzeFunc does the same
	// for FuncDecl bodies; closures need it here, or out = p; return
	// loses the returned view).
	if lit.Type.Results != nil {
		slot := 0
		for _, r := range lit.Type.Results.List {
			for _, name := range r.Names {
				obj := pf.pc.info.ObjectOf(name)
				if slot >= len(fs.results) {
					break
				}
				if pv, ok := st.stmtVars[obj]; ok && pv.tainted {
					fs.results[slot] = joinFieldTaint(fs.results[slot], fieldTaint{tainted: true, srcs: maxSrcOf(pv)})
				}
				if m, ok := st.structs[obj]; ok {
					for k, fv := range m {
						if fv.tainted {
							fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
						}
					}
				}
				slot++
			}
		}
	}
	if lit.Type.Params != nil {
		for _, f := range lit.Type.Params.List {
			for _, name := range f.Names {
				obj := pf.pc.info.ObjectOf(name)
				delete(st.stmtVars, obj)
				delete(st.structs, obj)
			}
		}
	}
	res := pf.evalLitResults(fs, bound, lit.Type)
	if len(res) > 0 {
		pf.callResults[call] = res
	}
	// Struct-result field taints also bind to the call: a field read on
	// the call result (f(page).Data) resolves through callFields.
	if fields := pf.litResultFields(fs, bound); len(fields) > 0 {
		pf.callFields[call] = fields
	}
	if len(res) == 0 || !res[0].tainted {
		return pageValue{}
	}
	return res[0]
}

// argFlowOf binds the value taint and struct-field taints of one call
// argument: local struct variables, composite literals, and prior call
// results (whose fields were recorded when the call was evaluated).
func (pf *pageFlow) argFlowOf(st *stmtState, a ast.Expr) argFlow {
	pv := pf.evalExpr(st, a)
	var fields map[string]pageValue
	if lit := structLitOf(a); lit != nil {
		pv = pf.evalExpr(st, lit)
		fields = pf.compositeFields(st, lit)
		return argFlow{pv: pv, fields: fields}
	}
	switch v := unparen(a).(type) {
	case *ast.Ident:
		obj := st.pf.pc.info.ObjectOf(v)
		fields = st.structs[obj]
		if len(fields) == 0 {
			// Package-scope struct variables keep their field taints in
			// the shared map: a method called on a package-global
			// holder (var get = holder.Get after set(page)) must see the
			// field state another function stored.
			if gm, ok := st.pkgStructs[obj]; ok {
				fields = gm
			}
		}
		if len(fields) == 0 {
			// A struct parameter (or a parameter carrying one): its
			// field taints arrive from the caller through the summary's
			// paramField sources, so the argument flow materializes the
			// declared byte-carrying fields the same way the receiver
			// and field reads use them. Without this, a method
			// expression or method summary called on a struct parameter
			// receiver never sees the field taint the caller stored.
			if idx, ok := st.params[obj]; ok {
				for path, ft := range paramLeafPaths(obj.Type()) {
					if !paramCanCarryPage(ft) {
						continue
					}
					if fields == nil {
						fields = map[string]pageValue{}
					}
					fields[path] = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: path, hasSrc: true}
				}
			}
		}
	case *ast.CompositeLit:
		fields = pf.compositeFields(st, v)
	case *ast.CallExpr:
		fields = pf.callFields[v]
	}
	return argFlow{pv: pv, fields: fields}
}

// promoteFullPageFields records whole-value page taint on an expression
// when any of its RECORDED STRUCT FIELDS holds a complete mapped page:
// an opaque call receiving such a value (a local var whose field was
// assigned a page, a struct parameter sourced by the caller) can copy
// the full page inside an unscanned body even though the value itself
// is not a byte slice. The rule-pass checks (opaque-call arg transfer,
// string-parameter call checks, unprovable-receiver checks) read
// whole-value taints through the shared expression cache, so the
// promotion stays call-site local to the expression node. Bounded field
// views are intentionally not promoted: their copies are policed by the
// bounded-record rules at the extraction sites.
func (pf *pageFlow) promoteFullPageFields(st *stmtState, e ast.Expr) {
	if e == nil {
		return
	}
	pv := pf.evalExpr(st, e)
	if pv.tainted && pageFull(pv) {
		return
	}
	af := pf.argFlowOf(st, e)
	if call, ok := unparen(e).(*ast.CallExpr); ok && pf.callFieldsFailClosed[call] {
		// Fail-closed field over-approximations must not graduate into
		// whole-value page taint (see callFieldsFailClosed): a clean
		// stdlib struct returned by an opaque call is not a mapped page.
		return
	}
	for _, fv := range af.fields {
		if !fv.tainted || !pageFull(fv) {
			continue
		}
		npv := pageValue{tainted: true, maxLen: fv.maxLen}
		if npv.maxLen == 0 {
			npv.maxLen = maxUnknown
		}
		pf.values[e] = npv
		return
	}
}

func recvTypeNameFromTypes(t types.Type) string {
	for {
		switch v := t.(type) {
		case *types.Pointer:
			t = v.Elem()
		case *types.Named:
			return v.Obj().Name()
		default:
			return "?"
		}
	}
}

func calleeObject(pc *packageCheck, call *ast.CallExpr) types.Object {
	switch f := unparen(call.Fun).(type) {
	case *ast.Ident:
		return pc.info.Uses[f]
	case *ast.SelectorExpr:
		return pc.info.Uses[f.Sel]
	}
	return nil
}

// resultHoldsPage reports whether a call result type could itself be a
// mapped page view: byte slices/arrays (and named aliases), strings,
// empty-method-set interfaces (a page view has no methods, so only any
// and empty interfaces can box it), tuples of such results, and
// containers of them. Structs that merely WRAP byte state (a
// *bytes.Reader holder) are deliberately excluded: their byte content is
// only reachable through methods policed by the selector/exemption
// rules, and an unproven factory for them is not itself a page mint.
func resultHoldsPage(t types.Type) bool {
	return resultHoldsPageSeen(t, map[types.Type]bool{})
}

func resultHoldsPageSeen(t types.Type, seen map[types.Type]bool) bool {
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		return u.Kind() == types.String
	case *types.Interface:
		return types.NewMethodSet(t).Len() == 0
	case *types.Slice:
		return isByteElem(u.Elem()) || resultHoldsPageSeen(u.Elem(), seen)
	case *types.Array:
		return isByteElem(u.Elem()) || resultHoldsPageSeen(u.Elem(), seen)
	case *types.Map:
		return resultHoldsPageSeen(u.Key(), seen) || resultHoldsPageSeen(u.Elem(), seen)
	case *types.Chan:
		return resultHoldsPageSeen(u.Elem(), seen)
	case *types.Pointer:
		return resultHoldsPageSeen(u.Elem(), seen)
	case *types.TypeParam:
		return true
	case *types.Tuple:
		for i := 0; i < u.Len(); i++ {
			if resultHoldsPageSeen(u.At(i).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Named:
		if seen[u] {
			return false
		}
		seen[u] = true
		return resultHoldsPageSeen(u.Underlying(), seen)
	case *types.Alias:
		if seen[u] {
			return false
		}
		seen[u] = true
		return resultHoldsPageSeen(types.Unalias(u), seen)
	}
	return false
}

// indirectCallee reports whether a call's function position is a callee
// the scan cannot resolve to a scanned body: an unproven function-typed
// variable, a struct function field, an interface method (only reachable
// pre-summary through the module-miss path), or index/call/type-assert/
// dereference produced callees. Direct functions, builtins, and type
// conversions are not indirect.
func indirectCallee(fun ast.Expr, pc *packageCheck) bool {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		switch pc.info.Uses[f].(type) {
		case *types.Var:
			return true
		case *types.Builtin:
			return false
		}
		return false
	case *ast.SelectorExpr:
		switch obj := pc.info.Uses[f.Sel].(type) {
		case *types.Var:
			return true // struct function field
		case *types.Func:
			// Concrete methods resolve through their summaries; only
			// interface methods have no body to prove.
			if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && isInterfaceType(sig.Recv().Type()) {
				return true
			}
			return false
		}
		return true
	case *ast.IndexExpr, *ast.IndexListExpr, *ast.CallExpr, *ast.TypeAssertExpr, *ast.StarExpr:
		return true
	case *ast.ParenExpr:
		return indirectCallee(unparen(f), pc)
	}
	return false
}

// propagateStructResult records tainted fields of a returned struct
// value into the summary: direct composite literals (S{Data: p}) and
// returned local struct variables (s := S{Data: p}; return s) whose
// field taints were recorded by the assignment.
func (pf *pageFlow) propagateStructResult(st *stmtState, expr ast.Expr, fs *funcSummary) {
	// A returned local struct variable carries its recorded field taints;
	// returning its address (&s) or dereferencing a pointer to it (*s)
	// delivers the same fields.
	var id *ast.Ident
	switch e := unparen(expr).(type) {
	case *ast.Ident:
		id = e
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			id = identOf(e.X)
		}
	case *ast.StarExpr:
		id = identOf(e.X)
	}
	if id != nil {
		if obj := pf.pc.info.ObjectOf(id); obj != nil {
			if m, ok := st.structs[obj]; ok {
				for k, fv := range m {
					if fv.tainted {
						fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
					}
				}
			}
		}
		return
	}
	lit := structLitOf(expr)
	if lit == nil {
		return
	}
	stt, ok := derefStruct(pf.pc.info.Types[lit].Type)
	if !ok {
		// A container of struct values ([]B{{Data: p}}) keeps the
		// element field taints as the result's fields: a call site
		// reading makeList(page)[0].Data sees the caller's page bound.
		if fm := pf.compositeFields(st, lit); len(fm) > 0 {
			for k, fv := range fm {
				if fv.tainted {
					fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
				}
			}
		}
		return
	}
	for i, el := range lit.Elts {
		var field *types.Var
		var val ast.Expr
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			id, _ := kv.Key.(*ast.Ident)
			for j := 0; j < stt.NumFields(); j++ {
				if stt.Field(j).Name() == id.Name {
					field = stt.Field(j)
					val = kv.Value
					break
				}
			}
		} else if i < stt.NumFields() {
			field = stt.Field(i)
			val = el
		}
		if field == nil || val == nil {
			continue
		}
		pv := pf.evalExpr(st, val)
		if pv.tainted {
			// Same-named fields of different result slots unions their
			// sources: split5(a, b) (S, S) returning S{Data: a}, S{Data: b}
			// keeps both parameter sources, so a call site with a page in
			// slot 1 still sees Data tainted (keeping only the first field
			// dropped the second slot).
			fs.fields[field.Name()] = joinFieldTaint(fs.fields[field.Name()], fieldTaint{tainted: true, srcs: maxSrcOf(pv)})
		}
	}
}

func derefStruct(t types.Type) (*types.Struct, bool) {
	for {
		switch v := t.(type) {
		case *types.Pointer:
			t = v.Elem()
		case *types.Named:
			t = v.Underlying()
		default:
			s, ok := t.(*types.Struct)
			return s, ok
		}
	}
}

// typeCanCarryPage reports whether a value of this static type can alias
// a mapped page view: byte slices and byte arrays (including named
// aliases of them), pointers to them, type parameters that may
// instantiate to them, and interfaces whose dynamic value can be one.
// Recursive containers (slices of page-carrying elements) count too: the
// element extraction rule (p[0]) propagates the taint. Structs are not
// covered as values; their fields are checked separately.
func typeCanCarryPage(t types.Type) bool {
	return typeCanCarryPageSeen(t, map[types.Type]bool{})
}

// typeCanCarryPageSeen is the recursive core of typeCanCarryPage. The
// seen set breaks container cycles of recursive defined types (type R []R):
// every recursive edge is a container constructor, so a type that revisits
// itself without ever reaching a byte/interface/pointer leaf cannot carry
// page bytes; reporting false there is the least fixed point and prevents
// the scanner from recursing without bound.
func typeCanCarryPageSeen(t types.Type, seen map[types.Type]bool) bool {
	switch u := t.(type) {
	case *types.Slice:
		return isByteElem(u.Elem()) || typeCanCarryPageSeen(u.Elem(), seen)
	case *types.Array:
		return isByteElem(u.Elem()) || typeCanCarryPageSeen(u.Elem(), seen)
	case *types.Map:
		return typeCanCarryPageSeen(u.Key(), seen) || typeCanCarryPageSeen(u.Elem(), seen)
	case *types.Chan:
		return typeCanCarryPageSeen(u.Elem(), seen)
	case *types.Pointer:
		return typeCanCarryPageSeen(u.Elem(), seen)
	case *types.TypeParam:
		return true // a ~[]byte instantiation is possible
	case *types.Interface:
		// An interface value can hold a mapped view (boxing a page into
		// any/error/io.Reader does not launder it); element extraction
		// and type assertions keep the taint.
		return true
	case *types.Named:
		if seen[u] {
			return false
		}
		seen[u] = true
		return typeCanCarryPageSeen(u.Underlying(), seen)
	case *types.Alias:
		if seen[u] {
			return false
		}
		seen[u] = true
		return typeCanCarryPageSeen(types.Unalias(u), seen)
	}
	return false
}

func isByteElem(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Kind() == types.Uint8
}

// paramCanCarryPage reports whether a function parameter can receive a
// mapped page view: concrete byte-carrying types (typeCanCarryPage) plus
// directly interface-typed parameters (func idAny(v any) any { return v }
// keeps the caller's taint through the summary). Variadic ([]T) and
// []interface{} parameters are deliberately excluded: production
// formatting helpers (corrupt, headerErr) spread such slices into
// fmt.Sprintf, and minting them would flag the spread at every helper
// definition regardless of callers. The taint stays call-site-sensitive:
// a clean argument never taints.
func paramCanCarryPage(t types.Type) bool {
	return paramCanCarryPageSeen(t, map[types.Type]bool{})
}

func paramCanCarryPageSeen(t types.Type, seen map[types.Type]bool) bool {
	// A map or channel parameter can receive a page view inside its
	// container (map[string][]byte m: m["x"], chan []byte ch: <-ch):
	// the summary must keep the taint argument-dependent like a direct
	// byte slice. Non-byte containers stay clean.
	switch u := types.Unalias(t).(type) {
	case *types.Map:
		// Nested containers recurse: a map of maps of byte slices, or a
		// channel of maps, can deliver a page view at any depth.
		return paramCanCarryPageSeen(u.Key(), seen) || paramCanCarryPageSeen(u.Elem(), seen)
	case *types.Chan:
		return paramCanCarryPageSeen(u.Elem(), seen)
	case *types.Slice:
		// Byte-element slices stay carriers (a []byte parameter receives
		// the full mapped view); nested containers recurse so a page
		// view delivered at any depth keeps its taint through element
		// extraction. The fmt helper spread itself is policed at the
		// call site (fmtCallee exemption), not by dropping the carrier.
		return isByteElem(u.Elem()) || paramCanCarryPageSeen(u.Elem(), seen)
	case *types.Array:
		return isByteElem(u.Elem()) || paramCanCarryPageSeen(u.Elem(), seen)
	}
	if typeCanCarryPageSeen(t, seen) {
		return true
	}
	return isInterfaceType(t)
}

// leafPathType resolves a dotted field path ("Inner.Data") from a struct
// type down to its leaf type, or nil when the path does not name a field
// path. Pointer and named struct fields are dereferenced like the
// selector read does, so o.Inner.Data on a value or pointer receiver
// resolves the same way.
func leafPathType(t types.Type, path string) types.Type {
	for _, part := range strings.Split(path, ".") {
		st, ok := derefStruct(t)
		if !ok {
			return nil
		}
		var f *types.Var
		for i := 0; i < st.NumFields(); i++ {
			if st.Field(i).Name() == part {
				f = st.Field(i)
				break
			}
		}
		if f == nil {
			return nil
		}
		t = f.Type()
	}
	return t
}

// paramLeafPaths returns every leaf field path of a struct type that can
// carry page bytes, flattened with dotted names ("Data", "Inner.Data"),
// so parameter fallback sources cover nested struct fields exactly like
// compositeFields flattens literals.
func paramLeafPaths(t types.Type) map[string]types.Type {
	out := map[string]types.Type{}
	walkSeen := map[types.Type]bool{}
	var walk func(t types.Type, prefix string)
	walk = func(t types.Type, prefix string) {
		st, ok := derefStruct(t)
		if !ok {
			return
		}
		if walkSeen[st] {
			return // recursion through a self-referencing pointer field
		}
		walkSeen[st] = true
		for i := 0; i < st.NumFields(); i++ {
			f := st.Field(i)
			p := f.Name()
			if prefix != "" {
				p = prefix + "." + f.Name()
			}
			if _, isSt := derefStruct(f.Type()); isSt {
				walk(f.Type(), p)
			} else if paramCanCarryPage(f.Type()) {
				out[p] = f.Type()
			}
		}
		walkSeen[st] = false
	}
	walk(t, "")
	return out
}

// paramFieldType returns the static type of a named field of the struct
// type of parameter object obj, or nil when obj is not a struct parameter.
func paramFieldType(obj types.Object, name string) types.Type {
	stt, ok := derefStruct(obj.Type())
	if !ok {
		return nil
	}
	for i := 0; i < stt.NumFields(); i++ {
		f := stt.Field(i)
		if f.Name() == name {
			return f.Type()
		}
	}
	return nil
}

// symbolOf computes the symbolic integer value of an expression: integer
// constants, integer parameters, their sums and differences, and len() of
// a definitely bounded tainted slice.
func symbolOf(e ast.Expr, st *stmtState) (symbol, bool) {
	if e == nil {
		return symbol{}, false
	}
	switch v := unparen(e).(type) {
	case *ast.BasicLit:
		if v.Kind == token.INT {
			if n, err := strconv.ParseInt(strings.ReplaceAll(v.Value, "_", ""), 0, 64); err == nil {
				return symConst(n), true
			}
		}
	case *ast.Ident:
		if obj := st.pf.pc.info.ObjectOf(v); obj != nil {
			if idx, ok := st.params[obj]; ok {
				return symParam(idx), true
			}
			if c, ok := obj.(*types.Const); ok {
				if i, ok := constantInt64(c); ok {
					return symConst(i), true
				}
			}
		}
	case *ast.BinaryExpr:
		a, ok1 := symbolOf(v.X, st)
		b, ok2 := symbolOf(v.Y, st)
		if !ok1 || !ok2 {
			return symbol{}, false
		}
		switch v.Op {
		case token.ADD:
			return a.add(b), true
		case token.SUB:
			return a.sub(b), true
		}
	case *ast.CallExpr:
		if id, ok := unparen(v.Fun).(*ast.Ident); ok && id.Name == "len" && len(v.Args) == 1 {
			if pv := st.pf.evalExpr(st, v.Args[0]); pv.tainted && pv.maxLen >= 0 {
				return symConst(pv.maxLen), true
			}
		}
	}
	return symbol{}, false
}

// constantInt64 extracts an int64 from a types constant.
func constantInt64(c *types.Const) (int64, bool) {
	if c.Val() == nil {
		return 0, false
	}
	v := c.Val().ExactString()
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
