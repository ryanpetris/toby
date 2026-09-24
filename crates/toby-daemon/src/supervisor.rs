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

    /// Makes sure the models proxy answers: its socket unit, or a detached
    /// process in the direct back end.
    pub async fn ensure_proxy(&self, paths: &Paths) -> io::Result<()> {
        match self {
            Supervisor::Systemd(s) => {
                if s.state("toby-proxy.socket").await? == "not-found" {
                    return Err(io::Error::new(
                        io::ErrorKind::NotFound,
                        "toby-proxy.socket is not installed",
                    ));
                }
                s.start("toby-proxy.socket").await
            }
            Supervisor::Direct { exe, logs } => {
                if tokio::net::UnixStream::connect(paths.proxy_sock()).await.is_ok() {
                    return Ok(());
                }
                let mut cmd = std::process::Command::new(exe);
                cmd.args(["internal", "proxy"]);
                toby_svc::direct::spawn_detached(cmd, &logs.join("toby-proxy.log")).map(drop)
            }
        }
    }

    /// Stops the machine's processes without waiting for the guest; used
    /// when a graceful power-off did not work.
    pub async fn kill(&self, id: &str, runtime: &toby_config::paths::MachineRuntime) -> io::Result<()> {
        match self {
            Supervisor::Systemd(s) => s.stop(&vm_unit(id)).await,
            Supervisor::Direct { .. } => {
                let pid = supervisor_pid(id, runtime).ok_or_else(|| {
                    io::Error::new(io::ErrorKind::NotFound, "the machine's supervisor is gone")
                })?;
                nix::sys::signal::kill(nix::unistd::Pid::from_raw(pid), nix::sys::signal::Signal::SIGTERM)
                    .map_err(io::Error::from)
            }
        }
    }

    /// Whether the back end still runs the machine's processes.
    pub async fn active(&self, id: &str, runtime: &toby_config::paths::MachineRuntime) -> bool {
        match self {
            Supervisor::Systemd(s) => s.busy(&vm_unit(id)).await.unwrap_or(false),
            Supervisor::Direct { .. } => supervisor_pid(id, runtime).is_some(),
        }
    }
}

/// The pid of the machine's supervisor, if the pid file names a live
/// supervisor of this machine (pids are reused).
fn supervisor_pid(id: &str, runtime: &toby_config::paths::MachineRuntime) -> Option<i32> {
    let pid: i32 = std::fs::read_to_string(runtime.dir.join("supervisor.pid")).ok()?.trim().parse().ok()?;
    let cmdline = std::fs::read(format!("/proc/{pid}/cmdline")).ok()?;
    let args: Vec<&[u8]> = cmdline.split(|b| *b == 0).collect();
    let supervise = args.contains(&&b"--supervise"[..]);
    let this = args.windows(2).any(|w| w[0] == b"--machine" && w[1] == id.as_bytes());
    (supervise && this).then_some(pid)
}
