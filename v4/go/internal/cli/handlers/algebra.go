// Query, join, algebra, and history-projection JSON-RPC handlers
// (iprange-jsonrpc-v1.md; Rust rpc/handlers/algebra.rs structural
// parity). Queries and joins stream their rows into one atomically
// published `result_budget`-bounded output file while a pinned SDK
// scope scans; algebra resolves one global same-name catalog over all
// sources. Every internally opened live reader is closed before the
// response; success results carry `source_close` (single-source
// families) or `source_closes` (multi-source families) exactly as the
// frozen result schemas in v4/cli/schema/results.py define.

package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ---------------------------------------------------------------------------
// Registration.
// ---------------------------------------------------------------------------

// RegisterAlgebra installs the query, join, algebra, and history
// projection handler families. The lead wires it into RegisterAll.
func RegisterAlgebra() {
	rpc.Register("iprange.v1.query.cardinalities", ValidateQueryCardinalitiesParams, QueryCardinalities)
	rpc.Register("iprange.v1.query.overlaps", ValidateQueryOverlapsParams, QueryOverlaps)
	rpc.Register("iprange.v1.query.matching_feeds", ValidateQueryMatchingFeedsParams, QueryMatchingFeeds)
	rpc.Register("iprange.v1.join.direct", ValidateJoinDirectParams, JoinDirect)
	rpc.Register("iprange.v1.join.membership", ValidateJoinMembershipParams, JoinMembership)
	rpc.Register("iprange.v1.algebra.count", ValidateAlgebraCountParams, AlgebraCount)
	rpc.Register("iprange.v1.algebra.compare", ValidateAlgebraCompareParams, AlgebraCompare)
	rpc.Register("iprange.v1.algebra.publish", ValidateAlgebraPublishParams, AlgebraPublish)
	rpc.Register("iprange.v1.history.project", ValidateHistoryProjectParams, HistoryProject)
}

// ---------------------------------------------------------------------------
// Strict params validators (each maps to the frozen methods.py schema).
// ---------------------------------------------------------------------------

// ValidateQueryCardinalitiesParams enforces the cardinalities schema.
func ValidateQueryCardinalitiesParams(params json.RawMessage) error {
	object, err := exactObject(params, "source", "selection", "membership_query_budget", "output")
	if err != nil {
		return err
	}
	if err := validateSource(object, "source"); err != nil {
		return err
	}
	if err := validateSelection(object["selection"]); err != nil {
		return err
	}
	if err := validateMembershipQueryBudget(object["membership_query_budget"]); err != nil {
		return err
	}
	return validateOutput(object["output"])
}

// ValidateQueryOverlapsParams enforces the overlaps schema.
func ValidateQueryOverlapsParams(params json.RawMessage) error {
	object, err := exactObject(params, "source", "selection", "membership_query_budget", "mode", "output")
	if err != nil {
		return err
	}
	if err := validateSource(object, "source"); err != nil {
		return err
	}
	if err := validateSelection(object["selection"]); err != nil {
		return err
	}
	if err := validateMembershipQueryBudget(object["membership_query_budget"]); err != nil {
		return err
	}
	if err := validateOverlapsMode(object["mode"]); err != nil {
		return err
	}
	return validateOutput(object["output"])
}

// ValidateQueryMatchingFeedsParams enforces the matching_feeds schema.
func ValidateQueryMatchingFeedsParams(params json.RawMessage) error {
	object, err := exactObject(params, "source", "addresses", "output")
	if err != nil {
		return err
	}
	if err := validateSource(object, "source"); err != nil {
		return err
	}
	if err := validateAddresses(object["addresses"]); err != nil {
		return err
	}
	return validateOutput(object["output"])
}

// ValidateJoinDirectParams enforces the direct-join schema.
func ValidateJoinDirectParams(params json.RawMessage) error {
	object, err := exactObject(params, "membership", "direct", "output", "max_result_cells")
	if err != nil {
		return err
	}
	if err := validateJoinSide(object["membership"]); err != nil {
		return err
	}
	if err := validateSource(object, "direct"); err != nil {
		return err
	}
	// Rust reader::u64_string: "0" is a valid cell bound, any other
	// canonical decimal parses within u64 (PARAM_U64).
	rawText, err := asString(object, "max_result_cells")
	if err != nil {
		return err
	}
	if _, err := canonicalDecimalString(rawText); err != nil {
		return err
	}
	return validateOutput(object["output"])
}

// ValidateJoinMembershipParams enforces the membership-join schema.
func ValidateJoinMembershipParams(params json.RawMessage) error {
	object, err := exactObject(params, "left", "right", "output")
	if err != nil {
		return err
	}
	if err := validateJoinSide(object["left"]); err != nil {
		return err
	}
	if err := validateJoinSide(object["right"]); err != nil {
		return err
	}
	return validateOutput(object["output"])
}

// ValidateAlgebraCountParams enforces the algebra.count schema.
func ValidateAlgebraCountParams(params json.RawMessage) error {
	object, err := exactObject(params, "sources", "selection", "algebra_budget")
	if err != nil {
		return err
	}
	if err := validateSources(object["sources"]); err != nil {
		return err
	}
	if err := validateSelection(object["selection"]); err != nil {
		return err
	}
	return validateAlgebraBudget(object["algebra_budget"], object["sources"])
}

// ValidateAlgebraCompareParams enforces the algebra.compare schema.
func ValidateAlgebraCompareParams(params json.RawMessage) error {
	object, err := exactObject(params, "sources", "left", "right", "algebra_budget")
	if err != nil {
		return err
	}
	if err := validateSources(object["sources"]); err != nil {
		return err
	}
	if err := validateSelection(object["left"]); err != nil {
		return err
	}
	if err := validateSelection(object["right"]); err != nil {
		return err
	}
	return validateAlgebraBudget(object["algebra_budget"], object["sources"])
}

// ValidateAlgebraPublishParams enforces the algebra.publish schema.
func ValidateAlgebraPublishParams(params json.RawMessage) error {
	object, err := exactObject(params, "sources", "operation", "output_mode", "value_tag",
		"metadata", "destination", "publication_policy", "algebra_budget", "algebra_output_budget")
	if err != nil {
		return err
	}
	if err := validateSources(object["sources"]); err != nil {
		return err
	}
	if err := validateAlgebraBudget(object["algebra_budget"], object["sources"]); err != nil {
		return err
	}
	if err := validateOperation(object["operation"]); err != nil {
		return err
	}
	if err := validateOutputMode(object["output_mode"]); err != nil {
		return err
	}
	if err := validateValueTag(object["value_tag"]); err != nil {
		return err
	}
	if err := validateMetadata(object["metadata"], false); err != nil {
		return err
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
		return err
	}
	if !validPublicationPolicyName(policy) {
		return fmt.Errorf("publication_policy is invalid")
	}
	return validateAlgebraOutputBudget(object["algebra_output_budget"])
}

// ValidateHistoryProjectParams enforces the history.project schema.
func ValidateHistoryProjectParams(params json.RawMessage) error {
	object, err := exactObject(params, "path", "last_seen", "windows", "metadata", "writer_budget")
	if err != nil {
		return err
	}
	path, err := asString(object, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	if err := validateSource(object, "last_seen"); err != nil {
		return err
	}
	if err := validateWindows(object["windows"]); err != nil {
		return err
	}
	if err := validateMetadata(object["metadata"], true); err != nil {
		return err
	}
	return validateWriterBudget(object["writer_budget"])
}

// validateSources enforces the algebra source list (methods.py
// _ALGEBRA_SOURCE): 1..budget entries, each with scope all.
func validateSources(raw json.RawMessage) error {
	var sources []json.RawMessage
	if err := json.Unmarshal(raw, &sources); err != nil {
		return fmt.Errorf("sources must be an array")
	}
	if len(sources) == 0 {
		return fmt.Errorf("sources must contain at least one entry")
	}
	for index, item := range sources {
		prefix := fmt.Sprintf("sources[%d]", index)
		object, err := decodeObject(item)
		if err != nil {
			return fmt.Errorf("%s must be an object", prefix)
		}
		if err := exactObjectRaw(object, "source", "scope", "membership_query_budget"); err != nil {
			return fmt.Errorf("%s: %v", prefix, err)
		}
		if err := validateSource(object, "source"); err != nil {
			return fmt.Errorf("%s: %v", prefix, err)
		}
		scope, err := memberObject(object, "scope")
		if err != nil {
			return fmt.Errorf("%s.scope must be an object", prefix)
		}
		if err := exactObjectRaw(scope, "mode"); err != nil {
			return fmt.Errorf("%s.scope: %v", prefix, err)
		}
		mode, err := asString(scope, "mode")
		if err != nil || mode != "all" {
			return fmt.Errorf("%s.scope.mode must be all", prefix)
		}
		if err := validateMembershipQueryBudget(object["membership_query_budget"]); err != nil {
			return fmt.Errorf("%s: %v", prefix, err)
		}
	}
	return nil
}

// validateAlgebraBudget enforces algebra_budget and the source-count
// cross check (1..max_sources).
func validateAlgebraBudget(raw, sourcesRaw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("algebra_budget must be an object")
	}
	if err := exactObjectRaw(object, "max_heap_bytes", "max_sources"); err != nil {
		return fmt.Errorf("algebra_budget: %v", err)
	}
	if _, err := parsePositiveU64Member(object, "max_heap_bytes"); err != nil {
		return fmt.Errorf("algebra_budget.max_heap_bytes: %v", err)
	}
	maximum, err := parsePositiveU32Member(object, "max_sources")
	if err != nil {
		return fmt.Errorf("algebra_budget.max_sources: %v", err)
	}
	var sources []json.RawMessage
	if err := json.Unmarshal(sourcesRaw, &sources); err != nil {
		return fmt.Errorf("sources must be an array")
	}
	if len(sources) < 1 || uint64(len(sources)) > uint64(maximum) {
		return fmt.Errorf("source count %d outside 1..%d", len(sources), maximum)
	}
	return nil
}

// validateAlgebraOutputBudget enforces algebra_output_budget.
func validateAlgebraOutputBudget(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("algebra_output_budget must be an object")
	}
	if err := exactObjectRaw(object, "max_output_pages", "max_open_files"); err != nil {
		return fmt.Errorf("algebra_output_budget: %v", err)
	}
	if _, err := parsePositiveU64Member(object, "max_output_pages"); err != nil {
		return fmt.Errorf("algebra_output_budget.max_output_pages: %v", err)
	}
	if _, err := parsePositiveU32Member(object, "max_open_files"); err != nil {
		return fmt.Errorf("algebra_output_budget.max_open_files: %v", err)
	}
	return nil
}

// validateMembershipQueryBudget enforces membership_query_budget.
func validateMembershipQueryBudget(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("membership_query_budget must be an object")
	}
	if err := exactObjectRaw(object, "max_heap_bytes"); err != nil {
		return fmt.Errorf("membership_query_budget: %v", err)
	}
	if _, err := parsePositiveU64Member(object, "max_heap_bytes"); err != nil {
		return fmt.Errorf("membership_query_budget.max_heap_bytes: %v", err)
	}
	return nil
}

// validateWriterBudget enforces writer_budget.
func validateWriterBudget(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("writer_budget must be an object")
	}
	if err := exactObjectRaw(object, "max_heap_bytes", "max_private_pages", "max_growth_pages", "max_open_files"); err != nil {
		return fmt.Errorf("writer_budget: %v", err)
	}
	for _, name := range []string{"max_heap_bytes", "max_private_pages", "max_growth_pages"} {
		if _, err := parsePositiveU64Member(object, name); err != nil {
			return fmt.Errorf("writer_budget.%s: %v", name, err)
		}
	}
	if _, err := parsePositiveU32Member(object, "max_open_files"); err != nil {
		return fmt.Errorf("writer_budget.max_open_files: %v", err)
	}
	return nil
}

// validateSource enforces one database source member (path + mode).
func validateSource(object rawObject, member string) error {
	source, err := memberObject(object, member)
	if err != nil {
		return fmt.Errorf("%s must be an object", member)
	}
	if err := exactObjectRaw(source, "path", "mode"); err != nil {
		return fmt.Errorf("%s: %v", member, err)
	}
	path, err := asString(source, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return err
	}
	if mode != "immutable" && mode != "live" {
		return fmt.Errorf("%s.mode must be immutable or live", member)
	}
	return nil
}

// validateSelection enforces one feed selection (all or named-unique).
func validateSelection(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("selection must be an object")
	}
	mode, err := asString(object, "mode")
	if err != nil {
		return fmt.Errorf("selection.mode must be all or named")
	}
	switch mode {
	case "all":
		return exactObjectRaw(object, "mode")
	case "named":
		if err := exactObjectRaw(object, "mode", "feeds"); err != nil {
			return err
		}
		feeds, err := asStringArray(object, "feeds")
		if err != nil {
			return fmt.Errorf("selection.feeds must be an array of strings")
		}
		if len(feeds) == 0 {
			return fmt.Errorf("selection.feeds must contain at least one feed")
		}
		seen := make(map[string]bool, len(feeds))
		for _, feed := range feeds {
			if err := validateFeedName(feed); err != nil {
				return err
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

// validateFeedName enforces the exact v4 FeedName grammar (1..255
// lowercase ASCII bytes; interior bytes additionally _, -, .).
func validateFeedName(feed string) error {
	if !feedNameValid(feed) {
		return fmt.Errorf("feed does not use the v4 FeedName grammar")
	}
	return nil
}

func isFeedEdge(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// validateOutput enforces the tabular output descriptor.
func validateOutput(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("output must be an object")
	}
	if err := exactObjectRaw(object, "path", "format", "publication_policy", "result_budget"); err != nil {
		return fmt.Errorf("output: %v", err)
	}
	path, err := asString(object, "path")
	if err != nil {
		return err
	}
	if err := validatePath(path); err != nil {
		return err
	}
	format, err := asString(object, "format")
	if err != nil {
		return err
	}
	if format != "jsonl" && format != "csv" {
		return fmt.Errorf("output.format must be jsonl or csv")
	}
	policy, err := asString(object, "publication_policy")
	if err != nil {
		return err
	}
	if !validPublicationPolicyName(policy) {
		return fmt.Errorf("output.publication_policy is invalid")
	}
	budget, err := memberObject(object, "result_budget")
	if err != nil {
		return fmt.Errorf("output.result_budget must be an object")
	}
	if err := exactObjectRaw(budget, "max_rows", "max_output_bytes", "max_open_files"); err != nil {
		return fmt.Errorf("output.result_budget: %v", err)
	}
	if _, err := parsePositiveU64Member(budget, "max_rows"); err != nil {
		return fmt.Errorf("result_budget.max_rows: %v", err)
	}
	if _, err := parsePositiveU64Member(budget, "max_output_bytes"); err != nil {
		return fmt.Errorf("result_budget.max_output_bytes: %v", err)
	}
	if _, err := parsePositiveU32Member(budget, "max_open_files"); err != nil {
		return fmt.Errorf("result_budget.max_open_files: %v", err)
	}
	return nil
}

// validateAddresses enforces the 1..4096 canonical-address array.
func validateAddresses(raw json.RawMessage) error {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("addresses must be an array of strings")
	}
	if len(values) == 0 || len(values) > 4096 {
		return fmt.Errorf("addresses must contain 1 through 4096 values")
	}
	for _, address := range values {
		parsed, err := netip.ParseAddr(address)
		if err != nil || parsed.String() != address {
			return fmt.Errorf("address is not canonical IP text: %s", address)
		}
	}
	return nil
}

// validateJoinSide enforces one join side (source + selection + budget).
func validateJoinSide(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("join side must be an object")
	}
	if err := exactObjectRaw(object, "source", "selection", "membership_query_budget"); err != nil {
		return err
	}
	if err := validateSource(object, "source"); err != nil {
		return err
	}
	if err := validateSelection(object["selection"]); err != nil {
		return err
	}
	return validateMembershipQueryBudget(object["membership_query_budget"])
}

// validateOverlapsMode enforces the overlaps mode discriminator.
func validateOverlapsMode(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("mode must be an object")
	}
	kind, err := asString(object, "kind")
	if err != nil {
		return fmt.Errorf("mode.kind must be all_pairs, target, or selected_pairs")
	}
	switch kind {
	case "all_pairs":
		return exactObjectRaw(object, "kind")
	case "target":
		if err := exactObjectRaw(object, "kind", "target_feed"); err != nil {
			return err
		}
		feed, err := asString(object, "target_feed")
		if err != nil {
			return err
		}
		return validateFeedName(feed)
	case "selected_pairs":
		if err := exactObjectRaw(object, "kind", "pairs"); err != nil {
			return err
		}
		pairs, err := asObjectArray(object, "pairs")
		if err != nil {
			return fmt.Errorf("mode.pairs must be an array of objects")
		}
		if len(pairs) == 0 {
			return fmt.Errorf("mode.pairs must contain at least one pair")
		}
		normalized := make(map[string]bool, len(pairs))
		for _, pair := range pairs {
			if err := exactObjectRaw(pair, "left", "right"); err != nil {
				return fmt.Errorf("mode.pairs: %v", err)
			}
			left, err := asString(pair, "left")
			if err != nil {
				return err
			}
			right, err := asString(pair, "right")
			if err != nil {
				return err
			}
			if err := validateFeedName(left); err != nil {
				return err
			}
			if err := validateFeedName(right); err != nil {
				return err
			}
			if left == right {
				return fmt.Errorf("pair left and right feeds must differ")
			}
			var key string
			if left < right {
				key = left + "\x00" + right
			} else {
				key = right + "\x00" + left
			}
			if normalized[key] {
				return fmt.Errorf("unordered pairs must be unique")
			}
			normalized[key] = true
		}
		return nil
	}
	return fmt.Errorf("mode.kind must be all_pairs, target, or selected_pairs")
}

// validateOperation enforces the algebra set operation.
func validateOperation(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("operation must be an object")
	}
	kind, err := asString(object, "kind")
	if err != nil {
		return fmt.Errorf("operation.kind must be union, intersection, or exclusion")
	}
	switch kind {
	case "union", "intersection":
		if err := exactObjectRaw(object, "kind", "selection"); err != nil {
			return err
		}
		return validateSelection(object["selection"])
	case "exclusion":
		if err := exactObjectRaw(object, "kind", "included", "excluded"); err != nil {
			return err
		}
		if err := validateSelection(object["included"]); err != nil {
			return err
		}
		return validateSelection(object["excluded"])
	}
	return fmt.Errorf("operation.kind must be union, intersection, or exclusion")
}

// validateOutputMode enforces the algebra output mode.
func validateOutputMode(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("output_mode must be an object")
	}
	kind, err := asString(object, "kind")
	if err != nil {
		return fmt.Errorf("output_mode.kind must be preserve_feeds or flat")
	}
	switch kind {
	case "preserve_feeds":
		return exactObjectRaw(object, "kind")
	case "flat":
		if err := exactObjectRaw(object, "kind", "feed"); err != nil {
			return err
		}
		feed, err := asString(object, "feed")
		if err != nil {
			return err
		}
		return validateFeedName(feed)
	}
	return fmt.Errorf("output_mode.kind must be preserve_feeds or flat")
}

// validateWindows enforces the 1..4096 unique window list.
func validateWindows(raw json.RawMessage) error {
	var windows []json.RawMessage
	if err := json.Unmarshal(raw, &windows); err != nil {
		return fmt.Errorf("windows must be an array")
	}
	if len(windows) == 0 || len(windows) > 4096 {
		return fmt.Errorf("windows must contain 1 through 4096 values")
	}
	seen := make(map[string]bool, len(windows))
	for index, item := range windows {
		window, err := decodeObject(item)
		if err != nil {
			return fmt.Errorf("windows[%d] must be an object", index)
		}
		if err := exactObjectRaw(window, "feed", "cutoff"); err != nil {
			return fmt.Errorf("windows[%d]: %v", index, err)
		}
		feed, err := asString(window, "feed")
		if err != nil {
			return err
		}
		if err := validateFeedName(feed); err != nil {
			return err
		}
		if seen[feed] {
			return fmt.Errorf("window feed names must be unique")
		}
		seen[feed] = true
		if _, err := asUint32(window, "cutoff"); err != nil {
			return fmt.Errorf("windows[%d].cutoff must be u32", index)
		}
	}
	return nil
}

// validateValueTag enforces one value-tag input ({text} or {hex}).
func validateValueTag(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("value_tag must be an object")
	}
	if _, err := asString(object, "text"); err == nil {
		if err := exactObjectRaw(object, "text"); err != nil {
			return err
		}
		text, _ := asString(object, "text")
		if len([]byte(text)) > 15 {
			return fmt.Errorf("value-tag text must be at most 15 bytes")
		}
		if strings.IndexByte(text, 0) >= 0 {
			return fmt.Errorf("value-tag text must not encode a NUL byte")
		}
		return nil
	}
	hexText, err := asString(object, "hex")
	if err != nil {
		return fmt.Errorf("value_tag must be exactly one of text or hex")
	}
	if err := exactObjectRaw(object, "hex"); err != nil {
		return err
	}
	return validateTagHex(hexText)
}

// validateTagHex enforces the even lowercase hex form with no NUL byte.
func validateTagHex(text string) error {
	if len(text) > 30 || len(text)%2 != 0 {
		return fmt.Errorf("value-tag hex must be an even number of lowercase digits")
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("value-tag hex must be an even number of lowercase digits")
		}
	}
	decoded, err := hex.DecodeString(text)
	if err != nil {
		return fmt.Errorf("value-tag hex must be an even number of lowercase digits")
	}
	for _, b := range decoded {
		if b == 0 {
			return fmt.Errorf("value-tag hex must not encode a NUL byte")
		}
	}
	return nil
}

// validateMetadata enforces one metadata input; keep is refused when
// allowKeep is false (new immutable destinations, methods.py
// METADATA_REPLACEMENT_INPUT).
func validateMetadata(raw json.RawMessage, allowKeep bool) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("metadata must be an object")
	}
	mode, err := asString(object, "mode")
	if err != nil {
		return fmt.Errorf("metadata.mode is invalid")
	}
	switch mode {
	case "keep":
		if !allowKeep {
			return fmt.Errorf("metadata.mode keep is invalid for a new immutable destination")
		}
		return exactObjectRaw(object, "mode")
	case "clear":
		return exactObjectRaw(object, "mode")
	case "replace_utf8":
		if err := exactObjectRaw(object, "mode", "text"); err != nil {
			return err
		}
		if _, err := asString(object, "text"); err != nil {
			return fmt.Errorf("metadata.text must be a string")
		}
		return nil
	case "replace_base64":
		if err := exactObjectRaw(object, "mode", "base64"); err != nil {
			return err
		}
		text, err := asString(object, "base64")
		if err != nil {
			return fmt.Errorf("metadata.base64 must be a string")
		}
		if _, err := base64Decode(text); err != nil {
			return fmt.Errorf("metadata.base64 must be canonical padded base64")
		}
		return nil
	case "replace_file":
		if err := exactObjectRaw(object, "mode", "path"); err != nil {
			return err
		}
		path, err := asString(object, "path")
		if err != nil {
			return fmt.Errorf("metadata.path must be a string")
		}
		return validatePath(path)
	}
	return fmt.Errorf("metadata.mode is invalid")
}

// ---------------------------------------------------------------------------
// Strict numeric decoders (Rust reader.rs u64_string /
// positive_u64_string / positive_u32 parity).
// ---------------------------------------------------------------------------

// canonicalU64 parses one canonical unsigned decimal string ("0" or a
// non-zero-leading digit string within u64).
func canonicalDecimalString(value string) (uint64, error) {
	return canonicalU64String(value)
}

// parsePositiveU64Member reads a strict decimal-string member and
// requires the canonical positive u64 form.
func parsePositiveU64Member(object rawObject, name string) (uint64, error) {
	return asPositiveU64String(object, name)
}

// parsePositiveU32Member reads a strict integral JSON member in
// 1..=2^32-1 (Rust positive_u32).
func parsePositiveU32Member(object rawObject, name string) (uint32, error) {
	return asPositiveU32(object, name)
}

// ---------------------------------------------------------------------------
// Handler-side decoders (wire shapes were strictly validated).
// ---------------------------------------------------------------------------

// decodedSelection is one feed selection ready for the SDK.
type decodedSelection struct {
	all   bool
	names []string
}

func decodeSelection(raw json.RawMessage) (*decodedSelection, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return nil, rpc.InvalidParamsError("selection must be an object")
	}
	mode, err := asString(object, "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("selection.mode must be all or named")
	}
	switch mode {
	case "all":
		return &decodedSelection{all: true}, nil
	default:
		feeds, err := asStringArray(object, "feeds")
		if err != nil {
			return nil, rpc.InvalidParamsError("selection.feeds must be an array of strings")
		}
		return &decodedSelection{names: feeds}, nil
	}
}

// decodeOverlapMode converts the wire mode into the SDK aggregation
// mode (Rust decode_overlap_mode).
func decodeOverlapMode(raw json.RawMessage) (iprangedb.MembershipAggregationMode, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return iprangedb.MembershipAggregationMode{}, rpc.InvalidParamsError("mode must be an object")
	}
	kind, err := asString(object, "kind")
	if err != nil {
		return iprangedb.MembershipAggregationMode{}, rpc.InvalidParamsError("mode.kind is invalid")
	}
	switch kind {
	case "all_pairs":
		return iprangedb.MembershipAggregationAllPairs(), nil
	case "target":
		feed, err := asString(object, "target_feed")
		if err != nil {
			return iprangedb.MembershipAggregationMode{}, rpc.InvalidParamsError("target_feed is invalid")
		}
		return iprangedb.MembershipAggregationTargetAgainstScope(feed), nil
	case "selected_pairs":
		pairs, err := asObjectArray(object, "pairs")
		if err != nil {
			return iprangedb.MembershipAggregationMode{}, rpc.InvalidParamsError("mode.pairs is invalid")
		}
		decoded := make([]iprangedb.FeedPair, 0, len(pairs))
		for _, pair := range pairs {
			left, err := asString(pair, "left")
			if err != nil {
				return iprangedb.MembershipAggregationMode{}, rpc.InvalidParamsError("pair left is invalid")
			}
			right, err := asString(pair, "right")
			if err != nil {
				return iprangedb.MembershipAggregationMode{}, rpc.InvalidParamsError("pair right is invalid")
			}
			decoded = append(decoded, iprangedb.FeedPair{Left: left, Right: right})
		}
		return iprangedb.MembershipAggregationSelectedPairs(decoded), nil
	}
	return iprangedb.MembershipAggregationMode{}, rpc.InvalidParamsError("mode.kind is invalid")
}

// decodeOperation converts the wire operation into the SDK set
// operation (Rust decode_operation).
func decodeOperation(raw json.RawMessage) (iprangedb.AlgebraSetOperation, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return iprangedb.AlgebraSetOperation{}, rpc.InvalidParamsError("operation must be an object")
	}
	kind, err := asString(object, "kind")
	if err != nil {
		return iprangedb.AlgebraSetOperation{}, rpc.InvalidParamsError("operation.kind is invalid")
	}
	switch kind {
	case "union":
		selection, herr := decodeSelection(object["selection"])
		if herr != nil {
			return iprangedb.AlgebraSetOperation{}, herr
		}
		return iprangedb.AlgebraSetUnion(algebraFeedSelection(selection)), nil
	case "intersection":
		selection, herr := decodeSelection(object["selection"])
		if herr != nil {
			return iprangedb.AlgebraSetOperation{}, herr
		}
		return iprangedb.AlgebraSetIntersection(algebraFeedSelection(selection)), nil
	case "exclusion":
		included, herr := decodeSelection(object["included"])
		if herr != nil {
			return iprangedb.AlgebraSetOperation{}, herr
		}
		excluded, herr := decodeSelection(object["excluded"])
		if herr != nil {
			return iprangedb.AlgebraSetOperation{}, herr
		}
		return iprangedb.AlgebraSetExclusion(algebraFeedSelection(included), algebraFeedSelection(excluded)), nil
	}
	return iprangedb.AlgebraSetOperation{}, rpc.InvalidParamsError("operation.kind is invalid")
}

// algebraFeedSelection projects one wire selection onto the algebra
// feed selection surface.
func algebraFeedSelection(selection *decodedSelection) iprangedb.FeedSelection {
	if selection.all {
		return iprangedb.AlgebraFeedSelectionAll()
	}
	return iprangedb.AlgebraFeedSelectionNamed(selection.names)
}

// decodeOutputMode converts the wire output mode into the SDK mode
// (Rust decode_output_mode).
func decodeOutputMode(raw json.RawMessage) (iprangedb.AlgebraOutputMode, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return iprangedb.AlgebraOutputMode{}, rpc.InvalidParamsError("output_mode must be an object")
	}
	kind, err := asString(object, "kind")
	if err != nil {
		return iprangedb.AlgebraOutputMode{}, rpc.InvalidParamsError("output_mode.kind is invalid")
	}
	switch kind {
	case "preserve_feeds":
		return iprangedb.AlgebraOutputModePreserveFeeds(), nil
	case "flat":
		feed, err := asString(object, "feed")
		if err != nil {
			return iprangedb.AlgebraOutputMode{}, rpc.InvalidParamsError("output_mode feed is invalid")
		}
		mode, err := iprangedb.AlgebraOutputModeFlat(feed)
		if err != nil {
			return iprangedb.AlgebraOutputMode{}, rpc.InvalidParamsError("output_mode feed is invalid")
		}
		return mode, nil
	}
	return iprangedb.AlgebraOutputMode{}, rpc.InvalidParamsError("output_mode.kind is invalid")
}

// outputSpec is the decoded tabular output descriptor.
type outputSpec struct {
	path   string
	jsonl  bool
	policy iprangedb.PublicationPolicy
	budget fileio.ExportBudget
}

// decodeOutput converts the output descriptor into writer inputs.
func decodeOutput(raw json.RawMessage) (*outputSpec, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return nil, rpc.InvalidParamsError("output must be an object")
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("output.path must be a string")
	}
	format, err := asString(object, "format")
	if err != nil {
		return nil, rpc.InvalidParamsError("output.format must be jsonl or csv")
	}
	policyName, err := asString(object, "publication_policy")
	if err != nil {
		return nil, rpc.InvalidParamsError("output.publication_policy is invalid")
	}
	budgetObject, err := memberObject(object, "result_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError("output.result_budget must be an object")
	}
	maxRows, herr := decodePositiveU64(budgetObject, "max_rows")
	if herr != nil {
		return nil, herr
	}
	maxBytes, herr := decodePositiveU64(budgetObject, "max_output_bytes")
	if herr != nil {
		return nil, herr
	}
	maxFiles, herr := decodePositiveU32(budgetObject, "max_open_files")
	if herr != nil {
		return nil, herr
	}
	return &outputSpec{
		path:   path,
		jsonl:  format == "jsonl",
		policy: policyByName(policyName),
		budget: fileio.ExportBudget{
			MaxRows:        maxRows,
			MaxOutputBytes: maxBytes,
			MaxOpenFiles:   maxFiles,
		},
	}, nil
}

// decodeMembershipBudget converts one membership_query_budget.
func decodeMembershipBudget(raw json.RawMessage) (iprangedb.MembershipQueryBudget, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return iprangedb.MembershipQueryBudget{}, rpc.InvalidParamsError("membership_query_budget must be an object")
	}
	heap, herr := decodePositiveU64(object, "max_heap_bytes")
	if herr != nil {
		return iprangedb.MembershipQueryBudget{}, herr
	}
	return iprangedb.MembershipQueryBudget{MaxHeapBytes: heap}, nil
}

// decodeAlgebraBudget converts one algebra_budget.
func decodeAlgebraBudget(raw json.RawMessage) (iprangedb.MembershipAlgebraBudget, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return iprangedb.MembershipAlgebraBudget{}, rpc.InvalidParamsError("algebra_budget must be an object")
	}
	heap, herr := decodePositiveU64(object, "max_heap_bytes")
	if herr != nil {
		return iprangedb.MembershipAlgebraBudget{}, herr
	}
	maxSources, herr := decodePositiveU32(object, "max_sources")
	if herr != nil {
		return iprangedb.MembershipAlgebraBudget{}, herr
	}
	return iprangedb.MembershipAlgebraBudget{MaxHeapBytes: heap, MaxSources: maxSources}, nil
}

// decodeAlgebraOutputBudget converts one algebra_output_budget.
func decodeAlgebraOutputBudget(raw json.RawMessage) (iprangedb.AlgebraOutputBudget, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return iprangedb.AlgebraOutputBudget{}, rpc.InvalidParamsError("algebra_output_budget must be an object")
	}
	pages, herr := decodePositiveU64(object, "max_output_pages")
	if herr != nil {
		return iprangedb.AlgebraOutputBudget{}, herr
	}
	files, herr := decodePositiveU32(object, "max_open_files")
	if herr != nil {
		return iprangedb.AlgebraOutputBudget{}, herr
	}
	return iprangedb.AlgebraOutputBudget{MaxOutputPages: pages, MaxOpenFiles: files}, nil
}

// decodeWriterBudget converts one writer_budget into the SDK page
// budget.
func decodeWriterBudgetRaw(raw json.RawMessage) (iprangedb.PageBudget, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return iprangedb.PageBudget{}, rpc.InvalidParamsError("writer_budget must be an object")
	}
	var budget iprangedb.PageBudget
	for _, name := range []string{"max_heap_bytes", "max_private_pages", "max_growth_pages"} {
		value, herr := decodePositiveU64(object, name)
		if herr != nil {
			return iprangedb.PageBudget{}, herr
		}
		switch name {
		case "max_heap_bytes":
			budget.MaxHeapBytes = value
		case "max_private_pages":
			budget.MaxPrivatePages = value
		case "max_growth_pages":
			budget.MaxGrowthPages = value
		}
	}
	files, herr := decodePositiveU32(object, "max_open_files")
	if herr != nil {
		return iprangedb.PageBudget{}, herr
	}
	budget.MaxOpenFiles = files
	return budget, nil
}

// decodeWindows converts the wire window list into SDK windows.
func decodeWindows(raw json.RawMessage) ([]iprangedb.HistoryWindow, *rpc.HandlerError) {
	var windows []json.RawMessage
	if err := json.Unmarshal(raw, &windows); err != nil {
		return nil, rpc.InvalidParamsError("windows must be an array")
	}
	decoded := make([]iprangedb.HistoryWindow, 0, len(windows))
	for _, item := range windows {
		window, err := decodeObject(item)
		if err != nil {
			return nil, rpc.InvalidParamsError("each window must be an object")
		}
		feed, err := asString(window, "feed")
		if err != nil {
			return nil, rpc.InvalidParamsError("window feed is invalid")
		}
		cutoff, err := asUint32(window, "cutoff")
		if err != nil {
			return nil, rpc.InvalidParamsError("window cutoff must be u32")
		}
		decoded = append(decoded, iprangedb.HistoryWindow{FeedName: feed, Cutoff: cutoff})
	}
	return decoded, nil
}

// decodeValueTag converts the wire value-tag input into the SDK tag.
func decodeValueTag(object rawObject, name string) (iprangedb.ValueTag, *rpc.HandlerError) {
	raw, ok := object[name]
	if !ok {
		return iprangedb.ValueTag{}, rpc.InvalidParamsError(name + " must be a value tag")
	}
	tagObject, err := decodeObject(raw)
	if err != nil {
		return iprangedb.ValueTag{}, rpc.InvalidParamsError(name + " must be a value tag")
	}
	tag, err := valueTagFromWire(tagObject, name)
	if err != nil {
		return iprangedb.ValueTag{}, rpc.InvalidParamsError(err.Error())
	}
	return tag, nil
}

// sourcePartsResult is one decoded database source.
type sourcePartsResult struct {
	path string
	mode string
}

// sourceParts decodes one `member` database source of a params object.
func sourceParts(object rawObject, member string) (sourcePartsResult, *rpc.HandlerError) {
	source, err := memberObject(object, member)
	if err != nil {
		return sourcePartsResult{}, rpc.InvalidParamsError(member + " must be an object")
	}
	path, err := asString(source, "path")
	if err != nil {
		return sourcePartsResult{}, rpc.InvalidParamsError(member + ".path must be a string")
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return sourcePartsResult{}, rpc.InvalidParamsError(member + ".mode must be a string")
	}
	return sourcePartsResult{path: path, mode: mode}, nil
}

// decodePositiveU64 reads one strict positive decimal-string member.
func decodePositiveU64(object rawObject, name string) (uint64, *rpc.HandlerError) {
	value, err := parsePositiveU64Member(object, name)
	if err != nil {
		return 0, rpc.InvalidParamsError(err.Error())
	}
	return value, nil
}

// decodePositiveU32 reads one strict positive u32 member.
func decodePositiveU32(object rawObject, name string) (uint32, *rpc.HandlerError) {
	value, err := parsePositiveU32Member(object, name)
	if err != nil {
		return 0, rpc.InvalidParamsError(err.Error())
	}
	return value, nil
}

// ---------------------------------------------------------------------------
// Wire row encoding (Rust io/export_writer.rs push_json_string /
// write_csv_field parity): byte-identical JSON string escaping and
// RFC-4180-style CSV quoting.
// ---------------------------------------------------------------------------

// jsonQuote appends one JSON string literal with serde_json-identical
// escaping (quotes and backslashes double, control bytes escape to
// \b \f \n \r \t or \u00xx, everything else passes through as exact
// UTF-8).
func jsonQuote(output *strings.Builder, value string) {
	output.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			output.WriteString(`\"`)
		case '\\':
			output.WriteString(`\\`)
		case '\b':
			output.WriteString(`\b`)
		case '\f':
			output.WriteString(`\f`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(output, `\u%04x`, r)
			} else {
				output.WriteRune(r)
			}
		}
	}
	output.WriteByte('"')
}

// csvField appends one CSV field, quoting when the value carries a
// comma, double quote, CR, or LF (Rust write_csv_field).
func csvField(output *strings.Builder, value string) {
	needsQuotes := false
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case ',', '"', '\r', '\n':
			needsQuotes = true
		}
	}
	if !needsQuotes {
		output.WriteString(value)
		return
	}
	output.WriteByte('"')
	for _, r := range value {
		if r == '"' {
			output.WriteByte('"')
		}
		output.WriteRune(r)
	}
	output.WriteByte('"')
}

// sinkFail records the first writer failure into the capture slot
// (once) and stops the SDK scan with the canonical stopped-by-sink
// error (Rust fail()).
func sinkFail(slot **rpc.HandlerError, failure *rpc.HandlerError) error {
	if *slot == nil {
		*slot = failure
	}
	return &iprangedb.Error{Code: iprangedb.ErrorStoppedBySink, Detail: "sink stopped the scan"}
}

// capturedFailure returns the captured writer failure, if any.
func capturedFailure(slot **rpc.HandlerError) *rpc.HandlerError {
	if slot == nil || *slot == nil {
		return nil
	}
	return *slot
}

// ---------------------------------------------------------------------------
// Sinks: bounded stream consumers over one atomic export writer. A
// writer failure is captured once and the SDK scan stops with
// StoppedBySink (Rust sinks).
// ---------------------------------------------------------------------------

// cardinalitySink writes FeedCardinality batches as feed,addresses
// rows.
type cardinalitySink struct {
	writer   *fileio.ExportWriter
	jsonl    bool
	captured **rpc.HandlerError
	line     strings.Builder
}

func (s *cardinalitySink) feedBatch(batch []iprangedb.FeedCardinality) error {
	for _, cell := range batch {
		s.line.Reset()
		if s.jsonl {
			s.line.WriteString(`{"feed":`)
			jsonQuote(&s.line, cell.Feed)
			s.line.WriteString(`,"addresses":"`)
			s.line.WriteString(cell.Addresses.String())
			s.line.WriteString(`"}`)
		} else {
			csvField(&s.line, cell.Feed)
			s.line.WriteByte(',')
			s.line.WriteString(cell.Addresses.String())
		}
		if herr := s.writer.WriteLine([]byte(s.line.String()), fileio.C129(cell.Addresses)); herr != nil {
			return sinkFail(s.captured, herr)
		}
	}
	return nil
}

// overlapBatch discards; cardinalities-only scans deliver no overlaps.
func (s *cardinalitySink) overlapBatch([]iprangedb.FeedOverlap) error { return nil }

// overlapSink writes FeedOverlap batches as left,right,addresses rows.
type overlapSink struct {
	writer   *fileio.ExportWriter
	jsonl    bool
	captured **rpc.HandlerError
	line     strings.Builder
}

func (s *overlapSink) feedBatch([]iprangedb.FeedCardinality) error { return nil }

func (s *overlapSink) overlapBatch(batch []iprangedb.FeedOverlap) error {
	for _, cell := range batch {
		s.line.Reset()
		if s.jsonl {
			s.line.WriteString(`{"left":`)
			jsonQuote(&s.line, cell.Left)
			s.line.WriteString(`,"right":`)
			jsonQuote(&s.line, cell.Right)
			s.line.WriteString(`,"addresses":"`)
			s.line.WriteString(cell.Addresses.String())
			s.line.WriteString(`"}`)
		} else {
			csvField(&s.line, cell.Left)
			s.line.WriteByte(',')
			csvField(&s.line, cell.Right)
			s.line.WriteByte(',')
			s.line.WriteString(cell.Addresses.String())
		}
		if herr := s.writer.WriteLine([]byte(s.line.String()), fileio.C129(cell.Addresses)); herr != nil {
			return sinkFail(s.captured, herr)
		}
	}
	return nil
}

// directJoinSink writes DirectJoinCell batches as
// feed,direct_value,addresses rows; an uncovered cell serializes as
// the literal null (CSV has no null vocabulary, Rust parity).
type directJoinSink struct {
	writer   *fileio.ExportWriter
	jsonl    bool
	captured **rpc.HandlerError
	line     strings.Builder
}

func (s *directJoinSink) cellBatch(batch []iprangedb.DirectJoinCell) error {
	for _, cell := range batch {
		s.line.Reset()
		if s.jsonl {
			s.line.WriteString(`{"feed":`)
			jsonQuote(&s.line, cell.Feed)
			s.line.WriteString(`,"direct_value":`)
		} else {
			csvField(&s.line, cell.Feed)
			s.line.WriteByte(',')
		}
		if cell.DirectValue != nil {
			fmt.Fprintf(&s.line, "%d", *cell.DirectValue)
		} else {
			s.line.WriteString("null")
		}
		if s.jsonl {
			s.line.WriteString(`,"addresses":"`)
			s.line.WriteString(cell.Addresses.String())
			s.line.WriteString(`"}`)
		} else {
			s.line.WriteByte(',')
			s.line.WriteString(cell.Addresses.String())
		}
		if herr := s.writer.WriteLine([]byte(s.line.String()), fileio.C129(cell.Addresses)); herr != nil {
			return sinkFail(s.captured, herr)
		}
	}
	return nil
}

// membershipJoinSink writes cross and uncovered batches into the
// flattened six-column CSV or the two JSONL shapes
// (kind:cross|uncovered).
type membershipJoinSink struct {
	writer   *fileio.ExportWriter
	jsonl    bool
	captured **rpc.HandlerError
	line     strings.Builder
}

func (s *membershipJoinSink) crossBatch(batch []iprangedb.MembershipCrossCell) error {
	for _, cell := range batch {
		s.line.Reset()
		if s.jsonl {
			s.line.WriteString(`{"kind":"cross","left":`)
			jsonQuote(&s.line, cell.Left)
			s.line.WriteString(`,"right":`)
			jsonQuote(&s.line, cell.Right)
			s.line.WriteString(`,"addresses":"`)
			s.line.WriteString(cell.Addresses.String())
			s.line.WriteString(`"}`)
		} else {
			// Rust parity: cross rows occupy columns
			// kind,left,(right),(side),feed,addresses with the right
			// feed in the feed column.
			s.line.WriteString("cross,")
			csvField(&s.line, cell.Left)
			s.line.WriteString(",,,")
			csvField(&s.line, cell.Right)
			s.line.WriteByte(',')
			s.line.WriteString(cell.Addresses.String())
		}
		if herr := s.writer.WriteLine([]byte(s.line.String()), fileio.C129(cell.Addresses)); herr != nil {
			return sinkFail(s.captured, herr)
		}
	}
	return nil
}

func (s *membershipJoinSink) uncoveredBatch(batch []iprangedb.UncoveredFeed) error {
	for _, cell := range batch {
		side := "left"
		if cell.Side == iprangedb.UncoveredRight {
			side = "right"
		}
		s.line.Reset()
		if s.jsonl {
			s.line.WriteString(`{"kind":"uncovered","side":"`)
			s.line.WriteString(side)
			s.line.WriteString(`","feed":`)
			jsonQuote(&s.line, cell.Feed)
			s.line.WriteString(`,"addresses":"`)
			s.line.WriteString(cell.Addresses.String())
			s.line.WriteString(`"}`)
		} else {
			s.line.WriteString("uncovered,,,")
			s.line.WriteString(side)
			s.line.WriteByte(',')
			csvField(&s.line, cell.Feed)
			s.line.WriteByte(',')
			s.line.WriteString(cell.Addresses.String())
		}
		if herr := s.writer.WriteLine([]byte(s.line.String()), fileio.C129(cell.Addresses)); herr != nil {
			return sinkFail(s.captured, herr)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mechanical report conversions (results.py shapes; every count is an
// exact decimal string).
// ---------------------------------------------------------------------------

// outputFacts converts the export facts into the OUTPUT_FACTS object.
func outputFacts(facts *fileio.ExportFacts) map[string]any {
	return map[string]any{
		"path":   facts.Path,
		"sha256": facts.SHA256,
		"bytes":  DecimalUint(facts.Bytes),
		"rows":   DecimalUint(facts.Rows),
	}
}

// aggregationReportJSON converts MembershipAggregationReport.
func aggregationReportJSON(report iprangedb.MembershipAggregationReport) map[string]any {
	return map[string]any{
		"scanned_range_count": DecimalUint(report.ScannedRangeCount),
		"scanned_addresses":   report.ScannedAddresses.String(),
		"feed_result_count":   DecimalUint(report.FeedResultCount),
		"pair_result_count":   DecimalUint(report.PairResultCount),
	}
}

// directJoinReportJSON converts DirectJoinReport.
func directJoinReportJSON(report iprangedb.DirectJoinReport) map[string]any {
	return map[string]any{
		"membership_range_count": DecimalUint(report.MembershipRangeCount),
		"direct_ranges_visited":  DecimalUint(report.DirectRangesVisited),
		"joined_segment_count":   DecimalUint(report.JoinedSegmentCount),
		"selected_addresses":     report.SelectedAddresses.String(),
		"mapped_addresses":       report.MappedAddresses.String(),
		"unmapped_addresses":     report.UnmappedAddresses.String(),
		"result_cell_count":      DecimalUint(report.ResultCellCount),
	}
}

// membershipJoinReportJSON converts MembershipJoinReport.
func membershipJoinReportJSON(report iprangedb.MembershipJoinReport) map[string]any {
	return map[string]any{
		"left_range_count":          DecimalUint(report.LeftRangeCount),
		"right_range_count":         DecimalUint(report.RightRangeCount),
		"joined_segment_count":      DecimalUint(report.JoinedSegmentCount),
		"left_addresses":            report.LeftAddresses.String(),
		"right_addresses":           report.RightAddresses.String(),
		"overlap_addresses":         report.OverlapAddresses.String(),
		"left_uncovered_addresses":  report.LeftUncoveredAddresses.String(),
		"right_uncovered_addresses": report.RightUncoveredAddresses.String(),
		"cross_result_count":        DecimalUint(report.CrossResultCount),
		"uncovered_result_count":    DecimalUint(report.UncoveredResultCount),
	}
}

// countReportJSON converts AlgebraCountReport.
func countReportJSON(report iprangedb.AlgebraCountReport) map[string]any {
	return map[string]any{
		"source_count":         DecimalUint(report.SourceCount),
		"source_range_count":   DecimalUint(report.SourceRangeCount),
		"joined_segment_count": DecimalUint(report.JoinedSegmentCount),
		"addresses":            report.Addresses.String(),
	}
}

// comparisonReportJSON converts AlgebraComparisonReport.
func comparisonReportJSON(report iprangedb.AlgebraComparisonReport) map[string]any {
	return map[string]any{
		"source_count":         DecimalUint(report.SourceCount),
		"source_range_count":   DecimalUint(report.SourceRangeCount),
		"joined_segment_count": DecimalUint(report.JoinedSegmentCount),
		"left_addresses":       report.LeftAddresses.String(),
		"right_addresses":      report.RightAddresses.String(),
		"overlap_addresses":    report.OverlapAddresses.String(),
		"left_only_addresses":  report.LeftOnlyAddresses.String(),
		"right_only_addresses": report.RightOnlyAddresses.String(),
		"union_addresses":      report.UnionAddresses.String(),
		"equal":                report.Equal,
	}
}

// setReportJSON converts AlgebraSetReport.
func setReportJSON(report iprangedb.AlgebraSetReport) map[string]any {
	return map[string]any{
		"source_count":         DecimalUint(report.SourceCount),
		"source_range_count":   DecimalUint(report.SourceRangeCount),
		"joined_segment_count": DecimalUint(report.JoinedSegmentCount),
		"output_feed_count":    DecimalUint(report.OutputFeedCount),
		"output_range_count":   DecimalUint(report.OutputRangeCount),
		"output_addresses":     report.OutputAddresses.String(),
	}
}

// historyProjectionReportJSON converts HistoryProjectionReport.
func historyProjectionReportJSON(report iprangedb.HistoryProjectionReport) map[string]any {
	windows := make([]any, 0, len(report.Windows))
	for _, window := range report.Windows {
		windows = append(windows, historyWindowReportJSON(window))
	}
	return map[string]any{
		"logical_change":        LogicalChangeName(report.LogicalChange),
		"source_range_count":    DecimalUint(report.SourceRangeCount),
		"source_addresses":      report.SourceAddresses.String(),
		"created_feed_count":    DecimalUint(report.CreatedFeedCount),
		"before_interval_count": DecimalUint(report.BeforeIntervalCount),
		"after_interval_count":  DecimalUint(report.AfterIntervalCount),
		"before_addresses":      report.BeforeAddresses.String(),
		"after_addresses":       report.AfterAddresses.String(),
		"unchanged_addresses":   report.UnchangedAddresses.String(),
		"added_addresses":       report.AddedAddresses.String(),
		"removed_addresses":     report.RemovedAddresses.String(),
		"windows":               windows,
	}
}

// historyWindowReportJSON converts HistoryWindowReport.
func historyWindowReportJSON(window iprangedb.HistoryWindowReport) map[string]any {
	return map[string]any{
		"feed_name":             window.FeedName,
		"cutoff":                window.Cutoff,
		"created":               window.Created,
		"before_interval_count": DecimalUint(window.BeforeIntervalCount),
		"after_interval_count":  DecimalUint(window.AfterIntervalCount),
		"before_addresses":      window.BeforeAddresses.String(),
		"after_addresses":       window.AfterAddresses.String(),
		"unchanged_addresses":   window.UnchangedAddresses.String(),
		"added_addresses":       window.AddedAddresses.String(),
		"removed_addresses":     window.RemovedAddresses.String(),
	}
}

// ---------------------------------------------------------------------------
// Ephemeral reader lifecycle (Rust reader.rs close_ephemeral_reader /
// finish_ephemeral_reader / close_readers parity).
// ---------------------------------------------------------------------------

// openTemporaryReader opens one method-local source reader (Rust
// open_temporary): the path must exist and the mode selects the
// reader kind. Live readers pin one committed generation for the
// method's lifetime and close before the response.
func openTemporaryReader(path, mode string, st *rpc.SessionState) (*rpc.ReaderValue, *rpc.HandlerError) {
	return openReader(path, mode, "database source", st.Token())
}

// readerCloseFact converts one live reader close result to its wire
// source_close fact (reader closes carry no cleanup artifacts; Rust

// closeEphemeralReader closes one method-local reader. Immutable
// readers have no close fact (nil, nil); a live close failure is a
// product error whose details preserve the factual close result
// (Rust close_ephemeral_reader).
func closeEphemeralFact(reader *rpc.ReaderValue) (any, *rpc.HandlerError) {
	if reader.Live == nil {
		// Immutable readers carry no close fact but their mapping must
		// still be released (Rust close_ephemeral_reader parity).
		if reader.Immutable != nil {
			if err := reader.Immutable.Close(); err != nil {
				return nil, readError(err)
			}
		}
		return nil, nil
	}
	result, err := reader.Live.Close()
	if err != nil {
		return nil, readError(err)
	}
	fact := readerCloseFact(result)
	if result.Outcome != iprangedb.CloseOutcomeClosed || result.Cause != nil {
		code := "io"
		message := "live reader close is incomplete"
		if result.Cause != nil {
			if typed, ok := result.Cause.(*iprangedb.Error); ok {
				code = sdkCode(typed.Code)
			}
			message = result.Cause.Error()
		}
		return nil, &rpc.HandlerError{
			Code:    code,
			Outcome: "read_only_failure",
			Message: message,
			Details: map[string]any{"source_close": fact},
		}
	}
	return fact, nil
}

// closeSingleReader completes one read-only method that opened one
// ephemeral reader: success carries the live close fact as
// `source_close`; a close failure preserves the completed report
// (Rust finish_ephemeral_reader).
func closeSingleReader(reader *rpc.ReaderValue, report map[string]any) (any, *rpc.HandlerError) {
	fact, herr := closeEphemeralFact(reader)
	switch {
	case herr != nil:
		return nil, PreserveCompletedReport(herr, report)
	case fact != nil:
		report["source_close"] = fact
		return boundedResult(report)
	default:
		return boundedResult(report)
	}
}

// closeAllReaders closes every reader of one method in order and
// returns the collected close facts plus the first close failure
// (Rust ReaderCloseCollector).
func closeAllReaders(readers []*rpc.ReaderValue) ([]any, *rpc.HandlerError) {
	var closes []any
	var first *rpc.HandlerError
	for _, reader := range readers {
		fact, herr := closeEphemeralFact(reader)
		if herr == nil {
			if fact != nil {
				closes = append(closes, fact)
			}
			continue
		}
		// A failed close is still an SDK fact: keep its source_close
		// result with the primary error instead of dropping it
		// (Rust double-fault rule).
		if details, ok := herr.Details.(map[string]any); ok {
			if fact, ok := details["source_close"]; ok {
				closes = append(closes, fact)
			}
		}
		if first == nil {
			first = herr
		}
	}
	return closes, first
}

// closeReadersReport completes a multi-reader method: success carries
// every live close fact as `source_closes` in reader order; a close
// failure keeps all close facts and the completed report in the error
// details (Rust close_readers).
func closeReadersReport(readers []*rpc.ReaderValue, report map[string]any) (any, *rpc.HandlerError) {
	closes, first := closeAllReaders(readers)
	if first != nil {
		if len(closes) > 0 {
			details := map[string]any{}
			if first.Details != nil {
				if existing, ok := first.Details.(map[string]any); ok {
					details = existing
				}
			}
			details["source_closes"] = closes
			first.Details = details
		}
		return nil, PreserveCompletedReport(first, report)
	}
	if len(closes) > 0 {
		report["source_closes"] = closes
	}
	return boundedResult(report)
}

// withSourceClose carries one factual live source close into a
// publisher outcome: success gets `source_closes` (schema
// CLOSE_RESULT_LIST), a product error preserves it in `details`
// (Rust with_source_close; absent for immutable sources).
func withSourceCloses(report map[string]any, herr *rpc.HandlerError, closeFact any) (any, *rpc.HandlerError) {
	if closeFact == nil {
		return report, herr
	}
	if herr == nil {
		report["source_closes"] = []any{closeFact}
		return report, nil
	}
	details := map[string]any{}
	if herr.Details != nil {
		if existing, ok := herr.Details.(map[string]any); ok {
			details = existing
		}
	}
	details["source_closes"] = []any{closeFact}
	herr.Details = details
	return nil, herr
}

// requireExistingDatabase enforces that `path` names a regular file

// requirePublicationParent enforces that the destination has a file
// name and its parent is an existing directory (Rust

// pathHasFileName mirrors Rust Path::file_name().is_none(): the empty
// string, ".", "..", and separator-only paths name no file.
func pathHasFileName(path string) bool {
	switch filepath.Base(path) {
	case "", ".", "..", "/":
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Query handlers.
// ---------------------------------------------------------------------------

// QueryCardinalities streams one feed,addresses row per selected feed
// in catalog order into the atomic output file.
func QueryCardinalities(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "source", "selection", "membership_query_budget", "output")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	source, herr := sourceParts(object, "source")
	if herr != nil {
		return nil, herr
	}
	selection, herr := decodeSelection(object["selection"])
	if herr != nil {
		return nil, herr
	}
	budget, herr := decodeMembershipBudget(object["membership_query_budget"])
	if herr != nil {
		return nil, herr
	}
	spec, herr := decodeOutput(object["output"])
	if herr != nil {
		return nil, herr
	}
	reader, herr := openTemporaryReader(source.path, source.mode, st)
	if herr != nil {
		return nil, herr
	}
	report, herr := runQueryCardinalities(st, reader, selection, budget, spec)
	if herr != nil {
		return nil, CloseOnError([]*rpc.ReaderValue{reader}, herr)
	}
	return closeSingleReader(reader, report)
}

func runQueryCardinalities(st *rpc.SessionState, reader *rpc.ReaderValue, selection *decodedSelection, budget iprangedb.MembershipQueryBudget, spec *outputSpec) (map[string]any, *rpc.HandlerError) {
	writer, herr := fileio.NewExportWriter(spec.path, spec.policy, spec.budget)
	if herr != nil {
		return nil, herr
	}
	finished := false
	defer func() {
		if !finished {
			writer.Abort()
		}
	}()
	if !spec.jsonl {
		if herr := writer.WriteChunk([]byte("feed,addresses\n"), 0, fileio.U64(0)); herr != nil {
			return nil, herr
		}
	}
	scope, herr := resolveScope(reader, selection, budget, st)
	if herr != nil {
		return nil, herr
	}
	var captured *rpc.HandlerError
	sink := cardinalitySink{writer: writer, jsonl: spec.jsonl, captured: &captured}
	report, err := scope.Aggregate(iprangedb.MembershipAggregationCardinalities(),
		sink.feedBatch, sink.overlapBatch, st.Token())
	if err != nil {
		if capturedFailure(&captured) != nil {
			return nil, capturedFailure(&captured)
		}
		return nil, readError(err)
	}
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	finished = true
	return map[string]any{
		"method": "iprange.v1.query.cardinalities",
		"output": outputFacts(facts),
		"report": aggregationReportJSON(report),
	}, nil
}

// QueryOverlaps streams one left,right,addresses row per requested
// unordered pair into the atomic output file.
func QueryOverlaps(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "source", "selection", "membership_query_budget", "mode", "output")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	source, herr := sourceParts(object, "source")
	if herr != nil {
		return nil, herr
	}
	selection, herr := decodeSelection(object["selection"])
	if herr != nil {
		return nil, herr
	}
	budget, herr := decodeMembershipBudget(object["membership_query_budget"])
	if herr != nil {
		return nil, herr
	}
	mode, herr := decodeOverlapMode(object["mode"])
	if herr != nil {
		return nil, herr
	}
	spec, herr := decodeOutput(object["output"])
	if herr != nil {
		return nil, herr
	}
	reader, herr := openTemporaryReader(source.path, source.mode, st)
	if herr != nil {
		return nil, herr
	}
	report, herr := runQueryOverlaps(st, reader, selection, budget, mode, spec)
	if herr != nil {
		return nil, CloseOnError([]*rpc.ReaderValue{reader}, herr)
	}
	return closeSingleReader(reader, report)
}

func runQueryOverlaps(st *rpc.SessionState, reader *rpc.ReaderValue, selection *decodedSelection, budget iprangedb.MembershipQueryBudget, mode iprangedb.MembershipAggregationMode, spec *outputSpec) (map[string]any, *rpc.HandlerError) {
	writer, herr := fileio.NewExportWriter(spec.path, spec.policy, spec.budget)
	if herr != nil {
		return nil, herr
	}
	finished := false
	defer func() {
		if !finished {
			writer.Abort()
		}
	}()
	if !spec.jsonl {
		if herr := writer.WriteChunk([]byte("left,right,addresses\n"), 0, fileio.U64(0)); herr != nil {
			return nil, herr
		}
	}
	scope, herr := resolveScope(reader, selection, budget, st)
	if herr != nil {
		return nil, herr
	}
	var captured *rpc.HandlerError
	sink := overlapSink{writer: writer, jsonl: spec.jsonl, captured: &captured}
	report, err := scope.Aggregate(mode, sink.feedBatch, sink.overlapBatch, st.Token())
	if err != nil {
		if capturedFailure(&captured) != nil {
			return nil, capturedFailure(&captured)
		}
		return nil, readError(err)
	}
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	finished = true
	return map[string]any{
		"method": "iprange.v1.query.overlaps",
		"output": outputFacts(facts),
		"report": aggregationReportJSON(report),
	}, nil
}

// QueryMatchingFeeds opens one reader once and streams one
// address,feeds row per requested address; the aggregate matching
// count is the exact sum of the per-address reports.
func QueryMatchingFeeds(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "source", "addresses", "output")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	source, herr := sourceParts(object, "source")
	if herr != nil {
		return nil, herr
	}
	spec, herr := decodeOutput(object["output"])
	if herr != nil {
		return nil, herr
	}
	addresses, err := asStringArray(object, "addresses")
	if err != nil {
		return nil, rpc.InvalidParamsError("addresses must be an array of strings")
	}
	reader, herr := openTemporaryReader(source.path, source.mode, st)
	if herr != nil {
		return nil, herr
	}
	report, herr := runQueryMatchingFeeds(st, reader, addresses, spec)
	if herr != nil {
		return nil, CloseOnError([]*rpc.ReaderValue{reader}, herr)
	}
	return closeSingleReader(reader, report)
}

func runQueryMatchingFeeds(st *rpc.SessionState, reader *rpc.ReaderValue, addresses []string, spec *outputSpec) (map[string]any, *rpc.HandlerError) {
	writer, herr := fileio.NewExportWriter(spec.path, spec.policy, spec.budget)
	if herr != nil {
		return nil, herr
	}
	finished := false
	defer func() {
		if !finished {
			writer.Abort()
		}
	}()
	if !spec.jsonl {
		if herr := writer.WriteChunk([]byte("address,feeds\n"), 0, fileio.U64(0)); herr != nil {
			return nil, herr
		}
	}
	query, herr := membershipQuery(reader)
	if herr != nil {
		return nil, herr
	}
	var matchingFeedCount uint64
	names := make([]string, 0, 8)
	var line strings.Builder
	for _, text := range addresses {
		point, herr := ParseAddress(text)
		if herr != nil {
			return nil, herr
		}
		names = names[:0]
		var report iprangedb.MatchingFeedsReport
		var err error
		if point.V4 != nil {
			report, err = query.MatchingFeedsV4(iprangedb.IPv4(*point.V4), func(name string) error {
				names = append(names, name)
				return nil
			}, st.Token())
		} else {
			report, err = query.MatchingFeedsV6(*point.V6, func(name string) error {
				names = append(names, name)
				return nil
			}, st.Token())
		}
		if err != nil {
			return nil, readError(err)
		}
		line.Reset()
		if spec.jsonl {
			line.WriteString(`{"address":`)
			jsonQuote(&line, text)
			line.WriteString(`,"feeds":[`)
			for index, name := range names {
				if index != 0 {
					line.WriteByte(',')
				}
				jsonQuote(&line, name)
			}
			line.WriteString(`]}`)
		} else {
			csvField(&line, text)
			line.WriteByte(',')
			for index, name := range names {
				if index != 0 {
					line.WriteByte(';')
				}
				line.WriteString(name)
			}
		}
		if herr := writer.WriteLine([]byte(line.String()), fileio.U64(1)); herr != nil {
			return nil, herr
		}
		matchingFeedCount += report.MatchingFeedCount
	}
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	finished = true
	return map[string]any{
		"method":              "iprange.v1.query.matching_feeds",
		"output":              outputFacts(facts),
		"matching_feed_count": DecimalUint(matchingFeedCount),
	}, nil
}

// membershipQuery opens the membership query surface of either reader
// kind.
func membershipQuery(reader *rpc.ReaderValue) (*iprangedb.MembershipQuery, *rpc.HandlerError) {
	var query *iprangedb.MembershipQuery
	var err error
	if reader.Live != nil {
		query, err = reader.Live.MembershipQuery()
	} else {
		query, err = reader.Immutable.MembershipQuery()
	}
	if err != nil {
		return nil, readError(err)
	}
	return query, nil
}

// resolveScope resolves one feed selection over one reader (Rust
// resolve_scope).
func resolveScope(reader *rpc.ReaderValue, selection *decodedSelection, budget iprangedb.MembershipQueryBudget, st *rpc.SessionState) (*iprangedb.MembershipScope, *rpc.HandlerError) {
	query, herr := membershipQuery(reader)
	if herr != nil {
		return nil, herr
	}
	if selection.all {
		scope, err := query.AllFeeds(budget, st.Token())
		if err != nil {
			return nil, readError(err)
		}
		return scope, nil
	}
	scope, err := query.NamedFeeds(selection.names, budget, st.Token())
	if err != nil {
		return nil, readError(err)
	}
	return scope, nil
}

// ---------------------------------------------------------------------------
// Join handlers.
// ---------------------------------------------------------------------------

// JoinDirect merges the membership selection with one pinned direct
// provider and streams one feed,direct_value,addresses row per result
// cell; the uncovered cell of every feed carries the literal null.
func JoinDirect(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "membership", "direct", "output", "max_result_cells")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	membership, err := memberObject(object, "membership")
	if err != nil {
		return nil, rpc.InvalidParamsError("membership must be an object")
	}
	selection, herr := decodeSelection(membership["selection"])
	if herr != nil {
		return nil, herr
	}
	budget, herr := decodeMembershipBudget(membership["membership_query_budget"])
	if herr != nil {
		return nil, herr
	}
	membershipSource, herr := sourceParts(membership, "source")
	if herr != nil {
		return nil, herr
	}
	directSource, herr := sourceParts(object, "direct")
	if herr != nil {
		return nil, herr
	}
	maxCellsText, err := asString(object, "max_result_cells")
	if err != nil {
		return nil, rpc.InvalidParamsError("max_result_cells must be a canonical unsigned decimal string")
	}
	maxCells, err := canonicalDecimalString(maxCellsText)
	if err != nil {
		return nil, rpc.InvalidParamsError("max_result_cells must be a canonical unsigned decimal string")
	}
	spec, herr := decodeOutput(object["output"])
	if herr != nil {
		return nil, herr
	}
	membershipReader, herr := openTemporaryReader(membershipSource.path, membershipSource.mode, st)
	if herr != nil {
		return nil, herr
	}
	directReader, herr := openTemporaryReader(directSource.path, directSource.mode, st)
	if herr != nil {
		return nil, CloseOnError([]*rpc.ReaderValue{membershipReader}, herr)
	}
	readers := []*rpc.ReaderValue{membershipReader, directReader}
	report, herr := runJoinDirect(st, readers, selection, budget, maxCells, spec)
	if herr != nil {
		return nil, CloseOnError(readers, herr)
	}
	return closeReadersReport(readers, report)
}

func runJoinDirect(st *rpc.SessionState, readers []*rpc.ReaderValue, selection *decodedSelection, budget iprangedb.MembershipQueryBudget, maxCells uint64, spec *outputSpec) (map[string]any, *rpc.HandlerError) {
	writer, herr := fileio.NewExportWriter(spec.path, spec.policy, spec.budget)
	if herr != nil {
		return nil, herr
	}
	finished := false
	defer func() {
		if !finished {
			writer.Abort()
		}
	}()
	if !spec.jsonl {
		if herr := writer.WriteChunk([]byte("feed,direct_value,addresses\n"), 0, fileio.U64(0)); herr != nil {
			return nil, herr
		}
	}
	scope, herr := resolveScope(readers[0], selection, budget, st)
	if herr != nil {
		return nil, herr
	}
	source := directJoinSource(readers[1])
	var captured *rpc.HandlerError
	sink := directJoinSink{writer: writer, jsonl: spec.jsonl, captured: &captured}
	report, err := scope.JoinDirect(source, iprangedb.DirectJoinBudget{MaxResultCells: maxCells},
		sink.cellBatch, st.Token())
	if err != nil {
		if capturedFailure(&captured) != nil {
			return nil, capturedFailure(&captured)
		}
		return nil, readError(err)
	}
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	finished = true
	return map[string]any{
		"method": "iprange.v1.join.direct",
		"output": outputFacts(facts),
		"report": directJoinReportJSON(report),
	}, nil
}

// directJoinSource pins the direct provider of either reader kind.
func directJoinSource(reader *rpc.ReaderValue) iprangedb.DirectJoinSource {
	if reader.Live != nil {
		return iprangedb.DirectJoinSourceLive(reader.Live)
	}
	return iprangedb.DirectJoinSourceImmutable(reader.Immutable)
}

// JoinMembership merges two membership selections and streams cross
// and per-side uncovered rows.
func JoinMembership(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "left", "right", "output")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	left, err := memberObject(object, "left")
	if err != nil {
		return nil, rpc.InvalidParamsError("left must be an object")
	}
	right, err := memberObject(object, "right")
	if err != nil {
		return nil, rpc.InvalidParamsError("right must be an object")
	}
	leftSelection, herr := decodeSelection(left["selection"])
	if herr != nil {
		return nil, herr
	}
	rightSelection, herr := decodeSelection(right["selection"])
	if herr != nil {
		return nil, herr
	}
	leftBudget, herr := decodeMembershipBudget(left["membership_query_budget"])
	if herr != nil {
		return nil, herr
	}
	rightBudget, herr := decodeMembershipBudget(right["membership_query_budget"])
	if herr != nil {
		return nil, herr
	}
	leftSource, herr := sourceParts(left, "source")
	if herr != nil {
		return nil, herr
	}
	rightSource, herr := sourceParts(right, "source")
	if herr != nil {
		return nil, herr
	}
	spec, herr := decodeOutput(object["output"])
	if herr != nil {
		return nil, herr
	}
	leftReader, herr := openTemporaryReader(leftSource.path, leftSource.mode, st)
	if herr != nil {
		return nil, herr
	}
	rightReader, herr := openTemporaryReader(rightSource.path, rightSource.mode, st)
	if herr != nil {
		return nil, CloseOnError([]*rpc.ReaderValue{leftReader}, herr)
	}
	readers := []*rpc.ReaderValue{leftReader, rightReader}
	report, herr := runJoinMembership(st, readers, leftSelection, rightSelection, leftBudget, rightBudget, spec)
	if herr != nil {
		return nil, CloseOnError(readers, herr)
	}
	return closeReadersReport(readers, report)
}

func runJoinMembership(st *rpc.SessionState, readers []*rpc.ReaderValue, leftSelection, rightSelection *decodedSelection, leftBudget, rightBudget iprangedb.MembershipQueryBudget, spec *outputSpec) (map[string]any, *rpc.HandlerError) {
	leftScope, herr := resolveScope(readers[0], leftSelection, leftBudget, st)
	if herr != nil {
		return nil, herr
	}
	rightScope, herr := resolveScope(readers[1], rightSelection, rightBudget, st)
	if herr != nil {
		return nil, herr
	}
	writer, herr := fileio.NewExportWriter(spec.path, spec.policy, spec.budget)
	if herr != nil {
		return nil, herr
	}
	finished := false
	defer func() {
		if !finished {
			writer.Abort()
		}
	}()
	if !spec.jsonl {
		if herr := writer.WriteChunk([]byte("kind,left,right,side,feed,addresses\n"), 0, fileio.U64(0)); herr != nil {
			return nil, herr
		}
	}
	var captured *rpc.HandlerError
	sink := membershipJoinSink{writer: writer, jsonl: spec.jsonl, captured: &captured}
	report, err := leftScope.JoinMembership(rightScope, sink.crossBatch, sink.uncoveredBatch, st.Token())
	if err != nil {
		if capturedFailure(&captured) != nil {
			return nil, capturedFailure(&captured)
		}
		return nil, readError(err)
	}
	facts, herr := writer.Finish()
	if herr != nil {
		return nil, herr
	}
	finished = true
	return map[string]any{
		"method": "iprange.v1.join.membership",
		"output": outputFacts(facts),
		"report": membershipJoinReportJSON(report),
	}, nil
}

// ---------------------------------------------------------------------------
// Algebra handlers.
// ---------------------------------------------------------------------------

// openSources opens every algebra source in order; a failure closes
// the readers already opened (Rust open_sources).
func openSources(raw json.RawMessage, st *rpc.SessionState) ([]*rpc.ReaderValue, *rpc.HandlerError) {
	var sources []json.RawMessage
	if err := json.Unmarshal(raw, &sources); err != nil {
		return nil, rpc.InvalidParamsError("sources must be an array")
	}
	readers := make([]*rpc.ReaderValue, 0, len(sources))
	for _, item := range sources {
		source, err := decodeObject(item)
		if err != nil {
			return nil, CloseOnError(readers, rpc.InvalidParamsError("each algebra source must be an object"))
		}
		parts, herr := sourceParts(source, "source")
		if herr != nil {
			return nil, CloseOnError(readers, herr)
		}
		reader, herr := openTemporaryReader(parts.path, parts.mode, st)
		if herr != nil {
			return nil, CloseOnError(readers, herr)
		}
		readers = append(readers, reader)
	}
	return readers, nil
}

// resolveAlgebraScopes resolves each source's scope-all selection
// independently before the global catalog construction (Rust
// resolve_algebra_scopes).
func resolveAlgebraScopes(readers []*rpc.ReaderValue, sources []json.RawMessage, st *rpc.SessionState) ([]*iprangedb.MembershipScope, *rpc.HandlerError) {
	scopes := make([]*iprangedb.MembershipScope, 0, len(readers))
	for index, item := range sources {
		source, err := decodeObject(item)
		if err != nil {
			return nil, rpc.InvalidParamsError("each algebra source must be an object")
		}
		budget, herr := decodeMembershipBudget(source["membership_query_budget"])
		if herr != nil {
			return nil, herr
		}
		query, herr := membershipQuery(readers[index])
		if herr != nil {
			return nil, herr
		}
		scope, err := query.AllFeeds(budget, st.Token())
		if err != nil {
			return nil, readError(err)
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// AlgebraCount resolves the global same-name catalog and reports the
// exact union cardinality of one selection.
func AlgebraCount(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "sources", "selection", "algebra_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	selection, herr := decodeSelection(object["selection"])
	if herr != nil {
		return nil, herr
	}
	budget, herr := decodeAlgebraBudget(object["algebra_budget"])
	if herr != nil {
		return nil, herr
	}
	readers, herr := openSources(object["sources"], st)
	if herr != nil {
		return nil, herr
	}
	report, herr := runAlgebraCount(st, readers, object["sources"], selection, budget)
	if herr != nil {
		return nil, CloseOnError(readers, herr)
	}
	return closeReadersReport(readers, report)
}

func runAlgebraCount(st *rpc.SessionState, readers []*rpc.ReaderValue, sourcesRaw json.RawMessage, selection *decodedSelection, budget iprangedb.MembershipAlgebraBudget) (map[string]any, *rpc.HandlerError) {
	var sources []json.RawMessage
	if err := json.Unmarshal(sourcesRaw, &sources); err != nil {
		return nil, rpc.InvalidParamsError("sources must be an array")
	}
	scopes, herr := resolveAlgebraScopes(readers, sources, st)
	if herr != nil {
		return nil, herr
	}
	algebra, err := iprangedb.NewMembershipAlgebra(scopes, budget, st.Token())
	if err != nil {
		return nil, readError(err)
	}
	report, err := algebra.Count(algebraFeedSelection(selection), st.Token())
	if err != nil {
		return nil, readError(err)
	}
	return map[string]any{
		"method":      "iprange.v1.algebra.count",
		"report":      countReportJSON(report),
		"cardinality": report.Addresses.String(),
	}, nil
}

// AlgebraCompare reports the exact four-case comparison of two
// selections over the global catalog.
func AlgebraCompare(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "sources", "left", "right", "algebra_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	left, herr := decodeSelection(object["left"])
	if herr != nil {
		return nil, herr
	}
	right, herr := decodeSelection(object["right"])
	if herr != nil {
		return nil, herr
	}
	budget, herr := decodeAlgebraBudget(object["algebra_budget"])
	if herr != nil {
		return nil, herr
	}
	readers, herr := openSources(object["sources"], st)
	if herr != nil {
		return nil, herr
	}
	report, herr := runAlgebraCompare(st, readers, object["sources"], left, right, budget)
	if herr != nil {
		return nil, CloseOnError(readers, herr)
	}
	return closeReadersReport(readers, report)
}

func runAlgebraCompare(st *rpc.SessionState, readers []*rpc.ReaderValue, sourcesRaw json.RawMessage, left, right *decodedSelection, budget iprangedb.MembershipAlgebraBudget) (map[string]any, *rpc.HandlerError) {
	var sources []json.RawMessage
	if err := json.Unmarshal(sourcesRaw, &sources); err != nil {
		return nil, rpc.InvalidParamsError("sources must be an array")
	}
	scopes, herr := resolveAlgebraScopes(readers, sources, st)
	if herr != nil {
		return nil, herr
	}
	algebra, err := iprangedb.NewMembershipAlgebra(scopes, budget, st.Token())
	if err != nil {
		return nil, readError(err)
	}
	report, err := algebra.Compare(algebraFeedSelection(left), algebraFeedSelection(right), st.Token())
	if err != nil {
		return nil, readError(err)
	}
	return map[string]any{
		"method": "iprange.v1.algebra.compare",
		"report": comparisonReportJSON(report),
	}, nil
}

// AlgebraPublish materializes one set operation into a fresh immutable
// membership file and publishes it. Preflight refuses an
// unrepresentable result before any source is opened or the
// destination is published (spec response ceiling).
func AlgebraPublish(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "sources", "operation", "output_mode", "value_tag",
		"metadata", "destination", "publication_policy", "algebra_budget", "algebra_output_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	budget, herr := decodeAlgebraBudget(object["algebra_budget"])
	if herr != nil {
		return nil, herr
	}
	var sources []json.RawMessage
	if err := json.Unmarshal(object["sources"], &sources); err != nil {
		return nil, rpc.InvalidParamsError("sources must be an array")
	}
	// The complete result carries one live-close fact per opened live
	// source; refuse an unrepresentable request before any source is
	// opened or the destination is published.
	if herr := preflightAlgebraPublish(st, len(sources)); herr != nil {
		return nil, herr
	}
	readers, herr := openSources(object["sources"], st)
	if herr != nil {
		return nil, herr
	}
	report, herr := runAlgebraPublish(st, readers, object, sources, budget)
	if herr != nil {
		return nil, CloseOnError(readers, herr)
	}
	return closeReadersReport(readers, report)
}

func runAlgebraPublish(st *rpc.SessionState, readers []*rpc.ReaderValue, object rawObject, sources []json.RawMessage, budget iprangedb.MembershipAlgebraBudget) (map[string]any, *rpc.HandlerError) {
	scopes, herr := resolveAlgebraScopes(readers, sources, st)
	if herr != nil {
		return nil, herr
	}
	algebra, err := iprangedb.NewMembershipAlgebra(scopes, budget, st.Token())
	if err != nil {
		return nil, readError(err)
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return nil, rpc.InvalidParamsError("destination must be a string")
	}
	if herr := requirePublicationParent(destination); herr != nil {
		return nil, herr
	}
	valueTag, herr := decodeValueTag(object, "value_tag")
	if herr != nil {
		return nil, herr
	}
	metadata, herr := MetadataValueFromObject(rawObjectValue(object, "metadata"))
	if herr != nil {
		return nil, herr
	}
	operation, herr := decodeOperation(object["operation"])
	if herr != nil {
		return nil, herr
	}
	outputMode, herr := decodeOutputMode(object["output_mode"])
	if herr != nil {
		return nil, herr
	}
	policyName, err := asString(object, "publication_policy")
	if err != nil {
		return nil, rpc.InvalidParamsError("publication_policy is invalid")
	}
	outputBudget, herr := decodeAlgebraOutputBudget(object["algebra_output_budget"])
	if herr != nil {
		return nil, herr
	}
	result, err := algebra.PublishSet(destination, valueTag, operation, outputMode,
		metadataJSONBytes(metadata), policyByName(policyName), outputBudget, st.Token())
	if err != nil {
		var failure *iprangedb.AlgebraPreparationFailure
		if errors.As(err, &failure) {
			return nil, algebraPreparationError(failure)
		}
		return nil, readError(err)
	}
	publication, herr := PublicationResultJSON(&result.Publication)
	if herr != nil {
		return nil, herr
	}
	if result.Publication.Publication != iprangedb.PublicationPublished || result.Publication.Cause != nil {
		code := "io"
		message := "algebra publication did not complete"
		if result.Publication.Cause != nil {
			code = publicationErrorCode(result.Publication.Cause)
			message = result.Publication.Cause.Error()
		}
		return nil, &rpc.HandlerError{
			Code:    code,
			Outcome: PublicationStatusName(result.Publication.Publication),
			Message: message,
			Details: map[string]any{
				"report":      setReportJSON(result.Report),
				"publication": publication,
			},
		}
	}
	return map[string]any{
		"method":      "iprange.v1.algebra.publish",
		"report":      setReportJSON(result.Report),
		"publication": publication,
	}, nil
}

// rawObjectValue wraps one raw member into a rawObject for the shared
// metadata decoder (the wire metadata is a single object).
func rawObjectValue(object rawObject, name string) rawObject {
	raw, ok := object[name]
	if !ok {
		return rawObject{}
	}
	decoded, err := decodeObject(raw)
	if err != nil {
		return rawObject{}
	}
	return decoded
}

// metadataJSONBytes projects one metadata terminal onto the SDK
// metadata-bytes argument: keep and clear stage no bytes; the wire
// schema already excludes keep for new immutable destinations.
func metadataJSONBytes(metadata MetadataValue) []byte {
	if metadata.Keep || metadata.Clear {
		return nil
	}
	return metadata.Bytes
}

// algebraPreparationError converts an SDK preparation failure to the
// full wire facts (Rust algebra.rs algebra_preparation_error: cleanup
// state, the cleanup ledger, the coordination cleanup class, the
// housekeeping evidence, and the private attempt identity; the attempt
// never completed a durable publication, so the outcome is
// not_started).
func algebraPreparationError(failure *iprangedb.AlgebraPreparationFailure) *rpc.HandlerError {
	code := "io"
	message := "algebra preparation failed"
	if failure.Cause != nil {
		if typed, ok := failure.Cause.(*iprangedb.Error); ok {
			code = sdkCode(typed.Code)
		}
		message = "algebra preparation failed: " + failure.Cause.Error()
	}
	return &rpc.HandlerError{
		Code:    code,
		Outcome: "not_started",
		Message: message,
		Details: map[string]any{
			"cleanup_state":        cleanupStateName(failure.Cleanup),
			"cleanup":              CleanupArtifactsJSON(failure.CleanupArtifacts),
			"coordination_cleanup": CoordinationCleanupJSON(failure.CoordinationCleanup),
			"housekeeping":         HousekeepingJSON(failure.Housekeeping, failure.VisibleHousekeeping),
			"visible_housekeeping": VisibleHousekeepingJSON(failure.VisibleHousekeeping),
			"output":               privateOutputAttemptValueOrNil(failure.Output),
		},
	}
}

// publicationErrorCode maps one publication-failure cause to its wire
// adapter code (Rust publication_code; sdkCode covers the rest).
func publicationErrorCode(err error) string {
	var typed *iprangedb.Error
	if !errors.As(err, &typed) {
		return "io"
	}
	switch typed.Code {
	case iprangedb.ErrorPublicationUnsupported:
		return "publication_unsupported"
	case iprangedb.ErrorOSUnsupported:
		return "os_unsupported"
	case iprangedb.ErrorDurabilityUnsupported:
		return "durability_unsupported"
	case iprangedb.ErrorAccessPolicyUnsupported:
		return "access_policy_unsupported"
	case iprangedb.ErrorSnapshotPreparationFailed:
		return "snapshot_preparation_failed"
	case iprangedb.ErrorLiveCoordinationUnsupported:
		return "live_coordination_unsupported"
	}
	return sdkCode(typed.Code)
}

// ---------------------------------------------------------------------------
// History projection handler (live writer publisher family).
// ---------------------------------------------------------------------------

// HistoryProject projects one last-seen source into named destination
// feeds of one live membership writer and finishes through the shared
// publisher facts machinery (Rust history_project).
func HistoryProject(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "path", "last_seen", "windows", "metadata", "writer_budget")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	path, err := asString(object, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError("path must be a string")
	}
	if herr := requireExistingDatabase(path); herr != nil {
		return nil, herr
	}
	metadata, herr := MetadataValueFromObject(rawObjectValue(object, "metadata"))
	if herr != nil {
		return nil, herr
	}
	budget, herr := decodeWriterBudgetRaw(object["writer_budget"])
	if herr != nil {
		return nil, herr
	}
	source, herr := sourceParts(object, "last_seen")
	if herr != nil {
		return nil, herr
	}
	windows, herr := decodeWindows(object["windows"])
	if herr != nil {
		return nil, herr
	}
	// The complete report grows linearly with the window count; refuse
	// a request whose worst-case inline result cannot fit the response
	// object ceiling BEFORE any writer is opened or mutation runs, so
	// a committed workflow is never relabeled as a read-only failure.
	if herr := preflightHistoryResult(st, windows); herr != nil {
		return nil, herr
	}
	writer, err := iprangedb.OpenLiveWriter(path, budget, st.Token())
	if err != nil {
		return nil, SDKError(err, "not_started")
	}
	reader, herr := openTemporaryReader(source.path, source.mode, st)
	if herr != nil {
		return nil, CloseWriterFacts(writer, herr)
	}
	projection, herr := projectHistory(writer, reader, windows, st)
	return finishProjectionFacts(writer, reader, projection, herr, &metadata, st)
}

// projectHistory drives one SDK projection over either source kind.
func projectHistory(writer *iprangedb.LiveWriter, reader *rpc.ReaderValue, windows []iprangedb.HistoryWindow, st *rpc.SessionState) (*iprangedb.FinishedHistoryProjection, *rpc.HandlerError) {
	var source iprangedb.HistoryProjectionSource
	if reader.Live != nil {
		source = iprangedb.HistoryProjectionSource{
			Kind: iprangedb.HistoryProjectionSourceLive,
			Live: reader.Live,
		}
	} else {
		source = iprangedb.HistoryProjectionSource{
			Kind:   iprangedb.HistoryProjectionSourceImmutable,
			Reader: reader.Immutable,
		}
	}
	projection, err := writer.ProjectHistory(source, windows, st.Token())
	if err != nil {
		return nil, readError(err)
	}
	return projection, nil
}

// finishProjectionFacts consumes one completed projection: the
// ephemeral last-seen reader closes first, the requested metadata is
// applied through the prepared draft (changed) or one fresh membership
// transaction (no change), and the commit/close facts are finished
// through the shared publisher machinery (Rust collect_projection_facts
// + finish_projection_facts).
func finishProjectionFacts(writer *iprangedb.LiveWriter, reader *rpc.ReaderValue, projection *iprangedb.FinishedHistoryProjection, projectionErr *rpc.HandlerError, metadata *MetadataValue, st *rpc.SessionState) (any, *rpc.HandlerError) {
	const method = "iprange.v1.history.project"
	sourceClose, closeErr := closeEphemeralFact(reader)
	if closeErr != nil {
		if projectionErr == nil && projection != nil {
			// The projection completed but the reader close failed:
			// keep the completed report and the writer close facts
			// (Rust ReaderCloseFailed).
			report := historyProjectionReportJSON(projection.Report())
			details := map[string]any{"report": report}
			if closeFacts, herr := CloseWriter(writer); herr == nil {
				details["writer_close"] = closeFacts
			}
			return nil, PreserveCompletedReport(closeErr, details)
		}
		// Double fault: keep the projection error primary and merge
		// the factual close result it carried into the error details
		// (Rust double-fault path).
		errorOut := projectionErr
		if errorOut == nil {
			errorOut = rpc.NewHandlerError("io", "read_only_failure", "history projection failed")
		}
		if closeDetails, ok := closeErr.Details.(map[string]any); ok {
			if fact, ok := closeDetails["source_close"]; ok {
				details := map[string]any{}
				if errorOut.Details != nil {
					if existing, ok := errorOut.Details.(map[string]any); ok {
						details = existing
					}
				}
				details["source_close"] = fact
				errorOut.Details = details
			}
		}
		return nil, errorOut
	}
	if projectionErr != nil {
		// No completed report: abort the draft and keep the writer
		// close facts (Rust workflow_failure).
		return withSourceCloses(nil, WorkflowFailure(writer, projectionErr), sourceClose)
	}
	report := historyProjectionReportJSON(projection.Report())
	if !projection.IsChanged() {
		// No-change projections discard their draft; a metadata
		// replacement or clear commits through one fresh membership
		// transaction (Rust publish_no_change). The commit error is
		// preserved so the details carry the factual commit result.
		metadataLogicalChange, commit, commitErr, stageErr := noChangeProjectionFacts(writer, metadata, st)
		if stageErr != nil {
			return withSourceCloses(nil, FinishWriterError(writer, stageErr, report), sourceClose)
		}
		value, ferr := FinishPublisher(writer, method, report, metadataLogicalChange, commit, commitErr)
		result, _ := value.(map[string]any)
		return withSourceCloses(result, ferr, sourceClose)
	}
	metadataLogicalChange, commit, commitErr, stageErr := stageChangedProjection(projection, metadata)
	if stageErr != nil {
		return withSourceCloses(nil, FinishWriterError(writer, stageErr, report), sourceClose)
	}
	value, ferr := FinishPublisher(writer, method, report, metadataLogicalChange, commit, commitErr)
	result, _ := value.(map[string]any)
	return withSourceCloses(result, ferr, sourceClose)
}

// stageChangedProjection stages the requested metadata inside one
// changed projection and commits, preserving the commit error fact
// (Rust publish_changed): the shared PublishChanged helper cannot
// carry a commit failure, which the factual-result rule requires.
func stageChangedProjection(projection *iprangedb.FinishedHistoryProjection, metadata *MetadataValue) (string, *iprangedb.CommitResult, error, *rpc.HandlerError) {
	metadataLogicalChange := "unchanged"
	switch {
	case metadata.Keep:
		// keep
	case metadata.Clear:
		switch changed, err := projection.ClearMetadataJSON(); {
		case err != nil:
			return "", nil, nil, SDKError(err, "not_started")
		case changed:
			metadataLogicalChange = "changed"
		}
	default:
		if _, err := projection.SetMetadataJSON(metadata.Bytes); err != nil {
			return "", nil, nil, SDKError(err, "not_started")
		}
		metadataLogicalChange = "changed"
	}
	commit, err := projection.Commit()
	if err != nil {
		return metadataLogicalChange, nil, err, nil
	}
	return metadataLogicalChange, &commit, nil, nil
}

// noChangeProjectionFacts commits only the requested metadata through
// one fresh membership transaction when no projection draft exists.
// Replacements always commit; clear commits only when metadata was
// present; a no-op keep returns no commit. The commit error is
// preserved (Rust publish_no_change; the shared PublishNoChange
// collapses the error into an empty result, which would fabricate
// commit facts).
func noChangeProjectionFacts(writer *iprangedb.LiveWriter, metadata *MetadataValue, st *rpc.SessionState) (string, *iprangedb.CommitResult, error, *rpc.HandlerError) {
	if metadata.Keep {
		return "unchanged", nil, nil, nil
	}
	transaction, err := writer.BeginMembershipTransaction(st.Token())
	if err != nil {
		return "", nil, nil, SDKError(err, "not_started")
	}
	if metadata.Clear {
		changed, err := transaction.ClearMetadataJSON()
		if err != nil {
			_ = transaction.Abort()
			return "", nil, nil, SDKError(err, "not_started")
		}
		if !changed {
			_ = transaction.Abort()
			return "unchanged", nil, nil, nil
		}
		commit, err := transaction.Commit()
		if err != nil {
			return "changed", nil, err, nil
		}
		return "changed", &commit, nil, nil
	}
	if _, err := transaction.SetMetadataJSON(metadata.Bytes); err != nil {
		_ = transaction.Abort()
		return "", nil, nil, SDKError(err, "not_started")
	}
	commit, err := transaction.Commit()
	if err != nil {
		return "changed", nil, err, nil
	}
	return "changed", &commit, nil, nil
}

// ---------------------------------------------------------------------------
// Response-ceiling preflight builders (Rust algebra.rs parity).
// ---------------------------------------------------------------------------

// widestBasename bounds every portably representable local basename
// (SDK clamps at 512 bytes; lossy UTF-8 widening at most doubles it).
const widestBasename = 1024

// widestCode bounds every SDK error-code name used on the wire.
const widestCode = 32

// widestHexID is the largest identity/digest hex form (16 bytes).
const widestHexID = "ffffffffffffffffffffffffffffffff"

func widestCleanupArtifact() map[string]any {
	return map[string]any{
		"kind":               "unpublished_main_tail",
		"directory_role":     "main_file",
		"directory_identity": WidestIdentity(),
		"basename_encoding":  uint32(65535),
		"basename":           strings.Repeat("b", widestBasename),
		"identity":           WidestIdentity(),
		"error": map[string]any{
			"code":   "io",
			"detail": strings.Repeat("d", 64),
		},
		"creation_security": map[string]any{
			"kind":       uint32(65535),
			"commitment": strings.Repeat("c", 64),
		},
		"unpublished_tail": map[string]any{
			"expected_database_id":            widestHexID,
			"committed_target_transaction_id": WidestU64,
			"committed_target_nonce":          widestHexID,
			"committed_target_length":         WidestU64,
			"observed_tail_end_exclusive":     WidestU64,
		},
	}
}

func widestHousekeepingArtifact() map[string]any {
	return map[string]any{
		"state":              "move_ambiguous",
		"directory_role":     "scratch_directory",
		"directory_identity": WidestIdentity(),
		"basename_encoding":  uint32(65535),
		"attempt_id":         widestHexID,
		"ordinal":            uint32(4294967295),
		"envelope_basename":  strings.Repeat("e", widestBasename),
		"envelope_identity":  WidestIdentity(),
		"source_basename":    strings.Repeat("s", widestBasename),
		"inert_basename":     strings.Repeat("i", widestBasename),
		"source_presence":    "unclassified",
		"source_identity":    WidestIdentity(),
		"inert_presence":     "unclassified",
		"inert_identity":     WidestIdentity(),
		"kind":               "unpublished_main_tail",
		"creation_security": map[string]any{
			"kind":       uint32(65535),
			"commitment": strings.Repeat("c", 64),
		},
		"selected_envelope_sequence": WidestU64,
	}
}

func widestCommitCleanupArtifact() map[string]any {
	return map[string]any{
		"directory_identity":          WidestIdentity(),
		"main_basename":               strings.Repeat("m", widestBasename),
		"main_identity":               WidestIdentity(),
		"expected_database_id":        widestHexID,
		"target_transaction_id":       WidestU64,
		"target_commit_nonce":         widestHexID,
		"committed_target_length":     WidestU64,
		"observed_tail_end_exclusive": WidestU64,
		"cleanup_error":               strings.Repeat("i", widestCode),
	}
}

// preflightHistoryResult refuses a history.project request whose
// worst-case complete inline report cannot fit the response-object
// ceiling, before any writer is opened or mutation runs (spec
// response ceiling; the widest windows use the request's real feed
// names).
func preflightHistoryResult(st *rpc.SessionState, windows []iprangedb.HistoryWindow) *rpc.HandlerError {
	widestWindows := make([]any, 0, len(windows))
	for _, window := range windows {
		widestWindows = append(widestWindows, map[string]any{
			"feed_name":             window.FeedName,
			"cutoff":                uint32(4294967295),
			"created":               true,
			"before_interval_count": WidestU64,
			"after_interval_count":  WidestU64,
			"before_addresses":      Widest129,
			"after_addresses":       Widest129,
			"unchanged_addresses":   Widest129,
			"added_addresses":       Widest129,
			"removed_addresses":     Widest129,
		})
	}
	artifact := widestCommitCleanupArtifact()
	identity := WidestIdentity()
	worst := map[string]any{
		"method":                  "iprange.v1.history.project",
		"metadata_logical_change": "unchanged",
		"writer_close": map[string]any{
			"outcome": "close_incomplete",
			"cleanup": map[string]any{"artifacts": []any{artifact}},
			"coordination_cleanup": map[string]any{
				"kind": "retained_writer_close_required",
			},
		},
		"source_closes": []any{WidestCloseFact()},
		"commit": map[string]any{
			"attempted_database_id":    widestHexID,
			"directory_identity":       identity,
			"main_identity":            identity,
			"attempted_transaction_id": WidestU64,
			"attempted_commit_nonce":   widestHexID,
			"durability":               "committed",
			"cleanup":                  map[string]any{"artifacts": []any{artifact}},
			"coordination_cleanup": map[string]any{
				"kind": "retained_reader_close_required",
			},
		},
		"report": map[string]any{
			"logical_change":        "changed",
			"source_range_count":    WidestU64,
			"source_addresses":      Widest129,
			"created_feed_count":    WidestU64,
			"before_interval_count": WidestU64,
			"after_interval_count":  WidestU64,
			"before_addresses":      Widest129,
			"after_addresses":       Widest129,
			"unchanged_addresses":   Widest129,
			"added_addresses":       Widest129,
			"removed_addresses":     Widest129,
			"windows":               widestWindows,
		},
	}
	return preflightResponse(st, worst)
}

// preflightAlgebraPublish refuses an algebra.publish request whose
// worst-case complete result (one live-close fact per source) cannot
// fit the response-object ceiling, before the destination is
// published (spec response ceiling).
func preflightAlgebraPublish(st *rpc.SessionState, sources int) *rpc.HandlerError {
	widestBasenameB64 := strings.Repeat("Z", 684)
	attempt := map[string]any{
		"database_id":                   widestHexID,
		"transaction_id":                WidestU64,
		"commit_nonce":                  widestHexID,
		"publication_attempt_id":        widestHexID,
		"directory_identity":            WidestIdentity(),
		"destination_basename_encoding": uint32(65535),
		"destination_basename":          widestBasenameB64,
		"output_identity":               WidestIdentity(),
		"output_byte_length":            WidestU64,
		"output_sha512":                 strings.Repeat("f", 128),
		"publication_policy":            "fail_if_exists",
		"previous_destination": map[string]any{
			"identity":    WidestIdentity(),
			"byte_length": WidestU64,
			"sha512":      strings.Repeat("e", 128),
		},
		"reservation_identity": WidestIdentity(),
		"creation_security": map[string]any{
			"kind":       uint32(65535),
			"commitment": strings.Repeat("d", 64),
		},
	}
	cleanupArtifacts := make([]any, 0, 4)
	housekeepingArtifacts := make([]any, 0, 2)
	for i := 0; i < 4; i++ {
		cleanupArtifacts = append(cleanupArtifacts, widestCleanupArtifact())
	}
	for i := 0; i < 2; i++ {
		housekeepingArtifacts = append(housekeepingArtifacts, widestHousekeepingArtifact())
	}
	visible := make([]any, 0, 2)
	for i := 0; i < 2; i++ {
		visible = append(visible, widestHousekeepingArtifact())
	}
	closeFacts := make([]any, sources)
	for i := range closeFacts {
		closeFacts[i] = WidestCloseFact()
	}
	worst := map[string]any{
		"method": "iprange.v1.algebra.publish",
		"report": map[string]any{
			"source_count":         WidestU64,
			"source_range_count":   WidestU64,
			"joined_segment_count": WidestU64,
			"output_feed_count":    WidestU64,
			"output_range_count":   WidestU64,
			"output_addresses":     Widest129,
		},
		"publication": map[string]any{
			"attempt":                                attempt,
			"main_namespace_may_have_been_attempted": true,
			"publication":                            "outcome_unknown",
			"destination_content":                    "unclassified",
			"later_canonical":                        "ready_live_sidecar",
			"main_access_policy":                     "changed_or_unproven",
			"coordination_access_policy":             "unclassified",
			"cleanup":                                map[string]any{"artifacts": cleanupArtifacts},
			"coordination_cleanup": map[string]any{
				"kind": "retained_reader_close_required",
			},
			"housekeeping": map[string]any{
				"state":     "visible",
				"artifacts": housekeepingArtifacts,
			},
			"visible_housekeeping": visible,
		},
		"source_closes": closeFacts,
	}
	return preflightResponse(st, worst)
}
