//! Sequential MVCC test — no fork. Verifies reader snapshot isolation
//! when writer commits 2+ subsequent transactions (the off-by-one in
//! reclaimable only manifests on the second commit).
use iprange_livedb::os::{FileWriter, MmapReader};
use iprange_livedb::Ipv4Key;

fn temp_db(name: &str) -> std::path::PathBuf {
    let pid = std::process::id();
    let p = std::env::temp_dir().join(format!("iprange_seq_{}_{}.iprdb", name, pid));
    let _ = std::fs::remove_file(&p);
    let _ = std::fs::remove_file(p.with_extension("iprdb.readers"));
    p
}

#[test]
fn sequential_mvcc_two_commits() {
    let path = temp_db("smvcc");

    // 1. Create + populate txn 1.
    {
        let mut w = FileWriter::<Ipv4Key>::create(&path, 0, 0).unwrap();
        for i in 0..500u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i).unwrap();
        }
        w.commit(1).unwrap();
        w.close();
    }

    // 2. Open reader (pins txn 1).
    let rdr = MmapReader::open(&path).unwrap();

    // 3. Verify txn 1 data via reader.
    {
        let r = rdr.reader().unwrap();
        for i in 0..10u32 {
            assert_eq!(
                r.lookup(Ipv4Key(i * 50)).unwrap(),
                Some(i * 50),
                "initial read at {}",
                i * 50
            );
        }
    }

    // 4. Writer commits txn 2 (churn).
    {
        let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();
        for i in 0..500u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..500u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i + 100).unwrap();
        }
        w.commit(2).unwrap();
        w.close();
    }

    // 5. Reader should STILL see txn 1 data after first commit.
    {
        let r = rdr.reader().unwrap();
        for i in 0..10u32 {
            assert_eq!(
                r.lookup(Ipv4Key(i * 50)).unwrap(),
                Some(i * 50),
                "MVCC violation after commit 2 at {}",
                i * 50
            );
        }
    }

    // 6. Writer commits txn 3 (second churn — this is where the off-by-one
    //    would reclaim pages still needed by the reader).
    {
        let mut w = FileWriter::<Ipv4Key>::open(&path).unwrap();
        for i in 0..500u32 {
            w.delete(Ipv4Key(i), Ipv4Key(i)).unwrap();
        }
        for i in 0..500u32 {
            w.set(Ipv4Key(i), Ipv4Key(i), i + 200).unwrap();
        }
        w.commit(3).unwrap();
        w.close();
    }

    // 7. Reader should STILL see txn 1 data after second commit.
    {
        let r = rdr.reader().unwrap();
        for i in 0..10u32 {
            assert_eq!(
                r.lookup(Ipv4Key(i * 50)).unwrap(),
                Some(i * 50),
                "MVCC violation after commit 3 at {}",
                i * 50
            );
        }
    }

    // 8. A NEW reader sees txn 3 data.
    let rdr2 = MmapReader::open(&path).unwrap();
    {
        let r = rdr2.reader().unwrap();
        for i in 0..10u32 {
            assert_eq!(
                r.lookup(Ipv4Key(i * 50)).unwrap(),
                Some(i * 50 + 200),
                "new reader should see latest at {}",
                i * 50
            );
        }
    }

    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(path.with_extension("iprdb.readers"));
}
