use iprange_livedb::{AddressRange, DirectRange, Ipv4Key, RangeSource};

const BATCH_CAPACITY: usize = 1024;
const DISPERSED_SEED: u64 = 0x9e37_79b9_7f4a_7c15;
const EMPTY_DIRECT: DirectRange<Ipv4Key> = DirectRange {
    from: Ipv4Key(0),
    to: Ipv4Key(0),
    value: 0,
};
const EMPTY_ADDRESS: AddressRange<Ipv4Key> = AddressRange {
    from: Ipv4Key(0),
    to: Ipv4Key(0),
};

pub(crate) struct DirectSource {
    count: usize,
    next: usize,
    pattern: DirectPattern,
    batch: [DirectRange<Ipv4Key>; BATCH_CAPACITY],
}

enum DirectPattern {
    Unordered(Permutation),
    Nested { end: u32 },
}

impl DirectSource {
    pub(crate) fn unordered(count: usize) -> Result<Self, String> {
        require_address_space(count, 0)?;
        Ok(Self {
            count,
            next: 0,
            pattern: DirectPattern::Unordered(Permutation::new(count, DISPERSED_SEED)),
            batch: [EMPTY_DIRECT; BATCH_CAPACITY],
        })
    }

    pub(crate) fn nested(count: usize) -> Result<Self, String> {
        require_address_space(count, 0)?;
        Ok(Self {
            count,
            next: 0,
            pattern: DirectPattern::Nested {
                end: (count as u32) * 4 + 1,
            },
            batch: [EMPTY_DIRECT; BATCH_CAPACITY],
        })
    }
}

impl RangeSource<DirectRange<Ipv4Key>> for DirectSource {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[DirectRange<Ipv4Key>]>> {
        if self.next == self.count {
            return Ok(None);
        }
        let length = (self.count - self.next).min(BATCH_CAPACITY);
        for offset in 0..length {
            let ordinal = self.next + offset;
            let index = match self.pattern {
                DirectPattern::Unordered(permutation) => permutation.at(ordinal),
                DirectPattern::Nested { .. } => ordinal,
            };
            self.batch[offset] = match self.pattern {
                DirectPattern::Unordered(_) => {
                    let start = index as u32 * 4;
                    DirectRange {
                        from: Ipv4Key(start),
                        to: Ipv4Key(start + 1),
                        value: index as u32 % 251 + 1,
                    }
                }
                DirectPattern::Nested { end } => DirectRange {
                    from: Ipv4Key(index as u32),
                    to: Ipv4Key(end - index as u32),
                    value: index as u32 % 2 + 1,
                },
            };
        }
        self.next += length;
        Ok(Some(&self.batch[..length]))
    }
}

pub(crate) struct AddressSource {
    count: usize,
    next: usize,
    phase: u32,
    permutation: Permutation,
    batch: [AddressRange<Ipv4Key>; BATCH_CAPACITY],
}

#[derive(Clone, Copy, Debug)]
pub(crate) enum FeedShape {
    AscendingDisjoint,
    DescendingDisjoint,
    RandomDisjoint,
    RandomOverlapChain,
}

impl FeedShape {
    pub(crate) const fn expected_intervals(self, count: usize) -> u64 {
        match self {
            Self::RandomOverlapChain => 1,
            Self::AscendingDisjoint | Self::DescendingDisjoint | Self::RandomDisjoint => {
                count as u64
            }
        }
    }
}

pub(crate) struct FeedShapeSource {
    count: usize,
    next: usize,
    shape: FeedShape,
    permutation: Permutation,
    batch: [AddressRange<Ipv4Key>; BATCH_CAPACITY],
}

impl FeedShapeSource {
    pub(crate) fn new(count: usize, shape: FeedShape) -> Result<Self, String> {
        require_address_space(count, 0)?;
        Ok(Self {
            count,
            next: 0,
            shape,
            permutation: Permutation::new(count, DISPERSED_SEED),
            batch: [EMPTY_ADDRESS; BATCH_CAPACITY],
        })
    }
}

impl RangeSource<AddressRange<Ipv4Key>> for FeedShapeSource {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv4Key>]>> {
        if self.next == self.count {
            return Ok(None);
        }
        let length = (self.count - self.next).min(BATCH_CAPACITY);
        for offset in 0..length {
            let ordinal = self.next + offset;
            let index = match self.shape {
                FeedShape::AscendingDisjoint => ordinal,
                FeedShape::DescendingDisjoint => self.count - ordinal - 1,
                FeedShape::RandomDisjoint | FeedShape::RandomOverlapChain => {
                    self.permutation.at(ordinal)
                }
            };
            let from = index as u32 * 4;
            let to = match self.shape {
                FeedShape::RandomOverlapChain => from + 7,
                FeedShape::AscendingDisjoint
                | FeedShape::DescendingDisjoint
                | FeedShape::RandomDisjoint => from + 1,
            };
            self.batch[offset] = AddressRange {
                from: Ipv4Key(from),
                to: Ipv4Key(to),
            };
        }
        self.next += length;
        Ok(Some(&self.batch[..length]))
    }
}

impl AddressSource {
    pub(crate) fn new(count: usize, phase: u32) -> Result<Self, String> {
        require_address_space(count, phase)?;
        Ok(Self {
            count,
            next: 0,
            phase,
            permutation: Permutation::new(count, u64::from(phase) + 29),
            batch: [EMPTY_ADDRESS; BATCH_CAPACITY],
        })
    }
}

impl RangeSource<AddressRange<Ipv4Key>> for AddressSource {
    fn next_batch(&mut self) -> iprange_livedb::Result<Option<&[AddressRange<Ipv4Key>]>> {
        if self.next == self.count {
            return Ok(None);
        }
        let length = (self.count - self.next).min(BATCH_CAPACITY);
        for offset in 0..length {
            let index = self.permutation.at(self.next + offset);
            let start = (index as u32 + self.phase) * 4;
            self.batch[offset] = AddressRange {
                from: Ipv4Key(start),
                to: Ipv4Key(start + 1),
            };
        }
        self.next += length;
        Ok(Some(&self.batch[..length]))
    }
}

#[derive(Clone, Copy)]
struct Permutation {
    count: usize,
    step: usize,
    offset: usize,
}

impl Permutation {
    fn new(count: usize, seed: u64) -> Self {
        if count <= 1 {
            return Self {
                count,
                step: 1,
                offset: 0,
            };
        }
        let mut step = seed as usize % count;
        if step == 0 {
            step = 1;
        }
        while gcd(step, count) != 1 {
            step += 1;
            if step == count {
                step = 1;
            }
        }
        Self {
            count,
            step,
            offset: seed.rotate_left(17) as usize % count,
        }
    }

    fn at(self, ordinal: usize) -> usize {
        if self.count <= 1 {
            return 0;
        }
        ((ordinal as u128 * self.step as u128 + self.offset as u128) % self.count as u128) as usize
    }
}

fn require_address_space(count: usize, phase: u32) -> Result<(), String> {
    let maximum = (u32::MAX - 1) / 4;
    if count == 0 || count as u128 + u128::from(phase) > u128::from(maximum) + 1 {
        return Err("benchmark size exceeds the IPv4 workload space".to_owned());
    }
    Ok(())
}

fn gcd(mut left: usize, mut right: usize) -> usize {
    while right != 0 {
        let remainder = left % right;
        left = right;
        right = remainder;
    }
    left
}
