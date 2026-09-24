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
            let decision = match decision {
                Decision::Approve => "approve",
                Decision::Deny => "deny",
            };
            let () = api
                .post(
                    &format!("/v1/approvals/{}", segment(&id)),
                    &toby_api::Decide { decision: decision.into() },
                )
                .await?;
        }
        (id, None) => {
            let list: Vec<toby_api::ApprovalInfo> = api.get("/v1/approvals").await?;
            let list: Vec<_> = list.into_iter().filter(|a| id.as_ref().is_none_or(|i| &a.id == i)).collect();
            if let Some(i) = id {
                let Some(a) = list.first() else { bail!("no approval {i}") };
                println!("{} {} ({})\n{}\n{}", a.id, a.kind, a.status, a.summary, a.detail);
                return Ok(ExitCode::SUCCESS);
            }
            let rows = list
                .into_iter()
                .map(|a| [a.id, a.status, a.kind, a.machine, age(a.created), a.summary])
                .collect();
            print(["APPROVAL", "STATUS", "ACTION", "MACHINE", "ASKED", "SUMMARY"], rows);
        }
        (None, Some(_)) => unreachable!("clap requires the ID"),
    }
    Ok(ExitCode::SUCCESS)
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
                            a.summary, a.id
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
            let Some(m) = services(&name) else { bail!("{name} has no services machine") };
            return crate::admin::machine(crate::cli::MachineCommand::Logs { id: m.id, follow }).await;
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
