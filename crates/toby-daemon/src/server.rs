//! The tobyd HTTP API on a Unix socket (plan §18).

use std::collections::BTreeMap;
use std::convert::Infallible;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};

use axum::Json;
use axum::extract::{Path, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get, post};
use bytes::Bytes;
use http_body::Frame;
use toby_api as api;
use toby_store::records::ImageSource;
use tokio::sync::mpsc;

use crate::builder::Builder;
use crate::builds::Builds;
use crate::machines::{Error, ErrorKind, Machines};

pub struct Daemon {
    pub machines: Arc<Machines>,
    pub builder: Arc<Builder>,
    pub builds: Builds,
}

type Shared = State<Arc<Daemon>>;
type ApiResult<T> = Result<Json<T>, Error>;

impl IntoResponse for Error {
    fn into_response(self) -> Response {
        let status = match self.kind {
            ErrorKind::BadRequest => StatusCode::BAD_REQUEST,
            ErrorKind::NotFound => StatusCode::NOT_FOUND,
            ErrorKind::Conflict => StatusCode::CONFLICT,
            ErrorKind::Internal => StatusCode::INTERNAL_SERVER_ERROR,
        };
        (status, Json(api::ApiError { code: self.code.into(), message: self.message })).into_response()
    }
}

fn bad(code: &'static str, message: impl Into<String>) -> Error {
    Error::new(ErrorKind::BadRequest, code, message)
}

pub fn router(daemon: Arc<Daemon>) -> axum::Router {
    axum::Router::new()
        .route("/v1/daemon", get(daemon_info))
        .route("/v1/machines", get(machines))
        .route("/v1/machines/ensure", post(ensure))
        .route("/v1/machines/{id}/stop", post(stop))
        .route("/v1/machines/{id}/attachments", post(add_attachment))
        .route("/v1/machines/{id}/attachments/{aid}", delete(remove_attachment))
        .route("/v1/sessions", get(sessions).post(create_session))
        .route("/v1/sessions/{id}/kill", post(kill_session))
        .route("/v1/images", get(images))
        .route("/v1/images/{id}", delete(remove_image))
        .route("/v1/images/prune", post(prune))
        .route("/v1/images/prepare", post(prepare))
        .route("/v1/builds", post(start_build))
        .route("/v1/builds/{id}", get(build_status))
        .route("/v1/builds/{id}/logs", get(build_logs))
        .route("/v1/bootstrap", post(bootstrap))
        .route("/v1/roots", get(roots).post(create_root))
        .route("/v1/roots/{name}/reset", post(reset_root))
        .route("/v1/roots/{name}/rebase", post(rebase_root))
        .route("/v1/roots/{name}", delete(remove_root))
        .route("/v1/homes", get(homes).post(create_home))
        .route("/v1/homes/{name}", delete(remove_home))
        .with_state(daemon)
}

async fn daemon_info(State(d): Shared) -> ApiResult<api::DaemonInfo> {
    let m = &d.machines;
    let backend = m.supervisor.backend();
    let linger = match backend {
        toby_config::global::Backend::SystemdUser => {
            toby_svc::systemd::linger(nix::unistd::getuid().as_raw()).await.ok()
        }
        toby_config::global::Backend::Direct => None,
    };
    Ok(Json(api::DaemonInfo {
        version: env!("CARGO_PKG_VERSION").into(),
        pid: std::process::id(),
        backend: match backend {
            toby_config::global::Backend::SystemdUser => "systemd-user".into(),
            toby_config::global::Backend::Direct => "direct".into(),
        },
        linger,
        state_dir: m.paths.state.display().to_string(),
        data_dir: m.paths.data.display().to_string(),
        runtime_dir: m.paths.runtime.display().to_string(),
    }))
}

// Machines

async fn machines(State(d): Shared) -> ApiResult<Vec<api::MachineInfo>> {
    Ok(Json(d.machines.list().await))
}

async fn ensure(State(d): Shared, Json(req): Json<api::EnsureMachine>) -> ApiResult<api::Ensured> {
    let spec = d.machines.ensure(req.home, req.root, req.ephemeral).await?;
    let warnings = d.machines.warnings().await;
    Ok(Json(api::Ensured { machine: d.machines.info(&spec).await, warnings }))
}

async fn stop(State(d): Shared, Path(id): Path<String>) -> ApiResult<()> {
    d.machines.stop(&id).await.map(Json)
}

async fn add_attachment(
    State(d): Shared,
    Path(id): Path<String>,
    Json(req): Json<api::AddAttachment>,
) -> ApiResult<api::AttachmentInfo> {
    d.machines.add_attachment(&id, req).await.map(Json)
}

async fn remove_attachment(State(d): Shared, Path((id, aid)): Path<(String, String)>) -> ApiResult<()> {
    d.machines.remove_attachment(&id, &aid).await.map(Json)
}

// Sessions

async fn create_session(
    State(d): Shared,
    Json(req): Json<api::CreateSession>,
) -> ApiResult<api::SessionCreated> {
    let (spec, id) = d.machines.create_session(req).await?;
    let runtime = d.machines.runtime(&spec.id);
    let warnings = d.machines.warnings().await;
    Ok(Json(api::SessionCreated {
        id,
        machine: spec.id,
        session_socket: runtime.session_sock().display().to_string(),
        control_socket: runtime.control_sock().display().to_string(),
        warnings,
    }))
}

async fn sessions(State(d): Shared) -> ApiResult<Vec<api::MachineSession>> {
    let out = d
        .machines
        .sessions()
        .await
        .into_iter()
        .map(|(machine, session)| {
            let runtime = d.machines.runtime(&machine);
            api::MachineSession {
                machine,
                session_socket: runtime.session_sock().display().to_string(),
                control_socket: runtime.control_sock().display().to_string(),
                session,
            }
        })
        .collect();
    Ok(Json(out))
}

async fn kill_session(
    State(d): Shared,
    Path(id): Path<String>,
    Json(req): Json<api::KillSession>,
) -> ApiResult<()> {
    d.machines.kill_session(&id, req.signal).await.map(Json)
}

// Images and builds

fn source(s: api::Source) -> ImageSource {
    match s {
        api::Source::Default => ImageSource::Default,
        api::Source::Dockerfile { path, context } => {
            ImageSource::Dockerfile { path: path.into(), context: context.into() }
        }
        api::Source::Mkosi { path } => ImageSource::Mkosi { path: path.into() },
        api::Source::Registry { reference } => ImageSource::Registry { reference },
        api::Source::Archive { path } => ImageSource::Archive { path: path.into() },
    }
}

async fn images(State(d): Shared) -> ApiResult<Vec<api::ImageInfo>> {
    let store = &d.builder.store;
    let default = d.builder.default_image()?.map(|i| i.id);
    let mut used: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for r in store.roots()? {
        used.entry(r.image).or_default().push(r.name);
    }
    let out = store
        .images()?
        .into_iter()
        .map(|img| api::ImageInfo {
            current_default: Some(&img.id) == default.as_ref(),
            roots: used.get(&img.id).cloned().unwrap_or_default(),
            source: img.source.describe(),
            created: img.created,
            kernel: img.kernel_version,
            id: img.id,
        })
        .collect();
    Ok(Json(out))
}

async fn remove_image(State(d): Shared, Path(id): Path<String>) -> ApiResult<()> {
    d.builder.store.remove_image(&id).map(Json).map_err(Into::into)
}

async fn prune(State(d): Shared) -> ApiResult<api::Pruned> {
    // Keep the newest image of every source and anything a root uses.
    let mut newest: BTreeMap<String, String> = BTreeMap::new();
    for img in d.builder.store.images()? {
        newest.insert(format!("{:?}{}", img.source, img.arch), img.id);
    }
    let keep: Vec<String> = newest.into_values().collect();
    let images = d.builder.store.prune_images(u64::MAX, &keep)?;
    let caches = d.builder.prune_caches()?.iter().map(|p| p.display().to_string()).collect();
    Ok(Json(api::Pruned { images, caches }))
}

fn started(b: Arc<crate::builds::Build>) -> Json<api::BuildStarted> {
    Json(api::BuildStarted { id: b.id.clone() })
}

async fn start_build(State(d): Shared, Json(req): Json<api::StartBuild>) -> ApiResult<api::BuildStarted> {
    let source = source(req.source);
    let builder = d.builder.clone();
    let b = d.builds.start(builder.paths.state.join("builds"), "image", move |out| {
        Box::pin(async move { builder.build(source, out).await.map(|r| Some(r.id)) })
    })?;
    Ok(started(b))
}

async fn prepare(State(d): Shared, Json(req): Json<api::Prepare>) -> ApiResult<api::BuildStarted> {
    let builder = d.builder.clone();
    let b = d.builds.start(builder.paths.state.join("builds"), "prepare", move |out| {
        Box::pin(async move { builder.prepare(req.all, req.rebuild, out).await.map(|r| Some(r.id)) })
    })?;
    Ok(started(b))
}

async fn bootstrap(State(d): Shared, Json(req): Json<api::Bootstrap>) -> ApiResult<api::BuildStarted> {
    let builder = d.builder.clone();
    let base = req.base.map(std::path::PathBuf::from);
    let b = d.builds.start(builder.paths.state.join("builds"), "bootstrap", move |out| {
        Box::pin(async move { builder.bootstrap(base.as_deref(), out).await.map(|r| Some(r.id)) })
    })?;
    Ok(started(b))
}

async fn build_status(State(d): Shared, Path(id): Path<String>) -> ApiResult<api::BuildStatus> {
    let b = d
        .builds
        .get(&id)
        .ok_or_else(|| Error::new(ErrorKind::NotFound, "build.not-found", "no such build"))?;
    Ok(Json(b.status()))
}

/// A response body fed from a channel.
struct ChannelBody(mpsc::Receiver<Bytes>);

impl http_body::Body for ChannelBody {
    type Data = Bytes;
    type Error = Infallible;

    fn poll_frame(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Bytes>, Infallible>>> {
        self.0.poll_recv(cx).map(|b| b.map(|b| Ok(Frame::data(b))))
    }
}

/// The build's output so far, then live until it finishes.
async fn build_logs(State(d): Shared, Path(id): Path<String>) -> Result<Response, Error> {
    let b = d
        .builds
        .get(&id)
        .ok_or_else(|| Error::new(ErrorKind::NotFound, "build.not-found", "no such build"))?;
    let (tx, rx) = mpsc::channel(16);
    tokio::spawn(async move {
        let mut changed = b.subscribe();
        let mut offset = 0;
        loop {
            changed.borrow_and_update();
            let (bytes, done) = b.output_from(offset);
            offset += bytes.len();
            if !bytes.is_empty() && tx.send(Bytes::from(bytes)).await.is_err() {
                return;
            }
            if done || changed.changed().await.is_err() {
                return;
            }
        }
    });
    Ok(axum::body::Body::new(ChannelBody(rx)).into_response())
}

// Roots and homes

async fn roots(State(d): Shared) -> ApiResult<Vec<api::RootInfo>> {
    let store = &d.builder.store;
    let out = store
        .roots()?
        .into_iter()
        .map(|r| {
            let newer = store.image(&r.image).ok().and_then(|img| store.newer_image(&img).ok().flatten());
            api::RootInfo {
                newer_image: newer.map(|n| n.id),
                name: r.name,
                image: r.image,
                created: r.created,
            }
        })
        .collect();
    Ok(Json(out))
}

fn resolve_image(d: &Daemon, image: &str) -> Result<String, Error> {
    if image == "default" {
        return d.builder.default_image()?.map(|i| i.id).ok_or_else(|| {
            Error::new(
                ErrorKind::NotFound,
                "image.no-default",
                "there is no current default image; build it with: toby image prepare --default",
            )
        });
    }
    Ok(d.builder.store.image(image)?.id)
}

async fn create_root(State(d): Shared, Json(req): Json<api::CreateRoot>) -> ApiResult<()> {
    let image = resolve_image(&d, &req.image)?;
    d.builder.store.create_root(&req.name, &image).await?;
    Ok(Json(()))
}

async fn reset_root(State(d): Shared, Path(name): Path<String>) -> ApiResult<()> {
    d.builder.store.reset_root(&name).await?;
    Ok(Json(()))
}

async fn rebase_root(
    State(d): Shared,
    Path(name): Path<String>,
    Json(req): Json<api::Rebase>,
) -> ApiResult<()> {
    let store = &d.builder.store;
    let target = match req.image {
        Some(i) => resolve_image(&d, &i)?,
        None => {
            let current = store.image(&store.root(&name)?.image)?;
            store
                .newer_image(&current)?
                .ok_or_else(|| {
                    Error::new(
                        ErrorKind::Conflict,
                        "root.up-to-date",
                        "the root already uses the newest image of its source",
                    )
                })?
                .id
        }
    };
    store.rebase_root(&name, &target).await?;
    Ok(Json(()))
}

async fn remove_root(State(d): Shared, Path(name): Path<String>) -> ApiResult<()> {
    d.builder.store.remove_root(&name).map(Json).map_err(Into::into)
}

async fn homes(State(d): Shared) -> ApiResult<Vec<api::HomeInfo>> {
    let out = d
        .builder
        .store
        .homes()?
        .into_iter()
        .map(|h| api::HomeInfo {
            name: h.name,
            username: h.username,
            uid: h.uid,
            formatted: h.formatted,
            created: h.created,
        })
        .collect();
    Ok(Json(out))
}

/// Creates the home's disk and formats it in a builder machine; the
/// returned build is the format job.
async fn create_home(State(d): Shared, Json(req): Json<api::CreateHome>) -> ApiResult<api::BuildStarted> {
    if !toby_guest::helper::user::valid_name(&req.username) {
        return Err(bad("home.invalid-user", format!("{:?} cannot be used as a user name", req.username)));
    }
    if req.uid == 0 {
        return Err(bad("home.invalid-uid", "the home user cannot be root"));
    }
    let builder = d.builder.clone();
    builder.store.create_home(&req.name, &req.username, req.uid, toby_store::store::HOME_SIZE).await?;
    let name = req.name.clone();
    let b = d.builds.start(builder.paths.state.join("builds"), "home", move |out| {
        Box::pin(async move {
            let result = builder.format_home(&name, out).await;
            if result.is_err() {
                let _ = builder.store.remove_home(&name);
            }
            result.map(|()| None)
        })
    })?;
    Ok(started(b))
}

async fn remove_home(State(d): Shared, Path(name): Path<String>) -> ApiResult<()> {
    d.builder.store.remove_home(&name).map(Json).map_err(Into::into)
}
