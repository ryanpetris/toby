//! `toby guest session`: owns one session's process, its terminal and its
//! output buffer, and serves one attached client at a time (plan §13.2).

use std::collections::VecDeque;
use std::ffi::CString;
use std::io;
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
use std::path::PathBuf;
use std::process::Stdio;
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

/// Output kept for replay.
pub const REPLAY_BYTES: usize = 1024 * 1024;

/// How long an exit record is kept for a client to collect it.
pub const KEEP_AFTER_EXIT: Duration = Duration::from_secs(3600);

/// How long output is drained after the child exits while other processes
/// still hold the terminal open.
const DRAIN_AFTER_EXIT: Duration = Duration::from_secs(2);

const DEFAULT_PATH: &str = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";

/// Creates a session directory and writes its spec. The relay calls this
/// before starting `toby guest session`.
pub fn prepare(paths: &GuestPaths, spec: &SpawnSpec) -> io::Result<PathBuf> {
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(paths.sessions())?;
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
            shell: if std::path::Path::new("/bin/bash").exists() {
                "/bin/bash"
            } else {
                "/bin/sh"
            }
            .into(),
        }),
        Identity::User => {
            let user: UserInfo = record::read(&paths.user_file()).map_err(|e| {
                io::Error::new(e.kind(), format!("no user is configured in this machine: {e}"))
            })?;
            Ok(Account {
                name: user.name,
                uid: user.uid,
                gid: user.gid,
                home: user.home,
                shell: user.shell,
            })
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

enum ChildIo {
    Pty(std::sync::Arc<AsyncFd<OwnedFd>>),
    Pipes {
        stdin: Option<tokio::process::ChildStdin>,
    },
}

enum Event {
    Output(Vec<u8>, bool),
    OutputEnd,
    Exited(ExitStatus),
    Accepted(UnixStream),
    Frame(u64, ClientFrame),
    ClientGone(u64),
    Terminate,
}

struct Client {
    generation: u64,
    welcomed: bool,
    tx: mpsc::Sender<Outgoing>,
}

enum Outgoing {
    Frame(ServerFrame),
    /// Signals once every frame queued before it has been written.
    Flush(oneshot::Sender<()>),
}

/// Runs the session with ID `id` until its exit has been collected.
pub async fn run(paths: GuestPaths, id: &str) -> io::Result<()> {
    if !crate::paths::valid_id(id) {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "invalid session ID"));
    }
    let dir = paths.session_dir(id);
    let spec: SpawnSpec = record::read(&dir.join(session_files::SPEC))?;
    if spec.argv.is_empty() {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "empty command"));
    }
    let account = resolve_account(&paths, spec.identity)?;

    let sock_path = dir.join(session_files::SOCKET);
    let _ = std::fs::remove_file(&sock_path);
    let listener = UnixListener::bind(&sock_path)?;
    std::fs::set_permissions(&sock_path, std::fs::Permissions::from_mode(0o600))?;

    let (events_tx, mut events) = mpsc::channel::<Event>(64);
    let (mut child, io, pgid) = start_child(&spec, &account, events_tx.clone())?;

    let started = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let mut rec = SessionRecord {
        info: SessionInfo {
            id: spec.session_id.clone(),
            argv0: spec.argv[0].clone(),
            attached: false,
            started,
            exit: None,
        },
        session_pid: std::process::id() as i32,
        child_pgid: pgid,
    };
    record::write(&dir.join(session_files::RECORD), &rec)?;

    {
        let tx = events_tx.clone();
        tokio::spawn(async move {
            let status = match child.wait().await {
                Ok(s) => exit_status(s),
                Err(_) => ExitStatus::Code(255),
            };
            let _ = tx.send(Event::Exited(status)).await;
        });
    }
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

    let tty = spec.tty.is_some();
    let mut io = io;
    let mut replay: VecDeque<u8> = VecDeque::new();
    let mut client: Option<Client> = None;
    let mut generation = 0u64;
    let mut exit: Option<ExitStatus> = None;
    let mut output_done = false;
    let mut drain_deadline: Option<tokio::time::Instant> = None;
    let mut keep_deadline: Option<tokio::time::Instant> = None;

    loop {
        // Once the child has exited and its output is drained (or the drain
        // timed out), deliver the exit.
        if let Some(status) = exit {
            let drained = output_done || drain_deadline.is_some_and(|d| tokio::time::Instant::now() >= d);
            if drained {
                if rec.info.exit.is_none() {
                    rec.info.exit = Some(status);
                    record::write(&dir.join(session_files::RECORD), &rec)?;
                    record::write(&dir.join(session_files::EXIT), &status)?;
                }
                if let Some(c) = client.as_ref().filter(|c| c.welcomed) {
                    if deliver_exit(c, status).await {
                        break;
                    }
                    client = None;
                }
                if !spec.keep_after_exit {
                    break;
                }
                let deadline =
                    *keep_deadline.get_or_insert_with(|| tokio::time::Instant::now() + KEEP_AFTER_EXIT);
                if tokio::time::Instant::now() >= deadline {
                    break;
                }
            }
        }

        let now = tokio::time::Instant::now();
        let drained = exit.is_some() && (output_done || drain_deadline.is_some_and(|d| now >= d));
        let timeout = if drained {
            keep_deadline
        } else if exit.is_some() {
            drain_deadline
        } else {
            None
        };
        let event = match timeout {
            Some(t) => match tokio::time::timeout_at(t, events.recv()).await {
                Ok(e) => e,
                Err(_) => continue,
            },
            None => events.recv().await,
        };
        let Some(event) = event else { break };

        match event {
            Event::Output(bytes, stderr) => {
                push_replay(&mut replay, &bytes);
                if let Some(c) = client.as_ref().filter(|c| c.welcomed) {
                    for chunk in bytes.chunks(MAX_CHUNK) {
                        let frame = if stderr {
                            ServerFrame::Stderr(session::Stderr {
                                bytes: chunk.to_vec(),
                            })
                        } else {
                            ServerFrame::Stdout(session::Stdout {
                                bytes: chunk.to_vec(),
                            })
                        };
                        if !queue(c, frame).await {
                            client = None;
                            break;
                        }
                    }
                }
            }
            Event::OutputEnd => output_done = true,
            Event::Exited(status) => {
                exit = Some(status);
                drain_deadline = Some(tokio::time::Instant::now() + DRAIN_AFTER_EXIT);
                if let ChildIo::Pipes { stdin } = &mut io {
                    stdin.take();
                }
            }
            Event::Accepted(stream) => {
                if let Some(old) = client.take() {
                    let _ = old
                        .tx
                        .try_send(Outgoing::Frame(ServerFrame::Detached(session::Detached {
                            reason: "attached elsewhere".into(),
                        })));
                }
                generation += 1;
                client = Some(start_client(stream, generation, events_tx.clone()));
                set_attached(&dir, &mut rec, true);
            }
            Event::Frame(generation_of, frame) => {
                let Some(c) = client.as_mut().filter(|c| c.generation == generation_of) else {
                    continue;
                };
                match frame {
                    ClientFrame::Hello(hello) if !c.welcomed => {
                        let Some(version) = types::negotiate(&hello.versions) else {
                            let _ = queue(
                                c,
                                ServerFrame::Refused(session::Refused {
                                    error: "unsupported version".into(),
                                }),
                            )
                            .await;
                            client = None;
                            set_attached(&dir, &mut rec, false);
                            continue;
                        };
                        let state = match rec.info.exit {
                            Some(s) => State::Exited(s),
                            None => State::Running,
                        };
                        c.welcomed = true;
                        let mut ok =
                            queue(c, ServerFrame::Welcome(session::Welcome { version, state, tty })).await;
                        if ok && hello.want_replay {
                            let (a, b) = replay.as_slices();
                            let all: Vec<u8> = a.iter().chain(b.iter()).copied().collect();
                            for chunk in all.chunks(MAX_CHUNK) {
                                ok = ok
                                    && queue(
                                        c,
                                        ServerFrame::Replay(session::Replay {
                                            bytes: chunk.to_vec(),
                                        }),
                                    )
                                    .await;
                            }
                        }
                        if !ok {
                            client = None;
                            set_attached(&dir, &mut rec, false);
                            continue;
                        }
                        if hello.rows > 0 && hello.cols > 0 {
                            resize(
                                &io,
                                TtySize {
                                    rows: hello.rows,
                                    cols: hello.cols,
                                },
                            );
                        }
                    }
                    ClientFrame::Hello(_) => {}
                    _ if !c.welcomed => {}
                    ClientFrame::Stdin(data) => write_input(&mut io, &data.bytes).await,
                    ClientFrame::CloseStdin(_) => match &mut io {
                        ChildIo::Pty(_) => write_input(&mut io, &[0x04]).await,
                        ChildIo::Pipes { stdin } => {
                            stdin.take();
                        }
                    },
                    ClientFrame::Resize(r) => resize(
                        &io,
                        TtySize {
                            rows: r.rows,
                            cols: r.cols,
                        },
                    ),
                    ClientFrame::Signal(s) => {
                        if exit.is_none()
                            && let Ok(sig) = NixSignal::try_from(s.signal)
                        {
                            let _ = killpg(Pid::from_raw(pgid), sig);
                        }
                    }
                }
            }
            Event::ClientGone(generation_of) => {
                if client.as_ref().is_some_and(|c| c.generation == generation_of) {
                    client = None;
                    set_attached(&dir, &mut rec, false);
                }
            }
            Event::Terminate => {
                if exit.is_none() {
                    let _ = killpg(Pid::from_raw(pgid), NixSignal::SIGHUP);
                } else {
                    break;
                }
            }
        }
    }

    let _ = std::fs::remove_dir_all(&dir);
    Ok(())
}

fn set_attached(dir: &std::path::Path, rec: &mut SessionRecord, attached: bool) {
    if rec.info.attached != attached {
        rec.info.attached = attached;
        let _ = record::write(&dir.join(session_files::RECORD), rec);
    }
}

async fn queue(c: &Client, frame: ServerFrame) -> bool {
    c.tx.send_timeout(Outgoing::Frame(frame), Duration::from_secs(30))
        .await
        .is_ok()
}

/// Sends the exit to a welcomed client and waits until it has been written.
async fn deliver_exit(c: &Client, status: ExitStatus) -> bool {
    if !queue(c, ServerFrame::Exit(session::Exit { status })).await {
        return false;
    }
    let (done_tx, done_rx) = oneshot::channel();
    if c.tx.send(Outgoing::Flush(done_tx)).await.is_err() {
        return false;
    }
    tokio::time::timeout(Duration::from_secs(30), done_rx)
        .await
        .is_ok_and(|r| r.is_ok())
}

fn push_replay(replay: &mut VecDeque<u8>, bytes: &[u8]) {
    replay.extend(bytes);
    if replay.len() > REPLAY_BYTES {
        let excess = replay.len() - REPLAY_BYTES;
        replay.drain(..excess);
    }
}

fn start_client(stream: UnixStream, generation: u64, events: mpsc::Sender<Event>) -> Client {
    let (mut rd, mut wr) = stream.into_split();
    let (tx, mut rx) = mpsc::channel::<Outgoing>(1024);

    tokio::spawn(async move {
        while let Some(out) = rx.recv().await {
            match out {
                Outgoing::Frame(frame) => {
                    let detached = matches!(frame, ServerFrame::Detached(_) | ServerFrame::Refused(_));
                    if frame::send(&mut wr, &frame).await.is_err() || detached {
                        break;
                    }
                }
                Outgoing::Flush(done) => {
                    let _ = wr.flush().await;
                    let _ = done.send(());
                }
            }
        }
        let _ = wr.shutdown().await;
    });

    tokio::spawn(async move {
        loop {
            match frame::recv::<ClientFrame, _>(&mut rd).await {
                Ok(f) => {
                    if events.send(Event::Frame(generation, f)).await.is_err() {
                        return;
                    }
                }
                Err(_) => {
                    let _ = events.send(Event::ClientGone(generation)).await;
                    return;
                }
            }
        }
    });

    Client {
        generation,
        welcomed: false,
        tx,
    }
}

fn resize(io: &ChildIo, size: TtySize) {
    if let ChildIo::Pty(master) = io {
        let ws = Winsize {
            ws_row: size.rows,
            ws_col: size.cols,
            ws_xpixel: 0,
            ws_ypixel: 0,
        };
        // SAFETY: TIOCSWINSZ reads a `winsize` from the pointer, which is valid for the call.
        unsafe {
            libc::ioctl(
                master.get_ref().as_raw_fd(),
                libc::TIOCSWINSZ,
                &ws as *const Winsize,
            );
        }
    }
}

async fn write_input(io: &mut ChildIo, mut data: &[u8]) {
    match io {
        ChildIo::Pty(master) => {
            while !data.is_empty() {
                let Ok(mut guard) = master.writable().await else {
                    return;
                };
                match guard.try_io(|fd| nix::unistd::write(fd.get_ref(), data).map_err(io::Error::from)) {
                    Ok(Ok(n)) => data = &data[n..],
                    Ok(Err(_)) => return,
                    Err(_would_block) => continue,
                }
            }
        }
        ChildIo::Pipes { stdin } => {
            if let Some(s) = stdin
                && s.write_all(data).await.is_err()
            {
                stdin.take();
            }
        }
    }
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

/// Starts the session's child and the tasks that read its output.
fn start_child(
    spec: &SpawnSpec,
    account: &Account,
    events: mpsc::Sender<Event>,
) -> io::Result<(tokio::process::Child, ChildIo, i32)> {
    let env = environment(spec, account);
    let cwd = spec.cwd.clone().unwrap_or_else(|| account.home.clone());

    let mut cmd = tokio::process::Command::new(&spec.argv[0]);
    cmd.args(&spec.argv[1..])
        .env_clear()
        .envs(env)
        .current_dir(&cwd)
        .kill_on_drop(false);

    let drop_to = (nix::unistd::geteuid().as_raw() != account.uid)
        .then(|| {
            CString::new(account.name.clone())
                .map(|name| (name, account.uid, account.gid))
                .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "user name contains NUL"))
        })
        .transpose()?;

    let (io, pty_master) = match spec.tty {
        Some(size) => {
            let ws = Winsize {
                ws_row: size.rows,
                ws_col: size.cols,
                ws_xpixel: 0,
                ws_ypixel: 0,
            };
            let pty = openpty(Some(&ws), None).map_err(io::Error::from)?;
            cmd.stdin(Stdio::from(pty.slave.try_clone()?))
                .stdout(Stdio::from(pty.slave.try_clone()?))
                .stderr(Stdio::from(pty.slave));
            set_nonblocking(&pty.master)?;
            let master = std::sync::Arc::new(AsyncFd::new(pty.master)?);
            (ChildIo::Pty(master.clone()), Some(master))
        }
        None => {
            cmd.stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped());
            (ChildIo::Pipes { stdin: None }, None)
        }
    };
    let with_tty = pty_master.is_some();

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
            if let Some((name, uid, gid)) = &drop_to {
                if libc::initgroups(name.as_ptr(), *gid) < 0 && libc::setgroups(1, gid) < 0 {
                    return Err(io::Error::last_os_error());
                }
                if libc::setgid(*gid) < 0 || libc::setuid(*uid) < 0 {
                    return Err(io::Error::last_os_error());
                }
            }
            Ok(())
        });
    }

    let mut child = cmd.spawn()?;
    drop(cmd);
    let pgid = child.id().map(|p| p as i32).unwrap_or(0);

    let io = match (io, pty_master) {
        (ChildIo::Pty(_), Some(master)) => {
            let tx = events.clone();
            let reader = master.clone();
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
                            if tx.send(Event::Output(buf[..n].to_vec(), false)).await.is_err() {
                                return;
                            }
                        }
                        Err(_would_block) => continue,
                    }
                }
                let _ = tx.send(Event::OutputEnd).await;
            });
            ChildIo::Pty(master)
        }
        _ => {
            let stdin = child.stdin.take();
            let stdout = child.stdout.take();
            let stderr = child.stderr.take();
            let tx = events.clone();
            tokio::spawn(async move {
                let out = pump(stdout, false, tx.clone());
                let err = pump(stderr, true, tx.clone());
                tokio::join!(out, err);
                let _ = tx.send(Event::OutputEnd).await;
            });
            ChildIo::Pipes { stdin }
        }
    };

    Ok((child, io, pgid))
}

async fn pump<R: tokio::io::AsyncRead + Unpin>(r: Option<R>, stderr: bool, tx: mpsc::Sender<Event>) {
    let Some(mut r) = r else { return };
    let mut buf = vec![0u8; 16 * 1024];
    loop {
        match r.read(&mut buf).await {
            Ok(0) | Err(_) => return,
            Ok(n) => {
                if tx.send(Event::Output(buf[..n].to_vec(), stderr)).await.is_err() {
                    return;
                }
            }
        }
    }
}
