//! The header `toby-machine` sends first on a guest connection it forwards to
//! a host service (the models proxy, tobyd's capability endpoint), naming the
//! machine the connection comes from (plan §11.6).

use serde::{Deserialize, Serialize};

use crate::messages;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct FromMachine {
    pub machine_id: String,
}

messages! {
    pub enum ServiceHeader {
        1 => FromMachine(FromMachine),
    }
}
