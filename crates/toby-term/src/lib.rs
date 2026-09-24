//! Terminal handling for attached sessions: raw mode, the detach key, window
//! size changes, and the client side of the session protocol (plan §13).

use std::future::Future;
use std::io::{self, IsTerminal, Write};
use std::pin::Pin;
use std::time::Duration;

use toby_proto::session::{self, ClientFrame, ServerFrame};
use toby_proto::types::{ExitStatus, SUPPORTED};
use toby_proto::{MAX_CHUNK, frame};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite};
use tokio::net::UnixStream;
use tokio::sync::mpsc;

/// The detach prefix, Ctrl-\. Pressing it and then `d` detaches.
pub const DETACH_PREFIX: u8 = 0x1c;

/// How long a lost connection is retried before giving up.
const REATTACH_FOR: Duration = Duration::from_secs(30);

/// Restores cooked mode when dropped.
pub struct RawMode(());

impl RawMode {
    /// Puts the terminal in raw mode when stdin is a terminal.
    pub fn enable() -> io::Result<Option<RawMode>> {
        if !io::stdin().is_terminal() {
            return Ok(None);
        }
        crossterm::terminal::enable_raw_mode()?;
        let default_hook = std::panic::take_hook();
        std::panic::set_hook(Box::new(move |info| {
            let _ = crossterm::terminal::disable_raw_mode();
            default_hook(info);
        }));
        Ok(Some(RawMode(())))
    }
}

impl Drop for RawMode {
    fn drop(&mut self) {
        let _ = crossterm::terminal::disable_raw_mode();
    }
}

/// Terminal size as (rows, cols), if stdout is a terminal.
pub fn size() -> Option<(u16, u16)> {
    crossterm::terminal::size().ok().map(|(cols, rows)| (rows, cols))
}

/// Why an attachment ended.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Outcome {
    Exited(ExitStatus),
    /// Another client attached.
    Replaced(String),
    /// The user pressed the detach key.
    Detached,
}

pub trait Stream: AsyncRead + AsyncWrite + Unpin + Send + 'static {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send + 'static> Stream for T {}

/// Opens a new stream to the session, already past the stream header.
pub type Connect =
    Box<dyn Fn() -> Pin<Box<dyn Future<Output = io::Result<UnixStream>> + Send>> + Send + Sync>;

/// Splits input into bytes for the session and a detach request.
#[derive(Debug, Default)]
pub struct DetachFilter {
    pending: bool,
}

impl DetachFilter {
    /// Returns the bytes to send and whether the user asked to detach.
    pub fn feed(&mut self, input: &[u8]) -> (Vec<u8>, bool) {
        let mut out = Vec::with_capacity(input.len());
        for &b in input {
            if self.pending {
                self.pending = false;
                match b {
                    b'd' => return (out, true),
                    DETACH_PREFIX => out.push(DETACH_PREFIX),
                    other => out.extend([DETACH_PREFIX, other]),
                }
            } else if b == DETACH_PREFIX {
                self.pending = true;
            } else {
                out.push(b);
            }
        }
        (out, false)
    }
}

enum Input {
    Bytes(Vec<u8>),
    Eof,
    Resize,
}

fn spawn_input(tty: bool) -> mpsc::Receiver<Input> {
    let (tx, rx) = mpsc::channel(64);
    let stdin_tx = tx.clone();
    tokio::spawn(async move {
        let mut stdin = tokio::io::stdin();
        let mut buf = vec![0u8; 16 * 1024];
        loop {
            match stdin.read(&mut buf).await {
                Ok(0) | Err(_) => {
                    let _ = stdin_tx.send(Input::Eof).await;
                    return;
                }
                Ok(n) => {
                    if stdin_tx.send(Input::Bytes(buf[..n].to_vec())).await.is_err() {
                        return;
                    }
                }
            }
        }
    });
    if tty {
        tokio::spawn(async move {
            let Ok(mut winch) = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::window_change())
            else {
                return;
            };
            while winch.recv().await.is_some() {
                if tx.send(Input::Resize).await.is_err() {
                    return;
                }
            }
        });
    }
    rx
}

async fn hello<S: Stream>(s: &mut S, want_replay: bool) -> io::Result<session::Welcome> {
    let (rows, cols) = size().unwrap_or((0, 0));
    let hello = ClientFrame::Hello(session::Hello {
        versions: SUPPORTED.to_vec(),
        rows,
        cols,
        want_replay,
    });
    frame::send(s, &hello).await?;
    match frame::recv::<ServerFrame, _>(s).await? {
        ServerFrame::Welcome(w) => Ok(w),
        ServerFrame::Refused(r) => Err(io::Error::other(r.error)),
        other => Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("unexpected {other:?}"),
        )),
    }
}

/// Forces full-screen programs to redraw by briefly changing the size.
async fn nudge<S: Stream>(s: &mut S) -> io::Result<()> {
    if let Some((rows, cols)) = size()
        && rows > 1
    {
        frame::send(s, &ClientFrame::Resize(session::Resize { rows: rows - 1, cols })).await?;
        tokio::time::sleep(Duration::from_millis(50)).await;
        frame::send(s, &ClientFrame::Resize(session::Resize { rows, cols })).await?;
    }
    Ok(())
}

fn write_out(bytes: &[u8], stderr: bool) {
    if stderr {
        let mut e = io::stderr().lock();
        let _ = e.write_all(bytes);
        let _ = e.flush();
    } else {
        let mut o = io::stdout().lock();
        let _ = o.write_all(bytes);
        let _ = o.flush();
    }
}

/// Attaches the terminal to a session until it exits or detaches.
///
/// `redraw` asks a full-screen program to repaint after the replay (used when
/// reattaching). A lost connection is re-established with `connect` for up to
/// 30 seconds.
pub async fn attach(connect: Connect, want_replay: bool, redraw: bool) -> io::Result<Outcome> {
    let tty = io::stdin().is_terminal();
    let mut input = spawn_input(tty);
    let mut filter = DetachFilter::default();

    let mut conn = connect().await?;
    let welcome = hello(&mut conn, want_replay).await?;
    let mut remote_tty = welcome.tty;
    if redraw && remote_tty {
        nudge(&mut conn).await?;
    }

    loop {
        let (mut rd, mut wr) = conn.into_split();
        let (frames_tx, mut frames) = mpsc::channel::<io::Result<ServerFrame>>(64);
        let reader = tokio::spawn(async move {
            loop {
                let f = frame::recv::<ServerFrame, _>(&mut rd)
                    .await
                    .map_err(io::Error::from);
                let stop = f.is_err();
                if frames_tx.send(f).await.is_err() || stop {
                    return;
                }
            }
        });

        let lost: io::Error = loop {
            tokio::select! {
                f = frames.recv() => match f {
                    Some(Ok(ServerFrame::Stdout(o))) => write_out(&o.bytes, false),
                    Some(Ok(ServerFrame::Replay(r))) => write_out(&r.bytes, false),
                    Some(Ok(ServerFrame::Stderr(e))) => write_out(&e.bytes, true),
                    Some(Ok(ServerFrame::Exit(e))) => { reader.abort(); return Ok(Outcome::Exited(e.status)); }
                    Some(Ok(ServerFrame::Detached(d))) => { reader.abort(); return Ok(Outcome::Replaced(d.reason)); }
                    Some(Ok(_)) => {}
                    Some(Err(e)) => break e,
                    None => break io::Error::new(io::ErrorKind::UnexpectedEof, "session connection closed"),
                },
                i = input.recv() => {
                    let result = match i {
                        Some(Input::Bytes(b)) => {
                            let (data, detach) = if tty { filter.feed(&b) } else { (b, false) };
                            let mut r = Ok(());
                            for chunk in data.chunks(MAX_CHUNK) {
                                r = frame::send(&mut wr, &ClientFrame::Stdin(session::Stdin { bytes: chunk.to_vec() })).await;
                                if r.is_err() { break; }
                            }
                            if detach {
                                reader.abort();
                                return Ok(Outcome::Detached);
                            }
                            r
                        }
                        Some(Input::Eof) => frame::send(&mut wr, &ClientFrame::CloseStdin(session::CloseStdin {})).await,
                        Some(Input::Resize) => match size() {
                            Some((rows, cols)) if remote_tty => frame::send(&mut wr, &ClientFrame::Resize(session::Resize { rows, cols })).await,
                            _ => Ok(()),
                        },
                        None => Ok(()),
                    };
                    if let Err(e) = result {
                        break e.into();
                    }
                }
            }
        };
        reader.abort();

        // The connection broke (for example the host process restarted):
        // reattach without replay and let full-screen programs redraw.
        let deadline = tokio::time::Instant::now() + REATTACH_FOR;
        conn = loop {
            if tokio::time::Instant::now() >= deadline {
                return Err(lost);
            }
            tokio::time::sleep(Duration::from_millis(500)).await;
            let Ok(mut c) = connect().await else { continue };
            let Ok(w) = hello(&mut c, false).await else {
                continue;
            };
            remote_tty = w.tty;
            if remote_tty {
                let _ = nudge(&mut c).await;
            }
            break c;
        };
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detach_key() {
        let mut f = DetachFilter::default();
        assert_eq!(f.feed(b"ab"), (b"ab".to_vec(), false));
        assert_eq!(f.feed(&[DETACH_PREFIX]), (vec![], false));
        assert_eq!(f.feed(b"d"), (vec![], true));

        let mut f = DetachFilter::default();
        assert_eq!(
            f.feed(&[b'x', DETACH_PREFIX, b'y']),
            (vec![b'x', DETACH_PREFIX, b'y'], false)
        );
        assert_eq!(
            f.feed(&[DETACH_PREFIX, DETACH_PREFIX]),
            (vec![DETACH_PREFIX], false)
        );
        assert_eq!(f.feed(&[b'q', DETACH_PREFIX, b'd', b'z']), (vec![b'q'], true));
    }
}
