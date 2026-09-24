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

/// What the tool's templates see in this machine.
pub fn context(
    machines: &Machines,
    spec: &MachineSpec,
    tool: &Manifest,
    workspace: &str,
) -> io::Result<Context> {
    let (user, home) = user(machines, spec);
    let config = machines.current_config();
    let provider = config.tools.get(&tool.tool.name).and_then(|t| t.models.clone());
    let models = match provider {
        Some(p) if tool.tool.models.is_some() => {
            let Some(provider) = config.models.get(&p) else {
                return Err(err(format!(
                    "tool {} uses model provider {p}, which is not configured",
                    tool.tool.name
                )));
            };
            let speaks = tool.tool.models.as_ref().map(|m| m.protocol);
            let offers = match provider.protocol {
                toby_config::global::Protocol::Anthropic => toby_tools::Protocol::Anthropic,
                toby_config::global::Protocol::Openai => toby_tools::Protocol::Openai,
            };
            if speaks != Some(offers) {
                return Err(err(format!(
                    "tool {} speaks another API than model provider {p}",
                    tool.tool.name
                )));
            }
            let token = toby_proxy::ensure_token(&machines.paths, &spec.id)?;
            Some(ModelsContext { url: format!("http://{MODELS_LISTEN}/{p}"), token })
        }
        _ => None,
    };
    Ok(Context {
        models,
        user,
        home,
        workspace: workspace.into(),
        connect: CONNECT.into(),
        ..Default::default()
    })
}

async fn run_user(
    machines: &Machines,
    spec: &MachineSpec,
    argv: Vec<String>,
    out: Output<'_>,
) -> io::Result<ExitStatus> {
    control::run(&machines.runtime(&spec.id), argv, Identity::User, Vec::new(), out).await
}

/// Checks the root, installs or updates the tool, and writes its files.
pub async fn prepare(
    machines: &Machines,
    spec: &MachineSpec,
    manifest: &Manifest,
    upgrade: bool,
    out: Output<'_>,
) -> io::Result<()> {
    let tool = &manifest.tool;
    let name = &tool.name;
    for r in &tool.requires {
        if r.is_empty() || !r.bytes().all(|b| b.is_ascii_alphanumeric() || b"._+-".contains(&b)) {
            return Err(err(format!("tool {name} requires an invalid command name {r:?}")));
        }
    }
    let requires = tool.requires.join(" ");
    let script = format!(
        "missing=; for c in {requires}; do command -v \"$c\" >/dev/null 2>&1 || missing=\"$missing $c\"; done; \
         if [ -n \"$missing\" ]; then echo \"the root lacks:$missing; they belong in the image\" >&2; exit 3; fi"
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
        let script = match (installed, &tool.update) {
            (true, Some(update)) => &update.script,
            _ => &tool.install.script,
        };
        let verb = if installed { "Updating" } else { "Installing" };
        out(format!("==> {verb} {name}\n").as_bytes(), false);
        if run_user(machines, spec, shell(tool, script), &mut *out).await? != ExitStatus::Code(0) {
            return Err(err(format!("installing {name} failed")));
        }
        if run_user(machines, spec, shell(tool, &format!("{check} >/dev/null 2>&1")), &mut sink).await?
            != ExitStatus::Code(0)
        {
            return Err(err(format!("{name} was installed, but `{}` fails", tool.check.join(" "))));
        }
    }

    let ctx = context(machines, spec, manifest, "")?;
    for f in &tool.files {
        let content = render(&f.template, &ctx).map_err(|e| err(format!("{name}: {}: {e}", f.path)))?;
        patch_file(machines, spec, &f.path, &content, f.format, f.mode).await?;
    }
    write_mcp(machines, spec, manifest, &ctx).await
}

/// Writes the tool's MCP servers into its configuration (plan §16.3):
/// Toby's own always, and those `[tools.<name>].mcp` names.
async fn write_mcp(
    machines: &Machines,
    spec: &MachineSpec,
    manifest: &Manifest,
    ctx: &Context,
) -> io::Result<()> {
    use toby_config::global::{McpKind, Placement};
    let tool = &manifest.tool;
    let Some(mcp) = &tool.mcp else { return Ok(()) };
    let config = machines.current_config();
    let wanted = config.tools.get(&tool.name).map(|t| t.mcp.clone()).unwrap_or_default();
    let mut servers = serde_json::Map::new();
    for name in std::iter::once("toby".to_string()).chain(wanted) {
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
                        .ok_or_else(|| err(format!("{} cannot use HTTP MCP servers", tool.name)))?
                }
                (McpKind::Stdio, Placement::Isolated) => &mcp.entry,
                (McpKind::Stdio, Placement::Machine) => {
                    ctx.command = server.command[0].clone();
                    ctx.args = server.command[1..].to_vec();
                    mcp.command_entry
                        .as_ref()
                        .ok_or_else(|| err(format!("{} cannot run MCP servers itself", tool.name)))?
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

/// The command and environment a tool session starts with.
pub fn launch(
    machines: &Machines,
    spec: &MachineSpec,
    manifest: &Manifest,
    workspace: &str,
    args: &[String],
    yolo: bool,
) -> io::Result<Launch> {
    let tool = &manifest.tool;
    let ctx = context(machines, spec, manifest, workspace)?;
    let mut env = Vec::new();
    let mut add = |vars: &std::collections::BTreeMap<String, String>| -> io::Result<()> {
        for (k, v) in vars {
            let value = render(v, &ctx).map_err(|e| err(format!("{}: env {k}: {e}", tool.name)))?;
            env.push((k.clone(), value));
        }
        Ok(())
    };
    add(&tool.env)?;
    if ctx.models.is_some()
        && let Some(models) = &tool.models
    {
        add(&models.env)?;
    }
    // The tool runs directly (its name is the session's command), with its
    // directories first on PATH.
    let mut path: Vec<String> = vec![format!("{}/.local/bin", ctx.home)];
    for p in &tool.path {
        path.push(match p.strip_prefix("~/") {
            Some(rest) => format!("{}/{rest}", ctx.home),
            None => p.clone(),
        });
    }
    path.push(DEFAULT_PATH.into());
    env.push(("PATH".into(), path.join(":")));
    if !env.iter().any(|(k, _)| k == "LANG") {
        env.push(("LANG".into(), "C.UTF-8".into()));
    }
    let config = machines.current_config();
    let settings = config.tools.get(&tool.name);
    let mut argv: Vec<String> = tool.launch.clone();
    if yolo || config.settings.yolo {
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
