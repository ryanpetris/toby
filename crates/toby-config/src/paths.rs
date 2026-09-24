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

/// Creates `dir` with mode 0700 if missing, and refuses to use it unless it
/// is a directory (not a symlink) owned by the current user that no one else
/// can access. The direct back end's runtime directory lives in `/tmp`,
/// where another user could create it first.
pub fn ensure_private_dir(dir: &Path) -> io::Result<()> {
    use std::os::unix::fs::{DirBuilderExt, MetadataExt};

    if let Some(parent) = dir.parent() {
        std::fs::create_dir_all(parent)?;
    }
    match std::fs::DirBuilder::new().mode(0o700).create(dir) {
        Ok(()) => {}
        Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {}
        Err(e) => return Err(e),
    }
    let meta = std::fs::symlink_metadata(dir)?;
    let uid = nix::unistd::getuid().as_raw();
    if !meta.file_type().is_dir() || meta.uid() != uid || meta.mode() & 0o077 != 0 {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            format!("{} must be a directory owned by you with mode 0700", dir.display()),
        ));
    }
    Ok(())
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
        Paths { config: home.join(".config/toby"), home, state, data, runtime }
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
        MachineRuntime { dir: self.runtime.join("machines").join(id) }
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

    /// A machine's throwaway root layer, recreated at every start.
    pub fn layer_disk(&self, machine: &str) -> PathBuf {
        self.data.join("layers").join(format!("{machine}.qcow2"))
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
    pub fn fs_control_sock(&self) -> PathBuf {
        self.dir.join("fs-control.sock")
    }
    pub fn net_sock(&self) -> PathBuf {
        self.dir.join("net.sock")
    }
    pub fn console_log(&self) -> PathBuf {
        self.dir.join("console.log")
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
        assert_eq!(p.machine_desired("m"), PathBuf::from("/h/.local/state/toby/machines/m/machine.toml"));
        assert_eq!(p.root_disk("work"), PathBuf::from("/h/.local/share/toby/roots/work.qcow2"));
        assert_eq!(
            p.machine_runtime("m").vsock_listen(1024),
            PathBuf::from("/run/user/1/toby/machines/m/vsock.sock_1024")
        );
    }

    #[test]
    fn private_dirs_are_checked() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let rt = dir.path().join("rt");
        ensure_private_dir(&rt).unwrap();
        ensure_private_dir(&rt).unwrap();
        std::fs::set_permissions(&rt, std::fs::Permissions::from_mode(0o755)).unwrap();
        assert!(ensure_private_dir(&rt).is_err());

        let link = dir.path().join("link");
        std::os::unix::fs::symlink(&rt, &link).unwrap();
        assert!(ensure_private_dir(&link).is_err());
    }

    #[test]
    fn overridden_roots_expand_tilde() {
        let cfg: GlobalConfig = toml::from_str("[paths]\nstorage_root = \"~/vm\"\n").unwrap();
        let p = Paths::with("/h".into(), &cfg, "/r".into());
        assert_eq!(p.data, PathBuf::from("/h/vm"));
    }
}
