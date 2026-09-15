package calleropen

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Design section 5.2 names the persistent and adapter-owned opens that must
// go through this package: the removals temporary of the live handler, the
// metadata temporary and its parent-directory sync, the export temporary and
// its parent-directory sync, the worker control create and open, and the four
// legacy one-shot inputs.
//
// On Linux every os.Open, os.OpenFile and os.Create registers the descriptor
// with the runtime network poller regardless of O_NONBLOCK
// (os/file_unix.go:155,:219; the regular-file and directory carve-outs are
// limited to the Apple and BSD platforms at os/file_unix.go:165-192), and the
// poller's creation has no failure path (runtime/netpoll_epoll.go:26,:31).
// Design section 10 checks the consequence process-wide, by sampling the
// pressured child's descriptor table; this pins the same invariant at the
// source, so routing a listed owner back through the os package is reported
// as the routing mistake it is even on a machine with a generous limit.
//
// The rule is deliberately narrow: it names the files that own those opens
// and forbids the four pollable constructors in them. Files that legitimately
// use os.Open for something else are not on the list, and are not judged.
var designOwnedOpenFiles = []string{
	"internal/cli/handlers/live.go",
	"internal/cli/handlers/output.go",
	"internal/cli/fileio/export_writer.go",
	"internal/worker/control_create_unix.go",
	"internal/worker/control_open_unix.go",
	"internal/cli/legacy/parse.go",
}

// pollableOpenConstructor matches the os entry points that reach
// os.newFile with a pollable kind.
var pollableOpenConstructor = regexp.MustCompile(`\bos\.(Open|OpenFile|Create|ReadFile)\(`)

func TestDesignOwnedOpensRouteThroughCallerOpen(t *testing.T) {
	root := moduleRoot(t)
	for _, relative := range designOwnedOpenFiles {
		source, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("cannot read %s: %v", relative, err)
		}
		for number, line := range strings.Split(string(source), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if pollableOpenConstructor.MatchString(trimmed) {
				t.Fatalf("%s:%d opens a persistent or adapter-owned node with %q; "+
					"route it through calleropen.Open (design section 5.2), which "+
					"hands the descriptor over without registering it with the "+
					"runtime network poller", relative, number+1, trimmed)
			}
		}
	}
}

// moduleRoot locates the v4/go module from this file's own build path, so the
// check does not depend on the working directory go test happens to use.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's own path")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // .../v4/go/internal/calleropen -> v4/go
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("derived module root %s has no go.mod: %v", root, err)
	}
	return root
}
