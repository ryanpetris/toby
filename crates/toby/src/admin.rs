//! `toby machine`, `toby daemon`, `toby linger` and `toby doctor`.

use std::os::unix::process::CommandExt;
use std::path::Path;
use std::process::ExitCode;

use anyhow::{Context, bail};
use toby_config::global::Backend;

use crate::api::{Api, segment};
use crate::cli::{DaemonCommand, MachineCommand, OnOff};
use crate::internal::load_config;
use crate::table::{duration, print};

pub async fn machine(cmd: MachineCommand) -> anyhow::Result<ExitCode> {
    match cmd {
        MachineCommand::Ls => {
            let api = Api::connect().await?;
            let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
            let rows = machines
                .into_iter()
                .map(|m| {
                    let running = m.state != "stopped";
                    let time = |t: Option<u64>| t.map(duration).unwrap_or_default();
                    [
                        m.id,
                        m.home.unwrap_or_default(),
                        m.root,
                        m.image.unwrap_or_default(),
                        m.state,
                        if running { m.sessions.to_string() } else { String::new() },
                        m.attachments.len().to_string(),
                        time(m.uptime_secs),
                        time(m.idle_secs),
                    ]
                })
                .collect();
            print(
                ["MACHINE", "HOME", "ROOT", "IMAGE", "STATE", "SESSIONS", "MOUNTS", "UPTIME", "IDLE"],
                rows,
            );
        }
        MachineCommand::Stop { id, all } => {
            let api = Api::connect().await?;
            let ids = match id {
                Some(id) => vec![id],
                None => {
                    debug_assert!(all);
                    let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
                    machines.into_iter().filter(|m| m.state != "stopped").map(|m| m.id).collect()
                }
            };
            for id in ids {
                let () = api.post(&format!("/v1/machines/{}/stop", segment(&id)), &()).await?;
            }
        }
        MachineCommand::Logs { id, follow } => {
            let (config, paths) = load_config()?;
            let units = ["vm", "machine", "fs", "net"].map(|p| format!("toby-{p}@{id}"));
            let err = match config.daemon.backend {
                Backend::SystemdUser => {
                    let mut cmd = std::process::Command::new("journalctl");
                    cmd.args(["--user", "--no-pager", "-o", "short-iso"]);
                    for u in &units {
                        cmd.args(["-u", u]);
                    }
                    if follow {
                        cmd.arg("-f");
                    }
                    cmd.exec()
                }
                Backend::Direct => {
                    let logs = paths.state.join("logs");
                    let files: Vec<_> = std::fs::read_dir(&logs)
                        .map(|d| d.flatten().map(|e| e.path()).collect())
                        .unwrap_or_default();
                    let mut files: Vec<_> = files
                        .into_iter()
                        .filter(|p| {
                            p.file_name().and_then(|n| n.to_str()).is_some_and(|n| {
                                units.iter().any(|u| n.starts_with(&format!("{u}."))) && n.ends_with(".log")
                            })
                        })
                        .collect();
                    if files.is_empty() {
                        bail!("machine {id} has no logs");
                    }
                    files.sort();
                    let mut cmd = std::process::Command::new("tail");
                    cmd.args(["-n", "100"]);
                    if follow {
                        cmd.arg("-F");
                    }
                    cmd.args(files).exec()
                }
            };
            return Err(err).context("showing the logs");
        }
    }
    Ok(ExitCode::SUCCESS)
}

/// Whether logind ends a user's processes at logout, as logind reports it.
async fn kill_user_processes() -> Option<bool> {
    toby_svc::systemd::kill_user_processes().await.ok()
}

pub async fn daemon(cmd: DaemonCommand) -> anyhow::Result<ExitCode> {
    let (config, paths) = load_config()?;
    let backend = config.daemon.backend;
    let sock = paths.runtime.join(toby_api::SOCKET);
    // Connecting to the systemd socket would start the daemon, so ask for
    // its unit's state there.
    let running = match backend {
        Backend::SystemdUser => {
            let systemd = toby_svc::systemd::SystemdUser::connect().await?;
            systemd.state("tobyd.service").await? == "active"
        }
        Backend::Direct => tokio::net::UnixStream::connect(&sock).await.is_ok(),
    };
    match cmd {
        DaemonCommand::Status => {
            if !running {
                println!("tobyd is not running");
                return Ok(ExitCode::from(3));
            }
            let api = Api::connect().await?;
            let info: toby_api::DaemonInfo = api.get("/v1/daemon").await?;
            let machines: Vec<toby_api::MachineInfo> = api.get("/v1/machines").await?;
            println!("tobyd {} (pid {}), back end {}", info.version, info.pid, info.backend);
            println!("machines running: {}", machines.iter().filter(|m| m.state != "stopped").count());
            println!("state: {}\ndata: {}\nruntime: {}", info.state_dir, info.data_dir, info.runtime_dir);
            match backend {
                Backend::SystemdUser => match info.linger {
                    Some(true) => println!("linger: on"),
                    Some(false) => println!("linger: off (machines stop after your last login session ends)"),
                    None => println!("linger: unknown"),
                },
                Backend::Direct => println!(
                    "machines survive logout only when logind has KillUserProcesses=no (it is {})",
                    match kill_user_processes().await {
                        Some(true) => "yes",
                        Some(false) => "no",
                        None => "unknown",
                    }
                ),
            }
        }
        DaemonCommand::Start => {
            Api::connect().await?;
        }
        DaemonCommand::Stop => stop_daemon(backend, running, &sock).await?,
        DaemonCommand::Restart => {
            stop_daemon(backend, running, &sock).await?;
            Api::connect().await?;
        }
        DaemonCommand::Logs { follow } => {
            let err = match backend {
                Backend::SystemdUser => {
                    let mut cmd = std::process::Command::new("journalctl");
                    cmd.args(["--user", "--no-pager", "-o", "short-iso", "-u", "tobyd.service"]);
                    if follow {
                        cmd.arg("-f");
                    }
                    cmd.exec()
                }
                Backend::Direct => {
                    let mut cmd = std::process::Command::new("tail");
                    cmd.args(["-n", "100"]);
                    if follow {
                        cmd.arg("-F");
                    }
                    cmd.arg(paths.state.join("logs/tobyd.log")).exec()
                }
            };
            return Err(err).context("showing the logs");
        }
    }
    Ok(ExitCode::SUCCESS)
}

async fn stop_daemon(backend: Backend, running: bool, sock: &Path) -> anyhow::Result<()> {
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(15);
    let wait = || async {
        if tokio::time::Instant::now() > deadline {
            bail!("tobyd did not stop");
        }
        tokio::time::sleep(std::time::Duration::from_millis(50)).await;
        anyhow::Ok(())
    };
    match backend {
        Backend::SystemdUser => {
            let systemd = toby_svc::systemd::SystemdUser::connect().await?;
            systemd.stop("tobyd.service").await?;
            // The socket stays and starts a new daemon on the next request,
            // so wait until this one is gone.
            while !matches!(
                systemd.state("tobyd.service").await?.as_str(),
                "inactive" | "failed" | "not-found"
            ) {
                wait().await?;
            }
        }
        Backend::Direct => {
            if !running {
                return Ok(());
            }
            let api = Api::connect().await?;
            let info: toby_api::DaemonInfo = api.get("/v1/daemon").await?;
            nix::sys::signal::kill(
                nix::unistd::Pid::from_raw(info.pid as i32),
                nix::sys::signal::Signal::SIGTERM,
            )?;
            while tokio::net::UnixStream::connect(sock).await.is_ok() {
                wait().await?;
            }
        }
    }
    Ok(())
}

pub async fn linger(state: OnOff) -> anyhow::Result<ExitCode> {
    let uid = nix::unistd::getuid().as_raw();
    let on = matches!(state, OnOff::On);
    if let Err(e) = toby_svc::systemd::set_linger(uid, on).await {
        let user = nix::unistd::User::from_uid(nix::unistd::getuid())?.map(|u| u.name).unwrap_or_default();
        let verb = if on { "enable-linger" } else { "disable-linger" };
        bail!("{e}\nrun with administrator rights: loginctl {verb} {user}");
    }
    Ok(ExitCode::SUCCESS)
}

struct Report {
    failed: bool,
}

impl Report {
    fn ok(&mut self, what: &str) {
        println!("ok    {what}");
    }
    fn warn(&mut self, what: &str) {
        println!("warn  {what}");
    }
    fn fail(&mut self, what: &str) {
        println!("FAIL  {what}");
        self.failed = true;
    }
    fn check(&mut self, ok: bool, good: &str, bad: &str) {
        if ok { self.ok(good) } else { self.fail(bad) }
    }
}

fn executable(p: &Path) -> bool {
    nix::unistd::access(p, nix::unistd::AccessFlags::X_OK).is_ok() && p.is_file()
}

/// `toby doctor --gc`: removes installed versions nothing uses (plan §3.3).
pub async fn collect_versions() -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    let r: toby_api::VersionsCollected = api.post("/v1/versions/gc", &()).await?;
    for v in &r.removed {
        println!("removed version {v}");
    }
    println!("in use: {}", r.kept.join(", "));
    for (v, e) in &r.failed {
        eprintln!("toby: version {v} could not be removed: {e}");
    }
    Ok(if r.failed.is_empty() { ExitCode::SUCCESS } else { ExitCode::FAILURE })
}

/// `toby doctor`: checks the host setup (plan §20).
pub async fn doctor() -> anyhow::Result<ExitCode> {
    let (config, paths) = load_config()?;
    let mut r = Report { failed: false };
    let programs = &config.programs;

    let kvm =
        nix::unistd::access("/dev/kvm", nix::unistd::AccessFlags::R_OK | nix::unistd::AccessFlags::W_OK);
    r.check(
        kvm.is_ok(),
        "/dev/kvm is usable",
        "/dev/kvm is missing or not accessible (add yourself to the kvm group)",
    );

    let ch = programs.cloud_hypervisor();
    if executable(&ch) {
        let version = std::process::Command::new(&ch).arg("--version").output().ok();
        let version = version
            .and_then(|o| String::from_utf8_lossy(&o.stdout).lines().next().map(str::to_string))
            .unwrap_or_default();
        r.ok(&format!("cloud-hypervisor: {} ({version})", ch.display()));
    } else {
        r.fail(&format!("cloud-hypervisor is missing: {}", ch.display()));
    }
    let fw = programs.firmware();
    r.check(
        fw.is_file(),
        &format!("firmware: {}", fw.display()),
        &format!("firmware is missing: {}", fw.display()),
    );
    let passt = programs.passt.clone().or_else(|| {
        std::env::var_os("PATH")?
            .to_str()?
            .split(':')
            .map(|d| Path::new(d).join("passt"))
            .find(|p| p.is_file())
    });
    match passt {
        Some(p) if executable(&p) => r.ok(&format!("passt: {}", p.display())),
        _ => r.fail("passt is not installed"),
    }
    let current = programs.versions().join("current/toby");
    r.check(
        current.is_file(),
        &format!("guest binary: {}", current.display()),
        &format!("{} is missing; machines cannot start", current.display()),
    );
    let share = programs.share();
    for dir in ["mkosi", "images/default", "dracut/99toby"] {
        let p = share.join(dir);
        r.check(
            p.is_dir(),
            &format!("bundled {dir}: {}", p.display()),
            &format!("bundled {dir} is missing: {}", p.display()),
        );
    }

    match config.daemon.backend {
        Backend::SystemdUser => {
            match toby_svc::systemd::SystemdUser::connect().await {
                Ok(systemd) => {
                    r.ok("back end systemd-user: user instance reachable");
                    match systemd.state("tobyd.socket").await.as_deref() {
                        Ok("not-found") => r.fail("tobyd.socket is not installed"),
                        Ok(state) => r.ok(&format!("tobyd.socket: {state}")),
                        Err(e) => r.fail(&format!("tobyd.socket: {e}")),
                    }
                    match toby_svc::systemd::linger(nix::unistd::getuid().as_raw()).await {
                    Ok(true) => r.ok("linger is on"),
                    Ok(false) => r.warn("linger is off: machines stop after your last login session ends (toby linger on)"),
                    Err(e) => r.warn(&format!("linger unknown: {e}")),
                }
                }
                Err(e) => r.fail(&format!(
                    "back end systemd-user: {e} (set daemon.backend = \"direct\" to run without it)"
                )),
            }
        }
        Backend::Direct => {
            r.ok("back end direct");
            if kill_user_processes().await == Some(true) {
                r.warn("logind has KillUserProcesses=yes: machines stop when you log out");
            }
        }
    }
    r.ok(&format!("runtime directory: {}", paths.runtime.display()));

    // Substitutions are resolved when used; unresolved ones show up here
    // first (plan §14.2).
    let config_dir = paths.global_config().parent().map(|p| p.to_path_buf()).unwrap_or_default();
    let home = toby_config::paths::home_dir()?;
    let mut refs: Vec<(String, String)> = Vec::new();
    for (name, p) in &config.models {
        refs.extend(p.headers.iter().map(|(k, v)| (format!("models.{name}.headers.{k}"), v.clone())));
    }
    for (name, m) in &config.mcp {
        refs.extend(m.env.iter().map(|(k, v)| (format!("mcp.{name}.env.{k}"), v.clone())));
        refs.extend(m.headers.iter().map(|(k, v)| (format!("mcp.{name}.headers.{k}"), v.clone())));
        refs.extend(m.url.iter().map(|v| (format!("mcp.{name}.url"), v.clone())));
        if let Err(e) = m.check(name) {
            r.fail(&e);
        }
    }
    let mut unresolved = false;
    for (key, value) in refs {
        if let Err(e) = toby_config::subst::resolve(&value, &config_dir, &home) {
            r.fail(&format!("{key}: {e}"));
            unresolved = true;
        }
    }
    if !unresolved {
        r.ok("configuration references resolve");
    }
    match Api::connect().await {
        Ok(api) => {
            let info: toby_api::DaemonInfo = api.get("/v1/daemon").await?;
            r.ok(&format!("tobyd {} is running", info.version));
            let images: Vec<toby_api::ImageInfo> = api.get("/v1/images").await?;
            if images.iter().any(|i| i.current_default) {
                r.ok("the default image is current");
            } else {
                r.warn("the default image is not built or out of date (toby image prepare --default)");
            }
        }
        Err(e) => r.fail(&format!("tobyd: {e:#}")),
    }
    Ok(if r.failed { ExitCode::FAILURE } else { ExitCode::SUCCESS })
}
