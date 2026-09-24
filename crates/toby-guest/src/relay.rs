//! `toby guest relay`: accepts host connections on vsock port 1024 and bridges
//! them to guest sessions, guest endpoints and the relay control channel
//! (plan §11.3, §11.4).

use std::collections::HashMap;
use std::future::Future;
use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use nix::sys::signal::{Signal, kill, killpg};
use nix::unistd::Pid;
use toby_proto::relay::{self, Request, Response};
use toby_proto::stream::{Accepted, GuestHeader, HostHeader, RelayHello, Reply};
use toby_proto::types::{self, Endpoint, SessionInfo, SpawnSpec};
use toby_proto::{Message, frame};
use tokio::io::{AsyncRead, AsyncWrite};
use tokio::net::{TcpListener, TcpStream, UnixListener, UnixStream};
use tokio::task::AbortHandle;
use tokio_vsock::{VMADDR_CID_ANY, VMADDR_CID_HOST, VsockAddr, VsockListener, VsockStream};

use crate::paths::{GuestPaths, session_files, valid_id, valid_version};
use crate::record::{self, SessionRecord};
use crate::session;

/// The vsock port the relay listens on and `toby-machine` listens on.
pub const PORT: u32 = 1024;

const HEADER_TIMEOUT: Duration = Duration::from_secs(10);
const SPAWN_TIMEOUT: Duration = Duration::from_secs(10);

/// A byte stream the relay can splice.
pub trait Stream: AsyncRead + AsyncWrite + Unpin + Send + 'static {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send + 'static> Stream for T {}

pub type BoxStream = Box<dyn Stream>;
type ConnectFuture = Pin<Box<dyn Future<Output = io::Result<BoxStream>> + Send>>;

/// Opens guest-to-host connections.
pub type Connector = Arc<dyn Fn() -> ConnectFuture + Send + Sync>;

/// Connects to `toby-machine` over vsock.
pub fn vsock_connector() -> Connector {
    Arc::new(|| {
        Box::pin(async {
            let s = VsockStream::connect(VsockAddr::new(VMADDR_CID_HOST, PORT)).await?;
            Ok(Box::new(s) as BoxStream)
        })
    })
}

/// How sessions are started.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Launcher {
    /// In their own scope unit with `systemd-run --scope`, so they survive
    /// relay restarts.
    SystemdScope,
    /// As plain child processes (tests).
    Direct,
}

pub struct Relay {
    paths: GuestPaths,
    /// Binary used for sessions when a spawn names no runtime version.
    exe: PathBuf,
    launcher: Launcher,
    connector: Connector,
    listeners: Mutex<HashMap<String, AbortHandle>>,
}

impl Relay {
    pub fn new(paths: GuestPaths, exe: PathBuf, launcher: Launcher, connector: Connector) -> Arc<Self> {
        Arc::new(Relay {
            paths,
            exe,
            launcher,
            connector,
            listeners: Mutex::new(HashMap::new()),
        })
    }

    /// Serves one host-initiated connection.
    pub async fn handle<S: Stream>(self: Arc<Self>, mut conn: S) -> io::Result<()> {
        let header: HostHeader = tokio::time::timeout(HEADER_TIMEOUT, frame::recv(&mut conn))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no stream header"))??;

        match header {
            HostHeader::Control(c) => {
                let Some(version) = types::negotiate(&c.proto_versions) else {
                    frame::send(&mut conn, &Reply::refused("unsupported protocol version")).await?;
                    return Ok(());
                };
                frame::send(&mut conn, &Reply::version(version)).await?;
                self.control(conn).await
            }
            HostHeader::SessionAttach(a) => {
                if !valid_id(&a.session_id) {
                    frame::send(&mut conn, &Reply::refused("invalid session ID")).await?;
                    return Ok(());
                }
                let sock = self.paths.session_dir(&a.session_id).join(session_files::SOCKET);
                match UnixStream::connect(&sock).await {
                    Ok(target) => {
                        frame::send(&mut conn, &Reply::ok()).await?;
                        splice(conn, target).await
                    }
                    Err(_) => {
                        frame::send(&mut conn, &Reply::refused("no such session")).await?;
                        Ok(())
                    }
                }
            }
            HostHeader::Dial(d) => match connect_endpoint(&d.target).await {
                Ok(target) => {
                    frame::send(&mut conn, &Reply::ok()).await?;
                    splice(conn, target).await
                }
                Err(e) => {
                    frame::send(&mut conn, &Reply::refused(format!("{}: {e}", d.target))).await?;
                    Ok(())
                }
            },
        }
    }

    async fn control<S: Stream>(self: Arc<Self>, mut conn: S) -> io::Result<()> {
        loop {
            let req: Request = match frame::recv(&mut conn).await {
                Ok(r) => r,
                Err(toby_proto::Error::Closed) => return Ok(()),
                Err(e) => return Err(e.into()),
            };
            let resp = self.clone().request(req).await;
            frame::send(&mut conn, &resp).await?;
        }
    }

    async fn request(self: Arc<Self>, req: Request) -> Response {
        let result = match req {
            Request::Spawn(s) => self
                .spawn(s)
                .await
                .map(|id| Response::Spawned(relay::Spawned { session_id: id })),
            Request::Listen(l) => self.listen(l).await.map(|()| Response::Done(relay::Done {})),
            Request::Unlisten(u) => {
                if let Some(h) = self.listeners.lock().unwrap().remove(&u.listener_id) {
                    h.abort();
                }
                Ok(Response::Done(relay::Done {}))
            }
            Request::Sessions(_) => Ok(Response::SessionList(relay::SessionList {
                sessions: self.sessions(),
            })),
            Request::Kill(k) => self
                .kill(&k.session_id, k.signal)
                .map(|()| Response::Done(relay::Done {})),
            Request::Forget(f) => self
                .forget(&f.session_id)
                .map(|()| Response::Done(relay::Done {})),
            Request::Ping(_) => Ok(Response::Done(relay::Done {})),
            Request::Hello(_) => Ok(Response::RelayInfo(relay::RelayInfo {
                version: env!("CARGO_PKG_VERSION").to_string(),
                boot_id: boot_id(),
            })),
        };
        result.unwrap_or_else(Response::failed)
    }

    async fn spawn(&self, s: relay::Spawn) -> io::Result<String> {
        let spec = s.spec;
        if !valid_id(&spec.session_id) {
            return Err(io::Error::new(io::ErrorKind::InvalidInput, "invalid session ID"));
        }
        let id = spec.session_id.clone();
        let exe = match &s.version {
            Some(v) if valid_version(v) => self.paths.runtime_binary(v),
            Some(_) => {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "invalid runtime version",
                ));
            }
            None => self.exe.clone(),
        };
        let dir = self.paths.session_dir(&id);
        let sock = dir.join(session_files::SOCKET);

        // A spawn repeated after a lost reply finds its session already there.
        if dir.exists() {
            let existing: SpawnSpec = record::read(&dir.join(session_files::SPEC))?;
            if existing != spec {
                return Err(io::Error::new(
                    io::ErrorKind::AlreadyExists,
                    format!("session {id} exists"),
                ));
            }
            let deadline = tokio::time::Instant::now() + SPAWN_TIMEOUT;
            while !sock.exists() {
                if tokio::time::Instant::now() >= deadline {
                    return Err(io::Error::new(io::ErrorKind::TimedOut, "session did not start"));
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
            return Ok(id);
        }
        session::prepare(&self.paths, &spec)?;

        let mut cmd = match self.launcher {
            Launcher::SystemdScope => {
                let mut c = tokio::process::Command::new("systemd-run");
                c.args(["--scope", "--collect", "--quiet"])
                    .arg(format!("--unit=toby-s-{id}"))
                    .arg("--")
                    .arg(&exe);
                c
            }
            Launcher::Direct => tokio::process::Command::new(&exe),
        };
        cmd.args(["guest", "session", "--id", &id])
            .env("TOBY_GUEST_ROOT", self.paths.root())
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::piped());
        let mut child = cmd.spawn().inspect_err(|_| {
            let _ = std::fs::remove_dir_all(&dir);
        })?;

        // The session's own errors go to the relay's log and, if it fails to
        // start, into the reply.
        let errors = Arc::new(Mutex::new(Vec::<u8>::new()));
        if let Some(mut stderr) = child.stderr.take() {
            let errors = errors.clone();
            tokio::spawn(async move {
                use tokio::io::AsyncReadExt;
                let mut buf = [0u8; 4096];
                while let Ok(n) = stderr.read(&mut buf).await {
                    if n == 0 {
                        break;
                    }
                    let _ = std::io::Write::write_all(&mut std::io::stderr(), &buf[..n]);
                    let mut e = errors.lock().unwrap();
                    if e.len() < 4096 {
                        e.extend_from_slice(&buf[..n]);
                    }
                }
            });
        }

        let deadline = tokio::time::Instant::now() + SPAWN_TIMEOUT;
        loop {
            if sock.exists() {
                break;
            }
            if let Ok(Some(status)) = child.try_wait() {
                let _ = std::fs::remove_dir_all(&dir);
                tokio::time::sleep(Duration::from_millis(20)).await;
                let text = String::from_utf8_lossy(&errors.lock().unwrap())
                    .trim()
                    .to_string();
                let detail = if text.is_empty() { status.to_string() } else { text };
                return Err(io::Error::other(format!("session could not start: {detail}")));
            }
            if tokio::time::Instant::now() >= deadline {
                let _ = child.kill().await;
                let _ = std::fs::remove_dir_all(&dir);
                return Err(io::Error::new(io::ErrorKind::TimedOut, "session did not start"));
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }

        // Reap the launcher; the session itself may outlive this relay.
        tokio::spawn(async move {
            let _ = child.wait().await;
        });
        Ok(id)
    }

    /// Whether the session process named in `rec` still runs; records of
    /// sessions that died abnormally are removed.
    fn alive(&self, rec: &SessionRecord) -> bool {
        let cmdline = std::fs::read(format!("/proc/{}/cmdline", rec.session_pid)).unwrap_or_default();
        let args: Vec<&[u8]> = cmdline.split(|b| *b == 0).collect();
        let ours = args
            .windows(2)
            .any(|w| w[0] == b"--id" && w[1] == rec.info.id.as_bytes());
        if !ours {
            let _ = std::fs::remove_dir_all(self.paths.session_dir(&rec.info.id));
        }
        ours
    }

    fn sessions(&self) -> Vec<SessionInfo> {
        let Ok(entries) = std::fs::read_dir(self.paths.sessions()) else {
            return Vec::new();
        };
        let mut out: Vec<SessionInfo> = entries
            .flatten()
            .filter_map(|e| record::read::<SessionRecord>(&e.path().join(session_files::RECORD)).ok())
            .filter(|r| self.alive(r))
            .map(|r| r.info)
            .collect();
        out.sort_by(|a, b| a.started.cmp(&b.started).then(a.id.cmp(&b.id)));
        out
    }

    fn record(&self, id: &str) -> io::Result<SessionRecord> {
        if !valid_id(id) {
            return Err(io::Error::new(io::ErrorKind::InvalidInput, "invalid session ID"));
        }
        record::read(&self.paths.session_dir(id).join(session_files::RECORD))
            .ok()
            .filter(|r| self.alive(r))
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, format!("no session {id}")))
    }

    fn kill(&self, id: &str, signal: i32) -> io::Result<()> {
        let rec = self.record(id)?;
        if rec.info.exit.is_some() {
            return Ok(());
        }
        let sig = Signal::try_from(signal).map_err(io::Error::from)?;
        if rec.child_pgid <= 1 {
            // The command has not started yet (it starts on attach): end the session.
            return kill(Pid::from_raw(rec.session_pid), Signal::SIGTERM).map_err(io::Error::from);
        }
        killpg(Pid::from_raw(rec.child_pgid), sig).map_err(io::Error::from)
    }

    fn forget(&self, id: &str) -> io::Result<()> {
        let rec = self.record(id)?;
        if rec.info.exit.is_none() {
            return Err(io::Error::other(format!("session {id} is still running")));
        }
        kill(Pid::from_raw(rec.session_pid), Signal::SIGTERM).map_err(io::Error::from)
    }

    async fn listen(self: &Arc<Self>, l: relay::Listen) -> io::Result<()> {
        if !valid_id(&l.listener_id) {
            return Err(io::Error::new(io::ErrorKind::InvalidInput, "invalid listener ID"));
        }
        let accept: Pin<Box<dyn Future<Output = ()> + Send>> = match &l.bind {
            Endpoint::Tcp { addr } => {
                let listener = TcpListener::bind(addr).await?;
                let this = self.clone();
                let id = l.listener_id.clone();
                Box::pin(async move {
                    while let Ok((s, _)) = listener.accept().await {
                        tokio::spawn(this.clone().forward_accepted(id.clone(), Box::new(s)));
                    }
                })
            }
            Endpoint::Unix { path } => {
                let _ = std::fs::remove_file(path);
                if let Some(parent) = std::path::Path::new(path).parent() {
                    std::fs::create_dir_all(parent)?;
                }
                let listener = UnixListener::bind(path)?;
                std::fs::set_permissions(path, std::fs::Permissions::from_mode(l.mode.unwrap_or(0o600)))?;
                let this = self.clone();
                let id = l.listener_id.clone();
                Box::pin(async move {
                    while let Ok((s, _)) = listener.accept().await {
                        tokio::spawn(this.clone().forward_accepted(id.clone(), Box::new(s)));
                    }
                })
            }
        };
        let handle = tokio::spawn(accept).abort_handle();
        if let Some(old) = self.listeners.lock().unwrap().insert(l.listener_id, handle) {
            old.abort();
        }
        Ok(())
    }

    async fn forward_accepted(self: Arc<Self>, listener_id: String, guest: BoxStream) {
        let Ok(mut host) = (self.connector)().await else {
            return;
        };
        let header = GuestHeader::Accepted(Accepted { listener_id });
        if frame::send(&mut host, &header).await.is_err() {
            return;
        }
        if let Ok(Reply::Ok(_)) = frame::recv::<Reply, _>(&mut host).await {
            let _ = splice(guest, host).await;
        }
    }
}

async fn connect_endpoint(ep: &Endpoint) -> io::Result<BoxStream> {
    Ok(match ep {
        Endpoint::Tcp { addr } => Box::new(TcpStream::connect(addr.as_str()).await?),
        Endpoint::Unix { path } => Box::new(UnixStream::connect(path).await?),
    })
}

/// Copies bytes both ways until both directions are closed.
pub async fn splice<A: Stream, B: Stream>(mut a: A, mut b: B) -> io::Result<()> {
    tokio::io::copy_bidirectional(&mut a, &mut b).await.map(|_| ())
}

fn boot_id() -> String {
    std::fs::read_to_string("/proc/sys/kernel/random/boot_id")
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

/// Announces the relay to `toby-machine`, retrying until it answers.
pub async fn announce(connector: Connector) {
    let hello = GuestHeader::RelayHello(RelayHello {
        version: env!("CARGO_PKG_VERSION").to_string(),
        proto_versions: types::SUPPORTED.to_vec(),
        boot_id: boot_id(),
    });
    loop {
        let attempt = async {
            let mut conn = connector().await?;
            frame::write_bytes(&mut conn, &hello.encode()?).await?;
            let reply: Reply = frame::recv(&mut conn).await?;
            reply.into_result().map_err(io::Error::other)?;
            io::Result::Ok(())
        };
        if attempt.await.is_ok() {
            return;
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
}

/// Runs the relay on vsock until the process is stopped.
pub async fn run(paths: GuestPaths) -> io::Result<()> {
    let listener = VsockListener::bind(VsockAddr::new(VMADDR_CID_ANY, PORT))?;
    let connector = vsock_connector();
    let exe = std::env::current_exe()?;
    let relay = Relay::new(paths, exe, Launcher::SystemdScope, connector.clone());

    tokio::spawn(announce(connector));

    loop {
        let (conn, peer) = listener.accept().await?;
        // Only the host may reach the relay; guest processes connecting
        // through vsock loopback are refused.
        if peer.cid() != VMADDR_CID_HOST {
            continue;
        }
        let relay = relay.clone();
        tokio::spawn(async move {
            let _ = relay.handle(conn).await;
        });
    }
}
