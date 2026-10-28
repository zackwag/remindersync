//go:build unix

// Package daemon is the loop behind `remindersync daemon`. It syncs at start,
// after Reminders changes settle, and on a timer to catch target changes,
// since targets like Skylight can't push them.
package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/zackwag/remindersync/internal/lock"
)

// ErrWatchStopped means the Reminders change feed closed, so the daemon
// would no longer hear about changes. Exiting lets launchd restart it.
var ErrWatchStopped = errors.New("daemon: Reminders change watch stopped")

// Options configures [Run].
type Options struct {
	// Changes signals that Reminders changed. Run returns ErrWatchStopped
	// when it closes.
	Changes <-chan struct{}
	// Sync syncs every pair once. It returns an error wrapping
	// lock.ErrHeld when another sync is running.
	Sync func(ctx context.Context) error
	// Poll is how long after a sync the next one runs if nothing else
	// triggers it.
	Poll time.Duration
	// Debounce is how long Reminders must stay quiet after a change before
	// it triggers a sync, so a burst of edits syncs once.
	Debounce time.Duration
	// Retry is how soon to try again when another sync held the lock.
	Retry  time.Duration
	Logger *slog.Logger
}

// Run syncs until ctx is cancelled. A sync in progress when that happens is
// allowed to finish, so it doesn't stop between a write and saving its link.
// A failed sync is logged and retried on the next trigger.
func Run(ctx context.Context, o Options) error {
	next := time.NewTimer(0)
	defer next.Stop()
	settle := time.NewTimer(o.Debounce)
	settle.Stop()
	defer settle.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-o.Changes:
			if !ok {
				return ErrWatchStopped
			}
			settle.Reset(o.Debounce)
			continue
		case <-settle.C:
		case <-next.C:
		}
		settle.Stop()

		wait := o.Poll
		err := o.Sync(context.WithoutCancel(ctx))
		switch {
		case errors.Is(err, lock.ErrHeld):
			o.Logger.Info("another sync is running; trying again shortly", "in", o.Retry)
			wait = o.Retry
		case err != nil:
			o.Logger.Error("sync failed", "err", err)
		}
		next.Reset(wait)
	}
}

// LogWriter returns a writer that prefixes each line with the time, for
// sync output going to the daemon's log file. Partial lines are held until
// their newline arrives.
func LogWriter(w io.Writer, now func() time.Time) io.Writer {
	return &logWriter{w: w, now: now}
}

type logWriter struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
	buf []byte
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := l.now().Format(time.RFC3339) + " " + string(l.buf[:i+1])
		l.buf = l.buf[i+1:]
		if _, err := io.WriteString(l.w, line); err != nil {
			return len(p), err
		}
	}
}
