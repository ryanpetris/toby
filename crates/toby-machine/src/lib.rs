//! The per-machine host process (`toby internal machine`): owns the machine's
//! host-side sockets, keeps the relay control channel, and splices streams
//! between host clients and the guest (plan §8.3, §11, §13.3).

pub mod link;

use std::collections::BTreeMap;
use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use toby_config::machine::{Attach, AttachState, AttachStatus, MachineSpec, MachineStatus, State};
use toby_config::paths::MachineRuntime;
use toby_engine::cloud_hypervisor::Api;
use toby_proto::machine::{self, Request, Response};
use toby_proto::stream::{GuestHeader, HostHeader, Reply};
use toby_proto::{frame, fs, relay, session, types};
use tokio::net::{UnixListener, UnixStream};

use crate::link::{RelayControl, open_relay};

const HEADER_TIMEOUT: Duration = Duration::from_secs(10);
/// Guest connections must send their header quickly.
const GUEST_HEADER_TIMEOUT: Duration = Duration::from_secs(5);
/// Guest connections handled at once; more are dropped.
const MAX_PENDING_GUEST: usize = 32;
const ACCEPT_BACKOFF: Duration = Duration::from_millis(100);
/// Longest time a boot helper may run.
const HELPER_TIMEOUT: Duration = Duration::from_secs(120);
/// Helper output kept for error messages.
const HELPER_OUTPUT: usize = 4096;
/// How long the guest gets to power off after the power button.
const POWER_OFF_GRACE: Duration = Duration::from_secs(30);

/// Static facts about the machine this process serves.
#[derive(Debug, Clone)]
pub struct Config {
    pub id: String,
    pub generation: u64,
    /// The desired state file, watched for changes.
    pub desired: PathBuf,
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
    /// Bounds guest connections being handled at once.
    pending_guest: Arc<tokio::sync::Semaphore>,
    /// Attachments mounted in the guest, by ID.
    mounted: Mutex<BTreeMap<String, Attach>>,
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
        let previous = MachineStatus::load(&config.runtime.status()).ok().unwrap_or_default();
        let mounted = previous
            .attach
            .iter()
            .filter(|a| a.state == AttachState::Ready)
            .map(|a| {
                let attach = Attach {
                    id: a.id.clone(),
                    host: a.host.clone(),
                    at: a.at.clone(),
                    read_only: a.read_only,
                    pinned: false,
                    persist: false,
                };
                (a.id.clone(), attach)
            })
            .collect();
        let status = MachineStatus {
            // Nothing of the current desired state is applied yet.
            observed_generation: previous.observed_generation,
            helpers_boot_id: previous.helpers_boot_id,
            attach: previous.attach,
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
            mounted: Mutex::new(mounted),
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
        // The guest controls the output: keep only the start of it.
        let mut output = Vec::new();
        let mut keep = |bytes: Vec<u8>| {
            let room = HELPER_OUTPUT.saturating_sub(output.len());
            output.extend_from_slice(&bytes[..bytes.len().min(room)]);
        };
        loop {
            match frame::recv::<session::ServerFrame, _>(&mut s).await? {
                session::ServerFrame::Stdout(o) => keep(o.bytes),
                session::ServerFrame::Stderr(e) => keep(e.bytes),
                session::ServerFrame::Replay(r) => keep(r.bytes),
                session::ServerFrame::Exit(e) => {
                    return Ok((e.status, printable(&output)));
                }
                session::ServerFrame::Refused(r) => return Err(io::Error::other(r.error)),
                _ => {}
            }
        }
    }

    /// Runs a helper and turns anything but success into an error message.
    async fn helper(&self, argv: &[String]) -> Result<(), String> {
        let result = tokio::time::timeout(HELPER_TIMEOUT, self.run_helper(argv))
            .await
            .unwrap_or_else(|_| Err(io::Error::new(io::ErrorKind::TimedOut, "did not finish in time")));
        match result {
            Ok((types::ExitStatus::Code(0), _)) => Ok(()),
            Ok((status, output)) => {
                // Helpers report their own errors as "toby: <message>".
                let message = output.trim().trim_start_matches("toby: ");
                if message.is_empty() {
                    Err(format!("failed with exit status {}", status.code()))
                } else {
                    Err(message.to_string())
                }
            }
            Err(e) => Err(e.to_string()),
        }
    }

    async fn run_boot_helpers(&self) -> Result<(), String> {
        for argv in &self.config.boot_helpers {
            let name = argv.get(3).map_or("helper", String::as_str);
            self.helper(argv).await.map_err(|e| format!("{name}: {e}"))?;
        }
        Ok(())
    }

    /// `toby guest helper <args>` with the machine's runtime version.
    fn helper_argv(&self, args: &[&str]) -> Vec<String> {
        let toby = format!("/run/toby/fs/versions/{}/toby", self.config.runtime_version);
        [toby.as_str(), "guest", "helper"].iter().chain(args).map(|s| s.to_string()).collect()
    }

    async fn attach(&self, fs: &mut FsControl, a: &Attach) -> Result<(), String> {
        let add = fs::Add { id: a.id.clone(), host_path: a.host.clone(), read_only: a.read_only };
        fs.call(fs::Request::Add(add)).await.map_err(|e| format!("serving {}: {e}", a.host))?;
        let src = format!("/run/toby/fs/projects/{}", a.id);
        let mut args = vec!["attach", "--src", &src, "--at", &a.at];
        if a.read_only {
            args.push("--ro");
        }
        if let Err(e) = self.helper(&self.helper_argv(&args)).await {
            let _ = fs.call(fs::Request::Remove(fs::Remove { id: a.id.clone() })).await;
            return Err(e);
        }
        Ok(())
    }

    async fn detach(&self, fs: &mut FsControl, a: &Attach) -> Result<(), String> {
        let src = format!("/run/toby/fs/projects/{}", a.id);
        self.helper(&self.helper_argv(&["detach", "--src", &src, "--at", &a.at])).await?;
        fs.call(fs::Request::Remove(fs::Remove { id: a.id.clone() }))
            .await
            .map_err(|e| format!("removing {}: {e}", a.host))
    }

    /// Brings the guest's attachments in line with the desired state (plan
    /// §10.4). Callers hold `booting`. Fails only if nothing could be
    /// reconciled; single attachments report their own errors.
    async fn reconcile(&self) -> Result<(), String> {
        let spec =
            MachineSpec::load(&self.config.desired).map_err(|e| format!("reading the desired state: {e}"))?;
        let mut fs = FsControl::connect(&self.config.runtime.fs_control_sock())
            .await
            .map_err(|e| format!("file sharing is not available: {e}"))?;
        let desired: BTreeMap<String, Attach> =
            spec.attach.iter().map(|a| (a.id.clone(), a.clone())).collect();
        let same = |a: &Attach, b: &Attach| a.host == b.host && a.at == b.at && a.read_only == b.read_only;
        let mut mounted = self.mounted.lock().unwrap().clone();
        let mut errors: BTreeMap<String, String> = BTreeMap::new();

        for (id, a) in mounted.clone() {
            if desired.get(&id).is_some_and(|d| same(d, &a)) {
                continue;
            }
            match self.detach(&mut fs, &a).await {
                Ok(()) => {
                    mounted.remove(&id);
                }
                Err(e) => {
                    errors.insert(id, e);
                }
            }
        }
        for (id, a) in &desired {
            if mounted.contains_key(id) {
                continue;
            }
            match self.attach(&mut fs, a).await {
                Ok(()) => {
                    mounted.insert(id.clone(), a.clone());
                }
                Err(e) => {
                    errors.insert(id.clone(), e);
                }
            }
        }

        let entry = |a: &Attach, state| AttachStatus {
            id: a.id.clone(),
            host: a.host.clone(),
            at: a.at.clone(),
            read_only: a.read_only,
            state,
            error: errors.get(&a.id).cloned(),
        };
        // Anything still served that is neither desired nor mounted (left by
        // an earlier boot or a failed cleanup) is no longer shared.
        if let Ok(served) = fs.list().await {
            for a in served {
                if !desired.contains_key(&a.id) && !mounted.contains_key(&a.id) {
                    let _ = fs.call(fs::Request::Remove(fs::Remove { id: a.id })).await;
                }
            }
        }

        let mut entries: Vec<AttachStatus> = mounted.values().map(|a| entry(a, AttachState::Ready)).collect();
        entries.extend(
            desired.values().filter(|a| !mounted.contains_key(&a.id)).map(|a| entry(a, AttachState::Failed)),
        );
        *self.mounted.lock().unwrap() = mounted;
        self.update_status(|s| {
            s.attach = entries;
            s.observed_generation = spec.generation;
        });
        Ok(())
    }

    /// Marks the machine ready once the relay answers and the boot helpers
    /// have run for the current guest boot. Returns whether it is ready.
    async fn relay_up(&self) -> bool {
        let _booting = self.booting.lock().await;
        self.check_relay().await
    }

    /// Like `relay_up`, but does nothing while a check is already running:
    /// used for guest hellos, which any guest process can send.
    async fn relay_up_if_idle(&self) {
        if let Ok(_booting) = self.booting.try_lock() {
            self.check_relay().await;
        }
    }

    async fn check_relay(&self) -> bool {
        let info = match self.relay.call(&relay::Request::Hello(relay::Hello {})).await {
            Ok(relay::Response::RelayInfo(info)) => info,
            _ => return false,
        };
        let (done, ready) = {
            let s = self.status.lock().unwrap();
            (s.helpers_boot_id.as_deref() == Some(info.boot_id.as_str()), s.state == State::Ready)
        };
        if !done {
            if let Err(e) = self.run_boot_helpers().await {
                self.update_status(|s| {
                    s.state = State::Failed;
                    s.error = Some(e);
                });
                return false;
            }
            // A new boot has none of the previous boot's mounts.
            self.mounted.lock().unwrap().clear();
        }
        let mut error = None;
        if !done || !ready {
            error = self.reconcile().await.err();
        }
        self.update_status(|s| {
            s.state = State::Ready;
            s.error = error;
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
        self.clone().watch_desired()?;

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

    /// Reconciles whenever the desired state file is replaced.
    fn watch_desired(self: Arc<Self>) -> io::Result<()> {
        use nix::sys::inotify::{AddWatchFlags, InitFlags, Inotify};
        use tokio::io::unix::AsyncFd;

        let dir = self.config.desired.parent().unwrap_or(Path::new(".")).to_path_buf();
        let name = self.config.desired.file_name().map(|n| n.to_owned());
        let inotify = Inotify::init(InitFlags::IN_NONBLOCK | InitFlags::IN_CLOEXEC)?;
        inotify.add_watch(&dir, AddWatchFlags::IN_CLOSE_WRITE | AddWatchFlags::IN_MOVED_TO)?;
        let fd = AsyncFd::new(Watch(inotify))?;
        tokio::spawn(async move {
            loop {
                let Ok(mut guard) = fd.readable().await else { return };
                let changed = match guard.get_inner().0.read_events() {
                    Ok(events) => events.iter().any(|e| e.name.as_ref() == name.as_ref()),
                    Err(nix::errno::Errno::EAGAIN) => {
                        guard.clear_ready();
                        continue;
                    }
                    Err(_) => return,
                };
                if changed {
                    let _booting = self.booting.lock().await;
                    if self.status.lock().unwrap().state == State::Ready {
                        let error = self.reconcile().await.err();
                        self.update_status(|s| s.error = error);
                    }
                }
            }
        });
        Ok(())
    }

    /// A connection the guest opened to the host. Everything from the guest is
    /// untrusted: only known headers are accepted.
    async fn guest_conn(
        self: Arc<Self>,
        mut s: UnixStream,
        _permit: tokio::sync::OwnedSemaphorePermit,
    ) -> io::Result<()> {
        let header: GuestHeader = tokio::time::timeout(GUEST_HEADER_TIMEOUT, frame::recv(&mut s))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no header"))??;
        match header {
            GuestHeader::RelayHello(hello) => {
                let Some(version) = types::negotiate(&hello.proto_versions) else {
                    frame::send(&mut s, &Reply::refused("unsupported protocol version")).await?;
                    return Ok(());
                };
                frame::send(&mut s, &Reply::version(version)).await?;
                // Any guest process can send this, so it only prompts a check
                // over the control channel; the relay's answers there decide.
                self.relay_up_if_idle().await;
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

struct Watch(nix::sys::inotify::Inotify);

impl std::os::fd::AsRawFd for Watch {
    fn as_raw_fd(&self) -> std::os::fd::RawFd {
        use std::os::fd::AsFd;
        self.0.as_fd().as_raw_fd()
    }
}

/// A connection to the machine's file sharing control socket.
struct FsControl {
    stream: UnixStream,
}

impl FsControl {
    async fn connect(path: &Path) -> io::Result<FsControl> {
        let mut stream = UnixStream::connect(path).await?;
        frame::send(&mut stream, &fs::Request::Hello(fs::Hello { versions: types::SUPPORTED.to_vec() }))
            .await?;
        match frame::recv(&mut stream).await? {
            fs::Response::Welcome(_) => Ok(FsControl { stream }),
            fs::Response::Failed(f) => Err(io::Error::other(f.error)),
            other => Err(io::Error::other(format!("unexpected response {other:?}"))),
        }
    }

    async fn list(&mut self) -> io::Result<Vec<fs::Attachment>> {
        frame::send(&mut self.stream, &fs::Request::List(fs::List {})).await?;
        match frame::recv(&mut self.stream).await? {
            fs::Response::Attachments(l) => Ok(l.attachments),
            fs::Response::Failed(f) => Err(io::Error::other(f.error)),
            other => Err(io::Error::other(format!("unexpected response {other:?}"))),
        }
    }

    async fn call(&mut self, req: fs::Request) -> io::Result<()> {
        frame::send(&mut self.stream, &req).await?;
        match frame::recv(&mut self.stream).await? {
            fs::Response::Done(_) => Ok(()),
            fs::Response::Failed(f) => Err(io::Error::other(f.error)),
            other => Err(io::Error::other(format!("unexpected response {other:?}"))),
        }
    }
}

/// Guest text made safe to show on a terminal: control characters other
/// than newlines and tabs are replaced.
fn printable(bytes: &[u8]) -> String {
    String::from_utf8_lossy(bytes)
        .chars()
        .map(|c| if c.is_control() && c != '\n' && c != '\t' { '\u{fffd}' } else { c })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn guest_text_loses_control_characters() {
        assert_eq!(printable(b"ok\n\tline\x1b]52;c;x\x07"), "ok\n\tline\u{fffd}]52;c;x\u{fffd}");
    }
}
