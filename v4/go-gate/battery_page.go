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
}
