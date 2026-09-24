//! Git on the host for the Toby MCP server (plan §16.4). A repository in a
//! project is the guest's to write, and git runs commands that repository
//! config and hooks name. So git runs with hooks and fsmonitor off, only
//! network transports that can neither read host files nor run commands, and
//! only when a repository config the guest can write sets nothing beyond a
//! short list of keys.

use std::path::{Component, Path, PathBuf};
use std::process::Output;
use std::time::Duration;

use toby_config::machine::MachineSpec;

/// Longest a git command may run.
const TIMEOUT: Duration = Duration::from_secs(300);

/// Keys a repository config the guest can write may set; `*` stands for a
/// subsection (a remote, branch or submodule name).
const ALLOWED_KEYS: &[&str] = &[
    "core.repositoryformatversion",
    "core.filemode",
    "core.bare",
    "core.logallrefupdates",
    "core.ignorecase",
    "core.precomposeunicode",
    "core.symlinks",
    "core.autocrlf",
    "core.eol",
    "core.safecrlf",
    "core.quotepath",
    "extensions.objectformat",
    "extensions.refstorage",
    "remote.*.url",
    "remote.*.pushurl",
    "remote.*.fetch",
    "remote.*.tagopt",
    "remote.*.prune",
    "remote.*.prunetags",
    "branch.*.remote",
    "branch.*.merge",
    "branch.*.rebase",
    "submodule.*.url",
    "submodule.*.active",
    "user.name",
    "user.email",
    "init.defaultbranch",
    "pull.rebase",
    "pull.ff",
    "fetch.prune",
    "lfs.repositoryformatversion",
];

/// Settings every command runs with, over any config.
const OVERRIDES: &[&str] = &[
    "core.fsmonitor=false",
    "core.hooksPath=/dev/null",
    "protocol.allow=never",
    "protocol.https.allow=always",
    "protocol.ssh.allow=always",
    "protocol.http.allow=never",
    "protocol.git.allow=never",
    "protocol.file.allow=never",
    "protocol.ext.allow=never",
    "submodule.recurse=false",
    "diff.ignoreSubmodules=all",
    "fetch.recurseSubmodules=false",
    "push.recurseSubmodules=no",
];

pub struct Remote {
    pub name: String,
    url: Option<String>,
    push_url: Option<String>,
}

impl Remote {
    /// Where the remote's fetches go.
    pub fn url(&self) -> &str {
        self.url.as_deref().unwrap_or("")
    }

    /// Where the remote's pushes go.
    pub fn push_url(&self) -> &str {
        self.push_url.as_deref().unwrap_or(self.url())
    }
}

/// A repository behind a guest path.
pub struct Repo {
    /// The host directory the action runs in.
    pub dir: PathBuf,
    pub work_tree: PathBuf,
    git_dir: PathBuf,
    remotes: Vec<Remote>,
    /// The project is mounted read-only.
    pub read_only: bool,
}

fn canonical(p: &Path) -> Result<PathBuf, String> {
    std::fs::canonicalize(p).map_err(|e| format!("{}: {e}", p.display()))
}

/// Opens the repository holding `guest`, a path in the machine inside one
/// of its attachments. Refuses paths that lead outside the attachment, a
/// repository elsewhere, and a guest-writable config that sets other keys.
pub fn open(spec: &MachineSpec, guest: &str) -> Result<Repo, String> {
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
    if !dir.is_dir() {
        return Err(format!("{guest} is not a directory"));
    }
    // What is inside the project, the guest may have written.
    let guest_writable = |p: &Path| p.starts_with(&root);

    let top = dir
        .ancestors()
        .find(|d| d.join(".git").symlink_metadata().is_ok())
        .ok_or_else(|| format!("{guest} is not in a git repository"))?
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
    let outside = || format!("the repository of {guest} is outside its project");
    if guest_writable(&top) && !guest_writable(&git_dir) {
        // A worktree of a repository elsewhere: that repository's own
        // record of the worktree has to name this one.
        let back = std::fs::read_to_string(git_dir.join("gitdir")).map_err(|_| outside())?;
        if canonical(Path::new(back.trim())).ok() != canonical(&dot_git).ok() {
            return Err(outside());
        }
    }
    let common = match std::fs::read_to_string(git_dir.join("commondir")) {
        Ok(c) => canonical(&git_dir.join(c.trim()))?,
        Err(_) => git_dir.clone(),
    };
    if guest_writable(&git_dir) && !guest_writable(&common) {
        return Err(outside());
    }
    if guest_writable(&common) && common.join("objects/info/alternates").exists() {
        return Err(format!(
            "the repository of {guest} borrows objects from elsewhere (objects/info/alternates)"
        ));
    }

    let config_path = common.join("config");
    let entries = match std::fs::canonicalize(&config_path) {
        Ok(path) => {
            let entries = read_config(&path)?;
            if guest_writable(&path) {
                check_config(&entries)?;
            }
            entries
        }
        Err(_) => Vec::new(),
    };
    Ok(Repo { dir, work_tree: top, git_dir, remotes: remotes(&entries), read_only: attach.read_only })
}

/// A config file's entries, without following its includes.
fn read_config(path: &Path) -> Result<Vec<(String, String)>, String> {
    let out = std::process::Command::new("git")
        .args(["config", "--no-includes", "--null", "--list", "--file"])
        .arg(path)
        .current_dir("/")
        .env_clear()
        .envs(clean_env())
        .stdin(std::process::Stdio::null())
        .output()
        .map_err(|e| format!("running git: {e}"))?;
    if !out.status.success() {
        return Err(format!("reading {}: {}", path.display(), String::from_utf8_lossy(&out.stderr).trim()));
    }
    Ok(parse_config(&out.stdout))
}

/// Parses `git config --null --list`: each entry is the key, a newline and
/// the value, ended by NUL.
fn parse_config(out: &[u8]) -> Vec<(String, String)> {
    out.split(|b| *b == 0)
        .filter(|e| !e.is_empty())
        .map(|e| {
            let e = String::from_utf8_lossy(e);
            match e.split_once('\n') {
                Some((k, v)) => (k.to_string(), v.to_string()),
                None => (e.to_string(), String::new()),
            }
        })
        .collect()
}

/// Whether `key` (section.subsection.name, lowercase section and name) is
/// in `ALLOWED_KEYS`.
fn allowed(key: &str) -> bool {
    let Some((section, rest)) = key.split_once('.') else { return false };
    let (sub, name) = match rest.rsplit_once('.') {
        Some((sub, name)) => (Some(sub), name),
        None => (None, rest),
    };
    let (section, name) = (section.to_ascii_lowercase(), name.to_ascii_lowercase());
    ALLOWED_KEYS.iter().any(|a| {
        let mut parts = a.split('.');
        let (s, m, n) = (parts.next(), parts.next(), parts.next());
        match (sub, n) {
            (None, None) => s == Some(section.as_str()) && m == Some(name.as_str()),
            (Some(_), Some(n)) => s == Some(section.as_str()) && m == Some("*") && n == name,
            _ => false,
        }
    })
}

fn check_config(entries: &[(String, String)]) -> Result<(), String> {
    match entries.iter().find(|(k, _)| !allowed(k)) {
        Some((k, _)) => Err(format!(
            "the repository's config sets {k}, which Toby's git actions do not run with; remove it with: git config --unset {k}"
        )),
        None => Ok(()),
    }
}

fn remotes(entries: &[(String, String)]) -> Vec<Remote> {
    let mut out: Vec<Remote> = Vec::new();
    for (k, v) in entries {
        let Some((sub, name)) = k.strip_prefix("remote.").and_then(|r| r.rsplit_once('.')) else { continue };
        let name = name.to_ascii_lowercase();
        if name != "url" && name != "pushurl" {
            continue;
        }
        let i = match out.iter().position(|r| r.name == sub) {
            Some(i) => i,
            None => {
                out.push(Remote { name: sub.to_string(), url: None, push_url: None });
                out.len() - 1
            }
        };
        let slot = if name == "url" { &mut out[i].url } else { &mut out[i].push_url };
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
    for (k, v) in [("GIT_TERMINAL_PROMPT", "0"), ("GIT_EDITOR", "true"), ("GIT_SEQUENCE_EDITOR", "true")] {
        env.push((k.into(), v.into()));
    }
    env
}

/// A branch, tag or remote name: no options, refspec syntax or
/// revision syntax.
pub fn name(s: &str) -> Result<&str, String> {
    let bad = s.is_empty()
        || s.starts_with(['-', '/', '.', '+'])
        || s.ends_with(['/', '.'])
        || s.ends_with(".lock")
        || s.contains("..")
        || s.contains("//")
        || s.contains("@{")
        || s == "@"
        || s.chars().any(|c| c.is_control() || c.is_whitespace() || "~^:?*[\\".contains(c));
    if bad { Err(format!("{s:?} is not a valid name")) } else { Ok(s) }
}

/// A revision such as `origin/main` or `HEAD~2`.
pub fn revision(s: &str) -> Result<&str, String> {
    let bad = s.is_empty() || s.starts_with('-') || s.chars().any(|c| c.is_control() || c.is_whitespace());
    if bad { Err(format!("{s:?} is not a valid revision")) } else { Ok(s) }
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
    pub async fn branch(&self) -> Result<String, String> {
        let out = self.run(&["symbolic-ref", "--short", "-q", "HEAD"]).await?;
        let b = String::from_utf8_lossy(&out.stdout).trim().to_string();
        if !out.status.success() || b.is_empty() {
            return Err("no branch is checked out".into());
        }
        name(&b).map(str::to_string)
    }

    /// Runs git in the repository.
    pub async fn run<S: AsRef<std::ffi::OsStr>>(&self, args: &[S]) -> Result<Output, String> {
        let mut cmd = tokio::process::Command::new("git");
        for o in OVERRIDES {
            cmd.arg("-c").arg(o);
        }
        cmd.args(args)
            .current_dir(&self.dir)
            .env_clear()
            .envs(clean_env())
            .env("GIT_DIR", &self.git_dir)
            .env("GIT_WORK_TREE", &self.work_tree)
            .stdin(std::process::Stdio::null())
            .kill_on_drop(true);
        match tokio::time::timeout(TIMEOUT, cmd.output()).await {
            Ok(Ok(o)) => Ok(o),
            Ok(Err(e)) => Err(format!("running git: {e}")),
            Err(_) => Err(format!("git did not finish within {} seconds", TIMEOUT.as_secs())),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn git(dir: &Path, args: &[&str]) {
        let ok = std::process::Command::new("git")
            .args(args)
            .current_dir(dir)
            .env_clear()
            .envs(clean_env())
            .env("HOME", dir)
            .output()
            .unwrap()
            .status
            .success();
        assert!(ok, "git {args:?}");
    }

    fn spec(host: &Path) -> MachineSpec {
        toml::from_str(&format!(
            "schema = 1\ngeneration = 1\nid = \"m\"\nroot = \"r\"\n[resources]\ncpus = 1\nmemory = \"1G\"\n\
             [[attach]]\nid = \"a\"\nhost = \"{}\"\nat = \"/toby/workspace/app\"\n",
            host.display()
        ))
        .unwrap()
    }

    #[test]
    fn only_listed_keys_are_allowed() {
        assert!(allowed("core.repositoryformatversion"));
        assert!(allowed("remote.origin.url"));
        assert!(allowed("remote.my.fork.URL"));
        assert!(allowed("branch.feature/x.merge"));
        assert!(!allowed("core.fsmonitor"));
        assert!(!allowed("core.hooksPath"));
        assert!(!allowed("include.path"));
        assert!(!allowed("remote.origin.uploadpack"));
        assert!(!allowed("remote.origin.push"));
        assert!(!allowed("remote.url"));
        assert!(!allowed("filter.lfs.clean"));
    }

    #[test]
    fn names_carry_no_options_or_refspecs() {
        assert!(name("main").is_ok());
        assert!(name("feature/x").is_ok());
        for bad in ["", "-f", ":main", "+main", "a:b", "ref*", "a..b", "HEAD~1", "x y", "a.lock"] {
            assert!(name(bad).is_err(), "{bad}");
        }
        assert!(revision("origin/main").is_ok());
        assert!(revision("HEAD~2").is_ok());
        assert!(revision("--exec=x").is_err());
    }

    #[test]
    fn guest_written_config_and_escapes_are_refused() {
        let tmp = tempfile::tempdir().unwrap();
        let project = tmp.path().join("app");
        std::fs::create_dir(&project).unwrap();
        git(&project, &["init", "-q"]);
        git(&project, &["remote", "add", "origin", "https://example.com/app.git"]);
        let s = spec(&project);

        let repo = open(&s, "/toby/workspace/app").unwrap();
        assert_eq!(repo.work_tree, project.canonicalize().unwrap());
        assert_eq!(repo.remote(None).unwrap().url(), "https://example.com/app.git");
        assert!(repo.remote(Some("upstream")).is_err());

        git(&project, &["config", "core.fsmonitor", "touch pwned"]);
        let e = open(&s, "/toby/workspace/app").err().unwrap();
        assert!(e.contains("core.fsmonitor"), "{e}");
        git(&project, &["config", "--unset", "core.fsmonitor"]);

        // A link out of the project, and a repository elsewhere.
        let other = tmp.path().join("other");
        std::fs::create_dir(&other).unwrap();
        git(&other, &["init", "-q"]);
        std::os::unix::fs::symlink(&other, project.join("l")).unwrap();
        assert!(open(&s, "/toby/workspace/app/l").err().unwrap().contains("outside"));
        let sub = project.join("sub");
        std::fs::create_dir(&sub).unwrap();
        std::fs::write(sub.join(".git"), format!("gitdir: {}\n", other.join(".git").display())).unwrap();
        assert!(open(&s, "/toby/workspace/app/sub").err().unwrap().contains("outside"));
        assert!(open(&s, "/toby/workspace/app/../..").is_err());
    }

    #[tokio::test]
    async fn hooks_and_fsmonitor_do_not_run() {
        let tmp = tempfile::tempdir().unwrap();
        let project = tmp.path().join("app");
        std::fs::create_dir(&project).unwrap();
        git(&project, &["init", "-q"]);
        let marker = tmp.path().join("ran");
        let hook = project.join(".git/hooks/pre-commit");
        std::fs::write(&hook, format!("#!/bin/sh\ntouch {}\n", marker.display())).unwrap();
        std::fs::set_permissions(&hook, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
        let repo = open(&spec(&project), "/toby/workspace/app").unwrap();
        let out = repo
            .run(&["-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"])
            .await
            .unwrap();
        assert!(out.status.success(), "{}", String::from_utf8_lossy(&out.stderr));
        assert!(!marker.exists());
    }
}
