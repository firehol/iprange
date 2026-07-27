use std::fs;
use std::path::{Path, PathBuf};

pub(crate) mod generate;
mod model;
pub(crate) mod verify;

pub(crate) use model::{
    Corpus, Family, Fixture, InvalidCase, InvalidMutation, Kind, MetadataExpectation,
};

pub(crate) fn corpus_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance")
}

pub(crate) fn load_corpus(root: &Path) -> Corpus {
    let path = root.join("cases.json");
    let bytes = fs::read(&path)
        .unwrap_or_else(|error| panic!("failed to read {}: {error}", path.display()));
    let corpus: Corpus = serde_json::from_slice(&bytes)
        .unwrap_or_else(|error| panic!("failed to parse {}: {error}", path.display()));
    assert_eq!(corpus.schema, 1, "unsupported conformance schema");
    corpus
}
