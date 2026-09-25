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
use crate::progress::Display;

/// Creates the home for the current user, as step `step`.
async fn create_home(api: &Api, display: &mut Display, step: u32, name: &str) -> anyhow::Result<()> {
    display.start(step);
    let uid = nix::unistd::getuid().as_raw();
    let username =
        nix::unistd::User::from_uid(nix::unistd::getuid())?.map(|u| u.name).with_context(|| {
            format!(
                "cannot determine your user name; create the home with: toby home create {name} --user NAME"
            )
        })?;
    let started: toby_api::BuildStarted =
        api.post("/v1/homes", &toby_api::CreateHome { name: name.into(), username, uid }).await?;
    let mut job = display.job(Some(step));
    api.follow(&started.id, display, &mut job).await?;
    display.end(step);
    Ok(())
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

/// Creates the root, as step `step`, from the launch's image or the default
/// one, built first when it is out of date.
async fn create_root(
    api: &Api,
    display: &mut Display,
    step: u32,
    name: &str,
    image: Option<&(ImageConfig, std::path::PathBuf)>,
) -> anyhow::Result<()> {
    display.start(step);
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
                let prepare =
                    toby_api::Prepare { sources: source.clone().into_iter().collect(), ..Default::default() };
                let started: toby_api::BuildStarted = api.post("/v1/images/prepare", &prepare).await?;
                let mut job = display.job(Some(step));
                api.follow(&started.id, display, &mut job).await?;
            }
            toby_api::CreateRoot { name: name.into(), image: "default".into(), source }
        }
    };
    let () = api.post("/v1/roots", &req).await?;
    display.end(step);
    Ok(())
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
    let find = |t: &str| {
        toby_tools::find(&tools, t)
            .map(|m| m.tool.name.clone())
            .with_context(|| format!("there is no tool {t:?}"))
    };
    let name = find(&plan.tool)?;
    let extra = plan.tools.iter().map(|t| find(t)).collect::<anyhow::Result<Vec<_>>>()?;

    // Everything up to the tool's start is shown as steps; a launch with
    // nothing to set up shows nothing.
    let mut display = Display::new(format!("Setting up {name}"));
    display.warnings(&api.config.settings, &plan.warnings);
    let setup = async {
        let (machine, running) = set_up(&api, &mut display, &plan, &name, &extra, machine, opts).await?;
        anyhow::Ok(match running {
            Some(s) => Some((s.session_socket, s.control_socket, s.session.id, machine, true)),
            None if opts.install => None,
            None => {
                let created = create_session(&api, &plan, &name, extra, &machine).await?;
                display.warnings(&api.config.settings, &created.warnings);
                Some((created.session_socket, created.control_socket, created.id, created.machine, false))
            }
        })
    }
    .await;
    display.finish(setup.is_ok());
    let Some((session_socket, control_socket, id, machine, reattach)) = setup? else {
        return Ok(ExitCode::SUCCESS);
    };
    attach_terminal(session_socket.into(), control_socket.into(), &id, &machine, true, reattach).await
}

/// The machine, and its tools prepared; or a running session of the tool
/// to attach to instead.
async fn set_up(
    api: &Api,
    display: &mut Display,
    plan: &Plan,
    name: &str,
    extra: &[String],
    machine: Option<String>,
    opts: &LaunchArgs,
) -> anyhow::Result<(String, Option<toby_api::MachineSession>)> {
    let machine = match machine {
        Some(id) => id,
        None => ensure_machine(api, display, plan, opts.ephemeral).await?,
    };
    if !opts.install
        && let Some(s) = running(api, display, &machine, name, opts).await?
    {
        return Ok((machine, Some(s)));
    }
    let projects: Vec<String> = plan.projects.iter().map(|p| p.at()).collect();
    for (tool, mcp) in extra.iter().map(|t| (t, Vec::new())).chain([(&name.to_string(), plan.mcp.clone())]) {
        let prepare =
            toby_api::PrepareTool { upgrade: opts.upgrade, yolo: plan.yolo, projects: projects.clone(), mcp };
        let started: toby_api::BuildStarted = api
            .post(&format!("/v1/machines/{}/tools/{}/prepare", segment(&machine), segment(tool)), &prepare)
            .await?;
        // Shown only if the tool is installed or updated.
        let mut job = display.lazy_job(tool.clone());
        api.follow(&started.id, display, &mut job).await?;
        if let Some(step) = job.parent() {
            display.end(step);
        }
    }
    Ok((machine, None))
}

async fn create_session(
    api: &Api,
    plan: &Plan,
    name: &str,
    extra: Vec<String>,
    machine: &str,
) -> anyhow::Result<toby_api::SessionCreated> {
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
        target: toby_api::MachineSelector { machine: Some(machine.into()), home: None, root: None },
        tool: Some(name.into()),
        yolo: plan.yolo,
        tools: extra,
        attachments,
        forwards: plan.forwards.clone(),
        mcp: plan.mcp.clone(),
        argv: plan.params.clone(),
        env,
        cwd: plan.workdir.clone(),
        identity: toby_proto::types::Identity::User,
        tty: tty.then(|| {
            let (rows, cols) = toby_term::session_size(api.config.settings.status_line()).unwrap_or((24, 80));
            toby_proto::types::TtySize { rows, cols }
        }),
    };
    api.post_again("/v1/sessions", &req).await
}

/// The machine of the launch's home and root, creating the home and root
/// and starting the machine as needed, each a step.
async fn ensure_machine(
    api: &Api,
    display: &mut Display,
    plan: &Plan,
    ephemeral: bool,
) -> anyhow::Result<String> {
    let home = plan.home.clone().unwrap_or_else(|| api.config.defaults.home().to_string());
    let homes: Vec<toby_api::HomeInfo> = api.get("/v1/homes").await?;
    let home_rec = homes.into_iter().find(|h| h.name == home);
    // The root tobyd will use: the given one, the home's default root, or
    // the root named default.
    let root = plan
        .root
        .clone()
        .or(home_rec.as_ref().and_then(|h| h.default_root.clone()))
        .unwrap_or_else(|| "default".into());
    let roots: Vec<toby_api::RootInfo> = api.get("/v1/roots").await?;
    let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
    let running = machines
        .iter()
        .any(|m| m.home.as_deref() == Some(home.as_str()) && m.root == root && m.state == "ready");
    let home_step = home_rec.is_none().then(|| display.queue(format!("Home {home}")));
    let root_step = (!roots.iter().any(|r| r.name == root)).then(|| display.queue(format!("Root {root}")));
    let machine_step = (!running).then(|| display.queue(format!("Machine {home}/{root}")));
    if let Some(step) = home_step {
        create_home(api, display, step, &home).await?;
    }
    if let Some(step) = root_step {
        create_root(api, display, step, &root, plan.image.as_ref()).await?;
    }
    let req = toby_api::EnsureMachine {
        home: Some(home),
        root: plan.root.clone(),
        ephemeral,
        cpus: plan.cpus,
        memory: plan.memory.clone(),
    };
    if let Some(step) = machine_step {
        display.start(step);
        let started: toby_api::BuildStarted = api.post("/v1/machines/start", &req).await?;
        let mut job = display.job(Some(step));
        api.follow(&started.id, display, &mut job).await?;
        display.end(step);
    }
    let ensured: toby_api::Ensured = api.post_again("/v1/machines/ensure", &req).await?;
    display.warnings(&api.config.settings, &ensured.warnings);
    Ok(ensured.machine.id)
}

/// A running session of the tool to attach to instead of starting one:
/// with `--attach`, or when one is detached and the user says so.
async fn running(
    api: &Api,
    display: &mut Display,
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
    display.leave();
    eprint!("A {tool} session is running here, detached. Attach to it? [Y/n] ");
    let mut answer = String::new();
    std::io::stdin().read_line(&mut answer)?;
    Ok(matches!(answer.trim(), "" | "y" | "Y" | "yes").then_some(first))
}
