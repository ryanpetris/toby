//! Builder machines: image builds, the one-time bootstrap and home
//! formatting (plan §15).

use std::fs::File;
use std::io::{self, Read, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use nix::fcntl::{Flock, FlockArg};
use toby_config::global::GlobalConfig;
use toby_config::machine::{self, Attach, Boot, Disk, MachineSpec, MachineStatus, RootSpec, State};
use toby_config::paths::{MachineRuntime, Paths};
use toby_proto::machine::{Request, Response};
use toby_proto::types::{ExitStatus, Identity, SUPPORTED};
use toby_proto::{frame, machine as mp};
use toby_store::records::{ImageConfig, ImageRecord, ImageSource, now};
use toby_store::{Store, hash, qcow2};
use tokio::net::UnixStream;

/// Version of the boot adaptation performed by builds; images from an older
/// adaptation are rebuilt.
pub const ADAPTATION_VERSION: u32 = 1;

/// Size of the persistent build cache disk.
pub const CACHE_SIZE: u64 = 100 << 30;

const READY_TIMEOUT: Duration = Duration::from_secs(300);
const STOP_TIMEOUT: Duration = Duration::from_secs(90);

/// Receives build output.
pub type Output<'a> = &'a mut (dyn FnMut(&[u8], bool) + Send);

pub struct Builder {
    pub config: GlobalConfig,
    pub paths: Paths,
    pub store: Store,
    /// The `toby` binary that supervises builder machines.
    pub exe: PathBuf,
}

/// Host architecture name as used in image records and cloud image names.
pub fn arch() -> &'static str {
    if cfg!(target_arch = "aarch64") { "aarch64" } else { "x86_64" }
}

fn debian_arch() -> &'static str {
    if cfg!(target_arch = "aarch64") { "arm64" } else { "amd64" }
}

fn err(msg: impl Into<String>) -> io::Error {
    io::Error::other(msg.into())
}

/// The version new guest processes run: the target of `<versions>/current`.
pub fn runtime_version(versions: &Path) -> String {
    std::fs::read_link(versions.join("current"))
        .ok()
        .and_then(|t| t.file_name().map(|n| n.to_string_lossy().into_owned()))
        .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_string())
}

/// A job's command line in the guest.
fn helper(version: &str, args: &[&str]) -> Vec<String> {
    let toby = format!("/run/toby/fs/versions/{version}/toby");
    [toby.as_str(), "guest", "helper"].iter().chain(args).map(|s| s.to_string()).collect()
}

impl Builder {
    pub fn new(config: GlobalConfig, paths: Paths, exe: PathBuf) -> Builder {
        let store = Store::new(paths.clone());
        Builder { config, paths, store, exe }
    }

    fn runtime_version(&self) -> String {
        runtime_version(&self.config.programs.versions())
    }

    fn builder_dir(&self) -> PathBuf {
        self.paths.data.join("builder").join(arch())
    }

    fn caches_dir(&self) -> PathBuf {
        self.builder_dir().join("caches")
    }

    pub fn bootstrap_image(&self) -> PathBuf {
        self.builder_dir().join("bootstrap-debian-13.qcow2")
    }

    /// The build cache for builds from `source`, locked for this build. Each
    /// source has its own cache, so a build can only affect later builds of
    /// the same source (plan §15.3).
    async fn cache_disk(&self, source: &ImageSource, out: Output<'_>) -> io::Result<(PathBuf, Flock<File>)> {
        let dir = self.caches_dir();
        std::fs::create_dir_all(&dir)?;
        let key = cache_key(source);
        let file = std::fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .write(true)
            .open(dir.join(format!("{key}.lock")))?;
        let lock = match Flock::lock(file, FlockArg::LockExclusiveNonblock) {
            Ok(l) => l,
            Err((f, nix::errno::Errno::EWOULDBLOCK)) => {
                out(b"==> Waiting for another build of the same source\n", false);
                tokio::task::spawn_blocking(move || Flock::lock(f, FlockArg::LockExclusive))
                    .await
                    .map_err(io::Error::other)?
                    .map_err(|(_, e)| io::Error::from(e))?
            }
            Err((_, e)) => return Err(e.into()),
        };
        let path = dir.join(format!("{key}.qcow2"));
        if !path.exists() {
            qcow2::create(&path, CACHE_SIZE, None).await?;
        }
        Ok((path, lock))
    }

    /// Removes the build caches of sources no image comes from any more.
    pub fn prune_caches(&self) -> io::Result<Vec<PathBuf>> {
        let mut keep: Vec<String> = self.store.images()?.iter().map(|i| cache_key(&i.source)).collect();
        keep.push(cache_key(&ImageSource::Default));
        let mut removed = Vec::new();
        let Ok(entries) = std::fs::read_dir(self.caches_dir()) else { return Ok(removed) };
        for e in entries.flatten() {
            let path = e.path();
            let Some(key) = path.file_stem().and_then(|k| k.to_str()).map(str::to_string) else { continue };
            if path.extension().is_none_or(|x| x != "qcow2") || keep.contains(&key) {
                continue;
            }
            let lock_path = path.with_extension("lock");
            let Ok(file) =
                std::fs::OpenOptions::new().create(true).truncate(false).write(true).open(&lock_path)
            else {
                continue;
            };
            // The lock file stays: a build may be waiting on it. The disk
            // itself stays locked while a builder machine uses it.
            if let Ok(_lock) = Flock::lock(file, FlockArg::LockExclusiveNonblock)
                && let Ok(_in_use) = toby_store::store::lock_disk(&path)
            {
                std::fs::remove_file(&path)?;
                removed.push(path);
            }
        }
        Ok(removed)
    }

    /// Removes what interrupted builds left behind: unfinished image
    /// directories, builder machine state and import directories that no
    /// running build holds. Each build locks its own right after creating
    /// them, so only directories older than a minute are considered.
    fn sweep(&self) {
        let parent = |p: PathBuf| p.parent().map(Path::to_path_buf).unwrap_or_default();
        // (directory, name prefix, name suffix)
        let leftovers = [
            (parent(self.paths.image_dir("x")), "", ".tmp"),
            (parent(self.paths.machine_state_dir("x")), "builder-", ""),
            (self.paths.state.clone(), "import-", ""),
        ];
        let recent = std::time::SystemTime::now() - Duration::from_secs(60);
        for (dir, prefix, suffix) in leftovers {
            let Ok(entries) = std::fs::read_dir(&dir) else { continue };
            for e in entries.flatten() {
                let name = e.file_name().to_string_lossy().into_owned();
                let old = e.metadata().and_then(|m| m.modified()).is_ok_and(|t| t < recent);
                if !name.starts_with(prefix) || !name.ends_with(suffix) || !old {
                    continue;
                }
                // An unfinished image's output disk stays locked while its
                // builder machine runs, even if the build process is gone.
                let disk = e.path().join("disk.qcow2");
                let disk_free = !disk.exists() || toby_store::store::lock_disk(&disk).is_ok();
                if let Ok(_held) = lock_dir(&e.path(), true)
                    && disk_free
                {
                    let _ = std::fs::remove_dir_all(e.path());
                    if name.starts_with("builder-") {
                        let _ = std::fs::remove_dir_all(self.paths.machine_runtime(&name).dir);
                    }
                }
            }
        }
    }

    /// What changes to boot adaptation: the job scripts, the dracut module
    /// and the adaptation version.
    fn adaptation_hash(&self) -> io::Result<String> {
        let scripts =
            toby_guest::helper::build::SCRIPTS.iter().flat_map(|(n, b)| [n.as_bytes(), b.as_bytes()]);
        let dracut = hash::hash_tree(&self.config.programs.share().join("dracut/99toby"))?;
        Ok(hash::combine(
            scripts
                .map(<[u8]>::to_vec)
                .chain([dracut.into_bytes(), ADAPTATION_VERSION.to_string().into_bytes()]),
        ))
    }

    fn mkosi_hash(&self) -> io::Result<String> {
        let mkosi = self.config.programs.share().join("mkosi");
        hash::hash_file(&mkosi.join("pyproject.toml")).or_else(|_| hash::hash_tree(&mkosi.join("mkosi")))
    }

    /// The hash of everything an image from `source` is built from; an image
    /// is current while it matches (plan §15.6). Registry references are
    /// hashed as written: a tag is re-resolved only on request.
    pub fn source_hash(&self, source: &ImageSource) -> io::Result<String> {
        let adaptation = self.adaptation_hash()?;
        Ok(match source {
            ImageSource::Default => {
                let conf = hash::hash_tree(&self.config.programs.share().join("images/default"))?;
                hash::combine([conf, self.mkosi_hash()?, adaptation])
            }
            ImageSource::Mkosi { path } => {
                hash::combine([hash::hash_tree(path)?, self.mkosi_hash()?, adaptation])
            }
            ImageSource::Dockerfile { path, context } => {
                hash::combine([hash::hash_file(path)?, hash::hash_tree(context)?, adaptation])
            }
            ImageSource::Registry { reference } => hash::combine([reference.clone(), adaptation]),
            ImageSource::Archive { path } => hash::combine([hash::hash_file(path)?, adaptation]),
        })
    }

    /// The newest image built from `source` as it is now, if any.
    pub fn current_image(&self, source: &ImageSource) -> io::Result<Option<ImageRecord>> {
        let hash = self.source_hash(source)?;
        Ok(self.store.images()?.into_iter().rfind(|i| {
            i.source == *source
                && i.arch == arch()
                && i.source_hash == hash
                && i.adaptation_version == ADAPTATION_VERSION
        }))
    }

    /// The current default image, if one has been built from the bundled
    /// configuration.
    pub fn default_image(&self) -> io::Result<Option<ImageRecord>> {
        self.current_image(&ImageSource::Default)
    }

    /// Any default image, current or not, to boot a builder with.
    fn any_default_image(&self) -> io::Result<Option<ImageRecord>> {
        Ok(self.store.images()?.into_iter().rfind(|i| i.source == ImageSource::Default && i.arch == arch()))
    }

    /// The image builders boot, bootstrapping one first if there is none.
    async fn builder_image(&self, out: Output<'_>) -> io::Result<ImageRecord> {
        match self.any_default_image()? {
            Some(img) => Ok(img),
            None => self.bootstrap(None, out).await,
        }
    }

    async fn run_machine(
        &self,
        spec: MachineSpec,
        jobs: Vec<Vec<String>>,
        out: Output<'_>,
    ) -> io::Result<()> {
        let id = spec.id.clone();
        let state = self.paths.machine_state_dir(&id);
        std::fs::create_dir_all(&state)?;
        // Held for the build, so a sweep by another build leaves it alone.
        let _held = lock_dir(&state, false)?;
        spec.store(&self.paths.machine_desired(&id))?;
        let runtime = self.paths.machine_runtime(&id);
        std::fs::create_dir_all(&runtime.dir)?;

        let log = std::fs::File::create(runtime.dir.join("supervisor.log"))?;
        let mut cmd = tokio::process::Command::new(&self.exe);
        cmd.args(["internal", "machine", "--supervise", "--machine", &id])
            .stdin(Stdio::null())
            .stdout(log.try_clone()?)
            .stderr(log)
            .kill_on_drop(true);
        // The builder machine stops if this process dies.
        // SAFETY: prctl is async-signal-safe.
        unsafe {
            cmd.pre_exec(|| {
                nix::sys::prctl::set_pdeathsig(nix::sys::signal::Signal::SIGTERM).map_err(io::Error::from)
            });
        }
        let mut supervisor = cmd.spawn()?;

        let result = async {
            wait_ready(&runtime, &mut supervisor).await?;
            for argv in jobs {
                // Root, with the adaptation version the scripts record.
                let env = vec![("TOBY_ADAPTATION_VERSION".into(), ADAPTATION_VERSION.to_string())];
                let status = crate::control::run(&runtime, argv, Identity::Root, env, &mut *out).await?;
                if status != ExitStatus::Code(0) {
                    return Err(err(format!("the build job failed (exit status {})", status.code())));
                }
            }
            Ok(())
        }
        .await;

        // Stop the machine whether or not the jobs succeeded.
        if let Ok(mut c) = UnixStream::connect(runtime.control_sock()).await {
            let _ = control_call(&mut c, Request::Hello(mp::Hello { versions: SUPPORTED.to_vec() })).await;
            let _ = control_call(&mut c, Request::Stop(mp::Stop {})).await;
        }
        if tokio::time::timeout(STOP_TIMEOUT, supervisor.wait()).await.is_err() {
            if let Some(pid) = supervisor.id() {
                let _ = nix::sys::signal::kill(
                    nix::unistd::Pid::from_raw(pid as i32),
                    nix::sys::signal::Signal::SIGTERM,
                );
            }
            if tokio::time::timeout(Duration::from_secs(10), supervisor.wait()).await.is_err() {
                let _ = supervisor.kill().await;
            }
        }
        // Keep the logs of a failed build for inspection.
        let _ = std::fs::remove_dir_all(&state);
        if result.is_ok() {
            let _ = std::fs::remove_dir_all(&runtime.dir);
        }
        result.map_err(|e| err(format!("{e} (machine logs: {})", runtime.dir.display())))
    }

    fn builder_spec(
        &self,
        root: RootSpec,
        boot: Option<String>,
        disks: Vec<Disk>,
        attach: Vec<Attach>,
    ) -> MachineSpec {
        MachineSpec {
            schema: machine::SCHEMA,
            generation: 1,
            id: format!("builder-{}", toby_config::new_id().to_lowercase()),
            home: None,
            root,
            ephemeral: false,
            resources: crate::machines::default_resources(),
            boot: Boot { image: boot },
            disk: disks,
            attach,
            forward: Vec::new(),
            capabilities: Default::default(),
            idle_timeout: None,
            services: None,
            tools: Vec::new(),
            mcp: Vec::new(),
        }
    }

    /// Builds an image from `source` (plan §15.3), bootstrapping the default
    /// image first if there is none.
    pub async fn build(&self, source: ImageSource, out: Output<'_>) -> io::Result<ImageRecord> {
        let fresh = self.any_default_image()?.is_none();
        let base = self.builder_image(&mut *out).await?;
        if fresh && source == ImageSource::Default {
            return Ok(base);
        }
        let job = self.job_for(&source)?;
        let source_hash = self.source_hash(&source)?;
        let root = RootSpec::Image { image: base.id.clone() };
        self.build_with(root, Some(base.id), source, job, source_hash, Vec::new(), out).await
    }

    /// Describes the build job for a source.
    fn job_for(&self, source: &ImageSource) -> io::Result<Job> {
        let name_in = |dir: &Path, file: &Path| -> io::Result<String> {
            let rel = file.strip_prefix(dir).map_err(|_| {
                err(format!("{} must be inside the build context {}", file.display(), dir.display()))
            })?;
            Ok(rel.to_string_lossy().into_owned())
        };
        Ok(match source {
            ImageSource::Default => Job { args: vec!["default".into()], context: None, _private: None },
            ImageSource::Mkosi { path } => {
                Job { args: vec!["mkosi".into(), ".".into()], context: Some(path.clone()), _private: None }
            }
            ImageSource::Dockerfile { path, context } => Job {
                args: vec!["dockerfile".into(), name_in(context, path)?],
                context: Some(context.clone()),
                _private: None,
            },
            ImageSource::Registry { reference } => {
                Job { args: vec!["registry".into(), reference.clone()], context: None, _private: None }
            }
            ImageSource::Archive { path } => {
                // Share only the archive with the build, not its directory.
                std::fs::create_dir_all(&self.paths.state)?;
                let dir = tempfile::Builder::new().prefix("import-").tempdir_in(&self.paths.state)?;
                let file = dir.path().join("image.tar");
                let held = lock_dir(dir.path(), false)?;
                if std::fs::hard_link(path, &file).is_err() {
                    std::fs::copy(path, &file)?;
                }
                Job {
                    args: vec!["archive".into(), "image.tar".into()],
                    context: Some(dir.path().to_path_buf()),
                    _private: Some((held, dir)),
                }
            }
        })
    }

    #[allow(clippy::too_many_arguments)]
    async fn build_with(
        &self,
        root: RootSpec,
        boot: Option<String>,
        source: ImageSource,
        job: Job,
        source_hash: String,
        before: Vec<Vec<String>>,
        out: Output<'_>,
    ) -> io::Result<ImageRecord> {
        self.sweep();
        let id = toby_config::new_id();
        let final_dir = self.paths.image_dir(&id);
        let work = final_dir.with_extension("tmp");
        let boot_dir = work.join("boot");
        std::fs::create_dir_all(&boot_dir)?;
        let _held = lock_dir(&work, false)?;
        let result = self.build_in(&work, root, boot, &source, &job, &id, before, out).await;
        let (kernel_version, config) = match result {
            Ok(r) => r,
            Err(e) => {
                let _ = std::fs::remove_dir_all(&work);
                return Err(e);
            }
        };
        std::fs::rename(&work, &final_dir)?;

        let rec = ImageRecord {
            id,
            arch: arch().into(),
            created: now(),
            source,
            source_hash,
            kernel_version,
            adaptation_version: ADAPTATION_VERSION,
            config,
        };
        self.store.add_image(&rec)?;
        Ok(rec)
    }

    /// Runs the build and moves its results into `work`: the disk, the
    /// kernel and initramfs. Returns the kernel version and image config.
    #[allow(clippy::too_many_arguments)]
    async fn build_in(
        &self,
        work: &Path,
        root: RootSpec,
        boot: Option<String>,
        source: &ImageSource,
        job: &Job,
        id: &str,
        before: Vec<Vec<String>>,
        out: Output<'_>,
    ) -> io::Result<(String, ImageConfig)> {
        let boot_dir = work.join("boot");
        let disk = work.join("disk.qcow2");
        qcow2::create(&disk, toby_store::store::IMAGE_SIZE, None).await?;
        let (cache, _cache_lock) = self.cache_disk(source, &mut *out).await?;
        let disks = vec![
            Disk { path: cache, serial: "cache".into(), read_only: false },
            Disk { path: disk.clone(), serial: "out".into(), read_only: false },
        ];
        let mut attach = vec![Attach {
            id: "boot".into(),
            host: path_str(&boot_dir)?,
            at: "/build/boot".into(),
            read_only: false,
            pinned: false,
            persist: false,
            sessions: Vec::new(),
        }];
        if let Some(ctx) = &job.context {
            attach.push(Attach {
                id: "context".into(),
                host: path_str(ctx)?,
                at: "/build/context".into(),
                read_only: true,
                pinned: false,
                persist: false,
                sessions: Vec::new(),
            });
        }

        let version = self.runtime_version();
        let mut args: Vec<&str> = vec!["build", id];
        args.extend(job.args.iter().map(|s| s.as_str()));
        let mut jobs = before;
        jobs.push(helper(&version, &args));
        let spec = self.builder_spec(root, boot, disks, attach);
        self.run_machine(spec, jobs, out).await?;

        // The build controls the boot directory: take regular files only,
        // copied into files of our own.
        let kernel_version = read_boot_file(&boot_dir.join("kernel-version"), 256)?.trim().to_string();
        if kernel_version.is_empty() || kernel_version.contains(['/', '\n']) {
            return Err(err("the build reported an invalid kernel version"));
        }
        let config = match read_boot_file(&boot_dir.join("config.json"), 1 << 20) {
            Ok(json) => parse_image_config(&json),
            Err(e) if e.kind() == io::ErrorKind::NotFound => ImageConfig::default(),
            Err(e) => return Err(e),
        };
        copy_boot_file(&boot_dir.join("vmlinuz"), &work.join("vmlinuz"), 512 << 20)?;
        copy_boot_file(&boot_dir.join("initramfs.img"), &work.join("initramfs.img"), 2 << 30)?;
        std::fs::remove_dir_all(&boot_dir)?;
        std::fs::set_permissions(&disk, std::fs::Permissions::from_mode(0o444))?;
        Ok((kernel_version, config))
    }

    /// Downloads the Debian 13 cloud image used once to build the first
    /// default image, checked against Debian's published SHA512SUMS.
    pub async fn download_bootstrap(&self, out: Output<'_>) -> io::Result<PathBuf> {
        let target = self.bootstrap_image();
        if target.exists() {
            return Ok(target);
        }
        std::fs::create_dir_all(self.builder_dir())?;
        // One download at a time; a second caller finds the image in place.
        let file = std::fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .write(true)
            .open(self.builder_dir().join("bootstrap.lock"))?;
        let _lock = tokio::task::spawn_blocking(move || Flock::lock(file, FlockArg::LockExclusive))
            .await
            .map_err(io::Error::other)?
            .map_err(|(_, e)| io::Error::from(e))?;
        if target.exists() {
            return Ok(target);
        }
        let name = format!("debian-13-genericcloud-{}.qcow2", debian_arch());
        out(format!("==> Downloading {name}\n").as_bytes(), false);
        let fetch = target.clone();
        tokio::task::spawn_blocking(move || -> io::Result<()> {
            let base = "https://cloud.debian.org/images/cloud/trixie/latest";
            let sums = crate::download::text(&format!("{base}/SHA512SUMS"))?;
            let expected = sums
                .lines()
                .find_map(|l| {
                    let (sum, file) = l.split_once(char::is_whitespace)?;
                    (file.trim().trim_start_matches('*') == name).then(|| sum.to_string())
                })
                .ok_or_else(|| err(format!("{name} is not listed in SHA512SUMS")))?;
            let part = fetch.with_extension("part");
            let actual = crate::download::file(&format!("{base}/{name}"), &part)?;
            if actual != expected {
                let _ = std::fs::remove_file(&part);
                return Err(err(format!("{name} does not match its published SHA512 checksum")));
            }
            std::fs::rename(&part, &fetch)
        })
        .await
        .map_err(io::Error::other)??;
        Ok(target)
    }

    /// Builds the default image in the bootstrap builder (plan §15.2).
    pub async fn bootstrap(&self, base: Option<&Path>, out: Output<'_>) -> io::Result<ImageRecord> {
        let cloud = match base {
            Some(b) => {
                if !b.is_file() {
                    return Err(err(format!("{} does not exist", b.display())));
                }
                b.to_path_buf()
            }
            None => self.download_bootstrap(&mut *out).await?,
        };
        let version = self.runtime_version();
        let job = self.job_for(&ImageSource::Default)?;
        let hash = self.source_hash(&ImageSource::Default)?;
        let root = RootSpec::CloudImage { cloud_image: cloud };
        let provision = vec![helper(&version, &["provision"])];
        self.build_with(root, None, ImageSource::Default, job, hash, provision, out).await
    }

    /// Makes sure the current default image exists, bootstrapping or
    /// rebuilding it as needed.
    pub async fn prepare_default(&self, rebuild: bool, out: Output<'_>) -> io::Result<ImageRecord> {
        if !rebuild && let Some(img) = self.default_image()? {
            return Ok(img);
        }
        self.build(ImageSource::Default, out).await
    }

    /// `toby image prepare` (plan §15.6): the default image, and with `all`
    /// the source of every root whose image is out of date. Returns the
    /// default image.
    pub async fn prepare(&self, all: bool, rebuild: bool, out: Output<'_>) -> io::Result<ImageRecord> {
        let default = self.prepare_default(rebuild, &mut *out).await?;
        if !all {
            return Ok(default);
        }
        let mut sources: Vec<ImageSource> = Vec::new();
        for root in self.store.roots()? {
            let img = self.store.image(&root.image)?;
            if img.source != ImageSource::Default && !sources.contains(&img.source) {
                sources.push(img.source);
            }
        }
        let mut failed = 0;
        for source in sources {
            let result = match self.current_image(&source) {
                Ok(Some(_)) if !rebuild => continue,
                Ok(_) => self.build(source.clone(), &mut *out).await.map(drop),
                Err(e) => Err(e),
            };
            if let Err(e) = result {
                out(format!("toby: {}: {e}\n", source.describe()).as_bytes(), true);
                failed += 1;
            }
        }
        if failed > 0 {
            return Err(err(format!("{failed} of the roots' images could not be built")));
        }
        Ok(default)
    }

    /// Formats a new home's disk with ext4 in a builder machine.
    pub async fn format_home(&self, name: &str, out: Output<'_>) -> io::Result<()> {
        let mut home = self.store.home(name)?;
        let base = self.builder_image(&mut *out).await?;
        self.sweep();
        // The builder machine locks the home disk like any machine would.
        let disk = self.paths.home_disk(name);
        let spec = self.builder_spec(
            RootSpec::Image { image: base.id.clone() },
            Some(base.id),
            vec![Disk { path: disk, serial: "out".into(), read_only: false }],
            Vec::new(),
        );
        let version = self.runtime_version();
        self.run_machine(spec, vec![helper(&version, &["format-home"])], out).await?;
        home.formatted = true;
        self.store.update_home(&home)
    }
}

/// A build job: its arguments, the directory attached as the build context
/// and, for imports, the private directory that holds the archive.
struct Job {
    args: Vec<String>,
    context: Option<PathBuf>,
    _private: Option<(Flock<File>, tempfile::TempDir)>,
}

/// Names a source's build cache.
fn cache_key(source: &ImageSource) -> String {
    match source {
        ImageSource::Default => "default".into(),
        other => hash::combine([format!("{other:?}")])[..16].to_string(),
    }
}

/// Locks a directory without waiting: shared by the processes working in it
/// (the build and the builder machine's supervisor), exclusive to remove it.
fn lock_dir(path: &Path, exclusive: bool) -> io::Result<Flock<File>> {
    let f = File::open(path)?;
    let arg = if exclusive { FlockArg::LockExclusiveNonblock } else { FlockArg::LockSharedNonblock };
    Flock::lock(f, arg).map_err(|(_, e)| io::Error::from(e))
}

/// Opens a file the build left behind: a regular file, never followed
/// through a link, no larger than `max`.
fn open_boot_file(path: &Path, max: u64) -> io::Result<File> {
    let f = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc_flags::O_NOFOLLOW | libc_flags::O_NONBLOCK)
        .open(path)?;
    let meta = f.metadata()?;
    if !meta.is_file() || meta.len() > max {
        return Err(err(format!("the build left an invalid {}", path.display())));
    }
    Ok(f)
}

fn read_boot_file(path: &Path, max: u64) -> io::Result<String> {
    let mut s = String::new();
    open_boot_file(path, max)?.take(max).read_to_string(&mut s)?;
    Ok(s)
}

fn copy_boot_file(from: &Path, to: &Path, max: u64) -> io::Result<()> {
    let mut src = open_boot_file(from, max)?.take(max);
    let mut dst = std::fs::OpenOptions::new().write(true).create_new(true).mode(0o444).open(to)?;
    io::copy(&mut src, &mut dst)?;
    dst.sync_all()
}

mod libc_flags {
    pub const O_NOFOLLOW: i32 = nix::fcntl::OFlag::O_NOFOLLOW.bits();
    pub const O_NONBLOCK: i32 = nix::fcntl::OFlag::O_NONBLOCK.bits();
}

fn path_str(p: &Path) -> io::Result<String> {
    p.to_str().map(str::to_string).ok_or_else(|| err(format!("{} is not UTF-8", p.display())))
}

/// Extracts the runtime configuration from `buildah inspect --type image`.
pub fn parse_image_config(json: &str) -> ImageConfig {
    let v: serde_json::Value = serde_json::from_str(json).unwrap_or_default();
    let cfg = &v["OCIv1"]["config"];
    let strings = |x: &serde_json::Value| -> Vec<String> {
        x.as_array()
            .map(|a| a.iter().filter_map(|s| s.as_str().map(str::to_string)).collect())
            .unwrap_or_default()
    };
    ImageConfig {
        env: strings(&cfg["Env"]),
        user: cfg["User"].as_str().unwrap_or_default().to_string(),
        workdir: cfg["WorkingDir"].as_str().unwrap_or_default().to_string(),
        labels: cfg["Labels"]
            .as_object()
            .map(|m| m.iter().filter_map(|(k, v)| Some((k.clone(), v.as_str()?.to_string()))).collect())
            .unwrap_or_default(),
    }
}

async fn wait_ready(runtime: &MachineRuntime, supervisor: &mut tokio::process::Child) -> io::Result<()> {
    let deadline = tokio::time::Instant::now() + READY_TIMEOUT;
    loop {
        if let Ok(st) = MachineStatus::load(&runtime.status()) {
            match st.state {
                // A build needs its boot directory and context mounted.
                State::Ready => {
                    if let Some(e) = &st.error {
                        return Err(err(format!("the builder machine failed: {e}")));
                    }
                    if let Some(a) = st.attach.iter().find(|a| a.state != machine::AttachState::Ready) {
                        return Err(err(format!(
                            "the builder machine could not mount {}: {}",
                            a.at,
                            a.error.as_deref().unwrap_or("unknown error")
                        )));
                    }
                    return Ok(());
                }
                State::Failed => {
                    return Err(err(format!("the builder machine failed: {}", st.error.unwrap_or_default())));
                }
                _ => {}
            }
        }
        if let Ok(Some(status)) = supervisor.try_wait() {
            return Err(err(format!("the builder machine stopped ({status})")));
        }
        if tokio::time::Instant::now() >= deadline {
            return Err(err("the builder machine did not start in time"));
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
}

async fn control_call(s: &mut UnixStream, req: Request) -> io::Result<Response> {
    frame::send(s, &req).await?;
    Ok(frame::recv(s).await?)
}

/// An output sink that writes to the terminal and a log file.
pub fn console_and_log(log: std::fs::File) -> impl FnMut(&[u8], bool) + Send {
    let mut log = log;
    move |bytes, stderr| {
        let _ = log.write_all(bytes);
        if stderr {
            let _ = io::stderr().write_all(bytes);
        } else {
            let _ = io::stdout().write_all(bytes);
            let _ = io::stdout().flush();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn image_config_from_buildah() {
        let json = r#"{"OCIv1":{"config":{"Env":["PATH=/usr/bin","A=1"],"User":"dev","WorkingDir":"/w","Labels":{"dev.toby.adapted":"manual"}}}}"#;
        let cfg = parse_image_config(json);
        assert_eq!(cfg.env, vec!["PATH=/usr/bin", "A=1"]);
        assert_eq!(cfg.user, "dev");
        assert_eq!(cfg.workdir, "/w");
        assert_eq!(cfg.labels.get("dev.toby.adapted").map(String::as_str), Some("manual"));
        assert_eq!(parse_image_config("{}"), ImageConfig::default());
    }
}
