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

/// How long a snapshot may take. A machine that does not answer costs at
/// most its connect (10 s) and session list (5 s) limits, so events are
/// late then, not lost.
const SNAPSHOT_TIMEOUT: Duration = Duration::from_secs(30);

pub struct Events {
    tx: broadcast::Sender<Event>,
    /// Wakes the watcher for a new listener.
    subscribed: tokio::sync::Notify,
}

impl Default for Events {
    fn default() -> Events {
        Events { tx: broadcast::channel(256).0, subscribed: tokio::sync::Notify::new() }
    }
}

impl Events {
    pub fn subscribe(&self) -> broadcast::Receiver<Event> {
        let rx = self.tx.subscribe();
        self.subscribed.notify_one();
        rx
    }
}

fn resync() -> Event {
    Event { kind: "resync".into(), id: String::new(), state: String::new(), machine: None }
}

/// What is compared: each item's state, what else of it matters, and its
/// machine.
type Snapshot = HashMap<(&'static str, String), (String, String, Option<String>)>;

async fn snapshot(d: &Daemon) -> Option<Snapshot> {
    let mut s = Snapshot::new();
    let all = toby_api::MachineFilter::default();
    let (machines, sessions) = tokio::join!(d.machines.list(&all), d.machines.sessions());
    for m in machines {
        // Uptime and idle time change all the time.
        let rest =
            serde_json::to_string(&(&m.error, m.sessions, &m.attachments, &m.forwards)).unwrap_or_default();
        s.insert(("machine", m.id), (m.state, rest, None));
    }
    for (machine, session) in sessions {
        let state = if session.exit.is_some() { "exited" } else { "running" };
        s.insert(("session", session.id), (state.into(), session.attached.to_string(), Some(machine)));
    }
    // A list that cannot be read is not a list of nothing.
    let Ok(approvals) = d.approvals.list() else { return None };
    for a in approvals {
        s.insert(("approval", a.id), (a.status, String::new(), Some(a.machine)));
    }
    for b in d.builds.list() {
        let status = b.status();
        s.insert(("build", status.id), (status.state, String::new(), None));
    }
    Some(s)
}

/// Sends changes while anyone listens. The first comparison after nobody
/// listened sends `resync`: what changed before it is not known.
pub async fn watch(d: Arc<Daemon>) {
    let mut previous: Option<Snapshot> = None;
    loop {
        tokio::select! {
            _ = tokio::time::sleep(INTERVAL) => {}
            _ = d.events.subscribed.notified(), if previous.is_none() => {}
        }
        let tx = &d.events.tx;
        if tx.receiver_count() == 0 {
            previous = None;
            continue;
        }
        let Ok(Some(now)) = tokio::time::timeout(SNAPSHOT_TIMEOUT, snapshot(&d)).await else { continue };
        if previous.is_none() {
            let _ = tx.send(resync());
        }
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
                    Err(broadcast::error::RecvError::Lagged(_)) => resync(),
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
