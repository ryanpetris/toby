//! The Toby MCP server (plan §16.4), reached from a machine with
//! `toby-connect mcp/toby`: git host actions in the host directory behind the
//! attachment that holds a path, forward requests and session information.
//! Actions are allowed, denied or asked for as `[permissions.actions]` says.

use std::path::{Component, Path, PathBuf};
use std::time::Duration;

use serde_json::{Value, json};
use toby_config::global::ActionPolicy;
use toby_config::machine::MachineSpec;
use tokio::io::{AsyncBufReadExt, AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, BufReader};

use crate::approvals::Approvals;
use crate::machines::Machines;

/// How long an action waits for its approval.
const APPROVAL_TIMEOUT: Duration = Duration::from_secs(600);
/// Longest request line accepted from the guest.
const MAX_LINE: usize = 1 << 20;

const PROTOCOL: &str = "2025-06-18";

/// Default policy of each action when the configuration names none.
fn default_policy(action: &str) -> ActionPolicy {
    match action {
        "git.status" | "git.fetch" | "session.info" => ActionPolicy::Allow,
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
            "Fetch on the host with the host's credentials",
            json!({"path": path, "remote": {"type": "string"}}),
            &["path"]
        ),
        tool(
            "git_push",
            "Push on the host with the host's credentials (asks the user)",
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
            "Ask the user to forward a port between the host and this machine",
            json!({"port": {"type": "integer"}, "direction": {"type": "string", "enum": ["host-to-guest", "guest-to-host"]}}),
            &["port"]
        ),
        tool("session_info", "Describe this machine and its mounted projects", json!({}), &[]),
    ])
}

/// Maps a guest path inside an attachment to its host path.
fn host_path(spec: &MachineSpec, guest: &str) -> Result<PathBuf, String> {
    let g = Path::new(guest);
    if !g.is_absolute() || g.components().any(|c| matches!(c, Component::ParentDir)) {
        return Err(format!("{guest} must be an absolute path without .."));
    }
    spec.attach
        .iter()
        .filter_map(|a| g.strip_prefix(&a.at).ok().map(|rest| Path::new(&a.host).join(rest)))
        .next()
        .ok_or_else(|| format!("{guest} is not inside a mounted project"))
}

fn text(s: impl Into<String>, error: bool) -> Value {
    json!({"content": [{"type": "text", "text": s.into()}], "isError": error})
}

fn arg<'a>(args: &'a Value, key: &str) -> Option<&'a str> {
    args.get(key).and_then(Value::as_str)
}

/// Git arguments must not smuggle options into positional places.
fn plain(s: &str) -> Result<&str, String> {
    if s.starts_with('-') || s.is_empty() { Err(format!("{s:?} is not allowed here")) } else { Ok(s) }
}

pub struct Server<'a> {
    pub machines: &'a Machines,
    pub approvals: &'a Approvals,
    pub machine: String,
}

impl Server<'_> {
    /// Asks, allows or denies an action; returns whether it may run.
    async fn permitted(&self, action: &str, summary: String, detail: String) -> Result<(), String> {
        let config = self.machines.current_config();
        let policy =
            config.permissions.actions.get(action).copied().unwrap_or_else(|| default_policy(action));
        match policy {
            ActionPolicy::Allow => Ok(()),
            ActionPolicy::Deny => Err(format!("{action} is denied by the configuration")),
            ActionPolicy::Ask | ActionPolicy::AlwaysAsk => {
                let a = self
                    .approvals
                    .create(&self.machine, action, summary, detail)
                    .map_err(|e| format!("recording the approval: {e}"))?;
                eprintln!("approval {} pending: {}", a.id, a.summary);
                match self.approvals.wait(&a.id, APPROVAL_TIMEOUT).await {
                    Ok(true) => Ok(()),
                    Ok(false) => Err(format!("the user did not approve {action}")),
                    Err(e) => Err(e.to_string()),
                }
            }
        }
    }

    async fn git(&self, action: &str, dir: &Path, args: Vec<String>, ask: bool) -> Value {
        let summary = format!("git {} in {}", args.join(" "), dir.display());
        if ask && let Err(e) = self.permitted(action, summary.clone(), String::new()).await {
            return text(e, true);
        }
        let out = tokio::process::Command::new("git")
            .arg("-C")
            .arg(dir)
            .args(&args)
            .stdin(std::process::Stdio::null())
            .output()
            .await;
        match out {
            Ok(o) => {
                let mut s = String::from_utf8_lossy(&o.stdout).into_owned();
                s.push_str(&String::from_utf8_lossy(&o.stderr));
                text(s, !o.status.success())
            }
            Err(e) => text(format!("running git: {e}"), true),
        }
    }

    async fn call(&self, name: &str, args: &Value) -> Value {
        let spec = match self.machines.record(&self.machine) {
            Ok(s) => s,
            Err(e) => return text(e.message, true),
        };
        let dir = || {
            arg(args, "path").ok_or_else(|| "path is required".to_string()).and_then(|p| host_path(&spec, p))
        };
        let run = |action: &'static str, argv: Result<Vec<String>, String>| async move {
            match (dir(), argv) {
                (Ok(d), Ok(a)) => {
                    // Allowed actions still pass the policy check (deny).
                    let ask = true;
                    self.git(action, &d, a, ask).await
                }
                (Err(e), _) | (_, Err(e)) => text(e, true),
            }
        };
        match name {
            "git_status" => {
                run("git.status", Ok(vec!["status".into(), "--short".into(), "--branch".into()])).await
            }
            "git_fetch" => {
                let mut a = vec!["fetch".to_string()];
                match arg(args, "remote").map(plain) {
                    Some(Ok(r)) => a.push(r.into()),
                    Some(Err(e)) => return text(e, true),
                    None => {}
                }
                run("git.fetch", Ok(a)).await
            }
            "git_commit" => {
                let Some(msg) = arg(args, "message") else { return text("message is required", true) };
                let mut a = vec!["commit".to_string()];
                if args.get("all").and_then(Value::as_bool) == Some(true) {
                    a.push("--all".into());
                }
                a.extend(["-m".to_string(), msg.to_string()]);
                run("git.commit", Ok(a)).await
            }
            "git_push" => {
                let mut a = vec!["push".to_string()];
                for key in ["remote", "branch"] {
                    match arg(args, key).map(plain) {
                        Some(Ok(v)) => a.push(v.into()),
                        Some(Err(e)) => return text(e, true),
                        None => {}
                    }
                }
                run("git.push", Ok(a)).await
            }
            "git_rebase" => match arg(args, "onto").map(plain) {
                Some(Ok(onto)) => run("git.rebase", Ok(vec!["rebase".into(), onto.into()])).await,
                Some(Err(e)) => text(e, true),
                None => text("onto is required", true),
            },
            "git_tag" => match arg(args, "name").map(plain) {
                Some(Ok(tag)) => {
                    let mut a = vec!["tag".to_string()];
                    if let Some(m) = arg(args, "message") {
                        a.extend(["-a".into(), "-m".into(), m.into()]);
                    }
                    a.push(tag.into());
                    run("git.tag", Ok(a)).await
                }
                Some(Err(e)) => text(e, true),
                None => text("name is required", true),
            },
            "forward_request" => {
                let Some(port) = args.get("port").and_then(Value::as_u64).filter(|p| (1..=65535).contains(p))
                else {
                    return text("port must be 1 to 65535", true);
                };
                let direction = arg(args, "direction").unwrap_or("host-to-guest").to_string();
                if direction != "host-to-guest" && direction != "guest-to-host" {
                    return text("direction must be host-to-guest or guest-to-host", true);
                }
                let summary = format!("forward port {port} ({direction}) for machine {}", self.machine);
                if let Err(e) = self.permitted("forward", summary, String::new()).await {
                    return text(e, true);
                }
                let addr = format!("127.0.0.1:{port}");
                let req = toby_api::AddForward {
                    direction,
                    host: addr.clone(),
                    guest: addr,
                    pinned: true,
                    persist: false,
                };
                match self.machines.add_forward(&self.machine, req, None).await {
                    Ok(f) => text(format!("forwarded: {} {} ({})", f.host, f.direction, f.id), false),
                    Err(e) => text(e.message, true),
                }
            }
            "session_info" => {
                let projects: Vec<Value> =
                    spec.attach.iter().map(|a| json!({"path": a.at, "read_only": a.read_only})).collect();
                let info = json!({"machine": spec.id, "home": spec.home, "projects": projects});
                text(info.to_string(), false)
            }
            other => text(format!("unknown tool {other}"), true),
        }
    }

    async fn handle(&self, req: Value) -> Option<Value> {
        let id = req.get("id").cloned();
        let method = req.get("method").and_then(Value::as_str).unwrap_or_default();
        let params = req.get("params").cloned().unwrap_or(Value::Null);
        let result = match method {
            "initialize" => {
                let version = params.get("protocolVersion").and_then(Value::as_str).unwrap_or(PROTOCOL);
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

    /// Serves newline-delimited JSON-RPC until the stream ends.
    pub async fn serve<S: AsyncRead + AsyncWrite + Unpin>(&self, stream: S) -> std::io::Result<()> {
        let (read, mut write) = tokio::io::split(stream);
        let mut lines = BufReader::new(read);
        let mut line = String::new();
        loop {
            line.clear();
            let n = (&mut lines).take(MAX_LINE as u64).read_line(&mut line).await?;
            if n == 0 {
                return Ok(());
            }
            if line.trim().is_empty() {
                continue;
            }
            let reply = match serde_json::from_str::<Value>(&line) {
                Ok(req) => self.handle(req).await,
                Err(e) => Some(
                    json!({"jsonrpc": "2.0", "id": null, "error": {"code": -32700, "message": e.to_string()}}),
                ),
            };
            if let Some(r) = reply {
                write.write_all(format!("{r}\n").as_bytes()).await?;
                write.flush().await?;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn spec() -> MachineSpec {
        toml::from_str(
            "schema = 1\ngeneration = 1\nid = \"m\"\nroot = \"r\"\n[resources]\ncpus = 1\nmemory = \"1G\"\n\
             [[attach]]\nid = \"a\"\nhost = \"/home/u/src/app\"\nat = \"/toby/workspace/app\"\n",
        )
        .unwrap()
    }

    #[test]
    fn guest_paths_map_to_their_attachment() {
        let s = spec();
        assert_eq!(host_path(&s, "/toby/workspace/app").unwrap(), PathBuf::from("/home/u/src/app"));
        assert_eq!(host_path(&s, "/toby/workspace/app/sub").unwrap(), PathBuf::from("/home/u/src/app/sub"));
        assert!(host_path(&s, "/toby/workspace/app/../../etc").is_err());
        assert!(host_path(&s, "/toby/workspace/other").is_err());
        assert!(host_path(&s, "relative").is_err());
    }

    #[test]
    fn positional_arguments_are_not_options() {
        assert!(plain("origin").is_ok());
        assert!(plain("--receive-pack=evil").is_err());
        assert!(plain("").is_err());
    }

    #[test]
    fn push_always_asks_by_default() {
        assert_eq!(default_policy("git.push"), ActionPolicy::AlwaysAsk);
        assert_eq!(default_policy("git.status"), ActionPolicy::Allow);
        assert_eq!(default_policy("git.commit"), ActionPolicy::Ask);
    }
}
