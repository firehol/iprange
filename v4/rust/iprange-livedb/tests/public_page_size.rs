//! Consumers budget database pages using the public format constant.

#[test]
fn public_page_size_matches_the_v4_wire_contract() {
    const PAGE_BYTES: usize = iprange_livedb::PAGE_SIZE;
    assert_eq!(PAGE_BYTES, 4096);
}
