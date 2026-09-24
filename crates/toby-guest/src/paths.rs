//! Guest filesystem locations used by Toby (plan §9.5).

use std::path::{Path, PathBuf};

/// Guest paths rooted at `/run/toby`; tests use a temporary root.
#[derive(Debug, Clone)]
pub struct GuestPaths {
    root: PathBuf,
}

impl Default for GuestPaths {
    fn default() -> Self {
        GuestPaths {
            root: PathBuf::from("/run/toby"),
        }
    }
}

impl GuestPaths {
    pub fn at(root: impl Into<PathBuf>) -> Self {
        GuestPaths { root: root.into() }
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    /// The virtio-fs mount.
    pub fn fs(&self) -> PathBuf {
        self.root.join("fs")
    }

    /// The Toby binary of a runtime version.
    pub fn runtime_binary(&self, version: &str) -> PathBuf {
        self.fs().join("versions").join(version).join("toby")
    }

    pub fn sessions(&self) -> PathBuf {
        self.root.join("sessions")
    }

    pub fn session_dir(&self, id: &str) -> PathBuf {
        self.sessions().join(id)
    }

    /// The home's user, written by the `user-setup` helper.
    pub fn user_file(&self) -> PathBuf {
        self.root.join("user")
    }
}

/// Files inside one session directory.
pub mod session_files {
    pub const SOCKET: &str = "sock";
    pub const SPEC: &str = "spec";
    pub const RECORD: &str = "record";
    pub const EXIT: &str = "exit";
}

/// Whether `id` is safe to use as a single path component.
pub fn valid_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 64
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

/// Whether `v` is a plausible Toby version usable as a path component.
pub fn valid_version(v: &str) -> bool {
    !v.is_empty()
        && v.len() <= 64
        && !v.starts_with('.')
        && v.bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b'+' | b'_'))
}

impl GuestPaths {
    /// Paths rooted at `$TOBY_GUEST_ROOT`, or `/run/toby`.
    pub fn from_env() -> Self {
        std::env::var_os("TOBY_GUEST_ROOT")
            .map(GuestPaths::at)
            .unwrap_or_default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_cannot_escape_the_sessions_directory() {
        assert!(valid_id("01J8Z3K5T6Y7"));
        assert!(!valid_id(""));
        assert!(!valid_id(".."));
        assert!(!valid_id("a/b"));
        assert!(!valid_id(&"x".repeat(65)));
        assert!(valid_version("0.17.0"));
        assert!(!valid_version(".."));
        assert!(!valid_version("0.17/../x"));
    }
}
