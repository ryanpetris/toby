//! The terminal compositor (plan §13.5). A session's output goes to the
//! terminal as it is, so the terminal's own scrollback, colors, mouse and
//! paste keep working, while a copy feeds an emulator of the session's
//! screen. The session gets every row but the last, which holds a status
//! line below a scroll region the session cannot widen; approvals open as
//! overlays over the session and are repainted from the emulator when they
//! close.

use unicode_width::UnicodeWidthChar;

/// What the status line and the approval overlay show.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Status {
    /// Items of the status line, such as the machine.
    pub items: Vec<String>,
    /// Approvals waiting for the user.
    pub approvals: Vec<Approval>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Approval {
    pub id: String,
    /// The action, such as `git.push`.
    pub kind: String,
    pub summary: String,
    pub detail: String,
}

/// A decision made in the overlay: the approval and whether it was
/// approved.
pub type Decision = (String, bool);

const SYNC_ON: &[u8] = b"\x1b[?2026h";
const SYNC_OFF: &[u8] = b"\x1b[?2026l";
/// Longest control sequence rewritten; longer ones pass unchanged.
const MAX_SEQUENCE: usize = 64;

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
enum State {
    #[default]
    Ground,
    Escape,
    /// After ESC and an intermediate byte, such as `(`.
    EscapeIntermediate(u8),
    Csi,
    /// A string (OSC, DCS, APC, PM, SOS) until BEL or ST.
    Text,
    TextEscape,
}

/// Follows the session's output on its way to the terminal: keeps absolute
/// rows and scroll regions within the session's rows, and notes what the
/// status line has to be drawn again after.
#[derive(Debug, Default)]
struct Scan {
    state: State,
    sequence: Vec<u8>,
    /// The session's scroll region (1-based, inclusive), as the terminal
    /// has it.
    top: u16,
    bottom: u16,
    origin: bool,
    insert: bool,
    /// The session is in a synchronized update.
    sync: bool,
    /// G0 is the DEC special graphics set.
    graphics: bool,
    /// Shifted to G1.
    shifted: bool,
    /// The status line may have been overwritten.
    damaged: bool,
    /// The terminal may have lost the scroll region.
    region_lost: bool,
}

fn numbers(params: &[u8]) -> Vec<Option<u16>> {
    params.split(|b| *b == b';').map(|n| std::str::from_utf8(n).ok()?.parse().ok()).collect()
}

impl Scan {
    fn new(limit: u16) -> Scan {
        Scan { top: 1, bottom: limit, ..Default::default() }
    }

    fn reset(&mut self, limit: u16) {
        *self = Scan { damaged: true, region_lost: true, ..Scan::new(limit) };
    }

    /// Passes `input` to `out`, rewriting what would reach the status line.
    /// `limit` is the session's last row.
    fn feed(&mut self, input: &[u8], limit: u16, out: &mut Vec<u8>) {
        for &b in input {
            match self.state {
                State::Ground => match b {
                    0x1b => {
                        self.state = State::Escape;
                        self.sequence.clear();
                        self.sequence.push(b);
                        continue;
                    }
                    0x0e => self.shifted = true,
                    0x0f => self.shifted = false,
                    _ => {}
                },
                // The escape is written with the byte after it, or held in
                // `sequence` with a control sequence until it is complete.
                State::Escape => {
                    match b {
                        b'[' => {
                            self.sequence.push(b);
                            self.state = State::Csi;
                            continue;
                        }
                        b']' | b'P' | b'_' | b'^' | b'X' => self.state = State::Text,
                        0x20..=0x2f => self.state = State::EscapeIntermediate(b),
                        b'c' => {
                            self.reset(limit);
                            self.state = State::Ground;
                        }
                        _ => self.state = State::Ground,
                    }
                    out.push(0x1b);
                }
                State::EscapeIntermediate(i) => {
                    match (i, b) {
                        (b'(', _) => self.graphics = b == b'0',
                        (b'#', b'8') => self.damaged = true,
                        _ => {}
                    }
                    self.state = State::Ground;
                }
                State::Csi => {
                    self.sequence.push(b);
                    if (0x40..=0x7e).contains(&b) {
                        self.state = State::Ground;
                        let sequence = std::mem::take(&mut self.sequence);
                        self.csi(&sequence, limit, out);
                        self.sequence = sequence;
                    } else if self.sequence.len() > MAX_SEQUENCE {
                        self.state = State::Ground;
                        out.extend_from_slice(&self.sequence);
                    }
                    continue;
                }
                State::Text => match b {
                    0x07 => self.state = State::Ground,
                    0x1b => self.state = State::TextEscape,
                    _ => {}
                },
                State::TextEscape => {
                    self.state = if b == b'\\' { State::Ground } else { State::Text };
                }
            }
            out.push(b);
        }
    }

    /// A complete control sequence, `ESC [ … final`.
    fn csi(&mut self, sequence: &[u8], limit: u16, out: &mut Vec<u8>) {
        let body = &sequence[2..sequence.len() - 1];
        let fin = sequence[sequence.len() - 1];
        let (private, params) = match body.first() {
            Some(&c @ (b'?' | b'>' | b'<' | b'=')) => (Some(c), &body[1..]),
            _ => (None, body),
        };
        let intermediate = params.iter().any(|b| (0x20..=0x2f).contains(b));
        let n = numbers(params);
        let first = n.first().copied().flatten().unwrap_or(0);
        match (private, intermediate, fin) {
            (None, false, b'r') => {
                let top = first.max(1);
                let bottom = n.get(1).copied().flatten().filter(|b| *b > 0).unwrap_or(limit).min(limit);
                if top < bottom {
                    self.top = top;
                    self.bottom = bottom;
                }
                out.extend(format!("\x1b[{top};{bottom}r").as_bytes());
                return;
            }
            (None, false, b'H' | b'f' | b'd') if first > limit => {
                let rest = params.iter().position(|b| *b == b';').map(|i| &params[i..]).unwrap_or(&[]);
                out.extend(format!("\x1b[{limit}").as_bytes());
                out.extend_from_slice(rest);
                out.push(fin);
                return;
            }
            (None, false, b'J') if first != 1 => self.damaged = true,
            (None, false, b'h' | b'l') if n.contains(&Some(4)) => self.insert = fin == b'h',
            (None, true, b'p') if params.ends_with(b"!") => {
                // A soft reset: margins, origin and insert mode go.
                self.top = 1;
                self.bottom = limit;
                self.origin = false;
                self.insert = false;
                self.damaged = true;
                self.region_lost = true;
            }
            (Some(b'?'), false, b'h' | b'l') => {
                let on = fin == b'h';
                for m in n.into_iter().flatten() {
                    match m {
                        6 => self.origin = on,
                        2026 => self.sync = on,
                        47 | 1047 | 1049 => {
                            self.damaged = true;
                            self.region_lost = true;
                        }
                        _ => {}
                    }
                }
            }
            _ => {}
        }
        out.extend_from_slice(sequence);
    }

    /// Makes the terminal write plain text at the cursor.
    fn neutral(&self, out: &mut Vec<u8>) {
        if self.insert {
            out.extend(b"\x1b[4l");
        }
        if self.graphics {
            out.extend(b"\x1b(B");
        }
        if self.shifted {
            out.push(0x0f);
        }
    }

    /// Undoes `neutral`.
    fn restore(&self, out: &mut Vec<u8>) {
        if self.insert {
            out.extend(b"\x1b[4h");
        }
        if self.graphics {
            out.extend(b"\x1b(0");
        }
        if self.shifted {
            out.push(0x0e);
        }
    }
}

/// Display width of a string.
fn width(s: &str) -> usize {
    s.chars().map(|c| c.width().unwrap_or(0)).sum()
}

/// `s` cut or padded to exactly `cols` columns.
fn fit(s: &str, cols: usize) -> String {
    let mut out = String::new();
    let mut used = 0;
    for c in s.chars() {
        let w = c.width().unwrap_or(0);
        if used + w > cols {
            break;
        }
        out.push(c);
        used += w;
    }
    out.extend(std::iter::repeat_n(' ', cols - used));
    out
}

/// Lines of at most `cols` columns, broken at spaces where possible.
fn wrap(text: &str, cols: usize) -> Vec<String> {
    let mut lines = Vec::new();
    for paragraph in text.lines() {
        let mut line = String::new();
        for word in paragraph.split(' ') {
            let sep = usize::from(!line.is_empty());
            if width(&line) + sep + width(word) <= cols {
                if sep == 1 {
                    line.push(' ');
                }
                line.push_str(word);
                continue;
            }
            if !line.is_empty() {
                lines.push(std::mem::take(&mut line));
            }
            for c in word.chars() {
                if width(&line) + c.width().unwrap_or(0) > cols {
                    lines.push(std::mem::take(&mut line));
                }
                line.push(c);
            }
        }
        lines.push(line);
    }
    lines
}

/// The session's screen with a status line and overlays on the terminal.
pub struct Compositor {
    rows: u16,
    cols: u16,
    screen: vt100::Parser,
    scan: Scan,
    status: Status,
    /// The approval the overlay shows.
    shown: Option<String>,
    /// Where the overlay is: first row (1-based), column, height, width.
    overlay_at: Option<(u16, u16, u16, u16)>,
    /// Approvals seen, dismissed or answered here.
    seen: std::collections::HashSet<String>,
    answered: std::collections::HashSet<String>,
    /// Drawing waits until the session leaves origin mode.
    pending: bool,
}

impl Compositor {
    /// Whether a terminal of this size has room for the status line.
    pub fn fits(rows: u16, cols: u16) -> bool {
        rows >= 3 && cols >= 20
    }

    /// For a terminal of `rows`×`cols` whose cursor is at `cursor` (1-based
    /// row and column), if known.
    pub fn new(rows: u16, cols: u16) -> Compositor {
        Compositor {
            rows,
            cols,
            screen: vt100::Parser::new(rows - 1, cols, 0),
            scan: Scan::new(rows - 1),
            status: Status::default(),
            shown: None,
            overlay_at: None,
            seen: Default::default(),
            answered: Default::default(),
            pending: false,
        }
    }

    /// The session's terminal size.
    pub fn session_size(&self) -> (u16, u16) {
        (self.rows - 1, self.cols)
    }

    fn limit(&self) -> u16 {
        self.rows - 1
    }

    /// Takes over the terminal: frees its last row for the status line.
    /// `cursor` is where the terminal's cursor is (1-based), if known;
    /// otherwise the screen is cleared.
    pub fn start(&mut self, cursor: Option<(u16, u16)>) -> Vec<u8> {
        let limit = self.limit();
        let mut out = Vec::new();
        let (row, col) = match cursor {
            // A new line keeps a row free below the cursor, which then
            // goes back up (apt's progress line does the same).
            Some((row, col)) => {
                out.extend(format!("\n\x1b7\x1b[1;{limit}r\x1b8\x1b[A").as_bytes());
                (if row < self.rows { row } else { limit }, col)
            }
            None => {
                out.extend(format!("\x1b[2J\x1b[1;{limit}r\x1b[H").as_bytes());
                (1, 1)
            }
        };
        self.screen.process(format!("\x1b[{row};{col}H").as_bytes());
        self.draw(&mut out);
        out
    }

    /// Bytes for the terminal after the session wrote `bytes`.
    pub fn output(&mut self, bytes: &[u8]) -> Vec<u8> {
        let mut out = Vec::new();
        let overlay = self.overlay_at.is_some();
        let sync = overlay && !self.scan.sync;
        if sync {
            out.extend(SYNC_ON);
        }
        if overlay && !self.scan.origin {
            self.erase_overlay(&mut out);
            self.put_cursor(&mut out);
        }
        self.scan.feed(bytes, self.limit(), &mut out);
        self.screen.process(bytes);
        if self.scan.damaged || self.scan.region_lost || overlay || self.pending {
            self.draw(&mut out);
        }
        if sync {
            out.extend(SYNC_OFF);
        }
        out
    }

    /// The terminal changed size to `rows`×`cols`.
    pub fn resize(&mut self, rows: u16, cols: u16) -> Vec<u8> {
        self.rows = rows;
        self.cols = cols;
        let limit = self.limit();
        self.screen.screen_mut().set_size(limit, cols);
        self.scan.bottom = self.scan.bottom.min(limit);
        if self.scan.top >= self.scan.bottom {
            self.scan.top = 1;
            self.scan.bottom = limit;
        }
        self.scan.region_lost = true;
        self.overlay_at = None;
        let mut out = Vec::new();
        self.draw(&mut out);
        out
    }

    /// New status; may open the overlay for a new approval.
    pub fn set_status(&mut self, status: Status) -> Vec<u8> {
        let fresh = status.approvals.iter().find(|a| !self.seen.contains(&a.id)).map(|a| a.id.clone());
        for a in &status.approvals {
            self.seen.insert(a.id.clone());
        }
        let live: std::collections::HashSet<&String> = status.approvals.iter().map(|a| &a.id).collect();
        self.answered.retain(|id| live.contains(id));
        self.status = status;
        if self.shown.as_ref().is_some_and(|id| !self.pending_approvals().any(|a| &a.id == id)) {
            self.shown = self.first_pending();
        }
        if self.shown.is_none() {
            self.shown = fresh;
        }
        let mut out = Vec::new();
        self.redraw(&mut out);
        out
    }

    fn pending_approvals(&self) -> impl Iterator<Item = &Approval> {
        self.status.approvals.iter().filter(|a| !self.answered.contains(&a.id))
    }

    fn first_pending(&self) -> Option<String> {
        self.pending_approvals().next().map(|a| a.id.clone())
    }

    /// Opens the overlay on the first approval waiting, if any.
    pub fn open_approvals(&mut self) -> Vec<u8> {
        self.shown = self.first_pending();
        let mut out = Vec::new();
        self.redraw(&mut out);
        out
    }

    /// Whether keys go to the overlay instead of the session.
    pub fn overlay_open(&self) -> bool {
        self.shown.is_some()
    }

    /// Keys typed while the overlay is open: the bytes for the terminal and
    /// a decision, if one was made.
    pub fn key(&mut self, input: &[u8]) -> (Vec<u8>, Option<Decision>) {
        let Some(id) = self.shown.clone() else { return (Vec::new(), None) };
        let decision = match key(input) {
            Some(Key::Char('y' | 'Y')) => Some(true),
            Some(Key::Char('n' | 'N')) => Some(false),
            Some(Key::Escape) => None,
            _ => return (Vec::new(), None),
        };
        if decision.is_some() {
            self.answered.insert(id.clone());
        }
        // The next approval, if another waits and this one was answered.
        self.shown = match decision {
            Some(_) => self.first_pending(),
            None => None,
        };
        let mut out = Vec::new();
        self.redraw(&mut out);
        (out, decision.map(|d| (id, d)))
    }

    /// Gives the terminal back: `restore` (the session's modes turned off)
    /// is written first, then the scroll region and status line go.
    pub fn finish(&mut self, restore: &[u8]) -> Vec<u8> {
        let mut out = Vec::new();
        if !self.scan.origin {
            self.erase_overlay(&mut out);
        }
        self.overlay_at = None;
        out.extend(restore);
        self.screen.process(restore);
        let rows = self.rows;
        out.extend(format!("\x1b[r\x1b[{rows};1H\x1b[0m\x1b[2K").as_bytes());
        out.extend(self.screen.screen().cursor_state_formatted());
        out.extend(b"\x1b[0m");
        out
    }

    /// Draws the overlay (or erases it) and the status line again.
    fn redraw(&mut self, out: &mut Vec<u8>) {
        let sync = !self.scan.sync;
        if sync {
            out.extend(SYNC_ON);
        }
        if !self.scan.origin {
            self.erase_overlay(out);
        }
        self.draw(out);
        if sync {
            out.extend(SYNC_OFF);
        }
    }

    /// Draws the status line and the overlay, then puts the cursor and
    /// attributes back. Waits while the session uses origin mode.
    fn draw(&mut self, out: &mut Vec<u8>) {
        if self.scan.origin {
            self.pending = true;
            return;
        }
        self.pending = false;
        self.scan.neutral(out);
        if self.scan.region_lost {
            out.extend(format!("\x1b[{};{}r", self.scan.top, self.scan.bottom).as_bytes());
            self.scan.region_lost = false;
        }
        self.scan.damaged = false;
        let rows = self.rows;
        out.extend(format!("\x1b[{rows};1H\x1b[0m\x1b[7m").as_bytes());
        out.extend(fit(&self.status_line(), self.cols as usize).as_bytes());
        out.extend(b"\x1b[0m");
        self.draw_overlay(out);
        self.put_cursor(out);
        self.scan.restore(out);
    }

    fn status_line(&self) -> String {
        let mut items = self.status.items.clone();
        match self.pending_approvals().count() {
            0 => {}
            1 => items.push("1 approval (ctrl-\\ a)".into()),
            n => items.push(format!("{n} approvals (ctrl-\\ a)")),
        }
        format!(" {}", items.join(" · "))
    }

    /// Puts the terminal's cursor and attributes where the session has
    /// them.
    fn put_cursor(&self, out: &mut Vec<u8>) {
        let screen = self.screen.screen();
        out.extend(screen.cursor_state_formatted());
        out.extend(screen.attributes_formatted());
    }

    /// Repaints the cells under the overlay from the session's screen.
    fn erase_overlay(&mut self, out: &mut Vec<u8>) {
        let Some((top, left, height, w)) = self.overlay_at.take() else { return };
        let screen = self.screen.screen();
        let rows: Vec<Vec<u8>> = screen.rows_formatted(left - 1, w).collect();
        self.scan.neutral(out);
        for r in top..top + height {
            out.extend(format!("\x1b[{r};{left}H\x1b[0m\x1b[{w}X").as_bytes());
            if let Some(row) = rows.get(usize::from(r - 1)) {
                out.extend(row);
            }
        }
        out.extend(b"\x1b[0m");
    }

    fn draw_overlay(&mut self, out: &mut Vec<u8>) {
        let Some(a) = self.shown.as_ref().and_then(|id| self.status.approvals.iter().find(|a| &a.id == id))
        else {
            self.shown = None;
            return;
        };
        let w = self.cols.saturating_sub(4).min(76);
        let inner = usize::from(w.saturating_sub(4));
        let mut lines: Vec<String> = wrap(&a.summary, inner).into_iter().take(6).collect();
        if !a.detail.is_empty() {
            lines.push(String::new());
            lines.extend(wrap(&a.detail, inner).into_iter().take(4));
        }
        lines.push(String::new());
        lines.push("y approve · n deny · esc later".into());
        let room = usize::from(self.limit().saturating_sub(2));
        if lines.len() > room {
            lines.drain(..lines.len() - room);
        }
        let height = lines.len() as u16 + 2;
        let top = self.limit() + 1 - height;
        let left = (self.cols - w) / 2 + 1;
        let count = self.pending_approvals().count();
        let index = self.pending_approvals().position(|p| p.id == a.id).unwrap_or(0) + 1;
        let title = format!(" {} ", a.kind);
        let tail = if count > 1 { format!(" {index}/{count} ") } else { String::new() };
        let fill = usize::from(w - 2).saturating_sub(width(&title) + width(&tail) + 1);
        let mut rows = vec![format!("┌─{title}{}{tail}┐", "─".repeat(fill))];
        rows.extend(lines.iter().map(|l| format!("│ {} │", fit(l, inner))));
        rows.push(format!("└{}┘", "─".repeat(usize::from(w - 2))));
        for (i, row) in rows.iter().enumerate() {
            let r = top + i as u16;
            out.extend(format!("\x1b[{r};{left}H\x1b[0m").as_bytes());
            if i == 0 {
                out.extend(b"\x1b[1m");
            }
            out.extend(row.as_bytes());
            out.extend(b"\x1b[0m");
        }
        self.overlay_at = Some((top, left, height, w));
    }
}

/// A key the overlay understands.
#[derive(Debug, PartialEq, Eq)]
enum Key {
    Char(char),
    Escape,
}

/// The first key in `input`: a plain character, Escape alone, or the same
/// in the kitty keyboard protocol (`CSI code [; mods] u`).
fn key(input: &[u8]) -> Option<Key> {
    if input == b"\x1b" {
        return Some(Key::Escape);
    }
    if let Some(rest) = input.strip_prefix(b"\x1b[") {
        let end = rest.iter().position(|b| (0x40..=0x7e).contains(b))?;
        if rest[end] != b'u' {
            return None;
        }
        let code: u32 = std::str::from_utf8(&rest[..end]).ok()?.split(';').next()?.parse().ok()?;
        return match code {
            27 => Some(Key::Escape),
            c => char::from_u32(c).map(Key::Char),
        };
    }
    std::str::from_utf8(input).ok()?.chars().next().filter(|c| !c.is_control()).map(Key::Char)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A terminal that shows what the compositor writes.
    struct Terminal {
        vt: vt100::Parser,
        comp: Compositor,
    }

    impl Terminal {
        fn new(rows: u16, cols: u16) -> Terminal {
            let mut t = Terminal { vt: vt100::Parser::new(rows, cols, 0), comp: Compositor::new(rows, cols) };
            let start = t.comp.start(Some((1, 1)));
            t.vt.process(&start);
            t
        }

        fn session(&mut self, bytes: &[u8]) {
            let out = self.comp.output(bytes);
            self.vt.process(&out);
        }

        fn apply(&mut self, out: Vec<u8>) {
            self.vt.process(&out);
        }

        fn row(&self, r: u16) -> String {
            let cols = self.vt.screen().size().1;
            self.vt.screen().contents_between(r, 0, r, cols).trim_end().to_string()
        }

        /// The terminal shows the session's screen above the status line,
        /// with the cursor where the session has it.
        fn matches_session(&self) {
            let (rows, cols) = self.comp.session_size();
            for r in 0..rows {
                let want = self.comp.screen.screen().contents_between(r, 0, r, cols);
                assert_eq!(self.row(r), want.trim_end(), "row {r}");
            }
            assert_eq!(self.vt.screen().cursor_position(), self.comp.screen.screen().cursor_position());
        }
    }

    fn status(approvals: &[(&str, &str)]) -> Status {
        Status {
            items: vec!["m1".into(), "dev/arch".into()],
            approvals: approvals
                .iter()
                .map(|(id, summary)| Approval {
                    id: id.to_string(),
                    kind: "git.push".into(),
                    summary: summary.to_string(),
                    detail: String::new(),
                })
                .collect(),
        }
    }

    #[test]
    fn output_scrolls_above_the_status_line() {
        let mut t = Terminal::new(6, 30);
        let out = t.comp.set_status(status(&[]));
        t.apply(out);
        for i in 0..20 {
            t.session(format!("line {i}\r\n").as_bytes());
        }
        t.matches_session();
        assert_eq!(t.row(5), " m1 · dev/arch");
        assert_eq!(t.row(3), "line 19");
    }

    #[test]
    fn the_session_cannot_reach_the_status_line() {
        let mut t = Terminal::new(6, 30);
        let out = t.comp.set_status(status(&[]));
        t.apply(out);
        // A full-screen program: its own region, a clear, rows past its end.
        t.session(b"\x1b[?1049h\x1b[r\x1b[2J\x1b[99;1Hbottom\x1b[1;99rx\x1b[H\x1b[Jtop");
        t.matches_session();
        assert_eq!(t.row(5), " m1 · dev/arch");
        t.session(b"\x1b[?1049l");
        t.matches_session();
        assert_eq!(t.row(5), " m1 · dev/arch");
    }

    #[test]
    fn sequences_split_across_chunks_are_rewritten() {
        let mut t = Terminal::new(6, 30);
        for chunk in [&b"\x1b"[..], b"[9", b"9;3H", b"x"] {
            t.session(chunk);
        }
        t.matches_session();
        assert_eq!(t.vt.screen().cursor_position(), (4, 3));
    }

    #[test]
    fn approvals_open_as_overlays_and_leave_no_trace() {
        let mut t = Terminal::new(12, 40);
        for i in 0..11 {
            t.session(format!("\x1b[3{}mline {i}\x1b[0m\r\n", i % 7).as_bytes());
        }
        t.session(b"\x1b[1mbold");
        let out = t.comp.set_status(status(&[("a1", "git push main to origin")]));
        t.apply(out);
        assert!(t.comp.overlay_open());
        let screen: String = (0..11).map(|r| t.row(r) + "\n").collect();
        assert!(screen.contains("git push main to origin"), "{screen}");
        assert!(screen.contains("y approve"), "{screen}");
        assert!(t.row(11).contains("1 approval"));
        // Output goes on under the overlay.
        t.session(b"\r\nmore\r\n");
        let (out, decision) = t.comp.key(b"y");
        t.apply(out);
        assert_eq!(decision, Some(("a1".into(), true)));
        assert!(!t.comp.overlay_open());
        t.matches_session();
        // Attributes are the session's again.
        t.session(b"x");
        let (row, col) = t.vt.screen().cursor_position();
        assert!(t.vt.screen().cell(row, col - 1).unwrap().bold());
    }

    #[test]
    fn a_dismissed_approval_waits_for_the_key() {
        let mut t = Terminal::new(12, 40);
        let out = t.comp.set_status(status(&[("a1", "one")]));
        t.apply(out);
        let (out, decision) = t.comp.key(b"\x1b");
        t.apply(out);
        assert_eq!(decision, None);
        assert!(!t.comp.overlay_open());
        // The same approval does not open again by itself; a new one does.
        let out = t.comp.set_status(status(&[("a1", "one")]));
        t.apply(out);
        assert!(!t.comp.overlay_open());
        let out = t.comp.open_approvals();
        t.apply(out);
        assert!(t.comp.overlay_open());
        let (out, decision) = t.comp.key(b"\x1b[110;1u");
        t.apply(out);
        assert_eq!(decision, Some(("a1".into(), false)));
        t.matches_session();
    }

    #[test]
    fn resizing_keeps_the_status_line() {
        let mut t = Terminal::new(8, 30);
        let out = t.comp.set_status(status(&[]));
        t.apply(out);
        t.session(b"hello");
        t.vt.screen_mut().set_size(10, 40);
        let out = t.comp.resize(10, 40);
        t.apply(out);
        assert_eq!(t.comp.session_size(), (9, 40));
        assert_eq!(t.row(9), " m1 · dev/arch");
    }

    #[test]
    fn finishing_gives_the_whole_terminal_back() {
        let mut t = Terminal::new(6, 30);
        let out = t.comp.set_status(status(&[]));
        t.apply(out);
        t.session(b"a\r\nb");
        let out = t.comp.finish(b"\x1b[0m");
        t.apply(out);
        assert_eq!(t.row(5), "");
        for _ in 0..10 {
            t.vt.process(b"\r\nx");
        }
        assert_eq!(t.row(5), "x", "the last row scrolls again");
    }

    #[test]
    fn keys_in_both_encodings() {
        assert_eq!(key(b"y"), Some(Key::Char('y')));
        assert_eq!(key(b"\x1b"), Some(Key::Escape));
        assert_eq!(key(b"\x1b[27u"), Some(Key::Escape));
        assert_eq!(key(b"\x1b[121;1u"), Some(Key::Char('y')));
        assert_eq!(key(b"\x1b[A"), None);
    }

    #[test]
    fn text_wraps_at_spaces() {
        assert_eq!(wrap("git push main to origin", 10), ["git push", "main to", "origin"]);
        assert_eq!(wrap("abcdefghijkl", 5), ["abcde", "fghij", "kl"]);
        assert_eq!(fit("ab", 4), "ab  ");
        assert_eq!(fit("日本語", 4), "日本");
    }
}
