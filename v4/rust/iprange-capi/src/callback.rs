//! Binding-owned cancellation polling.

use std::ffi::c_void;
use std::sync::Arc;

use iprange_livedb::CancellationToken;

use crate::abi::Cancellation;
use crate::error::BoundaryError;

struct Poll {
    callback: unsafe extern "C" fn(*mut c_void) -> u8,
    context: *mut c_void,
}

// The engine invokes the callback synchronously on the originating operation.
// C owns the context lifetime until the stored token is released.
unsafe impl Send for Poll {}
unsafe impl Sync for Poll {}

impl Poll {
    fn cancelled(&self) -> bool {
        // SAFETY: the C caller keeps the callback and context valid for the operation.
        unsafe { (self.callback)(self.context) != 0 }
    }
}

pub(crate) fn token(cancellation: Cancellation) -> Result<CancellationToken, BoundaryError> {
    match cancellation.callback {
        Some(callback) => {
            let poll = Arc::new(Poll {
                callback,
                context: cancellation.context,
            });
            Ok(CancellationToken::from_poll(Arc::new(move || {
                poll.cancelled()
            })))
        }
        None if cancellation.context.is_null() => Ok(CancellationToken::new()),
        None => Err(BoundaryError::invalid_argument(
            "cancellation context requires a callback",
        )),
    }
}
