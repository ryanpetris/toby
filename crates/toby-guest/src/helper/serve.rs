//! `serve-stdio`: runs a command for one connection on a Unix socket, the
//! connection being its stdin and stdout (isolated MCP servers, plan §16.3).

use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::time::Duration;

use tokio::net::UnixListener;

/// How long the socket waits for its one connection.
const ACCEPT_TIMEOUT: Duration = Duration::from_secs(60);

pub async fn serve_stdio(socket: &Path, argv: &[String]) -> io::Result<i32> {
    if argv.is_empty() {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "no command"));
    }
    if let Some(dir) = socket.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let _ = std::fs::remove_file(socket);
    let listener = UnixListener::bind(socket)?;
    std::fs::set_permissions(socket, std::fs::Permissions::from_mode(0o600))?;
    let accepted = tokio::time::timeout(ACCEPT_TIMEOUT, listener.accept()).await;
    let _ = std::fs::remove_file(socket);
    let (conn, _) = accepted.map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "no connection"))??;
    let conn = conn.into_std()?;
    conn.set_nonblocking(false)?;
    let stdout = std::os::fd::OwnedFd::from(conn.try_clone()?);
    let stdin = std::os::fd::OwnedFd::from(conn);
    let status = std::process::Command::new(&argv[0])
        .args(&argv[1..])
        .stdin(std::process::Stdio::from(stdin))
        .stdout(std::process::Stdio::from(stdout))
        .status()?;
    Ok(status.code().unwrap_or(1))
}
