use super::*;

pub(crate) fn map(file: &File) -> Result<Mapping> {
    Mapping::read_only_view(file, file.metadata()?.len())
}

pub(crate) fn read_exact_at(file: &File, output: &mut [u8], offset: u64) -> Result<()> {
    let mapping = map(file)?;
    if mapping.bytes(offset, output.len())?.copy_to(output) {
        Ok(())
    } else {
        Err(Error::Corrupt("test mapping changed while copying"))
    }
}

pub(crate) fn write_exact_at(file: &File, input: &[u8], offset: u64) -> Result<()> {
    let mut mapping = Mapping::read_write_view(file, file.metadata()?.len())?;
    mapping.bytes_mut(offset, input.len())?.write(0, input)
}

pub(crate) fn read_page(
    file: &File,
    page_number: u32,
    page_limit: u64,
    output: &mut [u8; PAGE_SIZE],
) -> Result<()> {
    let mapping = map(file)?;
    if mapping
        .page(page_number, page_limit)?
        .copy_range_to(0, output)
    {
        Ok(())
    } else {
        Err(Error::Corrupt("test page changed while copying"))
    }
}
