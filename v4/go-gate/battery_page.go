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
}
