//go:build windows

package pathname

import "testing"

// TestVerbatimPushComponentRules pins PathBuf::_push's verbatim
// branch against native windows-host rustc 1.97.1 answers: the
// pushed path's components fold into the verbatim base (repeated and
// trailing separators collapse, CurDir vanishes, ParentDir pops the
// last Normal component, RootDir truncates the buffer to its
// prefix) and the buffer re-emits with the main separator.  Names
// that carry their own prefix or are absolute replace the base in
// Rust and are the documented Push deviation (no caller passes
// them).
func TestVerbatimPushComponentRules(t *testing.T) {
	rows := []struct{ base, name, want string }{
		{`\\?\C:\x`, `n/`, `\\?\C:\x\n`},
		{`\\?\C:\x`, `n\`, `\\?\C:\x\n`},
		{`\\?\C:\x`, `.`, `\\?\C:\x`},
		{`\\?\C:\x`, `..`, `\\?\C:\`},
		{`\\?\C:\x`, `n`, `\\?\C:\x\n`},
		{`\\?\C:\x`, `a/b`, `\\?\C:\x\a\b`},
		{`\\?\C:\x\`, `n/`, `\\?\C:\x\n`},
		{`\\?\C:\x\`, `n\`, `\\?\C:\x\n`},
		{`\\?\C:\x\`, `.`, `\\?\C:\x`},
		{`\\?\C:\x\`, `..`, `\\?\C:\`},
		{`\\?\C:\x\`, `n`, `\\?\C:\x\n`},
		{`\\?\C:\x\`, `a/b`, `\\?\C:\x\a\b`},
		{`\\?\UNC\srv\sh\a`, `n/`, `\\?\UNC\srv\sh\a\n`},
		{`\\?\UNC\srv\sh\a`, `n\`, `\\?\UNC\srv\sh\a\n`},
		{`\\?\UNC\srv\sh\a`, `.`, `\\?\UNC\srv\sh\a`},
		{`\\?\UNC\srv\sh\a`, `..`, `\\?\UNC\srv\sh\`},
		{`\\?\UNC\srv\sh\a`, `n`, `\\?\UNC\srv\sh\a\n`},
		{`\\?\UNC\srv\sh\a`, `a/b`, `\\?\UNC\srv\sh\a\a\b`},
		{`\\?\C:`, `n/`, `\\?\C:\n`},
		{`\\?\C:`, `n\`, `\\?\C:\n`},
		{`\\?\C:`, `.`, `\\?\C:`},
		{`\\?\C:`, `..`, `\\?\C:`},
		{`\\?\C:`, `n`, `\\?\C:\n`},
		{`\\?\C:`, `a/b`, `\\?\C:\a\b`},
		// Trailing CurDir/ParentDir components of the base stay: the
		// fold pops only when the last buffer component is Normal
		// (native windows-host rustc 1.97.1 answers).
		{`\\?\C:\x\..`, `..`, `\\?\C:\x\..`},
		{`\\?\C:\x\..`, `../x`, `\\?\C:\x\..\x`},
		{`\\?\C:\x\..`, `n`, `\\?\C:\x\..\n`},
		{`\\?\C:\x\..`, `.`, `\\?\C:\x\..`},
		{`\\?\C:\x\..`, `../`, `\\?\C:\x\..`},
		{`\\?\UNC\srv\sh\a\..`, `..`, `\\?\UNC\srv\sh\a\..`},
		{`\\?\UNC\srv\sh\a\..`, `../x`, `\\?\UNC\srv\sh\a\..\x`},
		{`\\?\UNC\srv\sh\a\..`, `n`, `\\?\UNC\srv\sh\a\..\n`},
		{`\\?\UNC\srv\sh\a\..`, `.`, `\\?\UNC\srv\sh\a\..`},
		{`\\?\UNC\srv\sh\a\..`, `../`, `\\?\UNC\srv\sh\a\..`},
		{`\\?\C:\x\.`, `..`, `\\?\C:\x\.`},
		{`\\?\C:\x\.`, `../x`, `\\?\C:\x\.\x`},
		{`\\?\C:\x\.`, `n`, `\\?\C:\x\.\n`},
		{`\\?\C:\x\.`, `.`, `\\?\C:\x\.`},
		{`\\?\C:\x\.`, `../`, `\\?\C:\x\.`},
		{`\\?\C:\x\..\.`, `..`, `\\?\C:\x\..\.`},
		{`\\?\C:\x\..\.`, `../x`, `\\?\C:\x\..\.\x`},
		{`\\?\C:\x\..\.`, `n`, `\\?\C:\x\..\.\n`},
		{`\\?\C:\x\..\.`, `.`, `\\?\C:\x\..\.`},
		{`\\?\C:\x\..\.`, `../`, `\\?\C:\x\..\.`},
		{`\\?\C:`, `..`, `\\?\C:`},
		// Absolute and prefix-carrying names replace the base (std
		// need_clear); rooted names without a prefix truncate the
		// base to its prefix (native windows-host rustc answers).
		{`C:\x`, `\n`, `C:\n`},
		{`C:\x`, `C:n`, `C:n`},
		{`C:\x`, `C:\m`, `C:\m`},
		{`a`, ``, `a\`},
		{`a/b`, ``, `a/b\`},
		{`C:\x`, ``, `C:\x\`},
		{`/a/b`, `/c`, `/c`},
		// Pushed empty, root-relative, and compound spellings.
		{`\\?\C:\x`, ``, `\\?\C:\x\`},
		{`\\?\C:\x`, `\n`, `\\?\C:\n`},
		{`\\?\UNC\srv\sh`, `..`, `\\?\UNC\srv\sh`},
		{`\\?\UNC\srv\sh`, `a`, `\\?\UNC\srv\sh\a`},
		{`\\?\UNC\srv\sh\`, `a`, `\\?\UNC\srv\sh\a`},
		{`\\?\C:\x\a`, `../b`, `\\?\C:\x\b`},
		{`\\?\C:\x`, `a\..\b`, `\\?\C:\x\b`},
	}
	for _, r := range rows {
		if got := Push(r.base, r.name); got != r.want {
			t.Errorf("Push(%q, %q) = %q, want %q", r.base, r.name, got, r.want)
		}
	}
	// with_file_name routes through the same folded push below the
	// replaced name (native windows-host rustc answers).
	wfns := []struct{ path, name, want string }{
		{`\\?\C:\x`, `n/`, `\\?\C:\n`},
		{`\\?\C:\x`, `n\`, `\\?\C:\n`},
		{`\\?\C:\x`, `.`, `\\?\C:\`},
		{`\\?\C:\x`, `..`, `\\?\C:\`},
		{`\\?\C:\x`, `n`, `\\?\C:\n`},
		{`\\?\C:\x`, `a/b`, `\\?\C:\a\b`},
		// Trailing-ParentDir bases keep their tail under with_file_name.
		{`\\?\C:\x\..`, `..`, `\\?\C:\x\..`},
		{`\\?\C:\x\..`, `../x`, `\\?\C:\x\..\x`},
		{`\\?\C:\x\..`, `n`, `\\?\C:\x\..\n`},
		{`\\?\C:\x\..`, `.`, `\\?\C:\x\..`},
	}
	for _, r := range wfns {
		if got := WithFileName(r.path, r.name); got != r.want {
			t.Errorf("WithFileName(%q, %q) = %q, want %q", r.path, r.name, got, r.want)
		}
	}
}
