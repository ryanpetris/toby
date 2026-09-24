//! Content hashes of image sources, used to tell whether an image is current.

use std::io::{self, Read};
use std::os::unix::fs::PermissionsExt;
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
    feed_file(&mut h, path)?;
    Ok(hex(h.finalize()))
}

fn walk(h: &mut Sha256, root: &Path, rel: &Path) -> io::Result<()> {
    let dir = root.join(rel);
    let mut entries: Vec<_> = std::fs::read_dir(&dir)?.collect::<Result<_, _>>()?;
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
            feed_file(h, &root.join(&rel))?;
        }
        h.update([0]);
    }
    Ok(())
}

/// Hashes a directory tree: names, modes, file contents and symlink targets.
pub fn hash_tree(root: &Path) -> io::Result<String> {
    let mut h = Sha256::new();
    walk(&mut h, root, Path::new(""))?;
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
}
