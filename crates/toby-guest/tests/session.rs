//! Runs sessions in-process and drives them over their sockets.

use std::time::Duration;

use toby_guest::paths::{GuestPaths, session_files};
use toby_guest::record::{self, UserInfo};
use toby_guest::session;
use toby_proto::frame;
use toby_proto::session::{ClientFrame, Hello, Resize, ServerFrame, Signal, State, Stdin};
use toby_proto::types::{ExitStatus, Identity, SpawnSpec, TtySize};
use tokio::net::UnixStream;

struct Env {
    _dir: tempfile::TempDir,
    paths: GuestPaths,
}

fn env() -> Env {
    let dir = tempfile::tempdir().unwrap();
    let paths = GuestPaths::at(dir.path());
    std::fs::create_dir_all(paths.root()).unwrap();
    let user = UserInfo {
        name: std::env::var("USER").unwrap_or_else(|_| "user".into()),
        uid: nix::unistd::getuid().as_raw(),
        gid: nix::unistd::getgid().as_raw(),
        home: dir.path().display().to_string(),
        shell: "/bin/sh".into(),
    };
    record::write(&paths.user_file(), &user).unwrap();
    Env { _dir: dir, paths }
}

fn spec_on_attach(id: &str, argv: &[&str]) -> SpawnSpec {
    SpawnSpec { start_on_attach: true, ..spec(id, argv, false, true) }
}

fn spec(id: &str, argv: &[&str], tty: bool, keep: bool) -> SpawnSpec {
    SpawnSpec {
        session_id: id.into(),
        argv: argv.iter().map(|s| s.to_string()).collect(),
        env: vec![("TOBY_TEST".into(), "1".into())],
        cwd: None,
        identity: Identity::User,
        tty: tty.then_some(TtySize { rows: 24, cols: 80 }),
        keep_after_exit: keep,
        start_on_attach: false,
        tool: None,
    }
}

async fn start(env: &Env, spec: SpawnSpec) -> tokio::task::JoinHandle<std::io::Result<()>> {
    session::prepare(&env.paths, &spec).unwrap();
    let paths = env.paths.clone();
    let id = spec.session_id.clone();
    let task = tokio::spawn(async move { session::run(paths, &id).await });
    let sock = env.paths.session_dir(&spec.session_id).join(session_files::SOCKET);
    for _ in 0..200 {
        if sock.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    task
}

async fn attach(env: &Env, id: &str, replay: bool) -> UnixStream {
    let sock = env.paths.session_dir(id).join(session_files::SOCKET);
    let mut s = UnixStream::connect(sock).await.unwrap();
    let hello = ClientFrame::Hello(Hello {
        versions: vec![1],
        rows: 0,
        cols: 0,
        want_replay: replay,
        resume_from: None,
    });
    frame::send(&mut s, &hello).await.unwrap();
    s
}

async fn next(s: &mut UnixStream) -> ServerFrame {
    tokio::time::timeout(Duration::from_secs(10), frame::recv(s)).await.expect("frame in time").unwrap()
}

/// Reads frames until the exit, collecting stdout, stderr and replay bytes.
async fn collect(s: &mut UnixStream) -> (String, String, ExitStatus) {
    let (mut out, mut err) = (Vec::new(), Vec::new());
    loop {
        match next(s).await {
            ServerFrame::Stdout(o) => out.extend(o.bytes),
            ServerFrame::Replay(r) => out.extend(r.bytes),
            ServerFrame::Stderr(e) => err.extend(e.bytes),
            ServerFrame::Exit(e) => {
                return (
                    String::from_utf8_lossy(&out).into(),
                    String::from_utf8_lossy(&err).into(),
                    e.status,
                );
            }
            ServerFrame::Welcome(_) => {}
            other => panic!("unexpected {other:?}"),
        }
    }
}

async fn read_until(s: &mut UnixStream, needle: &str) -> String {
    let mut out = Vec::new();
    loop {
        match next(s).await {
            ServerFrame::Stdout(o) => out.extend(o.bytes),
            ServerFrame::Replay(r) => out.extend(r.bytes),
            ServerFrame::Welcome(_) => {}
            other => panic!("unexpected {other:?}"),
        }
        let text = String::from_utf8_lossy(&out).to_string();
        if text.contains(needle) {
            return text;
        }
    }
}

#[tokio::test]
async fn pipes_carry_stdout_stderr_and_exit_code() {
    let env = env();
    let task = start(
        &env,
        spec("p1", &["sh", "-c", "read x; echo out; echo err >&2; echo $TOBY_TEST; exit 3"], false, true),
    )
    .await;
    let mut s = attach(&env, "p1", false).await;
    frame::send(&mut s, &ClientFrame::Stdin(Stdin { bytes: b"go\n".to_vec() })).await.unwrap();
    let (out, err, status) = collect(&mut s).await;
    assert_eq!(out, "out\n1\n");
    assert_eq!(err, "err\n");
    assert_eq!(status, ExitStatus::Code(3));
    task.await.unwrap().unwrap();
    assert!(!env.paths.session_dir("p1").exists());
}

#[tokio::test]
async fn tty_sessions_get_a_terminal_and_input() {
    let env = env();
    let task = start(&env, spec("t1", &["sh", "-c", "stty size; read x; echo got:$x"], true, false)).await;
    let mut s = attach(&env, "t1", true).await;
    read_until(&mut s, "24 80").await;
    frame::send(&mut s, &ClientFrame::Stdin(Stdin { bytes: b"hi\n".to_vec() })).await.unwrap();
    let (out, _, status) = collect(&mut s).await;
    assert!(out.contains("got:hi"), "{out:?}");
    assert_eq!(status, ExitStatus::Code(0));
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn resize_reaches_the_terminal() {
    let env = env();
    let _task = start(&env, spec("r1", &["sh", "-c", "read x; stty size; read y"], true, false)).await;
    let mut s = attach(&env, "r1", false).await;
    assert!(matches!(next(&mut s).await, ServerFrame::Welcome(_)));
    frame::send(&mut s, &ClientFrame::Resize(Resize { rows: 40, cols: 132 })).await.unwrap();
    frame::send(&mut s, &ClientFrame::Stdin(Stdin { bytes: b"\n".to_vec() })).await.unwrap();
    read_until(&mut s, "40 132").await;
    frame::send(&mut s, &ClientFrame::Stdin(Stdin { bytes: b"\n".to_vec() })).await.unwrap();
}

#[tokio::test]
async fn newest_attach_wins_and_replays_output() {
    let env = env();
    let task = start(&env, spec("n1", &["cat"], true, false)).await;
    let mut a = attach(&env, "n1", false).await;
    frame::send(&mut a, &ClientFrame::Stdin(Stdin { bytes: b"first\n".to_vec() })).await.unwrap();
    read_until(&mut a, "first").await;

    let mut b = attach(&env, "n1", true).await;
    let seen = read_until(&mut b, "first").await;
    assert!(seen.contains("first"));
    loop {
        match next(&mut a).await {
            ServerFrame::Detached(_) => break,
            ServerFrame::Stdout(_) => {}
            other => panic!("unexpected {other:?}"),
        }
    }

    frame::send(&mut b, &ClientFrame::Signal(Signal { signal: libc::SIGTERM })).await.unwrap();
    let (_, _, status) = collect(&mut b).await;
    assert_eq!(status, ExitStatus::Signal(libc::SIGTERM));
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn exit_is_kept_until_collected() {
    let env = env();
    let task = start(&env, spec("k1", &["sh", "-c", "echo done; exit 7"], false, true)).await;
    let exit_file = env.paths.session_dir("k1").join(session_files::EXIT);
    for _ in 0..500 {
        if exit_file.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert!(exit_file.exists());

    let mut s = attach(&env, "k1", true).await;
    match next(&mut s).await {
        ServerFrame::Welcome(w) => assert_eq!(w.state, State::Exited(ExitStatus::Code(7))),
        other => panic!("unexpected {other:?}"),
    }
    let (out, _, status) = collect(&mut s).await;
    assert_eq!(out, "done\n");
    assert_eq!(status, ExitStatus::Code(7));
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn unsupported_version_is_refused() {
    let env = env();
    let _task = start(&env, spec("v1", &["sleep", "5"], false, false)).await;
    let sock = env.paths.session_dir("v1").join(session_files::SOCKET);
    let mut s = UnixStream::connect(sock).await.unwrap();
    let hello = ClientFrame::Hello(Hello {
        versions: vec![99],
        rows: 0,
        cols: 0,
        want_replay: false,
        resume_from: None,
    });
    frame::send(&mut s, &hello).await.unwrap();
    assert!(matches!(next(&mut s).await, ServerFrame::Refused(_)));
}

#[tokio::test]
async fn start_on_attach_streams_everything() {
    let env = env();
    // More output than the replay buffer holds, written before anything else.
    let task =
        start(&env, spec_on_attach("a1", &["sh", "-c", "head -c 3000000 /dev/zero; echo tail >&2"])).await;
    let mut s = attach(&env, "a1", true).await;
    let mut out = 0usize;
    let mut err = Vec::new();
    loop {
        match next(&mut s).await {
            ServerFrame::Stdout(o) => out += o.bytes.len(),
            ServerFrame::Stderr(e) => err.extend(e.bytes),
            ServerFrame::Replay(r) => panic!("unexpected replay of {} bytes", r.bytes.len()),
            ServerFrame::Welcome(_) => {}
            ServerFrame::Exit(e) => {
                assert_eq!(e.status, ExitStatus::Code(0));
                break;
            }
            other => panic!("unexpected {other:?}"),
        }
    }
    assert_eq!(out, 3_000_000);
    assert_eq!(err, b"tail\n");
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn a_command_that_cannot_start_exits_127() {
    let env = env();
    let task = start(&env, spec_on_attach("m1", &["/no/such/command"])).await;
    let mut s = attach(&env, "m1", false).await;
    let (_, err, status) = collect(&mut s).await;
    assert_eq!(status, ExitStatus::Code(127));
    assert!(err.contains("cannot start /no/such/command"), "{err:?}");
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn replay_keeps_stderr_apart() {
    let env = env();
    let task = start(&env, spec("e1", &["sh", "-c", "echo out; echo err >&2; exit 0"], false, true)).await;
    let exit_file = env.paths.session_dir("e1").join(session_files::EXIT);
    for _ in 0..500 {
        if exit_file.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let mut s = attach(&env, "e1", true).await;
    let (mut out, mut err) = (Vec::new(), Vec::new());
    loop {
        match next(&mut s).await {
            ServerFrame::Replay(r) if r.stderr => err.extend(r.bytes),
            ServerFrame::Replay(r) => out.extend(r.bytes),
            ServerFrame::Welcome(_) => {}
            ServerFrame::Exit(_) => break,
            other => panic!("unexpected {other:?}"),
        }
    }
    assert_eq!(out, b"out\n");
    assert_eq!(err, b"err\n");
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn a_missing_command_fails_the_start() {
    let env = env();
    let spec = spec("f1", &["/no/such/command"], false, false);
    toby_guest::session::prepare(&env.paths, &spec).unwrap();
    let err = toby_guest::session::run(env.paths.clone(), "f1").await.unwrap_err();
    assert_eq!(err.kind(), std::io::ErrorKind::NotFound);
    assert!(!env.paths.session_dir("f1").exists());
}

#[tokio::test]
async fn a_stalled_client_does_not_block_a_takeover() {
    let env = env();
    let task = start(&env, spec_on_attach("s1", &["sh", "-c", "yes"])).await;
    // The first client attaches and never reads.
    let _stalled = attach(&env, "s1", false).await;
    tokio::time::sleep(Duration::from_millis(500)).await;

    let mut second = attach(&env, "s1", false).await;
    let welcome = tokio::time::timeout(Duration::from_secs(5), frame::recv::<ServerFrame, _>(&mut second))
        .await
        .expect("the second client is welcomed")
        .unwrap();
    assert!(matches!(welcome, ServerFrame::Welcome(_)), "{welcome:?}");

    frame::send(&mut second, &ClientFrame::Signal(Signal { signal: libc::SIGKILL })).await.unwrap();
    loop {
        if let ServerFrame::Exit(e) = next(&mut second).await {
            assert_eq!(e.status, ExitStatus::Signal(libc::SIGKILL));
            break;
        }
    }
    task.await.unwrap().unwrap();
}

async fn attach_resume(env: &Env, id: &str, from: u64) -> UnixStream {
    let sock = env.paths.session_dir(id).join(session_files::SOCKET);
    let mut s = UnixStream::connect(sock).await.unwrap();
    let hello = ClientFrame::Hello(Hello {
        versions: vec![1],
        rows: 0,
        cols: 0,
        want_replay: false,
        resume_from: Some(from),
    });
    frame::send(&mut s, &hello).await.unwrap();
    s
}

/// Counts output bytes until the exit, returning the count and the status.
async fn count_until_exit(s: &mut UnixStream, limit: Option<usize>) -> (usize, Option<ExitStatus>) {
    let mut n = 0;
    loop {
        match tokio::time::timeout(Duration::from_secs(30), frame::recv::<ServerFrame, _>(s))
            .await
            .unwrap()
            .unwrap()
        {
            ServerFrame::Stdout(o) => n += o.bytes.len(),
            ServerFrame::Replay(r) => n += r.bytes.len(),
            ServerFrame::Stderr(_) | ServerFrame::Welcome(_) => {}
            ServerFrame::Exit(e) => return (n, Some(e.status)),
            other => panic!("unexpected {other:?}"),
        }
        if limit.is_some_and(|l| n >= l) {
            return (n, None);
        }
    }
}

#[tokio::test]
async fn a_slow_reader_gets_all_output_after_the_exit() {
    let env = env();
    let total = 32 * 1024 * 1024;
    let cmd = format!("head -c {total} /dev/zero");
    let task = start(&env, spec_on_attach("slow", &["sh", "-c", &cmd])).await;
    let mut s = attach(&env, "slow", false).await;
    // Far more output than the client's queue holds, and the command has
    // long exited before the client starts reading.
    tokio::time::sleep(Duration::from_secs(3)).await;
    let (n, status) = count_until_exit(&mut s, None).await;
    assert_eq!(n, total);
    assert_eq!(status, Some(ExitStatus::Code(0)));
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn output_waits_for_a_reconnecting_client() {
    let env = env();
    let total = 8 * 1024 * 1024;
    let cmd = format!("head -c {total} /dev/zero");
    let task = start(&env, spec_on_attach("rc", &["sh", "-c", &cmd])).await;
    let mut first = attach(&env, "rc", false).await;
    let (got, status) = count_until_exit(&mut first, Some(1024 * 1024)).await;
    assert!(status.is_none());
    drop(first);
    // More than the replay buffer is produced while no client is attached.
    tokio::time::sleep(Duration::from_secs(2)).await;
    let mut second = attach_resume(&env, "rc", got as u64).await;
    let welcome = frame::recv::<ServerFrame, _>(&mut second).await.unwrap();
    let ServerFrame::Welcome(w) = welcome else { panic!("{welcome:?}") };
    assert_eq!((w.offset, w.lost), (got as u64, 0));
    let (rest, status) = count_until_exit(&mut second, None).await;
    assert_eq!(got + rest, total);
    assert_eq!(status, Some(ExitStatus::Code(0)));
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn the_exit_reaches_a_client_that_takes_over() {
    let env = env();
    let task = start(&env, spec_on_attach("x1", &["sh", "-c", "head -c 2000000 /dev/zero; exit 5"])).await;
    // The first client starts the command and never reads.
    let _stalled = attach(&env, "x1", false).await;
    tokio::time::sleep(Duration::from_secs(1)).await;

    let mut second = attach_resume(&env, "x1", 0).await;
    let (_, status) = count_until_exit(&mut second, None).await;
    assert_eq!(status, Some(ExitStatus::Code(5)));
    task.await.unwrap().unwrap();
}
