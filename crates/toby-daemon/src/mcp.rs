//! The Toby MCP server (plan §16.4), reached from a machine with
//! `toby-connect mcp/toby`: git host actions in the repository behind the
//! attachment that holds a path, forward requests and session information.
//! Actions are allowed, denied or asked for as `[permissions.actions]` says.

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
        "git.status" | "session.info" => ActionPolicy::Allow,
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
        tool("git_status", "Show the git status of a project on the host", json!({"path": path}), &["path"]),
        tool(
            "git_commit",
            "Commit on the host (asks the user)",
            json!({"path": path, "message": {"type": "string"}, "all": {"type": "boolean"}}),
            &["path", "message"]
        ),
        tool(
            "git_fetch",
            "Fetch a configured remote on the host with the host's credentials (asks the user)",
            json!({"path": path, "remote": {"type": "string"}}),
            &["path"]
        ),
        tool(
            "git_push",
            "Push a branch to a configured remote on the host with the host's credentials (asks the user)",
            json!({"path": path, "remote": {"type": "string"}, "branch": {"type": "string"}}),
            &["path"]
        ),
        tool(
            "git_rebase",
            "Rebase on the host (asks the user)",
            json!({"path": path, "onto": {"type": "string"}}),
            &["path", "onto"]
        ),
        tool(
            "git_tag",
            "Create a tag on the host (asks the user)",
            json!({"path": path, "name": {"type": "string"}, "message": {"type": "string"}}),
            &["path", "name"]
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

/// The first line of a message, shortened, for a summary.
fn headline(s: &str) -> String {
    let line = s.lines().next().unwrap_or_default();
    match line.char_indices().nth(72) {
        Some((i, _)) => format!("{}…", &line[..i]),
        None => line.to_string(),
    }
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

    /// Runs a git action once it is permitted.
    async fn git(
        &self,
        repo: &git::Repo,
        action: &str,
        summary: String,
        detail: String,
        args: &[String],
    ) -> Value {
        if let Err(e) = self.permitted(action, summary, detail).await {
            return text(e, true);
        }
        match repo.run(args).await {
            Ok(o) => {
                let mut s = String::from_utf8_lossy(&o.stdout).into_owned();
                s.push_str(&String::from_utf8_lossy(&o.stderr));
                text(s, !o.status.success())
            }
            Err(e) => text(e, true),
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
        let repo = git::open(spec, path)?;
        let at = repo.work_tree.display().to_string();
        if repo.read_only && !matches!(action, "status" | "push") {
            return Err(format!("{path} is mounted read-only"));
        }
        let owned = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        Ok(match action {
            "status" => {
                let argv = owned(&["status", "--short", "--branch", "--ignore-submodules=all"]);
                self.git(&repo, "git.status", format!("git status in {at}"), String::new(), &argv).await
            }
            "fetch" => {
                let remote = repo.remote(arg(args, "remote").map(git::name).transpose()?)?;
                let summary = format!("git fetch {} ({}) in {at}", remote.name, remote.url());
                let argv = owned(&["fetch", "--no-recurse-submodules", &remote.name]);
                self.git(&repo, "git.fetch", summary, String::new(), &argv).await
            }
            "commit" => {
                let msg = arg(args, "message").ok_or("message is required")?;
                let all = args.get("all").and_then(Value::as_bool) == Some(true);
                let summary =
                    format!("git commit{} in {at}: {}", if all { " --all" } else { "" }, headline(msg));
                let mut argv = owned(&["commit"]);
                if all {
                    argv.push("--all".into());
                }
                argv.extend(["-m".to_string(), msg.to_string()]);
                self.git(&repo, "git.commit", summary, msg.to_string(), &argv).await
            }
            "push" => {
                let remote = repo.remote(arg(args, "remote").map(git::name).transpose()?)?;
                let branch = match arg(args, "branch") {
                    Some(b) => git::name(b)?.to_string(),
                    None => repo.branch().await?,
                };
                let summary =
                    format!("git push {} ({}) branch {branch} in {at}", remote.name, remote.push_url());
                let refspec = format!("refs/heads/{branch}:refs/heads/{branch}");
                let argv = owned(&["push", "--no-recurse-submodules", &remote.name, &refspec]);
                self.git(&repo, "git.push", summary, String::new(), &argv).await
            }
            "rebase" => {
                let onto = git::revision(arg(args, "onto").ok_or("onto is required")?)?;
                let argv = owned(&["rebase", onto]);
                self.git(&repo, "git.rebase", format!("git rebase {onto} in {at}"), String::new(), &argv)
                    .await
            }
            "tag" => {
                let tag = git::name(arg(args, "name").ok_or("name is required")?)?;
                let mut argv = owned(&["tag"]);
                let mut detail = String::new();
                if let Some(m) = arg(args, "message") {
                    argv.extend(["-a".into(), "-m".into(), m.into()]);
                    detail = m.to_string();
                }
                argv.extend(["--".into(), tag.into()]);
                self.git(&repo, "git.tag", format!("git tag {tag} in {at}"), detail, &argv).await
            }
            other => text(format!("unknown tool git_{other}"), true),
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
        // Requests still running end with the connection.
        tasks.abort_all();
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
        assert_eq!(default_policy("git.status"), ActionPolicy::Allow);
        assert_eq!(default_policy("git.fetch"), ActionPolicy::Ask);
        assert_eq!(default_policy("git.commit"), ActionPolicy::Ask);
    }

    #[test]
    fn summaries_show_one_short_line() {
        assert_eq!(headline("fix it\n\nbody"), "fix it");
        assert_eq!(headline(&"x".repeat(100)).chars().count(), 73);
    }
}
