package runtime

// Foreground passthrough: an interactive foreground tool runs with raw stdio
// passthrough between the host terminal and the container PTY, so the tool talks to the
// real terminal directly and every capability query is answered by the real terminal —
// full fidelity, nothing for us to emulate. A passive shadow terminal emulator records
// the tool's output (and only that), so the host can repaint the exact screen after an
// approval modal. The shadow never answers the tool's queries and never sees its input.
//
// While it runs, the foreground registers itself as the session's approval prompter:
// PromptApproval takes over the screen with a modal — composited (via lipgloss) over
// the shadow's current screen so the tool stays visible behind it — and blocks for the
// user's decision. The modal looks and behaves the same whether the tool is on the
// alternate screen or the main screen; only the restore differs: over an alt-screen
// tool we repaint from the shadow, while over a main-screen tool we use our own alt
// screen (so the terminal preserves the tool's scrollback) and replay any output held
// during the modal on dismissal.
//
// Set TOBY_FG_LOG=/path/to/log to trace the lifecycle.

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/vt"
	"github.com/moby/moby/client"
	"golang.org/x/term"

	sandboxapi "petris.dev/toby/sandbox"
)

// modalOptions are the approval choices, in selection order (index 0 approves).
var modalOptions = []string{"Approve", "Deny"}

// denySelection is the index of Deny — the safe option highlighted by default, so a
// stray Enter (or any unexpected close) denies.
const denySelection = 1

// runExecForeground drives an interactive foreground exec with raw passthrough plus a
// shadow emulator for the approval modal. The host terminal is put in raw mode; SIGWINCH
// resizes the PTY and the shadow; the tool's output stream ending means it has exited.
// While it runs, the foreground is registered as the approval prompter.
func runExecForeground(ctx context.Context, resize func(cols, rows int), attach client.HijackedResponse, register func(sandboxapi.ApprovalPrompter)) {
	closeLog := initFGLog()
	defer closeLog()

	if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
	}

	cols, rows := hostTerminalSize()
	fgLogf("start cols=%d rows=%d", cols, rows)

	f := &foreground{
		shadow: vt.NewSafeEmulator(cols, rows),
		conn:   attach.Conn,
		out:    os.Stdout,
		width:  cols,
		height: rows,
	}
	defer f.shadow.Close()

	if register != nil {
		register(f)
		defer register(nil)
	}

	resize(cols, rows)

	// The shadow generates replies to the tool's queries internally; drain and discard
	// them so its write side never blocks (the real terminal provides the real answers
	// to the tool via passthrough).
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := f.shadow.Read(buf); err != nil {
				return
			}
		}
	}()

	// Terminal resizes: resize the PTY and the shadow, and re-render an open modal.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			w, h, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil || w <= 0 || h <= 0 {
				continue
			}
			resize(w, h)
			f.resize(w, h)
		}
	}()

	// Host input → tool (raw) during passthrough; drives the modal while it's up. This
	// goroutine blocks on stdin and is reaped when the process exits.
	go f.pumpInput()

	// Tool output → terminal (and shadow); blocks until the tool's stream ends.
	f.pumpOutput(attach.Reader)
	fgLogf("tool exited")
}

// foreground coordinates raw passthrough and the approval modal. mu guards writes to the
// host terminal together with the modal state and dimensions.
type foreground struct {
	shadow *vt.SafeEmulator
	conn   io.Writer // tool stdin
	out    io.Writer // host stdout

	mu       sync.Mutex
	modal    bool
	modalAlt bool                       // modal is on our own alt screen (tool was on the main screen)
	req      sandboxapi.ApprovalRequest // the request the modal is deciding
	result   chan bool                  // receives the user's decision (allow)
	selected int                        // highlighted option while the modal is up
	altBuf   []byte                     // tool output held while our alt modal is up, replayed on dismiss

	width, height int
}

var _ sandboxapi.ApprovalPrompter = (*foreground)(nil)

// PromptApproval shows the approval modal for req and blocks until the user decides (or
// the context is cancelled, which denies). Only one prompt may be active at a time.
func (f *foreground) PromptApproval(ctx context.Context, req sandboxapi.ApprovalRequest) (bool, error) {
	result := make(chan bool, 1)

	f.mu.Lock()
	if f.modal {
		f.mu.Unlock()
		return false, fmt.Errorf("an approval prompt is already active")
	}
	f.modal = true
	f.selected = denySelection // default to the safe choice
	f.req = req
	f.result = result
	if f.shadow.IsAltScreen() {
		fgLogf("modal shown (overlay): %s", req.Action)
	} else {
		// Tool is on the main screen; take our own alt screen so the modal doesn't
		// clobber its scrollback — the terminal restores the main screen on exit.
		f.modalAlt = true
		_, _ = io.WriteString(f.out, "\x1b[?1049h")
		fgLogf("modal shown (alt screen): %s", req.Action)
	}
	f.renderModalLocked()
	f.mu.Unlock()

	select {
	case allow := <-result:
		return allow, nil
	case <-ctx.Done():
		f.mu.Lock()
		if f.modal && f.result == result {
			f.result = nil
			f.dismissLocked()
		}
		f.mu.Unlock()
		return false, ctx.Err()
	}
}

func (f *foreground) pumpOutput(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			f.onOutput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (f *foreground) onOutput(data []byte) {
	_, _ = f.shadow.Write(data) // record (drained by the discard goroutine)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.modal {
		// The modal owns the screen; record output (and hold it for replay when we used
		// our own alt screen) rather than drawing over the modal.
		if f.modalAlt {
			f.altBuf = append(f.altBuf, data...)
		}
		return
	}
	_, _ = f.out.Write(data) // passthrough to the real terminal
}

func (f *foreground) pumpInput() {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			f.onInput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (f *foreground) onInput(data []byte) {
	f.mu.Lock()
	if f.modal {
		sel, decided, allow := decisionForKey(data, f.selected)
		if decided {
			result := f.result
			f.result = nil
			f.dismissLocked()
			f.mu.Unlock()
			if result != nil {
				result <- allow
			}
			fgLogf("modal decision: allow=%v", allow)
			return
		}
		if sel != f.selected {
			f.selected = sel
			f.renderModalLocked()
		}
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()

	_, _ = f.conn.Write(data) // raw passthrough to the tool
}

// dismissLocked restores the screen the modal took over.
func (f *foreground) dismissLocked() {
	f.modal = false
	f.req = sandboxapi.ApprovalRequest{}
	if f.modalAlt {
		f.modalAlt = false
		// Leave our alt screen — the terminal restores the tool's main screen and
		// scrollback — then replay any output held during the modal.
		_, _ = io.WriteString(f.out, "\x1b[?25h\x1b[?1049l")
		if len(f.altBuf) > 0 {
			_, _ = f.out.Write(f.altBuf)
			f.altBuf = nil
		}
		return
	}
	f.repaintLocked() // repaint from the shadow, removing the modal
}

func (f *foreground) resize(w, h int) {
	f.shadow.Resize(w, h)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.width, f.height = w, h
	if f.modal {
		f.renderModalLocked()
	}
}

// renderModalLocked blacks out the whole screen and draws the modal centered on it, so
// it reads as its own screen rather than an overlay tinted by the tool's colors. The
// underlying tool screen is restored from the shadow (or our alt screen) on dismiss.
func (f *foreground) renderModalLocked() {
	box := renderModalBox(f.req, f.selected)
	boxLines := strings.Split(box, "\n")
	bw, bh := lipgloss.Width(box), len(boxLines)
	x := max((f.width-bw)/2, 0)
	y := max((f.height-bh)/2, 0)

	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[?25l") // begin synchronized update, hide cursor
	for i := 0; i < f.height; i++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[40m\x1b[K\x1b[0m", i+1) // black out the whole line
		if i >= y && i-y < bh {
			fmt.Fprintf(&b, "\x1b[%d;%dH%s", i+1, x+1, boxLines[i-y]) // draw the box on top
		}
	}
	b.WriteString("\x1b[?2026l")
	_, _ = io.WriteString(f.out, b.String())
}

// repaintLocked redraws the whole screen from the shadow and restores the cursor — used
// to remove the modal when overlaying an alt-screen tool.
func (f *foreground) repaintLocked() {
	pos := f.shadow.CursorPosition()
	f.paintLocked(f.shadow.Render())
	_, _ = io.WriteString(f.out, fmt.Sprintf("\x1b[%d;%dH\x1b[?25h", pos.Y+1, pos.X+1))
}

// paintLocked writes a full-screen frame line by line at absolute positions (so it is
// correct in raw mode), tear-free via synchronized-update mode, with the cursor hidden.
func (f *foreground) paintLocked(frame string) {
	lines := strings.Split(frame, "\n")
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[?25l") // begin synchronized update, hide cursor
	for i := 0; i < f.height; i++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[K", i+1) // position, clear line
		if i < len(lines) {
			b.WriteString(lines[i])
		}
	}
	b.WriteString("\x1b[?2026l")
	_, _ = io.WriteString(f.out, b.String())
}

// decisionForKey maps a raw input chunk to a new selection and, when the user confirms,
// the decision (allow). decided is false when the selection only moved.
func decisionForKey(data []byte, selected int) (sel int, decided, allow bool) {
	switch string(data) {
	case "\x1b[C", "\x1b[B", "\t": // right, down, tab
		return (selected + 1) % len(modalOptions), false, false
	case "\x1b[D", "\x1b[A", "\x1b[Z": // left, up, shift-tab
		return (selected - 1 + len(modalOptions)) % len(modalOptions), false, false
	case "\r", "\n", " ": // confirm current selection (index 0 approves)
		return selected, true, selected == 0
	case "a": // approve
		return 0, true, true
	case "d", "\x1b": // deny, or esc to dismiss
		return 1, true, false
	}
	return selected, false, false
}

// renderModalBox renders the approval modal for req with the given option highlighted.
// Everything sits on a black background so it blends into the blacked-out screen; only
// the border, the text, and the selected button carry colour.
func renderModalBox(req sandboxapi.ApprovalRequest, selected int) string {
	black := lipgloss.Color("0")
	on := func(s lipgloss.Style) lipgloss.Style { return s.Background(black) }

	title := on(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))).Render("Permission request")
	name := on(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))).Render(req.Name)
	desc := on(lipgloss.NewStyle().Foreground(lipgloss.Color("252"))).Render(req.Message)

	button := lipgloss.NewStyle().Padding(0, 2).Margin(0, 1).Foreground(lipgloss.Color("252")).Background(black)
	selectedButton := button.Foreground(lipgloss.Color("231")).Background(lipgloss.Color("63")).Bold(true)
	buttons := make([]string, len(modalOptions))
	for i, opt := range modalOptions {
		style := button
		if i == selected {
			style = selectedButton
		}
		buttons[i] = style.Render(opt)
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, buttons...)
	hint := on(lipgloss.NewStyle().Foreground(lipgloss.Color("245"))).Render("←/→ move · enter confirm · a approve · d deny")

	body := lipgloss.JoinVertical(lipgloss.Center, title, "", name, desc, "", row, "", hint)
	return lipgloss.NewStyle().
		Padding(1, 4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Background(black).
		Render(body)
}

// hostTerminalSize reads the host terminal size, falling back to the seeded default when
// it cannot be measured.
func hostTerminalSize() (cols, rows int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return defaultCols, defaultRows
	}
	return w, h
}

// --- temporary diagnostics ---------------------------------------------------

var fgLogger *log.Logger

func initFGLog() func() {
	path := os.Getenv("TOBY_FG_LOG")
	if path == "" {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return func() {}
	}
	fgLogger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
	return func() {
		fgLogger = nil
		_ = f.Close()
	}
}

func fgLogf(format string, args ...any) {
	if fgLogger != nil {
		fgLogger.Printf(format, args...)
	}
}
