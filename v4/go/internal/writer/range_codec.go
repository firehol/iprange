// Range family aliases over the canonical tree-owned types (Rust
// range_tree.rs RangeCodec + key.rs IpKey live inside the crate; the
// writer imports the same types so the tree-side gap layer and the
// writer's generic machinery share one record identity). The decoded
// records are family-typed like Rust Record<K>: an IPv4 record is 12
// bytes and an IPv6 record 36 bytes, so the mutation machinery never
// materializes the general tree key.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// key4 is the IPv4 range key: one numeric address (Rust Ipv4Key). The
// key lives in the value only; wire cells stay little-endian.
type key4 = tree.RangeKey4

// key6 is the IPv6 range key: the numeric high and low limbs (Rust
// Ipv6Key, high limb most significant).
type key6 = tree.RangeKey6

// rangeRecord is one decoded range leaf record in the family key space
// (Rust range_tree::Record<K>).
type rangeRecord[K any] = tree.RangeRecord[K]

// rangeFamily is one address-family range contract over its typed key
// (Rust IpKey: width, record size, checked next/previous, Ord).
type rangeFamily[K any] = tree.RangeFamily[K]

// rangeCodec4 is the IPv4 range tree codec (Rust RangeCodec<Ipv4Key>).
type rangeCodec4 = tree.RangeCodec4

// rangeCodec6 is the IPv6 range tree codec (Rust RangeCodec<Ipv6Key>).
type rangeCodec6 = tree.RangeCodec6

// privateGap evaluates the local gap around one candidate range (Rust
// range_mutation::PrivateGap).
type privateGap[K any] = tree.RangePrivateGap[K]
