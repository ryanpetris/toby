//! A job's steps (plan §15.3): what it is doing, as a tree of steps with
//! their output. Steps nest: a new step is part of the innermost open one,
//! and output goes to the innermost open step. A job that fails leaves its
//! open steps failed.

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use toby_api::progress::Event;

/// Lines longer than this are cut into pieces.
const MAX_LINE: usize = 4096;
/// How often a step's count is reported while it changes.
const COUNT_EVERY: Duration = Duration::from_millis(200);

/// Marks a guest job's step: `\x1eSTEP <name>` on standard output.
pub const GUEST_STEP: &str = "\x1eSTEP ";
/// Marks a step within the guest job's current step.
pub const GUEST_SUBSTEP: &str = "\x1eSUBSTEP ";

type Sink = Box<dyn Fn(&Event) + Send + Sync>;

/// The steps of one job; cloned handles share them.
#[derive(Clone)]
pub struct Steps(Arc<Inner>);

struct Inner {
    start: Instant,
    sink: Sink,
    state: Mutex<State>,
}

#[derive(Default)]
struct State {
    next: u32,
    open: Vec<u32>,
    /// Unfinished lines of standard output and standard error.
    partial: [Vec<u8>; 2],
    /// When the count of the innermost step was last reported.
    counted: Option<Instant>,
}

/// Printable text of a line of output: escape sequences and control
/// characters are dropped, tabs become spaces.
pub fn clean(bytes: &[u8]) -> String {
    let text = String::from_utf8_lossy(bytes);
    let mut out = String::with_capacity(text.len());
    let mut chars = text.chars().peekable();
    while let Some(c) = chars.next() {
        match c {
            '\x1b' => match chars.next() {
                // CSI: parameters up to a final byte.
                Some('[') => {
                    for c in chars.by_ref() {
                        if ('\x40'..='\x7e').contains(&c) {
                            break;
                        }
                    }
                }
                // OSC and other strings: up to BEL or ST.
                Some(']' | 'P' | '_' | '^' | 'X') => {
                    while let Some(c) = chars.next() {
                        if c == '\x07' || (c == '\x1b' && chars.next_if_eq(&'\\').is_some()) {
                            break;
                        }
                    }
                }
                _ => {}
            },
            '\t' => out.push_str("    "),
            c if c.is_control() => {}
            c => out.push(c),
        }
    }
    out.trim_end().to_string()
}

impl Steps {
    pub fn new(sink: impl Fn(&Event) + Send + Sync + 'static) -> Steps {
        Steps(Arc::new(Inner { start: Instant::now(), sink: Box::new(sink), state: Mutex::default() }))
    }

    /// Steps nobody sees.
    pub fn silent() -> Steps {
        Steps::new(|_| {})
    }

    fn t(&self) -> u64 {
        self.0.start.elapsed().as_millis() as u64
    }

    fn emit(&self, e: Event) {
        (self.0.sink)(&e);
    }

    /// Sends what is left of unfinished lines to the innermost step.
    fn flush(&self, s: &mut State) {
        let id = s.open.last().copied();
        for partial in &mut s.partial {
            if !partial.is_empty() {
                let line = clean(partial);
                partial.clear();
                if !line.is_empty() {
                    self.emit(Event::Output { id, line, t: self.t() });
                }
            }
        }
    }

    /// Starts a step, part of the innermost open one.
    pub fn begin(&self, name: impl Into<String>) {
        let mut s = self.0.state.lock().unwrap();
        self.flush(&mut s);
        s.next += 1;
        let id = s.next;
        let parent = s.open.last().copied();
        s.open.push(id);
        s.counted = None;
        self.emit(Event::Start { id, parent, name: name.into(), t: self.t() });
    }

    /// Finishes the innermost open step.
    pub fn end(&self) {
        let mut s = self.0.state.lock().unwrap();
        self.flush(&mut s);
        if let Some(id) = s.open.pop() {
            self.emit(Event::Done { id, t: self.t() });
        }
    }

    /// A step that had nothing to do.
    pub fn up_to_date(&self, name: impl Into<String>) {
        let mut s = self.0.state.lock().unwrap();
        s.next += 1;
        let id = s.next;
        let parent = s.open.last().copied();
        self.emit(Event::UpToDate { id, parent, name: name.into() });
    }

    /// How far the innermost step is; reported at most five times a second,
    /// and when it is complete.
    pub fn count(&self, done: u64, total: Option<u64>, bytes: bool) {
        let mut s = self.0.state.lock().unwrap();
        let Some(&id) = s.open.last() else { return };
        let complete = total.is_some_and(|t| done >= t);
        if !complete && s.counted.is_some_and(|c| c.elapsed() < COUNT_EVERY) {
            return;
        }
        s.counted = Some(Instant::now());
        self.emit(Event::Count { id, done, total, bytes, t: self.t() });
    }

    /// Output for the innermost step, split into lines. A carriage return
    /// starts its line over, as a terminal would show it.
    pub fn output(&self, bytes: &[u8], stderr: bool) {
        let mut s = self.0.state.lock().unwrap();
        let id = s.open.last().copied();
        let mut lines = Vec::new();
        let partial = &mut s.partial[usize::from(stderr)];
        for &b in bytes {
            match b {
                b'\n' => lines.push(std::mem::take(partial)),
                b'\r' => partial.clear(),
                _ => {
                    partial.push(b);
                    if partial.len() >= MAX_LINE {
                        lines.push(std::mem::take(partial));
                    }
                }
            }
        }
        drop(s);
        for line in lines {
            let line = clean(&line);
            if !line.is_empty() {
                self.emit(Event::Output { id, line, t: self.t() });
            }
        }
    }

    pub fn warn(&self, message: impl Into<String>) {
        self.emit(Event::Warning { message: message.into() });
    }

    /// How many steps are open.
    pub fn depth(&self) -> usize {
        self.0.state.lock().unwrap().open.len()
    }

    /// Finishes open steps until `depth` are left.
    pub fn end_to(&self, depth: usize) {
        while self.depth() > depth {
            self.end();
        }
    }

    /// Ends the job: its open steps finish, or fail when it failed.
    pub fn close(&self, ok: bool) {
        let mut s = self.0.state.lock().unwrap();
        self.flush(&mut s);
        while let Some(id) = s.open.pop() {
            let t = self.t();
            self.emit(if ok { Event::Done { id, t } } else { Event::Failed { id, t } });
        }
    }

    /// Output of a job in a machine, as the job's steps: `\x1eSTEP name`
    /// lines on standard output start the job's next step, and within it,
    /// `\x1eSUBSTEP name` lines, buildah's `STEP 2/5: …` and mkosi's `‣ …`
    /// lines start steps of their own. The job's steps cannot close steps it
    /// did not open.
    pub fn guest(&self) -> Guest {
        Guest { steps: self.clone(), floor: self.depth(), partial: [Vec::new(), Vec::new()], last: None }
    }
}

/// See [`Steps::guest`].
pub struct Guest {
    steps: Steps,
    /// Open steps when the job started.
    floor: usize,
    partial: [Vec<u8>; 2],
    /// The tool's last step: the same again is its output.
    last: Option<String>,
}

impl Guest {
    pub fn feed(&mut self, bytes: &[u8], stderr: bool) {
        let mut lines = Vec::new();
        {
            let partial = &mut self.partial[usize::from(stderr)];
            for &b in bytes {
                match b {
                    b'\n' => lines.push(std::mem::take(partial)),
                    b'\r' => partial.clear(),
                    _ => {
                        partial.push(b);
                        if partial.len() >= MAX_LINE {
                            lines.push(std::mem::take(partial));
                        }
                    }
                }
            }
        }
        for line in lines {
            self.line(&line, stderr);
        }
    }

    fn line(&mut self, raw: &[u8], stderr: bool) {
        let steps = &self.steps;
        if !stderr && let Some(name) = raw.strip_prefix(GUEST_STEP.as_bytes()) {
            steps.end_to(self.floor);
            steps.begin(clean(name));
            self.last = None;
            return;
        }
        if !stderr
            && steps.depth() > self.floor
            && let Some(name) = raw.strip_prefix(GUEST_SUBSTEP.as_bytes())
        {
            steps.end_to(self.floor + 1);
            steps.begin(clean(name));
            self.last = None;
            return;
        }
        let text = clean(raw);
        // A tool's own steps, within one of the job's.
        if steps.depth() > self.floor {
            let tool_step = text.strip_prefix("‣ ").map(str::trim).filter(|n| !n.is_empty()).or_else(|| {
                let rest = text.strip_prefix("STEP ")?;
                let (n, _) = rest.split_once(": ")?;
                n.split_once('/').filter(|(a, b)| a.parse::<u32>().is_ok() && b.parse::<u32>().is_ok())?;
                Some(text.as_str())
            });
            if let Some(name) = tool_step
                && self.last.as_deref() != Some(name)
            {
                let name = name.to_string();
                steps.end_to(self.floor + 1);
                steps.begin(name.clone());
                self.last = Some(name);
                return;
            }
        }
        steps.output(format!("{text}\n").as_bytes(), stderr);
    }

    /// The job ended: what is left of its lines goes out, and when it
    /// succeeded its steps finish; when it failed they stay open, to fail
    /// with the job.
    pub fn finish(mut self, ok: bool) {
        for stderr in [false, true] {
            let rest = std::mem::take(&mut self.partial[usize::from(stderr)]);
            if !rest.is_empty() {
                self.line(&rest, stderr);
            }
        }
        if ok {
            self.steps.end_to(self.floor);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn recorded() -> (Steps, Arc<Mutex<Vec<Event>>>) {
        let events = Arc::new(Mutex::new(Vec::new()));
        let sink = events.clone();
        (Steps::new(move |e| sink.lock().unwrap().push(e.clone())), events)
    }

    /// The events without their times.
    fn shape(events: &[Event]) -> Vec<String> {
        events
            .iter()
            .map(|e| match e {
                Event::Start { id, parent, name, .. } => format!("start {id} {parent:?} {name}"),
                Event::Done { id, .. } => format!("done {id}"),
                Event::Failed { id, .. } => format!("failed {id}"),
                Event::UpToDate { id, name, .. } => format!("up-to-date {id} {name}"),
                Event::Output { id, line, .. } => format!("output {id:?} {line}"),
                Event::Count { id, done, .. } => format!("count {id} {done}"),
                Event::Warning { message } => format!("warning {message}"),
            })
            .collect()
    }

    #[test]
    fn steps_nest_and_fail_with_the_job() {
        let (steps, events) = recorded();
        steps.begin("Building");
        steps.output(b"one\ntw", false);
        steps.output(b"o\r\x1b[31mthree\x1b[0m\n", false);
        steps.begin("Exporting");
        steps.count(5, Some(10), true);
        steps.count(6, Some(10), true);
        steps.count(10, Some(10), true);
        steps.end();
        steps.up_to_date("Cache");
        steps.begin("Adapting");
        steps.output(b"half", true);
        steps.close(false);
        assert_eq!(
            shape(&events.lock().unwrap()),
            [
                "start 1 None Building",
                "output Some(1) one",
                "output Some(1) three",
                "start 2 Some(1) Exporting",
                "count 2 5",
                "count 2 10",
                "done 2",
                "up-to-date 3 Cache",
                "start 4 Some(1) Adapting",
                "output Some(4) half",
                "failed 4",
                "failed 1",
            ]
        );
    }

    #[test]
    fn guest_output_becomes_steps() {
        let (steps, events) = recorded();
        steps.begin("Building the image");
        let mut guest = steps.guest();
        guest.feed(b"\x1eSTEP Building the Dockerfile\nSTEP 1/2: FROM debian\n", false);
        guest.feed(b"pulling\nSTEP 2/2: RUN make\nbuilt\nSTEP 2/2: RUN make\n", false);
        guest.feed(b"\x1eSTEP Exporting\n\x1eSUBSTEP Initramfs\nmade\n", false);
        // Standard error cannot start the job's steps, nor end steps the
        // job did not start.
        guest.feed(b"\x1eSTEP spoofed\n", true);
        guest.finish(true);
        steps.end();
        assert_eq!(
            shape(&events.lock().unwrap()),
            [
                "start 1 None Building the image",
                "start 2 Some(1) Building the Dockerfile",
                "start 3 Some(2) STEP 1/2: FROM debian",
                "output Some(3) pulling",
                "done 3",
                "start 4 Some(2) STEP 2/2: RUN make",
                "output Some(4) built",
                "output Some(4) STEP 2/2: RUN make",
                "done 4",
                "done 2",
                "start 5 Some(1) Exporting",
                "start 6 Some(5) Initramfs",
                "output Some(6) made",
                "output Some(6) STEP spoofed",
                "done 6",
                "done 5",
                "done 1",
            ]
        );
    }

    #[test]
    fn output_is_cleaned() {
        assert_eq!(clean(b"\x1b]0;title\x07a\tb\x1b[1;31mc\x1b[0m\x07 "), "a    bc");
        assert_eq!(clean(b"\xff ok"), "\u{fffd} ok");
    }
}
