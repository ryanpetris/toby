//! Relay control channel: requests from `toby-machine` to `toby-relay` and
//! their responses (plan §11.4). Requests are answered in order.

use serde::{Deserialize, Serialize};

use crate::messages;
use crate::types::{Endpoint, SessionInfo, SpawnSpec};

/// Start a session.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Spawn {
    pub spec: SpawnSpec,
    /// Runtime version whose binary runs the session; the relay's own when absent.
    #[serde(default)]
    pub version: Option<String>,
}

/// Bind a listener in the guest; accepted connections arrive at `toby-machine`
/// as [`crate::stream::Accepted`] with the same ID.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Listen {
    pub listener_id: String,
    pub bind: Endpoint,
    /// File mode for Unix sockets.
    #[serde(default)]
    pub mode: Option<u32>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Unlisten {
    pub listener_id: String,
}

/// List live sessions and kept exit records.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Sessions {}

/// Send a signal to a session's process group.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Kill {
    pub session_id: String,
    pub signal: i32,
}

/// Remove a session's exit record after its exit has been collected.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Forget {
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Ping {}

/// Asks for the relay's version and the guest boot ID.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Hello {}

messages! {
    pub enum Request {
        1 => Spawn(Spawn),
        2 => Listen(Listen),
        3 => Unlisten(Unlisten),
        4 => Sessions(Sessions),
        5 => Kill(Kill),
        6 => Forget(Forget),
        7 => Ping(Ping),
        8 => Hello(Hello),
    }
}

/// The request succeeded and has no result.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Done {}

/// A session was started and its socket exists.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Spawned {
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionList {
    pub sessions: Vec<SessionInfo>,
}

/// The relay's identity.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RelayInfo {
    pub version: String,
    pub boot_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Failed {
    pub error: String,
}

messages! {
    pub enum Response {
        64 => Done(Done),
        65 => Spawned(Spawned),
        66 => SessionList(SessionList),
        67 => Failed(Failed),
        68 => RelayInfo(RelayInfo),
    }
}

impl Response {
    pub fn failed(error: impl std::fmt::Display) -> Self {
        Response::Failed(Failed { error: error.to_string() })
    }
}
