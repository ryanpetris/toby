//! Small CBOR files shared between guest processes.

use std::io;
use std::os::unix::fs::OpenOptionsExt;
use std::path::Path;

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use toby_proto::types::SessionInfo;

/// State a session publishes for the relay.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionRecord {
    pub info: SessionInfo,
    /// PID of the `toby guest session` process.
    pub session_pid: i32,
    /// Process group of the session's child.
    pub child_pgid: i32,
}

/// The home's user as configured by the `user-setup` helper.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct UserInfo {
    pub name: String,
    pub uid: u32,
    pub gid: u32,
    pub home: String,
    pub shell: String,
}

/// Writes `value` to `path` atomically with mode 0600.
pub fn write<T: Serialize>(path: &Path, value: &T) -> io::Result<()> {
    let tmp = path.with_extension("tmp");
    {
        let mut f =
            std::fs::OpenOptions::new().write(true).create(true).truncate(true).mode(0o600).open(&tmp)?;
        ciborium::into_writer(value, &mut f).map_err(io::Error::other)?;
        f.sync_all()?;
    }
    std::fs::rename(&tmp, path)
}

pub fn read<T: DeserializeOwned>(path: &Path) -> io::Result<T> {
    let f = std::fs::File::open(path)?;
    ciborium::from_reader(f).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e.to_string()))
}
