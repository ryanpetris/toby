//! Per-machine state files: the desired state `machine.toml`, written only by
//! `tobyd`, and the observed state `status.toml`, written only by
//! `toby-machine` (plan §8.4, §8.5).

use std::io;
use std::path::Path;

use serde::{Deserialize, Serialize};

/// Version of the `machine.toml` format.
pub const SCHEMA: u32 = 1;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MachineSpec {
    pub schema: u32,
    pub generation: u64,
    pub id: String,
    pub home: String,
    pub root: String,
    #[serde(default)]
    pub ephemeral: bool,
    pub resources: Resources,
    pub boot: Boot,
    #[serde(default)]
    pub attach: Vec<Attach>,
    #[serde(default)]
    pub forward: Vec<Forward>,
    #[serde(default)]
    pub capabilities: Capabilities,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Resources {
    pub cpus: u32,
    /// Size with a `K`, `M`, `G` or `T` suffix, e.g. `"8G"`.
    pub memory: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Boot {
    /// Image whose kernel and initramfs boot this machine.
    pub image: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Attach {
    pub id: String,
    pub host: String,
    pub at: String,
    #[serde(default)]
    pub read_only: bool,
    #[serde(default)]
    pub pinned: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Direction {
    HostToGuest,
    GuestToHost,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Forward {
    pub id: String,
    pub direction: Direction,
    pub host: String,
    pub guest: String,
    #[serde(default)]
    pub pinned: bool,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Capabilities {
    pub sandbox_socket: Option<String>,
    pub models_listen: Option<String>,
}

/// Parses a size such as `512M` or `8G` into bytes.
pub fn parse_size(s: &str) -> Option<u64> {
    let s = s.trim();
    let (num, mult) = match s.char_indices().last()? {
        (i, 'K' | 'k') => (&s[..i], 1u64 << 10),
        (i, 'M' | 'm') => (&s[..i], 1 << 20),
        (i, 'G' | 'g') => (&s[..i], 1 << 30),
        (i, 'T' | 't') => (&s[..i], 1 << 40),
        _ => (s, 1),
    };
    num.trim().parse::<u64>().ok()?.checked_mul(mult)
}

impl MachineSpec {
    pub fn memory_bytes(&self) -> io::Result<u64> {
        parse_size(&self.resources.memory).ok_or_else(|| {
            io::Error::new(
                io::ErrorKind::InvalidData,
                format!("invalid memory size {:?}", self.resources.memory),
            )
        })
    }

    pub fn load(path: &Path) -> io::Result<MachineSpec> {
        let text = std::fs::read_to_string(path)?;
        let spec: MachineSpec = toml::from_str(&text)
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, format!("{}: {e}", path.display())))?;
        if spec.schema != SCHEMA {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("{}: unsupported schema {}", path.display(), spec.schema),
            ));
        }
        Ok(spec)
    }

    /// Writes the file atomically.
    pub fn store(&self, path: &Path) -> io::Result<()> {
        let text = toml::to_string_pretty(self).map_err(io::Error::other)?;
        write_atomic(path, text.as_bytes())
    }
}

/// Observed machine state.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum State {
    #[default]
    Starting,
    Ready,
    Stopping,
    Failed,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct MachineStatus {
    pub observed_generation: u64,
    pub state: State,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub relay_version: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proto: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub boot_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

impl MachineStatus {
    pub fn load(path: &Path) -> io::Result<MachineStatus> {
        let text = std::fs::read_to_string(path)?;
        toml::from_str(&text).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e.to_string()))
    }

    pub fn store(&self, path: &Path) -> io::Result<()> {
        let text = toml::to_string_pretty(self).map_err(io::Error::other)?;
        write_atomic(path, text.as_bytes())
    }
}

/// Writes `data` to a temporary file next to `path` and renames it into place.
pub fn write_atomic(path: &Path, data: &[u8]) -> io::Result<()> {
    use std::io::Write;

    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let tmp = path.with_extension(format!("tmp.{}", std::process::id()));
    {
        let mut f = std::fs::File::create(&tmp)?;
        f.write_all(data)?;
        f.sync_all()?;
    }
    std::fs::rename(&tmp, path)
}

#[cfg(test)]
mod tests {
    use super::*;

    const EXAMPLE: &str = r#"
schema = 1
generation = 42
id = "01J00000000000000000000000"
home = "work"
root = "work"

[resources]
cpus = 4
memory = "8G"

[boot]
image = "01J00000000000000000000001"

[[attach]]
id = "a1"
host = "/home/user/src/toby"
at = "/toby/workspace/toby"

[[forward]]
id = "f1"
direction = "host-to-guest"
host = "127.0.0.1:3000"
guest = "127.0.0.1:3000"
pinned = true

[capabilities]
sandbox_socket = "/run/toby/sandbox.sock"
models_listen = "127.0.0.1:41100"
"#;

    #[test]
    fn example_parses_and_roundtrips() {
        let spec: MachineSpec = toml::from_str(EXAMPLE).unwrap();
        assert_eq!(spec.generation, 42);
        assert_eq!(spec.memory_bytes().unwrap(), 8 << 30);
        assert_eq!(spec.forward[0].direction, Direction::HostToGuest);

        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("m/machine.toml");
        spec.store(&path).unwrap();
        assert_eq!(MachineSpec::load(&path).unwrap(), spec);
    }

    #[test]
    fn sizes() {
        assert_eq!(parse_size("512M"), Some(512 << 20));
        assert_eq!(parse_size("1g"), Some(1 << 30));
        assert_eq!(parse_size("4096"), Some(4096));
        assert_eq!(parse_size("x"), None);
        assert_eq!(parse_size("99999999999T"), None);
    }

    #[test]
    fn other_schemas_are_refused() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("machine.toml");
        std::fs::write(&path, EXAMPLE.replace("schema = 1", "schema = 2")).unwrap();
        assert!(MachineSpec::load(&path).is_err());
    }
}
