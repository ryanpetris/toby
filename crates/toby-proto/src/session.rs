//! Session protocol between a client (the CLI) and `toby-session`, carried
//! end to end through `toby-machine` and the relay (plan §13.1).

use serde::{Deserialize, Serialize};

use crate::messages;
use crate::types::ExitStatus;

/// Attach request; the first frame a client sends.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Hello {
    pub versions: Vec<u32>,
    pub rows: u16,
    pub cols: u16,
    pub want_replay: bool,
    /// Resume output at this offset (bytes of output since the session
    /// started) instead of replaying the whole buffer; used when a client
    /// reconnects.
    #[serde(default)]
    pub resume_from: Option<u64>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Stdin {
    #[serde(with = "serde_bytes")]
    pub bytes: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CloseStdin {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Resize {
    pub rows: u16,
    pub cols: u16,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Signal {
    pub signal: i32,
}

messages! {
    /// Frames sent by the client.
    pub enum ClientFrame {
        1 => Hello(Hello),
        2 => Stdin(Stdin),
        3 => CloseStdin(CloseStdin),
        4 => Resize(Resize),
        5 => Signal(Signal),
    }
}

/// Session state reported when a client attaches.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum State {
    Running,
    Exited(ExitStatus),
}

/// The attach was accepted. Replay frames, if requested, follow.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Welcome {
    pub version: u32,
    pub state: State,
    /// Whether the session has a terminal.
    pub tty: bool,
    /// Output offset of the first byte this client will receive.
    #[serde(default)]
    pub offset: u64,
    /// Output bytes between the requested resume offset and `offset` that
    /// are no longer buffered.
    #[serde(default)]
    pub lost: u64,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Replay {
    #[serde(with = "serde_bytes")]
    pub bytes: Vec<u8>,
    /// Output the session wrote to standard error (sessions without a terminal).
    #[serde(default)]
    pub stderr: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Stdout {
    #[serde(with = "serde_bytes")]
    pub bytes: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Stderr {
    #[serde(with = "serde_bytes")]
    pub bytes: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Exit {
    pub status: ExitStatus,
}

/// Another client attached; this connection closes after the frame.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Detached {
    pub reason: String,
}

/// The attach was refused (for example, an unsupported version).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Refused {
    pub error: String,
}

messages! {
    /// Frames sent by the session.
    pub enum ServerFrame {
        64 => Welcome(Welcome),
        65 => Replay(Replay),
        66 => Stdout(Stdout),
        67 => Stderr(Stderr),
        68 => Exit(Exit),
        69 => Detached(Detached),
        70 => Refused(Refused),
    }
}
