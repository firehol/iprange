// Streaming parser for released legacy-compatible IP-range text and
// binary inputs (Rust iprange-cli/src/io/input.rs parity).
//
// The parser keeps only one bounded line and one bounded range batch in
// memory. current.publish drains it through the immutable-feed builder;
// no caller receives a complete materialized feed. Text lines, the
// released v1/v2 binary payloads, @-file-list/directory expansion,
// legacy inet_aton forms, DNS hostnames, and family/network-fixing
// normalization mirror the released legacy parsers exactly.

package fileio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	iprangedb "github.com/firehol/iprange/v4/go"
)

const (
	// batchCapacity bounds one drained range batch; the source returns
	// a batch as soon as it fills so no feed is ever retained by the
	// adapter (Rust BATCH_CAPACITY).
	batchCapacity = 256
	// hostnameBatchCapacity bounds the buffered hostnames before DNS
	// work (Rust HOSTNAME_BATCH_CAPACITY).
	hostnameBatchCapacity = 4096
)

var (
	binaryV4Header = []byte("iprange binary format v1.0")
	binaryV6Header = []byte("iprange binary format v2.0")
)

// endianMarker is the released payload endianness marker value.
const endianMarker = 0x1a2b_3c4d

// AddressFamilyInput selects the address family of one text input.
type AddressFamilyInput uint8

const (
	AddressFamilyInputIPv4 AddressFamilyInput = iota
	AddressFamilyInputIPv6
)

// TextInputOptions carries the released text-input controls that affect
// address normalization (Rust TextInputOptions).
type TextInputOptions struct {
	Family        AddressFamilyInput
	FixNetwork    bool
	DefaultPrefix uint32
	DNSThreads    int
	DNSSilent     bool
	MaxLineBytes  int
}

// inputErrorKind is the stable adapter classification of one source
// failure (Rust InputErrorKind).
type inputErrorKind uint8

const (
	inputErrorInvalidPath inputErrorKind = iota
	inputErrorIO
	inputErrorFormat
)

// InputError is one bounded source failure with the stable adapter
// classification: invalid_path, io, or input_format.
type InputError struct {
	kind    inputErrorKind
	message string
}

// Code returns the stable wire adapter code of the failure.
func (e *InputError) Code() string {
	switch e.kind {
	case inputErrorInvalidPath:
		return "invalid_path"
	case inputErrorIO:
		return "io"
	default:
		return "input_format"
	}
}

// Error renders the failure message.
func (e *InputError) Error() string { return e.message }

// parsedRange is one inclusive canonical address interval in numeric
// order; ipv4 selects the 32-bit family so the generic core can refuse
// a cross-family range exactly like the Rust InputKey advertisement.
type parsedRange struct {
	fromHi, fromLo, toHi, toLo uint64
	ipv4                       bool
}

func (r parsedRange) from() uint128 { return uint128{hi: r.fromHi, lo: r.fromLo} }
func (r parsedRange) to() uint128   { return uint128{hi: r.toHi, lo: r.toLo} }

type parsedLineKind uint8

const (
	parsedEmpty parsedLineKind = iota
	parsedRangeLine
	parsedHostname
	parsedDroppedIPv6
)

type parsedLine struct {
	kind     parsedLineKind
	value    parsedRange
	hostname []byte
}

type activeText struct {
	reader      *bufio.Reader
	firstLine   bool
	droppedIPv6 uint64
	hostnames   []string
}

type activeBinary struct {
	reader         *bufio.Reader
	remaining      uint64
	optimized      bool
	expectedUnique uint128
	actualUnique   uint128
	previousTo     uint128
	hasPrevious    bool
}

type activeInput struct {
	text   *activeText
	binary *activeBinary
}

// textInputCore is the family-generic streaming source state; the
// typed exported sources wrap it with their batch conversion (Rust
// TextInputSource<K>).
type textInputCore[K any] struct {
	paths       []string
	activePath  string
	active      *activeInput
	options     TextInputOptions
	lineBuf     []byte
	batch       []K
	finished    bool
	lastCode    string
	lastMessage string
	convert     func(parsedRange) K
}

// TextInputSource4 streams one or more expanded input paths as bounded
// IPv4 range batches (implements iprangedb.RangeSource4).
type TextInputSource4 struct {
	core textInputCore[iprangedb.AddressRange4]
}

// TextInputSource6 streams one or more expanded input paths as bounded
// IPv6 range batches (implements iprangedb.RangeSource6).
type TextInputSource6 struct {
	core textInputCore[iprangedb.AddressRange6]
}

// NewTextInputSource4 builds an IPv4 streaming source over the given
// paths. The caller must not mutate `paths` after construction.
func NewTextInputSource4(paths []string, options TextInputOptions, expandAtPaths bool, maxExpandedPaths int) (*TextInputSource4, error) {
	source := &TextInputSource4{}
	if err := source.core.init(paths, options, expandAtPaths, maxExpandedPaths); err != nil {
		return nil, err
	}
	source.core.convert = func(value parsedRange) iprangedb.AddressRange4 {
		return iprangedb.AddressRange4{From: iprangedb.IPv4(value.fromLo), To: iprangedb.IPv4(value.toLo)}
	}
	return source, nil
}

// NewTextInputSource6 builds an IPv6 streaming source over the given
// paths. The caller must not mutate `paths` after construction.
func NewTextInputSource6(paths []string, options TextInputOptions, expandAtPaths bool, maxExpandedPaths int) (*TextInputSource6, error) {
	source := &TextInputSource6{}
	if err := source.core.init(paths, options, expandAtPaths, maxExpandedPaths); err != nil {
		return nil, err
	}
	source.core.convert = func(value parsedRange) iprangedb.AddressRange6 {
		return iprangedb.AddressRange6{
			FromHi: value.fromHi, FromLo: value.fromLo,
			ToHi: value.toHi, ToLo: value.toLo,
		}
	}
	return source, nil
}

// NextBatch returns the next non-empty IPv4 batch, nil at the end, or
// an exact source error (iprangedb.RangeSource4).
func (s *TextInputSource4) NextBatch() ([]iprangedb.AddressRange4, error) {
	return s.core.nextBatch()
}

// NextBatch returns the next non-empty IPv6 batch, nil at the end, or
// an exact source error (iprangedb.RangeSource6).
func (s *TextInputSource6) NextBatch() ([]iprangedb.AddressRange6, error) {
	return s.core.nextBatch()
}

// LastInputErrorCode returns the adapter classification of the most
// recent source failure ("" when none); the immutable-feed handler
// reports it when the SDK build failed because the input source failed
// (Rust TextInputSource::last_input_error).
func (s *TextInputSource4) LastInputErrorCode() string { return s.core.lastCode }

// LastInputErrorMessage returns the human message of the most recent
// source failure.
func (s *TextInputSource4) LastInputErrorMessage() string { return s.core.lastMessage }

// LastInputErrorCode returns the adapter classification of the most
// recent source failure ("" when none).
func (s *TextInputSource6) LastInputErrorCode() string { return s.core.lastCode }

// LastInputErrorMessage returns the human message of the most recent
// source failure.
func (s *TextInputSource6) LastInputErrorMessage() string { return s.core.lastMessage }

func (c *textInputCore[K]) init(paths []string, options TextInputOptions, expandAtPaths bool, maxExpandedPaths int) error {
	if len(paths) == 0 || len(paths) > maxExpandedPaths {
		return c.invalidPathError(fmt.Sprintf("input path count must be 1 through %d", maxExpandedPaths))
	}
	expanded, err := expandPaths(paths, expandAtPaths, maxExpandedPaths, options.MaxLineBytes)
	if err != nil {
		return err
	}
	c.paths = expanded
	c.options = options
	c.lineBuf = make([]byte, 0, 1024)
	return nil
}

// familyMatches reports whether one parsed range can be advertised by
// this core's family (Rust InputKey::family_matches); a mismatch is a
// format error surfaced by pushRange.
func (c *textInputCore[K]) familyMatches(value parsedRange) bool {
	if c.options.Family == AddressFamilyInputIPv4 {
		return value.ipv4
	}
	return !value.ipv4
}

func (c *textInputCore[K]) pathLabel() string {
	if c.activePath != "" {
		return c.activePath
	}
	return "input"
}

func (c *textInputCore[K]) nextBatch() ([]K, error) {
	if c.finished {
		return nil, nil
	}
	c.batch = c.batch[:0]
	for {
		if len(c.batch) >= batchCapacity {
			return c.batch, nil
		}
		if c.active == nil {
			opened, err := c.openNext()
			if err != nil {
				return nil, err
			}
			if !opened {
				c.finished = true
				if len(c.batch) == 0 {
					return nil, nil
				}
				return c.batch, nil
			}
		}
		unit, err := c.readStep()
		if err != nil {
			c.remember(err)
			return nil, err
		}
		switch unit.kind {
		case stepTextLine:
			line, err := parseTextLine(unit.text, c.options)
			if err != nil {
				return nil, c.formatError(err.Error())
			}
			if err := c.consumeParsed(line); err != nil {
				return nil, err
			}
		case stepTextFinished:
			var dropped uint64
			var hostnames []string
			if c.active != nil && c.active.text != nil {
				dropped = c.active.text.droppedIPv6
				hostnames = c.active.text.hostnames
				c.active = nil
			}
			if len(hostnames) > 0 {
				if err := c.resolveNames(hostnames); err != nil {
					return nil, err
				}
			}
			if dropped > 0 {
				fmt.Fprintf(os.Stderr, "iprange: %s: %d IPv6 entries dropped (use -6 for IPv6 mode)\n", c.pathLabel(), dropped)
			}
			c.activePath = ""
		case stepBinaryRecord:
			if c.active != nil && c.active.binary != nil {
				c.active.binary.remaining--
			}
			if err := c.pushRange(unit.value); err != nil {
				return nil, err
			}
		case stepBinaryEnd:
			c.active = nil
			c.activePath = ""
		}
	}
}

type stepKind uint8

const (
	stepTextLine stepKind = iota
	stepTextFinished
	stepBinaryRecord
	stepBinaryEnd
)

// step is one bounded unit of input progress; a text line lives in the
// core's reusable line buffer, so producing it never allocates.
type step struct {
	kind  stepKind
	text  []byte
	value parsedRange
}

func (c *textInputCore[K]) readStep() (*step, error) {
	if c.active == nil {
		return nil, c.formatError("input is not active")
	}
	if c.active.text != nil {
		if _, ok, err := readLimitedLine(c.active.text.reader, c.options.MaxLineBytes, &c.lineBuf); err != nil {
			return nil, err
		} else if ok {
			return &step{kind: stepTextLine, text: c.lineBuf}, nil
		}
		return &step{kind: stepTextFinished}, nil
	}
	binary := c.active.binary
	if binary.remaining == 0 {
		var trailing [1]byte
		count, _ := binary.reader.Read(trailing[:])
		if count != 0 {
			return nil, c.formatError("trailing data after binary payload")
		}
		if binary.optimized && binary.actualUnique != binary.expectedUnique {
			return nil, c.formatError("binary unique count does not match payload")
		}
		return &step{kind: stepBinaryEnd}, nil
	}
	ipv6 := c.options.Family == AddressFamilyInputIPv6
	size := 8
	if ipv6 {
		size = 32
	}
	record := make([]byte, size)
	if _, err := io.ReadFull(binary.reader, record); err != nil {
		return nil, c.ioError(fmt.Sprintf("read binary record: %v", err))
	}
	value, ok := binaryRecord(ipv6, record)
	if !ok {
		return nil, c.formatError("invalid binary range")
	}
	if compareU128(value.fromHi, value.fromLo, value.toHi, value.toLo) > 0 {
		return nil, c.formatError("binary range start exceeds end")
	}
	diff := subU128(value.toHi, value.toLo, value.fromHi, value.fromLo)
	unique, overflow := addU128(diff.hi, diff.lo, 0, 1)
	if overflow {
		return nil, c.formatError("binary range size overflows")
	}
	next, overflow := addU128(binary.actualUnique.hi, binary.actualUnique.lo, unique.hi, unique.lo)
	if overflow {
		return nil, c.formatError("binary unique count overflows")
	}
	if binary.optimized && binary.hasPrevious {
		// A released "optimized" payload is strictly ascending with
		// gaps; equal, overlapping, or adjacent records are invalid.
		if compareU128(value.fromHi, value.fromLo, binary.previousTo.hi, binary.previousTo.lo) <= 0 {
			return nil, c.formatError("optimized binary payload is unordered, overlapping, or adjacent")
		}
		nextFrom, overflow := addU128(binary.previousTo.hi, binary.previousTo.lo, 0, 1)
		if !overflow && nextFrom.hi == value.fromHi && nextFrom.lo == value.fromLo {
			return nil, c.formatError("optimized binary payload is unordered, overlapping, or adjacent")
		}
	}
	binary.actualUnique = next
	binary.previousTo = value.to()
	binary.hasPrevious = true
	return &step{kind: stepBinaryRecord, value: value}, nil
}

func (c *textInputCore[K]) openNext() (bool, error) {
	for len(c.paths) > 0 {
		path := c.paths[0]
		c.paths = c.paths[1:]
		file, err := openInput(path)
		if err != nil {
			// The message carries the path label exactly like the Rust
			// open_next mapping; the code and the remembered source
			// failure come from the open error.
			var inputErr *InputError
			if errors.As(err, &inputErr) {
				code := inputErr.Code()
				c.lastCode = code
				c.lastMessage = path + ": " + inputErr.message
				return false, &InputError{kind: inputErr.kind, message: c.lastMessage}
			}
			return false, err
		}
		reader := bufio.NewReaderSize(file, 64*1024)
		var first []byte
		syntheticNewline := false
		hadNewline, ok, err := readLimitedLine(reader, c.options.MaxLineBytes, &first)
		if err != nil {
			_ = file.Close()
			return false, err
		}
		if !ok {
			// An empty file contributes nothing (Rust skips it).
			_ = file.Close()
			continue
		}
		c.activePath = path
		if hadNewline {
			if c.options.Family == AddressFamilyInputIPv6 {
				first = stripBOM(first)
			}
			if equalBytes(first, binaryV4Header) {
				if c.options.Family == AddressFamilyInputIPv6 {
					return false, c.formatError("IPv4 binary file cannot load in IPv6 mode")
				}
				if err := c.openBinary(reader, false); err != nil {
					return false, err
				}
				return true, nil
			}
			if equalBytes(first, binaryV6Header) {
				if c.options.Family == AddressFamilyInputIPv4 {
					return false, c.formatError("IPv6 binary file cannot load in IPv4 mode")
				}
				if err := c.openBinary(reader, true); err != nil {
					return false, err
				}
				return true, nil
			}
		} else {
			// The final unterminated line gets a synthetic newline so
			// the released text parsers still accept it.
			first = append(first, '\n')
			if c.options.Family == AddressFamilyInputIPv6 {
				first = stripBOM(first)
			}
			syntheticNewline = true
		}
		c.active = &activeInput{text: &activeText{reader: reader, firstLine: true}}
		if syntheticNewline {
			first = first[:len(first)-1]
		}
		parsed, parseErr := parseTextLine(first, c.options)
		if parseErr != nil {
			return false, c.formatError(parseErr.Error())
		}
		if err := c.consumeParsed(parsed); err != nil {
			return false, err
		}
		if c.active != nil && c.active.text != nil {
			c.active.text.firstLine = false
		}
		return true, nil
	}
	return false, nil
}

func (c *textInputCore[K]) openBinary(reader *bufio.Reader, ipv6 bool) error {
	recordSize := uint64(8)
	if ipv6 {
		recordSize = 32
	}
	if ipv6 {
		line, err := binaryLine(reader, c.options.MaxLineBytes)
		if err != nil {
			return err
		}
		if !equalBytes(line, []byte("ipv6")) {
			return c.formatError("invalid binary header line")
		}
	}
	var optimized bool
	line, err := binaryLine(reader, c.options.MaxLineBytes)
	if err != nil {
		return err
	}
	switch {
	case equalBytes(line, []byte("optimized")):
		optimized = true
	case equalBytes(line, []byte("non-optimized")):
		optimized = false
	default:
		return c.formatError("invalid binary optimized flag")
	}
	expectedSize, err := binaryNumber(reader, []byte("record size "), c.options.MaxLineBytes)
	if err != nil {
		return err
	}
	if expectedSize == nil || expectedSize.hi != 0 || expectedSize.lo != recordSize {
		return c.formatError("invalid binary record size")
	}
	records, err := binaryNumber(reader, []byte("records "), c.options.MaxLineBytes)
	if err != nil {
		return err
	}
	if records == nil {
		return c.formatError("invalid binary record count")
	}
	bytes, err := binaryNumber(reader, []byte("bytes "), c.options.MaxLineBytes)
	if err != nil {
		return err
	}
	if bytes == nil {
		return c.formatError("invalid binary byte count")
	}
	lines, err := binaryNumber(reader, []byte("lines "), c.options.MaxLineBytes)
	if err != nil {
		return err
	}
	if lines == nil {
		return c.formatError("invalid binary line count")
	}
	expectedUnique, err := binaryNumber(reader, []byte("unique ips "), c.options.MaxLineBytes)
	if err != nil {
		return err
	}
	if expectedUnique == nil {
		return c.formatError("invalid binary unique count")
	}
	// The payload is the 4-byte endianness marker plus one record per
	// declared record count.
	payload, overflow := mulU128(records.hi, records.lo, recordSize)
	if overflow {
		return c.formatError("binary byte count overflows")
	}
	payload, overflow = addU128(payload.hi, payload.lo, 0, 4)
	if overflow {
		return c.formatError("binary byte count overflows")
	}
	if *bytes != payload {
		return c.formatError("binary byte count does not match records")
	}
	remaining, overflow := toUint64(*records)
	if overflow {
		return c.formatError("binary record count exceeds the platform bound")
	}
	if compareU128(lines.hi, lines.lo, records.hi, records.lo) < 0 {
		return c.formatError("binary line count is below record count")
	}
	if compareU128(expectedUnique.hi, expectedUnique.lo, records.hi, records.lo) < 0 &&
		!(ipv6 && expectedUnique.hi == 0 && expectedUnique.lo == 0) {
		return c.formatError("binary unique count is below record count")
	}
	var marker [4]byte
	if _, err := io.ReadFull(reader, marker[:]); err != nil {
		return c.ioError(fmt.Sprintf("binary endianness marker: %v", err))
	}
	if binary.NativeEndian.Uint32(marker[:]) != endianMarker {
		return c.formatError("binary endianness is incompatible")
	}
	c.active = &activeInput{binary: &activeBinary{
		reader:         reader,
		remaining:      remaining,
		optimized:      optimized,
		expectedUnique: *expectedUnique,
	}}
	return nil
}

func (c *textInputCore[K]) consumeParsed(parsed parsedLine) error {
	switch parsed.kind {
	case parsedEmpty:
		return nil
	case parsedDroppedIPv6:
		if c.active != nil && c.active.text != nil {
			c.active.text.droppedIPv6++
		}
		return nil
	case parsedRangeLine:
		return c.pushRange(parsed.value)
	case parsedHostname:
		var names []string
		if c.active != nil && c.active.text != nil {
			hostnames := &c.active.text.hostnames
			if len(*hostnames) < hostnameBatchCapacity {
				*hostnames = append(*hostnames, string(parsed.hostname))
			} else {
				// Swap the full buffer out for resolution and keep the
				// new hostname in the reusable buffer (Rust mem::swap).
				names, *hostnames = *hostnames, []string{string(parsed.hostname)}
			}
		} else {
			return c.formatError("text input is not active")
		}
		if names != nil {
			return c.resolveNames(names)
		}
		return nil
	}
	return nil
}

func (c *textInputCore[K]) resolveNames(names []string) error {
	addresses, err := resolveHostnames(names, c.options.Family, c.options.DNSThreads, c.options.DNSSilent)
	if err != nil {
		return c.formatError(err.Error())
	}
	for _, address := range addresses {
		value, err := dnsRange(address, c.options.Family)
		if err != nil {
			return c.formatError(err.Error())
		}
		if err := c.pushRange(value); err != nil {
			return err
		}
	}
	return nil
}

func (c *textInputCore[K]) pushRange(value parsedRange) error {
	if len(c.batch) >= batchCapacity || !c.familyMatches(value) {
		return c.formatError("range does not fit the bounded parser batch/family")
	}
	c.batch = append(c.batch, c.convert(value))
	return nil
}

func (c *textInputCore[K]) formatError(message string) error {
	c.lastCode = "input_format"
	c.lastMessage = message
	return &InputError{kind: inputErrorFormat, message: message}
}

func (c *textInputCore[K]) ioError(message string) error {
	c.lastCode = "io"
	c.lastMessage = message
	return &InputError{kind: inputErrorIO, message: message}
}

func (c *textInputCore[K]) invalidPathError(message string) error {
	c.lastCode = "invalid_path"
	c.lastMessage = message
	return &InputError{kind: inputErrorInvalidPath, message: message}
}

func (c *textInputCore[K]) remember(err error) error {
	if typed, ok := err.(*InputError); ok {
		c.lastCode = typed.Code()
		c.lastMessage = typed.message
	}
	return err
}

func expandPaths(paths []string, expandAtPaths bool, maxExpandedPaths, maxLineBytes int) ([]string, error) {
	var expanded []string
	push := func(path string) error {
		if len(expanded) >= maxExpandedPaths {
			return &InputError{kind: inputErrorInvalidPath,
				message: fmt.Sprintf("@-expansion exceeds the maximum of %d paths", maxExpandedPaths)}
		}
		expanded = append(expanded, path)
		return nil
	}
	for _, path := range paths {
		if !expandAtPaths || !strings.HasPrefix(path, "@") {
			if err := push(path); err != nil {
				return nil, err
			}
			continue
		}
		referenced := path[1:]
		info, err := os.Lstat(referenced)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, &InputError{kind: inputErrorInvalidPath,
					message: fmt.Sprintf("file-list or directory does not exist: %s", referenced)}
			}
			return nil, &InputError{kind: inputErrorIO,
				message: fmt.Sprintf("inspect file-list or directory %s: %v", referenced, err)}
		}
		if info.IsDir() {
			entries, err := os.ReadDir(referenced)
			if err != nil {
				return nil, &InputError{kind: inputErrorIO,
					message: fmt.Sprintf("read directory %s: %v", referenced, err)}
			}
			var files []string
			for _, entry := range entries {
				entryPath := filepath.Join(referenced, entry.Name())
				entryInfo, statErr := os.Stat(entryPath)
				if statErr == nil && entryInfo.Mode().IsRegular() {
					files = append(files, entryPath)
				}
			}
			if len(files) == 0 {
				return nil, &InputError{kind: inputErrorInvalidPath,
					message: fmt.Sprintf("directory contains no regular files: %s", referenced)}
			}
			sort.Strings(files)
			for _, entry := range files {
				if err := push(entry); err != nil {
					return nil, err
				}
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, &InputError{kind: inputErrorInvalidPath,
				message: fmt.Sprintf("file list is not a regular file: %s", referenced)}
		}
		file, err := os.Open(referenced)
		if err != nil {
			return nil, &InputError{kind: inputErrorIO,
				message: fmt.Sprintf("open file list %s: %v", referenced, err)}
		}
		reader := bufio.NewReaderSize(file, 64*1024)
		loaded := false
		for {
			var line []byte
			_, ok, err := readLimitedLine(reader, maxLineBytes, &line)
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			if !ok {
				break
			}
			trimmed := trimFileListLine(line)
			if len(trimmed) == 0 {
				continue
			}
			if err := push(string(trimmed)); err != nil {
				_ = file.Close()
				return nil, err
			}
			loaded = true
		}
		_ = file.Close()
		if !loaded {
			return nil, &InputError{kind: inputErrorInvalidPath,
				message: fmt.Sprintf("file list contains no paths: %s", referenced)}
		}
	}
	return expanded, nil
}

func trimFileListLine(line []byte) []byte {
	start := 0
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	rest := line[start:]
	if len(rest) > 0 && (rest[0] == '#' || rest[0] == ';' || rest[0] == '\r') {
		return nil
	}
	end := len(rest)
	for end > 0 && (rest[end-1] == ' ' || rest[end-1] == '\t' || rest[end-1] == '\r') {
		end--
	}
	return rest[:end]
}

func openInput(path string) (*os.File, *InputError) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &InputError{kind: inputErrorInvalidPath, message: "input does not exist: " + path}
		}
		return nil, &InputError{kind: inputErrorIO, message: fmt.Sprintf("inspect input %s: %v", path, err)}
	}
	if !info.Mode().IsRegular() {
		return nil, &InputError{kind: inputErrorInvalidPath, message: "input is not a regular file: " + path}
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &InputError{kind: inputErrorInvalidPath, message: "input does not exist: " + path}
		}
		return nil, &InputError{kind: inputErrorIO, message: fmt.Sprintf("open input %s: %v", path, err)}
	}
	return file, nil
}

// readLimitedLine reads one physical line without its LF terminator
// into the reusable output buffer (Rust read_limited_line): ok=false
// reports EOF with nothing pending; hadNewline distinguishes a final
// unterminated line from a terminated one.
func readLimitedLine(reader *bufio.Reader, maxLineBytes int, output *[]byte) (hadNewline bool, ok bool, err *InputError) {
	*output = (*output)[:0]
	for {
		chunk, readErr := reader.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			if herr := appendLimited(output, chunk, maxLineBytes); herr != nil {
				return false, false, herr
			}
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return false, false, &InputError{kind: inputErrorIO, message: readErr.Error()}
		}
		terminated := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		if terminated {
			chunk = chunk[:len(chunk)-1]
		}
		if len(chunk) > 0 {
			if herr := appendLimited(output, chunk, maxLineBytes); herr != nil {
				return false, false, herr
			}
			return terminated, true, nil
		}
		if readErr == io.EOF {
			return false, false, nil
		}
		// A bare LF at EOF produced an empty terminated line.
		return true, true, nil
	}
}

func appendLimited(output *[]byte, bytes []byte, maxLineBytes int) *InputError {
	if len(*output)+len(bytes) > maxLineBytes {
		return &InputError{kind: inputErrorFormat,
			message: fmt.Sprintf("input line exceeds %d bytes", maxLineBytes)}
	}
	*output = append(*output, bytes...)
	return nil
}

func stripBOM(line []byte) []byte {
	if len(line) >= 3 && line[0] == 0xef && line[1] == 0xbb && line[2] == 0xbf {
		return line[3:]
	}
	return line
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func parseTextLine(line []byte, options TextInputOptions) (parsedLine, error) {
	rest := trimLeading(line)
	if len(rest) == 0 || rest[0] == '#' || rest[0] == ';' {
		return parsedLine{kind: parsedEmpty}, nil
	}
	if options.Family == AddressFamilyInputIPv4 {
		if result, ok, err := parseIPv4ModeLine(rest, options); ok {
			return result, err
		}
		colons := 0
		for _, b := range rest {
			if b == ':' {
				colons++
			}
		}
		if colons >= 2 {
			if result, ok, err := parseV4ModeMapped(rest, options); ok {
				return result, err
			}
			return parsedLine{kind: parsedDroppedIPv6}, nil
		}
	} else {
		if result, ok, err := parseIPv6ModeLine(rest, options); ok {
			return result, err
		}
	}
	if hostnameIsComplete(rest) {
		return parsedLine{kind: parsedHostname, hostname: rest}, nil
	}
	return parsedLine{}, fmt.Errorf("invalid input line: %s", string(line))
}

func parseIPv4ModeLine(line []byte, options TextInputOptions) (parsedLine, bool, error) {
	first, afterFirst := scanWhile(line, isIPv4TokenByte)
	if len(first) == 0 {
		return parsedLine{}, false, nil
	}
	if completeAfterToken(afterFirst) {
		value, err := parseV4Endpoint(first, options)
		return parsedLine{kind: parsedRangeLine, value: value}, true, err
	}
	_, nextWithoutSpaces := scanWhile(afterFirst, func(b byte) bool { return b == ' ' || b == '\t' })
	if len(nextWithoutSpaces) == 0 || nextWithoutSpaces[0] != '-' {
		if containsByte(first, '/') || completeIPv4Candidate(first) {
			return parsedLine{}, true, fmt.Errorf("line looks like an IPv4 address but is invalid: %s", string(line))
		}
		return parsedLine{}, false, nil
	}
	_, afterDashWithoutDash := scanWhile(afterFirst, func(b byte) bool { return b == ' ' || b == '\t' })
	if len(afterDashWithoutDash) == 0 || afterDashWithoutDash[0] != '-' {
		return parsedLine{}, true, fmt.Errorf("invalid IPv4 range: %s", string(line))
	}
	_, afterDash := scanWhile(afterDashWithoutDash[1:], func(b byte) bool { return b == ' ' || b == '\t' })
	second, afterSecond := scanWhile(afterDash, isIPv4TokenByte)
	if len(second) == 0 || !completeAfterToken(afterSecond) {
		return parsedLine{}, true, fmt.Errorf("invalid IPv4 range: %s", string(line))
	}
	left, err := parseV4Endpoint(first, options)
	if err != nil {
		return parsedLine{}, true, err
	}
	right, err := parseV4Endpoint(second, options)
	if err != nil {
		return parsedLine{}, true, err
	}
	return parsedLine{kind: parsedRangeLine, value: joinedRange(left, right)}, true, nil
}

func parseIPv6ModeLine(line []byte, options TextInputOptions) (parsedLine, bool, error) {
	first, afterFirst := scanWhile(line, isIPv6TokenByte)
	if len(first) == 0 {
		return parsedLine{}, false, nil
	}
	if completeAfterToken(afterFirst) {
		if !containsByte(first, ':') && !classifyV4Token(first) {
			return parsedLine{}, false, nil
		}
		value, err := parseV6Endpoint(first, options)
		return parsedLine{kind: parsedRangeLine, value: value}, true, err
	}
	_, nextWithoutSpaces := scanWhile(afterFirst, func(b byte) bool { return b == ' ' || b == '\t' })
	if len(nextWithoutSpaces) == 0 || nextWithoutSpaces[0] != '-' {
		if containsByte(first, ':') || classifyV4Token(first) {
			return parsedLine{}, true, fmt.Errorf("line looks like an address but is invalid: %s", string(line))
		}
		return parsedLine{}, false, nil
	}
	_, afterDashWithoutDash := scanWhile(afterFirst, func(b byte) bool { return b == ' ' || b == '\t' })
	if len(afterDashWithoutDash) == 0 || afterDashWithoutDash[0] != '-' {
		return parsedLine{}, true, fmt.Errorf("invalid IPv6 range: %s", string(line))
	}
	_, afterDash := scanWhile(afterDashWithoutDash[1:], func(b byte) bool { return b == ' ' || b == '\t' })
	second, afterSecond := scanWhile(afterDash, isIPv6TokenByte)
	if len(second) == 0 || !completeAfterToken(afterSecond) {
		return parsedLine{}, true, fmt.Errorf("invalid IPv6 range: %s", string(line))
	}
	firstV6, firstIsV4 := tokenAddressFamily(first)
	secondV6, secondIsV4 := tokenAddressFamily(second)
	if firstIsV4 && secondIsV4 && firstV6 != secondV6 {
		return parsedLine{}, true, fmt.Errorf("mixed-family range: %s", string(line))
	}
	left, err := parseV6Endpoint(first, options)
	if err != nil {
		return parsedLine{}, true, err
	}
	right, err := parseV6Endpoint(second, options)
	if err != nil {
		return parsedLine{}, true, err
	}
	return parsedLine{kind: parsedRangeLine, value: joinedRange(left, right)}, true, nil
}

// parseV4ModeMapped handles the released "::ffff:a.b.c.d" form in IPv4
// mode: the mapped suffix parses as its IPv4 endpoint.
func parseV4ModeMapped(line []byte, options TextInputOptions) (parsedLine, bool, error) {
	rest := trimLeading(line)
	if len(rest) < 8 || !equalFoldPrefix(rest, "::ffff:") {
		return parsedLine{}, false, nil
	}
	token, after := scanWhile(rest[7:], isIPv4TokenByte)
	if len(token) == 0 || !completeAfterToken(after) {
		return parsedLine{}, false, nil
	}
	value, err := parseV4Endpoint(token, options)
	if err != nil {
		return parsedLine{}, true, err
	}
	return parsedLine{kind: parsedRangeLine, value: value}, true, nil
}

func equalFoldPrefix(bytes []byte, prefix string) bool {
	if len(bytes) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		b := bytes[i]
		c := prefix[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if b != c {
			return false
		}
	}
	return true
}

func joinedRange(left, right parsedRange) parsedRange {
	from := left.from()
	to := right.to()
	if compareU128(right.fromHi, right.fromLo, from.hi, from.lo) < 0 {
		from = right.from()
	}
	if compareU128(left.toHi, left.toLo, to.hi, to.lo) > 0 {
		to = left.to()
	}
	return parsedRange{fromHi: from.hi, fromLo: from.lo, toHi: to.hi, toLo: to.lo, ipv4: left.ipv4 && right.ipv4}
}

func parseV4Endpoint(token []byte, options TextInputOptions) (parsedRange, error) {
	if len(token) > 255 {
		return parsedRange{}, fmt.Errorf("IPv4 input token exceeds 255 bytes")
	}
	var addressToken []byte
	prefix := options.DefaultPrefix
	if prefix > 32 {
		prefix = 32
	}
	if position := indexByte(token, '/'); position >= 0 {
		parsedPrefix, err := parseV4Prefix(token[position+1:])
		if err != nil {
			return parsedRange{}, err
		}
		addressToken = token[:position]
		prefix = parsedPrefix
	} else {
		addressToken = token
	}
	address, err := parseInetAton(addressToken)
	if err != nil {
		return parsedRange{}, err
	}
	from := address
	if options.FixNetwork {
		from = networkV4(address, prefix)
	}
	to := broadcastV4(from, prefix)
	return parsedRange{
		fromHi: 0, fromLo: uint64(from),
		toHi: 0, toLo: uint64(to),
		ipv4: true,
	}, nil
}

func parseV4Prefix(token []byte) (uint32, error) {
	text := string(token)
	prefix, err := parseDecimalUint32(text)
	if err == nil {
		if prefix <= 32 {
			return prefix, nil
		}
		return 0, fmt.Errorf("IPv4 prefix is out of range: %s", text)
	}
	mask, err := parseInetAton(token)
	if err != nil {
		return 0, fmt.Errorf("invalid IPv4 netmask: %s", text)
	}
	if mask == ^uint32(0) {
		return 0, fmt.Errorf("invalid IPv4 netmask: %s", text)
	}
	inverted := ^mask
	if inverted == 0 || (inverted&(inverted+1)) != 0 {
		return 0, fmt.Errorf("invalid IPv4 netmask: %s", text)
	}
	return 32 - uint32(bits.OnesCount32(inverted)), nil
}

// parseInetAton implements the released inet_aton forms: one to four
// dot-separated parts with octal/hex/leading-zero acceptance and the
// classic suffix-width rules (Rust parse_inet_aton).
func parseInetAton(token []byte) (uint32, error) {
	text := string(token)
	var parts []uint64
	var current strings.Builder
	flush := func() error {
		if current.Len() == 0 {
			return fmt.Errorf("invalid IPv4 address: %s", text)
		}
		value, err := parseIPv4Number(current.String(), text)
		if err != nil {
			return err
		}
		parts = append(parts, value)
		current.Reset()
		return nil
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '.' {
			if err := flush(); err != nil {
				return 0, err
			}
		} else {
			current.WriteByte(text[i])
		}
	}
	if current.Len() == 0 || len(parts) >= 4 {
		return 0, fmt.Errorf("invalid IPv4 address: %s", text)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	var values [4]uint64
	copy(values[:], parts)
	switch len(parts) {
	case 1:
		if values[0] <= uint64(^uint32(0)) {
			return uint32(values[0]), nil
		}
	case 2:
		if values[0] <= 0xff && values[1] <= 0x00ff_ffff {
			return uint32(values[0])<<24 | uint32(values[1]), nil
		}
	case 3:
		if values[0] <= 0xff && values[1] <= 0xff && values[2] <= 0xffff {
			return uint32(values[0])<<24 | uint32(values[1])<<16 | uint32(values[2]), nil
		}
	default:
		if values[0] <= 0xff && values[1] <= 0xff && values[2] <= 0xff && values[3] <= 0xff {
			return uint32(values[0])<<24 | uint32(values[1])<<16 | uint32(values[2])<<8 | uint32(values[3]), nil
		}
	}
	return 0, fmt.Errorf("invalid IPv4 address: %s", text)
}

// parseIPv4Number parses one numeric part with the released radix
// rules: 0x hex, leading-zero octal, plain decimal (Rust
// parse_ipv4_number). An empty digit string is zero.
func parseIPv4Number(token, address string) (uint64, error) {
	bytes := []byte(token)
	if len(bytes) == 0 {
		return 0, fmt.Errorf("invalid IPv4 address: %s", address)
	}
	var digits []byte
	radix := 10
	if len(bytes) >= 2 && bytes[0] == '0' && (bytes[1] == 'x' || bytes[1] == 'X') {
		digits = bytes[2:]
		radix = 16
	} else if len(bytes) >= 2 && bytes[0] == '0' {
		digits = bytes[1:]
		radix = 8
	} else {
		digits = bytes
	}
	if len(digits) == 0 {
		return 0, nil
	}
	var value uint64
	for _, b := range digits {
		digit := digitValue(b, radix)
		if digit < 0 {
			return 0, fmt.Errorf("invalid IPv4 address: %s", address)
		}
		next := value*uint64(radix) + uint64(digit)
		if next < value || next > uint64(^uint32(0)) {
			return 0, fmt.Errorf("invalid IPv4 address: %s", address)
		}
		value = next
	}
	return value, nil
}

func digitValue(b byte, radix int) int {
	switch {
	case b >= '0' && b <= '9':
		value := int(b - '0')
		if value < radix {
			return value
		}
	case b >= 'a' && b <= 'f':
		value := int(b-'a') + 10
		if value < radix {
			return value
		}
	case b >= 'A' && b <= 'F':
		value := int(b-'A') + 10
		if value < radix {
			return value
		}
	}
	return -1
}

func networkV4(address uint32, prefix uint32) uint32 {
	if prefix == 0 {
		return 0
	}
	return address & (^uint32(0) << (32 - prefix))
}

func broadcastV4(address uint32, prefix uint32) uint32 {
	if prefix == 0 {
		return ^uint32(0)
	}
	if prefix == 32 {
		return address
	}
	return address | (^uint32(0) >> prefix)
}

func parseV6Endpoint(token []byte, options TextInputOptions) (parsedRange, error) {
	if len(token) > 256 {
		return parsedRange{}, fmt.Errorf("IPv6 input token exceeds 256 bytes")
	}
	if containsByte(token, ':') {
		var addressToken []byte
		prefix := options.DefaultPrefix
		if position := indexByte(token, '/'); position >= 0 {
			prefixText := string(token[position+1:])
			parsed, err := parseDecimalUint32(prefixText)
			if err != nil || parsed > 128 {
				return parsedRange{}, fmt.Errorf("invalid IPv6 prefix: %s", prefixText)
			}
			prefix = parsed
			addressToken = token[:position]
		} else {
			addressToken = token
		}
		parsed, err := netip.ParseAddr(string(addressToken))
		if err != nil || parsed.Zone() != "" || !parsed.Is6() {
			return parsedRange{}, fmt.Errorf("invalid IPv6 address: %s", string(addressToken))
		}
		bytes := parsed.As16()
		value := uint128{
			hi: uint64(bytes[0])<<56 | uint64(bytes[1])<<48 | uint64(bytes[2])<<40 | uint64(bytes[3])<<32 |
				uint64(bytes[4])<<24 | uint64(bytes[5])<<16 | uint64(bytes[6])<<8 | uint64(bytes[7]),
			lo: uint64(bytes[8])<<56 | uint64(bytes[9])<<48 | uint64(bytes[10])<<40 | uint64(bytes[11])<<32 |
				uint64(bytes[12])<<24 | uint64(bytes[13])<<16 | uint64(bytes[14])<<8 | uint64(bytes[15]),
		}
		from := value
		if options.FixNetwork {
			from = networkV6(value, prefix)
		}
		to := broadcastV6(from, prefix)
		return parsedRange{fromHi: from.hi, fromLo: from.lo, toHi: to.hi, toLo: to.lo, ipv4: false}, nil
	}
	endpoint, err := parseV4Endpoint(token, options)
	if err != nil {
		return parsedRange{}, err
	}
	if options.DefaultPrefix >= 32 {
		from := ipv4Mapped(uint32(endpoint.fromLo))
		to := ipv4Mapped(uint32(endpoint.toLo))
		return parsedRange{fromHi: from.hi, fromLo: from.lo, toHi: to.hi, toLo: to.lo, ipv4: false}, nil
	}
	return endpoint, nil
}

func ipv4Mapped(address uint32) uint128 {
	return uint128{hi: 0x0000_ffff, lo: uint64(address)}
}

// networkV6 clears the host bits of one IPv6 address.
func networkV6(address uint128, prefix uint32) uint128 {
	if prefix == 0 {
		return uint128{}
	}
	if prefix == 128 {
		return address
	}
	mask := hostMaskV6(prefix)
	return uint128{hi: address.hi & mask.hi, lo: address.lo & mask.lo}
}

// broadcastV6 sets the host bits of one IPv6 address.
func broadcastV6(address uint128, prefix uint32) uint128 {
	if prefix == 0 {
		return uint128{hi: ^uint64(0), lo: ^uint64(0)}
	}
	if prefix == 128 {
		return address
	}
	mask := hostMaskV6(prefix)
	return uint128{hi: address.hi | ^mask.hi, lo: address.lo | ^mask.lo}
}

// hostMaskV6 returns the network-bit mask of one IPv6 prefix (the
// high `prefix` bits set).
func hostMaskV6(prefix uint32) uint128 {
	if prefix >= 64 {
		// Host bits (128-prefix, at most 64) live entirely in the low
		// half.
		return uint128{hi: ^uint64(0), lo: ^uint64(0) << (128 - prefix)}
	}
	// Host bits span both halves; the low half is entirely host.
	return uint128{hi: ^uint64(0) << (64 - prefix), lo: 0}
}

func scanWhile(line []byte, accept func(byte) bool) ([]byte, []byte) {
	end := 0
	for end < len(line) && accept(line[end]) {
		end++
	}
	return line[:end], line[end:]
}

func trimLeading(line []byte) []byte {
	start := 0
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	return line[start:]
}

func completeAfterToken(rest []byte) bool {
	rest = trimLeading(rest)
	if len(rest) == 0 {
		return true
	}
	switch rest[0] {
	case '#', ';', '\r', 0:
		return true
	}
	return false
}

func isIPv4TokenByte(b byte) bool {
	return b >= '0' && b <= '9' || b == '.' || b == '/'
}

func isIPv6TokenByte(b byte) bool {
	return isASCIIHexDigit(b) || b == ':' || b == '.' || b == '/'
}

func isASCIIHexDigit(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func isHostnameByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b == '_' || b == '-' || b == '.'
}

// completeIPv4Candidate reports whether the token is four fully
// dotted decimal groups (Rust complete_ipv4_candidate).
func completeIPv4Candidate(token []byte) bool {
	dots := 0
	digits := 0
	for _, b := range token {
		if b >= '0' && b <= '9' {
			digits++
		} else if b == '.' && digits > 0 {
			dots++
			digits = 0
		} else {
			return false
		}
	}
	return dots == 3 && digits > 0
}

// classifyV4Token reports whether the token is IPv4-shaped (dots, a
// slash, or all digits) (Rust classify_v4_token).
func classifyV4Token(token []byte) bool {
	if len(token) == 0 {
		return false
	}
	if containsByte(token, '.') || containsByte(token, '/') {
		return true
	}
	for _, b := range token {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

// tokenAddressFamily returns (isIPv6, isAddressLike) (Rust
// token_address_family).
func tokenAddressFamily(token []byte) (bool, bool) {
	if containsByte(token, ':') {
		return true, true
	}
	return false, classifyV4Token(token)
}

func hostnameIsComplete(line []byte) bool {
	token, rest := scanWhile(line, isHostnameByte)
	if len(token) > 255 {
		return false
	}
	return len(token) > 0 && completeAfterToken(rest)
}

// dnsRange maps one resolved address to the input family: IPv6 mode
// keeps IPv4 answers as ::ffff:a.b.c.d (Rust dns_range).
func dnsRange(address net.IP, family AddressFamilyInput) (parsedRange, error) {
	if v4 := address.To4(); v4 != nil {
		value := uint64(v4[0])<<24 | uint64(v4[1])<<16 | uint64(v4[2])<<8 | uint64(v4[3])
		if family == AddressFamilyInputIPv4 {
			return singleRange(uint128{lo: value}), nil
		}
		return singleRange(ipv4Mapped(uint32(value))), nil
	}
	v6 := address.To16()
	if v6 == nil {
		return parsedRange{}, fmt.Errorf("DNS response contains an invalid address")
	}
	if family == AddressFamilyInputIPv4 {
		return parsedRange{}, fmt.Errorf("IPv4 DNS response contains an IPv6 address")
	}
	value := uint128{
		hi: uint64(v6[0])<<56 | uint64(v6[1])<<48 | uint64(v6[2])<<40 | uint64(v6[3])<<32 |
			uint64(v6[4])<<24 | uint64(v6[5])<<16 | uint64(v6[6])<<8 | uint64(v6[7]),
		lo: uint64(v6[8])<<56 | uint64(v6[9])<<48 | uint64(v6[10])<<40 | uint64(v6[11])<<32 |
			uint64(v6[12])<<24 | uint64(v6[13])<<16 | uint64(v6[14])<<8 | uint64(v6[15]),
	}
	return singleRange(value), nil
}

func singleRange(value uint128) parsedRange {
	return parsedRange{fromHi: value.hi, fromLo: value.lo, toHi: value.hi, toLo: value.lo, ipv4: value.hi == 0 && value.lo <= uint64(^uint32(0))}
}

// resolveHostnames resolves a batch of hostnames with the released
// round-robin worker distribution; IPv4 mode keeps only A records and
// refuses a result without any (Rust resolve_hostnames).
func resolveHostnames(names []string, family AddressFamilyInput, threads int, silent bool) ([]net.IP, error) {
	workers := len(names)
	if threads < workers {
		workers = threads
	}
	if workers < 1 {
		workers = 1
	}
	results := make([][]net.IP, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			var output []net.IP
			for index := worker; index < len(names); index += workers {
				resolved, err := resolveOne(names[index], silent)
				if err != nil {
					errs[worker] = err
					return
				}
				output = append(output, resolved...)
			}
			results[worker] = output
		}(worker)
	}
	wait.Wait()
	var addresses []net.IP
	for worker := 0; worker < workers; worker++ {
		if errs[worker] != nil {
			return nil, errs[worker]
		}
		addresses = append(addresses, results[worker]...)
	}
	if family == AddressFamilyInputIPv4 {
		var v4 []net.IP
		for _, address := range addresses {
			if address.To4() != nil {
				v4 = append(v4, address)
			}
		}
		if len(v4) == 0 {
			return nil, fmt.Errorf("DNS response contains no A records")
		}
		return v4, nil
	}
	return addresses, nil
}

// resolveOne resolves one hostname with the released retry policy:
// temporary failures retry up to twenty times with a one-second pause;
// warnings go to stderr unless silent (Rust resolve_one).
func resolveOne(name string, silent bool) ([]net.IP, error) {
	for attempt := 1; attempt <= 20; attempt++ {
		addresses, err := net.LookupIP(name)
		if err == nil {
			return addresses, nil
		}
		message := err.Error()
		temporary := false
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			temporary = dnsErr.IsTemporary || dnsErr.IsTimeout
		}
		if temporary && attempt < 20 {
			if !silent {
				fmt.Fprintf(os.Stderr, "iprange: DNS: '%s' will be retried: %v\n", name, err)
			}
			time.Sleep(time.Second)
			continue
		}
		if !silent {
			fmt.Fprintf(os.Stderr, "iprange: DNS: '%s' failed permanently: %v\n", name, err)
		}
		return nil, fmt.Errorf("DNS resolution failed for '%s': %s", name, message)
	}
	return nil, fmt.Errorf("DNS resolution failed for '%s'", name)
}

func binaryLine(reader *bufio.Reader, maxLineBytes int) ([]byte, *InputError) {
	var line []byte
	hadNewline, ok, err := readLimitedLine(reader, maxLineBytes, &line)
	if err != nil {
		return nil, err
	}
	if !ok || !hadNewline {
		return nil, nil
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil
}

// binaryNumber parses one released binary header numeric field (the
// prefix must match the field name); a missing or non-matching line is
// nil (Rust binary_number).
func binaryNumber(reader *bufio.Reader, prefix []byte, maxLineBytes int) (*uint128, *InputError) {
	line, err := binaryLine(reader, maxLineBytes)
	if err != nil || line == nil {
		return nil, err
	}
	if !hasPrefixBytes(line, prefix) {
		return nil, nil
	}
	value, parseErr := parseDecimal128(string(line[len(prefix):]))
	if parseErr != nil {
		return nil, &InputError{kind: inputErrorFormat, message: "invalid binary numeric field"}
	}
	return &value, nil
}

func hasPrefixBytes(line, prefix []byte) bool {
	if len(line) < len(prefix) {
		return false
	}
	for i := range prefix {
		if line[i] != prefix[i] {
			return false
		}
	}
	return true
}

// binaryRecord decodes one released payload record in native byte
// order (Rust binary_record).
func binaryRecord(ipv6 bool, bytes []byte) (parsedRange, bool) {
	if ipv6 {
		if len(bytes) < 32 {
			return parsedRange{}, false
		}
		return parsedRange{
			fromHi: binary.NativeEndian.Uint64(bytes[0:8]),
			fromLo: binary.NativeEndian.Uint64(bytes[8:16]),
			toHi:   binary.NativeEndian.Uint64(bytes[16:24]),
			toLo:   binary.NativeEndian.Uint64(bytes[24:32]),
			ipv4:   false,
		}, true
	}
	if len(bytes) < 8 {
		return parsedRange{}, false
	}
	return parsedRange{
		fromLo: uint64(binary.NativeEndian.Uint32(bytes[0:4])),
		toLo:   uint64(binary.NativeEndian.Uint32(bytes[4:8])),
		ipv4:   true,
	}, true
}

// uint128 is a compact unsigned 128-bit value used by the binary
// header arithmetic and the IPv6 range math.
type uint128 struct {
	hi uint64
	lo uint64
}

func parseDecimalUint32(text string) (uint32, error) {
	if text == "" {
		return 0, fmt.Errorf("empty number")
	}
	var value uint64
	for i := 0; i < len(text); i++ {
		b := text[i]
		if b < '0' || b > '9' {
			return 0, fmt.Errorf("not a number")
		}
		value = value*10 + uint64(b-'0')
		if value > 0xffffffff {
			return 0, fmt.Errorf("out of range")
		}
	}
	return uint32(value), nil
}

// parseDecimal128 parses an unsigned decimal string into a u128 (Rust
// u128 parse); values at or above 2^128 are an error.
func parseDecimal128(text string) (uint128, error) {
	var value uint128
	if text == "" {
		return value, fmt.Errorf("empty decimal")
	}
	for i := 0; i < len(text); i++ {
		b := text[i]
		if b < '0' || b > '9' {
			return value, fmt.Errorf("not a decimal")
		}
		multiplied, overflow := mulU128(value.hi, value.lo, 10)
		if overflow {
			return value, fmt.Errorf("decimal overflow")
		}
		added, overflow := addU128(multiplied.hi, multiplied.lo, 0, uint64(b-'0'))
		if overflow {
			return value, fmt.Errorf("decimal overflow")
		}
		value = added
	}
	return value, nil
}

func addU128(ahi, alo, bhi, blo uint64) (uint128, bool) {
	lo, carry := bits.Add64(alo, blo, 0)
	hi, carry := bits.Add64(ahi, bhi, carry)
	return uint128{hi: hi, lo: lo}, carry != 0
}

func subU128(ahi, alo, bhi, blo uint64) uint128 {
	lo, borrow := bits.Sub64(alo, blo, 0)
	hi, _ := bits.Sub64(ahi, bhi, borrow)
	return uint128{hi: hi, lo: lo}
}

// mulU128 multiplies one u128 by a u64 factor (the only released
// factors are record sizes 8 and 32; parseDecimal128 never creates a
// nonzero high limb before the multiply).
func mulU128(hi, lo, factor uint64) (uint128, bool) {
	if factor == 0 {
		return uint128{}, false
	}
	// bits.Mul64 returns the 128-bit product as (hi, lo); the low limb
	// of the result is lo*factor's low half, the high limb is the sum
	// of lo*factor's high half and hi*factor.
	loHi, loLo := bits.Mul64(lo, factor)
	hiHi, hiLo := bits.Mul64(hi, factor)
	resultHi, carry := bits.Add64(loHi, hiLo, 0)
	if hiHi != 0 {
		return uint128{}, true
	}
	return uint128{hi: resultHi, lo: loLo}, carry != 0
}

func compareU128(ahi, alo, bhi, blo uint64) int {
	if ahi < bhi {
		return -1
	}
	if ahi > bhi {
		return 1
	}
	if alo < blo {
		return -1
	}
	if alo > blo {
		return 1
	}
	return 0
}

func toUint64(value uint128) (uint64, bool) {
	if value.hi != 0 {
		return 0, true
	}
	return value.lo, false
}

func containsByte(bytes []byte, target byte) bool {
	for _, b := range bytes {
		if b == target {
			return true
		}
	}
	return false
}

func indexByte(bytes []byte, target byte) int {
	for i, b := range bytes {
		if b == target {
			return i
		}
	}
	return -1
}
