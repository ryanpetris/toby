//! Log WebSockets (plan §18): a machine's host processes, and an isolated
//! MCP server's standard error.

use std::sync::Arc;
use std::time::Duration;

use axum::extract::ws::{Message, WebSocket};
use toby_config::global::Backend;
use toby_proto::types::{Identity, SpawnSpec};
use tokio::io::AsyncReadExt;
use tokio::sync::mpsc;

use crate::server::Daemon;

/// Lines already in a log that are sent first.
const LINES: usize = 200;
/// How often a stopped services machine is looked at again.
const POLL: Duration = Duration::from_secs(2);
/// Longer lines are sent in parts.
const MAX_LINE: usize = 64 * 1024;

async fn send(socket: &mut WebSocket, text: String) -> bool {
    socket.send(Message::Text(text.into())).await.is_ok()
}

/// Whether the client went away; other messages are ignored.
async fn closed(socket: &mut WebSocket) -> bool {
    !matches!(socket.recv().await, Some(Ok(m)) if !matches!(m, Message::Close(_)))
}

/// Splits output into lines, whatever its encoding.
#[derive(Default)]
struct Lines(Vec<u8>);

impl Lines {
    fn push(&mut self, bytes: &[u8]) -> Vec<String> {
        self.0.extend_from_slice(bytes);
        let mut out = Vec::new();
        loop {
            let (end, newline) = match self.0.iter().position(|&b| b == b'\n') {
                Some(i) if i <= MAX_LINE => (i, 1),
                _ if self.0.len() >= MAX_LINE => (MAX_LINE, 0),
                _ => break,
            };
            let line: Vec<u8> = self.0.drain(..end).collect();
            self.0.drain(..newline);
            out.push(String::from_utf8_lossy(&line).into_owned());
        }
        out
    }
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
    let mut stdout = child.stdout.take().expect("piped");
    let mut lines = Lines::default();
    let mut buf = vec![0u8; 16 * 1024];
    loop {
        tokio::select! {
            n = stdout.read(&mut buf) => match n {
                Ok(n) if n > 0 => {
                    for l in lines.push(&buf[..n]) {
                        if !send(&mut socket, l).await {
                            return;
                        }
                    }
                }
                _ => return,
            },
            gone = closed(&mut socket) => if gone { return },
        }
    }
}

/// Streams an isolated MCP server's standard error while its services
/// machine runs: one `tail -F` in the machine, ended when the client goes.
pub async fn mcp(d: Arc<Daemon>, name: String, mut socket: WebSocket) {
    let pair = crate::services::pair_name(&name);
    let Some(spec) = d.machines.records().into_iter().find(|s| s.home.as_deref() == Some(pair.as_str()))
    else {
        let _ = send(&mut socket, format!("{name} has no services machine")).await;
        return;
    };
    let log = toby_guest::helper::serve::LOG;
    let mut said_stopped = false;
    loop {
        if d.machines.observe(&spec.id).await.state == "ready" {
            said_stopped = false;
            let runtime = d.machines.runtime(&spec.id);
            let session = SpawnSpec {
                session_id: toby_config::new_id(),
                argv: vec!["sh".into(), "-c".into(), format!("exec tail -n {LINES} -F \"$HOME/{log}\"")],
                env: Vec::new(),
                cwd: None,
                identity: Identity::User,
                tty: None,
                keep_after_exit: false,
                start_on_attach: true,
                tool: None,
            };
            let id = session.session_id.clone();
            // Output the page cannot take as fast as it comes is dropped.
            let (tx, mut rx) = mpsc::channel::<Vec<u8>>(64);
            let follow = tokio::spawn({
                let runtime = runtime.clone();
                async move {
                    let mut out = |b: &[u8], stderr: bool| {
                        if !stderr {
                            let _ = tx.try_send(b.to_vec());
                        }
                    };
                    crate::control::run_spec(&runtime, session, None, &mut out).await
                }
            });
            let mut lines = Lines::default();
            let gone = loop {
                tokio::select! {
                    chunk = rx.recv() => match chunk {
                        Some(b) => {
                            let mut sent = true;
                            for l in lines.push(&b) {
                                sent = sent && send(&mut socket, l).await;
                            }
                            if !sent {
                                break true;
                            }
                        }
                        // The machine stopped, or the log cannot be read.
                        None => break false,
                    },
                    gone = closed(&mut socket) => if gone { break true },
                }
            };
            if gone {
                follow.abort();
                if let Ok(mut c) = crate::control::Control::connect(&runtime).await {
                    let _ = c.kill(&id, libc_sigkill()).await;
                }
                return;
            }
        } else if !said_stopped {
            said_stopped = true;
            if !send(&mut socket, format!("{name}'s machine is not running")).await {
                return;
            }
        }
        tokio::select! {
            _ = tokio::time::sleep(POLL) => {}
            gone = closed(&mut socket) => if gone { return },
        }
    }
}

fn libc_sigkill() -> i32 {
    nix::sys::signal::Signal::SIGKILL as i32
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lines_split_and_survive_bad_bytes() {
        let mut l = Lines::default();
        assert_eq!(l.push(b"one\ntw"), ["one"]);
        assert_eq!(l.push(b"o\n\xff\n"), ["two", "\u{fffd}"]);
        assert!(l.push(&vec![b'x'; MAX_LINE - 1]).is_empty());
        let out = l.push(b"yz\n");
        assert_eq!(out.len(), 2);
        assert_eq!(out[0].len(), MAX_LINE);
        assert_eq!(out[1], "z");
    }
}
