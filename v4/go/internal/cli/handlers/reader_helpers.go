// Shared reader operation helpers (Rust handlers/reader.rs parity):
// connection reader handles, ephemeral reader lifecycles, metadata
// delivery (inline base64 or atomic file), the ordered numeric feed
// catalog snapshot, threat-feed name mapping, and address parsing.
// These are the single authority for the reader-facing wire shapes
// every family uses.

package handlers

import (
	"fmt"
	"net/netip"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ReaderHandle returns the connection reader for one handle with the
// documented handle errors (invalid, closed, unknown).
func ReaderHandle(state *rpc.SessionState, handle string) (*rpc.ReaderValue, *rpc.HandlerError) {
	if !validHandle(handle) {
		return nil, rpc.InvalidParamsError("invalid reader handle")
	}
	if state.Resources.ClosedReaders[handle] {
		return nil, rpc.NewHandlerError("handle_closed", "not_started", "reader handle is already closed")
	}
	reader := state.Resources.Readers[handle]
	if reader == nil {
		return nil, rpc.NewHandlerError("handle_not_found", "not_started", "reader handle is unknown")
	}
	return reader, nil
}

// CloseOnError closes every reader of one failed read-only method and
// merges the close facts into the error details.
func CloseOnError(readers []*rpc.ReaderValue, failure *rpc.HandlerError) *rpc.HandlerError {
	var closes []any
	for _, reader := range readers {
		result, ok, err := reader.CloseLive()
		if err != nil || !ok {
			continue
		}
		closeFacts := map[string]any{
			"outcome":              CloseOutcomeName(result.Outcome),
			"cleanup":              map[string]any{},
			"coordination_cleanup": CoordinationCleanupJSON(result.CoordinationCleanup),
		}
		closes = append(closes, closeFacts)
	}
	if len(closes) == 0 {
		return failure
	}
	details := map[string]any{}
	if failure.Details != nil {
		if existing, ok := failure.Details.(map[string]any); ok {
			details = existing
		}
	}
	details["source_closes"] = closes
	failure.Details = details
	return failure
}

// PreserveCompletedReport keeps the completed logical report of a
// failed post-report step in the error details (the factual work is
// never dropped).
func PreserveCompletedReport(failure *rpc.HandlerError, report any) *rpc.HandlerError {
	details := map[string]any{}
	if failure.Details != nil {
		if existing, ok := failure.Details.(map[string]any); ok {
			details = existing
		}
	}
	details["report"] = report
	failure.Details = details
	return failure
}

// MetadataJSONLen returns the exact decompressed metadata length
// without materializing the blob (both reader kinds).
func MetadataJSONLen(reader *rpc.ReaderValue) (uint64, bool, error) {
	if reader.Live != nil {
		return reader.Live.MetadataJSONLen()
	}
	return reader.Immutable.MetadataJSONLen()
}

// ValidateDelivery enforces the strict `delivery` parameter object
// (inline, or file with path/policy/budgets).
func ValidateDelivery(value rawObject) error {
	delivery, err := memberObject(value, "delivery")
	if err != nil {
		return fmt.Errorf("delivery must be an object")
	}
	mode, err := asString(delivery, "mode")
	if err != nil {
		return fmt.Errorf("delivery.mode must be inline or file")
	}
	switch mode {
	case "inline":
		return exactObjectRaw(delivery, "mode")
	case "file":
		if err := exactObjectRaw(delivery, "mode", "path", "publication_policy", "max_output_bytes", "max_open_files"); err != nil {
			return err
		}
		path, err := asString(delivery, "path")
		if err != nil {
			return err
		}
		if err := validatePath(path); err != nil {
			return err
		}
		policy, err := asString(delivery, "publication_policy")
		if err != nil {
			return err
		}
		if !validPublicationPolicyName(policy) {
			return fmt.Errorf("delivery.publication_policy is invalid")
		}
		bytesLimit, err := asDecimalString(delivery, "max_output_bytes")
		if err != nil {
			return err
		}
		if bytesLimit == "0" {
			return fmt.Errorf("delivery.max_output_bytes must be positive")
		}
		if _, err := asUint64(delivery, "max_open_files"); err != nil {
			return fmt.Errorf("delivery.max_open_files must be u32")
		}
		return nil
	}
	return fmt.Errorf("delivery.mode must be inline or file")
}

func exactObjectRaw(object rawObject, fields ...string) error {
	for key := range object {
		if !containsString(fields, key) {
			return fmt.Errorf("unknown member %q", key)
		}
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing member %q", field)
		}
	}
	return nil
}

func validPublicationPolicyName(value string) bool {
	switch value {
	case "fail_if_exists", "replace_existing", "replace_existing_no_rollback":
		return true
	}
	return false
}

// MetadataDeliveryInlineLen returns the inline metadata length when
// delivery is inline; nil otherwise.
func MetadataDeliveryInlineLen(reader *rpc.ReaderValue, delivery rawObject) (uint64, bool, bool, *rpc.HandlerError) {
	mode, _ := asString(delivery, "mode")
	if mode != "inline" {
		return 0, false, false, nil
	}
	length, present, err := MetadataJSONLen(reader)
	if err != nil {
		return 0, false, false, readError(err)
	}
	return length, present, true, nil
}

// PreflightMetadataInline refuses an inline metadata delivery whose
// worst-case base64 payload cannot fit the response-object ceiling,
// before the blob is materialized.
func PreflightMetadataInline(state *rpc.SessionState, method string, metadataLen uint64) *rpc.HandlerError {
	padded := (metadataLen + 2) / 3 * 4
	if padded >= uint64(rpc.ResponseObjectLimit) {
		return outputRefusal()
	}
	worst := map[string]any{
		"method":  method,
		"present": true,
		"base64":  string(make([]byte, padded)), // placeholder size only
	}
	// Replace the placeholder with 'Z' to model base64 text length.
	by := make([]byte, padded)
	for i := range by {
		by[i] = 'Z'
	}
	worst["base64"] = string(by)
	return preflightResponse(state, worst)
}

func outputRefusal() *rpc.HandlerError {
	return rpc.NewHandlerError("output_limit", "not_started",
		"request refused: the complete inline result cannot fit the 65000-byte response object")
}

// MetadataResult converts one reader metadata fetch under the strict
// delivery object. Callers must run the inline preflight before this
// materializes the blob.
func MetadataResult(method string, reader *rpc.ReaderValue, delivery rawObject) (any, *rpc.HandlerError) {
	mode, err := asString(delivery, "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError("delivery.mode must be inline or file")
	}
	switch mode {
	case "inline":
		bytes, present, err := readerMetadata(reader)
		if err != nil {
			return nil, readError(err)
		}
		if !present {
			return boundedResult(map[string]any{"method": method, "present": false})
		}
		return boundedResult(map[string]any{
			"method":  method,
			"present": true,
			"base64":  Base64Padded(bytes),
		})
	case "file":
		path, err := asString(delivery, "path")
		if err != nil {
			return nil, rpc.InvalidParamsError("delivery.path must be a string")
		}
		policyName, err := asString(delivery, "publication_policy")
		if err != nil {
			return nil, rpc.InvalidParamsError("delivery.publication_policy is invalid")
		}
		policy := policyByName(policyName)
		// max_output_bytes is a decimal string on the wire (common.POSITIVE_U64);
		// a JSON number cannot carry every u64 without client precision loss.
		maxBytes, err := asPositiveU64String(delivery, "max_output_bytes")
		if err != nil {
			return nil, rpc.InvalidParamsError("delivery.max_output_bytes is invalid")
		}
		maxFiles, err := asUint64(delivery, "max_open_files")
		if err != nil {
			return nil, rpc.InvalidParamsError("delivery.max_open_files must be u32")
		}
		bytes, present, err := readerMetadata(reader)
		if err != nil {
			return nil, readError(err)
		}
		if !present {
			return boundedResult(map[string]any{"method": method, "present": false})
		}
		facts, herr := MetadataOutput(path, bytes, policy, maxBytes, uint32(maxFiles))
		if herr != nil {
			return nil, herr
		}
		return boundedResult(map[string]any{"method": method, "present": true, "output": facts})
	}
	return nil, rpc.InvalidParamsError("delivery.mode must be inline or file")
}

func policyByName(name string) iprangedb.PublicationPolicy {
	switch name {
	case "fail_if_exists":
		return iprangedb.PolicyFailIfExists
	case "replace_existing_no_rollback":
		return iprangedb.PolicyReplaceExistingNoRollback
	default:
		return iprangedb.PolicyReplaceExisting
	}
}

func readerMetadata(reader *rpc.ReaderValue) ([]byte, bool, error) {
	if reader.Live != nil {
		return reader.Live.MetadataJSON()
	}
	return reader.Immutable.MetadataJSON()
}

// FeedSnapshot is one in-memory projection of the numeric feed
// catalog: `(index, name)` pairs in ascending feed-index order. Built
// once per page/stream so structured enumeration never re-scans the
// catalog for every record.
type FeedSnapshot struct {
	Feeds []FeedIndexName
}

// FeedIndexName is one catalog entry.
type FeedIndexName struct {
	Index uint32
	Name  string
}

// BuildFeedSnapshot sweeps the catalog once into an ordered name
// snapshot.
func BuildFeedSnapshot(reader *rpc.ReaderValue) (*FeedSnapshot, *rpc.HandlerError) {
	var cursor *iprangedb.FeedCursor
	var err error
	if reader.Live != nil {
		cursor, err = reader.Live.FeedCursor()
	} else {
		cursor, err = reader.Immutable.FeedCursor()
	}
	if err != nil {
		return nil, readError(err)
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

// ThreatFeedNames maps the membership bitmap of one structured record
// to catalog-ordered feed names.
func ThreatFeedNames(view iprangedb.NetworkEnrichmentV1View, snapshot *FeedSnapshot, words []uint64) ([]string, *rpc.HandlerError) {
	membership, found, err := view.ThreatMembership()
	if err != nil {
		return nil, readError(err)
	}
	if !found {
		return nil, nil
	}
	if len(snapshot.Feeds) == 0 {
		return nil, nil
	}
	lastFeed := snapshot.Feeds[len(snapshot.Feeds)-1].Index
	canonicalWords, err := membership.WordCount()
	if err != nil {
		return nil, readError(err)
	}
	needed := uint64(lastFeed)/64 + 1
	if needed > uint64(canonicalWords) {
		needed = uint64(canonicalWords)
	}
	if uint64(len(words)) < needed {
		words = append(words, make([]uint64, needed-uint64(len(words)))...)
	}
	words = words[:needed]
	read, err := membership.ReadWords(0, words)
	if err != nil {
		return nil, readError(err)
	}
	var feeds []string
	for wordIndex := 0; wordIndex < read; wordIndex++ {
		word := words[wordIndex]
		for word != 0 {
			bit := uint64(0)
			for i := uint64(0); i < 64; i++ {
				if word&(1<<i) != 0 {
					bit = i
					break
				}
			}
			index := uint64(wordIndex)*64 + bit
			if index <= uint64(^uint32(0)) {
				_, name := snapshotFeedAt(snapshot, uint32(index))
				if name != "" {
					feeds = append(feeds, name)
				}
			}
			word &= word - 1
		}
	}
	return feeds, nil
}

func snapshotFeedAt(snapshot *FeedSnapshot, index uint32) (bool, string) {
	for _, entry := range snapshot.Feeds {
		if entry.Index == index {
			return true, entry.Name
		}
	}
	return false, ""
}

// ParseAddress parses canonical IP text into a cursor checkpoint.
func ParseAddress(address string) (*rpc.CursorPoint, *rpc.HandlerError) {
	parsed, err := netip.ParseAddr(address)
	if err != nil || parsed.String() != address {
		return nil, rpc.InvalidParamsError("address is not canonical IP text: " + address)
	}
	var point rpc.CursorPoint
	if parsed.Is4() {
		value := parsed.As4()
		v := uint32(value[0])<<24 | uint32(value[1])<<16 | uint32(value[2])<<8 | uint32(value[3])
		point.V4 = &v
	} else {
		addr16 := parsed.As16()
		hi := uint64(addr16[0])<<56 | uint64(addr16[1])<<48 | uint64(addr16[2])<<40 | uint64(addr16[3])<<32 |
			uint64(addr16[4])<<24 | uint64(addr16[5])<<16 | uint64(addr16[6])<<8 | uint64(addr16[7])
		lo := uint64(addr16[8])<<56 | uint64(addr16[9])<<48 | uint64(addr16[10])<<40 | uint64(addr16[11])<<32 |
			uint64(addr16[12])<<24 | uint64(addr16[13])<<16 | uint64(addr16[14])<<8 | uint64(addr16[15])
		v6 := iprangedb.IPv6FromHalves(hi, lo)
		point.V6 = &v6
	}
	return &point, nil
}
