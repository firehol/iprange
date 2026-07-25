//! Fixed-memory validation and interning of streamed output memberships.

use crate::error::{Error, Result};
use crate::membership_dictionary;
use crate::used_bitmap::{self, Kind};

use super::Builder;

const CHECK_WORDS: usize = 64;

pub(crate) trait MembershipWords {
    fn word_count(&self) -> u32;
    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()>;
}

pub(super) fn intern<W: MembershipWords>(output: &mut Builder, source: &W) -> Result<u32> {
    let checked = CheckedWords {
        source,
        feed_root: output.meta.feed_used_root,
        feed_limit: output.meta.feed_index_limit,
    };
    require_shape(output, &checked)?;
    let mut state = output.membership_state();
    let interned = membership_dictionary::intern(output, &mut state, &checked)?;
    output.store_membership_state(state);
    Ok(interned.id)
}

struct CheckedWords<'a, W> {
    source: &'a W,
    feed_root: u32,
    feed_limit: u64,
}

impl<W: MembershipWords> membership_dictionary::Words<Builder> for CheckedWords<'_, W> {
    fn word_count(&self) -> u32 {
        self.source.word_count()
    }

    fn read_words(&self, store: &Builder, start: u32, output: &mut [u64]) -> Result<()> {
        self.source.read_words(start, output)?;
        let mut active = [0u64; CHECK_WORDS];
        for (offset, supplied) in output.chunks(CHECK_WORDS).enumerate() {
            let count = supplied.len();
            let word = start
                .checked_add((offset * CHECK_WORDS) as u32)
                .ok_or(Error::ArithmeticOverflow("membership word range"))?;
            used_bitmap::read_words(
                store,
                self.feed_root,
                self.feed_limit,
                Kind::Feed,
                word,
                &mut active[..count],
            )?;
            if has_inactive_bits(supplied, &active[..count]) {
                return Err(Error::InvalidArgument(
                    "membership references an inactive feed",
                ));
            }
        }
        Ok(())
    }
}

fn has_inactive_bits(supplied: &[u64], active: &[u64]) -> bool {
    supplied
        .iter()
        .zip(active)
        .any(|(&wanted, &present)| wanted & !present != 0)
}

fn require_shape<W: membership_dictionary::Words<Builder>>(
    store: &Builder,
    words: &W,
) -> Result<()> {
    let word_count = words.word_count();
    let maximum = store
        .meta
        .feed_index_limit
        .checked_add(63)
        .ok_or(Error::ArithmeticOverflow("membership word limit"))?
        / 64;
    if word_count == 0 || u64::from(word_count) > maximum {
        return Err(Error::InvalidArgument(
            "membership word count exceeds the feed-index limit",
        ));
    }
    let mut final_word = [0u64; 1];
    words.read_words(store, word_count - 1, &mut final_word)?;
    if final_word[0] == 0 {
        return Err(Error::InvalidArgument("membership bitmap is not canonical"));
    }
    Ok(())
}
