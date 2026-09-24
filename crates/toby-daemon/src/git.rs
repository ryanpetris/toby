//! Git fetch and push with the host's credentials for the Toby MCP server
//! (plan §16.4). A project's repository is the guest's to write: its config
//! and hooks can run commands, and its paths can lead anywhere on the host.
//! So git never runs in it. Each action uses a private bare repository whose
//! config the host writes, holding the project's packs and loose objects as
//! hard links (or copies); tobyd itself reads the project's config, HEAD,
//! refs and object files and writes back refs and packs, resolving every
//! path beneath the repository's directory without following links.

use std::io::{self, Read, Write};
use std::os::fd::{AsFd, AsRawFd, BorrowedFd, OwnedFd};
use std::path::{Component, Path, PathBuf};
use std::process::Output;
use std::time::Duration;

use nix::fcntl::{OFlag, OpenHow, ResolveFlag, openat2};
use nix::sys::stat::Mode;
use toby_config::machine::MachineSpec;

/// Longest a git command may run.
const TIMEOUT: Duration = Duration::from_secs(300);
/// Largest repository file read (config, HEAD, a ref, packed-refs).
const MAX_FILE: u64 = 16 << 20;
/// Most refs taken from the project.
const MAX_REFS: usize = 100_000;

/// Settings every command runs with, over any config.
const OVERRIDES: &[&str] = &[
    "core.hooksPath=/dev/null",
    "protocol.allow=never",
    "protocol.https.allow=always",
    "protocol.ssh.allow=always",
    "protocol.http.allow=never",
    "protocol.git.allow=never",
    "protocol.file.allow=never",
    "protocol.ext.allow=never",
    "transfer.unpackLimit=1",
    "fetch.unpackLimit=1",
    "submodule.recurse=false",
    "fetch.recurseSubmodules=false",
    "push.recurseSubmodules=no",
    "gc.auto=0",
    "maintenance.auto=false",
    "fetch.writeCommitGraph=false",
];

pub struct Remote {
    pub name: String,
    url: Option<String>,
    push_url: Option<String>,
}

impl Remote {
    /// Where fetches go.
    pub fn url(&self) -> &str {
        self.url.as_deref().unwrap_or("")
    }

    /// Where pushes go: the first push URL, or the URL.
    pub fn push_url(&self) -> &str {
        self.push_url.as_deref().unwrap_or(self.url())
    }
}

/// A project's repository, pinned when it was opened.
pub struct Repo {
    pub work_tree: PathBuf,
    /// The repository's own directory (HEAD).
    git_dir: OwnedFd,
    /// Its common directory (config, refs, objects).
    common: OwnedFd,
    object_format: String,
    remotes: Vec<Remote>,
    /// The project is mounted read-only.
    pub read_only: bool,
}

fn err(msg: impl Into<String>) -> io::Error {
    io::Error::other(msg.into())
}

fn beneath(flags: OFlag) -> OpenHow {
    let how = OpenHow::new()
        .flags(flags | OFlag::O_CLOEXEC)
        .resolve(ResolveFlag::RESOLVE_BENEATH | ResolveFlag::RESOLVE_NO_SYMLINKS);
    // A mode is only allowed when creating.
    if flags.contains(OFlag::O_CREAT) { how.mode(Mode::from_bits_truncate(0o644)) } else { how }
}

/// Opens a directory beneath `dir`, following no links.
fn open_dir(dir: BorrowedFd, path: &str) -> io::Result<OwnedFd> {
    Ok(openat2(dir, path, beneath(OFlag::O_RDONLY | OFlag::O_DIRECTORY))?)
}

/// Opens a regular file beneath `dir`, following no links; `None` when it
/// does not exist.
fn open_file(dir: BorrowedFd, path: &str) -> io::Result<Option<std::fs::File>> {
    let fd = match openat2(dir, path, beneath(OFlag::O_RDONLY | OFlag::O_NONBLOCK)) {
        Ok(fd) => fd,
        Err(nix::errno::Errno::ENOENT) => return Ok(None),
        Err(e) => return Err(err(format!("{path}: {e}"))),
    };
    let f = std::fs::File::from(fd);
    if !f.metadata()?.is_file() {
        return Err(err(format!("{path} is not a file")));
    }
    Ok(Some(f))
}

/// Reads a regular file beneath `dir`; `None` when it does not exist.
fn read_file(dir: BorrowedFd, path: &str) -> io::Result<Option<Vec<u8>>> {
    let Some(mut f) = open_file(dir, path)? else { return Ok(None) };
    let meta = f.metadata()?;
    if meta.len() > MAX_FILE {
        return Err(err(format!("{path} is too large")));
    }
    let mut out = Vec::new();
    (&mut f).take(MAX_FILE).read_to_end(&mut out)?;
    Ok(Some(out))
}

/// Opens (creating) the directories of `path` beneath `dir`.
fn make_dirs(dir: BorrowedFd, path: &str) -> io::Result<OwnedFd> {
    let mut current = open_dir(dir, ".")?;
    for part in path.split('/').filter(|p| !p.is_empty()) {
        match open_dir(current.as_fd(), part) {
            Ok(next) => current = next,
            Err(_) => {
                match nix::sys::stat::mkdirat(current.as_fd(), part, Mode::from_bits_truncate(0o755)) {
                    Ok(()) | Err(nix::errno::Errno::EEXIST) => {}
                    Err(e) => return Err(err(format!("{path}: {e}"))),
                }
                current = open_dir(current.as_fd(), part)?;
            }
        }
    }
    Ok(current)
}

/// Writes a file beneath `dir` in one step (a new file renamed into place).
fn write_file(dir: BorrowedFd, path: &str, content: &[u8]) -> io::Result<()> {
    write_from(dir, path, &mut &content[..])
}

/// Like `write_file`, with the content read from `from`.
fn write_from(dir: BorrowedFd, path: &str, from: &mut dyn Read) -> io::Result<()> {
    let (parent, name) = match path.rsplit_once('/') {
        Some((p, n)) => (make_dirs(dir, p)?, n),
        None => (open_dir(dir, ".")?, path),
    };
    let tmp = format!(".{name}.toby-{}", toby_config::new_id().to_lowercase());
    let fd =
        openat2(parent.as_fd(), tmp.as_str(), beneath(OFlag::O_WRONLY | OFlag::O_CREAT | OFlag::O_EXCL))?;
    let mut f = std::fs::File::from(fd);
    let written = io::copy(from, &mut f).and_then(|_| f.sync_all());
    let renamed = written.and_then(|()| {
        nix::fcntl::renameat(parent.as_fd(), tmp.as_str(), parent.as_fd(), name).map_err(io::Error::from)
    });
    if renamed.is_err() {
        let _ = nix::unistd::unlinkat(parent.as_fd(), tmp.as_str(), nix::unistd::UnlinkatFlags::NoRemoveDir);
    }
    renamed
}

/// Opens a directory by its canonical path and checks the descriptor is
/// that directory.
fn pin(path: &Path) -> io::Result<OwnedFd> {
    let fd = nix::fcntl::open(path, OFlag::O_RDONLY | OFlag::O_DIRECTORY | OFlag::O_CLOEXEC, Mode::empty())?;
    let actual = std::fs::read_link(format!("/proc/self/fd/{}", fd.as_raw_fd()))?;
    if actual != path {
        return Err(err(format!("{} changed while it was opened", path.display())));
    }
    Ok(fd)
}

fn canonical(p: &Path) -> Result<PathBuf, String> {
    std::fs::canonicalize(p).map_err(|e| format!("{}: {e}", p.display()))
}

/// Opens the repository of the project holding `guest`, a path in the
/// machine. `guest_roots` are the host directories of every attachment of
/// every machine: whatever lies beneath them a guest may have written.
pub fn open(spec: &MachineSpec, guest_roots: &[PathBuf], guest: &str) -> Result<Repo, String> {
    let g = Path::new(guest);
    if !g.is_absolute() || g.components().any(|c| matches!(c, Component::ParentDir)) {
        return Err(format!("{guest} must be an absolute path without .."));
    }
    let (attach, rest) = spec
        .attach
        .iter()
        .filter_map(|a| g.strip_prefix(&a.at).ok().map(|rest| (a, rest)))
        .max_by_key(|(a, _)| Path::new(&a.at).components().count())
        .ok_or_else(|| format!("{guest} is not inside a mounted project"))?;
    let root = canonical(Path::new(&attach.host))?;
    let dir = canonical(&root.join(rest)).map_err(|_| format!("{guest} does not exist"))?;
    if !dir.starts_with(&root) {
        return Err(format!("{guest} leads outside its project"));
    }
    let guest_written = |p: &Path| p.starts_with(&root) || guest_roots.iter().any(|r| p.starts_with(r));
    let outside = || format!("the repository of {guest} is outside its project");

    // The repository is inside the project.
    let top = dir
        .ancestors()
        .take_while(|d| d.starts_with(&root))
        .find(|d| d.join(".git").symlink_metadata().is_ok())
        .ok_or_else(|| format!("{guest} is not in a git repository inside its project"))?
        .to_path_buf();
    let dot_git = top.join(".git");
    let git_dir = if dot_git.is_dir() {
        canonical(&dot_git)?
    } else {
        let text = std::fs::read_to_string(&dot_git).map_err(|e| format!("{}: {e}", dot_git.display()))?;
        let target = text
            .strip_prefix("gitdir:")
            .map(str::trim)
            .ok_or_else(|| format!("{} is not a git directory reference", dot_git.display()))?;
        canonical(&top.join(target))?
    };
    let inside = git_dir.starts_with(&root);
    if !inside {
        // A worktree of a repository no guest can write: that repository's
        // own record of the worktree has to name this one.
        if guest_written(&git_dir) {
            return Err(outside());
        }
        let back = std::fs::read_to_string(git_dir.join("gitdir")).map_err(|_| outside())?;
        if canonical(Path::new(back.trim())).ok() != canonical(&dot_git).ok() {
            return Err(outside());
        }
    }
    let git_fd = pin(&git_dir).map_err(|e| e.to_string())?;
    let common = match read_file(git_fd.as_fd(), "commondir").map_err(|e| e.to_string())? {
        Some(c) => {
            let c = canonical(&git_dir.join(String::from_utf8_lossy(&c).trim()))?;
            if (inside && !c.starts_with(&root)) || (!inside && guest_written(&c)) {
                return Err(outside());
            }
            c
        }
        None => git_dir.clone(),
    };
    let common_fd = pin(&common).map_err(|e| e.to_string())?;
    let config = read_file(common_fd.as_fd(), "config").map_err(|e| e.to_string())?.unwrap_or_default();
    let entries = parse_config(&config)?;
    let get = |key: &str| entries.iter().find(|(k, _)| k.eq_ignore_ascii_case(key)).map(|(_, v)| v.as_str());
    if get("extensions.refstorage").is_some_and(|v| !v.eq_ignore_ascii_case("files")) {
        return Err("repositories with reftable refs are not supported".into());
    }
    let object_format = get("extensions.objectformat").unwrap_or("sha1").to_ascii_lowercase();
    if object_format != "sha1" && object_format != "sha256" {
        return Err(format!("unknown object format {object_format}"));
    }
    Ok(Repo {
        work_tree: top,
        git_dir: git_fd,
        common: common_fd,
        object_format,
        remotes: remotes(&entries),
        read_only: attach.read_only,
    })
}

/// Parses a config file's contents with `git config`, which only reads it.
fn parse_config(text: &[u8]) -> Result<Vec<(String, String)>, String> {
    let mut child = std::process::Command::new("git")
        .args(["config", "--file", "-", "--no-includes", "--null", "--list"])
        .current_dir("/")
        .env_clear()
        .envs(clean_env())
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .map_err(|e| format!("running git: {e}"))?;
    let mut stdin = child.stdin.take().expect("piped");
    let text = text.to_vec();
    let writer = std::thread::spawn(move || stdin.write_all(&text));
    let out = child.wait_with_output().map_err(|e| format!("running git: {e}"))?;
    let _ = writer.join();
    if !out.status.success() {
        return Err(format!("the repository's config: {}", String::from_utf8_lossy(&out.stderr).trim()));
    }
    Ok(out
        .stdout
        .split(|b| *b == 0)
        .filter(|e| !e.is_empty())
        .map(|e| {
            let e = String::from_utf8_lossy(e);
            match e.split_once('\n') {
                Some((k, v)) => (k.to_string(), v.to_string()),
                None => (e.to_string(), String::new()),
            }
        })
        .collect())
}

fn remotes(entries: &[(String, String)]) -> Vec<Remote> {
    let mut out: Vec<Remote> = Vec::new();
    for (k, v) in entries {
        let Some((sub, key)) = k.strip_prefix("remote.").and_then(|r| r.rsplit_once('.')) else { continue };
        let key = key.to_ascii_lowercase();
        if key != "url" && key != "pushurl" {
            continue;
        }
        let i = match out.iter().position(|r| r.name == sub) {
            Some(i) => i,
            None => {
                out.push(Remote { name: sub.to_string(), url: None, push_url: None });
                out.len() - 1
            }
        };
        let slot = if key == "url" { &mut out[i].url } else { &mut out[i].push_url };
        slot.get_or_insert_with(|| v.clone());
    }
    out.retain(|r| r.url.is_some());
    out
}

/// The environment git runs with: the daemon's, without git's own
/// variables, and never prompting.
fn clean_env() -> Vec<(std::ffi::OsString, std::ffi::OsString)> {
    let mut env: Vec<_> =
        std::env::vars_os().filter(|(k, _)| !k.as_encoded_bytes().starts_with(b"GIT_")).collect();
    env.push(("GIT_TERMINAL_PROMPT".into(), "0".into()));
    env
}

/// A branch or remote name: no options, refspec syntax or revision syntax.
pub fn name(s: &str) -> Result<&str, String> {
    let bad = s.is_empty()
        || s.starts_with(['-', '/', '.', '+'])
        || s.ends_with(['/', '.'])
        || s.ends_with(".lock")
        || s.contains("..")
        || s.contains("//")
        || s.contains("/.")
        || s.contains("@{")
        || s == "@"
        || s.chars().any(|c| c.is_control() || c.is_whitespace() || "~^:?*[\\".contains(c));
    if bad { Err(format!("{s:?} is not a valid name")) } else { Ok(s) }
}

/// A remote URL git may use: `https://`, `ssh://` or `host:path`.
fn url(u: &str) -> Result<&str, String> {
    #[cfg(test)]
    if tests::LOCAL_REMOTES.get() && u.starts_with('/') {
        return Ok(u);
    }
    let scp = u.split_once(':').is_some_and(|(host, path)| {
        !host.is_empty() && !host.contains('/') && !path.starts_with("//") && !path.is_empty()
    });
    let ok = !u.starts_with('-')
        && !u.contains("::")
        && !u.chars().any(|c| c.is_control() || c.is_whitespace())
        && (u.starts_with("https://") || u.starts_with("ssh://") || scp);
    if ok { Ok(u) } else { Err(format!("the remote URL {u:?} is not an https or ssh URL")) }
}

fn object_id(s: &str) -> bool {
    matches!(s.len(), 40 | 64) && s.bytes().all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
}

impl Repo {
    /// A configured remote; `origin` when none is named.
    pub fn remote(&self, name: Option<&str>) -> Result<&Remote, String> {
        let name = name.unwrap_or("origin");
        self.remotes
            .iter()
            .find(|r| r.name == name)
            .ok_or_else(|| format!("the repository has no remote {name}"))
    }

    /// The checked-out branch.
    pub fn branch(&self) -> Result<String, String> {
        let head = read_file(self.git_dir.as_fd(), "HEAD").map_err(|e| e.to_string())?.unwrap_or_default();
        let head = String::from_utf8_lossy(&head);
        let branch = head.trim().strip_prefix("ref: refs/heads/").ok_or("no branch is checked out")?;
        name(branch).map(str::to_string)
    }

    /// The object a ref names, from its file or `packed-refs`.
    pub fn resolve(&self, refname: &str) -> Result<String, String> {
        if let Some(v) = read_file(self.common.as_fd(), refname).map_err(|e| e.to_string())? {
            let v = String::from_utf8_lossy(&v).trim().to_string();
            return if object_id(&v) { Ok(v) } else { Err(format!("{refname} is not an object ID")) };
        }
        self.packed_refs()?
            .into_iter()
            .find(|(_, r)| r == refname)
            .map(|(id, _)| id)
            .ok_or_else(|| format!("there is no {refname}"))
    }

    fn packed_refs(&self) -> Result<Vec<(String, String)>, String> {
        let text =
            read_file(self.common.as_fd(), "packed-refs").map_err(|e| e.to_string())?.unwrap_or_default();
        Ok(String::from_utf8_lossy(&text)
            .lines()
            .filter_map(|l| l.split_once(' '))
            .filter(|(id, r)| object_id(id) && r.starts_with("refs/"))
            .map(|(id, r)| (id.to_string(), r.to_string()))
            .take(MAX_REFS)
            .collect())
    }

    /// Refs under `prefix` (loose and packed), loose ones first.
    fn refs(&self, prefix: &str) -> Vec<(String, String)> {
        let mut out = Vec::new();
        let mut dirs = vec![prefix.trim_end_matches('/').to_string()];
        while let Some(d) = dirs.pop() {
            let Ok(fd) = open_dir(self.common.as_fd(), &d) else { continue };
            let Ok(entries) = std::fs::read_dir(format!("/proc/self/fd/{}", fd.as_raw_fd())) else {
                continue;
            };
            for e in entries.flatten() {
                let n = e.file_name().to_string_lossy().to_string();
                if out.len() >= MAX_REFS {
                    break;
                }
                let path = format!("{d}/{n}");
                match e.file_type() {
                    Ok(t) if t.is_dir() => dirs.push(path),
                    Ok(t) if t.is_file() => {
                        if let Ok(id) = self.resolve(&path) {
                            out.push((id, path));
                        }
                    }
                    _ => {}
                }
            }
        }
        let packed = self.packed_refs().unwrap_or_default();
        for (id, r) in packed.into_iter().filter(|(_, r)| r.starts_with(prefix)) {
            if !out.iter().any(|(_, o)| *o == r) {
                out.push((id, r));
            }
        }
        out
    }

    /// Fetches `remote`'s branches into `refs/remotes/<remote>/`, with the
    /// objects written into the project as packs. Returns git's output.
    pub async fn fetch(&self, scratch: &Path, remote: &Remote) -> Result<String, String> {
        let private = Private::new(self, scratch).await?;
        let mut known = self.refs("refs/heads/");
        known.extend(self.refs(&format!("refs/remotes/{}/", remote.name)));
        private.set_refs(&known).await?;
        let refspec = format!("+refs/heads/*:refs/remotes/{}/*", remote.name);
        let out = private
            .run(&["fetch", "--no-tags", "--no-write-fetch-head", "--", url(remote.url())?, &refspec])
            .await?;
        let log = output(&out);
        if !out.status.success() {
            return Err(log);
        }
        // Packs first, so the refs never name missing objects.
        let objects = open_dir(self.common.as_fd(), "objects").map_err(|e| e.to_string())?;
        let mut packs: Vec<PathBuf> = std::fs::read_dir(private.dir.path().join("objects/pack"))
            .map_err(|e| e.to_string())?
            .flatten()
            .map(|e| e.path())
            .filter(|p| p.extension().is_some_and(|x| x == "pack" || x == "rev" || x == "idx"))
            .collect();
        // Git finds a pack by its index, which goes last.
        packs.sort_by_key(|p| p.extension().is_some_and(|x| x == "idx"));
        for p in packs {
            let name = p.file_name().and_then(|n| n.to_str()).ok_or("invalid pack name")?.to_string();
            // Only packs the fetch made.
            if private.taken.contains(&name) {
                continue;
            }
            let mut data = std::fs::File::open(&p).map_err(|e| e.to_string())?;
            write_from(objects.as_fd(), &format!("pack/{name}"), &mut data)
                .map_err(|e| format!("writing {name}: {e}"))?;
        }
        let prefix = format!("refs/remotes/{}/", remote.name);
        for (id, refname) in private.refs(&prefix).await? {
            let branch = refname.strip_prefix(&prefix).unwrap_or_default();
            if name(branch).is_err() || known.iter().any(|(k, r)| *r == refname && *k == id) {
                continue;
            }
            write_file(self.common.as_fd(), &refname, format!("{id}\n").as_bytes())
                .map_err(|e| format!("writing {refname}: {e}"))?;
        }
        Ok(log)
    }

    /// Pushes `id` to `branch` at `remote`'s push URL and records it as
    /// the remote-tracking branch.
    pub async fn push(
        &self,
        scratch: &Path,
        remote: &Remote,
        branch: &str,
        id: &str,
    ) -> Result<String, String> {
        let private = Private::new(self, scratch).await?;
        let refspec = format!("{id}:refs/heads/{branch}");
        let out = private.run(&["push", "--no-verify", "--", url(remote.push_url())?, &refspec]).await?;
        let log = output(&out);
        if !out.status.success() {
            return Err(log);
        }
        if !self.read_only {
            let tracking = format!("refs/remotes/{}/{branch}", remote.name);
            let _ = write_file(self.common.as_fd(), &tracking, format!("{id}\n").as_bytes());
        }
        Ok(log)
    }
}

fn output(o: &Output) -> String {
    let mut s = String::from_utf8_lossy(&o.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&o.stderr));
    s
}

/// A private bare repository with the project's objects.
struct Private {
    dir: tempfile::TempDir,
    /// The project's packs, taken in.
    taken: std::collections::HashSet<String>,
}

/// Puts the open file `f` at `dest`: a hard link to it, or a copy on
/// another file system.
fn take(f: &mut std::fs::File, dest: &Path) -> io::Result<()> {
    let src = format!("/proc/self/fd/{}", f.as_raw_fd());
    let cwd = nix::fcntl::AT_FDCWD;
    if nix::unistd::linkat(cwd, src.as_str(), cwd, dest, nix::fcntl::AtFlags::AT_SYMLINK_FOLLOW).is_ok() {
        return Ok(());
    }
    let mut out = std::fs::File::create_new(dest)?;
    io::copy(f, &mut out).map(drop)
}

fn hex(s: &str) -> bool {
    !s.is_empty() && s.bytes().all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
}

impl Private {
    async fn new(repo: &Repo, scratch: &Path) -> Result<Private, String> {
        std::fs::create_dir_all(scratch).map_err(|e| e.to_string())?;
        let dir = tempfile::Builder::new().prefix("git-").tempdir_in(scratch).map_err(|e| e.to_string())?;
        let mut p = Private { dir, taken: Default::default() };
        let format = format!("--object-format={}", repo.object_format);
        let out = p.run(&["init", "-q", "--bare", &format]).await?;
        if !out.status.success() {
            return Err(output(&out));
        }
        p.take_objects(repo).map_err(|e| format!("reading the project's objects: {e}"))?;
        Ok(p)
    }

    /// Links the project's packs and loose objects in, each file opened
    /// beneath the objects directory without following links. Nothing else
    /// (such as `info/alternates`) is taken.
    fn take_objects(&mut self, repo: &Repo) -> io::Result<()> {
        let objects = open_dir(repo.common.as_fd(), "objects")?;
        let dest = self.dir.path().join("objects");
        let list = |dir: &OwnedFd| -> io::Result<Vec<(String, bool)>> {
            Ok(std::fs::read_dir(format!("/proc/self/fd/{}", dir.as_raw_fd()))?
                .flatten()
                .filter_map(|e| Some((e.file_name().into_string().ok()?, e.file_type().ok()?.is_dir())))
                .collect())
        };
        if let Ok(pack) = open_dir(objects.as_fd(), "pack") {
            for (name, is_dir) in list(&pack)? {
                let wanted = [".pack", ".idx", ".rev"].iter().any(|x| name.ends_with(x));
                if is_dir || !name.starts_with("pack-") || !wanted {
                    continue;
                }
                if let Some(mut f) = open_file(pack.as_fd(), &name)? {
                    take(&mut f, &dest.join("pack").join(&name))?;
                    self.taken.insert(name);
                }
            }
        }
        for (sub, is_dir) in list(&objects)? {
            if !is_dir || sub.len() != 2 || !hex(&sub) {
                continue;
            }
            let src = open_dir(objects.as_fd(), &sub)?;
            let target = dest.join(&sub);
            std::fs::create_dir_all(&target)?;
            for (name, is_dir) in list(&src)? {
                if is_dir || !matches!(name.len(), 38 | 62) || !hex(&name) {
                    continue;
                }
                if let Some(mut f) = open_file(src.as_fd(), &name)? {
                    take(&mut f, &target.join(&name))?;
                }
            }
        }
        Ok(())
    }

    /// Refs the fetch can tell the remote it has; objects that are not
    /// there are left out.
    async fn set_refs(&self, refs: &[(String, String)]) -> Result<(), String> {
        let ids: String = refs.iter().map(|(id, _)| format!("{id}\n")).collect();
        let out = self
            .run_with(&["cat-file", "--batch-check=%(objectname) %(objecttype)"], ids.into_bytes())
            .await?;
        let commits: std::collections::HashSet<String> = String::from_utf8_lossy(&out.stdout)
            .lines()
            .filter_map(|l| l.strip_suffix(" commit"))
            .map(str::to_string)
            .collect();
        let mut kept: Vec<&(String, String)> = refs.iter().filter(|(id, _)| commits.contains(id)).collect();
        kept.sort_by(|a, b| a.1.cmp(&b.1));
        kept.dedup_by(|a, b| a.1 == b.1);
        let mut packed = String::from("# pack-refs with: sorted\n");
        for (id, r) in kept {
            packed.push_str(&format!("{id} {r}\n"));
        }
        std::fs::write(self.dir.path().join("packed-refs"), packed).map_err(|e| e.to_string())
    }

    async fn refs(&self, prefix: &str) -> Result<Vec<(String, String)>, String> {
        let out = self.run(&["for-each-ref", "--format=%(objectname) %(refname)", prefix]).await?;
        Ok(String::from_utf8_lossy(&out.stdout)
            .lines()
            .filter_map(|l| l.split_once(' '))
            .map(|(id, r)| (id.to_string(), r.to_string()))
            .collect())
    }

    async fn run(&self, args: &[&str]) -> Result<Output, String> {
        self.run_with(args, Vec::new()).await
    }

    async fn run_with(&self, args: &[&str], input: Vec<u8>) -> Result<Output, String> {
        let mut cmd = tokio::process::Command::new("git");
        cmd.arg("--git-dir").arg(self.dir.path());
        for o in OVERRIDES {
            cmd.arg("-c").arg(o);
        }
        #[cfg(test)]
        if tests::LOCAL_REMOTES.get() {
            cmd.args(["-c", "protocol.file.allow=always"]);
        }
        cmd.args(args)
            .current_dir(self.dir.path())
            .env_clear()
            .envs(clean_env())
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .kill_on_drop(true);
        let run = async {
            let mut child = cmd.spawn()?;
            let mut stdin = child.stdin.take().expect("piped");
            let feed = async move {
                use tokio::io::AsyncWriteExt;
                let _ = stdin.write_all(&input).await;
            };
            let (out, ()) = tokio::join!(child.wait_with_output(), feed);
            out
        };
        match tokio::time::timeout(TIMEOUT, run).await {
            Ok(Ok(o)) => Ok(o),
            Ok(Err(e)) => Err(format!("running git: {e}")),
            Err(_) => Err(format!("git did not finish within {} seconds", TIMEOUT.as_secs())),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    thread_local! {
        /// Lets a test use local bare repositories as remotes.
        pub static LOCAL_REMOTES: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
    }

    fn git(dir: &Path, args: &[&str]) -> String {
        let out = std::process::Command::new("git")
            .args(args)
            .current_dir(dir)
            .env_clear()
            .envs(clean_env())
            .env("HOME", dir)
            .output()
            .unwrap();
        assert!(out.status.success(), "git {args:?}: {}", String::from_utf8_lossy(&out.stderr));
        String::from_utf8_lossy(&out.stdout).trim().to_string()
    }

    fn spec(host: &Path) -> MachineSpec {
        toml::from_str(&format!(
            "schema = 1\ngeneration = 1\nid = \"m\"\nroot = \"r\"\n[resources]\ncpus = 1\nmemory = \"1G\"\n\
             [[attach]]\nid = \"a\"\nhost = \"{}\"\nat = \"/toby/workspace/app\"\n",
            host.display()
        ))
        .unwrap()
    }

    fn project(tmp: &Path) -> PathBuf {
        let project = tmp.join("app");
        std::fs::create_dir(&project).unwrap();
        git(&project, &["init", "-q", "-b", "main"]);
        std::fs::write(project.join("f"), "one\n").unwrap();
        git(&project, &["add", "f"]);
        git(&project, &["-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "one"]);
        project.canonicalize().unwrap()
    }

    #[test]
    fn names_carry_no_options_or_refspecs() {
        assert!(name("main").is_ok());
        assert!(name("feature/x").is_ok());
        for bad in ["", "-f", ":main", "+main", "a:b", "ref*", "a..b", "HEAD~1", "x y", "a.lock", "a/.b"] {
            assert!(name(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn refs_and_remotes_are_read_without_git() {
        let tmp = tempfile::tempdir().unwrap();
        let project = project(tmp.path());
        git(&project, &["remote", "add", "origin", "https://example.com/app.git"]);
        git(&project, &["config", "core.fsmonitor", "touch pwned"]);
        let repo = open(&spec(&project), &[], "/toby/workspace/app").unwrap();
        assert_eq!(repo.branch().unwrap(), "main");
        let main = git(&project, &["rev-parse", "main"]);
        assert_eq!(repo.resolve("refs/heads/main").unwrap(), main);
        git(&project, &["pack-refs", "--all"]);
        assert_eq!(repo.resolve("refs/heads/main").unwrap(), main);
        assert_eq!(repo.remote(None).unwrap().url(), "https://example.com/app.git");
        assert!(repo.remote(Some("upstream")).is_err());
        assert!(!project.join("pwned").exists());
    }

    #[test]
    fn repositories_elsewhere_are_refused() {
        let tmp = tempfile::tempdir().unwrap();
        let project = project(tmp.path());
        let other = tmp.path().join("other");
        std::fs::create_dir(&other).unwrap();
        git(&other, &["init", "-q"]);
        let s = spec(&project);
        std::os::unix::fs::symlink(&other, project.join("l")).unwrap();
        assert!(open(&s, &[], "/toby/workspace/app/l").err().unwrap().contains("outside"));
        let sub = project.join("sub");
        std::fs::create_dir(&sub).unwrap();
        std::fs::write(sub.join(".git"), format!("gitdir: {}\n", other.join(".git").display())).unwrap();
        assert!(open(&s, &[], "/toby/workspace/app/sub").err().unwrap().contains("outside"));
        // A git directory in another project, whose back-pointer the guest wrote.
        let b = tmp.path().join("b");
        std::fs::create_dir_all(b.join("gd")).unwrap();
        std::fs::write(b.join("gd/gitdir"), format!("{}\n", sub.join(".git").display())).unwrap();
        std::fs::write(sub.join(".git"), format!("gitdir: {}\n", b.join("gd").display())).unwrap();
        let roots = [b.canonicalize().unwrap()];
        assert!(open(&s, &roots, "/toby/workspace/app/sub").err().unwrap().contains("outside"));
        // Nothing above the project counts.
        std::fs::remove_dir_all(project.join(".git")).unwrap();
        git(tmp.path(), &["init", "-q"]);
        assert!(open(&s, &[], "/toby/workspace/app").is_err());
    }

    #[test]
    fn refs_are_written_beneath_the_repository() {
        let tmp = tempfile::tempdir().unwrap();
        let project = project(tmp.path());
        let repo = open(&spec(&project), &[], "/toby/workspace/app").unwrap();
        let id = repo.resolve("refs/heads/main").unwrap();
        write_file(repo.common.as_fd(), "refs/remotes/origin/x/y", format!("{id}\n").as_bytes()).unwrap();
        assert_eq!(git(&project, &["rev-parse", "origin/x/y"]), id);
        // A link in the way is not followed.
        let outside = tmp.path().join("outside");
        std::fs::create_dir(&outside).unwrap();
        std::os::unix::fs::symlink(&outside, project.join(".git/refs/remotes/evil")).unwrap();
        assert!(write_file(repo.common.as_fd(), "refs/remotes/evil/x", b"x").is_err());
        assert_eq!(std::fs::read_dir(&outside).unwrap().count(), 0);
    }

    #[tokio::test]
    async fn fetch_and_push_go_through_a_private_repository() {
        let tmp = tempfile::tempdir().unwrap();
        let project = project(tmp.path());
        let marker = tmp.path().join("ran");
        let hook = project.join(".git/hooks/pre-push");
        std::fs::write(&hook, format!("#!/bin/sh\ntouch {}\n", marker.display())).unwrap();
        std::fs::set_permissions(&hook, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
        let repo = open(&spec(&project), &[], "/toby/workspace/app").unwrap();
        let scratch = tmp.path().join("scratch");
        let id = repo.resolve("refs/heads/main").unwrap();
        // Only https and ssh remotes.
        for bad in ["/srv/r.git", "file:///srv/r.git", "-u/x:y", "ext::sh -c x", "http://h/r.git"] {
            let remote = Remote { name: "origin".into(), url: Some(bad.into()), push_url: None };
            let e = repo.push(&scratch, &remote, "main", &id).await.unwrap_err();
            assert!(e.contains("not an https or ssh URL"), "{bad}: {e}");
            assert!(repo.fetch(&scratch, &remote).await.is_err());
        }
        assert!(url("git@example.com:app.git").is_ok());
        assert!(url("ssh://example.com/app.git").is_ok());
        assert!(!marker.exists());
        assert_eq!(std::fs::read_dir(&scratch).unwrap().count(), 0);
    }

    #[tokio::test]
    async fn fetch_and_push_move_objects_and_refs() {
        LOCAL_REMOTES.set(true);
        let tmp = tempfile::tempdir().unwrap();
        let project = project(tmp.path());
        let bare = tmp.path().join("r.git");
        git(tmp.path(), &["init", "-q", "-b", "main", "--bare", bare.to_str().unwrap()]);
        let marker = tmp.path().join("ran");
        let hook = project.join(".git/hooks/pre-push");
        std::fs::write(&hook, format!("#!/bin/sh\ntouch {}\n", marker.display())).unwrap();
        std::fs::set_permissions(&hook, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
        git(&project, &["remote", "add", "origin", bare.to_str().unwrap()]);
        let repo = open(&spec(&project), &[], "/toby/workspace/app").unwrap();
        let scratch = tmp.path().join("scratch");
        let remote = repo.remote(None).unwrap();

        let id = repo.resolve("refs/heads/main").unwrap();
        repo.push(&scratch, remote, "main", &id).await.unwrap();
        assert_eq!(git(&bare, &["rev-parse", "main"]), id);
        assert_eq!(git(&project, &["rev-parse", "origin/main"]), id);
        assert!(!marker.exists());

        // Someone else pushes; the fetch brings the objects and the ref.
        let other = tmp.path().join("other");
        git(tmp.path(), &["clone", "-q", bare.to_str().unwrap(), other.to_str().unwrap()]);
        std::fs::write(other.join("g"), "two\n").unwrap();
        git(&other, &["add", "g"]);
        git(&other, &["-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "two"]);
        git(&other, &["push", "-q", "origin", "main"]);
        let new = git(&other, &["rev-parse", "main"]);
        repo.fetch(&scratch, remote).await.unwrap();
        assert_eq!(git(&project, &["rev-parse", "origin/main"]), new);
        git(&project, &["cat-file", "-e", &format!("{new}:g")]);
        git(&project, &["fsck", "--no-progress"]);
        assert_eq!(std::fs::read_dir(&scratch).unwrap().count(), 0);

        // Objects of another repository, borrowed through an alternate the
        // guest wrote, are not pushed.
        let secret = tmp.path().join("secret");
        std::fs::create_dir(&secret).unwrap();
        git(&secret, &["init", "-q", "-b", "main"]);
        git(
            &secret,
            &["-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "s"],
        );
        let hidden = git(&secret, &["rev-parse", "main"]);
        let alternates = project.join(".git/objects/info/alternates");
        std::fs::write(&alternates, format!("{}\n", secret.join(".git/objects").display())).unwrap();
        std::fs::write(project.join(".git/refs/heads/evil"), format!("{hidden}\n")).unwrap();
        assert!(repo.push(&scratch, remote, "evil", &hidden).await.is_err());
        let out = std::process::Command::new("git")
            .args(["cat-file", "-e", &hidden])
            .current_dir(&bare)
            .output()
            .unwrap();
        assert!(!out.status.success());
    }
}
