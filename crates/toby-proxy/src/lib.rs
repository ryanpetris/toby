//! The models proxy (plan §16.2): guests call
//! `http://127.0.0.1:41100/<provider>/…` with their machine's synthetic token;
//! the proxy checks it, replaces the credentials with the provider's
//! configured headers and forwards the request, streaming both ways.

use std::io;
use std::path::PathBuf;
use std::sync::Arc;

use bytes::Bytes;
use http_body_util::combinators::BoxBody;
use http_body_util::{BodyExt, Full};
use hyper::body::Incoming;
use hyper::header::{HeaderName, HeaderValue};
use hyper::{Request, Response, StatusCode};
use hyper_util::client::legacy::Client;
use hyper_util::client::legacy::connect::HttpConnector;
use hyper_util::rt::{TokioExecutor, TokioIo};
use toby_config::global::GlobalConfig;
use toby_config::paths::Paths;
use toby_proto::frame;
use toby_proto::service::ServiceHeader;
use tokio::net::{UnixListener, UnixStream};

type Body = BoxBody<Bytes, hyper::Error>;

/// How long a connection may take to send a request's headers, including
/// between requests on a kept-alive connection.
const HEADER_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(60);
type HttpsClient = Client<hyper_rustls::HttpsConnector<HttpConnector>, Incoming>;

/// Headers that belong to one connection and are never forwarded.
const HOP_BY_HOP: &[&str] = &[
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "host",
];

/// Where the proxy finds its configuration and machine tokens.
pub struct Proxy {
    pub config_path: PathBuf,
    pub paths: Paths,
    pub home: PathBuf,
    client: HttpsClient,
    /// Where HTTP MCP servers Toby runs were, and since when.
    endpoints: std::sync::Mutex<std::collections::HashMap<String, (std::time::Instant, String, u16)>>,
}

/// How long an HTTP MCP server's place is used before tobyd is asked
/// again, which also tells tobyd it is still in use.
const ENDPOINT_FOR: std::time::Duration = std::time::Duration::from_secs(60);

fn text(status: StatusCode, msg: impl Into<String>) -> Response<Body> {
    let body = Full::new(Bytes::from(msg.into() + "\n")).map_err(|never| match never {}).boxed();
    let mut r = Response::new(body);
    *r.status_mut() = status;
    r
}

/// The token a request carries: `x-api-key`, or a bearer `authorization`.
fn presented_token(req: &Request<Incoming>) -> Option<String> {
    let h = req.headers();
    if let Some(v) = h.get("x-api-key").and_then(|v| v.to_str().ok()) {
        return Some(v.to_string());
    }
    h.get("authorization")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.strip_prefix("Bearer "))
        .map(str::to_string)
}

/// Splits `/<provider>/<rest>` into the provider and the upstream path.
fn split_path(path_and_query: &str) -> Option<(&str, &str)> {
    let rest = path_and_query.strip_prefix('/')?;
    let (provider, tail) = match rest.find(['/', '?']) {
        Some(i) => (&rest[..i], &rest[i..]),
        None => (rest, ""),
    };
    (!provider.is_empty()).then_some((provider, tail))
}

/// Whether a path (before its query) has no `.` or `..` segments and no
/// encoded dots or separators, so an upstream cannot resolve it outside the
/// configured URL (a provider's or an HTTP MCP server's).
fn plain_path(tail: &str) -> bool {
    let path = tail.split('?').next().unwrap_or_default();
    let lower = path.to_ascii_lowercase();
    !path.split('/').any(|seg| seg == "." || seg == "..")
        && !path.contains('\\')
        && !["%2e", "%2f", "%5c"].iter().any(|e| lower.contains(e))
}

/// Compares secrets without stopping at the first difference.
fn same(a: &str, b: &str) -> bool {
    a.len() == b.len() && a.bytes().zip(b.bytes()).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

impl Proxy {
    pub fn new(config_path: PathBuf, paths: Paths, home: PathBuf) -> io::Result<Proxy> {
        let https = hyper_rustls::HttpsConnectorBuilder::new()
            .with_webpki_roots()
            .https_or_http()
            .enable_http1()
            .build();
        let client = Client::builder(TokioExecutor::new()).build(https);
        Ok(Proxy { config_path, paths, home, client, endpoints: Default::default() })
    }

    async fn handle(&self, machine: &str, req: Request<Incoming>) -> Response<Body> {
        let pq = req.uri().path_and_query().map(|p| p.as_str().to_string()).unwrap_or_default();
        match pq.strip_prefix("/mcp/") {
            Some(rest) => self.mcp(machine, rest, req).await,
            None => self.models(machine, req).await,
        }
    }

    /// An HTTP MCP server (plan §16.3): tools call `/mcp/<name>/…` and the
    /// proxy adds the server's configured headers. The connection comes
    /// from a machine's own capability; the server has to be one its tools
    /// use.
    async fn mcp(&self, machine: &str, rest: &str, mut req: Request<Incoming>) -> Response<Body> {
        let config = match GlobalConfig::load(&self.config_path) {
            Ok(c) => c,
            Err(e) => {
                // The error quotes the file, which may hold secrets: host only.
                eprintln!("configuration: {e}");
                return text(
                    StatusCode::BAD_GATEWAY,
                    "the Toby configuration cannot be read; see the host's logs",
                );
            }
        };
        let Some((name, tail)) = split_path(&format!("/{rest}")).map(|(n, t)| (n.to_string(), t.to_string()))
        else {
            return text(StatusCode::NOT_FOUND, "use /mcp/<server>");
        };
        let server = match config.mcp.get(&name) {
            Some(s) if s.kind == toby_config::global::McpKind::Http => s,
            _ => return text(StatusCode::NOT_FOUND, format!("no HTTP MCP server {name:?} is configured")),
        };
        if let Err(e) = server.check(&name) {
            return text(StatusCode::NOT_FOUND, e);
        }
        let reachable = toby_config::machine::MachineSpec::load(&self.paths.machine_desired(machine))
            .is_ok_and(|spec| config.mcp_reachable(&spec, &name));
        if !reachable {
            return text(
                StatusCode::FORBIDDEN,
                format!("no tool of this machine uses the MCP server {name}"),
            );
        }
        if !plain_path(&tail) {
            return text(
                StatusCode::BAD_REQUEST,
                "the path may not contain dot segments or encoded separators",
            );
        }
        let config_dir = self.config_path.parent().unwrap_or(&self.home).to_path_buf();
        let resolve = |v: &str| toby_config::subst::resolve(v, &config_dir, &self.home);
        let url = match server.url.as_deref().map(resolve) {
            Some(Ok(u)) => u,
            Some(Err(e)) => return text(StatusCode::BAD_GATEWAY, format!("mcp.{name}.url: {e}")),
            // A server Toby runs in a machine of its own, reached through
            // that machine's relay.
            None => {
                let mut headers = Vec::new();
                for (k, v) in &server.headers {
                    match resolve(v) {
                        Ok(v) => headers.push((k.clone(), v)),
                        Err(e) => {
                            return text(StatusCode::BAD_GATEWAY, format!("mcp.{name}.headers.{k}: {e}"));
                        }
                    }
                }
                for h in HOP_BY_HOP.iter().chain(&["authorization", "x-api-key"]) {
                    req.headers_mut().remove(*h);
                }
                return self.forward_local(&name, req, &tail, headers).await;
            }
        };
        let upstream = format!("{}{tail}", url.trim_end_matches('/'));
        let mut headers = Vec::new();
        for (k, v) in &server.headers {
            match resolve(v) {
                Ok(v) => headers.push((k.clone(), v)),
                Err(e) => return text(StatusCode::BAD_GATEWAY, format!("mcp.{name}.headers.{k}: {e}")),
            }
        }
        for h in HOP_BY_HOP.iter().chain(&["authorization", "x-api-key"]) {
            req.headers_mut().remove(*h);
        }
        self.forward(&name, req, &upstream, headers).await
    }

    /// Sends `req` to an HTTP MCP server Toby runs, at `path`.
    async fn forward_local(
        &self,
        name: &str,
        mut req: Request<Incoming>,
        path: &str,
        headers: Vec<(String, String)>,
    ) -> Response<Body> {
        let cached = self
            .endpoints
            .lock()
            .unwrap()
            .get(name)
            .filter(|(at, _, _)| at.elapsed() < ENDPOINT_FOR)
            .map(|(_, m, p)| (m.clone(), *p));
        let dialled = match cached {
            Some((machine, port)) => {
                toby_machine::link::dial_local(&self.paths.machine_runtime(&machine).vsock(), port)
                    .await
                    .ok()
                    .map(|s| (s, port))
            }
            None => None,
        };
        let (stream, port) = match dialled {
            Some(d) => d,
            None => {
                let (machine, port) = match self.endpoint(name).await {
                    Ok(e) => e,
                    Err(e) => {
                        self.endpoints.lock().unwrap().remove(name);
                        // tobyd's message may name host paths: host only.
                        eprintln!("mcp {name}: {e}");
                        return text(
                            StatusCode::BAD_GATEWAY,
                            format!("{name} could not be started; see toby mcp logs {name}"),
                        );
                    }
                };
                let vsock = self.paths.machine_runtime(&machine).vsock();
                match toby_machine::link::dial_local(&vsock, port).await {
                    Ok(s) => {
                        let entry = (std::time::Instant::now(), machine, port);
                        self.endpoints.lock().unwrap().insert(name.to_string(), entry);
                        (s, port)
                    }
                    Err(e) => return text(StatusCode::BAD_GATEWAY, format!("{name}: {e}")),
                }
            }
        };
        let (mut sender, conn) = match hyper::client::conn::http1::handshake(TokioIo::new(stream)).await {
            Ok(c) => c,
            Err(e) => return text(StatusCode::BAD_GATEWAY, format!("{name}: {e}")),
        };
        tokio::spawn(conn);
        for (k, v) in headers {
            match (HeaderName::try_from(k.as_str()), HeaderValue::try_from(v)) {
                (Ok(k), Ok(v)) => {
                    req.headers_mut().insert(k, v);
                }
                _ => return text(StatusCode::BAD_GATEWAY, format!("{name}: header {k} is invalid")),
            }
        }
        let path = if path.is_empty() { "/" } else { path };
        match path.parse::<hyper::Uri>() {
            Ok(u) => *req.uri_mut() = u,
            Err(e) => return text(StatusCode::BAD_REQUEST, format!("{name}: {e}")),
        }
        if let Ok(host) = HeaderValue::try_from(format!("127.0.0.1:{port}")) {
            req.headers_mut().insert(hyper::header::HOST, host);
        }
        *req.version_mut() = hyper::Version::HTTP_11;
        match sender.send_request(req).await {
            Ok(mut resp) => {
                for h in HOP_BY_HOP {
                    resp.headers_mut().remove(*h);
                }
                resp.map(|b| b.boxed())
            }
            Err(e) => text(StatusCode::BAD_GATEWAY, format!("{name}: {e}")),
        }
    }

    /// Asks tobyd where an HTTP MCP server it runs listens, starting it.
    async fn endpoint(&self, name: &str) -> Result<(String, u16), String> {
        let stream = UnixStream::connect(self.paths.api_sock()).await.map_err(|e| format!("tobyd: {e}"))?;
        let (mut sender, conn) =
            hyper::client::conn::http1::handshake(TokioIo::new(stream)).await.map_err(|e| e.to_string())?;
        tokio::spawn(conn);
        let req = Request::post(format!("/v1/mcp/{name}/endpoint"))
            .header(hyper::header::HOST, "localhost")
            .body(http_body_util::Empty::<Bytes>::new())
            .map_err(|e| e.to_string())?;
        let res = sender.send_request(req).await.map_err(|e| e.to_string())?;
        let ok = res.status().is_success();
        let body = res.into_body().collect().await.map_err(|e| e.to_string())?.to_bytes();
        let v: serde_json::Value = serde_json::from_slice(&body).map_err(|e| e.to_string())?;
        if !ok {
            return Err(v
                .get("message")
                .and_then(|m| m.as_str())
                .unwrap_or("tobyd gave no answer")
                .to_string());
        }
        let machine = v.get("machine").and_then(|m| m.as_str()).unwrap_or_default().to_string();
        let port = v.get("port").and_then(|p| p.as_u64()).and_then(|p| u16::try_from(p).ok());
        match port {
            Some(port)
                if !machine.is_empty() && machine.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-') =>
            {
                Ok((machine, port))
            }
            _ => Err("tobyd gave no endpoint".into()),
        }
    }

    async fn models(&self, machine: &str, mut req: Request<Incoming>) -> Response<Body> {
        let token_file = self.paths.machine_state_dir(machine).join("models-token");
        let expected = match std::fs::read_to_string(&token_file) {
            Ok(t) if !t.trim().is_empty() => t.trim().to_string(),
            _ => return text(StatusCode::UNAUTHORIZED, "this machine has no models token"),
        };
        if !presented_token(&req).is_some_and(|t| same(&t, &expected)) {
            return text(StatusCode::UNAUTHORIZED, "invalid models token");
        }
        // Read on every request, so configuration changes apply at once.
        let config = match GlobalConfig::load(&self.config_path) {
            Ok(c) => c,
            Err(e) => {
                // The error quotes the file, which may hold secrets: host only.
                eprintln!("configuration: {e}");
                return text(
                    StatusCode::BAD_GATEWAY,
                    "the Toby configuration cannot be read; see the host's logs",
                );
            }
        };
        let pq = req.uri().path_and_query().map(|p| p.as_str().to_string()).unwrap_or_default();
        let Some((name, tail)) = split_path(&pq) else {
            return text(StatusCode::NOT_FOUND, "use /<provider>/… with a configured provider");
        };
        let Some(provider) = config.models.get(name) else {
            return text(StatusCode::NOT_FOUND, format!("no model provider {name:?} is configured"));
        };
        if !plain_path(tail) {
            return text(
                StatusCode::BAD_REQUEST,
                "the path may not contain dot segments or encoded separators",
            );
        }
        let upstream = format!("{}{tail}", provider.url.trim_end_matches('/'));

        let config_dir = self.config_path.parent().unwrap_or(&self.home).to_path_buf();
        for h in HOP_BY_HOP.iter().chain(&["authorization", "x-api-key"]) {
            req.headers_mut().remove(*h);
        }
        let mut headers = Vec::new();
        for (k, v) in &provider.headers {
            match toby_config::subst::resolve(v, &config_dir, &self.home) {
                Ok(v) => headers.push((k.clone(), v)),
                Err(e) => return text(StatusCode::BAD_GATEWAY, format!("provider {name} header {k}: {e}")),
            }
        }
        self.forward(name, req, &upstream, headers).await
    }

    /// Sends `req` to `upstream` with `headers` set, streaming both ways.
    async fn forward(
        &self,
        name: &str,
        mut req: Request<Incoming>,
        upstream: &str,
        headers: Vec<(String, String)>,
    ) -> Response<Body> {
        let uri = match upstream.parse::<hyper::Uri>() {
            Ok(u) => u,
            Err(e) => return text(StatusCode::BAD_GATEWAY, format!("{name}: {e}")),
        };
        for (k, v) in headers {
            match (HeaderName::try_from(k.as_str()), HeaderValue::try_from(v)) {
                (Ok(k), Ok(v)) => {
                    req.headers_mut().insert(k, v);
                }
                _ => return text(StatusCode::BAD_GATEWAY, format!("{name}: header {k} is invalid")),
            }
        }
        if let Some(host) = uri.authority().and_then(|a| HeaderValue::try_from(a.as_str()).ok()) {
            req.headers_mut().insert(hyper::header::HOST, host);
        }
        *req.uri_mut() = uri;
        *req.version_mut() = hyper::Version::HTTP_11;

        match self.client.request(req).await {
            Ok(mut resp) => {
                for h in HOP_BY_HOP {
                    resp.headers_mut().remove(*h);
                }
                resp.map(|b| b.boxed())
            }
            Err(e) => text(StatusCode::BAD_GATEWAY, format!("provider {name}: {e}")),
        }
    }

    /// One connection from `toby-machine`: the machine header, then HTTP.
    async fn connection(self: Arc<Self>, mut s: UnixStream) -> io::Result<()> {
        // Only processes of this user may use the proxy.
        let peer = s.peer_cred()?;
        if peer.uid() != nix::unistd::getuid().as_raw() {
            return Ok(());
        }
        let ServiceHeader::FromMachine(from) = frame::recv(&mut s).await?;
        let machine = from.machine_id;
        if machine.is_empty() || !machine.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-') {
            return Ok(());
        }
        let service = hyper::service::service_fn(move |req| {
            let this = self.clone();
            let machine = machine.clone();
            async move { Ok::<_, std::convert::Infallible>(this.handle(&machine, req).await) }
        });
        let _ = hyper::server::conn::http1::Builder::new()
            .timer(hyper_util::rt::TokioTimer::new())
            .header_read_timeout(HEADER_TIMEOUT)
            .serve_connection(TokioIo::new(s), service)
            .await;
        Ok(())
    }

    /// Serves connections on `listener` until the process ends.
    pub async fn serve(self: Arc<Self>, listener: UnixListener) -> io::Result<()> {
        loop {
            match listener.accept().await {
                Ok((s, _)) => {
                    tokio::spawn(self.clone().connection(s));
                }
                // Out of descriptors, for example: keep serving.
                Err(_) => tokio::time::sleep(std::time::Duration::from_millis(100)).await,
            }
        }
    }
}

/// Creates the token a machine's tools use with the proxy, if it has none.
pub fn ensure_token(paths: &Paths, machine: &str) -> io::Result<String> {
    use std::io::Read;
    use std::os::unix::fs::OpenOptionsExt;
    let path = paths.machine_state_dir(machine).join("models-token");
    if let Ok(t) = std::fs::read_to_string(&path)
        && !t.trim().is_empty()
    {
        return Ok(t.trim().to_string());
    }
    let mut random = [0u8; 24];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut random)?;
    let hex: String = random.iter().map(|b| format!("{b:02x}")).collect();
    let token = format!("toby_{machine}_{hex}");
    std::fs::create_dir_all(paths.machine_state_dir(machine))?;
    // Written whole or not at all.
    let tmp = path.with_extension(format!("tmp.{}", hex.get(..8).unwrap_or_default()));
    let _ = std::fs::remove_file(&tmp);
    {
        let mut f = std::fs::OpenOptions::new().write(true).create_new(true).mode(0o600).open(&tmp)?;
        std::io::Write::write_all(&mut f, token.as_bytes())?;
        f.sync_all()?;
    }
    std::fs::rename(&tmp, &path)?;
    Ok(token)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_name_the_provider() {
        assert_eq!(split_path("/anthropic/v1/messages"), Some(("anthropic", "/v1/messages")));
        assert_eq!(split_path("/openai?x=1"), Some(("openai", "?x=1")));
        assert_eq!(split_path("/openai"), Some(("openai", "")));
        assert_eq!(split_path("/"), None);
        assert_eq!(split_path("anthropic/v1"), None);
    }

    #[test]
    fn mcp_paths_stay_under_the_server_url() {
        assert!(plain_path("/sse?x=../y"));
        assert!(plain_path(""));
        for bad in ["/../x", "/a/./b", "/%2E%2E/x", "/a%2fb", "/a\\b"] {
            assert!(!plain_path(bad), "{bad}");
        }
    }

    #[test]
    fn secrets_compare_exactly() {
        assert!(same("toby_a_1", "toby_a_1"));
        assert!(!same("toby_a_1", "toby_a_2"));
        assert!(!same("toby_a_1", "toby_a_12"));
    }
}
