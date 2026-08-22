// Shared semantic input and terminal workflow types (Rust workflow.rs and
// history.rs parity). These are value-free report/data types: they carry
// no mapped state and are produced by the typed writer workflows
// (milestone-3 chunk 4) and the logical read SDK.

package iprangedb

// AddressRange4 is one value-free inclusive IPv4 input interval.
type AddressRange4 struct {
	From, To IPv4
}

// AddressRange6 is one value-free inclusive IPv6 input interval.
type AddressRange6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
}

// WorkflowKind identifies the high-level operation that produced a report.
type WorkflowKind uint8

const (
	WorkflowCreateFeed        WorkflowKind = 0
	WorkflowReplaceFeed       WorkflowKind = 1
	WorkflowDirectReplacement WorkflowKind = 2
	WorkflowFirstSeenRefresh  WorkflowKind = 3
	WorkflowLastSeenRefresh   WorkflowKind = 4
	WorkflowMembershipImport  WorkflowKind = 5
)

// LogicalChange reports whether the complete requested state differs from
// the committed state.
type LogicalChange uint8

const (
	LogicalChanged  LogicalChange = 0
	LogicalNoChange LogicalChange = 1
)

// FirstSeenRemoval4 is one first-seen interval removed by a complete
// refresh.
type FirstSeenRemoval4 struct {
	From, To  IPv4
	FirstSeen uint32
	Addresses Cardinality129
}

// FirstSeenRemoval6 is one first-seen interval removed by a complete
// refresh.
type FirstSeenRemoval6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	FirstSeen                  uint32
	Addresses                  Cardinality129
}

// FirstSeenRemovalSink receives bounded batches of first-seen removals.
// Errors stop the refresh workflow and pass through unchanged.
type FirstSeenRemovalSink func(removals []FirstSeenRemoval4) error

// WorkflowReport is the exact semantic statistics produced before
// publication by one typed workflow.
type WorkflowReport struct {
	Workflow                      WorkflowKind
	LogicalChange                 LogicalChange
	InputRecordCount              uint64
	InputNormalizedIntervalCount  uint64
	BeforeRangeRecordCount        uint64
	AfterRangeRecordCount         uint64
	InputAddresses                Cardinality129
	BeforeAddresses               Cardinality129
	AfterAddresses                Cardinality129
	UnchangedValueAddresses       Cardinality129
	ChangedValueAddresses         Cardinality129
	AddedAddresses                Cardinality129
	RemovedAddresses              Cardinality129
	SourceFeedCount               uint64
	MatchedFeedCount              uint64
	CreatedFeedCount              uint64
	SourceDistinctMembershipCount uint64
	TranslatedMembershipCount     uint64
}

// HistoryWindow is one last-seen history window projection request
// (Rust history::HistoryWindow; the name is a Go string, the immutable
// value mirror of the Rust FeedName copy value).
type HistoryWindow struct {
	FeedName string
	Cutoff   uint32
}

// HistoryWindowReport is the exact outcome of one history window
// projection.
type HistoryWindowReport struct {
	FeedName            string
	Cutoff              uint32
	Created             bool
	BeforeIntervalCount uint64
	AfterIntervalCount  uint64
	BeforeAddresses     Cardinality129
	AfterAddresses      Cardinality129
	UnchangedAddresses  Cardinality129
	AddedAddresses      Cardinality129
	RemovedAddresses    Cardinality129
}

// HistoryProjectionReport is the exact aggregate plus per-window outcome
// of one history projection (Rust history::HistoryProjectionReport:
// aggregate statistics, then one HistoryWindowReport per projected
// window).
type HistoryProjectionReport struct {
	LogicalChange       LogicalChange
	SourceRangeCount    uint64
	SourceAddresses     Cardinality129
	CreatedFeedCount    uint64
	BeforeIntervalCount uint64
	AfterIntervalCount  uint64
	BeforeAddresses     Cardinality129
	AfterAddresses      Cardinality129
	UnchangedAddresses  Cardinality129
	AddedAddresses      Cardinality129
	RemovedAddresses    Cardinality129
	Windows             []HistoryWindowReport
}
