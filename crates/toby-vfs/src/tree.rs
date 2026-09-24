//! The file tree served to a machine: a synthetic read-only root with
//! passthrough mounts added and removed at runtime (plan §10.1).

use std::collections::BTreeMap;
use std::io;
use std::path::PathBuf;
use std::sync::{Arc, Mutex, RwLock};

use fuse_backend_rs::api::{BackFileSystem, Vfs, VfsOptions};
use fuse_backend_rs::passthrough::{Config, PassthroughFs};

use crate::guard::{Guard, Squash};

/// One passthrough mount.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MountSpec {
    pub source: PathBuf,
    pub read_only: bool,
}

type Table = BTreeMap<String, MountSpec>;

/// The mount table and the file system that serves it.
pub struct Tree {
    table: Arc<Mutex<Table>>,
    top: Arc<Guard<Vfs>>,
}

fn vfs_error(e: fuse_backend_rs::api::vfs::VfsError) -> io::Error {
    io::Error::other(format!("{e:?}"))
}

fn backend(spec: &MountSpec) -> io::Result<BackFileSystem> {
    // The passthrough opens its root without following symlinks; mount
    // sources come from host configuration, so resolve them first.
    let source = std::fs::canonicalize(&spec.source)
        .map_err(|e| io::Error::new(e.kind(), format!("{}: {e}", spec.source.display())))?;
    if !source.is_dir() {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            format!("{} is not a directory", source.display()),
        ));
    }
    let root_dir = source
        .to_str()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "path is not UTF-8"))?
        .to_string();
    let cfg = Config {
        root_dir,
        do_import: true,
        writeback: !spec.read_only,
        no_open: false,
        no_opendir: false,
        xattr: false,
        inode_file_handles: false,
        ..Default::default()
    };
    let fs = PassthroughFs::<()>::new(cfg)?;
    fs.import()?;
    Ok(if spec.read_only { Box::new(Guard::read_only(fs)) } else { Box::new(fs) })
}

fn options() -> VfsOptions {
    VfsOptions { no_open: false, no_opendir: false, ..VfsOptions::default() }
}

/// Where attachments are mounted; everything else is the runtime tree.
const ATTACHMENTS: &str = "/projects/";

/// Builds the file system for `table`. An attachment whose source can no
/// longer be opened (deleted or renamed) is dropped from the table rather
/// than failing the whole session; the runtime tree is required.
fn build(table: &mut Table) -> io::Result<Vfs> {
    let mut vfs = Vfs::new(options());
    vfs.set_remove_pseudo_root();
    let mut gone = Vec::new();
    for (path, spec) in table.iter() {
        match backend(spec) {
            Ok(fs) => {
                vfs.mount(fs, path).map_err(vfs_error)?;
            }
            Err(e) if !path.starts_with(ATTACHMENTS) => return Err(e),
            Err(e) => {
                eprintln!("dropping {path}: {e}");
                gone.push(path.clone());
            }
        }
    }
    for path in gone {
        table.remove(&path);
    }
    Ok(vfs)
}

fn check_path(path: &str) -> io::Result<()> {
    let ok = path.starts_with('/')
        && path.len() > 1
        && path[1..].split('/').all(|c| !c.is_empty() && c != "." && c != "..");
    if ok {
        Ok(())
    } else {
        Err(io::Error::new(io::ErrorKind::InvalidInput, format!("invalid mount path {path}")))
    }
}

impl Tree {
    pub fn new(squash: Squash) -> io::Result<Tree> {
        let table: Arc<Mutex<Table>> = Arc::default();
        let vfs = build(&mut Table::new())?;

        let for_rebuild = table.clone();
        let rebuild = Box::new(move |slot: &RwLock<Arc<Vfs>>| {
            let mut table = for_rebuild.lock().unwrap();
            let vfs = build(&mut table)?;
            *slot.write().unwrap() = Arc::new(vfs);
            Ok(())
        });
        let top = Arc::new(Guard::top(vfs, squash, rebuild));
        Ok(Tree { table, top })
    }

    /// The file system to serve.
    pub fn filesystem(&self) -> Arc<Guard<Vfs>> {
        self.top.clone()
    }

    /// Mounts `spec` at `path` (for example `/versions` or `/projects/a1`).
    pub fn mount(&self, path: &str, spec: MountSpec) -> io::Result<()> {
        check_path(path)?;
        let mut table = self.table.lock().unwrap();
        if table.contains_key(path) {
            return Err(io::Error::new(io::ErrorKind::AlreadyExists, format!("{path} is already mounted")));
        }
        self.top.current().mount(backend(&spec)?, path).map_err(vfs_error)?;
        table.insert(path.to_string(), spec);
        Ok(())
    }

    pub fn unmount(&self, path: &str) -> io::Result<()> {
        let mut table = self.table.lock().unwrap();
        if table.remove(path).is_none() {
            return Err(io::Error::new(io::ErrorKind::NotFound, format!("{path} is not mounted")));
        }
        self.top.current().umount(path).map_err(vfs_error)?;
        Ok(())
    }

    pub fn mounts(&self) -> Vec<(String, MountSpec)> {
        self.table.lock().unwrap().iter().map(|(k, v)| (k.clone(), v.clone())).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mount_paths_are_validated() {
        assert!(check_path("/versions").is_ok());
        assert!(check_path("/projects/a1").is_ok());
        assert!(check_path("versions").is_err());
        assert!(check_path("/").is_err());
        assert!(check_path("/projects/../x").is_err());
        assert!(check_path("/projects//x").is_err());
    }
}
