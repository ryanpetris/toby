//! The sandbox capability's host end (plan §16.3): tobyd decides what a
//! `toby-connect` request reaches. Toby's own MCP server is served here;
//! an isolated MCP server runs in a services machine of its own, with its
//! secrets only in that process's environment, and the two machines' host
//! processes splice the connection without tobyd.

use std::collections::HashMap;
use std::io;
use std::sync::{Arc, LazyLock, Mutex};
use std::time::Duration;

use toby_config::global::{McpKind, McpServer, Placement};
use toby_config::machine::MachineSpec;
use toby_proto::capability::{CapRequest, CapResponse, Refused, Serve, Splice};
use toby_proto::frame;
use toby_proto::service::ServiceHeader;
use toby_proto::types::{Endpoint, Identity, SpawnSpec};
use tokio::net::UnixStream;

use crate::control::Control;
use crate::server::Daemon;

const HEADER_TIMEOUT: Duration = Duration::from_secs(10);
/// Services machines are small (plan §14.5).
const SERVICES_CPUS: u32 = 1;
const SERVICES_MEMORY: &str = "1G";
/// Services machines stop sooner when idle.
pub const SERVICES_IDLE: Duration = Duration::from_secs(300);

/// The home and root of an MCP server's services machine.
pub fn pair_name(server: &str) -> String {
    format!("mcp-{server}")
}

fn refused(error: impl Into<String>) -> CapResponse {
    CapResponse::Refused(Refused { error: error.into() })
}

/// Serializes preparing each server's services machine.
fn server_lock(name: &str) -> Arc<tokio::sync::Mutex<()>> {
    static LOCKS: LazyLock<Mutex<HashMap<String, Arc<tokio::sync::Mutex<()>>>>> =
        LazyLock::new(Default::default);
    LOCKS.lock().unwrap().entry(name.into()).or_default().clone()
}

/// Makes sure the services machine for `name` exists and runs: its root
/// from the server's image, and its forwards to the host's ports.
async fn ensure_machine(d: &Daemon, name: &str, server: &McpServer) -> Result<String, String> {
    let lock = server_lock(name);
    let _lock = lock.lock().await;
    let pair = pair_name(name);
    let store = &d.builder.store;
    let formatted = match store.home(&pair) {
        Ok(h) => h.formatted,
        Err(_) => {
            store.create_home(&pair, "mcp", 1000, 4 << 30).await.map_err(|e| e.to_string())?;
            false
        }
    };
    if !formatted {
        let mut sink = |_: &[u8], _: bool| {};
        d.builder
            .format_home(&pair, &mut sink)
            .await
            .map_err(|e| format!("preparing the home of {name}: {e}"))?;
    }
    if store.root(&pair).is_err() {
        let config_dir =
            d.machines.paths.global_config().parent().map(|p| p.to_path_buf()).unwrap_or_default();
        let wanted = match &server.image {
            Some(i) => {
                crate::builder::wanted_image(i, &config_dir).map_err(|e| format!("mcp.{name}.image: {e}"))?
            }
            None => crate::builder::Wanted::Source(toby_store::records::ImageSource::Default),
        };
        let image = d
            .builder
            .image_for(&wanted)
            .map_err(|e| format!("{e}; build it with: toby image prepare --mcp {name}"))?;
        store.create_root(&pair, &image.id).await.map_err(|e| e.to_string())?;
    }
    let req = toby_api::EnsureMachine {
        home: Some(pair.clone()),
        root: Some(pair),
        ephemeral: false,
        cpus: Some(SERVICES_CPUS),
        memory: Some(SERVICES_MEMORY.into()),
    };
    let spec = d.machines.ensure_for(req, Some(name)).await.map_err(|e| e.message)?;
    d.machines.set_idle_timeout(&spec.id, SERVICES_IDLE).map_err(|e| e.message)?;
    // host_ports as they are configured now: kept while the machine exists.
    let wanted: Vec<String> = server.host_ports.iter().map(|p| format!("127.0.0.1:{p}")).collect();
    for f in spec.forward.iter().filter(|f| f.persist && !wanted.contains(&f.host)) {
        let _ = d.machines.remove_forward(&spec.id, &f.id).await;
    }
    for addr in wanted.iter().filter(|a| !spec.forward.iter().any(|f| f.persist && &&f.host == a)) {
        let fwd = toby_api::AddForward {
            direction: "guest-to-host".into(),
            host: addr.clone(),
            guest: addr.clone(),
            pinned: false,
            persist: true,
        };
        d.machines
            .add_forward(&spec.id, fwd, None)
            .await
            .map_err(|e| format!("mcp.{name}.host_ports: {}", e.message))?;
    }
    Ok(spec.id)
}

/// A server's command and environment, with substitutions resolved.
type Command = (Vec<String>, Vec<(String, String)>);

fn command(d: &Daemon, name: &str, server: &McpServer) -> Result<Command, String> {
    let config_dir = d.machines.paths.global_config().parent().map(|p| p.to_path_buf()).unwrap_or_default();
    let home = toby_config::paths::home_dir().map_err(|e| e.to_string())?;
    let mut env = Vec::new();
    for (k, v) in &server.env {
        let value = toby_config::subst::resolve(v, &config_dir, &home)
            .map_err(|e| format!("mcp.{name}.env.{k}: {e}"))?;
        env.push((k.clone(), value));
    }
    let mut command = Vec::new();
    for c in &server.command {
        command.push(
            toby_config::subst::resolve(c, &config_dir, &home).map_err(|e| format!("mcp.{name}: {e}"))?,
        );
    }
    Ok((command, env))
}

/// Starts the server for one connection in its services machine.
async fn start_isolated(d: &Daemon, name: &str, server: &McpServer) -> Result<Splice, String> {
    let (command, env) = command(d, name, server)?;
    let machine = ensure_machine(d, name, server).await?;

    // The server runs as the services machine's user, who can write /tmp.
    let socket = format!("/tmp/toby-mcp-{}.sock", toby_config::new_id().to_lowercase());
    let version = crate::builder::runtime_version(&d.machines.config.programs.versions());
    let mut argv: Vec<String> = [
        format!("/run/toby/fs/versions/{version}/toby"),
        "guest".into(),
        "helper".into(),
        "serve-stdio".into(),
        "--socket".into(),
        socket.clone(),
        "--".into(),
    ]
    .into();
    argv.extend(command);
    let spec = SpawnSpec {
        session_id: toby_config::new_id(),
        argv,
        env,
        cwd: None,
        identity: Identity::User,
        tty: None,
        keep_after_exit: false,
        start_on_attach: false,
        tool: None,
    };
    let mut c = Control::connect(&d.machines.runtime(&machine)).await.map_err(|e| e.to_string())?;
    c.spawn(spec).await.map_err(|e| format!("starting {name}: {e}"))?;
    Ok(Splice { machine, target: Endpoint::Unix { path: socket } })
}

/// An HTTP MCP server Toby runs, and when it was last asked for.
struct Running {
    machine: String,
    session: String,
    used: std::time::Instant,
}

static HTTP_SERVERS: LazyLock<Mutex<HashMap<String, Running>>> = LazyLock::new(Default::default);

/// How long a local HTTP server may take to listen.
const LISTEN_TIMEOUT: Duration = Duration::from_secs(60);

/// Opens a connection to `port` of machine `machine`'s 127.0.0.1 through
/// its relay; nothing listens on the host.
pub async fn dial(runtime: &toby_config::paths::MachineRuntime, port: u16) -> io::Result<UnixStream> {
    use toby_proto::stream::{Dial, HostHeader};
    let header = HostHeader::Dial(Dial { target: Endpoint::Tcp { addr: format!("127.0.0.1:{port}") } });
    let (s, reply) = toby_machine::link::open_relay(&runtime.vsock(), &header).await?;
    reply.into_result().map_err(io::Error::other)?;
    Ok(s)
}

/// Where HTTP MCP server `name` listens: its services machine and port
/// (plan §16.3). It is started when first asked for, and stopped after
/// five minutes without being asked.
pub async fn http_endpoint(d: Arc<Daemon>, name: &str) -> Result<(String, u16), String> {
    let config = d.machines.current_config();
    let server = config
        .mcp
        .get(name)
        .filter(|s| s.kind == McpKind::Http && s.own_machine())
        .ok_or_else(|| format!("{name} is not an HTTP MCP server Toby runs"))?;
    server.check(name)?;
    let port = server.port.unwrap_or_default();
    // Not the machine's lock, which ensure_machine takes.
    let lock = server_lock(&format!("http {name}"));
    let _lock = lock.lock().await;
    let known = HTTP_SERVERS.lock().unwrap().get(name).map(|r| (r.machine.clone(), r.session.clone()));
    if let Some((machine, session)) = known {
        let alive = match Control::connect(&d.machines.runtime(&machine)).await {
            Ok(mut c) => {
                c.sessions().await.is_ok_and(|l| l.iter().any(|s| s.id == session && s.exit.is_none()))
            }
            Err(_) => false,
        };
        if alive {
            if let Some(r) = HTTP_SERVERS.lock().unwrap().get_mut(name) {
                r.used = std::time::Instant::now();
            }
            return Ok((machine, port));
        }
        HTTP_SERVERS.lock().unwrap().remove(name);
    }

    let (command, env) = command(&d, name, server)?;
    let machine = ensure_machine(&d, name, server).await?;
    // Its output goes where `toby mcp logs` reads.
    let log = toby_guest::helper::serve::LOG;
    let mut argv: Vec<String> = vec![
        "sh".into(),
        "-c".into(),
        format!("mkdir -p \"$(dirname \"$HOME/{log}\")\" && exec \"$@\" >>\"$HOME/{log}\" 2>&1"),
        "sh".into(),
    ];
    argv.extend(command);
    let session = SpawnSpec {
        session_id: toby_config::new_id(),
        argv,
        env,
        cwd: None,
        identity: Identity::User,
        tty: None,
        keep_after_exit: false,
        start_on_attach: false,
        tool: None,
    };
    let id = session.session_id.clone();
    let runtime = d.machines.runtime(&machine);
    let mut c = Control::connect(&runtime).await.map_err(|e| e.to_string())?;
    c.spawn(session).await.map_err(|e| format!("starting {name}: {e}"))?;
    let deadline = tokio::time::Instant::now() + LISTEN_TIMEOUT;
    while dial(&runtime, port).await.is_err() {
        if tokio::time::Instant::now() > deadline {
            let _ = d.machines.kill_session(&id, None).await;
            return Err(format!("{name} does not listen on port {port}; see toby mcp logs {name}"));
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    let running = Running { machine: machine.clone(), session: id, used: std::time::Instant::now() };
    HTTP_SERVERS.lock().unwrap().insert(name.to_string(), running);
    tokio::spawn(stop_when_unused(d, name.to_string()));
    Ok((machine, port))
}

/// Ends a local HTTP server's session when nothing has asked for it for the
/// services machines' idle time; the machine then stops by itself.
async fn stop_when_unused(d: Arc<Daemon>, name: String) {
    loop {
        tokio::time::sleep(Duration::from_secs(30)).await;
        let lock = server_lock(&format!("http {name}"));
        let _lock = lock.lock().await;
        let stale = {
            let mut servers = HTTP_SERVERS.lock().unwrap();
            match servers.get(&name) {
                Some(r) if r.used.elapsed() >= SERVICES_IDLE => servers.remove(&name),
                Some(_) => None,
                None => return,
            }
        };
        if let Some(r) = stale {
            // Hangup and terminate, then kill.
            let _ = d.machines.kill_session(&r.session, None).await;
            return;
        }
    }
}

async fn decide(d: &Daemon, spec: &MachineSpec, target: &str) -> CapResponse {
    let Some(name) = target.strip_prefix("mcp/") else {
        return refused(format!("unknown target {target:?}"));
    };
    let config = d.machines.current_config();
    if !config.mcp_reachable(spec, name) {
        return refused(format!("no tool of this machine uses the MCP server {name}"));
    }
    if name == "toby" {
        return CapResponse::Serve(Serve {});
    }
    let Some(server) = config.mcp.get(name) else {
        return refused(format!("no MCP server {name} is configured"));
    };
    if let Err(e) = server.check(name) {
        return refused(e);
    }
    match (server.kind, server.placement()) {
        (McpKind::Http, _) => refused(format!("{name} is an HTTP server; tools use its URL")),
        (McpKind::Stdio, Placement::Machine) => refused(format!("{name} runs in the tool's machine")),
        (McpKind::Stdio, Placement::Isolated) => match start_isolated(d, name, server).await {
            Ok(splice) => CapResponse::Splice(splice),
            Err(e) => refused(e),
        },
    }
}

/// One connection on `capability.sock` from a machine's host process.
pub async fn connection(d: Arc<Daemon>, mut s: UnixStream) -> io::Result<()> {
    if s.peer_cred()?.uid() != nix::unistd::getuid().as_raw() {
        return Ok(());
    }
    let header = tokio::time::timeout(HEADER_TIMEOUT, async {
        let ServiceHeader::FromMachine(from) = frame::recv(&mut s).await?;
        let CapRequest::Connect(req) = frame::recv(&mut s).await?;
        Ok::<_, toby_proto::Error>((from.machine_id, req.target))
    })
    .await
    .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no request"))??;
    let (machine, target) = header;
    let Ok(spec) = d.machines.record(&machine) else {
        frame::send(&mut s, &refused("unknown machine")).await?;
        return Ok(());
    };
    let answer = decide(&d, &spec, &target).await;
    let serve = matches!(answer, CapResponse::Serve(_));
    frame::send(&mut s, &answer).await?;
    if serve {
        crate::mcp::Server { daemon: d, machine }.serve(s).await?;
    }
    Ok(())
}

/// Accepts capability connections until the daemon stops.
pub async fn serve(d: Arc<Daemon>, listener: tokio::net::UnixListener) {
    loop {
        match listener.accept().await {
            Ok((s, _)) => {
                let d = d.clone();
                tokio::spawn(async move {
                    let _ = connection(d, s).await;
                });
            }
            Err(_) => tokio::time::sleep(Duration::from_millis(100)).await,
        }
    }
}
