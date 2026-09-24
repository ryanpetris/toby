//! Installed Toby versions (plan §3.3): `<versions>/<version>/toby` with
//! `current` pointing at one of them. A version stays while anything runs
//! from it or may still start from it: host processes, a machine's relay,
//! and sessions in machines (by the version they were started with and by
//! the binary they run). Newly installed versions and `current` stay too.
//!
//! When tobyd starts, the models proxy and each machine's host process
//! still running an older version are restarted on `current` (plan §3.2).

use std::collections::BTreeSet;
use std::io;
use std::path::Path;
use std::time::{Duration, SystemTime};

use crate::control::Control;
use crate::machines::Machines;

/// Taken while `current` is switched or versions are removed.
pub const LOCK: &str = ".lock";

/// A version directory younger than this may still be being installed.
const MIN_AGE: Duration = Duration::from_secs(3600);

/// Serializes collections.
static COLLECTING: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

/// A host process running from an installed version.
pub struct HostProcess {
    pub pid: i32,
    /// When it started (clock ticks after boot), which tells it from a
    /// later process with the same pid.
    pub start: u64,
    version: String,
    /// Its arguments after the program name.
    args: Vec<String>,
}

/// When process `pid` started, from `/proc/<pid>/stat`.
pub fn start_time(pid: i32) -> Option<u64> {
    let stat = std::fs::read_to_string(format!("/proc/{pid}/stat")).ok()?;
    // The command name is in parentheses and may hold spaces.
    stat.rsplit_once(')')?.1.split_whitespace().nth(19)?.parse().ok()
}

/// This user's processes running from `versions`, by their executables.
fn host_processes(versions: &Path) -> io::Result<Vec<HostProcess>> {
    let mut out = Vec::new();
    let canonical = std::fs::canonicalize(versions).unwrap_or_else(|_| versions.to_path_buf());
    for p in std::fs::read_dir("/proc")? {
        let p = p?;
        let Some(pid) = p.file_name().to_str().and_then(|n| n.parse::<i32>().ok()) else { continue };
        let Ok(exe) = std::fs::read_link(p.path().join("exe")) else { continue };
        // A binary replaced on disk shows as "<path> (deleted)".
        let exe = exe.to_string_lossy().trim_end_matches(" (deleted)").to_string();
        let Some(version) = Path::new(&exe)
            .strip_prefix(&canonical)
            .ok()
            .and_then(|rest| rest.components().next())
            .map(|v| v.as_os_str().to_string_lossy().into_owned())
        else {
            continue;
        };
        let mut cmdline = std::fs::read(p.path().join("cmdline")).unwrap_or_default();
        // Each argument ends with a NUL.
        if cmdline.last() == Some(&0) {
            cmdline.pop();
        }
        let args =
            cmdline.split(|b| *b == 0).skip(1).map(|a| String::from_utf8_lossy(a).into_owned()).collect();
        let Some(start) = start_time(pid) else { continue };
        out.push(HostProcess { pid, start, version, args });
    }
    Ok(out)
}

/// The version `current` points at, read now.
fn current(versions: &Path) -> Option<String> {
    let target = std::fs::read_link(versions.join("current")).ok()?;
    Some(target.file_name()?.to_string_lossy().into_owned())
}

/// The installed versions, not counting `current`.
fn installed(versions: &Path) -> io::Result<Vec<String>> {
    let mut out = Vec::new();
    for e in std::fs::read_dir(versions)?.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if name != "current" && !name.starts_with('.') && e.file_type().is_ok_and(|t| t.is_dir()) {
            out.push(name);
        }
    }
    out.sort();
    Ok(out)
}

/// How long ago a version directory was installed: its change time,
/// which copying or unpacking with old times cannot set back.
fn installed_for(dir: &Path) -> Option<Duration> {
    let ctime = std::os::unix::fs::MetadataExt::ctime(&std::fs::metadata(dir).ok()?);
    let at = SystemTime::UNIX_EPOCH + Duration::from_secs(u64::try_from(ctime).ok()?);
    SystemTime::now().duration_since(at).ok()
}

/// Whether an installed version may go: nothing uses it, it is not
/// `current`, and it is a complete install that is not new.
fn removable(
    dir: &Path,
    version: &str,
    used: &BTreeSet<String>,
    current: &str,
    age: Option<Duration>,
) -> bool {
    !used.contains(version)
        && version != current
        && dir.join("toby").is_file()
        && age.is_some_and(|a| a >= MIN_AGE)
}

/// Whether a version from a guest is a plausible version name.
fn version_name(v: &str) -> bool {
    !v.is_empty()
        && v.len() <= 64
        && !v.starts_with('.')
        && v.bytes().all(|b| b.is_ascii_alphanumeric() || b"._+-".contains(&b))
}

/// The version a session binary path in the guest names.
fn guest_binary_version(path: &str) -> Option<String> {
    let v = path.strip_prefix("/run/toby/fs/versions/")?.split('/').next()?;
    version_name(v).then(|| v.to_string())
}

/// Versions still needed; an error when a running machine does not answer,
/// since its sessions could not be seen.
pub async fn in_use(machines: &Machines) -> Result<BTreeSet<String>, String> {
    let versions = machines.config.programs.versions();
    let processes = host_processes(&versions).map_err(|e| format!("listing processes: {e}"))?;
    let mut used: BTreeSet<String> = processes.into_iter().map(|p| p.version).collect();
    for spec in machines.records() {
        let observed = machines.observe(&spec.id).await;
        if observed.state == "stopped" {
            continue;
        }
        let unanswered = || format!("machine {} did not answer", spec.id);
        let status = observed.status.ok_or_else(unanswered)?;
        used.extend(status.relay_version);
        let mut c = Control::connect(&machines.runtime(&spec.id)).await.map_err(|_| unanswered())?;
        for s in c.sessions().await.map_err(|_| unanswered())? {
            used.extend(s.version);
            used.extend(guest_binary_version(&s.argv0));
        }
    }
    Ok(used)
}

/// What a collection did.
pub struct Collected {
    pub removed: Vec<String>,
    pub used: BTreeSet<String>,
    /// Versions that could not be removed, with the reason.
    pub failed: Vec<(String, String)>,
}

/// Removes installed versions nothing uses.
pub async fn collect(machines: &Machines) -> io::Result<Collected> {
    let _one = COLLECTING.lock().await;
    let versions = machines.config.programs.versions();
    if current(&versions).is_none() {
        return Err(io::Error::other(format!(
            "{} is not set; nothing was removed",
            versions.join("current").display()
        )));
    }
    if nix::unistd::access(&versions, nix::unistd::AccessFlags::W_OK).is_err() {
        return Err(io::Error::other(format!(
            "{} is not writable (installed by a package); nothing was removed",
            versions.display()
        )));
    }
    let used = in_use(machines).await.map_err(|e| io::Error::other(format!("{e}; nothing was removed")))?;
    // Installers switch `current` holding the same lock, so it never names
    // a version being removed.
    let lock =
        std::fs::OpenOptions::new().create(true).truncate(false).write(true).open(versions.join(LOCK))?;
    let _lock = nix::fcntl::Flock::lock(lock, nix::fcntl::FlockArg::LockExclusive).map_err(|(_, e)| e)?;
    let (mut removed, mut failed) = (Vec::new(), Vec::new());
    // Left by a removal that failed or was cut short.
    for e in std::fs::read_dir(&versions)?.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if let Some(v) = name.strip_prefix(".removing-") {
            let _ = std::fs::rename(e.path(), versions.join(v));
        }
    }
    for v in installed(&versions)? {
        // Read again for each: an upgrade may switch it meanwhile.
        let Some(current) = current(&versions) else { break };
        let dir = versions.join(&v);
        if !removable(&dir, &v, &used, &current, installed_for(&dir)) {
            continue;
        }
        // Moved aside first, so nothing half removed looks installed.
        let aside = versions.join(format!(".removing-{v}"));
        match std::fs::rename(&dir, &aside) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::NotFound => continue,
            Err(e) => {
                failed.push((v, e.to_string()));
                continue;
            }
        }
        match std::fs::remove_dir_all(&aside) {
            Ok(()) => removed.push(v),
            Err(e) => {
                // Tried again next time.
                let _ = std::fs::rename(&aside, &dir);
                failed.push((v, e.to_string()));
            }
        }
    }
    Ok(Collected { removed, used, failed })
}

/// Takes the versions lock, creating the directory and lock file as needed.
fn lock(versions: &Path) -> io::Result<nix::fcntl::Flock<std::fs::File>> {
    std::fs::create_dir_all(versions)?;
    let file =
        std::fs::OpenOptions::new().create(true).truncate(false).write(true).open(versions.join(LOCK))?;
    nix::fcntl::Flock::lock(file, nix::fcntl::FlockArg::LockExclusive).map_err(|(_, e)| io::Error::from(e))
}

/// Installs `binary` as `<versions>/<version>/toby` and points `current` at
/// it (a package's post-install step, plan §3.3). Running processes keep
/// the file they started from.
pub fn install(binary: &Path, versions: &Path, version: &str) -> io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    if !version_name(version) {
        return Err(io::Error::other(format!("{version:?} is not a version name")));
    }
    let _lock = lock(versions)?;
    let dir = versions.join(version);
    std::fs::create_dir_all(&dir)?;
    std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o755))?;
    let staged = dir.join(".toby.new");
    std::fs::copy(binary, &staged)?;
    std::fs::set_permissions(&staged, std::fs::Permissions::from_mode(0o755))?;
    std::fs::File::open(&staged)?.sync_all()?;
    std::fs::rename(&staged, dir.join("toby"))?;
    let link = versions.join(".current.new");
    let _ = std::fs::remove_file(&link);
    std::os::unix::fs::symlink(version, &link)?;
    std::fs::rename(&link, versions.join("current"))
}

/// Every process's use of installed versions, whoever runs it: its
/// executable, mapped files and open files. `toby-fs` holds the files a
/// guest runs open, so machines' relays and sessions count too. Needs
/// root to see other users' processes.
fn used_by_processes(versions: &Path) -> io::Result<BTreeSet<String>> {
    let canonical = std::fs::canonicalize(versions)?;
    let version_of = |path: &str| {
        let path = path.trim_end_matches(" (deleted)");
        Path::new(path)
            .strip_prefix(&canonical)
            .ok()
            .and_then(|rest| rest.components().next())
            .map(|v| v.as_os_str().to_string_lossy().into_owned())
    };
    let mut used = BTreeSet::new();
    for p in std::fs::read_dir("/proc")? {
        let p = p?;
        if p.file_name().to_str().and_then(|n| n.parse::<i32>().ok()).is_none() {
            continue;
        }
        let mut paths: Vec<String> = Vec::new();
        if let Ok(exe) = std::fs::read_link(p.path().join("exe")) {
            paths.push(exe.to_string_lossy().into_owned());
        }
        if let Ok(maps) = std::fs::read_to_string(p.path().join("maps")) {
            paths.extend(maps.lines().filter_map(|l| l.split_once('/').map(|(_, rest)| format!("/{rest}"))));
        }
        if let Ok(fds) = std::fs::read_dir(p.path().join("fd")) {
            paths.extend(
                fds.flatten()
                    .filter_map(|fd| std::fs::read_link(fd.path()).ok())
                    .map(|l| l.to_string_lossy().into_owned()),
            );
        }
        used.extend(paths.iter().filter_map(|path| version_of(path)));
    }
    Ok(used)
}

/// Removes installed versions no process uses, as root for a package's
/// versions directory: after an install, and from a timer.
pub fn collect_system(versions: &Path) -> io::Result<Collected> {
    if !nix::unistd::getuid().is_root() {
        return Err(io::Error::other("only root sees every process that may use a version"));
    }
    let _lock = lock(versions)?;
    let used = used_by_processes(versions)?;
    let (mut removed, mut failed) = (Vec::new(), Vec::new());
    for e in std::fs::read_dir(versions)?.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if let Some(v) = name.strip_prefix(".removing-") {
            let _ = std::fs::rename(e.path(), versions.join(v));
        }
    }
    let Some(current) = current(versions) else { return Ok(Collected { removed, used, failed }) };
    for v in installed(versions)? {
        let dir = versions.join(&v);
        if !removable(&dir, &v, &used, &current, installed_for(&dir)) {
            continue;
        }
        let aside = versions.join(format!(".removing-{v}"));
        if let Err(e) = std::fs::rename(&dir, &aside) {
            failed.push((v, e.to_string()));
            continue;
        }
        match std::fs::remove_dir_all(&aside) {
            Ok(()) => removed.push(v),
            Err(e) => {
                let _ = std::fs::rename(&aside, &dir);
                failed.push((v, e.to_string()));
            }
        }
    }
    Ok(Collected { removed, used, failed })
}

/// Restarts the models proxy and machines' host processes that run an
/// older version than `current`, so they pick up an upgrade.
pub async fn upgrade_control_tier(machines: &Machines, paths: &toby_config::paths::Paths) {
    let versions = machines.config.programs.versions();
    let Some(current) = current(&versions) else { return };
    let Ok(processes) = host_processes(&versions) else { return };
    for p in processes.into_iter().filter(|p| p.version != current) {
        let args: Vec<&str> = p.args.iter().map(String::as_str).collect();
        // Builders run no units of their own and end with their build.
        if matches!(args.as_slice(), ["internal", "machine", "--machine", id] if id.starts_with("builder-")) {
            continue;
        }
        let result = match args.as_slice() {
            ["internal", "proxy", ..] => machines.supervisor.restart_proxy(paths, &p).await,
            ["internal", "machine", "--machine", id] => {
                machines.supervisor.restart_machine_process(id, &p).await
            }
            _ => continue,
        };
        match result {
            Ok(()) => eprintln!("restarted {} (version {}) on version {current}", args[1], p.version),
            Err(e) => eprintln!("restarting {} (version {}): {e}", args[1], p.version),
        }
    }
}

#[cfg(test)]
mod tests {
    use std::os::unix::process::ExitStatusExt;

    use super::*;

    #[test]
    fn installs_and_sees_versions_in_use() {
        let dir = tempfile::tempdir().unwrap();
        let versions = dir.path().join("versions");
        let binary = dir.path().join("toby");
        std::fs::write(&binary, b"one").unwrap();
        super::install(&binary, &versions, "1.0.0").unwrap();
        std::fs::write(&binary, b"two").unwrap();
        super::install(&binary, &versions, "1.1.0").unwrap();
        assert_eq!(current(&versions).as_deref(), Some("1.1.0"));
        assert_eq!(std::fs::read(versions.join("1.0.0/toby")).unwrap(), b"one");
        assert_eq!(installed(&versions).unwrap(), ["1.0.0", "1.1.0"]);
        assert!(super::install(&binary, &versions, "../x").is_err());
        // A file held open, as toby-fs holds what a guest runs.
        let open = std::fs::File::open(versions.join("1.0.0/toby")).unwrap();
        assert!(used_by_processes(&versions).unwrap().contains("1.0.0"));
        drop(open);
        assert!(!used_by_processes(&versions).unwrap().contains("1.0.0"));
    }

    fn install(versions: &Path, v: &str) {
        let dir = versions.join(v);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("toby"), "").unwrap();
    }

    #[test]
    fn installed_versions_skip_current() {
        let dir = tempfile::tempdir().unwrap();
        for v in ["0.17.0", "0.18.0"] {
            std::fs::create_dir_all(dir.path().join(v)).unwrap();
        }
        std::os::unix::fs::symlink("0.18.0", dir.path().join("current")).unwrap();
        assert_eq!(installed(dir.path()).unwrap(), ["0.17.0", "0.18.0"]);
        assert_eq!(current(dir.path()).as_deref(), Some("0.18.0"));
    }

    #[test]
    fn only_old_unused_complete_versions_go() {
        let tmp = tempfile::tempdir().unwrap();
        let v = tmp.path();
        for name in ["0.16.0", "0.17.0", "0.18.0"] {
            install(v, name);
        }
        std::fs::create_dir(v.join("partial")).unwrap();
        let used: BTreeSet<String> = ["0.17.0".to_string()].into();
        let day = Some(Duration::from_secs(86400));
        let go = |name: &str, age| removable(&v.join(name), name, &used, "0.18.0", age);
        assert!(go("0.16.0", day));
        assert!(!go("0.17.0", day), "in use");
        assert!(!go("0.18.0", day), "current");
        assert!(!go("0.16.0", Some(Duration::from_secs(60))), "just installed");
        let old = SystemTime::now() - Duration::from_secs(86400);
        std::fs::File::open(v.join("0.16.0")).unwrap().set_modified(old).unwrap();
        assert!(!go("0.16.0", installed_for(&v.join("0.16.0"))), "installed now, whatever its times say");
        assert!(!go("partial", day), "no binary");
    }

    #[test]
    fn session_binaries_name_their_version() {
        assert_eq!(guest_binary_version("/run/toby/fs/versions/0.18.0/toby").as_deref(), Some("0.18.0"));
        assert_eq!(guest_binary_version("/usr/bin/claude"), None);
        assert_eq!(guest_binary_version("/run/toby/fs/versions/\u{1b}[2J/toby"), None);
    }

    #[tokio::test]
    async fn only_the_process_seen_is_signalled() {
        let mut child = std::process::Command::new("sleep").arg("30").spawn().unwrap();
        let pid = child.id() as i32;
        let start = start_time(pid).unwrap();
        let p = |start| HostProcess { pid, start, version: "0".into(), args: Vec::new() };
        // Another process with the same pid is left alone.
        crate::supervisor::terminate(&p(start + 1)).await.unwrap();
        assert!(child.try_wait().unwrap().is_none());
        crate::supervisor::terminate(&p(start)).await.unwrap();
        assert!(child.wait().unwrap().signal().is_some());
    }

    #[test]
    fn this_process_counts_as_a_user_of_its_directory() {
        let exe = std::env::current_exe().unwrap();
        let dir = exe.parent().unwrap().parent().unwrap();
        let name = exe.parent().unwrap().file_name().unwrap().to_string_lossy().into_owned();
        let me =
            host_processes(dir).unwrap().into_iter().find(|p| p.pid == std::process::id() as i32).unwrap();
        assert_eq!(Some(me.start), start_time(me.pid));
        assert_eq!(me.version, name);
        let args: Vec<String> = std::env::args().skip(1).collect();
        assert_eq!(me.args, args);
    }
}
