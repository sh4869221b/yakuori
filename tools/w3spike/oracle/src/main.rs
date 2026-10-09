// Thin research-only driver for the independently published crates.io implementation.
// Never linked into or distributed with Yakuori.
use std::{collections::HashMap, env, fs};
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<String> = env::args().collect();
    if args.len() != 4 { return Err("usage: oracle encode|decode INPUT OUTPUT".into()); }
    match args[1].as_str() {
        "encode" => fs::write(&args[3], w3strings::encode(&fs::read_to_string(&args[2])?)?)?,
        "decode" => fs::write(&args[3], w3strings::decode(&fs::read(&args[2])?, &HashMap::new())?)?,
        _ => return Err("unknown operation".into()),
    }
    Ok(())
}
