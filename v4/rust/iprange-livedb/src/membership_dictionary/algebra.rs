//! Fixed-memory bitmap algebra over interned memberships.

use crate::contract::MembershipOperation;
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiringStore, Store};

use super::{decode, find_record, intern, read_words, Interned, State, Words, HASH_WORDS};

struct Combined {
    id_root: u32,
    left_id: u32,
    left_words: u32,
    right_id: u32,
    right_words: u32,
    operation: MembershipOperation,
    word_count: u32,
}

impl<S: Store> Words<S> for Combined {
    fn word_count(&self) -> u32 {
        self.word_count
    }

    fn read_words(&self, store: &S, start: u32, output: &mut [u64]) -> Result<()> {
        read_operand(
            store,
            self.id_root,
            self.left_id,
            self.left_words,
            start,
            output,
        )?;
        let mut right = [0u64; HASH_WORDS];
        let mut done = 0usize;
        while done < output.len() {
            let count = (output.len() - done).min(HASH_WORDS);
            let at = start
                .checked_add(done as u32)
                .ok_or(Error::ArithmeticOverflow("membership word offset"))?;
            read_operand(
                store,
                self.id_root,
                self.right_id,
                self.right_words,
                at,
                &mut right[..count],
            )?;
            apply_words(
                &mut output[done..done + count],
                &right[..count],
                self.operation,
            );
            done += count;
        }
        Ok(())
    }
}

pub(crate) fn combine<S: RetiringStore>(
    store: &mut S,
    state: &mut State,
    left_id: u32,
    right_id: u32,
    right_words: u32,
    operation: MembershipOperation,
) -> Result<Interned> {
    let left_words = stored_word_count(store, state.id_root, left_id)?;
    require_words(store, state.id_root, right_id, right_words)?;
    if let Some(result) = identity(left_id, left_words, right_id, right_words, operation) {
        return Ok(result);
    }
    let raw_count = raw_word_count(left_words, right_words, operation);
    let mut source = Combined {
        id_root: state.id_root,
        left_id,
        left_words,
        right_id,
        right_words,
        operation,
        word_count: raw_count,
    };
    source.word_count = canonical_count(store, &source)?;
    if source.word_count == 0 {
        return Ok(empty());
    }
    intern(store, state, &source)
}

fn stored_word_count<S: Store>(store: &S, root: u32, id: u32) -> Result<u32> {
    if id == 0 {
        return Ok(0);
    }
    let found =
        find_record(store, root, id)?.ok_or(Error::Corrupt("range membership ID is missing"))?;
    decode(found.as_slice()).map(|record| record.word_count)
}

fn require_words<S: Store>(store: &S, root: u32, id: u32, expected: u32) -> Result<()> {
    if stored_word_count(store, root, id)? == expected && (id != 0 || expected == 0) {
        Ok(())
    } else {
        Err(Error::StaleReference)
    }
}

fn identity(
    left_id: u32,
    left_words: u32,
    right_id: u32,
    right_words: u32,
    operation: MembershipOperation,
) -> Option<Interned> {
    let left = (left_id, left_words);
    let right = (right_id, right_words);
    let selected = selected_identity(left, right, operation)?;
    Some(Interned {
        id: selected.0,
        word_count: selected.1,
        created: false,
    })
}

fn selected_identity(
    left: (u32, u32),
    right: (u32, u32),
    operation: MembershipOperation,
) -> Option<(u32, u32)> {
    match operation {
        MembershipOperation::Replace => Some(right),
        MembershipOperation::Union => union_identity(left, right),
        MembershipOperation::Difference => difference_identity(left, right),
        MembershipOperation::Intersection => intersection_identity(left, right),
        MembershipOperation::Xor => xor_identity(left, right),
    }
}

fn union_identity(left: (u32, u32), right: (u32, u32)) -> Option<(u32, u32)> {
    match (left.0, right.0) {
        (0, _) => Some(right),
        (_, 0) => Some(left),
        _ if left.0 == right.0 => Some(left),
        _ => None,
    }
}

fn difference_identity(left: (u32, u32), right: (u32, u32)) -> Option<(u32, u32)> {
    match (left.0, right.0) {
        (0, _) => Some((0, 0)),
        _ if left.0 == right.0 => Some((0, 0)),
        (_, 0) => Some(left),
        _ => None,
    }
}

fn intersection_identity(left: (u32, u32), right: (u32, u32)) -> Option<(u32, u32)> {
    match (left.0, right.0) {
        (0, _) | (_, 0) => Some((0, 0)),
        _ if left.0 == right.0 => Some(left),
        _ => None,
    }
}

fn xor_identity(left: (u32, u32), right: (u32, u32)) -> Option<(u32, u32)> {
    match (left.0, right.0) {
        (0, _) => Some(right),
        (_, 0) => Some(left),
        _ if left.0 == right.0 => Some((0, 0)),
        _ => None,
    }
}

fn raw_word_count(left: u32, right: u32, operation: MembershipOperation) -> u32 {
    match operation {
        MembershipOperation::Replace => right,
        MembershipOperation::Union | MembershipOperation::Xor => left.max(right),
        MembershipOperation::Difference => left,
        MembershipOperation::Intersection => left.min(right),
    }
}

fn canonical_count<S: Store>(store: &S, source: &Combined) -> Result<u32> {
    let mut end = source.word_count;
    let mut words = [0u64; HASH_WORDS];
    while end != 0 {
        let start = end.saturating_sub(HASH_WORDS as u32);
        let count = (end - start) as usize;
        source.read_words(store, start, &mut words[..count])?;
        if let Some(index) = words[..count].iter().rposition(|word| *word != 0) {
            return Ok(start + index as u32 + 1);
        }
        end = start;
    }
    Ok(0)
}

fn read_operand<S: Store>(
    store: &S,
    root: u32,
    id: u32,
    word_count: u32,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    output.fill(0);
    if id == 0 || start >= word_count {
        return Ok(());
    }
    let count = output.len().min((word_count - start) as usize);
    read_words(store, root, id, start, &mut output[..count])
}

fn apply_words(left: &mut [u64], right: &[u64], operation: MembershipOperation) {
    for (left, right) in left.iter_mut().zip(right) {
        *left = match operation {
            MembershipOperation::Replace => *right,
            MembershipOperation::Union => *left | *right,
            MembershipOperation::Difference => *left & !*right,
            MembershipOperation::Intersection => *left & *right,
            MembershipOperation::Xor => *left ^ *right,
        };
    }
}

fn empty() -> Interned {
    Interned {
        id: 0,
        word_count: 0,
        created: false,
    }
}
