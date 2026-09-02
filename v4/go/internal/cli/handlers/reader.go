// Reader and database read-only JSON-RPC handlers (Rust
// handlers/reader.rs parity, iprange-jsonrpc-v1.md reader family):
// connection reader handles, point lookups, metadata delivery, and the
// ephemeral database.info / database.metadata.get methods. Every
// handler holds the session lock, so connection maps need no extra
// synchronization.

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// ReaderLimit is the per-connection reader handle bound (spec limits).
const ReaderLimit = 64

// connectionReader is the shared immutable/live SDK reader surface the
// read-only handlers use; both public reader kinds implement it.
type connectionReader interface {
	Info() (iprangedb.DatabaseInfo, error)
	LookupDirectV4(ip iprangedb.IPv4) (uint32, bool, error)
	LookupDirectV6(ip iprangedb.IPv6) (uint32, bool, error)
	MembershipQuery() (*iprangedb.MembershipQuery, error)
	Pin() (*iprangedb.Pin, error)
	FeedCursor() (*iprangedb.FeedCursor, error)
	DirectCursorV4(direction iprangedb.RangeDirection) (*iprangedb.DirectCursorV4, error)
	DirectCursorV6(direction iprangedb.RangeDirection) (*iprangedb.DirectCursorV6, error)
	FeedRangeCursorV4(name string, direction iprangedb.RangeDirection) (*iprangedb.FeedRangeCursorV4, error)
	FeedRangeCursorV6(name string, direction iprangedb.RangeDirection) (*iprangedb.FeedRangeCursorV6, error)
	NetworkEnrichmentV1CursorV4(direction iprangedb.RangeDirection) (*iprangedb.NetworkEnrichmentV1CursorV4, error)
	NetworkEnrichmentV1CursorV6(direction iprangedb.RangeDirection) (*iprangedb.NetworkEnrichmentV1CursorV6, error)
}

// sdkSurface returns the SDK reader behind one connection handle.
func sdkSurface(reader *rpc.ReaderValue) connectionReader {
	if reader.Live != nil {
		return reader.Live
	}
	return reader.Immutable
}

// readerInfo converts one connection reader's logical identity.
func readerInfo(reader *rpc.ReaderValue) (iprangedb.DatabaseInfo, *rpc.HandlerError) {
	return sdk(sdkSurface(reader).Info())
}

// sdkErr converts an SDK error-only result of a read-only operation.
func sdkErr(err error) *rpc.HandlerError {
	if err != nil {
		return readError(err)
	}
	return nil
}

// RegisterReader installs the reader-family and database read-only
// methods. The lead calls it from register.go's RegisterAll.
func RegisterReader() {
	rpc.Register("iprange.v1.reader.open", ValidateReaderOpen, ReaderOpen)
	rpc.Register("iprange.v1.reader.close", ValidateReaderHandle, ReaderClose)
	rpc.Register("iprange.v1.reader.info", ValidateReaderHandle, ReaderInfo)
	rpc.Register("iprange.v1.reader.metadata", ValidateReaderMetadata, ReaderMetadata)
	rpc.Register("iprange.v1.reader.lookup", ValidateLookup, ReaderLookup)
	rpc.Register("iprange.v1.reader.matching_feeds", ValidateMatchingFeeds, ReaderMatchingFeeds)
	rpc.Register("iprange.v1.database.info", ValidateDatabaseInfo, DatabaseInfo)
	rpc.Register("iprange.v1.database.metadata.get", ValidateDatabaseMetadata, DatabaseMetadataGet)
}

// ---------------------------------------------------------------------------
// Validators
// ---------------------------------------------------------------------------

// validateSourceObject enforces one DATABASE_SOURCE object (methods.py):
// exactly path and mode, path bounds, mode immutable|live.
func validateSourceObject(source rawObject) error {
	if err := exactObjectRaw(source, "path", "mode"); err != nil {
		return err
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
	switch mode {
	case "immutable", "live":
		return nil
	}
	return fmt.Errorf("source.mode must be immutable or live")
}

// validateSourceParam enforces the single `source` member of the
// reader.open / database.info params.
func validateSourceParam(params json.RawMessage) error {
	object, err := exactObject(params, "source")
	if err != nil {
		return err
	}
	source, err := memberObject(object, "source")
	if err != nil {
		return err
	}
	return validateSourceObject(source)
}

// ValidateReaderOpen enforces the reader.open params (one source).
func ValidateReaderOpen(params json.RawMessage) error {
	return validateSourceParam(params)
}

// ValidateDatabaseInfo enforces the database.info params (one source).
func ValidateDatabaseInfo(params json.RawMessage) error {
	return validateSourceParam(params)
}

// ValidateReaderHandle enforces the handle-only params of
// reader.close and reader.info.
func ValidateReaderHandle(params json.RawMessage) error {
	object, err := exactObject(params, "reader")
	if err != nil {
		return err
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return err
	}
	return validateHandle(handle)
}

// validateDeliveryObject re-checks the strict delivery schema after the
// shared ValidateDelivery shape check: max_output_bytes is a positive
// canonical u64 decimal string and max_open_files is a positive u32
// integer (methods.py METADATA_DELIVERY).
func validateDeliveryObject(object rawObject) error {
	if err := ValidateDelivery(object); err != nil {
		return err
	}
	delivery, err := memberObject(object, "delivery")
	if err != nil {
		return fmt.Errorf("delivery must be an object")
	}
	mode, err := asString(delivery, "mode")
	if err != nil {
		return nil // ValidateDelivery already rejected the shape
	}
	if mode != "file" {
		return nil
	}
	text, err := asDecimalString(delivery, "max_output_bytes")
	if err != nil {
		return fmt.Errorf("delivery.max_output_bytes must be a positive canonical decimal string")
	}
	bytesLimit, herr := parseCanonicalU64(text)
	if herr != nil {
		return fmt.Errorf("delivery.max_output_bytes must be a positive canonical decimal string")
	}
	if bytesLimit == 0 {
		return fmt.Errorf("delivery.max_output_bytes must be positive")
	}
	files, err := asUint64(delivery, "max_open_files")
	if err != nil || files < 1 || files > 0xffffffff {
		return fmt.Errorf("delivery.max_open_files must be a positive u32")
	}
	return nil
}

// ValidateReaderMetadata enforces the reader.metadata params (reader
// handle plus one delivery).
func ValidateReaderMetadata(params json.RawMessage) error {
	object, err := exactObject(params, "reader", "delivery")
	if err != nil {
		return err
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return err
	}
	if err := validateHandle(handle); err != nil {
		return err
	}
	return validateDeliveryObject(object)
}

// ValidateDatabaseMetadata enforces the database.metadata.get params
// (one source plus one delivery).
func ValidateDatabaseMetadata(params json.RawMessage) error {
	object, err := exactObject(params, "source", "delivery")
	if err != nil {
		return err
	}
	source, err := memberObject(object, "source")
	if err != nil {
		return err
	}
	if err := validateSourceObject(source); err != nil {
		return err
	}
	return validateDeliveryObject(object)
}

// ValidateLookup enforces the reader.lookup params: one reader handle
// and 1 through 4096 canonical addresses.
func ValidateLookup(params json.RawMessage) error {
	object, err := exactObject(params, "reader", "addresses")
	if err != nil {
		return err
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return err
	}
	if err := validateHandle(handle); err != nil {
		return err
	}
	addresses, err := asStringArray(object, "addresses")
	if err != nil {
		return err
	}
	if len(addresses) == 0 || len(addresses) > 4096 {
		return fmt.Errorf("addresses must contain 1 through 4096 values")
	}
	for _, address := range addresses {
		if _, herr := ParseAddress(address); herr != nil {
			return fmt.Errorf("address is not canonical IP text: %s", address)
		}
	}
	return nil
}

// ValidateMatchingFeeds enforces the reader.matching_feeds params: one
// reader handle and one canonical address.
func ValidateMatchingFeeds(params json.RawMessage) error {
	object, err := exactObject(params, "reader", "address")
	if err != nil {
		return err
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return err
	}
	if err := validateHandle(handle); err != nil {
		return err
	}
	address, err := asString(object, "address")
	if err != nil {
		return err
	}
	if _, herr := ParseAddress(address); herr != nil {
		return fmt.Errorf("address is not canonical IP text: %s", address)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// ReaderOpen opens one source and registers a connection reader handle
// (spec reader.open). Live mode registers and pins one committed
// generation.
func ReaderOpen(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "source")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	source, err := memberObject(object, "source")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	path, err := asString(source, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	if len(st.Resources.Readers) >= ReaderLimit {
		return nil, rpc.NewHandlerError("server_busy", "not_started",
			"connection reader limit 64 is exhausted")
	}
	reader, herr := openReader(path, mode, "database source", st.Token())
	if herr != nil {
		return nil, herr
	}
	info, herr := readerInfo(reader)
	if herr != nil {
		return nil, closeReadersOnError(reader, herr)
	}
	handle, herr := rpc.NewHandle()
	if herr != nil {
		return nil, closeReadersOnError(reader, herr)
	}
	for st.Resources.Readers[handle] != nil || st.Resources.ClosedReaders[handle] {
		handle, herr = rpc.NewHandle()
		if herr != nil {
			return nil, closeReadersOnError(reader, herr)
		}
	}
	st.Resources.Readers[handle] = reader
	return boundedResult(map[string]any{
		"method": "iprange.v1.reader.open",
		"reader": handle,
		"info":   DatabaseInfoJSON(&info),
	})
}

// ReaderClose closes one connection reader (spec reader.close).
// Closing an already-closed or unknown handle is handle_not_found;
// dependent cursors are dropped first and live readers report their
// factual close result as source_close.
func ReaderClose(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	if st.Resources.ClosedReaders[handle] || st.Resources.Readers[handle] == nil {
		return nil, rpc.NewHandlerError("handle_not_found", "not_started",
			"reader handle is unknown or already closed")
	}
	reader := st.Resources.Readers[handle]
	delete(st.Resources.Readers, handle)
	// Drop every cursor of this reader before the close so no cursor
	// can outlive its reader (Rust reader.rs close parity).
	for cursorHandle, cursor := range st.Resources.Cursors {
		if cursor.Reader == handle {
			delete(st.Resources.Cursors, cursorHandle)
			st.Resources.RecordClosedCursor(cursorHandle)
		}
	}
	sourceClose, herr := closeConnectionReader(reader)
	if herr != nil {
		st.Resources.Readers[handle] = reader
		return nil, herr
	}
	st.Resources.RecordClosedReader(handle)
	result := map[string]any{"method": "iprange.v1.reader.close", "closed": true}
	if sourceClose != nil {
		result["source_close"] = sourceClose
	}
	return boundedResult(result)
}

// ReaderInfo returns the complete DatabaseInfo conversion of one
// connection reader.
func ReaderInfo(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	reader, herr := ReaderHandle(st, handle)
	if herr != nil {
		return nil, herr
	}
	info, herr := readerInfo(reader)
	if herr != nil {
		return nil, herr
	}
	return boundedResult(map[string]any{
		"method": "iprange.v1.reader.info",
		"info":   DatabaseInfoJSON(&info),
	})
}

// ReaderMetadata delivers the stored opaque metadata under one delivery
// (spec reader.metadata): inline base64 or an atomically published
// file; absent metadata produces only `present:false`.
func ReaderMetadata(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "reader", "delivery")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	delivery, err := memberObject(object, "delivery")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	reader, herr := ReaderHandle(st, handle)
	if herr != nil {
		return nil, herr
	}
	return metadataDelivery(st, reader, delivery, "iprange.v1.reader.metadata")
}

// metadataDelivery runs the inline preflight, then converts one
// metadata fetch under the strict delivery object. The shared
// MetadataResult helper cannot decode the decimal-string wire values of
// the file branch, so this local conversion is the handler authority
// (Rust reader.rs metadata_result parity).
func metadataDelivery(st *rpc.SessionState, reader *rpc.ReaderValue, delivery rawObject, method string) (any, *rpc.HandlerError) {
	length, present, inline, herr := MetadataDeliveryInlineLen(reader, delivery)
	if herr != nil {
		return nil, herr
	}
	if inline && present {
		if herr := PreflightMetadataInline(st, method, length); herr != nil {
			return nil, herr
		}
	}
	return deliverMetadata(method, reader, delivery)
}

// deliverMetadata converts one metadata fetch under the strict delivery
// object. Callers must run the inline preflight before this materializes
// the metadata blob.
func deliverMetadata(method string, reader *rpc.ReaderValue, delivery rawObject) (any, *rpc.HandlerError) {
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
		maxBytesText, err := asDecimalString(delivery, "max_output_bytes")
		if err != nil {
			return nil, rpc.InvalidParamsError("delivery.max_output_bytes must be a positive canonical decimal string")
		}
		maxBytes, herr := parseCanonicalU64(maxBytesText)
		if herr != nil {
			return nil, rpc.InvalidParamsError("delivery.max_output_bytes must be a positive canonical decimal string")
		}
		if maxBytes == 0 {
			return nil, rpc.InvalidParamsError("delivery.max_output_bytes must be positive")
		}
		maxFiles, err := asUint64(delivery, "max_open_files")
		if err != nil || maxFiles < 1 || maxFiles > 0xffffffff {
			return nil, rpc.InvalidParamsError("delivery.max_open_files must be a positive u32")
		}
		bytes, present, err := readerMetadata(reader)
		if err != nil {
			return nil, readError(err)
		}
		if !present {
			return boundedResult(map[string]any{"method": method, "present": false})
		}
		facts, ferr := MetadataOutput(path, bytes, policy, maxBytes, uint32(maxFiles))
		if ferr != nil {
			return nil, ferr
		}
		return boundedResult(map[string]any{
			"method":  method,
			"present": true,
			"output":  facts,
		})
	}
	return nil, rpc.InvalidParamsError("delivery.mode must be inline or file")
}

// ReaderLookup resolves 1 through 4096 addresses of the reader family
// (spec reader.lookup). Matches carry exactly the semantic value of the
// database kind (direct value, catalog-ordered feeds, or the decoded
// network_enrichment_v1 payload plus catalog-ordered threat feeds).
func ReaderLookup(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "reader", "addresses")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	addresses, err := asStringArray(object, "addresses")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	reader, herr := ReaderHandle(st, handle)
	if herr != nil {
		return nil, herr
	}
	op := sdkSurface(reader)
	info, herr := readerInfo(reader)
	if herr != nil {
		return nil, herr
	}
	cancellation := st.Token()
	matches := make([]any, 0, len(addresses))
	switch info.ValueKind {
	case iprangedb.ValueKindDirect:
		for _, text := range addresses {
			point, herr := ParseAddress(text)
			if herr != nil {
				return nil, herr
			}
			match := map[string]any{"address": text, "present": false}
			value, found, err := lookupDirect(op, point)
			if err != nil {
				return nil, err
			}
			if found {
				match["present"] = true
				match["value"] = value
			}
			matches = append(matches, match)
		}
	case iprangedb.ValueKindMembership:
		query, herr := sdk(op.MembershipQuery())
		if herr != nil {
			return nil, herr
		}
		for _, text := range addresses {
			point, herr := ParseAddress(text)
			if herr != nil {
				return nil, herr
			}
			match := map[string]any{"address": text, "present": false}
			feeds, herr := lookupMembership(query, point, cancellation)
			if herr != nil {
				return nil, herr
			}
			if len(feeds) > 0 {
				match["present"] = true
				match["feeds"] = feeds
			}
			matches = append(matches, match)
		}
	case iprangedb.ValueKindStructured:
		// The enrichment view is valid while its pin remains open, so
		// one pin guards every structured address of the call (Rust
		// reader borrow parity). One catalog snapshot and one reusable
		// membership-word buffer are shared by every address.
		pin, herr := sdk(op.Pin())
		if herr != nil {
			return nil, herr
		}
		defer pin.Close()
		snapshot, herr := BuildFeedSnapshot(reader)
		if herr != nil {
			return nil, herr
		}
		var words []uint64
		for _, text := range addresses {
			point, herr := ParseAddress(text)
			if herr != nil {
				return nil, herr
			}
			match := map[string]any{"address": text, "present": false}
			value, found, herr := lookupStructured(pin, point, snapshot, words)
			if herr != nil {
				return nil, herr
			}
			if found {
				for key, item := range value {
					match[key] = item
				}
				match["present"] = true
			}
			matches = append(matches, match)
		}
	}
	return boundedResult(map[string]any{
		"method":  "iprange.v1.reader.lookup",
		"matches": matches,
	})
}

// lookupDirect resolves one direct point match.
func lookupDirect(op connectionReader, point *rpc.CursorPoint) (uint32, bool, *rpc.HandlerError) {
	var value uint32
	var found bool
	var err error
	if point.V4 != nil {
		value, found, err = op.LookupDirectV4(iprangedb.IPv4(*point.V4))
	} else {
		value, found, err = op.LookupDirectV6(*point.V6)
	}
	if err != nil {
		return 0, false, readError(err)
	}
	return value, found, nil
}

// lookupMembership emits the catalog-ordered feed names covering one
// point through the membership query.
func lookupMembership(query *iprangedb.MembershipQuery, point *rpc.CursorPoint, cancellation *iprangedb.CancellationToken) ([]string, *rpc.HandlerError) {
	var feeds []string
	if point.V4 != nil {
		_, herr := sdk(query.MatchingFeedsV4(iprangedb.IPv4(*point.V4), func(name string) error {
			feeds = append(feeds, name)
			return nil
		}, cancellation))
		return feeds, herr
	}
	_, herr := sdk(query.MatchingFeedsV6(*point.V6, func(name string) error {
		feeds = append(feeds, name)
		return nil
	}, cancellation))
	return feeds, herr
}

// lookupStructured decodes one network_enrichment_v1 point payload
// through the lookup pin. Threat feeds are mapped through the shared
// catalog snapshot.
func lookupStructured(pin *iprangedb.Pin, point *rpc.CursorPoint, snapshot *FeedSnapshot, words []uint64) (map[string]any, bool, *rpc.HandlerError) {
	var view iprangedb.NetworkEnrichmentV1View
	var found bool
	var err error
	if point.V4 != nil {
		view, found, err = pin.LookupNetworkEnrichmentV1V4(iprangedb.IPv4(*point.V4))
	} else {
		view, found, err = pin.LookupNetworkEnrichmentV1V6(*point.V6)
	}
	if err != nil {
		return nil, false, readError(err)
	}
	if !found {
		return nil, false, nil
	}
	feeds, herr := ThreatFeedNames(view, snapshot, &words)
	if herr != nil {
		return nil, false, herr
	}
	value, err := view.Value()
	if err != nil {
		return nil, false, readError(err)
	}
	return NetworkEnrichmentJSON(value, feeds), true, nil
}

// ReaderMatchingFeeds resolves one address against the membership
// surface (spec reader.matching_feeds): direct databases refuse with
// handle_wrong_kind; membership and structured databases report the
// catalog-ordered match set and its count.
func ReaderMatchingFeeds(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "reader", "address")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	handle, err := asString(object, "reader")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	address, err := asString(object, "address")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	point, herr := ParseAddress(address)
	if herr != nil {
		return nil, herr
	}
	reader, herr := ReaderHandle(st, handle)
	if herr != nil {
		return nil, herr
	}
	info, herr := readerInfo(reader)
	if herr != nil {
		return nil, herr
	}
	feeds := make([]string, 0)
	var count uint64
	switch info.ValueKind {
	case iprangedb.ValueKindDirect:
		return nil, rpc.NewHandlerError("handle_wrong_kind", "not_started",
			"matching feeds requires a membership-capable database")
	case iprangedb.ValueKindMembership:
		query, herr := sdk(sdkSurface(reader).MembershipQuery())
		if herr != nil {
			return nil, herr
		}
		// A fresh token mirrors the Rust Default cancellation of this
		// interactive method.
		cancellation := iprangedb.NewCancellationToken()
		var report iprangedb.MatchingFeedsReport
		if point.V4 != nil {
			report, herr = sdk(query.MatchingFeedsV4(iprangedb.IPv4(*point.V4), func(name string) error {
				feeds = append(feeds, name)
				return nil
			}, cancellation))
		} else {
			report, herr = sdk(query.MatchingFeedsV6(*point.V6, func(name string) error {
				feeds = append(feeds, name)
				return nil
			}, cancellation))
		}
		if herr != nil {
			return nil, herr
		}
		count = report.MatchingFeedCount
	case iprangedb.ValueKindStructured:
		pin, herr := sdk(sdkSurface(reader).Pin())
		if herr != nil {
			return nil, herr
		}
		defer pin.Close()
		var view iprangedb.NetworkEnrichmentV1View
		var found bool
		var err error
		if point.V4 != nil {
			view, found, err = pin.LookupNetworkEnrichmentV1V4(iprangedb.IPv4(*point.V4))
		} else {
			view, found, err = pin.LookupNetworkEnrichmentV1V6(*point.V6)
		}
		if err != nil {
			return nil, readError(err)
		}
		if found {
			snapshot, herr := BuildFeedSnapshot(reader)
			if herr != nil {
				return nil, herr
			}
			var words []uint64
			feeds, herr = ThreatFeedNames(view, snapshot, &words)
			if herr != nil {
				return nil, herr
			}
		}
		count = uint64(len(feeds))
	}
	return boundedResult(map[string]any{
		"method":              "iprange.v1.reader.matching_feeds",
		"address":             address,
		"feeds":               feeds,
		"matching_feed_count": DecimalUint(count),
	})
}

// DatabaseInfo opens one ephemeral source reader, reports its complete
// DatabaseInfo, and reports the factual live close as source_close when
// one exists (spec database.info).
func DatabaseInfo(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	path, mode, herr := sourceFromParams(params)
	if herr != nil {
		return nil, herr
	}
	reader, herr := openReader(path, mode, "database source", st.Token())
	if herr != nil {
		return nil, herr
	}
	report, herr := (func() (any, *rpc.HandlerError) {
		info, herr := readerInfo(reader)
		if herr != nil {
			return nil, herr
		}
		return map[string]any{
			"method": "iprange.v1.database.info",
			"info":   DatabaseInfoJSON(&info),
		}, nil
	})()
	if herr != nil {
		return nil, closeReadersOnError(reader, herr)
	}
	return finishEphemeral(reader, report)
}

// DatabaseMetadataGet is reader.metadata without a connection handle
// (spec database.metadata.get): one ephemeral source reader, the same
// delivery, plus the factual live close when one exists.
func DatabaseMetadataGet(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := exactObject(params, "source", "delivery")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	source, err := memberObject(object, "source")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	path, err := asString(source, "path")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	delivery, err := memberObject(object, "delivery")
	if err != nil {
		return nil, rpc.InvalidParamsError(err.Error())
	}
	reader, herr := openReader(path, mode, "database source", st.Token())
	if herr != nil {
		return nil, herr
	}
	report, herr := (func() (any, *rpc.HandlerError) {
		return metadataDelivery(st, reader, delivery, "iprange.v1.database.metadata.get")
	})()
	if herr != nil {
		return nil, closeReadersOnError(reader, herr)
	}
	return finishEphemeral(reader, report)
}

// ---------------------------------------------------------------------------
// Shared reader lifecycle helpers
// ---------------------------------------------------------------------------

// openReader opens one database source; the path is verified before the
// SDK open so a missing path reports invalid_path and an unverifiable
// path reports io (Rust reader.rs open_reader parity).
func openReader(path, mode, label string, cancellation *iprangedb.CancellationToken) (*rpc.ReaderValue, *rpc.HandlerError) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rpc.NewHandlerError("invalid_path", "not_started",
				label+" does not exist: "+path)
		}
		return nil, rpc.NewHandlerError("io", "not_started",
			"cannot inspect "+label+" "+path+": "+err.Error())
	}
	if mode == "immutable" {
		reader, err := iprangedb.OpenImmutable(path)
		if err != nil {
			return nil, readError(err)
		}
		return &rpc.ReaderValue{Immutable: reader}, nil
	}
	reader, err := iprangedb.OpenLiveReader(path, cancellation)
	if err != nil {
		return nil, readError(err)
	}
	return &rpc.ReaderValue{Live: reader}, nil
}

// sourceFromParams decodes the validated single-source params of the
// database.info methods into path and mode.
func sourceFromParams(params json.RawMessage) (string, string, *rpc.HandlerError) {
	object, err := exactObject(params, "source")
	if err != nil {
		return "", "", rpc.InvalidParamsError(err.Error())
	}
	source, err := memberObject(object, "source")
	if err != nil {
		return "", "", rpc.InvalidParamsError(err.Error())
	}
	path, err := asString(source, "path")
	if err != nil {
		return "", "", rpc.InvalidParamsError(err.Error())
	}
	mode, err := asString(source, "mode")
	if err != nil {
		return "", "", rpc.InvalidParamsError(err.Error())
	}
	return path, mode, nil
}

// closeConnectionReader closes one connection reader: live readers
// return the factual close object; immutable readers release their
// mapping and produce no fact. A failed or incomplete live close is an
// error whose details keep the close fact.
func closeConnectionReader(reader *rpc.ReaderValue) (map[string]any, *rpc.HandlerError) {
	if reader.Live != nil {
		result, err := reader.Live.Close()
		if err != nil {
			return nil, readError(err)
		}
		fact := readerCloseFact(result)
		if result.Outcome != iprangedb.CloseOutcomeClosed || result.Cause != nil {
			code := "io"
			var message string
			if result.Cause != nil {
				var typed *iprangedb.Error
				if errors.As(result.Cause, &typed) {
					code = sdkCode(typed.Code)
				}
				message = result.Cause.Error()
			} else {
				message = "live reader close is incomplete"
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
	if err := reader.Immutable.Close(); err != nil {
		return nil, readError(err)
	}
	return nil, nil
}

// readerCloseFact is the wire conversion of one live reader close
// result (Rust reader.rs reader_close_result: cleanup is always empty
// for a reader).
func readerCloseFact(result iprangedb.ReaderCloseResult) map[string]any {
	return map[string]any{
		"outcome":              CloseOutcomeName(result.Outcome),
		"cleanup":              map[string]any{},
		"coordination_cleanup": CoordinationCleanupJSON(result.CoordinationCleanup),
	}
}

// finishEphemeral closes one internally opened reader and returns the
// completed report: live close facts ride as source_close; a close
// failure keeps both the close fact and the completed report in the
// error details (Rust reader.rs finish_ephemeral_reader parity).
func finishEphemeral(reader *rpc.ReaderValue, report any) (any, *rpc.HandlerError) {
	sourceClose, herr := closeConnectionReader(reader)
	if herr != nil {
		return nil, PreserveCompletedReport(herr, report)
	}
	if sourceClose != nil {
		if result, ok := report.(map[string]any); ok {
			result["source_close"] = sourceClose
			return boundedResult(result)
		}
	}
	return boundedResult(report)
}

// closeReadersOnError closes every reader opened by one failed
// read-only method and merges the live close facts into the error
// details. Immutable readers have no close fact but must still release
// their mapping (Rust reader.rs close_on_error parity).
func closeReadersOnError(reader *rpc.ReaderValue, failure *rpc.HandlerError) *rpc.HandlerError {
	if reader.Immutable != nil {
		_ = reader.Immutable.Close()
	}
	return CloseOnError([]*rpc.ReaderValue{reader}, failure)
}

// parseCanonicalU64 parses one canonical unsigned decimal string with
// no sign, separator, leading zero (except "0"), fraction, or exponent
// (Rust reader.rs u64_string parity).
func parseCanonicalU64(text string) (uint64, error) {
	return canonicalU64String(text)
}
