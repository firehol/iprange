//! Internal ownership bridge for the stable C binding.

mod reader;
mod writer;

pub use reader::{MembershipToken, Reader, ReaderCursor, ReaderCursorItem};
pub use writer::Writer;

/// Entry point used only by the version-matched SDK worker executable.
#[doc(hidden)]
pub fn worker_main() -> i32 {
    crate::worker::main()
}
