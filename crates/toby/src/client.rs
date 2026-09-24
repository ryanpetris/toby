//! Session commands: `toby exec`, `toby shell`, `toby attach` and
//! `toby sessions`. Sessions are created through tobyd; their bytes and
//! signals go straight to the machine (plan §13.3).

use std::path::PathBuf;
use std::process::ExitCode;

use anyhow::bail;
use toby_proto::frame;
use toby_proto::machine::{self, Request, Response};
use toby_proto::stream::{HostHeader, Reply, SessionAttach};
use toby_proto::types::{ExitStatus, Identity, SUPPORTED, TtySize};
use toby_term::Outcome;
use tokio::net::UnixStream;

use crate::api::Api;
use crate::cli::MachineSelector;

/// Sends a signal to a session over its machine's control socket, which
/// works whether or not tobyd is running.
async fn signal(control: &PathBuf, session_id: String, signal: i32) -> std::io::Result<()> {
    let mut s = UnixStream::connect(control).await?;
    frame::send(&mut s, &Request::Hello(machine::Hello { versions: SUPPORTED.to_vec() })).await?;
    frame::recv::<Response, _>(&mut s).await?;
    frame::send(&mut s, &Request::Kill(machine::Kill { session_id, signal })).await?;
    match frame::recv::<Response, _>(&mut s).await? {
        Response::Failed(f) => Err(std::io::Error::other(f.error)),
        _ => Ok(()),
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

pub async fn attach_terminal(
    session_sock: PathBuf,
    control_sock: PathBuf,
    session_id: &str,
    replay: bool,
    redraw: bool,
) -> anyhow::Result<ExitCode> {
    // Signals travel over the machine's control socket, so they arrive even
    // while the session's input is backed up.
    let signals: toby_term::SignalSink = {
        let id = session_id.to_string();
        Box::new(move |sig| {
            let control = control_sock.clone();
            let id = id.clone();
            Box::pin(async move { signal(&control, id, sig).await })
        })
    };
    let outcome =
        toby_term::attach(connector(session_sock, session_id.to_string()), replay, redraw, signals).await;
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

pub fn selector(sel: &MachineSelector) -> toby_api::MachineSelector {
    toby_api::MachineSelector { machine: sel.machine.clone(), home: sel.home.clone(), root: sel.root.clone() }
}

/// Starts `argv` in the selected machine and attaches to it.
pub async fn run_session(
    sel: &MachineSelector,
    argv: Vec<String>,
    identity: Identity,
    cwd: Option<String>,
) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let tty = toby_term::local_tty();
    let mut env = Vec::new();
    if tty && let Ok(term) = std::env::var("TERM") {
        env.push(("TERM".to_string(), term));
    }
    let req = toby_api::CreateSession {
        request_id: Some(toby_config::new_id()),
        target: selector(sel),
        tool: None,
        yolo: false,
        attachments: Vec::new(),
        argv,
        env,
        cwd,
        identity,
        tty: tty.then(|| {
            let (rows, cols) = toby_term::size().unwrap_or((24, 80));
            TtySize { rows, cols }
        }),
    };
    let created: toby_api::SessionCreated = api.post_again("/v1/sessions", &req).await?;
    api.warn(&created.warnings);
    let notices = crate::approvals::notices(std::sync::Arc::new(api), created.machine.clone());
    let result = attach_terminal(
        created.session_socket.into(),
        created.control_socket.into(),
        &created.id,
        true,
        false,
    )
    .await;
    notices.abort();
    result
}

/// `toby attach [<session>]`.
pub async fn attach(session: Option<String>) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let sessions: Vec<toby_api::MachineSession> = api.get("/v1/sessions").await?;
    let mut candidates: Vec<_> = sessions
        .into_iter()
        .filter(|s| match &session {
            Some(id) => &s.session.id == id,
            None => !s.session.attached,
        })
        .collect();
    match candidates.len() {
        0 => match session {
            Some(id) => bail!("no session {id}"),
            None => bail!("no detached session"),
        },
        1 => {
            let s = candidates.remove(0);
            attach_terminal(s.session_socket.into(), s.control_socket.into(), &s.session.id, true, true).await
        }
        _ => bail!(
            "several detached sessions are running; choose one: {}",
            candidates.iter().map(|s| s.session.id.as_str()).collect::<Vec<_>>().join(", ")
        ),
    }
}

/// `toby sessions ls`.
pub async fn list() -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let sessions: Vec<toby_api::MachineSession> = api.get("/v1/sessions").await?;
    let rows = sessions
        .into_iter()
        .map(|s| {
            let state = match s.session.exit {
                None => "running".to_string(),
                Some(ExitStatus::Code(c)) => format!("exited {c}"),
                Some(ExitStatus::Signal(n)) => format!("killed by signal {n}"),
            };
            let attached = if s.session.attached { "yes" } else { "no" };
            let version = s.session.version.unwrap_or_default();
            [s.session.id, s.machine, s.session.argv0, attached.into(), state, version]
        })
        .collect();
    crate::table::print(["SESSION", "MACHINE", "COMMAND", "ATTACHED", "STATE", "VERSION"], rows);
    Ok(ExitCode::SUCCESS)
}

/// `toby sessions kill <id>`.
pub async fn kill(id: &str) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let () = api
        .post(&format!("/v1/sessions/{}/kill", crate::api::segment(id)), &toby_api::KillSession::default())
        .await?;
    Ok(ExitCode::SUCCESS)
}
