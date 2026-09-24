//! Connections into the guest over Cloud Hypervisor's hybrid vsock.

use std::io;
use std::path::{Path, PathBuf};
use std::time::Duration;

use toby_proto::relay::{Request, Response};
use toby_proto::stream::{Control, HostHeader, Reply};
use toby_proto::{Message, frame, types};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;
use tokio::sync::Mutex;

/// The relay's vsock port.
pub const RELAY_PORT: u32 = 1024;

const CONNECT_TIMEOUT: Duration = Duration::from_secs(5);

/// Opens a stream to `port` in the guest through the hybrid vsock socket.
pub async fn connect_port(vsock: &Path, port: u32) -> io::Result<UnixStream> {
    tokio::time::timeout(CONNECT_TIMEOUT, async {
        let mut s = UnixStream::connect(vsock).await?;
        s.write_all(format!("CONNECT {port}\n").as_bytes()).await?;

        // The reply is a single line, `OK <host port>`.
        let mut line = Vec::with_capacity(32);
        loop {
            let b = s.read_u8().await?;
            if b == b'\n' {
                break;
            }
            if line.len() >= 64 {
                return Err(io::Error::new(io::ErrorKind::InvalidData, "overlong vsock reply"));
            }
            line.push(b);
        }
        if !line.starts_with(b"OK ") {
            return Err(io::Error::new(
                io::ErrorKind::ConnectionRefused,
                format!(
                    "guest refused vsock port {port}: {}",
                    String::from_utf8_lossy(&line)
                ),
            ));
        }
        Ok(s)
    })
    .await
    .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "vsock connect timed out"))?
}

/// Opens a relay stream with `header` and returns it with the relay's reply.
pub async fn open_relay(vsock: &Path, header: &HostHeader) -> io::Result<(UnixStream, Reply)> {
    let mut s = connect_port(vsock, RELAY_PORT).await?;
    frame::write_bytes(&mut s, &header.encode()?).await?;
    let reply: Reply = tokio::time::timeout(CONNECT_TIMEOUT, frame::recv(&mut s))
        .await
        .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "relay did not answer"))??;
    Ok((s, reply))
}

/// The relay control channel, reconnected on demand.
pub struct RelayControl {
    vsock: PathBuf,
    conn: Mutex<Option<UnixStream>>,
}

impl RelayControl {
    pub fn new(vsock: PathBuf) -> Self {
        RelayControl {
            vsock,
            conn: Mutex::new(None),
        }
    }

    async fn connect(&self) -> io::Result<UnixStream> {
        let header = HostHeader::Control(Control {
            proto_versions: types::SUPPORTED.to_vec(),
        });
        let (s, reply) = open_relay(&self.vsock, &header).await?;
        reply.into_result().map_err(io::Error::other)?;
        Ok(s)
    }

    /// Connects if needed; returns whether the relay answers.
    pub async fn ensure(&self) -> io::Result<()> {
        let mut conn = self.conn.lock().await;
        if conn.is_none() {
            *conn = Some(self.connect().await?);
        }
        Ok(())
    }

    /// Drops the current connection, e.g. after the relay announced a restart.
    pub async fn reset(&self) {
        *self.conn.lock().await = None;
    }

    /// Sends a request, reconnecting once if the channel broke.
    pub async fn call(&self, req: &Request) -> io::Result<Response> {
        let mut conn = self.conn.lock().await;
        for attempt in 0..2 {
            if conn.is_none() {
                *conn = Some(self.connect().await?);
            }
            let s = conn.as_mut().expect("connected");
            let result = async {
                frame::send(s, req).await?;
                frame::recv::<Response, _>(s).await
            }
            .await;
            match result {
                Ok(r) => return Ok(r),
                Err(e) if attempt == 0 => {
                    *conn = None;
                    let _ = e;
                }
                Err(e) => {
                    *conn = None;
                    return Err(e.into());
                }
            }
        }
        unreachable!("the loop returns on its second attempt")
    }
}
