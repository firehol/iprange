package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// gatescan scans every production file under the module root for the
// mmap-only content-transfer rules. See the package comment for the rule
// families and typeload.go for the type layer.

type astFile struct {
	path string
	f    *ast.File
}

type astFileInfo struct {
	path string
	f    *ast.File
}

// reporter accumulates violations for one scanned file.
type reporter struct {
	path   string
	config string
	fset   *token.FileSet
	failed bool
}

// gateOutMu serializes violation output from the parallel per-config
// scans so one line never interleaves with another.
var gateOutMu sync.Mutex

// diagnosticCapture collects violation text for battery cases that must
// prove a gate diagnostic rather than pass on an unrelated type error.
var diagnosticCapture []string

func captureDiagnostics(fn func()) []string {
	// The swap and restore cross the parallel per-config scan phase
	// (reporter.fail appends under gateOutMu), so the slice header is
	// only touched under the same mutex; fn itself runs unlocked
	// because fail re-enters the lock on the scanning goroutines.
	gateOutMu.Lock()
	old := diagnosticCapture
	diagnosticCapture = nil
	gateOutMu.Unlock()
	defer func() {
		gateOutMu.Lock()
		diagnosticCapture = old
		gateOutMu.Unlock()
	}()
	fn()
	gateOutMu.Lock()
	out := diagnosticCapture
	gateOutMu.Unlock()
	return out
}

func (r *reporter) fail(pos token.Pos, format string, args ...any) {
	where := ""
	if pos.IsValid() {
		where = r.fset.Position(pos).String() + ": "
	}
	msg := fmt.Sprintf("content-transfer violation (%s): %s%s\n", r.config, where, fmt.Sprintf(format, args...))
	gateOutMu.Lock()
	fmt.Print(msg)
	diagnosticCapture = append(diagnosticCapture, msg)
	gateOutMu.Unlock()
	r.failed = true
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--makezip" {
		runMakeZip(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "--dirhash" {
		runDirHash(args[1:])
		return
	}
	boundary := false
	jobs := 1
	chunkK, chunkN := -1, -1
	root := "."
	if len(args) > 0 && args[0] == "--boundary" {
		// Routine gate check (SOW-0025 decisions 2026-08-19 and
		// 2026-08-21): the boundary corpus only - one representative
		// form per launder family and holder package. The heavy
		// mutation battery was permanently retired by the 2026-08-21
		// user decision and removed from the tool; the enforced
		// guarantees are the view-holder code-isolation architecture,
		// this static corpus, and full-module scans.
		boundary = true
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--boundary-jobs" && len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			fmt.Fprintf(os.Stderr, "gatescan: invalid --boundary-jobs %q\n", args[1])
			os.Exit(2)
		}
		jobs = n
		boundary = true
		args = args[2:]
	}
	if len(args) > 0 && args[0] == "--boundary-chunk" && len(args) > 1 {
		var err error
		if chunkK, chunkN, err = parseChunk(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: invalid --boundary-chunk %q\n", args[1])
			os.Exit(2)
		}
		boundary = true
		args = args[2:]
	}
	if len(args) > 0 {
		root = args[0]
	}
	if boundary {
		if chunkK >= 0 {
			if runBoundaryChunk(root, chunkK, chunkN) {
				os.Exit(0)
			}
			os.Exit(1)
		}
		if jobs > 1 {
			if runBoundaryParallel(root, jobs) {
				os.Exit(0)
			}
			os.Exit(1)
		}
		if runBoundarySelfTest(root) {
			os.Exit(0)
		}
		os.Exit(1)
	}
	configs := osConfigs
	if env := os.Getenv("GATESCAN_CONFIGS"); env != "" {
		// Developer iteration knob: limit the scanned OS set (e.g.
		// "linux" alone) for fast local loops. CI and the battery never
		// set it, so the authoritative runs keep the full config set.
		var filtered []osConfig
		for _, c := range osConfigs {
			for _, want := range strings.Split(env, ",") {
				if c.GOOS == strings.TrimSpace(want) {
					filtered = append(filtered, c)
					break
				}
			}
		}
		configs = filtered
	}
	if scanRoot(root, configs, false) {
		os.Exit(0)
	}
	os.Exit(1)
}

// parseChunk parses "k/n" into 0-based worker index k and worker count n.
func parseChunk(s string) (int, int, error) {
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return -1, -1, fmt.Errorf("missing '/'")
	}
	k, err := strconv.Atoi(s[:slash])
	if err != nil || k < 0 {
		return -1, -1, fmt.Errorf("bad worker index")
	}
	n, err := strconv.Atoi(s[slash+1:])
	if err != nil || n < 1 || k >= n {
		return -1, -1, fmt.Errorf("bad worker count")
	}
	return k, n, nil
}

// scanRoot runs the scan over root under the given OS configs. battery
// limits the config selection (single config per case) and disables
// summary caching so mutations are re-analyzed.
func scanRoot(root string, configs []osConfig, battery bool) bool {
	fset := token.NewFileSet()
	var goFiles []string
	var asmFiles []string
	walkErr := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
			walkErr = true
			return nil
		}
		if d.IsDir() {
			if path != root && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), "_test.go") {
			goFiles = append(goFiles, path)
		}
		switch lower := strings.ToLower(d.Name()); {
		case strings.HasSuffix(lower, ".s"), strings.HasSuffix(lower, ".syso"):
			asmFiles = append(asmFiles, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
		return false
	}
	failed := walkErr
	for _, path := range asmFiles {
		fmt.Fprintf(os.Stderr, "gatescan: %s: assembly object rejected (syscall body invisible to the source scan)\n", path)
		failed = true
	}

	// Group files by package directory and compute the per-config package
	// types plus page-taint summaries in module dependency order (the
	// module graph is fixed and separately enforced by the shell harness).
	byDir := map[string][]string{}
	for _, f := range goFiles {
		byDir[filepath.Dir(f)] = append(byDir[filepath.Dir(f)], f)
	}
	for _, list := range byDir {
		sort.Strings(list)
	}
	var dirs []dirInfo
	for dir, list := range byDir {
		pkg, err := packagePathOf(root, dir)
		if err != nil {
			continue
		}
		dirs = append(dirs, dirInfo{dir: dir, pkg: pkg, files: list})
	}
	sort.Slice(dirs, func(i, j int) bool {
		if r := topoRank(dirs[i].pkg) - topoRank(dirs[j].pkg); r != 0 {
			return r < 0
		}
		return dirs[i].dir < dirs[j].dir
	})

	// Every OS config is independent: the package types, page-taint
	// summaries, and rule pass are computed per config. The configurations
	// run concurrently (the module is type-checked once per config, which
	// dominates the runtime); the FileSet and output are mutex-guarded.
	type configResult struct {
		cfg    osConfig
		failed bool
	}
	results := make(chan configResult, len(configs))
	for _, cfg := range configs {
		cfg := cfg
		go func() {
			failed := false
			defer func() {
				// A leaf-path walk divergence (memo budget exhausted
				// by a type family that fabricates a fresh identity
				// per descent) aborts only this config's scan and
				// fails it closed: a hung or partial analysis must
				// never read as a clean pass.
				if r := recover(); r != nil {
					if _, ok := r.(leafWalkDivergence); !ok {
						panic(r)
					}
					gateOutMu.Lock()
					fmt.Printf("gatescan: paramLeafPaths walk divergence in config %s: analysis aborted, scan fails closed\n", cfg)
					gateOutMu.Unlock()
					failed = true
				}
				results <- configResult{cfg: cfg, failed: failed}
			}()
			failed = scanConfig(root, cfg, fset, dirs)
		}()
	}
	for range configs {
		if r := <-results; r.failed {
			failed = true
		}
	}
	return !failed
}

// dirInfo is one package directory of the scanned module: its files and
// its import path under the module prefix.
type dirInfo struct {
	dir   string
	pkg   string
	files []string
}

// scanConfig runs the full package type-check, summary, and rules pass for
// one OS configuration.
func scanConfig(root string, cfg osConfig, fset *token.FileSet, dirs []dirInfo) (failed bool) {
	store := newSummaryStore()
	checks := map[string]*packageCheck{} // pkg path -> check
	// One loader per OS config, reused for every package directory: the
	// loader caches type-checked imports, so the stdlib closure, x/sys,
	// and every already-checked module package are type-checked exactly
	// once per scan instead of once per directory (11x). The mutation
	// battery depends on this: without it every battery case re-checks
	// the whole dependency closure per directory. Measured cost of one
	// scan of the grown tree: ~6-12 min (SOW-0025 decision record
	// 2026-08-19); the per-config loader and the shared FileSet are
	// the tracked allocation/GC follow-up, not M3 scope.
	loader, err := newLoader(root, cfg, fset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: %s: %v\n", cfg, err)
		return true
	}
	for _, di := range dirs {
		parsed := parseFilesForConfig(di.files, cfg, fset)
		if len(parsed) == 0 {
			continue
		}
		tc := &typesChecker{loader: loader, fset: fset}
		pc, err := tc.check(di.pkg, parsed)
		if err != nil {
			// A package that fails type-checking fails the gate: the
			// mutation battery expects every violation to be visible;
			// the production tree always type-checks.
			rep := &reporter{path: strings.Join(di.files, ","), config: cfg.String(), fset: fset}
			rep.fail(token.NoPos, "package %s does not type-check: %v", di.pkg, err)
			failed = true
			continue
		}
		pc.pf = &pageFlow{pc: pc, path: di.pkg, store: store, values: map[ast.Expr]pageValue{}, callFields: map[*ast.CallExpr]map[string]pageValue{}, callResults: map[*ast.CallExpr][]pageValue{}, fieldPromoted: map[ast.Expr]bool{}, callMethodValues: map[*ast.CallExpr]methodValueCall{}, pageSinkCalls: map[*ast.CallExpr][]ast.Expr{}, destAggregated: map[ast.Expr]bool{}, boundedPageSpans: map[boundedSpanKey]int{}, appendAliases: map[types.Object]types.Object{}, appendCallRoots: map[*ast.CallExpr]types.Object{}}
		sums, pf := summarizePackage(pc, di.pkg, store, parsed, pc.pf)
		pc.pf = pf
		pc.pf.summaries = sums
		checks[di.pkg] = pc
		// The checked package joins the loader import cache when no
		// earlier import created it: dependents checked later then
		// import one object per path instead of re-checking source per
		// directory. The first (importer-created) object wins: an
		// explicit check that overwrites it would orphan the types
		// already bound inside earlier packages and break type
		// identity across the module.
		loader.cachePackage(di.pkg, pc.pkg)
		// View-holder whitelist (SOW-0025 decision 2026-08-19): a
		// non-holder package may never export mapped page views, not
		// even as return values. Bounded record values stay legal.
		repB := &reporter{path: strings.Join(di.files, ","), config: cfg.String(), fset: fset}
		checkViewHolderExports(repB.fail, di.pkg, sums)
		if repB.failed {
			failed = true
		}
	}
	// The store-callback carrier slots are a module-wide property
	// (which function parameters receive the store callback formal
	// from a store implementation, directly or through forwarding
	// chains): computed once after every package's summaries
	// stabilize, read by the rules pass.
	store.computeStoreCbSlots(checks)
	for _, di := range dirs {
		pc := checks[di.pkg]
		if pc == nil {
			continue
		}
		for _, f := range pc.files {
			path := filepath.Join(di.dir, f.name)
			rep := &reporter{path: path, config: cfg.String(), fset: fset}
			runRules(rep, f.ast, pc, path)
			if rep.failed {
				failed = true
			}
		}
	}
	return failed
}

// topoRank orders the module packages by dependency: callees first.
func topoRank(pkg string) int {
	switch pkg {
	case "github.com/firehol/iprange/v4/go/internal/format", "github.com/firehol/iprange/v4/go/internal/work":
		return 0
	case "github.com/firehol/iprange/v4/go/internal/bootstrap":
		return 0
	case "github.com/firehol/iprange/v4/go/internal/tree",
		"github.com/firehol/iprange/v4/go/internal/bitmap",
		"github.com/firehol/iprange/v4/go/internal/retire",
		"github.com/firehol/iprange/v4/go/internal/mapping":
		return 1
	case "github.com/firehol/iprange/v4/go/internal/reader", "github.com/firehol/iprange/v4/go/internal/writer":
		return 2
	case "github.com/firehol/iprange/v4/go":
		return 3
	}
	return 4
}

// packagePathOf maps a directory under the module root to its import path.
func packagePathOf(root, dir string) (string, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return moduleImportPrefix, nil
	}
	return moduleImportPrefix + "/" + filepath.ToSlash(rel), nil
}

// parseFilesForConfig keeps only the files of one directory that match the
// config's build context.
func parseFilesForConfig(list []string, cfg osConfig, fset *token.FileSet) []*parsedFile {
	ctx := buildContext(cfg)
	var out []*parsedFile
	for _, path := range list {
		name := filepath.Base(path)
		ok, err := ctx.MatchFile(filepath.Dir(path), name)
		if err != nil {
			continue
		}
		// Files whose names start with a dot are ignored by go/build even
		// though they are ordinary Go sources; the gate walks hidden
		// directories like any other (a smuggled package must not be
		// invisible), so dot-prefixed files are always parsed.
		if !ok && !strings.HasPrefix(name, ".") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			continue
		}
		out = append(out, &parsedFile{name: name, ast: f})
	}
	return out
}

type parsedFile struct {
	name string
	ast  *ast.File
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runMakeZip and runDirHash are the module-cache integrity helpers used by
// the shell harness to pin the genuine golang.org/x/sys content.

func runMakeZip(args []string) {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "gatescan: --makezip requires a base name, a source directory, and a zip output path")
		os.Exit(2)
	}
	if err := makeZip(args[0], args[1], args[2]); err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: --makezip: %v\n", err)
		os.Exit(1)
	}
}

func runDirHash(args []string) {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "gatescan: --dirhash requires a module-cache base name and one directory argument")
		os.Exit(2)
	}
	hash, err := dirHash(args[0], args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: --dirhash %s: %v\n", args[1], err)
		os.Exit(1)
	}
	fmt.Println(hash)
}

// makeZip writes a module-cache zip of dir with every entry named
// base+"/"+rel (the module zip layout). The produced zip hashes to the
// same h1 value as the dir (Hash1 over the same names and contents).
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
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := prefix + "/" + filepath.ToSlash(rel)
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		zw.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return out.Close()
}

// dirHash computes the go module h1 hash of dir: SHA-256 per file over
// base+"/"+rel, then the h1 base64 of the joined digest, matching the sum
// recorded in go.sum for the extracted module.
func dirHash(base, dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
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
		sum := sha256.Sum256(data)
		// Hash1 line format: lowercase hex of the per-file digest, with
		// the joined digest itself base64-encoded (matching
		// golang.org/x/mod/sumdb/dirhash).
		fmt.Fprintf(h, "%x  %s\n", sum, base+"/"+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
