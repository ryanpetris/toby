//! Operations on images, roots and homes (plan §6.2).

use std::fs::File;
use std::io;
use std::os::fd::AsFd;
use std::path::{Path, PathBuf};

use nix::fcntl::{Flock, FlockArg};
use toby_config::paths::Paths;

use crate::qcow2;
use crate::records::{self, HomeRecord, ImageRecord, RootRecord, check_name, now};

/// Default virtual size of a home disk.
pub const HOME_SIZE: u64 = 100 << 30;
/// Default virtual size of an image disk.
pub const IMAGE_SIZE: u64 = 64 << 30;

/// An exclusive lock on a disk file, held while a machine uses it.
pub struct DiskLock {
    _lock: Flock<File>,
    pub path: PathBuf,
}

/// Takes the exclusive lock on `path` without waiting.
pub fn lock_disk(path: &Path) -> io::Result<DiskLock> {
    let f = File::open(path)?;
    let lock = Flock::lock(f, FlockArg::LockExclusiveNonblock).map_err(|(_, e)| {
        if e == nix::errno::Errno::EWOULDBLOCK {
            io::Error::new(
                io::ErrorKind::ResourceBusy,
                format!("{} is in use by a running machine", path.display()),
            )
        } else {
            io::Error::from(e)
        }
    })?;
    Ok(DiskLock {
        _lock: lock,
        path: path.to_path_buf(),
    })
}

/// Whether some process holds the lock on `path`.
pub fn is_locked(path: &Path) -> bool {
    match File::open(path) {
        Ok(f) => {
            let _ = f.as_fd();
            Flock::lock(f, FlockArg::LockExclusiveNonblock).is_err()
        }
        Err(_) => false,
    }
}

pub struct Store {
    pub paths: Paths,
}

impl Store {
    pub fn new(paths: Paths) -> Store {
        Store { paths }
    }

    fn images_dir(&self) -> PathBuf {
        self.paths.state.join("images")
    }
    fn roots_dir(&self) -> PathBuf {
        self.paths.state.join("roots")
    }
    fn homes_dir(&self) -> PathBuf {
        self.paths.state.join("homes")
    }

    pub fn image_record_path(&self, id: &str) -> PathBuf {
        self.images_dir().join(format!("{id}.toml"))
    }
    pub fn root_record_path(&self, name: &str) -> PathBuf {
        self.roots_dir().join(format!("{name}.toml"))
    }
    pub fn home_record_path(&self, name: &str) -> PathBuf {
        self.homes_dir().join(format!("{name}.toml"))
    }

    // Images

    pub fn images(&self) -> io::Result<Vec<ImageRecord>> {
        let mut v: Vec<ImageRecord> = records::load_all(&self.images_dir())?;
        v.sort_by(|a, b| a.created.cmp(&b.created).then(a.id.cmp(&b.id)));
        Ok(v)
    }

    pub fn image(&self, id: &str) -> io::Result<ImageRecord> {
        check_name_or_id(id)?;
        records::load(&self.image_record_path(id))
            .map_err(|_| io::Error::new(io::ErrorKind::NotFound, format!("no image {id}")))
    }

    pub fn add_image(&self, rec: &ImageRecord) -> io::Result<()> {
        records::store(&self.image_record_path(&rec.id), rec)
    }

    /// The newest image built from the same source as `rec`, if newer.
    pub fn newer_image(&self, rec: &ImageRecord) -> io::Result<Option<ImageRecord>> {
        Ok(self
            .images()?
            .into_iter()
            .rfind(|i| i.source == rec.source && i.arch == rec.arch && i.created > rec.created))
    }

    fn image_referenced(&self, id: &str) -> io::Result<Option<String>> {
        Ok(self.roots()?.into_iter().find(|r| r.image == id).map(|r| r.name))
    }

    /// Removes an image; refused while a root is based on it.
    pub fn remove_image(&self, id: &str) -> io::Result<()> {
        self.image(id)?;
        if let Some(root) = self.image_referenced(id)? {
            return Err(io::Error::other(format!("image {id} is used by root {root}")));
        }
        let dir = self.paths.image_dir(id);
        make_writable(&dir);
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::remove_file(self.image_record_path(id))
    }

    /// Removes unreferenced images created before `older_than`, except those
    /// in `keep`. Returns the removed IDs.
    pub fn prune_images(&self, older_than: u64, keep: &[String]) -> io::Result<Vec<String>> {
        let mut removed = Vec::new();
        for img in self.images()? {
            if img.created < older_than
                && !keep.contains(&img.id)
                && self.image_referenced(&img.id)?.is_none()
            {
                self.remove_image(&img.id)?;
                removed.push(img.id);
            }
        }
        Ok(removed)
    }

    // Roots

    pub fn roots(&self) -> io::Result<Vec<RootRecord>> {
        let mut v: Vec<RootRecord> = records::load_all(&self.roots_dir())?;
        v.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(v)
    }

    pub fn root(&self, name: &str) -> io::Result<RootRecord> {
        check_name("root", name)?;
        records::load(&self.root_record_path(name))
            .map_err(|_| io::Error::new(io::ErrorKind::NotFound, format!("no root {name}")))
    }

    async fn write_root_disk(&self, name: &str, image: &str) -> io::Result<()> {
        let backing = self.paths.image_dir(image).join("disk.qcow2");
        if !backing.is_file() {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                format!("image {image} has no disk"),
            ));
        }
        qcow2::create(&self.paths.root_disk(name), IMAGE_SIZE, Some(&backing)).await
    }

    pub async fn create_root(&self, name: &str, image: &str) -> io::Result<RootRecord> {
        check_name("root", name)?;
        self.image(image)?;
        if self.root_record_path(name).exists() {
            return Err(io::Error::new(
                io::ErrorKind::AlreadyExists,
                format!("root {name} exists"),
            ));
        }
        self.write_root_disk(name, image).await?;
        let rec = RootRecord {
            name: name.into(),
            image: image.into(),
            created: now(),
        };
        records::store(&self.root_record_path(name), &rec)?;
        Ok(rec)
    }

    /// Recreates the root's disk against `image` (the same image for a reset).
    async fn replace_root(&self, name: &str, image: &str) -> io::Result<RootRecord> {
        let mut rec = self.root(name)?;
        self.image(image)?;
        let disk = self.paths.root_disk(name);
        if disk.exists() {
            let _lock = lock_disk(&disk)?;
            std::fs::remove_file(&disk)?;
        }
        self.write_root_disk(name, image).await?;
        rec.image = image.into();
        rec.created = now();
        records::store(&self.root_record_path(name), &rec)?;
        Ok(rec)
    }

    pub async fn reset_root(&self, name: &str) -> io::Result<RootRecord> {
        let image = self.root(name)?.image;
        self.replace_root(name, &image).await
    }

    pub async fn rebase_root(&self, name: &str, image: &str) -> io::Result<RootRecord> {
        self.replace_root(name, image).await
    }

    pub fn remove_root(&self, name: &str) -> io::Result<()> {
        self.root(name)?;
        let disk = self.paths.root_disk(name);
        if disk.exists() {
            let _lock = lock_disk(&disk)?;
            std::fs::remove_file(&disk)?;
        }
        std::fs::remove_file(self.root_record_path(name))
    }

    // Homes

    pub fn homes(&self) -> io::Result<Vec<HomeRecord>> {
        let mut v: Vec<HomeRecord> = records::load_all(&self.homes_dir())?;
        v.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(v)
    }

    pub fn home(&self, name: &str) -> io::Result<HomeRecord> {
        check_name("home", name)?;
        records::load(&self.home_record_path(name))
            .map_err(|_| io::Error::new(io::ErrorKind::NotFound, format!("no home {name}")))
    }

    /// Creates the home's record and empty disk; it still needs formatting.
    pub async fn create_home(
        &self,
        name: &str,
        username: &str,
        uid: u32,
        size: u64,
    ) -> io::Result<HomeRecord> {
        check_name("home", name)?;
        if self.home_record_path(name).exists() {
            return Err(io::Error::new(
                io::ErrorKind::AlreadyExists,
                format!("home {name} exists"),
            ));
        }
        qcow2::create(&self.paths.home_disk(name), size, None).await?;
        let rec = HomeRecord {
            name: name.into(),
            username: username.into(),
            uid,
            sudo: true,
            shell: None,
            default_root: None,
            formatted: false,
            created: now(),
        };
        records::store(&self.home_record_path(name), &rec)?;
        Ok(rec)
    }

    pub fn update_home(&self, rec: &HomeRecord) -> io::Result<()> {
        records::store(&self.home_record_path(&rec.name), rec)
    }

    pub fn remove_home(&self, name: &str) -> io::Result<()> {
        self.home(name)?;
        let disk = self.paths.home_disk(name);
        if disk.exists() {
            let _lock = lock_disk(&disk)?;
            std::fs::remove_file(&disk)?;
        }
        std::fs::remove_file(self.home_record_path(name))
    }
}

fn check_name_or_id(id: &str) -> io::Result<()> {
    if !id.is_empty()
        && id.len() <= 64
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
    {
        Ok(())
    } else {
        Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("invalid image ID {id:?}"),
        ))
    }
}

/// Image files are read-only; make them removable.
fn make_writable(dir: &Path) {
    use std::os::unix::fs::PermissionsExt;
    if let Ok(entries) = std::fs::read_dir(dir) {
        for e in entries.flatten() {
            let _ = std::fs::set_permissions(e.path(), std::fs::Permissions::from_mode(0o644));
        }
    }
    let _ = std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o755));
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::records::{ImageConfig, ImageSource};
    use toby_config::global::GlobalConfig;

    fn store() -> (tempfile::TempDir, Store) {
        let dir = tempfile::tempdir().unwrap();
        let paths = Paths::with(
            dir.path().to_path_buf(),
            &GlobalConfig::default(),
            dir.path().join("run"),
        );
        (dir, Store::new(paths))
    }

    async fn image(s: &Store, id: &str, created: u64) {
        let d = s.paths.image_dir(id);
        std::fs::create_dir_all(&d).unwrap();
        qcow2::create(&d.join("disk.qcow2"), 1 << 30, None).await.unwrap();
        s.add_image(&ImageRecord {
            id: id.into(),
            arch: "x86_64".into(),
            created,
            source: ImageSource::Default,
            source_hash: "h".into(),
            kernel_version: "k".into(),
            adaptation_version: 1,
            config: ImageConfig::default(),
        })
        .unwrap();
    }

    #[tokio::test]
    async fn roots_follow_their_images() {
        let (_d, s) = store();
        image(&s, "img1", 1).await;
        image(&s, "img2", 2).await;

        s.create_root("work", "img1").await.unwrap();
        assert!(s.create_root("work", "img1").await.is_err());
        assert!(s.remove_image("img1").is_err());
        assert_eq!(
            s.newer_image(&s.image("img1").unwrap()).unwrap().unwrap().id,
            "img2"
        );

        let disk = s.paths.root_disk("work");
        std::fs::write(disk.with_extension("marker"), "").unwrap();
        s.reset_root("work").await.unwrap();
        assert!(disk.exists());

        s.rebase_root("work", "img2").await.unwrap();
        assert_eq!(s.root("work").unwrap().image, "img2");
        assert_eq!(s.prune_images(u64::MAX, &[]).unwrap(), vec!["img1".to_string()]);

        s.remove_root("work").unwrap();
        assert!(!disk.exists());
        assert!(s.roots().unwrap().is_empty());
    }

    #[tokio::test]
    async fn locked_disks_cannot_be_replaced() {
        let (_d, s) = store();
        image(&s, "img1", 1).await;
        s.create_root("busy", "img1").await.unwrap();
        let _held = lock_disk(&s.paths.root_disk("busy")).unwrap();
        assert!(is_locked(&s.paths.root_disk("busy")));
        let err = s.reset_root("busy").await.unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::ResourceBusy);
        assert!(s.remove_root("busy").is_err());
    }

    #[tokio::test]
    async fn homes() {
        let (_d, s) = store();
        let h = s.create_home("work", "dev", 1000, 1 << 30).await.unwrap();
        assert!(!h.formatted);
        assert!(s.paths.home_disk("work").exists());
        let mut h = s.home("work").unwrap();
        h.formatted = true;
        s.update_home(&h).unwrap();
        assert!(s.home("work").unwrap().formatted);
        assert!(s.create_home("Bad", "dev", 1000, 1).await.is_err());
        s.remove_home("work").unwrap();
        assert!(s.homes().unwrap().is_empty());
    }
}
