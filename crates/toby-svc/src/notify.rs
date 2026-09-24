//! systemd readiness notification (`sd_notify`) without libsystemd.

use std::io;
use std::os::linux::net::SocketAddrExt;
use std::os::unix::net::{SocketAddr, UnixDatagram};

/// Sends `state` (for example `READY=1`) to `$NOTIFY_SOCKET`. Does nothing
/// when the process was not started by systemd with notification enabled.
pub fn notify(state: &str) -> io::Result<()> {
    let Some(path) = std::env::var_os("NOTIFY_SOCKET") else {
        return Ok(());
    };
    let path = path.to_string_lossy().into_owned();
    let addr = match path.strip_prefix('@') {
        Some(name) => SocketAddr::from_abstract_name(name.as_bytes())?,
        None => SocketAddr::from_pathname(&path)?,
    };
    let sock = UnixDatagram::unbound()?;
    sock.send_to_addr(state.as_bytes(), &addr)?;
    Ok(())
}

/// Reports that the service finished starting.
pub fn ready() {
    let _ = notify("READY=1");
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sends_to_the_notify_socket() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("notify");
        let server = UnixDatagram::bind(&path).unwrap();
        // SAFETY: tests in this module do not read the environment concurrently.
        unsafe { std::env::set_var("NOTIFY_SOCKET", &path) };
        notify("READY=1").unwrap();
        unsafe { std::env::remove_var("NOTIFY_SOCKET") };
        let mut buf = [0u8; 16];
        let n = server.recv(&mut buf).unwrap();
        assert_eq!(&buf[..n], b"READY=1");
    }
}
