//! Machine control: requests to `toby-machine` on its control socket. The first
//! request of a connection is [`Hello`]; requests are answered in order.

use serde::{Deserialize, Serialize};

use crate::messages;
use crate::types::{SessionInfo, SpawnSpec};

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Hello {
    pub versions: Vec<u32>,
}

/// Start a session in the guest.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Spawn {
    pub spec: SpawnSpec,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Sessions {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Kill {
    pub session_id: String,
    pub signal: i32,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Status {}

/// Power the machine off.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Stop {}

messages! {
    pub enum Request {
        1 => Hello(Hello),
        2 => Spawn(Spawn),
        3 => Sessions(Sessions),
        4 => Kill(Kill),
        5 => Status(Status),
        6 => Stop(Stop),
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Welcome {
    pub version: u32,
    pub machine_id: String,
    /// Toby version of the `toby-machine` process.
    pub toby_version: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Spawned {
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionList {
    pub sessions: Vec<SessionInfo>,
}

/// Observed machine state.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum MachineState {
    Starting,
    Ready,
    Stopping,
    Failed,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct MachineStatus {
    pub state: MachineState,
    /// Toby version of the connected relay, if any.
    #[serde(default)]
    pub relay_version: Option<String>,
    #[serde(default)]
    pub boot_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Done {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Failed {
    pub error: String,
}

messages! {
    pub enum Response {
        64 => Welcome(Welcome),
        65 => Spawned(Spawned),
        66 => SessionList(SessionList),
        67 => MachineStatus(MachineStatus),
        68 => Done(Done),
        69 => Failed(Failed),
    }
}

impl Response {
    pub fn failed(error: impl std::fmt::Display) -> Self {
        Response::Failed(Failed {
            error: error.to_string(),
        })
    }
}
