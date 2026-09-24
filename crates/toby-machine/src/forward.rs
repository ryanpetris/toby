//! Forwards and capabilities (plan §11.5, §11.6): host listeners that dial
//! into the guest, and guest listeners whose connections arrive here and go
//! to a fixed host target.

use std::collections::HashMap;
use std::io;
use std::path::PathBuf;
use std::sync::Mutex;

use toby_config::machine::{Direction, Forward, ForwardState, ForwardStatus};
use toby_proto::frame;
use toby_proto::service::{FromMachine, ServiceHeader};
use toby_proto::stream::{Dial, HostHeader};
use toby_proto::types::Endpoint;
use tokio::net::{TcpListener, TcpStream, UnixStream};
use tokio::task::AbortHandle;

use crate::link::open_relay;

/// Where a guest listener's connections go.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Target {
    /// A host TCP address.
    Tcp(String),
    /// A host service's Unix socket, told which machine connects.
    Service(PathBuf),
}

/// A listener in the guest, registered with the relay.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GuestListener {
    pub id: String,
    pub bind: Endpoint,
    pub mode: Option<u32>,
    pub target: Target,
}

/// Listener IDs of capabilities; forward IDs never start with `cap-`.
pub const MODELS: &str = "cap-models";
pub const SANDBOX: &str = "cap-sandbox";

pub fn tcp(addr: &str) -> Endpoint {
    Endpoint::Tcp { addr: addr.to_string() }
}

#[derive(Default)]
pub struct Forwards {
    /// Guest listeners the relay has, by ID.
    pub guest: Mutex<HashMap<String, GuestListener>>,
    /// Host listeners of host-to-guest forwards, by forward ID.
    host: Mutex<HashMap<String, (Forward, AbortHandle)>>,
}

impl Forwards {
    pub fn target(&self, listener_id: &str) -> Option<Target> {
        self.guest.lock().unwrap().get(listener_id).map(|l| l.target.clone())
    }

    /// Makes the host listeners match `wanted` (host-to-guest forwards).
    /// Returns the forwards that could not listen, with the error.
    pub async fn reconcile_host(&self, vsock: PathBuf, wanted: &[Forward]) -> HashMap<String, String> {
        let wanted: Vec<&Forward> = wanted.iter().filter(|f| f.direction == Direction::HostToGuest).collect();
        let mut errors = HashMap::new();
        {
            let mut host = self.host.lock().unwrap();
            host.retain(|id, (f, handle)| {
                let keep = wanted.iter().any(|w| &w.id == id && w.host == f.host && w.guest == f.guest);
                if !keep {
                    handle.abort();
                }
                keep
            });
        }
        for f in wanted {
            if self.host.lock().unwrap().contains_key(&f.id) {
                continue;
            }
            match TcpListener::bind(f.host.as_str()).await {
                Ok(listener) => {
                    let target = tcp(&f.guest);
                    let vsock = vsock.clone();
                    let handle = tokio::spawn(async move {
                        while let Ok((conn, _)) = listener.accept().await {
                            let (vsock, target) = (vsock.clone(), target.clone());
                            tokio::spawn(async move {
                                let _ = dial(&vsock, target, conn).await;
                            });
                        }
                    })
                    .abort_handle();
                    self.host.lock().unwrap().insert(f.id.clone(), (f.clone(), handle));
                }
                Err(e) => {
                    errors.insert(f.id.clone(), format!("listening on {}: {e}", f.host));
                }
            }
        }
        errors
    }

    /// Stops every host listener.
    pub fn close(&self) {
        for (_, (_, handle)) in self.host.lock().unwrap().drain() {
            handle.abort();
        }
    }
}

/// Carries one host connection into the guest.
async fn dial(vsock: &std::path::Path, target: Endpoint, mut conn: TcpStream) -> io::Result<()> {
    let (mut guest, reply) = open_relay(vsock, &HostHeader::Dial(Dial { target })).await?;
    reply.into_result().map_err(io::Error::other)?;
    tokio::io::copy_bidirectional(&mut conn, &mut guest).await?;
    Ok(())
}

/// Connects a guest connection to its host target.
pub async fn connect_target(target: &Target, machine_id: &str) -> io::Result<Box<dyn Splice>> {
    Ok(match target {
        Target::Tcp(addr) => Box::new(TcpStream::connect(addr.as_str()).await?),
        Target::Service(path) => {
            let mut s = UnixStream::connect(path).await?;
            frame::send(&mut s, &ServiceHeader::FromMachine(FromMachine { machine_id: machine_id.into() }))
                .await?;
            Box::new(s)
        }
    })
}

pub trait Splice: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send {}
impl<T: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send> Splice for T {}

/// Status entries for the desired forwards.
pub fn statuses(wanted: &[Forward], errors: &HashMap<String, String>) -> Vec<ForwardStatus> {
    wanted
        .iter()
        .map(|f| ForwardStatus {
            id: f.id.clone(),
            state: if errors.contains_key(&f.id) { ForwardState::Failed } else { ForwardState::Listening },
            error: errors.get(&f.id).cloned(),
        })
        .collect()
}
