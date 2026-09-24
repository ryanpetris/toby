//! The CLI's connection to tobyd: HTTP on its Unix socket, starting the
//! daemon when it does not answer (plan §12, §18).

use std::path::PathBuf;
use std::time::Duration;

use anyhow::{Context, bail};
use http_body_util::{BodyExt, Full};
use hyper::body::Bytes;
use hyper::{Method, Request, StatusCode};
use hyper_util::rt::TokioIo;
use serde::Serialize;
use serde::de::DeserializeOwned;
use toby_config::global::{Backend, GlobalConfig};
use toby_config::paths::Paths;
use tokio::net::UnixStream;

use crate::internal::load_config;

const START_TIMEOUT: Duration = Duration::from_secs(10);

/// An error tobyd reported.
#[derive(Debug)]
pub struct Failure(pub toby_api::ApiError);

impl std::fmt::Display for Failure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0.message)
    }
}

impl std::error::Error for Failure {}

pub struct Api {
    sock: PathBuf,
    pub config: GlobalConfig,
    pub paths: Paths,
}

impl Api {
    /// Connects to tobyd, starting it if needed, and checks that it uses the
    /// same directories as this CLI.
    pub async fn connect() -> anyhow::Result<Api> {
        let (config, paths) = load_config()?;
        let api = Api { sock: paths.runtime.join(toby_api::SOCKET), config, paths };
        if UnixStream::connect(&api.sock).await.is_err() {
            api.start_daemon().await?;
        }
        let info: toby_api::DaemonInfo = api.get("/v1/daemon").await?;
        let mine = [&api.paths.state, &api.paths.data, &api.paths.runtime].map(|p| p.display().to_string());
        let theirs = [&info.state_dir, &info.data_dir, &info.runtime_dir];
        if mine.iter().zip(theirs).any(|(a, b)| a != b) {
            bail!(
                "tobyd uses other directories than this command (state {}, data {}, runtime {}); \
                 restart it with: toby daemon restart",
                info.state_dir,
                info.data_dir,
                info.runtime_dir
            );
        }
        Ok(api)
    }

    async fn start_daemon(&self) -> anyhow::Result<()> {
        match self.config.daemon.backend {
            Backend::SystemdUser => {
                let systemd = toby_svc::systemd::SystemdUser::connect().await.map_err(|e| {
                    anyhow::anyhow!("{e}; to run Toby without it, set daemon.backend = \"direct\"")
                })?;
                // Units installed after the user instance started are read
                // on a reload.
                if systemd.state("tobyd.socket").await? == "not-found" {
                    systemd.reload().await.context("reloading the systemd user instance")?;
                    if systemd.state("tobyd.socket").await? == "not-found" {
                        bail!("tobyd.socket is not installed for your systemd user instance");
                    }
                }
                systemd.start("tobyd.socket").await.context("starting tobyd.socket")?;
            }
            Backend::Direct => {
                let exe = toby_daemon::supervisor::current_exe(&self.config.programs.versions())?;
                let mut cmd = std::process::Command::new(exe);
                cmd.args(["internal", "daemon"]);
                toby_svc::direct::spawn_detached(cmd, &self.paths.state.join("logs/tobyd.log"))?;
            }
        }
        let deadline = tokio::time::Instant::now() + START_TIMEOUT;
        while UnixStream::connect(&self.sock).await.is_err() {
            if tokio::time::Instant::now() > deadline {
                bail!("tobyd did not start; see: toby daemon logs");
            }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        Ok(())
    }

    async fn send(
        &self,
        method: Method,
        path: &str,
        body: Option<Vec<u8>>,
    ) -> anyhow::Result<hyper::Response<hyper::body::Incoming>> {
        // A daemon that is restarting comes back (plan §3.2).
        let stream = match UnixStream::connect(&self.sock).await {
            Ok(s) => s,
            Err(_) => {
                self.start_daemon().await?;
                UnixStream::connect(&self.sock).await.context("connecting to tobyd")?
            }
        };
        let (mut sender, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream)).await?;
        tokio::spawn(conn);
        let mut req = Request::builder().method(method).uri(path).header("host", "tobyd");
        if body.is_some() {
            req = req.header("content-type", "application/json");
        }
        let req = req.body(Full::new(Bytes::from(body.unwrap_or_default())))?;
        Ok(sender.send_request(req).await?)
    }

    /// Changes as tobyd reports them (`GET /v1/events`).
    pub async fn events(&self) -> anyhow::Result<Events> {
        let stream = UnixStream::connect(&self.sock).await.context("connecting to tobyd")?;
        let (ws, _) = tokio_tungstenite::client_async("ws://tobyd/v1/events", stream).await?;
        Ok(Events(ws))
    }

    async fn call<T: DeserializeOwned>(
        &self,
        method: Method,
        path: &str,
        body: Option<Vec<u8>>,
    ) -> anyhow::Result<T> {
        // A daemon that went away mid-request: reading again is safe.
        let attempt = |method: Method, body: Option<Vec<u8>>| async move {
            let resp = self.send(method, path, body).await?;
            let status = resp.status();
            let bytes = resp.into_body().collect().await?.to_bytes();
            anyhow::Ok((status, bytes))
        };
        let (status, bytes) = match attempt(method.clone(), body.clone()).await {
            Err(_) if method == Method::GET => attempt(method, body).await?,
            r => r?,
        };
        if !status.is_success() {
            return Err(match serde_json::from_slice::<toby_api::ApiError>(&bytes) {
                Ok(e) => Failure(e).into(),
                Err(_) => anyhow::anyhow!("tobyd: {status}: {}", String::from_utf8_lossy(&bytes)),
            });
        }
        serde_json::from_slice(&bytes).with_context(|| format!("unexpected answer from tobyd for {path}"))
    }

    pub async fn get<T: DeserializeOwned>(&self, path: &str) -> anyhow::Result<T> {
        self.call(Method::GET, path, None).await
    }

    pub async fn post<B: Serialize, T: DeserializeOwned>(&self, path: &str, body: &B) -> anyhow::Result<T> {
        self.call(Method::POST, path, Some(serde_json::to_vec(body)?)).await
    }

    /// A POST that is safe to repeat (it carries a request ID or is
    /// idempotent): retried once when the connection to a restarting daemon
    /// breaks.
    pub async fn post_again<B: Serialize, T: DeserializeOwned>(
        &self,
        path: &str,
        body: &B,
    ) -> anyhow::Result<T> {
        let bytes = serde_json::to_vec(body)?;
        match self.call(Method::POST, path, Some(bytes.clone())).await {
            Err(e) if e.downcast_ref::<Failure>().is_none() => {
                self.call(Method::POST, path, Some(bytes)).await.map_err(|again| again.context(e.to_string()))
            }
            r => r,
        }
    }

    pub async fn delete<T: DeserializeOwned>(&self, path: &str) -> anyhow::Result<T> {
        self.call(Method::DELETE, path, None).await
    }

    /// Streams a response body to `f` until it ends.
    pub async fn stream(&self, path: &str, mut f: impl FnMut(&[u8])) -> anyhow::Result<()> {
        let resp = self.send(Method::GET, path, None).await?;
        if resp.status() != StatusCode::OK {
            bail!("tobyd: {}", resp.status());
        }
        let mut body = resp.into_body();
        while let Some(frame) = body.frame().await {
            if let Some(data) = frame?.data_ref() {
                f(data);
            }
        }
        Ok(())
    }

    /// Streams a build's output to the terminal and returns its result.
    pub async fn follow_build(&self, id: &str) -> anyhow::Result<toby_api::BuildStatus> {
        use std::io::Write;
        self.stream(&format!("/v1/builds/{id}/logs"), |bytes| {
            let mut out = std::io::stdout().lock();
            let _ = out.write_all(bytes);
            let _ = out.flush();
        })
        .await?;
        let status: toby_api::BuildStatus = self.get(&format!("/v1/builds/{id}")).await?;
        if status.state != "succeeded" {
            bail!("{} (build log: {})", status.error.as_deref().unwrap_or("the build failed"), status.log);
        }
        Ok(status)
    }

    /// Prints warnings the user has not suppressed.
    pub fn warn(&self, warnings: &[toby_api::Warning]) {
        for w in warnings {
            if !self.config.settings.suppressed(&w.id) {
                eprintln!("warning[{}]: {}", w.id, w.message);
            }
        }
    }
}

/// Encodes a path segment.
pub fn segment(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || b"-._~".contains(&b) {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// A stream of tobyd's events.
pub struct Events(tokio_tungstenite::WebSocketStream<UnixStream>);

impl Events {
    /// The next event, or `None` once the connection ends.
    pub async fn next(&mut self) -> Option<toby_api::Event> {
        use futures_util::StreamExt;
        loop {
            match self.0.next().await? {
                Ok(tokio_tungstenite::tungstenite::Message::Text(t)) => {
                    if let Ok(e) = serde_json::from_str(&t) {
                        return Some(e);
                    }
                }
                Ok(tokio_tungstenite::tungstenite::Message::Close(_)) | Err(_) => return None,
                Ok(_) => {}
            }
        }
    }
}
