//! Records of images, roots and homes in Toby's state directory (plan §6.1,
//! §14.4). Each record is one TOML file.

use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};

/// Where an image came from.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase", deny_unknown_fields)]
pub enum ImageSource {
    /// The bundled default image configuration.
    Default,
    Mkosi {
        path: PathBuf,
    },
    Dockerfile {
        path: PathBuf,
        context: PathBuf,
    },
    Registry {
        reference: String,
    },
    Archive {
        path: PathBuf,
    },
}

impl ImageSource {
    pub fn describe(&self) -> String {
        match self {
            ImageSource::Default => "default".into(),
            ImageSource::Mkosi { path } => format!("mkosi {}", path.display()),
            ImageSource::Dockerfile { path, .. } => format!("dockerfile {}", path.display()),
            ImageSource::Registry { reference } => format!("registry {reference}"),
            ImageSource::Archive { path } => format!("archive {}", path.display()),
        }
    }
}

/// Runtime configuration carried by container images.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ImageConfig {
    #[serde(default)]
    pub env: Vec<String>,
    #[serde(default)]
    pub user: String,
    #[serde(default)]
    pub workdir: String,
    #[serde(default)]
    pub labels: BTreeMap<String, String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ImageRecord {
    pub id: String,
    pub arch: String,
    /// Unix time in seconds.
    pub created: u64,
    pub source: ImageSource,
    /// Hash of the source inputs; an image is current when it matches.
    pub source_hash: String,
    pub kernel_version: String,
    pub adaptation_version: u32,
    #[serde(default)]
    pub config: ImageConfig,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RootRecord {
    pub name: String,
    pub image: String,
    pub created: u64,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HomeRecord {
    pub name: String,
    pub username: String,
    pub uid: u32,
    #[serde(default = "yes")]
    pub sudo: bool,
    #[serde(default)]
    pub shell: Option<String>,
    #[serde(default)]
    pub default_root: Option<String>,
    /// Whether the disk has been formatted.
    #[serde(default)]
    pub formatted: bool,
    pub created: u64,
}

fn yes() -> bool {
    true
}

/// Names of roots and homes: they become file names.
pub fn valid_name(n: &str) -> bool {
    let b = n.as_bytes();
    !b.is_empty()
        && b.len() <= 64
        && b[0].is_ascii_alphanumeric()
        && b.iter().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, b'-' | b'_'))
}

pub fn check_name(kind: &str, n: &str) -> io::Result<()> {
    if valid_name(n) {
        Ok(())
    } else {
        Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("invalid {kind} name {n:?}: use lowercase letters, digits, '-' and '_'"),
        ))
    }
}

pub fn now() -> u64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

pub fn load<T: DeserializeOwned>(path: &Path) -> io::Result<T> {
    let text = std::fs::read_to_string(path)?;
    toml::from_str(&text)
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, format!("{}: {e}", path.display())))
}

pub fn store<T: Serialize>(path: &Path, value: &T) -> io::Result<()> {
    let text = toml::to_string_pretty(value).map_err(io::Error::other)?;
    toby_config::machine::write_atomic(path, text.as_bytes())
}

/// Loads every record in `dir` with the `.toml` extension.
pub fn load_all<T: DeserializeOwned>(dir: &Path) -> io::Result<Vec<T>> {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e),
    };
    let mut out = Vec::new();
    for e in entries.flatten() {
        let p = e.path();
        if p.extension().is_some_and(|x| x == "toml") {
            out.push(load(&p)?);
        }
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn records_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let img = ImageRecord {
            id: "01J".into(),
            arch: "x86_64".into(),
            created: 1,
            source: ImageSource::Dockerfile { path: "/p/Dockerfile".into(), context: "/p".into() },
            source_hash: "abc".into(),
            kernel_version: "6.12".into(),
            adaptation_version: 1,
            config: ImageConfig { env: vec!["A=1".into()], ..Default::default() },
        };
        store(&dir.path().join("01J.toml"), &img).unwrap();
        assert_eq!(load::<ImageRecord>(&dir.path().join("01J.toml")).unwrap(), img);
        assert_eq!(load_all::<ImageRecord>(dir.path()).unwrap(), vec![img]);

        let home: HomeRecord =
            toml::from_str("name = \"w\"\nusername = \"u\"\nuid = 1000\ncreated = 2\n").unwrap();
        assert!(home.sudo);
        assert!(!home.formatted);
    }

    #[test]
    fn names() {
        assert!(valid_name("work"));
        assert!(valid_name("a-1_b"));
        assert!(!valid_name("Work"));
        assert!(!valid_name("-x"));
        assert!(!valid_name("a/b"));
        assert!(!valid_name(".."));
    }
}
