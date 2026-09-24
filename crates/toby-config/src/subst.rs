//! Substitutions in configuration values (plan §14.2): `{file:path}` is the
//! trimmed content of a file (relative to the configuration directory, `~`
//! allowed) and `{env:NAME}` an environment variable. They are resolved when
//! used and never written anywhere.

use std::io;
use std::path::Path;

/// Resolves every substitution in `value`.
pub fn resolve(value: &str, config_dir: &Path, home: &Path) -> io::Result<String> {
    let mut out = String::with_capacity(value.len());
    let mut rest = value;
    while let Some(start) = rest.find('{') {
        let tail = &rest[start..];
        let reference = ["{file:", "{env:"].iter().find(|p| tail.starts_with(**p));
        let (Some(prefix), Some(end)) = (reference, tail.find('}')) else {
            out.push_str(&rest[..=start]);
            rest = &rest[start + 1..];
            continue;
        };
        out.push_str(&rest[..start]);
        let arg = &tail[prefix.len()..end];
        let resolved = if *prefix == "{file:" {
            let path =
                if arg.starts_with('~') { crate::paths::expand(home, arg) } else { config_dir.join(arg) };
            std::fs::read_to_string(&path)
                .map_err(|e| io::Error::new(e.kind(), format!("{{file:{arg}}}: {e}")))?
                .trim()
                .to_string()
        } else {
            std::env::var(arg).map_err(|_| {
                io::Error::new(io::ErrorKind::NotFound, format!("{{env:{arg}}}: the variable is not set"))
            })?
        };
        out.push_str(&resolved);
        rest = &tail[end + 1..];
    }
    out.push_str(rest);
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn files_and_variables_are_substituted() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join("keys")).unwrap();
        std::fs::write(dir.path().join("keys/a"), "secret\n").unwrap();
        std::fs::write(dir.path().join("home-key"), "h").unwrap();
        let home = dir.path();
        assert_eq!(resolve("Bearer {file:keys/a}", dir.path(), home).unwrap(), "Bearer secret");
        assert_eq!(resolve("{file:~/home-key}", dir.path(), home).unwrap(), "h");
        // SAFETY: no other test reads this variable.
        unsafe { std::env::set_var("TOBY_SUBST_TEST", "v") };
        assert_eq!(resolve("x{env:TOBY_SUBST_TEST}y", dir.path(), home).unwrap(), "xvy");
        assert_eq!(resolve("{json} {other:x}", dir.path(), home).unwrap(), "{json} {other:x}");
        assert!(resolve("{file:missing}", dir.path(), home).is_err());
        assert!(resolve("{env:TOBY_SUBST_UNSET}", dir.path(), home).is_err());
    }
}
