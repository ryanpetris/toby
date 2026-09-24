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
    pub approvals: crate::approvals::Approvals,
    pub events: crate::events::Events,
    pub web: crate::web::Web,
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

/// The API's OpenAPI document (plan §18).
#[derive(utoipa::OpenApi)]
#[openapi(
    info(title = "tobyd", description = "Toby's control plane, on its Unix socket and the web UI's port."),
    paths(
        daemon_info,
        machines,
        ensure,
        stop,
        machine_logs,
        add_attachment,
        remove_attachment,
        add_forward,
        remove_forward,
        prepare_tool,
        sessions,
        create_session,
        kill_session,
        images,
        remove_image,
        prune,
        prepare,
        start_build,
        builds,
        build_status,
        build_logs,
        bootstrap,
        roots,
        create_root,
        reset_root,
        rebase_root,
        remove_root,
        homes,
        create_home,
        remove_home,
        collect_versions,
        mcp_servers,
        mcp_logs,
        approvals,
        decide,
        events,
        web_token
    )
)]
struct ApiDoc;

async fn openapi() -> Json<utoipa::openapi::OpenApi> {
    Json(<ApiDoc as utoipa::OpenApi>::openapi())
}

pub fn router(daemon: Arc<Daemon>) -> axum::Router {
    axum::Router::new()
        .route("/v1/openapi.json", get(openapi))
        .route("/v1/daemon", get(daemon_info))
        .route("/v1/machines", get(machines))
        .route("/v1/machines/ensure", post(ensure))
        .route("/v1/machines/{id}/stop", post(stop))
        .route("/v1/machines/{id}/logs", get(machine_logs))
        .route("/v1/mcp", get(mcp_servers))
        .route("/v1/mcp/{name}/logs", get(mcp_logs))
        .route("/v1/machines/{id}/attachments", post(add_attachment))
        .route("/v1/machines/{id}/attachments/{aid}", delete(remove_attachment))
        .route("/v1/machines/{id}/forwards", post(add_forward))
        .route("/v1/machines/{id}/forwards/{fid}", delete(remove_forward))
        .route("/v1/machines/{id}/tools/{name}/prepare", post(prepare_tool))
        .route("/v1/sessions", get(sessions).post(create_session))
        .route("/v1/sessions/{id}/kill", post(kill_session))
        .route("/v1/images", get(images))
        .route("/v1/images/{id}", delete(remove_image))
        .route("/v1/images/prune", post(prune))
        .route("/v1/images/prepare", post(prepare))
        .route("/v1/builds/{id}", get(build_status))
        .route("/v1/builds/{id}/logs", get(build_logs))
        .route("/v1/bootstrap", post(bootstrap))
        .route("/v1/roots", get(roots).post(create_root))
        .route("/v1/roots/{name}/reset", post(reset_root))
        .route("/v1/roots/{name}/rebase", post(rebase_root))
        .route("/v1/roots/{name}", delete(remove_root))
        .route("/v1/homes", get(homes).post(create_home))
        .route("/v1/homes/{name}", delete(remove_home))
        .route("/v1/versions/gc", post(collect_versions))
        .route("/v1/events", get(events))
        .route("/v1/web/token", post(web_token))
        .route("/v1/builds", get(builds).post(start_build))
        .route("/v1/approvals", get(approvals))
        .route("/v1/approvals/{id}", post(decide))
        .with_state(daemon)
}

#[utoipa::path(get, path = "/v1/daemon", tag = "daemon", responses((status = 200, body = api::DaemonInfo), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn daemon_info(State(d): Shared) -> ApiResult<api::DaemonInfo> {
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

#[utoipa::path(get, path = "/v1/machines", tag = "machines", responses((status = 200, body = Vec<api::MachineInfo>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn machines(State(d): Shared) -> ApiResult<Vec<api::MachineInfo>> {
    Ok(Json(d.machines.list().await))
}

#[utoipa::path(post, path = "/v1/machines/ensure", tag = "machines", request_body = api::EnsureMachine, responses((status = 200, body = api::Ensured), (status = "4XX", body = api::ApiError)))]
async fn ensure(State(d): Shared, Json(req): Json<api::EnsureMachine>) -> ApiResult<api::Ensured> {
    if req.cpus.is_some_and(|c| !(1..=256).contains(&c)) {
        return Err(bad("machine.invalid-cpus", "cpus must be between 1 and 256"));
    }
    if req.memory.as_deref().is_some_and(|m| toby_config::machine::parse_size(m).is_none()) {
        return Err(bad("machine.invalid-memory", "memory must be a size such as 8G"));
    }
    let spec = d.machines.ensure(req).await?;
    let warnings = d.machines.warnings().await;
    Ok(Json(api::Ensured { machine: d.machines.info(&spec).await, warnings }))
}

#[utoipa::path(post, path = "/v1/machines/{id}/stop", tag = "machines", params(("id" = String, Path)), responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn stop(State(d): Shared, Path(id): Path<String>) -> ApiResult<()> {
    d.machines.stop(&id).await.map(Json)
}

#[utoipa::path(post, path = "/v1/machines/{id}/attachments", tag = "machines", params(("id" = String, Path)), request_body = api::AddAttachment, responses((status = 200, body = api::AttachmentInfo), (status = "4XX", body = api::ApiError)))]
async fn add_attachment(
    State(d): Shared,
    Path(id): Path<String>,
    Json(req): Json<api::AddAttachment>,
) -> ApiResult<api::AttachmentInfo> {
    d.machines.add_attachment(&id, req, None).await.map(Json)
}

#[utoipa::path(delete, path = "/v1/machines/{id}/attachments/{aid}", tag = "machines", params(("id" = String, Path), ("aid" = String, Path)), responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn remove_attachment(State(d): Shared, Path((id, aid)): Path<(String, String)>) -> ApiResult<()> {
    d.machines.remove_attachment(&id, &aid).await.map(Json)
}

#[utoipa::path(post, path = "/v1/machines/{id}/forwards", tag = "machines", params(("id" = String, Path)), request_body = api::AddForward, responses((status = 200, body = api::ForwardInfo), (status = "4XX", body = api::ApiError)))]
async fn add_forward(
    State(d): Shared,
    Path(id): Path<String>,
    Json(req): Json<api::AddForward>,
) -> ApiResult<api::ForwardInfo> {
    d.machines.add_forward(&id, req, None).await.map(Json)
}

#[utoipa::path(delete, path = "/v1/machines/{id}/forwards/{fid}", tag = "machines", params(("id" = String, Path), ("fid" = String, Path)), responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn remove_forward(State(d): Shared, Path((id, fid)): Path<(String, String)>) -> ApiResult<()> {
    d.machines.remove_forward(&id, &fid).await.map(Json)
}

#[utoipa::path(post, path = "/v1/machines/{id}/tools/{name}/prepare", tag = "machines", params(("id" = String, Path), ("name" = String, Path)), request_body = api::PrepareTool, responses((status = 200, body = api::BuildStarted), (status = "4XX", body = api::ApiError)))]
async fn prepare_tool(
    State(d): Shared,
    Path((id, name)): Path<(String, String)>,
    Json(req): Json<api::PrepareTool>,
) -> ApiResult<api::BuildStarted> {
    let spec = d.machines.record(&id)?;
    let manifest = d.machines.manifest(&name)?;
    let machines = d.machines.clone();
    let b = d.builds.start(d.builder.paths.state.join("builds"), "tool", move |out| {
        Box::pin(async move {
            // One tool operation at a time per machine (plan §16.1).
            let lock = machines.tool_lock(&spec.id);
            let _lock = lock.lock().await;
            crate::tools::prepare(&machines, &spec, &manifest, req.upgrade, out).await.map(|()| None)
        })
    })?;
    Ok(started(b))
}

// Sessions

#[utoipa::path(post, path = "/v1/sessions", tag = "sessions", request_body = api::CreateSession, responses((status = 200, body = api::SessionCreated), (status = "4XX", body = api::ApiError)))]
async fn create_session(
    State(d): Shared,
    Json(req): Json<api::CreateSession>,
) -> ApiResult<api::SessionCreated> {
    let (spec, id, mut warnings) = d.machines.create_session(req).await?;
    let runtime = d.machines.runtime(&spec.id);
    warnings.extend(d.machines.warnings().await);
    Ok(Json(api::SessionCreated {
        id,
        machine: spec.id,
        session_socket: runtime.session_sock().display().to_string(),
        control_socket: runtime.control_sock().display().to_string(),
        warnings,
    }))
}

#[utoipa::path(get, path = "/v1/sessions", tag = "sessions", responses((status = 200, body = Vec<api::MachineSession>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn sessions(State(d): Shared) -> ApiResult<Vec<api::MachineSession>> {
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

#[utoipa::path(post, path = "/v1/sessions/{id}/kill", tag = "sessions", params(("id" = String, Path)), request_body = api::KillSession, responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
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

#[utoipa::path(get, path = "/v1/images", tag = "images", responses((status = 200, body = Vec<api::ImageInfo>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn images(State(d): Shared) -> ApiResult<Vec<api::ImageInfo>> {
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

#[utoipa::path(delete, path = "/v1/images/{id}", tag = "images", params(("id" = String, Path)), responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn remove_image(State(d): Shared, Path(id): Path<String>) -> ApiResult<()> {
    d.builder.store.remove_image(&id).map(Json).map_err(Into::into)
}

#[utoipa::path(post, path = "/v1/images/prune", tag = "images", responses((status = 200, body = api::Pruned), (status = "4XX", body = api::ApiError)))]
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

#[utoipa::path(post, path = "/v1/builds", tag = "images", request_body = api::StartBuild, responses((status = 200, body = api::BuildStarted), (status = "4XX", body = api::ApiError)))]
async fn start_build(State(d): Shared, Json(req): Json<api::StartBuild>) -> ApiResult<api::BuildStarted> {
    let source = source(req.source);
    let builder = d.builder.clone();
    let b = d.builds.start(builder.paths.state.join("builds"), "image", move |out| {
        Box::pin(async move { builder.build(source, out).await.map(|r| Some(r.id)) })
    })?;
    Ok(started(b))
}

#[utoipa::path(post, path = "/v1/images/prepare", tag = "images", request_body = api::Prepare, responses((status = 200, body = api::BuildStarted), (status = "4XX", body = api::ApiError)))]
async fn prepare(State(d): Shared, Json(req): Json<api::Prepare>) -> ApiResult<api::BuildStarted> {
    let builder = d.builder.clone();
    let b = d.builds.start(builder.paths.state.join("builds"), "prepare", move |out| {
        Box::pin(async move { builder.prepare(req.all, req.rebuild, out).await.map(|r| Some(r.id)) })
    })?;
    Ok(started(b))
}

#[utoipa::path(post, path = "/v1/bootstrap", tag = "images", request_body = api::Bootstrap, responses((status = 200, body = api::BuildStarted), (status = "4XX", body = api::ApiError)))]
async fn bootstrap(State(d): Shared, Json(req): Json<api::Bootstrap>) -> ApiResult<api::BuildStarted> {
    let builder = d.builder.clone();
    let base = req.base.map(std::path::PathBuf::from);
    let b = d.builds.start(builder.paths.state.join("builds"), "bootstrap", move |out| {
        Box::pin(async move { builder.bootstrap(base.as_deref(), out).await.map(|r| Some(r.id)) })
    })?;
    Ok(started(b))
}

#[utoipa::path(get, path = "/v1/builds/{id}", tag = "images", params(("id" = String, Path)), responses((status = 200, body = api::BuildStatus), (status = "4XX", body = api::ApiError)))]
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
#[utoipa::path(get, path = "/v1/builds/{id}/logs", tag = "images", params(("id" = String, Path)), responses((status = 200, description = "The output so far, then streamed until the build ends", content_type = "text/plain"), (status = "4XX", body = api::ApiError)))]
async fn build_logs(State(d): Shared, Path(id): Path<String>) -> Result<Response, Error> {
    let b = d
        .builds
        .get(&id)
        .ok_or_else(|| Error::new(ErrorKind::NotFound, "build.not-found", "no such build"))?;
    let (tx, rx) = mpsc::channel(16);
    tokio::spawn(async move {
        let mut changed = b.subscribe();
        let mut offset = 0u64;
        loop {
            changed.borrow_and_update();
            // Finished is read first, so the last output is read after it.
            let done = b.finished();
            loop {
                let Ok(bytes) = b.output_from(offset) else { return };
                if bytes.is_empty() {
                    break;
                }
                offset += bytes.len() as u64;
                if tx.send(Bytes::from(bytes)).await.is_err() {
                    return;
                }
            }
            if done || changed.changed().await.is_err() {
                return;
            }
        }
    });
    Ok(axum::body::Body::new(ChannelBody(rx)).into_response())
}

// Roots and homes

#[utoipa::path(get, path = "/v1/roots", tag = "roots", responses((status = 200, body = Vec<api::RootInfo>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn roots(State(d): Shared) -> ApiResult<Vec<api::RootInfo>> {
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

#[utoipa::path(post, path = "/v1/roots", tag = "roots", request_body = api::CreateRoot, responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn create_root(State(d): Shared, Json(req): Json<api::CreateRoot>) -> ApiResult<()> {
    let image = resolve_image(&d, &req.image)?;
    d.builder.store.create_root(&req.name, &image).await?;
    Ok(Json(()))
}

#[utoipa::path(post, path = "/v1/roots/{name}/reset", tag = "roots", params(("name" = String, Path)), responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn reset_root(State(d): Shared, Path(name): Path<String>) -> ApiResult<()> {
    d.builder.store.reset_root(&name).await?;
    Ok(Json(()))
}

#[utoipa::path(post, path = "/v1/roots/{name}/rebase", tag = "roots", params(("name" = String, Path)), request_body = api::Rebase, responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
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

#[utoipa::path(delete, path = "/v1/roots/{name}", tag = "roots", params(("name" = String, Path)), responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn remove_root(State(d): Shared, Path(name): Path<String>) -> ApiResult<()> {
    d.builder.store.remove_root(&name).map(Json).map_err(Into::into)
}

#[utoipa::path(get, path = "/v1/homes", tag = "homes", responses((status = 200, body = Vec<api::HomeInfo>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn homes(State(d): Shared) -> ApiResult<Vec<api::HomeInfo>> {
    let out = d
        .builder
        .store
        .homes()?
        .into_iter()
        .map(|h| api::HomeInfo {
            name: h.name,
            username: h.username,
            uid: h.uid,
            default_root: h.default_root,
            formatted: h.formatted,
            created: h.created,
        })
        .collect();
    Ok(Json(out))
}

/// Creates the home's disk and formats it in a builder machine; the
/// returned build is the format job.
#[utoipa::path(post, path = "/v1/homes", tag = "homes", request_body = api::CreateHome, responses((status = 200, body = api::BuildStarted), (status = "4XX", body = api::ApiError)))]
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

#[utoipa::path(post, path = "/v1/versions/gc", tag = "daemon", responses((status = 200, body = api::VersionsCollected), (status = "4XX", body = api::ApiError)))]
async fn collect_versions(State(d): Shared) -> ApiResult<api::VersionsCollected> {
    let c = crate::versions::collect(&d.machines).await?;
    Ok(Json(api::VersionsCollected {
        removed: c.removed,
        kept: c.used.into_iter().collect(),
        failed: c.failed,
    }))
}

#[utoipa::path(get, path = "/v1/machines/{id}/logs", tag = "machines", params(("id" = String, Path)), responses((status = 101, description = "A WebSocket of log lines"), (status = "4XX", body = api::ApiError)))]
async fn machine_logs(
    State(d): Shared,
    Path(id): Path<String>,
    ws: axum::extract::WebSocketUpgrade,
) -> Result<Response, Error> {
    d.machines.record(&id)?;
    Ok(ws.on_upgrade(move |socket| crate::logs::machine(d, id, socket)))
}

#[utoipa::path(get, path = "/v1/mcp", tag = "mcp", responses((status = 200, body = Vec<api::McpInfo>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn mcp_servers(State(d): Shared) -> ApiResult<Vec<api::McpInfo>> {
    use toby_config::global::{McpKind, Placement};
    let config = d.machines.current_config();
    let machines = d.machines.list().await;
    let mut out = vec![api::McpInfo {
        name: "toby".into(),
        kind: "built-in".into(),
        placement: "daemon".into(),
        machine: None,
        state: None,
    }];
    for (name, s) in &config.mcp {
        let pair = crate::services::pair_name(name);
        let m = machines.iter().find(|m| m.home.as_deref() == Some(pair.as_str()));
        out.push(api::McpInfo {
            name: name.clone(),
            kind: match s.kind {
                McpKind::Stdio => "stdio",
                McpKind::Http => "http",
            }
            .into(),
            placement: match (s.kind, s.placement()) {
                (McpKind::Http, _) => "proxy",
                (_, Placement::Machine) => "machine",
                (_, Placement::Isolated) => "isolated",
            }
            .into(),
            machine: m.map(|m| m.id.clone()),
            state: m.map(|m| m.state.clone()),
        });
    }
    Ok(Json(out))
}

#[utoipa::path(get, path = "/v1/mcp/{name}/logs", tag = "mcp", params(("name" = String, Path)), responses((status = 101, description = "A WebSocket of log lines"), (status = "4XX", body = api::ApiError)))]
async fn mcp_logs(
    State(d): Shared,
    Path(name): Path<String>,
    ws: axum::extract::WebSocketUpgrade,
) -> Response {
    ws.on_upgrade(move |socket| crate::logs::mcp(d, name, socket))
}

#[utoipa::path(post, path = "/v1/web/token", tag = "daemon", responses((status = 200, body = api::WebToken), (status = "4XX", body = api::ApiError)))]
async fn web_token(State(d): Shared) -> ApiResult<api::WebToken> {
    let url = d.web.login_url(d.clone()).await?;
    Ok(Json(api::WebToken { url }))
}

#[utoipa::path(get, path = "/v1/events", tag = "daemon", responses((status = 101, description = "A WebSocket of events", body = api::Event), (status = "4XX", body = api::ApiError)))]
async fn events(State(d): Shared, ws: axum::extract::WebSocketUpgrade) -> Response {
    ws.on_upgrade(move |socket| crate::events::serve(d, socket))
}

#[utoipa::path(get, path = "/v1/builds", tag = "images", responses((status = 200, body = Vec<api::BuildStatus>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn builds(State(d): Shared) -> ApiResult<Vec<api::BuildStatus>> {
    Ok(Json(d.builds.list().iter().map(|b| b.status()).collect()))
}

#[utoipa::path(get, path = "/v1/approvals", tag = "approvals", responses((status = 200, body = Vec<api::ApprovalInfo>), (status = "4XX", body = api::ApiError)))]
pub(crate) async fn approvals(State(d): Shared) -> ApiResult<Vec<api::ApprovalInfo>> {
    let list = d.approvals.list()?;
    Ok(Json(
        list.into_iter()
            .map(|a| api::ApprovalInfo {
                id: a.id,
                created: a.created,
                machine: a.machine,
                kind: a.kind,
                summary: a.summary,
                detail: a.detail,
                status: a.status,
            })
            .collect(),
    ))
}

#[utoipa::path(post, path = "/v1/approvals/{id}", tag = "approvals", params(("id" = String, Path)), request_body = api::Decide, responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn decide(State(d): Shared, Path(id): Path<String>, Json(req): Json<api::Decide>) -> ApiResult<()> {
    let approve = match req.decision.as_str() {
        "approve" => true,
        "deny" => false,
        _ => return Err(bad("approval.invalid", "decision must be approve or deny")),
    };
    d.approvals.decide(&id, approve, "cli")?;
    Ok(Json(()))
}

#[utoipa::path(delete, path = "/v1/homes/{name}", tag = "homes", params(("name" = String, Path)), responses((status = 200, description = "Done"), (status = "4XX", body = api::ApiError)))]
async fn remove_home(State(d): Shared, Path(name): Path<String>) -> ApiResult<()> {
    d.builder.store.remove_home(&name).map(Json).map_err(Into::into)
}

#[cfg(test)]
mod openapi_tests {
    use super::*;

    #[test]
    fn the_openapi_document_names_every_route() {
        let doc = <ApiDoc as utoipa::OpenApi>::openapi();
        let source = include_str!("server.rs");
        let routes: std::collections::BTreeSet<&str> = source
            .lines()
            .filter_map(|l| l.trim().strip_prefix(".route(\"")?.split('"').next())
            .filter(|p| *p != "/v1/openapi.json")
            .collect();
        let documented: std::collections::BTreeSet<&str> =
            doc.paths.paths.keys().map(String::as_str).collect();
        assert_eq!(routes, documented);
        let json = serde_json::to_string(&doc).unwrap();
        assert!(json.contains("MachineInfo"), "schemas are included");
    }
}
