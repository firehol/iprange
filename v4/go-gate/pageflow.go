package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"sort"
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
	// mapped records that the value provably aliases the mapping (a
	// Store callback's page parameter, or any slice/record derived from
	// it). Copies INTO a mapped destination never create an owned
	// complete page, so the byte-ownership rules treat mapped
	// destinations as benign. Conditional (parameter-sourced) values
	// keep mapped=false; their destinations are resolved by the
	// copy-parameter call-site rule instead.
	mapped bool
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
	mapped   bool   // the source value also aliases the mapping
}

type fieldTaint struct {
	tainted bool
	srcs    []maxSrc
	mapped  bool // tainted AND provably aliasing the mapping
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
	// pageSinkParams records parameter indexes whose elements are
	// written inside the callee (dst[i] = b). A call site inside a
	// page-sourcing loop that passes an owned buffer to such a
	// parameter is an element-wise complete-page copy.
	pageSinkParams map[int]bool
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
	// copyParams records copy(paramD[..], paramS[..]) pairs INSIDE the
	// body: destination parameter -> source parameter slots. A call
	// site that binds an OWNED destination and a MAPPED source to such
	// a pair creates the complete-page copy the definition site cannot
	// see (both sides are caller-dependent), so the call-site rule
	// fails that binding. The pairs compose through call chains
	// (F's argument is F's own parameter), so the fence survives
	// arbitrary helper depth.
	copyParams map[int][]int
	// callbackInvokes records byte-slice parameters the body passes to a
	// func-typed formal parameter it invokes: func-typed formal slot ->
	// callee parameter slots the byte views come from. Whether those
	// views are mapped is decided by the call sites that bind the func
	// formal (the store callback contract), so the store-callback fence
	// at the store-implementation call site uses this record exactly
	// like copyParams; the record composes through call chains.
	callbackInvokes map[int][]int
	// callbackInvokesInternal marks func-typed formal slots the body
	// invokes with byte arguments the definition site cannot trace to a
	// parameter (a field read, a call, a literal): the views are not
	// provably the caller's mapped views, so a store implementation
	// forwarding its callback formal into such a slot fails closed at
	// the call site.
	callbackInvokesInternal map[int]bool
	// callbackAliases records local function-typed variables that
	// alias a func-typed formal parameter of the function, keyed by the
	// local object: cb := fn, cb := func(a, b []byte) error { return
	// fn(a, b) }, and chains of both. A store implementation can hide
	// its callback formal behind such a local and hand the local to a
	// helper; the store-callback fence at the call site follows the
	// alias so the helper's byte arguments are still required to be
	// mapped views.
	callbackAliases map[types.Object]callbackAlias
	// rangeVars records func-typed range variables bound from a
	// container expression (for _, cb := range cbs): the loop value is
	// one of the container's elements, so an invocation through it
	// (cb(v)) is an element invocation of the container. Callee
	// resolution follows the container through the normal slot paths
	// (formal, alias, field, indexed composite), so the store-callback
	// fence at the call sites that bind the container polices the byte
	// views the element invocations receive.
	rangeVars map[types.Object]ast.Expr
	// fieldAliases records struct-field storage slots the body (or a
	// caller passing a carrier struct) associates with a func-typed
	// formal parameter (h.f = fn, o := outer{in: car{cb: fn}}): keyed
	// by the canonical struct type and field name, holding the
	// formal's parameter slot; path names the carrier steps from the
	// root down to the leaf's host struct, so the SAME key can hold a
	// flat record (h := car{fn}) and a nested one
	// (o := outer{in: car{fn}}) side by side and each binding keeps
	// its own enforcement. forwarded marks records received from a
	// caller (the function's own parameter carries the formal, so
	// local callee resolution must not treat it as a direct slot). The
	// store-callback fence uses the record both here (a later h.f(...)
	// invocation inside the same function carries the formal slot) and
	// in the rules pass (a store implementation invoking the callback
	// through a field must hand it mapped views).
	fieldAliases map[fieldSlotKey][]fieldSlotAlias
	// fieldInvokes records struct-field callees the body invokes with
	// byte-slice arguments but cannot resolve to a func-typed formal of
	// its own (h.f(a, b) with h a struct whose field f is bound to the
	// callback formal by a caller): keyed by the canonical struct type
	// and field name, holding the callee's parameter slots the byte
	// views come from. The store-callback fence at the helper call
	// sites composes the record against the caller's fieldAliases, so a
	// store implementation forwarding its callback formal through a
	// struct carrier is enforced exactly like a func-typed forward. The
	// record re-records through call chains (recordFieldAliasComposition)
	// so multi-helper carriers stay visible.
	fieldInvokes map[fieldSlotKey][]int
	// fieldInvokesInternal marks struct-field callees the body invokes
	// with byte arguments it cannot trace to a parameter (a field read,
	// a call, a literal): the views are not provably the caller's mapped
	// views, so a store implementation forwarding its callback formal
	// through the carrier fails closed at the call site.
	fieldInvokesInternal map[fieldSlotKey]bool
	// paramFieldInvokes records parameter indexes whose byte views
	// reach a struct-field callee through a callee chain the current
	// function forwards (launch(p) -> h(c, p) -> c.cb(p, p)): the
	// parameter's mappedness is decided at the call sites of THIS
	// function, so a non-store call site binding a concrete mapped view
	// to such a parameter fails closed (the chain has no direct
	// carrier record to enforce at), while parametrized chains cascade
	// the record to their own callers. Keyed by the summary layout
	// (receiver at slot 0).
	paramFieldInvokes map[int]map[fieldSlotKey]bool
	// paramAliases records locals the body binds to a func-typed formal
	// slot (f := fn, g := s.cb.(T)): keyed by the local object, holding
	// the formal's parameter slot. Internal to the function analysis;
	// the composition and the callback-invocation walk resolve callees
	// through it like the alias and holder records.
	paramAliases map[types.Object]int
	// indexAliases records indexed container slots the body binds to a
	// func-typed formal slot (arr[0] = fn, m["cb"] = fn, hs[0] = fn,
	// hs := []func{fn}): keyed by the container slot, holding the
	// formal's parameter slot. The store-callback fence follows indexed
	// callees through the record exactly like field holders.
	indexAliases map[indexSlotKey]int
	// returnAliases records local func literals whose body returns one
	// of their own func-typed parameters unchanged
	// (id := func(f F) F { return f }): keyed by the local object,
	// holding the returned parameter position, or -2 when different
	// branches return different parameters. A call id(x)(args...) is
	// then the callback bound to x, so the store-callback fence
	// counter-checks its byte arguments.
	returnAliases map[types.Object]int
	// returnFieldKeys records result slots whose value is a func-typed
	// field read of the function's own receiver or parameters
	// (getCB returning s.cb): a caller invoking the result resolves the
	// key against its own fieldAliases exactly like a direct field
	// invocation. multiReturnKey fails closed.
	returnFieldKeys map[int]fieldSlotKey
	// returnSlotAliases records result slots the body returns unchanged
	// from one of its own func-typed parameter slots (a scanned
	// identity helper): the caller's argument at that position decides
	// the mappedness. -2 marks different branches returning different
	// parameters (fails closed).
	returnSlotAliases map[int]int
	// returnCarrierFields records result slots holding a struct carrier
	// built in the body with a formal-bound field (mkCar(fn) returning
	// car{cb: fn}): the caller records its own fieldAlias for the
	// returned field key, sourced from the value the callee bound.
	returnCarrierFields map[int]returnCarrierField
}

// callbackAlias records one local that aliases a func-typed formal
// parameter of the enclosing function. slot is the formal's parameter
// slot. forwarded marks the closure parameter positions the body passes
// to the formal (a nil slice means an identity alias cb := fn, where
// every position forwards unchanged); lit is the wrapping func literal,
// or nil for a plain identity alias; litParams are the literal's
// parameter objects, used to defer the literal-body invocation records
// to the call sites that bind the literal.
// indexSlotKey identifies one indexed container slot (arr[0], m["cb"],
// s.hs[0]) by its root object, the selector path from the root to the
// container, and the constant index text. An empty index means "any
// index on this container" (a non-constant index write fails closed).
// The key is shared by the flow pass (assignments, callee resolution)
// and the rules pass (store-callback counter-check).
type indexSlotKey struct {
	root  types.Object
	path  string
	index string
}

// fieldSlotKey identifies a struct-field storage slot by the canonical
// structural key of the field's enclosing struct type and the field
// name. The key is shared by the flow pass (assignments, callee
// resolution, cross-function composition) and the rules pass
// (store-callback counter-check). Keying by the canonical type instead
// of the field objects lets a helper parameter declared as a named
// struct match a caller's anonymous struct value with the same fields,
// which is how a callback formal travels across functions inside a
// struct carrier (var h car; h.cb = fn; runCar(h, buf, buf)).
type fieldSlotKey struct {
	typ   string
	field string
}

// fieldSlotAlias records that a struct field holds the store callback
// formal. slot is the func-typed formal slot in the recording function.
// forwarded marks a record received from a caller at the given
// parameter slot (a carrier this function forwards, not a field it
// assigned): local callee resolution must not treat a forwarded record
// as this function's own formal slot, but composition and the call-site
// fence follow it through chains.
type fieldSlotAlias struct {
	slot      int
	forwarded bool
	// path names the struct steps from the carrier root down to the
	// leaf field's host struct, outer-to-inner, each step as
	// "<canonical host type>.<field name>" joined by '/'. For
	// o := outer{in: car{cb: fn}} the leaf key is {car,"cb"} and the
	// path is "<outer>.in". Empty for a flat carrier. The path lets
	// the cross-function composition match a callee parameter whose
	// type is the OUTER carrier while the callback field sits deeper
	// inside it (nested carrier launching).
	path string
}

// returnCarrierField records that a function result carries a struct
// carrier whose field holds the callback formal (mkCar(fn) returning
// car{cb: fn}): field is the returned carrier's field key, param the
// callee parameter slot the callback value came from (-1 when the
// value is a field read of the callee's own receiver or parameter,
// named by srcKey), and -2 when different branches disagree (nothing
// can be attributed to the caller, so the chain stays fail-closed at
// the store call sites).
type returnCarrierField struct {
	field  fieldSlotKey
	param  int
	srcKey fieldSlotKey
	path   string
	// srcRead marks a leaf read from a field of the callee's receiver.
	srcRead bool
}

// multiReturnKey marks a return slot that carries different field keys
// across branches: the caller cannot know which key binds, so the
// result fails closed.
var multiReturnKey = fieldSlotKey{typ: "*multi*", field: ""}

// isConversionCallExpr reports whether e is a value conversion
// (any(fn), (cbSig)(fn)) rather than a function call: conversions are
// identities on the bound value for callback-slot resolution.
func isConversionCallExpr(info *types.Info, e *ast.CallExpr) bool {
	if len(e.Args) != 1 {
		return false
	}
	tv, ok := info.Types[unparen(e.Fun)]
	return ok && tv.IsType()
}

// constIndexKey returns the canonical constant text of an index
// expression (0, "cb"), or ("", false) when the index is not a
// constant. Non-constant indices match the catch-all empty key.
func constIndexKey(info *types.Info, idx ast.Expr) (string, bool) {
	tv, ok := info.Types[idx]
	if !ok || tv.Value == nil {
		return "", false
	}
	return tv.Value.ExactString(), true
}

// isStructType reports whether t is a struct type (after unaliasing and
// unwrapping named types).
func isStructType(t types.Type) bool {
	t = types.Unalias(t)
	if n, ok := t.(*types.Named); ok {
		t = n.Underlying()
	}
	_, ok := t.(*types.Struct)
	return ok
}

// canonFieldType returns the canonical structural key of a struct
// type: the first registered types.Identical type wins, so a named
// struct and an anonymous struct with the same fields share one key.
// Pointer wrappers are stripped (field selection on *T and T name the
// same storage). Non-struct types return "". The registry lives on the
// pageFlow because field records must match across the whole scan.
func (pf *pageFlow) canonFieldType(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if t == nil || !isStructType(t) {
		return ""
	}
	if k, ok := pf.canonTypeIdx[t]; ok {
		return k
	}
	for e, k := range pf.canonTypeIdx {
		if types.Identical(e, t) {
			if pf.canonTypeIdx == nil {
				pf.canonTypeIdx = map[types.Type]string{}
			}
			pf.canonTypeIdx[t] = k
			return k
		}
	}
	k := fmt.Sprintf("t%d", len(pf.canonTypes))
	pf.canonTypes = append(pf.canonTypes, t)
	if pf.canonTypeIdx == nil {
		pf.canonTypeIdx = map[types.Type]string{}
	}
	pf.canonTypeIdx[t] = k
	return k
}

// fieldSlotKeyOf returns the canonical storage key of a plain struct
// field selection (h.cb): the canonical key of the selection's
// receiver struct type and the field name. Method values, interface
// dispatch, and non-struct receivers return false.
func (pf *pageFlow) fieldSlotKeyOf(info *types.Info, sel *ast.SelectorExpr) (fieldSlotKey, bool) {
	if sel == nil {
		return fieldSlotKey{}, false
	}
	s, isSel := info.Selections[sel]
	if !isSel || s.Kind() != types.FieldVal {
		return fieldSlotKey{}, false
	}
	k := pf.canonFieldType(s.Recv())
	if k == "" {
		return fieldSlotKey{}, false
	}
	return fieldSlotKey{typ: k, field: sel.Sel.Name}, true
}

// fieldCalleeKey resolves a func-callee expression to the canonical
// struct-field storage key it reads: a plain field selection (h.cb) or
// a type assertion chain over one (h.cb.(func([]byte, []byte) error)).
// It is the callee-resolution counterpart of fieldSlotKeyOf used when
// the local slotOfExpr cannot resolve the callee: the invocation may
// still name a struct field the CALLER bound to the callback formal.
func (pf *pageFlow) fieldCalleeKey(info *types.Info, e ast.Expr) (fieldSlotKey, bool) {
	src := unparen(e)
	for {
		if ta, isTa := src.(*ast.TypeAssertExpr); isTa {
			src = unparen(ta.X)
			continue
		}
		break
	}
	sel, ok := src.(*ast.SelectorExpr)
	if !ok {
		return fieldSlotKey{}, false
	}
	return pf.fieldSlotKeyOf(info, sel)
}

// snapshotEvalExpr evaluates e like evalExpr but restores the
// per-expression cache afterwards. The callback-invocation walk reads
// END-STATE values (the body has already been analyzed), so an
// evaluation there must not overwrite the call-time value the rule pass
// will read for the same node: a trailing mint after an invocation must
// not bless an owned buffer that already reached the callback.
func (pf *pageFlow) snapshotEvalExpr(st *stmtState, e ast.Expr) pageValue {
	prev, had := pf.values[e]
	out := pf.evalExpr(st, e)
	if had {
		pf.values[e] = prev
	} else {
		delete(pf.values, e)
	}
	return out
}

// indexSlotKeyOf builds the slot key of an indexed LHS or callee
// expression: arr[0], m["cb"], s.hs[0], xs[i, j]. The key is the root
// object, the selector path from the root to the indexed container, and
// the constant index text ("" when any index is non-constant, which
// matches the catch-all key recorded for non-constant writes).
func indexSlotKeyOf(info *types.Info, e ast.Expr) (indexSlotKey, bool) {
	var base ast.Expr
	var indices []ast.Expr
	switch t := unparen(e).(type) {
	case *ast.IndexExpr:
		base, indices = t.X, []ast.Expr{t.Index}
	case *ast.IndexListExpr:
		base, indices = t.X, t.Indices
	default:
		return indexSlotKey{}, false
	}
	var names []string
	for {
		b := unparen(base)
		if sel, isSel := b.(*ast.SelectorExpr); isSel {
			names = append([]string{sel.Sel.Name}, names...)
			base = sel.X
			continue
		}
		break
	}
	id, ok := unparen(base).(*ast.Ident)
	if !ok {
		return indexSlotKey{}, false
	}
	root := info.ObjectOf(id)
	if root == nil {
		return indexSlotKey{}, false
	}
	key := indexSlotKey{root: root, path: strings.Join(names, ".")}
	for _, ix := range indices {
		if s, c := constIndexKey(info, ix); c {
			if key.index != "" {
				key.index += ","
			}
			key.index += s
		} else {
			key.index = ""
			break
		}
	}
	return key, true
}

type callbackAlias struct {
	slot      int
	forwarded []bool
	lit       *ast.FuncLit
	litParams []types.Object
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
	mappedOf := func(s maxSrc) bool {
		switch s.kind {
		case "param":
			return s.param >= 0 && s.param < len(args) && args[s.param].mapped
		case "paramField":
			if s.param >= 0 && s.param < len(argFlows) {
				if fv, ok := argFlows[s.param].fields[s.field]; ok {
					return fv.mapped
				}
			}
			return false
		}
		return s.mapped
	}
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
			if mappedOf(s) {
				out.mapped = true
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
			if s.kind == "param" && s.param >= 0 && s.param < len(args) && args[s.param].mapped {
				pv.mapped = true
			} else if s.kind == "paramField" && s.param >= 0 && s.param < len(argFlows) {
				if fv, ok := argFlows[s.param].fields[s.field]; ok && fv.mapped {
					pv.mapped = true
				}
			} else if s.mapped {
				pv.mapped = true
			}
		}
		out[i] = pv
	}
	return out
}

// summaryStore holds the computed summaries of every module package.
type summaryStore struct {
	pkgs map[string]map[string]*funcSummary // import path -> summaries
	// storeCbSlots marks, per function key, the parameter slots that
	// provably receive the store callback formal: a store
	// implementation binds its own func-typed callback formal into the
	// slot, directly or through a chain of forwarding helpers
	// (computeStoreCbSlots). Only func-container formals in marked
	// slots are admitted as scanned callback containers at their
	// definition site (their element calls are policed by the store
	// fence at the binding call sites); every other func-container
	// formal keeps the unproven-indirection fail-closed rule, because
	// nothing guarantees its call sites bind scanned callbacks.
	storeCbSlots map[string]map[int]bool
}

func newSummaryStore() *summaryStore {
	return &summaryStore{pkgs: map[string]map[string]*funcSummary{}}
}

// computeStoreCbSlots computes the module-wide set of function
// parameter slots that receive the store callback formal, as a fixpoint
// over the call graph: a store implementation marks its own func-typed
// callback formals, and a function that passes a marked slot (directly,
// through a recorded alias, carrier field, indexed holder, container
// element, or func-container composite literal) into another function's
// func-typed or func-container slot marks that callee slot. The result
// gates the scanned-callback element admission: a func-container
// formal is only treated as a scanned callback container when a store
// implementation provably binds the callback into it, so its element
// invocations are policed by the store fence at the binding call sites.
// Any other func-container formal keeps the unproven-indirection
// fail-closed rule. The computation runs once after all package
// summaries stabilize; the rules pass reads the finished result.
func (store *summaryStore) computeStoreCbSlots(checks map[string]*packageCheck) {
	type fnInfo struct {
		path string
		key  string
		fd   *ast.FuncDecl
		pc   *packageCheck
	}
	var fns []fnInfo
	paths := make([]string, 0, len(checks))
	for p := range checks {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		pc := checks[path]
		sums := store.pkgs[path]
		if sums == nil {
			continue
		}
		for _, f := range pc.files {
			for _, decl := range f.ast.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				key := funcKey(fd)
				if _, ok := sums[key]; !ok {
					continue
				}
				fns = append(fns, fnInfo{path: path, key: key, fd: fd, pc: pc})
			}
		}
	}
	marked := map[string]map[int]bool{}
	isStoreImpl := func(fi fnInfo) bool {
		fd := fi.fd
		if fd.Recv == nil || len(fd.Recv.List) == 0 || !storeCallbackMethod(fd.Name.Name) {
			return false
		}
		rt := fi.pc.info.TypeOf(fd.Recv.List[0].Type)
		if rt == nil {
			return false
		}
		for _, iface := range fi.pc.approvedStoreInterfaces() {
			if types.Implements(rt, iface) {
				return true
			}
		}
		return false
	}
	// A store implementation's own func-typed formal parameters are the
	// callback slots the store contract blesses.
	initSlots := func(fi fnInfo) map[int]bool {
		m := map[int]bool{}
		if !isStoreImpl(fi) {
			return m
		}
		fd := fi.fd
		idx := 0
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			idx = 1
		}
		if fd.Type.Params != nil {
			for _, f := range fd.Type.Params.List {
				for _, name := range f.Names {
					if obj := fi.pc.info.ObjectOf(name); obj != nil && funcSignature(obj.Type()) != nil {
						m[idx] = true
					}
					idx++
				}
			}
		}
		return m
	}
	for _, fi := range fns {
		if m := initSlots(fi); len(m) > 0 {
			marked[fi.key] = m
		}
	}
	funcLikeSlot := func(t types.Type) bool {
		return funcSignature(t) != nil || elemFuncType(t)
	}
	// Fixpoint: a function holding a marked slot forwards the callback
	// when its body passes that parameter (or a recorded alias, carrier
	// field, indexed holder, or container element of it) into another
	// module function's func-typed or func-container slot.
	for changed := true; changed; {
		changed = false
		for _, fi := range fns {
			own := marked[fi.key]
			if len(own) == 0 {
				continue
			}
			pf := &pageFlow{pc: fi.pc, path: fi.path, store: store, summaries: store.pkgs[fi.path]}
			st := newStmtState(pf, fi.fd, nil, nil)
			fs := store.pkgs[fi.path][fi.key]
			ast.Inspect(fi.fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if pf.calleeSummaryOfCall(st, call) == nil {
					return true
				}
				fn, ok := pf.calleeExprFunc(st, call.Fun)
				if !ok {
					return true
				}
				sig, ok := fn.Type().(*types.Signature)
				if !ok {
					return true
				}
				recvExpr := ast.Expr(nil)
				if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
					if selRecv, isSel := pf.pc.info.Selections[sel]; isSel && selRecv.Kind() == types.MethodVal {
						recvExpr = sel.X
					}
				}
				argOff := 0
				if recvExpr != nil {
					argOff = 1
				}
				argAt := func(slot int) ast.Expr {
					if recvExpr != nil && slot == 0 {
						return recvExpr
					}
					ai := slot - argOff
					if ai < 0 || ai >= len(call.Args) {
						return nil
					}
					return call.Args[ai]
				}
				nSlots := sig.Params().Len()
				if sig.Recv() != nil {
					nSlots++
				}
				ckey := fn.Name()
				if sig.Recv() != nil {
					ckey = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
				}
				for slot := 0; slot < nSlots; slot++ {
					var pt *types.Var
					if sig.Recv() != nil {
						if slot == 0 {
							continue
						}
						pt = sig.Params().At(slot - 1)
					} else {
						pt = sig.Params().At(slot)
					}
					if pt == nil || !funcLikeSlot(pt.Type()) {
						continue
					}
					arg := argAt(slot)
					if arg == nil {
						continue
					}
					idx, ok := pf.slotOfExpr(st, fs, arg)
					if !ok || !own[idx] {
						continue
					}
					cm := marked[ckey]
					if cm == nil {
						cm = map[int]bool{}
						marked[ckey] = cm
					}
					if !cm[slot] {
						cm[slot] = true
						changed = true
					}
				}
				return true
			})
		}
	}
	store.storeCbSlots = marked
}

// pageFlow is the interpreter for one package's rules pass.
type pageFlow struct {
	pc         *packageCheck
	path       string
	summaries  map[string]*funcSummary // current package
	store      *summaryStore
	mappingT   *types.Named // resolved mapping owner, cached
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
	// fieldPromoted records expressions whose whole-value page taint was
	// synthesized by promoteFullPageFields from a struct-field taint
	// (b.Data = page; cb(b)): the value itself is clean, only a field
	// carries the mapped page. Opaque-call and interface-dispatch rules
	// use the marker to fail calls that hand a field-hidden complete page
	// to a callee body the call site cannot see, while whole-value page
	// arguments (a callback receiving the mapped page itself by contract)
	// stay benign for approved callees.
	fieldPromoted map[ast.Expr]bool
	// callMethodValues records calls resolved through a method VALUE
	// stored in a local or package variable (get := b.String; get()):
	// the resolved method and its receiver expression. The rule pass
	// uses them for call-site checks (string-param conversions on the
	// receiver) that a bare callee lookup cannot see.
	callMethodValues map[*ast.CallExpr]methodValueCall
	// pageSinkCalls records calls inside a page-sourcing loop whose
	// callee writes element-wise into one of its parameters, together
	// with the caller's argument expression for that parameter. The
	// rule pass fails the call site: the helper's per-element writes
	// aggregate to a complete page copy in the caller's buffer.
	pageSinkCalls map[*ast.CallExpr][]ast.Expr
	// destAggregated records element-write expressions made only inside a
	// destination-ranging loop; the rule pass fails them when the RHS
	// derives from a page. pageSourceLoops writes always fail.
	destAggregated map[ast.Expr]bool
	// boundedPageSpans accumulates bounded mapped-page spans copied or
	// appended into one canonical destination (root object + field path).
	boundedPageSpans map[boundedSpanKey]int
	// spanAliases records canonical destination-key aliases when one
	// selector field is assigned from another (h.right = h.left).
	spanAliases map[boundedSpanKey]boundedSpanKey
	// appendAliases maps a rebound slice name to the canonical variable
	// whose bounded page-span accumulation it shares.
	appendAliases map[types.Object]types.Object
	// appendCallRoots records each append call's destination canonical
	// root at the statement's position in control flow, before a later
	// rebind changes the variable's alias state.
	appendCallRoots map[*ast.CallExpr]types.Object
	// canonTypes is the registry of struct types seen while building
	// fieldSlotKey values, with canonTypeIdx their structural keys: two
	// types that are types.Identical (a named struct and an anonymous
	// struct with the same fields) share one key, so field records made
	// in different functions and through different declarations match.
	canonTypes   []types.Type
	canonTypeIdx map[types.Type]string
	accum        bool // final sweep: keep expression caches for the rule pass
}

// methodValueCall is one resolved method-value call: the method and the
// receiver expression bound at the binding site.
// canonicalAppendRoot resolves the current canonical root of an append
// destination, following bounded-span aliases until absence or a fresh
// rebind marker.
func (pf *pageFlow) canonicalAppendRoot(obj types.Object) types.Object {
	for d := 0; d < 8; d++ {
		next, ok := pf.appendAliases[obj]
		if !ok || next == nil {
			return obj
		}
		if next == obj {
			return obj
		}
		obj = next
	}
	return obj
}

// boundedSpanKey canonicalizes one bounded page-span destination.
type boundedSpanKey struct {
	obj  types.Object
	path string
}

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
	pf.pageSinkCalls = map[*ast.CallExpr][]ast.Expr{}
	pf.destAggregated = map[ast.Expr]bool{}
	pf.boundedPageSpans = map[boundedSpanKey]int{}
	pf.spanAliases = map[boundedSpanKey]boundedSpanKey{}
	pf.appendAliases = map[types.Object]types.Object{}
	pf.appendCallRoots = map[*ast.CallExpr]types.Object{}
}

// summarizePackage computes the symbolic summaries of one package,
// iterating to a fixpoint so intra-package helper chains compose.
func summarizePackage(pc *packageCheck, path string, store *summaryStore, files []*parsedFile, pf *pageFlow) (map[string]*funcSummary, *pageFlow) {
	// Analysis and rule passes must visit files in one stable order, or
	// per-package accumulation state (bounded page-copy spans) resets on
	// nondeterministic map iteration.
	sortedFiles := append([]*parsedFile{}, files...)
	sort.Slice(sortedFiles, func(i, j int) bool { return sortedFiles[i].name < sortedFiles[j].name })
	files = sortedFiles
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
	if len(fs.pageSinkParams) > 0 {
		out.pageSinkParams = map[int]bool{}
		for k := range fs.pageSinkParams {
			out.pageSinkParams[k] = true
		}
	}
	if len(fs.copyParams) > 0 {
		out.copyParams = map[int][]int{}
		for k, vs := range fs.copyParams {
			out.copyParams[k] = append([]int{}, vs...)
		}
	}
	if len(fs.callbackInvokes) > 0 {
		out.callbackInvokes = map[int][]int{}
		for k, vs := range fs.callbackInvokes {
			out.callbackInvokes[k] = append([]int{}, vs...)
		}
	}
	if len(fs.callbackInvokesInternal) > 0 {
		out.callbackInvokesInternal = map[int]bool{}
		for k := range fs.callbackInvokesInternal {
			out.callbackInvokesInternal[k] = true
		}
	}
	if len(fs.callbackAliases) > 0 {
		out.callbackAliases = map[types.Object]callbackAlias{}
		for k, v := range fs.callbackAliases {
			// forwarded/litParams are immutable after recording; sharing
			// them across copies is safe.
			out.callbackAliases[k] = v
		}
	}
	if len(fs.fieldAliases) > 0 {
		out.fieldAliases = map[fieldSlotKey][]fieldSlotAlias{}
		for k, vs := range fs.fieldAliases {
			out.fieldAliases[k] = append([]fieldSlotAlias(nil), vs...)
		}
	}
	if len(fs.fieldInvokes) > 0 {
		out.fieldInvokes = map[fieldSlotKey][]int{}
		for k, vs := range fs.fieldInvokes {
			out.fieldInvokes[k] = append([]int{}, vs...)
		}
	}
	if len(fs.fieldInvokesInternal) > 0 {
		out.fieldInvokesInternal = map[fieldSlotKey]bool{}
		for k := range fs.fieldInvokesInternal {
			out.fieldInvokesInternal[k] = true
		}
	}
	if len(fs.paramFieldInvokes) > 0 {
		out.paramFieldInvokes = map[int]map[fieldSlotKey]bool{}
		for p, keys := range fs.paramFieldInvokes {
			out.paramFieldInvokes[p] = map[fieldSlotKey]bool{}
			for k := range keys {
				out.paramFieldInvokes[p][k] = true
			}
		}
	}
	if len(fs.indexAliases) > 0 {
		out.indexAliases = map[indexSlotKey]int{}
		for k, v := range fs.indexAliases {
			out.indexAliases[k] = v
		}
	}
	if len(fs.returnAliases) > 0 {
		out.returnAliases = map[types.Object]int{}
		for k, v := range fs.returnAliases {
			out.returnAliases[k] = v
		}
	}
	if len(fs.returnFieldKeys) > 0 {
		out.returnFieldKeys = map[int]fieldSlotKey{}
		for k, v := range fs.returnFieldKeys {
			out.returnFieldKeys[k] = v
		}
	}
	if len(fs.returnSlotAliases) > 0 {
		out.returnSlotAliases = map[int]int{}
		for k, v := range fs.returnSlotAliases {
			out.returnSlotAliases[k] = v
		}
	}

	if len(fs.returnCarrierFields) > 0 {
		out.returnCarrierFields = map[int]returnCarrierField{}
		for k, v := range fs.returnCarrierFields {
			out.returnCarrierFields[k] = v
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
	if !stringParamsEqual(a.pageSinkParams, b.pageSinkParams) {
		return false
	}
	if !copyParamsEqual(a.copyParams, b.copyParams) {
		return false
	}
	if !callbackRecordsEqual(a, b) {
		return false
	}

	return true
}

// callbackRecordsEqual compares the four callback-record maps of two
// summaries. The records are built during the body walk
// (recordCallbackInvokeComposition) from callee summaries that stabilize
// across fixpoint passes, so the fixpoint must keep iterating until the
// records settle; without the comparison, a chain whose callee records
// arrive one pass late terminates early and the store-callback fence
// never sees the forwarded invocation.
func callbackRecordsEqual(a, b *funcSummary) bool {
	if len(a.callbackInvokes) != len(b.callbackInvokes) {
		return false
	}
	for k, vs := range a.callbackInvokes {
		bv, ok := b.callbackInvokes[k]
		if !ok || len(vs) != len(bv) {
			return false
		}
		for i := range vs {
			if vs[i] != bv[i] {
				return false
			}
		}
	}
	if len(a.callbackInvokesInternal) != len(b.callbackInvokesInternal) {
		return false
	}
	for k := range a.callbackInvokesInternal {
		if !b.callbackInvokesInternal[k] {
			return false
		}
	}
	if len(a.callbackAliases) != len(b.callbackAliases) {
		return false
	}
	for k, al := range a.callbackAliases {
		bl, ok := b.callbackAliases[k]
		if !ok || al.slot != bl.slot || len(al.forwarded) != len(bl.forwarded) ||
			al.lit != bl.lit || len(al.litParams) != len(bl.litParams) {
			return false
		}
		for i := range al.forwarded {
			if al.forwarded[i] != bl.forwarded[i] {
				return false
			}
		}
		for i := range al.litParams {
			if al.litParams[i] != bl.litParams[i] {
				return false
			}
		}
	}
	if len(a.fieldAliases) != len(b.fieldAliases) {
		return false
	}
	for k, avs := range a.fieldAliases {
		bvs, ok := b.fieldAliases[k]
		if !ok || len(avs) != len(bvs) {
			return false
		}
		for i := range avs {
			if avs[i] != bvs[i] {
				return false
			}
		}
	}
	if len(a.fieldInvokes) != len(b.fieldInvokes) {
		return false
	}
	for k, vs := range a.fieldInvokes {
		bv, ok := b.fieldInvokes[k]
		if !ok || len(vs) != len(bv) {
			return false
		}
		for i := range vs {
			if vs[i] != bv[i] {
				return false
			}
		}
	}
	if len(a.fieldInvokesInternal) != len(b.fieldInvokesInternal) {
		return false
	}
	for k := range a.fieldInvokesInternal {
		if !b.fieldInvokesInternal[k] {
			return false
		}
	}
	if len(a.paramFieldInvokes) != len(b.paramFieldInvokes) {
		return false
	}
	for p, keys := range a.paramFieldInvokes {
		bkeys, ok := b.paramFieldInvokes[p]
		if !ok || len(keys) != len(bkeys) {
			return false
		}
		for k := range keys {
			if !bkeys[k] {
				return false
			}
		}
	}
	if len(a.indexAliases) != len(b.indexAliases) {
		return false
	}
	for k, v := range a.indexAliases {
		if b.indexAliases[k] != v {
			return false
		}
	}
	if len(a.returnAliases) != len(b.returnAliases) {
		return false
	}
	for k, v := range a.returnAliases {
		if b.returnAliases[k] != v {
			return false
		}
	}
	if len(a.returnFieldKeys) != len(b.returnFieldKeys) {
		return false
	}
	for k, v := range a.returnFieldKeys {
		if b.returnFieldKeys[k] != v {
			return false
		}
	}
	if len(a.returnSlotAliases) != len(b.returnSlotAliases) {
		return false
	}
	for k, v := range a.returnSlotAliases {
		if b.returnSlotAliases[k] != v {
			return false
		}
	}

	if len(a.returnCarrierFields) != len(b.returnCarrierFields) {
		return false
	}
	for k, v := range a.returnCarrierFields {
		if b.returnCarrierFields[k] != v {
			return false
		}
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
	if a.tainted != b.tainted || a.mapped != b.mapped || len(a.srcs) != len(b.srcs) {
		return false
	}
	for i := range a.srcs {
		if a.srcs[i] != b.srcs[i] {
			return false
		}
	}
	return true
}

func copyParamsEqual(a, b map[int][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, vs := range a {
		bs, ok := b[k]
		if !ok || len(vs) != len(bs) {
			return false
		}
		for i := range vs {
			if vs[i] != bs[i] {
				return false
			}
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
	pf *pageFlow
	fd *ast.FuncDecl
	// activeFS is the funcSummary of the function currently being
	// analyzed; copy-parameter composition records the current
	// function's own copy pairs when it forwards its parameters into a
	// callee that copies between them.
	activeFS   *funcSummary
	params     map[types.Object]int
	stmtVars   map[types.Object]pageValue
	structs    map[types.Object]map[string]pageValue
	pkgVars    map[string]pageValue
	pkgStructs map[types.Object]map[string]pageValue
	// pageSourceLoops counts the enclosing loops that iterate over or
	// index into a page-tainted slice. Inside such a loop, element-wise
	// writes into an owned buffer of PageSize aggregate to a complete
	// page copy (binary-format-v4.md:108).
	pageSourceLoops int
	// loopBound records the statically known iterations of the innermost
	// bounded loop; nested loops multiply their bounds conservatively.
	loopBound int64
	// destRangeLoops counts loops ranging over an owned destination of
	// PageSize. Element writes in them are copies only when the RHS
	// derives from a page; the flow pass marks the destination and the
	// rule pass checks the RHS.
	destRangeLoops int

	// lenOfPage records variables bound to len(page) where page is a
	// page-tainted slice, so a for-loop condition using the alias
	// (for i := 0; i < n; i++) is recognized as a page-sourcing loop.
	lenOfPage map[types.Object]bool
	// sliceLens records the concrete length of slices made with
	// make([]T, n): a destination range over such a slice is a
	// complete-page copy context when n reaches PageSize.
	sliceLens map[types.Object]int64
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
		loopBound:     1,
		lenOfPage:     map[types.Object]bool{},
		sliceLens:     map[types.Object]int64{},
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

// chainRootObject resolves the binding object of a selector/index chain
// root that may name the base of a TYPE ASSERTION: v.(*H).Items[0] binds
// the asserted base variable v, so element records on a TypeAssertExpr
// root land on v exactly like records on a plain selector root
// (h.Items[0] binds h). The read side resolves the same roots through
// typeAssertBaseOf, so both sides agree on the flattened field paths.
func chainRootObject(st *stmtState, root ast.Expr) types.Object {
	if ta, ok := unparen(root).(*ast.TypeAssertExpr); ok {
		return objOfDeref(st, ta.X)
	}
	return objOfDeref(st, unparen(root))
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
	fs.pageSinkParams = map[int]bool{}
	// Callback-invocation records are value-dependent (the untraceable
	// mark depends on the mappedness of the body's byte expressions),
	// so every fixpoint pass rebuilds them like the other recorders
	// instead of accumulating an early unstable pass's verdict.
	fs.callbackInvokes = nil
	fs.callbackInvokesInternal = nil
	fs.callbackAliases = nil
	fs.paramAliases = nil
	pf.notePageSinks(st, fs, st.fd.Body)
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
	// Callback aliases are read by recordCallbackInvokeComposition while
	// analyzeStmts walks the body: a callee's callback-invocation records
	// only compose through this function when the scanned-helper call
	// hands the function's own callback formal (or one of its recorded
	// aliases) to the callee. Recording the aliases BEFORE the body walk
	// makes the composition see the bindings in the same pass; the
	// records derive only from the body AST and the formal signature,
	// so an earlier recording cannot be stale. noteCallbackInvokes still
	// runs after the walk: its mapped-exemption reads the end-state
	// values left by analyzeStmts (guarded by the assignment census).
	pf.noteCallbackAliases(st, fs, st.fd.Body)
	pf.analyzeStmts(st, st.fd.Body.List, fs)
	pf.noteStringConvs(st, fs, st.fd.Body)
	pf.noteFmtSpreads(st, fs, st.fd.Body)
	pf.noteCopyParams(st, fs, st.fd.Body)
	pf.noteCallbackInvokes(st, fs, st.fd.Body)
	// Named results with a naked return: the body's stores to the named
	// result variables are the function's results. Functions need it for
	// FuncDecl bodies; closures need it too (out = p; return loses the
	// returned view). Fields of a named
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

// condReferencesPage reports whether a for-loop condition references the
// length of a page-tainted slice (for i := 0; i < len(page); i++), an
// aliased length (n := len(page); for i := 0; i < n; i++), or a constant
// bound equal to PageSize (for i := 0; i < 4096; i++).
// boundedLoopCopiesPage counts page-derived byte writes into indexed
// destinations in one loop body.
func (pf *pageFlow) boundedLoopCopiesPage(st *stmtState, body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) {
				break
			}
			if _, ok := unparen(lhs).(*ast.IndexExpr); ok && pf.pageDerivedRHS(st, assign.Rhs[i]) {
				count++
			}
		}
		return true
	})
	return count
}

// constForIterations returns the compile-time iteration count of a for
// loop, or -1 when the form is unrecognized.
func (pf *pageFlow) constForIterations(init ast.Stmt, cond ast.Expr, post ast.Stmt) int64 {
	if cond == nil {
		return -1
	}
	bl, ok := unparen(cond).(*ast.BinaryExpr)
	if !ok {
		return -1
	}
	lo, loOK := pf.loopOperand(init, bl.X, true)
	hi, hiOK := pf.loopOperand(init, bl.Y, false)
	if !loOK || !hiOK {
		return -1
	}
	increment, incOK := pf.loopIncrement(post)
	if !incOK {
		return -1
	}
	var diff int64
	switch bl.Op {
	case token.LSS:
		if increment <= 0 {
			return -1
		}
		diff = saturatingSub(hi, lo)
	case token.LEQ:
		if increment <= 0 {
			return -1
		}
		diff = saturatingSub(hi, lo)
		if hi < lo {
			return 0
		}
		diff = saturatingAdd(diff, 1)
	case token.GTR:
		if increment >= 0 {
			return -1
		}
		diff = saturatingSub(lo, hi)
	case token.GEQ:
		if increment >= 0 {
			return -1
		}
		diff = saturatingSub(lo, hi)
		if lo < hi {
			return 0
		}
		diff = saturatingAdd(diff, 1)
	default:
		return -1
	}
	if diff < 0 {
		return 0
	}
	return saturatingDivUp(diff, abs64(increment))
}

// loopOperand resolves the constant starting or ending operand of a for
// loop from its initializer, when statically known.
func (pf *pageFlow) loopOperand(init ast.Stmt, e ast.Expr, _ bool) (int64, bool) {
	e = unparen(e)
	if id, ok := e.(*ast.Ident); ok {
		if init == nil {
			return 0, false
		}
		assign, ok := init.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return 0, false
		}
		if lhsID, ok := unparen(assign.Lhs[0]).(*ast.Ident); !ok || pf.pc.info.ObjectOf(lhsID) != pf.pc.info.ObjectOf(id) {
			return 0, false
		}
		e = unparen(assign.Rhs[0])
	}
	if n, ok := pf.constIntExpr(e); ok {
		return n, true
	}
	return 0, false
}

// constIntExpr evaluates literal, named, conversion, and binary integer
// expressions through go/types constant values.
func (pf *pageFlow) constIntExpr(e ast.Expr) (int64, bool) {
	if tv, ok := pf.pc.info.Types[e]; ok && tv.Value != nil {
		if n, err := strconv.ParseInt(tv.Value.ExactString(), 0, 64); err == nil {
			return n, true
		}
		// Unsigned bounds above MaxInt64 saturate: any such loop can run
		// far beyond PageSize, and signed conversion must not wrap to 0.
		if u, err := strconv.ParseUint(tv.Value.ExactString(), 0, 64); err == nil && u > math.MaxInt64 {
			return math.MaxInt64, true
		}
	}
	switch v := e.(type) {
	case *ast.ParenExpr:
		return pf.constIntExpr(v.X)
	case *ast.CallExpr:
		if len(v.Args) == 1 {
			return pf.constIntExpr(v.Args[0])
		}
	case *ast.BinaryExpr:
		a, ok := pf.constIntExpr(v.X)
		if !ok {
			return 0, false
		}
		b, ok := pf.constIntExpr(v.Y)
		if !ok {
			return 0, false
		}
		switch v.Op {
		case token.ADD:
			return saturatingAdd(a, b), true
		case token.SUB:
			return saturatingAdd(a, -b), true
		case token.MUL:
			return saturatingMul(max64(a, 0), max64(b, 0)), true
		case token.SHL:
			if b < 0 || b >= 64 {
				return 0, false
			}
			return saturatingMul(max64(a, 0), int64(1)<<uint(b)), true
		}
	}
	return 0, false
}

// loopIncrement resolves i++, i--, i += n, and i -= n.
func (pf *pageFlow) loopIncrement(post ast.Stmt) (int64, bool) {
	switch p := post.(type) {
	case *ast.IncDecStmt:
		if p.Tok == token.INC {
			return 1, true
		}
		return -1, true
	case *ast.AssignStmt:
		if len(p.Lhs) != 1 || len(p.Rhs) != 1 {
			return 0, false
		}
		tv, ok := pf.pc.info.Types[unparen(p.Rhs[0])]
		if !ok || tv.Value == nil {
			return 0, false
		}
		n, err := strconv.ParseInt(tv.Value.ExactString(), 0, 64)
		if err != nil {
			return 0, false
		}
		if p.Tok == token.ADD_ASSIGN {
			return n, true
		}
		if p.Tok == token.SUB_ASSIGN {
			return -n, true
		}
	}
	return 0, false
}

// saturatingAdd caps positive arithmetic at MaxInt64 and negative at MinInt64.
func saturatingAdd(a, b int64) int64 {
	if a > 0 && b > math.MaxInt64-a {
		return math.MaxInt64
	}
	if a < 0 && b < math.MinInt64-a {
		return math.MinInt64
	}
	return a + b
}

// saturatingMul caps positive arithmetic at MaxInt64.
func saturatingMul(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

// saturatingSub returns a-b, treating inverted bounds as zero and
// arithmetic overflow as MaxInt64.
func saturatingSub(a, b int64) int64 {
	if b > a {
		return 0
	}
	if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
		return math.MaxInt64
	}
	return a - b
}

// saturatingDivUp divides non-negative a by positive b, rounding up and
// saturating only when the rounded quotient cannot fit int64.
func saturatingDivUp(a, b int64) int64 {
	if a < 0 || b <= 0 {
		return 0
	}
	q := a / b
	if a%b != 0 {
		if q == math.MaxInt64 {
			return math.MaxInt64
		}
		q++
	}
	return q
}

// abs64 returns the absolute value when representable.
func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func (pf *pageFlow) condReferencesPage(st *stmtState, cond ast.Expr) bool {
	if cond == nil {
		return false
	}
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if found {
			return false
		}
		// Direct len(page) reference.
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "len" && len(call.Args) == 1 {
				pv := pf.evalExpr(st, call.Args[0])
				if pv.tainted && pageFull(pv) {
					found = true
					return false
				}
			}
		}
		// Aliased length: a variable bound to len(page). The variable
		// itself is not page-tainted (it's an int), but it was recorded
		// in lenOfPage when bound to len(page).
		if id, ok := n.(*ast.Ident); ok {
			if obj := pf.pc.info.ObjectOf(id); obj != nil {
				if st.lenOfPage[obj] {
					found = true
					return false
				}
			}
		}
		// Constant bound equal to PageSize: for i := 0; i < 4096; i++,
		// including named constants (format.PageSize, const N = 4096).
		if isPageSizeExpr(st, n) {
			found = true
			return false
		}
		return true
	})
	return found
}

// isPageSizeExpr reports whether n is an integer expression whose
// constant value equals PageSize: a literal or a named/qualified
// constant.
func isPageSizeExpr(st *stmtState, n ast.Node) bool {
	if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.INT {
		v, err := strconv.ParseInt(bl.Value, 0, 64)
		return err == nil && v == pageSize
	}
	e, ok := n.(ast.Expr)
	if !ok {
		return false
	}
	tv, ok := st.pf.pc.info.Types[e]
	if !ok || tv.Value == nil {
		return false
	}
	c, err := strconv.ParseInt(tv.Value.ExactString(), 0, 64)
	return err == nil && c == pageSize
}

// markPageAggregated records an element-wise destination inside a
// page-sourcing loop: direct indexed writes (out[i] = b), field
// destinations (h.Out[i] = b), pointer destinations (*p)[i] = b), and
// append destinations (out = append(out, b)). The buffer becomes
// page-tainted for the rule pass's complete-page check.
func (pf *pageFlow) markPageAggregated(st *stmtState, lhs, rhs ast.Expr, pageSourced bool) {
	if !pageSourced {
		// A destination-ranging loop only initializes the buffer when
		// the RHS is clean (out[i] = 0); it is a page copy when the RHS
		// derives from a page (page[i]).
		if !pf.pageDerivedRHS(st, rhs) {
			return
		}
		pf.destAggregated[lhs] = true
	}
	dst := lhs
	// For an index assignment (out[i] = b), the destination is the
	// indexed base, not the index expression.
	if ix, ok := unparen(dst).(*ast.IndexExpr); ok {
		dst = ix.X
	}
	// For an append assignment (out = append(out, b)), the destination
	// is the append's first argument.
	if call, ok := unparen(rhs).(*ast.CallExpr); ok {
		if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "append" && len(call.Args) >= 1 {
			dst = call.Args[0]
		}
	}
	dst = unparen(dst)

	// Resolve the destination type and storage key. A direct variable
	// uses its own object; a field destination (h.Out[i]) uses the root
	// object plus the flattened field path.
	var obj types.Object
	var fieldPath string
	switch d := dst.(type) {
	case *ast.Ident:
		obj = st.pf.pc.info.ObjectOf(d)
	case *ast.SelectorExpr, *ast.StarExpr:
		if o, path := selectorChain(st, dst); o != nil {
			obj, fieldPath = o, path
		} else {
			obj = chainRootObject(st, dst)
		}
	case *ast.SliceExpr:
		if id, ok := unparen(d.X).(*ast.Ident); ok {
			obj = st.pf.pc.info.ObjectOf(id)
		}
	}
	if obj == nil {
		return
	}
	t := obj.Type().Underlying()
	if fieldPath != "" {
		t = leafPathType(st.pf.pc.pkg, t, fieldPath)
		if t == nil {
			return
		}
		t = t.Underlying()
	}
	// A pointer destination ((*p)[i] = b) addresses the pointee buffer.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem().Underlying()
	}
	if arr, ok := t.(*types.Array); ok && arr.Len() >= pageSize {
		pv := pageValue{tainted: true, maxLen: arr.Len()}
		if fieldPath != "" {
			pf.recordAggregatedField(st, obj, fieldPath, pv)
		} else {
			st.stmtVars[obj] = pv
		}
		// The indexed LHS expression must read as page-full in the rules
		// pass (rules.checkAssign consults the expression cache).
		pf.values[lhs] = pv
		return
	}
	if _, ok := t.(*types.Slice); ok {
		pv := pageValue{tainted: true, maxLen: maxUnknown}
		if fieldPath != "" {
			pf.recordAggregatedField(st, obj, fieldPath, pv)
		} else {
			st.stmtVars[obj] = pv
		}
		// Store the taint on the original LHS too, so the rules pass
		// sees the aggregated element expression as page-tainted.
		pf.values[lhs] = pv
		if call, ok := unparen(rhs).(*ast.CallExpr); ok {
			if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "append" && len(call.Args) >= 1 {
				pf.values[call.Args[0]] = pv
			}
		}
	}
}

// recordAggregatedField stores an aggregated field destination in the
// statement's field state.
func (pf *pageFlow) recordAggregatedField(st *stmtState, obj types.Object, path string, pv pageValue) {
	if st.structs[obj] == nil {
		st.structs[obj] = map[string]pageValue{}
	}
	st.structs[obj][path] = pv
	if obj.Parent() == st.pf.pc.pkg.Scope() && st.pkgStructs != nil {
		gm := st.pkgStructs[obj]
		if gm == nil {
			gm = map[string]pageValue{}
			st.pkgStructs[obj] = gm
		}
		gm[path] = pv
	}
}

// max64 returns the larger two int64 values.
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// makeLen resolves a make length argument to a concrete integer, or -1
// when it is not a compile-time constant.
func (pf *pageFlow) makeLen(st *stmtState, n ast.Expr) int64 {
	if bl, ok := unparen(n).(*ast.BasicLit); ok && bl.Kind == token.INT {
		if v, err := strconv.ParseInt(bl.Value, 0, 64); err == nil {
			return v
		}
		return -1
	}
	if tv, ok := pf.pc.info.Types[unparen(n)]; ok && tv.Value != nil {
		if v, err := strconv.ParseInt(tv.Value.ExactString(), 0, 64); err == nil {
			return v
		}
	}
	return -1
}

// pageDerivedRHS reports whether a scalar element RHS derives from a
// page-tainted container in the current state.
func (pf *pageFlow) pageDerivedRHS(st *stmtState, rhs ast.Expr) bool {
	rhs = unparen(rhs)
	if ix, ok := rhs.(*ast.IndexExpr); ok {
		if pv := pf.evalExpr(st, ix.X); pv.tainted {
			return true
		}
	}
	// Range byte values (for i, b := range page) are bound from the page;
	// their scalar value is clean, but their source is the range over a
	// page. Any identifier/call/binary RHS inside a page-sourcing loop is
	// conservatively page-derived.
	switch rhs.(type) {
	case *ast.Ident, *ast.CallExpr:
		return true
	case *ast.BinaryExpr:
		be := rhs.(*ast.BinaryExpr)
		return pf.pageDerivedRHS(st, be.X) || pf.pageDerivedRHS(st, be.Y)
	}
	return pf.evalExpr(st, rhs).tainted
}

// constRangeDestination reports the definite iteration count of a range
// over an integer constant (Go 1.22+), or -1 when the bound is unknown.
func (pf *pageFlow) constRangeDestination(x ast.Expr) int64 {
	if bl, ok := unparen(x).(*ast.BasicLit); ok && bl.Kind == token.INT {
		if n, err := strconv.ParseInt(bl.Value, 0, 64); err == nil {
			return n
		}
		return -1
	}
	if tv, ok := pf.pc.info.Types[unparen(x)]; ok && tv.Value != nil {
		if n, err := strconv.ParseInt(tv.Value.ExactString(), 0, 64); err == nil {
			return n
		}
	}
	return -1
}

// rangeDestination reports whether the range expression names an owned
// byte destination whose element count is PageSize or more: a fixed
// array, a slice made with PageSize length/capacity, or a slice whose
// symbolic length reaches PageSize.
func (pf *pageFlow) rangeDestination(st *stmtState, x ast.Expr) pageValue {
	x = unparen(x)
	// A slice expression over an identifier names the same backing array.
	if se, ok := x.(*ast.SliceExpr); ok {
		x = unparen(se.X)
	}
	id, ok := x.(*ast.Ident)
	if !ok {
		return pageValue{}
	}
	obj := pf.pc.info.ObjectOf(id)
	if obj == nil {
		return pageValue{}
	}
	// An alias of a recorded slice destination shares its length.
	if _, ok := st.sliceLens[obj]; !ok {
		if bind, ok := st.localBindings[obj]; ok {
			if srcID, ok := unparen(bind).(*ast.Ident); ok {
				if srcObj := pf.pc.info.ObjectOf(srcID); srcObj != nil {
					if n, ok := st.sliceLens[srcObj]; ok {
						st.sliceLens[obj] = n
					}
				}
			}
		}
	}
	t := obj.Type().Underlying()
	if arr, ok := t.(*types.Array); ok && arr.Len() >= pageSize {
		return pageValue{tainted: true, maxLen: arr.Len()}
	}
	if _, ok := t.(*types.Slice); !ok {
		return pageValue{}
	}
	// Slice destination: resolve its recorded make length/capacity
	// (out := make([]byte, PageSize)).
	n, ok := st.sliceLens[obj]
	if !ok || n < pageSize {
		return pageValue{}
	}
	return pageValue{tainted: true, maxLen: n}
}

func (pf *pageFlow) analyzeStmts(st *stmtState, list []ast.Stmt, fs *funcSummary) {
	st.activeFS = fs
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
				if call, ok := unparen(rhs).(*ast.CallExpr); ok {
					if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "append" && len(call.Args) >= 1 {
						if argID, ok := unparen(call.Args[0]).(*ast.Ident); ok {
							if obj := pf.pc.info.ObjectOf(argID); obj != nil {
								pf.appendCallRoots[call] = pf.canonicalAppendRoot(obj)
							}
						}
					}
				}
				// A helper used in value position can still be an
				// element sink (_ = put(out, i, b)); record it like an
				// expression-statement call inside a page-sourcing loop.
				if st.pageSourceLoops > 0 {
					pf.recordPageSinkCall(st, rhs)
				}
				// Inside a page-sourcing loop, an element write into an
				// owned array of PageSize aggregates to a complete page
				// copy. Mark the array as page-aggregated so the
				// complete-page rule catches it.
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
				// Track len(page) bindings so a for-loop condition using
				// the alias (for i := 0; i < n; i++) is recognized as a
				// page-sourcing loop.
				if call, ok := unparen(v.Rhs[i]).(*ast.CallExpr); ok {
					if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "len" && len(call.Args) == 1 {
						if pv := pf.evalExpr(st, call.Args[0]); pv.tainted && pageFull(pv) {
							if obj := objOf(st, v.Lhs[i]); obj != nil {
								st.lenOfPage[obj] = true
							}
						}
					}
					if id, ok := unparen(call.Fun).(*ast.Ident); ok && id.Name == "make" && len(call.Args) >= 2 {
						if obj := objOf(st, v.Lhs[i]); obj != nil {
							if _, ok := obj.Type().Underlying().(*types.Slice); ok {
								if n := pf.makeLen(st, call.Args[1]); n >= 0 {
									st.sliceLens[obj] = n
								} else {
									delete(st.sliceLens, obj)
								}
							}
						}
					}
				}
				// A non-alias slice rebind names a fresh buffer: its
				// old bounded-span alias edge must not survive.
				if dstObj, ok := objOf(st, v.Lhs[i]).(*types.Var); ok {
					aliasSrcTmp := unparen(v.Rhs[i])
					if se, ok := aliasSrcTmp.(*ast.SliceExpr); ok {
						aliasSrcTmp = unparen(se.X)
					}
					if call, ok := aliasSrcTmp.(*ast.CallExpr); ok {
						if id, ok := unparen(call.Fun).(*ast.Ident); !ok || id.Name != "append" {
							pf.appendAliases[dstObj] = nil
						}
					} else if _, ok := aliasSrcTmp.(*ast.Ident); !ok {
						pf.appendAliases[dstObj] = nil
					}
				}
				// Selector fields alias backing storage with another
				// selector or an identifier. Canonicalize the destination
				// key to the source storage before accumulation.
				if srcSel, ok := unparen(v.Rhs[i]).(*ast.SelectorExpr); ok {
					if dstSel, ok := unparen(v.Lhs[i]).(*ast.SelectorExpr); ok {
						srcRoot, srcPath := selectorChain(st, srcSel)
						dstRoot, dstPath := selectorChain(st, dstSel)
						if srcRoot != nil && dstRoot != nil {
							srcKey := boundedSpanKey{obj: srcRoot, path: srcPath}
							dstKey := boundedSpanKey{obj: dstRoot, path: dstPath}
							root := srcKey
							for d := 0; d < 8; d++ {
								next, ok := pf.spanAliases[root]
								if !ok || next == root {
									break
								}
								root = next
							}
							pf.spanAliases[dstKey] = root
						}
					} else if dstSel, ok := unparen(v.Lhs[i]).(*ast.SelectorExpr); ok {
						if srcRoot, srcPath := selectorChain(st, srcSel); srcRoot != nil {
							if dstRoot, dstPath := selectorChain(st, dstSel); dstRoot != nil {
								srcKey := boundedSpanKey{obj: srcRoot, path: srcPath}
								pf.spanAliases[boundedSpanKey{obj: dstRoot, path: dstPath}] = srcKey
							}
						}
					}
				} else if srcID, ok := unparen(v.Rhs[i]).(*ast.Ident); ok {
					if dstSel, ok := unparen(v.Lhs[i]).(*ast.SelectorExpr); ok {
						if srcObj := objOf(st, srcID); srcObj != nil {
							if dstRoot, dstPath := selectorChain(st, dstSel); dstRoot != nil {
								pf.spanAliases[boundedSpanKey{obj: dstRoot, path: dstPath}] = boundedSpanKey{obj: srcObj}
							}
						}
					}
				} else if _, ok := unparen(v.Rhs[i]).(*ast.BasicLit); ok {
					if dstSel, ok := unparen(v.Lhs[i]).(*ast.SelectorExpr); ok {
						if dstRoot, dstPath := selectorChain(st, dstSel); dstRoot != nil {
							delete(pf.spanAliases, boundedSpanKey{obj: dstRoot, path: dstPath})
						}
					}
				}
				// A struct literal can alias one backing slice into
				// several fields (h = box{left: owned[:2048], right:
				// owned[2048:]}); every such field canonicalizes to that
				// backing identifier for bounded-span accumulation.
				if dstID, ok := unparen(v.Lhs[i]).(*ast.Ident); ok {
					if lit, ok := unparen(v.Rhs[i]).(*ast.CompositeLit); ok {
						for _, elt := range lit.Elts {
							kv, ok := elt.(*ast.KeyValueExpr)
							if !ok {
								continue
							}
							keyID, ok := unparen(kv.Key).(*ast.Ident)
							if !ok {
								continue
							}
							se, ok := unparen(kv.Value).(*ast.SliceExpr)
							if !ok {
								continue
							}
							srcID, ok := unparen(se.X).(*ast.Ident)
							if !ok {
								continue
							}
							srcObj := objOf(st, srcID)
							if srcObj == nil {
								continue
							}
							pf.spanAliases[boundedSpanKey{obj: pf.pc.info.ObjectOf(dstID), path: keyID.Name}] = boundedSpanKey{obj: srcObj}
						}
					}
				}
				// Chained length aliases: n := len(page); m := n keeps
				// the page-derived bound transitively. Slice aliases
				// (dst := out) share the recorded make length.
				aliasSrc := unparen(v.Rhs[i])
				if se, ok := aliasSrc.(*ast.SliceExpr); ok {
					if id, ok := unparen(se.X).(*ast.Ident); ok {
						aliasSrc = id
					}
				}
				if srcID, ok := aliasSrc.(*ast.Ident); ok {
					srcObj := objOf(st, srcID)
					obj := objOf(st, v.Lhs[i])
					if srcObj != nil && obj != nil {
						if st.lenOfPage[srcObj] {
							st.lenOfPage[obj] = true
						}
						// A slice alias rebind shares one backing buffer's
						// bounded page-append accumulation.
						if srcObj2, ok := srcObj.(*types.Var); ok {
							if obj2, ok := obj.(*types.Var); ok {
								root := types.Object(srcObj2)
								for d := 0; d < 8; d++ {
									next, ok := pf.appendAliases[root]
									if !ok || next == root {
										break
									}
									if next == nil {
										// The source was rebound to a fresh buffer;
										// that source variable is the new canonical
										// buffer name, not the end of the chain.
										break
									}
									root = next
								}
								pf.appendAliases[obj2] = root
							}
						}
						if _, ok := obj.Type().Underlying().(*types.Slice); ok {
							if _, ok := srcObj.Type().Underlying().(*types.Slice); ok {
								if n, ok := st.sliceLens[srcObj]; ok {
									st.sliceLens[obj] = n
								} else {
									delete(st.sliceLens, obj)
								}
							}
						}
					}
				}
				if se, ok := unparen(v.Rhs[i]).(*ast.SliceExpr); ok {
					if srcID, ok := unparen(se.X).(*ast.Ident); ok {
						if srcObj := objOf(st, srcID); srcObj != nil {
							if obj := objOf(st, v.Lhs[i]); obj != nil {
								if _, ok := obj.Type().Underlying().(*types.Slice); ok {
									if n, ok := st.sliceLens[srcObj]; ok {
										st.sliceLens[obj] = n
									}
								}
							}
						}
					}
				}
				pf.materializeStructFields(st, v.Lhs[i], v.Rhs[i])
				assignTarget(st, v.Lhs[i], pv)
				// Inside a page-sourcing loop, an element write into an
				// owned buffer aggregates to a complete page copy. Mark
				// the destination AFTER normal assignment processing so
				// the mark survives.
				if st.pageSourceLoops > 0 || st.destRangeLoops > 0 {
					pf.markPageAggregated(st, v.Lhs[i], rhs, st.pageSourceLoops > 0)
				}
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
						// resolves. A TYPE-ASSERTED base
						// (v.(*H).Items[0]) binds the asserted base
						// variable the same way.
						if obj := chainRootObject(st, root); obj != nil {
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
					} else if ta := typeAssertBaseOf(ix.X); ta != nil {
						// v.(*H).M[&b] = 1 with v an interface holding
						// *H: the map is a selected field of an
						// ASSERTED base, so the key's fields record on
						// the asserted base variable under the field
						// prefix, the same flattened path the key-only
						// range for k := range v.(*H).M resolves.
						if obj := objOfDeref(st, ta.X); obj != nil {
							if kf := pf.argFlowOf(st, ix.Index).fields; len(kf) > 0 {
								if st.structs[obj] == nil {
									st.structs[obj] = map[string]pageValue{}
								}
								path, _ := selectorIndexChain(ix.X)
								for k, fv := range kf {
									key := path + "." + k
									if prev, ok := st.structs[obj][key]; ok {
										st.structs[obj][key] = joinPageValue(prev, fv)
									} else {
										st.structs[obj][key] = fv
									}
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
								// A declared slice alias (short and var
								// forms) shares the canonical bounded
								// page-span accumulation.
								declSrc := unparen(vs.Values[i])
								if se, ok := declSrc.(*ast.SliceExpr); ok {
									if id, ok := unparen(se.X).(*ast.Ident); ok {
										declSrc = id
									}
								}
								if srcID, ok := declSrc.(*ast.Ident); ok {
									srcObj := objOf(st, srcID)
									dstObj, ok1 := obj.(*types.Var)
									srcVar, ok2 := srcObj.(*types.Var)
									if ok1 && ok2 {
										root := types.Object(srcVar)
										for d := 0; d < 8; d++ {
											next, ok := pf.appendAliases[root]
											if !ok || next == nil || next == root {
												break
											}
											root = next
										}
										pf.appendAliases[dstObj] = root
									}
								}
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
			// A for loop whose condition references the length of a
			// page-tainted slice makes the loop body a page-sourcing
			// context: element-wise writes into an owned buffer of
			// PageSize aggregate to a complete page copy.
			pageSourcing := false
			if pf.condReferencesPage(st, v.Cond) {
				st.pageSourceLoops++
				pageSourcing = true
			}
			// A statically PageSize-iteration loop with page-derived
			// indexed writes copies a complete page even when each
			// iteration writes several bytes and the bound is half-page.
			// A statically bounded loop whose iteration count times
			// page-derived indexed writes can reach PageSize copies a
			// complete page, including multiple disjoint writes per body.
			innerBound := st.loopBound
			if !pageSourcing && v.Cond != nil {
				if bound := pf.constForIterations(v.Init, v.Cond, v.Post); bound > 0 && saturatingMul(saturatingMul(innerBound, bound), int64(pf.boundedLoopCopiesPage(st, v.Body))) >= pageSize {
					st.pageSourceLoops++
					pageSourcing = true
				}
			}
			pre := st.clone()
			st.loopBound = saturatingMul(innerBound, max64(pf.constForIterations(v.Init, v.Cond, v.Post), 0))
			if st.loopBound == 0 {
				st.loopBound = 0
			}
			pf.analyzeStmts(st, v.Body.List, fs)
			if v.Post != nil {
				pf.analyzeStmts(st, []ast.Stmt{v.Post}, fs)
			}
			st.loopBound = innerBound
			if pageSourcing {
				st.pageSourceLoops--
			}
			st.joinWith(pre) // zero iterations stay possible
		case *ast.RangeStmt:
			// A range over a page-tainted slice makes the loop body a
			// page-sourcing context: element-wise writes into an owned
			// buffer of PageSize aggregate to a complete page copy.
			pageSourcing := false
			destRange := false
			pv := pf.evalExpr(st, v.X)
			if pv.tainted && pageFull(pv) {
				st.pageSourceLoops++
				pageSourcing = true
			}
			// A range over an owned destination (for i := range out {
			// out[i] = page[i] }) is also a page-sourcing context when
			// the destination has PageSize elements: a fixed array, a
			// slice allocated at PageSize, or a slice whose recorded
			// bound reaches PageSize.
			if !pageSourcing {
				if pf.rangeDestination(st, v.X).tainted {
					st.destRangeLoops++
					pageSourcing = true
					destRange = true
				}
			}
			// for i := range 4096 iterates PageSize times. Element writes
			// from a page source aggregate to a complete page copy.
			innerBound := st.loopBound
			if !pageSourcing {
				if n := pf.constRangeDestination(v.X); n > 0 && saturatingMul(saturatingMul(innerBound, n), int64(pf.boundedLoopCopiesPage(st, v.Body))) >= pageSize {
					st.pageSourceLoops++
					pageSourcing = true
				}
			}
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
				// A func-typed range VALUE (for _, cb := range cbs)
				// binds an element of the container: record the
				// container so an invocation through the loop value
				// (cb(v)) resolves to the container's callback slot and
				// the store-site fence polices the byte views it
				// receives.
				if valObj := objOf(st, v.Value); valObj != nil && v.X != nil && elemFuncType(pf.pc.info.TypeOf(v.X)) {
					if fs.rangeVars == nil {
						fs.rangeVars = map[types.Object]ast.Expr{}
					}
					fs.rangeVars[valObj] = v.X
				}
			}
			pre := st.clone()
			st.loopBound = saturatingMul(innerBound, max64(pf.constRangeDestination(v.X), 0))
			pf.analyzeStmts(st, v.Body.List, fs)
			st.loopBound = innerBound
			// Decrement the counter this statement incremented. Nested
			// mixed contexts (a destination range inside a page-sourcing
			// loop) must not close the outer loop's context.
			if destRange {
				st.destRangeLoops--
			} else if pageSourcing {
				st.pageSourceLoops--
			}
			st.joinWith(pre) // zero iterations stay possible
		case *ast.ExprStmt:
			pf.evalExpr(st, v.X)
			// A helper that writes element-wise into a parameter, called
			// inside a page-sourcing loop, aggregates the writes into the
			// caller's buffer. Record the destination argument so the rule
			// pass can fail the call site.
			if st.pageSourceLoops > 0 {
				pf.recordPageSinkCall(st, v.X)
			}
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
				// A MULTI-TYPE case (case B, *B) types the implicit
				// variable with the GUARD's interface type: the
				// page-carrying leaves of every case type project onto
				// it, because the concrete value can be any of them and
				// the type-asserted reads inside the clause resolve the
				// same records.
				caseTypes := []types.Type{cv.Type()}
				if isInterfaceType(cv.Type()) && len(cc.List) > 0 {
					caseTypes = caseTypes[:0]
					for _, ce := range cc.List {
						if ct := pf.pc.info.Types[ce].Type; ct != nil {
							caseTypes = append(caseTypes, ct)
						}
					}
				}
				for _, ct := range caseTypes {
					for path, ft := range paramLeafPaths(ct) {
						if !paramCanCarryPage(ft) {
							continue
						}
						if branch.structs[cv] == nil {
							branch.structs[cv] = map[string]pageValue{}
						}
						branch.structs[cv][path] = pageValue{tainted: true, maxLen: maxUnknown}
					}
				}
				// A case whose matched type is itself an INTERFACE
				// (case any, or a multi-type case carrying the guard's
				// interface type) projects no concrete leaves; the
				// concrete value is unknowable, so the implicit
				// variable keeps its whole-value taint and body-side
				// type assertions (b := x.(T); b.Data) fail closed
				// through the asserted type's leaf projection exactly
				// like any other whole-tainted interface variable.
				if isInterfaceType(cv.Type()) {
					branch.stmtVars[cv] = pageValue{tainted: true, maxLen: maxUnknown}
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
	dstObj := objOf(st, dst)
	if dstObj == nil {
		return
	}

	var fields map[string]pageValue
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
	if id != nil {
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
	} else {
		// Ident-less sources include COMPOSITE LITERALS besides the
		// indexed, selected, dereferenced-call and call-produced values
		// (B{Data: page}, b.Inner, xs[0], *f(p), f(p).Inner): all bind
		// the same fields direct argument flow resolves, so closure
		// parameters fed such arguments carry the selected element
		// fields into the body. Without this fallback a directly called
		// func-literal parameter (func(x B) { out = x.Data }(B{Data:
		// page})) loses the field taint and the captured write launders
		// the page.
		fields = pf.argFlowOf(st, src).fields
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
	// An INDEXED channel (cs[0] <- B{Data: page} with cs []chan B, or
	// h.Chs[0] with h.Chs []chan B) names an element of a container the
	// same way an indexed struct store does: the channel's element
	// records live on the root container object — a plain variable with
	// no prefix, or a selected field under the "Chs." prefix — and the
	// matching receive resolves the same root, so the send must record
	// there.
	if root := indexChainRoot(ch); root != ch {
		if path, ro := selectorIndexChain(ch); ro != nil {
			// A TYPE-ASSERTED base (v.(*H).Chs[0] with v an interface
			// holder) binds the asserted base variable: the send's
			// element records land on v under the "Chs." prefix, the
			// same object the matching asserted receive resolves.
			if oo := chainRootObject(st, ro); oo != nil {
				record(oo, path+".")
				return
			}
		}
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
	// An INDEXED channel (cs[0], h.Chs[0]) resolves the same root
	// container the send recorded on: the received element fields are
	// the root's element records — a plain variable's records directly,
	// a selected field's records under the "Chs." prefix, stripped to
	// the direct element field names — exactly like the indexed-store
	// read path.
	if root := indexChainRoot(ch); root != ch {
		if path, ro := selectorIndexChain(ch); ro != nil {
			// A TYPE-ASSERTED channel base (v.(*H).Chs[0]) resolves the
			// asserted base variable's "Chs."-prefixed records, the
			// same object the asserted send recorded them on.
			if oo := chainRootObject(st, ro); oo != nil {
				add(oo, path+".", true)
			}
		}
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
	mk := func(s maxSrc) maxSrc {
		s.mapped = pv.mapped
		return s
	}
	// A definite symbol bound wins over the parametric source: a slice
	// of a parameter with constant low/high (p[48:112]) carries a
	// caller-independent bound, so its summary must keep the constant
	// instead of degrading to the parameter's whole bound (which would
	// turn every bounded slice into a full page at the call site).
	// Parametric-sourced values WITHOUT a symbol (plain parameters,
	// extractions, mints) keep their caller-dependent source below.
	if pv.hasSym {
		if c, ok := pv.sym.isConst(); ok {
			// A bounded slice of a mapped view is still mapped: the
			// store-callback rule and the copy rules need the mapped
			// provenance across summary boundaries, not only the bound.
			return []maxSrc{mk(maxSrc{param: -1, kind: "const", constVal: c})}
		}
		if len(pv.sym.coeff) == 1 && pv.sym.c == 0 {
			for p := range pv.sym.coeff {
				return []maxSrc{mk(maxSrc{param: p, kind: "value"})}
			}
		}
		return []maxSrc{mk(maxSrc{param: -1, kind: "const", constVal: maxUnknown})}
	}
	if pv.hasSrc {
		if pv.srcField != "" {
			return []maxSrc{mk(maxSrc{param: pv.srcParam, kind: "paramField", field: pv.srcField})}
		}
		return []maxSrc{mk(maxSrc{param: pv.srcParam, kind: "param"})}
	}
	if pv.hasSym {
		if c, ok := pv.sym.isConst(); ok {
			return []maxSrc{mk(maxSrc{param: -1, kind: "const", constVal: c})}
		}
		if len(pv.sym.coeff) == 1 && pv.sym.c == 0 {
			for p := range pv.sym.coeff {
				return []maxSrc{mk(maxSrc{param: p, kind: "value"})}
			}
		}
		return []maxSrc{mk(maxSrc{param: -1, kind: "const", constVal: maxUnknown})}
	}
	if pv.maxLen >= 0 {
		return []maxSrc{mk(maxSrc{param: -1, kind: "const", constVal: pv.maxLen})}
	}
	return []maxSrc{mk(maxSrc{param: -1, kind: "const", constVal: maxUnknown})}
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
		// The mapped flag is PROVABLY-mapped: the value must be a mapped
		// view on every path that can produce it, or the fail-closed
		// consumers (store-callback mapped-view fence, owned-copy
		// checks) would bless an owned buffer that one branch delivers.
		return pageValue{tainted: true, maxLen: maxUnknown, mapped: a.mapped && b.mapped}
	}
	if a.maxLen >= b.maxLen {
		a.mapped = a.mapped && b.mapped
		return a
	}
	b.mapped = a.mapped && b.mapped
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
	// Conservative mapped join: a value tainted on any path keeps every
	// length source, but it is PROVABLY mapped only when both records
	// are (one owned path makes the consumer checks fail closed).
	out.mapped = a.mapped && b.mapped
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
	cp.destRangeLoops = st.destRangeLoops
	cp.loopBound = st.loopBound
	cp.lenOfPage = map[types.Object]bool{}
	for k := range st.lenOfPage {
		cp.lenOfPage[k] = true
	}
	cp.sliceLens = map[types.Object]int64{}
	for k, v := range st.sliceLens {
		cp.sliceLens[k] = v
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
	if other.destRangeLoops > st.destRangeLoops {
		st.destRangeLoops = other.destRangeLoops
	}
	// A nested loop is reachable through either branch; the larger bound
	// is the conservative join.
	if other.loopBound > st.loopBound {
		st.loopBound = other.loopBound
	}
	for k := range other.lenOfPage {
		st.lenOfPage[k] = true
	}
	for k, v := range other.sliceLens {
		if v > st.sliceLens[k] {
			st.sliceLens[k] = v
		}
	}
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
			sym := sliceLenSym(v, st, base.maxLen)
			maxLen := int64(maxUnknown)
			if c, ok := sym.isConst(); ok {
				maxLen = c
			}
			out = pageValue{tainted: true, maxLen: maxLen, sym: sym, hasSym: true, mapped: base.mapped}
			if base.hasSrc {
				out.srcParam = base.srcParam
				out.srcField = base.srcField
				out.hasSrc = true
			}
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
			if t := valueCarrierType(pf.pc.info.Types[v].Type); t != nil && typeCanCarryPage(t) {
				out = derivedPageValue(base)
			}
		}
	case *ast.IndexListExpr:
		if base := pf.evalExpr(st, v.X); base.tainted {
			if t := valueCarrierType(pf.pc.info.Types[v].Type); t != nil && typeCanCarryPage(t) {
				out = derivedPageValue(base)
			}
		}
	case *ast.TypeAssertExpr:
		// x.([]byte): the asserted value keeps the mapped-view taint when
		// the asserted type can hold page bytes; asserting to a scalar
		// stays clean. A TWO-VALUE form (b, ok := v.(T), b, ok := m[k])
		// types the expression node as the (T, bool) tuple: the carrier
		// test must use the value slot, or every page-holding assertion
		// or map read launders the taint into the bound variable.
		if pv := pf.evalExpr(st, v.X); pv.tainted {
			if t := valueCarrierType(pf.pc.info.Types[v].Type); t != nil && typeCanCarryPage(t) {
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
	out := pageValue{tainted: true, maxLen: base.maxLen, hasSym: base.hasSym, sym: base.sym, mapped: base.mapped}
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
	// A struct carrier in expression position (runCar(car{cb: fn}, ...),
	// or a composite returned from a helper): seed the field records on
	// the active summary exactly like the assignment forms, so the
	// carrier fence and the cross-function composition see the binding.
	if st.activeFS != nil {
		pf.seedStructComposite(st, st.activeFS, v)
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
	// A complete struct carrying byte views hides them (wrap(fn, pair{
	// x, y}) passes the pair, the wrapper calls cb(p.a, p.b)): the
	// composite's mappedness is the conjunction of every byte-carrying
	// field that is itself a recorded view. One owned field makes the
	// whole composite unprovable, so the store-callback view check
	// stays fail-closed on mixed carriers.
	out := pageValue{tainted: true, maxLen: maxLen}
	if styp, ok := derefStruct(typ); ok && styp.NumFields() > 0 {
		allMapped := true
		saw := false
		fields := map[string]pageValue{}
		for i, el := range v.Elts {
			var field string
			var val ast.Expr
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if fid, ok := unparen(kv.Key).(*ast.Ident); ok {
					field, val = fid.Name, kv.Value
				}
			} else if i < styp.NumFields() {
				field, val = styp.Field(i).Name(), el
			}
			if field == "" || val == nil {
				continue
			}
			fv := pf.evalExpr(st, val)
			fields[field] = fv
		}
		for i := 0; i < styp.NumFields(); i++ {
			ft := styp.Field(i).Type()
			if _, isSlice := types.Unalias(ft).(*types.Slice); !isSlice {
				continue
			}
			fv, ok := fields[styp.Field(i).Name()]
			if !ok {
				allMapped = false
				break
			}
			saw = true
			if !fv.mapped {
				allMapped = false
				break
			}
		}
		if saw {
			out.mapped = allMapped
		}
	}
	return out
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

// valueCarrierType returns the value-carrying slot of an expression
// result type: a two-value form (b, ok := v.(T), b, ok := m[k]) types
// the expression node as the (T, bool) tuple, and every whole-value
// taint test must use the value slot, not the tuple.
func valueCarrierType(t types.Type) types.Type {
	if tt, ok := t.(*types.Tuple); ok && tt.Len() == 2 {
		return tt.At(0).Type()
	}
	return t
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

// notePageSinks records parameter indexes whose elements are written
// inside the callee (dst[i] = b, d[i] = b with d := dst, dst.Out[i] = b,
// or (*dst)[i] = b). A call site inside a page-sourcing loop that passes
// an owned buffer to such a parameter aggregates the helper's per-element
// writes into a complete page copy.
func (pf *pageFlow) notePageSinks(st *stmtState, fs *funcSummary, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	// sinkAliases maps local variables assigned from a sink parameter;
	// writes through those aliases name the same backing buffer.
	sinkAliases := map[types.Object]int{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) {
				break
			}
			dstObj := objOf(st, lhs)
			if dstObj == nil {
				continue
			}
			srcID, ok := unparen(assign.Rhs[i]).(*ast.Ident)
			if !ok {
				continue
			}
			srcObj := objOf(st, srcID)
			if srcObj == nil {
				continue
			}
			if idx, ok := st.params[srcObj]; ok {
				sinkAliases[dstObj] = idx
			} else if idx, ok := sinkAliases[srcObj]; ok {
				sinkAliases[dstObj] = idx
			}
		}
		return true
	})
	record := func(idx int) {
		if fs.pageSinkParams == nil {
			fs.pageSinkParams = map[int]bool{}
		}
		fs.pageSinkParams[idx] = true
	}
	recordBase := func(base ast.Expr) {
		// Strip slice expressions: dst[:][i] still names dst.
		for {
			if se, ok := unparen(base).(*ast.SliceExpr); ok {
				base = se.X
				continue
			}
			break
		}
		// Direct parameter or an alias of one.
		if id, ok := unparen(base).(*ast.Ident); ok {
			obj := pf.pc.info.ObjectOf(id)
			if obj == nil {
				return
			}
			if idx, ok := st.params[obj]; ok {
				record(idx)
			} else if idx, ok := sinkAliases[obj]; ok {
				record(idx)
			}
			return
		}
		// Field/pointer destinations rooted at a parameter or alias.
		root, _ := selectorChain(st, unparen(base))
		if root == nil {
			root = chainRootObject(st, unparen(base))
		}
		if root == nil {
			return
		}
		if idx, ok := st.params[root]; ok {
			record(idx)
		} else if idx, ok := sinkAliases[root]; ok {
			record(idx)
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			ix, ok := unparen(lhs).(*ast.IndexExpr)
			if !ok {
				continue
			}
			recordBase(ix.X)
		}
		return true
	})
}

// noteCopyParams records copy(paramD[..], paramS[..]) pairs inside the
// body: the callee copies between two caller-bound buffers, so the
// owned/mapped decision belongs to the call sites (an owned destination
// bound together with a mapped full-page source is the complete-page
// copy the definition site cannot see). Aliases of parameters count as
// the parameter they were assigned from (d := page; copy(d[..], cell)).
func (pf *pageFlow) noteCopyParams(st *stmtState, fs *funcSummary, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	paramAliases := map[types.Object]int{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) {
				break
			}
			dstObj := objOf(st, lhs)
			if dstObj == nil {
				continue
			}
			srcID, ok := unparen(assign.Rhs[i]).(*ast.Ident)
			if !ok {
				continue
			}
			srcObj := objOf(st, srcID)
			if srcObj == nil {
				continue
			}
			if idx, ok := st.params[srcObj]; ok {
				paramAliases[dstObj] = idx
			} else if idx, ok := paramAliases[srcObj]; ok {
				paramAliases[dstObj] = idx
			}
		}
		return true
	})
	rootSlot := func(e ast.Expr) (int, bool) {
		// Strip slice wrappers: copy(page[:][..], x) still names page.
		for {
			if se, ok := unparen(e).(*ast.SliceExpr); ok {
				e = se.X
				continue
			}
			break
		}
		id, ok := unparen(e).(*ast.Ident)
		if !ok {
			return 0, false
		}
		obj := pf.pc.info.ObjectOf(id)
		if obj == nil {
			return 0, false
		}
		if idx, ok := st.params[obj]; ok {
			return idx, true
		}
		return 0, false
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		id, ok := unparen(call.Fun).(*ast.Ident)
		if !ok || id.Name != "copy" {
			return true
		}
		d, dok := rootSlot(call.Args[0])
		s, sok := rootSlot(call.Args[1])
		if !dok || !sok {
			return true
		}
		if fs.copyParams == nil {
			fs.copyParams = map[int][]int{}
		}
		for _, prev := range fs.copyParams[d] {
			if prev == s {
				return true
			}
		}
		fs.copyParams[d] = append(fs.copyParams[d], s)
		return true
	})
}

// noteCallbackAliases records local function-typed variables that
// alias a func-typed formal parameter of the enclosing function:
// cb := fn, cb := func(a, b []byte) error { return fn(a, b) }, and
// chains of both (cb2 := cb). The alias record carries the formal's
// parameter slot and, for literal wrappers, the closure parameter
// positions the body actually forwards to the formal. A store
// implementation can hide its callback formal behind such a local and
// hand the local to a scanned helper; the store-callback fence at the
// call site follows the alias, and only views bound to forwarded
// positions reach the callback. A literal that cannot be shown to
// invoke the formal is not an alias: binding it at a call site is not
// forwarding the callback formal, and the fence would be unsound.
// rootObjectOf resolves the root object of a selector/identifier
// expression (s.hs -> s), used by composite-literal seeding to key the
// container slot.
func (pf *pageFlow) rootObjectOf(e ast.Expr) types.Object {
	for {
		switch t := unparen(e).(type) {
		case *ast.SelectorExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		default:
			if id, ok := t.(*ast.Ident); ok {
				return pf.pc.info.ObjectOf(id)
			}
			return nil
		}
	}
}

// funcTypeOf returns the signature type of a func-typed expression (a
// callee expression: identifier, field, index element, asserted type, or
// call result).
func (pf *pageFlow) funcTypeOf(e ast.Expr) *types.Signature {
	t := pf.pc.info.TypeOf(unparen(e))
	if t == nil {
		return nil
	}
	return funcSignature(t)
}

// elemFuncType reports whether t is a slice or map whose element type
// is a function signature (a func-typed callback container).
func elemFuncType(t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	switch u := t.(type) {
	case *types.Slice:
		return funcSignature(u.Elem()) != nil
	case *types.Map:
		return funcSignature(u.Elem()) != nil
	}
	return false
}

// slotOfExpr resolves a func-typed expression to a store-callback
// formal slot of the current function: the formal itself, a local
// identity or type-assertion alias, a struct field that holds the
// formal, an indexed slot that holds it, or a type assertion of any of
// these. It is the single callee-resolution authority shared by the
// alias pass, the invocation walk, and the composition.
func (pf *pageFlow) slotOfExpr(st *stmtState, fs *funcSummary, e ast.Expr) (int, bool) {
	switch t := unparen(e).(type) {
	case *ast.Ident:
		obj := pf.pc.info.ObjectOf(t)
		if obj == nil {
			return 0, false
		}
		if idx, ok := st.params[obj]; ok {
			return idx, true
		}
		if idx, ok := fs.paramAliases[obj]; ok {
			return idx, true
		}
		if al, ok := fs.callbackAliases[obj]; ok {
			return al.slot, true
		}
		// A func-typed range variable over a callback container (for
		// _, cb := range cbs): the loop value is one of the container's
		// elements, so it resolves to the container's slot.
		if rv, ok := fs.rangeVars[obj]; ok {
			if idx, ok := pf.slotOfExpr(st, fs, rv); ok {
				return idx, true
			}
			// A local composite container (fns := []func{fn, fn}) has
			// no direct slot: any recorded element holding a formal
			// makes every element a possible callback.
			if id, isID := unparen(rv).(*ast.Ident); isID {
				if robj := pf.pc.info.ObjectOf(id); robj != nil {
					for k, v := range fs.indexAliases {
						if k.root == robj {
							return v, true
						}
					}
				}
			}
		}
	case *ast.SelectorExpr:
		if key, ok := pf.fieldSlotKeyOf(pf.pc.info, t); ok {
			for _, r := range fs.fieldAliases[key] {
				if !r.forwarded {
					return r.slot, true
				}
			}
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		if key, ok := indexSlotKeyOf(pf.pc.info, t); ok {
			if idx, ok := fs.indexAliases[key]; ok {
				return idx, true
			}
			if key.index != "" {
				if idx, ok2 := fs.indexAliases[indexSlotKey{root: key.root, path: key.path}]; ok2 {
					return idx, true
				}
			}
			// An indexed element of a func-typed FORMAL container
			// (cbs[i] with cbs ...func): the element is one of the
			// container's callbacks, so the callee slot is the
			// container's own slot.
			if v, isVar := key.root.(*types.Var); isVar && elemFuncType(v.Type()) {
				if idx, ok := st.params[key.root]; ok {
					return idx, true
				}
				if idx, ok := fs.paramAliases[key.root]; ok {
					return idx, true
				}
				if al, ok := fs.callbackAliases[key.root]; ok {
					return al.slot, true
				}
			}
		}
	case *ast.TypeAssertExpr:
		return pf.slotOfExpr(st, fs, t.X)
	case *ast.CompositeLit:
		// A func-container composite literal ([]func{fn, fn}) assembles
		// the container from its elements: a composite whose element
		// resolves to the store formal is a container of the callback
		// and resolves to the same slot (the rules-side callbackSlotOf
		// agrees on the shape). Any element can be selected by an
		// index, so one formal-bound element is enough for the
		// container to be a possible callback holder.
		if elemFuncType(pf.pc.info.TypeOf(t)) {
			for _, el := range t.Elts {
				if idx, ok := pf.slotOfExpr(st, fs, el); ok {
					return idx, true
				}
			}
		}
	case *ast.CallExpr:
		// A conversion (any(fn), (cbSig)(fn), any(s.cb)) is an identity
		// on the bound value: the callback slot survives it. A real
		// function call result resolves through the callee summary's
		// return records at the invocation and composition sites, not
		// here.
		if isConversionCallExpr(pf.pc.info, t) {
			return pf.slotOfExpr(st, fs, t.Args[0])
		}
	}
	return 0, false
}

// setFieldAlias records a carrier field binding on a summary. The same
// field key may be bound at several nesting depths in one function
// (h := car{fn} and o := outer{in: car{fn}} both create a {car,"cb"}
// key): EVERY distinct record is kept, because the flat record matches
// a helper parameter declared as the leaf type while the nested record
// matches one declared as the OUTER carrier type, and dropping either
// silences one enforcement direction. Identical records dedupe so the
// fixpoint stabilizes.
func (pf *pageFlow) setFieldAlias(target *funcSummary, key fieldSlotKey, rec fieldSlotAlias) {
	if target.fieldAliases == nil {
		target.fieldAliases = map[fieldSlotKey][]fieldSlotAlias{}
	}
	for _, cur := range target.fieldAliases[key] {
		if cur == rec {
			return
		}
	}
	target.fieldAliases[key] = append(target.fieldAliases[key], rec)
}

// seedStructComposite records formal-bound fields of a struct
// composite literal onto the target summary: h := car{cb: fn} and
// expression-position carriers (runCar(car{cb: fn}, ...), composites
// returned from helpers) bind field cb of the literal type to the
// formal slot, so local callee resolution and the cross-function
// composition see the carrier.
func (pf *pageFlow) seedStructComposite(st *stmtState, target *funcSummary, lit *ast.CompositeLit) {
	pf.seedStructCompositeAt(st, target, lit, "")
}

// seedCarrierFieldOfReceiver seeds the leaf record for a carrier field
// read off a receiver/arg expression (get() returning h.p): when the
// expression is a composite literal (or a local bound to one) whose
// field holds a nested carrier composite, the child seeds under the
// given path step.
func (pf *pageFlow) seedCarrierFieldOfReceiver(st *stmtState, target *funcSummary, root ast.Expr, field string, leafStep string) {
	if root == nil {
		return
	}
	cv2, ok := pf.fieldBoundValue(st, root, field)
	if !ok {
		return
	}
	cv2u := unparen(cv2)
	if ue, isUe := cv2u.(*ast.UnaryExpr); isUe && ue.Op == token.AND {
		cv2u = unparen(ue.X)
	}
	if child, isLit := cv2u.(*ast.CompositeLit); isLit {
		pf.seedStructCompositeAt(st, target, child, leafStep)
	}
}

// seedStructCompositeAt records formal-bound fields of a struct
// composite literal onto the target summary, recursing into nested
// struct composites (o := outer{in: car{cb: fn}} seeds the leaf key
// {car,"cb"} with the path "<outer>.in"). path names the steps from
// the CARRIER ROOT (the value the caller will hand to a helper) down
// to this literal's host type, each step "<canonical host type>.<field>"
// joined by '/'; leaf records carry it so the cross-function
// composition can match a callee parameter declared as the outer
// carrier type while the callback field sits deeper inside it. A field
// value that is a local holding a struct composite resolves through
// its definition the same way (o := outerJL{in: x} with x :=
// carJ1{cb: fn}).
func (pf *pageFlow) seedStructCompositeAt(st *stmtState, target *funcSummary, lit *ast.CompositeLit, path string) {
	t := pf.pc.info.TypeOf(lit)
	if t == nil {
		return
	}
	k := pf.canonFieldType(t)
	if k == "" {
		return
	}
	// Positional elements resolve by declaration order against the
	// composite's struct type; the keyed spelling resolves by field
	// name. Both seed the same canonical field-holder records, so a
	// carrier literal spelled h := car{fn} is as visible to the fence
	// as h := car{cb: fn}. Non-struct composites (arrays/slices/maps)
	// have no field slot and stay in the indexAliases machinery.
	var styp *types.Struct
	switch u := types.Unalias(t).(type) {
	case *types.Struct:
		styp = u
	case *types.Named:
		// A defined struct type (type h hcar struct{...}) is a Named;
		// positional elements must resolve through its underlying
		// struct, or Fix-B seeding silently no-ops for named carriers.
		if su, ok := u.Underlying().(*types.Struct); ok {
			styp = su
		}
	}
	for i, el := range lit.Elts {
		var field string
		var val ast.Expr
		if kv, isKV := el.(*ast.KeyValueExpr); isKV {
			fid, isID := unparen(kv.Key).(*ast.Ident)
			if !isID {
				continue
			}
			field, val = fid.Name, kv.Value
		} else {
			if styp == nil || i >= styp.NumFields() {
				continue
			}
			field, val = styp.Field(i).Name(), el
		}
		if val == nil {
			continue
		}
		if slot, ok := pf.slotOfExpr(st, target, val); ok {
			key := fieldSlotKey{typ: k, field: field}
			pf.setFieldAlias(target, key, fieldSlotAlias{slot: slot, path: path})
		}
		cv := unparen(val)
		if ue, isUe := cv.(*ast.UnaryExpr); isUe && ue.Op == token.AND {
			// Pointer-element carriers (w := pw{p: &pn{cb: fn}}): the
			// pointee composite seeds the leaf record with the same
			// path step, so a consumer reading w.p.cb matches the
			// outer-typed helper parameter.
			cv = unparen(ue.X)
		}
		if child, isLit := cv.(*ast.CompositeLit); isLit {
			pf.seedStructCompositeAt(st, target, child, pf.joinPath(path, k, field))
			continue
		}
		// A local holding a struct composite (o := outerJL{in: x} with
		// x := carJ1{cb: fn}): seed through the local's definition.
		if id, isID := unparen(val).(*ast.Ident); isID {
			obj, ok := pf.pc.info.ObjectOf(id).(*types.Var)
			if !ok || st == nil || st.fd == nil || st.fd.Body == nil {
				continue
			}
			init, single, taken := varDefOf(pf.pc.info, st.fd.Body, obj)
			if !single || taken || init == nil {
				continue
			}
			iv := unparen(init)
			if ue, isUe := iv.(*ast.UnaryExpr); isUe && ue.Op == token.AND {
				iv = unparen(ue.X)
			}
			if child, isLit := iv.(*ast.CompositeLit); isLit {
				pf.seedStructCompositeAt(st, target, child, pf.joinPath(path, k, field))
			}
			continue
		}
		// A field value produced by a call (w := pw21{p: mkP21(fn)} or
		// w := pw23{p: (&holder23{p: &pn23{cb: fn}}).get()}): the
		// callee's return carrier records seed the leaf under this
		// field's path step. A param-sourced record binds the call's
		// argument at the callee slot; a receiver-field record (srcRead)
		// resolves the receiver composite's field value visible at this
		// call site.
		if call, isCall := unparen(val).(*ast.CallExpr); isCall {
			cfs := pf.calleeSummaryOfCall(st, call)
			if cfs == nil {
				continue
			}
			leafStep := pf.joinPath(path, k, field)
			for _, rc := range cfs.returnCarrierFields {
				if rc.param == -2 {
					continue
				}
				if rc.param >= 0 {
					argExpr, ok := pf.callArgAtSlot(call, rc.param)
					if !ok {
						continue
					}
					slot, ok := pf.slotOfExpr(st, target, argExpr)
					if !ok {
						continue
					}
					pf.setFieldAlias(target, rc.field, fieldSlotAlias{slot: slot, path: leafStep})
					continue
				}
				if rc.srcRead {
					pf.seedCarrierFieldOfReceiver(st, target, call, rc.srcKey.field, leafStep)
				}
			}
			// A receiver/param FIELD read returned directly (get()
			// returning h.p): the callee's returnFieldKeys name the
			// field the result came from. Resolve that field against
			// the call's receiver (method) or first argument (free
			// function) and seed the composite it holds, exactly like
			// an srcRead carrier record.
			if fk, ok := cfs.returnFieldKeys[0]; ok && fk != multiReturnKey {
				if len(cfs.returnCarrierFields) == 0 || funcSignature(pf.pc.info.TypeOf(unparen(call.Fun))) == nil {
					var root ast.Expr
					if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
						if rsel, isSel := pf.pc.info.Selections[sel]; isSel && rsel.Kind() == types.MethodVal {
							root = sel.X
						}
					}
					if root == nil {
						if len(call.Args) > 0 {
							root = call.Args[0]
						}
					}
					if root != nil {
						pf.seedCarrierFieldOfReceiver(st, target, root, fk.field, leafStep)
					}
				}
			}
			continue
		}
	}
}

// joinPath appends one struct step ("<canonical host type>.<field>")
// to a carrier path, outer-to-inner order.
func (pf *pageFlow) joinPath(path, k, field string) string {
	if path != "" {
		path += "/"
	}
	return path + k + "." + field
}

// callResultAlias resolves a func-typed call result (cb := f(...)) to
// the value it provably returns: a scanned callee whose return position
// 0 is one of its own func-typed parameters (passthrough(fn)), the -2
// multi-branch marker, or a struct field holding the callback formal
// (getCB returning s.cb) when the current function bound that canonical
// key. The argument at a returned parameter position is resolved
// recursively so chained helpers (cb := passthrough(passthrough(fn)))
// bottom out in the same record. Callers with an unknown body or a
// non-param return resolve to nothing.
func (pf *pageFlow) callResultAlias(st *stmtState, fs *funcSummary, x ast.Expr, seen map[*ast.CallExpr]bool) (callbackAlias, bool) {
	return pf.callResultAliasAt(st, fs, x, 0, seen)
}

// callResultAliasAt resolves RESULT SLOT slot of a func-typed call
// result (_, cb := pair(fn) binds cb to result slot 1) to the value it
// provably returns: a scanned callee whose return position slot is one
// of its own func-typed parameters (pair returning f at slot 1), the
// -2 multi-branch marker, or a struct field holding the callback
// formal (getCB returning s.cb) when the current function bound that
// canonical field key to its formal.
func (pf *pageFlow) callResultAliasAt(st *stmtState, fs *funcSummary, x ast.Expr, slot int, seen map[*ast.CallExpr]bool) (callbackAlias, bool) {
	call, ok := unparen(x).(*ast.CallExpr)
	if !ok || seen[call] {
		return callbackAlias{}, false
	}
	seen[call] = true
	fn, ok := pf.calleeExprFunc(st, call.Fun)
	if !ok || fn.Pkg() == nil || !strings.HasPrefix(fn.Pkg().Path(), moduleImportPrefix) {
		return callbackAlias{}, false
	}
	sums := pf.summaries
	if fn.Pkg().Path() != pf.path {
		sums = pf.store.pkgs[fn.Pkg().Path()]
	}
	if sums == nil {
		return callbackAlias{}, false
	}
	key := fn.Name()
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
	}
	cfs, ok := sums[key]
	if !ok {
		return callbackAlias{}, false
	}
	if p, ok := cfs.returnSlotAliases[slot]; ok {
		if p == -2 {
			return callbackAlias{slot: -2, forwarded: nil}, true
		}
		if p < 0 || p >= len(call.Args) {
			return callbackAlias{}, false
		}
		return pf.resolveFuncValue(st, fs, call.Args[p], seen)
	}
	if fk, ok := cfs.returnFieldKeys[slot]; ok && fk != multiReturnKey {
		for _, r := range fs.fieldAliases[fk] {
			if !r.forwarded {
				return callbackAlias{slot: r.slot, forwarded: nil}, true
			}
		}
		return callbackAlias{}, false
	}

	return callbackAlias{}, false
}

// calleeSummaryOfCall returns the scanned summary of a call's resolved
// module callee (free function or method), mirroring callResultAlias's
// resolution so callers can read the callee's return records.
func (pf *pageFlow) calleeSummaryOfCall(st *stmtState, call *ast.CallExpr) *funcSummary {
	fn, ok := pf.calleeExprFunc(st, call.Fun)
	if !ok || fn.Pkg() == nil || !strings.HasPrefix(fn.Pkg().Path(), moduleImportPrefix) {
		return nil
	}
	sums := pf.summaries
	if fn.Pkg().Path() != pf.path {
		sums = pf.store.pkgs[fn.Pkg().Path()]
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

// callArgAtSlot returns the argument expression bound to a callee
// parameter slot, mapping slot 0 to the receiver expression for method
// calls (mirroring the summary layout used by recordReturnCarrierCompos
// ition).
func (pf *pageFlow) callArgAtSlot(call *ast.CallExpr, slot int) (ast.Expr, bool) {
	off := 0
	if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
		if recv, isSel := pf.pc.info.Selections[sel]; isSel && recv.Kind() == types.MethodVal {
			if slot == 0 {
				return sel.X, true
			}
			off = 1
		}
	}
	ai := slot - off
	if ai < 0 || ai >= len(call.Args) {
		return nil, false
	}
	return call.Args[ai], true
}

// resolveFuncValue resolves a func-typed expression to a callback-alias
// record: an existing local alias, a formal parameter of the current
// function, a holder read or conversion, or a chained call result.
func (pf *pageFlow) resolveFuncValue(st *stmtState, fs *funcSummary, e ast.Expr, seen map[*ast.CallExpr]bool) (callbackAlias, bool) {
	src := unparen(e)
	for {
		if ta, isTa := src.(*ast.TypeAssertExpr); isTa {
			src = unparen(ta.X)
			continue
		}
		if c, isC := src.(*ast.CallExpr); isC && isConversionCallExpr(pf.pc.info, c) {
			src = unparen(c.Args[0])
			continue
		}
		break
	}
	switch s := src.(type) {
	case *ast.Ident:
		obj := pf.pc.info.ObjectOf(s)
		if obj == nil {
			return callbackAlias{}, false
		}
		if al, ok := fs.callbackAliases[obj]; ok {
			return al, true
		}
		if idx, ok := st.params[obj]; ok && funcSignature(obj.Type()) != nil {
			return callbackAlias{slot: idx, forwarded: nil}, true
		}
		if idx, ok := fs.paramAliases[obj]; ok {
			return callbackAlias{slot: idx, forwarded: nil}, true
		}
		return callbackAlias{}, false
	case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		if slot, ok := pf.slotOfExpr(st, fs, s); ok {
			return callbackAlias{slot: slot, forwarded: nil}, true
		}
		return callbackAlias{}, false
	case *ast.CallExpr:
		return pf.callResultAlias(st, fs, s, seen)
	}
	return callbackAlias{}, false
}

func (pf *pageFlow) noteCallbackAliases(st *stmtState, fs *funcSummary, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	// A local assigned more than once (or whose address is taken) may
	// stop holding the literal, so only single-assignment locals whose
	// initializer is the func literal (or an identity alias of one)
	// count as aliases.
	assignCount := map[types.Object]int{}
	addressTaken := map[types.Object]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if obj := objOf(st, lhs); obj != nil {
					assignCount[obj]++
				}
			}
		case *ast.ValueSpec:
			for _, name := range v.Names {
				if obj := pf.pc.info.ObjectOf(name); obj != nil {
					assignCount[obj]++
				}
			}
		case *ast.UnaryExpr:
			if v.Op == token.AND {
				if obj := objOf(st, v.X); obj != nil {
					addressTaken[obj] = true
				}
			}
		}
		return true
	})
	// literalParams returns the literal's parameter objects in
	// declaration order, for forwarded-position resolution and for the
	// invocation-record deferral in noteCallbackInvokes.
	litParams := map[*ast.FuncLit][]types.Object{}
	paramsOf := func(lit *ast.FuncLit) []types.Object {
		if objs, ok := litParams[lit]; ok {
			return objs
		}
		var objs []types.Object
		if lit.Type.Params != nil {
			for _, f := range lit.Type.Params.List {
				for _, name := range f.Names {
					objs = append(objs, pf.pc.info.ObjectOf(name))
				}
			}
		}
		litParams[lit] = objs
		return objs
	}
	// aliasOf resolves a callee expression to a func-typed formal slot:
	// the formal itself, a recorded local alias, or a formal parameter
	// of the current function.
	aliasOf := func(e ast.Expr) (callbackAlias, bool) {
		id, ok := unparen(e).(*ast.Ident)
		if !ok {
			return callbackAlias{}, false
		}
		if obj := pf.pc.info.ObjectOf(id); obj != nil {
			if al, ok := fs.callbackAliases[obj]; ok {
				return al, true
			}
			if idx, ok := st.params[obj]; ok {
				return callbackAlias{slot: idx}, true
			}
		}
		return callbackAlias{}, false
	}
	record := func(obj types.Object, al callbackAlias, lit *ast.FuncLit) {
		if obj == nil {
			return
		}
		// The parameter-alias record survives instability (address
		// taken, reassignment): the value MAY still hold the formal, so
		// slotOfExpr keeps resolving it and the call-site fences fail
		// closed on un-mapped views instead of losing the chain (a
		// delegated address-taken alias must not launder silently).
		if al.slot >= 0 {
			if fs.paramAliases == nil {
				fs.paramAliases = map[types.Object]int{}
			}
			fs.paramAliases[obj] = al.slot
		}
		if addressTaken[obj] || assignCount[obj] != 1 {
			return
		}
		if fs.callbackAliases == nil {
			fs.callbackAliases = map[types.Object]callbackAlias{}
		}
		fs.callbackAliases[obj] = al
	}
	// returnParamOf reports the parameter position a func literal
	// returns unchanged from its body (id := func(f F) F { return f }),
	// or -2 when different branches return different parameters. Only
	// func-typed parameters count; any other return shape means the
	// literal is not a return wrapper. Returns nested in branches are
	// visited; nested func literals are not.
	returnParamOf := func(lit *ast.FuncLit, params []types.Object) (int, bool) {
		pos := -1
		ok := false
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			if fl, isFl := n.(*ast.FuncLit); isFl && fl != lit {
				return false
			}
			ret, isRet := n.(*ast.ReturnStmt)
			if !isRet {
				return true
			}
			if len(ret.Results) != 1 {
				ok = false
				pos = -1
				return false
			}
			id, isID := unparen(ret.Results[0]).(*ast.Ident)
			if !isID {
				ok = false
				pos = -1
				return false
			}
			obj := pf.pc.info.ObjectOf(id)
			idx := -3
			for p, pobj := range params {
				if pobj != nil && pobj == obj && funcSignature(obj.Type()) != nil {
					idx = p
					break
				}
			}
			if idx < 0 {
				ok = false
				pos = -1
				return false
			}
			if pos == -1 {
				pos = idx
				ok = true
			} else if pos != idx {
				pos = -2
			}
			return true
		})
		return pos, ok
	}
	// Aliases may be declared in any block of the enclosing function
	// (if/switch/loop bodies) and as var declarations; the fence must
	// follow them wherever they are declared. Nested func literals are
	// analyzed as their own summaries, so the traversal never descends
	// into a literal body: their statements must not be attributed to
	// this function.
	collect := func(wrappers bool) {
		recordAlias := func(lhs, rhs ast.Expr) {
			if wrappers {
				lit, ok := unparen(rhs).(*ast.FuncLit)
				if !ok {
					return
				}
				dstObj := objOf(st, lhs)
				if dstObj == nil {
					return
				}
				params := paramsOf(lit)
				// A return wrapper id := func(f F) F { return f } is not
				// an invocation wrapper: calling id(x)(out, out) invokes
				// the callback bound to x, so the call's byte arguments
				// are policed like every holder invocation. Recorded
				// independently of the invocation-wrapper analysis below.
				if retPos, isWrapper := returnParamOf(lit, params); isWrapper {
					if dstObj != nil && !addressTaken[dstObj] && assignCount[dstObj] == 1 {
						if fs.returnAliases == nil {
							fs.returnAliases = map[types.Object]int{}
						}
						fs.returnAliases[dstObj] = retPos
					}
				}
				slot := -1
				forwarded := make([]bool, len(params))
				ast.Inspect(lit.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					al, ok := aliasOf(call.Fun)
					if !ok {
						return true
					}
					if slot == -1 {
						slot = al.slot
					} else if slot != al.slot {
						// A literal invoking several func formals is not a
						// single-wrapper alias; call-site views could reach
						// either formal, so the fence does not attribute
						// them.
						slot = -2
					}
					for i, arg := range call.Args {
						if i >= len(params) {
							break
						}
						aid, ok := unparen(arg).(*ast.Ident)
						if !ok {
							continue
						}
						argObj := pf.pc.info.ObjectOf(aid)
						for p, pobj := range params {
							if pobj != nil && pobj == argObj {
								forwarded[p] = true
								break
							}
						}
					}
					return true
				})
				if slot < 0 {
					return
				}
				used := false
				for _, f := range forwarded {
					if f {
						used = true
						break
					}
				}
				if !used {
					// The literal invokes the formal only with
					// non-parameter expressions: the views it forwards are
					// its own captured values, not the call-site bindings,
					// so call-site enforcement would be unsound. The
					// invocation remains visible to the body-level
					// counter-check inside the literal.
					return
				}
				record(dstObj, callbackAlias{slot: slot, forwarded: forwarded, lit: lit, litParams: params}, lit)
				return
			}
			// Identity aliases: cb := fn records the formal itself, and
			// cb2 := cb copies the existing record (the value is the same
			// closure object, so the forwarded positions are the same
			// signature positions). Type assertions bind the same
			// closure value as their base (f := fn.(T), g := s.cb.(T)),
			// and holder reads copied to a local (f := s.cb) resolve
			// through the holder records of the previous fixpoint pass.
			src := unparen(rhs)
			for {
				if ta, isTa := src.(*ast.TypeAssertExpr); isTa {
					src = unparen(ta.X)
					continue
				}
				if c, isC := src.(*ast.CallExpr); isC && isConversionCallExpr(pf.pc.info, c) {
					src = unparen(c.Args[0])
					continue
				}
				break
			}
			switch s := src.(type) {
			case *ast.Ident:
				srcObj := pf.pc.info.ObjectOf(s)
				if srcObj == nil {
					return
				}
				if al, ok := fs.callbackAliases[srcObj]; ok {
					record(objOf(st, lhs), callbackAlias{slot: al.slot, forwarded: nil, lit: al.lit, litParams: al.litParams}, al.lit)
				} else if slot, ok := st.params[srcObj]; ok && funcSignature(srcObj.Type()) != nil {
					record(objOf(st, lhs), callbackAlias{slot: slot, forwarded: nil}, nil)
				} else if slot, ok := fs.paramAliases[srcObj]; ok {
					record(objOf(st, lhs), callbackAlias{slot: slot, forwarded: nil}, nil)
				}
			case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
				if slot, ok := pf.slotOfExpr(st, fs, src); ok {
					record(objOf(st, lhs), callbackAlias{slot: slot, forwarded: nil}, nil)
				}
			case *ast.CallExpr:
				// A scanned callee returning one of its own func-typed
				// parameters unchanged (cb := passthrough(fn)) or a
				// struct field that holds the formal (cb := s.getCB())
				// binds the same closure value as the formal itself;
				// the recorded alias makes the invocation fences follow
				// it exactly like a direct alias. Chained calls resolve
				// through the same resolver.
				if al, ok := pf.callResultAlias(st, fs, src, make(map[*ast.CallExpr]bool)); ok {
					record(objOf(st, lhs), al, al.lit)
				} else if cfs := pf.calleeSummaryOfCall(st, s); cfs != nil &&
					(len(cfs.returnSlotAliases) > 0 || len(cfs.returnFieldKeys) > 0) {
					// The callee provably returns one of its own func
					// formals, but the bound argument resolved to a
					// shape (a method value/expression, an unresolved
					// local) this body cannot attribute. The invocation
					// of the result must fail closed on un-mapped views
					// instead of dropping the chain silently.
					if o := objOf(st, lhs); o != nil {
						if fs.paramAliases == nil {
							fs.paramAliases = map[types.Object]int{}
						}
						fs.paramAliases[o] = -1
					}
				}
			}
		}
		skipStmt := func(lhs []ast.Expr, rhs []ast.Expr) bool {
			for i, l := range lhs {
				if i >= len(rhs) {
					// A multi-value call result pairs by position
					// (_, cb := pair(fn) binds cb to result slot 1).
					if len(rhs) == 1 {
						if call, isCall := unparen(rhs[0]).(*ast.CallExpr); isCall {
							if al, ok := pf.callResultAliasAt(st, fs, call, i, make(map[*ast.CallExpr]bool)); ok {
								record(objOf(st, l), al, al.lit)
							}
							continue
						}
					}
					break
				}
				recordAlias(l, rhs[i])
			}
			// Do not descend: a func literal bound by this statement (or
			// nested inside its initializer expressions) is analyzed as
			// its own summary.
			return false
		}
		ast.Inspect(body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				return skipStmt(v.Lhs, v.Rhs)
			case *ast.ValueSpec:
				names := make([]ast.Expr, len(v.Names))
				for i, name := range v.Names {
					names[i] = name
				}
				return skipStmt(names, v.Values)
			case *ast.FuncLit:
				return false
			}
			return true
		})
	}
	// Literal wrappers first, then identity aliases: a chain cb2 := cb
	// copies the wrapper's record, so wrapper records must exist before
	// the identity pass resolves them.
	collect(true)
	collect(false)
}

// invokeCensus snapshots the assignment structure of a function body so
// the callback-invocation records can tell whether an expression's
// end-state value equals its value at an earlier call. The exemption in
// noteCallbackInvokes accepts a locally-minted mapped view only when
// every storage the argument reads is assigned at most once in the whole
// body, with that assignment preceding the call: a mint AFTER the call
// must not let the record-time check bless an owned buffer that already
// reached the callback.
type invokeCensus struct {
	pf *pageFlow
	// params: the enclosing function's parameter objects (receiver
	// included). Parameters are stable unless reassigned or
	// address-taken inside the body.
	params map[types.Object]bool
	// assignCount/assignPos: identifiers written anywhere in the body
	// (locals and parameters; the walk descends into nested func
	// literals, whose captured writes mutate the enclosing local).
	assignCount map[types.Object]int
	assignPos   map[types.Object]token.Pos
	// fieldCount/fieldPos: selector paths written anywhere
	// (h.Inner.Buf = v marks both "Inner" and "Inner.Buf").
	fieldCount map[string]int
	fieldPos   map[string]token.Pos
	// indexCount/indexPos: container roots written through an index
	// (xs[0] = v marks the root object of xs).
	indexCount map[types.Object]int
	indexPos   map[types.Object]token.Pos
	// derefCount/derefPos: roots written through a dereference (*p = v).
	derefCount map[types.Object]int
	derefPos   map[types.Object]token.Pos
	// addressTaken: roots whose address escapes into storage the scan
	// cannot follow (&x, &h.Buf), so their values can change behind the
	// expression's back.
	addressTaken map[types.Object]bool
	// bodyDeclared: objects declared inside the body (locals, nested
	// block variables, literal parameters). Package-level and captured
	// outer values are never position-stable for the exemption.
	bodyDeclared map[types.Object]bool
	// litParams: func-literal parameters declared inside the body; they
	// bind like function parameters (stable unless reassigned).
	litParams map[types.Object]bool
}

func newInvokeCensus(pf *pageFlow, st *stmtState, body *ast.BlockStmt) *invokeCensus {
	c := &invokeCensus{
		pf:           pf,
		params:       map[types.Object]bool{},
		assignCount:  map[types.Object]int{},
		assignPos:    map[types.Object]token.Pos{},
		fieldCount:   map[string]int{},
		fieldPos:     map[string]token.Pos{},
		indexCount:   map[types.Object]int{},
		indexPos:     map[types.Object]token.Pos{},
		derefCount:   map[types.Object]int{},
		derefPos:     map[types.Object]token.Pos{},
		addressTaken: map[types.Object]bool{},
		bodyDeclared: map[types.Object]bool{},
		litParams:    map[types.Object]bool{},
	}
	for obj := range st.params {
		c.params[obj] = true
	}
	// Declared objects are collected by walking the body AST (go/types
	// does not key every block scope by an in-body node, so the scope
	// map cannot be filtered reliably): short-variable declarations,
	// var declarations, range variables, and func-literal parameters
	// are all declared inside the body.
	declared := func(e ast.Expr) {
		id, ok := unparen(e).(*ast.Ident)
		if !ok {
			return
		}
		if obj := pf.pc.info.ObjectOf(id); obj != nil {
			c.bodyDeclared[obj] = true
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			if v.Tok == token.DEFINE {
				for _, lhs := range v.Lhs {
					declared(lhs)
				}
			}
		case *ast.ValueSpec:
			for _, name := range v.Names {
				c.bodyDeclared[pf.pc.info.ObjectOf(name)] = true
			}
		case *ast.RangeStmt:
			if v.Tok == token.DEFINE {
				declared(v.Key)
				declared(v.Value)
			}
		case *ast.FuncLit:
			// The literal's own parameters bind like function
			// parameters: stable unless reassigned inside the literal.
			if v.Type.Params != nil {
				for _, f := range v.Type.Params.List {
					for _, name := range f.Names {
						if obj := pf.pc.info.ObjectOf(name); obj != nil {
							c.bodyDeclared[obj] = true
							c.litParams[obj] = true
						}
					}
				}
			}
		}
		return true
	})
	var count func(pos token.Pos, e ast.Expr)
	count = func(pos token.Pos, e ast.Expr) {
		switch t := unparen(e).(type) {
		case *ast.Ident:
			if obj := pf.pc.info.ObjectOf(t); obj != nil {
				c.assignCount[obj]++
				if c.assignCount[obj] == 1 {
					c.assignPos[obj] = pos
				}
			}
		case *ast.SelectorExpr:
			for _, key := range c.fieldKeys(t) {
				c.fieldCount[key]++
				if c.fieldCount[key] == 1 {
					c.fieldPos[key] = pos
				}
			}
			count(pos, t.X)
		case *ast.IndexExpr:
			if obj := c.rootOf(t.X); obj != nil {
				c.indexCount[obj]++
				if c.indexCount[obj] == 1 {
					c.indexPos[obj] = pos
				}
			}
			count(pos, t.X)
		case *ast.StarExpr:
			if obj := c.rootOf(t.X); obj != nil {
				c.derefCount[obj]++
				if c.derefCount[obj] == 1 {
					c.derefPos[obj] = pos
				}
			}
			count(pos, t.X)
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				count(v.Pos(), lhs)
			}
		case *ast.ValueSpec:
			for _, name := range v.Names {
				count(v.Pos(), name)
			}
		case *ast.IncDecStmt:
			count(v.Pos(), v.X)
		case *ast.UnaryExpr:
			if v.Op == token.AND {
				if obj := c.rootOf(v.X); obj != nil {
					c.addressTaken[obj] = true
				}
			}
		}
		return true
	})
	return c
}

// fieldKeys returns the selector-path keys of a selector expression in
// outer-to-inner order (h.Inner.Buf -> ["Inner", "Inner.Buf"]), so a
// write to a whole field blocks an argument reading a path beneath it,
// and a write to a nested field blocks an argument reading the whole
// field or the nested path.
func (c *invokeCensus) fieldKeys(sel *ast.SelectorExpr) []string {
	var inner []string
	for e := ast.Expr(sel); ; {
		s, ok := unparen(e).(*ast.SelectorExpr)
		if !ok {
			break
		}
		inner = append(inner, s.Sel.Name)
		e = s.X
	}
	var keys []string
	key := ""
	for i := len(inner) - 1; i >= 0; i-- {
		if key == "" {
			key = inner[i]
		} else {
			key = key + "." + inner[i]
		}
		keys = append(keys, key)
	}
	return keys
}

// rootOf resolves the root identifier object of an expression chain,
// digging through selectors, indexes, dereferences, slices, and type
// assertions to the base object (h.Items[0].Data -> h).
func (c *invokeCensus) rootOf(e ast.Expr) types.Object {
	for {
		switch t := unparen(e).(type) {
		case *ast.SelectorExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.StarExpr:
			e = t.X
		case *ast.SliceExpr:
			e = t.X
		case *ast.TypeAssertExpr:
			e = t.X
		default:
			if id, ok := t.(*ast.Ident); ok {
				return c.pf.pc.info.ObjectOf(id)
			}
			return nil
		}
	}
}

// stable reports whether an argument expression's end-state value
// equals its value at the invocation. analyzeStmts has already finished
// before noteCallbackInvokes runs, so the end-state snapshot is the only
// value available; every storage the expression reads must be assigned
// at most once in the whole body, with that assignment before the call,
// or the mappedness decision made here is not the one the call saw.
func (c *invokeCensus) stable(e ast.Expr, callPos token.Pos) bool {
	ok := true
	ast.Inspect(e, func(n ast.Node) bool {
		if !ok {
			return false
		}
		switch t := n.(type) {
		case *ast.Ident:
			if !c.identStable(t, callPos) {
				ok = false
				return false
			}
		case *ast.SelectorExpr:
			for _, key := range c.fieldKeys(t) {
				if cnt := c.fieldCount[key]; cnt > 1 || (cnt == 1 && c.fieldPos[key] >= callPos) {
					ok = false
					return false
				}
			}
		case *ast.IndexExpr:
			if obj := c.rootOf(t.X); obj != nil {
				if cnt := c.indexCount[obj]; cnt > 1 || (cnt == 1 && c.indexPos[obj] >= callPos) {
					ok = false
					return false
				}
			}
		case *ast.StarExpr:
			if obj := c.rootOf(t.X); obj != nil {
				if cnt := c.derefCount[obj]; cnt > 1 || (cnt == 1 && c.derefPos[obj] >= callPos) {
					ok = false
					return false
				}
			}
		}
		return true
	})
	return ok
}

// identStable reports whether one identifier's value is the same at the
// call site and at the end-state snapshot. Constants, builtins, type
// names, and package qualifiers are position-independent. Parameters
// are stable unless reassigned or address-taken in the body. Locals
// are stable only when declared in the body, assigned exactly once
// (their definition), not address-taken, and defined before the call;
// everything else falls back to fail-closed.
func (c *invokeCensus) identStable(id *ast.Ident, callPos token.Pos) bool {
	if id.Name == "_" {
		return true
	}
	obj := c.pf.pc.info.ObjectOf(id)
	if obj == nil {
		return false
	}
	switch obj.(type) {
	case *types.Builtin, *types.Const, *types.TypeName, *types.PkgName:
		return true
	}
	if c.params[obj] || c.litParams[obj] {
		return c.assignCount[obj] == 0 && !c.addressTaken[obj]
	}
	return c.bodyDeclared[obj] && c.assignCount[obj] == 1 && !c.addressTaken[obj] &&
		c.assignPos[obj] != 0 && c.assignPos[obj] < callPos
}

// noteCallbackInvokes records byte-slice parameters the body passes to
// each func-typed formal parameter it invokes (fn(x, y) with fn a func
// formal and x, y byte parameters). The invocation is the
// definition-site side of the store-callback fence: whether the views
// that reach the callback are mapped is decided by the call sites that
// bind the func formal, so the byte-parameter slots (and the
// untraceable mark) are carried through call chains exactly like
// copyParams. Aliases of a formal (cb := fn, or a func literal wrapping
// it) count as the formal; a wrapper literal's OWN parameters are
// deferred to the composition at the call sites that bind the literal,
// because only there are the views known.
func (pf *pageFlow) noteCallbackInvokes(st *stmtState, fs *funcSummary, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	census := newInvokeCensus(pf, st, body)
	// fs.paramAliases is the per-pass recording of locals bound to a
	// func-typed formal slot; the assignment walk below records it,
	// callee resolution reads it, and the composition resolves through
	// it like the alias and holder records.
	if fs.paramAliases == nil {
		fs.paramAliases = map[types.Object]int{}
	}
	// recordSlot stores a bound slot under an LHS: an identifier local
	// (paramAliases, plus a guarded callbackAlias so cross-function
	// composition sees it), a struct field, or an indexed container
	// slot.
	recordSlot := func(lhs ast.Expr, idx int) {
		switch l := unparen(lhs).(type) {
		case *ast.Ident:
			obj := objOf(st, l)
			if obj == nil {
				return
			}
			fs.paramAliases[obj] = idx
			// The asserted holder copied to a local must be visible to
			// the composition at helper call sites; the census guards
			// match record() in noteCallbackAliases (single assignment,
			// no address taken).
			if census.assignCount[obj] == 1 && !census.addressTaken[obj] {
				if fs.callbackAliases == nil {
					fs.callbackAliases = map[types.Object]callbackAlias{}
				}
				fs.callbackAliases[obj] = callbackAlias{slot: idx}
			}
		case *ast.SelectorExpr:
			// h.f = fn, and t.m.cb = fn: the field (or nested leaf
			// field) holds the formal; a later invocation carries the
			// formal slot. Nested destinations record the steps from
			// the destination ROOT down to the leaf's host struct
			// (t.m.cb = fn records {mL,"cb"} with the path "<tL>.m")
			// so the cross-function composition matches a callee
			// parameter declared as the outer type. Only plain field
			// selections count (method-value writes cannot name a
			// storage slot). The canonical type key lets a caller's
			// anonymous struct value match a helper's named parameter
			// carrying the same field across functions.
			if key, ok := pf.fieldSlotKeyOf(pf.pc.info, l); ok {
				path := ""
				for e := unparen(l.X); ; {
					sel, isSel := unparen(e).(*ast.SelectorExpr)
					if !isSel {
						break
					}
					if k2, ok := pf.fieldSlotKeyOf(pf.pc.info, sel); ok {
						step := k2.typ + "." + k2.field
						if path == "" {
							path = step
						} else {
							path += "/" + step
						}
					}
					e = sel.X
				}
				pf.setFieldAlias(fs, key, fieldSlotAlias{slot: idx, path: path})
			}
		case *ast.IndexExpr, *ast.IndexListExpr:
			// arr[0] = fn: the indexed slot holds the formal. The
			// container root and the selector path to it make the key,
			// so s.hs[0] and hs[0] stay distinct; a non-constant index
			// records the catch-all key and any indexed call on the
			// container fails closed.
			if key, ok2 := indexSlotKeyOf(pf.pc.info, l); ok2 {
				if fs.indexAliases == nil {
					fs.indexAliases = map[indexSlotKey]int{}
				}
				fs.indexAliases[key] = idx
			}
		}
	}
	// seedComposite records formal-bound elements of an array, slice, or
	// map literal bound to a local or field: hs := []func{fn} seeds
	// slot hs[0], s.hs = []func{fn} seeds path hs element 0, and a map
	// literal seeds its constant keys.
	seedComposite := func(lhs ast.Expr, lit *ast.CompositeLit) {
		var base ast.Expr
		path := ""
		structPath := ""
		switch l := unparen(lhs).(type) {
		case *ast.Ident:
			base = l
		case *ast.SelectorExpr:
			base = l
			// o.in = car{cb: fn}: the composite lands in a nested field;
			// the leaf records carry the steps from the destination root
			// (outer-to-inner) so the cross-function composition matches
			// a callee parameter declared as the outer type.
			for e := ast.Expr(l); ; {
				sel, isSel := unparen(e).(*ast.SelectorExpr)
				if !isSel {
					break
				}
				if key, ok := pf.fieldSlotKeyOf(pf.pc.info, sel); ok {
					step := key.typ + "." + key.field
					if structPath == "" {
						structPath = step
					} else {
						structPath = step + "/" + structPath
					}
				}
				e = sel.X
			}
		default:
			return
		}
		// A struct composite literal seeds the field holder records the
		// same way an assignment does: h := car{cb: fn} binds field cb
		// of the literal's type to the formal slot, so the caller's
		// cross-function composition and the local callee resolution
		// see the carrier. A nested destination (o.in = car{cb: fn})
		// seeds the leaf with the destination steps.
		pf.seedStructCompositeAt(st, fs, lit, structPath)
		root := pf.rootObjectOf(base)
		if root == nil {
			return
		}
		for {
			b := unparen(base)
			if sel, isSel := b.(*ast.SelectorExpr); isSel {
				if path == "" {
					path = sel.Sel.Name
				} else {
					path = sel.Sel.Name + "." + path
				}
				base = sel.X
				continue
			}
			break
		}
		record := func(index string, e ast.Expr) {
			if slot, ok := pf.slotOfExpr(st, fs, e); ok {
				key := indexSlotKey{root: root, path: path, index: index}
				if fs.indexAliases == nil {
					fs.indexAliases = map[indexSlotKey]int{}
				}
				fs.indexAliases[key] = slot
				// A non-constant index dispatch (fns[i]) can name any
				// element: the catch-all key records that the container
				// holds the callback, so the store-site counter-check
				// applies to variable-index invocations exactly like the
				// constant spellings.
				if index != "" {
					fs.indexAliases[indexSlotKey{root: root, path: path}] = slot
				}
			}
		}
		pos := 0
		for _, el := range lit.Elts {
			if kv, isKV := el.(*ast.KeyValueExpr); isKV {
				key := ""
				if k, c := constIndexKey(pf.pc.info, kv.Key); c {
					key = k
				}
				record(key, kv.Value)
				pos++
				continue
			}
			record(strconv.Itoa(pos), el)
			pos++
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if i >= len(n.Rhs) {
					break
				}
				// Type assertions bind the same closure value as their
				// base: fn.(T), s.cb.(T), arr[0].(T) all resolve through
				// the base's slot when one is recorded.
				src := unparen(n.Rhs[i])
				for {
					if ta, isTa := src.(*ast.TypeAssertExpr); isTa {
						src = unparen(ta.X)
						continue
					}
					break
				}
				switch s := src.(type) {
				case *ast.CompositeLit:
					seedComposite(lhs, s)
				default:
					if idx, ok := pf.slotOfExpr(st, fs, src); ok {
						recordSlot(lhs, idx)
					}
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if i >= len(n.Values) {
					break
				}
				if lit, isLit := unparen(n.Values[i]).(*ast.CompositeLit); isLit {
					seedComposite(name, lit)
					continue
				}
				if idx, ok := pf.slotOfExpr(st, fs, n.Values[i]); ok {
					recordSlot(name, idx)
				}
			}
		}
		return true
	})
	rootSlot := func(e ast.Expr) (int, bool) {
		// Strip slice wrappers: fn(page[:]) still names page.
		for {
			if se, ok := unparen(e).(*ast.SliceExpr); ok {
				e = se.X
				continue
			}
			break
		}
		// A selector chain rooted at a parameter (v.a, s.buf, h.cb) is
		// parameter-sourced: the view inside the field is decided at the
		// call sites binding the root, exactly like the ident itself.
		// This keeps pair-carrying wrapper literals (wrap(fn, p) calling
		// cb(p.a, p.b)) traceable instead of failing the unprovable-buffer
		// mark.
		root := e
		for {
			if se, ok := unparen(root).(*ast.SelectorExpr); ok {
				root = se.X
				continue
			}
			break
		}
		id, ok := unparen(root).(*ast.Ident)
		if !ok {
			return 0, false
		}
		obj := pf.pc.info.ObjectOf(id)
		if obj == nil {
			return 0, false
		}
		if idx, ok := st.params[obj]; ok {
			return idx, true
		}
		if idx, ok := fs.paramAliases[obj]; ok {
			return idx, true
		}
		return 0, false
	}
	// Call nodes are visited in source order (children before
	// siblings), so the nearest enclosing func literal is the last
	// pushed literal whose subtree has not ended yet.
	var litStack []*ast.FuncLit
	popExpired := func(n ast.Node) {
		for len(litStack) > 0 && n.Pos() > litStack[len(litStack)-1].End() {
			litStack = litStack[:len(litStack)-1]
		}
	}
	// deferredArg reports whether e is one of the wrapping literal's own
	// parameters: such arguments are the wrapper's parameters, whose
	// views are decided at the call sites that bind the wrapper, so
	// this invocation record defers to the composition there.
	deferredArg := func(lit *ast.FuncLit, e ast.Expr) bool {
		if lit == nil {
			return false
		}
		var al callbackAlias
		found := false
		for _, cand := range fs.callbackAliases {
			if cand.lit == lit {
				al = cand
				found = true
				break
			}
		}
		if !found {
			return false
		}
		aid, ok := unparen(e).(*ast.Ident)
		if !ok {
			return false
		}
		obj := pf.pc.info.ObjectOf(aid)
		for _, pobj := range al.litParams {
			if pobj != nil && pobj == obj {
				return true
			}
		}
		return false
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		popExpired(n)
		switch v := n.(type) {
		case *ast.FuncLit:
			litStack = append(litStack, v)
			return true
		case *ast.ReturnStmt:
			// Returns inside a nested func literal belong to that
			// literal's own summary, not this function's.
			if len(litStack) > 0 {
				return true
			}
			for i, r := range v.Results {
				e := unparen(r)
				for {
					if ta, isTa := e.(*ast.TypeAssertExpr); isTa {
						e = unparen(ta.X)
						continue
					}
					if c, isC := e.(*ast.CallExpr); isC && isConversionCallExpr(pf.pc.info, c) {
						e = unparen(c.Args[0])
						continue
					}
					break
				}
				if ue, isUe := e.(*ast.UnaryExpr); isUe && ue.Op == token.AND {
					// Pointer-returned carriers (return &car{cb: fn})
					// seed the same leaf records as their pointee.
					e = unparen(ue.X)
				}
				// A struct carrier literal returned from the body:
				// mkCar(fn) returning car{cb: fn} (and nested spellings
				// return top{mid{car{cb: fn}}}).
				if lit, isLit := e.(*ast.CompositeLit); isLit {
					pf.recordReturnComposite(st, fs, i, lit, "")
					continue
				}
				// A func-typed field read returned directly: getCB()
				// returning s.cb resolves against the caller's own
				// fieldAliases at the call site.
				if sel, isSel := e.(*ast.SelectorExpr); isSel {
					if key, ok := pf.fieldSlotKeyOf(pf.pc.info, sel); ok {
						if fs.returnFieldKeys == nil {
							fs.returnFieldKeys = map[int]fieldSlotKey{}
						}
						if cur, isCur := fs.returnFieldKeys[i]; isCur && cur != key {
							fs.returnFieldKeys[i] = multiReturnKey
						} else {
							fs.returnFieldKeys[i] = key
						}
						continue
					}
				}
				// A struct CARRIER returned through a callee's call
				// result (mk24(fn) { return mk24b(fn) }): forward the
				// callee's return carrier records with the call's
				// argument positions bound to this function's own
				// parameter slots, so callers of THIS function compose
				// the leaf records without seeing the callee's frame.
				if call, isCall := e.(*ast.CallExpr); isCall {
					if cfs := pf.calleeSummaryOfCall(st, call); cfs != nil && len(cfs.returnCarrierFields) > 0 {
						for slot, rc := range cfs.returnCarrierFields {
							nrc := rc
							if rc.param == -2 {
								// keep the fail-closed marker below
							} else if rc.param >= 0 {
								argExpr, ok := pf.callArgAtSlot(call, rc.param)
								if !ok {
									continue
								}
								sp, ok := pf.slotOfExpr(st, fs, argExpr)
								if !ok {
									continue
								}
								nrc.param = sp
							} else {
								// srcRead records (a receiver-field
								// return) do not survive a further hop:
								// the receiver is only visible at the
								// first call site. Fail closed instead.
								nrc.param = -2
							}
							if fs.returnCarrierFields == nil {
								fs.returnCarrierFields = map[int]returnCarrierField{}
							}
							if cur, isCur := fs.returnCarrierFields[slot]; isCur && cur != nrc {
								fs.returnCarrierFields[slot] = returnCarrierField{field: nrc.field, param: -2, path: nrc.path}
							} else {
								fs.returnCarrierFields[slot] = nrc
							}
						}
					}
					// A call returned unchanged: h2(g) { return h1(g) }
					// forwards one of the called helper's OWN return
					// positions. Resolve EAGERLY against this function's
					// own frame (the call's arguments are its
					// expressions): once the called helper's return
					// summary has settled (a later fixpoint pass), the
					// result records the parameter slot of THIS
					// function, which the callers resolve without
					// seeing the callee's frame.
					al, alok := pf.callResultAlias(st, fs, call, make(map[*ast.CallExpr]bool))
					if alok {
						if fs.returnSlotAliases == nil {
							fs.returnSlotAliases = map[int]int{}
						}
						if cur, isCur := fs.returnSlotAliases[i]; isCur && cur != al.slot {
							fs.returnSlotAliases[i] = -2
						} else {
							fs.returnSlotAliases[i] = al.slot
						}
						continue
					}
					continue
				}
				// A func-typed container read of a parameter returned
				// unchanged (return f[0] of a variadic func parameter):
				// the result is the parameter's func value.
				if ix, isIx := e.(*ast.IndexExpr); isIx {
					if id, isID := unparen(ix.X).(*ast.Ident); isID {
						if pobj, ok := pf.pc.info.ObjectOf(id).(*types.Var); ok {
							isF := funcSignature(pobj.Type()) != nil
							if !isF {
								if sl, ok := types.Unalias(pobj.Type()).(*types.Slice); ok {
									isF = funcSignature(sl.Elem()) != nil
								}
							}
							if isF {
								if idx, ok2 := st.params[pobj]; ok2 {
									if fs.returnSlotAliases == nil {
										fs.returnSlotAliases = map[int]int{}
									}
									if cur, isCur := fs.returnSlotAliases[i]; isCur && cur != idx {
										fs.returnSlotAliases[i] = -2
									} else {
										fs.returnSlotAliases[i] = idx
									}
									continue
								}
							}
						}
					}
				}
				// A func-typed parameter (or recorded alias of one)
				// returned unchanged: func id(f F) F { return f } as a
				// scanned function.
				if slot, ok := pf.slotOfExpr(st, fs, e); ok {
					if fs.returnSlotAliases == nil {
						fs.returnSlotAliases = map[int]int{}
					}
					if cur, isCur := fs.returnSlotAliases[i]; isCur && cur != slot {
						fs.returnSlotAliases[i] = -2
					} else {
						fs.returnSlotAliases[i] = slot
					}
				}
			}
			return true
		case *ast.CallExpr:
			// Resolve the callee to a func-typed formal slot: the formal
			// itself, a recorded local or assertion alias, a struct
			// field or indexed slot assigned the formal (h.f = fn,
			// arr[0] = fn), a type assertion of any of these
			// (s.cb.(T)(a, b)), or the result of a return wrapper
			// (id := func(f F) F { return f }; id(x)(a, b) invokes x).
			// When the callee names a struct FIELD the body itself cannot
			// resolve to a local formal (h.cb with h a parameter or a
			// carrier), the invocation is still recorded under the
			// canonical field key: a caller that bound the field to the
			// store callback formal composes the record into its own
			// call-site fence (recordFieldAliasComposition).
			var fnSlot int
			var fk fieldSlotKey
			var isFieldCall bool
			var sig *types.Signature
			fun := unparen(v.Fun)
			if _, isCall := fun.(*ast.CallExpr); isCall {
				// Return wrapper: the inner call's result is the
				// callback bound to the returned parameter position.
				ic := unparen(v.Fun).(*ast.CallExpr)
				id, ok := unparen(ic.Fun).(*ast.Ident)
				if !ok {
					return true
				}
				obj := pf.pc.info.ObjectOf(id)
				if obj == nil {
					return true
				}
				pos, ok := fs.returnAliases[obj]
				if !ok || pos < 0 || pos >= len(ic.Args) {
					return true
				}
				fnSlot, ok = pf.slotOfExpr(st, fs, ic.Args[pos])
				if !ok {
					return true
				}
			} else {
				var ok bool
				fnSlot, ok = pf.slotOfExpr(st, fs, fun)
				if !ok {
					fk, isFieldCall = pf.fieldCalleeKey(pf.pc.info, v.Fun)
					if !isFieldCall {
						return true
					}
				}
			}
			if sig = pf.funcTypeOf(fun); sig == nil {
				return true
			}
			if sig == nil || sig.Params() == nil {
				return true
			}
			params := sig.Params()
			nargs := params.Len()
			if nargs > len(v.Args) {
				nargs = len(v.Args)
			}
			internal := false
			var slots []int
			var litCtx *ast.FuncLit
			if len(litStack) > 0 {
				litCtx = litStack[len(litStack)-1]
			}
			for i := 0; i < nargs; i++ {
				pt := types.Unalias(params.At(i).Type())
				if _, isSlice := pt.(*types.Slice); !isSlice {
					if _, isTP := pt.(*types.TypeParam); !isTP {
						continue
					}
					// A type-parameter byte view (f(v) with f
					// func(T) T in a generic wrapper): only the
					// concrete instantiation decides whether v is
					// bytes, so the argument is recorded like a traced
					// byte slot and the call-site fences test the
					// instantiated view's mappedness. Without this,
					// gApply(func(b []byte) []byte {...}, page) keeps
					// the copying closure invisible to every fence.
				}
				if deferredArg(litCtx, v.Args[i]) {
					// The wrapper literal's own parameter: the call
					// sites binding the wrapper decide its mappedness.
					continue
				}
				slot, traced := rootSlot(v.Args[i])
				if !traced {
					// A byte view the body mints locally (a mapped page from
					// the mapping owner, a reader.page result) is provably
					// mapped at the definition site and stays honest;
					// anything else is not provably the caller's mapped view
					// and fails closed at the store-implementation call site.
					// The end-state snapshot only speaks for storage assigned at
					// most once BEFORE the invocation: a trailing mint after the
					// call must not exempt an owned buffer that already reached
					// the callback.
					pv := pf.snapshotEvalExpr(st, v.Args[i])
					if pv.mapped && census.stable(v.Args[i], v.Pos()) {
						continue
					}
					internal = true
					continue
				}
				dup := false
				if !isFieldCall {
					for _, prev := range fs.callbackInvokes[fnSlot] {
						if prev == slot {
							dup = true
							break
						}
					}
				} else {
					for _, prev := range fs.fieldInvokes[fk] {
						if prev == slot {
							dup = true
							break
						}
					}
				}
				if !dup {
					slots = append(slots, slot)
				}
			}
			if !isFieldCall {
				if len(slots) > 0 {
					if fs.callbackInvokes == nil {
						fs.callbackInvokes = map[int][]int{}
					}
					fs.callbackInvokes[fnSlot] = append(fs.callbackInvokes[fnSlot], slots...)
				}
				if internal {
					if fs.callbackInvokesInternal == nil {
						fs.callbackInvokesInternal = map[int]bool{}
					}
					fs.callbackInvokesInternal[fnSlot] = true
				}
			} else {
				if len(slots) > 0 {
					if fs.fieldInvokes == nil {
						fs.fieldInvokes = map[fieldSlotKey][]int{}
					}
					fs.fieldInvokes[fk] = append(fs.fieldInvokes[fk], slots...)
				}
				if internal {
					if fs.fieldInvokesInternal == nil {
						fs.fieldInvokesInternal = map[fieldSlotKey]bool{}
					}
					fs.fieldInvokesInternal[fk] = true
				}
			}
		}
		return true
	})
}

// recordCopyParamComposition forwards a callee's copy-parameter pairs
// through the current function when both slots bind the current
// function's own parameters: F(p1, p2) calling G(p1, p2) where G copies
// p2 into p1 means F's callers bind the same owned/mapped decision, so
// F's summary carries the pair onward.
// storeCallbackMethod reports whether the method name belongs to the
// module store callback surface (Rust Store::inspect_page/update_page/
// copy_page). Only these methods hand a complete mapped page view to a
// caller-supplied function.
func storeCallbackMethod(name string) bool {
	return name == "Inspect" || name == "Update" || name == "CopyPage"
}

func (pf *pageFlow) recordCopyParamComposition(st *stmtState, call *ast.CallExpr, fs *funcSummary, recvExpr ast.Expr, argOff int) {
	if st.activeFS == nil || fs == nil || len(fs.copyParams) == 0 {
		return
	}
	slotExpr := func(slot int) ast.Expr {
		if recvExpr != nil && slot == 0 {
			return recvExpr
		}
		ai := slot - argOff
		if ai < 0 || ai >= len(call.Args) {
			return nil
		}
		return call.Args[ai]
	}
	paramSlot := func(e ast.Expr) (int, bool) {
		if e == nil {
			return 0, false
		}
		for {
			if se, ok := unparen(e).(*ast.SliceExpr); ok {
				e = se.X
				continue
			}
			break
		}
		id, ok := unparen(e).(*ast.Ident)
		if !ok {
			return 0, false
		}
		obj := pf.pc.info.ObjectOf(id)
		if obj == nil {
			return 0, false
		}
		idx, ok := st.params[obj]
		return idx, ok
	}
	for d, srcs := range fs.copyParams {
		dd, dok := paramSlot(slotExpr(d))
		if !dok {
			continue
		}
		for _, s := range srcs {
			ss, sok := paramSlot(slotExpr(s))
			if !sok {
				continue
			}
			if st.activeFS.copyParams == nil {
				st.activeFS.copyParams = map[int][]int{}
			}
			dup := false
			for _, prev := range st.activeFS.copyParams[dd] {
				if prev == ss {
					dup = true
					break
				}
			}
			if !dup {
				st.activeFS.copyParams[dd] = append(st.activeFS.copyParams[dd], ss)
			}
		}
	}
}

// recordCallbackInvokeComposition forwards a callee's callback-invocation
// records through the current function when the func-typed slot binds
// the current function's own func-typed formal: F(fn, a, b) calling
// G(fn, a, b) where G invokes fn with its byte parameters means F's
// summary carries the record onward, so the store-callback fence at the
// store-implementation call site still sees the invocation. A byte slot
// that does not bind a current-function parameter has views the call
// site cannot prove mapped, and the record turns internal.
func (pf *pageFlow) recordCallbackInvokeComposition(st *stmtState, call *ast.CallExpr, fs *funcSummary, recvExpr ast.Expr, argOff int) {
	if st.activeFS == nil || fs == nil || (len(fs.callbackInvokes) == 0 && len(fs.callbackInvokesInternal) == 0) {
		return
	}
	slotExpr := func(slot int) ast.Expr {
		if recvExpr != nil && slot == 0 {
			return recvExpr
		}
		ai := slot - argOff
		if ai < 0 || ai >= len(call.Args) {
			return nil
		}
		return call.Args[ai]
	}
	paramObj := func(e ast.Expr) (types.Object, bool) {
		if e == nil {
			return nil, false
		}
		for {
			if se, ok := unparen(e).(*ast.SliceExpr); ok {
				e = se.X
				continue
			}
			break
		}
		id, ok := unparen(e).(*ast.Ident)
		if !ok {
			return nil, false
		}
		obj := pf.pc.info.ObjectOf(id)
		if obj == nil {
			return nil, false
		}
		if _, ok := st.params[obj]; !ok {
			return nil, false
		}
		return obj, true
	}
	recordInternal := func(slot int) {
		if st.activeFS.callbackInvokesInternal == nil {
			st.activeFS.callbackInvokesInternal = map[int]bool{}
		}
		st.activeFS.callbackInvokesInternal[slot] = true
	}
	// resolveFnIdx maps the callee's func-typed slot to the current
	// function's summary slot: the slot binds the current function's
	// own func-typed formal directly, or a local alias, a struct field
	// or indexed holder, or an assertion of any of these, through the
	// shared slotOfExpr resolver.
	resolveFnIdx := func(slot int) (int, bool) {
		se := slotExpr(slot)
		if se == nil {
			return 0, false
		}
		for {
			if sl, ok := unparen(se).(*ast.SliceExpr); ok {
				se = sl.X
				continue
			}
			break
		}
		return pf.slotOfExpr(st, st.activeFS, se)
	}
	for fnSlot := range fs.callbackInvokes {
		fnIdx, ok := resolveFnIdx(fnSlot)
		if !ok {
			continue
		}
		if fs.callbackInvokesInternal[fnSlot] {
			recordInternal(fnIdx)
		}
		for _, byteSlot := range fs.callbackInvokes[fnSlot] {
			bobj, bok := paramObj(slotExpr(byteSlot))
			if !bok {
				recordInternal(fnIdx)
				continue
			}
			bIdx := st.params[bobj]
			dup := false
			for _, prev := range st.activeFS.callbackInvokes[fnIdx] {
				if prev == bIdx {
					dup = true
					break
				}
			}
			if !dup {
				if st.activeFS.callbackInvokes == nil {
					st.activeFS.callbackInvokes = map[int][]int{}
				}
				st.activeFS.callbackInvokes[fnIdx] = append(st.activeFS.callbackInvokes[fnIdx], bIdx)
			}
		}
	}
	for fnSlot := range fs.callbackInvokesInternal {
		if _, traced := fs.callbackInvokes[fnSlot]; traced {
			continue
		}
		fnIdx, ok := resolveFnIdx(fnSlot)
		if !ok {
			continue
		}
		recordInternal(fnIdx)
	}
}

// recordFieldAliasComposition composes struct-field callback records
// through a helper call, the structural-field counterpart of
// recordCallbackInvokeComposition:
//
//   - (a) a callee that INVOKES a struct field (h.cb(a, b), recorded in
//     fs.fieldInvokes) is re-recorded in the caller with the caller's
//     own byte-argument slots whenever the caller passes a carrier
//     value for that field key up the chain, so multi-helper carriers
//     (store -> h1 -> h2, with h2 invoking h.cb) stay visible at the
//     store-implementation call site;
//   - (b) a caller that hands a carrier value (a struct argument or
//     receiver holding the callback formal in one of its fields) to a
//     callee writes the callee's field-alias record as FORWARDED at the
//     callee's carrier parameter slot, so the callee's own body walk
//     and its onward call sites resolve the same canonical key.
//
// Only same-package callees are composed: cross-package summaries may
// already have stabilized before this package's fixpoint runs.
func (pf *pageFlow) recordFieldAliasComposition(st *stmtState, call *ast.CallExpr, fs *funcSummary, recvExpr ast.Expr, argOff int) {
	if st.activeFS == nil || fs == nil || call == nil {
		return
	}
	if len(fs.fieldInvokes) == 0 && len(fs.fieldAliases) == 0 && len(st.activeFS.fieldAliases) == 0 {
		return
	}
	// slotExpr maps a callee-relative slot to the caller's argument
	// expression (the receiver is slot 0 of a method call).
	slotExpr := func(slot int) ast.Expr {
		if recvExpr != nil && slot == 0 {
			return recvExpr
		}
		ai := slot - argOff
		if ai < 0 || ai >= len(call.Args) {
			return nil
		}
		return call.Args[ai]
	}
	paramSlot := func(e ast.Expr) (int, bool) {
		if e == nil {
			return 0, false
		}
		for {
			if se, ok := unparen(e).(*ast.SliceExpr); ok {
				e = se.X
				continue
			}
			break
		}
		id, ok := unparen(e).(*ast.Ident)
		if !ok {
			return 0, false
		}
		obj := pf.pc.info.ObjectOf(id)
		if obj == nil {
			return 0, false
		}
		idx, ok := st.params[obj]
		return idx, ok
	}
	// calleeParamType returns the canonical struct key of the callee's
	// parameter at the given callee-relative slot, or "" when the slot
	// does not name a carrier-capable struct parameter (or maps to no
	// argument expression at all).
	calleeParamType := func(slot int) string {
		if recvExpr != nil && slot == 0 {
			if pv := pf.pc.info.TypeOf(recvExpr); pv != nil {
				return pf.canonFieldType(pv)
			}
			return ""
		}
		sig := pf.funcTypeOf(call.Fun)
		if sig == nil || sig.Params() == nil {
			return ""
		}
		ai := slot - argOff
		if ai < 0 || ai >= sig.Params().Len() {
			return ""
		}
		return pf.canonFieldType(types.Unalias(sig.Params().At(ai).Type()))
	}
	// (a) re-record the callee's field invocations in the caller with
	// caller-relative byte slots. The caller must carry the same field
	// key (directly or forwarded) for the callee's invocation to be part
	// of the callback flow.
	for fk, byteSlots := range fs.fieldInvokes {
		if len(st.activeFS.fieldAliases[fk]) == 0 {
			continue
		}
		internal := false
		var slots []int
		for _, bs := range byteSlots {
			bIdx, bok := paramSlot(slotExpr(bs))
			if !bok {
				internal = true
				continue
			}
			dup := false
			for _, prev := range st.activeFS.fieldInvokes[fk] {
				if prev == bIdx {
					dup = true
					break
				}
			}
			if !dup {
				slots = append(slots, bIdx)
			}
		}
		if len(slots) > 0 {
			if st.activeFS.fieldInvokes == nil {
				st.activeFS.fieldInvokes = map[fieldSlotKey][]int{}
			}
			st.activeFS.fieldInvokes[fk] = append(st.activeFS.fieldInvokes[fk], slots...)
		}
		if internal {
			if st.activeFS.fieldInvokesInternal == nil {
				st.activeFS.fieldInvokesInternal = map[fieldSlotKey]bool{}
			}
			st.activeFS.fieldInvokesInternal[fk] = true
		}
	}
	// (a2) cascade unrecorded carrier chains upward: when the callee's
	// field-invocation byte slots land on the CALLER'S OWN PARAMETERS,
	// the parametrized views reach the field callee with no direct
	// carrier record to anchor enforcement; the caller's summary
	// carries the parameter -> field-key mapping so non-store call
	// sites binding a CONCRETE mapped view to that parameter fail
	// closed, and parameterized bindings cascade the record further.
	if len(fs.fieldInvokes) > 0 {
		for fk, byteSlots := range fs.fieldInvokes {
			for _, bs := range byteSlots {
				bIdx, bok := paramSlot(slotExpr(bs))
				if !bok {
					continue
				}
				if st.activeFS.paramFieldInvokes == nil {
					st.activeFS.paramFieldInvokes = map[int]map[fieldSlotKey]bool{}
				}
				if st.activeFS.paramFieldInvokes[bIdx] == nil {
					st.activeFS.paramFieldInvokes[bIdx] = map[fieldSlotKey]bool{}
				}
				st.activeFS.paramFieldInvokes[bIdx][fk] = true
			}
		}
	}
	// (c) pull callee DIRECT carrier records into the caller: a setter
	// or binding helper records its own field alias (s.cb = fn in a
	// prep method, setCb(&h, fn), a global-carrier setter). The caller
	// passing its own callback formal to that slot binds the same key,
	// so the store-implementation summary must carry the direct record
	// for moduleFieldCarrier and the param-sourced suppression to see
	// it. Only direct callee records pull up; forwarded records stay
	// anchored at the store call sites that own the enforcement.
	for fk, recs := range fs.fieldAliases {
		for _, r := range recs {
			if r.forwarded {
				continue
			}
			src := slotExpr(r.slot)
			if src == nil {
				continue
			}
			cs, ok := pf.slotOfExpr(st, st.activeFS, src)
			if !ok {
				continue
			}
			hasDirect := false
			for _, cur := range st.activeFS.fieldAliases[fk] {
				if cur == (fieldSlotAlias{slot: cs, path: r.path}) {
					hasDirect = true
					break
				}
			}
			if hasDirect {
				continue
			}
			pf.setFieldAlias(st.activeFS, fk, fieldSlotAlias{slot: cs, path: r.path})
		}
	}
	// (b) forward caller carrier records into the callee: every callee
	// parameter (receiver included) whose canonical struct type matches
	// one of the caller's field keys receives a forwarded alias record
	// at its own slot, so the callee's body walk resolves the key and
	// its own call sites compose onward. Forwarded records cascade
	// (store -> h1 -> h2 chains need h2's record even though h1's is a
	// forwarded one). A direct record already recorded by the callee's
	// own body wins.
	//
	// A nested carrier record matches a callee parameter declared as
	// the OUTER carrier type (runOuter(o outerJL, ...) with the
	// callback at o.in.cb): the path's first step names that outer
	// type, and the leaf key stays the field the callee actually
	// invokes.
	for fk, recs := range st.activeFS.fieldAliases {
		for _, rec := range recs {
			want := fk.typ
			if rec.path != "" {
				if i := strings.IndexByte(rec.path, '.'); i > 0 {
					want = rec.path[:i]
				}
			}
			n := len(call.Args) + argOff
			if recvExpr != nil {
				n++
			}
			for slot := 0; slot < n; slot++ {
				if calleeParamType(slot) != want {
					continue
				}
				dup := false
				for _, cur := range fs.fieldAliases[fk] {
					if !cur.forwarded || cur == (fieldSlotAlias{slot: slot, forwarded: true, path: rec.path}) {
						dup = true
						break
					}
				}
				if dup {
					break
				}
				if fs.fieldAliases == nil {
					fs.fieldAliases = map[fieldSlotKey][]fieldSlotAlias{}
				}
				fs.fieldAliases[fk] = append(fs.fieldAliases[fk], fieldSlotAlias{slot: slot, forwarded: true, path: rec.path})
				break
			}
		}
	}
}

// recordReturnComposite records carrier fields of a struct composite
// literal returned from a callee (mkCar(fn) returning car{cb: fn}, or
// nested: return top{mid{car{cb: fn}}}). Positional and keyed elements
// both resolve through the literal's struct type; each func-typed leaf
// bound to the formal slot becomes a returnCarrierField entry at the
// return position, with path naming the steps from the returned carrier
// root down to the leaf's host struct (outer-to-inner).
func (pf *pageFlow) recordReturnComposite(st *stmtState, fs *funcSummary, i int, lit *ast.CompositeLit, path string) {
	t := pf.pc.info.TypeOf(lit)
	if t == nil {
		return
	}
	k := pf.canonFieldType(t)
	if k == "" {
		return
	}
	var styp *types.Struct
	switch u := types.Unalias(t).(type) {
	case *types.Struct:
		styp = u
	case *types.Named:
		if su, ok := u.Underlying().(*types.Struct); ok {
			styp = su
		}
	}
	for j, el := range lit.Elts {
		var field string
		var val ast.Expr
		if kv, isKV := el.(*ast.KeyValueExpr); isKV {
			fid, isID := unparen(kv.Key).(*ast.Ident)
			if !isID {
				continue
			}
			field, val = fid.Name, kv.Value
		} else {
			if styp == nil || j >= styp.NumFields() {
				continue
			}
			field, val = styp.Field(j).Name(), el
		}
		if val == nil {
			continue
		}
		childPath := pf.joinPath(path, k, field)
		src := unparen(val)
		for {
			if ta, isTa := src.(*ast.TypeAssertExpr); isTa {
				src = unparen(ta.X)
				continue
			}
			if c, isC := src.(*ast.CallExpr); isC && isConversionCallExpr(pf.pc.info, c) {
				src = unparen(c.Args[0])
				continue
			}
			break
		}
		if litv, isLit := src.(*ast.CompositeLit); isLit {
			pf.recordReturnComposite(st, fs, i, litv, childPath)
			continue
		}
		rc := returnCarrierField{field: fieldSlotKey{typ: k, field: field}, param: -1, path: childPath}
		if slot, ok := pf.slotOfExpr(st, fs, src); ok {
			rc.param = slot
		} else if sel, isSel := src.(*ast.SelectorExpr); isSel {
			if srcKey, ok2 := pf.fieldSlotKeyOf(pf.pc.info, sel); ok2 {
				rc.srcRead = true
				rc.srcKey = srcKey
			}
		}
		if fs.returnCarrierFields == nil {
			fs.returnCarrierFields = map[int]returnCarrierField{}
		}
		if cur, ok := fs.returnCarrierFields[i]; ok && cur != rc {
			fs.returnCarrierFields[i] = returnCarrierField{field: cur.field, param: -2, path: cur.path}
			continue
		}
		fs.returnCarrierFields[i] = rc
	}
}

// recordReturnCarrierComposition composes callee results that carry
// the callback formal into the caller: mkCar(fn) returning
// car{cb: fn} records the caller's own fieldAlias for the returned
// carrier key, sourced from the callee parameter the value came from
// or from the caller's own field record when the callee read a field
// of its receiver. The caller-side record must exist before the same
// call's field-invocation composition re-records the callee's
// fieldInvokes, so later fixpoint passes settle chains built from
// returned carriers (helper -> helper -> store implementation).
func (pf *pageFlow) recordReturnCarrierComposition(st *stmtState, call *ast.CallExpr, fs *funcSummary, recvExpr ast.Expr, argOff int) {
	if st.activeFS == nil || fs == nil || len(fs.returnCarrierFields) == 0 {
		return
	}
	slotExpr := func(slot int) ast.Expr {
		if recvExpr != nil && slot == 0 {
			return recvExpr
		}
		ai := slot - argOff
		if ai < 0 || ai >= len(call.Args) {
			return nil
		}
		return call.Args[ai]
	}
	for _, rc := range fs.returnCarrierFields {
		if rc.param == -2 {
			continue
		}
		var slot int
		if rc.param >= 0 {
			src := slotExpr(rc.param)
			if src == nil {
				continue
			}
			s, ok := pf.slotOfExpr(st, st.activeFS, src)
			if !ok {
				continue
			}
			slot = s
		} else if rc.srcRead {
			found := false
			for _, r := range st.activeFS.fieldAliases[rc.srcKey] {
				if r.forwarded {
					continue
				}
				if rc.path != "" && r.path != rc.path {
					continue
				}
				slot, found = r.slot, true
				break
			}
			if !found {
				continue
			}
		} else {
			continue
		}
		pf.setFieldAlias(st.activeFS, rc.field, fieldSlotAlias{slot: slot, path: rc.path})
	}
}

// seedMappedCallbackParams marks a Store-surface callback literal's
// byte-carrying parameters as mapping aliases: Inspect/Update/CopyPage
// on a module-internal store interface hand the mapped page to the
// callback, so inside its body copies INTO those parameters stay in
// mapped memory. The parameters are seeded as parameter-conditional
// taint (the caller-bound length argument decides the span) plus the
// mapped flag; the seeds are removed after the literal body is
// analyzed.
func (pf *pageFlow) seedMappedCallbackParams(st *stmtState, lit *ast.FuncLit) []types.Object {
	if lit.Type.Params == nil {
		return nil
	}
	var seeded []types.Object
	idx := 0
	for _, f := range lit.Type.Params.List {
		for _, name := range f.Names {
			obj := pf.pc.info.ObjectOf(name)
			if obj != nil && paramCanCarryPage(obj.Type()) {
				st.stmtVars[obj] = pageValue{tainted: true, maxLen: maxUnknown, hasSrc: true, srcParam: idx, mapped: true}
				seeded = append(seeded, obj)
			}
			idx++
		}
	}
	return seeded
}

// recordPageSinkCall checks an expression-statement call made inside a
// page-sourcing loop. When the callee writes element-wise into one of
// its parameters and the caller passes a byte-buffer expression to that
// slot, the call is a complete-page copy through the helper.
func (pf *pageFlow) recordPageSinkCall(st *stmtState, x ast.Expr) {
	call, ok := unparen(x).(*ast.CallExpr)
	if !ok {
		return
	}
	var fn *types.Func
	switch f := unparen(call.Fun).(type) {
	case *ast.Ident:
		if obj, ok := pf.pc.info.Uses[f].(*types.Func); ok {
			fn = obj
		}
	case *ast.SelectorExpr:
		if obj, ok := pf.pc.info.Uses[f.Sel].(*types.Func); ok {
			fn = obj
		}
	}
	if fn == nil || fn.Pkg() == nil {
		return
	}
	pkgPath := fn.Pkg().Path()
	if !strings.HasPrefix(pkgPath, moduleImportPrefix) {
		return
	}
	sums := pf.summaries
	if pkgPath != pf.path {
		sums = pf.store.pkgs[pkgPath]
	}
	if sums == nil {
		return
	}
	key := fn.Name()
	recvOffset := 0
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		key = recvTypeNameFromTypes(sig.Recv().Type()) + "." + fn.Name()
		if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
			if selRecv, ok := pf.pc.info.Selections[sel]; ok && selRecv.Kind() == types.MethodVal {
				recvOffset = 1
			}
		}
	}
	fs, ok := sums[key]
	if !ok || len(fs.pageSinkParams) == 0 {
		return
	}
	var dests []ast.Expr
	for pi := range fs.pageSinkParams {
		if pi == 0 && recvOffset == 1 {
			if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
				dests = append(dests, sel.X)
			}
			continue
		}
		ai := pi - recvOffset
		if ai < 0 || ai >= len(call.Args) {
			continue
		}
		dests = append(dests, call.Args[ai])
	}
	if len(dests) == 0 {
		return
	}
	pf.pageSinkCalls[call] = dests
	// Mark the caller's destination as a page-aggregated buffer: the
	// helper writes PageSize elements into it across this loop.
	pv := pageValue{tainted: true, maxLen: maxUnknown}
	for _, dst := range dests {
		pf.values[dst] = pv
		if id, ok := unparen(dst).(*ast.Ident); ok {
			if obj := pf.pc.info.ObjectOf(id); obj != nil {
				if _, ok := obj.Type().Underlying().(*types.Slice); ok {
					st.stmtVars[obj] = pv
				}
			}
			continue
		}
		// A receiver/argument whose field is written records the field
		// path (h.put: h.Buf) on the root object.
		if obj, path := selectorChain(st, dst); obj != nil {
			if st.structs[obj] == nil {
				st.structs[obj] = map[string]pageValue{}
			}
			st.structs[obj][path] = pv
		}
	}
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
func sliceLenSym(v *ast.SliceExpr, st *stmtState, baseMax int64) symbol {
	a, okA := symbolOf(v.Low, st)
	b, okB := symbolOf(v.High, st)
	switch {
	case okA && okB:
		return b.sub(a)
	case v.Low == nil && okB:
		return b
	case v.High == nil && okA:
		// x[a:] — the remainder of the base view: bounded by the base's
		// definite maximum length minus a constant offset. Bases whose
		// length is unknown (params, unbound locals) stay unknown so the
		// complete-page fence fails closed. A negative or over-long
		// constant offset cannot name a non-empty remainder.
		if c, ok := a.isConst(); ok && c >= 0 && baseMax >= 0 {
			if c >= baseMax {
				return symConst(0)
			}
			return symConst(baseMax - c)
		}
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
				// An unproven callee (interface method, type-parameter
				// receiver, func field) can hand back a view of its
				// inputs, so the result inherits the receiver's and
				// arguments' provenance: mapped when any of them
				// aliases the mapping, otherwise the first tainted
				// source (so the call-site binding keeps the caller's
				// page source). This closes the generic-erasure gap: a
				// generic helper in a non-holder calling m.Page(0) on
				// a type-parameter receiver bound to a minted page
				// summarizes its result with the caller's mapped
				// provenance, and the view-holder export rule fails
				// closed on the leak.
				mapped := false
				var src pageValue
				take := func(pv pageValue) {
					if pv.mapped {
						mapped = true
					}
					if !src.tainted && !src.hasSrc && (pv.tainted || pv.hasSrc) {
						src = pv
					}
				}
				if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
					if _, isSel := pf.pc.info.Selections[sel]; isSel {
						rv := pf.evalExpr(st, sel.X)
						take(rv)
						// Erased receiver admitted by the mapping owner:
						// the value behind the interface can be the
						// mapping itself even when this body's parameter
						// summary is untainted. Fail closed on mapped so
						// the result carries mapping provenance into the
						// summary and the view-holder export rule fires;
						// interfaces the mapping cannot implement
						// (Codec.ReadKey, external Stringer/error) keep
						// tainted-only results and bounded record copies
						// stay legal.
						if !mapped && pf.couldBeMappingOwner(sel.X) {
							mapped = true
						}
					}
				}
				for _, a := range call.Args {
					take(pf.evalExpr(st, a))
				}
				if src.tainted || src.hasSrc {
					out := pageValue{tainted: true, maxLen: maxUnknown, srcParam: src.srcParam, srcField: src.srcField, hasSrc: true}
					if mapped {
						out.mapped = true
					}
					return out
				}
				if mapped {
					return pageValue{tainted: true, mapped: true, maxLen: maxUnknown}
				}
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
	// Store-surface callbacks (Inspect/Update/CopyPage on one of the
	// explicitly approved module-internal store interfaces) receive
	// mapped page views: the func-literal arguments are analyzed with
	// their byte parameters marked as mapping aliases. A non-approved
	// interface is never seeded: its implementation is not provably a
	// scanned module store, so a callback argument could escape mapped
	// data into owned memory.
	storeCallback := false
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && storeCallbackMethod(fn.Name()) {
		rt := sig.Recv().Type()
		if isInterfaceType(rt) {
			storeCallback = approvedModuleInternalInterface(rt)
		} else {
			// Concrete receiver (e.g. *writer.DraftStore): the call is
			// store-surface only when the receiver provably implements
			// one of the approved store interfaces, whose implementation
			// bodies the module scan owns. A concrete type that shares
			// the method names but implements no approved store
			// interface never receives mapped page views, so its
			// callback literals must not be seeded.
			for _, iface := range pf.pc.approvedStoreInterfaces() {
				if types.Implements(rt, iface) {
					storeCallback = true
					break
				}
			}
		}
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
			pv := pageValue{tainted: true, maxLen: pageSize, mapped: true}
			pf.callResults[call] = []pageValue{pv, {}}
			return pv
		case "View":
			// View(off, length): the length argument bounds the view; a
			// constant bound keeps the result below a complete page.
			pv := pageValue{tainted: true, maxLen: maxUnknown, mapped: true}
			if len(call.Args) == 2 {
				if s, ok := symbolOf(call.Args[1], st); ok {
					if c, ok := s.isConst(); ok {
						pv.maxLen = c
					}
				}
			}
			pf.callResults[call] = []pageValue{pv, {}}
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
		// Store-surface interface methods (Inspect/Update/CopyPage) hand
		// a complete mapped page view to a caller-supplied callback. The
		// interface method has no body summary, so this miss path is the
		// only analysis these calls get: evaluate callback literals with
		// their byte parameters seeded as mapping aliases, exactly like
		// the concrete-callee args loop below.
		if storeCallback {
			for _, a := range call.Args {
				var seeded []types.Object
				if lit, ok := unparen(a).(*ast.FuncLit); ok {
					seeded = pf.seedMappedCallbackParams(st, lit)
				}
				pf.promoteFullPageFields(st, a)
				pf.evalExpr(st, a)
				for _, obj := range seeded {
					delete(st.stmtVars, obj)
				}
			}
		}
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
				// Carry the mapped provenance when the erased receiver
				// could be the mapping owner itself (pager5.Page has
				// the mint signature, so *mapping.Mapping implements
				// it): the launder-helper shape PeekC(m pager5) then
				// summarizes mapped and the view-holder export rule
				// fails closed. Interfaces the mapping cannot
				// implement keep tainted-only results.
				if pf.couldBeMappingOwner(recvExpr) {
					return pageValue{tainted: true, maxLen: maxUnknown, mapped: true}
				}
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
	pf.recordCopyParamComposition(st, call, fs, recvExpr, argOff)
	if recvExpr != nil {
		argOff = 1
	}
	pf.recordCallbackInvokeComposition(st, call, fs, recvExpr, argOff)
	pf.recordFieldAliasComposition(st, call, fs, recvExpr, argOff)
	pf.recordReturnCarrierComposition(st, call, fs, recvExpr, argOff)
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
		// Store-surface callback literals receive mapped page views by
		// contract: Inspect/Update/CopyPage on a module-internal store
		// interface hand the mapping to the callback, so its byte
		// parameters are seeded as mapping aliases while the literal
		// body is analyzed. Copies INTO those parameters stay mapped;
		// the fail-closed page checks over the literal's own calls keep
		// seeing the conditional taint, so nothing launders.
		var seeded []types.Object
		if storeCallback {
			if lit, ok := unparen(a).(*ast.FuncLit); ok {
				seeded = pf.seedMappedCallbackParams(st, lit)
			}
		}
		pf.promoteFullPageFields(st, a)
		pv := pf.evalExpr(st, a)
		sv, _ := symbolOf(a, st)
		af := pf.argFlowOf(st, a)
		// The seeds must outlive argFlowOf: it re-evaluates the literal
		// (and the rule pass reads the last cached body values), so
		// deleting before it would leave the callback formals'
		// mapped-taint erased.
		for _, obj := range seeded {
			delete(st.stmtVars, obj)
		}
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
	// Func-literal arguments bound to a callee formal the callee
	// invokes are analyzed with the literal's parameters bound to the
	// call-site views the invocation record says reach the callback:
	// the store-callback counter-check and the fail-closed
	// unproven-callee checks over the literal body then evaluate those
	// views instead of unbound parameters. Slots the callee marks
	// internal leave the parameters unbound (the views are not provably
	// the call-site arguments and fail closed at the call site).
	for fnSlot, byteSlots := range fs.callbackInvokes {
		if fs.callbackInvokesInternal[fnSlot] {
			continue
		}
		ai := fnSlot - argOff
		if ai < 0 || ai >= len(call.Args) {
			continue
		}
		lit, ok := unparen(call.Args[ai]).(*ast.FuncLit)
		if !ok {
			// A local bound to the literal (cb := func...): resolve the
			// current binding so the literal body still sees the views
			// the callee hands the callback.
			if id, isID := unparen(call.Args[ai]).(*ast.Ident); isID {
				if obj := pf.pc.info.ObjectOf(id); obj != nil {
					lit, _ = st.localFuncs[obj]
				}
			}
		}
		if lit == nil {
			continue
		}
		bound := make([]ast.Expr, 0, len(byteSlots))
		missing := false
		for _, bs := range byteSlots {
			bi := bs - argOff
			if bi < 0 || bi >= len(call.Args) {
				missing = true
				break
			}
			bound = append(bound, call.Args[bi])
		}
		if missing {
			continue
		}
		pf.analyzeFuncLitCall(st, lit, &ast.CallExpr{Fun: lit, Args: bound})
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
				if src.mapped {
					pv.mapped = true
				}
			case "param":
				if src.param >= 0 && src.param < len(bound) {
					b := bound[src.param]
					m = b.maxLen
					if b.mapped {
						pv.mapped = true
					}
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
					if b.mapped {
						pv.mapped = true
					}
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
				if src.mapped {
					pv.mapped = true
				}
			case "param", "paramMax":
				if src.param >= 0 && src.param < len(bound) {
					b := bound[src.param]
					m = b.maxLen
					if b.mapped {
						pv.mapped = true
					}
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
				// &h.Items[0] addresses an element of a SELECTED-FIELD
				// container: the mutation binds the base object under
				// the "Items." prefix, the same flattened path the
				// h.Items[0].Data read resolves.
				if path, ro := selectorIndexChain(op); ro != nil {
					// A TYPE-ASSERTED root (&v.(*H).Items[0]) binds the
					// asserted base variable under the "Items." prefix.
					if oo := chainRootObject(st, ro); oo != nil {
						targets = append(targets, oo)
						prefix = path + "."
					}
				}
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
	fs := &funcSummary{fields: map[string]fieldTaint{}, stringParams: map[int]bool{}, pageSinkParams: map[int]bool{}}
	pf.analyzeStmts(st, lit.Body.List, fs)
	pf.noteStringConvs(st, fs, lit.Body)
	pf.noteFmtSpreads(st, fs, lit.Body)
	pf.notePageSinks(st, fs, lit.Body)
	pf.noteCopyParams(st, fs, lit.Body)
}

// analyzeFuncLitCall binds a closure's parameters to the call-site
// argument taints, analyzes the body, and returns the closure's result
// taints as the call result. Every result slot is recorded so a
// multi-result closure assignment distributes taint per slot.
func (pf *pageFlow) analyzeFuncLitCall(st *stmtState, lit *ast.FuncLit, call *ast.CallExpr) pageValue {
	pf.clearExprCaches()
	args := call.Args
	fs := &funcSummary{fields: map[string]fieldTaint{}, stringParams: map[int]bool{}, mutFields: map[int]map[string]fieldTaint{}, pageSinkParams: map[int]bool{}}
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
	pf.notePageSinks(st, fs, lit.Body)
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
		// A LOCAL interface value with unprovable whole-value taint (a
		// type-switch implicit variable of an `any` case, a variable
		// assigned an unprovable call result) asserted to a struct
		// exposes the asserted type's page-carrying leaves without a
		// recorded source: the concrete value is unknowable, so every
		// leaf fails closed exactly like the direct assertion read
		// path, and a bind-then-read (b := v.(T); b.Data) keeps the
		// projection inside the callee summary.
		if fields == nil {
			if pv := pf.evalExpr(st, v.X); pv.tainted {
				if stt, ok := derefStruct(pf.pc.info.Types[v.Type].Type); ok {
					for path, ft := range paramLeafPaths(stt) {
						if !paramCanCarryPage(ft) {
							continue
						}
						if fields == nil {
							fields = map[string]pageValue{}
						}
						fields[path] = pageValue{tainted: true, maxLen: maxUnknown}
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
			// A LOCAL asserted base with unprovable whole-value taint
			// (a type-switch implicit variable of an `any` case, a
			// variable assigned an unprovable call result) exposes the
			// asserted type's page-carrying leaves under the selected
			// chain without a recorded source: the concrete value is
			// unknowable, so every leaf fails closed exactly like the
			// direct asserted-read path.
			if fields == nil {
				if pv := pf.evalExpr(st, ta.X); pv.tainted {
					if stt, ok := derefStruct(pf.pc.info.Types[ta.Type].Type); ok {
						for path, ft := range paramLeafPaths(stt) {
							if !paramCanCarryPage(ft) || !strings.HasPrefix(path, prefix) {
								continue
							}
							if fields == nil {
								fields = map[string]pageValue{}
							}
							fields[path[len(prefix):]] = pageValue{tainted: true, maxLen: maxUnknown}
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
	return mapUnderlyingSeen(t, map[types.Type]bool{})
}

// mapUnderlyingSeen is the recursive core of mapUnderlying. The seen
// set stops a self-referential pointer chain (type P *P) from unwrapping
// forever: every recursive edge revisits the named type and the walk
// reports "no map" at the cycle.
func mapUnderlyingSeen(t types.Type, seen map[types.Type]bool) *types.Map {
	switch u := types.Unalias(t).(type) {
	case *types.Map:
		return u
	case *types.Named:
		if seen[u] {
			return nil
		}
		seen[u] = true
		return mapUnderlyingSeen(u.Underlying(), seen)
	case *types.Pointer:
		// A pointer-wrapped map parameter or field (m *map[*B]int)
		// exposes the same key leaves as the map value itself: the
		// key-only range and key-store rules dereference only map
		// (and named-map) wrappers, so the pointer must unwrap here
		// or every key leaf of the pointed-to map is lost.
		if seen[u] {
			return nil
		}
		seen[u] = true
		return mapUnderlyingSeen(u.Elem(), seen)
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
	// Evaluate the expression FIRST, before the promotionCandidate gate:
	// for a call-expression argument (len(s.Text()), a conversion, an
	// opaque helper) this runs evalCall on the nested call, which
	// analyzes the interface dispatch (promoting a concrete page-carrying
	// receiver via the miss path) and records fail-closed call state.
	// The candidate gate below would otherwise skip a non-carrier
	// argument (a string result, a uint) and the nested call would never
	// be flow-analyzed at this site. The gate still controls the
	// whole-value/field PROMOTION on e itself; evalExpr is idempotent
	// (results cached per expression node), so this ordering only decides
	// whether the nested call is analyzed before the gate.
	pv := pf.evalExpr(st, e)
	// A promoted whole-value taint is only meaningful when the expression
	// itself can hold page bytes: struct values carry their field taints
	// (b.Data = page; cb(b)), byte slices carry the view, interface
	// values can box either. Scalar expressions (int/bool variables that
	// an over-approximating multi-result distribution stamped with a
	// junk field map) must never graduate into whole-value page taint:
	// the field map on a scalar is an analyzer artifact, not a real page
	// carrier.
	if t := pf.pc.info.Types[e].Type; t == nil || !promotionCandidate(t) {
		return
	}
	af := pf.argFlowOf(st, e)
	// A whole-value full-page taint with NO recorded struct field is a
	// direct page binding or a parameter fallback (an interface or byte
	// parameter that might receive a page from a caller), not a
	// field-hidden carrier: nothing is promoted, and the fail-closed
	// module-internal receiver and opaque-call checks stay benign for
	// such values (their concrete carriers are policed at the source).
	// Expressions that carry BOTH a full whole value AND recorded
	// page-bearing fields (an interface variable bound to a
	// page-carrying struct, a struct literal with a page-bound field)
	// fall through to the scan below, which marks the field-promoted
	// marker on the concrete carrier.
	if pv.tainted && pageFull(pv) && len(af.fields) == 0 {
		return
	}
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
		if pf.fieldPromoted == nil {
			pf.fieldPromoted = map[ast.Expr]bool{}
		}
		pf.fieldPromoted[e] = true
		return
	}
	// The whole value may ALREADY be a full-page taint (an interface
	// variable bound to a page-carrying struct, a struct literal with a
	// page-bound field) while the same recorded struct fields still make
	// it a FIELD-HIDDEN carrier: a callee this call site cannot resolve
	// (an interface dispatch, a callback) can extract and copy the field
	// even though the value itself is not a byte slice. The old early
	// return above would never have marked such expressions as promoted,
	// so the fail-closed receiver/argument checks could not see concrete
	// carriers bound through a whole-page value. The scan above keeps
	// the promotion gate: a whole-value fallback taint WITHOUT a recorded
	// full-page field (an interface parameter that might receive a page
	// from a caller) is not a concrete carrier and stays benign.
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
	case *ast.IndexExpr, *ast.IndexListExpr:
		// A generic instantiation (gApply[T](...)) names the same
		// scanned function as the plain identifier: resolve the base
		// so the call reaches the generic body's summary (callback
		// invocations, bound literal analysis, string-conversion
		// propagation). A func-typed container slot (arr[0](fn)) whose
		// base is a variable resolves to that variable and stays in the
		// local-binding/unproven path exactly as before.
		var base ast.Expr
		if ie, ok := f.(*ast.IndexExpr); ok {
			base = ie.X
		} else if il, ok := f.(*ast.IndexListExpr); ok {
			base = il.X
		}
		if id, ok := unparen(base).(*ast.Ident); ok {
			return pc.info.Uses[id]
		}
		// A generic METHOD with its own type arguments
		// (w.apply[T](...)): the base is a selector naming the same
		// scanned method as the plain call.
		if sel, ok := unparen(base).(*ast.SelectorExpr); ok {
			return pc.info.Uses[sel.Sel]
		}
	}
	return nil
}

// varDefOf returns the single definition expression of obj in body,
// whether that definition is unique (every write counted, nested
// closures included), and whether obj's address is taken anywhere.
// A variable with several writes or an escaped address is not provably
// bound to one value, so the callers fail closed (resolve nothing).
func varDefOf(info *types.Info, body *ast.BlockStmt, obj types.Object) (ast.Expr, bool, bool) {
	if info == nil || body == nil || obj == nil {
		return nil, false, false
	}
	rootOf := func(e ast.Expr) types.Object {
		for {
			switch t := unparen(e).(type) {
			case *ast.SelectorExpr:
				e = t.X
			case *ast.IndexExpr:
				e = t.X
			case *ast.StarExpr:
				e = t.X
			case *ast.SliceExpr:
				e = t.X
			case *ast.TypeAssertExpr:
				e = t.X
			default:
				if id, ok := t.(*ast.Ident); ok {
					return info.ObjectOf(id)
				}
				return nil
			}
		}
	}
	taken := false
	count := 0
	var init ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if rootOf(lhs) == obj {
					count++
					if count == 1 && i < len(v.Rhs) {
						init = v.Rhs[i]
					}
				}
			}
		case *ast.ValueSpec:
			for i, name := range v.Names {
				if info.ObjectOf(name) == obj {
					count++
					if count == 1 && i < len(v.Values) {
						init = v.Values[i]
					}
				}
			}
		case *ast.IncDecStmt:
			if rootOf(v.X) == obj {
				count++
			}
		case *ast.UnaryExpr:
			if v.Op == token.AND && rootOf(v.X) == obj {
				taken = true
			}
		}
		return true
	})
	if count != 1 {
		return nil, false, taken
	}
	if taken {
		// The variable may still hold its single definition (the
		// address was taken but the value may be unchanged), so return
		// the initializer with the taken flag: callers that resolve
		// deliberately (calleeExprFunc, method values) stay fail-closed
		// through the resolved summary instead of dropping the chain.
		return init, false, true
	}
	return init, true, false
}

// calleeExprFunc resolves a call's callee expression to the scanned
// module function it provably names: a package function identifier, a
// method selector, or a LOCAL func-typed variable whose single
// definition binds one of those (g := passthrough; cb := g(fn) and
// mv := s.getCB; cb := mv() launch the same closure values as the
// direct spellings). The local resolution also follows address-taken
// single-definition locals (p := &g does not prove g was exchanged),
// func-typed struct fields bound through a composite definition
// (h := car{g: passthrough}; cb := h.g(fn)), and func-typed indexed
// container slots (arr := [1]func{...}{passthrough}; cb := arr[0](fn)).
// Chained locals, field/selector chains, conversion/unwrap expressions,
// and call-result wrappers resolve recursively.
func (pf *pageFlow) calleeExprFunc(st *stmtState, x ast.Expr) (*types.Func, bool) {
	for {
		e := unparen(x)
		for {
			if ta, isTa := e.(*ast.TypeAssertExpr); isTa {
				e = unparen(ta.X)
				continue
			}
			if c, isC := e.(*ast.CallExpr); isC && isConversionCallExpr(pf.pc.info, c) {
				e = unparen(c.Args[0])
				continue
			}
			break
		}
		switch t := e.(type) {
		case *ast.Ident:
			if f, ok := pf.pc.info.Uses[t].(*types.Func); ok {
				return f, true
			}
			v, ok := pf.pc.info.Uses[t].(*types.Var)
			if !ok || st == nil || st.fd == nil || st.fd.Body == nil {
				return nil, false
			}
			init, single, taken := varDefOf(pf.pc.info, st.fd.Body, v)
			if init == nil || (!single && !taken) {
				return nil, false
			}
			x = init
		case *ast.SelectorExpr:
			if f, ok := pf.pc.info.Uses[t.Sel].(*types.Func); ok {
				return f, true
			}
			// A func-typed FIELD called through a receiver whose
			// binding provably holds a function value (h :=
			// car{g: passthrough}; cb := h.g(fn)): extract the bound
			// value and keep resolving.
			if v, ok := pf.pc.info.Uses[t.Sel].(*types.Var); ok && funcSignature(v.Type()) != nil {
				if val, ok := pf.fieldBoundValue(st, t.X, t.Sel.Name); ok {
					x = val
					continue
				}
			}
			return nil, false
		case *ast.IndexExpr, *ast.IndexListExpr:
			// A generic instantiation of a scanned function
			// (cb := runG[T](...)...): the callee is the generic
			// function itself.
			var base ast.Expr
			if ie, ok := t.(*ast.IndexExpr); ok {
				base = ie.X
			} else if il, ok := t.(*ast.IndexListExpr); ok {
				base = il.X
			}
			if id, ok := unparen(base).(*ast.Ident); ok {
				if f, ok := pf.pc.info.Uses[id].(*types.Func); ok {
					return f, true
				}
			}
			if sel, ok := unparen(base).(*ast.SelectorExpr); ok {
				if f, ok := pf.pc.info.Uses[sel.Sel].(*types.Func); ok {
					return f, true
				}
			}
			if ie, ok := t.(*ast.IndexExpr); ok {
				// A func-typed container slot called through a constant
				// index (arr := [1]func{...}{passthrough}; cb :=
				// arr[0](fn)).
				if val, ok := pf.indexBoundValue(st, ie); ok {
					x = val
					continue
				}
				// A non-constant index over a container whose every
				// element is the same func value (hs := []F{passthrough};
				// for i := range hs { cb := hs[i](fn) }): any index
				// selects that value.
				if val, ok := pf.anyIndexBoundValue(st, ie); ok {
					x = val
					continue
				}
			}
			return nil, false
		case *ast.CallExpr:
			// The func-typed value is the call's own callee (a local
			// bound to passthrough(x) still names passthrough).
			x = t.Fun
		default:
			return nil, false
		}
	}
}

// exprCompositeLit resolves an expression to the struct/array composite
// literal its provable value binds: a literal itself (optionally
// parenthesized or address-taken), a local's single definition, or a
// field of such a composite read through a selector chain. The
// resolution deliberately follows address-taken single-definition
// locals (the value may still be the literal) so the callee fence
// stays fail-closed instead of dropping the chain.
func (pf *pageFlow) exprCompositeLit(st *stmtState, x ast.Expr) (*ast.CompositeLit, bool) {
	for {
		e := unparen(x)
		if ue, isUe := e.(*ast.UnaryExpr); isUe && ue.Op == token.AND {
			e = unparen(ue.X)
		}
		switch t := e.(type) {
		case *ast.CompositeLit:
			return t, true
		case *ast.Ident:
			obj := pf.pc.info.ObjectOf(t)
			if obj == nil || st == nil || st.fd == nil || st.fd.Body == nil {
				return nil, false
			}
			if obj.Parent() == pf.pc.pkg.Scope() {
				// A package-scope container (var ar = [1]F{passthrough})
				// has no definition inside the function body: resolve
				// through the package initializer, guarded by the
				// never-reassigned proof the package-var rules rely on.
				pv, isVar := obj.(*types.Var)
				if !isVar || pf.pc.varInits == nil || pf.pc.reassignedVars[pv] {
					return nil, false
				}
				init, ok := pf.pc.varInits[pv]
				if !ok {
					return nil, false
				}
				x = init
				continue
			}
			init, single, taken := varDefOf(pf.pc.info, st.fd.Body, obj)
			if init == nil || (!single && !taken) {
				return nil, false
			}
			x = init
		case *ast.SelectorExpr:
			if _, ok := pf.pc.info.Uses[t.Sel].(*types.Var); ok {
				val, ok := pf.fieldBoundValue(st, t.X, t.Sel.Name)
				if !ok {
					return nil, false
				}
				x = val
				continue
			}
			return nil, false
		default:
			return nil, false
		}
	}
}

// fieldBoundValue extracts the value bound to a struct field from the
// composite literal its receiver provably binds (h := car{g:
// passthrough} then h.g; chains like h.a.g resolve recursively).
func (pf *pageFlow) fieldBoundValue(st *stmtState, x ast.Expr, field string) (ast.Expr, bool) {
	lit, ok := pf.exprCompositeLit(st, x)
	if !ok {
		return nil, false
	}
	t := pf.pc.info.TypeOf(lit)
	if t == nil {
		return nil, false
	}
	var styp *types.Struct
	switch u := types.Unalias(t).(type) {
	case *types.Struct:
		styp = u
	case *types.Named:
		if su, ok := u.Underlying().(*types.Struct); ok {
			styp = su
		}
	}
	if styp == nil {
		return nil, false
	}
	for i, el := range lit.Elts {
		var fname string
		var val ast.Expr
		if kv, isKV := el.(*ast.KeyValueExpr); isKV {
			fid, isID := unparen(kv.Key).(*ast.Ident)
			if !isID {
				continue
			}
			fname, val = fid.Name, kv.Value
		} else {
			if i >= styp.NumFields() {
				return nil, false
			}
			fname, val = styp.Field(i).Name(), el
		}
		if fname == field {
			return val, true
		}
	}
	return nil, false
}

// indexBoundValue extracts the value bound at a constant index of a
// container composite (arr := [1]func{...}{passthrough}; arr[0],
// m := map[string]F{"cb": passthrough}; m["cb"]). Integer keys select
// positional elements of array/slice literals; any constant key selects
// a keyed map element.
func (pf *pageFlow) indexBoundValue(st *stmtState, ix *ast.IndexExpr) (ast.Expr, bool) {
	idx, ok := pf.constValue(st, ix.Index)
	if !ok {
		return nil, false
	}
	lit, ok := pf.exprCompositeLit(st, ix.X)
	if !ok {
		return nil, false
	}
	positional := int64(0)
	for _, el := range lit.Elts {
		if kv, isKV := el.(*ast.KeyValueExpr); isKV {
			kid, kok := pf.constValue(st, kv.Key)
			if !kok || kid.Kind() != idx.Kind() {
				continue
			}
			if constant.Compare(kid, token.EQL, idx) {
				return kv.Value, true
			}
			continue
		}
		if idx.Kind() == constant.Int {
			if n, ok := constant.Int64Val(idx); ok && positional == n {
				return el, true
			}
		}
		positional++
	}
	return nil, false
}

// constValue evaluates e to a constant value (an array index, a map
// key), resolving single-definition local variables (i := 0; hs[i]).
func (pf *pageFlow) constValue(st *stmtState, e ast.Expr) (constant.Value, bool) {
	for {
		e2 := unparen(e)
		if tv, ok := pf.pc.info.Types[e2]; ok && tv.Value != nil {
			return tv.Value, true
		}
		id, ok := e2.(*ast.Ident)
		if !ok || st == nil || st.fd == nil || st.fd.Body == nil {
			return nil, false
		}
		init, single, taken := varDefOf(pf.pc.info, st.fd.Body, pf.pc.info.ObjectOf(id))
		if init == nil || !single || taken {
			return nil, false
		}
		e = init
	}
}

// constIndex evaluates an expression to a constant integer index.
func (pf *pageFlow) constIndex(st *stmtState, e ast.Expr) (int64, bool) {
	cv, ok := pf.constValue(st, e)
	if !ok || cv.Kind() != constant.Int {
		return 0, false
	}
	return constant.Int64Val(cv)
}

// anyIndexBoundValue resolves a container read at a NON-constant index
// when every positional element of the container composite resolves to
// the same func-typed value: whatever the index selects, the value is
// that element. Keyed (map) literals and mixed element sets stay
// unresolved (conservative).
func (pf *pageFlow) anyIndexBoundValue(st *stmtState, ix *ast.IndexExpr) (ast.Expr, bool) {
	lit, ok := pf.exprCompositeLit(st, ix.X)
	if !ok || len(lit.Elts) == 0 {
		return nil, false
	}
	var want *types.Func
	for _, el := range lit.Elts {
		if _, isKV := el.(*ast.KeyValueExpr); isKV {
			return nil, false
		}
		f, ok := pf.calleeExprFunc(st, el)
		if !ok {
			return nil, false
		}
		if want == nil {
			want = f
		} else if want != f {
			return nil, false
		}
	}
	if want == nil {
		return nil, false
	}
	return unparen(lit.Elts[0]), true
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
	foreign := func(tt types.Type) bool {
		sp := structDeclPkg(tt)
		return sp != nil && sp != pf.pc.pkg
	}
	walk = func(tt types.Type, prefix string) {
		stt, ok := derefStruct(tt)
		if !ok {
			return
		}
		if walkSeen[stt] {
			return // recursion through a self-referencing pointer field
		}
		walkSeen[stt] = true
		isForeign := foreign(tt)
		for i := 0; i < stt.NumFields(); i++ {
			f := stt.Field(i)
			// A foreign struct's UNEXPORTED fields are unreachable from
			// this package's reads, but its EXPORTED field graph is
			// readable and can carry page leaves (encoding/pem.Block
			// has exported Bytes []byte): an unproven callee returning
			// such a struct can launder a page through the exported
			// read, so only the private fields stay untainted (the
			// bytes.Reader.src shape P218 relies on).
			if isForeign && !f.Exported() {
				continue
			}
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

// couldBeMappingOwner reports whether a receiver expression's static
// type admits the mapping owner as an implementation: only then can an
// unproven call's erased value BE the mapping and hand back a mapped
// view without any argument carrying it. pager5.Page(uint32)([]byte,
// error) qualifies (the mint signature of *mapping.Mapping); tree.Codec
// and every external interface (error, io.Reader, fmt.Stringer) do not,
// so their results stay tainted-only and bounded record copies remain
// legal. Handing a mapped value to any other interface is policed
// separately by the type-erasure launder rule at the argument site.
func (pf *pageFlow) couldBeMappingOwner(recvExpr ast.Expr) bool {
	if recvExpr == nil {
		return false
	}
	var iface *types.Interface
	switch u := types.Unalias(pf.pc.info.TypeOf(recvExpr)).(type) {
	case *types.Named:
		i, ok := u.Underlying().(*types.Interface)
		if !ok {
			return false
		}
		iface = i
	case *types.Interface:
		iface = u
	default:
		return false
	}
	mapping := pf.mappingOwnerType()
	return mapping != nil && types.Implements(types.NewPointer(mapping), iface)
}

// mappingOwnerType resolves the module's *mapping.Mapping owner type,
// cached per page flow.
func (pf *pageFlow) mappingOwnerType() *types.Named {
	if pf.mappingT != nil {
		return pf.mappingT
	}
	pkg, err := pf.pc.loader.Import(mappingImportPath)
	if err != nil {
		return nil
	}
	tn, ok := pkg.Scope().Lookup("Mapping").(*types.TypeName)
	if !ok {
		return nil
	}
	pf.mappingT, _ = tn.Type().(*types.Named)
	return pf.mappingT
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
		} else if ta := typeAssertBaseOf(sel); ta != nil {
			// v.(*H).Inner with v an interface-typed parameter: the
			// selected value keeps the caller's field taints through the
			// asserted type's "Inner."-prefixed leaf paths, the same
			// projection `return v.(T)` applies for a whole asserted
			// value, so identity helpers over an interface keep their
			// results caller-dependent.
			chain, _ := selectorIndexChain(sel)
			prefix := chain + "."
			if o := objOfDeref(st, ta.X); o != nil {
				if idx, ok := st.params[o]; ok && isInterfaceType(o.Type()) {
					if stt, ok := derefStruct(pf.pc.info.Types[ta.Type].Type); ok {
						for p, ft := range paramLeafPaths(stt) {
							if !paramCanCarryPage(ft) || !strings.HasPrefix(p, prefix) {
								continue
							}
							src := pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: p, hasSrc: true}
							fs.fields[p[len(prefix):]] = joinFieldTaint(fs.fields[p[len(prefix):]], fieldTaint{tainted: true, srcs: maxSrcOf(src)})
						}
					}
				}
			}
			// A LOCAL whole-tainted asserted base (a type-switch
			// implicit variable of an `any` case) fails closed on the
			// same leaves without a recorded source.
			if pv := pf.evalExpr(st, ta.X); pv.tainted {
				if stt, ok := derefStruct(pf.pc.info.Types[ta.Type].Type); ok {
					for p, ft := range paramLeafPaths(stt) {
						if !paramCanCarryPage(ft) || !strings.HasPrefix(p, prefix) {
							continue
						}
						if fields == nil {
							fields = map[string]pageValue{}
						}
						fields[p[len(prefix):]] = pageValue{tainted: true, maxLen: maxUnknown}
					}
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
		// A returned container element (returns xs[0], m[0][0]):
		// every trailing index names an element of the same root
		// container, so the recorded element-field taints resolve
		// from the root expression, the same unwrap argument flow
		// and field reads use. A SELECTED-FIELD container root
		// (return h.Items[0]) resolves the base object's
		// "Items."-prefixed records, stripped to the direct field
		// names, so the caller's read of the returned value stays
		// sourced.
		if path, ro := selectorIndexChain(expr); ro != nil {
			prefix := path + "."
			if obj := chainRootObject(st, ro); obj != nil {
				if m, ok := st.structs[obj]; ok {
					for k, fv := range m {
						if fv.tainted && strings.HasPrefix(k, prefix) {
							fs.fields[k[len(prefix):]] = joinFieldTaint(fs.fields[k[len(prefix):]], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
						}
					}
				}
				if idx, ok := st.params[obj]; ok {
					for k, fv := range pf.paramFieldFallback(st, obj, idx) {
						if !fv.tainted || !strings.HasPrefix(k, prefix) {
							continue
						}
						fs.fields[k[len(prefix):]] = joinFieldTaint(fs.fields[k[len(prefix):]], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
					}
				}
			}
			if ta, ok := unparen(ro).(*ast.TypeAssertExpr); ok {
				// v.(*H).Items[0] with v an INTERFACE-TYPED parameter:
				// the returned element keeps the caller's field taints
				// through the asserted type's "Items."-prefixed leaf
				// paths, the same projection `return v.(T)` applies for
				// a whole asserted value, so identity helpers over an
				// interface keep their results caller-dependent.
				if o := objOfDeref(st, ta.X); o != nil {
					if idx, ok := st.params[o]; ok && isInterfaceType(o.Type()) {
						if stt, ok := derefStruct(pf.pc.info.Types[ta.Type].Type); ok {
							for p, ft := range paramLeafPaths(stt) {
								if !paramCanCarryPage(ft) || !strings.HasPrefix(p, prefix) {
									continue
								}
								src := pageValue{tainted: true, maxLen: maxUnknown, srcParam: idx, srcField: p, hasSrc: true}
								fs.fields[p[len(prefix):]] = joinFieldTaint(fs.fields[p[len(prefix):]], fieldTaint{tainted: true, srcs: maxSrcOf(src)})
							}
						}
					}
				}
			}
			if call, chain := callRootChain(expr); call != nil && chain != "" {
				// makeH(p).Items[0]: the returned element of a
				// CALL-PRODUCED selected container keeps the callee's
				// flattened "Items."-prefixed element fields, renamed to
				// the element's direct names, so the caller's read of
				// the returned value stays sourced.
				if m := pf.callProducedFields(st, expr); len(m) > 0 {
					for k, fv := range m {
						if fv.tainted {
							fs.fields[k] = joinFieldTaint(fs.fields[k], fieldTaint{tainted: true, srcs: maxSrcOf(fv)})
						}
					}
				}
			}
		}
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
	seen := map[types.Type]bool{}
	for {
		switch v := t.(type) {
		case *types.Pointer:
			t = v.Elem()
		case *types.Named:
			// A self-referential named pointer (type P *P) must stop at
			// the revisiting type instead of unwrapping forever: P has
			// no struct fields, so reporting "not a struct" terminates
			// the walk with the correct result.
			if seen[v] {
				return nil, false
			}
			seen[v] = true
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
// promotionCandidate reports whether an expression can carry a mapped
// page either as its whole value (byte slices, interfaces, containers)
// or through struct fields (b.Data = page makes b a carrier for the
// field-promotion rule). Scalar expressions (int, bool, uint indexes)
// are never carriers: a field map stamped on them by an
// over-approximating multi-result distribution is an analyzer artifact.
func promotionCandidate(t types.Type) bool {
	if paramCanCarryPage(t) {
		return true
	}
	for _, ft := range paramLeafPaths(t) {
		if paramCanCarryPage(ft) {
			return true
		}
	}
	return false
}

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
	return paramLeafPathsSeen(t, map[*types.Struct]*leafPathsMemo{})
}

// paramLeafPathsSeen is the recursive core of paramLeafPaths with a
// per-struct memo shared across nested element calls. Each struct is
// walked once per top-level call: its relative leaf paths are identical
// for every caller prefix, so completed entries are reused by every
// later re-walk (sibling fields, promoted aliases, container elements)
// and the walk stays polynomial and deterministic. The former
// backtracking seen set re-walked shared subtrees and could diverge on
// type shapes whose walk regenerates the same relative leaves under
// ever-growing prefixes (observed as unbounded paramLeafPathsSeen
// recursion in the mutation battery; SOW-0026 tracks the typed-analyzer
// follow-up).
func paramLeafPathsSeen(t types.Type, memo map[*types.Struct]*leafPathsMemo) map[string]types.Type {
	st, ok := derefStruct(t)
	if !ok {
		return nil // not a struct: no leaves (range over nil is empty)
	}
	if m := memo[st]; m != nil {
		if m.paths == nil {
			return nil // recursion through a self-referencing field
		}
		return m.paths
	}
	if len(memo) >= leafWalkBudget {
		panic(leafWalkDivergence{steps: len(memo)})
	}
	m := &leafPathsMemo{}
	memo[st] = m
	out := map[string]types.Type{}
	// add copies a memoized relative-leaf set into out, prepending the
	// caller prefix exactly like the former prefix-threaded walk did.
	add := func(prefix string, rel map[string]types.Type) {
		for path, ft := range rel {
			if prefix != "" {
				out[prefix+"."+path] = ft
			} else {
				out[path] = ft
			}
		}
	}
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		p := f.Name()
		if _, isSt := derefStruct(f.Type()); isSt {
			add(p, paramLeafPathsSeen(f.Type(), memo))
			// Promoted leaves of an embedded struct also bind without
			// the type-name segment (o.Data with Data declared on an
			// embedded inner struct, through any number of embedding
			// levels): the callee's paramField sources name the promoted
			// path the field read resolved, so the fallback must expose
			// the alias or take(o) loses the caller's taint.
			if f.Anonymous() {
				add("", paramLeafPathsSeen(f.Type(), memo))
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
		// also exposes its KEY leaves for key-only ranges.
		fieldSeen := map[types.Type]bool{}
		for et := containerElemType(f.Type()); et != nil && !fieldSeen[et]; et = containerElemType(et) {
			fieldSeen[et] = true
			for path, ft := range paramLeafPathsSeen(et, memo) {
				if !paramCanCarryPage(ft) {
					continue
				}
				out[p+"."+path] = ft
			}
		}
		if mft := mapUnderlying(f.Type()); mft != nil {
			for path, ft := range paramLeafPathsSeen(mft.Key(), memo) {
				if !paramCanCarryPage(ft) {
					continue
				}
				out[p+"."+path] = ft
			}
			keySeen := map[types.Type]bool{}
			for et := containerElemType(mft.Key()); et != nil && !keySeen[et]; et = containerElemType(et) {
				keySeen[et] = true
				for path, ft := range paramLeafPathsSeen(et, memo) {
					if !paramCanCarryPage(ft) {
						continue
					}
					out[p+"."+path] = ft
				}
			}
		}
	}
	m.paths = out
	return out
}

// leafWalkBudget bounds one top-level paramLeafPaths call. The memo
// keeps every terminating type graph polynomial (one walk per distinct
// struct), so the budget only fires when a scanned or dependency type
// family fabricates a fresh identity on every descent (for example a
// pointer-parameterized self-reference such as Sub []P[*T]) and would
// otherwise grow the memo without bound. Exceeding it aborts the scan
// of the affected OS config fail-closed (no silent partial results).
const leafWalkBudget = 1 << 20

// leafWalkDivergence is the sentinel panic for a budget-exhausted leaf
// walk; scanRoot recovers it per OS config and reports the scan as
// failed.
type leafWalkDivergence struct{ steps int }

// leafPathsMemo caches the relative page-carrying leaf paths of one
// struct. A nil paths value marks the struct as in progress on the
// current walk path: a cycle edge returns nothing, exactly like the
// former per-walk seen set, while completed entries are reused by every
// later re-walk instead of regenerating the same relative paths.
type leafPathsMemo struct {
	paths map[string]types.Type
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
