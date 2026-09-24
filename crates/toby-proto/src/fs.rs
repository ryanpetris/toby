//! File sharing control: requests to `toby-fs` on its control socket (plan
//! §10.2). The first request of a connection is [`Hello`]; requests are
//! answered in order.

use serde::{Deserialize, Serialize};

use crate::messages;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Hello {
    pub versions: Vec<u32>,
}

/// Serve `host_path` at `/projects/<id>`. Adding the same attachment again
/// succeeds; the same ID with a different path or mode is refused.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Add {
    pub id: String,
    pub host_path: String,
    pub read_only: bool,
}

/// Stop serving an attachment. Removing one that is not served succeeds.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Remove {
    pub id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct List {}

messages! {
    pub enum Request {
        1 => Hello(Hello),
        2 => Add(Add),
        3 => Remove(Remove),
        4 => List(List),
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Welcome {
    pub version: u32,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Done {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Failed {
    pub error: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Attachment {
    pub id: String,
    pub host_path: String,
    pub read_only: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Attachments {
    pub attachments: Vec<Attachment>,
}

messages! {
    pub enum Response {
        64 => Welcome(Welcome),
        65 => Done(Done),
        66 => Failed(Failed),
        67 => Attachments(Attachments),
    }
}

impl Response {
    pub fn failed(error: impl std::fmt::Display) -> Self {
        Response::Failed(Failed { error: error.to_string() })
    }
}
