//! `toby guest session`: owns one session's process, its terminal and its
//! output buffer, and serves one attached client at a time (plan §13.2).

use std::collections::VecDeque;
use std::ffi::CString;
use std::io;
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use nix::pty::{Winsize, openpty};
use nix::sys::signal::{Signal as NixSignal, killpg};
use nix::unistd::Pid;
use toby_proto::session::{ClientFrame, ServerFrame, State};
use toby_proto::types::{ExitStatus, Identity, SessionInfo, SpawnSpec, TtySize};
use toby_proto::{MAX_CHUNK, frame, session, types};
use tokio::io::unix::AsyncFd;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{UnixListener, UnixStream};
use tokio::sync::{mpsc, oneshot};

use crate::paths::{GuestPaths, session_files};
use crate::record::{self, SessionRecord, UserInfo};

/// Output replayed to a client that attaches and asks for recent output.
pub const REPLAY_BYTES: usize = 1024 * 1024;

/// Output kept so a client that loses its connection can resume without a
/// gap; larger than everything that can be in transit to a client (its queue
/// plus socket buffers along the way).
pub const RESUME_BYTES: usize = 8 * 1024 * 1024;

/// Frames queued for a client (each at most 16 KiB of output).
const CLIENT_QUEUE: usize = 16;

/// How long an exit record is kept for a client to collect it.
pub const KEEP_AFTER_EXIT: Duration = Duration::from_secs(3600);

/// How long output is drained after the child exits while other processes
/// still hold the terminal open.
const DRAIN_AFTER_EXIT: Duration = Duration::from_secs(2);

/// How long a client may take to accept queued output before it is dropped.
const CLIENT_STALL: Duration = Duration::from_secs(30);

/// Exit code reported when the command cannot be started, as shells do.
const CANNOT_START: i32 = 127;

const DEFAULT_PATH: &str = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";

/// Creates a session directory and writes its spec. The relay calls this
/// before starting `toby guest session`.
pub fn prepare(paths: &GuestPaths, spec: &SpawnSpec) -> io::Result<PathBuf> {
    std::fs::DirBuilder::new().recursive(true).mode(0o700).create(paths.sessions())?;
    let dir = paths.session_dir(&spec.session_id);
    std::fs::DirBuilder::new().mode(0o700).create(&dir)?;
    record::write(&dir.join(session_files::SPEC), spec)?;
    Ok(dir)
}

/// The account a session runs as.
#[derive(Debug, Clone)]
struct Account {
    name: String,
    uid: u32,
    gid: u32,
    home: String,
    shell: String,
}

fn resolve_account(paths: &GuestPaths, identity: Identity) -> io::Result<Account> {
    match identity {
        Identity::Root => Ok(Account {
            name: "root".into(),
            uid: 0,
            gid: 0,
            home: "/root".into(),
            shell: if Path::new("/bin/bash").exists() { "/bin/bash" } else { "/bin/sh" }.into(),
        }),
        Identity::User => {
            let user: UserInfo = record::read(&paths.user_file()).map_err(|e| {
                io::Error::new(e.kind(), format!("no user is configured in this machine: {e}"))
            })?;
            Ok(Account { name: user.name, uid: user.uid, gid: user.gid, home: user.home, shell: user.shell })
        }
    }
}

fn environment(spec: &SpawnSpec, account: &Account) -> Vec<(String, String)> {
    let mut env: Vec<(String, String)> = vec![
        ("PATH".into(), DEFAULT_PATH.into()),
        ("HOME".into(), account.home.clone()),
        ("USER".into(), account.name.clone()),
        ("LOGNAME".into(), account.name.clone()),
        ("SHELL".into(), account.shell.clone()),
    ];
    if spec.tty.is_some() {
        env.push(("TERM".into(), "xterm-256color".into()));
    }
    for (k, v) in &spec.env {
        env.retain(|(ek, _)| ek != k);
        env.push((k.clone(), v.clone()));
    }
    env
}

/// Input for the child, written by its own task so a child that does not
/// read never blocks the session.
enum Input {
    Data(Vec<u8>),
    Close,
}

/// A running child.
struct Child {
    pgid: i32,
    master: Option<Arc<AsyncFd<OwnedFd>>>,
}

enum Event {
    Exited(ExitStatus),
    Accepted(UnixStream),
    Frame(u64, ClientFrame),
    ClientGone(u64),
    Terminate,
}

/// Output of the command, on its own channel so the session only takes it
/// when the attached client can accept it.
enum Out {
    Data(Vec<u8>, bool),
    End,
}

/// Standard input received from clients, reported to reconnecting clients
/// so they can tell whether input was lost.
#[derive(Default)]
struct InputStats {
    bytes: std::sync::atomic::AtomicU64,
    closed: std::sync::atomic::AtomicBool,
}

struct Client {
    generation: u64,
    welcomed: bool,
    tx: mpsc::Sender<Outgoing>,
    /// Tells the writer to send `Detached` ahead of any queued output.
    detach: Option<oneshot::Sender<String>>,
}

enum Outgoing {
    Frame(ServerFrame),
    /// Signals once every frame queued before it has been written.
    Flush(oneshot::Sender<()>),
}

/// Recent output, keeping which stream each piece came from. Output is
/// numbered by offset (bytes since the session started) so a reconnecting
/// client can resume where it stopped.
#[derive(Default)]
struct Replay {
    chunks: VecDeque<(bool, Vec<u8>)>,
    len: usize,
    /// Offset of the first buffered byte.
    start: u64,
}

impl Replay {
    fn push(&mut self, bytes: &[u8], stderr: bool) {
        match self.chunks.back_mut() {
            Some((s, b)) if *s == stderr && b.len() + bytes.len() <= MAX_CHUNK => b.extend_from_slice(bytes),
            _ => self.chunks.push_back((stderr, bytes.to_vec())),
        }
        self.len += bytes.len();
        while self.len > RESUME_BYTES {
            let Some((_, front)) = self.chunks.front_mut() else { break };
            let excess = self.len - RESUME_BYTES;
            if front.len() <= excess {
                self.len -= front.len();
                self.start += front.len() as u64;
                self.chunks.pop_front();
            } else {
                front.drain(..excess);
                self.len -= excess;
                self.start += excess as u64;
            }
        }
    }

    /// Offset just past the last byte produced.
    fn end(&self) -> u64 {
        self.start + self.len as u64
    }

    /// Where replay starts for a client resuming at `from`, and how many
    /// bytes before that are no longer buffered.
    fn resume_point(&self, from: u64) -> (u64, u64) {
        let at = from.clamp(self.start, self.end());
        (at, at.saturating_sub(from))
    }

    /// Replay frames for the buffered output from offset `from` on.
    fn frames_from(&self, from: u64) -> Vec<ServerFrame> {
        let mut out = Vec::new();
        let mut offset = self.start;
        for (stderr, b) in &self.chunks {
            let end = offset + b.len() as u64;
            if end > from {
                let skip = from.saturating_sub(offset) as usize;
                for c in b[skip..].chunks(MAX_CHUNK) {
                    out.push(ServerFrame::Replay(session::Replay { bytes: c.to_vec(), stderr: *stderr }));
                }
            }
            offset = end;
        }
        out
    }
}

/// Runs the session with ID `id` until its exit has been collected.
pub async fn run(paths: GuestPaths, id: &str) -> io::Result<()> {
    if !crate::paths::valid_id(id) {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "invalid session ID"));
    }
    let dir = paths.session_dir(id);
    let result = serve(&paths, &dir).await;
    let _ = std::fs::remove_dir_all(&dir);
    result
}

async fn serve(paths: &GuestPaths, dir: &Path) -> io::Result<()> {
    let spec: SpawnSpec = record::read(&dir.join(session_files::SPEC))?;
    if spec.argv.is_empty() {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "empty command"));
    }
    let account = resolve_account(paths, spec.identity)?;
    let tty = spec.tty.is_some();

    let (events_tx, mut events) = mpsc::channel::<Event>(64);
    let (output_tx, mut output) = mpsc::channel::<Out>(16);
    let (input_tx, input_rx) = mpsc::channel::<Input>(64);
    let mut input_rx = Some(input_rx);

    // A session that starts on its own reports a failed start to the relay
    // by exiting before its socket exists.
    let mut child: Option<Child> = None;
    if !spec.start_on_attach {
        let rx = input_rx.take().expect("unused");
        child = Some(start_child(&spec, &account, events_tx.clone(), output_tx.clone(), rx)?);
    }

    let sock_path = dir.join(session_files::SOCKET);
    let _ = std::fs::remove_file(&sock_path);
    let listener = UnixListener::bind(&sock_path)?;
    std::fs::set_permissions(&sock_path, std::fs::Permissions::from_mode(0o600))?;

    let started = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    let mut rec = SessionRecord {
        info: SessionInfo {
            id: spec.session_id.clone(),
            argv0: spec.argv[0].clone(),
            attached: false,
            started,
            exit: None,
            version: None,
        },
        session_pid: std::process::id() as i32,
        child_pgid: child.as_ref().map(|c| c.pgid).unwrap_or(0),
    };
    record::write(&dir.join(session_files::RECORD), &rec)?;

    {
        let tx = events_tx.clone();
        tokio::spawn(async move {
            while let Ok((stream, _)) = listener.accept().await {
                if tx.send(Event::Accepted(stream)).await.is_err() {
                    break;
                }
            }
        });
    }
    {
        let tx = events_tx.clone();
        tokio::spawn(async move {
            let Ok(mut term) = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            else {
                return;
            };
            while term.recv().await.is_some() {
                if tx.send(Event::Terminate).await.is_err() {
                    break;
                }
            }
        });
    }

    let mut replay = Replay::default();
    let current = Arc::new(std::sync::atomic::AtomicU64::new(0));
    let stats = Arc::new(InputStats::default());
    let mut client: Option<Client> = None;
    let mut generation = 0u64;
    let mut exit: Option<ExitStatus> = None;
    let mut output_done = false;
    let mut drain_deadline: Option<tokio::time::Instant> = None;
    let mut keep_deadline: Option<tokio::time::Instant> = None;
    let mut stall_deadline: Option<tokio::time::Instant> = None;
    // Output of an exec-style command without a terminal waits for a client
    // (including across reconnections) instead of filling the replay buffer.
    let hold_for_client = !tty && spec.start_on_attach;
    let mut exited_at: Option<tokio::time::Instant> = None;
    // The exit has been queued for the attached client; resolves once written.
    // Generation of the client it was queued for, and its completion.
    let mut exit_flush: Option<(u64, oneshot::Receiver<()>)> = None;

    loop {
        let now = tokio::time::Instant::now();
        // An exit queued for a client that has since gone is delivered again.
        if exit_flush.as_ref().is_some_and(|(g, _)| client.as_ref().map(|c| c.generation) != Some(*g)) {
            exit_flush = None;
        }
        let welcomed = client.as_ref().is_some_and(|c| c.welcomed);
        let client_full = welcomed && client.as_ref().is_some_and(|c| c.tx.capacity() == 0);
        let held = client_full
            || (hold_for_client && !welcomed && exited_at.is_none_or(|t| now < t + KEEP_AFTER_EXIT));
        if !client_full {
            stall_deadline = None;
        } else if tty && stall_deadline.is_none() {
            stall_deadline = Some(now + CLIENT_STALL);
        }

        // The drain period after the child exits (for processes that keep
        // the terminal or pipes open) only runs while output flows.
        if exit.is_some() && held && !output_done {
            drain_deadline = Some(now + DRAIN_AFTER_EXIT);
        }
        let drained = exit.is_some() && (output_done || drain_deadline.is_some_and(|d| now >= d));
        if let (Some(status), true, None) = (exit, drained, &exit_flush) {
            if rec.info.exit.is_none() {
                // The command could not start (start-on-attach failure).
                rec.info.exit = Some(status);
                record::write(&dir.join(session_files::RECORD), &rec)?;
                record::write(&dir.join(session_files::EXIT), &status)?;
            }
            match client.as_ref().filter(|c| c.welcomed) {
                Some(c) if c.tx.capacity() >= 2 => {
                    let (done_tx, done_rx) = oneshot::channel();
                    let queued =
                        c.tx.try_send(Outgoing::Frame(ServerFrame::Exit(session::Exit { status }))).is_ok()
                            && c.tx.try_send(Outgoing::Flush(done_tx)).is_ok();
                    if queued {
                        exit_flush = Some((c.generation, done_rx));
                    } else {
                        drop_client(&mut client, dir, &mut rec);
                    }
                }
                // Wait for room in the client's queue (see the select below).
                Some(_) => {}
                None => {
                    if !spec.keep_after_exit {
                        break;
                    }
                    let deadline = *keep_deadline.get_or_insert_with(|| now + KEEP_AFTER_EXIT);
                    if now >= deadline {
                        break;
                    }
                }
            }
        }
        // Room for one output frame, or for the exit and its flush.
        let room_needed = if drained && exit_flush.is_none() && welcomed {
            2
        } else if client_full {
            1
        } else {
            0
        };

        let deadline = [
            if drained {
                keep_deadline
            } else if exit.is_some() {
                drain_deadline
            } else {
                None
            },
            stall_deadline,
        ]
        .into_iter()
        .flatten()
        .min();

        tokio::select! {
            e = events.recv() => {
                let Some(event) = e else { break };
                match event {
                    Event::Exited(status) => {
                        exit = Some(status);
                        // Recorded at once, so listings show it even while
                        // output is still waiting for a client.
                        rec.info.exit = Some(status);
                        record::write(&dir.join(session_files::RECORD), &rec)?;
                        record::write(&dir.join(session_files::EXIT), &status)?;
                        exited_at = Some(tokio::time::Instant::now());
                        drain_deadline = Some(tokio::time::Instant::now() + DRAIN_AFTER_EXIT);
                    }
                    Event::Accepted(stream) => {
                        if let Some(mut old) = client.take()
                            && let Some(d) = old.detach.take()
                        {
                            let _ = d.send("attached elsewhere".into());
                        }
                        generation += 1;
                        current.store(generation, std::sync::atomic::Ordering::Release);
                        client = Some(start_client(
                            stream,
                            generation,
                            current.clone(),
                            stats.clone(),
                            events_tx.clone(),
                            input_tx.clone(),
                        ));
                        set_attached(dir, &mut rec, true);
                    }
                    Event::Frame(generation_of, frame) => {
                        let Some(c) = client.as_mut().filter(|c| c.generation == generation_of) else {
                            continue;
                        };
                        match frame {
                            ClientFrame::Hello(hello) if !c.welcomed => {
                                let Some(version) = types::negotiate(&hello.versions) else {
                                    let refused = session::Refused { error: "unsupported version".into() };
                                    let _ = queue(c, ServerFrame::Refused(refused)).await;
                                    drop_client(&mut client, dir, &mut rec);
                                    continue;
                                };
                                if hello.rows > 0
                                    && hello.cols > 0
                                    && let Some(ch) = &child
                                {
                                    resize(ch, TtySize { rows: hello.rows, cols: hello.cols });
                                }
                                let state = match rec.info.exit {
                                    Some(s) => State::Exited(s),
                                    None => State::Running,
                                };
                                c.welcomed = true;
                                let (offset, lost) = match (hello.resume_from, hello.want_replay) {
                                    (Some(from), _) => replay.resume_point(from),
                                    (None, true) => (replay.end().saturating_sub(REPLAY_BYTES as u64).max(replay.start), 0),
                                    (None, false) => (replay.end(), 0),
                                };
                                let welcome = session::Welcome {
                                    version,
                                    state,
                                    tty,
                                    offset,
                                    lost,
                                    input: stats.bytes.load(std::sync::atomic::Ordering::Acquire),
                                    input_closed: stats.closed.load(std::sync::atomic::Ordering::Acquire),
                                };
                                let mut ok = queue(c, ServerFrame::Welcome(welcome)).await;
                                for f in replay.frames_from(offset) {
                                    ok = ok && queue(c, f).await;
                                }
                                if !ok {
                                    drop_client(&mut client, dir, &mut rec);
                                    continue;
                                }
                                if let Some(rx) = input_rx.take() {
                                    let mut spec = spec.clone();
                                    if let (Some(size), true) = (spec.tty.as_mut(), hello.rows > 0 && hello.cols > 0) {
                                        *size = TtySize { rows: hello.rows, cols: hello.cols };
                                    }
                                    match start_child(&spec, &account, events_tx.clone(), output_tx.clone(), rx) {
                                        Ok(ch) => {
                                            rec.child_pgid = ch.pgid;
                                            record::write(&dir.join(session_files::RECORD), &rec)?;
                                            child = Some(ch);
                                        }
                                        Err(e) => {
                                            let msg = format!("toby: cannot start {}: {e}\r\n", spec.argv[0]);
                                            replay.push(msg.as_bytes(), !tty);
                                            let bytes = msg.into_bytes();
                                            let f = if tty {
                                                ServerFrame::Stdout(session::Stdout { bytes })
                                            } else {
                                                ServerFrame::Stderr(session::Stderr { bytes })
                                            };
                                            let _ = queue(c, f).await;
                                            exit = Some(ExitStatus::Code(CANNOT_START));
                                            output_done = true;
                                        }
                                    }
                                }
                            }
                            ClientFrame::Resize(r) => {
                                if let Some(ch) = &child {
                                    resize(ch, TtySize { rows: r.rows, cols: r.cols });
                                }
                            }
                            ClientFrame::Signal(s) => {
                                if exit.is_none()
                                    && let (Some(ch), Ok(sig)) = (&child, NixSignal::try_from(s.signal))
                                {
                                    let _ = killpg(Pid::from_raw(ch.pgid), sig);
                                }
                            }
                            // Input goes straight to the child's input task.
                            ClientFrame::Hello(_) | ClientFrame::Stdin(_) | ClientFrame::CloseStdin(_) => {}
                        }
                    }
                    Event::ClientGone(generation_of) => {
                        if client.as_ref().is_some_and(|c| c.generation == generation_of) {
                            drop_client(&mut client, dir, &mut rec);
                        }
                    }
                    Event::Terminate => match (&child, exit) {
                        (Some(ch), None) => {
                            let _ = killpg(Pid::from_raw(ch.pgid), NixSignal::SIGHUP);
                        }
                        _ => break,
                    },
                }
            }
            o = output.recv(), if !output_done && !held => match o {
                Some(Out::Data(bytes, stderr)) => {
                    replay.push(&bytes, stderr);
                    if let Some(c) = client.as_ref().filter(|c| c.welcomed) {
                        let frame = if stderr {
                            ServerFrame::Stderr(session::Stderr { bytes })
                        } else {
                            ServerFrame::Stdout(session::Stdout { bytes })
                        };
                        if c.tx.try_send(Outgoing::Frame(frame)).is_err() {
                            drop_client(&mut client, dir, &mut rec);
                        }
                    }
                }
                Some(Out::End) | None => output_done = true,
            },
            ok = room(&client, room_needed), if room_needed > 0 => {
                if !ok {
                    drop_client(&mut client, dir, &mut rec);
                }
            }
            (g, ok) = flushed(&mut exit_flush), if exit_flush.is_some() => {
                exit_flush = None;
                if ok {
                    break;
                }
                // That client went away before receiving the exit.
                if client.as_ref().is_some_and(|c| c.generation == g) {
                    drop_client(&mut client, dir, &mut rec);
                }
            }
            _ = sleep_until(deadline) => {
                if stall_deadline.is_some_and(|d| tokio::time::Instant::now() >= d) {
                    stall_deadline = None;
                    drop_client(&mut client, dir, &mut rec);
                }
            }
        }
    }
    Ok(())
}

/// Sleeps until `deadline`, or forever without one.
async fn sleep_until(deadline: Option<tokio::time::Instant>) {
    match deadline {
        Some(d) => tokio::time::sleep_until(d).await,
        None => std::future::pending().await,
    }
}

fn drop_client(client: &mut Option<Client>, dir: &Path, rec: &mut SessionRecord) {
    *client = None;
    set_attached(dir, rec, false);
}

fn set_attached(dir: &Path, rec: &mut SessionRecord, attached: bool) {
    if rec.info.attached != attached {
        rec.info.attached = attached;
        let _ = record::write(&dir.join(session_files::RECORD), rec);
    }
}

/// Queues a frame for the client, giving up on a client that does not read.
async fn queue(c: &Client, frame: ServerFrame) -> bool {
    c.tx.send_timeout(Outgoing::Frame(frame), CLIENT_STALL).await.is_ok()
}

/// Resolves when the client's queue has room for `n` frames; false if the
/// client's writer is gone.
async fn room(client: &Option<Client>, n: usize) -> bool {
    match client {
        Some(c) => c.tx.reserve_many(n).await.is_ok(),
        None => std::future::pending().await,
    }
}

/// Resolves with the client generation and whether the queued exit was
/// written to it.
async fn flushed(pending: &mut Option<(u64, oneshot::Receiver<()>)>) -> (u64, bool) {
    match pending {
        Some((g, rx)) => (*g, rx.await.is_ok()),
        None => std::future::pending().await,
    }
}

fn start_client(
    stream: UnixStream,
    generation: u64,
    current: Arc<std::sync::atomic::AtomicU64>,
    stats: Arc<InputStats>,
    events: mpsc::Sender<Event>,
    input: mpsc::Sender<Input>,
) -> Client {
    let (mut rd, mut wr) = stream.into_split();
    let (tx, mut rx) = mpsc::channel::<Outgoing>(CLIENT_QUEUE);
    let (detach_tx, mut detach_rx) = oneshot::channel::<String>();

    tokio::spawn(async move {
        // A dropped sender means the client is going away normally: finish
        // writing what is queued. A sent reason preempts the queue.
        let mut detach_pending = true;
        loop {
            tokio::select! {
                biased;
                reason = &mut detach_rx, if detach_pending => match reason {
                    Ok(reason) => {
                        let _ = frame::send(&mut wr, &ServerFrame::Detached(session::Detached { reason })).await;
                        break;
                    }
                    Err(_) => detach_pending = false,
                },
                out = rx.recv() => match out {
                    Some(Outgoing::Frame(f)) => {
                        let last = matches!(f, ServerFrame::Refused(_));
                        if frame::send(&mut wr, &f).await.is_err() || last {
                            break;
                        }
                    }
                    Some(Outgoing::Flush(done)) => {
                        let _ = wr.flush().await;
                        let _ = done.send(());
                    }
                    None => break,
                },
            }
        }
        let _ = wr.shutdown().await;
    });

    tokio::spawn(async move {
        let mut hello_seen = false;
        loop {
            let f = match frame::recv::<ClientFrame, _>(&mut rd).await {
                Ok(f) => f,
                Err(_) => {
                    let _ = events.send(Event::ClientGone(generation)).await;
                    return;
                }
            };
            // Only the attached client's input reaches the command.
            let attached = current.load(std::sync::atomic::Ordering::Acquire) == generation;
            match f {
                ClientFrame::Stdin(d) if hello_seen && attached => {
                    stats.bytes.fetch_add(d.bytes.len() as u64, std::sync::atomic::Ordering::AcqRel);
                    let _ = input.send(Input::Data(d.bytes)).await;
                }
                ClientFrame::CloseStdin(_) if hello_seen && attached => {
                    stats.closed.store(true, std::sync::atomic::Ordering::Release);
                    let _ = input.send(Input::Close).await;
                }
                ClientFrame::Stdin(_) | ClientFrame::CloseStdin(_) => {}
                other => {
                    hello_seen |= matches!(other, ClientFrame::Hello(_));
                    if events.send(Event::Frame(generation, other)).await.is_err() {
                        return;
                    }
                }
            }
        }
    });

    Client { generation, welcomed: false, tx, detach: Some(detach_tx) }
}

fn resize(child: &Child, size: TtySize) {
    if let Some(master) = &child.master {
        let ws = Winsize { ws_row: size.rows, ws_col: size.cols, ws_xpixel: 0, ws_ypixel: 0 };
        // SAFETY: TIOCSWINSZ reads a `winsize` from the pointer, which is valid for the call.
        unsafe {
            libc::ioctl(master.get_ref().as_raw_fd(), libc::TIOCSWINSZ, &ws as *const Winsize);
        }
    }
}

async fn write_pty(master: &AsyncFd<OwnedFd>, mut data: &[u8]) -> io::Result<()> {
    while !data.is_empty() {
        let mut guard = master.writable().await?;
        match guard.try_io(|fd| nix::unistd::write(fd.get_ref(), data).map_err(io::Error::from)) {
            Ok(Ok(n)) => data = &data[n..],
            Ok(Err(e)) => return Err(e),
            Err(_would_block) => continue,
        }
    }
    Ok(())
}

fn exit_status(status: std::process::ExitStatus) -> ExitStatus {
    use std::os::unix::process::ExitStatusExt;
    match (status.code(), status.signal()) {
        (Some(code), _) => ExitStatus::Code(code),
        (None, Some(sig)) => ExitStatus::Signal(sig),
        _ => ExitStatus::Code(255),
    }
}

fn set_nonblocking(fd: &OwnedFd) -> io::Result<()> {
    use nix::fcntl::{FcntlArg, OFlag, fcntl};
    let flags = OFlag::from_bits_truncate(fcntl(fd, FcntlArg::F_GETFL)?);
    fcntl(fd, FcntlArg::F_SETFL(flags | OFlag::O_NONBLOCK))?;
    Ok(())
}

/// Credentials to switch to in the child, resolved before the fork.
struct Credentials {
    uid: libc::uid_t,
    gid: libc::gid_t,
    groups: Vec<libc::gid_t>,
}

fn credentials(account: &Account) -> io::Result<Option<Credentials>> {
    if nix::unistd::geteuid().as_raw() == account.uid {
        return Ok(None);
    }
    let name = CString::new(account.name.clone())
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "user name contains NUL"))?;
    let groups = nix::unistd::getgrouplist(&name, nix::unistd::Gid::from_raw(account.gid))
        .map(|g| g.into_iter().map(|g| g.as_raw()).collect())
        .unwrap_or_else(|_| vec![account.gid]);
    Ok(Some(Credentials { uid: account.uid, gid: account.gid, groups }))
}

/// Starts the session's child and the tasks that feed and read it.
fn start_child(
    spec: &SpawnSpec,
    account: &Account,
    events: mpsc::Sender<Event>,
    output: mpsc::Sender<Out>,
    mut input: mpsc::Receiver<Input>,
) -> io::Result<Child> {
    let env = environment(spec, account);
    let cwd = spec.cwd.clone().unwrap_or_else(|| account.home.clone());
    if !Path::new(&cwd).is_dir() {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            format!("working directory {cwd} does not exist"),
        ));
    }
    let cwd = CString::new(cwd)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "directory contains NUL"))?;
    let creds = credentials(account)?;

    let mut cmd = tokio::process::Command::new(&spec.argv[0]);
    cmd.args(&spec.argv[1..]).env_clear().envs(env).kill_on_drop(false);

    let master = match spec.tty {
        Some(size) => {
            let ws = Winsize { ws_row: size.rows, ws_col: size.cols, ws_xpixel: 0, ws_ypixel: 0 };
            let pty = openpty(Some(&ws), None).map_err(io::Error::from)?;
            cmd.stdin(Stdio::from(pty.slave.try_clone()?))
                .stdout(Stdio::from(pty.slave.try_clone()?))
                .stderr(Stdio::from(pty.slave));
            set_nonblocking(&pty.master)?;
            Some(Arc::new(AsyncFd::new(pty.master)?))
        }
        None => {
            cmd.stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped());
            None
        }
    };
    let with_tty = master.is_some();

    // SAFETY: the closure only makes async-signal-safe system calls on data
    // prepared before the fork.
    unsafe {
        cmd.pre_exec(move || {
            if libc::setsid() < 0 {
                return Err(io::Error::last_os_error());
            }
            if with_tty && libc::ioctl(0, libc::TIOCSCTTY, 0) < 0 {
                return Err(io::Error::last_os_error());
            }
            if let Some(c) = &creds
                && (libc::setgroups(c.groups.len(), c.groups.as_ptr()) < 0
                    || libc::setgid(c.gid) < 0
                    || libc::setuid(c.uid) < 0)
            {
                return Err(io::Error::last_os_error());
            }
            // After dropping privileges, so the directory is checked as the user.
            if libc::chdir(cwd.as_ptr()) < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }

    let mut child = cmd.spawn()?;
    drop(cmd);
    let pgid = child.id().map(|p| p as i32).unwrap_or(0);

    match &master {
        Some(m) => {
            let reader = m.clone();
            let tx = output.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; 16 * 1024];
                loop {
                    let Ok(mut guard) = reader.readable().await else {
                        break;
                    };
                    match guard
                        .try_io(|fd| nix::unistd::read(fd.get_ref(), &mut buf).map_err(io::Error::from))
                    {
                        Ok(Ok(0)) | Ok(Err(_)) => break,
                        Ok(Ok(n)) => {
                            if tx.send(Out::Data(buf[..n].to_vec(), false)).await.is_err() {
                                return;
                            }
                        }
                        Err(_would_block) => continue,
                    }
                }
                let _ = tx.send(Out::End).await;
            });
            let writer = m.clone();
            tokio::spawn(async move {
                while let Some(i) = input.recv().await {
                    let data = match i {
                        Input::Data(d) => d,
                        Input::Close => vec![0x04],
                    };
                    if write_pty(&writer, &data).await.is_err() {
                        break;
                    }
                }
            });
        }
        None => {
            let stdout = child.stdout.take();
            let stderr = child.stderr.take();
            let tx = output.clone();
            tokio::spawn(async move {
                let out = pump(stdout, false, tx.clone());
                let err = pump(stderr, true, tx.clone());
                tokio::join!(out, err);
                let _ = tx.send(Out::End).await;
            });
            let mut stdin = child.stdin.take();
            tokio::spawn(async move {
                while let Some(i) = input.recv().await {
                    match i {
                        Input::Data(d) => {
                            if let Some(s) = stdin.as_mut()
                                && s.write_all(&d).await.is_err()
                            {
                                stdin = None;
                            }
                        }
                        Input::Close => stdin = None,
                    }
                }
            });
        }
    }

    let tx = events;
    tokio::spawn(async move {
        let status = match child.wait().await {
            Ok(s) => exit_status(s),
            Err(_) => ExitStatus::Code(255),
        };
        let _ = tx.send(Event::Exited(status)).await;
    });

    Ok(Child { pgid, master })
}

async fn pump<R: tokio::io::AsyncRead + Unpin>(r: Option<R>, stderr: bool, tx: mpsc::Sender<Out>) {
    let Some(mut r) = r else { return };
    let mut buf = vec![0u8; 16 * 1024];
    loop {
        match r.read(&mut buf).await {
            Ok(0) | Err(_) => return,
            Ok(n) => {
                if tx.send(Out::Data(buf[..n].to_vec(), stderr)).await.is_err() {
                    return;
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn replay_keeps_streams_and_is_bounded() {
        let mut r = Replay::default();
        r.push(b"out1 ", false);
        r.push(b"out2 ", false);
        r.push(b"err", true);
        assert_eq!(
            r.frames_from(0),
            vec![
                ServerFrame::Replay(session::Replay { bytes: b"out1 out2 ".to_vec(), stderr: false }),
                ServerFrame::Replay(session::Replay { bytes: b"err".to_vec(), stderr: true }),
            ]
        );

        let mut r = Replay::default();
        for _ in 0..3 {
            r.push(&vec![b'x'; RESUME_BYTES / 2 + 1], false);
        }
        assert_eq!(r.len, RESUME_BYTES);
        let total: usize = r
            .frames_from(0)
            .iter()
            .map(|f| if let ServerFrame::Replay(p) = f { p.bytes.len() } else { 0 })
            .sum();
        assert_eq!(total, RESUME_BYTES);
        assert_eq!(r.end(), 3 * (RESUME_BYTES as u64 / 2 + 1));
    }

    #[test]
    fn resuming_skips_what_the_client_has() {
        let mut r = Replay::default();
        r.push(b"abc", false);
        r.push(b"def", true);
        assert_eq!(r.resume_point(4), (4, 0));
        assert_eq!(
            r.frames_from(4),
            vec![ServerFrame::Replay(session::Replay { bytes: b"ef".to_vec(), stderr: true })]
        );
        assert_eq!(r.resume_point(99), (6, 0));

        let mut r = Replay::default();
        for _ in 0..3 {
            r.push(&vec![b'x'; RESUME_BYTES / 2 + 1], false);
        }
        let (at, lost) = r.resume_point(10);
        assert_eq!(at, r.start);
        assert_eq!(lost, r.start - 10);
    }
}
