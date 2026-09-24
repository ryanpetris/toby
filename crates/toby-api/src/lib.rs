//! Types of the tobyd HTTP API (plan §18). Requests and responses are JSON;
//! errors are an [`ApiError`] body with a non-success status.

use serde::{Deserialize, Serialize};
use toby_proto::types::{Identity, SessionInfo, TtySize};

/// Path of the API socket below the runtime directory.
pub const SOCKET: &str = "tobyd.sock";

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ApiError {
    /// Stable identifier, e.g. `machine.pair-in-use`.
    pub code: String,
    pub message: String,
}

/// A warning for the CLI to print unless the user suppressed its ID.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Warning {
    pub id: String,
    pub message: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DaemonInfo {
    pub version: String,
    pub pid: u32,
    /// `systemd-user` or `direct`.
    pub backend: String,
    /// Whether logind keeps the user's processes after logout (systemd-user).
    pub linger: Option<bool>,
    pub state_dir: String,
    pub data_dir: String,
    pub runtime_dir: String,
}

/// `GET /v1/machines`, and the result of starting one.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct MachineInfo {
    pub id: String,
    pub home: Option<String>,
    pub root: String,
    pub image: Option<String>,
    /// `stopped`, `starting`, `ready`, `stopping` or `failed`.
    pub state: String,
    pub error: Option<String>,
    pub sessions: usize,
    pub attachments: Vec<AttachmentInfo>,
    pub forwards: Vec<ForwardInfo>,
    pub uptime_secs: Option<u64>,
    pub idle_secs: Option<u64>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AttachmentInfo {
    pub id: String,
    pub host: String,
    pub at: String,
    pub read_only: bool,
    pub pinned: bool,
    pub persist: bool,
    /// `ready`, `failed` or `pending`.
    pub state: String,
    pub error: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ForwardInfo {
    pub id: String,
    /// `host-to-guest` or `guest-to-host`.
    pub direction: String,
    pub host: String,
    pub guest: String,
    pub pinned: bool,
    pub persist: bool,
    /// `listening`, `failed` or `pending`.
    pub state: String,
    pub error: Option<String>,
}

/// `POST /v1/machines/{id}/forwards`. Addresses are `ADDR:PORT`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AddForward {
    /// `host-to-guest` or `guest-to-host`.
    pub direction: String,
    pub host: String,
    pub guest: String,
    #[serde(default)]
    pub pinned: bool,
    #[serde(default)]
    pub persist: bool,
}

/// `POST /v1/machines/ensure`: the machine for a home and root, started if
/// needed. Unset fields use the configured defaults.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct EnsureMachine {
    pub home: Option<String>,
    pub root: Option<String>,
    #[serde(default)]
    pub ephemeral: bool,
    /// Resources for a machine that is started (default: plan §14.5).
    #[serde(default)]
    pub cpus: Option<u32>,
    /// Memory such as `8G`.
    #[serde(default)]
    pub memory: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Ensured {
    pub machine: MachineInfo,
    pub warnings: Vec<Warning>,
}

/// `POST /v1/machines/{id}/attachments`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AddAttachment {
    pub host: String,
    /// Default: `/toby/workspace/<directory name>`.
    pub at: Option<String>,
    #[serde(default)]
    pub read_only: bool,
    /// Kept while the machine runs, even without sessions.
    #[serde(default)]
    pub pinned: bool,
    /// Recreated every time the machine starts.
    #[serde(default)]
    pub persist: bool,
}

/// Selects a machine: by ID, or by home and root (started if needed).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct MachineSelector {
    pub machine: Option<String>,
    pub home: Option<String>,
    pub root: Option<String>,
}

/// `POST /v1/sessions`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CreateSession {
    #[serde(flatten)]
    pub target: MachineSelector,
    /// Chosen by the client (letters and digits, at most 64): it becomes the
    /// session's ID, so a request repeated after a lost connection gets the
    /// session the first one created.
    #[serde(default)]
    pub request_id: Option<String>,
    /// A tool to launch (plan §16.1); `argv` then holds extra arguments.
    #[serde(default)]
    pub tool: Option<String>,
    /// Launch the tool without its permission prompts.
    #[serde(default)]
    pub yolo: bool,
    /// Directories attached for as long as sessions use them; the first is
    /// the working directory unless `cwd` is given.
    #[serde(default)]
    pub attachments: Vec<AddAttachment>,
    pub argv: Vec<String>,
    #[serde(default)]
    pub env: Vec<(String, String)>,
    pub cwd: Option<String>,
    pub identity: Identity,
    pub tty: Option<TtySize>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionCreated {
    pub id: String,
    pub machine: String,
    /// Where the client attaches (plan §13.3).
    pub session_socket: String,
    /// The machine's control socket, for signals.
    pub control_socket: String,
    pub warnings: Vec<Warning>,
}

/// `GET /v1/sessions`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct MachineSession {
    pub machine: String,
    pub session_socket: String,
    pub control_socket: String,
    #[serde(flatten)]
    pub session: SessionInfo,
}

/// `POST /v1/machines/{id}/tools/{name}/prepare`: check, install or update
/// a tool and write its files; returns a build ID.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PrepareTool {
    #[serde(default)]
    pub upgrade: bool,
}

/// `POST /v1/sessions/{id}/kill`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct KillSession {
    /// A signal number; default: end the session (hangup, then terminate,
    /// then kill).
    pub signal: Option<i32>,
}

/// A build or other builder job (`POST /v1/builds`, `/v1/homes`,
/// `/v1/images/prepare`): its logs stream from `GET /v1/builds/{id}/logs`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct BuildStarted {
    pub id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct BuildStatus {
    pub id: String,
    /// `running`, `succeeded` or `failed`.
    pub state: String,
    pub error: Option<String>,
    /// The image a successful build produced.
    pub image: Option<String>,
    pub log: String,
}

/// An image source as the API names it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Source {
    Default,
    Dockerfile { path: String, context: String },
    Mkosi { path: String },
    Registry { reference: String },
    Archive { path: String },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct StartBuild {
    pub source: Source,
}

/// `POST /v1/images/prepare` (plan §15.6).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Prepare {
    #[serde(default)]
    pub all: bool,
    #[serde(default)]
    pub rebuild: bool,
}

/// `POST /v1/bootstrap`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Bootstrap {
    /// A local Debian 13 cloud image to start from instead of downloading one.
    pub base: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ImageInfo {
    pub id: String,
    pub created: u64,
    pub source: String,
    /// The current default image.
    pub current_default: bool,
    pub kernel: String,
    pub roots: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RootInfo {
    pub name: String,
    pub image: String,
    pub created: u64,
    pub newer_image: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CreateRoot {
    pub name: String,
    /// An image ID or `default`.
    pub image: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Rebase {
    /// Default: the newest image of the root's source.
    pub image: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HomeInfo {
    pub name: String,
    pub username: String,
    pub uid: u32,
    /// The root machines of this home use unless another is given.
    pub default_root: Option<String>,
    pub formatted: bool,
    pub created: u64,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CreateHome {
    pub name: String,
    pub username: String,
    pub uid: u32,
}

/// `GET /v1/approvals`: pending first, then recent decisions.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ApprovalInfo {
    pub id: String,
    pub created: u64,
    pub machine: String,
    pub kind: String,
    pub summary: String,
    pub detail: String,
    /// `pending`, `approved`, `denied` or `expired`.
    pub status: String,
}

/// `POST /v1/approvals/{id}`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Decide {
    /// `approve` or `deny`.
    pub decision: String,
}

/// `POST /v1/versions/gc`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct VersionsCollected {
    pub removed: Vec<String>,
    /// Versions still in use.
    pub kept: Vec<String>,
    /// Versions that could not be removed, with the reason.
    pub failed: Vec<(String, String)>,
}

/// Names removed by a prune.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Pruned {
    pub images: Vec<String>,
    pub caches: Vec<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sessions_flatten_their_target() {
        let req = CreateSession {
            target: MachineSelector { home: Some("work".into()), ..Default::default() },
            request_id: None,
            tool: None,
            yolo: false,
            attachments: Vec::new(),
            argv: vec!["bash".into()],
            env: Vec::new(),
            cwd: None,
            identity: Identity::User,
            tty: Some(TtySize { rows: 24, cols: 80 }),
        };
        let json = serde_json::to_value(&req).unwrap();
        assert_eq!(json["home"], "work");
        assert_eq!(serde_json::from_value::<CreateSession>(json).unwrap(), req);
    }

    #[test]
    fn sources_are_tagged() {
        let json = serde_json::to_string(&Source::Registry { reference: "r".into() }).unwrap();
        assert_eq!(json, r#"{"registry":{"reference":"r"}}"#);
        assert_eq!(serde_json::to_string(&Source::Default).unwrap(), r#""default""#);
    }
}
