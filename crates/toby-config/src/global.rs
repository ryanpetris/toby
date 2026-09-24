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
    fn unknown_fields_are_errors() {
        assert!(toml::from_str::<GlobalConfig>("[daemon]\nbakend = \"direct\"\n").is_err());
    }

    #[test]
    fn dns_host_must_be_ipv4() {
        assert!(toml::from_str::<GlobalConfig>("[network]\ndns_host = \"2001:db8::1\"\n").is_err());
    }
}
