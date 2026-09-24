//! `build`, `provision` and `format-home`: builder machine jobs, written as
//! shell scripts because they drive distribution tools.

use std::io;
use std::os::unix::fs::PermissionsExt;
use std::os::unix::process::CommandExt;
use std::path::Path;

/// The job scripts, by name.
pub const SCRIPTS: &[(&str, &str)] = &[
    ("adapt.sh", include_str!("../../assets/adapt.sh")),
    ("build.sh", include_str!("../../assets/build.sh")),
    ("provision.sh", include_str!("../../assets/provision.sh")),
    ("format-home.sh", include_str!("../../assets/format-home.sh")),
];

/// Writes the job scripts to `dir` and replaces this process with `script`.
pub fn exec_script(dir: &Path, script: &str, args: &[String]) -> io::Result<()> {
    std::fs::create_dir_all(dir)?;
    std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))?;
    for (name, body) in SCRIPTS {
        std::fs::write(dir.join(name), body)?;
    }
    Err(std::process::Command::new("/bin/sh").arg(dir.join(script)).args(args).exec())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scripts_are_valid_shell() {
        for (name, body) in SCRIPTS {
            let status = std::process::Command::new("sh").args(["-n", "-c", body]).status().unwrap();
            assert!(status.success(), "{name}");
        }
    }
}
