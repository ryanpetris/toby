//! Host process supervision: the systemd user manager and direct supervision
//! (plan §12).

pub mod activation;
pub mod direct;
pub mod notify;
pub mod systemd;
