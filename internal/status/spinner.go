package status

// The Spinner is the terminal rendering primitive behind the status Service: a
// single self-erasing line with an animated twirler. It knows nothing about what
// the status means — Service owns that — it only draws "<twirler> <text>" in
// place and erases it on Stop.

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// frames are the twirler glyphs cycled while a status is displayed.
var frames = []rune{'|', '/', '-', '\\'}

// interval is how often the twirler advances to its next frame.
const interval = 100 * time.Millisecond

// Spinner draws an animated "<twirler> <text>" line on a terminal, redrawing in
// place so the whole indicator stays on one line. The animation runs on its own
// goroutine between Start and Stop; Update swaps the text without interrupting
// the twirl. Stop erases the line and leaves the cursor at column zero so
// nothing remains once the tool takes over. All methods are safe for concurrent
// use and tolerate being called out of order.
type Spinner struct {
	w       io.Writer
	enabled bool

	mu    sync.Mutex
	text  string
	frame int
	stop  chan struct{}
	done  chan struct{}
}

// newSpinner builds a Spinner that draws to w. The spinner only animates when w
// is a terminal (e.g. os.Stderr attached to a TTY); otherwise it is permanently
// inert and every method is a no-op.
func newSpinner(w io.Writer) *Spinner {
	s := &Spinner{w: w}
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		s.enabled = true
	}
	return s
}

// Start draws text immediately and begins advancing the twirler every interval.
// It is a no-op when the spinner is inert or already running.
func (s *Spinner) Start(text string) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	s.text = text
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	s.mu.Unlock()

	s.render()
	go s.loop()
}

// Update replaces the text shown next to the twirler.
func (s *Spinner) Update(text string) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	s.text = text
	s.mu.Unlock()

	s.render()
}

// Stop halts the animation and erases the line, leaving the cursor at the start
// of a clean line. It is idempotent and safe to defer.
func (s *Spinner) Stop() {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	if s.stop == nil {
		s.mu.Unlock()
		return
	}
	close(s.stop)
	done := s.done
	s.stop = nil
	s.mu.Unlock()

	<-done
	fmt.Fprint(s.w, "\r\033[K")
}

// loop advances the twirler until Stop closes the stop channel.
func (s *Spinner) loop() {
	defer close(s.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.mu.Lock()
			s.frame++
			s.mu.Unlock()

			s.render()
		}
	}
}

// render redraws the indicator in place: \r returns to column zero and \033[K
// clears to end of line so a shorter text never leaves stale characters. It
// draws nothing once Stop has cleared the run, so no frame can land after the
// final erase.
func (s *Spinner) render() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop == nil {
		return
	}
	fmt.Fprintf(s.w, "\r\033[K%c %s", frames[s.frame%len(frames)], s.text)
}
