//! A tree with more files than the default descriptor limit can be looked up
//! in full: every looked-up inode holds a descriptor (plan §10.5).

use std::ffi::CString;

use fuse_backend_rs::api::filesystem::{Context, FileSystem, FsOptions, ROOT_ID};
use nix::sys::resource::{Resource, getrlimit, setrlimit};
use toby_vfs::{MountSpec, Squash, Tree};

const FILES: usize = 3000;

#[test]
fn more_files_than_the_default_limit() {
    let (_, hard) = getrlimit(Resource::RLIMIT_NOFILE).unwrap();
    if hard < (FILES as u64) * 2 {
        eprintln!("skipped: the hard descriptor limit is {hard}");
        return;
    }
    // Start from the common default soft limit, as a service would.
    setrlimit(Resource::RLIMIT_NOFILE, 1024, hard).unwrap();
    assert!(toby_fs::raise_fd_limit().unwrap() >= FILES as u64);

    let dir = tempfile::tempdir().unwrap();
    for i in 0..FILES {
        std::fs::write(dir.path().join(format!("f{i}")), "").unwrap();
    }
    let uid = nix::unistd::getuid().as_raw();
    let gid = nix::unistd::getgid().as_raw();
    let tree = Tree::new(Squash { host_uid: uid, host_gid: gid, guest_uid: 1000, guest_gid: 1000 }).unwrap();
    tree.mount("/projects/p", MountSpec { source: dir.path().to_path_buf(), read_only: false }).unwrap();
    let fs = tree.filesystem();
    fs.init(FsOptions::all()).unwrap();

    let mut c = Context { uid: 0, gid: 0, pid: 1 };
    fs.id_remap(&mut c).unwrap();
    let lookup = |parent: u64, n: &str| fs.lookup(&c, parent.into(), &CString::new(n).unwrap()).unwrap();
    let projects = lookup(ROOT_ID, "projects");
    let p = lookup(projects.inode, "p");
    for i in 0..FILES {
        lookup(p.inode, &format!("f{i}"));
    }
}
