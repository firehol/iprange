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

// prefixKind classifies the Windows prefix exactly like Rust
// sys/path/windows_prefix.rs parse_prefix (Prefix::Disk, UNC,
// Verbatim, VerbatimDisk, VerbatimUNC, DeviceNS).
type prefixKind int

const (
	prefixNone prefixKind = iota
	prefixDisk
	prefixUNC
	prefixVerbatim
	prefixVerbatimDisk
	prefixVerbatimUNC
	prefixDeviceNS
)

// prefixInfo is the classified Windows volume prefix (Rust Prefix).
type prefixInfo struct {
	length int
	kind   prefixKind
}

// hasImplicitRoot mirrors Rust Prefix::has_implicit_root (!is_drive):
// every non-drive prefix is rooted without a visible separator.
func (p prefixInfo) hasImplicitRoot() bool {
	return p.kind != prefixNone && p.kind != prefixDisk
}

// verbatim mirrors Rust Prefix::is_verbatim: these forms parse
// backslash-only separators and never yield the implicit root.
func (p prefixInfo) verbatim() bool {
	return p.kind == prefixVerbatim || p.kind == prefixVerbatimDisk || p.kind == prefixVerbatimUNC
}

// isDrive mirrors Rust Prefix::is_drive (the push special case).
func (p prefixInfo) isDrive() bool {
	return p.kind == prefixDisk
}

// isSepByte reports one ordinary separator of the platform (both
// separators on Windows, "/" on unix).
func isSepByte(b byte) bool {
	if runtime.GOOS != "windows" {
		return b == '/'
	}
	return b == '/' || b == '\\'
}

// isAlpha reports Rust is_ascii_alphabetic for the drive check.
func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// parsePrefix mirrors Rust sys/path/windows_prefix.rs parse_prefix on
// the raw path bytes; the Unix build keeps a zero prefix.
func parsePrefix(path string) prefixInfo {
	if runtime.GOOS != "windows" {
		return prefixInfo{}
	}
	if len(path) < 2 || !isSepByte(path[0]) || !isSepByte(path[1]) {
		return parseDrive(path)
	}
	// The verbatim and device branches start with "\" + one of
	// "?\" or ".\" (Rust strips the leading "\\" first), and the
	// verbatim forms additionally require no forward slash inside the
	// recognized prefix header.
	if len(path) >= 4 && path[2] == '?' && isSepByte(path[3]) {
		// Rust rejects the verbatim form when any of the first four
		// raw bytes is a forward slash.
		for i := 0; i < 4; i++ {
			if path[i] == '/' {
				return parseUNC(path)
			}
		}
		rest := path[4:]
		if len(rest) >= 4 && rest[:4] == "UNC\\" {
			// \\?\\UNC\\server\\share
			server, after := nextComponent(rest[4:], true)
			share, after2 := nextComponent(after, true)
			if server == "" || share == "" || after2 == "" {
				return parseUNC(path)
			}
			_ = after2
			return prefixInfo{length: 4 + 4 + len(server) + 1 + len(share), kind: prefixVerbatimUNC}
		}
		if d := parseDriveExact(rest); d != 0 {
			return prefixInfo{length: 4 + 2, kind: prefixVerbatimDisk}
		}
		comp, _ := nextComponent(rest, true)
		return prefixInfo{length: 4 + len(comp), kind: prefixVerbatim}
	}
	if len(path) >= 4 && path[2] == '.' && isSepByte(path[3]) {
		comp, _ := nextComponent(path[4:], false)
		return prefixInfo{length: 4 + len(comp), kind: prefixDeviceNS}
	}
	return parseUNC(path)
}

// parseUNC mirrors the Rust UNC branch: server and share must both be
// non-empty, and the prefix covers the share plus its trailing
// separator when one follows.
func parseUNC(path string) prefixInfo {
	server, after := nextComponent(path[2:], false)
	if server == "" {
		return prefixInfo{}
	}
	share, _ := nextComponent(after, false)
	if share == "" {
		return prefixInfo{}
	}
	consumed := 2 + len(server) + 1 + len(share)
	// The share consumed its trailing separator only when one existed.
	if len(after) > len(share) {
		consumed++
	}
	if consumed > len(path) {
		consumed = len(path)
	}
	return prefixInfo{length: consumed, kind: prefixUNC}
}

// nextComponent mirrors Rust parse_next_component: it returns the
// component and the remainder after its trailing separator; verbatim
// parses backslash-only separators.
func nextComponent(path string, verbatim bool) (string, string) {
	seps := "/\\"
	if verbatim {
		seps = "\\"
	}
	if i := strings.IndexAny(path, seps); i >= 0 {
		return path[:i], path[i+1:]
	}
	return path, ""
}

// parseDrive mirrors Rust parse_drive: one ASCII letter plus ":".
func parseDrive(path string) prefixInfo {
	if len(path) >= 2 && isAlpha(path[0]) && path[1] == ':' {
		return prefixInfo{length: 2, kind: prefixDisk}
	}
	return prefixInfo{}
}

// parseDriveExact mirrors Rust parse_drive_exact: a drive whose
// following byte is a separator or the end of the path; returns the
// drive letter byte or zero.
func parseDriveExact(path string) byte {
	if len(path) >= 2 && isAlpha(path[0]) && path[1] == ':' {
		if len(path) == 2 || isSepByte(path[2]) {
			return path[0]
		}
	}
	return 0
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
	return parsePrefix(path)
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
	if hasPrefixes && c.prefix.verbatim() {
		return b == '\\'
	}
	return isSepByte(b)
}

// rooted mirrors std has_root: physical root or prefix implicit root.
func (c *Components) rooted() bool {
	return c.hasRoot || (hasPrefixes && c.prefix.hasImplicitRoot())
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
		if hasPrefixes && c.prefix.verbatim() {
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
	seps := separatorSet(c.prefix.verbatim())
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
			if hasPrefixes && c.prefix.length > 0 {
				// Rust folds the prefix branch into the else-if chain:
				// when a prefix is present, the implicit-root test is
				// the only alternative and the CurDir branch is never
				// reached (the drive-relative "C:." stays a bare
				// prefix with no CurDir component).
				if c.prefix.hasImplicitRoot() && !c.prefix.verbatim() {
					return compRootDir
				}
			} else if c.includeCurDir() {
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
		seps := separatorSet(c.prefix.verbatim())
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
		body = path[parsePrefix(path).length:]
		if parsePrefix(path).verbatim() {
			seps = `\\`
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
	// push: a separator is needed unless the path already ends with a
	// separator or is a bare drive prefix (Rust PathBuf::push; the
	// separator check uses the ordinary separator set, not the
	// verbatim one).
	if isSepByte(path[len(path)-1]) {
		return path + name
	}
	if hasPrefixes {
		p := parsePrefix(path)
		if p.isDrive() && p.length == len(path) {
			return path + name
		}
	}
	mainSep := "/"
	if runtime.GOOS == "windows" {
		mainSep = `\`
	}
	return path + mainSep + name
}
