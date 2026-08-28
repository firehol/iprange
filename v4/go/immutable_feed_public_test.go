package iprangedb

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sliceSource4 is one finite caller-owned IPv4 batch source over a
// committed slice (Rust source::SliceSource parity).
type sliceSource4 []AddressRange4

func (s *sliceSource4) NextBatch() ([]AddressRange4, error) {
	if len(*s) == 0 {
		return nil, nil
	}
	batch := *s
	*s = nil
	return batch, nil
}

// sliceSource6 is one finite caller-owned IPv6 batch source.
type sliceSource6 []AddressRange6

func (s *sliceSource6) NextBatch() ([]AddressRange6, error) {
	if len(*s) == 0 {
		return nil, nil
	}
	batch := *s
	*s = nil
	return batch, nil
}

// immutableFeedBudgetFor returns the default test budget (generous
// output and workspace pages, three open files).
func immutableFeedBudgetFor() *ImmutableFeedBudget {
	return &ImmutableFeedBudget{
		MaxHeapBytes:      64 * 1024 * 1024,
		MaxOutputPages:    20_000,
		MaxWorkspacePages: 20_000,
		MaxOpenFiles:      3,
	}
}

func TestImmutableFeedV4PublishesAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "feed.v4")
	valueTag, err := NewValueTag([]byte("downloaded"))
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	feedName, err := NewFeedName("current")
	if err != nil {
		t.Fatalf("feed name: %v", err)
	}
	// Three disjoint ranges plus one overlapping pair: the normalized
	// output must hold the merged intervals.
	source := sliceSource4{
		{From: 0x0a000001, To: 0x0a00000a},
		{From: 0x0a000014, To: 0x0a00001e},
		{From: 0x0a000010, To: 0x0a000018}, // overlaps [0a000014, 0a00001e] and touches the prefix
		{From: 0xb0000000, To: 0xb0000001},
	}
	metadata := []byte(`{"fixture":"immutable-feed-v4"}`)
	result, err := CreateImmutableFeedV4(destination, valueTag, feedName, metadata, PolicyFailIfExists, &source, immutableFeedBudgetFor(), nil)
	if err != nil {
		t.Fatalf("CreateImmutableFeedV4: %v", err)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", result.CleanupState())
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication.Publication)
	}
	if result.Report.InputRecordCount != 4 {
		t.Errorf("input record count = %d, want 4", result.Report.InputRecordCount)
	}
	if result.Report.NormalizedIntervalCount != 3 {
		t.Errorf("normalized interval count = %d, want 3", result.Report.NormalizedIntervalCount)
	}
	// Expected addresses: [0a000001,0a00000a] (10) + merged
	// [0a000010,0a00001e] (15) + [b0000000,b0000001] (2) = 27.
	want := CardinalityFromUint64(27)
	if result.Report.Addresses.Compare(want) != 0 {
		t.Errorf("addresses = %v, want %v", result.Report.Addresses, want)
	}

	reader, err := OpenImmutable(destination)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer reader.Close()
	info, err := reader.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ValueTag != valueTag {
		t.Errorf("value tag = %v, want %v", info.ValueTag, valueTag)
	}
	if got, ok, err := reader.MetadataJSON(); err != nil || !ok || string(got) != string(metadata) {
		t.Errorf("metadata = %q ok=%v err=%v, want %q", got, ok, err, metadata)
	}
	cursor, err := reader.FeedRangeCursorV4("current", RangeDirectionForward)
	if err != nil {
		t.Fatalf("FeedRangeCursorV4: %v", err)
	}
	var got []AddressRange4
	for {
		rng, ok, err := cursor.NextRange()
		if err != nil {
			t.Fatalf("NextRange: %v", err)
		}
		if !ok {
			break
		}
		got = append(got, rng)
	}
	wantRanges := []AddressRange4{
		{From: 0x0a000001, To: 0x0a00000a},
		{From: 0x0a000010, To: 0x0a00001e},
		{From: 0xb0000000, To: 0xb0000001},
	}
	if len(got) != len(wantRanges) {
		t.Fatalf("read ranges = %v, want %v", got, wantRanges)
	}
	for i := range wantRanges {
		if got[i] != wantRanges[i] {
			t.Errorf("range %d = %v, want %v", i, got[i], wantRanges[i])
		}
	}
}

func TestImmutableFeedV6PublishesAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "feed.v6")
	valueTag, err := NewValueTag([]byte("asn6"))
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	feedName, err := NewFeedName("v6")
	if err != nil {
		t.Fatalf("feed name: %v", err)
	}
	source := sliceSource6{
		{FromHi: 0x2001, FromLo: 0x0db8, ToHi: 0x2001, ToLo: 0x0dbf},
		{FromHi: 0x2001, FromLo: 0x0e00, ToHi: 0x2001, ToLo: 0x0eff},
	}
	result, err := CreateImmutableFeedV6(destination, valueTag, feedName, nil, PolicyFailIfExists, &source, immutableFeedBudgetFor(), nil)
	if err != nil {
		t.Fatalf("CreateImmutableFeedV6: %v", err)
	}
	if result.Report.InputRecordCount != 2 || result.Report.NormalizedIntervalCount != 2 {
		t.Errorf("report = %+v, want two input and two normalized", result.Report)
	}
	reader, err := OpenImmutable(destination)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer reader.Close()
	cursor, err := reader.FeedRangeCursorV6("v6", RangeDirectionForward)
	if err != nil {
		t.Fatalf("FeedRangeCursorV6: %v", err)
	}
	count := 0
	var first AddressRange6
	for {
		rng, ok, err := cursor.NextRange()
		if err != nil {
			t.Fatalf("NextRange: %v", err)
		}
		if !ok {
			break
		}
		if count == 0 {
			first = rng
		}
		count++
	}
	if count != 2 || first.FromHi != 0x2001 || first.FromLo != 0x0db8 {
		t.Errorf("read ranges = %d first=%+v, want 2 starting at 2001:db8", count, first)
	}
}

func TestImmutableFeedEmptySourcePublishesEmptyFeed(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "empty.v4")
	valueTag, err := NewValueTag([]byte("downloaded"))
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	feedName, err := NewFeedName("empty")
	if err != nil {
		t.Fatalf("feed name: %v", err)
	}
	source := sliceSource4(nil)
	result, err := CreateImmutableFeedV4(destination, valueTag, feedName, nil, PolicyFailIfExists, &source, immutableFeedBudgetFor(), nil)
	if err != nil {
		t.Fatalf("CreateImmutableFeedV4: %v", err)
	}
	if result.Report.InputRecordCount != 0 || result.Report.NormalizedIntervalCount != 0 {
		t.Errorf("report = %+v, want zero counts", result.Report)
	}
	reader, err := OpenImmutable(destination)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer reader.Close()
	cursor, err := reader.FeedRangeCursorV4("empty", RangeDirectionForward)
	if err != nil {
		t.Fatalf("FeedRangeCursorV4: %v", err)
	}
	if _, ok, err := cursor.NextRange(); err != nil || ok {
		t.Errorf("empty feed cursor = ok=%v err=%v, want no ranges", ok, err)
	}
}

func TestImmutableFeedGuardsAndBudgetValidation(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "guards.v4")
	valueTag, _ := NewValueTag([]byte("downloaded"))
	feedName, _ := NewFeedName("g")
	source := sliceSource4{{From: 1, To: 2}}

	if _, err := CreateImmutableFeedV4(destination, valueTag, feedName, nil, PolicyFailIfExists, nil, immutableFeedBudgetFor(), nil); err == nil || feedFailureCode(t, err) != ErrorInvalidArgument {
		t.Errorf("nil source: err = %v, want invalid argument", err)
	}
	if _, err := CreateImmutableFeedV4(destination, valueTag, feedName, nil, PolicyFailIfExists, &source, nil, nil); err == nil || feedFailureCode(t, err) != ErrorInvalidArgument {
		t.Errorf("nil budget: err = %v, want invalid argument", err)
	}
	cases := []struct {
		budget *ImmutableFeedBudget
		want   ErrorCode
	}{
		{&ImmutableFeedBudget{MaxHeapBytes: 1, MaxOutputPages: 1, MaxWorkspacePages: 1, MaxOpenFiles: 3}, ErrorInsufficientResourceBudget},
		{&ImmutableFeedBudget{MaxHeapBytes: 1, MaxOutputPages: 2, MaxWorkspacePages: 1, MaxOpenFiles: 2}, ErrorInsufficientResourceBudget},
		{&ImmutableFeedBudget{MaxHeapBytes: 1, MaxOutputPages: 2, MaxWorkspacePages: 1 << 32, MaxOpenFiles: 3}, ErrorPageSpaceExhausted},
	}
	for i, tc := range cases {
		if _, err := CreateImmutableFeedV4(destination, valueTag, feedName, nil, PolicyFailIfExists, &source, tc.budget, nil); err == nil || feedFailureCode(t, err) != tc.want {
			t.Errorf("budget case %d: err = %v, want code %v", i, err, tc.want)
		}
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Errorf("guarded refusals left a destination artifact")
	}
}

func TestImmutableFeedEmptyBatchAndSourceError(t *testing.T) {
	dir := t.TempDir()
	valueTag, _ := NewValueTag([]byte("downloaded"))
	feedName, _ := NewFeedName("g")

	empty := &emptyBatchSource4{}
	dest1 := filepath.Join(dir, "empty-batch.v4")
	if _, err := CreateImmutableFeedV4(dest1, valueTag, feedName, nil, PolicyFailIfExists, empty, immutableFeedBudgetFor(), nil); err == nil || feedFailureCode(t, err) != ErrorInvalidArgument {
		t.Errorf("empty batch: err = %v, want invalid argument", err)
	}
	if _, err := os.Stat(dest1); !os.IsNotExist(err) {
		t.Errorf("empty-batch refusal left a destination artifact")
	}

	failing := &failingSource4{cause: errors.New("source exploded")}
	dest2 := filepath.Join(dir, "source-error.v4")
	if _, err := CreateImmutableFeedV4(dest2, valueTag, feedName, nil, PolicyFailIfExists, failing, immutableFeedBudgetFor(), nil); err == nil {
		t.Fatalf("source error: want failure")
	} else {
		var pref *ImmutableFeedPreparationFailure
		if !errors.As(err, &pref) {
			t.Fatalf("source error type = %T, want *ImmutableFeedPreparationFailure", err)
		}
		if pref.Cleanup != CleanupStateClean {
			t.Errorf("source-error cleanup = %v, want clean", pref.Cleanup)
		}
		if !strings.Contains(pref.Error(), "source exploded") {
			t.Errorf("failure error = %q, want the primary cause rendered", pref.Error())
		}
	}
	if _, err := os.Stat(dest2); !os.IsNotExist(err) {
		t.Errorf("source-error refusal left a destination artifact")
	}
}

func TestImmutableFeedCancellation(t *testing.T) {
	dir := t.TempDir()
	valueTag, _ := NewValueTag([]byte("downloaded"))
	feedName, _ := NewFeedName("g")

	pre := NewCancellationToken()
	pre.Cancel()
	dest1 := filepath.Join(dir, "pre.v4")
	source := sliceSource4{{From: 1, To: 2}}
	if _, err := CreateImmutableFeedV4(dest1, valueTag, feedName, nil, PolicyFailIfExists, &source, immutableFeedBudgetFor(), pre); err == nil || feedFailureCode(t, err) != ErrorCancelled {
		t.Errorf("pre-cancelled: err = %v, want cancelled", err)
	}
	if _, err := os.Stat(dest1); !os.IsNotExist(err) {
		t.Errorf("pre-cancelled refusal left a destination artifact")
	}

	// Mid-build cancellation: the source hands the first batch, then the
	// token cancels before the second batch, so the normalize drain
	// refuses between batches and the attempt is discarded.
	dest2 := filepath.Join(dir, "mid.v4")
	token := NewCancellationToken()
	cancelSource := &cancelOnSecondBatchSource4{cancel: func() { token.Cancel() }}
	if _, err := CreateImmutableFeedV4(dest2, valueTag, feedName, nil, PolicyFailIfExists, cancelSource, immutableFeedBudgetFor(), token); err == nil || feedFailureCode(t, err) != ErrorCancelled {
		t.Errorf("mid-build cancellation: err = %v, want cancelled", err)
	}
	if !cancelSource.sawSecondBatch {
		t.Errorf("cancellation source never reached the second batch")
	}
	if _, err := os.Stat(dest2); !os.IsNotExist(err) {
		t.Errorf("cancelled build left a destination artifact")
	}
}

func TestImmutableFeedWorkspaceBudgetExhausted(t *testing.T) {
	dir := t.TempDir()
	valueTag, _ := NewValueTag([]byte("downloaded"))
	feedName, _ := NewFeedName("g")
	// One workspace page cannot hold the normalized tree of 1000
	// disjoint ranges: the workspace allocation refuses with the
	// budget class and the attempt is discarded.
	source := make([]AddressRange4, 1000)
	for i := range source {
		from := uint32(1_000_000 + i*100)
		source[i] = AddressRange4{From: IPv4(from), To: IPv4(from + 50)}
	}
	s := sliceSource4(source)
	dest := filepath.Join(dir, "workspace.v4")
	budget := &ImmutableFeedBudget{MaxHeapBytes: 64 << 20, MaxOutputPages: 20_000, MaxWorkspacePages: 0, MaxOpenFiles: 3}
	if _, err := CreateImmutableFeedV4(dest, valueTag, feedName, nil, PolicyFailIfExists, &s, budget, nil); err == nil || feedFailureCode(t, err) != ErrorInsufficientResourceBudget {
		t.Errorf("workspace exhaustion: err = %v, want budget-exceeded", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("workspace-exhausted build left a destination artifact")
	}
}

func TestImmutableFeedExistingDestinationRefused(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "exists.v4")
	if err := os.WriteFile(destination, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	valueTag, _ := NewValueTag([]byte("downloaded"))
	feedName, _ := NewFeedName("g")
	source := sliceSource4{{From: 1, To: 2}}
	if _, err := CreateImmutableFeedV4(destination, valueTag, feedName, nil, PolicyFailIfExists, &source, immutableFeedBudgetFor(), nil); err == nil {
		t.Fatalf("existing destination: want failure")
	}
}

// emptyBatchSource4 returns one empty batch (the Rust source contract
// refuses it).
type emptyBatchSource4 struct{ done bool }

func (s *emptyBatchSource4) NextBatch() ([]AddressRange4, error) {
	if s.done {
		return nil, nil
	}
	s.done = true
	return []AddressRange4{}, nil
}

// failingSource4 returns an exact source error.
type failingSource4 struct{ cause error }

func (s *failingSource4) NextBatch() ([]AddressRange4, error) {
	return nil, s.cause
}

// cancelOnSecondBatchSource4 cancels before returning the second
// batch, so the drain's per-batch checkpoint refuses.
type cancelOnSecondBatchSource4 struct {
	first          bool
	sawSecondBatch bool
	cancel         func()
}

func (s *cancelOnSecondBatchSource4) NextBatch() ([]AddressRange4, error) {
	if !s.first {
		s.first = true
		return []AddressRange4{{From: 1, To: 2}}, nil
	}
	s.sawSecondBatch = true
	s.cancel()
	return []AddressRange4{{From: 10, To: 20}}, nil
}

// feedFailureCode extracts the public error code of one immutable feed
// preparation failure.
func feedFailureCode(t *testing.T, err error) ErrorCode {
	t.Helper()
	var failure *ImmutableFeedPreparationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("not an *ImmutableFeedPreparationFailure: %v", err)
	}
	var public *Error
	if !errors.As(failure.Cause, &public) {
		t.Fatalf("cause not a public *Error: %v", failure.Cause)
	}
	return public.Code
}
