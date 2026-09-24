//! The sandbox capability (plan §16.3): `toby-connect` in the guest asks
//! for a target on `/run/toby/sandbox.sock`; `toby internal machine` passes
//! the request to tobyd, which either serves the connection itself or names
//! a guest endpoint in another machine that the two machines' host processes
//! splice to, keeping tobyd out of the data path.

use serde::{Deserialize, Serialize};

use crate::messages;
use crate::types::Endpoint;

/// The first frame `toby-connect` sends.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Connect {
    /// What to connect to, such as `mcp/github` or `mcp/toby`.
    pub target: String,
}

messages! {
    pub enum CapRequest {
        1 => Connect(Connect),
    }
}

/// tobyd serves the connection itself; the stream continues with it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Serve {}

/// Connect to `target` inside machine `machine` and splice.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Splice {
    pub machine: String,
    pub target: Endpoint,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Refused {
    pub error: String,
}

messages! {
    pub enum CapResponse {
        64 => Serve(Serve),
        65 => Splice(Splice),
        66 => Refused(Refused),
    }
}
