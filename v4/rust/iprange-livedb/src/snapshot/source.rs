//! Logical-reader bridge for a source protected by snapshot coordination.

use crate::reader_core::GenerationReader;
use crate::recovery::source_guard::Source;

pub(super) fn reader(source: &Source) -> GenerationReader<'_> {
    GenerationReader::new(source.mapping(), source.meta(), None)
}
