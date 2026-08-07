use super::*;

impl PageSink for [u8; PAGE_SIZE] {
    fn fill(&mut self, value: u8) {
        <[u8]>::fill(self, value);
    }

    fn write(&mut self, at: usize, bytes: &[u8]) -> Result<()> {
        let destination = self
            .get_mut(at..at.saturating_add(bytes.len()))
            .ok_or(Error::ArithmeticOverflow("test page write"))?;
        destination.copy_from_slice(bytes);
        Ok(())
    }

    fn write_source<S: ByteSource>(&mut self, at: usize, bytes: S) -> Result<()> {
        let destination = self
            .get_mut(at..at.saturating_add(bytes.len()))
            .ok_or(Error::ArithmeticOverflow("test page write"))?;
        if bytes.copy_range_to(0, destination) {
            Ok(())
        } else {
            Err(Error::Corrupt("test page source changed"))
        }
    }

    fn copy_within(&mut self, source_at: usize, destination_at: usize, len: usize) -> Result<()> {
        let end = source_at
            .checked_add(len)
            .ok_or(Error::ArithmeticOverflow("test page copy"))?;
        if end > PAGE_SIZE || destination_at.saturating_add(len) > PAGE_SIZE {
            return Err(Error::ArithmeticOverflow("test page copy"));
        }
        <[u8]>::copy_within(self, source_at..end, destination_at);
        Ok(())
    }

    fn set_byte(&mut self, at: usize, value: u8) -> Result<()> {
        *self
            .get_mut(at)
            .ok_or(Error::ArithmeticOverflow("test page byte"))? = value;
        Ok(())
    }

    fn put_u16(&mut self, at: usize, value: u16) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }

    fn put_u32(&mut self, at: usize, value: u32) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }

    fn put_u64(&mut self, at: usize, value: u64) -> Result<()> {
        self.write(at, &value.to_le_bytes())
    }
}

impl PageEdit for [u8; PAGE_SIZE] {
    type View<'a>
        = &'a [u8; PAGE_SIZE]
    where
        Self: 'a;

    fn view(&self) -> Self::View<'_> {
        self
    }

    fn zero(&mut self, at: usize, len: usize) -> Result<()> {
        let destination = self
            .get_mut(at..at.saturating_add(len))
            .ok_or(Error::ArithmeticOverflow("test page zero"))?;
        destination.fill(0);
        Ok(())
    }
}

#[test]
fn builder_and_parser_round_trip_fixed_cells() {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
    builder.push(&[1; 12]).unwrap();
    builder.push(&[2; 12]).unwrap();
    builder.finish().unwrap();

    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert_eq!(header.item_count, 2);
    assert_eq!(cell(&page, &header, 0, 12).unwrap().as_slice(), &[1; 12]);
    assert_eq!(cell(&page, &header, 1, 12).unwrap().as_slice(), &[2; 12]);
    assert_eq!(u32_le(&page, 28), 0);
    crate::page_checksum::seal(&mut page).unwrap();
    assert_eq!(
        u32_le(&page, 28),
        crate::crc32c::crc32c_with_zeroed(&page, 28, 4).unwrap()
    );
}

#[test]
fn builder_rejects_an_overfull_or_empty_page() {
    let mut page = [0; PAGE_SIZE];
    assert!(Builder::new(&mut page, 2, 1, 0, 4).finish().is_err());

    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, 2, 1, 0, 4);
    assert!(builder.push(&[0; PAGE_SIZE]).is_err());
}

#[test]
fn in_place_insertion_changes_only_slots_and_free_space() {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
    builder.push(b"aa").unwrap();
    builder.push(b"cc").unwrap();
    builder.finish().unwrap();

    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert!(insert(&mut page, &header, 1, b"bb").unwrap());
    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert!(insert(&mut page, &header, 0, b"00").unwrap());
    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert!(insert(&mut page, &header, 4, b"zz").unwrap());

    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    let records: Vec<&[u8]> = (0..header.item_count)
        .map(|index| cell(&page, &header, index, 2).unwrap().as_slice())
        .collect();
    assert_eq!(records, [b"00", b"aa", b"bb", b"cc", b"zz"]);
    assert!(page[header.lower..header.upper]
        .iter()
        .all(|byte| *byte == 0));
}

#[test]
fn in_place_insertion_does_not_modify_a_full_page() {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
    builder.push(&[1; PAGE_SIZE - HEADER_SIZE - 2]).unwrap();
    builder.finish().unwrap();
    let before = page;
    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();

    assert!(!insert(&mut page, &header, 1, b"x").unwrap());
    assert_eq!(page, before);
}

#[test]
fn in_place_edits_clear_vacated_record_bytes() {
    let mut page = [0; PAGE_SIZE];
    let mut builder = Builder::new(&mut page, 2, 7, 0, 4);
    for cell in [b"aaaa", b"bbbb", b"cccc", b"dddd"] {
        builder.push(cell).unwrap();
    }
    builder.finish().unwrap();

    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert!(replace(&mut page, &header, 1, 4, b"b").unwrap());
    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert!(page[header.lower..header.upper]
        .iter()
        .all(|&byte| byte == 0));

    remove(&mut page, &header, 2, 4).unwrap();
    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert!(page[header.lower..header.upper]
        .iter()
        .all(|&byte| byte == 0));

    truncate(&mut page, &header, 2).unwrap();
    let header = parse(&page, 7, 2, 4, Some(0)).unwrap();
    assert!(page[header.lower..header.upper]
        .iter()
        .all(|&byte| byte == 0));
    assert_eq!(cell(&page, &header, 0, 4).unwrap().as_slice(), b"aaaa");
    assert_eq!(cell(&page, &header, 1, 1).unwrap().as_slice(), b"b");
}
