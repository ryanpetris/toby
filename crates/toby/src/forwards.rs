//! `toby forward`: ports between the host and a machine (plan §11.5).

use std::process::ExitCode;

use anyhow::bail;

use crate::api::{Api, segment};
use crate::cli::{ForwardCommand, MachineSelector};

/// Parses `PORT`, `HOSTPORT:GUESTPORT` or `ADDR:HOSTPORT:GUESTPORT` into
/// host and guest addresses; addresses default to loopback.
fn parse(spec: &str) -> anyhow::Result<(String, String)> {
    let port = |p: &str| -> anyhow::Result<u16> {
        p.parse::<u16>().ok().filter(|p| *p > 0).ok_or_else(|| anyhow::anyhow!("{p:?} is not a port"))
    };
    let parts: Vec<&str> = spec.split(':').collect();
    let (addr, host, guest) = match parts.as_slice() {
        [p] => ("127.0.0.1", port(p)?, port(p)?),
        [h, g] => ("127.0.0.1", port(h)?, port(g)?),
        [a, h, g] => (*a, port(h)?, port(g)?),
        _ => bail!("{spec:?} is not PORT, HOSTPORT:GUESTPORT or ADDR:HOSTPORT:GUESTPORT"),
    };
    if addr.parse::<std::net::IpAddr>().is_err() {
        bail!("{addr:?} is not an IP address");
    }
    Ok((format!("{addr}:{host}"), format!("127.0.0.1:{guest}")))
}

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

pub async fn forward(cmd: ForwardCommand) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    match cmd {
        ForwardCommand::Add { spec, to_host, machine, persist } => {
            let (host, guest) = parse(&spec)?;
            let id = machine_for(&api, &machine).await?;
            let direction = if to_host { "guest-to-host" } else { "host-to-guest" }.to_string();
            let req = toby_api::AddForward { direction, host, guest, pinned: true, persist };
            let f: toby_api::ForwardInfo =
                api.post(&format!("/v1/machines/{}/forwards", segment(&id)), &req).await?;
            let arrow = if to_host { "<-" } else { "->" };
            println!("{} {arrow} {} ({})", f.host, f.guest, f.id);
        }
        ForwardCommand::Rm { id } => {
            let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
            let Some(m) = machines.iter().find(|m| m.forwards.iter().any(|f| f.id == id)) else {
                bail!("no forward {id}");
            };
            let () =
                api.delete(&format!("/v1/machines/{}/forwards/{}", segment(&m.id), segment(&id))).await?;
        }
        ForwardCommand::Ls => {
            let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
            let rows = machines
                .into_iter()
                .flat_map(|m| {
                    m.forwards.into_iter().map(move |f| {
                        let state = match f.error {
                            Some(e) => format!("{} ({e})", f.state),
                            None => f.state,
                        };
                        [f.id, m.id.clone(), f.direction, f.host, f.guest, state]
                    })
                })
                .collect();
            crate::table::print(["FORWARD", "MACHINE", "DIRECTION", "HOST", "GUEST", "STATE"], rows);
        }
    }
    Ok(ExitCode::SUCCESS)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn specs() {
        assert_eq!(parse("3000").unwrap(), ("127.0.0.1:3000".into(), "127.0.0.1:3000".into()));
        assert_eq!(parse("8080:3000").unwrap(), ("127.0.0.1:8080".into(), "127.0.0.1:3000".into()));
        assert_eq!(parse("0.0.0.0:8080:3000").unwrap(), ("0.0.0.0:8080".into(), "127.0.0.1:3000".into()));
        for bad in ["", "0", "x", "1:2:3:4", "host:1:2", "70000"] {
            assert!(parse(bad).is_err(), "{bad}");
        }
    }
}
