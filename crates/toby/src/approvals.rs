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
            // What is decided is shown first: a notice in a session's output
            // could have been printed by the guest.
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

/// Text from a guest, without control characters other than newlines.
fn clean(s: &str) -> String {
    s.chars().map(|c| if c.is_control() && c != '\n' { ' ' } else { c }).collect()
}

/// Prints a notice for approvals the machine asks for while a session is
/// attached; the user answers with `toby approvals`.
pub fn notices(api: Arc<Api>, machine: String) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        let mut seen: Vec<String> = Vec::new();
        loop {
            if let Ok(list) = api.get::<Vec<toby_api::ApprovalInfo>>("/v1/approvals").await {
                for a in list.iter().filter(|a| a.status == "pending" && a.machine == machine) {
                    if !seen.contains(&a.id) {
                        seen.push(a.id.clone());
                        eprint!(
                            "\r\n\x1b[K[toby] approval needed: {} (toby approvals {} approve)\r\n",
                            clean(&a.summary).replace('\n', " "),
                            a.id
                        );
                    }
                }
            }
            tokio::time::sleep(std::time::Duration::from_secs(2)).await;
        }
    })
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
            let Some(m) = services(&name) else { bail!("{name} has no services machine") };
            let log = toby_guest::helper::serve::LOG;
            let tail = if follow { "tail -n 200 -F" } else { "tail -n 200" };
            let sel = crate::cli::MachineSelector { machine: Some(m.id), home: None, root: None };
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
