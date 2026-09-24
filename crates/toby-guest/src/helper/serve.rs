//! `serve-stdio`: runs a command for one connection on a Unix socket, the
//! connection being its stdin and stdout (isolated MCP servers, plan §16.3).

use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::time::Duration;

use tokio::net::UnixListener;

/// How long the socket waits for its one connection.
const ACCEPT_TIMEOUT: Duration = Duration::from_secs(60);
/// The command's standard error, relative to the home (`toby mcp logs`).
pub const LOG: &str = ".local/state/toby-mcp.log";
/// Size at which the log starts over, keeping the previous one.
const LOG_LIMIT: u64 = 1 << 20;

/// The log to append the command's standard error to, or none.
fn log_file() -> Option<std::fs::File> {
    let home = std::env::var_os("HOME")?;
    let path = Path::new(&home).join(LOG);
    std::fs::create_dir_all(path.parent()?).ok()?;
    if std::fs::metadata(&path).is_ok_and(|m| m.len() > LOG_LIMIT) {
        let _ = std::fs::rename(&path, path.with_extension("log.1"));
    }
    std::fs::OpenOptions::new().create(true).append(true).open(path).ok()
}

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
    let mut cmd = std::process::Command::new(&argv[0]);
    cmd.args(&argv[1..]).stdin(std::process::Stdio::from(stdin)).stdout(std::process::Stdio::from(stdout));
    if let Some(log) = log_file() {
        cmd.stderr(log);
    }
    let status = cmd.status()?;
    Ok(status.code().unwrap_or(1))
}
