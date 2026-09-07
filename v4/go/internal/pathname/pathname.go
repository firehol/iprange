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
		// Rust PrefixParser::get_prefix normalizes the first eight
		// bytes ( "/" -> "\\") before any strip_prefix, so a
		// forward slash in the "UNC\\" header matches too
		// ("\\?\\UNC/server/share" is still VerbatimUNC); the raw
		// "/" rejection above covers only the first four bytes.
		header := rest
		if len(header) > 4 {
			header = header[:4]
		}
		headerNorm := strings.Map(func(r rune) rune {
			if r == '/' {
				return '\\'
			}
			return r
		}, header)
		if len(rest) >= 4 && headerNorm == "UNC\\" {
			// \\?\\UNC\\server\\share.  Rust returns
			// VerbatimUNC(server, share) unconditionally (windows_prefix.rs
			// parse_prefix): a share-terminal path has no body, so it has
			// no file name or parent, and with_file_name appends after the
			// share.  Reparsing as a plain UNC (the previous guard) turned
			// the share into a file name and dropped it from derived
			// paths.
			server, after := nextComponent(rest[4:], true)
			share, _ := nextComponent(after, true)
			// Rust Prefix::len for VerbatimUNC counts the separator
			// before the share only when the share is non-empty.
			consumed := 4 + 4 + len(server)
			if share != "" {
				consumed += 1 + len(share)
			}
			if consumed > len(path) {
				consumed = len(path)
			}
			return prefixInfo{length: consumed, kind: prefixVerbatimUNC}
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
	// Rust Prefix::len(UNC) = 2 + server + 1 + share: the share's
	// trailing separator (when one follows) is not part of the prefix;
	// it stays in the body where has_physical_root consumes it as the
	// root byte. Absorbing it here kept doubled separators in derived
	// parent and sidecar paths for doubled-separator share spellings.
	consumed := 2 + len(server) + 1 + len(share)
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
//
// The physical-root check mirrors std has_physical_root, which uses
// the static separator set even after a verbatim prefix: a "/" right
// after the "\\?\\" prefix (hand-built long-path spelling) is the
// root, while body components after it still split on "\\" only.
func NewComponents(path string) *Components {
	c := &Components{path: path, front: stateStartDir, back: stateBody}
	if hasPrefixes {
		c.front = statePrefix
		c.prefix = classifyPrefix(path)
	}
	c.hasRoot = len(path) > c.prefix.length && isSepByte(path[c.prefix.length])
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

// nextFront pops the first yielded component (std Components::next)
// and returns its kind and raw bytes (separators excluded). Repeated
// separators and skipped "." components are consumed silently, and a
// physical root is consumed as exactly one byte (std
// has_physical_root + StartDir). The Prefix component returns the raw
// prefix bytes.
func (c *Components) nextFront() (compKind, string) {
	for {
		if c.finished() {
			return compNone, ""
		}
		switch c.front {
		case stateBody:
			if len(c.path) > 0 {
				seps := separatorSet(c.prefix.verbatim())
				extra := 0
				var comp string
				if i := strings.IndexAny(c.path, seps); i >= 0 {
					comp = c.path[:i]
					extra = 1
				} else {
					comp = c.path
				}
				kind := c.parseSingleComponent(comp)
				c.path = c.path[len(comp)+extra:]
				if kind != compNone {
					return kind, comp
				}
				continue
			}
			c.front = stateDone
		case stateStartDir:
			c.front = stateBody
			if c.hasRoot {
				c.path = c.path[1:]
				return compRootDir, ""
			}
			if hasPrefixes && c.prefix.length > 0 {
				if c.prefix.hasImplicitRoot() && !c.prefix.verbatim() {
					return compRootDir, ""
				}
			} else if c.includeCurDir() {
				c.path = c.path[1:]
				return compCurDir, "."
			}
		case statePrefix:
			if c.prefix.length == 0 {
				c.front = stateStartDir
				continue
			}
			c.front = stateStartDir
			raw := c.path[:c.prefix.length]
			c.path = c.path[c.prefix.length:]
			return compPrefix, raw
		default:
			return compNone, ""
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
// "." components are dropped (except inside a verbatim prefix, where
// "." stays a CurDir component and yields no name), mid-path ".." and
// ".."-prefixed names are ordinary, and there is no name when the path
// is empty, is only a root or prefix, is exactly ".", or ends in a
// ".." component. A verbatim prefix may be followed by a "/" physical
// root byte (std has_physical_root uses the static separator set even
// for verbatim prefixes), which is consumed before body parsing. It is
// equivalent to the Components state machine above (differentially
// verified against rustc Path::file_name on a 375-shape corpus).
func FileName(path string) (string, bool) {
	seps := separatorSet(false)
	body := path
	verbatim := false
	if hasPrefixes {
		p := parsePrefix(path)
		body = path[p.length:]
		if p.verbatim() {
			seps = `\\`
			verbatim = true
			// std has_physical_root uses the static separator set even
			// for verbatim prefixes: a "/" directly after the prefix is
			// the root byte, not the start of the first body component.
			if len(body) > 0 && isSepByte(body[0]) {
				body = body[1:]
			}
		}
	}
	// Backward component walk with no allocation: scan from the end
	// over the body, skipping separator runs and (outside verbatim
	// paths) trailing "." components, and return the last remaining
	// component as a substring.  Equivalent to the FieldsFunc split
	// plus the Rust back walk: a trailing "." inside a verbatim
	// prefix is a CurDir component (no name), a trailing ".." has no
	// name, and mid-path "." / ".." components are ordinary.
	// (The Windows prefix parse above may allocate once for a
	// forward-slash verbatim header; the walk itself never does.)
	end := len(body)
	for {
		// Skip a trailing separator run.
		for end > 0 && strings.IndexByte(seps, body[end-1]) >= 0 {
			end--
		}
		if end == 0 {
			return "", false
		}
		start := end
		for start > 0 && strings.IndexByte(seps, body[start-1]) < 0 {
			start--
		}
		comp := body[start:end]
		if comp == "." {
			if verbatim {
				// Trailing "." inside a verbatim prefix stays a
				// CurDir component (std parse_single_component); the
				// back walk stops there and the path has no name.
				return "", false
			}
			// Ordinary paths normalize trailing "." away; keep
			// walking backward.
			end = start
			continue
		}
		if comp == ".." {
			return "", false
		}
		return comp, true
	}
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
	// Rust PathBuf::set_file_name replaces the final component via
	// _push(parent, name); Push implements the full _push contract
	// (need_clear replacement, the verbatim component fold, the
	// rooted-name truncate, and the separator rules).
	return Push(path, name)
}

// verbatimPushRebuild mirrors Rust PathBuf::_push's verbatim branch
// for with_file_name: the base components plus the appended name are
// re-emitted component by component with the main separator between
// them. The prefix raw bytes are preserved exactly as parsed (they may
// contain "/"), a physical root byte is re-emitted as the main
// separator, and the appended name follows directly after a RootDir.
func verbatimPushRebuild(base, name string) string {
	type part struct {
		kind compKind
		raw  string
	}
	collect := func(path string) []part {
		c := NewComponents(path)
		var parts []part
		for {
			kind, raw := c.nextFront()
			if kind == compNone {
				break
			}
			parts = append(parts, part{kind, raw})
		}
		return parts
	}
	// std PathBuf::_push verbatim branch: the buffer starts as the
	// base components and the pushed path's components fold in —
	// CurDir vanishes, ParentDir pops the last Normal component,
	// RootDir truncates the buffer to its prefix, and Prefix/Normal
	// components append — then the whole buffer is re-emitted with
	// the main separator.
	buf := collect(base)
	for _, p := range collect(name) {
		switch p.kind {
		case compCurDir:
			// std Component::CurDir => (): no-op.
		case compParentDir:
			// std pops only when the last buffer component is a
			// Normal (if let Some(Normal) = buf.last()): a trailing
			// CurDir or ParentDir component of the base stays.
			if len(buf) > 0 && buf[len(buf)-1].kind == compNormal {
				buf = buf[:len(buf)-1]
			}
		case compRootDir:
			// std truncate(1): a pushed root keeps only the
			// verbatim prefix, then appends the root.
			if len(buf) > 1 {
				buf = buf[:1]
			}
			buf = append(buf, p)
		default:
			buf = append(buf, p)
		}
	}
	var sb strings.Builder
	needSep := false
	for _, p := range buf {
		if needSep && p.kind != compRootDir {
			sb.WriteByte('\\')
		}
		switch p.kind {
		case compRootDir:
			sb.WriteByte('\\') // std Component::RootDir::as_os_str
			needSep = false
		case compPrefix:
			sb.WriteString(p.raw)
			// std sets need_sep = !prefix.is_drive() && prefix.len() > 0;
			// is_drive is Disk(_) only: a verbatim prefix always
			// needs the following separator, a pushed disk prefix
			// ("C:name") never does.
			drive := false
			if hasPrefixes {
				drive = parsePrefix(p.raw).isDrive()
			}
			needSep = !drive && len(p.raw) > 0
		default:
			sb.WriteString(p.raw)
			needSep = true
		}
	}
	return sb.String()
}

// Push mirrors the full Rust PathBuf::_push contract: an absolute or
// prefix-carrying pushed name replaces the base (std need_clear), a
// verbatim-prefixed base folds the pushed components with the main
// separator (verbatim branch), a rooted pushed name without a prefix
// truncates the base to its prefix, and a relative name appends after
// a main separator unless the base is empty, already ends with a
// separator, or is a bare drive prefix.  The four SDK join sites
// (temporary placement and "@"-expansion) always pass a plain
// separator-free name, which reaches the final appending arm.
func Push(base, name string) string {
	if name != "" {
		nc := NewComponents(name)
		nameHasPrefix := hasPrefixes && nc.prefix.kind != prefixNone
		nameAbs := nc.hasRoot && (!hasPrefixes || nameHasPrefix)
		if nameAbs || nameHasPrefix {
			// std need_clear: inner.clear() then push the raw
			// pushed bytes (no separator is ever needed).
			return name
		}
		if hasPrefixes && parsePrefix(base).verbatim() {
			return verbatimPushRebuild(base, name)
		}
		if nc.hasRoot {
			// std: a rooted name without a prefix truncates the
			// base to its prefix ("C:\\x" + "\\n" -> "C:\\n").
			prefix := 0
			if hasPrefixes {
				prefix = parsePrefix(base).length
			}
			return base[:prefix] + name
		}
	}
	needSep := len(base) > 0 && !isSepByte(base[len(base)-1])
	if needSep && hasPrefixes {
		if p := parsePrefix(base); p.isDrive() && p.length == len(base) {
			needSep = false
		}
	}
	if !needSep {
		return base + name
	}
	mainSep := "/"
	if runtime.GOOS == "windows" {
		mainSep = `\`
	}
	return base + mainSep + name
}
