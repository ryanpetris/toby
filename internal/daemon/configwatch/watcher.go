// Package configwatch keeps the daemon's view of the Toby config current. It polls
// the config source files' mtime/size and, on a change, reloads appconfig — holding
// the last-good config if a reload fails (e.g. a half-written edit). New project
// bring-ups read Current(); a reload never disturbs already-running projects, whose
// configuration is frozen at the launch that created them.
package configwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"petris.dev/toby/config"
	appconfig "petris.dev/toby/internal/config/app"
)

// sourceFiles are the config filenames Load reads, in precedence order.
var sourceFiles = []string{"config.json", "config.yaml", "config.yml"}

// defaultInterval is how often the config files are polled.
const defaultInterval = time.Second

// Watcher holds the current config and reloads it when the files change.
type Watcher struct {
	paths    config.Paths
	interval time.Duration
	current  atomic.Pointer[appconfig.Service]
	onReload atomic.Pointer[func(*appconfig.Service)]
	lastFP   string // only touched by the poll goroutine (and tests, single-threaded)
	stop     chan struct{}
}

// New loads the config once and returns a watcher around it.
func New(paths config.Paths) (*Watcher, error) {
	cfg, err := appconfig.New(paths)
	if err != nil {
		return nil, err
	}
	w := &Watcher{paths: paths, interval: defaultInterval, stop: make(chan struct{})}
	w.current.Store(cfg)
	w.lastFP = w.fingerprint()
	return w, nil
}

// Current returns the most recently loaded config.
func (w *Watcher) Current() *appconfig.Service { return w.current.Load() }

// SetOnReload installs a callback invoked with the new config after each successful
// reload (used to reconcile shared resources).
func (w *Watcher) SetOnReload(fn func(*appconfig.Service)) { w.onReload.Store(&fn) }

// watch polls the config files until stopped, reloading on any change.
func (w *Watcher) watch() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.reloadIfChanged()
		}
	}
}

// reloadIfChanged reloads the config when the source files have changed since the last
// check, keeping the last-good config if the reload fails. It reports whether a reload
// happened.
func (w *Watcher) reloadIfChanged() bool {
	fp := w.fingerprint()
	if fp == w.lastFP {
		return false
	}
	w.lastFP = fp
	cfg, err := appconfig.New(w.paths)
	if err != nil {
		// Keep the last-good config through a bad/partial edit.
		return false
	}
	w.current.Store(cfg)
	if fn := w.onReload.Load(); fn != nil {
		(*fn)(cfg)
	}
	return true
}

// fingerprint summarizes the source files' mtime and size so any edit is detected.
func (w *Watcher) fingerprint() string {
	dir := w.paths.TobyConfigDir()
	var b strings.Builder
	for _, name := range sourceFiles {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", name, info.ModTime().UnixNano(), info.Size())
		}
	}
	return b.String()
}
