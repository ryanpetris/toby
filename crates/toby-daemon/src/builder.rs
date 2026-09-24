//! Builder machines: image builds, the one-time bootstrap and home
//! formatting (plan §15).

use std::io::{self, Write};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use toby_config::global::GlobalConfig;
use toby_config::machine::{
    self, Attach, Boot, Disk, MachineSpec, MachineStatus, Resources, RootSpec, State,
};
use toby_config::paths::{MachineRuntime, Paths};
use toby_proto::machine::{Request, Response};
use toby_proto::session::{ClientFrame, ServerFrame};
use toby_proto::stream::{HostHeader, Reply, SessionAttach};
use toby_proto::types::{ExitStatus, Identity, SUPPORTED, SpawnSpec};
use toby_proto::{frame, machine as mp, session};
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

/// Resources for builder machines: half the host's CPUs (2 to 8) and half
/// its memory, at most 8 GiB (plan §14.5).
pub fn builder_resources() -> Resources {
    let cpus = std::thread::available_parallelism().map(|n| n.get() as u32).unwrap_or(2);
    let mem = std::fs::read_to_string("/proc/meminfo")
        .ok()
        .and_then(|m| {
            m.lines()
                .find(|l| l.starts_with("MemTotal:"))
                .and_then(|l| l.split_whitespace().nth(1)?.parse::<u64>().ok())
        })
        .map(|kib| (kib * 1024 / 2).min(8 << 30))
        .unwrap_or(4 << 30);
    Resources { cpus: (cpus / 2).clamp(2, 8), memory: format!("{}M", mem >> 20) }
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
        let versions = self.config.programs.versions();
        std::fs::read_link(versions.join("current"))
            .ok()
            .and_then(|t| t.file_name().map(|n| n.to_string_lossy().into_owned()))
            .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_string())
    }

    fn builder_dir(&self) -> PathBuf {
        self.paths.data.join("builder").join(arch())
    }

    pub fn bootstrap_image(&self) -> PathBuf {
        self.builder_dir().join("bootstrap-debian-13.qcow2")
    }

    async fn cache_disk(&self) -> io::Result<PathBuf> {
        let path = self.builder_dir().join("cache.qcow2");
        if !path.exists() {
            qcow2::create(&path, CACHE_SIZE, None).await?;
        }
        Ok(path)
    }

    /// Hash of the bundled default image configuration and mkosi release.
    pub fn default_source_hash(&self) -> io::Result<String> {
        let share = self.config.programs.share();
        let conf = hash::hash_tree(&share.join("images/default"))?;
        let mkosi = hash::hash_file(&share.join("mkosi/pyproject.toml"))
            .or_else(|_| hash::hash_tree(&share.join("mkosi/mkosi")))?;
        Ok(hash::combine([conf, mkosi, ADAPTATION_VERSION.to_string()]))
    }

    /// The current default image, if one has been built from the bundled
    /// configuration.
    pub fn default_image(&self) -> io::Result<Option<ImageRecord>> {
        let hash = self.default_source_hash()?;
        Ok(self
            .store
            .images()?
            .into_iter()
            .filter(|i| i.source == ImageSource::Default && i.arch == arch())
            .rfind(|i| i.source_hash == hash && i.adaptation_version == ADAPTATION_VERSION))
    }

    /// Any default image, current or not, to boot a builder with.
    fn any_default_image(&self) -> io::Result<Option<ImageRecord>> {
        Ok(self.store.images()?.into_iter().rfind(|i| i.source == ImageSource::Default && i.arch == arch()))
    }

    /// Runs a builder machine until its jobs finish.
    async fn run_machine(
        &self,
        spec: MachineSpec,
        jobs: Vec<Vec<String>>,
        out: Output<'_>,
    ) -> io::Result<()> {
        let id = spec.id.clone();
        let desired = self.paths.machine_desired(&id);
        spec.store(&desired)?;
        let runtime = self.paths.machine_runtime(&id);
        std::fs::create_dir_all(&runtime.dir)?;

        let log = std::fs::File::create(runtime.dir.join("supervisor.log"))?;
        let mut supervisor = tokio::process::Command::new(&self.exe)
            .args(["internal", "machine", "--supervise", "--machine", &id])
            .stdin(Stdio::null())
            .stdout(log.try_clone()?)
            .stderr(log)
            .kill_on_drop(true)
            .spawn()?;

        let result = async {
            wait_ready(&runtime, &mut supervisor).await?;
            for argv in jobs {
                let status = run_job(&runtime, argv, &mut *out).await?;
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
            let _ = supervisor.kill().await;
        }
        // Keep the logs of a failed build for inspection.
        let _ = std::fs::remove_dir_all(self.paths.machine_state_dir(&id));
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
            resources: builder_resources(),
            boot: Boot { image: boot },
            disk: disks,
            attach,
            forward: Vec::new(),
            capabilities: Default::default(),
        }
    }

    /// Builds an image from `source` (plan §15.3).
    pub async fn build(&self, source: ImageSource, out: Output<'_>) -> io::Result<ImageRecord> {
        let base = self.any_default_image()?.ok_or_else(|| {
            err("there is no default image to build with; run `toby image prepare --default` first")
        })?;
        let (kind_args, context, source_hash) = self.job_for(&source)?;
        let root = RootSpec::Image { image: base.id.clone() };
        self.build_with(root, Some(base.id), source, kind_args, context, source_hash, Vec::new(), out).await
    }

    /// Describes the build job for a source: its arguments, the directory
    /// attached as the build context, and the source hash.
    fn job_for(&self, source: &ImageSource) -> io::Result<(Vec<String>, Option<PathBuf>, String)> {
        let name_in = |dir: &Path, file: &Path| -> io::Result<String> {
            let rel = file.strip_prefix(dir).map_err(|_| {
                err(format!("{} must be inside the build context {}", file.display(), dir.display()))
            })?;
            Ok(rel.to_string_lossy().into_owned())
        };
        Ok(match source {
            ImageSource::Default => (vec!["default".into()], None, self.default_source_hash()?),
            ImageSource::Mkosi { path } => {
                let hash = hash::combine([hash::hash_tree(path)?, self.default_source_hash()?]);
                (vec!["mkosi".into(), ".".into()], Some(path.clone()), hash)
            }
            ImageSource::Dockerfile { path, context } => {
                let file = name_in(context, path)?;
                let hash = hash::combine([hash::hash_file(path)?, hash::hash_tree(context)?]);
                (vec!["dockerfile".into(), file], Some(context.clone()), hash)
            }
            ImageSource::Registry { reference } => {
                (vec!["registry".into(), reference.clone()], None, hash::combine([reference.as_str()]))
            }
            ImageSource::Archive { path } => {
                let dir = path.parent().ok_or_else(|| err("invalid archive path"))?;
                let file = name_in(dir, path)?;
                (vec!["archive".into(), file], Some(dir.to_path_buf()), hash::hash_file(path)?)
            }
        })
    }

    #[allow(clippy::too_many_arguments)]
    async fn build_with(
        &self,
        root: RootSpec,
        boot: Option<String>,
        source: ImageSource,
        kind_args: Vec<String>,
        context: Option<PathBuf>,
        source_hash: String,
        before: Vec<Vec<String>>,
        out: Output<'_>,
    ) -> io::Result<ImageRecord> {
        let id = toby_config::new_id();
        let final_dir = self.paths.image_dir(&id);
        let work = final_dir.with_extension("tmp");
        let boot_dir = work.join("boot");
        std::fs::create_dir_all(&boot_dir)?;
        let disk = work.join("disk.qcow2");
        qcow2::create(&disk, toby_store::store::IMAGE_SIZE, None).await?;

        let mut disks =
            vec![Disk { path: self.cache_disk().await?, serial: "cache".into(), read_only: false }];
        disks.push(Disk { path: disk.clone(), serial: "out".into(), read_only: false });
        let mut attach = vec![Attach {
            id: "boot".into(),
            host: path_str(&boot_dir)?,
            at: "/build/boot".into(),
            read_only: false,
            pinned: false,
        }];
        if let Some(ctx) = &context {
            attach.push(Attach {
                id: "context".into(),
                host: path_str(ctx)?,
                at: "/build/context".into(),
                read_only: true,
                pinned: false,
            });
        }

        let version = self.runtime_version();
        let mut args: Vec<&str> = vec!["build", &id];
        args.extend(kind_args.iter().map(|s| s.as_str()));
        let mut jobs = before;
        jobs.push(helper(&version, &args));

        let spec = self.builder_spec(root, boot, disks, attach);
        let result = self.run_machine(spec, jobs, out).await;
        if let Err(e) = result {
            let _ = std::fs::remove_dir_all(&work);
            return Err(e);
        }

        let read = |name: &str| std::fs::read_to_string(boot_dir.join(name)).map(|s| s.trim().to_string());
        let kernel_version = read("kernel-version")?;
        let config = parse_image_config(&read("config.json").unwrap_or_default());
        for (from, to) in [("vmlinuz", "vmlinuz"), ("initramfs.img", "initramfs.img")] {
            std::fs::rename(boot_dir.join(from), work.join(to))?;
        }
        std::fs::remove_dir_all(&boot_dir)?;
        for f in ["disk.qcow2", "vmlinuz", "initramfs.img"] {
            std::fs::set_permissions(work.join(f), std::fs::Permissions::from_mode(0o444))?;
        }
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

    /// Downloads the Debian 13 cloud image used once to build the first
    /// default image, checked against Debian's published SHA512SUMS.
    pub fn download_bootstrap(&self, out: Output<'_>) -> io::Result<PathBuf> {
        let target = self.bootstrap_image();
        if target.exists() {
            return Ok(target);
        }
        std::fs::create_dir_all(self.builder_dir())?;
        let base = "https://cloud.debian.org/images/cloud/trixie/latest";
        let name = format!("debian-13-genericcloud-{}.qcow2", debian_arch());

        out(format!("==> Downloading {name}\n").as_bytes(), false);
        let sums = crate::download::text(&format!("{base}/SHA512SUMS"))?;
        let expected = sums
            .lines()
            .find_map(|l| {
                let (sum, file) = l.split_once(char::is_whitespace)?;
                (file.trim().trim_start_matches('*') == name).then(|| sum.to_string())
            })
            .ok_or_else(|| err(format!("{name} is not listed in SHA512SUMS")))?;
        let part = target.with_extension("part");
        let actual = crate::download::file(&format!("{base}/{name}"), &part)?;
        if actual != expected {
            let _ = std::fs::remove_file(&part);
            return Err(err(format!("{name} does not match its published SHA512 checksum")));
        }
        std::fs::rename(&part, &target)?;
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
            None => self.download_bootstrap(&mut *out)?,
        };
        let version = self.runtime_version();
        let (kind_args, _, hash) = self.job_for(&ImageSource::Default)?;
        let root = RootSpec::CloudImage { cloud_image: cloud };
        self.build_with(
            root,
            None,
            ImageSource::Default,
            kind_args,
            None,
            hash,
            vec![helper(&version, &["provision"])],
            out,
        )
        .await
    }

    /// Makes sure the current default image exists, bootstrapping or
    /// rebuilding it as needed.
    pub async fn prepare_default(&self, rebuild: bool, out: Output<'_>) -> io::Result<ImageRecord> {
        if !rebuild && let Some(img) = self.default_image()? {
            return Ok(img);
        }
        match self.any_default_image()? {
            Some(_) => self.build(ImageSource::Default, out).await,
            None => self.bootstrap(None, out).await,
        }
    }

    /// Formats a new home's disk with ext4 in a builder machine.
    pub async fn format_home(&self, name: &str, out: Output<'_>) -> io::Result<()> {
        let mut home = self.store.home(name)?;
        let base = self.any_default_image()?.ok_or_else(|| {
            err("there is no default image to format homes with; run `toby image prepare --default` first")
        })?;
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
                State::Ready => return Ok(()),
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

/// Runs one command in the builder as root and streams its output.
async fn run_job(runtime: &MachineRuntime, argv: Vec<String>, out: Output<'_>) -> io::Result<ExitStatus> {
    let mut c = UnixStream::connect(runtime.control_sock()).await?;
    match control_call(&mut c, Request::Hello(mp::Hello { versions: SUPPORTED.to_vec() })).await? {
        Response::Welcome(_) => {}
        other => return Err(err(format!("unexpected response {other:?}"))),
    }
    let spec = SpawnSpec {
        session_id: toby_config::new_id(),
        argv,
        env: Vec::new(),
        cwd: Some("/".into()),
        identity: Identity::Root,
        tty: None,
        keep_after_exit: true,
        start_on_attach: true,
    };
    let id = spec.session_id.clone();
    match control_call(&mut c, Request::Spawn(mp::Spawn { spec })).await? {
        Response::Spawned(_) => {}
        Response::Failed(f) => return Err(err(f.error)),
        other => return Err(err(format!("unexpected response {other:?}"))),
    }

    let mut s = UnixStream::connect(runtime.session_sock()).await?;
    frame::send(&mut s, &HostHeader::SessionAttach(SessionAttach { session_id: id })).await?;
    frame::recv::<Reply, _>(&mut s).await?.into_result().map_err(err)?;
    let hello = ClientFrame::Hello(session::Hello {
        versions: SUPPORTED.to_vec(),
        rows: 0,
        cols: 0,
        want_replay: true,
        resume_from: None,
    });
    frame::send(&mut s, &hello).await?;
    loop {
        match frame::recv::<ServerFrame, _>(&mut s).await? {
            ServerFrame::Stdout(o) => out(&o.bytes, false),
            ServerFrame::Stderr(e) => out(&e.bytes, true),
            ServerFrame::Replay(r) => out(&r.bytes, r.stderr),
            ServerFrame::Exit(e) => return Ok(e.status),
            ServerFrame::Refused(r) => return Err(err(r.error)),
            _ => {}
        }
    }
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

    #[test]
    fn builder_resources_are_bounded() {
        let r = builder_resources();
        assert!((2..=8).contains(&r.cpus));
        assert!(toby_config::machine::parse_size(&r.memory).unwrap() <= 8 << 30);
    }
}
