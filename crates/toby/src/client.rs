//! Session commands: `toby exec`, `toby shell`, `toby attach` and
//! `toby sessions`, talking to a machine's host process.

use std::path::PathBuf;
use std::process::ExitCode;

use anyhow::{Context, bail};
use toby_config::paths::{MachineRuntime, Paths};
use toby_proto::frame;
use toby_proto::machine::{self, Request, Response};
use toby_proto::stream::{HostHeader, Reply, SessionAttach};
use toby_proto::types::{ExitStatus, Identity, SUPPORTED, SessionInfo, SpawnSpec, TtySize};
use toby_term::Outcome;
use tokio::net::UnixStream;

use crate::cli::MachineSelector;
use crate::internal::load_config;

/// A connection to a machine's control socket.
pub struct Control {
    stream: UnixStream,
}

impl Control {
    pub async fn connect(runtime: &MachineRuntime) -> anyhow::Result<Control> {
        let mut stream = UnixStream::connect(runtime.control_sock())
            .await
            .with_context(|| format!("connecting to {}", runtime.control_sock().display()))?;
        frame::send(&mut stream, &Request::Hello(machine::Hello { versions: SUPPORTED.to_vec() })).await?;
        match frame::recv::<Response, _>(&mut stream).await? {
            Response::Welcome(_) => Ok(Control { stream }),
            Response::Failed(f) => bail!("machine refused the connection: {}", f.error),
            other => bail!("unexpected response {other:?}"),
        }
    }

    pub async fn call(&mut self, req: Request) -> anyhow::Result<Response> {
        frame::send(&mut self.stream, &req).await?;
        match frame::recv::<Response, _>(&mut self.stream).await? {
            Response::Failed(f) => bail!("{}", f.error),
            r => Ok(r),
        }
    }

    pub async fn sessions(&mut self) -> anyhow::Result<Vec<SessionInfo>> {
        match self.call(Request::Sessions(machine::Sessions {})).await? {
            Response::SessionList(l) => Ok(l.sessions),
            other => bail!("unexpected response {other:?}"),
        }
    }
}

/// Machines whose host process answers, as (ID, runtime).
async fn running_machines(paths: &Paths) -> Vec<(String, MachineRuntime)> {
    let Ok(entries) = std::fs::read_dir(paths.machines_runtime()) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for e in entries.flatten() {
        let id = e.file_name().to_string_lossy().into_owned();
        let rt = paths.machine_runtime(&id);
        if Control::connect(&rt).await.is_ok() {
            out.push((id, rt));
        }
    }
    out.sort_by(|a, b| a.0.cmp(&b.0));
    out
}

/// Resolves the machine a command targets.
pub async fn select(paths: &Paths, sel: &MachineSelector) -> anyhow::Result<(String, MachineRuntime)> {
    if let Some(id) = &sel.machine {
        return Ok((id.clone(), paths.machine_runtime(id)));
    }
    if sel.home.is_some() || sel.root.is_some() {
        bail!("selecting a machine by home and root needs the Toby daemon; use --machine");
    }
    let mut running = running_machines(paths).await;
    match running.len() {
        0 => bail!("no machine is running"),
        1 => Ok(running.remove(0)),
        _ => bail!(
            "several machines are running; choose one with --machine: {}",
            running.iter().map(|(id, _)| id.as_str()).collect::<Vec<_>>().join(", ")
        ),
    }
}

fn connector(session_sock: PathBuf, session_id: String) -> toby_term::Connect {
    Box::new(move || {
        let sock = session_sock.clone();
        let id = session_id.clone();
        Box::pin(async move {
            let mut s = UnixStream::connect(&sock).await?;
            frame::send(&mut s, &HostHeader::SessionAttach(SessionAttach { session_id: id })).await?;
            let reply: Reply = frame::recv(&mut s).await?;
            reply.into_result().map_err(std::io::Error::other)?;
            Ok(s)
        })
    })
}

fn exit_code(status: ExitStatus) -> ExitCode {
    ExitCode::from(status.code().clamp(0, 255) as u8)
}

async fn attach_terminal(
    runtime: &MachineRuntime,
    session_id: &str,
    replay: bool,
    redraw: bool,
) -> anyhow::Result<ExitCode> {
    // Signals for the session travel over the machine's control socket, so
    // they arrive even while the session's input is backed up.
    let signals: toby_term::SignalSink = {
        let runtime = runtime.clone();
        let id = session_id.to_string();
        Box::new(move |signal| {
            let runtime = runtime.clone();
            let session_id = id.clone();
            Box::pin(async move {
                let mut c = Control::connect(&runtime).await.map_err(std::io::Error::other)?;
                c.call(Request::Kill(machine::Kill { session_id, signal }))
                    .await
                    .map_err(std::io::Error::other)?;
                Ok(())
            })
        })
    };
    let outcome =
        toby_term::attach(connector(runtime.session_sock(), session_id.to_string()), replay, redraw, signals)
            .await;
    Ok(match outcome? {
        Outcome::Exited(status) => exit_code(status),
        Outcome::Replaced(reason) => {
            eprintln!("\ntoby: detached ({reason})");
            ExitCode::SUCCESS
        }
        Outcome::Detached => {
            eprintln!("\ntoby: detached; reattach with: toby attach {session_id}");
            ExitCode::SUCCESS
        }
    })
}

/// Starts `argv` in the selected machine and attaches to it.
pub async fn run_session(
    sel: &MachineSelector,
    argv: Vec<String>,
    identity: Identity,
    cwd: Option<String>,
) -> anyhow::Result<ExitCode> {
    let (_, paths) = load_config()?;
    let (_, runtime) = select(&paths, sel).await?;
    let mut control = Control::connect(&runtime).await?;

    let tty = toby_term::local_tty();
    let mut env = Vec::new();
    if tty && let Ok(term) = std::env::var("TERM") {
        env.push(("TERM".to_string(), term));
    }
    let spec = SpawnSpec {
        session_id: toby_config::new_id(),
        argv,
        env,
        cwd,
        identity,
        tty: tty.then(|| {
            let (rows, cols) = toby_term::size().unwrap_or((24, 80));
            TtySize { rows, cols }
        }),
        keep_after_exit: true,
        start_on_attach: true,
    };
    let id = match control.call(Request::Spawn(machine::Spawn { spec })).await? {
        Response::Spawned(s) => s.session_id,
        other => bail!("unexpected response {other:?}"),
    };
    attach_terminal(&runtime, &id, true, false).await
}

/// `toby attach [<session>]`.
pub async fn attach(session: Option<String>) -> anyhow::Result<ExitCode> {
    let (_, paths) = load_config()?;
    let mut candidates = Vec::new();
    for (machine, runtime) in running_machines(&paths).await {
        let Ok(mut c) = Control::connect(&runtime).await else {
            continue;
        };
        for s in c.sessions().await.unwrap_or_default() {
            let wanted = match &session {
                Some(id) => &s.id == id,
                None => !s.attached,
            };
            if wanted {
                candidates.push((machine.clone(), runtime.clone(), s));
            }
        }
    }
    match candidates.len() {
        0 => match session {
            Some(id) => bail!("no session {id}"),
            None => bail!("no detached session"),
        },
        1 => {
            let (_, runtime, s) = candidates.remove(0);
            attach_terminal(&runtime, &s.id, true, true).await
        }
        _ => bail!(
            "several detached sessions are running; choose one: {}",
            candidates.iter().map(|(_, _, s)| s.id.as_str()).collect::<Vec<_>>().join(", ")
        ),
    }
}

/// `toby sessions ls`.
pub async fn list() -> anyhow::Result<ExitCode> {
    let (_, paths) = load_config()?;
    println!("{:<26}  {:<26}  {:<16}  {:<10}  STATE", "SESSION", "MACHINE", "COMMAND", "ATTACHED");
    for (machine, runtime) in running_machines(&paths).await {
        let Ok(mut c) = Control::connect(&runtime).await else {
            continue;
        };
        for s in c.sessions().await? {
            let state = match s.exit {
                None => "running".to_string(),
                Some(ExitStatus::Code(c)) => format!("exited {c}"),
                Some(ExitStatus::Signal(n)) => format!("killed by signal {n}"),
            };
            let attached = if s.attached { "yes" } else { "no" };
            println!("{:<26}  {:<26}  {:<16}  {:<10}  {state}", s.id, machine, s.argv0, attached);
        }
    }
    Ok(ExitCode::SUCCESS)
}

/// `toby sessions kill <id>`: hangup and terminate, then kill if the
/// session is still running after a grace period (interactive shells ignore
/// SIGTERM).
pub async fn kill(id: &str) -> anyhow::Result<ExitCode> {
    let (_, paths) = load_config()?;
    for (_, runtime) in running_machines(&paths).await {
        let Ok(mut c) = Control::connect(&runtime).await else {
            continue;
        };
        if !c.sessions().await?.iter().any(|s| s.id == id) {
            continue;
        }
        let running = |list: &[SessionInfo]| list.iter().any(|s| s.id == id && s.exit.is_none());
        for signal in [libc::SIGHUP, libc::SIGTERM] {
            c.call(Request::Kill(machine::Kill { session_id: id.to_string(), signal })).await?;
        }
        for _ in 0..30 {
            if !running(&c.sessions().await?) {
                return Ok(ExitCode::SUCCESS);
            }
            tokio::time::sleep(std::time::Duration::from_millis(100)).await;
        }
        c.call(Request::Kill(machine::Kill { session_id: id.to_string(), signal: libc::SIGKILL })).await?;
        return Ok(ExitCode::SUCCESS);
    }
    bail!("no session {id}")
}
