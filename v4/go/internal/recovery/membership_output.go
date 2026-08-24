package recovery

// Whole-component rejection and canonical membership-range output
// (Rust recovery/membership_output.rs): one accepted component is
// coalesced with an adjacent equal-membership record and pushed to the
// destination builder streaming the verified source bitmap, and one
// unresolved membership streams the missing-membership fence.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// membershipOutput is the membership-range policy of the
// overlap-component pass (Rust MembershipOutput): the pending range
// coalesces adjacent equal-bitmap records before the push.
type membershipOutput struct {
	mapping     *mapping.Mapping
	meta        format.Meta
	memberships *membershipIndex
	tables      *tableStore
	builder     *writer.OutputBuilder
	rep         *reporter
	family      uint8
	// previous carries the pending coalesced range by value (Rust
	// Option<OutputRange>): a pointer holds one heap allocation for
	// every accepted record, the pointer variant also chases that
	// heap cell on every coalesce.
	previous    outputRange
	hasPrevious bool
	// words is one reusable membership word-source slot: pushOutput
	// fills it before every push and passes its address, so the
	// writer interface conversion never boxes a per-push value (the
	// writer interns the words synchronously inside the push; Rust
	// passes &impl MembershipWords).
	words locatorWords
}

// outputRange is one pending membership range (Rust OutputRange).
type outputRange struct {
	from       rangeKey
	to         rangeKey
	membership membershipLocator
}

// resolve proves one record's membership (Rust
// MembershipOutput::resolve: the dictionary lookup, or the
// MembershipMissing fence envelope).
func (o *membershipOutput) resolve(record rangeRecord) (any, error) {
	membership, found, err := o.memberships.get(o.tables, record.value)
	if err != nil {
		return nil, err
	}
	if !found || membership.rejected {
		fence := o.codec().fence(record.from, record.to)
		if err := o.rep.unknown(unknownEnvelope{
			reason:       validation.ReasonMembershipMissing,
			object:       validation.ObjectMembershipDictionary,
			addressFence: &fence,
		}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return membership, nil
}

// accept counts one accepted component and coalesces it (Rust
// MembershipOutput::accept: the missing membership reports the
// rejected range, the resolved membership streams the accepted range).
func (o *membershipOutput) accept(record rangeRecord, resolved any) error {
	membership, ok := resolved.(membershipLocator)
	if !ok {
		if err := o.codec().reportRejected(o.rep, 1, record.from, record.to); err != nil {
			return err
		}
		return nil
	}
	if err := o.codec().reportAccepted(o.rep, record); err != nil {
		return err
	}
	return o.coalesce(outputRange{from: record.from, to: record.to, membership: membership})
}

// rejectOverlap streams one whole overlap component (Rust
// MembershipOutput::reject_overlap).
func (o *membershipOutput) rejectOverlap(count uint64, from, to rangeKey) error {
	return reportOverlap(o.rep, o.codec(), count, from, to)
}

// finish pushes the pending range (Rust MembershipOutput::finish over
// finish_output).
func (o *membershipOutput) finish() error {
	if !o.hasPrevious {
		return nil
	}
	o.hasPrevious = false
	return o.pushOutput(o.previous)
}

// coalesce merges one range with an adjacent equal-membership previous
// range or pushes the previous (Rust MembershipOutput::coalesce: the
// adjacency proof and the locator equality over the mapped words).
func (o *membershipOutput) coalesce(current outputRange) error {
	if !o.hasPrevious {
		o.previous = current
		o.hasPrevious = true
		return nil
	}
	previous := o.previous
	next, ok := o.codec().nextKey(previous.to)
	equal, err := locatorEqual(previous.membership, current.membership, o.mapping, o.meta)
	if err != nil {
		return err
	}
	if equal && ok && next == current.from {
		o.previous.to = current.to
		return nil
	}
	if err := o.pushOutput(previous); err != nil {
		return err
	}
	o.previous = current
	return nil
}

// pushOutput pushes one range to the destination builder (Rust
// MembershipKey::push_membership): the verified source bitmap streams
// into the writer intern through the word-source seam.
func (o *membershipOutput) pushOutput(output outputRange) error {
	o.words = locatorWords{reader: membershipWordReader{m: o.mapping, meta: o.meta, locator: output.membership}}
	switch o.family {
	case format.AddressFamilyIPv4:
		return o.builder.PushMembershipV4Words(uint32(output.from.hi), uint32(output.to.hi), &o.words)
	case format.AddressFamilyIPv6:
		return o.builder.PushMembershipV6Words(output.from.hi, output.from.lo, output.to.hi, output.to.lo, &o.words)
	default:
		return corruptError("recovery membership output family is invalid")
	}
}

// codec returns the family codec of one membership output.
func (o *membershipOutput) codec() rangeCodec {
	switch o.family {
	case format.AddressFamilyIPv4:
		return rangeV4Codec{}
	case format.AddressFamilyIPv6:
		return rangeV6Codec{}
	default:
		return nil
	}
}

// locatorWords adapts one recovered locator to the writer word-source
// seam (Rust RecoveredWords over MembershipWords): every chunk is a
// copy of the mapped words, so the writer never retains page bytes.
type locatorWords struct {
	reader membershipWordReader
}

func (w locatorWords) WordCount() uint32 { return w.reader.wordCount() }

// ReadChunk copies the up-to-64 words starting at start (the seam
// chunk read over the verified locator bitmap).
func (w locatorWords) ReadChunk(start uint32) ([64]uint64, uint32, error) {
	var words [64]uint64
	remaining := w.reader.wordCount() - start
	count := uint32(64)
	if remaining < count {
		count = remaining
	}
	if err := w.reader.readWords(start, words[:count]); err != nil {
		return words, 0, err
	}
	return words, count, nil
}
