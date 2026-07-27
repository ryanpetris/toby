// Package status presents bounded startup progress without changing the
// foreground application's streams. Interactive launches use a clearable
// inline Bubble Tea view, debug and redirected launches retain raw output, and
// quiet launches discard all non-foreground output.
package status

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/exitcode"
)

const (
	defaultTranscriptLimit = 1 << 20
	startupFailureMessage  = "Toby startup failed. Re-run with --debug for details."
	plainProgressInterval  = 5 * time.Second
)

// Options selects the startup presentation for one launch.
type Options struct {
	Debug bool
	Quiet bool
}

// Service serializes startup progress, owns the optional interactive renderer,
// and creates startup-only output sinks. The original foreground streams never
// pass through this type.
type Service struct {
	mu sync.Mutex

	diagnostics *diagnostic.Service
	logger      *diagnostic.Logger
	out         io.Writer
	terminal    bool
	structured  bool
	captureAll  bool
	mode        Mode
	debug       bool
	started     bool
	handedOff   bool
	nextID      uint64
	nextOrder   uint64
	active      OperationID

	program    *tea.Program
	programEnd chan error
	renderErr  error

	transcriptLimit int
	pendingBytes    int
	operations      map[OperationID]*operationState
	writers         map[*streamWriter]struct{}
}

// NewService builds the process-wide startup presentation over stderr.
func NewService(diagnostics *diagnostic.Service) *Service {
	return newServiceWithStderr(diagnostics, os.Stderr)
}

func newServiceWithStderr(
	diagnostics *diagnostic.Service,
	stderr *os.File,
) *Service {
	if diagnostics == nil {
		return newService(
			io.Discard,
			false,
			false,
			defaultTranscriptLimit,
		)
	}

	service := newService(
		stderr,
		stderr != nil && term.IsTerminal(int(stderr.Fd())),
		false,
		defaultTranscriptLimit,
	)
	service.diagnostics = diagnostics
	service.logger = diagnostics.Logger("status")
	service.structured = diagnostics.Format() == diagnostic.FormatJSON

	return service
}

func newService(
	out io.Writer,
	terminal bool,
	captureAll bool,
	transcriptLimit int,
) *Service {
	if out == nil {
		out = io.Discard
	}
	transcriptLimit = newBoundedTranscript(transcriptLimit).limit

	return &Service{
		out:             out,
		terminal:        terminal,
		captureAll:      captureAll,
		transcriptLimit: transcriptLimit,
		operations:      make(map[OperationID]*operationState),
		writers:         make(map[*streamWriter]struct{}),
	}
}

// Begin fixes the output mode before the first startup operation.
func (s *Service) Begin(options Options) error {
	if options.Debug && options.Quiet {
		return fmt.Errorf("debug and quiet startup output are mutually exclusive")
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("startup presentation is already active")
	}

	switch {
	case options.Quiet:
		s.mode = ModeQuiet
	case s.structured:
		s.mode = ModePlain
	case options.Debug && s.terminal:
		s.mode = ModeDebugTTY
	case options.Debug || !s.terminal:
		s.mode = ModePlain
	default:
		s.mode = ModeInteractive
	}
	s.started = true
	s.debug = options.Debug
	s.handedOff = false
	s.active = ""
	s.renderErr = nil
	s.pendingBytes = 0
	clear(s.operations)
	s.mu.Unlock()

	if options.Quiet && s.diagnostics != nil {
		s.diagnostics.BeginQuiet()
	}

	return nil
}

// StartOperation begins one launch-local startup operation.
func (s *Service) StartOperation(label string) *Operation {
	return s.startOperation("", label)
}

// StartScopedOperation begins one launch-local startup operation whose labels
// are presented beneath a stable scope.
func (s *Service) StartScopedOperation(
	scope string,
	label string,
) *Operation {
	return s.startOperation(scope, label)
}

func (s *Service) startChildOperation(
	parent OperationID,
	label string,
) *Operation {
	s.flushActiveWriters()

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.operations[parent]
	if state == nil || !state.running {
		return nil
	}

	s.nextID++
	id := OperationID(fmt.Sprintf("local-%d", s.nextID))
	s.startOperationLocked(id, parent, state.scope, label)

	return &Operation{service: s, id: id}
}

func (s *Service) startOperation(
	scope string,
	label string,
) *Operation {
	s.flushActiveWriters()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := OperationID(fmt.Sprintf("local-%d", s.nextID))
	s.startOperationLocked(id, "", scope, label)

	return &Operation{service: s, id: id}
}

func (s *Service) startOperationLocked(
	id OperationID,
	parent OperationID,
	scope string,
	label string,
) {
	scope = strings.TrimSpace(scope)
	label = normalizeOperationLabel(label)
	if !s.started ||
		(s.mode == ModeInteractive && s.handedOff) {
		return
	}

	s.nextOrder++
	for candidate, state := range s.operations {
		if !state.running {
			s.removeOperationLocked(candidate)
		}
	}
	state := &operationState{
		id:         id,
		parent:     parent,
		scope:      scope,
		label:      label,
		order:      s.nextOrder,
		running:    true,
		transcript: newBoundedTranscript(s.transcriptLimit),
	}
	s.operations[id] = state
	s.active = id

	switch s.mode {
	case ModePlain:
		s.writeLineLocked(operationLabel(scope, label) + "...")
	case ModeDebugTTY:
		if s.program == nil {
			s.writeLineLocked(operationLabel(scope, label) + "...")
		} else {
			s.sendModelLocked()
		}
	case ModeInteractive:
		s.sendModelLocked()
	}
}

func operationLabel(scope, label string) string {
	if scope == "" {
		return label
	}
	return scope + ": " + label
}

func (s *Service) writeLineLocked(line string) {
	if s.structured {
		s.logger.Info(line)
		return
	}
	_, err := fmt.Fprintln(s.out, line)
	diagnostic.DiscardError(
		"startup status presentation cannot interrupt launch",
		"write startup status line",
		err,
		"line", line,
	)
}

func normalizeOperationLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "Working"
	}
	return label
}

func (s *Service) setOperationLabel(id OperationID, label string) {
	label = normalizeOperationLabel(label)

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.operations[id]
	if state == nil || !state.running || state.label == label {
		return
	}
	state.label = label

	switch s.mode {
	case ModePlain:
		s.writeLineLocked(operationLabel(state.scope, label) + "...")
	case ModeDebugTTY:
		if s.program == nil {
			s.writeLineLocked(operationLabel(state.scope, label) + "...")
		} else if !s.handedOff {
			s.sendModelLocked()
		}
	case ModeInteractive:
		if !s.handedOff {
			s.sendModelLocked()
		}
	}
}

func (s *Service) setOperationProgress(
	id OperationID,
	progress Progress,
) {
	if progress.CompletedBytes < 0 {
		progress.CompletedBytes = 0
	}
	if progress.TotalBytes < 0 {
		progress.TotalBytes = 0
	}
	if progress.TotalBytes != 0 &&
		progress.CompletedBytes > progress.TotalBytes {
		progress.CompletedBytes = progress.TotalBytes
	}
	if progress.CompletedItems < 0 {
		progress.CompletedItems = 0
	}
	if progress.TotalItems < 0 {
		progress.TotalItems = 0
	}
	if progress.TotalItems != 0 &&
		progress.CompletedItems > progress.TotalItems {
		progress.CompletedItems = progress.TotalItems
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.operations[id]
	if state == nil || !state.running {
		return
	}
	snapshot := progress
	state.progress = &snapshot

	switch s.mode {
	case ModePlain:
		now := time.Now()
		complete := progress.TotalBytes != 0 &&
			progress.CompletedBytes == progress.TotalBytes
		if !complete &&
			!state.lastPlainProgress.IsZero() &&
			now.Sub(state.lastPlainProgress) < plainProgressInterval {
			return
		}
		if !complete && state.lastPlainProgress.IsZero() {
			state.lastPlainProgress = now
			return
		}
		state.lastPlainProgress = now
		s.writeLineLocked(
			plainProgressLine(
				operationLabel(state.scope, state.label),
				progress,
			),
		)
	case ModeDebugTTY:
		if !s.handedOff {
			s.sendModelLocked()
		}
	case ModeInteractive:
		if !s.handedOff {
			s.sendModelLocked()
		}
	}
}

func plainProgressLine(label string, progress Progress) string {
	if progress.TotalBytes == 0 {
		return label
	}

	percent := float64(progress.CompletedBytes) /
		float64(progress.TotalBytes) * 100
	return fmt.Sprintf(
		"%s: %.0f%% %s",
		label,
		percent,
		formatBytePair(
			progress.CompletedBytes,
			progress.TotalBytes,
		),
	)
}

func (s *Service) operationWriter(
	operation OperationID,
	destination io.Writer,
) io.Writer {
	if destination == nil {
		destination = io.Discard
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.mode {
	case ModeQuiet:
		return io.Discard
	case ModeInteractive:
		if s.handedOff {
			return io.Discard
		}
		if !s.captureDestination(destination) {
			return destination
		}
		writer := &streamWriter{
			service:   s,
			operation: operation,
		}
		s.writers[writer] = struct{}{}
		return writer
	case ModeDebugTTY:
		return destination
	default:
		return destination
	}
}

// RevealsHiddenOutput reports whether normally hidden lifecycle probes should
// write their output. Only debug mode opts into this diagnostic behavior.
func (s *Service) RevealsHiddenOutput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.started && s.debug
}

// Handoff clears startup presentation before the foreground application takes
// ownership of its original terminal streams.
func (s *Service) Handoff() error {
	s.closeWriters()

	s.mu.Lock()
	err := s.stopProgramLocked()
	err = errors.Join(err, s.renderErr)
	if err != nil {
		s.logger.DebugError("stop startup presentation", err)
	}
	s.handedOff = true
	s.active = ""
	s.pendingBytes = 0
	clear(s.operations)
	s.mu.Unlock()

	if s.diagnostics != nil {
		s.diagnostics.BeginForeground()
	}

	return nil
}

// Finish stops any remaining presentation and returns the error the CLI should
// report. Interactive startup failures conceal captured details behind a
// generic diagnostic; debug, redirected, quiet, and post-handoff failures
// retain the original error.
func (s *Service) Finish(launchErr error) error {
	s.closeWriters()

	s.mu.Lock()
	conceal := launchErr != nil &&
		s.mode == ModeInteractive &&
		!s.handedOff
	presentationErr := s.stopProgramLocked()
	s.started = false
	presentationErr = errors.Join(presentationErr, s.renderErr)
	if presentationErr != nil {
		s.logger.DebugError(
			"stop startup presentation",
			presentationErr,
		)
	}
	s.active = ""
	s.pendingBytes = 0
	clear(s.operations)
	s.mu.Unlock()

	if launchErr == nil {
		return nil
	}
	if conceal {
		return exitcode.New(
			exitcode.FromError(launchErr),
			"%s",
			startupFailureMessage,
		)
	}

	return launchErr
}

func (s *Service) appendOutput(
	operation OperationID,
	data []byte,
) {
	if len(data) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started ||
		s.mode != ModeInteractive ||
		s.handedOff {
		return
	}

	state := s.operations[operation]
	if state == nil {
		return
	}
	state.transcript.Append(data)
	s.enforceTranscriptLimitLocked()
	if s.active == operation {
		s.sendModelLocked()
	}
}

func (s *Service) finishOperation(
	id OperationID,
	failed bool,
	terminalLabel string,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.operations[id]
	if state == nil || !state.running {
		return
	}
	state.running = false
	state.failed = failed

	if terminalLabel != "" {
		label := normalizeOperationLabel(terminalLabel)
		switch s.mode {
		case ModePlain:
			suffix := ": done"
			if failed {
				suffix = ": failed"
			}
			s.writeLineLocked(operationLabel(state.scope, label) + suffix)
		case ModeDebugTTY:
			if s.program == nil {
				suffix := ": done"
				if failed {
					suffix = ": failed"
				}
				s.writeLineLocked(
					operationLabel(state.scope, label) + suffix,
				)
			}
		}
		state.label = label
	}

	if s.mode == ModeDebugTTY {
		s.removeOperationLocked(id)
		if s.program == nil {
			return
		}
		if s.hasRunningProgressLocked() {
			s.sendModelLocked()
			return
		}
		if err := s.stopProgramLocked(); err != nil {
			s.renderErr = errors.Join(s.renderErr, err)
		}
		return
	}
	if s.mode != ModeInteractive || s.handedOff {
		s.removeOperationLocked(id)
		return
	}
	if s.active != id {
		s.removeOperationLocked(id)
		s.sendModelLocked()
		return
	}

	s.reparentOperationChildrenLocked(id)
	if replacement := s.newestVisibleRunningOperationLocked(); replacement != nil {
		delete(s.operations, id)
		s.active = replacement.id
	}
	s.sendModelLocked()
}

func (s *Service) removeOperationLocked(id OperationID) {
	s.reparentOperationChildrenLocked(id)
	delete(s.operations, id)
}

func (s *Service) reparentOperationChildrenLocked(id OperationID) {
	state := s.operations[id]
	if state == nil {
		return
	}
	for _, child := range s.operations {
		if child.parent == id {
			child.parent = state.parent
		}
	}
}

func (s *Service) hasRunningProgressLocked() bool {
	for _, state := range s.operations {
		if state.running && state.progress != nil {
			return true
		}
	}

	return false
}

func (s *Service) newestVisibleRunningOperationLocked() *operationState {
	var result *operationState
	for _, state := range s.operations {
		if !state.running ||
			s.hasRunningChildLocked(state.id) ||
			(result != nil && result.order >= state.order) {
			continue
		}
		result = state
	}
	return result
}

func (s *Service) sendModelLocked() {
	if s.program == nil {
		if err := s.startProgramLocked(); err != nil {
			s.renderErr = errors.Join(s.renderErr, err)
			return
		}
	}

	states := s.visibleOperationStatesLocked()
	sort.Slice(states, func(left, right int) bool {
		return states[left].order < states[right].order
	})

	rows := make([]progressRow, 0, len(states))
	for _, state := range states {
		row := progressRow{
			Scope:   state.scope,
			Step:    state.label,
			Running: state.running,
			Failed:  state.failed,
		}
		if state.progress != nil {
			row.Progress = *state.progress
			row.HasProgress = true
		}
		rows = append(rows, row)
	}

	var transcript string
	if active := s.operations[s.active]; active != nil {
		transcript = active.transcript.String()
	}
	s.program.Send(progressUpdate{
		Rows:       rows,
		Transcript: transcript,
	})
}

func (s *Service) visibleOperationStatesLocked() []*operationState {
	states := make([]*operationState, 0, len(s.operations))
	for _, state := range s.operations {
		if (state.running && !s.hasRunningChildLocked(state.id)) ||
			(!state.running && state.id == s.active) {
			states = append(states, state)
		}
	}
	return states
}

func (s *Service) hasRunningChildLocked(parent OperationID) bool {
	for _, state := range s.operations {
		if state.running && state.parent == parent {
			return true
		}
	}
	return false
}

func (s *Service) enforceTranscriptLimitLocked() {
	for s.totalRetainedBytesLocked() > s.transcriptLimit {
		var candidate *operationState
		for _, state := range s.operations {
			if state.id == s.active ||
				state.transcript.Len() == 0 ||
				(candidate != nil && candidate.order <= state.order) {
				continue
			}
			candidate = state
		}
		if candidate == nil {
			active := s.operations[s.active]
			if active == nil || active.transcript.Len() == 0 {
				return
			}
			active.transcript.Omit()
			continue
		}
		candidate.transcript.Omit()
	}
}

func (s *Service) totalRetainedBytesLocked() int {
	total := s.pendingBytes
	for _, state := range s.operations {
		total += state.transcript.Len()
	}
	return total
}

func (s *Service) changePendingBytes(delta int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pendingBytes += delta
	if s.pendingBytes < 0 {
		s.pendingBytes = 0
	}
	s.enforceTranscriptLimitLocked()

	return s.totalRetainedBytesLocked() > s.transcriptLimit
}

func (s *Service) startProgramLocked() error {
	ready := make(chan struct{})
	model := newProgressModel(ready)
	program := tea.NewProgram(
		model,
		tea.WithInput(nil),
		tea.WithOutput(s.out),
		tea.WithoutSignalHandler(),
	)
	programEnd := make(chan error, 1)
	go func() {
		_, err := program.Run()
		programEnd <- err
	}()

	select {
	case <-ready:
		s.program = program
		s.programEnd = programEnd
		return nil
	case err := <-programEnd:
		return fmt.Errorf("start progress display: %w", err)
	}
}

func (s *Service) stopProgramLocked() error {
	program := s.program
	programEnd := s.programEnd
	s.program = nil
	s.programEnd = nil

	var err error
	if program != nil {
		program.Send(progressStop{})
		err = <-programEnd
		if err != nil {
			err = fmt.Errorf("stop progress display: %w", err)
		}
	}

	return err
}

func (s *Service) closeWriters() {
	s.mu.Lock()
	writers := make([]*streamWriter, 0, len(s.writers))
	for writer := range s.writers {
		writers = append(writers, writer)
	}
	s.writers = make(map[*streamWriter]struct{})
	s.mu.Unlock()

	for _, writer := range writers {
		writer.close()
	}
}

func (s *Service) closeOperationWriters(operation OperationID) {
	s.mu.Lock()
	writers := make([]*streamWriter, 0)
	for writer := range s.writers {
		if writer.operation != operation {
			continue
		}
		writers = append(writers, writer)
		delete(s.writers, writer)
	}
	s.mu.Unlock()

	for _, writer := range writers {
		writer.close()
	}
}

func (s *Service) flushActiveWriters() {
	s.mu.Lock()
	active := s.active
	writers := make([]*streamWriter, 0)
	for writer := range s.writers {
		if writer.operation == active {
			writers = append(writers, writer)
		}
	}
	s.mu.Unlock()

	for _, writer := range writers {
		writer.flush()
	}
}

func (s *Service) operationActive(operation OperationID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.started &&
		s.mode == ModeInteractive &&
		!s.handedOff &&
		s.active == operation
}

func (s *Service) captureDestination(destination io.Writer) bool {
	if s.captureAll {
		return true
	}

	uiFile, uiOK := s.out.(*os.File)
	destinationFile, destinationOK := destination.(*os.File)
	if !uiOK || !destinationOK ||
		!term.IsTerminal(int(destinationFile.Fd())) {
		return false
	}

	uiInfo, uiErr := uiFile.Stat()
	destinationInfo, destinationErr := destinationFile.Stat()
	return uiErr == nil &&
		destinationErr == nil &&
		os.SameFile(uiInfo, destinationInfo)
}
