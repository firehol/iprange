// Package gate implements the v4 Go mmap-only content-transfer gate: a
// type-aware scanner over production sources of the v4/go module.
//
// The tool is stdlib-only and lives in its own module (v4/go-gate) so the
// gate never scans itself. The scan type-checks the module with go/types
// under every operating-system file set the module supports, and applies
// three rule families over the typed tree:
//
//   - textual bans: content-transfer imports, dot imports, .s/.syso
//     assembly objects, //go:linkname, embed directives, and bodyless
//     function declarations;
//   - the *os.File capability surface: file-typed values may only feed
//     mapping-lifecycle methods (Fd/Close/Name/Stat/Sync/Truncate/...),
//     same-package or module-internal callees, and the x/sys surface used
//     by the mapping owner; every other use — including laundering a file
//     into an interface/any/container that erases its type — is a
//     violation;
//   - the complete-page ownership rule: a mapped page view (mapping.Page,
//     mapping.View, the mapping data slice, or any value derived from
//     them) must never be copied, appended, converted, or otherwise
//     materialized into an owned buffer at or above PageSize; bounded
//     record decodes below PageSize stay legal. The in-memory metadata
//     inflation nodes in internal/reader/metadata.go are exempted as exact
//     call shapes (their source text is compared literally) and only when
//     their operands are not page-tainted.
package main

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// osConfigs are the file sets the module compiles for. The mapping owner
// has per-OS files; every other package is OS-independent. "other" covers
// every POSIX target that has no proven OFD lifetime primitive
// (mapping_lifetime_other.go: !linux && !darwin && !freebsd && !windows,
// represented here by netbsd).
var osConfigs = []osConfig{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "freebsd", GOARCH: "amd64"},
	{GOOS: "netbsd", GOARCH: "amd64"}, // the !linux/darwin/freebsd/windows set
	{GOOS: "windows", GOARCH: "amd64"},
}

type osConfig struct {
	GOOS   string
	GOARCH string
}

func (c osConfig) String() string { return c.GOOS + "/" + c.GOARCH }

// moduleImportPrefix is the module path of the scanned tree.
const moduleImportPrefix = "github.com/firehol/iprange/v4/go"

// stdlibSrcCache is the cross-scan cache of type-checked standard-library
// packages per OS config. Battery mutations never touch GOROOT, so one
// closure per config serves every self-test case; without it the 280+
// cases would re-type-check the stdlib each time.
var stdlibSrcCache = map[string]map[string]*types.Package{}

// xsysDirCache resolves golang.org/x/sys once per scan invocation series.
var xsysDirCache = map[string]string{}

// loader type-checks one OS file set of the module. Standard library,
// module packages, and golang.org/x/sys are read from source with the
// build context of the target OS (cross-OS export data would mismatch,
// e.g. freebsd x/sys/unix against linux syscall). Imported packages are
// cached for the lifetime of the loader.
type loader struct {
	modRoot string
	cfg     osConfig
	ctx     *build.Context
	gc      types.Importer
	fset    *token.FileSet
	cache   map[string]*types.Package
	xsysDir string
	global  bool
}

func newLoader(modRoot string, cfg osConfig, fset *token.FileSet) (*loader, error) {
	ctx := build.Default
	ctx.GOOS = cfg.GOOS
	ctx.GOARCH = cfg.GOARCH
	ctx.CgoEnabled = false
	xsysDir, err := resolveXsysDir(modRoot)
	if err != nil {
		return nil, err
	}
	l := &loader{
		modRoot: modRoot,
		cfg:     cfg,
		ctx:     &ctx,
		gc:      importer.Default(),
		fset:    fset,
		cache:   map[string]*types.Package{},
		xsysDir: xsysDir,
		global:  true,
	}
	// Preload every stdlib package the gc importer already knows? No:
	// load lazily. Seed none.
	return l, nil
}

// resolveXsysDir locates the golang.org/x/sys module checkout the module
// resolves to (the go toolchain's own loader is the single authority for
// module resolution; the gate never walks a module it cannot resolve).
func resolveXsysDir(modRoot string) (string, error) {
	if dir, ok := xsysDirCache[modRoot]; ok {
		return dir, nil
	}
	cmd := exec.Command("go", "-C", modRoot, "list", "-m", "-f", "{{.Dir}}", "golang.org/x/sys")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", os.ErrNotExist
	}
	xsysDirCache[modRoot] = dir
	return dir, nil
}

// Import satisfies types.Importer.
func (l *loader) Import(path string) (*types.Package, error) {
	if pkg, ok := l.cache[path]; ok {
		return pkg, nil
	}
	var (
		pkg *types.Package
		err error
	)
	switch {
	case path == moduleImportPrefix, strings.HasPrefix(path, moduleImportPrefix+"/"):
		var dir string
		if path == moduleImportPrefix {
			dir = l.modRoot
		} else {
			dir = filepath.Join(l.modRoot, strings.TrimPrefix(path, moduleImportPrefix))
		}
		pkg, err = l.checkDir(path, dir)
	case strings.HasPrefix(path, "golang.org/x/sys"):
		dir := filepath.Join(l.xsysDir, strings.TrimPrefix(path, "golang.org/x/sys"))
		pkg, err = l.checkDir(path, dir)
	default:
		if path == "unsafe" || path == "C" {
			// unsafe has no parseable source (special declarations);
			// C is rejected by the import ban and never type-checked.
			pkg, err = l.gc.Import(path)
			break
		}
		// Standard library (no dot in the first path element). The gc
		// importer serves the HOST OS's export data, which mismatches
		// cross-OS source (e.g. x/sys/unix freebsd against linux
		// syscall.Rlimit). Type-checking stdlib from GOROOT source with
		// the target build context keeps every OS config self-consistent.
		key := l.cfg.GOOS + "/" + l.cfg.GOARCH
		shared := stdlibSrcCache[key]
		if shared == nil {
			shared = map[string]*types.Package{}
			stdlibSrcCache[key] = shared
		}
		pkg = shared[path]
		if pkg == nil {
			srcDir := filepath.Join(runtime.GOROOT(), "src", filepath.FromSlash(path))
			pkg, err = l.checkDir(path, srcDir)
			if err == nil {
				shared[path] = pkg
			}
		}
	}
	if err != nil {
		return nil, err
	}
	l.cache[path] = pkg
	return pkg, nil
}

// checkDir parses and type-checks one package directory with the loader's
// build context (only files that match the target OS are included).
func (l *loader) checkDir(path, dir string) (*types.Package, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*astFile
	var astFiles []*ast.File
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fpath := filepath.Join(dir, name)
		match, err := l.ctx.MatchFile(dir, name)
		if err != nil {
			continue
		}
		// Same hidden-file rule as scanRoot: dot-prefixed sources are
		// parsed and type-checked like any other.
		if !match && !strings.HasPrefix(name, ".") {
			continue
		}
		f, err := parser.ParseFile(l.fset, fpath, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, &astFile{path: fpath, f: f})
		astFiles = append(astFiles, f)
		names = append(names, fpath)
	}
	if len(files) == 0 {
		return nil, os.ErrNotExist
	}
	conf := types.Config{
		Importer: l,
	}
	pkg, err := conf.Check(path, l.fset, astFiles, nil)
	if err != nil {
		return nil, err
	}
	return pkg, nil
}
