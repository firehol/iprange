package main

// Complete-page ownership battery: the external-review P1 gap. Every form
// below must make the gate fail: a mapped page view (mapping.Page /
// mapping.View / reader.page results) copied, appended, or converted into
// an owned buffer at or above PageSize (binary-format-v4.md:108). The
// benign twins bound the copy below a complete page and stay legal.

var batteryPageCases = []batteryCase{
	{name: "P1: copy of a full mapped page into an owned [4096]byte", desc: "copy(page, [4096]byte) in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_pagecopy.go", content: "package mapping\n\nfunc pageCopyProbe(m *Mapping) ([4096]byte, error) {\n\tpage, err := m.Page(0)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tcopy(out[:], page)\n\treturn out, nil\n}"},
	}},

	{name: "P2: append of a full mapped page into an owned buffer", desc: "append(owned, page...) in the reader core", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_pageappend.go", content: "package reader\n\nimport \"github.com/firehol/iprange/v4/go/internal/mapping\"\n\nfunc pageAppendProbe(m *mapping.Mapping) ([]byte, error) {\n\tpage, err := m.Page(0)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 8192)\n\tout = append(out, page...)\n\treturn out, nil\n}"},
	}},

	{name: "P3: full-page View copied into an owned [4096]byte", desc: "copy(a[:], m.View(0, format.PageSize))", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_viewcopy.go", content: "package reader\n\nimport (\n\t\"github.com/firehol/iprange/v4/go/internal/format\"\n\t\"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\nfunc viewCopyProbe(m *mapping.Mapping) ([4096]byte, error) {\n\tv, err := m.View(0, format.PageSize)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tcopy(out[:], v)\n\treturn out, nil\n}"},
	}},

	{name: "P4: array conversion of a full mapped page", desc: "[4096]byte(page) in the reader core", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_pagearr.go", content: "package reader\n\nimport \"github.com/firehol/iprange/v4/go/internal/mapping\"\n\nfunc pageArrayProbe(m *mapping.Mapping) ([4096]byte, error) {\n\tpage, err := m.Page(0)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\treturn [4096]byte(page), nil\n}"},
	}},

	{name: "P5: copy of a reader.page result into an owned [4096]byte", desc: "copy(a[:], r.page(pgno)) through the reader owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_rpagecopy.go", content: "package reader\n\nfunc rPageCopyProbe(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tcopy(out[:], page)\n\treturn out, nil\n}"},
	}},

	{name: "P6: benign bounded record copy below a complete page", desc: "bounded record copy below a complete page stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_boundedcopy.go", content: "package reader\n\nimport \"github.com/firehol/iprange/v4/go/internal/mapping\"\n\nfunc boundedCopyProbe(m *mapping.Mapping) ([]byte, error) {\n\tpage, err := m.Page(0)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\trec := page[48:112]\n\tout := make([]byte, 64)\n\tcopy(out, rec)\n\treturn out, nil\n}"},
	}},

	{name: "P7: benign bounded metadata-chunk append stays legal", desc: "append of a decoded metadata chunk keeps its 4048 bound", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chunkappend.go", content: "package reader\n\nimport \"github.com/firehol/iprange/v4/go/internal/format\"\n\nfunc chunkAppendProbe(page []byte) []byte {\n\tchunk, err := format.DecodeMetadataChunk(page)\n\tif err != nil {\n\t\treturn nil\n\t}\n\tout := make([]byte, 0, 8192)\n\tout = append(out, chunk.Data...)\n\treturn out\n}"},
	}},

	{name: "P8: full mapped page through a function variable", desc: "stdlib func bound into a same-package var and called with a page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fnvarpage.go", content: "package reader\n\nimport \"bytes\"\n\nvar clonePage = bytes.Clone\n\nfunc fnVarPageProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn clonePage(page), nil\n}"},
	}},

	{name: "P9: benign function variable without a page argument", desc: "func var called with an owned buffer stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fnvarplain.go", content: "package reader\n\nimport \"bytes\"\n\nvar clonePlain = bytes.Clone\n\nfunc fnVarPlainProbe() []byte {\n\treturn clonePlain(make([]byte, 64))\n}"},
	}},

	{name: "P10: benign same-package call carries a mapped page", desc: "direct same-package func call with a page view stays legal (body scanned)", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_pkgfnpage.go", content: "package reader\n\nfunc pagePassThrough(page []byte) []byte { return page }\n\nfunc pkgFnPageProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn pagePassThrough(page), nil\n}"},
	}},

	{name: "P11: full page copied inside a defer closure", desc: "defer func(){ copy(out[:], page) }() in the reader core", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_deferpage.go", content: "package reader\n\nfunc deferPageProbe(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tdefer func() { copy(out[:], page) }()\n\treturn out, nil\n}"},
	}},

	{name: "P12: full page appended inside a directly called closure", desc: "func(){ return append(owned, page...) }() in the reader core", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_iifepage.go", content: "package reader\n\nfunc iifePageProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn func() []byte { return append([]byte{}, page...) }(), nil\n}"},
	}},

	{name: "P13: full page copied inside a go closure", desc: "go func(){ copy(out[:], page) }() in the reader core", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_gopage.go", content: "package reader\n\nfunc goPageProbe(r *ImmutableReader, pgno uint32) [4096]byte {\n\tpage, perr := r.page(pgno)\n\tif perr != nil {\n\t\treturn [4096]byte{}\n\t}\n\tvar out [4096]byte\n\tgo func() { copy(out[:], page) }()\n\treturn out\n}"},
	}},

	{name: "P14: benign bounded copy inside a defer closure", desc: "defer func(){ copy(out, page[48:112]) }() stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_deferok.go", content: "package reader\n\nfunc deferOkProbe(r *ImmutableReader, pgno uint32) []byte {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil\n\t}\n\trec := page[48:112]\n\tout := make([]byte, 64)\n\tdefer func() { copy(out, rec) }()\n\treturn out\n}"},
	}},

	{name: "P15: benign same-package function bound into a var with a page", desc: "var f = samePkgFn; f(page) stays legal (body scanned)", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_pkgvar.go", content: "package reader\n\nfunc pagePassThrough(page []byte) []byte { return page }\n\nvar passThrough = pagePassThrough\n\nfunc pkgVarProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn passThrough(page), nil\n}"},
	}},

	{name: "P16: full page through a func-literal variable", desc: "var f = func(p){ copy(out,p) }; f(page) in the reader core", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fnlitvar.go", content: "package reader\n\nvar cloneLit = func(page []byte) []byte {\n\tout := make([]byte, len(page))\n\tcopy(out, page)\n\treturn out\n}\n\nfunc fnLitVarProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn cloneLit(page), nil\n}"},
	}},

	{name: "P17: benign func-literal variable with a bounded slice", desc: "var f = func(p){ copy(out,p) }; f(page[48:112]) stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fnlitvarok.go", content: "package reader\n\nvar cloneLitB = func(page []byte) []byte {\n\tout := make([]byte, len(page))\n\tcopy(out, page)\n\treturn out\n}\n\nfunc fnLitVarProbeB(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn cloneLitB(page[48:112]), nil\n}"},
	}},

	{name: "P18: reassigned func-literal variable with a page", desc: "var f = func(p){ return p }; f = bytes.Clone; f(page) rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_reassignfn.go", content: "package reader\n\nimport \"bytes\"\n\nvar cloneLitR = func(page []byte) []byte {\n\treturn page\n}\n\nfunc fnLitVarProbeR(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tcloneLitR = bytes.Clone\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn cloneLitR(page), nil\n}"},
	}},

	{name: "P19: full page through a two-hop func-literal variable chain", desc: "var a = func(p){ copy(out,p) }; var b = a; b(page) rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chainfn.go", content: "package reader\n\nvar cloneChainA = func(page []byte) []byte {\n\tout := make([]byte, len(page))\n\tcopy(out, page)\n\treturn out\n}\n\nvar cloneChainB = cloneChainA\n\nfunc chainFnProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn cloneChainB(page), nil\n}"},
	}},

	{name: "P20: benign two-hop chain with a bounded slice", desc: "var a = func(p){ copy(out,p) }; var b = a; b(page[48:112]) stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chainfnok.go", content: "package reader\n\nvar cloneChainC = func(page []byte) []byte {\n\tout := make([]byte, len(page))\n\tcopy(out, page)\n\treturn out\n}\n\nvar cloneChainD = cloneChainC\n\nfunc chainFnOkProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn cloneChainD(page[48:112]), nil\n}"},
	}},
	{name: "P21: range reassignment of a func-literal variable with a page", desc: "for _, f = range fs; f(page) with fs={bytes.Clone} rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_rangereassign.go", content: "package reader\n\nimport \"bytes\"\n\nvar cloneRangeF = func(p []byte) []byte { return p }\n\nvar cloneRangeFS = []func([]byte) []byte{bytes.Clone}\n\nfunc rangeReassignProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n	page, err := r.page(pgno)\n	if err != nil {\n		return nil, err\n	}\n	var out []byte\n	for _, cloneRangeF = range cloneRangeFS {\n		out = cloneRangeF(page)\n	}\n	return out, nil\n}"},
	}},
	{name: "P22: address-taken store rebinds a func-literal variable", desc: "p := &f; *p = bytes.Clone; f(page) rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ptrreassign.go", content: "package reader\n\nimport \"bytes\"\n\nvar clonePtrF = func(p []byte) []byte { return p }\n\nfunc ptrReassignProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n	p := &clonePtrF\n	*p = bytes.Clone\n	page, err := r.page(pgno)\n	if err != nil {\n		return nil, err\n	}\n	return clonePtrF(page), nil\n}"},
	}},
	{name: "P23: benign range rebinding without a page call", desc: "for _, f = range fs { _ = f } without a mapped-page argument stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_rangenocall.go", content: "package reader\n\nvar cloneRangeG = func(p []byte) []byte { return p }\n\nvar cloneRangeGS = []func([]byte) []byte{cloneRangeG}\n\nfunc rangeNoCallProbe() int {\n	n := 0\n	for _, cloneRangeG = range cloneRangeGS {\n		n += len(cloneRangeG(make([]byte, 8)))\n	}\n	return n\n}"},
	}},
	{name: "P24: append through a same-package pass-through helper", desc: "append(owned, helper(page)...) must not lose the page taint", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_helperappend.go", content: "package reader\n\nfunc pagePassThrough24(page []byte) []byte { return page }\n\nfunc helperAppendProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, pagePassThrough24(page)...), nil\n}"},
	}},
	{name: "P25: append through a local closure", desc: "id := func(p) { return p }; append(owned, id(page)...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_closureappend.go", content: "package reader\n\nfunc closureAppendProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tid := func(p []byte) []byte { return p }\n\treturn append([]byte{}, id(page)...), nil\n}"},
	}},
	{name: "P26: append through a package func-alias variable", desc: "var f = samePkgFn; append(owned, f(page)...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasappend.go", content: "package reader\n\nfunc pagePassThrough26(page []byte) []byte { return page }\n\nvar passAlias26 = pagePassThrough26\n\nfunc aliasAppendProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, passAlias26(page)...), nil\n}"},
	}},
	{name: "P27: append through element extraction", desc: "firstElem(p[0]) of a page-carrying slice must keep the page taint", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_elemappend.go", content: "package reader\n\nfunc firstElem27(p [][]byte) []byte { return p[0] }\n\nfunc elemAppendProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, firstElem27([][]byte{page})...), nil\n}"},
	}},
	{name: "P28: append through a pointer-dereferenced page", desc: "deref(*p) of a page pointer must keep the page taint", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_derefappend.go", content: "package reader\n\nfunc deref28(p *[]byte) []byte { return *p }\n\nfunc derefAppendProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, deref28(&page)...), nil\n}"},
	}},
	{name: "P29: append through a generic identity", desc: "gid[T ~[]byte](p) T must keep the page taint", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genericappend.go", content: "package reader\n\nfunc gid29[T ~[]byte](p T) T { return p }\n\nfunc genericAppendProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, gid29(page)...), nil\n}"},
	}},
	{name: "P30: post-loop append after a branch-tainted accumulator", desc: "loop { if i==0 { out = append(out, page...) } }; append(owned, out...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_loopjoinappend.go", content: "package reader\n\nfunc loopJoinProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar out []byte\n\ti := 0\n\tfor i < 2 {\n\t\tif i == 0 {\n\t\t\tout = append(out, page...)\n\t\t} else {\n\t\t\tout = nil\n\t\t}\n\t\ti++\n\t}\n\tout2 := append([]byte{}, out...)\n\treturn out2, nil\n}"},
	}},
	{name: "P31: multi-result call distributed per slot", desc: "_, p := split(page); append(owned, p...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_multiresultappend.go", content: "package reader\n\nfunc split31(p []byte) ([]byte, []byte) { return p[48:112], p }\n\nfunc multiResultProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\t_, p := split31(page)\n\treturn append([]byte{}, p...), nil\n}"},
	}},
	{name: "P32: named array conversion of a full page", desc: "type A [4096]byte; A(page) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedarrconv.go", content: "package reader\n\ntype pageArr32 [4096]byte\n\nfunc namedArrProbe(r *ImmutableReader, pgno uint32) (pageArr32, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn pageArr32{}, err\n\t}\n\treturn pageArr32(page), nil\n}"},
	}},
	{name: "P33: named string conversion of a full page", desc: "type S string; S(page) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedstrconv.go", content: "package reader\n\ntype pageStr33 string\n\nfunc namedStrProbe(r *ImmutableReader, pgno uint32) (pageStr33, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\treturn pageStr33(page), nil\n}"},
	}},
	{name: "P34: package func-literal variable returning a file", desc: "var f = func() *os.File { return os.Stdout }; f() outside the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fnlitfile.go", content: "package reader\n\nimport \"os\"\n\nvar pkgFile34 = func() *os.File { return os.Stdout }\n\nfunc fnLitFileProbe() *os.File { return pkgFile34() }"},
	}},
	{name: "P35: bounded View(0, 64) copy stays legal", desc: "copy(out, m.View(0, 64)) must not be treated as a full page", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_boundedview.go", content: "package reader\n\nimport \"github.com/firehol/iprange/v4/go/internal/mapping\"\n\nfunc boundedViewProbe(m *mapping.Mapping) ([]byte, error) {\n\tv, err := m.View(0, 64)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 64)\n\tcopy(out, v)\n\treturn out, nil\n}"},
	}},
	{name: "P36: bounded slice through a local closure stays legal", desc: "id(page[48:112]) appended stays below a complete page", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_closurebounded.go", content: "package reader\n\nfunc closureBoundedProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tid := func(p []byte) []byte { return p }\n\treturn append([]byte{}, id(page[48:112])...), nil\n}"},
	}},
	{name: "P37: bounded multi-result slot stays legal", desc: "b, _ := split(page); append(owned, b...) with b = page[48:112] stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_multiresultbounded.go", content: "package reader\n\nfunc split37(p []byte) ([]byte, []byte) { return p[48:112], p }\n\nfunc multiResultBoundedProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb, _ := split37(page)\n\treturn append([]byte{}, b...), nil\n}"},
	}},
	{name: "P38: bounded append through a loop stays legal", desc: "loop { out = append(out, page[48:112]...) }; append(owned, out...) stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_loopbounded.go", content: "package reader\n\nfunc loopBoundedProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar out []byte\n\tfor i := 0; i < 2; i++ {\n\t\tout = append(out, page[48:112]...)\n\t}\n\treturn append([]byte{}, out...), nil\n}"},
	}},
	{name: "P39: helper summary keeps any tainted source (choose)", desc: "choose(nil, page, true) returns the page through a two-source summary and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_choose.go", content: "package reader\n\nfunc choose39(a, b []byte, takeB bool) []byte {\n\tif takeB {\n\t\treturn b\n\t}\n\treturn a\n}\n\nfunc chooseProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, choose39(nil, page, true)...), nil\n}"},
	}},
	{name: "P40: named result with a naked return carries page taint", desc: "func pass(p []byte) (out []byte) { out = p; return } appended must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedret.go", content: "package reader\n\nfunc pass40(p []byte) (out []byte) {\n\tout = p\n\treturn\n}\n\nfunc namedRetProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, pass40(page)...), nil\n}"},
	}},
	{name: "P41: multi-result struct field keeps page taint", desc: "c, err := multi(page); append(owned, c.Data...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_multistruct.go", content: "package reader\n\ntype chunk41 struct{ Data []byte }\n\nfunc multi41(p []byte) (chunk41, error) { return chunk41{Data: p}, nil }\n\nfunc multiStructProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tc, err2 := multi41(page)\n\tif err2 != nil {\n\t\treturn nil, err2\n\t}\n\treturn append([]byte{}, c.Data...), nil\n}"},
	}},
	{name: "P42: void unknown callback receiving a full page", desc: "var fn func([]byte); fn(page) with an unproven void callee must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_voidcallback.go", content: "package reader\n\nfunc voidCallbackProbe(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tvar fn func([]byte)\n\tfn(page)\n\treturn nil\n}"},
	}},
	{name: "P43: append into a complete mapped page view", desc: "append(page[0:4096:4096], page[:1]...) reallocates the full page into owned memory and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_appenddest.go", content: "package reader\n\nfunc appendDestProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append(page[0:4096:4096], page[:1]...), nil\n}"},
	}},
	{name: "P44: element store into a local container", desc: "slots[0] = page; append(owned, slots[0]...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_indexstore.go", content: "package reader\n\nfunc indexStoreProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tslots := make([][]byte, 1)\n\tslots[0] = page\n\treturn append([]byte{}, slots[0]...), nil\n}"},
	}},
	{name: "P45: dereference store into a pointed-to variable", desc: "*h = page; append(owned, *h...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_derefstore.go", content: "package reader\n\nfunc derefStoreProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar holder []byte\n\th := &holder\n\t*h = page\n\treturn append([]byte{}, *h...), nil\n}"},
	}},
	{name: "P46: range variable over a page collection", desc: "for _, p := range [][]byte{page} { append(owned, p...) } must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_rangevar.go", content: "package reader\n\nfunc rangeVarProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar out []byte\n\tfor _, p := range [][]byte{page} {\n\t\tout = append(out, p...)\n\t}\n\treturn out, nil\n}"},
	}},
	{name: "P47: interface boxing conversion keeps page taint", desc: "x := any(page); append(owned, x.([]byte)...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_anyconv.go", content: "package reader\n\nfunc anyConvProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := any(page)\n\treturn append([]byte{}, x.([]byte)...), nil\n}"},
	}},
	{name: "P48: interface-typed identity helper keeps page taint", desc: "idAny(page).([]byte) through func idAny(v any) any must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_anyident.go", content: "package reader\n\nfunc idAny48(v any) any { return v }\n\nfunc anyIdentProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, idAny48(page).([]byte)...), nil\n}"},
	}},
	{name: "P49: channel round trip keeps page taint", desc: "ch <- page; p := <-ch; append(owned, p...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chan.go", content: "package reader\n\nfunc chanProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tch := make(chan []byte)\n\tch <- page\n\tp := <-ch\n\treturn append([]byte{}, p...), nil\n}"},
	}},
	{name: "P50: switch fallthrough carries the previous case state", desc: "case 0 assigns the page, fallthrough into case 1 appends it; must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fallthrough.go", content: "package reader\n\nfunc fallProbe(r *ImmutableReader, pgno uint32, n int) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar out []byte\n\tswitch n {\n\tcase 0:\n\t\tout = page\n\t\tfallthrough\n\tcase 1:\n\t\treturn append([]byte{}, out...), nil\n\t}\n\treturn nil, nil\n}"},
	}},
	{name: "P51: nested func-literal result context must not leak", desc: "func leak(f *os.File) any with an inner func() *os.File returning nil; the outer return f must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_nestedlit.go", content: "package reader\n\nimport \"os\"\n\nfunc leakFile51(f *os.File) any {\n\t_ = func() *os.File { return nil }\n\treturn f\n}"},
	}},
	{name: "P52: unproven package func var with an interface result", desc: "var makeR func() io.Reader; makeR() outside the mapping owner must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ifacereader.go", content: "package reader\n\nimport \"io\"\n\nvar makeR52 func() io.Reader\n\nfunc ifaceReaderProbe() {\n\t_ = makeR52()\n}"},
	}},
	{name: "P53: choose with a bounded second source stays legal", desc: "choose(nil, page[48:112], true) appended stays below a complete page", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_choosebounded.go", content: "package reader\n\nfunc chooseB53(a, b []byte, takeB bool) []byte {\n\tif takeB {\n\t\treturn b\n\t}\n\treturn a\n}\n\nfunc chooseBoundedProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, chooseB53(nil, page[48:112], true)...), nil\n}"},
	}},
	{name: "P54: fallthrough with a bounded assignment stays legal", desc: "case 0 assigns a bounded view, fallthrough into case 1 appends it; stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fallbounded.go", content: "package reader\n\nfunc fallBoundedProbe(r *ImmutableReader, pgno uint32, n int) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar out []byte\n\tswitch n {\n\tcase 0:\n\t\tout = page[48:112]\n\t\tfallthrough\n\tcase 1:\n\t\treturn append([]byte{}, out...), nil\n\t}\n\treturn nil, nil\n}"},
	}},
	{name: "P55: named result with a bounded view stays legal", desc: "func pass(p []byte) (out []byte) { out = p[48:112]; return } appended stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedbounded.go", content: "package reader\n\nfunc passB55(p []byte) (out []byte) {\n\tout = p[48:112]\n\treturn\n}\n\nfunc namedBoundedProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, passB55(page)...), nil\n}"},
	}},
	{name: "P56: cross-file package func var with an interface result", desc: "var factory func() any declared in one file and called from another file of the same package outside the mapping owner must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_xfvar.go", content: "package reader\n\nvar factoryXF56 func() any\n"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_xfuse.go", content: "package reader\n\nfunc crossFileProbe56(r *ImmutableReader, pgno uint32) error {\n\t_ = factoryXF56()\n\treturn nil\n}"},
	}},
	{name: "P57: scalar-result unproven callback receiving a full page", desc: "useCb1(cb func([]byte) int, page) with an unproven callback must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_scalarcb.go", content: "package reader\n\nfunc useCb1(cb func([]byte) int, page []byte) int {\n\treturn cb(page)\n}\n\nfunc scalarCbProbe(r *ImmutableReader, pgno uint32) int {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn -1\n\t}\n\treturn useCb1(func(p []byte) int { return len(p) }, page)\n}"},
	}},
	{name: "P58: named func type with an interface result", desc: "type factoryF2 func() any; var makeF2 factoryF2; makeF2() outside the mapping owner must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedfn.go", content: "package reader\n\ntype factoryF2 func() any\n\nvar makeF2 factoryF2\n\nfunc namedFuncVarProbe2() {\n\t_ = makeF2()\n}"},
	}},
	{name: "P66: literal-bound func var reassigned to a non-literal stays fail-closed", desc: "a package func var bound to a literal and later rebound to another func-typed value has an unknowable callee; its interface-result call must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_relit.go", content: "package reader\n\nvar fRelit = func() any { return nil }\nvar fOther = func() any { return nil }\n\nfunc relitProbe() {\n\tfRelit = fOther\n\t_ = fRelit()\n}\n"},
	}},

	{name: "P67: opaque function-field callee receiving a full page", desc: "h.cb(page) with cb a function-typed struct field has an unknowable body and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fieldcb.go", content: "package reader\n\ntype cbH67 struct{ cb func([]byte) int }\n\nfunc useCbField67(h cbH67, page []byte) int { return h.cb(page) }\n\nfunc fieldCbProbe67(r *ImmutableReader, pgno uint32) int {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn -1\n\t}\n\treturn useCbField67(cbH67{cb: func(p []byte) int { return len(p) }}, page)\n}"},
	}},
	{name: "P68: slice-indexed callee receiving a full page", desc: "fs[0](page) with fs []func([]byte) int has an unknowable body and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_slicecb.go", content: "package reader\n\nfunc useCbSlice68(fs []func([]byte) int, page []byte) int { return fs[0](page) }\n\nfunc sliceCbProbe68(r *ImmutableReader, pgno uint32) int {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn -1\n\t}\n\treturn useCbSlice68([]func([]byte) int{func(p []byte) int { return len(p) }}, page)\n}"},
	}},
	{name: "P69: func literal with a named result returned naked", desc: "var f = func(p []byte) (out []byte) { out = p; return }; append(owned, f(page)...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedret.go", content: "package reader\n\nvar fNamed69 = func(p []byte) (out []byte) { out = p; return }\n\nfunc namedRetProbe69(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, fNamed69(page)...), nil\n}"},
	}},
	{name: "P70: pointer struct literal field taint", desc: "return &B{Data: p} then append(owned, makeB(page).Data...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ptrstruct.go", content: "package reader\n\ntype pBox70 struct{ Data []byte }\n\nfunc makePB70(p []byte) *pBox70 { return &pBox70{Data: p} }\n\nfunc ptrStructProbe70(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, makePB70(page).Data...), nil\n}"},
	}},
	{name: "P71: page through an any container and a type assertion", desc: "map[string]any{\"x\": page} -> x.([]byte) appended must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_typeassert.go", content: "package reader\n\nfunc mapToAny71(m map[string]any) any { return m[\"x\"] }\n\nfunc typeAssertProbe71(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := mapToAny71(map[string]any{\"x\": page})\n\treturn append([]byte{}, x.([]byte)...), nil\n}"},
	}},
	{name: "P72: collection literal keeps a definite element bound", desc: "first([][]byte{page[0:4096]}) appended must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_collbound.go", content: "package reader\n\nfunc firstL72(xs [][]byte) []byte { return xs[0] }\n\nfunc collKnownProbe72(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := firstL72([][]byte{page[0:4096]})\n\treturn append([]byte{}, x...), nil\n}"},
	}},
	{name: "P73: package global write visible across functions", desc: "a global assigned a page in one function and read by another must stay tainted (summary fixpoint)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_globalwrite.go", content: "package reader\n\nvar latePage73 []byte\n\nfunc getLateL73() []byte { return latePage73 }\n\nfunc lateInitProbe73(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tlatePage73 = page\n\treturn append([]byte{}, getLateL73()...), nil\n}"},
	}},
	{name: "P74: string conversion of a page view with an unknown bound", desc: "string(page[0:n]) with runtime n up to page size must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_stringunk.go", content: "package reader\n\nfunc stringUnknownProbe74(r *ImmutableReader, pgno uint32, n int) (string, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\tv := page[0:n]\n\treturn string(v), nil\n}"},
	}},
	{name: "P75: reflect byte extraction over a mapped view", desc: "reflect.ValueOf(page).Bytes() hands out the underlying mapped bytes and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_reflect.go", content: "package reader\n\nimport \"reflect\"\n\nfunc reflectProbe75(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := reflect.ValueOf(page).Bytes()\n\treturn append([]byte{}, b...), nil\n}"},
	}},

	{name: "P59: map and channel parameters carrying a page", desc: "m[\"x\"] and <-ch return the mapped page through map/chan parameters and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_mapchan.go", content: "package reader\n\nfunc getM3(m map[string][]byte) []byte { return m[\"x\"] }\n\nfunc recvC3(ch chan []byte) []byte { return <-ch }\n\nfunc mapChanCarrierProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\ta := append([]byte{}, getM3(map[string][]byte{\"x\": page})...)\n\tch := make(chan []byte, 1)\n\tch <- page\n\tb := append([]byte{}, recvC3(ch)...)\n\treturn append(a, b...), nil\n}"},
	}},
	{name: "P60: second struct-result slot keeps its field taint", desc: "_, s := split5(nil, page); append(owned, s.Data...) with the page in slot 1 must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_slot1.go", content: "package reader\n\ntype pairS5 struct{ Data []byte }\n\nfunc split5(a, b []byte) (pairS5, pairS5) { return pairS5{Data: a}, pairS5{Data: b} }\n\nfunc slot1StructProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\t_, s := split5(nil, page)\n\treturn append([]byte{}, s.Data...), nil\n}"},
	}},
	{name: "P61: returned local struct variable keeps its field taint", desc: "box5(p) returning a local s := S{Data:p}; box5(page).Data appended must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_boxlocal.go", content: "package reader\n\ntype boxS5 struct{ Data []byte }\n\nfunc box5(p []byte) boxS5 {\n\ts := boxS5{Data: p}\n\treturn s\n}\n\nfunc localStructProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, box5(page).Data...), nil\n}"},
	}},
	{name: "P62: string conversion of a definite full-page view", desc: "string(page[0:4096]) copies a complete mapped page and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_stringfull.go", content: "package reader\n\nfunc stringFullProbe(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\t_ = string(page[0:4096])\n\treturn nil\n}"},
	}},
	{name: "P63: append into a multi-page mapped view", desc: "append(page[0:8192:8192], 1) reallocates the two-page span into owned memory and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_oversizeddest.go", content: "package reader\n\nfunc oversizedDestProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append(page[0:8192:8192], 1), nil\n}"},
	}},
	{name: "P64: owned byte-builder sink copies a full page", desc: "bytes.NewBuffer(page).Bytes() owns the mapped bytes and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bufferlaunder.go", content: "package reader\n\nimport \"bytes\"\n\nfunc bufferLaunderProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := bytes.NewBuffer(page)\n\treturn append([]byte{}, b.Bytes()...), nil\n}"},
	}},
	{name: "P65: unsafe import anywhere in the module", desc: "import \"unsafe\" in the mapping owner must be rejected (unsafe.Slice over a mapped descriptor escapes the type layer)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_unsafe.go", content: "package mapping\n\nimport \"unsafe\"\n\nvar _ = unsafe.Pointer(nil)"},
	}},
	{name: "P76: func literal with multi-named results returned naked", desc: "var f = func(p []byte) (a, b []byte) { a, b = p, p; return }; append(owned, f(page)...) must be rejected and must not crash the scan", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_multinamed.go", content: "package reader\n\nvar fMulti76 = func(p []byte) (a, b []byte) {\n\ta, b = p, p\n\treturn\n}\n\nfunc multiRetProbe76(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx, _ := fMulti76(page)\n\treturn append([]byte{}, x...), nil\n}"},
	}},
	{name: "P77: helper parameter converted to an owned string", desc: "toString(p) { return string(p) } with a full page at the call site copies the mapped bytes inside the callee and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_stringparam.go", content: "package reader\n\nfunc toString77(p []byte) string { return string(p) }\n\nfunc stringParamProbe77(r *ImmutableReader, pgno uint32) (string, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\treturn toString77(page), nil\n}"},
	}},
	{name: "P78: stale bounded call result across summary fixpoints", desc: "a call cached clean before the callee summary stabilizes must be re-evaluated; append of the late-tainted result must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_stalecall.go", content: "package reader\n\nvar lateSrc78 []byte\n\nfunc pick78() []byte { return lateSrc78 }\n\nfunc lateCallProbe78(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := append([]byte{}, pick78()...)\n\tlateSrc78 = page\n\treturn out, nil\n}"},
	}},
	{name: "P79: struct-field flow through dereferenced writes and indexed literal reads", desc: "(*b).Data = page followed by box.Data, and []B{{Data: page}}[0].Data, must keep the page taint and be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fieldshapes.go", content: "package reader\n\ntype box79 struct{ Data []byte }\n\nfunc derefFieldProbe79(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar box box79\n\tb := &box\n\t(*b).Data = page\n\treturn append([]byte{}, box.Data...), nil\n}\n\nfunc indexFieldProbe79(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := []box79{{Data: page}}[0].Data\n\treturn append([]byte{}, x...), nil\n}"},
	}},
	{name: "P80: package-global stores join bounds instead of last-writer", desc: "setFull(page) then setBound(page[0:1]) must leave the global conservatively full; the append must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_globaljoin.go", content: "package reader\n\nvar gBound80 []byte\n\nfunc setFull80(page []byte) { gBound80 = page }\n\nfunc setBound80(page []byte) { gBound80 = page[0:1] }\n\nfunc joinGlobalProbe80(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tsetFull80(page)\n\tsetBound80(page[0:1])\n\treturn append([]byte{}, gBound80...), nil\n}"},
	}},
	{name: "P81: []any and nested map/chan parameters carry pages", desc: "xs[0] of []any{page} and m[\"a\"][\"b\"] of nested maps must keep the taint and be rejected on append", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_nestedcarriers.go", content: "package reader\n\nfunc firstAny81(xs []any) any { return xs[0] }\n\nfunc nestedMap81(m map[string]map[string][]byte) []byte { return m[\"a\"][\"b\"] }\n\nfunc nestedCarrierProbe81(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := firstAny81([]any{page}).([]byte)\n\ta := append([]byte{}, x...)\n\ty := nestedMap81(map[string]map[string][]byte{\"a\": {\"b\": page}})\n\treturn append(a, y...), nil\n}"},
	}},
	{name: "P82: interface-method and call-produced dynamic callees", desc: "s.Apply(page) on an interface and factory()(page) on a call-produced func have unknowable bodies and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ifacecb.go", content: "package reader\n\ntype sink82 interface{ Apply([]byte) int }\n\ntype impl82 struct{}\n\nfunc (impl82) Apply(p []byte) int { return len(p) }\n\nfunc useIface82(s sink82, page []byte) int { return s.Apply(page) }\n\nfunc ifaceMethodProbe82(r *ImmutableReader, pgno uint32) int {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn -1\n\t}\n\treturn useIface82(impl82{}, page)\n}"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_callcb.go", content: "package reader\n\nfunc factory82() func([]byte) int { return func(p []byte) int { return len(p) } }\n\nfunc callOfCallProbe82(r *ImmutableReader, pgno uint32) int {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn -1\n\t}\n\treturn factory82()(page)\n}"},
	}},
	{name: "P83: method value stored in a local resolves its method summary", desc: "method value get := r.page; get(1) returns the mapped view through the method summary and the copy must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_1.go", content: "package reader\n\nfunc mvProbe83(r *ImmutableReader, pgno uint32) error {\n\tget := r.page\n\tpage, err := get(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tvar out [4096]byte\n\tcopy(out[:], page)\n\treturn nil\n}"},
	}},
	{name: "P84: method receiver field taint flows through the summary", desc: "box{Data: page}.Get() returns the receiver's Data field through the method summary and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_2.go", content: "package reader\n\ntype box84 struct{ Data []byte }\n\nfunc (b box84) Get() []byte { return b.Data }\n\nfunc methRecvProbe84(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, box84{Data: page}.Get()...), nil\n}"},
	}},
	{name: "P85: fmt variadic spread of a concrete page collection", desc: "args := []any{page}; fmt.Sprintf(\"%s\", args...) copies the full mapped view into an owned string and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_3.go", content: "package reader\n\nimport \"fmt\"\n\nfunc fmtSpreadProbe85(r *ImmutableReader, pgno uint32) (string, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\targs := []any{page}\n\treturn fmt.Sprintf(\"%s\", args...), nil\n}"},
	}},
	{name: "P86: defined string type conversion inside a helper", desc: "type S string; f(p) { return len(S(p)) } records the owned-string copy; a full page at the call site must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_4.go", content: "package reader\n\ntype S86 string\n\nfunc f86(p []byte) int { return len(S86(p)) }\n\nfunc namedStrProbe86(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\t_ = f86(page)\n\treturn nil\n}"},
	}},
	{name: "P87: function variable alias of a string-converting helper", desc: "var a = f; a(page) where f converts its parameter to a string inside a void helper must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_5.go", content: "package reader\n\nfunc f87(p []byte) { _ = string(p) }\n\nvar a87 = f87\n\nfunc aliasProbe87(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\ta87(page)\n\treturn nil\n}"},
	}},
	{name: "P88: clean store to one struct field must not shadow another field's page", desc: "sink(box{Data: page}) writes b.Other clean and returns b.Data; the caller's append must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_6.go", content: "package reader\n\ntype box88 struct{ Data, Other []byte }\n\nfunc sink88(b box88) []byte { b.Other = []byte{1}; return b.Data }\n\nfunc fieldShadowProbe88(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, sink88(box88{Data: page})...), nil\n}"},
	}},
	{name: "P89: clean element store must not erase the container taint", desc: "slots[0] = page; slots[1] = []byte{0}; append(owned, slots[0]...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_7.go", content: "package reader\n\nfunc indexStoreProbe89(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tslots := make([][]byte, 2)\n\tslots[0] = page\n\tslots[1] = []byte{0}\n\treturn append([]byte{}, slots[0]...), nil\n}"},
	}},
	{name: "P90: indexed local container of structs keeps element field taint", desc: "xs := []box{{Data: page}}; append(owned, xs[0].Data...) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_8.go", content: "package reader\n\ntype box90 struct{ Data []byte }\n\nfunc idxNestedProbe90(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\txs := []box90{{Data: page}}\n\treturn append([]byte{}, xs[0].Data...), nil\n}"},
	}},
	{name: "P91: returning the address of a local tainted struct keeps its fields", desc: "s := box{Data: p}; return &s; the caller's box(page).Data copy must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_9.go", content: "package reader\n\ntype box91 struct{ Data []byte }\n\nfunc mk91(p []byte) *box91 {\n\ts := box91{Data: p}\n\treturn &s\n}\n\nfunc retAddrProbe91(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, mk91(page).Data...), nil\n}"},
	}},
	{name: "P92: page-bearing map composite keys keep the collection taint", desc: "m := map[*[]byte]int{&page: 1}; (*k)... of a range key must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_10.go", content: "package reader\n\nfunc mapKeyProbe92(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := map[*[]byte]int{&page: 1}\n\tfor k := range m {\n\t\treturn append([]byte{}, (*k)...), nil\n\t}\n\treturn nil, nil\n}"},
	}},
	{name: "P93: file-bearing value hidden in an interface-valued collection", desc: "func box(f *os.File) []any { return []any{f} } launders the descriptor into a non-file-bearing slot and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_11.go", content: "package reader\n\nimport \"os\"\n\nfunc box93(f *os.File) []any { return []any{f} }\n\nvar fileVar93 *os.File\n\nfunc leak93() { _ = box93(fileVar93) }"},
	}},
	{name: "P94: struct function field returning a file fails closed", desc: "type H struct{ get func() *os.File }; leak(h H) *os.File { return h.get() } dispatches to an unknowable body and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_12.go", content: "package reader\n\nimport \"os\"\n\ntype H94 struct{ get func() *os.File }\n\nfunc leak94(h H94) *os.File { return h.get() }"},
	}},
	{name: "P95: recursive carrier types terminate the type walk", desc: "type R []R parameters must not recurse the scanner without bound; the page copy in the same package must still be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_13.go", content: "package reader\n\ntype R95 []R95\n\nfunc recCarrier95(r R95, p []byte) []byte {\n\t_ = r\n\treturn append([]byte{}, p...)\n}\n\nfunc recUseProbe95(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn recCarrier95(nil, page), nil\n}"},
	}},
	{name: "P96: recursive carrier types without page flow stay accepted", desc: "type R []R, M map[string]M, C chan C, P *P with no page flow must not crash and must pass the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r9_14.go", content: "package reader\n\ntype R96 []R96\ntype M96 map[string]M96\ntype C96 chan C96\ntype P96 *P96\n\nfunc recOk96(r R96, m M96, c C96, p P96) bool {\n\t_ = r\n\t_ = m\n\t_ = c\n\treturn p != nil\n}"},
	}},
	{name: "P97: conditional clean store must not erase the branch-taken taint", desc: "if c { b.Data = []byte{1} }; return b.Data stays tainted from the argument's page source and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_1.go", content: "package reader\n\ntype boxR101 struct{ Data []byte }\n\nfunc condCleanR101(b boxR101, c bool) []byte {\n\tif c {\n\t\tb.Data = []byte{1}\n\t}\n\treturn b.Data\n}\n\nfunc condCleanProbeR101(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, condCleanR101(boxR101{Data: page}, false)...), nil\n}"},
	}},

	{name: "P98: local struct copy keeps field taint", desc: "c := b; return c.Data must keep the page taint of the argument struct and be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_2a.go", content: "package reader\n\ntype boxR102a struct{ Data []byte }\n\nfunc localCopyR102a(b boxR102a) []byte {\n\tc := b\n\treturn c.Data\n}\n\nfunc localCopyProbeR102a(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, localCopyR102a(boxR102a{Data: page})...), nil\n}"},
	}},

	{name: "P99: pointer alias of a parameter struct keeps field taint", desc: "q := &b; return q.Data must keep the page taint of the argument struct and be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_2b.go", content: "package reader\n\ntype boxR102b struct{ Data []byte }\n\nfunc ptrAliasR102b(b boxR102b) []byte {\n\tq := &b\n\treturn q.Data\n}\n\nfunc ptrAliasProbeR102b(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, ptrAliasR102b(boxR102b{Data: page})...), nil\n}"},
	}},

	{name: "P100: range over an inline struct literal keeps field taint", desc: "for _, x := range []box{{Data: p}}; returning x.Data must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_2c.go", content: "package reader\n\ntype boxR102c struct{ Data []byte }\n\nfunc rangeLitR102c(p []byte) []byte {\n\tfor _, x := range []boxR102c{{Data: p}} {\n\t\treturn x.Data\n\t}\n\treturn nil\n}\n\nfunc rangeLitProbeR102c(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, rangeLitR102c(page)...), nil\n}"},
	}},

	{name: "P101: method-expression alias must not misbind the receiver", desc: "get := box.Get; get(b) carries the struct argument into the method summary and the page copy must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_3.go", content: "package reader\n\ntype boxR103 struct{ Data []byte }\n\nfunc (b boxR103) Get() []byte { return b.Data }\n\nfunc methExprHelperR103(b boxR103) []byte {\n\tget := boxR103.Get\n\treturn append([]byte{}, get(b)...)\n}\n\nfunc methExprProbeR103(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, methExprHelperR103(boxR103{Data: page})...), nil\n}"},
	}},

	{name: "P102: closure struct-result fields keep the page taint", desc: "f := func() box { return box{Data: page} }; f().Data copied into owned memory must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_4.go", content: "package reader\n\ntype boxR104 struct{ Data []byte }\n\nfunc litFieldProbeR104(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func() boxR104 { return boxR104{Data: page} }\n\treturn append([]byte{}, f().Data...), nil\n}"},
	}},

	{name: "P103: interface byte-result method with unknown body fails closed", desc: "s.Capture() with a []byte result has no visible body; the caller's append must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_5.go", content: "package reader\n\ntype capturerR105 interface{ Capture() []byte }\n\ntype capImplR105 struct{ data []byte }\n\nfunc (c capImplR105) Capture() []byte { return c.data }\n\nfunc ifaceResultProbeR105(r *ImmutableReader, pgno uint32, s capturerR105) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\t_ = capImplR105{data: page}\n\treturn append([]byte{}, s.Capture()...), nil\n}"},
	}},

	{name: "P104: interface-method-expression binding fails closed", desc: "var put = sink.Put; put(s, page) must not bind the unverifiable method body and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_6.go", content: "package reader\n\ntype sinkR106 interface{ Put([]byte) int }\n\nvar putR106 = sinkR106.Put\n\nfunc aliasIfaceProbeR106(s sinkR106, page []byte) {\n\tputR106(s, page)\n}"},
	}},

	{name: "P105: method receiver string conversion is a full-page transfer", desc: "box{Data: page}.Str() converts the receiver field to an owned string and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_7.go", content: "package reader\n\ntype boxR107 struct{ Data []byte }\n\nfunc (b boxR107) Str() int {\n\t_ = string(b.Data)\n\treturn 0\n}\n\nfunc recvStrProbeR107(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tboxR107{Data: page}.Str()\n\treturn nil\n}"},
	}},

	{name: "P106: fmt variadic spread through a user helper fails closed", desc: "func fmtLaunder(a ...any) string { return fmt.Sprintf(\"%s\", a...) }; fmtLaunder(page) must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_8.go", content: "package reader\n\nimport \"fmt\"\n\nfunc fmtLaunderR108(a ...any) string { return fmt.Sprintf(\"%s\", a...) }\n\nfunc launderProbeR108(r *ImmutableReader, pgno uint32) (string, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\treturn fmtLaunderR108(page), nil\n}"},
	}},

	{name: "P107: struct with a file field into any fails closed", desc: "func leak(f *os.File) any { return H{F: f} } launders the descriptor into a non-file-bearing slot and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_9.go", content: "package reader\n\nimport \"os\"\n\ntype H109 struct{ F *os.File }\n\nfunc leakR109(f *os.File) any { return H109{F: f} }\n\nvar fileVarR109 *os.File\n\nfunc useR109() { _ = leakR109(fileVarR109) }"},
	}},

	{name: "P108: file-bearing map keys fail closed", desc: "m := map[any]int{file: 1} launders the descriptor through a map key and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_10.go", content: "package reader\n\nimport \"os\"\n\nvar fileKeyR110 *os.File\n\nfunc mapKeyLaunderR110() {\n\t_ = map[any]int{fileKeyR110: 1}\n}"},
	}},

	{name: "P109: append file into an interface collection fails closed", desc: "append([]any{}, file) launders the descriptor into a non-file-bearing element slot and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_11.go", content: "package reader\n\nimport \"os\"\n\nvar fileArgR111 *os.File\n\nfunc appendFileLaunderR111() {\n\t_ = append([]any{}, fileArgR111)\n}"},
	}},

	{name: "P110: range of file values into any fails closed", desc: "for _, x = range files { return x } assigns a descriptor into an any and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_12.go", content: "package reader\n\nimport \"os\"\n\nvar fileListR112 []*os.File\n\nfunc rangeIfaceR112() any {\n\tvar x any\n\tfor _, x = range fileListR112 {\n\t\treturn x\n\t}\n\treturn nil\n}"},
	}},

	{name: "P111: complete-page copy through the reader exemption must still be rejected", desc: "io.ReadFull(bytes.NewReader(page), out) copies the mapped page into an owned buffer despite the reader-arg exemption and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r10_13.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n\n\t\"github.com/firehol/iprange/v4/go/internal/format\"\n)\n\nfunc gateProbeR113(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tout := make([]byte, format.PageSize)\n\t_, err = io.ReadFull(bytes.NewReader(page), out)\n\treturn err\n}"},
	}},

	{name: "P112: branch-local func-literal binding divergence fails closed", desc: "reassigning a local func literal on one branch must not hide the page-returning binding (bounded probe)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_1.go", content: "package reader\n\nfunc branchLocalFuncProbeR111(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func() []byte { return []byte{1} }\n\tif page != nil {\n\t\tf = func() []byte { return page }\n\t}\n\treturn append([]byte{}, f()...), nil\n}"},
	}},

	{name: "P113: partial struct-local field store must not suppress caller field taints", desc: "writing one field clean locally must not erase the page taint of the untouched other field on a copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_2.go", content: "package reader\n\ntype boxR112 struct{ Data, Other []byte }\n\nfunc partialCopyR112(b boxR112) []byte {\n\tb.Data = []byte{1}\n\tc := b\n\treturn c.Other\n}\n\nfunc partialCopyProbeR112(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, partialCopyR112(boxR112{Data: page, Other: page})...), nil\n}"},
	}},

	{name: "P114: package-global struct stores keep field taints across functions", desc: "a pkg-global struct assigned a page in one helper must keep Data tainted for the reader function", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_3.go", content: "package reader\n\ntype boxR113 struct{ Data []byte }\n\nvar gR113 boxR113\n\nfunc setR113(page []byte) { gR113 = boxR113{Data: page} }\n\nfunc getR113() []byte { return gR113.Data }\n\nfunc globalStructProbeR113(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tsetR113(page)\n\treturn append([]byte{}, getR113()...), nil\n}"},
	}},

	{name: "P115: indexed struct stores keep element field taints", desc: "an element write must track the struct field taints onto the container so a later read of that element sees them", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_4.go", content: "package reader\n\ntype boxR114 struct{ Data []byte }\n\nfunc idxStructWriteProbeR114(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\txs := make([]boxR114, 2)\n\txs[0] = boxR114{Data: page}\n\txs[1] = boxR114{Data: []byte{1}}\n\treturn append([]byte{}, xs[0].Data...), nil\n}"},
	}},

	{name: "P116: nested and promoted struct fields keep page taints", desc: "o.Inner.Data and embedded-field promotion must resolve the flatten field path and stay tainted", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_5.go", content: "package reader\n\ntype innerR115 struct{ Data []byte }\n\ntype outerR115 struct{ Inner innerR115 }\n\nfunc nestedFieldProbeR115(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\to := outerR115{Inner: innerR115{Data: page}}\n\treturn append([]byte{}, o.Inner.Data...), nil\n}\n\ntype embedR115 struct{ innerR115 }\n\nfunc promotedFieldProbeR115(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\to := embedR115{innerR115{Data: page}}\n\treturn append([]byte{}, o.Data...), nil\n}"},
	}},

	{name: "P117: func-literal parameter struct fields bind from the caller", desc: "a closure parameter whose struct field is page-sourced must keep the taint into the append sink", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_6.go", content: "package reader\n\ntype boxR116 struct{ Data []byte }\n\nfunc litParamProbeR116(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func(x boxR116) []byte { return x.Data }\n\treturn append([]byte{}, f(boxR116{Data: page})...), nil\n}"},
	}},

	{name: "P118: struct var with a page field passed to a closure fails closed", desc: "a locally stored field (b.Data = page) must reach the closure parameter's field source", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_6b.go", content: "package reader\n\ntype boxR116b struct{ Data []byte }\n\nfunc litParamVarProbeR116b(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func(x boxR116b) []byte { return x.Data }\n\tvar b boxR116b\n\tb.Data = page\n\treturn append([]byte{}, f(b)...), nil\n}"},
	}},

	{name: "P119: opaque interface method converting a page-carrying receiver fails closed", desc: "an interface method with a string result over a receiver holding a complete page is a transfer at the call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_7.go", content: "package reader\n\ntype texterR117 interface{ Text() string }\n\ntype boxR117 struct{ Data []byte }\n\nfunc (b boxR117) Text() string { return string(b.Data) }\n\nfunc opaqueTextProbeR117(r *ImmutableReader, pgno uint32) int {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn -1\n\t}\n\tvar s texterR117 = boxR117{Data: page}\n\treturn len(s.Text())\n}"},
	}},

	{name: "P120: dynamic struct results and type-asserted struct fields fail closed", desc: "interface method struct results and v.(box) assertions must keep field taints into the append sink", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_8.go", content: "package reader\n\ntype boxR118 struct{ Data []byte }\n\ntype getterR118 interface{ Get() boxR118 }\n\nfunc dynStructResultProbeR118(g getterR118) []byte {\n\treturn append([]byte{}, g.Get().Data...)\n}\n\nfunc taStructProbeR118(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar v any = boxR118{Data: page}\n\treturn append([]byte{}, v.(boxR118).Data...), nil\n}"},
	}},

	{name: "P121: string-param conversion through a helper fails closed", desc: "s(box{Data: page}) with the callee converting the field to a string must be rejected at the call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_9.go", content: "package reader\n\ntype boxR119 struct{ Data []byte }\n\nfunc sR119(b boxR119) { _ = string(b.Data) }\n\nfunc strFieldSlotProbeR119(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tsR119(boxR119{Data: page})\n\treturn nil\n}"},
	}},

	{name: "P122: string-param conversion through a bound parameter fails closed", desc: "outer(box{Data: page}) delegating into the converting callee keeps the full-page call at the outermost site", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_9b.go", content: "package reader\n\ntype boxR119b struct{ Data []byte }\n\nfunc sR119b(b boxR119b) { _ = string(b.Data) }\n\nfunc outerR119b(b boxR119b) { sR119b(b) }\n\nfunc strFieldBoundProbeR119b(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\touterR119b(boxR119b{Data: page})\n\treturn nil\n}"},
	}},

	{name: "P123: runtime map key taint stays visible", desc: "m[&page] = 1 must keep the page reachable through the key range and the append sink", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_10.go", content: "package reader\n\nfunc mapIndexKeyProbeR1110(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := make(map[*[]byte]int)\n\tm[&page] = 1\n\tfor k := range m {\n\t\treturn append([]byte{}, (*k)...), nil\n\t}\n\treturn nil, nil\n}"},
	}},

	{name: "P124: struct-with-file into conversion/send/range fails closed", desc: "any(H{F: f}) and ch <- H{F: f} launder the descriptor and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_11.go", content: "package reader\n\nimport \"os\"\n\ntype H1111 struct{ F *os.File }\n\nvar fileVarR1111 *os.File\n\nfunc convLaunderR1111() any { return any(H1111{F: fileVarR1111}) }\n\nfunc sendLaunderR1111() {\n\tch := make(chan any, 1)\n\tch <- H1111{F: fileVarR1111}\n}"},
	}},

	{name: "P125: runtime map-key file store fails closed", desc: "m[fileKey] = 1 must be rejected like the literal map-key launder", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r11_12.go", content: "package reader\n\nimport \"os\"\n\nvar fileKeyR1112 *os.File\n\nfunc runtimeMapKeyProbeR1112() {\n\tm := make(map[any]int)\n\tm[fileKeyR1112] = 1\n}"},
	}},

	{name: "P126: variadic trailing-argument join fails closed", desc: "func pick(xs ...[]byte) []byte { return xs[1] }; pick(clean, page) must be rejected (any trailing arg can name the slot)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_1.go", content: "package reader\n\nfunc pickR121(xs ...[]byte) []byte { return xs[1] }\n\nfunc varargsProbeR121(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, pickR121([]byte{1}, page)...), nil\n}"},
	}},

	{name: "P127: var-decl struct initializer keeps field page taints", desc: "var b B = B{Data: page} must keep Data tainted into the append sink", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_2.go", content: "package reader\n\ntype boxR122 struct{ Data []byte }\n\nfunc varDeclFieldProbeR122(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar b boxR122 = boxR122{Data: page}\n\treturn append([]byte{}, b.Data...), nil\n}"},
	}},

	{name: "P128: nested selector stores keep dotted field paths", desc: "o.Inner.Data = page must keep the nested path tainted into the append sink", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_3.go", content: "package reader\n\ntype innerR123 struct{ Data []byte }\n\ntype outerR123 struct{ Inner innerR123 }\n\nfunc nestedStoreProbeR123(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar o outerR123\n\to.Inner.Data = page\n\treturn append([]byte{}, o.Inner.Data...), nil\n}"},
	}},

	{name: "P129: indexed call-result struct fields stay tainted", desc: "makeList(page)[0].Data must read the caller's page bound through the container result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_4.go", content: "package reader\n\ntype boxR124 struct{ Data []byte }\n\nfunc makeListR124(p []byte) []boxR124 { return []boxR124{{Data: p}} }\n\nfunc collResultProbeR124(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, makeListR124(page)[0].Data...), nil\n}"},
	}},

	{name: "P130: opaque callback with a field-only page value fails closed", desc: "cb(b) with b.Data = page must be rejected at the opaque call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_5.go", content: "package reader\n\ntype boxR125 struct{ Data []byte }\n\nfunc fieldOnlyCbProbeR125(r *ImmutableReader, pgno uint32, cb func(boxR125)) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tvar b boxR125\n\tb.Data = page\n\tcb(b)\n\treturn nil\n}"},
	}},

	{name: "P131: unknown-bound view into a struct field fails closed", desc: "m.View(0, n) keeps maxUnknown (not zero) so a struct field store cannot launder it", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_6.go", content: "package reader\n\nimport \"github.com/firehol/iprange/v4/go/internal/mapping\"\n\ntype boxR126 struct{ Data []byte }\n\nfunc unknownBoundStructProbeR126(m *mapping.Mapping, n int, cb func(boxR126)) {\n\tpage, _ := m.View(0, uint64(n))\n\tcb(boxR126{Data: page})\n}"},
	}},

	{name: "P132: string-param field conversion with a local struct var fails closed", desc: "var b B; b.Data = page; sink(b) where sink converts the field to a string must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_7.go", content: "package reader\n\ntype boxR127 struct{ Data []byte }\n\nfunc sinkR127(b boxR127) { _ = string(b.Data) }\n\nfunc fieldSlotCallProbeR127(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tvar b boxR127\n\tb.Data = page\n\tsinkR127(b)\n\treturn nil\n}"},
	}},

	{name: "P133: opaque interface and factory params returning any fail closed", desc: "an interface method with an any result and a func() any param must be rejected at the call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_8.go", content: "package reader\n\nimport \"os\"\n\nvar fileVarR128 *os.File\n\ntype mgrR128 interface{ Make() any }\n\nfunc ifaceFactoryProbeR128(m mgrR128) any { return m.Make() }\n\nfunc paramFactoryProbeR128b(factory func() any) any { return factory() }"},
	}},

	{name: "P134: package interface variable with an any result fails closed", desc: "mgrVar.Make() any on a package-level interface variable must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_8b.go", content: "package reader\n\ntype mgrR128b interface{ Make() any }\n\nvar mgrVarR128b mgrR128b\n\nfunc ifaceFactoryVarProbeR128b() any { return mgrVarR128b.Make() }"},
	}},

	{name: "P135: struct holding a file into an interface formal fails closed", desc: "sink(H{F: f}) with sink func(any) erases the struct and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_9.go", content: "package reader\n\nimport \"os\"\n\ntype H129 struct{ F *os.File }\n\nvar fileVarR129 *os.File\n\nfunc ifaceArgLaunderR129(sink func(any)) {\n\tsink(H129{F: fileVarR129})\n}"},
	}},

	{name: "P136: struct holding a file into a runtime map key fails closed", desc: "m[H{F: f}] = 1 with an any map key erases the struct and must be rejected", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_11.go", content: "package reader\n\nimport \"os\"\n\ntype H1211 struct{ F *os.File }\n\nvar fileVarR1211 *os.File\n\nfunc mapKeyStructProbeR1211() {\n\tm := make(map[any]int)\n\tm[H1211{F: fileVarR1211}] = 1\n}"},
	}},

	{name: "P137: if-nested func-literal reassignment divergence fails closed", desc: "if c1 { if c2 { f = page-returning } } must not hide the page binding", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_12.go", content: "package reader\n\nfunc nestedAmbigProbeR1212(r *ImmutableReader, pgno uint32, c1, c2 bool) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func() []byte { return []byte{1} }\n\tif c1 {\n\t\tif c2 {\n\t\t\tf = func() []byte { return page }\n\t\t}\n\t}\n\treturn append([]byte{}, f()...), nil\n}"},
	}},

	{name: "P138: else-nested func-literal reassignment divergence fails closed", desc: "else { if c2 { f = page-returning } } must not hide the page binding either", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_12c.go", content: "package reader\n\nfunc nestedAmbigElseProbeR1212c(r *ImmutableReader, pgno uint32, c1, c2 bool) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func() []byte { return page }\n\tif c1 {\n\t\tf = func() []byte { return []byte{1} }\n\t} else {\n\t\tif c2 {\n\t\t\tf = func() []byte { return page }\n\t\t}\n\t}\n\treturn append([]byte{}, f()...), nil\n}"},
	}},

	{name: "P139: package-initializer struct fields keep page taints", desc: "var g = B{Data: pageSrc} must stay tainted when pageSrc is assigned a page later", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_13.go", content: "package reader\n\ntype boxR1213 struct{ Data []byte }\n\nvar pageSrcR1213 []byte\n\nvar gR1213 = boxR1213{Data: pageSrcR1213}\n\nfunc pkgInitFieldProbeR1213(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tpageSrcR1213 = page\n\treturn append([]byte{}, gR1213.Data...), nil\n}"},
	}},

	{name: "P140: package method-value bindings keep receiver state", desc: "var get = holder.Get after set(page) must carry the page into the append sink", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r12_14.go", content: "package reader\n\ntype holderR1214 struct{ data []byte }\n\nvar holderVarR1214 holderR1214\n\nfunc (h *holderR1214) Get() []byte { return h.data }\n\nfunc setR1214(page []byte) { holderVarR1214.data = page }\n\nvar getR1214 = holderVarR1214.Get\n\nfunc pkgMethValProbeR1214(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tsetR1214(page)\n\treturn append([]byte{}, getR1214()...), nil\n}"},
	}},
	{name: "P141: variadic ...any interface erasure", desc: "sink(nil, file) with sink(xs ...any) drops the file into a trailing variadic element slot", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_01.go", content: "package reader\n\nimport \"os\"\n\nvar fileVarR1301 *os.File\n\nfunc sinkR1301(xs ...any) {}\n\nfunc variadicAnyFileProbeR1301() { sinkR1301(nil, fileVarR1301) }"},
	}},

	{name: "P142: variadic string-conversion copy", desc: "sink([]byte{1}, page) with sink(xs ...[]byte){ _ = string(xs[1]) } copies a trailing variadic element", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_02.go", content: "package reader\n\nfunc sinkR1302(xs ...[]byte) { _ = string(xs[1]) }\n\nfunc variadicStrProbeR1302(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tsinkR1302([]byte{1}, page)\n\treturn nil\n}"},
	}},

	{name: "P143: range rebinding of a function binding", desc: "for _, f = range fs rebinds f; a later f() may call the page-returning element", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_03.go", content: "package reader\n\nfunc rangeRebindProbeR1303(r *ImmutableReader, pgno uint32, fs []func() []byte) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func() []byte { return []byte{1} }\n\tfor _, f = range fs {\n\t\t_ = page\n\t}\n\treturn append([]byte{}, f()...), nil\n}"},
	}},

	{name: "P144: pointer-store rebinding of a function binding", desc: "*p = func() []byte { return page } rebinds f through p := &f", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_04.go", content: "package reader\n\nfunc ptrRebindProbeR1304(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf := func() []byte { return []byte{1} }\n\tp := &f\n\t*p = func() []byte { return page }\n\treturn append([]byte{}, f()...), nil\n}"},
	}},

	{name: "P145: nested struct parameter fields", desc: "take(o) returning o.Inner.Data with a caller-supplied nested page field", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_05.go", content: "package reader\n\ntype innerR1305 struct{ Data []byte }\n\ntype outerR1305 struct{ Inner innerR1305 }\n\nfunc takeR1305(o outerR1305) []byte { return o.Inner.Data }\n\nfunc nestedParamProbeR1305(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, takeR1305(outerR1305{Inner: innerR1305{Data: page}})...), nil\n}"},
	}},

	{name: "P146: map-key struct fields through a key range", desc: "m[box{Data: page}] = 1 then for k := range m { k.(box).Data }", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_06.go", content: "package reader\n\ntype boxR1306 struct{ Data []byte }\n\nfunc mapKeyRangeProbeR1306(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := make(map[any]int)\n\tm[boxR1306{Data: page}] = 1\n\tfor k := range m {\n\t\treturn append([]byte{}, k.(boxR1306).Data...), nil\n\t}\n\treturn nil, nil\n}"},
	}},

	{name: "P147: method-value receiver string conversion", desc: "get := b.String; get() with String() converting b.Data to an owned string", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_07.go", content: "package reader\n\ntype boxR1307 struct{ Data []byte }\n\nfunc (b boxR1307) String() string { return string(b.Data) }\n\nfunc methValStrProbeR1307(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tb := boxR1307{Data: page}\n\tget := b.String\n\t_ = get()\n\treturn nil\n}"},
	}},

	{name: "P148: file recovered from a non-empty interface by assertion", desc: "factory().(*os.File) with factory func() io.Reader names the descriptor as a typed file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_08.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc ifaceAssertProbeR1308(factory func() io.Reader) *os.File {\n\treturn factory().(*os.File)\n}"},
	}},

	{name: "P149: file recovered from an interface by a type switch", desc: "switch v := factory().(type) { case *os.File: return v } recovers the descriptor like the assertion form", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_r13_09.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc ifaceTypeSwitchProbeR1309(factory func() io.Reader) *os.File {\n\tswitch v := factory().(type) {\n\tcase *os.File:\n\t\treturn v\n\tdefault:\n\t\treturn nil\n\t}\n}"},
	}},

	{name: "P150: one-sided branch join of a local callable", desc: "g := f (an unproven parameter), g = func() on one branch only; g() after the join may run the unproven callee", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l14_01.go", content: "package reader\n\nfunc branchJoinProbeL1401(r *ImmutableReader, pgno uint32, f func() []byte) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tg := f\n\tif page != nil {\n\t\tg = func() []byte { return []byte{1} }\n\t}\n\t_ = page\n\treturn append([]byte{}, g()...), nil\n}"},
	}},

	{name: "P151: struct-field provenance for selector-valued arguments", desc: "take(h.Box) with h.Box.Data recorded a page keeps the field taint through the call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l14_02.go", content: "package reader\n\ntype lboxL1402 struct{ Data []byte }\n\ntype lholdL1402 struct{ Box lboxL1402 }\n\nfunc takeL1402(o lboxL1402) []byte { return o.Data }\n\nfunc selArgProbeL1402(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := lholdL1402{Box: lboxL1402{Data: page}}\n\treturn append([]byte{}, takeL1402(h.Box)...), nil\n}"},
	}},

	{name: "P152: promoted embedded struct fields keep page taint", desc: "take(o) with o's embedded inner struct holding a page resolves o.Data through the promoted path", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l14_03.go", content: "package reader\n\ntype linnerL1403 struct{ Data []byte }\n\ntype louterL1403 struct{ linnerL1403 }\n\nfunc takeL1403(o louterL1403) []byte { return o.Data }\n\nfunc embedPromoProbeL1403(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, takeL1403(louterL1403{linnerL1403: linnerL1403{Data: page}})...), nil\n}"},
	}},

	{name: "P153: container field provenance for variable-held map keys", desc: "m[b] = 1 with b.Data a page (and m[box{Data: page}] = 1 whole-value forms) keep the field taint through the key range", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l14_04.go", content: "package reader\n\ntype lboxL1404 struct{ Data []byte }\n\nfunc varKeyStoreProbeL1404(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := lboxL1404{}\n\tb.Data = page\n\tm := make(map[any]int)\n\tm[b] = 1\n\tfor k := range m {\n\t\treturn append([]byte{}, k.(lboxL1404).Data...), nil\n\t}\n\treturn nil, nil\n}"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_l14_05.go", content: "package reader\n\ntype lboxL1405 struct{ Data []byte }\n\nfunc varKeyWholeProbeL1405(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := lboxL1405{Data: page}\n\tm := make(map[any]int)\n\tm[b] = 1\n\tfor k := range m {\n\t\treturn append([]byte{}, k.(lboxL1405).Data...), nil\n\t}\n\treturn nil, nil\n}"},
	}},

	{name: "P154: bounded param-field slice keeps the flow legal", desc: "b := box{Data: page[48:112]}; take(b) stays below a complete page and must not fail the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l14_06.go", content: "package reader\n\ntype lboxL1406 struct{ Data []byte }\n\nfunc takeL1406(o lboxL1406) []byte { return o.Data }\n\nfunc boundedParamFieldProbeL1406(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := lboxL1406{Data: page[48:112]}\n\treturn append([]byte{}, takeL1406(b)...), nil\n}"},
	}},

	{name: "P155: switch without default keeps the pre-switch callable reachable", desc: "g := f (an unproven parameter); one case rebinds g to a closure but no default exists, so g() after the switch may still run the unproven callee", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_01.go", content: "package reader\n\nfunc switchNoDefaultProbeL1501(r *ImmutableReader, pgno uint32, f func() []byte) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tg := f\n\tswitch page != nil {\n\tcase true:\n\t\tg = func() []byte { return []byte{1} }\n\t}\n\t_ = page\n\treturn append([]byte{}, g()...), nil\n}"},
	}},

	{name: "P156: dereferenced argument provenance", desc: "p := &b with b.Data a mapped page; take(*p) keeps the pointed-to struct field taint through the call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_02.go", content: "package reader\n\ntype lboxL1502 struct{ Data []byte }\n\nfunc takeL1502(o lboxL1502) []byte { return o.Data }\n\nfunc derefArgProbeL1502(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := lboxL1502{Data: page}\n\tp := &b\n\treturn append([]byte{}, takeL1502(*p)...), nil\n}"},
	}},

	{name: "P157: indexed argument provenance", desc: "xs := []box{{Data: page}}; take(xs[0]) keeps the container element field taint through the call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_03.go", content: "package reader\n\ntype lboxL1503 struct{ Data []byte }\n\nfunc takeL1503(o lboxL1503) []byte { return o.Data }\n\nfunc idxArgProbeL1503(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\txs := []lboxL1503{{Data: page}}\n\treturn append([]byte{}, takeL1503(xs[0])...), nil\n}"},
	}},

	{name: "P158: type-asserted argument provenance", desc: "v any = box{Data: page}; take(v.(box)) keeps the asserted struct field taint through the call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_04.go", content: "package reader\n\ntype lboxL1504 struct{ Data []byte }\n\nfunc takeL1504(o lboxL1504) []byte { return o.Data }\n\nfunc assertArgProbeL1504(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar v any = lboxL1504{Data: page}\n\treturn append([]byte{}, takeL1504(v.(lboxL1504))...), nil\n}"},
	}},

	{name: "P159: naked multi-result return forwarding", desc: "return f(p) with f returning several values forwards EVERY result slot: a page in a later slot of the wrapper must stay visible to _, b := wrap(page); append", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_05.go", content: "package reader\n\nfunc sourceL1505(p []byte) (error, []byte) { return nil, p }\n\nfunc wrapL1505(p []byte) (error, []byte) {\n\treturn sourceL1505(p)\n}\n\nfunc multiForwardProbeL1505(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\t_, b := wrapL1505(page)\n\treturn append([]byte{}, b...), nil\n}"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_06.go", content: "package reader\n\nfunc pairSrcL1506(p []byte) ([]byte, error) { return p, nil }\n\nfunc pairWrapL1506(p []byte) ([]byte, error) {\n\treturn pairSrcL1506(p)\n}\n\nfunc pairForwardProbeL1506(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb, err2 := pairWrapL1506(page)\n\t_ = err2\n\treturn append([]byte{}, b...), nil\n}"},
	}},

	{name: "P160: struct-pointer parameter keeps dereferenced argument provenance", desc: "take(*p) inside a helper with p *box: the caller's box field taint reaches the callee param through the declared pointer leaves", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_07.go", content: "package reader\n\ntype lboxL1507 struct{ Data []byte }\n\nfunc takeL1507(o lboxL1507) []byte { return o.Data }\n\nfunc derefParamCallHelperL1507(p *lboxL1507) []byte { return takeL1507(*p) }\n\nfunc derefParamCallProbeL1507(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := lboxL1507{Data: page}\n\treturn append([]byte{}, derefParamCallHelperL1507(&b)...), nil\n}"},
	}},

	{name: "P161: container parameter keeps indexed argument provenance", desc: "take(xs[0]) inside a helper with xs []box: caller element field taint reaches the callee param through the declared element leaves", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l15_08.go", content: "package reader\n\ntype lboxL1508 struct{ Data []byte }\n\nfunc takeL1508(o lboxL1508) []byte { return o.Data }\n\nfunc idxParamCallHelperL1508(xs []lboxL1508) []byte { return takeL1508(xs[0]) }\n\nfunc idxParamCallProbeL1508(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\txs := []lboxL1508{{Data: page}}\n\treturn append([]byte{}, idxParamCallHelperL1508(xs)...), nil\n}"},
	}},

	{name: "P162: partial local record does not suppress parameter leaves", desc: "o.Other = 1 inside a helper must not hide a caller-supplied o.Data when o is passed on", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l16_01.go", content: "package reader\n\ntype lboxL1601 struct{ Data []byte; Other int }\n\nfunc takeL1601(o lboxL1601) []byte { return o.Data }\n\nfunc suppressHelperL1601(o lboxL1601) []byte {\n\to.Other = 1\n\treturn takeL1601(o)\n}\n\nfunc suppressProbeL1601(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, suppressHelperL1601(lboxL1601{Data: page})...), nil\n}"},
	}},

	{name: "P163: call-produced container keeps element field provenance", desc: "take(makeList(page)[0]) with makeList returning []box{{Data: page}}: the callee's element fields bind at the index", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l16_02.go", content: "package reader\n\ntype lboxL1602 struct{ Data []byte }\n\nfunc takeL1602(o lboxL1602) []byte { return o.Data }\n\nfunc makeListL1602(p []byte) []lboxL1602 { return []lboxL1602{{Data: p}} }\n\nfunc idxCallProbeL1602(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, takeL1602(makeListL1602(page)[0])...), nil\n}"},
	}},

	{name: "P164: returned struct parameter keeps caller field provenance", desc: "func id(b box) box { return b }; x := id(box{Data: page}); take(x) keeps Data through the identity return", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l16_03.go", content: "package reader\n\ntype lboxL1603 struct{ Data []byte }\n\nfunc takeL1603(o lboxL1603) []byte { return o.Data }\n\nfunc idL1603(o lboxL1603) lboxL1603 { return o }\n\nfunc paramRetProbeL1603(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := idL1603(lboxL1603{Data: page})\n\treturn append([]byte{}, takeL1603(x)...), nil\n}"},
	}},

	{name: "P165: promoted embedded leaves bind in parameter fallback", desc: "wrap(outer{inner{Data: page}}) with a helper taking outer and reading o.Data through the embedded inner struct", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l16_04.go", content: "package reader\n\ntype linnerL1604 struct{ Data []byte }\n\ntype louterL1604 struct{ linnerL1604 }\n\nfunc takeL1604(o louterL1604) []byte { return o.Data }\n\nfunc wrapL1604(o louterL1604) []byte { return takeL1604(o) }\n\nfunc promotedProbeL1604(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := louterL1604{linnerL1604{Data: page}}\n\treturn append([]byte{}, wrapL1604(b)...), nil\n}"},
	}},
}
