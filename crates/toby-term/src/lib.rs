//! Terminal handling for attached sessions: raw mode, the detach key, window
//! size changes, and the client side of the session protocol (plan §13).

pub mod compositor;

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

/// Whether a status line takes the terminal's last row.
static COMPOSING: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

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
            if COMPOSING.load(std::sync::atomic::Ordering::Acquire) {
                // The whole terminal scrolls again, without the status line.
                let _ = io::stdout().write_all(b"\x1b[r\x1b[999;1H\x1b[0m\x1b[2K");
                let _ = io::stdout().flush();
            }
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
const TRACKED_MODES: &[u16] = &[6, 47, 1047, 1049, 1000, 1002, 1003, 1004, 1005, 1006, 1015, 2004, 2026];

/// Follows the terminal modes a session enables in its output, so that the
/// user's terminal can be put back when the attachment ends.
#[derive(Debug, Default)]
pub struct Modes {
    enabled: std::collections::BTreeSet<u16>,
    cursor_hidden: bool,
    keyboard_pushes: u32,
    /// Insert mode (`CSI 4 h`).
    insert: bool,
    /// G0 is not ASCII, or G1 is invoked (`SO`).
    charset: bool,
    state: ParseState,
    params: Vec<u8>,
}

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
enum ParseState {
    #[default]
    Ground,
    Escape,
    /// After `ESC (`.
    Charset,
    Csi,
}

impl Modes {
    /// Scans output bytes; sequences may be split across calls.
    pub fn feed(&mut self, bytes: &[u8]) {
        for &b in bytes {
            match self.state {
                ParseState::Ground => match b {
                    0x1b => self.state = ParseState::Escape,
                    0x0e => self.charset = true,
                    0x0f => self.charset = false,
                    _ => {}
                },
                ParseState::Escape => {
                    self.state = match b {
                        b'[' => ParseState::Csi,
                        b'(' => ParseState::Charset,
                        _ => ParseState::Ground,
                    };
                    self.params.clear();
                }
                ParseState::Charset => {
                    self.charset = b != b'B';
                    self.state = ParseState::Ground;
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
            (None, b'h' | b'l') if Self::numbers(rest).contains(&4) => self.insert = fin == b'h',
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
        if self.insert {
            out.extend(b"\x1b[4l");
        }
        if self.charset {
            out.extend(b"\x1b(B\x0f");
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

/// What the user asked for with the prefix key.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Command {
    /// `d`: detach.
    Detach,
    /// `a`: show the approvals waiting.
    Approvals,
}

/// A key in the kitty keyboard protocol, `CSI code[:…] [; mods[:event]] u`:
/// its length, code, modifiers and event (1 press, 2 repeat, 3 release).
fn kitty_key(input: &[u8]) -> Option<(usize, u32, u32, u32)> {
    let rest = input.strip_prefix(b"\x1b[")?;
    let end = rest.iter().position(|b| !(b.is_ascii_digit() || matches!(b, b';' | b':')))?;
    if rest[end] != b'u' {
        return None;
    }
    let text = std::str::from_utf8(&rest[..end]).ok()?;
    let mut parts = text.split(';');
    let code = parts.next()?.split(':').next()?.parse().ok()?;
    let (mods, event) = match parts.next() {
        Some(m) => {
            let mut m = m.split(':');
            let mods = m.next().filter(|s| !s.is_empty()).map(str::parse).transpose().ok()?.unwrap_or(1);
            let event = m.next().map(str::parse).transpose().ok()?.unwrap_or(1);
            (mods, event)
        }
        None => (1, 1),
    };
    Some((end + 3, code, mods, event))
}

/// Keys that are modifiers alone in the kitty keyboard protocol.
const KITTY_MODIFIERS: std::ops::RangeInclusive<u32> = 57441..=57452;

/// Splits input into bytes for the session and a command given with the
/// prefix key.
#[derive(Debug, Default)]
pub struct DetachFilter {
    /// The prefix key was pressed, as these bytes.
    pending: Option<Vec<u8>>,
    /// The start of a control sequence the next read completes.
    held: Vec<u8>,
}

impl DetachFilter {
    /// Returns the bytes to send and the command, if one was given; input
    /// after a command is dropped. The prefix key also works as the kitty
    /// keyboard protocol reports it, and its release and repeats are not
    /// keys of their own.
    pub fn feed(&mut self, input: &[u8]) -> (Vec<u8>, Option<Command>) {
        let mut input =
            std::mem::take(&mut self.held).into_iter().chain(input.iter().copied()).collect::<Vec<u8>>();
        // A control sequence cut by the read waits for its end.
        if let Some(start) = input.iter().rposition(|b| *b == 0x1b)
            && input[start + 1..].first() == Some(&b'[')
            && input[start + 2..].iter().all(|b| b.is_ascii_digit() || matches!(b, b';' | b':'))
        {
            self.held = input.split_off(start);
        }
        let input = &input[..];
        let mut out = Vec::with_capacity(input.len());
        let mut i = 0;
        while i < input.len() {
            let (len, code, mods, event) = match kitty_key(&input[i..]) {
                Some(k) => k,
                None => {
                    let b = input[i];
                    (1, u32::from(b), if b == DETACH_PREFIX { 5 } else { 1 }, 1)
                }
            };
            let bytes = &input[i..i + len];
            i += len;
            let prefix = (len == 1 && bytes[0] == DETACH_PREFIX) || (len > 1 && code == 92 && mods == 5);
            match self.pending.take() {
                None if prefix && event == 1 => self.pending = Some(bytes.to_vec()),
                None => out.extend_from_slice(bytes),
                Some(p) => {
                    let plain = mods == 1 && event == 1;
                    if event != 1 || KITTY_MODIFIERS.contains(&code) {
                        // A release or repeat, or a modifier alone: still waiting.
                        self.pending = Some(p);
                    } else if plain && code == u32::from(b'd') {
                        return (out, Some(Command::Detach));
                    } else if plain && code == u32::from(b'a') {
                        return (out, Some(Command::Approvals));
                    } else if prefix {
                        // The prefix twice sends it once.
                        out.extend_from_slice(&p);
                    } else {
                        out.extend_from_slice(&p);
                        out.extend_from_slice(bytes);
                    }
                }
            }
        }
        (out, None)
    }
}

/// Events for the attachment loop.
enum Event {
    /// The user pressed the detach key.
    Detach,
    /// The user asked for the approvals.
    Approvals,
    /// Keys for the overlay.
    Keys(Vec<u8>),
    Resize,
    Signal(i32),
}

/// The status line and approvals of an attachment with a terminal.
pub struct Ui {
    pub status: tokio::sync::watch::Receiver<compositor::Status>,
    /// Decisions made in the overlay go here.
    pub decisions: mpsc::Sender<compositor::Decision>,
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
/// Keys go to the overlay while it is open.
fn spawn_stdin(
    interactive: bool,
    overlay: Arc<std::sync::atomic::AtomicBool>,
    data: mpsc::Sender<Data>,
    events: mpsc::Sender<Event>,
) {
    std::thread::spawn(move || {
        use std::io::Read;
        use std::sync::atomic::Ordering;
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
            let (bytes, command) =
                if interactive { filter.feed(&buf[..n]) } else { (buf[..n].to_vec(), None) };
            if !bytes.is_empty() {
                let sent = if overlay.load(Ordering::Acquire) {
                    events.blocking_send(Event::Keys(bytes)).is_ok()
                } else {
                    data.blocking_send(Data::Stdin(bytes)).is_ok()
                };
                if !sent {
                    return;
                }
            }
            match command {
                Some(Command::Detach) => {
                    let _ = events.blocking_send(Event::Detach);
                    return;
                }
                Some(Command::Approvals) if events.blocking_send(Event::Approvals).is_err() => return,
                _ => {}
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
    reserved: u16,
) -> io::Result<session::Welcome> {
    let (rows, cols) = size().map(|(r, c)| (r - reserved, c)).unwrap_or((0, 0));
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
async fn nudge<S: Stream>(s: &mut S, reserved: u16) -> io::Result<()> {
    if let Some((rows, cols)) = size().map(|(r, c)| (r - reserved, c))
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

/// Whether an attachment with a status line uses this terminal: it has
/// room for the line.
fn composes() -> bool {
    local_tty() && size().is_some_and(|(rows, cols)| compositor::Compositor::fits(rows, cols))
}

/// The terminal size a session attached here gets: the terminal's, less
/// the status line if there is one.
pub fn session_size(status_line: bool) -> Option<(u16, u16)> {
    let (rows, cols) = size()?;
    Some(if status_line && composes() { (rows - 1, cols) } else { (rows, cols) })
}

/// Asks the terminal where its cursor is (1-based row and column). Input
/// typed meanwhile is returned for the session.
fn cursor_position() -> (Option<(u16, u16)>, Vec<u8>) {
    use std::os::fd::AsRawFd;
    let mut o = io::stdout().lock();
    if o.write_all(b"\x1b[6n").and_then(|()| o.flush()).is_err() {
        return (None, Vec::new());
    }
    let fd = io::stdin().as_raw_fd();
    let deadline = std::time::Instant::now() + Duration::from_secs(1);
    let mut got = Vec::new();
    while let Some(left) = deadline.checked_duration_since(std::time::Instant::now()) {
        let mut pfd = libc::pollfd { fd, events: libc::POLLIN, revents: 0 };
        // SAFETY: one valid pollfd.
        if unsafe { libc::poll(&mut pfd, 1, left.as_millis() as i32) } <= 0 {
            break;
        }
        let mut buf = [0u8; 256];
        // SAFETY: reads into a buffer of its length.
        let n = unsafe { libc::read(fd, buf.as_mut_ptr().cast(), buf.len()) };
        if n <= 0 {
            break;
        }
        got.extend_from_slice(&buf[..n as usize]);
        if let Some((pos, range)) = cursor_report(&got) {
            got.drain(range);
            return (Some(pos), got);
        }
    }
    (None, got)
}

/// Finds a cursor position report, `ESC [ row ; col R`, in `input`.
fn cursor_report(input: &[u8]) -> Option<((u16, u16), std::ops::Range<usize>)> {
    (0..input.len()).find_map(|start| {
        let rest = input[start..].strip_prefix(b"\x1b[")?;
        let end = rest.iter().position(|b| !(b.is_ascii_digit() || *b == b';'))?;
        if rest[end] != b'R' {
            return None;
        }
        let (row, col) = std::str::from_utf8(&rest[..end]).ok()?.split_once(';')?;
        Some(((row.parse().ok()?, col.parse().ok()?), start..start + 2 + end + 1))
    })
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
    ui: Option<Ui>,
) -> io::Result<Outcome> {
    let composing = ui.is_some() && composes();
    let reserved = u16::from(composing);
    let mut conn = connect().await?;
    let welcome = hello(&mut conn, want_replay, None, reserved).await?;
    let interactive = welcome.tty && local_tty();
    let ui = ui.filter(|_| interactive && composing);
    let reserved = u16::from(ui.is_some());
    if reserved == 0 && composing {
        // No status line after all: the session gets every row.
        let (rows, cols) = size().unwrap_or((24, 80));
        frame::send(&mut conn, &ClientFrame::Resize(session::Resize { rows, cols })).await?;
    }

    let raw = if interactive { RawMode::enable()? } else { None };
    let (data_tx, data_rx) = mpsc::channel::<Data>(64);
    let (events_tx, events_rx) = mpsc::channel::<Event>(64);
    let mut comp = None;
    if ui.is_some() {
        let (rows, cols) = size().unwrap_or((24, 80));
        let mut c = compositor::Compositor::new(rows, cols);
        let (cursor, typed) = cursor_position();
        COMPOSING.store(true, std::sync::atomic::Ordering::Release);
        write_out(&c.start(cursor), false)?;
        if !typed.is_empty() {
            let _ = data_tx.send(Data::Stdin(typed)).await;
        }
        comp = Some(c);
    }
    let overlay = Arc::new(std::sync::atomic::AtomicBool::new(false));
    spawn_stdin(interactive, overlay.clone(), data_tx, events_tx.clone());
    spawn_signals(interactive, &events_tx);

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
        comp,
        ui,
        overlay,
        reserved,
    };
    let result = async {
        if redraw && interactive {
            nudge(&mut conn, reserved).await?;
        }
        state.run(conn, connect, signals).await
    }
    .await;
    if interactive {
        let restore = state.modes.restore_sequence();
        let out = match &mut state.comp {
            Some(c) => c.finish(&restore),
            None => restore,
        };
        let mut o = io::stdout().lock();
        let _ = o.write_all(&out);
        let _ = o.flush();
        COMPOSING.store(false, std::sync::atomic::Ordering::Release);
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
    /// The status line and overlays, with a terminal.
    comp: Option<compositor::Compositor>,
    ui: Option<Ui>,
    /// Whether keys go to the overlay.
    overlay: Arc<std::sync::atomic::AtomicBool>,
    /// Rows the session does not get.
    reserved: u16,
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
                changed = status_changed(&mut self.ui) => {
                    if let (Some(status), Some(c)) = (changed, &mut self.comp) {
                        let out = c.set_status(status);
                        if let Err(e) = self.screen(&out) {
                            return Pumped::Done(Err(e));
                        }
                    }
                }
                e = self.events.recv() => match e {
                    Some(Event::Detach) => return Pumped::Done(Ok(Outcome::Detached)),
                    Some(Event::Approvals) => {
                        if let Some(c) = &mut self.comp {
                            let out = c.open_approvals();
                            if let Err(e) = self.screen(&out) {
                                return Pumped::Done(Err(e));
                            }
                        }
                    }
                    Some(Event::Keys(keys)) => {
                        let Some(c) = &mut self.comp else { continue };
                        let (out, decision, input) = c.key(&keys);
                        if let (Some(d), Some(ui)) = (decision, &self.ui) {
                            let _ = ui.decisions.send(d).await;
                        }
                        if let Err(e) = self.screen(&out) {
                            return Pumped::Done(Err(e));
                        }
                        // Replies to the session's queries, and keys typed
                        // after the overlay closed, are the session's.
                        if !input.is_empty() {
                            let _ = ctl.send(ClientFrame::Stdin(session::Stdin { bytes: input })).await;
                        }
                    }
                    Some(Event::Resize) => {
                        if let Some((rows, cols)) = size() {
                            if let Err(e) = self.resized(rows, cols) {
                                return Pumped::Done(Err(e));
                            }
                            let rows = rows - self.reserved;
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
        if stderr {
            return write_out(bytes, true);
        }
        self.modes.feed(bytes);
        match &mut self.comp {
            Some(c) => {
                let out = c.output(bytes);
                // The overlay may have been drawn only now.
                self.screen(&out)
            }
            None => write_out(bytes, false),
        }
    }

    /// The terminal is `rows`×`cols` now. Too small for the status line,
    /// the session gets all of it for the rest of the attachment.
    fn resized(&mut self, rows: u16, cols: u16) -> io::Result<()> {
        let Some(c) = &mut self.comp else { return Ok(()) };
        if compositor::Compositor::fits(rows, cols) {
            let out = c.resize(rows, cols);
            return self.screen(&out);
        }
        let out = c.finish(b"");
        self.comp = None;
        self.reserved = 0;
        COMPOSING.store(false, std::sync::atomic::Ordering::Release);
        self.overlay.store(false, std::sync::atomic::Ordering::Release);
        write_out(&out, false)
    }

    /// Writes what the compositor drew, and notes whether the overlay is
    /// open.
    fn screen(&mut self, out: &[u8]) -> io::Result<()> {
        if let Some(c) = &self.comp {
            self.overlay.store(c.overlay_open(), std::sync::atomic::Ordering::Release);
        }
        write_out(out, false)
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
            let Ok(w) = hello(&mut c, false, Some(self.offset), self.reserved).await else { continue };
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
                let note = format!("\r\n[toby: {} bytes of output were lost]\r\n", w.lost);
                let _ = match &mut self.comp {
                    Some(comp) => write_out(&comp.output(note.as_bytes()), false),
                    None => write_out(note.as_bytes(), true),
                };
            }
            if self.interactive {
                // A size change meanwhile went nowhere.
                if let Some((rows, cols)) = size() {
                    self.resized(rows, cols)?;
                }
                let _ = nudge(&mut c, self.reserved).await;
            }
            return Ok(c);
        }
    }
}

/// The next status, when the attachment has a status line; never
/// resolves otherwise, or once the status is no longer updated.
async fn status_changed(ui: &mut Option<Ui>) -> Option<compositor::Status> {
    let Some(ui) = ui else { return std::future::pending().await };
    if ui.status.changed().await.is_err() {
        return std::future::pending().await;
    }
    Some(ui.status.borrow_and_update().clone())
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
    fn origin_insert_sync_and_charsets_are_turned_off() {
        assert_eq!(
            restore(&[b"\x1b[?6h\x1b[?2026h\x1b[4h\x1b(0"]),
            "\x1b[?2026l\x1b[?6l\x1b[4l\x1b(B\x0f\x1b[0m"
        );
        assert_eq!(restore(&[b"\x0e\x1b[4h\x1b[4l\x0f"]), "\x1b[0m");
    }

    #[test]
    fn keyboard_protocol_pushes_are_popped() {
        assert_eq!(restore(&[b"\x1b[>1u\x1b[>3u\x1b[<u"]), "\x1b[<1u\x1b[0m");
    }

    #[test]
    fn detach_key() {
        let mut f = DetachFilter::default();
        assert_eq!(f.feed(b"ab"), (b"ab".to_vec(), None));
        assert_eq!(f.feed(&[DETACH_PREFIX]), (vec![], None));
        assert_eq!(f.feed(b"d"), (vec![], Some(Command::Detach)));

        let mut f = DetachFilter::default();
        assert_eq!(f.feed(&[b'x', DETACH_PREFIX, b'y']), (vec![b'x', DETACH_PREFIX, b'y'], None));
        assert_eq!(f.feed(&[DETACH_PREFIX, DETACH_PREFIX]), (vec![DETACH_PREFIX], None));
        assert_eq!(f.feed(&[b'q', DETACH_PREFIX, b'd', b'z']), (vec![b'q'], Some(Command::Detach)));
        assert_eq!(f.feed(&[DETACH_PREFIX, b'a']), (vec![], Some(Command::Approvals)));

        // The kitty keyboard protocol's Ctrl-\: its release and a modifier
        // alone come before the key, which may be encoded too.
        let mut f = DetachFilter::default();
        assert_eq!(f.feed(b"x\x1b[92;5ud"), (b"x".to_vec(), Some(Command::Detach)));
        let mut f = DetachFilter::default();
        assert_eq!(f.feed(b"\x1b[92;5u\x1b[92;5:3u\x1b[57442;5:3u"), (vec![], None));
        assert_eq!(f.feed(b"\x1b[97;1:1u"), (vec![], Some(Command::Approvals)));
        let mut f = DetachFilter::default();
        assert_eq!(f.feed(b"\x1b[92;"), (vec![], None), "the rest comes with the next read");
        assert_eq!(f.feed(b"5ud"), (vec![], Some(Command::Detach)));
        let mut f = DetachFilter::default();
        assert_eq!(f.feed(b"\x1b[92;5u\x1b[92;5u"), (b"\x1b[92;5u".to_vec(), None));
        assert_eq!(f.feed(b"\x1b[92;5u\x1b[120u"), (b"\x1b[92;5u\x1b[120u".to_vec(), None));
        assert_eq!(
            f.feed(b"\x1b[92;5:3u"),
            (b"\x1b[92;5:3u".to_vec(), None),
            "a release alone is the session's"
        );
    }

    #[test]
    fn cursor_reports_are_found_among_typed_keys() {
        let (pos, range) = cursor_report(b"ab\x1b[12;40Rc").unwrap();
        assert_eq!(pos, (12, 40));
        assert_eq!(range, 2..10);
        assert!(cursor_report(b"\x1b[12;40").is_none());
    }
}
