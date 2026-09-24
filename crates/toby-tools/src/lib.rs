//! Tool manifests, templating and configuration file patching (plan §16.1).
//!
//! A manifest says how to check, install, update and launch a tool inside a
//! machine, which environment it gets when a model provider is configured
//! for it, which files to write, and which ports its login needs forwarded.
//! Built-in manifests can be overridden or extended with files in
//! `~/.config/toby/tools/`.

use std::collections::BTreeMap;
use std::io;
use std::path::Path;

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Manifest {
    pub tool: Tool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Tool {
    pub name: String,
    /// Commands the root must provide (`command -v`).
    #[serde(default)]
    pub requires: Vec<String>,
    /// Succeeds when the tool is installed.
    pub check: Vec<String>,
    /// Runs as the user when `check` fails.
    pub install: Script,
    /// Runs as the user on an upgrade request; default: `install`.
    pub update: Option<Script>,
    pub launch: Vec<String>,
    /// Arguments that skip the tool's permission prompts (`--yolo`).
    #[serde(default)]
    pub yolo: Vec<String>,
    /// Directories added to `PATH` for the tool (`~/.local/bin` always is).
    #[serde(default)]
    pub path: Vec<String>,
    /// Environment of every launch (templates).
    #[serde(default)]
    pub env: BTreeMap<String, String>,
    /// Applied when a model provider is configured for the tool.
    pub models: Option<Models>,
    #[serde(default)]
    pub files: Vec<File>,
    pub mcp: Option<Mcp>,
    #[serde(default)]
    pub forwards: Vec<ToolForward>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Script {
    pub script: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Models {
    /// The API the tool speaks.
    pub protocol: Protocol,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Protocol {
    Anthropic,
    Openai,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Format {
    Json,
    Toml,
    Text,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Mode {
    Merge,
    Replace,
}

/// A file written into the home before launch.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct File {
    pub path: String,
    pub format: Format,
    pub mode: Mode,
    pub template: String,
}

/// Where MCP server entries go (plan §16.3).
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Mcp {
    pub path: String,
    pub format: Format,
    /// JSON pointer of the object entries are written to.
    pub pointer: String,
    /// A server reached with `toby-connect` (Toby's own, isolated ones).
    pub entry: String,
    /// An HTTP server behind the proxy (`url`).
    pub http_entry: Option<String>,
    /// A server run in the machine (`command`, `args`).
    pub command_entry: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum When {
    /// While a session of the tool runs, so a login's callback reaches it.
    Login,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ToolForward {
    pub when: When,
    /// `host-to-guest` or `guest-to-host`.
    pub direction: String,
    pub port: u16,
}

const BUILTIN: &[&str] = &[
    include_str!("../tools/claude.toml"),
    include_str!("../tools/codex.toml"),
    include_str!("../tools/opencode.toml"),
];

fn parse(text: &str, origin: &str) -> io::Result<Manifest> {
    toml::from_str(text).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, format!("{origin}: {e}")))
}

/// The built-in manifests.
pub fn builtin() -> Vec<Manifest> {
    BUILTIN.iter().map(|t| parse(t, "built-in tool").expect("built-in manifests parse")).collect()
}

/// Built-in manifests, overridden and extended by `*.toml` files in `dir`.
pub fn load(dir: &Path) -> io::Result<BTreeMap<String, Manifest>> {
    let mut out: BTreeMap<String, Manifest> =
        builtin().into_iter().map(|m| (m.tool.name.clone(), m)).collect();
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(out),
        Err(e) => return Err(e),
    };
    let mut paths: Vec<_> =
        entries.flatten().map(|e| e.path()).filter(|p| p.extension().is_some_and(|x| x == "toml")).collect();
    paths.sort();
    for p in paths {
        let m = parse(&std::fs::read_to_string(&p)?, &p.display().to_string())?;
        out.insert(m.tool.name.clone(), m);
    }
    Ok(out)
}

/// What templates see.
#[derive(Debug, Clone, Default, Serialize)]
pub struct Context {
    /// The models proxy for the tool's provider, if one is configured.
    pub models: Option<ModelsContext>,
    pub user: String,
    pub home: String,
    pub workspace: String,
    /// `toby-connect` in the guest.
    pub connect: String,
    /// MCP server name, when rendering an MCP entry.
    pub name: String,
    /// An HTTP MCP server's URL through the proxy.
    pub url: String,
    /// A machine MCP server's command and arguments.
    pub command: String,
    pub args: Vec<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ModelsContext {
    pub url: String,
    pub token: String,
}

/// Renders a template; undefined values are errors.
pub fn render(template: &str, ctx: &Context) -> Result<String, String> {
    let mut env = minijinja::Environment::new();
    env.set_undefined_behavior(minijinja::UndefinedBehavior::Strict);
    env.render_str(template, ctx).map_err(|e| e.to_string())
}

/// Deep-merges `patch` into `base`: objects merge key by key, anything else
/// is replaced.
fn merge_value(base: &mut serde_json::Value, patch: serde_json::Value) {
    match (base, patch) {
        (serde_json::Value::Object(b), serde_json::Value::Object(p)) => {
            for (k, v) in p {
                merge_value(b.entry(k).or_insert(serde_json::Value::Null), v);
            }
        }
        (b, p) => *b = p,
    }
}

/// Merges `patch` into `base` key by key, keeping the base's comments,
/// order and formatting.
fn merge_toml_tables(base: &mut dyn toml_edit::TableLike, patch: &dyn toml_edit::TableLike) {
    for (key, item) in patch.iter() {
        let merged = match (base.get_mut(key), item) {
            (Some(b), p) if b.is_table_like() && p.is_table_like() => {
                merge_toml_tables(b.as_table_like_mut().expect("table"), p.as_table_like().expect("table"));
                true
            }
            _ => false,
        };
        if !merged {
            base.insert(key, item.clone());
        }
    }
}

/// The new content of a file: `content` merged into or replacing `existing`.
/// Formats are JSON, TOML or text (always replaced); merging keeps what the
/// file already has, including a TOML file's comments and layout.
pub fn patch(existing: Option<&str>, content: &str, format: Format, mode: Mode) -> Result<String, String> {
    if format == Format::Toml {
        let patch: toml_edit::DocumentMut =
            content.parse().map_err(|e: toml_edit::TomlError| e.to_string())?;
        return match (mode, existing.filter(|e| !e.trim().is_empty())) {
            (Mode::Merge, Some(e)) => {
                let mut base: toml_edit::DocumentMut =
                    e.parse().map_err(|e: toml_edit::TomlError| e.to_string())?;
                merge_toml_tables(base.as_table_mut(), patch.as_table());
                Ok(base.to_string())
            }
            _ => Ok(patch.to_string()),
        };
    }
    let parse = |text: &str| -> Result<serde_json::Value, String> {
        match format {
            Format::Json => serde_json::from_str(text).map_err(|e| e.to_string()),
            Format::Toml => toml::from_str::<serde_json::Value>(text).map_err(|e| e.to_string()),
            Format::Text => Ok(serde_json::Value::String(text.into())),
        }
    };
    let patch = parse(content)?;
    let value = match (mode, existing.filter(|e| !e.trim().is_empty())) {
        (Mode::Merge, Some(e)) if format != Format::Text => {
            let mut base = parse(e)?;
            merge_value(&mut base, patch);
            base
        }
        _ => patch,
    };
    match format {
        Format::Json => serde_json::to_string_pretty(&value).map(|s| s + "\n").map_err(|e| e.to_string()),
        Format::Toml => toml::to_string_pretty(&value).map_err(|e| e.to_string()),
        Format::Text => Ok(value.as_str().unwrap_or_default().to_string()),
    }
}

/// Wraps `entry` so that it sits at the JSON `pointer` (such as
/// `/mcpServers/<name>`), for merging into a file.
pub fn at_pointer(pointer: &str, entry: serde_json::Value) -> serde_json::Value {
    pointer.split('/').filter(|s| !s.is_empty()).rev().fold(entry, |acc, key| {
        let key = key.replace("~1", "/").replace("~0", "~");
        serde_json::json!({ key: acc })
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builtins_parse() {
        let names: Vec<String> = builtin().into_iter().map(|m| m.tool.name).collect();
        assert_eq!(names, ["claude", "codex", "opencode"]);
    }

    #[test]
    fn user_manifests_override_and_extend() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(
            dir.path().join("claude.toml"),
            "[tool]\nname = \"claude\"\ncheck = [\"true\"]\ninstall = { script = \"true\" }\nlaunch = [\"claude\", \"--x\"]\n",
        )
        .unwrap();
        std::fs::write(
            dir.path().join("mine.toml"),
            "[tool]\nname = \"mine\"\ncheck = [\"mine\"]\ninstall = { script = \"true\" }\nlaunch = [\"mine\"]\n",
        )
        .unwrap();
        let all = load(dir.path()).unwrap();
        assert_eq!(all["claude"].tool.launch, ["claude", "--x"]);
        assert!(all.contains_key("mine") && all.contains_key("codex"));
        std::fs::write(dir.path().join("bad.toml"), "[tool]\nname = 1\n").unwrap();
        assert!(load(dir.path()).is_err());
    }

    #[test]
    fn templates_render_strictly() {
        let ctx = Context {
            models: Some(ModelsContext { url: "http://127.0.0.1:41100/anthropic".into(), token: "t".into() }),
            ..Default::default()
        };
        assert_eq!(render("{{ models.url }}/v1", &ctx).unwrap(), "http://127.0.0.1:41100/anthropic/v1");
        assert!(render("{{ nothing }}", &ctx).is_err());
    }

    #[test]
    fn files_merge_or_replace() {
        let merged =
            patch(Some(r#"{"a":1,"env":{"X":"1"}}"#), r#"{"env":{"Y":"2"}}"#, Format::Json, Mode::Merge)
                .unwrap();
        let v: serde_json::Value = serde_json::from_str(&merged).unwrap();
        assert_eq!(v, serde_json::json!({"a":1,"env":{"X":"1","Y":"2"}}));
        let replaced = patch(Some(r#"{"a":1}"#), r#"{"b":2}"#, Format::Json, Mode::Replace).unwrap();
        assert_eq!(serde_json::from_str::<serde_json::Value>(&replaced).unwrap(), serde_json::json!({"b":2}));
        let toml = patch(
            Some("# mine\nz = 1\nx = 1\n[t]\na = 1 # keep\n"),
            "[t]\nb = 2\n[s.u]\nc = 3\n",
            Format::Toml,
            Mode::Merge,
        )
        .unwrap();
        assert!(toml.starts_with("# mine\nz = 1\nx = 1\n"), "{toml}");
        assert!(toml.contains("a = 1 # keep") && toml.contains("b = 2") && toml.contains("c = 3"), "{toml}");
        let json = patch(Some(r#"{"z":1,"a":2}"#), r#"{"m":3}"#, Format::Json, Mode::Merge).unwrap();
        assert!(json.find("\"z\"").unwrap() < json.find("\"a\"").unwrap(), "{json}");
        assert_eq!(patch(Some("old"), "new", Format::Text, Mode::Merge).unwrap(), "new");
        assert!(patch(Some("{"), "{}", Format::Json, Mode::Merge).is_err());
    }

    #[test]
    fn mcp_entries_render_to_json() {
        let ctx = Context {
            connect: "/run/toby/bin/toby-connect".into(),
            name: "gh".into(),
            url: "http://127.0.0.1:41100/mcp/gh".into(),
            command: "npx".into(),
            args: vec!["-y".into(), "srv \"x\"".into()],
            ..Default::default()
        };
        for m in builtin() {
            let mcp = m.tool.mcp.expect("built-in tools take MCP servers");
            for t in
                [Some(&mcp.entry), mcp.http_entry.as_ref(), mcp.command_entry.as_ref()].into_iter().flatten()
            {
                let out = render(t, &ctx).unwrap();
                serde_json::from_str::<serde_json::Value>(&out)
                    .unwrap_or_else(|e| panic!("{}: {out}: {e}", m.tool.name));
            }
        }
    }

    #[test]
    fn pointers_nest_entries() {
        let v = at_pointer("/mcpServers/github", serde_json::json!({"command": "c"}));
        assert_eq!(v, serde_json::json!({"mcpServers": {"github": {"command": "c"}}}));
    }
}
