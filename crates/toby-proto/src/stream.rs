//! Stream headers: the first frame of every vsock connection (plan §11.3).
//!
//! After the header is acknowledged the connection carries raw bytes or a
//! protocol owned by its two endpoints; the processes in the middle only splice.

use serde::{Deserialize, Serialize};

use crate::messages;
use crate::types::Endpoint;

/// Relay control channel; relay control frames follow.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Control {
    pub proto_versions: Vec<u32>,
}

/// Bridge to a session's socket; the session protocol follows.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionAttach {
    pub session_id: String,
}

/// Connect to an endpoint inside the guest and splice.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Dial {
    pub target: Endpoint,
}

messages! {
    /// Headers of host-initiated connections to the relay.
    pub enum HostHeader {
        1 => Control(Control),
        2 => SessionAttach(SessionAttach),
        3 => Dial(Dial),
    }
}

/// Sent by the relay when it starts; `toby-machine` answers with [`Reply`].
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RelayHello {
    /// Toby version of the relay binary.
    pub version: String,
    pub proto_versions: Vec<u32>,
    /// Guest boot ID (`/proc/sys/kernel/random/boot_id`).
    pub boot_id: String,
}

/// A guest listener accepted a connection; `toby-machine` connects it to the
/// host target registered for that listener.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Accepted {
    pub listener_id: String,
}

messages! {
    /// Headers of guest-initiated connections to `toby-machine`.
    pub enum GuestHeader {
        16 => RelayHello(RelayHello),
        17 => Accepted(Accepted),
    }
}

/// The header was accepted.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Ok {
    /// Negotiated protocol version, when the header negotiates one.
    #[serde(default)]
    pub version: Option<u32>,
}

/// The header was refused; the connection closes after this frame.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Refused {
    pub error: String,
}

messages! {
    /// Answer to a stream header.
    pub enum Reply {
        112 => Ok(Ok),
        113 => Refused(Refused),
    }
}

impl Reply {
    pub fn ok() -> Self {
        Reply::Ok(Ok { version: None })
    }

    pub fn version(version: u32) -> Self {
        Reply::Ok(Ok {
            version: Some(version),
        })
    }

    pub fn refused(error: impl Into<String>) -> Self {
        Reply::Refused(Refused { error: error.into() })
    }

    /// Converts a refusal into an error.
    pub fn into_result(self) -> Result<Option<u32>, String> {
        match self {
            Reply::Ok(ok) => std::result::Result::Ok(ok.version),
            Reply::Refused(r) => Err(r.error),
        }
    }
}
