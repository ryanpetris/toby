//! Log WebSockets (plan §18): a machine's host processes, and an isolated
//! MCP server's standard error.

use std::sync::Arc;
use std::time::Duration;

use axum::extract::ws::{Message, WebSocket};
use toby_config::global::Backend;
use toby_proto::types::Identity;
use tokio::io::{AsyncBufReadExt, BufReader};

use crate::server::Daemon;

/// Lines already in a log that are sent first.
const LINES: usize = 200;
/// How often an MCP server's log is read again.
const POLL: Duration = Duration::from_secs(2);

async fn send(socket: &mut WebSocket, text: String) -> bool {
    socket.send(Message::Text(text.into())).await.is_ok()
}

/// Whether the client went away; other messages are ignored.
async fn closed(socket: &mut WebSocket) -> bool {
    !matches!(socket.recv().await, Some(Ok(m)) if !matches!(m, Message::Close(_)))
}

/// Streams the logs of machine `id`'s host processes, one line a message.
pub async fn machine(d: Arc<Daemon>, id: String, mut socket: WebSocket) {
    let units = ["vm", "machine", "fs", "net"].map(|p| format!("toby-{p}@{id}"));
    let mut cmd = match d.machines.supervisor.backend() {
        Backend::SystemdUser => {
            let mut c = tokio::process::Command::new("journalctl");
            c.args(["--user", "--no-pager", "-o", "short-iso", "-f", "-n", &LINES.to_string()]);
            for u in &units {
                c.args(["-u", u]);
            }
            c
        }
        Backend::Direct => {
            let logs = d.machines.paths.state.join("logs");
            let files: Vec<_> = std::fs::read_dir(&logs)
                .map(|entries| entries.flatten().map(|e| e.path()).collect())
                .unwrap_or_default();
            let files: Vec<_> = files
                .into_iter()
                .filter(|p| {
                    p.file_name().and_then(|n| n.to_str()).is_some_and(|n| {
                        units.iter().any(|u| n.starts_with(&format!("{u}."))) && n.ends_with(".log")
                    })
                })
                .collect();
            if files.is_empty() {
                let _ = send(&mut socket, format!("machine {id} has no logs")).await;
                return;
            }
            let mut c = tokio::process::Command::new("tail");
            c.args(["-q", "-n", &LINES.to_string(), "-F"]).args(files);
            c
        }
    };
    cmd.stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::null())
        .kill_on_drop(true);
    let Ok(mut child) = cmd.spawn() else {
        let _ = send(&mut socket, "the logs cannot be read".into()).await;
        return;
    };
    let mut lines = BufReader::new(child.stdout.take().expect("piped")).lines();
    loop {
        tokio::select! {
            line = lines.next_line() => match line {
                Ok(Some(l)) => if !send(&mut socket, l).await { return },
                _ => return,
            },
            gone = closed(&mut socket) => if gone { return },
        }
    }
}

/// Streams an isolated MCP server's standard error while its services
/// machine runs, reading the log in the machine every two seconds.
pub async fn mcp(d: Arc<Daemon>, name: String, mut socket: WebSocket) {
    let pair = crate::services::pair_name(&name);
    let Some(spec) = d.machines.records().into_iter().find(|s| s.home.as_deref() == Some(pair.as_str()))
    else {
        let _ = send(&mut socket, format!("{name} has no services machine")).await;
        return;
    };
    let log = toby_guest::helper::serve::LOG;
    let mut offset: Option<u64> = None;
    loop {
        if d.machines.observe(&spec.id).await.state != "ready" {
            if !send(&mut socket, format!("{name}'s machine is not running")).await {
                return;
            }
        } else {
            // The size first, then what is new (or the last lines at first).
            let read = match offset {
                None => format!("wc -c < \"$HOME/{log}\"; tail -n {LINES} \"$HOME/{log}\""),
                Some(n) => format!("wc -c < \"$HOME/{log}\"; tail -c +{} \"$HOME/{log}\"", n + 1),
            };
            let mut out = Vec::new();
            let mut collect = |b: &[u8], stderr: bool| {
                if !stderr {
                    out.extend_from_slice(b);
                }
            };
            let argv = vec!["sh".into(), "-c".into(), read];
            let runtime = d.machines.runtime(&spec.id);
            if crate::control::run(&runtime, argv, Identity::User, Vec::new(), &mut collect).await.is_ok() {
                let text = String::from_utf8_lossy(&out);
                let (size, rest) = text.split_once('\n').unwrap_or((&text, ""));
                let size: u64 = size.trim().parse().unwrap_or(0);
                offset = match offset {
                    // The log started over: read it from its beginning.
                    Some(n) if size < n => {
                        offset = Some(0);
                        continue;
                    }
                    Some(n) => Some(n + rest.len() as u64),
                    None => Some(size),
                };
                for line in rest.lines() {
                    if !send(&mut socket, line.to_string()).await {
                        return;
                    }
                }
            }
        }
        tokio::select! {
            _ = tokio::time::sleep(POLL) => {}
            gone = closed(&mut socket) => if gone { return },
        }
    }
}
