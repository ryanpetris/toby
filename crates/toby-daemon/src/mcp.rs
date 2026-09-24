//! The Toby MCP server (plan §16.4), reached from a machine with
//! `toby-connect mcp/toby`: git fetch and push with the host's credentials
//! for the repository of the project that holds a path, forward requests and
//! session information.
//! Actions are allowed, denied or asked for as `[permissions.actions]` says.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use serde_json::{Value, json};
use toby_config::global::ActionPolicy;
use toby_config::machine::MachineSpec;
use tokio::io::{AsyncBufReadExt, AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, BufReader};

use crate::git;
use crate::server::Daemon;

/// How long an action waits for its approval.
const APPROVAL_TIMEOUT: Duration = Duration::from_secs(600);
/// Longest request line accepted from the guest.
const MAX_LINE: usize = 1 << 20;
/// Requests of one connection handled at a time.
const IN_FLIGHT: usize = 4;

/// Protocol versions the server speaks, newest first.
const PROTOCOLS: &[&str] = &["2025-06-18", "2025-03-26", "2024-11-05"];

/// Default policy of each action when the configuration names none.
fn default_policy(action: &str) -> ActionPolicy {
    match action {
        "session.info" => ActionPolicy::Allow,
        "git.push" => ActionPolicy::AlwaysAsk,
        _ => ActionPolicy::Ask,
    }
}

fn tools() -> Value {
    let path =
        json!({"type": "string", "description": "A directory in the machine, inside a mounted project"});
    let tool = |name: &str, description: &str, props: Value, required: &[&str]| {
        json!({"name": name, "description": description,
               "inputSchema": {"type": "object", "properties": props, "required": required}})
    };
    json!([
        tool(
            "git_fetch",
            "Fetch the branches of a configured remote with the host's credentials (asks the user)",
            json!({"path": path, "remote": {"type": "string"}}),
            &["path"]
        ),
        tool(
            "git_push",
            "Push a branch to a configured remote with the host's credentials (asks the user)",
            json!({"path": path, "remote": {"type": "string"}, "branch": {"type": "string"}}),
            &["path"]
        ),
        tool(
            "forward_request",
            "Ask the user to forward a port between the host and this machine while its sessions run",
            json!({"port": {"type": "integer"}, "direction": {"type": "string", "enum": ["host-to-guest", "guest-to-host"]}}),
            &["port"]
        ),
        tool("session_info", "Describe this machine and its mounted projects", json!({}), &[]),
    ])
}

fn text(s: impl Into<String>, error: bool) -> Value {
    json!({"content": [{"type": "text", "text": s.into()}], "isError": error})
}

fn arg<'a>(args: &'a Value, key: &str) -> Option<&'a str> {
    args.get(key).and_then(Value::as_str)
}

/// A remote URL, and where the host's git config sends it if elsewhere.
fn via(url: &str, to: &str) -> String {
    if url == to { url.to_string() } else { format!("{url} via {to}") }
}

pub struct Server {
    pub daemon: Arc<Daemon>,
    pub machine: String,
}

impl Server {
    /// Asks, allows or denies an action; returns whether it may run.
    async fn permitted(&self, action: &str, summary: String, detail: String) -> Result<(), String> {
        let config = self.daemon.machines.current_config();
        let policy =
            config.permissions.actions.get(action).copied().unwrap_or_else(|| default_policy(action));
        let ask = match policy {
            ActionPolicy::Allow => false,
            ActionPolicy::Deny => return Err(format!("{action} is denied by the configuration")),
            ActionPolicy::Ask => !self.daemon.machines.yolo(&self.machine).await,
            ActionPolicy::AlwaysAsk => true,
        };
        if !ask {
            return Ok(());
        }
        let approvals = &self.daemon.approvals;
        let a = approvals
            .create(&self.machine, action, summary, detail, APPROVAL_TIMEOUT)
            .map_err(|e| format!("recording the approval: {e}"))?;
        eprintln!("approval {} pending: {}", a.id, a.summary);
        match approvals.wait(&a.id).await {
            Ok(true) => Ok(()),
            Ok(false) => Err(format!("the user did not approve {action}")),
            Err(e) => Err(e.to_string()),
        }
    }

    async fn call(&self, name: &str, args: &Value) -> Value {
        let spec = match self.daemon.machines.record(&self.machine) {
            Ok(s) => s,
            Err(e) => return text(e.message, true),
        };
        if let Some(action) = name.strip_prefix("git_") {
            return match self.call_git(&spec, action, args).await {
                Ok(v) => v,
                Err(e) => text(e, true),
            };
        }
        match name {
            "forward_request" => {
                let Some(port) = args.get("port").and_then(Value::as_u64).filter(|p| (1..=65535).contains(p))
                else {
                    return text("port must be 1 to 65535", true);
                };
                let direction = arg(args, "direction").unwrap_or("host-to-guest").to_string();
                if direction != "host-to-guest" && direction != "guest-to-host" {
                    return text("direction must be host-to-guest or guest-to-host", true);
                }
                let summary = format!(
                    "forward port {port} ({direction}) for machine {} while its sessions run",
                    self.machine
                );
                if let Err(e) = self.permitted("forward", summary, String::new()).await {
                    return text(e, true);
                }
                self.forward(port, direction).await
            }
            "session_info" => {
                if let Err(e) =
                    self.permitted("session.info", "session information".into(), String::new()).await
                {
                    return text(e, true);
                }
                let projects: Vec<Value> =
                    spec.attach.iter().map(|a| json!({"path": a.at, "read_only": a.read_only})).collect();
                let info = json!({"machine": spec.id, "home": spec.home, "projects": projects});
                text(info.to_string(), false)
            }
            other => text(format!("unknown tool {other}"), true),
        }
    }

    async fn call_git(&self, spec: &MachineSpec, action: &str, args: &Value) -> Result<Value, String> {
        let path = arg(args, "path").ok_or("path is required")?;
        let machines = &self.daemon.machines;
        let roots: Vec<PathBuf> = machines
            .records()
            .iter()
            .flat_map(|m| m.attach.iter())
            .filter_map(|a| std::fs::canonicalize(&a.host).ok())
            .collect();
        let repo = git::open(spec, &roots, path)?;
        let remote = repo.remote(arg(args, "remote").map(git::name).transpose()?)?;
        let at = repo.work_tree.display();
        let scratch = machines.paths.state.join("git");
        let result = match action {
            "fetch" => {
                if repo.read_only {
                    return Err(format!("{path} is mounted read-only"));
                }
                let to = repo.destination(&scratch, remote.url(), false).await?;
                let summary = format!("git fetch {} ({}) into {at}", remote.name, via(remote.url(), &to));
                self.permitted("git.fetch", summary, String::new()).await?;
                repo.fetch(&scratch, remote).await
            }
            "push" => {
                let branch = match arg(args, "branch") {
                    Some(b) => git::name(b)?.to_string(),
                    None => repo.branch()?,
                };
                let id = repo.resolve(&format!("refs/heads/{branch}"))?;
                let to = repo.destination(&scratch, remote.push_url(), true).await?;
                let summary = format!(
                    "git push {branch} ({}) to {} ({}) from {at}",
                    &id[..12],
                    remote.name,
                    via(remote.push_url(), &to)
                );
                self.permitted("git.push", summary, String::new()).await?;
                repo.push(&scratch, remote, &branch, &id).await
            }
            other => return Err(format!("unknown tool git_{other}")),
        };
        Ok(match result {
            Ok(log) => text(log, false),
            Err(log) => text(log, true),
        })
    }

    /// Adds a forward that lasts while the machine's current sessions run.
    async fn forward(&self, port: u64, direction: String) -> Value {
        let machines = &self.daemon.machines;
        let live: Vec<String> = machines
            .sessions()
            .await
            .into_iter()
            .filter(|(m, s)| *m == self.machine && s.exit.is_none())
            .map(|(_, s)| s.id)
            .collect();
        if live.is_empty() {
            return text("the machine has no running sessions", true);
        }
        let addr = format!("127.0.0.1:{port}");
        let mut result = None;
        for session in &live {
            let req = toby_api::AddForward {
                direction: direction.clone(),
                host: addr.clone(),
                guest: addr.clone(),
                pinned: false,
                persist: false,
            };
            result = Some(machines.add_forward(&self.machine, req, Some(session)).await);
            if let Some(Err(_)) = &result {
                break;
            }
        }
        match result {
            Some(Ok(f)) => text(format!("forwarded: {} {} ({})", f.host, f.direction, f.id), false),
            Some(Err(e)) => text(e.message, true),
            None => text("the machine has no running sessions", true),
        }
    }

    async fn handle(&self, req: Value) -> Option<Value> {
        let id = req.get("id").cloned();
        let method = req.get("method").and_then(Value::as_str).unwrap_or_default();
        let params = req.get("params").cloned().unwrap_or(Value::Null);
        let result = match method {
            "initialize" => {
                let asked = params.get("protocolVersion").and_then(Value::as_str);
                let version = asked.filter(|v| PROTOCOLS.contains(v)).unwrap_or(PROTOCOLS[0]);
                Ok(json!({
                    "protocolVersion": version,
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": "toby", "version": env!("CARGO_PKG_VERSION")}
                }))
            }
            "ping" => Ok(json!({})),
            "tools/list" => Ok(json!({"tools": tools()})),
            "tools/call" => {
                let name = params.get("name").and_then(Value::as_str).unwrap_or_default();
                let args = params.get("arguments").cloned().unwrap_or(json!({}));
                Ok(self.call(name, &args).await)
            }
            _ if id.is_none() => return None,
            other => Err(json!({"code": -32601, "message": format!("unknown method {other}")})),
        };
        let id = id?;
        Some(match result {
            Ok(r) => json!({"jsonrpc": "2.0", "id": id, "result": r}),
            Err(e) => json!({"jsonrpc": "2.0", "id": id, "error": e}),
        })
    }

    /// Serves newline-delimited JSON-RPC until the stream ends. Requests run
    /// concurrently, a few at a time, so one waiting for an approval does not
    /// hold up the others.
    pub async fn serve<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
        self,
        stream: S,
    ) -> std::io::Result<()> {
        let server = Arc::new(self);
        let (read, write) = tokio::io::split(stream);
        let (tx, mut rx) = tokio::sync::mpsc::channel::<Value>(IN_FLIGHT);
        let writer = tokio::spawn(async move {
            let mut write = write;
            while let Some(r) = rx.recv().await {
                write.write_all(format!("{r}\n").as_bytes()).await?;
                write.flush().await?;
            }
            Ok::<_, std::io::Error>(())
        });
        let permits = Arc::new(tokio::sync::Semaphore::new(IN_FLIGHT));
        let mut tasks = tokio::task::JoinSet::new();
        let mut lines = BufReader::new(read);
        let mut line = String::new();
        let result = loop {
            let Ok(permit) = permits.clone().acquire_owned().await else { break Ok(()) };
            line.clear();
            let n = match (&mut lines).take(MAX_LINE as u64).read_line(&mut line).await {
                Ok(n) => n,
                Err(e) => break Err(e),
            };
            if n == 0 {
                break Ok(());
            }
            if line.trim().is_empty() {
                continue;
            }
            let request = serde_json::from_str::<Value>(&line);
            let (server, tx) = (server.clone(), tx.clone());
            tasks.spawn(async move {
                let reply = match request {
                    Ok(req) => server.handle(req).await,
                    Err(e) => Some(
                        json!({"jsonrpc": "2.0", "id": null, "error": {"code": -32700, "message": e.to_string()}}),
                    ),
                };
                if let Some(r) = reply {
                    let _ = tx.send(r).await;
                }
                drop(permit);
            });
        };
        // Requests already received are answered; the writer ends with them,
        // or at once when the connection broke.
        match &result {
            Ok(()) => while tasks.join_next().await.is_some() {},
            Err(_) => tasks.abort_all(),
        }
        drop(tx);
        let _ = writer.await;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn push_always_asks_by_default() {
        assert_eq!(default_policy("git.push"), ActionPolicy::AlwaysAsk);
        assert_eq!(default_policy("git.fetch"), ActionPolicy::Ask);
        assert_eq!(default_policy("session.info"), ActionPolicy::Allow);
    }
}
