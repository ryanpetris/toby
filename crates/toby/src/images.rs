//! `toby image`, `toby root`, `toby home` and `toby builder` commands.

use std::path::{Path, PathBuf};
use std::process::ExitCode;

use anyhow::Context;
use toby_api::Source;

use crate::api::{Api, segment};
use crate::cli::{BuilderCommand, HomeCommand, ImageCommand, RootCommand};
use crate::table::{age, print};

fn absolute(p: &Path) -> anyhow::Result<String> {
    let p = std::fs::canonicalize(p).with_context(|| p.display().to_string())?;
    p.to_str().map(str::to_string).with_context(|| format!("{} is not UTF-8", p.display()))
}

async fn build(api: &Api, source: Source) -> anyhow::Result<()> {
    let started: toby_api::BuildStarted = api.post("/v1/builds", &toby_api::StartBuild { source }).await?;
    let status = api.follow_build(&started.id).await?;
    if let Some(image) = status.image {
        println!("Built image {image}");
    }
    Ok(())
}

pub async fn image(cmd: ImageCommand) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    match cmd {
        ImageCommand::Prepare { all, default, mcp, project, rebuild, pull } => {
            // Without a choice: the default image, every MCP server's, and
            // the current project's.
            let nothing = !all && !default && mcp.is_none() && project.is_none();
            let mcp = if nothing { Some(Vec::new()) } else { mcp };
            let mut sources = Vec::new();
            if nothing || all || project.is_some() {
                let config_dir =
                    api.paths.global_config().parent().map(Path::to_path_buf).unwrap_or_default();
                let home = toby_config::paths::home_dir()?;
                let cwd = std::env::current_dir()?;
                let path = project.flatten();
                let found =
                    crate::launch::project_image(&api.config, &config_dir, &home, &cwd, path.as_deref());
                match found {
                    Ok((image, warnings)) => {
                        api.warn(&warnings);
                        let source = match &image {
                            Some((image, dir)) => crate::tool::api_source(image, dir)?,
                            None => None,
                        };
                        match source {
                            Some(Ok(s)) => sources.push(s),
                            Some(Err(id)) => println!("The project uses image {id}"),
                            None => {}
                        }
                    }
                    Err(e) => return Err(e),
                }
            }
            let req = toby_api::Prepare { all, default, mcp, sources, rebuild, pull };
            let started: toby_api::BuildStarted = api.post("/v1/images/prepare", &req).await?;
            let status = api.follow_build(&started.id).await?;
            if let Some(image) = status.image {
                println!("Default image {image}");
            }
        }
        ImageCommand::Build { dockerfile, context, mkosi } => {
            let source = match mkosi {
                Some(dir) => Source::Mkosi { path: absolute(&dir)? },
                None => {
                    let context = absolute(&context.unwrap_or_else(|| PathBuf::from(".")))?;
                    let path = match dockerfile {
                        Some(f) => absolute(&f)?,
                        None => format!("{context}/Dockerfile"),
                    };
                    Source::Dockerfile { path, context }
                }
            };
            build(&api, source).await?;
        }
        ImageCommand::Pull { reference } => build(&api, Source::Registry { reference }).await?,
        ImageCommand::Import { archive } => {
            build(&api, Source::Archive { path: absolute(&archive)? }).await?
        }
        ImageCommand::Ls => {
            let images: Vec<toby_api::ImageInfo> = api.get("/v1/images").await?;
            let rows = images
                .into_iter()
                .map(|i| {
                    let source = if i.current_default { format!("{} (current)", i.source) } else { i.source };
                    [i.id, age(i.created), source, i.kernel, i.roots.join(",")]
                })
                .collect();
            print(["IMAGE", "CREATED", "SOURCE", "KERNEL", "ROOTS"], rows);
        }
        ImageCommand::Rm { id } => api.delete(&format!("/v1/images/{}", segment(&id))).await?,
        ImageCommand::Prune => {
            let pruned: toby_api::Pruned = api.post("/v1/images/prune", &()).await?;
            for id in pruned.images {
                println!("Removed image {id}");
            }
            for cache in pruned.caches {
                println!("Removed build cache {cache}");
            }
        }
    }
    Ok(ExitCode::SUCCESS)
}

pub async fn root(cmd: RootCommand) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    match cmd {
        RootCommand::Ls => {
            let roots: Vec<toby_api::RootInfo> = api.get("/v1/roots").await?;
            let rows = roots
                .into_iter()
                .map(|r| [r.name, r.image, age(r.created), r.newer_image.unwrap_or_default()])
                .collect();
            print(["ROOT", "IMAGE", "CREATED", "NEWER IMAGE"], rows);
        }
        RootCommand::Create { name, image } => {
            let () = api.post("/v1/roots", &toby_api::CreateRoot { name, image, source: None }).await?;
        }
        RootCommand::Reset { name } => api.post(&format!("/v1/roots/{}/reset", segment(&name)), &()).await?,
        RootCommand::Rebase { name, image } => {
            api.post(&format!("/v1/roots/{}/rebase", segment(&name)), &toby_api::Rebase { image }).await?
        }
        RootCommand::Rm { name } => api.delete(&format!("/v1/roots/{}", segment(&name))).await?,
    }
    Ok(ExitCode::SUCCESS)
}

pub async fn home(cmd: HomeCommand) -> anyhow::Result<ExitCode> {
    let api = Api::connect().await?;
    match cmd {
        HomeCommand::Ls => {
            let homes: Vec<toby_api::HomeInfo> = api.get("/v1/homes").await?;
            let rows = homes
                .into_iter()
                .map(|h| {
                    let user = if h.formatted { h.username } else { format!("{} (unformatted)", h.username) };
                    [h.name, user, h.uid.to_string(), age(h.created)]
                })
                .collect();
            print(["HOME", "USER", "UID", "CREATED"], rows);
        }
        HomeCommand::Create { name, user, uid } => {
            let username = match user {
                Some(u) => u,
                None => nix::unistd::User::from_uid(nix::unistd::getuid())?
                    .map(|u| u.name)
                    .context("cannot determine your user name; pass --user")?,
            };
            let uid = uid.unwrap_or_else(|| nix::unistd::getuid().as_raw());
            let started: toby_api::BuildStarted =
                api.post("/v1/homes", &toby_api::CreateHome { name, username, uid }).await?;
            api.follow_build(&started.id).await?;
        }
        HomeCommand::Rm { name } => api.delete(&format!("/v1/homes/{}", segment(&name))).await?,
    }
    Ok(ExitCode::SUCCESS)
}

pub async fn builder_cmd(cmd: BuilderCommand) -> anyhow::Result<ExitCode> {
    let (config, paths) = crate::internal::load_config()?;
    let local = toby_daemon::builder::Builder::new(config, paths, PathBuf::new());
    match cmd {
        BuilderCommand::Bootstrap { base: _, clean: true } => {
            let image = local.bootstrap_image();
            if image.exists() {
                // A running bootstrap holds it.
                let _unused = toby_store::store::lock_disk(&image)?;
                std::fs::remove_file(&image)?;
            }
        }
        BuilderCommand::Bootstrap { base, clean: false } => {
            let api = Api::connect().await?;
            let base = base.map(|p| absolute(&p)).transpose()?;
            let started: toby_api::BuildStarted =
                api.post("/v1/bootstrap", &toby_api::Bootstrap { base }).await?;
            if let Some(image) = api.follow_build(&started.id).await?.image {
                println!("Default image {image}");
            }
        }
        BuilderCommand::Status => {
            let api = Api::connect().await?;
            let images: Vec<toby_api::ImageInfo> = api.get("/v1/images").await?;
            match images.iter().find(|i| i.current_default) {
                Some(img) => println!("default image: {} ({})", img.id, age(img.created)),
                None => println!("default image: none current"),
            }
            let boot = local.bootstrap_image();
            println!(
                "bootstrap image: {}",
                if boot.exists() { boot.display().to_string() } else { "not downloaded".into() }
            );
        }
    }
    Ok(ExitCode::SUCCESS)
}
