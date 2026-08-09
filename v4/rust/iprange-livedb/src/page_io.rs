//! Mutable access to a database page at its final mapped offset.

use crate::error::Result;
use crate::mapping::{ByteSource, PageMut, PageView};

pub(crate) trait PageSink {
    fn fill(&mut self, value: u8);
    fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()>;
    fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()>;
    fn copy_within(&mut self, source_at: usize, destination_at: usize, len: usize) -> Result<()>;
    fn set_byte(&mut self, at: usize, value: u8) -> Result<()>;
    fn put_u16(&mut self, at: usize, value: u16) -> Result<()>;
    fn put_u32(&mut self, at: usize, value: u32) -> Result<()>;
    fn put_u64(&mut self, at: usize, value: u64) -> Result<()>;
}

pub(crate) trait PageEdit: PageSink {
    type View<'a>: ByteSource
    where
        Self: 'a;

    fn view(&self) -> Self::View<'_>;
    fn zero(&mut self, at: usize, len: usize) -> Result<()>;
}

impl PageSink for PageMut<'_> {
    fn fill(&mut self, value: u8) {
        PageMut::fill(self, value);
    }

    fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        PageMut::write(self, at, bytes)
    }

    fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()> {
        PageMut::write_source(self, at, bytes)
    }

    fn copy_within(&mut self, source_at: usize, destination_at: usize, len: usize) -> Result<()> {
        PageMut::copy_within(self, source_at, destination_at, len)
    }

    fn set_byte(&mut self, at: usize, value: u8) -> Result<()> {
        PageMut::set_byte(self, at, value)
    }

    fn put_u16(&mut self, at: usize, value: u16) -> Result<()> {
        PageMut::put_u16(self, at, value)
    }

    fn put_u32(&mut self, at: usize, value: u32) -> Result<()> {
        PageMut::put_u32(self, at, value)
    }

    fn put_u64(&mut self, at: usize, value: u64) -> Result<()> {
        PageMut::put_u64(self, at, value)
    }
}

impl PageEdit for PageMut<'_> {
    type View<'a>
        = PageView<'a>
    where
        Self: 'a;

    fn view(&self) -> Self::View<'_> {
        PageMut::view(self)
    }

    fn zero(&mut self, at: usize, len: usize) -> Result<()> {
        PageMut::zero(self, at, len)
    }
}
