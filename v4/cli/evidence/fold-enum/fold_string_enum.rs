// Wave 19.13 fold parity: fold a corpus that places every scalar in
// both sigma-neighbor contexts (before and after U+03A3), plus fixed
// boundary spellings, and emit the result for hashing.  The Go
// production fold test and the Rust production fold test pin the
// same expected sha256 of this output.
use std::io::Write;

fn main() {
    let mut corpus = String::with_capacity(14_000_000);
    for cp in 0u32..=0x10FFFF {
        if (0xD800..=0xDFFF).contains(&cp) {
            continue;
        }
        let c = char::from_u32(cp).unwrap();
        corpus.push(c); // sigma preceded by c, followed by '1'
        corpus.push('\u{03A3}');
        corpus.push('1');
    }
    for cp in 0u32..=0x10FFFF {
        if (0xD800..=0xDFFF).contains(&cp) {
            continue;
        }
        let c = char::from_u32(cp).unwrap();
        corpus.push('a'); // sigma preceded by 'a', followed by c
        corpus.push('\u{03A3}');
        corpus.push(c);
        corpus.push('1');
    }
    // Fixed boundary / context spellings.
    corpus.push_str("a\u{03A3}a\u{03A3}a\u{03A3}\u{0308}a\u{0308}\u{03A3}\u{03A3}1\u{03A3}\u{2160}\u{03A3}a\u{03A3}\u{0345}a\u{0345}\u{03A3}a\u{03A3}\u{1C89}\u{03A3}");
    let out = corpus.to_lowercase();
    let mut f = std::fs::File::create("corpus_lower.rs.txt").unwrap();
    f.write_all(out.as_bytes()).unwrap();
    println!("corpus chars: {}", corpus.chars().count());
    println!("out bytes: {}", out.len());
}
