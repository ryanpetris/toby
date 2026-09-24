//! The single Toby binary: the CLI and every host and guest component.

mod cli;

use std::process::ExitCode;

use clap::Parser;

use cli::{Cli, Command, GuestCommand, InternalCommand, ToolArgs};

#[global_allocator]
static GLOBAL: mimalloc::MiMalloc = mimalloc::MiMalloc;

fn main() -> ExitCode {
    let argv = cli::expand_multicall(std::env::args_os().collect());
    let cli = Cli::parse_from(argv);

    match run(cli) {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("toby: {err:#}");
            ExitCode::FAILURE
        }
    }
}

fn runtime() -> anyhow::Result<tokio::runtime::Runtime> {
    Ok(tokio::runtime::Builder::new_multi_thread().enable_all().build()?)
}

fn run(cli: Cli) -> anyhow::Result<()> {
    let name = match cli.command {
        Command::Run { .. } => "run",
        Command::Exec(_) => "exec",
        Command::Shell(_) => "shell",
        Command::Sessions(_) => "sessions",
        Command::Attach { .. } => "attach",
        Command::Machine(_) => "machine",
        Command::Mount(_) => "mount",
        Command::Unmount { .. } => "unmount",
        Command::Forward(_) => "forward",
        Command::Image(_) => "image",
        Command::Root(_) => "root",
        Command::Home(_) => "home",
        Command::Builder(_) => "builder",
        Command::Mcp(_) => "mcp",
        Command::Approvals(_) => "approvals",
        Command::Daemon { .. } => "daemon",
        Command::Linger { .. } => "linger",
        Command::Config(_) => "config",
        Command::Doctor => "doctor",
        Command::Web => "web",
        Command::Internal(cmd) => match cmd {
            InternalCommand::Proxy => "internal proxy",
            InternalCommand::Machine { .. } => "internal machine",
            InternalCommand::Fs { .. } => "internal fs",
            InternalCommand::Vm { .. } => "internal vm",
            InternalCommand::Net { .. } => "internal net",
        },
        Command::Guest(cmd) => match cmd {
            GuestCommand::Relay => {
                return Ok(
                    runtime()?.block_on(toby_guest::relay::run(toby_guest::paths::GuestPaths::from_env()))?
                );
            }
            GuestCommand::Session { id } => {
                return Ok(runtime()?.block_on(toby_guest::session::run(
                    toby_guest::paths::GuestPaths::from_env(),
                    &id,
                ))?);
            }
            GuestCommand::Connect { .. } => "guest connect",
            GuestCommand::Helper { .. } => "guest helper",
        },
        Command::Tool(argv) => {
            ToolArgs::try_parse_from(&argv[1..]).map_err(|e| anyhow::anyhow!("{e}"))?;
            "<tool>"
        }
    };
    anyhow::bail!("`toby {name}` is not implemented yet")
}
