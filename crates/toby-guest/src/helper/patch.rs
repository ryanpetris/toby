//! `patch-file`: writes a tool's configuration file in the home, merging
//! into what is there (plan §16.1). Runs as the user.

use std::io::{self, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};

use toby_tools::{Format, Mode};

/// Expands `~/` with the home directory.
pub fn expand_home(path: &str, home: &Path) -> PathBuf {
    match path.strip_prefix("~/") {
        Some(rest) => home.join(rest),
        None => PathBuf::from(path),
    }
}

/// Merges or writes `content` into `path` atomically, keeping an existing
/// file's mode (new files are private).
pub fn patch_file(path: &Path, content: &str, format: Format, mode: Mode) -> io::Result<()> {
    // A linked file (a dotfiles checkout) is written where it lives.
    let resolved;
    let path = match std::fs::symlink_metadata(path) {
        Ok(m) if m.file_type().is_symlink() => {
            resolved = std::fs::canonicalize(path)?;
            resolved.as_path()
        }
        _ => path,
    };
    let existing = match std::fs::read_to_string(path) {
        Ok(s) => Some(s),
        Err(e) if e.kind() == io::ErrorKind::NotFound => None,
        Err(e) => return Err(e),
    };
    let new = toby_tools::patch(existing.as_deref(), content, format, mode)
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, format!("{}: {e}", path.display())))?;
    if existing.as_deref() == Some(new.as_str()) {
        return Ok(());
    }
    let dir = path.parent().ok_or_else(|| io::Error::other("no parent directory"))?;
    std::fs::create_dir_all(dir)?;
    let perm = std::fs::metadata(path).map(|m| m.permissions().mode() & 0o7777).unwrap_or(0o600);
    let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("file");
    let tmp = dir.join(format!(".{name}.toby-tmp.{}", std::process::id()));
    let _ = std::fs::remove_file(&tmp);
    {
        let mut f = std::fs::OpenOptions::new().write(true).create_new(true).mode(perm).open(&tmp)?;
        f.write_all(new.as_bytes())?;
        f.sync_all()?;
    }
    std::fs::rename(&tmp, path)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn patches_atomically_and_keeps_the_mode() {
        let dir = tempfile::tempdir().unwrap();
        let p = dir.path().join("sub/settings.json");
        patch_file(&p, r#"{"a":1}"#, Format::Json, Mode::Merge).unwrap();
        assert_eq!(std::fs::metadata(&p).unwrap().permissions().mode() & 0o777, 0o600);
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o644)).unwrap();
        patch_file(&p, r#"{"b":2}"#, Format::Json, Mode::Merge).unwrap();
        let v: serde_json::Value = serde_json::from_str(&std::fs::read_to_string(&p).unwrap()).unwrap();
        assert_eq!(v, serde_json::json!({"a":1,"b":2}));
        assert_eq!(std::fs::metadata(&p).unwrap().permissions().mode() & 0o777, 0o644);
        assert_eq!(expand_home("~/x/y", Path::new("/h")), PathBuf::from("/h/x/y"));
    }
}
