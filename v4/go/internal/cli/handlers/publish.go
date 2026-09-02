// `iprange.v1.current.publish`: immutable current-feed publication
// (Rust handlers/publish.rs parity).
//
// The handler normalizes one or more released legacy-compatible text or
// binary inputs through the streaming parser into one fresh immutable
// membership file with the named feed, applies the requested metadata
// replacement inside the draft, and publishes under the requested
// policy. Input failures abort the draft and preserve the prior
// generation (spec "Publisher mutation methods").

package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// RegisterPublish installs the current.publish handler family.
func RegisterPublish() {
	rpc.Register("iprange.v1.current.publish", ValidateCurrentPublish, CurrentPublish)
}

// ValidateCurrentPublish enforces the strict current.publish params
// schema (v4/cli/schema/methods.py iprange.v1.current.publish).
func ValidateCurrentPublish(params json.RawMessage) error {
	object, err := exactObject(params,
		"input", "feed", "value_tag", "metadata", "destination",
		"publication_policy", "immutable_feed_budget")
	if err != nil {
		return err
	}
	if err := validateTextInput(object, "input"); err != nil {
		return err
	}
	feed, err := asString(object, "feed")
	if err != nil {
		return fmt.Errorf("feed must be a string")
	}
	if !feedNameGrammarValid(feed) {
		return fmt.Errorf("feed does not use the v4 FeedName grammar")
	}
	if err := validateValueTag(object["value_tag"]); err != nil {
		return err
	}
	if err := validateMetadataReplacement(object["metadata"]); err != nil {
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
		return fmt.Errorf("publication_policy is invalid")
	}
	if !validPublicationPolicyName(policy) {
		return fmt.Errorf("publication_policy is invalid")
	}
	return validateImmutableBudget(object["immutable_feed_budget"])
}

// feedNameGrammarValid enforces the v4 FeedName grammar: 1 through 255
// lowercase ASCII alphanumerics with '_', '-', '.' inside (Rust
// publish.rs validate_feed).
func feedNameGrammarValid(feed string) bool {
	bytes := []byte(feed)
	if len(bytes) < 1 || len(bytes) > 255 {
		return false
	}
	edge := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
	}
	if !edge(bytes[0]) || !edge(bytes[len(bytes)-1]) {
		return false
	}
	for _, b := range bytes {
		if !(edge(b) || b == '_' || b == '-' || b == '.') {
			return false
		}
	}
	return true
}

// CurrentPublish implements the immutable current-feed publication.
func CurrentPublish(st *rpc.SessionState, params json.RawMessage) (any, *rpc.HandlerError) {
	object, err := decodeObject(params)
	if err != nil {
		return nil, rpc.InvalidParamsError("params must be an object")
	}
	input, herr := textInputParams(object["input"])
	if herr != nil {
		return nil, herr
	}
	destination, err := asString(object, "destination")
	if err != nil {
		return nil, rpc.InvalidParamsError("destination must be a string")
	}
	if herr := requirePublicationParent(destination); herr != nil {
		return nil, herr
	}
	metadataObject, err := memberObject(object, "metadata")
	if err != nil {
		return nil, rpc.InvalidParamsError("metadata must be an object")
	}
	metadata, herr := MetadataValueFromObject(metadataObject)
	if herr != nil {
		return nil, herr
	}
	var metadataJSON []byte
	switch {
	case metadata.Keep, metadata.Clear:
		// No metadata is staged for keep/clear on a fresh immutable
		// output (Rust MetadataValue::Clear => None).
	default:
		// A non-nil empty slice still stages an empty metadata blob:
		// empty bytes are a distinct valid value under the v4 contract.
		metadataJSON = make([]byte, len(metadata.Bytes))
		copy(metadataJSON, metadata.Bytes)
	}
	feedText, err := asString(object, "feed")
	if err != nil {
		return nil, rpc.InvalidParamsError("feed must be a string")
	}
	feed, err := iprangedb.NewFeedName(feedText)
	if err != nil {
		return nil, rpc.InvalidParamsError("feed is invalid")
	}
	valueTag, herr := decodeValueTag(object, "value_tag")
	if herr != nil {
		return nil, herr
	}
	policy, herr := decodePublicationPolicy(object["publication_policy"])
	if herr != nil {
		return nil, herr
	}
	budget, herr := immutableBudget(object["immutable_feed_budget"])
	if herr != nil {
		return nil, herr
	}
	// The parser is family-specific: drain the exact source contract of
	// the selected family (Rust TextInputSource::<Ipv4Key|Ipv6Key>).
	var result iprangedb.ImmutableFeedResult
	var sourceCode, sourceMessage string
	token := st.Token()
	switch input.Family {
	case fileio.AddressFamilyInputIPv4:
		source, serr := fileio.NewTextInputSource4(input.Paths, input.Options, input.ExpandAtPaths, input.MaxExpandedPaths)
		if serr != nil {
			return nil, inputError(serr)
		}
		result, err = iprangedb.CreateImmutableFeedV4(destination, valueTag, feed, metadataJSON, policy, source, &budget, token)
		sourceCode = source.LastInputErrorCode()
		sourceMessage = source.LastInputErrorMessage()
	case fileio.AddressFamilyInputIPv6:
		source, serr := fileio.NewTextInputSource6(input.Paths, input.Options, input.ExpandAtPaths, input.MaxExpandedPaths)
		if serr != nil {
			return nil, inputError(serr)
		}
		result, err = iprangedb.CreateImmutableFeedV6(destination, valueTag, feed, metadataJSON, policy, source, &budget, token)
		sourceCode = source.LastInputErrorCode()
		sourceMessage = source.LastInputErrorMessage()
	default:
		return nil, rpc.InvalidParamsError("input.family is invalid")
	}
	if err != nil {
		var failure *iprangedb.ImmutableFeedPreparationFailure
		if ok := asPreparationFailure(err, &failure); ok {
			return nil, preparationError(failure, sourceCode, sourceMessage)
		}
		return nil, rpc.InvalidParamsError(err.Error())
	}
	return publicationSuccess(result)
}

// asPreparationFailure type-asserts one SDK error to the preparation
// failure surface.
func asPreparationFailure(err error, target **iprangedb.ImmutableFeedPreparationFailure) bool {
	failure, ok := err.(*iprangedb.ImmutableFeedPreparationFailure)
	if ok {
		*target = failure
	}
	return ok
}

// requirePublicationParent enforces that the destination has a file
// name inside an existing directory before any SDK work runs (Rust
// publish.rs require_publication_parent).
func requirePublicationParent(destination string) *rpc.HandlerError {
	base := filepath.Base(destination)
	if base == "" || base == "." || base == ".." || base == "/" {
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("publication destination has no file name: %s", destination))
	}
	parent := filepath.Dir(destination)
	if parent == "" {
		parent = "."
	}
	info, err := os.Stat(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return rpc.NewHandlerError("invalid_path", "not_started",
				fmt.Sprintf("publication parent does not exist: %s", parent))
		}
		return rpc.NewHandlerError("io", "not_started",
			fmt.Sprintf("inspect publication parent %s: %v", parent, err))
	}
	if !info.IsDir() {
		return rpc.NewHandlerError("invalid_path", "not_started",
			fmt.Sprintf("publication parent is not a directory: %s", parent))
	}
	return nil
}

// publicationSuccess converts one completed immutable feed result; a
// publish that did not complete (refused or unprovable) is a product
// error whose details keep the complete report and publication facts
// (Rust publish.rs publication_success).
func publicationSuccess(result iprangedb.ImmutableFeedResult) (any, *rpc.HandlerError) {
	publication, herr := PublicationResultJSON(&result.Publication)
	if herr != nil {
		return nil, herr
	}
	report := immutableFeedReportJSON(&result.Report)
	if result.Publication.Publication != iprangedb.PublicationPublished || result.Publication.Cause != nil {
		code := "io"
		message := "immutable feed publication did not complete"
		if cause := result.Publication.Cause; cause != nil {
			if typed, ok := cause.(*iprangedb.Error); ok {
				code = sdkCode(typed.Code)
				message = typed.Detail
			} else {
				message = cause.Error()
			}
		}
		return nil, &rpc.HandlerError{
			Code:    code,
			Outcome: PublicationStatusName(result.Publication.Publication),
			Message: message,
			Details: map[string]any{
				"report":      report,
				"publication": publication,
			},
		}
	}
	return boundedResult(map[string]any{
		"method":      "iprange.v1.current.publish",
		"report":      report,
		"publication": publication,
	})
}

// immutableFeedReportJSON converts the SDK report to its wire object
// (results.py IMMUTABLE_FEED_REPORT).
func immutableFeedReportJSON(report *iprangedb.ImmutableFeedReport) map[string]any {
	return map[string]any{
		"input_record_count":        DecimalUint(report.InputRecordCount),
		"normalized_interval_count": DecimalUint(report.NormalizedIntervalCount),
		"addresses":                 report.Addresses.String(),
	}
}

// preparationError converts one preparation failure; when the input
// source itself failed, the source code/message win and the outcome is
// not_started; a discarded private attempt reports not_published (Rust
// publish.rs preparation_error, collapsed Go failure surface).
func preparationError(failure *iprangedb.ImmutableFeedPreparationFailure, sourceCode, sourceMessage string) *rpc.HandlerError {
	code := "io"
	message := "immutable feed preparation failed"
	outcome := "not_started"
	if sourceCode != "" {
		code = sourceCode
		message = sourceMessage
	} else {
		if typed, ok := failure.Cause.(*iprangedb.Error); ok {
			code = sdkCode(typed.Code)
			message = typed.Detail
		} else {
			message = failure.Cause.Error()
		}
		if failure.Cleanup == iprangedb.CleanupStateResiduePossible {
			outcome = "not_published"
		}
	}
	details := map[string]any{
		"cleanup_state": cleanupStateName(failure.Cleanup),
	}
	return &rpc.HandlerError{Code: code, Outcome: outcome, Message: message, Details: details}
}

// inputError converts one streaming input failure to the handler error
// (Rust publish.rs input_error: the input code wins, not_started).
func inputError(err error) *rpc.HandlerError {
	typed, ok := err.(*fileio.InputError)
	if !ok {
		return rpc.NewHandlerError("input_format", "not_started", err.Error())
	}
	return rpc.NewHandlerError(typed.Code(), "not_started", typed.Error())
}

// parsedTextInput is the decoded text-input descriptor (Rust
// text_input_params).
type parsedTextInput struct {
	Paths            []string
	Options          fileio.TextInputOptions
	ExpandAtPaths    bool
	MaxExpandedPaths int
	Family           fileio.AddressFamilyInput
}

// textInputParams decodes the strict `input` object into parser
// options after the validator checked its shape.
func textInputParams(raw json.RawMessage) (*parsedTextInput, *rpc.HandlerError) {
	object, err := decodeObject(raw)
	if err != nil {
		return nil, rpc.InvalidParamsError("input must be an object")
	}
	paths, err := asStringArray(object, "paths")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.paths must be an array of strings")
	}
	familyText, err := asString(object, "family")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.family is invalid")
	}
	var family fileio.AddressFamilyInput
	switch familyText {
	case "ipv4":
		family = fileio.AddressFamilyInputIPv4
	case "ipv6":
		family = fileio.AddressFamilyInputIPv6
	default:
		return nil, rpc.InvalidParamsError("input.family is invalid")
	}
	fixNetwork, err := asBool(object, "fix_network")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.fix_network must be boolean")
	}
	prefix, err := asUint32(object, "default_prefix")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.default_prefix must be u32")
	}
	dns, err := memberObject(object, "dns")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.dns must be an object")
	}
	threads, err := asUint64(dns, "threads")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.dns.threads must be u32")
	}
	silent, err := asBool(dns, "silent")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.dns.silent must be boolean")
	}
	maxLineBytes, err := asUint32(object, "max_line_bytes")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.max_line_bytes must be u32")
	}
	maxExpandedPaths, err := asUint32(object, "max_expanded_paths")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.max_expanded_paths must be u32")
	}
	expandAtPaths, err := asBool(object, "expand_at_paths")
	if err != nil {
		return nil, rpc.InvalidParamsError("input.expand_at_paths must be boolean")
	}
	return &parsedTextInput{
		Paths: paths,
		Options: fileio.TextInputOptions{
			Family:        family,
			FixNetwork:    fixNetwork,
			DefaultPrefix: prefix,
			DNSThreads:    int(threads),
			DNSSilent:     silent,
			MaxLineBytes:  int(maxLineBytes),
		},
		ExpandAtPaths:    expandAtPaths,
		MaxExpandedPaths: int(maxExpandedPaths),
		Family:           family,
	}, nil
}

// validateTextInput enforces the strict TEXT_INPUT schema (Rust
// publish.rs validate_text_input).
func validateTextInput(object rawObject, name string) error {
	input, err := memberObject(object, name)
	if err != nil {
		return fmt.Errorf("input must be an object")
	}
	if err := exactObjectRaw(input,
		"paths", "family", "fix_network", "default_prefix", "dns",
		"expand_at_paths", "max_line_bytes", "max_expanded_paths"); err != nil {
		return err
	}
	paths, err := asStringArray(input, "paths")
	if err != nil {
		return fmt.Errorf("input.paths must be an array of strings")
	}
	if len(paths) == 0 {
		return fmt.Errorf("input.paths must contain at least one path")
	}
	for _, path := range paths {
		if err := validatePath(path); err != nil {
			return err
		}
	}
	family, err := asString(input, "family")
	if err != nil {
		return fmt.Errorf("input.family must be ipv4 or ipv6")
	}
	maxPrefix := uint32(32)
	switch family {
	case "ipv4":
	case "ipv6":
		maxPrefix = 128
	default:
		return fmt.Errorf("input.family must be ipv4 or ipv6")
	}
	if _, err := asBool(input, "fix_network"); err != nil {
		return fmt.Errorf("input.fix_network must be boolean")
	}
	prefix, err := asUint32(input, "default_prefix")
	if err != nil {
		return fmt.Errorf("input.default_prefix must be u32")
	}
	if prefix > maxPrefix {
		return fmt.Errorf("input.default_prefix is outside the selected family range")
	}
	dns, err := memberObject(input, "dns")
	if err != nil {
		return fmt.Errorf("input.dns must be an object")
	}
	if err := exactObjectRaw(dns, "threads", "silent"); err != nil {
		return err
	}
	threads, err := asUint64(dns, "threads")
	if err != nil {
		return fmt.Errorf("input.dns.threads must be u32")
	}
	if threads == 0 || threads > 2147483647 {
		return fmt.Errorf("input.dns.threads must be 1 through 2147483647")
	}
	if _, err := asBool(dns, "silent"); err != nil {
		return fmt.Errorf("input.dns.silent must be boolean")
	}
	if _, err := asBool(input, "expand_at_paths"); err != nil {
		return fmt.Errorf("input.expand_at_paths must be boolean")
	}
	line, err := asUint32(input, "max_line_bytes")
	if err != nil {
		return fmt.Errorf("input.max_line_bytes must be u32")
	}
	if line == 0 || line > 1_048_576 {
		return fmt.Errorf("input.max_line_bytes must be 1 through 1048576")
	}
	expanded, err := asUint32(input, "max_expanded_paths")
	if err != nil {
		return fmt.Errorf("input.max_expanded_paths must be u32")
	}
	if expanded == 0 || expanded > 1_000_000 {
		return fmt.Errorf("input.max_expanded_paths must be 1 through 1000000")
	}
	return nil
}

func containsNUL(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == 0 {
			return true
		}
	}
	return false
}

// validateMetadataReplacement enforces METADATA_REPLACEMENT_INPUT (no
// keep): clear, replace_utf8, replace_base64, or replace_file (Rust
// lifecycle.rs validate_metadata with allow_keep=false).
func validateMetadataReplacement(raw json.RawMessage) error {
	object, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("metadata must be an object")
	}
	mode, err := asString(object, "mode")
	if err != nil {
		return fmt.Errorf("metadata.mode is invalid for this method")
	}
	switch mode {
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
		return validateBase64Strict(text)
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
	return fmt.Errorf("metadata.mode is invalid for this method")
}

// validateBase64Strict enforces canonical RFC 4648 base64 with the
// released padding rules and zero trailing bits (Rust lifecycle.rs
// decode_base64); Go's stdlib decoder accepts non-canonical padding
// bits, so the wire validator is the strict authority.
func validateBase64Strict(value string) error {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	if len(value)%4 != 0 {
		return fmt.Errorf("base64 length must be a multiple of four")
	}
	bytes := []byte(value)
	chunkCount := len(bytes) / 4
	for index := 0; index < chunkCount; index++ {
		chunk := bytes[index*4 : index*4+4]
		last := index == chunkCount-1
		padding := 0
		for i := len(chunk) - 1; i >= 0 && chunk[i] == '='; i-- {
			padding++
		}
		if padding > 2 || (last && padding == 0 && len(bytes) == 0) {
			return fmt.Errorf("base64 padding is invalid")
		}
		if !last && padding != 0 {
			return fmt.Errorf("base64 padding is not at the end")
		}
		var word uint32
		for position, b := range chunk {
			var digit uint32
			if b == '=' {
				if !last || position < 4-padding {
					return fmt.Errorf("base64 padding is invalid")
				}
				digit = 0
			} else {
				indexInAlphabet := -1
				for i := 0; i < 64; i++ {
					if alphabet[i] == b {
						indexInAlphabet = i
						break
					}
				}
				if indexInAlphabet < 0 {
					return fmt.Errorf("base64 uses the standard alphabet only")
				}
				digit = uint32(indexInAlphabet)
			}
			word = word<<6 | digit
		}
		if padding > 0 {
			if word&((1<<(padding*8))-1) != 0 {
				return fmt.Errorf("base64 has non-canonical trailing bits")
			}
		}
	}
	return nil
}

// canonicalUint64 parses a canonical unsigned decimal string ("0" or
// digits without a leading zero) into a u64 (Rust reader.rs
// u64_string; methods.py decimal_max).
func canonicalUint64(text string) (uint64, error) {
	if text == "0" {
		return 0, nil
	}
	if text == "" || !allASCIIDigits(text) || text[0] == '0' {
		return 0, fmt.Errorf("value must be a canonical unsigned decimal string")
	}
	var value uint64
	for i := 0; i < len(text); i++ {
		next := value*10 + uint64(text[i]-'0')
		if next < value {
			return 0, fmt.Errorf("value must be a canonical unsigned decimal string")
		}
		value = next
	}
	return value, nil
}

// positiveU64String parses a positive canonical u64 (Rust
// positive_u64_string).
func positiveDecimalU64(text string) (uint64, error) {
	value, err := canonicalUint64(text)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return 0, fmt.Errorf("value must be a positive canonical unsigned decimal string")
	}
	return value, nil
}

// positiveU32Value validates one positive u32 JSON integer (Rust
// positive_u32).
func positiveU32Value(value uint64) (uint32, error) {
	if value == 0 || value > 0xffffffff {
		return 0, fmt.Errorf("value must be a positive u32 integer")
	}
	return uint32(value), nil
}

// validateImmutableBudget enforces the strict immutable_feed_budget
// schema (Rust publish.rs validate_immutable_budget).
func validateImmutableBudget(raw json.RawMessage) error {
	budget, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("immutable_feed_budget must be an object")
	}
	if err := exactObjectRaw(budget, "max_heap_bytes", "max_output_pages", "max_workspace_pages", "max_open_files"); err != nil {
		return err
	}
	for _, field := range []string{"max_heap_bytes", "max_output_pages", "max_workspace_pages"} {
		text, err := asString(budget, field)
		if err != nil {
			return fmt.Errorf("immutable_feed_budget.%s: value must be a positive canonical unsigned decimal string", field)
		}
		if _, err := positiveDecimalU64(text); err != nil {
			return fmt.Errorf("immutable_feed_budget.%s: %v", field, err)
		}
	}
	files, err := asUint64(budget, "max_open_files")
	if err != nil {
		return fmt.Errorf("immutable_feed_budget.max_open_files: value must be a positive u32 integer")
	}
	if _, err := positiveU32Value(files); err != nil {
		return fmt.Errorf("immutable_feed_budget.max_open_files: value must be a positive u32 integer")
	}
	return nil
}

// immutableBudget decodes the validated budget into its SDK value
// (Rust publish.rs immutable_budget).
func immutableBudget(raw json.RawMessage) (iprangedb.ImmutableFeedBudget, *rpc.HandlerError) {
	budget, err := decodeObject(raw)
	if err != nil {
		return iprangedb.ImmutableFeedBudget{}, rpc.InvalidParamsError("immutable_feed_budget must be an object")
	}
	decode := func(field string) (uint64, *rpc.HandlerError) {
		text, err := asDecimalString(budget, field)
		if err != nil {
			return 0, rpc.InvalidParamsError(fmt.Sprintf("immutable_feed_budget.%s is invalid", field))
		}
		value, err := canonicalUint64(text)
		if err != nil {
			return 0, rpc.InvalidParamsError(fmt.Sprintf("immutable_feed_budget.%s is invalid", field))
		}
		return value, nil
	}
	maxHeap, herr := decode("max_heap_bytes")
	if herr != nil {
		return iprangedb.ImmutableFeedBudget{}, herr
	}
	maxOutput, herr := decode("max_output_pages")
	if herr != nil {
		return iprangedb.ImmutableFeedBudget{}, herr
	}
	maxWorkspace, herr := decode("max_workspace_pages")
	if herr != nil {
		return iprangedb.ImmutableFeedBudget{}, herr
	}
	files, err := asUint64(budget, "max_open_files")
	if err != nil {
		return iprangedb.ImmutableFeedBudget{}, rpc.InvalidParamsError("max_open_files must be u32")
	}
	if files > 0xffffffff {
		return iprangedb.ImmutableFeedBudget{}, rpc.InvalidParamsError("max_open_files must be u32")
	}
	return iprangedb.ImmutableFeedBudget{
		MaxHeapBytes:      maxHeap,
		MaxOutputPages:    maxOutput,
		MaxWorkspacePages: maxWorkspace,
		MaxOpenFiles:      uint32(files),
	}, nil
}

// decodePublicationPolicy maps the wire policy name to the SDK value
// (Rust reader.rs publication_policy).
func decodePublicationPolicy(raw json.RawMessage) (iprangedb.PublicationPolicy, *rpc.HandlerError) {
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return iprangedb.PolicyFailIfExists, rpc.InvalidParamsError("publication_policy is invalid")
	}
	switch name {
	case "fail_if_exists":
		return iprangedb.PolicyFailIfExists, nil
	case "replace_existing":
		return iprangedb.PolicyReplaceExisting, nil
	case "replace_existing_no_rollback":
		return iprangedb.PolicyReplaceExistingNoRollback, nil
	}
	return iprangedb.PolicyFailIfExists, rpc.InvalidParamsError("publication_policy is invalid")
}
