package main

// Code generated from check-import-graph.sh by extract_battery.py; DO NOT EDIT BY HAND.

// The durable mutation battery: every transfer form below must make

// the gate fail; the benign forms must pass. Shell-side cases (module

// graph, x/sys ownership, boundary imports) live in the shell harness.

type batteryOp struct {
	kind    string // "create" | "append" | "ins" | "inject"
	path    string
	content string
}

type batteryCase struct {
	name       string
	desc       string
	expectFail bool
	// expectRule, when set, is a substring that must appear in a gate
	// violation; an unrelated type-check failure cannot satisfy the case.
	expectRule string
	// allowTypeCheck marks deliberately invalid-Go or binary-only
	// fail-closed shapes whose rejection is itself the contract.
	allowTypeCheck bool
	ops            []batteryOp
}

var batteryCases = []batteryCase{

	{name: "1: direct io.ReadAll call", desc: "direct io.ReadAll call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_readall/mut.go", content: "package gatemut_readall\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nvar file *os.File\n\nfunc use() { _, _ = io.ReadAll(file) }"},
	}},

	{name: "2: io.ReadAll function alias", desc: "io.ReadAll function alias", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_alias/mut.go", content: "package gatemut_alias\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nvar file *os.File\n\nvar rd = io.ReadAll\n\nfunc use() { _, _ = rd(file) }"},
	}},

	{name: "3: os.File.Read method value", desc: "os.File.Read method value", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_methodval/mut.go", content: "package gatemut_methodval\n\nimport \"os\"\n\nvar file *os.File\n\nvar m = file.Read\n\nfunc use() { var b []byte; _, _ = m(b) }"},
	}},

	{name: "4: os.File.Seek call", desc: "os.File.Seek call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_seek/mut.go", content: "package gatemut_seek\n\nimport \"os\"\n\nvar file *os.File\n\nfunc use() { _, _ = file.Seek(0, 0) }"},
	}},

	{name: "5: os.ReadFile in a new package directory", desc: "os.ReadFile in a new package directory", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_newdir/mut.go", content: "package gatemut_newdir\n\nimport \"os\"\n\nfunc read(p string) ([]byte, error) { return os.ReadFile(p) }"},
	}},

	{name: "6: unix.Readv descriptor read in the mapping owner", desc: "unix.Readv descriptor read in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_readv.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc readv(fd int, b [][]byte) (int, error) { return unix.Readv(fd, b) }"},
	}},

	{name: "7: bufio.NewReader(file).ReadByte", desc: "bufio.NewReader(file).ReadByte", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_bufio/mut.go", content: "package gatemut_bufio\n\nimport (\n\t\"bufio\"\n\t\"os\"\n)\n\nvar file *os.File\n\nfunc use() (byte, error) { return bufio.NewReader(file).ReadByte() }"},
	}},

	{name: "8: dot-imported os.ReadFile", desc: "dot-imported os.ReadFile", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_dotimport/mut.go", content: "package gatemut_dotimport\n\nimport . \"os\"\n\nfunc read(p string) ([]byte, error) { return ReadFile(p) }"},
	}},

	{name: "9: single-line bufio import with Peek", desc: "single-line bufio import with Peek", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_singleline_bufio.go", content: "package iprangedb\n\nimport \"bufio\"\n\nvar br *bufio.Reader\n\nfunc peek() ([]byte, error) { return br.Peek(1) }"},
	}},

	{name: "10: aliased bufio import with Peek", desc: "aliased bufio import with Peek", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_aliased_bufio.go", content: "package iprangedb\n\nimport b \"bufio\"\n\nvar br *b.Reader\n\nfunc peek() ([]byte, error) { return br.Peek(1) }"},
	}},

	{name: "11: windows-only package with os.ReadFile", desc: "windows-only package with os.ReadFile", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_winfile/mut.go", content: "//go:build windows\n\npackage gatemut_winfile\n\nimport \"os\"\n\nfunc read(p string) ([]byte, error) { return os.ReadFile(p) }"},
	}},

	{name: "12: fmt.Fscan over a file", desc: "fmt.Fscan over a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_fscan/mut.go", content: "package gatemut_fscan\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar f *os.File\n\nfunc use(x any) (int, error) { return fmt.Fscan(f, x) }"},
	}},

	{name: "13: io.CopyN between files", desc: "io.CopyN between files", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_copyn/mut.go", content: "package gatemut_copyn\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nvar f *os.File\nvar d *os.File\n\nfunc use() { _, _ = io.CopyN(d, f, 10) }"},
	}},

	{name: "14: reflection-invoked Read", desc: "reflection-invoked Read", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_reflect/mut.go", content: "package gatemut_reflect\n\nimport (\n\t\"os\"\n\t\"reflect\"\n)\n\nvar f *os.File\n\nfunc use() { _ = reflect.ValueOf(f).MethodByName(\"Read\").Call(nil) }"},
	}},

	{name: "15: raw unix.Syscall(SYS_READ) in the mapping owner", desc: "raw unix.Syscall(SYS_READ) in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rawsys.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc rawRead(fd int) (int, error) {\n\tn, _, e := unix.Syscall(unix.SYS_READ, uintptr(fd), 0, 0)\n\treturn int(n), e\n}"},
	}},

	{name: "16: unix.CopyFileRange in the mapping owner", desc: "unix.CopyFileRange in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_cfr.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc copyRange(a, b, n int) (int, error) {\n\treturn unix.CopyFileRange(a, nil, b, nil, n, 0)\n}"},
	}},

	{name: "17: forbidden transfer sharing a line with a tolerated call", desc: "forbidden transfer sharing a line with a tolerated call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_exline/mut.go", content: "package gatemut_exline\n\nimport \"os\"\n\nvar f *os.File\nvar c = struct{ r *os.File }{f}\n\nfunc use() {\n\tvar b [1]byte\n\t_, _ = f.Read(b[:]); _, _ = c.r.Read(b[:]) // tolerated call on the same line must not hide the file read\n}"},
	}},

	{name: "19: encoding/json decoder over a file", desc: "encoding/json decoder over a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_decoder/mut.go", content: "package gatemut_decoder\n\nimport (\n\t\"encoding/json\"\n\t\"os\"\n)\n\nvar f *os.File\n\nfunc use() { var x any; _ = json.NewDecoder(f).Decode(&x) }"},
	}},

	{name: "20: os.File.WriteString", desc: "os.File.WriteString", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_writestr/mut.go", content: "package gatemut_writestr\n\nimport \"os\"\n\nvar f *os.File\n\nfunc use() { _, _ = f.WriteString(\"payload\") }"},
	}},

	{name: "21: nested transfer inside the tolerated call node", desc: "forbidden transfer nested inside the tolerated call node", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_nested/mut.go", content: "package gatemut_nested\n\nimport \"os\"\n\nvar f *os.File\nvar c = struct{ r *os.File }{f}\n\nfunc use() {\n\tvar b [1]byte\n\t_ = c.r.Read(f.Read(b[:])) // intentional textual probe (cannot typecheck: no\n\t// []byte-typed file-read expression exists); the nested transfer must\n\t// stay visible to the gate, not be blanked with the tolerated node\n}"},
	}},

	{name: "22: reflection Method(i).Call", desc: "reflection Method(i).Call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_refmeth/mut.go", content: "package gatemut_refmeth\n\nimport (\n\t\"os\"\n\t\"reflect\"\n)\n\nvar f *os.File\n\nfunc use() { _ = reflect.ValueOf(f).Method(2).Call(nil) }"},
	}},

	{name: "23: io.ReadFull over a file", desc: "io.ReadFull over a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_readfull/mut.go", content: "package gatemut_readfull\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nvar f *os.File\n\nfunc use() { var b [10]byte; _, _ = io.ReadFull(f, b[:]) }"},
	}},

	{name: "24: io.ReadAtLeast over a file", desc: "io.ReadAtLeast over a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_readleast/mut.go", content: "package gatemut_readleast\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nvar f *os.File\n\nfunc use() { var b [10]byte; _, _ = io.ReadAtLeast(f, b[:], 1) }"},
	}},

	{name: "25: log package writing to a file", desc: "log package writing to a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_logw/mut.go", content: "package gatemut_logw\n\nimport (\n\t\"log\"\n\t\"os\"\n)\n\nvar f *os.File\n\nfunc use() { log.New(f, \"\", 0).Println(\"payload\") }"},
	}},

	{name: "26: flate.NewWriter over a file", desc: "flate.NewWriter over a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_flatew/mut.go", content: "package gatemut_flatew\n\nimport (\n\t\"compress/flate\"\n\t\"os\"\n)\n\nvar f *os.File\n\nfunc use() { w, _ := flate.NewWriter(f, 6); w.Close() }"},
	}},

	{name: "27: transfer nested inside the io.ReadFull exemption node", desc: "transfer nested inside the io.ReadFull exemption node", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_rfshadow/mut.go", content: "package gatemut_rfshadow\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nvar f *os.File\nvar zr *os.File // in-memory-looking receiver name\n\nfunc mv(n int, e error) []byte { var b [8]byte; _ = n; _ = e; return b[:] }\n\nfunc use() {\n\tvar b [8]byte\n\t_, _ = io.ReadFull(zr, mv(f.Read(b[:]))) // nested transfer must stay visible\n}"},
	}},

	{name: "28: io.ReadFull over a file-backed flate reader", desc: "io.ReadFull over a file-backed flate reader", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_zrfile/mut.go", content: "package gatemut_zrfile\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc use(f *os.File) {\n\tvar b [8]byte\n\tzr := flate.NewReader(f) // file-backed inflater: must not be exempted\n\t_, _ = io.ReadFull(zr, b[:])\n}"},
	}},

	{name: "29: file-backed c.r receiver", desc: "file-backed c.r receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_crfile/mut.go", content: "package gatemut_crfile\n\nimport \"os\"\n\ntype T struct{ r *os.File }\n\nfunc (c *T) use() {\n\tvar b [1]byte\n\t_, _ = c.r.Read(b[:]) // file-backed receiver must not be exempted\n}"},
	}},

	{name: "30: file-backed zr/out reader with a different index shape", desc: "file-backed zr/out reader with a different index shape", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_zrout/mut.go", content: "package gatemut_zrout\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc use(f *os.File) {\n\tzr := flate.NewReader(f)\n\tvar out [8]byte\n\t_, _ = io.ReadFull(zr, out[:]) // same names, different shape: must stay visible\n}"},
	}},

	{name: "31: selector split after the dot (method)", desc: "selector split after the dot (method)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_splitmethod.go", content: "package mapping\n\nimport \"os\"\n\nfunc transferSplit(f *os.File, p []byte) (int, error) {\n\treturn f.\n\t\tRead(p)\n}"},
	}},

	{name: "32: selector split after the dot (package)", desc: "selector split after the dot (package)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_splitpkg.go", content: "package mapping\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc transferSplit(f *os.File) ([]byte, error) {\n\treturn io.\n\t\tReadAll(f)\n}"},
	}},

	{name: "33: exact tolerated c.r.Read(p) text with a file-backed r", desc: "exact tolerated c.r.Read(p) text with a file-backed r", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_cr_exact.go", content: "package mapping\n\nimport \"os\"\n\ntype reviewFileReader struct{ r *os.File }\n\nfunc (c *reviewFileReader) transfer(p []byte) (int, error) {\n\treturn c.r.Read(p)\n}"},
	}},

	{name: "34: exact tolerated io.ReadFull shape with zr *os.File", desc: "exact tolerated io.ReadFull shape with zr *os.File", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rf_exact.go", content: "package mapping\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype reviewMeta struct{ MetadataUncompressed uint64 }\n\nfunc transferExact(zr *os.File, out []byte, meta reviewMeta) (int, error) {\n\treturn io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])\n}"},
	}},

	{name: "35: compress/gzip.NewReader(file) + exact ReadFull shape", desc: "compress/gzip.NewReader(file) with the exact ReadFull shape", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_gzip.go", content: "package mapping\n\nimport (\n\t\"compress/gzip\"\n\t\"io\"\n\t\"os\"\n)\n\ntype reviewMeta struct{ MetadataUncompressed uint64 }\n\nfunc transferGzip(f *os.File, out []byte, meta reviewMeta) (int, error) {\n\tzr, err := gzip.NewReader(f)\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\tdefer zr.Close()\n\treturn io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])\n}"},
	}},

	{name: "36: log/slog writer over a file", desc: "log/slog.NewTextHandler over a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_slog.go", content: "package mapping\n\nimport (\n\t\"context\"\n\t\"log/slog\"\n\t\"os\"\n)\n\nfunc transferSlog(f *os.File) error {\n\th := slog.NewTextHandler(f, nil)\n\treturn h.Handle(context.Background(), slog.Record{})\n}"},
	}},

	{name: "37: runtime/trace writer over a file", desc: "runtime/trace.Start over a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_trace.go", content: "package mapping\n\nimport (\n\t\"os\"\n\t\"runtime/trace\"\n)\n\nfunc transferTrace(f *os.File) error {\n\treturn trace.Start(f)\n}"},
	}},

	{name: "38: os.StartProcess with the artifact file attached", desc: "os.StartProcess with the artifact file attached", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_startproc.go", content: "package mapping\n\nimport \"os\"\n\nfunc transferChild(path string, file *os.File) (*os.Process, error) {\n\treturn os.StartProcess(\"/bin/cat\", []string{\"cat\", path}, &os.ProcAttr{Files: []*os.File{file, file, file}})\n}"},
	}},

	{name: "39: gatemut_-named violation must be detected, not swept", desc: "gatemut_-named file carrying a transfer", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_hidden.go", content: "package mapping\n\nimport \"os\"\n\nfunc hidden(x *os.File) { var b [1]byte; _, _ = x.Read(b[:]) }"},
	}},

	{name: "40: aliased os import must not dodge the producer taint", desc: "aliased os import dodging the producer taint", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_osalias.go", content: "package mapping\n\nimport (\n\tfsp \"os\"\n\t\"path/filepath\"\n)\n\nfunc aliasProbe(path string) error {\n\tf, err := fsp.OpenFile(filepath.Clean(path), fsp.O_RDONLY, 0)\n\tif err != nil {\n\t\treturn err\n\t}\n\tdefer f.Close()\n\treturn f.Chdir() // unapproved file method: reachable only through the aliased producer taint\n}"},
	}},

	{name: "41: accessor-method *os.File return must keep the taint", desc: "accessor-method *os.File return", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_accessor.go", content: "package mapping\n\nimport \"os\"\n\ntype reviewHolder struct{ f *os.File }\n\nfunc (h *reviewHolder) file() *os.File { return h.f }\n\nfunc accessorProbe() error {\n\th := &reviewHolder{}\n\treturn h.file().Chdir() // unapproved file method: reachable only through the accessor taint\n}"},
	}},

	{name: "42: type-alias conversion of *os.File must keep the taint", desc: "type-alias conversion of *os.File", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_aliasconv.go", content: "package mapping\n\nimport \"os\"\n\ntype zrAlias = *os.File\n\nfunc aliasConv(f *os.File) error {\n\tzr := zrAlias(f)\n\treturn zr.Chdir() // alias conversion must not untaint the file\n}"},
	}},

	{name: "43: type-alias parameter of *os.File must be tainted", desc: "type-alias parameter of *os.File", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_aliasparam.go", content: "package mapping\n\nimport \"os\"\n\ntype zrAlias = *os.File\n\nfunc aliasParam(zr zrAlias) error {\n\treturn zr.Chdir() // aliased parameter type must still be file-tainted\n}"},
	}},

	{name: "44: file-carrying struct built before the call", desc: "os.StartProcess with a separately built ProcAttr", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_procattr.go", content: "package mapping\n\nimport \"os\"\n\nfunc leakStart() {\n\tf, _ := os.Open(\"/etc/hosts\")\n\tpa := &os.ProcAttr{Files: []*os.File{f}}\n\t_, _ = os.StartProcess(\"/bin/cat\", []string{\"cat\", \"/dev/null\"}, pa)\n}"},
	}},

	{name: "45: os.Pipe file pair must be tainted", desc: "os.Pipe producer taint", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_ospipe.go", content: "package mapping\n\nimport \"os\"\n\nfunc pipeProbe() error {\n\t_, w, err := os.Pipe()\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn w.Chdir() // pipe file: reachable only through the os.Pipe producer taint\n}"},
	}},

	{name: "47: struct-field stored file behind the inflater exemption", desc: "struct-field stored file shadowing the inflater exemption", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fieldbox.go", content: "package reader\n\nimport \"os\"\n\ntype gatemutBox struct{ r *os.File }\n\nvar gatemutBoxVal gatemutBox\n\nfunc init() { gatemutBoxVal.r, _, _ = os.Pipe() }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gatemutBoxVal.r"},
	}},

	{name: "48: channel-transported file behind the inflater exemption", desc: "channel-transported file shadowing the inflater exemption", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanbox.go", content: "package reader\n\nimport \"os\"\n\nvar gatemutCh = make(chan *os.File)\n\nfunc init() {\n\tr, _, _ := os.Pipe()\n\tgatemutCh <- r\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = <-gatemutCh"},
	}},

	{name: "50: inline FuncLit returning *os.File behind the exemption", desc: "inline FuncLit file behind the inflater exemption", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_funclit.go", content: "package reader"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }()"},
	}},

	{name: "51: type assertion to *os.File behind the exemption", desc: "type-assertion file behind the inflater exemption", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_assert.go", content: "package reader\n\nimport \"os\"\n\ntype zrBox struct{ r any }\n\nvar zb zrBox\n\nfunc init() {\n\tw, _, _ := os.Pipe()\n\tzb.r = w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = zb.r.(*os.File)"},
	}},

	{name: "52: two-hop channel transport behind the exemption", desc: "two-hop channel file behind the inflater exemption", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chan2.go", content: "package reader\n\nimport \"os\"\n\nvar outer = make(chan chan *os.File)\n\nfunc init() {\n\tinner := make(chan *os.File)\n\tw, _, _ := os.Pipe()\n\tinner <- w\n\touter <- inner\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "inner2 := <-outer; zr = <-inner2"},
	}},

	{name: "53: single-variable channel range must taint the element", desc: "single-variable channel range element", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanrange.go", content: "package reader\n\nimport \"os\"\n\nvar ch53 = make(chan *os.File)\n\nfunc init() {\n\tw, _, _ := os.Pipe()\n\tch53 <- w\n}\n\nfunc rangeProbe() error {\n\tfor z := range ch53 {\n\t\treturn z.Chdir() // unapproved method on a ranged channel element\n\t}\n\treturn nil\n}"},
	}},

	{name: "54: parenthesized producer call behind the exemption", desc: "parenthesized producer call behind the inflater exemption", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_parensel.go", content: "package reader\n\nimport \"os\"\n\nfunc getFile() *os.File { f, _ := os.Open(\"/dev/null\"); return f }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = (getFile)()"},
	}},

	{name: "55: parenthesized inline FuncLit behind the exemption", desc: "parenthesized FuncLit file behind the inflater exemption", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_parenlit.go", content: "package reader"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = (func() *os.File { f, _ := os.Open(\"/dev/null\"); return f })()"},
	}},

	{name: "56: interface-typed closure returning a file", desc: "interface-typed FuncLit file behind the inflater exemption", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ifacelit.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = func() io.ReadCloser { f, _ := os.Open(\"/dev/null\"); return f }()"},
	}},

	{name: "57: alias-typed function variable producing a file", desc: "alias-typed function variable producing a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasfn.go", content: "package reader\n\nimport \"os\"\n\ntype fileFn = func() *os.File\n\nvar getFat fileFn\n\nfunc init() {\n\tgetFat = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getFat()"},
	}},

	{name: "58: type-switch bound file behind the exemption", desc: "type-switch bound file behind the inflater exemption", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_typeswitch.go", content: "package reader\n\nimport \"os\"\n\nvar anyFile2 any\n\nfunc init() {\n\tw, _, _ := os.Pipe()\n\tanyFile2 = w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "switch zv := anyFile2.(type) { case *os.File: zr = zv }"},
	}},

	{name: "59: benign parenthesized call must pass (no false positive)", desc: "benign parenthesized call passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignpar.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw59 struct{ *bytes.Reader }\n\nfunc (w *rcw59) Close() error { return nil }\n\nfunc getCloser59() io.ReadCloser { return &rcw59{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = (getCloser59)()"},
	}},

	{name: "60: defined func type variable producing a file", desc: "defined func type variable producing a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_deffn.go", content: "package reader\n\nimport \"os\"\n\ntype fileFn3 func() *os.File\n\nvar getDef2 fileFn3\n\nfunc init() {\n\tgetDef2 = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getDef2()"},
	}},

	{name: "61: func-valued return through a same-package helper", desc: "func-valued return through a same-package helper", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_funcret.go", content: "package reader\n\nimport \"os\"\n\ntype fileFn5 func() *os.File\n\nfunc useDef2(g fileFn5) fileFn5 { return g }\n\nvar getDef3 fileFn5\n\nfunc init() {\n\tgetDef3 = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "g := useDef2(getDef3)\nh := g()\nzr = h"},
	}},

	{name: "62: type-switch bound defined-func-type case", desc: "type-switch bound defined-func-type case", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_tsfunc.go", content: "package reader\n\nimport \"os\"\n\ntype fileFn4 func() *os.File\n\nvar anyFn any\n\nfunc init() {\n\tanyFn = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "switch v := anyFn.(type) { case fileFn4: zr = v() }"},
	}},

	{name: "63: benign defined func type returning a reader must pass", desc: "benign defined func type passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignfn.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw63 struct{ *bytes.Reader }\n\nfunc (w *rcw63) Close() error { return nil }\n\ntype brFn func() io.ReadCloser\n\nvar getBR brFn\n\nfunc init() { getBR = func() io.ReadCloser { return &rcw63{bytes.NewReader(nil)} } }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getBR()"},
	}},

	{name: "64: method returning a defined func type (single hop)", desc: "method returning a defined func type", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_methfn.go", content: "package reader\n\nimport \"os\"\n\ntype fileFn6 func() *os.File\n\ntype zbox struct{}\n\nvar zb zbox\n\nfunc (z *zbox) mk() fileFn6 { return func() *os.File { f, _ := os.Open(\"/dev/null\"); return f } }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "rf := zb.mk()\nzr = rf()"},
	}},

	{name: "65: method func-valued double call", desc: "method func-valued double call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_methdbl.go", content: "package reader\n\nimport \"os\"\n\ntype fileFn7 func() *os.File\n\ntype ybox struct{}\n\nvar yb ybox\n\nfunc (y *ybox) mk2() fileFn7 { return func() *os.File { f, _ := os.Open(\"/dev/null\"); return f } }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = yb.mk2()()"},
	}},

	{name: "66: same-package helper func-valued double call", desc: "same-package helper func-valued double call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_funcdbl.go", content: "package reader\n\nimport \"os\"\n\ntype fileFn8 func() *os.File\n\nfunc useDef4(g fileFn8) fileFn8 { return g }\n\nvar getDef4 fileFn8\n\nfunc init() {\n\tgetDef4 = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = useDef4(getDef4)()"},
	}},

	{name: "67: benign method func-valued double call must pass", desc: "benign method double call passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignmeth.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw67 struct{ *bytes.Reader }\n\nfunc (w *rcw67) Close() error { return nil }\n\ntype qbox struct{}\n\nvar qb qbox\n\nfunc (q *qbox) mk3() func() io.ReadCloser { return func() io.ReadCloser { return &rcw67{bytes.NewReader(nil)} } }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = qb.mk3()()"},
	}},

	{name: "68: func value stored in a struct field", desc: "func value stored in a struct field", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fnfield.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnA func() *os.File\n\ntype fnBox struct{ fn fileFnA }\n\nvar hb fnBox\n\nfunc init() {\n\thb.fn = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = hb.fn()"},
	}},

	{name: "69: chan of func() *os.File", desc: "chan of func type receive and call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanfunc.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnB func() *os.File\n\nvar fnCh = make(chan fileFnB)\n\nfunc init() {\n\tfnCh <- func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "got := <-fnCh\nzr = got()"},
	}},

	{name: "70: any-erased func return asserted and called", desc: "any-erased func return asserted and called", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_anyfunc.go", content: "package reader\n\nimport \"os\"\n\nfunc getFn() any { return func() *os.File { f, _ := os.Open(\"/dev/null\"); return f } }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = (getFn().(func() *os.File))()"},
	}},

	{name: "71: os.Stdout through an interface closure", desc: "os.Stdout through an interface closure", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_stdout.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc useStdout3() {\n\t_, _ = io.ReadAll(func() io.ReadCloser { return os.Stdout }())\n}"},
	}},

	{name: "72: benign chan of func() int must pass", desc: "benign chan of func() int passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignchanfn.go", content: "package reader\n\ntype intFn3 func() int\n\nvar intCh = make(chan intFn3)\n\nfunc init() { intCh <- func() int { return 3 } }\n\nfunc useChanInt() int {\n\tgot := <-intCh\n\treturn got()\n}"},
	}},

	{name: "73: nested struct-field func value", desc: "nested struct-field func value", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_nestedfn.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnC func() *os.File\n\ntype nestI struct{ fn fileFnC }\n\ntype nestH struct{ inner nestI }\n\nvar nh nestH\n\nfunc init() {\n\tnh.inner.fn = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = nh.inner.fn()"},
	}},

	{name: "74: named interface-typed helper returning a tainted file", desc: "named interface-typed helper returning a tainted file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedfn.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc getNamed() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getNamed()"},
	}},

	{name: "75: named helper returning os.Stdout", desc: "named helper returning os.Stdout", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedstd.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc getStd() io.ReadCloser { return os.Stdout }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getStd()"},
	}},

	{name: "76: chan of func through a same-package helper", desc: "chan of func through a same-package helper", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanpass.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnD func() *os.File\n\nvar fnCh3 = make(chan fileFnD)\n\nfunc init() {\n\tfnCh3 <- func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}\n\nfunc passFn(ch chan fileFnD) chan fileFnD { return ch }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "got := <-passFn(fnCh3)\nzr = got()"},
	}},

	{name: "77: benign named interface-typed helper must pass", desc: "benign named interface-typed helper passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignnamed.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype brCloser struct{ *bytes.Reader }\n\nfunc (b *brCloser) Close() error { return nil }\n\nfunc getBR4() io.ReadCloser { return &brCloser{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getBR4()"},
	}},

	{name: "78: named method returning a tainted file", desc: "named method returning a tainted file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedmeth.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype mbox struct{}\n\nvar mb mbox\n\nfunc (m *mbox) named() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mb.named()"},
	}},

	{name: "79: named method returning os.Stdout", desc: "named method returning os.Stdout", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_namedmethstd.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype mbox2 struct{}\n\nvar mb2 mbox2\n\nfunc (m *mbox2) namedstd() io.ReadCloser { return os.Stdout }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mb2.namedstd()"},
	}},

	{name: "80: deep method chain returning a tainted file", desc: "deep method chain returning a tainted file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_deepmeth.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype hbox struct{}\n\nvar hb2 hbox\n\nfunc (h *hbox) deep() io.ReadCloser {\n\treturn h.mid()\n}\n\nfunc (h *hbox) mid() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = hb2.deep()"},
	}},

	{name: "81: benign named method must pass", desc: "benign named method passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignmeth2.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype brCloser struct{ *bytes.Reader }\n\nfunc (w *brCloser) Close() error { return nil }\n\ntype rbox struct{}\n\nvar rb rbox\n\nfunc (r *rbox) named() io.ReadCloser { return &brCloser{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = rb.named()"},
	}},

	{name: "82: nested method receiver returning a tainted file", desc: "nested method receiver returning a tainted file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_nestedmethrecv.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnF func() *os.File\n\ntype minner struct{}\n\nvar mh minner\n\ntype mholder struct{ inner minner }\n\nvar mhv mholder\n\nfunc (m *minner) mk() fileFnF { return func() *os.File { f, _ := os.Open(\"/dev/null\"); return f } }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mhv.inner.mk()()"},
	}},

	{name: "83: benign nested method must pass", desc: "benign nested method passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignnestedmeth.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype brCloser struct{ *bytes.Reader }\n\nfunc (w *brCloser) Close() error { return nil }\n\ntype inbox2 struct{}\n\nvar ib2 inbox2\n\ntype iholder struct{ inner inbox2 }\n\nvar ihv iholder\n\nfunc (i *inbox2) named() io.ReadCloser { return &brCloser{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = ihv.inner.named()"},
	}},

	{name: "84: method value bound to a variable, called once", desc: "method value bound to a variable", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_methodval.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gm84 struct{}\n\nvar gm84v gm84\n\nfunc (g *gm84) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fn84()"},
	}},

	{name: "85: helper returning a method value through an interface", desc: "helper returning a method value", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_funcretmethval.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gm85 struct{}\n\nvar gm85v gm85\n\nfunc (g *gm85) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nfunc getFn85() func() io.ReadCloser {\n\treturn gm85v.get\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getFn85()()"},
	}},

	{name: "86: nested-receiver method value, double call", desc: "nested-receiver method value double call", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_nestedmethval.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnI func() *os.File\n\ntype minner86 struct{}\n\ntype mholder86 struct{ inner minner86 }\n\nvar mhv86 mholder86\n\nfunc (m *minner86) mk() fileFnI { return func() *os.File { f, _ := os.Open(\"/dev/null\"); return f } }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fn86()()"},
	}},

	{name: "87: method value through a package-level channel", desc: "method value through a package channel", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanmethval.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gm87 struct{}\n\nvar gm87v gm87\n\nfunc (g *gm87) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar ch87 = make(chan func() io.ReadCloser)\n\nfunc init() {\n\tch87 <- gm87v.get\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = (<-ch87)()"},
	}},

	{name: "88: generic pass-through of a file", desc: "generic pass-through of a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genericfile.go", content: "package reader\n\nimport \"os\"\n\nfunc idf88[T any](f T) T { return f }\n\nvar osFile88 *os.File"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = idf88(osFile88)"},
	}},

	{name: "89: generic pass-through of a func-file, double call", desc: "generic pass-through of a func-file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genericfunc.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnJ func() *os.File\n\nfunc idg89[T any](f T) T { return f }\n\nvar getDef89 fileFnJ\n\nfunc init() {\n\tgetDef89 = func() *os.File { f, _ := os.Open(\"/dev/null\"); return f }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = idg89(getDef89)()"},
	}},

	{name: "90: benign method value must pass", desc: "benign method value passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignmethval.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype mcw90 struct{ *bytes.Reader }\n\nfunc (w *mcw90) Close() error { return nil }\n\ntype gm90 struct{}\n\nvar gm90v gm90\n\nfunc (g *gm90) get() io.ReadCloser { return &mcw90{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "_ = gm90v.get"},
	}},

	{name: "91: benign generic pass-through must pass", desc: "benign generic pass-through passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benigngeneric.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype mcw91 struct{ *bytes.Reader }\n\nfunc (w *mcw91) Close() error { return nil }\n\nfunc idh91[T any](f T) T { return f }\n\nvar rc91 io.ReadCloser = &mcw91{bytes.NewReader(nil)}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = idh91(rc91)"},
	}},

	{name: "92: generic container element binding a file", desc: "generic container element binding a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genericcont.go", content: "package reader\n\nimport \"os\"\n\nfunc get92[T any](xs []T) T { return xs[0] }\n\nvar osFiles92 = []*os.File{os.Stdin}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = get92(osFiles92)"},
	}},

	{name: "93: method value returning a chan of func-file", desc: "method value returning a chan of func-file", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanmethval.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnM func() *os.File\n\ntype chH93 struct{}\n\nvar chh93 chH93\n\nfunc (h chH93) ch() chan fileFnM { return nil }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "fn93 := chh93.ch"},
	}},

	{name: "94: func-typed struct field assigned a file closure", desc: "func-typed field assigned a file closure", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fnfieldassign.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype fnBox94 struct{ fn func() io.ReadCloser }\n\nvar fb94 fnBox94\n\nfunc init() {\n\tfb94.fn = func() io.ReadCloser {\n\t\tw, _, _ := os.Pipe()\n\t\treturn w\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fb94.fn()"},
	}},

	{name: "95: chan-typed struct field assigned a chan of func-file", desc: "chan-typed field assigned a chan of func-file", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanfieldassign.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnN func() *os.File\n\ntype chBox95 struct{ ch chan fileFnN }\n\nvar cb95 chBox95\n\nfunc init() {\n\tcb95.ch = make(chan fileFnN)\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "got95 := <-cb95.ch"},
	}},

	{name: "96: benign generic container must pass", desc: "benign generic container passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benigengenericcont.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw96 struct{ *bytes.Reader }\n\nfunc (w *rcw96) Close() error { return nil }\n\nfunc getB96[T any](xs []T) T { return xs[0] }\n\nvar rcList96 = []io.ReadCloser{&rcw96{bytes.NewReader(nil)}}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = getB96(rcList96)"},
	}},

	{name: "97: benign func-typed field must pass", desc: "benign func-typed field passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignfnfield.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw97 struct{ *bytes.Reader }\n\nfunc (w *rcw97) Close() error { return nil }\n\ntype fnBox97 struct{ fn func() io.ReadCloser }\n\nvar fb97 fnBox97\n\nfunc init() {\n\tfb97.fn = func() io.ReadCloser { return &rcw97{bytes.NewReader(nil)} }\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fb97.fn()"},
	}},

	{name: "98: range over a struct-field chan of func-file", desc: "range over a field chan of func-file", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_rangefieldchan.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnP func() *os.File\n\ntype chBoxR98 struct{ ch chan fileFnP }\n\nvar cbr98 chBoxR98\n\nfunc init() {\n\tcbr98.ch = make(chan fileFnP)\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "for got98 := range cbr98.ch {"},
	}},

	{name: "99: receive from a struct-field chan of files", desc: "receive from a field chan of files", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_recvfieldchan.go", content: "package reader\n\nimport \"os\"\n\ntype chFileR99 struct{ ch chan *os.File }\n\nvar cfr99 chFileR99\n\nfunc init() {\n\tcfr99.ch = make(chan *os.File)\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = <-cfr99.ch"},
	}},

	{name: "100: method value sent into a field chan from another function", desc: "method value sent into a field chan", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_sendfieldchan.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gm100 struct{}\n\nvar gs100 gm100\n\nfunc (g *gm100) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\ntype chBox100 struct{ ch chan func() io.ReadCloser }\n\nvar cbs100 chBox100\n\nfunc init() {\n\tcbs100.ch = make(chan func() io.ReadCloser)\n}\n\nfunc fill100() {\n\tcbs100.ch <- gs100.get\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "got100 := <-cbs100.ch"},
	}},

	{name: "101: benign range over a field chan must pass", desc: "benign range over a field chan passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignrangefieldchan.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw101 struct{ *bytes.Reader }\n\nfunc (w *rcw101) Close() error { return nil }\n\ntype chBoxB101 struct{ ch chan func() io.ReadCloser }\n\nvar cbb101 chBoxB101\n\nfunc init() {\n\tcbb101.ch = make(chan func() io.ReadCloser)\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "for got101 := range cbb101.ch {\n\tzr = got101()\n}"},
	}},

	{name: "102: benign receive from a field chan must pass", desc: "benign receive from a field chan passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignrecvfieldchan.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw102 struct{ *bytes.Reader }\n\nfunc (w *rcw102) Close() error { return nil }\n\ntype chBoxC102 struct{ ch chan io.ReadCloser }\n\nvar cbc102 chBoxC102\n\nfunc init() {\n\tcbc102.ch = make(chan io.ReadCloser)\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = <-cbc102.ch"},
	}},

	{name: "103: map field element holding a file-producing closure", desc: "map field element holding a file closure", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_mapfield.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype fnMap103 struct{ m map[string]func() io.ReadCloser }\n\nvar fm103 fnMap103\n\nfunc init() {\n\tfm103.m = map[string]func() io.ReadCloser{\n\t\t\"k\": func() io.ReadCloser {\n\t\t\tw, _, _ := os.Pipe()\n\t\t\treturn w\n\t\t},\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fm103.m[\"k\"]()"},
	}},

	{name: "104: slice field element holding a file-producing closure", desc: "slice field element holding a file closure", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_slicefield.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype fnSlice104 struct{ s []func() io.ReadCloser }\n\nvar fs104 fnSlice104\n\nfunc init() {\n\tfs104.s = []func() io.ReadCloser{\n\t\tfunc() io.ReadCloser {\n\t\t\tw, _, _ := os.Pipe()\n\t\t\treturn w\n\t\t},\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fs104.s[0]()"},
	}},

	{name: "105: method value stored in a map field", desc: "method value stored in a map field", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_mapmethodval.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gm105 struct{}\n\nvar gm105v gm105\n\nfunc (g *gm105) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\ntype fnMap105 struct{ m map[string]func() io.ReadCloser }\n\nvar fg105 fnMap105\n\nfunc init() {\n\tfg105.m = map[string]func() io.ReadCloser{}\n\tfg105.m[\"k\"] = gm105v.get\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fg105.m[\"k\"]()"},
	}},

	{name: "106: declared map element shape of a defined func-file type", desc: "declared map element of a func-file type", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_declaredmap.go", content: "package reader\n\nimport \"os\"\n\ntype fileFnR func() *os.File\n\ntype fnMap106 struct{ m map[string]fileFnR }\n\nvar fm106 fnMap106\n\nfunc init() {\n\tfm106.m = map[string]fileFnR{}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fm106.m[\"k\"]()"},
	}},

	{name: "107: benign map field must pass", desc: "benign map field passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignmapfield.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw107 struct{ *bytes.Reader }\n\nfunc (w *rcw107) Close() error { return nil }\n\ntype fnMapB107 struct{ m map[string]func() io.ReadCloser }\n\nvar fmb107 fnMapB107\n\nfunc init() {\n\tfmb107.m = map[string]func() io.ReadCloser{\n\t\t\"k\": func() io.ReadCloser { return &rcw107{bytes.NewReader(nil)} },\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fmb107.m[\"k\"]()"},
	}},

	{name: "108: anonymous-receiver method returning a file", desc: "anonymous-receiver method returning a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_anonrecv.go", content: "package reader\n\nimport \"os\"\n\ntype an108 struct{}\n\nfunc (an108) getf() *os.File {\n\tf, _ := os.Open(\"/dev/null\")\n\treturn f\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = an108{}.getf()"},
	}},

	{name: "109: anonymous-receiver method returning an interface file", desc: "anonymous-receiver method returning an interface-hidden file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_anonrecviface.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype an109 struct{}\n\nfunc (an109) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = an109{}.get()"},
	}},

	{name: "110: anonymous pointer-receiver method returning a file", desc: "anonymous pointer-receiver method returning a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_anonptrrecv.go", content: "package reader\n\nimport \"os\"\n\ntype an110 struct{}\n\nfunc (*an110) getf() *os.File {\n\tf, _ := os.Open(\"/dev/null\")\n\treturn f\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = (&an110{}).getf()"},
	}},

	{name: "111: anonymous-receiver method value stored in a map field", desc: "anonymous-receiver method value stored in a map field", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_anonrecvmap.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype an111 struct{}\n\nfunc (an111) getf() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\ntype fnMap111 struct{ m map[string]func() io.ReadCloser }\n\nvar fm111 fnMap111\n\nfunc init() {\n\tfm111.m = map[string]func() io.ReadCloser{}\n\tfm111.m[\"k\"] = an111{}.getf\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fm111.m[\"k\"]()"},
	}},

	{name: "112: benign anonymous-receiver method must pass", desc: "benign anonymous-receiver method passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignanonrecv.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw112 struct{ *bytes.Reader }\n\nfunc (w *rcw112) Close() error { return nil }\n\ntype an112 struct{}\n\nfunc (an112) get() io.ReadCloser { return &rcw112{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = an112{}.get()"},
	}},

	{name: "113: alias receiver with an interface-hidden result", desc: "alias-receiver method with an interface-hidden file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasrecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsT struct{}\n\ntype rT = gsT\n\nfunc (rT) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar al rT"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = al.get()"},
	}},

	{name: "114: alias-named composite literal receiver returning a file", desc: "alias-named composite literal receiver returning a file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliaslit.go", content: "package reader\n\nimport \"os\"\n\ntype gsF struct{}\n\ntype rF = gsF\n\nfunc (rF) getf() *os.File {\n\tf, _ := os.Open(\"/dev/null\")\n\treturn f\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = rF{}.getf()"},
	}},

	{name: "115: benign alias receiver must pass", desc: "benign alias receiver passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benignalias.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcb115 struct{ *bytes.Reader }\n\nfunc (w *rcb115) Close() error { return nil }\n\ntype gsBn115 struct{}\n\ntype rBn115 = gsBn115\n\nfunc (rBn115) get() io.ReadCloser { return &rcb115{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = rBn115{}.get()"},
	}},

	{name: "116: defined-type receiver with an interface-hidden result", desc: "defined-type receiver with an interface-hidden file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_defrecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsV116 struct{}\n\ntype bV116 gsV116\n\nfunc (bV116) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar bv116 bV116"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = bv116.get()"},
	}},

	{name: "117: generic-instantiated struct variable method call", desc: "generic-instantiated struct variable method call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_geninst.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsG117[T any] struct{}\n\nfunc (gsG117[T]) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar gv117 gsG117[int]"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gv117.get()"},
	}},

	{name: "118: embedded-field method promotion", desc: "embedded-field method promotion with an interface-hidden file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_embedmeth.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsE118 struct{}\n\nfunc (gsE118) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\ntype hE118 struct{ gsE118 }\n\nvar hve118 hE118"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = hve118.get()"},
	}},

	{name: "119: pointer-alias receiver", desc: "pointer-alias receiver with an interface-hidden file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ptralias.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsP119 struct{}\n\ntype p119 = *gsP119\n\nfunc (p119) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar pv119 p119 = &gsP119{}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = pv119.get()"},
	}},

	{name: "120: benign embedded-promotion method must pass", desc: "benign embedded-promotion method passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benemb.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcE120 struct{ *bytes.Reader }\n\nfunc (w *rcE120) Close() error { return nil }\n\ntype gsBE120 struct{}\n\nfunc (gsBE120) get() io.ReadCloser { return &rcE120{bytes.NewReader(nil)} }\n\ntype hBE120 struct{ gsBE120 }\n\nvar hvbe120 hBE120"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = hvbe120.get()"},
	}},

	{name: "121: pointer to a defined type without an initializer", desc: "pointer to a defined type without an initializer", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ptrdef.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsN121 struct{}\n\ntype dN121 gsN121\n\nfunc (dN121) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar pn121 *dN121"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = pn121.get()"},
	}},

	{name: "122: benign pointer to a defined type must pass", desc: "benign pointer-to-defined-type method passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benptrdef.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcN122 struct{ *bytes.Reader }\n\nfunc (w *rcN122) Close() error { return nil }\n\ntype gsBN122 struct{}\n\ntype dBN122 gsBN122\n\nfunc (dBN122) get() io.ReadCloser { return &rcN122{bytes.NewReader(nil)} }\n\nvar pbn122 *dBN122"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = pbn122.get()"},
	}},

	{name: "123: new() of a defined type as the receiver", desc: "new() of a defined type as the receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_newdef.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsM123 struct{}\n\ntype dM123 gsM123\n\nfunc (dM123) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = new(dM123).get()"},
	}},

	{name: "124: array-index receiver", desc: "array-index receiver with an interface-hidden file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_arrrecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsH124 struct{}\n\nfunc (gsH124) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar arr124 [3]*gsH124"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = arr124[1].get()"},
	}},

	{name: "125: map-index receiver", desc: "map-index receiver with an interface-hidden file", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_maprecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsH125 struct{}\n\nfunc (gsH125) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar mm125 map[string]*gsH125"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mm125[\"k\"].get()"},
	}},

	{name: "126: benign array-index receiver must pass", desc: "benign array-index receiver passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benarrrecv.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcbH126 struct{ *bytes.Reader }\n\nfunc (w *rcbH126) Close() error { return nil }\n\ntype gsHB126 struct{}\n\nfunc (gsHB126) get() io.ReadCloser { return &rcbH126{bytes.NewReader(nil)} }\n\nvar arrB126 [3]*gsHB126"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = arrB126[1].get()"},
	}},

	{name: "127: struct-field container index receiver", desc: "struct-field container index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fieldidx.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsF127 struct{}\n\nfunc (gsF127) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\ntype sf127 struct{ arr []*gsF127 }\n\nvar s127 sf127\n\nfunc fldGet127() io.ReadCloser { return s127.arr[1].get() }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = fldGet127()"},
	}},

	{name: "128: call-result index receiver", desc: "call-result index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_callidx.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsC128 struct{}\n\nfunc (gsC128) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nfunc arrSrc128() []*gsC128 { return nil }\n\nfunc clGet128() io.ReadCloser { return arrSrc128()[0].get() }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = clGet128()"},
	}},

	{name: "129: dereferenced pointer-to-container index receiver", desc: "dereferenced pointer-to-container index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_derefidx.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsDP129 struct{}\n\nfunc (gsDP129) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar pdp129 *[]*gsDP129\n\nfunc drGet129() io.ReadCloser { return (*pdp129)[0].get() }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = drGet129()"},
	}},

	{name: "130: make() short-declared map receiver", desc: "make() short-declared map receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_makerecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsMK130 struct{}\n\nfunc (gsMK130) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nfunc mkGet130() io.ReadCloser {\n\tmmk := make(map[string]*gsMK130)\n\treturn mmk[\"k\"].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mkGet130()"},
	}},

	{name: "131: range-variable element receiver", desc: "range-variable element receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_rangerecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsR131 struct{}\n\nfunc (gsR131) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar arrr131 [2]*gsR131\n\nfunc rngGet131() io.ReadCloser {\n\tfor _, v := range arrr131 {\n\t\treturn v.get()\n\t}\n\treturn nil\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = rngGet131()"},
	}},

	{name: "132: chan-receive element receiver", desc: "chan-receive element receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanrecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsCH132 struct{}\n\nfunc (gsCH132) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar chch132 chan *gsCH132\n\nfunc chGet132() io.ReadCloser { return (<-chch132).get() }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = chGet132()"},
	}},

	{name: "133: benign make() map receiver must pass", desc: "benign make() map receiver passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benmakerecv.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcbM133 struct{ *bytes.Reader }\n\nfunc (w *rcbM133) Close() error { return nil }\n\ntype gsBM133 struct{}\n\nfunc (gsBM133) get() io.ReadCloser { return &rcbM133{bytes.NewReader(nil)} }\n\nfunc mkGetB133() io.ReadCloser {\n\tmb := make(map[string]*gsBM133)\n\treturn mb[\"k\"].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mkGetB133()"},
	}},

	{name: "134: range-variable container index receiver", desc: "range-variable container index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_rangeidx.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsRG134 struct{}\n\nfunc (gsRG134) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar arrx134 [][]*gsRG134\n\nfunc rgGet134() io.ReadCloser {\n\tfor _, sl := range arrx134 {\n\t\treturn sl[0].get()\n\t}\n\treturn nil\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = rgGet134()"},
	}},

	{name: "135: composite-literal indexed receiver", desc: "composite-literal indexed receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_litidx.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsMK135 struct{}\n\nfunc (gsMK135) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nfunc litGet135() io.ReadCloser { return map[string]*gsMK135{\"a\": {}}[\"a\"].get() }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = litGet135()"},
	}},

	{name: "136: benign composite-literal indexed receiver must pass", desc: "benign composite-literal indexed receiver passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benlitidx.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcq136 struct{ *bytes.Reader }\n\nfunc (w *rcq136) Close() error { return nil }\n\ntype gsBQ136 struct{}\n\nfunc (gsBQ136) get() io.ReadCloser { return &rcq136{bytes.NewReader(nil)} }\n\nfunc litGetB136() io.ReadCloser { return map[string]*gsBQ136{\"a\": {}}[\"a\"].get() }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = litGetB136()"},
	}},

	{name: "137: type-switch bound struct receiver", desc: "type-switch bound struct receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_typeswitch.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsTS137 struct{}\n\nfunc (gsTS137) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar iv137 interface{} = &gsTS137{}\n\nfunc tsGet137() io.ReadCloser {\n\tswitch v := iv137.(type) {\n\tcase *gsTS137:\n\t\treturn v.get()\n\t}\n\treturn nil\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = tsGet137()"},
	}},

	{name: "138: multi-assign call result index receiver", desc: "multi-assign call result index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_multiasgn.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsMA138 struct{}\n\nfunc (gsMA138) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nfunc mk2_138() ([]*gsMA138, error) { return []*gsMA138{}, nil }\n\nfunc maGet138() io.ReadCloser {\n\ta, _ := mk2_138()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = maGet138()"},
	}},

	{name: "139: benign type-switch bound receiver must pass", desc: "benign type-switch bound receiver passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bents.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv139 struct{ *bytes.Reader }\n\nfunc (w *rcv139) Close() error { return nil }\n\ntype gsBT139 struct{}\n\nfunc (gsBT139) get() io.ReadCloser { return &rcv139{bytes.NewReader(nil)} }\n\nvar ivB139 interface{} = &gsBT139{}\n\nfunc tsGetB139() io.ReadCloser {\n\tswitch v := ivB139.(type) {\n\tcase *gsBT139:\n\t\treturn v.get()\n\t}\n\treturn nil\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = tsGetB139()"},
	}},

	{name: "140: single-LHS call-result index receiver", desc: "single-LHS call-result index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_singlecall.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsSC140 struct{}\n\nfunc (gsSC140) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nfunc mkArr140() []*gsSC140 { return nil }\n\nfunc sgGet140() io.ReadCloser {\n\ta := mkArr140()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = sgGet140()"},
	}},

	{name: "141: single-LHS method-call result index receiver", desc: "single-LHS method-call result index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_singlemeth.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsSC141 struct{}\n\nfunc (gsSC141) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\ntype mkBox141 struct{}\n\nfunc (mkBox141) mkArr() []*gsSC141 { return nil }\n\nvar mkbox141 mkBox141\n\nfunc sgGet141() io.ReadCloser {\n\ta := mkbox141.mkArr()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = sgGet141()"},
	}},

	{name: "142: type-switch default clause bound receiver", desc: "type-switch default clause bound receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_tsdefault.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype ifD142 interface{ get() io.ReadCloser }\n\ntype gsTS142 struct{}\n\nfunc (gsTS142) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar iv142 ifD142 = &gsTS142{}\n\nfunc tsGet142() io.ReadCloser {\n\tswitch v := iv142.(type) {\n\tdefault:\n\t\treturn v.get()\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = tsGet142()"},
	}},

	{name: "143: multi-assign non-call RHS index receiver", desc: "multi-assign non-call RHS index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_multimap.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsMM143 struct{}\n\nfunc (gsMM143) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nvar mm143 map[string][]*gsMM143\n\nfunc mgGet143() io.ReadCloser {\n\ta, _ := mm143[\"k\"], 0\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mgGet143()"},
	}},

	{name: "144: benign single-LHS call-result index must pass", desc: "benign single-LHS call-result index passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benscall.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv144 struct{ *bytes.Reader }\n\nfunc (w *rcv144) Close() error { return nil }\n\ntype gsSCB144 struct{}\n\nfunc (gsSCB144) get() io.ReadCloser { return &rcv144{bytes.NewReader(nil)} }\n\nfunc mkArrB144() []*gsSCB144 { return nil }\n\nfunc sgGetB144() io.ReadCloser {\n\ta := mkArrB144()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = sgGetB144()"},
	}},

	{name: "145: generic explicit-instantiation index receiver", desc: "generic explicit-instantiation index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_gencall.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsG145 struct{ x int }\n\nfunc (g *gsG145) get() io.ReadCloser {\n\tr, _, _ := os.Pipe()\n\treturn r\n}\n\nfunc mkGen145[T any]() []T { return nil }\n\nfunc sgGen145() io.ReadCloser {\n\ta := mkGen145[*gsG145]()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = sgGen145()"},
	}},

	{name: "146: type-switch default with interface-returning call", desc: "type-switch default interface-returning call", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_tsifc.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype ifD146 interface{ get() io.ReadCloser }\n\ntype gsD146 struct{}\n\nfunc (gsD146) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\nfunc mkIf146() ifD146 { return &gsD146{} }\n\nfunc tsD146() io.ReadCloser {\n\tswitch v := mkIf146().(type) {\n\tdefault:\n\t\treturn v.get()\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = tsD146()"},
	}},

	{name: "147: type-switch default with interface-typed field", desc: "type-switch default interface-typed field", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_tsifcf.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype ifD147 interface{ get() io.ReadCloser }\n\ntype gsD147 struct{}\n\nfunc (gsD147) get() io.ReadCloser {\n\tw, _, _ := os.Pipe()\n\treturn w\n}\n\ntype sdH147 struct{ f ifD147 }\n\nfunc mkSd147() sdH147 { return sdH147{f: &gsD147{}} }\n\nfunc tsF147() io.ReadCloser {\n\tsd := mkSd147()\n\tswitch v := sd.f.(type) {\n\tdefault:\n\t\treturn v.get()\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = tsF147()"},
	}},

	{name: "148: multi-assign chan-receive index receiver", desc: "multi-assign chan-receive index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanrecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsCH148 struct{ x int }\n\nfunc (g *gsCH148) get() io.ReadCloser {\n\tr, _, _ := os.Pipe()\n\treturn r\n}\n\nvar chS148 chan []*gsCH148\n\nfunc mgCH148() io.ReadCloser {\n\ta, _ := (<-chS148), 0\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = mgCH148()"},
	}},

	{name: "149: benign generic explicit-instantiation index must pass", desc: "benign generic explicit-instantiation index passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bengen.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv149 struct{ *bytes.Reader }\n\nfunc (w *rcv149) Close() error { return nil }\n\ntype gsGB149 struct{}\n\nfunc (gsGB149) get() io.ReadCloser { return &rcv149{bytes.NewReader(nil)} }\n\nfunc mkGenB149[T any]() []T { return nil }\n\nfunc sgGenB149() io.ReadCloser {\n\ta := mkGenB149[*gsGB149]()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = sgGenB149()"},
	}},

	{name: "150: benign type-switch default interface call must pass", desc: "benign type-switch default interface passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bentsifc.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv150 struct{ *bytes.Reader }\n\nfunc (w *rcv150) Close() error { return nil }\n\ntype ifDB150 interface{ get() io.ReadCloser }\n\ntype gsDB150 struct{}\n\nfunc (gsDB150) get() io.ReadCloser { return &rcv150{bytes.NewReader(nil)} }\n\nfunc mkIfB150() ifDB150 { return &gsDB150{} }\n\nfunc tsDB150() io.ReadCloser {\n\tswitch v := mkIfB150().(type) {\n\tdefault:\n\t\treturn v.get()\n\t}\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = tsDB150()"},
	}},

	{name: "151: generic-receiver container index receiver", desc: "generic-receiver container index receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genrecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsG151 struct{ x int }\n\nfunc (g *gsG151) get() io.ReadCloser {\n\tr, _, _ := os.Pipe()\n\treturn r\n}\n\ntype gR151[T any] struct{}\n\nfunc (r gR151[T]) mk() []T { return nil }\n\nfunc gb151() io.ReadCloser {\n\trr := gR151[*gsG151]{}\n\ta := rr.mk()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb151()"},
	}},

	{name: "152: generic-receiver direct file result", desc: "generic-receiver direct file result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genrecvf.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gR152[T any] struct{}\n\nfunc (r *gR152[T]) mk() T { var z T; return z }\n\nfunc gb152() io.ReadCloser {\n\trr := &gR152[*os.File]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb152()"},
	}},

	{name: "153: embedded generic-receiver promotion", desc: "embedded generic-receiver promotion", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genpromo.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsG153 struct{ x int }\n\nfunc (g *gsG153) get() io.ReadCloser {\n\tr, _, _ := os.Pipe()\n\treturn r\n}\n\ntype gR153[T any] struct{}\n\nfunc (r gR153[T]) mk() []T { return nil }\n\ntype hE153 struct{ gR153[*gsG153] }\n\nfunc gb153() io.ReadCloser {\n\the := hE153{}\n\ta := he.mk()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb153()"},
	}},

	{name: "154: explicit-instantiation direct file flow", desc: "explicit-instantiation direct file flow", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genexpf.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc mkT154[T any]() T { var zero T; return zero }\n\nfunc gb154() io.ReadCloser {\n\treturn mkT154[*os.File]()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb154()"},
	}},

	{name: "155: explicit-instantiation struct receiver", desc: "explicit-instantiation struct receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genexps.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsG155 struct{ x int }\n\nfunc (g *gsG155) get() io.ReadCloser {\n\tr, _, _ := os.Pipe()\n\treturn r\n}\n\nfunc mkT155[T any]() T { var zero T; return zero }\n\nfunc gb155() io.ReadCloser {\n\treturn mkT155[*gsG155]().get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb155()"},
	}},

	{name: "156: arg-inferred struct-bound result", desc: "arg-inferred struct-bound result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_geninf.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gsG156 struct{ x int }\n\nfunc (g *gsG156) get() io.ReadCloser {\n\tr, _, _ := os.Pipe()\n\treturn r\n}\n\nfunc mkT156[T any](x T) T { return x }\n\nfunc gb156() io.ReadCloser {\n\treturn mkT156(&gsG156{}).get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb156()"},
	}},

	{name: "157: benign generic-receiver container must pass", desc: "benign generic-receiver container passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bengenrecv.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv157 struct{ *bytes.Reader }\n\nfunc (w *rcv157) Close() error { return nil }\n\ntype gsGB157 struct{}\n\nfunc (gsGB157) get() io.ReadCloser { return &rcv157{bytes.NewReader(nil)} }\n\ntype gRB157[T any] struct{}\n\nfunc (r gRB157[T]) mk() []T { return nil }\n\nfunc gbb157() io.ReadCloser {\n\trr := gRB157[*gsGB157]{}\n\ta := rr.mk()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb157()"},
	}},

	{name: "158: benign inferred struct-bound result must pass", desc: "benign inferred struct-bound result passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bengeninf.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv158 struct{ *bytes.Reader }\n\nfunc (w *rcv158) Close() error { return nil }\n\ntype gsGB158 struct{}\n\nfunc (gsGB158) get() io.ReadCloser { return &rcv158{bytes.NewReader(nil)} }\n\nfunc mkTB158[T any](x T) T { return x }\n\nfunc gbb158() io.ReadCloser {\n\treturn mkTB158(&gsGB158{}).get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb158()"},
	}},

	{name: "49: benign same-shaped control must pass (no false positive)", desc: "benign same-shaped control passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benign.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcw49 struct{ *bytes.Reader }\n\nfunc (w *rcw49) Close() error { return nil }\n\ntype gb8 struct{ r io.ReadCloser }\n\nvar gb8v gb8\n\nfunc init() { gb8v.r = &rcw49{bytes.NewReader(nil)} }"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb8v.r"},
	}},

	{name: "46: an innocent gatemut_-named file must survive and pass", desc: "an innocent gatemut_-named file is not deleted", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_innocent.go", content: "package mapping\n\nfunc gatemutInnocent() int { return 1 }"},
	}},

	{name: "159: alias-spelled generic receiver result", desc: "alias-spelled generic receiver result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasrecv.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA159 = *os.File\n\ntype gRA159[T any] struct{}\n\nfunc (r gRA159[T]) mk() T { var z T; return z }\n\nfunc gb159() io.ReadCloser {\n\trr := gRA159[zfA159]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb159()"},
	}},

	{name: "160: nested alias generic receiver result", desc: "nested alias generic receiver result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasnested.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA160 = *os.File\ntype zfB160 = zfA160\n\ntype gRA160[T any] struct{}\n\nfunc (r gRA160[T]) mk() T { var z T; return z }\n\nfunc gb160() io.ReadCloser {\n\tvar rr gRA160[zfB160]\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb160()"},
	}},

	{name: "161: alias-spelled explicit instantiation result", desc: "alias-spelled explicit instantiation result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasinst.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA161 = *os.File\n\nfunc mkGen161[T any]() T { var z T; return z }\n\nfunc gb161() io.ReadCloser {\n\treturn mkGen161[zfA161]()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb161()"},
	}},

	{name: "162: alias-spelled generic method value result", desc: "alias-spelled generic method value result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasmethval.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA162 = *os.File\n\ntype gRA162[T any] struct{}\n\nfunc (r gRA162[T]) mk() T { var z T; return z }\n\nfunc gb162() io.ReadCloser {\n\trr := gRA162[zfA162]{}\n\tf := rr.mk\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb162()"},
	}},

	{name: "163: alias-spelled embedded generic promotion", desc: "alias-spelled embedded generic promotion", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliaspromo.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA163 = *os.File\n\ntype gRA163[T any] struct{}\n\nfunc (r gRA163[T]) mk() T { var z T; return z }\n\ntype hEA163 struct{ gRA163[zfA163] }\n\nfunc gb163() io.ReadCloser {\n\the := hEA163{}\n\treturn he.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb163()"},
	}},

	{name: "164: alias-spelled inferred element binding", desc: "alias-spelled inferred element binding", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasinf.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA164 = *os.File\n\nfunc idc164[T any](xs []T) T { var z T; return z }\n\nfunc gb164() io.ReadCloser {\n\treturn idc164([]zfA164{nil})\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb164()"},
	}},

	{name: "165: benign alias generic receiver must pass", desc: "benign alias generic receiver passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benaliasrecv.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv165 struct{ *bytes.Reader }\n\nfunc (w *rcv165) Close() error { return nil }\n\ntype gsGB165 struct{}\n\nfunc (gsGB165) get() io.ReadCloser { return &rcv165{bytes.NewReader(nil)} }\n\ntype zfC165 = *gsGB165\n\ntype gRB165[T any] struct{}\n\nfunc (r gRB165[T]) mk() T { var z T; return z }\n\nfunc gbb165() io.ReadCloser {\n\trr := gRB165[zfC165]{}\n\treturn rr.mk().get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb165()"},
	}},

	{name: "166: benign alias inferred element must pass", desc: "benign alias inferred element passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benaliasinf.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv166 struct{ *bytes.Reader }\n\nfunc (w *rcv166) Close() error { return nil }\n\ntype gsGB166 struct{}\n\nfunc (gsGB166) get() io.ReadCloser { return &rcv166{bytes.NewReader(nil)} }\n\ntype zfC166 = *gsGB166\n\nfunc idcB166[T any](xs []T) T { var z T; return z }\n\nfunc gbb166() io.ReadCloser {\n\treturn idcB166([]zfC166{nil}).get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb166()"},
	}},

	{name: "167: container element read of a generic result", desc: "container element read of a generic result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_elemgen.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA167 = *os.File\n\ntype gR167[T any] struct{}\n\nfunc (r gR167[T]) mk() T { var z T; return z }\n\nfunc gb167() io.ReadCloser {\n\trr := gR167[[]zfA167]{}\n\ta := rr.mk()\n\treturn a[0]\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb167()"},
	}},

	{name: "168: map element read of a generic result", desc: "map element read of a generic result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_mapgen.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA168 = *os.File\n\ntype gR168[T any] struct{}\n\nfunc (r gR168[T]) mk() T { var z T; return z }\n\nfunc gb168() io.ReadCloser {\n\trr := gR168[map[string]zfA168]{}\n\tm := rr.mk()\n\treturn m[\"k\"]\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb168()"},
	}},

	{name: "169: pointer deref of a generic result", desc: "pointer deref of a generic result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_derefgen.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA169 = *os.File\n\ntype gR169[T any] struct{}\n\nfunc (r gR169[T]) mk() T { var z T; return z }\n\nfunc gb169() io.ReadCloser {\n\trr := gR169[*zfA169]{}\n\tp := rr.mk()\n\treturn *p\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb169()"},
	}},

	{name: "170: chan-of-file receive in return position", desc: "chan-of-file receive in return position", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_chanret.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc gb170() io.ReadCloser {\n\tvar c chan *os.File\n\treturn <-c\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb170()"},
	}},

	{name: "171: method call yielding chan of files", desc: "method call yielding chan of files", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_methchan.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype hM171 struct{}\n\nfunc (h hM171) ch() chan *os.File { return nil }\n\nfunc gb171() io.ReadCloser {\n\th := hM171{}\n\tc := h.ch()\n\treturn <-c\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb171()"},
	}},

	{name: "172: generic func result chan of files", desc: "generic func result chan of files", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genchan.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc mkC172[T any]() chan T { return nil }\n\nfunc gb172() io.ReadCloser {\n\tc := mkC172[*os.File]()\n\treturn <-c\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb172()"},
	}},

	{name: "173: cross-package alias generic type argument", desc: "cross-package alias generic type argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_qalias.go", content: "package mapping\n\nimport \"os\"\n\ntype MappingFile = *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_qalias.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\t\"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gR173[T any] struct{}\n\nfunc (r gR173[T]) mk() T { var z T; return z }\n\nfunc gb173() io.ReadCloser {\n\trr := gR173[mapping.MappingFile]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb173()"},
	}},

	{name: "174: generic method result as struct receiver", desc: "generic method result as struct receiver", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_structgen.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype zfA174 = *os.File\n\ntype wS174 struct{ f zfA174 }\n\ntype gR174[T any] struct{}\n\nfunc (r gR174[T]) mk() T { var z T; return z }\n\nfunc gb174() io.ReadCloser {\n\trr := gR174[wS174]{}\n\tr := rr.mk()\n\treturn r.f\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb174()"},
	}},

	{name: "175: benign container element read must pass", desc: "benign container element read passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benelem.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv175 struct{ *bytes.Reader }\n\nfunc (w *rcv175) Close() error { return nil }\n\ntype gsGB175 struct{}\n\nfunc (gsGB175) get() io.ReadCloser { return &rcv175{bytes.NewReader(nil)} }\n\ntype gRB175[T any] struct{}\n\nfunc (r gRB175[T]) mk() T { var z T; return z }\n\nfunc gbb175() io.ReadCloser {\n\trr := gRB175[[]*gsGB175]{}\n\ta := rr.mk()\n\treturn a[0].get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb175()"},
	}},

	{name: "176: benign method chan must pass", desc: "benign method chan passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benmethchan.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv176 struct{ *bytes.Reader }\n\nfunc (w *rcv176) Close() error { return nil }\n\ntype gsGB176 struct{}\n\nfunc (gsGB176) get() io.ReadCloser { return &rcv176{bytes.NewReader(nil)} }\n\ntype hM176 struct{}\n\nfunc (h hM176) ch() chan *gsGB176 { return nil }\n\nfunc gbb176() io.ReadCloser {\n\th := hM176{}\n\tc := h.ch()\n\treturn (<-c).get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb176()"},
	}},

	{name: "177: benign cross-package alias must pass", desc: "benign cross-package alias passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_benqalias.go", content: "package mapping\n\nimport \"bytes\"\n\ntype mrc177 struct{ *bytes.Reader }\n\nfunc (w *mrc177) Close() error { return nil }\n\ntype MappingRC177 = *mrc177"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_benqalias.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\t\"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRB177[T any] struct{}\n\nfunc (r gRB177[T]) mk() T { var z T; return z }\n\nfunc gbb177() io.ReadCloser {\n\trr := gRB177[mapping.MappingRC177]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb177()"},
	}},

	{name: "178: benign generic struct result must pass", desc: "benign generic struct result passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benstruct.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype rcv178 struct{ *bytes.Reader }\n\nfunc (w *rcv178) Close() error { return nil }\n\ntype gsGB178 struct{}\n\nfunc (gsGB178) get() io.ReadCloser { return &rcv178{bytes.NewReader(nil)} }\n\ntype wSB178 struct{ f *gsGB178 }\n\ntype gRB178[T any] struct{}\n\nfunc (r gRB178[T]) mk() T { var z T; return z }\n\nfunc gbb178() io.ReadCloser {\n\trr := gRB178[wSB178]{}\n\tr := rr.mk()\n\treturn r.f.get()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb178()"},
	}},

	{name: "179: renamed-import qualified alias generic type argument", desc: "renamed-import qualified alias generic type argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rqalias.go", content: "package mapping\n\nimport \"os\"\n\ntype MappingFile = *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rqalias.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gR179[T any] struct{}\n\nfunc (r gR179[T]) mk() T { var z T; return z }\n\nfunc gb179() io.ReadCloser {\n\trr := gR179[mm.MappingFile]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb179()"},
	}},

	{name: "180: local alias of renamed-import qualified alias", desc: "local alias of renamed-import qualified alias", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rqchain.go", content: "package mapping\n\nimport \"os\"\n\ntype MappingFile = *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rqchain.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype MChainRen180 = mm.MappingFile\n\ntype gR180[T any] struct{}\n\nfunc (r gR180[T]) mk() T { var z T; return z }\n\nfunc gb180() io.ReadCloser {\n\trr := gR180[MChainRen180]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb180()"},
	}},

	{name: "181: renamed-import qualified alias element spelling", desc: "renamed-import qualified alias element spelling", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rqelem.go", content: "package mapping\n\nimport \"os\"\n\ntype MappingFile = *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rqelem.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gR181[T any] struct{}\n\nfunc (r gR181[T]) mk() T { var z T; return z }\n\nfunc gb181() io.ReadCloser {\n\trr := gR181[[]mm.MappingFile]{}\n\ta := rr.mk()\n\treturn a[0]\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb181()"},
	}},

	{name: "182: renamed-import qualified alias declared variable", desc: "renamed-import qualified alias declared variable", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rqvar.go", content: "package mapping\n\nimport \"os\"\n\ntype MappingFile = *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rqvar.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\nfunc gb182() io.ReadCloser {\n\tvar z mm.MappingFile\n\tz.Chdir()\n\treturn nil\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb182()"},
	}},

	{name: "183: benign renamed-import qualified alias must pass", desc: "benign renamed-import qualified alias passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_benrq.go", content: "package mapping\n\nimport \"bytes\"\n\ntype mrc183 struct{ *bytes.Reader }\n\nfunc (w *mrc183) Close() error { return nil }\n\ntype MappingRC183 = *mrc183"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_benrq.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRB183[T any] struct{}\n\nfunc (r gRB183[T]) mk() T { var z T; return z }\n\nfunc gbb183() io.ReadCloser {\n\trr := gRB183[mm.MappingRC183]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb183()"},
	}},

	{name: "184: benign local alias of renamed-import alias must pass", desc: "benign local alias of renamed-import alias passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_benrqchain.go", content: "package mapping\n\nimport \"bytes\"\n\ntype mrc184 struct{ *bytes.Reader }\n\nfunc (w *mrc184) Close() error { return nil }\n\ntype MappingRC184 = *mrc184"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_benrqchain.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype MChainRenB184 = mm.MappingRC184\n\ntype gRB184[T any] struct{}\n\nfunc (r gRB184[T]) mk() T { var z T; return z }\n\nfunc gbb184() io.ReadCloser {\n\trr := gRB184[MChainRenB184]{}\n\treturn rr.mk()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb184()"},
	}},

	{name: "185: generic method func-typed type argument", desc: "generic method func-typed type argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genmethfunc.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gRZ185[T any] struct{}\n\nfunc (r gRZ185[T]) mk() T { var z T; return z }\n\nfunc gb185() io.ReadCloser {\n\trr := gRZ185[func() *os.File]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb185()"},
	}},

	{name: "186: embedded generic receiver func-typed promotion", desc: "embedded generic receiver func-typed promotion", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genmethfuncemb.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gRZ186[T any] struct{}\n\nfunc (r gRZ186[T]) mk() T { var z T; return z }\n\ntype hE186 struct{ gRZ186[func() *os.File] }\n\nfunc gb186() io.ReadCloser {\n\the := hE186{}\n\tf := he.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb186()"},
	}},

	{name: "187: local alias of func-typed generic type argument", desc: "local alias of func-typed generic type argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genmethfuncalias.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype Fz187 = func() *os.File\n\ntype gRZ187[T any] struct{}\n\nfunc (r gRZ187[T]) mk() T { var z T; return z }\n\nfunc gb187() io.ReadCloser {\n\trr := gRZ187[Fz187]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb187()"},
	}},

	{name: "188: renamed-import func alias as generic type argument", desc: "renamed-import func alias as generic type argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rqfunc.go", content: "package mapping\n\nimport \"os\"\n\ntype F188 = func() *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rqfunc.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRZ188[T any] struct{}\n\nfunc (r gRZ188[T]) mk() T { var z T; return z }\n\nfunc gb188() io.ReadCloser {\n\trr := gRZ188[mm.F188]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb188()"},
	}},

	{name: "189: unapproved method on an invoked generic func result", desc: "unapproved method on invoked generic func result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genmethfuncchdir.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gRZ189[T any] struct{}\n\nfunc (r gRZ189[T]) mk() T { var z T; return z }\n\nfunc gb189() io.ReadCloser {\n\trr := gRZ189[func() *os.File]{}\n\tf := rr.mk()\n\tf().Chdir()\n\treturn nil\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb189()"},
	}},

	{name: "190: benign func-typed generic method result must pass", desc: "benign func-typed generic method result passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bengenmethfunc.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype mrc190 struct{ *bytes.Reader }\n\nfunc (w *mrc190) Close() error { return nil }\n\ntype gRB190[T any] struct{}\n\nfunc (r gRB190[T]) mk() T { var z T; return z }\n\nfunc gbb190() io.ReadCloser {\n\trr := gRB190[func() *mrc190]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb190()"},
	}},

	{name: "191: mixed multi-result function, func-file at position 0", desc: "mixed multi-result function func-file at position 0", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_mixedfunc0.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc getFn191() (func() *os.File, error) { return nil, nil }\n\nfunc gb191() io.ReadCloser {\n\tf, _ := getFn191()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb191()"},
	}},

	{name: "192: mixed multi-result function, func-file at position 1", desc: "mixed multi-result function func-file at position 1", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_mixedfunc1.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc hW192() (int, func() *os.File) { return 0, nil }\n\nfunc gb192() io.ReadCloser {\n\t_, f := hW192()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb192()"},
	}},

	{name: "193: defined type over a renamed-qualified alias", desc: "defined type over renamed-qualified alias", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_defren.go", content: "package mapping\n\nimport \"os\"\n\ntype FUnqual = func() *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_defren.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype F193 mm.FUnqual\n\ntype gRZ193[T any] struct{}\n\nfunc (r gRZ193[T]) mk() T { var z T; return z }\n\nfunc gb193() io.ReadCloser {\n\trr := gRZ193[F193]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb193()"},
	}},

	{name: "194: local defined func type as generic type argument", desc: "local defined func type as generic type argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_deffunc.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype F194 func() *os.File\n\ntype gRZ194[T any] struct{}\n\nfunc (r gRZ194[T]) mk() T { var z T; return z }\n\nfunc gb194() io.ReadCloser {\n\trr := gRZ194[F194]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb194()"},
	}},

	{name: "195: cross-package defined func type as generic type argument", desc: "cross-package defined func type as generic type argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_defqual.go", content: "package mapping\n\nimport \"os\"\n\ntype F195 func() *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_defqual.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRZ195[T any] struct{}\n\nfunc (r gRZ195[T]) mk() T { var z T; return z }\n\nfunc gb195() io.ReadCloser {\n\trr := gRZ195[mm.F195]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb195()"},
	}},

	{name: "196: defined type over a cross-package defined func type", desc: "defined type over cross-package defined func type", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_defdef.go", content: "package mapping\n\nimport \"os\"\n\ntype F196 func() *os.File"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_defdef.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype F196B mm.F196\n\ntype gRZ196[T any] struct{}\n\nfunc (r gRZ196[T]) mk() T { var z T; return z }\n\nfunc gb196() io.ReadCloser {\n\trr := gRZ196[F196B]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb196()"},
	}},

	{name: "197: benign mixed multi-result bytes func must pass", desc: "benign mixed multi-result bytes func passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benmixed.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype mrc197 struct{ *bytes.Reader }\n\nfunc (w *mrc197) Close() error { return nil }\n\nfunc getB197() (func() *mrc197, error) { return nil, nil }\n\nfunc gbb197() io.ReadCloser {\n\tf, _ := getB197()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb197()"},
	}},

	{name: "198: benign defined-over-renamed bytes type must pass", desc: "benign defined-over-renamed bytes type passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_bandef.go", content: "package mapping\n\nimport \"bytes\"\n\ntype mrc198 struct{ *bytes.Reader }\n\nfunc (w *mrc198) Close() error { return nil }\n\ntype MRC198 = func() *mrc198"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_bandef.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype B198 mm.MRC198\n\ntype gRB198[T any] struct{}\n\nfunc (r gRB198[T]) mk() T { var z T; return z }\n\nfunc gbb198() io.ReadCloser {\n\trr := gRB198[B198]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb198()"},
	}},

	{name: "199: interface-typed generic result of a mixed method, pos 0", desc: "interface-typed generic result of a mixed method at position 0", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ifacepos0.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype Iface199 interface{ Get() (func() *os.File, error) }\n\ntype gRZ199[T any] struct{}\n\nfunc (r gRZ199[T]) mk() T { var z T; return z }\n\nfunc gb199() io.ReadCloser {\n\trr := gRZ199[Iface199]{}\n\tx := rr.mk()\n\tf, _ := x.Get()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb199()"},
	}},

	{name: "200: interface-typed generic result of a mixed method, pos 1", desc: "interface-typed generic result of a mixed method at position 1", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ifacepos1.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype Iface200 interface{ Get() (error, func() *os.File) }\n\ntype gRZ200[T any] struct{}\n\nfunc (r gRZ200[T]) mk() T { var z T; return z }\n\nfunc gb200() io.ReadCloser {\n\trr := gRZ200[Iface200]{}\n\tx := rr.mk()\n\t_, f := x.Get()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb200()"},
	}},

	{name: "201: interface-typed generic result, chan-of-func mixed method", desc: "interface-typed generic chan-of-func mixed method result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ifacechan.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype Iface201 interface{ Get() (chan func() *os.File, error) }\n\ntype gRZ201[T any] struct{}\n\nfunc (r gRZ201[T]) mk() T { var z T; return z }\n\nfunc gb201() io.ReadCloser {\n\trr := gRZ201[Iface201]{}\n\tx := rr.mk()\n\tc, _ := x.Get()\n\tz := <-c\n\treturn z()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb201()"},
	}},

	{name: "202: cross-package defined type over a same-package alias", desc: "cross-package defined type over a same-package alias", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_defalias.go", content: "package mapping\n\nimport \"os\"\n\ntype FA202 = func() *os.File\ntype FD202 FA202"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_defalias.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRZ202[T any] struct{}\n\nfunc (r gRZ202[T]) mk() T { var z T; return z }\n\nfunc gb202() io.ReadCloser {\n\trr := gRZ202[mm.FD202]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb202()"},
	}},

	{name: "203: cross-package alias over a defined func type", desc: "cross-package alias over a defined func type", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_aliasdef.go", content: "package mapping\n\nimport \"os\"\n\ntype FD203 func() *os.File\ntype FE203 = FD203"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_aliasdef.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRZ203[T any] struct{}\n\nfunc (r gRZ203[T]) mk() T { var z T; return z }\n\nfunc gb203() io.ReadCloser {\n\trr := gRZ203[mm.FE203]{}\n\tf := rr.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb203()"},
	}},

	{name: "204: non-generic method mixed results, func-file at pos 0", desc: "non-generic method mixed results at position 0", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_methpos0.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype hM204 struct{}\n\nfunc (h hM204) mk() (func() *os.File, error) { return nil, nil }\n\nfunc gb204() io.ReadCloser {\n\th := hM204{}\n\tf, _ := h.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb204()"},
	}},

	{name: "205: non-generic method mixed results, func-file at pos 1", desc: "non-generic method mixed results at position 1", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_methpos1.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype hM205 struct{}\n\nfunc (h hM205) mk() (int, func() *os.File) { return 0, nil }\n\nfunc gb205() io.ReadCloser {\n\th := hM205{}\n\t_, f := h.mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb205()"},
	}},

	{name: "206: benign interface-typed generic mixed bytes method", desc: "benign interface-typed mixed bytes method passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_beniface.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype mrc206 struct{ *bytes.Reader }\n\nfunc (w *mrc206) Close() error { return nil }\n\ntype Iface206 interface{ Get() (func() *mrc206, error) }\n\ntype gRZ206[T any] struct{}\n\nfunc (r gRZ206[T]) mk() T { var z T; return z }\n\nfunc gbb206() io.ReadCloser {\n\trr := gRZ206[Iface206]{}\n\tx := rr.mk()\n\tf, _ := x.Get()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb206()"},
	}},

	{name: "207: embedded interface promotion, single func-file method", desc: "embedded interface promotion with a single func-file method", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_embiface.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype IBase207 interface{ Get() func() *os.File }\n\ntype IEmb207 interface{ IBase207 }\n\ntype gRZ207[T any] struct{}\n\nfunc (r gRZ207[T]) mk() T { var z T; return z }\n\nfunc gb207() io.ReadCloser {\n\trr := gRZ207[IEmb207]{}\n\tx := rr.mk()\n\treturn x.Get()()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb207()"},
	}},

	{name: "208: embedded interface promotion, mixed multi-result method", desc: "embedded interface promotion with mixed multi-result method", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_embifacemixed.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype IBase208 interface{ Get() (func() *os.File, error) }\n\ntype IEmb208 interface{ IBase208 }\n\ntype gRZ208[T any] struct{}\n\nfunc (r gRZ208[T]) mk() T { var z T; return z }\n\nfunc gb208() io.ReadCloser {\n\trr := gRZ208[IEmb208]{}\n\tx := rr.mk()\n\tf, _ := x.Get()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb208()"},
	}},

	{name: "209: cross-package defined struct method via generic arg", desc: "cross-package defined struct method through a generic argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_crossstruct.go", content: "package mapping\n\nimport \"os\"\n\ntype S209 struct{ F func() *os.File }\n\nfunc (s S209) Get() func() *os.File { return s.F }\n\ntype Mk209[T any] struct{}\n\nfunc (r Mk209[T]) Mk() T { var z T; return z }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_crossstruct.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRZ209[T any] struct{}\n\nfunc (r gRZ209[T]) md() T { var z T; return z }\n\nfunc gb209() io.ReadCloser {\n\trr := gRZ209[mm.S209]{}\n\ts := rr.md()\n\tf := s.Get()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb209()"},
	}},

	{name: "210: nine-hop qualified defined chain", desc: "nine-hop qualified defined chain through a generic argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_longchain.go", content: "package mapping\n\nimport \"os\"\n\ntype A210 = func() *os.File\ntype B210 A210\ntype C210 B210\ntype D210 C210\ntype E210 D210\ntype F210 E210\ntype G210 F210\ntype H210 G210\ntype I210 H210\ntype J210 I210"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_longchain.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRZ210[T any] struct{}\n\nfunc (r gRZ210[T]) mc() T { var z T; return z }\n\nfunc gb210() io.ReadCloser {\n\trr := gRZ210[mm.J210]{}\n\tf := rr.mc()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb210()"},
	}},

	{name: "211: benign embedded interface bytes twin", desc: "benign embedded interface bytes method passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_benembiface.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype IBase211 interface{ Get() func() *bytes.Reader }\n\ntype IEmb211 interface{ IBase211 }\n\ntype gRZ211[T any] struct{}\n\nfunc (r gRZ211[T]) mk() T { var z T; return z }\n\nfunc gbb211() io.ReadCloser {\n\trr := gRZ211[IEmb211]{}\n\tx := rr.mk()\n\treturn io.NopCloser(x.Get()())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb211()"},
	}},

	{name: "212: benign cross-package struct method bytes twin", desc: "benign cross-package struct method bytes twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_bencrossstruct.go", content: "package mapping\n\nimport \"bytes\"\n\ntype SB212 struct{ F func() *bytes.Reader }\n\nfunc (s SB212) Get() func() *bytes.Reader { return s.F }\n\ntype Mk212[T any] struct{}\n\nfunc (r Mk212[T]) Mk() T { var z T; return z }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_bencrossstruct.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype gRZ212[T any] struct{}\n\nfunc (r gRZ212[T]) md() T { var z T; return z }\n\nfunc gbb212() io.ReadCloser {\n\trr := gRZ212[mm.SB212]{}\n\ts := rr.md()\n\tf := s.Get()\n\treturn io.NopCloser(f())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb212()"},
	}},

	{name: "213: renamed-qualifier cross-package interface embedding", desc: "renamed-qualifier cross-package interface embedding", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rniface.go", content: "package mapping\n\nimport \"os\"\n\ntype IMapBase213 interface{ Get() func() *os.File }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rniface.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype IEmb213 interface{ mm.IMapBase213 }\n\ntype gRZ213[T any] struct{}\n\nfunc (r gRZ213[T]) mk() T { var z T; return z }\n\nfunc gb213() io.ReadCloser {\n\tx := gRZ213[IEmb213]{}.mk()\n\treturn x.Get()()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb213()"},
	}},

	{name: "214: generic-interface instantiation embedding, func arg", desc: "generic-interface instantiation embedding with func-file argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_geniface.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype IBaseGN214[T any] interface{ Get() T }\ntype IEmbGN214 interface{ IBaseGN214[func() *os.File] }\n\ntype gRZ214[T any] struct{}\n\nfunc (r gRZ214[T]) mk() T { var z T; return z }\n\nfunc gb214() io.ReadCloser {\n\tx := gRZ214[IEmbGN214]{}.mk()\n\treturn x.Get()()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb214()"},
	}},

	{name: "215: generic-interface instantiation embedding, chan arg", desc: "generic-interface instantiation embedding with chan-of-func argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genifacechan.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype IBaseGO215[T any] interface{ Get() T }\ntype IEmbGO215 interface{ IBaseGO215[chan func() *os.File] }\n\ntype gRZ215[T any] struct{}\n\nfunc (r gRZ215[T]) mk() T { var z T; return z }\n\nfunc gb215() io.ReadCloser {\n\tx := gRZ215[IEmbGO215]{}.mk()\n\tch := x.Get()\n\tf := <-ch\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb215()"},
	}},

	{name: "216: renamed-qualified generic interface instantiation", desc: "renamed-qualified generic interface instantiation embedding", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rngeniface.go", content: "package mapping\n\ntype IMapBase216[T any] interface{ Get() T }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rngeniface.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype IEmb216 interface{ mm.IMapBase216[func() *os.File] }\n\ntype gRZ216[T any] struct{}\n\nfunc (r gRZ216[T]) mk() T { var z T; return z }\n\nfunc gb216() io.ReadCloser {\n\tx := gRZ216[IEmb216]{}.mk()\n\treturn x.Get()()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb216()"},
	}},

	{name: "217: cross-package generic struct method via renamed qualifier", desc: "cross-package generic struct method instantiated via renamed qualifier", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rngenstruct.go", content: "package mapping\n\ntype MkS217[T any] struct{}\n\nfunc (r MkS217[T]) Mk() T { var z T; return z }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rngenstruct.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\nfunc gb217() io.ReadCloser {\n\trr := mm.MkS217[func() *os.File]{}\n\tf := rr.Mk()\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb217()"},
	}},

	{name: "218: benign renamed-qualifier interface bytes twin", desc: "benign renamed-qualifier interface bytes twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_benrniface.go", content: "package mapping\n\nimport \"bytes\"\n\ntype IMapBaseL218 interface{ Get() func() *bytes.Reader }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_benrniface.go", content: "package reader\n\nimport (\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype IEmb218 interface{ mm.IMapBaseL218 }\n\ntype gRZ218[T any] struct{}\n\nfunc (r gRZ218[T]) mk() T { var z T; return z }\n\nfunc gbb218() io.ReadCloser {\n\tx := gRZ218[IEmb218]{}.mk()\n\treturn io.NopCloser(x.Get()())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb218()"},
	}},

	{name: "219: benign generic-interface instantiation bytes twin", desc: "benign generic-interface instantiation bytes twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bengeniface.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype IBaseGP219[T any] interface{ Get() T }\ntype IEmbGP219 interface{ IBaseGP219[func() *bytes.Reader] }\n\ntype gRZ219[T any] struct{}\n\nfunc (r gRZ219[T]) mk() T { var z T; return z }\n\nfunc gbb219() io.ReadCloser {\n\tx := gRZ219[IEmbGP219]{}.mk()\n\treturn io.NopCloser(x.Get()())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb219()"},
	}},

	{name: "220: benign renamed-qualified generic interface bytes twin", desc: "benign renamed-qualified generic interface bytes twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_benrngeniface.go", content: "package mapping\n\ntype IMapBaseQ220[T any] interface{ Get() T }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_benrngeniface.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype IEmb220 interface{ mm.IMapBaseQ220[func() *bytes.Reader] }\n\ntype gRZ220[T any] struct{}\n\nfunc (r gRZ220[T]) mk() T { var z T; return z }\n\nfunc gbb220() io.ReadCloser {\n\tx := gRZ220[IEmb220]{}.mk()\n\treturn io.NopCloser(x.Get()())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb220()"},
	}},

	{name: "221: benign remote generic struct bytes twin", desc: "benign remote generic struct bytes twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_benrngenstruct.go", content: "package mapping\n\ntype MkT221[T any] struct{}\n\nfunc (r MkT221[T]) Mk() T { var z T; return z }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_benrngenstruct.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\nfunc gbb221() io.ReadCloser {\n\trr := mm.MkT221[func() *bytes.Reader]{}\n\tf := rr.Mk()\n\treturn io.NopCloser(f())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb221()"},
	}},

	{name: "222: defined type over instantiated generic interface", desc: "defined type over instantiated generic interface embedding", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_definst.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype IBaseGA222[T any] interface{ Get() T }\ntype DA222 IBaseGA222[func() *os.File]\ntype IEmbA222 interface{ DA222 }\n\ntype gRZ222[T any] struct{}\n\nfunc (r gRZ222[T]) mk() T { var z T; return z }\n\nfunc gb222() io.ReadCloser {\n\tx := gRZ222[IEmbA222]{}.mk()\n\treturn x.Get()()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb222()"},
	}},

	{name: "223: renamed-qualified defined-over-instantiated interface", desc: "renamed-qualified defined-over-instantiated generic interface embedding", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rndefinst.go", content: "package mapping\n\ntype IBaseGG223[T any] interface{ Get() T }\n\ntype Mk223[T any] struct{}\n\nfunc (r Mk223[T]) Mk() T { var z T; return z }"},
		batteryOp{kind: "create", path: "internal/reader/gatemut_rndefinst.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n\n\tmm \"github.com/firehol/iprange/v4/go/internal/mapping\"\n)\n\ntype DG223 mm.IBaseGG223[func() *os.File]\ntype IEmbG223 interface{ DG223 }\n\ntype gRZ223[T any] struct{}\n\nfunc (r gRZ223[T]) mk() T { var z T; return z }\n\nfunc gb223() io.ReadCloser {\n\tx := gRZ223[IEmbG223]{}.mk()\n\treturn x.Get()()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb223()"},
	}},

	{name: "224: benign defined-over-instantiated interface bytes twin", desc: "benign defined-over-instantiated interface bytes twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bandefinst.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype IBaseGH224[T any] interface{ Get() T }\ntype DH224 IBaseGH224[func() *bytes.Reader]\ntype IEmbH224 interface{ DH224 }\n\ntype gRZ224[T any] struct{}\n\nfunc (r gRZ224[T]) mk() T { var z T; return z }\n\nfunc gbb224() io.ReadCloser {\n\tx := gRZ224[IEmbH224]{}.mk()\n\treturn io.NopCloser(x.Get()())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb224()"},
	}},

	{name: "225: multi-level generic-interface instantiation embedding", desc: "multi-level generic-interface instantiation embedding", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_nestedgen.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype InnerL225[T any] interface{ Get() T }\ntype IBaseGL225[T any] interface{ InnerL225[T] }\ntype IEmbL225 interface{ IBaseGL225[func() *os.File] }\n\ntype gRZ225[T any] struct{}\n\nfunc (r gRZ225[T]) mk() T { var z T; return z }\n\nfunc gb225() io.ReadCloser {\n\tx := gRZ225[IEmbL225]{}.mk()\n\treturn x.Get()()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb225()"},
	}},

	{name: "226: three-level generic-interface instantiation embedding", desc: "three-level generic-interface instantiation embedding with chan argument", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_nestedgen3.go", content: "package reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype InnerX226[T any] interface{ Get() T }\ntype MidX226[T any] interface{ InnerX226[T] }\ntype TopX226 interface{ MidX226[chan func() *os.File] }\n\ntype gRZ226[T any] struct{}\n\nfunc (r gRZ226[T]) mk() T { var z T; return z }\n\nfunc gb226() io.ReadCloser {\n\tx := gRZ226[TopX226]{}.mk()\n\tch := x.Get()\n\tf := <-ch\n\treturn f()\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gb226()"},
	}},

	{name: "227: benign multi-level generic-interface bytes twin", desc: "benign multi-level generic-interface bytes twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bennestedgen.go", content: "package reader\n\nimport (\n\t\"bytes\"\n\t\"io\"\n)\n\ntype InnerXB227[T any] interface{ Get() T }\ntype MidXB227[T any] interface{ InnerXB227[T] }\ntype TopXB227 interface{ MidXB227[func() *bytes.Reader] }\n\ntype gRZ227[T any] struct{}\n\nfunc (r gRZ227[T]) mk() T { var z T; return z }\n\nfunc gbb227() io.ReadCloser {\n\tx := gRZ227[TopXB227]{}.mk()\n\treturn io.NopCloser(x.Get()())\n}"},
		batteryOp{kind: "ins", path: "internal/reader/metadata.go", content: "zr = gbb227()"},
	}},

	{name: "228: cgo content transfer via import \"C\" + C.pread", desc: "cgo C.pread content transfer", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_cgo.go", content: "package iprangedb\n\n/*\n#include <unistd.h>\n*/\nimport \"C\"\n\nimport (\n\t\"os\"\n\t\"unsafe\"\n)\n\nfunc gateCGORead228(file *os.File, out []byte) int {\n\tif len(out) == 0 {\n\t\treturn 0\n\t}\n\treturn int(C.pread(C.int(file.Fd()), unsafe.Pointer(&out[0]), C.size_t(len(out)), 0))\n}"},
	}},

	{name: "229: unix.RawSyscall descriptor read in the mapping owner", desc: "unix.RawSyscall descriptor read in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rawsyscall_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"os\"\n\t\"unsafe\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc gateRawSyscallRead229(file *os.File, out []byte) int {\n\tif len(out) == 0 {\n\t\treturn 0\n\t}\n\tn, _, _ := unix.RawSyscall(unix.SYS_READ, file.Fd(), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)))\n\treturn int(n)\n}"},
	}},

	{name: "230: go:linkname raw-symbol aliasing", desc: "go:linkname alias to syscall.read", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_linkname.go", content: "package iprangedb\n\nimport (\n\t_ \"unsafe\"\n)\n\n//go:linkname gateRawRead230 syscall.read\nfunc gateRawRead230(fd int, p []byte, n int) (int, error)\n\nfunc useLinkname230(fd int, p []byte) {\n\t_, _ = gateRawRead230(fd, p, len(p))\n}"},
	}},

	{name: "231: benign x/sys lifecycle twin", desc: "benign x/sys lifecycle twin passes the gate", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_lifecycle_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"os\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc gateLifecycle231(file *os.File, size int) error {\n\tflock := unix.Flock_t{Type: unix.F_RDLCK}\n\tif err := unix.FcntlFlock(file.Fd(), unix.F_SETLKW, &flock); err != nil {\n\t\treturn err\n\t}\n\t_, err := unix.Mmap(int(file.Fd()), 0, size, unix.PROT_READ, unix.MAP_SHARED)\n\treturn err\n}"},
	}},

	{name: "232: unix.Preadv2 descriptor read in the mapping owner", desc: "unix.Preadv2 descriptor read in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_preadv2_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"os\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc gatePreadv2Read232(file *os.File, iovs [][]byte) (int, error) {\n\treturn unix.Preadv2(int(file.Fd()), iovs, 0, 0)\n}"},
	}},

	{name: "233: unix.RawSyscallNoError descriptor read", desc: "unix.RawSyscallNoError descriptor read in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rawsyscallnoerror_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"os\"\n\t\"unsafe\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc gateRawSyscallNoErrorRead233(file *os.File, out []byte) uintptr {\n\tif len(out) == 0 {\n\t\treturn 0\n\t}\n\tr1, _ := unix.RawSyscallNoError(unix.SYS_READ, file.Fd(), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)))\n\treturn r1\n}"},
	}},

	{name: "234: unix.SyscallNoError descriptor read", desc: "unix.SyscallNoError descriptor read in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_syscallnoerror_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"os\"\n\t\"unsafe\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc gateSyscallNoErrorRead234(file *os.File, out []byte) uintptr {\n\tif len(out) == 0 {\n\t\treturn 0\n\t}\n\tr1, _ := unix.SyscallNoError(unix.SYS_READ, file.Fd(), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)))\n\treturn r1\n}"},
	}},

	{name: "235: unix.Pwritev2 descriptor write in the mapping owner", desc: "unix.Pwritev2 descriptor write in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_pwritev2_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"os\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc gatePwritev2Write235(file *os.File, iovs [][]byte) (int, error) {\n\treturn unix.Pwritev2(int(file.Fd()), iovs, 0, 0)\n}"},
	}},

	{name: "236: unix.Dup2 + unix.Exec subprocess content transfer", desc: "unix.Dup2+Exec subprocess content transfer", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_dupexec_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"os\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc gateDup2Exec236(file *os.File) error {\n\tif err := unix.Dup2(int(file.Fd()), 0); err != nil {\n\t\treturn err\n\t}\n\treturn unix.Exec(\"/bin/cat\", []string{\"/bin/cat\"}, nil)\n}"},
	}},

	{name: "237: bodyless assembly-backed raw read", desc: "bodyless assembly-backed raw read", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_asmread_linux.go", content: "//go:build linux\npackage mapping\n\nfunc gateAsmRead237(fd uintptr, p []byte) (int, error)"},
		batteryOp{kind: "create", path: "internal/mapping/gatemut_asmread_linux.s", content: "//go:build linux\n\n#include \"textflag.h\"\n\nTEXT \u00b7gateAsmRead237(SB),NOSPLIT,$0-56\n\tMOVQ $0, AX\n\tSYSCALL\n\tRET"},
	}},

	{name: "239: assembly object without a Go declaration", desc: "assembly object file", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_asmfile/gatemut_asmfile_linux.s", content: "//go:build linux\n\nTEXT \u00b7gateAsmNop239(SB),NOSPLIT,$0\n\tRET"},
	}},

	{name: "240: fcntl F_DUPFD descriptor duplication", desc: "fcntl F_DUPFD descriptor duplication", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_fcntldup_linux.go", content: "//go:build linux\npackage mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateFcntlDup240(fd uintptr) (int, error) {\n\treturn unix.FcntlInt(fd, unix.F_DUPFD, 0)\n}"},
	}},

	{name: "244: hidden dot-directory is scanned like any other", desc: "hidden dot-directory content", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: ".smuggle/.smuggle.go", content: "package smuggle\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\nfunc ReadAll(f *os.File) ([]byte, error) { return io.ReadAll(f) }"},
	}},

	{name: "247: uppercase assembly-object suffix", desc: "uppercase .S assembly object", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_evil.S", content: "TEXT \u00b7GateMut247(SB),0,$0-0\n\tRET"},
	}},

	{name: "249: os.CopyFS directory copy", desc: "os.CopyFS directory copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_copyfs/mut.go", content: "package gatemut_copyfs\n\nimport (\n\t\"io/fs\"\n\t\"os\"\n)\n\nfunc Fetch(dst string, tree fs.FS) error {\n\treturn os.CopyFS(dst, tree)\n}"},
	}},

	{name: "250: os.OpenInRoot handle reaching a flate reader", desc: "os.OpenInRoot handle reaching a flate reader", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_openroot_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc gateOpenInflated250(dir, name string) (io.ReadCloser, error) {\n\tf, err := os.OpenInRoot(dir, name)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn flate.NewReader(f), nil\n}"},
	}},

	{name: "251: unix.Tee descriptor-to-descriptor copy", desc: "unix.Tee descriptor copy", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_tee_linux.go", content: "//go:build linux\npackage mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateTee251(src, dst, length int) (int64, error) {\n\treturn unix.Tee(src, dst, length, 0)\n}"},
	}},

	{name: "252: os.Root laundered through a struct field", desc: "os.Root laundered through a struct field", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_rootfield_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\ntype gateRootField252 struct {\n\tr *os.Root\n}\n\nfunc gateRootFieldOpen252(dir, name string) (io.ReadCloser, error) {\n\troot, err := os.OpenRoot(dir)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\th := gateRootField252{r: root}\n\tf, err := h.r.Open(name)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn flate.NewReader(f), nil\n}"},
	}},

	{name: "253: file method value bound then invoked", desc: "file method value bound then invoked", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_mv_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc gateMethodValue253(dir, name string) (io.ReadCloser, error) {\n\troot, err := os.OpenRoot(dir)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\topen := root.Open\n\tf, err := open(name)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn flate.NewReader(f), nil\n}"},
	}},

	{name: "254: func-typed var with an initializer (os.Root result)", desc: "func-typed var with an initializer producing a Root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_ftv_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc gateFuncTypedVar254(dir, name string) (io.ReadCloser, error) {\n\tvar newRoot func(string) (*os.Root, error) = os.OpenRoot\n\troot, err := newRoot(dir)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tf, err := root.Open(name)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn flate.NewReader(f), nil\n}"},
	}},

	{name: "255: func-typed var with an initializer (*os.File result)", desc: "func-typed var with an initializer producing a File", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_ftf_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc gateFuncTypedFile255(name string) (io.ReadCloser, error) {\n\tvar openPath func(string) (*os.File, error) = os.Open\n\tf, err := openPath(name)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn flate.NewReader(f), nil\n}"},
	}},

	{name: "256: plain assignment of a stdlib producer value", desc: "plain assignment of a stdlib producer value", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_plain_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc gatePlainOpen256(name string) (io.ReadCloser, error) {\n\topenPath := os.Open\n\tf, err := openPath(name)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn flate.NewReader(f), nil\n}"},
	}},

	{name: "257: bound method expression on os.Root", desc: "bound method expression on os.Root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_me_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc gateMethodExpr257(dir, name string) io.ReadCloser {\n\troot, _ := os.OpenRoot(dir)\n\topen := (*os.Root).Open\n\tf, _ := open(root, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "258: package-level method-expression var", desc: "package-level method expression on os.Root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_me_pkg_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nvar openRootPkg = (*os.Root).Open\n\nfunc gatePkgMethodExpr258(dir, name string) io.ReadCloser {\n\troot, _ := os.OpenRoot(dir)\n\tf, _ := openRootPkg(root, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "259: same-module cross-package producer var (os.OpenRoot)", desc: "same-module cross-package producer var (os.OpenRoot)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/format/gatemut_p15.go", content: "//go:build linux\n\npackage format\n\nimport \"os\"\n\nvar OpenRoot = os.OpenRoot"},
		batteryOp{kind: "create", path: "internal/mapping/gatemut_p15.go", content: "//go:build linux\n\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\n\t\"github.com/firehol/iprange/v4/go/internal/format\"\n)\n\nfunc gateCrossPkgVar259(dir, name string) io.ReadCloser {\n\troot, _ := format.OpenRoot(dir)\n\tf, _ := root.Open(name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "260: same-module cross-package producer var (os.Open)", desc: "same-module cross-package producer var (os.Open)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/format/gatemut_p15f.go", content: "//go:build linux\n\npackage format\n\nimport \"os\"\n\nvar Open = os.Open"},
		batteryOp{kind: "create", path: "internal/mapping/gatemut_p15f.go", content: "//go:build linux\n\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\n\t\"github.com/firehol/iprange/v4/go/internal/format\"\n)\n\nfunc gateCrossPkgFile260(name string) io.ReadCloser {\n\tf, _ := format.Open(name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "261: doubly-parenthesized method expression", desc: "doubly-parenthesized method expression on os.Root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_dp_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\nfunc gateDoubleParen261(dir, name string) io.ReadCloser {\n\troot, _ := os.OpenRoot(dir)\n\topen := ((*os.Root)).Open\n\tf, _ := open(root, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "262: renamed-import method expression", desc: "renamed-import method expression on os.Root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_renimp_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\to \"os\"\n)\n\nfunc gateRenamedImp262(dir, name string) io.ReadCloser {\n\troot, _ := o.OpenRoot(dir)\n\topen := (*o.Root).Open\n\tf, _ := open(root, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "263: alias over a renamed stdlib import", desc: "alias-over-renamed method expression on os.Root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_renalias_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\to \"os\"\n)\n\ntype RR = o.Root\n\nfunc gateRenamedAlias263(dir, name string) io.ReadCloser {\n\troot, _ := o.OpenRoot(dir)\n\topen := (*RR).Open\n\tf, _ := open(root, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "264: wrapper struct promoting *os.Root methods", desc: "wrapper-promoted method expression on os.Root", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_wrap_linux.go", content: "//go:build linux\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\ntype WE struct{ *os.Root }\n\nfunc gateWrapper264(dir, name string) io.ReadCloser {\n\troot, _ := os.OpenRoot(dir)\n\twe := &WE{Root: root}\n\topen := (*WE).Open\n\tf, _ := open(we, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "265: cross-package producer var bound as a value", desc: "cross-package producer var bound as a value", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/format/gatemut_p15v.go", content: "//go:build linux\n\npackage format\n\nimport \"os\"\n\nvar Open = os.Open"},
		batteryOp{kind: "create", path: "internal/mapping/gatemut_p15v.go", content: "//go:build linux\n\npackage mapping\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\n\t\"github.com/firehol/iprange/v4/go/internal/format\"\n)\n\nfunc gateBoundValue265(name string) io.ReadCloser {\n\topen := format.Open\n\tf, _ := open(name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "266: file value laundered through an interface conversion", desc: "file value laundered through an interface conversion", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_zr_linux.go", content: "//go:build linux\npackage reader\n\nimport (\n\t\"io\"\n\t\"os\"\n)\n\ntype gateZR interface {\n\tRead([]byte) (int, error)\n}\n\nfunc gateLaunder266(name string) ([]byte, bool, error) {\n\tf, err := os.Open(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tzr := gateZR(f)\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "267: generic identity erasing the taint into an interface", desc: "generic identity erasing the taint into an interface", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc probeWrapR[T io.Reader](v T) io.Reader { return v }\n\nfunc gateGenIface267(f *os.File, n int) ([]byte, error) {\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tzr := probeWrapR(f)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "268: composite-literal field launder", desc: "composite-literal field launder", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateLaunderS struct{ r io.Reader }\n\nfunc gateComplit268(f *os.File, n int) ([]byte, error) {\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\ts := gateLaunderS{r: f}\n\tzr := s.r\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "269: method expression on an instantiated generic wrapper", desc: "method expression on an instantiated generic wrapper", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_genwrap_linux.go", content: "//go:build linux\npackage reader\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\ntype gW[T any] struct{ *os.Root }\n\nfunc gateGenWrap269(dir, name string) io.ReadCloser {\n\troot, _ := os.OpenRoot(dir)\n\tw := &gW[byte]{Root: root}\n\topen := (*gW[byte]).Open\n\tf, _ := open(w, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "270: embedding chain deeper than the original walk budget", desc: "embedding chain deeper than the original walk budget", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_deep_linux.go", content: "//go:build linux\npackage reader\n\nimport (\n\t\"compress/flate\"\n\t\"io\"\n\t\"os\"\n)\n\ntype gDE1 struct{ *os.Root }\ntype gDE2 struct{ *gDE1 }\ntype gDE3 struct{ *gDE2 }\ntype gDE4 struct{ *gDE3 }\ntype gDE5 struct{ *gDE4 }\n\nfunc gateDeep270(dir, name string) io.ReadCloser {\n\troot, _ := os.OpenRoot(dir)\n\tw := &gDE5{&gDE4{&gDE3{&gDE2{&gDE1{Root: root}}}}}\n\topen := (*gDE5).Open\n\tf, _ := open(w, name)\n\treturn flate.NewReader(f)\n}"},
	}},

	{name: "271: generic interface result under a renamed io qualifier", desc: "generic interface result under a renamed io qualifier", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\"\\n\\tr \"io\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateHuntWrapA[T r.Reader](v T) r.Reader { return v }\n\nfunc gateHuntA(f *os.File, n int) ([]byte, error) {\n\tzr := gateHuntWrapA(f)\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "272: generic interface result from another stdlib package", desc: "generic interface result from another stdlib package", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\"\\n\\t\"io/fs\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateHuntWrapB[T fs.File](v T) fs.File { return v }\n\nfunc gateHuntB(f *os.File, n int) ([]byte, error) {\n\tzr := gateHuntWrapB(f)\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "273: generic slice-of-type-parameter result", desc: "generic slice-of-type-parameter result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateHuntWrapC[T any](v T) []T { return []T{v} }\n\nfunc gateHuntC(f *os.File, n int) ([]byte, error) {\n\tfiles := gateHuntWrapC(f)\n\tzr := files[0]\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "274: positional composite-literal field launder", desc: "positional composite-literal field launder", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateHuntS struct{ r io.Reader }\n\nfunc gateHuntD(f *os.File, n int) ([]byte, error) {\n\ts := gateHuntS{f}\n\tzr := s.r\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "275: generic array-of-type-parameter result", desc: "generic array-of-type-parameter result", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateHuntWrapE[T any](v T) [2]T { return [2]T{v, v} }\n\nfunc gateHuntE(f *os.File, n int) ([]byte, error) {\n\tarr := gateHuntWrapE(f)\n\tzr := arr[0]\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "276: chan send of a generic-erased value", desc: "chan send of a generic-erased value", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\"\\n\\tr \"io\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateHuntWrapF[T r.Reader](v T) r.Reader { return v }\n\nfunc gateHuntF(f *os.File, n int) ([]byte, error) {\n\tch := make(chan r.Reader, 1)\n\tch <- gateHuntWrapF(f)\n\tzr := <-ch\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "277: positional literal hiding an os.Root opener", desc: "positional literal hiding an os.Root opener", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateHuntRootIface struct {\n\topener interface {\n\t\tOpen(string) (*os.File, error)\n\t}\n}\n\nfunc gateHuntG(dir, name string, n int) ([]byte, error) {\n\troot, _ := os.OpenRoot(dir)\n\ts := gateHuntRootIface{root}\n\tzr0 := s.opener\n\tf, _ := zr0.Open(name)\n\tzr := f\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "278: embedded file-handle literal launder", desc: "embedded file-handle literal launder", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateEmbR struct {\n\t*os.Root\n}\n\nfunc gateEmbeddedR278(dir, name string, n int) ([]byte, error) {\n\troot, _ := os.OpenRoot(dir)\n\ts := gateEmbR{root}\n\tf, _ := s.Open(name)\n\tzr := f\n\tmeta := struct{ MetadataUncompressed int }{n}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], nil\n}"},
	}},

	{name: "279: var-bound generic instantiation erasing container taint", desc: "var-bound generic instantiation erasing container taint", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateHuntWrapXP[T any](v T) []T { return []T{v} }\n\nvar gateHuntAliasXP = gateHuntWrapXP[*os.File]\n\nfunc gateVarInst279(name string) ([]byte, bool, error) {\n\tf, err := os.Open(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tfiles := gateHuntAliasXP(f)\n\tzr := files[0]\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "280: positional element of an anonymous struct literal", desc: "positional element of an anonymous struct literal", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateAnonStruct280(name string, f *os.File) ([]byte, bool, error) {\n\tx := struct{ r io.Reader }{f}\n\tzr := x.r\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "281: anonymous struct embedding a file handle", desc: "anonymous struct embedding a file handle", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateAnonRoot281(dir, name string) ([]byte, bool, error) {\n\troot, err := os.OpenRoot(dir)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\ts := struct{ *os.Root }{root}\n\tf, _ := s.Open(name)\n\tzr := f\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "282: var-bound generic instantiation erased to an interface", desc: "var-bound generic instantiation erased to an interface", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateHuntWrapXR[T io.Reader](v T) T { return v }\n\nvar gateHuntAliasXR = gateHuntWrapXR[io.Reader]\n\nfunc gateVarR282(name string, f *os.File) ([]byte, bool, error) {\n\tzr := gateHuntAliasXR(f)\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "283: explicit single instantiation with a func-file arg", desc: "explicit single instantiation with a func-file arg", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateForm283[T any](name string, mk func(string) (T, error)) (T, error) {\n\treturn mk(name)\n}\n\nfunc gateForm283main(name string) ([]byte, bool, error) {\n\tzr, err := gateForm283[io.Reader](name, func(n string) (io.Reader, error) { return os.Open(n) })\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "284: variadic explicit instantiation", desc: "variadic explicit instantiation", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\nfunc gateForm284[T any](name string, mks ...func(string) (T, error)) (T, error) {\n\treturn mks[0](name)\n}\n\nfunc gateForm284main(name string) ([]byte, bool, error) {\n\tzr, err := gateForm284[io.Reader](name, func(n string) (io.Reader, error) { return os.Open(n) })\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "285: elided slice element positional", desc: "elided slice element positional", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateForm285S struct {\n\tfn func(string) (io.Reader, error)\n}\n\nfunc gateForm285main(name string) ([]byte, bool, error) {\n\tfn := func(name string) (io.Reader, error) { return os.Open(name) }\n\ts := []gateForm285S{{fn}}\n\tzr, err := s[0].fn(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "286: map elided element with a keyed literal", desc: "map elided element with a keyed literal", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateForm286S struct {\n\tfn func(string) (io.Reader, error)\n}\n\nfunc gateForm286main(name string) ([]byte, bool, error) {\n\tfn := func(name string) (io.Reader, error) { return os.Open(name) }\n\tm := map[string]gateForm286S{\"a\": {fn}}\n\tzr, err := m[\"a\"].fn(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "287: pointer composite literal", desc: "pointer composite literal", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateForm287A struct {\n\tfn func(string) (io.Reader, error)\n}\n\ntype gateForm287B struct {\n\tr any\n}\n\nfunc gateForm287main(name string) ([]byte, bool, error) {\n\tfn := func(name string) (io.Reader, error) { return os.Open(name) }\n\ts := &gateForm287A{fn}\n\tzr, err := s.fn(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tf, err := os.Open(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tt := &gateForm287B{r: f}\n\tif zr2, ok2 := t.r.(io.Reader); ok2 {\n\t\tzr = zr2\n\t}\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "288: nested and channel elided container elements", desc: "nested and channel elided container elements", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "inject", path: "internal/reader/metadata.go", content: "\"os\""},
		batteryOp{kind: "append", path: "internal/reader/metadata.go", content: "\ntype gateForm288S1 struct {\n\tfn func(string) (io.Reader, error)\n}\n\ntype gateForm288S6 struct {\n\tin []gateForm288S1\n}\n\ntype gateForm288S5 struct {\n\tch any\n}\n\nfunc gateForm288main(name string) ([]byte, bool, error) {\n\tfn := func(name string) (io.Reader, error) { return os.Open(name) }\n\ts := gateForm288S6{in: []gateForm288S1{{fn}}}\n\tzr, err := s.in[0].fn(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tf, err := os.Open(name)\n\tif err != nil {\n\t\treturn nil, false, err\n\t}\n\tch := make(chan *os.File, 1)\n\tch <- f\n\tc := []gateForm288S5{{ch}}\n\tif c0, ok0 := c[0].ch.(chan *os.File); ok0 {\n\t\tzr = <-c0\n\t}\n\tmeta := struct{ MetadataUncompressed int }{4096}\n\tout := make([]byte, int(meta.MetadataUncompressed)+1)\n\tif _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {\n\t\treturn nil, false, err\n\t}\n\treturn out[:int(meta.MetadataUncompressed)], true, nil\n}"},
	}},

	{name: "289: embed import (compile-time database copy)", desc: "embed import (compile-time database copy)", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_embedimp/mut.go", content: "package gatemut_embedimp\n\nimport \"embed\"\n\nvar _ embed.FS"},
	}},

	{name: "290: //go:embed directive with blank embed import", desc: "go:embed directive with blank embed import", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "gatemut_embdir/mut.go", content: "package gatemut_embdir\n\nimport _ \"embed\"\n\n//go:embed probe.db\nvar gatemutDB []byte"},
	}},
	{name: "291: unix.Recvfrom in the mapping owner", desc: "unix.Recvfrom descriptor read in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_recvfrom.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc recvfrom(fd int, b []byte) (int, error) { n, _, err := unix.Recvfrom(fd, b, 0); return n, err }"},
	}},

	{name: "292: unix.Recvmsg in the mapping owner", desc: "unix.Recvmsg descriptor read in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_recvmsg.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc recvmsg(fd int, b []byte) error { _, _, _, _, err := unix.Recvmsg(fd, b, nil, 0); return err }"},
	}},

	{name: "293: unix.Recvmmsg in the mapping owner", desc: "unix.Recvmmsg descriptor read in the mapping owner", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_recvmmsg.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc recvmmsg(fd int, b []byte) (int, error) { return unix.Recvmmsg(fd, b, nil, 0) }"},
	}},

	{name: "294: unix.Sendto in the mapping owner", desc: "unix.Sendto descriptor write in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_sendto.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc sendto(fd int, b []byte) error { return unix.Sendto(fd, b, 0, nil) }"},
	}},

	{name: "295: unix.Sendmsg in the mapping owner", desc: "unix.Sendmsg descriptor write in the mapping owner", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_sendmsg.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc sendmsg(fd int, b []byte) error { return unix.Sendmsg(fd, nil, b, nil, 0) }"},
	}},

	{name: "296: unix.Sendmmsg in the mapping owner", desc: "unix.Sendmmsg descriptor write in the mapping owner", expectFail: true, allowTypeCheck: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_sendmmsg.go", content: "package mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc sendmmsg(fd int, b []byte) (int, error) { return unix.Sendmmsg(fd, b, nil, 0) }"},
	}},

	{name: "297: big.Int.SetBytes of a full page", desc: "new(big.Int).SetBytes(page) copies a complete mapped page into owned limbs", expectFail: true, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bigint.go", content: "package reader\n\nimport \"math/big\"\n\nfunc bigIntProbe(r *ImmutableReader, pgno uint32) (*big.Int, error) {\n\tpage, err := r.page(pgno)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn new(big.Int).SetBytes(page), nil\n}"},
	}},

	{name: "298: benign bounded big.Int.SetBytes record stays legal", desc: "SetBytes of a bounded record is not a complete-page copy", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_bigintbounded.go", content: "package reader\n\nimport \"math/big\"\n\nfunc bigIntBounded(rec []byte) *big.Int { return new(big.Int).SetBytes(rec[48:112]) }"},
	}},

	{name: "299: unix.Ftruncate outside the mapping owner", desc: "raw lifecycle growth syscall outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Ftruncate", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ftruncate.go", content: "package reader\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateFtruncate(fd int, n int64) error { return unix.Ftruncate(fd, n) }"},
	}},

	{name: "300: unix.Msync outside the mapping owner", desc: "raw lifecycle flush syscall outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Msync", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_msync.go", content: "package reader\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateMsync(b []byte) error { return unix.Msync(b, unix.MS_SYNC) }"},
	}},

	{name: "301: unix.Fsync outside the mapping owner", desc: "raw lifecycle durability syscall outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Fsync", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fsync.go", content: "package reader\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateFsync(fd int) error { return unix.Fsync(fd) }"},
	}},

	{name: "302: os.File.Sync outside the mapping owner", desc: "file durability method outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Sync", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_filesync.go", content: "package reader\n\nimport \"os\"\n\nvar gateSyncFile *os.File\n\nfunc gateFileSync() error { return gateSyncFile.Sync() }"},
	}},

	{name: "303: os.File.Truncate outside the mapping owner", desc: "file truncation method outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Truncate", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_filetruncate.go", content: "package reader\n\nimport \"os\"\n\nvar gateTruncFile *os.File\n\nfunc gateFileTruncate(n int64) error { return gateTruncFile.Truncate(n) }"},
	}},

	{name: "304: benign lifecycle syscall inside the mapping owner", desc: "raw lifecycle syscalls stay legal in the mapping owner", expectFail: false, ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/mapping/gatemut_lifecycle_benign.go", content: "//go:build linux\npackage mapping\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateLifecycleBenign(fd int, b []byte, n int64, path string) error {\n\tif err := unix.Fsync(fd); err != nil {\n\t\treturn err\n\t}\n\tif err := unix.Fdatasync(fd); err != nil {\n\t\treturn err\n\t}\n\tif err := unix.Ftruncate(fd, n); err != nil {\n\t\treturn err\n\t}\n\tif err := unix.Msync(b, unix.MS_SYNC); err != nil {\n\t\treturn err\n\t}\n\tunix.Sync()\n\treturn unix.Truncate(path, n)\n}"},
	}},

	{name: "305: unix.Fdatasync outside the mapping owner", desc: "raw fdatasync durability syscall outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Fdatasync", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_fdatasync.go", content: "package reader\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateFdatasync(fd int) error { return unix.Fdatasync(fd) }"},
	}},

	{name: "306: unix.Syncfs outside the mapping owner", desc: "raw syncfs durability syscall outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Syncfs", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_syncfs.go", content: "package reader\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateSyncfs(fd int) error { return unix.Syncfs(fd) }"},
	}},

	{name: "307: unix.Sync outside the mapping owner", desc: "global writeback sync outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Sync", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_sync.go", content: "package reader\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateSync() { unix.Sync() }"},
	}},

	{name: "308: unix.Truncate outside the mapping owner", desc: "path-based truncation outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Truncate", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_truncate.go", content: "package reader\n\nimport \"golang.org/x/sys/unix\"\n\nfunc gateTruncate(path string, n int64) error { return unix.Truncate(path, n) }"},
	}},

	{name: "309: os.Truncate outside the mapping owner", desc: "stdlib path-based truncation outside the mapping owner", expectFail: true, expectRule: "banned content-transfer selector .Truncate", ops: []batteryOp{
		batteryOp{kind: "create", path: "internal/reader/gatemut_ostruncate.go", content: "package reader\n\nimport \"os\"\n\nfunc gateOsTruncate(path string, n int64) error { return os.Truncate(path, n) }"},
	}},
}
