//! `toby mount` and `toby unmount`: host directories in a machine (plan
//! §10.4).

use std::process::ExitCode;

use anyhow::{Context, bail};

use crate::api::{Api, segment};
use crate::cli::{MachineSelector, MountArgs};

/// The machine a command targets: by ID, or the machine for a home and root
/// (started if needed).
async fn machine_for(api: &Api, sel: &MachineSelector) -> anyhow::Result<String> {
    if let Some(id) = &sel.machine {
        return Ok(id.clone());
    }
    let req =
        toby_api::EnsureMachine { home: sel.home.clone(), root: sel.root.clone(), ..Default::default() };
    let ensured: toby_api::Ensured = api.post("/v1/machines/ensure", &req).await?;
    api.warn(&ensured.warnings);
    Ok(ensured.machine.id)
}

pub async fn mount(args: MountArgs) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let host = std::fs::canonicalize(&args.path)
        .with_context(|| format!("{} does not exist", args.path.display()))?;
    let host = host.to_str().context("the path is not UTF-8")?.to_string();
    let machine = machine_for(&api, &args.machine).await?;
    let req = toby_api::AddAttachment {
        host,
        at: args.at,
        read_only: args.ro,
        pinned: true,
        persist: args.persist,
    };
    let added: toby_api::AttachmentInfo =
        api.post(&format!("/v1/machines/{}/attachments", segment(&machine)), &req).await?;
    println!("{}", added.at);
    Ok(ExitCode::SUCCESS)
}

/// Removes an attachment given by host path, guest path or ID.
pub async fn unmount(target: String, sel: MachineSelector) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let host = std::fs::canonicalize(&target).ok().and_then(|p| p.to_str().map(str::to_string));
    let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
    let matches: Vec<(String, String)> = machines
        .iter()
        .filter(|m| {
            sel.machine.as_ref().is_none_or(|id| &m.id == id)
                && sel.home.as_ref().is_none_or(|h| m.home.as_ref() == Some(h))
                && sel.root.as_ref().is_none_or(|r| &m.root == r)
        })
        .flat_map(|m| {
            m.attachments
                .iter()
                .filter(|a| a.id == target || a.at == target || host.as_ref() == Some(&a.host))
                .map(|a| (m.id.clone(), a.id.clone()))
        })
        .collect();
    let (machine, id) = match matches.as_slice() {
        [] => bail!("{target} is not mounted in any machine"),
        [one] => one.clone(),
        _ if matches.iter().all(|(m, _)| *m == matches[0].0) => bail!(
            "{target} is mounted more than once; choose one by ID: {}",
            matches.iter().map(|(_, a)| a.as_str()).collect::<Vec<_>>().join(", ")
        ),
        _ => bail!(
            "{target} is mounted in several machines; choose one with --machine: {}",
            matches.iter().map(|(m, _)| m.as_str()).collect::<Vec<_>>().join(", ")
        ),
    };
    let () = api.delete(&format!("/v1/machines/{}/attachments/{}", segment(&machine), segment(&id))).await?;
    Ok(ExitCode::SUCCESS)
}
