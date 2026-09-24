//! `toby <tool>` and `toby run -f`: a tool in the machine of a home and root,
//! with its projects attached (plan §14.6, §20).

use std::ffi::OsString;
use std::path::Path;
use std::process::ExitCode;

use anyhow::Context;
use clap::Parser;
use toby_config::launch::ImageConfig;

use crate::api::{Api, segment};
use crate::cli::{LaunchArgs, RunArgs, ToolArgs};
use crate::client::attach_terminal;
use crate::launch::{Flags, Plan};

/// Creates the home if it does not exist, for the current user.
async fn ensure_home(api: &Api, name: &str) -> anyhow::Result<()> {
    let homes: Vec<toby_api::HomeInfo> = api.get("/v1/homes").await?;
    if homes.iter().any(|h| h.name == name) {
        return Ok(());
    }
    eprintln!("==> Creating home {name}");
    let uid = nix::unistd::getuid().as_raw();
    let username =
        nix::unistd::User::from_uid(nix::unistd::getuid())?.map(|u| u.name).with_context(|| {
            format!(
                "cannot determine your user name; create the home with: toby home create {name} --user NAME"
            )
        })?;
    let started: toby_api::BuildStarted =
        api.post("/v1/homes", &toby_api::CreateHome { name: name.into(), username, uid }).await?;
    api.follow_build(&started.id).await.map(drop)
}

fn absolute(dir: &Path, p: &str) -> anyhow::Result<String> {
    let home = toby_config::paths::home_dir()?;
    let p = dir.join(toby_config::paths::expand(&home, p));
    let p = std::fs::canonicalize(&p).with_context(|| p.display().to_string())?;
    p.to_str().map(str::to_string).with_context(|| format!("{} is not UTF-8", p.display()))
}

/// An image configuration as the API names it: none for the default image,
/// an image ID, or a source; relative paths start in `dir`.
pub fn api_source(
    image: &ImageConfig,
    dir: &Path,
) -> anyhow::Result<Option<Result<toby_api::Source, String>>> {
    Ok(match image {
        ImageConfig::Named(n) if n == "default" => None,
        ImageConfig::Named(id) => Some(Err(id.clone())),
        ImageConfig::Mkosi { mkosi } => Some(Ok(toby_api::Source::Mkosi { path: absolute(dir, mkosi)? })),
        ImageConfig::Dockerfile { dockerfile, context } => Some(Ok(toby_api::Source::Dockerfile {
            path: absolute(dir, dockerfile)?,
            context: absolute(dir, context.as_deref().unwrap_or("."))?,
        })),
        ImageConfig::Registry { registry } => {
            Some(Ok(toby_api::Source::Registry { reference: registry.clone() }))
        }
        ImageConfig::Archive { archive } => {
            Some(Ok(toby_api::Source::Archive { path: absolute(dir, archive)? }))
        }
    })
}

/// Creates the root if it does not exist, from the launch's image or the
/// default one (built first when needed).
async fn ensure_root(
    api: &Api,
    name: &str,
    image: Option<&(ImageConfig, std::path::PathBuf)>,
) -> anyhow::Result<()> {
    let roots: Vec<toby_api::RootInfo> = api.get("/v1/roots").await?;
    if roots.iter().any(|r| r.name == name) {
        return Ok(());
    }
    let source = match image {
        None => None,
        Some((image, dir)) => api_source(image, dir)?,
    };
    let req = match source {
        Some(Err(id)) => toby_api::CreateRoot { name: name.into(), image: id, source: None },
        // The default image, or the source's, built only when out of date.
        other => {
            let source = other.and_then(Result::ok);
            let images: Vec<toby_api::ImageInfo> = api.get("/v1/images").await?;
            if source.is_some() || !images.iter().any(|i| i.current_default) {
                eprintln!("==> Preparing the image of root {name}");
                let prepare =
                    toby_api::Prepare { sources: source.clone().into_iter().collect(), ..Default::default() };
                let started: toby_api::BuildStarted = api.post("/v1/images/prepare", &prepare).await?;
                api.follow_build(&started.id).await?;
            }
            toby_api::CreateRoot { name: name.into(), image: "default".into(), source }
        }
    };
    eprintln!("==> Creating root {name}");
    api.post("/v1/roots", &req).await
}

fn utf8(args: &[OsString]) -> anyhow::Result<Vec<String>> {
    args.iter()
        .map(|a| a.to_str().map(str::to_string).with_context(|| format!("argument {a:?} is not UTF-8")))
        .collect()
}

pub async fn run(argv: Vec<OsString>) -> anyhow::Result<ExitCode> {
    let name = argv[0].to_str().with_context(|| format!("argument {:?} is not UTF-8", argv[0]))?.to_string();
    let args = ToolArgs::try_parse_from(&argv[1..]).unwrap_or_else(|e| e.exit());
    let flags = Flags {
        tool: Some(name),
        home: args.machine.home.clone(),
        root: args.machine.root.clone(),
        projects: args.project.clone(),
        yolo: args.launch.yolo,
        args: utf8(&args.args)?,
    };
    launch(flags, None, args.machine.machine.clone(), &args.launch).await
}

pub async fn run_file(args: RunArgs) -> anyhow::Result<ExitCode> {
    let flags = Flags { yolo: args.launch.yolo, args: utf8(&args.args)?, ..Default::default() };
    launch(flags, Some(&args.file), None, &args.launch).await
}

async fn launch(
    flags: Flags,
    file: Option<&Path>,
    machine: Option<String>,
    opts: &LaunchArgs,
) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let config_dir = api.paths.global_config().parent().map(Path::to_path_buf).unwrap_or_default();
    // A mistyped command is not a tool: say so before any setup.
    let tools = toby_tools::load(&config_dir.join("tools"))?;
    if let Some(t) = &flags.tool
        && toby_tools::find(&tools, t).is_none()
    {
        anyhow::bail!("there is no command or tool {t}; see: toby --help");
    }
    let home_dir = toby_config::paths::home_dir()?;
    let plan =
        crate::launch::plan(&api.config, &config_dir, &home_dir, &std::env::current_dir()?, file, flags)?;
    api.warn(&plan.warnings);
    let find = |t: &str| {
        toby_tools::find(&tools, t)
            .map(|m| m.tool.name.clone())
            .with_context(|| format!("there is no tool {t:?}"))
    };
    let name = find(&plan.tool)?;
    let extra = plan.tools.iter().map(|t| find(t)).collect::<anyhow::Result<Vec<_>>>()?;

    let machine = match machine {
        Some(id) => id,
        None => ensure_machine(&api, &plan, opts.ephemeral).await?,
    };

    if !opts.install
        && let Some(s) = running(&api, &machine, &name, opts).await?
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

    let projects: Vec<String> = plan.projects.iter().map(|p| p.at()).collect();
    for (tool, mcp) in extra.iter().map(|t| (t, Vec::new())).chain([(&name, plan.mcp.clone())]) {
        let prepare =
            toby_api::PrepareTool { upgrade: opts.upgrade, yolo: plan.yolo, projects: projects.clone(), mcp };
        let started: toby_api::BuildStarted = api
            .post(&format!("/v1/machines/{}/tools/{}/prepare", segment(&machine), segment(tool)), &prepare)
            .await?;
        api.follow_build(&started.id).await?;
    }
    if opts.install {
        return Ok(ExitCode::SUCCESS);
    }

    let mut attachments = Vec::new();
    for p in &plan.projects {
        let host = p.host.to_str().with_context(|| format!("{} is not UTF-8", p.host.display()))?.to_string();
        attachments.push(toby_api::AddAttachment {
            host,
            at: Some(p.at()),
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
        yolo: plan.yolo,
        tools: extra,
        attachments,
        forwards: plan.forwards,
        mcp: plan.mcp.clone(),
        argv: plan.params,
        env,
        cwd: plan.workdir,
        identity: toby_proto::types::Identity::User,
        tty: tty.then(|| {
            let (rows, cols) = toby_term::session_size(api.config.settings.status_line()).unwrap_or((24, 80));
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

/// The machine of the launch's home and root, creating them as needed.
async fn ensure_machine(api: &Api, plan: &Plan, ephemeral: bool) -> anyhow::Result<String> {
    let home = plan.home.clone().unwrap_or_else(|| api.config.defaults.home().to_string());
    ensure_home(api, &home).await?;
    // The root tobyd will use: the given one, the home's default root, or
    // the root named default.
    let homes: Vec<toby_api::HomeInfo> = api.get("/v1/homes").await?;
    let default_root = homes.into_iter().find(|h| h.name == home).and_then(|h| h.default_root);
    let root = plan.root.clone().or(default_root).unwrap_or_else(|| "default".into());
    ensure_root(api, &root, plan.image.as_ref()).await?;
    let req = toby_api::EnsureMachine {
        home: Some(home),
        root: plan.root.clone(),
        ephemeral,
        cpus: plan.cpus,
        memory: plan.memory.clone(),
    };
    let ensured: toby_api::Ensured = api.post_again("/v1/machines/ensure", &req).await?;
    api.warn(&ensured.warnings);
    Ok(ensured.machine.id)
}

/// A running session of the tool to attach to instead of starting one:
/// with `--attach`, or when one is detached and the user says so.
async fn running(
    api: &Api,
    machine: &str,
    tool: &str,
    opts: &LaunchArgs,
) -> anyhow::Result<Option<toby_api::MachineSession>> {
    if opts.new {
        return Ok(None);
    }
    let sessions: Vec<toby_api::MachineSession> = api.get("/v1/sessions").await?;
    let mut running: Vec<_> = sessions
        .into_iter()
        .filter(|s| {
            s.machine == machine && s.session.tool.as_deref() == Some(tool) && s.session.exit.is_none()
        })
        .collect();
    // Detached ones first, the newest first.
    running.sort_by_key(|s| (s.session.attached, std::cmp::Reverse(s.session.started)));
    let Some(first) = running.into_iter().next() else { return Ok(None) };
    if opts.attach {
        return Ok(Some(first));
    }
    if first.session.attached || !toby_term::local_tty() {
        return Ok(None);
    }
    eprint!("A {tool} session is running here, detached. Attach to it? [Y/n] ");
    let mut answer = String::new();
    std::io::stdin().read_line(&mut answer)?;
    Ok(matches!(answer.trim(), "" | "y" | "Y" | "yes").then_some(first))
}
