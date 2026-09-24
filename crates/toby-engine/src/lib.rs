//! Virtual machine engine abstraction and its Cloud Hypervisor implementation.

pub mod cloud_hypervisor;
pub mod spec;

pub use spec::{Arch, BootSpec, DiskSpec, FileShareSpec, VmSpec};
