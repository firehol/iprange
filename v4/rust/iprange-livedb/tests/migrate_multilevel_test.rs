use iprange_livedb::{Ipv4Key, Writer, DesiredRecord, SortedStream, MigrateOptions};
use iprange_livedb::page_store::{PageStore, VecPageStore};

#[test]
fn migrate_multilevel_tree() {
    let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
    for i in 0..1000u32 {
        w.set(Ipv4Key(i * 10), Ipv4Key(i * 10 + 5), i).unwrap();
    }
    w.commit(0, u64::MAX).unwrap();
    
    let img = w.into_image().unwrap();
    let store: Box<dyn PageStore> = Box::new(VecPageStore::new(img));
    let mut w2 = Writer::<Ipv4Key>::open(store).unwrap();
    
    // Migrate to a slightly different dataset
    let desired: Vec<DesiredRecord<Ipv4Key>> = (0..1000u32)
        .map(|i| DesiredRecord { from: Ipv4Key(i * 10), to: Ipv4Key(i * 10 + 5), scope_id: i })
        .collect();
    let mut stream = SortedStream::from_unsorted(desired);
    let result = iprange_livedb::migrate(&mut w2, &mut stream, &MigrateOptions::default());
    eprintln!("migrate result: {:?}", result.as_ref().map(|c| (c.added, c.removed, c.changed, c.unchanged)));
    result.unwrap();
    w2.commit(1, u64::MAX).unwrap();
}
