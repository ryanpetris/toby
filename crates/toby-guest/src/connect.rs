//! `toby-connect <target>`: connects its stdin and stdout to a Toby service,
//! such as an MCP server (plan §16.3), through the sandbox socket.

use std::io;
use std::path::Path;

use toby_proto::capability::{CapRequest, Connect};
use toby_proto::frame;
use toby_proto::stream::Reply;
use tokio::io::AsyncWriteExt;
use tokio::net::UnixStream;

/// The guest end of the sandbox capability.
pub const SANDBOX_SOCKET: &str = "/run/toby/sandbox.sock";

pub async fn run(socket: &Path, target: &str) -> io::Result<()> {
    let mut s = UnixStream::connect(socket)
        .await
        .map_err(|e| io::Error::new(e.kind(), format!("{}: {e}", socket.display())))?;
    frame::send(&mut s, &CapRequest::Connect(Connect { target: target.into() })).await?;
    frame::recv::<Reply, _>(&mut s).await?.into_result().map_err(io::Error::other)?;
    let (mut read, mut write) = s.into_split();
    let to_service = async {
        tokio::io::copy(&mut tokio::io::stdin(), &mut write).await?;
        write.shutdown().await
    };
    let from_service = async {
        let mut out = tokio::io::stdout();
        tokio::io::copy(&mut read, &mut out).await?;
        out.flush().await
    };
    // Done when the service closes its side; input may still be open.
    tokio::select! {
        r = from_service => r,
        r = async { to_service.await?; std::future::pending::<io::Result<()>>().await } => r,
    }
}
