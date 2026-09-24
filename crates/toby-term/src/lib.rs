//! Terminal handling for attached sessions: raw mode, the detach key, window
//! size changes, and the client side of the session protocol (plan §13).

use std::future::Future;
use std::io::{self, IsTerminal, Write};
use std::pin::Pin;
use std::time::Duration;

use std::sync::Arc;
use toby_proto::session::{self, ClientFrame, ServerFrame};
use toby_proto::types::{ExitStatus, SUPPORTED};
use toby_proto::{MAX_CHUNK, frame};

use tokio::io::{AsyncRead, AsyncWrite};
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

/// DEC private modes whose state is restored when an attachment ends.
const TRACKED_MODES: &[u16] = &[47, 1047, 1049, 1000, 1002, 1003, 1004, 1005, 1006, 1015, 2004];

/// Follows the terminal modes a session enables in its output, so that the
/// user's terminal can be put back when the attachment ends.
#[derive(Debug, Default)]
pub struct Modes {
    enabled: std::collections::BTreeSet<u16>,
    cursor_hidden: bool,
    keyboard_pushes: u32,
    state: ParseState,
    params: Vec<u8>,
}

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
enum ParseState {
    #[default]
    Ground,
    Escape,
    Csi,
}

impl Modes {
    /// Scans output bytes; sequences may be split across calls.
    pub fn feed(&mut self, bytes: &[u8]) {
        for &b in bytes {
            match self.state {
                ParseState::Ground => {
                    if b == 0x1b {
                        self.state = ParseState::Escape;
                    }
                }
                ParseState::Escape => {
                    self.state = if b == b'[' { ParseState::Csi } else { ParseState::Ground };
                    self.params.clear();
                }
                ParseState::Csi => {
                    if (0x40..=0x7e).contains(&b) {
                        self.csi(b);
                        self.state = ParseState::Ground;
                    } else if self.params.len() < 32 {
                        self.params.push(b);
                    } else {
                        self.state = ParseState::Ground;
                    }
                }
            }
        }
    }

    fn numbers(rest: &[u8]) -> Vec<u16> {
        rest.split(|&c| c == b';').filter_map(|n| std::str::from_utf8(n).ok()?.parse().ok()).collect()
    }

    fn csi(&mut self, fin: u8) {
        let (lead, rest) = match self.params.first() {
            Some(&c @ (b'?' | b'>' | b'<' | b'=')) => (Some(c), &self.params[1..]),
            _ => (None, &self.params[..]),
        };
        match (lead, fin) {
            (Some(b'?'), b'h' | b'l') => {
                let on = fin == b'h';
                for n in Self::numbers(rest) {
                    if n == 25 {
                        self.cursor_hidden = !on;
                    } else if TRACKED_MODES.contains(&n) {
                        if on {
                            self.enabled.insert(n);
                        } else {
                            self.enabled.remove(&n);
                        }
                    }
                }
            }
            (Some(b'>'), b'u') => self.keyboard_pushes += 1,
            (Some(b'<'), b'u') => {
                let n = Self::numbers(rest).first().copied().unwrap_or(1) as u32;
                self.keyboard_pushes = self.keyboard_pushes.saturating_sub(n.max(1));
            }
            _ => {}
        }
    }

    /// Sequences that turn off every mode the session left on.
    pub fn restore_sequence(&self) -> Vec<u8> {
        let mut out = Vec::new();
        // Leave the alternate screen first so the rest applies to the main screen.
        for n in self.enabled.iter().rev() {
            out.extend(format!("\x1b[?{n}l").as_bytes());
        }
        if self.cursor_hidden {
            out.extend(b"\x1b[?25h");
        }
        if self.keyboard_pushes > 0 {
            out.extend(format!("\x1b[<{}u", self.keyboard_pushes).as_bytes());
        }
        out.extend(b"\x1b[0m");
        out
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

/// Events for the attachment loop.
enum Event {
    /// The user pressed the detach key.
    Detach,
    Resize,
    Signal(i32),
}

/// Frames the writer task sends; stdin and its end go through their own
/// queue so window size changes never wait behind input.
enum Data {
    Stdin(Vec<u8>),
    Close,
}

/// Delivers a signal to the session outside its stream.
pub type SignalSink = Box<dyn Fn(i32) -> Pin<Box<dyn Future<Output = io::Result<()>> + Send>> + Send + Sync>;

/// Reads standard input on a plain thread: a read blocked on the terminal
/// must not keep the runtime from shutting down when the session ends. The
/// detach key is recognised here.
fn spawn_stdin(interactive: bool, data: mpsc::Sender<Data>, events: mpsc::Sender<Event>) {
    std::thread::spawn(move || {
        use std::io::Read;
        let mut filter = DetachFilter::default();
        let mut stdin = io::stdin().lock();
        let mut buf = vec![0u8; 16 * 1024];
        loop {
            let n = match stdin.read(&mut buf) {
                Ok(0) | Err(_) => {
                    let _ = data.blocking_send(Data::Close);
                    return;
                }
                Ok(n) => n,
            };
            let (bytes, detach) =
                if interactive { filter.feed(&buf[..n]) } else { (buf[..n].to_vec(), false) };
            if !bytes.is_empty() && data.blocking_send(Data::Stdin(bytes)).is_err() {
                return;
            }
            if detach {
                let _ = events.blocking_send(Event::Detach);
                return;
            }
        }
    });
}

fn spawn_signals(interactive: bool, events: &mpsc::Sender<Event>) {
    use tokio::signal::unix::{SignalKind, signal};
    if interactive {
        let tx = events.clone();
        tokio::spawn(async move {
            let Ok(mut winch) = signal(SignalKind::window_change()) else { return };
            while winch.recv().await.is_some() {
                if tx.send(Event::Resize).await.is_err() {
                    return;
                }
            }
        });
    } else {
        for (kind, number) in [
            (SignalKind::interrupt(), libc::SIGINT),
            (SignalKind::terminate(), libc::SIGTERM),
            (SignalKind::hangup(), libc::SIGHUP),
            (SignalKind::quit(), libc::SIGQUIT),
        ] {
            let tx = events.clone();
            tokio::spawn(async move {
                let Ok(mut s) = signal(kind) else { return };
                while s.recv().await.is_some() {
                    if tx.send(Event::Signal(number)).await.is_err() {
                        return;
                    }
                }
            });
        }
    }
}

async fn hello<S: Stream>(
    s: &mut S,
    want_replay: bool,
    resume_from: Option<u64>,
) -> io::Result<session::Welcome> {
    let (rows, cols) = size().unwrap_or((0, 0));
    let hello = ClientFrame::Hello(session::Hello {
        versions: SUPPORTED.to_vec(),
        rows,
        cols,
        want_replay,
        resume_from,
    });
    frame::send(s, &hello).await?;
    match frame::recv::<ServerFrame, _>(s).await? {
        ServerFrame::Welcome(w) => Ok(w),
        ServerFrame::Refused(r) => Err(io::Error::other(r.error)),
        other => Err(io::Error::new(io::ErrorKind::InvalidData, format!("unexpected {other:?}"))),
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

/// Writes session output; a failure here means the output was not delivered
/// and ends the attachment with an error.
fn write_out(bytes: &[u8], stderr: bool) -> io::Result<()> {
    if stderr {
        let mut e = io::stderr().lock();
        e.write_all(bytes)?;
        e.flush()
    } else {
        let mut o = io::stdout().lock();
        o.write_all(bytes)?;
        o.flush()
    }
}

/// Whether this process's terminal can act as the session's terminal: both
/// standard input and output must be terminals.
pub fn local_tty() -> bool {
    io::stdin().is_terminal() && io::stdout().is_terminal()
}

/// Attaches to a session until it exits or detaches.
///
/// With a local terminal (see [`local_tty`]) and a session that has one, the
/// terminal is in raw mode for the attachment, the detach key works and size
/// changes are forwarded. For a session without a terminal, interrupt,
/// termination, hangup and quit signals received by this process go to the
/// session through `signals`. `redraw` asks a full-screen program to repaint
/// after the replay (used when reattaching). A lost connection is
/// re-established with `connect` for up to 30 seconds, resuming the output
/// where it stopped; if output was lost meanwhile, a session without a
/// terminal ends the attachment with an error.
pub async fn attach(
    connect: Connect,
    want_replay: bool,
    redraw: bool,
    signals: SignalSink,
) -> io::Result<Outcome> {
    let mut conn = connect().await?;
    let welcome = hello(&mut conn, want_replay, None).await?;
    let interactive = welcome.tty && local_tty();

    let raw = if interactive { RawMode::enable()? } else { None };
    let (data_tx, data_rx) = mpsc::channel::<Data>(64);
    let (events_tx, events_rx) = mpsc::channel::<Event>(64);
    spawn_stdin(interactive, data_tx, events_tx.clone());
    spawn_signals(interactive, &events_tx);

    if redraw && interactive {
        nudge(&mut conn).await?;
    }
    let mut state = Attached {
        interactive,
        tty: welcome.tty,
        offset: welcome.offset,
        lost: welcome.lost,
        input: welcome.input,
        input_closed: welcome.input_closed,
        sent: Arc::new(Sent::default()),
        modes: Modes::default(),
        data: Arc::new(tokio::sync::Mutex::new(data_rx)),
        events: events_rx,
    };
    let result = state.run(conn, connect, signals).await;
    if interactive {
        let mut o = io::stdout().lock();
        let _ = o.write_all(&state.modes.restore_sequence());
        let _ = o.flush();
    }
    drop(raw);
    result
}

struct Attached {
    interactive: bool,
    tty: bool,
    /// Output offset of the next byte expected from the session.
    offset: u64,
    /// Output bytes that were lost across reconnections.
    lost: u64,
    /// Standard input the session had received when this client attached.
    input: u64,
    input_closed: bool,
    /// Standard input this client has sent.
    sent: Arc<Sent>,
    modes: Modes,
    data: Arc<tokio::sync::Mutex<mpsc::Receiver<Data>>>,
    events: mpsc::Receiver<Event>,
}

impl Attached {
    async fn run(
        &mut self,
        mut conn: UnixStream,
        connect: Connect,
        signals: SignalSink,
    ) -> io::Result<Outcome> {
        loop {
            let (mut rd, wr) = conn.into_split();
            let (ctl_tx, ctl_rx) = mpsc::channel::<ClientFrame>(16);
            let writer = tokio::spawn(write_frames(wr, ctl_rx, self.data.clone(), self.sent.clone()));
            let (frames_tx, mut frames) = mpsc::channel::<io::Result<ServerFrame>>(64);
            let reader = tokio::spawn(async move {
                loop {
                    let f = frame::recv::<ServerFrame, _>(&mut rd).await.map_err(io::Error::from);
                    let stop = f.is_err();
                    if frames_tx.send(f).await.is_err() || stop {
                        return;
                    }
                }
            });

            let outcome = self.pump(&mut frames, &ctl_tx, &signals).await;
            reader.abort();
            writer.abort();
            match outcome {
                Pumped::Done(r) => return r,
                Pumped::Lost(lost) => {
                    // The connection broke (for example the host process
                    // restarted): reattach and resume the output.
                    conn = self.reconnect(&connect, lost).await?;
                }
            }
        }
    }

    async fn pump(
        &mut self,
        frames: &mut mpsc::Receiver<io::Result<ServerFrame>>,
        ctl: &mpsc::Sender<ClientFrame>,
        signals: &SignalSink,
    ) -> Pumped {
        loop {
            tokio::select! {
                f = frames.recv() => {
                    let written = match f {
                        Some(Ok(ServerFrame::Stdout(o))) => self.output(&o.bytes, false),
                        Some(Ok(ServerFrame::Replay(r))) => self.output(&r.bytes, r.stderr),
                        Some(Ok(ServerFrame::Stderr(e))) => self.output(&e.bytes, true),
                        Some(Ok(ServerFrame::Exit(e))) => return Pumped::Done(self.exited(e.status)),
                        Some(Ok(ServerFrame::Detached(d))) => return Pumped::Done(Ok(Outcome::Replaced(d.reason))),
                        Some(Ok(_)) => Ok(()),
                        Some(Err(e)) => return Pumped::Lost(e),
                        None => return Pumped::Lost(io::Error::new(io::ErrorKind::UnexpectedEof, "session connection closed")),
                    };
                    if let Err(e) = written {
                        return Pumped::Done(Err(e));
                    }
                }
                e = self.events.recv() => match e {
                    Some(Event::Detach) => return Pumped::Done(Ok(Outcome::Detached)),
                    Some(Event::Resize) => {
                        if let Some((rows, cols)) = size() {
                            let _ = ctl.try_send(ClientFrame::Resize(session::Resize { rows, cols }));
                        }
                    }
                    Some(Event::Signal(n)) => {
                        if let Err(e) = signals(n).await {
                            return Pumped::Done(Err(e));
                        }
                    }
                    None => {}
                },
            }
        }
    }

    fn output(&mut self, bytes: &[u8], stderr: bool) -> io::Result<()> {
        self.offset += bytes.len() as u64;
        if !stderr {
            self.modes.feed(bytes);
        }
        write_out(bytes, stderr)
    }

    fn exited(&self, status: ExitStatus) -> io::Result<Outcome> {
        if self.lost > 0 && !self.tty {
            return Err(io::Error::other(format!(
                "{} bytes of the command's output were lost while reconnecting",
                self.lost
            )));
        }
        Ok(Outcome::Exited(status))
    }

    async fn reconnect(&mut self, connect: &Connect, lost: io::Error) -> io::Result<UnixStream> {
        let deadline = tokio::time::Instant::now() + REATTACH_FOR;
        loop {
            if tokio::time::Instant::now() >= deadline {
                return Err(lost);
            }
            tokio::select! {
                _ = tokio::time::sleep(Duration::from_millis(500)) => {}
                e = self.events.recv() => match e {
                    Some(Event::Detach) | Some(Event::Signal(_)) => {
                        return Err(io::Error::new(io::ErrorKind::Interrupted, "interrupted while reconnecting"));
                    }
                    _ => continue,
                },
            }
            let Ok(mut c) = connect().await else { continue };
            let Ok(w) = hello(&mut c, false, Some(self.offset)).await else { continue };
            // Input is not resent: without a terminal, any input that did not
            // arrive makes the command's result unreliable.
            let sent = self.input + self.sent.bytes.load(std::sync::atomic::Ordering::Acquire);
            let closed = self.input_closed || self.sent.closed.load(std::sync::atomic::Ordering::Acquire);
            if !self.tty && (w.input != sent || w.input_closed != closed) {
                return Err(io::Error::other("standard input was lost while reconnecting"));
            }
            self.offset = w.offset;
            self.lost += w.lost;
            if w.lost > 0 && self.interactive {
                let _ = write_out(
                    format!("\r\n[toby: {} bytes of output were lost]\r\n", w.lost).as_bytes(),
                    true,
                );
            }
            if self.interactive {
                let _ = nudge(&mut c).await;
            }
            return Ok(c);
        }
    }
}

enum Pumped {
    Done(io::Result<Outcome>),
    Lost(io::Error),
}

/// Standard input written to the session connection.
#[derive(Default)]
struct Sent {
    bytes: std::sync::atomic::AtomicU64,
    closed: std::sync::atomic::AtomicBool,
}

/// Sends control frames ahead of queued input.
async fn write_frames(
    mut wr: tokio::net::unix::OwnedWriteHalf,
    mut ctl: mpsc::Receiver<ClientFrame>,
    data: Arc<tokio::sync::Mutex<mpsc::Receiver<Data>>>,
    sent: Arc<Sent>,
) {
    let mut data = data.lock().await;
    let mut data_open = true;
    loop {
        let f = tokio::select! {
            biased;
            c = ctl.recv() => match c {
                Some(f) => f,
                None => return,
            },
            d = data.recv(), if data_open => match d {
                Some(Data::Stdin(bytes)) => ClientFrame::Stdin(session::Stdin { bytes }),
                Some(Data::Close) => ClientFrame::CloseStdin(session::CloseStdin {}),
                None => {
                    data_open = false;
                    continue;
                }
            },
        };
        match &f {
            ClientFrame::Stdin(session::Stdin { bytes }) => {
                // Counted as sent before writing: a byte that may be in
                // flight must not look delivered after a reconnect.
                sent.bytes.fetch_add(bytes.len() as u64, std::sync::atomic::Ordering::AcqRel);
                for c in bytes.chunks(MAX_CHUNK) {
                    let chunk = ClientFrame::Stdin(session::Stdin { bytes: c.to_vec() });
                    if frame::send(&mut wr, &chunk).await.is_err() {
                        return;
                    }
                }
                continue;
            }
            ClientFrame::CloseStdin(_) => sent.closed.store(true, std::sync::atomic::Ordering::Release),
            _ => {}
        }
        if frame::send(&mut wr, &f).await.is_err() {
            return;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn restore(chunks: &[&[u8]]) -> String {
        let mut m = Modes::default();
        for c in chunks {
            m.feed(c);
        }
        String::from_utf8(m.restore_sequence()).unwrap()
    }

    #[test]
    fn nothing_to_restore_for_plain_output() {
        assert_eq!(restore(&[b"hello \x1b[31mred\x1b[0m"]), "\x1b[0m");
    }

    #[test]
    fn restores_only_modes_left_on() {
        let s = restore(&[b"\x1b[?1049h\x1b[?2004h\x1b[?25l", b"\x1b[?2004l"]);
        assert_eq!(s, "\x1b[?1049l\x1b[?25h\x1b[0m");
        assert_eq!(restore(&[b"\x1b[?1049h", b"\x1b[?1049l\x1b[?25l\x1b[?25h"]), "\x1b[0m");
    }

    #[test]
    fn sequences_split_across_chunks() {
        assert_eq!(restore(&[b"\x1b", b"[?10", b"00;1006h"]), "\x1b[?1006l\x1b[?1000l\x1b[0m");
    }

    #[test]
    fn keyboard_protocol_pushes_are_popped() {
        assert_eq!(restore(&[b"\x1b[>1u\x1b[>3u\x1b[<u"]), "\x1b[<1u\x1b[0m");
    }

    #[test]
    fn detach_key() {
        let mut f = DetachFilter::default();
        assert_eq!(f.feed(b"ab"), (b"ab".to_vec(), false));
        assert_eq!(f.feed(&[DETACH_PREFIX]), (vec![], false));
        assert_eq!(f.feed(b"d"), (vec![], true));

        let mut f = DetachFilter::default();
        assert_eq!(f.feed(&[b'x', DETACH_PREFIX, b'y']), (vec![b'x', DETACH_PREFIX, b'y'], false));
        assert_eq!(f.feed(&[DETACH_PREFIX, DETACH_PREFIX]), (vec![DETACH_PREFIX], false));
        assert_eq!(f.feed(&[b'q', DETACH_PREFIX, b'd', b'z']), (vec![b'q'], true));
    }
}
