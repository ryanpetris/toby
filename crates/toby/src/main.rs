//! The single Toby binary: the CLI and every host and guest component.

mod admin;
mod api;
mod cli;
mod client;
mod forwards;
mod images;
mod internal;
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
    let name = match cli.command {
        Command::Run { .. } => "run",
        Command::Exec(a) => {
            let argv = a
                .command
                .into_iter()
                .map(|s| s.into_string().map_err(|s| anyhow::anyhow!("argument is not UTF-8: {s:?}")))
                .collect::<anyhow::Result<Vec<_>>>()?;
            return runtime()?.block_on(client::run_session(&a.machine, argv, identity(a.as_root), a.cwd));
        }
        Command::Shell(a) => {
            let argv = SHELL.iter().map(|s| s.to_string()).collect();
            return runtime()?.block_on(client::run_session(&a.machine, argv, identity(a.as_root), None));
        }
        Command::Sessions(SessionsCommand::Ls) => return runtime()?.block_on(client::list()),
        Command::Sessions(SessionsCommand::Kill { id }) => return runtime()?.block_on(client::kill(&id)),
        Command::Attach { session } => return runtime()?.block_on(client::attach(session)),
        Command::Machine(cmd) => return runtime()?.block_on(admin::machine(cmd)),
        Command::Mount(a) => return runtime()?.block_on(mounts::mount(a)),
        Command::Unmount { target, machine } => return runtime()?.block_on(mounts::unmount(target, machine)),
        Command::Forward(cmd) => return runtime()?.block_on(forwards::forward(cmd)),
        Command::Image(cmd) => return runtime()?.block_on(images::image(cmd)),
        Command::Root(cmd) => return runtime()?.block_on(images::root(cmd)),
        Command::Home(cmd) => return runtime()?.block_on(images::home(cmd)),
        Command::Builder(cmd) => return runtime()?.block_on(images::builder_cmd(cmd)),
        Command::Mcp(_) => "mcp",
        Command::Approvals(_) => "approvals",
        Command::Daemon(cmd) => return runtime()?.block_on(admin::daemon(cmd)),
        Command::Linger { state } => return runtime()?.block_on(admin::linger(state)),
        Command::Config(_) => "config",
        Command::Doctor => return runtime()?.block_on(admin::doctor()),
        Command::Web => "web",
        Command::Internal(cmd) => match cmd {
            InternalCommand::Daemon => return internal::daemon().map(|()| ExitCode::SUCCESS),
            InternalCommand::Proxy => return internal::proxy().map(|()| ExitCode::SUCCESS),
            InternalCommand::Machine { machine, supervise: false, .. } => {
                return internal::machine(&machine).map(|()| ExitCode::SUCCESS);
            }
            InternalCommand::Machine { machine, supervise: true, log_dir } => {
                return internal::supervise(&machine, log_dir.as_deref()).map(|()| ExitCode::SUCCESS);
            }
            InternalCommand::Fs { machine } => return internal::fs(&machine).map(|()| ExitCode::SUCCESS),
            InternalCommand::Vm { machine, stop: false } => {
                return internal::vm(&machine).map(|()| ExitCode::SUCCESS);
            }
            InternalCommand::Vm { machine, stop: true } => {
                return internal::vm_stop(&machine).map(|()| ExitCode::SUCCESS);
            }
            InternalCommand::Net { machine } => return internal::net(&machine).map(|()| ExitCode::SUCCESS),
        },
        Command::Guest(cmd) => match cmd {
            GuestCommand::Relay => {
                runtime()?.block_on(toby_guest::relay::run(toby_guest::paths::GuestPaths::from_env()))?;
                return Ok(ExitCode::SUCCESS);
            }
            GuestCommand::Session { id } => {
                runtime()?
                    .block_on(toby_guest::session::run(toby_guest::paths::GuestPaths::from_env(), &id))?;
                return Ok(ExitCode::SUCCESS);
            }
            GuestCommand::Connect { .. } => "guest connect",
            GuestCommand::Helper(cmd) => return helper(cmd).map(|()| ExitCode::SUCCESS),
        },
        Command::Tool(argv) => return runtime()?.block_on(tool::run(argv)),
    };
    anyhow::bail!("`toby {name}` is not implemented yet")
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
        HelperCommand::PatchFile { path, format, mode, content_hex } => {
            let bytes = (0..content_hex.len())
                .step_by(2)
                .map(|i| content_hex.get(i..i + 2).and_then(|h| u8::from_str_radix(h, 16).ok()))
                .collect::<Option<Vec<u8>>>()
                .ok_or_else(|| anyhow::anyhow!("--content-hex is not hex"))?;
            let content = String::from_utf8(bytes)?;
            let format = match format.as_str() {
                "json" => toby_tools::Format::Json,
                "toml" => toby_tools::Format::Toml,
                _ => toby_tools::Format::Text,
            };
            let mode = if mode == "merge" { toby_tools::Mode::Merge } else { toby_tools::Mode::Replace };
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
