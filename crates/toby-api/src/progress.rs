//! Progress of a job (plan §15.3): the steps it goes through, their output
//! and warnings, as events streamed by `GET /v1/builds/{id}/events`. The
//! plain format here is the job's log file, and the CLI's output when it
//! does not write to a terminal.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

/// One event of a job's progress. Steps are numbered from 1 within the job;
/// `t` is milliseconds since the job started.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, utoipa::ToSchema)]
#[serde(tag = "event", rename_all = "snake_case")]
pub enum Event {
    /// A step starts, as part of `parent`.
    Start {
        id: u32,
        parent: Option<u32>,
        name: String,
        t: u64,
    },
    Done {
        id: u32,
        t: u64,
    },
    /// A step with nothing to do.
    UpToDate {
        id: u32,
        parent: Option<u32>,
        name: String,
    },
    Failed {
        id: u32,
        t: u64,
    },
    /// How far a step is: bytes or items done, of `total` if known.
    Count {
        id: u32,
        done: u64,
        total: Option<u64>,
        bytes: bool,
        t: u64,
    },
    /// A line a step printed; without a step, one the job printed.
    Output {
        id: Option<u32>,
        line: String,
        t: u64,
    },
    Warning {
        message: String,
    },
}

/// `1.5 GiB`, `3.2 MiB`, `512 B`.
pub fn size(n: u64) -> String {
    const UNITS: [&str; 5] = ["B", "KiB", "MiB", "GiB", "TiB"];
    let mut v = n as f64;
    let mut unit = 0;
    while v >= 1024.0 && unit < UNITS.len() - 1 {
        v /= 1024.0;
        unit += 1;
    }
    if unit == 0 { format!("{n} B") } else { format!("{v:.1} {}", UNITS[unit]) }
}

/// `12.3s`, `4m 05s`, `1h 02m`.
pub fn elapsed(ms: u64) -> String {
    let s = ms / 1000;
    match s {
        0..60 => format!("{}.{}s", s, ms % 1000 / 100),
        60..3600 => format!("{}m {:02}s", s / 60, s % 60),
        _ => format!("{}h {:02}m", s / 3600, s % 3600 / 60),
    }
}

/// `done / total` in the step's unit.
pub fn count(done: u64, total: Option<u64>, bytes: bool) -> String {
    let n = |v: u64| if bytes { size(v) } else { v.to_string() };
    match total {
        Some(t) => format!("{} / {}", n(done), n(t)),
        None => n(done),
    }
}

/// Formats events as lines, as `docker buildx --progress=plain` does:
/// `#3 name`, `#3 1.2 output`, `#3 DONE 4.5s`.
#[derive(Debug, Default)]
pub struct Plain {
    /// Start time and name of each step.
    steps: HashMap<u32, (u64, String)>,
    /// When each step's count was last shown.
    counted: HashMap<u32, u64>,
    /// Added to every step number, to number several jobs' steps in one
    /// sequence.
    pub offset: u32,
}

impl Plain {
    pub fn new(offset: u32) -> Plain {
        Plain { offset, ..Default::default() }
    }

    fn heading(&self, id: u32, parent: Option<u32>, name: &str) -> String {
        match parent.and_then(|p| self.steps.get(&p)) {
            Some((_, p)) => format!("#{} [{p}] {name}", id + self.offset),
            None => format!("#{} {name}", id + self.offset),
        }
    }

    /// The lines for an event, if any.
    pub fn format(&mut self, e: &Event) -> Vec<String> {
        match e {
            Event::Start { id, parent, name, t } => {
                let line = self.heading(*id, *parent, name);
                self.steps.insert(*id, (*t, name.clone()));
                vec![line]
            }
            Event::UpToDate { id, parent, name } => {
                let line = self.heading(*id, *parent, name);
                self.steps.insert(*id, (0, name.clone()));
                vec![line, format!("#{} UP TO DATE", id + self.offset)]
            }
            Event::Done { id, t } => {
                let start = self.steps.get(id).map_or(*t, |s| s.0);
                vec![format!("#{} DONE {}", id + self.offset, elapsed(t.saturating_sub(start)))]
            }
            Event::Failed { id, .. } => vec![format!("#{} ERROR", id + self.offset)],
            Event::Count { id, done, total, bytes, t } => {
                // Every five seconds, and at the end.
                let last = self.counted.get(id).copied();
                if last.is_some_and(|l| t.saturating_sub(l) < 5000) && total.is_none_or(|n| *done < n) {
                    return Vec::new();
                }
                self.counted.insert(*id, *t);
                vec![format!("#{} {}", id + self.offset, count(*done, *total, *bytes))]
            }
            Event::Output { id: Some(id), line, t } => {
                let start = self.steps.get(id).map_or(*t, |s| s.0);
                let secs = t.saturating_sub(start);
                vec![format!("#{} {}.{:03} {line}", id + self.offset, secs / 1000, secs % 1000)]
            }
            Event::Output { id: None, line, .. } => vec![line.clone()],
            Event::Warning { message } => vec![message.clone()],
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn plain_lines() {
        let mut p = Plain::new(0);
        let events = [
            Event::Start { id: 1, parent: None, name: "Building the default image".into(), t: 0 },
            Event::Start { id: 2, parent: Some(1), name: "mkosi".into(), t: 1000 },
            Event::Output { id: Some(2), line: "‣ Installing".into(), t: 2500 },
            Event::Count { id: 2, done: 10, total: Some(100), bytes: false, t: 2600 },
            Event::Count { id: 2, done: 20, total: Some(100), bytes: false, t: 3000 },
            Event::Done { id: 2, t: 61_000 },
            Event::UpToDate { id: 3, parent: Some(1), name: "Build cache".into() },
            Event::Failed { id: 1, t: 62_000 },
        ];
        let lines: Vec<String> = events.iter().flat_map(|e| p.format(e)).collect();
        assert_eq!(
            lines,
            [
                "#1 Building the default image",
                "#2 [Building the default image] mkosi",
                "#2 1.500 ‣ Installing",
                "#2 10 / 100",
                "#2 DONE 1m 00s",
                "#3 [Building the default image] Build cache",
                "#3 UP TO DATE",
                "#1 ERROR",
            ]
        );
        assert_eq!(size(612 * 1024 * 1024), "612.0 MiB");
        assert_eq!(elapsed(4_512), "4.5s");
        let json = serde_json::to_string(&Event::Done { id: 1, t: 5 }).unwrap();
        assert_eq!(json, r#"{"event":"done","id":1,"t":5}"#);
    }
}
