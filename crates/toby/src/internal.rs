//! `toby internal …`: the per-machine host processes started by the service
//! manager (plan §3.3, §12).

use std::ffi::OsString;
use std::net::Ipv4Addr;
use std::os::unix::fs::DirBuilderExt;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};

use anyhow::{Context, bail};
use toby_config::global::GlobalConfig;
use toby_config::machine::MachineSpec;
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
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(&runtime.dir)?;
        Ok(Host {
            config,
            paths,
            spec,
            runtime,
        })
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

    let uid = nix::unistd::getuid().as_raw();
    let gid = nix::unistd::getgid().as_raw();
    let squash = toby_vfs::Squash {
        host_uid: uid,
        host_gid: gid,
        guest_uid: uid,
        guest_gid: gid,
    };
    let tree = toby_vfs::Tree::new(squash)?;

    let versions = host.config.programs.versions();
    tree.mount(
        "/versions",
        toby_vfs::MountSpec {
            source: versions.clone(),
            read_only: true,
        },
    )
    .with_context(|| format!("serving {}", versions.display()))?;

    toby_fs::serve(
        &host.runtime.fs_sock(),
        tree.filesystem(),
        toby_svc::notify::ready,
    )?;
    Ok(())
}

/// The first IPv4 nameserver in a resolv.conf.
pub fn first_ipv4_nameserver(resolv_conf: &str) -> Option<Ipv4Addr> {
    resolv_conf.lines().find_map(|line| {
        let mut words = line.split_whitespace();
        (words.next() == Some("nameserver"))
            .then(|| words.next()?.parse().ok())
            .flatten()
    })
}

fn find_in_path(name: &str) -> Option<PathBuf> {
    std::env::var_os("PATH")?
        .to_str()?
        .split(':')
        .map(|d| Path::new(d).join(name))
        .find(|p| p.is_file())
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
    let mut args: Vec<OsString> = vec![
        "--vhost-user".into(),
        "-s".into(),
        sock.into(),
        "-f".into(),
        "-q".into(),
    ];
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

/// `toby internal machine`: the machine's host process.
pub fn machine(machine: &str) -> anyhow::Result<()> {
    let host = Host::load(machine)?;
    let config = toby_machine::Config {
        id: host.spec.id.clone(),
        generation: host.spec.generation,
        runtime: host.runtime.clone(),
        runtime_version: current_runtime_version(&host.config.programs.versions()),
    };
    let rt = tokio::runtime::Builder::new_multi_thread().enable_all().build()?;
    rt.block_on(toby_machine::Machine::new(config, toby_svc::notify::ready).run())?;
    Ok(())
}

/// Builds the machine's VM description from its desired state.
pub fn vm_spec(host: &Host) -> anyhow::Result<VmSpec> {
    let image = host.paths.image_dir(&host.spec.boot.image);
    let version = current_runtime_version(&host.config.programs.versions());
    let cmdline = format!(
        "root=/dev/disk/by-id/virtio-root rw console=hvc0 quiet toby.machine={} toby.version={version}",
        host.spec.id
    );

    let root = if host.spec.ephemeral {
        host.runtime.ephemeral_disk()
    } else {
        host.paths.root_disk(&host.spec.root)
    };
    let mut disks = vec![DiskSpec {
        path: root,
        serial: "root".into(),
        read_only: false,
        backing_allowed: true,
    }];
    let home = host.paths.home_disk(&host.spec.home);
    if home.exists() {
        disks.push(DiskSpec {
            path: home,
            serial: "home".into(),
            read_only: false,
            backing_allowed: false,
        });
    }

    Ok(VmSpec {
        arch: Arch::host(),
        cpus: host.spec.resources.cpus,
        memory_bytes: host.spec.memory_bytes()?,
        boot: BootSpec::Kernel {
            kernel: image.join("vmlinuz"),
            initramfs: image.join("initramfs.img"),
            cmdline,
        },
        disks,
        share: Some(FileShareSpec {
            socket: host.runtime.fs_sock(),
            tag: toby_fs::TAG.into(),
        }),
        net_socket: Some(host.runtime.net_sock()),
        vsock_socket: host.runtime.vsock(),
        console_log: host.runtime.console_log(),
        api_socket: host.runtime.ch_api(),
        oem_strings: Vec::new(),
    })
}

/// `toby internal vm`: replaces itself with Cloud Hypervisor for the machine.
pub fn vm(machine: &str) -> anyhow::Result<()> {
    let host = Host::load(machine)?;
    let spec = vm_spec(&host)?;
    if let BootSpec::Kernel {
        kernel, initramfs, ..
    } = &spec.boot
    {
        for f in [kernel, initramfs] {
            if !f.is_file() {
                bail!("{} is missing", f.display());
            }
        }
    }
    if !spec.disks[0].path.is_file() {
        bail!("root disk {} is missing", spec.disks[0].path.display());
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
        assert_eq!(
            first_ipv4_nameserver("nameserver 127.0.0.53\n"),
            Some("127.0.0.53".parse().unwrap())
        );
        assert_eq!(first_ipv4_nameserver("nameserver ::1\n"), None);
        assert_eq!(first_ipv4_nameserver(""), None);
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
