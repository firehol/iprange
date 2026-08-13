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
		if !r.tainted || !taintOf(r.srcs[0]) {
			continue
		}
		out.tainted = true
		for _, s := range r.srcs {
			if m := val(s); m > out.maxLen {
				out.maxLen = m
			}
		}
	}
	for name, r := range fs.fields {
		if !r.tainted || !taintOf(r.srcs[0]) {
			continue
		}
		if fields == nil {
			fields = map[string]pageValue{}
		}
		pv := pageValue{tainted: true, maxLen: maxUnknown}
		for _, s := range r.srcs {
			if m := val(s); m > pv.maxLen {
				pv.maxLen = m
			}
		}
		fields[name] = pv
	}
	return out, fields
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
						st := newStmtState(pf, nil, pkgVars)
						if pv := pf.evalExpr(st, vs.Values[i]); pv.tainted {
							pkgVars[name.Name] = pv
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
			fs := &funcSummary{fields: map[string]fieldTaint{}, params: countParams(fd)}
			sums[funcKey(fd)] = fs
			st := newStmtState(pf, fd, pkgVars)
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
				st := newStmtState(pf, fd, pkgVars)
				pf.analyzeFunc(st, sums[key])
				if !summaryEqual(before, sums[key]) {
					changed = true
				}
			}
		}
	}
	return sums, pf
}

func countParams(fd *ast.FuncDecl) int {
	n := 0
	if fd.Type.Params != nil {
		for _, f := range fd.Type.Params.List {
			n += len(f.Names)
		}
	}
	return n
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
		results: append([]fieldTaint{}, fs.results...),
		fields:  map[string]fieldTaint{},
		params:  fs.params,
	}
	for k, v := range fs.fields {
		out.fields[k] = v
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
	pf       *pageFlow
	fd       *ast.FuncDecl
	params   map[types.Object]int
	stmtVars map[types.Object]pageValue
	structs  map[types.Object]map[string]pageValue
	pkgVars  map[string]pageValue
}

func newStmtState(pf *pageFlow, fd *ast.FuncDecl, pkgVars map[string]pageValue) *stmtState {
	st := &stmtState{
		pf:       pf,
		fd:       fd,
		params:   map[types.Object]int{},
		stmtVars: map[types.Object]pageValue{},
		structs:  map[types.Object]map[string]pageValue{},
		pkgVars:  pkgVars,
	}
	if fd != nil && fd.Type.Params != nil {
		idx := 0
		for _, f := range fd.Type.Params.List {
			for _, name := range f.Names {
				st.params[pf.pc.info.ObjectOf(name)] = idx
				idx++
			}
		}
	}
	return st
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
	fs.results = make([]fieldTaint, 0)
	fs.fields = map[string]fieldTaint{}
	if st.fd.Type.Results != nil {
		for range st.fd.Type.Results.List {
			fs.results = append(fs.results, fieldTaint{})
		}
	}
	pf.analyzeStmts(st, st.fd.Body.List, fs)
}

func (pf *pageFlow) analyzeStmts(st *stmtState, list []ast.Stmt, fs *funcSummary) {
	for _, s := range list {
		switch v := s.(type) {
		case *ast.AssignStmt:
			for i, rhs := range v.Rhs {
				if i >= len(v.Lhs) {
					break
				}
				pv := pf.evalExpr(st, rhs)
				var fields map[string]pageValue
				switch r := unparen(rhs).(type) {
				case *ast.CallExpr:
					fields = pf.callFields[r]
				case *ast.CompositeLit:
					fields = pf.compositeFields(st, r)
				}
				assignTarget(st, v.Lhs[i], pv)
				if obj := objOf(st, v.Lhs[i]); obj != nil && len(fields) > 0 {
					if st.structs[obj] == nil {
						st.structs[obj] = map[string]pageValue{}
					}
					for k, fv := range fields {
						st.structs[obj][k] = fv
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
								if pv.tainted {
									st.stmtVars[pf.pc.info.ObjectOf(name)] = pv
								}
							}
						}
					}
				}
			}
		case *ast.ReturnStmt:
			for i, rv := range v.Results {
				pv := pf.evalExpr(st, rv)
				if i < len(fs.results) && pv.tainted {
					fs.results[i] = fieldTaint{tainted: true, srcs: maxSrcOf(pv)}
				}
			}
			if len(v.Results) == 1 && len(fs.results) >= 1 {
				pf.propagateStructResult(st, v.Results[0], fs)
			}
		case *ast.IfStmt:
			pf.analyzeStmts(st, v.Body.List, fs)
			if blk, ok := v.Else.(*ast.BlockStmt); ok {
				pf.analyzeStmts(st, blk.List, fs)
			}
		case *ast.ForStmt:
			pf.analyzeStmts(st, v.Body.List, fs)
		case *ast.RangeStmt:
			pf.analyzeStmts(st, v.Body.List, fs)
		case *ast.ExprStmt:
			pf.evalExpr(st, v.X)
		case *ast.SwitchStmt:
			for _, c := range v.Body.List {
				if cc, ok := c.(*ast.CaseClause); ok {
					pf.analyzeStmts(st, cc.Body, fs)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, c := range v.Body.List {
				if cc, ok := c.(*ast.CaseClause); ok {
					pf.analyzeStmts(st, cc.Body, fs)
				}
			}
		case *ast.DeferStmt:
			pf.evalExpr(st, v.Call)
		case *ast.GoStmt:
			pf.evalExpr(st, v.Call)
		case *ast.SelectStmt:
			for _, c := range v.Body.List {
				cc, ok := c.(*ast.CommClause)
				if !ok {
					continue
				}
				switch comm := cc.Comm.(type) {
				case *ast.SendStmt:
					pf.evalExpr(st, comm.Chan)
					pf.evalExpr(st, comm.Value)
				case *ast.AssignStmt:
					for _, rhs := range comm.Rhs {
						pf.evalExpr(st, rhs)
					}
				case *ast.ExprStmt:
					pf.evalExpr(st, comm.X)
				}
				pf.analyzeStmts(st, cc.Body, fs)
			}
		case *ast.LabeledStmt:
			pf.analyzeStmts(st, []ast.Stmt{v.Stmt}, fs)
		case *ast.BlockStmt:
			pf.analyzeStmts(st, v.List, fs)
		}
	}
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
		if pv.tainted {
			st.stmtVars[obj] = pv
		} else {
			delete(st.stmtVars, obj)
		}
	case *ast.SelectorExpr:
		if obj := objOf(st, v.X); obj != nil {
			m := st.structs[obj]
			if m == nil {
				m = map[string]pageValue{}
				st.structs[obj] = m
			}
			if pv.tainted {
				m[v.Sel.Name] = pv
			} else {
				delete(m, v.Sel.Name)
			}
		}
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
			} else if idx, ok := st.params[obj]; ok && typeCanCarryPage(obj.Type()) {
				out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, hasSrc: true}
			}
		} else if pv, ok := st.pkgVars[v.Name]; ok {
			out = pv
		}
	case *ast.SelectorExpr:
		if obj := objOf(st, v.X); obj != nil {
			if m, ok := st.structs[obj]; ok {
				if pv, ok := m[v.Sel.Name]; ok {
					out = pv
				}
			} else if idx, ok := st.params[obj]; ok && typeCanCarryPage(paramFieldType(obj, v.Sel.Name)) {
				out = pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: v.Sel.Name, hasSrc: true}
			}
		}
	case *ast.SliceExpr:
		base := pf.evalExpr(st, v.X)
		if base.tainted {
			out = pageValue{tainted: true, sym: sliceLenSym(v, st), hasSym: true}
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
		}
	case *ast.ParenExpr:
		out = pf.evalExpr(st, v.X)
	}
	pf.values[e] = out
	return out
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
		// Slice/array/map literals: their elements are evaluated only
		// when the literal's own element type can carry a taint; the
		// element expressions are visited by the assignment rules.
		return pageValue{}
	}
	tainted := false
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
			if pv.maxLen > maxLen {
				maxLen = pv.maxLen
			}
		}
	}
	if !tainted {
		return pageValue{}
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
		return nil
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
		}
	}
	return out
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

// evalCall resolves callee summaries and mints.
func (pf *pageFlow) evalCall(st *stmtState, call *ast.CallExpr) pageValue {
	// Direct call of a function literal: bind the literal's parameters to
	// the evaluated argument taints and analyze the body in the enclosing
	// variable state, then carry the literal's return taint as the call
	// result (a closure returning a mapped view or an owned copy of it).
	if lit, ok := unparen(call.Fun).(*ast.FuncLit); ok {
		return pf.analyzeFuncLitCall(st, lit, call.Args)
	}
	obj := calleeObject(pf.pc, call)
	// Calls through a function-typed variable whose package-level
	// initializer is a func literal: analyze the literal body here with
	// the call-site arguments bound to its parameters, so a complete-page
	// sink inside the literal is visible (the literal's own file position
	// is scanned by the rules pass; the taint must be supplied at the
	// call). Variables without a literal initializer have no scanable body.
	if v, ok := obj.(*types.Var); ok && !pf.pc.reassignedVars[v] {
		if lit, ok2 := pf.pc.varInits[v]; ok2 {
			if fl, ok3 := unparen(lit).(*ast.FuncLit); ok3 {
				return pf.analyzeFuncLitCall(st, fl, call.Args)
			}
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		// Builtins (copy/append/len/delete) and type conversions never
		// return taint themselves, but their arguments can carry it: the
		// rule pass reads the complete-page sinks through the shared
		// expression cache, so every argument must be evaluated here.
		for _, a := range call.Args {
			pf.evalExpr(st, a)
		}
		return pageValue{}
	}
	// Arguments are evaluated for every resolved call too (mints, stdlib
	// consumers, and module summaries), so tainted expressions inside
	// arguments are always visible to the rule pass.
	for _, a := range call.Args {
		pf.evalExpr(st, a)
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
			pv := pageValue{tainted: true, maxLen: maxUnknown}
			if len(call.Args) == 3 {
				if s, ok := symbolOf(call.Args[2], st); ok {
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
		return pageValue{}
	}
	args := make([]pageValue, len(call.Args))
	argVals := make([]symbol, len(call.Args))
	argFlows := make([]argFlow, len(call.Args))
	for i, a := range call.Args {
		args[i] = pf.evalExpr(st, a)
		argVals[i], _ = symbolOf(a, st)
		argFlows[i] = pf.argFlowOf(st, a)
	}
	rv, fields := fs.eval(args, argVals, argFlows)
	if rv.tainted {
		return rv
	}
	if len(fields) > 0 {
		pf.callFields[call] = fields
		return pageValue{tainted: true, maxLen: maxUnknown}
	}
	return pageValue{}
}

// analyzeFuncLitBody analyzes a closure body in the current statement
// state. The closure shares the enclosing variable scope, so captured
// variables keep their taint; parameters are left unbound (they are
// assigned at direct-call sites).
func (pf *pageFlow) analyzeFuncLitBody(st *stmtState, lit *ast.FuncLit) {
	fs := &funcSummary{}
	pf.analyzeStmts(st, lit.Body.List, fs)
}

// analyzeFuncLitCall binds a closure's parameters to the call-site
// argument taints, analyzes the body, and returns the closure's first
// result taint as the call result.
func (pf *pageFlow) analyzeFuncLitCall(st *stmtState, lit *ast.FuncLit, args []ast.Expr) pageValue {
	fs := &funcSummary{}
	if lit.Type.Results != nil {
		for range lit.Type.Results.List {
			fs.results = append(fs.results, fieldTaint{})
		}
	}
	if lit.Type.Params != nil {
		idx := 0
		for _, f := range lit.Type.Params.List {
			for _, name := range f.Names {
				obj := pf.pc.info.ObjectOf(name)
				if idx < len(args) {
					pv := pf.evalExpr(st, args[idx])
					if pv.tainted {
						st.stmtVars[obj] = pv
					} else {
						delete(st.stmtVars, obj)
					}
				}
				idx++
			}
		}
	}
	pf.analyzeStmts(st, lit.Body.List, fs)
	if lit.Type.Params != nil {
		for _, f := range lit.Type.Params.List {
			for _, name := range f.Names {
				delete(st.stmtVars, pf.pc.info.ObjectOf(name))
			}
		}
	}
	if len(fs.results) == 0 || !fs.results[0].tainted {
		return pageValue{}
	}
	return pageValue{tainted: true, maxLen: maxUnknown}
}

// argFlowOf binds the value taint and struct-field taints of one call
// argument: local struct variables, composite literals, and prior call
// results (whose fields were recorded when the call was evaluated).
func (pf *pageFlow) argFlowOf(st *stmtState, a ast.Expr) argFlow {
	pv := pf.evalExpr(st, a)
	var fields map[string]pageValue
	switch v := unparen(a).(type) {
	case *ast.Ident:
		fields = st.structs[st.pf.pc.info.ObjectOf(v)]
	case *ast.CompositeLit:
		fields = pf.compositeFields(st, v)
	case *ast.CallExpr:
		fields = pf.callFields[v]
	}
	return argFlow{pv: pv, fields: fields}
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

// propagateStructResult records tainted fields of a returned composite
// literal into the summary.
func (pf *pageFlow) propagateStructResult(st *stmtState, expr ast.Expr, fs *funcSummary) {
	lit, ok := unparen(expr).(*ast.CompositeLit)
	if !ok {
		return
	}
	stt, ok := derefStruct(pf.pc.info.Types[lit].Type)
	if !ok {
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
			fs.fields[field.Name()] = fieldTaint{tainted: true, srcs: maxSrcOf(pv)}
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
// aliases of them). Scalars, structs (their fields are checked
// separately), interfaces, and pointer-to-slice forms cannot carry page
// bytes by value, so param-sourced taint is never minted for them.
func typeCanCarryPage(t types.Type) bool {
	switch u := t.(type) {
	case *types.Slice:
		return isByteElem(u.Elem())
	case *types.Array:
		return isByteElem(u.Elem())
	case *types.Named:
		return typeCanCarryPage(u.Underlying())
	case *types.Alias:
		return typeCanCarryPage(types.Unalias(u))
	}
	return false
}

func isByteElem(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Kind() == types.Uint8
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
