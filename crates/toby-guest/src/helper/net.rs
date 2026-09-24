//! `net-up`: static guest networking without depending on the image's tools.

use std::ffi::CString;
use std::io;
use std::net::Ipv4Addr;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::path::{Path, PathBuf};

/// Options of `net-up`.
#[derive(Debug, Clone)]
pub struct NetUp {
    pub addr: Ipv4Addr,
    pub prefix: u8,
    pub gateway: Ipv4Addr,
    pub dns: Ipv4Addr,
    pub hostname: Option<String>,
}

#[repr(C)]
#[derive(Clone, Copy)]
struct SockaddrIn {
    family: u16,
    port: u16,
    addr: [u8; 4],
    zero: [u8; 8],
}

impl SockaddrIn {
    fn new(a: Ipv4Addr) -> Self {
        SockaddrIn {
            family: libc::AF_INET as u16,
            port: 0,
            addr: a.octets(),
            zero: [0; 8],
        }
    }
}

/// `struct ifreq` with the address or flags member.
#[repr(C)]
struct IfReq {
    name: [u8; 16],
    data: IfReqData,
}

#[repr(C)]
union IfReqData {
    addr: SockaddrIn,
    flags: i16,
    pad: [u8; 24],
}

/// `struct rtentry` (identical for glibc, musl and the kernel on LP64).
#[repr(C)]
struct RtEntry {
    pad1: libc::c_ulong,
    dst: SockaddrIn,
    gateway: SockaddrIn,
    genmask: SockaddrIn,
    flags: libc::c_ushort,
    pad2: libc::c_short,
    pad3: libc::c_ulong,
    pad4: *mut libc::c_void,
    metric: libc::c_short,
    dev: *mut libc::c_char,
    mtu: libc::c_ulong,
    window: libc::c_ulong,
    irtt: libc::c_ushort,
}

const SIOCSIFADDR: libc::c_ulong = 0x8916;
const SIOCSIFNETMASK: libc::c_ulong = 0x891c;
const SIOCGIFFLAGS: libc::c_ulong = 0x8913;
const SIOCSIFFLAGS: libc::c_ulong = 0x8914;
const SIOCADDRT: libc::c_ulong = 0x890b;
const RTF_UP: libc::c_ushort = 0x1;
const RTF_GATEWAY: libc::c_ushort = 0x2;

fn ifreq(name: &str) -> io::Result<IfReq> {
    let bytes = name.as_bytes();
    if bytes.len() >= 16 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "interface name too long",
        ));
    }
    let mut n = [0u8; 16];
    n[..bytes.len()].copy_from_slice(bytes);
    Ok(IfReq {
        name: n,
        data: IfReqData { pad: [0; 24] },
    })
}

fn socket() -> io::Result<OwnedFd> {
    // SAFETY: socket(2) returns a new descriptor or -1.
    let fd = unsafe { libc::socket(libc::AF_INET, libc::SOCK_DGRAM | libc::SOCK_CLOEXEC, 0) };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: `fd` is a valid descriptor we own.
    Ok(unsafe { OwnedFd::from_raw_fd(fd) })
}

fn ioctl<T>(fd: &OwnedFd, req: libc::c_ulong, arg: &mut T) -> io::Result<()> {
    // SAFETY: `arg` points to a correctly laid out structure for `req`.
    let r = unsafe { libc::ioctl(fd.as_raw_fd(), req as _, arg as *mut T) };
    if r < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

fn set_up(fd: &OwnedFd, name: &str) -> io::Result<()> {
    let mut req = ifreq(name)?;
    ioctl(fd, SIOCGIFFLAGS, &mut req)?;
    // SAFETY: SIOCGIFFLAGS filled the flags member.
    let flags = unsafe { req.data.flags };
    req.data.flags = flags | libc::IFF_UP as i16;
    ioctl(fd, SIOCSIFFLAGS, &mut req)
}

fn netmask(prefix: u8) -> Ipv4Addr {
    let bits = if prefix == 0 {
        0
    } else {
        u32::MAX << (32 - prefix.min(32) as u32)
    };
    Ipv4Addr::from(bits)
}

/// The first interface driven by virtio-net; its name depends on the PCI slot.
pub fn virtio_interface(sys_class_net: &Path) -> io::Result<String> {
    let mut names: Vec<String> = std::fs::read_dir(sys_class_net)?
        .flatten()
        .filter(|e| {
            std::fs::read_link(e.path().join("device/driver"))
                .is_ok_and(|d| d.file_name().is_some_and(|n| n == "virtio_net"))
        })
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    names.sort();
    names
        .into_iter()
        .next()
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "no virtio network interface"))
}

/// Where a bind mount over `/etc/resolv.conf` must go: the file itself, or
/// the target of a symlink (created if missing).
fn resolv_target(etc_resolv: &Path) -> io::Result<PathBuf> {
    let Ok(meta) = std::fs::symlink_metadata(etc_resolv) else {
        std::fs::write(etc_resolv, "")?;
        return Ok(etc_resolv.to_path_buf());
    };
    if !meta.file_type().is_symlink() {
        return Ok(etc_resolv.to_path_buf());
    }
    let link = std::fs::read_link(etc_resolv)?;
    let target = if link.is_absolute() {
        link
    } else {
        etc_resolv.parent().unwrap_or(Path::new("/")).join(link)
    };
    if !target.exists() {
        if let Some(p) = target.parent() {
            std::fs::create_dir_all(p)?;
        }
        std::fs::write(&target, "")?;
    }
    Ok(target)
}

pub fn bind_mount(src: &Path, dst: &Path) -> io::Result<()> {
    nix::mount::mount(
        Some(src),
        dst,
        None::<&str>,
        nix::mount::MsFlags::MS_BIND,
        None::<&str>,
    )
    .map_err(io::Error::from)
}

fn is_mountpoint(path: &Path) -> bool {
    std::fs::read_to_string("/proc/self/mountinfo")
        .map(|m| m.lines().any(|l| l.split(' ').nth(4) == path.to_str()))
        .unwrap_or(false)
}

/// Configures the network interface, route, hostname and resolver.
pub fn net_up(opts: &NetUp, run_dir: &Path) -> io::Result<()> {
    let fd = socket()?;
    set_up(&fd, "lo")?;

    let iface = virtio_interface(Path::new("/sys/class/net"))?;
    let mut req = ifreq(&iface)?;
    req.data.addr = SockaddrIn::new(opts.addr);
    ioctl(&fd, SIOCSIFADDR, &mut req)?;
    let mut req = ifreq(&iface)?;
    req.data.addr = SockaddrIn::new(netmask(opts.prefix));
    ioctl(&fd, SIOCSIFNETMASK, &mut req)?;
    set_up(&fd, &iface)?;

    let dev = CString::new(iface.clone()).map_err(io::Error::other)?;
    let mut rt = RtEntry {
        pad1: 0,
        dst: SockaddrIn::new(Ipv4Addr::UNSPECIFIED),
        gateway: SockaddrIn::new(opts.gateway),
        genmask: SockaddrIn::new(Ipv4Addr::UNSPECIFIED),
        flags: RTF_UP | RTF_GATEWAY,
        pad2: 0,
        pad3: 0,
        pad4: std::ptr::null_mut(),
        metric: 0,
        dev: dev.as_ptr() as *mut libc::c_char,
        mtu: 0,
        window: 0,
        irtt: 0,
    };
    match ioctl(&fd, SIOCADDRT, &mut rt) {
        Err(e) if e.raw_os_error() != Some(libc::EEXIST) => return Err(e),
        _ => {}
    }

    if let Some(name) = &opts.hostname {
        nix::unistd::sethostname(name).map_err(io::Error::from)?;
    }

    // Name resolution through passt's forwarder.
    std::fs::create_dir_all(run_dir)?;
    let resolv = run_dir.join("resolv.conf");
    std::fs::write(&resolv, format!("nameserver {}\n", opts.dns))?;
    let target = resolv_target(Path::new("/etc/resolv.conf"))?;
    if !is_mountpoint(&target) {
        bind_mount(&resolv, &target)?;
    }
    if Path::new("/run/systemd/resolve").is_dir() {
        // systemd-resolved answers on 127.0.0.53 and needs the server too.
        let _ = std::process::Command::new("resolvectl")
            .args(["dns", &iface, &opts.dns.to_string()])
            .status();
        let _ = std::process::Command::new("resolvectl")
            .args(["domain", &iface, "~."])
            .status();
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn netmasks() {
        assert_eq!(netmask(24), Ipv4Addr::new(255, 255, 255, 0));
        assert_eq!(netmask(32), Ipv4Addr::new(255, 255, 255, 255));
        assert_eq!(netmask(0), Ipv4Addr::new(0, 0, 0, 0));
    }

    #[test]
    fn struct_layouts_match_the_kernel() {
        assert_eq!(std::mem::size_of::<IfReq>(), 40);
        assert_eq!(std::mem::size_of::<RtEntry>(), 120);
    }

    #[test]
    fn finds_the_virtio_interface_by_driver() {
        let dir = tempfile::tempdir().unwrap();
        let sys = dir.path().join("sys");
        for (name, driver) in [
            ("lo", None),
            ("ens5", Some("virtio_net")),
            ("docker0", Some("bridge")),
        ] {
            let dev = sys.join(name).join("device");
            std::fs::create_dir_all(&dev).unwrap();
            if let Some(d) = driver {
                let target = dir.path().join("drivers").join(d);
                std::fs::create_dir_all(&target).unwrap();
                std::os::unix::fs::symlink(&target, dev.join("driver")).unwrap();
            }
        }
        assert_eq!(virtio_interface(&sys).unwrap(), "ens5");
    }

    #[test]
    fn resolv_conf_symlink_targets_are_created() {
        let dir = tempfile::tempdir().unwrap();
        let etc = dir.path().join("etc");
        std::fs::create_dir_all(&etc).unwrap();
        std::os::unix::fs::symlink("../run/resolve/stub.conf", etc.join("resolv.conf")).unwrap();
        let t = resolv_target(&etc.join("resolv.conf")).unwrap();
        assert!(t.ends_with("run/resolve/stub.conf"));
        assert!(t.exists());
    }
}
