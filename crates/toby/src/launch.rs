//! What one launch of a tool uses (plan §14.3, §14.6): flags over a launch
//! file, over the primary project's `.toby/config.toml`, over the global
//! configuration.

use std::path::{Component, Path, PathBuf};

use anyhow::{Context, bail};
use toby_config::global::GlobalConfig;
use toby_config::launch::{ImageConfig, Launch};
use toby_config::paths::expand;

/// An image configuration, and the directory its relative paths start in.
pub type Image = (ImageConfig, PathBuf);

/// A project attached at `/toby/workspace/<name>`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Project {
    pub name: String,
    pub host: PathBuf,
}

impl Project {
    pub fn at(&self) -> String {
        format!("/toby/workspace/{}", self.name)
    }
}

/// Command-line choices, which win over files.
#[derive(Debug, Default)]
pub struct Flags {
    pub tool: Option<String>,
    pub home: Option<String>,
    pub root: Option<String>,
    pub projects: Vec<PathBuf>,
    pub yolo: bool,
    pub args: Vec<String>,
}

#[derive(Debug, Default)]
pub struct Plan {
    pub tool: String,
    /// Other tools prepared and put on the tool's `PATH`.
    pub tools: Vec<String>,
    pub params: Vec<String>,
    pub home: Option<String>,
    pub root: Option<String>,
    /// The image for a root that does not exist, and the directory its
    /// relative paths start from.
    pub image: Option<(ImageConfig, PathBuf)>,
    /// The primary project first.
    pub projects: Vec<Project>,
    pub workdir: Option<String>,
    pub forwards: Vec<toby_api::AddForward>,
    pub mcp: Vec<String>,
    pub cpus: Option<u32>,
    pub memory: Option<String>,
    pub yolo: bool,
    pub warnings: Vec<toby_api::Warning>,
}

/// The directory relative project paths start from.
fn projects_dir(config: &GlobalConfig, home: &Path) -> PathBuf {
    let dir = expand(home, config.settings.projects_dir.as_deref().unwrap_or("~/Projects"));
    std::fs::canonicalize(&dir).unwrap_or(dir)
}

/// Resolves a project path; `external` allows one outside `projects_dir`.
fn resolve(path: &Path, projects_dir: &Path, external: bool) -> anyhow::Result<PathBuf> {
    let host = std::fs::canonicalize(path).with_context(|| format!("project {}", path.display()))?;
    if !host.is_dir() {
        bail!("project {} is not a directory", host.display());
    }
    if !external && !host.starts_with(projects_dir) {
        bail!(
            "project {} is outside {}; set settings.allow_external_projects to use it",
            host.display(),
            projects_dir.display()
        );
    }
    Ok(host)
}

/// A `--project` value: a relative path starts from `projects_dir` unless
/// it starts with `.` or `..`.
fn flag_path(p: &Path, cwd: &Path, projects_dir: &Path) -> PathBuf {
    if p.is_absolute() || matches!(p.components().next(), Some(Component::CurDir | Component::ParentDir)) {
        cwd.join(p)
    } else {
        projects_dir.join(p)
    }
}

/// The project the current directory is in: the nearest directory with
/// `.git` or `.toby`, else the directory under `projects_dir`, else the
/// current directory.
fn current_project(cwd: &Path, projects_dir: &Path) -> anyhow::Result<PathBuf> {
    if cwd == projects_dir {
        bail!("{} holds projects; run this in one, or name it with --project", cwd.display());
    }
    for dir in cwd.ancestors() {
        if dir == projects_dir || dir.parent().is_none() {
            break;
        }
        if dir.join(".git").exists() || dir.join(".toby").exists() {
            return Ok(dir.to_path_buf());
        }
    }
    match cwd.strip_prefix(projects_dir).ok().and_then(|r| r.components().next()) {
        Some(first) => Ok(projects_dir.join(first)),
        None => Ok(cwd.to_path_buf()),
    }
}

fn check_name(name: &str) -> anyhow::Result<()> {
    if name.is_empty()
        || name.len() > 64
        || name.starts_with('.')
        || !name.bytes().all(|b| b.is_ascii_alphanumeric() || b"-._".contains(&b))
    {
        bail!("{name:?} cannot name a project: use letters, digits, '-', '.' and '_'");
    }
    Ok(())
}

fn add(projects: &mut Vec<Project>, name: String, host: PathBuf) -> anyhow::Result<()> {
    if projects.iter().any(|p| p.host == host) {
        return Ok(());
    }
    check_name(&name)?;
    if projects.iter().any(|p| p.name == name) {
        bail!("two projects are named {name}; name them in a launch file");
    }
    projects.push(Project { name, host });
    Ok(())
}

fn dir_name(p: &Path) -> anyhow::Result<String> {
    p.file_name()
        .and_then(|n| n.to_str())
        .map(str::to_string)
        .context("a project needs a UTF-8 directory name")
}

/// Checks that the host paths a project's image names stay in
/// `projects_dir` (plan §14.3): its build context is shared with the
/// build.
fn check_project_image(
    image: &ImageConfig,
    base: &Path,
    home: &Path,
    projects_dir: &Path,
    external: bool,
) -> anyhow::Result<()> {
    if external {
        return Ok(());
    }
    let paths: Vec<&str> = match image {
        ImageConfig::Named(_) | ImageConfig::Registry { .. } => Vec::new(),
        ImageConfig::Mkosi { mkosi } => vec![mkosi],
        ImageConfig::Dockerfile { dockerfile, context } => {
            vec![dockerfile, context.as_deref().unwrap_or(".")]
        }
        ImageConfig::Archive { archive } => vec![archive],
    };
    for p in paths {
        // Resolved as the build resolves it.
        let path = base.join(expand(home, p));
        let host = std::fs::canonicalize(&path).with_context(|| path.display().to_string())?;
        if !host.starts_with(projects_dir) {
            bail!(
                "the project's image uses {}, outside {}; set settings.allow_external_projects to use it",
                host.display(),
                projects_dir.display()
            );
        }
    }
    Ok(())
}

/// The image configured for the project at `path` (the current project
/// without one): its configuration's, or `[defaults] image`, with the
/// directory its relative paths start in.
pub fn project_image(
    config: &GlobalConfig,
    config_dir: &Path,
    home: &Path,
    cwd: &Path,
    path: Option<&Path>,
) -> anyhow::Result<(Option<Image>, Vec<toby_api::Warning>)> {
    let projects_dir = projects_dir(config, home);
    let project = match path {
        Some(p) => resolve(&flag_path(p, cwd, &projects_dir), &projects_dir, true)?,
        None => resolve(&current_project(cwd, &projects_dir)?, &projects_dir, true)?,
    };
    let file = project.join(".toby/config.toml");
    let mut warnings = Vec::new();
    if file.exists() {
        if config.settings.autoload_project_config {
            if let Some(image) = Launch::load_project(&file)?.image {
                check_project_image(
                    &image,
                    &project,
                    home,
                    &projects_dir,
                    config.settings.allow_external_projects,
                )?;
                return Ok((Some((image, project)), warnings));
            }
        } else {
            warnings.push(toby_api::Warning {
                id: "project.autoload-disabled".into(),
                message: format!(
                    "{} is not read; set settings.autoload_project_config to use it",
                    file.display()
                ),
            });
        }
    }
    Ok((config.defaults.image.clone().map(|i| (i, config_dir.to_path_buf())), warnings))
}

/// Works out a launch.
pub fn plan(
    config: &GlobalConfig,
    config_dir: &Path,
    home: &Path,
    cwd: &Path,
    file: Option<&Path>,
    flags: Flags,
) -> anyhow::Result<Plan> {
    let projects_dir = projects_dir(config, home);
    let external = config.settings.allow_external_projects;
    let mut plan = Plan::default();

    let (launch, launch_dir) = match file {
        Some(f) => {
            let l = Launch::load(f)?;
            let dir = std::fs::canonicalize(f)?.parent().map(Path::to_path_buf).unwrap_or_default();
            (l, dir)
        }
        None => (Launch::default(), PathBuf::new()),
    };

    // A launch file names its projects, which may be anywhere.
    let mut primary_chosen = false;
    for (name, entry) in &launch.projects {
        let path = match &entry.path {
            Some(p) => launch_dir.join(expand(home, p)),
            None => projects_dir.join(name),
        };
        let host = resolve(&path, &projects_dir, true)?;
        add(&mut plan.projects, name.clone(), host)?;
        if entry.primary && !primary_chosen {
            let p = plan.projects.pop().expect("just added");
            plan.projects.insert(0, p);
            primary_chosen = true;
        }
    }
    for p in &flags.projects {
        let host = resolve(&flag_path(p, cwd, &projects_dir), &projects_dir, external)?;
        add(&mut plan.projects, dir_name(&host)?, host)?;
    }
    if plan.projects.is_empty() {
        let host = resolve(&current_project(cwd, &projects_dir)?, &projects_dir, external)?;
        add(&mut plan.projects, dir_name(&host)?, host)?;
    }

    let primary = plan.projects[0].host.clone();
    let project_file = primary.join(".toby/config.toml");
    let project = if !project_file.exists() {
        Launch::default()
    } else if config.settings.autoload_project_config {
        Launch::load_project(&project_file)?
    } else {
        plan.warnings.push(toby_api::Warning {
            id: "project.autoload-disabled".into(),
            message: format!(
                "{} is not read; set settings.autoload_project_config to use it",
                project_file.display()
            ),
        });
        Launch::default()
    };
    for (name, entry) in &project.projects {
        let path = match &entry.path {
            Some(p) => primary.join(p),
            None => projects_dir.join(name),
        };
        let host = resolve(&path, &projects_dir, external)?;
        add(&mut plan.projects, name.clone(), host.clone())?;
        if entry.primary && !primary_chosen {
            let i = plan.projects.iter().position(|p| p.host == host).expect("just added");
            let p = plan.projects.remove(i);
            plan.projects.insert(0, p);
            primary_chosen = true;
        }
    }

    plan.tool = match flags.tool.or(launch.tool) {
        Some(t) => t,
        None => bail!("the launch file names no tool"),
    };
    plan.tools = launch.tools;
    plan.params = launch.params;
    plan.params.extend(flags.args);
    plan.home = flags.home.or(launch.home).or(project.home);
    plan.root = flags.root.or(launch.root).or(project.root);
    if launch.image.is_none()
        && let Some(image) = &project.image
    {
        check_project_image(image, &primary, home, &projects_dir, external)?;
    }
    plan.image = launch
        .image
        .map(|i| (i, launch_dir.clone()))
        .or(project.image.map(|i| (i, primary.clone())))
        .or(config.defaults.image.clone().map(|i| (i, config_dir.to_path_buf())));
    plan.cpus = launch.cpus.or(project.cpus);
    plan.memory = launch.memory.or(project.memory);
    plan.yolo = flags.yolo || launch.settings.yolo.unwrap_or(config.settings.yolo);

    let at = plan.projects[0].at();
    plan.workdir = match launch.workdir.or(project.workdir) {
        Some(w) if w.starts_with('/') => Some(w),
        Some(w) => Some(format!("{at}/{w}")),
        // Where the command runs, when that is in the primary project.
        None => cwd
            .strip_prefix(&plan.projects[0].host)
            .ok()
            .filter(|rel| !rel.as_os_str().is_empty())
            .and_then(|rel| rel.to_str())
            .map(|rel| format!("{at}/{rel}")),
    };
    if let Some(w) = &plan.workdir
        && Path::new(w).components().any(|c| c == Component::ParentDir)
    {
        bail!("workdir {w} cannot contain ..");
    }

    for f in launch.forwards.iter().chain(&project.forwards) {
        let forward = toby_api::AddForward {
            direction: f.direction.clone(),
            host: format!("127.0.0.1:{}", f.host),
            guest: format!("127.0.0.1:{}", f.guest.unwrap_or(f.host)),
            pinned: false,
            persist: false,
        };
        if !plan.forwards.contains(&forward) {
            plan.forwards.push(forward);
        }
    }
    for m in launch.mcp.into_iter().chain(project.mcp) {
        if !plan.mcp.contains(&m) {
            plan.mcp.push(m);
        }
    }
    Ok(plan)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(text: &str) -> GlobalConfig {
        toml::from_str(text).unwrap()
    }

    struct Tree {
        _dir: tempfile::TempDir,
        home: PathBuf,
        projects: PathBuf,
    }

    fn tree() -> Tree {
        let dir = tempfile::tempdir().unwrap();
        let home = dir.path().canonicalize().unwrap();
        let projects = home.join("Projects");
        for p in ["app/src", "app/.git", "lib", "org/web/.git", "loose/sub"] {
            std::fs::create_dir_all(projects.join(p)).unwrap();
        }
        std::fs::create_dir_all(home.join("elsewhere")).unwrap();
        Tree { _dir: dir, home, projects }
    }

    fn names(p: &Plan) -> Vec<&str> {
        p.projects.iter().map(|p| p.name.as_str()).collect()
    }

    #[test]
    fn projects_from_the_current_directory() {
        let t = tree();
        let c = config("");
        let flags = || Flags { tool: Some("claude".into()), ..Default::default() };
        let p = plan(&c, &t.home, &t.home, &t.projects.join("app/src"), None, flags()).unwrap();
        assert_eq!(names(&p), ["app"]);
        assert_eq!(p.workdir.as_deref(), Some("/toby/workspace/app/src"));
        let p = plan(&c, &t.home, &t.home, &t.projects.join("org/web"), None, flags()).unwrap();
        assert_eq!(names(&p), ["web"]);
        assert_eq!(p.workdir, None);
        let p = plan(&c, &t.home, &t.home, &t.projects.join("loose/sub"), None, flags()).unwrap();
        assert_eq!(names(&p), ["loose"]);
        assert!(plan(&c, &t.home, &t.home, &t.projects, None, flags()).is_err());
        let e = plan(&c, &t.home, &t.home, &t.home.join("elsewhere"), None, flags()).unwrap_err();
        assert!(e.to_string().contains("allow_external_projects"), "{e}");
        let c = config("[settings]\nallow_external_projects = true\n");
        let p = plan(&c, &t.home, &t.home, &t.home.join("elsewhere"), None, flags()).unwrap();
        assert_eq!(names(&p), ["elsewhere"]);
    }

    #[test]
    fn project_flags() {
        let t = tree();
        let c = config("");
        let flags = |p: &[&str]| Flags {
            tool: Some("claude".into()),
            projects: p.iter().map(PathBuf::from).collect(),
            ..Default::default()
        };
        let p = plan(&c, &t.home, &t.home, &t.home, None, flags(&["lib", "app"])).unwrap();
        assert_eq!(names(&p), ["lib", "app"]);
        assert_eq!(p.projects[1].at(), "/toby/workspace/app");
        let p = plan(&c, &t.home, &t.home, &t.projects.join("app"), None, flags(&["../lib"])).unwrap();
        assert_eq!(names(&p), ["lib"]);
        assert!(plan(&c, &t.home, &t.home, &t.home, None, flags(&["./elsewhere"])).is_err());
        assert!(plan(&c, &t.home, &t.home, &t.home, None, flags(&["missing"])).is_err());
    }

    #[test]
    fn launch_files_and_project_configs() {
        let t = tree();
        let file = t.home.join("review.toml");
        std::fs::write(
            &file,
            "tool = \"codex\"\ntools = [\"gh\"]\nparams = [\"-m\", \"x\"]\nhome = \"work\"\nworkdir = \"src\"\n\
             image = { dockerfile = \"Dockerfile\" }\nforwards = [{ host = 3000 }]\n\
             [projects.main]\npath = \"Projects/app\"\nprimary = true\n[projects.outside]\npath = \"elsewhere\"\n\
             [settings]\nyolo = true\n",
        )
        .unwrap();
        std::fs::create_dir_all(t.projects.join("app/.toby")).unwrap();
        std::fs::write(
            t.projects.join("app/.toby/config.toml"),
            "home = \"other\"\nroot = \"work\"\nmcp = [\"github\"]\nforwards = [{ host = 3000 }, { host = 8080, guest = 80 }]\n\
             [projects.lib]\n",
        )
        .unwrap();
        let flags = Flags { args: vec!["extra".into()], ..Default::default() };
        let c = config("");
        let p = plan(&c, &t.home, &t.home, &t.home, Some(&file), flags).unwrap();
        assert_eq!(p.tool, "codex");
        assert_eq!(names(&p), ["main", "outside"]);
        assert_eq!(p.params, ["-m", "x", "extra"]);
        assert_eq!(p.workdir.as_deref(), Some("/toby/workspace/main/src"));
        assert!(p.yolo);
        assert_eq!(p.image.as_ref().unwrap().1, t.home);
        assert_eq!(p.warnings[0].id, "project.autoload-disabled");
        assert_eq!(p.forwards.len(), 1);

        let c = config("[settings]\nautoload_project_config = true\n");
        let flags = Flags { home: Some("cli".into()), ..Default::default() };
        let p = plan(&c, &t.home, &t.home, &t.home, Some(&file), flags).unwrap();
        assert!(p.warnings.is_empty());
        assert_eq!(names(&p), ["main", "outside", "lib"]);
        assert_eq!((p.home.as_deref(), p.root.as_deref()), (Some("cli"), Some("work")));
        assert_eq!(p.mcp, ["github"]);
        assert_eq!(p.forwards.len(), 2);
        assert_eq!(p.forwards[1].guest, "127.0.0.1:80");

        // A project's configuration cannot reach outside projects_dir, with
        // its image's build context or with its projects.
        std::fs::write(
            t.projects.join("app/.toby/config.toml"),
            "image = { dockerfile = \"Dockerfile\", context = \"../..\" }\n",
        )
        .unwrap();
        std::fs::write(t.projects.join("app/Dockerfile"), "FROM x\n").unwrap();
        let flags = Flags { tool: Some("claude".into()), ..Default::default() };
        let e = plan(&c, &t.home, &t.home, &t.projects.join("app"), None, flags).unwrap_err();
        assert!(e.to_string().contains("outside"), "{e}");
        std::fs::write(
            t.projects.join("app/.toby/config.toml"),
            "[projects.x]\npath = \"../../elsewhere\"\n",
        )
        .unwrap();
        let flags = Flags { tool: Some("claude".into()), ..Default::default() };
        assert!(plan(&c, &t.home, &t.home, &t.projects.join("app"), None, flags).is_err());
    }
}
