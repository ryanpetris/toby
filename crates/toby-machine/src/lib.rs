//! The per-machine host process (`toby internal machine`): owns the machine's
//! host-side sockets, keeps the relay control channel, and splices streams
//! between host clients and the guest (plan §8.3, §11, §13.3).

pub mod link;

use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use toby_config::machine::{MachineStatus, State};
use toby_config::paths::MachineRuntime;
use toby_engine::cloud_hypervisor::Api;
use toby_proto::machine::{self, Request, Response};
use toby_proto::stream::{GuestHeader, HostHeader, Reply};
use toby_proto::{frame, relay, types};
use tokio::net::{UnixListener, UnixStream};

use crate::link::{RelayControl, open_relay};

const HEADER_TIMEOUT: Duration = Duration::from_secs(10);
/// How long the guest gets to power off after the power button.
const POWER_OFF_GRACE: Duration = Duration::from_secs(30);

/// Static facts about the machine this process serves.
#[derive(Debug, Clone)]
pub struct Config {
    pub id: String,
    pub generation: u64,
    pub runtime: MachineRuntime,
    /// Toby version guest sessions are started with.
    pub runtime_version: String,
}

pub struct Machine {
    config: Config,
    relay: RelayControl,
    status: Mutex<MachineStatus>,
    ready_sent: std::sync::atomic::AtomicBool,
    on_ready: Box<dyn Fn() + Send + Sync>,
}

fn bind(path: &Path) -> io::Result<UnixListener> {
    let _ = std::fs::remove_file(path);
    let l = UnixListener::bind(path)?;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))?;
    Ok(l)
}

impl Machine {
    pub fn new(config: Config, on_ready: impl Fn() + Send + Sync + 'static) -> Arc<Machine> {
        let status = MachineStatus {
            observed_generation: config.generation,
            ..Default::default()
        };
        Arc::new(Machine {
            relay: RelayControl::new(config.runtime.vsock()),
            config,
            status: Mutex::new(status),
            ready_sent: false.into(),
            on_ready: Box::new(on_ready),
        })
    }

    fn update_status(&self, f: impl FnOnce(&mut MachineStatus)) {
        let mut s = self.status.lock().unwrap();
        f(&mut s);
        let _ = s.store(&self.config.runtime.status());
    }

    /// Marks the machine ready once the relay control channel works.
    async fn relay_up(&self) {
        let info = match self.relay.call(&relay::Request::Hello(relay::Hello {})).await {
            Ok(relay::Response::RelayInfo(info)) => info,
            _ => return,
        };
        self.update_status(|s| {
            s.state = State::Ready;
            s.proto = Some(types::V1);
            s.relay_version = Some(info.version);
            s.boot_id = Some(info.boot_id);
        });
        if !self.ready_sent.swap(true, std::sync::atomic::Ordering::AcqRel) {
            (self.on_ready)();
        }
    }

    /// Runs until the process is stopped.
    pub async fn run(self: Arc<Self>) -> io::Result<()> {
        let rt = &self.config.runtime;
        let guest = bind(&rt.vsock_listen(link::RELAY_PORT))?;
        let control = bind(&rt.control_sock())?;
        let sessions = bind(&rt.session_sock())?;
        self.update_status(|s| s.state = State::Starting);

        // After a restart of this process the relay is already running and
        // will not announce itself again.
        {
            let this = self.clone();
            tokio::spawn(async move { this.relay_up().await });
        }

        loop {
            tokio::select! {
                Ok((s, _)) = guest.accept() => {
                    let this = self.clone();
                    tokio::spawn(async move { let _ = this.guest_conn(s).await; });
                }
                Ok((s, _)) = control.accept() => {
                    let this = self.clone();
                    tokio::spawn(async move { let _ = this.control_conn(s).await; });
                }
                Ok((s, _)) = sessions.accept() => {
                    let this = self.clone();
                    tokio::spawn(async move { let _ = this.session_conn(s).await; });
                }
            }
        }
    }

    /// A connection the guest opened to the host. Everything from the guest is
    /// untrusted: only known headers are accepted.
    async fn guest_conn(self: Arc<Self>, mut s: UnixStream) -> io::Result<()> {
        let header: GuestHeader = tokio::time::timeout(HEADER_TIMEOUT, frame::recv(&mut s))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no header"))??;
        match header {
            GuestHeader::RelayHello(hello) => {
                let Some(version) = types::negotiate(&hello.proto_versions) else {
                    frame::send(&mut s, &Reply::refused("unsupported protocol version")).await?;
                    return Ok(());
                };
                frame::send(&mut s, &Reply::version(version)).await?;
                self.relay.reset().await;
                self.relay_up().await;
            }
            GuestHeader::Accepted(_) => {
                frame::send(&mut s, &Reply::refused("unknown listener")).await?;
            }
        }
        Ok(())
    }

    /// A host client's session attachment: forwarded to the relay and spliced.
    async fn session_conn(self: Arc<Self>, mut client: UnixStream) -> io::Result<()> {
        let header: HostHeader = tokio::time::timeout(HEADER_TIMEOUT, frame::recv(&mut client))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no header"))??;
        if !matches!(header, HostHeader::SessionAttach(_)) {
            frame::send(
                &mut client,
                &Reply::refused("only session attachments are accepted here"),
            )
            .await?;
            return Ok(());
        }
        let (mut guest, reply) = match open_relay(&self.config.runtime.vsock(), &header).await {
            Ok(r) => r,
            Err(e) => {
                frame::send(
                    &mut client,
                    &Reply::refused(format!("machine is not reachable: {e}")),
                )
                .await?;
                return Ok(());
            }
        };
        let ok = matches!(reply, Reply::Ok(_));
        frame::send(&mut client, &reply).await?;
        if ok {
            tokio::io::copy_bidirectional(&mut client, &mut guest).await?;
        }
        Ok(())
    }

    async fn control_conn(self: Arc<Self>, mut s: UnixStream) -> io::Result<()> {
        let hello: Request = tokio::time::timeout(HEADER_TIMEOUT, frame::recv(&mut s))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no hello"))??;
        let Request::Hello(hello) = hello else {
            frame::send(&mut s, &Response::failed("expected hello")).await?;
            return Ok(());
        };
        let Some(version) = types::negotiate(&hello.versions) else {
            frame::send(&mut s, &Response::failed("unsupported protocol version")).await?;
            return Ok(());
        };
        let welcome = Response::Welcome(machine::Welcome {
            version,
            machine_id: self.config.id.clone(),
            toby_version: env!("CARGO_PKG_VERSION").into(),
        });
        frame::send(&mut s, &welcome).await?;

        loop {
            let req: Request = match frame::recv(&mut s).await {
                Ok(r) => r,
                Err(toby_proto::Error::Closed) => return Ok(()),
                Err(e) => return Err(e.into()),
            };
            let resp = self.clone().request(req).await.unwrap_or_else(Response::failed);
            frame::send(&mut s, &resp).await?;
        }
    }

    async fn request(self: Arc<Self>, req: Request) -> io::Result<Response> {
        Ok(match req {
            Request::Hello(_) => Response::failed("already greeted"),
            Request::Spawn(sp) => {
                let r = relay::Request::Spawn(relay::Spawn {
                    spec: sp.spec,
                    version: Some(self.config.runtime_version.clone()),
                });
                match self.relay.call(&r).await? {
                    relay::Response::Spawned(s) => Response::Spawned(machine::Spawned {
                        session_id: s.session_id,
                    }),
                    relay::Response::Failed(f) => Response::failed(f.error),
                    other => Response::failed(format!("unexpected relay response {other:?}")),
                }
            }
            Request::Sessions(_) => match self
                .relay
                .call(&relay::Request::Sessions(relay::Sessions {}))
                .await?
            {
                relay::Response::SessionList(l) => {
                    Response::SessionList(machine::SessionList { sessions: l.sessions })
                }
                relay::Response::Failed(f) => Response::failed(f.error),
                other => Response::failed(format!("unexpected relay response {other:?}")),
            },
            Request::Kill(k) => {
                let r = relay::Request::Kill(relay::Kill {
                    session_id: k.session_id,
                    signal: k.signal,
                });
                match self.relay.call(&r).await? {
                    relay::Response::Done(_) => Response::Done(machine::Done {}),
                    relay::Response::Failed(f) => Response::failed(f.error),
                    other => Response::failed(format!("unexpected relay response {other:?}")),
                }
            }
            Request::Status(_) => {
                let st = self.status.lock().unwrap().clone();
                Response::MachineStatus(machine::MachineStatus {
                    state: match st.state {
                        State::Starting => machine::MachineState::Starting,
                        State::Ready => machine::MachineState::Ready,
                        State::Stopping => machine::MachineState::Stopping,
                        State::Failed => machine::MachineState::Failed,
                    },
                    relay_version: st.relay_version,
                    boot_id: st.boot_id,
                })
            }
            Request::Stop(_) => {
                self.update_status(|s| s.state = State::Stopping);
                let api = Api::new(self.config.runtime.ch_api());
                tokio::spawn(power_off(api));
                Response::Done(machine::Done {})
            }
        })
    }
}

/// Presses the power button, then stops the VM if the guest does not power
/// off in time.
pub async fn power_off(api: Api) {
    if api.power_button().await.is_ok() {
        let deadline = tokio::time::Instant::now() + POWER_OFF_GRACE;
        while tokio::time::Instant::now() < deadline {
            if !api.alive().await {
                return;
            }
            tokio::time::sleep(Duration::from_millis(500)).await;
        }
    }
    let _ = api.shutdown().await;
    let _ = api.shutdown_vmm().await;
}
