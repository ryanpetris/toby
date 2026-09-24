//! The direct back end: processes started detached from their parent, with
//! logs in files (plan §12.3).

use std::fs::{File, OpenOptions};
use std::io;
use std::os::unix::process::CommandExt;
use std::path::Path;
use std::process::{Command, Stdio};

/// Largest log file before it is rotated to `<name>.1`.
const MAX_LOG: u64 = 10 << 20;

/// Opens a log for appending, rotating it first if it is too large.
pub fn open_log(path: &Path) -> io::Result<File> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    if std::fs::metadata(path).is_ok_and(|m| m.len() > MAX_LOG) {
        std::fs::rename(path, path.with_extension("log.1"))?;
    }
    OpenOptions::new().create(true).append(true).open(path)
}

/// Starts `cmd` in a new session with its output in `log`, so it outlives
/// the process that started it and has no controlling terminal.
pub fn spawn_detached(mut cmd: Command, log: &Path) -> io::Result<u32> {
    let out = open_log(log)?;
    // Keep no directory of the starting shell busy.
    cmd.current_dir("/").stdin(Stdio::null()).stdout(out.try_clone()?).stderr(out);
    // SAFETY: setsid is async-signal-safe.
    unsafe {
        cmd.pre_exec(|| nix::unistd::setsid().map(drop).map_err(io::Error::from));
    }
    let mut child = cmd.spawn()?;
    let pid = child.id();
    // Reap it in the background; it runs on after this process exits.
    std::thread::spawn(move || {
        let _ = child.wait();
    });
    Ok(pid)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn logs_rotate_when_large() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("x.log");
        std::fs::write(&log, vec![b'a'; (MAX_LOG + 1) as usize]).unwrap();
        let mut f = open_log(&log).unwrap();
        io::Write::write_all(&mut f, b"new").unwrap();
        assert_eq!(std::fs::read(&log).unwrap(), b"new");
        assert!(dir.path().join("x.log.1").exists());
    }

    #[test]
    fn detached_processes_get_their_own_session() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("sid.log");
        let mut cmd = Command::new("sh");
        cmd.args(["-c", "ps -o sid= -p $$; ps -o pid= -p $$"]);
        spawn_detached(cmd, &log).unwrap();
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
        let text = loop {
            let text = std::fs::read_to_string(&log).unwrap_or_default();
            if text.lines().count() >= 2 || std::time::Instant::now() > deadline {
                break text;
            }
            std::thread::sleep(std::time::Duration::from_millis(20));
        };
        let ids: Vec<&str> = text.split_whitespace().collect();
        assert_eq!(ids.len(), 2, "{text}");
        assert_eq!(ids[0], ids[1], "the process leads its own session");
    }
}
