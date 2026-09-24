//! systemd socket activation: listening sockets passed as `LISTEN_FDS`.

use std::os::fd::{FromRawFd, OwnedFd};

const FIRST_FD: i32 = 3;

/// Takes the sockets systemd passed to this process, if any. The variables
/// are removed so children do not inherit them.
pub fn take_listen_fds() -> Vec<OwnedFd> {
    let pid = std::env::var("LISTEN_PID").ok().and_then(|p| p.parse::<u32>().ok());
    let count = std::env::var("LISTEN_FDS").ok().and_then(|n| n.parse::<i32>().ok()).unwrap_or(0);
    // SAFETY: only this function touches these variables, at start.
    unsafe {
        std::env::remove_var("LISTEN_PID");
        std::env::remove_var("LISTEN_FDS");
        std::env::remove_var("LISTEN_FDNAMES");
    }
    if pid != Some(std::process::id()) {
        return Vec::new();
    }
    (FIRST_FD..FIRST_FD + count)
        .map(|fd| {
            let _ = nix::fcntl::fcntl(
                // SAFETY: systemd passed these descriptors to us.
                unsafe { std::os::fd::BorrowedFd::borrow_raw(fd) },
                nix::fcntl::FcntlArg::F_SETFD(nix::fcntl::FdFlag::FD_CLOEXEC),
            );
            // SAFETY: systemd passed these descriptors to us; nothing else owns them.
            unsafe { OwnedFd::from_raw_fd(fd) }
        })
        .collect()
}
