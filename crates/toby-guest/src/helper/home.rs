//! `home-mount`, `attach`, `detach` and `links`.

use std::io;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};

use nix::mount::{MntFlags, MsFlags, mount, umount2};

/// Marks a home that has been set up, so `/etc/skel` is copied only once.
const MARKER: &str = ".toby-home";

/// Decodes the octal escapes (`\040` for a space) of a mountinfo field.
fn unescape(b: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'\\'
            && i + 3 < b.len()
            && (b'0'..=b'3').contains(&b[i + 1])
            && b[i + 2..i + 4].iter().all(|c| (b'0'..=b'7').contains(c))
        {
            out.push((b[i + 1] - b'0') * 64 + (b[i + 2] - b'0') * 8 + (b[i + 3] - b'0'));
            i += 4;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    out
}

/// A mount as mountinfo describes it.
#[derive(Debug, PartialEq, Eq)]
struct MountEntry {
    /// The mounted directory within its file system, decoded.
    root: PathBuf,
    fstype: Vec<u8>,
}

/// The topmost mount at `path` (a canonical path). Works on bytes, since
/// names need not be UTF-8.
fn top_mount(mountinfo: &[u8], path: &Path) -> Option<MountEntry> {
    mountinfo.split(|b| *b == b'\n').rev().find_map(|l| {
        let fields: Vec<&[u8]> = l.split(|b| *b == b' ').collect();
        let (root, point) = (fields.get(3)?, fields.get(4)?);
        if unescape(point) != path.as_os_str().as_bytes() {
            return None;
        }
        // Optional fields end with a lone "-"; the file system type follows.
        let sep = fields.iter().skip(6).position(|f| *f == b"-")? + 6;
        let fstype = fields.get(sep + 1)?.to_vec();
        Some(MountEntry { root: PathBuf::from(std::ffi::OsStr::from_bytes(&unescape(root))), fstype })
    })
}

fn mount_at(path: &Path) -> Option<MountEntry> {
    top_mount(&std::fs::read("/proc/self/mountinfo").ok()?, path)
}

fn mounted_at(path: &Path) -> bool {
    mount_at(path).is_some()
}

/// Where the file share is mounted in the guest.
const FILE_SHARE: &str = "/run/toby/fs";

/// Whether a mount is the attachment `src` (`/run/toby/fs/projects/<id>`):
/// the file share's `/projects/<id>` bound there.
fn is_attachment(mount: &MountEntry, src: &Path) -> bool {
    let Ok(rel) = src.strip_prefix(FILE_SHARE) else { return false };
    mount.fstype == b"virtiofs" && mount.root == Path::new("/").join(rel) && mount.root != Path::new("/")
}

/// Copies `src` into `dst` recursively, keeping modes and symlinks, owned by
/// `uid`/`gid`. Existing files are left alone; existing directories are
/// completed, so an interrupted copy finishes on the next run.
pub fn copy_tree(src: &Path, dst: &Path, uid: u32, gid: u32) -> io::Result<()> {
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let from = entry.path();
        let to = dst.join(entry.file_name());
        let meta = std::fs::symlink_metadata(&from)?;
        let ft = meta.file_type();
        if let Ok(existing) = std::fs::symlink_metadata(&to) {
            if ft.is_dir() && existing.is_dir() {
                copy_tree(&from, &to, uid, gid)?;
            }
            continue;
        }
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
    let at = &std::fs::canonicalize(at)?;
    if !mounted_at(at) {
        mount(Some(device), at, Some("ext4"), MsFlags::MS_NOSUID | MsFlags::MS_NODEV, None::<&str>).map_err(
            |e| io::Error::other(format!("mounting {} at {}: {e}", device.display(), at.display())),
        )?;
    }
    if !at.join(MARKER).exists() {
        if skel.is_dir() {
            copy_tree(skel, at, uid, gid)?;
        }
        // Private on first use; the user may change it later.
        std::fs::set_permissions(at, std::fs::Permissions::from_mode(0o700))?;
        std::fs::write(at.join(MARKER), "")?;
        std::os::unix::fs::lchown(at.join(MARKER), Some(uid), Some(gid))?;
    }
    // The home's root belongs to its user even if the UID was changed.
    let meta = std::fs::metadata(at)?;
    if meta.uid() != uid || meta.gid() != gid {
        std::os::unix::fs::chown(at, Some(uid), Some(gid))?;
    }
    Ok(())
}

/// Bind-mounts an attachment from the file share at `at`, without setuid
/// or device files, read-only if requested.
pub fn attach(src: &Path, at: &Path, read_only: bool) -> io::Result<()> {
    let created = !at.exists();
    std::fs::create_dir_all(at)?;
    let at = std::fs::canonicalize(at)?;
    if created {
        mark_mount_point(&at);
    }
    if let Some(mount) = mount_at(&at) {
        // Already attached (a helper run again after a restart), unless
        // something else is mounted there.
        if is_attachment(&mount, src) {
            return Ok(());
        }
        return Err(io::Error::other(format!("something else is mounted at {}", at.display())));
    }
    mount(Some(src), &at, None::<&str>, MsFlags::MS_BIND, None::<&str>)
        .map_err(|e| io::Error::other(format!("mounting {} at {}: {e}", src.display(), at.display())))?;
    let mut flags = MsFlags::MS_BIND | MsFlags::MS_REMOUNT | MsFlags::MS_NOSUID | MsFlags::MS_NODEV;
    if read_only {
        flags |= MsFlags::MS_RDONLY;
    }
    if let Err(e) = mount(None::<&str>, &at, None::<&str>, flags, None::<&str>) {
        // Never leave the bind without its restrictions.
        let _ = umount2(&at, MntFlags::MNT_DETACH);
        return Err(io::Error::other(format!("restricting the mount at {}: {e}", at.display())));
    }
    Ok(())
}

/// Unmounts the attachment `src` from `at`; refused while it is in use. A
/// mount point Toby created (empty, owned by root) is left so that nobody can
/// write to it, so writes meant for the attachment fail instead of landing in
/// the root or home; other directories keep their owner and mode.
pub fn detach(src: &Path, at: &Path) -> io::Result<()> {
    let at = match std::fs::canonicalize(at) {
        Ok(p) => p,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(e),
    };
    match mount_at(&at) {
        Some(mount) if is_attachment(&mount, src) => {
            umount2(&at, MntFlags::empty()).map_err(|e| match e {
                nix::errno::Errno::EBUSY => io::Error::other(format!("{} is in use", at.display())),
                e => io::Error::other(format!("unmounting {}: {e}", at.display())),
            })?;
        }
        Some(_) => return Err(io::Error::other(format!("something else is mounted at {}", at.display()))),
        None => return Ok(()),
    }
    if mount_at(&at).is_some_and(|mount| is_attachment(&mount, src)) {
        return Err(io::Error::other(format!("{} is still mounted", at.display())));
    }
    lock_down(&at)
}

/// Extended attribute marking a directory Toby created as a mount point.
const MOUNT_POINT_XATTR: &std::ffi::CStr = c"trusted.toby.mount-point";

/// Marks a directory Toby created for an attachment, so detaching may lock
/// it down later. File systems without extended attributes get no mark.
fn mark_mount_point(dir: &Path) {
    let Ok(path) = std::ffi::CString::new(dir.as_os_str().as_bytes()) else { return };
    // SAFETY: both strings are valid and NUL-terminated; the value is one byte.
    unsafe {
        libc::lsetxattr(path.as_ptr(), MOUNT_POINT_XATTR.as_ptr(), b"1".as_ptr().cast(), 1, 0);
    }
}

/// Makes an empty mount point Toby created unwritable for the user, without
/// following a link put in its place. Other directories are left alone.
fn lock_down(dir: &Path) -> io::Result<()> {
    use nix::fcntl::{OFlag, open};
    use nix::sys::stat::Mode;
    let fd = match open(
        dir,
        OFlag::O_RDONLY | OFlag::O_DIRECTORY | OFlag::O_NOFOLLOW | OFlag::O_CLOEXEC,
        Mode::empty(),
    ) {
        Ok(fd) => fd,
        Err(_) => return Ok(()),
    };
    let st = nix::sys::stat::fstat(&fd)?;
    let mut mark = [0u8; 1];
    // SAFETY: the descriptor is open and the buffer is one byte long.
    let marked = unsafe {
        libc::fgetxattr(
            std::os::fd::AsRawFd::as_raw_fd(&fd),
            MOUNT_POINT_XATTR.as_ptr(),
            mark.as_mut_ptr().cast(),
            mark.len(),
        )
    } == 1;
    if !marked || st.st_uid != 0 {
        return Ok(());
    }
    let empty = std::fs::read_dir(format!("/proc/self/fd/{}", std::os::fd::AsRawFd::as_raw_fd(&fd)))?
        .next()
        .is_none();
    if empty {
        nix::sys::stat::fchmod(&fd, Mode::from_bits_truncate(0o555))?;
    }
    Ok(())
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
    fn mountinfo_fields_are_decoded() {
        let info = b"22 1 0:21 / / rw - ext4 /dev/vda rw\n\
                    40 22 0:30 /projects/a1 /toby/workspace/My\\040Project rw,nosuid shared:5 - virtiofs toby rw\n\
                    41 22 0:30 /projects/b\\134x /srv/b rw - virtiofs toby rw\n\
                    42 22 8:1 /boot /build/boot rw - ext4 /dev/vda rw\n";
        let a1 = top_mount(info, Path::new("/toby/workspace/My Project")).unwrap();
        assert_eq!(a1, MountEntry { root: "/projects/a1".into(), fstype: b"virtiofs".to_vec() });
        assert_eq!(top_mount(info, Path::new("/srv/b")).unwrap().root, PathBuf::from("/projects/b\\x"));
        assert_eq!(top_mount(info, Path::new("/toby/workspace/My\\040Project")), None);
        let odd = b"40 22 0:30 /projects/c /srv/\xff rw - virtiofs toby rw\n";
        let odd_path = Path::new(std::ffi::OsStr::from_bytes(b"/srv/\xff"));
        assert_eq!(top_mount(odd, odd_path).unwrap().root, PathBuf::from("/projects/c"));

        assert!(is_attachment(&a1, Path::new("/run/toby/fs/projects/a1")));
        assert!(!is_attachment(&a1, Path::new("/run/toby/fs/projects/a2")));
        // A guest bind of its own /boot is not the attachment "boot".
        let boot = top_mount(info, Path::new("/build/boot")).unwrap();
        assert!(!is_attachment(&boot, Path::new("/run/toby/fs/projects/boot")));
        let suffix = MountEntry { root: "/a1".into(), fstype: b"virtiofs".to_vec() };
        assert!(!is_attachment(&suffix, Path::new("/run/toby/fs/projects/a1")));
    }

    #[test]
    fn interrupted_skeleton_copies_are_completed() {
        let dir = tempfile::tempdir().unwrap();
        let skel = dir.path().join("skel");
        let home = dir.path().join("home");
        std::fs::create_dir_all(skel.join(".config/app")).unwrap();
        std::fs::write(skel.join(".config/app/rc"), "x").unwrap();
        std::fs::create_dir_all(home.join(".config")).unwrap();
        let (uid, gid) = (nix::unistd::getuid().as_raw(), nix::unistd::getgid().as_raw());
        copy_tree(&skel, &home, uid, gid).unwrap();
        assert_eq!(std::fs::read_to_string(home.join(".config/app/rc")).unwrap(), "x");
    }

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
        assert_eq!(std::fs::read_link(home.join(".link")).unwrap(), Path::new(".bashrc"));
    }

    #[test]
    fn links_point_at_the_runtime_binary() {
        let dir = tempfile::tempdir().unwrap();
        let bin = dir.path().join("bin");
        let target = Path::new("/run/toby/fs/versions/current/toby");
        links(&bin, target).unwrap();
        links(&bin, target).unwrap();
        assert_eq!(std::fs::read_link(bin.join("toby")).unwrap(), target);
        assert_eq!(std::fs::read_link(bin.join("toby-connect")).unwrap(), Path::new("toby"));
    }
}
