//! Content hashes of image sources, used to tell whether an image is current.

use std::io::{self, Read};
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::Path;

use sha2::{Digest, Sha256};

fn hex(d: impl AsRef<[u8]>) -> String {
    d.as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

fn feed_file(h: &mut Sha256, path: &Path) -> io::Result<()> {
    let mut f = std::fs::File::open(path)?;
    let mut buf = vec![0u8; 64 * 1024];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            return Ok(());
        }
        h.update(&buf[..n]);
    }
}

pub fn hash_file(path: &Path) -> io::Result<String> {
    let mut h = Sha256::new();
    feed_file(&mut h, path).map_err(|e| io::Error::new(e.kind(), format!("{}: {e}", path.display())))?;
    Ok(hex(h.finalize()))
}

/// Entries the user cannot read (for example files a container left owned by
/// root) are hashed by their metadata instead of failing the build.
fn unreadable(h: &mut Sha256, meta: &std::fs::Metadata) {
    h.update(format!("unreadable {} {}.{}", meta.len(), meta.mtime(), meta.mtime_nsec()).as_bytes());
}

fn walk(h: &mut Sha256, root: &Path, rel: &Path) -> io::Result<()> {
    let dir = root.join(rel);
    let read = match std::fs::read_dir(&dir) {
        Err(e) if e.kind() == io::ErrorKind::PermissionDenied => {
            unreadable(h, &std::fs::symlink_metadata(&dir)?);
            return Ok(());
        }
        r => r?,
    };
    let mut entries: Vec<_> = read.collect::<Result<_, _>>()?;
    entries.sort_by_key(|e| e.file_name());
    for e in entries {
        let rel = rel.join(e.file_name());
        let meta = std::fs::symlink_metadata(root.join(&rel))?;
        let ft = meta.file_type();
        h.update(rel.as_os_str().as_encoded_bytes());
        h.update([0]);
        h.update(format!("{:o}", meta.permissions().mode()).as_bytes());
        h.update([0]);
        if ft.is_dir() {
            h.update(b"d");
            walk(h, root, &rel)?;
        } else if ft.is_symlink() {
            h.update(b"l");
            h.update(std::fs::read_link(root.join(&rel))?.as_os_str().as_encoded_bytes());
        } else if ft.is_file() {
            h.update(b"f");
            match feed_file(h, &root.join(&rel)) {
                Err(e) if e.kind() == io::ErrorKind::PermissionDenied => unreadable(h, &meta),
                r => r?,
            }
        }
        h.update([0]);
    }
    Ok(())
}

/// Hashes a directory tree: names, modes, file contents and symlink targets.
pub fn hash_tree(root: &Path) -> io::Result<String> {
    let mut h = Sha256::new();
    walk(&mut h, root, Path::new(""))
        .map_err(|e| io::Error::new(e.kind(), format!("{}: {e}", root.display())))?;
    Ok(hex(h.finalize()))
}

/// Combines several hashes or values into one.
pub fn combine<I: IntoIterator<Item = S>, S: AsRef<[u8]>>(parts: I) -> String {
    let mut h = Sha256::new();
    for p in parts {
        h.update(p.as_ref());
        h.update([0]);
    }
    hex(h.finalize())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tree_hash_follows_content_and_layout() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("t");
        std::fs::create_dir_all(root.join("sub")).unwrap();
        std::fs::write(root.join("a"), "1").unwrap();
        std::fs::write(root.join("sub/b"), "2").unwrap();
        let h1 = hash_tree(&root).unwrap();
        assert_eq!(hash_tree(&root).unwrap(), h1);

        std::fs::write(root.join("sub/b"), "3").unwrap();
        let h2 = hash_tree(&root).unwrap();
        assert_ne!(h1, h2);

        std::os::unix::fs::symlink("a", root.join("link")).unwrap();
        assert_ne!(hash_tree(&root).unwrap(), h2);
        assert_eq!(hash_file(&root.join("a")).unwrap().len(), 64);
        assert_ne!(combine(["a", "b"]), combine(["ab"]));
    }

    #[test]
    fn unreadable_entries_are_hashed_by_metadata() {
        if nix::unistd::geteuid().is_root() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("t");
        std::fs::create_dir_all(root.join("locked")).unwrap();
        std::fs::write(root.join("secret"), "x").unwrap();
        for p in ["locked", "secret"] {
            std::fs::set_permissions(root.join(p), std::fs::Permissions::from_mode(0o000)).unwrap();
        }
        let h = hash_tree(&root).unwrap();
        assert_eq!(hash_tree(&root).unwrap(), h);
        for p in ["locked", "secret"] {
            std::fs::set_permissions(root.join(p), std::fs::Permissions::from_mode(0o700)).unwrap();
        }
    }
}
