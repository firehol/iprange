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
	pc          *packageCheck
	path        string
	summaries   map[string]*funcSummary // current package
	store       *summaryStore
	values      map[ast.Expr]pageValue
	callFields  map[*ast.CallExpr]map[string]pageValue // struct-result fields of the last evaluated calls
	callResults map[*ast.CallExpr][]pageValue          // per-slot results of the last evaluated module calls
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
	// localFuncs records the current func-literal binding of local
	// function-typed variables, so a call through a locally declared
	// closure (id := func(p []byte) []byte { return p }) resolves the
	// literal body instead of dropping the call's result taint.
	localFuncs map[types.Object]*ast.FuncLit
}

func newStmtState(pf *pageFlow, fd *ast.FuncDecl, pkgVars map[string]pageValue) *stmtState {
	st := &stmtState{
		pf:         pf,
		fd:         fd,
		params:     map[types.Object]int{},
		stmtVars:   map[types.Object]pageValue{},
		structs:    map[types.Object]map[string]pageValue{},
		pkgVars:    pkgVars,
		localFuncs: map[types.Object]*ast.FuncLit{},
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
				switch r := unparen(rhs).(type) {
				case *ast.CallExpr:
					fields = pf.callFields[r]
				case *ast.CompositeLit:
					fields = pf.compositeFields(st, r)
				}
				var lit *ast.FuncLit
				if l, ok := unparen(rhs).(*ast.FuncLit); ok {
					lit = l
				}
				pf.bindLocalFunc(st, v.Lhs[i], lit)
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
								obj := pf.pc.info.ObjectOf(name)
								var lit *ast.FuncLit
								if l, ok := unparen(vs.Values[i]).(*ast.FuncLit); ok {
									lit = l
								}
								pf.bindLocalFunc(st, name, lit)
								if pv.tainted {
									st.stmtVars[obj] = pv
								} else {
									delete(st.stmtVars, obj)
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
			if v.X != nil {
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
		return
	}
	if lit != nil {
		st.localFuncs[obj] = lit
	} else {
		delete(st.localFuncs, obj)
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
	case *ast.IndexExpr:
		// An element store (slots[0] = page, m[k] = page) makes the
		// container itself page-carrying; element reads derive the taint.
		assignTarget(st, v.X, pv)
	case *ast.IndexListExpr:
		assignTarget(st, v.X, pv)
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
	return &cp
}

// joinWith merges another possible state into this one: every variable or
// struct field set in other becomes a possible value here.
func (st *stmtState) joinWith(other *stmtState) {
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
				cur[fk] = fv
			}
		}
	}
	for k, v := range other.localFuncs {
		st.localFuncs[k] = v
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
			} else if idx, ok := st.params[obj]; ok && paramCanCarryPage(paramFieldType(obj, v.Sel.Name)) {
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
// base (element extraction, type assertion): the concrete bound is lost,
// but a parameter-sourced base keeps its source so summaries of
// param-derived results stay caller-dependent instead of reporting
// "always tainted".
func derivedPageValue(base pageValue) pageValue {
	out := pageValue{tainted: true, maxLen: maxUnknown}
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
		maxLen := int64(0)
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
			if pv := pf.evalExpr(st, val); pv.tainted {
				tainted = true
				if pv.maxLen > maxLen {
					maxLen = pv.maxLen
				}
			}
		}
		if tainted {
			return pageValue{tainted: true, maxLen: maxLen}
		}
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
		return pf.analyzeFuncLitCall(st, lit, call)
	}
	obj := calleeObject(pf.pc, call)
	// Calls through a function-typed variable: resolve the variable to
	// the concrete callable it provably binds — a func literal (local or
	// package-level initializer chain, analyzed here with the call-site
	// arguments bound so sinks and result taints are visible) or a plain
	// function reached through the chain (evaluated through its summary).
	// Variables without a provable binding have no scanable body.
	if v, ok := obj.(*types.Var); ok {
		if lit, fn := pf.calleeTarget(st, v, 0); lit != nil {
			return pf.analyzeFuncLitCall(st, lit, call)
		} else if fn != nil {
			obj = fn
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
			pf.evalExpr(st, a)
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

// calleeTarget resolves the concrete callable a function-typed variable
// provably binds: a local variable currently bound to a func literal, or
// a package-level initializer chain ending in a func literal or a plain
// function. Reassigned variables have no provable binding. Locals bound
// to non-literals (parameters, stdlib functions) stay unresolved.
func (pf *pageFlow) calleeTarget(st *stmtState, v *types.Var, depth int) (*ast.FuncLit, *types.Func) {
	if v == nil || depth > 2 || pf.pc.reassignedVars[v] {
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
	fs := &funcSummary{}
	pf.analyzeStmts(st, lit.Body.List, fs)
}

// analyzeFuncLitCall binds a closure's parameters to the call-site
// argument taints, analyzes the body, and returns the closure's result
// taints as the call result. Every result slot is recorded so a
// multi-result closure assignment distributes taint per slot.
func (pf *pageFlow) analyzeFuncLitCall(st *stmtState, lit *ast.FuncLit, call *ast.CallExpr) pageValue {
	args := call.Args
	fs := &funcSummary{}
	if lit.Type.Results != nil {
		for range lit.Type.Results.List {
			fs.results = append(fs.results, fieldTaint{})
		}
	}
	var bound []pageValue
	if lit.Type.Params != nil {
		idx := 0
		for _, f := range lit.Type.Params.List {
			for _, name := range f.Names {
				obj := pf.pc.info.ObjectOf(name)
				if idx < len(args) {
					pv := pf.evalExpr(st, args[idx])
					bound = append(bound, pv)
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
	res := pf.evalLitResults(fs, bound, lit.Type)
	if len(res) > 0 {
		pf.callResults[call] = res
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
			if _, exists := fs.fields[field.Name()]; !exists {
				fs.fields[field.Name()] = fieldTaint{tainted: true, srcs: maxSrcOf(pv)}
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
	switch u := t.(type) {
	case *types.Slice:
		return isByteElem(u.Elem()) || typeCanCarryPage(u.Elem())
	case *types.Array:
		return isByteElem(u.Elem()) || typeCanCarryPage(u.Elem())
	case *types.Pointer:
		return typeCanCarryPage(u.Elem())
	case *types.TypeParam:
		return true // a ~[]byte instantiation is possible
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
	if typeCanCarryPage(t) {
		return true
	}
	return isInterfaceType(t)
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
