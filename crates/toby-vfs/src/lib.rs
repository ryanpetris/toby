//! virtio-fs server library: a synthetic tree with passthrough mounts and Toby's identity policy.

pub mod guard;
pub mod tree;

pub use guard::{Guard, OVERFLOW_ID, Squash};
pub use tree::{MountSpec, Tree};
