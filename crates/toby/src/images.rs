//! `toby image`, `toby root`, `toby home` and `toby builder` commands.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::ExitCode;

use anyhow::{Context, bail};
use toby_daemon::builder::{Builder, console_and_log};
use toby_store::records::{ImageRecord, ImageSource};

use crate::cli::{BuilderCommand, HomeCommand, ImageCommand, RootCommand};
use crate::internal::load_config;

fn builder() -> anyhow::Result<Builder> {
    let (config, paths) = load_config()?;
    Ok(Builder::new(config, paths, std::env::current_exe()?))
}

/// A build log in the state directory, named after the time and kind.
fn build_log(b: &Builder, kind: &str) -> anyhow::Result<(std::fs::File, PathBuf)> {
    let dir = b.paths.state.join("builds");
    std::fs::create_dir_all(&dir)?;
    let path = dir.join(format!("{}-{kind}.log", toby_store::records::now()));
    Ok((std::fs::File::create(&path)?, path))
}

fn absolute(p: &Path) -> anyhow::Result<PathBuf> {
    std::fs::canonicalize(p).with_context(|| format!("{} does not exist", p.display()))
}

async fn build(b: &Builder, source: ImageSource, kind: &str) -> anyhow::Result<ImageRecord> {
    let (log, path) = build_log(b, kind)?;
    let mut out = console_and_log(log);
    let rec = b.build(source, &mut out).await.with_context(|| format!("build log: {}", path.display()))?;
    println!("Built image {}", rec.id);
    Ok(rec)
}

/// Prints rows under a header with columns sized to their contents.
fn table<const N: usize>(header: [&str; N], rows: Vec<[String; N]>) {
    let mut widths = header.map(str::len);
    for row in &rows {
        for (w, cell) in widths.iter_mut().zip(row) {
            *w = (*w).max(cell.len());
        }
    }
    let line = |cells: [&str; N]| {
        let mut out = String::new();
        for (i, (cell, w)) in cells.iter().zip(widths).enumerate() {
            if i + 1 == N {
                out.push_str(cell);
            } else {
                out.push_str(&format!("{cell:<w$}  "));
            }
        }
        println!("{}", out.trim_end());
    };
    line(header);
    for row in &rows {
        line(row.each_ref().map(String::as_str));
    }
}

fn age(created: u64) -> String {
    let secs = toby_store::records::now().saturating_sub(created);
    match secs {
        s if s < 3600 => format!("{} minutes ago", s / 60),
        s if s < 86400 => format!("{} hours ago", s / 3600),
        s => format!("{} days ago", s / 86400),
    }
}

pub async fn image(cmd: ImageCommand) -> anyhow::Result<ExitCode> {
    let b = builder()?;
    match cmd {
        ImageCommand::Prepare { all, default, mcp, project, rebuild, pull } => {
            if mcp.is_some() || project.is_some() || pull {
                bail!("preparing MCP and project images is not implemented yet");
            }
            let (log, path) = build_log(&b, "default")?;
            let mut out = console_and_log(log);
            let rec = b
                .prepare_default(rebuild, &mut out)
                .await
                .with_context(|| format!("build log: {}", path.display()))?;
            println!("Default image {}", rec.id);
            // Without flags, "everything the configuration needs" is the
            // default image until MCP and project images exist.
            let _ = default;
            if all {
                // Every root's source, rebuilt when it is behind.
                let mut sources: Vec<ImageSource> = Vec::new();
                for root in b.store.roots()? {
                    let img = b.store.image(&root.image)?;
                    if img.source != ImageSource::Default && !sources.contains(&img.source) {
                        sources.push(img.source);
                    }
                }
                let mut failed = 0;
                for source in sources {
                    let result = match b.current_image(&source) {
                        Ok(Some(_)) if !rebuild => continue,
                        Ok(_) => build(&b, source.clone(), "root").await.map(|_| ()),
                        Err(e) => Err(e.into()),
                    };
                    if let Err(e) = result {
                        eprintln!("toby: {}: {e:#}", source.describe());
                        failed += 1;
                    }
                }
                if failed > 0 {
                    bail!("{failed} of the roots' images could not be built");
                }
            }
        }
        ImageCommand::Build { dockerfile, context, mkosi } => {
            let source = match mkosi {
                Some(dir) => ImageSource::Mkosi { path: absolute(&dir)? },
                None => {
                    let context = absolute(&context.unwrap_or_else(|| PathBuf::from(".")))?;
                    let path = match dockerfile {
                        Some(f) => absolute(&f)?,
                        None => context.join("Dockerfile"),
                    };
                    ImageSource::Dockerfile { path, context }
                }
            };
            build(&b, source, "image").await?;
        }
        ImageCommand::Pull { reference } => {
            build(&b, ImageSource::Registry { reference }, "pull").await?;
        }
        ImageCommand::Import { archive } => {
            build(&b, ImageSource::Archive { path: absolute(&archive)? }, "import").await?;
        }
        ImageCommand::Ls => {
            let default = b.default_image()?.map(|i| i.id);
            let used: BTreeMap<String, Vec<String>> =
                b.store.roots()?.into_iter().fold(BTreeMap::new(), |mut m, r| {
                    m.entry(r.image).or_default().push(r.name);
                    m
                });
            let mut rows = Vec::new();
            for img in b.store.images()? {
                let mut source = img.source.describe();
                if Some(&img.id) == default.as_ref() {
                    source.push_str(" (current)");
                }
                let roots = used.get(&img.id).map(|r| r.join(",")).unwrap_or_default();
                rows.push([img.id, age(img.created), source, img.kernel_version, roots]);
            }
            table(["IMAGE", "CREATED", "SOURCE", "KERNEL", "ROOTS"], rows);
        }
        ImageCommand::Rm { id } => b.store.remove_image(&id)?,
        ImageCommand::Prune => {
            // Keep the newest image of every source and anything a root uses.
            let mut newest: BTreeMap<String, String> = BTreeMap::new();
            for img in b.store.images()? {
                newest.insert(format!("{:?}{}", img.source, img.arch), img.id);
            }
            let keep: Vec<String> = newest.into_values().collect();
            for id in b.store.prune_images(u64::MAX, &keep)? {
                println!("Removed image {id}");
            }
            for cache in b.prune_caches()? {
                println!("Removed build cache {}", cache.display());
            }
        }
    }
    Ok(ExitCode::SUCCESS)
}

pub async fn root(cmd: RootCommand) -> anyhow::Result<ExitCode> {
    let b = builder()?;
    let resolve = |image: &str| -> anyhow::Result<String> {
        if image == "default" {
            return Ok(b.default_image()?.context("there is no current default image")?.id);
        }
        Ok(b.store.image(image)?.id)
    };
    match cmd {
        RootCommand::Ls => {
            let mut rows = Vec::new();
            for r in b.store.roots()? {
                let newer =
                    b.store.image(&r.image).ok().and_then(|img| b.store.newer_image(&img).ok().flatten());
                let newer = newer.map(|n| n.id).unwrap_or_default();
                rows.push([r.name, r.image, age(r.created), newer]);
            }
            table(["ROOT", "IMAGE", "CREATED", "NEWER IMAGE"], rows);
        }
        RootCommand::Create { name, image } => {
            b.store.create_root(&name, &resolve(&image)?).await?;
        }
        RootCommand::Reset { name } => {
            b.store.reset_root(&name).await?;
        }
        RootCommand::Rebase { name, image } => {
            let target = match image {
                Some(i) => resolve(&i)?,
                None => {
                    let current = b.store.image(&b.store.root(&name)?.image)?;
                    b.store
                        .newer_image(&current)?
                        .context("the root already uses the newest image of its source")?
                        .id
                }
            };
            b.store.rebase_root(&name, &target).await?;
        }
        RootCommand::Rm { name } => b.store.remove_root(&name)?,
    }
    Ok(ExitCode::SUCCESS)
}

pub async fn home(cmd: HomeCommand) -> anyhow::Result<ExitCode> {
    let b = builder()?;
    match cmd {
        HomeCommand::Ls => {
            let mut rows = Vec::new();
            for h in b.store.homes()? {
                let user =
                    if h.formatted { h.username.clone() } else { format!("{} (unformatted)", h.username) };
                rows.push([h.name, user, h.uid.to_string(), age(h.created)]);
            }
            table(["HOME", "USER", "UID", "CREATED"], rows);
        }
        HomeCommand::Create { name, user, uid } => {
            let user = match user {
                Some(u) => u,
                None => nix::unistd::User::from_uid(nix::unistd::getuid())?
                    .map(|u| u.name)
                    .context("cannot determine your user name; pass --user")?,
            };
            if !toby_guest::helper::user::valid_name(&user) {
                bail!("{user:?} cannot be used as a guest user name; pass --user");
            }
            let uid = uid.unwrap_or_else(|| nix::unistd::getuid().as_raw());
            if uid == 0 {
                bail!("the home user cannot be root; pass --uid");
            }
            b.store.create_home(&name, &user, uid, toby_store::store::HOME_SIZE).await?;
            let (log, path) = build_log(&b, "home")?;
            let mut out = console_and_log(log);
            if let Err(e) = b.format_home(&name, &mut out).await {
                let _ = b.store.remove_home(&name);
                return Err(e).with_context(|| format!("build log: {}", path.display()));
            }
        }
        HomeCommand::Rm { name } => b.store.remove_home(&name)?,
    }
    Ok(ExitCode::SUCCESS)
}

pub async fn builder_cmd(cmd: BuilderCommand) -> anyhow::Result<ExitCode> {
    let b = builder()?;
    match cmd {
        BuilderCommand::Bootstrap { base, clean } => {
            if clean {
                let image = b.bootstrap_image();
                if image.exists() {
                    std::fs::remove_file(&image)?;
                }
                return Ok(ExitCode::SUCCESS);
            }
            let (log, path) = build_log(&b, "bootstrap")?;
            let mut out = console_and_log(log);
            let base = base.map(|p| absolute(&p)).transpose()?;
            let rec = b
                .bootstrap(base.as_deref(), &mut out)
                .await
                .with_context(|| format!("build log: {}", path.display()))?;
            println!("Default image {}", rec.id);
        }
        BuilderCommand::Status => {
            match b.default_image()? {
                Some(img) => println!("default image: {} ({})", img.id, age(img.created)),
                None => println!("default image: none current"),
            }
            let boot = b.bootstrap_image();
            println!(
                "bootstrap image: {}",
                if boot.exists() { boot.display().to_string() } else { "not downloaded".into() }
            );
        }
    }
    Ok(ExitCode::SUCCESS)
}
