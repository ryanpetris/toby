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
use toby_proto::{frame, relay, session, types};
use tokio::net::{UnixListener, UnixStream};

use crate::link::{RelayControl, open_relay};

const HEADER_TIMEOUT: Duration = Duration::from_secs(10);
/// Guest connections must send their header quickly.
const GUEST_HEADER_TIMEOUT: Duration = Duration::from_secs(5);
/// Guest connections allowed to be waiting for their header at once.
const MAX_PENDING_GUEST: usize = 32;
const ACCEPT_BACKOFF: Duration = Duration::from_millis(100);
/// Longest time a boot helper may run.
const HELPER_TIMEOUT: Duration = Duration::from_secs(120);
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
    /// Commands run as root, in order, once per guest boot before the machine
    /// is ready (plan §9.6).
    pub boot_helpers: Vec<Vec<String>>,
}

pub struct Machine {
    config: Config,
    relay: RelayControl,
    status: Mutex<MachineStatus>,
    ready_sent: std::sync::atomic::AtomicBool,
    on_ready: Box<dyn Fn() + Send + Sync>,
    /// Serializes readiness checks and boot helpers.
    booting: tokio::sync::Mutex<()>,
    /// Bounds guest connections that have not sent their header yet.
    pending_guest: Arc<tokio::sync::Semaphore>,
}

fn bind(path: &Path) -> io::Result<UnixListener> {
    let _ = std::fs::remove_file(path);
    let l = UnixListener::bind(path)?;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))?;
    Ok(l)
}

impl Machine {
    pub fn new(config: Config, on_ready: impl Fn() + Send + Sync + 'static) -> Arc<Machine> {
        // Keep the record of completed boot helpers across restarts of this process.
        let previous = MachineStatus::load(&config.runtime.status()).ok();
        let status = MachineStatus {
            observed_generation: config.generation,
            helpers_boot_id: previous.and_then(|p| p.helpers_boot_id),
            ..Default::default()
        };
        Arc::new(Machine {
            relay: RelayControl::new(config.runtime.vsock()),
            config,
            status: Mutex::new(status),
            ready_sent: false.into(),
            on_ready: Box::new(on_ready),
            booting: tokio::sync::Mutex::new(()),
            pending_guest: Arc::new(tokio::sync::Semaphore::new(MAX_PENDING_GUEST)),
        })
    }

    fn update_status(&self, f: impl FnOnce(&mut MachineStatus)) {
        let mut s = self.status.lock().unwrap();
        f(&mut s);
        let _ = s.store(&self.config.runtime.status());
    }

    /// Runs one command to completion in the guest as root and returns its
    /// exit status and (truncated) output.
    pub async fn run_helper(&self, argv: &[String]) -> io::Result<(types::ExitStatus, String)> {
        let spec = types::SpawnSpec {
            session_id: toby_config::new_id(),
            argv: argv.to_vec(),
            env: Vec::new(),
            cwd: Some("/".into()),
            identity: types::Identity::Root,
            tty: None,
            keep_after_exit: true,
            start_on_attach: true,
        };
        let id = spec.session_id.clone();
        let req =
            relay::Request::Spawn(relay::Spawn { spec, version: Some(self.config.runtime_version.clone()) });
        match self.relay.call(&req).await? {
            relay::Response::Spawned(_) => {}
            relay::Response::Failed(f) => return Err(io::Error::other(f.error)),
            other => return Err(io::Error::other(format!("unexpected relay response {other:?}"))),
        }

        let header = HostHeader::SessionAttach(toby_proto::stream::SessionAttach { session_id: id });
        let (mut s, reply) = open_relay(&self.config.runtime.vsock(), &header).await?;
        reply.into_result().map_err(io::Error::other)?;
        let hello = session::ClientFrame::Hello(session::Hello {
            versions: types::SUPPORTED.to_vec(),
            rows: 0,
            cols: 0,
            want_replay: true,
            resume_from: None,
        });
        frame::send(&mut s, &hello).await?;
        let mut output = Vec::new();
        loop {
            match frame::recv::<session::ServerFrame, _>(&mut s).await? {
                session::ServerFrame::Stdout(o) => output.extend(o.bytes),
                session::ServerFrame::Stderr(e) => output.extend(e.bytes),
                session::ServerFrame::Replay(r) => output.extend(r.bytes),
                session::ServerFrame::Exit(e) => {
                    output.truncate(64 * 1024);
                    return Ok((e.status, String::from_utf8_lossy(&output).into_owned()));
                }
                session::ServerFrame::Refused(r) => return Err(io::Error::other(r.error)),
                _ => {}
            }
        }
    }

    async fn run_boot_helpers(&self) -> Result<(), String> {
        for argv in &self.config.boot_helpers {
            let name = argv.get(3).cloned().unwrap_or_else(|| argv.join(" "));
            let result = tokio::time::timeout(HELPER_TIMEOUT, self.run_helper(argv))
                .await
                .unwrap_or_else(|_| Err(io::Error::new(io::ErrorKind::TimedOut, "did not finish in time")));
            match result {
                Ok((types::ExitStatus::Code(0), _)) => {}
                Ok((status, output)) => {
                    return Err(format!("{name} failed ({}): {}", status.code(), output.trim()));
                }
                Err(e) => return Err(format!("{name}: {e}")),
            }
        }
        Ok(())
    }

    /// Marks the machine ready once the relay answers and the boot helpers
    /// have run for the current guest boot. Returns whether it is ready.
    async fn relay_up(&self) -> bool {
        let _booting = self.booting.lock().await;
        let info = match self.relay.call(&relay::Request::Hello(relay::Hello {})).await {
            Ok(relay::Response::RelayInfo(info)) => info,
            _ => return false,
        };
        let done = self.status.lock().unwrap().helpers_boot_id.as_deref() == Some(info.boot_id.as_str());
        if !done && let Err(e) = self.run_boot_helpers().await {
            self.update_status(|s| {
                s.state = State::Failed;
                s.error = Some(e);
            });
            return false;
        }
        self.update_status(|s| {
            s.state = State::Ready;
            s.error = None;
            s.proto = Some(types::V1);
            s.relay_version = Some(info.version);
            s.helpers_boot_id = Some(info.boot_id.clone());
            s.boot_id = Some(info.boot_id);
        });
        if !self.ready_sent.swap(true, std::sync::atomic::Ordering::AcqRel) {
            (self.on_ready)();
        }
        true
    }

    /// Runs until the process is stopped.
    pub async fn run(self: Arc<Self>) -> io::Result<()> {
        let rt = &self.config.runtime;
        let guest = bind(&rt.vsock_listen(link::RELAY_PORT))?;
        let control = bind(&rt.control_sock())?;
        let sessions = bind(&rt.session_sock())?;
        self.update_status(|s| s.state = State::Starting);

        // After a restart of this process the relay is already running and
        // will not announce itself again, so keep checking until it answers.
        {
            let this = self.clone();
            tokio::spawn(async move {
                while !this.relay_up().await && this.status.lock().unwrap().state != State::Failed {
                    tokio::time::sleep(Duration::from_secs(2)).await;
                }
            });
        }

        loop {
            tokio::select! {
                r = guest.accept() => match r {
                    Ok((s, _)) => {
                        // Excess guest connections are dropped rather than queued.
                        let Ok(permit) = self.pending_guest.clone().try_acquire_owned() else { continue };
                        let this = self.clone();
                        tokio::spawn(async move { let _ = this.guest_conn(s, permit).await; });
                    }
                    Err(_) => tokio::time::sleep(ACCEPT_BACKOFF).await,
                },
                r = control.accept() => match r {
                    Ok((s, _)) => {
                        let this = self.clone();
                        tokio::spawn(async move { let _ = this.control_conn(s).await; });
                    }
                    Err(_) => tokio::time::sleep(ACCEPT_BACKOFF).await,
                },
                r = sessions.accept() => match r {
                    Ok((s, _)) => {
                        let this = self.clone();
                        tokio::spawn(async move { let _ = this.session_conn(s).await; });
                    }
                    Err(_) => tokio::time::sleep(ACCEPT_BACKOFF).await,
                },
            }
        }
    }

    /// A connection the guest opened to the host. Everything from the guest is
    /// untrusted: only known headers are accepted.
    async fn guest_conn(
        self: Arc<Self>,
        mut s: UnixStream,
        permit: tokio::sync::OwnedSemaphorePermit,
    ) -> io::Result<()> {
        let header: GuestHeader = tokio::time::timeout(GUEST_HEADER_TIMEOUT, frame::recv(&mut s))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no header"))??;
        drop(permit);
        match header {
            GuestHeader::RelayHello(hello) => {
                let Some(version) = types::negotiate(&hello.proto_versions) else {
                    frame::send(&mut s, &Reply::refused("unsupported protocol version")).await?;
                    return Ok(());
                };
                frame::send(&mut s, &Reply::version(version)).await?;
                // Any guest process can send this, so it only prompts a check
                // over the control channel; the relay's answers there decide.
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
            frame::send(&mut client, &Reply::refused("only session attachments are accepted here")).await?;
            return Ok(());
        }
        let (mut guest, reply) = match open_relay(&self.config.runtime.vsock(), &header).await {
            Ok(r) => r,
            Err(e) => {
                frame::send(&mut client, &Reply::refused(format!("machine is not reachable: {e}"))).await?;
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
                    relay::Response::Spawned(s) => {
                        Response::Spawned(machine::Spawned { session_id: s.session_id })
                    }
                    relay::Response::Failed(f) => Response::failed(f.error),
                    other => Response::failed(format!("unexpected relay response {other:?}")),
                }
            }
            Request::Sessions(_) => {
                match self.relay.call(&relay::Request::Sessions(relay::Sessions {})).await? {
                    relay::Response::SessionList(l) => {
                        Response::SessionList(machine::SessionList { sessions: l.sessions })
                    }
                    relay::Response::Failed(f) => Response::failed(f.error),
                    other => Response::failed(format!("unexpected relay response {other:?}")),
                }
            }
            Request::Kill(k) => {
                let r = relay::Request::Kill(relay::Kill { session_id: k.session_id, signal: k.signal });
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
