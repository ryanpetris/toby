//! A client for a machine's control socket (`toby-machine`).

use std::io;
use std::time::Duration;

use toby_config::paths::MachineRuntime;
use toby_proto::frame;
use toby_proto::machine::{self, Request, Response};
use toby_proto::types::{SUPPORTED, SessionInfo, SpawnSpec};
use tokio::net::UnixStream;

const CALL_TIMEOUT: Duration = Duration::from_secs(60);

pub struct Control {
    stream: UnixStream,
}

fn unexpected(r: Response) -> io::Error {
    io::Error::other(format!("unexpected response {r:?}"))
}

impl Control {
    pub async fn connect(runtime: &MachineRuntime) -> io::Result<Control> {
        let mut stream = UnixStream::connect(runtime.control_sock()).await?;
        frame::send(&mut stream, &Request::Hello(machine::Hello { versions: SUPPORTED.to_vec() })).await?;
        match frame::recv::<Response, _>(&mut stream).await? {
            Response::Welcome(_) => Ok(Control { stream }),
            Response::Failed(f) => Err(io::Error::other(f.error)),
            other => Err(unexpected(other)),
        }
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
