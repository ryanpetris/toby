//! `toby approvals` and `toby mcp` (plan §16.3–16.5).

use std::process::ExitCode;
use std::sync::Arc;

use anyhow::bail;

use crate::api::{Api, segment};
use crate::cli::{ApprovalsArgs, Decision, McpCommand};
use crate::table::{age, print};

pub async fn approvals(args: ApprovalsArgs) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    match (args.id, args.decision) {
        (Some(id), Some(decision)) => {
            // What is decided is shown first: the ID may come from text the
            // guest printed.
            let list: Vec<toby_api::ApprovalInfo> = api.get("/v1/approvals").await?;
            let Some(a) = list.into_iter().find(|a| a.id == id) else { bail!("no approval {id}") };
            let verb = match decision {
                Decision::Approve => "approve",
                Decision::Deny => "deny",
            };
            println!("{} on machine {}: {}", a.kind, a.machine, clean(&a.summary));
            if !a.detail.is_empty() {
                println!("{}", clean(&a.detail));
            }
            if std::io::IsTerminal::is_terminal(&std::io::stdin()) {
                eprint!("{verb}? [y/N] ");
                let mut answer = String::new();
                std::io::stdin().read_line(&mut answer)?;
                if !matches!(answer.trim(), "y" | "Y" | "yes") {
                    bail!("nothing decided");
                }
            }
            let () = api
                .post(&format!("/v1/approvals/{}", segment(&id)), &toby_api::Decide { decision: verb.into() })
                .await?;
            println!("{}", if verb == "approve" { "approved" } else { "denied" });
        }
        (id, None) => {
            let list: Vec<toby_api::ApprovalInfo> = api.get("/v1/approvals").await?;
            let list: Vec<_> = list.into_iter().filter(|a| id.as_ref().is_none_or(|i| &a.id == i)).collect();
            if let Some(i) = id {
                let Some(a) = list.first() else { bail!("no approval {i}") };
                println!("{} {} ({})\n{}\n{}", a.id, a.kind, a.status, clean(&a.summary), clean(&a.detail));
                return Ok(ExitCode::SUCCESS);
            }
            let rows = list
                .into_iter()
                .map(|a| {
                    [a.id, a.status, a.kind, a.machine, age(a.created), clean(&a.summary).replace('\n', " ")]
                })
                .collect();
            print(["APPROVAL", "STATUS", "ACTION", "MACHINE", "ASKED", "SUMMARY"], rows);
        }
        (None, Some(_)) => unreachable!("clap requires the ID"),
    }
    Ok(ExitCode::SUCCESS)
}

/// Text from a guest, without control characters other than newlines or
/// characters that reorder or hide text.
fn clean(s: &str) -> String {
    let invisible = |c: char| matches!(c, '\u{200b}'..='\u{200f}' | '\u{202a}'..='\u{202e}' | '\u{2060}'..='\u{206f}' | '\u{feff}' | '\u{061c}' | '\u{2028}' | '\u{2029}' | '\u{00ad}' | '\u{180e}');
    s.chars().map(|c| if (c.is_control() && c != '\n') || invisible(c) { ' ' } else { c }).collect()
}

/// Feeds an attached terminal's status line and approval overlay from
/// tobyd, and sends the decisions made in the overlay.
pub fn ui(api: Arc<Api>, machine: String) -> (toby_term::Ui, tokio::task::JoinHandle<()>) {
    let (status_tx, status) = tokio::sync::watch::channel(toby_term::compositor::Status::default());
    let (decisions, mut decided) = tokio::sync::mpsc::channel::<toby_term::compositor::Decision>(8);
    let task = tokio::spawn(async move {
        // Status is fetched again when tobyd reports a change to the machine
        // or its approvals, and now and then in case events are missed.
        let mut events = api.events().await.ok();
        let mut refresh = true;
        // Decisions that did not reach tobyd; their approvals show again.
        let mut failed: Vec<String> = Vec::new();
        let mut last = tokio::time::Instant::now();
        loop {
            if refresh || last.elapsed() >= std::time::Duration::from_secs(30) {
                refresh = false;
                last = tokio::time::Instant::now();
                if let Some(mut s) = status_of(&api, &machine).await {
                    s.failed = std::mem::take(&mut failed);
                    status_tx.send_if_modified(|old| {
                        let changed = *old != s;
                        *old = s;
                        changed
                    });
                }
            }
            tokio::select! {
                e = async {
                    match &mut events {
                        Some(ev) => ev.next().await,
                        None => std::future::pending().await,
                    }
                } => match e {
                    Some(e) => {
                        refresh = e.kind == "resync"
                            || (e.kind == "machine" && e.id == machine)
                            || (e.kind == "approval" && e.machine.as_deref() == Some(machine.as_str()));
                    }
                    // tobyd went away (a restart): connect again shortly.
                    None => events = None,
                },
                // Without events, status is polled.
                _ = tokio::time::sleep(std::time::Duration::from_secs(2)), if events.is_none() => {
                    events = api.events().await.ok();
                    refresh = true;
                }
                d = decided.recv() => {
                    let Some((id, approve)) = d else { return };
                    let decision = toby_api::Decide { decision: if approve { "approve" } else { "deny" }.into() };
                    let sent: anyhow::Result<()> = api.post(&format!("/v1/approvals/{}", segment(&id)), &decision).await;
                    if sent.is_err() {
                        failed.push(id);
                    }
                    refresh = true;
                }
                _ = tokio::time::sleep(std::time::Duration::from_secs(30)) => {}
            }
        }
    });
    (toby_term::Ui { status, decisions }, task)
}

async fn status_of(api: &Api, machine: &str) -> Option<toby_term::compositor::Status> {
    let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await.ok()?;
    let m = machines.into_iter().find(|m| m.id == machine)?;
    let mut items = vec![format!("{}/{}", m.home.as_deref().unwrap_or("-"), m.root)];
    match m.forwards.len() {
        0 => {}
        1 => items.push("1 forward".into()),
        n => items.push(format!("{n} forwards")),
    }
    let list: Vec<toby_api::ApprovalInfo> = api.get("/v1/approvals").await.ok()?;
    let approvals = list
        .into_iter()
        .filter(|a| a.status == "pending" && a.machine == machine)
        .map(|a| toby_term::compositor::Approval {
            id: a.id,
            kind: a.kind,
            summary: clean(&a.summary).replace('\n', " "),
            detail: clean(&a.detail),
        })
        .collect();
    Some(toby_term::compositor::Status { items, approvals, failed: Vec::new() })
}

pub async fn mcp(cmd: McpCommand) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let config = toby_config::global::GlobalConfig::load(&api.paths.global_config())?;
    let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
    let services = |name: &str| {
        let pair = format!("mcp-{name}");
        machines.iter().find(|m| m.home.as_deref() == Some(pair.as_str())).cloned()
    };
    match cmd {
        McpCommand::Ls => {
            let mut rows =
                vec![["toby".to_string(), "built-in".into(), "daemon".into(), String::new(), String::new()]];
            for (name, s) in &config.mcp {
                let kind = match s.kind {
                    toby_config::global::McpKind::Stdio => "stdio",
                    toby_config::global::McpKind::Http => "http",
                };
                let placement = match (s.kind, s.placement()) {
                    (toby_config::global::McpKind::Http, _) => "proxy",
                    (_, toby_config::global::Placement::Machine) => "machine",
                    (_, toby_config::global::Placement::Isolated) => "isolated",
                };
                let m = services(name);
                rows.push([
                    name.clone(),
                    kind.into(),
                    placement.into(),
                    m.as_ref().map(|m| m.id.clone()).unwrap_or_default(),
                    m.map(|m| m.state).unwrap_or_default(),
                ]);
            }
            print(["MCP", "KIND", "PLACEMENT", "MACHINE", "STATE"], rows);
        }
        McpCommand::Logs { name, follow } => {
            // The server's own output, kept in its services machine's home.
            if services(&name).is_none() {
                bail!("{name} has no services machine");
            }
            let log = toby_guest::helper::serve::LOG;
            let tail = if follow { "tail -n 200 -F" } else { "tail -n 200" };
            // By home and root, so a stopped machine starts.
            let pair = format!("mcp-{name}");
            let sel =
                crate::cli::MachineSelector { machine: None, home: Some(pair.clone()), root: Some(pair) };
            let argv = vec!["sh".into(), "-c".into(), format!("{tail} \"$HOME/{log}\"")];
            return crate::client::run_session(&sel, argv, toby_proto::types::Identity::User, None).await;
        }
        McpCommand::Restart { name } => {
            // The next connection starts it again.
            let Some(m) = services(&name) else { bail!("{name} has no services machine") };
            if m.state != "stopped" {
                let () = api.post(&format!("/v1/machines/{}/stop", segment(&m.id)), &()).await?;
            }
        }
    }
    Ok(ExitCode::SUCCESS)
}
