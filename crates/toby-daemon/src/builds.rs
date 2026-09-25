//! Jobs run by tobyd (image builds, bootstrap, home formatting, tool
//! preparation, machine starts): their progress is kept in an events file,
//! streamed to clients, and written as plain text to a log file (plan
//! §15.3).

use std::collections::HashMap;
use std::future::Future;
use std::io::Write;
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::{Arc, Mutex};

use toby_api::BuildStatus;
use toby_api::progress::{Event, Plain};
use tokio::sync::watch;

use crate::progress::Steps;

/// A running job: it returns the image it produced, if any.
pub type JobFuture = Pin<Box<dyn Future<Output = std::io::Result<Option<String>>> + Send>>;

pub struct Build {
    pub id: String,
    pub log_path: PathBuf,
    /// The job's progress events, one JSON object a line.
    pub events_path: PathBuf,
    inner: Mutex<Inner>,
    /// Bumped whenever output is added or the build finishes.
    changed: watch::Sender<u64>,
}

struct Inner {
    state: &'static str,
    error: Option<String>,
    image: Option<String>,
}

impl Build {
    pub fn status(&self) -> BuildStatus {
        let inner = self.inner.lock().unwrap();
        BuildStatus {
            id: self.id.clone(),
            state: inner.state.into(),
            error: inner.error.clone(),
            image: inner.image.clone(),
            log: self.log_path.display().to_string(),
        }
    }

    pub fn finished(&self) -> bool {
        self.inner.lock().unwrap().state != "running"
    }

    /// Up to 64 KiB of the log from `offset` on (it is not kept in
    /// memory).
    pub fn output_from(&self, offset: u64) -> std::io::Result<Vec<u8>> {
        read_from(&self.log_path, offset)
    }

    /// Up to 64 KiB of the events file from `offset` on.
    pub fn events_from(&self, offset: u64) -> std::io::Result<Vec<u8>> {
        read_from(&self.events_path, offset)
    }

    pub fn subscribe(&self) -> watch::Receiver<u64> {
        self.changed.subscribe()
    }

    fn appended(&self) {
        self.changed.send_modify(|n| *n += 1);
    }

    fn finish(&self, result: Result<Option<String>, String>) {
        {
            let mut inner = self.inner.lock().unwrap();
            match result {
                Ok(image) => {
                    inner.state = "succeeded";
                    inner.image = image;
                }
                Err(e) => {
                    inner.state = "failed";
                    inner.error = Some(e);
                }
            }
        }
        self.changed.send_modify(|n| *n += 1);
    }
}

fn read_from(path: &std::path::Path, offset: u64) -> std::io::Result<Vec<u8>> {
    use std::io::{Read, Seek};
    let mut f = std::fs::File::open(path)?;
    f.seek(std::io::SeekFrom::Start(offset))?;
    let mut buf = Vec::with_capacity(64 * 1024);
    f.take(64 * 1024).read_to_end(&mut buf)?;
    Ok(buf)
}

/// How long a finished build's output stays in memory; its log stays.
const KEEP_FINISHED: std::time::Duration = std::time::Duration::from_secs(3600);

#[derive(Default)]
pub struct Builds {
    builds: Arc<Mutex<HashMap<String, Arc<Build>>>>,
}

impl Builds {
    pub fn get(&self, id: &str) -> Option<Arc<Build>> {
        self.builds.lock().unwrap().get(id).cloned()
    }

    /// Builds running or finished within the last hour.
    pub fn list(&self) -> Vec<Arc<Build>> {
        let mut all: Vec<_> = self.builds.lock().unwrap().values().cloned().collect();
        all.sort_by(|a, b| b.id.cmp(&a.id));
        all
    }

    /// Starts `job` in the background with the steps it reports to. The
    /// job returns the image it produced, if any.
    pub fn start<F>(&self, logs: PathBuf, kind: &str, job: F) -> std::io::Result<Arc<Build>>
    where
        F: FnOnce(Steps) -> JobFuture + Send + 'static,
    {
        std::fs::create_dir_all(&logs)?;
        let id = toby_config::new_id();
        let log_path = logs.join(format!("{id}-{kind}.log"));
        let events_path = logs.join(format!("{id}-{kind}.events"));
        let files = (std::fs::File::create(&log_path)?, std::fs::File::create(&events_path)?, Plain::new(0));
        let build = Arc::new(Build {
            id: id.clone(),
            log_path,
            events_path,
            inner: Mutex::new(Inner { state: "running", error: None, image: None }),
            changed: watch::channel(0).0,
        });
        self.builds.lock().unwrap().insert(id.clone(), build.clone());
        let b = build.clone();
        let builds = self.builds.clone();
        let files = Arc::new(Mutex::new(Some(files)));
        let sink = files.clone();
        let recorded = b.clone();
        let steps = Steps::new(move |e: &Event| {
            let mut f = sink.lock().unwrap();
            let Some((log, events, plain)) = &mut *f else { return };
            let mut text = String::new();
            for line in plain.format(e) {
                text.push_str(&line);
                text.push('\n');
            }
            let _ = log.write_all(text.as_bytes());
            if let Ok(mut json) = serde_json::to_string(e) {
                json.push('\n');
                let _ = events.write_all(json.as_bytes());
            }
            recorded.appended();
        });
        tokio::spawn(async move {
            let result = job(steps.clone()).await.map_err(|e| e.to_string());
            steps.close(result.is_ok());
            drop(steps);
            // The error ends the log; the job's status carries it.
            if let Some((mut log, ..)) = files.lock().unwrap().take()
                && let Err(e) = &result
            {
                let _ = writeln!(log, "toby: {e}");
            }
            b.finish(result);
            tokio::time::sleep(KEEP_FINISHED).await;
            builds.lock().unwrap().remove(&id);
        });
        Ok(build)
    }
}
