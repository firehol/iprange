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
}
