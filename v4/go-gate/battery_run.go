package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// runSelfTest applies the durable mutation battery to a private copy of
// the module: every fail case must make the scan reject the tree, every
// pass case must stay clean. The reviewed tree is never modified.
func runSelfTest(root string) bool {
	tmp, err := os.MkdirTemp("", "iprange-gate-self")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: %v\n", err)
		return false
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(root, tmp); err != nil {
		fmt.Fprintf(os.Stderr, "gatescan: copy: %v\n", err)
		return false
	}
	ok := true
	ran := 0
	all := append(append([]batteryCase{}, batteryCases...), batteryPageCases...)
	for _, c := range all {
		ran++
		if !runBatteryCase(tmp, c) {
			ok = false
		}
	}
	if ok {
		fmt.Printf("gatescan self-test passed (%d cases, %d fail forms, %d benign forms)\n", ran, failFormsAll(), passFormsAll())
	} else {
		fmt.Printf("gatescan self-test FAILED (%d cases)\n", ran)
	}
	return ok
}

func failFormsAll() int {
	n := 0
	for _, c := range append(append([]batteryCase{}, batteryCases...), batteryPageCases...) {
		if c.expectFail {
			n++
		}
	}
	return n
}

func passFormsAll() int { return len(batteryCases) + len(batteryPageCases) - failFormsAll() }

// copyTree copies the module root into dst, skipping .git (the scan
// itself skips only .git; hidden directories are scanned).
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
