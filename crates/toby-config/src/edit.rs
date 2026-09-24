//! `toby config get` and `toby config set`: dotted keys in the global
//! configuration file, edited in place.

use toml_edit::{DocumentMut, Item, Table, Value};

use crate::global::GlobalConfig;

fn parse(text: &str) -> Result<DocumentMut, String> {
    text.parse::<DocumentMut>().map_err(|e| e.to_string())
}

/// Splits `a.b."c.d"` into its keys.
fn keys(key: &str) -> Result<Vec<String>, String> {
    let parsed =
        toml_edit::Key::parse(key).map_err(|_| format!("{key:?} is not a key such as daemon.backend"))?;
    Ok(parsed.into_iter().map(|k| k.get().to_string()).collect())
}

/// The value at `key`: a string as it is, anything else as TOML.
pub fn get(text: &str, key: &str) -> Result<Option<String>, String> {
    let doc = parse(text)?;
    let mut item = doc.as_item();
    for k in keys(key)? {
        match item.get(&k) {
            Some(i) => item = i,
            None => return Ok(None),
        }
    }
    Ok(Some(match item {
        Item::Value(Value::String(s)) => s.value().clone(),
        Item::Value(v) => v.clone().decorated("", "").to_string(),
        Item::Table(t) => t.to_string().trim_end().to_string(),
        Item::ArrayOfTables(a) => a.to_string().trim_end().to_string(),
        Item::None => return Ok(None),
    }))
}

/// Sets `key` to `value` (TOML, or else a string) and returns the new file,
/// which must still be a valid configuration.
pub fn set(text: &str, key: &str, value: &str) -> Result<String, String> {
    let mut doc = parse(text)?;
    let value = format!("v = {value}")
        .parse::<DocumentMut>()
        .ok()
        .and_then(|d| d.get("v").and_then(Item::as_value).cloned())
        .unwrap_or_else(|| Value::from(value));
    let keys = keys(key)?;
    let (last, tables) = keys.split_last().ok_or("an empty key")?;
    let mut table: &mut Table = doc.as_table_mut();
    for k in tables {
        let item = table.entry(k).or_insert_with(|| {
            let mut t = Table::new();
            t.set_implicit(true);
            Item::Table(t)
        });
        table = item.as_table_mut().ok_or_else(|| format!("{k} is not a table"))?;
    }
    table.insert(last, Item::Value(value));
    let out = doc.to_string();
    toml::from_str::<GlobalConfig>(&out).map_err(|e| e.message().to_string())?;
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn gets_and_sets_keys() {
        let text = "# mine\n[daemon]\nbackend = \"direct\"\n\n[settings]\nyolo = false\n";
        assert_eq!(get(text, "daemon.backend").unwrap().as_deref(), Some("direct"));
        assert_eq!(get(text, "settings.yolo").unwrap().as_deref(), Some("false"));
        assert_eq!(get(text, "settings.projects_dir").unwrap(), None);

        let out = set(text, "settings.yolo", "true").unwrap();
        assert!(out.starts_with("# mine\n") && out.contains("yolo = true"), "{out}");
        let out = set(&out, "settings.projects_dir", "~/src").unwrap();
        assert_eq!(get(&out, "settings.projects_dir").unwrap().as_deref(), Some("~/src"));
        let out = set(&out, "settings.suppress_warnings", "[\"*\"]").unwrap();
        assert_eq!(get(&out, "settings.suppress_warnings").unwrap().as_deref(), Some("[\"*\"]"));
        let out = set("", "tools.claude.params", "[\"--verbose\"]").unwrap();
        assert!(toml::from_str::<GlobalConfig>(&out).unwrap().tools["claude"].params == ["--verbose"]);

        assert!(set(text, "daemon.no_such_key", "1").is_err());
        assert!(set(text, "settings.yolo", "maybe").is_err());
        assert!(set(text, "daemon.backend.x", "1").is_err());
    }
}
