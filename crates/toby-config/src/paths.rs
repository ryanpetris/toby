//! Where Toby keeps configuration, state, data and runtime files (plan §6.1).
//!
//! Paths are resolved from `$HOME` and the global configuration, never from
//! `XDG_*` variables, so shells and services agree.

use std::io;
use std::path::{Path, PathBuf};

use crate::global::{Backend, GlobalConfig};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Paths {
    pub home: PathBuf,
    /// `~/.config/toby`
    pub config: PathBuf,
    /// `~/.local/state/toby`
    pub state: PathBuf,
    /// `~/.local/share/toby`
    pub data: PathBuf,
    /// Runtime sockets and observed state.
    pub runtime: PathBuf,
}

/// The user's home directory from `$HOME`, or the password database.
pub fn home_dir() -> io::Result<PathBuf> {
    if let Some(h) = std::env::var_os("HOME").filter(|h| !h.is_empty()) {
        return Ok(PathBuf::from(h));
    }
    nix::unistd::User::from_uid(nix::unistd::getuid())
        .ok()
        .flatten()
        .map(|u| u.dir)
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "cannot determine the home directory"))
}

/// Expands a leading `~/` against `home`.
pub fn expand(home: &Path, p: &str) -> PathBuf {
    match p.strip_prefix("~/") {
        Some(rest) => home.join(rest),
        None if p == "~" => home.to_path_buf(),
        None => PathBuf::from(p),
    }
}

impl Paths {
    /// Resolves every path for the current user.
    pub fn resolve(config: &GlobalConfig) -> io::Result<Paths> {
        let home = home_dir()?;
        let uid = nix::unistd::getuid().as_raw();
        let runtime = match config.daemon.backend {
            Backend::SystemdUser => PathBuf::from(format!("/run/user/{uid}/toby")),
            Backend::Direct => std::env::var_os("TOBY_RUNTIME_DIR")
                .map(PathBuf::from)
                .unwrap_or_else(|| PathBuf::from(format!("/tmp/toby-{uid}"))),
        };
        Ok(Paths::with(home, config, runtime))
    }

    /// Builds paths below an explicit home and runtime directory.
    pub fn with(home: PathBuf, config: &GlobalConfig, runtime: PathBuf) -> Paths {
        let state = config
            .paths
            .state_root
            .as_deref()
            .map(|p| expand(&home, p))
            .unwrap_or_else(|| home.join(".local/state/toby"));
        let data = config
            .paths
            .storage_root
            .as_deref()
            .map(|p| expand(&home, p))
            .unwrap_or_else(|| home.join(".local/share/toby"));
        Paths {
            config: home.join(".config/toby"),
            home,
            state,
            data,
            runtime,
        }
    }

    pub fn global_config(&self) -> PathBuf {
        self.config.join("config.toml")
    }

    pub fn machine_state_dir(&self, id: &str) -> PathBuf {
        self.state.join("machines").join(id)
    }

    /// The desired state file written by `tobyd`.
    pub fn machine_desired(&self, id: &str) -> PathBuf {
        self.machine_state_dir(id).join("machine.toml")
    }

    pub fn machine_runtime(&self, id: &str) -> MachineRuntime {
        MachineRuntime {
            dir: self.runtime.join("machines").join(id),
        }
    }

    pub fn machines_runtime(&self) -> PathBuf {
        self.runtime.join("machines")
    }

    pub fn image_dir(&self, id: &str) -> PathBuf {
        self.data.join("images").join(id)
    }

    pub fn root_disk(&self, name: &str) -> PathBuf {
        self.data.join("roots").join(format!("{name}.qcow2"))
    }

    pub fn home_disk(&self, name: &str) -> PathBuf {
        self.data.join("homes").join(format!("{name}.qcow2"))
    }
}

/// Runtime files of one machine (plan §6.1).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MachineRuntime {
    pub dir: PathBuf,
}

impl MachineRuntime {
    pub fn control_sock(&self) -> PathBuf {
        self.dir.join("control.sock")
    }
    pub fn session_sock(&self) -> PathBuf {
        self.dir.join("session.sock")
    }
    pub fn status(&self) -> PathBuf {
        self.dir.join("status.toml")
    }
    pub fn ch_api(&self) -> PathBuf {
        self.dir.join("ch-api.sock")
    }
    /// Hybrid vsock socket for host-initiated connections.
    pub fn vsock(&self) -> PathBuf {
        self.dir.join("vsock.sock")
    }
    /// Socket Cloud Hypervisor connects to for guest connections to `port`.
    pub fn vsock_listen(&self, port: u32) -> PathBuf {
        self.dir.join(format!("vsock.sock_{port}"))
    }
    pub fn fs_sock(&self) -> PathBuf {
        self.dir.join("fs.sock")
    }
    pub fn net_sock(&self) -> PathBuf {
        self.dir.join("net.sock")
    }
    pub fn console_log(&self) -> PathBuf {
        self.dir.join("console.log")
    }
    /// Root disk layer used when the machine is ephemeral.
    pub fn ephemeral_disk(&self) -> PathBuf {
        self.dir.join("ephemeral.qcow2")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_layout() {
        let p = Paths::with("/h".into(), &GlobalConfig::default(), "/run/user/1/toby".into());
        assert_eq!(p.state, PathBuf::from("/h/.local/state/toby"));
        assert_eq!(p.data, PathBuf::from("/h/.local/share/toby"));
        assert_eq!(
            p.machine_desired("m"),
            PathBuf::from("/h/.local/state/toby/machines/m/machine.toml")
        );
        assert_eq!(
            p.root_disk("work"),
            PathBuf::from("/h/.local/share/toby/roots/work.qcow2")
        );
        assert_eq!(
            p.machine_runtime("m").vsock_listen(1024),
            PathBuf::from("/run/user/1/toby/machines/m/vsock.sock_1024")
        );
    }

    #[test]
    fn overridden_roots_expand_tilde() {
        let cfg: GlobalConfig = toml::from_str("[paths]\nstorage_root = \"~/vm\"\n").unwrap();
        let p = Paths::with("/h".into(), &cfg, "/r".into());
        assert_eq!(p.data, PathBuf::from("/h/vm"));
    }
}
