//! qcow2 container creation (plan §6.2). Toby only writes headers and empty
//! tables; guest data is read and written by the VMM alone.

use std::io;
use std::path::Path;

use imago::file::File;
use imago::qcow2::Qcow2;
use imago::{FormatCreateBuilder, Storage, StorageCreateOptions};

/// Creates a sparse qcow2 image of `size` bytes, optionally as an overlay of
/// the qcow2 image `backing` (stored as an absolute path with an explicit
/// backing format). Fails if `path` exists.
pub async fn create(path: &Path, size: u64, backing: Option<&Path>) -> io::Result<()> {
    if path.exists() {
        return Err(io::Error::new(io::ErrorKind::AlreadyExists, format!("{} exists", path.display())));
    }
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }

    let file = File::create_open(StorageCreateOptions::new().filename(path).size(0)).await?;
    let mut builder = Qcow2::<File>::create_builder(file).size(size);
    if let Some(b) = backing {
        let b = std::path::absolute(b)?;
        let name = b
            .to_str()
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "backing path is not UTF-8"))?;
        builder = builder.backing(name.to_string(), "qcow2".to_string());
    }
    let result = builder.create().await;
    if result.is_err() {
        let _ = std::fs::remove_file(path);
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    fn qemu_img_info(path: &Path) -> Option<String> {
        let out = std::process::Command::new("qemu-img").arg("info").arg(path).output().ok()?;
        Some(String::from_utf8_lossy(&out.stdout).into())
    }

    #[tokio::test]
    async fn creates_sparse_images_and_overlays() {
        let dir = tempfile::tempdir().unwrap();
        let base = dir.path().join("base.qcow2");
        let over = dir.path().join("over.qcow2");
        create(&base, 64 << 30, None).await.unwrap();
        create(&over, 64 << 30, Some(&base)).await.unwrap();

        assert!(std::fs::metadata(&over).unwrap().len() < 1 << 20);
        assert!(create(&over, 1 << 30, None).await.is_err());

        if let Some(info) = qemu_img_info(&over) {
            assert!(info.contains("virtual size: 64 GiB"), "{info}");
            assert!(info.contains(&format!("backing file: {}", base.display())), "{info}");
            assert!(info.contains("backing file format: qcow2"), "{info}");
            let check = std::process::Command::new("qemu-img").arg("check").arg(&over).status().unwrap();
            assert!(check.success());
        }
    }
}
