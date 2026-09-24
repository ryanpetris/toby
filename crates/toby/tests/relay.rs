//! Drives the relay over in-memory streams with sessions run by the `toby`
//! binary itself.

use std::sync::{Arc, Mutex};
use std::time::Duration;

use toby_guest::paths::GuestPaths;
use toby_guest::record::{self, UserInfo};
use toby_guest::relay::{BoxStream, Connector, Launcher, Relay};
use toby_proto::frame;
use toby_proto::relay::{self, Request, Response};
use toby_proto::session::{ClientFrame, Hello, ServerFrame};
use toby_proto::stream::{Accepted, Control, Dial, GuestHeader, HostHeader, Reply, SessionAttach};
use toby_proto::types::{Endpoint, ExitStatus, Identity, SpawnSpec};
use tokio::io::{AsyncReadExt, AsyncWriteExt, DuplexStream};

const BIN: &str = env!("CARGO_BIN_EXE_toby");

struct Env {
    dir: tempfile::TempDir,
    relay: Arc<Relay>,
    /// Streams the relay opened towards the host, with their headers still unread.
    accepted: Arc<Mutex<Vec<DuplexStream>>>,
}

fn env() -> Env {
    let dir = tempfile::tempdir().unwrap();
    let paths = GuestPaths::at(dir.path());
    let user = UserInfo {
        name: std::env::var("USER").unwrap_or_else(|_| "user".into()),
        uid: nix::unistd::getuid().as_raw(),
        gid: nix::unistd::getgid().as_raw(),
        home: dir.path().display().to_string(),
        shell: "/bin/sh".into(),
    };
    record::write(&paths.user_file(), &user).unwrap();

    let accepted = Arc::new(Mutex::new(Vec::new()));
    let sink = accepted.clone();
    let connector: Connector = Arc::new(move || {
        let (host, guest) = tokio::io::duplex(1 << 16);
        sink.lock().unwrap().push(host);
        Box::pin(async move { Ok(Box::new(guest) as BoxStream) })
    });
    let relay = Relay::new(paths, BIN.into(), Launcher::Direct, connector);
    Env { dir, relay, accepted }
}

fn open(env: &Env) -> DuplexStream {
    let (host, guest) = tokio::io::duplex(1 << 16);
    tokio::spawn(env.relay.clone().handle(guest));
    host
}

async fn control(env: &Env) -> DuplexStream {
    let mut c = open(env);
    frame::send(&mut c, &HostHeader::Control(Control { proto_versions: vec![1] })).await.unwrap();
    let reply: Reply = frame::recv(&mut c).await.unwrap();
    assert_eq!(reply.into_result().unwrap(), Some(1));
    c
}

async fn call(c: &mut DuplexStream, req: Request) -> Response {
    frame::send(c, &req).await.unwrap();
    tokio::time::timeout(Duration::from_secs(15), frame::recv(c)).await.unwrap().unwrap()
}

fn spec(id: &str, argv: &[&str]) -> SpawnSpec {
    SpawnSpec {
        session_id: id.into(),
        argv: argv.iter().map(|s| s.to_string()).collect(),
        env: vec![],
        cwd: None,
        identity: Identity::User,
        tty: None,
        keep_after_exit: true,
        start_on_attach: false,
    }
}

async fn attach(env: &Env, id: &str) -> DuplexStream {
    let mut s = open(env);
    frame::send(&mut s, &HostHeader::SessionAttach(SessionAttach { session_id: id.into() })).await.unwrap();
    let reply: Reply = frame::recv(&mut s).await.unwrap();
    reply.into_result().unwrap();
    let hello = ClientFrame::Hello(Hello {
        versions: vec![1],
        rows: 0,
        cols: 0,
        want_replay: true,
        resume_from: None,
    });
    frame::send(&mut s, &hello).await.unwrap();
    s
}

async fn wait_exit(s: &mut DuplexStream) -> (String, ExitStatus) {
    let mut out = Vec::new();
    loop {
        let f: ServerFrame =
            tokio::time::timeout(Duration::from_secs(15), frame::recv(s)).await.unwrap().unwrap();
        match f {
            ServerFrame::Stdout(o) => out.extend(o.bytes),
            ServerFrame::Replay(r) => out.extend(r.bytes),
            ServerFrame::Exit(e) => return (String::from_utf8_lossy(&out).into(), e.status),
            _ => {}
        }
    }
}

#[tokio::test]
async fn spawn_attach_list_and_forget() {
    let env = env();
    let mut c = control(&env).await;

    let resp = call(
        &mut c,
        Request::Spawn(relay::Spawn { spec: spec("s1", &["sh", "-c", "echo hi; exit 4"]), version: None }),
    )
    .await;
    assert_eq!(resp, Response::Spawned(relay::Spawned { session_id: "s1".into() }));

    let mut s = attach(&env, "s1").await;
    let (out, status) = wait_exit(&mut s).await;
    assert_eq!(out, "hi\n");
    assert_eq!(status, ExitStatus::Code(4));

    // The session is gone once its exit was collected.
    for _ in 0..200 {
        if !env.dir.path().join("sessions/s1").exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let Response::SessionList(list) = call(&mut c, Request::Sessions(relay::Sessions {})).await else {
        panic!("expected a session list");
    };
    assert!(list.sessions.is_empty());
}

#[tokio::test]
async fn kill_and_forget_sessions() {
    let env = env();
    let mut c = control(&env).await;
    call(&mut c, Request::Spawn(relay::Spawn { spec: spec("k1", &["sleep", "30"]), version: None })).await;

    let Response::SessionList(list) = call(&mut c, Request::Sessions(relay::Sessions {})).await else {
        panic!("expected a session list");
    };
    assert_eq!(list.sessions.len(), 1);
    assert_eq!(list.sessions[0].argv0, "sleep");

    let resp =
        call(&mut c, Request::Kill(relay::Kill { session_id: "k1".into(), signal: libc::SIGKILL })).await;
    assert_eq!(resp, Response::Done(relay::Done {}));

    let mut exited = false;
    for _ in 0..500 {
        if let Response::SessionList(l) = call(&mut c, Request::Sessions(relay::Sessions {})).await
            && l.sessions.first().and_then(|s| s.exit) == Some(ExitStatus::Signal(libc::SIGKILL))
        {
            exited = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert!(exited);

    let resp = call(&mut c, Request::Forget(relay::Forget { session_id: "k1".into() })).await;
    assert_eq!(resp, Response::Done(relay::Done {}));
    for _ in 0..500 {
        if !env.dir.path().join("sessions/k1").exists() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("forgotten session still exists");
}

#[tokio::test]
async fn bad_requests_fail_cleanly() {
    let env = env();
    let mut c = control(&env).await;
    let resp =
        call(&mut c, Request::Spawn(relay::Spawn { spec: spec("../x", &["true"]), version: None })).await;
    assert!(matches!(resp, Response::Failed(_)));
    let resp = call(
        &mut c,
        Request::Spawn(relay::Spawn { spec: spec("v1", &["true"]), version: Some("../..".into()) }),
    )
    .await;
    assert!(matches!(resp, Response::Failed(_)));
    let resp = call(&mut c, Request::Kill(relay::Kill { session_id: "missing".into(), signal: 15 })).await;
    assert!(matches!(resp, Response::Failed(_)));

    let mut s = open(&env);
    frame::send(&mut s, &HostHeader::SessionAttach(SessionAttach { session_id: "../../etc".into() }))
        .await
        .unwrap();
    assert!(frame::recv::<Reply, _>(&mut s).await.unwrap().into_result().is_err());

    let mut s = open(&env);
    frame::send(&mut s, &HostHeader::Control(Control { proto_versions: vec![7] })).await.unwrap();
    assert!(frame::recv::<Reply, _>(&mut s).await.unwrap().into_result().is_err());
}

#[tokio::test]
async fn dial_splices_to_a_guest_endpoint() {
    let env = env();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap().to_string();
    tokio::spawn(async move {
        let (mut s, _) = listener.accept().await.unwrap();
        let mut buf = [0u8; 4];
        s.read_exact(&mut buf).await.unwrap();
        s.write_all(&buf.map(|b| b.to_ascii_uppercase())).await.unwrap();
    });

    let mut c = open(&env);
    frame::send(&mut c, &HostHeader::Dial(Dial { target: Endpoint::Tcp { addr } })).await.unwrap();
    frame::recv::<Reply, _>(&mut c).await.unwrap().into_result().unwrap();
    c.write_all(b"ping").await.unwrap();
    let mut buf = [0u8; 4];
    c.read_exact(&mut buf).await.unwrap();
    assert_eq!(&buf, b"PING");
}

#[tokio::test]
async fn guest_listeners_report_accepted_connections() {
    let env = env();
    let mut c = control(&env).await;
    let path = env.dir.path().join("listen.sock");
    let resp = call(
        &mut c,
        Request::Listen(relay::Listen {
            listener_id: "f1".into(),
            bind: Endpoint::Unix { path: path.display().to_string() },
            mode: Some(0o666),
        }),
    )
    .await;
    assert_eq!(resp, Response::Done(relay::Done {}));

    let mut guest_client = tokio::net::UnixStream::connect(&path).await.unwrap();
    let mut host = loop {
        if let Some(s) = env.accepted.lock().unwrap().pop() {
            break s;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    };
    let header: GuestHeader = frame::recv(&mut host).await.unwrap();
    assert_eq!(header, GuestHeader::Accepted(Accepted { listener_id: "f1".into() }));
    frame::send(&mut host, &Reply::ok()).await.unwrap();

    guest_client.write_all(b"abc").await.unwrap();
    let mut buf = [0u8; 3];
    host.read_exact(&mut buf).await.unwrap();
    assert_eq!(&buf, b"abc");

    let resp = call(&mut c, Request::Unlisten(relay::Unlisten { listener_id: "f1".into() })).await;
    assert_eq!(resp, Response::Done(relay::Done {}));
}

#[tokio::test]
async fn repeated_spawns_are_idempotent() {
    let env = env();
    let mut c = control(&env).await;
    let spawn = relay::Spawn { spec: spec("r1", &["sleep", "30"]), version: None };
    for _ in 0..2 {
        let resp = call(&mut c, Request::Spawn(spawn.clone())).await;
        assert_eq!(resp, Response::Spawned(relay::Spawned { session_id: "r1".into() }));
    }
    let other = relay::Spawn { spec: spec("r1", &["sleep", "31"]), version: None };
    assert!(matches!(call(&mut c, Request::Spawn(other)).await, Response::Failed(_)));
    call(&mut c, Request::Kill(relay::Kill { session_id: "r1".into(), signal: libc::SIGKILL })).await;
}

#[tokio::test]
async fn failed_starts_report_the_reason() {
    let env = env();
    let mut c = control(&env).await;
    let resp =
        call(&mut c, Request::Spawn(relay::Spawn { spec: spec("x1", &["/no/such/command"]), version: None }))
            .await;
    let Response::Failed(f) = resp else { panic!("expected a failure, got {resp:?}") };
    assert!(f.error.contains("could not start"), "{}", f.error);
    assert!(!env.dir.path().join("sessions/x1").exists());
}

#[tokio::test]
async fn a_repeated_spawn_does_not_rerun_a_finished_command() {
    let env = env();
    let mut c = control(&env).await;
    let marker = env.dir.path().join("ran");
    let cmd = format!("echo x >> {}", marker.display());
    let mut spec = spec("once", &["sh", "-c", &cmd]);
    spec.keep_after_exit = false;
    let spawn = relay::Spawn { spec, version: None };

    call(&mut c, Request::Spawn(spawn.clone())).await;
    for _ in 0..500 {
        if !env.dir.path().join("sessions/once").exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let resp = call(&mut c, Request::Spawn(spawn)).await;
    assert_eq!(resp, Response::Spawned(relay::Spawned { session_id: "once".into() }));
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(std::fs::read_to_string(&marker).unwrap(), "x\n");
}

#[tokio::test]
async fn a_failed_spawn_can_be_retried() {
    let env = env();
    let mut c = control(&env).await;
    let spawn = relay::Spawn { spec: spec("retry", &["/no/such/command"]), version: None };
    for _ in 0..2 {
        assert!(matches!(call(&mut c, Request::Spawn(spawn.clone())).await, Response::Failed(_)));
    }
}

#[tokio::test]
async fn unsupported_protocol_versions_are_refused() {
    let env = env();
    // Relay control: only versions both sides know.
    let mut c = open(&env);
    frame::send(&mut c, &HostHeader::Control(Control { proto_versions: vec![2] })).await.unwrap();
    let reply: Reply = frame::recv(&mut c).await.unwrap();
    assert!(reply.into_result().is_err());
    let mut c = open(&env);
    frame::send(&mut c, &HostHeader::Control(Control { proto_versions: vec![1, 2] })).await.unwrap();
    let reply: Reply = frame::recv(&mut c).await.unwrap();
    assert_eq!(reply.into_result().unwrap(), Some(1));

    // Session protocol: a client that only knows a later version is refused.
    let mut c = control(&env).await;
    let r =
        call(&mut c, Request::Spawn(relay::Spawn { spec: spec("s1", &["sleep", "5"]), version: None })).await;
    assert!(matches!(r, Response::Spawned(_)), "{r:?}");
    let mut s = open(&env);
    frame::send(&mut s, &HostHeader::SessionAttach(SessionAttach { session_id: "s1".into() })).await.unwrap();
    let _: Reply = frame::recv(&mut s).await.unwrap();
    let hello = ClientFrame::Hello(Hello {
        versions: vec![2],
        rows: 0,
        cols: 0,
        want_replay: false,
        resume_from: None,
    });
    frame::send(&mut s, &hello).await.unwrap();
    let f: ServerFrame =
        tokio::time::timeout(Duration::from_secs(15), frame::recv(&mut s)).await.unwrap().unwrap();
    assert!(matches!(f, ServerFrame::Refused(_)), "{f:?}");
    call(&mut c, Request::Kill(relay::Kill { session_id: "s1".into(), signal: libc::SIGKILL })).await;
}

/// Output up to the first newline.
async fn first_line(s: &mut DuplexStream) -> String {
    let mut out = Vec::new();
    while !out.contains(&b'\n') {
        let f: ServerFrame =
            tokio::time::timeout(Duration::from_secs(15), frame::recv(s)).await.unwrap().unwrap();
        match f {
            ServerFrame::Stdout(o) => out.extend(o.bytes),
            ServerFrame::Replay(r) => out.extend(r.bytes),
            _ => {}
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// An upgrade installs a new version next to the old one: sessions started
/// before keep running the old binary, new ones run the new binary, and the
/// relay reports each session's version.
#[tokio::test]
async fn sessions_keep_their_version_across_an_upgrade() {
    let env = env();
    for v in ["1.0.0", "1.1.0"] {
        let dir = env.dir.path().join("fs/versions").join(v);
        std::fs::create_dir_all(&dir).unwrap();
        std::os::unix::fs::symlink(BIN, dir.join("toby")).unwrap();
    }
    let mut c = control(&env).await;
    // The command prints the session process's command line, then waits.
    let show = ["sh", "-c", "tr '\\0' ' ' < /proc/$PPID/cmdline; echo; read x || true"];
    let spawn = |id: &'static str, version: &'static str| {
        let mut sp = spec(id, &show);
        sp.start_on_attach = true;
        Request::Spawn(relay::Spawn { spec: sp, version: Some(version.into()) })
    };
    assert!(matches!(call(&mut c, spawn("old", "1.0.0")).await, Response::Spawned(_)));
    let mut old = attach(&env, "old").await;
    let line = first_line(&mut old).await;
    assert!(line.contains("/versions/1.0.0/toby guest session"), "{line}");

    // After the upgrade (`current` moved), new sessions get the new version.
    assert!(matches!(call(&mut c, spawn("new", "1.1.0")).await, Response::Spawned(_)));
    let mut new = attach(&env, "new").await;
    let line = first_line(&mut new).await;
    assert!(line.contains("/versions/1.1.0/toby guest session"), "{line}");

    let Response::SessionList(list) = call(&mut c, Request::Sessions(relay::Sessions {})).await else {
        panic!("no session list")
    };
    let version = |id: &str| list.sessions.iter().find(|s| s.id == id).and_then(|s| s.version.clone());
    assert_eq!(version("old").as_deref(), Some("1.0.0"));
    assert_eq!(version("new").as_deref(), Some("1.1.0"));
    assert!(list.sessions.iter().all(|s| s.exit.is_none()), "both still run");

    for s in [&mut old, &mut new] {
        frame::send(s, &ClientFrame::CloseStdin(toby_proto::session::CloseStdin {})).await.unwrap();
        let (_, status) = wait_exit(s).await;
        assert_eq!(status, ExitStatus::Code(0));
    }
}
