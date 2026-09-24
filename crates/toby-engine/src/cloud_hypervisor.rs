//! Cloud Hypervisor: command line and API client (plan §7.2).

use std::ffi::OsString;
use std::io;
use std::path::{Path, PathBuf};
use std::time::Duration;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;

use crate::spec::{BootSpec, VmSpec};

/// The guest context ID; hybrid vsock uses per-VM sockets, so it never collides.
pub const GUEST_CID: u32 = 3;

/// Builds the Cloud Hypervisor arguments for `spec`.
pub fn args(spec: &VmSpec) -> io::Result<Vec<OsString>> {
    let mut a: Vec<OsString> = Vec::new();
    let mut push = |k: &str, v: String| {
        a.push(k.into());
        a.push(v.into());
    };

    push("--api-socket", format!("path={}", utf8(&spec.api_socket)?));
    push("--cpus", format!("boot={}", spec.cpus));
    push("--memory", format!("size={}M,shared=on", spec.memory_bytes >> 20));
    push("--balloon", "size=0,free_page_reporting=on".into());

    match &spec.boot {
        BootSpec::Kernel { kernel, initramfs, cmdline } => {
            push("--kernel", utf8(kernel)?.into());
            push("--initramfs", utf8(initramfs)?.into());
            push("--cmdline", cmdline.clone());
        }
        // On aarch64 the UEFI firmware (CLOUDHV_EFI.fd) is loaded as the
        // kernel.
        BootSpec::Firmware { path } if cfg!(target_arch = "aarch64") => push("--kernel", utf8(path)?.into()),
        BootSpec::Firmware { path } => push("--firmware", utf8(path)?.into()),
    }

    if !spec.disks.is_empty() {
        a.push("--disk".into());
        for d in &spec.disks {
            check_value(&d.serial)?;
            a.push(
                format!(
                    "path={},image_type=qcow2,backing_files={},readonly={},serial={}",
                    utf8(&d.path)?,
                    on_off(d.backing_allowed),
                    on_off(d.read_only),
                    d.serial
                )
                .into(),
            );
        }
    }

    let mut push = |k: &str, v: String| {
        a.push(k.into());
        a.push(v.into());
    };
    if let Some(fs) = &spec.share {
        check_value(&fs.tag)?;
        push("--fs", format!("tag={},socket={},num_queues=1,queue_size=1024", fs.tag, utf8(&fs.socket)?));
    }
    if let Some(net) = &spec.net_socket {
        push("--net", format!("vhost_user=true,socket={},vhost_mode=client", utf8(net)?));
    }
    push("--vsock", format!("cid={GUEST_CID},socket={}", utf8(&spec.vsock_socket)?));
    push("--rng", "src=/dev/urandom".into());
    push("--console", format!("file={}", utf8(&spec.console_log)?));
    push("--serial", "off".into());

    if !spec.oem_strings.is_empty() {
        for s in &spec.oem_strings {
            if s.contains([',', '[', ']']) {
                return Err(invalid(format!("OEM string contains a separator: {s}")));
            }
        }
        push("--platform", format!("oem_strings=[{}]", spec.oem_strings.join(",")));
    }
    Ok(a)
}

fn on_off(b: bool) -> &'static str {
    if b { "on" } else { "off" }
}

fn invalid(msg: String) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, msg)
}

/// Paths and values end up in comma-separated option lists.
fn utf8(p: &Path) -> io::Result<&str> {
    let s = p.to_str().ok_or_else(|| invalid(format!("path is not UTF-8: {}", p.display())))?;
    check_value(s)?;
    Ok(s)
}

fn check_value(s: &str) -> io::Result<()> {
    if s.contains(',') || s.contains('=') && !s.starts_with('/') {
        return Err(invalid(format!("value cannot be passed to Cloud Hypervisor: {s}")));
    }
    Ok(())
}

/// Client for the Cloud Hypervisor HTTP API on its Unix socket.
#[derive(Debug, Clone)]
pub struct Api {
    socket: PathBuf,
}

impl Api {
    pub fn new(socket: impl Into<PathBuf>) -> Api {
        Api { socket: socket.into() }
    }

    async fn request(&self, method: &str, path: &str) -> io::Result<(u16, String)> {
        let mut s = UnixStream::connect(&self.socket).await?;
        let req = format!(
            "{method} /api/v1/{path} HTTP/1.1\r\nHost: localhost\r\nAccept: */*\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
        );
        s.write_all(req.as_bytes()).await?;

        let mut buf = Vec::new();
        tokio::time::timeout(Duration::from_secs(10), s.read_to_end(&mut buf))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "Cloud Hypervisor API timed out"))??;
        let text = String::from_utf8_lossy(&buf);
        let status = text
            .split_whitespace()
            .nth(1)
            .and_then(|c| c.parse().ok())
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "malformed API response"))?;
        let body = text.split_once("\r\n\r\n").map(|(_, b)| b.to_string()).unwrap_or_default();
        Ok((status, body))
    }

    async fn put(&self, path: &str) -> io::Result<()> {
        let (status, body) = self.request("PUT", path).await?;
        if !(200..300).contains(&status) {
            return Err(io::Error::other(format!("{path}: HTTP {status}: {}", body.trim())));
        }
        Ok(())
    }

    /// Presses the ACPI power button.
    pub async fn power_button(&self) -> io::Result<()> {
        self.put("vm.power-button").await
    }

    /// Stops the VM immediately.
    pub async fn shutdown(&self) -> io::Result<()> {
        self.put("vm.shutdown").await
    }

    /// Stops the VMM process.
    pub async fn shutdown_vmm(&self) -> io::Result<()> {
        self.put("vmm.shutdown").await
    }

    /// Returns the VM information document (JSON).
    pub async fn info(&self) -> io::Result<String> {
        let (status, body) = self.request("GET", "vm.info").await?;
        if status != 200 {
            return Err(io::Error::other(format!("vm.info: HTTP {status}")));
        }
        Ok(body)
    }

    /// Whether the API answers.
    pub async fn alive(&self) -> bool {
        self.request("GET", "vmm.ping").await.is_ok_and(|(s, _)| s == 200)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::spec::{Arch, DiskSpec, FileShareSpec};

    fn spec() -> VmSpec {
        VmSpec {
            arch: Arch::X86_64,
            cpus: 4,
            memory_bytes: 8 << 30,
            boot: BootSpec::Kernel {
                kernel: "/i/vmlinuz".into(),
                initramfs: "/i/initramfs.img".into(),
                cmdline: "root=/dev/disk/by-id/virtio-root rw console=hvc0".into(),
            },
            disks: vec![
                DiskSpec {
                    path: "/d/root.qcow2".into(),
                    serial: "root".into(),
                    read_only: false,
                    backing_allowed: true,
                },
                DiskSpec {
                    path: "/d/home.qcow2".into(),
                    serial: "home".into(),
                    read_only: false,
                    backing_allowed: false,
                },
            ],
            share: Some(FileShareSpec { socket: "/r/fs.sock".into(), tag: "toby".into() }),
            net_socket: Some("/r/net.sock".into()),
            vsock_socket: "/r/vsock.sock".into(),
            console_log: "/r/console.log".into(),
            api_socket: "/r/ch-api.sock".into(),
            oem_strings: vec![],
        }
    }

    #[test]
    fn kernel_boot_command_line() {
        let a: Vec<String> = args(&spec()).unwrap().into_iter().map(|s| s.into_string().unwrap()).collect();
        let joined = a.join(" ");
        assert!(joined.contains("--memory size=8192M,shared=on"));
        assert!(joined.contains("--kernel /i/vmlinuz --initramfs /i/initramfs.img"));
        assert!(joined.contains("--cmdline root=/dev/disk/by-id/virtio-root rw console=hvc0"));
        assert!(joined.contains(
            "--disk path=/d/root.qcow2,image_type=qcow2,backing_files=on,readonly=off,serial=root \
             path=/d/home.qcow2,image_type=qcow2,backing_files=off,readonly=off,serial=home"
        ));
        assert!(joined.contains("--fs tag=toby,socket=/r/fs.sock,num_queues=1,queue_size=1024"));
        assert!(joined.contains("--net vhost_user=true,socket=/r/net.sock,vhost_mode=client"));
        assert!(joined.contains("--vsock cid=3,socket=/r/vsock.sock"));
        assert!(!joined.contains("--platform"));
    }

    #[test]
    fn firmware_boot_with_oem_strings() {
        let mut s = spec();
        s.boot = BootSpec::Firmware { path: "/usr/lib/toby/firmware/CLOUDHV.fd".into() };
        s.oem_strings = vec!["io.systemd.credential.binary:x=YQ==".into()];
        let joined =
            args(&s).unwrap().into_iter().map(|s| s.into_string().unwrap()).collect::<Vec<_>>().join(" ");
        assert!(joined.contains("--firmware /usr/lib/toby/firmware/CLOUDHV.fd"));
        assert!(joined.contains("--platform oem_strings=[io.systemd.credential.binary:x=YQ==]"));
        assert!(!joined.contains("--kernel"));
    }

    #[test]
    fn separators_in_values_are_rejected() {
        let mut s = spec();
        s.disks[0].path = "/d/a,b.qcow2".into();
        assert!(args(&s).is_err());
        let mut s = spec();
        s.oem_strings = vec!["a,b".into()];
        assert!(args(&s).is_err());
    }
}
