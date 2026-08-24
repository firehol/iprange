package recovery

// One ordered overlap-component pass for recovered ranges (Rust
// recovery/range_components.rs): adjacent-disjoint readable records
// stream into the policy, overlapping records accumulate one component
// that is rejected as a whole with its fence, and an order regression
// is the candidate-changed class.

import "github.com/firehol/iprange/v4/go/internal/live"

// rangePolicy consumes the resolved components of one overlap pass
// (Rust Policy).
type rangePolicy interface {
	resolve(record rangeRecord) (any, error)
	accept(record rangeRecord, resolved any) error
	rejectOverlap(count uint64, from, to rangeKey) error
	finish() error
}

// component is one accumulated overlap component (Rust Component): the
// first record, the resolved token, the maximum to-key, and the record
// count.
type component struct {
	first     rangeRecord
	resolved  any
	maximumTo rangeKey
	count     uint64
}

// components is the ordered overlap-component pass (Rust Components).
type components struct {
	check     func() error
	codec     rangeCodec
	policy    rangePolicy
	component *component
}

// push accepts one record into the pass (Rust Components::push: the
// checkpoint, the policy resolution, the order proof, and the overlap
// accumulation or component finish).
func (c *components) push(record rangeRecord) error {
	if err := live.Checkpoint(c.check); err != nil {
		return err
	}
	resolved, err := c.policy.resolve(record)
	if err != nil {
		return err
	}
	if c.component == nil {
		c.component = &component{first: record, resolved: resolved, maximumTo: record.to, count: 1}
		return nil
	}
	current := *c.component
	c.component = nil
	if c.codec.lessKey(record.from, current.first.from) {
		return candidateChangedError()
	}
	if !c.codec.lessKey(current.maximumTo, record.from) {
		// record.from <= maximum_to: the record belongs to the open
		// component.
		if c.codec.lessKey(current.maximumTo, record.to) {
			current.maximumTo = record.to
		}
		next := current.count + 1
		if next == 0 {
			return overflowError("recovery overlap component")
		}
		current.count = next
		c.component = &current
		return nil
	}
	if err := c.finishComponent(current); err != nil {
		return err
	}
	c.component = &component{first: record, resolved: resolved, maximumTo: record.to, count: 1}
	return nil
}

// finish closes the open component and the policy (Rust
// Components::finish).
func (c *components) finish() error {
	if c.component != nil {
		current := *c.component
		c.component = nil
		if err := c.finishComponent(current); err != nil {
			return err
		}
	}
	return c.policy.finish()
}

// finishComponent resolves one closed component (Rust
// Components::finish_component: one record is accepted, a multi-record
// component is rejected as a whole).
func (c *components) finishComponent(current component) error {
	if current.count != 1 {
		return c.policy.rejectOverlap(current.count, current.first.from, current.maximumTo)
	}
	return c.policy.accept(current.first, current.resolved)
}
