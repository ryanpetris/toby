//! `toby internal …`: the per-machine host processes started by the service
//! manager (plan §3.3, §12).

use std::ffi::OsString;
use std::net::Ipv4Addr;
use std::os::unix::fs::DirBuilderExt;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use anyhow::{Context, bail};
use toby_config::global::GlobalConfig;
use toby_config::machine::{MachineSpec, RootSpec};
use toby_config::paths::{MachineRuntime, Paths};
use toby_engine::cloud_hypervisor;
use toby_engine::{Arch, BootSpec, DiskSpec, FileShareSpec, VmSpec};

/// Guest addressing used with passt (plan §11.1).
pub const GUEST_ADDR: &str = "10.0.2.15";
pub const GUEST_PREFIX: u8 = 24;
pub const GATEWAY: &str = "10.0.2.2";
pub const DNS_FORWARDER: &str = "10.0.2.3";

/// Configuration, paths and desired state of one machine.
pub struct Host {
    pub config: GlobalConfig,
    pub paths: Paths,
    pub spec: MachineSpec,
    pub runtime: MachineRuntime,
}

pub fn load_config() -> anyhow::Result<(GlobalConfig, Paths)> {
    let home = toby_config::paths::home_dir()?;
    let config = GlobalConfig::load(&home.join(".config/toby/config.toml"))?;
    let paths = Paths::resolve(&config)?;
    toby_config::paths::ensure_private_dir(&paths.runtime)?;
    Ok((config, paths))
}

impl Host {
    pub fn load(machine: &str) -> anyhow::Result<Host> {
        let (config, paths) = load_config()?;
        let desired = paths.machine_desired(machine);
        let spec = MachineSpec::load(&desired).with_context(|| format!("reading {}", desired.display()))?;
        if spec.id != machine {
            bail!("{} describes machine {}", desired.display(), spec.id);
        }
        let runtime = paths.machine_runtime(machine);
        std::fs::DirBuilder::new().recursive(true).mode(0o700).create(&runtime.dir)?;
        Ok(Host { config, paths, spec, runtime })
    }
}

/// The runtime version new guest processes use: the target of
/// `<versions>/current`, or this binary's version.
pub fn current_runtime_version(versions: &Path) -> String {
    std::fs::read_link(versions.join("current"))
        .ok()
        .and_then(|t| t.file_name().map(|n| n.to_string_lossy().into_owned()))
        .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_string())
}

/// `toby internal fs`: serves the machine's virtio-fs tree.
pub fn fs(machine: &str) -> anyhow::Result<()> {
    let host = Host::load(machine)?;
    toby_fs::raise_fd_limit().context("raising the open file limit")?;

    // Files owned by the host user appear owned by the home's user, or by
    // root in machines without a home (builders).
    let guest_uid = match &host.spec.home {
        Some(name) => toby_store::Store::new(host.paths.clone()).home(name)?.uid,
        None => 0,
    };
    let squash = toby_vfs::Squash {
        host_uid: nix::unistd::getuid().as_raw(),
        host_gid: nix::unistd::getgid().as_raw(),
        guest_uid,
        guest_gid: guest_uid,
    };
    let tree = Arc::new(toby_vfs::Tree::new(squash)?);
    let ro = |source: PathBuf| toby_vfs::MountSpec { source, read_only: true };

    let versions = host.config.programs.versions();
    tree.mount("/versions", ro(versions.clone()))
        .with_context(|| format!("serving {}", versions.display()))?;
    if !matches!(host.spec.root, RootSpec::Named(_)) {
        let share = host.config.programs.share();
        for (at, dir) in
            [("/mkosi", "mkosi"), ("/images/default", "images/default"), ("/dracut/99toby", "dracut/99toby")]
        {
            let source = share.join(dir);
            tree.mount(at, ro(source.clone())).with_context(|| format!("serving {}", source.display()))?;
        }
    }
    for a in &host.spec.attach {
        let spec = toby_vfs::MountSpec { source: PathBuf::from(&a.host), read_only: a.read_only };
        tree.mount(&toby_fs::control::attachment_path(&a.id)?, spec)
            .with_context(|| format!("attaching {}", a.host))?;
    }

    toby_fs::control::spawn(&host.runtime.fs_control_sock(), tree.clone())?;
    toby_fs::serve(&host.runtime.fs_sock(), tree.filesystem(), toby_svc::notify::ready)?;
    Ok(())
}

/// The first IPv4 nameserver in a resolv.conf.
pub fn first_ipv4_nameserver(resolv_conf: &str) -> Option<Ipv4Addr> {
    resolv_conf.lines().find_map(|line| {
        let mut words = line.split_whitespace();
        (words.next() == Some("nameserver")).then(|| words.next()?.parse().ok()).flatten()
    })
}

fn find_in_path(name: &str) -> Option<PathBuf> {
    std::env::var_os("PATH")?.to_str()?.split(':').map(|d| Path::new(d).join(name)).find(|p| p.is_file())
}

/// `toby internal net`: replaces itself with passt for the machine.
pub fn net(machine: &str) -> anyhow::Result<()> {
    let host = Host::load(machine)?;
    let passt = match &host.config.programs.passt {
        Some(p) => p.clone(),
        None => find_in_path("passt").context("passt is not installed")?,
    };
    let dns_host = match host.config.network.dns_host {
        Some(a) => a,
        None => {
            let conf = std::fs::read_to_string("/etc/resolv.conf").context("reading /etc/resolv.conf")?;
            first_ipv4_nameserver(&conf)
                .context("the host has no IPv4 nameserver; set network.dns_host in the Toby configuration")?
        }
    };

    let sock = host.runtime.net_sock();
    let _ = std::fs::remove_file(&sock);
    let _ = std::fs::remove_file(sock.with_extension("sock.repair"));

    let prefix = GUEST_PREFIX.to_string();
    let dns_host = dns_host.to_string();
    let mut args: Vec<OsString> =
        vec!["--vhost-user".into(), "-s".into(), sock.into(), "-f".into(), "-q".into()];
    args.extend(
        [
            "-4",
            "-a",
            GUEST_ADDR,
            "-n",
            &prefix,
            "-g",
            GATEWAY,
            "--dns-forward",
            DNS_FORWARDER,
            "-D",
            DNS_FORWARDER,
            "--dns-host",
            &dns_host,
            "--no-map-gw",
            "-t",
            "none",
            "-u",
            "none",
        ]
        .map(OsString::from),
    );
    Err(std::process::Command::new(&passt).args(args).exec())
        .with_context(|| format!("starting {}", passt.display()))
}

fn machine_config(host: &Host) -> anyhow::Result<toby_machine::Config> {
    Ok(toby_machine::Config {
        id: host.spec.id.clone(),
        generation: host.spec.generation,
        desired: host.paths.machine_desired(&host.spec.id),
        runtime: host.runtime.clone(),
        runtime_version: current_runtime_version(&host.config.programs.versions()),
        boot_helpers: boot_helpers(host)?,
    })
}

/// `toby internal machine`: the machine's host process.
pub fn machine(machine: &str) -> anyhow::Result<()> {
    let host = Host::load(machine)?;
    let config = machine_config(&host)?;
    let rt = tokio::runtime::Builder::new_multi_thread().enable_all().build()?;
    rt.block_on(toby_machine::Machine::new(config, toby_svc::notify::ready).run())?;
    Ok(())
}

/// `toby internal machine --supervise`: runs the machine's host process and
/// supervises its file share, network and VMM (the direct back end, plan
/// §12.3). A file share or network exit stops the VM; a VM exit stops the
/// rest; SIGTERM or SIGINT powers the guest off first.
pub fn supervise(machine: &str) -> anyhow::Result<()> {
    use tokio::process::Command;
    use tokio::signal::unix::{SignalKind, signal};

    let host = Host::load(machine)?;
    let exe = std::env::current_exe()?;
    let config = machine_config(&host)?;
    // Held (shared) until the machine has stopped, so nothing removes its
    // state while it runs, even if the process that started it is gone.
    let _state = nix::fcntl::Flock::lock(
        std::fs::File::open(host.paths.machine_state_dir(machine))?,
        nix::fcntl::FlockArg::LockSharedNonblock,
    )
    .map_err(|(_, e)| e)?;
    let rt = tokio::runtime::Builder::new_multi_thread().enable_all().build()?;
    let runtime = host.runtime.clone();

    rt.block_on(async move {
        let log = |name: &str| -> anyhow::Result<std::process::Stdio> {
            let f = std::fs::File::create(runtime.dir.join(format!("{name}.log")))?;
            Ok(f.into())
        };
        let part = |name: &str| -> anyhow::Result<tokio::process::Child> {
            let mut c = Command::new(&exe);
            c.args(["internal", name, "--machine", machine])
                .stdin(std::process::Stdio::null())
                .stdout(log(name)?)
                .stderr(log(&format!("{name}.err"))?)
                .kill_on_drop(true);
            // The parts must not outlive a supervisor that is killed.
            // SAFETY: prctl is async-signal-safe.
            unsafe {
                c.pre_exec(|| {
                    nix::sys::prctl::set_pdeathsig(nix::sys::signal::Signal::SIGTERM)
                        .map_err(std::io::Error::from)
                });
            }
            Ok(c.spawn()?)
        };

        // State from an earlier run must not look current.
        let _ = std::fs::remove_file(runtime.status());
        let _ = std::fs::remove_file(runtime.fs_sock());
        let mut fs = part("fs")?;
        let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(10);
        while !runtime.fs_sock().exists() {
            if let Ok(Some(status)) = fs.try_wait() {
                bail!(
                    "the file share exited during start ({status}); see {}",
                    runtime.dir.join("fs.err.log").display()
                );
            }
            if tokio::time::Instant::now() >= deadline {
                bail!("the file share did not start");
            }
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
        let mut net = part("net")?;
        let mut vm = part("vm")?;

        let server = tokio::spawn(toby_machine::Machine::new(config, || {}).run());
        let api = cloud_hypervisor::Api::new(runtime.ch_api());
        let mut term = signal(SignalKind::terminate())?;
        let mut int = signal(SignalKind::interrupt())?;
        let mut hup = signal(SignalKind::hangup())?;

        tokio::select! {
            _ = vm.wait() => {}
            _ = fs.wait() => { let _ = api.shutdown().await; let _ = api.shutdown_vmm().await; }
            _ = net.wait() => { let _ = api.shutdown().await; let _ = api.shutdown_vmm().await; }
            _ = term.recv() => toby_machine::power_off(api.clone()).await,
            _ = int.recv() => toby_machine::power_off(api.clone()).await,
            _ = hup.recv() => toby_machine::power_off(api.clone()).await,
        }
        if tokio::time::timeout(std::time::Duration::from_secs(45), vm.wait()).await.is_err() {
            let _ = vm.kill().await;
        }
        let _ = fs.kill().await;
        let _ = net.kill().await;
        server.abort();
        anyhow::Ok(())
    })?;

    if host.spec.has_layer() {
        let _ = std::fs::remove_file(host.paths.layer_disk(machine));
    }
    for f in [
        host.runtime.control_sock(),
        host.runtime.session_sock(),
        host.runtime.fs_sock(),
        host.runtime.net_sock(),
    ] {
        let _ = std::fs::remove_file(f);
    }
    Ok(())
}

/// Guest units the bootstrap builder receives as systemd credentials: its
/// stock cloud image has no Toby initramfs to write them.
const FS_MOUNT_UNIT: &str = include_str!("../../../packaging/dracut/99toby/run-toby-fs.mount");
const RELAY_UNIT: &str = include_str!("../../../packaging/dracut/99toby/toby-relay.service.in");

fn base64(data: &[u8]) -> String {
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let b = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
        let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
        for (i, shift) in [18, 12, 6, 0].into_iter().enumerate() {
            if i <= chunk.len() {
                out.push(T[((n >> shift) & 63) as usize] as char);
            } else {
                out.push('=');
            }
        }
    }
    out
}

/// SMBIOS OEM strings that give a stock image Toby's guest units.
pub fn credential_units(version: &str) -> Vec<String> {
    let cred = |name: &str, content: &str| {
        format!("io.systemd.credential.binary:{name}={}", base64(content.as_bytes()))
    };
    vec![
        cred("systemd.extra-unit.run-toby-fs.mount", FS_MOUNT_UNIT),
        cred("systemd.extra-unit.toby-relay.service", &RELAY_UNIT.replace("@VERSION@", version)),
        cred("systemd.unit-dropin.multi-user.target~toby", "[Unit]\nWants=toby-relay.service\n"),
    ]
}

/// The disk a throwaway root layer is created over, if the machine has one.
pub fn layer_base(host: &Host) -> anyhow::Result<Option<PathBuf>> {
    Ok(match &host.spec.root {
        RootSpec::Named(name) if host.spec.ephemeral => Some(host.paths.root_disk(name)),
        RootSpec::Named(_) => None,
        RootSpec::Image { image } => Some(host.paths.image_dir(image).join("disk.qcow2")),
        RootSpec::CloudImage { cloud_image } => Some(cloud_image.clone()),
    })
}

/// The boot helpers for the machine (plan §9.6), as guest command lines.
pub fn boot_helpers(host: &Host) -> anyhow::Result<Vec<Vec<String>>> {
    let version = current_runtime_version(&host.config.programs.versions());
    let toby = format!("/run/toby/fs/versions/{version}/toby");
    let helper = |args: &[&str]| -> Vec<String> {
        [toby.as_str(), "guest", "helper"].iter().chain(args).map(|s| s.to_string()).collect()
    };
    let store = toby_store::Store::new(host.paths.clone());

    let hostname = host.spec.home.clone().unwrap_or_else(|| "toby".into());
    let addr = format!("{GUEST_ADDR}/{GUEST_PREFIX}");
    let mut out = vec![helper(&[
        "net-up",
        "--addr",
        &addr,
        "--gw",
        GATEWAY,
        "--dns",
        DNS_FORWARDER,
        "--hostname",
        &hostname,
    ])];
    if let Some(name) = &host.spec.home {
        let home = store.home(name)?;
        if !home.formatted {
            bail!("home {name} has not been formatted");
        }
        let uid = home.uid.to_string();
        let mut setup = vec!["user-setup", "--name", &home.username, "--uid", &uid];
        if let Some(shell) = &home.shell {
            setup.extend(["--shell", shell]);
        }
        if home.sudo {
            setup.push("--sudo");
        }
        out.push(helper(&setup));
        let at = format!("/home/{}", home.username);
        out.push(helper(&[
            "home-mount",
            "--device",
            "/dev/disk/by-id/virtio-home",
            "--at",
            &at,
            "--uid",
            &uid,
            "--gid",
            &uid,
        ]));
    }
    out.push(helper(&["links", "--target", "/run/toby/fs/versions/current/toby"]));
    Ok(out)
}

/// Builds the machine's VM description from its desired state.
pub fn vm_spec(host: &Host) -> anyhow::Result<VmSpec> {
    let version = current_runtime_version(&host.config.programs.versions());
    let mut oem_strings = Vec::new();
    // A root boots the kernel of the image it was created from unless the
    // machine names another.
    let image = match (&host.spec.root, &host.spec.boot.image) {
        (_, Some(image)) => Some(image.clone()),
        (RootSpec::Named(name), None) => Some(toby_store::Store::new(host.paths.clone()).root(name)?.image),
        (RootSpec::Image { image }, None) => Some(image.clone()),
        (RootSpec::CloudImage { .. }, None) => None,
    };
    let boot = match (&host.spec.root, &image) {
        (RootSpec::CloudImage { .. }, _) => {
            oem_strings = credential_units(&version);
            BootSpec::Firmware { path: host.config.programs.firmware() }
        }
        (_, Some(image)) => {
            let dir = host.paths.image_dir(image);
            BootSpec::Kernel {
                kernel: dir.join("vmlinuz"),
                initramfs: dir.join("initramfs.img"),
                cmdline: format!(
                    "root=/dev/disk/by-id/virtio-root rw console=hvc0 quiet toby.machine={} toby.version={version}",
                    host.spec.id
                ),
            }
        }
        (_, None) => unreachable!("only cloud images boot without an image"),
    };

    let root = match (&host.spec.root, layer_base(host)?) {
        (_, Some(_)) => host.paths.layer_disk(&host.spec.id),
        (RootSpec::Named(name), None) => host.paths.root_disk(name),
        _ => unreachable!("only named roots have no layer"),
    };
    let mut disks =
        vec![DiskSpec { path: root, serial: "root".into(), read_only: false, backing_allowed: true }];
    if let Some(home) = &host.spec.home {
        disks.push(DiskSpec {
            path: host.paths.home_disk(home),
            serial: "home".into(),
            read_only: false,
            backing_allowed: false,
        });
    }
    for d in &host.spec.disk {
        disks.push(DiskSpec {
            path: d.path.clone(),
            serial: d.serial.clone(),
            read_only: d.read_only,
            backing_allowed: false,
        });
    }

    Ok(VmSpec {
        arch: Arch::host(),
        cpus: host.spec.resources.cpus,
        memory_bytes: host.spec.memory_bytes()?,
        boot,
        disks,
        share: Some(FileShareSpec { socket: host.runtime.fs_sock(), tag: toby_fs::TAG.into() }),
        net_socket: Some(host.runtime.net_sock()),
        vsock_socket: host.runtime.vsock(),
        console_log: host.runtime.console_log(),
        api_socket: host.runtime.ch_api(),
        oem_strings,
    })
}

/// Creates the machine's throwaway root layer afresh.
pub fn create_layer(host: &Host) -> anyhow::Result<()> {
    let Some(base) = layer_base(host)? else {
        return Ok(());
    };
    if !base.is_file() {
        bail!("{} is missing", base.display());
    }
    let layer = host.paths.layer_disk(&host.spec.id);
    let _ = std::fs::remove_file(&layer);
    let rt = tokio::runtime::Builder::new_current_thread().build()?;
    rt.block_on(toby_store::qcow2::create(&layer, toby_store::store::IMAGE_SIZE, Some(&base)))
        .with_context(|| format!("creating {}", layer.display()))?;
    Ok(())
}

/// `toby internal vm`: replaces itself with Cloud Hypervisor for the machine.
/// Locks the machine's disks: its root, home and writable extra disks
/// exclusively, an image or cloud image it layers over shared (plan §6.2).
fn disk_locks(host: &Host) -> anyhow::Result<Vec<toby_store::store::DiskLock>> {
    use toby_store::store::{lock_disk, lock_disk_shared};
    let busy = |what: String| {
        move |e: std::io::Error| {
            if e.kind() == std::io::ErrorKind::ResourceBusy {
                anyhow::anyhow!("{what} is in use by another machine")
            } else {
                anyhow::Error::from(e).context(format!("locking {what}"))
            }
        }
    };
    let mut locks = Vec::new();
    match &host.spec.root {
        RootSpec::Named(name) => {
            locks.push(lock_disk(&host.paths.root_disk(name)).map_err(busy(format!("root {name}")))?)
        }
        RootSpec::Image { image } => locks.push(
            lock_disk_shared(&host.paths.image_dir(image).join("disk.qcow2"))
                .map_err(busy(format!("image {image}")))?,
        ),
        RootSpec::CloudImage { cloud_image } => {
            locks.push(lock_disk_shared(cloud_image).map_err(busy(cloud_image.display().to_string()))?)
        }
    }
    if let Some(home) = &host.spec.home {
        locks.push(lock_disk(&host.paths.home_disk(home)).map_err(busy(format!("home {home}")))?);
    }
    // Builder disks: the cache and the output (or a home being formatted).
    for d in &host.spec.disk {
        let lock = if d.read_only { lock_disk_shared(&d.path) } else { lock_disk(&d.path) };
        locks.push(lock.map_err(busy(d.path.display().to_string()))?);
    }
    Ok(locks)
}

pub fn vm(machine: &str) -> anyhow::Result<()> {
    use nix::fcntl::{FcntlArg, FdFlag, fcntl};

    let host = Host::load(machine)?;
    // Cloud Hypervisor inherits the locks, so they last exactly as long as
    // the VM runs.
    let locks = disk_locks(&host)?;
    for l in &locks {
        fcntl(l.file(), FcntlArg::F_SETFD(FdFlag::empty()))?;
    }
    create_layer(&host)?;
    let spec = vm_spec(&host)?;
    let mut files: Vec<&Path> = spec.disks.iter().map(|d| d.path.as_path()).collect();
    match &spec.boot {
        BootSpec::Kernel { kernel, initramfs, .. } => {
            // An image's boot files are the store's own files, never links.
            for f in [kernel, initramfs] {
                if !std::fs::symlink_metadata(f).is_ok_and(|m| m.is_file()) {
                    bail!("{} is missing", f.display());
                }
            }
        }
        BootSpec::Firmware { path } => files.push(path),
    }
    for f in files {
        if !f.is_file() {
            bail!("{} is missing", f.display());
        }
    }
    let _ = std::fs::remove_file(host.runtime.ch_api());
    let _ = std::fs::remove_file(host.runtime.vsock());

    let ch = host.config.programs.cloud_hypervisor();
    let args = cloud_hypervisor::args(&spec)?;
    Err(std::process::Command::new(&ch).args(args).exec())
        .with_context(|| format!("starting {}", ch.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn picks_the_first_ipv4_nameserver() {
        let conf = "# generated\nsearch example\nnameserver fe80::1%eth0\nnameserver 2001:db8::53\nnameserver 192.0.2.53\nnameserver 192.0.2.54\n";
        assert_eq!(first_ipv4_nameserver(conf), Some("192.0.2.53".parse().unwrap()));
        assert_eq!(first_ipv4_nameserver("nameserver 127.0.0.53\n"), Some("127.0.0.53".parse().unwrap()));
        assert_eq!(first_ipv4_nameserver("nameserver ::1\n"), None);
        assert_eq!(first_ipv4_nameserver(""), None);
    }

    #[test]
    fn base64_matches_the_standard_alphabet() {
        assert_eq!(base64(b""), "");
        assert_eq!(base64(b"f"), "Zg==");
        assert_eq!(base64(b"fo"), "Zm8=");
        assert_eq!(base64(b"foo"), "Zm9v");
        assert_eq!(base64(b"[Unit]\nWants=x\n"), "W1VuaXRdCldhbnRzPXgK");
    }

    #[test]
    fn credential_units_carry_the_version() {
        let units = credential_units("1.2.3");
        assert_eq!(units.len(), 3);
        assert!(units.iter().all(|u| !u.contains(',')));
        let relay = units[1].split_once('=').unwrap().1;
        assert!(units[1].starts_with("io.systemd.credential.binary:systemd.extra-unit.toby-relay.service="));
        assert!(relay.len() > 40);
    }

    #[test]
    fn runtime_version_follows_current() {
        let dir = tempfile::tempdir().unwrap();
        assert_eq!(current_runtime_version(dir.path()), env!("CARGO_PKG_VERSION"));
        std::fs::create_dir(dir.path().join("1.2.3")).unwrap();
        std::os::unix::fs::symlink("1.2.3", dir.path().join("current")).unwrap();
        assert_eq!(current_runtime_version(dir.path()), "1.2.3");
    }
}
