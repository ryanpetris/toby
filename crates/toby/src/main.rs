//! The single Toby binary: the CLI and every host and guest component.

mod admin;
mod api;
mod approvals;
mod cli;
mod client;
mod forwards;
mod images;
mod internal;
mod launch;
mod mounts;
mod table;
mod tool;

use std::process::ExitCode;

use clap::Parser;

use cli::{Cli, Command, GuestCommand, HelperCommand, InternalCommand, SessionsCommand};
use toby_proto::types::Identity;

#[global_allocator]
static GLOBAL: mimalloc::MiMalloc = mimalloc::MiMalloc;

fn main() -> ExitCode {
    let argv = cli::expand_multicall(std::env::args_os().collect());
    let cli = Cli::parse_from(argv);

    match run(cli) {
        Ok(code) => code,
        Err(err) => {
            eprintln!("toby: {err:#}");
            ExitCode::FAILURE
        }
    }
}

fn runtime() -> anyhow::Result<tokio::runtime::Runtime> {
    Ok(tokio::runtime::Builder::new_multi_thread().enable_all().build()?)
}

fn identity(as_root: bool) -> Identity {
    if as_root { Identity::Root } else { Identity::User }
}

/// A login shell of the session's account.
const SHELL: &[&str] = &["/bin/sh", "-c", "exec \"${SHELL:-/bin/sh}\" -l"];

fn run(cli: Cli) -> anyhow::Result<ExitCode> {
    match cli.command {
        Command::Run(a) => runtime()?.block_on(tool::run_file(a)),
        Command::Exec(a) => {
            let argv = a
                .command
                .into_iter()
                .map(|s| s.into_string().map_err(|s| anyhow::anyhow!("argument is not UTF-8: {s:?}")))
                .collect::<anyhow::Result<Vec<_>>>()?;
            runtime()?.block_on(client::run_session(&a.machine, argv, identity(a.as_root), a.cwd))
        }
        Command::Shell(a) => {
            let argv = SHELL.iter().map(|s| s.to_string()).collect();
            runtime()?.block_on(client::run_session(&a.machine, argv, identity(a.as_root), None))
        }
        Command::Sessions(SessionsCommand::Ls) => runtime()?.block_on(client::list()),
        Command::Sessions(SessionsCommand::Kill { id }) => runtime()?.block_on(client::kill(&id)),
        Command::Attach { session } => runtime()?.block_on(client::attach(session)),
        Command::Machine(cmd) => runtime()?.block_on(admin::machine(cmd)),
        Command::Mount(a) => runtime()?.block_on(mounts::mount(a)),
        Command::Unmount { target, machine } => runtime()?.block_on(mounts::unmount(target, machine)),
        Command::Forward(cmd) => runtime()?.block_on(forwards::forward(cmd)),
        Command::Image(cmd) => runtime()?.block_on(images::image(cmd)),
        Command::Root(cmd) => runtime()?.block_on(images::root(cmd)),
        Command::Home(cmd) => runtime()?.block_on(images::home(cmd)),
        Command::Builder(cmd) => runtime()?.block_on(images::builder_cmd(cmd)),
        Command::Mcp(cmd) => runtime()?.block_on(approvals::mcp(cmd)),
        Command::Approvals(args) => runtime()?.block_on(approvals::approvals(args)),
        Command::Daemon(cmd) => runtime()?.block_on(admin::daemon(cmd)),
        Command::Linger { state } => runtime()?.block_on(admin::linger(state)),
        Command::Config(cmd) => admin::config(cmd),
        Command::Doctor { gc: false } => runtime()?.block_on(admin::doctor()),
        Command::Doctor { gc: true } => runtime()?.block_on(admin::collect_versions()),
        Command::Web => runtime()?.block_on(admin::web()),
        Command::Internal(cmd) => match cmd {
            InternalCommand::Daemon => internal::daemon().map(|()| ExitCode::SUCCESS),
            InternalCommand::Proxy => internal::proxy().map(|()| ExitCode::SUCCESS),
            InternalCommand::Machine { machine, supervise: false, .. } => {
                internal::machine(&machine).map(|()| ExitCode::SUCCESS)
            }
            InternalCommand::Machine { machine, supervise: true, log_dir } => {
                internal::supervise(&machine, log_dir.as_deref()).map(|()| ExitCode::SUCCESS)
            }
            InternalCommand::Fs { machine } => internal::fs(&machine).map(|()| ExitCode::SUCCESS),
            InternalCommand::Vm { machine, stop: false } => {
                internal::vm(&machine).map(|()| ExitCode::SUCCESS)
            }
            InternalCommand::Vm { machine, stop: true } => {
                internal::vm_stop(&machine).map(|()| ExitCode::SUCCESS)
            }
            InternalCommand::Net { machine } => internal::net(&machine).map(|()| ExitCode::SUCCESS),
        },
        Command::Guest(cmd) => match cmd {
            GuestCommand::Relay => {
                runtime()?.block_on(toby_guest::relay::run(toby_guest::paths::GuestPaths::from_env()))?;
                Ok(ExitCode::SUCCESS)
            }
            GuestCommand::Session { id } => {
                runtime()?
                    .block_on(toby_guest::session::run(toby_guest::paths::GuestPaths::from_env(), &id))?;
                Ok(ExitCode::SUCCESS)
            }
            GuestCommand::Connect { target } => {
                let socket = std::path::Path::new(toby_guest::connect::SANDBOX_SOCKET);
                runtime()?.block_on(toby_guest::connect::run(socket, &target))?;
                Ok(ExitCode::SUCCESS)
            }
            GuestCommand::Helper(cmd) => helper(cmd).map(|()| ExitCode::SUCCESS),
        },
        Command::Tool(argv) => runtime()?.block_on(tool::run(argv)),
    }
}

fn helper(cmd: HelperCommand) -> anyhow::Result<()> {
    use toby_guest::helper;
    let paths = toby_guest::paths::GuestPaths::from_env();
    match cmd {
        HelperCommand::NetUp { addr, gw, dns, hostname } => {
            let (a, p) =
                addr.split_once('/').ok_or_else(|| anyhow::anyhow!("--addr needs a prefix length"))?;
            let opts = helper::NetUp { addr: a.parse()?, prefix: p.parse()?, gateway: gw, dns, hostname };
            helper::net_up(&opts, paths.root())?;
        }
        HelperCommand::UserSetup { name, uid, shell, sudo } => {
            let u = helper::UserSetup { name, uid, shell, sudo };
            helper::user_setup(&u, std::path::Path::new("/"), &paths.user_file())?;
        }
        HelperCommand::HomeMount { device, at, uid, gid } => {
            helper::home_mount(&device, &at, uid, gid, std::path::Path::new("/etc/skel"))?;
        }
        HelperCommand::Links { target } => helper::links(&paths.root().join("bin"), &target)?,
        HelperCommand::Attach { src, at, ro } => helper::attach(&src, &at, ro)?,
        HelperCommand::Detach { src, at } => helper::detach(&src, &at)?,
        HelperCommand::ServeStdio { socket, command } => {
            let rt = tokio::runtime::Builder::new_current_thread().enable_all().build()?;
            let code = rt.block_on(helper::serve::serve_stdio(&socket, &command))?;
            std::process::exit(code);
        }
        HelperCommand::PatchFile { path, format, mode } => {
            let mut content = String::new();
            std::io::Read::read_to_string(&mut std::io::stdin(), &mut content)?;
            let format = match format.as_str() {
                "json" => toby_tools::Format::Json,
                "toml" => toby_tools::Format::Toml,
                _ => toby_tools::Format::Text,
            };
            let mode = match mode.as_str() {
                "merge" => toby_tools::Mode::Merge,
                "extend" => toby_tools::Mode::Extend,
                _ => toby_tools::Mode::Replace,
            };
            let home = std::env::var_os("HOME").map(std::path::PathBuf::from).unwrap_or_default();
            helper::patch::patch_file(&helper::patch::expand_home(&path, &home), &content, format, mode)?;
        }
        HelperCommand::Build { id, kind, args } => {
            let args: Vec<String> = [id, kind].into_iter().chain(args).collect();
            helper::build::exec_script(&paths.root().join("build"), "build.sh", &args)?;
        }
        HelperCommand::Provision => {
            helper::build::exec_script(&paths.root().join("build"), "provision.sh", &[])?
        }
        HelperCommand::FormatHome => {
            helper::build::exec_script(&paths.root().join("build"), "format-home.sh", &[])?
        }
    }
    Ok(())
}
