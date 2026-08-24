package recovery

// Recovery report (Rust recovery/report.rs): the truthful physical and
// logical counters of one recovery read, the streamed damage
// envelopes, and the synchronous envelope sink. The reporter folds
// every counter overflow to the ArithmeticOverflow class, mirrors the
// possible-span accounting over the validation address fence, and
// stops exactly like the Rust sink control.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// RecoveryPageCounts is the physical-page facts of one recovery read
// (Rust RecoveryPageCounts).
type RecoveryPageCounts struct {
	Examined     uint64
	Accepted     uint64
	Rejected     uint64
	IOUnreadable uint64
}

// RecoveryLogicalCounts is the logical-object facts of one recovery
// read (Rust RecoveryLogicalCounts).
type RecoveryLogicalCounts struct {
	Examined uint64
	Accepted uint64
	Rejected uint64
}

// RecoveryReport is the truthful completed or partial recovery facts
// (Rust RecoveryReport).
type RecoveryReport struct {
	Pages                        RecoveryPageCounts
	Ranges                       RecoveryLogicalCounts
	CatalogEntries               RecoveryLogicalCounts
	MembershipEntries            RecoveryLogicalCounts
	StructureEntries             RecoveryLogicalCounts
	MetadataChunks               RecoveryLogicalCounts
	RetirementRecords            RecoveryLogicalCounts
	VerifiedAddresses            format.Cardinality129
	RejectedAddresses            format.Cardinality129
	BoundedPossibleSpanAddresses format.Cardinality129
	HasUnboundedUnknown          bool
	UnknownEnvelopes             uint64
}

// RecoveryUnknownEnvelope is one independently established
// recovery-damage envelope (Rust RecoveryUnknownEnvelope).
type RecoveryUnknownEnvelope struct {
	Sequence                  uint64
	Reason                    validation.ValidationReason
	Object                    validation.ValidationObject
	PageNumber                *uint32
	PhysicalBytes             *validation.PhysicalByteInterval
	AddressFence              *validation.ValidationAddressFence
	ContributesToPossibleSpan bool
	HasUnboundedExtent        bool
}

// RecoverySinkControl is the sink response for one borrowed damage
// envelope (Rust RecoverySinkControl).
type RecoverySinkControl uint8

const (
	RecoverySinkContinue RecoverySinkControl = iota
	RecoverySinkStop
)

// RecoverySink consumes one borrowed recovery-damage envelope and
// decides whether the read continues (Rust RecoverySink). A nil sink
// (or a nil function adapter) behaves like Continue for every
// envelope.
type RecoverySink interface {
	Unknown(*RecoveryUnknownEnvelope) (RecoverySinkControl, error)
}

// RecoverySinkFunc adapts a plain function to the recovery sink
// interface (Rust impl RecoverySink for F).
type RecoverySinkFunc func(*RecoveryUnknownEnvelope) (RecoverySinkControl, error)

// Unknown implements RecoverySink.
func (f RecoverySinkFunc) Unknown(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
	if f == nil {
		return RecoverySinkContinue, nil
	}
	return f(envelope)
}

// unknownEnvelope is the mutable envelope builder of one sink call
// (Rust report::Unknown).
type unknownEnvelope struct {
	reason                    validation.ValidationReason
	object                    validation.ValidationObject
	pageNumber                *uint32
	physicalBytes             *validation.PhysicalByteInterval
	addressFence              *validation.ValidationAddressFence
	contributesToPossibleSpan bool
	hasUnboundedExtent        bool
}

// reporter is the counting and streaming machine of one recovery read
// (Rust Reporter).
type reporter struct {
	report   RecoveryReport
	sequence uint64
	sink     RecoverySink
}

// newReporter starts one recovery report into the sink (Rust
// Reporter::new).
func newReporter(sink RecoverySink) *reporter {
	return &reporter{report: RecoveryReport{}, sequence: 0, sink: sink}
}

// resumeReporter continues a partial recovery report (Rust
// Reporter::resume: the envelope sequence continues from the report).
func resumeReporter(report RecoveryReport, sink RecoverySink) *reporter {
	sequence := report.UnknownEnvelopes
	return &reporter{report: report, sequence: sequence, sink: sink}
}

// finish returns the accumulated report (Rust Reporter::finish).
func (r *reporter) finish() RecoveryReport {
	return r.report
}

// pageAccepted counts one accepted page (Rust Reporter::page_accepted).
func (r *reporter) pageAccepted() error {
	if err := increment(&r.report.Pages.Examined, "recovery pages examined"); err != nil {
		return err
	}
	return increment(&r.report.Pages.Accepted, "recovery pages accepted")
}

// pageRejected counts one rejected page (Rust Reporter::page_rejected;
// ioUnreadable additionally counts the I/O-unreadable class).
func (r *reporter) pageRejected(ioUnreadable bool) error {
	if err := increment(&r.report.Pages.Examined, "recovery pages examined"); err != nil {
		return err
	}
	if err := increment(&r.report.Pages.Rejected, "recovery pages rejected"); err != nil {
		return err
	}
	if ioUnreadable {
		return increment(&r.report.Pages.IOUnreadable, "recovery I/O-unreadable pages")
	}
	return nil
}

// rangeExamined counts one examined range (Rust Reporter::
// range_examined).
func (r *reporter) rangeExamined() error {
	return increment(&r.report.Ranges.Examined, "recovery ranges examined")
}

// rangeAcceptedV4 counts one accepted IPv4 range and its verified
// addresses (Rust Reporter::range_accepted).
func (r *reporter) rangeAcceptedV4(from, to uint32) error {
	if err := increment(&r.report.Ranges.Accepted, "recovery ranges accepted"); err != nil {
		return err
	}
	return addAddressesV4(&r.report.VerifiedAddresses, from, to)
}

// rangeAcceptedV6 counts one accepted IPv6 range and its verified
// addresses (Rust Reporter::range_accepted).
func (r *reporter) rangeAcceptedV6(fromHi, fromLo, toHi, toLo uint64) error {
	if err := increment(&r.report.Ranges.Accepted, "recovery ranges accepted"); err != nil {
		return err
	}
	return addAddressesV6(&r.report.VerifiedAddresses, fromHi, fromLo, toHi, toLo)
}

// rangesRejectedV4 counts rejected ranges and their rejected
// addresses (Rust Reporter::ranges_rejected).
func (r *reporter) rangesRejectedV4(count uint64, from, to uint32) error {
	if err := addCount(&r.report.Ranges.Rejected, count, "recovery ranges rejected"); err != nil {
		return err
	}
	return addAddressesV4(&r.report.RejectedAddresses, from, to)
}

// rangesRejectedV6 counts rejected ranges and their rejected
// addresses (Rust Reporter::ranges_rejected).
func (r *reporter) rangesRejectedV6(count uint64, fromHi, fromLo, toHi, toLo uint64) error {
	if err := addCount(&r.report.Ranges.Rejected, count, "recovery ranges rejected"); err != nil {
		return err
	}
	return addAddressesV6(&r.report.RejectedAddresses, fromHi, fromLo, toHi, toLo)
}

// rangeRejectedWithoutBounds counts one rejected range without any
// address proof (Rust Reporter::range_rejected_without_bounds).
func (r *reporter) rangeRejectedWithoutBounds() error {
	return increment(&r.report.Ranges.Rejected, "recovery ranges rejected")
}

// catalogExamined counts one examined catalog entry (Rust
// Reporter::catalog_examined).
func (r *reporter) catalogExamined() error {
	return increment(&r.report.CatalogEntries.Examined, "recovery catalog entries examined")
}

// catalogAccepted adds accepted catalog entries (Rust
// Reporter::catalog_accepted).
func (r *reporter) catalogAccepted(count uint64) error {
	return addCount(&r.report.CatalogEntries.Accepted, count, "recovery catalog entries accepted")
}

// catalogRejected adds rejected catalog entries (Rust
// Reporter::catalog_rejected).
func (r *reporter) catalogRejected(count uint64) error {
	return addCount(&r.report.CatalogEntries.Rejected, count, "recovery catalog entries rejected")
}

// membershipExamined counts one examined membership entry (Rust
// Reporter::membership_examined).
func (r *reporter) membershipExamined() error {
	return increment(&r.report.MembershipEntries.Examined, "recovery membership entries examined")
}

// membershipAccepted adds accepted membership entries (Rust
// Reporter::membership_accepted).
func (r *reporter) membershipAccepted(count uint64) error {
	return addCount(&r.report.MembershipEntries.Accepted, count, "recovery membership entries accepted")
}

// membershipRejected adds rejected membership entries (Rust
// Reporter::membership_rejected).
func (r *reporter) membershipRejected(count uint64) error {
	return addCount(&r.report.MembershipEntries.Rejected, count, "recovery membership entries rejected")
}

// structureExamined counts one examined structure entry (Rust
// Reporter::structure_examined).
func (r *reporter) structureExamined() error {
	return increment(&r.report.StructureEntries.Examined, "recovery structure entries examined")
}

// structureAccepted adds accepted structure entries (Rust
// Reporter::structure_accepted).
func (r *reporter) structureAccepted(count uint64) error {
	return addCount(&r.report.StructureEntries.Accepted, count, "recovery structure entries accepted")
}

// structureRejected adds rejected structure entries (Rust
// Reporter::structure_rejected).
func (r *reporter) structureRejected(count uint64) error {
	return addCount(&r.report.StructureEntries.Rejected, count, "recovery structure entries rejected")
}

// metadataChunkExamined counts one examined metadata chunk (Rust
// Reporter::metadata_chunk_examined).
func (r *reporter) metadataChunkExamined() error {
	return increment(&r.report.MetadataChunks.Examined, "recovery metadata chunks examined")
}

// metadataFinished folds the examined metadata chunks into the
// accepted or rejected class (Rust Reporter::metadata_finished).
func (r *reporter) metadataFinished(accepted bool) error {
	count := r.report.MetadataChunks.Examined
	if accepted {
		return addCount(&r.report.MetadataChunks.Accepted, count, "recovery metadata chunk outcome")
	}
	return addCount(&r.report.MetadataChunks.Rejected, count, "recovery metadata chunk outcome")
}

// unknown streams one damage envelope (Rust Reporter::unknown): the
// sequence and envelope counters, the possible-span accounting over
// the fence cardinality, the unbounded flag, and the sink control
// (Stop is the StoppedBySink class, a failing sink the SinkFailed
// class).
func (r *reporter) unknown(unknown unknownEnvelope) error {
	r.sequence++
	if r.sequence == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery unknown sequence"}
	}
	r.report.UnknownEnvelopes = r.sequence
	if unknown.contributesToPossibleSpan {
		if unknown.addressFence == nil {
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "bounded recovery unknown is missing its address fence"}
		}
		if err := r.addPossibleSpan(*unknown.addressFence); err != nil {
			return err
		}
	}
	r.report.HasUnboundedUnknown = r.report.HasUnboundedUnknown || unknown.hasUnboundedExtent
	envelope := RecoveryUnknownEnvelope{
		Sequence:                  r.sequence,
		Reason:                    unknown.reason,
		Object:                    unknown.object,
		PageNumber:                unknown.pageNumber,
		PhysicalBytes:             unknown.physicalBytes,
		AddressFence:              unknown.addressFence,
		ContributesToPossibleSpan: unknown.contributesToPossibleSpan,
		HasUnboundedExtent:        unknown.hasUnboundedExtent,
	}
	sink := r.sink
	if sink == nil {
		sink = RecoverySinkFunc(nil)
	}
	switch control, err := sink.Unknown(&envelope); {
	case err != nil:
		return &format.Error{Code: format.CodeSinkFailed, Detail: err.Error()}
	case control == RecoverySinkStop:
		return &format.Error{Code: format.CodeStoppedBySink, Detail: "recovery sink requested stop"}
	default:
		return nil
	}
}

// addPossibleSpan folds one bounded fence into the possible-span
// cardinality (Rust Reporter::add_possible_span over the inclusive
// interval cardinality).
func (r *reporter) addPossibleSpan(fence validation.ValidationAddressFence) error {
	var cardinality format.Cardinality129
	var err error
	if fence.IPv4 {
		cardinality, err = format.IPv4Inclusive(uint32(fence.From), uint32(fence.To))
	} else {
		cardinality, err = format.IPv6Inclusive(
			asUint64(fence.FromV6[0:8]), asUint64(fence.FromV6[8:16]),
			asUint64(fence.ToV6[0:8]), asUint64(fence.ToV6[8:16]),
		)
	}
	if err != nil {
		return err
	}
	total, err := r.report.BoundedPossibleSpanAddresses.Add(cardinality)
	if err != nil {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery possible-span cardinality"}
	}
	r.report.BoundedPossibleSpanAddresses = total
	return nil
}

// increment folds one checked +1 (Rust increment).
func increment(value *uint64, purpose string) error {
	next := *value + 1
	if next == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: purpose}
	}
	*value = next
	return nil
}

// addCount folds one checked add (Rust add_count).
func addCount(value *uint64, count uint64, purpose string) error {
	next := *value + count
	if next < *value {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: purpose}
	}
	*value = next
	return nil
}

// addAddressesV4 adds the inclusive IPv4 interval cardinality (Rust
// add_addresses).
func addAddressesV4(total *format.Cardinality129, from, to uint32) error {
	cardinality, err := format.IPv4Inclusive(from, to)
	if err != nil {
		return err
	}
	next, err := total.Add(cardinality)
	if err != nil {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery address cardinality"}
	}
	*total = next
	return nil
}

// addAddressesV6 adds the inclusive IPv6 interval cardinality (Rust
// add_addresses).
func addAddressesV6(total *format.Cardinality129, fromHi, fromLo, toHi, toLo uint64) error {
	cardinality, err := format.IPv6Inclusive(fromHi, fromLo, toHi, toLo)
	if err != nil {
		return err
	}
	next, err := total.Add(cardinality)
	if err != nil {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery address cardinality"}
	}
	*total = next
	return nil
}

// pageInterval builds the physical byte interval of one page (Rust
// report::page_interval).
func pageInterval(page uint32) validation.PhysicalByteInterval {
	start := uint64(page) * format.PageSize
	return validation.PhysicalByteInterval{Start: start, EndExclusive: start + format.PageSize}
}

// emitPageUnknown streams one page damage envelope (Rust
// emit_page_unknown: the page interval is attached to the envelope).
func (r *reporter) emitPageUnknown(reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error {
	var interval *validation.PhysicalByteInterval
	if page != nil {
		value := pageInterval(*page)
		interval = &value
	}
	return r.unknown(unknownEnvelope{
		reason:        reason,
		object:        object,
		pageNumber:    page,
		physicalBytes: interval,
	})
}
