//! The web UI (plan §21): pages served by tobyd on 127.0.0.1 once `toby
//! web` asks for them. A page is a shell whose script fetches its content.
//! The one-time login token in the URL's fragment becomes a session secret
//! kept in the page's storage, which belongs to the UI's origin alone (a
//! cookie would reach every port of 127.0.0.1, forwarded guest servers
//! included); content, the API and WebSockets need that secret.

use std::collections::{HashMap, HashSet};
use std::io::{self, Read};
use std::sync::{Arc, LazyLock, Mutex};
use std::time::{Duration, Instant};

use axum::extract::{Path, Request, State};
use axum::http::{HeaderMap, HeaderValue, Method, StatusCode, header};
use axum::middleware::Next;
use axum::response::{Html, IntoResponse, Redirect, Response};
use axum::routing::{get, post};
use serde::Serialize;

use crate::server::Daemon;

/// How long a login URL works.
const TOKEN_TTL: Duration = Duration::from_secs(60);

#[derive(Default)]
pub struct Web {
    port: tokio::sync::OnceCell<u16>,
    /// Login tokens not used yet, with when they were made.
    tokens: Mutex<HashMap<String, Instant>>,
    /// Secrets of logged-in pages.
    sessions: Mutex<HashSet<String>>,
}

/// Marks a request that came through the web UI.
#[derive(Clone, Copy)]
pub struct FromWeb;

/// `n` random bytes as hex.
fn random_hex(n: usize) -> io::Result<String> {
    let mut bytes = vec![0u8; n];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    Ok(bytes.iter().map(|b| format!("{b:02x}")).collect())
}

impl Web {
    /// A one-time login URL; the pages are served from the first call on.
    pub async fn login_url(&self, d: Arc<Daemon>) -> io::Result<String> {
        let port = *self.port.get_or_try_init(|| start(d)).await?;
        let token = random_hex(32)?;
        let mut tokens = self.tokens.lock().unwrap();
        tokens.retain(|_, made| made.elapsed() < TOKEN_TTL);
        tokens.insert(token.clone(), Instant::now());
        Ok(format!("http://127.0.0.1:{port}/machines#login={token}"))
    }
}

async fn start(d: Arc<Daemon>) -> io::Result<u16> {
    let port = d.machines.current_config().daemon.web_port.unwrap_or(0);
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", port)).await?;
    let port = listener.local_addr()?.port();
    let app = axum::Router::new()
        .route("/", get(|| async { Redirect::to("/machines") }))
        .route("/login", post(login))
        .route("/static/app.js", get(|| async { ([(header::CONTENT_TYPE, "text/javascript")], APP_JS) }))
        .route("/static/style.css", get(|| async { ([(header::CONTENT_TYPE, "text/css")], STYLE) }))
        .route("/machines", get(machines))
        .route("/machines/{id}/logs", get(machine_logs))
        .route("/sessions", get(sessions))
        .route("/approvals", get(approvals))
        .route("/images", get(images))
        .route("/builds/{id}", get(build))
        .route("/storage", get(storage))
        .route("/mcp", get(mcp))
        .route("/mcp/{name}/logs", get(mcp_logs))
        .with_state(d.clone())
        .merge(crate::server::router(d.clone()))
        .layer(axum::middleware::from_fn_with_state(d, guard));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });
    Ok(port)
}

/// A local name, on any port (an SSH forward may use another).
fn local_host(host: Option<&str>) -> bool {
    let Some(host) = host else { return false };
    let name = match host.rsplit_once(':') {
        Some((name, port)) if port.bytes().all(|b| b.is_ascii_digit()) => name,
        _ => host,
    };
    matches!(name, "127.0.0.1" | "localhost" | "[::1]")
}

/// The session secret a request carries: `Authorization: Bearer`, or for a
/// WebSocket, the subprotocols `toby` and the secret.
fn secret(headers: &HeaderMap) -> Option<String> {
    let bearer = headers
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.strip_prefix("Bearer "))
        .map(str::to_string);
    bearer.or_else(|| {
        let protocols = headers.get(header::SEC_WEBSOCKET_PROTOCOL)?.to_str().ok()?;
        let mut list = protocols.split(',').map(str::trim);
        list.clone().any(|p| p == "toby").then(|| list.find(|p| *p != "toby"))?.map(str::to_string)
    })
}

/// A local `Host` always; a page's shell, its files and the login for
/// anyone; everything else for logged-in pages. Nothing may be framed.
async fn guard(State(d): State<Arc<Daemon>>, mut req: Request, next: Next) -> Response {
    let headers = req.headers();
    let mut response = if !local_host(headers.get(header::HOST).and_then(|h| h.to_str().ok())) {
        (StatusCode::FORBIDDEN, "wrong host").into_response()
    } else {
        let path = req.uri().path();
        let open = path.starts_with("/static/") || path == "/login" || path == "/";
        let shell = req.method() == Method::GET
            && !path.starts_with("/v1/")
            && headers.get("x-toby-part").is_none()
            && headers.get(header::UPGRADE).is_none();
        if shell && !open {
            page_shell(path)
        } else if open || secret(headers).is_some_and(|s| d.web.sessions.lock().unwrap().contains(&s)) {
            req.extensions_mut().insert(FromWeb);
            next.run(req).await
        } else {
            (StatusCode::UNAUTHORIZED, Html(LOGGED_OUT)).into_response()
        }
    };
    let h = response.headers_mut();
    h.insert(header::X_FRAME_OPTIONS, HeaderValue::from_static("DENY"));
    h.insert(
        header::CONTENT_SECURITY_POLICY,
        HeaderValue::from_static(
            "frame-ancestors 'none'; script-src 'self'; object-src 'none'; base-uri 'none'",
        ),
    );
    h.insert(header::X_CONTENT_TYPE_OPTIONS, HeaderValue::from_static("nosniff"));
    h.insert(header::REFERRER_POLICY, HeaderValue::from_static("no-referrer"));
    response
}

const LOGGED_OUT: &str = "<p>Open the web UI with <code>toby web</code>.</p>";

#[derive(serde::Deserialize)]
struct Login {
    token: String,
}

#[derive(Serialize)]
struct LoggedIn {
    session: String,
}

/// Exchanges a login token for a session secret.
async fn login(State(d): State<Arc<Daemon>>, axum::Json(q): axum::Json<Login>) -> Response {
    let valid = d.web.tokens.lock().unwrap().remove(&q.token).is_some_and(|made| made.elapsed() < TOKEN_TTL);
    if !valid {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    let Ok(session) = random_hex(32) else {
        return StatusCode::INTERNAL_SERVER_ERROR.into_response();
    };
    d.web.sessions.lock().unwrap().insert(session.clone());
    axum::Json(LoggedIn { session }).into_response()
}

const APP_JS: &str = include_str!("app.js");
const STYLE: &str = include_str!("style.css");

const PAGES: &[(&str, &str)] = &[
    ("machines", "Machines"),
    ("sessions", "Sessions"),
    ("approvals", "Approvals"),
    ("images", "Images"),
    ("storage", "Homes and roots"),
    ("mcp", "MCP"),
];

static TEMPLATES: LazyLock<minijinja::Environment<'static>> = LazyLock::new(|| {
    let mut env = minijinja::Environment::new();
    for (name, source) in [
        ("layout.html", include_str!("layout.html")),
        ("machines.html", include_str!("machines.html")),
        ("sessions.html", include_str!("sessions.html")),
        ("approvals.html", include_str!("approvals.html")),
        ("images.html", include_str!("images.html")),
        ("storage.html", include_str!("storage.html")),
        ("mcp.html", include_str!("mcp.html")),
        ("log.html", include_str!("log.html")),
        ("build.html", include_str!("build.html")),
    ] {
        env.add_template(name, source).expect("the web templates are valid");
    }
    env.add_filter("age", |secs: u64| age(secs));
    env.add_filter("duration", |secs: Option<u64>| secs.map(duration).unwrap_or_default());
    env
});

fn duration(secs: u64) -> String {
    match secs {
        0..60 => format!("{secs}s"),
        60..3600 => format!("{}m", secs / 60),
        3600..86400 => format!("{}h {}m", secs / 3600, secs % 3600 / 60),
        _ => format!("{}d {}h", secs / 86400, secs % 86400 / 3600),
    }
}

fn age(unix: u64) -> String {
    let now = toby_store::records::now();
    format!("{} ago", duration(now.saturating_sub(unix)))
}

/// The layout of the page at `path`, whose script fetches its content.
fn page_shell(path: &str) -> Response {
    let first = path.trim_start_matches('/').split('/').next().unwrap_or_default();
    let page = if first == "builds" { "images" } else { first };
    let title = PAGES.iter().find(|p| p.0 == page).map(|p| p.1).unwrap_or("Toby");
    match TEMPLATES
        .get_template("layout.html")
        .and_then(|t| t.render(minijinja::context! { page, title, pages => PAGES }))
    {
        Ok(html) => Html(html).into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    }
}

/// A page's content.
fn page(template: &str, ctx: impl Serialize) -> Response {
    match TEMPLATES.get_template(template).and_then(|t| t.render(&ctx)) {
        Ok(html) => Html(html).into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    }
}

type Shared = State<Arc<Daemon>>;
type PageResult = Result<Response, crate::machines::Error>;

async fn machines(State(d): Shared) -> PageResult {
    let axum::Json(machines) = crate::server::machines(State(d)).await?;
    Ok(page("machines.html", minijinja::context! { machines }))
}

async fn machine_logs(State(d): Shared, Path(id): Path<String>) -> PageResult {
    d.machines.record(&id)?;
    let ws = format!("/v1/machines/{id}/logs");
    Ok(page("log.html", minijinja::context! { heading => format!("Machine {id}"), ws }))
}

async fn sessions(State(d): Shared) -> PageResult {
    let axum::Json(list) = crate::server::sessions(State(d)).await?;
    #[derive(Serialize)]
    struct Row {
        machine: String,
        #[serde(flatten)]
        session: toby_proto::types::SessionInfo,
    }
    let sessions: Vec<Row> =
        list.into_iter().map(|s| Row { machine: s.machine, session: s.session }).collect();
    Ok(page("sessions.html", minijinja::context! { sessions }))
}

async fn approvals(State(d): Shared) -> PageResult {
    let axum::Json(approvals) = crate::server::approvals(State(d)).await?;
    Ok(page("approvals.html", minijinja::context! { approvals }))
}

async fn images(State(d): Shared) -> PageResult {
    let axum::Json(images) = crate::server::images(State(d.clone())).await?;
    let axum::Json(builds) = crate::server::builds(State(d)).await?;
    Ok(page("images.html", minijinja::context! { images, builds }))
}

async fn build(State(d): Shared, Path(id): Path<String>) -> PageResult {
    let b = d.builds.get(&id).ok_or_else(|| {
        crate::machines::Error::new(
            crate::machines::ErrorKind::NotFound,
            "build.not-found",
            format!("no build {id}"),
        )
    })?;
    let state = b.status().state;
    Ok(page("build.html", minijinja::context! { id, state }))
}

async fn storage(State(d): Shared) -> PageResult {
    let axum::Json(homes) = crate::server::homes(State(d.clone())).await?;
    let axum::Json(roots) = crate::server::roots(State(d)).await?;
    let uid = nix::unistd::getuid().as_raw();
    let user =
        nix::unistd::User::from_uid(nix::unistd::getuid()).ok().flatten().map(|u| u.name).unwrap_or_default();
    Ok(page("storage.html", minijinja::context! { homes, roots, user, uid }))
}

async fn mcp(State(d): Shared) -> PageResult {
    let axum::Json(servers) = crate::server::mcp_servers(State(d)).await?;
    Ok(page("mcp.html", minijinja::context! { servers }))
}

async fn mcp_logs(Path(name): Path<String>) -> PageResult {
    let ws = format!("/v1/mcp/{name}/logs");
    Ok(page("log.html", minijinja::context! { heading => format!("MCP server {name}"), ws }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn templates_render() {
        for name in
            ["machines.html", "sessions.html", "approvals.html", "images.html", "storage.html", "mcp.html"]
        {
            TEMPLATES.get_template(name).unwrap().render(minijinja::context! {}).unwrap();
        }
        let machine = serde_json::json!({
            "id": "m1", "home": "dev", "root": "arch", "state": "ready", "sessions": 1,
            "attachments": [{"id": "a", "host": "/src/<x>", "at": "/w", "read_only": false, "state": "ready"}],
            "forwards": [{"id": "f", "direction": "host-to-guest", "host": "127.0.0.1:1", "guest": "127.0.0.1:1", "state": "listening"}],
            "uptime_secs": 70
        });
        let html = TEMPLATES
            .get_template("machines.html")
            .unwrap()
            .render(minijinja::context! { machines => vec![machine] })
            .unwrap();
        assert!(html.contains("&lt;x&gt;") && !html.contains("<x>"), "guest text is escaped: {html}");
        assert!(html.contains("1m"));
    }

    #[test]
    fn hosts_and_secrets() {
        assert!(
            local_host(Some("127.0.0.1:8080"))
                && local_host(Some("localhost:9"))
                && local_host(Some("[::1]:1"))
        );
        assert!(
            !local_host(Some("evil.example:8080"))
                && !local_host(Some("127.0.0.1.evil.example"))
                && !local_host(None)
        );
        let mut h = HeaderMap::new();
        h.insert(header::AUTHORIZATION, "Bearer abc".parse().unwrap());
        assert_eq!(secret(&h).as_deref(), Some("abc"));
        let mut h = HeaderMap::new();
        h.insert(header::SEC_WEBSOCKET_PROTOCOL, "toby, abc".parse().unwrap());
        assert_eq!(secret(&h).as_deref(), Some("abc"));
        h.insert(header::SEC_WEBSOCKET_PROTOCOL, "abc".parse().unwrap());
        assert_eq!(secret(&h), None);
    }
}
