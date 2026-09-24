//! tobyd, the per-user control plane (plan §3, §8, §18).

pub mod approvals;
pub mod builder;
pub mod builds;
pub mod control;
pub mod download;
pub mod machines;
pub mod mcp;
pub mod server;
pub mod services;
pub mod supervisor;
pub mod tools;
pub mod versions;

use std::io;
use std::sync::Arc;
use std::time::Duration;

use toby_config::global::{Backend, GlobalConfig};
use toby_config::paths::Paths;

/// How often the direct back end's runtime directory is touched, so
/// age-based cleanup of `/tmp` leaves it alone (plan §6.1).
const TOUCH_INTERVAL: Duration = Duration::from_secs(3600);
/// How often unused installed versions are removed.
const VERSION_GC_INTERVAL: Duration = Duration::from_secs(3600);
/// How long requests in flight may finish after a stop is requested.
const SHUTDOWN_GRACE: Duration = Duration::from_secs(5);
/// Build logs older than this are removed when the daemon starts.
const BUILD_LOG_AGE: Duration = Duration::from_secs(30 * 86400);

/// Accepts only connections from processes of this user (plan §17).
struct OwnUser(tokio::net::UnixListener);

impl axum::serve::Listener for OwnUser {
    type Io = tokio::net::UnixStream;
    type Addr = tokio::net::unix::SocketAddr;

    async fn accept(&mut self) -> (Self::Io, Self::Addr) {
        let uid = nix::unistd::getuid().as_raw();
        loop {
            match self.0.accept().await {
                Ok((s, addr)) if s.peer_cred().is_ok_and(|c| c.uid() == uid) => return (s, addr),
                Ok(_) => {}
                Err(_) => tokio::time::sleep(Duration::from_millis(100)).await,
            }
        }
    }

    fn local_addr(&self) -> io::Result<Self::Addr> {
        self.0.local_addr()
    }
}

fn prune_build_logs(dir: &std::path::Path) {
    let Ok(entries) = std::fs::read_dir(dir) else { return };
    for e in entries.flatten() {
        let old = e
            .metadata()
            .and_then(|m| m.modified())
            .is_ok_and(|t| t.elapsed().is_ok_and(|a| a > BUILD_LOG_AGE));
        if old {
            let _ = std::fs::remove_file(e.path());
        }
    }
}

/// Serves the API on `listener` until SIGTERM or SIGINT.
pub async fn run(
    config: GlobalConfig,
    paths: Paths,
    listener: tokio::net::UnixListener,
    capability: tokio::net::UnixListener,
    on_ready: impl FnOnce(),
) -> io::Result<()> {
    let exe = supervisor::current_exe(&config.programs.versions())?;
    let idle = config.daemon.idle_timeout()?;
    let backend = config.daemon.backend;
    let sup = supervisor::Supervisor::new(backend, &paths, exe.clone()).await?;
    if let Err(e) = sup.ensure_proxy(&paths).await {
        eprintln!("the models proxy is not available: {e}");
    }
    let machines = Arc::new(machines::Machines::new(config.clone(), paths.clone(), sup));
    let builder = Arc::new(builder::Builder::new(config, paths.clone(), exe));
    if let Some(timeout) = idle {
        tokio::spawn(machines.clone().idle_loop(timeout));
    }
    tokio::spawn(machines.clone().session_loop());
    {
        // Old versions nothing runs any more go (plan §3.3); a versions
        // directory the user cannot write (a package's) is left alone.
        let machines = machines.clone();
        tokio::spawn(async move {
            loop {
                let _ = versions::collect(&machines).await;
                tokio::time::sleep(VERSION_GC_INTERVAL).await;
            }
        });
    }
    if backend == Backend::Direct {
        // A proxy that died is started again.
        let (machines, paths) = (machines.clone(), paths.clone());
        tokio::spawn(async move {
            loop {
                tokio::time::sleep(Duration::from_secs(30)).await;
                let _ = machines.supervisor.ensure_proxy(&paths).await;
            }
        });
    }
    if backend == Backend::Direct {
        let runtime = paths.runtime.clone();
        tokio::spawn(async move {
            loop {
                tokio::time::sleep(TOUCH_INTERVAL).await;
                touch_tree(&runtime);
            }
        });
    }
    prune_build_logs(&paths.state.join("builds"));
    let approvals = approvals::Approvals::new(paths.state.join("approvals"));
    let daemon = Arc::new(server::Daemon { machines, builder, builds: Default::default(), approvals });
    tokio::spawn(services::serve(daemon.clone(), capability));
    on_ready();

    let stopping = Arc::new(tokio::sync::Notify::new());
    let on_stop = stopping.clone();
    let shutdown = async move {
        use tokio::signal::unix::{SignalKind, signal};
        let (Ok(mut term), Ok(mut int)) = (signal(SignalKind::terminate()), signal(SignalKind::interrupt()))
        else {
            return std::future::pending().await;
        };
        tokio::select! {
            _ = term.recv() => {}
            _ = int.recv() => {}
        }
        on_stop.notify_one();
    };
    let serve = axum::serve(OwnUser(listener), server::router(daemon)).with_graceful_shutdown(shutdown);
    // Long requests (build logs, starts) are cut off after a grace period.
    tokio::select! {
        r = serve => r,
        _ = async { stopping.notified().await; tokio::time::sleep(SHUTDOWN_GRACE).await } => Ok(()),
    }
}

/// Updates the access and modification times of a directory tree.
fn touch_tree(dir: &std::path::Path) {
    let now = nix::sys::time::TimeSpec::UTIME_NOW;
    let _ = nix::sys::stat::utimensat(
        nix::fcntl::AT_FDCWD,
        dir,
        &now,
        &now,
        nix::sys::stat::UtimensatFlags::NoFollowSymlink,
    );
    if let Ok(entries) = std::fs::read_dir(dir) {
        for e in entries.flatten() {
            if e.file_type().is_ok_and(|t| t.is_dir()) {
                touch_tree(&e.path());
            } else {
                let _ = nix::sys::stat::utimensat(
                    nix::fcntl::AT_FDCWD,
                    &e.path(),
                    &now,
                    &now,
                    nix::sys::stat::UtimensatFlags::NoFollowSymlink,
                );
            }
        }
    }
}
