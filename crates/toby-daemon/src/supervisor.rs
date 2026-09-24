//! Starting and stopping machines through the configured back end (plan
//! §12): units of the systemd user instance, or detached process trees.

use std::io;
use std::path::PathBuf;

use toby_config::global::Backend;
use toby_config::paths::Paths;
use toby_svc::systemd::SystemdUser;

pub enum Supervisor {
    Systemd(SystemdUser),
    Direct {
        /// The `toby` binary machines are started with.
        exe: PathBuf,
        logs: PathBuf,
    },
}

/// The binary new processes run: the installed current version, so an
/// upgrade applies to machines started after it.
pub fn current_exe(versions: &std::path::Path) -> io::Result<PathBuf> {
    let current = versions.join("current").join("toby");
    if current.is_file() { Ok(current) } else { std::env::current_exe() }
}

pub fn vm_unit(id: &str) -> String {
    format!("toby-vm@{id}.service")
}

impl Supervisor {
    pub async fn new(backend: Backend, paths: &Paths, exe: PathBuf) -> io::Result<Supervisor> {
        Ok(match backend {
            Backend::SystemdUser => Supervisor::Systemd(SystemdUser::connect().await?),
            Backend::Direct => Supervisor::Direct { exe, logs: paths.state.join("logs") },
        })
    }

    pub fn backend(&self) -> Backend {
        match self {
            Supervisor::Systemd(_) => Backend::SystemdUser,
            Supervisor::Direct { .. } => Backend::Direct,
        }
    }

    /// Starts the machine's processes; readiness is observed separately.
    pub async fn start(&self, id: &str) -> io::Result<()> {
        match self {
            Supervisor::Systemd(s) => {
                // A failed earlier run would keep the unit from starting.
                let _ = s.reset_failed(&vm_unit(id)).await;
                s.start(&vm_unit(id)).await
            }
            Supervisor::Direct { exe, logs } => {
                let mut cmd = std::process::Command::new(exe);
                cmd.args(["internal", "machine", "--supervise", "--machine", id, "--log-dir"]).arg(logs);
                toby_svc::direct::spawn_detached(cmd, &logs.join(format!("toby-machine@{id}.log"))).map(drop)
            }
        }
    }

    /// Stops the machine's processes without waiting for the guest; used
    /// when a graceful power-off did not work.
    pub async fn kill(&self, id: &str, runtime: &toby_config::paths::MachineRuntime) -> io::Result<()> {
        match self {
            Supervisor::Systemd(s) => s.stop(&vm_unit(id)).await,
            Supervisor::Direct { .. } => {
                let pid = std::fs::read_to_string(runtime.dir.join("supervisor.pid"))?;
                let pid: i32 = pid.trim().parse().map_err(|_| io::Error::other("invalid supervisor.pid"))?;
                nix::sys::signal::kill(nix::unistd::Pid::from_raw(pid), nix::sys::signal::Signal::SIGTERM)
                    .map_err(io::Error::from)
            }
        }
    }

    /// Whether the back end still runs the machine's processes.
    pub async fn active(&self, id: &str, runtime: &toby_config::paths::MachineRuntime) -> bool {
        match self {
            Supervisor::Systemd(s) => {
                matches!(s.state(&vm_unit(id)).await.as_deref(), Ok("active" | "activating" | "deactivating"))
            }
            Supervisor::Direct { .. } => std::fs::read_to_string(runtime.dir.join("supervisor.pid"))
                .ok()
                .and_then(|p| p.trim().parse::<i32>().ok())
                .is_some_and(|pid| {
                    std::fs::read(format!("/proc/{pid}/cmdline"))
                        .is_ok_and(|c| c.split(|b| *b == 0).any(|a| a == b"--supervise"))
                }),
        }
    }
}
