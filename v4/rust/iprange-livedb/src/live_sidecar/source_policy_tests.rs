use super::*;

/// Serializes this test against every other switch-asserting test in
/// the lib binary through the crate's own lock: an unserialized flip
/// could race a CreatorOnlyGuard holder's create (the 54a3761a
/// class). The crafted-header test itself has no unix constructs, so
/// the module is portable; the FIFO twin lives in the integration
/// binary where its own ENV_LOCK serializes it.
fn with_env_lock(body: impl FnOnce()) {
    let _lock = crate::publication::security::CREATOR_ONLY_ENV_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    body();
}

fn unique_path(label: &str) -> std::path::PathBuf {
    let unique = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    std::env::temp_dir().join(format!(
        "iprange-v4-sidecar-policy-{label}-{}-{unique}",
        std::process::id()
    ))
}

// A valid-CRC header whose identities are zero is corrupt under the
// full read_header rule: the classifier must follow the process
// switch, never the policy byte such a header carries. Mutation
// probe: removing the identity check flips the answer to protected.
#[test]
fn classifier_rejects_a_zero_identity_header() {
    with_env_lock(|| {
        let directory = unique_path("zero-identity");
        let _ = std::fs::remove_dir_all(&directory);
        std::fs::create_dir_all(&directory).unwrap();
        let main = directory.join("crafted.iprdb");
        crate::create_live(
            &main,
            crate::AddressFamily::Ipv4,
            crate::ValueKind::Direct,
            crate::contract::StructureKind::None,
            crate::ValueTag::new(b"asn").unwrap(),
            2,
            &crate::CancellationToken::new(),
            true,
        )
        .unwrap();
        let sidecar = path::canonical_sidecar(&main).unwrap();
        let mut page = std::fs::read(&sidecar).unwrap();
        // Zero both identities ([32..64)), keep the protected policy
        // byte, then fix the header CRC so only the identity rule can
        // refuse it.
        for byte in page.iter_mut().skip(32).take(32) {
            *byte = 0;
        }
        let checksum = crate::crc32c::crc32c_with_zeroed(&page[..4096], 64, 4).unwrap();
        page[64..68].copy_from_slice(&checksum.to_le_bytes());
        std::fs::write(&sidecar, &page).unwrap();

        let previous = std::env::var_os("IPRANGE_CREATOR_ONLY");
        std::env::set_var("IPRANGE_CREATOR_ONLY", "0");
        assert!(
            !crate::source_creator_only(&main),
            "zero-identity header answered protected instead of following the switch"
        );
        std::env::set_var("IPRANGE_CREATOR_ONLY", "1");
        assert!(
            crate::source_creator_only(&main),
            "zero-identity header with the switch on must follow the switch"
        );
        match previous {
            Some(value) => std::env::set_var("IPRANGE_CREATOR_ONLY", value),
            None => std::env::remove_var("IPRANGE_CREATOR_ONLY"),
        }
        let _ = std::fs::remove_dir_all(&directory);
    })
}
