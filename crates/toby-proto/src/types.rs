//! Types shared by several protocols.

use serde::{Deserialize, Serialize};

/// Version 1 of every Toby stream protocol.
pub const V1: u32 = 1;

/// Protocol versions this build speaks, newest last.
pub const SUPPORTED: &[u32] = &[V1];

/// Picks the newest version offered by the peer that this build supports.
pub fn negotiate(offered: &[u32]) -> Option<u32> {
    SUPPORTED.iter().rev().copied().find(|v| offered.contains(v))
}

/// Who a guest command runs as.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Identity {
    /// The home's user.
    User,
    Root,
}

/// Terminal size in character cells.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct TtySize {
    pub rows: u16,
    pub cols: u16,
}

/// A stream endpoint: a loopback TCP port or a Unix socket path.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Endpoint {
    Tcp { addr: String },
    Unix { path: String },
}

impl std::fmt::Display for Endpoint {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Endpoint::Tcp { addr } => write!(f, "tcp:{addr}"),
            Endpoint::Unix { path } => write!(f, "unix:{path}"),
        }
    }
}

impl std::str::FromStr for Endpoint {
    type Err = String;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        if let Some(addr) = s.strip_prefix("tcp:") {
            return Ok(Endpoint::Tcp {
                addr: addr.to_string(),
            });
        }
        if let Some(path) = s.strip_prefix("unix:") {
            return Ok(Endpoint::Unix {
                path: path.to_string(),
            });
        }
        Err(format!("endpoint must start with tcp: or unix: ({s})"))
    }
}

/// Everything needed to start a session.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SpawnSpec {
    pub session_id: String,
    pub argv: Vec<String>,
    #[serde(default)]
    pub env: Vec<(String, String)>,
    #[serde(default)]
    pub cwd: Option<String>,
    pub identity: Identity,
    #[serde(default)]
    pub tty: Option<TtySize>,
    #[serde(default)]
    pub keep_after_exit: bool,
}

/// How a session's process ended.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ExitStatus {
    Code(i32),
    Signal(i32),
}

impl ExitStatus {
    /// The shell-style exit code: the code, or 128 + signal.
    pub fn code(self) -> i32 {
        match self {
            ExitStatus::Code(c) => c,
            ExitStatus::Signal(s) => 128 + s,
        }
    }
}

/// A live or exited session as reported by the relay.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionInfo {
    pub id: String,
    pub argv0: String,
    pub attached: bool,
    /// Unix time in seconds.
    pub started: u64,
    #[serde(default)]
    pub exit: Option<ExitStatus>,
}
