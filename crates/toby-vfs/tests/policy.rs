//! Exercises the identity and write policies through the FUSE API.

use std::ffi::CString;
use std::os::unix::fs::{MetadataExt, PermissionsExt};

use fuse_backend_rs::abi::fuse_abi::{CreateIn, stat64};
use fuse_backend_rs::api::filesystem::{Context, Entry, FileSystem, FsOptions, ROOT_ID, SetattrValid};
use toby_vfs::{MountSpec, OVERFLOW_ID, Squash, Tree};

const GUEST_UID: u32 = 4321;

struct Fixture {
    _dir: tempfile::TempDir,
    rw: std::path::PathBuf,
    ro: std::path::PathBuf,
    tree: Tree,
}

fn fixture() -> Fixture {
    let dir = tempfile::tempdir().unwrap();
    let rw = dir.path().join("rw");
    let ro = dir.path().join("ro");
    std::fs::create_dir_all(&rw).unwrap();
    std::fs::create_dir_all(&ro).unwrap();
    std::fs::write(ro.join("file"), "x").unwrap();

    let squash = Squash {
        host_uid: nix::unistd::getuid().as_raw(),
        host_gid: nix::unistd::getgid().as_raw(),
        guest_uid: GUEST_UID,
        guest_gid: GUEST_UID,
    };
    let tree = Tree::new(squash).unwrap();
    tree.mount("/projects/p", MountSpec { source: rw.clone(), read_only: false }).unwrap();
    tree.mount("/versions", MountSpec { source: ro.clone(), read_only: true }).unwrap();
    tree.filesystem().init(FsOptions::all()).unwrap();
    Fixture { _dir: dir, rw, ro, tree }
}

fn name(s: &str) -> CString {
    CString::new(s).unwrap()
}

/// A request context as the guest's root would send it, remapped as the server does.
fn ctx(fs: &impl FileSystem) -> Context {
    let mut c = Context { uid: 0, gid: 0, pid: 1 };
    fs.id_remap(&mut c).unwrap();
    c
}

fn guest_ctx(fs: &impl FileSystem, uid: u32) -> Context {
    let mut c = Context { uid, gid: uid, pid: 1 };
    fs.id_remap(&mut c).unwrap();
    c
}

fn walk(fs: &impl FileSystem, path: &[&str]) -> Entry {
    let c = ctx(fs);
    let mut ino = ROOT_ID;
    let mut entry = None;
    for p in path {
        let e = fs.lookup(&c, ino.into(), &name(p)).unwrap();
        ino = e.inode;
        entry = Some(e);
    }
    entry.unwrap()
}

fn create(fs: &impl FileSystem, parent: u64, n: &str, mode: u32, uid: u32) -> std::io::Result<Entry> {
    let args = CreateIn { flags: libc::O_RDWR as u32, mode: libc::S_IFREG | mode, umask: 0, fuse_flags: 0 };
    fs.create(&guest_ctx(fs, uid), parent.into(), &name(n), args).map(|(e, h, _, _)| {
        if let Some(h) = h {
            let _ = fs.release(&ctx(fs), e.inode.into(), 0, h, false, false, None);
        }
        e
    })
}

fn errno(e: std::io::Error) -> i32 {
    e.raw_os_error().unwrap()
}

#[test]
fn files_from_any_guest_identity_belong_to_the_host_user() {
    let f = fixture();
    let fs = f.tree.filesystem();
    let p = walk(&*fs, &["projects", "p"]);

    let by_root = create(&*fs, p.inode, "root-file", 0o644, 0).unwrap();
    let by_user = create(&*fs, p.inode, "user-file", 0o644, GUEST_UID).unwrap();
    assert_eq!(by_root.attr.st_uid, GUEST_UID);
    assert_eq!(by_user.attr.st_uid, GUEST_UID);
    for n in ["root-file", "user-file"] {
        assert_eq!(std::fs::metadata(f.rw.join(n)).unwrap().uid(), nix::unistd::getuid().as_raw());
    }
}

#[test]
fn other_owners_are_reported_as_overflow() {
    let f = fixture();
    let fs = f.tree.filesystem();
    // /proc/1 is owned by root on the host, which is not the host user.
    let tree = Tree::new(Squash { host_uid: 99999, host_gid: 99999, guest_uid: 1, guest_gid: 1 }).unwrap();
    tree.mount("/v", MountSpec { source: f.ro.clone(), read_only: true }).unwrap();
    let other = tree.filesystem();
    other.init(FsOptions::all()).unwrap();
    let e = walk(&*other, &["v", "file"]);
    assert_eq!((e.attr.st_uid, e.attr.st_gid), (OVERFLOW_ID, OVERFLOW_ID));
    drop(fs);
}

#[test]
fn chown_is_limited_to_the_guest_user_and_root() {
    let f = fixture();
    let fs = f.tree.filesystem();
    let p = walk(&*fs, &["projects", "p"]);
    let e = create(&*fs, p.inode, "f", 0o644, 0).unwrap();
    let c = ctx(&*fs);

    let mut attr: stat64 = unsafe { std::mem::zeroed() };
    for (uid, ok) in [(GUEST_UID, true), (0, true), (1234, false)] {
        attr.st_uid = uid;
        attr.st_gid = uid;
        let r = fs.setattr(&c, e.inode.into(), attr, None, SetattrValid::UID | SetattrValid::GID);
        assert_eq!(r.is_ok(), ok, "chown to {uid}");
        if let Err(err) = r {
            assert_eq!(errno(err), libc::EPERM);
        }
    }
}

#[test]
fn setid_bits_are_stripped() {
    let f = fixture();
    let fs = f.tree.filesystem();
    let p = walk(&*fs, &["projects", "p"]);
    let e = create(&*fs, p.inode, "suid", 0o4755, 0).unwrap();
    assert_eq!(std::fs::metadata(f.rw.join("suid")).unwrap().permissions().mode() & 0o7777, 0o755);

    let mut attr: stat64 = unsafe { std::mem::zeroed() };
    attr.st_mode = libc::S_IFREG | 0o6755;
    fs.setattr(&ctx(&*fs), e.inode.into(), attr, None, SetattrValid::MODE).unwrap();
    assert_eq!(std::fs::metadata(f.rw.join("suid")).unwrap().permissions().mode() & 0o7777, 0o755);

    fs.mkdir(&ctx(&*fs), p.inode.into(), &name("d"), 0o2775, 0).unwrap();
    assert_eq!(std::fs::metadata(f.rw.join("d")).unwrap().permissions().mode() & 0o7777, 0o775);
}

#[test]
fn fifos_and_devices_are_refused() {
    let f = fixture();
    let fs = f.tree.filesystem();
    let p = walk(&*fs, &["projects", "p"]);
    let c = ctx(&*fs);
    for mode in [libc::S_IFIFO, libc::S_IFCHR, libc::S_IFBLK] {
        let err = fs.mknod(&c, p.inode.into(), &name("n"), mode | 0o644, 0, 0).unwrap_err();
        assert_eq!(errno(err), libc::EPERM);
    }
    assert!(!f.rw.join("n").exists());
}

#[test]
fn dot_lookups_are_rejected() {
    let f = fixture();
    let fs = f.tree.filesystem();
    let p = walk(&*fs, &["projects", "p"]);
    let c = ctx(&*fs);
    for n in [".", ".."] {
        assert_eq!(errno(fs.lookup(&c, p.inode.into(), &name(n)).unwrap_err()), libc::ENOENT);
    }
}

#[test]
fn read_only_mounts_refuse_writes() {
    let f = fixture();
    let fs = f.tree.filesystem();
    let v = walk(&*fs, &["versions"]);
    let file = walk(&*fs, &["versions", "file"]);
    let c = ctx(&*fs);

    assert_eq!(errno(create(&*fs, v.inode, "new", 0o644, 0).unwrap_err()), libc::EROFS);
    assert_eq!(errno(fs.unlink(&c, v.inode.into(), &name("file")).unwrap_err()), libc::EROFS);
    assert_eq!(errno(fs.open(&c, file.inode.into(), libc::O_WRONLY as u32, 0).unwrap_err()), libc::EROFS);
    assert!(fs.open(&c, file.inode.into(), libc::O_RDONLY as u32, 0).is_ok());
}

#[test]
fn a_new_session_keeps_mounts_and_full_options() {
    let f = fixture();
    let fs = f.tree.filesystem();
    // The fixture's first session negotiated everything; a firmware-like
    // session negotiates nothing.
    fs.init(FsOptions::empty()).unwrap();
    f.tree.mount("/projects/q", MountSpec { source: f.rw.clone(), read_only: false }).unwrap();

    let opts = fs.init(FsOptions::all()).unwrap();
    assert!(opts.contains(FsOptions::MAX_PAGES), "{opts:?}");
    walk(&*fs, &["projects", "p"]);
    walk(&*fs, &["projects", "q"]);
    walk(&*fs, &["versions", "file"]);
}

#[test]
fn a_session_after_destroy_starts_fresh() {
    let f = fixture();
    let fs = f.tree.filesystem();
    // A guest reboot: the kernel unmounts (DESTROY) and mounts again (INIT).
    fs.destroy();
    let opts = fs.init(FsOptions::all()).unwrap();
    assert!(opts.contains(FsOptions::MAX_PAGES), "{opts:?}");
    walk(&*fs, &["projects", "p"]);
    walk(&*fs, &["versions", "file"]);
}

#[test]
fn unmounted_paths_disappear() {
    let f = fixture();
    let fs = f.tree.filesystem();
    f.tree.unmount("/projects/p").unwrap();
    let projects = walk(&*fs, &["projects"]);
    assert!(fs.lookup(&ctx(&*fs), projects.inode.into(), &name("p")).is_err());
    assert!(f.tree.unmount("/projects/p").is_err());
}

#[test]
fn symlinks_are_not_followed_on_the_host() {
    let f = fixture();
    let outside = tempfile::tempdir().unwrap();
    std::fs::write(outside.path().join("secret"), "s").unwrap();
    std::os::unix::fs::symlink(outside.path(), f.rw.join("out")).unwrap();
    std::os::unix::fs::symlink("../../..", f.rw.join("up")).unwrap();
    let fs = f.tree.filesystem();
    let c = ctx(&*fs);

    for link in ["out", "up"] {
        let e = walk(&*fs, &["projects", "p", link]);
        assert_eq!(e.attr.st_mode & libc::S_IFMT, libc::S_IFLNK, "{link}");
        // The guest gets the link text and resolves it in its own tree.
        assert!(fs.readlink(&c, e.inode.into()).is_ok());
        // Looking up a name under the link must not reach the host target.
        assert!(fs.lookup(&c, e.inode.into(), &name("secret")).is_err(), "{link}");
    }
}

#[test]
fn crafted_names_and_inodes_are_refused() {
    let f = fixture();
    std::fs::create_dir(f.rw.join("sub")).unwrap();
    let fs = f.tree.filesystem();
    let c = ctx(&*fs);
    let p = walk(&*fs, &["projects", "p"]);
    let sub = walk(&*fs, &["projects", "p", "sub"]);

    for n in ["../x", "a/b", "/etc", "..", "."] {
        assert!(fs.lookup(&c, sub.inode.into(), &name(n)).is_err(), "{n}");
        assert!(create(&*fs, p.inode, n, 0o644, 0).is_err(), "{n}");
    }
    for n in ["..", "."] {
        assert!(fs.lookup(&c, ROOT_ID.into(), &name(n)).is_err(), "{n}");
    }
    // Inode numbers the server never handed out.
    for ino in [u64::MAX, 0xdead_beef, p.inode + (1 << 40)] {
        assert!(fs.lookup(&c, ino.into(), &name("file")).is_err(), "{ino:#x}");
        assert!(fs.getattr(&c, ino.into(), None).is_err(), "{ino:#x}");
    }
    // Renames and links cannot move names out of their mount.
    let v = walk(&*fs, &["versions"]);
    let file = walk(&*fs, &["versions", "file"]);
    assert!(fs.link(&c, file.inode.into(), p.inode.into(), &name("stolen")).is_err());
    assert!(fs.rename(&c, p.inode.into(), &name("sub"), v.inode.into(), &name("moved"), 0).is_err());
    assert!(!f.rw.join("stolen").exists());
    assert!(!f.ro.join("moved").exists());
}
