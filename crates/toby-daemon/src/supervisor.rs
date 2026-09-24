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
                        "toby-proxy.socket is not installed; install the Toby package",
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

    /// Restarts the models proxy (process `p`) on the current version.
    pub async fn restart_proxy(&self, paths: &Paths, p: &crate::versions::HostProcess) -> io::Result<()> {
        match self {
            Supervisor::Systemd(s) => s.try_restart("toby-proxy.service").await,
            Supervisor::Direct { .. } => {
                terminate(p).await?;
                self.ensure_proxy(paths).await
            }
        }
    }

    /// Restarts a machine's host process (process `p`) on the current
    /// version; the VM keeps running.
    pub async fn restart_machine_process(
        &self,
        id: &str,
        p: &crate::versions::HostProcess,
    ) -> io::Result<()> {
        match self {
            Supervisor::Systemd(s) => s.try_restart(&format!("toby-machine@{id}.service")).await,
            // The machine's supervisor starts it again.
            Supervisor::Direct { .. } => terminate(p).await,
        }
    }

    /// Stops the machine's processes without waiting for the guest; used
    /// when a graceful power-off did not work.
    pub async fn kill(&self, id: &str, runtime: &toby_config::paths::MachineRuntime) -> io::Result<()> {
        match self {
            Supervisor::Systemd(s) => s.stop(&vm_unit(id)).await,
            Supervisor::Direct { .. } => {
                let pid = supervisor_pid(id, runtime).ok_or_else(|| {
                    io::Error::new(io::ErrorKind::NotFound, format!("machine {id} is not running"))
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

/// Sends SIGTERM to process `p` and waits up to five seconds for it to
/// exit. The signal goes through a pidfd opened while `p` still was that
/// process, so a later process that got its pid is never signalled.
pub(crate) async fn terminate(p: &crate::versions::HostProcess) -> io::Result<()> {
    use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
    let gone = |e: io::Error| if e.raw_os_error() == Some(libc::ESRCH) { Ok(()) } else { Err(e) };
    // SAFETY: pidfd_open takes a pid and flags and returns a new fd.
    let fd = unsafe { libc::syscall(libc::SYS_pidfd_open, p.pid, 0) };
    if fd < 0 {
        return gone(io::Error::last_os_error());
    }
    // SAFETY: the fd was just returned and is owned here.
    let fd = unsafe { OwnedFd::from_raw_fd(fd as i32) };
    if crate::versions::start_time(p.pid) != Some(p.start) {
        return Ok(());
    }
    // SAFETY: a valid pidfd, a signal number, no siginfo, no flags.
    let sent = unsafe {
        libc::syscall(libc::SYS_pidfd_send_signal, fd.as_raw_fd(), libc::SIGTERM, std::ptr::null::<u8>(), 0)
    };
    if sent < 0 {
        return gone(io::Error::last_os_error());
    }
    // A pidfd becomes readable when its process exits.
    let fd = tokio::io::unix::AsyncFd::new(fd)?;
    match tokio::time::timeout(std::time::Duration::from_secs(5), fd.readable()).await {
        Ok(_) => Ok(()),
        Err(_) => Err(io::Error::new(io::ErrorKind::TimedOut, format!("process {} did not exit", p.pid))),
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
