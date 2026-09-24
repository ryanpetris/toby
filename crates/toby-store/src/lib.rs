//! Images, roots, homes, boot files, locks and garbage collection.

pub mod qcow2;
pub mod records;
pub mod store;

pub use records::{HomeRecord, ImageConfig, ImageRecord, ImageSource, RootRecord};
pub use store::{DiskLock, Store, is_locked, lock_disk};
