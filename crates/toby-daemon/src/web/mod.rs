//! The web UI (plan §21): pages served by tobyd on 127.0.0.1 once `toby
//! web` asks for them. A one-time login URL becomes a `SameSite=Strict`
//! cookie; every request needs the cookie and a local `Host`, and requests
//! that change something (and WebSockets) an `Origin` of the UI itself.

use std::collections::{HashMap, HashSet};
use std::io::{self, Read};
use std::sync::{Arc, LazyLock, Mutex};
use std::time::{Duration, Instant};

use axum::extract::{Path, Request, State};
use axum::http::{HeaderMap, Method, StatusCode, header};
use axum::middleware::Next;
use axum::response::{Html, IntoResponse, Redirect, Response};
use axum::routing::get;
use serde::Serialize;

use crate::server::Daemon;

/// How long a login URL works.
const TOKEN_TTL: Duration = Duration::from_secs(60);
const COOKIE: &str = "toby";

#[derive(Default)]
pub struct Web {
    port: tokio::sync::OnceCell<u16>,
    /// Login tokens not used yet, with when they were made.
    tokens: Mutex<HashMap<String, Instant>>,
    /// Cookies of logged-in browsers.
    sessions: Mutex<HashSet<String>>,
}

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
        Ok(format!("http://127.0.0.1:{port}/login?token={token}"))
    }
}

async fn start(d: Arc<Daemon>) -> io::Result<u16> {
    let port = d.machines.current_config().daemon.web_port.unwrap_or(0);
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", port)).await?;
    let port = listener.local_addr()?.port();
    let app = axum::Router::new()
        .route("/", get(|| async { Redirect::to("/machines") }))
        .route("/login", get(login))
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
        .layer(axum::middleware::from_fn_with_state((d, port), guard));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });
    Ok(port)
}

fn origin_ok(value: Option<&str>, port: u16) -> bool {
    value.is_some_and(|v| v == format!("http://127.0.0.1:{port}") || v == format!("http://localhost:{port}"))
}

fn cookie(headers: &HeaderMap) -> Option<String> {
    headers
        .get_all(header::COOKIE)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|v| v.split(';'))
        .find_map(|c| c.trim().strip_prefix(&format!("{COOKIE}=")).map(str::to_string))
}

/// Local `Host` always; the login cookie except for `/login`; the UI's own
/// `Origin` for anything but reading.
async fn guard(State((d, port)): State<(Arc<Daemon>, u16)>, req: Request, next: Next) -> Response {
    let headers = req.headers();
    let host = headers.get(header::HOST).and_then(|h| h.to_str().ok());
    let local = host.is_some_and(|h| h == format!("127.0.0.1:{port}") || h == format!("localhost:{port}"));
    if !local {
        return (StatusCode::FORBIDDEN, "wrong host").into_response();
    }
    let websocket = headers.get(header::UPGRADE).is_some();
    let reading = matches!(*req.method(), Method::GET | Method::HEAD) && !websocket;
    if !reading && !origin_ok(headers.get(header::ORIGIN).and_then(|o| o.to_str().ok()), port) {
        return (StatusCode::FORBIDDEN, "wrong origin").into_response();
    }
    if req.uri().path() != "/login" {
        let known = cookie(headers).is_some_and(|c| d.web.sessions.lock().unwrap().contains(&c));
        if !known {
            return (StatusCode::UNAUTHORIZED, Html(LOGGED_OUT)).into_response();
        }
    }
    next.run(req).await
}

const LOGGED_OUT: &str = "<!doctype html><meta charset=utf-8><title>Toby</title>\
<p style=\"font-family:system-ui;margin:40px\">Open the web UI with <code>toby web</code>.</p>";

#[derive(serde::Deserialize)]
struct Login {
    token: String,
}

async fn login(
    State(d): State<Arc<Daemon>>,
    axum::extract::Query(q): axum::extract::Query<Login>,
) -> Response {
    let valid = d.web.tokens.lock().unwrap().remove(&q.token).is_some_and(|made| made.elapsed() < TOKEN_TTL);
    if !valid {
        return (StatusCode::UNAUTHORIZED, Html(LOGGED_OUT)).into_response();
    }
    let Ok(session) = random_hex(32) else {
        return StatusCode::INTERNAL_SERVER_ERROR.into_response();
    };
    d.web.sessions.lock().unwrap().insert(session.clone());
    let cookie = format!("{COOKIE}={session}; HttpOnly; SameSite=Strict; Path=/");
    ([(header::SET_COOKIE, cookie)], Redirect::to("/machines")).into_response()
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

/// A page, or only its main part when the script refreshes it.
fn page(headers: &HeaderMap, page: &str, template: &str, ctx: impl Serialize) -> Response {
    let render = || -> Result<String, minijinja::Error> {
        let main = TEMPLATES.get_template(template)?.render(&ctx)?;
        if headers.get("x-toby-part").is_some() {
            return Ok(main);
        }
        let title = PAGES.iter().find(|p| p.0 == page).map(|p| p.1).unwrap_or("Toby");
        TEMPLATES
            .get_template("layout.html")?
            .render(minijinja::context! { page, title, pages => PAGES, main })
    };
    match render() {
        Ok(html) => Html(html).into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    }
}

type Shared = State<Arc<Daemon>>;
type PageResult = Result<Response, crate::machines::Error>;

async fn machines(State(d): Shared, headers: HeaderMap) -> PageResult {
    let axum::Json(machines) = crate::server::machines(State(d)).await?;
    Ok(page(&headers, "machines", "machines.html", minijinja::context! { machines }))
}

async fn machine_logs(State(d): Shared, headers: HeaderMap, Path(id): Path<String>) -> PageResult {
    d.machines.record(&id)?;
    let ws = format!("/v1/machines/{id}/logs");
    Ok(page(&headers, "machines", "log.html", minijinja::context! { heading => format!("Machine {id}"), ws }))
}

async fn sessions(State(d): Shared, headers: HeaderMap) -> PageResult {
    let axum::Json(list) = crate::server::sessions(State(d)).await?;
    #[derive(Serialize)]
    struct Row {
        machine: String,
        #[serde(flatten)]
        session: toby_proto::types::SessionInfo,
    }
    let sessions: Vec<Row> =
        list.into_iter().map(|s| Row { machine: s.machine, session: s.session }).collect();
    Ok(page(&headers, "sessions", "sessions.html", minijinja::context! { sessions }))
}

async fn approvals(State(d): Shared, headers: HeaderMap) -> PageResult {
    let axum::Json(approvals) = crate::server::approvals(State(d)).await?;
    Ok(page(&headers, "approvals", "approvals.html", minijinja::context! { approvals }))
}

async fn images(State(d): Shared, headers: HeaderMap) -> PageResult {
    let axum::Json(images) = crate::server::images(State(d.clone())).await?;
    let axum::Json(builds) = crate::server::builds(State(d)).await?;
    Ok(page(&headers, "images", "images.html", minijinja::context! { images, builds }))
}

async fn build(State(d): Shared, headers: HeaderMap, Path(id): Path<String>) -> PageResult {
    let b = d.builds.get(&id).ok_or_else(|| {
        crate::machines::Error::new(
            crate::machines::ErrorKind::NotFound,
            "build.not-found",
            format!("no build {id}"),
        )
    })?;
    let state = b.status().state;
    Ok(page(&headers, "images", "build.html", minijinja::context! { id, state }))
}

async fn storage(State(d): Shared, headers: HeaderMap) -> PageResult {
    let axum::Json(homes) = crate::server::homes(State(d.clone())).await?;
    let axum::Json(roots) = crate::server::roots(State(d)).await?;
    let uid = nix::unistd::getuid().as_raw();
    let user =
        nix::unistd::User::from_uid(nix::unistd::getuid()).ok().flatten().map(|u| u.name).unwrap_or_default();
    Ok(page(&headers, "storage", "storage.html", minijinja::context! { homes, roots, user, uid }))
}

async fn mcp(State(d): Shared, headers: HeaderMap) -> PageResult {
    let axum::Json(servers) = crate::server::mcp_servers(State(d)).await?;
    Ok(page(&headers, "mcp", "mcp.html", minijinja::context! { servers }))
}

async fn mcp_logs(headers: HeaderMap, Path(name): Path<String>) -> PageResult {
    let ws = format!("/v1/mcp/{name}/logs");
    Ok(page(&headers, "mcp", "log.html", minijinja::context! { heading => format!("MCP server {name}"), ws }))
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
    fn cookies_and_origins() {
        let mut h = HeaderMap::new();
        h.insert(header::COOKIE, "a=b; toby=abc; c=d".parse().unwrap());
        assert_eq!(cookie(&h).as_deref(), Some("abc"));
        assert!(origin_ok(Some("http://127.0.0.1:8080"), 8080));
        assert!(!origin_ok(Some("http://evil.example"), 8080));
        assert!(!origin_ok(None, 8080));
    }
}
