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
	// mutFields records pointer-parameter FIELD MUTATIONS inside the
	// body: parameter index -> field path -> taint sources (func
	// (b *box) Set(p []byte) { b.Data = p } exports slot 0 "Data" from
	// p). Call sites re-bind the sources to the actual argument values
	// and write the resulting field taints onto the receiver/argument
	// object, so b.Set(page); return b.Data keeps the caller's page
	// source. Value parameters (copies) never export mutations.
	mutFields map[int]map[string]fieldTaint
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
		case "paramField":
			if s.param >= 0 && s.param < len(argFlows) {
				if fv, ok := argFlows[s.param].fields[s.field]; ok {
					return fv.maxLen
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
		case "paramField":
			if s.param >= 0 && s.param < len(argFlows) {
				if fv, ok := argFlows[s.param].fields[s.field]; ok {
					return fv.maxLen
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
	fs.mutFields = map[int]map[string]fieldTaint{}
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
	// Pointer-parameter field mutations are part of the callee's
	// summary: field stores on pointer params (b.Data = p inside a
	// method body) tell call sites which fields of the receiver or
	// pointer argument become page-carrying. Only tainted final stores
	// export (a body that ends with a clean store leaves no record);
	// value parameters are copies and never export.
	for obj, idx := range st.params {
		if !isStructPtrTyped(obj.Type()) {
			continue
		}
		m, ok := st.structs[obj]
		if !ok || len(m) == 0 {
			continue
		}
		for k, fv := range m {
			if !fv.tainted {
				continue
			}
			fm := fs.mutFields[idx]
			if fm == nil {
				fm = map[string]fieldTaint{}
				fs.mutFields[idx] = fm
			}
			fm[k] = joinFieldTaint(fm[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
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
				// A struct-valued right side keeps its field provenance
				// on the bound variable exactly like direct argument
				// flow: composite literals flatten their fields, calls
				// carry the callee's recorded fields, and indexed,
				// dereferenced, or selected values (xs[0], *p, b.Inner,
				// makeList(p)[0], *makePtr(p), makeBox(p).Inner) bind
				// the element/pointee/selected field names.
				var fields map[string]pageValue
				if lit := structLitOf(rhs); lit != nil {
					fields = pf.compositeFields(st, lit)
				} else {
					fields = pf.argFlowOf(st, rhs).fields
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
					} else if path, root := selectorIndexChain(ix.X); root != nil {
						// h.Items[0] = B{Data: page}: the container is
						// reached through a FIELD (with optional base
						// indexes): the element fields record under the
						// field prefix on the base object, the same
						// flattened path the read h.Items[0].Data
						// resolves.
						if obj := objOfDeref(st, unparen(root)); obj != nil {
							if st.structs[obj] == nil {
								st.structs[obj] = map[string]pageValue{}
							}
							for k, fv := range fields {
								key := path + "." + k
								if prev, ok := st.structs[obj][key]; ok {
									st.structs[obj][key] = joinPageValue(prev, fv)
								} else {
									st.structs[obj][key] = fv
								}
							}
						}
					}
					// A dereferenced container root ((*q)[0] = B{Data:
					// page} with q bound to &xs): the pointer names the
					// container directly or through an alias binding, so
					// the element fields record on the pointer's object
					// AND on the alias target, and reads under either
					// name ((*q)[0].Data or xs[0].Data) stay sourced.
					if star, ok := unparen(ix.X).(*ast.StarExpr); ok {
						var targets []types.Object
						if pobj := objOfDeref(st, star.X); pobj != nil {
							targets = append(targets, pobj)
						}
						if al := pf.derefTarget(st, star.X, 0); al != nil {
							dup := false
							for _, t := range targets {
								if t == al {
									dup = true
									break
								}
							}
							if !dup {
								targets = append(targets, al)
							}
						}
						for _, pobj := range targets {
							if st.structs[pobj] == nil {
								st.structs[pobj] = map[string]pageValue{}
							}
							for k, fv := range fields {
								if prev, ok := st.structs[pobj][k]; ok {
									st.structs[pobj][k] = joinPageValue(prev, fv)
								} else {
									st.structs[pobj][k] = fv
								}
							}
						}
					}
				}
				// A dereference store of a struct value keeps the
				// field taints on the pointed-to variable: *p = B{Data:
				// page} followed by p.Data (or (*p).Data) resolves the
				// same record the whole-value store marks, so the
				// selected read keeps the caller's source.
				if star, ok := unparen(v.Lhs[i]).(*ast.StarExpr); ok && len(fields) > 0 {
					// The store records on the pointer expression's
					// object AND on the alias target the pointer binds
					// (q := &b; *q = B{Data: page} must keep b.Data
					// sourced, not only (*q).Data).
					var targets []types.Object
					if pobj := objOfDeref(st, star.X); pobj != nil {
						targets = append(targets, pobj)
					}
					if al := pf.derefTarget(st, star.X, 0); al != nil {
						dup := false
						for _, t := range targets {
							if t == al {
								dup = true
								break
							}
						}
						if !dup {
							targets = append(targets, al)
						}
					}
					for _, pobj := range targets {
						if st.structs[pobj] == nil {
							st.structs[pobj] = map[string]pageValue{}
						}
						for k, fv := range fields {
							st.structs[pobj][k] = fv
						}
						if pobj.Parent() == st.pf.pc.pkg.Scope() && st.pkgStructs != nil {
							gm := st.pkgStructs[pobj]
							if gm == nil {
								gm = map[string]pageValue{}
								st.pkgStructs[pobj] = gm
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
				// A struct-valued map key (or slice index) keeps its
				// element field taints on the container: m[S{Data:
				// page}] = 1 and m[b] = 1 with b.Data assigned a page
				// both must stay tainted through for k := range m {
				// k.(S).Data }, because the range visits every key. The
				// argument flow covers literals, variables, selectors,
				// and parameter-held sources.
				if ix, ok := unparen(v.Lhs[i]).(*ast.IndexExpr); ok {
					if cobj := objOf(st, ix.X); cobj != nil {
						if kf := pf.argFlowOf(st, ix.Index).fields; len(kf) > 0 {
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
					} else if obj, path := selectorChain(st, ix.X); obj != nil {
						// h.M[&b] = 1 with h.M map[*B]int: the key's
						// field taints record under the field prefix on
						// the base object, the same flattened path the
						// key-only range read for k := range h.M
						// resolves ("M.Data" on h, bound to k's Data).
						if kf := pf.argFlowOf(st, ix.Index).fields; len(kf) > 0 {
							if st.structs[obj] == nil {
								st.structs[obj] = map[string]pageValue{}
							}
							for k, fv := range kf {
								key := path + "." + k
								if prev, ok := st.structs[obj][key]; ok {
									st.structs[obj][key] = joinPageValue(prev, fv)
								} else {
									st.structs[obj][key] = fv
								}
							}
						}
					} else if pobj := objOfDeref(st, ix.X); pobj != nil {
						// A dereferenced map container ((*q)[&b] = 1):
						// the key's fields record on the pointer's
						// object, resolved by the dereferenced read.
						if kf := pf.argFlowOf(st, ix.Index).fields; len(kf) > 0 {
							if st.structs[pobj] == nil {
								st.structs[pobj] = map[string]pageValue{}
							}
							for k, fv := range kf {
								if prev, ok := st.structs[pobj][k]; ok {
									st.structs[pobj][k] = joinPageValue(prev, fv)
								} else {
									st.structs[pobj][k] = fv
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
								} else {
									// Same field provenance as the
									// assignment form: indexed, selected,
									// dereferenced and call-produced
									// init values keep their element or
									// selected field taints.
									fields = pf.argFlowOf(st, vs.Values[i]).fields
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
			// A naked return of one multi-valued call (return f(p) where
			// the function and f return several values) forwards every
			// result: distributing only the first slot would drop the
			// taint of a later page-carrying result (the common
			// (error, []byte) shape has the page in slot 1).
			if len(v.Results) == 1 {
				if call, ok := unparen(v.Results[0]).(*ast.CallExpr); ok {
					// Re-evaluate in the current statement state: a
					// cached result from an earlier fixpoint pass is
					// stale once callee summaries stabilized.
					delete(pf.values, call)
					pf.evalExpr(st, call)
					handled := false
					if res, ok := pf.callResults[call]; ok {
						for i := range fs.results {
							if i < len(res) && res[i].tainted {
								fs.results[i] = joinFieldTaint(fs.results[i], fieldTaint{tainted: true, srcs: maxSrcOf(res[i])})
							}
						}
						handled = true
					}
					if fields, ok := pf.callFields[call]; ok && len(fields) > 0 {
						for k, fv := range fields {
							if fv.tainted {
								fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
							}
						}
						handled = true
					}
					delete(pf.callResults, call)
					delete(pf.callFields, call)
					// A naked return of one call expression is only
					// fully handled when the call resolved to concrete
					// per-result values; a mint or other early-return
					// specialization (r.m.Page(pgno)) records no
					// callResults, so fall through to the per-result
					// loop, whose evaluation still carries the taint of
					// the whole call into the first result slot.
					if handled {
						break
					}
				}
			}
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
				// {Data: page}} binds x.Data), like the indexed read path. The
				// element fields resolve from four roots: a recorded local
				// container, a struct-element container PARAMETER (declared
				// element leaves with the parameter source), an inline
				// container literal (per-element union), and a call-produced
				// container (re-evaluated callee fields).
				valObj := objOf(st, v.Value)
				keyObj := objOf(st, v.Key)
				var elemFields map[string]pageValue
				if obj := objOf(st, v.X); obj != nil {
					if m, ok := st.structs[obj]; ok && len(m) > 0 {
						elemFields = m
					} else if idx, ok := st.params[obj]; ok {
						// A container PARAMETER of struct elements exposes the
						// declared element leaves: the loop value binds the element
						// fields with the parameter source, exactly like the indexed
						// read path.
						elemFields = pf.paramFieldFallback(st, obj, idx)
					}
				} else if cl, ok := unparen(v.X).(*ast.CompositeLit); ok {
					elemFields = pf.compositeFields(st, cl)
				} else if call, ok := unparen(v.X).(*ast.CallExpr); ok {
					if len(call.Args) == 1 && pf.pc.info.Types[call.Fun].IsType() {
						// A type conversion preserves the container's
						// element fields: for _, x := range []B(h.Items)
						// binds the same element leaves the converted
						// operand carries.
						elemFields = pf.argFlowOf(st, call.Args[0]).fields
					} else {
						// The call must be re-evaluated: a cached result
						// from an earlier fixpoint pass may predate
						// callee stabilization.
						delete(pf.values, call)
						pf.evalExpr(st, call)
						elemFields = pf.callFields[call]
					}
				} else {
					// A container reached through a FIELD, an INDEX chain,
					// an interface assertion, or a pointer dereference
					// (h.Items, m[0].Items, v.(holder).Items, p.Items):
					// the element fields resolve exactly like argument
					// flow renames them, so the loop value binds the
					// same recorded leaves and declared parameter paths.
					switch unparen(v.X).(type) {
					case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.TypeAssertExpr, *ast.StarExpr:
						if af := pf.argFlowOf(st, v.X).fields; len(af) > 0 {
							elemFields = af
						}
					}
				}
				if len(elemFields) > 0 {
					// A key-only range of a map (for k := range m) pulls the
					// container's key-field taints out through the key
					// variable. A TWO-variable range of a MAP binds the key
					// fields to the KEY variable and the map value's own
					// fields to the VALUE variable (for k, v := range m with
					// m map[*box]int keeps k.Data, not v); slice and array
					// ranges bind the element fields to the value variable.
					bindRange := func(tgt types.Object, fields map[string]pageValue) {
						if tgt == nil || len(fields) == 0 {
							return
						}
						if st.structs[tgt] == nil {
							st.structs[tgt] = map[string]pageValue{}
						}
						for k, fv := range fields {
							st.structs[tgt][k] = fv
						}
					}
					if mt := mapUnderlying(pf.pc.info.Types[v.X].Type); mt != nil && valObj != nil && keyObj != nil {
						ke := leafNameSet(mt.Key())
						ve := leafNameSet(mt.Elem())
						var kf, vf map[string]pageValue
						for k, fv := range elemFields {
							if ke[k] {
								if kf == nil {
									kf = map[string]pageValue{}
								}
								kf[k] = fv
							}
							if ve[k] {
								if vf == nil {
									vf = map[string]pageValue{}
								}
								vf[k] = fv
							}
						}
						bindRange(keyObj, kf)
						bindRange(valObj, vf)
					} else {
						tgt := valObj
						if tgt == nil {
							tgt = keyObj
						}
						bindRange(tgt, elemFields)
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
			// A sent STRUCT keeps its field provenance on the channel
			// (ch <- B{Data: page}; x := <-ch; x.Data stays sourced),
			// through every name the channel expression addresses:
			// the channel variable, a struct-field channel (h.Ch), or
			// an alias variable bound to either. Tainted sends join.
			if af := pf.argFlowOf(st, v.Value); len(af.fields) > 0 {
				pf.recordChanSendFields(st, v.Chan, af.fields)
			}
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
			// The asserted expression's WHOLE-VALUE taint (v := x.(type)
			// with x an unprovable call result or a page-carrying
			// interface value) projects the page-carrying leaves of
			// every matched case type onto the implicit per-case
			// variable: go/types types the case variable with the case
			// type (info.Implicits), and its field reads inside the
			// clause resolve the recorded leaves exactly like a
			// struct-typed local.
			baseTainted := false
			if as, ok := v.Assign.(*ast.AssignStmt); ok && len(as.Rhs) == 1 && len(as.Lhs) == 1 {
				if ta, ok := unparen(as.Rhs[0]).(*ast.TypeAssertExpr); ok {
					baseTainted = pf.evalExpr(st, ta.X).tainted
				}
			}
			pf.typeSwitchJoin(st, v.Body.List, fs, baseTainted)
		case *ast.DeferStmt:
			pf.evalExpr(st, v.Call)
		case *ast.GoStmt:
			pf.evalExpr(st, v.Call)
		case *ast.SelectStmt:
			pre := st.clone()
			// Model every tainted SEND clause on the shared pre-select
			// state before branch analysis: a page sent by one case may
			// be taken by another case's receive, and every branch
			// analyzes from its own pre-select clone, so a send recorded
			// only inside its own branch would be invisible to receives
			// in the others. Clean sends leave no record.
			for _, c := range v.Body.List {
				cc, ok := c.(*ast.CommClause)
				if !ok {
					continue
				}
				if s, ok := cc.Comm.(*ast.SendStmt); ok {
					if af := pf.argFlowOf(st, s.Value); len(af.fields) > 0 {
						pf.recordChanSendFields(pre, s.Chan, af.fields)
						pf.recordChanSendFields(st, s.Chan, af.fields)
					}
				}
			}
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
					if af := pf.argFlowOf(branch, comm.Value); len(af.fields) > 0 {
						pf.recordChanSendFields(branch, comm.Chan, af.fields)
					}
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
	hasDefault := false
	emptyDefault := false
	var fallState *stmtState
	for _, c := range body {
		cc, ok := c.(*ast.CaseClause)
		if !ok {
			continue
		}
		// The default clause (no expression list) makes the pre-switch
		// state unreachable after the statement; without it the switch
		// can skip every case and fall out with the state unchanged.
		if cc.List == nil {
			hasDefault = true
			if len(cc.Body) == 0 {
				emptyDefault = true
			}
		}
		if len(cc.Body) == 0 {
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
	if !hasDefault || emptyDefault {
		// A switch with no default can match no case: the pre-switch
		// state (an unproven callable, a page held in a variable) stays
		// reachable after the statement, so it must join the same way
		// the zero-iteration path joins a loop. An EMPTY default (default:)
		// also keeps the pre-switch state reachable: its body is a no-op,
		// so the fall-out state is the pre-switch state joined with every
		// non-empty case body.
		st.joinWith(pre)
	}
}

// typeSwitchJoin mirrors switchJoin for a TYPE switch ("v := x.(type)")
// and additionally projects the asserted base's whole-value taint onto
// the page-carrying leaves of each matched case type: when the base is
// an unprovable call result the concrete value is unknowable, so the
// implicit per-case variable (info.Implicits) must fail closed on every
// leaf the case type could carry, the same projection the direct
// type-assert read applies. Fallthrough and the no-default join behave
// exactly like the value switch.
func (pf *pageFlow) typeSwitchJoin(st *stmtState, body []ast.Stmt, fs *funcSummary, baseTainted bool) {
	pre := st.clone()
	first := true
	hasDefault := false
	emptyDefault := false
	var fallState *stmtState
	for _, c := range body {
		cc, ok := c.(*ast.CaseClause)
		if !ok {
			continue
		}
		if cc.List == nil {
			hasDefault = true
			if len(cc.Body) == 0 {
				emptyDefault = true
			}
		}
		if len(cc.Body) == 0 {
			continue
		}
		branch := st
		if !first {
			branch = pre.clone()
		}
		if fallState != nil {
			branch.joinWith(fallState)
		}
		if baseTainted {
			if cv, ok := pf.pc.info.Implicits[cc]; ok {
				for path, ft := range paramLeafPaths(cv.Type()) {
					if !paramCanCarryPage(ft) {
						continue
					}
					if branch.structs[cv] == nil {
						branch.structs[cv] = map[string]pageValue{}
					}
					branch.structs[cv][path] = pageValue{tainted: true, maxLen: maxUnknown}
				}
			}
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
	if !hasDefault || emptyDefault {
		st.joinWith(pre)
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
	if dstObj == nil {
		return
	}
	var fields map[string]pageValue
	if id == nil {
		// Indexed, selected, dereferenced-call and call-produced values
		// (b.Inner, xs[0], *f(p), f(p).Inner) bind the same fields
		// direct argument flow resolves; closure parameters fed such
		// arguments carry the selected element fields into the body.
		fields = pf.argFlowOf(st, src).fields
	} else {
		srcObj := pf.pc.info.ObjectOf(id)
		if srcObj == nil {
			return
		}
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
			for path, fv := range pf.paramFieldFallback(st, srcObj, idx) {
				if _, recorded := fields[path]; recorded {
					continue
				}
				if fields == nil {
					fields = map[string]pageValue{}
				}
				fields[path] = fv
			}
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

// recordChanSendFields records the sent value's struct-field taints on
// every name the channel expression addresses: a plain channel variable,
// a selector field channel (h.Ch <- B{Data: p} records "Ch.Data" on h),
// and an alias variable bound to either (q := h.Ch sends through both
// names, so a receive on the other name resolves the record). Tainted
// sends join (a later clean send must not erase the taint); clean-only
// sends leave no record.
func (pf *pageFlow) recordChanSendFields(st *stmtState, ch ast.Expr, fields map[string]pageValue) {
	record := func(obj types.Object, prefix string) {
		if obj == nil {
			return
		}
		join := func(m map[string]pageValue) map[string]pageValue {
			if m == nil {
				m = map[string]pageValue{}
			}
			for k, fv := range fields {
				if !fv.tainted {
					continue
				}
				p := prefix + k
				if prev, ok := m[p]; ok {
					m[p] = joinPageValue(prev, fv)
				} else {
					m[p] = fv
				}
			}
			return m
		}
		st.structs[obj] = join(st.structs[obj])
		if obj.Parent() == st.pf.pc.pkg.Scope() && st.pkgStructs != nil {
			st.pkgStructs[obj] = join(st.pkgStructs[obj])
		}
	}
	if o, path := selectorChain(st, ch); o != nil {
		record(o, path+".")
		return
	}
	if o := objOfDeref(st, ch); o != nil {
		record(o, "")
	}
	// An INDEXED channel (cs[0] <- B{Data: page} with cs []chan B)
	// names an element of a container the same way an indexed struct
	// store does: the channel's element records live on the root
	// container object, and the matching receive resolves the same
	// root, so the send must record there with no prefix.
	if root := indexChainRoot(ch); root != ch {
		if o := objOf(st, root); o != nil {
			record(o, "")
			return
		}
	}
	if id, ok := unparen(ch).(*ast.Ident); ok {
		if obj := pf.pc.info.ObjectOf(id); obj != nil {
			if bind, ok := st.localBindings[obj]; ok {
				if o, path := selectorChain(st, bind); o != nil {
					record(o, path+".")
				} else if bo := objOfDeref(st, bind); bo != nil {
					record(bo, "")
				} else if broot := indexChainRoot(bind); broot != bind {
					if bo := objOf(st, broot); bo != nil {
						record(bo, "")
					}
				}
			}
		}
	}
}

// chanRecvFields resolves the field taints of a received struct from
// the channel expression: a plain channel variable keeps its recorded
// fields, a selector channel (x := <-h.Ch) reads the base object's
// "Ch."-prefixed records, and an alias variable bound to either (q :=
// h.Ch) falls back to the bound channel's records. The receive binds
// the same element field names the send recorded.
func (pf *pageFlow) chanRecvFields(st *stmtState, ch ast.Expr) map[string]pageValue {
	var out map[string]pageValue
	add := func(obj types.Object, prefix string, strip bool) {
		if obj == nil {
			return
		}
		for k, fv := range pf.fieldTaintsOf(st, obj) {
			if !fv.tainted {
				continue
			}
			if strip {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				k = k[len(prefix):]
			}
			if out == nil {
				out = map[string]pageValue{}
			}
			out[k] = fv
		}
	}
	if o, path := selectorChain(st, ch); o != nil {
		add(o, path+".", true)
	}
	if o := objOfDeref(st, ch); o != nil {
		add(o, "", false)
	}
	// An INDEXED channel (cs[0]) resolves the same root container the
	// send recorded on: the received element fields are the root's
	// element records, exactly like the indexed-store read path.
	if root := indexChainRoot(ch); root != ch {
		if o := objOf(st, root); o != nil {
			add(o, "", false)
		}
	}
	if id, ok := unparen(ch).(*ast.Ident); ok {
		if obj := pf.pc.info.ObjectOf(id); obj != nil {
			if bind, ok := st.localBindings[obj]; ok {
				if o, path := selectorChain(st, bind); o != nil {
					add(o, path+".", true)
				} else if bo := objOfDeref(st, bind); bo != nil {
					add(bo, "", false)
				} else if broot := indexChainRoot(bind); broot != bind {
					if bo := objOf(st, broot); bo != nil {
						add(bo, "", false)
					}
				}
			}
		}
	}
	return out
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
		// b.Data = p, (*b).Data = p, q.Data = p (q := &b), and
		// o.Inner.Data = p: the selector chain resolves the base object
		// and the flattened field path (nested stores record
		// "Inner.Data"), so the matching read path (selectorChain in
		// evalExpr) sees the taint. A pointer-typed base that binds an
		// address (q := &b; q.Data = page) also records on the alias
		// target, so the ORIGINAL variable's field read sees the store.
		if obj, path := selectorChain(st, v); obj != nil {
			recordFieldStore := func(target types.Object) {
				m := st.structs[target]
				if m == nil {
					m = map[string]pageValue{}
					st.structs[target] = m
				}
				if target.Parent() != st.pf.pc.pkg.Scope() {
					// Local variables: a clean store to one field records
					// a clean marker instead of deleting the entry, so a
					// later read of a DIFFERENT field still falls back to
					// its parameter source (sink6 writes b.Other after
					// the caller stored a page into b.Data) while a read
					// of the clean-stored field itself stays clean.
					m[path] = pv
				} else if pv.tainted {
					m[path] = pv
				} else {
					// Package-level variables: the shared pkgStructs map
					// is the cross-function authority, so a clean local
					// store removes the local entry and the read falls
					// through to the package state (which only records
					// tainted field stores and joins them).
					delete(m, path)
				}
				if target.Parent() == st.pf.pc.pkg.Scope() && st.pkgStructs != nil {
					if !pv.tainted {
						return
					}
					gm := st.pkgStructs[target]
					if gm == nil {
						gm = map[string]pageValue{}
						st.pkgStructs[target] = gm
					}
					if prev, ok := gm[path]; ok && prev.tainted {
						gm[path] = joinPageValue(prev, pv)
					} else {
						gm[path] = pv
					}
				}
			}
			recordFieldStore(obj)
			// Alias-following: q.Data with q bound to &b stores through
			// the same storage b names.
			base := unparen(v)
			for sel, ok := base.(*ast.SelectorExpr); ok; sel, ok = base.(*ast.SelectorExpr) {
				base = unparen(sel.X)
			}
			if al := st.pf.derefTarget(st, base, 0); al != nil && al != obj {
				recordFieldStore(al)
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
	// A callable binding must survive a join only when BOTH branches
	// bound the same provable value. A binding present on exactly one
	// path is not provable at runtime: the other path may hold an
	// unproven callable (a parameter, an opaque value), so the variable
	// becomes ambiguous and calls through it fail closed.
	funcs := map[types.Object]bool{}
	for k := range st.localFuncs {
		funcs[k] = true
	}
	for k := range other.localFuncs {
		funcs[k] = true
	}
	for k := range funcs {
		if st.ambigBind[k] {
			continue
		}
		cur, okCur := st.localFuncs[k]
		ov, okOth := other.localFuncs[k]
		if okCur && okOth {
			if cur != ov {
				// Divergent literal bindings on alternative paths: the
				// call through this variable has no single provable
				// callee. The ambiguity is sticky: a later branch that
				// re-binds the same variable to its pre-branch literal
				// must not re-establish a provable callee (the
				// page-returning branch is still possible).
				st.ambigBind[k] = true
				delete(st.localFuncs, k)
			}
			continue
		}
		// A callable binding present on exactly one path is not provable
		// at runtime for a LOCAL variable: the other path may hold an
		// unproven callable (a parameter, an opaque value), so the
		// variable becomes ambiguous and calls through it fail closed.
		// Package-scope callables are exempt: their binding is proven by
		// the package initializer (calleeTarget falls back to varInits
		// and resolveMethodValue to the recorded seed), reassignment is
		// policed by reassignedVars, and the merge keeps the surviving
		// side's seed instead of erasing package truth on a branch-local
		// invalidation.
		if obj, isVar := k.(*types.Var); isVar && obj.Parent() == st.pf.pc.pkg.Scope() {
			if okOth && !okCur {
				st.localFuncs[k] = ov
			}
			continue
		}
		st.ambigBind[k] = true
		delete(st.localFuncs, k)
	}
	bindings := map[types.Object]bool{}
	for k := range st.localBindings {
		bindings[k] = true
	}
	for k := range other.localBindings {
		bindings[k] = true
	}
	for k := range bindings {
		if st.ambigBind[k] {
			continue
		}
		cur, okCur := st.localBindings[k]
		ov, okOth := other.localBindings[k]
		if okCur && okOth {
			if cur == ov {
				// The same expression node provably bound both paths:
				// the binding survives unchanged (package initializer
				// seeds are the same node on every branch).
				continue
			}
			// Divergent bindings on alternative paths: the call through
			// this variable has no single provable callee. Two
			// different AST nodes with the same text bind the same
			// callee and are not divergent.
			same := false
			if cur != nil && ov != nil {
				ct, vt := exprText(cur), exprText(ov)
				same = ct != "..." && ct == vt
			}
			if !same {
				st.ambigBind[k] = true
				delete(st.localBindings, k)
			}
			continue
		}
		// One-sided bindings are unproven for LOCAL variables: one path
		// may hold an unproven callable (a parameter, an opaque value).
		// Package seeds are package truth and survive the merge (see
		// the localFuncs join above).
		if obj, isVar := k.(*types.Var); isVar && obj.Parent() == st.pf.pc.pkg.Scope() {
			if okOth && !okCur {
				st.localBindings[k] = ov
			}
			continue
		}
		st.ambigBind[k] = true
		delete(st.localBindings, k)
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
				if idx, ok := st.params[obj]; ok && paramCanCarryPage(paramFieldType(st.pf.pc.pkg, obj, v.Sel.Name)) {
					out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: v.Sel.Name, hasSrc: true}
				}
			}
		} else if call, chain := callRootChain(v); call != nil {
			// A field select on a call-produced struct value
			// (box5(page).Data, box5(page).A.Data, box6(page)[0].Data,
			// (*box7(page)).Data): the callee's flattened field paths
			// carry the full dotted chain, including the outermost
			// selection. The call must be re-evaluated with its cached
			// result dropped: an earlier fixpoint pass cached the call's
			// result before the argument taints stabilized, and the
			// cache hit would return that stale value without
			// refreshing callFields (evalCall records them only when
			// it runs).
			delete(pf.values, call)
			pf.evalExpr(st, call)
			if m, ok := pf.callFields[call]; ok {
				if pv, ok := m[chain]; ok {
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
		} else if ta := typeAssertBaseOf(v); ta != nil {
			// v.(B).Data and v.(T).Inner.Data with v an interface variable
			// or parameter holding a struct: the assertion keeps the
			// recorded field taints on the full dotted path. An
			// INTERFACE-TYPED parameter asserted to a struct has no
			// recorded local store and no declared leaves of its own: the
			// read keeps the parameter source through the asserted type's
			// leaf, the same way a struct parameter's leaf reads do, and
			// an asserted CONTAINER (v.([]B)[0].Data) reads the element
			// leaves because the extraction names an element.
			fullPath, _ := selectorIndexChain(v)
			if obj := objOf(st, ta.X); obj != nil {
				if m, ok := st.structs[obj]; ok {
					if pv, ok := m[fullPath]; ok {
						out = pv
					}
				}
				if !out.tainted {
					if idx, ok := st.params[obj]; ok && isInterfaceType(obj.Type()) {
						if ft := assertLeafType(st.pf.pc.pkg, pf.pc.info.Types[ta.Type].Type, fullPath); ft != nil && paramCanCarryPage(ft) {
							out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: fullPath, hasSrc: true}
						}
					}
				}
			}
			// The asserted base may be an INTERFACE VALUE whose
			// whole-value taint came from an unprovable callee result (a
			// struct func field, a func var, or an interface method
			// returning any) — including a DIRECT call base
			// (h.get().(B).Data) that has no binding object of its own:
			// the concrete value is unknowable, so every page-carrying
			// leaf of the asserted type fails closed, exactly like the
			// interface-result rule projects the callee's fields. A
			// clean base contributes nothing, and a locally recorded or
			// parameter-sourced taint above already wins.
			if !out.tainted {
				if basePv := pf.evalExpr(st, ta.X); basePv.tainted {
					if t := pf.pc.info.Types[ta.Type].Type; t != nil {
						if ft := assertLeafType(st.pf.pc.pkg, t, fullPath); ft != nil && paramCanCarryPage(ft) {
							out = pageValue{tainted: true, maxLen: maxUnknown}
						}
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
					if ft := leafPathType(st.pf.pc.pkg, obj.Type(), path); ft != nil && paramCanCarryPage(ft) {
						out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: path, hasSrc: true}
					}
				}
			}
		} else if chainContainsIndex(v.X) {
			// xs[0].Data, m[0][0].Data and m[0][0].Inner.Data: a field
			// select on an indexed value reads the container's
			// element-field taints no matter how many trailing
			// selectors and index levels the expression has, because
			// every level names an element or field path of the same
			// root container. The root may be a composite literal (the
			// union of the elements' field taints, since the extraction
			// may name any element), a bound local or parameter
			// container, or a call result.
			fullPath, root := selectorIndexChain(v)
			if lit := structLitOf(root); lit != nil {
				var m map[string]pageValue
				for _, el := range lit.Elts {
					fm := pf.elementFieldsOf(st, el)
					if fm == nil {
						continue
					}
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
				if pv, ok := m[fullPath]; ok {
					out = pv
				}
			} else if obj := objOf(st, root); obj != nil {
				// xs[0].Data with xs a bound container: the container's
				// element-field taints were recorded when the slice
				// literal was assigned (xs := []box8{{Data: page}}).
				if m, ok := st.structs[obj]; ok {
					if pv, ok := m[fullPath]; ok {
						out = pv
					}
				}
				// xs[0].Data with xs a container PARAMETER: the
				// declared element leaves carry the caller's fields
				// through the parameter source, the same fallback the
				// binding sites use.
				if !out.tainted {
					if idx, ok := st.params[obj]; ok {
						if pv, ok := pf.paramFieldFallback(st, obj, idx)[fullPath]; ok {
							out = pv
						}
					}
				}
			} else if obj := objOfDeref(st, root); obj != nil {
				// (*q)[0].Data and (*p)[0].Data with a dereferenced
				// container root: the pointer names the container
				// variable directly, so the element fields recorded on
				// it (or the declared element leaves of a pointer
				// container parameter) resolve the same flattened
				// path.
				if m, ok := st.structs[obj]; ok {
					if pv, ok := m[fullPath]; ok {
						out = pv
					}
				}
				if !out.tainted {
					if idx, ok := st.params[obj]; ok {
						if pv, ok := pf.paramFieldFallback(st, obj, idx)[fullPath]; ok {
							out = pv
						}
					}
				}
			} else if call, ok := unparen(root).(*ast.CallExpr); ok {
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
					if pv, ok := m[fullPath]; ok {
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

// elementFieldsOf resolves the struct-field taints one container
// ELEMENT contributes: a composite literal contributes its flattened
// fields, and a variable or parameter element contributes its recorded
// fields and declared leaves, so xs := []B{b} keeps b's fields on the
// container exactly like xs := []B{{Data: page}} does.
func (pf *pageFlow) elementFieldsOf(st *stmtState, el ast.Expr) map[string]pageValue {
	var val ast.Expr
	if kv, ok := el.(*ast.KeyValueExpr); ok {
		val = kv.Value
	} else {
		val = el
	}
	if lit := structLitOf(val); lit != nil {
		return pf.compositeFields(st, lit)
	}
	if cl, ok := unparen(val).(*ast.CompositeLit); ok {
		// A NESTED container literal element ([1]box{{Data: page}} inside
		// [1][1]box): recurse so the innermost struct fields surface on
		// every enclosing container level.
		return pf.compositeFields(st, cl)
	}
	if af := pf.argFlowOf(st, val); len(af.fields) > 0 {
		// A call-produced element (makeBox(p)), the address of a
		// recorded variable (&b), a dereference (*pb), a conversion,
		// or any other element expression the argument-flow rules can
		// rename: the element field names are the direct names,
		// exactly like the literal and variable branches above.
		return af.fields
	}
	return nil
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
		isMap := mapUnderlying(typ) != nil
		unionInto := func(fm map[string]pageValue) {
			if fm == nil {
				return
			}
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
		for _, el := range v.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				// Map KEYS can carry the page too (map[*box]int{&box{
				// Data: page}: 1} then for k := range m: k.Data): a key held
				// as a composite literal or variable contributes the same
				// flattened fields the container records.
				if isMap {
					unionInto(pf.elementFieldsOf(st, kv.Key))
				}
			}
			unionInto(pf.elementFieldsOf(st, el))
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
			// A CONTAINER-typed field keeps its element-field taints
			// reachable under the "Field." prefix: h.Items[0].Data,
			// take(h.Items[0]), and for _, x := range h.Items resolve
			// through the flattened "Items.Data" path exactly like
			// nested struct values, because the field holds a slice,
			// map, or array of structs rather than one struct.
			if cfm := pf.elementFieldsOf(st, val); len(cfm) > 0 {
				for k, fv := range cfm {
					if !fv.tainted {
						continue
					}
					out[field.Name()+"."+k] = fv
				}
			}
		}
	}
	return out
}

// indexChainRoot unwraps the trailing index expressions of a container
// value (m[0][0] -> m): every index level names an element of the same
// root container, so the element-field taints live on the root
// expression's record, not on the intermediate index nodes.
func indexChainRoot(e ast.Expr) ast.Expr {
	cur := unparen(e)
	for {
		ix, ok := cur.(*ast.IndexExpr)
		if !ok {
			return cur
		}
		cur = unparen(ix.X)
	}
}

// chainContainsIndex reports whether an expression chain carries an
// index anywhere below the outermost selector (xs[0].Data and
// m[0][0].Inner.Data qualify, b.Inner.Data does not).
func chainContainsIndex(e ast.Expr) bool {
	cur := unparen(e)
	for {
		switch n := cur.(type) {
		case *ast.SelectorExpr:
			cur = unparen(n.X)
		case *ast.IndexExpr:
			return true
		default:
			return false
		}
	}
}

// selectorIndexChain collects the dotted field names of a selector
// expression whose base is an indexed value, unwrapping every trailing
// index level: m[0][0].Inner.Data yields ("Inner.Data", the root
// expression) and makeM(page)[0][0].Inner yields ("Inner", the call).
// Selector names before and between index levels keep their order; the
// caller has already established that at least one selector exists.
func selectorIndexChain(e ast.Expr) (string, ast.Expr) {
	parts := []string{}
	cur := unparen(e)
	for {
		switch n := unparen(cur).(type) {
		case *ast.SelectorExpr:
			parts = append([]string{n.Sel.Name}, parts...)
			cur = unparen(n.X)
		case *ast.IndexExpr:
			cur = unparen(n.X)
		default:
			if len(parts) == 0 {
				return "", nil
			}
			return strings.Join(parts, "."), cur
		}
	}
}

// stripFieldPrefix renames recorded element/struct fields under one
// selected path onto the direct field names of the selected value:
// {"Inner.Data": tainted} with prefix "Inner." becomes {"Data":
// tainted}, the same rename recorded-base arguments receive.
func stripFieldPrefix(m map[string]pageValue, prefix string) map[string]pageValue {
	var out map[string]pageValue
	for k, fv := range m {
		if fv.tainted && strings.HasPrefix(k, prefix) {
			if out == nil {
				out = map[string]pageValue{}
			}
			out[k[len(prefix):]] = fv
		}
	}
	return out
}

// typeAssertBaseOf returns the type assertion at the base of a
// dotted selector/index chain (v.(T).Inner.Data names the
// assertion), or nil when the chain does not start from one.
func typeAssertBaseOf(e ast.Expr) *ast.TypeAssertExpr {
	cur := unparen(e)
	for {
		switch n := unparen(cur).(type) {
		case *ast.SelectorExpr:
			cur = n.X
		case *ast.IndexExpr:
			cur = n.X
		default:
			ta, ok := n.(*ast.TypeAssertExpr)
			if !ok {
				return nil
			}
			return ta
		}
	}
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
			if fields := pf.failClosedCallFields(pf.pc.info.Types[call].Type); len(fields) > 0 {
				pf.callFields[call] = fields
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
			// the caller's field reads and copies stay visible. This
			// covers CONTAINER results too: an unscanned interface
			// method can return []B (or map[K]B, chan B, nested
			// containers) whose elements hold mapped pages, so the
			// page-carrying element leaves fail closed the same way.
			pf.callFieldsFailClosed[call] = true
			if fields := pf.failClosedCallFields(pf.pc.info.Types[call].Type); len(fields) > 0 {
				pf.callFields[call] = fields
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
	// Pointer-argument field mutations inside the callee (b.Set(page)
	// writing b.Data = page, or f(&b, page) writing a field of its
	// pointer argument) land on the caller's recorded state: the
	// summary's mutFields re-bind to the actual argument values here,
	// so the caller's own b.Data read after the call stays sourced.
	pf.applySummaryMutations(st, call, fs, args, argFlows, recvExpr, argOff)
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
// applySummaryMutations lands a callee's pointer-parameter field
// mutations on the caller's state: the callee body stored a page into a
// field of a pointer receiver or pointer argument, and the call site
// re-binds the recorded sources to the actual argument values so
// subsequent field reads of the receiver/argument stay sourced.
func (pf *pageFlow) applySummaryMutations(st *stmtState, call *ast.CallExpr, fs *funcSummary, args []pageValue, argFlows []argFlow, recvExpr ast.Expr, argOff int) {
	if len(fs.mutFields) == 0 {
		return
	}
	bound := func(ft fieldTaint) (pageValue, bool) {
		pv := pageValue{tainted: true}
		tainted := false
		for _, src := range ft.srcs {
			m := int64(maxUnknown)
			switch src.kind {
			case "const":
				m = src.constVal
				tainted = true
			case "param", "paramMax", "value":
				if src.param >= 0 && src.param < len(args) {
					a := args[src.param]
					m = a.maxLen
					if a.hasSym {
						if c, ok := a.sym.isConst(); ok {
							m = c
						}
					}
					if a.tainted || a.hasSrc {
						tainted = true
					}
				} else {
					tainted = true // unknown binding stays conservative
				}
			case "paramField":
				// A mutation sourced from another parameter's FIELD
				// (b.Data = o.Data with o a struct parameter) binds the
				// caller's recorded field bound: a clean or absent
				// field at the call site contributes nothing, exactly
				// like fs.eval's paramField resolution.
				if src.param >= 0 && src.param < len(argFlows) {
					if fv, ok := argFlows[src.param].fields[src.field]; ok {
						m = fv.maxLen
						if fv.tainted {
							tainted = true
						}
					}
				}
			}
			if m == maxUnknown || m > pv.maxLen {
				pv.maxLen = m
			}
		}
		return pv, tainted
	}
	for pidx, fm := range fs.mutFields {
		var argExpr ast.Expr
		switch {
		case argOff == 1 && pidx == 0:
			argExpr = recvExpr
		case pidx-argOff >= 0 && pidx-argOff < len(call.Args):
			argExpr = call.Args[pidx-argOff]
		}
		if argExpr == nil {
			continue
		}
		prefix := ""
		var targets []types.Object
		if o, path := selectorChain(st, argExpr); o != nil {
			targets = append(targets, o)
			prefix = path + "."
		} else if o := objOfDeref(st, argExpr); o != nil {
			targets = append(targets, o)
		} else if u, ok := unparen(argExpr).(*ast.UnaryExpr); ok && u.Op == token.AND {
			// f(&b, page) names the pointed-to variable directly;
			// f(&h.Inner, page) and f(&xs[0], page) address a selected
			// field or a container element, which the callee's mutation
			// binds through the same flattened paths the field reads
			// resolve ("Inner.Data" on h, "Data" on xs).
			switch op := unparen(u.X).(type) {
			case *ast.SelectorExpr:
				if o, path := selectorChain(st, op); o != nil {
					targets = append(targets, o)
					prefix = path + "."
				}
			case *ast.IndexExpr:
				root := indexChainRoot(op)
				if o := objOf(st, root); o != nil {
					targets = append(targets, o)
				} else if o := objOfDeref(st, root); o != nil {
					targets = append(targets, o)
				}
			default:
				if o := objOf(st, u.X); o != nil {
					targets = append(targets, o)
				}
			}
		} else if chainContainsIndex(argExpr) {
			// xs[0].Set(page) and xs[0].Inner.Set(page): a method call
			// on an INDEXED element implicitly addresses the element
			// (the receiver is an addressable container slot), so the
			// callee's pointer-parameter field mutations bind the root
			// container's element records exactly like the &xs[0]
			// argument form — under the selected field path when the
			// receiver carries one.
			chPath, root := selectorIndexChain(argExpr)
			if root == nil {
				root = indexChainRoot(argExpr)
			}
			if root != nil {
				if o := objOf(st, root); o != nil {
					targets = append(targets, o)
					if chPath != "" {
						prefix = chPath + "."
					}
				} else if o := objOfDeref(st, root); o != nil {
					targets = append(targets, o)
					if chPath != "" {
						prefix = chPath + "."
					}
				}
			}
		}
		if al := pf.derefTarget(st, argExpr, 0); al != nil {
			dup := false
			for _, t := range targets {
				if t == al {
					dup = true
					break
				}
			}
			if !dup {
				targets = append(targets, al)
			}
		}
		for _, o := range targets {
			for k, ft := range fm {
				if !ft.tainted {
					continue
				}
				pv, srcTainted := bound(ft)
				if !srcTainted {
					// Every source stayed clean or absent at the call
					// site: the callee's body could not have stored a
					// page through this mutation, so no record lands.
					continue
				}
				path := prefix + k
				m := st.structs[o]
				if m == nil {
					m = map[string]pageValue{}
					st.structs[o] = m
				}
				m[path] = pv
				if o.Parent() == st.pf.pc.pkg.Scope() && st.pkgStructs != nil {
					gm := st.pkgStructs[o]
					if gm == nil {
						gm = map[string]pageValue{}
						st.pkgStructs[o] = gm
					}
					if prev, ok := gm[path]; ok && prev.tainted {
						gm[path] = joinPageValue(prev, pv)
					} else {
						gm[path] = pv
					}
				}
			}
		}
	}
}

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
	fs := &funcSummary{fields: map[string]fieldTaint{}, stringParams: map[int]bool{}, mutFields: map[int]map[string]fieldTaint{}}
	litParamIdx := map[types.Object]int{}
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
	var argFlows []argFlow
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
					argFlows = append(argFlows, argFlow{})
					idx++
					continue
				}
				if arg < len(args) {
					pf.promoteFullPageFields(st, args[arg])
					pv := pf.evalExpr(st, args[arg])
					// The literal body's values are recorded with the
					// LITERAL's own parameter slot as the source, not
					// the caller-context index the argument value
					// carries: a mutation or result re-bound at a call
					// site (including a DIFFERENT call site of the
					// same literal) must resolve through the literal's
					// arg slots. The call-site bound re-evaluates the
					// argument, so the rebased maxLen and symbol stay
					// the caller's.
					pvLit := pv
					if pvLit.hasSrc {
						pvLit.srcParam = idx
					}
					bound = append(bound, pvLit)
					argFlows = append(argFlows, pf.argFlowOf(st, args[arg]))
					if pvLit.tainted {
						st.stmtVars[obj] = pvLit
					} else {
						delete(st.stmtVars, obj)
					}
					// The closure parameter carries the argument's
					// struct-field knowledge: f := func(x B) []byte {
					// return x.Data }; f(b) with b.Data = page must
					// resolve x.Data through the literal body. The
					// copied fields rebase to the literal slot the
					// same way the whole value does.
					pf.materializeStructFields(st, name, args[arg])
					if m := st.structs[obj]; m != nil {
						for fk, fv := range m {
							if fv.hasSrc {
								fv.srcParam = idx
								m[fk] = fv
							}
						}
					}
				} else {
					argFlows = append(argFlows, argFlow{})
				}
				litParamIdx[obj] = idx
				arg++
				idx++
			}
		}
	}
	pf.analyzeStmts(st, lit.Body.List, fs)
	pf.noteStringConvs(st, fs, lit.Body)
	pf.noteFmtSpreads(st, fs, lit.Body)
	// Pointer-parameter field mutations inside the literal
	// (func(q *B, v []byte){ q.Data = v }(&b, page)) are part of the
	// closure's summary exactly like a named callee's: only tainted
	// final field records of struct-POINTER parameters export, and the
	// direct call site re-binds the recorded sources to the actual
	// argument values, so the caller's own b.Data read after the call
	// stays sourced.
	for obj, idx := range litParamIdx {
		if !isStructPtrTyped(obj.Type()) {
			continue
		}
		m, ok := st.structs[obj]
		if !ok || len(m) == 0 {
			continue
		}
		for k, fv := range m {
			if !fv.tainted {
				continue
			}
			fm := fs.mutFields[idx]
			if fm == nil {
				fm = map[string]fieldTaint{}
				fs.mutFields[idx] = fm
			}
			fm[k] = joinFieldTaint(fm[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
		}
	}
	pf.applySummaryMutations(st, call, fs, bound, argFlows, nil, 0)
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

// callRootChain walks an expression down to the call that produced
// it, collecting the dotted selector chain on the way: f(p) yields
// (call, ""), f(p).A.B yields (call, "A.B"), f(p)[0] and *f(p) yield
// (call, ""), and a nested selection off an indexed or dereferenced
// call result keeps the collected names. Any other root yields nil.
func callRootChain(x ast.Expr) (*ast.CallExpr, string) {
	chain := ""
	cur := unparen(x)
	for {
		switch e := unparen(cur).(type) {
		case *ast.SelectorExpr:
			if chain == "" {
				chain = e.Sel.Name
			} else {
				chain = e.Sel.Name + "." + chain
			}
			cur = e.X
		case *ast.IndexExpr:
			cur = e.X
		case *ast.StarExpr:
			cur = e.X
		case *ast.CallExpr:
			return e, chain
		default:
			return nil, ""
		}
	}
}

// callProducedFields resolves the struct-field taints an expression
// derived from a call result: f(p) keeps the callee's recorded fields
// as-is, f(p)[0] and *f(p) keep the element and pointee fields, and
// f(p).A.B keeps the SELECTED value's fields (the dotted chain
// stripped), the same rename recorded-base arguments receive. A call
// node first reached through here may never have been evaluated in
// this statement state, so the cached result is dropped and the call
// re-evaluated like the direct-call argument path does.
func (pf *pageFlow) callProducedFields(st *stmtState, x ast.Expr) map[string]pageValue {
	call, chain := callRootChain(x)
	if call == nil {
		return nil
	}
	delete(pf.values, call)
	pf.evalExpr(st, call)
	cf := pf.callFields[call]
	if len(cf) == 0 {
		return nil
	}
	prefix := ""
	if chain != "" {
		prefix = chain + "."
	}
	out := map[string]pageValue{}
	for k, fv := range cf {
		if fv.tainted && strings.HasPrefix(k, prefix) {
			out[k[len(prefix):]] = fv
		}
	}
	return out
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
		// A plain struct variable: its recorded field taints arrive from
		// the local state, a package-level holder, or the declared
		// parameter leaves (see fieldTaintsOf for the sources).
		fields = pf.fieldTaintsOf(st, st.pf.pc.info.ObjectOf(v))
	case *ast.StarExpr:
		// A dereferenced argument (*p): the pointed-to struct's fields
		// resolve through the pointer binding (p := &b), the parameter
		// leaves of a struct-pointer parameter, or recorded state on
		// the pointer name itself. A call-produced pointee
		// (*makePtr(page)) keeps the callee's recorded fields.
		if o := pf.derefTarget(st, v.X, 0); o != nil {
			fields = pf.fieldTaintsOf(st, o)
		} else {
			fields = pf.callProducedFields(st, v.X)
		}
	case *ast.IndexExpr:
		// An element-carrying argument (xs[0], m[k], m[0][k]): every
		// trailing index names an element of the same root container,
		// so the element-field taints resolve from the root: a local or
		// parameter container keeps them on the variable (parameters
		// through the declared leaves), a composite literal keeps the
		// union of its element struct fields, and a call-produced
		// container (makeList(page)[0]) keeps the callee's element
		// fields.
		root := indexChainRoot(v)
		if o := objOf(st, root); o != nil {
			fields = pf.fieldTaintsOf(st, o)
		} else if lit := structLitOf(root); lit != nil {
			fields = pf.compositeFields(st, lit)
		} else {
			switch unparen(root).(type) {
			case *ast.SelectorExpr, *ast.TypeAssertExpr:
				// A container element reached through a FIELD or an
				// interface assertion (h.Items[0], v.(holder).Items[0]):
				// the container's element fields resolve through the
				// selector's argument flow, which renames recorded
				// paths, parameter leaves, and call-produced prefixes
				// to the container's own direct field names.
				fields = pf.argFlowOf(st, root).fields
			default:
				fields = pf.callProducedFields(st, root)
			}
		}
	case *ast.TypeAssertExpr:
		// An asserted argument (v.(T)): the asserted value keeps the
		// fields recorded on the base (a local interface variable, a
		// call result, or a dereference).
		switch e := unparen(v.X).(type) {
		case *ast.Ident:
			fields = pf.fieldTaintsOf(st, st.pf.pc.info.ObjectOf(e))
		case *ast.StarExpr:
			if o := pf.derefTarget(st, e, 0); o != nil {
				fields = pf.fieldTaintsOf(st, o)
			}
		case *ast.CallExpr:
			fields = pf.callFields[e]
		}
		// An INTERFACE-TYPED parameter asserted to a concrete struct
		// type exposes the asserted type's leaves with the parameter
		// source: the param has no declared leaves of its own, but a
		// helper reading v.(T).Data must keep the caller's field taint
		// bound exactly like a struct parameter's declared leaves do.
		// The asserted leaf names match the caller's concrete argument
		// fields, so the summary's paramField sources bind at the
		// call site. The asserted type is read from the assertion's
		// type EXPRESSION: a two-value assertion (b, ok := v.(T))
		// types the expression node as the (T, bool) tuple.
		if o := objOf(st, v.X); o != nil {
			if idx, ok := st.params[o]; ok && isInterfaceType(o.Type()) {
				if stt, ok := derefStruct(pf.pc.info.Types[v.Type].Type); ok {
					for path, ft := range paramLeafPaths(stt) {
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
		}
	case *ast.SelectorExpr:
		// A struct VALUE read off a field (take(h.Box) with h.Box.Data
		// a recorded page): the base's flattened field taints carry the
		// selector path prefix, and the argument's own field names drop
		// it ("Box.Data" becomes the argument's "Data"), so the
		// summary's paramField sources bind at the call site.
		if obj, path := selectorChain(st, v); obj != nil {
			base := st.structs[obj]
			if len(base) == 0 {
				if gm, ok := st.pkgStructs[obj]; ok {
					base = gm
				}
			}
			prefix := path + "."
			if len(base) > 0 {
				for k, fv := range base {
					if fv.tainted && strings.HasPrefix(k, prefix) {
						if fields == nil {
							fields = map[string]pageValue{}
						}
						fields[k[len(prefix):]] = fv
					}
				}
			}
			// A parameter-held base contributes its declared leaves the
			// same way: the parameter source stays attached to the
			// full dotted path, and the argument flow renames it for
			// the summary's direct field names.
			if fields == nil {
				if idx, ok := st.params[obj]; ok {
					for k, fv := range pf.paramFieldFallback(st, obj, idx) {
						if strings.HasPrefix(k, prefix) {
							if fields == nil {
								fields = map[string]pageValue{}
							}
							fields[k[len(prefix):]] = fv
						}
					}
				}
			}
		} else if parts, root := selectorIndexChain(v); root != nil && (objOf(st, root) != nil || structLitOf(root) != nil) {
			// A selected field of an indexed base (m[0][0].Inner,
			// xs[0].B, []box{{Data: page}}[0].B): the root container's
			// recorded element fields, parameter leaves, or literal
			// element union carry the full dotted selection; stripping
			// it binds the selected value's direct field names, the
			// same rename recorded-base arguments receive.
			if objOf(st, root) != nil {
				fields = stripFieldPrefix(pf.fieldTaintsOf(st, objOf(st, root)), parts+".")
			} else {
				fields = stripFieldPrefix(pf.compositeFields(st, structLitOf(root)), parts+".")
			}
		} else if ta := typeAssertBaseOf(v); ta != nil {
			// v.(T).Inner / v.(T)[k].Inner: an asserted struct VALUE read
			// off an interface variable or parameter keeps the source
			// through the asserted type's leaves, renamed to the selected
			// value's direct field names, so the callee's paramField
			// sources bind at the call site. Recorded interface-variable
			// state keeps the flattened dotted paths (the same rename
			// recorded-base arguments receive).
			chain, _ := selectorIndexChain(v)
			prefix := chain + "."
			if obj := objOf(st, ta.X); obj != nil {
				base := st.structs[obj]
				if len(base) == 0 {
					if gm, ok := st.pkgStructs[obj]; ok {
						base = gm
					}
				}
				for k2, fv := range base {
					if fv.tainted && strings.HasPrefix(k2, prefix) {
						if fields == nil {
							fields = map[string]pageValue{}
						}
						fields[k2[len(prefix):]] = fv
					}
				}
				if fields == nil {
					if idx, ok := st.params[obj]; ok && isInterfaceType(obj.Type()) {
						if stt, ok := derefStruct(pf.pc.info.Types[ta.Type].Type); ok {
							for path, ft := range paramLeafPaths(stt) {
								if !paramCanCarryPage(ft) || !strings.HasPrefix(path, prefix) {
									continue
								}
								if fields == nil {
									fields = map[string]pageValue{}
								}
								fields[path[len(prefix):]] = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: path, hasSrc: true}
							}
						}
					}
				}
			}
		} else {
			// A struct value produced by a call (take(makeBox(page).B),
			// take(makeBox(page).A.B)): the callee's flattened field
			// paths carry the full dotted selection (including the
			// outermost name), and stripping it binds the selected
			// value's direct field names, the same rename recorded-base
			// arguments receive.
			fields = pf.callProducedFields(st, v)
		}
	case *ast.UnaryExpr:
		if v.Op == token.AND {
			// &b and &h.Box: the address of a variable (or its
			// selected field) exposes the same field taints as the
			// value itself — takePtr(&b) after b.Data = page keeps the
			// callee's paramField sources bound.
			fields = pf.argFlowOf(st, v.X).fields
		} else if v.Op == token.ARROW {
			// A receive (x := <-ch): the received struct carries the
			// field taints recorded on the channel by the send — via
			// the channel variable, a struct-field channel (h.Ch), or
			// an alias variable bound to either — so the assignment
			// binds x's fields from the channel's records.
			fields = pf.chanRecvFields(st, v.X)
		}
	case *ast.CompositeLit:
		fields = pf.compositeFields(st, v)
	case *ast.CallExpr:
		if len(v.Args) == 1 && pf.pc.info.Types[v.Fun].IsType() {
			// Type conversions (any(x), T(x)) keep the converted
			// argument's field provenance: boxing a struct into an
			// interface, or converting between named byte shapes, does
			// not flatten the leaves a callee reads after the
			// assertion.
			fields = pf.argFlowOf(st, v.Args[0]).fields
		} else {
			fields = pf.callFields[v]
		}
	}
	return argFlow{pv: pv, fields: fields}
}

// paramFieldFallback materializes the declared leaf field sources of a
// struct parameter, shared by the argument-flow and materialization
// paths.
func (pf *pageFlow) paramFieldFallback(st *stmtState, obj types.Object, idx int) map[string]pageValue {
	out := map[string]pageValue{}
	addLeaves := func(t2 types.Type) {
		for path, ft := range paramLeafPaths(t2) {
			if _, has := out[path]; has {
				continue
			}
			if !paramCanCarryPage(ft) {
				continue
			}
			out[path] = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: path, hasSrc: true}
		}
	}
	addLeaves(obj.Type())
	// A container parameter of struct elements ([]box, map[K]box, ...)
	// exposes the element fields through element extraction (xs[0],
	// m[k]): materialize the element's declared leaves with the same
	// parameter source so take(xs[0]) at a call site binds the callee's
	// param fields even when the container arrives as a parameter.
	// Nested containers ([1][1]box, map[K][]box) expose the same leaf
	// paths at every depth: each trailing index names an element of
	// the same root, so every container level must contribute. The seen
	// set stops a self-referential named container (type R []R).
	seen := map[types.Type]bool{}
	for et := containerElemType(obj.Type()); et != nil && !seen[et]; et = containerElemType(et) {
		seen[et] = true
		addLeaves(et)
	}
	// A map parameter exposes the KEY's fields to a key-only range
	// (for k := range m with m map[*box]int keeps k.Data caller
	// sourced): the declared key leaves and key container chains bind
	// exactly like the value side.
	if mtyp := mapUnderlying(obj.Type()); mtyp != nil {
		addLeaves(mtyp.Key())
		seenKey := map[types.Type]bool{}
		for et := containerElemType(mtyp.Key()); et != nil && !seenKey[et]; et = containerElemType(et) {
			seenKey[et] = true
			addLeaves(et)
		}
	}
	return out
}

// containerElemType unwraps one container level of a type (slice, array,
// map value, channel element) and returns the element type, or nil when
// the type is not a direct container.
func containerElemType(t types.Type) types.Type {
	return containerElemTypeSeen(t, map[types.Type]bool{})
}

// mapUnderlying returns the map type behind an alias or a NAMED map
// (type M map[*B]int): the named wrapper must unwrap to its underlying
// map, or the key-side leaves and the literal key unions are lost
// (types.Unalias does not unwrap named types).
func mapUnderlying(t types.Type) *types.Map {
	switch u := types.Unalias(t).(type) {
	case *types.Map:
		return u
	case *types.Named:
		return mapUnderlying(u.Underlying())
	case *types.Pointer:
		// A pointer-wrapped map parameter or field (m *map[*B]int)
		// exposes the same key leaves as the map value itself: the
		// key-only range and key-store rules dereference only map
		// (and named-map) wrappers, so the pointer must unwrap here
		// or every key leaf of the pointed-to map is lost.
		return mapUnderlying(u.Elem())
	}
	return nil
}

// containerElemTypeSeen is the recursive core: a named container
// (type matrix [1][1]box) unwraps through its underlying chain; a
// self-referential named container (type R []R) stops at the
// revisiting type instead of recursing forever.
func containerElemTypeSeen(t types.Type, seen map[types.Type]bool) types.Type {
	switch v := types.Unalias(t).(type) {
	case *types.Slice:
		return v.Elem()
	case *types.Array:
		return v.Elem()
	case *types.Map:
		return v.Elem()
	case *types.Chan:
		return v.Elem()
	case *types.Pointer:
		// A pointer to a container ((*q)[0], for k, v := range m with
		// m map[*[]B]int) names the same element leaves as the pointed-to
		// container: the pointer unwraps to its element chain.
		return v.Elem()
	case *types.Named:
		if seen[v] {
			return nil
		}
		seen[v] = true
		return containerElemTypeSeen(v.Underlying(), seen)
	}
	return nil
}

// fieldTaintsOf returns the recorded struct-field taints of one local,
// package-scope, or parameter object: recorded local struct state first,
// then package-level field maps, then the declared leaf sources of a
// struct holding, pointer, or container parameter.
func (pf *pageFlow) fieldTaintsOf(st *stmtState, obj types.Object) map[string]pageValue {
	if obj == nil {
		return nil
	}
	var out map[string]pageValue
	if m, ok := st.structs[obj]; ok && len(m) > 0 {
		out = map[string]pageValue{}
		for k, fv := range m {
			out[k] = fv
		}
	} else if gm, ok := st.pkgStructs[obj]; ok && len(gm) > 0 {
		out = map[string]pageValue{}
		for k, fv := range gm {
			out[k] = fv
		}
	}
	// A partial local record must not suppress the untouched declared
	// leaves of a struct parameter (o.Other = 1 must not hide a
	// caller-supplied o.Data): the parameter fallback joins for every
	// path the local state never recorded, the same policy
	// materializeStructFields applies to copies.
	if idx, ok := st.params[obj]; ok {
		for path, fv := range pf.paramFieldFallback(st, obj, idx) {
			if _, recorded := out[path]; recorded {
				continue
			}
			if out == nil {
				out = map[string]pageValue{}
			}
			out[path] = fv
		}
	}
	return out
}

// derefTarget resolves the object a dereference expression names
// through recorded pointer bindings: *p with p := &b names b, and plain
// aliases are followed with a depth cap (p := q; q := &b). When nothing
// is bound the identifier itself is returned, so a struct-pointer
// parameter contributes its declared leaves through the parameter
// fallback.
func (pf *pageFlow) derefTarget(st *stmtState, e ast.Expr, depth int) types.Object {
	if st == nil || e == nil || depth > 4 {
		return nil
	}
	id := identOf(unparen(e))
	if id == nil {
		return nil
	}
	obj := pf.pc.info.ObjectOf(id)
	if obj == nil {
		return nil
	}
	b, ok := st.localBindings[obj]
	if !ok {
		return obj
	}
	switch u := unparen(b).(type) {
	case *ast.UnaryExpr:
		if u.Op == token.AND {
			if t := identOf(u.X); t != nil {
				if tgt := pf.pc.info.ObjectOf(t); tgt != nil {
					return tgt
				}
			}
		}
		return obj
	case *ast.Ident:
		return pf.derefTarget(st, u, depth+1)
	default:
		return obj
	}
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
// failClosedCallFields returns the worst-case page-carrying fields of
// an unprovable call result: every reachable page-carrying leaf path
// of a struct result, plus, for CONTAINER results ([]B, map[K]B,
// chan B, and nested containers at any depth), the element and KEY
// leaves. The leaves are the same flattened dotted paths the caller's
// field reads resolve, so a struct that WRAPS a container
// (holder{Items []B}) exposes "Items.Data" exactly like a direct Data
// field exposes "Data", and a map-key struct keeps k.Data visible.
// Recursion stops at struct types declared OUTSIDE the scanned package
// (an embedded *bytes.Reader contributes its exported surface, not its
// private src field): unreachable leaves would otherwise promote to
// whole-value taints and false-reject readers wrapping stdlib types.
// An unscanned body can return a container whose struct elements hold
// a mapped page, so the caller's element field reads and copies stay
// visible exactly like direct struct fields.
func (pf *pageFlow) failClosedCallFields(t types.Type) map[string]pageValue {
	var out map[string]pageValue
	addPV := func(path string) {
		if out == nil {
			out = map[string]pageValue{}
		}
		out[path] = pageValue{tainted: true, maxLen: maxUnknown}
	}
	// walk adds every page-carrying leaf path of a struct type: direct
	// byte/interface fields, nested struct fields (with promoted
	// embedded leaves), container-field element leaves at every depth,
	// and map-key leaves, all under the dotted prefix the caller's
	// reads resolve.
	var walk func(tt types.Type, prefix string)
	walkSeen := map[types.Type]bool{}
	walk = func(tt types.Type, prefix string) {
		stt, ok := derefStruct(tt)
		if !ok {
			return
		}
		if sp := structDeclPkg(tt); sp != nil && sp != pf.pc.pkg {
			// A foreign struct's fields are unreachable from this
			// package's reads; only its page-CARRYING VALUE as a field
			// of the scanned struct matters, handled by the caller.
			return
		}
		if walkSeen[stt] {
			return // recursion through a self-referencing pointer field
		}
		walkSeen[stt] = true
		for i := 0; i < stt.NumFields(); i++ {
			f := stt.Field(i)
			p := f.Name()
			if prefix != "" {
				p = prefix + "." + f.Name()
			}
			if _, isSt := derefStruct(f.Type()); isSt {
				walk(f.Type(), p)
				if f.Anonymous() {
					// Promoted leaves of an embedded struct also bind
					// without the type-name segment, exactly like the
					// caller's promoted field reads.
					walk(f.Type(), prefix)
				}
				continue
			}
			if paramCanCarryPage(f.Type()) {
				addPV(p)
			}
			// A CONTAINER-typed field keeps its ELEMENT leaves under the
			// field prefix: h.Items with holder.Items []B exposes
			// "Items.Data" so h.Items[0].Data resolves the fail-closed
			// source like the read path does.
			fieldSeen := map[types.Type]bool{}
			for et := containerElemType(f.Type()); et != nil && !fieldSeen[et]; et = containerElemType(et) {
				fieldSeen[et] = true
				walk(et, p)
			}
			if mft := mapUnderlying(f.Type()); mft != nil {
				walk(mft.Key(), p)
				keySeen := map[types.Type]bool{}
				for et := containerElemType(mft.Key()); et != nil && !keySeen[et]; et = containerElemType(et) {
					keySeen[et] = true
					walk(et, p)
				}
			}
		}
		walkSeen[stt] = false
	}
	if _, ok := derefStruct(t); ok {
		walk(t, "")
	}
	// CONTAINER results: the extracted element structs' leaves bind the
	// direct field names the caller's trailing reads resolve
	// (makeList(page)[0].Data reads "Data").
	seen := map[types.Type]bool{}
	for et := containerElemType(t); et != nil && !seen[et]; et = containerElemType(et) {
		seen[et] = true
		// Only the last container level is a struct element; walk it so
		// its nested fields and sub-containers stay reachable.
		walk(et, "")
	}
	if mt := mapUnderlying(t); mt != nil {
		// A map result's KEY can carry page-bearing struct fields too
		// (for k := range m with m map[*box]int keeps k.Data).
		walk(mt.Key(), "")
	}
	return out
}

// structDeclPkg returns the package that declared a struct type, or
// nil for anonymous structs (which belong to the package whose code
// declares them).
func structDeclPkg(t types.Type) *types.Package {
	for {
		switch v := t.(type) {
		case *types.Pointer:
			t = v.Elem()
		case *types.Named:
			return v.Obj().Pkg()
		case *types.Alias:
			t = types.Unalias(t)
		default:
			return nil
		}
	}
}

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
			if gm, ok := st.pkgStructs[obj]; ok {
				for k, fv := range gm {
					if fv.tainted {
						fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
					}
				}
			}
			// A returned struct PARAMETER (func id(b box) box { return
			// b }) and a returned container element (return xs[0], with
			// xs a container parameter) carry the caller's field taints
			// through the declared leaf sources: the local state never
			// records the untouched fields, so without the fallback the
			// summary loses them and id(box{Data: page}).Data goes clean.
			if idx, ok := st.params[obj]; ok {
				for path, fv := range pf.paramFieldFallback(st, obj, idx) {
					fs.fields[path] = joinFieldTaint(fs.fields[path], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
				}
			}
		}
		return
	}
	if sel, ok := unparen(expr).(*ast.SelectorExpr); ok {
		// A returned selected field (return makeBox(p).Inner, return
		// b.Inner): the selected value's direct field taints resolve
		// from the base's recorded fields, parameter leaves, or the
		// callee's flattened summary, with the selection prefix
		// stripped, so a call-site read of the returned value's fields
		// (x := f(page); x.Data) keeps the caller's source. A param-held
		// base keeps the paramField source on the ORIGINAL leaf path so
		// the call-site argument binding matches its flattened record.
		var fields map[string]pageValue
		if obj, path := selectorChain(st, sel); obj != nil {
			prefix := path + "."
			if idx, ok := st.params[obj]; ok {
				for k, fv := range pf.paramFieldFallback(st, obj, idx) {
					if strings.HasPrefix(k, prefix) {
						if fields == nil {
							fields = map[string]pageValue{}
						}
						fields[k[len(prefix):]] = fv
					}
				}
			}
			base := st.structs[obj]
			if len(base) == 0 {
				if gm, ok := st.pkgStructs[obj]; ok {
					base = gm
				}
			}
			for k, fv := range base {
				if fv.tainted && strings.HasPrefix(k, prefix) {
					if fields == nil {
						fields = map[string]pageValue{}
					}
					fields[k[len(prefix):]] = fv
				}
			}
		} else {
			fields = pf.callProducedFields(st, sel)
		}
		if len(fields) > 0 {
			for k, fv := range fields {
				if fv.tainted {
					fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
				}
			}
		}
		return
	}
	if _, ok := unparen(expr).(*ast.IndexExpr); ok {
		// A returned container element (returns xs[0], m[0][0]): every
		// trailing index names an element of the same root container,
		// so the recorded element-field taints resolve from the root
		// expression, the same unwrap argument flow and field reads
		// use.
		root := indexChainRoot(expr)
		if o := objOf(st, root); o != nil {
			if m, ok := st.structs[o]; ok {
				for k, fv := range m {
					if fv.tainted {
						fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
					}
				}
			}
			if idx, ok := st.params[o]; ok {
				for path, fv := range pf.paramFieldFallback(st, o, idx) {
					fs.fields[path] = joinFieldTaint(fs.fields[path], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
				}
			}
		} else if lit := structLitOf(root); lit != nil {
			// A returned element of an INLINE container literal
			// (return []box{{Data: p}}[0]): the literal's element-field
			// union carries the result exactly like a whole-literal
			// return of the same container.
			if fm := pf.compositeFields(st, lit); len(fm) > 0 {
				for k, fv := range fm {
					if fv.tainted {
						fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
					}
				}
			}
		} else if call, ok := unparen(root).(*ast.CallExpr); ok {
			// A returned element of a CALL-produced container
			// (return makeList(page)[0]): re-evaluate the call (a cached
			// result may predate callee stabilization) and record the
			// callee's element fields.
			delete(pf.values, call)
			pf.evalExpr(st, call)
			if m, ok := pf.callFields[call]; ok {
				for k, fv := range m {
					if fv.tainted {
						fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
					}
				}
			}
		}
		return
	}
	if ta, ok := unparen(expr).(*ast.TypeAssertExpr); ok {
		// A returned asserted value (return v.(T) with v an
		// interface-typed parameter): the asserted type's leaves carry
		// the parameter source, so an identity helper over an
		// interface keeps the caller's field taints bound.
		if o := objOf(st, ta.X); o != nil {
			if idx, ok := st.params[o]; ok && isInterfaceType(o.Type()) {
				if stt, ok := derefStruct(pf.pc.info.Types[ta.Type].Type); ok {
					for path, ft := range paramLeafPaths(stt) {
						if !paramCanCarryPage(ft) {
							continue
						}
						fs.fields[path] = joinFieldTaint(fs.fields[path], fieldTaint{tainted: true, srcs: maxSrcOf(pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: path, hasSrc: true})})
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
	// Nested struct, embedded, and container fields of the literal keep
	// their flattened dotted paths on the summary (compositeFields
	// semantics): a call site reading makeBox(page).Inner.Data, or an
	// argument flow selecting .Inner off the result, resolves the same
	// paths a locally-bound composite keeps. The whole-field record
	// above stays for direct whole-value consumers.
	if fm := pf.compositeFields(st, lit); len(fm) > 0 {
		for k, fv := range fm {
			if fv.tainted {
				fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
			}
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
// resolves the same way. The lookup package is required: go/types
// refuses to resolve UNEXPORTED field names when pkg is nil, and
// production records (holder.data) legitimately use unexported fields.
func leafPathType(pkg *types.Package, t types.Type, path string) types.Type {
	for _, part := range strings.Split(path, ".") {
		if _, ok := derefStruct(t); !ok {
			return nil
		}
		// Embedded fields promote their fields and leaves, exactly like
		// go/types resolves o.Data through an embedded struct: direct
		// name lookup alone would miss the promoted path.
		f, _, _ := types.LookupFieldOrMethod(t, true, pkg, part)
		fv, ok := f.(*types.Var)
		if !ok {
			return nil
		}
		t = fv.Type()
	}
	return t
}

// paramLeafPaths returns every leaf field path of a struct type that can
// carry page bytes, flattened with dotted names ("Data", "Inner.Data"),
// so parameter fallback sources cover nested struct fields exactly like
// compositeFields flattens literals.
// leafNameSet returns the declared page-carrying leaf paths of a type,
// used to split recorded map-entry fields between the key and the value
// variable of a two-variable map range. Container types contribute
// their element struct leaves (for k, v := range m with m map[int][]B
// binds v[0].Data to the value variable), pointer-wrapped containers
// and map keys contribute theirs too ((*k)[0].Data with
// m map[*[]B]int binds to the key variable), so the split keeps every
// leaf the range body reads.
func leafNameSet(t types.Type) map[string]bool {
	out := map[string]bool{}
	add := func(tt types.Type) {
		for path, ft := range paramLeafPaths(tt) {
			if paramCanCarryPage(ft) {
				out[path] = true
			}
		}
	}
	add(t)
	seen := map[types.Type]bool{}
	for et := containerElemType(t); et != nil && !seen[et]; et = containerElemType(et) {
		seen[et] = true
		add(et)
	}
	if mt := mapUnderlying(t); mt != nil {
		add(mt.Key())
		keySeen := map[types.Type]bool{}
		for et := containerElemType(mt.Key()); et != nil && !keySeen[et]; et = containerElemType(et) {
			keySeen[et] = true
			add(et)
		}
	}
	return out
}

// isStructPtrTyped reports whether a function parameter is a pointer a
// callee can mutate through (b.Data = p with b *box): value parameters
// are copies and their field mutations never reach callers.
func isStructPtrTyped(t types.Type) bool {
	for {
		switch u := types.Unalias(t).(type) {
		case *types.Pointer:
			return true
		case *types.Named:
			t = u.Underlying()
		default:
			return false
		}
	}
}

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
				if f.Anonymous() {
					// Promoted leaves of an embedded struct also bind
					// without the type-name segment (o.Data with Data
					// declared on an embedded inner struct, through any
					// number of embedding levels): the callee's
					// paramField sources name the promoted path the
					// field read resolved, so the fallback must expose
					// the alias or take(o) loses the caller's taint.
					walk(f.Type(), prefix)
				}
			} else if paramCanCarryPage(f.Type()) {
				out[p] = f.Type()
			}
			// A CONTAINER-typed field keeps its ELEMENT leaves under the
			// field prefix: h.Items with holder.Items []box exposes
			// "Items.Data" so h.Items[0].Data, take(h.Items[0]), and
			// for _, x := range h.Items resolve the same parameter
			// source as a container parameter's own elements. Every
			// container depth of the field contributes, element structs
			// recurse for their own container fields, and a MAP field
			// also exposes its KEY leaves for key-only ranges. The seen
			// set is per field so repeated element types on sibling
			// fields keep their prefixed paths.
			fieldSeen := map[types.Type]bool{}
			for et := containerElemType(f.Type()); et != nil && !fieldSeen[et]; et = containerElemType(et) {
				fieldSeen[et] = true
				for path, ft := range paramLeafPaths(et) {
					if !paramCanCarryPage(ft) {
						continue
					}
					out[p+"."+path] = ft
				}
			}
			if mft := mapUnderlying(f.Type()); mft != nil {
				keySeen := map[types.Type]bool{}
				for path, ft := range paramLeafPaths(mft.Key()) {
					if !paramCanCarryPage(ft) {
						continue
					}
					out[p+"."+path] = ft
				}
				for et := containerElemType(mft.Key()); et != nil && !keySeen[et]; et = containerElemType(et) {
					keySeen[et] = true
					for path, ft := range paramLeafPaths(et) {
						if !paramCanCarryPage(ft) {
							continue
						}
						out[p+"."+path] = ft
					}
				}
			}
		}
		walkSeen[st] = false
	}
	walk(t, "")
	return out
}

// assertLeafType resolves a dotted field path against a TYPE
// ASSERTION's asserted type: the struct's own leaves first, then
// the leaves of a container's struct ELEMENTS (v.([]box)[0].Data
// reads the element leaves because the extraction names an
// element). A seen set stops self-referential named containers.
func assertLeafType(pkg *types.Package, t types.Type, path string) types.Type {
	if ft := leafPathType(pkg, t, path); ft != nil {
		return ft
	}
	seen := map[types.Type]bool{}
	for et := containerElemType(t); et != nil && !seen[et]; et = containerElemType(et) {
		seen[et] = true
		if ft := leafPathType(pkg, et, path); ft != nil {
			return ft
		}
	}
	return nil
}

// paramFieldType returns the static type of a named field of the struct
// type of parameter object obj, or nil when obj is not a struct parameter.
func paramFieldType(pkg *types.Package, obj types.Object, name string) types.Type {
	return leafPathType(pkg, obj.Type(), name)
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
