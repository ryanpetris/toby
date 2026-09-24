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
    fn unknown_fields_are_errors() {
        assert!(toml::from_str::<GlobalConfig>("[daemon]\nbakend = \"direct\"\n").is_err());
    }

    #[test]
    fn dns_host_must_be_ipv4() {
        assert!(toml::from_str::<GlobalConfig>("[network]\ndns_host = \"2001:db8::1\"\n").is_err());
    }
}
