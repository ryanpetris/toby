//! The sandbox capability's host end (plan §16.3): tobyd decides what a
//! `toby-connect` request reaches. Toby's own MCP server is served here;
//! an isolated MCP server runs in a services machine of its own, with its
//! secrets only in that process's environment, and the two machines' host
//! processes splice the connection without tobyd.

use std::io;
use std::sync::Arc;
use std::time::Duration;

use toby_config::global::{McpKind, McpServer, Placement};
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

/// Makes sure the services machine for `name` exists and runs.
async fn ensure_machine(d: &Daemon, name: &str) -> Result<String, String> {
    let pair = pair_name(name);
    let store = &d.builder.store;
    if store.home(&pair).is_err() {
        store.create_home(&pair, "mcp", 1000, 4 << 30).await.map_err(|e| e.to_string())?;
        let mut sink = |_: &[u8], _: bool| {};
        if let Err(e) = d.builder.format_home(&pair, &mut sink).await {
            let _ = store.remove_home(&pair);
            return Err(format!("preparing the home of {name}: {e}"));
        }
    }
    if store.root(&pair).is_err() {
        let image = d
            .builder
            .default_image()
            .ok()
            .flatten()
            .ok_or("there is no current default image; build it with: toby image prepare --default")?;
        store.create_root(&pair, &image.id).await.map_err(|e| e.to_string())?;
    }
    let req = toby_api::EnsureMachine {
        home: Some(pair.clone()),
        root: Some(pair),
        ephemeral: false,
        cpus: Some(SERVICES_CPUS),
        memory: Some(SERVICES_MEMORY.into()),
    };
    let spec = d.machines.ensure(req).await.map_err(|e| e.message)?;
    d.machines.set_idle_timeout(&spec.id, SERVICES_IDLE).map_err(|e| e.message)?;
    Ok(spec.id)
}

/// Starts the server for one connection in its services machine.
async fn start_isolated(d: &Daemon, name: &str, server: &McpServer) -> Result<Splice, String> {
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
    let machine = ensure_machine(d, name).await?;

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
    };
    let mut c = Control::connect(&d.machines.runtime(&machine)).await.map_err(|e| e.to_string())?;
    c.spawn(spec).await.map_err(|e| format!("starting {name}: {e}"))?;
    Ok(Splice { machine, target: Endpoint::Unix { path: socket } })
}

async fn decide(d: &Daemon, target: &str) -> CapResponse {
    let Some(name) = target.strip_prefix("mcp/") else {
        return refused(format!("unknown target {target:?}"));
    };
    if name == "toby" {
        return CapResponse::Serve(Serve {});
    }
    let config = d.machines.current_config();
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
    if d.machines.record(&machine).is_err() {
        frame::send(&mut s, &refused("unknown machine")).await?;
        return Ok(());
    }
    let answer = decide(&d, &target).await;
    let serve = matches!(answer, CapResponse::Serve(_));
    frame::send(&mut s, &answer).await?;
    if serve {
        let server = crate::mcp::Server { machines: &d.machines, approvals: &d.approvals, machine };
        server.serve(s).await?;
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
