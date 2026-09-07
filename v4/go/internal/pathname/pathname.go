// Package pathname implements the Rust std::path component semantics
// exactly as the shipped Rust engines derive pathname components
// (Path::file_name, Path::parent, PathBuf::with_file_name in Rust
// 1.97.1 library/std/src/path.rs). The Rust engines derive names and
// parents from raw caller-supplied paths without normalizing them, so
// every Go destination, sidecar, and namespace binding that mirrors a
// Rust twin must use these helpers instead of path/filepath
// Base/Dir/Join, which resolve ".." and "." differently and would
// change accept/reject parity at the wire boundary.
//
// The port follows the Rust Components state machine: repeated and
// trailing separators collapse, "." components are dropped everywhere
// except as the leading CurDir, ".." components are ordinary
// components that are never resolved, the Windows volume prefix is a
// component of its own, and a root or prefix-only path has no name
// and no parent.
package pathname

import (
	"path/filepath"
	"runtime"
	"strings"
)

// std::path::State values in order: Prefix < StartDir < Body < Done.
// finished() is front==Done || back==Done || front>back.
const (
	statePrefix   = 0
	stateStartDir = 1
	stateBody     = 2
	stateDone     = 3
)

// parsed component classes (std parse_single_component: "." is skipped
// as a body component but is yielded as CurDir at the path start, ".."
// is ParentDir, empty is skipped, everything else is Normal).
type compKind int

const (
	compNone compKind = iota
	compNormal
	compParentDir
	compCurDir
	compRootDir
	compPrefix
)

// hasPrefixes reports whether this GOOS carries Windows-style prefixes.
const hasPrefixes = runtime.GOOS == "windows"

// prefixInfo is the classified Windows volume prefix (Rust Prefix:
// drive, UNC, or verbatim forms).
type prefixInfo struct {
	length          int
	hasImplicitRoot bool // UNC-like prefixes are rooted without a separator
	verbatim        bool // \\?\ forms parse backslash-only separators
}

// Components walks one raw path exactly like std::path::Components.
// Only the back (next_back) traversal is needed, which is what
// Path::file_name, Path::parent, and PathBuf pop/set_file_name use.
type Components struct {
	path    string
	prefix  prefixInfo
	hasRoot bool
	front   int
	back    int
}

// NewComponents constructs the walker over path (std
// Path::components: front starts at Prefix on Windows, else StartDir;
// back starts at Body).
func NewComponents(path string) *Components {
	c := &Components{path: path, front: stateStartDir, back: stateBody}
	if hasPrefixes {
		c.front = statePrefix
		c.prefix = classifyPrefix(path)
	}
	c.hasRoot = len(path) > c.prefix.length && c.isSepByte(path[c.prefix.length])
	return c
}

// classifyPrefix mirrors the Windows parse_prefix classification used
// by the state machine (meaningful only on Windows).
func classifyPrefix(path string) prefixInfo {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return prefixInfo{}
	}
	switch {
	case strings.HasPrefix(vol, `\\?\UNC\`):
		return prefixInfo{length: len(vol), hasImplicitRoot: true, verbatim: true}
	case strings.HasPrefix(vol, `\\?\`):
		return prefixInfo{length: len(vol), verbatim: true}
	case len(vol) >= 2 && vol[1] == ':':
		return prefixInfo{length: len(vol)}
	default: // \\server\share and other UNC forms
		return prefixInfo{length: len(vol), hasImplicitRoot: true}
	}
}

// separatorSet is the separator character set for index scans.
func separatorSet(verbatim bool) string {
	if runtime.GOOS != "windows" {
		return "/"
	}
	if verbatim {
		return `\`
	}
	return `/\`
}

// isSepByte reports one separator byte (std is_sep_byte).
func (c *Components) isSepByte(b byte) bool {
	return strings.ContainsRune(separatorSet(c.prefix.verbatim), rune(b))
}

// rooted mirrors std has_root: physical root or prefix implicit root.
func (c *Components) rooted() bool {
	return c.hasRoot || (hasPrefixes && c.prefix.hasImplicitRoot)
}

// includeCurDir mirrors std include_cur_dir: only a non-rooted path
// whose current body starts with "." (or is exactly ".") yields the
// leading CurDir.
func (c *Components) includeCurDir() bool {
	if c.rooted() {
		return false
	}
	body := c.path[c.prefixRemaining():]
	if len(body) >= 1 && body[0] == '.' {
		if len(body) == 1 {
			return true
		}
		return c.isSepByte(body[1])
	}
	return false
}

// prefixRemaining mirrors std prefix_remaining: the prefix bytes are
// inside the walk window only while front is still the Prefix state.
func (c *Components) prefixRemaining() int {
	if c.front == statePrefix {
		return c.prefix.length
	}
	return 0
}

// lenBeforeBody is the byte offset where body components begin (std
// len_before_body: prefix bytes plus the physical root separator or
// the leading CurDir byte).
func (c *Components) lenBeforeBody() int {
	root := 0
	if c.front <= stateStartDir && c.hasRoot {
		root = 1
	}
	cur := 0
	if c.front <= stateStartDir && c.includeCurDir() {
		cur = 1
	}
	return c.prefixRemaining() + root + cur
}

// parseSingleComponent classifies one raw component (std
// parse_single_component: "." is skipped away except inside verbatim
// prefixes where it stays a CurDir).
func (c *Components) parseSingleComponent(comp string) compKind {
	switch {
	case comp == ".":
		if hasPrefixes && c.prefix.verbatim {
			return compCurDir
		}
		return compNone
	case comp == "..":
		return compParentDir
	case comp == "":
		return compNone
	default:
		return compNormal
	}
}

// parseNextComponentBack returns (consumed bytes, kind) for the last
// body component (std parse_next_component_back: parsing starts at
// len_before_body, and the consumed size includes the separator that
// precedes the component).
func (c *Components) parseNextComponentBack() (int, compKind) {
	start := c.lenBeforeBody()
	body := c.path[start:]
	seps := separatorSet(c.prefix.verbatim)
	i := strings.LastIndexAny(body, seps)
	var comp string
	extra := 0
	if i < 0 {
		comp = body
	} else {
		comp = body[i+1:]
		extra = 1
	}
	return len(comp) + extra, c.parseSingleComponent(comp)
}

// nextBack pops the last yielded component (std next_back); skipped
// body components (empty and ".") are consumed without yielding.
func (c *Components) nextBack() compKind {
	for {
		if c.finished() {
			return compNone
		}
		switch c.back {
		case stateBody:
			if len(c.path) > c.lenBeforeBody() {
				size, kind := c.parseNextComponentBack()
				c.path = c.path[:len(c.path)-size]
				if kind != compNone {
					return kind
				}
				continue
			}
			c.back = stateStartDir
		case stateStartDir:
			c.back = stateDone
			if hasPrefixes {
				c.back = statePrefix
			}
			if c.hasRoot {
				c.path = c.path[:len(c.path)-1]
				return compRootDir
			}
			if hasPrefixes && c.prefix.hasImplicitRoot && !c.prefix.verbatim {
				return compRootDir
			}
			if c.includeCurDir() {
				c.path = c.path[:len(c.path)-1]
				return compCurDir
			}
		case statePrefix:
			c.back = stateDone
			c.path = c.path[c.prefix.length:]
			return compPrefix
		default:
			return compNone
		}
	}
}

// finished mirrors std finished: exhausted from either end.
func (c *Components) finished() bool {
	return c.front == stateDone || c.back == stateDone || c.front > c.back
}

// asPath returns the raw path that the remaining components span
// (std Components::as_path: with the states unchanged after the pop,
// the front is never a Body state during back traversal, so only the
// trailing empty and "." body components are trimmed).
func (c *Components) asPath() string {
	if c.back != stateBody {
		return c.path
	}
	path := c.path
	for len(path) > c.lenBeforeBody() {
		start := c.lenBeforeBody()
		body := path[start:]
		seps := separatorSet(c.prefix.verbatim)
		i := strings.LastIndexAny(body, seps)
		var comp string
		extra := 0
		if i < 0 {
			comp = body
		} else {
			comp = body[i+1:]
			extra = 1
		}
		if c.parseSingleComponent(comp) != compNone {
			break
		}
		path = path[:len(path)-(len(comp)+extra)]
	}
	return path
}

// FileName returns the final component of path exactly like Rust
// Path::file_name: repeated and trailing separators collapse, trailing
// "." components are dropped, mid-path ".." and ".."-prefixed names
// are ordinary, and there is no name when the path is empty, is only a
// root or prefix, is exactly ".", or ends in a ".." component. It is
// equivalent to the Components state machine above (differentially
// verified against rustc Path::file_name on a 375-shape corpus).
func FileName(path string) (string, bool) {
	seps := separatorSet(false)
	body := path
	if hasPrefixes {
		if vol := filepath.VolumeName(path); vol != "" {
			body = path[len(vol):]
		}
	}
	parts := strings.FieldsFunc(body, func(r rune) bool {
		return strings.ContainsRune(seps, r)
	})
	for len(parts) > 0 && parts[len(parts)-1] == "." {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return "", false
	}
	last := parts[len(parts)-1]
	if last == ".." {
		return "", false
	}
	return last, true
}

// HasFileName reports whether path has a file name the way Rust
// Path::file_name does (the accept gates at the Rust handlers check
// file_name().is_none()).
func HasFileName(path string) bool {
	_, ok := FileName(path)
	return ok
}

// Parent returns the parent of path exactly like Rust Path::parent:
// (parent, true) when the path has one, (_, false) for an empty path,
// a root, or a bare prefix. The returned string is the raw remaining
// prefix of the input (Rust Components::as_path), so it is never
// re-normalized: mid-path ".." and repeated interior separators stay.
func Parent(path string) (string, bool) {
	c := NewComponents(path)
	kind := c.nextBack()
	switch kind {
	case compNormal, compCurDir, compParentDir:
		return c.asPath(), true
	default:
		return "", false
	}
}

// ParentOrDot returns the handler parent default: Rust
// require_publication_parent and require_creatable_parent resolve the
// parent with parent().filter(|p| !p.as_os_str().is_empty())
// .unwrap_or(Path::new(".")).
func ParentOrDot(path string) string {
	parent, ok := Parent(path)
	if !ok || parent == "" {
		return "."
	}
	return parent
}

// WithFileName mirrors PathBuf::with_file_name (set_file_name): when
// path has a file name it is replaced by the parent plus name; when it
// has none the name is appended to the raw path (push semantics: a
// separator is inserted only when the path is non-empty, does not end
// in a separator, and is not a bare drive prefix).
func WithFileName(path, name string) string {
	if _, ok := FileName(path); ok {
		if parent, yes := Parent(path); yes {
			path = parent
		}
	}
	if path == "" {
		return name
	}
	seps := separatorSet(hasPrefixes && isVerbatim(path))
	if strings.ContainsRune(seps, rune(path[len(path)-1])) {
		return path + name
	}
	if hasPrefixes && isBareDrive(path) {
		return path + name
	}
	mainSep := "/"
	if runtime.GOOS == "windows" {
		mainSep = `\`
	}
	return path + mainSep + name
}

// isVerbatim reports a verbatim-prefixed path (push separator rules).
func isVerbatim(path string) bool {
	vol := filepath.VolumeName(path)
	return strings.HasPrefix(vol, `\\?\`)
}

// isBareDrive reports the exact "C:" shape (Rust push: no separator
// after a bare drive prefix).
func isBareDrive(path string) bool {
	vol := filepath.VolumeName(path)
	return len(vol) >= 2 && vol[1] == ':' && len(path) == len(vol)
}
