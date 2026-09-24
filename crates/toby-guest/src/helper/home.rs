//! `home-mount` and `links`.

use std::io;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::Path;

use nix::mount::{MsFlags, mount};

/// Marks a home that has been set up, so `/etc/skel` is copied only once.
const MARKER: &str = ".toby-home";

fn mounted_at(path: &Path) -> bool {
    std::fs::read_to_string("/proc/self/mountinfo")
        .map(|m| m.lines().any(|l| l.split(' ').nth(4) == path.to_str()))
        .unwrap_or(false)
}

/// Copies `src` into `dst` recursively, keeping modes and symlinks, owned by
/// `uid`/`gid`. Existing files are left alone.
pub fn copy_tree(src: &Path, dst: &Path, uid: u32, gid: u32) -> io::Result<()> {
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let from = entry.path();
        let to = dst.join(entry.file_name());
        if std::fs::symlink_metadata(&to).is_ok() {
            continue;
        }
        let meta = std::fs::symlink_metadata(&from)?;
        let ft = meta.file_type();
        if ft.is_dir() {
            std::fs::create_dir(&to)?;
            std::fs::set_permissions(&to, std::fs::Permissions::from_mode(meta.mode() & 0o7777))?;
            copy_tree(&from, &to, uid, gid)?;
        } else if ft.is_symlink() {
            std::os::unix::fs::symlink(std::fs::read_link(&from)?, &to)?;
        } else if ft.is_file() {
            std::fs::copy(&from, &to)?;
        } else {
            continue;
        }
        std::os::unix::fs::lchown(&to, Some(uid), Some(gid))?;
    }
    Ok(())
}

/// Mounts the home disk at `at` and prepares it on first use.
pub fn home_mount(device: &Path, at: &Path, uid: u32, gid: u32, skel: &Path) -> io::Result<()> {
    std::fs::create_dir_all(at)?;
    if !mounted_at(at) {
        mount(
            Some(device),
            at,
            Some("ext4"),
            MsFlags::MS_NOSUID | MsFlags::MS_NODEV,
            None::<&str>,
        )
        .map_err(|e| io::Error::other(format!("mounting {} at {}: {e}", device.display(), at.display())))?;
    }
    if !at.join(MARKER).exists() {
        if skel.is_dir() {
            copy_tree(skel, at, uid, gid)?;
        }
        std::fs::write(at.join(MARKER), "")?;
        std::os::unix::fs::lchown(at.join(MARKER), Some(uid), Some(gid))?;
    }
    // The home's root belongs to its user even if the UID was changed.
    let meta = std::fs::metadata(at)?;
    if meta.uid() != uid || meta.gid() != gid {
        std::os::unix::fs::chown(at, Some(uid), Some(gid))?;
    }
    std::fs::set_permissions(at, std::fs::Permissions::from_mode(0o700))?;
    Ok(())
}

/// Bind-mounts an attachment from the file share at `at`, without setuid
/// or device files, read-only if requested.
pub fn attach(src: &Path, at: &Path, read_only: bool) -> io::Result<()> {
    std::fs::create_dir_all(at)?;
    if mounted_at(at) {
        return Ok(());
    }
    mount(Some(src), at, None::<&str>, MsFlags::MS_BIND, None::<&str>)
        .map_err(|e| io::Error::other(format!("mounting {} at {}: {e}", src.display(), at.display())))?;
    let mut flags = MsFlags::MS_BIND | MsFlags::MS_REMOUNT | MsFlags::MS_NOSUID | MsFlags::MS_NODEV;
    if read_only {
        flags |= MsFlags::MS_RDONLY;
    }
    mount(None::<&str>, at, None::<&str>, flags, None::<&str>).map_err(io::Error::from)
}

/// Multi-call names linked to the `toby` binary in `/run/toby/bin`.
pub const LINKS: &[&str] = &["toby-connect", "toby-session", "toby-helper"];

/// Creates `bin/toby` pointing at `target` and the multi-call names.
pub fn links(bin: &Path, target: &Path) -> io::Result<()> {
    std::fs::create_dir_all(bin)?;
    let replace = |name: &str, to: &Path| -> io::Result<()> {
        let path = bin.join(name);
        let tmp = bin.join(format!(".{name}.tmp"));
        let _ = std::fs::remove_file(&tmp);
        std::os::unix::fs::symlink(to, &tmp)?;
        std::fs::rename(&tmp, &path)
    };
    replace("toby", target)?;
    for name in LINKS {
        replace(name, Path::new("toby"))?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn copies_skel_without_overwriting() {
        let dir = tempfile::tempdir().unwrap();
        let skel = dir.path().join("skel");
        let home = dir.path().join("home");
        std::fs::create_dir_all(skel.join(".config/app")).unwrap();
        std::fs::write(skel.join(".bashrc"), "skel").unwrap();
        std::fs::write(skel.join(".config/app/rc"), "x").unwrap();
        std::os::unix::fs::symlink(".bashrc", skel.join(".link")).unwrap();
        std::fs::create_dir_all(&home).unwrap();
        std::fs::write(home.join(".bashrc"), "mine").unwrap();

        let (uid, gid) = (nix::unistd::getuid().as_raw(), nix::unistd::getgid().as_raw());
        copy_tree(&skel, &home, uid, gid).unwrap();
        assert_eq!(std::fs::read_to_string(home.join(".bashrc")).unwrap(), "mine");
        assert_eq!(std::fs::read_to_string(home.join(".config/app/rc")).unwrap(), "x");
        assert_eq!(
            std::fs::read_link(home.join(".link")).unwrap(),
            Path::new(".bashrc")
        );
    }

    #[test]
    fn links_point_at_the_runtime_binary() {
        let dir = tempfile::tempdir().unwrap();
        let bin = dir.path().join("bin");
        let target = Path::new("/run/toby/fs/versions/current/toby");
        links(&bin, target).unwrap();
        links(&bin, target).unwrap();
        assert_eq!(std::fs::read_link(bin.join("toby")).unwrap(), target);
        assert_eq!(
            std::fs::read_link(bin.join("toby-connect")).unwrap(),
            Path::new("toby")
        );
    }
}
