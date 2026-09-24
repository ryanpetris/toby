//! `toby mount` and `toby unmount`: attach host directories to a running
//! machine by editing its desired state (plan §10.4).

use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::time::Duration;

use anyhow::{Context, bail};
use nix::fcntl::{Flock, FlockArg};
use toby_config::machine::{Attach, AttachState, AttachStatus, MachineSpec, MachineStatus};
use toby_config::paths::MachineRuntime;

use crate::cli::{MachineSelector, MountArgs};
use crate::client;
use crate::internal::load_config;

/// Longest wait for the machine to apply a change (a guest helper may take
/// up to two minutes).
const APPLY_TIMEOUT: Duration = Duration::from_secs(150);

/// Checks a guest mount point: absolute, normalized, not the root directory.
fn check_guest_path(at: &str) -> anyhow::Result<()> {
    let normal = at.strip_prefix('/').is_some_and(|rest| {
        !rest.is_empty() && rest.split('/').all(|c| !c.is_empty() && c != "." && c != "..")
    });
    if !normal {
        bail!("{at:?} cannot be used as a mount point in the machine");
    }
    Ok(())
}

fn default_guest_path(host: &Path) -> anyhow::Result<String> {
    let name = host.file_name().and_then(|n| n.to_str()).context("choose a mount point with --at")?;
    Ok(format!("/toby/workspace/{name}"))
}

/// Changes the desired state under its lock and returns the new generation.
fn update_desired(
    path: &Path,
    f: impl FnOnce(&mut MachineSpec) -> anyhow::Result<()>,
) -> anyhow::Result<u64> {
    let lock_path = path.with_extension("lock");
    let file = std::fs::OpenOptions::new().create(true).truncate(false).write(true).open(&lock_path)?;
    let _lock = Flock::lock(file, FlockArg::LockExclusive).map_err(|(_, e)| e)?;
    let mut spec = MachineSpec::load(path)?;
    f(&mut spec)?;
    spec.generation += 1;
    spec.store(path)?;
    Ok(spec.generation)
}

/// Waits until the machine has applied `generation` and returns its status.
async fn applied(rt: &MachineRuntime, generation: u64) -> anyhow::Result<MachineStatus> {
    let deadline = tokio::time::Instant::now() + APPLY_TIMEOUT;
    loop {
        if let Ok(s) = MachineStatus::load(&rt.status())
            && s.observed_generation >= generation
        {
            return Ok(s);
        }
        if tokio::time::Instant::now() > deadline {
            bail!("the machine did not apply the change in time");
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
}

fn find<'a>(status: &'a MachineStatus, id: &str) -> Option<&'a AttachStatus> {
    status.attach.iter().find(|a| a.id == id)
}

pub async fn mount(args: MountArgs) -> anyhow::Result<ExitCode> {
    if args.persist {
        bail!("--persist is not implemented yet");
    }
    let (_, paths) = load_config()?;
    let (machine, rt) = client::select(&paths, &args.machine).await?;
    let host = std::fs::canonicalize(&args.path)
        .with_context(|| format!("{} does not exist", args.path.display()))?;
    if !host.is_dir() {
        bail!("{} is not a directory", host.display());
    }
    let host_str = host.to_str().context("the path is not UTF-8")?.to_string();
    let at = match args.at {
        Some(at) => at,
        None => default_guest_path(&host)?,
    };
    check_guest_path(&at)?;

    let id = toby_config::new_id().to_lowercase();
    let desired = paths.machine_desired(&machine);
    let generation = update_desired(&desired, |spec| {
        if let Some(a) = spec.attach.iter().find(|a| a.at == at) {
            bail!("{} is already mounted at {at}", a.host);
        }
        spec.attach.push(Attach {
            id: id.clone(),
            host: host_str.clone(),
            at: at.clone(),
            read_only: args.ro,
            pinned: true,
        });
        Ok(())
    })?;

    let status = applied(&rt, generation).await?;
    match find(&status, &id) {
        Some(a) if a.state == AttachState::Ready => {
            println!("{at}");
            Ok(ExitCode::SUCCESS)
        }
        other => {
            let error = other.and_then(|a| a.error.clone()).unwrap_or_else(|| "unknown error".into());
            update_desired(&desired, |spec| {
                spec.attach.retain(|a| a.id != id);
                Ok(())
            })?;
            bail!("mounting {host_str}: {error}")
        }
    }
}

pub async fn unmount(target: String, sel: MachineSelector) -> anyhow::Result<ExitCode> {
    let (_, paths) = load_config()?;
    let (machine, rt) = client::select(&paths, &sel).await?;
    let host: Option<PathBuf> = std::fs::canonicalize(&target).ok();
    let desired = paths.machine_desired(&machine);

    let mut removed = None;
    let generation = update_desired(&desired, |spec| {
        let pos = spec
            .attach
            .iter()
            .position(|a| a.id == target || host.as_deref().is_some_and(|h| Path::new(&a.host) == h))
            .with_context(|| format!("{target} is not mounted in machine {machine}"))?;
        removed = Some(spec.attach.remove(pos));
        Ok(())
    })?;
    let removed = removed.expect("set by update_desired");

    let status = applied(&rt, generation).await?;
    if let Some(still) = find(&status, &removed.id) {
        let error = still.error.clone().unwrap_or_else(|| "unknown error".into());
        // Keep the desired state in line with the guest, which still has it.
        update_desired(&desired, |spec| {
            spec.attach.push(removed.clone());
            Ok(())
        })?;
        bail!("{error}");
    }
    Ok(ExitCode::SUCCESS)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn guest_paths_are_checked() {
        for ok in ["/toby/workspace/x", "/home/dev/src", "/a"] {
            assert!(check_guest_path(ok).is_ok(), "{ok}");
        }
        for bad in ["/", "relative", "/a/../b", "/a/./b", "/a//b", "/a/", ""] {
            assert!(check_guest_path(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn default_mount_point_uses_the_directory_name() {
        assert_eq!(default_guest_path(Path::new("/home/u/src/toby")).unwrap(), "/toby/workspace/toby");
        assert!(default_guest_path(Path::new("/")).is_err());
    }
}
