//! A client for a machine's control socket (`toby-machine`).

use std::io;
use std::time::Duration;

use toby_config::paths::MachineRuntime;
use toby_proto::frame;
use toby_proto::machine::{self, Request, Response};
use toby_proto::session::{self, ClientFrame, ServerFrame};
use toby_proto::stream::{HostHeader, Reply, SessionAttach};
use toby_proto::types::{ExitStatus, Identity, SUPPORTED, SessionInfo, SpawnSpec};
use tokio::net::UnixStream;

const CALL_TIMEOUT: Duration = Duration::from_secs(60);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

pub struct Control {
    stream: UnixStream,
}

fn unexpected(r: Response) -> io::Error {
    io::Error::other(format!("unexpected response {r:?}"))
}

impl Control {
    pub async fn connect(runtime: &MachineRuntime) -> io::Result<Control> {
        let hello = async {
            let mut stream = UnixStream::connect(runtime.control_sock()).await?;
            frame::send(&mut stream, &Request::Hello(machine::Hello { versions: SUPPORTED.to_vec() }))
                .await?;
            match frame::recv::<Response, _>(&mut stream).await? {
                Response::Welcome(_) => Ok(Control { stream }),
                Response::Failed(f) => Err(io::Error::other(f.error)),
                other => Err(unexpected(other)),
            }
        };
        tokio::time::timeout(CONNECT_TIMEOUT, hello)
            .await
            .unwrap_or_else(|_| Err(io::Error::new(io::ErrorKind::TimedOut, "the machine did not answer")))
    }

    pub async fn call(&mut self, req: Request) -> io::Result<Response> {
        let exchange = async {
            frame::send(&mut self.stream, &req).await?;
            Ok::<_, io::Error>(frame::recv::<Response, _>(&mut self.stream).await?)
        };
        match tokio::time::timeout(CALL_TIMEOUT, exchange).await {
            Ok(Ok(Response::Failed(f))) => Err(io::Error::other(f.error)),
            Ok(r) => r,
            Err(_) => Err(io::Error::new(io::ErrorKind::TimedOut, "the machine did not answer")),
        }
    }

    pub async fn sessions(&mut self) -> io::Result<Vec<SessionInfo>> {
        match self.call(Request::Sessions(machine::Sessions {})).await? {
            Response::SessionList(l) => Ok(l.sessions),
            other => Err(unexpected(other)),
        }
    }

    pub async fn spawn(&mut self, spec: SpawnSpec) -> io::Result<String> {
        match self.call(Request::Spawn(machine::Spawn { spec })).await? {
            Response::Spawned(s) => Ok(s.session_id),
            other => Err(unexpected(other)),
        }
    }

    pub async fn kill(&mut self, session_id: &str, signal: i32) -> io::Result<()> {
        self.call(Request::Kill(machine::Kill { session_id: session_id.into(), signal })).await.map(drop)
    }

    /// Asks the machine to power off.
    pub async fn stop(&mut self) -> io::Result<()> {
        self.call(Request::Stop(machine::Stop {})).await.map(drop)
    }
}

/// Runs a command in the machine to completion, streaming its output to
/// `out` (with whether it is stderr), and returns its exit status.
pub async fn run(
    runtime: &MachineRuntime,
    argv: Vec<String>,
    identity: Identity,
    env: Vec<(String, String)>,
    out: &mut (dyn FnMut(&[u8], bool) + Send),
) -> io::Result<ExitStatus> {
    run_with_input(runtime, argv, identity, env, None, out).await
}

/// Like [`run`], with `input` as the command's stdin.
pub async fn run_with_input(
    runtime: &MachineRuntime,
    argv: Vec<String>,
    identity: Identity,
    env: Vec<(String, String)>,
    input: Option<Vec<u8>>,
    out: &mut (dyn FnMut(&[u8], bool) + Send),
) -> io::Result<ExitStatus> {
    let spec = SpawnSpec {
        session_id: toby_config::new_id(),
        argv,
        env,
        cwd: None,
        identity,
        tty: None,
        keep_after_exit: true,
        start_on_attach: true,
        tool: None,
    };
    let id = Control::connect(runtime).await?.spawn(spec).await?;

    let mut s = UnixStream::connect(runtime.session_sock()).await?;
    frame::send(&mut s, &HostHeader::SessionAttach(SessionAttach { session_id: id })).await?;
    frame::recv::<Reply, _>(&mut s).await?.into_result().map_err(io::Error::other)?;
    let hello = ClientFrame::Hello(session::Hello {
        versions: SUPPORTED.to_vec(),
        rows: 0,
        cols: 0,
        want_replay: true,
        resume_from: None,
    });
    frame::send(&mut s, &hello).await?;
    if let Some(input) = input {
        for chunk in input.chunks(toby_proto::MAX_CHUNK) {
            frame::send(&mut s, &ClientFrame::Stdin(session::Stdin { bytes: chunk.to_vec() })).await?;
        }
        frame::send(&mut s, &ClientFrame::CloseStdin(session::CloseStdin {})).await?;
    }
    loop {
        match frame::recv::<ServerFrame, _>(&mut s).await? {
            ServerFrame::Stdout(o) => out(&o.bytes, false),
            ServerFrame::Stderr(e) => out(&e.bytes, true),
            ServerFrame::Replay(r) => out(&r.bytes, r.stderr),
            ServerFrame::Exit(e) => return Ok(e.status),
            ServerFrame::Refused(r) => return Err(io::Error::other(r.error)),
            _ => {}
        }
    }
}
