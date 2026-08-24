package recovery

// Recovery-report tests: the page counter classes, the envelope
// streaming with the possible-span and unbounded flags, the sink stop
// and failure classes, and the address cardinality folds.

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

func TestReportPageCountersAndResume(t *testing.T) {
	reporter := newReporter(nil)
	if err := reporter.pageAccepted(); err != nil {
		t.Fatal(err)
	}
	if err := reporter.pageAccepted(); err != nil {
		t.Fatal(err)
	}
	if err := reporter.pageRejected(false); err != nil {
		t.Fatal(err)
	}
	if err := reporter.pageRejected(true); err != nil {
		t.Fatal(err)
	}
	report := reporter.finish()
	if report.Pages.Examined != 4 || report.Pages.Accepted != 2 || report.Pages.Rejected != 2 || report.Pages.IOUnreadable != 1 {
		t.Fatalf("pages %+v", report.Pages)
	}
	// Resume continues the envelope sequence from the report.
	if err := reporter.rangeExamined(); err != nil {
		t.Fatal(err)
	}
	if err := reporter.rangeAcceptedV4(10, 12); err != nil {
		t.Fatal(err)
	}
	if err := reporter.rangesRejectedV4(2, 20, 21); err != nil {
		t.Fatal(err)
	}
	if err := reporter.rangeRejectedWithoutBounds(); err != nil {
		t.Fatal(err)
	}
	report = reporter.finish()
	if report.Ranges.Examined != 1 || report.Ranges.Accepted != 1 || report.Ranges.Rejected != 3 {
		t.Fatalf("ranges %+v", report.Ranges)
	}
	verified, err := report.VerifiedAddresses.Uint64()
	if err != nil || verified != 3 {
		t.Fatalf("verified %v err %v", report.VerifiedAddresses.String(), err)
	}
	rejected, err := report.RejectedAddresses.Uint64()
	if err != nil || rejected != 2 {
		t.Fatalf("rejected %v err %v", report.RejectedAddresses.String(), err)
	}
	continued := resumeReporter(report, nil)
	if continued.sequence != report.UnknownEnvelopes {
		t.Fatalf("resume sequence %d, want %d", continued.sequence, report.UnknownEnvelopes)
	}
}

func TestReportEnvelopeStreaming(t *testing.T) {
	var envelopes []RecoveryUnknownEnvelope
	reporter := newReporter(RecoverySinkFunc(func(e *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		envelopes = append(envelopes, *e)
		return RecoverySinkContinue, nil
	}))
	fence := validation.ValidationAddressFence{IPv4: true, From: 5, To: 7}
	page := uint32(9)
	interval := pageInterval(page)
	if err := reporter.unknown(unknownEnvelope{
		reason:                    validation.ReasonPageCrcMismatch,
		object:                    validation.ObjectRangeTree,
		pageNumber:                &page,
		physicalBytes:             &interval,
		addressFence:              &fence,
		contributesToPossibleSpan: true,
		hasUnboundedExtent:        true,
	}); err != nil {
		t.Fatal(err)
	}
	report := reporter.finish()
	if report.UnknownEnvelopes != 1 || !report.HasUnboundedUnknown {
		t.Fatalf("report %+v", report)
	}
	span, err := report.BoundedPossibleSpanAddresses.Uint64()
	if err != nil || span != 3 {
		t.Fatalf("possible span %v err %v", report.BoundedPossibleSpanAddresses.String(), err)
	}
	if len(envelopes) != 1 {
		t.Fatalf("envelopes %d", len(envelopes))
	}
	envelope := envelopes[0]
	if envelope.Sequence != 1 || envelope.Reason != validation.ReasonPageCrcMismatch ||
		envelope.PageNumber == nil || *envelope.PageNumber != 9 ||
		envelope.PhysicalBytes == nil || envelope.PhysicalBytes.Start != 9*format.PageSize ||
		!envelope.ContributesToPossibleSpan || !envelope.HasUnboundedExtent {
		t.Fatalf("envelope %+v", envelope)
	}
}

func TestReportEnvelopeStopAndSinkFailure(t *testing.T) {
	reporter := newReporter(RecoverySinkFunc(func(e *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		return RecoverySinkStop, nil
	}))
	if err := reporter.unknown(unknownEnvelope{reason: validation.ReasonMetaInvalid, object: validation.ObjectMeta}); err == nil {
		t.Fatal("sink stop accepted")
	}
	reporter = newReporter(RecoverySinkFunc(func(e *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		return RecoverySinkContinue, errors.New("consumer failed")
	}))
	if err := reporter.unknown(unknownEnvelope{reason: validation.ReasonMetaInvalid, object: validation.ObjectMeta}); err == nil {
		t.Fatal("sink failure accepted")
	}
}

func TestReportPossibleSpanRequiresTheFence(t *testing.T) {
	reporter := newReporter(nil)
	if err := reporter.unknown(unknownEnvelope{
		reason: validation.ReasonPageAlias, object: validation.ObjectRangeTree,
		contributesToPossibleSpan: true,
	}); err == nil {
		t.Fatal("possible-span envelope without a fence accepted")
	}
}

func TestReportIPv6Cardinality(t *testing.T) {
	reporter := newReporter(nil)
	var from, to [16]byte
	from[15] = 1
	to[15] = 4
	if err := reporter.rangeAcceptedV6(0, 0, 0, 4); err != nil {
		t.Fatal(err)
	}
	report := reporter.finish()
	count, err := report.VerifiedAddresses.Uint64()
	if err != nil || count != 5 {
		t.Fatalf("v6 verified %v err %v", report.VerifiedAddresses.String(), err)
	}
}

func TestReportPageEnvelopeHelper(t *testing.T) {
	reporter := newReporter(nil)
	page := uint32(4)
	if err := reporter.emitPageUnknown(validation.ReasonPageHeaderInvalid, validation.ObjectFreeBitmap, &page); err != nil {
		t.Fatal(err)
	}
	report := reporter.finish()
	if report.UnknownEnvelopes != 1 {
		t.Fatalf("unknown envelopes %d", report.UnknownEnvelopes)
	}
}
