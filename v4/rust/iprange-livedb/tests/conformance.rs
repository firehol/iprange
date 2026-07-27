mod conformance_support;

use conformance_support::{corpus_root, generate, load_corpus, verify};

#[test]
fn rust_fixtures_open_and_match_the_current_v4_contract() {
    let root = corpus_root();
    let corpus = load_corpus(&root);
    verify::corpus(&root, &corpus);
}

#[test]
#[ignore = "explicitly regenerates committed conformance fixtures"]
fn regenerate_rust_fixtures() {
    let root = corpus_root();
    let corpus = load_corpus(&root);
    generate::rust_fixtures(&root, &corpus);
    verify::corpus(&root, &corpus);
}
