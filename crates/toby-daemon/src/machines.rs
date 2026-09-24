//! The machine registry and lifecycle (plan §8): which machine serves a home
//! and root, starting and stopping machines, their attachments, sessions and
//! idle stop.

use std::collections::HashMap;
use std::io;
use std::path::Path;
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use nix::fcntl::{Flock, FlockArg};
use toby_api::{AttachmentInfo, ForwardInfo, MachineInfo, Warning};
use toby_config::global::{Backend, GlobalConfig};
use toby_config::machine::{
    self, Attach, AttachState, Direction, Forward, ForwardState, MachineSpec, MachineStatus, RootSpec, State,
};
use toby_config::paths::{MachineRuntime, Paths};
use toby_proto::types::{Identity, SessionInfo, SpawnSpec, TtySize};
use toby_store::Store;

use crate::control::Control;
use crate::supervisor::Supervisor;

const START_TIMEOUT: Duration = Duration::from_secs(300);
const STOP_TIMEOUT: Duration = Duration::from_secs(60);
/// A guest helper may take up to two minutes (plan §9.6).
const APPLY_TIMEOUT: Duration = Duration::from_secs(150);
const IDLE_CHECK: Duration = Duration::from_secs(30);
/// How long a requested start counts as running before its processes show:
/// as long as a start may take (`wait_ready` clears it sooner).
const START_GRACE: Duration = START_TIMEOUT;

/// Guest ends of the capabilities (plan §11.6).
pub const MODELS_LISTEN: &str = "127.0.0.1:41100";
pub const SANDBOX_SOCKET: &str = "/run/toby/sandbox.sock";

/// An API error: a status class, a stable code and a message.
#[derive(Debug)]
pub struct Error {
    pub kind: ErrorKind,
    pub code: &'static str,
    pub message: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorKind {
    BadRequest,
    NotFound,
    Conflict,
    Internal,
}

impl Error {
    pub fn new(kind: ErrorKind, code: &'static str, message: impl Into<String>) -> Error {
        Error { kind, code, message: message.into() }
    }
}

impl From<io::Error> for Error {
    fn from(e: io::Error) -> Error {
        let (kind, code) = match e.kind() {
            io::ErrorKind::NotFound => (ErrorKind::NotFound, "not-found"),
            io::ErrorKind::InvalidInput => (ErrorKind::BadRequest, "invalid"),
            io::ErrorKind::AlreadyExists => (ErrorKind::Conflict, "exists"),
            io::ErrorKind::ResourceBusy => (ErrorKind::Conflict, "in-use"),
            _ => (ErrorKind::Internal, "error"),
        };
        Error::new(kind, code, e.to_string())
    }
}

pub type Result<T> = std::result::Result<T, Error>;

/// How waiting for a stop ended.
enum Stopped {
    Yes,
    /// Started again after the stop was asked for.
    Restarted,
    TimedOut,
}

/// What the machine's processes report.
#[derive(Debug, Clone)]
pub struct Observed {
    /// `stopped`, `starting`, `ready`, `stopping` or `failed`.
    pub state: &'static str,
    pub status: Option<MachineStatus>,
}

pub struct Machines {
    pub config: GlobalConfig,
    pub paths: Paths,
    pub store: Store,
    pub supervisor: Supervisor,
    /// Serializes starting machines.
    lock: tokio::sync::Mutex<()>,
    /// Serializes attachment changes per machine, which can wait for the
    /// guest.
    machine_locks: Mutex<HashMap<String, std::sync::Arc<tokio::sync::Mutex<()>>>>,
    /// When each machine last had a session, was started or was first seen.
    activity: Mutex<HashMap<String, Instant>>,
    started: Mutex<HashMap<String, Instant>>,
    /// Machines asked to start whose processes may not be visible yet.
    starting: Mutex<HashMap<String, Instant>>,
    /// Machines asked to stop, until they are seen stopped.
    stopping: Mutex<HashMap<String, Instant>>,
    /// Sessions being created: their attachments and forwards are kept.
    creating: Mutex<std::collections::HashSet<String>>,
    /// Sessions created recently, by the client's request ID.
    created: Mutex<std::collections::VecDeque<(String, String, String)>>,
    /// Serializes tool installs and file writes per machine (plan §16.1).
    tool_locks: Mutex<HashMap<String, std::sync::Arc<tokio::sync::Mutex<()>>>>,
    linger_warned: AtomicBool,
}

fn state_name(s: State) -> &'static str {
    match s {
        State::Starting => "starting",
        State::Ready => "ready",
        State::Stopping => "stopping",
        State::Failed => "failed",
    }
}

fn is_builder(id: &str) -> bool {
    id.starts_with("builder-")
}

/// Checks a guest mount point: absolute, normalized, not the root directory.
pub fn check_guest_path(at: &str) -> Result<()> {
    let normal = at.strip_prefix('/').is_some_and(|rest| {
        !rest.is_empty() && rest.split('/').all(|c| !c.is_empty() && c != "." && c != "..")
    });
    if normal {
        Ok(())
    } else {
        Err(Error::new(
            ErrorKind::BadRequest,
            "attach.invalid-target",
            format!("{at:?} cannot be used as a mount point in the machine"),
        ))
    }
}

pub fn default_guest_path(host: &Path) -> Result<String> {
    let name = host.file_name().and_then(|n| n.to_str()).ok_or_else(|| {
        Error::new(ErrorKind::BadRequest, "attach.invalid-target", "choose a mount point in the machine")
    })?;
    Ok(format!("/toby/workspace/{name}"))
}

/// Default machine resources: half the host's CPUs (2 to 8) and half its
/// memory, at most 8 GiB (plan §14.5).
pub fn default_resources() -> machine::Resources {
    let cpus = std::thread::available_parallelism().map(|n| n.get() as u32).unwrap_or(2);
    let mem = std::fs::read_to_string("/proc/meminfo")
        .ok()
        .and_then(|m| {
            m.lines()
                .find(|l| l.starts_with("MemTotal:"))
                .and_then(|l| l.split_whitespace().nth(1)?.parse::<u64>().ok())
        })
        .map(|kib| (kib * 1024 / 2).min(8 << 30))
        .unwrap_or(4 << 30);
    machine::Resources { cpus: (cpus / 2).clamp(2, 8), memory: format!("{}M", mem >> 20) }
}

impl Machines {
    pub fn new(config: GlobalConfig, paths: Paths, supervisor: Supervisor) -> Machines {
        let store = Store::new(paths.clone());
        Machines {
            config,
            paths,
            store,
            supervisor,
            lock: tokio::sync::Mutex::new(()),
            machine_locks: Mutex::default(),
            activity: Mutex::default(),
            started: Mutex::default(),
            starting: Mutex::default(),
            stopping: Mutex::default(),
            creating: Mutex::default(),
            created: Mutex::default(),
            tool_locks: Mutex::default(),
            linger_warned: AtomicBool::new(false),
        }
    }

    /// The configuration as it is on disk now, for settings that apply
    /// without restarting the daemon (tools, model providers); the one the
    /// daemon started with if the file cannot be read.
    pub fn current_config(&self) -> GlobalConfig {
        GlobalConfig::load(&self.paths.global_config()).unwrap_or_else(|_| self.config.clone())
    }

    /// The lock that serializes tool operations in a machine.
    pub fn tool_lock(&self, id: &str) -> std::sync::Arc<tokio::sync::Mutex<()>> {
        self.tool_locks.lock().unwrap().entry(id.to_string()).or_default().clone()
    }

    fn machine_lock(&self, id: &str) -> std::sync::Arc<tokio::sync::Mutex<()>> {
        self.machine_locks.lock().unwrap().entry(id.to_string()).or_default().clone()
    }

    pub fn runtime(&self, id: &str) -> MachineRuntime {
        self.paths.machine_runtime(id)
    }

    /// Machine records, without builder machines.
    pub fn records(&self) -> Vec<MachineSpec> {
        let Ok(entries) = std::fs::read_dir(self.paths.state.join("machines")) else { return Vec::new() };
        let mut out: Vec<MachineSpec> = entries
            .flatten()
            .filter_map(|e| {
                let id = e.file_name().to_string_lossy().into_owned();
                if is_builder(&id) {
                    return None;
                }
                MachineSpec::load(&self.paths.machine_desired(&id)).ok().filter(|s| s.id == id)
            })
            .collect();
        out.sort_by(|a, b| a.id.cmp(&b.id));
        out
    }

    pub fn record(&self, id: &str) -> Result<MachineSpec> {
        if id.is_empty() || !id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-') {
            return Err(Error::new(
                ErrorKind::BadRequest,
                "machine.invalid-id",
                format!("invalid machine ID {id:?}"),
            ));
        }
        MachineSpec::load(&self.paths.machine_desired(id))
            .map_err(|_| Error::new(ErrorKind::NotFound, "machine.not-found", format!("no machine {id}")))
    }

    pub async fn observe(&self, id: &str) -> Observed {
        let runtime = self.runtime(id);
        if Control::connect(&runtime).await.is_ok() {
            let status = MachineStatus::load(&runtime.status()).ok();
            let state = status.as_ref().map_or("starting", |s| state_name(s.state));
            return Observed { state, status };
        }
        if self.supervisor.active(id, &runtime).await {
            // Without its host process a machine is either coming up or
            // going down; a stop was requested for the latter.
            let state = if self.stopping.lock().unwrap().contains_key(id) { "stopping" } else { "starting" };
            return Observed { state, status: None };
        }
        self.stopping.lock().unwrap().remove(id);
        Observed { state: "stopped", status: None }
    }

    async fn running(&self, id: &str) -> bool {
        // A start just requested counts, before its processes show.
        let starting = self.starting.lock().unwrap().get(id).is_some_and(|t| t.elapsed() < START_GRACE);
        starting || self.observe(id).await.state != "stopped"
    }

    /// Appends to the machine's lifecycle log.
    fn history(&self, id: &str, event: &str) {
        use std::io::Write;
        let line = format!("{{\"time\":{},\"event\":\"{event}\"}}\n", toby_store::records::now());
        let path = self.paths.machine_state_dir(id).join("history.jsonl");
        if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(path) {
            let _ = f.write_all(line.as_bytes());
        }
    }

    pub async fn info(&self, spec: &MachineSpec) -> MachineInfo {
        let observed = self.observe(&spec.id).await;
        let mut sessions = 0;
        if observed.state == "ready"
            && let Ok(mut c) = Control::connect(&self.runtime(&spec.id)).await
        {
            sessions = c.sessions().await.map(|l| l.iter().filter(|s| s.exit.is_none()).count()).unwrap_or(0);
        }
        let now = Instant::now();
        let running = observed.state != "stopped";
        let since = |m: &Mutex<HashMap<String, Instant>>| {
            m.lock().unwrap().get(&spec.id).filter(|_| running).map(|t| now.duration_since(*t).as_secs())
        };
        let status_attach = observed.status.as_ref().map(|s| s.attach.clone()).unwrap_or_default();
        let status_forward = observed.status.as_ref().map(|s| s.forward.clone()).unwrap_or_default();
        MachineInfo {
            id: spec.id.clone(),
            home: spec.home.clone(),
            root: match &spec.root {
                RootSpec::Named(n) => n.clone(),
                RootSpec::Image { image } => format!("image {image}"),
                RootSpec::CloudImage { cloud_image } => cloud_image.display().to_string(),
            },
            image: match &spec.root {
                RootSpec::Named(n) => self.store.root(n).ok().map(|r| r.image),
                RootSpec::Image { image } => Some(image.clone()),
                RootSpec::CloudImage { .. } => None,
            },
            state: observed.state.into(),
            error: observed.status.and_then(|s| s.error),
            sessions,
            attachments: attachment_infos(spec, &status_attach, running),
            forwards: spec
                .forward
                .iter()
                .map(|f| forward_info(f, status_forward.iter().find(|s| s.id == f.id), running))
                .collect(),
            uptime_secs: since(&self.started),
            idle_secs: if sessions > 0 { Some(0) } else { since(&self.activity) },
        }
    }

    pub async fn list(&self) -> Vec<MachineInfo> {
        let mut out = Vec::new();
        for spec in self.records() {
            out.push(self.info(&spec).await);
        }
        out
    }

    /// The linger warning, once per daemon lifetime (plan §12.2).
    pub async fn warnings(&self) -> Vec<Warning> {
        if self.supervisor.backend() != Backend::SystemdUser || self.linger_warned.load(Ordering::Acquire) {
            return Vec::new();
        }
        let uid = nix::unistd::getuid().as_raw();
        match toby_svc::systemd::linger(uid).await {
            Ok(false) if !self.linger_warned.swap(true, Ordering::AcqRel) => vec![Warning {
                id: "daemon.linger-disabled".into(),
                message: concat!(
                    "linger is off; machines and sessions stop shortly after your last login session ends.\n",
                    "         enable: toby linger on   ·   ",
                    "silence: add \"daemon.linger-disabled\" to settings.suppress_warnings"
                )
                .into(),
            }],
            _ => Vec::new(),
        }
    }

    /// The machine for a home and root, started if needed (plan §8.2).
    pub async fn ensure(&self, req: toby_api::EnsureMachine) -> Result<MachineSpec> {
        let toby_api::EnsureMachine { home, root, ephemeral, cpus, memory } = req;
        let home = home.unwrap_or_else(|| self.config.defaults.home().to_string());
        let home_rec = self.store.home(&home).map_err(|_| {
            Error::new(
                ErrorKind::NotFound,
                "home.not-found",
                format!("there is no home {home}; create it with: toby home create {home}"),
            )
        })?;
        if !home_rec.formatted {
            return Err(Error::new(
                ErrorKind::Conflict,
                "home.unformatted",
                format!("home {home} was never formatted; remove it and create it again"),
            ));
        }
        let root = root.or(home_rec.default_root).unwrap_or_else(|| "default".into());
        self.store.root(&root).map_err(|_| {
            Error::new(
                ErrorKind::NotFound,
                "root.not-found",
                format!("there is no root {root}; create it with: toby root create {root} --image default"),
            )
        })?;

        let _lock = self.lock.lock().await;
        let records = self.records();
        let pair =
            |s: &MachineSpec| s.home.as_deref() == Some(&home) && s.root == RootSpec::Named(root.clone());
        for other in records.iter().filter(|s| !pair(s)) {
            let shares_home = other.home.as_deref() == Some(&home);
            let shares_root = other.root == RootSpec::Named(root.clone());
            if (shares_home || shares_root) && self.running(&other.id).await {
                let what = if shares_home { format!("home {home}") } else { format!("root {root}") };
                return Err(Error::new(
                    ErrorKind::Conflict,
                    "machine.pair-in-use",
                    format!(
                        "{what} is in use by machine {} ({} with {}); stop it with: toby machine stop {}",
                        other.id,
                        other.home.as_deref().unwrap_or("no home"),
                        match &other.root {
                            RootSpec::Named(r) => format!("root {r}"),
                            _ => "another root".into(),
                        },
                        other.id
                    ),
                ));
            }
        }

        let template = match records.into_iter().find(pair) {
            Some(spec) => spec,
            None => MachineSpec {
                schema: machine::SCHEMA,
                generation: 0,
                id: toby_config::new_id().to_lowercase(),
                home: Some(home.clone()),
                root: RootSpec::Named(root.clone()),
                ephemeral: false,
                resources: default_resources(),
                boot: Default::default(),
                disk: Vec::new(),
                attach: Vec::new(),
                forward: Vec::new(),
                capabilities: Default::default(),
                idle_timeout: None,
            },
        };
        let id = template.id.clone();
        // A machine being stopped is started again once it has stopped.
        if self.observe(&id).await.state == "stopping" {
            self.wait_stopped(&id, Instant::now()).await;
        }
        if self.running(&id).await {
            // Recorded before letting go of the lock, so idle stop sees it.
            self.activity.lock().unwrap().insert(id.clone(), Instant::now());
            drop(_lock);
            self.wait_ready(&id).await?;
            return Ok(template);
        }
        // Attachment edits for this machine wait until it has started; the
        // desired state is read again under its file lock.
        let machine_lock = self.machine_lock(&id);
        let _machine = machine_lock.lock().await;
        let spec = {
            let _file = self.lock_desired(&id)?;
            let path = self.paths.machine_desired(&id);
            let mut spec = MachineSpec::load(&path).unwrap_or(template);
            // Only persistent attachments and forwards outlive a run.
            spec.attach.retain(|a| a.persist);
            spec.forward.retain(|f| f.persist);
            spec.capabilities.models_listen = Some(MODELS_LISTEN.into());
            toby_proxy::ensure_token(&self.paths, &id)?;
            spec.capabilities.sandbox_socket = Some(SANDBOX_SOCKET.into());
            spec.ephemeral = ephemeral;
            if let Some(cpus) = cpus {
                spec.resources.cpus = cpus;
            }
            if let Some(memory) = memory {
                spec.resources.memory = memory;
            }
            spec.generation += 1;
            spec.store(&path)?;
            spec
        };
        self.starting.lock().unwrap().insert(spec.id.clone(), Instant::now());
        self.stopping.lock().unwrap().remove(&spec.id);
        self.history(&spec.id, "start");
        if let Err(e) = self.supervisor.start(&spec.id).await {
            self.starting.lock().unwrap().remove(&spec.id);
            return Err(e.into());
        }
        let now = Instant::now();
        self.started.lock().unwrap().insert(spec.id.clone(), now);
        self.activity.lock().unwrap().insert(spec.id.clone(), now);
        drop(_machine);
        drop(_lock);
        self.wait_ready(&spec.id).await?;
        Ok(spec)
    }

    /// Waits until a starting machine is ready.
    async fn wait_ready(&self, id: &str) -> Result<()> {
        let result = self.wait_ready_inner(id).await;
        self.starting.lock().unwrap().remove(id);
        result
    }

    async fn wait_ready_inner(&self, id: &str) -> Result<()> {
        let deadline = tokio::time::Instant::now() + START_TIMEOUT;
        let grace = tokio::time::Instant::now() + Duration::from_secs(10);
        loop {
            let observed = self.observe(id).await;
            match observed.state {
                "ready" => return Ok(()),
                "failed" => {
                    let error = observed.status.and_then(|s| s.error).unwrap_or_default();
                    return Err(Error::new(
                        ErrorKind::Internal,
                        "machine.failed",
                        format!(
                            "machine {id} failed to start: {error}\nstop it with: toby machine stop {id}"
                        ),
                    ));
                }
                "stopped" if tokio::time::Instant::now() > grace => {
                    return Err(Error::new(
                        ErrorKind::Internal,
                        "machine.stopped",
                        format!("machine {id} stopped while starting; see: toby machine logs {id}"),
                    ));
                }
                _ => {}
            }
            if tokio::time::Instant::now() > deadline {
                return Err(Error::new(
                    ErrorKind::Internal,
                    "machine.start-timeout",
                    format!("machine {id} did not become ready in time"),
                ));
            }
            tokio::time::sleep(Duration::from_millis(250)).await;
        }
    }

    /// Powers the machine off, stopping its processes if the guest does not.
    pub async fn stop(&self, id: &str) -> Result<()> {
        self.record(id)?;
        let asked = self.request_stop(id, "stop").await;
        match self.wait_stopped(id, asked).await {
            Stopped::Restarted => return Ok(()),
            Stopped::TimedOut => self.supervisor.kill(id, &self.runtime(id)).await?,
            Stopped::Yes => {}
        }
        self.forget(id);
        Ok(())
    }

    /// Asks the guest to power off; returns when that was asked.
    async fn request_stop(&self, id: &str, event: &str) -> Instant {
        let now = Instant::now();
        self.history(id, event);
        self.stopping.lock().unwrap().insert(id.to_string(), now);
        if let Ok(mut c) = Control::connect(&self.runtime(id)).await {
            let _ = c.stop().await;
        }
        now
    }

    /// Waits until the machine's processes are gone, or it was started
    /// again after the stop `asked` (then there is nothing left to stop).
    async fn wait_stopped(&self, id: &str, asked: Instant) -> Stopped {
        let deadline = tokio::time::Instant::now() + STOP_TIMEOUT;
        while tokio::time::Instant::now() < deadline {
            let restarted = self.starting.lock().unwrap().get(id).is_some_and(|t| *t > asked)
                || self.started.lock().unwrap().get(id).is_some_and(|t| *t > asked);
            if restarted {
                return Stopped::Restarted;
            }
            if self.observe(id).await.state == "stopped" {
                return Stopped::Yes;
            }
            tokio::time::sleep(Duration::from_millis(250)).await;
        }
        Stopped::TimedOut
    }

    fn forget(&self, id: &str) {
        self.started.lock().unwrap().remove(id);
        self.activity.lock().unwrap().remove(id);
    }

    /// A running machine by ID, or the machine for a home and root.
    pub async fn select(&self, target: &toby_api::MachineSelector) -> Result<MachineSpec> {
        match &target.machine {
            Some(id) => {
                let spec = self.record(id)?;
                // Under the start lock, so idle stop either sees this activity
                // or has already asked the machine to stop.
                let _lock = self.lock.lock().await;
                let state = self.observe(id).await.state;
                if state != "ready" {
                    return Err(Error::new(
                        ErrorKind::Conflict,
                        "machine.not-running",
                        format!("machine {id} is {state}"),
                    ));
                }
                self.activity.lock().unwrap().insert(id.clone(), Instant::now());
                Ok(spec)
            }
            None => {
                let req = toby_api::EnsureMachine {
                    home: target.home.clone(),
                    root: target.root.clone(),
                    ..Default::default()
                };
                self.ensure(req).await
            }
        }
    }

    // Attachments

    /// The lock that serializes writes of a machine's desired state.
    fn lock_desired(&self, id: &str) -> io::Result<Flock<std::fs::File>> {
        let path = self.paths.machine_desired(id);
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let file = std::fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .write(true)
            .open(path.with_extension("lock"))?;
        Flock::lock(file, FlockArg::LockExclusive).map_err(|(_, e)| io::Error::from(e))
    }

    /// Changes the desired state under its lock and returns the new
    /// generation.
    fn update_desired(&self, id: &str, f: impl FnOnce(&mut MachineSpec) -> Result<()>) -> Result<u64> {
        let path = self.paths.machine_desired(id);
        let _lock = self.lock_desired(id)?;
        let mut spec = MachineSpec::load(&path)?;
        f(&mut spec)?;
        spec.generation += 1;
        spec.store(&path)?;
        Ok(spec.generation)
    }

    /// Waits until a running machine has applied `generation`.
    async fn applied(&self, id: &str, generation: u64) -> Result<MachineStatus> {
        let runtime = self.runtime(id);
        let deadline = tokio::time::Instant::now() + APPLY_TIMEOUT;
        loop {
            if let Ok(s) = MachineStatus::load(&runtime.status())
                && s.observed_generation >= generation
            {
                return Ok(s);
            }
            if tokio::time::Instant::now() > deadline {
                let reason = MachineStatus::load(&runtime.status()).ok().and_then(|s| s.error);
                return Err(Error::new(
                    ErrorKind::Internal,
                    "machine.apply-timeout",
                    match reason {
                        Some(r) => format!("machine {id} did not apply the change: {r}"),
                        None => format!("machine {id} did not apply the change in time"),
                    },
                ));
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }

    /// Adds an attachment; for a session, one already at the same place
    /// with the same directory is shared instead.
    pub async fn add_attachment(
        &self,
        id: &str,
        req: toby_api::AddAttachment,
        session: Option<&str>,
    ) -> Result<AttachmentInfo> {
        self.record(id)?;
        {
            // Under the start lock, so idle stop either sees this activity or
            // has already asked the machine to stop (then this is refused).
            let _lock = self.lock.lock().await;
            let state = self.observe(id).await.state;
            if state == "stopping" {
                return Err(Error::new(
                    ErrorKind::Conflict,
                    "machine.busy",
                    format!("machine {id} is stopping; try again"),
                ));
            }
            self.activity.lock().unwrap().insert(id.to_string(), Instant::now());
        }
        let host = std::fs::canonicalize(&req.host).map_err(|_| {
            Error::new(ErrorKind::NotFound, "attach.missing", format!("{} does not exist", req.host))
        })?;
        if !host.is_dir() {
            return Err(Error::new(
                ErrorKind::BadRequest,
                "attach.not-directory",
                format!("{} is not a directory", host.display()),
            ));
        }
        let host = host
            .to_str()
            .ok_or_else(|| Error::new(ErrorKind::BadRequest, "attach.invalid-path", "the path is not UTF-8"))?
            .to_string();
        // Without a place, the directory's name under /toby/workspace; another
        // directory of the same name gets a numbered place (chosen below,
        // under the lock).
        let numbered = req.at.is_none();
        let at = match req.at {
            Some(at) => at,
            None => default_guest_path(Path::new(&host))?,
        };
        check_guest_path(&at)?;

        let attach = Attach {
            id: toby_config::new_id().to_lowercase(),
            host: host.clone(),
            at: at.clone(),
            read_only: req.read_only,
            pinned: session.is_none() && (req.pinned || req.persist),
            persist: session.is_none() && req.persist,
            sessions: session.map(|s| vec![s.to_string()]).unwrap_or_default(),
        };
        let lock = self.machine_lock(id);
        let _lock = lock.lock().await;
        let running = self.observe(id).await.state == "ready";
        if !running && !attach.persist {
            return Err(Error::new(
                ErrorKind::Conflict,
                "machine.not-running",
                format!("machine {id} is not running; --persist mounts it whenever it starts"),
            ));
        }
        let mut shared = None;
        let mut attach = attach;
        let generation = self.update_desired(id, |spec| {
            if numbered {
                let taken = |p: &str| spec.attach.iter().any(|a| a.at == p && a.host != host);
                let mut n = 2;
                while taken(&attach.at) {
                    attach.at = format!("{at}-{n}");
                    n += 1;
                }
            }
            let at = attach.at.clone();
            if let Some(a) = spec.attach.iter_mut().find(|a| a.at == at) {
                if let Some(s) = session
                    && a.host == host
                    && a.read_only == attach.read_only
                {
                    if !a.sessions.iter().any(|x| x == s) {
                        a.sessions.push(s.to_string());
                    }
                    shared = Some(a.clone());
                    return Ok(());
                }
                return Err(Error::new(
                    ErrorKind::Conflict,
                    "attach.target-in-use",
                    format!("{} is already mounted at {at}", a.host),
                ));
            }
            spec.attach.push(attach.clone());
            Ok(())
        })?;
        if let Some(a) = shared {
            let status = MachineStatus::load(&self.runtime(id).status()).ok();
            let entry = status.as_ref().and_then(|s| s.attach.iter().find(|x| x.id == a.id));
            return Ok(attachment_info(&a, entry, running));
        }
        if !running {
            return Ok(attachment_info(&attach, None, false));
        }
        let status = match self.applied(id, generation).await {
            Ok(s) => s,
            Err(e) => {
                // Do not let it appear later after reporting a failure.
                self.update_desired(id, |spec| {
                    spec.attach.retain(|a| a.id != attach.id);
                    Ok(())
                })?;
                return Err(e);
            }
        };
        match status.attach.iter().find(|a| a.id == attach.id) {
            Some(s) if s.state == AttachState::Ready => Ok(attachment_info(&attach, Some(s), true)),
            other => {
                let error = other.and_then(|a| a.error.clone()).unwrap_or_else(|| "unknown error".into());
                self.update_desired(id, |spec| {
                    spec.attach.retain(|a| a.id != attach.id);
                    Ok(())
                })?;
                Err(Error::new(ErrorKind::Internal, "attach.failed", format!("mounting {host}: {error}")))
            }
        }
    }

    /// Removes an attachment by ID or host path; refused while it is in use.
    pub async fn remove_attachment(&self, id: &str, target: &str) -> Result<()> {
        self.record(id)?;
        // Between starting and ready the guest may still hold the mount, and
        // nothing could confirm the detach.
        let state = self.observe(id).await.state;
        if state == "failed" {
            return Err(Error::new(
                ErrorKind::Conflict,
                "machine.failed",
                format!("machine {id} failed; stop it first with: toby machine stop {id}"),
            ));
        }
        if state != "ready" && state != "stopped" {
            return Err(Error::new(
                ErrorKind::Conflict,
                "machine.busy",
                format!("machine {id} is {state}; try again when it is ready"),
            ));
        }
        let host = std::fs::canonicalize(target).ok();
        let lock = self.machine_lock(id);
        let _lock = lock.lock().await;
        let mut removed = None;
        let generation = self.update_desired(id, |spec| {
            let pos = spec
                .attach
                .iter()
                .position(|a| a.id == target || host.as_deref().is_some_and(|h| Path::new(&a.host) == h))
                .ok_or_else(|| {
                    Error::new(
                        ErrorKind::NotFound,
                        "attach.not-found",
                        format!("{target} is not mounted in {id}"),
                    )
                })?;
            removed = Some(spec.attach.remove(pos));
            Ok(())
        })?;
        let removed = removed.expect("set by update_desired");
        match self.observe(id).await.state {
            "ready" => {}
            "stopped" => return Ok(()),
            // It restarted between the checks: nothing can confirm the detach.
            state => {
                self.update_desired(id, |spec| {
                    spec.attach.push(removed.clone());
                    Ok(())
                })?;
                return Err(Error::new(
                    ErrorKind::Conflict,
                    "machine.busy",
                    format!("machine {id} is {state}; try again when it is ready"),
                ));
            }
        }
        let status = match self.applied(id, generation).await {
            Ok(s) => s,
            Err(e) => {
                // The guest may still have it; keep it desired so it is
                // not detached later without a request.
                self.update_desired(id, |spec| {
                    spec.attach.push(removed.clone());
                    Ok(())
                })?;
                return Err(e);
            }
        };
        if let Some(still) = status.attach.iter().find(|a| a.id == removed.id) {
            let error = still.error.clone().unwrap_or_else(|| "unknown error".into());
            // Keep the desired state in line with the guest, which still has it.
            self.update_desired(id, |spec| {
                spec.attach.push(removed.clone());
                Ok(())
            })?;
            return Err(Error::new(ErrorKind::Conflict, "attach.busy", error));
        }
        Ok(())
    }

    // Forwards

    /// Adds a forward; for a session, the same forward in this machine is
    /// shared instead.
    pub async fn add_forward(
        &self,
        id: &str,
        req: toby_api::AddForward,
        session: Option<&str>,
    ) -> Result<ForwardInfo> {
        self.record(id)?;
        let direction = match req.direction.as_str() {
            "host-to-guest" => Direction::HostToGuest,
            "guest-to-host" => Direction::GuestToHost,
            other => {
                return Err(Error::new(
                    ErrorKind::BadRequest,
                    "forward.invalid",
                    format!("unknown direction {other:?}"),
                ));
            }
        };
        for addr in [&req.host, &req.guest] {
            if addr.parse::<std::net::SocketAddr>().is_err() {
                return Err(Error::new(
                    ErrorKind::BadRequest,
                    "forward.invalid",
                    format!("{addr:?} is not an address such as 127.0.0.1:3000"),
                ));
            }
        }
        let forward = Forward {
            id: format!("f{}", toby_config::new_id().to_lowercase()),
            direction,
            host: req.host.clone(),
            guest: req.guest.clone(),
            pinned: session.is_none() && (req.pinned || req.persist),
            persist: session.is_none() && req.persist,
            sessions: session.map(|s| vec![s.to_string()]).unwrap_or_default(),
        };
        if let Some(s) = session {
            let lock = self.machine_lock(id);
            let _lock = lock.lock().await;
            let mut shared = None;
            self.update_desired(id, |spec| {
                if let Some(f) = spec
                    .forward
                    .iter_mut()
                    .find(|f| f.direction == direction && f.host == req.host && f.guest == req.guest)
                {
                    if !f.sessions.iter().any(|x| x == s) {
                        f.sessions.push(s.to_string());
                    }
                    shared = Some(f.clone());
                }
                Ok(())
            })?;
            if let Some(f) = shared {
                return Ok(forward_info(&f, None, true));
            }
        }
        // A host address can be listened on once, across machines.
        if direction == Direction::HostToGuest {
            for other in self.records() {
                if let Some(f) =
                    other.forward.iter().find(|f| f.direction == Direction::HostToGuest && f.host == req.host)
                    && (other.id == id || self.running(&other.id).await)
                {
                    return Err(Error::new(
                        ErrorKind::Conflict,
                        "forward.host-in-use",
                        format!("{} is already forwarded by machine {} ({})", req.host, other.id, f.id),
                    ));
                }
            }
        } else if self.record(id)?.forward.iter().any(|f| f.direction == direction && f.guest == req.guest) {
            return Err(Error::new(
                ErrorKind::Conflict,
                "forward.guest-in-use",
                format!("{} is already forwarded in machine {id}", req.guest),
            ));
        }

        let lock = self.machine_lock(id);
        let _lock = lock.lock().await;
        let running = self.observe(id).await.state == "ready";
        if !running && !forward.persist {
            return Err(Error::new(
                ErrorKind::Conflict,
                "machine.not-running",
                format!("machine {id} is not running; --persist adds the forward whenever it starts"),
            ));
        }
        let generation = self.update_desired(id, |spec| {
            spec.forward.push(forward.clone());
            Ok(())
        })?;
        if !running {
            return Ok(forward_info(&forward, None, false));
        }
        let status = self.applied(id, generation).await;
        let failure = match &status {
            Ok(s) => match s.forward.iter().find(|f| f.id == forward.id) {
                Some(f) if f.state == ForwardState::Listening => None,
                other => Some(other.and_then(|f| f.error.clone()).unwrap_or_else(|| "unknown error".into())),
            },
            Err(e) => Some(e.message.clone()),
        };
        if let Some(error) = failure {
            self.update_desired(id, |spec| {
                spec.forward.retain(|f| f.id != forward.id);
                Ok(())
            })?;
            return Err(Error::new(ErrorKind::Internal, "forward.failed", error));
        }
        let status = status?;
        Ok(forward_info(&forward, status.forward.iter().find(|f| f.id == forward.id), true))
    }

    pub async fn remove_forward(&self, id: &str, fid: &str) -> Result<()> {
        self.record(id)?;
        // Between starting and ready, nothing could confirm the removal.
        let state = self.observe(id).await.state;
        if state != "ready" && state != "stopped" {
            return Err(Error::new(
                ErrorKind::Conflict,
                "machine.busy",
                format!("machine {id} is {state}; try again when it is ready"),
            ));
        }
        let lock = self.machine_lock(id);
        let _lock = lock.lock().await;
        let generation = self.update_desired(id, |spec| {
            let before = spec.forward.len();
            spec.forward.retain(|f| f.id != fid);
            if spec.forward.len() == before {
                return Err(Error::new(
                    ErrorKind::NotFound,
                    "forward.not-found",
                    format!("no forward {fid} in {id}"),
                ));
            }
            Ok(())
        })?;
        if self.observe(id).await.state == "ready" {
            self.applied(id, generation).await?;
        }
        Ok(())
    }

    // Sessions

    /// The tool manifests: built-in ones and the user's (plan §16.1).
    pub fn manifests(&self) -> io::Result<std::collections::BTreeMap<String, toby_tools::Manifest>> {
        let dir = self.paths.global_config().parent().map(|d| d.join("tools")).unwrap_or_default();
        toby_tools::load(&dir)
    }

    pub fn manifest(&self, name: &str) -> Result<toby_tools::Manifest> {
        self.manifests()?.remove(name).ok_or_else(|| {
            Error::new(ErrorKind::NotFound, "tool.unknown", format!("there is no tool {name}"))
        })
    }

    /// Starts a session: a command, or a tool with its environment. The
    /// session's attachments (projects) and a tool's login forwards last as
    /// long as sessions use them.
    pub async fn create_session(
        &self,
        req: toby_api::CreateSession,
    ) -> Result<(MachineSpec, String, Vec<Warning>)> {
        if req.argv.is_empty() && req.tool.is_none() {
            return Err(Error::new(ErrorKind::BadRequest, "session.no-command", "no command given"));
        }
        let request_id = req.request_id.clone();
        if let Some(rid) = &request_id {
            let found = self.created.lock().unwrap().iter().find(|(r, _, _)| r == rid).cloned();
            if let Some((_, machine, session)) = found {
                return Ok((self.record(&machine)?, session, Vec::new()));
            }
        }
        let manifest = req.tool.as_deref().map(|t| self.manifest(t)).transpose()?;
        let spec = self.select(&req.target).await?;
        let session_id = toby_config::new_id();
        let mut warnings = Vec::new();
        // Until it runs, the session's items must not look abandoned; the
        // guard also clears the mark if the request is abandoned.
        struct Creating<'a>(&'a Machines, String);
        impl Drop for Creating<'_> {
            fn drop(&mut self) {
                self.0.creating.lock().unwrap().remove(&self.1);
            }
        }
        self.creating.lock().unwrap().insert(session_id.clone());
        let _creating = Creating(self, session_id.clone());
        let id = self.create_session_with(req, manifest, &spec, &session_id, &mut warnings).await?;
        if let Some(rid) = request_id {
            let mut created = self.created.lock().unwrap();
            created.push_back((rid, spec.id.clone(), id.clone()));
            if created.len() > 256 {
                created.pop_front();
            }
        }
        Ok((spec, id, warnings))
    }

    async fn create_session_with(
        &self,
        req: toby_api::CreateSession,
        manifest: Option<toby_tools::Manifest>,
        spec: &MachineSpec,
        session_id: &str,
        warnings: &mut Vec<Warning>,
    ) -> Result<String> {
        let spec = spec.clone();
        let session_id = session_id.to_string();

        let mut workspace = None;
        for a in req.attachments {
            let info = self.add_attachment(&spec.id, a, Some(&session_id)).await?;
            workspace.get_or_insert(info.at);
        }
        let cwd = req.cwd.or(workspace.clone());
        let (argv, env) = match &manifest {
            Some(m) => {
                for f in &m.tool.forwards {
                    let addr = format!("127.0.0.1:{}", f.port);
                    let fwd = toby_api::AddForward {
                        direction: f.direction.clone(),
                        host: addr.clone(),
                        guest: addr,
                        pinned: false,
                        persist: false,
                    };
                    if let Err(e) = self.add_forward(&spec.id, fwd, Some(&session_id)).await {
                        warnings.push(Warning {
                            id: "tool.login-forward".into(),
                            message: format!("{}'s login may not complete: {}", m.tool.name, e.message),
                        });
                    }
                }
                let ws = workspace.as_deref().unwrap_or("");
                let (argv, mut env) = crate::tools::launch(self, &spec, m, ws, &req.argv, req.yolo)?;
                // The client's own settings (its terminal type) unless the
                // tool sets them.
                for (k, v) in req.env {
                    if !env.iter().any(|(ek, _)| *ek == k) {
                        env.push((k, v));
                    }
                }
                (argv, env)
            }
            None => (req.argv, req.env),
        };
        let session = SpawnSpec {
            session_id,
            argv,
            env,
            cwd,
            identity: if spec.home.is_none() { Identity::Root } else { req.identity },
            tty: req.tty.map(|t| TtySize { rows: t.rows, cols: t.cols }),
            keep_after_exit: true,
            start_on_attach: true,
        };
        let mut c = Control::connect(&self.runtime(&spec.id)).await?;
        let id = c.spawn(session).await?;
        self.activity.lock().unwrap().insert(spec.id.clone(), Instant::now());
        Ok(id)
    }

    /// Removes attachments and forwards whose sessions have all ended.
    async fn release_session_items(&self, spec: &MachineSpec) {
        let has_owned = spec.attach.iter().any(|a| !a.sessions.is_empty())
            || spec.forward.iter().any(|f| !f.sessions.is_empty());
        if !has_owned {
            return;
        }
        // Read before the session list: a session that finishes starting in
        // between is then in one of the two.
        let creating: Vec<String> = self.creating.lock().unwrap().iter().cloned().collect();
        let Ok(mut c) = Control::connect(&self.runtime(&spec.id)).await else { return };
        let Ok(sessions) = c.sessions().await else { return };
        let mut live: Vec<String> = sessions.into_iter().filter(|s| s.exit.is_none()).map(|s| s.id).collect();
        live.extend(creating);
        let lock = self.machine_lock(&spec.id);
        let _lock = lock.lock().await;
        live.extend(self.creating.lock().unwrap().iter().cloned());
        let _ = self.update_desired(&spec.id, |spec| {
            for a in &mut spec.attach {
                a.sessions.retain(|s| live.contains(s));
            }
            for f in &mut spec.forward {
                f.sessions.retain(|s| live.contains(s));
            }
            // Items that had sessions and have none left go, unless pinned.
            spec.attach.retain(|a| a.pinned || !a.sessions.is_empty() || a.persist);
            spec.forward.retain(|f| f.pinned || !f.sessions.is_empty() || f.persist);
            Ok(())
        });
    }

    /// Sessions of every running machine, with the machine's ID.
    pub async fn sessions(&self) -> Vec<(String, SessionInfo)> {
        let mut out = Vec::new();
        for spec in self.records() {
            let Ok(mut c) = Control::connect(&self.runtime(&spec.id)).await else { continue };
            for s in c.sessions().await.unwrap_or_default() {
                out.push((spec.id.clone(), s));
            }
        }
        out
    }

    /// Sends a signal to a session, or ends it: hangup and terminate, then
    /// kill if it still runs after a grace period (interactive shells ignore
    /// SIGTERM).
    pub async fn kill_session(&self, session: &str, signal: Option<i32>) -> Result<()> {
        let machine =
            self.sessions().await.into_iter().find(|(_, s)| s.id == session).map(|(m, _)| m).ok_or_else(
                || Error::new(ErrorKind::NotFound, "session.not-found", format!("no session {session}")),
            )?;
        let mut c = Control::connect(&self.runtime(&machine)).await?;
        if let Some(signal) = signal {
            c.kill(session, signal).await?;
            return Ok(());
        }
        for signal in [libc_signal::SIGHUP, libc_signal::SIGTERM] {
            c.kill(session, signal).await?;
        }
        for _ in 0..30 {
            let running = c.sessions().await?.iter().any(|s| s.id == session && s.exit.is_none());
            if !running {
                return Ok(());
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
        c.kill(session, libc_signal::SIGKILL).await?;
        Ok(())
    }

    // Idle stop

    /// Sets how long the machine may be idle before it stops.
    pub fn set_idle_timeout(&self, id: &str, timeout: Duration) -> Result<()> {
        let secs = timeout.as_secs();
        if self.record(id)?.idle_timeout == Some(secs) {
            return Ok(());
        }
        self.update_desired(id, |spec| {
            spec.idle_timeout = Some(secs);
            Ok(())
        })
        .map(drop)
    }

    /// Removes records of stopped machines whose home or root no longer
    /// exists: that pair can never run again.
    async fn remove_stale(&self) {
        for spec in self.records() {
            let gone = |r: io::Result<()>| r.is_err_and(|e| e.kind() == io::ErrorKind::NotFound);
            let home_gone = spec.home.as_ref().is_some_and(|h| gone(self.store.home(h).map(drop)));
            let root_gone = matches!(&spec.root, RootSpec::Named(r) if gone(self.store.root(r).map(drop)));
            if (home_gone || root_gone) && !self.running(&spec.id).await {
                eprintln!("removing machine {}: its home or root no longer exists", spec.id);
                let _ = std::fs::remove_dir_all(self.paths.machine_state_dir(&spec.id));
                let _ = std::fs::remove_dir_all(self.runtime(&spec.id).dir);
            }
        }
    }

    /// Releases what ended sessions held and removes stale records, whether
    /// or not machines stop when idle.
    pub async fn session_loop(self: std::sync::Arc<Self>) {
        loop {
            tokio::time::sleep(IDLE_CHECK).await;
            self.remove_stale().await;
            for spec in self.records() {
                if self.observe(&spec.id).await.state == "ready" {
                    self.release_session_items(&spec).await;
                }
            }
        }
    }

    /// Stops machines that had no sessions and nothing pinned for the idle
    /// timeout (plan §8.3). Detached sessions count as activity.
    pub async fn idle_loop(self: std::sync::Arc<Self>, timeout: Duration) {
        loop {
            tokio::time::sleep(IDLE_CHECK).await;
            for spec in self.records() {
                if self.observe(&spec.id).await.state != "ready" {
                    continue;
                }
                let busy = match Control::connect(&self.runtime(&spec.id)).await {
                    Ok(mut c) => {
                        c.sessions().await.map(|l| l.iter().any(|s| s.exit.is_none())).unwrap_or(true)
                    }
                    Err(_) => true,
                };
                let pinned = spec.attach.iter().any(|a| a.pinned) || spec.forward.iter().any(|f| f.pinned);
                let timeout = spec.idle_timeout.map(Duration::from_secs).unwrap_or(timeout);
                let now = Instant::now();
                let last = *self.activity.lock().unwrap().entry(spec.id.clone()).or_insert(now);
                if busy || pinned {
                    self.activity.lock().unwrap().insert(spec.id.clone(), now);
                    continue;
                }
                if now.duration_since(last) < timeout {
                    continue;
                }
                // Decide under the start lock, so a command joining the
                // machine meanwhile keeps it (it records activity first).
                let decided = {
                    let _lock = self.lock.lock().await;
                    // Checked again under the lock: sessions and mounts may
                    // have been added meanwhile.
                    let now_spec = self.record(&spec.id).unwrap_or_else(|_| spec.clone());
                    let pinned =
                        now_spec.attach.iter().any(|a| a.pinned) || now_spec.forward.iter().any(|f| f.pinned);
                    let busy = match Control::connect(&self.runtime(&spec.id)).await {
                        Ok(mut c) => {
                            c.sessions().await.map(|l| l.iter().any(|s| s.exit.is_none())).unwrap_or(true)
                        }
                        Err(_) => true,
                    };
                    let last = self.activity.lock().unwrap().get(&spec.id).copied().unwrap_or(now);
                    let idle = !pinned && !busy && Instant::now().duration_since(last) >= timeout;
                    if idle {
                        eprintln!("stopping idle machine {}", spec.id);
                        Some(self.request_stop(&spec.id, "idle-stop").await)
                    } else {
                        None
                    }
                };
                if let Some(asked) = decided {
                    match self.wait_stopped(&spec.id, asked).await {
                        Stopped::Restarted => continue,
                        Stopped::TimedOut => {
                            let _ = self.supervisor.kill(&spec.id, &self.runtime(&spec.id)).await;
                        }
                        Stopped::Yes => {}
                    }
                    self.forget(&spec.id);
                }
            }
        }
    }
}

mod libc_signal {
    pub const SIGHUP: i32 = nix::sys::signal::Signal::SIGHUP as i32;
    pub const SIGTERM: i32 = nix::sys::signal::Signal::SIGTERM as i32;
    pub const SIGKILL: i32 = nix::sys::signal::Signal::SIGKILL as i32;
}

fn attachment_info(
    a: &Attach,
    status: Option<&toby_config::machine::AttachStatus>,
    running: bool,
) -> AttachmentInfo {
    AttachmentInfo {
        id: a.id.clone(),
        host: a.host.clone(),
        at: a.at.clone(),
        read_only: a.read_only,
        pinned: a.pinned,
        persist: a.persist,
        state: match (status, running) {
            (Some(s), true) if s.state == AttachState::Ready => "ready".into(),
            (Some(_), true) => "failed".into(),
            _ => "pending".into(),
        },
        error: status.and_then(|s| s.error.clone()),
    }
}

fn forward_info(
    f: &Forward,
    status: Option<&toby_config::machine::ForwardStatus>,
    running: bool,
) -> ForwardInfo {
    ForwardInfo {
        id: f.id.clone(),
        direction: match f.direction {
            Direction::HostToGuest => "host-to-guest".into(),
            Direction::GuestToHost => "guest-to-host".into(),
        },
        host: f.host.clone(),
        guest: f.guest.clone(),
        pinned: f.pinned,
        persist: f.persist,
        state: match (status, running) {
            (Some(s), true) if s.state == ForwardState::Listening => "listening".into(),
            (Some(_), true) => "failed".into(),
            _ => "pending".into(),
        },
        error: status.and_then(|s| s.error.clone()),
    }
}

fn attachment_infos(
    spec: &MachineSpec,
    status: &[toby_config::machine::AttachStatus],
    running: bool,
) -> Vec<AttachmentInfo> {
    spec.attach.iter().map(|a| attachment_info(a, status.iter().find(|s| s.id == a.id), running)).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn guest_paths_are_checked() {
        for ok in ["/toby/workspace/x", "/home/dev/src", "/a"] {
            assert!(check_guest_path(ok).is_ok(), "{ok}");
        }
        for bad in ["/", "relative", "/a/../b", "/a/./b", "/a//b", "/a/", ""] {
            assert!(check_guest_path(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn default_mount_point_uses_the_directory_name() {
        assert_eq!(default_guest_path(Path::new("/home/u/src/toby")).unwrap(), "/toby/workspace/toby");
        assert!(default_guest_path(Path::new("/")).is_err());
    }

    #[test]
    fn default_resources_are_bounded() {
        let r = default_resources();
        assert!((2..=8).contains(&r.cpus));
        assert!(machine::parse_size(&r.memory).unwrap() <= 8 << 30);
    }
}
