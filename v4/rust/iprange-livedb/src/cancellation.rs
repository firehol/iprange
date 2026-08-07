//! Explicit cancellation shared with bounded operations.

use std::fmt;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use crate::error::{Error, Result};

/// Thread-safe cancellation token checked between bounded units of work.
#[derive(Clone)]
pub struct CancellationToken {
    cancelled: Arc<AtomicBool>,
    poll: Option<Arc<dyn Fn() -> bool + Send + Sync>>,
}

impl CancellationToken {
    /// Create an active token.
    pub fn new() -> Self {
        Self {
            cancelled: Arc::new(AtomicBool::new(false)),
            poll: None,
        }
    }

    /// Create a token that polls one binding-owned callback at every checkpoint.
    #[doc(hidden)]
    pub fn from_poll(poll: Arc<dyn Fn() -> bool + Send + Sync>) -> Self {
        Self {
            cancelled: Arc::new(AtomicBool::new(false)),
            poll: Some(poll),
        }
    }

    /// Request cancellation. Repeated requests are harmless.
    pub fn cancel(&self) {
        self.cancelled.store(true, Ordering::Release);
    }

    /// Report whether cancellation was requested.
    pub fn is_cancelled(&self) -> bool {
        self.cancelled.load(Ordering::Acquire) || self.poll.as_ref().is_some_and(|poll| poll())
    }

    pub(crate) fn check(&self) -> Result<()> {
        if self.is_cancelled() {
            Err(Error::Cancelled)
        } else {
            Ok(())
        }
    }

    pub(crate) fn requires_external_poll(&self) -> bool {
        self.poll.is_some()
    }
}

impl fmt::Debug for CancellationToken {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("CancellationToken")
            .field("cancelled", &self.cancelled.load(Ordering::Acquire))
            .field("polling", &self.poll.is_some())
            .finish()
    }
}

impl Default for CancellationToken {
    fn default() -> Self {
        Self::new()
    }
}
