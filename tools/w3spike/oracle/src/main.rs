// Thin research-only driver for the independently published crates.io implementation.
// Never linked into or distributed with Yakuori.
use std::{collections::HashMap, env, fs, io::Cursor};

fn records(data: &[u8]) -> Result<Vec<u8>, Box<dyn std::error::Error>> {
    let file = w3strings::W3StringFile::read(&mut Cursor::new(data))?;
    let mut output = Vec::new();
    output.extend_from_slice(b"YAKW3O1\0");
    output.extend_from_slice(&file.version.to_le_bytes());
    let language = file.language.handle.as_bytes();
    output.extend_from_slice(&u32::try_from(language.len())?.to_le_bytes());
    output.extend_from_slice(language);
    output.extend_from_slice(&u32::try_from(file.block1.len())?.to_le_bytes());
    for record in file.block1 {
        output.extend_from_slice(&record.str_id.to_le_bytes());
        output.extend_from_slice(&u32::try_from(record.str.len())?.to_le_bytes());
        output.extend_from_slice(record.str.as_bytes());
    }
    output.extend_from_slice(&u32::try_from(file.block2.len())?.to_le_bytes());
    for relation in file.block2 {
        output.extend_from_slice(&relation.str_id.to_le_bytes());
        output.extend_from_slice(&relation.str_key_hex.to_le_bytes());
    }
    Ok(output)
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<String> = env::args().collect();
    if args.len() != 4 { return Err("usage: oracle encode|decode|records INPUT OUTPUT".into()); }
    match args[1].as_str() {
        "encode" => fs::write(&args[3], w3strings::encode(&fs::read_to_string(&args[2])?)?)?,
        "decode" => fs::write(&args[3], w3strings::decode(&fs::read(&args[2])?, &HashMap::new())?)?,
        "records" => fs::write(&args[3], records(&fs::read(&args[2])?)?)?,
        _ => return Err("unknown operation".into()),
    }
    Ok(())
}
