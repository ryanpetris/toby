//! Installed Toby versions (plan §3.3): `<versions>/<version>/toby` with
//! `current` pointing at one of them. A version stays while anything runs
//! from it: host processes (a machine's file share keeps the version the
//! machine started with, which is also its relay's), and sessions in
//! machines (they run the version they were started with).

use std::collections::BTreeSet;
use std::io;
use std::path::Path;

use crate::control::Control;
use crate::machines::Machines;

/// Versions host processes of this user run from, by their executables.
fn host_versions(versions: &Path) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    let canonical = std::fs::canonicalize(versions).unwrap_or_else(|_| versions.to_path_buf());
    let Ok(procs) = std::fs::read_dir("/proc") else { return out };
    for p in procs.flatten() {
        let Ok(exe) = std::fs::read_link(p.path().join("exe")) else { continue };
        // A binary replaced on disk shows as "<path> (deleted)".
        let exe = exe.to_string_lossy().trim_end_matches(" (deleted)").to_string();
        if let Ok(rest) = Path::new(&exe).strip_prefix(&canonical)
            && let Some(v) = rest.components().next()
        {
            out.insert(v.as_os_str().to_string_lossy().into_owned());
        }
    }
    out
}

/// The installed versions, not counting `current`.
fn installed(versions: &Path) -> io::Result<Vec<String>> {
    let mut out = Vec::new();
    for e in std::fs::read_dir(versions)?.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if name != "current" && e.file_type().is_ok_and(|t| t.is_dir()) {
            out.push(name);
        }
    }
    out.sort();
    Ok(out)
}

/// Versions still needed.
pub async fn in_use(machines: &Machines) -> BTreeSet<String> {
    let versions = machines.config.programs.versions();
    let mut used = host_versions(&versions);
    used.insert(crate::builder::runtime_version(&versions));
    for spec in machines.records() {
        let observed = machines.observe(&spec.id).await;
        if observed.state == "stopped" {
            continue;
        }
        if let Some(v) = observed.status.and_then(|s| s.relay_version) {
            used.insert(v);
        }
        if let Ok(mut c) = Control::connect(&machines.runtime(&spec.id)).await {
            for s in c.sessions().await.unwrap_or_default() {
                used.extend(s.version);
            }
        }
    }
    used
}

/// Removes installed versions nothing uses. Returns the removed versions
/// and the ones that could not be removed, with the reason.
pub async fn collect(machines: &Machines) -> io::Result<(Vec<String>, Vec<(String, String)>)> {
    let versions = machines.config.programs.versions();
    let used = in_use(machines).await;
    let (mut removed, mut failed) = (Vec::new(), Vec::new());
    for v in installed(&versions)? {
        if used.contains(&v) {
            continue;
        }
        match std::fs::remove_dir_all(versions.join(&v)) {
            Ok(()) => removed.push(v),
            Err(e) => failed.push((v, e.to_string())),
        }
    }
    Ok((removed, failed))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn installed_versions_skip_current() {
        let dir = tempfile::tempdir().unwrap();
        for v in ["0.17.0", "0.18.0"] {
            std::fs::create_dir_all(dir.path().join(v)).unwrap();
        }
        std::os::unix::fs::symlink("0.18.0", dir.path().join("current")).unwrap();
        assert_eq!(installed(dir.path()).unwrap(), ["0.17.0", "0.18.0"]);
    }

    #[test]
    fn this_process_counts_as_a_user_of_its_directory() {
        let exe = std::env::current_exe().unwrap();
        let dir = exe.parent().unwrap().parent().unwrap();
        let name = exe.parent().unwrap().file_name().unwrap().to_string_lossy().into_owned();
        assert!(host_versions(dir).contains(&name));
    }
}
