//! Per-machine state files: the desired state `machine.toml`, written only by
//! `tobyd`, and the observed state `status.toml`, written only by
//! `toby-machine` (plan §8.4, §8.5).

use std::io;
use std::path::Path;

use serde::{Deserialize, Serialize};

/// Version of the `machine.toml` format.
pub const SCHEMA: u32 = 1;

// Fields these files do not know are ignored: tobyd and toby-machine can be
// different versions while an upgrade is under way (plan §3.2), and a
// newer writer's additions must not stop an older reader.

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct MachineSpec {
    pub schema: u32,
    pub generation: u64,
    pub id: String,
    /// Home disk; builder machines have none.
    #[serde(default)]
    pub home: Option<String>,
    pub root: RootSpec,
    /// Adds a throwaway layer over a named root.
    #[serde(default)]
    pub ephemeral: bool,
    pub resources: Resources,
    #[serde(default)]
    pub boot: Boot,
    /// Extra disks (builder cache and output disks).
    #[serde(default)]
    pub disk: Vec<Disk>,
    #[serde(default)]
    pub attach: Vec<Attach>,
    #[serde(default)]
    pub forward: Vec<Forward>,
    #[serde(default)]
    pub capabilities: Capabilities,
    /// Seconds without sessions before the machine stops, instead of
    /// `daemon.idle_timeout` (services machines).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub idle_timeout: Option<u64>,
    /// The isolated MCP server a services machine runs; such a machine
    /// gets no capabilities.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub services: Option<String>,
    /// Tools started in the machine: their `mcp` lists name the MCP servers
    /// it may reach.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<String>,
    /// Configured MCP servers launches in the machine enabled, while their
    /// sessions run.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub mcp_grants: Vec<McpGrant>,
}

/// A configured MCP server the machine may reach for these sessions.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct McpGrant {
    pub name: String,
    pub sessions: Vec<String>,
    /// Sessions of the server in its own machine that connections the
    /// grant allowed run; they end with the grant.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub connections: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Resources {
    pub cpus: u32,
    /// Size with a `K`, `M`, `G` or `T` suffix, e.g. `"8G"`.
    pub memory: String,
}

/// The machine's root disk.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum RootSpec {
    /// A persistent root, `roots/<name>.qcow2`.
    Named(String),
    /// A throwaway layer over an image's disk (builder machines).
    Image { image: String },
    /// A throwaway layer over a stock cloud image booted with firmware (the
    /// bootstrap builder).
    CloudImage { cloud_image: std::path::PathBuf },
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Boot {
    /// Image whose kernel and initramfs boot this machine; unset for a cloud
    /// image, which boots its own bootloader.
    pub image: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Disk {
    pub path: std::path::PathBuf,
    /// The guest sees `/dev/disk/by-id/virtio-<serial>`.
    pub serial: String,
    #[serde(default)]
    pub read_only: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Attach {
    pub id: String,
    pub host: String,
    pub at: String,
    #[serde(default)]
    pub read_only: bool,
    /// Kept until removed or the machine stops, even without sessions.
    #[serde(default)]
    pub pinned: bool,
    /// Recreated every time the machine starts.
    #[serde(default)]
    pub persist: bool,
    /// Sessions that use the attachment; it is removed when the last one
    /// ends (unless pinned).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sessions: Vec<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Direction {
    HostToGuest,
    GuestToHost,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Forward {
    pub id: String,
    pub direction: Direction,
    /// Host TCP address, e.g. `127.0.0.1:3000`.
    pub host: String,
    /// Guest TCP address, e.g. `127.0.0.1:3000`.
    pub guest: String,
    /// Kept without sessions until removed or the machine stops.
    #[serde(default)]
    pub pinned: bool,
    /// Recreated every time the machine starts.
    #[serde(default)]
    pub persist: bool,
    /// Sessions that use the forward; it is removed when the last one ends
    /// (unless pinned).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sessions: Vec<String>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
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
    /// Whether the root is a throwaway layer created at start and deleted at stop.
    pub fn has_layer(&self) -> bool {
        self.ephemeral || !matches!(self.root, RootSpec::Named(_))
    }

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
    /// Guest boot for which the boot helpers last completed.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub helpers_boot_id: Option<String>,
    /// Attachments that are desired or still mounted.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attach: Vec<AttachStatus>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub forward: Vec<ForwardStatus>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ForwardState {
    Listening,
    Failed,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ForwardStatus {
    pub id: String,
    pub state: ForwardState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum AttachState {
    /// Mounted in the guest.
    Ready,
    /// Desired but not mounted; see the error.
    Failed,
}

/// An attachment as the machine has it: mounted ones record where, so they
/// can be detached even after the desired state no longer lists them.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AttachStatus {
    pub id: String,
    pub host: String,
    pub at: String,
    #[serde(default)]
    pub read_only: bool,
    pub state: AttachState,
    /// Why the attachment could not be mounted, or could not be detached
    /// although the desired state no longer lists it.
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
    fn root_forms() {
        for (text, root) in [
            ("root = \"work\"", RootSpec::Named("work".into())),
            ("root = { image = \"01J\" }", RootSpec::Image { image: "01J".into() }),
            (
                "root = { cloud_image = \"/c.qcow2\" }",
                RootSpec::CloudImage { cloud_image: "/c.qcow2".into() },
            ),
        ] {
            let doc = format!(
                "schema = 1\ngeneration = 1\nid = \"m\"\n{text}\n[resources]\ncpus = 1\nmemory = \"1G\"\n"
            );
            let spec: MachineSpec = toml::from_str(&doc).unwrap();
            assert_eq!(spec.root, root);
            assert_eq!(spec.home, None);
        }
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
    fn fields_from_newer_writers_are_ignored() {
        let text = EXAMPLE.replace("generation = 42", "generation = 42\nlater = [\"x\"]");
        assert_eq!(toml::from_str::<MachineSpec>(&text).unwrap(), toml::from_str(EXAMPLE).unwrap());
    }

    #[test]
    fn other_schemas_are_refused() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("machine.toml");
        std::fs::write(&path, EXAMPLE.replace("schema = 1", "schema = 2")).unwrap();
        assert!(MachineSpec::load(&path).is_err());
    }
}
