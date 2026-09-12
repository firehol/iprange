// `iprange.v1.export`: canonical file export of one pinned v4 view
// (Rust handlers/export.rs parity).
//
// Source iteration is a bounded stream of canonical constant-value
// segments over public SDK cursors. Flat set formats merge those
// segments into maximal coverage; row formats keep their values. The
// writer enforces the caller's row/byte budgets before each row, so
// prefix or per-address expansion refuses instead of exploding. The
// result carries the destination facts and the source database
// identity; a live source contributes its factual close result.

package handlers

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/pathname"
)

// RegisterExport installs the export handler family.
func RegisterExport() {
	rpc.Register("iprange.v1.export", ValidateExport, Export)
}

// exportViewKind is the decoded `view` selector discriminator.
type exportViewKind uint8

const (
	exportViewDirect exportViewKind = iota
	exportViewStructured
	exportViewFeed
	exportViewSelection
)

// exportView is the decoded view selector (Rust ExportView); named nil
// selects every feed, a non-nil slice preserves caller names.
type exportView struct {
	kind  exportViewKind
	feed  string
	named []string
}

// exportValue is one semantic row value carried through the row format
// encoders (Rust ExportValue): a direct u32, a structured wire object,
// or a catalog-ordered feed-name set.
type exportValue struct {
	direct     *uint32
	feeds      []string
	structured map[string]any
}

// equal compares two semantic values by content (Rust PartialEq).
func (v exportValue) equal(other exportValue) bool {
	if (v.direct == nil) != (other.direct == nil) {
		return false
	}
	if v.direct != nil && *v.direct != *other.direct {
		return false
	}
	if (v.structured == nil) != (other.structured == nil) {
		return false
	}
	if v.structured != nil && !reflect.DeepEqual(v.structured, other.structured) {
		return false
	}
	if (v.feeds == nil) != (other.feeds == nil) {
		return false
	}
	if v.feeds != nil && !equalStrings(v.feeds, other.feeds) {
		return false
	}
	return true
}

func equalStrings(left, right []string) bool {
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

// exportFactsWithIdentity is one completed export plus the retained
// source identity used in its wire result.
type exportFactsWithIdentity struct {
	facts    *fileio.ExportFacts
	identity any
}

// exportReader is the cursor surface shared by the immutable and live
// public readers; both SDK facades implement it, so one stream
// implementation serves both source modes.
type exportReader interface {
	Info() (iprangedb.DatabaseInfo, error)
	DirectCursorV4(direction iprangedb.RangeDirection) (*iprangedb.DirectCursorV4, error)
	DirectCursorV6(direction iprangedb.RangeDirection) (*iprangedb.DirectCursorV6, error)
	FeedCursor() (*iprangedb.FeedCursor, error)
	FeedRangeCursorV4(name string, direction iprangedb.RangeDirection) (*iprangedb.FeedRangeCursorV4, error)
	FeedRangeCursorV6(name string, direction iprangedb.RangeDirection) (*iprangedb.FeedRangeCursorV6, error)
	NetworkEnrichmentV1CursorV4(direction iprangedb.RangeDirection) (*iprangedb.NetworkEnrichmentV1CursorV4, error)
	NetworkEnrichmentV1CursorV6(direction iprangedb.RangeDirection) (*iprangedb.NetworkEnrichmentV1CursorV6, error)
	LookupFeed(name string) (iprangedb.FeedEntry, bool, error)
}

// ValidateExport enforces the strict export params schema
// (v4/cli/schema/methods.py iprange.v1.export).
func ValidateExport(params json.RawMessage) error {
	object, err := exactObjectOpt(params,
		[]string{"source", "view", "format", "destination", "publication_policy", "result_budget"},
		[]string{"min_prefix", "prefixes"})
	if err != nil {
		return err
	}
	if err := validateExportSource(object["source"]); err != nil {
		return err
	}
	if err := validateExportView(object["view"]); err != nil {
		return err
	}
	format, err := asString(object, "format")
	if err != nil {
		return fmt.Errorf("format must be a string")
	}
	switch format {
	case "netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary":
	default:
		return fmt.Errorf("format must be netset, ipset, ranges, csv, jsonl, or legacy_binary")
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return err
	}
	if err := validatePath(destination); err != nil {
		return err
	}
	policy, err := asString(object, "publication_policy")
	if err != nil {
		return fmt.Errorf("publication_policy is invalid")
	}
	if !validPublicationPolicyName(policy) {
		return fmt.Errorf("publication_policy is invalid")
	}
	if err := validateResultBudget(object["result_budget"]); err != nil {
		return err
	}
	minimum, hasMinimum := object["min_prefix"]
	prefixes, hasPrefixes := object["prefixes"]
	if hasMinimum && hasPrefixes {
		return fmt.Errorf("min_prefix and prefixes are mutually exclusive")
	}
	if format != "netset" && (hasMinimum || hasPrefixes) {
		return fmt.Errorf("min_prefix and prefixes apply only to netset format")
	}
	if hasMinimum {
		value, err := decodeUint64(minimum)
		if err != nil {
			return fmt.Errorf("min_prefix must be u32")
		}
		if value > 128 {
			return fmt.Errorf("min_prefix must not exceed 128")
		}
	}
	if hasPrefixes {
		if isRawNull(prefixes) {
			return fmt.Errorf("prefixes must be an array; null is not valid")
		}
		var list []json.RawMessage
		if err := json.Unmarshal(prefixes, &list); err != nil {
			return fmt.Errorf("prefixes must be an array")
		}
		if len(list) == 0 {
			return fmt.Errorf("prefixes must contain at least one value")
		}
		seen := make(map[uint64]bool)
		for _, item := range list {
			value, err := decodeUint64(item)
			if err != nil {
				return fmt.Errorf("each prefix must be u32")
			}
			if value > 128 {
				return fmt.Errorf("each prefix must not exceed 128")
			}
			if seen[value] {
				return fmt.Errorf("prefixes must be unique")
			}
			seen[value] = true
		}
	}
	return nil
}

func validateExportSource(raw json.RawMessage) error {
	source, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("source must be an object")
	}
	if len(source) != 2 || !sourceContains(source, "path") || !sourceContains(source, "mode") {
		return fmt.Errorf("source requires exactly path and mode")
	}
	path, err := asString(source, "path")
	if err != nil {
		return fmt.Errorf("source.path must be a string")
	}
	if err := validatePath(path); err != nil {
		return err
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return fmt.Errorf("source.mode must be immutable or live")
	}
	if mode != "immutable" && mode != "live" {
		return fmt.Errorf("source.mode must be immutable or live")
	}
	return nil
}

func sourceContains(source rawObject, name string) bool {
	_, ok := source[name]
	return ok
}

func validateExportView(raw json.RawMessage) error {
	view, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("view must be an object")
	}
	kind, err := asString(view, "kind")
	if err != nil {
		return fmt.Errorf("view must be an object")
	}
	switch kind {
	case "direct", "structured":
		if len(view) != 1 {
			return fmt.Errorf("direct and structured views accept only kind")
		}
		return nil
	case "feed":
		if len(view) != 2 || !sourceContains(view, "feed") {
			return fmt.Errorf("feed view requires exactly kind and feed")
		}
		name, err := asString(view, "feed")
		if err != nil {
			return fmt.Errorf("view.feed must be a string")
		}
		if !feedNameGrammarValid(name) {
			return fmt.Errorf("feed name is invalid")
		}
		return nil
	case "selection":
		if len(view) != 2 || !sourceContains(view, "selection") {
			return fmt.Errorf("selection view requires exactly kind and selection")
		}
		selection, err := memberObject(view, "selection")
		if err != nil {
			return fmt.Errorf("view.selection must be an object")
		}
		mode, err := asString(selection, "mode")
		if err != nil {
			return fmt.Errorf("view.selection must be an object")
		}
		switch mode {
		case "all":
			if len(selection) != 1 {
				return fmt.Errorf("all selection accepts only mode")
			}
			return nil
		case "named":
			if len(selection) != 2 || !sourceContains(selection, "feeds") {
				return fmt.Errorf("named selection requires exactly mode and feeds")
			}
			feeds, err := asStringArray(selection, "feeds")
			if err != nil {
				return fmt.Errorf("selection.feeds must be an array")
			}
			if len(feeds) == 0 {
				return fmt.Errorf("selection.feeds must contain at least one name")
			}
			seen := make(map[string]bool)
			for _, feed := range feeds {
				if !feedNameGrammarValid(feed) {
					return fmt.Errorf("feed name is invalid")
				}
				if seen[feed] {
					return fmt.Errorf("selection.feeds must be unique")
				}
				seen[feed] = true
			}
			return nil
		}
		return fmt.Errorf("selection.mode must be all or named")
	}
	return fmt.Errorf("view.kind must be direct, structured, feed, or selection")
}

func validateResultBudget(raw json.RawMessage) error {
	budget, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("result_budget must be an object")
	}
	if len(budget) != 3 {
		return fmt.Errorf("result_budget requires exactly max_rows, max_output_bytes, and max_open_files")
	}
	for _, field := range []string{"max_rows", "max_output_bytes"} {
		text, err := asString(budget, field)
		if err != nil {
			return fmt.Errorf("result_budget.%s: value must be a positive canonical unsigned decimal string", field)
		}
		if _, err := positiveDecimalU64(text); err != nil {
			return fmt.Errorf("result_budget.%s: %v", field, err)
		}
	}
	files, err := asUint64(budget, "max_open_files")
	if err != nil {
		return fmt.Errorf("result_budget.max_open_files: value must be a positive u32 integer")
	}
	if _, err := positiveU32Value(files); err != nil {
		return fmt.Errorf("result_budget.max_open_files: value must be a positive u32 integer")
	}
	return nil
}

// Export implements the canonical file export.
func Export(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	sourcePath, sourceMode, herr := exportSourceValue(object["source"])
	if herr != nil {
		return nil, herr
	}
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return nil, rpc.NewHandlerError("invalid_path", "not_started",
				fmt.Sprintf("database source does not exist: %s", sourcePath))
		}
		return nil, rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("cannot inspect database source %s: %v", sourcePath, err))
	}
	view, herr := decodeExportView(object["view"])
	if herr != nil {
		return nil, herr
	}
	format, err := asString(object, "format")
	if err != nil {
		return nil, rpc.InvalidParamsError("format must be a string")
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return nil, rpc.InvalidParamsError("destination must be a string")
	}
	policy, herr := decodePublicationPolicy(object["publication_policy"])
	if herr != nil {
		return nil, herr
	}
	budget, herr := decodeExportBudget(object["result_budget"])
	if herr != nil {
		return nil, herr
	}
	// The v1 contract never modifies its input files: publishing the
	// export over the source pathname would atomically replace the
	// database with the output text and report success. Refuse it
	// before the source opens or any output temporary exists, with
	// the canonical invalid_argument/not_started shape (Rust
	// export.rs refuse_output_over_source parity).
	sourceID := captureFileIdentity(sourcePath)
	sidecarID := sidecarIdentity(sourcePath)
	if herr := refuseOutputOverSource(destination, sourcePath, sourceID, sidecarID); herr != nil {
		return nil, herr
	}
	// The complete inline result carries the destination string and the
	// source identity; refuse an unrepresentable request before the
	// source reader is opened or any output file is created, so a
	// published export is never relabeled as a read-only failure by the
	// post-hoc response bound (iprange-jsonrpc-v1.md).
	if herr := preflightExport(st, format, destination, sourceMode); herr != nil {
		return nil, herr
	}
	var immutable *iprangedb.ImmutableReader
	var live *iprangedb.LiveReader
	if sourceMode == "live" {
		live, err = iprangedb.OpenLiveReader(sourcePath, st.Token())
	} else {
		immutable, err = iprangedb.OpenImmutable(sourcePath)
	}
	if err != nil {
		return nil, readError(err)
	}
	var reader exportReader
	if live != nil {
		reader = live
	} else {
		reader = immutable
	}
	completed, exportErr := exportWithReader(st, object, sourcePath, sourceMode, view, format,
		destination, policy, budget, reader)
	// Close the source even when the export failed, then report the
	// export failure; a completed export below must also survive a
	// close failure with both facts preserved.
	var closeFacts map[string]any
	var closeErr *rpc.HandlerError
	if live != nil {
		closeFacts, closeErr = closeExportSource(live)
	} else if immutable != nil {
		// Immutable sources carry no close fact but their mapping must
		// still be released (Rust export.rs source close parity).
		if cerr := immutable.Close(); cerr != nil {
			closeErr = readError(cerr)
		}
	}
	if exportErr != nil {
		// A product error preserves the close result of the reader it
		// opened whether the close succeeded or failed: keep the
		// export error primary and merge the factual source_close into
		// its details (double-fault pattern, Rust export.rs).
		if closeFacts != nil {
			details := map[string]any{}
			if exportErr.Details != nil {
				if existing, ok := exportErr.Details.(map[string]any); ok {
					details = existing
				}
			}
			details["source_close"] = closeFacts
			exportErr.Details = details
		}
		return nil, exportErr
	}
	result := map[string]any{
		"method":    "iprange.v1.export",
		"path":      completed.facts.Path,
		"format":    format,
		"sha256":    completed.facts.SHA256,
		"rows":      fmt.Sprintf("%d", completed.facts.Rows),
		"addresses": completed.facts.Addresses.String(),
		"bytes":     fmt.Sprintf("%d", completed.facts.Bytes),
		"identity":  completed.identity,
	}
	if closeErr != nil {
		return nil, PreserveCompletedReport(closeErr, result)
	}
	if closeFacts != nil {
		result["source_close"] = closeFacts
	}
	return boundedResult(result)
}

// exportSourceValue extracts the source path/mode after the validator
// checked its shape (Rust export.rs source_value).
func exportSourceValue(raw json.RawMessage) (string, string, *rpc.HandlerError) {
	source, err := decodeObject(raw)
	if err != nil {
		return "", "", rpc.InvalidParamsError("source must be an object")
	}
	path, err := asString(source, "path")
	if err != nil {
		return "", "", rpc.InvalidParamsError("source.path must be a string")
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return "", "", rpc.InvalidParamsError("source.mode must be immutable or live")
	}
	return path, mode, nil
}

// decodeExportView decodes the view selector after validation (Rust
// export.rs decode_view).
func decodeExportView(raw json.RawMessage) (*exportView, *rpc.HandlerError) {
	view, err := decodeObject(raw)
	if err != nil {
		return nil, rpc.InvalidParamsError("view must be an object")
	}
	kind, err := asString(view, "kind")
	if err != nil {
		return nil, rpc.InvalidParamsError("view.kind must be direct, structured, feed, or selection")
	}
	switch kind {
	case "direct":
		return &exportView{kind: exportViewDirect}, nil
	case "structured":
		return &exportView{kind: exportViewStructured}, nil
	case "feed":
		name, err := asString(view, "feed")
		if err != nil {
			return nil, rpc.InvalidParamsError("view.feed must be a string")
		}
		return &exportView{kind: exportViewFeed, feed: name}, nil
	case "selection":
		selection, err := memberObject(view, "selection")
		if err != nil {
			return nil, rpc.InvalidParamsError("view.selection must be an object")
		}
		mode, err := asString(selection, "mode")
		if err != nil {
			return nil, rpc.InvalidParamsError("selection.mode must be all or named")
		}
		if mode == "all" {
			return &exportView{kind: exportViewSelection, named: nil}, nil
		}
		feeds, err := asStringArray(selection, "feeds")
		if err != nil {
			return nil, rpc.InvalidParamsError("selection.feeds must be an array")
		}
		return &exportView{kind: exportViewSelection, named: feeds}, nil
	}
	return nil, rpc.InvalidParamsError("view.kind must be direct, structured, feed, or selection")
}

// decodeExportBudget decodes the validated result budget (Rust
// export.rs decode_budget).
func decodeExportBudget(raw json.RawMessage) (*fileio.ExportBudget, *rpc.HandlerError) {
	budget, err := decodeObject(raw)
	if err != nil {
		return nil, rpc.InvalidParamsError("result_budget must be an object")
	}
	decode := func(field string) (uint64, *rpc.HandlerError) {
		text, err := asDecimalString(budget, field)
		if err != nil {
			return 0, rpc.InvalidParamsError(fmt.Sprintf("result_budget.%s is invalid", field))
		}
		value, err := canonicalUint64(text)
		if err != nil {
			return 0, rpc.InvalidParamsError(fmt.Sprintf("result_budget.%s is invalid", field))
		}
		return value, nil
	}
	maxRows, herr := decode("max_rows")
	if herr != nil {
		return nil, herr
	}
	maxBytes, herr := decode("max_output_bytes")
	if herr != nil {
		return nil, herr
	}
	files, err := asUint64(budget, "max_open_files")
	if err != nil {
		return nil, rpc.InvalidParamsError("max_open_files must be u32")
	}
	if files > 0xffffffff {
		return nil, rpc.InvalidParamsError("max_open_files must be u32")
	}
	return &fileio.ExportBudget{
		MaxRows:        maxRows,
		MaxOutputBytes: maxBytes,
		MaxOpenFiles:   uint32(files),
	}, nil
}

// exportWithReader runs the selected view through the format encoder
// and returns the published facts with the source identity (Rust
// export.rs export_with_reader).
func exportWithReader(st *rpc.SessionState, object rawObject, sourcePath, sourceMode string, view *exportView,
	format, destination string, policy iprangedb.PublicationPolicy, budget *fileio.ExportBudget,
	reader exportReader) (*exportFactsWithIdentity, *rpc.HandlerError) {
	info, herr := sdk(reader.Info())
	if herr != nil {
		return nil, herr
	}
	hostPrefix := uint32(32)
	if info.Family == iprangedb.AddressFamilyIPv6 {
		hostPrefix = 128
	}
	filter, herr := decodePrefixes(object, format, hostPrefix)
	if herr != nil {
		return nil, herr
	}
	if format == "legacy_binary" &&
		view.kind != exportViewFeed && view.kind != exportViewSelection {
		return nil, rpc.NewHandlerError("invalid_argument", "not_started",
			"legacy_binary exports a flat address set; direct/structured values cannot be discarded")
	}
	if policy == iprangedb.PolicyFailIfExists {
		exists, herr := destinationExists(destination)
		if herr != nil {
			return nil, herr
		}
		if exists {
			return nil, rpc.NewHandlerError("name_exists", "not_started",
				fmt.Sprintf("export destination already exists: %s", destination))
		}
	}
	identity, herr := exportSourceIdentity(st, sourcePath, sourceMode, budget)
	if herr != nil {
		return nil, herr
	}
	token := st.Token()
	var facts *fileio.ExportFacts
	switch format {
	case "legacy_binary":
		facts, herr = writeLegacyBinary(destination, policy, budget, reader, view, hostPrefix, token)
	case "csv", "jsonl":
		facts, herr = writeRows(destination, policy, budget, reader, view, hostPrefix, token, format == "jsonl")
	case "ipset":
		var line []byte
		facts, herr = writeStreamed(destination, policy, budget, reader, view,
			func(writer *fileio.ExportWriter, from, to u128) *rpc.HandlerError {
				return emitIpset(from, to, hostPrefix, &line, func(text []byte) *rpc.HandlerError {
					if herr := checkCancelled(token); herr != nil {
						return herr
					}
					return writer.WriteLine(text, fileio.U64(1))
				})
			})
	case "netset":
		var line []byte
		facts, herr = writeStreamed(destination, policy, budget, reader, view,
			func(writer *fileio.ExportWriter, from, to u128) *rpc.HandlerError {
				return emitNetset(from, to, filter, &line, func(text []byte, span iprangedb.Cardinality129) *rpc.HandlerError {
					if herr := checkCancelled(token); herr != nil {
						return herr
					}
					return writer.WriteLine(text, fileio.C129(span))
				})
			})
	default: // ranges
		var line []byte
		facts, herr = writeStreamed(destination, policy, budget, reader, view,
			func(writer *fileio.ExportWriter, from, to u128) *rpc.HandlerError {
				if herr := checkCancelled(token); herr != nil {
					return herr
				}
				span, herr := spanOf(from, to, hostPrefix)
				if herr != nil {
					return herr
				}
				line = pushRangesLine(line[:0], from, to, hostPrefix)
				return writer.WriteLine(line, fileio.C129(span))
			})
	}
	if herr != nil {
		return nil, herr
	}
	return &exportFactsWithIdentity{facts: facts, identity: identity}, nil
}

// prefixFilter is the enabled netset prefix set of one address family
// (Rust PrefixFilter).
type prefixFilter struct {
	hostPrefix uint32
	enabled    []bool
}

func (f *prefixFilter) isEnabled(prefix uint32) bool {
	return f.enabled[prefix]
}

// decodePrefixes decodes the optional netset prefix controls (Rust
// export.rs decode_prefixes).
func decodePrefixes(object rawObject, format string, hostPrefix uint32) (*prefixFilter, *rpc.HandlerError) {
	if format != "netset" {
		return allPrefixes(hostPrefix), nil
	}
	if raw, ok := object["min_prefix"]; ok {
		value, err := decodeUint64(raw)
		if err != nil {
			return nil, rpc.InvalidParamsError("min_prefix must be u32")
		}
		if value > uint64(hostPrefix) {
			return nil, rpc.InvalidParamsError("min_prefix exceeds the family host prefix")
		}
		return minPrefixFilter(hostPrefix, uint32(value)), nil
	}
	if raw, ok := object["prefixes"]; ok {
		if isRawNull(raw) {
			return nil, rpc.InvalidParamsError("each prefix must be u32; null is not valid")
		}
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, rpc.InvalidParamsError("each prefix must be u32")
		}
		decoded := make([]uint32, 0, len(list))
		includeHost := false
		for _, item := range list {
			value, err := decodeUint64(item)
			if err != nil {
				return nil, rpc.InvalidParamsError("each prefix must be u32")
			}
			if value > uint64(hostPrefix) {
				return nil, rpc.InvalidParamsError("prefix exceeds the family host prefix")
			}
			if uint32(value) == hostPrefix {
				includeHost = true
			}
			decoded = append(decoded, uint32(value))
		}
		if !includeHost {
			return nil, rpc.InvalidParamsError("prefixes must include the family host prefix")
		}
		return listedPrefixes(hostPrefix, decoded), nil
	}
	return allPrefixes(hostPrefix), nil
}

func allPrefixes(hostPrefix uint32) *prefixFilter {
	enabled := make([]bool, hostPrefix+1)
	for i := range enabled {
		enabled[i] = true
	}
	return &prefixFilter{hostPrefix: hostPrefix, enabled: enabled}
}

// minPrefixFilter enables every prefix from minimum through the host.
func minPrefixFilter(hostPrefix, minimum uint32) *prefixFilter {
	filter := allPrefixes(hostPrefix)
	for prefix := uint32(0); prefix < minimum; prefix++ {
		filter.enabled[prefix] = false
	}
	return filter
}

// listedPrefixes enables exactly the listed prefixes.
func listedPrefixes(hostPrefix uint32, prefixes []uint32) *prefixFilter {
	filter := allPrefixes(hostPrefix)
	for i := range filter.enabled {
		filter.enabled[i] = false
	}
	for _, prefix := range prefixes {
		filter.enabled[prefix] = true
	}
	return filter
}

// worstJSONPath models the worst-case JSON serialization of the export
// destination string (Rust export.rs worst_json_path): it never shorter
// than serde_json's escaping, so the preflight bound is faithful.
func worstJSONPath(destination string) string {
	var worst strings.Builder
	worst.Grow(len(destination))
	for i := 0; i < len(destination); i++ {
		byteValue := destination[i]
		switch {
		case byteValue == '"' || byteValue == '\\':
			worst.WriteByte(byteValue)
		case byteValue <= 0x1f:
			worst.WriteByte(byteValue)
		case byteValue < 0x7f:
			worst.WriteByte(byteValue)
		default:
			worst.WriteString("xxxxxx")
		}
	}
	return worst.String()
}

// preflightExport refuses an export whose worst-case complete inline
// result cannot fit the 65,000-byte response object, before any file
// is opened or created (Rust export.rs preflight_export).
func preflightExport(st *rpc.SessionState, format, destination, sourceMode string) *rpc.HandlerError {
	worst := map[string]any{
		"method":    "iprange.v1.export",
		"path":      worstJSONPath(destination),
		"format":    format,
		"sha256":    strings.Repeat("f", 128),
		"rows":      WidestU64,
		"addresses": Widest129,
		"bytes":     WidestU64,
		"identity":  WidestIdentity(),
	}
	if sourceMode == "live" {
		worst["source_close"] = WidestCloseFact()
	}
	return preflightResponse(st, worst)
}

func destinationExists(destination string) (bool, *rpc.HandlerError) {
	if _, err := os.Stat(destination); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("cannot inspect export destination %s: %v", destination, err))
	}
}

// exportSourceIdentity returns the retained source-file identity of one
// export source through the public recovery inspection (Rust export.rs
// source_identity): the wire result requires the same {volume,file}
// pair as recovery inspection.
func exportSourceIdentity(st *rpc.SessionState, source, sourceMode string, budget *fileio.ExportBudget) (any, *rpc.HandlerError) {
	inspection := iprangedb.HeapOnly(8, budget.MaxOpenFiles)
	mode := iprangedb.RecoveryInspectionImmutable
	if sourceMode == "live" {
		mode = iprangedb.RecoveryInspectionLive
	}
	result, err := iprangedb.InspectRecoveryCandidates(source, mode, inspection, st.Token())
	if err != nil {
		return nil, readError(err)
	}
	return FileIdentityJSON(&result.SourceIdentity)
}

// closeExportSource closes one internally opened live export source and
// converts its factual close result (immutable sources have no close
// fact; Rust export.rs close_ephemeral_source).
func closeExportSource(live *iprangedb.LiveReader) (map[string]any, *rpc.HandlerError) {
	result, err := live.Close()
	if err != nil {
		return nil, readError(err)
	}
	return map[string]any{
		"outcome":              CloseOutcomeName(result.Outcome),
		"cleanup":              map[string]any{},
		"coordination_cleanup": CoordinationCleanupJSON(result.CoordinationCleanup),
	}, nil
}

// writeStreamed creates the writer, streams maximal coverage through
// one format callback, and publishes atomically (Rust export.rs
// write_streamed).
func writeStreamed(destination string, policy iprangedb.PublicationPolicy, budget *fileio.ExportBudget,
	reader exportReader, view *exportView,
	format func(writer *fileio.ExportWriter, from, to u128) *rpc.HandlerError) (*fileio.ExportFacts, *rpc.HandlerError) {
	writer, herr := fileio.NewExportWriter(destination, policy, *budget)
	if herr != nil {
		return nil, herr
	}
	work := streamCoverage(reader, view, func(from, to u128) *rpc.HandlerError {
		return format(writer, from, to)
	})
	if work != nil {
		writer.Abort()
		return nil, work
	}
	return writer.Finish()
}

// writeRows writes CSV or JSONL rows with their constant semantic
// values; adjacent equal-value segments become one canonical row
// without retaining the stream (Rust export.rs write_rows).
func writeRows(destination string, policy iprangedb.PublicationPolicy, budget *fileio.ExportBudget,
	reader exportReader, view *exportView, hostPrefix uint32, token *iprangedb.CancellationToken,
	jsonl bool) (*fileio.ExportFacts, *rpc.HandlerError) {
	writer, herr := fileio.NewExportWriter(destination, policy, *budget)
	if herr != nil {
		return nil, herr
	}
	work := func() *rpc.HandlerError {
		if !jsonl {
			if herr := writer.WriteChunk([]byte("from,to,value\n"), 0, fileio.U64(0)); herr != nil {
				return herr
			}
		}
		// One reusable line buffer and one reusable RFC-4180 quote
		// buffer serve every row (no per-row allocation), and one
		// pending slot merges adjacent equal-value segments.
		var line []byte
		var quote []byte
		var pendingFrom, pendingTo u128
		var pendingValue exportValue
		hasPending := false
		segment := func(from, to u128, value exportValue) *rpc.HandlerError {
			if herr := checkCancelled(token); herr != nil {
				return herr
			}
			if !hasPending {
				pendingFrom, pendingTo, pendingValue, hasPending = from, to, value, true
				return nil
			}
			next := pendingTo.addOne()
			if next == from && pendingValue.equal(value) {
				pendingTo = to
				return nil
			}
			if herr := writeRow(writer, hostPrefix, jsonl, pendingFrom, pendingTo, &pendingValue, &line, &quote); herr != nil {
				return herr
			}
			pendingFrom, pendingTo, pendingValue = from, to, value
			return nil
		}
		if err := streamSegments(reader, view, segment); err != nil {
			return err
		}
		if hasPending {
			return writeRow(writer, hostPrefix, jsonl, pendingFrom, pendingTo, &pendingValue, &line, &quote)
		}
		return nil
	}()
	if work != nil {
		writer.Abort()
		return nil, work
	}
	return writer.Finish()
}

// writeRow encodes one canonical row into the reusable line buffer
// (Rust export.rs write_row).
func writeRow(writer *fileio.ExportWriter, hostPrefix uint32, jsonl bool,
	from, to u128, value *exportValue, line, quote *[]byte) *rpc.HandlerError {
	span, herr := spanOf(from, to, hostPrefix)
	if herr != nil {
		return herr
	}
	buf := (*line)[:0]
	if jsonl {
		buf = append(buf, `{"from":`...)
		buf = exportPushAddress(buf, from, hostPrefix)
		buf = append(buf, `,"to":`...)
		buf = exportPushAddress(buf, to, hostPrefix)
		buf = append(buf, `,"value":`...)
		if value.direct != nil {
			buf = appendDecimal(buf, *value.direct)
		} else if value.structured != nil {
			buf, herr = writeJSONValue(buf, value.structured)
			if herr != nil {
				return herr
			}
		} else {
			buf = append(buf, '[')
			for i, feed := range value.feeds {
				if i > 0 {
					buf = append(buf, ',')
				}
				buf = pushJSONString(buf, feed)
			}
			buf = append(buf, ']')
		}
		buf = append(buf, '}')
	} else {
		buf = exportPushAddress(buf, from, hostPrefix)
		buf = append(buf, ',')
		buf = exportPushAddress(buf, to, hostPrefix)
		buf = append(buf, ',')
		if value.direct != nil {
			buf = appendDecimal(buf, *value.direct)
		} else if value.feeds != nil {
			// Feed names are [a-z0-9_.-] (SDK FeedName grammar), so the
			// semicolon-joined field never needs RFC-4180 quoting.
			for i, feed := range value.feeds {
				if i > 0 {
					buf = append(buf, ';')
				}
				buf = append(buf, feed...)
			}
		} else {
			// Structured values are canonical compact JSON; quote the
			// field when it contains RFC-4180 specials.
			start := len(buf)
			buf, herr = writeJSONValue(buf, value.structured)
			if herr != nil {
				return herr
			}
			encoded := buf[start:]
			needsQuotes := false
			for _, b := range encoded {
				if b == ',' || b == '"' || b == '\r' || b == '\n' {
					needsQuotes = true
					break
				}
			}
			if needsQuotes {
				*quote = (*quote)[:0]
				*quote = append(*quote, '"')
				for _, r := range string(encoded) {
					if r == '"' {
						*quote = append(*quote, '"')
					}
					*quote = append(*quote, string(r)...)
				}
				*quote = append(*quote, '"')
				buf = buf[:start]
				buf = append(buf, (*quote)...)
			}
		}
	}
	*line = buf
	return writer.WriteLine(*line, fileio.C129(span))
}

// writeLegacyBinary writes the released legacy binary format for a flat
// address set. The released header declares record, byte, line, and
// unique-IP counts before the payload, so the canonical ranges are
// streamed once to prove the counts and budgets, then streamed again to
// write (Rust export.rs write_legacy_binary).
func writeLegacyBinary(destination string, policy iprangedb.PublicationPolicy, budget *fileio.ExportBudget,
	reader exportReader, view *exportView, hostPrefix uint32, token *iprangedb.CancellationToken) (*fileio.ExportFacts, *rpc.HandlerError) {
	ipv6 := hostPrefix == 128
	recordSize := uint64(8)
	if ipv6 {
		recordSize = 32
	}
	minimumHeader := legacyBinaryMinHeaderBytes(ipv6)
	var records uint64
	addresses := iprangedb.CardinalityZero()
	streamErr := streamCoverage(reader, view, func(from, to u128) *rpc.HandlerError {
		if herr := checkCancelled(token); herr != nil {
			return herr
		}
		if records >= budget.MaxRows || records == ^uint64(0) {
			return rpc.NewHandlerError("output_limit", "not_started",
				fmt.Sprintf("export refused before exceeding budget: record %d exceeds max_rows (limit %d)", records, budget.MaxRows))
		}
		records++
		payload := saturatingAdd(saturatingAdd(recordSize*records, 4), minimumHeader)
		if payload > budget.MaxOutputBytes {
			return rpc.NewHandlerError("output_limit", "not_started",
				fmt.Sprintf("export refused before exceeding budget: byte %d exceeds max_output_bytes (limit %d)", payload, budget.MaxOutputBytes))
		}
		span, herr := spanOf(from, to, hostPrefix)
		if herr != nil {
			return herr
		}
		next, addErr := addresses.Add(span)
		if addErr != nil {
			return rpc.NewHandlerError("output_limit", "not_started",
				"export address cardinality exceeded the exact 129-bit counter")
		}
		addresses = next
		return nil
	})
	if streamErr != nil {
		return nil, streamErr
	}
	if records == 0 {
		// The released writer emits nothing for an empty set; the
		// destination is still atomically published as an empty file.
		writer, herr := fileio.NewExportWriter(destination, policy, *budget)
		if herr != nil {
			return nil, herr
		}
		return writer.Finish()
	}
	// The released header parses `unique ips` into a uint128 for IPv6
	// (src/ipset6_binary.c), so the 2^128 addresses of a full IPv6
	// space cannot be represented exactly; refuse before writing any
	// output instead of emitting a wrong count.
	uniqueHi, uniqueLo, herr := addressesToUint128(addresses)
	if herr != nil {
		return nil, herr
	}
	header := legacyBinaryHeader(ipv6, records, u128{hi: uniqueHi, lo: uniqueLo})
	exactBytes := uint64(len(header)) + 4 + recordSize*records
	if exactBytes > budget.MaxOutputBytes {
		return nil, rpc.NewHandlerError("output_limit", "not_started",
			fmt.Sprintf("export refused before exceeding budget: byte %d exceeds max_output_bytes (limit %d)", exactBytes, budget.MaxOutputBytes))
	}
	writer, herr := fileio.NewExportWriter(destination, policy, *budget)
	if herr != nil {
		return nil, herr
	}
	work := func() *rpc.HandlerError {
		if herr := writer.WriteChunk(header, 0, fileio.U64(0)); herr != nil {
			return herr
		}
		if herr := writer.WriteChunk(legacyEndiannessMarker(), 0, fileio.U64(0)); herr != nil {
			return herr
		}
		return streamCoverage(reader, view, func(from, to u128) *rpc.HandlerError {
			if herr := checkCancelled(token); herr != nil {
				return herr
			}
			span, herr := spanOf(from, to, hostPrefix)
			if herr != nil {
				return herr
			}
			var record []byte
			if ipv6 {
				record = legacyBinaryRecordV6(from, to)
			} else {
				record = legacyBinaryRecordV4(uint32(from.lo), uint32(to.lo))
			}
			return writer.WriteChunk(record, 1, fileio.C129(span))
		})
	}()
	if work != nil {
		writer.Abort()
		return nil, work
	}
	return writer.Finish()
}

// addressesToUint128 converts the exact cardinality to the u128 halves
// of the released header's `unique ips` field.
func addressesToUint128(addresses iprangedb.Cardinality129) (uint64, uint64, *rpc.HandlerError) {
	hi, lo, err := addresses.Uint128()
	if err != nil {
		return 0, 0, rpc.NewHandlerError("output_limit", "not_started",
			"legacy_binary header stores unique ips as uint128; the exported full IPv6 space (340282366920938463463374607431768211456 addresses) cannot be represented exactly")
	}
	return hi, lo, nil
}

func saturatingAdd(left, right uint64) uint64 {
	sum := left + right
	if sum < left {
		return ^uint64(0)
	}
	return sum
}

func legacyBinaryMinHeaderBytes(ipv6 bool) uint64 {
	if ipv6 {
		return 108
	}
	return 100
}

// legacyBinaryHeader renders the released binary header. `records` are
// the canonical optimized records that follow; `uniqueIps` is the exact
// address count (Rust export_writer.rs legacy_binary_header).
func legacyBinaryHeader(ipv6 bool, records uint64, uniqueIps u128) []byte {
	recordSize := uint64(8)
	if ipv6 {
		recordSize = 32
	}
	payloadBytes := recordSize*records + 4
	var header strings.Builder
	if ipv6 {
		header.WriteString("iprange binary format v2.0\n")
		header.WriteString("ipv6\n")
	} else {
		header.WriteString("iprange binary format v1.0\n")
	}
	header.WriteString("optimized\n")
	fmt.Fprintf(&header, "record size %d\n", recordSize)
	fmt.Fprintf(&header, "records %d\n", records)
	fmt.Fprintf(&header, "bytes %d\n", payloadBytes)
	// A v4 export has no source line accounting; the minimal factual
	// value that satisfies the released loader is the record count.
	fmt.Fprintf(&header, "lines %d\n", records)
	header.WriteString("unique ips ")
	header.WriteString(uniqueIps.decimal())
	header.WriteString("\n")
	return []byte(header.String())
}

// legacyEndiannessMarker is the released payload endianness marker in
// native byte order (Rust LEGACY_ENDIANNESS_MARKER).
func legacyEndiannessMarker() []byte {
	buffer := make([]byte, 4)
	binary.NativeEndian.PutUint32(buffer, 0x1a2b_3c4d)
	return buffer
}

// legacyBinaryRecordV4 renders one released network_addr_t record: two
// host-order u32 values in native byte order (src/ipset_binary.c).
func legacyBinaryRecordV4(from, to uint32) []byte {
	record := make([]byte, 8)
	binary.NativeEndian.PutUint32(record[0:4], from)
	binary.NativeEndian.PutUint32(record[4:8], to)
	return record
}

// legacyBinaryRecordV6 renders one released network_addr6_t record: two
// host-order u128 values in native layout (src/ipset6_binary.c).
func legacyBinaryRecordV6(from, to u128) []byte {
	record := make([]byte, 32)
	binary.NativeEndian.PutUint64(record[0:8], from.hi)
	binary.NativeEndian.PutUint64(record[8:16], from.lo)
	binary.NativeEndian.PutUint64(record[16:24], to.hi)
	binary.NativeEndian.PutUint64(record[24:32], to.lo)
	return record
}

// streamCoverage streams the view as maximal coverage ranges, merging
// adjacent segments regardless of their values (flat set semantics;
// Rust export.rs stream_coverage).
func streamCoverage(reader exportReader, view *exportView, sink func(u128, u128) *rpc.HandlerError) *rpc.HandlerError {
	var pendingFrom *u128
	var pendingTo u128
	segment := func(from, to u128, _ exportValue) *rpc.HandlerError {
		if pendingFrom == nil {
			value := from
			pendingFrom = &value
			pendingTo = to
			return nil
		}
		if from == pendingTo.addOne() {
			pendingTo = to
			return nil
		}
		if err := sink(*pendingFrom, pendingTo); err != nil {
			return err
		}
		value := from
		pendingFrom = &value
		pendingTo = to
		return nil
	}
	if err := streamSegments(reader, view, segment); err != nil {
		return err
	}
	// Flush the terminal pending segment (Rust's trailing
	// if-let flush after the sweep).
	if pendingFrom != nil {
		return sink(*pendingFrom, pendingTo)
	}
	return nil
}

// streamSegments streams one ordered canonical segment per constant
// semantic value (Rust export.rs stream_segments).
func streamSegments(reader exportReader, view *exportView, sink func(u128, u128, exportValue) *rpc.HandlerError) *rpc.HandlerError {
	info, herr := sdk(reader.Info())
	if herr != nil {
		return herr
	}
	if info.Family == iprangedb.AddressFamilyIPv4 {
		return streamSegmentsV4(reader, view, sink)
	}
	return streamSegmentsV6(reader, view, sink)
}

// checkCancelled reports the shared cancellation of a long export.
func checkCancelled(token *iprangedb.CancellationToken) *rpc.HandlerError {
	if token != nil && token.IsCancelled() {
		return rpc.NewHandlerError("cancelled", "not_started", "export was cancelled")
	}
	return nil
}

// exportViewCursor maps one cursor-open SDK failure; a mismatched
// database kind becomes the documented handle_wrong_kind adapter error
// (Rust export.rs view_cursor/view_error).
func exportViewCursor[T any](result T, err error) (T, *rpc.HandlerError) {
	if err != nil {
		var zero T
		return zero, exportViewError(readError(err))
	}
	return result, nil
}

func exportViewError(herr *rpc.HandlerError) *rpc.HandlerError {
	if herr.Code == "wrong_value_kind" || herr.Code == "wrong_structure_kind" {
		return rpc.NewHandlerError("handle_wrong_kind", "not_started",
			"reader does not support the requested export view")
	}
	return herr
}

func streamSegmentsV4(reader exportReader, view *exportView, sink func(u128, u128, exportValue) *rpc.HandlerError) *rpc.HandlerError {
	switch view.kind {
	case exportViewDirect:
		cursor, herr := exportViewCursor(reader.DirectCursorV4(iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		for {
			record, ok, err := cursor.NextRange()
			if err != nil {
				return exportViewError(readError(err))
			}
			if !ok {
				return nil
			}
			value := record.Value
			if err := sink(u128{lo: uint64(record.From)}, u128{lo: uint64(record.To)},
				exportValue{direct: &value}); err != nil {
				return err
			}
		}
	case exportViewStructured:
		cursor, herr := exportViewCursor(reader.NetworkEnrichmentV1CursorV4(iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		defer cursor.Close()
		// One catalog sweep for the whole stream and one reusable
		// membership-word buffer shared by every record.
		snapshot, herr := exportFeedSnapshot(reader)
		if herr != nil {
			return herr
		}
		var words []uint64
		for {
			record, ok, err := cursor.NextRange()
			if err != nil {
				return exportViewError(readError(err))
			}
			if !ok {
				return nil
			}
			feeds, herr := ThreatFeedNames(record.Value, snapshot, &words)
			if herr != nil {
				return herr
			}
			decoded, err := record.Value.Value()
			if err != nil {
				return readError(err)
			}
			if err := sink(u128{lo: uint64(record.From)}, u128{lo: uint64(record.To)},
				exportValue{structured: NetworkEnrichmentJSON(decoded, feeds)}); err != nil {
				return err
			}
		}
	case exportViewFeed:
		if _, herr := requireExportFeed(reader, view.feed); herr != nil {
			return herr
		}
		cursor, herr := exportViewCursor(reader.FeedRangeCursorV4(view.feed, iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		value := exportValue{feeds: []string{view.feed}}
		for {
			record, ok, err := cursor.NextRange()
			if err != nil {
				return exportViewError(readError(err))
			}
			if !ok {
				return nil
			}
			if err := sink(u128{lo: uint64(record.From)}, u128{lo: uint64(record.To)}, value); err != nil {
				return err
			}
		}
	default: // selection
		return streamSelectionV4(reader, view, sink)
	}
}

func streamSelectionV4(reader exportReader, view *exportView, sink func(u128, u128, exportValue) *rpc.HandlerError) *rpc.HandlerError {
	feeds, herr := resolveExportSelection(reader, view.named)
	if herr != nil {
		return herr
	}
	type cursorHead struct {
		cursor *iprangedb.FeedRangeCursorV4
		head   *u128Range
	}
	state := make([]cursorHead, 0, len(feeds))
	for _, feed := range feeds {
		cursor, herr := exportViewCursor(reader.FeedRangeCursorV4(feed.name, iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		record, ok, err := cursor.NextRange()
		if err != nil {
			return exportViewError(readError(err))
		}
		var head *u128Range
		if ok {
			value := u128Range{from: u128{lo: uint64(record.From)}, to: u128{lo: uint64(record.To)}}
			head = &value
		}
		state = append(state, cursorHead{cursor: cursor, head: head})
	}
	names := make([]string, 0, len(feeds))
	heads := make([]*u128Range, 0, len(state))
	for i := range state {
		names = append(names, feeds[i].name)
		heads = append(heads, state[i].head)
	}
	return selectionSweep(heads, names, func(index int) (*u128Range, *rpc.HandlerError) {
		record, ok, err := state[index].cursor.NextRange()
		if err != nil {
			return nil, exportViewError(readError(err))
		}
		if !ok {
			return nil, nil
		}
		return &u128Range{from: u128{lo: uint64(record.From)}, to: u128{lo: uint64(record.To)}}, nil
	}, func(from, to u128, names []string) *rpc.HandlerError {
		return sink(from, to, exportValue{feeds: names})
	})
}

func streamSegmentsV6(reader exportReader, view *exportView, sink func(u128, u128, exportValue) *rpc.HandlerError) *rpc.HandlerError {
	switch view.kind {
	case exportViewDirect:
		cursor, herr := exportViewCursor(reader.DirectCursorV6(iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		for {
			record, ok, err := cursor.NextRange()
			if err != nil {
				return exportViewError(readError(err))
			}
			if !ok {
				return nil
			}
			value := record.Value
			if err := sink(u128{hi: record.FromHi, lo: record.FromLo}, u128{hi: record.ToHi, lo: record.ToLo},
				exportValue{direct: &value}); err != nil {
				return err
			}
		}
	case exportViewStructured:
		cursor, herr := exportViewCursor(reader.NetworkEnrichmentV1CursorV6(iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		defer cursor.Close()
		snapshot, herr := exportFeedSnapshot(reader)
		if herr != nil {
			return herr
		}
		var words []uint64
		for {
			record, ok, err := cursor.NextRange()
			if err != nil {
				return exportViewError(readError(err))
			}
			if !ok {
				return nil
			}
			feeds, herr := ThreatFeedNames(record.Value, snapshot, &words)
			if herr != nil {
				return herr
			}
			decoded, err := record.Value.Value()
			if err != nil {
				return readError(err)
			}
			from := u128{hi: record.FromHi, lo: record.FromLo}
			to := u128{hi: record.ToHi, lo: record.ToLo}
			if err := sink(from, to, exportValue{structured: NetworkEnrichmentJSON(decoded, feeds)}); err != nil {
				return err
			}
		}
	case exportViewFeed:
		if _, herr := requireExportFeed(reader, view.feed); herr != nil {
			return herr
		}
		cursor, herr := exportViewCursor(reader.FeedRangeCursorV6(view.feed, iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		value := exportValue{feeds: []string{view.feed}}
		for {
			record, ok, err := cursor.NextRange()
			if err != nil {
				return exportViewError(readError(err))
			}
			if !ok {
				return nil
			}
			from := u128{hi: record.FromHi, lo: record.FromLo}
			to := u128{hi: record.ToHi, lo: record.ToLo}
			if err := sink(from, to, value); err != nil {
				return err
			}
		}
	default:
		return streamSelectionV6(reader, view, sink)
	}
}

func streamSelectionV6(reader exportReader, view *exportView, sink func(u128, u128, exportValue) *rpc.HandlerError) *rpc.HandlerError {
	feeds, herr := resolveExportSelection(reader, view.named)
	if herr != nil {
		return herr
	}
	type cursorHead struct {
		cursor *iprangedb.FeedRangeCursorV6
		head   *u128Range
	}
	state := make([]cursorHead, 0, len(feeds))
	for _, feed := range feeds {
		cursor, herr := exportViewCursor(reader.FeedRangeCursorV6(feed.name, iprangedb.RangeDirectionForward))
		if herr != nil {
			return herr
		}
		record, ok, err := cursor.NextRange()
		if err != nil {
			return exportViewError(readError(err))
		}
		var head *u128Range
		if ok {
			value := u128Range{from: u128{hi: record.FromHi, lo: record.FromLo}, to: u128{hi: record.ToHi, lo: record.ToLo}}
			head = &value
		}
		state = append(state, cursorHead{cursor: cursor, head: head})
	}
	names := make([]string, 0, len(feeds))
	heads := make([]*u128Range, 0, len(state))
	for i := range state {
		names = append(names, feeds[i].name)
		heads = append(heads, state[i].head)
	}
	return selectionSweep(heads, names, func(index int) (*u128Range, *rpc.HandlerError) {
		record, ok, err := state[index].cursor.NextRange()
		if err != nil {
			return nil, exportViewError(readError(err))
		}
		if !ok {
			return nil, nil
		}
		return &u128Range{from: u128{hi: record.FromHi, lo: record.FromLo}, to: u128{hi: record.ToHi, lo: record.ToLo}}, nil
	}, func(from, to u128, names []string) *rpc.HandlerError {
		return sink(from, to, exportValue{feeds: names})
	})
}

// selectedExportFeed is one resolved catalog feed of an export
// selection.
type selectedExportFeed struct {
	index uint32
	name  string
}

// resolveExportSelection resolves a selection to its catalog-ordered
// feed list (Rust export.rs resolve_selection): "all" sweeps the
// catalog, a named selection resolves each name; both sort by feed
// index so emitted names are always in catalog order.
func resolveExportSelection(reader exportReader, named []string) ([]selectedExportFeed, *rpc.HandlerError) {
	var feeds []selectedExportFeed
	if named == nil {
		cursor, herr := sdk(reader.FeedCursor())
		if herr != nil {
			return nil, herr
		}
		for {
			entry, ok, err := cursor.NextFeed()
			if err != nil {
				return nil, readError(err)
			}
			if !ok {
				break
			}
			feeds = append(feeds, selectedExportFeed{index: entry.Index, name: entry.Name})
		}
	} else {
		for _, name := range named {
			entry, herr := requireExportFeed(reader, name)
			if herr != nil {
				return nil, herr
			}
			feeds = append(feeds, selectedExportFeed{index: entry.Index, name: name})
		}
	}
	sort.Slice(feeds, func(i, j int) bool { return feeds[i].index < feeds[j].index })
	return feeds, nil
}

// requireExportFeed resolves one feed name against the opened reader
// (Rust export.rs require_feed).
func requireExportFeed(reader exportReader, name string) (iprangedb.FeedEntry, *rpc.HandlerError) {
	if _, err := iprangedb.NewFeedName(name); err != nil {
		return iprangedb.FeedEntry{}, rpc.NewHandlerError("name_invalid", "not_started",
			fmt.Sprintf("export feed name is invalid: %v", err))
	}
	entry, found, err := reader.LookupFeed(name)
	if err != nil {
		return iprangedb.FeedEntry{}, readError(err)
	}
	if !found {
		return iprangedb.FeedEntry{}, rpc.NewHandlerError("name_not_found", "not_started",
			fmt.Sprintf("export feed does not exist: %s", name))
	}
	return entry, nil
}

// u128Range is one inclusive canonical coverage interval.
type u128Range struct {
	from u128
	to   u128
}

// selectionSweep is a K-way sweep over per-feed forward range cursors:
// one maximal segment is emitted whenever the catalog-ordered set of
// selected feeds covering the address changes; only each feed's current
// head range is retained, so the working set stays bounded by the
// selected-feed count (Rust export.rs selection_sweep).
func selectionSweep(heads []*u128Range, names []string,
	advance func(index int) (*u128Range, *rpc.HandlerError),
	emit func(from, to u128, names []string) *rpc.HandlerError) *rpc.HandlerError {
	current := heads
	type pendingKey struct {
		from  u128
		index int
	}
	var pending []pendingKey
	insertPending := func(key pendingKey) {
		at := 0
		for at < len(pending) && (pending[at].from.compare(key.from) < 0 ||
			(pending[at].from == key.from && pending[at].index < key.index)) {
			at++
		}
		pending = append(pending, pendingKey{})
		copy(pending[at+1:], pending[at:])
		pending[at] = key
	}
	for index, head := range current {
		if head != nil {
			insertPending(pendingKey{from: head.from, index: index})
		}
	}
	type activeKey struct {
		index int
		to    u128
	}
	var active []activeKey
	var position u128
	activate := func() {
		for len(pending) > 0 {
			next := pending[0]
			if next.from.compare(position) > 0 {
				break
			}
			pending = pending[1:]
			to := u128{}
			if current[next.index] != nil {
				to = current[next.index].to
			}
			at := 0
			for at < len(active) && active[at].index < next.index {
				at++
			}
			active = append(active, activeKey{})
			copy(active[at+1:], active[at:])
			active[at] = activeKey{index: next.index, to: to}
		}
	}
	for {
		if len(active) == 0 {
			if len(pending) == 0 {
				return nil
			}
			position = pending[0].from
			activate()
		}
		var boundary *u128
		for _, entry := range active {
			// Rust checked_add: a range ending at the family maximum
			// contributes no boundary (the sweep then emits through the
			// maximum and returns).
			if entry.to.hi == ^uint64(0) && entry.to.lo == ^uint64(0) {
				continue
			}
			next := entry.to.addOne()
			if boundary == nil || next.compare(*boundary) < 0 {
				value := next
				boundary = &value
			}
		}
		if len(pending) > 0 {
			next := pending[0].from
			if boundary == nil || next.compare(*boundary) < 0 {
				value := next
				boundary = &value
			}
		}
		segmentNames := make([]string, 0, len(active))
		for _, entry := range active {
			segmentNames = append(segmentNames, names[entry.index])
		}
		if boundary == nil {
			if err := emit(position, u128{hi: ^uint64(0), lo: ^uint64(0)}, segmentNames); err != nil {
				return err
			}
			return nil
		}
		end := boundary.subOne()
		if err := emit(position, end, segmentNames); err != nil {
			return err
		}
		position = *boundary
		index := 0
		for index < len(active) {
			if active[index].to.compare(*boundary) < 0 {
				finished := active[index].index
				active = append(active[:index], active[index+1:]...)
				next, herr := advance(finished)
				if herr != nil {
					return herr
				}
				current[finished] = next
				if next != nil {
					insertPending(pendingKey{from: next.from, index: finished})
				}
			} else {
				index++
			}
		}
		activate()
	}
}

// exportFeedSnapshot sweeps the catalog once into an ordered name
// snapshot (the shared FeedSnapshot authority over the export reader
// surface, used by ThreatFeedNames).
func exportFeedSnapshot(reader exportReader) (*FeedSnapshot, *rpc.HandlerError) {
	cursor, herr := sdk(reader.FeedCursor())
	if herr != nil {
		return nil, herr
	}
	snapshot := &FeedSnapshot{}
	for {
		entry, ok, err := cursor.NextFeed()
		if err != nil {
			return nil, readError(err)
		}
		if !ok {
			return snapshot, nil
		}
		snapshot.Feeds = append(snapshot.Feeds, FeedIndexName{Index: entry.Index, Name: entry.Name})
	}
}

// u128 is the numeric address type of the export encoders; v4 values
// live entirely in lo, v6 uses both halves.
type u128 struct {
	hi uint64
	lo uint64
}

// addOne returns the following address; the family maximum saturates
// exactly like u128::saturating_add (the adjacency checks of the sweep
// treat a saturated maximum as the end of the address space).
func (v u128) addOne() u128 {
	if v.hi == ^uint64(0) && v.lo == ^uint64(0) {
		return v
	}
	lo, carry := add64(v.lo, 1)
	hi := v.hi
	if carry {
		hi++
	}
	return u128{hi: hi, lo: lo}
}

// subOne returns the preceding address; the family minimum saturates
// (a sweep boundary is never below one).
func (v u128) subOne() u128 {
	if v.hi == 0 && v.lo == 0 {
		return v
	}
	lo, borrow := sub64(v.lo, 1)
	hi := v.hi
	if borrow {
		hi--
	}
	return u128{hi: hi, lo: lo}
}

func add64(left, right uint64) (uint64, bool) {
	sum := left + right
	return sum, sum < left
}

func sub64(left, right uint64) (uint64, bool) {
	return left - right, left < right
}

func (v u128) compare(other u128) int {
	if v.hi < other.hi {
		return -1
	}
	if v.hi > other.hi {
		return 1
	}
	if v.lo < other.lo {
		return -1
	}
	if v.lo > other.lo {
		return 1
	}
	return 0
}

// lowMask returns (1<<bits)-1 for bits 0..127 (u128 host mask used by
// the netset splitter).
func lowMask(bits uint32) u128 {
	if bits >= 64 {
		return u128{hi: ^uint64(0) >> (128 - bits), lo: ^uint64(0)}
	}
	if bits == 0 {
		return u128{}
	}
	return u128{lo: (uint64(1) << bits) - 1}
}

// oneHot returns 1<<shift for shift 0..127.
func oneHot(shift uint32) u128 {
	if shift >= 64 {
		return u128{hi: uint64(1) << (shift - 64)}
	}
	return u128{lo: uint64(1) << shift}
}

// decimal renders the u128 as a canonical decimal string through the
// exact 129-bit decimal decoder (the single authority for the wire
// counting vocabulary).
func (v u128) decimal() string {
	value, err := iprangedb.NewCardinality129(0, v.hi, v.lo)
	if err != nil {
		return "0"
	}
	return value.String()
}

// spanOf returns the exact inclusive address count of one canonical
// span (binary-format-v4.md section 17; Rust inclusive_span).
func spanOf(from, to u128, hostPrefix uint32) (iprangedb.Cardinality129, *rpc.HandlerError) {
	if hostPrefix == 32 {
		span, err := iprangedb.IPv4Inclusive(uint32(from.lo), uint32(to.lo))
		if err != nil {
			return iprangedb.CardinalityZero(), rpc.NewHandlerError("output_limit", "not_started",
				"export address cardinality exceeded the exact 129-bit counter")
		}
		return span, nil
	}
	span, err := iprangedb.IPv6Inclusive(from.hi, from.lo, to.hi, to.lo)
	if err != nil {
		return iprangedb.CardinalityZero(), rpc.NewHandlerError("output_limit", "not_started",
			"export address cardinality exceeded the exact 129-bit counter")
	}
	return span, nil
}

// exportPushAddress writes the canonical address text for one numeric
// family-local address into a reusable buffer (Rust push_address).
func exportPushAddress(buffer []byte, value u128, hostPrefix uint32) []byte {
	if hostPrefix == 32 {
		v4 := uint32(value.lo)
		return append(buffer, netip.AddrFrom4([4]byte{byte(v4 >> 24), byte(v4 >> 16), byte(v4 >> 8), byte(v4)}).String()...)
	}
	var bytes [16]byte
	bytes[0] = byte(value.hi >> 56)
	bytes[1] = byte(value.hi >> 48)
	bytes[2] = byte(value.hi >> 40)
	bytes[3] = byte(value.hi >> 32)
	bytes[4] = byte(value.hi >> 24)
	bytes[5] = byte(value.hi >> 16)
	bytes[6] = byte(value.hi >> 8)
	bytes[7] = byte(value.hi)
	bytes[8] = byte(value.lo >> 56)
	bytes[9] = byte(value.lo >> 48)
	bytes[10] = byte(value.lo >> 40)
	bytes[11] = byte(value.lo >> 32)
	bytes[12] = byte(value.lo >> 24)
	bytes[13] = byte(value.lo >> 16)
	bytes[14] = byte(value.lo >> 8)
	bytes[15] = byte(value.lo)
	return append(buffer, netip.AddrFrom16(bytes).String()...)
}

// pushRangesLine writes one from-to line; a singleton is emitted as
// its single address (Rust push_ranges_line).
func pushRangesLine(line []byte, from, to u128, hostPrefix uint32) []byte {
	line = exportPushAddress(line, from, hostPrefix)
	if from != to {
		line = append(line, '-')
		line = exportPushAddress(line, to, hostPrefix)
	}
	return line
}

// emitNetset emits the canonical minimal CIDR blocks covering
// [from, to] using only enabled prefixes, in address order (the
// released split_range() algorithm generalized to both families; Rust
// emit_netset/split_netset).
func emitNetset(from, to u128, filter *prefixFilter, line *[]byte,
	emit func([]byte, iprangedb.Cardinality129) *rpc.HandlerError) *rpc.HandlerError {
	return splitNetset(u128{}, 0, from, to, filter, line, emit)
}

func splitNetset(base u128, prefix uint32, from, to u128, filter *prefixFilter, line *[]byte,
	emit func([]byte, iprangedb.Cardinality129) *rpc.HandlerError) *rpc.HandlerError {
	host := filter.hostPrefix
	bits := host - prefix
	networkEnd := u128{}
	if bits >= 128 {
		networkEnd = u128{hi: ^uint64(0), lo: ^uint64(0)}
	} else {
		networkEnd = u128{hi: base.hi | lowMask(bits).hi, lo: base.lo | lowMask(bits).lo}
	}
	if from == base && to == networkEnd && filter.isEnabled(prefix) {
		buf := (*line)[:0]
		buf = exportPushAddress(buf, base, host)
		if prefix != host {
			buf = append(buf, '/')
			buf = appendDecimal(buf, prefix)
		}
		span, herr := spanOf(base, networkEnd, host)
		if herr != nil {
			return herr
		}
		*line = buf
		return emit(buf, span)
	}
	half := u128{hi: base.hi | oneHot(host-prefix-1).hi, lo: base.lo | oneHot(host-prefix-1).lo}
	if to.compare(half) < 0 {
		return splitNetset(base, prefix+1, from, to, filter, line, emit)
	}
	if from.compare(half) >= 0 {
		return splitNetset(half, prefix+1, from, to, filter, line, emit)
	}
	if herr := splitNetset(base, prefix+1, from, half.subOne(), filter, line, emit); herr != nil {
		return herr
	}
	return splitNetset(half, prefix+1, half, to, filter, line, emit)
}

// emitIpset emits every address in [from, to], one per line, reusing
// the caller's line buffer (Rust emit_ipset).
func emitIpset(from, to u128, hostPrefix uint32, line *[]byte,
	emit func([]byte) *rpc.HandlerError) *rpc.HandlerError {
	for address := from; ; {
		*line = exportPushAddress((*line)[:0], address, hostPrefix)
		if herr := emit(*line); herr != nil {
			return herr
		}
		if address == to {
			return nil
		}
		address = address.addOne()
	}
}

// appendDecimal appends one u32 as decimal text into the buffer.
func appendDecimal(buffer []byte, value uint32) []byte {
	if value == 0 {
		return append(buffer, '0')
	}
	var digits [10]byte
	used := 0
	for value > 0 {
		digits[used] = byte('0' + value%10)
		value /= 10
		used++
	}
	for i := used - 1; i >= 0; i-- {
		buffer = append(buffer, digits[i])
	}
	return buffer
}

// pushJSONString appends one JSON string literal with standard escaping
// into the buffer (byte-identical with encoding/json string output;
// Rust push_json_string).
func pushJSONString(buffer []byte, value string) []byte {
	buffer = append(buffer, '"')
	for _, r := range value {
		switch r {
		case '"':
			buffer = append(buffer, '\\', '"')
		case '\\':
			buffer = append(buffer, '\\', '\\')
		case '\b':
			buffer = append(buffer, '\\', 'b')
		case '\f':
			buffer = append(buffer, '\\', 'f')
		case '\n':
			buffer = append(buffer, '\\', 'n')
		case '\r':
			buffer = append(buffer, '\\', 'r')
		case '\t':
			buffer = append(buffer, '\\', 't')
		default:
			if r < 0x20 {
				const digits = "0123456789abcdef"
				buffer = append(buffer, '\\', 'u', '0', '0', digits[(r>>4)&0xf], digits[r&0xf])
			} else {
				buffer = append(buffer, string(r)...)
			}
		}
	}
	return append(buffer, '"')
}

// writeJSONValue appends one compact JSON value into the buffer (all
// v4 wire values are valid UTF-8; Rust write_json_value).
func writeJSONValue(buffer []byte, value map[string]any) ([]byte, *rpc.HandlerError) {
	encoded, err := rpc.MarshalJSONL(value)
	if err != nil {
		return buffer, rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("export row JSON encoding failed: %v", err))
	}
	return append(buffer, encoded...), nil
}

// refuseOutputOverSource refuses a file destination that resolves to
// the source database file (Rust output.rs refuse_output_over_source
// parity): the v1 contract never modifies its input files, so the
// destination must differ from the source under canonical same-file
// resolution. Two identities are compared: the canonical pathname
// spelling (symlinks and ./.. decorations resolved as far as the
// filesystem allows) and, when the destination and the captured
// source/sidecar identities exist, the numeric file identity
// ((device, inode) / (volume serial, file index); Rust FileIdentity
// parity), so a destination that names the same file through a
// rename or a hard link is refused too.
func refuseOutputOverSource(destination, source string, sourceID, sidecarID *rpc.FileIdentity) *rpc.HandlerError {
	destID := captureFileIdentity(destination)
	if sameCanonical(canonicalAbsolute(destination), canonicalAbsolute(source)) {
		return refusedSameSource()
	}
	// The live database's reader-coordination sidecar (main +
	// ".readers") is a distinct file that records reader state;
	// publishing output over it destroys the source's readability, so
	// it is refused exactly like the main database (pathname and
	// same-file arms, Rust refuse_output_over_source parity).  The
	// sidecar component is derived lexically (no main-name grammar),
	// so a reserved-name source still derives a sidecar component and
	// is refused preflight (wave-19.6 parity finding).
	if sidecar, ok := outputSidecarPath(source); ok {
		if sameCanonical(canonicalAbsolute(destination), canonicalAbsolute(sidecar)) {
			return refusedSameSource()
		}
		// Same-ancestor arm (Windows only): two spellings of the same
		// eventual file can live in different namespace families
		// (drive letter, loopback UNC like \\localhost\C$\\... or
		// \\127.0.0.1\\C$\\..., volume GUID like \\\\?\\Volume{...})
		// that no lexical strip can reconcile, while the deepest
		// EXISTING ancestor of both spellings is the same real
		// directory with one kernel file identity.  Refuse when the
		// ancestor identities match AND the folded missing suffix
		// matches; distinct directories or distinct names stay
		// allowed (wave-19.15 security P1).
		if runtime.GOOS == "windows" && sameAncestorPath(destination, sidecar) {
			return refusedSameSource()
		}
		// The file-identity arm prefers the sidecar identity captured
		// at reader open (a renamed sidecar keeps its identity) and
		// falls back to a fresh capture at the derived path for
		// ephemeral preflights (Rust sidecar twin parity; the derived
		// path is the sidecar itself, never double-suffixed).
		twin := sidecarID
		if twin == nil {
			twin = captureFileIdentity(sidecar)
		}
		if destID.SameFile(twin) {
			return refusedSameSource()
		}
	}
	if destID.SameFile(sourceID) {
		return refusedSameSource()
	}
	return nil
}

// outputSidecarPath derives the reader-coordination sidecar component
// of a main database path lexically (pathname.FileName/WithFileName
// parity with the reader, Rust sidecar_path parity): no main-name
// grammar is applied, so a reserved-name source still derives a
// sidecar component and the guard refuses it preflight.
func outputSidecarPath(path string) (string, bool) {
	name, ok := pathname.FileName(path)
	if !ok {
		return "", false
	}
	return pathname.WithFileName(path, name+format.CoordinationSuffix), true
}

// sameCanonical compares two canonical pathname identities under the
// destination filesystem's filename-equivalence rules.  On Windows an
// absent sidecar (or any not-yet-existing destination) has no file
// identity for the os.SameFile arms, so the pathname arm must apply
// the platform's name equivalence: Win32 strips trailing dots and
// spaces from the final component at create, and the volume's case
// table equates name spellings.  The fold below is the Unicode full
// lowercase shared with Rust (the only BMP character whose full
// lowercase expands, U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE, is
// mapped to its two-rune form so both engines fold byte-identically);
// it is a practical approximation of the per-volume upcase table for
// ABSENT names — existing files stay protected by the OS file-identity
// arm (wave 19 round 19.12 security finding).  POSIX names are
// case-sensitive and compare exactly.
func sameCanonical(a, b string) bool {
	if a == b {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	return windowsNameIdentity(a) == windowsNameIdentity(b)
}

// windowsFoldPath returns the Windows-equivalence fold of a canonical
// pathname: trailing dots/spaces of the final component trimmed (the
// Win32 create normalization), then Unicode lowercase per rune with
// the Rust full-lowercase expansion for U+0130.
func windowsFoldPath(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	comp := path[i+1:]
	if comp != "." && comp != ".." {
		if trimmed := strings.TrimRight(comp, ". "); trimmed != "" && trimmed != comp {
			path = path[:len(path)-len(comp)] + trimmed
		}
	}
	var b strings.Builder
	b.Grow(len(path) + 2)
	runes := []rune(path)
	for i, r := range runes {
		// rustc 1.97.1 (the Windows Rust product toolchain) applies
		// the Unicode-16 lowercase mappings for U+1C89, U+A7CB,
		// U+A7CC, U+A7CE, U+A7D2, U+A7D4, U+A7DA, U+A7DC, Garay
		// U+10D50..U+10D65, and Kirat Rai U+16EA0..U+16EB8, while
		// the go1.26.5 (Windows Go product toolchain) tables predate
		// them (added in go1.27), and applies the contextual
		// Final_Sigma rule for U+03A3 (word-final sigma) that a
		// per-rune simple mapping cannot express.  The fold maps
		// them explicitly so the two Windows products fold
		// byte-identically; the differential enumerations are in
		// v4/cli/evidence/fold-enum/ (wave 19 round 19.13
		// fold-parity findings).
		switch r {
		case 0x0130:
			// Rust char::to_lowercase expansion parity (the only
			// BMP character whose full lowercase has two runes).
			b.WriteString("i\u0307")
			continue
		case 0x03A3:
			// Final_Sigma (Unicode 3.13): capital sigma folds to
			// final sigma U+03C2 when it is preceded by a cased
			// character and not followed by a cased character
			// (case-ignorable characters are skipped on both
			// sides), exactly like Rust str::to_lowercase.
			prev, next := -1, len(runes)
			for j := i - 1; j >= 0; j-- {
				if !isCaseIgnorable(runes[j]) {
					prev = j
					break
				}
			}
			for j := i + 1; j < len(runes); j++ {
				if !isCaseIgnorable(runes[j]) {
					next = j
					break
				}
			}
			if prev >= 0 && isCased(runes[prev]) && (next >= len(runes) || !isCased(runes[next])) {
				b.WriteRune(0x03C2)
			} else {
				b.WriteRune(0x03C3)
			}
			continue
		case 0x1C89:
			b.WriteRune(0x1C8A)
			continue
		case 0xA7CB:
			b.WriteRune(0x0264)
			continue
		case 0xA7CC:
			b.WriteRune(0xA7CD)
			continue
		case 0xA7CE, 0xA7D2, 0xA7D4:
			b.WriteRune(r + 1)
			continue
		case 0xA7DA:
			b.WriteRune(0xA7DB)
			continue
		case 0xA7DC:
			b.WriteRune(0x019B)
			continue
		}
		if r >= 0x10D50 && r <= 0x10D65 { // Garay uppercase block
			b.WriteRune(r + 0x20)
			continue
		}
		if r >= 0x16EA0 && r <= 0x16EB8 { // Kirat Rai uppercase block
			b.WriteRune(r + 0x1B)
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// windowsNameIdentity returns the Windows-equivalence identity of a
// canonical pathname used by sameCanonical: the windowsFoldPath
// lowercasing plus the per-volume upcase-name equivalence of the
// Greek sigma class.  NTFS treats the sigma spellings as one name
// family (the classic default upcase table maps U+03A3/U+03C2/U+03C3
// all to U+03A3; on the qualified Win11 volume the measured classes
// are {U+03A3, U+03C3} equal with U+03C2 distinct); the contextual
// Final_Sigma fold would split the family into U+03C2 (word-final)
// and U+03C3, letting a sigma-spelled sidecar escape the guard, so
// the comparison collapses all three sigmas to one identity.  The
// collapse is conservative (refusal direction only): a genuinely
// distinct U+03C2 spelling on a volume that separates it is refused
// exactly like the fold already refuses it (wave 19 round 19.14
// astra findings).
func windowsNameIdentity(path string) string {
	folded := windowsFoldPath(path)
	if !strings.ContainsAny(folded, "\u03A3\u03C2\u03C3") {
		return folded
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case 0x03A3, 0x03C2, 0x03C3:
			return 0x03A3
		}
		return r
	}, folded)
}

// windowsStripExtended removes a leading Win32 device-family prefix
// from a canonical identity: extended-length ("\\?\"), device
// ("\\.\"), and the NT object-manager spelling of the verbatim
// family ("\??\").  The prefixed spelling names the same file as the
// ordinary one, and Rust's canonicalize re-emits every resolved
// identity in the "\\?\" form, so the pathname comparison must
// reconcile all three spellings (wave 19 round 19.14 astra parity
// finding; the "\??\" sibling is the wave-19.15 security finding).
// A "UNC\" head after any prefix becomes the ordinary
// "\\server\share" form.  Non-Windows identities pass through
// unchanged.
func windowsStripExtended(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	for _, prefix := range []string{`\\?\`, `\\.\`, `\??\`} {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := path[len(prefix):]
		if len(rest) >= 4 && strings.EqualFold(rest[:4], "unc\\") {
			return `\\` + rest[4:]
		}
		return rest
	}
	return path
}

func refusedSameSource() *rpc.HandlerError {
	return rpc.NewHandlerError("invalid_argument", "not_started",
		"destination must differ from the source database")
}

// windowsTrimFinalLeaf returns the path with the Win32
// create-normalization characters (trailing dots and spaces) removed
// from its FINAL component only, leaving "." and ".." untouched:
// exactly the final-component fold windowsFoldPath applies to
// identities, without the case fold, so a split walk probes the name
// the kernel resolves.  A "\\?\GLOBALROOT\...\db.iprange.readers.."
// spelling names the reader sidecar file (the trailing-dot/space
// spelling folds to it at the Win32 layer), but EvalSymlinks and
// os.Stat see the literal NT name under the extended-length namespace
// heads and fail, so an untrimmed walk collects the literal leaf into
// the missing suffix while the existing sidecar's walk collects
// nothing: the suffix lengths diverge and the same-ancestor guard
// lets the destination through (wave-19.18 parity P1: 12
// namespace-head x trailing-dot/space destinations published in Go
// while Rust refused them, .local/parity/w1920/remote/report7.json).
// Call sites are Windows-gated; the function itself is pure string
// logic so Linux CI pins it (TestWindowsTrimFinalLeaf).
func windowsTrimFinalLeaf(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	comp := path[i+1:]
	if comp == "." || comp == ".." {
		return path
	}
	if trimmed := strings.TrimRight(comp, ". "); trimmed != "" && trimmed != comp {
		return path[:len(path)-len(comp)] + trimmed
	}
	return path
}

// sameAncestorPath reports whether two path spellings denote the
// same eventual file by comparing the kernel file identity of their
// deepest existing ancestors plus the folded relative suffix: the
// namespace-independent part of canonicalSplitPath (wave-19.15 security
// P1, UNC-loopback and volume-GUID sidecar spellings).
func sameAncestorPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		a = strings.ReplaceAll(a, "/", `\`)
		b = strings.ReplaceAll(b, "/", `\`)
		// Probe the spliced spellings under the final name the kernel
		// resolves: a trailing-dot/space leaf folds to the sidecar
		// name, so the walk must see the folded leaf or the lengths of
		// the missing suffixes diverge from the existing sidecar's
		// (empty) suffix and the leaf-class escape passes the guard
		// (wave-19.18 parity P1).
		a = windowsTrimFinalLeaf(a)
		b = windowsTrimFinalLeaf(b)
	}
	// Anchor relative spellings at the process working directory
	// before the split walk, exactly like Rust canonical_split (the
	// astra turn-5 parity finding: a bare relative name has no
	// parent below the drive root, which would compare the drive
	// root against the destination's real ancestor and let a
	// relative source or destination spelling escape the namespace
	// arm).  canonicalAbsolute is NOT used here: it would strip the
	// extended-length family prefix from a verbatim or volume-GUID
	// spelling and turn an absolute identity into a relative one,
	// regressing the volume-GUID arm (wave-19.17 harness run).
	// pathname.Push mirrors Rust PathBuf::push, so drive-relative
	// ("C:name") and rooted ("\name") spellings are preserved.
	if !filepath.IsAbs(a) {
		if cwd, err := os.Getwd(); err == nil {
			a = pathname.Push(cwd, a)
		}
	}
	if !filepath.IsAbs(b) {
		if cwd, err := os.Getwd(); err == nil {
			b = pathname.Push(cwd, b)
		}
	}
	aAnc, aSuf, aOK := canonicalSplitPath(a)
	bAnc, bSuf, bOK := canonicalSplitPath(b)
	if !aOK || !bOK || len(aSuf) != len(bSuf) {
		return false
	}
	for i := range aSuf {
		if !sameSuffixComponent(aSuf[i], bSuf[i]) {
			return false
		}
	}
	aID := captureFileIdentity(aAnc)
	bID := captureFileIdentity(bAnc)
	return aID != nil && bID != nil && aID.Dev == bID.Dev && aID.Ino == bID.Ino
}

// sameSuffixComponent compares one missing-suffix component under
// the destination filesystem's name-equivalence rules: the Windows
// fold (case, trailing dots/spaces, sigma class) or exact bytes.
func sameSuffixComponent(a, b string) bool {
	if runtime.GOOS == "windows" {
		return windowsNameIdentity(a) == windowsNameIdentity(b)
	}
	return a == b
}

// canonicalAbsolute resolves path to a stable identity for same-file
// comparison: the deepest existing ancestor is symlink-resolved and
// the remaining components are appended (Rust output.rs
// canonical_absolute parity), so a not-yet-existing destination still
// resolves through symlinked parents.
//
// The raw path is walked without up-front Clean: a ".." component is
// resolved with symlink semantics by EvalSymlinks (POSIX: ".." pops
// the symlink TARGET's parent, e.g. "/var/run/.." is "/" when
// /var/run is a symlink to /run). Cleaning up front would fold ".."
// against the symlink component lexically ("/var/run/.." -> "/var")
// and the guard would miss a destination that IS the source (wave 19
// round 19.8 parity finding).
func canonicalAbsolute(path string) string {
	if runtime.GOOS == "windows" {
		// Rust's PathBuf normalizes every separator to '\' eagerly
		// at parse; Go's filepath keeps the caller spelling, and
		// EvalSymlinks preserves it too, so a forward-slash
		// verbatim spelling ("\\?\C:/dir/...") produced a
		// mixed-separator identity that escaped the same-source
		// guard and failed later at the kernel rename with
		// io/read_only_failure instead of the canonical guard error
		// (wave-19.15 security P2).  Normalizing here restores the
		// Rust behavior for every identity the guard compares.
		path = strings.ReplaceAll(path, "/", `\`)
	}
	absolute := path
	if !filepath.IsAbs(path) {
		if cwd, err := os.Getwd(); err == nil {
			// pathname.Push mirrors Rust PathBuf::push exactly and
			// never cleans: filepath.Join would fold ".." components
			// before the symlink walk (wave-19.9 security finding),
			// while a plain string concatenation breaks Windows
			// drive-rooted spellings ("\x" must keep the base's
			// volume and replace its directory; wave-19.10 astra
			// finding).
			absolute = pathname.Push(cwd, path)
		}
	}
	ancestor, suffix, ok := canonicalSplitPath(absolute)
	if !ok {
		return windowsStripExtended(windowsAbsolutize(filepath.Clean(absolute)))
	}
	// suffix is already in path order (deepest-collected reversed).
	resolved := ancestor
	for _, name := range suffix {
		resolved = filepath.Join(resolved, name)
	}
	return windowsStripExtended(resolved)
}

// windowsUncProbe rewrites a leading verbatim-family UNC prefix
// ("\\?\UNC\server\share" or the NT object-manager twin
// "\??\UNC\server\share") to the ordinary UNC spelling
// ("\\server\share") for filesystem probes: the two spellings name
// the same shares and the ordinary form is walkable by Go's
// EvalSymlinks, which cannot open the verbatim "server" prefix
// alone (wave-19.15 security P1).
func windowsUncProbe(path string) string {
	for _, prefix := range []string{`\\?\UNC\`, `\??\UNC\`} {
		// The kernel resolves the UNC head case-insensitively, so a
		// lowercase spelling must be presented to the walk in the
		// ordinary form exactly like the canonical one (wave-19.17
		// parity P1: lowercase `\\?\\unc\\localhost\\...`
		// bypassed the rewrites and delivered over the live sidecar
		// while Rust refused it).
		if len(path) >= len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) {
			return `\\` + path[len(prefix):]
		}
	}
	return path
}

// globalrootFamilyProbe reports whether path lives under one of the
// NT device-root namespace heads ("\\?\GLOBALROOT\", its "\\.\"
// twin, and the object-manager "\??\" twin), compared
// case-insensitively because the NT namespace resolves the head in
// any spelling case (wave-19.17 parity P1: the case-sensitive gate
// let lowercase `globalroot` deliver over the live sidecar while
// Rust refused it).  The literals are single-separator raw strings:
// a doubled-separator literal can never name a real path and
// silently reverts the gate (wave-19.18 regression class, caught by
// the native end-to-end pin and pinned platform-independently by
// TestGlobalrootFamilyProbe).  The trailing separator scopes the
// family to the namespace head itself, so a look-alike device
// component ("\\?\GLOBALROOTX\") stays outside the gate.
func globalrootFamilyProbe(path string) bool {
	for _, prefix := range []string{`\\?\GLOBALROOT\`, `\\.\GLOBALROOT\`, `\??\GLOBALROOT\`} {
		if len(path) >= len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// canonicalSplitPath walks an anchored, separator-normalized
// spelling exactly like canonicalAbsolute but returns the deepest
// EXISTING ancestor and the missing suffix separately: the ancestor
// keeps a kernel file identity that is namespace-independent (drive
// letter, loopback UNC, volume GUID all name the same real
// directory), which the same-ancestor guard arm compares; ok=false
// means no ancestor exists (the whole spelling is not-yet-existing),
// so the string identity fallback applies (wave-19.15 security P1).
func canonicalSplitPath(path string) (ancestor string, suffix []string, ok bool) {
	var missing []string
	probe := path
	for {
		// Go's EvalSymlinks probes path components one by one and
		// cannot open the "\\?\UNC\server" prefix alone (CreateFile
		// fails with ERROR_INVALID_NAME), while the ordinary UNC
		// spelling "\\server\share" walks fine; the two spellings
		// name the same shares, so the deepest-existing-ancestor
		// walk probes through the ordinary spelling (wave-19.15
		// security P1).  "\\??\UNC\" was already presented as its
		// verbatim twin by normalize_nt_namespace.
		probe = windowsUncProbe(probe)
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			// missing holds the walked components deepest-first;
			// reverse it so the suffix reads in path order.
			suffix = make([]string, len(missing))
			for i, name := range missing {
				suffix[len(missing)-1-i] = name
			}
			return windowsAbsolutize(resolved), suffix, true
		}
		// Namespace families that Go's EvalSymlinks cannot walk but
		// the kernel resolves through ordinary file APIs (the NT
		// device-root namespace \\?\\GLOBALROOT\\Device\\
		// HarddiskVolumeN\\... and its \\.\\ and \\??\\ twins):
		// the symlink walk fails at the intermediate \\Device
		// component while the publication path opens the same real
		// file through the caller spelling.  os.Stat proves the
		// probe is the deepest existing ancestor and gives the
		// kernel identity the same-ancestor arm compares, so the raw
		// existing ancestor is authoritative (wave-19.17 security P1:
		// Go delivered metadata over the live sidecar through the
		// GLOBALROOT spelling while Rust refused it).  The fallback
		// is gated to the GLOBALROOT family because a raw-ancestor
		// shortcut in any other class would skip the symlink-aware
		// ".." resolution the walk performs (the wave-19.17
		// portability P1: the relative symlink-plus-".." class
		// regressed TestCanonicalAbsoluteRelativeSymlinkDotDot).
		if runtime.GOOS == "windows" && globalrootFamilyProbe(probe) {
			if _, statErr := os.Stat(probe); statErr == nil {
				suffix = make([]string, len(missing))
				for i, name := range missing {
					suffix[len(missing)-1-i] = name
				}
				return windowsAbsolutize(probe), suffix, true
			}
		}
		name, parent := pathLeaf(probe)
		if name == "" || parent == probe {
			return "", nil, false
		}
		missing = append(missing, name)
		probe = parent
	}
}

// windowsAbsolutize resolves a drive-relative or rooted-without-volume
// identity against the OS per-drive/current-drive working directory,
// exactly like the file APIs the publication path will use.  Go's
// EvalSymlinks keeps drive-relative results relative ("C:" resolves
// to "C:."), and filepath.Join on such a result would fold to the
// drive root instead of the drive's working directory; filepath.Abs
// on Windows resolves through GetFullPathName, matching how Rust
// canonicalize re-resolves the drive-relative parent (wave 19 round
// 19.11 astra finding).  POSIX paths are returned unchanged: on
// POSIX canonicalAbsolute already anchors relative inputs to the
// absolute process working directory.
func windowsAbsolutize(path string) string {
	if runtime.GOOS != "windows" || filepath.IsAbs(path) {
		return path
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

// pathLeaf returns (last component, parent directory) with Rust Path
// component semantics.  A trailing separator is not a component, so
// "/a/b/" yields ("b", "/a").  The parent is computed RAW: Go's
// filepath.Dir cleans its result, which would fold a ".." component
// lexically ("/var/run/../x" -> "/var") and defeat the symlink-aware
// walk of canonicalAbsolute.
func pathLeaf(path string) (string, string) {
	trimmed := path
	for len(trimmed) > 1 && os.IsPathSeparator(trimmed[len(trimmed)-1]) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) <= 1 {
		return "", path
	}
	name := filepath.Base(trimmed)
	separator := len(trimmed) - 1
	for separator > 0 && !os.IsPathSeparator(trimmed[separator]) {
		separator--
	}
	if separator == 0 {
		// Rust Path::parent parity: a drive-relative name
		// ("C:db") has the bare volume prefix as its parent
		// ("C:"), not the drive root; a bare volume is a prefix
		// without a file name and terminates the walk like Rust
		// (wave 19 round 19.11 astra finding).
		if runtime.GOOS == "windows" {
			if vol := filepath.VolumeName(trimmed); vol != "" {
				if vol == trimmed {
					return "", vol
				}
				return name, vol
			}
		}
		return name, string(os.PathSeparator)
	}
	if runtime.GOOS == "windows" {
		// Rust Path::parent parity for "C:\\name": the parent is the
		// drive ROOT ("C:\\"), which exists and terminates the
		// canonicalAbsolute walk; the bare volume ("C:") is a
		// drive-relative prefix that EvalSymlinks would re-anchor on
		// the per-drive working directory, falsely joining the missing
		// name onto the source's directory (wave 19 round 19.14 astra
		// finding).  Windows-only: verbatim/device volumes
		// ("\\\\?\\C:", "\\\\.\\C:") and UNC prefixes keep their
		// bare-prefix parent, matching Rust Path::parent.
		if vol := filepath.VolumeName(trimmed); separator == len(vol) &&
			len(vol) == 2 && vol[1] == ':' &&
			(vol[0] >= 'A' && vol[0] <= 'Z' || vol[0] >= 'a' && vol[0] <= 'z') {
			return name, vol + string(os.PathSeparator)
		}
	}
	return name, trimmed[:separator]
}
