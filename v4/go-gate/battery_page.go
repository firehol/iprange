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

	{name: "P166: bound call-produced container keeps element provenance", desc: "x := makeList(page)[0] then take(x): the bound variable keeps the callee's element field taints exactly like the direct argument form", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l17_01.go", content: "package reader\n\ntype lboxL1701 struct{ Data []byte }\n\nfunc takeL1701(o lboxL1701) []byte { return o.Data }\n\nfunc makeListL1701(p []byte) []lboxL1701 { return []lboxL1701{{Data: p}} }\n\nfunc bindIdxProbeL1701(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := makeListL1701(page)[0]\n\treturn append([]byte{}, takeL1701(x)...), nil\n}"},
	}},

	{name: "P167: bound selected field of a call-produced struct keeps provenance", desc: "x := makeBox(page).Inner then take(x): the selector strips the callee's dotted prefix so the bound value binds its direct field names", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l17_02.go", content: "package reader\n\ntype linnerL1702 struct{ Data []byte }\n\ntype louterL1702 struct{ Inner linnerL1702 }\n\nfunc takeL1702(o linnerL1702) []byte { return o.Data }\n\nfunc makeBoxL1702(p []byte) louterL1702 { return louterL1702{Inner: linnerL1702{Data: p}} }\n\nfunc bindSelProbeL1702(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := makeBoxL1702(page).Inner\n\treturn append([]byte{}, takeL1702(x)...), nil\n}"},
	}},

	{name: "P168: bound selected field of a recorded local keeps provenance", desc: "b := louter{Inner: ...}; x := b.Inner then take(x): local selector bindings rename the flattened dotted path to the selected value's direct field names", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l17_03.go", content: "package reader\n\ntype linnerL1703 struct{ Data []byte }\n\ntype louterL1703 struct{ Inner linnerL1703 }\n\nfunc takeL1703(o linnerL1703) []byte { return o.Data }\n\nfunc bindSelRecProbeL1703(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := louterL1703{Inner: linnerL1703{Data: page}}\n\tx := b.Inner\n\treturn append([]byte{}, takeL1703(x)...), nil\n}"},
	}},

	{name: "P169: bound container-parameter element keeps provenance", desc: "x := xs[0] then take(x) inside a helper with xs []box: the bound variable carries the caller's element field taints through the declared leaves", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l17_04.go", content: "package reader\n\ntype lboxL1704 struct{ Data []byte }\n\nfunc takeL1704(o lboxL1704) []byte { return o.Data }\n\nfunc bindParamIdxHelperL1704(xs []lboxL1704) []byte {\n\tx := xs[0]\n\treturn takeL1704(x)\n}\n\nfunc bindParamIdxProbeL1704(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, bindParamIdxHelperL1704([]lboxL1704{{Data: page}})...), nil\n}"},
	}},

	{name: "P170: nested call-produced selector chains keep provenance", desc: "read makeBox(page).Inner.Inner.Data, pass take(makeBox(page).Inner.Inner), and bind x := makeBox(page).Inner.Inner: flattened dotted paths of returned literals resolve at every depth", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l17_05.go", content: "package reader\n\ntype lleafL1705 struct{ Data []byte }\n\ntype lmidL1705 struct{ Inner lleafL1705 }\n\ntype ltopL1705 struct{ Inner lmidL1705 }\n\nfunc takeL1705(o lleafL1705) []byte { return o.Data }\n\nfunc makeBoxL1705(p []byte) ltopL1705 { return ltopL1705{Inner: lmidL1705{Inner: lleafL1705{Data: p}}} }\n\nfunc selNestedReadProbeL1705(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, makeBoxL1705(page).Inner.Inner.Data...), nil\n}\n\nfunc selNestedArgProbeL1705(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, takeL1705(makeBoxL1705(page).Inner.Inner)...), nil\n}\n\nfunc selNestedBindProbeL1705(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := makeBoxL1705(page).Inner.Inner\n\treturn append([]byte{}, takeL1705(x)...), nil\n}"},
	}},

	{name: "P171: returned asserted interface-param struct keeps caller provenance", desc: "func as(v any) box { return v.(box) }; x := as(any(box{Data: page})); take(x): a returned type assertion over an interface parameter carries the asserted type's leaves with the parameter source, and the explicit any() conversion keeps the argument's field provenance", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l18_01.go", content: "package reader\n\ntype lboxL1801 struct{ Data []byte }\n\nfunc takeL1801(o lboxL1801) []byte { return o.Data }\n\nfunc asBoxL1801(v any) lboxL1801 { return v.(lboxL1801) }\n\nfunc taRetProbeL1801(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := asBoxL1801(any(lboxL1801{Data: page}))\n\treturn append([]byte{}, takeL1801(x)...), nil\n}"},
	}},

	{name: "P172: two-value asserted read inside a helper keeps caller provenance", desc: "if b, ok := v.(box); ok { return b.Data } with v any: the two-value assertion types the expression as a (box, bool) tuple, so the asserted type must be read from the assertion's type expression; the asserted leaves keep the parameter source", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l18_02.go", content: "package reader\n\ntype lboxL1802 struct{ Data []byte }\n\nfunc takeAnyL1802(v any) []byte {\n\tif b, ok := v.(lboxL1802); ok {\n\t\treturn b.Data\n\t}\n\treturn nil\n}\n\nfunc taArgProbeL1802(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, takeAnyL1802(any(lboxL1802{Data: page}))...), nil\n}"},
	}},
	{name: "P173: multi-dim indexed literal read keeps provenance", desc: "m := [1][1]box{{{Data: page}}}; append(..., m[0][0].Data...): every trailing index names an element of the same root container, so a field select on a multi-level index reads the element-field taints recorded for the root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l19_01.go", content: "package reader\n\ntype lboxL1901 struct{ Data []byte }\n\nfunc idxReadProbeL1901(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := [1][1]lboxL1901{{{Data: page}}}\n\treturn append([]byte{}, m[0][0].Data...), nil\n}"},
	}},

	{name: "P174: multi-dim indexed literal binding keeps provenance", desc: "m := [1][1]box{{{Data: page}}}; x := m[0][0] then take(x): an element bind through a multi-level index resolves the root container's element-field taints", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l19_02.go", content: "package reader\n\ntype lboxL1902 struct{ Data []byte }\n\nfunc takeL1902(o lboxL1902) []byte { return o.Data }\n\nfunc idxBindProbeL1902(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := [1][1]lboxL1902{{{Data: page}}}\n\tx := m[0][0]\n\treturn append([]byte{}, takeL1902(x)...), nil\n}"},
	}},

	{name: "P175: forced literal element binding keeps provenance", desc: "x := []box{{Data: page}}[0] then take(x): extracting an element straight from a composite literal binds the union of the elements' field taints", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l19_03.go", content: "package reader\n\ntype lboxL1903 struct{ Data []byte }\n\nfunc takeL1903(o lboxL1903) []byte { return o.Data }\n\nfunc flatBindProbeL1903(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := []lboxL1903{{Data: page}}[0]\n\treturn append([]byte{}, takeL1903(x)...), nil\n}"},
	}},

	{name: "P176: multi-index call-result read keeps provenance", desc: "append(..., makeMatrix(page)[0][0].Data...): a field select on a multi-level index of a call result reads the callee's recorded element fields", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l19_04.go", content: "package reader\n\ntype lboxL1904 struct{ Data []byte }\n\nfunc makeMatrixL1904(p []byte) [1][1]lboxL1904 { return [1][1]lboxL1904{{{Data: p}}} }\n\nfunc idxCallProbeL1904(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, makeMatrixL1904(page)[0][0].Data...), nil\n}"},
	}},
	{name: "P177: returned multi-dim element keeps provenance", desc: "func retElem(m [1][1]box) box { return m[0][0] }; append(..., retElem([1][1]box{{{Data: page}}}).Data...): a returned container element through a multi-level index resolves the root's recorded fields and the parameter's declared element leaves", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l20_01.go", content: "package reader\n\ntype lboxL2001 struct{ Data []byte }\n\nfunc retElemL2001(m [1][1]lboxL2001) lboxL2001 { return m[0][0] }\n\nfunc retReadProbeL2001(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, retElemL2001([1][1]lboxL2001{{{Data: page}}}).Data...), nil\n}"},
	}},

	{name: "P178: bound returned multi-dim element keeps provenance", desc: "x := retElem([1][1]box{{{Data: page}}}); take(x): the bound value of a helper returning a multi-dim container element keeps the element fields", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l20_02.go", content: "package reader\n\ntype lboxL2002 struct{ Data []byte }\n\nfunc takeL2002(o lboxL2002) []byte { return o.Data }\n\nfunc retElemL2002(m [1][1]lboxL2002) lboxL2002 { return m[0][0] }\n\nfunc retBindProbeL2002(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := retElemL2002([1][1]lboxL2002{{{Data: page}}})\n\treturn append([]byte{}, takeL2002(x)...), nil\n}"},
	}},
	{name: "P179: nested struct read through multi-dim indexed local keeps provenance", desc: "m := [1][1]outer{{{Inner: inner{Data: page}}}}; append(..., m[0][0].Inner.Data...): selectors above the index chain keep the full dotted path on the root container's record", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l21_01.go", content: "package reader\n\ntype linnerL2101 struct{ Data []byte }\n\ntype louterL2101 struct{ Inner linnerL2101 }\n\nfunc idxSelReadProbeL2101(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := [1][1]louterL2101{{{Inner: linnerL2101{Data: page}}}}\n\treturn append([]byte{}, m[0][0].Inner.Data...), nil\n}"},
	}},

	{name: "P180: bound nested struct of a multi-dim indexed local keeps provenance", desc: "x := m[0][0].Inner then take(x): an indexed base with a selected field strips the wrapper path onto the selected value's direct field names", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l21_02.go", content: "package reader\n\ntype linnerL2102 struct{ Data []byte }\n\ntype louterL2102 struct{ Inner linnerL2102 }\n\nfunc takeL2102(o linnerL2102) []byte { return o.Data }\n\nfunc idxSelBindProbeL2102(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := [1][1]louterL2102{{{Inner: linnerL2102{Data: page}}}}\n\tx := m[0][0].Inner\n\treturn append([]byte{}, takeL2102(x)...), nil\n}"},
	}},

	{name: "P181: inline multi-dim literal with nested struct read keeps provenance", desc: "append(..., [1][1]outer{{{Inner: inner{Data: page}}}}[0][0].Inner.Data...): inline matrix element extraction with a nested selection reads the union of element fields at the full dotted path", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l21_03.go", content: "package reader\n\ntype linnerL2103 struct{ Data []byte }\n\ntype louterL2103 struct{ Inner linnerL2103 }\n\nfunc litSelProbeL2103(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, [1][1]louterL2103{{{Inner: linnerL2103{Data: page}}}}[0][0].Inner.Data...), nil\n}"},
	}},

	{name: "P182: nested struct read through multi-dim indexed call result keeps provenance", desc: "append(..., makeMatrix(page)[0][0].Inner.Data...): a nested selection over a multi-level index of a call result reads the callee's flattened dotted paths", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l21_04.go", content: "package reader\n\ntype linnerL2104 struct{ Data []byte }\n\ntype louterL2104 struct{ Inner linnerL2104 }\n\nfunc makeMatrixL2104(p []byte) [1][1]louterL2104 { return [1][1]louterL2104{{{Inner: linnerL2104{Data: p}}}} }\n\nfunc idxSelCallProbeL2104(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, makeMatrixL2104(page)[0][0].Inner.Data...), nil\n}"},
	}},

	{name: "P183: empty default switch keeps the pre-switch state reachable", desc: "switch { case 0: b = []byte{1}; default: } with b := page: an EMPTY default still executes, so the page held before the switch survives into the append", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_01.go", content: "package reader\n\nfunc emptyDefaultProbeL2201(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := page\n\tswitch pgno {\n\tcase 0:\n\t\tb = []byte{1}\n\tdefault:\n\t}\n\treturn append([]byte{}, b...), nil\n}"},
	}},

	{name: "P184: returned selected field of a call keeps provenance", desc: "func retSel(p []byte) inner { return makeBox(p).Inner }; x := retSel(page); append(..., x.Data...): a returned selected field resolves the callee's flattened paths with the selection prefix stripped", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_02.go", content: "package reader\n\ntype linnerL2202 struct{ Data []byte }\ntype louterL2202 struct{ Inner linnerL2202 }\n\nfunc makeBoxL2202(p []byte) louterL2202 { return louterL2202{Inner: linnerL2202{Data: p}} }\n\nfunc retSelL2202(p []byte) linnerL2202 { return makeBoxL2202(p).Inner }\n\nfunc retSelProbeL2202(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := retSelL2202(page)\n\treturn append([]byte{}, x.Data...), nil\n}"},
	}},

	{name: "P185: returned element of an inline literal index keeps provenance", desc: "func retLitIdx(p []byte) box { return []box{{Data: p}}[0] }; x := retLitIdx(page); append(..., x.Data...): the literal's element-field union carries the returned element", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_03.go", content: "package reader\n\ntype lboxL2203 struct{ Data []byte }\n\nfunc retLitIdxL2203(p []byte) lboxL2203 { return []lboxL2203{{Data: p}}[0] }\n\nfunc retLitIdxProbeL2203(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := retLitIdxL2203(page)\n\treturn append([]byte{}, x.Data...), nil\n}"},
	}},

	{name: "P186: range over a container parameter binds the element fields", desc: "for _, x := range xs with xs []box parameter: the declared element leaves carry the caller's fields into the loop value, so x.Data after rangeParamHelper([]box{{Data: page}}) stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_04.go", content: "package reader\n\ntype lboxL2204 struct{ Data []byte }\n\nfunc rangeParamHelperL2204(xs []lboxL2204) []byte {\n\tfor _, x := range xs {\n\t\treturn x.Data\n\t}\n\treturn nil\n}\n\nfunc rangeParamProbeL2204(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, rangeParamHelperL2204([]lboxL2204{{Data: page}})...), nil\n}"},
	}},

	{name: "P187: nested selector read after an interface assertion keeps provenance", desc: "func f(v any) []byte { return v.(outer).Inner.Data }: an interface-param asserted to a concrete struct keeps the parameter source through the asserted type's leaf at the full dotted path", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_05.go", content: "package reader\n\ntype linnerL2205 struct{ Data []byte }\ntype louterL2205 struct{ Inner linnerL2205 }\n\nfunc assertNestedReadL2205(v any) []byte {\n\treturn v.(louterL2205).Inner.Data\n}\n\nfunc assertNestedReadProbeL2205(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, assertNestedReadL2205(any(louterL2205{Inner: linnerL2205{Data: page}}))...), nil\n}"},
	}},

	{name: "P188: nested asserted value bound to a take argument keeps provenance", desc: "take(v.(outer).Inner) with v any: the asserted struct VALUE keeps the asserted type's leaves renamed to the selected value's direct field names", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_06.go", content: "package reader\n\ntype linnerL2206 struct{ Data []byte }\ntype louterL2206 struct{ Inner linnerL2206 }\n\nfunc takeInnerL2206(o linnerL2206) []byte { return o.Data }\n\nfunc assertNestedBindL2206(v any) []byte {\n\treturn takeInnerL2206(v.(louterL2206).Inner)\n}\n\nfunc assertNestedBindProbeL2206(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, assertNestedBindL2206(any(louterL2206{Inner: linnerL2206{Data: page}}))...), nil\n}"},
	}},

	{name: "P189: named container types keep element provenance", desc: "type matrix [1][1]box; func retNamed(m matrix) box { return m[0][0] }: a NAMED container parameter unwraps to its underlying chain, so the declared element leaves stay bound", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_07.go", content: "package reader\n\ntype lboxL2207 struct{ Data []byte }\n\ntype matrixL2207 [1][1]lboxL2207\n\nfunc retNamedL2207(m matrixL2207) lboxL2207 { return m[0][0] }\n\nfunc namedProbeL2207(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tx := retNamedL2207(matrixL2207{{{Data: page}}})\n\treturn append([]byte{}, x.Data...), nil\n}"},
	}},

	{name: "P190: map-key parameter range keeps key provenance", desc: "for k := range m with m map[*box]int parameter: a key-only range binds the KEY's declared leaves, so k.Data after mapKeyRangeHelper(map[*box]int{{Data: page}: 1}) stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_08.go", content: "package reader\n\ntype lboxL2208 struct{ Data []byte }\n\nfunc mapKeyRangeHelperL2208(m map[*lboxL2208]int) []byte {\n\tfor k := range m {\n\t\treturn k.Data\n\t}\n\treturn nil\n}\n\nfunc mapKeyRangeProbeL2208(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, mapKeyRangeHelperL2208(map[*lboxL2208]int{{Data: page}: 1})...), nil\n}"},
	}},

	{name: "P191: container literal with a variable element keeps element provenance", desc: "b := box{Data: page}; xs := []box{b}; append(..., xs[0].Data...): a VARIABLE element inside a container literal contributes its recorded fields exactly like a literal element", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_09.go", content: "package reader\n\ntype lboxL2209 struct{ Data []byte }\n\nfunc varElemProbeL2209(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tb := lboxL2209{Data: page}\n\txs := []lboxL2209{b}\n\treturn append([]byte{}, xs[0].Data...), nil\n}"},
	}},

	{name: "P192: map composite-literal key struct fields through a key range", desc: "m := map[*box]int{{Data: page}: 1}; for k := range m { k.Data }: a composite-literal map KEY contributes its flattened fields to the key-only range exactly like the parameter form", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_10.go", content: "package reader\n\ntype lboxL2210 struct{ Data []byte }\n\nfunc mapKeyLitProbeL2210(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := map[*lboxL2210]int{{Data: page}: 1}\n\tfor k := range m {\n\t\treturn append([]byte{}, k.Data...), nil\n\t}\n\treturn nil, nil\n}"},
	}},

	{name: "P193: range over a struct-field container keeps element provenance", desc: "for _, x := range h.Items with h.Items []box: the loop value binds the field's element leaves, so x.Data after rangeSelHelper(holder{Items: []box{{Data: page}}}) stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_11.go", content: "package reader\n\ntype lboxL2211 struct{ Data []byte }\n\ntype holderL2211 struct{ Items []lboxL2211 }\n\nfunc rangeSelHelperL2211(h holderL2211) []byte {\n\tfor _, x := range h.Items {\n\t\treturn x.Data\n\t}\n\treturn nil\n}\n\nfunc rangeSelProbeL2211(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, rangeSelHelperL2211(holderL2211{Items: []lboxL2211{{Data: page}}})...), nil\n}"},
	}},

	{name: "P194: indexed read of a struct-field container keeps element provenance", desc: "h.Items[0].Data with h.Items []box: the element field taints record under the flattened Items.Data path, so the helper read keeps the caller's page source", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_12.go", content: "package reader\n\ntype lboxL2212 struct{ Data []byte }\n\ntype holderL2212 struct{ Items []lboxL2212 }\n\nfunc idxReadHelperL2212(h holderL2212) []byte { return h.Items[0].Data }\n\nfunc idxReadProbeL2212(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, idxReadHelperL2212(holderL2212{Items: []lboxL2212{{Data: page}}})...), nil\n}"},
	}},

	{name: "P195: struct-field container element bound to an argument keeps provenance", desc: "take(h.Items[0]) with h.Items []box: an indexed element of a FIELD-held container resolves the container's element fields through the selector argument flow", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_13.go", content: "package reader\n\ntype lboxL2213 struct{ Data []byte }\n\ntype holderL2213 struct{ Items []lboxL2213 }\n\nfunc takeL2213(o lboxL2213) []byte { return o.Data }\n\nfunc idxArgHelperL2213(h holderL2213) []byte { return takeL2213(h.Items[0]) }\n\nfunc idxArgProbeL2213(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, idxArgHelperL2213(holderL2213{Items: []lboxL2213{{Data: page}}})...), nil\n}"},
	}},

	{name: "P196: range over an index-selected container keeps element provenance", desc: "for _, x := range m[0].Items with m [1]holder: the index chain unwraps to a selector-held container and binds the element leaves", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_14.go", content: "package reader\n\ntype lboxL2214 struct{ Data []byte }\n\ntype holderL2214 struct{ Items []lboxL2214 }\n\nfunc rangeIdxSelHelperL2214(m [1]holderL2214) []byte {\n\tfor _, x := range m[0].Items {\n\t\treturn x.Data\n\t}\n\treturn nil\n}\n\nfunc rangeIdxSelProbeL2214(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, rangeIdxSelHelperL2214([1]holderL2214{{Items: []lboxL2214{{Data: page}}}})...), nil\n}"},
	}},

	{name: "P197: struct-field map key-only range keeps key provenance", desc: "for k := range h.Keyed with h.Keyed map[*box]int: the KEY leaves of a struct-field map expose under the field prefix, so k.Data stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_15.go", content: "package reader\n\ntype lboxL2215 struct{ Data []byte }\n\ntype holderL2215 struct{ Keyed map[*lboxL2215]int }\n\nfunc mapFieldKeyHelperL2215(h holderL2215) []byte {\n\tfor k := range h.Keyed {\n\t\treturn k.Data\n\t}\n\treturn nil\n}\n\nfunc mapFieldKeyProbeL2215(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, mapFieldKeyHelperL2215(holderL2215{Keyed: map[*lboxL2215]int{{Data: page}: 1}})...), nil\n}"},
	}},

	{name: "P198: NAMED map types keep key provenance", desc: "type M map[*box]int; for k := range m with m M: the named wrapper unwraps to its underlying map for the key leaves and the literal key union, so k.Data stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_16.go", content: "package reader\n\ntype lboxL2216 struct{ Data []byte }\n\ntype mapKeyedL2216 map[*lboxL2216]int\n\nfunc mapKeyNamedHelperL2216(m mapKeyedL2216) []byte {\n\tfor k := range m {\n\t\treturn k.Data\n\t}\n\treturn nil\n}\n\nfunc mapKeyNamedProbeL2216(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, mapKeyNamedHelperL2216(mapKeyedL2216{{Data: page}: 1})...), nil\n}"},
	}},

	{name: "P199: dereference store of a struct keeps field provenance", desc: "*p = B{Data: page} then p.Data (and (*p).Data): the pointed-to variable records the struct's field taints, so both read forms keep the caller's source", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_17.go", content: "package reader\n\ntype lboxL2217 struct{ Data []byte }\n\nfunc derefStoreProbeL2217(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tp := &lboxL2217{}\n\t*p = lboxL2217{Data: page}\n\treturn append([]byte{}, p.Data...), nil\n}"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_18.go", content: "package reader\n\ntype lboxL2218 struct{ Data []byte }\n\nfunc derefStarReadProbeL2218(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tp := &lboxL2218{}\n\t*p = lboxL2218{Data: page}\n\treturn append([]byte{}, (*p).Data...), nil\n}"},
	}},
	{name: "P200: two-variable map range binds the KEY fields to the key variable", desc: "for k, v := range m with m map[*box]int: key-only provenance must bind to k, not to the value variable, so k.Data after mapKVHelper(map[*box]int{{Data: page}: 1}) stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_19.go", content: "package reader\n\ntype lboxL2219 struct{ Data []byte }\n\nfunc mapKVHelperL2219(m map[*lboxL2219]int) []byte {\n\tfor k, v := range m {\n\t\t_ = v\n\t\treturn k.Data\n\t}\n\treturn nil\n}\n\nfunc mapKVProbeL2219(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, mapKVHelperL2219(map[*lboxL2219]int{{Data: page}: 1})...), nil\n}"},
	}},

	{name: "P201: container literal elements produced by a call keep their fields", desc: "xs := []box{makeBox(p)}; xs[0].Data: the element's call-produced fields resolve under the literal's element namespace, so the reads and appends stay sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_20.go", content: "package reader\n\ntype lboxL2220 struct{ Data []byte }\n\nfunc makeBoxL2220(p []byte) lboxL2220 { return lboxL2220{Data: p} }\n\nfunc callElemHelperL2220(p []byte) []byte {\n\txs := []lboxL2220{makeBoxL2220(p)}\n\treturn xs[0].Data\n}\n\nfunc callElemProbeL2220(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, callElemHelperL2220(page)...), nil\n}"},
	}},

	{name: "P202: container literal elements that are ADDRESSES of variables keep their fields", desc: "xs := []*box{&b}; xs[0].Data after b.Data = p: the address-of element exposes the pointed-to variable's recorded fields", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_21.go", content: "package reader\n\ntype lboxL2221 struct{ Data []byte }\n\nfunc addrElemHelperL2221(p []byte) []byte {\n\tb := lboxL2221{Data: p}\n\txs := []*lboxL2221{&b}\n\treturn xs[0].Data\n}\n\nfunc addrElemProbeL2221(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, addrElemHelperL2221(page)...), nil\n}"},
	}},

	{name: "P203: indexed whole-struct store into a FIELD container keeps element provenance", desc: "h.Items[0] = B{Data: p} with h.Items []box: the element fields record under the field prefix (Items.Data), so h.Items[0].Data stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_22.go", content: "package reader\n\ntype lboxL2222 struct{ Data []byte }\ntype holderL2222 struct{ Items []lboxL2222 }\n\nfunc idxStoreHelperL2222(h holderL2222, p []byte) []byte {\n\th.Items = make([]lboxL2222, 1)\n\th.Items[0] = lboxL2222{Data: p}\n\treturn h.Items[0].Data\n}\n\nfunc idxStoreProbeL2222(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, idxStoreHelperL2222(holderL2222{}, page)...), nil\n}"},
	}},

	{name: "P204: dereference store through an ALIASED pointer keeps the original variable sourced", desc: "q := &b; *q = B{Data: page} then b.Data: the pointed-to record lands on the alias target too, so reads under the original name stay sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_23.go", content: "package reader\n\ntype lboxL2223 struct{ Data []byte }\n\nfunc aliasStoreHelperL2223(p []byte) []byte {\n\tb := lboxL2223{}\n\tq := &b\n\t*q = lboxL2223{Data: p}\n\treturn b.Data\n}\n\nfunc aliasStoreProbeL2223(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, aliasStoreHelperL2223(page)...), nil\n}"},
	}},

	{name: "P205: pointer-receiver METHOD mutation propagates to the caller's variable", desc: "b.Set(p) writing b.Data = p then b.Data: the callee summary exports pointer-parameter field mutations and the call site re-binds them to the receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_24.go", content: "package reader\n\ntype lboxL2224 struct{ Data []byte }\n\nfunc (b *lboxL2224) Set(p []byte) { b.Data = p }\n\nfunc methodMutHelperL2224(p []byte) []byte {\n\tb := lboxL2224{}\n\tb.Set(p)\n\treturn b.Data\n}\n\nfunc methodMutProbeL2224(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, methodMutHelperL2224(page)...), nil\n}"},
	}},

	{name: "P206: struct field provenance survives channel send and receive", desc: "ch <- B{Data: page}; x := <-ch; x.Data: the sent struct's fields record on the channel and the receive binds them to the received variable", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_25.go", content: "package reader\n\ntype lboxL2225 struct{ Data []byte }\n\nfunc chanHelperL2225(p []byte) []byte {\n\tch := make(chan lboxL2225, 1)\n\tch <- lboxL2225{Data: p}\n\tx := <-ch\n\treturn x.Data\n}\n\nfunc chanProbeL2225(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, chanHelperL2225(page)...), nil\n}"},
	}},

	{name: "P207: address-of variable arguments keep the variable's field provenance", desc: "takePtr(&b) after b.Data = p: the &b argument exposes b's recorded fields, so the callee's o.Data read stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_26.go", content: "package reader\n\ntype lboxL2226 struct{ Data []byte }\n\nfunc takePtrL2226(o *lboxL2226) []byte { return o.Data }\n\nfunc ptrArgHelperL2226(p []byte) []byte {\n\tb := lboxL2226{Data: p}\n\treturn takePtrL2226(&b)\n}\n\nfunc ptrArgProbeL2226(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, ptrArgHelperL2226(page)...), nil\n}"},
	}},

	{name: "P208: interface method calls returning CONTAINERS fail closed on element fields", desc: "x.Boxes()[0].Data with a page-carrying concrete holder behind an interface: an unscanned interface result's container element leaves fail closed like direct struct fields", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_27.go", content: "package reader\n\ntype lboxL2227 struct{ Data []byte }\n\ntype boxerL2227 interface{ Boxes() []lboxL2227 }\n\ntype holderL2227 struct{ items []lboxL2227 }\n\nfunc (h holderL2227) Boxes() []lboxL2227 { return h.items }\n\nfunc ifaceHelperL2227(p []byte) []byte {\n\th := holderL2227{items: []lboxL2227{{Data: p}}}\n\tvar x boxerL2227 = h\n\treturn x.Boxes()[0].Data\n}\n\nfunc ifaceProbeL2227(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, ifaceHelperL2227(page)...), nil\n}"},
	}},

	{name: "P209: range over a TYPE-CONVERTED container keeps element provenance", desc: "for _, x := range []B(h.Items) with h.Items []box: the conversion call preserves the operand's element fields for the loop value", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_28.go", content: "package reader\n\ntype lboxL2228 struct{ Data []byte }\ntype holderL2228 struct{ Items []lboxL2228 }\n\nfunc convRangeHelperL2228(h holderL2228) []byte {\n\tfor _, x := range []lboxL2228(h.Items) {\n\t\treturn x.Data\n\t}\n\treturn nil\n}\n\nfunc convRangeProbeL2228(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, convRangeHelperL2228(holderL2228{Items: []lboxL2228{{Data: page}}})...), nil\n}"},
	}},
	{name: "P210: nested opaque carrier fields fail closed", desc: "x.Outer().Items[0].Data with Outer returning a struct whose field is a container: an unscanned interface result's CONTAINER FIELDS expose their element leaves like direct struct fields", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_30.go", content: "package reader\n\ntype lboxL2230 struct{ Data []byte }\ntype outerL2230 struct{ Items []lboxL2230 }\n\ntype boxerL2230 interface{ Outer() outerL2230 }\n\ntype holderL2230 struct{ o outerL2230 }\n\nfunc (h holderL2230) Outer() outerL2230 { return h.o }\n\nfunc nestedOpaqueHelperL2230(p []byte) []byte {\n\th := holderL2230{o: outerL2230{Items: []lboxL2230{{Data: p}}}}\n\tvar x boxerL2230 = h\n\treturn x.Outer().Items[0].Data\n}\n\nfunc nestedOpaqueProbeL2230(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, nestedOpaqueHelperL2230(page)...), nil\n}"},
	}},

	{name: "P211: two-variable map range keeps nested container element leaves", desc: "for k, v := range m with m map[int][]B keeps v[0].Data and m map[*[]B]int keeps (*k)[0].Data: container value and pointer-wrapped container key leaves bind their range variables", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_31.go", content: "package reader\n\ntype lboxL2231 struct{ Data []byte }\n\nfunc mapValContainerHelperL2231(m map[int][]lboxL2231) []byte {\n\tfor k, v := range m {\n\t\t_ = k\n\t\treturn v[0].Data\n\t}\n\treturn nil\n}\n\nfunc mapKeyPtrContainerHelperL2231(m map[*[]lboxL2231]int) []byte {\n\tfor k, v := range m {\n\t\t_ = v\n\t\treturn (*k)[0].Data\n\t}\n\treturn nil\n}\n\nfunc mapValContainerProbeL2231(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\ta := mapValContainerHelperL2231(map[int][]lboxL2231{1: {lboxL2231{Data: page}}})\n\tb := mapKeyPtrContainerHelperL2231(map[*[]lboxL2231]int{{lboxL2231{Data: page}}: 1})\n\treturn append(append([]byte{}, a...), b...), nil\n}"},
	}},

	{name: "P212: address-of SELECTED FIELD mutation summaries bind the base object", desc: "set(&h.Inner, page) writing b.Data = p through a helper: the &h.Inner argument binds the mutation to h.Inner.Data exactly like a plain &b", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_32.go", content: "package reader\n\ntype lboxL2232 struct{ Data []byte }\ntype holderL2232 struct{ Inner lboxL2232 }\n\nfunc setL2232(b *lboxL2232, p []byte) { b.Data = p }\n\nfunc addrSelMutHelperL2232(p []byte) []byte {\n\th := holderL2232{}\n\tsetL2232(&h.Inner, p)\n\treturn h.Inner.Data\n}\n\nfunc addrSelMutProbeL2232(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, addrSelMutHelperL2232(page)...), nil\n}"},
	}},

	{name: "P213: address-of INDEXED ELEMENT mutation summaries bind the container", desc: "set(&xs[0], page): the &xs[0] argument binds the mutation to the container's element fields, so xs[0].Data stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_33.go", content: "package reader\n\ntype lboxL2233 struct{ Data []byte }\n\nfunc setL2233(b *lboxL2233, p []byte) { b.Data = p }\n\nfunc addrIdxMutHelperL2233(p []byte) []byte {\n\txs := make([]lboxL2233, 1)\n\tsetL2233(&xs[0], p)\n\treturn xs[0].Data\n}\n\nfunc addrIdxMutProbeL2233(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, addrIdxMutHelperL2233(page)...), nil\n}"},
	}},

	{name: "P214: directly called func-literal pointer mutations reach the caller", desc: "func(q *B, v []byte){ q.Data = v }(&b, page) then b.Data: the closure summary exports pointer-parameter field mutations and the direct call site re-binds them", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_34.go", content: "package reader\n\ntype lboxL2234 struct{ Data []byte }\n\nfunc litPtrMutHelperL2234(p []byte) []byte {\n\tb := lboxL2234{}\n\tfunc(q *lboxL2234, v []byte) { q.Data = v }(&b, p)\n\treturn b.Data\n}\n\nfunc litPtrMutProbeL2234(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, litPtrMutHelperL2234(page)...), nil\n}"},
	}},

	{name: "P215: struct-field channel send and receive keep field provenance", desc: "h.Ch <- B{Data: page}; x := <-h.Ch; x.Data through a holder field, plus the select-clause send form: both record the \"Ch.\"-prefixed fields on the base object", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_35.go", content: "package reader\n\ntype lboxL2235 struct{ Data []byte }\ntype holderL2235 struct{ Ch chan lboxL2235 }\n\nfunc chanFieldHelperL2235(p []byte) []byte {\n\th := holderL2235{Ch: make(chan lboxL2235, 1)}\n\th.Ch <- lboxL2235{Data: p}\n\tx := <-h.Ch\n\treturn x.Data\n}\n\nfunc chanSelHelperL2235(p []byte) []byte {\n\th := holderL2235{Ch: make(chan lboxL2235, 1)}\n\tselect {\n\tcase h.Ch <- lboxL2235{Data: p}:\n\tcase y := <-h.Ch:\n\t\treturn y.Data\n\t}\n\treturn nil\n}\n\nfunc chanFieldProbeL2235(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\ta := chanFieldHelperL2235(page)\n\tb := chanSelHelperL2235(page)\n\treturn append(append([]byte{}, a...), b...), nil\n}"},
	}},

	{name: "P216: indexed whole-struct store through a DEREFERENCED container keeps element provenance", desc: "q := &xs; (*q)[0] = B{Data: page} then (*q)[0].Data: the dereference store records the element fields on the pointer and its alias target", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_36.go", content: "package reader\n\ntype lboxL2236 struct{ Data []byte }\n\nfunc derefIdxStoreHelperL2236(p []byte) []byte {\n\txs := make([]lboxL2236, 1)\n\tq := &xs\n\t(*q)[0] = lboxL2236{Data: p}\n\treturn (*q)[0].Data\n}\n\nfunc derefIdxStoreProbeL2236(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, derefIdxStoreHelperL2236(page)...), nil\n}"},
	}},

	{name: "P217: runtime map-key store through a FIELD container keeps key provenance", desc: "h.M[&b] = 1 with h.M map[*B]int after b.Data = page: the key's fields record under the field prefix, so for k := range h.M keeps k.Data", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_37.go", content: "package reader\n\ntype lboxL2237 struct{ Data []byte }\ntype holderL2237 struct{ M map[*lboxL2237]int }\n\nfunc mapFieldKeyStoreHelperL2237(p []byte) []byte {\n\th := holderL2237{M: map[*lboxL2237]int{}}\n\tb := lboxL2237{Data: p}\n\th.M[&b] = 1\n\tfor k := range h.M {\n\t\treturn k.Data\n\t}\n\treturn nil\n}\n\nfunc mapFieldKeyStoreProbeL2237(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, mapFieldKeyStoreHelperL2237(page)...), nil\n}"},
	}},

	{name: "P218: benign bounded struct-field helper copy stays legal", desc: "b.CopyFrom(lboxL229{Data: []byte{0,1,2}}) writing b.Data = o.Data: a paramField-sourced mutation binds the caller's recorded field bound (3 bytes), so the owned copy stays below a complete page", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l22_38.go", content: "package reader\n\ntype lboxL2238 struct{ Data []byte }\n\nfunc (b *lboxL2238) CopyFrom(o lboxL2238) { b.Data = o.Data }\n\nfunc cleanCopyHelperL2238() []byte {\n\tb := lboxL2238{}\n\tb.CopyFrom(lboxL2238{Data: []byte{0, 1, 2}})\n\treturn append([]byte{}, b.Data...)\n}\n\nfunc cleanCopyProbeL2238(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\t_, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn cleanCopyHelperL2238(), nil\n}"},
	}},

	{name: "P219: type assertion on a func-field any call result projects asserted leaves", desc: "h.get().(B).Data with get func() any bound to a closure returning B{Data: page}: the unprovable struct-func-field callee's whole-value taint fails closed on every page-carrying leaf of the asserted type, including a DIRECT call base with no binding object", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l23_1.go", content: "package reader\n\ntype lboxL231 struct{ Data []byte }\n\ntype holderL231 struct{ get func() any }\n\nfunc assertFuncFieldHelperL231(p []byte) []byte {\n\th := holderL231{get: func() any { return lboxL231{Data: p} }}\n\treturn h.get().(lboxL231).Data\n}\n\nfunc assertFuncFieldProbeL231(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, assertFuncFieldHelperL231(page)...), nil\n}"},
	}},

	{name: "P220: type-switch per-case variable from a func-field any call result", desc: "switch v := h.get().(type) { case B: v.Data } with get func() any: the implicit per-case variable carries B's page-carrying leaves because the asserted base's whole-value taint is unknowable", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l23_2.go", content: "package reader\n\ntype lboxL232 struct{ Data []byte }\n\ntype holderL232 struct{ get func() any }\n\nfunc typeSwitchFuncFieldHelperL232(p []byte) []byte {\n\th := holderL232{get: func() any { return lboxL232{Data: p} }}\n\tswitch v := h.get().(type) {\n\tcase lboxL232:\n\t\treturn v.Data\n\t}\n\treturn nil\n}\n\nfunc typeSwitchFuncFieldProbeL232(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, typeSwitchFuncFieldHelperL232(page)...), nil\n}"},
	}},

	{name: "P221: pointer-mutation method on an INDEXED element binds the container", desc: "xs[0].Set(page) with Set(p []byte) { b.Data = p }: a method call on an addressable indexed element mutates the element, so the callee's pointer-parameter field mutations bind the root container's element records and xs[0].Data stays sourced", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l23_3.go", content: "package reader\n\ntype lboxL233 struct{ Data []byte }\n\nfunc (b *lboxL233) Set(p []byte) { b.Data = p }\n\nfunc idxMethHelperL233(p []byte) []byte {\n\txs := make([]lboxL233, 1)\n\txs[0].Set(p)\n\treturn xs[0].Data\n}\n\nfunc idxMethProbeL233(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, idxMethHelperL233(page)...), nil\n}"},
	}},

	{name: "P222: indexed channel send and receive keep element record provenance", desc: "cs[0] <- B{Data: page}; x := <-cs[0]; x.Data with cs []chan B: the send records the root container's element records and the indexed receive resolves them, exactly like the indexed-store read path", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l23_4.go", content: "package reader\n\ntype lboxL234 struct{ Data []byte }\n\nfunc chanIdxHelperL234(p []byte) []byte {\n\tcs := make([]chan lboxL234, 1)\n\tcs[0] = make(chan lboxL234, 1)\n\tcs[0] <- lboxL234{Data: p}\n\tx := <-cs[0]\n\treturn x.Data\n}\n\nfunc chanIdxProbeL234(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, chanIdxHelperL234(page)...), nil\n}"},
	}},

	{name: "P223: pointer-wrapped map parameter keeps key leaf provenance", desc: "func(m *map[*B]int) { for k := range *m { return k.Data } } called with a map holding a page-carrying key: the pointer layer unwraps to the map so the key-only range leaves bind like a plain map parameter", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l23_5.go", content: "package reader\n\ntype lboxL235 struct{ Data []byte }\n\nfunc mapPtrParamHelperL235(m *map[*lboxL235]int) []byte {\n\tfor k := range *m {\n\t\treturn k.Data\n\t}\n\treturn nil\n}\n\nfunc mapPtrParamProbeL235(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tm := map[*lboxL235]int{{Data: page}: 1}\n\treturn append([]byte{}, mapPtrParamHelperL235(&m)...), nil\n}"},
	}},

	{name: "P224: two-value type assertion and map index keep whole-value page taint", desc: "b, ok := v.([]byte) and b, ok := m[k] with v an interface holding a mapped page and m map[string][]byte: go/types types the expression node as the (T, bool) tuple, and the whole-value carrier test must use the value slot or the bound variable launders the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l23_6.go", content: "package reader\n\nfunc twoValAssertHelperL236(v any) []byte {\n\tb, _ := v.([]byte)\n\treturn b\n}\n\nfunc twoValMapIdxHelperL236(m map[string][]byte) []byte {\n\tb, _ := m[\"k\"]\n\treturn b\n}\n\nfunc twoValProbeL236(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar v any = page\n\ta := twoValAssertHelperL236(v)\n\tb := twoValMapIdxHelperL236(map[string][]byte{\"k\": page})\n\treturn append(append([]byte{}, a...), b...), nil\n}"},
	}},

	{name: "P225: self-referential pointer result of an unproven callee must not hang", desc: "type P *P with get func() P called as h.get(): the unproven-callee result walk dereferences pointer wrappers (failClosedCallFields, mapUnderlying) and must stop at the revisiting named type instead of recursing forever; no page flow so the form stays legal", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l23_7.go", content: "package reader\n\ntype selfPtrL237 *selfPtrL237\n\ntype holderL237 struct{ get func() selfPtrL237 }\n\nfunc selfPtrHelperL237() selfPtrL237 {\n\th := holderL237{get: func() selfPtrL237 { return nil }}\n\treturn h.get()\n}\n\nfunc selfPtrProbeL237(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\t_, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn nil, nil\n}"},
	}},

	{name: "P226: foreign exported struct fields keep page leaves in an unproven callee result", desc: "h.get() pem.Block with get func() pem.Block bound to a closure returning pem.Block{Bytes: page}: the EXPORTED field graph of a foreign struct is readable after the call, so the fail-closed projection must walk foreign exported fields like local ones and skip only unexported foreign fields (the bytes.Reader.src shape)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l24_1.go", content: "package reader\n\nimport \"encoding/pem\"\n\ntype holderL241 struct{ get func() pem.Block }\n\nfunc pemHelperL241(p []byte) []byte {\n\th := holderL241{get: func() pem.Block { return pem.Block{Bytes: p} }}\n\treturn h.get().Bytes\n}\n\nfunc pemProbeL241(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, pemHelperL241(page)...), nil\n}"},
	}},

	{name: "P227: direct func-literal struct argument loses fields to a captured write", desc: "func(x lbox) { out = x.Data }(lbox{Data: page}) then return out: a directly called func-literal argument shaped as a COMPOSITE LITERAL never reached the argFlowOf fallback (materializeStructFields returned early on id == nil), so the captured field write laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l24_2.go", content: "package reader\n\ntype lboxL242 struct{ Data []byte }\n\nfunc litArgHelperL242(p []byte) []byte {\n\tout := make([]byte, 4096)\n\tfunc(x lboxL242) { copy(out, x.Data) }(lboxL242{Data: p})\n\treturn out\n}\n\nfunc litArgProbeL242(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, litArgHelperL242(page)...), nil\n}"},
	}},

	{name: "P228: INDEXED SELECTOR-rooted channel send and receive keep element provenance", desc: "h.Chs[0] <- lbox{Data: page}; x := <-h.Chs[0]; x.Data with h.Chs []chan lbox: selector-rooted index chains (h.Chs[0]) were missed by the indexChainRoot-only send/receive recording, so the element records never bound under the Chs. prefix", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l24_3.go", content: "package reader\n\ntype lboxL243 struct{ Data []byte }\ntype holderL243 struct{ Chs []chan lboxL243 }\n\nfunc chanSelIdxHelperL243(p []byte) []byte {\n\th := holderL243{Chs: []chan lboxL243{make(chan lboxL243, 1)}}\n\th.Chs[0] <- lboxL243{Data: p}\n\tx := <-h.Chs[0]\n\treturn x.Data\n}\n\nfunc chanSelIdxProbeL243(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, chanSelIdxHelperL243(page)...), nil\n}"},
	}},

	{name: "P229: returned INDEXED SELECTOR value keeps element records", desc: "return h.Items[0] with h.Items []lbox holding lbox{Data: page}: propagateStructResult only handled the indexChainRoot, so a SELECTED-FIELD container root (h.Items[0]) returned its element without the Items.-prefixed records", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l24_4.go", content: "package reader\n\ntype lboxL244 struct{ Data []byte }\ntype holderL244 struct{ Items []lboxL244 }\n\nfunc retIdxSelHelperL244(h holderL244) lboxL244 { return h.Items[0] }\n\nfunc retIdxSelProbeL244(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := holderL244{Items: []lboxL244{{Data: page}}}\n\treturn append([]byte{}, retIdxSelHelperL244(h).Data...), nil\n}"},
	}},

	{name: "P230: address-of INDEXED SELECTOR element mutation binds the base container", desc: "set(&h.Items[0], page) with set(b *lbox, p []byte) { b.Data = p } then h.Items[0].Data: applySummaryMutations only handled the indexChainRoot, so the &h.Items[0] summary never bound the Items.-prefixed records on the base object", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l24_5.go", content: "package reader\n\ntype lboxL245 struct{ Data []byte }\ntype holderL245 struct{ Items []lboxL245 }\n\nfunc setL245(b *lboxL245, p []byte) { b.Data = p }\n\nfunc addrIdxSelHelperL245(p []byte) []byte {\n\th := holderL245{Items: make([]lboxL245, 1)}\n\tsetL245(&h.Items[0], p)\n\treturn h.Items[0].Data\n}\n\nfunc addrIdxSelProbeL245(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, addrIdxSelHelperL245(page)...), nil\n}"},
	}},

	{name: "P231: MULTI-TYPE type switch cases project leaves on the implicit variable", desc: "switch v := h.get().(type) { case lbox, *lbox: v.(lbox).Data } with get func() any: a multi-type case types the implicit variable with the guard's INTERFACE type, so paramLeafPaths(cv.Type()) projected no case leaves and the per-case assertion laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l24_6.go", content: "package reader\n\ntype lboxL246 struct{ Data []byte }\ntype holderL246 struct{ get func() any }\n\nfunc typeSwitchMultiHelperL246(p []byte) []byte {\n\th := holderL246{get: func() any { return lboxL246{Data: p} }}\n\tswitch v := h.get().(type) {\n\tcase lboxL246, *lboxL246:\n\t\tif b, ok := v.(lboxL246); ok {\n\t\t\treturn b.Data\n\t\t}\n\t}\n\treturn nil\n}\n\nfunc typeSwitchMultiProbeL246(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, typeSwitchMultiHelperL246(page)...), nil\n}"},
	}},

	{name: "P232: direct func-literal struct argument captured write with BOUNDED data stays legal", desc: "func(x lbox) { out = x.Data }(lbox{Data: []byte{0,1,2}}): the composite-literal argFlowOf fallback binds the caller's recorded field bound, so a bounded literal keeps the captured write below a complete page and the form stays accepted", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l24_7.go", content: "package reader\n\ntype lboxL247 struct{ Data []byte }\n\nfunc litArgBoundedHelperL247() []byte {\n\tout := make([]byte, 3)\n\tfunc(x lboxL247) { copy(out, x.Data) }(lboxL247{Data: []byte{0, 1, 2}})\n\treturn out\n}\n\nfunc litArgBoundedProbeL247(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\t_, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, litArgBoundedHelperL247()...), nil\n}"},
	}},

	{name: "P233: type-asserted INDEXED CHANNEL send and receive keep element provenance", desc: "v.(*H).Chs[0] <- lbox{Data: page}; y := <-v.(*H).Chs[0]; y.Data with v an any holding *H: the asserted-base channel chain was missed by the indexChainRoot-only recording (chainRootObject), so the element records never bound on the asserted base", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_1.go", content: "package reader\n\ntype lboxL251 struct{ Data []byte }\ntype holderL251 struct{ Chs []chan lboxL251 }\n\nfunc chanAssertHelperL251(v any, p []byte) []byte {\n\tv.(*holderL251).Chs[0] <- lboxL251{Data: p}\n\ty := <-v.(*holderL251).Chs[0]\n\treturn y.Data\n}\n\nfunc chanAssertProbeL251(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, chanAssertHelperL251(any(&holderL251{Chs: []chan lboxL251{make(chan lboxL251, 1)}}), page)...), nil\n}"},
	}},

	{name: "P234: type-asserted INDEXED STRUCT store keeps element provenance", desc: "v.(*H).Items[0] = lbox{Data: page} then v.(*H).Items[0].Data: the store recorded element fields only on plain or dereferenced roots, never on a TYPE-ASSERTED root, so the asserted read laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_2.go", content: "package reader\n\ntype lboxL252 struct{ Data []byte }\ntype holderL252 struct{ Items []lboxL252 }\n\nfunc idxStoreAssertHelperL252(v any, p []byte) []byte {\n\tv.(*holderL252).Items[0] = lboxL252{Data: p}\n\treturn v.(*holderL252).Items[0].Data\n}\n\nfunc idxStoreAssertProbeL252(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := &holderL252{Items: make([]lboxL252, 1)}\n\treturn append([]byte{}, idxStoreAssertHelperL252(any(h), page)...), nil\n}"},
	}},

	{name: "P235: type-asserted FIELD MAP key store keeps key provenance", desc: "v.(*H).M[&b] = 1 after b.Data = page, then for k := range v.(*H).M { k.Data }: the map-key store bound only selector or dereference roots, never a TYPE-ASSERTED root, so the asserted key-only range laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_3.go", content: "package reader\n\ntype lboxL253 struct{ Data []byte }\ntype holderL253 struct{ M map[*lboxL253]int }\n\nfunc mapKeyAssertHelperL253(v any, p []byte) []byte {\n\tb := lboxL253{Data: p}\n\tv.(*holderL253).M[&b] = 1\n\tfor k := range v.(*holderL253).M {\n\t\treturn k.Data\n\t}\n\treturn nil\n}\n\nfunc mapKeyAssertProbeL253(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := &holderL253{M: map[*lboxL253]int{}}\n\treturn append([]byte{}, mapKeyAssertHelperL253(any(h), page)...), nil\n}"},
	}},

	{name: "P236: address-of TYPE-ASSERTED INDEXED element mutation binds the asserted base", desc: "set(&v.(*H).Items[0], page) with set(b *lbox, p []byte) { b.Data = p } then v.(*H).Items[0].Data: applySummaryMutations bound only selector or plain roots for &xs[0], never a TYPE-ASSERTED root, so the mutation laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_4.go", content: "package reader\n\ntype lboxL254 struct{ Data []byte }\ntype holderL254 struct{ Items []lboxL254 }\n\nfunc setL254(b *lboxL254, p []byte) { b.Data = p }\n\nfunc addrIdxAssertHelperL254(v any, p []byte) []byte {\n\tsetL254(&v.(*holderL254).Items[0], p)\n\treturn v.(*holderL254).Items[0].Data\n}\n\nfunc addrIdxAssertProbeL254(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := &holderL254{Items: make([]lboxL254, 1)}\n\treturn append([]byte{}, addrIdxAssertHelperL254(any(h), page)...), nil\n}"},
	}},

	{name: "P237: returned TYPE-ASSERTED indexed element keeps interface-parameter fields", desc: "return v.(*H).Items[0] with v an any parameter: propagateStructResult's IndexExpr branch resolved only plain or dereferenced roots, so an interface-typed parameter asserted to a holder lost the caller's Items.-prefixed element fields", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_5.go", content: "package reader\n\ntype lboxL255 struct{ Data []byte }\ntype holderL255 struct{ Items []lboxL255 }\n\nfunc retAssertIdxHelperL255(v any) lboxL255 { return v.(*holderL255).Items[0] }\n\nfunc retAssertIdxProbeL255(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := &holderL255{Items: []lboxL255{{Data: page}}}\n\treturn append([]byte{}, retAssertIdxHelperL255(h).Data...), nil\n}"},
	}},

	{name: "P238: returned indexed element of a CALL-PRODUCED selected container keeps element fields", desc: "return makeH(p).Items[0] with makeH building holder{Items: [lbox{Data: p}]}: propagateStructResult only handled an indexChainRoot CALL, never a SELECTED call-produced root (makeH(p).Items[0]), so the returned element laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_6.go", content: "package reader\n\ntype lboxL256 struct{ Data []byte }\ntype holderL256 struct{ Items []lboxL256 }\n\nfunc makeL256(p []byte) holderL256 { return holderL256{Items: []lboxL256{{Data: p}}} }\n\nfunc retCallIdxHelperL256(p []byte) lboxL256 { return makeL256(p).Items[0] }\n\nfunc retCallIdxProbeL256(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, retCallIdxHelperL256(page).Data...), nil\n}"},
	}},

	{name: "P239: type-switch ANY case keeps whole-value taint and asserted leaves", desc: "switch x := h.get().(type) { case any: b := x.(lbox); return b.Data } with get func() any: an interface-typed case (case any) projected no concrete leaves and the implicit variable carried no whole-value taint, so the body-side assertion laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_7.go", content: "package reader\n\ntype lboxL257 struct{ Data []byte }\ntype holderL257 struct{ get func() any }\n\nfunc typeSwitchAnyHelperL257(p []byte) []byte {\n\th := holderL257{get: func() any { return lboxL257{Data: p} }}\n\tswitch x := h.get().(type) {\n\tcase any:\n\t\tb := x.(lboxL257)\n\t\treturn b.Data\n\t}\n\treturn nil\n}\n\nfunc typeSwitchAnyProbeL257(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, typeSwitchAnyHelperL257(page)...), nil\n}"},
	}},
	{name: "P240: type-switch ANY case keeps asserted FIELD MAP key provenance", desc: "switch x := h.get().(type) { case any: for k := range x.(*H).M { return k.Data } } with get func() any returning *H{M: {page:1}}: the interface-typed case var carried no whole-value taint and the asserted map-key store bound no leaf, so the range laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_8.go", content: "package reader\n\ntype lboxL258 struct{ Data []byte }\ntype holderL258 struct {\n\tM   map[*lboxL258]int\n\tget func() any\n}\n\nfunc typeSwitchAnyMapHelperL258(p []byte) []byte {\n\th := holderL258{get: func() any { return &holderL258{M: map[*lboxL258]int{{Data: p}: 1}} }}\n\tswitch x := h.get().(type) {\n\tcase any:\n\t\tfor k := range x.(*holderL258).M {\n\t\t\treturn k.Data\n\t\t}\n\t}\n\treturn nil\n}\n\nfunc typeSwitchAnyMapProbeL258(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, typeSwitchAnyMapHelperL258(page)...), nil\n}"},
	}},

	{name: "P241: returned type-asserted SELECTOR value keeps interface-parameter fields", desc: "return v.(*H).Inner with v an any parameter and H{Inner: lbox{Data: page}}: propagateStructResult's SelectorExpr branch never projected an interface-parameter asserted base, so the returned struct laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_9.go", content: "package reader\n\ntype lboxL259 struct{ Data []byte }\ntype holderL259 struct{ Inner lboxL259 }\n\nfunc retAssertSelHelperL259(v any) lboxL259 { return v.(*holderL259).Inner }\n\nfunc retAssertSelProbeL259(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := &holderL259{Inner: lboxL259{Data: page}}\n\treturn append([]byte{}, retAssertSelHelperL259(h).Data...), nil\n}"},
	}},

	{name: "P242: type-switch ANY case binding an asserted SELECTOR keeps leaves", desc: "switch x := h.get().(type) { case any: b := x.(*H).Inner; return b.Data } with get func() any returning *H{Inner: lbox{Data: page}}: the interface-typed case projected no leaves and the asserted selector bind produced no whole-value taint, so the bound struct laundered the page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l25_10.go", content: "package reader\n\ntype lboxL2510 struct{ Data []byte }\ntype holderL2510 struct {\n\tInner lboxL2510\n\tget   func() any\n}\n\nfunc typeSwitchAnyBindHelperL2510(p []byte) []byte {\n\th := holderL2510{get: func() any { return &holderL2510{Inner: lboxL2510{Data: p}} }}\n\tswitch x := h.get().(type) {\n\tcase any:\n\t\tb := x.(*holderL2510).Inner\n\t\treturn b.Data\n\t}\n\treturn nil\n}\n\nfunc typeSwitchAnyBindProbeL2510(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn append([]byte{}, typeSwitchAnyBindHelperL2510(page)...), nil\n}"},
	}},

	{name: "P243: range over a mapped page into an owned [4096]byte", desc: "for i, b := range page { out[i] = b } with page from r.page(pgno): the range over a page-tainted slice makes the loop body a page-sourcing context; the element writes into the owned array aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l26_1.go", content: "package reader\n\nfunc rangeCopyProbe(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i, b := range page {\n\t\tout[i] = b\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P244: indexed for loop over a mapped page into an owned [4096]byte", desc: "for i := 0; i < len(page); i++ { out[i] = page[i] } with page from r.page(pgno): the for loop whose condition references the length of a page-tainted slice makes the loop body a page-sourcing context; the element writes into the owned array aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l26_2.go", content: "package reader\n\nfunc indexedCopyProbe(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i < len(page); i++ {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P245: byte-by-byte append of a mapped page into an owned slice", desc: "for i := 0; i < len(page); i++ { out = append(out, page[i]) } with page from r.page(pgno): the for loop whose condition references the length of a page-tainted slice makes the loop body a page-sourcing context; the byte-by-byte appends into the owned slice aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l26_3.go", content: "package reader\n\nfunc appendCopyProbe(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 4096)\n\tfor i := 0; i < len(page); i++ {\n\t\tout = append(out, page[i])\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P246: aliased-length indexed loop over a mapped page into an owned [4096]byte", desc: "n := len(page); for i := 0; i < n; i++ { out[i] = page[i] } with page from r.page(pgno): the for loop whose condition uses a len-of-page alias makes the loop body a page-sourcing context; the element writes into the owned array aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l26_4.go", content: "package reader\n\nfunc aliasedLenCopyProbe(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tn := len(page)\n\tfor i := 0; i < n; i++ {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P247: range over the destination array copying from a mapped page", desc: "for i := range out { out[i] = page[i] } with out an owned [4096]byte and page from r.page(pgno): the range over a PageSize destination array makes the loop body a page-sourcing context; the element writes aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l26_5.go", content: "package reader\n\nfunc rangeDestCopyProbe(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := range out {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P248: helper-mediated element copy of a mapped page", desc: "for i, b := range page { putByte(out[:], i, b) } with putByte(dst []byte, i int, b byte) { dst[i] = b }: the helper writes a page byte into the caller's owned buffer; the call site fails because the callee parameter is an element sink and the caller is in a page-sourcing loop", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l27_1.go", content: "package reader\n\nfunc putByteL27(dst []byte, i int, b byte) { dst[i] = b }\n\nfunc helperCopyProbeL27(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i, b := range page {\n\t\tputByteL27(out[:], i, b)\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P249: selector destination element copy of a mapped page", desc: "for i, b := range page { h.Out[i] = b } with h a struct holding [4096]byte: a field destination resolves through the selector root and field path; the element writes aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l27_2.go", content: "package reader\n\ntype holderL27 struct{ Out [4096]byte }\n\nfunc selectorCopyProbeL27(r *ImmutableReader, pgno uint32) (holderL27, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn holderL27{}, err\n\t}\n\th := holderL27{}\n\tfor i, b := range page {\n\t\th.Out[i] = b\n\t}\n\treturn h, nil\n}"},
	}},

	{name: "P250: slice destination element copy of a mapped page", desc: "out := make([]byte, 4096); for i := range out { out[i] = page[i] }: the range over an owned PageSize slice makes the loop body a page-copy context; the element writes aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l27_3.go", content: "package reader\n\nfunc sliceDestCopyProbeL27(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 4096)\n\tfor i := range out {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P251: for post-clause append copies a mapped page", desc: "for ; n < len(page); n, out = n+1, append(out, page[n]) {}: the post clause executes inside the page-sourcing loop; the byte-by-byte appends into the owned slice aggregate to a complete page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l27_4.go", content: "package reader\n\nfunc postClauseCopyProbeL27(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 4096)\n\tn := 0\n\tfor ; n < len(page); n, out = n+1, append(out, page[n]) {\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P252: named-constant loop bound copies a mapped page", desc: "const N = 4096; for i := 0; i < N; i++ { out[i] = page[i] }: a named constant equal to PageSize is resolved through go/types constant values; the loop body is a page-sourcing context", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l27_5.go", content: "package reader\n\nconst pageSizeL27 = 4096\n\nfunc constBoundCopyProbeL27(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i < pageSizeL27; i++ {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P253 benign: zero-initialization of an owned PageSize array", desc: "var out [4096]byte; for i := range out { out[i] = 0 }: a destination-ranging loop with a clean scalar RHS initializes the buffer; no page source reaches the writes, so the complete-page rule stays silent", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l27_6.go", content: "package reader\n\nfunc benignZeroProbeL27() [4096]byte {\n\tvar out [4096]byte\n\tfor i := range out {\n\t\tout[i] = 0\n\t}\n\treturn out\n}"},
	}},

	{name: "P254: nested page-source loop containing a destination range", desc: "for i := 0; i < len(page); i++ { for j := range out { out[j] = page[i] } }: the inner destination range must close its own loop context; decrementing the outer page-source counter corrupts nesting and loses the complete-page copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l27_7.go", content: "package reader\n\nfunc nestedMixProbeL30(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i < len(page); i++ {\n\t\tfor j := range out {\n\t\t\tout[j] = page[i]\n\t\t}\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P255: helper sink aliases the destination parameter", desc: "func put(dst []byte, i int, b byte) { d := dst; d[i] = b } called inside for i, b := range page: local aliases of a sink parameter keep the element-write summary", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_1.go", content: "package reader\n\nfunc putByteAliasL28(dst []byte, i int, b byte) {\n\td := dst\n\td[i] = b\n}\n\nfunc helperAliasProbeL28(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i, b := range page {\n\t\tputByteAliasL28(out[:], i, b)\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P256: pointer destination element copy of a mapped page", desc: "p := &out; for i, b := range page { (*p)[i] = b }: pointer destinations dereference to the pointee array before the PageSize ownership test", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_2.go", content: "package reader\n\nfunc ptrDestProbeL28(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tp := &out\n\tfor i, b := range page {\n\t\t(*p)[i] = b\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P257: method receiver field is a page sink", desc: "func (s sink) put(i int, b byte) { s.buf[i] = b }; h.put(i, b) inside a page loop: selector-rooted writes mark the receiver parameter as an element sink", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_3.go", content: "package reader\n\ntype sinkSL28 struct{ buf []byte }\n\nfunc (s sinkSL28) putL28(i int, b byte) { s.buf[i] = b }\n\nfunc methodSinkProbeL28(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 4096)\n\th := sinkSL28{buf: out}\n\tfor i, b := range page {\n\t\th.putL28(i, b)\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P258: page-sink helper used in assignment RHS", desc: "_ = put(out[:], i, b) with put returning byte and writing dst[i]: value-position helper calls in a page-sourcing loop are complete-page copies", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_4.go", content: "package reader\n\nfunc putByteRHSByteL28(dst []byte, i int, b byte) byte { dst[i] = b; return b }\n\nfunc helperRHSProbeL28(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i, b := range page {\n\t\t_ = putByteRHSByteL28(out[:], i, b)\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P259: range over a slice alias of an owned PageSize destination", desc: "out := make([]byte, 4096); dst := out; for i := range dst[:] { dst[i] = page[i] }: identifier and slice-expression aliases share the destination's make length", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_5.go", content: "package reader\n\nfunc sliceAliasRangeProbeL28(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 4096)\n\tdst := out\n\tfor i := range dst[:] {\n\t\tdst[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P260: chained len(page) aliases keep the page-source bound", desc: "n := len(page); m := n; for i := 0; i < m; i++ { out[i] = page[i] }: chained identifier aliases preserve the page-derived loop bound", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_6.go", content: "package reader\n\nfunc chainedLenProbeL28(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tn := len(page)\n\tm := n\n\tfor i := 0; i < m; i++ {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P261: bounded page spans assembled by repeated copy", desc: "copy(out[:2048], page[:2048]); copy(out[2048:4096], page[2048:4096]): bounded copies into one destination root accumulate to a complete owned page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_7.go", content: "package reader\n\nfunc boundedAssemblyProbeL28(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 4096)\n\tcopy(out[:2048], page[:2048])\n\tcopy(out[2048:4096], page[2048:4096])\n\treturn out, nil\n}"},
	}},

	{name: "P262: bounded page spans assembled by repeated append", desc: "out = append(out, page[:2048]...); out = append(out, page[2048:4096]...): repeated bounded appends into one destination variable assemble a complete owned page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_8.go", content: "package reader\n\nfunc boundedAssemblyAppendProbeL28(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 4096)\n\tout = append(out, page[:2048]...)\n\tout = append(out, page[2048:4096]...)\n\treturn out, nil\n}"},
	}},

	{name: "P263: bounded page spans assembled through a field destination", desc: "copy(h.Buf[:2048], page[:2048]); copy(h.Buf[2048:4096], page[2048:4096]): selector-rooted destinations share the root object's bounded-copy accumulation", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_9.go", content: "package reader\n\ntype holderL28Acc struct{ Buf [4096]byte }\n\nfunc selectorAssemblyProbeL28(r *ImmutableReader, pgno uint32) (holderL28Acc, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn holderL28Acc{}, err\n\t}\n\tvar h holderL28Acc\n\tcopy(h.Buf[:2048], page[:2048])\n\tcopy(h.Buf[2048:4096], page[2048:4096])\n\treturn h, nil\n}"},
	}},

	{name: "P264: bounded page spans appended through a field destination", desc: "h.Buf = append(h.Buf, page[:2048]...); h.Buf = append(h.Buf, page[2048:4096]...): selector-rooted append destinations resolve to the root variable's accumulation counter", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_10.go", content: "package reader\n\ntype holderAppendL28 struct{ Buf []byte }\n\nfunc selectorAppendAssemblyProbeL28(r *ImmutableReader, pgno uint32) (holderAppendL28, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn holderAppendL28{}, err\n\t}\n\th := holderAppendL28{Buf: make([]byte, 0, 4096)}\n\th.Buf = append(h.Buf, page[:2048]...)\n\th.Buf = append(h.Buf, page[2048:4096]...)\n\treturn h, nil\n}"},
	}},

	{name: "P265: bounded page spans appended through slice alias rebinds", desc: "a := out; a = append(a, page[:2048]...); b := a; b = append(b, page[2048:4096]...): alias rebinds transfer one buffer's accumulated bounded-append bytes", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_11.go", content: "package reader\n\nfunc aliasAppendProbeL28(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 4096)\n\ta := out\n\ta = append(a, page[:2048]...)\n\tb := a\n\tb = append(b, page[2048:4096]...)\n\treturn b, nil\n}"},
	}},

	{name: "P266 benign: two one-byte page appends stay bounded", desc: "out = append(out, page[0:1]...); out = append(out, page[1:2]...): append accumulation sums source spans, so two provably bounded bytes cannot reach PageSize", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l28_12.go", content: "package reader\n\nfunc tinyAppendProbeL28(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 4)\n\tout = append(out, page[0:1]...)\n\tout = append(out, page[1:2]...)\n\treturn out, nil\n}"},
	}},

	{name: "P267: bounded page spans appended through a var-declared alias", desc: "var b = a between two bounded page appends: ValueSpec aliases resolve to the canonical bounded-span destination", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_1.go", content: "package reader\n\nfunc varAliasAppendProbeL29(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 4096)\n\ta := out\n\ta = append(a, page[:2048]...)\n\tvar b = a\n\tb = append(b, page[2048:4096]...)\n\treturn b, nil\n}"},
	}},

	{name: "P268 benign: bounded page copies into distinct fields stay separate", desc: "copy(h.A, page[:2048]); copy(h.B, page[2048:4096]) with A and B separate 2048-byte fields: field-path keys prevent joining independent destinations", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_2.go", content: "package reader\n\ntype twoBufsL29 struct{ A, B []byte }\n\nfunc twoFieldCopyProbeL29(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\th := twoBufsL29{A: make([]byte, 2048), B: make([]byte, 2048)}\n\tcopy(h.A, page[:2048])\n\tcopy(h.B, page[2048:4096])\n\treturn nil\n}"},
	}},

	{name: "P269: bounded page spans assembled by copy and append into one destination", desc: "copy(out[:2048], page[:2048]); out = append(out[:2048], page[2048:4096]...): copy and append share one canonical bounded-span accumulator", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_3.go", content: "package reader\n\nfunc copyThenAppendProbeL29(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 4096)\n\tcopy(out[:2048], page[:2048])\n\tout = append(out[:2048], page[2048:4096]...)\n\treturn out, nil\n}"},
	}},

	{name: "P270: bounded copy spans through an identifier alias", desc: "a := out; copy(a[:2048], page[:2048]); copy(out[2048:], page[2048:4096]): identifier aliases resolve to one canonical bounded-span destination", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_4.go", content: "package reader\n\nfunc copyAliasProbeL29(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 4096)\n\ta := out\n\tcopy(a[:2048], page[:2048])\n\tcopy(out[2048:], page[2048:4096])\n\treturn out, nil\n}"},
	}},

	{name: "P271 benign: alias rebind to a fresh small buffer stops accumulation", desc: "a := out; append 2048 page bytes; a = make([]byte, 0, 8); append 2048 page bytes: each buffer receives at most 2048 bytes and no complete page exists", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_5.go", content: "package reader\n\nfunc rebindAliasProbeL29(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 0, 4096)\n\ta := out\n\ta = append(a, page[:2048]...)\n\ta = make([]byte, 0, 8)\n\ta = append(a, page[:2048]...)\n\treturn a, nil\n}"},
	}},

	{name: "P272: bounded appends through an asserted field destination", desc: "v.(*T).Buf = append(v.(*T).Buf, page[:2048]...) twice: type-asserted selector roots resolve to one canonical destination", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_6.go", content: "package reader\n\ntype typeAssertHolderL29 struct{ Buf []byte }\n\nfunc typeAssertAppendProbeL29(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tvar v interface{} = &typeAssertHolderL29{Buf: make([]byte, 0, 4096)}\n\tv.(*typeAssertHolderL29).Buf = append(v.(*typeAssertHolderL29).Buf, page[:2048]...)\n\tv.(*typeAssertHolderL29).Buf = append(v.(*typeAssertHolderL29).Buf, page[2048:4096]...)\n\treturn nil\n}"},
	}},

	{name: "P273: bounded copy and append through a re-slice alias", desc: "sl := out[:2048]; copy(out[:2048], page[:2048]); sl = append(sl, page[2048:4096]...): slice aliases resolve to the same canonical destination", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_7.go", content: "package reader\n\nfunc sliceAliasAppendProbeL29(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tout := make([]byte, 4096)\n\tsl := out[:2048]\n\tcopy(out[:2048], page[:2048])\n\tsl = append(sl, page[2048:4096]...)\n\treturn out, nil\n}"},
	}},

	{name: "P274 benign: page slice headers appended to a slice of slices", desc: "chunks = append(chunks, page[:2048], page[2048:4096]): byte-span accumulation applies only to byte-element destinations, not [][]byte slice-header collections", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_8.go", content: "package reader\n\nfunc chunksAppendProbeL29(r *ImmutableReader, pgno uint32) ([][]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tchunks := make([][]byte, 0, 2)\n\tchunks = append(chunks, page[:2048], page[2048:4096])\n\treturn chunks, nil\n}"},
	}},

	{name: "P275: bounded copy spans through an asserted field destination", desc: "copy(v.(*H).Buf[:2048], page[:2048]); copy(v.(*H).Buf[2048:], page[2048:4096]): type-asserted copy destinations resolve to one canonical root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_9.go", content: "package reader\n\ntype typeAssertCopyL29 struct{ Buf []byte }\n\nfunc typeAssertCopyProbeL29(r *ImmutableReader, pgno uint32) error {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn err\n\t}\n\tvar v interface{} = &typeAssertCopyL29{Buf: make([]byte, 4096)}\n\tcopy(v.(*typeAssertCopyL29).Buf[:2048], page[:2048])\n\tcopy(v.(*typeAssertCopyL29).Buf[2048:], page[2048:4096])\n\treturn nil\n}"},
	}},

	{name: "P276 benign: full page slice header appended to a slice of slices", desc: "chunks = append(chunks, page[:]): the destination stores a slice header, not a complete page of owned bytes", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_10.go", content: "package reader\n\nfunc fullPageSliceHeaderProbeL29(r *ImmutableReader, pgno uint32) ([][]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tchunks := make([][]byte, 0, 2)\n\tchunks = append(chunks, page[:])\n\treturn chunks, nil\n}"},
	}},

	{name: "P277 benign: slice-header copy into a slice of slices", desc: "copy(chunks, [][]byte{page[:]}): the operation copies slice headers, not page bytes into an owned byte buffer", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l29_11.go", content: "package reader\n\nfunc sliceHeaderCopyProbeL29(r *ImmutableReader, pgno uint32) ([][]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tchunks := make([][]byte, 1)\n\tcopy(chunks, [][]byte{page[:]})\n\treturn chunks, nil\n}"},
	}},

	{name: "P278: range over a PageSize integer constant", desc: "for i := range 4096 { out[i] = page[i] }: integer ranges with PageSize iterations are page-sourcing contexts; element writes aggregate to a complete page", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l30_1.go", content: "package reader\n\nfunc rangeConstProbeL30(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := range 4096 {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P279: half-page loop writing two page bytes per iteration", desc: "for i := 0; i < 2048; i++ { out[2*i] = page[2*i]; out[2*i+1] = page[2*i+1] }: iteration count times page-derived indexed writes reaches PageSize", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l30_2.go", content: "package reader\n\nfunc twoWritesProbeL30(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i < 2048; i++ {\n\t\tout[2*i] = page[2*i]\n\t\tout[2*i+1] = page[2*i+1]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P280 benign: repeated slice-header copies into [][]byte", desc: "copy(chunks, [][]byte{page[:2048]}); copy(chunks, [][]byte{page[2048:]}): bounded-span accumulation is restricted to byte-element destinations", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l30_3.go", content: "package reader\n\nfunc sliceHeaderCopiesProbeL30(r *ImmutableReader, pgno uint32) ([][]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tchunks := make([][]byte, 2)\n\tcopy(chunks, [][]byte{page[:2048]})\n\tcopy(chunks[1:], [][]byte{page[2048:]})\n\treturn chunks, nil\n}"},
	}},

	{name: "P281: half-page integer range writing two page bytes per iteration", desc: "for i := range 2048 { out[2*i] = page[2*i]; out[2*i+1] = page[2*i+1] }: integer-range bound times page-derived indexed writes reaches PageSize", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l31_1.go", content: "package reader\n\nfunc range2048TwoProbeL31(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := range 2048 {\n\t\tout[2*i] = page[2*i]\n\t\tout[2*i+1] = page[2*i+1]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P282: nested constant loops compose to a complete page", desc: "for i := 0; i < 64; i++ { for j := 0; j < 64; j++ { out[i*64+j] = page[i*64+j] } }: nested bounded-loop iterations multiply before applying page-derived write counts", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l31_2.go", content: "package reader\n\nfunc nested64ProbeL31(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i < 64; i++ {\n\t\tfor j := 0; j < 64; j++ {\n\t\t\tout[i*64+j] = page[i*64+j]\n\t\t}\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P283: field aliases join bounded copy spans", desc: "h = faBoxL31{left: owned[:2048], right: owned[2048:]}; copy(h.left, page[:2048]); copy(h.right, page[2048:4096]): struct-literal field aliases canonicalize to the backing buffer", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l31_3.go", content: "package reader\n\ntype faBoxL31 struct{ left, right []byte }\n\nfunc fieldAliasCopyL31(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\towned := make([]byte, 4096)\n\th := faBoxL31{left: owned[:2048], right: owned[2048:]}\n\tcopy(h.left, page[:2048])\n\tcopy(h.right, page[2048:4096])\n\treturn h.left, nil\n}"},
	}},

	{name: "P284: field aliases join bounded append spans", desc: "h = faBox2L31{left: owned[:2048], right: owned[2048:]}; appends through both fields canonicalize to the backing buffer", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l31_4.go", content: "package reader\n\ntype faBox2L31 struct{ left, right []byte }\n\nfunc fieldAliasAppendL31(r *ImmutableReader, pgno uint32) ([]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\towned := make([]byte, 0, 4096)\n\th := faBox2L31{left: owned[:2048], right: owned[2048:]}\n\th.left = append(h.left, page[:2048]...)\n\th.right = append(h.right, page[2048:4096]...)\n\treturn h.left, nil\n}"},
	}},

	{name: "P285: descending bounded loop copies a complete page", desc: "for i := 4095; i >= 0; i-- { out[i] = page[i] }: descending inclusive loop iteration counts compose with page-derived writes", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l32_1.go", content: "package reader\n\nfunc descendingProbeL32(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 4095; i >= 0; i-- {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P286: ascending inclusive bounded loop copies a complete page", desc: "for i := 0; i <= 4096; i++ { out[i] = page[i] }: inclusive iteration counts compose with page-derived writes", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l32_2.go", content: "package reader\n\nfunc inclusiveProbeL32(r *ImmutableReader, pgno uint32) ([4097]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4097]byte{}, err\n\t}\n\tvar out [4097]byte\n\tfor i := 0; i <= 4096; i++ {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P287: bounded-loop arithmetic saturates instead of wrapping", desc: "a constant bound of 1<<62 cannot wrap iteration×write arithmetic to zero; any positive page-derived write count remains a page-copy context", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l32_3.go", content: "package reader\n\nfunc overflowProbeL32(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i < 1<<62; i++ {\n\t\tout[i] = page[i]\n\t\tif i >= 4095 {\n\t\t\tbreak\n\t\t}\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P288: unsigned 1<<63 loop bound saturates", desc: "for i := uint64(0); i < 1<<63; i++ copies page bytes with a bounded break: unsigned bounds beyond MaxInt64 cannot wrap signed arithmetic to zero", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l33_1.go", content: "package reader\n\nfunc unsignedOverflowProbeL33(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := uint64(0); i < 1<<63; i++ {\n\t\tout[i] = page[i]\n\t\tif i >= 4095 {\n\t\t\tbreak\n\t\t}\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P289 benign: 4095-byte bounded loop stays sub-page", desc: "for i := 0; i < 4095; i++ { out[i] = page[i] }: exact iteration counting must not inflate an exclusive bound by one", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l33_2.go", content: "package reader\n\nfunc subPageLoopProbeL33(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i < 4095; i++ {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P290: unsigned 1<<63 inclusive bound saturates through subtraction", desc: "for i := uint64(0); i <= 1<<63; i++ copies page bytes with a bounded break: inclusive hi-lo+1 arithmetic must saturate instead of wrapping to zero", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l34_1.go", content: "package reader\n\nfunc unsignedInclusiveOverflowProbeL34(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := uint64(0); i <= 1<<63; i++ {\n\t\tout[i] = page[i]\n\t\tif i >= 4095 {\n\t\t\tbreak\n\t\t}\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P291 benign: one-iteration huge-step loop copies one byte", desc: "for i := 0; i < math.MaxInt64; i += math.MaxInt64 { out[i] = page[i] }: exact division preserves one iteration and does not reject a sub-page copy", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l35_1.go", content: "package reader\n\nimport \"math\"\n\nfunc oneHugeStepProbeL35(r *ImmutableReader, pgno uint32) ([1]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [1]byte{}, err\n\t}\n\tvar out [1]byte\n\tfor i := 0; i < math.MaxInt64; i += math.MaxInt64 {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P292 benign: unreachable empty bound stays zero iterations", desc: "for i := 1; i < 0; i++ never executes; an empty range cannot make a one-byte page write into a complete-page copy", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l36_1.go", content: "package reader\n\nfunc emptyBoundProbeL36(r *ImmutableReader, pgno uint32) ([1]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [1]byte{}, err\n\t}\n\tvar out [1]byte\n\tfor i := 1; i < 0; i++ {\n\t\tout[i] = page[i]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P293: negative loop bounds can still iterate PageSize times", desc: "for i := -4095; i < 1; i++ { out[i+4095] = page[i+4095] }: negative start bounds preserve exact iteration counts instead of collapsing to zero", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l37_1.go", content: "package reader\n\nfunc negativeBoundProbeL37(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := -4095; i < 1; i++ {\n\t\tout[i+4095] = page[i+4095]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P294: MinInt64-to-zero loop counts without wrapping", desc: "for i := math.MinInt64; i < 0; i++ { out[uint64(i)&4095] = page[uint64(i)&4095] }: subtraction across MinInt64 saturates instead of wrapping to a zero bound", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l38_1.go", content: "package reader\n\nimport \"math\"\n\nfunc minIntBoundProbeL38(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := int64(math.MinInt64); i < 0; i++ {\n\t\tout[uint64(i)&4095] = page[uint64(i)&4095]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P295 benign: empty inclusive loop stays zero iterations", desc: "for i := 1; i <= 0; i++ never executes; inclusive arithmetic must not turn an inverted bound into one page-derived write", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l39_1.go", content: "package reader\n\nfunc emptyInclusiveProbeL39(r *ImmutableReader, pgno uint32) ([1]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [1]byte{}, err\n\t}\n\tvar out [1]byte\n\tfor i := 1; i <= 0; i++ {\n\t\tout[0] = page[0]\n\t}\n\treturn out, nil\n}"},
	}},

	{name: "P296: singleton inclusive loop with an inner PageSize loop", desc: "for i := 0; i <= 0; i++ { for j := 0; j < 4096; j++ { out[j] = page[j] } }: hi==lo denotes one inclusive iteration, not an empty loop", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_l40_1.go", content: "package reader\n\nfunc singletonInnerPageProbeL40(r *ImmutableReader, pgno uint32) ([4096]byte, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn [4096]byte{}, err\n\t}\n\tvar out [4096]byte\n\tfor i := 0; i <= 0; i++ {\n\t\tfor j := 0; j < 4096; j++ {\n\t\t\tout[j] = page[j]\n\t\t}\n\t}\n\treturn out, nil\n}"},
	}},
}
