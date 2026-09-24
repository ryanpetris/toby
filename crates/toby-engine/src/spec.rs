//! Platform-neutral description of a virtual machine (plan §7.1).

use std::path::PathBuf;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Arch {
    X86_64,
    Aarch64,
}

impl Arch {
    pub fn host() -> Arch {
        if cfg!(target_arch = "aarch64") { Arch::Aarch64 } else { Arch::X86_64 }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum BootSpec {
    /// Direct kernel boot with the image's kernel and Toby's initramfs.
    Kernel { kernel: PathBuf, initramfs: PathBuf, cmdline: String },
    /// UEFI firmware booting the disk's own bootloader.
    Firmware { path: PathBuf },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DiskSpec {
    pub path: PathBuf,
    /// Serial number; the guest sees `/dev/disk/by-id/virtio-<serial>`.
    pub serial: String,
    pub read_only: bool,
    /// Whether the disk may open its backing chain.
    pub backing_allowed: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FileShareSpec {
    pub socket: PathBuf,
    pub tag: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct VmSpec {
    pub arch: Arch,
    pub cpus: u32,
    pub memory_bytes: u64,
    pub boot: BootSpec,
    pub disks: Vec<DiskSpec>,
    pub share: Option<FileShareSpec>,
    /// vhost-user network back-end socket.
    pub net_socket: Option<PathBuf>,
    /// Hybrid vsock socket.
    pub vsock_socket: PathBuf,
    pub console_log: PathBuf,
    pub api_socket: PathBuf,
    /// SMBIOS type 11 strings.
    pub oem_strings: Vec<String>,
}
