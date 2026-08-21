package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// boundaryCaseNames is the routine boundary corpus (SOW-0025 decision
// 2026-08-19; the heavy mutation battery was permanently retired by
// user decision on 2026-08-21 and removed from the tool): one
// representative form per launder family and per view-holder package.
// Per-form scans run in parallel; the measured cost of one module scan
// is ~6-12 min (decision record), so the corpus is not a seconds-scale
// check on the grown tree.
var boundaryCaseNames = []string{
	// descriptor / content-transfer families
	"direct io.ReadAll call", "io.ReadAll function alias",
	"os.File.Read method value", "os.File.Seek call",
	"os.ReadFile in a new package directory",
	"unix.Readv descriptor read in the mapping owner",
	"bufio.NewReader(file).ReadByte", "dot-imported os.ReadFile",
	"fmt.Fscan over a file", "reflection-invoked Read",
	"raw unix.Syscall", "unix.CopyFileRange",
	"encoding/json decoder over a file", "os.File.WriteString",
	"log package writing to a file", "os.StartProcess",
	"flate.NewWriter over a file", "unsafe import anywhere in the module",
	// complete-page ownership families (holder packages)
	"P1: copy of a full mapped page", "P2: append of a full mapped page",
	"P4: array conversion of a full mapped page",
	"P8: full mapped page through a function variable",
	"P11: full page copied inside a defer closure",
	"P13: full page copied inside a go closure",
	"P16: full page through a func-literal variable",
	"P24: append through a same-package pass-through helper",
	"P33: named string conversion of a full page",
	"P42: void unknown callback receiving a full page",
	"P47: interface boxing conversion keeps page taint",
	// P49 (channel round trip) is back in the routine corpus: the
	// paramLeafPathsSeen walk is memoized per struct (2026-08-20), so
	// the container/key recursion that previously diverged with
	// ever-growing prefixes now terminates deterministically.
	"P49: channel round trip keeps page taint",
	"P62: string conversion of a definite full-page view",
	"P63: append into a multi-page mapped view",
	"P74: string conversion of a page view with an unknown bound",
	"P85: fmt variadic spread of a concrete page collection",
	"P111: complete-page copy through the reader exemption",
	"P138: store CopyPage callback laundering a mapped page",
	"P243: store callback invocation with an owned buffer",
	"P75: reflect byte extraction over a mapped view",
	// benign twins (must stay legal)
	"P6: benign bounded record copy",
	"P7: benign bounded metadata-chunk append",
	"P9: benign function variable without a page argument",
	"P10: benign same-package call carries a mapped page",
	"P14: benign bounded copy inside a defer closure",
	"P35: bounded View(0, 64) copy stays legal",
	"P36: bounded slice through a local closure stays legal",
	"P55: named result with a bounded view stays legal",
	"P137: store CopyPage callback copying between mapped pages stays benign",
	// view-holder whitelist boundary (new rule)
	"B1: minted page export from internal/tree",
	"B2: minted page export from a new package",
	"B3: minted page export from internal/bitmap",
	"B4: holder export of a mapped page stays benign",
	"B5: interface-method helper laundering a minted page",
	"B8: type-parameter helper laundering a minted page",
	"B6: public facade exporting a mapped page view",
	"B7: public facade bounded export stays benign",
}

// boundaryCases returns the routine boundary corpus. The corpus is the
// selected table (boundaryCorpus), ordered for the scan runs; no
// heavier case set exists in the tool anymore.
func boundaryCases() []batteryCase {
	return boundaryCorpus
}

// runBoundarySelfTest runs only the boundary corpus, the routine gate
// check for every chunk and gate change.
func runBoundarySelfTest(root string) bool {
	cases := boundaryCases()
	ok, ran := runSelfTestCases(root, cases)
	if ok {
		fmt.Printf("gatescan boundary self-test passed (%d cases, %d fail forms, %d benign forms)\n", ran, failForms(cases), passForms(cases))
	} else {
		fmt.Printf("gatescan boundary self-test FAILED (%d cases)\n", ran)
	}
	return ok
}

// failForms counts the fail-expecting cases in a selection.
func failForms(cases []batteryCase) int {
	n := 0
	for _, c := range cases {
		if c.expectFail {
			n++
		}
	}
	return n
}

// passForms counts the benign cases in a selection.
func passForms(cases []batteryCase) int {
	return len(cases) - failForms(cases)
}

// runSelfTest applies the durable mutation battery to a private copy of
// the module: every fail case must make the scan reject the tree, every
// pass case must stay clean. The reviewed tree is never modified.
func runSelfTestCases(root string, cases []batteryCase) (bool, int) {
	tmp, err := os.MkdirTemp("", "iprange-gate-self")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
		return false, 0
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(root, tmp); err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: copy: %v\n", err)
		return false, 0
	}
	ok := true
	ran := 0
	for _, c := range cases {
		ran++
		if !runBatteryCase(tmp, c) {
			ok = false
		}
	}
	return ok, ran
}

// runSelfTestChunk runs worker k of n of the battery in its own private
// module copy. Workers share nothing: each copy is independent, so
// arbitrary worker counts are safe (no shared temp tree, no shared
// analyzer state across processes).
func chunkRange(total, k, n int) (int, int) {
	base := total / n
	rem := total % n
	start := k*base + min(k, rem)
	end := start + base
	if k < rem {
		end++
	}
	return start, end
}

// runSelfTestParallel splits the battery across jobs worker processes of
// this same binary, streaming every worker's per-case verdicts to stdout,
// and aggregates the totals. The case-by-case expectations are identical
// to the sequential run; only the wall time changes.
func runBoundaryParallel(root string, jobs int) bool {
	cases := boundaryCases()
	if jobs > len(cases) {
		jobs = len(cases)
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: executable: %v\n", err)
		return false
	}
	type result struct {
		k    int
		ok   bool
		note string
	}
	results := make(chan result, jobs)
	for k := 0; k < jobs; k++ {
		k := k
		go func() {
			cmd := exec.Command(exe, "--boundary-chunk", fmt.Sprintf("%d/%d", k, jobs))
			cmd.Args = append(cmd.Args, root)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			note := "ok"
			ok := true
			if err := cmd.Run(); err != nil {
				ok = false
				note = err.Error()
			}
			results <- result{k: k, ok: ok, note: note}
		}()
	}
	allOK := true
	for k := 0; k < jobs; k++ {
		r := <-results
		if !r.ok {
			allOK = false
			fmt.Fprintf(os.Stderr, "gatescan: boundary worker %d/%d failed: %s\n", r.k+1, jobs, r.note)
		}
	}
	if allOK {
		fmt.Printf("gatescan boundary self-test passed (%d cases, %d fail forms, %d benign forms)\n", len(cases), failForms(cases), passForms(cases))
	} else {
		fmt.Printf("gatescan boundary self-test FAILED (%d cases)\n", len(cases))
	}
	return allOK
}

// runBoundaryChunk runs worker k of n over the boundary corpus.
func runBoundaryChunk(root string, k, n int) bool {
	cases := boundaryCases()
	if n > len(cases) {
		if k >= len(cases) {
			return true
		}
		n = len(cases)
	}
	start, end := chunkRange(len(cases), k, n)
	ok, ran := runSelfTestCases(root, cases[start:end])
	fmt.Printf("[boundary worker %d/%d] %s (%d cases)\n", k+1, n, map[bool]string{true: "ok", false: "FAILED"}[ok], ran)
	return ok
}

func copyTree(root, dst string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel != "." && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		// Symlinks (CLAUDE.md -> AGENTS.md and the like) must be
		// re-created as symlinks: copying through them would dereference
		// relative targets outside the copy, and copy_file_range cannot
		// open a symlinked directory.
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, filepath.Join(dst, rel))
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// batteryConfigsFor selects the OS configs a battery case needs: linux
// always, plus any OS whose build tag appears in the case's files.
func batteryConfigsFor(c batteryCase) []osConfig {
	cfg := []osConfig{{GOOS: "linux", GOARCH: "amd64"}}
	text := ""
	for _, op := range c.ops {
		text += op.content + "\n"
	}
	for _, g := range []struct {
		tag  string
		conf osConfig
	}{
		{"//go:build windows", osConfig{GOOS: "windows", GOARCH: "amd64"}},
		{"//go:build darwin", osConfig{GOOS: "darwin", GOARCH: "amd64"}},
		{"//go:build freebsd", osConfig{GOOS: "freebsd", GOARCH: "amd64"}},
		{"//go:build netbsd", osConfig{GOOS: "netbsd", GOARCH: "amd64"}},
	} {
		if strings.Contains(text, g.tag) {
			cfg = append(cfg, g.conf)
		}
	}
	return cfg
}

// runBatteryCase applies one case's ops to the temp tree, runs the scan,
// restores the tree, and reports whether the expectation matched.
func runBatteryCase(root string, c batteryCase) bool {
	type saved struct {
		path    string
		content []byte
		existed bool
	}
	var created []string
	var restored []saved
	snap := func(path string) {
		abs := filepath.Join(root, path)
		data, err := os.ReadFile(abs)
		restored = append(restored, saved{path: path, content: data, existed: err == nil})
	}
	for _, op := range c.ops {
		abs := filepath.Join(root, op.path)
		switch op.kind {
		case "create":
			created = append(created, op.path)
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return reportBattery(root, c, false, "setup: %v", err)
			}
			if err := os.WriteFile(abs, []byte(op.content), 0o644); err != nil {
				return reportBattery(root, c, false, "setup: %v", err)
			}
		case "append":
			snap(op.path)
			f, err := os.OpenFile(abs, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return reportBattery(root, c, false, "setup: %v", err)
			}
			if _, err := f.WriteString(op.content); err != nil {
				f.Close()
				return reportBattery(root, c, false, "setup: %v", err)
			}
			f.Close()
		case "ins":
			// Insert "\t"+content after the flate reader line in
			// metadata.go (the exact anchor the shell battery used).
			snap(op.path)
			data, err := os.ReadFile(abs)
			if err != nil {
				return reportBattery(root, c, false, "setup: %v", err)
			}
			anchor := []byte("zr := flate.NewReader(cr)")
			insert := "\t" + op.content
			out := insertAfterLine(data, anchor, insert)
			if string(out) == string(data) {
				return reportBattery(root, c, false, "insert anchor missing")
			}
			if err := os.WriteFile(abs, out, 0o644); err != nil {
				return reportBattery(root, c, false, "setup: %v", err)
			}
		case "inject":
			snap(op.path)
			data, err := os.ReadFile(abs)
			if err != nil {
				return reportBattery(root, c, false, "setup: %v", err)
			}
			out := injectImport(data, op.content)
			if string(out) == string(data) {
				return reportBattery(root, c, false, "inject anchor missing")
			}
			if err := os.WriteFile(abs, out, 0o644); err != nil {
				return reportBattery(root, c, false, "setup: %v", err)
			}
		}
	}
	cleanup := func() {
		// Restore in reverse op order: when one case lists two ops on the
		// same file (inject + append into metadata.go), the first snapshot
		// holds the original content and must be the last write back.
		for i := len(restored) - 1; i >= 0; i-- {
			s := restored[i]
			abs := filepath.Join(root, s.path)
			if s.existed {
				os.WriteFile(abs, s.content, 0o644)
			} else {
				os.Remove(abs)
			}
		}
		for _, p := range created {
			os.Remove(filepath.Join(root, p))
		}
	}
	var captured []string
	clean := true
	captured = captureDiagnostics(func() {
		clean = scanRoot(root, batteryConfigsFor(c), true)
	})
	cleanup()
	// A fail case expects the gate to reject the tree (clean=false); a
	// benign case expects it to stay accepted (clean=true). A mismatch is
	// when the scan outcome equals the fail flag, not when it differs.
	if clean == c.expectFail {
		return reportBattery(root, c, clean, "expectation mismatch")
	}
	if c.expectFail && !c.allowTypeCheck {
		if c.expectRule != "" {
			found := false
			for _, msg := range captured {
				if strings.Contains(msg, c.expectRule) {
					found = true
					break
				}
			}
			if !found {
				return reportBattery(root, c, clean, "missing expected rule %q", c.expectRule)
			}
		} else {
			// By default, an expected rejection must carry a semantic gate
			// diagnostic; a type-check error alone proves nothing about the
			// named mutation.
			semantic := false
			for _, msg := range captured {
				if !strings.Contains(msg, "does not type-check") {
					semantic = true
					break
				}
			}
			if !semantic {
				return reportBattery(root, c, clean, "rejection came only from a type-check error")
			}
		}
	}
	if c.expectFail {
		fmt.Printf("self-test OK: %s rejected\n", c.desc)
	} else {
		fmt.Printf("self-test OK: %s accepted\n", c.desc)
	}
	return true
}

func reportBattery(root string, c batteryCase, clean bool, format string, args ...any) bool {
	if c.expectFail {
		fmt.Printf("self-test MISS: %s did not fail the gate (%s)\n", c.desc, fmt.Sprintf(format, args...))
	} else {
		fmt.Printf("self-test MISS: %s failed the gate (false positive; %s)\n", c.desc, fmt.Sprintf(format, args...))
	}
	return false
}

// insertAfterLine inserts text after the first line containing anchor.
func insertAfterLine(data []byte, anchor []byte, insert string) []byte {
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.Contains(l, string(anchor)) {
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, insert)
			out = append(out, lines[i+1:]...)
			return []byte(strings.Join(out, "\n"))
		}
	}
	return data
}

// injectImport inserts "line" after the "\t\"io\"" import line of a file,
// expanding the \n and \t escapes exactly as the shell's sed did.
func injectImport(data []byte, line string) []byte {
	line = strings.ReplaceAll(line, `\n`, "\n")
	line = strings.ReplaceAll(line, `\t`, "\t")
	anchor := "\t\"io\""
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if l == anchor {
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, line)
			out = append(out, lines[i+1:]...)
			return []byte(strings.Join(out, "\n"))
		}
	}
	return data
}
