//! Changes for `GET /v1/events` (plan §18): while anyone listens, tobyd
//! compares machines, sessions, approvals and builds every two seconds and
//! sends what changed.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use toby_api::Event;
use tokio::sync::broadcast;

use crate::server::Daemon;

const INTERVAL: Duration = Duration::from_secs(2);

pub struct Events {
    tx: broadcast::Sender<Event>,
}

impl Default for Events {
    fn default() -> Events {
        Events { tx: broadcast::channel(256).0 }
    }
}

impl Events {
    pub fn subscribe(&self) -> broadcast::Receiver<Event> {
        self.tx.subscribe()
    }
}

/// What is compared: each item's state, what else of it matters, and its
/// machine.
type Snapshot = HashMap<(&'static str, String), (String, String, Option<String>)>;

async fn snapshot(d: &Daemon) -> Snapshot {
    let mut s = Snapshot::new();
    for m in d.machines.list().await {
        // Uptime and idle time change all the time.
        let rest =
            serde_json::to_string(&(&m.error, m.sessions, &m.attachments, &m.forwards)).unwrap_or_default();
        s.insert(("machine", m.id), (m.state, rest, None));
    }
    for (machine, session) in d.machines.sessions().await {
        let state = if session.exit.is_some() { "exited" } else { "running" };
        s.insert(("session", session.id), (state.into(), session.attached.to_string(), Some(machine)));
    }
    for a in d.approvals.list().unwrap_or_default() {
        s.insert(("approval", a.id), (a.status, String::new(), Some(a.machine)));
    }
    for b in d.builds.list() {
        let status = b.status();
        s.insert(("build", status.id), (status.state, String::new(), None));
    }
    s
}

/// Sends changes while anyone listens.
pub async fn watch(d: Arc<Daemon>) {
    let mut previous: Option<Snapshot> = None;
    loop {
        tokio::time::sleep(INTERVAL).await;
        let tx = &d.events.tx;
        if tx.receiver_count() == 0 {
            previous = None;
            continue;
        }
        let now = snapshot(&d).await;
        if let Some(before) = &previous {
            for (key, (state, rest, machine)) in &now {
                if before.get(key).is_none_or(|(s, r, _)| s != state || r != rest) {
                    let _ = tx.send(Event {
                        kind: key.0.into(),
                        id: key.1.clone(),
                        state: state.clone(),
                        machine: machine.clone(),
                    });
                }
            }
            for (key, (_, _, machine)) in before.iter().filter(|(k, _)| !now.contains_key(*k)) {
                let _ = tx.send(Event {
                    kind: key.0.into(),
                    id: key.1.clone(),
                    state: "removed".into(),
                    machine: machine.clone(),
                });
            }
        }
        previous = Some(now);
    }
}

/// Serves one `GET /v1/events` WebSocket.
pub async fn serve(d: Arc<Daemon>, mut socket: axum::extract::ws::WebSocket) {
    use axum::extract::ws::Message;
    let mut rx = d.events.subscribe();
    loop {
        tokio::select! {
            e = rx.recv() => {
                let e = match e {
                    Ok(e) => e,
                    Err(broadcast::error::RecvError::Lagged(_)) => {
                        Event { kind: "resync".into(), id: String::new(), state: String::new(), machine: None }
                    }
                    Err(broadcast::error::RecvError::Closed) => return,
                };
                let text = serde_json::to_string(&e).unwrap_or_default();
                if socket.send(Message::Text(text.into())).await.is_err() {
                    return;
                }
            }
            m = socket.recv() => match m {
                Some(Ok(Message::Close(_))) | Some(Err(_)) | None => return,
                Some(Ok(_)) => {}
            },
        }
    }
}
