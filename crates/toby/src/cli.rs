//! Command-line interface of the `toby` binary.

use std::ffi::OsString;
use std::path::PathBuf;

use clap::{Args, Parser, Subcommand, ValueEnum};

#[derive(Debug, Parser)]
#[command(name = "toby", version, about = "Run development tools inside virtual machines")]
pub struct Cli {
    #[command(subcommand)]
    pub command: Command,
}

#[derive(Debug, Subcommand)]
pub enum Command {
    /// Run a tool from a launch file
    Run {
        /// Launch file
        #[arg(short = 'f', long = "file")]
        file: PathBuf,
    },
    /// Run a command in a machine
    Exec(ExecArgs),
    /// Open a shell in a machine
    Shell(ShellArgs),
    /// Manage sessions
    #[command(subcommand)]
    Sessions(SessionsCommand),
    /// Reattach a session
    Attach {
        /// Session ID
        session: Option<String>,
    },
    /// Manage machines
    #[command(subcommand)]
    Machine(MachineCommand),
    /// Make a host path visible in a machine
    Mount(MountArgs),
    /// Remove a mounted host path from a machine
    Unmount {
        /// Host path or attachment ID
        target: String,
        #[command(flatten)]
        machine: MachineSelector,
    },
    /// Manage port forwards
    #[command(subcommand)]
    Forward(ForwardCommand),
    /// Manage images
    #[command(subcommand)]
    Image(ImageCommand),
    /// Manage roots
    #[command(subcommand)]
    Root(RootCommand),
    /// Manage homes
    #[command(subcommand)]
    Home(HomeCommand),
    /// Manage the builder
    #[command(subcommand)]
    Builder(BuilderCommand),
    /// Manage MCP servers
    #[command(subcommand)]
    Mcp(McpCommand),
    /// List or decide approvals
    Approvals(ApprovalsArgs),
    /// Manage the Toby daemon
    #[command(subcommand)]
    Daemon(DaemonCommand),
    /// Keep machines running after logout
    Linger { state: OnOff },
    /// Read or change configuration
    #[command(subcommand)]
    Config(ConfigCommand),
    /// Check the host setup
    Doctor,
    /// Open the web UI
    Web,
    /// Host-side component processes
    #[command(subcommand, hide = true)]
    Internal(InternalCommand),
    /// Guest-side component processes
    #[command(subcommand, hide = true)]
    Guest(GuestCommand),
    /// Launch a tool
    #[command(external_subcommand)]
    Tool(Vec<OsString>),
}

/// Selects the machine by its home and root.
#[derive(Debug, Args)]
pub struct MachineSelector {
    /// Home
    #[arg(long)]
    pub home: Option<String>,
    /// Root
    #[arg(long)]
    pub root: Option<String>,
    /// Machine ID
    #[arg(long, conflicts_with_all = ["home", "root"])]
    pub machine: Option<String>,
}

#[derive(Debug, Args)]
pub struct ExecArgs {
    /// Run as root
    #[arg(long)]
    pub as_root: bool,
    #[command(flatten)]
    pub machine: MachineSelector,
    /// Working directory in the guest
    #[arg(long)]
    pub cwd: Option<String>,
    /// Command and arguments
    #[arg(last = true, required = true)]
    pub command: Vec<OsString>,
}

#[derive(Debug, Args)]
pub struct ShellArgs {
    /// Run as root
    #[arg(long)]
    pub as_root: bool,
    #[command(flatten)]
    pub machine: MachineSelector,
}

#[derive(Debug, Subcommand)]
pub enum SessionsCommand {
    /// List sessions
    Ls,
    /// Kill a session
    Kill { id: String },
}

#[derive(Debug, Subcommand)]
pub enum MachineCommand {
    /// List machines
    Ls,
    /// Stop machines
    Stop {
        /// Machine ID
        #[arg(required_unless_present = "all", conflicts_with = "all")]
        id: Option<String>,
        /// Stop every machine
        #[arg(long)]
        all: bool,
    },
    /// Show a machine's logs
    Logs {
        id: String,
        /// Follow new output
        #[arg(short, long)]
        follow: bool,
    },
}

#[derive(Debug, Args)]
pub struct MountArgs {
    /// Host path
    pub path: PathBuf,
    /// Guest path
    #[arg(long)]
    pub at: Option<String>,
    /// Read-only
    #[arg(long)]
    pub ro: bool,
    #[command(flatten)]
    pub machine: MachineSelector,
    /// Recreate on every machine start
    #[arg(long)]
    pub persist: bool,
}

#[derive(Debug, Subcommand)]
pub enum ForwardCommand {
    /// Add a forward
    Add {
        /// HOST[:GUEST] port or address
        spec: String,
        /// Forward a guest port to the host
        #[arg(long)]
        to_host: bool,
        #[command(flatten)]
        machine: MachineSelector,
        /// Recreate on every machine start
        #[arg(long)]
        persist: bool,
    },
    /// Remove a forward
    Rm { id: String },
    /// List forwards
    Ls,
}

#[derive(Debug, Subcommand)]
pub enum ImageCommand {
    /// Build every image the configuration needs
    Prepare {
        /// Everything, including every root's source
        #[arg(long)]
        all: bool,
        /// The default image
        #[arg(long)]
        default: bool,
        /// Isolated MCP server images
        #[arg(long, num_args = 0.., value_name = "NAME")]
        mcp: Option<Vec<String>>,
        /// A project's image
        #[arg(long, num_args = 0..=1, value_name = "PATH")]
        project: Option<Option<PathBuf>>,
        /// Rebuild images that are up to date
        #[arg(long)]
        rebuild: bool,
        /// Re-resolve registry references and base images
        #[arg(long)]
        pull: bool,
    },
    /// Build an image from a Dockerfile
    Build {
        /// Dockerfile (default: Dockerfile in the context)
        #[arg(long, conflicts_with = "mkosi")]
        dockerfile: Option<PathBuf>,
        /// Build context (default: the current directory)
        #[arg(long, conflicts_with = "mkosi")]
        context: Option<PathBuf>,
        /// mkosi configuration directory
        #[arg(long)]
        mkosi: Option<PathBuf>,
    },
    /// Build an image from a registry reference
    Pull { reference: String },
    /// Build an image from an OCI archive
    Import { archive: PathBuf },
    /// List images
    Ls,
    /// Remove an image
    Rm { id: String },
    /// Remove unreferenced images
    Prune,
}

#[derive(Debug, Subcommand)]
pub enum RootCommand {
    /// List roots
    Ls,
    /// Create a root
    Create {
        name: String,
        #[arg(long)]
        image: String,
    },
    /// Return a root to its image's state
    Reset { name: String },
    /// Move a root to a newer image
    Rebase {
        name: String,
        #[arg(long)]
        image: Option<String>,
    },
    /// Remove a root
    Rm { name: String },
}

#[derive(Debug, Subcommand)]
pub enum HomeCommand {
    /// List homes
    Ls,
    /// Create a home
    Create {
        name: String,
        /// Guest user name (default: yours)
        #[arg(long)]
        user: Option<String>,
        /// Guest user ID (default: yours)
        #[arg(long)]
        uid: Option<u32>,
    },
    /// Remove a home
    Rm { name: String },
}

#[derive(Debug, Subcommand)]
pub enum BuilderCommand {
    /// Build the first default image
    Bootstrap {
        /// Debian 13 cloud image to start from
        #[arg(long)]
        base: Option<PathBuf>,
        /// Delete the bootstrap cloud image
        #[arg(long)]
        clean: bool,
    },
    /// Show builder state
    Status,
}

#[derive(Debug, Subcommand)]
pub enum McpCommand {
    /// List MCP servers
    Ls,
    /// Show an MCP server's logs
    Logs {
        name: String,
        #[arg(short, long)]
        follow: bool,
    },
    /// Restart an MCP server
    Restart { name: String },
}

#[derive(Debug, Args)]
pub struct ApprovalsArgs {
    /// Approval ID
    pub id: Option<String>,
    /// Decision
    #[arg(requires = "id")]
    pub decision: Option<Decision>,
}

#[derive(Debug, Clone, Copy, ValueEnum)]
pub enum Decision {
    Approve,
    Deny,
}

#[derive(Debug, Subcommand)]
pub enum DaemonCommand {
    /// Show daemon status
    Status,
    /// Start the daemon
    Start,
    /// Stop the daemon
    Stop,
    /// Restart the daemon
    Restart,
    /// Show daemon logs
    Logs {
        #[arg(short, long)]
        follow: bool,
    },
}

#[derive(Debug, Clone, Copy, ValueEnum)]
pub enum OnOff {
    On,
    Off,
}

#[derive(Debug, Subcommand)]
pub enum ConfigCommand {
    /// Print a configuration value
    Get { key: String },
    /// Set a configuration value
    Set { key: String, value: String },
}

#[derive(Debug, Subcommand)]
pub enum InternalCommand {
    /// The per-user control plane (tobyd)
    Daemon,
    /// Models and remote MCP proxy
    Proxy,
    /// Per-machine host process
    Machine {
        #[arg(long)]
        machine: String,
        /// Supervise the machine's processes (direct back end)
        #[arg(long)]
        supervise: bool,
        /// Where the supervised processes log (default: the runtime directory)
        #[arg(long, requires = "supervise")]
        log_dir: Option<PathBuf>,
    },
    /// Per-machine virtio-fs back end
    Fs {
        #[arg(long)]
        machine: String,
    },
    /// Start the machine's VMM
    Vm {
        #[arg(long)]
        machine: String,
        /// Power the running VM off instead: power button, then stop it
        #[arg(long)]
        stop: bool,
    },
    /// Start the machine's network back end
    Net {
        #[arg(long)]
        machine: String,
    },
}

#[derive(Debug, Subcommand)]
pub enum GuestCommand {
    /// Guest relay
    Relay,
    /// Session process
    Session {
        /// Session ID
        #[arg(long)]
        id: String,
    },
    /// Connect stdio to a Toby service
    Connect { target: String },
    /// Guest helper operation
    #[command(subcommand)]
    Helper(HelperCommand),
}

/// Short-lived guest operations run as root by the machine's host process.
#[derive(Debug, Subcommand)]
pub enum HelperCommand {
    /// Configure the network interface, route, hostname and resolver
    NetUp {
        /// Address with prefix length, e.g. 10.0.2.15/24
        #[arg(long)]
        addr: String,
        #[arg(long)]
        gw: std::net::Ipv4Addr,
        #[arg(long)]
        dns: std::net::Ipv4Addr,
        #[arg(long)]
        hostname: Option<String>,
    },
    /// Create the home's user in the root
    UserSetup {
        #[arg(long)]
        name: String,
        #[arg(long)]
        uid: u32,
        #[arg(long)]
        shell: Option<String>,
        /// Allow passwordless sudo
        #[arg(long)]
        sudo: bool,
    },
    /// Mount the home disk and prepare it on first use
    HomeMount {
        #[arg(long)]
        device: PathBuf,
        #[arg(long)]
        at: PathBuf,
        #[arg(long)]
        uid: u32,
        #[arg(long)]
        gid: u32,
    },
    /// Create the /run/toby/bin links
    Links {
        #[arg(long)]
        target: PathBuf,
    },
    /// Build an image (builder machines)
    Build { id: String, kind: String, args: Vec<String> },
    /// Install build tools into the bootstrap builder
    Provision,
    /// Format a new home disk
    FormatHome,
    /// Bind-mount an attached host directory
    Attach {
        #[arg(long)]
        src: PathBuf,
        #[arg(long)]
        at: PathBuf,
        #[arg(long)]
        ro: bool,
    },
    /// Unmount an attached host directory
    Detach {
        #[arg(long)]
        src: PathBuf,
        #[arg(long)]
        at: PathBuf,
    },
}

/// Options of `toby <tool>`.
#[derive(Debug, Parser)]
#[command(no_binary_name = true, bin_name = "toby <tool>")]
pub struct ToolArgs {
    #[command(flatten)]
    pub machine: MachineSelector,
    /// Project path (repeatable)
    #[arg(long)]
    pub project: Vec<PathBuf>,
    /// Add a throwaway layer over the root
    #[arg(long)]
    pub ephemeral: bool,
    /// Attach to an existing session of the tool
    #[arg(long, conflicts_with = "new")]
    pub attach: bool,
    /// Start a new session even if one exists
    #[arg(long)]
    pub new: bool,
    /// Skip tool permission prompts
    #[arg(long)]
    pub yolo: bool,
    /// Install the tool and exit
    #[arg(long)]
    pub install: bool,
    /// Run the tool's installer
    #[arg(long)]
    pub upgrade: bool,
    /// Arguments passed to the tool
    #[arg(last = true)]
    pub args: Vec<OsString>,
}

/// Maps an `argv[0]` basename to the subcommand words it stands for.
pub fn multicall(name: &str) -> Option<&'static [&'static str]> {
    Some(match name {
        "tobyd" => &["internal", "daemon"],
        "toby-relay" => &["guest", "relay"],
        "toby-session" => &["guest", "session"],
        "toby-connect" => &["guest", "connect"],
        "toby-helper" => &["guest", "helper"],
        _ => return None,
    })
}

/// Rewrites `argv` so that a multi-call name dispatches like the matching subcommand.
pub fn expand_multicall(argv: Vec<OsString>) -> Vec<OsString> {
    let Some(first) = argv.first() else {
        return argv;
    };
    let name = std::path::Path::new(first).file_name().and_then(|n| n.to_str()).unwrap_or_default();
    match multicall(name) {
        Some(words) => {
            let mut out: Vec<OsString> = vec!["toby".into()];
            out.extend(words.iter().map(OsString::from));
            out.extend(argv.into_iter().skip(1));
            out
        }
        None => argv,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::CommandFactory;

    fn os(v: &[&str]) -> Vec<OsString> {
        v.iter().map(OsString::from).collect()
    }

    #[test]
    fn cli_definition_is_valid() {
        Cli::command().debug_assert();
    }

    #[test]
    fn multicall_names_expand() {
        assert_eq!(
            expand_multicall(os(&["/run/toby/bin/toby-connect", "mcp/toby"])),
            os(&["toby", "guest", "connect", "mcp/toby"])
        );
        assert_eq!(expand_multicall(os(&["tobyd"])), os(&["toby", "internal", "daemon"]));
        assert_eq!(expand_multicall(os(&["toby", "doctor"])), os(&["toby", "doctor"]));
    }

    #[test]
    fn unknown_subcommand_is_a_tool() {
        let cli = Cli::try_parse_from(os(&["toby", "claude", "--home", "work", "--", "-p", "x"])).unwrap();
        let Command::Tool(argv) = cli.command else {
            panic!("expected a tool launch");
        };
        let args = ToolArgs::try_parse_from(&argv[1..]).unwrap();
        assert_eq!(argv[0], "claude");
        assert_eq!(args.machine.home.as_deref(), Some("work"));
        assert_eq!(args.args, os(&["-p", "x"]));
    }

    #[test]
    fn daemon_requires_a_subcommand() {
        assert!(Cli::try_parse_from(os(&["toby", "daemon"])).is_err());
        let cli = Cli::try_parse_from(os(&["toby", "internal", "daemon"])).unwrap();
        assert!(matches!(cli.command, Command::Internal(InternalCommand::Daemon)));
    }
}
