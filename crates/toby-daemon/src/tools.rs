//! Tools in machines (plan §16.1): checking the root has what a tool needs,
//! installing or updating it as the user, writing its configuration files,
//! and the command and environment it is launched with.

use std::io;

use toby_config::machine::MachineSpec;
use toby_proto::types::{ExitStatus, Identity};
use toby_tools::{Context, Manifest, ModelsContext, render};

use crate::builder::Output;
use crate::control;
use crate::machines::{MODELS_LISTEN, Machines};

/// `toby-connect` in the guest.
const CONNECT: &str = "/run/toby/bin/toby-connect";

fn err(msg: impl Into<String>) -> io::Error {
    io::Error::other(msg.into())
}

/// Quotes a word for the shell.
fn quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// A login shell command with the tool's directories on `PATH`.
fn shell(tool: &toby_tools::Tool, script: &str) -> Vec<String> {
    let mut dirs = vec!["$HOME/.local/bin".to_string()];
    for p in &tool.path {
        dirs.push(match p.strip_prefix("~/") {
            Some(rest) => format!("$HOME/{rest}"),
            None => p.clone(),
        });
    }
    let script = format!("export PATH=\"{}:$PATH\"; {script}", dirs.join(":"));
    vec!["/bin/bash".into(), "-lc".into(), script]
}

/// The machine's user, for templates.
fn user(machines: &Machines, spec: &MachineSpec) -> (String, String) {
    match spec.home.as_ref().and_then(|h| machines.store.home(h).ok()) {
        Some(h) => (h.username.clone(), format!("/home/{}", h.username)),
        None => ("root".into(), "/root".into()),
    }
}

/// What a launch is about.
#[derive(Debug, Clone, Default)]
pub struct Session {
    /// The primary project's path in the machine.
    pub workspace: String,
    /// Every project's path in the machine.
    pub projects: Vec<String>,
    /// The tool skips its permission prompts.
    pub yolo: bool,
    /// Configured MCP servers the tool gets besides its configured ones.
    pub mcp: Vec<String>,
    /// Other tools whose `PATH` and environment the tool gets.
    pub extra: Vec<String>,
}

/// How long a provider's list of models is kept.
const MODELS_FOR: std::time::Duration = std::time::Duration::from_secs(300);

/// The models provider `name` lists (plan §14.6), kept for five minutes.
pub async fn discover_models(machines: &Machines, name: &str) -> Result<Vec<String>, String> {
    if let Some((at, list)) = machines.models_cache.lock().unwrap().get(name)
        && at.elapsed() < MODELS_FOR
    {
        return Ok(list.clone());
    }
    let config = machines.current_config();
    let provider = config.models.get(name).ok_or_else(|| format!("no model provider {name}"))?.clone();
    let config_dir = machines.paths.global_config().parent().map(|p| p.to_path_buf()).unwrap_or_default();
    let home = toby_config::paths::home_dir().map_err(|e| e.to_string())?;
    let mut headers = Vec::new();
    for (k, v) in &provider.headers {
        headers.push((
            k.clone(),
            toby_config::subst::resolve(v, &config_dir, &home).map_err(|e| e.to_string())?,
        ));
    }
    if provider.protocol == toby_config::global::Protocol::Anthropic
        && !headers.iter().any(|(k, _)| k.eq_ignore_ascii_case("anthropic-version"))
    {
        headers.push(("anthropic-version".into(), "2023-06-01".into()));
    }
    let url = format!("{}/v1/models", provider.url.trim_end_matches('/'));
    let list = tokio::task::spawn_blocking(move || -> Result<Vec<String>, String> {
        let agent: ureq::Agent = ureq::Agent::config_builder()
            .timeout_global(Some(std::time::Duration::from_secs(10)))
            .build()
            .into();
        let mut req = agent.get(&url);
        for (k, v) in &headers {
            req = req.header(k, v);
        }
        let mut resp = req.call().map_err(|e| e.to_string())?;
        let text = resp.body_mut().read_to_string().map_err(|e| e.to_string())?;
        let body: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
        let mut ids: Vec<String> = body
            .get("data")
            .and_then(|d| d.as_array())
            .map(|a| a.iter().filter_map(|m| m.get("id")?.as_str().map(str::to_string)).collect())
            .unwrap_or_default();
        ids.sort();
        Ok(ids)
    })
    .await
    .map_err(|e| e.to_string())??;
    machines.models_cache.lock().unwrap().insert(name.into(), (std::time::Instant::now(), list.clone()));
    Ok(list)
}

/// The configured instruction files, joined (plan §14.6). Entries are host
/// paths or globs in their last component, relative to the configuration's
/// directory; missing ones are reported in `warnings`.
fn instructions(
    entries: &[String],
    config_dir: &std::path::Path,
    home: &std::path::Path,
    warnings: &mut Vec<String>,
) -> String {
    let mut docs = Vec::new();
    for entry in entries {
        let path = match entry.strip_prefix("~/") {
            Some(rest) => home.join(rest),
            None => config_dir.join(entry),
        };
        let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
        let files: Vec<std::path::PathBuf> = match name.split_once('*') {
            Some((prefix, suffix)) => {
                let dir = path.parent().unwrap_or(std::path::Path::new("/"));
                let mut found: Vec<_> = std::fs::read_dir(dir)
                    .map(|d| d.flatten().map(|e| e.path()).collect())
                    .unwrap_or_default();
                found.retain(|p: &std::path::PathBuf| {
                    let n = p.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
                    n.starts_with(prefix) && n.ends_with(suffix) && n.len() >= prefix.len() + suffix.len()
                });
                found.sort();
                found
            }
            None => vec![path],
        };
        if files.is_empty() {
            warnings.push(format!("warning[config.instruction-missing]: nothing matches {entry}"));
        }
        for f in files {
            match std::fs::read_to_string(&f) {
                Ok(text) if !text.trim().is_empty() => docs.push(text.trim_end_matches('\n').to_string()),
                Ok(_) => {}
                Err(e) => warnings.push(format!("warning[config.instruction-missing]: {}: {e}", f.display())),
            }
        }
    }
    if docs.is_empty() { String::new() } else { docs.join("\n\n") + "\n" }
}

/// What the tool's templates see in this machine.
pub fn context(
    machines: &Machines,
    spec: &MachineSpec,
    tool: &Manifest,
    session: &Session,
    warnings: &mut Vec<String>,
) -> io::Result<Context> {
    let (user, home) = user(machines, spec);
    let config = machines.current_config();
    let provider = config.tools.get(&tool.tool.name).and_then(|t| t.models.clone());
    let models = match provider {
        Some(p) if tool.tool.models.is_some() => {
            let Some(provider) = config.models.get(&p) else {
                return Err(err(format!(
                    "tools.{}.models: no model provider {p} is configured",
                    tool.tool.name
                )));
            };
            let offers = match provider.protocol {
                toby_config::global::Protocol::Anthropic => toby_tools::Protocol::Anthropic,
                toby_config::global::Protocol::Openai => toby_tools::Protocol::Openai,
            };
            if !tool.tool.models.as_ref().is_some_and(|m| m.protocols.contains(&offers)) {
                return Err(err(format!(
                    "tools.{}.models: {} cannot use the {} API of provider {p}",
                    tool.tool.name,
                    tool.tool.name,
                    match offers {
                        toby_tools::Protocol::Anthropic => "Anthropic",
                        toby_tools::Protocol::Openai => "OpenAI",
                    }
                )));
            }
            let token = toby_proxy::ensure_token(&machines.paths, &spec.id)?;
            let list =
                machines.models_cache.lock().unwrap().get(&p).map(|(_, l)| l.clone()).unwrap_or_default();
            Some(ModelsContext {
                url: format!("http://{MODELS_LISTEN}/{p}"),
                token,
                provider: p,
                protocol: offers,
                list,
            })
        }
        _ => None,
    };
    let config_dir = machines.paths.global_config().parent().map(|p| p.to_path_buf()).unwrap_or_default();
    let host_home = toby_config::paths::home_dir()?;
    let instructions = instructions(&config.instructions, &config_dir, &host_home, warnings);
    // Guest paths; `~` is the home in the machine.
    let mut permissions: std::collections::BTreeMap<String, String> = config
        .permissions
        .paths
        .iter()
        .map(|(p, policy)| {
            let p = match p.strip_prefix('~') {
                Some(rest) => format!("{home}{rest}"),
                None => p.clone(),
            };
            let p = if p.len() > 1 { p.trim_end_matches('/').to_string() } else { p };
            (p, policy.as_str().to_string())
        })
        .collect();
    permissions.entry("/tmp".into()).or_insert_with(|| "allow".into());
    for p in &session.projects {
        permissions.entry(p.clone()).or_insert_with(|| "allow".into());
    }
    // The launch resolved --yolo, its launch file and settings.yolo.
    let yolo = session.yolo;
    if yolo {
        permissions.insert("/".into(), "allow".into());
    }
    let plain = |want: &str| {
        permissions
            .iter()
            .filter(|(p, policy)| *policy == want && !p.contains(['*', '?', '[']))
            .map(|(p, _)| p.clone())
            .collect()
    };
    let (allowed, denied) = (plain("allow"), plain("deny"));
    Ok(Context {
        models,
        user,
        home,
        workspace: session.workspace.clone(),
        connect: CONNECT.into(),
        instructions,
        permissions,
        allowed,
        denied,
        projects: session.projects.clone(),
        yolo,
        ..Default::default()
    })
}

/// The tool after the tools it depends on, each once, dependencies first.
pub fn with_dependencies(machines: &Machines, manifest: &Manifest) -> io::Result<Vec<Manifest>> {
    fn visit(
        machines: &Machines,
        m: &Manifest,
        path: &mut Vec<String>,
        out: &mut Vec<Manifest>,
    ) -> io::Result<()> {
        if out.iter().any(|o| o.tool.name == m.tool.name) {
            return Ok(());
        }
        if path.contains(&m.tool.name) {
            return Err(err(format!("tools depend on each other: {} and {}", path.join(" → "), m.tool.name)));
        }
        path.push(m.tool.name.clone());
        for d in &m.tool.depends {
            let dep = machines.manifest(d).map_err(|e| err(e.message))?;
            visit(machines, &dep, path, out)?;
        }
        path.pop();
        out.push(m.clone());
        Ok(())
    }
    let mut out = Vec::new();
    visit(machines, manifest, &mut Vec::new(), &mut out)?;
    Ok(out)
}

async fn run_user(
    machines: &Machines,
    spec: &MachineSpec,
    argv: Vec<String>,
    out: Output<'_>,
) -> io::Result<ExitStatus> {
    control::run(&machines.runtime(&spec.id), argv, Identity::User, Vec::new(), out).await
}

/// Checks the root, installs or updates the tool and the tools it depends
/// on, and writes their files.
pub async fn prepare(
    machines: &Machines,
    spec: &MachineSpec,
    manifest: &Manifest,
    upgrade: bool,
    session: &Session,
    out: Output<'_>,
) -> io::Result<()> {
    // The provider's models, for the files.
    let config = machines.current_config();
    if let Some(p) = config.tools.get(&manifest.tool.name).and_then(|t| t.models.clone())
        && manifest.tool.models.is_some()
        && let Err(e) = discover_models(machines, &p).await
        && !config.settings.suppressed("models.endpoint-unavailable")
    {
        out(format!("warning[models.endpoint-unavailable]: model provider {p}: {e}\n").as_bytes(), true);
    }
    for m in with_dependencies(machines, manifest)? {
        prepare_one(machines, spec, &m, upgrade, session, &mut *out).await?;
    }
    Ok(())
}

async fn prepare_one(
    machines: &Machines,
    spec: &MachineSpec,
    manifest: &Manifest,
    upgrade: bool,
    session: &Session,
    out: Output<'_>,
) -> io::Result<()> {
    let tool = &manifest.tool;
    let name = &tool.name;
    let root = match &spec.root {
        toby_config::machine::RootSpec::Named(r) => r.clone(),
        _ => "the machine's root".into(),
    };
    for r in &tool.requires {
        if r.is_empty() || !r.bytes().all(|b| b.is_ascii_alphanumeric() || b"._+-".contains(&b)) {
            return Err(err(format!("tool {name} requires an invalid command name {r:?}")));
        }
    }
    let requires = tool.requires.join(" ");
    let script = format!(
        "missing=; for c in {requires}; do command -v \"$c\" >/dev/null 2>&1 || missing=\"$missing $c\"; done; \
         if [ -n \"$missing\" ]; then echo \"needs$missing, which root {root} lacks; add them to its image and run: toby root rebase {root}\" >&2; exit 3; fi"
    );
    let mut sink = |_: &[u8], _: bool| {};
    let mut errors = Vec::new();
    let mut collect = |b: &[u8], _: bool| errors.extend_from_slice(b);
    if run_user(machines, spec, vec!["/bin/sh".into(), "-c".into(), script], &mut collect).await?
        != ExitStatus::Code(0)
    {
        return Err(err(format!("{name}: {}", String::from_utf8_lossy(&errors).trim())));
    }

    let check = tool.check.iter().map(|a| quote(a)).collect::<Vec<_>>().join(" ");
    let installed = run_user(machines, spec, shell(tool, &format!("{check} >/dev/null 2>&1")), &mut sink)
        .await?
        == ExitStatus::Code(0);
    if !installed || upgrade {
        let script = match (installed, &tool.update, &tool.install) {
            (true, Some(update), _) => &update.script,
            (_, _, Some(install)) => &install.script,
            // The image provides it.
            (true, None, None) => "true",
            (false, _, None) => {
                return Err(err(format!(
                    "{name} is not in root {root}; add it to its image and run: toby root rebase {root}"
                )));
            }
        };
        let verb = if installed { "Updating" } else { "Installing" };
        out(format!("==> {verb} {name}\n").as_bytes(), false);
        if run_user(machines, spec, shell(tool, script), &mut *out).await? != ExitStatus::Code(0) {
            return Err(err(format!("{} {name} failed", verb.to_lowercase())));
        }
        if run_user(machines, spec, shell(tool, &format!("{check} >/dev/null 2>&1")), &mut sink).await?
            != ExitStatus::Code(0)
        {
            return Err(err(format!("{name} was installed, but `{}` fails", tool.check.join(" "))));
        }
    }

    let mut warnings = Vec::new();
    let ctx = context(machines, spec, manifest, session, &mut warnings)?;
    let settings = machines.current_config().settings.clone();
    for w in warnings {
        let id =
            w.strip_prefix("warning[").and_then(|w| w.split_once(']')).map(|(id, _)| id).unwrap_or_default();
        if !settings.suppressed(id) {
            out(format!("{w}\n").as_bytes(), true);
        }
    }
    for f in &tool.files {
        let content = render(&f.template, &ctx).map_err(|e| err(format!("{name}: {}: {e}", f.path)))?;
        if f.optional && content.trim().is_empty() {
            continue;
        }
        patch_file(machines, spec, &f.path, &content, f.format, f.mode).await?;
    }
    write_mcp(machines, spec, manifest, session, &ctx).await
}

/// Writes the tool's MCP servers into its configuration (plan §16.3):
/// Toby's own always, those `[tools.<name>].mcp` names and the launch's.
async fn write_mcp(
    machines: &Machines,
    spec: &MachineSpec,
    manifest: &Manifest,
    session: &Session,
    ctx: &Context,
) -> io::Result<()> {
    use toby_config::global::{McpKind, Placement};
    let tool = &manifest.tool;
    let Some(mcp) = &tool.mcp else { return Ok(()) };
    let config = machines.current_config();
    let mut wanted = config.tools.get(&tool.name).map(|t| t.mcp.clone()).unwrap_or_default();
    for m in &session.mcp {
        if !wanted.contains(m) {
            wanted.push(m.clone());
        }
    }
    let mut servers = serde_json::Map::new();
    for name in std::iter::once("toby".to_string()).chain(wanted.into_iter().filter(|n| n != "toby")) {
        let mut ctx = ctx.clone();
        ctx.name = name.clone();
        let template = if name == "toby" {
            &mcp.entry
        } else {
            let server = config.mcp.get(&name).ok_or_else(|| {
                err(format!("tool {} names MCP server {name}, which is not configured", tool.name))
            })?;
            server.check(&name).map_err(err)?;
            match (server.kind, server.placement()) {
                (McpKind::Http, _) => {
                    ctx.url = format!("http://{MODELS_LISTEN}/mcp/{name}");
                    mcp.http_entry
                        .as_ref()
                        .ok_or_else(|| err(format!("{} cannot use HTTP MCP server {name}", tool.name)))?
                }
                (McpKind::Stdio, Placement::Isolated) => &mcp.entry,
                (McpKind::Stdio, Placement::Machine) => {
                    ctx.command = server.command[0].clone();
                    ctx.args = server.command[1..].to_vec();
                    ctx.env = server.env.clone().into_iter().collect();
                    mcp.command_entry
                        .as_ref()
                        .ok_or_else(|| {
                            err(format!(
                                "{} cannot run MCP server {name} in its machine; set mcp.{name}.placement = \"isolated\"",
                                tool.name
                            ))
                        })?
                }
            }
        };
        let entry = render(template, &ctx).map_err(|e| err(format!("{}: MCP {name}: {e}", tool.name)))?;
        let value: serde_json::Value =
            serde_json::from_str(&entry).map_err(|e| err(format!("{}: MCP {name}: {e}", tool.name)))?;
        servers.insert(name, value);
    }
    let patch = toby_tools::at_pointer(&mcp.pointer, serde_json::Value::Object(servers));
    let content = match mcp.format {
        toby_tools::Format::Toml => toml::to_string(&patch).map_err(|e| err(e.to_string()))?,
        _ => patch.to_string(),
    };
    patch_file(machines, spec, &mcp.path, &content, mcp.format, toby_tools::Mode::Merge).await
}

/// Merges or writes a file in the home through the guest helper.
pub async fn patch_file(
    machines: &Machines,
    spec: &MachineSpec,
    path: &str,
    content: &str,
    format: toby_tools::Format,
    mode: toby_tools::Mode,
) -> io::Result<()> {
    let format = match format {
        toby_tools::Format::Json => "json",
        toby_tools::Format::Toml => "toml",
        toby_tools::Format::Text => "text",
    };
    let mode = match mode {
        toby_tools::Mode::Merge => "merge",
        toby_tools::Mode::Extend => "extend",
        toby_tools::Mode::Replace => "replace",
    };
    let version = crate::builder::runtime_version(&machines.config.programs.versions());
    let toby = format!("/run/toby/fs/versions/{version}/toby");
    let argv = [&toby, "guest", "helper", "patch-file", "--path", path, "--format", format, "--mode", mode]
        .iter()
        .map(|s| s.to_string())
        .collect();
    let mut errors = Vec::new();
    let mut collect = |b: &[u8], _: bool| errors.extend_from_slice(b);
    // The content goes on stdin, so its size is not bounded by a frame.
    let runtime = machines.runtime(&spec.id);
    let input = Some(content.as_bytes().to_vec());
    let status =
        control::run_with_input(&runtime, argv, Identity::User, Vec::new(), input, &mut collect).await?;
    if status != ExitStatus::Code(0) {
        return Err(err(format!("writing {path}: {}", String::from_utf8_lossy(&errors).trim())));
    }
    Ok(())
}

/// A tool's command line and environment.
pub type Launch = (Vec<String>, Vec<(String, String)>);

/// The command and environment a tool session starts with: the tool's,
/// with the `PATH` and environment of the tools it depends on.
pub fn launch(
    machines: &Machines,
    spec: &MachineSpec,
    manifest: &Manifest,
    session: &Session,
    args: &[String],
    warnings: &mut Vec<String>,
) -> io::Result<Launch> {
    let tool = &manifest.tool;
    let ctx = context(machines, spec, manifest, session, warnings)?;
    let mut tools: Vec<Manifest> = Vec::new();
    for name in &session.extra {
        let m = machines.manifest(name).map_err(|e| err(e.message))?;
        tools.extend(with_dependencies(machines, &m)?);
    }
    tools.extend(with_dependencies(machines, manifest)?);
    let mut seen = std::collections::BTreeSet::new();
    tools.retain(|t| seen.insert(t.tool.name.clone()));
    let mut env: Vec<(String, String)> = Vec::new();
    let mut set = |k: &str, v: String| {
        env.retain(|(ek, _)| ek != k);
        env.push((k.to_string(), v));
    };
    for t in &tools {
        let vars = t.tool.env.iter().chain(
            t.tool
                .models
                .as_ref()
                .filter(|_| ctx.models.is_some() && t.tool.name == tool.name)
                .map(|m| &m.env)
                .into_iter()
                .flatten(),
        );
        for (k, v) in vars {
            let value = render(v, &ctx).map_err(|e| err(format!("{}: env {k}: {e}", t.tool.name)))?;
            set(k, value);
        }
    }
    // The tool runs directly (its name is the session's command), with its
    // directories and its dependencies' first on PATH.
    let mut path: Vec<String> = vec![format!("{}/.local/bin", ctx.home)];
    for t in tools.iter().rev() {
        for p in &t.tool.path {
            let p = match p.strip_prefix("~/") {
                Some(rest) => format!("{}/{rest}", ctx.home),
                None => p.clone(),
            };
            if !path.contains(&p) {
                path.push(p);
            }
        }
    }
    path.push(DEFAULT_PATH.into());
    set("PATH", path.join(":"));
    if !env.iter().any(|(k, _)| k == "LANG") {
        env.push(("LANG".into(), "C.UTF-8".into()));
    }
    let config = machines.current_config();
    let settings = config.tools.get(&tool.name);
    let mut argv = Vec::new();
    for a in &tool.launch {
        argv.push(render(a, &ctx).map_err(|e| err(format!("{}: launch: {e}", tool.name)))?);
    }
    if ctx.yolo {
        argv.extend(tool.yolo.iter().cloned());
    }
    argv.extend(settings.map(|s| s.params.clone()).unwrap_or_default());
    argv.extend(args.iter().cloned());
    Ok((argv, env))
}

/// The guest's `PATH` without the tool's directories.
const DEFAULT_PATH: &str = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn shell_words_are_quoted() {
        assert_eq!(quote("a b"), "'a b'");
        assert_eq!(quote("it's"), r"'it'\''s'");
    }

    #[test]
    fn tools_run_with_their_directories_on_path() {
        let m = toby_tools::builtin().into_iter().find(|m| m.tool.name == "opencode").unwrap();
        let argv = shell(&m.tool, "true");
        assert_eq!(argv[..2], ["/bin/bash", "-lc"]);
        assert!(
            argv[2].starts_with("export PATH=\"$HOME/.local/bin:$HOME/.opencode/bin:$PATH\"; "),
            "{}",
            argv[2]
        );
    }
}
