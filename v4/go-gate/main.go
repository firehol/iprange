package main

import (
	"archive/zip"
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

func (r *reporter) fail(pos token.Pos, format string, args ...any) {
	where := ""
	if pos.IsValid() {
		where = r.fset.Position(pos).String() + ": "
	}
	fmt.Printf("content-transfer violation (%s): %s%s\n", r.config, where, fmt.Sprintf(format, args...))
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
	selfTest := false
	root := "."
	if len(args) > 0 && args[0] == "--self-test" {
		selfTest = true
		args = args[1:]
	}
	if len(args) > 0 {
		root = args[0]
	}
	if selfTest {
		if runSelfTest(root) {
			os.Exit(0)
		}
		os.Exit(1)
	}
	if scanRoot(root, osConfigs, false) {
		os.Exit(0)
	}
	os.Exit(1)
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
	type dirInfo struct {
		dir   string
		pkg   string
		files []string
	}
	var dirs []dirInfo
	for dir, list := range byDir {
		pkg, err := packagePathOf(root, dir)
		if err != nil {
			continue
		}
		dirs = append(dirs, dirInfo{dir: dir, pkg: pkg, files: list})
	}
	sort.Slice(dirs, func(i, j int) bool { return topoRank(dirs[i].pkg) < topoRank(dirs[j].pkg) })

	for _, cfg := range configs {
		store := newSummaryStore()
		checks := map[string]*packageCheck{} // pkg path -> check
		for _, di := range dirs {
			loader, err := newLoader(root, cfg, fset)
			if err != nil {
				fmt.Fprintf(os.Stderr, "gatescan: %s: %v\n", cfg, err)
				failed = true
				continue
			}
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
			pc.pf = &pageFlow{pc: pc, path: di.pkg, store: store, values: map[ast.Expr]pageValue{}, callFields: map[*ast.CallExpr]map[string]pageValue{}}
			sums, pf := summarizePackage(pc, di.pkg, store, parsed, pc.pf)
			pc.pf = pf
			pc.pf.summaries = sums
			checks[di.pkg] = pc
		}
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
	}
	return !failed
}

// topoRank orders the module packages by dependency: callees first.
func topoRank(pkg string) int {
	switch pkg {
	case "github.com/firehol/iprange/v4/go/internal/format", "github.com/firehol/iprange/v4/go/internal/work":
		return 0
	case "github.com/firehol/iprange/v4/go/internal/mapping":
		return 1
	case "github.com/firehol/iprange/v4/go/internal/reader":
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
