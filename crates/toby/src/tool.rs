//! `toby <tool>`: a tool in the machine of a home and root, with the current
//! project attached (plan §20).

use std::ffi::OsString;
use std::process::ExitCode;

use anyhow::Context;
use clap::Parser;

use crate::api::{Api, segment};
use crate::cli::ToolArgs;
use crate::client::attach_terminal;

/// Creates the home if it does not exist, for the current user.
async fn ensure_home(api: &Api, name: &str) -> anyhow::Result<()> {
    let homes: Vec<toby_api::HomeInfo> = api.get("/v1/homes").await?;
    if homes.iter().any(|h| h.name == name) {
        return Ok(());
    }
    eprintln!("==> Creating home {name}");
    let uid = nix::unistd::getuid().as_raw();
    let username = nix::unistd::User::from_uid(nix::unistd::getuid())?
        .map(|u| u.name)
        .context("cannot determine your user name; create the home with: toby home create")?;
    let started: toby_api::BuildStarted =
        api.post("/v1/homes", &toby_api::CreateHome { name: name.into(), username, uid }).await?;
    api.follow_build(&started.id).await.map(drop)
}

/// Creates the root from the default image if it does not exist, building
/// the default image first when needed.
async fn ensure_root(api: &Api, name: &str) -> anyhow::Result<()> {
    let roots: Vec<toby_api::RootInfo> = api.get("/v1/roots").await?;
    if roots.iter().any(|r| r.name == name) {
        return Ok(());
    }
    let images: Vec<toby_api::ImageInfo> = api.get("/v1/images").await?;
    if !images.iter().any(|i| i.current_default) {
        eprintln!("==> Building the default image");
        let started: toby_api::BuildStarted =
            api.post("/v1/images/prepare", &toby_api::Prepare::default()).await?;
        api.follow_build(&started.id).await?;
    }
    eprintln!("==> Creating root {name}");
    api.post("/v1/roots", &toby_api::CreateRoot { name: name.into(), image: "default".into() }).await
}

pub async fn run(argv: Vec<OsString>) -> anyhow::Result<ExitCode> {
    let name = argv[0].to_str().context("the tool name is not UTF-8")?.to_string();
    let args = ToolArgs::try_parse_from(&argv[1..]).unwrap_or_else(|e| e.exit());
    let extra = args
        .args
        .iter()
        .map(|a| a.to_str().map(str::to_string).context("arguments must be UTF-8"))
        .collect::<anyhow::Result<Vec<_>>>()?;
    let api = Api::connect().await?;
    // A mistyped command is not a tool: say so before any setup.
    let tools_dir = api.paths.global_config().parent().map(|d| d.join("tools")).unwrap_or_default();
    if !toby_tools::load(&tools_dir)?.contains_key(&name) {
        anyhow::bail!("there is no command or tool {name:?}; see toby --help");
    }

    let machine = match &args.machine.machine {
        Some(id) => id.clone(),
        None => {
            let home = args.machine.home.clone().unwrap_or_else(|| api.config.defaults.home().to_string());
            ensure_home(&api, &home).await?;
            // The root tobyd will use: the given one, the home's default
            // root, or the root named default.
            let homes: Vec<toby_api::HomeInfo> = api.get("/v1/homes").await?;
            let default_root = homes.into_iter().find(|h| h.name == home).and_then(|h| h.default_root);
            let root = args.machine.root.clone().or(default_root).unwrap_or_else(|| "default".into());
            ensure_root(&api, &root).await?;
            let req = toby_api::EnsureMachine {
                home: Some(home),
                root: args.machine.root.clone(),
                ephemeral: args.ephemeral,
                ..Default::default()
            };
            let ensured: toby_api::Ensured = api.post_again("/v1/machines/ensure", &req).await?;
            api.warn(&ensured.warnings);
            ensured.machine.id
        }
    };

    // Reattaching needs nothing prepared.
    if args.attach && !args.install {
        let sessions: Vec<toby_api::MachineSession> = api.get("/v1/sessions").await?;
        if let Some(s) = sessions
            .into_iter()
            .find(|s| s.machine == machine && s.session.argv0 == name && s.session.exit.is_none())
        {
            return attach_terminal(
                s.session_socket.into(),
                s.control_socket.into(),
                &s.session.id,
                &machine,
                true,
                true,
            )
            .await;
        }
    }

    let prepare = toby_api::PrepareTool { upgrade: args.upgrade };
    let started: toby_api::BuildStarted = api
        .post(&format!("/v1/machines/{}/tools/{}/prepare", segment(&machine), segment(&name)), &prepare)
        .await?;
    api.follow_build(&started.id).await?;
    if args.install {
        return Ok(ExitCode::SUCCESS);
    }

    let projects =
        if args.project.is_empty() { vec![std::env::current_dir()?] } else { args.project.clone() };
    let mut attachments = Vec::new();
    for p in projects {
        let host = std::fs::canonicalize(&p).with_context(|| format!("{} does not exist", p.display()))?;
        let host = host.to_str().context("the project path is not UTF-8")?.to_string();
        attachments.push(toby_api::AddAttachment {
            host,
            at: None,
            read_only: false,
            pinned: false,
            persist: false,
        });
    }
    let tty = toby_term::local_tty();
    let mut env = Vec::new();
    if tty && let Ok(term) = std::env::var("TERM") {
        env.push(("TERM".to_string(), term));
    }
    let req = toby_api::CreateSession {
        request_id: Some(toby_config::new_id()),
        target: toby_api::MachineSelector { machine: Some(machine), home: None, root: None },
        tool: Some(name),
        yolo: args.yolo,
        attachments,
        argv: extra,
        env,
        cwd: None,
        identity: toby_proto::types::Identity::User,
        tty: tty.then(|| {
            let (rows, cols) = toby_term::session_size().unwrap_or((24, 80));
            toby_proto::types::TtySize { rows, cols }
        }),
    };
    let created: toby_api::SessionCreated = api.post_again("/v1/sessions", &req).await?;
    api.warn(&created.warnings);
    attach_terminal(
        created.session_socket.into(),
        created.control_socket.into(),
        &created.id,
        &created.machine,
        true,
        false,
    )
    .await
}
