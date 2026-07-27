//go:build linux

package bwrap

// Drives managed host-terminal passthrough and the approval modal over a local
// PTY while a passive terminal shadow preserves the application's screen.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/vt"
	"github.com/muesli/cancelreader"

	"petris.dev/toby/internal/diagnostic"
	sandboxapi "petris.dev/toby/internal/sandbox"
)

const (
	maxHeldTerminalOutput   = 4 << 20
	maxTerminalModalName    = 128
	maxTerminalModalMessage = 1024
)

var terminalModalOptions = []string{"Approve", "Deny"}

const terminalDenySelection = 1

// terminalForeground coordinates raw passthrough, a passive terminal shadow,
// and at most one synchronous approval prompt.
type terminalForeground struct {
	shadow *vt.SafeEmulator
	input  cancelreader.CancelReader
	tool   io.Writer
	output io.Writer

	register         func(sandboxapi.ApprovalPrompter)
	suspend          func() (int, int, bool, error)
	resume           func() error
	suspendCharacter func() (byte, bool)

	mu          sync.Mutex
	closed      bool
	modal       bool
	modalAlt    bool
	repaintMain bool
	request     sandboxapi.ApprovalRequest
	result      chan terminalApprovalResult
	selected    int
	heldOutput  []byte
	width       int
	height      int

	failures   chan error
	inputDone  chan struct{}
	inputErr   error
	shadowStop chan struct{}
	shadowDone chan struct{}
	closeOnce  sync.Once
	closeErr   error
}

type terminalApprovalResult struct {
	allow bool
	err   error
}

var (
	_ io.Closer                   = (*terminalForeground)(nil)
	_ sandboxapi.ApprovalPrompter = (*terminalForeground)(nil)
)

func newTerminalForeground(
	input *os.File,
	output *os.File,
	tool io.Writer,
	register func(sandboxapi.ApprovalPrompter),
	suspend func() (int, int, bool, error),
	resume func() error,
	suspendCharacter func() (byte, bool),
	width, height int,
) (*terminalForeground, error) {
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return nil, fmt.Errorf("make terminal input cancelable: %w", err)
	}

	foreground := &terminalForeground{
		shadow:           vt.NewSafeEmulator(width, height),
		input:            reader,
		tool:             tool,
		output:           output,
		register:         register,
		suspend:          suspend,
		resume:           resume,
		suspendCharacter: suspendCharacter,
		width:            width,
		height:           height,
		failures:         make(chan error, 2),
		inputDone:        make(chan struct{}),
		shadowStop:       make(chan struct{}),
		shadowDone:       make(chan struct{}),
	}
	if register != nil {
		register(foreground)
	}

	go func() {
		defer close(foreground.shadowDone)
		foreground.discardShadowReplies()
	}()
	go func() {
		foreground.inputErr = foreground.pumpInput()
		close(foreground.inputDone)
		if foreground.inputErr != nil &&
			!errors.Is(foreground.inputErr, cancelreader.ErrCanceled) {
			foreground.reportFailure(foreground.inputErr)
		}
	}()

	return foreground, nil
}

// PumpOutput forwards PTY output until the sandbox closes its terminal.
func (f *terminalForeground) PumpOutput(reader io.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			if outputErr := f.onOutput(buffer[:count]); outputErr != nil {
				return outputErr
			}
		}
		if err != nil {
			return normalizeManagedPTYMasterReadError(err)
		}
	}
}

// Resize updates the passive shadow and repaints an active modal.
func (f *terminalForeground) Resize(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.shadow.Resize(width, height)
	f.width = width
	f.height = height
	var err error
	if f.modal {
		err = f.renderModalLocked()
	}
	f.mu.Unlock()
	f.reportFailure(err)
}

// PromptApproval takes over the host terminal until the user decides or the
// request context ends. Closing the foreground safely denies an active prompt.
func (f *terminalForeground) PromptApproval(
	ctx context.Context,
	request sandboxapi.ApprovalRequest,
) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("approval context is nil")
	}
	result := make(chan terminalApprovalResult, 1)

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return false, fmt.Errorf("managed terminal is closed")
	}
	if f.modal {
		f.mu.Unlock()
		return false, fmt.Errorf("an approval prompt is already active")
	}
	f.modal = true
	f.selected = terminalDenySelection
	f.request = request
	f.result = result
	if !f.shadow.IsAltScreen() {
		f.modalAlt = true
		if err := writeTerminalString(f.output, "\x1b[?1049h"); err != nil {
			f.result = nil
			f.modal = false
			f.modalAlt = false
			f.request = sandboxapi.ApprovalRequest{}
			f.mu.Unlock()
			return false, fmt.Errorf("enter approval screen: %w", err)
		}
	}
	if err := f.renderModalLocked(); err != nil {
		f.result = nil
		dismissErr := f.dismissLocked()
		f.mu.Unlock()
		return false, errors.Join(
			fmt.Errorf("render approval prompt: %w", err),
			dismissErr,
		)
	}
	f.mu.Unlock()

	select {
	case outcome := <-result:
		return outcome.allow, outcome.err
	case <-ctx.Done():
		f.mu.Lock()
		var dismissErr error
		if f.modal && f.result == result {
			f.result = nil
			dismissErr = f.dismissLocked()
		}
		f.mu.Unlock()
		return false, errors.Join(ctx.Err(), dismissErr)
	}
}

// Close cancels the input pump, restores any modal-owned screen, unregisters
// the prompter, and releases the shadow.
func (f *terminalForeground) Close() error {
	if f == nil {
		return nil
	}

	f.closeOnce.Do(func() {
		f.closeErr = f.close()
	})
	return f.closeErr
}

func (f *terminalForeground) close() error {
	f.mu.Lock()
	f.closed = true
	result := f.result
	f.result = nil
	var dismissErr error
	if f.modal {
		dismissErr = f.dismissLocked()
	}
	f.mu.Unlock()

	if result != nil {
		result <- terminalApprovalResult{allow: false}
	}
	if f.register != nil {
		f.register(nil)
	}

	f.input.Cancel()
	<-f.inputDone
	inputCloseErr := f.input.Close()
	inputErr := f.inputErr
	if errors.Is(inputErr, cancelreader.ErrCanceled) {
		inputErr = nil
	}

	close(f.shadowStop)
	_, shadowWakeErr := f.shadow.Write([]byte("\x1b[5n"))
	<-f.shadowDone
	shadowCloseErr := f.shadow.Close()

	diagnostic.DiscardError(
		"closing the canceled terminal input reader is cleanup",
		"close managed terminal input reader",
		inputCloseErr,
	)
	diagnostic.DiscardError(
		"waking the terminal shadow only completes its cleanup",
		"wake managed terminal shadow",
		shadowWakeErr,
	)
	diagnostic.DiscardError(
		"closing the terminal shadow follows required screen restoration",
		"close managed terminal shadow",
		shadowCloseErr,
	)

	return errors.Join(dismissErr, inputErr)
}

func (f *terminalForeground) discardShadowReplies() {
	buffer := make([]byte, 4096)
	for {
		if _, err := f.shadow.Read(buffer); err != nil {
			return
		}
		select {
		case <-f.shadowStop:
			return
		default:
		}
	}
}

func (f *terminalForeground) pumpInput() error {
	buffer := make([]byte, 4096)
	for {
		count, err := f.input.Read(buffer)
		if count > 0 {
			if inputErr := f.onInput(buffer[:count]); inputErr != nil {
				return inputErr
			}
		}
		if err != nil {
			if errors.Is(err, cancelreader.ErrCanceled) {
				return err
			}
			return fmt.Errorf("read host terminal input: %w", err)
		}
	}
}

func (f *terminalForeground) onOutput(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return io.ErrClosedPipe
	}
	if err := writeTerminalBytes(f.shadow, data); err != nil {
		return fmt.Errorf("update terminal shadow: %w", err)
	}
	if !f.modal {
		if err := writeTerminalBytes(f.output, data); err != nil {
			return fmt.Errorf("write host terminal output: %w", err)
		}
		return nil
	}
	if !f.modalAlt || f.repaintMain {
		return nil
	}
	if len(f.heldOutput)+len(data) > maxHeldTerminalOutput {
		f.heldOutput = nil
		f.repaintMain = true
		return nil
	}
	f.heldOutput = append(f.heldOutput, data...)
	return nil
}

func (f *terminalForeground) onInput(data []byte) error {
	for len(data) > 0 {
		suspend, enabled := byte(0), false
		if f.suspendCharacter != nil {
			suspend, enabled = f.suspendCharacter()
		}
		if !enabled {
			return f.onInputWithoutSuspend(data)
		}
		index := bytes.IndexByte(data, suspend)
		if index < 0 {
			return f.onInputWithoutSuspend(data)
		}
		if index > 0 {
			if err := f.onInputWithoutSuspend(data[:index]); err != nil {
				return err
			}
		}
		if f.suspend != nil {
			if err := f.suspendAndRepaint(); err != nil {
				return fmt.Errorf("suspend managed terminal: %w", err)
			}
		}
		data = data[index+1:]
	}
	return nil
}

func (f *terminalForeground) suspendAndRepaint() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return io.ErrClosedPipe
	}

	var prepareErr error
	if f.modal {
		if f.modalAlt {
			prepareErr = writeTerminalString(
				f.output,
				"\x1b[?25h\x1b[?1049l",
			)
		} else {
			prepareErr = f.repaintLocked()
		}
	}

	width, height, foreground, suspendErr := f.suspend()
	if foreground && width > 0 && height > 0 {
		f.shadow.Resize(width, height)
		f.width = width
		f.height = height
	}

	var repaintErr error
	if !foreground {
		repaintErr = nil
	} else if f.modal {
		if f.modalAlt {
			repaintErr = writeTerminalString(f.output, "\x1b[?1049h")
		}
		repaintErr = errors.Join(repaintErr, f.renderModalLocked())
	} else {
		repaintErr = f.repaintLocked()
	}
	var resumeErr error
	if f.resume != nil {
		resumeErr = f.resume()
	}

	return errors.Join(prepareErr, suspendErr, repaintErr, resumeErr)
}

func (f *terminalForeground) onInputWithoutSuspend(data []byte) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return io.ErrClosedPipe
	}
	if f.modal {
		selected, decided, allow := terminalDecisionForKey(
			data,
			f.selected,
		)
		if decided {
			result := f.result
			f.result = nil
			dismissErr := f.dismissLocked()
			f.mu.Unlock()
			if result != nil {
				result <- terminalApprovalResult{
					allow: allow,
					err:   dismissErr,
				}
			}
			return dismissErr
		}
		if selected != f.selected {
			f.selected = selected
			if err := f.renderModalLocked(); err != nil {
				f.mu.Unlock()
				return fmt.Errorf("render approval selection: %w", err)
			}
		}
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	if err := writeTerminalBytes(f.tool, data); err != nil {
		return fmt.Errorf("write managed PTY input: %w", err)
	}
	return nil
}

func (f *terminalForeground) dismissLocked() error {
	f.modal = false
	f.request = sandboxapi.ApprovalRequest{}
	if f.modalAlt {
		f.modalAlt = false
		var returnErr error
		if err := writeTerminalString(
			f.output,
			"\x1b[?25h\x1b[?1049l",
		); err != nil {
			returnErr = fmt.Errorf("leave approval screen: %w", err)
		}
		switch {
		case f.repaintMain:
			returnErr = errors.Join(returnErr, f.repaintLocked())
		case len(f.heldOutput) > 0:
			if err := writeTerminalBytes(f.output, f.heldOutput); err != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("flush held terminal output: %w", err),
				)
			}
		}
		f.heldOutput = nil
		f.repaintMain = false
		return returnErr
	}
	return f.repaintLocked()
}

func (f *terminalForeground) renderModalLocked() error {
	box := renderTerminalModal(f.request, f.selected)
	boxLines := strings.Split(box, "\n")
	boxWidth, boxHeight := lipgloss.Width(box), len(boxLines)
	x := max((f.width-boxWidth)/2, 0)
	y := max((f.height-boxHeight)/2, 0)

	var frame strings.Builder
	frame.WriteString("\x1b[?2026h\x1b[?25l")
	for row := 0; row < f.height; row++ {
		fmt.Fprintf(
			&frame,
			"\x1b[%d;1H\x1b[40m\x1b[K\x1b[0m",
			row+1,
		)
		if row >= y && row-y < boxHeight {
			fmt.Fprintf(
				&frame,
				"\x1b[%d;%dH%s",
				row+1,
				x+1,
				boxLines[row-y],
			)
		}
	}
	frame.WriteString("\x1b[?2026l")
	return writeTerminalString(f.output, frame.String())
}

func (f *terminalForeground) repaintLocked() error {
	position := f.shadow.CursorPosition()
	paintErr := f.paintLocked(f.shadow.Render())
	cursorErr := writeTerminalString(
		f.output,
		fmt.Sprintf(
			"\x1b[%d;%dH\x1b[?25h",
			position.Y+1,
			position.X+1,
		),
	)
	return errors.Join(paintErr, cursorErr)
}

func (f *terminalForeground) paintLocked(frame string) error {
	lines := strings.Split(frame, "\n")
	var output strings.Builder
	output.WriteString("\x1b[?2026h\x1b[?25l")
	for row := 0; row < f.height; row++ {
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[K", row+1)
		if row < len(lines) {
			output.WriteString(lines[row])
		}
	}
	output.WriteString("\x1b[?2026l")
	return writeTerminalString(f.output, output.String())
}

func (f *terminalForeground) Failures() <-chan error {
	if f == nil {
		return nil
	}
	return f.failures
}

func (f *terminalForeground) reportFailure(err error) {
	if err == nil {
		return
	}
	select {
	case f.failures <- err:
	default:
	}
}

func writeTerminalBytes(writer io.Writer, data []byte) error {
	count, err := writer.Write(data)
	if err != nil {
		return err
	}
	if count != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func writeTerminalString(writer io.Writer, data string) error {
	count, err := io.WriteString(writer, data)
	if err != nil {
		return err
	}
	if count != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func terminalDecisionForKey(
	data []byte,
	selected int,
) (selection int, decided bool, allow bool) {
	switch string(data) {
	case "\x1b[C", "\x1b[B", "\t":
		return (selected + 1) % len(terminalModalOptions), false, false
	case "\x1b[D", "\x1b[A", "\x1b[Z":
		return (selected - 1 + len(terminalModalOptions)) %
			len(terminalModalOptions), false, false
	case "\r", "\n", " ":
		return selected, true, selected == 0
	case "a":
		return 0, true, true
	case "d", "\x1b":
		return terminalDenySelection, true, false
	default:
		return selected, false, false
	}
}

func renderTerminalModal(
	request sandboxapi.ApprovalRequest,
	selected int,
) string {
	black := lipgloss.Color("0")
	onBlack := func(style lipgloss.Style) lipgloss.Style {
		return style.Background(black)
	}

	title := onBlack(
		lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("231")),
	).Render("Permission request")
	name := onBlack(
		lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("231")),
	).Render(sanitizeTerminalModalText(request.Name, maxTerminalModalName))
	description := onBlack(
		lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")),
	).Render(sanitizeTerminalModalText(
		request.Message,
		maxTerminalModalMessage,
	))

	button := lipgloss.NewStyle().
		Padding(0, 2).
		Margin(0, 1).
		Foreground(lipgloss.Color("252")).
		Background(black)
	selectedButton := button.
		Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("63")).
		Bold(true)
	buttons := make([]string, len(terminalModalOptions))
	for index, option := range terminalModalOptions {
		style := button
		if index == selected {
			style = selectedButton
		}
		buttons[index] = style.Render(option)
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, buttons...)
	hint := onBlack(
		lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")),
	).Render(
		"←/→ move · enter confirm · a approve · d deny",
	)

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		title,
		"",
		name,
		description,
		"",
		row,
		"",
		hint,
	)

	return lipgloss.NewStyle().
		Padding(1, 4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Background(black).
		Render(body)
}

func sanitizeTerminalModalText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}

	var sanitized strings.Builder
	count := 0
	for _, current := range text {
		if count == limit {
			break
		}
		if !unicode.IsPrint(current) {
			current = ' '
		}
		sanitized.WriteRune(current)
		count++
	}

	return sanitized.String()
}
