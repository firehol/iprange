fn main() {
    for cp in 0u32..=0x10FFFF {
        if (0xD800..=0xDFFF).contains(&cp) {
            continue;
        }
        let ch = char::from_u32(cp).unwrap();
        let out: String = ch.to_lowercase().collect();
        print!("U+{:04X} ", cp);
        for c in out.chars() {
            print!("U+{:04X} ", c as u32);
        }
        println!();
    }
}
