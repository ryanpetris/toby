//! tobyd, the per-user control plane (plan §3, §8, §18).

pub mod builder;
pub mod builds;
pub mod control;
pub mod download;
pub mod machines;
pub mod server;
pub mod supervisor;

use std::io;
use std::sync::Arc;
use std::time::Duration;

use toby_config::global::{Backend, GlobalConfig};
use toby_config::paths::Paths;

/// How often the direct back end's runtime directory is touched, so
/// age-based cleanup of `/tmp` leaves it alone (plan §6.1).
const TOUCH_INTERVAL: Duration = Duration::from_secs(3600);

/// Serves the API on `listener` until SIGTERM or SIGINT.
pub async fn run(
    config: GlobalConfig,
    paths: Paths,
    listener: tokio::net::UnixListener,
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
    if backend == Backend::Direct {
        let runtime = paths.runtime.clone();
        tokio::spawn(async move {
            loop {
                tokio::time::sleep(TOUCH_INTERVAL).await;
                touch_tree(&runtime);
            }
        });
    }
    let daemon = Arc::new(server::Daemon { machines, builder, builds: Default::default() });
    on_ready();

    let shutdown = async {
        use tokio::signal::unix::{SignalKind, signal};
        let (Ok(mut term), Ok(mut int)) = (signal(SignalKind::terminate()), signal(SignalKind::interrupt()))
        else {
            return std::future::pending().await;
        };
        tokio::select! {
            _ = term.recv() => {}
            _ = int.recv() => {}
        }
    };
    axum::serve(listener, server::router(daemon)).with_graceful_shutdown(shutdown).await
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
