// Command gatescan enforces the v4 Go mmap-only content-transfer ban over
// production sources (the AST half of v4/go/check-import-graph.sh).
//
// It parses every non-test .go file under the walk root (only .git is
// skipped; hidden directories are scanned like any other), and rejects
// any .s/.syso object — build tags,
// line wrapping, comments, aliases, and file names are irrelevant to the
// token stream — and reports:
//
//   - banned imports (content-transfer packages) and dot imports;
//   - selector-based transfer calls (.Read/.Write/.Seek families,
//     reflection Call, decoders/encoders, fmt.Fscan*, ...); and
//   - any *os.File-typed value used outside the approved capability
//     surface (mapping lifecycle: Fd/Close/Name/Stat/Sync/Truncate, and
//     consumers in the same package, module-internal packages, or x/sys).
//   - any .s/.syso assembly object (a hand-written syscall body the
//     token stream cannot see; only a bodyless Go declaration or
//     //go:linkname can link it, and both fail closed).
//
// The three in-memory inflater nodes in internal/reader/metadata.go are
// exempted as exact call shapes (their source text is compared
// literally) and only when their receiver/arguments are not file-tainted.
// A file-backed receiver reproducing the same text — c.r.Read(p) with
// r *os.File, or io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])
// with zr *os.File — stays visible through the file taint and fails.
//
// The analysis is deliberately type-light: a small syntactic taint tracks
// *os.File values (declarations, parameters, os.Open*/os.Create*
// producers, same-package constructors returning *os.File, struct
// fields, chan elements, func values producing files, and the
// os.Stdin/Stdout/Stderr singletons). That keeps the gate a mechanical
// tripwire with no module dependency beyond the standard library.
// Known residual: a *os.File value exported by a third-party package
// (other than the os std handles enumerated above) is not visible to
// the taint unless the code mentions *os.File textually or moves the
// value through an already-tainted route. Same-module package-level
// producer vars are resolved through the process-wide producer-var
// registry collected from every scanned directory.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const moduleInternalPrefix = "github.com/firehol/iprange/v4/go/internal"

// bannedImports are packages whose API transfers or re-wraps content
// through read/write/seek-equivalent surfaces the SDK must not use, or
// that exist only to move bytes. compress/flate is deliberately absent:
// the metadata inflater reads an in-memory payload.
var bannedImports = map[string]bool{
	"C":           true, // cgo: C.pread etc. would bypass every Go selector ban
	"archive/tar": true, "archive/zip": true,
	"bufio": true, "compress/bzip2": true, "compress/gzip": true,
	"compress/lzw": true, "compress/zlib": true,
	"debug/buildinfo": true, "debug/elf": true, "debug/macho": true,
	"debug/pe": true, "debug/plan9obj": true,
	"encoding/ascii85": true, "encoding/base64": true, "encoding/csv": true,
	"encoding/gob": true, "encoding/json": true, "encoding/xml": true,
	"go/parser": true, "go/scanner": true,
	"html/template": true, "image": true, "image/gif": true,
	"image/jpeg": true, "image/png": true, "io/ioutil": true,
	"log": true, "log/slog": true, "mime/multipart": true,
	"mime/quotedprintable": true, "net/http": true, "os/exec": true,
	"runtime/trace": true, "syscall": true, "text/scanner": true,
	"text/tabwriter": true, "text/template": true,
}

// bannedSelectors are the content-transfer call families. The list is
// deliberately broad: it also covers function aliases, method values,
// reflection Invocation, x/sys descriptor variants, and the subprocess
// escape (dup the descriptor onto stdin, then exec a reader).
var bannedSelectors = map[string]bool{
	"Call": true, "CallSlice": true, "Clonefile": true, "Clonefileat": true,
	"Copy": true, "CopyBuffer": true, "CopyFS": true,
	"CopyFileRange": true, "CopyN": true, "Decode": true, "Dup": true, "Dup2": true, "Dup3": true,
	"Encode": true, "Exec": true, "FcntlInt": true, "ForkExec": true,
	"IoctlFileClone": true, "IoctlFileCloneRange": true, "IoctlFileDedupeRange": true,
	"Tee": true, "Vmsplice": true,
	"Fprint": true, "Fprintf": true, "Fprintln": true, "Fscan": true,
	"Fscanf": true, "Fscanln": true, "Method": true, "MethodByName": true,
	"NewDecoder": true, "NewWriter": true, "Peek": true, "Pread": true,
	"Preadv": true, "Print": true, "Printf": true, "Println": true,
	"Preadv2": true, "Pwrite": true, "Pwritev": true, "Pwritev2": true,
	"RawSyscall": true, "RawSyscall6": true, "RawSyscall9": true,
	"RawSyscallN": true, "RawSyscallNoError": true, "Read": true,
	"ReadAll": true,
	"ReadAt":  true, "ReadAtLeast": true, "ReadByte": true, "ReadFile": true,
	"ReadFrom": true, "ReadFull": true, "ReadLine": true, "ReadRune": true,
	"ReadString": true, "Readv": true, "Scan": true, "Scanf": true,
	"Scanln": true, "Seek": true, "Sendfile": true, "Splice": true,
	"StartProcess": true,
	"Syscall":      true, "Syscall6": true, "Syscall9": true, "SyscallN": true,
	"SyscallNoError": true,
	"Write":          true, "WriteAt": true, "WriteByte": true, "WriteFile": true,
	"WriteRune": true, "WriteString": true, "WriteTo": true, "Writev": true,
}

// fileProducers are stdlib functions that return *os.File; the value lists
// the result positions that are files (error results are never files).
var fileProducers = map[string][]int{
	"os.Create":     {0},
	"os.CreateTemp": {0},
	"os.NewFile":    {0},
	"os.Open":       {0},
	"os.OpenFile":   {0},
	"os.OpenInRoot": {0},
	"os.OpenRoot":   {0},
	"os.Pipe":       {0, 1},
}

// approvedFileMethods are the only methods allowed on a file-tainted
// value: mapping lifecycle and identity operations. Anything else
// (Read/Write/Seek/... and any future transfer) fails the gate.
var approvedFileMethods = map[string]bool{
	"Chmod": true, "Chown": true, "Close": true, "Fd": true,
	"Name": true, "Stat": true, "Sync": true, "Truncate": true,
}

// structs maps, per package directory, type name -> field name -> type
// text. funcs maps same-package function names to their result type
// texts. Both are collected syntactically.
// qualifiedAliases keys cross-package alias spellings
// ("mapping.MappingFile" -> "*os.File") collected from every scanned
// directory. The scanner builds one pkgInfo per directory, so a type
// argument declared as an alias in another package must resolve
// through this process-wide registry.
var qualifiedAliases = map[string]string{}

// pkgAliasesByDir keys directory-relative package paths
// ("internal/mapping") to the type aliases that directory declares,
// so an alias imported under a renamed local identifier (import mm
// ".../internal/mapping"; mm.MappingFile) resolves through the
// importing file's own import map.
var pkgAliasesByDir = map[string]map[string]string{}

// qualifiedProducerVars keys clause-qualified package-level variables
// whose value is a func-file producer ("format.OpenRoot" for a
// package-level var OpenRoot = os.OpenRoot, a declared func type with
// file-bearing results, or a closure returning files). The per-package
// taint registry is visible only inside the declaring directory; an
// importing same-module package resolves the producer claim through
// this process-wide registry instead of re-deriving it from source it
// cannot see.
var qualifiedProducerVars = map[string]bool{}

// pkgProducerVarsByDir keys directory-relative package paths
// ("internal/format") to the producer-var names that directory
// declares, so a call under a renamed import qualifier (import fm
// ".../internal/format"; fm.OpenRoot) resolves through the importing
// file's own import map, mirroring pkgAliasesByDir.
var pkgProducerVarsByDir = map[string]map[string]bool{}

// currentImports snapshots the import map of the file being analyzed.
// The scanner is single-threaded and processes one file at a time;
// the snapshot lets alias resolution translate the local qualifier
// of a cross-package type argument back to its package path.
var currentImports map[string]string

// remoteStructs/remoteMethods/remoteMethodFull/remoteEmbedded mirror
// every scanned directory's struct and interface metadata under both
// the bare type name and the clause-qualified spelling ("S28" and
// "mapping.S28"). Each parseDir merges the mirrors into its local
// pkgInfo, so a type argument spelled with an import qualifier
// (mm.S28) resolves to the other package's struct, and method calls
// on the bound value resolve their declared results. Local
// declarations always win on bare-name collisions because the local
// collectPkgInfo overwrites merged entries.
var remoteStructs = map[string]map[string]string{}
var remoteMethods = map[string][]string{}
var remoteMethodFull = map[string]string{}
var remoteEmbedded = map[string][]string{}
var remoteRecvTypeParams = map[string][]string{}
var remoteIfaceParams = map[string][]string{}

type pkgInfo struct {
	structs        map[string]map[string]string
	funcs          map[string][]string
	methods        map[string][]string // structName.method -> result type texts
	aliases        map[string]string   // type-alias name -> underlying type text
	retFuncs       map[string]bool     // named funcs whose body returns a tainted *os.File value
	retMethods     map[string]bool     // structName.method whose body returns a tainted *os.File value
	retFuncFiles   map[string]bool     // named funcs whose body returns a func-file value
	retMethodFiles map[string]bool     // structName.method whose body returns a func-file value
	funcTypeParams map[string][]string // generic func name -> type-parameter names
	funcParams     map[string][]string // generic func name -> parameter type texts
	methodFull     map[string]string   // "struct.method" -> full signature text
	recvTypeParams map[string][]string // "struct.method" -> receiver type-parameter names
	pkgVars        map[string]bool     // package-level variable names
	definedTo      map[string]string   // defined type (type a b) -> underlying type name
	embedded       map[string][]string // struct name -> embedded (promoted) type names
	ifaceParams    map[string][]string // generic interface name -> type parameter names
	varTypes       map[string]string   // variable name -> declared type text
}

// makeZip writes a module-cache zip of dir with every entry named
// base+"/"+rel (the module zip layout: golang.org/x/sys@v0.35.0/...). The
// self-test uses it to build the evil module cache and file-proxy
// fixtures without external tools; the produced zip hashes to the same
// h1: value as the dir (Hash1 over the same names and contents).
func makeZip(base, dir, zipPath string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	prefix := filepath.ToSlash(base)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(prefix + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// dirHash returns the h1: dirhash of dir with every file name prefixed
// by base (the module-cache name, e.g. golang.org/x/sys@v0.35.0, which
// the caller derives by trimming the module cache root; it contains
// slashes and is not a filesystem basename). The result is exactly the
// official module zip sum from go.sum (the Hash1 scheme of
// golang.org/x/mod/sumdb/dirhash): one "sha256:<hex>  <name>" line per
// regular file, sorted by name, hashed as one blob. The module cache
// checkout of golang.org/x/sys must hash to the pinned official value; a
// poisoned cache that keeps the allowed path while smuggling files
// changes this hash.
func dirHash(base, dir string) (string, error) {
	type entry struct{ name, rel string }
	prefix := filepath.ToSlash(base)
	var entries []entry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(rel)
		entries = append(entries, entry{name: prefix + "/" + slashed, rel: slashed})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	h := sha256.New()
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(e.rel)))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%x  %s\n", sum, e.name)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

func main() {
	root := "."
	if len(os.Args) > 1 && os.Args[1] == "--makezip" {
		if len(os.Args) != 5 {
			fmt.Fprintln(os.Stderr, "gatescan: --makezip requires a base name, a source directory, and a zip output path")
			os.Exit(2)
		}
		if err := makeZip(os.Args[2], os.Args[3], os.Args[4]); err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: --makezip: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--dirhash" {
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "gatescan: --dirhash requires a module-cache base name and one directory argument")
			os.Exit(2)
		}
		hash, err := dirHash(os.Args[2], os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: --dirhash %s: %v\n", os.Args[3], err)
			os.Exit(1)
		}
		fmt.Println(hash)
		return
	}
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	files := []string{}
	asmFiles := []string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
			return nil
		}
		if d.IsDir() {
			if path != root && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), "_test.go") {
			files = append(files, path)
		}
		switch lower := strings.ToLower(d.Name()); {
		case strings.HasSuffix(lower, ".s"), strings.HasSuffix(lower, ".syso"):
			asmFiles = append(asmFiles, path)
		}
		return nil
	})

	// Assembly objects are rejected outright: a syscall body written in
	// assembly is invisible to the token scan. Only a bodyless Go
	// declaration or //go:linkname (both banned) could link it, so this
	// is defense in depth, not a second choke point.
	fail := len(asmFiles) > 0
	for _, path := range asmFiles {
		fmt.Fprintf(os.Stderr, "gatescan: %s: assembly object rejected (syscall body invisible to the source scan)\n", path)
	}
	byDir := map[string][]string{}
	for _, f := range files {
		dir := filepath.Dir(f)
		byDir[dir] = append(byDir[dir], f)
	}
	for _, dir := range sortedKeys(byDir) {
		list := byDir[dir]
		info, srcs, fses, parsed := parseDir(list)
		// Package-level declarations are visible to every file of the
		// package, so the scanner shares one package taint across the
		// directory before running any file.
		shared := newTaints()
		for _, f := range list {
			collectPkgTaints(parsed[f], shared, info, dir)
		}
		// Pre-scan every named function and method: a body whose return
		// statement yields a file-tainted value is a file producer even
		// when the declared result type hides the file behind an
		// interface. The pre-scan runs before any runFile so call sites
		// in every file of the directory see the complete producer set;
		// it iterates to a fixpoint so helper chains compose.
		prescanFileProducers(list, parsed, shared, info)
		for _, f := range list {
			if err := runFile(f, parsed[f], fses[f], srcs[f], info, shared); err != nil {
				fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
				fail = true
			}
		}
	}
	if fail {
		os.Exit(1)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func parseDir(paths []string) (pkgInfo, map[string][]byte, map[string]*token.FileSet, map[string]*ast.File) {
	info := pkgInfo{structs: map[string]map[string]string{}, funcs: map[string][]string{}, methods: map[string][]string{}, aliases: map[string]string{}, retFuncs: map[string]bool{}, retMethods: map[string]bool{}, retFuncFiles: map[string]bool{}, retMethodFiles: map[string]bool{}, funcTypeParams: map[string][]string{}, funcParams: map[string][]string{}, methodFull: map[string]string{}, recvTypeParams: map[string][]string{}, pkgVars: map[string]bool{}, definedTo: map[string]string{}, embedded: map[string][]string{}, ifaceParams: map[string][]string{}, varTypes: map[string]string{}}
	// Cross-package struct and method metadata collected from
	// previously parsed directories resolves generic type arguments
	// that spell a struct with an import qualifier (mm.S28): the
	// remote mirrors are merged before the local files register, so
	// local declarations always win on a bare-name collision.
	for k, v := range remoteStructs {
		if _, exists := info.structs[k]; !exists {
			info.structs[k] = v
		}
	}
	for k, v := range remoteMethods {
		if _, exists := info.methods[k]; !exists {
			info.methods[k] = v
		}
	}
	for k, v := range remoteMethodFull {
		if _, exists := info.methodFull[k]; !exists {
			info.methodFull[k] = v
		}
	}
	for k, v := range remoteEmbedded {
		if _, exists := info.embedded[k]; !exists {
			info.embedded[k] = v
		}
	}
	for k, v := range remoteRecvTypeParams {
		if _, exists := info.recvTypeParams[k]; !exists {
			info.recvTypeParams[k] = v
		}
	}
	for k, v := range remoteIfaceParams {
		if _, exists := info.ifaceParams[k]; !exists {
			info.ifaceParams[k] = v
		}
	}
	srcs := map[string][]byte{}
	fses := map[string]*token.FileSet{}
	parsed := map[string]*ast.File{}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
			continue
		}
		if bytes.Contains(src, []byte("//go:linkname")) {
			fmt.Fprintf(os.Stderr, "gatescan: %s uses //go:linkname (raw-symbol aliasing bypasses the import and selector bans)\n", p)
			os.Exit(1)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gate scan failed to parse %s: %v\n", p, err)
			os.Exit(1)
		}
		srcs[p], fses[p], parsed[p] = src, fset, file
		collectPkgInfo(p, file, &info)
	}
	if len(paths) > 0 {
		dir := filepath.Dir(paths[0])
		clause := ""
		if pf, ok := parsed[paths[0]]; ok {
			clause = pf.Name.Name
		}
		finalizeDirAliases(dir, clause, info)
	}
	return info, srcs, fses, parsed
}

// finalizeDirAliases resolves every cross-package alias/defined entry
// of one directory to its final type text now that the whole directory
// is parsed and information is complete. During collection a defined
// type over an alias (type D A; type A = func() *os.File) keeps the
// bare first-hop text (the alias may be declared after the defined
// type), and an alias over a defined func type stops at the defined
// name; the fixpoint closes both chains so a qualified spelling from
// another package (mm.D, mm.E) expands to the func text instead of a
// name that is meaningless outside the defining directory.
func finalizeDirAliases(dir, clause string, info pkgInfo) {
	aliases, ok := pkgAliasesByDir[dir]
	if !ok {
		return
	}
	// Iterate to a true fixpoint: with a single pass the outcome of a
	// long chain depended on map iteration order (an entry processed
	// before its intermediate hops were finalized stopped at a bare
	// hop name), which made long qualified chains nondeterministically
	// bypass the gate. Repeating until no entry changes makes the
	// final registered text order-independent.
	for changed := true; changed; {
		changed = false
		for name, text := range aliases {
			nt := resolveDirText(text, aliases, info.definedTo)
			if nt == text {
				continue
			}
			aliases[name] = nt
			if clause != "" {
				qualifiedAliases[clause+"."+name] = nt
			}
			changed = true
		}
	}
}

// resolveDirText follows alias and defined-type hops within one
// directory's registries to a fixpoint, yielding the final underlying
// type text of the named entry. Self-entries (a struct name registered
// as itself for qualifier translation) and alias cycles (which Go
// forbids, but the mechanical scanner still guards) terminate via the
// self-hop guard and the iteration cap.
func resolveDirText(text string, aliases map[string]string, definedTo map[string]string) string {
	for i := 0; i < 64; i++ {
		prev := text
		if a, ok := aliases[text]; ok && a != text {
			text = a
			continue
		}
		if n, ok := definedTo[text]; ok && n != text {
			text = n
			continue
		}
		if text == prev {
			break
		}
	}
	return text
}

func collectPkgInfo(path string, f *ast.File, info *pkgInfo) {
	pkgDir := filepath.Dir(path)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if ts.Assign.IsValid() {
				// type X = T: record the alias so file-taint checks
				// resolve it instead of being blind to the name. The
				// qualified key (package clause name) also resolves
				// cross-package spellings (mapping.MappingFile) used
				// as generic type arguments or declared types.
				info.aliases[ts.Name.Name] = exprText(ts.Type)
				qualifiedAliases[f.Name.Name+"."+ts.Name.Name] = exprText(ts.Type)
				if pkgAliasesByDir[pkgDir] == nil {
					pkgAliasesByDir[pkgDir] = map[string]string{}
				}
				pkgAliasesByDir[pkgDir][ts.Name.Name] = exprText(ts.Type)
				continue
			}
			if it, ok := ts.Type.(*ast.InterfaceType); ok {
				// Interfaces register as pseudo-structs: the method
				// signatures (and their result metadata) resolve
				// interface-valued receivers - a type-switch default
				// clause, a constructor return, a struct field - so
				// v.get() classifies when the declared signature is a
				// file producer (get() *os.File, get() func() *os.File).
				var ifaceTps []string
				if ts.TypeParams != nil {
					for _, fld := range ts.TypeParams.List {
						for _, n := range fld.Names {
							ifaceTps = append(ifaceTps, n.Name)
						}
					}
				}
				if len(ifaceTps) > 0 {
					info.ifaceParams[ts.Name.Name] = ifaceTps
					remoteIfaceParams[ts.Name.Name] = ifaceTps
					remoteIfaceParams[f.Name.Name+"."+ts.Name.Name] = ifaceTps
				}
				fields := map[string]string{}
				for _, m := range it.Methods.List {
					if len(m.Names) == 0 {
						// Embedded interface: record the promoted type
						// so method resolution walks the embedding
						// chain exactly like a struct's embedded
						// fields; without it, x.get() on an interface
						// that embeds a file-producing interface
						// resolves no method at all.
						info.embedded[ts.Name.Name] = append(info.embedded[ts.Name.Name], exprText(m.Type))
						continue
					}
					t := exprText(m.Type)
					for _, name := range m.Names {
						fields[name.Name] = t
						if ft, ok2 := m.Type.(*ast.FuncType); ok2 {
							info.methods[ts.Name.Name+"."+name.Name] = collectResults(ft)
							// A generic interface instantiated at an
							// embedding site (IBaseGN[func() *os.File])
							// promotes the method with the type
							// parameter substituted; record the
							// interface's own type parameters under
							// the same key space genericMethodResults
							// uses for generic receivers.
							if len(ifaceTps) > 0 {
								info.recvTypeParams[ts.Name.Name+"."+name.Name] = ifaceTps
							}
						}
					}
				}
				info.structs[ts.Name.Name] = fields
				remoteStructs[ts.Name.Name] = fields
				remoteStructs[f.Name.Name+"."+ts.Name.Name] = fields
				// A renamed-import qualifier (mm.IMapBase) must reduce
				// to the bare interface name before method lookup: the
				// clause-qualified mirror only matches the unrenamed
				// package clause, so register the self-entry exactly
				// like the struct branch does.
				if pkgDir != "" {
					if pkgAliasesByDir[pkgDir] == nil {
						pkgAliasesByDir[pkgDir] = map[string]string{}
					}
					pkgAliasesByDir[pkgDir][ts.Name.Name] = ts.Name.Name
				}
				qualifiedAliases[f.Name.Name+"."+ts.Name.Name] = ts.Name.Name
				for mname, mres := range info.methods {
					if strings.HasPrefix(mname, ts.Name.Name+".") {
						remoteMethods[mname] = mres
						remoteMethods[f.Name.Name+"."+mname] = mres
						if tps, okT := info.recvTypeParams[mname]; okT {
							remoteRecvTypeParams[mname] = tps
							full := f.Name.Name + "." + mname
							remoteRecvTypeParams[full] = tps
						}
					}
				}
				for _, emb := range info.embedded[ts.Name.Name] {
					remoteEmbedded[ts.Name.Name] = append(remoteEmbedded[ts.Name.Name], emb)
					remoteEmbedded[f.Name.Name+"."+ts.Name.Name] = append(remoteEmbedded[f.Name.Name+"."+ts.Name.Name], emb)
				}
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				// A defined func type (type F func() *os.File) is still a
				// file producer whenever a value of that type is called;
				// register it like an alias so funcTypeResultsFile and
				// resolveTypeText expand it.
				if ft, ok := ts.Type.(*ast.FuncType); ok {
					info.aliases[ts.Name.Name] = exprText(ft)
					// A defined func type is also reachable through the
					// package qualifier (mm.F with F func() *os.File):
					// register it like an alias so renamed and
					// clause-name references expand to the func text.
					if pkgDir != "" {
						if pkgAliasesByDir[pkgDir] == nil {
							pkgAliasesByDir[pkgDir] = map[string]string{}
						}
						pkgAliasesByDir[pkgDir][ts.Name.Name] = exprText(ft)
					}
					qualifiedAliases[f.Name.Name+"."+ts.Name.Name] = exprText(ft)
				} else if id, ok := ts.Type.(*ast.Ident); ok {
					// A defined type (type b a) chains to its underlying
					// name so receivers and instances resolve to the
					// base struct.
					info.definedTo[ts.Name.Name] = id.Name
					// Cross-package spellings (mm.b) must expand the
					// chain too; the per-directory fixpoint resolves
					// the first hop (and any alias hop) to the final
					// type text once the whole directory is parsed.
					if pkgDir != "" {
						if pkgAliasesByDir[pkgDir] == nil {
							pkgAliasesByDir[pkgDir] = map[string]string{}
						}
						pkgAliasesByDir[pkgDir][ts.Name.Name] = id.Name
					}
					qualifiedAliases[f.Name.Name+"."+ts.Name.Name] = id.Name
				} else {
					// A defined type over a qualified or complex
					// underlying (type x mm.A, type x []*os.File,
					// type x map[string]F) chains to its underlying
					// type text so type arguments, receivers, and
					// instances resolve through it.
					info.definedTo[ts.Name.Name] = exprText(ts.Type)
					if pkgDir != "" {
						if pkgAliasesByDir[pkgDir] == nil {
							pkgAliasesByDir[pkgDir] = map[string]string{}
						}
						pkgAliasesByDir[pkgDir][ts.Name.Name] = exprText(ts.Type)
					}
					qualifiedAliases[f.Name.Name+"."+ts.Name.Name] = exprText(ts.Type)
				}
				continue
			}
			fields := map[string]string{}
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					// Embedded field: record the promoted type so method
					// resolution can walk the embedding chain.
					info.embedded[ts.Name.Name] = append(info.embedded[ts.Name.Name], exprText(field.Type))
					continue
				}
				t := exprText(field.Type)
				for _, name := range field.Names {
					fields[name.Name] = t
				}
			}
			info.structs[ts.Name.Name] = fields
			// Cross-package struct spellings (mm.S28 as a generic type
			// argument, composite literal, or embedded name) must
			// resolve: mirror the struct into the remote registries
			// under the bare and clause-qualified keys, and register
			// the qualified name in the alias registry as itself so
			// the local import qualifier translates to the bare name
			// (structs carry no func text, so the self-entry only
			// serves the name translation).
			remoteStructs[ts.Name.Name] = fields
			remoteStructs[f.Name.Name+"."+ts.Name.Name] = fields
			if pkgDir != "" {
				if pkgAliasesByDir[pkgDir] == nil {
					pkgAliasesByDir[pkgDir] = map[string]string{}
				}
				pkgAliasesByDir[pkgDir][ts.Name.Name] = ts.Name.Name
			}
			qualifiedAliases[f.Name.Name+"."+ts.Name.Name] = ts.Name.Name
			for _, emb := range info.embedded[ts.Name.Name] {
				remoteEmbedded[ts.Name.Name] = append(remoteEmbedded[ts.Name.Name], emb)
				remoteEmbedded[f.Name.Name+"."+ts.Name.Name] = append(remoteEmbedded[f.Name.Name+"."+ts.Name.Name], emb)
			}
		}
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Type.Results == nil {
			continue
		}
		if fd.Recv != nil {
			// Methods are never package funcs, whether or not the
			// receiver names its variable: anonymous-receiver methods
			// key under the receiver type like any other.
			_, recvStruct := receiverOf(fd, *info)
			if recvStruct != "" {
				mkey := recvStruct + "." + fd.Name.Name
				info.methods[mkey] = collectResults(fd.Type)
				info.methodFull[mkey] = exprText(fd.Type)
				remoteMethods[mkey] = collectResults(fd.Type)
				remoteMethods[f.Name.Name+"."+mkey] = collectResults(fd.Type)
				remoteMethodFull[mkey] = exprText(fd.Type)
				remoteMethodFull[f.Name.Name+"."+mkey] = exprText(fd.Type)
				// Generic receivers (gR[T] mk() []T) record their type
				// parameters so call sites through gR[*gsG] substitute
				// the instantiation into the raw results.
				if tps := parseBracketArgs(exprText(fd.Recv.List[0].Type)); tps != nil {
					info.recvTypeParams[mkey] = tps
					remoteRecvTypeParams[mkey] = tps
					remoteRecvTypeParams[f.Name.Name+"."+mkey] = tps
				}
				if res := resolveTypeText(recvStruct, *info); res != recvStruct {
					info.methods[res+"."+fd.Name.Name] = collectResults(fd.Type)
					info.methodFull[res+"."+fd.Name.Name] = exprText(fd.Type)
				}
			}
			continue
		}
		info.funcs[fd.Name.Name] = collectResults(fd.Type)
		var tps []string
		if fd.Type.TypeParams != nil {
			for _, fld := range fd.Type.TypeParams.List {
				for _, n := range fld.Names {
					tps = append(tps, n.Name)
				}
			}
		}
		if len(tps) > 0 {
			info.funcTypeParams[fd.Name.Name] = tps
			info.funcParams[fd.Name.Name] = collectParams(fd.Type)
		}
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok {
				for _, n := range vs.Names {
					if n.Name != "_" {
						info.pkgVars[n.Name] = true
						if vs.Type != nil {
							info.varTypes[n.Name] = exprText(vs.Type)
						}
					}
				}
			}
		}
	}
}

// collectParams returns the parameter type texts of a function type,
// one entry per declared parameter position.
func collectParams(ft *ast.FuncType) []string {
	var params []string
	if ft.Params == nil {
		return params
	}
	for _, fld := range ft.Params.List {
		t := exprText(fld.Type)
		for range fld.Names {
			params = append(params, t)
		}
		if len(fld.Names) == 0 {
			params = append(params, t)
		}
	}
	return params
}

func collectResults(ft *ast.FuncType) []string {
	var results []string
	if ft.Results == nil {
		return results
	}
	for _, r := range ft.Results.List {
		t := exprText(r.Type)
		for range r.Names {
			results = append(results, t)
		}
		if len(r.Names) == 0 {
			results = append(results, t)
		}
	}
	return results
}

func exprText(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprText(t.X)
	case *ast.ArrayType:
		return "[]" + exprText(t.Elt)
	case *ast.SelectorExpr:
		return exprText(t.X) + "." + t.Sel.Name
	case *ast.ChanType:
		switch t.Dir {
		case ast.SEND:
			return "chan<- " + exprText(t.Value)
		case ast.RECV:
			return "<-chan " + exprText(t.Value)
		}
		return "chan " + exprText(t.Value)
	case *ast.MapType:
		return "map[" + exprText(t.Key) + "]" + exprText(t.Value)
	case *ast.Ellipsis:
		return "..." + exprText(t.Elt)
	case *ast.ParenExpr:
		return "(" + exprText(t.X) + ")"
	case *ast.IndexExpr:
		return exprText(t.X) + "[" + exprText(t.Index) + "]"
	case *ast.IndexListExpr:
		parts := []string{}
		for _, ix := range t.Indices {
			parts = append(parts, exprText(ix))
		}
		return exprText(t.X) + "[" + strings.Join(parts, ", ") + "]"
	case *ast.FuncType:
		parts := []string{}
		if t.Params != nil {
			for _, fld := range t.Params.List {
				parts = append(parts, exprText(fld.Type))
			}
		}
		out := "func(" + strings.Join(parts, ", ") + ")"
		if t.Results != nil && len(t.Results.List) > 0 {
			rps := []string{}
			for _, fld := range t.Results.List {
				rps = append(rps, exprText(fld.Type))
			}
			out += " " + strings.Join(rps, ", ")
		}
		return out
	}
	return ""
}

// taints is the per-scope syntactic *os.File state.
type taints struct {
	file         map[string]bool    // identifiers holding *os.File
	container    map[string]bool    // identifiers holding []*os.File or a struct with file fields
	struc        map[string]string  // identifiers holding a same-package struct value: name -> type name
	chanFile     map[string]bool    // identifiers holding chan *os.File (make, declared, or send-marked)
	chanFuncFile map[string]bool    // identifiers holding chan of func() *os.File
	fieldTaint   map[string]kind    // expr.field = value kind from an assignment of a tainted value
	elementTaint map[string]kind    // container expr -> element kind (map/slice element reads and writes)
	funcFile     map[string]bool    // identifiers holding func() *os.File (closures and declared func types)
	retFile      map[token.Pos]bool // closure/function nodes whose body returns a file-tainted value
}

func newTaints() *taints {
	return &taints{file: map[string]bool{}, container: map[string]bool{}, struc: map[string]string{}, chanFile: map[string]bool{}, chanFuncFile: map[string]bool{}, fieldTaint: map[string]kind{}, elementTaint: map[string]kind{}, funcFile: map[string]bool{}, retFile: map[token.Pos]bool{}}
}

func cloneTaints(t *taints) *taints {
	c := newTaints()
	for k, v := range t.file {
		c.file[k] = v
	}
	for k, v := range t.container {
		c.container[k] = v
	}
	for k, v := range t.struc {
		c.struc[k] = v
	}
	for k, v := range t.chanFile {
		c.chanFile[k] = v
	}
	for k, v := range t.chanFuncFile {
		c.chanFuncFile[k] = v
	}
	for k, v := range t.fieldTaint {
		c.fieldTaint[k] = v
	}
	for k, v := range t.elementTaint {
		c.elementTaint[k] = v
	}
	for k, v := range t.funcFile {
		c.funcFile[k] = v
	}
	for k, v := range t.retFile {
		c.retFile[k] = v
	}
	return c
}

// collectPkgTaints registers package-level var declarations (type-only
// struct instances, chan *os.File vars, and producer-bound values) into a
// shared package taint so every file of the package sees them.
func collectPkgTaints(f *ast.File, pkg *taints, info pkgInfo, dir string) {
	imports := map[string]string{}
	currentImports = imports
	for _, imp := range f.Imports {
		pathText := strings.Trim(imp.Path.Value, `"`)
		name := pathText
		if imp.Name != nil && imp.Name.Name != "." && imp.Name.Name != "_" {
			name = imp.Name.Name
		}
		imports[pathText] = pathText
		imports[name] = pathText
		currentImports = imports
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gd.Tok != token.VAR && gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			var cls kind
			if vs.Type != nil {
				cls = classifyType(vs.Type, info)
			}
			if vs.Type != nil {
				if k := elementKindShape(exprText(vs.Type), info); k != kindNone {
					for _, n := range vs.Names {
						if n.Name != "_" {
							pkg.elementTaint[n.Name] = k
						}
					}
				}
			}
			for i, name := range vs.Names {
				if len(vs.Values) > i {
					cls = classify(vs.Values[i], pkg, info, imports)
					if c, ok := classifyStruct(vs.Values[i], pkg, info); ok {
						pkg.struc[name.Name] = c
					}
					if funcTypeResultsFile(vs.Values[i], info) {
						pkg.funcFile[name.Name] = true
					}
					// A declared func type whose results are files is a
					// producer even when the variable has an initializer
					// (var f func(string) (*os.File, error) = os.Open):
					// the declared result type is the stable contract and
					// the initializer may be an untracked stdlib value.
					if vs.Type != nil && funcTypeResultsFile(vs.Type, info) {
						pkg.funcFile[name.Name] = true
					}
					// A stdlib producer bound as a value (var f = os.Open)
					// is a func-file: invoking it yields a file even though
					// the stdlib signature is invisible to the scanner.
					if sel, ok := vs.Values[i].(*ast.SelectorExpr); ok {
						if selp, ok2 := sel.X.(*ast.Ident); ok2 && imports[selp.Name] == "os" {
							if _, found := fileProducers["os."+sel.Sel.Name]; found {
								pkg.funcFile[name.Name] = true
							}
						}
					}
				} else if vs.Type != nil {
					// type-only package var: register struct instances so
					// field reads in any file resolve the taint, and
					// func-typed values (incl. aliases) as producers.
					if base, ok := structBase(vs.Type, info); ok {
						pkg.struc[name.Name] = base
					}
					if funcTypeResultsFile(vs.Type, info) {
						pkg.funcFile[name.Name] = true
					}
				}
				applyKind(pkg, name.Name, cls)
				// A package-level producer var is visible to every file
				// of the declaring directory through pkg.funcFile; a
				// caller in another package of the module has no view
				// of that taint, so the same registration is mirrored
				// process-wide.
				if pkg.funcFile[name.Name] {
					registerPkgProducerVar(f.Name.Name, dir, name.Name)
				}
			}
		}
	}
}

// registerPkgProducerVar records a package-level func-file var under
// the declaring clause name and directory, so a same-module caller
// (format.OpenRoot, or fm.OpenRoot under a renamed import) resolves
// the producer claim through producerCall's process-wide registry.
func registerPkgProducerVar(clause, dir, name string) {
	if clause == "" {
		return
	}
	qualifiedProducerVars[clause+"."+name] = true
	if pkgProducerVarsByDir[dir] == nil {
		pkgProducerVarsByDir[dir] = map[string]bool{}
	}
	pkgProducerVarsByDir[dir][name] = true
}

// runFile applies the rules to one production file.
// fileImports builds the import lookup for one file: path text and local
// name both map to the canonical path so `import fsp "os"` cannot dodge
// a package check. Dot and blank imports are skipped (dot imports are
// separately rejected), and banned content-transfer imports are reported.
func fileImports(f *ast.File, reporter *reporter) map[string]string {
	imports := map[string]string{}
	for _, imp := range f.Imports {
		pathText := strings.Trim(imp.Path.Value, `"`)
		name := pathText
		if imp.Name != nil && imp.Name.Name != "." {
			name = imp.Name.Name
		} else if imp.Name != nil && imp.Name.Name == "." {
			if reporter != nil {
				reporter.fail("dot-import of " + pathText)
			}
			continue
		}
		if imp.Name != nil && imp.Name.Name == "_" {
			continue // blank import: no names leak
		}
		imports[pathText] = pathText
		imports[name] = pathText
		if bannedImports[pathText] {
			if reporter != nil {
				reporter.fail("banned content-transfer import " + pathText)
			}
		}
	}
	return imports
}

func runFile(path string, f *ast.File, fset *token.FileSet, src []byte, info pkgInfo, shared *taints) error {
	reporter := &reporter{path: path}
	imports := fileImports(f, reporter)
	currentImports = imports

	pkg := cloneTaints(shared)

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				// A bodyless Go function can only be implemented by
				// assembly (or //go:linkname, banned above): both attach
				// a syscall body the source gate cannot see. The
				// .s/.syso walk rejects the assembly object, and this
				// check rejects the declaration itself instead of
				// crashing on it.
				reporter.fail("bodyless function declaration " + d.Name.Name + " (assembly stub)")
				continue
			}
			st := cloneTaints(pkg)
			addSignatureTaints(st, d.Recv, info)
			addSignatureTaints(st, d.Type.Params, info)
			prepassStmts(d.Body.List, st, info, imports)
			exempts := findExemptions(d, src, fset, st, info, imports)
			rulesWalk("func "+d.Name.Name, d.Body, st, exempts, imports, info, reporter)
		case *ast.GenDecl:
			if d.Tok == token.VAR || d.Tok == token.CONST {
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						walkRulesNode(vs, pkg, nil, imports, info, reporter)
					}
				}
			}
		default:
			walkRulesNode(d, pkg, nil, imports, info, reporter)
		}
	}
	return reporter.err()
}

type kind int

const (
	kindNone kind = iota
	kindFile
	kindContainer
	kindChanFile
	kindChanFuncFile
	kindFuncFile
)

// callResultsFuncFile reports whether e is a same-package call whose
// every declared result position is a func type producing *os.File
// (through alias and defined-func-type expansion), so a value returned
// through a helper keeps its file-producer taint. Method receivers
// resolve through the struct instance, not the receiver variable name.
func callResultsFuncFile(e ast.Expr, st *taints, info pkgInfo) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	fun := unwrapParen(call.Fun)
	var results []string
	switch f := fun.(type) {
	case *ast.Ident:
		results = info.funcs[f.Name]
	case *ast.SelectorExpr:
		// The receiver may be a nested field chain (mhv.inner.mk()),
		// not just a plain identifier; resolveStruct walks the chain.
		if structName, ok2 := resolveStruct(unwrapParen(f.X), st, info); ok2 {
			results, _ = methodMeta(structName, f.Sel.Name, info)
		}
	}
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		rt := resolveTypeText(r, info)
		if !strings.HasPrefix(rt, "func(") || !mentionsFileType(rt) {
			return false
		}
	}
	return true
}

// callResults resolves a call's declared result type texts: plain
// functions by name, methods through the struct instance, with generic
// instantiation and receiver type-parameter substitution applied when
// the call carries them.
func callResults(e ast.Expr, st *taints, info pkgInfo) []string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil
	}
	if gcr, okG := genericCallResults(call, st, info); okG {
		return gcr
	}
	fun := unwrapParen(call.Fun)
	switch f := fun.(type) {
	case *ast.Ident:
		return info.funcs[f.Name]
	case *ast.SelectorExpr:
		if structName, ok2 := resolveStruct(unwrapParen(f.X), st, info); ok2 {
			if mres, ok3 := genericMethodResults(f, st, info); ok3 {
				return mres
			}
			// methodMeta's bool reports body-marked file producers
			// (retMethods), not whether the method exists: declared
			// results are authoritative whenever the resolver finds
			// any, so a mixed method (get() (func() *os.File, error))
			// keeps its func-file position kind instead of losing
			// every result to a false ok.
			if mres, _ := methodMeta(structName, f.Sel.Name, info); len(mres) > 0 {
				return mres
			}
		}
	}
	return nil
}

// callResultKinds reports the taint classes present among a call's
// declared results: func-file, chan-of-func-file, and chan-of-file,
// resolved per position through alias and defined-type chains. Mixed
// multi-result calls (getFn() (func() *os.File, error)) keep their
// producer class even when other result positions are plain values.
func callResultKinds(e ast.Expr, st *taints, info pkgInfo) (funcFile, chanFuncFile, chanFile bool) {
	for _, r := range callResults(e, st, info) {
		rt := resolveTaintType(r, info)
		switch {
		case funcTextFile(rt):
			funcFile = true
		case chanElemFuncFile(rt, info):
			chanFuncFile = true
		case chanElemFile(rt, info):
			chanFile = true
		}
	}
	return funcFile, chanFuncFile, chanFile
}

// callResultKindAt returns the taint kind of one declared result
// position (func-file, chan-of-func-file, chan-of-file), or kindNone.
func callResultKindAt(e ast.Expr, index int, st *taints, info pkgInfo) kind {
	results := callResults(e, st, info)
	if index < 0 || index >= len(results) {
		return kindNone
	}
	rt := resolveTaintType(results[index], info)
	switch {
	case funcTextFile(rt):
		return kindFuncFile
	case chanElemFuncFile(rt, info):
		return kindChanFuncFile
	case chanElemFile(rt, info):
		return kindChanFile
	}
	return kindNone
}

// callResultsChanFuncFile reports whether e is a same-package call whose
// declared results are channels whose element is a func type producing
// *os.File (chan F with F = func() *os.File), so a channel returned
// through a helper keeps its chan-of-func taint.
func callResultsChanFuncFile(e ast.Expr, st *taints, info pkgInfo) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	fun := unwrapParen(call.Fun)
	var results []string
	switch f := fun.(type) {
	case *ast.Ident:
		results = info.funcs[f.Name]
	case *ast.SelectorExpr:
		if structName, ok2 := resolveStruct(unwrapParen(f.X), st, info); ok2 {
			results, _ = methodMeta(structName, f.Sel.Name, info)
		}
	}
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !chanElemFuncFile(resolveTypeText(r, info), info) {
			return false
		}
	}
	return true
}

// unwrapParen strips parentheses around an expression so call and
// selector matching sees (getFile)() and ((f).Read)(p) the same way.
func unwrapParen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// chanElemFile reports whether a type text is a channel whose element
// type resolves to *os.File, directly or through nested channels and
// aliases (chan chan *os.File and chan C with C = chan *os.File both
// carry files). Depth is bounded so cyclic alias text cannot loop.
func chanElemFile(text string, info pkgInfo) bool {
	return chanElemFileDepth(text, info, 0)
}

func chanElemFileDepth(text string, info pkgInfo, depth int) bool {
	if depth > 8 {
		return false
	}
	for _, p := range []string{"chan<- ", "<-chan ", "chan "} {
		if strings.HasPrefix(text, p) {
			el := resolveTypeText(strings.TrimSpace(strings.TrimPrefix(text, p)), info)
			if isFileTyped(el) {
				return true
			}
			return chanElemFileDepth(el, info, depth+1)
		}
	}
	return false
}

// funcTextFile reports whether a resolved type text is a func type
// returning *os.File in any position.
func funcTextFile(text string) bool {
	return strings.HasPrefix(text, "func(") && mentionsFileType(text)
}

// chanElemFuncFile reports whether a type text is a channel whose
// element is a func type producing *os.File (chan F with
// F = func() *os.File), directly or through nested channels.
func chanElemFuncFile(text string, info pkgInfo) bool {
	return chanElemFuncFileDepth(text, info, 0)
}

func chanElemFuncFileDepth(text string, info pkgInfo, depth int) bool {
	if depth > 8 {
		return false
	}
	for _, p := range []string{"chan<- ", "<-chan ", "chan "} {
		if strings.HasPrefix(text, p) {
			el := resolveTypeText(strings.TrimSpace(strings.TrimPrefix(text, p)), info)
			if funcTextFile(el) {
				return true
			}
			return chanElemFuncFileDepth(el, info, depth+1)
		}
	}
	return false
}

// aliasLookup resolves an alias name through the same-package alias
// map, the clause-name cross-package registry, or the current file's
// import map (a locally renamed qualifier such as mm.MappingFile).
func aliasLookup(name string, info pkgInfo) (string, bool) {
	if a, ok := info.aliases[name]; ok {
		return a, true
	}
	if a, ok := qualifiedAliases[name]; ok {
		return a, true
	}
	// Cross-package alias spelled with a locally renamed import
	// qualifier: translate the qualifier through the current file's
	// import map, then match the scanned directory of the alias.
	if i := strings.IndexByte(name, '.'); i > 0 && i < len(name)-1 {
		q, rest := name[:i], name[i+1:]
		if spec, ok := currentImports[q]; ok {
			for dir, aliases := range pkgAliasesByDir {
				if !strings.HasSuffix(spec, "/"+dir) && spec != dir {
					continue
				}
				if a, ok := aliases[rest]; ok {
					return a, true
				}
			}
		}
	}
	return "", false
}

// resolveTypeText expands type aliases (bare or pointer-qualified) so a
// `type zr = *os.File` alias is seen as a file type by the taint checks.
func resolveTypeText(text string, info pkgInfo) string {
	for i := 0; i < 8; i++ {
		stripped := strings.TrimPrefix(text, "*")
		if a, ok := aliasLookup(stripped, info); ok {
			text = strings.Repeat("*", len(text)-len(stripped)) + a
			continue
		}
		break
	}
	return text
}

// resolveDefinedType follows defined-type chains (type b a) to the
// underlying type name.
func resolveDefinedType(text string, info pkgInfo) string {
	for i := 0; i < 8; i++ {
		if n, ok := info.definedTo[text]; ok {
			text = n
			continue
		}
		break
	}
	return text
}

// resolveTaintType reduces a type text through alias and defined-type
// chains until stable, so taint comparisons after generic substitution
// use the underlying spelling: a type argument written as an alias
// (type zfA = *os.File; gR[zfA]{}) must compare as *os.File, and a
// container result ([]zfA) must compare as []*os.File.
func resolveTaintType(text string, info pkgInfo) string {
	for i := 0; i < 8; i++ {
		before := text
		text = resolveTypeText(text, info)
		text = resolveDefinedType(text, info)
		if text == before {
			break
		}
	}
	return text
}

// resolveStructName reduces any receiver or instance type spelling to the
// underlying struct name: type aliases, defined-type chains, pointer
// prefixes, and generic instantiations all resolve to the base name. The
// reductions run to a fixpoint so a pointer to a defined type (*d where
// d is defined from a struct) resolves in either order.
func resolveStructName(name string, info pkgInfo) string {
	text := name
	for i := 0; i < 8; i++ {
		prev := text
		text = resolveTypeText(text, info)
		text = resolveDefinedType(text, info)
		text = strings.TrimPrefix(text, "*")
		if j := strings.IndexByte(text, '['); j >= 0 {
			text = text[:j]
		}
		if text == prev {
			break
		}
	}
	return text
}

// methodMeta resolves method metadata on a struct, walking promoted
// (embedded) fields when the direct key misses. Generic parameters are
// threaded down the embedding chain: each frame substitutes its own
// instantiation into the next embedded type text, and the frame that
// declares the method applies its accumulated type arguments to the
// declared results, so a multi-level chain (type InnerL[T] interface{
// Get() T }; type IBaseGL[T] interface{ InnerL[T] }; type IEmb
// interface{ IBaseGL[func() *os.File] }) keeps the substituted file
// shapes instead of leaking the raw type parameter.
func methodMeta(structName, method string, info pkgInfo) ([]string, bool) {
	seen := map[string]bool{}
	var walk func(string, []string, []string) ([]string, bool)
	walk = func(s string, tps, args []string) ([]string, bool) {
		base := resolveStructName(s, info)
		if base == "" || seen[base] {
			return nil, false
		}
		seen[base] = true
		mres := info.methods[base+"."+method]
		ret := info.retMethods[base+"."+method]
		if mres != nil || ret {
			if len(args) > 0 && len(tps) > 0 {
				out := make([]string, len(mres))
				for i, res := range mres {
					out[i] = substituteTypeParams(res, tps, args, info)
				}
				return out, ret
			}
			return mres, ret
		}
		for _, emb := range info.embedded[base] {
			// Substitute the frame's own instantiation into the
			// embedded text before recursing: for a frame whose entry
			// instantiates a generic interface embedding another
			// generic interface (IBaseGL[T] embeds InnerL[T]), the
			// inner entry must read InnerL[func() *os.File] when the
			// outer frame binds func() *os.File.
			subEmb := emb
			if len(args) > 0 && len(tps) > 0 {
				subEmb = substituteTypeParams(emb, tps, args, info)
			}
			embBase := resolveStructName(subEmb, info)
			// The embedded entry may name a defined type over an
			// instantiated generic interface (type D IBaseG[func() *os.File];
			// type IEmb interface{ D }): the brackets live in the defined
			// chain, not in the spelling, so resolve the text before
			// extracting the type arguments.
			nextArgs := parseBracketArgs(resolveTaintType(subEmb, info))
			var nextTps []string
			if ftps, okT := info.ifaceParams[embBase]; okT {
				nextTps = ftps
			} else if len(nextArgs) > 0 {
				// Generic struct frames register receiver parameters
				// per method.
				nextTps = info.recvTypeParams[embBase+"."+method]
			}
			if mm, r := walk(embBase, nextTps, nextArgs); r {
				return mm, r
			} else if len(mm) > 0 {
				// Declared results on a promoted embedded type are authoritative
				// even when the embedded method body was never marked as a file
				// producer: ok only records body-marked claims, so dropping mm
				// here would blind the whole embedding chain.
				return mm, false
			}
		}
		return nil, false
	}
	return walk(structName, nil, nil)
}

// typeSwitchSwitched returns the switched expression of a type-switch
// guard (the x in `switch v := x.(type)` or `switch x.(type)`), or nil.
func typeSwitchSwitched(assign ast.Stmt) ast.Expr {
	switch a := assign.(type) {
	case *ast.AssignStmt:
		if len(a.Rhs) == 1 {
			if ta, ok := a.Rhs[0].(*ast.TypeAssertExpr); ok {
				return ta.X
			}
		}
	case *ast.ExprStmt:
		if ta, ok := a.X.(*ast.TypeAssertExpr); ok {
			return ta.X
		}
	}
	return nil
}

// ifaceImplProducer reports whether a call to method on an
// interface-typed receiver can produce a file through a concrete
// implementation: any same-package struct method whose full signature
// text matches the interface method's is a valid dynamic type, and the
// type-switch default clause can bind any of them, so a producer among
// them taints the call (the interface's own declared signature is
// already covered by methodMeta on the pseudo-struct).
func ifaceImplProducer(structName, method string, info pkgInfo) bool {
	sig, ok := info.structs[structName][method]
	if !ok {
		return false
	}
	for base := range info.structs {
		if base == structName {
			continue
		}
		fsig, ok2 := info.methodFull[base+"."+method]
		if !ok2 || fsig != sig {
			continue
		}
		if info.retMethodFiles[base+"."+method] || info.retMethods[base+"."+method] {
			return true
		}
		mres, _ := methodMeta(base, method, info)
		for _, r := range mres {
			rt := resolveTypeText(r, info)
			if isFileTyped(rt) || funcTextFile(rt) || chanElemFile(rt, info) || chanElemFuncFile(rt, info) {
				return true
			}
		}
	}
	return false
}

// typeSwitchBound returns the identifier bound by a type-switch guard
// (switch zv := x.(type)) or the empty string.
func typeSwitchBound(assign ast.Stmt) string {
	switch a := assign.(type) {
	case *ast.AssignStmt:
		if len(a.Lhs) == 1 {
			if id, ok := a.Lhs[0].(*ast.Ident); ok {
				return id.Name
			}
		}
	case *ast.ExprStmt:
		if as, ok := a.X.(*ast.TypeAssertExpr); ok {
			// switch x.(type) without a bound variable: nothing to taint.
			_ = as
		}
	}
	return ""
}

// funcTypeResultsFile reports whether a declared function type or
// closure literal returns *os.File in any result position. Alias-typed
// function values (type fileFn = func() *os.File) resolve through the
// textual alias expansion first.
func funcTypeResultsFile(e ast.Expr, info pkgInfo) bool {
	txt := resolveTypeText(exprText(e), info)
	if strings.HasPrefix(txt, "func(") && mentionsFileType(txt) {
		return true
	}
	switch t := e.(type) {
	case *ast.FuncType:
		return len(fileResultPositions(collectResults(t))) > 0
	case *ast.FuncLit:
		return len(fileResultPositions(collectResults(t.Type))) > 0
	}
	return false
}

// structBase returns the same-package struct type name behind a declared
// type expression (T, *T), resolving type aliases.
func structBase(t ast.Expr, info pkgInfo) (string, bool) {
	base := resolveStructName(exprText(t), info)
	if _, isStruct := info.structs[base]; isStruct {
		return base, true
	}
	return "", false
}

// elementTypeText strips a map/slice/array wrapper, returning the
// element type text. "" when the type is not a container.
func elementTypeText(text string) string {
	if strings.HasPrefix(text, "map[") {
		if i := strings.LastIndex(text, "]"); i >= 0 && i+1 < len(text) {
			return text[i+1:]
		}
		return ""
	}
	if strings.HasPrefix(text, "[]") {
		return text[2:]
	}
	if strings.HasPrefix(text, "[") {
		if i := strings.Index(text, "]"); i >= 0 && i+1 < len(text) {
			return text[i+1:]
		}
		return ""
	}
	return ""
}

// chanCarrier reports whether e names a channel whose elements are
// files or func-files: an identifier registered as a chan carrier, or
// an expression classifying as a chan carrier. classify maps carrier
// identifiers to kindFile (the call-through semantic), so the
// registries are checked before classify.
func chanCarrier(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) bool {
	if id, ok := e.(*ast.Ident); ok {
		return st.chanFile[id.Name] || st.chanFuncFile[id.Name]
	}
	k := classify(e, st, info, imports)
	return k == kindChanFile || k == kindChanFuncFile
}

// elementReadKind returns the element kind of an index read over base:
// the base's declared type (call result, alias-spelled binding, or a
// deref/star wrapper) is resolved, then the container element shape is
// mapped ([]*os.File and map[K]*os.File elements are files).
func elementReadKind(base ast.Expr, st *taints, info pkgInfo) kind {
	bt, ok := typeOfBase(base, st, info)
	if !ok || bt == "" {
		return kindNone
	}
	rt := resolveTaintType(bt, info)
	if strings.HasPrefix(rt, "*") {
		if isFileTyped(strings.TrimPrefix(rt, "*")) {
			return kindFile
		}
		return kindNone
	}
	return elementKindShape(rt, info)
}

// methodMetaResults returns the declared result types of a method on a
// resolved receiver struct, or nil when the method is unknown.
func methodMetaResults(structName, method string, info pkgInfo) []string {
	res, _ := methodMeta(structName, method, info)
	return res
}

// elementKindShape maps a container type text to its element kind.
func elementKindShape(text string, info pkgInfo) kind {
	el := elementTypeText(text)
	if el == "" {
		return kindNone
	}
	rt := resolveTypeText(el, info)
	switch {
	case isFileTyped(rt):
		return kindFile
	case funcTextFile(rt):
		return kindFuncFile
	case chanElemFuncFile(rt, info):
		return kindChanFuncFile
	case chanElemFile(rt, info):
		return kindChanFile
	case mentionsFileType(rt):
		return kindContainer
	}
	return kindNone
}

// containerElementKindExpr resolves the declared element kind of a
// struct-field container (fb.m of type map[string]F -> kindFuncFile).
func containerElementKindExpr(e ast.Expr, st *taints, info pkgInfo) kind {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return kindNone
	}
	structName, ok := resolveStruct(sel.X, st, info)
	if !ok {
		return kindNone
	}
	return elementKindShape(resolveTypeText(info.structs[structName][sel.Sel.Name], info), info)
}

// classifyType maps a declared type expression to file/container taint.
func classifyType(t ast.Expr, info pkgInfo) kind {
	text := resolveTypeText(exprText(t), info)
	if isFileTyped(text) {
		return kindFile
	}
	if chanElemFile(text, info) {
		return kindChanFile
	}
	if chanElemFuncFile(text, info) {
		return kindChanFuncFile
	}
	if funcTextFile(text) {
		return kindFuncFile
	}
	if mentionsFileType(text) {
		return kindContainer
	}
	return kindNone
}

// classify maps an expression value to file/container/struct taint.
func classify(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) kind {
	switch v := e.(type) {
	case *ast.Ident:
		if st.file[v.Name] {
			return kindFile
		}
		if st.container[v.Name] {
			return kindContainer
		}
		if st.chanFile[v.Name] {
			return kindFile
		}
		if st.chanFuncFile[v.Name] {
			return kindChanFuncFile
		}
		if st.funcFile[v.Name] {
			return kindFuncFile
		}
	case *ast.FuncLit:
		if funcTypeResultsFile(v.Type, info) || st.retFile[v.Pos()] {
			return kindFuncFile
		}
	case *ast.TypeAssertExpr:
		rt := resolveTypeText(exprText(v.Type), info)
		if isFileTyped(rt) {
			return kindFile
		}
		if funcTextFile(rt) {
			return kindFuncFile
		}
		return classify(v.X, st, info, imports)
	case *ast.IndexExpr:
		// A map/slice element read: the element kind comes from a
		// recorded write (m[k] = fn), from the declared element shape
		// of the container (map[string]fileFn), or from a file
		// container ([]*os.File element = file).
		if k := st.elementTaint[exprText(v.X)]; k != kindNone {
			return k
		}
		if k := containerElementKindExpr(v.X, st, info); k != kindNone {
			return k
		}
		if isContainerExpr(v.X, st, info) {
			return kindFile
		}
		if k := elementReadKind(v.X, st, info); k != kindNone {
			return k
		}
		return classify(v.X, st, info, imports)
	case *ast.StarExpr:
		// *p: a deref of a pointer whose resolved base type is a file
		// alias (*zfA where zfA = *os.File) yields a file value even
		// when the pointer binding itself was never tainted.
		if bt, ok := typeOfBase(v.X, st, info); ok && bt != "" {
			if isFileTyped(strings.TrimPrefix(resolveTaintType(bt, info), "*")) {
				return kindFile
			}
		}
		return classify(v.X, st, info, imports)
	case *ast.ParenExpr:
		return classify(v.X, st, info, imports)
	case *ast.UnaryExpr:
		if v.Op == token.ARROW {
			// <-ch: a receive from a chan of files yields a file;
			// from a chan of funcs it yields a func-file.
			k := classify(v.X, st, info, imports)
			if k == kindChanFuncFile {
				return kindFuncFile
			}
			if k == kindChanFile {
				return kindFile
			}
		}
		return classify(v.X, st, info, imports)
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "make" && len(v.Args) == 1 {
			if ct, ok := v.Args[0].(*ast.ChanType); ok {
				if chanElemFile(exprText(ct), info) {
					return kindChanFile
				}
				if chanElemFuncFile(exprText(ct), info) {
					return kindChanFuncFile
				}
			}
		}
		// Declared-result kinds take precedence over the whole-call
		// producer claim: a func-file result (an interface method
		// returning func() *os.File) stays invoke-able instead of
		// being misread as a plain file, while calls whose declared
		// results hide the file (io.ReadCloser behind a tainted body)
		// still fall through to producerCall below.
		if funcF, chanFF, chanF := callResultKinds(v, st, info); funcF {
			return kindFuncFile
		} else if chanFF {
			return kindChanFuncFile
		} else if chanF {
			return kindChanFile
		}
		if _, _, ok := producerCall(v, st, info, imports); ok {
			return kindFile
		}
		if gcr, okG := genericCallResults(v, st, info); okG {
			for _, r := range gcr {
				rt := resolveTypeText(r, info)
				if funcTextFile(rt) {
					return kindFuncFile
				}
				if chanElemFuncFile(rt, info) {
					return kindChanFuncFile
				}
				if chanElemFile(rt, info) {
					return kindChanFile
				}
			}
		}
		// A call whose result is a func-file value: the callee's body
		// returns a funcFile behind an interface, or a generic
		// instantiation binds a type parameter to a funcFile argument.
		if id, ok2 := v.Fun.(*ast.Ident); ok2 {
			if info.retFuncFiles[id.Name] {
				return kindFuncFile
			}
			// fn() where fn holds a chan value (a chan-typed method
			// value bound to a variable): the call yields the channel.
			// chan-of-file mirrors the Ident read semantic (element
			// kind); chan-of-funcFile keeps the carrier kind so a
			// receive afterwards yields the func-file.
			if st.chanFile[id.Name] {
				return kindFile
			}
			if st.chanFuncFile[id.Name] {
				return kindChanFuncFile
			}
			// A same-package function whose declared result is a
			// channel (mkC() chan *os.File) yields the carrier; the
			// binding must register as chan-tainted so a later
			// receive stays tainted.
			for _, r := range info.funcs[id.Name] {
				rt := resolveTypeText(r, info)
				if chanElemFile(rt, info) {
					return kindChanFile
				}
				if chanElemFuncFile(rt, info) {
					return kindChanFuncFile
				}
			}
		}
		if sel, ok2 := v.Fun.(*ast.SelectorExpr); ok2 {
			if structName, ok3 := resolveStruct(sel.X, st, info); ok3 {
				if info.retMethodFiles[structName+"."+sel.Sel.Name] {
					return kindFuncFile
				}
				if ifaceImplProducer(structName, sel.Sel.Name, info) {
					return kindFuncFile
				}
				if mresG, okG := genericMethodResults(sel, st, info); okG {
					for _, r := range mresG {
						rt := resolveTypeText(r, info)
						if funcTextFile(rt) {
							return kindFuncFile
						}
						if chanElemFile(rt, info) {
							return kindChanFile
						}
						if chanElemFuncFile(rt, info) {
							return kindChanFuncFile
						}
					}
				}
				// A method call whose declared result is a channel
				// (h.ch() chan *os.File) yields the channel itself;
				// the binding must register as a chan carrier so a
				// later receive taints the value.
				for _, r := range methodMetaResults(structName, sel.Sel.Name, info) {
					rt := resolveTypeText(r, info)
					if funcTextFile(rt) {
						return kindFuncFile
					}
					if chanElemFile(rt, info) {
						return kindChanFile
					}
					if chanElemFuncFile(rt, info) {
						return kindChanFuncFile
					}
				}
			}
		}
		if genericResultFuncFile(v, st, info, imports) {
			return kindFuncFile
		}
	case *ast.CompositeLit:
		text := exprText(v.Type)
		if mentionsFileType(text) {
			return kindContainer
		}
		// A struct built with a file element (ProcAttr{Files: []*os.File{...}})
		// is a file container even when the composite type name says nothing.
		for _, el := range v.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			if isFileOrContainer(el, st, info, imports) {
				return kindContainer
			}
		}
		base := resolveStructName(exprText(v.Type), info)
		if _, isStruct := info.structs[base]; isStruct {
			return kindNone // struct value; field taint is resolved on access
		}
	case *ast.SelectorExpr:
		switch st.fieldTaint[exprText(v.X)+"."+v.Sel.Name] {
		case kindFile:
			return kindFile
		case kindContainer:
			return kindContainer
		case kindFuncFile:
			return kindFuncFile
		case kindChanFile:
			return kindChanFile
		case kindChanFuncFile:
			return kindChanFuncFile
		}
		if isFileExpr(v, st, info, imports) {
			return kindFile
		}
		if isContainerExpr(v, st, info) {
			return kindContainer
		}
		// A selector into a struct instance: hb.fn where the field type
		// is func() *os.File, or a method value (g.get, mh.inner.mk)
		// whose method is a file producer. Receivers may be nested
		// field chains, resolved like the call path.
		if structName, ok2 := resolveStruct(v.X, st, info); ok2 {
			if ft, ok3 := info.structs[structName][v.Sel.Name]; ok3 {
				if funcTextFile(resolveTypeText(ft, info)) {
					return kindFuncFile
				}
			}
			mres, retM := methodMeta(structName, v.Sel.Name, info)
			if fileResultPositions(mres) != nil || retM {
				return kindFuncFile
			}
			if ifaceImplProducer(structName, v.Sel.Name, info) {
				return kindFuncFile
			}
			if mresG, okG := genericMethodResults(v, st, info); okG {
				if fileResultPositions(mresG) != nil {
					return kindFuncFile
				}
				for _, r := range mresG {
					rt := resolveTypeText(r, info)
					if funcTextFile(rt) {
						return kindFuncFile
					}
					if chanElemFile(rt, info) {
						return kindChanFile
					}
					if chanElemFuncFile(rt, info) {
						return kindChanFuncFile
					}
				}
			}
			for _, r := range mres {
				rt := resolveTypeText(r, info)
				if funcTextFile(rt) {
					return kindFuncFile
				}
				if chanElemFile(rt, info) {
					return kindChanFile
				}
				if chanElemFuncFile(rt, info) {
					return kindChanFuncFile
				}
			}
		}
	}
	return kindNone
}

func applyKind(st *taints, name string, k kind) {
	switch k {
	case kindFile:
		st.file[name] = true
	case kindContainer:
		st.container[name] = true
	case kindChanFile:
		st.chanFile[name] = true
	case kindChanFuncFile:
		st.chanFuncFile[name] = true
	case kindFuncFile:
		st.funcFile[name] = true
	}
}

// producerCall reports whether e is a call producing *os.File: a stdlib
// producer, or a same-package function whose result type is *os.File.
// producerCall returns a call whose results include *os.File plus the
// result positions that are files. Same-package functions and methods are
// matched by their collected result type texts.
func producerCall(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) (*ast.CallExpr, []int, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, nil, false
	}
	fun := unwrapParen(call.Fun)
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		if pkg, ok := sel.X.(*ast.Ident); ok {
			// Resolve the package by local alias so an aliased
			// `import fsp "os"` cannot dodge the producer taint.
			path := imports[pkg.Name]
			if path == "os" {
				if pos, found := fileProducers["os."+sel.Sel.Name]; found {
					return call, pos, true
				}
			}
			if pos, found := fileProducers[pkg.Name+"."+sel.Sel.Name]; found {
				return call, pos, true
			}
			// A package-level func-file var exported by a same-module
			// package (var OpenRoot = os.OpenRoot in internal/format,
			// invoked as format.OpenRoot from internal/mapping): the
			// declaring directory's taint registry is invisible here, so
			// the process-wide producer-var registry resolves the claim.
			// A renamed qualifier translates through the import map to
			// the exact declaring directory; a plain import keys the
			// full module path rather than the bare clause, so the
			// clause-qualified registry is the fallback there (clause
			// names are unique across the scanned module).
			if producerVarByImportPath(path, sel.Sel.Name) ||
				(path == "" && qualifiedProducerVars[pkg.Name+"."+sel.Sel.Name]) {
				return call, []int{0}, true
			}
		}
		// A struct field whose runtime value is a func-file (assigned
		// through a tainted closure or method value) is a producer
		// even when the declared field type hides the file behind an
		// interface.
		if st.fieldTaint[exprText(sel.X)+"."+sel.Sel.Name] == kindFuncFile {
			return call, []int{0}, true
		}
		// Same-package method returning *os.File (e.g. an accessor), or
		// whose body returns a tainted value behind an interface.
		if structName, found := resolveStruct(sel.X, st, info); found {
			mres, retM := methodMeta(structName, sel.Sel.Name, info)
			if pos := fileResultPositions(mres); pos != nil {
				return call, pos, true
			}
			if retM {
				return call, []int{0}, true
			}
			// hb.fn() where the struct field type is func() *os.File.
			// A declared method is not a field: interface methods are
			// stored as pseudo-fields with their signature text, and
			// claiming their func-file results as raw file positions
			// would taint bindings as plain files, so a later
			// invocation of the bound func would lose the taint. The
			// declared-result path (callResults/classify) keeps the
			// func-file kind for methods.
			if _, isMethod := info.methods[structName+"."+sel.Sel.Name]; !isMethod {
				if ft, okf := info.structs[structName][sel.Sel.Name]; okf {
					if funcTextFile(resolveTypeText(ft, info)) {
						return call, []int{0}, true
					}
				}
			}
			// An interface-typed receiver whose declaration (or any
			// signature-identical implementation) produces a file: the
			// dynamic type is unknowable at the call, so the producer
			// union taints the call.
			if ifaceImplProducer(structName, sel.Sel.Name, info) {
				return call, []int{0}, true
			}
			// A generically instantiated receiver
			// (rr := &gR[*os.File]{}; rr.mk() T) produces the file
			// after the receiver's type arguments are substituted.
			if mresG, okG := genericMethodResults(sel, st, info); okG {
				// A func-typed generic method result (T bound to
				// func() *os.File) must stay a func-file, not be
				// claimed as a direct file: classify's own mresG
				// loop yields kindFuncFile and applyKind records
				// the binding in st.funcFile, so a later call of
				// the bound func keeps the file taint. Claiming it
				// here as a file position made the invocation lose
				// the taint and let a file-backed zr slip through
				// the io.ReadFull exemption.
				if pos := fileResultPositions(mresG); pos != nil {
					return call, pos, true
				}
			}
		}
	}
	if id, ok := fun.(*ast.Ident); ok {
		if pos := fileResultPositions(info.funcs[id.Name]); pos != nil {
			return call, pos, true
		}
		if info.retFuncs[id.Name] {
			return call, []int{0}, true
		}
		if st.funcFile[id.Name] {
			return call, []int{0}, true
		}
		// A single-argument conversion through a type alias of *os.File
		// (type zr = *os.File; zr(f)) keeps the file taint.
		if len(call.Args) == 1 && isFileTyped(resolveTypeText(id.Name, info)) {
			return call, []int{0}, true
		}
		// A generic instantiation (idf[T any](f T) T called with a
		// file argument) makes the matching result positions files.
		if pos := genericParamFilePositions(call, info, st, imports); pos != nil {
			return call, pos, true
		}
	}
	if gcr, okG := genericCallResults(call, st, info); okG {
		// Explicit instantiations and inferred generic calls: a
		// type-parameter result bound to *os.File is a producer
		// (mkT[*os.File](), mkT2(&gsG{}) after substitution).
		if pos := fileResultPositions(gcr); pos != nil {
			return call, pos, true
		}
	}
	if ix, ok := fun.(*ast.IndexExpr); ok {
		// m[k]() and s[0](): a map/slice element that is a func-file
		// value; calling it yields the file.
		if classify(ix, st, info, imports) == kindFuncFile {
			return call, []int{0}, true
		}
	}
	if inner, ok := fun.(*ast.CallExpr); ok {
		// zb.mk()() and useDef(getDef2)(): the callee is itself a call
		// whose value is a func returning *os.File; invoking it yields
		// a file at result position zero.
		if callResultsFuncFile(inner, st, info) {
			return call, []int{0}, true
		}
		// fn()() where fn is a variable holding a func-file value (a
		// method value bound to a name, or a helper returning one):
		// the inner call yields a funcFile, the outer yields the file.
		if id, ok2 := unwrapParen(inner.Fun).(*ast.Ident); ok2 {
			if st.funcFile[id.Name] || info.retFuncFiles[id.Name] {
				return call, []int{0}, true
			}
		}
		if classify(inner, st, info, imports) == kindFuncFile {
			return call, []int{0}, true
		}
	}
	if ue, ok := fun.(*ast.UnaryExpr); ok && ue.Op == token.ARROW {
		// (<-ch)(): a receive whose value is a funcFile, invoked
		// immediately; calling it yields the file.
		if classify(ue, st, info, imports) == kindFuncFile {
			return call, []int{0}, true
		}
	}
	if ta, ok := fun.(*ast.TypeAssertExpr); ok {
		// (getFn().(func() *os.File))(): the asserted value is a func
		// producing a file; invoking it yields the file.
		if funcTextFile(resolveTypeText(exprText(ta.Type), info)) {
			return call, []int{0}, true
		}
	}
	if fl, ok := fun.(*ast.FuncLit); ok {
		if pos := fileResultPositions(collectResults(fl.Type)); pos != nil {
			return call, pos, true
		}
		// A closure whose declared result type hides the file behind an
		// interface is still a producer when its body returns a tainted
		// value; every declared result position is then file-tainted.
		if st.retFile[fl.Pos()] {
			pos := make([]int, len(collectResults(fl.Type)))
			for i := range pos {
				pos[i] = i
			}
			return call, pos, true
		}
	}
	return nil, nil, false
}

// positionsOf returns the result positions whose type text is want, or nil.
func positionsOf(want string, results []string) []int {
	var pos []int
	for i, r := range results {
		if r == want {
			pos = append(pos, i)
		}
	}
	return pos
}

// isFileTyped reports whether a value type text is a file-bearing
// handle: *os.File, or *os.Root whose Open/OpenFile/Create methods
// produce files. Root handles carry the same taint as files, so every
// Root method outside the approved lifecycle surface fails closed.
// methodExprFileType reports whether e is a method expression whose
// receiver type is a file-bearing handle: (T).M, (*T).M, or T.M where T
// resolves — through same-package, qualified, and renamed-import alias
// registries — to *os.File or *os.Root. A method expression binds the
// method as a value and takes the receiver as an explicit first
// argument; the receiver node is a type expression, so it never carries
// value taint and the value-position checks cannot see it.
func methodExprFileType(e ast.Expr, info pkgInfo) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	x := sel.X
	if p, ok := x.(*ast.ParenExpr); ok {
		x = p.X
	}
	resolved := resolveTypeText(exprText(x), info)
	if isFileTyped(resolved) {
		return resolved
	}
	return ""
}

// producerVarByImportPath resolves a same-module package-level
// producer var by the import path of its declaring directory. The
// directory registry is keyed by the scanner's relative directory
// ("internal/format"); the call-site qualifier is translated through
// the file's import map first, so a renamed import still resolves.
func producerVarByImportPath(spec, name string) bool {
	for dir, vars := range pkgProducerVarsByDir {
		if !strings.HasSuffix(spec, "/"+dir) && spec != dir {
			continue
		}
		if vars[name] {
			return true
		}
	}
	return false
}

func isFileTyped(t string) bool {
	return t == "*os.File" || t == "*os.Root"
}

// mentionsFileType reports whether a type text mentions a file-bearing
// handle spelling (struct fields, containers, func/chan element types).
func mentionsFileType(t string) bool {
	return strings.Contains(t, "*os.File") || strings.Contains(t, "*os.Root")
}

// fileResultPositions returns the result positions whose resolved type
// is a file-bearing handle (*os.File or *os.Root).
func fileResultPositions(results []string) []int {
	var pos []int
	for i, r := range results {
		if isFileTyped(r) {
			pos = append(pos, i)
		}
	}
	return pos
}

// isFileExpr reports whether expr names a *os.File value: a tainted
// identifier, a struct-field access whose field is *os.File, or a
// producer call.
func isFileExpr(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return st.file[v.Name]
	case *ast.SelectorExpr:
		if st.fieldTaint[exprText(v.X)+"."+v.Sel.Name] == kindFile {
			return true
		}
		// os.Stdin/Stdout/Stderr are process-wide *os.File singletons.
		if id, ok := v.X.(*ast.Ident); ok && imports[id.Name] == "os" {
			if v.Sel.Name == "Stdin" || v.Sel.Name == "Stdout" || v.Sel.Name == "Stderr" {
				return true
			}
		}
		structName, ok := resolveStruct(v.X, st, info)
		if !ok {
			return false
		}
		return isFileTyped(resolveTypeText(info.structs[structName][v.Sel.Name], info))
	case *ast.CallExpr:
		_, _, ok := producerCall(v, st, info, imports)
		return ok
	case *ast.TypeAssertExpr:
		if isFileTyped(resolveTypeText(exprText(v.Type), info)) {
			return true
		}
		return isFileExpr(v.X, st, info, imports)
	case *ast.IndexExpr:
		if isContainerExpr(v.X, st, info) {
			return true
		}
		if k := elementReadKind(v.X, st, info); k == kindFile || k == kindContainer {
			return true
		}
		return false
	case *ast.StarExpr:
		if bt, ok := typeOfBase(v.X, st, info); ok && bt != "" {
			if isFileTyped(strings.TrimPrefix(resolveTaintType(bt, info), "*")) {
				return true
			}
		}
		return isFileExpr(v.X, st, info, imports)
	case *ast.ParenExpr:
		return isFileExpr(v.X, st, info, imports)
	}
	return false
}

func isContainerExpr(e ast.Expr, st *taints, info pkgInfo) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return st.container[v.Name]
	case *ast.SelectorExpr:
		if st.fieldTaint[exprText(v.X)+"."+v.Sel.Name] == kindContainer {
			return true
		}
		structName, ok := resolveStruct(v.X, st, info)
		if !ok {
			return false
		}
		return mentionsFileType(resolveTypeText(info.structs[structName][v.Sel.Name], info))
	case *ast.CompositeLit:
		return mentionsFileType(exprText(v.Type))
	case *ast.StarExpr:
		return isContainerExpr(v.X, st, info)
	case *ast.ParenExpr:
		return isContainerExpr(v.X, st, info)
	}
	return false
}

// isFileOrContainer is the argument-taint test: a file value, a
// container value, or a composite literal that textually or transitively
// holds files (ProcAttr{Files: []*os.File{...}}).
func isFileOrContainer(e ast.Expr, st *taints, info pkgInfo, imports map[string]string) bool {
	if isFileExpr(e, st, info, imports) || isContainerExpr(e, st, info) {
		return true
	}
	switch v := e.(type) {
	case *ast.CompositeLit:
		if mentionsFileType(exprText(v.Type)) {
			return true
		}
		for _, el := range v.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			if isFileOrContainer(el, st, info, imports) {
				return true
			}
		}
	case *ast.CallExpr:
		_, _, ok := producerCall(v, st, info, imports)
		return ok
	case *ast.UnaryExpr:
		if v.Op == token.ARROW {
			// <-c: the received value of a chan-of-file carrier is
			// itself a file, even in return/argument positions.
			if chanCarrier(v.X, st, info, imports) {
				return true
			}
		}
		return isFileOrContainer(v.X, st, info, imports)
	case *ast.ParenExpr:
		return isFileOrContainer(v.X, st, info, imports)
	case *ast.IndexExpr:
		if isContainerExpr(v.X, st, info) || isFileOrContainer(v.X, st, info, imports) {
			return true
		}
		if k := elementReadKind(v.X, st, info); k == kindFile || k == kindContainer {
			return true
		}
		return false
	case *ast.TypeAssertExpr:
		if isFileTyped(resolveTypeText(exprText(v.Type), info)) {
			return true
		}
		return isFileOrContainer(v.X, st, info, imports)
	}
	return false
}

// resolveStruct resolves an expression to a same-package struct type
// name: tainted-struct identifiers, struct return values, and struct
// composite literals.
// prescanFileProducers marks named functions and methods whose bodies
// return file-tainted values as producers. It runs to a fixpoint (up to
// 8 passes) so chains like deep() -> mid() -> os.Pipe resolve even when
// the declaration order is not topological.
func prescanFileProducers(list []string, parsed map[string]*ast.File, shared *taints, info pkgInfo) {
	for pass := 0; pass < 8; pass++ {
		added := 0
		for _, f := range list {
			imp := fileImports(parsed[f], nil)
			currentImports = imp
			for _, decl := range parsed[f].Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fst := cloneTaints(shared)
				addSignatureTaints(fst, fd.Recv, info)
				addSignatureTaints(fst, fd.Type.Params, info)
				prepassStmts(fd.Body.List, fst, info, imp)
				// Sends into package-level channels are shared state:
				// a send in one function taints the channel for every
				// function that later receives from it.
				for k := range fst.chanFuncFile {
					if info.pkgVars[k] {
						shared.chanFuncFile[k] = true
					}
				}
				for k := range fst.chanFile {
					if info.pkgVars[k] {
						shared.chanFile[k] = true
					}
				}
				// Field writes on package-level struct instances are
				// shared state too: init() filling fb.fn with a
				// file-producing closure taints the field for every
				// function (and file) that reads it later.
				for k, kv := range fst.fieldTaint {
					if pkgVarRoot(k, info) {
						shared.fieldTaint[k] = kv
					}
				}
				for k, kv := range fst.elementTaint {
					if pkgVarRoot(k, info) {
						shared.elementTaint[k] = kv
					}
				}
				if _, recvStruct := receiverOf(fd, info); recvStruct != "" {
					key := recvStruct + "." + fd.Name.Name
					if returnsFileIn(fd.Body, fst, info, imp) && !info.retMethods[key] {
						info.retMethods[key] = true
						added++
					}
					if returnsFuncFileIn(fd.Body, fst, info, imp) && !info.retMethodFiles[key] {
						info.retMethodFiles[key] = true
						added++
					}
				} else {
					if returnsFileIn(fd.Body, fst, info, imp) && !info.retFuncs[fd.Name.Name] {
						info.retFuncs[fd.Name.Name] = true
						added++
					}
					if returnsFuncFileIn(fd.Body, fst, info, imp) && !info.retFuncFiles[fd.Name.Name] {
						info.retFuncFiles[fd.Name.Name] = true
						added++
					}
				}
			}
		}
		if added == 0 {
			return
		}
	}
}

// returnsFileIn reports whether any return statement of the function
// body (not inside nested closures) yields a file or file-container
// tainted value.
func returnsFileIn(body *ast.BlockStmt, st *taints, info pkgInfo, imports map[string]string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.FuncLit:
			return false // closure returns do not mark the enclosing func
		case *ast.ReturnStmt:
			for _, res := range v.Results {
				if isFileOrContainer(res, st, info, imports) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// returnsFuncFileIn reports whether any return statement of the
// function body (not inside nested closures) yields a func-file value:
// a function or method value that, when called, produces a *os.File.
func returnsFuncFileIn(body *ast.BlockStmt, st *taints, info pkgInfo, imports map[string]string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.FuncLit:
			return false // closure returns do not mark the enclosing func
		case *ast.ReturnStmt:
			for _, res := range v.Results {
				if classify(res, st, info, imports) == kindFuncFile {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// explicitGenericCall identifies an explicitly instantiated generic
// call callee (mkGen[*gsG] or mkGen[*gsG, int]) and returns the
// function name and the provided type-argument texts.
func explicitGenericCall(fun ast.Expr) (string, []string, bool) {
	switch f := fun.(type) {
	case *ast.IndexExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name, []string{exprText(f.Index)}, true
		}
	case *ast.IndexListExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			args := make([]string, 0, len(f.Indices))
			for _, ix := range f.Indices {
				args = append(args, exprText(ix))
			}
			return id.Name, args, true
		}
	}
	return "", nil, false
}

// substituteTypeParams replaces every type-parameter identifier in a
// declared type text with the explicit instantiation argument at the
// same position: mkGen[*gsG]() binds []T to []*gsG before recording.
// Token boundaries are respected so T2 is never touched when T is the
// parameter. Type arguments and the final result are reduced through
// alias/defined-type chains, so an argument spelled as an alias
// (type zfA = *os.File; gR[zfA]{}) still binds the file spelling.
func substituteTypeParams(text string, tps, args []string, info pkgInfo) string {
	if len(args) == 0 {
		return resolveTaintType(text, info)
	}
	var b strings.Builder
	start := 0
	for i := 0; i <= len(text); i++ {
		if i == len(text) || !(text[i] == '_' || text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z' || text[i] >= '0' && text[i] <= '9') {
			if i > start {
				tok := text[start:i]
				for pi, tp := range tps {
					if tok == tp && pi < len(args) {
						tok = resolveTaintType(args[pi], info)
						break
					}
				}
				b.WriteString(tok)
			}
			if i < len(text) {
				b.WriteByte(text[i])
			}
			start = i + 1
		}
	}
	return resolveTaintType(b.String(), info)
}

// genericSubstitutedResults returns the declared result types of an
// explicitly instantiated generic call with the type parameters
// replaced by the call's type arguments (mkGen[*gsG]() []T -> []*gsG).
func genericSubstitutedResults(fun ast.Expr, info pkgInfo) []string {
	name, args, ok := explicitGenericCall(fun)
	if !ok {
		return nil
	}
	results := info.funcs[name]
	if len(results) == 0 {
		return nil
	}
	tps := info.funcTypeParams[name]
	if len(tps) == 0 {
		return nil
	}
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = substituteTypeParams(r, tps, args, info)
	}
	return out
}

// parseBracketArgs extracts the type arguments of an instantiated type
// text: "gR[*gsG, int]" -> ["*gsG", "int"]; returns nil without
// brackets. Nesting (gR[[]T], map[K]V) is respected.
func parseBracketArgs(text string) []string {
	i := strings.IndexByte(text, '[')
	if i < 0 || !strings.HasSuffix(text, "]") {
		return nil
	}
	inner := text[i+1 : len(text)-1]
	var args []string
	depth := 0
	start := 0
	for j := 0; j <= len(inner); j++ {
		if j == len(inner) {
			if start < j {
				args = append(args, strings.TrimSpace(inner[start:j]))
			}
			break
		}
		switch inner[j] {
		case '[', '(', '{':
			depth++
		case ']', ')', '}':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(inner[start:j]))
				start = j + 1
			}
		}
	}
	return args
}

// genericMethodResults resolves the declared result types of a method
// call on a generically instantiated receiver, substituting the
// receiver's type arguments: rr := gR[*gsG]{}; rr.mk() binds T to
// *gsG in the raw []T result. Embedded promotion carries the embedded
// field's own instantiation text (type hE struct{ gR[*gsG] }).
func genericMethodResults(sel *ast.SelectorExpr, st *taints, info pkgInfo) ([]string, bool) {
	base, ok := resolveStruct(sel.X, st, info)
	if !ok {
		return nil, false
	}
	if tps, ok2 := info.recvTypeParams[base+"."+sel.Sel.Name]; ok2 {
		args := parseBracketArgs(receiverArgText(sel.X, st, info))
		if len(args) < len(tps) {
			return nil, false
		}
		results := info.methods[base+"."+sel.Sel.Name]
		out := make([]string, len(results))
		for i, r := range results {
			out[i] = substituteTypeParams(r, tps, args, info)
		}
		return out, true
	}
	// Embedded promotion: the embedded field may instantiate a generic
	// receiver (gS[func() *os.File]) or a generic interface at any depth
	// of the embedding chain; walk the chain substituting each frame's
	// arguments into the next frame's type text, exactly like methodMeta.
	seen := map[string]bool{}
	var walk func(string, []string, []string) ([]string, bool)
	walk = func(s string, tps, args []string) ([]string, bool) {
		embBase := resolveStructName(s, info)
		if embBase == "" || seen[embBase] {
			return nil, false
		}
		seen[embBase] = true
		var ftps []string
		if tps != nil {
			ftps = tps
		} else if ft, ok := info.ifaceParams[embBase]; ok {
			ftps = ft
		} else {
			ftps = info.recvTypeParams[embBase+"."+sel.Sel.Name]
		}
		if mres := info.methods[embBase+"."+sel.Sel.Name]; mres != nil {
			if len(args) > 0 && len(ftps) > 0 {
				if len(args) != len(ftps) {
					return nil, false
				}
				out := make([]string, len(mres))
				for i, r := range mres {
					out[i] = substituteTypeParams(r, ftps, args, info)
				}
				return out, true
			}
			return mres, true
		}
		for _, emb := range info.embedded[embBase] {
			subEmb := emb
			if len(args) > 0 && len(ftps) > 0 {
				subEmb = substituteTypeParams(emb, ftps, args, info)
			}
			nextArgs := parseBracketArgs(resolveTaintType(subEmb, info))
			if mm, ok := walk(subEmb, nil, nextArgs); ok {
				return mm, true
			}
		}
		return nil, false
	}
	for _, emb := range info.embedded[base] {
		// The argument list parsed here is the instantiation carried by
		// the embedded entry itself; it must reach the declaring frame
		// through the walk (the direct-hit substitution applies it).
		nextArgs := parseBracketArgs(resolveTaintType(emb, info))
		if mm, ok := walk(emb, nil, nextArgs); ok {
			return mm, true
		}
	}
	return nil, false
}

// receiverArgText returns the raw instantiated type text of a receiver
// expression: the variable's declared type (gR[*gsG]), or the operand
// type of an address-of value (*gR[*os.File]).
func receiverArgText(e ast.Expr, st *taints, info pkgInfo) string {
	if tt, ok := typeOfBase(e, st, info); ok {
		return tt
	}
	return ""
}

// inferTypeArgs binds each type parameter of a generic function from
// the resolved types of the call's arguments: exact parameter shapes
// (f T) bind the argument type; one-wrapper shapes ([]T, *T, map[K]T,
// chan T) bind the corresponding element. An argument whose type cannot
// be resolved leaves its parameter unbound.
func inferTypeArgs(name string, call *ast.CallExpr, st *taints, info pkgInfo, tps []string) []string {
	args := make([]string, len(tps))
	params := info.funcParams[name]
	for pi, pt := range params {
		if pi >= len(call.Args) {
			continue
		}
		at, ok := typeOfBase(call.Args[pi], st, info)
		if !ok || at == "" {
			continue
		}
		for ti, tp := range tps {
			if args[ti] != "" {
				continue
			}
			switch {
			case pt == tp:
				args[ti] = at
			case strings.HasPrefix(pt, "[]"):
				args[ti] = elemTypeOne(at)
			case strings.HasPrefix(pt, "*"):
				args[ti] = strings.TrimPrefix(at, "*")
			case strings.HasPrefix(pt, "map["):
				args[ti] = elementTypeText(at)
			case strings.HasPrefix(pt, "chan ") || strings.HasPrefix(pt, "<-chan "):
				args[ti] = elemTypeOne(at)
			}
		}
	}
	for _, a := range args {
		if a == "" {
			return nil
		}
	}
	return args
}

// genericCallResults resolves the declared result types of a
// same-package generic call: explicit instantiations
// (mkGen[*gsG]()) substitute the call's type arguments; inferred calls
// (mkT2(&gsG{})) bind type parameters from the argument types. Returns
// ok only when every type parameter is bound.
func genericCallResults(call *ast.CallExpr, st *taints, info pkgInfo) ([]string, bool) {
	if call == nil {
		return nil, false
	}
	fun := unwrapParen(call.Fun)
	var name string
	var exArgs []string
	switch f := fun.(type) {
	case *ast.IndexExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			name = id.Name
			exArgs = []string{exprText(f.Index)}
		}
	case *ast.IndexListExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			name = id.Name
			for _, ix := range f.Indices {
				exArgs = append(exArgs, exprText(ix))
			}
		}
	case *ast.Ident:
		name = f.Name
	}
	if name == "" {
		return nil, false
	}
	tps := info.funcTypeParams[name]
	results := info.funcs[name]
	if len(tps) == 0 || len(results) == 0 {
		return nil, false
	}
	args := exArgs
	if len(args) == 0 {
		args = inferTypeArgs(name, call, st, info, tps)
	}
	if args == nil || len(args) < len(tps) {
		return nil, false
	}
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = substituteTypeParams(r, tps, args, info)
	}
	return out, true
}

// genericParamFilePositions maps type-parameter result positions back
// to argument positions for a same-package generic call: idf(os.Stdin)
// with idf[T any](f T) T binds T to *os.File, so the result position
// carrying T is a file position.
func genericParamFilePositions(call *ast.CallExpr, info pkgInfo, st *taints, imports map[string]string) []int {
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return nil
	}
	tps := info.funcTypeParams[id.Name]
	if len(tps) == 0 {
		return nil
	}
	params := info.funcParams[id.Name]
	var pos []int
	for ri, rt := range info.funcs[id.Name] {
		for _, tp := range tps {
			if rt != tp {
				continue
			}
			for ai, pt := range params {
				if bindsTypeParam(pt, tp) && ai < len(call.Args) && isFileOrContainer(call.Args[ai], st, info, imports) {
					pos = append(pos, ri)
					break
				}
			}
		}
	}
	if len(pos) == 0 {
		return nil
	}
	return pos
}

// pkgVarRoot reports whether the root identifier of a fieldTaint key
// (expr.field or expr.inner.field) names a package-level variable.
func pkgVarRoot(key string, info pkgInfo) bool {
	root := key
	if i := strings.IndexByte(key, '.'); i >= 0 {
		root = key[:i]
	}
	return info.pkgVars[root]
}

// bindsTypeParam reports whether a parameter type text binds the named
// type parameter: exactly (T), or through a container/pointer element
// shape ([]T, chan T, map[K]T, *T). Token matching avoids a substring
// false positive for longer identifiers containing the parameter name.
func bindsTypeParam(pt, tp string) bool {
	if pt == tp {
		return true
	}
	for _, tok := range strings.FieldsFunc(pt, func(r rune) bool {
		return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		if tok == tp {
			return true
		}
	}
	return false
}

// genericResultFuncFile reports a same-package generic call whose
// result is a type parameter bound to a func-file argument:
// idf(getDef3) with idf[T any](f T) T yields a funcFile.
func genericResultFuncFile(call *ast.CallExpr, st *taints, info pkgInfo, imports map[string]string) bool {
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	tps := info.funcTypeParams[id.Name]
	if len(tps) == 0 {
		return false
	}
	params := info.funcParams[id.Name]
	for _, rt := range info.funcs[id.Name] {
		for _, tp := range tps {
			if rt != tp {
				continue
			}
			for ai, pt := range params {
				if bindsTypeParam(pt, tp) && ai < len(call.Args) && classify(call.Args[ai], st, info, imports) == kindFuncFile {
					return true
				}
			}
		}
	}
	return false
}

// resolveStruct resolves the struct type name of an instance expression:
// an identifier registered as a struct value, new(T), a same-package
// constructor, a composite literal, or a field chain like h.inner where
// the root instance's field holds a nested struct value.
func resolveStruct(e ast.Expr, st *taints, info pkgInfo) (string, bool) {
	switch v := e.(type) {
	case *ast.Ident:
		name, ok := st.struc[v.Name]
		return name, ok
	case *ast.CallExpr:
		if gcr, okG := genericCallResults(v, st, info); okG {
			for _, r := range gcr {
				n := resolveStructName(strings.TrimPrefix(r, "*"), info)
				if _, isStruct := info.structs[n]; isStruct {
					return n, true
				}
			}
		}
		// A generic or plain method call whose declared result names a
		// struct (rr.mk() with T bound to wS) registers the result as
		// an instance so later field reads (r.f) resolve taint.
		if sel, okS := v.Fun.(*ast.SelectorExpr); okS {
			if sn, ok2 := resolveStruct(sel.X, st, info); ok2 {
				if mres, okM := genericMethodResults(sel, st, info); okM {
					for _, r := range mres {
						n := resolveStructName(strings.TrimPrefix(r, "*"), info)
						if _, isStruct := info.structs[n]; isStruct {
							return n, true
						}
					}
				}
				for _, r := range methodMetaResults(sn, sel.Sel.Name, info) {
					n := resolveStructName(strings.TrimPrefix(r, "*"), info)
					if _, isStruct := info.structs[n]; isStruct {
						return n, true
					}
				}
			}
		}
		if id, ok := v.Fun.(*ast.Ident); ok {
			if id.Name == "new" && len(v.Args) == 1 {
				if aid, ok := v.Args[0].(*ast.Ident); ok {
					base := resolveStructName(aid.Name, info)
					if _, isStruct := info.structs[base]; isStruct {
						return base, true
					}
				}
			}
			for _, r := range info.funcs[id.Name] {
				n := resolveStructName(strings.TrimPrefix(r, "*"), info)
				if _, isStruct := info.structs[n]; isStruct {
					return n, true
				}
			}
		}
	case *ast.CompositeLit:
		// Alias and defined names (type f = s; f{}), pointer spellings,
		// and generic instantiations (gsG[int]{}) all resolve to the
		// underlying struct before the method lookup.
		base := resolveStructName(exprText(v.Type), info)
		if _, isStruct := info.structs[base]; isStruct {
			return base, true
		}
	case *ast.IndexExpr:
		return containerElementStruct(v, st, info)
	case *ast.SelectorExpr:
		// h.inner.fn: resolve the root instance, then walk the field
		// chain until the final field's type is a struct.
		rootName, ok := resolveStruct(v.X, st, info)
		if !ok {
			return "", false
		}
		ft, okf := info.structs[rootName][v.Sel.Name]
		if !okf {
			return "", false
		}
		base := strings.TrimPrefix(resolveTypeText(ft, info), "*")
		if _, isStruct := info.structs[base]; isStruct {
			return base, true
		}
		return "", false
	case *ast.StarExpr:
		return resolveStruct(v.X, st, info)
	case *ast.ParenExpr:
		return resolveStruct(v.X, st, info)
	case *ast.UnaryExpr:
		if v.Op == token.ARROW {
			// <-ch: the channel's element type names the struct.
			if sn, ok := exprElemStruct(v.X, st, info); ok {
				return sn, true
			}
		}
		return resolveStruct(v.X, st, info)
	}
	return "", false
}

// typeOfBase resolves the declared type text of a base expression: a
// variable (varTypes), a struct-instance field, a same-package call
// result, or a deref/parsed wrapper of those.
func typeOfBase(e ast.Expr, st *taints, info pkgInfo) (string, bool) {
	switch v := e.(type) {
	case *ast.Ident:
		tt, ok := info.varTypes[v.Name]
		return tt, ok
	case *ast.ParenExpr:
		return typeOfBase(v.X, st, info)
	case *ast.StarExpr:
		t, ok := typeOfBase(v.X, st, info)
		if !ok {
			return "", false
		}
		return strings.TrimPrefix(t, "*"), true
	case *ast.SelectorExpr:
		if sn, ok := resolveStruct(v.X, st, info); ok {
			if ft, ok := info.structs[sn][v.Sel.Name]; ok {
				return resolveTypeText(ft, info), true
			}
		}
		return "", false
	case *ast.CompositeLit:
		// map[string]*gs{"a": {}} / []chan *gs{...}: the literal's
		// declared type names the container or element type.
		return exprText(v.Type), true
	case *ast.IndexExpr:
		// s[i] / m["k"]: the base container's element type (one
		// wrapper stripped), so the read value itself can serve as an
		// indexed or receiver base later.
		bt, ok := typeOfBase(v.X, st, info)
		if !ok {
			return "", false
		}
		return elemTypeOne(bt), true
	case *ast.CallExpr:
		if gcr, okG := genericCallResults(v, st, info); okG && len(gcr) > 0 {
			return resolveTypeText(gcr[0], info), true
		}
		fun := unwrapParen(v.Fun)
		if id, ok := fun.(*ast.Ident); ok {
			if rs := info.funcs[id.Name]; len(rs) > 0 {
				return resolveTypeText(rs[0], info), true
			}
		}
		if sel, ok := fun.(*ast.SelectorExpr); ok {
			if sn, ok2 := resolveStruct(sel.X, st, info); ok2 {
				if mres, _ := methodMeta(sn, sel.Sel.Name, info); len(mres) > 0 {
					return resolveTypeText(mres[0], info), true
				}
			}
		}
		return "", false
	case *ast.UnaryExpr:
		if v.Op == token.ARROW {
			// <-ch: the channel's element type is the received value's
			// type (one wrapper stripped), so multi-assign receive
			// bindings can serve as indexed or receiver bases.
			ct, ok := typeOfBase(v.X, st, info)
			if !ok {
				return "", false
			}
			return elemTypeOne(ct), true
		}
		if v.Op == token.AND {
			// &x: the address-of value's type is a pointer to the
			// operand's type (needed for &gR[*os.File]{} receivers).
			t, ok := typeOfBase(v.X, st, info)
			if !ok {
				return "", false
			}
			return "*" + t, true
		}
		return "", false
	}
	return "", false
}

// stripElemType removes every container and channel wrapper from a
// declared type text, leaving the element type spelling.
func stripElemType(t string) string {
	for i := 0; i < 8; i++ {
		prev := t
		if n := elementTypeText(t); n != "" {
			t = n
		} else if strings.HasPrefix(t, "chan ") {
			t = strings.TrimPrefix(t, "chan ")
		} else if strings.HasPrefix(t, "<-chan ") {
			t = strings.TrimPrefix(t, "<-chan ")
		}
		if t == prev {
			break
		}
	}
	return t
}

// elemTypeOne strips exactly one container or channel wrapper from a
// declared type text, leaving the element type spelling of the
// outermost wrapper (used for range-variable bindings).
func elemTypeOne(t string) string {
	if n := elementTypeText(t); n != "" {
		return n
	}
	if strings.HasPrefix(t, "chan ") {
		return strings.TrimPrefix(t, "chan ")
	}
	if strings.HasPrefix(t, "<-chan ") {
		return strings.TrimPrefix(t, "<-chan ")
	}
	return t
}

// exprElemStruct returns the base struct name of the element type of e
// (a container or channel expression, or the struct type itself).
func exprElemStruct(e ast.Expr, st *taints, info pkgInfo) (string, bool) {
	tt, ok := typeOfBase(e, st, info)
	if !ok || tt == "" {
		return "", false
	}
	baseName := resolveStructName(stripElemType(tt), info)
	if _, isStruct := info.structs[baseName]; isStruct {
		return baseName, true
	}
	return "", false
}

// exprIsChan reports whether e's declared type is a channel.
func exprIsChan(e ast.Expr, st *taints, info pkgInfo) bool {
	tt, ok := typeOfBase(e, st, info)
	if !ok {
		return false
	}
	return strings.HasPrefix(tt, "chan ") || strings.HasPrefix(tt, "<-chan")
}

// containerElementStruct resolves the struct name behind a container
// element access (arr[1], mm["k"], s.f[1], call()[0], (*p)[0]): the
// base expression's declared type is stripped of every container
// wrapper, then the element type is resolved to the base struct name.
func containerElementStruct(v *ast.IndexExpr, st *taints, info pkgInfo) (string, bool) {
	base := ast.Expr(v)
	for {
		ix, ok := base.(*ast.IndexExpr)
		if !ok {
			break
		}
		base = ix.X
	}
	tt, ok := typeOfBase(base, st, info)
	if !ok || tt == "" {
		return "", false
	}
	baseName := resolveStructName(stripElemType(tt), info)
	if _, isStruct := info.structs[baseName]; isStruct {
		return baseName, true
	}
	return "", false
}

// addSignatureTaints taints receiver and parameter identifiers from
// their declared types.
func addSignatureTaints(st *taints, fields *ast.FieldList, info pkgInfo) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		t := resolveTypeText(exprText(field.Type), info)
		for _, name := range field.Names {
			switch {
			case isFileTyped(t):
				st.file[name.Name] = true
			case funcTypeResultsFile(field.Type, info):
				st.funcFile[name.Name] = true
			case chanElemFuncFile(t, info):
				st.chanFuncFile[name.Name] = true
			case chanElemFile(t, info):
				st.chanFile[name.Name] = true
			case mentionsFileType(t):
				st.container[name.Name] = true
			}
			if k := elementKindShape(t, info); k != kindNone {
				st.elementTaint[name.Name] = k
			}
			base := strings.TrimPrefix(t, "*")
			if _, isStruct := info.structs[base]; isStruct {
				st.struc[name.Name] = base
			}
		}
		if len(field.Names) == 0 {
			// embedded field or unnamed result: nothing to taint
		}
	}
}

// prepassStmts walks statements in source order so assignment-based
// taint propagation sees earlier assignments.
func prepassStmts(list []ast.Stmt, st *taints, info pkgInfo, imports map[string]string) {
	for _, s := range list {
		// Closure bodies capture the outer taints; walk every statement's
		// expression tree for function literals so assignments inside
		// closures propagate with the same state (they appear in RHS,
		// call arguments, or standalone-call positions, not as statements).
		ast.Inspect(s, func(n ast.Node) bool {
			if fl, ok := n.(*ast.FuncLit); ok {
				prepassStmts(fl.Body.List, st, info, imports)
				// A closure whose body returns a file-tainted value is a
				// producer regardless of its declared result type (a
				// *os.File satisfies io.ReadCloser).
				ast.Inspect(fl.Body, func(rn ast.Node) bool {
					if ret, ok := rn.(*ast.ReturnStmt); ok {
						for _, res := range ret.Results {
							if isFileOrContainer(res, st, info, imports) {
								st.retFile[fl.Pos()] = true
								return false
							}
						}
					}
					return true
				})
			}
			return true
		})
		switch v := s.(type) {
		case *ast.AssignStmt:
			if len(v.Rhs) == 1 && len(v.Lhs) > 1 {
				for i, lhs := range v.Lhs {
					applyLHSMulti(lhs, v.Rhs[0], i, st, info, imports)
				}
				break
			}
			for i, lhs := range v.Lhs {
				if i >= len(v.Rhs) {
					break
				}
				applyLHS(lhs, v.Rhs[i], st, info, imports)
				if v.Tok == token.DEFINE && len(v.Lhs) == 1 {
					if id, ok := lhs.(*ast.Ident); ok {
						switch r := v.Rhs[i].(type) {
						case *ast.CompositeLit:
							info.varTypes[id.Name] = exprText(r.Type)
						case *ast.CallExpr:
							// mm := make(map[string]*gs): the first make
							// argument names the declared container.
							if f, ok := r.Fun.(*ast.Ident); ok && f.Name == "make" && len(r.Args) >= 1 {
								info.varTypes[id.Name] = exprText(r.Args[0])
							}
						}
					}
				}
			}
		case *ast.DeclStmt:
			if gd, ok := v.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						var cls kind
						if vs.Type != nil {
							cls = classifyType(vs.Type, info)
							for _, n := range vs.Names {
								if n.Name != "_" {
									info.varTypes[n.Name] = exprText(vs.Type)
								}
							}
						}
						for i, name := range vs.Names {
							if len(vs.Values) > i {
								cls = classify(vs.Values[i], st, info, imports)
								if c, ok := classifyStruct(vs.Values[i], st, info); ok {
									st.struc[name.Name] = c
								}
								// Declared file-producing result types keep
								// the producer taint with an initializer
								// (var f func(string) (*os.File, error) = os.Open).
								if vs.Type != nil && funcTypeResultsFile(vs.Type, info) {
									st.funcFile[name.Name] = true
								}
								// A stdlib producer bound as a value is a
								// func-file (f := os.Open).
								if sel, ok := vs.Values[i].(*ast.SelectorExpr); ok {
									if selp, ok2 := sel.X.(*ast.Ident); ok2 && imports[selp.Name] == "os" {
										if _, found := fileProducers["os."+sel.Sel.Name]; found {
											st.funcFile[name.Name] = true
										}
									}
								}
							} else if vs.Type != nil {
								// type-only `var t T`: register the struct
								// instance so t.field file reads resolve.
								if base, ok := structBase(vs.Type, info); ok {
									st.struc[name.Name] = base
								}
							}
							applyKind(st, name.Name, cls)
						}
					}
				}
			}
		case *ast.IfStmt:
			if v.Init != nil {
				prepassStmts([]ast.Stmt{v.Init}, st, info, imports)
			}
			prepassStmts(v.Body.List, st, info, imports)
			if v.Else != nil {
				if blk, ok := v.Else.(*ast.BlockStmt); ok {
					prepassStmts(blk.List, st, info, imports)
				} else if ifs, ok := v.Else.(*ast.IfStmt); ok {
					prepassStmts([]ast.Stmt{ifs}, st, info, imports)
				}
			}
		case *ast.ForStmt:
			if v.Init != nil {
				prepassStmts([]ast.Stmt{v.Init}, st, info, imports)
			}
			prepassStmts(v.Body.List, st, info, imports)
		case *ast.RangeStmt:
			// Ranging over a container yields *os.File elements in the
			// Value position; ranging over a file channel yields them in
			// the Value position, or in the Key position for the
			// single-variable form (for z := range ch). The ranged
			// expression is classified whole, so struct-field channels
			// and method values resolve through the same rules.
			rk := classify(v.X, st, info, imports)
			bind := func(k *ast.Ident) {
				if k == nil || k.Name == "_" {
					return
				}
				switch rk {
				case kindFile, kindContainer, kindChanFile:
					st.file[k.Name] = true
				case kindChanFuncFile, kindFuncFile:
					st.funcFile[k.Name] = true
				}
			}
			if v.Value != nil {
				if k, ok := v.Value.(*ast.Ident); ok {
					bind(k)
				}
			} else {
				if k, ok := v.Key.(*ast.Ident); ok {
					bind(k)
				}
			}
			// Register the bound variable as a struct instance when the
			// ranged element type is a struct: the two-variable form
			// binds the element in Value; a channel's single-variable
			// form binds it in Key (a container's single-variable form
			// binds the index, never a struct element). The element
			// type is also recorded so the binding itself can serve as
			// an indexed or receive base later.
			if rtt, rtOK := typeOfBase(v.X, st, info); rtOK {
				one := elemTypeOne(rtt)
				if v.Value != nil {
					if k, ok := v.Value.(*ast.Ident); ok && k.Name != "_" {
						info.varTypes[k.Name] = one
						if esn, esnOK := exprElemStruct(v.X, st, info); esnOK {
							st.struc[k.Name] = esn
						}
					}
				} else if exprIsChan(v.X, st, info) {
					if k, ok := v.Key.(*ast.Ident); ok && k.Name != "_" {
						info.varTypes[k.Name] = one
						if esn, esnOK := exprElemStruct(v.X, st, info); esnOK {
							st.struc[k.Name] = esn
						}
					}
				}
			}
			prepassStmts(v.Body.List, st, info, imports)
		case *ast.SendStmt:
			// `ch <- f` with a file value: mark the channel as carrying
			// files so a later receive (or loop) taints the value.
			// Selector-typed channels (fb.ch) record the same taint on
			// the field.
			markSend := func(key string) {
				if isFileOrContainer(v.Value, st, info, imports) {
					st.fieldTaint[key] = kindChanFile
				}
				if classify(v.Value, st, info, imports) == kindFuncFile {
					st.fieldTaint[key] = kindChanFuncFile
				}
			}
			if id, ok := v.Chan.(*ast.Ident); ok {
				if isFileOrContainer(v.Value, st, info, imports) {
					st.chanFile[id.Name] = true
				}
				if classify(v.Value, st, info, imports) == kindFuncFile {
					st.chanFuncFile[id.Name] = true
				}
			} else if sel, ok := v.Chan.(*ast.SelectorExpr); ok {
				markSend(exprText(sel.X) + "." + sel.Sel.Name)
			}
		case *ast.SwitchStmt:
			if v.Init != nil {
				prepassStmts([]ast.Stmt{v.Init}, st, info, imports)
			}
			for _, cc := range v.Body.List {
				if cs, ok := cc.(*ast.CaseClause); ok {
					prepassStmts(cs.Body, st, info, imports)
				}
			}
		case *ast.TypeSwitchStmt:
			if v.Init != nil {
				prepassStmts([]ast.Stmt{v.Init}, st, info, imports)
			}
			if v.Assign != nil {
				prepassStmts([]ast.Stmt{v.Assign}, st, info, imports)
			}
			bound := typeSwitchBound(v.Assign)
			for _, cc := range v.Body.List {
				if cs, ok := cc.(*ast.CaseClause); ok {
					// switch zv := x.(type) { case *os.File: ... } binds
					// zv as *os.File inside the clause. The default
					// clause binds the switched expression's own type:
					// 	func f() io.ReadCloser {
					// 		switch v := ivd.(type) { default: return v.get() }
					// 	}
					// copies the variable's struct instance and type
					// text to v so v.method() and v.field reads resolve.
					if bound != "" && len(cs.List) == 0 {
						if sx := typeSwitchSwitched(v.Assign); sx != nil {
							if sn, ok := resolveStruct(sx, st, info); ok {
								st.struc[bound] = sn
							}
							if tt, ok := typeOfBase(sx, st, info); ok {
								info.varTypes[bound] = tt
							}
						}
					}
					if bound != "" {
						for _, ce := range cs.List {
							ct := resolveTypeText(exprText(ce), info)
							switch {
							case isFileTyped(ct):
								st.file[bound] = true
							case strings.HasPrefix(ct, "func(") && mentionsFileType(ct):
								st.funcFile[bound] = true
							}
							// case *gs: the bound variable is an
							// instance of the case struct (and keeps
							// the case type text for indexed bases).
							if base := resolveStructName(ct, info); base != "" {
								if _, isStruct := info.structs[base]; isStruct {
									st.struc[bound] = base
								}
							}
							info.varTypes[bound] = ct
						}
					}
					prepassStmts(cs.Body, st, info, imports)
				}
			}
		case *ast.SelectStmt:
			// select cases: receive/send assignments plus clause bodies.
			for _, cc := range v.Body.List {
				if cs, ok := cc.(*ast.CommClause); ok {
					if cs.Comm != nil {
						prepassStmts([]ast.Stmt{cs.Comm}, st, info, imports)
					}
					prepassStmts(cs.Body, st, info, imports)
				}
			}
		case *ast.BlockStmt:
			prepassStmts(v.List, st, info, imports)
		case *ast.LabeledStmt:
			prepassStmts([]ast.Stmt{v.Stmt}, st, info, imports)
		}
	}
}

func classifyStruct(e ast.Expr, st *taints, info pkgInfo) (string, bool) {
	switch v := e.(type) {
	case *ast.CompositeLit:
		base := resolveStructName(exprText(v.Type), info)
		if _, isStruct := info.structs[base]; isStruct {
			return base, true
		}
	case *ast.UnaryExpr:
		if v.Op == token.ARROW {
			if sn, ok := exprElemStruct(v.X, st, info); ok {
				return sn, true
			}
		}
		return classifyStruct(v.X, st, info)
	case *ast.CallExpr:
		if gcr, okG := genericCallResults(v, st, info); okG {
			for _, r := range gcr {
				n := resolveStructName(strings.TrimPrefix(r, "*"), info)
				if _, isStruct := info.structs[n]; isStruct {
					return n, true
				}
			}
		}
		if sel, okS := v.Fun.(*ast.SelectorExpr); okS {
			if sn, ok2 := resolveStruct(sel.X, st, info); ok2 {
				if mres, okM := genericMethodResults(sel, st, info); okM {
					for _, r := range mres {
						n := resolveStructName(strings.TrimPrefix(r, "*"), info)
						if _, isStruct := info.structs[n]; isStruct {
							return n, true
						}
					}
				}
				for _, r := range methodMetaResults(sn, sel.Sel.Name, info) {
					n := resolveStructName(strings.TrimPrefix(r, "*"), info)
					if _, isStruct := info.structs[n]; isStruct {
						return n, true
					}
				}
			}
		}
		if id, ok := v.Fun.(*ast.Ident); ok {
			if id.Name == "new" && len(v.Args) == 1 {
				if aid, ok := v.Args[0].(*ast.Ident); ok {
					base := resolveStructName(aid.Name, info)
					if _, isStruct := info.structs[base]; isStruct {
						return base, true
					}
				}
			}
			for _, r := range info.funcs[id.Name] {
				n := resolveStructName(strings.TrimPrefix(r, "*"), info)
				if _, isStruct := info.structs[n]; isStruct {
					return n, true
				}
			}
		}
	case *ast.IndexExpr:
		return containerElementStruct(v, st, info)
	}
	return "", false
}

// applyLHSField records value taint behind a struct-field write
// (t.r = w or t.r, _ = producer()), so a later read of t.r stays tainted
// even when the field's declared type hides the taint (any, io.Reader,
// func() io.ReadCloser). Every producer kind is recorded: file,
// container, func-file, and channel carriers.
func applyLHSField(lhs, rhs ast.Expr, cls kind, st *taints, info pkgInfo, imports map[string]string) {
	sel, ok := lhs.(*ast.SelectorExpr)
	if !ok {
		return
	}
	switch cls {
	case kindFile, kindContainer, kindFuncFile, kindChanFile, kindChanFuncFile:
		st.fieldTaint[exprText(sel.X)+"."+sel.Sel.Name] = cls
	}
	// A container literal assigned to the field (map[string]F{...})
	// records its element kind so later m[k] reads resolve.
	if cl, ok := rhs.(*ast.CompositeLit); ok {
		for _, el := range cl.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			if k := classify(el, st, info, imports); k != kindNone {
				st.elementTaint[exprText(sel.X)+"."+sel.Sel.Name] = k
				break
			}
		}
	}
}

func applyLHS(lhs, rhs ast.Expr, st *taints, info pkgInfo, imports map[string]string) {
	if ix, ok := lhs.(*ast.IndexExpr); ok {
		// m[k] = v records the element kind for later element reads.
		if k := classify(rhs, st, info, imports); k != kindNone {
			st.elementTaint[exprText(ix.X)] = k
		}
	}
	if _, ok := lhs.(*ast.SelectorExpr); ok {
		applyLHSField(lhs, rhs, classify(rhs, st, info, imports), st, info, imports)
	}
	id, ok := lhs.(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	cls := classify(rhs, st, info, imports)
	applyKind(st, id.Name, cls)
	if funcTypeResultsFile(rhs, info) || callResultsFuncFile(rhs, st, info) {
		st.funcFile[id.Name] = true
	}
	// A stdlib producer bound as a value (open := os.Open) is a
	// func-file: invoking it yields a file even though the stdlib
	// signature is invisible to the scanner.
	if sel, ok := rhs.(*ast.SelectorExpr); ok {
		if selp, ok2 := sel.X.(*ast.Ident); ok2 && imports[selp.Name] == "os" {
			if _, found := fileProducers["os."+sel.Sel.Name]; found {
				st.funcFile[id.Name] = true
			}
		}
	}
	if callResultsChanFuncFile(rhs, st, info) {
		st.chanFuncFile[id.Name] = true
	}
	if fl, ok := rhs.(*ast.FuncLit); ok && st.retFile[fl.Pos()] {
		st.funcFile[id.Name] = true
	}
	if c, ok := classifyStruct(rhs, st, info); ok {
		if _, isIx := rhs.(*ast.IndexExpr); isIx {
			// mm["k"] binds the container's element type: the struct
			// instance is only valid when that element type itself
			// names a struct (map[string]*gs binds *gs, not the
			// stripped gs instance of a []*gs element).
			if tt, ok := info.varTypes[id.Name]; ok {
				if base := resolveStructName(tt, info); base != "" {
					if _, isStruct := info.structs[base]; isStruct {
						st.struc[id.Name] = base
					}
				}
			}
		} else {
			st.struc[id.Name] = c
		}
	}
	if sel, ok := rhs.(*ast.SelectorExpr); ok {
		// x := h.inner registers the nested struct instance so later
		// x.fn() field reads resolve through it.
		if sn, ok2 := resolveStruct(sel, st, info); ok2 {
			st.struc[id.Name] = sn
		}
	}
	// A non-call RHS also declares the binding's static type: a
	// container element read (a, _ := mm["k"], 0 binds the element
	// type []*gs), a struct field value, or a composite literal.
	if _, isCall := rhs.(*ast.CallExpr); !isCall {
		if tt, ok := typeOfBase(rhs, st, info); ok && tt != "" {
			info.varTypes[id.Name] = tt
		}
	}
	// A single-value call result declares the binding's type (a :=
	// mkArr() binds []*gs), so the binding can serve as an indexed or
	// field base later. Method-call receivers resolve like the
	// multi-assign path.
	if call, ok := rhs.(*ast.CallExpr); ok {
		fun := unwrapParen(call.Fun)
		var results []string
		if gcr, okG := genericCallResults(call, st, info); okG {
			results = gcr
		} else {
			switch f := fun.(type) {
			case *ast.Ident:
				results = info.funcs[f.Name]
			case *ast.SelectorExpr:
				if sn, ok2 := resolveStruct(f.X, st, info); ok2 {
					if mres, ok3 := genericMethodResults(f, st, info); ok3 {
						results = mres
					} else {
						results, _ = methodMeta(sn, f.Sel.Name, info)
					}
				}
			}
		}
		if len(results) > 0 {
			info.varTypes[id.Name] = results[0]
		}
	}
}

// applyLHSMulti handles `a, b := producer()` where one RHS call yields
// several results: only the result positions declared as *os.File become
// file-tainted (an error result must not).
func applyLHSMulti(lhs, rhs ast.Expr, index int, st *taints, info pkgInfo, imports map[string]string) {
	// a, b := f(): record the declared result type at this index so
	// the binding can serve as an indexed or receive base later.
	if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
		if call, ok := rhs.(*ast.CallExpr); ok {
			fun := unwrapParen(call.Fun)
			var results []string
			if gcr, okG := genericCallResults(call, st, info); okG {
				results = gcr
			} else {
				switch f := fun.(type) {
				case *ast.Ident:
					results = info.funcs[f.Name]
				case *ast.SelectorExpr:
					if sn, ok2 := resolveStruct(f.X, st, info); ok2 {
						if mres, ok3 := genericMethodResults(f, st, info); ok3 {
							results = mres
						} else {
							results, _ = methodMeta(sn, f.Sel.Name, info)
						}
					}
				}
			}
			if index < len(results) {
				info.varTypes[id.Name] = results[index]
			}
		}
	}
	if _, pos, isProducer := producerCall(rhs, st, info, imports); isProducer {
		for _, p := range pos {
			if p == index {
				// A producer claim made for a func-file-shaped
				// declared result (an interface method, an iface-impl
				// union) must stay a func-file/chan carrier so the
				// binding can be invoked and traced; only the raw
				// *os.File positions are plain files. The declared
				// position kind, when it resolves, is authoritative.
				if k := callResultKindAt(rhs, index, st, info); k != kindNone && k != kindFile {
					applyLHSField(lhs, nil, k, st, info, imports)
					if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
						applyKind(st, id.Name, k)
					}
					return
				}
				applyLHSField(lhs, nil, kindFile, st, info, imports)
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					st.file[id.Name] = true
				}
				return
			}
		}
		return // non-file result positions (error results) get no taint
	}
	// Mixed multi-result calls (f, err := getFn() with getFn()
	// (func() *os.File, error)) keep their producer taint at the
	// exact func-typed result position: register that position's own
	// carrier kind instead of applying the whole-call class to every
	// binding (which would taint the error position).
	if _, isCall := rhs.(*ast.CallExpr); isCall {
		if k := callResultKindAt(rhs, index, st, info); k != kindNone {
			applyLHSField(lhs, nil, k, st, info, imports)
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				applyKind(st, id.Name, k)
			}
			return
		}
		// An unmatched position of a multi-result call (an error
		// result, or a plain value) gets no taint.
		return
	}
	cls := classify(rhs, st, info, imports)
	if cls != kindNone {
		applyLHSField(lhs, rhs, cls, st, info, imports)
		if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
			applyKind(st, id.Name, cls)
		}
	}
	// A non-call RHS also declares the binding's static type: a
	// container element read (a, _ := mm["k"], 0 binds the element
	// type []*gs), a struct field, a composite literal, or a channel
	// value. Multi-result calls are recorded by the call path above at
	// the exact result index, so calls are not re-resolved here.
	if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
		if _, isCall := rhs.(*ast.CallExpr); !isCall {
			if tt, ok := typeOfBase(rhs, st, info); ok && tt != "" {
				info.varTypes[id.Name] = tt
			}
		}
	}
	if c, ok := classifyStruct(rhs, st, info); ok {
		if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
			if _, isIx := rhs.(*ast.IndexExpr); isIx {
				// mm["k"] binds the container's element type: the
				// struct instance is only valid when that element type
				// itself names the struct (map[string]*gs binds *gs,
				// not the stripped gs instance of a []*gs element).
				if tt, ok := info.varTypes[id.Name]; ok {
					if base := resolveStructName(tt, info); base != "" {
						if _, isStruct := info.structs[base]; isStruct {
							st.struc[id.Name] = base
						}
					}
				}
			} else {
				st.struc[id.Name] = c
			}
		}
	}
}

// findExemptions locates the three tolerated in-memory inflater call
// shapes inside internal/reader/metadata.go and records their selector
// positions so the rules pass ignores exactly those nodes.
func findExemptions(fd *ast.FuncDecl, src []byte, fset *token.FileSet, st *taints, info pkgInfo, imports map[string]string) map[token.Pos]bool {
	exempts := map[token.Pos]bool{}
	path := fset.Position(fd.Pos()).Filename
	if !strings.HasSuffix(path, "internal/reader/metadata.go") {
		return exempts
	}
	recvName, recvStruct := receiverOf(fd, info)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "ReadFull":
			if len(call.Args) == 2 && isIOIdent(sel.X) {
				args := src[int(call.Lparen) : int(call.Rparen)-1]
				shape := string(args)
				if (shape == "zr, out[:int(meta.MetadataUncompressed)]" || shape == "zr, out[int(meta.MetadataUncompressed):]") &&
					!isFileOrContainer(call.Args[0], st, info, imports) {
					exempts[sel.Pos()] = true
				}
			}
		case "Read", "ReadByte":
			if recvName == "" {
				return true
			}
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "r" {
				return true
			}
			id, ok := inner.X.(*ast.Ident)
			if ok && id.Name == recvName {
				if _, isStruct := info.structs[recvStruct]; isStruct {
					if info.structs[recvStruct]["r"] == "*bytes.Reader" {
						exempts[sel.Pos()] = true
					}
				}
			}
		}
		return true
	})
	return exempts
}

func isIOIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "io"
}

func receiverOf(fd *ast.FuncDecl, info pkgInfo) (name, structName string) {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return "", ""
	}
	recv := fd.Recv.List[0]
	if len(recv.Names) > 0 {
		name = recv.Names[0].Name
	}
	// The receiver type itself identifies the struct: anonymous
	// receivers (func (T) m()) carry no name, generic receivers spell
	// T[P], and aliases must resolve to the underlying struct so
	// instance lookups key consistently.
	t := exprText(recv.Type)
	structName = strings.TrimPrefix(t, "*")
	if i := strings.IndexByte(structName, '['); i >= 0 {
		structName = structName[:i]
	}
	// Alias and defined receivers (type a = s; type b s; func (a) m())
	// must key under the underlying struct: call sites resolve the
	// receiver type through structBase, so a raw alias key would never
	// be consulted. Pointer aliases (type p = *s) also reduce here.
	structName = resolveStructName(structName, info)
	return name, structName
}

// rulesWalk visits every expression node of a function body and applies
// the selector ban, the file-method ban, and the file-argument ban.
func rulesWalk(scope string, body *ast.BlockStmt, st *taints, exempts map[token.Pos]bool, imports map[string]string, info pkgInfo, reporter *reporter) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if exempts[v.Pos()] {
				return true
			}
			if bannedSelectors[v.Sel.Name] {
				reporter.failf("%s: banned content-transfer selector .%s", scope, v.Sel.Name)
			}
			// A file method used in value position (open := root.Open)
			// keeps the file capability: the approved surface applies to
			// the method name itself, not only to call receivers.
			if isFileExpr(v.X, st, info, imports) && !approvedFileMethods[v.Sel.Name] {
				reporter.failf("%s: %s on an *os.File value outside the approved capability surface", scope, v.Sel.Name)
			}
			// A bound method expression on a file-bearing receiver type
			// ((*os.Root).Open, or a same-package alias of one) binds the
			// method as a value with the receiver as an explicit first
			// argument. The receiver is a type expression, which never
			// carries value taint (isFileExpr above cannot see it), so
			// the bound name would invoke the open untainted; the type
			// spelling alone triggers the capability surface.
			if rt := methodExprFileType(v, info); rt != "" && !approvedFileMethods[v.Sel.Name] {
				reporter.failf("%s: %s method expression on %s outside the approved capability surface", scope, v.Sel.Name, rt)
			}
		case *ast.CallExpr:
			fun := unwrapParen(v.Fun)
			if sel, ok := fun.(*ast.SelectorExpr); ok && !exempts[sel.Pos()] {
				if isFileExpr(sel.X, st, info, imports) && !approvedFileMethods[sel.Sel.Name] {
					reporter.failf("%s: %s on an *os.File value outside the approved capability surface", scope, sel.Sel.Name)
				}
			}
			for _, arg := range v.Args {
				if isFileOrContainer(arg, st, info, imports) && !approvedCallee(fun, imports) {
					reporter.failf("%s: *os.File value passed to %s", scope, calleeText(fun))
				}
			}
		case *ast.FuncLit:
			// Nested closures see the outer taints; their own
			// assignments are deliberately not propagated (the
			// production tree has none).
		}
		return true
	})
}

// approvedCallee allows file values into same-package functions
// (their bodies are scanned too), module-internal packages, and the
// x/sys syscall surface used by the mapping owner. Any other callee —
// in particular every standard-library consumer — is a transfer.
func approvedCallee(fun ast.Expr, imports map[string]string) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		id, ok := f.X.(*ast.Ident)
		if !ok {
			return false
		}
		p := imports[id.Name]
		return strings.HasPrefix(p, moduleInternalPrefix) || p == "golang.org/x/sys/unix"
	}
	return false
}

func calleeText(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return exprText(f)
	}
	return "?"
}

// walkRulesNode applies the same rules to package-level expressions
// (initializers) with package-level taint state.
func walkRulesNode(node ast.Node, st *taints, _ map[token.Pos]bool, imports map[string]string, info pkgInfo, reporter *reporter) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if bannedSelectors[v.Sel.Name] {
				reporter.failf("init: banned content-transfer selector .%s", v.Sel.Name)
			}
			if isFileExpr(v.X, st, info, imports) && !approvedFileMethods[v.Sel.Name] {
				reporter.failf("init: %s on an *os.File value outside the approved capability surface", v.Sel.Name)
			}
			if rt := methodExprFileType(v, info); rt != "" && !approvedFileMethods[v.Sel.Name] {
				reporter.failf("init: %s method expression on %s outside the approved capability surface", v.Sel.Name, rt)
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
				if isFileExpr(sel.X, st, info, imports) && !approvedFileMethods[sel.Sel.Name] {
					reporter.failf("init: %s on an *os.File value outside the approved capability surface", sel.Sel.Name)
				}
			}
			for _, arg := range v.Args {
				if isFileOrContainer(arg, st, info, imports) && !approvedCallee(v.Fun, imports) {
					reporter.failf("init: *os.File value passed to %s", calleeText(v.Fun))
				}
			}
		}
		return true
	})
}

type reporter struct {
	path   string
	failed bool
}

func (r *reporter) fail(msg string) {
	fmt.Printf("content-transfer violation: %s: %s\n", r.path, msg)
	r.failed = true
}

func (r *reporter) failf(format string, args ...any) {
	r.fail(fmt.Sprintf(format, args...))
}

func (r *reporter) err() error {
	if r.failed {
		return fmt.Errorf("violations in %s", r.path)
	}
	return nil
}
