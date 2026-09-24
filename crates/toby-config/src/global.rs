//! The global configuration file, `~/.config/toby/config.toml` (plan §14.1).

use std::io;
use std::path::{Path, PathBuf};

use serde::Deserialize;

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct GlobalConfig {
    #[serde(default)]
    pub daemon: Daemon,
    #[serde(default)]
    pub paths: PathsConfig,
    #[serde(default)]
    pub programs: Programs,
    #[serde(default)]
    pub network: Network,
    #[serde(default)]
    pub settings: Settings,
    #[serde(default)]
    pub defaults: Defaults,
    /// Model providers, by name (plan §16.2).
    #[serde(default)]
    pub models: std::collections::BTreeMap<String, ModelProvider>,
    /// Per-tool settings, by tool name (plan §14.1).
    #[serde(default)]
    pub tools: std::collections::BTreeMap<String, ToolSettings>,
    /// MCP servers, by name (plan §16.3).
    #[serde(default)]
    pub mcp: std::collections::BTreeMap<String, McpServer>,
    #[serde(default)]
    pub permissions: Permissions,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum McpKind {
    Stdio,
    Http,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Placement {
    /// In the tool's machine, as the user; no secrets.
    Machine,
    /// In a services machine of its own, with its secrets.
    Isolated,
}

/// An MCP server (plan §16.3). Values in `env`, `url` and `headers` may use
/// substitutions.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct McpServer {
    pub kind: McpKind,
    /// stdio: the command.
    #[serde(default)]
    pub command: Vec<String>,
    /// stdio: its environment.
    #[serde(default)]
    pub env: std::collections::BTreeMap<String, String>,
    /// stdio: where it runs; default isolated when it uses substitutions.
    pub placement: Option<Placement>,
    /// http: the server's URL.
    pub url: Option<String>,
    /// http: headers added by the proxy.
    #[serde(default)]
    pub headers: std::collections::BTreeMap<String, String>,
}

fn uses_substitutions(s: &str) -> bool {
    s.contains("{file:") || s.contains("{env:")
}

impl McpServer {
    /// Where a stdio server runs.
    pub fn placement(&self) -> Placement {
        self.placement.unwrap_or_else(|| {
            let secret = self.env.values().any(|v| uses_substitutions(v))
                || self.command.iter().any(|c| uses_substitutions(c));
            if secret { Placement::Isolated } else { Placement::Machine }
        })
    }

    /// Checks the combination of fields.
    pub fn check(&self, name: &str) -> Result<(), String> {
        match self.kind {
            McpKind::Stdio if self.command.is_empty() => {
                Err(format!("mcp.{name}: a stdio server needs a command"))
            }
            McpKind::Stdio
                if self.placement() == Placement::Machine
                    && (self.env.values().any(|v| uses_substitutions(v))
                        || self.command.iter().any(|c| uses_substitutions(c))) =>
            {
                Err(format!(
                    "mcp.{name}: a server that runs in the tool's machine cannot use secrets; use placement = \"isolated\""
                ))
            }
            McpKind::Http if self.url.is_none() => Err(format!("mcp.{name}: an http server needs a url")),
            _ => Ok(()),
        }
    }
}

/// How Toby MCP host actions are decided (plan §14.6).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum ActionPolicy {
    Allow,
    Deny,
    /// Ask, unless a tool in the machine runs with `--yolo` (or
    /// `settings.yolo`).
    Ask,
    /// Ask, even with `--yolo`.
    AlwaysAsk,
}

/// The Toby MCP host actions `[permissions.actions]` can name.
pub const ACTIONS: &[&str] = &["git.fetch", "git.push", "forward", "session.info"];

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Permissions {
    /// Host actions such as `git.push`, by name.
    #[serde(default, deserialize_with = "actions")]
    pub actions: std::collections::BTreeMap<String, ActionPolicy>,
}

fn actions<'de, D: serde::Deserializer<'de>>(
    d: D,
) -> Result<std::collections::BTreeMap<String, ActionPolicy>, D::Error> {
    let map = std::collections::BTreeMap::<String, ActionPolicy>::deserialize(d)?;
    match map.keys().find(|k| !ACTIONS.contains(&k.as_str())) {
        Some(k) => {
            Err(serde::de::Error::custom(format!("unknown action {k:?}; actions are {}", ACTIONS.join(", "))))
        }
        None => Ok(map),
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ToolSettings {
    /// The model provider the tool uses through the models proxy; without
    /// one the tool uses its own login.
    pub models: Option<String>,
    /// MCP servers the tool gets.
    #[serde(default)]
    pub mcp: Vec<String>,
    /// Extra arguments of every launch.
    #[serde(default)]
    pub params: Vec<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Protocol {
    Anthropic,
    Openai,
}

/// A model provider the models proxy forwards to. Header values may use
/// substitutions (`{file:…}`, `{env:…}`).
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ModelProvider {
    pub protocol: Protocol,
    /// Display name.
    pub name: Option<String>,
    pub url: String,
    #[serde(default)]
    pub headers: std::collections::BTreeMap<String, String>,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Backend {
    #[default]
    SystemdUser,
    Direct,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Daemon {
    #[serde(default)]
    pub backend: Backend,
    /// How long a machine without sessions keeps running, e.g. `"15m"`;
    /// `"0"` keeps machines running.
    pub idle_timeout: Option<String>,
}

impl Daemon {
    pub fn idle_timeout(&self) -> io::Result<Option<std::time::Duration>> {
        match &self.idle_timeout {
            None => Ok(Some(std::time::Duration::from_secs(15 * 60))),
            Some(s) => match parse_duration(s) {
                Some(d) if d.is_zero() => Ok(None),
                Some(d) => Ok(Some(d)),
                None => Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    format!("daemon.idle_timeout: {s:?} is not a duration such as \"15m\""),
                )),
            },
        }
    }
}

/// Parses durations such as `30s`, `15m`, `2h` or `0`.
pub fn parse_duration(s: &str) -> Option<std::time::Duration> {
    let s = s.trim();
    if s == "0" {
        return Some(std::time::Duration::ZERO);
    }
    let (num, unit) = s.split_at(s.find(|c: char| !c.is_ascii_digit())?);
    let n: u64 = num.parse().ok()?;
    let secs = match unit {
        "s" => n,
        "m" => n.checked_mul(60)?,
        "h" => n.checked_mul(3600)?,
        _ => return None,
    };
    Some(std::time::Duration::from_secs(secs))
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Settings {
    /// Warning IDs not to print, or `"*"` for all.
    #[serde(default)]
    pub suppress_warnings: Vec<String>,
    /// Launch tools without their permission prompts.
    #[serde(default)]
    pub yolo: bool,
}

impl Settings {
    pub fn suppressed(&self, id: &str) -> bool {
        self.suppress_warnings.iter().any(|w| w == "*" || w == id)
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Defaults {
    /// Home used when none is given (default `default`).
    pub home: Option<String>,
}

impl Defaults {
    pub fn home(&self) -> &str {
        self.home.as_deref().unwrap_or("default")
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PathsConfig {
    pub storage_root: Option<String>,
    pub state_root: Option<String>,
}

/// Program locations; unset entries use the bundled or system defaults.
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Programs {
    pub cloud_hypervisor: Option<PathBuf>,
    pub firmware: Option<PathBuf>,
    pub passt: Option<PathBuf>,
    /// Directory of installed Toby versions (`<version>/toby` and `current`).
    pub versions: Option<PathBuf>,
    /// Directory with the bundled mkosi, image configurations and dracut
    /// module (`mkosi/`, `images/`, `dracut/`).
    pub share: Option<PathBuf>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Network {
    /// IPv4 address of the host resolver guest DNS is forwarded to.
    pub dns_host: Option<std::net::Ipv4Addr>,
}

/// Bundled program locations (plan §4).
pub const BUNDLED_CLOUD_HYPERVISOR: &str = "/usr/lib/toby/cloud-hypervisor";
pub const BUNDLED_VERSIONS: &str = "/usr/lib/toby/versions";
pub const BUNDLED_SHARE: &str = "/usr/share/toby";

impl Programs {
    pub fn cloud_hypervisor(&self) -> PathBuf {
        self.cloud_hypervisor.clone().unwrap_or_else(|| BUNDLED_CLOUD_HYPERVISOR.into())
    }

    /// The edk2 firmware for this architecture.
    pub fn firmware(&self) -> PathBuf {
        self.firmware.clone().unwrap_or_else(|| {
            let name = if cfg!(target_arch = "aarch64") { "CLOUDHV_EFI.fd" } else { "CLOUDHV.fd" };
            PathBuf::from("/usr/lib/toby/firmware").join(name)
        })
    }

    pub fn share(&self) -> PathBuf {
        self.share.clone().unwrap_or_else(|| BUNDLED_SHARE.into())
    }

    pub fn versions(&self) -> PathBuf {
        self.versions.clone().unwrap_or_else(|| BUNDLED_VERSIONS.into())
    }
}

impl GlobalConfig {
    /// Whether a machine may reach MCP server `name`: Toby's own server
    /// from any machine but a services machine, another server when a tool
    /// started in the machine lists it.
    pub fn mcp_reachable(&self, spec: &crate::machine::MachineSpec, name: &str) -> bool {
        if spec.services.is_some() {
            return false;
        }
        name == "toby"
            || spec.tools.iter().any(|t| self.tools.get(t).is_some_and(|t| t.mcp.iter().any(|m| m == name)))
    }

    /// Loads the configuration file; a missing file means defaults.
    pub fn load(path: &Path) -> io::Result<GlobalConfig> {
        match std::fs::read_to_string(path) {
            Ok(text) => toml::from_str(&text)
                .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, format!("{}: {e}", path.display()))),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(GlobalConfig::default()),
            Err(e) => Err(e),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_known_sections() {
        let cfg: GlobalConfig = toml::from_str(
            "[daemon]\nbackend = \"direct\"\n[programs]\npasst = \"/opt/passt\"\n[network]\ndns_host = \"192.0.2.1\"\n",
        )
        .unwrap();
        assert_eq!(cfg.daemon.backend, Backend::Direct);
        assert_eq!(cfg.programs.passt, Some("/opt/passt".into()));
        assert_eq!(cfg.network.dns_host, Some("192.0.2.1".parse().unwrap()));
    }

    #[test]
    fn durations() {
        use std::time::Duration;
        assert_eq!(parse_duration("15m"), Some(Duration::from_secs(900)));
        assert_eq!(parse_duration("30s"), Some(Duration::from_secs(30)));
        assert_eq!(parse_duration("2h"), Some(Duration::from_secs(7200)));
        assert_eq!(parse_duration("0"), Some(Duration::ZERO));
        for bad in ["", "m", "15", "1.5h", "-1m", "15min"] {
            assert_eq!(parse_duration(bad), None, "{bad}");
        }
        let d: Daemon = toml::from_str("idle_timeout = \"0\"").unwrap();
        assert_eq!(d.idle_timeout().unwrap(), None);
        assert_eq!(Daemon::default().idle_timeout().unwrap(), Some(Duration::from_secs(900)));
    }

    #[test]
    fn unknown_actions_are_refused() {
        assert!(toml::from_str::<GlobalConfig>("[permissions.actions]\n\"git.push\" = \"allow\"\n").is_ok());
        let e =
            toml::from_str::<GlobalConfig>("[permissions.actions]\n\"git.psuh\" = \"allow\"\n").unwrap_err();
        assert!(e.to_string().contains("git.psuh"), "{e}");
    }

    #[test]
    fn machines_reach_the_mcp_servers_of_their_tools() {
        let cfg: GlobalConfig = toml::from_str("[tools.claude]\nmcp = [\"github\"]\n").unwrap();
        let mut spec: crate::machine::MachineSpec = toml::from_str(
            "schema = 1\ngeneration = 1\nid = \"m\"\nroot = \"r\"\n[resources]\ncpus = 1\nmemory = \"1G\"\n",
        )
        .unwrap();
        assert!(cfg.mcp_reachable(&spec, "toby"));
        assert!(!cfg.mcp_reachable(&spec, "github"));
        spec.tools.push("claude".into());
        assert!(cfg.mcp_reachable(&spec, "github"));
        assert!(!cfg.mcp_reachable(&spec, "docs"));
        spec.services = Some("github".into());
        assert!(!cfg.mcp_reachable(&spec, "toby"));
    }

    #[test]
    fn warnings_can_be_suppressed() {
        let s = Settings { suppress_warnings: vec!["daemon.linger-disabled".into()], ..Default::default() };
        assert!(s.suppressed("daemon.linger-disabled"));
        assert!(!s.suppressed("other"));
        assert!(Settings { suppress_warnings: vec!["*".into()], ..Default::default() }.suppressed("other"));
    }

    #[test]
    fn model_providers() {
        let cfg: GlobalConfig = toml::from_str(
            "[models.anthropic]\nprotocol = \"anthropic\"\nurl = \"https://api.anthropic.com\"\nheaders = { \"x-api-key\" = \"{file:keys/a}\" }\n",
        )
        .unwrap();
        let p = &cfg.models["anthropic"];
        assert_eq!(p.protocol, Protocol::Anthropic);
        assert_eq!(p.headers["x-api-key"], "{file:keys/a}");
    }

    #[test]
    fn mcp_placement_follows_secrets() {
        let cfg: GlobalConfig = toml::from_str(
            "[mcp.gh]\nkind = \"stdio\"\ncommand = [\"gh-mcp\"]\nenv = { TOKEN = \"{file:keys/gh}\" }\n\
             [mcp.fs]\nkind = \"stdio\"\ncommand = [\"fs-mcp\"]\n\
             [mcp.bad]\nkind = \"stdio\"\ncommand = [\"x\"]\nplacement = \"machine\"\nenv = { T = \"{env:T}\" }\n\
             [mcp.docs]\nkind = \"http\"\nurl = \"https://example.com/mcp\"\n\
             [permissions.actions]\n\"git.push\" = \"always-ask\"\n",
        )
        .unwrap();
        assert_eq!(cfg.mcp["gh"].placement(), Placement::Isolated);
        assert_eq!(cfg.mcp["fs"].placement(), Placement::Machine);
        assert!(cfg.mcp["gh"].check("gh").is_ok() && cfg.mcp["docs"].check("docs").is_ok());
        assert!(cfg.mcp["bad"].check("bad").is_err());
        assert_eq!(cfg.permissions.actions["git.push"], ActionPolicy::AlwaysAsk);
    }

    #[test]
    fn unknown_fields_are_errors() {
        assert!(toml::from_str::<GlobalConfig>("[daemon]\nbakend = \"direct\"\n").is_err());
    }

    #[test]
    fn dns_host_must_be_ipv4() {
        assert!(toml::from_str::<GlobalConfig>("[network]\ndns_host = \"2001:db8::1\"\n").is_err());
    }
}
