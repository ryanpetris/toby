//! `user-setup`: makes the home's user exist in the root by editing the
//! account files directly, so images need no `useradd`.

use std::io;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::Path;

use crate::record::{self, UserInfo};

/// Options of `user-setup`.
#[derive(Debug, Clone)]
pub struct UserSetup {
    pub name: String,
    pub uid: u32,
    /// Preferred login shell; used when the image has it.
    pub shell: Option<String>,
    pub sudo: bool,
}

pub fn valid_name(n: &str) -> bool {
    let b = n.as_bytes();
    !b.is_empty()
        && b.len() <= 32
        && (b[0].is_ascii_lowercase() || b[0] == b'_')
        && b.iter().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, b'_' | b'-'))
}

/// Picks the first shell that exists in the root.
pub fn pick_shell(root: &Path, preferred: Option<&str>) -> String {
    let exists = |s: &str| root.join(s.trim_start_matches('/')).exists();
    preferred.into_iter().chain(["/bin/bash", "/bin/sh"]).find(|s| exists(s)).unwrap_or("/bin/sh").to_string()
}

/// Puts `line` for `name` first and drops other lines for the same name, so
/// Toby's entry wins lookups by name and by ID while image entries that
/// share the ID stay in place.
fn upsert(content: &str, name: &str, line: &str) -> String {
    let mut out = String::with_capacity(content.len() + line.len() + 1);
    out.push_str(line);
    out.push('\n');
    for l in content.lines() {
        if l.split(':').next() == Some(name) {
            continue;
        }
        out.push_str(l);
        out.push('\n');
    }
    out
}

pub fn passwd(content: &str, u: &UserSetup, home: &str, shell: &str) -> String {
    let line = format!("{}:x:{}:{}:Toby user:{}:{}", u.name, u.uid, u.uid, home, shell);
    upsert(content, &u.name, &line)
}

pub fn group(content: &str, u: &UserSetup) -> String {
    upsert(content, &u.name, &format!("{}:x:{}:", u.name, u.uid))
}

/// Shadow entry with a locked password; access is through Toby and sudo.
pub fn shadow(content: &str, u: &UserSetup) -> String {
    match content.lines().find(|l| l.split(':').next() == Some(u.name.as_str())) {
        Some(existing) => upsert(content, &u.name, existing),
        None => upsert(content, &u.name, &format!("{}:!:19000:0:99999:7:::", u.name)),
    }
}

fn replace_file(path: &Path, content: &str) -> io::Result<()> {
    let meta = std::fs::metadata(path).ok();
    let tmp = path.with_extension("toby-tmp");
    std::fs::write(&tmp, content)?;
    if let Some(m) = &meta {
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(m.mode() & 0o7777))?;
        let _ = std::os::unix::fs::chown(&tmp, Some(m.uid()), Some(m.gid()));
    }
    std::fs::rename(&tmp, path)
}

fn edit(path: &Path, f: impl FnOnce(&str) -> String) -> io::Result<()> {
    let old = std::fs::read_to_string(path).unwrap_or_default();
    let new = f(&old);
    if new != old {
        replace_file(path, &new)?;
    }
    Ok(())
}

/// Applies the user to the root at `root` (normally `/`) and records it at
/// `user_file` for sessions.
pub fn user_setup(u: &UserSetup, root: &Path, user_file: &Path) -> io::Result<UserInfo> {
    if !valid_name(&u.name) {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, format!("invalid user name {:?}", u.name)));
    }
    if u.uid == 0 {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "the home user cannot be root"));
    }
    let home = format!("/home/{}", u.name);
    let shell = pick_shell(root, u.shell.as_deref());
    let etc = root.join("etc");

    edit(&etc.join("passwd"), |c| passwd(c, u, &home, &shell))?;
    edit(&etc.join("group"), |c| group(c, u))?;
    if etc.join("shadow").exists() {
        edit(&etc.join("shadow"), |c| shadow(c, u))?;
    }

    let sudoers = etc.join("sudoers.d");
    let dropin = sudoers.join("toby");
    if u.sudo {
        if !sudoers.is_dir() {
            std::fs::create_dir_all(&sudoers)?;
            std::fs::set_permissions(&sudoers, std::fs::Permissions::from_mode(0o750))?;
        }
        std::fs::write(&dropin, format!("{} ALL=(ALL) NOPASSWD: ALL\n", u.name))?;
        std::fs::set_permissions(&dropin, std::fs::Permissions::from_mode(0o440))?;
    } else if !u.sudo {
        let _ = std::fs::remove_file(&dropin);
    }

    let info = UserInfo { name: u.name.clone(), uid: u.uid, gid: u.uid, home, shell };
    if let Some(p) = user_file.parent() {
        std::fs::create_dir_all(p)?;
    }
    record::write(user_file, &info)?;
    Ok(info)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn u() -> UserSetup {
        UserSetup { name: "dev".into(), uid: 1000, shell: Some("/usr/bin/zsh".into()), sudo: true }
    }

    const PASSWD: &str = "root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000::/home/ubuntu:/bin/sh\ndev:x:1001:1001::/home/dev:/bin/sh\n";

    #[test]
    fn toby_entry_comes_first_and_replaces_same_name() {
        let out = passwd(PASSWD, &u(), "/home/dev", "/bin/bash");
        let lines: Vec<&str> = out.lines().collect();
        assert_eq!(lines[0], "dev:x:1000:1000:Toby user:/home/dev:/bin/bash");
        assert!(lines.contains(&"ubuntu:x:1000:1000::/home/ubuntu:/bin/sh"));
        assert_eq!(out.matches("\ndev:").count() + usize::from(out.starts_with("dev:")), 1);
        // Idempotent.
        assert_eq!(passwd(&out, &u(), "/home/dev", "/bin/bash"), out);
    }

    #[test]
    fn shadow_keeps_an_existing_entry() {
        let s = shadow("root:*:1:0:99999:7:::\ndev:$6$x:1:0:99999:7:::\n", &u());
        assert!(s.starts_with("dev:$6$x:"));
        let s = shadow("root:*:1:0:99999:7:::\n", &u());
        assert!(s.starts_with("dev:!:"));
    }

    #[test]
    fn names() {
        assert!(valid_name("dev"));
        assert!(valid_name("a_b-1"));
        assert!(!valid_name("Dev"));
        assert!(!valid_name("1dev"));
        assert!(!valid_name("a:b"));
        assert!(!valid_name(""));
    }

    #[test]
    fn applies_to_a_root_tree() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path();
        std::fs::create_dir_all(root.join("etc/sudoers.d")).unwrap();
        std::fs::create_dir_all(root.join("bin")).unwrap();
        std::fs::write(root.join("bin/bash"), "").unwrap();
        std::fs::write(root.join("etc/passwd"), PASSWD).unwrap();
        std::fs::write(root.join("etc/group"), "root:x:0:\n").unwrap();
        std::fs::write(root.join("etc/shadow"), "root:*:1:0:99999:7:::\n").unwrap();

        let info = user_setup(&u(), root, &root.join("run/toby/user")).unwrap();
        assert_eq!(info.shell, "/bin/bash");
        assert!(std::fs::read_to_string(root.join("etc/group")).unwrap().starts_with("dev:x:1000:\n"));
        assert_eq!(
            std::fs::read_to_string(root.join("etc/sudoers.d/toby")).unwrap(),
            "dev ALL=(ALL) NOPASSWD: ALL\n"
        );
        let rec: UserInfo = record::read(&root.join("run/toby/user")).unwrap();
        assert_eq!(rec, info);

        let no_sudo = UserSetup { sudo: false, ..u() };
        user_setup(&no_sudo, root, &root.join("run/toby/user")).unwrap();
        assert!(!root.join("etc/sudoers.d/toby").exists());
    }
}
